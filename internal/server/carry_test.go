package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// carryYard is a yard whose server reads the packs this build embeds,
// with the operator mckean, the crew member yardmaster and one bead.
func carryYard(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(ctx) })
	st.SetPackReader(packReaderFor(home))
	if err := st.SetSetting(ctx, store.OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: home}, "mckean"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return st, home, b.ID
}

func testCarrier(st *store.Store, home, boot string) *carrier {
	return &carrier{st: st, home: home, boot: boot,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), load: embeddedPack}
}

// drain carries out everything that waits under boot, taking up what
// an earlier boot left as a starting server does.
func drain(t *testing.T, st *store.Store, home, boot string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.ResumeCarry(ctx, boot); err != nil {
		t.Fatal(err)
	}
	c := testCarrier(st, home, boot)
	for {
		did, err := c.carryNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			return
		}
	}
}

func askApprove(t *testing.T, st *store.Store, bead string, ask store.Ask) uint64 {
	t.Helper()
	ctx := context.Background()
	seq, err := st.AddAsk(ctx, bead, ask, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	// Whatever the ask is, the helper approves it as its person would
	// after seeing its weight: a pack with command files is weighty.
	if _, err := st.ApproveAsk(ctx, bead, seq, "mckean", true); err != nil {
		t.Fatal(err)
	}
	return seq
}

func packAskOf(name string) store.Ask {
	return store.Ask{Question: "Add the " + name + " source?", To: "mckean",
		Context: []string{"the yard has no source that tells a user"},
		Run:     &store.AskRun{Argv: []string{"pack apply " + name}}}
}

func resolvedLines(t *testing.T, st *store.Store, bead string, seq uint64) string {
	t.Helper()
	notes, err := st.Notes(context.Background(), bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq {
			return strings.Join(r.Lines(), "\n")
		}
	}
	t.Fatalf("no ask %d on %s", seq, bead)
	return ""
}

func carryState(t *testing.T, st *store.Store, bead string, seq uint64) *store.Carry {
	t.Helper()
	rec, err := st.CarryOf(context.Background(), bead, seq)
	if err != nil || rec == nil {
		t.Fatalf("the carrying record = %+v, %v", rec, err)
	}
	return rec
}

// An approved telegram apply is carried out once, as the operator:
// the source stands with the pack's text, each command file copied
// whole at the mode the card showed, the record done, the card ran,
// and the import's event carries the approval.
func TestCarryPackApplyTelegram(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	seq := askApprove(t, st, bead, packAskOf("telegram"))
	drain(t, st, home, "boot-one")

	src, err := st.GetSource(ctx, "telegram")
	if err != nil {
		t.Fatal(err)
	}
	reading, err := st.ReadPack(ctx, "telegram")
	if err != nil {
		t.Fatal(err)
	}
	if src.Text != reading.Sources[0].Text {
		t.Fatalf("the stored text is not the pack's")
	}
	for _, f := range reading.Sources[0].Files {
		path := filepath.Join(home, "sources", "telegram", filepath.FromSlash(f.Program))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != f.Text {
			t.Errorf("the copy of %s is not the pack's bytes", f.Program)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != source.CopyMode {
			t.Errorf("the copy of %s stands at %v, not mode %04o", f.Program, info.Mode(), source.CopyMode)
		}
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
	lines := resolvedLines(t, st, bead, seq)
	if !strings.Contains(lines, "approved, ran: yardr pack apply telegram") || !strings.Contains(lines, "declared source telegram") {
		t.Fatalf("the carried-out card reads:\n%s", lines)
	}
	evs, err := st.EventsOfKind(ctx, "pack_applied", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("pack_applied events = %v, %v", evs, err)
	}
	approved, ok := evs[0].Data["approved"].(map[string]any)
	if !ok || approved["bead"] != bead {
		t.Fatalf("the pack_applied event carries %+v", evs[0].Data["approved"])
	}
	// Carried out once: a second drain changes nothing.
	drain(t, st, home, "boot-two")
	if evs, err := st.EventsOfKind(ctx, "pack_applied", 10); err != nil || len(evs) != 1 {
		t.Fatalf("after a second drain, pack_applied events = %v, %v", evs, err)
	}
}

const looseDecl = `name: loose
base: https://loose.example.com/v1
credential: { env: LOOSE_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
limits:
  command: [loose/limits, "{provider}", --json]
  kinds: { claude: claude }
thread:
  reply: [loose/thread, reply]
`

var looseFiles = map[string]string{
	"loose/limits": "#!/bin/sh\n# limits\n",
	"loose/thread": "#!/bin/sh\n# thread\n",
}

func looseAsk() store.Ask {
	var files []store.AskFile
	for _, prog := range []string{"loose/limits", "loose/thread"} {
		sum := sha256.Sum256([]byte(looseFiles[prog]))
		files = append(files, store.AskFile{Program: prog, Text: looseFiles[prog], SHA256: hex.EncodeToString(sum[:])})
	}
	return store.Ask{Question: "Add the loose source?", To: "mckean",
		Context: []string{"its declaration is loose.yaml beside the work"},
		Run: &store.AskRun{Argv: []string{"source add loose.yaml"},
			Carried: []store.AskSource{{Name: "loose", Text: looseDecl, Files: files}}}}
}

// A loose ask that names commands has its carried bytes written and
// verified after its declaration, and a third text stored in between
// is recorded failed with nothing written.
func TestCarryLooseSourceWithCommands(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	seq := askApprove(t, st, bead, looseAsk())
	if _, err := os.Stat(filepath.Join(home, "sources", "loose")); !os.IsNotExist(err) {
		t.Fatalf("the approval wrote the command files: %v", err)
	}
	drain(t, st, home, "boot-one")
	for prog, body := range looseFiles {
		data, err := os.ReadFile(filepath.Join(home, "sources", "loose", filepath.FromSlash(prog)))
		if err != nil || string(data) != body {
			t.Fatalf("the copy of %s = %q, %v", prog, data, err)
		}
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
	if lines := resolvedLines(t, st, bead, seq); !strings.Contains(lines, "source loose saved: 1 call under https://loose.example.com/v1") {
		t.Fatalf("the carried-out card reads:\n%s", lines)
	}

	st2, home2, bead2 := carryYard(t)
	seq2 := askApprove(t, st2, bead2, looseAsk())
	other := strings.Replace(looseDecl, "https://loose.example.com/v1", "https://other.example.com", 1)
	if _, err := st2.PutSource(ctx, "loose.yaml", other, "mckean"); err != nil {
		t.Fatal(err)
	}
	drain(t, st2, home2, "boot-one")
	if rec := carryState(t, st2, bead2, seq2); rec.State != store.CarryFailed {
		t.Fatalf("a third text's record = %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(home2, "sources", "loose")); !os.IsNotExist(err) {
		t.Fatalf("a failed carrying out wrote files: %v", err)
	}
	if lines := resolvedLines(t, st2, bead2, seq2); !strings.Contains(lines, "approved, failed: yardr source add loose.yaml") {
		t.Fatalf("the failed card reads:\n%s", lines)
	}
}

// A copy moved to a third text after the approval, and an entry
// planted after it, each fail the carrying out with nothing further
// applied: the standing source is left as it stood.
func TestCarryPackStaleCopiesFail(t *testing.T) {
	ctx := context.Background()
	readingOf := func(t *testing.T, st *store.Store) *store.PackReading {
		r, err := st.ReadPack(ctx, "telegram")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	plantOld := func(t *testing.T, st *store.Store, home string) {
		r := readingOf(t, st)
		old := strings.Replace(r.Sources[0].Text, "https://api.telegram.org", "https://old.example.com", 1)
		if _, err := st.PutSource(ctx, "telegram.yaml", old, "mckean"); err != nil {
			t.Fatal(err)
		}
		for _, f := range r.Sources[0].Files {
			p := filepath.Join(home, "sources", "telegram", filepath.FromSlash(f.Program))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(f.Text), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	st, home, bead := carryYard(t)
	plantOld(t, st, home)
	seq := askApprove(t, st, bead, packAskOf("telegram"))
	victim := filepath.Join(home, "sources", "telegram", "telegram", "thread")
	if err := os.WriteFile(victim, []byte("#!/bin/sh\n# a third hand\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-one")
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryFailed || !strings.Contains(rec.Result, "telegram/thread") {
		t.Fatalf("a third copy's record = %+v", rec)
	}
	if src, err := st.GetSource(ctx, "telegram"); err != nil || !strings.Contains(src.Text, "old.example.com") {
		t.Fatalf("the standing source was applied over: %+v, %v", src, err)
	}

	st2, home2, bead2 := carryYard(t)
	plantOld(t, st2, home2)
	seq2 := askApprove(t, st2, bead2, packAskOf("telegram"))
	if err := os.WriteFile(filepath.Join(home2, "sources", "telegram", "planted.txt"), []byte("by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	drain(t, st2, home2, "boot-one")
	if rec := carryState(t, st2, bead2, seq2); rec.State != store.CarryFailed || !strings.Contains(rec.Result, "planted.txt") {
		t.Fatalf("a planted entry's record = %+v", rec)
	}
}

func TestCarryPackRejectsChangedReadingIdentity(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{"name", "kind", "label"} {
		t.Run(change, func(t *testing.T) {
			st, home, bead := carryYard(t)
			seq := askApprove(t, st, bead, packAskOf("telegram"))
			reading, err := st.ReadPack(ctx, "telegram")
			if err != nil {
				t.Fatal(err)
			}
			wasBuild := reading.Build
			if change == "name" {
				reading.Name = "different"
			} else if change == "kind" {
				reading.Kind = "depot"
			} else {
				reading.Label, reading.Build = "embedded in yardr test2", "test2"
			}
			st.SetPackReader(func(context.Context, string, store.API) (*store.PackReading, error) {
				return reading, nil
			})
			drain(t, st, home, "boot-one")
			rec := carryState(t, st, bead, seq)
			if rec.State != store.CarryFailed {
				t.Fatalf("a changed pack %s was carried out: %+v", change, rec)
			}
			if change == "label" && !strings.Contains(rec.Result,
				"was carded under yardr "+wasBuild+" and this yard now runs yardr test2, whose pack differs") {
				t.Fatalf("a changed build's refusal = %q", rec.Result)
			}
			if _, err := st.GetSource(ctx, "telegram"); err == nil {
				t.Fatal("the changed pack was applied")
			}
		})
	}
}

// The three states a stop inside a source's write leaves are each
// finished by the taken-up carrying out: the pack's text with no
// command files, with a subset of them, and — on a replacement — with
// the replaced text's files still standing.
func TestCarryPackFinishesPartialSourceWrites(t *testing.T) {
	ctx := context.Background()
	craft := func(t *testing.T, mode string) {
		st, home, bead := carryYard(t)
		r, err := st.ReadPack(ctx, "telegram")
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(home, "sources", "telegram")
		if mode == "old" {
			// A replacement: the yard's text and its files stand,
			// and the card is written against them.
			old := strings.Replace(r.Sources[0].Text, "https://api.telegram.org", "https://old.example.com", 1)
			if _, err := st.PutSource(ctx, "telegram.yaml", old, "mckean"); err != nil {
				t.Fatal(err)
			}
			for _, f := range r.Sources[0].Files {
				p := filepath.Join(dir, filepath.FromSlash(f.Program))
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("#!/bin/sh\n# the replaced text's file\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
		}
		askApprove(t, st, bead, packAskOf("telegram"))
		// The stop: the pack's text stored, the copy not done — for
		// "old", the replaced text's files still stand.
		if _, err := st.PutSource(ctx, "telegram.yaml", r.Sources[0].Text, "mckean"); err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "subset":
			f := r.Sources[0].Files[0]
			p := filepath.Join(dir, filepath.FromSlash(f.Program))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(f.Text), 0o700); err != nil {
				t.Fatal(err)
			}
		case "none":
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
		drain(t, st, home, "boot-one")
		src, err := st.GetSource(ctx, "telegram")
		if err != nil || src.Text != r.Sources[0].Text {
			t.Fatalf("%s: the finished source = %v", mode, err)
		}
		want := map[string]string{}
		for _, f := range r.Sources[0].Files {
			want[f.Program] = f.SHA256
		}
		if err := source.VerifyCopies(home, "telegram", want); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	for _, mode := range []string{"none", "subset", "old"} {
		t.Run(mode, func(t *testing.T) { craft(t, mode) })
	}
}

// A server stopped after its claim, and one stopped between approval
// and carrying out, each have the ask taken up and finished at the
// next start; a source replaced by a third text while the server was
// down is recorded failed with what stands named.
func TestCarryTakenUpAtNextStart(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	seq := askApprove(t, st, bead, packAskOf("telegram"))
	// The first server claims and stops before its first write.
	if _, err := st.ClaimCarry(ctx, "boot-one"); err != nil {
		t.Fatal(err)
	}
	// A second boot does not take a running boot's claim...
	c := testCarrier(st, home, "boot-two")
	if did, err := c.carryNext(ctx); err != nil || did {
		t.Fatalf("a running boot's claim carried by another = %v, %v", did, err)
	}
	// ...and the next start, which knows it stopped, finishes it.
	drain(t, st, home, "boot-two")
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the taken-up record = %+v", rec)
	}
	if _, err := st.GetSource(ctx, "telegram"); err != nil {
		t.Fatal(err)
	}

	st2, home2, bead2 := carryYard(t)
	seq2 := askApprove(t, st2, bead2, packAskOf("telegram"))
	other := "name: telegram\nbase: https://other.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	if _, err := st2.PutSource(ctx, "telegram.yaml", other, "mckean"); err != nil {
		t.Fatal(err)
	}
	drain(t, st2, home2, "boot-nine")
	rec := carryState(t, st2, bead2, seq2)
	if rec.State != store.CarryFailed || !strings.Contains(rec.Result, "telegram") {
		t.Fatalf("a third text while down = %+v", rec)
	}
	if src, err := st2.GetSource(ctx, "telegram"); err != nil || src.Text != other {
		t.Fatalf("the standing source was applied over: %v", err)
	}
}

// A loose ask stopped between its declaration's storing and its
// files' writing — claimed by a boot that then stopped — is taken
// up at the next start and finished.
func TestCarryLooseTakenUp(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	seq := askApprove(t, st, bead, looseAsk())
	if _, err := st.ClaimCarry(ctx, "boot-one"); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-two")
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the taken-up record = %+v", rec)
	}
	for prog, body := range looseFiles {
		data, err := os.ReadFile(filepath.Join(home, "sources", "loose", filepath.FromSlash(prog)))
		if err != nil || string(data) != body {
			t.Fatalf("the copy of %s = %q, %v", prog, data, err)
		}
	}
}

// The copies a carrying out writes stand at the mode the card
// showed, whatever mode the file they replace stood at: a replaced
// copy at 0600 is carried out to 0700 copies of the pack's bytes.
func TestCarryWritesModeTheCardShowed(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	r, err := st.ReadPack(ctx, "telegram")
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(r.Sources[0].Text, "https://api.telegram.org", "https://old.example.com", 1)
	if _, err := st.PutSource(ctx, "telegram.yaml", old, "mckean"); err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Sources[0].Files {
		p := filepath.Join(home, "sources", "telegram", filepath.FromSlash(f.Program))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.Text), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	askApprove(t, st, bead, packAskOf("telegram"))
	drain(t, st, home, "boot-one")
	for _, f := range r.Sources[0].Files {
		p := filepath.Join(home, "sources", "telegram", filepath.FromSlash(f.Program))
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != source.CopyMode {
			t.Fatalf("the carried-out copy of %s stands at %v", f.Program, info.Mode())
		}
	}
}

// A carrying out beside a terminal apply and a terminal add of the
// same source, each under the apply lock for its whole write phase,
// leaves every stored text and copied file whole and verified: the
// last of them wins entire, never a mix.
func TestCarryBesideTerminalApplyAndAdd(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	askApprove(t, st, bead, packAskOf("telegram"))
	r, err := st.ReadPack(ctx, "telegram")
	if err != nil {
		t.Fatal(err)
	}
	addText := strings.Replace(r.Sources[0].Text, "https://api.telegram.org", "https://added.example.com", 1)
	addFiles := []source.FileCopy{}
	for _, f := range r.Sources[0].Files {
		addFiles = append(addFiles, source.FileCopy{Program: f.Program, Data: []byte("#!/bin/sh\n# the terminal add's file\n")})
	}
	errs := make(chan error, 3)
	// The carrying out.
	go func() {
		c := testCarrier(st, home, "boot-one")
		_, err := c.carryNext(ctx)
		errs <- err
	}()
	// A terminal pack apply of the same pack, under the lock.
	go func() {
		release, err := config.AcquireApplyLock(home)
		if err != nil {
			errs <- err
			return
		}
		defer release()
		p, _, err := embeddedPack("telegram")
		if err != nil {
			errs <- err
			return
		}
		plan, err := pack.Build(ctx, st, p, pack.Options{Home: home})
		if err != nil {
			errs <- err
			return
		}
		_, err = pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: pack.Answers{}, Actor: "mckean"})
		errs <- err
	}()
	// A terminal source add of the same source, under the lock.
	go func() {
		release, err := config.AcquireApplyLock(home)
		if err != nil {
			errs <- err
			return
		}
		defer release()
		if _, err := st.PutSource(ctx, "telegram.yaml", addText, "mckean"); err != nil {
			errs <- err
			return
		}
		errs <- source.WriteCopies(home, "telegram", addFiles)
	}()
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	src, err := st.GetSource(ctx, "telegram")
	if err != nil {
		t.Fatal(err)
	}
	// Whichever wrote last, its text and its files stand whole.
	want := map[string]string{}
	switch src.Text {
	case r.Sources[0].Text:
		for _, f := range r.Sources[0].Files {
			want[f.Program] = f.SHA256
		}
	case addText:
		for _, f := range addFiles {
			sum := sha256.Sum256(f.Data)
			want[f.Program] = hex.EncodeToString(sum[:])
		}
	default:
		t.Fatalf("the stored text is neither the pack's nor the add's")
	}
	if err := source.VerifyCopies(home, "telegram", want); err != nil {
		t.Fatal(err)
	}
}

// A carrying out waits on the apply lock: while another holder has
// it, the ask stands claimed and nothing is written; when it lets
// go, the carrying out finishes whole.
func TestCarryWaitsOnApplyLock(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	seq := askApprove(t, st, bead, packAskOf("telegram"))
	release, err := config.AcquireApplyLock(home)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		c := testCarrier(st, home, "boot-one")
		_, err := c.carryNext(ctx)
		done <- err
	}()
	// The claim is the effect to wait for: claimed, and nothing written.
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec, err := st.CarryOf(ctx, bead, seq)
		if err == nil && rec != nil && rec.State == store.CarryClaimed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the carrying out never claimed: %+v, %v", rec, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := st.GetSource(ctx, "telegram"); err == nil {
		t.Fatal("the carrying out wrote while the lock was held")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("after the lock let go, the record = %+v", rec)
	}
	if _, err := st.GetSource(ctx, "telegram"); err != nil {
		t.Fatal(err)
	}
}

// A stop after the last object and before the record is taken up to
// a written record: the objects stand as the pack says, and the
// carrying out writes the record and finishes.
func TestCarryWritesRecordAfterStopBeforeIt(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	seq := askApprove(t, st, bead, packAskOf("pushover"))
	// The stop: the source stored (pushover names no command files),
	// no import record written.
	r, err := st.ReadPack(ctx, "pushover")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "pushover.yaml", r.Sources[0].Text, "mckean"); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-one")
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
	packs, err := st.Packs(ctx)
	if err != nil || len(packs) != 1 || packs[0].Pack != "pushover" {
		t.Fatalf("the import record = %+v, %v", packs, err)
	}
}

const miniPack = `name: mini
flows:
  default:
    stages:
      new:    { group: builders, outcomes: { done: review, question: decide }, manual: [decide] }
      review: { group: assembly, outcomes: { done: merged } }
      decide: { group: people, manual: [new] }
      merged: { end: true }
groups:
  builders: { agent: roles/builder.md, limit: 2 }
  assembly: { run: scripts/land }
  people:   { people: true }
crew:
  yardmaster:
    role: roles/yardmaster.md
`

func writeMiniPack(t *testing.T, dir, builder, yardmaster string) {
	t.Helper()
	files := map[string]string{
		"pack.yaml":           miniPack,
		"roles/builder.md":    builder,
		"roles/yardmaster.md": yardmaster,
		"scripts/land":        "#!/bin/sh\n",
	}
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// depotYard is a yard with the mini pack applied to depot yardr from
// dir, ready for a re-apply ask against dir's next contents. Its
// reader and the carrier's loader both read dir, as a server's read
// the binary it runs.
func depotYard(t *testing.T) (*store.Store, string, string, string) {
	t.Helper()
	ctx := context.Background()
	st, home, bead := carryYard(t)
	dir := t.TempDir()
	writeMiniPack(t, dir, "build v1\n", "yardmaster v1\n")
	if err := st.PutDepot(ctx, store.Depot{Name: "yardr", Path: t.TempDir()}, "mckean"); err != nil {
		t.Fatal(err)
	}
	p, err := pack.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pack.Build(ctx, st, p, pack.Options{Depot: "yardr", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	answers := pack.Answers{}
	for _, q := range plan.Questions {
		switch q.Field {
		case "kind":
			answers[q.ID] = "claude"
		case "dir":
			answers[q.ID] = home
		default:
			answers[q.ID] = ""
		}
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: answers, Actor: "mckean"}); err != nil {
		t.Fatal(err)
	}
	st.SetPackReader(func(ctx context.Context, name string, yard store.API) (*store.PackReading, error) {
		p, err := pack.LoadDir(dir)
		if err != nil {
			return nil, err
		}
		plan, err := pack.Build(ctx, yard, p, pack.Options{Depot: "yardr", Home: home})
		if err != nil {
			return nil, err
		}
		return &store.PackReading{Name: p.Name, Kind: p.Kind, Depot: "yardr",
			Label: "embedded in yardr test", Plan: askPlanOf(os.DirFS(dir), plan)}, nil
	})
	return st, home, bead, dir
}

func miniCarrier(st *store.Store, home, dir string) *carrier {
	c := testCarrier(st, home, "boot-one")
	c.load = func(name string) (*pack.Pack, fs.FS, error) {
		p, err := pack.LoadDir(dir)
		return p, os.DirFS(dir), err
	}
	return c
}

func drainMini(t *testing.T, st *store.Store, home, dir string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.ResumeCarry(ctx, "boot-one"); err != nil {
		t.Fatal(err)
	}
	c := miniCarrier(st, home, dir)
	for {
		did, err := c.carryNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			return
		}
	}
}

// A depot carrying out finishes a half-applied plan object by object:
// one changed file already installed is finishing, not staleness,
// and every carded object reads unchanged after it. One whose object
// the yard moved fails with nothing further applied.
func TestCarryDepotPack(t *testing.T) {
	st, home, bead, dir := depotYard(t)
	writeMiniPack(t, dir, "build v2\n", "yardmaster v2\n")
	seq := askApprove(t, st, bead, packAskOf("mini"))
	// Half applied: the builder's role already stands as v2.
	builder := filepath.Join(home, "packs", "mini", "roles", "builder.md")
	if err := os.WriteFile(builder, []byte("build v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drainMini(t, st, home, dir)
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the half-applied record = %+v", rec)
	}
	for name, want := range map[string]string{"builder.md": "build v2\n", "yardmaster.md": "yardmaster v2\n"} {
		data, err := os.ReadFile(filepath.Join(home, "packs", "mini", "roles", name))
		if err != nil || string(data) != want {
			t.Fatalf("the installed %s = %q, %v", name, data, err)
		}
	}
	if lines := resolvedLines(t, st, bead, seq); !strings.Contains(lines, "approved, ran: yardr pack apply mini") {
		t.Fatalf("the carried-out card reads:\n%s", lines)
	}

	st2, home2, bead2, dir2 := depotYard(t)
	writeMiniPack(t, dir2, "build v2\n", "yardmaster v2\n")
	seq2 := askApprove(t, st2, bead2, packAskOf("mini"))
	builder2 := filepath.Join(home2, "packs", "mini", "roles", "builder.md")
	if err := os.WriteFile(builder2, []byte("build by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drainMini(t, st2, home2, dir2)
	if rec := carryState(t, st2, bead2, seq2); rec.State != store.CarryFailed || !strings.Contains(rec.Result, "roles/builder.md") {
		t.Fatalf("a moved object's record = %+v", rec)
	}
	if data, err := os.ReadFile(builder2); err != nil || string(data) != "build by hand\n" {
		t.Fatalf("the moved file was applied over: %q, %v", data, err)
	}
}

// Depot packs: the holding and the verifying of a plan, an object at
// a time. A carded change that rebuilds unchanged is finishing; one
// the yard moved is staleness.
func TestDepotPlanHolding(t *testing.T) {
	carded := &store.AskPlan{Objects: []store.AskPlanObject{
		{Kind: "file", Name: "roles/a.md", Action: "change", FileOldSHA: "a", FileNewSHA: "b", Text: "new\n"},
		{Kind: "group", Name: "builders", Action: "unchanged"},
		{Kind: "file", Name: "roles/gone.md", Action: "dropped"},
	}}
	finishing := &store.AskPlan{Objects: []store.AskPlanObject{
		{Kind: "file", Name: "roles/a.md", Action: "unchanged", FileOldSHA: "b", FileNewSHA: "b", Text: "new\n"},
		{Kind: "group", Name: "builders", Action: "unchanged"},
		{Kind: "file", Name: "roles/gone.md", Action: "dropped"},
	}}
	if err := holdDepotObjects(carded, finishing); err != nil {
		t.Fatalf("a half-applied plan held: %v", err)
	}
	if err := verifyDepotObjects(carded, finishing); err != nil {
		t.Fatalf("a finished plan verified: %v", err)
	}
	moved := &store.AskPlan{Objects: []store.AskPlanObject{
		{Kind: "file", Name: "roles/a.md", Action: "change", FileOldSHA: "third", FileNewSHA: "b", Text: "new\n"},
		{Kind: "group", Name: "builders", Action: "unchanged"},
		{Kind: "file", Name: "roles/gone.md", Action: "dropped"},
	}}
	if err := holdDepotObjects(carded, moved); err == nil || !strings.Contains(err.Error(), "roles/a.md") {
		t.Fatalf("a moved file held: %v", err)
	}
	changed := &store.AskPlan{Objects: []store.AskPlanObject{
		{Kind: "file", Name: "roles/a.md", Action: "change", FileOldSHA: "a", FileNewSHA: "b", Text: "new\n"},
		{Kind: "group", Name: "builders", Action: "change"},
		{Kind: "file", Name: "roles/gone.md", Action: "dropped"},
	}}
	if err := holdDepotObjects(carded, changed); err == nil || !strings.Contains(err.Error(), "builders") {
		t.Fatalf("a moved object held: %v", err)
	}
	extra := &store.AskPlan{Objects: []store.AskPlanObject{
		{Kind: "file", Name: "roles/a.md", Action: "change", FileOldSHA: "a", FileNewSHA: "b", Text: "new\n"},
		{Kind: "group", Name: "builders", Action: "unchanged"},
		{Kind: "file", Name: "roles/gone.md", Action: "dropped"},
		{Kind: "group", Name: "reviewers", Action: "create"},
	}}
	if err := holdDepotObjects(carded, extra); err == nil || !strings.Contains(err.Error(), "reviewers") {
		t.Fatalf("an object not on the card held: %v", err)
	}
	extraAfter := &store.AskPlan{Objects: []store.AskPlanObject{
		{Kind: "file", Name: "roles/a.md", Action: "unchanged", FileOldSHA: "b", FileNewSHA: "b", Text: "new\n"},
		{Kind: "group", Name: "builders", Action: "unchanged"},
		{Kind: "file", Name: "roles/gone.md", Action: "dropped"},
		{Kind: "group", Name: "reviewers", Action: "unchanged"},
	}}
	if err := verifyDepotObjects(carded, extraAfter); err == nil || !strings.Contains(err.Error(), "reviewers") {
		t.Fatalf("an object not on the card verified: %v", err)
	}
	newAsk := &store.AskPlan{Objects: carded.Objects, Asks: []string{"let builders open threads in linear"}}
	if err := holdDepotObjects(carded, newAsk); err == nil || !strings.Contains(err.Error(), "linear") {
		t.Fatalf("a new plan ask held: %v", err)
	}
	shadowed := &store.AskPlan{Objects: []store.AskPlanObject{
		carded.Objects[0],
		{Kind: "group", Name: "builders", Action: "unchanged", Shadows: []string{".yardr/roles/builders.md"}},
		carded.Objects[2],
	}}
	if err := holdDepotObjects(carded, shadowed); err == nil || !strings.Contains(err.Error(), "builders") {
		t.Fatalf("a changed shadowing held: %v", err)
	}
}

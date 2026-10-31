package server

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

func setupAskOf(pack string, to ...store.Grantee) store.Ask {
	return store.Ask{Question: "Set up " + pack + " for the yardmaster?", To: "mckean",
		Context:   []string{"the yardmaster reads mail to work beads"},
		Recommend: 1, Why: "reading is the job",
		Setup: &store.AskSetup{Pack: pack, To: to}}
}

// askSetupApprove asks the setup of pack for to and approves it with the
// preset pick option and the typed account values, as the operator.
func askSetupApprove(t *testing.T, st *store.Store, bead, pack string, option int, values map[string]string, to ...store.Grantee) uint64 {
	t.Helper()
	ctx := context.Background()
	seq, err := st.AddAsk(ctx, bead, setupAskOf(pack, to...), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveSetup(ctx, bead, seq, option, values, "", "mckean", true); err != nil {
		t.Fatal(err)
	}
	return seq
}

func setupMailValues() map[string]string {
	return map[string]string{"GOOGLE_ACCOUNT": "ops@example.com"}
}

// grantHeld fails unless to holds exactly want of src, as a plain grant.
func grantHeld(t *testing.T, st *store.Store, src string, to store.Grantee, want ...string) {
	t.Helper()
	grants, err := st.SourceGrants(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if g.Grantee == to && g.As == "" {
			if slices.Equal(g.Calls, want) {
				return
			}
			t.Fatalf("%s holds %v of %s, want %v", to, g.Calls, src, want)
		}
	}
	t.Fatalf("no grant of %s to %s", src, to)
}

// An approved setup is carried out once, as the operator: the pack
// applied with its files copied at the mode the card showed, the account
// written to the carded file, and the picked preset's calls granted —
// each verified back, the record done and the card ran.
func TestCarrySetupGoogleMail(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	seq := askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(), to)
	drain(t, st, home, "boot-one")

	src, err := st.GetSource(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	reading, err := st.ReadPack(ctx, "google-mail", "", store.Mappings{})
	if err != nil {
		t.Fatal(err)
	}
	if src.Text != reading.Sources[0].Text {
		t.Fatalf("the stored text is not the pack's")
	}
	for _, f := range reading.Sources[0].Files {
		path := filepath.Join(home, "sources", "google-mail", filepath.FromSlash(f.Program))
		data, err := os.ReadFile(path)
		if err != nil || string(data) != f.Text {
			t.Fatalf("the copy of %s = %q, %v", f.Program, data, err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != source.CopyMode {
			t.Fatalf("the copy of %s stands at %v, not mode %04o", f.Program, info.Mode(), source.CopyMode)
		}
	}
	envFile := filepath.Join(home, "sources", "google-mail.env")
	f, err := store.ReadEnvFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := source.Lookup(f.Env, "GOOGLE_ACCOUNT"); got != "ops@example.com" {
		t.Fatalf("the env file holds GOOGLE_ACCOUNT=%q", got)
	}
	grantHeld(t, st, "google-mail", to, "read")
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
	lines := resolvedLines(t, st, bead, seq)
	for _, want := range []string{
		"approved, ran: setup of pack google-mail, preset read to crew yardmaster; gave GOOGLE_ACCOUNT",
		"applied pack google-mail to the yard",
		"declared source google-mail",
		"the account GOOGLE_ACCOUNT is set in " + envFile,
		"may call google-mail",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("the carried-out card has no %q:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "still owed") {
		t.Fatalf("the carried-out card still owes:\n%s", lines)
	}
	// Carried out once: a second drain changes nothing.
	drain(t, st, home, "boot-two")
	if evs, err := st.EventsOfKind(ctx, "pack_applied", 10); err != nil || len(evs) != 1 {
		t.Fatalf("after a second drain, pack_applied events = %v, %v", evs, err)
	}
	if evs, err := st.EventsOfKind(ctx, "source_granted", 10); err != nil || len(evs) != 1 {
		t.Fatalf("after a second drain, source_granted events = %v, %v", evs, err)
	}
}

// A setup that chose a region base sets it on each carried source that
// declares bases, as the operator, and says where its calls go. A setup
// that chose none sets nothing, and a carried source without bases is
// left out.
func TestCarrySetupBase(t *testing.T) {
	ctx := context.Background()
	st, home, _ := carryYard(t)
	text := `name: widget
base: https://widget.example.com
bases:
  us: https://widget.example.com
  eu: https://eu.widget.example.com
credential: { env: WIDGET_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`
	if _, err := st.PutSource(ctx, "widget.yaml", text, "mckean"); err != nil {
		t.Fatal(err)
	}
	plain := `name: plain
base: https://plain.example.com
credential: { env: PLAIN_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`
	if _, err := st.PutSource(ctx, "plain.yaml", plain, "mckean"); err != nil {
		t.Fatal(err)
	}
	c := testCarrier(st, home, "boot-one")
	rec := &store.Carry{Bead: "yardr-x", Ask: 1, Actor: "mckean"}
	setup := &store.AskSetup{Pack: "widget", Base: "eu", Carried: []store.AskSource{{Name: "widget", Text: text}, {Name: "plain", Text: plain}}}
	lines, failure := c.carrySetupBase(ctx, rec, setup)
	if failure != "" {
		t.Fatal(failure)
	}
	if want := "source widget: calls go to https://eu.widget.example.com (base eu)"; len(lines) != 1 || lines[0] != want {
		t.Fatalf("lines = %q, want only %q", lines, want)
	}
	if src, err := st.GetSource(ctx, "widget"); err != nil || src.Base != "eu" {
		t.Fatalf("the source after the base step = %+v, %v", src, err)
	}
	if src, err := st.GetSource(ctx, "plain"); err != nil || src.Base != "" {
		t.Fatalf("the source without bases after the base step = %+v, %v", src, err)
	}
	setup = &store.AskSetup{Pack: "widget", Carried: []store.AskSource{{Name: "widget", Text: text}}}
	if lines, failure := c.carrySetupBase(ctx, rec, setup); failure != "" || len(lines) != 0 {
		t.Fatalf("with no base chosen: %q, %q", lines, failure)
	}
}

// A setup approved under one build is carried out by a server that runs
// another, the pack the same file for file: the apply step holds the reading
// to the card's bytes and to its label without the build, and the build the
// card kept is provenance (carryPackApply).
func TestCarrySetupRunsPastAMovedBuild(t *testing.T) {
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	seq := askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(), to)
	read := packReaderFor(home)
	st.SetPackReader(func(ctx context.Context, name string, depot string, mappings store.Mappings, yard store.API) (*store.PackReading, error) {
		reading, err := read(ctx, name, depot, mappings, yard)
		if err == nil {
			reading.Label, reading.Build = "embedded in yardr test2", "test2"
		}
		return reading, err
	})
	drain(t, st, home, "boot-one")
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
	grantHeld(t, st, "google-mail", to, "read")
}

// A setup whose pack no longer holds what the card showed fails its apply
// step with the pack's finishing command: nothing further is written, no
// account and no grants.
func TestCarrySetupApplyFailureStopsRest(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	seq := askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(), store.Grantee{Crew: "yardmaster"})
	reading, err := st.ReadPack(ctx, "google-mail", "", store.Mappings{})
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(reading.Sources[0].Text, "https://gmail.googleapis.com", "https://other.example.com", 1)
	if _, err := st.PutSource(ctx, "google-mail.yaml", other, "mckean"); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-one")

	rec := carryState(t, st, bead, seq)
	if rec.State != store.CarryFailed || !strings.Contains(rec.Result, "yardr pack apply google-mail") {
		t.Fatalf("the record = %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(home, "sources", "google-mail.env")); !os.IsNotExist(err) {
		t.Fatalf("a failed apply wrote the account: %v", err)
	}
	if grants, err := st.SourceGrants(ctx, "google-mail"); err != nil || len(grants) != 0 {
		t.Fatalf("a failed apply granted: %+v, %v", grants, err)
	}
	if lines := resolvedLines(t, st, bead, seq); !strings.Contains(lines, "approved, failed: setup of pack google-mail, preset read to crew yardmaster; gave GOOGLE_ACCOUNT") {
		t.Fatalf("the failed card reads:\n%s", lines)
	}
}

// A setup whose account cannot be written fails its account step with what
// stands — the apply — and the key command that finishes it: no grants run.
func TestCarrySetupAccountFailureStopsGrants(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	reading, err := st.ReadPack(ctx, "google-mail", "", store.Mappings{})
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(reading.Sources[0].Text, "https://gmail.googleapis.com", "https://old.example.com", 1)
	if _, err := st.PutSource(ctx, "google-mail.yaml", old, "mckean"); err != nil {
		t.Fatal(err)
	}
	// The account's file is a directory: the secret path's write fails.
	dir := filepath.Join(home, "sources", "google-mail.env")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "google-mail", store.SourceEnvFile, dir, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(), store.Grantee{Crew: "yardmaster"})
	drain(t, st, home, "boot-one")

	rec := carryState(t, st, bead, seq)
	for _, want := range []string{
		"pack google-mail is applied",
		"yardr source key google-mail GOOGLE_ACCOUNT",
		"yardr source grant google-mail read --crew yardmaster",
	} {
		if rec.State != store.CarryFailed || !strings.Contains(rec.Result, want) {
			t.Fatalf("the record has no %q: %+v", want, rec)
		}
	}
	if src, err := st.GetSource(ctx, "google-mail"); err != nil || src.Text != reading.Sources[0].Text {
		t.Fatalf("the apply did not stand: %+v, %v", src, err)
	}
	if grants, err := st.SourceGrants(ctx, "google-mail"); err != nil || len(grants) != 0 {
		t.Fatalf("a failed account step granted: %+v, %v", grants, err)
	}
}

// A setup whose grants cannot run fails its grants step with what stands —
// the apply and the account — and the grant commands that finish it.
func TestCarrySetupGrantsFailureStopsRest(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(),
		store.Grantee{Crew: "yardmaster"}, store.Grantee{Group: "builders"})
	if _, err := st.DeleteGroup(ctx, "builders", "mckean"); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-one")

	rec := carryState(t, st, bead, seq)
	for _, want := range []string{
		"pack google-mail is applied and the account GOOGLE_ACCOUNT is set in",
		"builders",
		"yardr source grant google-mail read --group builders",
		"yardr source grant google-mail read --crew yardmaster",
	} {
		if rec.State != store.CarryFailed || !strings.Contains(rec.Result, want) {
			t.Fatalf("the record has no %q: %+v", want, rec)
		}
	}
	f, err := store.ReadEnvFile(filepath.Join(home, "sources", "google-mail.env"))
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "ops@example.com" {
		t.Fatalf("the account did not stand: %+v, %v", f, err)
	}
	if grants, err := st.SourceGrants(ctx, "google-mail"); err != nil || len(grants) != 0 {
		t.Fatalf("a failed grants step granted: %+v, %v", grants, err)
	}
}

// A server stopped after its setup claim, and two takers of one setup:
// the second takes nothing while the first runs, and the next start
// finishes the ask whole.
func TestCarrySetupTakenUpAtNextStart(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	seq := askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(), to)
	// The first server claims and stops before its first write.
	if _, err := st.ClaimCarry(ctx, "boot-one", time.Now()); err != nil {
		t.Fatal(err)
	}
	// A second boot does not take a running boot's claim.
	c := testCarrier(st, home, "boot-two")
	if did, err := c.carryNext(ctx); err != nil || did {
		t.Fatalf("a running boot's setup carried by another = %v, %v", did, err)
	}
	// The next start, which knows it stopped, finishes it.
	drain(t, st, home, "boot-two")
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the taken-up record = %+v", rec)
	}
	if _, err := st.GetSource(ctx, "google-mail"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ReadEnvFile(filepath.Join(home, "sources", "google-mail.env"))
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "ops@example.com" {
		t.Fatalf("the taken-up account = %+v, %v", f, err)
	}
	grantHeld(t, st, "google-mail", to, "read")
}

// A stop before the account's write loses the typed value, which lives in
// the server's memory alone: the ask is recorded failed with the apply
// standing and the account asked again.
func TestCarrySetupLostValuesFailsAskedAgain(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	seq := askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(), store.Grantee{Crew: "yardmaster"})
	// The stop: the handed values are lost with the server's memory.
	if _, ok := st.TakeSetupSecret(bead, seq); !ok {
		t.Fatal("the approval handed no values to lose")
	}
	drain(t, st, home, "boot-one")

	rec := carryState(t, st, bead, seq)
	for _, want := range []string{
		"pack google-mail is applied",
		"Ask for it again",
		"GOOGLE_ACCOUNT",
		"--secret google-mail",
		"yardr source key google-mail GOOGLE_ACCOUNT",
		"yardr source grant google-mail read --crew yardmaster",
	} {
		if rec.State != store.CarryFailed || !strings.Contains(rec.Result, want) {
			t.Fatalf("the record has no %q: %+v", want, rec)
		}
	}
	reading, err := st.ReadPack(ctx, "google-mail", "", store.Mappings{})
	if err != nil {
		t.Fatal(err)
	}
	if src, err := st.GetSource(ctx, "google-mail"); err != nil || src.Text != reading.Sources[0].Text {
		t.Fatalf("the apply did not stand: %+v, %v", src, err)
	}
	if _, err := os.Stat(filepath.Join(home, "sources", "google-mail.env")); !os.IsNotExist(err) {
		t.Fatalf("a lost value wrote the account: %v", err)
	}
	if grants, err := st.SourceGrants(ctx, "google-mail"); err != nil || len(grants) != 0 {
		t.Fatalf("a lost value granted: %+v, %v", grants, err)
	}
}

// A file that already held the account before approval is not proof that
// the approved value was written: after a stop before the account step,
// the setup asks for the account again and grants nothing.
func TestCarrySetupLostValuesDoesNotReuseExistingAccount(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	reading, err := st.ReadPack(ctx, "google-mail", "", store.Mappings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "google-mail.yaml", reading.Sources[0].Text, "mckean"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "sources", "google-mail.env")
	if _, err := st.SetSource(ctx, "google-mail", store.SourceEnvFile, file, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.WriteEnvFile(file, map[string]string{"GOOGLE_ACCOUNT": "old@example.com"}); err != nil {
		t.Fatal(err)
	}
	seq := askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(), store.Grantee{Crew: "yardmaster"})
	if _, ok := st.TakeSetupSecret(bead, seq); !ok {
		t.Fatal("the approval handed no values to lose")
	}
	drain(t, st, home, "boot-one")

	rec := carryState(t, st, bead, seq)
	if rec.State != store.CarryFailed || !strings.Contains(rec.Result, "Ask for it again") || !strings.Contains(rec.Result, "yardr source key google-mail GOOGLE_ACCOUNT") {
		t.Fatalf("the record reused an older account: %+v", rec)
	}
	f, err := store.ReadEnvFile(file)
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "old@example.com" {
		t.Fatalf("the existing account changed: %+v, %v", f, err)
	}
	if grants, err := st.SourceGrants(ctx, "google-mail"); err != nil || len(grants) != 0 {
		t.Fatalf("the lost account granted: %+v, %v", grants, err)
	}
}

// A stop after the account's write is taken up past it: the values were
// taken once, the file holds every name, and the carrying out grants.
func TestCarrySetupResumedAfterAccountWrite(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	reading, err := st.ReadPack(ctx, "google-mail", "", store.Mappings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "google-mail.yaml", reading.Sources[0].Text, "mckean"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "sources", "google-mail.env")
	if _, err := st.SetSource(ctx, "google-mail", store.SourceEnvFile, file, "mckean"); err != nil {
		t.Fatal(err)
	}
	to := store.Grantee{Crew: "yardmaster"}
	seq := askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(), to)
	claimed, err := st.ClaimCarry(ctx, "boot-one", time.Now())
	if err != nil || claimed == nil || claimed.Ask != seq {
		t.Fatalf("the claim = %+v, %v", claimed, err)
	}
	c := testCarrier(st, home, "boot-one")
	setup, _, err := c.askSetup(ctx, claimed)
	if err != nil {
		t.Fatal(err)
	}
	if _, failure := c.carrySetupApply(ctx, claimed, setup); failure != "" {
		t.Fatal(failure)
	}
	// The carrying out's take and verified account write, before the stop.
	taken, ok := st.TakeSetupSecret(bead, seq)
	if !ok {
		t.Fatal("the approval handed no values to write")
	}
	if _, err := st.KeySourceForSetup(ctx, bead, seq, "boot-two", "google-mail", taken, "mckean"); err == nil || !strings.Contains(err.Error(), "not claimed by this server") {
		t.Fatalf("a different boot wrote the account: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("a different boot left an account file: %v", err)
	}
	if _, err := st.KeySourceForSetup(ctx, bead, seq, claimed.Boot, "google-mail", taken, "mckean"); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-two")

	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the resumed record = %+v", rec)
	}
	grantHeld(t, st, "google-mail", to, "read")
	if lines := resolvedLines(t, st, bead, seq); !strings.Contains(lines, "the account GOOGLE_ACCOUNT is set in "+file) {
		t.Fatalf("the resumed card reads:\n%s", lines)
	}
}

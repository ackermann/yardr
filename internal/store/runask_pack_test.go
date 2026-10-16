package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/source"
)

// packReaderOf returns a reader of one fake embedded pack, wired, whose
// label and files the test moves between asking and approving.
type fakePack struct {
	label string
	files map[string]string
	plan  *AskPlan
	kind  string
	depot string
}

func (f *fakePack) reading() *PackReading {
	kind := f.kind
	if kind == "" {
		kind = "source"
	}
	files := f.files
	if files == nil {
		files = wiredFiles
	}
	// In the declaration's command order, as a server reading builds it.
	var srcs []AskSource
	_ = srcs
	return &PackReading{Name: "wired", Kind: kind, Depot: f.depot, Label: f.label,
		Sources: []AskSource{{Name: "wired", Text: wiredDecl, Files: askFilesOrdered(files)}}, Plan: f.plan}
}

func askFilesOrdered(files map[string]string) []AskFile {
	var out []AskFile
	for _, prog := range []string{"wired/limits", "wired/thread", "wired/normalize", "wired/events"} {
		body := files[prog]
		sum := sha256.Sum256([]byte(body))
		out = append(out, AskFile{Program: prog, Text: body, SHA256: hex.EncodeToString(sum[:])})
	}
	return out
}

func (y *askYard) setPack(f *fakePack) {
	y.Store.SetPackReader(func(ctx context.Context, name string, yard API) (*PackReading, error) {
		if name != "wired" && name != "starter" {
			return nil, errFakeNoPack(name)
		}
		r := f.reading()
		r.Name = name
		return r, nil
	})
}

func errFakeNoPack(name string) error {
	return &fakeNoPack{name}
}

type fakeNoPack struct{ name string }

func (e *fakeNoPack) Error() string {
	return "pack " + e.name + ": yardr embeds no pack of that name: it is the operator's to apply in a terminal (yardr pack check " + e.name + ")"
}

func packAsk() Ask {
	return Ask{Question: "Add the wired source from its pack?", To: "mckean",
		Context: []string{"the yard has no source that speaks in threads"},
		Run:     &AskRun{Argv: []string{"pack apply wired"}}}
}

// A pack ask is stored with the server's own bytes, whatever the writer
// sent for them, and its approval records it to be carried out.
func TestPackApplyAskStoresServerBytes(t *testing.T) {
	y, _ := runYard(t)
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	ask := packAsk()
	ask.Run.Carried = []AskSource{{Name: "wired", Text: "name: forged\n", Files: []AskFile{{Program: "x", Text: "forged", SHA256: "0"}}}}
	ask.Run.Does = []string{"a small apply, nothing to read here"}
	ask.Run.Seen = map[string]string{"pack wired": "forged"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if got.Ask.Run == nil || got.Ask.Run.Pack == nil || got.Ask.Run.Pack.Label != "embedded in yardr test1" {
		t.Fatalf("the stored ask's pack = %+v", got.Ask.Run)
	}
	if len(got.Ask.Run.Carried) != 1 || got.Ask.Run.Carried[0].Text != wiredDecl {
		t.Fatalf("the stored ask carries %+v, want the server's declaration", got.Ask.Run.Carried)
	}
	joined := strings.Join(got.Ask.Run.Does, "\n")
	for _, want := range []string{
		"pack wired (embedded in yardr test1), of kind source, into the yard: it adds 1 source",
		"source wired: its calls go to https://wired.example.com/v1",
		"call ping (read): GET /me",
		"command file wired/thread:",
		"still owed after it: the credential, by a secret ask for wired",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "nothing to read here") || strings.Contains(joined, "forged") {
		t.Errorf("the writer's words stood:\n%s", joined)
	}
	if got.Ask.Run.Description != "Import a pack into the yard: set the yard to what the pack says" {
		t.Errorf("the card's description = %q", got.Ask.Run.Description)
	}
	// The queue and the bead carry the yard's sentences whole.
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil || len(q.Open) != 1 || q.Open[0].Run == nil || q.Open[0].Run.Pack == nil ||
		!strings.Contains(strings.Join(q.Open[0].Run.Does, "\n"), "pack wired (embedded in yardr test1)") {
		t.Fatalf("the queue's row = %+v, %v", q, err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "approved, to be carried out" {
		t.Fatalf("the approval recorded %q", done.Result)
	}
	if _, err := y.GetSource(y.ctx, "wired"); err == nil {
		t.Fatalf("a pack approval applied the pack in its write: carrying out is the server's after it")
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil || !strings.Contains(err.Error(), "was approved already") {
		t.Fatalf("a second approval = %v", err)
	}
}

// A pack ask of a store with no reading registered is refused naming
// the server, and flags and other names are refused to a terminal.
func TestPackApplyRefusals(t *testing.T) {
	y, _ := runYard(t)
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "no server has registered") {
		t.Fatalf("a pack ask with no reading = %v", err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	for _, c := range []struct{ argv, want string }{
		{"pack apply wired --yes", "pack apply with --yes"},
		{"pack apply wired --depot yardr", "pack apply with --depot"},
		{"pack apply wired extra", "one pack's name"},
		{"pack apply nosuch", "yardr embeds no pack of that name"},
	} {
		ask := packAsk()
		ask.Run.Argv = []string{c.argv}
		if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: AddAsk = %v, want %q", c.argv, err, c.want)
		}
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
}

// An approval is refused when the build the pack is embedded in moved:
// the label alone says the pack is unchanged but carded from another
// build, and changed bytes say the pack differs.
func TestPackApplyStaleBuild(t *testing.T) {
	y, _ := runYard(t)
	f := &fakePack{label: "embedded in yardr one"}
	y.setPack(f)
	seq := y.ask(t, packAsk(), "yardmaster")
	f.label = "embedded in yardr two"
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "the pack is unchanged file for file, but the card named the build") {
		t.Fatalf("a moved label: ApproveAsk = %v", err)
	}
	seq2 := y.ask(t, packAsk(), "yardmaster")
	changed := map[string]string{}
	for k, v := range wiredFiles {
		changed[k] = v
	}
	changed["wired/thread"] = "#!/bin/sh\n# thread, changed in the new build\n"
	f.files = changed
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq2, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "whose pack differs") {
		t.Fatalf("changed bytes: ApproveAsk = %v", err)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after refused approvals", r.State)
	}
	if answers := answerNotes(t, y); len(answers) != 0 {
		t.Fatalf("refused approvals recorded %d answers", len(answers))
	}
}

// plantWired puts the wired source in the yard with its copies on disk
// and one entry neither declaration names.
func plantWired(t *testing.T, y *askYard) string {
	t.Helper()
	if _, err := y.PutSource(y.ctx, "wired.yaml", wiredDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(y.Store.home, "sources", "wired")
	for prog, body := range wiredFiles {
		p := filepath.Join(dir, filepath.FromSlash(prog))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("left by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A pack that would replace a source cards each command file's change
// with its hashes whole and its diff, and names each entry neither
// declaration names as removed.
func TestPackApplyReplacementCard(t *testing.T) {
	y, _ := runYard(t)
	dir := plantWired(t, y)
	// The standing copy of wired/thread differs from the pack's.
	if err := os.WriteFile(filepath.Join(dir, "wired", "thread"), []byte("#!/bin/sh\n# thread, as the yard has it\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	got := y.asked(t, seq)
	carried := got.Ask.Run.Carried[0]
	var diff *AskFileDiff
	for i := range carried.Old {
		if carried.Old[i].Program == "wired/thread" {
			diff = &carried.Old[i]
		}
	}
	if diff == nil || diff.OldSHA256 == diff.NewSHA256 || !strings.Contains(diff.Diff, "-# thread, as the yard has it") || !strings.Contains(diff.Diff, "+# thread") {
		t.Fatalf("the stored change = %+v", carried.Old)
	}
	if len(carried.Removed) != 1 || carried.Removed[0].Path != "extra.txt" || carried.Removed[0].Text != "left by hand\n" {
		t.Fatalf("the stored removed entries = %+v", carried.Removed)
	}
	joined := strings.Join(got.Ask.Run.Does, "\n")
	for _, want := range []string{
		"it would replace the source wired the yard has under that name.",
		"command file wired/thread would change: sha256 " + diff.OldSHA256 + " -> sha256 " + diff.NewSHA256,
		"the copy would remove extra.txt, which neither declaration names: sha256 ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	lines := strings.Join(got.Lines(), "\n")
	if !strings.Contains(lines, "-# thread, as the yard has it") {
		t.Errorf("the terminal card lacks the diff whole:\n%s", lines)
	}
}

func TestPackApplyCarriesRemovedTextUpToThePackPayloadBound(t *testing.T) {
	y, _ := runYard(t)
	dir := plantWired(t, y)
	text := strings.Repeat("a removed line\n", 800) // larger than the loose-file limit
	if err := os.WriteFile(filepath.Join(dir, "large-extra.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	got := y.asked(t, seq)
	for _, rem := range got.Ask.Run.Carried[0].Removed {
		if rem.Path == "large-extra.txt" {
			if rem.Text != text {
				t.Fatalf("carried removed text has %d bytes, want %d", len(rem.Text), len(text))
			}
			return
		}
	}
	t.Fatalf("the pack card omitted the removed text: %+v", got.Ask.Run.Carried[0].Removed)
}

func TestPackReplacementDiffShowsWholeDeclarationKeys(t *testing.T) {
	oldCalls, err := source.Parse("old", []byte(tgDecl))
	if err != nil {
		t.Fatal(err)
	}
	newText := strings.Replace(tgDecl, `text: "hi {text}"`, `text: "hello {text}"`, 1)
	newCalls, err := source.Parse("new", []byte(newText))
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(packDeclDiffLines("tg", oldCalls, newCalls), "\n")
	for _, want := range []string{`"text":"hi {text}"`, `"text":"hello {text}"`, `"chat_id":"{chat}"`} {
		if !strings.Contains(calls, want) {
			t.Errorf("the calls change omits %q:\n%s", want, calls)
		}
	}

	oldHook, err := source.Parse("old", []byte(wiredDecl))
	if err != nil {
		t.Fatal(err)
	}
	newText = strings.Replace(wiredDecl, "X-Wired-Secret", "X-New-Secret", 1)
	newHook, err := source.Parse("new", []byte(newText))
	if err != nil {
		t.Fatal(err)
	}
	hook := strings.Join(packDeclDiffLines("wired", oldHook, newHook), "\n")
	if !strings.Contains(hook, "X-Wired-Secret") || !strings.Contains(hook, "X-New-Secret") {
		t.Errorf("the hook change omits its old or new verification header:\n%s", hook)
	}
}

// A chmod of a command copy after the ask makes the approval stale, and
// so does a changed copy and a changed entry neither declaration names:
// the refusal names the file that moved.
func TestPackApplyStaleCopy(t *testing.T) {
	y, _ := runYard(t)
	dir := plantWired(t, y)
	y.setPack(&fakePack{label: "embedded in yardr test1"})

	seq := y.ask(t, packAsk(), "yardmaster")
	if err := os.Chmod(filepath.Join(dir, "wired", "thread"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "wired/thread") || !strings.Contains(err.Error(), "chmod") {
		t.Fatalf("a chmod after the ask: ApproveAsk = %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "wired", "thread"), 0o700); err != nil {
		t.Fatal(err)
	}

	seq = y.ask(t, packAsk(), "yardmaster")
	if err := os.WriteFile(filepath.Join(dir, "wired", "normalize"), []byte("#!/bin/sh\n# changed by hand\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "wired/normalize") {
		t.Fatalf("a changed copy after the ask: ApproveAsk = %v", err)
	}

	seq = y.ask(t, packAsk(), "yardmaster")
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("changed by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "extra.txt") {
		t.Fatalf("a changed removed entry after the ask: ApproveAsk = %v", err)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after refused approvals", r.State)
	}
}

func TestPackApplyReplacementCardsSymlinksWithoutFollowingThem(t *testing.T) {
	y, _ := runYard(t)
	dir := plantWired(t, y)
	target := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(target, []byte("outside secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "outside.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	got := y.asked(t, seq)
	var removed *AskRemoved
	for i := range got.Ask.Run.Carried[0].Removed {
		if got.Ask.Run.Carried[0].Removed[i].Path == "outside.txt" {
			removed = &got.Ask.Run.Carried[0].Removed[i]
		}
	}
	if removed == nil || !removed.Symlink || removed.Text != "" {
		t.Fatalf("the symbolic link inventory = %+v", got.Ask.Run.Carried[0].Removed)
	}
	if strings.Contains(strings.Join(got.Lines(), "\n"), "outside secret") {
		t.Fatalf("the card read the link target outside the source copies:\n%s", strings.Join(got.Lines(), "\n"))
	}

	other := filepath.Join(t.TempDir(), "other.txt")
	if err := os.WriteFile(other, []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil || !strings.Contains(err.Error(), "outside.txt") {
		t.Fatalf("a moved symbolic link: ApproveAsk = %v", err)
	}
}

func TestPackApplyRefusesChangedCommandSymlink(t *testing.T) {
	y, _ := runYard(t)
	dir := plantWired(t, y)
	target := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(dir, "wired", "thread")
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, command); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("a command copy symbolic link = %v", err)
	}
}

// A pending source set and a pending source grant go stale when a
// command copy of their source changes after the ask.
func TestSourceRowsStaleWhenCopyChanges(t *testing.T) {
	y, _ := runYard(t)
	dir := plantWired(t, y)
	if err := y.PutGroup(y.ctx, Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	set := Ask{Question: "More calls?", To: "mckean", Context: []string{"c"},
		Run: &AskRun{Argv: []string{"source set wired calls_per_hour=200"}}}
	seq := y.ask(t, set, "yardmaster")
	grant := Ask{Question: "Grant it?", To: "mckean", Context: []string{"c"},
		Run: &AskRun{Argv: []string{"source grant wired read --group builders"}}}
	gseq := y.ask(t, grant, "yardmaster")
	if err := os.WriteFile(filepath.Join(dir, "wired", "thread"), []byte("#!/bin/sh\n# changed by hand\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil {
		t.Fatalf("a source set approved over a changed copy")
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, gseq, "mckean"); err == nil {
		t.Fatalf("a source grant approved over a changed copy")
	}
}

// A depot pack ask stores the server-built plan with each changed
// file's diff, throws a forged plan away, and its approval is held to
// the plan rebuilt.
func TestPackApplyDepotCardAndStale(t *testing.T) {
	y, _ := runYard(t)
	diff := "--- was\n+++ is\n@@ -1,1 +1,1 @@\n-old role\n+new role\n"
	plan := &AskPlan{
		Objects: []AskPlanObject{
			{Kind: "file", Name: "roles/a.md", Action: "change", FileOldSHA: strings.Repeat("a", 64), FileNewSHA: strings.Repeat("b", 64), Text: "new role\n", Diff: diff},
			{Kind: "group", Name: "builders", Action: "create", Changes: []AskChange{{Field: "runner", Old: "", New: "exec"}}},
			{Kind: "file", Name: "roles/old.md", Action: "dropped"},
		},
		Governs: []AskGovern{{Flow: "default", Now: "other"}},
	}
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "yardr", plan: plan}
	y.setPack(f)
	ask := packAsk()
	ask.Run.Argv = []string{"pack apply starter"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if got.Ask.Run.Pack == nil || got.Ask.Run.Pack.Plan == nil || len(got.Ask.Run.Pack.Plan.Objects) != 3 {
		t.Fatalf("the stored plan = %+v", got.Ask.Run.Pack)
	}
	joined := strings.Join(got.Ask.Run.Does, "\n")
	for _, want := range []string{
		"pack starter (embedded in yardr test1), of kind git, into depot yardr: 1 to create, 1 to change, 0 unchanged, and 1 dropped",
		"file roles/a.md: change, sha256 " + strings.Repeat("a", 64) + " -> sha256 " + strings.Repeat("b", 64),
		"group builders: create (runner: none -> exec)",
		"file roles/old.md: dropped by the pack, left where it stands.",
		"depot yardr would follow flow default; it follows other now.",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if got.Ask.Run.Description != "Import a pack into depot yardr: set the yard to what the pack says" {
		t.Errorf("the card's description = %q", got.Ask.Run.Description)
	}
	if lines := strings.Join(got.Lines(), "\n"); !strings.Contains(lines, "-old role") || !strings.Contains(lines, "+new role") {
		t.Errorf("the terminal card lacks the file's diff whole:\n%s", lines)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
	if err != nil || done.Result != "approved, to be carried out" {
		t.Fatalf("the approval = %+v, %v", done, err)
	}
	// A plan that moves under a second ask stales its approval.
	seq2 := y.ask(t, ask, "yardmaster")
	plan.Objects[1].Changes[0].New = "herdr"
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq2, "mckean"); err == nil || !strings.Contains(err.Error(), "the plan") {
		t.Fatalf("a moved plan: ApproveAsk = %v", err)
	}
}

// A depot ask is refused where the plan asks, where there is no single
// depot, and where its card or payload would run past the bound.
func TestPackApplyDepotRefusals(t *testing.T) {
	y, _ := runYard(t)
	ask := packAsk()
	ask.Run.Argv = []string{"pack apply starter"}
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "yardr",
		plan: &AskPlan{Missing: []string{"group.building.agent: which agent?"}}}
	y.setPack(f)
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "yardr pack check starter --depot yardr") {
		t.Fatalf("a plan that asks: AddAsk = %v", err)
	}
	f.plan, f.depot = &AskPlan{}, ""
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "no single depot") {
		t.Fatalf("no single depot: AddAsk = %v", err)
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
}

// The store refuses a pack source whose parse names another source, a
// card past forty lines, and a payload past the bound.
func TestPackApplyStoreRefusals(t *testing.T) {
	y, _ := runYard(t)
	y.Store.SetPackReader(func(ctx context.Context, name string, yard API) (*PackReading, error) {
		return &PackReading{Name: name, Kind: "source", Label: "embedded in yardr test1",
			Sources: []AskSource{{Name: "other", Text: wiredDecl}}}, nil
	})
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "its declaration names wired") {
		t.Fatalf("a carried name the parse does not give: AddAsk = %v", err)
	}
	// Past the line bound: many sources, each with its own card lines.
	y.Store.SetPackReader(func(ctx context.Context, name string, yard API) (*PackReading, error) {
		var srcs []AskSource
		for i := 0; i < 6; i++ {
			text := strings.Replace(wiredDecl, "name: wired", "name: wiredx", 1)
			text = strings.Replace(text, "wiredx", "w"+strings.Repeat("x", i+1), 1)
			srcs = append(srcs, AskSource{Name: "w" + strings.Repeat("x", i+1), Text: text, Files: askFiles(wiredFiles)})
		}
		return &PackReading{Name: name, Kind: "source", Label: "embedded in yardr test1", Sources: srcs}, nil
	})
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "card would run to") {
		t.Fatalf("a card past the bound: AddAsk = %v", err)
	}
	// Past the payload bound: one command file too large together.
	big := map[string]string{}
	for k := range wiredFiles {
		big[k] = wiredFiles[k]
	}
	big["wired/limits"] = strings.Repeat("a", 70000)
	y.setPack(&fakePack{label: "embedded in yardr test1", files: big})
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "a card holds at most 65536") {
		t.Fatalf("a payload past the bound: AddAsk = %v", err)
	}
}

func askFiles(files map[string]string) []AskFile {
	var out []AskFile
	for prog, body := range files {
		sum := sha256.Sum256([]byte(body))
		out = append(out, AskFile{Program: prog, Text: body, SHA256: hex.EncodeToString(sum[:])})
	}
	return out
}

// An approval is refused when a source the pack would add was added
// since the ask.
func TestPackApplyStaleSourceAdded(t *testing.T) {
	y, _ := runYard(t)
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	if _, err := y.PutSource(y.ctx, "wired.yaml", wiredDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "was not in the yard: it was added since") {
		t.Fatalf("ApproveAsk = %v", err)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after a refused approval", r.State)
	}
}

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// setupNotes are a source pack's words about what a setup takes.
const setupNotes = "# Setup\n\nMake the webhook, then the hook.\n"

// setupPack is kindPack with setup notes.
func setupPack(t *testing.T) string {
	t.Helper()
	dir := kindPack(t)
	file := filepath.Join(dir, "pack.yaml")
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	with := strings.Replace(string(text), "kind: source\n", "kind: source\nsetup: SETUP.md\n", 1)
	if with == string(text) {
		t.Fatal("pack.yaml has no kind: source line")
	}
	if err := os.WriteFile(file, []byte(with), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SETUP.md"), []byte(setupNotes), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const setupMark = "setup notes of pack acme-pack, from SETUP.md (from the pack, not checked by yardr):"

// pack check prints a pack's setup notes after the plan, under the line
// saying they are the pack's words.
func TestPackCheckPrintsSetupNotes(t *testing.T) {
	a := packApp(t)
	out, err := run(t, a.packCmd(), "check", setupPack(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, setupMark+"\n"+setupNotes) {
		t.Fatalf("the plan lacks the notes under the marking line:\n%s", out)
	}
	if strings.Index(out, setupMark) < strings.LastIndex(out, "not in the pack") {
		t.Fatalf("the notes come before the plan's end:\n%s", out)
	}
}

// A pack without the key prints nothing more than today.
func TestPackCheckWithoutSetupPrintsNothingMore(t *testing.T) {
	a := packApp(t)
	out, err := run(t, a.packCmd(), "check", kindPack(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "setup notes of pack") || strings.Contains(out, "not checked by yardr") {
		t.Fatalf("a pack without setup notes names them:\n%s", out)
	}
}

// pack apply prints the notes after what it did, and records that the pack
// carries them; a pack without the key records none.
func TestPackApplyPrintsSetupNotesAndRecordsThem(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	out, err := run(t, a.packCmd(), "apply", setupPack(t), "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, setupMark+"\n"+setupNotes) {
		t.Fatalf("the output lacks the notes under the marking line:\n%s", out)
	}
	// Twice: after the plan it prints first, and after what it did.
	if n := strings.Count(out, setupMark); n != 2 {
		t.Fatalf("the notes are printed %d times, want after the plan and after the result:\n%s", n, out)
	}
	if strings.LastIndex(out, setupMark) < strings.Index(out, "applied pack acme-pack to the yard") {
		t.Fatalf("the notes come before the result:\n%s", out)
	}
	recs, err := a.st.Packs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || !recs[0].Setup {
		t.Fatalf("records = %+v, want one with setup notes", recs)
	}
	plain := packApp(t)
	setInteractive(t, false)
	if out, err := run(t, plain.packCmd(), "apply", kindPack(t), "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	recs, err = plain.st.Packs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Setup {
		t.Fatalf("records = %+v, want one without setup notes", recs)
	}
}

// pack show prints a pack's setup notes later; a pack without any says so.
// The embedded linear pack carries its own.
func TestPackShowPrintsSetupNotes(t *testing.T) {
	a := packApp(t)
	out, err := run(t, a.packCmd(), "show", setupPack(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out != setupMark+"\n"+setupNotes {
		t.Fatalf("show prints:\n%s", out)
	}
	if out, err := run(t, a.packCmd(), "show", kindPack(t)); err != nil || out != "pack acme-pack carries no setup notes\n" {
		t.Fatalf("show of a pack without notes: %v\n%s", err, out)
	}
	out, err = run(t, a.packCmd(), "show", "linear")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"setup notes of pack linear, from SETUP.md (from the pack, not checked by yardr):\n",
		"yardr hook add linear --source linear-declared",
		"yardr crew identity <name> --source linear-declared --id <user id> --key <handle>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show linear lacks %q:\n%s", want, out)
		}
	}
}

// A crew member's prime names the setup notes of the packs imported into
// its yard or depot by the command that prints them, each pack once — and
// no pack without any. Not the text itself: the record carries none.
func TestPrimeNamesSetupNotesByTheCommandThatPrintsThem(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, rec := range []store.PackImport{
		{Pack: "linear", Source: "embedded in yardr test", Setup: true},
		{Pack: "linear", Depot: "web", Source: "packs/linear", Setup: true},
		{Pack: "starter", Depot: "web", Source: "packs/starter", Setup: true},
		{Pack: "team", Depot: "web", Source: "packs/team"},
	} {
		if _, err := st.RecordPack(ctx, rec, true, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	for _, want := range []string{
		"A setup starts with the pack's notes, its author's words on what it takes: yardr pack show linear\n",
		"A setup starts with the pack's notes, its author's words on what it takes: yardr pack show starter\n",
	} {
		if strings.Count(out, want) != 1 {
			t.Errorf("prime names it %d times, want once %q:\n%s", strings.Count(out, want), want, out)
		}
	}
	if strings.Contains(out, "pack show team") {
		t.Errorf("prime names a pack without notes:\n%s", out)
	}
	// Where the memory and journal part ends: after what to recall, before
	// the mail lines.
	if before, after := strings.Index(out, "yardr recall <words>"), strings.Index(out, "pack show linear"); before < 0 || after < before {
		t.Errorf("the notes are not named after the memory lines:\n%s", out)
	}
	if at, mail := strings.Index(out, "pack show starter"), strings.Index(out, "yardr mail <name>"); at < 0 || mail < at {
		t.Errorf("the notes are not named before the mail lines:\n%s", out)
	}
}

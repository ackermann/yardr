package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// pack export writes the depot's pack, says what went in and what was left
// out, and what it wrote reads as a pack; a second export into the same
// directory is refused.
func TestPackExport(t *testing.T) {
	t.Parallel()
	// Its own yard, with no operator: the export restores {{operator}}
	// where the import filled the default. packApp names one. The import
	// runs as the crew member it seats, whose own subscriptions it writes.
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.PutSource(ctx, "linear.yaml", linearPackDecl, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "boss", cfg: &config.Config{Home: t.TempDir()}}
	requireNonInteractive(t)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dir := filepath.Join(t.TempDir(), "out")
	out, err := run(t, a.packCmd(), "export", "--depot", "web", "--name", "team", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pack team, kind git, written to " + dir + " from depot web\n",
		"flows\n  default          8 stages, from starter.default\n  train            type train, 10 stages, from starter.train\n",
		"  builders         agent   roles/builders.md  (the yard's web-builders)\n",
		"  assembly         script  scripts/assembly  (the yard's web-assembly)\n",
		"  backlog          people  (the yard's backlog)\n",
		"files\n  flows/default/approved.md\n",
		"  pack.yaml\n",
		"{{operator}} stands where the yard's files said " + defaultOperator() + ";",
		"left out\n",
		"  group web-builders: args, kind, model: not in the pack; an import asks for the agent, its model and its arguments",
		": names crew member boss: the name stands in the file as plain text, and the pack brings no crew member of that name\n",
		"never written: beads, notes, mail, settings,",
		"checked: " + dir + " reads back as a pack",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q", want)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
	p, err := pack.LoadDir(dir)
	if err != nil || p.Name != "team" || len(p.Groups) != 6 || len(p.Crew) != 0 {
		t.Fatalf("the exported pack = %+v, %v", p, err)
	}
	// What was written is a pack the yard can check as any other.
	if out, err := run(t, a.packCmd(), "check", dir, "--depot", "web"); err != nil || !strings.Contains(out, "pack team (") {
		t.Fatalf("pack check of the export: %v\n%s", err, out)
	}

	if _, err := run(t, a.packCmd(), "export", "--depot", "web", dir); err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("a second export into the directory: %v", err)
	}

	a.json = true
	out, err = run(t, a.packCmd(), "export", "--depot", "web", filepath.Join(t.TempDir(), "again"))
	if err != nil {
		t.Fatal(err)
	}
	var ex pack.Exported
	if err := json.Unmarshal([]byte(out), &ex); err != nil || ex.Pack != "web" || len(ex.Flows) != 2 || len(ex.Left) == 0 {
		t.Fatalf("json: %v\n%s", err, out)
	}
}

// The help says in plain words what is written and what never is.
func TestPackExportHelp(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	out, err := run(t, a.packCmd(), "export", "--help")
	if err != nil {
		t.Fatal(err)
	}
	// The help wraps lines for the terminal; assert on its words, not those
	// line breaks.
	out = strings.Join(strings.Fields(out), " ")
	for _, want := range []string{
		"What is written:",
		"What is never written, whatever the depot holds: a bead, a note",
		"a setting",
		"writes no crew member",
		"a route that wins for only some beads because of a label",
		"left out",
		"refuses, and writes nothing",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the help lacks %q", want)
		}
	}
	if _, err := run(t, a.packCmd(), "export", filepath.Join(os.TempDir(), "never-written")); err == nil || !strings.Contains(err.Error(), `"depot" not set`) {
		t.Fatalf("export without --depot: %v", err)
	}
}

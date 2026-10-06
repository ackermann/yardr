package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The yardmaster a session is told to mail is the yard's: the yardmaster
// setting yardr init stores, else the one crew member of a yard from before
// the setting, else init's default name. A yard with several crew members
// and no setting does not guess among them.
func TestYardmasterOf(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if got := yardmasterOf(ctx, st); got != "yardmaster" {
		t.Errorf("empty yard: %s, want yardmaster", got)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := yardmasterOf(ctx, st); got != "boss" {
		t.Errorf("one crew member: %s, want boss", got)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := yardmasterOf(ctx, st); got != "yardmaster" {
		t.Errorf("two crew members, no setting: %s, want yardmaster", got)
	}
	if _, err := run(t, (&app{st: st}).settingsCmd(), "set", "yardmaster", "scribe"); err != nil {
		t.Fatal(err)
	}
	if got := yardmasterOf(ctx, st); got != "scribe" {
		t.Errorf("yardmaster setting: %s, want scribe", got)
	}
	if _, err := run(t, (&app{st: st}).settingsCmd(), "set", "yardmaster", "no spaces"); err == nil {
		t.Error("settings set yardmaster accepted a name a crew member cannot have")
	}
}

// ownDepotFiles gives depot the .yardr files of a depot that keeps its own
// way of working (testdata/owndepot): its roles, its flow with a stage file
// for every stage, and for a git depot its gate. No command writes these
// since a depot is set up from a pack; a depot from before, or one that
// chose to, has them, and yardr reads them as it did.
func ownDepotFiles(t *testing.T, depot store.Depot) {
	t.Helper()
	kind := "git"
	if depot.IsDir() {
		kind = "dir"
	}
	if err := os.CopyFS(filepath.Join(depot.Path, ".yardr"), os.DirFS(filepath.Join("testdata", "owndepot", kind))); err != nil {
		t.Fatal(err)
	}
	if kind == "git" {
		// os.CopyFS keeps no more of a mode than whether it was 0o644.
		if err := os.Chmod(filepath.Join(depot.Path, ".yardr", "check"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// A depot that keeps its own flow has a stage file for every stage of it
// that is not terminal: the depot's check then has no stage to name as
// lacking one and no stray file, for a git depot (once committed on the
// base) and a dir depot; and a stage file removed is a stage named.
func TestStageFileFindingsOfADepotsOwnFlow(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	git := gitDepot(t, st)
	commitDepot(t, git)
	dir := store.Depot{Name: "invoices", Path: t.TempDir(), Kind: store.DepotDir}
	if err := st.PutDepot(ctx, dir, "me"); err != nil {
		t.Fatal(err)
	}
	ownDepotFiles(t, dir)
	for _, depot := range []store.Depot{git, dir} {
		governing, err := st.DepotFlows(ctx, depot.Name)
		if want := map[string]int{store.DepotGit: 1, store.DepotDir: 2}[depot.Kind]; err != nil || len(governing) != want {
			t.Fatalf("%s: %d governing flows, %v; want %d", depot.Name, len(governing), err, want)
		}
		notes, stray, strayHome, err := stageFileFindings(ctx, st, depot, depot.Path, t.TempDir())
		if err != nil || len(notes)+len(stray)+len(strayHome) != 0 {
			t.Errorf("%s: notes %v, stray %v, stray in home %v, %v; want none", depot.Name, notes, stray, strayHome, err)
		}
		if err := os.Remove(filepath.Join(depot.Path, ".yardr", "flows", "default", "review.md")); err != nil {
			t.Fatal(err)
		}
		notes, _, _, err = stageFileFindings(ctx, st, depot, depot.Path, t.TempDir())
		if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "flow default has no stage file for review ") {
			t.Errorf("%s: without review.md: notes %v, %v", depot.Name, notes, err)
		}
	}
}

// The gate is guessed from the repo: npm's placeholder test script is none.
func TestGuessGate(t *testing.T) {
	for _, tc := range []struct {
		files map[string]string
		gate  string
	}{
		{map[string]string{"go.mod": "module x\n"}, "go vet ./... && go test ./..."},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest run"}}`}, "npm test"},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest"}}`, "pnpm-lock.yaml": ""}, "pnpm test"},
		{map[string]string{"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`}, ""},
		{map[string]string{"package.json": `{}`}, ""},
		{nil, ""},
	} {
		dir := t.TempDir()
		for n, b := range tc.files {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(b), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gate, script := guessGate(store.Depot{Name: "r", Path: dir, Base: "main"})
		if gate != tc.gate {
			t.Errorf("%v: gate %q, want %q", tc.files, gate, tc.gate)
		}
		if gate == "" && (!strings.Contains(script, "TODO") || !strings.HasSuffix(script, "exit 0\n")) {
			t.Errorf("%v: no-guess script:\n%s", tc.files, script)
		}
	}
}

// snapshot reads every file under dir outside .git, with its mode.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() && fi.Name() == ".git" {
			return filepath.SkipDir
		}
		if !fi.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			m[p] = fi.Mode().String() + "\n" + string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// This repository's own flows: every stage that is not terminal has a stage
// file, and no stage file is an orphan, which is what yardr depot check yardr
// reports of the tree.
func TestThisRepoHasItsStageFiles(t *testing.T) {
	tree, err := store.ReadFlowDir(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if stray := tree.StrayStageFiles(); len(stray) > 0 {
		t.Errorf("stage files that belong to no stage: %v", stray)
	}
	names := map[string]bool{}
	for _, rf := range tree.Flows {
		if rf.Flow == nil {
			t.Fatalf("%s: %s", rf.Path, rf.Error)
		}
		names[rf.Flow.Name] = true
		for _, s := range rf.Flow.Stages {
			if !rf.Flow.IsTerminal(s) && tree.StageFile(rf.Flow.Name, s) == nil {
				t.Errorf("flow %s has no stage file for %s", rf.Flow.Name, s)
			}
		}
	}
	if !names["default"] || !names["yardr.train"] {
		t.Fatalf("flows = %v", names)
	}
}

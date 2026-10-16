package cli

import (
	"context"
	"os"
	"path/filepath"
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
// for every stage. No command writes these
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
}

// The test command is guessed from the repository: a test script of its
// own by its path, before what its build files say; npm's placeholder test
// script is none, nor is a Makefile without a test target, and a repository
// that says nothing gets none.
func TestGuessTest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"go.mod": "module x\n"}, "go vet ./... && go test ./..."},
		{map[string]string{"go.mod": "module x\n", "scripts/test": "#!/bin/sh\n"}, "scripts/test"},
		{map[string]string{"scripts/test": "#!/bin/sh\n", ".yardr/test": "#!/bin/sh\n", "Makefile": "test:\n"}, ".yardr/test"},
		// A directory of tests is no script.
		{map[string]string{"scripts/test/a_test.py": "", "Cargo.toml": ""}, "cargo test"},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest run"}}`}, "npm test"},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest"}}`, "pnpm-lock.yaml": ""}, "pnpm test"},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest"}}`, "yarn.lock": ""}, "yarn test"},
		{map[string]string{"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`}, ""},
		{map[string]string{"package.json": `{}`}, ""},
		{map[string]string{"Cargo.toml": "[package]\n"}, "cargo test"},
		{map[string]string{"Makefile": "all: build\n\n.PHONY: test\ntest: build\n\tgo test ./...\n"}, "make test"},
		{map[string]string{"Makefile": "build test: deps\n"}, "make test"},
		{map[string]string{"Makefile": "TESTS := test\n.PHONY: test\nall:\n\techo test: no\n"}, ""},
		{nil, ""},
	} {
		dir := t.TempDir()
		for n, b := range tc.files {
			writeFile(t, filepath.Join(dir, n), b)
		}
		if got := guessTest(dir); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.files, got, tc.want)
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
// file, and no stage file is an orphan.
func TestThisRepoHasItsStageFiles(t *testing.T) {
	t.Parallel()
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

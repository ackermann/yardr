package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Depot yardr's test command, as it is in this checkout: what a builder and
// a reviewer run in their worktree, and with YARDR_LANDING=1 what the
// assembly runs on what lands. Its table of files that tests read by path
// is in a shell variable, reads='...'.
const yardTest = ".yardr/test"

type readsRow struct {
	path string
	pkgs []string // empty for a - row: no test reads the path
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// scriptEnv is this test's environment without what the run it is in gave
// the script it is part of: the base and the landing of an agent's session
// or the assembly, the socket client and the GOOS of a landing's steps, a
// plan, the race lock the run's own race leg holds, and the quiet run's
// marks (YARDR_R1_STRICT and YARDR_QUIET_RUN), which would otherwise change
// strict-budget judging and enable quiet-run-only tests. A case sets what
// it means the script to have: one that runs the script sets YARDR_RACE_HELD,
// so its race step shares the hold instead of taking the machine's lock,
// except the lock's own test, which takes a lock of its own.
func scriptEnv() []string {
	return scriptEnvFrom(os.Environ())
}

// scriptEnvFrom lets a case check the filter with a synthetic run environment.
func scriptEnvFrom(environ []string) []string {
	return slices.DeleteFunc(environ, func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"YARDR_BASE", "YARDR_LANDING", "YARDR_TEST_PLAN", "YARDR_TEST_RPC", "GOOS", "YARDR_RACE_HELD", "YARDR_R1_STRICT", "YARDR_QUIET_RUN"}, k)
	})
}

func yardTestRows(t *testing.T) (script string, rows []readsRow) {
	t.Helper()
	b, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(b), "\nreads='\n")
	if ok {
		table, _, ok = strings.Cut(table, "\n'\n")
	}
	if !ok {
		t.Fatalf("%s has no reads='...' table", yardTest)
	}
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			t.Fatalf("row %q: want a path and its packages, or -", line)
		}
		row := readsRow{path: f[0]}
		if f[1] != "-" {
			row.pkgs = f[1:]
		}
		rows = append(rows, row)
	}
	return string(b), rows
}

// Every row names a path the repository has and packages that have tests:
// a row left behind by a rename would otherwise select nothing, silently.
func TestYardTestRowsAreThere(t *testing.T) {
	_, rows := yardTestRows(t)
	for _, row := range rows {
		info, err := os.Stat(row.path)
		if err != nil {
			t.Errorf("row %s: %v", row.path, err)
		} else if info.IsDir() != strings.HasSuffix(row.path, "/") {
			t.Errorf("row %s: a directory's row ends in /, a file's does not", row.path)
		}
		for _, pkg := range row.pkgs {
			if tests, _ := filepath.Glob(filepath.Join(pkg, "*_test.go")); len(tests) == 0 {
				t.Errorf("row %s: %s has no tests", row.path, pkg)
			}
		}
	}
}

// The base .yardr/test compares with: YARDR_BASE, else origin/main, and its
// first line says which. One it cannot find is "could
// not run" (exit 2), never "nothing changed"; for a landing it is the whole
// suite, as a landing of nothing is.
func TestYardTestSaysItsBase(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go in PATH")
	}
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m.test/m\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	short := gitT(t, dir, "rev-parse", "--short", "HEAD")
	gitT(t, dir, "branch", "other")
	run := func(env ...string) (string, int) {
		t.Helper()
		cmd := exec.Command("sh", "-c", string(script))
		cmd.Dir = dir
		cmd.Env = append(scriptEnv(), "YARDR_TEST_PLAN=1")
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), 0
	}
	for _, c := range []struct {
		name string
		env  []string
		want string
		code int
	}{
		{"YARDR_BASE", []string{"YARDR_BASE=other"},
			"test: what changed since other (YARDR_BASE), from " + short, 0},
		{"a landing of nothing", []string{"YARDR_BASE=other", "YARDR_LANDING=1"},
			"test: YARDR_LANDING is set, and nothing changed since other (YARDR_BASE), from " + short + ": the whole suite", 0},
		{"a base that is not there", []string{"YARDR_BASE=nowhere"},
			"test: cannot compare with nowhere (YARDR_BASE): no such commit here, or none in common with HEAD", 2},
		// The fake module has no origin.
		{"origin/main when it is not set", nil,
			"test: cannot compare with origin/main (the default, YARDR_BASE not being set): no such commit here, or none in common with HEAD", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, code := run(c.env...)
			// A base that is there: the first line, whatever the module's
			// packages then make of the table. One that is not: exit 2,
			// after git's own complaint.
			if first, _, _ := strings.Cut(out, "\n"); c.code == 0 && first != c.want {
				t.Errorf("first line %q, want %q", first, c.want)
			}
			if c.code != 0 && (code != c.code || !strings.Contains(out, c.want+"\n")) {
				t.Errorf("exit %d, output %q, want exit %d and %q", code, out, c.code, c.want)
			}
		})
	}
	// CI's checkout: one commit, no origin/main. After git's own complaint.
	t.Run("a landing with a base that is not there", func(t *testing.T) {
		out, code := run("YARDR_LANDING=1")
		want := "test: YARDR_LANDING is set, and origin/main (the default, YARDR_BASE not being set) is no commit here, or has none in common with HEAD: the whole suite\n" +
			"test: no comparable base: running the socket suites beside the first run\n"
		if code != 0 || !strings.HasSuffix(out, want) {
			t.Errorf("exit %d, output %q, want exit 0 and at its end %q", code, out, want)
		}
	})
}

// What a plain run selects, in a module that has the table's packages and
// nothing in them, after a line saying what it is compared with: a file a
// test reads by path runs that test's package, a file nobody is known to
// read runs everything, a file the table says no test reads runs nothing.
// YARDR_TEST_PLAN makes the script say so and stop. A landing selects the
// same when nothing compiled changed, the selection plus the six
// black-box suites when code changed, and the whole suite when what
// changed cannot be selected.
func TestYardTestSelects(t *testing.T) {
	t.Parallel()
	script, rows := yardTestRows(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go in PATH")
	}
	const pre = "test: "
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module m.test/m\n\ngo 1.21\n")
	for _, row := range rows {
		for _, pkg := range row.pkgs {
			write(pkg+"/p.go", "package p\n")
			write(pkg+"/p_test.go", "package p\n")
		}
	}
	// One socket suite imports internal/rpc, as the four do in the
	// repository: touching rpc selects store as an importer, while nothing
	// imports internal/cli.
	write("internal/store/p.go", "package p\n\nimport _ \"m.test/m/internal/rpc\"\n")
	write("internal/rpc/p.go", "package p\n")
	// internal/web stands alone: touching it selects web itself, and the
	// gate runs the socket leg for a selection that holds it.
	write("internal/web/p.go", "package p\n")
	// A package that embeds a file, one that imports it, and one alone.
	write("emb/p.go", "package emb\n\nimport _ \"embed\"\n\n//go:embed a.txt\nvar A string\n")
	write("emb/a.txt", "a\n")
	write("top/p.go", "package top\n\nimport _ \"m.test/m/emb\"\n")
	write("alone/p.go", "package p\n")
	// A package one case's change deletes, while the script still rows
	// it, as base's copy does at a landing.
	write("fakepkg/p.go", "package p\n\nvar Existing int\n")
	write("docs/web.md", "web\n")
	// As packs does: it embeds starter, which the table rows to
	// internal/pack and internal/cli, and one package imports it.
	write("packs/p.go", "package p\n\nimport \"embed\"\n\n//go:embed starter\nvar FS embed.FS\n")
	write("packs/starter/pack.yaml", "pack\n")
	write("packs/starter/roles/reviewer.md", "reviewer\n")
	write("packtop/p.go", "package packtop\n\nimport _ \"m.test/m/packs\"\n")
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	base := gitT(t, dir, "rev-parse", "HEAD")
	short := gitT(t, dir, "rev-parse", "--short", "HEAD")

	since := " since " + base + " (YARDR_BASE), from " + short
	// The script's plan for a branch that changed these files, or deleted
	// them (a name after "-"): its first line, and the rest.
	planned := func(t *testing.T, script string, env []string, changed ...string) (first, rest string, err error) {
		t.Helper()
		gitT(t, dir, "reset", "-q", "--hard", base)
		gitT(t, dir, "clean", "-qfd")
		for _, name := range changed {
			text := "changed\n"
			if strings.HasSuffix(name, ".go") {
				text = "package p\n\nvar Changed int\n"
			}
			if name == "moved/p.go" {
				text = "package p\n\nvar Existing int\n"
			}
			if name == "go.mod" {
				text = "module m.test/m\n\ngo 1.21\n\n// changed\n"
			}
			if gone, ok := strings.CutPrefix(name, "-"); ok {
				if err := os.Remove(filepath.Join(dir, gone)); err != nil {
					t.Fatal(err)
				}
			} else {
				write(name, text)
			}
		}
		gitT(t, dir, "add", "-A")
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(append(scriptEnv(), "YARDR_TEST_PLAN=1", "YARDR_BASE="+base), env...)
		b, err := cmd.CombinedOutput()
		first, rest, _ = strings.Cut(strings.TrimSpace(string(b)), "\n")
		return first, rest, err
	}
	plan := func(t *testing.T, script string, changed ...string) (string, error) {
		t.Helper()
		first, out, err := planned(t, script, nil, changed...)
		if want := "test: what changed" + since; first != want {
			t.Errorf("first line %q, want %q", first, want)
		}
		return out, err
	}
	tested := pre + "testing, with -race, what changed, what imports it and what reads it:"
	for _, c := range []struct {
		name    string
		changed []string
		want    []string
	}{
		{"the starter pack", []string{"packs/starter/roles/builder.md"}, []string{
			pre + "packs/starter/roles/builder.md changed: read by the tests of internal/pack internal/cli",
			tested + " m.test/m/internal/cli m.test/m/internal/pack m.test/m/packs m.test/m/packtop"}},
		{"two files of the starter pack", []string{"packs/starter/roles/builder.md", "packs/starter/pack.yaml"}, []string{
			pre + "packs/starter/pack.yaml and 1 more under packs/starter/ changed: read by the tests of internal/pack internal/cli",
			tested + " m.test/m/internal/cli m.test/m/internal/pack m.test/m/packs m.test/m/packtop"}},
		{"a deleted embedded file under a rowed directory", []string{"-packs/starter/roles/reviewer.md"}, []string{
			pre + "packs/starter/roles/reviewer.md changed: read by the tests of internal/pack internal/cli",
			tested + " m.test/m/internal/cli m.test/m/internal/pack m.test/m/packs m.test/m/packtop"}},
		{"a deleted file under no embedded directory", []string{"-docs/web.md"}, []string{
			pre + "docs/web.md changed: read by the tests of .",
			tested + " m.test/m"}},
		{"another package's testdata", []string{"internal/hooks/testdata/github/ping.json"}, []string{
			pre + "internal/hooks/testdata/github/ping.json changed: read by the tests of internal/hooks internal/dispatch",
			tested + " m.test/m/internal/dispatch m.test/m/internal/hooks"}},
		{"a file no row knows", []string{"contrib/x.txt", "docs/web.md"}, []string{
			pre + "contrib/x.txt is read by no known package: testing everything"}},
		{"three files no row knows", []string{"contrib/x.txt", "alone/y.txt", "z"}, []string{
			pre + "alone/y.txt (and 2 more) is read by no known package: testing everything"}},
		// The root package's test follows the README's links into docs/.
		{"docs only", []string{"docs/web.md", "docs/new.md"}, []string{
			pre + "docs/new.md and 1 more under docs/ changed: read by the tests of .",
			tested + " m.test/m"}},
		{"what no test reads", []string{".github/a.yml", ".github/b.yml"}, []string{
			pre + ".github/a.yml and 1 more under .github/ changed: no test reads it",
			pre + "no package changed since " + short + "; no tests to run"}},
		// AGENTS.md is the map of the repository for agents, and CLAUDE.md
		// imports it: no test reads either. A change to them alone tests
		// nothing, and the change that adds a package, which the depot's rule
		// has update the map, tests that package and no other (not
		// everything, as a file no row knows does).
		{"the map for agents", []string{"AGENTS.md", "CLAUDE.md"}, []string{
			pre + "AGENTS.md changed: no test reads it",
			pre + "CLAUDE.md changed: no test reads it",
			pre + "no package changed since " + short + "; no tests to run"}},
		{"a new package and the map that lists it", []string{"AGENTS.md", "newpkg/p.go"}, []string{
			pre + "AGENTS.md changed: no test reads it",
			tested + " m.test/m/newpkg"}},
		// A file's row is that file only: .yardr/test is not a prefix of
		// .yardr/tests.md, which no row knows.
		{"the script itself", []string{".yardr/test"}, []string{
			pre + ".yardr/test changed: read by the tests of .",
			tested + " m.test/m"}},
		{"a file its name begins", []string{".yardr/tests.md"}, []string{
			pre + ".yardr/tests.md is read by no known package: testing everything"}},
		{"an embedded file", []string{"emb/a.txt"}, []string{
			tested + " m.test/m/emb m.test/m/top"}},
		{"a go file", []string{"alone/p.go", "docs/web.md"}, []string{
			pre + "docs/web.md changed: read by the tests of .",
			tested + " m.test/m m.test/m/alone"}},
		{"nothing", nil, []string{
			pre + "no package changed since " + short + "; no tests to run"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := plan(t, script, c.changed...)
			if want := strings.Join(c.want, "\n"); err != nil || out != want {
				t.Errorf("err %v, output:\n%s\nwant:\n%s", err, out, want)
			}
		})
	}
	// A landing (YARDR_LANDING=1): the first line says what runs, and why.
	// When nothing compiled changed, the rest is a plain run's. A code
	// landing runs the selection plus the six black-box suites and says
	// so, with the socket gate's line (the leg runs only when the plain
	// selection holds store, dispatch, bdp or web), the rows that fired, and
	// the testing line naming every package. What cannot be selected
	// (go.mod, go.sum, a deletion, a file no row knows, nothing at all)
	// is the whole suite, and its rest is the gate's line alone.
	const landing = pre + "YARDR_LANDING is set, and "
	const wholeSuffix = ": the whole suite"
	const selectionSuffix = ": the selection plus the six black-box suites (contrib/assembly, contrib/peer, contrib/deploy, contrib/server-yard, contrib/isolated-yard, contrib/release)"
	const skipSocket = pre + "the plain selection holds neither store, dispatch, bdp nor web: skipping the socket suites"
	const runSocketStore = pre + "the plain selection holds store: running the socket suites beside the first run"
	const runSocketWeb = pre + "the plain selection holds web: running the socket suites beside the first run"
	const runSocketAll = pre + "the plain selection holds store dispatch bdp web: running the socket suites beside the first run"
	for _, c := range []struct {
		name    string
		changed []string
		first   string
		rest    []string
	}{
		{"a landing of one design document", []string{"docs/design/2026-10-12-a.md"},
			landing + "nothing compiled changed" + since, []string{
				pre + "docs/design/2026-10-12-a.md changed: read by the tests of .",
				tested + " m.test/m"}},
		{"a landing of the starter pack and what no test reads", []string{"packs/starter/roles/builder.md", ".github/a.yml"},
			landing + "packs/starter/roles/builder.md, which a package embeds, changed" + since + selectionSuffix, []string{
				skipSocket,
				pre + "packs/starter/roles/builder.md changed: read by the tests of internal/pack internal/cli",
				pre + ".github/a.yml changed: no test reads it",
				tested + " m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/cli m.test/m/internal/pack m.test/m/packs m.test/m/packtop"}},
		{"a landing of what no test reads", []string{".github/a.yml"},
			landing + "nothing compiled changed" + since, []string{
				pre + ".github/a.yml changed: no test reads it",
				pre + "no package changed since " + short + "; no tests to run"}},
		{"a landing of a go file", []string{"docs/web.md", "alone/p.go"},
			landing + "alone/p.go changed" + since + selectionSuffix, []string{
				skipSocket,
				pre + "docs/web.md changed: read by the tests of .",
				tested + " m.test/m m.test/m/alone m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard"}},
		{"a landing of a test file", []string{"alone/p_test.go"},
			landing + "alone/p_test.go changed" + since + selectionSuffix, []string{
				skipSocket,
				tested + " m.test/m/alone m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard"}},
		{"a landing of go.mod", []string{"go.mod", "docs/web.md"},
			landing + "go.mod changed" + since + wholeSuffix, []string{runSocketAll}},
		{"a landing of go.sum", []string{"go.sum"},
			landing + "go.sum changed" + since + wholeSuffix, []string{runSocketAll}},
		{"a landing of an embedded file", []string{"docs/web.md", "emb/a.txt"},
			landing + "emb/a.txt, which a package embeds, changed" + since + selectionSuffix, []string{
				skipSocket,
				pre + "docs/web.md changed: read by the tests of .",
				tested + " m.test/m m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/emb m.test/m/top"}},
		{"a landing of a file no row knows", []string{"docs/web.md", "contrib/x.txt"},
			landing + "contrib/x.txt, which is read by no known package, changed" + since + wholeSuffix, []string{runSocketAll}},
		// What embedded a file that is gone cannot be asked any more.
		{"a landing that deletes a file", []string{"-docs/web.md"},
			landing + "docs/web.md was deleted" + since + wholeSuffix, []string{skipSocket}},
		{"a landing of nothing", nil,
			landing + "nothing changed" + since + wholeSuffix, []string{skipSocket}},
		// A Go change runs the selection plus the six while its plain
		// selection excludes the socket suites: the gate skips the leg.
		// A change to internal/rpc selects store as an importer: the
		// gate runs the leg, still on the plain selection alone.
		{"a landing touching internal/cli skips the socket suites", []string{"internal/cli/p.go"},
			landing + "internal/cli/p.go changed" + since + selectionSuffix, []string{
				skipSocket,
				tested + " m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/cli"}},
		{"a landing touching internal/rpc runs the socket suites", []string{"internal/rpc/p.go"},
			landing + "internal/rpc/p.go changed" + since + selectionSuffix, []string{
				runSocketStore,
				tested + " m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/rpc m.test/m/internal/store"}},
		{"a landing touching internal/web runs the socket suites", []string{"internal/web/p.go"},
			landing + "internal/web/p.go changed" + since + selectionSuffix, []string{
				runSocketWeb,
				tested + " m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/web"}},
		{"a landing of an unknown file runs the socket suites", []string{"contrib/x.txt"},
			landing + "contrib/x.txt, which is read by no known package, changed" + since + wholeSuffix, []string{runSocketAll}},
	} {
		t.Run(c.name, func(t *testing.T) {
			first, rest, err := planned(t, script, []string{"YARDR_LANDING=1"}, c.changed...)
			if want := strings.Join(c.rest, "\n"); err != nil || first != c.first || rest != want {
				t.Errorf("err %v, output:\n%s\n%s\nwant:\n%s\n%s", err, first, rest, c.first, want)
			}
		})
	}
	// Run, not planned, such a landing is a plain run's steps: no vet for
	// linux, no socket suites, and the tests of the root package only. The go
	// it runs is the real one for the list of packages and writes down every
	// other call.
	t.Run("a landing of one design document runs the plain steps", func(t *testing.T) {
		realGo, err := exec.LookPath("go")
		if err != nil {
			t.Fatal(err)
		}
		fake := t.TempDir()
		for name, text := range map[string]string{
			"go":    "#!/bin/sh\nif [ \"$1\" = list ]; then exec \"$REAL_GO\" \"$@\"; fi\necho \"${GOOS:+$GOOS }$*\" >>\"$FAKE/calls\"\n",
			"gofmt": "#!/bin/sh\n",
		} {
			if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		first, rest, err := planned(t, script, []string{"YARDR_LANDING=1", "YARDR_TEST_PLAN=", "YARDR_RACE_HELD=1", "FAKE=" + fake, "REAL_GO=" + realGo,
			"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH")}, "docs/design/2026-10-12-a.md")
		if err != nil || first != landing+"nothing compiled changed"+since {
			t.Errorf("err %v, output:\n%s\n%s", err, first, rest)
		}
		calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
		if got, want := strings.TrimSpace(string(calls)), "vet ./...\nbuild ./...\ntest -race m.test/m"; got != want {
			t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
		}
		// Each step is timed, choosing the packages too, after the first
		// line: that one says which run it is.
		var took []string
		for _, m := range regexp.MustCompile(`(?m)^test: (.*) took \d+ s$`).FindAllStringSubmatch(rest, -1) {
			took = append(took, m[1])
		}
		if want := []string{"go list -test ./...", "gofmt -l .", "go vet ./...", "go build ./...", "sleep budgets", "go test -race m.test/m"}; !slices.Equal(took, want) {
			t.Errorf("the steps the output times: %q, want %q", took, want)
		}
	})
	// Run, not planned, a landing over a code change runs the selection
	// plus all six black-box suites (the first line and the go test call
	// both name them), beside the socket leg when the plain selection
	// holds one of its packages: a change the socket suites cannot
	// reach starts no leg, and one over internal/rpc or internal/web
	// starts it. A landing over an unknown file is still the whole
	// suite. The go it runs is the real one for the list of packages
	// and writes down every other call.
	const selCLI = "m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/cli"
	const selRPC = "m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/rpc m.test/m/internal/store"
	const selWEB = "m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/web"
	for _, c := range []struct {
		name    string
		changed string
		reason  string
		suffix  string
		line    string
		calls   []string
		steps   []string
	}{
		{"a landing over internal/cli starts no socket leg", "internal/cli/p.go", "internal/cli/p.go changed", selectionSuffix, skipSocket,
			[]string{"vet ./...", "linux vet ./...", "test -race " + selCLI},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "go test -race " + selCLI}},
		{"a landing over internal/rpc starts the socket leg", "internal/rpc/p.go", "internal/rpc/p.go changed", selectionSuffix, runSocketStore,
			[]string{"vet ./...", "linux vet ./...", "test -race " + selRPC, "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "go test -race " + selRPC,
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}},
		{"a landing over internal/web starts the socket leg", "internal/web/p.go", "internal/web/p.go changed", selectionSuffix, runSocketWeb,
			[]string{"vet ./...", "linux vet ./...", "test -race " + selWEB, "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "go test -race " + selWEB,
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}},
		{"a landing over an unknown file starts the socket leg", "contrib/x.txt", "contrib/x.txt, which is read by no known package, changed", wholeSuffix, runSocketAll,
			[]string{"vet ./...", "linux vet ./...", "test -race ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "go test -race ./...",
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			realGo, err := exec.LookPath("go")
			if err != nil {
				t.Fatal(err)
			}
			fake := t.TempDir()
			for name, text := range map[string]string{
				"go":    "#!/bin/sh\nif [ \"$1\" = list ]; then exec \"$REAL_GO\" \"$@\"; fi\necho \"${YARDR_TEST_RPC:+rpc }${GOOS:+$GOOS }$*\" >>\"$FAKE/calls\"\n",
				"gofmt": "#!/bin/sh\n",
			} {
				if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			first, rest, err := planned(t, script, []string{"YARDR_LANDING=1", "YARDR_TEST_PLAN=", "YARDR_RACE_HELD=1", "FAKE=" + fake, "REAL_GO=" + realGo,
				"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH")}, c.changed)
			if err != nil || first != landing+c.reason+since+c.suffix {
				t.Errorf("err %v, output:\n%s\n%s", err, first, rest)
			}
			if !strings.Contains(rest, c.line+"\n") {
				t.Errorf("the output has no gate line %q in:\n%s", c.line, rest)
			}
			// The socket leg runs beside go test -race, so which of the
			// two go is called for first is not told: its calls are
			// compared after the others, where its output is.
			calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
			var ran, beside []string
			for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if strings.HasPrefix(call, "rpc ") {
					beside = append(beside, call)
				} else {
					ran = append(ran, call)
				}
			}
			if got, want := strings.Join(append(ran, beside...), "\n"), strings.Join(c.calls, "\n"); got != want {
				t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
			}
			var took []string
			for _, m := range regexp.MustCompile(`(?m)^test: (.*) took \d+ s$`).FindAllStringSubmatch(rest, -1) {
				took = append(took, m[1])
			}
			if !slices.Equal(took, c.steps) {
				t.Errorf("the steps the output times:\n%s\nwant:\n%s", strings.Join(took, "\n"), strings.Join(c.steps, "\n"))
			}
		})
	}
	// A row whose package is gone fails the run, whatever changed.
	t.Run("a stale row", func(t *testing.T) {
		stale := strings.Replace(script, "\nreads='\n", "\nreads='\nnowhere/ internal/pack internal/gone\n", 1)
		out, err := plan(t, stale, "docs/web.md")
		if want := pre + "the row for nowhere/ in " + yardTest + " names internal/gone, which is no package here"; err == nil || out != want {
			t.Errorf("err %v, output:\n%s\nwant:\n%s", err, out, want)
		}
	})
	// A row for a directory the change deletes is skipped, not stale: the
	// branch removes the row, but a landing runs this script as it is on
	// the base, whose row still names the directory. A row for a
	// directory in neither stays a failure.
	t.Run("a row for a directory the change deletes", func(t *testing.T) {
		deleted := strings.Replace(script, "\nreads='\n", "\nreads='\nfakepkg/ fakepkg .\n", 1)
		out, err := plan(t, deleted, "-fakepkg/p.go")
		if want := strings.Join([]string{
			pre + "the row for fakepkg/ in " + yardTest + " names fakepkg, which this change deletes: skipping the row",
			pre + "fakepkg/p.go changed: read by the tests of fakepkg .",
			tested + " m.test/m",
		}, "\n"); err != nil || out != want {
			t.Errorf("err %v, output:\n%s\nwant:\n%s", err, out, want)
		}
		// The landing that failed for yardr-d860: base's copy, over a
		// change that deletes a rowed directory, is the whole suite,
		// not a stale row.
		first, rest, err := planned(t, deleted, []string{"YARDR_LANDING=1"}, "-fakepkg/p.go")
		if want := landing + "fakepkg/p.go was deleted" + since + wholeSuffix; err != nil || first != want || rest != skipSocket {
			t.Errorf("err %v, output:\n%s\n%s\nwant:\n%s\n%s", err, first, rest, want, skipSocket)
		}
		// A directory move is a rename, so a landing reaches pick() instead
		// of taking the whole-suite path for a deleted file. This reproduces
		// the case where the base's row still names fakepkg/.
		first, rest, err = planned(t, deleted, []string{"YARDR_LANDING=1"}, "-fakepkg/p.go", "moved/p.go")
		want := strings.Join([]string{
			pre + "the row for fakepkg/ in " + yardTest + " names fakepkg, which this change deletes: skipping the row",
			tested + " m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/moved",
		}, "\n")
		if wantFirst := landing + "moved/p.go changed" + since + selectionSuffix; err != nil || first != wantFirst || rest != skipSocket+"\n"+want {
			t.Errorf("err %v, output:\n%s\n%s\nwant:\n%s\n%s\n%s", err, first, rest, wantFirst, skipSocket, want)
		}
		stale := strings.Replace(deleted, "\nreads='\n", "\nreads='\nnowhere/ nowhere\n", 1)
		out, err = plan(t, stale, "-fakepkg/p.go")
		if want := pre + "the row for nowhere/ in " + yardTest + " names nowhere, which is no package here"; err == nil || out != want {
			t.Errorf("err %v, output:\n%s\nwant:\n%s", err, out, want)
		}
	})
}

// What go vet said on 7 Oct 2026, when a file of the shared build cache was
// gone under a run.
const cacheLost = "vet: internal/store/sources.go:10:2: could not import yardr.dev/yardr/internal/source (open ../../../../Library/Caches/go-build/43/439f5c1e-d: no such file or directory)"

// A go that fails one of its steps (its first argument, or "rpc" for a call
// that has YARDR_TEST_RPC: a landing runs that one beside its other go test)
// the first few times with a line of its own, and writes down every call:
// the arguments, after "rpc" when the call has YARDR_TEST_RPC and after the
// GOOS it was given. gofmt finds nothing to format.
const fakeGo = `#!/bin/sh
echo "${YARDR_TEST_RPC:+rpc }${GOOS:+$GOOS }$*" >>"$FAKE/calls"
step=$1
[ -z "$YARDR_TEST_RPC" ] || step=rpc
if [ "$step" = "$FAKE_STEP" ]; then
  n=$(cat "$FAKE/n" 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" >"$FAKE/n"
  if [ "$n" -le "$FAKE_TIMES" ]; then
    echo "$FAKE_LINE" >&2
    exit 1
  fi
fi
echo "ran ${YARDR_TEST_RPC:+rpc }$1"
`

// A yardr that writes down every call, answers bead list with
// YARDR_FAKE_LIST and bead show with YARDR_FAKE_SHOW, and mints m-filed
// for bead create, keeping the body it was given: the gate files a repeat
// flake through it.
const fakeYardr = `#!/bin/sh
echo "$*" >>"$FAKE/yardr-calls"
if [ "$1 $2" = "bead list" ]; then
  printf '%s' "$YARDR_FAKE_LIST"
elif [ "$1 $2" = "bead show" ]; then
  printf '%s' "$YARDR_FAKE_SHOW"
elif [ "$1 $2" = "bead create" ]; then
  cat >"$FAKE/yardr-body"
  echo "m-filed"
fi
`

// The test command runs a go step once more when it failed because a file
// of Go's build cache was gone, and only then: a second failure stands, a
// step that failed of itself is not run again, and a log that reports a
// race is not run again for the cache either. Both runs' output is
// printed, and a step that failed for good is followed by the lines that
// say what failed: a failing test's a second time. A landing runs the socket
// suites beside the tests, whether those are green or not; each of the two
// is run again by the same rule, either makes the run red, and the socket
// step's output follows the whole of the other's.
func TestYardTestRunsAgainWhenTheBuildCacheLostAFile(t *testing.T) {
	t.Parallel()
	const notice = "the Go build cache lost a file under us; running again"
	const failing = "--- FAIL: TestX (0.00s)"
	const raceLost = cacheLost + "\nWARNING: DATA RACE\n--- FAIL: TestX (0.00s)\nFAIL\nFAIL\tm.test/m 0.123s"
	landing := []string{"vet ./...", "linux vet ./...", "test -race ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}
	plain := []string{"vet ./...", "build ./...", "test -race ./..."}
	noBase := []string{"YARDR_LANDING=1", "YARDR_BASE=nowhere"}
	for _, c := range []struct {
		name   string
		step   string // the step that fails,
		times  string // this many times,
		line   string // saying this
		ok     bool
		calls  []string
		lines  int // how often the output has line
		again  bool
		env    []string // variables of the script's environment beyond the base
		summed string   // the line a failed step's summary starts with
	}{
		// Run plainly it tests what changed; for a landing, everything, and
		// the socket suites again.
		{"build once", "build", "1", cacheLost, true,
			[]string{plain[0], plain[1], plain[1], plain[2]}, 1, true, nil, ""},
		{"test twice", "test", "2", cacheLost, false,
			[]string{plain[0], plain[1], plain[2], plain[2]}, 2, true, nil, "test: FAILED: go test -race ./..."},
		{"a failing test", "test", "1", failing, false,
			plain, 2, false, nil, "test: FAILED: go test -race ./...\n" + failing},
		// A race accuses the branch whatever the cache lost with it: the
		// step is not run again for the cache, and the flaky retry
		// refuses it for the same reason.
		{"a race with a lost cache file", "test", "1", raceLost, false,
			plain, 1, false, nil, "test: FAILED: go test -race ./...\nWARNING: DATA RACE"},
		// Landings without a comparable base (CI's shape): the socket
		// suites run whole, ungated, so these cases exercise the leg
		// itself rather than the gate.
		{"landing: vet once", "vet", "1", cacheLost, true,
			[]string{landing[0], landing[0], landing[1], landing[2], landing[3]}, 1, true, noBase, ""},
		{"landing: a failing test", "test", "1", failing, false,
			landing, 2, false, noBase, "test: FAILED: go test -race ./...\n" + failing},
		{"landing: a failing socket test", "rpc", "1", failing, false,
			landing, 2, false, noBase, "test: FAILED: env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/\n" + failing},
		{"landing: the socket step once", "rpc", "1", cacheLost, true,
			[]string{landing[0], landing[1], landing[2], landing[3], landing[3]}, 1, true, noBase, ""},
		{"landing: the socket step twice", "rpc", "2", cacheLost, false,
			[]string{landing[0], landing[1], landing[2], landing[3], landing[3]}, 2, true, noBase, "test: FAILED: env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"},
	} {
		t.Run(c.name, func(t *testing.T) {
			script, err := os.ReadFile(yardTest)
			if err != nil {
				t.Fatal(err)
			}
			fake, dir := t.TempDir(), t.TempDir()
			for name, text := range map[string]string{"go": fakeGo, "gofmt": "#!/bin/sh\n"} {
				if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tmp := filepath.Join(fake, "tmp")
			if err := os.Mkdir(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			// A branch that changed go.mod: a plain run tests every
			// package, and asks go for no list of them.
			mod := filepath.Join(dir, "go.mod")
			if err := os.WriteFile(mod, []byte("module m.test/m\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitT(t, dir, "init", "-q", "-b", "main")
			gitT(t, dir, "add", "-A")
			gitT(t, dir, "commit", "-q", "-m", "base")
			if err := os.WriteFile(mod, []byte("module m.test/m\n\ngo 1.21\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", string(script))
			cmd.Dir = dir
			// Without the YARDR_TEST_RPC of the run this test is in (CI's rpc
			// job, a landing's last step): the fake go writes "rpc" for it,
			// and only the script's own step is to have it. The same holds
			// of GOOS, which a landing sets for its linux vet (scriptEnv).
			// The case's own variables come last, so a landing's nowhere
			// base wins over the commit below.
			env := scriptEnv()
			cmd.Env = append(env,
				"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
				"YARDR_BASE="+gitT(t, dir, "rev-parse", "HEAD"),
				"TMPDIR="+tmp, "FAKE="+fake, "FAKE_STEP="+c.step, "FAKE_TIMES="+c.times, "FAKE_LINE="+c.line, "YARDR_RACE_HELD=1")
			cmd.Env = append(cmd.Env, c.env...)
			b, err := cmd.CombinedOutput()
			out := string(b)
			if (err == nil) != c.ok {
				t.Errorf("err %v, want ok %v", err, c.ok)
			}
			// A landing's socket step runs beside its go test -race, so which
			// of the two go is called for first is not told: the socket
			// step's calls are compared after the others, where its output is.
			calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
			var ran, beside []string
			for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if strings.HasPrefix(call, "rpc ") {
					beside = append(beside, call)
				} else {
					ran = append(ran, call)
				}
			}
			if got, want := strings.Join(append(ran, beside...), "\n"), strings.Join(c.calls, "\n"); got != want {
				t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
			}
			// The socket step's output is whole and after the other's: none
			// of it before the line that says how long go test -race took.
			if at := strings.Index(out, "ran rpc test"); at >= 0 {
				if end := strings.Index(out, "test: go test -race ./... took"); end < 0 || at < end {
					t.Errorf("the socket step's output is not after the whole of go test -race's")
				}
			}
			if n := strings.Count(out, c.line); n != c.lines {
				t.Errorf("the output has the failing line %d times, want %d", n, c.lines)
			}
			if strings.Contains(out, notice) != c.again {
				t.Errorf("the output says it ran again: %v, want %v", !c.again, c.again)
			}
			// One line a step says how long it took, both runs of it where
			// there were two: gofmt's, then go's in the order they ran.
			// The sleep budget is timed too, after the build and before
			// the tests.
			steps := []string{"gofmt -l ."}
			for _, call := range slices.Compact(slices.Clone(c.calls)) {
				call = "go " + call
				if after, ok := strings.CutPrefix(call, "go linux "); ok {
					call = "env GOOS=linux go " + after
				} else if after, ok := strings.CutPrefix(call, "go rpc "); ok {
					call = "env YARDR_TEST_RPC=1 go " + after
				}
				steps = append(steps, call)
			}
			for i, step := range steps {
				if strings.HasPrefix(step, "go test ") {
					steps = slices.Insert(steps, i, "sleep budgets")
					break
				}
			}
			var took []string
			for _, m := range regexp.MustCompile(`(?m)^test: (.*) took \d+ s$`).FindAllStringSubmatch(out, -1) {
				took = append(took, m[1])
			}
			if !slices.Equal(took, steps) {
				t.Errorf("the steps the output times:\n%s\nwant:\n%s", strings.Join(took, "\n"), strings.Join(steps, "\n"))
			}
			if strings.Contains(out, "test: FAILED") != (c.summed != "") || !strings.Contains(out, c.summed) {
				t.Errorf("the output's summary of a failed step: want %q", c.summed)
			}
			if t.Failed() {
				t.Logf("output:\n%s", out)
			}
			// The log the step's output was kept in is gone again.
			if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) > 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}

// A go test step that fails runs the failed packages (the FAIL lines) once
// more, alone, with the same flags: when they pass, the step passes, and one
// line starting with `test: flaky:` names the tests that failed the first
// time. A package that fails twice fails the step as before, with the failed
// lines. The plain run does the same as a landing. A run whose log reports a
// data race is never run again: the step fails, naming packages whose output
// reported the race. A test the flaky log already names is filed as a
// backlog P3 Flaky bead carrying its lines, unless an open bead already
// carries the title; a test that flaked 3 times in 7 days fails the step
// instead of passing on its rerun. Where the yardr CLI is not there the
// log line stays the record.
func TestYardTestRerunsFailedPackagesOnce(t *testing.T) {
	t.Parallel()
	const pkg = "m.test/m"
	const test = "TestX"
	flakyFail := "--- FAIL: " + test + " (0.00s)\nFAIL\nFAIL\t" + pkg + " 0.123s"
	raceFail := "WARNING: DATA RACE\n" + flakyFail
	racePkg, ordinaryPkg := pkg+"/race", pkg+"/ordinary"
	raceFailMixed := "WARNING: DATA RACE\n--- FAIL: TestRace (0.00s)\nFAIL\nFAIL\t" + racePkg + " 0.123s\n" +
		"--- FAIL: TestOrdinary (0.00s)\nFAIL\nFAIL\t" + ordinaryPkg + " 0.123s"
	running := "test: running the failed packages again: " + pkg
	norace := "test: not running the failed packages again: DATA RACE in " + pkg
	title := "Flaky: " + test + " (" + pkg + ")"
	other := "m-1 Something else\n"
	plainSteps := []string{"gofmt -l .", "go vet ./...", "go build ./...", "sleep budgets", "go test -race ./..."}
	landingSteps := []string{"gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "go test -race ./...",
		"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}
	landing := []string{"vet ./...", "linux vet ./...", "test -race ./...", "test -race " + pkg, "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}
	plain := []string{"vet ./...", "build ./...", "test -race ./...", "test -race " + pkg}
	landingNoRetry := []string{"vet ./...", "linux vet ./...", "test -race ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}
	plainNoRetry := []string{"vet ./...", "build ./...", "test -race ./..."}
	noBase := []string{"YARDR_LANDING=1", "YARDR_BASE=nowhere"}
	for _, c := range []struct {
		name     string
		testName string // override the failed test name
		glob     bool   // add a path that an accidental test-name glob would match
		times    string // how often the test step fails: once (flaky) or twice
		ok       bool
		calls    []string
		env      []string // variables of the script's environment beyond the base
		steps    []string
		line     string          // the failure the step fails with: empty for a flake
		racePkgs string          // packages whose output includes the race warning
		raced    bool            // the failure reports a race, so there is no rerun
		seed     []time.Duration // ages of the flaky log's prior sightings
		yardr    bool            // a stub yardr is on PATH, answering list and show
		list     string          // what the stub's bead list prints
		show     string          // what the stub's bead show prints
		barePath bool            // PATH holds no yardr at all, as CI's does not
		create   bool            // the run files the flake through the stub
		depot    string          // the --depot the filing names: empty for none
		strike   bool            // the flake is a third in 7 days: the step fails
	}{
		{name: "plain: passes on rerun", times: "1", ok: true, calls: plain, steps: plainSteps},
		{name: "plain: fails twice", times: "2", ok: false, calls: plain, steps: plainSteps},
		{name: "plain: a race fails without a retry", times: "1", ok: false, calls: plainNoRetry, steps: plainSteps,
			line: raceFail, racePkgs: pkg, raced: true},
		{name: "plain: a race names only the racing package", times: "1", ok: false, calls: plainNoRetry, steps: plainSteps,
			line: raceFailMixed, racePkgs: racePkg, raced: true},
		// A repeat files through the stub: without a depot where the
		// session names none, into YARDR_DEPOT where it does, else into
		// its bead's depot. An open bead carrying the title files
		// nothing; a third sighting in 7 days fails the step. Sightings
		// of any age file, but only recent ones strike.
		{name: "plain: a repeat flake files a bead", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour}, yardr: true, list: other, create: true},
		{name: "plain: test name glob characters stay literal", testName: "TestX*", glob: true, times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour}, yardr: true, list: other, create: true},
		{name: "plain: a repeat flake files into YARDR_DEPOT", times: "1", ok: true, calls: plain, steps: plainSteps,
			env: []string{"YARDR_DEPOT=m"}, seed: []time.Duration{24 * time.Hour}, yardr: true, list: other, create: true, depot: "m"},
		{name: "plain: a repeat flake files into its bead's depot", times: "1", ok: true, calls: plain, steps: plainSteps,
			env: []string{"YARDR_BEAD=x"}, seed: []time.Duration{24 * time.Hour}, yardr: true, list: other, show: "  depot m\n", create: true, depot: "m"},
		{name: "plain: an open Flaky bead files nothing", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour}, yardr: true, list: other + "m-2 " + title + "\n"},
		{name: "plain: a third strike in 7 days fails the step", times: "1", ok: false, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour, 48 * time.Hour}, yardr: true, list: other + "m-2 " + title + "\n", strike: true},
		{name: "plain: an old repeat still files", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{30 * 24 * time.Hour}, yardr: true, list: other, create: true},
		{name: "plain: old sightings do not strike", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{8 * 24 * time.Hour, 9 * 24 * time.Hour}, yardr: true, list: other + "m-2 " + title + "\n"},
		{name: "plain: without the yardr CLI a repeat only logs", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour}, barePath: true},
		// Landings without a comparable base (CI's shape): the socket
		// suites run whole, ungated, so these cases exercise the leg
		// itself rather than the gate.
		{name: "landing: passes on rerun", times: "1", ok: true, calls: landing, env: noBase, steps: landingSteps},
		{name: "landing: fails twice", times: "2", ok: false, calls: landing, env: noBase, steps: landingSteps},
		{name: "landing: a race fails without a retry", times: "1", ok: false, calls: landingNoRetry, env: noBase, steps: landingSteps,
			line: raceFail, racePkgs: pkg, raced: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			testName := test
			if c.testName != "" {
				testName = c.testName
			}
			flaky := "test: flaky: " + testName + " (" + pkg + ") passed on rerun"
			caseTitle := "Flaky: " + testName + " (" + pkg + ")"
			script, err := os.ReadFile(yardTest)
			if err != nil {
				t.Fatal(err)
			}
			fake, dir := t.TempDir(), t.TempDir()
			bins := map[string]string{"go": fakeGo, "gofmt": "#!/bin/sh\n"}
			if c.yardr {
				bins["yardr"] = fakeYardr
			}
			for name, text := range bins {
				if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tmp := filepath.Join(fake, "tmp")
			if err := os.Mkdir(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			mod := filepath.Join(dir, "go.mod")
			if err := os.WriteFile(mod, []byte("module m.test/m\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitT(t, dir, "init", "-q", "-b", "main")
			gitT(t, dir, "add", "-A")
			gitT(t, dir, "commit", "-q", "-m", "base")
			if err := os.WriteFile(mod, []byte("module m.test/m\n\ngo 1.21\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if c.glob {
				if err := os.WriteFile(filepath.Join(dir, "TestX-match"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			common := gitT(t, dir, "rev-parse", "--git-common-dir")
			if !filepath.IsAbs(common) {
				common = filepath.Join(dir, common)
			}
			flakyLog := filepath.Join(common, "yardr-flaky.log")
			// Prior sightings, at the short sha the run appends under, so
			// the log's lines all read as the gate's own.
			if len(c.seed) > 0 {
				sha := gitT(t, dir, "rev-parse", "--short", "HEAD")
				var seeds []string
				for _, age := range c.seed {
					seeds = append(seeds, time.Now().Add(-age).Format("2006-01-02 15:04:05")+" "+sha+" "+testName+" ("+pkg+") passed on rerun")
				}
				if err := os.WriteFile(flakyLog, []byte(strings.Join(seeds, "\n")+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", "-c", string(script))
			cmd.Dir = dir
			// The case's own variables come last, so a landing's nowhere
			// base wins over the commit below. The bead and depot of the
			// run this test is in are cleared first: the script files a
			// repeat into them, and the test's own would leak in.
			line := c.line
			if line == "" {
				line = "--- FAIL: " + testName + " (0.00s)\nFAIL\nFAIL\t" + pkg + " 0.123s"
			}
			env := scriptEnv()
			pathEnv := "PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH")
			if c.barePath {
				pathEnv = "PATH=" + fake + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin"
			}
			cmd.Env = append(env, pathEnv,
				"YARDR_BASE="+gitT(t, dir, "rev-parse", "HEAD"),
				"YARDR_BEAD=", "YARDR_DEPOT=",
				"TMPDIR="+tmp, "FAKE="+fake, "FAKE_STEP=test", "FAKE_TIMES="+c.times, "FAKE_LINE="+line, "YARDR_RACE_HELD=1")
			if c.yardr {
				cmd.Env = append(cmd.Env, "YARDR_FAKE_LIST="+c.list, "YARDR_FAKE_SHOW="+c.show)
			}
			cmd.Env = append(cmd.Env, c.env...)
			b, err := cmd.CombinedOutput()
			out := string(b)
			if (err == nil) != c.ok {
				t.Errorf("err %v, want ok %v", err, c.ok)
			}
			calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
			var ran, beside []string
			for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if strings.HasPrefix(call, "rpc ") {
					beside = append(beside, call)
				} else {
					ran = append(ran, call)
				}
			}
			if got, want := strings.Join(append(ran, beside...), "\n"), strings.Join(c.calls, "\n"); got != want {
				t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
			}
			// The rerun is said before it: the failed packages, again. A
			// race is said instead: the failed packages, not run again.
			if c.raced {
				if strings.Contains(out, running+"\n") {
					t.Errorf("the output runs a race again")
				}
				raceMessage := strings.Replace(norace, pkg, c.racePkgs, 1)
				if !strings.Contains(out, raceMessage+"\n") {
					t.Errorf("the output has no %q line", raceMessage)
				}
			} else if !strings.Contains(out, running+"\n") {
				t.Errorf("the output has no %q line", running)
			}
			if c.ok || c.strike {
				// One flaky line names the test and its package; a strike
				// fails the step after it.
				n := 0
				for _, line := range strings.Split(out, "\n") {
					if strings.HasPrefix(line, "test: flaky:") {
						n++
						if line != flaky {
							t.Errorf("flaky line %q, want %q", line, flaky)
						}
					}
				}
				if n != 1 {
					t.Errorf("the output has %d flaky lines, want 1", n)
				}
				// One log line per printed flaky line, in the log the repo
				// owns: the date, the short sha, and the names the printed
				// line carries. A strike's own sighting is logged with the
				// priors.
				logb, err := os.ReadFile(flakyLog)
				if err != nil {
					t.Fatal(err)
				}
				var logged []string
				if s := strings.TrimSpace(string(logb)); s != "" {
					logged = strings.Split(s, "\n")
				}
				if len(logged) != n+len(c.seed) {
					t.Errorf("the flaky log has %d lines, want one per printed flaky line (%d) plus the %d priors", len(logged), n, len(c.seed))
				}
				sha := gitT(t, dir, "rev-parse", "--short", "HEAD")
				dated := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} `)
				for _, line := range logged {
					for _, want := range []string{sha, pkg, testName} {
						if !strings.Contains(line, want) {
							t.Errorf("flaky log line %q, want it to carry %q", line, want)
						}
					}
					if !dated.MatchString(line) {
						t.Errorf("flaky log line %q, want it to start with the date", line)
					}
				}
				if c.strike {
					if want := "test: " + testName + " flaked 3 times in 7 days; see the Flaky: bead\n"; !strings.Contains(out, want) {
						t.Errorf("the output has no %q line", strings.TrimSuffix(want, "\n"))
					}
					if !strings.Contains(out, "test: FAILED: go test -race ./...\n") {
						t.Errorf("a striking step is not followed by a failure summary")
					}
				} else if strings.Contains(out, "test: FAILED") {
					t.Errorf("a step that passed on rerun is followed by a failure summary")
				}
			} else {
				if strings.Contains(out, "test: flaky:") {
					t.Errorf("a step that failed twice names a flake")
				}
				// No flaky line printed, so no log line either.
				if _, err := os.Stat(flakyLog); !os.IsNotExist(err) {
					t.Errorf("a step that failed twice wrote the flaky log")
				}
				if c.raced {
					// The race is the first line the summary shows, before
					// the failed test and package.
					want := "test: FAILED: go test -race ./...\nWARNING: DATA RACE"
					if !strings.Contains(out, want) || !strings.Contains(out, "FAIL\t"+pkg) {
						t.Errorf("the output's summary of a failed step: want %q and a FAIL line for %s", want, pkg)
					}
				} else {
					want := "test: FAILED: go test -race ./...\n--- FAIL: " + testName
					if !strings.Contains(out, want) || !strings.Contains(out, "FAIL\t"+pkg) {
						t.Errorf("the output's summary of a failed step: want %q and a FAIL line for %s", want, pkg)
					}
				}
			}
			// A repeat files a backlog P3 carrying the title, into the
			// session's depot or none; a single sighting, an open Flaky
			// bead, and no CLI file nothing. The body carries the test's
			// lines of the log, and the filed line the minted id.
			ycalls, _ := os.ReadFile(filepath.Join(fake, "yardr-calls"))
			var created string
			lists, shows := 0, 0
			for _, call := range strings.Split(strings.TrimSpace(string(ycalls)), "\n") {
				switch {
				case strings.HasPrefix(call, "bead create "):
					created = call
				case call == "bead list":
					lists++
				case strings.HasPrefix(call, "bead show "):
					shows++
				case call != "":
					t.Errorf("the stub was run for %q", call)
				}
			}
			if (created != "") != c.create {
				t.Errorf("the run filed the flake: %v, want %v", created != "", c.create)
			}
			if c.create {
				for _, want := range []string{caseTitle, "--stage backlog -p 3 -b -"} {
					if !strings.Contains(created, want) {
						t.Errorf("the create call %q, want it to carry %q", created, want)
					}
				}
				if c.depot != "" {
					if !strings.Contains(created, "--depot "+c.depot) {
						t.Errorf("the create call %q, want it to name --depot %s", created, c.depot)
					}
				} else if strings.Contains(created, "--depot") {
					t.Errorf("the create call %q, want no --depot", created)
				}
				body, _ := os.ReadFile(filepath.Join(fake, "yardr-body"))
				var carried []string
				if s := strings.TrimSpace(string(body)); s != "" {
					carried = strings.Split(s, "\n")
				}
				if len(carried) != len(c.seed)+1 {
					t.Errorf("the filed body has %d lines, want the %d sightings", len(carried), len(c.seed)+1)
				}
				for _, line := range carried {
					if !strings.Contains(line, testName+" ("+pkg+")") || !strings.Contains(line, "passed on rerun") {
						t.Errorf("the filed body line %q, want the test's log line", line)
					}
				}
				if want := "test: filed m-filed: " + caseTitle + "\n"; !strings.Contains(out, want) {
					t.Errorf("the output has no %q line", strings.TrimSuffix(want, "\n"))
				}
			} else if strings.Contains(out, "test: filed") {
				t.Errorf("the output files a bead where none was to be filed")
			}
			if c.yardr {
				// Every repeat consults the open beads once; only a case
				// whose bead names a depot asks the CLI for it.
				if lists != 1 {
					t.Errorf("the run listed beads %d times, want 1", lists)
				}
				wantShows := 0
				if c.show != "" {
					wantShows = 1
				}
				if shows != wantShows {
					t.Errorf("the run showed a bead %d times, want %d", shows, wantShows)
				}
			} else if len(strings.TrimSpace(string(ycalls))) != 0 {
				t.Errorf("the run called yardr %q with none on PATH", strings.TrimSpace(string(ycalls)))
			}
			if c.barePath && strings.Contains(out, "test: warning") {
				t.Errorf("a run with no CLI warns, want it to log only")
			}
			var took []string
			for _, m := range regexp.MustCompile(`(?m)^test: (.*) took \d+ s$`).FindAllStringSubmatch(out, -1) {
				took = append(took, m[1])
			}
			if !slices.Equal(took, c.steps) {
				t.Errorf("the steps the output times:\n%s\nwant:\n%s", strings.Join(took, "\n"), strings.Join(c.steps, "\n"))
			}
			if t.Failed() {
				t.Logf("output:\n%s", out)
			}
			if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) > 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}

// The run ends with its own test line (docs/testing.md R4), the line a note
// carries whole: the result, the packages passed and failed, the wall
// seconds, and the seconds queued on the race lock, at the short sha. A
// package that passed on rerun counts as passed; a plan and a run that
// could not start print none.
func TestYardTestPrintsItsTestLine(t *testing.T) {
	const flaky = "test: flaky: TestX (m.test/m/a) passed on rerun"
	line := regexp.MustCompile(`^test: sh \.yardr/test @ ([0-9a-f]+): (pass|fail), ([0-9]+) packages ok, ([0-9]+) failed, ([0-9]+) s, waited ([0-9]+) s$`)
	for _, c := range []struct {
		name  string
		fails string // how often the test step fails: 0 (green), 1 (flaky), 2 (red)
		wait  string // a race-lock wait the step reports, "" for none
		code  int
		res   string // want result
		pok   string // want packages ok
		pfail string // want failed
		pwait string // want waited seconds
		flaky bool
		env   []string // variables of the script's environment beyond the base
		nosum bool     // want no test line at all
	}{
		{"green", "0", "", 0, "pass", "2", "0", "0", false, nil, false},
		{"a wait is summed", "0", "3", 0, "pass", "2", "0", "3", false, nil, false},
		{"a pass on rerun counts as passed", "1", "", 0, "pass", "2", "0", "0", true, nil, false},
		{"red", "2", "", 1, "fail", "0", "1", "0", false, nil, false},
		{"a plan prints none", "0", "", 0, "", "", "", "", false, []string{"YARDR_TEST_PLAN=1"}, true},
		{"exit 2 prints none", "0", "", 2, "", "", "", "", false, []string{"YARDR_BASE=nowhere"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			script, err := os.ReadFile(yardTest)
			if err != nil {
				t.Fatal(err)
			}
			fake, dir := t.TempDir(), t.TempDir()
			// A go whose test step reports a wait the way the race lock
			// would, then fails its first few runs with one package's
			// FAIL lines and passes with two packages' ok lines.
			goFake := `#!/bin/sh
if [ "$1" = test ]; then
  n=$(cat "$FAKE/n" 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" >"$FAKE/n"
  [ -z "$FAKE_WAIT" ] || echo "test: waited $FAKE_WAIT s for the race lock"
  if [ "$n" -le "$FAKE_FAILS" ]; then
    echo "--- FAIL: TestX (0.00s)"
    echo "FAIL"
    printf 'FAIL\tm.test/m/a 0.123s\n'
    exit 1
  fi
  printf 'ok  \tm.test/m/a\t1.234s\nok  \tm.test/m/b\t0.123s\n'
fi
`
			for name, text := range map[string]string{"go": goFake, "gofmt": "#!/bin/sh\n"} {
				if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tmp := filepath.Join(fake, "tmp")
			if err := os.Mkdir(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			// A branch that changed go.mod: a plain run tests every
			// package, and asks go for no list of them.
			mod := filepath.Join(dir, "go.mod")
			if err := os.WriteFile(mod, []byte("module m.test/m\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitT(t, dir, "init", "-q", "-b", "main")
			gitT(t, dir, "add", "-A")
			gitT(t, dir, "commit", "-q", "-m", "base")
			if err := os.WriteFile(mod, []byte("module m.test/m\n\ngo 1.21\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			short := gitT(t, dir, "rev-parse", "--short", "HEAD")
			cmd := exec.Command("sh", "-c", string(script))
			cmd.Dir = dir
			// The case's own variables come last, so a landing's nowhere
			// base wins over the commit below.
			env := scriptEnv()
			cmd.Env = append(env,
				"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
				"YARDR_BASE="+gitT(t, dir, "rev-parse", "HEAD"),
				"TMPDIR="+tmp, "FAKE="+fake, "FAKE_FAILS="+c.fails, "FAKE_WAIT="+c.wait, "YARDR_RACE_HELD=1")
			cmd.Env = append(cmd.Env, c.env...)
			b, err := cmd.CombinedOutput()
			out := string(b)
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != c.code {
				t.Errorf("exit %d, want %d", code, c.code)
			}
			if c.nosum {
				if strings.Contains(out, "sh .yardr/test @") {
					t.Errorf("the output has a test line, want none:\n%s", out)
				}
				return
			}
			// The test line closes the run: its last line, at this sha.
			lines := strings.Split(strings.TrimSpace(out), "\n")
			m := line.FindStringSubmatch(lines[len(lines)-1])
			if m == nil {
				t.Fatalf("last line %q, want the test line", lines[len(lines)-1])
			}
			if m[1] != short || m[2] != c.res || m[3] != c.pok || m[4] != c.pfail || m[6] != c.pwait {
				t.Errorf("test line %q, want @ %s: %s, %s packages ok, %s failed, waited %s s",
					lines[len(lines)-1], short, c.res, c.pok, c.pfail, c.pwait)
			}
			if strings.Contains(out, flaky) != c.flaky {
				t.Errorf("the output has the flaky line: %v, want %v", !c.flaky, c.flaky)
			}
			if t.Failed() {
				t.Logf("output:\n%s", out)
			}
			if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) > 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}

// The landing's socket leg runs in a background shell. Its package totals
// must retain the same retry-aware meaning as the foreground leg: a package
// that fails once and passes on retry is passed, not both passed and failed.
func TestYardTestCountsSocketRetryByFinalResult(t *testing.T) {
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	fake, dir := t.TempDir(), t.TempDir()
	goFake := `#!/bin/sh
if [ "$1" = test ] && [ -n "$YARDR_TEST_RPC" ]; then
  n=$(cat "$FAKE/rpc.n" 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" >"$FAKE/rpc.n"
  if [ "$n" -eq 1 ]; then
    printf 'ok  m.test/m/b\t1.234s\n'
    echo '--- FAIL: TestX (0.00s)'
    echo 'FAIL'
    printf 'FAIL\tm.test/m/a 0.123s\n'
    exit 1
  fi
  printf 'ok  m.test/m/a\t1.234s\n'
elif [ "$1" = test ]; then
  printf 'ok  m.test/m/a\t1.234s\nok  m.test/m/b\t0.123s\n'
fi
`
	for name, text := range map[string]string{"go": goFake, "gofmt": "#!/bin/sh\n"} {
		if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m.test/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	cmd := exec.Command("sh", "-c", string(script))
	cmd.Dir = dir
	cmd.Env = append(scriptEnv(),
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"YARDR_BASE=nowhere", "YARDR_LANDING=1", "YARDR_RACE_HELD=1", "FAKE="+fake)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("landing run: %v\n%s", err, b)
	}
	line := regexp.MustCompile(`(?m)^test: sh \.yardr/test @ [0-9a-f]+: pass, 4 packages ok, 0 failed, [0-9]+ s, waited 0 s$`)
	if !line.Match(b) {
		t.Fatalf("summary does not report four passed package results and no final failures:\n%s", b)
	}
}

// The race leg runs under one machine-wide lock: a gate whose race step
// starts while another holds the lock waits for it, holds it for the run,
// then releases it, and says how long it waited. The test holds the lock
// past the run's build step, its last step before the race leg, so the wait
// spans whole seconds however slowly the run started under load; its own
// home stands in for the yard's, so no real gate waits on it. Once the
// race leg starts, the lock is the run's until its budget checks end: the
// test cannot take it during the race run or its isolated rerun, and can
// once the leg has ended. Vet and build run before the lock is held.
func TestYardTestRaceLegWaitsForTheLock(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	lock := filepath.Join(home, ".yardr", "race.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(lock, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("holding the lock: %v", err)
	}
	fake, dir := t.TempDir(), t.TempDir()
	for name, text := range map[string]string{
		"go": `#!/bin/sh
echo "$*" >>"$FAKE/calls"
if [ "$1" = test ]; then
  case "$*" in
  *"-count=1"*)
    echo 'ok  yardr.dev/yardr/internal/dispatch 12.3s'
    sleep 2
    ;;
  *)
    sleep 2
    echo 'ok  yardr.dev/yardr/internal/dispatch 1m1.2s'
    ;;
  esac
fi
`,
		"gofmt": "#!/bin/sh\n",
	} {
		if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tmp := filepath.Join(fake, "tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	// A branch that changed go.mod: a plain run tests every package, and
	// asks go for no list of them.
	mod := filepath.Join(dir, "go.mod")
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".yardr", "test"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mod, []byte("module yardr.dev/yardr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	if err := os.WriteFile(mod, []byte("module yardr.dev/yardr\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", ".yardr/test")
	cmd.Dir = dir
	cmd.Env = append(scriptEnv(),
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"YARDR_BASE="+gitT(t, dir, "rev-parse", "HEAD"),
		"HOME="+home, "TMPDIR="+tmp, "FAKE="+fake)
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.CombinedOutput()
		close(done)
	}()
	// The run's build step is its last before the race leg: hold the lock
	// until the run is queued and its ticket is a second old, measured,
	// so the wait is real by construction and spans whole seconds.
	callsPath := filepath.Join(fake, "calls")
	waitFor := func(what string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			calls, _ := os.ReadFile(callsPath)
			if strings.Contains(string(calls), what) {
				return
			}
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				<-done
				t.Fatalf("the run never reached %s, output:\n%s", what, out)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitFor("build ./...")
	// The queue publishes the wait as data: a ticket file. Seeing the
	// ticket alone is not enough — the wait timer starts before the
	// ticket is made, so a release before the first lock attempt leaves
	// a zero wait and no wait line. Hold until the ticket is a second
	// old, measured from its first sight, so the first attempt has run
	// while held and the summary's waited seconds are positive.
	qdir := filepath.Join(home, ".yardr", "race.queue")
	var firstSeen time.Time
	ticketDeadline := time.Now().Add(60 * time.Second)
	for {
		seen := false
		if entries, err := os.ReadDir(qdir); err == nil {
			for _, e := range entries {
				if raceTicketName.MatchString(e.Name()) {
					seen = true
					break
				}
			}
		}
		if seen {
			firstSeen = time.Now()
			break
		}
		if time.Now().After(ticketDeadline) {
			cmd.Process.Kill()
			<-done
			t.Fatalf("the run never queued for the race lock, output:\n%s", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if wait := time.Until(firstSeen.Add(1100 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	// The race leg starts once it has the lock. It keeps it through the
	// package-budget rerun, so the test cannot take it during either run.
	waitFor("test -race")
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Errorf("took the lock while the race run holds it")
	}
	waitFor("test -race -count=1")
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Errorf("took the lock while the package-budget rerun holds it")
	}
	<-done
	if runErr != nil {
		t.Fatalf("err %v, output:\n%s", runErr, out)
	}
	waits := regexp.MustCompile(`(?m)^test: waited \d+ s for the race lock$`).FindAll(out, -1)
	if len(waits) != 1 {
		t.Errorf("the output has %d wait lines, want 1:\n%s", len(waits), out)
	}
	if !raceWaitStarted.Match(out) {
		t.Errorf("the output has no start marker for the active race-lock wait:\n%s", out)
	}
	summary := regexp.MustCompile(`(?m)^test: sh \.yardr/test @ [0-9a-f]+: pass, [0-9]+ packages ok, 0 failed, [0-9]+ s, waited [1-9][0-9]* s$`)
	if !summary.MatchString(string(out)) {
		t.Errorf("the summary does not include the race-lock wait:\n%s", out)
	}
	calls, _ := os.ReadFile(callsPath)
	if got, want := strings.TrimSpace(string(calls)), "vet ./...\nbuild ./...\ntest -race ./...\ntest -race -count=1 yardr.dev/yardr/internal/dispatch"; got != want {
		t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("cannot take the lock after the run ended: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}

// The race lock is a file of the yard's: a gate that makes the yard's home
// and the lock in it makes them as the yard makes its own, the home and the
// ticket queue 0700 and the lock file 0600, whatever the umask.
func TestYardTestRaceLockIsTheYards(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	home, fake, dir := t.TempDir(), t.TempDir(), t.TempDir()
	for name, text := range map[string]string{
		"go":    "#!/bin/sh\n",
		"gofmt": "#!/bin/sh\n",
	} {
		if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tmp := filepath.Join(fake, "tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	// A branch that changed go.mod: a plain run tests every package.
	mod := filepath.Join(dir, "go.mod")
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".yardr", "test"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mod, []byte("module m.test/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	if err := os.WriteFile(mod, []byte("module m.test/m\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "umask 022; sh .yardr/test")
	cmd.Dir = dir
	cmd.Env = append(scriptEnv(),
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"YARDR_BASE="+gitT(t, dir, "rev-parse", "HEAD"),
		"HOME="+home, "TMPDIR="+tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("err %v, output:\n%s", err, out)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(home, ".yardr"):               0o700,
		filepath.Join(home, ".yardr", "race.lock"):  0o600,
		filepath.Join(home, ".yardr", "race.queue"): 0o700,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s is %o, want %o", path, got, want)
		}
	}
}

// The race lock dies with the run, whatever the run leaves behind: a run
// under racelocked that backgrounds a long sleep and exits releases the
// lock at once, so a second run gets it at its first attempt (an orphaned
// sleep once held it past the run: yardr-tsms). A run that has to queue says
// so before its first sleep. How long one takes is the machine's and is not
// asserted: it starts a ps for every look at a pid, and those starts took a
// second run 2.5 s beside a race suite (yardr-q9vn) and 1.0 to 3.4 s at load
// 60 to 80, none of them queued. The holder also ends as a shell would: the
// run's exit status, 128+n for a run ended by signal n, and a TERM to the
// holder stops the run with it. The holder speaks the worker protocol, status
// file first and the run re-execed with YARDR_TEST_RACE_LEG; the probe answers
// as a worker that runs no leg, so it pins the holder alone. Its own home
// stands in for the yard's, so no real gate waits on it.
func TestYardTestRaceLockDiesWithTheRun(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	b, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	// The function as the script has it, to the next function: a script
	// the cuts no longer follow fails the test, telling its editor to
	// move the probe with it.
	_, fn, ok := strings.Cut(string(b), "racelocked() {")
	if ok {
		fn, _, ok = strings.Cut("racelocked() {"+fn, "\nagain() {")
	}
	if !ok || !strings.Contains(fn, "race.lock") || !strings.Contains(fn, "flock(") || !strings.Contains(fn, "YARDR_TEST_RACE_LEG") {
		t.Fatalf("cannot cut racelocked out of %s: move the probe with the script", yardTest)
	}
	home := t.TempDir()
	statusFile := filepath.Join(home, "status")
	driver := filepath.Join(t.TempDir(), "driver.sh")
	// The probe worker runs no leg: it drops the status file and runs
	// the command, and the file stays unwritten.
	if err := os.WriteFile(driver, []byte("#!/bin/sh\n"+fn+"\nif [ -n \"$YARDR_TEST_RACE_LEG\" ]; then shift; exec \"$@\"; fi\nracelocked \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	run := func(ctx context.Context, args ...string) (int, []byte) {
		t.Helper()
		// Into a file, not a pipe: a run that orphans a sleep would hold
		// a pipe open past its own exit, and the wait below with it.
		outFile, err := os.CreateTemp("", "racelocked")
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(outFile.Name())
		cmd := exec.CommandContext(ctx, driver, append([]string{statusFile}, args...)...)
		cmd.Env = append(scriptEnv(), "HOME="+home)
		cmd.Stdout, cmd.Stderr = outFile, outFile
		err = cmd.Run()
		outFile.Close()
		out, _ := os.ReadFile(outFile.Name())
		if err == nil {
			return 0, out
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), out
		}
		t.Fatalf("racelocked %v: %v\n%s", args, err, out)
		return 0, nil
	}
	pidOf := func(path string) int {
		t.Helper()
		p, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(p)))
		if err != nil {
			t.Fatalf("%s holds %q, not a pid", path, p)
		}
		return pid
	}
	// A run that backgrounds a long sleep and exits: the sleep outlives
	// it, and the lock must not.
	pidFile := filepath.Join(home, "sleeper")
	t.Cleanup(func() {
		if p, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(p))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	if code, out := run(context.Background(), "sh", "-c", "sleep 300 & echo $! >"+quote(pidFile)); code != 0 {
		t.Fatalf("the run that orphans a sleep exits %d, want 0:\n%s", code, out)
	}
	if pid := pidOf(pidFile); syscall.Kill(pid, 0) != nil {
		t.Fatalf("the backgrounded sleep (pid %d) is already gone: the run below proves nothing", pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, out := run(ctx, "true")
	if ctx.Err() != nil {
		t.Fatalf("a second run waits past 30 s for a lock the first left behind:\n%s", out)
	}
	if code != 0 {
		t.Fatalf("a second run exits %d, want 0:\n%s", code, out)
	}
	if raceWaitStarted.Match(out) {
		t.Errorf("a second run had to queue for a lock the first left behind:\n%s", out)
	}
	// The holder reports the run's end as a shell would.
	if code, out := run(context.Background(), "sh", "-c", "exit 3"); code != 3 {
		t.Errorf("a run that exits 3 is reported as %d:\n%s", code, out)
	}
	if code, out := run(context.Background(), "sh", "-c", "kill -TERM $$"); code != 143 {
		t.Errorf("a run ended by TERM is reported as %d, want 143:\n%s", code, out)
	}
	// A TERM to the holder stops the run with it: the run TERMs its
	// parent, the holder, and the holder must stop the worker's whole
	// process group, including a child the run leaves in the background.
	victim := filepath.Join(home, "victim")
	victimChild := filepath.Join(home, "victim-child")
	t.Cleanup(func() {
		for _, path := range []string{victim, victimChild} {
			if p, err := os.ReadFile(path); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(p))); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
	})
	code, out = run(context.Background(), "sh", "-c", "echo $$ >"+quote(victim)+"; sleep 30 & echo $! >"+quote(victimChild)+"; kill -TERM $PPID; wait")
	if code != 143 {
		t.Fatalf("a run that TERMs its holder is reported as %d, want 143:\n%s", code, out)
	}
	if pid := pidOf(victim); syscall.Kill(pid, 0) == nil {
		t.Errorf("the run (pid %d) survived the TERM to its holder: it was not forwarded", pid)
	}
	childPID := pidOf(victimChild)
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(childPID, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(childPID, 0) == nil {
		t.Errorf("the run's child (pid %d) survived the TERM to its holder", childPID)
	}
}

// The package budget's re-run shares the race leg's hold on the lock: a
// package over budget in the run is run again alone, with the leg's flags
// and -count=1, and judged by the re-run, and the leg takes the lock once,
// for the run, never again for the re-run. The perl is a stand-in that
// logs every hold and runs the rest, so the run takes no real lock.
func TestYardTestBudgetRerunSharesTheRaceHold(t *testing.T) {
	t.Parallel()
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		alone string // the re-run's Go duration: fast passes, over fails
		ok    bool
		want  string // the verdict line
	}{
		{"under alone passes", "12.3s", true,
			"test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 12.3 s alone: under budget, loaded run"},
		{"over alone fails naming both", "1m6.6s", false,
			"test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, 66.6 s alone, over the 60 s budget (docs/testing.md R1)"},
		{"sub-second alone passes", "750ms", true,
			"test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 0.75 s alone: under budget, loaded run"},
		{"microsecond alone passes", "750µs", true,
			"test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 0.00075 s alone: under budget, loaded run"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake, dir := t.TempDir(), t.TempDir()
			// A go whose race leg runs one package over budget and whose
			// re-run of it reports FAKE_ALONE, writing down every call
			// with YARDR_RACE_HELD; a perl that logs every hold and runs
			// the rest, as racelocked calls it.
			goFake := `#!/bin/sh
echo "$* held=${YARDR_RACE_HELD-unset} lock=${FAKE_LOCK_HELD-unset}" >>"$FAKE/calls"
if [ "$1" = test ]; then
  case "$*" in
  *"-count=1"*) printf 'ok  \tyardr.dev/yardr/internal/dispatch\t%s\n' "$FAKE_ALONE" ;;
  *) printf 'ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n' ;;
  esac
fi
`
			perlFake := `#!/bin/sh
echo "hold $3" >>"$FAKE/locks"
shift 3
FAKE_LOCK_HELD=1
export FAKE_LOCK_HELD
YARDR_RACE_HELD=1
YARDR_TEST_RACE_LEG=1
export YARDR_RACE_HELD YARDR_TEST_RACE_LEG
exec "$@"
`
			for name, text := range map[string]string{"go": goFake, "perl": perlFake, "gofmt": "#!/bin/sh\n"} {
				if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tmp := filepath.Join(fake, "tmp")
			if err := os.Mkdir(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			// A branch that changed go.mod: a plain run tests every
			// package, and asks go for no list of them.
			mod := filepath.Join(dir, "go.mod")
			if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".yardr", "test"), script, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mod, []byte("module m.test/m\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitT(t, dir, "init", "-q", "-b", "main")
			gitT(t, dir, "add", "-A")
			gitT(t, dir, "commit", "-q", "-m", "base")
			if err := os.WriteFile(mod, []byte("module m.test/m\n\ngo 1.21\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", ".yardr/test")
			cmd.Dir = dir
			// No YARDR_RACE_HELD: the race leg takes the lock itself, as
			// a top-level run does, and its worker keeps that hold through
			// the package re-measurement. The load is pinned quiet, so
			// the over-alone case fails however loaded the test machine
			// is.
			cmd.Env = append(scriptEnv(),
				"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
				"YARDR_BASE="+gitT(t, dir, "rev-parse", "HEAD"),
				"HOME="+home, "TMPDIR="+tmp, "FAKE="+fake, "FAKE_ALONE="+c.alone,
				"YARDR_TEST_LOADAVG=1.5", "YARDR_TEST_CORES=10")
			b, err := cmd.CombinedOutput()
			out := string(b)
			if (err == nil) != c.ok {
				t.Errorf("err %v, want ok %v", err, c.ok)
			}
			if !strings.Contains(out, c.want+"\n") {
				t.Errorf("the output has no verdict line %q", c.want)
			}
			// The re-run is the leg's flags and -count=1 on the one
			// package, inside the hold the leg set.
			calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
			wantCalls := "vet ./... held=unset lock=unset\n" +
				"build ./... held=unset lock=unset\n" +
				"test -race ./... held=1 lock=1\n" +
				"test -race -count=1 yardr.dev/yardr/internal/dispatch held=1 lock=1"
			if got := strings.TrimSpace(string(calls)); got != wantCalls {
				t.Errorf("go was run as:\n%s\nwant:\n%s", got, wantCalls)
			}
			// One hold, for the race leg: the re-run waited on nothing.
			locks, _ := os.ReadFile(filepath.Join(fake, "locks"))
			if got, want := strings.TrimSpace(string(locks)), "hold "+filepath.Join(home, ".yardr", "race.lock"); got != want {
				t.Errorf("the lock was held as:\n%s\nwant:\n%s", got, want)
			}
			if t.Failed() {
				t.Logf("output:\n%s", out)
			}
			if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) > 0 {
				t.Errorf("left behind: %v", left)
			}
		})
	}
}

// raceTicketName matches a queue ticket: <seconds><nanoseconds>-<pid>.
var raceTicketName = regexp.MustCompile(`^\d+-\d+$`)

// raceWaitStarted matches the line a run prints when its first attempt at the
// race lock failed, before its first sleep: the one sign that it queued. The
// "waited N s" line is not, for it counts whole seconds on the clock and a
// run that never queued says it beside a loaded machine when its starts cross
// a second.
var raceWaitStarted = regexp.MustCompile(`(?m)^test: race lock wait started at \d+$`)

// raceLockDriver cuts racelocked out of .yardr/test into a driver script,
// failing when the script no longer holds that shape, and returns the
// driver with a home for it to lock under. Each test locks under its own
// home, never the yard's.
func raceLockDriver(t *testing.T) (driver, home string) {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	b, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	_, fn, ok := strings.Cut(string(b), "racelocked() {")
	if ok {
		fn, _, ok = strings.Cut("racelocked() {"+fn, "\nagain() {")
	}
	if !ok || !strings.Contains(fn, "race.lock") || !strings.Contains(fn, "flock(") || !strings.Contains(fn, "race.queue") || !strings.Contains(fn, "YARDR_TEST_RACE_LEG") {
		t.Fatalf("cannot cut racelocked out of %s: move the probe with the script", yardTest)
	}
	home = t.TempDir()
	driver = filepath.Join(t.TempDir(), "driver.sh")
	// The worker re-enters this driver with YARDR_TEST_RACE_LEG set. Drop
	// its status path and run the command, as the real script's worker does.
	if err := os.WriteFile(driver, []byte("#!/bin/sh\n"+fn+"\nif [ -n \"$YARDR_TEST_RACE_LEG\" ]; then shift; exec \"$@\"; fi\nracelocked \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return driver, home
}

// The race lock serves waiters in arrival order, and says so: waiters
// queued behind a held lock in a known order take it in that order, each
// printing its place and the holder. A waiter killed with KILL while
// queued leaves a ticket the next waiter skips and removes, and one killed
// with TERM removes its own ticket and exits 143. A ticket carries the
// waiter's bead and worktree. Its own home stands in for the yard's, so no
// real gate waits on it.
func TestYardTestRaceLockServesWaitersInOrder(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	driver, home := raceLockDriver(t)
	qdir := filepath.Join(home, ".yardr", "race.queue")
	wt, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	order := filepath.Join(t.TempDir(), "order")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	type raceRun struct {
		cmd *exec.Cmd
		out *os.File
		in  *os.File
	}
	start := func(bead string, args ...string) raceRun {
		t.Helper()
		out, err := os.CreateTemp("", "racequeue")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(out.Name()) })
		in, release, err := os.Pipe()
		if err != nil {
			out.Close()
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, driver, append([]string{filepath.Join(home, "worker.status")}, args...)...)
		cmd.Dir = wt
		cmd.Env = append(scriptEnv(), "HOME="+home, "YARDR_BEAD="+bead)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		if err := cmd.Start(); err != nil {
			in.Close()
			release.Close()
			out.Close()
			t.Fatal(err)
		}
		in.Close()
		return raceRun{cmd, out, release}
	}
	finish := func(q raceRun) (int, []byte) {
		t.Helper()
		q.in.Close()
		err := q.cmd.Wait()
		q.out.Close()
		out, _ := os.ReadFile(q.out.Name())
		if ctx.Err() != nil {
			t.Fatalf("a run waits past 60 s:\n%s", out)
		}
		if err == nil {
			return 0, out
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), out
		}
		t.Fatalf("racelocked: %v\n%s", err, out)
		return 0, nil
	}
	tickets := func() []string {
		t.Helper()
		entries, err := os.ReadDir(qdir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			if raceTicketName.MatchString(e.Name()) {
				names = append(names, e.Name())
			}
		}
		slices.Sort(names)
		return names
	}
	// waitTicket waits for a ticket beyond those known. Starting each
	// waiter after the last one's ticket is visible queues them in a
	// known order, whatever the clock's grain.
	waitTicket := func(known []string) []string {
		t.Helper()
		seen := map[string]bool{}
		for _, n := range known {
			seen[n] = true
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			got := tickets()
			for _, n := range got {
				if !seen[n] {
					return got
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("no new ticket after %v", known)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	killTicket := func(name string, sig syscall.Signal) {
		t.Helper()
		_, s, _ := strings.Cut(name, "-")
		pid, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("ticket %q names no pid", name)
		}
		if err := syscall.Kill(pid, sig); err != nil {
			t.Fatalf("kill %d: %v", pid, err)
		}
	}
	newest := func(names []string) string { return names[len(names)-1] }
	// The holder takes the lock first and waits for the test to release it.
	holder := start("holder-bead", "sh", "-c", "read _ || true")
	known := waitTicket(nil)
	holderFile := filepath.Join(home, ".yardr", "race.holder")
	holderDeadline := time.Now().Add(30 * time.Second)
	for {
		b, err := os.ReadFile(holderFile)
		if err == nil && strings.Contains(string(b), "bead=holder-bead\n") {
			break
		}
		if time.Now().After(holderDeadline) {
			t.Fatalf("the holder never acquired the lock and recorded itself: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A waiter killed with KILL leaves its ticket behind; the queue
	// must skip it.
	dead := start("dead-bead", "sh", "-c", "echo dead >>"+quote(order))
	known = waitTicket(known)
	killTicket(newest(known), syscall.SIGKILL)
	if code, _ := finish(dead); code != 137 {
		t.Errorf("a waiter killed with KILL exits %d, want 137", code)
	}
	w2 := start("w2-bead", "sh", "-c", "echo w2 >>"+quote(order))
	known = waitTicket(known)
	w3 := start("w3-bead", "sh", "-c", "echo w3 >>"+quote(order))
	known = waitTicket(known)
	// A ticket carries the waiter's bead and worktree.
	found := false
	for _, n := range tickets() {
		b, _ := os.ReadFile(filepath.Join(qdir, n))
		if strings.Contains(string(b), "bead=w2-bead\n") {
			found = true
			if !strings.Contains(string(b), "dir="+wt+"\n") {
				t.Errorf("the w2 ticket names no worktree %q:\n%s", wt, b)
			}
		}
	}
	if !found {
		t.Errorf("no ticket carries bead=w2-bead")
	}
	// A waiter killed with TERM removes its own ticket and exits 143.
	term := start("term-bead", "sh", "-c", "echo term >>"+quote(order))
	known = waitTicket(known)
	termTicket := newest(known)
	killTicket(termTicket, syscall.SIGTERM)
	if code, _ := finish(term); code != 143 {
		t.Errorf("a waiter killed with TERM exits %d, want 143", code)
	}
	// Its handler removes the ticket before the process exits, so it is gone
	// the moment the waiter is, with no later pass to reap it (a TERM that
	// came as the ticket showed once left it, which
	// TestYardTestRaceLockTermAsTheTicketShowsLeavesNoTicket pins). The
	// directory is the oracle, not --race-queue, which skips dead tickets
	// without removing them.
	if slices.Contains(tickets(), termTicket) {
		t.Errorf("the TERM killed waiter's ticket is still queued")
	}
	if code, out := finish(holder); code != 0 {
		t.Fatalf("the holder exits %d, want 0:\n%s", code, out)
	}
	w2code, w2out := finish(w2)
	w3code, w3out := finish(w3)
	if w2code != 0 || w3code != 0 {
		t.Fatalf("the waiters exit %d and %d, want 0:\n%s\n%s", w2code, w3code, w2out, w3out)
	}
	// The survivors ran in arrival order, past the killed tickets.
	if body, _ := os.ReadFile(order); string(body) != "w2\nw3\n" {
		t.Errorf("the runs ordered %q, want %q", body, "w2\nw3\n")
	}
	// Each printed its place and the holder while it waited.
	for out, want := range map[string]string{
		string(w2out): `(?m)^test: race lock: 2nd in line, held by holder-bead for \d+ s$`,
		string(w3out): `(?m)^test: race lock: 3rd in line, held by holder-bead for \d+ s$`,
	} {
		if ok, _ := regexp.MatchString(want, out); !ok {
			t.Errorf("a waiter printed no %q:\n%s", want, out)
		}
		if !strings.Contains(out, "test: waited ") {
			t.Errorf("a waiter printed no waited line:\n%s", out)
		}
	}
	// Nothing is left behind: no ticket, live or dead, and no holder.
	if got := tickets(); len(got) != 0 {
		t.Errorf("tickets left behind: %v", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".yardr", "race.holder")); !os.IsNotExist(err) {
		t.Errorf("the holder file is left behind")
	}
	// A pre-seeded dead ticket at the back of the queue is absent after
	// the next acquisition: the acquirer's fullclean reaps it. The seed
	// sorts last by a future timestamp, so front does not reach it, and
	// a live ticket lingering here would convict the perl instead.
	if err := os.MkdirAll(qdir, 0o700); err != nil {
		t.Fatal(err)
	}
	deadPID := raceDeadPID(t)
	now := time.Now().Unix()
	seedName := fmt.Sprintf("%010d%09d-%d", now+3600, 999999999, deadPID)
	seed := fmt.Sprintf("pid=%d\nstart=0\nbead=dead-seed\ndir=%s\ncreated=%d\n", deadPID, wt, now)
	if err := os.WriteFile(filepath.Join(qdir, seedName), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	// No holder blocks it: the waiter takes the lock at once, so its
	// ticket may come and go before a poll sees it. Finish it and read
	// the queue it leaves.
	seedWaiter := start("seed-bead", "true")
	if code, out := finish(seedWaiter); code != 0 {
		t.Fatalf("the seed waiter exits %d, want 0:\n%s", code, out)
	}
	if slices.Contains(tickets(), seedName) {
		t.Errorf("the pre-seeded dead ticket is still queued after the next acquisition")
	}
}

// A waiter that gets TERM while its ticket is being made leaves no queue file
// behind: before the rename it is a .tmp file, after it a ticket. A TERM in
// either gap can be handled between the filesystem operation and the next
// assignment, so the probes send the signal at each point instead of waiting
// for chance. Its own home stands in for the yard's, so no real gate waits on
// it.
func TestYardTestRaceLockTermDuringTicketCreationLeavesNoQueueFiles(t *testing.T) {
	t.Parallel()
	driver, home := raceLockDriver(t)
	b, err := os.ReadFile(driver)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		point string
	}{
		{"before rename", "      $mine = $name;\n"},
		{"after rename", "      return $name;\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(string(b), tc.point) != 1 {
				t.Fatalf("cannot find the %s point in %s: move the probe with the script", tc.name, yardTest)
			}
			probe := filepath.Join(t.TempDir(), "probe.sh")
			patched := strings.Replace(string(b), tc.point, "      kill(\"TERM\", $$);\n"+tc.point, 1)
			if err := os.WriteFile(probe, []byte(patched), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(probe, filepath.Join(home, "worker.status"), "true")
			cmd.Env = append(scriptEnv(), "HOME="+home)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 143 {
				t.Fatalf("a waiter that TERMs itself %s: %v, want exit 143:\n%s", tc.name, err, out)
			}
			entries, err := os.ReadDir(filepath.Join(home, ".yardr", "race.queue"))
			if err != nil {
				t.Fatalf("no queue: the TERM came before mkticket made one: %v", err)
			}
			for _, e := range entries {
				if raceTicketName.MatchString(e.Name()) || strings.HasPrefix(e.Name(), ".tmp-") {
					t.Errorf("queue file %s remains after the waiter was TERMed %s", e.Name(), tc.name)
				}
			}
		})
	}
}

// raceOwnStart is this process's start time as the queue's perl reads it:
// the lstart ps reports, or the starttime in /proc/<pid>/stat.
func raceOwnStart(t *testing.T, pid int) string {
	t.Helper()
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			t.Fatalf("unexpected /proc stat %q", s)
		}
		if f := strings.Fields(s[i+1:]); len(f) >= 20 {
			return f[19]
		}
		t.Fatalf("unexpected /proc stat %q", s)
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if start := strings.TrimSpace(string(out)); start != "" {
		return start
	}
	t.Fatalf("ps tells no start time for pid %d", pid)
	return ""
}

// raceDeadPID is a pid with no process behind it: a child already reaped.
func raceDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// The --race-queue view prints the holder and waiters with their beads
// and ages, oldest first, skipping dead tickets without leaving a ticket
// or lock behind. Synthetic tickets stand in for queued runs: a helper
// process stands in for the holder, the test's own pid for live waiters,
// and a reaped child for a dead waiter. Its own home stands in for the
// yard's, so no real gate waits on it.
func TestYardTestRaceQueueView(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	driver, home := raceLockDriver(t)
	yard := filepath.Join(home, ".yardr")
	qdir := filepath.Join(yard, "race.queue")
	if err := os.MkdirAll(qdir, 0o700); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	now := time.Now().Unix()
	holderProc := exec.Command("sleep", "30")
	if err := holderProc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = holderProc.Process.Kill()
		_ = holderProc.Wait()
	})
	holderPID := holderProc.Process.Pid
	holderStart := raceOwnStart(t, holderPID)
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ticket := func(when int64, pid int, start, bead, dir string) {
		t.Helper()
		write(qdir, strconv.FormatInt(when, 10)+"000000000-"+strconv.Itoa(pid),
			"pid="+strconv.Itoa(pid)+"\nstart="+start+"\nbead="+bead+"\ndir="+dir+"\ncreated="+strconv.FormatInt(when, 10)+"\n")
	}
	live := raceOwnStart(t, self)
	ticket(now-400, raceDeadPID(t), "bogus", "gone-bead", "/wt/gone")
	ticket(now-350, holderPID, holderStart, "holder-bead", "/wt/holder")
	ticket(now-300, self, live, "old-bead", "/wt/old")
	ticket(now-30, self, live, "new-bead", "/wt/new")
	write(yard, "race.holder",
		"pid="+strconv.Itoa(holderPID)+"\nstart="+holderStart+"\nbead=holder-bead\ndir=/wt/holder\nsince="+strconv.FormatInt(now-360, 10)+"\n")
	view := func() string {
		t.Helper()
		cmd := exec.Command(driver, "--show-queue")
		cmd.Env = append(scriptEnv(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("--show-queue: %v\n%s", err, out)
		}
		return string(out)
	}
	// The lock held for real: holder and waiters, the dead skipped.
	lock, err := os.OpenFile(filepath.Join(yard, "race.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	out := view()
	for _, want := range []string{
		`(?m)^test: race lock: held by holder-bead for 6 m \(/wt/holder\)$`,
		`(?m)^test: race lock: 1st in line: old-bead waiting 5 m \(/wt/old\)$`,
		`(?m)^test: race lock: 2nd in line: new-bead waiting 3\d s \(/wt/new\)$`,
	} {
		if ok, _ := regexp.MatchString(want, out); !ok {
			t.Errorf("the view prints no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "gone-bead") {
		t.Errorf("the view lists the dead ticket:\n%s", out)
	}
	if strings.Contains(out, "holder-bead waiting") {
		t.Errorf("the view lists the holder as a waiter:\n%s", out)
	}
	// The lock free and no holder recorded: free, the waiters still listed.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if err := os.Remove(filepath.Join(yard, "race.holder")); err != nil {
		t.Fatal(err)
	}
	out = view()
	if !strings.Contains(out, "test: race lock: free\n") {
		t.Errorf("the free lock views as:\n%s", out)
	}
	if !strings.Contains(out, "old-bead") || !strings.Contains(out, "new-bead") {
		t.Errorf("the view lost the waiters:\n%s", out)
	}
	// Nothing anywhere: free alone, and the view took nothing.
	if err := os.Remove(filepath.Join(yard, "race.lock")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(qdir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(qdir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if out := view(); out != "test: race lock: free\n" {
		t.Errorf("the empty view prints %q", out)
	}
	if entries, _ := os.ReadDir(qdir); len(entries) != 0 {
		t.Errorf("the view left %d queue entries", len(entries))
	}
	if _, err := os.Stat(filepath.Join(yard, "race.holder")); !os.IsNotExist(err) {
		t.Errorf("the view left a holder file")
	}
	if _, err := os.Stat(filepath.Join(yard, "race.lock")); !os.IsNotExist(err) {
		t.Errorf("the view left a lock file")
	}
	// The script's own flag reaches the view without a base and prints
	// no test line, leaving the home it looked at untouched.
	empty := t.TempDir()
	cmd := exec.Command("sh", yardTest, "--race-queue")
	cmd.Env = append(scriptEnv(), "HOME="+empty)
	outb, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh .yardr/test --race-queue: %v\n%s", err, outb)
	}
	if string(outb) != "test: race lock: free\n" {
		t.Errorf("sh .yardr/test --race-queue prints %q", outb)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("--race-queue made %d entries in the home", len(entries))
	}
}

// yardTestProbe pulls the budget checks out of .yardr/test: the two
// allow-lists, the two functions that judge them, and what they call (took,
// runlogged, the load samples). The probe runs them as the script has them,
// so a case over it fails when the gate would. It also returns the
// allow-listed directories and how many distinct sleep sites the table
// holds. A script the cuts no longer follow fails the test, telling its
// editor to move the probe with it.
func yardTestProbe(t *testing.T) (probe string, overDirs []string, sleepSites int) {
	t.Helper()
	b, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	cut := func(start, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(s, start)
		if !ok {
			t.Fatalf("%s has no %q: move the probe with the script", yardTest, start)
		}
		mid, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("%s has no %q after %q: move the probe with the script", yardTest, end, start)
		}
		return start + mid + end
	}
	took := cut("\ntook() {", "}\n")
	runlogfn := cut("\nrunlogged() {", "}\n")
	over := cut("\nover60='\n", "\n'\n")
	loadfn := cut("\nloadedmachine() {\n", "\n}\n")
	eitherfn := cut("\nloadedeither() {\n", "\n}\n")
	pkgfn := cut("\npackagebudget() {\n", "\n}\n")
	long := cut("\nlongsleeps='\n", "\n'\n")
	sleepfn := cut("\nsleepbudget() {\n", "\n}\n")
	if !strings.Contains(pkgfn, "60 s budget") || !strings.Contains(sleepfn, "500 ms budget") || !strings.Contains(loadfn, "load $avg on $cores cores") || !strings.Contains(eitherfn, `echo "$1"`) {
		t.Fatal("the probe missed the budget functions: move it with .yardr/test")
	}
	for _, line := range strings.Split(over, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] == "over60='" || line == "'" {
			continue
		}
		if len(f) < 3 {
			t.Fatalf("over60 row %q: want a directory, a bead and why", line)
		}
		overDirs = append(overDirs, f[0])
	}
	keys := map[[2]string]int{}
	for _, line := range strings.Split(long, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] == "longsleeps='" || line == "'" {
			continue
		}
		if len(f) < 3 {
			t.Fatalf("longsleeps row %q: want a file, milliseconds and why", line)
		}
		if _, err := strconv.Atoi(f[1]); err != nil {
			t.Fatalf("longsleeps row %q: the milliseconds are a number", line)
		}
		keys[[2]string{f[0], f[1]}]++
	}
	if len(overDirs) == 0 || len(keys) == 0 {
		t.Fatal("the probe found no allow-list rows: move it with .yardr/test")
	}
	sites := 0
	for _, n := range keys {
		sites += n
	}
	return took + runlogfn + over + loadfn + eitherfn + pkgfn + long + sleepfn + "\n\"$@\"\n", overDirs, sites
}

// The race lock is released when the race-leg command and its R1 checks end,
// even if a test leaves a background process holding stdout open. The worker
// logs each command to a regular file so an inherited pipe cannot keep tee
// and the lock holder alive after go test exits.
func TestYardTestRaceLegLockDiesWithOrphan(t *testing.T) {
	t.Parallel()
	testYardTestRaceLegLockDiesWithOrphan(t, false)
}

// The deadline case holds the fake test at a marker, then exercises the same
// timeout branch as the orphan test without waiting for the full three minutes.
func TestYardTestRaceLegLockDiesWithOrphanDeadline(t *testing.T) {
	t.Parallel()
	testYardTestRaceLegLockDiesWithOrphan(t, true)
}

func testYardTestRaceLegLockDiesWithOrphan(t *testing.T, forceDeadline bool) {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	home, fake, dir, tmp := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for name, text := range map[string]string{
		"go": `#!/bin/sh
if [ "$1" = test ]; then
  if [ "$FORCE_ORPHAN_DEADLINE" = true ]; then
    : >"$FAKE/fixture-ready"
    while [ ! -f "$FAKE/allow-barrier" ]; do sleep 0.1; done
    : >"$FAKE/barrier-reached"
    while [ ! -f "$FAKE/release" ]; do sleep 0.1; done
  fi
  sleep 300 &
  echo $! >"$FAKE/sleeper"
  echo 'ok  yardr.dev/yardr/internal/dispatch 12.3s'
  : >"$FAKE/test-done"
fi
`,
		"gofmt": "#!/bin/sh\n",
	} {
		if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".yardr", "test"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m.test/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	base := gitT(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m.test/m\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", ".yardr/test")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(scriptEnv(),
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"YARDR_BASE="+base, "HOME="+home, "TMPDIR="+tmp, "FAKE="+fake,
		"FORCE_ORPHAN_DEADLINE="+strconv.FormatBool(forceDeadline))
	outFile, err := os.CreateTemp(t.TempDir(), "yardtest-orphan")
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	cmd.Stdout, cmd.Stderr = outFile, outFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	proc := cmd.Process
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	pidPath := filepath.Join(fake, "sleeper")
	runFinished := false
	t.Cleanup(func() {
		if !runFinished {
			killProcessTree(proc.Pid)
		}
		if b, err := os.ReadFile(pidPath); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	waitDone := func() error {
		err := <-done
		runFinished = true
		return err
	}
	if forceDeadline {
		readyDeadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(fake, "fixture-ready")); err == nil {
				break
			}
			if time.Now().After(readyDeadline) {
				killProcessTree(proc.Pid)
				waitDone()
				t.Fatalf("the race leg never reached the fixture barrier")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := os.WriteFile(filepath.Join(fake, "allow-barrier"), nil, 0o600); err != nil {
			killProcessTree(proc.Pid)
			waitDone()
			t.Fatal(err)
		}
		barrierDeadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(fake, "barrier-reached")); err == nil {
				break
			}
			if time.Now().After(barrierDeadline) {
				killProcessTree(proc.Pid)
				waitDone()
				t.Fatalf("the fixture never reached its blocking barrier")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// The bound is a fuse against a hang, not a performance assertion: the
	// loop already polls the effect, so the deadline must hold still while a
	// loaded machine runs the fixture script (docs/testing.md R2).
	deadline := 3 * time.Minute
	if forceDeadline {
		deadline = 2 * time.Second
	}
	deadlineAt := time.Now().Add(deadline)
	for {
		if _, err := os.Stat(filepath.Join(fake, "test-done")); err == nil {
			if forceDeadline {
				killProcessTree(proc.Pid)
				waitDone()
				t.Fatalf("test-done appeared before the forced deadline")
			}
			break
		}
		if time.Now().After(deadlineAt) {
			killProcessTree(proc.Pid)
			err := waitDone()
			if forceDeadline {
				if err == nil {
					t.Fatal("the forced deadline run exited without being killed")
				}
				if _, err := os.Stat(filepath.Join(fake, "test-done")); err == nil {
					t.Fatal("test-done appeared after the forced deadline")
				}
				return
			}
			t.Fatalf("the race leg never reached go test: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	sleeper, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("the fake test did not leave a sleeper pid: %v", err)
	}
	sleeperPID, err := strconv.Atoi(strings.TrimSpace(string(sleeper)))
	if err != nil {
		t.Fatalf("the fake test left invalid sleeper pid %q: %v", sleeper, err)
	}
	if err := syscall.Kill(sleeperPID, 0); err != nil {
		t.Fatalf("the fake test's stdout-holding sleeper (pid %d) is already gone: %v", sleeperPID, err)
	}
	lock, err := os.OpenFile(filepath.Join(home, ".yardr", "race.lock"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// A fuse, not a measurement: after go test exits the worker still has
	// to finish and release the lock, and a loaded machine can hold it
	// past seconds (docs/testing.md R2).
	lockDeadline := time.Now().Add(time.Minute)
	lockAcquired := false
	for time.Now().Before(lockDeadline) {
		if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			lockAcquired = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !lockAcquired {
		if b, readErr := os.ReadFile(pidPath); readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(b))); parseErr == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		select {
		case <-done:
			runFinished = true
		case <-time.After(time.Minute):
			killProcessTree(proc.Pid)
		}
		outFile.Seek(0, 0)
		b, _ := io.ReadAll(outFile)
		t.Fatalf("the race lock stayed held after go test exited; output:\n%s", b)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		runFinished = true
		if err != nil {
			outFile.Seek(0, 0)
			b, _ := io.ReadAll(outFile)
			t.Fatalf("run failed: %v\n%s", err, b)
		}
	case <-time.After(time.Minute):
		killProcessTree(proc.Pid)
		t.Fatal("race-leg worker did not finish after releasing the lock")
	}
}

// killProcessTree SIGKILLs root and every process descended from it. The
// gate's perl starts its worker in a new process group (setpgid), so
// killing root's group alone leaves the worker, a fake go waiting on its
// barrier, and the sleeper behind. ps names every pid with its parent and
// group; the groups named are the tree's own, so killing them reaches
// nothing else. A process born between the listing and the kill is the
// only miss, and a barrier sleep born then exits on its own within a
// tenth of a second.
func killProcessTree(root int) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=").Output()
	if err != nil {
		_ = syscall.Kill(-root, syscall.SIGKILL)
		_ = syscall.Kill(root, syscall.SIGKILL)
		return
	}
	children := map[int][]int{}
	pgidOf := map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		pgid, err3 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		pgidOf[pid] = pgid
	}
	seen := map[int]bool{root: true}
	stack := []int{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range children[n] {
			if !seen[c] {
				seen[c] = true
				stack = append(stack, c)
			}
		}
	}
	groups := map[int]bool{}
	for pid := range seen {
		if g, ok := pgidOf[pid]; ok && g > 0 {
			groups[g] = true
		}
	}
	for g := range groups {
		_ = syscall.Kill(-g, syscall.SIGKILL)
	}
	for pid := range seen {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// sleepSite is one sleep line for a fixture, built from parts so this file
// holds no over-budget sleep of its own for the gate to judge.
func sleepSite(dur string) string {
	return "time.Sleep" + "(" + dur + ")"
}

// runProbe runs the budget probe in dir with the run's environment filtered
// before the case's variables are added; it returns the exit code and output.
func runProbe(t *testing.T, probePath, dir string, inheritedEnv, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{probePath}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(scriptEnvFrom(inheritedEnv), env...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

// r1go is a go for the package budget's re-run: it reports the alone
// seconds FAKE_OVER names over budget, every other package fast, and
// fails every re-run when FAKE_FAIL is set. It writes down every call
// with YARDR_RACE_HELD.
func r1go(t *testing.T) (fake string, env []string) {
	t.Helper()
	fake = t.TempDir()
	goFake := `#!/bin/sh
echo "$* held=${YARDR_RACE_HELD-unset}" >>"$FAKE/calls"
for last in "$@"; do pkg=$last; done
if [ -n "$FAKE_FAIL" ]; then
  echo "--- FAIL: TestX (0.00s)"
  echo "FAIL"
  printf 'FAIL\t%s\t0.123s\n' "$pkg"
  exit 1
fi
case " $FAKE_OVER " in
 *" $pkg "*) secs=1m6.6s ;;
 *) secs=12.3s ;;
esac
printf 'ok  \t%s\t%s\n' "$pkg" "$secs"
`
	if err := os.WriteFile(filepath.Join(fake, "go"), []byte(goFake), 0o755); err != nil {
		t.Fatal(err)
	}
	env = []string{
		"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE=" + fake, "YARDR_RACE_HELD=1",
	}
	return fake, env
}

// The budgets .yardr/test enforces (docs/testing.md R1 and R2), through the
// script's own functions: an over-budget package or sleep fails unless the
// allow-list names it, and the allow-list names nothing the tree no longer
// holds. The cases run the probe above, so they fail when the gate would:
// macOS awk takes no newline in -v, and an allow-list passed that way fails
// every run on this machine (yardr-v7v8).
func TestYardTestBudgets(t *testing.T) {
	t.Parallel()
	probe, overDirs, sleepSites := yardTestProbe(t)
	probePath := filepath.Join(t.TempDir(), "probe.sh")
	if err := os.WriteFile(probePath, []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}
	runEnv := func(dir string, env []string, args ...string) (string, int) {
		t.Helper()
		return runProbe(t, probePath, dir, os.Environ(), env, args...)
	}
	run := func(dir string, args ...string) (string, int) {
		t.Helper()
		return runEnv(dir, nil, args...)
	}
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a compliant tree passes the sleep budget", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "ok_test.go", "package p\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestOk(t *testing.T) {\n\t"+
			sleepSite("500 * time.Millisecond")+"\n\t"+sleepSite("10 * time.Millisecond")+"\n\tdelay := 300 * time.Millisecond\n\t"+sleepSite("delay")+"\n}\n")
		// A sleep in a line comment is not code, and a sleep outside a
		// test file is not judged.
		write(dir, "comment_test.go", "package p\n\n// "+sleepSite("30 * time.Second")+" in a comment is not a sleep.\n")
		write(dir, "other.go", "package p\n\nimport \"time\"\n\nfunc wait() { "+sleepSite("30 * time.Second")+" }\n")
		out, code := run(dir, "sleepbudget")
		if code != 0 || strings.Contains(out, "over the 500 ms budget") {
			t.Errorf("exit %d, output %q, want exit 0 and no budget line", code, out)
		}
	})

	t.Run("an over-budget site fails the sleep budget", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "bad_test.go", "package p\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestBad(t *testing.T) {\n\t"+
			sleepSite("2 * time.Second")+"\n\t"+sleepSite("600 * time.Millisecond")+"\n\t"+sleepSite("time.Second")+"\n\t"+sleepSite("500.4 * time.Millisecond")+"\n}\n")
		out, code := run(dir, "sleepbudget")
		if code != 1 {
			t.Fatalf("exit %d, output %q, want exit 1", code, out)
		}
		for _, want := range []string{
			"bad_test.go:9 sleeps 2 * time.Second, over the 500 ms budget (docs/testing.md R2)",
			"bad_test.go:10 sleeps 600 * time.Millisecond, over the 500 ms budget (docs/testing.md R2)",
			"bad_test.go:11 sleeps time.Second, over the 500 ms budget (docs/testing.md R2)",
			"bad_test.go:12 sleeps 500.4 * time.Millisecond, over the 500 ms budget (docs/testing.md R2)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output %q, want it to contain %q", out, want)
			}
		}
	})

	t.Run("a duplicate allowed duration does not cover another site", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "yardtest_test.go", "package p\n\nimport \"time\"\n\nfunc f() {\n\t"+
			sleepSite("3 * time.Second")+"\n\t"+sleepSite("3 * time.Second")+"\n}\n")
		out, code := run(dir, "sleepbudget")
		if code != 1 || !strings.Contains(out, "yardtest_test.go:7 sleeps 3 * time.Second, over the 500 ms budget") {
			t.Errorf("exit %d, output %q, want the second 3 s sleep to fail", code, out)
		}
	})

	t.Run("the tree's allow-listed sleeps are counted", func(t *testing.T) {
		// On the checkout itself: green, with every row's site still in
		// the tree. A row whose site is gone, or a site no row names,
		// fails here as it fails the gate.
		out, code := run(".", "sleepbudget")
		if code != 0 {
			t.Fatalf("sleepbudget on the tree: exit %d\n%s", code, out)
		}
		if want := "test: " + strconv.Itoa(sleepSites) + " sleeps over 500 ms allow-listed"; !strings.Contains(out, want) {
			t.Errorf("output %q, want it to contain %q", out, want)
		}
	})

	t.Run("the package budget judges race legs by re-measuring alone", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "red.txt",
			"ok  \tyardr.dev/yardr/internal/cli\t1m58.3s\n"+
				"ok  \tyardr.dev/yardr\t1m5.5s\n"+
				"ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n"+
				"ok  \tyardr.dev/yardr/internal/pack\t1m1.5s\n"+
				"ok  \tyardr.dev/yardr/internal/store\t1m0.1s\n"+
				"ok  \tyardr.dev/yardr/internal/web\t1m40.0s\n"+
				"ok  \tyardr.dev/yardr/contrib/isolated-yard\t2m30.0s\n"+
				"ok  \tyardr.dev/yardr/internal/rpc\t60s\n"+
				"ok  \tyardr.dev/yardr/internal/bdp\t(cached)\n"+
				"?   \tyardr.dev/yardr/internal/harnesses\t[no test files]\n"+
				"FAIL\tyardr.dev/yardr/internal/broken\n"+
				"--- FAIL: TestBroken (0.01s)\n"+
				"ok  \texample.com/other\t99.9s\n")
		// isolated-yard is allow-listed, exactly 60 s complies, and
		// cached, failed and foreign lines are not judged: only root,
		// cli, dispatch, pack, store and web are re-run, sorted, one
		// package after another, with the leg's flags and -count=1,
		// inside its hold. The module path is the root, whose
		// allow-list row yardr-1x36 deleted, as yardr-3cc4 deleted
		// store's, yardr-ab6t cli's and yardr-3gdg web's. The load is
		// pinned quiet, so the root's over-alone verdict fails however
		// loaded the test machine is.
		fake, env := r1go(t)
		out, code := runEnv(dir, append(env,
			"FAKE_OVER=yardr.dev/yardr",
			"YARDR_TEST_LOADAVG=1.5", "YARDR_TEST_CORES=10"),
			"packagebudget", "red.txt", "go", "test", "-race", "./...")
		if code != 1 {
			t.Fatalf("exit %d, output %q, want exit 1", code, out)
		}
		// The root is over alone too: the run fails, naming both times.
		// Cli, dispatch, pack, store and web are under alone: each
		// passes with the R1 line. Store's allow-list row yardr-3cc4
		// deleted, yardr-ab6t cli's and yardr-3gdg web's, so over
		// budget they are re-run with the rest; isolated-yard's row
		// stands, so it is not re-run.
		root := "test: yardr.dev/yardr took 65.5 s with -race, 66.6 s alone, over the 60 s budget (docs/testing.md R1)"
		cli := "test: R1: yardr.dev/yardr/internal/cli took 118.3 s in the run, 12.3 s alone: under budget, loaded run"
		dispatch := "test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 12.3 s alone: under budget, loaded run"
		pack := "test: R1: yardr.dev/yardr/internal/pack took 61.5 s in the run, 12.3 s alone: under budget, loaded run"
		store := "test: R1: yardr.dev/yardr/internal/store took 60.1 s in the run, 12.3 s alone: under budget, loaded run"
		web := "test: R1: yardr.dev/yardr/internal/web took 100 s in the run, 12.3 s alone: under budget, loaded run"
		if !strings.Contains(out, root+"\n") || !strings.Contains(out, cli+"\n") || !strings.Contains(out, dispatch+"\n") || !strings.Contains(out, pack+"\n") || !strings.Contains(out, store+"\n") || !strings.Contains(out, web+"\n") {
			t.Fatalf("output %q, want it to name root, cli, dispatch, pack, store and web", out)
		}
		if got := strings.Count(out, "over the 60 s budget"); got != 1 {
			t.Errorf("output %q, want 1 budget failure, got %d", out, got)
		}
		calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
		want := "test -race -count=1 yardr.dev/yardr held=1\n" +
			"test -race -count=1 yardr.dev/yardr/internal/cli held=1\n" +
			"test -race -count=1 yardr.dev/yardr/internal/dispatch held=1\n" +
			"test -race -count=1 yardr.dev/yardr/internal/pack held=1\n" +
			"test -race -count=1 yardr.dev/yardr/internal/store held=1\n" +
			"test -race -count=1 yardr.dev/yardr/internal/web held=1"
		if got := strings.TrimSpace(string(calls)); got != want {
			t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("a package under budget alone passes", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "loaded.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
		fake, env := r1go(t)
		out, code := runEnv(dir, append(env, "FAKE_OVER="),
			"packagebudget", "loaded.txt", "go", "test", "-race", "./...")
		if code != 0 {
			t.Fatalf("exit %d, output %q, want exit 0", code, out)
		}
		want := "test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 12.3 s alone: under budget, loaded run"
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output %q, want it to hold %q", out, want)
		}
		if strings.Contains(out, "over the 60 s budget") {
			t.Errorf("output %q, want no budget failure", out)
		}
		calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
		if got, want := strings.TrimSpace(string(calls)), "test -race -count=1 yardr.dev/yardr/internal/dispatch held=1"; got != want {
			t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("a package over budget alone on a loaded machine passes", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "heavy.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
		fake, env := r1go(t)
		out, code := runEnv(dir, append(env,
			"FAKE_OVER=yardr.dev/yardr/internal/dispatch",
			"YARDR_TEST_LOADAVG=72.4", "YARDR_TEST_CORES=10"),
			"packagebudget", "heavy.txt", "go", "test", "-race", "./...")
		if code != 0 {
			t.Fatalf("exit %d, output %q, want exit 0", code, out)
		}
		want := "test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 66.6 s alone: over budget, loaded machine (load 72.4 on 10 cores), not failed"
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output %q, want it to hold %q", out, want)
		}
		if strings.Contains(out, "over the 60 s budget") {
			t.Errorf("output %q, want no budget failure", out)
		}
		calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
		if got, want := strings.TrimSpace(string(calls)), "test -race -count=1 yardr.dev/yardr/internal/dispatch held=1"; got != want {
			t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("quiet run marks do not reach the budget probe", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "leak.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
		inheritedEnv := append(os.Environ(), "YARDR_R1_STRICT=1", "YARDR_QUIET_RUN=1")
		for _, kv := range scriptEnvFrom(inheritedEnv) {
			k, _, _ := strings.Cut(kv, "=")
			if k == "YARDR_R1_STRICT" || k == "YARDR_QUIET_RUN" {
				t.Fatalf("scriptEnv carries %q from the run", kv)
			}
		}
		_, env := r1go(t)
		out, code := runProbe(t, probePath, dir, inheritedEnv, append(env,
			"FAKE_OVER=yardr.dev/yardr/internal/dispatch",
			"YARDR_TEST_LOADAVG=72.4", "YARDR_TEST_CORES=10"),
			"packagebudget", "leak.txt", "go", "test", "-race", "./...")
		if code != 0 {
			t.Fatalf("exit %d, output %q, want exit 0", code, out)
		}
		want := "test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 66.6 s alone: over budget, loaded machine (load 72.4 on 10 cores), not failed"
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output %q, want it to hold %q", out, want)
		}
	})

	t.Run("the quiet run's strict setting fails over budget alone on a loaded machine", func(t *testing.T) {
		// The same loaded re-measurement, judged twice: without the
		// setting it loud-passes as at a landing, with YARDR_R1_STRICT,
		// which the quiet run alone sets, it fails, naming both times
		// and the load beside them.
		dir := t.TempDir()
		write(dir, "strict.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
		loaded := []string{
			"FAKE_OVER=yardr.dev/yardr/internal/dispatch",
			"YARDR_TEST_LOADAVG=72.4", "YARDR_TEST_CORES=10",
		}
		_, env := r1go(t)
		out, code := runEnv(dir, append(env, loaded...),
			"packagebudget", "strict.txt", "go", "test", "-race", "./...")
		if code != 0 {
			t.Fatalf("without the setting: exit %d, output %q, want exit 0", code, out)
		}
		loud := "test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 66.6 s alone: over budget, loaded machine (load 72.4 on 10 cores), not failed"
		if !strings.Contains(out, loud+"\n") {
			t.Errorf("without the setting: output %q, want it to hold %q", out, loud)
		}
		_, env = r1go(t)
		out, code = runEnv(dir, append(env, append(loaded, "YARDR_R1_STRICT=1")...),
			"packagebudget", "strict.txt", "go", "test", "-race", "./...")
		if code != 1 {
			t.Fatalf("with the setting: exit %d, output %q, want exit 1", code, out)
		}
		strict := "test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, 66.6 s alone, over the 60 s budget (docs/testing.md R1), loaded machine (load 72.4 on 10 cores)"
		if !strings.Contains(out, strict+"\n") {
			t.Errorf("with the setting: output %q, want it to hold %q", out, strict)
		}
		if strings.Contains(out, "not failed") {
			t.Errorf("with the setting: output %q, want no loaded pass", out)
		}
	})

	t.Run("the load is sampled as the re-run starts and as it ends", func(t *testing.T) {
		// The one-minute average lags, so a re-run that sat in a load was
		// judged quiet once the load dipped before the check (yardr-f5f7).
		// The machine is loaded when either end of the re-run was, and the
		// line names the start's load where the start was loaded. The probe
		// here carries a loadedmachine that answers with the load the case
		// sets for the side of the re-run it is asked on: the start's while
		// the fake go has written no call, the end's after. A sample taken
		// on the wrong side reads the wrong load, and the verdict shows it.
		sampling, ok := strings.CutSuffix(probe, "\n\"$@\"\n")
		if !ok {
			t.Fatal("the probe no longer ends by running its arguments: move this case with it")
		}
		sampling += `
loadedmachine() {
  if [ -s "$FAKE/calls" ]; then sample=$FAKE_LOAD_END; else sample=$FAKE_LOAD_START; fi
  [ -n "$sample" ] || return 1
  echo "$sample"
}
"$@"
`
		samplingPath := filepath.Join(t.TempDir(), "sampling.sh")
		if err := os.WriteFile(samplingPath, []byte(sampling), 0o644); err != nil {
			t.Fatal(err)
		}
		const (
			quiet = ""
			early = "load 26.4 on 10 cores"
			late  = "load 72.4 on 10 cores"
		)
		const overQuiet = "test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, 66.6 s alone, over the 60 s budget (docs/testing.md R1)"
		overLoaded := func(load string) string { return overQuiet + ", loaded machine (" + load + ")" }
		loadedPass := func(load string) string {
			return "test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 66.6 s alone: over budget, loaded machine (" + load + "), not failed"
		}
		for _, c := range []struct {
			name       string
			start, end string // the sample as the re-run starts and as it ends
			strict     bool   // YARDR_R1_STRICT, which the quiet run sets
			code       int
			want       string // the verdict line
		}{
			{"loaded at the start, quiet at the end", early, quiet, false, 0, loadedPass(early)},
			{"quiet at the start, loaded at the end", quiet, late, false, 0, loadedPass(late)},
			{"loaded at both names the start's", early, late, false, 0, loadedPass(early)},
			{"quiet at both fails naming both times", quiet, quiet, false, 1, overQuiet},
			{"strict, loaded at the start only", early, quiet, true, 1, overLoaded(early)},
			{"strict, loaded at the end only", quiet, late, true, 1, overLoaded(late)},
			{"strict, loaded at both names the start's", early, late, true, 1, overLoaded(early)},
			{"strict, quiet at both", quiet, quiet, true, 1, overQuiet},
		} {
			t.Run(c.name, func(t *testing.T) {
				dir := t.TempDir()
				write(dir, "sampled.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
				fake, env := r1go(t)
				env = append(env, "FAKE_OVER=yardr.dev/yardr/internal/dispatch",
					"FAKE_LOAD_START="+c.start, "FAKE_LOAD_END="+c.end)
				if c.strict {
					env = append(env, "YARDR_R1_STRICT=1")
				}
				out, code := runProbe(t, samplingPath, dir, os.Environ(), env,
					"packagebudget", "sampled.txt", "go", "test", "-race", "./...")
				if code != c.code {
					t.Errorf("exit %d, output %q, want exit %d", code, out, c.code)
				}
				if !strings.Contains(out, c.want+"\n") {
					t.Errorf("output %q, want it to hold %q", out, c.want)
				}
				// One alone pass, however the machine was: the re-run is not
				// run again to wait out a load.
				calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
				if got, want := strings.TrimSpace(string(calls)), "test -race -count=1 yardr.dev/yardr/internal/dispatch held=1"; got != want {
					t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	})

	t.Run("unknown load and a load at the core count fail the run", func(t *testing.T) {
		for _, c := range []struct {
			name string
			avg  string
		}{
			{"unreadable", "bogus"},
			{"at the count", "10"},
		} {
			t.Run(c.name, func(t *testing.T) {
				dir := t.TempDir()
				write(dir, "stray.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
				_, env := r1go(t)
				out, code := runEnv(dir, append(env,
					"FAKE_OVER=yardr.dev/yardr/internal/dispatch",
					"YARDR_TEST_LOADAVG="+c.avg, "YARDR_TEST_CORES=10"),
					"packagebudget", "stray.txt", "go", "test", "-race", "./...")
				if code != 1 {
					t.Fatalf("exit %d, output %q, want exit 1", code, out)
				}
				want := "test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, 66.6 s alone, over the 60 s budget (docs/testing.md R1)"
				if !strings.Contains(out, want+"\n") {
					t.Errorf("output %q, want it to hold %q", out, want)
				}
			})
		}
	})

	t.Run("a re-run that fails fails the run", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "flaky.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t61.2s\n")
		_, env := r1go(t)
		out, code := runEnv(dir, append(env, "FAKE_FAIL=1"),
			"packagebudget", "flaky.txt", "go", "test", "-race", "./...")
		if code != 1 {
			t.Fatalf("exit %d, output %q, want exit 1", code, out)
		}
		want := "test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, over the 60 s budget (docs/testing.md R1), and its re-run alone failed"
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output %q, want it to hold %q", out, want)
		}
	})

	t.Run("a leg with nothing over budget runs nothing again", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "green.txt",
			"ok  \tyardr.dev/yardr/internal/cli\t51.2s\n"+
				"ok  \tyardr.dev/yardr/internal/web\t32.4s\n"+
				"ok  \tyardr.dev/yardr/contrib/isolated-yard\t118.3s\n"+
				"ok  \tyardr.dev/yardr/internal/store\t55.0s\n"+
				"ok  \tyardr.dev/yardr\t31.2s\n"+
				"ok  \tyardr.dev/yardr/internal/pack\t21.0s\n")
		fake, env := r1go(t)
		if out, code := runEnv(dir, env, "packagebudget", "green.txt", "go", "test", "-race", "./..."); code != 0 || out != "" {
			t.Errorf("exit %d, output %q, want exit 0 and no output", code, out)
		}
		if calls, _ := os.ReadFile(filepath.Join(fake, "calls")); len(calls) != 0 {
			t.Errorf("go was run as:\n%s\nwant no re-run", calls)
		}
	})

	t.Run("allow-listed packages are there", func(t *testing.T) {
		for _, dir := range overDirs {
			info, err := os.Stat(dir)
			if err != nil {
				t.Errorf("allow-listed %s: %v", dir, err)
			} else if !info.IsDir() {
				t.Errorf("allow-listed %s: not a directory", dir)
			}
		}
	})
}

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

// What the first line of a run without YARDR_LANDING ends with when it has
// something to test: such a run is never raced, the detector being the
// landing's (docs/testing.md R4).
const plainWhy = ": testing without -race (race runs at landing only, docs/testing.md R4)"

// noYardHome is the YARDR_HOME a case's script gets: a fixed path under the
// temp dir that no test creates, so a real yardr reached anyway reads no
// yard and fails, and the script's filing only warns.
var noYardHome = filepath.Join(os.TempDir(), "yardr-test-no-yard")

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

// copyFixture is one row's copy of a template fixture the parent built: the
// rows of a matrix share the commit and the files, and each runs the script
// against its own copy, so they run in parallel without each building (and
// committing) the same repository. The root package has little room under
// R1's 60 s (yardr-2t2m, yardr-qach).
func copyFixture(t *testing.T, template string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "row")
	cmd := exec.Command("cp", "-R", template, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cp -R %s %s: %v\n%s", template, dir, err, out)
	}
	return dir
}

// goModTemplate is the repository the whole-suite rows run the script
// against: a module with nothing in it, committed, and the base commit and
// short sha every row's copy shares. A row copies it, changes go.mod, and
// runs.
func goModTemplate(t *testing.T) (template, base, short string) {
	t.Helper()
	template = t.TempDir()
	if err := os.WriteFile(filepath.Join(template, "go.mod"), []byte("module m.test/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, template, "init", "-q", "-b", "main")
	gitT(t, template, "add", "-A")
	gitT(t, template, "commit", "-q", "-m", "base")
	base = gitT(t, template, "rev-parse", "HEAD")
	short = gitT(t, template, "rev-parse", "--short", "HEAD")
	return template, base, short
}

// scriptEnv is this test's environment without what the run it is in gave
// the script it is part of: the base and the landing of an agent's session
// or the assembly, the socket client and the GOOS of a landing's steps, the
// GOFLAGS the run's own script set for its go (a case that checks the flags
// the script sets would find them there already), a plan, the race lock the
// run's own race leg holds, the load factor that leg was given
// (YARDR_TEST_LOAD, which a case that checks what the script gives its go
// would find there already), the beads a train's integration says its tests are
// of (YARDR_TEST_BEADS, which a case that checks the beads a flaky log line
// carries would find there already), the quiet run's marks
// (YARDR_R1_STRICT and YARDR_QUIET_RUN), which would otherwise change
// strict-budget judging and enable quiet-run-only tests, and the run's yard
// (YARDR_HOME, YARDR_BEAD, YARDR_DEPOT, YARDR_ACTOR and YARDR_SESSION): a
// case that runs the script files a flake through a stub yardr or through
// none, never through the real CLI into the run's yard. It also reads a quiet
// machine, a load of 1.5 on 10 cores, whatever the machine and the run: a
// loaded one would give a landing's race leg a -p of its own (gatepar), and
// the cases that run a landing assert the go commands it makes. A case sets
// what it means the script to have, after these, so its own load wins: one that
// runs the script sets YARDR_RACE_HELD, so its race step shares the hold instead
// of taking the machine's lock, except the lock's own test, which takes a lock
// of its own.
func scriptEnv() []string {
	return scriptEnvFrom(os.Environ())
}

// scriptEnvFrom lets a case check the filter with a synthetic run environment.
func scriptEnvFrom(environ []string) []string {
	env := slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"YARDR_BASE", "YARDR_LANDING", "YARDR_TEST_PLAN", "YARDR_TEST_RPC", "GOOS", "GOFLAGS", "YARDR_RACE_HELD", "YARDR_R1_STRICT", "YARDR_QUIET_RUN", "YARDR_TEST_LOAD", "YARDR_TEST_BEADS", "YARDR_HOME", "YARDR_BEAD", "YARDR_DEPOT", "YARDR_ACTOR", "YARDR_SESSION"}, k)
	})
	return append(env, "YARDR_TEST_LOADAVG=1.5", "YARDR_TEST_CORES=10", "YARDR_HOME="+noYardHome)
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
// suite, as a landing of nothing is. A commit on the branch is a base like
// any other: a reviewer that committed names the commit the last test line
// does (the review stages say so), and the plan holds what its own commits
// changed, and what imports it. If that commit is ahead of HEAD, the merge
// base is HEAD and the script must fail closed instead of passing an empty
// selection.
func TestYardTestSaysItsBase(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go in PATH")
	}
	script, rows := yardTestRows(t)
	dir := t.TempDir()
	// A plain run reads the table's packages before it prints its first line,
	// as a landing does, and a row whose package is gone stops it there: the
	// module has the table's packages, and go has some to list.
	files := map[string]string{"go.mod": "module m.test/m\n\ngo 1.21\n"}
	for _, row := range rows {
		for _, pkg := range row.pkgs {
			files[pkg+"/p.go"] = "package p\n"
		}
	}
	// What a branch changes below: a package nothing imports, one that embeds
	// a file, and the one that imports it.
	files["alone/p.go"] = "package p\n"
	files["emb/p.go"] = "package emb\n\nimport _ \"embed\"\n\n//go:embed a.txt\nvar A string\n"
	files["emb/a.txt"] = "a\n"
	files["top/p.go"] = "package top\n\nimport _ \"m.test/m/emb\"\n"
	write := func(name, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range files {
		write(name, text)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	short := gitT(t, dir, "rev-parse", "--short", "HEAD")
	gitT(t, dir, "branch", "other")
	run := func(env ...string) (string, int) {
		t.Helper()
		cmd := exec.Command("sh", "-c", script)
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
	t.Run("a base ahead of HEAD", func(t *testing.T) {
		gitT(t, dir, "checkout", "-q", "other")
		write("alone/p.go", "package p\n\nvar Future int\n")
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", "a later test line")
		ahead := gitT(t, dir, "rev-parse", "HEAD")
		gitT(t, dir, "checkout", "-q", "main")

		out, code := run("YARDR_BASE=" + ahead)
		want := "test: cannot compare with " + ahead + " (YARDR_BASE): base is not an ancestor of HEAD\n"
		if code != 2 || !strings.Contains(out, want) {
			t.Errorf("a base ahead of HEAD: exit %d, output %q, want exit 2 and %q", code, out, want)
		}

		out, code = run("YARDR_BASE="+ahead, "YARDR_LANDING=1")
		first, _, _ := strings.Cut(out, "\n")
		want = "test: YARDR_LANDING is set, and " + ahead + " (YARDR_BASE) is not an ancestor of HEAD: the whole suite"
		if code != 0 || first != want {
			t.Errorf("a landing with a base ahead of HEAD: exit %d, first line %q, want exit 0 and %q", code, first, want)
		}
	})
	// The builder changed a package nothing imports, the reviewer a file that
	// another package embeds. Against the builder's commit the plan holds the
	// reviewer's change alone and the package that imports it. Once main has
	// moved and the branch is rebased, that commit is no ancestor of HEAD: the
	// script compares from where the two meet, the commit the builder started
	// from, so the plan holds the whole branch and what main gained meanwhile,
	// more than the reviewer's commits and never less.
	t.Run("a base on the branch", func(t *testing.T) {
		gitT(t, dir, "checkout", "-q", "-b", "feature")
		t.Cleanup(func() {
			gitT(t, dir, "checkout", "-q", "-f", "main")
			gitT(t, dir, "branch", "-q", "-D", "feature")
		})
		write("alone/p.go", "package p\n\nvar Built int\n")
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", "builder")
		built := gitT(t, dir, "rev-parse", "HEAD")
		write("emb/a.txt", "fixed\n")
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", "review fix")
		const tested = "test: testing, without -race, what changed, what imports it and what reads it:"
		out, code := run("YARDR_BASE=" + built)
		want := "test: what changed since " + built + " (YARDR_BASE), from " + gitT(t, dir, "rev-parse", "--short", built) + plainWhy + "\n" +
			tested + " m.test/m/emb m.test/m/top\n"
		if code != 0 || out != want {
			t.Errorf("a base on the branch: exit %d, output:\n%s\nwant exit 0 and:\n%s", code, out, want)
		}
		gitT(t, dir, "checkout", "-q", "main")
		write("docs/web.md", "moved\n")
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", "main moves")
		gitT(t, dir, "checkout", "-q", "feature")
		gitT(t, dir, "rebase", "-q", "main")
		var exit *exec.ExitError
		if err := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", built, "HEAD").Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("the commit tested before the rebase is an ancestor of HEAD after it: %v", err)
		}
		out, code = run("YARDR_BASE=" + built)
		want = "test: what changed since " + built + " (YARDR_BASE), from " + short + plainWhy + "\n" +
			"test: docs/web.md changed: read by the tests of .\n" +
			tested + " m.test/m m.test/m/alone m.test/m/emb m.test/m/top\n"
		if code != 0 || out != want {
			t.Errorf("a base the branch was rebased away from: exit %d, output:\n%s\nwant exit 0 and:\n%s", code, out, want)
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
// changed cannot be selected. A plain run tests its selection without -race,
// whatever changed, and says so in its first line and its testing line
// (docs/testing.md R4); a landing tests it without -race only when nothing
// compiled changed (no .go file, no embedded file, no deletion), which its
// first line and testing line both say, and with it otherwise. The rows share
// one template fixture and run against a copy each, so they run in parallel
// without each committing the same repository: the root package has little
// room under R1's 60 s (yardr-2t2m, yardr-qach).
func TestYardTestSelects(t *testing.T) {
	t.Parallel()
	script, rows := yardTestRows(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go in PATH")
	}
	const pre = "test: "
	// In the row tables below, sincePH and shortPH stand for the shared
	// base: the row fills it in before it compares.
	const sincePH = "\x00since\x00"
	const shortPH = "\x00short\x00"
	writeFile := func(t *testing.T, dir, name, text string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// selectsFixture is one row's copy of the module the script runs
	// against: its own directory, and the shared base commit with the words
	// the script prints for them.
	type selectsFixture struct {
		dir   string
		base  string
		short string
		since string
	}
	// newFixture builds the module the rows run the script against, once:
	// the table's packages and nothing in them, the socket suites' shape,
	// and the embedding and rowed-directory cases the rows change. It reads
	// the shared table rows, which no row writes. Each row copies it.
	newFixture := func(t *testing.T) selectsFixture {
		t.Helper()
		dir := t.TempDir()
		write := func(name, text string) {
			t.Helper()
			writeFile(t, dir, name, text)
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
		// One that embeds a glob below its directory, as internal/web does its
		// templates, and one that imports it. A page that leaves the glob is no
		// error for go list, as emb's file would be: the package embeds one page
		// fewer, and only the deleted-file rule can tell.
		write("globemb/p.go", "package globemb\n\nimport \"embed\"\n\n//go:embed pages/*.md\nvar Pages embed.FS\n")
		write("globemb/pages/a.md", "a\n")
		write("globemb/pages/b.md", "b\n")
		write("globtop/p.go", "package globtop\n\nimport _ \"m.test/m/globemb\"\n")
		// One that embeds a glob beside its Go file, as docs does its pages, and
		// one that imports it. The rule has to tell a page that leaves from a
		// neighbour the glob never embedded (notes.txt, yardr-56xw). A name under
		// all: embeds its hidden files too, and one without does not: kept/ and
		// dropped/ each hold one.
		write("mdemb/p.go", "package mdemb\n\nimport \"embed\"\n\n//go:embed *.md all:kept dropped\nvar Pages embed.FS\n")
		write("mdemb/a.md", "a\n")
		write("mdemb/b.md", "b\n")
		write("mdemb/notes.txt", "notes\n")
		write("mdemb/kept/x.txt", "x\n")
		write("mdemb/kept/.hidden", "hidden\n")
		write("mdemb/dropped/y.txt", "y\n")
		write("mdemb/dropped/.hidden", "hidden\n")
		write("mdtop/p.go", "package mdtop\n\nimport _ \"m.test/m/mdemb\"\n")
		// One whose pattern the rule does not read: ? is a glob it leaves alone.
		write("qemb/p.go", "package qemb\n\nimport \"embed\"\n\n//go:embed v?.txt\nvar V embed.FS\n")
		write("qemb/v1.txt", "v\n")
		write("qemb/other.txt", "other\n")
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
		return selectsFixture{dir: dir, base: base, short: short, since: " since " + base + " (YARDR_BASE), from " + short}
	}
	template := newFixture(t)
	rowFixture := func(t *testing.T) selectsFixture {
		t.Helper()
		fx := template
		fx.dir = copyFixture(t, fx.dir)
		return fx
	}
	// The script's plan for a branch that changed these files, deleted them
	// (a name after "-") or moved them (a name "from -> to", done by git mv,
	// which git reports as a rename unless asked not to): its first line, and
	// the rest.
	planned := func(t *testing.T, fx selectsFixture, script string, env []string, changed ...string) (first, rest string, err error) {
		t.Helper()
		gitT(t, fx.dir, "reset", "-q", "--hard", fx.base)
		gitT(t, fx.dir, "clean", "-qfd")
		for _, name := range changed {
			text := "changed\n"
			if strings.HasSuffix(name, ".go") {
				text = "package p\n\nvar Changed int\n"
			}
			if name == "go.mod" {
				text = "module m.test/m\n\ngo 1.21\n\n// changed\n"
			}
			if gone, ok := strings.CutPrefix(name, "-"); ok {
				if err := os.Remove(filepath.Join(fx.dir, gone)); err != nil {
					t.Fatal(err)
				}
			} else if from, to, ok := strings.Cut(name, " -> "); ok {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(fx.dir, to)), 0o755); err != nil {
					t.Fatal(err)
				}
				gitT(t, fx.dir, "mv", from, to)
			} else {
				writeFile(t, fx.dir, name, text)
			}
		}
		gitT(t, fx.dir, "add", "-A")
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = fx.dir
		cmd.Env = append(append(scriptEnv(), "YARDR_TEST_PLAN=1", "YARDR_BASE="+fx.base), env...)
		b, err := cmd.CombinedOutput()
		first, rest, _ = strings.Cut(strings.TrimSpace(string(b)), "\n")
		return first, rest, err
	}
	// What the first line of a landing that tests without -race ends with.
	// (A plain run's is plainWhy: a plain run is never raced.)
	const unraced = ": testing without -race"
	// A plain run's plan: its first line says what it was compared with and,
	// when there is something to test, that it tests without -race and why.
	plan := func(t *testing.T, fx selectsFixture, script string, nothing bool, changed ...string) (string, error) {
		t.Helper()
		first, out, err := planned(t, fx, script, nil, changed...)
		want := "test: what changed" + fx.since + plainWhy
		if nothing {
			want = "test: what changed" + fx.since
		}
		if first != want {
			t.Errorf("first line %q, want %q", first, want)
		}
		// Whatever it changed, compiled or not, a plain run is never raced:
		// the race detector is the landing's (docs/testing.md R4).
		if strings.Contains(out, "with -race") {
			t.Errorf("a plain run's plan tests with -race:\n%s", out)
		}
		return out, err
	}
	tested := pre + "testing, with -race, what changed, what imports it and what reads it:"
	testedNoRace := pre + "testing, without -race, what changed, what imports it and what reads it:"
	for _, c := range []struct {
		name    string
		changed []string
		want    []string
	}{
		{"the starter pack", []string{"packs/starter/roles/builder.md"}, []string{
			pre + "packs/starter/roles/builder.md changed: read by the tests of internal/pack internal/cli",
			testedNoRace + " m.test/m/internal/cli m.test/m/internal/pack m.test/m/packs m.test/m/packtop"}},
		{"two files of the starter pack", []string{"packs/starter/roles/builder.md", "packs/starter/pack.yaml"}, []string{
			pre + "packs/starter/pack.yaml and 1 more under packs/starter/ changed: read by the tests of internal/pack internal/cli",
			testedNoRace + " m.test/m/internal/cli m.test/m/internal/pack m.test/m/packs m.test/m/packtop"}},
		{"a deleted embedded file under a rowed directory", []string{"-packs/starter/roles/reviewer.md"}, []string{
			pre + "packs/starter/roles/reviewer.md changed: read by the tests of internal/pack internal/cli",
			testedNoRace + " m.test/m/internal/cli m.test/m/internal/pack m.test/m/packs m.test/m/packtop"}},
		{"a deleted file under no embedded directory", []string{"-docs/web.md"}, []string{
			pre + "docs/web.md changed: read by the tests of .",
			testedNoRace + " m.test/m"}},
		// git's rename detection would list the new path alone, one that
		// nothing embeds. A move is the deletion of the old path and the
		// addition of the new, so the page that left the glob selects its
		// package as a deleted page does.
		{"a file moved out of an embedded glob", []string{"globemb/pages/b.md -> docs/design/b.md"}, []string{
			pre + "docs/design/b.md changed: read by the tests of .",
			testedNoRace + " m.test/m m.test/m/globemb m.test/m/globtop"}},
		// The glob is beside the package's Go file, as docs' is: there is no
		// directory below the package's in which to look for the page that went.
		{"a deleted page of a glob beside the package's Go file", []string{"-mdemb/a.md"}, []string{
			testedNoRace + " m.test/m/mdemb m.test/m/mdtop"}},
		{"a page moved out of a glob beside the package's Go file", []string{"mdemb/b.md -> docs/design/b.md"}, []string{
			pre + "docs/design/b.md changed: read by the tests of .",
			testedNoRace + " m.test/m m.test/m/mdemb m.test/m/mdtop"}},
		// A deleted neighbour is no page, so it is no more the package's than
		// a file the package never embedded: nobody is known to read it.
		{"a deleted neighbour the glob does not match", []string{"-mdemb/notes.txt"}, []string{
			pre + "mdemb/notes.txt is read by no known package: testing everything"}},
		// kept/ is embedded with all:, so its hidden file counts as the
		// package's change; dropped/ is not, so its hidden file does not, and
		// it alone is read by no known package.
		{"deleted hidden files under all: and under a plain name", []string{"-mdemb/kept/.hidden", "-mdemb/dropped/.hidden"}, []string{
			pre + "mdemb/dropped/.hidden is read by no known package: testing everything"}},
		// A pattern the rule cannot read leaves a deleted path unaccounted for,
		// so the whole-suite fallback covers it instead of assuming it was embedded.
		{"a deleted file under a pattern the rule cannot read", []string{"-qemb/other.txt"}, []string{
			pre + "qemb/other.txt is read by no known package: testing everything"}},
		{"another package's testdata", []string{"internal/hooks/testdata/github/ping.json"}, []string{
			pre + "internal/hooks/testdata/github/ping.json changed: read by the tests of internal/hooks internal/dispatch",
			testedNoRace + " m.test/m/internal/dispatch m.test/m/internal/hooks"}},
		{"a file no row knows", []string{"contrib/x.txt", "docs/web.md"}, []string{
			pre + "contrib/x.txt is read by no known package: testing everything"}},
		{"three files no row knows", []string{"contrib/x.txt", "alone/y.txt", "z"}, []string{
			pre + "alone/y.txt (and 2 more) is read by no known package: testing everything"}},
		// The root package's test follows the README's links into docs/.
		{"docs only", []string{"docs/web.md", "docs/new.md"}, []string{
			pre + "docs/new.md and 1 more under docs/ changed: read by the tests of .",
			testedNoRace + " m.test/m"}},
		// What no test reads (the map for agents, which a design that adds a
		// package updates beside it) adds no package.
		{"docs and the map for agents", []string{"docs/design/2026-10-12-a.md", "AGENTS.md"}, []string{
			pre + "docs/design/2026-10-12-a.md changed: read by the tests of .",
			pre + "AGENTS.md changed: no test reads it",
			testedNoRace + " m.test/m"}},
		{"what no test reads", []string{".github/a.yml", ".github/b.yml"}, []string{
			pre + ".github/a.yml and 1 more under .github/ changed: no test reads it",
			pre + "no package changed since " + shortPH + "; no tests to run"}},
		// AGENTS.md is the map of the repository for agents, and CLAUDE.md
		// imports it: no test reads either. A change to them alone tests
		// nothing, and the change that adds a package, which the depot's rule
		// has update the map, tests that package and no other (not
		// everything, as a file no row knows does).
		{"the map for agents", []string{"AGENTS.md", "CLAUDE.md"}, []string{
			pre + "AGENTS.md changed: no test reads it",
			pre + "CLAUDE.md changed: no test reads it",
			pre + "no package changed since " + shortPH + "; no tests to run"}},
		{"a new package and the map that lists it", []string{"AGENTS.md", "newpkg/p.go"}, []string{
			pre + "AGENTS.md changed: no test reads it",
			testedNoRace + " m.test/m/newpkg"}},
		// A file's row is that file only: .yardr/test is not a prefix of
		// .yardr/tests.md, which no row knows.
		{"the script itself", []string{".yardr/test"}, []string{
			pre + ".yardr/test changed: read by the tests of .",
			testedNoRace + " m.test/m"}},
		{"a file its name begins", []string{".yardr/tests.md"}, []string{
			pre + ".yardr/tests.md is read by no known package: testing everything"}},
		{"an embedded file", []string{"emb/a.txt"}, []string{
			testedNoRace + " m.test/m/emb m.test/m/top"}},
		{"a go file", []string{"alone/p.go", "docs/web.md"}, []string{
			pre + "docs/web.md changed: read by the tests of .",
			testedNoRace + " m.test/m m.test/m/alone"}},
		// A test file is compiled into its package's test binary.
		{"a test file", []string{"alone/p_test.go", "docs/web.md"}, []string{
			pre + "docs/web.md changed: read by the tests of .",
			testedNoRace + " m.test/m m.test/m/alone"}},
		// go.mod moves every package: no selection, and no go list for one.
		{"go.mod", []string{"go.mod", "docs/web.md"}, []string{
			pre + "go.mod or go.sum changed; testing every package"}},
		{"nothing", nil, []string{
			pre + "no package changed since " + shortPH + "; no tests to run"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fx := rowFixture(t)
			// A plain run is never raced, and says so in its first line and
			// its testing line, unless it has nothing to test: then it makes
			// no claim about how.
			nothing := slices.ContainsFunc(c.want, func(line string) bool { return strings.HasPrefix(line, pre+"no package changed") })
			out, err := plan(t, fx, script, nothing, c.changed...)
			want := strings.ReplaceAll(strings.Join(c.want, "\n"), shortPH, fx.short)
			if err != nil || out != want {
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
			landing + "nothing compiled changed" + sincePH + unraced, []string{
				pre + "docs/design/2026-10-12-a.md changed: read by the tests of .",
				testedNoRace + " m.test/m"}},
		{"a landing of the starter pack and what no test reads", []string{"packs/starter/roles/builder.md", ".github/a.yml"},
			landing + "packs/starter/roles/builder.md, which a package embeds, changed" + sincePH + selectionSuffix, []string{
				skipSocket,
				pre + "packs/starter/roles/builder.md changed: read by the tests of internal/pack internal/cli",
				pre + ".github/a.yml changed: no test reads it",
				tested + " m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/cli m.test/m/internal/pack m.test/m/packs m.test/m/packtop"}},
		// Nothing to test, so no claim about how: the first line says only what
		// compiled.
		{"a landing of what no test reads", []string{".github/a.yml"},
			landing + "nothing compiled changed" + sincePH, []string{
				pre + ".github/a.yml changed: no test reads it",
				pre + "no package changed since " + shortPH + "; no tests to run"}},
		{"a landing of a go file", []string{"docs/web.md", "alone/p.go"},
			landing + "alone/p.go changed" + sincePH + selectionSuffix, []string{
				skipSocket,
				pre + "docs/web.md changed: read by the tests of .",
				tested + " m.test/m m.test/m/alone m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard"}},
		{"a landing of a test file", []string{"alone/p_test.go"},
			landing + "alone/p_test.go changed" + sincePH + selectionSuffix, []string{
				skipSocket,
				tested + " m.test/m/alone m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard"}},
		{"a landing of go.mod", []string{"go.mod", "docs/web.md"},
			landing + "go.mod changed" + sincePH + wholeSuffix, []string{runSocketAll}},
		{"a landing of go.sum", []string{"go.sum"},
			landing + "go.sum changed" + sincePH + wholeSuffix, []string{runSocketAll}},
		{"a landing of an embedded file", []string{"docs/web.md", "emb/a.txt"},
			landing + "emb/a.txt, which a package embeds, changed" + sincePH + selectionSuffix, []string{
				skipSocket,
				pre + "docs/web.md changed: read by the tests of .",
				tested + " m.test/m m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/emb m.test/m/top"}},
		{"a landing of a file no row knows", []string{"docs/web.md", "contrib/x.txt"},
			landing + "contrib/x.txt, which is read by no known package, changed" + sincePH + wholeSuffix, []string{runSocketAll}},
		// What embedded a file that is gone cannot be asked any more.
		{"a landing that deletes a file", []string{"-docs/web.md"},
			landing + "docs/web.md was deleted" + sincePH + wholeSuffix, []string{skipSocket}},
		// A move deletes the old path too, and what embedded it cannot be
		// asked any more. It used to read as one new file: nothing compiled.
		{"a landing that moves a file out of an embedded glob", []string{"globemb/pages/b.md -> docs/design/b.md"},
			landing + "globemb/pages/b.md was deleted" + sincePH + wholeSuffix, []string{skipSocket}},
		// The whole suite either way. What tells a page that is known from a file
		// nobody reads is the socket line, which goes by the plain selection: the
		// package and its importer hold none of the four suites, where a file the
		// plain run cannot place selects everything.
		{"a landing that deletes a page of a glob beside the package's Go file", []string{"-mdemb/a.md"},
			landing + "mdemb/a.md was deleted" + sincePH + wholeSuffix, []string{skipSocket}},
		{"a landing that moves a page out of a glob beside the package's Go file", []string{"mdemb/b.md -> docs/design/b.md"},
			landing + "mdemb/b.md was deleted" + sincePH + wholeSuffix, []string{skipSocket}},
		{"a landing of nothing", nil,
			landing + "nothing changed" + sincePH + wholeSuffix, []string{skipSocket}},
		// A Go change runs the selection plus the six while its plain
		// selection excludes the socket suites: the gate skips the leg.
		// A change to internal/rpc selects store as an importer: the
		// gate runs the leg, still on the plain selection alone.
		{"a landing touching internal/cli skips the socket suites", []string{"internal/cli/p.go"},
			landing + "internal/cli/p.go changed" + sincePH + selectionSuffix, []string{
				skipSocket,
				tested + " m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/cli"}},
		{"a landing touching internal/rpc runs the socket suites", []string{"internal/rpc/p.go"},
			landing + "internal/rpc/p.go changed" + sincePH + selectionSuffix, []string{
				runSocketStore,
				tested + " m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/rpc m.test/m/internal/store"}},
		{"a landing touching internal/web runs the socket suites", []string{"internal/web/p.go"},
			landing + "internal/web/p.go changed" + sincePH + selectionSuffix, []string{
				runSocketWeb,
				tested + " m.test/m/contrib/assembly m.test/m/contrib/deploy m.test/m/contrib/isolated-yard m.test/m/contrib/peer m.test/m/contrib/release m.test/m/contrib/server-yard m.test/m/internal/web"}},
		{"a landing of an unknown file runs the socket suites", []string{"contrib/x.txt"},
			landing + "contrib/x.txt, which is read by no known package, changed" + sincePH + wholeSuffix, []string{runSocketAll}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fx := rowFixture(t)
			first, rest, err := planned(t, fx, script, []string{"YARDR_LANDING=1"}, c.changed...)
			firstWant := strings.ReplaceAll(c.first, sincePH, fx.since)
			want := strings.ReplaceAll(strings.Join(c.rest, "\n"), shortPH, fx.short)
			if err != nil || first != firstWant || rest != want {
				t.Errorf("err %v, output:\n%s\n%s\nwant:\n%s\n%s", err, first, rest, firstWant, want)
			}
			// A landing that changed code, or whose change cannot be selected,
			// is raced; only a landing of nothing compiled tests without -race.
			if (strings.HasSuffix(c.first, selectionSuffix) || strings.HasSuffix(c.first, wholeSuffix)) && strings.Contains(first+"\n"+rest, "without -race") {
				t.Errorf("a landing of code tests without -race:\n%s\n%s", first, rest)
			}
		})
	}
	// Run, not planned, a plain run tests its selection with a go test that
	// has no -race, whatever it changed (docs/testing.md R4), and so does a
	// landing of nothing compiled: without vet for linux or socket suites.
	// The selection is the packages the table's rows name for the changed
	// files, the root package's among them; a file no row knows and go.mod
	// make it every package. A leaf selection takes no race lock, and its
	// home stays empty: a run that raced would make the lock there and
	// queue for it. A selection that covers the whole suite queues on it
	// as class 1, so those rows set YARDR_RACE_HELD, as the landing rows
	// below do: the worker re-enters from the script's path and cannot
	// start from this test's sh -c. The lock's own tests run those legs
	// from a file and pin the queueing. The go it runs is the real
	// one for the list of packages, with this test's home, and writes down
	// every other call.
	for _, c := range []struct {
		name    string
		env     []string // beyond the plain run's: a landing's mark
		changed []string
		first   string
		calls   []string
		steps   []string
	}{
		{"a plain run of the docs runs without -race", nil, []string{"docs/api.md", "docs/design/2026-10-12-a.md"},
			pre + "what changed" + sincePH + plainWhy,
			[]string{"vet ./...", "build ./...", "test m.test/m m.test/m/internal/rpc"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "go build ./...", "sleep budgets", "redaction marker scan", "go test m.test/m m.test/m/internal/rpc"}},
		{"a landing of one design document runs without -race", []string{"YARDR_LANDING=1"}, []string{"docs/design/2026-10-12-a.md"},
			landing + "nothing compiled changed" + sincePH + unraced,
			[]string{"vet ./...", "build ./...", "test m.test/m"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "go build ./...", "sleep budgets", "redaction marker scan", "go test m.test/m"}},
		// A landing races compiled code; a plain run does not.
		{"a plain run of a go file runs without -race", nil, []string{"alone/p.go"},
			pre + "what changed" + sincePH + plainWhy,
			[]string{"vet ./...", "build ./...", "test m.test/m/alone"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "go build ./...", "sleep budgets", "redaction marker scan", "go test m.test/m/alone"}},
		{"a plain run of a file no row knows tests every package without -race", []string{"YARDR_RACE_HELD=1"}, []string{"contrib/x.txt"},
			pre + "what changed" + sincePH + plainWhy,
			[]string{"vet ./...", "build ./...", "test ./..."},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "go build ./...", "sleep budgets", "redaction marker scan", "go test ./..."}},
		{"a plain run of go.mod tests every package without -race", []string{"YARDR_RACE_HELD=1"}, []string{"go.mod"},
			pre + "what changed" + sincePH + plainWhy,
			[]string{"vet ./...", "build ./...", "test ./..."},
			[]string{"gofmt -l .", "go vet ./...", "go build ./...", "sleep budgets", "redaction marker scan", "go test ./..."}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fx := rowFixture(t)
			realGo, err := exec.LookPath("go")
			if err != nil {
				t.Fatal(err)
			}
			fake, home := t.TempDir(), t.TempDir()
			for name, text := range map[string]string{
				"go":    "#!/bin/sh\nif [ \"$1\" = list ]; then HOME=$REAL_HOME exec \"$REAL_GO\" \"$@\"; fi\necho \"${GOOS:+$GOOS }$*\" >>\"$FAKE/calls\"\n",
				"gofmt": "#!/bin/sh\n",
			} {
				if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			env := append([]string{"YARDR_TEST_PLAN=", "HOME=" + home, "REAL_HOME=" + os.Getenv("HOME"), "FAKE=" + fake, "REAL_GO=" + realGo,
				"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH")}, c.env...)
			first, rest, err := planned(t, fx, script, env, c.changed...)
			firstWant := strings.ReplaceAll(c.first, sincePH, fx.since)
			if err != nil || first != firstWant {
				t.Errorf("err %v, output:\n%s\n%s", err, first, rest)
			}
			calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
			if got, want := strings.TrimSpace(string(calls)), strings.Join(c.calls, "\n"); got != want {
				t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
			}
			// Each step is timed, choosing the packages too, after the first
			// line: that one says which run it is.
			var took []string
			for _, m := range regexp.MustCompile(`(?m)^test: (.*) took \d+ s$`).FindAllStringSubmatch(rest, -1) {
				took = append(took, m[1])
			}
			if !slices.Equal(took, c.steps) {
				t.Errorf("the steps the output times: %q, want %q", took, c.steps)
			}
			// No lock: the home never saw the yard's directory, and the test
			// line says the run waited for nothing.
			if _, err := os.Stat(filepath.Join(home, ".yardr")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the run made the yard's directory in its home, so it took the race lock: %v", err)
			}
			if !regexp.MustCompile(`(?m)^test: sh \.yardr/test @ [0-9a-f]+: pass, [0-9]+ packages ok, 0 failed, [0-9]+ s, waited 0 s$`).MatchString(rest) {
				t.Errorf("the output has no test line that waited 0 s:\n%s", rest)
			}
		})
	}
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
		load    []string // the machine's load, where a row means one: else the quiet one scriptEnv pins
	}{
		{"a landing over internal/cli starts no socket leg", "internal/cli/p.go", "internal/cli/p.go changed", selectionSuffix, skipSocket,
			[]string{"vet ./...", "linux vet ./...", "test -race " + selCLI},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "redaction marker scan", "go test -race " + selCLI}, nil},
		{"a landing over internal/rpc starts the socket leg", "internal/rpc/p.go", "internal/rpc/p.go changed", selectionSuffix, runSocketStore,
			[]string{"vet ./...", "linux vet ./...", "test -race " + selRPC, "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "redaction marker scan", "go test -race " + selRPC,
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}, nil},
		// A loaded machine, on the row that starts the socket leg: the selection's
		// race leg takes the cores the load leaves it (gatepar), and the suites
		// beside it stay go's default. The root package has no seconds to spare
		// (yardr-fvkm), so this row replaces one instead of adding one.
		{"a landing over internal/web starts the socket leg, and on a loaded machine runs its race leg with -p=2", "internal/web/p.go", "internal/web/p.go changed", selectionSuffix, runSocketWeb,
			[]string{"vet ./...", "linux vet ./...", "test -race -p=2 " + selWEB, "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "redaction marker scan", "go test -race -p=2 " + selWEB,
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"},
			[]string{"YARDR_TEST_LOADAVG=40", "YARDR_TEST_CORES=10"}},
		{"a landing over an unknown file starts the socket leg", "contrib/x.txt", "contrib/x.txt, which is read by no known package, changed", wholeSuffix, runSocketAll,
			[]string{"vet ./...", "linux vet ./...", "test -race ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "redaction marker scan", "go test -race ./...",
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fx := rowFixture(t)
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
			first, rest, err := planned(t, fx, script, append([]string{"YARDR_LANDING=1", "YARDR_TEST_PLAN=", "YARDR_RACE_HELD=1", "FAKE=" + fake, "REAL_GO=" + realGo,
				"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH")}, c.load...), c.changed)
			if err != nil || first != landing+c.reason+fx.since+c.suffix {
				t.Errorf("err %v, output:\n%s\n%s", err, first, rest)
			}
			if !strings.Contains(rest, c.line+"\n") {
				t.Errorf("the output has no gate line %q in:\n%s", c.line, rest)
			}
			// One line says the -p, and only where the load made one.
			var wantPar []string
			if c.load != nil {
				wantPar = []string{"test: go test -p=2 (load 40 on 10 cores)"}
			}
			if got := regexp.MustCompile(`(?m)^test: go test -p=.*$`).FindAllString(rest, -1); !slices.Equal(got, wantPar) {
				t.Errorf("the output's -p lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantPar, "\n"))
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
	// A row whose package is gone fails the run, whatever changed. A plain run
	// reads the table in its selection, ahead of its first line as a landing's
	// does, so the complaint is all it says.
	t.Run("a stale row", func(t *testing.T) {
		t.Parallel()
		fx := rowFixture(t)
		stale := strings.Replace(script, "\nreads='\n", "\nreads='\nnowhere/ internal/pack internal/gone\n", 1)
		first, rest, err := planned(t, fx, stale, nil, "docs/web.md")
		if want := pre + "the row for nowhere/ in " + yardTest + " names internal/gone, which is no package here"; err == nil || first != want || rest != "" {
			t.Errorf("err %v, output:\n%s\n%s\nwant:\n%s", err, first, rest, want)
		}
	})
	// A row for a directory the change deletes is skipped, not stale: the
	// branch removes the row, but a landing runs this script as it is on
	// the base, whose row still names the directory. A row for a
	// directory in neither stays a failure.
	t.Run("a row for a directory the change deletes", func(t *testing.T) {
		t.Parallel()
		fx := rowFixture(t)
		deleted := strings.Replace(script, "\nreads='\n", "\nreads='\nfakepkg/ fakepkg .\n", 1)
		out, err := plan(t, fx, deleted, false, "-fakepkg/p.go")
		if want := strings.Join([]string{
			pre + "the row for fakepkg/ in " + yardTest + " names fakepkg, which this change deletes: skipping the row",
			pre + "fakepkg/p.go changed: read by the tests of fakepkg .",
			testedNoRace + " m.test/m",
		}, "\n"); err != nil || out != want {
			t.Errorf("err %v, output:\n%s\nwant:\n%s", err, out, want)
		}
		// The landing that failed for yardr-d860: base's copy, over a
		// change that deletes a rowed directory, is the whole suite,
		// not a stale row.
		first, rest, err := planned(t, fx, deleted, []string{"YARDR_LANDING=1"}, "-fakepkg/p.go")
		if want := landing + "fakepkg/p.go was deleted" + fx.since + wholeSuffix; err != nil || first != want || rest != skipSocket {
			t.Errorf("err %v, output:\n%s\n%s\nwant:\n%s\n%s", err, first, rest, want, skipSocket)
		}
		// A directory move is the deletion of its files and the addition of
		// the new ones, not a rename: git's rename detection used to hide the
		// deletion, which let a landing reach pick() with the base's row
		// still naming fakepkg/ instead of taking the whole-suite path.
		out, err = plan(t, fx, deleted, false, "fakepkg/p.go -> moved/p.go")
		if want := strings.Join([]string{
			pre + "the row for fakepkg/ in " + yardTest + " names fakepkg, which this change deletes: skipping the row",
			pre + "fakepkg/p.go changed: read by the tests of fakepkg .",
			testedNoRace + " m.test/m m.test/m/moved",
		}, "\n"); err != nil || out != want {
			t.Errorf("err %v, output:\n%s\nwant:\n%s", err, out, want)
		}
		first, rest, err = planned(t, fx, deleted, []string{"YARDR_LANDING=1"}, "fakepkg/p.go -> moved/p.go")
		if want := landing + "fakepkg/p.go was deleted" + fx.since + wholeSuffix; err != nil || first != want || rest != skipSocket {
			t.Errorf("err %v, output:\n%s\n%s\nwant:\n%s\n%s", err, first, rest, want, skipSocket)
		}
		stale := strings.Replace(deleted, "\nreads='\n", "\nreads='\nnowhere/ nowhere\n", 1)
		first, rest, err = planned(t, fx, stale, nil, "-fakepkg/p.go")
		if want := pre + "the row for nowhere/ in " + yardTest + " names nowhere, which is no package here"; err == nil || first != want || rest != "" {
			t.Errorf("err %v, output:\n%s\n%s\nwant:\n%s", err, first, rest, want)
		}
	})
}

// What go vet said on 7 Oct 2026, when a file of the shared build cache was
// gone under a run.
const cacheLost = "vet: internal/store/sources.go:10:2: could not import yardr.dev/yardr/internal/source (open ../../../../Library/Caches/go-build/43/439f5c1e-d: no such file or directory)"

// wantLoads checks the load factor the fake go below saw on each call, from
// the loads it wrote down: the race leg's go, its reruns with it, has the
// factor of the load the case pinned (25.3 on 10 cores is 2.5), and no other
// call has one. A landing must have run a race leg, so that a go never given
// a factor cannot pass for one that needed none.
func wantLoads(t *testing.T, fake string, landing bool) {
	t.Helper()
	loads, _ := os.ReadFile(filepath.Join(fake, "loads"))
	raceCalls := 0
	for _, call := range strings.Split(strings.TrimSpace(string(loads)), "\n") {
		want := "unset"
		if strings.HasPrefix(call, "test -race ") {
			want = "2.5"
			raceCalls++
		}
		if !strings.HasSuffix(call, " load="+want) {
			t.Errorf("go was run as %q, want it with load=%s", call, want)
		}
	}
	if landing && raceCalls == 0 {
		t.Errorf("a landing ran no race leg in the loads:\n%s", loads)
	}
}

// A go that fails one of its steps (its first argument, or "rpc" for a call
// that has YARDR_TEST_RPC: a landing runs that one beside its other go test)
// the first few times with a line of its own, printed as it is (printf, since
// some shells' echo reads the backslashes a failing line can have), and writes
// down every call: the arguments, after "rpc" when the call has
// YARDR_TEST_RPC and after the GOOS it was given, and in goflags, a line a
// call, the GOFLAGS it had, and in loads, a line a call, the YARDR_TEST_LOAD
// it had. FAKE_RPC_TIMES and FAKE_RPC_LINE fail the rpc step a case's own
// number of times with a line of its own, beside the FAKE_STEP failure, so a
// landing can strike in one leg and fail really in the other. gofmt finds
// nothing to format.
const fakeGo = `#!/bin/sh
echo "${YARDR_TEST_RPC:+rpc }${GOOS:+$GOOS }$*" >>"$FAKE/calls"
echo "${YARDR_TEST_RPC:+rpc }${GOOS:+$GOOS }$* load=${YARDR_TEST_LOAD-unset}" >>"$FAKE/loads"
echo "$GOFLAGS" >>"$FAKE/goflags"
step=$1
[ -z "$YARDR_TEST_RPC" ] || step=rpc
if [ "$step" = "$FAKE_STEP" ]; then
  n=$(cat "$FAKE/n" 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" >"$FAKE/n"
  if [ "$n" -le "$FAKE_TIMES" ]; then
    printf '%s\n' "$FAKE_LINE" >&2
    exit 1
  fi
fi
if [ "$step" = rpc ] && [ -n "$FAKE_RPC_TIMES" ]; then
  n=$(cat "$FAKE/n-rpc" 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" >"$FAKE/n-rpc"
  if [ "$n" -le "$FAKE_RPC_TIMES" ]; then
    printf '%s\n' "$FAKE_RPC_LINE" >&2
    exit 1
  fi
fi
echo "ran ${YARDR_TEST_RPC:+rpc }$1"
`

// A yardr that writes down every call, answers bead list with
// YARDR_FAKE_LIST and bead show with YARDR_FAKE_SHOW, mints m-filed for bead
// create, keeping the body it was given, and keeps each bead note as a line:
// the bead's id, a space, the text it was given. The gate files a repeat
// flake through it, or notes the open bead that stands for it. The call
// YARDR_FAKE_REFUSE names ("bead list", "bead note", "bead create") fails, as
// with the yard down.
const fakeYardr = `#!/bin/sh
echo "$*" >>"$FAKE/yardr-calls"
if [ -n "$YARDR_FAKE_REFUSE" ] && [ "$1 $2" = "$YARDR_FAKE_REFUSE" ]; then
  echo "the yard is down" >&2
  exit 1
fi
if [ "$1 $2" = "bead list" ]; then
  printf '%s' "$YARDR_FAKE_LIST"
elif [ "$1 $2" = "bead show" ]; then
  printf '%s' "$YARDR_FAKE_SHOW"
elif [ "$1 $2" = "bead create" ]; then
  cat >"$FAKE/yardr-body"
  echo "m-filed"
elif [ "$1 $2" = "bead note" ]; then
  printf '%s %s\n' "$3" "$(cat)" >>"$FAKE/yardr-notes"
  echo "note 1 on $3"
fi
`

// The test command runs a go step once more when it failed because a file
// of Go's build cache was gone, and only then: a second failure stands, a
// step that failed of itself is not run again, and a log that reports a
// race is not run again for the cache either. Both runs' output is
// printed, and a step that failed for good is followed by the lines that
// say what failed: a failing test's report a third time, its own lines
// under it the last. A landing runs the socket
// suites beside the tests, whether those are green or not; each of the two
// is run again by the same rule, either makes the run red, and the socket
// step's output follows the whole of the other's. Every go it runs, each
// run of a step included, has -trimpath in its GOFLAGS, after the caller's
// own, so that the Go cache serves one worktree's results to another.
func TestYardTestRunsAgainWhenTheBuildCacheLostAFile(t *testing.T) {
	t.Parallel()
	const notice = "the Go build cache lost a file under us; running again"
	const failing = "--- FAIL: TestX (0.00s)"
	const failingMsg = "--- FAIL: TestX (0.00s)\n    x_test.go:12: x"
	const compileErr = "internal/foo/foo.go:10:2: undefined: x"
	const raceLost = cacheLost + "\nWARNING: DATA RACE\n--- FAIL: TestX (0.00s)\nFAIL\nFAIL\tm.test/m 0.123s"
	// Thirty failures are more than the summary holds: its which-lines
	// and why-blocks together are capped at 60.
	manyFails := strings.Repeat("--- FAIL: TestMany (0.00s)\n    many_test.go:1: boom\n", 30)
	// The landing rows run at a load of 25.3 on 10 cores: the race leg has -p=3.
	landing := []string{"vet ./...", "linux vet ./...", "test -race -p=3 ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}
	plain := []string{"vet ./...", "build ./...", "test ./..."}
	noBase := []string{"YARDR_LANDING=1", "YARDR_BASE=nowhere", "YARDR_TEST_LOADAVG=25.3", "YARDR_TEST_CORES=10"}
	// One template repository for every row: each copies it, changes go.mod
	// and runs, so no row commits the same module again.
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	template, base, _ := goModTemplate(t)
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
			[]string{plain[0], plain[1], plain[2], plain[2]}, 3, true, nil, "test: FAILED: go test ./..."},
		{"a failing test", "test", "1", failing, false,
			plain, 3, false, nil, "test: FAILED: go test ./...\n" + failing},
		// The summary carries the failure's own lines under its report:
		// a test's message, a build error with no test in it, and never
		// more than 60 lines over both.
		{"a failing test's message", "test", "1", failingMsg, false,
			plain, 2, false, nil, "test: FAILED: go test ./...\n" + failing + "\n" + failingMsg},
		{"a failing build", "build", "1", compileErr, false,
			[]string{plain[0], plain[1]}, 3, false, nil, "test: FAILED: go build ./...\n" + compileErr},
		{"many failures are capped", "test", "1", manyFails, false,
			plain, 1, false, nil, "test: FAILED: go test ./..."},
		// No step fails: the caller's GOFLAGS are among what every go has.
		{"the caller's GOFLAGS stay", "none", "0", "never printed", true,
			plain, 0, false, []string{"GOFLAGS=-mod=mod"}, ""},
		// Landings without a comparable base (CI's shape): the socket
		// suites run whole, ungated, so these cases exercise the leg
		// itself rather than the gate.
		{"landing: vet once", "vet", "1", cacheLost, true,
			[]string{landing[0], landing[0], landing[1], landing[2], landing[3]}, 1, true, noBase, ""},
		{"landing: a failing test", "test", "1", failing, false,
			landing, 3, false, noBase, "test: FAILED: go test -race -p=3 ./...\n" + failing},
		// The race leg run again for the lost file has the load factor too.
		{"landing: the race leg once", "test", "1", cacheLost, true,
			[]string{landing[0], landing[1], landing[2], landing[2], landing[3]}, 1, true, noBase, ""},
		// A race accuses the branch whatever the cache lost with it: the
		// step is not run again for the cache, and the flaky retry
		// refuses it for the same reason. Only a race leg, and so only a
		// landing's, can report one.
		{"landing: a race with a lost cache file", "test", "1", raceLost, false,
			landing, 1, false, noBase, "test: FAILED: go test -race -p=3 ./...\nWARNING: DATA RACE"},
		{"landing: a failing socket test", "rpc", "1", failing, false,
			landing, 3, false, noBase, "test: FAILED: env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/\n" + failing},
		{"landing: the socket step once", "rpc", "1", cacheLost, true,
			[]string{landing[0], landing[1], landing[2], landing[3], landing[3]}, 1, true, noBase, ""},
		{"landing: the socket step twice", "rpc", "2", cacheLost, false,
			[]string{landing[0], landing[1], landing[2], landing[3], landing[3]}, 3, true, noBase, "test: FAILED: env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fake := t.TempDir()
			dir := copyFixture(t, template)
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
				"YARDR_BASE="+base,
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
			wantLoads(t, fake, slices.Contains(c.env, "YARDR_LANDING=1"))
			// Each of those had -trimpath after the caller's own flags: a
			// line a call, so a go that missed it is told from the rest.
			flags, _ := os.ReadFile(filepath.Join(fake, "goflags"))
			wantFlags := "-trimpath"
			for _, kv := range c.env {
				if v, ok := strings.CutPrefix(kv, "GOFLAGS="); ok {
					wantFlags = v + " " + wantFlags
				}
			}
			for i, got := range strings.Split(strings.TrimSpace(string(flags)), "\n") {
				if got != wantFlags {
					t.Errorf("go call %d had GOFLAGS %q, want %q", i+1, got, wantFlags)
				}
			}
			// The socket step's output is whole and after the other's: none
			// of it before the line that says how long go test -race took.
			if at := strings.Index(out, "ran rpc test"); at >= 0 {
				if end := strings.Index(out, "test: go test -race -p=3 ./... took"); end < 0 || at < end {
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
			// The sleep budget and the redaction marker scan are timed
			// too, after the build and before the tests.
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
					steps = slices.Insert(steps, i, "sleep budgets", "redaction marker scan")
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
			if c.summed != "" {
				// The summary holds at most 60 lines after its header,
				// however many failures the log names.
				rest := out[strings.Index(out, "test: FAILED:")+len("test: FAILED:"):]
				if end := strings.Index(rest, "\ntest: "); end >= 0 {
					rest = rest[:end]
				}
				if n := strings.Count(rest, "\n") - 1; n > 60 {
					t.Errorf("the failed step's summary holds %d lines, want at most 60", n)
				}
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
// backlog P3 Flaky bead carrying its lines. A test and its subtests fail
// together and share one bead, so where an open bead already stands for them
// it is noted with the run's log line instead, and a strike that names a
// test with its subtests files or notes once. A test that flaked in 3
// different changes in 7 days fails the step instead of passing on its
// rerun, a change being the beads the log line's beads= field names (the bead
// of the session that ran the gate, else the beads a train's integration
// gives, YARDR_TEST_BEADS), and the line's short sha where it has none, so
// the same change again, at whatever commit, is no more of a strike (the
// filing counts lines, and files a repeat at the second). Where the yardr CLI
// is not there the log line stays the record.
//
// The rerun's output replaces the first run's, so the log line keeps what the
// first run printed for each failed test, behind a separator: the test's
// `--- FAIL:` line and the lines under it (a subtest has a block of its own,
// its parent's holds it), at most 6 lines of 200 characters, joined by a
// backslash and an n, and 20 blocks to a line. With no FAIL line, one block
// keeps a timeout's panic line and running tests, a fatal error's or panic's
// line and the dump's head, else the first output lines, and a timeout names
// its running tests, a fatal error or panic its crashing test, in the head,
// striking and filing as failures do. The printed line stays short. Only the
// part before the separator says which tests flaked: a line without it, as
// the log held before the separator existed, is all of it.
//
// A row is a whole run of the script, 2 to 3 s apiece, and the rows run in
// parallel: the root package has little room under R1's 60 s (yardr-fvkm).
// A case for flakyopen or flakyfile goes in
// TestYardTestKeepsTheFailingLinesOfAFlake, which runs the functions alone.
func TestYardTestRerunsFailedPackagesOnce(t *testing.T) {
	t.Parallel()
	const pkg = "m.test/m"
	const test = "TestX"
	// The log line's separator, and what joins the lines of a block: a
	// backslash and an n, which a raw string spells.
	const sep, eol = " | ", `\n`
	flakyFail := "--- FAIL: " + test + " (0.00s)\nFAIL\nFAIL\t" + pkg + " 0.123s"
	// Reports as go prints them: a parent with two failing subtests and a
	// message of its own after them, noise from the code under test before,
	// between and after the reports, then a second test.
	nestedFail := "2026/10/23 02:02:23 noise before the reports\n" +
		"--- FAIL: TestX (0.50s)\n" +
		"    --- FAIL: TestX/mail (10.20s)\n" +
		"        x_test.go:604: open /tmp/a: no such file or directory\n" +
		"    --- FAIL: TestX/done (10.38s)\n" +
		"        x_test.go:674: open /tmp/b: no such file or directory\n" +
		"    x_test.go:700: the parent's own message\n" +
		"2026/10/23 02:02:24 noise between the reports\n" +
		"--- FAIL: TestY (0.01s)\n" +
		"    y_test.go:5: boom\n" +
		"2026/10/23 02:02:25 noise after the reports\n" +
		"FAIL\nFAIL\t" + pkg + " 0.123s"
	nestedKept := strings.Join([]string{
		"--- FAIL: TestX (0.50s)" + eol + "    --- FAIL: TestX/mail (10.20s)" + eol + "        x_test.go:604: open /tmp/a: no such file or directory" + eol +
			"    --- FAIL: TestX/done (10.38s)" + eol + "        x_test.go:674: open /tmp/b: no such file or directory" + eol + "    x_test.go:700: the parent's own message",
		"--- FAIL: TestX/mail (10.20s)" + eol + "    x_test.go:604: open /tmp/a: no such file or directory",
		"--- FAIL: TestX/done (10.38s)" + eol + "    x_test.go:674: open /tmp/b: no such file or directory",
		"--- FAIL: TestY (0.01s)" + eol + "    y_test.go:5: boom",
	}, sep)
	// What an earlier strike named when it had the same tests: the head of a
	// log line, for the seeded sightings of a run that fails nestedFail.
	nestedPatternFail := strings.ReplaceAll(nestedFail, "TestY", "Test[X]")
	nestedPatternKept := strings.ReplaceAll(nestedKept, "TestY", "Test[X]")
	familyHead := "TestX TestX/done TestX/mail Test[X] (" + pkg + ") passed on rerun"
	// A sighting of another test, whose kept lines mention TestX.
	mentions := "TestOther (m.test/m) passed on rerun" + sep + "--- FAIL: TestOther (0.30s)" + eol + "    x_test.go:9: waiting for TestX to start"
	// A timeout, as go prints it: the panic line, the running tests
	// double-tabbed under a tabbed header, a blank line, then the dump.
	// The head names the running tests; the block keeps the panic line
	// and the entries, not the dump.
	timeoutFail := "panic: test timed out after 10m0s\n\trunning tests:\n\t\tTestX (10m0s)\n\t\tTestY (9m59s)\n\n" +
		"goroutine 5 [running]:\ntesting.(*M).startAlarm.func1()\n\t/go/src/testing/testing.go:2959 +0x2c4\nFAIL\t" + pkg + " 600.123s"
	timeoutKept := "panic: test timed out after 10m0s" + eol + "\t\tTestX (10m0s)" + eol + "\t\tTestY (9m59s)"
	// A timeout that caught one test: its repeat files for the stuck test.
	timeoutSingleFail := "panic: test timed out after 10m0s\n\trunning tests:\n\t\tTestX (10m0s)\n\n" +
		"goroutine 5 [running]:\ntesting.(*M).startAlarm.func1()\n\t/go/src/testing/testing.go:2959 +0x2c4\nFAIL\t" + pkg + " 600.123s"
	timeoutSingleKept := "panic: test timed out after 10m0s" + eol + "\t\tTestX (10m0s)"
	// A fatal error, as go prints it: the dump's first frames are the
	// runtime's own (internal/runtime on this go), then the test's
	// closure, so the head names the test; the block keeps the fatal
	// line and the dump's head.
	fatalFail := "fatal error: concurrent map writes\n\ngoroutine 8 [running]:\ninternal/runtime/maps.fatal({0x1?, 0x0?})\n" +
		"\t/go/src/runtime/panic.go:1195 +0x20\n" + pkg + ".TestX.func1()\n\tx_test.go:16 +0x60\n" +
		"created by " + pkg + ".TestX in goroutine 4\n\tx_test.go:13 +0x48\nFAIL\t" + pkg + " 0.280s"
	fatalKept := "fatal error: concurrent map writes" + eol + "goroutine 8 [running]:" + eol + "internal/runtime/maps.fatal({0x1?, 0x0?})" + eol +
		"\t/go/src/runtime/panic.go:1195 +0x20" + eol + pkg + ".TestX.func1()" + eol + "\tx_test.go:16 +0x60"
	// A fatal error whose first goroutine holds testing and runtime
	// frames only: no test is named, so the head stays package-only,
	// and the block still keeps the dump.
	fatalNoFrameFail := "fatal error: all goroutines are asleep - deadlock!\n\ngoroutine 1 [chan receive]:\nruntime.gopark()\n" +
		"\t/go/src/runtime/proc.go:424 +0x12c\ntesting.tRunner()\n\t/go/src/testing/testing.go:2258 +0x3b8\nFAIL\t" + pkg + " 0.280s"
	fatalNoFrameKept := "fatal error: all goroutines are asleep - deadlock!" + eol + "goroutine 1 [chan receive]:" + eol + "runtime.gopark()" + eol +
		"\t/go/src/runtime/proc.go:424 +0x12c" + eol + "testing.tRunner()" + eol + "\t/go/src/testing/testing.go:2258 +0x3b8"
	// A TestMain that exits after its last word: no test is named
	// anywhere, so the head stays package-only, and the block keeps
	// the first output lines.
	testMainFail := "about to die\nFAIL\t" + pkg + " 0.219s\nFAIL"
	raceFail := "WARNING: DATA RACE\n" + flakyFail
	racePkg, ordinaryPkg := pkg+"/race", pkg+"/ordinary"
	raceFailMixed := "WARNING: DATA RACE\n--- FAIL: TestRace (0.00s)\nFAIL\nFAIL\t" + racePkg + " 0.123s\n" +
		"--- FAIL: TestOrdinary (0.00s)\nFAIL\nFAIL\t" + ordinaryPkg + " 0.123s"
	running := "test: running the failed packages again: " + pkg
	norace := "test: not running the failed packages again: DATA RACE in " + pkg
	title := "Flaky: " + test + " (" + pkg + ")"
	listHeader := "ID                 TRAIN  PRI  TYPE    STAGE    STATUS  GROUP           DEPOT         TITLE\n"
	listRow := func(id, title string) string {
		return fmt.Sprintf("%-18s %-6s %-4s %-7s %-8s %-7s %-15s %-13s %s\n", id, "", "P3", "task", "backlog", "open", "-", "yardr", title)
	}
	other := listHeader + listRow("m-1", "Something else")
	plainSteps := []string{"gofmt -l .", "go vet ./...", "go build ./...", "sleep budgets", "redaction marker scan", "go test ./..."}
	landingSteps := []string{"gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "sleep budgets", "redaction marker scan", "go test -race -p=3 ./...",
		"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}
	landing := []string{"vet ./...", "linux vet ./...", "test -race -p=3 ./...", "test -race -p=3 " + pkg, "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}
	plain := []string{"vet ./...", "build ./...", "test ./...", "test " + pkg}
	landingNoRetry := []string{"vet ./...", "linux vet ./...", "test -race -p=3 ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/"}
	landingStrikeReal := []string{"vet ./...", "linux vet ./...", "test -race -p=3 ./...", "test -race -p=3 " + pkg,
		"rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/ ./internal/web/", "rpc test " + pkg}
	// The load is pinned, so what the race leg is given is known: 25.3 on 10
	// cores is a factor of 2.5 and a -p of 3, which the rerun of its failed
	// packages keeps; the socket suites beside the leg stay go's default.
	noBase := []string{"YARDR_LANDING=1", "YARDR_BASE=nowhere", "YARDR_TEST_LOADAVG=25.3", "YARDR_TEST_CORES=10"}
	// One template repository for every row: each copies it, changes go.mod
	// and runs, so no row commits the same module again.
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	template, base, short := goModTemplate(t)
	for _, c := range []struct {
		name     string
		testName string // override the failed test name
		glob     bool   // add a path that an accidental test-name glob would match
		times    string // how often the test step fails: once (flaky) or twice
		ok       bool
		code     int    // expected exit status when !ok: 3 for strike-only, 1 otherwise; 0 means 3 when strike, else 1
		rpcTimes string // how often the rpc step fails beside the test step: empty for green
		rpcLine  string // the failure the rpc step fails with: the test step's line when empty
		calls    []string
		env      []string // variables of the script's environment beyond the base
		steps    []string
		line     string          // the failure the step fails with: empty for a flake
		racePkgs string          // packages whose output includes the race warning
		raced    bool            // the failure reports a race, so there is no rerun
		seed     []time.Duration // ages of the flaky log's prior sightings
		seedShas []string        // the short sha of each prior sighting, in order: the run's own for those not named
		seedBead []string        // the beads field of each prior sighting, in order: none for those not named
		beads    string          // the beads field the run's own line carries: none when empty
		load     string          // the load words the run's own line ends with: the pinned load when empty
		yardr    bool            // a stub yardr is on PATH, answering list and show
		list     string          // what the stub's bead list prints
		show     string          // what the stub's bead show prints
		barePath bool            // PATH holds no yardr at all, as CI's does not
		create   bool            // the run files the flake through the stub
		depot    string          // the --depot the filing names: empty for none
		strike   bool            // the flake is a third in 7 days: the step fails
		tests    string          // the tests the flaky line names, space separated: testName when empty
		kept     string          // what the log line keeps behind the separator: the bare FAIL line when line is empty
		noTests  bool            // the failure names no test, so the flaky line names the package and the log line keeps nothing
		seedAs   string          // the text of each seeded sighting after its sha: the short head when empty
		notes    []string        // "<bead> <test>" for each open bead the run notes with its log line, in order: the test whose pass found it
		foreign  string          // one more seeded line, which names the test only behind its separator
	}{
		{name: "plain: passes on rerun", times: "1", ok: true, calls: plain, steps: plainSteps},
		{name: "plain: fails twice", times: "2", ok: false, calls: plain, steps: plainSteps},
		// A repeat files through the stub: without a depot where the
		// session names none, into YARDR_DEPOT where it does, else into
		// its bead's depot. An open bead carrying the title files
		// nothing; a third change in 7 days fails the step, a second does
		// not. Most of these runs name no bead, so a line is counted by its sha.
		// Sightings of any age file, but only recent ones strike, and
		// only on commits of their own: the seeds are at the run's commit
		// unless they name another, so three lines on one commit file a
		// repeat and do not strike.
		{name: "plain: a repeat flake on a second commit files a bead", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour}, seedShas: []string{"a1b2c3d4"}, yardr: true, list: other, create: true},
		{name: "plain: test name glob characters stay literal", testName: "TestX*", glob: true, times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour}, yardr: true, list: other, create: true},
		{name: "plain: a repeat flake files into YARDR_DEPOT", times: "1", ok: true, calls: plain, steps: plainSteps,
			env: []string{"YARDR_DEPOT=m"}, seed: []time.Duration{24 * time.Hour}, yardr: true, list: other, create: true, depot: "m"},
		// The session's bead ends the line's head (beads=), and a strike counts
		// changes by it, whatever commits they were tested at: here x is
		// tested at two commits (one seeded, the run's own) and y at a third,
		// two changes at three commits, where counting shas struck.
		{name: "plain: a repeat flake files into its bead's depot, and the bead's runs at three commits are one change", times: "1", ok: true, calls: plain, steps: plainSteps,
			env: []string{"YARDR_BEAD=x"}, beads: "x", seed: []time.Duration{24 * time.Hour, 36 * time.Hour},
			seedShas: []string{"a1b2c3d4", "e5f6a7b8"}, seedBead: []string{"y", "x"}, yardr: true, list: other, show: "  depot m\n", create: true, depot: "m"},
		// An open bead for the test is noted with the run's line, not filed
		// again, whether the strike below then fails the step or not.
		{name: "plain: an open Flaky bead is noted once, and three lines on one commit do not strike", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour, 48 * time.Hour}, yardr: true, list: other + listRow("m-2", title), notes: []string{"m-2 " + test}},
		// Four lines, three changes: a line with no bead (written before the
		// field, counted by its sha), a bead on two lines, and the run's own
		// bead. The step fails, naming three, after the open bead is noted.
		{name: "plain: a third change in 7 days fails the step", times: "1", ok: false, calls: plain, steps: plainSteps,
			env: []string{"YARDR_BEAD=z"}, beads: "z", seed: []time.Duration{24 * time.Hour, 36 * time.Hour, 48 * time.Hour},
			seedShas: []string{"a1b2c3d4", "e5f6a7b8", "c9d0e1f2"}, seedBead: []string{"", "x", "x"},
			yardr: true, list: other + listRow("m-2", title), show: "  depot m\n", strike: true, notes: []string{"m-2 " + test}},
		{name: "plain: an old repeat still files", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{30 * 24 * time.Hour}, yardr: true, list: other, create: true},
		// Old, on commits of their own: with the window dropped these would
		// be three commits with the run's.
		{name: "plain: old sightings do not strike", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{8 * 24 * time.Hour, 9 * 24 * time.Hour}, seedShas: []string{"a1b2c3d4", "e5f6a7b8"},
			yardr: true, list: other + listRow("m-2", title), notes: []string{"m-2 " + test}},
		{name: "plain: without the yardr CLI a repeat only logs", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour}, barePath: true},
		// The rerun's output replaces the first run's, so the log line keeps
		// the failing lines of each failed test behind a separator, and the
		// printed line stays short.
		{name: "plain: a subtest has a block of its own and its parent's holds it", times: "1", ok: true, calls: plain, steps: plainSteps,
			line: nestedFail, tests: "TestX TestX/done TestX/mail TestY", kept: nestedKept},
		// A failure with no `--- FAIL:` line keeps what the log says
		// instead. A timeout names its running tests in the head, and a
		// repeat files for the stuck test with the dump lines carried.
		// A fatal error names its crashing test past the runtime's own
		// frames, or nothing where there is no test frame. A TestMain
		// that exits names no test, and keeps its last lines.
		{name: "plain: a timeout names its running tests", times: "1", ok: true, calls: plain, steps: plainSteps,
			line: timeoutFail, tests: "TestX TestY", kept: timeoutKept},
		{name: "plain: a timeout's repeat files for the stuck test", times: "1", ok: true, calls: plain, steps: plainSteps,
			line: timeoutSingleFail, kept: timeoutSingleKept,
			seed: []time.Duration{24 * time.Hour}, seedShas: []string{"a1b2c3d4"}, yardr: true, list: other, create: true},
		{name: "plain: a fatal error names its crashing test", times: "1", ok: true, calls: plain, steps: plainSteps,
			line: fatalFail, kept: fatalKept},
		{name: "plain: a fatal error with no test frame keeps the dump under a package-only head", times: "1", ok: true, calls: plain, steps: plainSteps,
			line: fatalNoFrameFail, noTests: true, kept: fatalNoFrameKept},
		{name: "plain: a TestMain that exits keeps its last lines under a package-only head", times: "1", ok: true, calls: plain, steps: plainSteps,
			line: testMainFail, noTests: true, kept: "about to die"},
		// The run's own line ends with the load the gate measured as it
		// wrote the line: a quiet load and a loaded one in the same form,
		// "load unknown" where the readings are no numbers. The filed body
		// and the note carry the line whole, words with it.
		{name: "plain: the flaky log line ends with a quiet load, and the filed body carries it", times: "1", ok: true, calls: plain, steps: plainSteps,
			env: []string{"YARDR_TEST_LOADAVG=0.7", "YARDR_TEST_CORES=8"}, load: "load 0.7 on 8 cores",
			seed: []time.Duration{24 * time.Hour}, yardr: true, list: other, create: true},
		{name: "plain: the flaky log line ends with a load past the cores, and the note carries it", times: "1", ok: true, calls: plain, steps: plainSteps,
			env: []string{"YARDR_TEST_LOADAVG=72.4", "YARDR_TEST_CORES=10"}, load: "load 72.4 on 10 cores",
			seed: []time.Duration{24 * time.Hour}, yardr: true, list: other + listRow("m-2", title), notes: []string{"m-2 " + test}},
		{name: "plain: the flaky log line ends with load unknown when the readings are no numbers", times: "1", ok: true, calls: plain, steps: plainSteps,
			env: []string{"YARDR_TEST_LOADAVG=bogus", "YARDR_TEST_CORES=10"}, load: "load unknown",
			seed: []time.Duration{24 * time.Hour}, yardr: true, list: other, create: true},
		// A test and its subtests fail together and share one bead. A strike
		// that names a test with two subtests, and another test, files one
		// bead for the three, with the lines whole and the kept ones with
		// them, or notes the open one once, and the other test's beside it.
		// The strikes are each test's own: the second case is a third commit
		// in 7 days for all four, and the step fails naming each.
		{name: "plain: a repeat of a test and its subtests files one bead with the kept lines too", times: "1", ok: true, calls: plain, steps: plainSteps,
			line: nestedFail, tests: "TestX TestX/done TestX/mail TestY", kept: nestedKept,
			seed: []time.Duration{24 * time.Hour}, yardr: true, list: other, create: true,
			seedAs:  "TestX TestX/done TestX/mail (m.test/m) passed on rerun" + sep + "--- FAIL: TestX (0.30s)" + eol + "    x_test.go:9: an older boom",
			foreign: mentions},
		{name: "plain: bracket characters in another test name stay literal across families", times: "1", ok: false, calls: plain, steps: plainSteps,
			line: nestedPatternFail, tests: "TestX TestX/done TestX/mail Test[X]", kept: nestedPatternKept,
			seed: []time.Duration{24 * time.Hour, 48 * time.Hour}, seedShas: []string{"a1b2c3d4", "e5f6a7b8"},
			yardr: true, list: other + listRow("m-2", title) + listRow("m-3", "Flaky: Test[X] ("+pkg+")"),
			seedAs: familyHead, notes: []string{"m-2 TestX", "m-3 Test[X]"}, strike: true},
		// Only the part before the separator names the tests that flaked: a
		// test that another test's kept lines mention has no sighting of its
		// own, which would file it and strike it.
		{name: "plain: a test named only in another's kept lines is no sighting", times: "1", ok: true, calls: plain, steps: plainSteps,
			seed: []time.Duration{24 * time.Hour, 48 * time.Hour}, yardr: true, list: other,
			seedAs: mentions},
		// Landings without a comparable base (CI's shape): the socket
		// suites run whole, ungated, so these cases exercise the leg
		// itself rather than the gate.
		{name: "landing: fails twice", times: "2", ok: false, calls: landing, env: noBase, steps: landingSteps},
		{name: "landing: passes on rerun", times: "1", ok: true, calls: landing, env: noBase, steps: landingSteps},
		// Only a race leg, and so only a landing's, can report a race.
		{name: "landing: a race fails without a retry", times: "1", ok: false, calls: landingNoRetry, env: noBase, steps: landingSteps,
			line: raceFail, racePkgs: pkg, raced: true},
		{name: "landing: a race names only the racing package", times: "1", ok: false, calls: landingNoRetry, env: noBase, steps: landingSteps,
			line: raceFailMixed, racePkgs: racePkg, raced: true},
		// A strike-only landing exits 3, with a fail line naming 0 failed;
		// a strike beside a real failure in the other leg exits 1.
		{name: "landing: a strike-only run exits 3", times: "1", ok: false, calls: landing, env: noBase, steps: landingSteps,
			seed: []time.Duration{24 * time.Hour, 48 * time.Hour}, seedShas: []string{"a1b2c3d4", "e5f6a7b8"},
			yardr: true, list: other + listRow("m-2", title), strike: true, notes: []string{"m-2 " + test}},
		{name: "landing: a strike beside a real failure exits 1", times: "1", ok: false, code: 1, calls: landingStrikeReal, env: noBase, steps: landingSteps,
			rpcTimes: "2",
			seed:     []time.Duration{24 * time.Hour, 48 * time.Hour}, seedShas: []string{"a1b2c3d4", "e5f6a7b8"},
			yardr: true, list: other + listRow("m-2", title), strike: true, notes: []string{"m-2 " + test}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			testName := test
			if c.testName != "" {
				testName = c.testName
			}
			// The head of the log line, which the printed line repeats: the
			// tests that failed and their package.
			names := testName
			if c.tests != "" {
				names = c.tests
			}
			head := names + " (" + pkg + ") passed on rerun"
			if c.noTests {
				head = pkg + " passed on rerun"
			}
			flaky := "test: flaky: " + head
			caseTitle := "Flaky: " + testName + " (" + pkg + ")"
			// The load words the run's own line ends with: the quiet load
			// the script's environment pins, the loaded one the landing
			// rows pin, or the case's own.
			load := "load 1.5 on 10 cores"
			if slices.Contains(c.env, "YARDR_LANDING=1") {
				load = "load 25.3 on 10 cores"
			}
			if c.load != "" {
				load = c.load
			}
			// The step a failure summary names: a landing's is raced, a plain
			// run's is not.
			failedStep := "go test ./..."
			if slices.Contains(c.env, "YARDR_LANDING=1") {
				failedStep = "go test -race -p=3 ./..."
			}
			fake := t.TempDir()
			dir := copyFixture(t, template)
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
			// A branch that changed go.mod: a plain run tests every
			// package, and asks go for no list of them.
			mod := filepath.Join(dir, "go.mod")
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
			sha := short
			// The sha each line of the log carries, in file order: the run's
			// own is the last.
			var lineShas []string
			// Prior sightings, at the short sha the run appends under unless
			// the case names another, so the log's lines all read as the
			// gate's own. Without a text of their own they are the short
			// lines the log held before the separator existed.
			if len(c.seed) > 0 {
				seedText := testName + " (" + pkg + ") passed on rerun"
				if c.seedAs != "" {
					seedText = c.seedAs
				}
				var seeds []string
				if c.foreign != "" {
					seeds = append(seeds, time.Now().Add(-time.Hour).Format("2006-01-02 15:04:05")+" "+sha+" "+c.foreign)
					lineShas = append(lineShas, sha)
				}
				for i, age := range c.seed {
					at := sha
					if i < len(c.seedShas) {
						at = c.seedShas[i]
					}
					seeded := time.Now().Add(-age).Format("2006-01-02 15:04:05") + " " + at + " " + seedText
					if i < len(c.seedBead) && c.seedBead[i] != "" {
						seeded += " beads=" + c.seedBead[i]
					}
					seeds = append(seeds, seeded)
					lineShas = append(lineShas, at)
				}
				if err := os.WriteFile(flakyLog, []byte(strings.Join(seeds, "\n")+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			lineShas = append(lineShas, sha)
			cmd := exec.Command("sh", "-c", string(script))
			cmd.Dir = dir
			// The case's own variables come last, so a landing's nowhere
			// base wins over the commit below. The bead and depot of the
			// run this test is in are cleared first: the script files a
			// repeat into them, and the test's own would leak in. The
			// locale is C: the script sorts test names with sort, whose
			// order for a name with brackets differs by locale, and the
			// rows pin the order it lists and notes them in.
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
				"LC_ALL=C",
				"YARDR_BASE="+base,
				"YARDR_BEAD=", "YARDR_DEPOT=",
				"TMPDIR="+tmp, "FAKE="+fake, "FAKE_STEP=test", "FAKE_TIMES="+c.times, "FAKE_LINE="+line, "YARDR_RACE_HELD=1")
			if c.rpcTimes != "" {
				rpcLine := c.rpcLine
				if rpcLine == "" {
					rpcLine = line
				}
				cmd.Env = append(cmd.Env, "FAKE_RPC_TIMES="+c.rpcTimes, "FAKE_RPC_LINE="+rpcLine)
			}
			if c.yardr {
				cmd.Env = append(cmd.Env, "YARDR_FAKE_LIST="+c.list, "YARDR_FAKE_SHOW="+c.show)
			}
			cmd.Env = append(cmd.Env, c.env...)
			b, err := cmd.CombinedOutput()
			out := string(b)
			wantCode := 0
			if !c.ok {
				wantCode = 1
				if c.code != 0 {
					wantCode = c.code
				} else if c.strike {
					wantCode = 3
				}
			}
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != wantCode {
				t.Errorf("exit %d, want %d (ok %v)", code, wantCode, c.ok)
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
			wantLoads(t, fake, slices.Contains(c.env, "YARDR_LANDING=1"))
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
			var logged []string
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
				if s := strings.TrimSpace(string(logb)); s != "" {
					logged = strings.Split(s, "\n")
				}
				priors := len(c.seed)
				if c.foreign != "" {
					priors++
				}
				if len(logged) != n+priors {
					t.Errorf("the flaky log has %d lines, want one per printed flaky line (%d) plus the %d priors", len(logged), n, priors)
				}
				dated := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} `)
				wants := []string{pkg, testName}
				if c.noTests {
					wants = []string{pkg}
				}
				for i, line := range logged {
					lineWants := wants
					if i < len(lineShas) {
						lineWants = append(slices.Clone(wants), lineShas[i])
					}
					for _, want := range lineWants {
						if !strings.Contains(line, want) {
							t.Errorf("flaky log line %q, want it to carry %q", line, want)
						}
					}
					if !dated.MatchString(line) {
						t.Errorf("flaky log line %q, want it to start with the date", line)
					}
				}
				// The run's own line is the last: the date, the head the
				// printed line repeats, and, behind the separator, what the
				// first run printed for each failed test, and last the load
				// the gate measured as it wrote the line. A failure that
				// names no test keeps none, and its line is the short one
				// with the load after it.
				if len(logged) > 0 {
					kept := c.kept
					if c.line == "" {
						kept = "--- FAIL: " + testName + " (0.00s)"
					}
					want := sha + " " + head
					if c.beads != "" {
						want += " beads=" + c.beads
					}
					if kept != "" {
						want += sep + kept
					}
					want += " " + load
					if last := logged[len(logged)-1]; len(last) < len("2006-01-02 15:04:05 ") || last[len("2006-01-02 15:04:05 "):] != want {
						t.Errorf("the run's flaky log line is %q, want the date and then %q", last, want)
					}
				}
				if c.strike {
					for _, name := range strings.Fields(names) {
						if want := "test: " + name + " flaked on 3 changes in 7 days; see the Flaky: bead\n"; !strings.Contains(out, want) {
							t.Errorf("the output has no %q line", strings.TrimSuffix(want, "\n"))
						}
					}
					if !strings.Contains(out, "test: FAILED: "+failedStep+"\n") {
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
					want := "test: FAILED: " + failedStep + "\nWARNING: DATA RACE"
					if !strings.Contains(out, want) || !strings.Contains(out, "FAIL\t"+pkg) {
						t.Errorf("the output's summary of a failed step: want %q and a FAIL line for %s", want, pkg)
					}
				} else {
					want := "test: FAILED: " + failedStep + "\n--- FAIL: " + testName
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
			var noteCalls []string
			creates, lists, shows := 0, 0, 0
			for _, call := range strings.Split(strings.TrimSpace(string(ycalls)), "\n") {
				switch {
				case strings.HasPrefix(call, "bead create "):
					created = call
					creates++
				case call == "bead list":
					lists++
				case strings.HasPrefix(call, "bead show "):
					shows++
				case strings.HasPrefix(call, "bead note "):
					noteCalls = append(noteCalls, call)
				case call != "":
					t.Errorf("the stub was run for %q", call)
				}
			}
			// One bead for a test and its subtests, however many of their
			// names the strike carries.
			wantCreates := 0
			if c.create {
				wantCreates = 1
			}
			if creates != wantCreates {
				t.Errorf("the run filed %d beads, want %d", creates, wantCreates)
			}
			// An open bead that stands for the test is noted with the line
			// the run added to the log, whole, on stdin, and nothing is filed.
			// The stub keeps each note as the bead's id and the text.
			var wantCalls, wantNotes []string
			for _, n := range c.notes {
				id, _, _ := strings.Cut(n, " ")
				wantCalls = append(wantCalls, "bead note "+id+" -")
				if len(logged) > 0 {
					wantNotes = append(wantNotes, id+" "+logged[len(logged)-1])
				}
			}
			if !slices.Equal(noteCalls, wantCalls) {
				t.Errorf("the run noted:\n%s\nwant:\n%s", strings.Join(noteCalls, "\n"), strings.Join(wantCalls, "\n"))
			}
			recorded, _ := os.ReadFile(filepath.Join(fake, "yardr-notes"))
			var gotNotes []string
			if s := strings.TrimSpace(string(recorded)); s != "" {
				gotNotes = strings.Split(s, "\n")
			}
			if !slices.Equal(gotNotes, wantNotes) {
				t.Errorf("the notes written are:\n%s\nwant the run's own log line, whole, on each:\n%s", strings.Join(gotNotes, "\n"), strings.Join(wantNotes, "\n"))
			}
			for _, note := range gotNotes {
				if !strings.HasSuffix(note, " "+load) {
					t.Errorf("the note written is %q, want it to end with the load %q", note, load)
				}
			}
			for _, n := range c.notes {
				id, noted, _ := strings.Cut(n, " ")
				if want := "test: noted " + id + ": Flaky: " + noted + " (" + pkg + ")\n"; !strings.Contains(out, want) {
					t.Errorf("the output has no %q line", strings.TrimSuffix(want, "\n"))
				}
			}
			if len(c.notes) == 0 && strings.Contains(out, "test: noted") {
				t.Errorf("the output notes a bead where none was to be noted")
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
					if !strings.Contains(line, " ("+pkg+") passed on rerun") {
						t.Errorf("the filed body line %q, want the test's log line", line)
					}
				}
				// Whole lines, the kept ones with them: the body is what the
				// log says of the test, here every line that names it in its
				// head, not the one that mentions it behind the separator.
				named := slices.DeleteFunc(slices.Clone(logged), func(line string) bool { return c.foreign != "" && strings.HasSuffix(line, c.foreign) })
				if !slices.Equal(carried, named) {
					t.Errorf("the filed body is:\n%s\nwant the flaky log's lines that name the test:\n%s", strings.Join(carried, "\n"), strings.Join(named, "\n"))
				}
				if len(carried) > 0 {
					if last := carried[len(carried)-1]; !strings.HasSuffix(last, " "+load) {
						t.Errorf("the filed body's last line is %q, want it to end with the load %q", last, load)
					}
				}
				if want := "test: filed m-filed: " + caseTitle + "\n"; !strings.Contains(out, want) {
					t.Errorf("the output has no %q line", strings.TrimSuffix(want, "\n"))
				}
			} else if strings.Contains(out, "test: filed") {
				t.Errorf("the output files a bead where none was to be filed")
			}
			if c.yardr {
				// Every pass that files or notes consults the open beads once,
				// a pass a test and its subtests share; only a case whose bead
				// names a depot asks the CLI for it.
				wantLists := len(c.notes)
				if c.create {
					wantLists++
				}
				if lists != wantLists {
					t.Errorf("the run listed beads %d times, want %d", lists, wantLists)
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
			// The test line: a strike-only run fails naming 0 failed, a
			// real red naming its failed packages.
			if !c.ok {
				lines := strings.Split(strings.TrimSpace(out), "\n")
				last := lines[len(lines)-1]
				if wantCode == 3 {
					if !strings.Contains(last, ": fail, ") || !strings.Contains(last, " 0 failed, ") {
						t.Errorf("test line %q, want fail naming 0 failed", last)
					}
				} else if !strings.Contains(last, ": fail, ") || strings.Contains(last, " 0 failed, ") {
					t.Errorf("test line %q, want fail naming its failed packages", last)
				}
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

// What the flaky log line keeps of the first run (flakyfails), the tests a
// failure names without a FAIL line (flakystuck), which of its lines name a
// test (flakylines), the beads it says it tested (flakybeads), in how many
// changes a test flaked in the last 7 days (flakychanges), which open bead
// stands for a test (flakyopen) and what a repeat does with it (flakyfile),
// through the script's own functions, cut out of it and run on output as go
// prints it and on a listing as yardr prints it: a shell and an awk or a
// perl a case, where a gate run costs seconds.
// TestYardTestRerunsFailedPackagesOnce pins that the gate writes and reads
// them. A block is a test's `--- FAIL:` line and the lines indented deeper
// than it, so a subtest has a block of its own and its parent's holds it;
// output between reports ends a block; a panic's line, which go prints
// unindented after the FAIL line, joins the blocks it follows; a block keeps
// 6 lines of 200 characters and a line 20 blocks; control characters are
// dropped. With no FAIL line, one block keeps a timeout's panic line and
// running tests, a fatal error's or panic's line and the dump's head, else
// the first output lines; a timeout names its running tests, a fatal error
// or panic its crashing test past the runtime's frames. Only the head of a
// log line, before the separator, names the tests that flaked and the beads
// it tested. A change is the set of beads a line's
// last field names, in any order, whatever commits it was tested at, and a
// line with no field is a change of its sha: the same one on many lines is
// one change, and only lines in the last 7 days count, whichever change they
// are of. The bead that stands for a test is its own, else its top-level
// test's, else any other subtest's of that one, in the packages its title
// names; with one open the repeat notes it with the run's line, with none it
// files, and a yard that refuses a call only warns.
func TestYardTestKeepsTheFailingLinesOfAFlake(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	// The functions as the script has them, from the separator to the run's
	// test line: a script the cuts no longer follow fails the test, telling
	// its editor to move the probe with it.
	_, fns, ok := strings.Cut(string(b), "\nflaky_sep=")
	if ok {
		fns, _, ok = strings.Cut("flaky_sep="+fns, "\n# summary prints the run's test line")
	}
	for _, fn := range []string{"flakyfails", "flakystuck", "flakylines", "flakybeads", "flakychanges", "flakyopen", "flakyfile"} {
		ok = ok && strings.Contains(fns, "\n"+fn+"() {")
	}
	if !ok {
		t.Fatalf("cannot cut flakyfails, flakystuck, flakylines, flakybeads, flakychanges, flakyopen and flakyfile out of %s: move the probe with the script", yardTest)
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probe, []byte(fns+"\n\"$@\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// run calls one of the functions on a file, and returns what it printed
	// less its last newline.
	run := func(t *testing.T, in, fn string, args ...string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "in")
		if err := os.WriteFile(file, []byte(in), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", append([]string{probe, fn}, append(args, file)...)...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("%s: %v\n%s", fn, err, stderr.String())
		}
		return strings.TrimSuffix(string(out), "\n")
	}
	const sep, eol = " | ", `\n`
	block := func(lines ...string) string { return strings.Join(lines, eol) }
	// A report of seven lines, the fifth 300 characters long: six are kept,
	// the fifth cut at 200, and the seventh is dropped.
	long := "    a_test.go:5: " + strings.Repeat("x", 300)
	// Twenty-five tests fail with a line each: twenty blocks are kept.
	var manyIn, manyBlocks []string
	for i := range 25 {
		manyIn = append(manyIn, fmt.Sprintf("--- FAIL: TestT%02d (0.00s)\n    t_test.go:1: boom %02d", i, i))
		if i < 20 {
			manyBlocks = append(manyBlocks, block(fmt.Sprintf("--- FAIL: TestT%02d (0.00s)", i), fmt.Sprintf("    t_test.go:1: boom %02d", i)))
		}
	}
	for _, c := range []struct {
		name string
		in   string
		want []string // the blocks, in the order the reports print them
	}{
		{"an assertion", "--- FAIL: TestA (0.00s)\n    a_test.go:10: got 1, want 2\nFAIL\nFAIL\tm.test/a 0.1s\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:10: got 1, want 2")}},
		{"a report with no lines under it", "--- FAIL: TestA (0.00s)\nFAIL\nFAIL\tm.test/a 0.1s\n",
			[]string{"--- FAIL: TestA (0.00s)"}},
		// go indents a message's later lines by 8 and prints a blank one as
		// the 8 spaces: neither ends the block, nor is kept.
		{"a message of several lines", "--- FAIL: TestA (0.00s)\n    a_test.go:15: first line\n        \n        third line\n    a_test.go:16: stop here\nFAIL\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:15: first line", "        third line", "    a_test.go:16: stop here")}},
		{"output between reports is not kept", "--- FAIL: TestA (0.00s)\n    a_test.go:1: one\n2026/10/23 02:02:23 noise from the code under test\n    a_test.go:2: under no report\n--- FAIL: TestB (0.00s)\n    b_test.go:1: two\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:1: one"), block("--- FAIL: TestB (0.00s)", "    b_test.go:1: two")}},
		// The parent's message after its subtest is at the subtest's indent:
		// it ends the subtest's block and belongs to the parent's.
		{"a subtest and its parent's later message", "--- FAIL: TestP (0.01s)\n    --- FAIL: TestP/child (0.00s)\n        p_test.go:21: child went wrong\n    p_test.go:26: parent after\nFAIL\n",
			[]string{
				block("--- FAIL: TestP (0.01s)", "    --- FAIL: TestP/child (0.00s)", "        p_test.go:21: child went wrong", "    p_test.go:26: parent after"),
				block("--- FAIL: TestP/child (0.00s)", "    p_test.go:21: child went wrong"),
			}},
		// Each block is shown as the test would print at the top level: the
		// indentation of its FAIL line comes off its lines.
		{"three deep", "--- FAIL: TestQ (0.00s)\n    --- FAIL: TestQ/mid (0.00s)\n        --- FAIL: TestQ/mid/deep (0.00s)\n            q_test.go:24: deep went wrong\nFAIL\n",
			[]string{
				block("--- FAIL: TestQ (0.00s)", "    --- FAIL: TestQ/mid (0.00s)", "        --- FAIL: TestQ/mid/deep (0.00s)", "            q_test.go:24: deep went wrong"),
				block("--- FAIL: TestQ/mid (0.00s)", "    --- FAIL: TestQ/mid/deep (0.00s)", "        q_test.go:24: deep went wrong"),
				block("--- FAIL: TestQ/mid/deep (0.00s)", "    q_test.go:24: deep went wrong"),
			}},
		{"6 lines of 200 characters", "--- FAIL: TestA (0.00s)\n    a_test.go:1: one\n    a_test.go:2: two\n    a_test.go:3: three\n    a_test.go:4: four\n" + long + "\n    a_test.go:6: six\nFAIL\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:1: one", "    a_test.go:2: two", "    a_test.go:3: three", "    a_test.go:4: four", long[:200])}},
		{"20 blocks", strings.Join(manyIn, "\n") + "\nFAIL\n", manyBlocks},
		// go prints a test's panic after its FAIL line, unindented, then a
		// stack: the panic's line joins every block open at that point.
		{"a panic", "--- FAIL: TestA (0.00s)\n    --- FAIL: TestA/sub (0.00s)\npanic: boom [recovered]\n\tpanic: boom\n\ngoroutine 7 [running]:\ntesting.tRunner.func1.2({0x1, 0x2})\n\ttesting/testing.go:1631 +0x1c4\nFAIL\tm.test/a 0.1s\n",
			[]string{
				block("--- FAIL: TestA (0.00s)", "    --- FAIL: TestA/sub (0.00s)", "panic: boom [recovered]"),
				block("--- FAIL: TestA/sub (0.00s)", "panic: boom [recovered]"),
			}},
		{"backslashes and tabs as printed", "--- FAIL: TestA (0.00s)\n    a_test.go:12: got \"a\\nb\", want \"a\\tb\" in C:\\dir\\c\tend\nFAIL\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:12: got \"a\\nb\", want \"a\\tb\" in C:\\dir\\c\tend")}},
		{"trailing spaces and carriage returns", "--- FAIL: TestA (0.00s)\r\n    a_test.go:1: one  \r\nFAIL\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:1: one")}},
		// An escape sequence, a bell, a carriage return inside a line and a
		// delete: none is kept, the rest of the line is.
		{"control characters", "--- FAIL: TestA (0.00s)\n    a_test.go:1: red \x1b[31mbold\x1b[0m bell\a cr\rmid del\x7f end\tstop\nFAIL\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:1: red [31mbold[0m bell crmid del end\tstop")}},
		// A FAIL marker inside a message is the message's text, no report.
		{"a marker inside a message", "--- FAIL: TestA (0.00s)\n    a_test.go:3: output was --- FAIL: TestInner (0.00s)\nFAIL\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:3: output was --- FAIL: TestInner (0.00s)")}},
		{"a test failing in two packages", "--- FAIL: TestA (0.00s)\n    a_test.go:1: in one\nFAIL\nFAIL\tm.test/one 0.1s\n--- FAIL: TestA (0.00s)\n    a_test.go:1: in two\nFAIL\nFAIL\tm.test/two 0.1s\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:1: in one"), block("--- FAIL: TestA (0.00s)", "    a_test.go:1: in two")}},
		// A failure with no `--- FAIL:` line keeps one block of what the
		// log says instead: a timeout's panic line and running entries
		// (not the dump behind the blank line), a fatal error's or
		// panic's line and the dump's head, else the first output lines.
		{"a timeout keeps its panic line and running tests", "ok  \tm.test/a\t0.1s\npanic: test timed out after 10m0s\n\trunning tests:\n\t\tTestA (10m0s)\n\t\tTestA/sub (10m0s)\n\ngoroutine 5 [running]:\ntesting.(*M).startAlarm.func1()\n\t/go/src/testing/testing.go:2959 +0x2c4\nFAIL\tm.test/b 600.1s\n",
			[]string{block("panic: test timed out after 10m0s", "\t\tTestA (10m0s)", "\t\tTestA/sub (10m0s)")}},
		{"a timeout keeps the panic line and five entries", "panic: test timed out after 10m0s\n\trunning tests:\n\t\tTestT00 (10m0s)\n\t\tTestT01 (10m0s)\n\t\tTestT02 (10m0s)\n\t\tTestT03 (10m0s)\n\t\tTestT04 (10m0s)\n\t\tTestT05 (10m0s)\n\t\tTestT06 (10m0s)\n\nFAIL\tm.test/b 600.1s\n",
			[]string{block("panic: test timed out after 10m0s", "\t\tTestT00 (10m0s)", "\t\tTestT01 (10m0s)", "\t\tTestT02 (10m0s)", "\t\tTestT03 (10m0s)", "\t\tTestT04 (10m0s)")}},
		{"a fatal error keeps its line and the dump head", "=== RUN   TestA\n--- PASS: TestA (0.00s)\nfatal error: concurrent map writes\n\ngoroutine 8 [running]:\ninternal/runtime/maps.fatal({0x1?, 0x0?})\n\t/go/src/runtime/panic.go:1195 +0x20\nm.test/a.TestA.func1()\n\ta_test.go:16 +0x60\ncreated by m.test/a.TestA in goroutine 4\n\ta_test.go:13 +0x48\nFAIL\tm.test/a 0.1s\n",
			[]string{block("fatal error: concurrent map writes", "goroutine 8 [running]:", "internal/runtime/maps.fatal({0x1?, 0x0?})", "\t/go/src/runtime/panic.go:1195 +0x20", "m.test/a.TestA.func1()", "\ta_test.go:16 +0x60")}},
		{"a panic in a code goroutine keeps its line and the dump head", "panic: boom from a code goroutine\n\ngoroutine 5 [running]:\nm.test/a.TestA.func1()\n\ta_test.go:11 +0x38\ncreated by m.test/a.TestA in goroutine 4\n\ta_test.go:9 +0x24\nFAIL\tm.test/a 0.1s\n",
			[]string{block("panic: boom from a code goroutine", "goroutine 5 [running]:", "m.test/a.TestA.func1()", "\ta_test.go:11 +0x38", "created by m.test/a.TestA in goroutine 4", "\ta_test.go:9 +0x24")}},
		{"a TestMain that exits keeps its last lines", "about to die\nFAIL\tm.test/a 0.1s\nFAIL\n",
			[]string{"about to die"}},
		{"a silent TestMain keeps the exit status", "exit status 3\nFAIL\tm.test/a 0.1s\nFAIL\n",
			[]string{"exit status 3"}},
		// A failure report wins over a timeout behind it; a timeout wins
		// over a fatal error in another package behind it.
		{"a failure report wins over a timeout behind it", "--- FAIL: TestA (0.00s)\n    a_test.go:10: got 1, want 2\nFAIL\nFAIL\tm.test/a 0.1s\npanic: test timed out after 10m0s\n\trunning tests:\n\t\tTestB (10m0s)\n\nFAIL\tm.test/b 600.1s\n",
			[]string{block("--- FAIL: TestA (0.00s)", "    a_test.go:10: got 1, want 2")}},
		{"a timeout wins over a fatal error behind it", "panic: test timed out after 10m0s\n\trunning tests:\n\t\tTestA (10m0s)\n\nFAIL\tm.test/a 600.1s\nfatal error: concurrent map writes\n\ngoroutine 8 [running]:\nFAIL\tm.test/b 0.1s\n",
			[]string{block("panic: test timed out after 10m0s", "\t\tTestA (10m0s)")}},
		{"a log of summaries only keeps nothing", "ok  \tm.test/a\t0.1s\nFAIL\nFAIL\tm.test/b 600.1s\n", nil},
		{"an empty log", "", nil},
	} {
		t.Run("keeps: "+c.name, func(t *testing.T) {
			if got, want := run(t, c.in, "flakyfails"), strings.Join(c.want, sep); got != want {
				t.Errorf("flakyfails printed:\n%s\nwant:\n%s", got, want)
			}
		})
	}

	// The tests a failure names without a `--- FAIL:` line: the running
	// tests of a timeout, else the crashing test of a fatal error or a
	// panic in a code goroutine. The crashing test is the first frame of
	// the first goroutine outside testing and the runtime, with the args,
	// the package path and a closure suffix stripped; a created-by line
	// and a second goroutine name nothing.
	var stuckManyIn, stuckManyWant []string
	for i := range 25 {
		stuckManyIn = append(stuckManyIn, fmt.Sprintf("\t\tTestT%02d (10m0s)", i))
		if i < 20 {
			stuckManyWant = append(stuckManyWant, fmt.Sprintf("TestT%02d", i))
		}
	}
	for _, c := range []struct {
		name string
		in   string
		want []string // the names, in the order the log lists them
	}{
		{"a timeout names its running tests", "panic: test timed out after 10m0s\n\trunning tests:\n\t\tTestA (10m0s)\n\t\tTestA/sub (10m0s)\n\nFAIL\tm.test/b 600.1s\n",
			[]string{"TestA", "TestA/sub"}},
		{"a timeout names twenty running tests", "panic: test timed out after 10m0s\n\trunning tests:\n" + strings.Join(stuckManyIn, "\n") + "\n\nFAIL\tm.test/b 600.1s\n",
			stuckManyWant},
		{"a fatal error names the test past the runtime frames", "fatal error: concurrent map writes\n\ngoroutine 8 [running]:\ninternal/runtime/maps.fatal({0x1?, 0x0?})\n\t/go/src/runtime/panic.go:1195 +0x20\nm.test/a.TestA.func1()\n\ta_test.go:16 +0x60\ncreated by m.test/a.TestA in goroutine 4\n\ta_test.go:13 +0x48\nFAIL\tm.test/a 0.1s\n",
			[]string{"TestA"}},
		{"a panic in a code goroutine names the test", "panic: boom\n\ngoroutine 5 [running]:\nm.test/a.TestA()\n\ta_test.go:11 +0x38\nFAIL\tm.test/a 0.1s\n",
			[]string{"TestA"}},
		{"a nested closure panic names its test", "panic: boom\n\ngoroutine 5 [running]:\nm.test/a.TestA.func1.1()\n\ta_test.go:11 +0x38\nFAIL\tm.test/a 0.1s\n",
			[]string{"TestA"}},
		// Elided frames inside the stack, and a testing frame above the
		// crash, are passed over; only the first goroutine is read.
		{"testing frames and elided frames are passed over", "panic: boom\n\ngoroutine 5 [running]:\nruntime.throw({0x1?, 0x0?})\n\t/go/src/runtime/panic.go:1077 +0x5d\ntesting.tRunner()\n\t/go/src/testing/testing.go:2258 +0x3b8\n...additional frames elided...\nm.test/a.TestA.func2()\n\ta_test.go:11 +0x38\n\nFAIL\tm.test/a 0.1s\n",
			[]string{"TestA"}},
		{"a second goroutine names nothing", "fatal error: concurrent map writes\n\ngoroutine 8 [running]:\nruntime.throw({0x1?, 0x0?})\n\t/go/src/runtime/panic.go:1077 +0x5d\ncreated by testing.(*T).Run in goroutine 1\n\t/go/src/testing/testing.go:2280 +0x3b8\n\ngoroutine 1 [chan receive]:\nm.test/a.TestA()\n\ta_test.go:11 +0x38\nFAIL\tm.test/a 0.1s\n",
			nil},
		{"runtime frames alone name nothing", "fatal error: all goroutines are asleep - deadlock!\n\ngoroutine 1 [chan receive]:\nruntime.gopark()\n\t/go/src/runtime/proc.go:424 +0x12c\nFAIL\tm.test/a 0.1s\n",
			nil},
		{"a TestMain that exits names nothing", "about to die\nFAIL\tm.test/a 0.1s\nFAIL\n", nil},
		{"a timeout wins over a fatal error behind it", "panic: test timed out after 10m0s\n\trunning tests:\n\t\tTestA (10m0s)\n\nFAIL\tm.test/a 600.1s\nfatal error: concurrent map writes\n\ngoroutine 8 [running]:\nm.test/b.TestB()\n\tb_test.go:16 +0x60\nFAIL\tm.test/b 0.1s\n",
			[]string{"TestA"}},
		{"an empty log", "", nil},
	} {
		t.Run("stuck: "+c.name, func(t *testing.T) {
			if got, want := run(t, c.in, "flakystuck"), strings.Join(c.want, "\n"); got != want {
				t.Errorf("flakystuck printed:\n%s\nwant:\n%s", got, want)
			}
		})
	}

	// The lines of a log that name a test: its name between spaces before
	// the separator. A line without a separator is all head.
	const log = "2026-10-20 10:00:00 abc1234 TestA TestA/sub (m.test/a) passed on rerun\n" +
		"2026-10-20 10:05:00 abc1234 TestAB (m.test/a) passed on rerun\n" +
		"2026-10-20 10:10:00 abc1234 TestC (m.test/c) passed on rerun | --- FAIL: TestC (0.00s)" + eol + "    c_test.go:1: waiting for TestA to start\n" +
		"2026-10-20 10:15:00 abc1234 TestA (m.test/a) passed on rerun | --- FAIL: TestA (0.00s)" + eol + "    a_test.go:2: boom\n" +
		"2026-10-20 10:20:00 abc1234 m.test/z passed on rerun\n" +
		"2026-10-20 10:25:00 abc1234 TestX/a\\b (m.test/x) passed on rerun\n" +
		"2026-10-20 10:30:00 abc1234 TestG* (m.test/g) passed on rerun\n"
	logLines := strings.Split(strings.TrimSuffix(log, "\n"), "\n")
	for _, c := range []struct {
		name, test string
		want       []int // the lines of log, by index
	}{
		{"a name in a head", "TestA", []int{0, 3}},
		{"a subtest in a head", "TestA/sub", []int{0}},
		{"a longer name is another test", "TestAB", []int{1}},
		{"a name only in another test's kept lines", "TestC", []int{2}},
		{"a name with a backslash is data", `TestX/a\b`, []int{5}},
		{"a name with a glob character is data", "TestG*", []int{6}},
		{"a pattern is no name", "Test*", nil},
		{"a name the log lacks", "TestZ", nil},
	} {
		t.Run("names: "+c.name, func(t *testing.T) {
			var want []string
			for _, i := range c.want {
				want = append(want, logLines[i])
			}
			if got := run(t, log, "flakylines", c.test); got != strings.Join(want, "\n") {
				t.Errorf("flakylines %s printed:\n%s\nwant:\n%s", c.test, got, strings.Join(want, "\n"))
			}
		})
	}

	// The beads a run says it tested, for the field its log line ends its head
	// with: its bead, or the ones a train's integration names instead of the
	// train, or none. Only the characters an id and a list of them are made of
	// are kept.
	for _, c := range []struct {
		name string
		env  []string
		want string
	}{
		{"a bead", []string{"YARDR_BEAD=m-1"}, "m-1"},
		{"the beads a train's integration names win over the train", []string{"YARDR_BEAD=m-t", "YARDR_TEST_BEADS=m-1,m-2"}, "m-1,m-2"},
		{"set but empty names none, not the train", []string{"YARDR_BEAD=m-t", "YARDR_TEST_BEADS="}, ""},
		{"no bead", nil, ""},
		{"an empty bead", []string{"YARDR_BEAD="}, ""},
		{"only the characters of an id and a list", []string{"YARDR_BEAD=a_b.c,d-e f\tg;h\n$(i)`j`|k*l"}, "a_b.c,d-efghijkl"},
		{"nothing left once cleaned", []string{"YARDR_BEAD= ;|\n"}, ""},
	} {
		t.Run("beads: "+c.name, func(t *testing.T) {
			cmd := exec.Command("sh", probe, "flakybeads")
			cmd.Env = append(slices.DeleteFunc(scriptEnv(), func(kv string) bool { return strings.HasPrefix(kv, "YARDR_BEAD=") }), c.env...)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil || stderr.Len() > 0 {
				t.Fatalf("flakybeads: %v\n%s", err, stderr.String())
			}
			if got := string(out); got != c.want {
				t.Errorf("flakybeads printed %q with %q, want %q", got, c.env, c.want)
			}
		})
	}

	// In how many changes a test flaked in the last 7 days. The lines are
	// aged from now, dated as the gate dates them (local time), a day or more
	// from the 7-day mark: a change of clock moves that mark by an hour.
	const day = 24 * time.Hour
	const shaA, shaB, shaC, shaD = "a1b2c3d4", "e5f6a7b8", "c9d0e1f2", "0a1b2c3d"
	const headA = "TestA (m.test/a) passed on rerun"
	at := func(age time.Duration, sha, head string) string {
		return time.Now().Add(-age).Format("2006-01-02 15:04:05") + " " + sha + " " + head
	}
	// The real log's lines of 23 Oct 2026 for one test: a landing of one bead
	// at ed8c93f1, the builder of another at b86a8b1f twice, and that bead's
	// landing at af4f88bd, the landing's rewrite of the builder's commit. Four
	// lines, three shas, two changes; the kept lines behind the separator are
	// no part of the head.
	const watch = "TestWatchdogTakesTheYardDownWhenItsOwnerIsGone"
	const watchTail = " (yardr.dev/yardr/contrib/isolated-yard) passed on rerun"
	kept := sep + "--- FAIL: " + watch + " (0.00s)" + eol + "    watchdog_test.go:51: the yard is still up"
	replay := []string{
		at(4*time.Hour, "ed8c93f1", "TestWatchdogOfAYardMadeForAGoneOwnerTakesItDown "+watch+watchTail+" beads=yardr-q4bk"+kept),
		at(3*time.Hour, "b86a8b1f", watch+watchTail+" beads=yardr-myez"+kept),
		at(2*time.Hour, "b86a8b1f", "TestServerYardWithoutTheHarnessLogin TestWatchdogOfAYardMadeForAGoneOwnerTakesItDown "+watch+watchTail+" beads=yardr-myez"),
		at(time.Hour, "af4f88bd", watch+watchTail+" beads=yardr-myez"+kept),
	}
	for _, c := range []struct {
		name  string
		lines []string
		test  string
		want  string
	}{
		{"the real log: a builder and its landing at another commit are one change", replay, watch, "2"},
		{"the real log with a third change", append(slices.Clone(replay), at(30*time.Minute, "5c6d7e8f", watch+watchTail+" beads=yardr-k3dy")), watch, "3"},
		{"one commit on many lines", []string{at(time.Hour, shaA, headA), at(2*time.Hour, shaA, headA), at(3*day, shaA, headA)}, "TestA", "1"},
		{"three commits", []string{at(day, shaA, headA), at(2*day, shaB, headA), at(3*day, shaC, headA)}, "TestA", "3"},
		{"a commit on two lines among others", []string{at(day, shaA, headA), at(2*day, shaA, headA), at(3*day, shaB, headA), at(4*day, shaC, headA)}, "TestA", "3"},
		{"only the last 7 days", []string{at(9*day, shaD, headA), at(8*day, shaC, headA), at(2*day, shaB, headA), at(day, shaA, headA)}, "TestA", "2"},
		// A line before the window is no sighting of its commit, so it does
		// not take the commit from a later line inside it.
		{"a commit before the window and in it", []string{at(8*day, shaA, headA), at(day, shaA, headA)}, "TestA", "1"},
		{"a name only in another test's kept lines", []string{
			at(day, shaA, "TestC (m.test/a) passed on rerun"+sep+"--- FAIL: TestC (0.00s)"+eol+"    c_test.go:1: waiting for TestA to start"),
			at(2*day, shaB, headA)}, "TestA", "1"},
		{"a longer name is another test", []string{at(day, shaA, "TestAB (m.test/a) passed on rerun"), at(2*day, shaB, headA)}, "TestA", "1"},
		// What the gate writes when git cannot name HEAD: the sha is empty.
		{"lines with no sha count once", []string{at(day, "", headA), at(2*day, "", headA), at(3*day, shaA, headA)}, "TestA", "2"},
		{"a line with no date is skipped", []string{"x TestA flaked on some day", at(day, shaA, headA)}, "TestA", "1"},
		{"a test the log lacks", []string{at(day, shaA, headA)}, "TestZ", "0"},
		{"an empty log", nil, "TestA", "0"},
		// A line with the field is a change of its beads, whatever commit it
		// ran at; a set is the same set in any order and counted once each,
		// and a train's set is one change apart from each of its children.
		{"the same beads at other commits", []string{at(day, shaA, headA+" beads=m-1"), at(2*day, shaB, headA+" beads=m-1"), at(3*day, shaC, headA+" beads=m-1")}, "TestA", "1"},
		{"a set in any order, and a bead twice", []string{at(day, shaA, headA+" beads=m-1,m-2"), at(2*day, shaB, headA+" beads=m-2,m-1,m-2")}, "TestA", "1"},
		{"a train and one of its children", []string{at(day, shaA, headA+" beads=m-1,m-2,m-3"), at(2*day, shaB, headA+" beads=m-1")}, "TestA", "2"},
		{"a line with no sha and a field", []string{at(day, "", headA+" beads=m-1"), at(2*day, shaA, headA+" beads=m-1")}, "TestA", "1"},
		// Old lines and new ones, in the 7 days after the field began, are
		// counted apart: a sha and a bead are not the same key.
		{"a line with the field and one without", []string{at(day, shaA, headA), at(2*day, shaA, headA+" beads=m-1")}, "TestA", "2"},
		{"a bead named like a sha is no sha", []string{at(day, shaA, headA), at(2*day, shaB, headA+" beads="+shaA)}, "TestA", "2"},
		{"a change before the window does not count", []string{at(8*day, shaA, headA+" beads=m-1"), at(day, shaB, headA+" beads=m-2")}, "TestA", "1"},
		// The head ends with the field; text behind the separator is the
		// first run's, and may say anything, even beads=.
		{"the field is read from the head, not from the kept lines", []string{
			at(day, shaA, headA+sep+"--- FAIL: TestA (0.00s)"+eol+"    a_test.go:1: want beads=m-8"),
			at(2*day, shaA, headA+sep+"--- FAIL: TestA (0.00s)"+eol+"    a_test.go:1: want beads=m-9")}, "TestA", "1"},
	} {
		t.Run("changes: "+c.name, func(t *testing.T) {
			in := strings.Join(c.lines, "\n")
			if in != "" {
				in += "\n"
			}
			if got := run(t, in, "flakychanges", c.test); got != c.want {
				t.Errorf("flakychanges %s printed %q, want %q for:\n%s", c.test, got, c.want, in)
			}
		})
	}

	// The open beads as `yardr bead list` prints them: a header, and a line a
	// bead, its title the last column, after a train that is blank for most.
	const pkg = "m.test/m"
	const header = "ID                 TRAIN  PRI  TYPE    STAGE    STATUS  GROUP           DEPOT         TITLE"
	bead := func(id, title string) string {
		return fmt.Sprintf("%-18s %-6s %-4s %-7s %-8s %-7s %-15s %-13s %s", id, "", "P3", "task", "backlog", "open", "-", "yardr", title)
	}
	flaky := func(id, name, pkgs string) string { return bead(id, "Flaky: "+name+" ("+pkgs+")") }
	other := bead("m-1", "Something else")
	listing := func(lines []string) string { return header + "\n" + strings.Join(lines, "\n") + "\n" }

	// The bead that stands for a test, from the listing on stdin: the test's
	// own, else its top-level test's, else any other subtest's of that one,
	// the first of a kind in the order the listing prints them, and only of
	// the packages the test's title names. Names are data, not patterns.
	for _, c := range []struct {
		name, test, pkgs string
		list             []string
		want             string // the id printed: empty for none
	}{
		{"the test's own bead", "TestX", pkg, []string{other, flaky("m-2", "TestX", pkg)}, "m-2"},
		{"a subtest finds its parent's bead", "TestX/a", pkg, []string{other, flaky("m-2", "TestX", pkg)}, "m-2"},
		{"a subtest finds a sibling's bead", "TestX/a", pkg, []string{other, flaky("m-2", "TestX/b", pkg)}, "m-2"},
		{"a parent finds a subtest's bead", "TestX", pkg, []string{other, flaky("m-2", "TestX/b", pkg)}, "m-2"},
		{"a deeper subtest finds a bead of its path", "TestX/a/deep", pkg, []string{flaky("m-2", "TestX/a", pkg)}, "m-2"},
		{"its own bead before its parent's and a sibling's", "TestX/a", pkg,
			[]string{flaky("m-1", "TestX/b", pkg), flaky("m-2", "TestX", pkg), flaky("m-3", "TestX/a", pkg)}, "m-3"},
		{"its parent's bead before a sibling's", "TestX/a", pkg,
			[]string{flaky("m-1", "TestX/b", pkg), flaky("m-2", "TestX", pkg)}, "m-2"},
		{"the parent's before a deeper sibling's", "TestX/a/deep", pkg,
			[]string{flaky("m-1", "TestX/a", pkg), flaky("m-2", "TestX", pkg)}, "m-2"},
		{"the first of a kind in the listing", "TestX/a", pkg,
			[]string{flaky("m-1", "TestX/c", pkg), flaky("m-2", "TestX/b", pkg)}, "m-1"},
		{"another package", "TestX", pkg, []string{flaky("m-2", "TestX", "m.test/other")}, ""},
		{"a title with two packages is the same two", "TestX", "m.test/a m.test/b", []string{flaky("m-2", "TestX", "m.test/a m.test/b")}, "m-2"},
		{"two packages are not one", "TestX", "m.test/a m.test/b", []string{flaky("m-2", "TestX", "m.test/a")}, ""},
		{"one package is not two", "TestX", "m.test/a", []string{flaky("m-2", "TestX", "m.test/a m.test/b")}, ""},
		{"a longer name is another test", "TestX", pkg, []string{flaky("m-2", "TestXY", pkg), flaky("m-3", "TestX2/a", pkg)}, ""},
		{"a shorter name is another test", "TestXY", pkg, []string{flaky("m-2", "TestX", pkg), flaky("m-3", "TestX/a", pkg)}, ""},
		{"a name with a glob character is data", "TestG*", pkg, []string{flaky("m-2", "TestGa", pkg), flaky("m-3", "TestG*", pkg)}, "m-3"},
		{"a pattern is no name", "Test*", pkg, []string{flaky("m-2", "TestA", pkg)}, ""},
		// A sibling's bead is listed first: a name read as an escape sequence
		// finds that one, not its own.
		{"a name with a backslash is data", `TestX/a\b`, pkg, []string{flaky("m-1", "TestX/other", pkg), flaky("m-2", `TestX/a\b`, pkg)}, "m-2"},
		{"a title that is no test's", "TestX", pkg, []string{bead("m-2", "Flaky: the push tests ("+pkg+")")}, ""},
		{"Flaky text elsewhere in a title is no Flaky bead", "TestX", pkg, []string{bead("m-2", "Something Flaky: TestX ("+pkg+")")}, ""},
		{"a Flaky title with a suffix is not exact", "TestX", pkg, []string{flaky("m-2", "TestX", pkg) + " old"}, ""},
		// A title with a line break in it makes a line of the listing that
		// starts anywhere: it is no bead's id when it could be an option.
		{"a first field that is no id", "TestX", pkg, []string{"--help " + "Flaky: TestX (" + pkg + ")", flaky("m-2", "TestX/a", pkg)}, "m-2"},
		{"an id with dots and hyphens", "TestX", pkg, []string{flaky("yardr.dev-q1m7", "TestX", pkg)}, "yardr.dev-q1m7"},
		{"a title whose packages do not follow the name", "TestX", pkg, []string{bead("m-2", "Flaky: TestX see ("+pkg+")")}, ""},
		{"no Flaky bead", "TestX", pkg, []string{other}, ""},
		{"no bead", "TestX", pkg, nil, ""},
	} {
		t.Run("opens: "+c.name, func(t *testing.T) {
			cmd := exec.Command("sh", probe, "flakyopen", c.test, c.pkgs)
			cmd.Stdin = strings.NewReader(listing(c.list))
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil || stderr.Len() > 0 {
				t.Fatalf("flakyopen: %v\n%s", err, stderr.String())
			}
			if got := strings.TrimSuffix(string(out), "\n"); got != c.want {
				t.Errorf("flakyopen %s printed %q, want %q for:\n%s", c.test, got, c.want, listing(c.list))
			}
		})
	}

	// What a repeat does with it (flakyfile), against a stub yardr that writes
	// its calls down. The stub is written once, since the first run of a new
	// executable costs a scan of its own. With a bead open that stands for the
	// test, the run's line is noted on it, whole. With none, the bead is filed
	// with the test's lines of the log. A yard that refuses a call is warned
	// of and the step goes on, so the function runs under sh -e, as the step
	// does, and must return 0.
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "yardr"), []byte(fakeYardr), 0o755); err != nil {
		t.Fatal(err)
	}
	const line = "2026-10-23 12:00:00 abc1234 TestX TestX/a (m.test/m) passed on rerun | --- FAIL: TestX (0.00s)" + eol + "    x_test.go:3: boom"
	const flakyLog = "2026-10-22 12:00:00 abc1234 TestX (m.test/m) passed on rerun\n" + line + "\n"
	const create = "bead create Flaky: TestX (m.test/m) --stage backlog -p 3 -b -"
	const filed = "test: filed m-filed: Flaky: TestX (m.test/m)\n"
	for _, c := range []struct {
		name, test string
		list       []string
		refuse     string // the call the stub refuses
		depot      string
		calls      []string // what the stub was run as, in order
		note       string   // what it kept as a note: the bead, a space, the text
		body       string   // what it kept as a filing's body
		out, warn  string   // stdout and stderr
	}{
		{name: "an open bead for the test is noted with the run's line", test: "TestX", list: []string{other, flaky("m-2", "TestX", pkg)},
			calls: []string{"bead list", "bead note m-2 -"}, note: "m-2 " + line, out: "test: noted m-2: Flaky: TestX (m.test/m)\n"},
		{name: "a subtest is noted on its parent's bead", test: "TestX/a", list: []string{other, flaky("m-2", "TestX", pkg)},
			calls: []string{"bead list", "bead note m-2 -"}, note: "m-2 " + line, out: "test: noted m-2: Flaky: TestX/a (m.test/m)\n"},
		{name: "a subtest is noted on a sibling's bead", test: "TestX/a", list: []string{other, flaky("m-2", "TestX/b", pkg)},
			calls: []string{"bead list", "bead note m-2 -"}, note: "m-2 " + line, out: "test: noted m-2: Flaky: TestX/a (m.test/m)\n"},
		{name: "with none open the test's lines of the log are filed", test: "TestX", list: []string{other},
			calls: []string{"bead list", create}, body: flakyLog, out: filed},
		{name: "a bead of another package is no bead for the test", test: "TestX", list: []string{other, flaky("m-2", "TestX", "m.test/other")},
			calls: []string{"bead list", create}, body: flakyLog, out: filed},
		{name: "a filing names the depot", test: "TestX", depot: "m", list: []string{other},
			calls: []string{"bead list", "bead create Flaky: TestX (m.test/m) --depot m --stage backlog -p 3 -b -"}, body: flakyLog, out: filed},
		{name: "a refused list warns and files nothing", test: "TestX", list: []string{flaky("m-2", "TestX", pkg)}, refuse: "bead list",
			calls: []string{"bead list"}, warn: "test: warning: cannot list beads to file the repeat flake TestX: the yard is down\n"},
		{name: "a refused note warns and files nothing", test: "TestX", list: []string{flaky("m-2", "TestX", pkg)}, refuse: "bead note",
			calls: []string{"bead list", "bead note m-2 -"}, warn: "test: warning: cannot note the repeat flake TestX on m-2: the yard is down\n"},
		{name: "a refused filing warns", test: "TestX", list: []string{other}, refuse: "bead create",
			calls: []string{"bead list", create}, warn: "test: warning: cannot file the repeat flake TestX: the yard is down\n"},
	} {
		t.Run("repeat: "+c.name, func(t *testing.T) {
			fake := t.TempDir()
			logf := filepath.Join(fake, "flaky.log")
			if err := os.WriteFile(logf, []byte(flakyLog), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-e", probe, "flakyfile", c.test, pkg, logf, c.depot, line)
			// Only the stub and the system on PATH: no real yardr to reach.
			cmd.Env = []string{"PATH=" + stub + ":/usr/bin:/bin", "FAKE=" + fake, "YARDR_FAKE_LIST=" + listing(c.list), "YARDR_FAKE_REFUSE=" + c.refuse}
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Errorf("flakyfile returned %v, which fails the step\n%s", err, stderr.String())
			}
			if stdout.String() != c.out || stderr.String() != c.warn {
				t.Errorf("flakyfile printed %q and warned %q, want %q and %q", stdout.String(), stderr.String(), c.out, c.warn)
			}
			read := func(name string) string {
				b, _ := os.ReadFile(filepath.Join(fake, name))
				return string(b)
			}
			if got, want := strings.TrimSpace(read("yardr-calls")), strings.Join(c.calls, "\n"); got != want {
				t.Errorf("yardr was run as:\n%s\nwant:\n%s", got, want)
			}
			if got := strings.TrimSuffix(read("yardr-notes"), "\n"); got != c.note {
				t.Errorf("the note kept is %q, want %q", got, c.note)
			}
			if got := read("yardr-body"); got != c.body {
				t.Errorf("the filing's body is %q, want %q", got, c.body)
			}
		})
	}
}

// The run ends with its own test line (docs/testing.md R4), the line a note
// carries whole: the result, the packages passed and failed, the wall
// seconds, and the seconds queued on the race lock, at the short sha. A
// package that passed on rerun counts as passed; a plan and a run that
// could not start print none. Every row runs the gate in its own fixture,
// so the rows run in parallel.
func TestYardTestPrintsItsTestLine(t *testing.T) {
	t.Parallel()
	const flaky = "test: flaky: TestX (m.test/m/a) passed on rerun"
	line := regexp.MustCompile(`^test: sh \.yardr/test @ ([0-9a-f]+): (pass|fail), ([0-9]+) packages ok, ([0-9]+) failed, ([0-9]+) s, waited ([0-9]+) s$`)
	// One template repository for every row: each copies it, changes go.mod
	// and runs, so no row commits the same module again.
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	template, base, short := goModTemplate(t)
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
			t.Parallel()
			fake := t.TempDir()
			dir := copyFixture(t, template)
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
			if err := os.WriteFile(mod, []byte("module m.test/m\n\ngo 1.21\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", string(script))
			cmd.Dir = dir
			// The case's own variables come last, so a landing's nowhere
			// base wins over the commit below.
			env := scriptEnv()
			cmd.Env = append(env,
				"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
				"YARDR_BASE="+base,
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

// A gate ended by a signal prints no `pass`: its test line agrees with the
// non-zero status it ends with. The run below selects one package and is
// TERM'd in its own process group once its go test step starts: it must exit
// non-zero with no `: pass,` line. Where the shell runs the EXIT trap (bash)
// the line says `fail`; where it runs none on signal death (dash) there is
// no line at all, so a test line is only required to say `fail` when one is
// printed. Cleanup on signal death is the EXIT trap's, which dash skips, so
// unlike its neighbours this test asserts nothing is left behind. Each row
// signals its own process group, so the two run in parallel.
func TestYardTestKilledGatePrintsNoPass(t *testing.T) {
	t.Parallel()
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	_, rows := yardTestRows(t)
	// The selection answers the gate's go list with the changed package
	// and every package the reads table names, so no row fails the run
	// for naming what is no package here.
	seen := map[string]bool{".": true, "a": true}
	dirs := []string{".", "a"}
	for _, row := range rows {
		for _, pkg := range row.pkgs {
			if !seen[pkg] {
				seen[pkg] = true
				dirs = append(dirs, pkg)
			}
		}
	}
	var list strings.Builder
	for _, d := range dirs {
		dir, imp := "$(pwd)/"+d, "m.test/m/"+d
		if d == "." {
			dir, imp = "$(pwd)", "m.test/m"
		}
		fmt.Fprintf(&list, "  printf '%%s\\n' \"%s|%s|||\"\n", imp, dir)
	}
	for _, c := range []struct {
		name        string
		groupSignal bool
		wantCode    int
	}{
		{"TERM", true, -1},
		{"INT", false, 130},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			// The fake go answers the selection's list. For TERM it holds
			// its test step at a marker; for INT it signals the gate shell
			// and exits so the shell handles INT after the command returns.
			goFake := "#!/bin/sh\n" +
				"if [ \"$1\" = list ]; then\n" + list.String() +
				"elif [ \"$1\" = test ]; then\n" +
				"  : >\"$FAKE/started\"\n" +
				"  if [ \"$SIGNAL\" = INT ]; then kill -INT \"$(cat \"$FAKE/gate.pid\")\"; else sleep 30; fi\n" +
				"fi\n"
			fake, dir := t.TempDir(), t.TempDir()
			for name, text := range map[string]string{"go": goFake, "gofmt": "#!/bin/sh\n"} {
				if err := os.WriteFile(filepath.Join(fake, name), []byte(text), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tmp := filepath.Join(fake, "tmp")
			if err := os.Mkdir(tmp, 0o755); err != nil {
				t.Fatal(err)
			}
			// A branch that changed one package's .go file: a plain run tests
			// that package, and asks go for the list of them.
			if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m.test/m\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			a := filepath.Join(dir, "a", "a.go")
			if err := os.WriteFile(a, []byte("package a\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitT(t, dir, "init", "-q", "-b", "main")
			gitT(t, dir, "add", "-A")
			gitT(t, dir, "commit", "-q", "-m", "base")
			if err := os.WriteFile(a, []byte("package a\n\n// touched\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", "echo $$ > \"$FAKE/gate.pid\"\n"+string(script))
			cmd.Dir = dir
			// Its own process group, so the TERM below reaches the script and
			// the go test it holds, and nothing else.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Env = append(scriptEnv(),
				"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
				"YARDR_BASE="+gitT(t, dir, "rev-parse", "HEAD"),
				"TMPDIR="+tmp, "FAKE="+fake, "SIGNAL="+c.name, "YARDR_RACE_HELD=1")
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			pgid := cmd.Process.Pid
			done := make(chan struct{})
			var runErr error
			go func() {
				runErr = cmd.Wait()
				close(done)
			}()
			// Once the test step starts, signal the gate as a tree kill
			// would, or let the fake go send INT to the waiting shell.
			marker := filepath.Join(fake, "started")
			deadline := time.Now().Add(60 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				select {
				case <-done:
					t.Fatalf("the run exited before its test step, output:\n%s%s", stdout.String(), stderr.String())
				default:
				}
				if time.Now().After(deadline) {
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
					<-done
					t.Fatalf("the run never started its test step, output:\n%s%s", stdout.String(), stderr.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if c.groupSignal {
				if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
					<-done
					t.Fatalf("TERM the run's process group: %v", err)
				}
			}
			<-done
			out := stdout.String() + stderr.String()
			if runErr == nil {
				t.Errorf("a killed gate exited 0, want non-zero")
			}
			if c.wantCode >= 0 {
				var ee *exec.ExitError
				if !errors.As(runErr, &ee) || ee.ExitCode() != c.wantCode {
					t.Errorf("a killed gate exited %v, want %d", runErr, c.wantCode)
				}
			}
			if strings.Contains(out, ": pass,") {
				t.Errorf("a killed gate printed a pass test line")
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "sh .yardr/test @") && !strings.Contains(line, ": fail,") {
					t.Errorf("a killed gate's test line %q, want fail", line)
				}
			}
			if t.Failed() {
				t.Logf("output:\n%s", out)
			}
		})
	}
}

// The landing's socket leg runs in a background shell. Its package totals
// must retain the same retry-aware meaning as the foreground leg: a package
// that fails once and passes on retry is passed, not both passed and failed.
func TestYardTestCountsSocketRetryByFinalResult(t *testing.T) {
	t.Parallel()
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
// then releases it, and says how long it waited. Only a landing has a race
// leg (docs/testing.md R4); the one these tests run has no comparable base,
// as CI's has not, so it picks no packages from the fake go. The test holds
// the lock past the run's linux vet, its last go step before the race leg, so
// the wait spans whole seconds however slowly the run started under load; its
// own home stands in for the yard's, so no real gate waits on it. Once the
// race leg starts, the lock is the run's until its budget checks end: the
// test cannot take it during the race run or its isolated rerun, and can
// once the leg has ended. The vets run before the lock is held.
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
		// The landing's socket step runs beside the race leg and holds no
		// lock: this go ignores it, so the calls are the race leg's.
		"go": `#!/bin/sh
[ -z "$YARDR_TEST_RPC" ] || exit 0
echo "${GOOS:+$GOOS }$*" >>"$FAKE/calls"
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
	// A landing with no comparable base tests every package, and asks go for
	// no list of them.
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".yardr", "test"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module yardr.dev/yardr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	cmd := exec.Command("sh", ".yardr/test")
	cmd.Dir = dir
	cmd.Env = append(scriptEnv(),
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"YARDR_LANDING=1", "YARDR_BASE=nowhere",
		"HOME="+home, "TMPDIR="+tmp, "FAKE="+fake)
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.CombinedOutput()
		close(done)
	}()
	// The run's linux vet is its last go step before the race leg: hold the
	// lock until the run is queued and its ticket is a second old, measured,
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
	waitFor("linux vet ./...")
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
	if got, want := strings.TrimSpace(string(calls)), "vet ./...\nlinux vet ./...\ntest -race ./...\ntest -race -count=1 yardr.dev/yardr/internal/dispatch"; got != want {
		t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("cannot take the lock after the run ended: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}

// plainLockFixture builds a module the plain-lock tests run the gate
// against: every package the gate's reads table names (pick fails a row
// whose package is gone), four leaf packages nothing imports, and the gate
// script filed as .yardr/test, so a leg that queues re-enters from a path.
// Every package is a leaf, so a change selects the changed set alone. It
// returns the directory, its base commit and its package directories.
func plainLockFixture(t *testing.T, script []byte) (dir, base string, pkgs []string) {
	t.Helper()
	_, rows := yardTestRows(t)
	dir = t.TempDir()
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
	seen := map[string]bool{}
	add := func(pkg string) {
		if !seen[pkg] {
			seen[pkg] = true
			pkgs = append(pkgs, pkg)
		}
		write(pkg+"/p.go", "package p\n")
	}
	for _, row := range rows {
		for _, pkg := range row.pkgs {
			add(pkg)
		}
	}
	for _, leaf := range []string{"a", "b", "c", "d"} {
		add(leaf)
	}
	write("go.mod", "module m.test/m\n\ngo 1.21\n")
	write(".yardr/test", string(script))
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	return dir, gitT(t, dir, "rev-parse", "HEAD"), pkgs
}

// plainLockFake writes the go a plain-lock run drives: the real one for the
// list of packages, and every other call written down. A go test also marks
// its start and end in ORDER (RUN names the run), waiting for RELEASE first,
// and prints the ok lines OKS names, so two runs' legs order themselves in
// one file.
func plainLockFake(t *testing.T, realGo string) (fake string) {
	t.Helper()
	fake = t.TempDir()
	text := "#!/bin/sh\n" +
		"if [ \"$1\" = list ]; then HOME=$REAL_HOME exec \"$REAL_GO\" \"$@\"; fi\n" +
		"echo \"${GOOS:+$GOOS }$*\" >>\"$FAKE/calls\"\n" +
		"if [ \"$1\" = test ]; then\n" +
		"  echo \"start-$RUN\" >>\"$ORDER\"\n" +
		"  n=0\n" +
		"  while [ ! -e \"$RELEASE\" ] && [ \"$n\" -lt 600 ]; do sleep 0.1; n=$((n+1)); done\n" +
		"  echo \"end-$RUN\" >>\"$ORDER\"\n" +
		"  for p in $OKS; do echo \"ok  $p  0.1s\"; done\n" +
		"fi\n"
	if err := os.WriteFile(filepath.Join(fake, "go"), []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fake, "gofmt"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return fake
}

// plainLockTicket waits for a ticket carrying this bead, and returns its
// name: the queue publishes the wait as data.
func plainLockTicket(t *testing.T, cmd *exec.Cmd, qdir, bead string) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if entries, err := os.ReadDir(qdir); err == nil {
			for _, e := range entries {
				if !raceTicketName.MatchString(e.Name()) {
					continue
				}
				if b, _ := os.ReadFile(filepath.Join(qdir, e.Name())); strings.Contains(string(b), "bead="+bead+"\n") {
					return e.Name()
				}
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cmd.Process.Kill()
	t.Fatalf("no ticket for %s in 60 s", bead)
	return ""
}

// plainLockStart starts the gate in dir against base: under ctx, with a
// home and fake go of its own, and the bead for its ticket. The caller
// applies its changes first. The fake go test marks its start and end in
// order, waiting for release first, and prints the ok lines oks names;
// the caller settles release when the leg may pass.
func plainLockStart(t *testing.T, ctx context.Context, dir, base, home, realGo, bead, order, release, oks string, env []string) (cmd *exec.Cmd, fake string, out *os.File) {
	t.Helper()
	fake = plainLockFake(t, realGo)
	out, err := os.CreateTemp("", "wholeplain")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(out.Name()) })
	cmd = exec.CommandContext(ctx, "sh", ".yardr/test")
	cmd.Dir = dir
	cmd.Env = append(scriptEnv(),
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"YARDR_BASE="+base, "YARDR_BEAD="+bead,
		"HOME="+home, "REAL_HOME="+os.Getenv("HOME"),
		"FAKE="+fake, "REAL_GO="+realGo,
		"RUN="+bead, "ORDER="+order, "RELEASE="+release, "OKS="+oks)
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		out.Close()
		t.Fatal(err)
	}
	return cmd, fake, out
}

// plainLockFinish waits for a started gate and returns its output.
func plainLockFinish(cmd *exec.Cmd, out *os.File) (string, error) {
	err := cmd.Wait()
	out.Close()
	b, _ := os.ReadFile(out.Name())
	return string(b), err
}

// plainLockSettle lets a started run's go test pass: the release its fake
// go waits for.
func plainLockSettle(t *testing.T, release string) {
	t.Helper()
	if err := os.WriteFile(release, []byte("go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two whole-suite plain runs serialize on the race lock: the second queues
// behind the first as class 1, waits, then runs. The selection is over
// half the fixture's packages, so the plain step queues; the first run's
// go test waits for the test's release, so the second's wait is real by
// construction and spans whole seconds. Each run has its own directory,
// worktree and bead, and the two share one lock, as two builders' runs do.
func TestYardTestWholePlainRunsSerialize(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go in PATH")
	}
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	order := filepath.Join(t.TempDir(), "order")
	release := filepath.Join(t.TempDir(), "release")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir1, base1, pkgs := plainLockFixture(t, script)
	dir2, base2, _ := plainLockFixture(t, script)
	// Over half the suite's packages, changed in both worktrees: the same
	// step, the same queue. Nothing imports anything, so the selection is
	// the changed set alone.
	need := len(pkgs)/2 + 1
	changed := make([]string, 0, need)
	oks := make([]string, 0, need)
	for _, pkg := range pkgs[:need] {
		changed = append(changed, pkg+"/p.go")
		if pkg == "." {
			oks = append(oks, "m.test/m")
		} else {
			oks = append(oks, "m.test/m/"+pkg)
		}
	}
	change := func(dir string) {
		t.Helper()
		for i, name := range changed {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(fmt.Sprintf("package p\n\nvar Changed%d int\n", i)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitT(t, dir, "add", "-A")
	}
	change(dir1)
	change(dir2)
	cmd1, fake1, out1 := plainLockStart(t, ctx, dir1, base1, home, realGo, "run1", order, release, strings.Join(oks, " "), nil)
	// The first run's go test marks its start once it holds the lock; the
	// second starts only then, so the two legs cannot interleave by luck.
	waitOrder := func(mark string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			if b, _ := os.ReadFile(order); strings.Contains(string(b), mark+"\n") {
				return
			}
			if time.Now().After(deadline) {
				cmd1.Process.Kill()
				b1, _ := os.ReadFile(out1.Name())
				t.Fatalf("the order never holds %s:\n%s", mark, b1)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitOrder("start-run1")
	cmd2, fake2, out2 := plainLockStart(t, ctx, dir2, base2, home, realGo, "run2", order, release, strings.Join(oks, " "), nil)
	qdir := filepath.Join(home, ".yardr", "race.queue")
	ticket := plainLockTicket(t, cmd2, qdir, "run2")
	if !strings.HasPrefix(ticket, "1-") {
		t.Errorf("the second run's ticket is %s, want class 1", ticket)
	}
	// The wait timer starts before the ticket is made, so a release before
	// the first lock attempt leaves a zero wait and no wait line. Hold
	// until the ticket is a second old, measured from its first sight, so
	// the first attempt has run while held and the summary's waited
	// seconds are positive.
	firstSeen := time.Now()
	if wait := time.Until(firstSeen.Add(1100 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	if b, _ := os.ReadFile(order); strings.Contains(string(b), "start-run2\n") {
		t.Errorf("the second run's leg started while the first holds the lock")
	}
	if err := os.WriteFile(release, []byte("go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b1, err1 := plainLockFinish(cmd1, out1)
	b2, err2 := plainLockFinish(cmd2, out2)
	if err1 != nil {
		t.Errorf("the first run exits with %v:\n%s", err1, b1)
	}
	if err2 != nil {
		t.Errorf("the second run exits with %v:\n%s", err2, b2)
	}
	slices.Sort(oks)
	wantCalls := "vet ./...\nbuild ./...\ntest " + strings.Join(oks, " ")
	for i, fake := range []string{fake1, fake2} {
		if calls, _ := os.ReadFile(filepath.Join(fake, "calls")); strings.TrimSpace(string(calls)) != wantCalls {
			t.Errorf("run %d's go was run as:\n%s\nwant:\n%s", i+1, calls, wantCalls)
		}
	}
	if b, _ := os.ReadFile(order); string(b) != "start-run1\nend-run1\nstart-run2\nend-run2\n" {
		t.Errorf("the legs ordered %q, want the first's whole leg before the second's", b)
	}
	// The second run queued: its wait-started line and its waited seconds.
	// The first took the free lock and printed neither.
	if !raceWaitStarted.MatchString(b2) {
		t.Errorf("the second run has no start marker for its race-lock wait:\n%s", b2)
	}
	waits := regexp.MustCompile(`(?m)^test: waited \d+ s for the race lock$`).FindAllString(b2, -1)
	if len(waits) != 1 {
		t.Errorf("the second run has %d wait lines, want 1:\n%s", len(waits), b2)
	}
	summary := regexp.MustCompile(`(?m)^test: sh \.yardr/test @ [0-9a-f]+: pass, [0-9]+ packages ok, 0 failed, [0-9]+ s, waited [1-9][0-9]* s$`)
	if !summary.MatchString(b2) {
		t.Errorf("the second run's summary does not include the race-lock wait:\n%s", b2)
	}
	if raceWaitStarted.MatchString(b1) {
		t.Errorf("the first run queued for a free lock:\n%s", b1)
	}
	if entries, _ := os.ReadDir(qdir); len(entries) != 0 {
		t.Errorf("tickets left behind: %v", entries)
	}
}

// A plain run over go.mod, or over a file no row knows, tests every package,
// and queues for it on the race lock as class 1: held here by the test, the
// run waits with its ticket out, then runs when the lock is free, with the
// same wait line and waited seconds a landing's wait prints.
func TestYardTestWholePlainPathsQueue(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go in PATH")
	}
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		change func(t *testing.T, dir string)
	}{
		{"go.mod", func(t *testing.T, dir string) {
			t.Helper()
			b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), append(b, []byte("\n// changed\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"a file no row knows", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "contrib", "x.txt"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
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
			dir, base, _ := plainLockFixture(t, script)
			c.change(t, dir)
			gitT(t, dir, "add", "-A")
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			order := filepath.Join(t.TempDir(), "order")
			release := filepath.Join(t.TempDir(), "release")
			cmd, fake, out := plainLockStart(t, ctx, dir, base, home, realGo, "whole-bead", order, release, "m.test/m/a m.test/m/b", nil)
			plainLockSettle(t, release)
			qdir := filepath.Join(home, ".yardr", "race.queue")
			ticket := plainLockTicket(t, cmd, qdir, "whole-bead")
			if !strings.HasPrefix(ticket, "1-") {
				t.Errorf("the run's ticket is %s, want class 1", ticket)
			}
			if calls, _ := os.ReadFile(filepath.Join(fake, "calls")); strings.Contains(string(calls), "test ./...") {
				t.Errorf("the run's leg started while the lock is held")
			}
			// The wait timer starts before the ticket is made, so a
			// release before the first lock attempt leaves a zero wait
			// and no wait line. Hold until the ticket is a second old,
			// so the first attempt has run while held and the summary's
			// waited seconds are positive.
			firstSeen := time.Now()
			if wait := time.Until(firstSeen.Add(1100 * time.Millisecond)); wait > 0 {
				time.Sleep(wait)
			}
			if err := syscall.Flock(int(held.Fd()), syscall.LOCK_UN); err != nil {
				t.Fatal(err)
			}
			got, err := plainLockFinish(cmd, out)
			if err != nil {
				t.Fatalf("err %v, output:\n%s", err, got)
			}
			if !raceWaitStarted.MatchString(got) {
				t.Errorf("the output has no start marker for the race-lock wait:\n%s", got)
			}
			waits := regexp.MustCompile(`(?m)^test: waited \d+ s for the race lock$`).FindAllString(got, -1)
			if len(waits) != 1 {
				t.Errorf("the output has %d wait lines, want 1:\n%s", len(waits), got)
			}
			summary := regexp.MustCompile(`(?m)^test: sh \.yardr/test @ [0-9a-f]+: pass, [0-9]+ packages ok, 0 failed, [0-9]+ s, waited [1-9][0-9]* s$`)
			if !summary.MatchString(got) {
				t.Errorf("the summary does not include the race-lock wait:\n%s", got)
			}
			if calls, _ := os.ReadFile(filepath.Join(fake, "calls")); strings.TrimSpace(string(calls)) != "vet ./...\nbuild ./...\ntest ./..." {
				t.Errorf("go was run as:\n%s\nwant:\n%s", calls, "vet ./...\nbuild ./...\ntest ./...")
			}
			if entries, _ := os.ReadDir(qdir); len(entries) != 0 {
				t.Errorf("tickets left behind: %v", entries)
			}
		})
	}
}

// A plain run over one leaf package takes no ticket and finishes in
// seconds: the lock is held here, so a run that queued would wait on it
// instead of passing, and the queue stays empty, the output without a
// wait line.
func TestYardTestLeafPlainRunTakesNoTicket(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go in PATH")
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
	dir, base, _ := plainLockFixture(t, script)
	if err := os.WriteFile(filepath.Join(dir, "a", "p.go"), []byte("package p\n\nvar Changed int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "-A")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	order := filepath.Join(t.TempDir(), "order")
	release := filepath.Join(t.TempDir(), "release")
	cmd, fake, out := plainLockStart(t, ctx, dir, base, home, realGo, "leaf-bead", order, release, "m.test/m/a", nil)
	plainLockSettle(t, release)
	got, err := plainLockFinish(cmd, out)
	if err != nil {
		t.Fatalf("a leaf run under a held lock exits with %v, as if it queued:\n%s", err, got)
	}
	if entries, _ := os.ReadDir(filepath.Join(home, ".yardr", "race.queue")); len(entries) != 0 {
		t.Errorf("the run queued tickets: %v", entries)
	}
	if raceWaitStarted.MatchString(got) {
		t.Errorf("the run printed a race-lock wait line:\n%s", got)
	}
	if calls, _ := os.ReadFile(filepath.Join(fake, "calls")); strings.TrimSpace(string(calls)) != "vet ./...\nbuild ./...\ntest m.test/m/a" {
		t.Errorf("go was run as:\n%s\nwant:\n%s", calls, "vet ./...\nbuild ./...\ntest m.test/m/a")
	}
}

// A run nested in another's leg shares its hold instead of waiting on it:
// a whole-suite plain run with YARDR_RACE_HELD set takes no ticket, though
// the lock is held here and the selection queues without it.
func TestYardTestHeldWholePlainRunNeverQueues(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl in PATH")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go in PATH")
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
	dir, base, pkgs := plainLockFixture(t, script)
	changed := pkgs[:len(pkgs)/2+1]
	for i, pkg := range changed {
		if err := os.WriteFile(filepath.Join(dir, pkg+"/p.go"), []byte(fmt.Sprintf("package p\n\nvar Changed%d int\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, dir, "add", "-A")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	order := filepath.Join(t.TempDir(), "order")
	release := filepath.Join(t.TempDir(), "release")
	cmd, _, out := plainLockStart(t, ctx, dir, base, home, realGo, "held-bead", order, release, "m.test/m/a", []string{"YARDR_RACE_HELD=1"})
	plainLockSettle(t, release)
	got, err := plainLockFinish(cmd, out)
	if err != nil {
		t.Fatalf("a held run under a held lock exits with %v, as if it queued:\n%s", err, got)
	}
	if entries, _ := os.ReadDir(filepath.Join(home, ".yardr", "race.queue")); len(entries) != 0 {
		t.Errorf("the run queued tickets: %v", entries)
	}
	if raceWaitStarted.MatchString(got) {
		t.Errorf("the run printed a race-lock wait line:\n%s", got)
	}
}

// wholeplain is the gate's verdict on a plain selection: the whole suite at
// half the suite's packages or more, counted from pick's lists, the test
// variants folded to their package. The probe cuts it out of .yardr/test,
// failing when the script no longer holds that shape, and the cases feed it
// canned pairs: the boundary both ways, and every variant spelling.
func TestYardTestWholePlainThreshold(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	_, fn, ok := strings.Cut(string(b), "\nwholeplain() {\n")
	if ok {
		fn, _, ok = strings.Cut("\nwholeplain() {\n"+fn, "\n}\n")
	}
	if !ok || !strings.Contains(fn, "sel + sel") {
		t.Fatalf("cannot cut wholeplain out of %s: move the probe with the script", yardTest)
	}
	probe := filepath.Join(t.TempDir(), "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\n"+fn+"\n}\n\"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// pkgsLine is one go list -test line; only the import path counts.
	pkgsLine := func(path string) string { return path + "|/tmp/x|m.test/m/z||\n" }
	for _, c := range []struct {
		name  string
		pkgs  []string
		run   []string
		whole bool
	}{
		{"exactly half queues", []string{"m.test/m/a", "m.test/m/b", "m.test/m/c", "m.test/m/d"},
			[]string{"m.test/m/a", "m.test/m/b"}, true},
		{"under half skips", []string{"m.test/m/a", "m.test/m/b", "m.test/m/c", "m.test/m/d"},
			[]string{"m.test/m/a"}, false},
		{"one of two queues", []string{"m.test/m/a", "m.test/m/b"},
			[]string{"m.test/m/a"}, true},
		{"one of three skips", []string{"m.test/m/a", "m.test/m/b", "m.test/m/c"},
			[]string{"m.test/m/a"}, false},
		{"all queues", []string{"m.test/m/a", "m.test/m/b"},
			[]string{"m.test/m/a", "m.test/m/b"}, true},
		{"none skips", []string{"m.test/m/a", "m.test/m/b"}, nil, false},
		{"test variants fold to their package", []string{"m.test/m/a", "m.test/m/a [m.test/m/a.test]", "m.test/m/a_test [m.test/m/a.test]", "m.test/m/a.test", "m.test/m/b"},
			[]string{"m.test/m/a"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			var pkgs, run strings.Builder
			for _, p := range c.pkgs {
				pkgs.WriteString(pkgsLine(p))
			}
			for _, p := range c.run {
				run.WriteString(p + "\n")
			}
			if err := os.WriteFile(filepath.Join(dir, "pkgs"), []byte(pkgs.String()), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "run"), []byte(run.String()), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", probe, "wholeplain", filepath.Join(dir, "pkgs"), filepath.Join(dir, "run"))
			cmd.Env = scriptEnv()
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			code := 0
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			want := 1
			if c.whole {
				want = 0
			}
			if code != want || len(out) != 0 {
				t.Errorf("exit %d, output %q, want exit %d and no output", code, out, want)
			}
		})
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
	// A landing with no comparable base tests every package: only a landing
	// has a race leg.
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
	cmd := exec.Command("sh", "-c", "umask 022; sh .yardr/test")
	cmd.Dir = dir
	cmd.Env = append(scriptEnv(),
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"YARDR_LANDING=1", "YARDR_BASE=nowhere",
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
// logs every hold and runs the rest, so the run takes no real lock. Every
// row runs the gate under its own home and lock, so the rows run in
// parallel.
//
// The race leg also gives its go the load factor that sizes the waits of the
// contrib suites (YARDR_TEST_LOAD, internal/testload), read when the leg starts
// and named in a line: the machine's load over its cores from 1 to 4. Nothing
// else gets it, the re-run alone included, so that a slow path of the product
// still shows there; a factor the caller had is no one's but the gate's.
func TestYardTestBudgetRerunSharesTheRaceHold(t *testing.T) {
	t.Parallel()
	script, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	const (
		underAlone = "test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 12.3 s alone: under budget, loaded run"
		quietSays  = "machine not loaded, or its load unknown"
	)
	// One template repository for every row: a landing with no comparable
	// base tests every package, and asks go for no list of them: only a
	// landing has a race leg. Each row copies it and runs.
	template := t.TempDir()
	if err := os.MkdirAll(filepath.Join(template, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, ".yardr", "test"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, "go.mod"), []byte("module m.test/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, template, "init", "-q", "-b", "main")
	gitT(t, template, "add", "-A")
	gitT(t, template, "commit", "-q", "-m", "base")
	for _, c := range []struct {
		name  string
		alone string // the re-run's Go duration: fast passes, over fails
		ok    bool
		want  string // the verdict line
		// The machine's one-minute load on 10 cores, "" for a quiet 1.5; the
		// YARDR_TEST_LOAD the caller started the script with, "" for none; and
		// the factor the race leg is given with the load its line names.
		avg, given, factor, says, par string
	}{
		{"under alone passes", "12.3s", true, underAlone, "", "", "1.0", quietSays, ""},
		{"over alone fails naming both", "1m6.6s", false,
			"test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, 66.6 s alone, over the 60 s budget (docs/testing.md R1)",
			"", "", "1.0", quietSays, ""},
		{"sub-second alone passes", "750ms", true,
			"test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 0.75 s alone: under budget, loaded run",
			"", "", "1.0", quietSays, ""},
		{"microsecond alone passes", "750µs", true,
			"test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 0.00075 s alone: under budget, loaded run",
			"", "", "1.0", quietSays, ""},
		// The arithmetic at its edges (the cap, an unreadable load) is the
		// probe's, in TestYardTestBudgets; a row is a whole script run, and
		// the package has no time to spare (docs/testing.md R1), so these
		// two are the wiring: the factor and the -p reach the race leg (and the
		// -p its alone re-run), and a factor the caller had does not reach
		// anything.
		{"a loaded machine sizes the leg's waits and its -p by its load over its cores", "12.3s", true, underAlone,
			"25.3", "", "2.5", "load 25.3 on 10 cores", "-p=3"},
		{"a factor the caller had is not passed on", "12.3s", true, underAlone,
			"", "7", "1.0", quietSays, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fake := t.TempDir()
			dir := copyFixture(t, template)
			// A go whose race leg runs one package over budget and whose
			// re-run of it reports FAKE_ALONE, writing down every call
			// with YARDR_RACE_HELD, but for the landing's socket step beside
			// the race leg, which holds no lock; a perl that logs every hold
			// and runs the rest, as racelocked calls it.
			goFake := `#!/bin/sh
[ -z "$YARDR_TEST_RPC" ] || exit 0
echo "${GOOS:+$GOOS }$* held=${YARDR_RACE_HELD-unset} lock=${FAKE_LOCK_HELD-unset} load=${YARDR_TEST_LOAD-unset}" >>"$FAKE/calls"
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
			cmd := exec.Command("sh", ".yardr/test")
			cmd.Dir = dir
			// No YARDR_RACE_HELD: the race leg takes the lock itself, as
			// a top-level run does, and its worker keeps that hold through
			// the package re-measurement. The load is pinned, quiet unless
			// a case sets it, so the over-alone case fails however loaded
			// the test machine is.
			avg := c.avg
			if avg == "" {
				avg = "1.5"
			}
			cmd.Env = append(scriptEnv(),
				"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
				"YARDR_LANDING=1", "YARDR_BASE=nowhere",
				"HOME="+home, "TMPDIR="+tmp, "FAKE="+fake, "FAKE_ALONE="+c.alone,
				"YARDR_TEST_LOADAVG="+avg, "YARDR_TEST_CORES=10")
			if c.given != "" {
				cmd.Env = append(cmd.Env, "YARDR_TEST_LOAD="+c.given)
			}
			b, err := cmd.CombinedOutput()
			out := string(b)
			if (err == nil) != c.ok {
				t.Errorf("err %v, want ok %v", err, c.ok)
			}
			if !strings.Contains(out, c.want+"\n") {
				t.Errorf("the output has no verdict line %q", c.want)
			}
			// The re-run is the leg's flags and -count=1 on the one
			// package, inside the hold the leg set: the -p of a loaded
			// machine's leg among them, as one word.
			race := "test -race"
			if c.par != "" {
				race += " " + c.par
			}
			calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
			wantCalls := "vet ./... held=unset lock=unset load=unset\n" +
				"linux vet ./... held=unset lock=unset load=unset\n" +
				race + " ./... held=1 lock=1 load=" + c.factor + "\n" +
				race + " -count=1 yardr.dev/yardr/internal/dispatch held=1 lock=1 load=unset"
			if got := strings.TrimSpace(string(calls)); got != wantCalls {
				t.Errorf("go was run as:\n%s\nwant:\n%s", got, wantCalls)
			}
			// The leg says the factor it was given, once, with the load it read.
			sizing := "test: YARDR_TEST_LOAD=" + c.factor + " for the race leg (" + c.says + ")\n"
			if n := strings.Count(out, sizing); n != 1 {
				t.Errorf("the output has the factor line %q %d times, want once", strings.TrimSpace(sizing), n)
			}
			// One line says the -p, with the load it came from, and only
			// where the load made one.
			var wantPar []string
			if c.par != "" {
				wantPar = []string{"test: go test " + c.par + " (load " + c.avg + " on 10 cores)"}
			}
			if got := regexp.MustCompile(`(?m)^test: go test -p=.*$`).FindAllString(out, -1); !slices.Equal(got, wantPar) {
				t.Errorf("the output's -p lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantPar, "\n"))
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

// raceTicketName matches a queue ticket: <class>-<seconds><nanoseconds>-<pid>,
// the class 0 for a landing and 1 for any other run.
var raceTicketName = regexp.MustCompile(`^[01]-\d+-\d+$`)

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

// The race lock serves waiters in arrival order, landings first, and says
// so: waiters queued behind a held lock in a known order take it in that
// order, a landing (YARDR_LANDING set and not empty) before the plain runs
// that queued earlier, and two of a class in the order they came, each
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
	startWith := func(bead string, env []string, args ...string) raceRun {
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
		cmd.Env = append(append(scriptEnv(), "HOME="+home, "YARDR_BEAD="+bead), env...)
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
	start := func(bead string, args ...string) raceRun { return startWith(bead, nil, args...) }
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
	// waitPlace waits for a waiter to print its place, which it does once it
	// has looked at the queue. The place counts the tickets there are as it
	// looks, so a waiter is started only after the one before it has printed
	// its own, and the holder is released only after all have.
	waitPlace := func(q raceRun) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			b, _ := os.ReadFile(q.out.Name())
			if strings.Contains(string(b), " in line, ") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("a waiter printed no place in 30 s:\n%s", b)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	killTicket := func(name string, sig syscall.Signal) {
		t.Helper()
		pid, err := strconv.Atoi(name[strings.LastIndex(name, "-")+1:])
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
	// ticketOf is the ticket a bead's waiter queued under, "" for none.
	ticketOf := func(bead string) string {
		t.Helper()
		for _, n := range tickets() {
			if b, _ := os.ReadFile(filepath.Join(qdir, n)); strings.Contains(string(b), "bead="+bead+"\n") {
				return n
			}
		}
		return ""
	}
	w2 := start("w2-bead", "sh", "-c", "echo w2 >>"+quote(order))
	known = waitTicket(known)
	waitPlace(w2)
	// A YARDR_LANDING that is set and empty is no landing, as the script reads it.
	w3 := startWith("w3-bead", []string{"YARDR_LANDING="}, "sh", "-c", "echo w3 >>"+quote(order))
	known = waitTicket(known)
	waitPlace(w3)
	// A ticket carries the waiter's bead and worktree.
	if n := ticketOf("w2-bead"); n == "" {
		t.Errorf("no ticket carries bead=w2-bead")
	} else if b, _ := os.ReadFile(filepath.Join(qdir, n)); !strings.Contains(string(b), "dir="+wt+"\n") {
		t.Errorf("the w2 ticket names no worktree %q:\n%s", wt, b)
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
	// Two landings queue after both plain waiters. They take the lock before
	// them, in the order they came, and the holder, a plain run, is not
	// pre-empted: it still holds the lock until the test lets it go. Each
	// counts the holder in its place, as a plain waiter does, though its
	// ticket sorts behind theirs.
	l1 := startWith("l1-bead", []string{"YARDR_LANDING=1"}, "sh", "-c", "echo l1 >>"+quote(order))
	known = waitTicket(known)
	waitPlace(l1)
	l2 := startWith("l2-bead", []string{"YARDR_LANDING=1"}, "sh", "-c", "echo l2 >>"+quote(order))
	waitTicket(known)
	waitPlace(l2)
	// The class is the first part of the ticket's name.
	for bead, class := range map[string]string{"holder-bead": "1-", "w2-bead": "1-", "w3-bead": "1-", "l1-bead": "0-", "l2-bead": "0-"} {
		if n := ticketOf(bead); !strings.HasPrefix(n, class) {
			t.Errorf("the ticket of %s is %q, want one of class %s", bead, n, class)
		}
	}
	if code, out := finish(holder); code != 0 {
		t.Fatalf("the holder exits %d, want 0:\n%s", code, out)
	}
	type waiter struct {
		bead string
		run  raceRun
		want string
	}
	waiters := []waiter{
		{"w2-bead", w2, `(?m)^test: race lock: 2nd in line, held by holder-bead for \d+ s$`},
		{"w3-bead", w3, `(?m)^test: race lock: 3rd in line, held by holder-bead for \d+ s$`},
		{"l1-bead", l1, `(?m)^test: race lock: 2nd in line, held by holder-bead for \d+ s$`},
		{"l2-bead", l2, `(?m)^test: race lock: 3rd in line, held by holder-bead for \d+ s$`},
	}
	for _, w := range waiters {
		code, out := finish(w.run)
		if code != 0 {
			t.Fatalf("%s exits %d, want 0:\n%s", w.bead, code, out)
		}
		// Each printed its place and the holder while it waited.
		if ok, _ := regexp.MatchString(w.want, string(out)); !ok {
			t.Errorf("%s printed no %q:\n%s", w.bead, w.want, out)
		}
		if !strings.Contains(string(out), "test: waited ") {
			t.Errorf("%s printed no waited line:\n%s", w.bead, out)
		}
	}
	// The landings ran first, then the plain runs, each class in arrival
	// order, past the killed tickets.
	if body, _ := os.ReadFile(order); string(body) != "l1\nl2\nw2\nw3\n" {
		t.Errorf("the runs ordered %q, want %q", body, "l1\nl2\nw2\nw3\n")
	}
	// Nothing is left behind: no ticket, live or dead, and no holder.
	if got := tickets(); len(got) != 0 {
		t.Errorf("tickets left behind: %v", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".yardr", "race.holder")); !os.IsNotExist(err) {
		t.Errorf("the holder file is left behind")
	}
	// Pre-seeded dead tickets at the back of the queue are absent after the
	// next acquisition: the acquirer's fullclean reaps them. The first sorts
	// last by a future timestamp; the second is named as an older copy of the
	// script named its tickets, with no class, which sorts after every
	// classed one. So front does not reach either, and a live ticket
	// lingering here would convict the perl instead.
	if err := os.MkdirAll(qdir, 0o700); err != nil {
		t.Fatal(err)
	}
	deadPID := raceDeadPID(t)
	now := time.Now().Unix()
	seedNames := []string{
		fmt.Sprintf("1-%010d%09d-%d", now+3600, 999999999, deadPID),
		fmt.Sprintf("%010d%09d-%d", now+3600, 999999999, deadPID),
	}
	seed := fmt.Sprintf("pid=%d\nstart=0\nbead=dead-seed\ndir=%s\ncreated=%d\n", deadPID, wt, now)
	for _, name := range seedNames {
		if err := os.WriteFile(filepath.Join(qdir, name), []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// No holder blocks it: the waiter takes the lock at once, so its
	// ticket may come and go before a poll sees it. Finish it and read
	// the queue it leaves.
	seedWaiter := start("seed-bead", "true")
	if code, out := finish(seedWaiter); code != 0 {
		t.Fatalf("the seed waiter exits %d, want 0:\n%s", code, out)
	}
	for _, name := range seedNames {
		if _, err := os.Stat(filepath.Join(qdir, name)); !os.IsNotExist(err) {
			t.Errorf("the pre-seeded dead ticket %s is still queued after the next acquisition: %v", name, err)
		}
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

// The --race-queue view prints the holder and waiters with their classes,
// beads and ages, in the order they take the lock (a landing first, even when
// it is the newest; a ticket from an older copy of the script, which has no
// class, among the plain runs by the time it came), skipping dead tickets
// without leaving a ticket or lock behind. Synthetic tickets stand in for
// queued runs: a helper process stands in for the holder, the test's own pid
// for live waiters, and a reaped child for a dead waiter. Its own home stands
// in for the yard's, so no real gate waits on it.
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
	// class is the first part of a ticket's name: 0 for a landing, 1 for any
	// other run, none for a ticket an older copy of the script made.
	ticket := func(class string, when int64, pid int, start, bead, dir string) {
		t.Helper()
		name := strconv.FormatInt(when, 10) + "000000000-" + strconv.Itoa(pid)
		if class != "" {
			name = class + "-" + name
		}
		write(qdir, name,
			"pid="+strconv.Itoa(pid)+"\nstart="+start+"\nbead="+bead+"\ndir="+dir+"\ncreated="+strconv.FormatInt(when, 10)+"\n")
	}
	live := raceOwnStart(t, self)
	ticket("1", now-400, raceDeadPID(t), "bogus", "gone-bead", "/wt/gone")
	ticket("1", now-350, holderPID, holderStart, "holder-bead", "/wt/holder")
	ticket("1", now-300, self, live, "old-bead", "/wt/old")
	ticket("", now-200, self, live, "legacy-bead", "/wt/legacy")
	ticket("0", now-30, self, live, "new-bead", "/wt/new")
	// A ticket whose content says nothing is a waiter all the same: its pid is
	// in its name.
	write(qdir, "1-"+strconv.FormatInt(now-100, 10)+"000000000-"+strconv.Itoa(self), "")
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
	want := []string{
		`^test: race lock: held by holder-bead for 6 m \(/wt/holder\)$`,
		// The age is measured at the view, after the ticket writes, the
		// flock and the script's start, which take seconds on a loaded
		// Mac: 30 s of slack, as the minute rows have.
		`^test: race lock: 1st in line \(landing\): new-bead waiting [3-5]\d s \(/wt/new\)$`,
		`^test: race lock: 2nd in line \(plain\): old-bead waiting 5 m \(/wt/old\)$`,
		`^test: race lock: 3rd in line \(plain\): legacy-bead waiting 3 m \(/wt/legacy\)$`,
		`^test: race lock: 4th in line \(plain\): pid ` + strconv.Itoa(self) + ` waiting 1 m \(\?\)$`,
	}
	if lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n"); len(lines) != len(want) {
		t.Errorf("the view prints %d lines, want %d:\n%s", len(lines), len(want), out)
	} else {
		for i, w := range want {
			if ok, _ := regexp.MatchString(w, lines[i]); !ok {
				t.Errorf("line %d of the view is %q, want one matching %q", i+1, lines[i], w)
			}
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
	for _, bead := range []string{"old-bead", "new-bead", "legacy-bead"} {
		if !strings.Contains(out, bead) {
			t.Errorf("the view lost %s:\n%s", bead, out)
		}
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
// runlogged, machineload and loadedmachine), sizewaits, which reads the
// same load for the race leg's wait factor, and gatepar and legflag, which
// turn it into the race leg's -p. The probe runs them as the script has
// them, so a case over it fails when the gate would. It also returns the
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
	machinefn := cut("\nmachineload() {\n", "\n}\n")
	loadfn := cut("\nloadedmachine() {\n", "\n}\n")
	sizefn := cut("\nsizewaits() {\n", "\n}\n")
	eitherfn := cut("\nloadedeither() {\n", "\n}\n")
	gatefn := cut("\ngatepar() {\n", "\n}\n")
	legfn := cut("\nlegflag() {\n", "\n}\n")
	pkgfn := cut("\npackagebudget() {\n", "\n}\n")
	long := cut("\nlongsleeps='\n", "\n'\n")
	sleepfn := cut("\nsleepbudget() {\n", "\n}\n")
	markfn := cut("\nnomarkers() {\n", "\n}\n")
	if !strings.Contains(pkgfn, "60 s budget") || !strings.Contains(sleepfn, "500 ms budget") || !strings.Contains(markfn, "redaction marker scan") || !strings.Contains(machinefn, "load $avg on $cores cores") || !strings.Contains(loadfn, "machineload") || !strings.Contains(sizefn, "YARDR_TEST_LOAD=") || !strings.Contains(eitherfn, `echo "$1"`) || !strings.Contains(gatefn, "awk") || !strings.Contains(legfn, "gatepar") {
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
	return took + runlogfn + over + machinefn + loadfn + sizefn + eitherfn + gatefn + legfn + pkgfn + long + sleepfn + markfn + "\n\"$@\"\n", overDirs, sites
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
		// The landing's socket step runs beside the race leg and holds no
		// lock: this go ignores it, so only the race leg leaves an orphan.
		"go": `#!/bin/sh
[ -z "$YARDR_TEST_RPC" ] || exit 0
if [ "$1" = test ]; then
  if [ "$FORCE_ORPHAN_DEADLINE" = true ]; then
    : >"$FAKE/fixture-ready"
    # Each wait also ends when the test binary is gone: t.Cleanup never
    # runs for a killed binary, so without kill -0 the fixture would poll
    # for a file nobody will write, forever.
    while [ ! -f "$FAKE/allow-barrier" ]; do kill -0 "$YARDR_TEST_PID" 2>/dev/null || exit 0; sleep 0.1; done
    : >"$FAKE/barrier-reached"
    while [ ! -f "$FAKE/release" ]; do kill -0 "$YARDR_TEST_PID" 2>/dev/null || exit 0; sleep 0.1; done
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
	// A landing with no comparable base tests every package: only a landing
	// has a race leg.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m.test/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	cmd := exec.Command("sh", ".yardr/test")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// YARDR_TEST_PID is the test binary's own pid: the fake go's waits end
	// when it is gone, since a killed binary runs no cleanup.
	cmd.Env = append(scriptEnv(),
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"YARDR_LANDING=1", "YARDR_BASE=nowhere", "HOME="+home, "TMPDIR="+tmp, "FAKE="+fake,
		"FORCE_ORPHAN_DEADLINE="+strconv.FormatBool(forceDeadline),
		"YARDR_TEST_PID="+strconv.Itoa(os.Getpid()))
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
// fails every re-run when FAKE_FAIL is set. FAKE_SLEEP sleeps that long
// in every re-run, opening a window the re-runs overlap in; FAKE_TRACK
// records the maximum concurrent re-runs, which is what proves the
// overlap, since a wall clock fails on a loaded machine; FAKE_CACHED
// prints no seconds, as a cached re-run would. It writes down every call
// with YARDR_RACE_HELD.
func r1go(t *testing.T) (fake string, env []string) {
	t.Helper()
	fake = t.TempDir()
	goFake := `#!/bin/sh
echo "$* held=${YARDR_RACE_HELD-unset}" >>"$FAKE/calls"
# The mkdir lock makes the fake's concurrent-run count atomic for its test.
if [ -n "$FAKE_TRACK" ]; then
  lock=$FAKE/active.lock
  while ! mkdir "$lock" 2>/dev/null; do :; done
  active=$(cat "$FAKE/active" 2>/dev/null || echo 0)
  active=$((active + 1))
  echo "$active" >"$FAKE/active"
  max=$(cat "$FAKE/max" 2>/dev/null || echo 0)
  [ "$active" -le "$max" ] || echo "$active" >"$FAKE/max"
  rmdir "$lock"
fi
[ -n "$FAKE_SLEEP" ] && sleep "$FAKE_SLEEP"
if [ -n "$FAKE_TRACK" ]; then
  lock=$FAKE/active.lock
  while ! mkdir "$lock" 2>/dev/null; do :; done
  active=$(cat "$FAKE/active" 2>/dev/null || echo 0)
  echo "$((active - 1))" >"$FAKE/active"
  rmdir "$lock"
fi
for last in "$@"; do pkg=$last; done
if [ -n "$FAKE_FAIL" ]; then
  echo "--- FAIL: TestX (0.00s)"
  echo "FAIL"
  printf 'FAIL\t%s\t0.123s\n' "$pkg"
  exit 1
fi
if [ -n "$FAKE_CACHED" ]; then
  printf 'ok  \t%s\t(cached)\n' "$pkg"
  exit 0
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
	// The cases run in parallel, each in its own directory with its own
	// fake go: the probe, the table rows and the counts are read-only once
	// built. Each takes its own t, as TestYardTestSelects' rows do. The
	// re-run waves below stay sequential, so the timed row's batch runs
	// under the pool's contention alone; its only wall time is a floor the
	// load cannot break, and the fake's record proves the overlap.
	runEnv := func(t *testing.T, dir string, env []string, args ...string) (string, int) {
		t.Helper()
		return runProbe(t, probePath, dir, os.Environ(), env, args...)
	}
	run := func(t *testing.T, dir string, args ...string) (string, int) {
		t.Helper()
		return runEnv(t, dir, nil, args...)
	}
	write := func(t *testing.T, dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a compliant tree passes the sleep budget", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "ok_test.go", "package p\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestOk(t *testing.T) {\n\t"+
			sleepSite("500 * time.Millisecond")+"\n\t"+sleepSite("10 * time.Millisecond")+"\n\tdelay := 300 * time.Millisecond\n\t"+sleepSite("delay")+"\n}\n")
		// A sleep in a line comment is not code, and a sleep outside a
		// test file is not judged.
		write(t, dir, "comment_test.go", "package p\n\n// "+sleepSite("30 * time.Second")+" in a comment is not a sleep.\n")
		write(t, dir, "other.go", "package p\n\nimport \"time\"\n\nfunc wait() { "+sleepSite("30 * time.Second")+" }\n")
		out, code := run(t, dir, "sleepbudget")
		if code != 0 || strings.Contains(out, "over the 500 ms budget") {
			t.Errorf("exit %d, output %q, want exit 0 and no budget line", code, out)
		}
	})

	t.Run("an over-budget site fails the sleep budget", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "bad_test.go", "package p\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestBad(t *testing.T) {\n\t"+
			sleepSite("2 * time.Second")+"\n\t"+sleepSite("600 * time.Millisecond")+"\n\t"+sleepSite("time.Second")+"\n\t"+sleepSite("500.4 * time.Millisecond")+"\n}\n")
		out, code := run(t, dir, "sleepbudget")
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
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "yardtest_test.go", "package p\n\nimport \"time\"\n\nfunc f() {\n\t"+
			sleepSite("3 * time.Second")+"\n\t"+sleepSite("3 * time.Second")+"\n}\n")
		out, code := run(t, dir, "sleepbudget")
		if code != 1 || !strings.Contains(out, "yardtest_test.go:7 sleeps 3 * time.Second, over the 500 ms budget") {
			t.Errorf("exit %d, output %q, want the second 3 s sleep to fail", code, out)
		}
	})

	t.Run("the tree's allow-listed sleeps are counted", func(t *testing.T) {
		t.Parallel()
		// On the checkout itself: green, with every row's site still in
		// the tree. A row whose site is gone, or a site no row names,
		// fails here as it fails the gate.
		out, code := run(t, ".", "sleepbudget")
		if code != 0 {
			t.Fatalf("sleepbudget on the tree: exit %d\n%s", code, out)
		}
		if want := "test: " + strconv.Itoa(sleepSites) + " sleeps over 500 ms allow-listed"; !strings.Contains(out, want) {
			t.Errorf("output %q, want it to contain %q", out, want)
		}
	})

	t.Run("a tracked file carrying the redaction marker fails the marker check", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		gitT(t, dir, "init", "-q", "-b", "main")
		// Built from parts, as the step builds it: the literal must not
		// sit in this file, which the step judges too.
		marker := "[" + "REDACTED]"
		write(t, dir, "note.md", "sent as a token\nline two carries "+marker+" here\n")
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", "base")
		out, code := run(t, dir, "nomarkers")
		if code != 1 {
			t.Fatalf("exit %d, output %q, want exit 1", code, out)
		}
		if want := "test: note.md:2:line two carries " + marker + " here"; !strings.Contains(out, want) {
			t.Errorf("output %q, want it to contain %q", out, want)
		}
		if !strings.Contains(out, "test: a tracked file carries the redaction marker") {
			t.Errorf("output %q, want it to name the failure", out)
		}
	})

	t.Run("the product's lowercase markers pass the marker check", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		gitT(t, dir, "init", "-q", "-b", "main")
		write(t, dir, "login.go", "package p\n\n// [redacted] and [credential redacted] stay.\n")
		gitT(t, dir, "add", "-A")
		gitT(t, dir, "commit", "-q", "-m", "base")
		out, code := run(t, dir, "nomarkers")
		if code != 0 || strings.Contains(out, "carries the redaction marker") {
			t.Errorf("exit %d, output %q, want exit 0 and no marker line", code, out)
		}
	})

	t.Run("a failed marker scan fails the check", func(t *testing.T) {
		t.Parallel()
		// A real failure of git grep, not a stand-in for one: git dies on a
		// GIT_DIR that is no repository, as it does where the run is no
		// checkout. That needs no repository and no executable written
		// first, whose first run is slow on a loaded machine. The status is
		// git's, so the test names no number.
		dir := t.TempDir()
		out, code := runEnv(t, dir, []string{"GIT_DIR=" + filepath.Join(dir, "no-repository")}, "nomarkers")
		if want := "test: redaction marker scan failed (git grep exited " + strconv.Itoa(code) + ")"; code == 0 || code == 1 || !strings.Contains(out, want) {
			t.Errorf("exit %d, output %q, want git's failure status (not 0 or 1) and %q", code, out, want)
		}
	})

	t.Run("an untracked file carrying the marker passes the marker check", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		gitT(t, dir, "init", "-q", "-b", "main")
		gitT(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
		marker := "[" + "REDACTED]"
		write(t, dir, "draft.md", "line one carries "+marker+" but is untracked\n")
		out, code := run(t, dir, "nomarkers")
		if code != 0 || strings.Contains(out, "carries the redaction marker") {
			t.Errorf("exit %d, output %q, want exit 0 and no marker line", code, out)
		}
	})

	t.Run("the package budget judges race legs by re-measuring alone", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "red.txt",
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
		// cli, dispatch, pack, store and web are re-run, up to three at
		// a time, with the leg's flags and -count=1, inside its hold.
		// The module path is the root, whose
		// allow-list row yardr-1x36 deleted, as yardr-3cc4 deleted
		// store's, yardr-ab6t cli's and yardr-3gdg web's. The load is
		// pinned quiet, so the root's over-alone verdict fails however
		// loaded the test machine is.
		fake, env := r1go(t)
		out, code := runEnv(t, dir, append(env,
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
		// The re-runs overlap, so the journal's order is the scheduler's
		// rather than the launch's; the set is pinned, in sorted order.
		calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
		got := strings.Split(strings.TrimSpace(string(calls)), "\n")
		slices.Sort(got)
		want := []string{
			"test -race -count=1 yardr.dev/yardr held=1",
			"test -race -count=1 yardr.dev/yardr/internal/cli held=1",
			"test -race -count=1 yardr.dev/yardr/internal/dispatch held=1",
			"test -race -count=1 yardr.dev/yardr/internal/pack held=1",
			"test -race -count=1 yardr.dev/yardr/internal/store held=1",
			"test -race -count=1 yardr.dev/yardr/internal/web held=1",
		}
		if !slices.Equal(got, want) {
			t.Errorf("go was run as:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	t.Run("a package under budget alone passes", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "loaded.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
		fake, env := r1go(t)
		out, code := runEnv(t, dir, append(env, "FAKE_OVER="),
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

	t.Run("the alone re-run carries the leg's -p as one word", func(t *testing.T) {
		t.Parallel()
		// A loaded landing's race leg has -p=N (gatepar), and the re-run takes
		// the leg's flags, the dash-words after test: in two words, -p 2 would
		// reach it as -p -count=1, which go refuses.
		dir := t.TempDir()
		write(t, dir, "par.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
		fake, env := r1go(t)
		out, code := runEnv(t, dir, append(env, "FAKE_OVER="),
			"packagebudget", "par.txt", "go", "test", "-race", "-p=2", "./...")
		if code != 0 {
			t.Fatalf("exit %d, output %q, want exit 0", code, out)
		}
		want := "test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 12.3 s alone: under budget, loaded run"
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output %q, want it to hold %q", out, want)
		}
		calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
		if got, want := strings.TrimSpace(string(calls)), "test -race -p=2 -count=1 yardr.dev/yardr/internal/dispatch held=1"; got != want {
			t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("a package over budget alone on a loaded machine passes", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "heavy.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
		fake, env := r1go(t)
		out, code := runEnv(t, dir, append(env,
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

	t.Run("quiet run marks and the race leg's wait factor do not reach the budget probe", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "leak.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
		inheritedEnv := append(os.Environ(), "YARDR_R1_STRICT=1", "YARDR_QUIET_RUN=1", "YARDR_TEST_LOAD=3.0")
		for _, kv := range scriptEnvFrom(inheritedEnv) {
			k, _, _ := strings.Cut(kv, "=")
			if k == "YARDR_R1_STRICT" || k == "YARDR_QUIET_RUN" || k == "YARDR_TEST_LOAD" {
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

	t.Run("the run's yard does not reach the script", func(t *testing.T) {
		t.Parallel()
		// A builder's session names its bead, depot and yard; none of
		// those reach a case's script, and the home a real yardr reached
		// anyway would read is a path that cannot be a yard (yardr-vvy2).
		inheritedEnv := append(os.Environ(),
			"YARDR_BEAD=m-1", "YARDR_DEPOT=m", "YARDR_HOME=/home/u/.yardr",
			"YARDR_ACTOR=u", "YARDR_SESSION=s")
		var home string
		n := 0
		for _, kv := range scriptEnvFrom(inheritedEnv) {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "YARDR_BEAD", "YARDR_DEPOT", "YARDR_ACTOR", "YARDR_SESSION":
				t.Fatalf("scriptEnv carries %q from the run", kv)
			case "YARDR_HOME":
				home, n = v, n+1
			}
		}
		if n != 1 {
			t.Fatalf("scriptEnv sets YARDR_HOME %d times, want once", n)
		}
		if home != noYardHome {
			t.Errorf("scriptEnv sets YARDR_HOME=%q, want the unusable %q", home, noYardHome)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("YARDR_HOME %q exists, want a path that cannot be a yard", home)
		}
	})

	t.Run("the quiet run's strict setting fails over budget alone on a loaded machine", func(t *testing.T) {
		t.Parallel()
		// The same loaded re-measurement, judged twice: without the
		// setting it loud-passes as at a landing, with YARDR_R1_STRICT,
		// which the quiet run alone sets, it fails, naming both times
		// and the load beside them.
		dir := t.TempDir()
		write(t, dir, "strict.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
		loaded := []string{
			"FAKE_OVER=yardr.dev/yardr/internal/dispatch",
			"YARDR_TEST_LOADAVG=72.4", "YARDR_TEST_CORES=10",
		}
		_, env := r1go(t)
		out, code := runEnv(t, dir, append(env, loaded...),
			"packagebudget", "strict.txt", "go", "test", "-race", "./...")
		if code != 0 {
			t.Fatalf("without the setting: exit %d, output %q, want exit 0", code, out)
		}
		loud := "test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 66.6 s alone: over budget, loaded machine (load 72.4 on 10 cores), not failed"
		if !strings.Contains(out, loud+"\n") {
			t.Errorf("without the setting: output %q, want it to hold %q", out, loud)
		}
		_, env = r1go(t)
		out, code = runEnv(t, dir, append(env, append(loaded, "YARDR_R1_STRICT=1")...),
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
		t.Parallel()
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
				t.Parallel()
				dir := t.TempDir()
				write(t, dir, "sampled.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
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
		t.Parallel()
		for _, c := range []struct {
			name string
			avg  string
		}{
			{"unreadable", "bogus"},
			{"at the count", "10"},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				write(t, dir, "stray.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n")
				_, env := r1go(t)
				out, code := runEnv(t, dir, append(env,
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

	t.Run("the race leg's wait factor is the load over the cores, from 1 to 4", func(t *testing.T) {
		t.Parallel()
		// sizewaits reads the load as loadedmachine does, so a quiet machine
		// and an unreadable one are both 1, and says what it set under to.
		const quiet = "machine not loaded, or its load unknown"
		for _, c := range []struct {
			name, avg, cores, factor, says string
		}{
			{"a quiet machine", "1.5", "10", "1.0", quiet},
			{"a load at the core count", "10", "10", "1.0", quiet},
			{"a load just past it", "10.4", "10", "1.0", "load 10.4 on 10 cores"},
			{"twice the cores", "20", "10", "2.0", "load 20 on 10 cores"},
			{"a fraction of a core over", "25.3", "10", "2.5", "load 25.3 on 10 cores"},
			{"fewer cores", "6.8", "4", "1.7", "load 6.8 on 4 cores"},
			{"four times the cores", "40", "10", "4.0", "load 40 on 10 cores"},
			{"past four times the cores", "72.4", "10", "4.0", "load 72.4 on 10 cores"},
			{"a load that is no number", "bogus", "10", "1.0", quiet},
			{"cores that are no number", "20", "many", "1.0", quiet},
			{"no cores", "20", "0", "1.0", quiet},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				out, code := runEnv(t, t.TempDir(), []string{"YARDR_TEST_LOADAVG=" + c.avg, "YARDR_TEST_CORES=" + c.cores},
					"eval", `sizewaits; echo "under=$under"`)
				want := "test: YARDR_TEST_LOAD=" + c.factor + " for the race leg (" + c.says + ")\n" +
					"under=env YARDR_TEST_LOAD=" + c.factor + "\n"
				if code != 0 || out != want {
					t.Errorf("exit %d, output %q, want exit 0 and %q", code, out, want)
				}
			})
		}
		// loadedmachine answers only for a machine past its cores, but the
		// factor keeps its own floor: a line under the cores, which a change
		// to that predicate could let through, is 1 and not a shorter wait.
		out, code := runEnv(t, t.TempDir(), nil, "eval",
			`loadedmachine() { echo "load 5 on 10 cores"; }; sizewaits; echo "under=$under"`)
		want := "test: YARDR_TEST_LOAD=1.0 for the race leg (load 5 on 10 cores)\nunder=env YARDR_TEST_LOAD=1.0\n"
		if code != 0 || out != want {
			t.Errorf("a line under the cores: exit %d, output %q, want exit 0 and %q", code, out, want)
		}
	})

	t.Run("gatepar gives the race leg the cores the load leaves it", func(t *testing.T) {
		t.Parallel()
		// The line loadedmachine printed in, the leg's -p out: the cores
		// squared over the load, rounded down, at least 2 and at most the
		// cores. The line is all it reads, so the cases need no machine.
		for _, c := range []struct {
			line string
			want string
		}{
			{"load 40 on 10 cores", "2"},
			{"load 15 on 10 cores", "6"},
			{"load 10.5 on 10 cores", "9"},
			{"load 72.4 on 10 cores", "2"}, // 1.38 rounds down to 1, held at 2
			{"load 5 on 1 cores", "1"},     // 2 would raise go's default of 1
			{"load 4 on 10 cores", "10"},   // a line loadedmachine never prints: all the cores
		} {
			t.Run(c.line, func(t *testing.T) {
				t.Parallel()
				out, code := run(t, t.TempDir(), "gatepar", c.line)
				if code != 0 || out != c.want+"\n" {
					t.Errorf("exit %d, output %q, want exit 0 and %q", code, out, c.want+"\n")
				}
			})
		}
		// No line is where loadedmachine found the machine quiet or its load
		// unknown: no -p, so the leg's command is what it was.
		if out, code := run(t, t.TempDir(), "gatepar", ""); code != 1 || out != "" {
			t.Errorf("an empty line: exit %d, output %q, want exit 1 and no output", code, out)
		}
	})

	t.Run("the leg's -p follows the load, and a quiet or unknown one adds none", func(t *testing.T) {
		t.Parallel()
		// legflag sets the flag the race leg's command carries and says so in
		// one line, only where the load made one. The probe here carries a
		// function that shows the flag it set.
		showing, ok := strings.CutSuffix(probe, "\n\"$@\"\n")
		if !ok {
			t.Fatal("the probe no longer ends by running its arguments: move this case with it")
		}
		showing += "\nshowleg() { legflag; echo \"legpar=[$legpar]\"; }\n\"$@\"\n"
		showingPath := filepath.Join(t.TempDir(), "showing.sh")
		if err := os.WriteFile(showingPath, []byte(showing), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			name string
			avg  string // the machine's one-minute load on 10 cores: quiet where empty, as scriptEnv pins it
			want string
		}{
			{"quiet", "", "legpar=[]\n"},
			{"loaded", "40", "test: go test -p=2 (load 40 on 10 cores)\nlegpar=[-p=2]\n"},
			{"unknown", "bogus", "legpar=[]\n"},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				var env []string
				if c.avg != "" {
					env = []string{"YARDR_TEST_LOADAVG=" + c.avg, "YARDR_TEST_CORES=10"}
				}
				out, code := runProbe(t, showingPath, t.TempDir(), os.Environ(), env, "showleg")
				if code != 0 || out != c.want {
					t.Errorf("exit %d, output %q, want exit 0 and %q", code, out, c.want)
				}
			})
		}
	})

	t.Run("a re-run that fails fails the run", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "flaky.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t61.2s\n")
		_, env := r1go(t)
		out, code := runEnv(t, dir, append(env, "FAKE_FAIL=1"),
			"packagebudget", "flaky.txt", "go", "test", "-race", "./...")
		if code != 1 {
			t.Fatalf("exit %d, output %q, want exit 1", code, out)
		}
		want := "test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, over the 60 s budget (docs/testing.md R1), and its re-run alone failed"
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output %q, want it to hold %q", out, want)
		}
	})

	t.Run("over-budget packages are re-run up to three at a time", func(t *testing.T) {
		// Four packages over budget, each re-run sleeping two seconds so
		// the re-runs overlap: waves of three, which the fake's maximum
		// concurrent re-runs proves, not the wall clock, which a loaded
		// machine stretches past any ceiling. The verdicts are the
		// sequential ones, every package judged by the one
		// load pair sampled around the batch. The leg lists the packages
		// out of order; the verdicts print in sorted order. Only the quiet
		// row sleeps: the other rows judge the same verdicts without paying
		// for the overlap twice, since root's own budget pays for this case.
		leg := "ok  \tyardr.dev/yardr/internal/store\t1m1.4s\n" +
			"ok  \tyardr.dev/yardr/internal/cli\t1m1.1s\n" +
			"ok  \tyardr.dev/yardr/internal/pack\t1m1.3s\n" +
			"ok  \tyardr.dev/yardr/internal/dispatch\t1m1.2s\n"
		cli := "test: R1: yardr.dev/yardr/internal/cli took 61.1 s in the run, 12.3 s alone: under budget, loaded run"
		pack := "test: R1: yardr.dev/yardr/internal/pack took 61.3 s in the run, 12.3 s alone: under budget, loaded run"
		store := "test: R1: yardr.dev/yardr/internal/store took 61.4 s in the run, 12.3 s alone: under budget, loaded run"
		for _, c := range []struct {
			name string
			env  []string
			code int
			want string // dispatch's verdict: over alone, judged by the load
		}{
			{"quiet convicts", []string{"YARDR_TEST_LOADAVG=1.5", "YARDR_TEST_CORES=10", "FAKE_SLEEP=2"}, 1,
				"test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, 66.6 s alone, over the 60 s budget (docs/testing.md R1)"},
			{"loaded passes", []string{"YARDR_TEST_LOADAVG=72.4", "YARDR_TEST_CORES=10"}, 0,
				"test: R1: yardr.dev/yardr/internal/dispatch took 61.2 s in the run, 66.6 s alone: over budget, loaded machine (load 72.4 on 10 cores), not failed"},
			{"strict fails naming the load", []string{"YARDR_TEST_LOADAVG=72.4", "YARDR_TEST_CORES=10", "YARDR_R1_STRICT=1"}, 1,
				"test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, 66.6 s alone, over the 60 s budget (docs/testing.md R1), loaded machine (load 72.4 on 10 cores)"},
		} {
			t.Run(c.name, func(t *testing.T) {
				dir := t.TempDir()
				write(t, dir, "wave.txt", leg)
				fake, env := r1go(t)
				tmp := filepath.Join(t.TempDir(), "tmp with spaces")
				if err := os.MkdirAll(tmp, 0o700); err != nil {
					t.Fatal(err)
				}
				env = append(env, "FAKE_OVER=yardr.dev/yardr/internal/dispatch")
				env = append(env, "TMPDIR="+tmp)
				env = append(env, c.env...)
				timed := slices.Contains(c.env, "FAKE_SLEEP=2")
				if timed {
					env = append(env, "FAKE_TRACK=1")
				}
				start := time.Now()
				out, code := runEnv(t, dir, env,
					"packagebudget", "wave.txt", "go", "test", "-race", "./...")
				wall := time.Since(start)
				if code != c.code {
					t.Errorf("exit %d, output %q, want exit %d", code, out, c.code)
				}
				for _, want := range []string{cli, c.want, pack, store} {
					if !strings.Contains(out, want+"\n") {
						t.Errorf("output %q, want it to hold %q", out, want)
					}
				}
				// The verdicts print in sorted order, whatever order the
				// leg ran in: cli, dispatch, pack, store.
				last := -1
				for _, want := range []string{cli, c.want, pack, store} {
					i := strings.Index(out, want)
					if i < 0 || i < last {
						t.Errorf("output %q, want the verdicts in sorted order", out)
						break
					}
					last = i
				}
				// The floor keeps the overlap honest: without it a sleep
				// that never ran would still record its three-wide
				// maximum. The maximum below is what proves the re-runs
				// overlapped; the batch's wall proves nothing more, so
				// there is no ceiling for a loaded machine to miss.
				if timed {
					if wall < 2*time.Second {
						t.Errorf("the batch took %s, want at least one 2 s re-run: the sleep did not run", wall)
					}
				}
				if timed {
					max, err := os.ReadFile(filepath.Join(fake, "max"))
					if err != nil {
						t.Errorf("read maximum concurrent re-runs: %v", err)
					} else if got := strings.TrimSpace(string(max)); got != "3" {
						t.Errorf("maximum concurrent re-runs = %s, want 3", got)
					}
				}
				// Each package re-run once, with the leg's flags, inside
				// its hold; the journal's order is the scheduler's.
				calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
				got := strings.Split(strings.TrimSpace(string(calls)), "\n")
				slices.Sort(got)
				wantCalls := []string{
					"test -race -count=1 yardr.dev/yardr/internal/cli held=1",
					"test -race -count=1 yardr.dev/yardr/internal/dispatch held=1",
					"test -race -count=1 yardr.dev/yardr/internal/pack held=1",
					"test -race -count=1 yardr.dev/yardr/internal/store held=1",
				}
				if !slices.Equal(got, wantCalls) {
					t.Errorf("go was run as:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantCalls, "\n"))
				}
			})
		}
	})

	t.Run("a re-run that prints no seconds fails the run", func(t *testing.T) {
		t.Parallel()
		// A re-run that prints no seconds, cached where the leg's run was
		// over budget, clears nothing: the leg saw an over-budget package
		// the re-run did not clear.
		dir := t.TempDir()
		write(t, dir, "cached.txt", "ok  \tyardr.dev/yardr/internal/dispatch\t61.2s\n")
		_, env := r1go(t)
		out, code := runEnv(t, dir, append(env, "FAKE_CACHED=1"),
			"packagebudget", "cached.txt", "go", "test", "-race", "./...")
		if code != 1 {
			t.Fatalf("exit %d, output %q, want exit 1", code, out)
		}
		want := "test: yardr.dev/yardr/internal/dispatch took 61.2 s with -race, over the 60 s budget (docs/testing.md R1), and its re-run alone failed"
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output %q, want it to hold %q", out, want)
		}
	})

	t.Run("a leg with nothing over budget runs nothing again", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		write(t, dir, "green.txt",
			"ok  \tyardr.dev/yardr/internal/cli\t51.2s\n"+
				"ok  \tyardr.dev/yardr/internal/web\t32.4s\n"+
				"ok  \tyardr.dev/yardr/contrib/isolated-yard\t118.3s\n"+
				"ok  \tyardr.dev/yardr/internal/store\t55.0s\n"+
				"ok  \tyardr.dev/yardr\t31.2s\n"+
				"ok  \tyardr.dev/yardr/internal/pack\t21.0s\n")
		fake, env := r1go(t)
		if out, code := runEnv(t, dir, env, "packagebudget", "green.txt", "go", "test", "-race", "./..."); code != 0 || out != "" {
			t.Errorf("exit %d, output %q, want exit 0 and no output", code, out)
		}
		if calls, _ := os.ReadFile(filepath.Join(fake, "calls")); len(calls) != 0 {
			t.Errorf("go was run as:\n%s\nwant no re-run", calls)
		}
	})

	t.Run("allow-listed packages are there", func(t *testing.T) {
		t.Parallel()
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

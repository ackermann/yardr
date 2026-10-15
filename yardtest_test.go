package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
// plan, and the race lock the run's own race leg holds. A case sets what it
// means the script to have: one that runs the script sets YARDR_RACE_HELD,
// so its race step shares the hold instead of taking the machine's lock,
// except the lock's own test, which takes a lock of its own.
func scriptEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"YARDR_BASE", "YARDR_LANDING", "YARDR_TEST_PLAN", "YARDR_TEST_RPC", "GOOS", "YARDR_RACE_HELD"}, k)
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
// same when nothing compiled changed, and is the whole suite otherwise.
func TestYardTestSelects(t *testing.T) {
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
	// One socket suite imports internal/rpc, as the three do in the
	// repository: touching rpc selects store as an importer, while nothing
	// imports internal/cli.
	write("internal/store/p.go", "package p\n\nimport _ \"m.test/m/internal/rpc\"\n")
	write("internal/rpc/p.go", "package p\n")
	// A package that embeds a file, one that imports it, and one alone.
	write("emb/p.go", "package emb\n\nimport _ \"embed\"\n\n//go:embed a.txt\nvar A string\n")
	write("emb/a.txt", "a\n")
	write("top/p.go", "package top\n\nimport _ \"m.test/m/emb\"\n")
	write("alone/p.go", "package p\n")
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
	// A landing (YARDR_LANDING=1): the first line says whether anything
	// compiled changed, and with what. When nothing did, the rest is a plain
	// run's; otherwise it is the whole suite and the rest is the socket
	// gate's line: the leg runs only when the plain selection holds store,
	// dispatch or bdp.
	const landing = pre + "YARDR_LANDING is set, and "
	const skipSocket = pre + "the plain selection holds neither store, dispatch nor bdp: skipping the socket suites"
	const runSocketStore = pre + "the plain selection holds store: running the socket suites beside the first run"
	const runSocketAll = pre + "the plain selection holds store dispatch bdp: running the socket suites beside the first run"
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
			landing + "packs/starter/roles/builder.md, which a package embeds, changed" + since + ": the whole suite", []string{skipSocket}},
		{"a landing of what no test reads", []string{".github/a.yml"},
			landing + "nothing compiled changed" + since, []string{
				pre + ".github/a.yml changed: no test reads it",
				pre + "no package changed since " + short + "; no tests to run"}},
		{"a landing of a go file", []string{"docs/web.md", "alone/p.go"},
			landing + "alone/p.go changed" + since + ": the whole suite", []string{skipSocket}},
		{"a landing of a test file", []string{"alone/p_test.go"},
			landing + "alone/p_test.go changed" + since + ": the whole suite", []string{skipSocket}},
		{"a landing of go.mod", []string{"go.mod", "docs/web.md"},
			landing + "go.mod changed" + since + ": the whole suite", []string{runSocketAll}},
		{"a landing of go.sum", []string{"go.sum"},
			landing + "go.sum changed" + since + ": the whole suite", []string{runSocketAll}},
		{"a landing of an embedded file", []string{"docs/web.md", "emb/a.txt"},
			landing + "emb/a.txt, which a package embeds, changed" + since + ": the whole suite", []string{skipSocket}},
		{"a landing of a file no row knows", []string{"docs/web.md", "contrib/x.txt"},
			landing + "contrib/x.txt, which is read by no known package, changed" + since + ": the whole suite", []string{runSocketAll}},
		// What embedded a file that is gone cannot be asked any more.
		{"a landing that deletes a file", []string{"-docs/web.md"},
			landing + "docs/web.md was deleted" + since + ": the whole suite", []string{skipSocket}},
		{"a landing of nothing", nil,
			landing + "nothing changed" + since + ": the whole suite", []string{skipSocket}},
		// A Go change enters the whole suite while its plain selection
		// excludes the socket suites: the gate skips the leg. A change to
		// internal/rpc selects store as an importer: the leg runs.
		{"a landing touching internal/cli skips the socket suites", []string{"internal/cli/p.go"},
			landing + "internal/cli/p.go changed" + since + ": the whole suite", []string{skipSocket}},
		{"a landing touching internal/rpc runs the socket suites", []string{"internal/rpc/p.go"},
			landing + "internal/rpc/p.go changed" + since + ": the whole suite", []string{runSocketStore}},
		{"a landing of an unknown file runs the socket suites", []string{"contrib/x.txt"},
			landing + "contrib/x.txt, which is read by no known package, changed" + since + ": the whole suite", []string{runSocketAll}},
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
		if want := []string{"go list -test ./...", "gofmt -l .", "go vet ./...", "go build ./...", "go test -race m.test/m"}; !slices.Equal(took, want) {
			t.Errorf("the steps the output times: %q, want %q", took, want)
		}
	})
	// Run, not planned, a landing over a change the socket suites cannot
	// reach starts no socket leg, and one over internal/rpc starts it
	// beside the first run. The go it runs is the real one for the list
	// of packages and writes down every other call.
	for _, c := range []struct {
		name    string
		changed string
		reason  string
		line    string
		calls   []string
		steps   []string
	}{
		{"a landing over internal/cli starts no socket leg", "internal/cli/p.go", "internal/cli/p.go changed", skipSocket,
			[]string{"vet ./...", "linux vet ./...", "test -race ./..."},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "go test -race ./..."}},
		{"a landing over internal/rpc starts the socket leg", "internal/rpc/p.go", "internal/rpc/p.go changed", runSocketStore,
			[]string{"vet ./...", "linux vet ./...", "test -race ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "go test -race ./...",
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"}},
		{"a landing over an unknown file starts the socket leg", "contrib/x.txt", "contrib/x.txt, which is read by no known package, changed", runSocketAll,
			[]string{"vet ./...", "linux vet ./...", "test -race ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"},
			[]string{"go list -test ./...", "gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "go test -race ./...",
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"}},
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
			if err != nil || first != landing+c.reason+since+": the whole suite" {
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

// The test command runs a go step once more when it failed because a file
// of Go's build cache was gone, and only then: a second failure stands, and
// a step that failed of itself is not run again. Both runs' output is
// printed, and a step that failed for good is followed by the lines that
// say what failed: a failing test's a second time. A landing runs the socket
// suites beside the tests, whether those are green or not; each of the two
// is run again by the same rule, either makes the run red, and the socket
// step's output follows the whole of the other's.
func TestYardTestRunsAgainWhenTheBuildCacheLostAFile(t *testing.T) {
	const notice = "the Go build cache lost a file under us; running again"
	const failing = "--- FAIL: TestX (0.00s)"
	landing := []string{"vet ./...", "linux vet ./...", "test -race ./...", "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"}
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
		// Landings without a comparable base (CI's shape): the socket
		// suites run whole, ungated, so these cases exercise the leg
		// itself rather than the gate.
		{"landing: vet once", "vet", "1", cacheLost, true,
			[]string{landing[0], landing[0], landing[1], landing[2], landing[3]}, 1, true, noBase, ""},
		{"landing: a failing test", "test", "1", failing, false,
			landing, 2, false, noBase, "test: FAILED: go test -race ./...\n" + failing},
		{"landing: a failing socket test", "rpc", "1", failing, false,
			landing, 2, false, noBase, "test: FAILED: env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/\n" + failing},
		{"landing: the socket step once", "rpc", "1", cacheLost, true,
			[]string{landing[0], landing[1], landing[2], landing[3], landing[3]}, 1, true, noBase, ""},
		{"landing: the socket step twice", "rpc", "2", cacheLost, false,
			[]string{landing[0], landing[1], landing[2], landing[3], landing[3]}, 2, true, noBase, "test: FAILED: env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"},
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
// lines. The plain run does the same as a landing.
func TestYardTestRerunsFailedPackagesOnce(t *testing.T) {
	const pkg = "m.test/m"
	const test = "TestX"
	flakyFail := "--- FAIL: " + test + " (0.00s)\nFAIL\nFAIL\t" + pkg + " 0.123s"
	flaky := "test: flaky: " + test + " (" + pkg + ") passed on rerun"
	running := "test: running the failed packages again: " + pkg
	landing := []string{"vet ./...", "linux vet ./...", "test -race ./...", "test -race " + pkg, "rpc test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"}
	plain := []string{"vet ./...", "build ./...", "test -race ./...", "test -race " + pkg}
	noBase := []string{"YARDR_LANDING=1", "YARDR_BASE=nowhere"}
	for _, c := range []struct {
		name  string
		times string // how often the test step fails: once (flaky) or twice
		ok    bool
		calls []string
		env   []string // variables of the script's environment beyond the base
		steps []string
	}{
		{"plain: passes on rerun", "1", true, plain, nil,
			[]string{"gofmt -l .", "go vet ./...", "go build ./...", "go test -race ./..."}},
		{"plain: fails twice", "2", false, plain, nil,
			[]string{"gofmt -l .", "go vet ./...", "go build ./...", "go test -race ./..."}},
		// Landings without a comparable base (CI's shape): the socket
		// suites run whole, ungated, so these cases exercise the leg
		// itself rather than the gate.
		{"landing: passes on rerun", "1", true, landing, noBase,
			[]string{"gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "go test -race ./...",
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"}},
		{"landing: fails twice", "2", false, landing, noBase,
			[]string{"gofmt -l .", "go vet ./...", "env GOOS=linux go vet ./...", "go test -race ./...",
				"env YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/"}},
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
			// The case's own variables come last, so a landing's nowhere
			// base wins over the commit below.
			env := scriptEnv()
			cmd.Env = append(env,
				"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
				"YARDR_BASE="+gitT(t, dir, "rev-parse", "HEAD"),
				"TMPDIR="+tmp, "FAKE="+fake, "FAKE_STEP=test", "FAKE_TIMES="+c.times, "FAKE_LINE="+flakyFail, "YARDR_RACE_HELD=1")
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
			// The rerun is said before it: the failed packages, again.
			if !strings.Contains(out, running+"\n") {
				t.Errorf("the output has no %q line", running)
			}
			if c.ok {
				// One flaky line names the test and its package.
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
				if strings.Contains(out, "test: FAILED") {
					t.Errorf("a step that passed on rerun is followed by a failure summary")
				}
			} else {
				if strings.Contains(out, "test: flaky:") {
					t.Errorf("a step that failed twice names a flake")
				}
				want := "test: FAILED: go test -race ./...\n--- FAIL: " + test
				if !strings.Contains(out, want) || !strings.Contains(out, "FAIL\t"+pkg) {
					t.Errorf("the output's summary of a failed step: want %q and a FAIL line for %s", want, pkg)
				}
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

// The race leg runs under one machine-wide lock: a gate whose race step
// starts while another holds the lock waits for it, holds it for the run,
// then releases it, and says how long it waited. The test holds the lock
// past the run's build step, its last step before the race leg, so the wait
// spans whole seconds however slowly the run started under load; its own
// home stands in for the yard's, so no real gate waits on it. Once the
// run's test step starts, the lock is the run's until it ends: the test
// cannot take it meanwhile, and can once the run has ended. vet and build
// run while the lock is held: only the race leg waits.
func TestYardTestRaceLegWaitsForTheLock(t *testing.T) {
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
		"go":    "#!/bin/sh\necho \"$*\" >>\"$FAKE/calls\"\nif [ \"$1\" = test ]; then sleep 2; fi\n",
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
	// three seconds past it, so the run is still waiting when the hold
	// ends however slowly it started, and the wait spans whole seconds.
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
	time.Sleep(3 * time.Second)
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	// The run's test step starts once it has the lock: from then until it
	// ends the lock is the run's, so the test cannot take it meanwhile.
	waitFor("test -race")
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Errorf("took the lock while the run's race step holds it")
	}
	<-done
	if runErr != nil {
		t.Fatalf("err %v, output:\n%s", runErr, out)
	}
	waits := regexp.MustCompile(`(?m)^test: waited \d+ s for the race lock$`).FindAll(out, -1)
	if len(waits) != 1 {
		t.Errorf("the output has %d wait lines, want 1:\n%s", len(waits), out)
	}
	calls, _ := os.ReadFile(callsPath)
	if got, want := strings.TrimSpace(string(calls)), "vet ./...\nbuild ./...\ntest -race ./..."; got != want {
		t.Errorf("go was run as:\n%s\nwant:\n%s", got, want)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("cannot take the lock after the run ended: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}

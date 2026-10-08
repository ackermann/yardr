package rigcheck

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Depot yardr's own light gate, as it is in this checkout. Its table of files
// that tests read by path is in a shell variable, reads='...'.
const checkLight = "../../.yardr/check-light"

// Depot yardr's test command, which is the light gate and, for a landing,
// the full one, until the gates go.
const yardTest = "../../.yardr/test"

type readsRow struct {
	path string
	pkgs []string // empty for a - row: no test reads the path
}

func checkLightRows(t *testing.T) (script string, rows []readsRow) {
	t.Helper()
	b, err := os.ReadFile(checkLight)
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(b), "\nreads='\n")
	if ok {
		table, _, ok = strings.Cut(table, "\n'\n")
	}
	if !ok {
		t.Fatalf("%s has no reads='...' table", checkLight)
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
func TestCheckLightRowsAreThere(t *testing.T) {
	_, rows := checkLightRows(t)
	for _, row := range rows {
		info, err := os.Stat(filepath.Join("../..", row.path))
		if err != nil {
			t.Errorf("row %s: %v", row.path, err)
		} else if info.IsDir() != strings.HasSuffix(row.path, "/") {
			t.Errorf("row %s: a directory's row ends in /, a file's does not", row.path)
		}
		for _, pkg := range row.pkgs {
			if tests, _ := filepath.Glob(filepath.Join("../..", pkg, "*_test.go")); len(tests) == 0 {
				t.Errorf("row %s: %s has no tests", row.path, pkg)
			}
		}
	}
}

// What the light gate selects, in a module that has the table's packages
// and nothing in them: a file a test reads by path runs that test's
// package, a file nobody is known to read runs everything, a file the table
// says no test reads runs nothing. YARDR_CHECK_PLAN makes the script say so and stop.
func TestCheckLightSelects(t *testing.T) {
	script, rows := checkLightRows(t)
	selects(t, script, rows, "light gate: ", ".yardr/check-light", false)
}

// Depot yardr's test command, .yardr/test: run plainly it selects as the
// light gate does, from the same table, after a line saying what it is
// compared with. YARDR_LANDING does not change what a plan says.
func TestYardTestSelects(t *testing.T) {
	light, rows := checkLightRows(t)
	b, err := os.ReadFile(yardTest)
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	table := func(s string) string {
		_, table, _ := strings.Cut(s, "\nreads='\n")
		table, _, _ = strings.Cut(table, "\n'\n")
		return table
	}
	if got, want := table(script), table(light); got == "" || got != want {
		t.Fatalf("the reads table of %s is not that of %s:\n%s\nwant:\n%s", yardTest, checkLight, got, want)
	}
	selects(t, script, rows, "test: ", ".yardr/test", true)
}

// The base .yardr/test compares with: YARDR_CHECK_BASE, else YARDR_BASE, else
// origin/main, and its first line says which. One it cannot find is "could
// not run" (exit 2), never "nothing changed".
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
		cmd.Env = append(slices.DeleteFunc(checkEnv(os.Environ()), func(kv string) bool {
			return strings.HasPrefix(kv, "YARDR_BASE=") || strings.HasPrefix(kv, "YARDR_LANDING=")
		}), "YARDR_CHECK_PLAN=1")
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
		{"YARDR_CHECK_BASE first", []string{BaseVar + "=main", "YARDR_BASE=nowhere"},
			"test: what changed since main (YARDR_CHECK_BASE), from " + short, 0},
		{"then YARDR_BASE", []string{"YARDR_BASE=other"},
			"test: what changed since other (YARDR_BASE), from " + short, 0},
		{"a landing's plan too", []string{"YARDR_BASE=other", "YARDR_LANDING=1"},
			"test: what changed since other (YARDR_BASE), from " + short, 0},
		{"a base that is not there", []string{"YARDR_BASE=nowhere"},
			"test: cannot compare with nowhere (YARDR_BASE): no such commit here, or none in common with HEAD", 2},
		// The fake module has no origin.
		{"origin/main when neither is set", nil,
			"test: cannot compare with origin/main (the default, neither YARDR_CHECK_BASE nor YARDR_BASE being set): no such commit here, or none in common with HEAD", 2},
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
}

// selects runs script, a copy of the light gate's selection that starts its
// lines with pre and calls itself name, through the cases of what changed.
// said is whether it first says what it is compared with.
func selects(t *testing.T, script string, rows []readsRow, pre, name string, said bool) {

	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go in PATH")
	}
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
	// A package that embeds a file, one that imports it, and one alone.
	write("emb/p.go", "package emb\n\nimport _ \"embed\"\n\n//go:embed a.txt\nvar A string\n")
	write("emb/a.txt", "a\n")
	write("top/p.go", "package top\n\nimport _ \"m.test/m/emb\"\n")
	write("alone/p.go", "package p\n")
	write("docs/web.md", "web\n")
	gitT(t, dir, "init", "-q", "-b", "main")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "base")
	base := gitT(t, dir, "rev-parse", "HEAD")
	short := gitT(t, dir, "rev-parse", "--short", "HEAD")

	plan := func(t *testing.T, script string, changed ...string) (string, error) {
		t.Helper()
		gitT(t, dir, "reset", "-q", "--hard", base)
		gitT(t, dir, "clean", "-qfd")
		for _, name := range changed {
			text := "changed\n"
			if strings.HasSuffix(name, ".go") {
				text = "package p\n\nvar Changed int\n"
			}
			write(name, text)
		}
		gitT(t, dir, "add", "-A")
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "YARDR_CHECK_PLAN=1", BaseVar+"="+base)
		b, err := cmd.CombinedOutput()
		out := strings.TrimSpace(string(b))
		if said {
			first, rest, _ := strings.Cut(out, "\n")
			if want := "test: what changed since " + base + " (YARDR_CHECK_BASE), from " + short; first != want {
				t.Errorf("first line %q, want %q", first, want)
			}
			out = rest
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
			tested + " m.test/m/internal/cli m.test/m/internal/pack"}},
		{"two files of the starter pack", []string{"packs/starter/roles/builder.md", "packs/starter/pack.yaml"}, []string{
			pre + "packs/starter/pack.yaml and 1 more under packs/starter/ changed: read by the tests of internal/pack internal/cli",
			tested + " m.test/m/internal/cli m.test/m/internal/pack"}},
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
		// A file's row is that file only: .yardr/check is not a prefix of
		// .yardr/check-light.
		{"the gate itself", []string{".yardr/check-light"}, []string{
			pre + ".yardr/check-light changed: read by the tests of internal/rigcheck",
			tested + " m.test/m/internal/rigcheck"}},
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
	// A row whose package is gone fails the gate, whatever changed.
	t.Run("a stale row", func(t *testing.T) {
		stale := strings.Replace(script, "\nreads='\n", "\nreads='\nnowhere/ internal/pack internal/gone\n", 1)
		out, err := plan(t, stale, "docs/web.md")
		if want := pre + "the row for nowhere/ in " + name + " names internal/gone, which is no package here"; err == nil || out != want {
			t.Errorf("err %v, output:\n%s\nwant:\n%s", err, out, want)
		}
	})
}

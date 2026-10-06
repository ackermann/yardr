package rigcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Rig yardr's own light gate, as it is in this checkout. Its table of files
// that tests read by path is in a shell variable, reads='...'.
const checkLight = "../../.yardr/check-light"

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
// package, a file nobody is known to read runs everything, docs run
// nothing. YARDR_CHECK_PLAN makes the script say so and stop.
func TestCheckLightSelects(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go in PATH")
	}
	script, rows := checkLightRows(t)
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
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	const tested = "light gate: testing, with -race, what changed, what imports it and what reads it:"
	for _, c := range []struct {
		name    string
		changed []string
		want    []string
	}{
		{"the starter pack", []string{"packs/starter/roles/builder.md"}, []string{
			"light gate: packs/starter/roles/builder.md changed: read by the tests of internal/pack internal/cli",
			tested + " m.test/m/internal/cli m.test/m/internal/pack"}},
		{"a role and its starter copy", []string{"roles/builder.md", "packs/starter/roles/builder.md", "packs/starter/pack.yaml"}, []string{
			"light gate: roles/builder.md changed: read by the tests of internal/pack",
			"light gate: packs/starter/pack.yaml and 1 more under packs/starter/ changed: read by the tests of internal/pack internal/cli",
			tested + " m.test/m/internal/cli m.test/m/internal/pack"}},
		{"another package's testdata", []string{"internal/hooks/testdata/github/ping.json"}, []string{
			"light gate: internal/hooks/testdata/github/ping.json changed: read by the tests of internal/hooks internal/dispatch",
			tested + " m.test/m/internal/dispatch m.test/m/internal/hooks"}},
		{"a file no row knows", []string{"contrib/x.txt", "docs/web.md"}, []string{
			"light gate: contrib/x.txt is read by no known package: testing everything"}},
		{"three files no row knows", []string{"contrib/x.txt", "alone/y.txt", "z"}, []string{
			"light gate: alone/y.txt (and 2 more) is read by no known package: testing everything"}},
		{"docs only", []string{"docs/web.md", "docs/new.md"}, []string{
			"light gate: docs/new.md and 1 more under docs/ changed: no test reads it",
			"light gate: no package changed since " + short + "; no tests to run"}},
		// A file's row is that file only: .yardr/check is not a prefix of
		// .yardr/check-light.
		{"the gate itself", []string{".yardr/check-light"}, []string{
			"light gate: .yardr/check-light changed: read by the tests of internal/rigcheck",
			tested + " m.test/m/internal/rigcheck"}},
		{"an embedded file", []string{"emb/a.txt"}, []string{
			tested + " m.test/m/emb m.test/m/top"}},
		{"a go file", []string{"alone/p.go", "docs/web.md"}, []string{
			"light gate: docs/web.md changed: no test reads it",
			tested + " m.test/m/alone"}},
		{"nothing", nil, []string{
			"light gate: no package changed since " + short + "; no tests to run"}},
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
		if want := "light gate: the row for nowhere/ in .yardr/check-light names internal/gone, which is no package here"; err == nil || out != want {
			t.Errorf("err %v, output:\n%s\nwant:\n%s", err, out, want)
		}
	})
}

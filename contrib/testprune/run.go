package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// testOutcome is what one alone run ended as. A failing run still leaves
// its coverage behind in the usual case, so the outcome marks the row,
// not the set.
const (
	outcomePass      = "pass"
	outcomeFail      = "fail"
	outcomeSkip      = "skip"
	outcomeNoProfile = "noprofile"
)

// testResult is one test's alone run: its wall time, its outcome, and the
// statements it covered.
type testResult struct {
	name       string
	seconds    float64
	outcome    string
	covered    map[block]bool
	statements int
	unique     int
	removed    bool
}

// run measures one package: it builds the test binary once, runs the
// whole suite for the baseline, then each top-level test alone with its
// own coverage profile, one at a time in one stream, and writes the
// Markdown and JSON reports into out.
func run(pkg, out string) error {
	dir, importPath, err := resolvePackage(pkg)
	if err != nil {
		return err
	}
	names, err := listTests(pkg)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "testprune-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	bin := filepath.Join(work, "test.bin")
	if err := buildTestBinary(pkg, importPath, bin); err != nil {
		return err
	}
	logf("package %s (%s): %d tests", pkg, importPath, len(names))

	full, fullSeconds, err := runSuite(dir, bin, filepath.Join(work, "full.cover"))
	if err != nil {
		return err
	}
	total := totalStatements(full)
	coveredAll := statements(coveredSet(full))
	logf("full suite: %.1f s, %d of %d statements (%.1f%%)",
		fullSeconds, coveredAll, total, pct(coveredAll, total))

	profDir := filepath.Join(work, "profiles")
	if err := os.MkdirAll(profDir, 0o755); err != nil {
		return err
	}
	results := make([]testResult, 0, len(names))
	for i, name := range names {
		r, err := runOne(dir, bin, name, filepath.Join(profDir, name+".cover"))
		if err != nil {
			return err
		}
		results = append(results, r)
		logf("[%d/%d] %s: %s %.2f s, %d statements",
			i+1, len(names), r.name, r.outcome, r.seconds, r.statements)
	}

	rep := analyze(pkg, importPath, results, total, coveredAll, fullSeconds)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	mdPath := filepath.Join(out, "testprune.md")
	jsPath := filepath.Join(out, "testprune.json")
	if err := writeReport(rep, mdPath, jsPath); err != nil {
		return err
	}
	fmt.Printf("testprune: %d tests, %d candidates (%.1f s of %.1f s), coverage %.1f%% -> %.1f%%\n",
		len(results), len(rep.removal), rep.removalSeconds, rep.testsSeconds,
		pct(rep.coveredAll, rep.total), pct(rep.coveredKept, rep.total))
	fmt.Printf("testprune: wrote %s and %s\n", mdPath, jsPath)
	return nil
}

// resolvePackage answers the directory the package's tests run in and the
// import path the coverage instrument counts under.
func resolvePackage(pkg string) (dir, importPath string, err error) {
	out, err := exec.Command("go", "list", "-e", "-f", "{{.Dir}}\n{{.ImportPath}}\n{{.TestGoFiles}} {{.XTestGoFiles}}", pkg).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("no package %s: %s", pkg, oneLine(out))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 || lines[0] == "" || lines[1] == "" {
		return "", "", fmt.Errorf("no package %s", pkg)
	}
	if strings.TrimSpace(lines[2]) == "[] []" || strings.TrimSpace(lines[2]) == "" {
		return "", "", fmt.Errorf("package %s has no test files", pkg)
	}
	return lines[0], lines[1], nil
}

var testListLine = regexp.MustCompile(`^Test\w*$`)

// listTests names the package's top-level tests. Subtests are not listed,
// so they count with their parent, which is the unit the report judges.
func listTests(pkg string) ([]string, error) {
	out, err := exec.Command("go", "test", "-list", `^Test`, pkg).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("cannot list the tests of %s: %s", pkg, oneLine(out))
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if testListLine.MatchString(line) {
			names = append(names, line)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("package %s has no top-level tests", pkg)
	}
	sort.Strings(names)
	return names, nil
}

// buildTestBinary compiles the package's tests once, instrumented for the
// package itself, ahead of every run.
func buildTestBinary(pkg, importPath, bin string) error {
	out, err := exec.Command("go", "test", "-c", "-cover", "-coverpkg="+importPath, "-o", bin, pkg).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cannot build the tests of %s: %s", pkg, oneLine(out))
	}
	return nil
}

// runSuite runs the whole suite once, for the baseline coverage every
// per-test number is read against.
func runSuite(dir, bin, cover string) (profile, float64, error) {
	start := time.Now()
	cmd := exec.Command(bin, "-test.count=1", "-test.coverprofile="+cover)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return profile{}, 0, fmt.Errorf("the full suite does not pass on its own: %s", suiteFailure(out))
	}
	p, err := parseCoverProfile(cover)
	if err != nil {
		return profile{}, 0, err
	}
	return p, time.Since(start).Seconds(), nil
}

// runOne runs one test alone with its own profile, in the package's
// directory as go test does, and times the wall clock around it.
func runOne(dir, bin, name, cover string) (testResult, error) {
	r := testResult{name: name, covered: map[block]bool{}}
	start := time.Now()
	cmd := exec.Command(bin, "-test.run=^"+name+"$", "-test.count=1", "-test.v", "-test.coverprofile="+cover)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	r.seconds = time.Since(start).Seconds()
	r.outcome = verdict(name, out, runErr)
	p, err := parseCoverProfile(cover)
	if err != nil {
		// No usable profile: the run crashed before it wrote one, or
		// wrote nothing to write. The test stays in the report with its
		// time, but out of the sets, so it can neither accuse another
		// test nor be accused.
		r.outcome = outcomeNoProfile
		return r, nil
	}
	r.covered = coveredSet(p)
	r.statements = statements(r.covered)
	return r, nil
}

// verdict reads one alone run's outcome the way burn does: exit 0 and the
// test's own --- PASS line is a pass; a name that matched nothing and a
// test that skipped are not. The top-level verdict starts at the line's
// start; a subtest's is indented under it.
func verdict(name string, out []byte, runErr error) string {
	passed, skipped := false, false
	for _, line := range bytes.Split(out, []byte("\n")) {
		switch {
		case bytes.HasPrefix(line, []byte("--- PASS: "+name+" ")),
			bytes.Equal(line, []byte("--- PASS: "+name)):
			passed = true
		case bytes.HasPrefix(line, []byte("--- SKIP: "+name+" ")),
			bytes.Equal(line, []byte("--- SKIP: "+name)):
			skipped = true
		}
	}
	switch {
	case runErr == nil && passed:
		return outcomePass
	case skipped:
		return outcomeSkip
	default:
		return outcomeFail
	}
}

// suiteFailure keeps the first lines that say what failed, for the error
// when the baseline run itself is red.
func suiteFailure(out []byte) string {
	var kept []string
	for _, line := range bytes.Split(out, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("--- FAIL")) ||
			bytes.Contains(line, []byte("_test.go:")) ||
			bytes.HasPrefix(line, []byte("FAIL")) {
			kept = append(kept, string(line))
		}
		if len(kept) >= 10 {
			break
		}
	}
	if len(kept) == 0 {
		return oneLine(out)
	}
	return strings.Join(kept, "; ")
}

func oneLine(out []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if line == "" {
		return "no output"
	}
	return line
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "testprune: "+format+"\n", args...)
}

func goVersion() string {
	return runtime.Version()
}

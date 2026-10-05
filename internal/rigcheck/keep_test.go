package rigcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

func commitPattern(t *testing.T, dir, pattern string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FailPatternPath), []byte(pattern), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", FailPatternPath)
	gitT(t, dir, "commit", "-q", "-m", "pattern")
}

// failsEarly is a check that says what failed and then prints after more
// lines, so that no tail of a sensible length holds the failure.
func failsEarly(after int) string {
	return fmt.Sprintf("#!/bin/sh\necho start\necho '--- FAIL: TestIt (0.00s)'\necho '    it_test.go:7: got 1, want 2'\n"+
		"i=0; while [ $i -lt %d ]; do i=$((i+1)); echo \"ok pkg$i\"; done\necho FAIL\nexit 1\n", after)
}

// A failure 500 lines above the end is not in the tail: the rig's pattern
// finds it, with the lines around it, and the kept file holds all of it.
func TestFailureAboveTheTailIsMatchedAndKept(t *testing.T) {
	ctx := context.Background()
	rig, wt := rigWithBead(t, failsEarly(500))
	commitPattern(t, rig.Path, "# go\n--- FAIL\n^FAIL\n")
	// The bead's own pattern is not read, as its own gate is not.
	commitPattern(t, wt, "^ok\n")

	res, err := Run(ctx, rig, wt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass || Lines(res.Output) != 504 {
		t.Fatalf("pass=%v, %d lines of output", res.Pass, Lines(res.Output))
	}
	if tail := Tail(res.Output, 200); strings.Contains(tail, "TestIt") {
		t.Fatal("the failure is in the tail: the test proves nothing")
	}
	want := "-- line 1\nstart\n--- FAIL: TestIt (0.00s)\n    it_test.go:7: got 1, want 2\n"
	if !strings.HasPrefix(res.Matched, want) || !strings.HasSuffix(res.Matched, "\n-- line 502\nok pkg499\nok pkg500\nFAIL") || res.MatchedMore != 0 {
		t.Fatalf("matched (%d more):\n%s", res.MatchedMore, res.Matched)
	}
	if n := Lines(res.Matched); n != 2+MatchAfter+1+1+3 {
		t.Fatalf("matched has %d lines:\n%s", n, res.Matched)
	}

	home := t.TempDir()
	file, err := Keep(home, res, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "gates", "r", "b1-20261005T120000Z.log"); file != want {
		t.Fatalf("kept in %s, want %s", file, want)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != res.Output {
		t.Fatalf("the file does not hold the whole output: %v, %d bytes of %d", err, len(b), len(res.Output))
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	// A second run in the same second keeps its own; the light gate's is
	// named for what it is.
	res.Light = true
	second, err := Keep(home, res, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if err != nil || filepath.Base(second) != "b1-20261005T120000Z.light.log" {
		t.Fatalf("light gate kept in %s, %v", second, err)
	}
	if third, err := Keep(home, res, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)); err != nil || filepath.Base(third) != "b1-20261005T120000Z-2.light.log" {
		t.Fatalf("same second kept in %s, %v", third, err)
	}
	// Without a home there is nowhere to keep it, which is no error.
	if file, err := Keep("", res, time.Now()); file != "" || err != nil {
		t.Fatalf("no home: %q, %v", file, err)
	}
}

// A rig without a pattern has no matched lines, and a passing check none
// either, whatever it printed.
func TestNoPatternNoMatch(t *testing.T) {
	ctx := context.Background()
	rig, wt := rigWithBead(t, failsEarly(10))
	res, err := Run(ctx, rig, wt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass || res.Matched != "" || len(res.PatternNotes) != 0 {
		t.Fatalf("no pattern: pass=%v matched %q notes %v", res.Pass, res.Matched, res.PatternNotes)
	}

	rig, wt = rigWithBead(t, "#!/bin/sh\necho '--- FAIL: but exits 0'\n")
	commitPattern(t, rig.Path, "--- FAIL\n")
	if res, err = Run(ctx, rig, wt); err != nil || !res.Pass || res.Matched != "" {
		t.Fatalf("passing check: %+v, %v", res, err)
	}
}

// The pattern is the repository's under a store override too (at the base
// as it is locally), and a dir rig's is in its directory. A line that is no
// regular expression is named and skipped; the others count.
func TestPatternSources(t *testing.T) {
	ctx := context.Background()
	rig, wt := rigWithBead(t, "")
	commitPattern(t, rig.Path, "(\nboom\n")
	rig.Check, rig.CheckLight = "echo boom; echo after; exit 1", "echo light boom; exit 1"
	for _, run := range []func(context.Context, store.Rig, string) (*Result, error){Run, RunLight} {
		res, err := run(ctx, rig, wt)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Matched, "boom") || len(res.PatternNotes) != 1 ||
			!strings.Contains(res.PatternNotes[0], FailPatternPath+" line 1 is not a regular expression") {
			t.Fatalf("store override (light %v): matched %q notes %v", res.Light, res.Matched, res.PatternNotes)
		}
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{RepoPath: "echo one; echo boom; exit 1\n", FailPatternPath: "boom\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Run(ctx, store.Rig{Name: "d", Kind: store.RigDir, Path: dir}, "")
	if err != nil || res.Matched != "-- line 1\none\nboom" {
		t.Fatalf("dir rig: %+v, %v", res, err)
	}
}

// Matching shows every line once: stretches that touch are one, a match
// already shown opens no stretch of its own, and past MatchMax lines it
// counts what it left out.
func TestMatching(t *testing.T) {
	pats, notes := ParsePatterns([]byte("  ^FAIL  \n\n# not this\npanic:\n"))
	if len(pats) != 2 || len(notes) != 0 {
		t.Fatalf("patterns %v notes %v", pats, notes)
	}
	if got, more := Matching("a\nb\n", pats); got != "" || more != 0 {
		t.Fatalf("no match: %q, %d", got, more)
	}
	if got, _ := Matching("FAIL x\n", nil); got != "" {
		t.Fatalf("no pattern: %q", got)
	}

	var lines []string
	for i := 1; i <= 60; i++ {
		lines = append(lines, fmt.Sprintf("l%d", i))
	}
	lines[9] = "FAIL one"  // line 10
	lines[14] = "FAIL two" // line 15, inside the first's context
	lines[39] = "panic: x" // line 40
	lines[54] = "panic: x" // line 55, the same text: shown already
	lines[58] = "not FAIL" // ^FAIL does not match it
	got, more := Matching(strings.Join(lines, "\n")+"\n", pats)
	want := "-- line 8\n" + strings.Join(lines[7:15+MatchAfter], "\n") + "\n-- line 38\n" + strings.Join(lines[37:40+MatchAfter], "\n")
	if got != want || more != 0 {
		t.Fatalf("matching (%d more):\n%s\nwant:\n%s", more, got, want)
	}

	// Distinct failures, far apart: the bound holds, and the rest is counted.
	lines = nil
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("FAIL %d", i))
		for j := 0; j < 20; j++ {
			lines = append(lines, "ok")
		}
	}
	got, more = Matching(strings.Join(lines, "\n"), pats)
	body := slices.DeleteFunc(strings.Split(got, "\n"), func(s string) bool { return strings.HasPrefix(s, "-- line ") })
	shown := strings.Count(got, "FAIL ")
	if len(body) > MatchMax || len(body) < MatchMax-MatchBefore-MatchAfter || shown+more != 40 || more == 0 {
		t.Fatalf("%d lines (max %d), %d failures shown, %d more", len(body), MatchMax, shown, more)
	}
}

// Kept outputs are bounded: per directory the check ran in the newest
// KeepPerDir stay, and nothing older than KeepFor, whichever rig or bead.
func TestKeepIsBounded(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	keep := func(rig, dir string, at time.Time) string {
		t.Helper()
		file, err := Keep(home, &Result{Rig: rig, Dir: filepath.Join("/wt", dir), Output: "x\n"}, at)
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Base(file)
	}
	names := func(rig string) []string {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(home, GatesDir, rig))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}

	old := keep("r", "r-old", now.Add(-KeepFor-time.Hour))
	other := keep("r", "r-2", now.Add(-time.Hour))
	elsewhere := keep("s", "r-1", now.Add(-KeepFor-time.Hour))
	// Not a kept output: never removed.
	stray := filepath.Join(home, GatesDir, "r", "notes.txt")
	if err := os.WriteFile(stray, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var mine []string
	for i := 0; i < KeepPerDir+3; i++ {
		mine = append(mine, keep("r", "r-1", now.Add(time.Duration(i)*time.Minute)))
	}
	got := names("r")
	want := append([]string{"notes.txt"}, mine[3:]...)
	want = append(want, other)
	slices.Sort(want)
	if !strings.HasPrefix(mine[0], "r-1-2026") {
		t.Fatalf("a bead's output is kept as %s", mine[0])
	}
	if !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v (the newest %d of r-1, r-2's, not %s)", got, want, KeepPerDir, old)
	}
	// Another rig's are not this run's to remove.
	if got := names("s"); !slices.Equal(got, []string{elsewhere}) {
		t.Fatalf("rig s: %v", got)
	}
	// A name that is no file name stays inside the rig's directory.
	if file := keep("../x", "..", now); file != "_-20261005T120000Z.log" || len(names("__x")) != 1 {
		t.Fatalf("unsafe names kept as %s", file)
	}
}

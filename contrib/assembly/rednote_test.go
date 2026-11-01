package assembly

import (
	"context"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// failsEarly is a test command that says what failed and then prints 500
// more lines: more than the 200 the scripts put in a note.
const failsEarly = `echo '--- FAIL: TestIt (0.00s)'; echo '    it_test.go:7: got 1, want 2'; i=0; while [ $i -lt 500 ]; do i=$((i+1)); echo "ok pkg$i"; done; exit 1`

// failsEarlyGradle is a Gradle-style gate: it says only how many failed,
// writes which test failed to TEST-*.xml, and prints 500 more lines.
const failsEarlyGradle = `mkdir -p app/build/test-results/testDebugUnitTest; cat > app/build/test-results/testDebugUnitTest/TEST-com.example.Calc.xml <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<testsuite name="com.example.Calc" tests="109" failures="1" errors="0">
  <testcase classname="com.example.Calc" name="additionWorks" time="0.001"/>
  <testcase classname="com.example.Calc" name="divisionByZero" time="0.002">
    <failure message="expected 0, got panic">stack</failure>
  </testcase>
</testsuite>
EOF
echo '109 tests completed, 1 failed'; i=0; while [ $i -lt 500 ]; do i=$((i+1)); echo "ok pkg$i"; done; exit 1`

// failsEarlyXCTest is an XCTest-style gate: xcodebuild's failing line,
// then 500 more lines.
const failsEarlyXCTest = `echo "Test Case '-[CalcTests testDivisionByZero]' failed (0.001 seconds)"; i=0; while [ $i -lt 500 ]; do i=$((i+1)); echo "ok pkg$i"; done; exit 1`

// attachedFile returns the bytes of the one file the bead's notes carry,
// failing the test unless there is exactly one: the full log of a red run.
func attachedFile(t *testing.T, y *yard, id string) string {
	t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	notes, err := st.Notes(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, n := range notes {
		for _, f := range n.Files {
			hashes = append(hashes, f.Hash)
		}
	}
	if len(hashes) != 1 {
		t.Fatalf("bead %s notes carry %d files, want the one full log", id, len(hashes))
	}
	b, err := os.ReadFile(store.BlobPath(y.home, hashes[0]))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// namesAheadOfTail fails the test unless want stands in end ahead of the
// tail's first line: the failing test a red note keeps above its last 200
// lines.
func namesAheadOfTail(t *testing.T, what, end, want string) {
	t.Helper()
	failAt, tailAt := strings.Index(end, want), strings.Index(end, "ok pkg301\n")
	if failAt < 0 || tailAt < failAt {
		t.Fatalf("%s does not name %q ahead of the tail (at %d, %d):\n%s", what, want, failAt, tailAt, end)
	}
}

// noteTexts returns the raw text of each of the bead's notes, in order:
// what the scripts wrote, without the rendering's indent.
func noteTexts(t *testing.T, y *yard, id string) []string {
	t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	notes, err := st.Notes(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range notes {
		out = append(out, n.Text)
	}
	return out
}

// A red run of the depot's test command, longer than a note holds: the note
// a builder reads with yardr prime leads with the branch, the commit, the
// base and what ran, says how much of the output it has, names the failing
// test ahead of the tail, and carries its last 200 lines. The full output
// rides beside the note as an attached file. What matters of a long run is
// the test command's to print at its end.
func TestMergeLocalRedTestsNoteHasTheTail(t *testing.T) {
	y := newYard(t)
	y.tests(failsEarly)
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	short := y.git(y.repo, "rev-parse", "--short", "yard/r-1")
	notes := y.notes()
	lead := strings.Index(notes, "assembly: the tests failed on yard/r-1 at "+short+" rebased onto main (the command ")
	tail := strings.Index(notes, "exit 1); the last 200 of 502 lines of output:\n")
	if lead < 0 || tail < lead {
		t.Fatalf("want the commit and what ran, then the tail (at %d, %d):\n%s", lead, tail, notes)
	}
	// The command itself, in the note's lead, has the failing test's name.
	if end := notes[tail:]; !strings.Contains(end, "ok pkg301\n") || strings.Contains(end, "ok pkg300\n") || strings.Contains(end, "it_test.go:7") {
		t.Fatalf("the tail is not the last 200 lines:\n%s", end)
	}
	namesAheadOfTail(t, "the red note", notes[tail:], "--- FAIL: TestIt (0.00s)")
	if log := attachedFile(t, y, "r-1"); strings.Count(log, "\n") != 502 {
		t.Fatalf("the attached log has %d lines, want all 502", strings.Count(log, "\n"))
	} else if !strings.Contains(log, "--- FAIL: TestIt (0.00s)") || !strings.Contains(log, "ok pkg1\n") || !strings.Contains(log, "ok pkg500\n") {
		t.Fatalf("the attached log is not the full output:\n%.500s", log)
	}
	if shown := y.yardr("bead", "show", "r-1"); !strings.Contains(shown, "◆ 1 file: ") || !strings.Contains(shown, "full gate output of the failed run") {
		t.Fatalf("bead show names no attached full log:\n%s", shown)
	}
}

// A Gradle-style gate says only how many failed; the failing test is named
// from the TEST-*.xml files the run left in the worktree, ahead of the
// tail, and the passing tests are not named.
func TestMergeLocalRedTestsNoteNamesGradleFailure(t *testing.T) {
	y := newYard(t)
	y.tests(failsEarlyGradle)
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	notes := y.notes()
	tail := strings.Index(notes, "exit 1); the last 200 of 501 lines of output:\n")
	if tail < 0 {
		t.Fatalf("no tail in the red note:\n%s", notes)
	}
	end := notes[tail:]
	namesAheadOfTail(t, "the red note", end, "com.example.Calc.divisionByZero")
	if strings.Contains(end, "com.example.Calc.additionWorks") {
		t.Fatalf("a passing Gradle test is named as failing:\n%s", end)
	}
}

// An XCTest-style gate's failing line is named ahead of the tail.
func TestMergeLocalRedTestsNoteNamesXCTestFailure(t *testing.T) {
	y := newYard(t)
	y.tests(failsEarlyXCTest)
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	notes := y.notes()
	tail := strings.Index(notes, "exit 1); the last 200 of 501 lines of output:\n")
	if tail < 0 {
		t.Fatalf("no tail in the red note:\n%s", notes)
	}
	namesAheadOfTail(t, "the red note", notes[tail:], "Test Case '-[CalcTests testDivisionByZero]' failed")
}

// A run ended by a signal names the failing tests too: in the first run's
// note, and in both runs of the hold note when a signal ends the second run
// as well, with the second run's full log attached.
func TestMergeLocalSignalPathsNameFailingTests(t *testing.T) {
	y := newYard(t)
	y.tests(`echo '--- FAIL: TestIt (0.00s)'; i=0; while [ $i -lt 500 ]; do i=$((i+1)); echo "ok pkg$i"; done; exit 143`)
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	if b := y.bead(); !b.Hold || b.Stage != "approved" {
		t.Fatalf("bead %s hold=%v, want held in approved", b.Stage, b.Hold)
	}
	texts := noteTexts(t, y, "r-1")
	first, hold := "", ""
	for _, text := range texts {
		if strings.Contains(text, "running them once more on the same commit") {
			first = text
		}
		if strings.Contains(text, "and again by signal 15 (exit 143) when run once more") {
			hold = text
		}
	}
	if first == "" || hold == "" {
		t.Fatalf("want the first run's note and the hold note:\n%s", strings.Join(texts, "\n---\n"))
	}
	for _, tc := range []struct {
		what  string
		text  string
		wants []string
	}{
		{"the first run's note", first, []string{
			"Of the first run, the last 200 of 501 lines of output:\n--- FAIL: TestIt (0.00s)\nok pkg301\n",
		}},
		{"the hold note", hold, []string{
			"First run:\n--- FAIL: TestIt (0.00s)\nok pkg301\n",
			"Second run:\n--- FAIL: TestIt (0.00s)\nok pkg301\n",
		}},
	} {
		for _, want := range tc.wants {
			if !strings.Contains(tc.text, want) {
				t.Fatalf("%s does not name the failing test ahead of the tail:\n%s", tc.what, tc.text)
			}
		}
	}
	if log := attachedFile(t, y, "r-1"); strings.Count(log, "\n") != 501 {
		t.Fatalf("the attached log has %d lines, want the second run's 501", strings.Count(log, "\n"))
	}
}

// A green landing attaches nothing and notes no names: the full log rides
// red runs alone.
func TestMergeLocalGreenAttachesNothing(t *testing.T) {
	y := newYard(t)
	y.tests("true")
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if shown := y.yardr("bead", "show", "r-1"); strings.Contains(shown, "◆") || strings.Contains(shown, "full gate output") {
		t.Fatalf("a green landing attached its log:\n%s", shown)
	}
}

// pr-ci's red note names the failing test ahead of the tail, and the full
// output rides beside the note.
func TestPRCIRedTestsNoteNamesFailingTests(t *testing.T) {
	c := newCIYard(t)
	const num = 40
	sha := c.pull(num, map[string]string{"feature.txt": "new\n"})
	const id = "r-j40"
	c.prBeadIn(id, prURLn(num), "ciwait", false)
	c.reviewed(id, sha)
	c.tests(failsEarly)
	c.ci(id, mergeOpts{head: sha}, map[string]string{})

	c.has(id, "red", "contributor", false)
	notes := c.notesOf(id)
	tail := strings.Index(notes, "exit 1); the last 200 of 502 lines of output:\n")
	if tail < 0 {
		t.Fatalf("no tail in the red note:\n%s", notes)
	}
	namesAheadOfTail(t, "the red note", notes[tail:], "--- FAIL: TestIt (0.00s)")
	if log := attachedFile(t, c.yard, id); strings.Count(log, "\n") != 502 {
		t.Fatalf("the attached log has %d lines, want all 502", strings.Count(log, "\n"))
	}
	c.clean(id, "red")
}

// The train's red run: the note on the train and the body of the child added
// to fix it both lead with the commit and what ran, say how much of the
// output they have, name the failing test ahead of the tail, and carry its
// last 200 lines. The full output rides beside the train's note.
func TestTrainRedTestsFixChildHasTheTail(t *testing.T) {
	y := landedTrainYard(t, "true")
	y.tests(failsEarly)

	y.exec("integrate-train", "yard-integrators", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open, not held", e.Stage, e.Hold)
	}
	at := "yard/r-e at " + y.git(y.repo, "rev-parse", "--short", "yard/r-e") + " rebased onto main (the command "
	const shown = "exit 1); the last 200 of 502 lines of output:\n"
	notes := y.notesOf("r-e")
	if lead, tail := strings.Index(notes, "integrator: the tests failed on "+at), strings.Index(notes, shown); lead < 0 || tail < lead {
		t.Fatalf("the train's note: want the commit and what ran, then the tail (at %d, %d):\n%s", lead, tail, notes)
	}
	namesAheadOfTail(t, "the train's note", notes[strings.Index(notes, shown):], "--- FAIL: TestIt (0.00s)")
	f := y.familyOf("r-e")
	if len(f.Children) != 3 {
		t.Fatalf("train children %v, want r-1, r-2 and a fix", f.Children)
	}
	fix := y.beadOf(f.Children[2])
	lead := strings.Index(fix.Body, "The tests failed on train r-e's branch "+at)
	tail := strings.Index(fix.Body, "exit 1). Make them pass on yard/r-e. Of the run, the last 200 of 502 lines of output:\n")
	if fix.Title != "Fix the tests on train r-e" || lead != 0 || tail < lead {
		t.Fatalf("fix child %q: want the commit and what ran, then the tail:\n%s", fix.Title, fix.Body)
	}
	if end := fix.Body[tail:]; !strings.Contains(end, "ok pkg301\n") || strings.Contains(end, "ok pkg300\n") {
		t.Fatalf("the tail is not the last 200 lines:\n%s", end)
	}
	namesAheadOfTail(t, "the fix child's body", fix.Body[tail:], "--- FAIL: TestIt (0.00s)")
	if log := attachedFile(t, y, "r-e"); strings.Count(log, "\n") != 502 {
		t.Fatalf("the attached log has %d lines, want all 502", strings.Count(log, "\n"))
	}
	// What the fix child's builder is shown is the body, whole.
	if shown := y.yardr("prime", "--bead", f.Children[2]); !strings.Contains(shown, "ok pkg301") || !strings.Contains(shown, "ok pkg500") {
		t.Fatalf("prime cuts the fix child's body:\n%s", shown)
	}
}

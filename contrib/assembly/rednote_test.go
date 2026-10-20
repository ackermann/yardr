package assembly

import (
	"strings"
	"testing"
)

// failsEarly is a test command that says what failed and then prints 500
// more lines: more than the 200 the scripts put in a note.
const failsEarly = `echo '--- FAIL: TestIt (0.00s)'; echo '    it_test.go:7: got 1, want 2'; i=0; while [ $i -lt 500 ]; do i=$((i+1)); echo "ok pkg$i"; done; exit 1`

// A red run of the depot's test command, longer than a note holds: the note
// a builder reads with yardr prime leads with the branch, the commit, the
// base and what ran, says how much of the output it has, and carries its
// last 200 lines. What matters of a long run is the test command's to print
// at its end.
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
}

// The train's red run: the note on the train and the body of the child added
// to fix it both lead with the commit and what ran, say how much of the
// output they have, and carry its last 200 lines.
func TestTrainRedTestsFixChildHasTheTail(t *testing.T) {
	y := newTrainYard(t, "true")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
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
	// What the fix child's builder is shown is the body, whole.
	if shown := y.yardr("prime", "--bead", f.Children[2]); !strings.Contains(shown, "ok pkg301") || !strings.Contains(shown, "ok pkg500") {
		t.Fatalf("prime cuts the fix child's body:\n%s", shown)
	}
}

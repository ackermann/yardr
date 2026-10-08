package assembly

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// failsEarly is a gate, or a test command, that says what failed and then
// prints 500 more lines: more than the 200 the scripts put in a note.
const failsEarly = `echo '--- FAIL: TestIt (0.00s)'; echo '    it_test.go:7: got 1, want 2'; i=0; while [ $i -lt 500 ]; do i=$((i+1)); echo "ok pkg$i"; done; exit 1`

// pattern commits a .yardr/check-fail-pattern on main, where depot check
// reads it for integrate-train.
func (y *yard) pattern(text string) {
	y.t.Helper()
	if err := os.MkdirAll(filepath.Join(y.repo, ".yardr"), 0o755); err != nil {
		y.t.Fatal(err)
	}
	y.commit(y.repo, ".yardr/check-fail-pattern", text)
}

// wholeOutput finds the file text says the whole output is in, after lead,
// and checks that it holds all of the gate's output.
func (y *yard) wholeOutput(text, lead string) string {
	y.t.Helper()
	m := regexp.MustCompile(regexp.QuoteMeta(lead) + `; the whole output is in (\S+\.log)[:.]`).FindStringSubmatch(text)
	if m == nil {
		y.t.Fatalf("no %q naming the file:\n%s", lead, text)
	}
	b, err := os.ReadFile(m[1])
	if err != nil {
		y.t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "--- FAIL: TestIt (0.00s)\n") || strings.Count(string(b), "\n") != 502 {
		y.t.Fatalf("%s does not hold the whole output:\n%.300s", m[1], b)
	}
	if info, _ := os.Stat(m[1]); info.Mode().Perm() != 0o600 || !strings.HasPrefix(m[1], filepath.Join(y.home, "gates", "r")+string(filepath.Separator)) {
		y.t.Fatalf("%s: mode %v, want 0600 under the home's gates/r", m[1], info.Mode().Perm())
	}
	return m[1]
}

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

// The train's red gate: the note on the train and the body of the child added
// to fix it both lead with the commit and the file, then the matching
// lines, then the tail.
func TestTrainRedGateFixChildLeadsWithWhatFailed(t *testing.T) {
	y := newTrainYard(t, "true")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.gate(failsEarly)
	y.pattern("--- FAIL\n")

	y.exec("integrate-train", "yard-integrators", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open, not held", e.Stage, e.Hold)
	}
	at := "yard/r-e at " + y.git(y.repo, "rev-parse", "--short", "yard/r-e") + " rebased onto main"
	file := y.wholeOutput(y.yardr("prime", "--bead", "r-e"), "integrator: the gate failed on "+at)
	f := y.familyOf("r-e")
	if len(f.Children) != 3 {
		t.Fatalf("train children %v, want r-1, r-2 and a fix", f.Children)
	}
	body := y.beadOf(f.Children[2]).Body
	if got := y.wholeOutput(body, "The gate failed on train r-e's branch "+at); got != file {
		t.Fatalf("the fix child names %s, the train's note %s", got, file)
	}
	matched := strings.Index(body, "lines matching .yardr/check-fail-pattern")
	failure := strings.Index(body, "it_test.go:7: got 1, want 2")
	tail := strings.Index(body, "the last 200 lines:")
	if !(0 < matched && matched < failure && failure < tail) || !strings.Contains(body, "Make it pass on yard/r-e.") {
		t.Fatalf("fix child: want the matching lines, then the tail:\n%s", body)
	}
	// What the fix child's builder is shown is the body, whole.
	if shown := y.yardr("prime", "--bead", f.Children[2]); !strings.Contains(shown, file) || !strings.Contains(shown, "ok pkg500") {
		t.Fatalf("prime cuts the fix child's body:\n%s", shown)
	}
}

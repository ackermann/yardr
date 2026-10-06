package refinery

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// failsEarly is a gate that says what failed and then prints 500 more
// lines: more than the 200 the scripts put in a note.
const failsEarly = `echo '--- FAIL: TestIt (0.00s)'; echo '    it_test.go:7: got 1, want 2'; i=0; while [ $i -lt 500 ]; do i=$((i+1)); echo "ok pkg$i"; done; exit 1`

// pattern commits a .yardr/check-fail-pattern on main, where depot check
// reads it.
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

// A red gate whose failure is 500 lines above the end: the note a builder
// reads with yardr prime leads with the branch, the commit, the base and the
// file all of the output is in, then the lines the depot's pattern matches,
// then the tail. The tail alone would not hold the failure.
func TestMergeLocalRedGateNoteLeadsWithWhatFailed(t *testing.T) {
	y := newYard(t)
	y.gate(failsEarly)
	y.pattern("--- FAIL\n")
	tip := y.commit(y.wt, "b.txt", "bead\n")

	y.refinery()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	// The branch was rebased onto main, which the pattern's commit moved.
	if now := y.git(y.repo, "rev-parse", "yard/r-1"); now == tip {
		t.Fatal("the branch was not rebased: the note's commit proves nothing")
	}
	short := y.git(y.repo, "rev-parse", "--short", "yard/r-1")
	notes := y.notes()
	file := y.wholeOutput(notes, "refinery: the gate failed on yard/r-1 at "+short+" rebased onto main")
	if filepath.Base(file)[:4] != "r-1-" {
		t.Fatalf("kept as %s, want named after the bead", file)
	}
	lead := strings.Index(notes, "the whole output is in")
	matched := strings.Index(notes, "lines matching .yardr/check-fail-pattern, of 502 lines of output:")
	failure := strings.Index(notes, "it_test.go:7: got 1, want 2")
	tail := strings.Index(notes, "the last 200 lines:")
	if lead < 0 || !(lead < matched && matched < failure && failure < tail) {
		t.Fatalf("want the file, then the matching lines, then the tail (at %d, %d, %d, %d):\n%s", lead, matched, failure, tail, notes)
	}
	if !strings.Contains(notes[tail:], "ok pkg301\n") || strings.Contains(notes[tail:], "ok pkg300\n") || strings.Contains(notes[tail:], "TestIt") {
		t.Fatalf("the tail is not the last 200 lines:\n%s", notes[tail:])
	}
}

// A depot without a pattern: the file, and the tail as before.
func TestMergeLocalRedGateNoteWithoutAPattern(t *testing.T) {
	y := newYard(t)
	y.gate(failsEarly)
	y.commit(y.wt, "b.txt", "bead\n")

	y.refinery()

	short := y.git(y.repo, "rev-parse", "--short", "yard/r-1")
	notes := y.notes()
	y.wholeOutput(notes, "refinery: the gate failed on yard/r-1 at "+short+" rebased onto main")
	if strings.Contains(notes, "lines matching") || strings.Contains(notes, "it_test.go:7") ||
		!strings.Contains(notes, "ok pkg301\n") || strings.Contains(notes, "ok pkg300\n") {
		t.Fatalf("want the last 200 lines alone:\n%s", notes)
	}
}

// A car's red light gate is kept and noted the same way.
func TestMergeLocalCarRedLightGateIsKept(t *testing.T) {
	y := newTrainYard(t, "true")
	y.gates("true", failsEarly, true)
	y.pattern("--- FAIL\n")
	y.git(y.repo, "branch", "-f", "yard/r-e", "main")
	y.yardr("bead", "update", "r-1", "--type", "car")

	y.exec("merge-local", "refinery", "r-1")

	short := y.git(y.repo, "rev-parse", "--short", "yard/r-1")
	notes := y.yardr("prime", "--bead", "r-1")
	file := y.wholeOutput(notes, "refinery: the light gate failed on yard/r-1 at "+short+" rebased onto yard/r-e")
	if !strings.HasSuffix(file, ".light.log") || !strings.Contains(notes, "it_test.go:7: got 1, want 2") {
		t.Fatalf("light gate kept in %s; note:\n%s", file, notes)
	}
}

// The train's red gate: the note on the train and the body of the child added
// to fix it both lead with the commit and the file, then the matching
// lines, then the tail.
func TestTrainRedGateFixChildLeadsWithWhatFailed(t *testing.T) {
	y := newTrainYard(t, "true")
	y.exec("merge-local", "refinery", "r-1")
	y.exec("merge-local", "refinery", "r-2")
	y.gates(failsEarly, "", false)
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

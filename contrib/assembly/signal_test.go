package assembly

// A run of the tests ended by a signal from outside (exit 128+N) is not a
// red one: no builder can fix a signal. merge-local, with the depot's test
// command, run it once more on the
// same commit, and hold the bead if a signal ends it again. Seen as exit 143
// after every package had printed ok (yardr-z0bw).

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// tests makes test depot r's test command, which merge-local and
// integrate-train run, none when it is empty.
func (y *yard) tests(test string) {
	y.t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if err := st.PutDepot(context.Background(), store.Depot{Name: "r", Path: y.repo, Base: "main", Test: test}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// runs is how many times a command that appends a line to file has run.
func runs(t *testing.T, file string) int {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// The first run is killed by SIGTERM, the second passes: the bead lands as
// usual, and a note keeps the first run on record with its output.
func TestMergeLocalTestsEndedBySignalIsRunAgain(t *testing.T) {
	y := newYard(t)
	tip := y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.tests(`echo run >> ` + ran + `; [ "$(wc -l < ` + ran + `)" -gt 1 ] || { echo "ok every package"; kill -TERM $$; }`)

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" || b.Hold {
		t.Fatalf("bead %s/%s hold=%v, want merged and closed", b.Stage, b.Status, b.Hold)
	}
	if main := y.git(y.repo, "rev-parse", "main"); main != tip {
		t.Fatalf("main %s, want the tested commit %s", main, tip)
	}
	if n := runs(t, ran); n != 2 {
		t.Fatalf("the tests ran %d times, want 2", n)
	}
	notes := y.notes()
	for _, want := range []string{"the tests on yard/r-1 at " + tip[:7], "were ended by signal 15 (exit 143), not by a failing test",
		"running them once more on the same commit", "ok every package", "merged " + tip[:7]} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
}

// Ended by a signal both times (here the way a shell reports a killed
// command of its script, exit 143): held where it is with both outputs,
// never sent back to new, and not run a third time.
func TestMergeLocalTestsEndedBySignalTwiceHolds(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.tests(`echo run >> ` + ran + `; echo "output of run $(wc -l < ` + ran + ` | tr -d ' ')"; exit 143`)

	y.assembly()

	b := y.bead()
	if !b.Hold || b.Stage != "approved" || b.Status != "open" {
		t.Fatalf("bead %s/%s hold=%v, want held in approved", b.Stage, b.Status, b.Hold)
	}
	if n := runs(t, ran); n != 2 {
		t.Fatalf("the tests ran %d times, want 2", n)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on tests ended by a signal")
	}
	notes := y.notes()
	for _, want := range []string{"were ended by signal 15 (exit 143)", "and again by signal 15 (exit 143) when run once more",
		"First run:\n", "output of run 1", "Second run:\n", "output of run 2"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "the tests failed") {
		t.Fatalf("tests ended by a signal were reported as red:\n%s", notes)
	}
}

// The second run decides as any run does: red goes back to new, with the
// first run's note still on the bead.
func TestMergeLocalTestsEndedBySignalThenRed(t *testing.T) {
	y := newYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.tests(`echo run >> ` + ran + `; [ "$(wc -l < ` + ran + `)" -gt 1 ] || exit 143; echo "really red"; exit 1`)

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	notes := y.notes()
	for _, want := range []string{"were ended by signal 15 (exit 143)", "the tests failed", "really red"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
}

// The script's own kill at the deadline (YARDR_TEST_TIMEOUT) is not a signal
// from outside: the run is red, goes back to new and is not run again. What
// the command started is killed with it, here a sleep below a shell below
// the command's, which would otherwise outlive the landing.
func TestMergeLocalTestsDeadlineIsRed(t *testing.T) {
	y := newYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	ran, pids := filepath.Join(y.home, "ran"), filepath.Join(y.home, "pids")
	y.tests(`echo run >> ` + ran + `; echo started; sh -c 'sleep 300 & echo $! >` + pids + `; wait'`)
	y.opts = "YARDR_TEST_TIMEOUT=1"

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	if n := runs(t, ran); n != 1 {
		t.Fatalf("the tests ran %d times, want 1", n)
	}
	notes := y.notes()
	if !strings.Contains(notes, "the tests failed") || !strings.Contains(notes, "ran past their deadline of 1s and were killed") ||
		!strings.Contains(notes, "  started\n") || strings.Contains(notes, "ended by signal") {
		t.Fatalf("the deadline is not reported as a red run:\n%s", notes)
	}
	b, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	// Killed, it is gone once its parent, killed too, is reaped by init.
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the sleep the test command started (pid %d) outlived the deadline's kill", pid)
		}
	}
}

// The deadline counts only time not spent waiting for the race lock: a run
// that prints `test: waited N s` and then runs past LIMIT but within
// LIMIT+N lands, not killed. Here LIMIT is 2, the wait 10, and the run 4.
func TestMergeLocalTestsDeadlineExcludesRaceLockWait(t *testing.T) {
	y := newYard(t)
	tip := y.commit(y.wt, "b.txt", "bead\n")
	y.tests(`echo "test: waited 10 s for the race lock"; sleep 4`)
	y.opts = "YARDR_TEST_TIMEOUT=2"

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" || b.Hold {
		t.Fatalf("bead %s/%s hold=%v, want merged and closed", b.Stage, b.Status, b.Hold)
	}
	if main := y.git(y.repo, "rev-parse", "main"); main != tip {
		t.Fatalf("main %s, want the tested commit %s", main, tip)
	}
	if notes := y.notes(); strings.Contains(notes, "ran past their deadline") {
		t.Fatalf("a run within its deadline plus its wait was killed:\n%s", notes)
	}
}

// A lock wait longer than LIMIT is still excluded before the gate can print
// its final wait line. Here it marks the wait at the start, waits 4 seconds,
// then runs for 1 second: 5 seconds total is within LIMIT+the wait (7).
func TestMergeLocalTestsDeadlineExcludesRaceLockWaitLongerThanLimit(t *testing.T) {
	y := newYard(t)
	tip := y.commit(y.wt, "b.txt", "bead\n")
	y.tests(`echo "test: race lock wait started at $(date +%s)"; sleep 4; echo "test: waited 4 s for the race lock"; sleep 1`)
	y.opts = "YARDR_TEST_TIMEOUT=3"

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" || b.Hold {
		t.Fatalf("bead %s/%s hold=%v, want merged and closed", b.Stage, b.Status, b.Hold)
	}
	if main := y.git(y.repo, "rev-parse", "main"); main != tip {
		t.Fatalf("main %s, want the tested commit %s", main, tip)
	}
	if notes := y.notes(); strings.Contains(notes, "ran past their deadline") {
		t.Fatalf("a lock wait longer than the deadline was not credited:\n%s", notes)
	}
}

// Past LIMIT plus its waited seconds, the run is killed as before: here
// LIMIT is 2, the wait 2, and the run 10, killed at 4.
func TestMergeLocalTestsDeadlinePastRaceLockWaitIsRed(t *testing.T) {
	y := newYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.tests(`echo run >> ` + ran + `; echo "test: waited 2 s for the race lock"; sleep 10`)
	y.opts = "YARDR_TEST_TIMEOUT=2"

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	if n := runs(t, ran); n != 1 {
		t.Fatalf("the tests ran %d times, want 1", n)
	}
	notes := y.notes()
	if !strings.Contains(notes, "the tests failed") || !strings.Contains(notes, "ran past their deadline of 2s and were killed") {
		t.Fatalf("the deadline past its wait is not reported as a red run:\n%s", notes)
	}
}

// trainSignalYard is newTrainYard with both children landed, and tests that
// from then on are ended by SIGTERM: the first times times, 0 for always.
func trainSignalYard(t *testing.T, times int) (y *yard, ran string) {
	t.Helper()
	y = newTrainYard(t, "true")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	ran = filepath.Join(y.home, "ran")
	check := `echo run >> ` + ran + `; echo "output of run $(wc -l < ` + ran + ` | tr -d ' ')"; `
	if times > 0 {
		check += `[ "$(wc -l < ` + ran + `)" -gt ` + string(rune('0'+times)) + ` ] || `
	}
	y.tests(check + `kill -TERM $$`)
	return y, ran
}

// integrate-train runs the tests the same way: ended by a signal once, the
// train goes to review on the second run, with the first on record.
func TestTrainTestsEndedBySignalAreRunAgain(t *testing.T) {
	y, ran := trainSignalYard(t, 1)

	y.exec("integrate-train", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if e.Stage != "review" || e.Status != "open" || e.Hold {
		t.Fatalf("train %s/%s hold=%v, want review", e.Stage, e.Status, e.Hold)
	}
	if n := runs(t, ran); n != 2 {
		t.Fatalf("the tests ran %d times, want 2", n)
	}
	notes := y.notesOf("r-e")
	for _, want := range []string{"were ended by signal 15 (exit 143), not by a failing test", "output of run 1", "tests passed"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
}

// Ended by a signal twice, the train is held in open with both outputs, and
// no child is added to fix tests that were never red.
func TestTrainTestsEndedBySignalTwiceHold(t *testing.T) {
	y, ran := trainSignalYard(t, 0)

	y.exec("integrate-train", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if !e.Hold || e.Stage != "open" || e.Status != "open" {
		t.Fatalf("train %s/%s hold=%v, want held in open", e.Stage, e.Status, e.Hold)
	}
	if n := runs(t, ran); n != 2 {
		t.Fatalf("the tests ran %d times, want 2", n)
	}
	if f := y.familyOf("r-e"); len(f.Children) != 2 {
		t.Fatalf("train children %v, want only r-1 and r-2", f.Children)
	}
	notes := y.notesOf("r-e")
	for _, want := range []string{"and again by signal 15 (exit 143) when run once more", "First run:\n", "output of run 1", "Second run:\n", "output of run 2"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
}

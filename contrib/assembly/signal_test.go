package assembly

// A run of the tests ended by a signal from outside (exit 128+N) is not a
// red one: no builder can fix a signal. merge-local, with the depot's test
// command, run it once more on the
// same commit, and hold the bead if a signal ends it again. Seen as exit 143
// after every package had printed ok (yardr-z0bw).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/testload"
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
	for deadline := time.Now().Add(testload.Scale(5 * time.Second)); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
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
// LIMIT is 2, the wait 2, and the run 30, killed at 4. The run outlasts
// the kill by 26 s, not 6: under a pileup's load the watcher's spawns
// stall past a 10 s run, which then lands instead of going back to new
// (yardr-d6wj). A green run still ends at the kill, so the longer sleep
// costs it nothing, and a run that finishes fails the assertions below,
// well short of the exec fuse.
func TestMergeLocalTestsDeadlinePastRaceLockWaitIsRed(t *testing.T) {
	y := newYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.tests(`echo run >> ` + ran + `; echo "test: waited 2 s for the race lock"; sleep 30`)
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

// A race leg the gate throttled with -p runs slower by design, so the
// deadline follows it: cores over -p, floored at 1, times LIMIT. With
// -p=2 on 10 cores and LIMIT 2 the deadline is 10: a run of 4 lands, a
// run of 30 is killed with the scaled deadline in the notes, and without
// the line a run of 30 is killed at the plain 2. That run is 30, not the
// 4 of the scaled one, for the reason of the test above: between the
// deadline and the kill the watcher starts a few processes (date and the
// awks that read the log, ps for the tree), and under a pileup's load
// those stall past a run of 4, which then lands instead of going back to
// new (yardr-aps5). A green run still ends at the kill, so the longer
// sleep costs it nothing.
func TestMergeLocalTestsDeadlineFollowsParThrottle(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	throttle := `echo "test: go test -p=2 (load 40 on 10 cores)"; `
	for _, tc := range []struct {
		name  string
		test  string
		stage string
		want  []string
		avoid string
	}{
		{"scaled", throttle + `sleep 4`, "merged", nil, "ran past their deadline"},
		{"scaled past the deadline is red", throttle + `sleep 30`, "new",
			[]string{"the tests failed", "ran past their deadline of 2s and were killed", "deadline 2 s x 5 for -p=2 on 10 cores"}, ""},
		{"without the line the deadline is plain", `sleep 30`, "new",
			[]string{"the tests failed", "ran past their deadline of 2s and were killed"}, "deadline 2 s x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y := newYard(t)
			tip := y.commit(y.wt, "b.txt", "bead\n")
			ran := filepath.Join(y.home, "ran")
			y.tests(`echo run >> ` + ran + `; ` + tc.test)
			y.opts = "YARDR_TEST_TIMEOUT=2"

			y.assembly()

			b := y.bead()
			if b.Stage != tc.stage || b.Hold {
				t.Fatalf("bead %s hold=%v, want %s", b.Stage, b.Hold, tc.stage)
			}
			if n := runs(t, ran); n != 1 {
				t.Fatalf("the tests ran %d times, want 1", n)
			}
			notes := y.notes()
			for _, want := range tc.want {
				if !strings.Contains(notes, want) {
					t.Fatalf("notes lack %q:\n%s", want, notes)
				}
			}
			if tc.avoid != "" && strings.Contains(notes, tc.avoid) {
				t.Fatalf("notes hold %q, want none:\n%s", tc.avoid, notes)
			}
			if tc.stage == "merged" {
				if b.Status != "closed" {
					t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
				}
				if main := y.git(y.repo, "rev-parse", "main"); main != tip {
					t.Fatalf("main %s, want the tested commit %s", main, tip)
				}
			}
		})
	}
}

func TestMergeLocalTimeoutThrottleUsesDecimalLimit(t *testing.T) {
	script := readFile(t, "merge-local")
	start := strings.Index(script, "      case \"$fact\" in")
	if start < 0 {
		t.Fatal("merge-local has no throttle factor check")
	}
	end := strings.Index(script[start:], "      extra=$((waited - credited + extension - parred))")
	if end < 0 {
		t.Fatal("merge-local has no extension calculation")
	}
	calculation := script[start : start+end]
	for _, tc := range []struct {
		limit string
		want  string
	}{
		{limit: "08", want: "32"},
		{limit: "000", want: "0"},
		{limit: "invalid", want: "0"},
	} {
		t.Run(tc.limit, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", "LIMIT="+tc.limit+"; fact=5\n"+calculation+"printf '%s\\n' \"$extension\"")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("timeout scaling failed for LIMIT=%s: %v\n%s", tc.limit, err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("LIMIT=%s scaled to %s, want %s", tc.limit, got, tc.want)
			}
		})
	}
}

// trainSignalYard is newTrainYard with both children landed, and tests that
// from then on are ended by SIGTERM: the first times times, 0 for always.
// The landed yard is the template's, copied (fixture_test.go).
func trainSignalYard(t *testing.T, times int) (y *yard, ran string) {
	t.Helper()
	y = borrowLanded(t, "true")
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

// wantIntegrated fails the test unless the train went to review with its
// tests passed, not killed at a deadline.
func wantIntegrated(t *testing.T, y *yard) {
	t.Helper()
	if e := y.beadOf("r-e"); e.Stage != "review" || e.Status != "open" || e.Hold {
		t.Fatalf("train %s/%s hold=%v, want review", e.Stage, e.Status, e.Hold)
	}
	notes := y.notesOf("r-e")
	if strings.Contains(notes, "ran past their deadline") || !strings.Contains(notes, "tests passed") {
		t.Fatalf("a run within its deadline plus its wait was not integrated:\n%s", notes)
	}
}

// integrate-train's deadline counts only time not spent waiting for the race
// lock, as merge-local's does (TestMergeLocalTestsDeadlineExcludesRaceLockWait):
// a run that prints `test: waited N s` and then runs past LIMIT but within
// LIMIT+N goes to review, not killed. Here LIMIT is 2, the wait 10, and the
// run 4.
func TestTrainTestsDeadlineExcludesRaceLockWait(t *testing.T) {
	y := landedTrainYard(t, "true")
	y.tests(`echo "test: waited 10 s for the race lock"; sleep 4`)
	y.opts = "YARDR_TEST_TIMEOUT=2"

	y.exec("integrate-train", "yard-integrators", "r-e")

	wantIntegrated(t, y)
}

// A lock wait longer than LIMIT is still excluded before the gate can print
// its final wait line (TestMergeLocalTestsDeadlineExcludesRaceLockWaitLongerThanLimit).
// Here it marks the wait at the start, waits 4 seconds, then runs for 1
// second: 5 seconds total is within LIMIT+the wait (7).
func TestTrainTestsDeadlineExcludesRaceLockWaitLongerThanLimit(t *testing.T) {
	y := landedTrainYard(t, "true")
	y.tests(`echo "test: race lock wait started at $(date +%s)"; sleep 4; echo "test: waited 4 s for the race lock"; sleep 1`)
	y.opts = "YARDR_TEST_TIMEOUT=3"

	y.exec("integrate-train", "yard-integrators", "r-e")

	wantIntegrated(t, y)
}

// Past LIMIT plus its waited seconds, the run is killed as before, and a
// killed run is red: the train goes back to open with a child to fix it, and
// the tests are not run again. Here LIMIT is 2, the wait 2, and the run 30,
// killed at 4. The run outlasts the kill by 26 s, not 6: between the
// deadline and the kill the watcher starts a few processes (date and the
// awks that read the log, ps for the tree), and under a pileup's load those
// stall past a 10 s run, which then ends green and sends the train to review
// instead of back to open (yardr-hrd1, as yardr-d6wj for merge-local's twin
// of this test). A run killed on time ends at the kill, so the longer sleep
// costs it nothing, and a run that finishes fails the assertions below, well
// short of the exec fuse.
func TestTrainTestsDeadlinePastRaceLockWaitIsRed(t *testing.T) {
	y := landedTrainYard(t, "true")
	ran := filepath.Join(y.home, "ran")
	y.tests(`echo run >> ` + ran + `; echo "test: waited 2 s for the race lock"; sleep 30`)
	y.opts = "YARDR_TEST_TIMEOUT=2"

	y.exec("integrate-train", "yard-integrators", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if n := runs(t, ran); n != 1 {
		t.Fatalf("the tests ran %d times, want 1", n)
	}
	notes := y.notesOf("r-e")
	if !strings.Contains(notes, "the tests failed") || !strings.Contains(notes, "ran past their deadline of 2s and were killed") {
		t.Fatalf("the deadline past its wait is not reported as a red run:\n%s", notes)
	}
	if f := y.familyOf("r-e"); len(f.Children) != 3 {
		t.Fatalf("train children %v, want r-1, r-2 and a fix", f.Children)
	}
}

// creditLoop is the part of a script's timeout watcher that credits the
// seconds the run waited for the race lock: from credited=0 to the done that
// ends its loop.
func creditLoop(t *testing.T, script string) string {
	t.Helper()
	_, rest, ok := strings.Cut(readFile(t, script), "\n    credited=0\n")
	if !ok {
		t.Fatalf("%s has no credit loop (no credited=0)", script)
	}
	loop, _, ok := strings.Cut(rest, "\n    done\n")
	if !ok {
		t.Fatalf("%s: its credit loop does not end in done", script)
	}
	return loop
}

// integrate-train's and pr-ci's watchers credit the race-lock wait with
// merge-local's loop, copied: the scripts are installed one file each, so there
// is none to source. A fix to one is made in all, as the copies of merge-local
// are one script (TestMergeLocalCopiesIdentical): yardr-bywx fixed merge-local
// alone, and a train late in the queue was killed with every test green.
func TestCopiedScriptBlocksStayEqual(t *testing.T) {
	for _, script := range []string{"integrate-train", "pr-ci"} {
		if want, got := creditLoop(t, "merge-local"), creditLoop(t, script); got != want {
			t.Errorf("%s's credit loop differs from merge-local's: change all\nmerge-local:\n%s\n%s:\n%s", script, want, script, got)
		}
	}
	want := markedBlock(t, "merge-local", closefdsOpen, closefdsClose)
	for _, s := range closefdsScripts[1:] {
		if got := markedBlock(t, s.path, closefdsOpen, closefdsClose); got != want {
			t.Errorf("%s's closefds differs from merge-local's: change all\nmerge-local:\n%s\n%s:\n%s", s.path, want, s.path, got)
		}
	}
}

// shellWithoutHigh is the first of shells that cannot name descriptors above
// 9, the one sh closefds refuses under, ready to run a -c driver; false where
// every shell the host has can. It picks by what the shell cannot do, not by
// name: where the host's sh is dash (Debian, Ubuntu), testShells drops its dash
// candidate as the same file and lists the one dash as sh, with a blank path
// for the shebang's. That is /bin/sh, the file its high was asked of, not
// whichever sh a PATH finds first.
func shellWithoutHigh(shells []testShell) (testShell, bool) {
	for _, sh := range shells {
		if sh.high {
			continue
		}
		if sh.path == "" {
			sh.path = "/bin/sh"
		}
		return sh, true
	}
	return testShell{}, false
}

// shellWithoutHigh is fed the lists testShells makes on the hosts this suite
// runs on. Debian's is the one it was written for: its dash is listed as sh,
// and a pick by the name dash found none, so the refusal below was never run
// there.
func TestShellWithoutHigh(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		shells []testShell
		want   testShell
		found  bool
	}{
		{
			name:   "debian lists its dash once, as sh with no path",
			shells: []testShell{{name: "sh", path: "", high: false}},
			want:   testShell{name: "sh", path: "/bin/sh", high: false},
			found:  true,
		},
		{
			name:   "debian with bash beside it",
			shells: []testShell{{name: "sh", path: "", high: false}, {name: "bash", path: "/usr/bin/bash", high: true}},
			want:   testShell{name: "sh", path: "/bin/sh", high: false},
			found:  true,
		},
		{
			name:   "mac has a bash sh and its dash apart, with a path",
			shells: []testShell{{name: "sh", path: "", high: true}, {name: "dash", path: "/bin/dash", high: false}, {name: "bash", path: "/bin/bash", high: true}},
			want:   testShell{name: "dash", path: "/bin/dash", high: false},
			found:  true,
		},
		{
			name:   "the first of two wins",
			shells: []testShell{{name: "sh", path: "", high: false}, {name: "dash", path: "/usr/local/bin/dash", high: false}},
			want:   testShell{name: "sh", path: "/bin/sh", high: false},
			found:  true,
		},
		{
			name:   "none where every shell closes above 9",
			shells: []testShell{{name: "sh", path: "", high: true}, {name: "bash", path: "/bin/bash", high: true}},
			found:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, found := shellWithoutHigh(tc.shells)
			if got != tc.want || found != tc.found {
				t.Errorf("shellWithoutHigh(%+v) = %+v, %v, want %+v, %v", tc.shells, got, found, tc.want, tc.found)
			}
		})
	}
}

// closefds refuses when it cannot even list the descriptors it must close:
// with no listing, the glob matches nothing, and the run must hold rather
// than start over unseen descriptors. The block is merge-local's, read
// from the script, so the test pins whatever the equality test holds
// across the scripts; the unlisted row points it at a directory that does
// not exist, which fails the listing the way a failed open does. The
// listed row keeps the harness honest. Both run under the sh that cannot
// close above 9 (shellWithoutHigh), the one the refusal is for: a shell that
// can close them closes what it lists and starts the command.
func TestCloseFdsRefusesWhenDescriptorsCannotBeListed(t *testing.T) {
	t.Parallel()
	sh, ok := shellWithoutHigh(testShells())
	if !ok {
		t.Skip("no shell without high descriptors on this host")
	}
	t.Logf("closefds is run under %s (%s)", sh.name, sh.path)
	block := markedBlock(t, "merge-local", closefdsOpen, closefdsClose)
	for _, tc := range []struct {
		name    string
		rewrite bool // point the listing at a missing directory
		want    string
	}{
		{"listed", false, "10"},
		{"unlisted", true, "?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// From a string, not a file: dash holds a script file open
			// while it reads it, and its descriptor would join the
			// listing the test pins.
			driver := block + "set -u\nclosefds \"the tests\"\necho SURVIVED:$?\n"
			if tc.rewrite {
				driver = strings.ReplaceAll(driver, "/dev/fd", "/dev/no-such-dir-8hm5")
			}
			cmd := exec.Command(sh.path, "-c", driver, "closefds.sh")
			cmd.Dir = t.TempDir()
			cmd.ExtraFiles = plantedFiles(t, cmd.Dir, true, 0)
			out, err := cmd.CombinedOutput()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatalf("closefds driver: %v\n%s", err, out)
			}
			if code != 2 {
				t.Errorf("closefds with %s descriptors exits %d, want 2 (refused):\n%s", tc.name, code, out)
			}
			if strings.Contains(string(out), "SURVIVED") {
				t.Errorf("closefds with %s descriptors started the command:\n%s", tc.name, out)
			}
			if got := refusedList(string(out), "closefds.sh"); !refusedHas(got, tc.want) {
				t.Errorf("closefds with %s descriptors refused with %v, want %s among them:\n%s", tc.name, got, tc.want, out)
			}
		})
	}
}

const (
	closefdsOpen  = "# --- closefds: "
	closefdsClose = "# --- end of closefds ---\n"
)

// closefdsScripts are the scripts that close what they inherited before they
// start a command of the depot's or the operator's, merge-local first, and
// whether their timeout watcher closes it for its sleep too.
var closefdsScripts = []struct {
	path    string
	watcher bool
}{
	{"merge-local", true},
	{"integrate-train", true},
	{"pr-ci", true},
	{"../deploy/deploy-dokku", false},
	{"../deploy/deploy-void", false},
}

// Every command these scripts start from a subshell is started after
// closefds, named for the refusal it may owe, and their watchers call it
// bare, for the sleeps they start. merge-local's and integrate-train's runs
// show that in the tests of their descriptors; this reads the rest, whose
// fixtures (a pull request, a Void project) cost more than the line they would
// pin.
func TestCloseFdsComesBeforeEveryStart(t *testing.T) {
	for _, s := range closefdsScripts {
		lines := strings.Split(readFile(t, s.path), "\n")
		starts, watchers := 0, 0
		for i, line := range lines {
			code := strings.TrimSpace(line)
			if code == "closefds" {
				watchers++
			}
			if strings.HasPrefix(code, "#") || !strings.Contains(code, "exec sh ") {
				continue
			}
			starts++
			prev := ""
			for j := i - 1; j >= 0 && prev == ""; j-- {
				if c := strings.TrimSpace(lines[j]); c != "" && !strings.HasPrefix(c, "#") {
					prev = c
				}
			}
			if !strings.HasPrefix(prev, `closefds "`) {
				t.Errorf("%s: line %d starts a command (%s) after %q, not after closefds", s.path, i+1, code, prev)
			}
		}
		if starts == 0 {
			t.Errorf("%s starts no command by exec sh: has the start moved?", s.path)
		}
		if want := map[bool]int{true: 1, false: 0}[s.watcher]; watchers != want {
			t.Errorf("%s calls closefds bare %d times, want %d (its watcher's)", s.path, watchers, want)
		}
	}
}

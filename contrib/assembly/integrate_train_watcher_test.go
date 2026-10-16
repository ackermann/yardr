package assembly

// integrate-train's timeout watcher never leaves its sleep behind, as
// merge-local's does not (watcher_test.go): when integrate-train itself is
// killed with TERM mid-run, the watcher and its sleep go with it. The
// watcher was ported from merge-local without its traps, so the sleep was
// orphaned for up to $LIMIT (yardr-dpff).
//
// The run is the same direct one watcher_test.go gives merge-local: a
// throwaway repository with base main and branch yard/r-1 ahead of it in
// its worktree, a stub yardr, and a recording sleep first on PATH. The
// branch is what integrate-train works on, so the train id reads r-1.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// newIntegrateTrainWatcherRun is newWatcherRun aimed at integrate-train:
// the same repository, stub and sleep; only the script differs.
func newIntegrateTrainWatcherRun(t *testing.T, test, limit string, hi int) *watcherRun {
	t.Helper()
	w := newWatcherRun(t, test, limit, hi)
	script, err := filepath.Abs("integrate-train")
	if err != nil {
		t.Fatal(err)
	}
	// An input of the test cache, as merge-local is there.
	if _, err := os.ReadFile(script); err != nil {
		t.Fatal(err)
	}
	w.script = script
	return w
}

// When integrate-train itself is killed with TERM, its watcher and the
// watcher's sleep go with it: the script reports 143, and no sleep it
// started is left behind.
func TestIntegrateTrainKilledWithTermLeavesNoSleepBehind(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "testpid")
	w := newIntegrateTrainWatcherRun(t, "sleep 60 & echo $! >"+shellQuote(pidFile)+"; wait", "1789", 0)
	t.Cleanup(func() {
		if p, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(p))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		killWatcherSleepers(t, w.record, "1789")
	})
	// A slow test command, so integrate-train is mid-run when it is
	// killed; its own sleep is killed above, whatever outlives the run.
	cmd := w.cmd()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// In ps, the sleep has exec'd past the recording stand-in, so the
	// recording is complete before it is killed below.
	_, watcher := awaitWatcherSleep(t, cmd, w.record, "1789")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("waiting for integrate-train: %v", err)
	}
	if code != 143 {
		t.Errorf("integrate-train killed with TERM exits %d, want 143", code)
	}
	if syscall.Kill(watcher, 0) == nil {
		t.Errorf("the watcher (pid %d) survived the TERM to integrate-train", watcher)
	}
	awaitNoWatcherSleepers(t, w.record, "1789")
	assertWatcherSleepFds(t, w.record, "1789")
}

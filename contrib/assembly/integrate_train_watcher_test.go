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

// The pid file lives only long enough for the parent to find the watcher's
// sleep. It is created with mktemp, so it keeps the yard's private-file mode
// even when the caller's umask would make a redirection readable by others.
func assertWatcherNapFileMode(t *testing.T, dir string, pid int) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(b)) != strconv.Itoa(pid) {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
			t.Errorf("watcher pid file mode is %v, want a regular 0600 file", info.Mode())
		}
		return path
	}
	t.Fatalf("no watcher pid file for sleep %d in %s", pid, dir)
	return ""
}

// When integrate-train itself is killed with TERM, its watcher and the
// watcher's sleep go with it, and so does its test command with everything
// that command started, before a file is taken away, as merge-local's does
// (and a second stop of its whole process group cuts the removal of its files
// short as little): the script reports 143, no sleep it started is left behind,
// and nothing it made is.
func TestIntegrateTrainKilledWithTermLeavesNoSleepBehind(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "testpid")
	w := newIntegrateTrainWatcherRun(t, "sleep 300 & echo $! >"+shellQuote(pidFile)+"; wait", "1789", 0)
	bin := filepath.Join(filepath.Dir(w.record), "bin")
	made := recordMktemp(t, bin)
	arm, held, release := gateRm(t, bin)
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
	// Use an unrestrictive umask so the private-file check catches a PID
	// file made by an ordinary redirection instead of mktemp.
	cmd := exec.Command("sh", "-c", "umask 000; exec \"$1\"", "sh", w.script)
	cmd.Dir, cmd.Env, cmd.ExtraFiles = w.tmp, w.env, w.extra
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	group := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-group, syscall.SIGKILL) })
	// The await waited for the sleep's record file, which the fake sleep
	// writes before it execs, so the recording is complete before the sleep
	// is killed below.
	nap, watcher := awaitWatcherSleep(t, cmd, w.tmp, w.record, "1789")
	assertWatcherNapFileMode(t, w.tmp, nap)
	child := awaitTestChild(t, pidFile)
	touch(t, arm)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	// The script is taking its files away, and has stopped its test command
	// first. The daemon's second stop, which reaches the rm too, finds it there.
	awaitFile(t, held, "integrate-train never began to remove its files")
	assertGone(t, child, "the test command's sleep")
	if err := syscall.Kill(-group, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	touch(t, release)
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
	assertNothingLeft(t, made)
}

// A local user of a shared TMPDIR can list it, see integrate-train's log by
// name and put a symlink at a name made from it before integrate-train
// creates that name (yardr-gb50): ".late" for the deadline marker, ".sh" for
// the copy of the base's test script that is then run. A mktemp that plants
// both symlinks at every file it hands back stands in for the user, each
// symlink to a victim file. integrate-train must make each of its files with
// mktemp and name none from another's, or it writes through a symlink and a
// victim changes. The run is the base's copy of a script that outlasts its
// deadline, which writes both.
func TestIntegrateTrainWritesThroughNoPlantedSymlink(t *testing.T) {
	t.Parallel()
	w := newIntegrateTrainWatcherRun(t, "check.sh", "1", 0)
	t.Cleanup(func() { killWatcherSleepers(t, w.record, "1") })
	if err := os.WriteFile(filepath.Join(w.repo, "check.sh"), []byte("echo from the base\nsleep 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	watcherGit(t, w.repo, "add", "check.sh")
	watcherGit(t, w.repo, "commit", "-q", "-m", "the test script")

	const precious = "not for integrate-train to write\n"
	victims := []struct{ name, path string }{
		{"the deadline marker", filepath.Join(t.TempDir(), "late")},
		{"the copy of the test script", filepath.Join(t.TempDir(), "copy")},
	}
	for _, v := range victims {
		if err := os.WriteFile(v.path, []byte(precious), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mktemp, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatal(err)
	}
	planting := "#!/bin/sh\nf=$(" + shellQuote(mktemp) + " \"$@\") || exit\n" +
		"ln -s " + shellQuote(victims[0].path) + " \"$f.late\"\n" +
		"ln -s " + shellQuote(victims[1].path) + " \"$f.sh\"\n" +
		"printf '%s\\n' \"$f\"\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(w.record), "bin", "mktemp"), []byte(planting), 0o755); err != nil {
		t.Fatal(err)
	}

	// Into a file, not a pipe, as the runs in watcher_test.go: see there.
	log, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := w.cmd()
	cmd.Stdout, cmd.Stderr = log, log
	err = cmd.Run()
	log.Close()
	b, _ := os.ReadFile(log.Name())
	out := string(b)
	if err != nil {
		t.Fatalf("integrate-train: %v\n%s", err, out)
	}
	for _, want := range []string{"the base's copy of check.sh", "from the base", "the tests ran past their deadline of 1s and were killed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the run did not use the base's copy up to its deadline (no %q):\n%s", want, out)
		}
	}

	for _, v := range victims {
		if got, err := os.ReadFile(v.path); err != nil || string(got) != precious {
			t.Errorf("integrate-train wrote through a symlink planted at the name of %s: the victim holds %q (%v)", v.name, got, err)
		}
	}
	// Each file it made is gone: only the planted symlinks are left.
	entries, err := os.ReadDir(w.tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			t.Errorf("integrate-train left %s in TMPDIR", e.Name())
		}
	}
}

// A train's test command starts with no descriptor above 2 that the run
// did not give it, as a landing's does not (watcher_test.go): the run
// inherits open files above 2 as a race run holds its lock, with the high
// sentinel when the limit has room for one, and the command lists its own
// descriptors and probes every inherited one, as that test's does, under every
// sh the host has; where an sh cannot close what the run holds, the command is
// not started at all.
func TestIntegrateTrainTestCommandStartsWithNoInheritedFile(t *testing.T) {
	t.Parallel()
	for _, row := range fdRuns() {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			record := filepath.Join(t.TempDir(), "fds")
			w := newIntegrateTrainWatcherRun(t, probeCommand(record), "1799", 0)
			w.under(t, row.sh, row.high, row.sentinel(t))
			t.Cleanup(func() { killWatcherSleepers(t, w.record, "1799") })
			// Into a file, not a pipe, as the runs in watcher_test.go: see there.
			log, err := os.Create(filepath.Join(t.TempDir(), "out"))
			if err != nil {
				t.Fatal(err)
			}
			cmd := w.cmd()
			cmd.Stdout, cmd.Stderr = log, log
			err = cmd.Run()
			log.Close()
			b, _ := os.ReadFile(log.Name())
			out := string(b)
			if err != nil {
				t.Fatalf("integrate-train (ls /dev/fd): %v\n%s", err, out)
			}
			awaitNoWatcherSleepers(t, w.record, "1799")
			if row.refused {
				assertRefused(t, "integrate-train", out, record)
				return
			}
			if !strings.Contains(out, "== r-1 done") {
				t.Fatalf("integrate-train (ls /dev/fd) did not report done:\n%s", out)
			}
			assertNoInheritedFds(t, record)
		})
	}
}

// A pid file that holds zero must not make integrate-train signal its whole
// process group, as merge-local's does not
// (TestMergeLocalIgnoresZeroWatcherPID): the digits-only check let 0 through
// to kill, and kill reads 000 as 0 too (yardr-0bd1). The script leads a
// process group of its own, so a signal to the group reaches it, and its TERM
// trap turns that into exit 143.
func TestIntegrateTrainIgnoresZeroWatcherPID(t *testing.T) {
	t.Parallel()
	for _, zero := range []string{"0", "000"} {
		t.Run(zero, func(t *testing.T) {
			t.Parallel()
			w := newIntegrateTrainWatcherRun(t, "sleep 1", "1789", 0)
			// The test command waits for a release file, as merge-local's
			// zero test does: the run cannot finish and remove the nap
			// file before the zero is written, however long this goroutine
			// is unscheduled.
			release := filepath.Join(w.tmp, "release")
			gated := "while [ ! -f " + shellQuote(release) + " ]; do sleep 1; done"
			for i, kv := range w.env {
				if strings.HasPrefix(kv, "YARDR_TEST=") {
					w.env[i] = "YARDR_TEST=" + gated
					break
				}
			}
			w.test = gated
			t.Cleanup(func() { killWatcherSleepers(t, w.record, "1789") })
			cmd := w.cmd()
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			group := cmd.Process.Pid
			t.Cleanup(func() { _ = syscall.Kill(-group, syscall.SIGKILL) })

			nap, _ := awaitWatcherSleep(t, cmd, w.tmp, w.record, "1789")
			napfile := assertWatcherNapFileMode(t, w.tmp, nap)
			if err := os.WriteFile(napfile, []byte(zero+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			// The release is created only after the zero is written.
			if err := os.WriteFile(release, []byte{}, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("integrate-train with a watcher pid of %s: %v", zero, err)
			}
			awaitNoWatcherSleepers(t, w.record, "1789")
		})
	}
}

package assembly

// The timeout watcher of merge-local never leaves its sleep behind: when
// its test command finishes, the watcher and its sleep go with the run,
// and when merge-local itself is killed with TERM, both go with it (an
// orphaned sleep once held the machine race lock past the run:
// yardr-tsms). The sleep also starts with no inherited file open: every fd
// above 2 closed.
//
// These tests run merge-local directly, without a yard: a throwaway
// repository with base main and branch yard/r-1 in its worktree, a stub
// yardr that says yes to every move, and a sleep that records the fds it
// was started with. The run inherits open files above 2, as a race run
// holds its lock: filler to fd 9, the lock stand-in at fd 10, and one high
// sentinel under the process's own open-files limit, which the limit may
// be too low for.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// watcherRun is merge-local's world for one direct run: a repository, a
// stub yardr, a recording sleep first on PATH, and the environment the
// exec runner would give it.
type watcherRun struct {
	t           *testing.T
	repo, wt    string
	tmp         string
	script      string
	record      string
	env         []string
	extra       []*os.File
	test, limit string
}

func watcherGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// highSentinel is the high fd one run inherits, proving the watcher closes
// descriptors far above the shell's own: 512, under the process's own
// open-files limit and well under FD_SETSIZE (a fixed 1024 aborted a
// landing: yardr-tsms). The entries are contiguous from 3, and the spawn
// wants room above them besides (88 spare fails the spawn, 588 passes),
// so the limit must clear 2048. Too low a limit for one is not a
// failure: the slow run still proves the rest without it.
func highSentinel(t *testing.T) (int, bool) {
	t.Helper()
	var fdLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &fdLimit); err != nil {
		t.Fatal(err)
	}
	if fdLimit.Cur < 2048 {
		return 0, false
	}
	return 512, true
}

func newWatcherRun(t *testing.T, test, limit string, hi int) *watcherRun {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	wt := filepath.Join(dir, "wt")
	tmp := filepath.Join(dir, "tmp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	watcherGit(t, dir, "init", "-q", "-b", "main", repo)
	watcherGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	watcherGit(t, repo, "branch", "yard/r-1")
	watcherGit(t, repo, "worktree", "add", "-q", wt, "yard/r-1")
	watcherGit(t, wt, "commit", "-q", "--allow-empty", "-m", "bead")
	stub := filepath.Join(dir, "stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ncat >/dev/null 2>&1; exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Each sleep records into its own file: several run at once, and one
	// shared record interleaves their headers with each other's lists.
	fake := "#!/bin/sh\n{ echo \"argv: $*\"; ls /dev/fd; } >\"$FAKE_SLEEP_RECORD.$$\"\nexec /bin/sleep \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "sleep"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "sleep-record")
	// The run's fds 3 to 9 are filler and its fd 10 the lock stand-in:
	// ExtraFiles entry i is the child's fd 3+i, so the entries between
	// are filler too, up to the high sentinel at hi. Reuse one filler
	// file for all of them: each entry is dup'd onto its own fd.
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	var extra []*os.File
	for i := 0; i < 7; i++ {
		extra = append(extra, null)
	}
	lock, err := os.Create(filepath.Join(dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	extra = append(extra, lock)
	if hi > 0 {
		for len(extra) < hi-2 { // entry hi-3 becomes child fd hi
			extra = append(extra, null)
		}
	}
	t.Cleanup(func() {
		closed := make(map[*os.File]bool)
		for _, f := range extra {
			if !closed[f] {
				closed[f] = true
				_ = f.Close()
			}
		}
	})
	script, err := filepath.Abs("merge-local")
	if err != nil {
		t.Fatal(err)
	}
	// Reading the script makes it an input of the test cache: go test
	// does not see files a test only executes, and would keep a stale
	// pass.
	if _, err := os.ReadFile(script); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"YARDR_BEAD=r-1", "YARDR_DEPOT=r", "YARDR_DEPOT_PATH="+repo, "YARDR_BASE=main",
		"YARDR_BRANCH=yard/r-1", "YARDR_WORKTREE="+wt, "YARDR_TEST="+test,
		"YARDR_TEST_TIMEOUT="+limit, "YARDR_BIN="+stub, "TMPDIR="+tmp,
		"FAKE_SLEEP_RECORD="+record, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &watcherRun{t: t, repo: repo, wt: wt, tmp: tmp, script: script,
		record: record, env: env, extra: extra, test: test, limit: limit}
}

// cmd starts merge-local as the exec runner would, with the filler and the
// lock stand-in open beside it: the run's fds 3 to 10.
func (w *watcherRun) cmd() *exec.Cmd {
	cmd := exec.Command(w.script)
	cmd.Dir = w.tmp
	cmd.Env = w.env
	cmd.ExtraFiles = w.extra
	return cmd
}

// watcherSleepers is every "sleep <limit>" process ps sees, as pid and
// ppid: the watcher's sleep, by the limit no other test uses.
func watcherSleepers(t *testing.T, limit string) [][2]int {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid", "-o", "ppid", "-o", "command").CombinedOutput()
	if err != nil {
		t.Fatalf("ps: %v\n%s", err, out)
	}
	var found [][2]int
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue // the header
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		argv := f[2:]
		if err1 != nil || err2 != nil || len(argv) != 2 || filepath.Base(argv[0]) != "sleep" || argv[1] != limit {
			continue
		}
		found = append(found, [2]int{pid, ppid})
	}
	return found
}

// awaitNoWatcherSleepers fails unless every "sleep <limit>" is gone within
// the wait: a killed sleep takes a moment to die, an orphaned one never
// leaves.
func awaitNoWatcherSleepers(t *testing.T, limit string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		strays := watcherSleepers(t, limit)
		if len(strays) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d stray sleep %s still alive: %v", len(strays), limit, strays)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// assertWatcherSleepFds fails unless the recording sleep saw no inherited
// file when it started the watcher's sleep: nothing at fd 10, where the
// lock stand-in would be, nor above it. What the recording itself opens
// (its output, the /dev/fd read) takes the lowest free fds, far below.
func assertWatcherSleepFds(t *testing.T, record, limit string) {
	t.Helper()
	files, _ := filepath.Glob(record + ".*")
	blocks := 0
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) == 0 || lines[0] != "argv: "+limit {
			continue
		}
		blocks++
		for _, line := range lines[1:] {
			fd, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatalf("sleep %s saw %q in /dev/fd, not a number", limit, line)
			}
			if fd >= 10 {
				t.Fatalf("sleep %s started with fd %d open: %v", limit, fd, lines[1:])
			}
		}
	}
	if blocks == 0 {
		t.Fatalf("no sleep %s in %d recordings", limit, len(files))
	}
}

// awaitWatcherSleep waits for the watcher's sleep to show in ps and
// returns its pid and ppid: the ppid is the watcher. Whatever starts the
// sleep, a run whose sleep never shows has nothing to kill.
func awaitWatcherSleep(t *testing.T, cmd *exec.Cmd, limit string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if strays := watcherSleepers(t, limit); len(strays) > 0 {
			return strays[0][0], strays[0][1]
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("the watcher's sleep never showed in ps")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// killWatcherSleepers kills every "sleep <limit>" it sees: a failing run's
// hygiene, so an orphan it proved never outlives the test by half an hour.
func killWatcherSleepers(t *testing.T, limit string) {
	t.Helper()
	for _, s := range watcherSleepers(t, limit) {
		_ = syscall.Kill(s[0], syscall.SIGKILL)
	}
}

// When its test command finishes, merge-local leaves no sleep behind: once
// with a command that finishes at once, while the watcher is likeliest
// still starting, and once with a slow one, whose sleep starts with no
// inherited file open.
func TestMergeLocalWatcherLeavesNoSleepBehind(t *testing.T) {
	t.Parallel()
	// Into a file, not a pipe: an orphaned sleep would hold a pipe open
	// past the run's own exit, and the wait below with it.
	finished := func(test, limit string, hi int) *watcherRun {
		t.Helper()
		w := newWatcherRun(t, test, limit, hi)
		t.Cleanup(func() { killWatcherSleepers(t, limit) })
		log, err := os.Create(filepath.Join(w.tmp, "out"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := w.cmd()
		cmd.Stdout, cmd.Stderr = log, log
		err = cmd.Run()
		log.Close()
		out, _ := os.ReadFile(log.Name())
		if err != nil {
			t.Fatalf("merge-local (%s): %v\n%s", test, err, out)
		}
		if main, tip := watcherGit(t, w.repo, "rev-parse", "main"), watcherGit(t, w.wt, "rev-parse", "HEAD"); main != tip {
			t.Fatalf("merge-local (%s): main %s is not the landed branch %s:\n%s", test, main, tip, out)
		}
		awaitNoWatcherSleepers(t, limit)
		return w
	}
	finished("true", "1799", 0)
	// The slow run carries the high sentinel when the limit has room for
	// one, and proves the run inherited it before closing it away; too
	// low a limit runs the same slow proof without it, and says so.
	test := "sleep 2"
	hi := 0
	if sentinel, ok := highSentinel(t); ok {
		hi = sentinel
		test = fmt.Sprintf("[ -e /dev/fd/%d ] || [ -L /dev/fd/%d ] || exit 42; exec %d<&-; sleep 2", hi, hi, hi)
	} else {
		t.Logf("open-files limit too low for a high-fd sentinel: the slow run proves no sleep left and clean fds without it")
	}
	w := finished(test, "1797", hi)
	assertWatcherSleepFds(t, w.record, "1797")
}

// When merge-local itself is killed with TERM, its watcher and the
// watcher's sleep go with it: the script reports 143, and no sleep it
// started is left behind.
func TestMergeLocalKilledWithTermLeavesNoSleepBehind(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "testpid")
	t.Cleanup(func() {
		if p, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(p))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		killWatcherSleepers(t, "1789")
	})
	// A slow test command, so merge-local is mid-run when it is killed;
	// its own sleep is killed above, whatever outlives the run.
	w := newWatcherRun(t, "sleep 60 & echo $! >"+shellQuote(pidFile)+"; wait", "1789", 0)
	cmd := w.cmd()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// In ps, the sleep has exec'd past the recording stand-in, so the
	// recording is complete before it is killed below.
	_, watcher := awaitWatcherSleep(t, cmd, "1789")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("waiting for merge-local: %v", err)
	}
	if code != 143 {
		t.Errorf("merge-local killed with TERM exits %d, want 143", code)
	}
	if syscall.Kill(watcher, 0) == nil {
		t.Errorf("the watcher (pid %d) survived the TERM to merge-local", watcher)
	}
	awaitNoWatcherSleepers(t, "1789")
	assertWatcherSleepFds(t, w.record, "1789")
}

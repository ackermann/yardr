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
// be too low for. Those above 9 only for an sh that can close them: dash
// reads one digit as a redirection's number, so a run under it is given
// the filler alone, and the scripts refuse the rest (see closefds).

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	shell       string // what runs the script, empty for its shebang's sh
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
// descriptors far above the shell's own: 512, under the child shell's
// open-files limit and well under FD_SETSIZE (a fixed 1024 aborted a
// landing: yardr-tsms). Read the child limit from sh because Go raises its
// own soft limit and restores the original only for child processes. The
// entries are contiguous from 3, and the spawn wants room above them too
// (88 spare fails the spawn, 588 passes), so the limit must clear 2048. Too
// low a limit for one is not a failure: the slow run still proves the rest
// without it.
func highSentinel(t *testing.T) (int, bool) {
	t.Helper()
	out, err := exec.Command("sh", "-c", "ulimit -n").Output()
	if err != nil {
		t.Fatalf("reading the child shell's open-files limit: %v", err)
	}
	fdLimit, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatalf("child shell's open-files limit %q is not a number", out)
	}
	if fdLimit < 2048 {
		return 0, false
	}
	return 512, true
}

// testShell is an sh the scripts run under in the tests of what they close.
// The scripts say #!/bin/sh and a host's sh is whatever it is (bash on a Mac,
// dash on Debian and Ubuntu, busybox on Alpine), so these tests also run them
// under dash and bash where the host has them. The shells differ in what
// closefds can do: dash reads one digit as a redirection's number and so
// cannot close a descriptor above 9.
type testShell struct {
	name string // the subtest's; sh is the script run as the kernel runs it
	path string // what runs the script, empty for the shebang's sh
	high bool   // whether it names descriptors above 9
}

// testShells are the shells the host has, each once: its sh as the shebang
// finds it, then dash and bash unless one is that same file. high is asked of
// the shell as closefds asks it.
var testShells = sync.OnceValue(func() []testShell {
	var shells []testShell
	seen := map[string]bool{}
	for _, c := range []testShell{{name: "sh", path: "/bin/sh"}, {name: "dash", path: "dash"}, {name: "bash", path: "bash"}} {
		found, err := exec.LookPath(c.path)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(found)
		if err != nil || seen[resolved] {
			continue
		}
		seen[resolved] = true
		c.high = exec.Command(found, "-c", "(eval 'exec 10>&-') >/dev/null 2>&1").Run() == nil
		c.path = found
		if c.name == "sh" {
			c.path = ""
		}
		shells = append(shells, c)
	}
	return shells
})

// hostHigh says whether the sh the scripts' shebang finds names descriptors
// above 9: it decides what a run that is not about the shell is given.
func hostHigh() bool { return testShells()[0].high }

// plantedFiles are the files a run inherits, as a parent that leaves them
// open without close-on-exec hands them on: filler at fds 3 to 9, and with
// high the lock stand-in at fd 10 and filler up to hi, the high sentinel.
// ExtraFiles entry i is the child's fd 3+i, so the entries between are
// filler too. One filler file serves for all of them: each entry is dup'd
// onto its own fd.
func plantedFiles(t *testing.T, dir string, high bool, hi int) []*os.File {
	t.Helper()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	var extra []*os.File
	for i := 0; i < 7; i++ {
		extra = append(extra, null)
	}
	if high {
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
	return extra
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
	// The run's fds 3 to 9 are filler and its fd 10 the lock stand-in, where
	// the sh can close them (plantedFiles).
	extra := plantedFiles(t, dir, hostHigh(), hi)
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
	if w.shell != "" {
		cmd = exec.Command(w.shell, w.script)
	}
	cmd.Dir = w.tmp
	cmd.Env = w.env
	cmd.ExtraFiles = w.extra
	return cmd
}

// under runs the script under sh, given what plantedFiles makes of high and
// hi: with high, descriptors above 9 for an sh that cannot close them, which
// the script must then refuse to go on with (fdRun.refused).
func (w *watcherRun) under(t *testing.T, sh testShell, high bool, hi int) {
	t.Helper()
	w.shell = sh.path
	w.extra = plantedFiles(t, filepath.Dir(w.tmp), high, hi)
}

// watcherSleepers is this run's recorded timeout sleep, as pid and ppid.
// It scopes both assertions and cleanup to PIDs created by the fake sleep
// on this test's PATH, so another run's sleep is never signaled.
func watcherSleepers(t *testing.T, record, limit string) [][2]int {
	t.Helper()
	files, err := filepath.Glob(record + ".*")
	if err != nil {
		t.Fatalf("glob sleep records: %v", err)
	}
	fakeSleep := filepath.Join(filepath.Dir(record), "bin", "sleep")
	var found [][2]int
	for _, file := range files {
		pid, err := strconv.Atoi(strings.TrimPrefix(file, record+"."))
		if err != nil {
			continue
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading sleep record %s: %v", file, err)
		}
		first, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
		if first != "argv: "+limit {
			continue
		}
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "stat=", "-o", "command=").CombinedOutput()
		if err != nil && strings.TrimSpace(string(out)) != "" {
			t.Fatalf("ps pid %d: %v\n%s", pid, err, out)
		}
		fields := strings.Fields(string(out))
		if len(fields) < 3 || strings.Contains(fields[1], "Z") {
			continue
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("ps pid %d has invalid parent %q", pid, fields[0])
		}
		command := strings.Join(fields[2:], " ")
		argv := strings.Fields(command)
		if len(argv) == 0 || argv[len(argv)-1] != limit ||
			(filepath.Base(argv[0]) != "sleep" && !strings.Contains(command, fakeSleep)) {
			continue
		}
		found = append(found, [2]int{pid, ppid})
	}
	return found
}

// awaitNoWatcherSleepers fails unless every timeout sleep recorded by this
// run is gone within the wait: a killed sleep takes a moment to die, an
// orphaned one never leaves.
func awaitNoWatcherSleepers(t *testing.T, record, limit string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		strays := watcherSleepers(t, record, limit)
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

// awaitWatcherSleep waits for the watcher's sleep to appear in its files and
// returns its pid and its watcher's: the pid from the files themselves, the
// watcher from one ps lookup of that pid. The files are the fake sleep's
// record for the limit and the nap file holding the same pid; both must
// appear, so the recording is complete before the caller kills the run. The
// one-minute deadline is a fuse against the watcher never starting, not a
// window the fixture must fit in: a full race run can delay process startup.
// Process-table visibility under load decides nothing.
func awaitWatcherSleep(t *testing.T, cmd *exec.Cmd, tmp, record, limit string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	var pid int
	for {
		if p, ok := watcherSleepPidFromFiles(t, tmp, record, limit); ok {
			pid = p
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("the watcher's sleep never wrote its record and nap files")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// One ps lookup of the known pid for the watcher, its parent: the sleep
	// lives as long as the test command, 60 s or gated on a release file,
	// so it is alive however long this one ps takes under load.
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "stat=", "-o", "command=").CombinedOutput()
	if err != nil && strings.TrimSpace(string(out)) != "" {
		t.Fatalf("ps pid %d: %v\n%s", pid, err, out)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 || strings.Contains(fields[1], "Z") {
		t.Fatalf("the watcher's sleep %d from its files is not running", pid)
	}
	ppid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("ps pid %d has invalid parent %q", pid, fields[0])
	}
	command := strings.Join(fields[2:], " ")
	argv := strings.Fields(command)
	fakeSleep := filepath.Join(filepath.Dir(record), "bin", "sleep")
	if len(argv) == 0 || argv[len(argv)-1] != limit ||
		(filepath.Base(argv[0]) != "sleep" && !strings.Contains(command, fakeSleep)) {
		t.Fatalf("the watcher's sleep %d from its files is not sleep %s in ps: %q", pid, limit, command)
	}
	return pid, ppid
}

// watcherSleepPidFromFiles returns the watcher's sleep pid once both its
// files agree: the fake sleep's record for the limit, named with its pid,
// and the nap file in tmp holding the same pid. Either alone is not enough:
// the nap appears before the recording is complete, and the record without
// the nap leaves the caller with no pid file to check.
func watcherSleepPidFromFiles(t *testing.T, tmp, record, limit string) (int, bool) {
	t.Helper()
	files, err := filepath.Glob(record + ".*")
	if err != nil {
		t.Fatalf("glob sleep records: %v", err)
	}
	for _, file := range files {
		pid, err := strconv.Atoi(strings.TrimPrefix(file, record+"."))
		if err != nil {
			continue
		}
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
		if first != "argv: "+limit {
			continue
		}
		if watcherNapHasPid(t, tmp, pid) {
			return pid, true
		}
	}
	return 0, false
}

// watcherNapHasPid reports whether tmp holds a file containing pid: the
// watcher's nap file, holding its sleep's pid. The run's other files are
// empty or hold anything but a bare pid, so content alone tells them apart.
func watcherNapHasPid(t *testing.T, tmp string, pid int) bool {
	t.Helper()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("reading tmp for the nap file: %v", err)
	}
	want := strconv.Itoa(pid)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(tmp, entry.Name()))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == want {
			return true
		}
	}
	return false
}

// killWatcherSleepers kills only this run's recorded timeout sleep: a
// failing run's hygiene without signaling another process.
func killWatcherSleepers(t *testing.T, record, limit string) {
	t.Helper()
	for _, s := range watcherSleepers(t, record, limit) {
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
		t.Cleanup(func() { killWatcherSleepers(t, w.record, limit) })
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
		awaitNoWatcherSleepers(t, w.record, limit)
		return w
	}
	finished("true", "1799", 0)
	// The slow run carries the high sentinel when the limit has room for
	// one, so the watcher proves it closes high descriptors too; too low
	// a limit runs the same slow proof without it, and says so. The test
	// command's own descriptors are another test's proof.
	hi := 0
	if sentinel, ok := highSentinel(t); ok {
		hi = sentinel
	} else {
		t.Logf("open-files limit too low for a high-fd sentinel: the slow run proves no sleep left and clean fds without it")
	}
	w := finished("sleep 2", "1797", hi)
	assertWatcherSleepFds(t, w.record, "1797")
}

// When merge-local itself is killed with TERM, its watcher and the
// watcher's sleep go with it, and so does its test command with everything
// that command started (the sleep below, in a child of its own), before a file
// is taken away: the script reports 143, no sleep it started is left behind,
// and nothing it made is. The TERM goes to the script alone, which is how a
// test command outlived its landing (yardr-s41x). A second goes to its whole
// process group, the rm included, while its files are being removed, as the
// daemon's does, and cuts nothing short.
func TestMergeLocalKilledWithTermLeavesNoSleepBehind(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "testpid")
	w := newWatcherRun(t, "sleep 300 & echo $! >"+shellQuote(pidFile)+"; wait", "1789", 0)
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
	// A slow test command, so merge-local is mid-run when it is killed;
	// its own sleep is killed above, whatever outlives the run.
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
	awaitFile(t, held, "merge-local never began to remove its files")
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
		t.Fatalf("waiting for merge-local: %v", err)
	}
	if code != 143 {
		t.Errorf("merge-local killed with TERM exits %d, want 143", code)
	}
	if syscall.Kill(watcher, 0) == nil {
		t.Errorf("the watcher (pid %d) survived the TERM to merge-local", watcher)
	}
	awaitNoWatcherSleepers(t, w.record, "1789")
	assertWatcherSleepFds(t, w.record, "1789")
	assertNothingLeft(t, made)
}

// A landing's test command starts with no descriptor above 2 that the
// landing did not give it: whatever the parent held open without
// close-on-exec (the daemon's leaked resolver sockets once reached the
// tests: yardr-b4ep) is closed before the command runs. The run inherits
// open files above 2 as a race run holds its lock, with the high sentinel
// when the limit has room for one, and the command lists its own
// descriptors and probes every inherited one: only 0, 1 and 2 may show,
// beside ls's own directory fd, and none of the run's files may answer. It
// does so under every sh the host has (fdRuns); and where an sh cannot close
// what the run holds, the command is not started at all.
func TestMergeLocalTestCommandStartsWithNoInheritedFile(t *testing.T) {
	t.Parallel()
	for _, row := range fdRuns() {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			record := filepath.Join(t.TempDir(), "fds")
			w := newWatcherRun(t, probeCommand(record), "1799", 0)
			w.under(t, row.sh, row.high, row.sentinel(t))
			t.Cleanup(func() { killWatcherSleepers(t, w.record, "1799") })
			// Into a file, not a pipe: an orphaned sleep would hold a pipe open
			// past the run's own exit, and the wait below with it.
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
				t.Fatalf("merge-local (ls /dev/fd): %v\n%s", err, out)
			}
			main, tip := watcherGit(t, w.repo, "rev-parse", "main"), watcherGit(t, w.wt, "rev-parse", "HEAD")
			awaitNoWatcherSleepers(t, w.record, "1799")
			if row.refused {
				assertRefused(t, "merge-local", string(out), record)
				if main == tip {
					t.Fatalf("merge-local landed the branch though the tests were not started:\n%s", out)
				}
				return
			}
			if main != tip {
				t.Fatalf("merge-local (ls /dev/fd): main %s is not the landed branch %s:\n%s", main, tip, out)
			}
			assertNoInheritedFds(t, record)
		})
	}
}

// fdRun is a row of the tests of what a script's test command inherits: a
// shell the script runs under, whether the run is given descriptors above 9,
// and whether the script must refuse to start the command, as it must where
// the shell cannot close them.
type fdRun struct {
	name    string
	sh      testShell
	high    bool
	refused bool
}

// fdRuns is a row for every shell the host has, given what it can close, and
// for a shell that cannot close what is above 9 one more, given a descriptor
// there all the same.
func fdRuns() []fdRun {
	var rows []fdRun
	for _, sh := range testShells() {
		rows = append(rows, fdRun{name: sh.name, sh: sh, high: sh.high})
		if !sh.high {
			rows = append(rows, fdRun{name: sh.name + " with fd 10 open", sh: sh, high: true, refused: true})
		}
	}
	return rows
}

// sentinel is the high sentinel a run of this row is given: one under the
// process's open-files limit when the limit has room for it, and the shell can
// close it. A refused run is given the lock stand-in alone, so that the
// descriptor the script names is the one the test expects.
func (r fdRun) sentinel(t *testing.T) int {
	t.Helper()
	if !r.high || r.refused {
		return 0
	}
	hi, ok := highSentinel(t)
	if !ok {
		t.Logf("open-files limit too low for a high-fd sentinel: the run proves clean fds without it")
	}
	return hi
}

// assertRefused fails unless a script held its bead without starting the test
// command, and said which descriptor it could not close and why. The command
// would have left record.
func assertRefused(t *testing.T, script, out, record string) {
	t.Helper()
	for _, want := range []string{
		"== hold r-1: ",
		script + ": descriptors 10 are open above 9 and this sh cannot close them; not starting the tests with them",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("%s did not hold with %q:\n%s", script, want, out)
		}
	}
	if _, err := os.Stat(record); err == nil {
		t.Fatalf("%s started the test command, which left %s", script, record)
	}
}

// probeCommand is a test command that records its descriptors in record:
// the listing itself, then one "leaked N" line for every fd of the run's
// inherited set 3 to 10 and the high sentinel that still answers. The
// probes are stat-based, so a readdir entry that is no open file is not
// reported; probing 512 when the run carries no sentinel reports nothing.
func probeCommand(record string) string {
	return "ls /dev/fd >" + shellQuote(record) +
		`; for probe in 3 4 5 6 7 8 9 10 512; do` +
		` [ -e /dev/fd/$probe ] || [ -L /dev/fd/$probe ] || continue;` +
		` echo "leaked $probe" >>` + shellQuote(record) + `; done`
}

// assertNoInheritedFds fails unless the record a test command left holds
// no "leaked N" line and its listing nothing at the lock stand-in or
// above: 0, 1, 2 and ls's own directory fd, with room below 10 for what
// the shell and the listing itself hold, as the watcher's sleep allows.
// A missing record fails too: the command ran it, so it exists.
func assertNoInheritedFds(t *testing.T, record string) {
	t.Helper()
	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the test command recorded no descriptor listing: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for _, line := range lines {
		if leaked, ok := strings.CutPrefix(strings.TrimSpace(line), "leaked "); ok {
			t.Fatalf("the test command started with fd %s open", leaked)
		}
		fd, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			t.Fatalf("the test command saw %q in /dev/fd, not a number", line)
		}
		if fd >= 10 {
			t.Fatalf("the test command started with fd %d open: %v", fd, lines)
		}
	}
}

// A malformed zero PID must not signal merge-local's whole process group.
func TestMergeLocalIgnoresZeroWatcherPID(t *testing.T) {
	t.Parallel()
	w := newWatcherRun(t, "sleep 1", "1789", 0)
	// The test command waits for a release file instead of sleeping a fixed
	// second: however long this goroutine is unscheduled, merge-local cannot
	// finish, kill its watcher and remove the nap file before the zero is
	// written. The release path needs w.tmp, so the command is gated here.
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
	if err := os.WriteFile(napfile, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The release is created only after the zero is written: the gated
	// command cannot exit before it, so the zero is always the pid
	// unwatched reads.
	if err := os.WriteFile(release, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("merge-local with a zero watcher PID: %v", err)
	}
	awaitNoWatcherSleepers(t, w.record, "1789")
}

// A local user of a shared TMPDIR can list it, see merge-local's log by name
// and put a symlink at a name made from it before merge-local creates that
// name (yardr-gb50): ".late" for the deadline marker, ".sh" for the copy of
// the base's test script that is then run. A mktemp that plants both symlinks
// at every file it hands back stands in for the user, each symlink to a victim
// file. merge-local must make each of its files with mktemp and name none from
// another's, or it writes through a symlink and a victim changes. The run is
// the base's copy of a script that outlasts its deadline, which writes both.
func TestMergeLocalWritesThroughNoPlantedSymlink(t *testing.T) {
	t.Parallel()
	w := newWatcherRun(t, "check.sh", "1", 0)
	t.Cleanup(func() { killWatcherSleepers(t, w.record, "1") })
	if err := os.WriteFile(filepath.Join(w.repo, "check.sh"), []byte("echo from the base\nsleep 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	watcherGit(t, w.repo, "add", "check.sh")
	watcherGit(t, w.repo, "commit", "-q", "-m", "the test script")

	const precious = "not for merge-local to write\n"
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

	// Into a file, not a pipe, as the runs above: see there.
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
		t.Fatalf("merge-local: %v\n%s", err, out)
	}
	for _, want := range []string{"the base's copy of check.sh", "from the base", "the tests ran past their deadline of 1s and were killed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the run did not use the base's copy up to its deadline (no %q):\n%s", want, out)
		}
	}

	for _, v := range victims {
		if got, err := os.ReadFile(v.path); err != nil || string(got) != precious {
			t.Errorf("merge-local wrote through a symlink planted at the name of %s: the victim holds %q (%v)", v.name, got, err)
		}
	}
	// Each file it made is gone: only the planted symlinks are left.
	entries, err := os.ReadDir(w.tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			t.Errorf("merge-local left %s in TMPDIR", e.Name())
		}
	}
}

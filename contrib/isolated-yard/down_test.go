// Package isolatedyard tests the isolated-yard script: that `down` stops
// what runs in a yard and nothing else. The tests use bare yards (no herdr
// server), so they run where herdr is not installed.
package isolatedyard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// script runs isolated-yard from the repository's root, where `bare`
// builds yardr from.
func script(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "contrib/isolated-yard/isolated-yard"), args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// bare makes a bare yard and returns its directory. Should the test fail
// before its own `down`, the yard is taken down when the test ends.
func bare(t *testing.T) string {
	t.Helper()
	out, err := script(t, "bare")
	if err != nil {
		t.Fatalf("isolated-yard bare: %v\n%s", err, out)
	}
	lines := strings.Split(out, "\n")
	d := lines[len(lines)-1]
	if !strings.HasPrefix(d, "/tmp/iy.") {
		t.Fatalf("isolated-yard bare printed %q", out)
	}
	// By the script, which goes by the yard's directory: a pid the test
	// read earlier may be someone else's by then.
	t.Cleanup(func() {
		if _, err := os.Stat(d); err == nil {
			if out, err := script(t, "down", d); err != nil {
				t.Errorf("cleanup: isolated-yard down %s: %v\n%s", d, err, out)
			}
		}
	})
	return d
}

// in runs a command inside the yard, through its wrapper.
func in(t *testing.T, d string, args ...string) string {
	t.Helper()
	out, err := exec.Command(filepath.Join(d, "e"), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s/e %s: %v\n%s", d, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// pidIn reads the pid a server or daemon left in file, waiting for it.
func pidIn(t *testing.T, file string) int {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		b, _ := os.ReadFile(file)
		line, _, _ := strings.Cut(string(b), "\n")
		pid, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if n, err := strconv.Atoi(pid); err == nil && n > 0 {
			return n
		}
	}
	t.Fatalf("no pid in %s", file)
	return 0
}

// running reports whether pid is a live process (one that ended and was
// not reaped yet is not).
func running(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	s := strings.TrimSpace(string(out))
	return s != "" && !strings.HasPrefix(s, "Z")
}

// down runs `isolated-yard down d` and checks that it removed d and that
// none of pids is left.
func down(t *testing.T, d string, pids ...int) {
	t.Helper()
	out, err := script(t, "down", d)
	if err != nil {
		t.Fatalf("isolated-yard down: %v\n%s", err, out)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Errorf("%s is still there (%v)", d, err)
	}
	for _, pid := range pids {
		if running(pid) {
			t.Errorf("pid %d outlived down:\n%s", pid, out)
		}
	}
}

// startDaemon starts the yard's daemon the way init --start does: in the
// background, in a session of its own.
func startDaemon(t *testing.T, d string) int {
	t.Helper()
	cmd := exec.Command(filepath.Join(d, "e"), "yardr", "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	return pidIn(t, filepath.Join(d, "y", "daemon.lock"))
}

// yardr-npdr: after `go build -o D/bin/yardr`, the yard's server and daemon
// run a file that D/bin no longer names, and a down that went by the files
// in D/bin said "stopped and removed" with both still running.
func TestDownAfterTheBinaryWasRebuilt(t *testing.T) {
	d := bare(t)
	in(t, d, "yardr", "server", "start")
	server := pidIn(t, filepath.Join(d, "y", "server.pid"))
	daemon := startDaemon(t, d)
	// Go's module cache deliberately makes downloaded files read-only. down
	// must still remove a yard after a test populated that cache.
	mod := filepath.Join(d, "h", "go", "pkg", "mod", "example.invalid", "module@v1")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "source.go"), []byte("package module\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mod, 0o555); err != nil {
		t.Fatal(err)
	}

	// Rebuilt the way go build does it and the way an install does it: the
	// file written anew, and a new file renamed over the name.
	bin := filepath.Join(d, "bin", "yardr")
	if out, err := exec.Command("go", "build", "-o", bin, "yardr.dev/yardr").CombinedOutput(); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".new", b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bin+".new", bin); err != nil {
		t.Fatal(err)
	}
	if !running(server) || !running(daemon) {
		t.Fatalf("server running=%v daemon running=%v before down", running(server), running(daemon))
	}
	down(t, d, server, daemon)
}

// yardr-npdr: a server started by another yardr (the installed one) with
// its home in the yard runs no file of D/bin at all.
func TestDownStopsAServerStartedFromAnotherBinary(t *testing.T) {
	d := bare(t)
	other := filepath.Join(t.TempDir(), "yardr")
	if out, err := exec.Command("go", "build", "-o", other, "yardr.dev/yardr").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(d, "y")
	cmd := exec.Command(other, "server", "start")
	cmd.Env = []string{"HOME=" + filepath.Join(d, "h"), "PATH=/usr/bin:/bin", "YARDR_HOME=" + home}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("server start: %v\n%s", err, out)
	}
	server := pidIn(t, filepath.Join(home, "server.pid"))
	if !running(server) {
		t.Fatal("no server before down")
	}
	down(t, d, server)
}

// down stops processes by the directory it is given, so it takes none that
// `up` did not make: not a directory that only has the name of one, and not
// one that carries another yard's mark.
func TestDownRefusesWhatUpDidNotMake(t *testing.T) {
	plain, err := os.MkdirTemp("/tmp", "iy.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(plain) })
	wrapper := func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "e"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wrapper(plain)
	marked, err := os.MkdirTemp("/tmp", "iy.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(marked) })
	wrapper(marked)
	if err := os.WriteFile(filepath.Join(marked, ".isolated-yard"), []byte(plain+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{plain, marked, t.TempDir()} {
		out, err := script(t, "down", dir)
		if err == nil || !strings.Contains(out, "isolated-yard: ") {
			t.Errorf("down %s: err %v, output %q; want a refusal", dir, err, out)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("down %s removed it: %v", dir, err)
		}
	}
}

// match runs `isolated-yard match home` over lines of "<pid> <environment>"
// and returns what it printed and whether it failed.
func match(t *testing.T, home string, lines ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "contrib/isolated-yard/isolated-yard"), "match", home)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return stderr.String(), err
	}
	return strings.Join(strings.Fields(string(out)), " "), nil
}

// yardr-6gyb: the home was escaped into a regular expression by a sed
// expression GNU sed refuses, so on Linux the pattern was empty and every
// process with any YARDR_HOME was one of the yard. The home is compared as
// a string: what a pattern would read as an operator is a character here.
func TestMatchComparesTheHomeAsAString(t *testing.T) {
	for _, home := range []string{
		"/tmp/iy.Ab12Cd",
		"/tmp/iy.a[b]",
		"/tmp/iy.[.x.]",
		"/tmp/iy.a*b+c?",
		"/tmp/iy.with space",
		`/tmp/iy.back\slash$^(x|y)`,
	} {
		got, err := match(t, home,
			"11 PATH=/bin YARDR_HOME="+home+"/y TERM=x",
			"12 YARDR_HOME="+home,
			"13 YARDR_HOME=/private"+home+"/y",
			// Not the yard's: a longer name, another yard, a real yard,
			// no home, the home under another name, and a line of no process.
			"21 YARDR_HOME="+home+"x/y",
			"22 YARDR_HOME=/tmp/iy.other/y",
			"23 YARDR_HOME=/Users/someone/.yardr",
			"24 PATH=/bin",
			"25 NOT_YARDR_HOME="+home+"/y",
			"26 YARDR_HOME=/elsewhere"+home+"/y",
			"x YARDR_HOME="+home+"/y",
		)
		if err != nil || got != "11 12 13" {
			t.Errorf("match %q: %q, %v; want 11 12 13", home, got, err)
		}
	}
	// A home that is all pattern matches itself only.
	if got, err := match(t, "/.*", "1 YARDR_HOME=/tmp/iy.a/y", "2 YARDR_HOME=/.*/y"); err != nil || got != "2" {
		t.Errorf("match /.*: %q, %v; want 2", got, err)
	}
}

// yardr-6gyb: nothing may match every process. No home, "/" and what is no
// absolute path are refused, with a message and no pid.
func TestMatchRefusesAHomeThatWouldMatchEverything(t *testing.T) {
	for _, home := range []string{"", "/", "//", "/tmp/", "tmp/iy.a", "."} {
		out, err := match(t, home, "1 YARDR_HOME=/tmp/iy.a/y", "2 YARDR_HOME=/y", "3 YARDR_HOME=")
		if err == nil || !strings.Contains(out, "isolated-yard: ") {
			t.Errorf("match %q: %q, %v; want a refusal", home, out, err)
		}
	}
}

// The list of a yard's processes is refused, not empty and not everyone's,
// for what is not an isolated yard.
func TestPidsRefusesWhatIsNoYard(t *testing.T) {
	unmarked, err := os.MkdirTemp("/tmp", "iy.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(unmarked) })
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"", "/", "/tmp", "/tmp/iy.", unmarked, unmarked + "/y", "/tmp/iy.nosuchyard", filepath.Join(home, ".yardr"), t.TempDir()} {
		out, err := script(t, "pids", dir)
		if err == nil || !strings.HasPrefix(out, "isolated-yard: ") || strings.Contains(out, "\n") {
			t.Errorf("pids %q: err %v, output %q; want a refusal and nothing else", dir, err, out)
		}
	}
}

// A failed process listing must not look like an empty yard and let down
// remove it without knowing whether any of its processes are still alive.
func TestDownRefusesWhenPsFails(t *testing.T) {
	d := bare(t)
	bin := t.TempDir()
	ps := filepath.Join(bin, "ps")
	if err := os.WriteFile(ps, []byte("#!/bin/sh\necho simulated ps failure >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "contrib/isolated-yard/isolated-yard"), "down", d)
	cmd.Dir = root
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PATH=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "cannot tell the processes") || !strings.Contains(string(out), "ps failed") {
		t.Fatalf("down with failing ps: err %v, output %q; want a refusal", err, out)
	}
	if _, err := os.Stat(d); err != nil {
		t.Fatalf("down removed the yard after ps failed: %v", err)
	}
}

// yardr-jd3w: a version probe (`claude --version`, what yardr doctor runs)
// and a one-shot call (`claude -p`, what yardr doctor --try runs) start no
// agent, so `down` leaves those starts out of what it reports: a trial's
// last line reads as proof of how many agents ran.
func TestDownLeavesProbesOutOfTheStandInCount(t *testing.T) {
	d := bare(t)
	in(t, d, "yardr", "init", "--harness", "claude", "--operator", "jo")
	// doctor exits 1 here (no depot yet); the probe is what matters.
	_, _ = exec.Command(filepath.Join(d, "e"), "yardr", "doctor").CombinedOutput()
	in(t, d, "claude", "-p", "say ok")
	// One real start: the stand-in with no probe args, its stdin closed
	// so it exits.
	in(t, d, "claude")
	log, err := os.ReadFile(filepath.Join(d, "agents.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []string{`args=["--version"]`, `args=["-p" "say ok"]`} {
		if !strings.Contains(string(log), probe) {
			t.Errorf("agents.log has no start with %s:\n%s", probe, log)
		}
	}
	out, err := script(t, "down", d)
	if err != nil {
		t.Fatalf("isolated-yard down: %v\n%s", err, out)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Errorf("%s is still there (%v)", d, err)
	}
	if !strings.Contains(out, "stand-in agents started: 1") {
		t.Errorf("down printed %q; want it to report one agent started", out)
	}
}

// As a sleeper (see there) the test binary only waits to be stopped.
func TestMain(m *testing.M) {
	if os.Getenv("ISOLATED_TEST_SLEEPER") != "" {
		time.Sleep(5 * time.Minute)
		return
	}
	os.Exit(m.Run())
}

// sleeper starts a process with the given YARDR_HOME that does nothing, and
// ends it with the test. It is this test binary, not sleep(1): macOS's ps
// shows no environment for the system's own binaries, so down does not see
// one of those by its home.
func sleeper(t *testing.T, home string) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "ISOLATED_TEST_SLEEPER=1", "YARDR_HOME=" + home}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	return cmd.Process.Pid
}

// yardr-6gyb: down on Linux stopped the processes of every yard on the
// machine, the servers of the tests running beside it among them. A process
// with its home in another yard, in a real one or in a directory that only
// begins like this yard's outlives down.
func TestDownLeavesTheProcessesOfOtherYards(t *testing.T) {
	d := bare(t)
	mine := []int{sleeper(t, d+"/y"), sleeper(t, d)}
	others := []int{
		sleeper(t, d+"x/y"),
		sleeper(t, "/tmp/iy.another/y"),
		sleeper(t, filepath.Join(t.TempDir(), "home")),
		sleeper(t, ""),
		sleeper(t, "/"),
	}
	out, err := script(t, "pids", d)
	if err != nil {
		t.Fatalf("isolated-yard pids: %v\n%s", err, out)
	}
	listed := map[string]bool{}
	for _, f := range strings.Fields(out) {
		listed[f] = true
	}
	for _, pid := range mine {
		if !listed[strconv.Itoa(pid)] {
			t.Errorf("pids lacks %d of the yard: %q", pid, out)
		}
	}
	for _, pid := range others {
		if listed[strconv.Itoa(pid)] {
			t.Errorf("pids has %d, which is not of the yard: %q", pid, out)
		}
	}
	if t.Failed() {
		t.FailNow() // down would signal what pids listed
	}
	down(t, d, mine...)
	for _, pid := range others {
		if !running(pid) {
			t.Errorf("down stopped pid %d, which is not of the yard", pid)
		}
	}
}

// The login shell of a yard finds what its PATH has and no more: yardr asks
// it for a program PATH lacks, and yardr up starts the herdr it finds. With
// the system's profile alone a bare yard's login shell had the machine's own
// directories on PATH, and yardr up there started the machine's herdr, with
// the real agent on its panes' PATH (yardr-0pk6).
func TestLoginShellFindsNothingOfTheMachine(t *testing.T) {
	d := bare(t)
	want := d + "/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	if got := strings.TrimSpace(in(t, d, "/bin/sh", "-l", "-c", "echo $PATH")); got != want {
		t.Fatalf("the login shell's PATH is %s, want %s", got, want)
	}
	if got := strings.TrimSpace(in(t, d, "/bin/sh", "-l", "-c", "command -v herdr || echo none")); got != "none" {
		t.Fatalf("the login shell of a bare yard finds herdr: %s", got)
	}
}

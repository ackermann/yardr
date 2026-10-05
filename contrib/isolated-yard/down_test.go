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
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
			return pid
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

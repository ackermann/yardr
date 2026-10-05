package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The check at the end of the run sees a process a test started and left
// running, with the directory it runs in, and none once it has ended.
func TestLeftBehindNamesARunningChild(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	t.Cleanup(stop)
	find := func(left []child) *child {
		for _, c := range left {
			if c.pid == cmd.Process.Pid {
				return &c
			}
		}
		return nil
	}
	c := find(leftBehind(0))
	if c == nil {
		t.Fatal("a running child is not among those left behind")
	}
	if !strings.Contains(c.command, "sleep 60") {
		t.Errorf("command = %q, want the child's", c.command)
	}
	if got, _ := filepath.EvalSymlinks(c.dir); got != dir {
		t.Errorf("dir = %q, want %q", c.dir, dir)
	}

	// Ended and not yet waited for, it is a zombie: not running.
	_ = cmd.Process.Kill()
	for deadline := time.Now().Add(5 * time.Second); find(leftBehind(0)) != nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a child that has ended is still among those left behind")
		}
	}
	stop()
}

// Started the way yardr starts its server, the test binary does not run the
// suite again (and, in it, a server that nothing stops): it ends at once and
// says what it is.
func TestTheTestBinaryIsNotYardr(t *testing.T) {
	cmd := exec.Command(os.Args[0], "server", "run")
	cmd.Env = append(os.Environ(), "YARDR_HOME="+newHome(t))
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("the test binary run as `server run` did not end: %s", out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "is a test binary, not yardr") {
		t.Fatalf("%v: %s, want exit 2 and the reason", err, out)
	}
}

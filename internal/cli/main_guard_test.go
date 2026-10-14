package cli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/server"
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

// A command that reaches for a store no test gave it reaches no yard of the
// machine the tests run on: not the one the environment of `go test` names
// (an agent runs them in a pane of the operator's yard, with its YARDR_HOME), and
// not the one at the default home of whoever runs them. The first hid a
// test that failed everywhere else from three runs (yardr-j4vm). This test
// names no home: it has the one TestMain left it.
func TestNoCommandReachesTheMachinesYard(t *testing.T) {
	// Where a test that clears the variables ends up: nothing is there.
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.DefaultHome(user)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the default home %s of these tests: %v, want none: TestMain gives them a user's home with no yard", config.DefaultHome(user), err)
	}

	// A server answers at the user's default home, here a user of the
	// test's own. A command run with no flag still gets none.
	user = shortDir(t, "yu")
	t.Setenv("HOME", user)
	live := &config.Config{Home: config.DefaultHome(user)}
	if err := os.MkdirAll(live.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, live, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	for deadline := time.Now().Add(5 * time.Second); !server.Running(ctx, live); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("server did not come up")
		}
	}
	a := &app{actor: "me"}
	if _, err := run(t, a.beadCmd(), "list"); !errors.Is(err, errNoServer) {
		t.Fatalf("bead list with no store and no flag: %v, want %v: it reached a yard of the machine", err, errNoServer)
	}
	if a.cfg == nil || a.cfg.Home == live.Home {
		t.Fatalf("the command's home: %+v, want one with no yard, not %s", a.cfg, live.Home)
	}
}

// killStray ends only a process that is still this run's child: a pid
// freed and reused since the listing is left alone. (yardr-4wfs.)
func TestKillStrayLeavesAReusedPidAlone(t *testing.T) {
	old := strayKill
	var kills []int
	strayKill = func(pid int, sig syscall.Signal) error {
		kills = append(kills, pid)
		return nil
	}
	t.Cleanup(func() { strayKill = old })

	killStray(1) // another process's, never ours
	finished := exec.Command("true")
	if err := finished.Run(); err != nil {
		t.Fatal(err)
	}
	killStray(finished.Process.Pid) // reaped: its pid is free
	if len(kills) != 0 {
		t.Fatalf("kills = %v, want none", kills)
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	killStray(cmd.Process.Pid)
	if len(kills) != 1 || kills[0] != cmd.Process.Pid {
		t.Fatalf("kills = %v, want one, to the running child", kills)
	}
}

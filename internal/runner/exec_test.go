package runner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// After a daemon replace nothing waits for an exec session's process, so
// it exits into a zombie. Alive must reap it and call it gone, and the next
// pass must end the session, or the group's slot stays taken forever.
func TestExecZombieEndsTheSession(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	mustT(t, st.PutGroup(ctx, store.Group{Name: "refinery", Runner: "exec", Limit: 1,
		Config: map[string]any{"cmd": "exit 0", "dir": t.TempDir()}}, "me"))
	_, err := st.AddRoute(ctx, store.Route{Stage: "merge", Group: "refinery"}, "me")
	mustT(t, err)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "land it", Stage: "merge"}, "t")
	mustT(t, err)

	e := &Exec{noReap: true}
	d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"exec": e},
		Backoff: time.Hour}
	mustT(t, d.Reconcile(ctx))
	got, err := st.GetBead(ctx, b.ID)
	mustT(t, err)
	if got.Session == nil {
		t.Fatalf("not started: %+v", got)
	}
	s, err := st.GetSession(ctx, *got.Session)
	mustT(t, err)
	pid, err := pidOf(s.Handle)
	mustT(t, err)
	if _, ok := uintOf(s.Handle["start"]); !ok {
		t.Fatalf("handle has no start time: %v", s.Handle)
	}

	// Wait for the zombie: the process has exited and nobody reaped it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, err := procStat(pid); err == nil && st.state == 'Z' {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process never became a zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("kill(0) on a zombie: %v; the test no longer shows the bug", err)
	}

	mustT(t, d.Reconcile(ctx))
	s, err = st.GetSession(ctx, s.ID)
	mustT(t, err)
	if s.State != "died" {
		t.Fatalf("session state = %q, want died", s.State)
	}
	if _, err := procStat(pid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("zombie not reaped: %v", err)
	}
	if alive, err := e.Alive(ctx, s.Handle); alive || err != nil {
		t.Fatalf("Alive = %v, %v after reaping", alive, err)
	}
}

// A handle whose pid now belongs to a later process is dead, and Stop
// leaves that process alone.
func TestExecStalePidIsNotOurs(t *testing.T) {
	ctx := context.Background()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	mustT(t, cmd.Start())
	pid := cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })

	real, err := procStat(pid)
	mustT(t, err)
	e := &Exec{}
	ours := map[string]any{"pid": pid, "start": float64(real.start)}
	if alive, err := e.Alive(ctx, ours); !alive || err != nil {
		t.Fatalf("Alive with matching start = %v, %v", alive, err)
	}
	stale := map[string]any{"pid": pid, "log": "x", "start": float64(real.start - 1)}
	if alive, err := e.Alive(ctx, stale); alive || err != nil {
		t.Fatalf("Alive with stale start = %v, %v", alive, err)
	}
	mustT(t, e.Stop(ctx, stale))
	select {
	case <-exited:
		t.Fatal("Stop signalled a process the session does not own")
	case <-time.After(300 * time.Millisecond):
	}
}

// A cmd that is no string (`--set cmd=true` stores the boolean) fails the
// session with what was stored and how the string is written, where it said
// only that the group has no cmd. group add refuses such a group; this is
// for one that came another way.
func TestExecSaysWhatCmdIsWhenItIsNoString(t *testing.T) {
	ctx := context.Background()
	start := func(cfg map[string]any) error {
		_, err := (&Exec{noReap: true}).Start(ctx, dispatch.Job{
			Bead:    store.Bead{ID: "b-1"},
			Session: store.Session{ID: "s-1"},
			Group:   store.Group{Name: "tick", Runner: "exec", Config: cfg},
		})
		return err
	}
	for want, cfg := range map[string]map[string]any{
		`group tick: exec runner needs config.cmd as a string, got the JSON boolean true; write --set 'cmd="true"'`:  {"cmd": true},
		`group tick: exec runner needs config.cmd as a string, got the JSON number 1.5; write --set 'cmd="1.5"'`:     {"cmd": 1.5},
		`group tick: exec runner needs config.dir as a string, got the JSON object {"a":1}; write --set 'dir="..."'`: {"cmd": "true", "dir": map[string]any{"a": 1.0}},
		"group tick: exec runner needs config.cmd": {},
	} {
		if err := start(cfg); err == nil || err.Error() != want {
			t.Errorf("Start(%v) = %v, want %s", cfg, err, want)
		}
	}
	if err := start(map[string]any{"cmd": "true", "dir": t.TempDir(), "log": t.TempDir() + "/log"}); err != nil {
		t.Fatalf("the string true is a command: %v", err)
	}
	// A key nothing reads is no reason not to run: the group was stored
	// before the store refused one, and doctor reports it.
	if err := start(map[string]any{"cmd": "true", "dir": t.TempDir(), "log": t.TempDir() + "/log", "idel": "5m"}); err != nil {
		t.Fatalf("a key nothing reads stopped the session: %v", err)
	}
}

// With no config.log a session logs under the yard's home, where it was the
// system temp dir: an isolated yard, whose environment has no TMPDIR, left
// one file per session in /tmp that removing the yard did not take
// (yardr-q2g8). config.log still wins.
func TestExecLogsUnderTheHome(t *testing.T) {
	ctx := context.Background()
	home, tmp := filepath.Join(t.TempDir(), "home"), t.TempDir()
	t.Setenv("TMPDIR", tmp)
	e := &Exec{Home: home, noReap: true}
	start := func(session string, cfg map[string]any) string {
		t.Helper()
		h, err := e.Start(ctx, dispatch.Job{
			Bead:    store.Bead{ID: "b-1"},
			Session: store.Session{ID: session},
			Group:   store.Group{Name: "tick", Runner: "exec", Config: cfg},
		})
		mustT(t, err)
		pid, err := pidOf(h)
		mustT(t, err)
		_, _ = syscall.Wait4(pid, nil, 0, nil) // the command's output is all written
		log, _ := h["log"].(string)
		if b, err := os.ReadFile(log); err != nil || string(b) != "said\n" {
			t.Errorf("session %s: log %s holds %q (%v), want what the command said", session, log, b, err)
		}
		return log
	}
	if got, want := start("s-1", map[string]any{"cmd": "echo said"}), filepath.Join(home, "logs", "sessions", "yard-s-1.log"); got != want {
		t.Errorf("default log = %s, want %s", got, want)
	}
	set := filepath.Join(t.TempDir(), "own.log")
	if got := start("s-2", map[string]any{"cmd": "echo said", "log": set}); got != set {
		t.Errorf("log with config.log = %s, want %s", got, set)
	}
	if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
		t.Errorf("the temp dir holds %v (%v), want nothing", left, err)
	}
}

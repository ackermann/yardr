package runner

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
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
		Config: map[string]any{"cmd": "exit 0", "dir": t.TempDir()}}))
	_, err := st.AddRoute(ctx, store.Route{Stage: "merge", Group: "refinery"})
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
	e := NewExec()
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

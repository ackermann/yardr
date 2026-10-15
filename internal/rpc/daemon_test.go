package rpc_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A pane that rewrites a session's handle over the socket is refused: the
// daemon's writes are the daemon's alone to make over the wire too. The
// yard's own lock is held with another live process recorded as its
// holder, so the test process calls as anyone but the daemon; recording
// the test process instead serves the same calls, as in-process calls
// with no caller are served.
func TestSocketServesTheDaemonsWritesToTheDaemonAlone(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
	ctx := context.Background()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 3}, "jo"))
	b1, err := st.CreateBead(ctx, store.NewBead{Title: "one"}, "jo")
	must(err)
	b2, err := st.CreateBead(ctx, store.NewBead{Title: "two"}, "jo")
	must(err)
	b3, err := st.CreateBead(ctx, store.NewBead{Title: "three"}, "jo")
	must(err)
	s1, err := st.Claim(ctx, b1.ID, "builders", "new", "yard")
	must(err)
	_, err = st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo")
	must(err)
	must(st.CrewStartedFor(ctx, "yardmaster", "", map[string]any{"agent": "yard-yardmaster"}))

	// Unix socket paths are limited to ~108 bytes; t.TempDir is too long.
	dir, err := os.MkdirTemp("", "yr")
	must(err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	must(err)
	srv := &http.Server{Handler: rpc.Handler(st, nil), ConnContext: rpc.ConnContext}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c := rpc.NewClient(sock)

	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Skipf("no sleep to stand in for the daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = sleep.Process.Kill()
		_ = sleep.Wait()
	})
	desc, err := peer.Describe(sleep.Process.Pid)
	must(err)
	t.Cleanup(storetest.HoldDaemonAs(t, home, sleep.Process.Pid, desc.Start))

	forged := map[string]any{"agent": "jo", "shell_pid": os.Getpid()}
	refused := map[string]func() error{
		"SetHandle":   func() error { return c.SetHandle(ctx, s1.ID, forged) },
		"MarkRunning": func() error { return c.MarkRunning(ctx, s1.ID, forged) },
		"Claim": func() error {
			_, err := c.Claim(ctx, b2.ID, "builders", "new", "yard")
			return err
		},
		"MarkCleaned":      func() error { return c.MarkCleaned(ctx, s1.ID) },
		"CrewStartingFor":  func() error { return c.CrewStartingFor(ctx, "yardmaster", "", forged) },
		"SetCrewHandleFor": func() error { return c.SetCrewHandleFor(ctx, "yardmaster", "", forged) },
	}
	for _, method := range []string{"SetHandle", "MarkRunning", "Claim", "MarkCleaned", "CrewStartingFor", "SetCrewHandleFor"} {
		err := refused[method]()
		if err == nil || err.Error() != method+" is the daemon's alone" {
			t.Errorf("%s over the socket as another caller = %v, want %q", method, err, method+" is the daemon's alone")
		}
	}
	// The refused writes changed nothing: the session is still starting
	// with no handle, and the second bead unclaimed.
	if got, err := st.GetSession(ctx, s1.ID); err != nil || got.State != "starting" || len(got.Handle) != 0 {
		t.Errorf("session after refused writes = %+v, %v: want it starting with no handle", got, err)
	}
	if got, err := st.GetBead(ctx, b2.ID); err != nil || got.Session != nil {
		t.Errorf("bead two after refused claim = %+v, %v: want it unclaimed", got, err)
	}
	if got, err := st.GetCrew(ctx, "yardmaster"); err != nil || got.Starting != nil {
		t.Errorf("crew member after refused starting handle = %+v, %v: want no starting handle", got, err)
	}

	// The holder passes: the lock names this process now.
	me, err := peer.Describe(os.Getpid())
	must(err)
	lock := config.At(home).DaemonLock()
	must(os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())+" "+strconv.FormatUint(me.Start, 10)+"\n"), 0o600))
	must(c.MarkRunning(ctx, s1.ID, map[string]any{"agent": "yard-one-new"}))
	must(c.SetHandle(ctx, s1.ID, map[string]any{"agent": "yard-one-new", "pane": "w1:p1"}))
	must(c.MarkCleaned(ctx, s1.ID))
	if _, err := c.Claim(ctx, b2.ID, "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	must(c.CrewStartingFor(ctx, "yardmaster", "", map[string]any{"shell_pid": os.Getpid()}))
	must(c.SetCrewHandleFor(ctx, "yardmaster", "", map[string]any{"agent": "yard-yardmaster", "pane": "w1:p2"}))
	if got, err := st.GetSession(ctx, s1.ID); err != nil || got.State != "running" || !got.Cleaned {
		t.Errorf("session after the holder's writes = %+v, %v: want it running and cleaned", got, err)
	}

	// No caller passes: the owning process makes the same writes.
	s3, err := st.Claim(ctx, b3.ID, "builders", "new", "yard")
	must(err)
	must(st.MarkRunning(ctx, s3.ID, map[string]any{"agent": "yard-three-new"}))
	must(st.SetHandle(ctx, s3.ID, map[string]any{"agent": "yard-three-new"}))
	must(st.MarkCleaned(ctx, s3.ID))
	must(st.CrewStartingFor(ctx, "yardmaster", "", map[string]any{"agent": "yard-yardmaster"}))
	must(st.SetCrewHandleFor(ctx, "yardmaster", "", map[string]any{"agent": "yard-yardmaster"}))
}

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A second daemon on the same home exits with "already running" before it
// touches the store; once the first one lets go, the next one starts.
func TestSecondDaemonRefused(t *testing.T) {
	home, err := os.MkdirTemp("", "yd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("YARDR_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_STATE_HOME", home)

	unlock, err := lockDaemon(filepath.Join(home, "daemon.lock"), 0)
	if err != nil {
		t.Fatal(err)
	}
	// A refused daemon must not get as far as dispatching: the context is
	// already cancelled, so a missing lock ends Run at once instead of
	// hanging, and the error below says why the test failed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &app{st: storetest.New(t), actor: "me"}
	cmd := a.daemonCmd()
	cmd.SetArgs(nil)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err = cmd.ExecuteContext(ctx)
	want := "yardr daemon already running (pid " + strconv.Itoa(os.Getpid()) + " holds "
	if !errors.Is(err, errDaemonRunning) || !strings.Contains(err.Error(), want) {
		t.Fatalf("second daemon: %v, want %q", err, want)
	}

	unlock()
	again, err := lockDaemon(filepath.Join(home, "daemon.lock"), 0)
	if err != nil {
		t.Fatalf("lock after the first daemon let go: %v", err)
	}
	again()
}

// The lock waits out a holder that lets go within the wait: a replaced
// daemon's new image takes the lock its old image held.
func TestDaemonLockWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	unlock, err := lockDaemon(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(100*time.Millisecond, unlock)
	next, err := lockDaemon(path, 5*time.Second)
	if err != nil {
		t.Fatalf("lock after the holder let go: %v", err)
	}
	next()
}

// The lock's descriptor is close-on-exec: a self-replacing exec releases
// it for the new image, and runner children never inherit (and so never
// keep) it.
func TestDaemonLockClosedOnExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	unlock, err := lockDaemon(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// Find the descriptor holding path among ours.
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc:", err)
	}
	found := false
	for _, e := range fds {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil || target != path {
			continue
		}
		fd, _ := strconv.Atoi(e.Name())
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if errno != 0 {
			t.Fatal(errno)
		}
		if flags&syscall.FD_CLOEXEC == 0 {
			t.Fatalf("fd %d on %s is inherited across exec", fd, path)
		}
		found = true
	}
	if !found {
		t.Fatalf("no descriptor open on %s", path)
	}
}

// bead show --json carries the revision, and --revision makes update, hold
// and the moves compare-and-swap on it.
func TestBeadRevisionFlag(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me", json: true}
	out, err := run(t, a.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var shown struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.Revision != b.Revision {
		t.Fatalf("show --json revision = %q (%v), want %q", shown.Revision, err, b.Revision)
	}
	if _, err := run(t, a.beadCmd(), "update", b.ID, "--title", "y", "--revision", shown.Revision); err != nil {
		t.Fatalf("update at the shown revision: %v", err)
	}
	cur, _ := st.GetBead(ctx, b.ID)
	for _, args := range [][]string{
		{"update", b.ID, "--title", "z"},
		{"hold", b.ID},
		{"advance", b.ID, "--to", "review"},
		{"close", b.ID},
	} {
		_, err := run(t, a.beadCmd(), append(args, "--revision", shown.Revision)...)
		if !errors.Is(err, store.ErrConflict) {
			t.Errorf("%s at a stale revision: %v, want a conflict", args[0], err)
		}
	}
	if got, _ := st.GetBead(ctx, b.ID); got.Revision != cur.Revision {
		t.Fatalf("a refused command changed the bead: %+v", got)
	}
}

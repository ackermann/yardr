package cli

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// daemonHome is a home with the daemon's lock held, and the context the
// daemon would run under.
func daemonHome(t *testing.T) (*config.Config, context.Context, *syncBuffer) {
	t.Helper()
	home, err := os.MkdirTemp("", "yd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	cfg := &config.Config{Home: home}
	unlock, err := lockDaemon(cfg.DaemonLock(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	logged := &syncBuffer{}
	ctx, release, err := whileHomeStands(context.Background(), cfg, 10*time.Millisecond, slog.New(slog.NewTextHandler(logged, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return cfg, ctx, logged
}

func stopped(t *testing.T, ctx context.Context, logged *syncBuffer, want string) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon was not told to stop")
	}
	// The line is written before the context ends.
	if n := strings.Count(logged.String(), "no longer its own"); n != 1 || !strings.Contains(logged.String(), want) {
		t.Fatalf("want one line saying %q, got:\n%s", want, logged.String())
	}
}

// A daemon whose home was removed under it kept running, like the server
// (yardr-npdr): a detached process no command of the yard could reach.
func TestDaemonStopsWhenItsHomeIsRemoved(t *testing.T) {
	cfg, ctx, logged := daemonHome(t)
	if err := os.RemoveAll(cfg.Home); err != nil {
		t.Fatal(err)
	}
	stopped(t, ctx, logged, "is gone")
}

// The home's name holds another yard now, with a daemon lock of its own.
func TestDaemonStopsWhenItsLockIsAnothers(t *testing.T) {
	cfg, ctx, logged := daemonHome(t)
	// Renamed over, in one step: removed and then written, a check that fell
	// between the two saw no lock and said "is gone" (yardr-5vsd).
	other := cfg.DaemonLock() + ".new"
	if err := os.WriteFile(other, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, cfg.DaemonLock()); err != nil {
		t.Fatal(err)
	}
	stopped(t, ctx, logged, "another file")
}

func TestDaemonRunsOnWhileItsHomeStands(t *testing.T) {
	cfg, ctx, logged := daemonHome(t)
	// A second daemon that is refused opens the lock file; nothing is lost.
	if _, err := lockDaemon(cfg.DaemonLock(), 0); err == nil {
		t.Fatal("a second lock was given")
	}
	select {
	case <-ctx.Done():
		t.Fatalf("stopped:\n%s", logged.String())
	case <-time.After(150 * time.Millisecond):
	}
}

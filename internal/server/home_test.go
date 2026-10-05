package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/rpc"
)

// lockedBuffer is a log a test reads while the server writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// stopsByItself waits for the server to end without being told to, and
// returns what it logged about its home.
func stopsByItself(t *testing.T, done <-chan error, logged *lockedBuffer) string {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the server ended with %v, want a plain stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server kept running")
	}
	var lines []string
	for _, l := range strings.Split(logged.String(), "\n") {
		if strings.Contains(l, "no longer its own") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("want one line about the home, got %d:\n%s", len(lines), logged.String())
	}
	return lines[0]
}

// A server whose home was removed under it (a test's temp directory, an
// isolated yard torn down) was left running until someone found it by pid.
func TestServerStopsWhenItsHomeIsRemoved(t *testing.T) {
	c := home(t)
	logged := &lockedBuffer{}
	done := start(t, &runner{c: c, rev: "r", homeCheck: 20 * time.Millisecond,
		log: slog.New(slog.NewTextHandler(logged, nil))})
	if err := os.RemoveAll(c.Home); err != nil {
		t.Fatal(err)
	}
	if line := stopsByItself(t, done, logged); !strings.Contains(line, "is gone") {
		t.Fatalf("log line: %s", line)
	}
	// It must not make the home again on its way out.
	if _, err := os.Stat(c.Home); !os.IsNotExist(err) {
		t.Fatalf("the home is back: %v", err)
	}
}

// The home was removed and one of the same name made, with a server of its
// own: the first server must go, and leave the second one's files alone.
func TestServerStopsWhenItsSocketIsAnotherServers(t *testing.T) {
	c := home(t)
	logged := &lockedBuffer{}
	done := start(t, &runner{c: c, rev: "r", homeCheck: 20 * time.Millisecond,
		log: slog.New(slog.NewTextHandler(logged, nil))})

	// The other server's pidfile comes first: a server that has seen the
	// other socket reads the pidfile at once, and removes the one that still
	// names it.
	if err := os.WriteFile(c.PIDFile(), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Renamed over, in one step: removed and then listened on, a check that
	// fell between the two saw no socket and said "is gone" (yardr-xx58).
	other, err := net.Listen("unix", c.Socket()+".new")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// Closing unlinks the name the listener was made with, which is no
	// longer its socket's.
	other.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Rename(c.Socket()+".new", c.Socket()); err != nil {
		t.Fatal(err)
	}

	if line := stopsByItself(t, done, logged); !strings.Contains(line, "another file") {
		t.Fatalf("log line: %s", line)
	}
	if _, err := os.Stat(c.Socket()); err != nil {
		t.Fatalf("the other server's socket was removed: %v", err)
	}
	if b, _ := os.ReadFile(c.PIDFile()); string(b) != "1\n" {
		t.Fatalf("the other server's pidfile is %q", b)
	}
}

// A socket removed with no server in its place: the server that goes still
// owns the pidfile, and a stale one would name a pid that is someone else's
// tomorrow.
func TestServerThatLostItsSocketTakesItsPidfileAlong(t *testing.T) {
	c := home(t)
	logged := &lockedBuffer{}
	done := start(t, &runner{c: c, rev: "r", homeCheck: 20 * time.Millisecond,
		log: slog.New(slog.NewTextHandler(logged, nil))})
	if err := os.Remove(c.Socket()); err != nil {
		t.Fatal(err)
	}
	stopsByItself(t, done, logged)
	if _, err := os.Stat(c.PIDFile()); !os.IsNotExist(err) {
		t.Fatalf("pidfile left behind: %v", err)
	}
}

func TestServerKeepsRunningWhileItsHomeStands(t *testing.T) {
	c := home(t)
	done := start(t, &runner{c: c, rev: "r", homeCheck: 5 * time.Millisecond})
	// Use the home as a yard does: files come and go beside the socket.
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(c.HookState()+".x", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		os.Remove(c.HookState() + ".x")
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("the server stopped: %v", err)
	default:
	}
	if err := rpc.NewClient(c.Socket()).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

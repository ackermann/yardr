package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/rpc"
)

// stopsWithItsOwner waits for the server to end without being told to, and
// returns what it logged about its test owner.
func stopsWithItsOwner(t *testing.T, done <-chan error, logged *lockedBuffer) string {
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
		if strings.Contains(l, "the test that started") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("want one line about the test owner, got %d:\n%s", len(lines), logged.String())
	}
	return lines[0]
}

// owner is a stand-in for the test process: a sleep with its pid and start
// time, killed and reaped by the caller.
func owner(t *testing.T) (pid int, start uint64, kill func()) {
	t.Helper()
	sleep := exec.Command("sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sleep.Process.Kill(); sleep.Wait() })
	if st, err := peer.Describe(sleep.Process.Pid); err == nil {
		start = st.Start
	}
	return sleep.Process.Pid, start, func() {
		sleep.Process.Kill()
		sleep.Wait()
	}
}

// A server a test starts stops by itself when the test process is gone (a
// test binary killed by its timeout runs no cleanup): the home it leaves
// intact gives the home watch nothing to act on, so the owner watch stops
// it through the normal path instead.
func TestServerStopsWhenItsTestOwnerIsGone(t *testing.T) {
	pid, begin, kill := owner(t)
	t.Setenv("YARDR_TEST_OWNER", fmt.Sprintf("%d %d", pid, begin))
	c := home(t)
	logged := &lockedBuffer{}
	done := start(t, &runner{c: c, rev: "r", homeCheck: 20 * time.Millisecond,
		log: slog.New(slog.NewTextHandler(logged, nil))})
	kill()
	if line := stopsWithItsOwner(t, done, logged); !strings.Contains(line, "is gone") {
		t.Fatalf("log line: %s", line)
	}
	// The normal path: the home, socket and pidfiles are still this
	// server's, and the stop removes them.
	if _, err := os.Stat(c.Socket()); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
	if _, err := os.Stat(c.PIDFile()); !os.IsNotExist(err) {
		t.Fatalf("pidfile left behind: %v", err)
	}
	if _, err := os.Stat(c.PIDStart()); !os.IsNotExist(err) {
		t.Fatalf("start file left behind: %v", err)
	}
}

func TestServerKeepsServingWhileItsTestOwnerLives(t *testing.T) {
	var begin uint64
	if st, err := peer.Describe(os.Getpid()); err == nil {
		begin = st.Start
	}
	t.Setenv("YARDR_TEST_OWNER", fmt.Sprintf("%d %d", os.Getpid(), begin))
	c := home(t)
	done := start(t, &runner{c: c, rev: "r", homeCheck: 5 * time.Millisecond})
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the server stopped: %v", err)
	default:
	}
	if err := rpc.NewClient(c.Socket()).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A stray value cannot stop a real yard: a malformed owner is one warning,
// and the server serves on.
func TestServerIgnoresAMalformedTestOwner(t *testing.T) {
	t.Setenv("YARDR_TEST_OWNER", "not a pid")
	c := home(t)
	logged := &lockedBuffer{}
	done := start(t, &runner{c: c, rev: "r", homeCheck: 5 * time.Millisecond,
		log: slog.New(slog.NewTextHandler(logged, nil))})
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the server stopped: %v", err)
	default:
	}
	if err := rpc.NewClient(c.Socket()).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logged.String(), "malformed YARDR_TEST_OWNER"); n != 1 {
		t.Fatalf("want one warning about the owner, got %d:\n%s", n, logged.String())
	}
}

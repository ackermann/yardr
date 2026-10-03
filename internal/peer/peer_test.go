package peer

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// The peer of a unix connection is the connecting process, and its
// ancestors begin with it and its parent.
func TestPIDAndAncestors(t *testing.T) {
	dir, err := os.MkdirTemp("", "yp") // t.TempDir is too long for a socket path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			t.Error(err)
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	if server == nil {
		t.FailNow()
	}
	defer server.Close()
	pid, err := PID(server)
	if err != nil {
		t.Fatalf("PID: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("peer pid %d, want this process %d", pid, os.Getpid())
	}
	anc := Ancestors(os.Getpid())
	if !slices.Equal(anc[:2], []int{os.Getpid(), os.Getppid()}) {
		t.Fatalf("ancestors %v, want to begin with %d, %d", anc, os.Getpid(), os.Getppid())
	}
	// A pid no process has is nobody's child.
	if p, err := Parent(1<<22 - 7); err != nil || p != 0 {
		t.Fatalf("parent of a free pid = %d, %v", p, err)
	}
	if _, err := PID(fakeConn{}); err == nil {
		t.Fatal("a non-unix connection has a peer")
	}
}

// Alive tells a process from a later one with its pid by the start time,
// and knows a reaped one is gone.
func TestAlive(t *testing.T) {
	if !HaveStat() {
		t.Skip("this OS reports no process state")
	}
	me := os.Getpid()
	st, err := Describe(me)
	if err != nil {
		t.Fatal(err)
	}
	if !Alive(me, st.Start) {
		t.Fatal("this process is not alive")
	}
	if Alive(me, st.Start+1) {
		t.Fatal("another start time is the same process")
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if Alive(cmd.Process.Pid, 0) {
		t.Fatal("a reaped process is alive")
	}
	if _, err := Describe(cmd.Process.Pid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a reaped process: %v, want %v", err, fs.ErrNotExist)
	}
}

type fakeConn struct{ net.Conn }

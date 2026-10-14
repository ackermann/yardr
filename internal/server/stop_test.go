package server

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
)

// decoy starts a process that stands in for the server, or for an unrelated
// process holding its pid: it sleeps until it is signalled, and is reaped
// when the test ends.
func decoy(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap it when it exits: an unreaped child answers kill as if it
	// still ran, which a stop would wait out.
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	return cmd
}

// stale writes a pidfile naming pid with recorded start, as a server that
// has since died left them.
func stale(t *testing.T, c *config.Config, pid int, start uint64) {
	t.Helper()
	if err := os.WriteFile(c.PIDFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.PIDStart(), []byte(strconv.FormatUint(start, 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A stale pidfile whose pid another process has reused must not get that
// process signalled: Stop reports the server gone and signals nothing, and
// alive reads it as no server (yardr-yz19).
func TestStopLeavesAReusedPIDAlone(t *testing.T) {
	if !peer.HaveStat() {
		t.Skip("the OS reports no process start times")
	}
	c := home(t)
	sleeper := decoy(t)
	pid := sleeper.Process.Pid
	real, err := peer.Describe(pid)
	if err != nil {
		t.Fatal(err)
	}
	stale(t, c, pid, real.Start+1)
	if err := Stop(c); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("Stop signalled the decoy: %v", err)
	}
	if alive(c) {
		t.Fatal("alive: a reused pid reads as the running server")
	}
}

// The server the pidfile names still stops: a pid whose start time matches
// the recorded one is signalled.
func TestStopStopsTheServerItNames(t *testing.T) {
	c := home(t)
	sleeper := decoy(t)
	pid := sleeper.Process.Pid
	if st, err := peer.Describe(pid); err == nil {
		stale(t, c, pid, st.Start)
	} else if err := os.WriteFile(c.PIDFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Stop(c); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("Stop left the server running")
	}
}

// A pidfile without a recorded start names a process Stop cannot verify:
// a live one is refused, not signalled.
func TestStopRefusesAPidItCannotVerify(t *testing.T) {
	if !peer.HaveStat() {
		t.Skip("without start times the pid is trusted, as before")
	}
	c := home(t)
	sleeper := decoy(t)
	pid := sleeper.Process.Pid
	if err := os.WriteFile(c.PIDFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Stop(c); err == nil {
		t.Fatal("Stop signalled a pid it could not verify")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("Stop signalled the decoy: %v", err)
	}
	if alive(c) {
		t.Fatal("alive: a pid with no recorded start reads as the running server")
	}
}

// A pidfile naming no process (0, or a negative id that would signal a
// group) is refused before anything is signalled.
func TestStopRefusesAnInvalidPID(t *testing.T) {
	c := home(t)
	for _, pid := range []string{"0", "-3"} {
		if err := os.WriteFile(c.PIDFile(), []byte(pid+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if alive(c) {
			t.Fatalf("alive: pidfile %q reads as a running server", pid)
		}
		if err := Stop(c); err == nil {
			t.Fatalf("Stop on pidfile %q: no error", pid)
		}
	}
}

// A pidfile whose pid is dead is no server, as before: Stop signals nothing
// and says so without an error.
func TestStopOnADeadPIDReportsGone(t *testing.T) {
	c := home(t)
	sleeper := decoy(t)
	pid := sleeper.Process.Pid
	var start uint64 = 1
	if st, err := peer.Describe(pid); err == nil {
		start = st.Start + 1
	}
	if err := sleeper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// The reaper takes it from a zombie to gone; wait for gone, so the
	// pidfile names a dead pid and not a dying one.
	for i := 0; i < 200 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("the decoy would not die")
	}
	stale(t, c, pid, start)
	if alive(c) {
		t.Fatal("alive: a dead pid reads as the running server")
	}
	if err := Stop(c); err != nil {
		t.Fatalf("Stop on a dead pid: %v", err)
	}
}

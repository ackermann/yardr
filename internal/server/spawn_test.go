package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// What SpawnDetached starts is in a session of its own, so that it outlives
// the starter's terminal, appends what it prints to the log, and its end
// reaches the starter that still runs.
func TestSpawnDetached(t *testing.T) {
	log := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(log, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "echo out; echo err >&2; sleep 0.5; exit 3")
	exited, err := SpawnDetached(cmd, log)
	if err != nil {
		t.Fatal(err)
	}
	if sid, err := unix.Getsid(cmd.Process.Pid); err != nil || sid != cmd.Process.Pid {
		t.Errorf("session %d of pid %d (%v): not its own", sid, cmd.Process.Pid, err)
	}
	select {
	case err := <-exited:
		if err == nil || !strings.Contains(err.Error(), "exit status 3") {
			t.Fatalf("end = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("its end did not come")
	}
	b, _ := os.ReadFile(log)
	if got := string(b); !strings.HasPrefix(got, "before\n") || !strings.Contains(got, "out\n") || !strings.Contains(got, "err\n") {
		t.Fatalf("log = %q", got)
	}
	// A log that cannot be opened starts nothing.
	cmd = exec.Command("/bin/sh", "-c", "true")
	if _, err := SpawnDetached(cmd, filepath.Join(t.TempDir(), "none", "x.log")); err == nil || cmd.Process != nil {
		t.Fatalf("err = %v, started %v", err, cmd.Process)
	}
}

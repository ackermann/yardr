package cli

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"syscall"
	"testing"

	"yardr.dev/yardr/internal/peer"
)

// Cancelling never signals a pid another process has reused since the
// command was waited for: Cancel also runs for a deferred cancel after
// a success. (yardr-4wfs: every one of those fired at the freed pid.)
func TestOwnGroupCancelLeavesAReusedPidAlone(t *testing.T) {
	oldKill, oldDescribe := ownGroupKill, ownGroupDescribe
	var kills []int
	ownGroupKill = func(pid int, sig syscall.Signal) error {
		kills = append(kills, pid)
		return nil
	}
	ownGroupDescribe = func(pid int) (peer.Stat, error) {
		return peer.Stat{State: 'R', PPID: 1}, nil // another process's now
	}
	t.Cleanup(func() { ownGroupKill, ownGroupDescribe = oldKill, oldDescribe })

	cmd := exec.CommandContext(context.Background(), "true")
	ownGroup(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatal(err)
	}
	if len(kills) != 0 {
		t.Fatalf("kills = %v, want none: the pid is foreign", kills)
	}
}

// While the command runs, cancelling kills its group as before, and a
// pid with no process still gets the signal: one of ours may remain in
// its group.
func TestOwnGroupCancelWhileRunningKillsTheGroup(t *testing.T) {
	old := ownGroupKill
	var kills []int
	ownGroupKill = func(pid int, sig syscall.Signal) error {
		kills = append(kills, pid)
		return nil
	}
	t.Cleanup(func() { ownGroupKill = old })

	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if err := cmd.Cancel(); err != nil {
		t.Fatal(err)
	}
	if len(kills) != 1 || kills[0] != -cmd.Process.Pid {
		t.Fatalf("kills = %v, want one to %d", kills, -cmd.Process.Pid)
	}

	done := exec.CommandContext(context.Background(), "true")
	ownGroup(done)
	if err := done.Run(); err != nil {
		t.Fatal(err)
	}
	if err := done.Cancel(); err != nil {
		t.Fatal(err)
	}
	if len(kills) != 2 {
		t.Fatalf("kills = %v, want the gone pid signalled too", kills)
	}
}

func TestOwnGroupCancelDoesNotSignalWhenIdentityIsUnknown(t *testing.T) {
	oldKill, oldDescribe := ownGroupKill, ownGroupDescribe
	wantErr := errors.New("process table unavailable")
	var kills []int
	ownGroupKill = func(pid int, sig syscall.Signal) error {
		kills = append(kills, pid)
		return nil
	}
	ownGroupDescribe = func(int) (peer.Stat, error) { return peer.Stat{}, wantErr }
	t.Cleanup(func() { ownGroupKill, ownGroupDescribe = oldKill, oldDescribe })

	cmd := exec.CommandContext(context.Background(), "true")
	ownGroup(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Cancel(); !errors.Is(err, wantErr) {
		t.Fatalf("Cancel = %v, want %v", err, wantErr)
	}
	if len(kills) != 0 {
		t.Fatalf("kills = %v, want none while identity is unknown", kills)
	}
}

func TestOwnGroupCancelStillCleansAGroupWithoutItsLeader(t *testing.T) {
	oldKill, oldDescribe := ownGroupKill, ownGroupDescribe
	var kills []int
	ownGroupKill = func(pid int, sig syscall.Signal) error {
		kills = append(kills, pid)
		return nil
	}
	ownGroupDescribe = func(int) (peer.Stat, error) { return peer.Stat{}, fs.ErrNotExist }
	t.Cleanup(func() { ownGroupKill, ownGroupDescribe = oldKill, oldDescribe })

	cmd := exec.CommandContext(context.Background(), "true")
	ownGroup(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatal(err)
	}
	if len(kills) != 1 || kills[0] != -cmd.Process.Pid {
		t.Fatalf("kills = %v, want the process group for its surviving children", kills)
	}
}

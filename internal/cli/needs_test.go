package cli

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/scripttest"
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

// A herdr older than herdrOldest is said to be, by the numbers of its
// version and not their text (0.10 is newer than 0.9), with a prerelease
// below its release and build metadata ignored, as semver orders them; one
// whose version flag fails or prints no herdr version is not called old, and
// is asked for its server as before.
func TestHerdrTooOld(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, c := range []struct{ name, script, want string }{
		{"old", "echo 'herdr 0.7.3'", "herdr 0.7.3 is older than " + herdrOldest + ", the oldest yardr starts agents with"},
		{"just older", "echo 'herdr 0.8.9'", "herdr 0.8.9 is older than " + herdrOldest + ", the oldest yardr starts agents with"},
		{"oldest", "echo 'herdr " + herdrOldest + "'", ""},
		{"prerelease of oldest", "echo 'herdr 0.9.0-rc.1'", "herdr 0.9.0-rc.1 is older than " + herdrOldest + ", the oldest yardr starts agents with"},
		{"prerelease of newer", "echo 'herdr 0.9.1-preview.2'", ""},
		{"build metadata", "echo 'herdr 0.9.0+build.5'", ""},
		{"newer", "echo 'herdr 0.9.5'", ""},
		{"two digits", "echo 'herdr 0.10.1'", ""},
		{"another program", "echo 'tool 0.1.0'", ""},
		{"json", `echo '{"id":"cli:x","result":{"agents":[]}}'`, ""},
		{"fails", "echo 'config.toml: bad key' >&2; exit 2", ""},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-"))
		scripttest.Write(t, path, "#!/bin/sh\n"+c.script+"\n")
		if got := herdrTooOld(context.Background(), path); got != c.want {
			t.Errorf("%s: herdrTooOld = %q, want %q", c.name, got, c.want)
		}
	}
}

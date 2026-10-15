package store_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The daemon's writes are refused to any other caller while a daemon runs:
// each is the daemon's alone, whatever else of the call is wrong (the
// check runs before any validation). The lock is held with another live
// process recorded as its holder, so the refusal is for not being the
// holder, not for a stale record.
func TestDaemonWritesRefusedToAnotherCaller(t *testing.T) {
	home := t.TempDir()
	s := storetest.Open(t, filepath.Join(home, "yard.db"))
	ctx := context.Background()

	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Skipf("no sleep to stand in for the daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = sleep.Process.Kill()
		_ = sleep.Wait()
	})
	desc, err := peer.Describe(sleep.Process.Pid)
	if err != nil {
		t.Fatalf("describe sleep: %v", err)
	}
	t.Cleanup(storetest.HoldDaemonAs(t, home, sleep.Process.Pid, desc.Start))

	other := store.WithCaller(ctx, os.Getpid())
	calls := map[string]func() error{
		"Claim": func() error {
			_, err := s.Claim(other, "no-such-bead", "g", "new", "yard")
			return err
		},
		"MarkRunning": func() error {
			return s.MarkRunning(other, "no-such-session", nil)
		},
		"SetHandle": func() error {
			return s.SetHandle(other, "no-such-session", map[string]any{"agent": "jo"})
		},
		"MarkCleaned": func() error {
			return s.MarkCleaned(other, "no-such-session")
		},
		"CrewStartedFor": func() error {
			return s.CrewStartedFor(other, "nope", "", nil)
		},
		"CrewSettledFor": func() error {
			return s.CrewSettledFor(other, "nope", "")
		},
		"CrewStatusFor": func() error {
			return s.CrewStatusFor(other, "nope", "", "bogus")
		},
		"SetCrewHandleFor": func() error {
			return s.SetCrewHandleFor(other, "nope", "", nil)
		},
		"CrewGoneFor": func() error {
			return s.CrewGoneFor(other, "nope", "", "gone")
		},
		"CrewLeftFor": func() error {
			return s.CrewLeftFor(other, "nope", "", nil)
		},
		"CrewStartFailedFor": func() error {
			return s.CrewStartFailedFor(other, "nope", "", "boom", 0)
		},
		"MarkRotationAskedFor": func() error {
			return s.MarkRotationAskedFor(other, "nope", "")
		},
		"MarkNotifiedFor": func() error {
			return s.MarkNotifiedFor(other, "nope", "", 1)
		},
	}
	for _, method := range []string{"Claim", "MarkRunning", "SetHandle", "MarkCleaned",
		"CrewStartedFor", "CrewSettledFor", "CrewStatusFor", "SetCrewHandleFor",
		"CrewGoneFor", "CrewLeftFor", "CrewStartFailedFor",
		"MarkRotationAskedFor", "MarkNotifiedFor"} {
		err := calls[method]()
		if err == nil || err.Error() != method+" is the daemon's alone" {
			t.Errorf("%s from another caller = %v, want %q", method, err, method+" is the daemon's alone")
		}
	}

	// What the daemon does not alone make is not refused as its own:
	// ending a session stays any caller's (yardr down ends them).
	if err := s.EndSession(other, "no-such-session", "died", "gone"); err == nil ||
		err.Error() == "EndSession is the daemon's alone" {
		t.Errorf("EndSession from another caller = %v, want no daemon refusal", err)
	}
}

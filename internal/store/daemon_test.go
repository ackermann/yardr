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
	t.Parallel()
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
		"RecordUsage": func() error {
			return s.RecordUsage(other, "no-such-session", map[string]any{"input": 1})
		},
		"MarkCleaned": func() error {
			return s.MarkCleaned(other, "no-such-session")
		},
		"CrewStartingFor": func() error {
			return s.CrewStartingFor(other, "nope", "", map[string]any{"shell_pid": os.Getpid()})
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
		"MarkContextAskedFor": func() error {
			return s.MarkContextAskedFor(other, "nope", "", 60, 200000)
		},
		"SetContextFor": func() error {
			return s.SetContextFor(other, "nope", "", 60, 200000)
		},
		"MarkNotifiedFor": func() error {
			return s.MarkNotifiedFor(other, "nope", "", 1)
		},
		"RecordCrewUsage": func() error {
			return s.RecordCrewUsage(other, "nope", "", "a1", map[string]any{"input": 1})
		},
		"EndSession": func() error {
			return s.EndSession(other, "no-such-session", "died", "gone")
		},
		"MarkStopped": func() error {
			return s.MarkStopped(other, "no-such-session")
		},
		"MarkCountDue": func() error {
			return s.MarkCountDue(other, "no-such-session")
		},
	}
	for _, method := range []string{"Claim", "MarkRunning", "SetHandle", "RecordUsage", "MarkCleaned", "CrewStartingFor",
		"CrewStartedFor", "CrewSettledFor", "CrewStatusFor", "SetCrewHandleFor",
		"CrewGoneFor", "CrewLeftFor", "CrewStartFailedFor",
		"MarkRotationAskedFor", "MarkContextAskedFor", "SetContextFor", "MarkNotifiedFor", "RecordCrewUsage",
		"EndSession", "MarkStopped", "MarkCountDue"} {
		err := calls[method]()
		if err == nil || err.Error() != method+" is the daemon's alone" {
			t.Errorf("%s from another caller = %v, want %q", method, err, method+" is the daemon's alone")
		}
	}
	// yardr down only clears a start in progress; it cannot set a handle a
	// pane check would trust.
	if err := s.CrewStartingFor(other, "nope", "", nil); err == nil ||
		err.Error() == "CrewStartingFor is the daemon's alone" {
		t.Errorf("clearing a starting handle from another caller = %v, want no daemon refusal", err)
	}

}

// Pruning the store and asking a crew member's session to restart pass
// the daemon's own call in-process as "yard": the automatic prune and a
// rotation restart. The same calls from a pane or a terminal as anyone
// else are refused in the operator's words, marked alone — prune in the
// operator's alone, a restart in the operator's or the yardmaster's —
// and actor "yard" from a pane still faces the check, resolved to the
// pane's member.
func TestDaemonOrOperatorPruneAndRestart(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	s := storetest.Open(t, filepath.Join(home, "yard.db"))
	ctx := context.Background()
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"yardmaster", "clerk"} {
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: r, Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
	}

	daemonPID := os.Getpid()
	desc, err := peer.Describe(daemonPID)
	if err != nil {
		t.Skipf("describe the test process as the daemon: %v", err)
	}
	t.Cleanup(storetest.HoldDaemonAs(t, home, daemonPID, desc.Start))

	daemon := store.WithCaller(ctx, daemonPID)
	panePID := os.Getppid()
	paneDesc, err := peer.Describe(panePID)
	if err != nil {
		t.Skipf("describe the parent process as the crew pane: %v", err)
	}
	other := store.WithCaller(ctx, panePID)

	// The daemon leg: the holder's calls as "yard" pass, and the
	// restart is recorded.
	if res, err := s.Prune(daemon, store.Retention{}, "yard"); err != nil || res == nil {
		t.Fatalf("the daemon's prune = %+v, %v", res, err)
	}
	if err := s.RequestRestart(daemon, "yardmaster", "rotation", "", "yard"); err != nil {
		t.Fatalf("the daemon's restart = %v", err)
	}
	if err := s.RequestRestartFor(daemon, "clerk", "", "rotation", "", "yard"); err != nil {
		t.Fatalf("the daemon's restart for a session = %v", err)
	}
	// And in-process, as the dispatcher calls them, with no caller.
	if _, err := s.Prune(ctx, store.Retention{}, "yard"); err != nil {
		t.Fatalf("the in-process prune = %v", err)
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if r.CrewSession.RestartRequested != "rotation" {
		t.Fatalf("yardmaster restart requested = %q", r.CrewSession.RestartRequested)
	}
	evs, err := s.Events(ctx, r.Journal, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Kind != "crew_restart_requested" || evs[len(evs)-1].Actor != "yard" {
		t.Fatalf("journal's last event = %+v, want crew_restart_requested by yard", evs)
	}

	alone := func(err error, what string) {
		t.Helper()
		if ask, ok := store.IsOperatorOnly(err); !ok || ask != store.Alone {
			t.Errorf("%s = %v (mark %v, %v), want an operator-only refusal marked alone", what, err, ask, ok)
		}
	}
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// From a terminal as a non-operator: no pane is on record, so the
	// actor stands as named.
	if _, err := s.Prune(other, store.Retention{}, "clerk"); err == nil ||
		err.Error() != "only the operator (jo) may prune the store; this is clerk" {
		t.Errorf("prune from a terminal as clerk = %v", err)
	} else {
		alone(err, "prune from a terminal as clerk")
	}
	if err := s.RequestRestart(other, "yardmaster", "x", "", "clerk"); err == nil ||
		err.Error() != "only the operator (jo) or the yardmaster (yardmaster) may restart a crew member" {
		t.Errorf("restart from a terminal as clerk = %v", err)
	} else {
		alone(err, "restart from a terminal as clerk")
	}
	if now, _ := s.Head(ctx); now != head {
		t.Errorf("the terminal refusals logged %d events", now-head)
	}
	// The operator from a terminal passes, before any pane is on record.
	if err := s.RequestRestart(other, "clerk", "x", "", "jo"); err != nil {
		t.Errorf("restart as the operator = %v", err)
	}
	if _, err := s.Prune(other, store.Retention{}, "jo"); err != nil {
		t.Errorf("prune as the operator = %v", err)
	}
	// From the crew member's pane naming "yard": the daemon leg fails
	// (not the holder) and the pane resolves to the member.
	handle := map[string]any{"shell_pid": panePID, "shell_start": paneDesc.Start}
	if err := s.CrewStarted(ctx, "clerk", handle); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, panePID)
	head, err = s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prune(pane, store.Retention{}, "yard"); err == nil ||
		err.Error() != "only the operator (jo) may prune the store; this is clerk" {
		t.Errorf("prune from clerk's pane as yard = %v", err)
	} else {
		alone(err, "prune from clerk's pane as yard")
	}
	if err := s.RequestRestartFor(pane, "clerk", "", "x", "", "yard"); err == nil ||
		err.Error() != "only the operator (jo) or the yardmaster (yardmaster) may restart a crew member" {
		t.Errorf("restart from clerk's pane as yard = %v", err)
	} else {
		alone(err, "restart from clerk's pane as yard")
	}
	if now, _ := s.Head(ctx); now != head {
		t.Errorf("the pane refusals logged %d events", now-head)
	}
}

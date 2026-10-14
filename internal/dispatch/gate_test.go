package dispatch_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// Every completed pass stamps its build and the time, through a temporary
// file renamed into place: no temp file is left behind.
func TestPassStampsBuildAndTime(t *testing.T) {
	_, _, d := setup(t)
	d.Home = t.TempDir()
	d.Binary = &version.Binary{Running: "rev-9"}
	now := time.Now().Truncate(time.Second)
	dispatch.SetClock(d, func() time.Time { return now })
	must(t, d.Reconcile(context.Background()))
	s, err := config.ReadPassStamp(d.Home)
	if err != nil || s.Build != "rev-9" || !s.At.Equal(now) {
		t.Fatalf("stamp = %+v, %v; want build rev-9 at %v", s, err, now)
	}
	now = now.Add(30 * time.Second)
	must(t, d.Reconcile(context.Background()))
	if s, err = config.ReadPassStamp(d.Home); err != nil || !s.At.Equal(now) {
		t.Fatalf("stamp after a second pass = %+v, %v; want it at %v", s, err, now)
	}
	names, err := filepath.Glob(filepath.Join(d.Home, ".*.tmp-*"))
	if err != nil || len(names) != 0 {
		t.Fatalf("temp files left in the home: %v, %v", names, err)
	}
}

// liveGate holds d's home paused by a gate naming this test's own process.
func liveGate(t *testing.T, home string) {
	t.Helper()
	var start uint64
	if st, err := peer.Describe(os.Getpid()); err == nil {
		start = st.Start
	}
	must(t, config.WriteUpgradeGate(home, config.UpgradeGate{PID: os.Getpid(), Start: start}))
}

// While the gate names a live process no bead is claimed and no agent is
// started, while passes still stamp; once it is lifted the bead dispatches.
func TestGatePausesClaimsAndStarts(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	d.Home = t.TempDir()
	liveGate(t, d.Home)

	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 0 {
		t.Fatalf("starts while paused = %v, want none", got)
	}
	if sessions, err := s.Sessions(ctx, false); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions while paused = %v, %v; want none claimed", sessions, err)
	}
	if _, err := config.ReadPassStamp(d.Home); err != nil {
		t.Fatalf("no stamp while paused: %v", err)
	}
	must(t, os.Remove(config.At(d.Home).UpgradeGate()))
	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 1 || got[0] != b.ID {
		t.Fatalf("starts once lifted = %v, want %s", got, b.ID)
	}
}

// While the gate names a live process no crew member is started, and the
// held start counts as no failure; once it is lifted the member starts.
func TestGateHoldsCrewStarts(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	d.Home = t.TempDir()
	liveGate(t, d.Home)

	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil || r.Live() || f.n != 0 || r.Failures != 0 || r.RetryAfter != nil {
		t.Fatalf("crew member while paused: %+v starts %d, %v; want no start and no failure", r, f.n, err)
	}
	must(t, os.Remove(config.At(d.Home).UpgradeGate()))
	must(t, d.Reconcile(ctx))
	if r, _ := s.GetCrew(ctx, "yardmaster"); !r.Live() || f.n != 1 {
		t.Fatalf("crew member once lifted: %+v starts %d, want it started", r, f.n)
	}
}

// A live crew member is left running during the upgrade gate, even when it
// is disabled or has a restart pending. The deferred stop runs once the
// gate lifts.
func TestGateDoesNotStopLiveCrew(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(context.Context, store.API) error
	}{
		{
			name: "disabled",
			change: func(ctx context.Context, s store.API) error {
				return s.SetCrewEnabled(ctx, "yardmaster", false, "", "me")
			},
		},
		{
			name: "restart requested",
			change: func(ctx context.Context, s store.API) error {
				return s.RequestRestart(ctx, "yardmaster", "requested by me", "", "me")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, f, d := crewSetup(t, store.CrewMember{})
			ctx := context.Background()
			must(t, d.Reconcile(ctx))
			if err := test.change(ctx, s); err != nil {
				t.Fatal(err)
			}
			d.Home = t.TempDir()
			liveGate(t, d.Home)

			must(t, d.Reconcile(ctx))
			r, err := s.GetCrew(ctx, "yardmaster")
			if err != nil || !r.Live() || f.stops != 0 {
				t.Fatalf("crew member during gate: %+v stops %d, %v; want it still live and not stopped", r, f.stops, err)
			}

			must(t, os.Remove(config.At(d.Home).UpgradeGate()))
			must(t, d.Reconcile(ctx))
			if f.stops != 1 {
				t.Fatalf("stops after gate: %d, want deferred stop", f.stops)
			}
		})
	}
}

// A gate naming a dead process is removed and dispatch resumes.
func TestGateNamingADeadProcessIsRemoved(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	d.Home = t.TempDir()
	// A pid that ran and was reaped: dead whatever the OS reports.
	reaped := exec.Command("true")
	must(t, reaped.Run())
	must(t, config.WriteUpgradeGate(d.Home, config.UpgradeGate{PID: reaped.Process.Pid}))

	must(t, d.Reconcile(ctx))
	if _, err := os.Stat(config.At(d.Home).UpgradeGate()); !os.IsNotExist(err) {
		t.Fatalf("the dead gate is still there: %v", err)
	}
	if got := f.starts(); len(got) != 1 || got[0] != b.ID {
		t.Fatalf("starts = %v, want %s dispatched", got, b.ID)
	}
}

// A gate that is unreadable names no process to wait on: it is removed
// and dispatch resumes.
func TestUnreadableGateIsRemoved(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	d.Home = t.TempDir()
	must(t, os.WriteFile(config.At(d.Home).UpgradeGate(), []byte("not json"), 0o600))

	must(t, d.Reconcile(ctx))
	if _, err := os.Stat(config.At(d.Home).UpgradeGate()); !os.IsNotExist(err) {
		t.Fatalf("the unreadable gate is still there: %v", err)
	}
	if got := f.starts(); len(got) != 1 || got[0] != b.ID {
		t.Fatalf("starts = %v, want %s dispatched", got, b.ID)
	}
}

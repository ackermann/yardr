package dispatch_test

import (
	"context"
	"errors"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// homeSetup is a dispatcher whose herdr runner is the fake, with a herdr
// group builders routed from new.
func homeSetup(t *testing.T) (store.API, *fakeRunner, *dispatch.Dispatcher) {
	t.Helper()
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": f}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	return s, f, d
}

// A pass publishes the herdr sessions it saw and started: the bead's
// session and the crew member's line, each with its handle. Nothing is
// kept before the first pass.
func TestHomeSessionsHoldTheLastPass(t *testing.T) {
	s, _, d := homeSetup(t)
	ctx := context.Background()
	if got := d.HomeSessions(); len(got) != 0 {
		t.Fatalf("HomeSessions before any pass = %+v", got)
	}
	must(t, func() error {
		_, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me")
		return err
	}())
	must(t, s.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster", "tab": "w1:t1"}))
	b, err := s.CreateBead(ctx, store.NewBead{Title: "feature"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	got := d.HomeSessions()
	if len(got) != 2 {
		t.Fatalf("HomeSessions after the pass = %+v, want the bead's and the crew member's", got)
	}
	byKind := map[string]dispatch.HomeSession{}
	for _, h := range got {
		byKind[h.Kind+h.For] = h
	}
	bead, crew := byKind["bead"], byKind["crew"]
	if bead.Name != b.ID || bead.Stage != "new" || bead.Group != "builders" || bead.Session == "" ||
		bead.Runner != "herdr" || len(bead.Handle) == 0 {
		t.Fatalf("the bead's session = %+v", bead)
	}
	if want := "bead " + b.ID + " (stage new, group builders)"; bead.String() != want {
		t.Fatalf("the bead's session says %q, want %q", bead.String(), want)
	}
	if crew.Name != "yardmaster" || crew.Runner != "herdr" || crew.Agent != "yardmaster" ||
		crew.Handle["tab"] != "w1:t1" {
		t.Fatalf("the crew member's session = %+v", crew)
	}
	if want := "crew member yardmaster"; crew.String() != want {
		t.Fatalf("the crew member's session says %q, want %q", crew.String(), want)
	}
}

// Sessions of another runner are none of the snapshot's: the home-gone
// close leaves them as a SIGTERM does.
func TestHomeSessionsSkipOtherRunners(t *testing.T) {
	s, _, d := homeSetup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "scripts", Runner: "exec", Limit: 2, Config: map[string]any{"cmd": "true"}}, "me"))
	b, err := s.CreateBead(ctx, store.NewBead{Title: "scripted"}, "me")
	must(t, err)
	sess, err := s.Claim(ctx, b.ID, "scripts", b.Stage, "yard")
	must(t, err)
	must(t, s.MarkRunning(ctx, sess.ID, map[string]any{"pid": 123}))
	must(t, d.Reconcile(ctx))
	if got := d.HomeSessions(); len(got) != 0 {
		t.Fatalf("HomeSessions with an exec session running = %+v, want none", got)
	}
}

// A session the pass started is in the snapshot even when the pass never
// ends: its store gone halfway through, nothing publishes what the reads
// saw.
func TestHomeSessionsKeepSessionsStartedByAFailedPass(t *testing.T) {
	s, f, _ := homeSetup(t)
	d := &dispatch.Dispatcher{Store: failingMarkRunning{s}, Runners: map[string]dispatch.Runner{"herdr": f}}
	ctx := context.Background()
	b, err := s.CreateBead(ctx, store.NewBead{Title: "feature"}, "me")
	must(t, err)
	if err := d.Reconcile(ctx); err == nil {
		t.Fatal("Reconcile with a failing store succeeded")
	}
	got := d.HomeSessions()
	if len(got) != 1 || got[0].Name != b.ID || len(got[0].Handle) == 0 {
		t.Fatalf("HomeSessions after the failed pass = %+v, want the started session", got)
	}
}

// failingMarkRunning is a store gone after the session started: MarkRunning
// fails, everything else answers.
type failingMarkRunning struct{ store.API }

func (failingMarkRunning) MarkRunning(context.Context, string, map[string]any) error {
	return errors.New("the store is gone")
}

// A session that died is dropped by the next pass: the snapshot is what
// runs, not what ran.
func TestHomeSessionsDropDeadSessions(t *testing.T) {
	s, f, d := homeSetup(t)
	ctx := context.Background()
	_, err := s.CreateBead(ctx, store.NewBead{Title: "feature"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := d.HomeSessions(); len(got) != 1 {
		t.Fatalf("HomeSessions after dispatch = %+v, want the session", got)
	}
	sessions, err := s.Sessions(ctx, false)
	must(t, err)
	f.mu.Lock()
	f.dead[sessions[0].ID] = true
	f.mu.Unlock()
	must(t, d.Reconcile(ctx))
	if got := d.HomeSessions(); len(got) != 0 {
		t.Fatalf("HomeSessions after the session died = %+v, want none", got)
	}
}

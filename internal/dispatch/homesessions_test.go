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
	t.Parallel()
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
	t.Parallel()
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

// A session whose start's MarkRunning fails in the store is still in
// the snapshot: the worker records what it opened before it attempts
// the write, and the pass no longer fails for the write, so the
// snapshot is the record home-loss cleanup closes from.
func TestHomeSessionsKeepSessionsStartedByAFailedPass(t *testing.T) {
	t.Parallel()
	s, f, _ := homeSetup(t)
	d := &dispatch.Dispatcher{Store: failingMarkRunning{s}, Runners: map[string]dispatch.Runner{"herdr": f}}
	ctx := context.Background()
	b, err := s.CreateBead(ctx, store.NewBead{Title: "feature"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	got := d.HomeSessions()
	if len(got) != 1 || got[0].Name != b.ID || len(got[0].Handle) == 0 {
		t.Fatalf("HomeSessions after the failed MarkRunning = %+v, want the started session", got)
	}
}

// A herdr start can open its tab and then fail while recording the handle.
// The session still belongs in the snapshot so home-loss cleanup can close it.
func TestHomeSessionsKeepPartialHerdrStarts(t *testing.T) {
	t.Parallel()
	s, f, d := homeSetup(t)
	d.Runners["herdr"] = partialHerdrStart{fakeRunner: f}
	b, err := s.CreateBead(context.Background(), store.NewBead{Title: "feature"}, "me")
	must(t, err)
	must(t, d.Reconcile(context.Background()))
	got := d.HomeSessions()
	if len(got) != 1 || got[0].Name != b.ID || got[0].Handle["tab"] != "tab-1" {
		t.Fatalf("HomeSessions after a partial herdr start = %+v, want its open tab", got)
	}
}

type partialHerdrStart struct{ *fakeRunner }

func (partialHerdrStart) Start(_ context.Context, j dispatch.Job) (map[string]any, error) {
	return map[string]any{"session": j.Session.ID, "tab": "tab-1"}, errors.New("recording the pane failed")
}

// A failed crew start is kept only if its attempted cleanup also fails.
func TestHomeSessionsKeepUnclosedPartialCrewStarts(t *testing.T) {
	t.Parallel()
	for _, stopErr := range []error{nil, errors.New("herdr is unavailable")} {
		name := "closed"
		if stopErr != nil {
			name = "still open"
		}
		t.Run(name, func(t *testing.T) {
			s, f, d := crewSetup(t, store.CrewMember{})
			ctx := context.Background()
			member, err := s.GetCrew(ctx, "yardmaster")
			must(t, err)
			member.Runner = "herdr"
			_, err = s.PutCrew(ctx, *member, "me")
			must(t, err)
			d.Runners = map[string]dispatch.Runner{"herdr": partialCrewStart{crewRunner: f, stopErr: stopErr}}
			must(t, d.Reconcile(ctx))
			got := d.HomeSessions()
			if stopErr == nil && len(got) != 0 {
				t.Fatalf("HomeSessions after the partial start was closed = %+v, want none", got)
			}
			if stopErr != nil && (len(got) != 1 || got[0].Handle["tab"] != "tab-1") {
				t.Fatalf("HomeSessions after its partial start could not be closed = %+v, want its open tab", got)
			}
		})
	}
}

// A failed ordinary stop leaves the pane open; retain its handle so a home
// loss during this pass can still close it from the snapshot.
func TestHomeSessionsKeepCrewWhenStopFails(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	member, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	member.Runner, member.Enabled = "herdr", false
	_, err = s.PutCrew(ctx, *member, "me")
	must(t, err)
	must(t, s.SetCrewEnabled(ctx, "yardmaster", false, "", "me"))
	must(t, s.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster", "tab": "tab-1"}))
	d.Runners = map[string]dispatch.Runner{"herdr": &failedCrewStop{crewRunner: f}}

	must(t, d.Reconcile(ctx))
	got := d.HomeSessions()
	if len(got) != 1 || got[0].Name != "yardmaster" || got[0].Handle["tab"] != "tab-1" {
		t.Fatalf("HomeSessions after the crew stop failed = %+v, want its open tab", got)
	}
}

type failedCrewStop struct{ *crewRunner }

func (failedCrewStop) Stop(context.Context, map[string]any) error {
	return errors.New("herdr is unavailable")
}

type partialCrewStart struct {
	*crewRunner
	stopErr error
}

func (partialCrewStart) StartCrew(context.Context, store.CrewMember, string) (map[string]any, error) {
	return map[string]any{"agent": "yardmaster-partial", "tab": "tab-1"}, errors.New("recording the pane failed")
}

func (p partialCrewStart) Stop(ctx context.Context, handle map[string]any) error {
	if p.stopErr != nil {
		return p.stopErr
	}
	return p.crewRunner.Stop(ctx, handle)
}

// A start that opens a tab and then has its store writes fail still
// leaves the tab in the snapshot: the worker records it before it
// attempts MarkRunning (on success) or SetHandle (on a partial start),
// and regardless of whether either succeeds, so a home-loss cleanup
// over the snapshot still closes it.
func TestHomeSessionsKeepTabWhenStoreWritesFail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("MarkRunning fails", func(t *testing.T) {
		t.Parallel()
		s, f, _ := homeSetup(t)
		d := &dispatch.Dispatcher{Store: failingMarkRunning{s}, Runners: map[string]dispatch.Runner{"herdr": tabHerdrStart{fakeRunner: f}}}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "feature"}, "me")
		must(t, err)
		must(t, d.Reconcile(ctx))
		got := d.HomeSessions()
		if len(got) != 1 || got[0].Name != b.ID || got[0].Handle["tab"] != "tab-1" {
			t.Fatalf("HomeSessions after the failed MarkRunning = %+v, want its open tab", got)
		}
		// The next pass publishes its own snapshot: the tab is still
		// there, read from the handle the worker kept in the store.
		must(t, d.Reconcile(ctx))
		got = d.HomeSessions()
		if len(got) != 1 || got[0].Handle["tab"] != "tab-1" {
			t.Fatalf("HomeSessions after the next pass = %+v, want its open tab", got)
		}
	})
	t.Run("SetHandle fails on a partial start", func(t *testing.T) {
		t.Parallel()
		s, f, _ := homeSetup(t)
		d := &dispatch.Dispatcher{Store: failingSetHandle{s}, Runners: map[string]dispatch.Runner{"herdr": partialHerdrStart{fakeRunner: f}}}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "feature"}, "me")
		must(t, err)
		must(t, d.Reconcile(ctx))
		got := d.HomeSessions()
		if len(got) != 1 || got[0].Name != b.ID || got[0].Handle["tab"] != "tab-1" {
			t.Fatalf("HomeSessions after the failed SetHandle = %+v, want its open tab", got)
		}
	})
}

type tabHerdrStart struct{ *fakeRunner }

func (tabHerdrStart) Start(_ context.Context, j dispatch.Job) (map[string]any, error) {
	return map[string]any{"session": j.Session.ID, "tab": "tab-1"}, nil
}

// failingSetHandle is a store gone after a partial start: SetHandle
// fails, everything else answers.
type failingSetHandle struct{ store.API }

func (failingSetHandle) SetHandle(context.Context, string, map[string]any) error {
	return errors.New("the store is gone")
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
	t.Parallel()
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

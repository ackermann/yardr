package dispatch_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mckean/yard/internal/dispatch"
	"github.com/mckean/yard/internal/store"
	"github.com/mckean/yard/internal/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

// fakeRunner records starts and lets tests kill sessions.
type fakeRunner struct {
	mu       sync.Mutex
	started  []string // bead ids
	dead     map[string]bool
	stopped  []string
	failNext error
}

func (f *fakeRunner) Start(_ context.Context, j dispatch.Job) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return nil, err
	}
	f.started = append(f.started, j.Bead.ID)
	return map[string]any{"session": j.Session.ID}, nil
}

func (f *fakeRunner) Alive(_ context.Context, h map[string]any) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead[h["session"].(string)], nil
}

func (f *fakeRunner) Stop(_ context.Context, h map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, h["session"].(string))
	return nil
}

func (f *fakeRunner) starts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.started)
}

func setup(t *testing.T) (*store.Store, *fakeRunner, *dispatch.Dispatcher) {
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f},
		Backoff: time.Nanosecond}
	return s, f, d
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPipelineThroughStages(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 2}))
	must(t, s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "fake"}))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"})
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"})
	must(t, err)

	b, _ := s.CreateBead(ctx, store.NewBead{Title: "feature"}, "me")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx)) // idempotent: no second start
	if got := f.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v", got)
	}
	got, _ := s.GetBead(ctx, b.ID)
	if got.Group == nil || *got.Group != "builders" {
		t.Fatalf("not assigned to builders: %+v", got)
	}

	// The builder hands off; the next pass routes to reviewers and tears
	// down the builder session.
	firstSession := *got.Session
	_, err = s.Advance(ctx, b.ID, "review", "builder")
	must(t, err)
	must(t, d.Reconcile(ctx))
	got, _ = s.GetBead(ctx, b.ID)
	if got.Group == nil || *got.Group != "reviewers" {
		t.Fatalf("not routed to reviewers: %+v", got)
	}
	if !slices.Contains(f.stopped, firstSession) {
		t.Fatalf("builder session not stopped: %v", f.stopped)
	}

	// Closing ends it; nothing more is dispatched.
	_, err = s.CloseBead(ctx, b.ID, "merged", "reviewer")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if n := len(f.starts()); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
}

func TestLimitsBlockersAndRouteSpecificity(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "general", Runner: "fake", Limit: 1}))
	must(t, s.PutGroup(ctx, store.Group{Name: "bugs", Runner: "fake", Limit: 5}))
	bug := "bug"
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "general"})
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Type: &bug, Group: "bugs"})

	t1, _ := s.CreateBead(ctx, store.NewBead{Title: "t1"}, "me")
	t2, _ := s.CreateBead(ctx, store.NewBead{Title: "t2"}, "me")
	b1, _ := s.CreateBead(ctx, store.NewBead{Title: "b1", Type: "bug"}, "me")
	b2, _ := s.CreateBead(ctx, store.NewBead{Title: "b2", Type: "bug"}, "me")
	must(t, s.AddDep(ctx, b1.ID, b2.ID, "blocks", "me"))

	must(t, d.Reconcile(ctx))
	// general has limit 1: only t1; bugs: b1 (b2 is blocked by b1).
	if got := f.starts(); !slices.Equal(got, []string{t1.ID, b1.ID}) {
		t.Fatalf("starts = %v", got)
	}
	_, _ = s.CloseBead(ctx, b1.ID, "done", "me")
	_, _ = s.CloseBead(ctx, t1.ID, "done", "me")
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{t1.ID, b1.ID, t2.ID, b2.ID}) {
		t.Fatalf("starts after closes = %v", got)
	}
}

func TestDeadSessionIsRetriedThenHeld(t *testing.T) {
	s, f, d := setup(t)
	d.MaxAttempts = 2
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "flaky"}, "me")

	// Attempt 1 dies.
	must(t, d.Reconcile(ctx))
	got, _ := s.GetBead(ctx, b.ID)
	f.dead[*got.Session] = true
	must(t, d.Reconcile(ctx)) // reaps, then re-dispatches (attempt 2)
	if n := len(f.starts()); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
	// Attempt 2 dies too: with MaxAttempts=2 the bead is held for a human.
	got, _ = s.GetBead(ctx, b.ID)
	f.dead[*got.Session] = true
	must(t, d.Reconcile(ctx))
	got, _ = s.GetBead(ctx, b.ID)
	if !got.Hold || got.Group != nil {
		t.Fatalf("bead not held after %d failures: %+v", d.MaxAttempts, got)
	}
	evs, _ := s.Events(ctx, b.ID, 100)
	if evs[len(evs)-1].Kind != "gave_up" {
		t.Fatalf("last event = %s", evs[len(evs)-1].Kind)
	}

	// A human un-holds: attempts reset, so one failed start is retried.
	time.Sleep(10 * time.Millisecond)
	_, err := s.SetHold(ctx, b.ID, false, "human")
	must(t, err)
	f.failNext = errors.New("boom")
	must(t, d.Reconcile(ctx)) // start fails (attempt 1 since unhold)
	must(t, d.Reconcile(ctx)) // retried
	got, _ = s.GetBead(ctx, b.ID)
	if got.Group == nil || got.Hold {
		t.Fatalf("not re-dispatched after unhold: %+v", got)
	}
	if n := len(f.starts()); n != 3 {
		t.Fatalf("successful starts = %d, want 3", n)
	}
}

func TestUnroutedIsLoggedOnce(t *testing.T) {
	s, _, d := setup(t)
	ctx := context.Background()
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "orphan", Stage: "limbo"}, "me")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	evs, _ := s.Events(ctx, b.ID, 100)
	n := 0
	for _, e := range evs {
		if e.Kind == "unrouted" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("unrouted events = %d", n)
	}
}

func TestRunWakesOnChange(t *testing.T) {
	s, f, d := setup(t)
	d.Interval = time.Hour // only live notifications can trigger the pass
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	done := make(chan error)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)

	b, _ := s.CreateBead(ctx, store.NewBead{Title: "push"}, "me")
	deadline := time.Now().Add(3 * time.Second)
	for len(f.starts()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v; live wake did not dispatch", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMatchOrder(t *testing.T) {
	bug, web := "bug", "web"
	rs := []store.Route{
		{ID: "a", Stage: "new", Group: "any"},
		{ID: "b", Stage: "new", Type: &bug, Group: "bugs"},
		{ID: "c", Stage: "new", Type: &bug, Rig: &web, Group: "webbugs"},
		{ID: "d", Stage: "new", Group: "urgent", Priority: 9},
	}
	dispatch.SortRoutes(rs)
	b := store.Bead{Stage: "new", Type: "bug", Rig: &web}
	if r := dispatch.Match(rs, b); r.Group != "urgent" {
		t.Fatalf("priority should win, got %s", r.Group)
	}
	rs = rs[1:] // drop urgent (sorted first)
	if r := dispatch.Match(rs, b); r.Group != "webbugs" {
		t.Fatalf("most specific should win, got %s", r.Group)
	}
	if r := dispatch.Match(rs, store.Bead{Stage: "new", Type: "task"}); r.Group != "any" {
		t.Fatalf("fallback should match, got %s", r.Group)
	}
	if r := dispatch.Match(rs, store.Bead{Stage: "other"}); r != nil {
		t.Fatalf("wrong stage matched %s", r.Group)
	}
}

// statusRunner is a fakeRunner that also reports agent status.
type statusRunner struct {
	*fakeRunner
	status string
}

func (s *statusRunner) Status(context.Context, map[string]any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, nil
}

func TestBlockedSessionIsReportedOnce(t *testing.T) {
	s := storetest.New(t)
	r := &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")

	must(t, d.Reconcile(ctx)) // start
	r.status = "blocked"
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r.status = "working"
	must(t, d.Reconcile(ctx))
	r.status = "" // agent gone
	must(t, d.Reconcile(ctx))

	evs, _ := s.Events(ctx, b.ID, 100)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"created", "claimed", "started", "session_blocked", "session_unblocked", "session_died"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
}

// finishRunner is a fakeRunner that leaves a "worktree" behind.
type finishRunner struct {
	*fakeRunner
	finished []string
}

func (f *finishRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	h, err := f.fakeRunner.Start(ctx, j)
	if h != nil {
		h["worktree"] = "/wt/" + j.Bead.ID
	}
	return h, err
}

func (f *finishRunner) Finish(_ context.Context, h map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if slices.Contains(f.finished, h["worktree"].(string)) {
		return dispatch.ErrNothingLeft
	}
	f.finished = append(f.finished, h["worktree"].(string))
	return nil
}

func TestWorkspaceCleanedWhenBeadClosesLater(t *testing.T) {
	s := storetest.New(t)
	agents := &finishRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}}
	human := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"agents": agents, "human": human}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "agents"}))
	must(t, s.PutGroup(ctx, store.Group{Name: "me", Runner: "human"}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"})
	_, _ = s.AddRoute(ctx, store.Route{Stage: "fix", Group: "builders"})
	_, _ = s.AddRoute(ctx, store.Route{Stage: "review", Group: "me"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")

	// build -> review -> back to fix -> review again
	must(t, d.Reconcile(ctx))
	for _, stage := range []string{"review", "fix", "review"} {
		_, err := s.Advance(ctx, b.ID, stage, "someone")
		must(t, err)
		must(t, d.Reconcile(ctx))
	}
	if len(agents.finished) != 0 {
		t.Fatalf("cleaned up while the bead is open: %v", agents.finished)
	}
	// The human closes it; the builders' worktree goes, once.
	_, err := s.CloseBead(ctx, b.ID, "done", "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if !slices.Equal(agents.finished, []string{"/wt/" + b.ID}) {
		t.Fatalf("finished = %v", agents.finished)
	}
	evs, _ := s.Events(ctx, b.ID, 100)
	n := 0
	for _, e := range evs {
		if e.Kind == "workspace_removed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("workspace_removed events = %d, want 1", n)
	}
}

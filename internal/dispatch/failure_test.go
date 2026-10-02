package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// downRunner fails every Start as its runtime would when down, until up.
type downRunner struct {
	*fakeRunner
	mu       sync.Mutex
	up       bool
	attempts int
}

func (r *downRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	r.mu.Lock()
	r.attempts++
	up := r.up
	r.mu.Unlock()
	if !up {
		return nil, dispatch.Runtime(errors.New("herdr: executable file not found in $PATH"))
	}
	return r.fakeRunner.Start(ctx, j)
}

func (r *downRunner) tries() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

// groupEvents counts events of kind per group (bead-less events carry the group
// in their data).
func groupEvents(t *testing.T, s store.API, kind string) map[string]int {
	t.Helper()
	evs, err := s.EventsSince(context.Background(), 0, 0)
	must(t, err)
	n := map[string]int{}
	for _, e := range evs {
		if e.Kind == kind {
			n[fmt.Sprint(e.Data["group"])]++
		}
	}
	return n
}

// With the runtime down, no bead counts a failure or gets held, however many
// passes run, and each group reports runtime_down once; once it is back the
// beads start and runtime_up says so.
func TestRuntimeDownHoldsNoBead(t *testing.T) {
	s := storetest.New(t)
	r := &downRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r},
		Backoff: time.Nanosecond, RuntimeBackoff: time.Nanosecond, MaxAttempts: 2}
	ctx := context.Background()
	for _, g := range []string{"builders", "reviewers"} {
		must(t, s.PutGroup(ctx, store.Group{Name: g, Runner: "fake", Limit: 2}))
	}
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"})
	_, _ = s.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"})
	var beads []*store.Bead
	for i, stage := range []string{"new", "new", "new", "review"} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i), Stage: stage}, "me")
		must(t, err)
		beads = append(beads, b)
	}

	for range 6 {
		must(t, d.Reconcile(ctx))
		time.Sleep(time.Millisecond)
	}
	if r.tries() < 6 {
		t.Fatalf("starts tried = %d; the outage was not retried", r.tries())
	}
	for _, b := range beads {
		got, _ := s.GetBead(ctx, b.ID)
		if got.Hold || got.Group != nil {
			t.Fatalf("bead %s touched by the outage: hold=%v group=%v", b.ID, got.Hold, got.Group)
		}
		if n, _ := s.FailureCount(ctx, b.ID, b.Stage); n != 0 {
			t.Fatalf("bead %s has %d failures from the outage", b.ID, n)
		}
	}
	if got := groupEvents(t, s, "runtime_down"); got["builders"] != 1 || got["reviewers"] != 1 || len(got) != 2 {
		t.Fatalf("runtime_down per group = %v, want one each", got)
	}

	r.mu.Lock()
	r.up = true
	r.mu.Unlock()
	time.Sleep(time.Millisecond)
	must(t, d.Reconcile(ctx))
	if n := len(r.starts()); n != 3 { // builders' limit 2, reviewers 1
		t.Fatalf("starts after recovery = %d, want 3", n)
	}
	if got := groupEvents(t, s, "runtime_up"); got["builders"] != 1 || got["reviewers"] != 1 {
		t.Fatalf("runtime_up per group = %v", got)
	}
}

// While a group's runtime is down its beads are not tried again before the
// backoff ends; an error of the bead's own still counts toward the hold.
func TestRuntimeDownBacksOff(t *testing.T) {
	s := storetest.New(t)
	r := &downRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r},
		Backoff: time.Nanosecond, RuntimeBackoff: time.Hour, MaxAttempts: 2}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 3}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	for i := range 3 {
		_, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i)}, "me")
		must(t, err)
	}
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if n := r.tries(); n != 1 {
		t.Fatalf("starts tried = %d, want 1: the pass should stop at the first outage and back off", n)
	}

	// A bead-specific failure is unchanged: it counts, and holds.
	s2, f, d2 := setup(t)
	d2.MaxAttempts = 1
	must(t, s2.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}))
	_, _ = s2.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	b, _ := s2.CreateBead(ctx, store.NewBead{Title: "bad brief"}, "me")
	f.failNext = errors.New("group g prompt: no such file")
	must(t, d2.Reconcile(ctx))
	time.Sleep(time.Millisecond)
	must(t, d2.Reconcile(ctx))
	if got, _ := s2.GetBead(ctx, b.ID); !got.Hold {
		t.Fatalf("bead-specific failure did not hold: %+v", got)
	}
	if got := groupEvents(t, s2, "runtime_down"); len(got) != 0 {
		t.Fatalf("bead failure reported as runtime_down: %v", got)
	}
}

// Holding a bead ends its session and frees the group's slot for the next
// bead; unholding routes it again, with no failure counted.
func TestHoldFreesTheSlot(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 1}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	a, _ := s.CreateBead(ctx, store.NewBead{Title: "a", Priority: 1}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "b", Priority: 2}, "me")

	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{a.ID}) {
		t.Fatalf("starts = %v, want only %s (limit 1)", got, a.ID)
	}
	held, _ := s.GetBead(ctx, a.ID)
	sess := *held.Session

	// The agent holds its own bead, from its own session.
	_, err := s.SetHold(ctx, a.ID, true, store.Expect{Session: sess}, "agent")
	must(t, err)
	held, _ = s.GetBead(ctx, a.ID)
	if held.Group != nil || held.Session != nil {
		t.Fatalf("held bead still owned: %+v", held)
	}
	if got, _ := s.GetSession(ctx, sess); got.State != "held" {
		t.Fatalf("session state = %s, want held", got.State)
	}
	live, _ := s.Sessions(ctx, false)
	if len(live) != 0 {
		t.Fatalf("live sessions after hold = %d, want 0", len(live))
	}

	must(t, d.Reconcile(ctx))
	if !slices.Contains(f.stopped, sess) {
		t.Fatalf("held session not stopped: %v", f.stopped)
	}
	if got := f.starts(); !slices.Equal(got, []string{a.ID, b.ID}) {
		t.Fatalf("starts = %v; the freed slot did not go to %s", got, b.ID)
	}

	_, err = s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me")
	must(t, err)
	_, err = s.SetHold(ctx, a.ID, false, store.Expect{}, "mayor")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{a.ID, b.ID, a.ID}) {
		t.Fatalf("starts = %v; unhold did not route %s again", got, a.ID)
	}
	if n, _ := s.FailureCount(ctx, a.ID, "new"); n != 0 {
		t.Fatalf("hold counted as %d failures", n)
	}
}

func eventKinds(t *testing.T, s store.API, bead, kind string) []store.Event {
	t.Helper()
	evs, err := s.Events(context.Background(), bead, 1000)
	must(t, err)
	var out []store.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// An agent idle past its group's limit raises session_idle once per idle
// spell; with a grace set it is then stopped.
func TestIdleSessionIsReported(t *testing.T) {
	s := storetest.New(t)
	r := &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"idle": "30ms"}}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	set := func(st string) {
		r.mu.Lock()
		r.status = st
		r.mu.Unlock()
	}

	must(t, d.Reconcile(ctx)) // start
	set("idle")
	must(t, d.Reconcile(ctx)) // idle from here
	if n := len(eventKinds(t, s, b.ID, "session_idle")); n != 0 {
		t.Fatalf("session_idle before the limit: %d", n)
	}
	time.Sleep(40 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_idle")
	if len(evs) != 1 {
		t.Fatalf("session_idle events = %d, want 1", len(evs))
	}
	got, _ := s.GetBead(ctx, b.ID)
	if evs[0].Data["session"] != *got.Session || evs[0].Data["group"] != "g" {
		t.Fatalf("session_idle data = %v", evs[0].Data)
	}

	// Working again ends the spell; the next one is reported again.
	set("working")
	must(t, d.Reconcile(ctx))
	set("done")
	must(t, d.Reconcile(ctx))
	time.Sleep(40 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	if n := len(eventKinds(t, s, b.ID, "session_idle")); n != 2 {
		t.Fatalf("session_idle events = %d, want 2", n)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Session == nil {
		t.Fatal("session stopped without a grace configured")
	}

	// With a grace, a session idle past limit and grace is stopped.
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake",
		Config: map[string]any{"idle": "30ms", "grace": "0"}}))
	must(t, d.Reconcile(ctx))
	got, _ = s.GetBead(ctx, b.ID)
	if got.Session != nil {
		t.Fatalf("idle session not stopped after grace: %+v", got)
	}
	if n := len(eventKinds(t, s, b.ID, "session_failed")); n != 1 {
		t.Fatalf("session_failed events = %d", n)
	}
}

// A session running past its group's max_age raises session_aged once, even
// across daemon restarts; a status-less runner gets no limit unless set.
func TestAgedSessionIsReportedOnce(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"max_age": "20ms"}}))
	must(t, s.PutGroup(ctx, store.Group{Name: "plain", Runner: "fake"}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	_, _ = s.AddRoute(ctx, store.Route{Stage: "other", Group: "plain"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "long"}, "me")
	p, _ := s.CreateBead(ctx, store.NewBead{Title: "plain", Stage: "other"}, "me")

	must(t, d.Reconcile(ctx))
	time.Sleep(30 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	// A new daemon (no memory of the first) does not tell again.
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}}
	must(t, d2.Reconcile(ctx))
	if n := len(eventKinds(t, s, b.ID, "session_aged")); n != 1 {
		t.Fatalf("session_aged events = %d, want 1", n)
	}
	if n := len(eventKinds(t, s, p.ID, "session_aged")); n != 0 {
		t.Fatalf("status-less runner got the default age limit")
	}
	got, _ := s.GetBead(ctx, b.ID)
	if got.Session == nil {
		t.Fatal("aged session stopped without a grace")
	}
	sess := *got.Session

	// With a grace it is stopped as failed (and, with no backoff here,
	// routed again in the same pass to a fresh session).
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake",
		Config: map[string]any{"max_age": "20ms", "grace": "1ms"}}))
	must(t, d.Reconcile(ctx))
	if st, _ := s.GetSession(ctx, sess); st.State != "failed" {
		t.Fatalf("aged session state = %s after grace, want failed", st.State)
	}
}

// blockedRunner refuses every Start as the herdr runner does when its
// group's env_file is refused.
type blockedRunner struct{ *fakeRunner }

func (r *blockedRunner) Start(context.Context, dispatch.Job) (map[string]any, error) {
	return nil, dispatch.Blocked(errors.New("group builders: env_file /x/b.env is open to others (mode 0644); chmod 600 it"))
}

// A group blocked by its own config is not the bead's failure: the bead is
// neither counted nor held, and the group reports why once.
func TestBlockedGroupHoldsNoBead(t *testing.T) {
	s := storetest.New(t)
	r := &blockedRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r},
		Backoff: time.Nanosecond, RuntimeBackoff: time.Nanosecond, MaxAttempts: 2}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 1}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"})
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Stage: "new"}, "me")
	must(t, err)
	for range 4 {
		must(t, d.Reconcile(ctx))
		time.Sleep(time.Millisecond)
	}
	got, _ := s.GetBead(ctx, b.ID)
	if n, _ := s.FailureCount(ctx, b.ID, b.Stage); got.Hold || n != 0 {
		t.Fatalf("blocked group failed the bead: hold=%v failures=%d", got.Hold, n)
	}
	if got := groupEvents(t, s, "runtime_down"); got["builders"] != 1 {
		t.Fatalf("runtime_down = %v, want one for builders", got)
	}
}

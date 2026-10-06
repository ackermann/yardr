package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/rigcheck"
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
	_, err = s.SetHold(ctx, a.ID, false, store.Expect{}, "yardmaster")
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
	// The hard cap without a grace is twice max_age (4s here): the three
	// passes below must all run before it, and under -race with the rest
	// of the suite alongside one pass has taken 300ms. The long backoff
	// makes a cap that fires early fail loudly (no session) instead of
	// passing on the fresh session the bead would otherwise restart on.
	d.Backoff = time.Hour
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"max_age": "2s"}}))
	must(t, s.PutGroup(ctx, store.Group{Name: "plain", Runner: "fake"}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	_, _ = s.AddRoute(ctx, store.Route{Stage: "other", Group: "plain"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "long"}, "me")
	p, _ := s.CreateBead(ctx, store.NewBead{Title: "plain", Stage: "other"}, "me")

	must(t, d.Reconcile(ctx))
	time.Sleep(2050 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	// A new daemon (no memory of the first) does not tell again.
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}}
	must(t, d2.Reconcile(ctx))
	aged := eventKinds(t, s, b.ID, "session_aged")
	if len(aged) != 1 {
		t.Fatalf("session_aged events = %d, want 1", len(aged))
	}
	if aged[0].Data["stop_after"] != "2s" {
		t.Fatalf("session_aged without a grace must say when the cap stops it: %v", aged[0].Data)
	}
	if n := len(eventKinds(t, s, p.ID, "session_aged")); n != 0 {
		t.Fatalf("status-less runner got the default age limit")
	}
	got, _ := s.GetBead(ctx, b.ID)
	if got.Session == nil {
		t.Fatal("aged session stopped without a grace, before the cap")
	}
	sess := *got.Session

	// With a grace it is stopped as failed.
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake",
		Config: map[string]any{"max_age": "2s", "grace": "1ms"}}))
	must(t, d.Reconcile(ctx))
	if st, _ := s.GetSession(ctx, sess); st.State != "failed" {
		t.Fatalf("aged session state = %s after grace, want failed", st.State)
	}
}

// With no grace set, a session that has run twice its max_age is stopped
// anyway (failed, so repeats still hold the bead), and the bead is released
// with a note saying why, so the next agent and the yardmaster see it.
func TestAgedSessionIsCappedWithoutGrace(t *testing.T) {
	s, _, d := setup(t)
	d.Backoff = time.Hour // keep the bead released, not restarted, for the checks
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"max_age": "20ms"}}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "long"}, "me")

	must(t, d.Reconcile(ctx))
	got, _ := s.GetBead(ctx, b.ID)
	sess := *got.Session
	time.Sleep(50 * time.Millisecond)
	must(t, d.Reconcile(ctx))

	st, err := s.GetSession(ctx, sess)
	must(t, err)
	if st.State != "failed" || st.Error == nil || !strings.Contains(*st.Error, "hard cap") {
		t.Fatalf("capped session = %s %v, want failed at the hard cap", st.State, st.Error)
	}
	got, _ = s.GetBead(ctx, b.ID)
	if got.Session != nil || got.Group != nil || got.Hold {
		t.Fatalf("bead not released: %+v", got)
	}
	notes, err := s.Notes(ctx, b.ID)
	must(t, err)
	var found bool
	for _, n := range notes {
		if strings.Contains(n.Text, sess) && strings.Contains(n.Text, "max_age") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no note on the bead names the capped session: %+v", notes)
	}
	if n := len(eventKinds(t, s, b.ID, "session_aged")); n != 1 {
		t.Fatalf("session_aged events = %d, want 1", n)
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

// A bead held after repeated failures and then un-held is not held again
// once the log that recorded the unhold is pruned: the attempts since the
// unhold live on the bead, not in the event.
func TestUnheldBeadSurvivesPruningTheUnheldEvent(t *testing.T) {
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f},
		Backoff: time.Nanosecond, MaxAttempts: 2}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "flaky"}, "me")
	for range 2 {
		must(t, d.Reconcile(ctx))
		got, _ := s.GetBead(ctx, b.ID)
		if got.Session == nil {
			t.Fatalf("bead not started: %+v", got)
		}
		f.dead[*got.Session] = true
		time.Sleep(time.Millisecond)
	}
	must(t, d.Reconcile(ctx))
	if got, _ := s.GetBead(ctx, b.ID); !got.Hold {
		t.Fatalf("bead not held after %d deaths: %+v", d.MaxAttempts, got)
	}
	_, err := s.SetHold(ctx, b.ID, false, store.Expect{}, "human")
	must(t, err)
	must(t, s.AddNote(ctx, b.ID, "cause fixed, retrying", "human")) // the newest event stays, so this one does

	// Everything so far ages out at once: the unheld event and the dead
	// sessions (stopped by the passes above) go.
	time.Sleep(time.Millisecond)
	res, err := s.Prune(ctx, store.Retention{Events: time.Nanosecond}, "me")
	must(t, err)
	if res.Sessions != 2 || res.HeldBy != "" {
		t.Fatalf("prune = %+v, want both dead sessions gone", res)
	}
	if evs, _ := s.Events(ctx, b.ID, 0); len(evs) != 1 || evs[0].Kind != "noted" {
		t.Fatalf("bead events after prune = %+v, want only the note", evs)
	}

	must(t, d.Reconcile(ctx))
	got, _ := s.GetBead(ctx, b.ID)
	if got.Hold || got.Session == nil {
		t.Fatalf("un-held bead re-held without a new failure: %+v", got)
	}
	if n := len(f.starts()); n != 3 {
		t.Fatalf("starts = %d, want 3", n)
	}
	if n, _ := s.FailureCount(ctx, b.ID, "new"); n != 0 {
		t.Fatalf("failures after unhold and prune = %d, want 0", n)
	}
}

// An agent that waits for its bead's gate (yardr rig check) is idle to
// herdr and not to the yard: no session_idle while the gate runs, however
// long, and none under a grace either. The idle clock starts again when the
// gate has ended, so the event comes once the limit has passed after that.
func TestSessionWaitingForItsGateIsNotIdle(t *testing.T) {
	s := storetest.New(t)
	r := &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour, Home: t.TempDir()}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"idle": "30ms", "grace": "0"}}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	other, _ := s.CreateBead(ctx, store.NewBead{Title: "another bead's gate is not this one's", Stage: "elsewhere"}, "me")
	idle := func() int { t.Helper(); return len(eventKinds(t, s, b.ID, "session_idle")) }

	must(t, d.Reconcile(ctx)) // start
	// Another bead's gate runs throughout, and excuses nobody else.
	otherGate, err := rigcheck.MarkRunning(d.Home, rigcheck.Gate{Bead: other.ID, Rig: "r", Since: time.Now()})
	must(t, err)
	defer otherGate.End()
	// The agent has committed and started its gate, waiting for a slot at
	// first, and sits idle.
	gate, err := rigcheck.MarkRunning(d.Home, rigcheck.Gate{Bead: b.ID, Rig: "r", Light: true, Waiting: true, Since: time.Now()})
	must(t, err)
	r.mu.Lock()
	r.status = "idle"
	r.mu.Unlock()
	must(t, d.Reconcile(ctx))
	time.Sleep(40 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	gate.SetWaiting(false)
	time.Sleep(40 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	if n := idle(); n != 0 {
		t.Fatalf("session_idle while the gate runs: %d", n)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Session == nil {
		t.Fatal("the session was stopped as idle while its gate ran")
	}

	// The gate ends: the clock starts at the first pass that finds it gone,
	// not when the agent went idle.
	gate.End()
	must(t, d.Reconcile(ctx))
	if n := idle(); n != 0 {
		t.Fatalf("session_idle at once after the gate, before the limit passed again: %d", n)
	}
	time.Sleep(40 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	if n := idle(); n != 1 {
		t.Fatalf("session_idle events after the gate ended and the limit passed = %d, want 1", n)
	}
}

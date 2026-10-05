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
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// residentRunner fakes herdr for residents: sessions with a status, and the
// nudges they received.
type residentRunner struct {
	mu       sync.Mutex
	n        int
	status   map[string]string // agent -> idle|working|"" (gone)
	nudges   []string
	stops    int
	failNext error
	blocked  bool                // next start blocks before the agent is ready (a trust prompt)
	foreign  *dispatch.LiveAgent // an agent yardr did not start that already is the resident
	findErr  error               // the agent list cannot be read
}

func (f *residentRunner) LiveResident(context.Context, store.Resident) (*dispatch.LiveAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.foreign, f.findErr
}

func (f *residentRunner) StartResident(_ context.Context, r store.Resident) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return nil, err
	}
	f.n++
	agent := fmt.Sprintf("%s-%d", r.Name, f.n)
	f.status[agent] = "idle"
	if f.blocked {
		f.blocked = false
		f.status[agent] = "blocked"
		return map[string]any{"agent": agent, dispatch.PendingPrompt: "prime yourself"}, nil
	}
	return map[string]any{"agent": agent}, nil
}
func (f *residentRunner) Start(context.Context, dispatch.Job) (map[string]any, error) {
	return nil, nil
}
func (f *residentRunner) Alive(context.Context, map[string]any) (bool, error) { return true, nil }
func (f *residentRunner) Stop(_ context.Context, h map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	delete(f.status, h["agent"].(string))
	return nil
}
func (f *residentRunner) Status(_ context.Context, h map[string]any) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[h["agent"].(string)], nil
}
func (f *residentRunner) Nudge(_ context.Context, h map[string]any, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nudges = append(f.nudges, h["agent"].(string)+": "+text)
	return nil
}
func (f *residentRunner) set(agent, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[agent] = status
}

func residentSetup(t *testing.T, def store.Resident) (store.API, *residentRunner, *dispatch.Dispatcher) {
	s := storetest.New(t)
	f := &residentRunner{status: map[string]string{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}}
	def.Name, def.Runner = "mayor", "fake"
	if _, err := s.PutResident(context.Background(), def, "me"); err != nil {
		t.Fatal(err)
	}
	return s, f, d
}

func agentOf(t *testing.T, s store.API) string {
	r, err := s.GetResident(context.Background(), "mayor")
	if err != nil || !r.Live() {
		t.Fatalf("resident not running: %+v %v", r, err)
	}
	return r.Handle["agent"].(string)
}

func TestResidentKeptAliveAndInboxNudgedWhenIdle(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{On: []string{"gave_up"}})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if f.n != 1 {
		t.Fatalf("started %d sessions, want 1", f.n)
	}
	agent := agentOf(t, s)

	// Busy: mail waits.
	f.set(agent, "working")
	must(t, s.Mail(ctx, "mayor", "deploy freeze", "chris"))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 0 {
		t.Fatalf("nudged a busy agent: %v", f.nudges)
	}
	// Idle: one nudge, not repeated for the same events.
	f.set(agent, "idle")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 {
		t.Fatalf("nudges = %v", f.nudges)
	}
	// Something new: nudged again.
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, s.LogDispatch(ctx, b.ID, "gave_up", "yard", nil))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 2 {
		t.Fatalf("nudges = %v", f.nudges)
	}

	// The session dies, long enough after its start to be no failed start:
	// yard starts another.
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(2 * store.MinSessionLife) })
	f.set(agent, "")
	must(t, d.Reconcile(ctx))
	if f.n != 2 || agentOf(t, s) == agent {
		t.Fatalf("not restarted after death: starts %d", f.n)
	}
}

func TestHandoffRestartsWhenIdle(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	agent := agentOf(t, s)
	f.set(agent, "working") // still writing its handoff turn
	must(t, s.Journal(ctx, "mayor", store.NoteHandoff, "Next: review webapp-3", agent))
	must(t, d.Reconcile(ctx))
	if f.n != 1 {
		t.Fatal("restarted mid-turn")
	}
	f.set(agent, "idle")
	must(t, d.Reconcile(ctx))
	if f.n != 2 || f.stops != 1 {
		t.Fatalf("starts %d stops %d", f.n, f.stops)
	}
	p, err := s.ResidentPrime(ctx, "mayor")
	if err != nil || p.Handoff == nil || p.Handoff.Text != "Next: review webapp-3" || p.NoHandoff {
		t.Fatalf("prime after handoff: %+v %v", p, err)
	}
}

func TestRotationAsksThenForces(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{MaxAge: "1ms", HandoffGrace: "1ms"})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	time.Sleep(5 * time.Millisecond)
	must(t, d.Reconcile(ctx)) // due: asked to hand off
	if len(f.nudges) != 1 || f.n != 1 {
		t.Fatalf("nudges %v starts %d", f.nudges, f.n)
	}
	time.Sleep(5 * time.Millisecond)
	must(t, d.Reconcile(ctx)) // no handoff within grace: restart requested
	must(t, d.Reconcile(ctx)) // ...and carried out
	if f.n != 2 {
		t.Fatalf("starts %d, want a forced rotation", f.n)
	}
	p, _ := s.ResidentPrime(ctx, "mayor")
	if !p.NoHandoff {
		t.Fatal("the forced rotation left no handoff; the prime must fall back")
	}
}

func TestResidentStartFailureBacksOffAndDisableStops(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	f.failNext = errors.New("herdr down")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx)) // within backoff: no retry
	r, _ := s.GetResident(ctx, "mayor")
	if f.n != 0 || r.Failures != 1 || r.RetryAfter == nil {
		t.Fatalf("starts %d resident %+v", f.n, r)
	}
	must(t, s.SetResidentEnabled(ctx, "mayor", true, "", "me")) // clears the backoff
	must(t, d.Reconcile(ctx))
	agentOf(t, s)
	must(t, s.SetResidentEnabled(ctx, "mayor", false, "", "me"))
	must(t, d.Reconcile(ctx))
	if r, _ = s.GetResident(ctx, "mayor"); r.Live() || f.stops != 1 {
		t.Fatalf("disable did not stop it: %+v stops %d", r, f.stops)
	}
}

// Found live: a resident whose agent stops at Claude's trust prompt was
// treated as a failed start, its tab closed and retried in a loop, so the
// human could never answer. A blocked start is kept and its first prompt
// delivered once the agent is idle.
func TestBlockedStartWaitsForHumanThenPrompts(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	f.blocked = true
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r, _ := s.GetResident(ctx, "mayor")
	if !r.Live() || f.stops != 0 || f.n != 1 || r.Failures != 0 {
		t.Fatalf("blocked start was not kept: %+v stops %d starts %d", r, f.stops, f.n)
	}
	if len(f.nudges) != 0 {
		t.Fatalf("prompted a blocked agent: %v", f.nudges)
	}
	evs, _ := s.Events(ctx, r.Journal, 100)
	blocked := 0
	for _, e := range evs {
		if e.Kind == "resident_blocked" {
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatalf("resident_blocked logged %d times", blocked)
	}
	// The human answers in the pane; the agent becomes idle.
	f.set(agentOf(t, s), "idle")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 || !strings.HasSuffix(f.nudges[0], "prime yourself") {
		t.Fatalf("pending prompt delivered %v", f.nudges)
	}
	if r, _ = s.GetResident(ctx, "mayor"); r.Handle[dispatch.PendingPrompt] != nil {
		t.Fatal("pending prompt not cleared")
	}
}

// nudgeRunner is a bead runner whose agents can be blocked and prompted.
type nudgeRunner struct {
	*fakeRunner
	status string
	nudges []string
}

func (n *nudgeRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	h, err := n.fakeRunner.Start(ctx, j)
	if h != nil {
		h[dispatch.PendingPrompt] = "read your brief"
	}
	return h, err
}
func (n *nudgeRunner) Status(context.Context, map[string]any) (string, error) { return n.status, nil }
func (n *nudgeRunner) Nudge(_ context.Context, _ map[string]any, text string) error {
	n.nudges = append(n.nudges, text)
	return nil
}

func TestBlockedBeadSessionGetsItsPromptWhenIdle(t *testing.T) {
	s := storetest.New(t)
	r := &nudgeRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "blocked"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 0 {
		t.Fatal("prompted while blocked")
	}
	r.status = "idle"
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 1 || r.nudges[0] != "read your brief" {
		t.Fatalf("nudges = %v", r.nudges)
	}
	got, _ := s.GetBead(ctx, b.ID)
	sess, _ := s.GetSession(ctx, *got.Session)
	if sess.Handle[dispatch.PendingPrompt] != nil || sess.State != "running" {
		t.Fatalf("session after prompt: %+v", sess)
	}
}

// A resident whose env_file is refused (missing, open to others) does not
// start, and resident_blocked tells the mayor why, once while the reason
// stays the same.
func TestResidentBlockedByItsConfig(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	refuse := dispatch.Blocked(errors.New("resident mayor: env_file /x/mayor.env is open to others (mode 0644); chmod 600 it"))
	for range 2 {
		f.failNext = refuse
		must(t, s.SetResidentEnabled(ctx, "mayor", true, "", "me")) // clears the backoff
		must(t, d.Reconcile(ctx))
	}
	r, _ := s.GetResident(ctx, "mayor")
	if r.Live() || f.n != 0 {
		t.Fatalf("blocked resident started: %+v", r)
	}
	evs, _ := s.Events(ctx, r.Journal, 100)
	var reasons []string
	for _, e := range evs {
		if e.Kind == "resident_blocked" {
			reasons = append(reasons, fmt.Sprint(e.Data["reason"]))
		}
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "env_file") {
		t.Fatalf("resident_blocked = %q, want one naming the env_file", reasons)
	}
	// An ordinary failure is not a block.
	f.failNext = errors.New("herdr down")
	must(t, s.SetResidentEnabled(ctx, "mayor", true, "", "me"))
	must(t, d.Reconcile(ctx))
	if evs, _ = s.Events(ctx, r.Journal, 100); slices.ContainsFunc(evs[len(evs)-1:], func(e store.Event) bool {
		return e.Kind == "resident_blocked"
	}) {
		t.Fatal("herdr down reported as resident_blocked")
	}
}

// Found live (2026-10-03): a pane started by hand was the yardmaster when
// the resident was defined, and the daemon started a second one beside it.
// A resident whose runner finds a live agent that already is it is not
// started: resident_blocked names the agent and its pane, once while it is
// the same agent, nothing counts as a failed start, and the resident starts
// on the pass after the agent is gone.
func TestResidentNotStartedBesideALiveAgent(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	blocked := func() (reasons []string) {
		evs, _ := s.Events(ctx, "mayor-journal", 100)
		for _, e := range evs {
			if e.Kind == "resident_blocked" {
				reasons = append(reasons, fmt.Sprint(e.Data["reason"]))
			}
		}
		return reasons
	}
	f.foreign = &dispatch.LiveAgent{Name: "mayor", Pane: "w1:p1"}
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	r, _ := s.GetResident(ctx, "mayor")
	if r.Live() || f.n != 0 || r.Failures != 0 || r.RetryAfter != nil {
		t.Fatalf("started, or counted as a failure: %+v starts %d", r, f.n)
	}
	if got := blocked(); len(got) != 1 || !strings.Contains(got[0], `"mayor"`) || !strings.Contains(got[0], "w1:p1") {
		t.Fatalf("resident_blocked = %q, want one naming agent and pane", got)
	}
	// Another agent in its place is news.
	f.foreign = &dispatch.LiveAgent{Name: "yardr-mayor", Pane: "w2:p1"}
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if got := blocked(); len(got) != 2 || !strings.Contains(got[1], "yardr-mayor") || !strings.Contains(got[1], "w2:p1") {
		t.Fatalf("resident_blocked = %q, want a second for the other agent", got)
	}
	// The human ends it: the next pass starts the resident.
	f.foreign = nil
	must(t, d.Reconcile(ctx))
	if r, _ = s.GetResident(ctx, "mayor"); !r.Live() || f.n != 1 {
		t.Fatalf("not started once the agent was gone: %+v starts %d", r, f.n)
	}
	// It dies, long enough after its start to be no failed start, while
	// another agent is it again: said again, not restarted.
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(2 * store.MinSessionLife) })
	f.set(agentOf(t, s), "")
	f.foreign = &dispatch.LiveAgent{Name: "mayor", Pane: "w1:p1"}
	must(t, d.Reconcile(ctx))
	if r, _ = s.GetResident(ctx, "mayor"); r.Live() || f.n != 1 || len(blocked()) != 3 {
		t.Fatalf("restarted beside a live agent: %+v starts %d blocked %q", r, f.n, blocked())
	}
}

// An agent list that cannot be read is a failed start like any other (herdr
// down): counted and backed off, not reported as blocked, and nothing is
// started blind.
func TestResidentAgentListUnreadableIsAFailedStart(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	f.findErr = errors.New("herdr: connection refused")
	must(t, d.Reconcile(ctx))
	r, _ := s.GetResident(ctx, "mayor")
	if r.Live() || f.n != 0 || r.Failures != 1 || r.RetryAfter == nil {
		t.Fatalf("resident after an unreadable agent list: %+v starts %d", r, f.n)
	}
	evs, _ := s.Events(ctx, r.Journal, 100)
	if slices.ContainsFunc(evs, func(e store.Event) bool { return e.Kind == "resident_blocked" }) {
		t.Fatal("herdr down reported as resident_blocked")
	}
}

// shortLived is residentSetup with a clock the test moves: at(by) puts the
// dispatcher by after the real time, which is what the store stamps a start
// with, so at(time.Second) right after a start makes that session a second
// old.
func shortLived(t *testing.T) (s store.API, f *residentRunner, d *dispatch.Dispatcher, at func(time.Duration)) {
	s, f, d = residentSetup(t, store.Resident{On: []string{"resident_gave_up"}})
	var skew time.Duration
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(skew) })
	return s, f, d, func(by time.Duration) { skew = by }
}

// Found live (yardr-05dd): a harness that exits right after its start was
// started again on every pass, a new workspace every 5 seconds, with nothing
// counted and nobody told. A session gone within store.MinSessionLife is a
// failed start: the waits grow and after store.MaxStartFails the resident is
// given up.
func TestResidentSessionGoneAtOnceIsAFailedStart(t *testing.T) {
	s, f, d, at := shortLived(t)
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	var waits []time.Duration
	for n := 1; n <= store.MaxStartFails; n++ {
		if f.n != n {
			t.Fatalf("round %d: %d sessions started", n, f.n)
		}
		f.set(agentOf(t, s), "") // the harness exits
		at(time.Second)
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx)) // within the wait: no start
		r, _ := s.GetResident(ctx, "mayor")
		if r.Live() || f.n != n || r.Failures != n || r.RetryAfter == nil || r.Handle != nil {
			t.Fatalf("round %d: starts %d resident %+v", n, f.n, r)
		}
		wait := time.Until(*r.RetryAfter)
		waits = append(waits, wait)
		if n > 1 && wait < waits[n-2]+waits[n-2]/2 {
			t.Fatalf("waits do not grow: %v", waits)
		}
		if n == 3 && (r.State != "stopped" || wait < 100*time.Second) {
			t.Fatalf("after three sessions: state %s, waits %v, want stopped and about 30s, 1m, 2m", r.State, waits)
		}
		if n == store.MaxStartFails {
			if r.State != "failed" {
				t.Fatalf("state %s after %d sessions gone at once, want failed", r.State, n)
			}
			break
		}
		at(wait + time.Second)
		must(t, d.Reconcile(ctx))
	}
	// Given up: never started again, however long ago.
	at(24 * time.Hour)
	must(t, d.Reconcile(ctx))
	if f.n != store.MaxStartFails {
		t.Fatalf("started again after giving up: %d sessions", f.n)
	}
	evs, _ := s.Events(ctx, "mayor-journal", 100)
	kinds := map[string]int{}
	reason := ""
	for _, e := range evs {
		kinds[e.Kind]++
		if e.Kind == "resident_gave_up" {
			reason = fmt.Sprint(e.Data["reason"])
		}
	}
	if kinds["resident_start_failed"] != store.MaxStartFails-1 || kinds["resident_gave_up"] != 1 ||
		kinds["resident_stopped"] != store.MaxStartFails {
		t.Fatalf("events %v", kinds)
	}
	if !strings.Contains(reason, "the session ended 1s after its start") {
		t.Fatalf("resident_gave_up reason %q does not say how long the session lived", reason)
	}
}

// A session that lives past the limit ends the streak, still running or gone
// since, so an early exit long ago does not add up with one today; the start
// alone does not, or two early exits would never be two in a row.
func TestResidentSessionThatLivesResetsFailedStarts(t *testing.T) {
	s, f, d, at := shortLived(t)
	ctx := context.Background()
	failures := func() int {
		r, _ := s.GetResident(ctx, "mayor")
		return r.Failures
	}
	early := func() { // the running session exits a second after its start, and the next is started
		t.Helper()
		f.set(agentOf(t, s), "")
		at(time.Second)
		must(t, d.Reconcile(ctx))
		at(time.Hour)
		must(t, d.Reconcile(ctx))
		agentOf(t, s)
	}
	must(t, d.Reconcile(ctx))
	early()
	if failures() != 1 {
		t.Fatalf("failures %d right after the next start, want 1: a start alone does not reset", failures())
	}
	at(store.MinSessionLife - time.Second)
	must(t, d.Reconcile(ctx))
	if failures() != 1 {
		t.Fatalf("failures %d before the limit, want 1", failures())
	}
	// Still running past the limit.
	at(store.MinSessionLife + time.Second)
	must(t, d.Reconcile(ctx))
	if failures() != 0 {
		t.Fatalf("failures %d with a session past the limit, want 0", failures())
	}
	// Gone past the limit, between two passes: reset, and started at once.
	early()
	n := f.n
	f.set(agentOf(t, s), "")
	at(store.MinSessionLife + time.Second)
	must(t, d.Reconcile(ctx))
	if r, _ := s.GetResident(ctx, "mayor"); !r.Live() || r.Failures != 0 || r.RetryAfter != nil || f.n != n+1 {
		t.Fatalf("a session gone after the limit: starts %d (was %d) resident %+v", f.n, n, r)
	}
}

// Endings that were asked for are no failed starts, however soon after the
// start: a handoff, a requested restart (a rotation is one), a stop; nor is
// a session that ends itself once its restart was asked for.
func TestResidentWantedEndingsAreNoFailedStarts(t *testing.T) {
	s, f, d, at := shortLived(t)
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	at(time.Second)
	restarted := func(what string, n int) {
		t.Helper()
		must(t, d.Reconcile(ctx))
		r, _ := s.GetResident(ctx, "mayor")
		if !r.Live() || r.Failures != 0 || r.RetryAfter != nil || f.n != n {
			t.Fatalf("%s: starts %d, want %d; resident %+v", what, f.n, n, r)
		}
	}
	must(t, s.Journal(ctx, "mayor", store.NoteHandoff, "all quiet", "mayor"))
	restarted("handoff", 2)
	must(t, s.RequestRestart(ctx, "mayor", "requested by me", "", "me"))
	restarted("restart", 3)
	must(t, s.RequestRestart(ctx, "mayor", "requested by me", "", "me"))
	f.set(agentOf(t, s), "") // it exits by itself before the daemon stops it
	restarted("restart of a session that left", 4)
	must(t, s.SetResidentEnabled(ctx, "mayor", false, "", "me"))
	must(t, d.Reconcile(ctx))
	if r, _ := s.GetResident(ctx, "mayor"); r.Live() || r.Failures != 0 || r.RetryAfter != nil {
		t.Fatalf("stop counted as a failed start: %+v", r)
	}
	evs, _ := s.Events(ctx, "mayor-journal", 100)
	if slices.ContainsFunc(evs, func(e store.Event) bool { return e.Kind == "resident_start_failed" }) {
		t.Fatal("a wanted ending logged resident_start_failed")
	}
}

// What the runtime says a resident's session is doing is recorded on each
// pass, as the store's words for it, so a view reads it from the store and
// its events and asks no runtime.
func TestResidentStatusIsRecorded(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	agent := agentOf(t, s)
	for _, c := range []struct{ runtime, want string }{
		{"idle", "idle"}, {"working", "working"}, {"blocked", "blocked"}, {"done", "idle"}, {"starting", "unknown"},
	} {
		f.set(agent, c.runtime)
		must(t, d.Reconcile(ctx))
		r, err := s.GetResident(ctx, "mayor")
		if err != nil || r.Status != c.want {
			t.Fatalf("runtime says %s: status %q, want %q (%v)", c.runtime, r.Status, c.want, err)
		}
	}
	// A pass that sees the same writes nothing.
	head, _ := s.Head(ctx)
	must(t, d.Reconcile(ctx))
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("an unchanged status was logged: head %d, was %d", now, head)
	}
}

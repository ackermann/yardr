package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mckean/yard/internal/dispatch"
	"github.com/mckean/yard/internal/store"
	"github.com/mckean/yard/internal/storetest"
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
	blocked  bool // next start blocks before the agent is ready (a trust prompt)
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
	must(t, s.LogEvent(ctx, b.ID, "gave_up", "yard", nil))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 2 {
		t.Fatalf("nudges = %v", f.nudges)
	}

	// The session dies: yard starts another.
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
	must(t, s.SetResidentEnabled(ctx, "mayor", true, "me")) // clears the backoff
	must(t, d.Reconcile(ctx))
	agentOf(t, s)
	must(t, s.SetResidentEnabled(ctx, "mayor", false, "me"))
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

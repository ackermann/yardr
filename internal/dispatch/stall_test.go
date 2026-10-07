package dispatch_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// agentRunner is a statusRunner whose agents have a pane to read and a
// worktree that may have changed, as herdr's do.
type agentRunner struct {
	*statusRunner
	tail   string
	worked bool
	look   string // what Start's look at the first prompt saw, for the handle
}

func (a *agentRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	h, err := a.statusRunner.Start(ctx, j)
	if h != nil && a.look != "" {
		h[dispatch.PromptLook] = a.look
	}
	return h, err
}

func (a *agentRunner) Tail(context.Context, map[string]any, int) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tail, nil
}

func (a *agentRunner) Worked(context.Context, map[string]any) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.worked, nil
}

const modelError = "There's an issue with the selected model (claude-nope-9). It may not exist or you may not have access to it."

// stallSetup is a yard with one group of agents (limit 2, stall 1m) and a
// runner whose agents start in status.
func stallSetup(t *testing.T, status string, cfg map[string]any) (store.API, *agentRunner, *dispatch.Dispatcher) {
	t.Helper()
	s := storetest.New(t)
	r := &agentRunner{statusRunner: &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: status},
		tail: "❯ Read brief.md and follow it.\n⏺ " + modelError}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Nanosecond}
	conf := map[string]any{"stall": "1m", "model": "claude-nope-9"}
	for k, v := range cfg {
		conf[k] = v
	}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 2, Config: conf}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	must(t, err)
	return s, r, d
}

// An agent whose first request fails prints the error and sits idle. It is
// reported as session_stalled on the first pass after the group's stall
// limit, once, with the pane's last lines, and the group starts no further
// session with that model until the stalled one is released or the model
// changes. The event says what the runner saw of the first prompt when it
// sent it (yardr-myyv: two stalled on a prompt that was never submitted,
// and nothing said why it had counted as arrived).
func TestStalledSessionIsReportedOnceWithItsPane(t *testing.T) {
	s, r, d := stallSetup(t, "idle", nil)
	r.look = "working, look 1"
	ctx := context.Background()
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")

	must(t, d.Reconcile(ctx)) // start and prompt
	must(t, d.Reconcile(ctx)) // idle, but not for long
	if n := len(eventKinds(t, s, b.ID, "session_stalled")); n != 0 {
		t.Fatalf("session_stalled before the limit: %d", n)
	}
	later(d)
	must(t, d.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_stalled")
	if len(evs) != 1 {
		t.Fatalf("session_stalled events = %d, want 1 on the first pass after the limit", len(evs))
	}
	got, _ := s.GetBead(ctx, b.ID)
	data := evs[0].Data
	if data["session"] != *got.Session || data["group"] != "g" || data["model"] != "claude-nope-9" ||
		!strings.Contains(data["tail"].(string), modelError) || data[dispatch.PromptLook] != "working, look 1" {
		t.Fatalf("session_stalled data = %v", data)
	}

	// The next bead of the group waits: its session would stall too.
	b2, _ := s.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
	later(d)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v; the group started a session with the model that stalled", got)
	}
	if n := len(eventKinds(t, s, b.ID, "session_stalled")); n != 1 {
		t.Fatalf("session_stalled events = %d, want 1 per session", n)
	}

	// A daemon that replaced itself tells nobody again and still holds the
	// group back: the mark is on the session's handle.
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Nanosecond}
	must(t, d2.Reconcile(ctx))
	later(d2)
	must(t, d2.Reconcile(ctx))
	if n := len(eventKinds(t, s, b.ID, "session_stalled")); n != 1 {
		t.Fatalf("session_stalled events after a restart = %d, want 1", n)
	}
	if got := r.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts after a restart = %v", got)
	}

	// A bead that brings its own model is not held back by the group's.
	b3, _ := s.CreateBead(ctx, store.NewBead{Title: "z", Metadata: map[string]any{"model": "opus"}}, "me")
	must(t, d2.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b3.ID}) {
		t.Fatalf("starts = %v; want the bead with its own model started, the other still waiting", got)
	}

	// Releasing the stalled bead says "try again": the group's own model
	// starts sessions again.
	_, err := s.Release(ctx, b.ID, "model fixed", store.Expect{}, "me")
	must(t, err)
	must(t, d2.Reconcile(ctx))
	if got := r.starts(); len(got) != 3 || (got[2] != b.ID && got[2] != b2.ID) {
		t.Fatalf("starts = %v; nothing started with the group's model after the release", got)
	}
}

// Changing the group's model lets it start sessions again while the stalled
// one still sits there.
func TestChangedModelStartsAgain(t *testing.T) {
	s, r, d := stallSetup(t, "idle", map[string]any{})
	ctx := context.Background()
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, d.Reconcile(ctx))
	later(d)
	must(t, d.Reconcile(ctx))
	if n := len(eventKinds(t, s, b.ID, "session_stalled")); n != 1 {
		t.Fatalf("session_stalled events = %d, want 1", n)
	}
	b2, _ := s.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
	must(t, d.Reconcile(ctx))
	if got := r.starts(); len(got) != 1 {
		t.Fatalf("starts = %v", got)
	}
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 2,
		Config: map[string]any{"stall": "1m", "model": "opus"}}, "me"))
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b2.ID}) {
		t.Fatalf("starts = %v; the changed model did not start the waiting bead", got)
	}
}

// An agent that works is not stalled, however it idles later: one seen
// working since its prompt, one that left a note, one that changed its
// worktree, and one whose group turned the check off.
func TestWorkingSessionIsNotStalled(t *testing.T) {
	for name, tc := range map[string]struct {
		status string
		cfg    map[string]any
		during func(t *testing.T, s store.API, r *agentRunner, bead string)
	}{
		"seen working between idle spells": {status: "working"},
		"left a note": {status: "idle", during: func(t *testing.T, s store.API, _ *agentRunner, bead string) {
			must(t, s.AddNote(context.Background(), bead, "reading the code", "builder"))
		}},
		"changed its worktree": {status: "idle", during: func(_ *testing.T, _ store.API, r *agentRunner, _ string) {
			r.mu.Lock()
			r.worked = true
			r.mu.Unlock()
		}},
		"stall off": {status: "idle", cfg: map[string]any{"stall": "off"}},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, d := stallSetup(t, tc.status, tc.cfg)
			ctx := context.Background()
			b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
			must(t, d.Reconcile(ctx)) // start and prompt
			must(t, d.Reconcile(ctx)) // sees its status
			if tc.during != nil {
				tc.during(t, s, r, b.ID)
			}
			// Idle from here, past the limit: between tool calls, or done.
			r.mu.Lock()
			r.status = "idle"
			r.mu.Unlock()
			must(t, d.Reconcile(ctx))
			later(d)
			must(t, d.Reconcile(ctx))
			must(t, d.Reconcile(ctx))
			if n := len(eventKinds(t, s, b.ID, "session_stalled")); n != 0 {
				t.Fatalf("session_stalled events = %d, want 0", n)
			}
			// And the group goes on starting sessions.
			b2, _ := s.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
			must(t, d.Reconcile(ctx))
			if got := r.starts(); !slices.Equal(got, []string{b.ID, b2.ID}) {
				t.Fatalf("starts = %v", got)
			}
		})
	}
}

// A prompt held back until a human answered the agent's startup question is
// watched from its delivery, not from the session's start.
func TestStallCountsFromThePendingPromptsDelivery(t *testing.T) {
	s, r, d := stallSetup(t, "blocked", nil)
	n := &pendingRunner{agentRunner: r}
	d.Runners["fake"] = n
	d.PromptSettle = -1 // delivered on the first pass that finds the agent idle
	ctx := context.Background()
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, d.Reconcile(ctx)) // starts with the prompt pending
	later(d)
	must(t, d.Reconcile(ctx)) // blocked on the trust question: not stalled
	if c := len(eventKinds(t, s, b.ID, "session_stalled")); c != 0 {
		t.Fatalf("session_stalled while the prompt was pending: %d", c)
	}
	r.mu.Lock()
	r.status = "idle"
	r.mu.Unlock()
	must(t, d.Reconcile(ctx)) // delivers the prompt
	if c := len(eventKinds(t, s, b.ID, "session_stalled")); c != 0 {
		t.Fatalf("session_stalled at the prompt's delivery: %d", c)
	}
	later(d)
	must(t, d.Reconcile(ctx))
	if c := len(eventKinds(t, s, b.ID, "session_stalled")); c != 1 {
		t.Fatalf("session_stalled events = %d, want 1 after the delivered prompt went nowhere", c)
	}
}

// pendingRunner starts every session with its prompt pending and takes the
// nudge that delivers it.
type pendingRunner struct{ *agentRunner }

func (n *pendingRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	h, err := n.agentRunner.Start(ctx, j)
	if err == nil {
		h[dispatch.PendingPrompt] = "Read the brief."
	}
	return h, err
}

func (n *pendingRunner) Nudge(context.Context, map[string]any, string) error { return nil }

// later moves d's clock two minutes on, past the tests' stall limit.
func later(d *dispatch.Dispatcher) {
	skew[d] += 2 * time.Minute
	by := skew[d]
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(by) })
}

// skew is how far later has moved each dispatcher's clock.
var skew = map[*dispatch.Dispatcher]time.Duration{}

// permissionQuestion is the end of a Claude Code pane that waits on a
// permission prompt.
const permissionQuestion = "⏺ Bash(git push origin yard/x)\n" +
	"╭──────────────────────────────────────────╮\n" +
	"│ Bash command                             │\n" +
	"│   git push origin yard/x                 │\n" +
	"│ Do you want to proceed?                  │\n" +
	"│ ❯ 1. Yes                                 │\n" +
	"│   2. No, and tell Claude what to do      │\n" +
	"╰──────────────────────────────────────────╯"

// An agent that waits on a permission prompt is reported with the end of
// its pane, so the question is in the event and nobody reads the pane to
// learn what to answer (yardr-cdmr: the yardmaster ran `herdr agent read`
// for each session_blocked, 7 times in a day).
func TestBlockedSessionCarriesItsPane(t *testing.T) {
	s, r, d := stallSetup(t, "working", nil)
	ctx := context.Background()
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx)) // start

	r.mu.Lock()
	r.status, r.tail = "blocked", permissionQuestion
	r.mu.Unlock()
	must(t, d.Reconcile(ctx))

	evs, err := s.Events(ctx, b.ID, 100)
	must(t, err)
	var blocked []store.Event
	for _, e := range evs {
		if e.Kind == "session_blocked" {
			blocked = append(blocked, e)
		}
	}
	if len(blocked) != 1 {
		t.Fatalf("session_blocked events = %d, want 1", len(blocked))
	}
	tail, _ := blocked[0].Data["tail"].(string)
	if tail != permissionQuestion || !strings.Contains(tail, "Do you want to proceed?") {
		t.Errorf("tail = %q, want the pane with its question", tail)
	}
	if blocked[0].Data["handle"] == nil || blocked[0].Data["session"] == nil {
		t.Errorf("data = %v, want the session and its handle as before", blocked[0].Data)
	}
}

// A runner with no pane to read reports a blocked session without one.
func TestBlockedSessionWithoutAPane(t *testing.T) {
	s := storetest.New(t)
	r := &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	must(t, err)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	r.mu.Lock()
	r.status = "blocked"
	r.mu.Unlock()
	must(t, d.Reconcile(ctx))

	evs, err := s.Events(ctx, b.ID, 100)
	must(t, err)
	for _, e := range evs {
		if e.Kind == "session_blocked" {
			if _, has := e.Data["tail"]; has {
				t.Errorf("data = %v, want no tail from a runner without a pane", e.Data)
			}
			return
		}
	}
	t.Fatal("no session_blocked event")
}

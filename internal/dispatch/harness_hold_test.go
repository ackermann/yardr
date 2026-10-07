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

// paneRunner is a retryRunner whose agents can each have a status and a pane
// of their own, by session: one stopped on its usage limit beside others
// that work. A session nothing was shown for has the runner's.
type paneRunner struct {
	*retryRunner
	panes map[string][2]string // session -> status, tail
}

func (p *paneRunner) Status(ctx context.Context, h map[string]any) (string, error) {
	p.mu.Lock()
	own, ok := p.panes[h["session"].(string)]
	p.mu.Unlock()
	if ok {
		return own[0], nil
	}
	return p.retryRunner.Status(ctx, h)
}

func (p *paneRunner) Tail(ctx context.Context, h map[string]any, n int) (string, error) {
	p.mu.Lock()
	own, ok := p.panes[h["session"].(string)]
	p.mu.Unlock()
	if ok {
		return own[1], nil
	}
	return p.retryRunner.Tail(ctx, h, n)
}

// showBead gives the agent of b's session a status and a pane of its own.
func (p *paneRunner) showBead(t *testing.T, s store.API, b store.Bead, status, tail string) {
	t.Helper()
	got, err := s.GetBead(context.Background(), b.ID)
	must(t, err)
	if got.Session == nil {
		t.Fatalf("bead %s has no session", b.ID)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.panes[*got.Session] = [2]string{status, tail}
}

// holdYard is a yard whose group g runs up to three kimi sessions with the
// model k3, one of them started for the bead returned and seen working,
// with a stand-in provider of limits and a clock the test moves.
func holdYard(t *testing.T) (s store.API, r *paneRunner, d *dispatch.Dispatcher, b store.Bead, p *standIn, now *time.Time) {
	t.Helper()
	r = &paneRunner{retryRunner: &retryRunner{agentRunner: &agentRunner{statusRunner: &statusRunner{
		fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}, worked: true}}, panes: map[string][2]string{}}
	s = storetest.New(t)
	d = &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	at := noon()
	now = &at
	dispatch.SetClock(d, func() time.Time { return *now })
	p = newStandIn(t, s)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 3,
		Config: map[string]any{"kind": "kimi", "stall": "off", "model": "k3"}}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	must(t, err)
	bead, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx)) // start
	must(t, d.Reconcile(ctx)) // seen working
	return s, r, d, *bead, p, now
}

// One stopped session per group and model, not three (yardr-vpzj): while a
// session waits for the window of its usage limit to reset, the next bead of
// its group and model waits too, since its session would stop on the same
// limit, and a bead of the group with another model starts at once. The
// event says so, with the model. The passes of the wait ask the provider
// nothing, in this daemon or its next. At the wake the agent is prompted
// and the bead that waited starts.
func TestGroupStartsNoFurtherSessionWithAModelThatWaitsForItsReset(t *testing.T) {
	s, r, d, b, p, now := holdYard(t)
	ctx := context.Background()
	resets := now.Add(42 * time.Minute)
	p.say(kimiUsed(resets))
	r.showBead(t, s, b, "idle", kimiLimit)
	must(t, d.Reconcile(ctx))

	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 {
		t.Fatalf("session_harness_error events = %d, want 1 at the stop", len(evs))
	}
	wakes := resets.Add(time.Minute)
	hint, _ := evs[0].Data["hint"].(string)
	if evs[0].Data["model"] != "k3" || evs[0].Data["wakes_at"] != wakes.UTC().Format(time.RFC3339) ||
		!strings.Contains(hint, "prompts the agent when the window is open; the group starts no further session with this model until then; ") {
		t.Fatalf("data = %v, want the model and the hint saying the group starts no further session with it", evs[0].Data)
	}
	until, window, model, ok := dispatch.ResetWait(handleOfBead(t, s, b))
	if !ok || !until.Equal(wakes) || window != "5h" || model != "k3" {
		t.Fatalf("the handle's wait = %v %q %q %v, want until %v for the 5h window with k3", until, window, model, ok, wakes)
	}

	// The second bead of the group and model waits; one with another model
	// starts at once.
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
	must(t, err)
	b3, err := s.CreateBead(ctx, store.NewBead{Title: "z", Metadata: map[string]any{"model": "k2"}}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b3.ID}) {
		t.Fatalf("starts = %v, want the bead of another model started and the one of the waiting model not", got)
	}
	for _, by := range []time.Duration{time.Minute, 20 * time.Minute, 42 * time.Minute} {
		*now = noon().Add(by)
		must(t, d.Reconcile(ctx))
	}
	// A daemon that replaced itself holds the group back as well: the wait
	// is on the session's handle.
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	dispatch.SetClock(d2, func() time.Time { return *now })
	must(t, d2.Reconcile(ctx))
	must(t, d2.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b3.ID}) {
		t.Fatalf("starts in the wait = %v, want none for the waiting model", got)
	}
	if asks := p.asks(); len(asks) != 1 {
		t.Fatalf("the provider was run %d times, want the one ask of the stop: holding a group back asks nothing", len(asks))
	}

	// The wake: the provider is asked the once the wake asks, the agent is
	// prompted, and the bead that waited starts.
	p.say(kimiOpen)
	*now = wakes.Add(time.Second)
	must(t, d2.Reconcile(ctx))
	if len(r.nudges) != 1 || r.nudges[0] != resetLine {
		t.Fatalf("typed %q at the wake, want the reset line", r.nudges)
	}
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b3.ID, b2.ID}) {
		t.Fatalf("starts after the wake = %v, want the bead that waited started", got)
	}
	if _, _, _, waits := dispatch.ResetWait(handleOfBead(t, s, b)); waits {
		t.Fatalf("the wait is still on the handle after the prompt: %v", handleOfBead(t, s, b))
	}
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run %d times, want 2: at the stop and at the wake", len(asks))
	}
}

// An agent that someone got going before the reset no longer waits, so its
// group starts sessions with its model again.
func TestAgentGoingAgainBeforeTheResetLiftsTheHold(t *testing.T) {
	s, r, d, b, p, now := holdYard(t)
	ctx := context.Background()
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	r.showBead(t, s, b, "idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v, want the second bead waiting", got)
	}
	r.showBead(t, s, b, "working", " ● Going on")
	*now = now.Add(time.Minute)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b2.ID}) {
		t.Fatalf("starts = %v, want the second bead started once the first works again", got)
	}
	if asks := p.asks(); len(asks) != 1 {
		t.Fatalf("the provider was run %d times, want the one ask of the stop", len(asks))
	}
}

// A session already waiting at upgrade time has no harness_reset_model key.
// Its bead and group still say which model its wait must hold back.
func TestResetWaitFromBeforeUpgradeHoldsTheGroupModel(t *testing.T) {
	s, r, d, b, p, now := holdYard(t)
	ctx := context.Background()
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	r.showBead(t, s, b, "idle", kimiLimit)
	must(t, d.Reconcile(ctx))

	h := handleOfBead(t, s, b)
	delete(h, "harness_reset_model") // the previous release's handle
	got, err := s.GetBead(ctx, b.ID)
	must(t, err)
	must(t, s.SetHandle(ctx, *got.Session, h))
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if starts := r.starts(); !slices.Equal(starts, []string{b.ID}) {
		t.Fatalf("starts = %v, want the bead using the waiting group's model held back", starts)
	}
	if len(eventKinds(t, s, b2.ID, "session_harness_error")) != 0 {
		t.Fatal("the waiting bead should not have started and stopped on the same limit")
	}
}

// A window yardr does not wait for (a weekly one) holds nothing back: there
// is no time at which the group would go on by itself, so what starts is the
// yardmaster's to decide, as before.
func TestUsageLimitYardrDoesNotWaitForHoldsNoGroupBack(t *testing.T) {
	s, r, d, b, p, now := holdYard(t)
	ctx := context.Background()
	p.say(kimiUsed(now.Add(80 * time.Hour)))
	r.showBead(t, s, b, "idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 || evs[0].Data["wakes_at"] != nil || evs[0].Data["model"] != nil ||
		strings.Contains(evs[0].Data["hint"].(string), "starts no further session") {
		t.Fatalf("events = %v, want one with no wake and no word of a hold", evs)
	}
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b2.ID}) {
		t.Fatalf("starts = %v, want the second bead started", got)
	}
}

// The provider is not leaned on: passes that start sessions, find a group
// full and find agents working and idle, with nothing stopped on a usage
// limit, do not run its command.
func TestPassesThatStartSessionsWithNothingStoppedAskNoLimits(t *testing.T) {
	s, r, d, b, p, now := holdYard(t)
	ctx := context.Background()
	var beads []string
	for _, title := range []string{"a", "b", "c"} {
		nb, err := s.CreateBead(ctx, store.NewBead{Title: title}, "me")
		must(t, err)
		beads = append(beads, nb.ID)
		*now = now.Add(time.Minute)
		must(t, d.Reconcile(ctx))
	}
	r.showBead(t, s, b, "idle", kimiIdle)
	for range 3 {
		*now = now.Add(time.Minute)
		must(t, d.Reconcile(ctx))
	}
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	dispatch.SetClock(d2, func() time.Time { return *now })
	must(t, d2.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, beads[0], beads[1]}) {
		t.Fatalf("starts = %v, want the group filled to its limit of 3", got)
	}
	if asks := p.asks(); asks != nil {
		t.Fatalf("the limits command was run for %v by passes with nothing stopped on a usage limit", asks)
	}
}

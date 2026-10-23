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
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 3,
		Config: map[string]any{"kind": "kimi", "stall": "off", "model": "k3"}}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	must(t, err)
	bead, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx)) // start
	must(t, d.Reconcile(ctx)) // seen working
	// The provider is declared once the session runs: a start on a route
	// with no condition asks it (chooseGroup), and what it said would answer
	// the stop's read from memory. These tests are about the stop.
	p = newStandIn(t, s)
	return s, r, d, *bead, p, now
}

// One stopped session per group and model, not three (yardr-vpzj): while a
// session waits for the window of its usage limit to reset, the next bead of
// its group and model waits too, since its session would stop on the same
// limit. While the provider shows that window used up the group starts no
// session whatever its model (the route reads the windows, as a condition
// does): the bead of the waiting model is held without a question, with one
// group_wait event naming the hold, and the one of another model waits on
// the quota. When the provider shows it open,
// a minute before the wake, the bead of another model starts and the one of
// the waiting model still does not. The event says so, with the model. The
// passes of the wait ask the provider nothing more, in this daemon or its
// next, but for the one question a bead that could start puts. At the wake
// the agent is prompted and the bead that waited starts.
func TestGroupStartsNoFurtherSessionWithAModelThatWaitsForItsReset(t *testing.T) {
	t.Parallel()
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

	// The window is used up: neither bead starts. The one of the waiting
	// model is held back and asks nothing; the one of another model waits on
	// the quota, with the stop's answer.
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
	must(t, err)
	b3, err := s.CreateBead(ctx, store.NewBead{Title: "z", Metadata: map[string]any{"model": "k2"}}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	*now = now.Add(time.Minute)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v, want none while the 5h window is used up", got)
	}
	if n := len(eventKinds(t, s, b2.ID, "quota_wait")); n != 0 {
		t.Fatalf("quota_wait events of the bead held back = %d, want none: the hold keeps it before the quota is asked", n)
	}
	waits := eventKinds(t, s, b2.ID, "group_wait")
	if len(waits) != 1 {
		t.Fatalf("group_wait events of the bead held back = %d, want 1 naming the model hold", len(waits))
	}
	if reason, _ := waits[0].Data["reason"].(string); !strings.Contains(reason, "model k3") {
		t.Fatalf("group_wait reason = %q, want the held model", reason)
	}
	if waits := eventKinds(t, s, b3.ID, "quota_wait"); len(waits) != 1 || waits[0].Data["resets_at"] != resets.UTC().Format(time.RFC3339) {
		t.Fatalf("quota_wait events of the bead of another model = %v, want one with the reset", waits)
	}
	if asks := p.asks(); len(asks) != 1 {
		t.Fatalf("the provider was run %d times, want the one ask of the stop: the quota reads it for five minutes", len(asks))
	}

	// At the reset the provider shows the window open, a minute before the
	// wake: the bead of another model starts, the one of the waiting model
	// does not. A daemon that replaced itself holds the group back as well:
	// the wait is on the session's handle.
	p.say(kimiOpen)
	*now = resets.Add(time.Second)
	must(t, d.Reconcile(ctx))
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	dispatch.SetClock(d2, func() time.Time { return *now })
	must(t, d2.Reconcile(ctx))
	must(t, d2.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b3.ID}) {
		t.Fatalf("starts in the wait = %v, want the bead of another model started and the one of the waiting model not", got)
	}
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run %d times, want 2: the stop and the start of the other model; holding a group back asks nothing", len(asks))
	}

	// The wake: the provider is asked the once the wake asks, the agent is
	// prompted, and the bead that waited starts.
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
	if asks := p.asks(); len(asks) != 3 {
		t.Fatalf("the provider was run %d times, want 3: at the stop, at the start of the other model and at the wake", len(asks))
	}
}

// An agent that someone got going before the reset no longer waits, so its
// group starts sessions with its model again, once the provider shows no
// window used up: the hold lifts, and the quota still says what the provider
// does.
func TestAgentGoingAgainBeforeTheResetLiftsTheHold(t *testing.T) {
	t.Parallel()
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
	// Going again lifts the hold, and the provider still says the window is
	// used up: the second bead waits on the quota instead.
	r.showBead(t, s, b, "working", " ● Going on")
	*now = now.Add(time.Minute)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v, want the second bead waiting on the quota", got)
	}
	if n := len(eventKinds(t, s, b2.ID, "quota_wait")); n != 1 {
		t.Fatalf("quota_wait events = %d, want 1: the hold is lifted and the window is used up", n)
	}
	// It starts once the provider shows the window open.
	p.say(kimiOpen)
	*now = now.Add(5 * time.Minute)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b2.ID}) {
		t.Fatalf("starts = %v, want the second bead started once the first works again and the window is open", got)
	}
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run %d times, want 2: the stop and the quota's second look", len(asks))
	}
}

// A session already waiting at upgrade time has no harness_reset_model key.
// Its bead and group still say which model its wait must hold back.
func TestResetWaitFromBeforeUpgradeHoldsTheGroupModel(t *testing.T) {
	t.Parallel()
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
	// The hold keeps it, not the quota, which the used-up window would also
	// do: a bead held back asks nothing, so it has no quota_wait.
	if n := len(eventKinds(t, s, b2.ID, "quota_wait")); n != 0 {
		t.Fatalf("quota_wait events = %d, want none: the hold keeps the bead before the quota is asked", n)
	}
}

// A window yardr does not wait for (a weekly one) sets no hold: there is no
// time at which the group would go on by itself, so no model is held back and
// the event has no wake. The quota still keeps the group from starting a
// session into a window that is used up: the second bead waits, with the
// reset days away, and starts once the provider shows the window open.
func TestUsageLimitYardrDoesNotWaitForSetsNoHoldAndTheQuotaWaits(t *testing.T) {
	t.Parallel()
	s, r, d, b, p, now := holdYard(t)
	ctx := context.Background()
	resets := now.Add(80 * time.Hour)
	p.say(kimiUsed(resets))
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
	if got := r.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v, want the second bead waiting: the 5h window is used up", got)
	}
	if waits := eventKinds(t, s, b2.ID, "quota_wait"); len(waits) != 1 || waits[0].Data["resets_at"] != resets.UTC().Format(time.RFC3339) {
		t.Fatalf("quota_wait events = %v, want one with the reset days away", waits)
	}
	p.say(kimiOpen)
	*now = now.Add(6 * time.Minute)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID, b2.ID}) {
		t.Fatalf("starts = %v, want the second bead started once the window is open", got)
	}
}

// The provider is not leaned on: passes that start sessions ask it for the
// quota once in five minutes, however many start, and passes that find a
// group full and find agents working and idle, with nothing stopped on a
// usage limit, do not run its command.
func TestPassesThatStartSessionsWithNothingStoppedAskLimitsOnceInFiveMinutes(t *testing.T) {
	t.Parallel()
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
	if asks := p.asks(); !slices.Equal(asks, []string{"moonshot"}) {
		t.Fatalf("the limits command was run for %v, want once, for the two starts: the full group and the working and idle agents ask nothing", asks)
	}
}

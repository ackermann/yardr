package dispatch_test

import (
	"context"
	"fmt"
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
	t.Cleanup(func() { dispatch.WaitLimitsIdle(d) })
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
	primeLimits(t, d, "kimi", "", nil)
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
	dispatch.WaitLimitsIdle(d)
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run %d times, want 2: the stop and the start of the other model; holding a group back asks nothing", len(asks))
	}

	// The wake: the provider is asked the once the wake asks, the agent is
	// prompted, and the bead that waited starts.
	*now = wakes.Add(time.Second)
	primeLimits(t, d2, "kimi", "", nil)
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
	primeLimits(t, d, "kimi", "", nil)
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
	// It starts once the provider shows the window open: the pass that finds
	// the answer stale still holds it for the window the answer said was used
	// up, and the pass after the new answer has landed starts it.
	p.say(kimiOpen)
	*now = now.Add(5 * time.Minute)
	must(t, d.Reconcile(ctx))
	dispatch.WaitLimitsIdle(d)
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
	primeLimits(t, d, "kimi", "", nil)
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
	primeLimits(t, d, "kimi", "", nil)
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
	// The answer has gone stale, and the window it said was used up, until a
	// reset days off, still keeps the bead from starting: nothing fresh says
	// otherwise yet. The pass the new answer's landing wakes finds it open.
	p.say(kimiOpen)
	*now = now.Add(6 * time.Minute)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v, want the second bead waiting until the provider has answered again", got)
	}
	dispatch.WaitLimitsIdle(d)
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
		// The first pass reads the key cold, and the refresher starts its run
		// at the clock the passes last stamped. Left to run beside the next
		// passes, under load that is a later minute: the answer is then asked
		// later than the count below counts on, still holds when the clock
		// reaches the end, and the lease's due run never comes. With each run
		// landed before the clock moves on, the count does not depend on how
		// soon the refresher's goroutine ran.
		dispatch.WaitLimitsIdle(d)
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
	dispatch.WaitLimitsIdle(d)
	// The two starts are one run, as they were. The second run is the
	// wanted key's own: its answer came due (five minutes on) while its
	// lease, from the last start's read, still stood, so the refresher
	// ran it once more in the background. No pass ran it: the passes
	// that found the group full and the agents working and idle read
	// the cache and asked nothing themselves.
	if asks := p.asks(); !slices.Equal(asks, []string{"moonshot", "moonshot"}) {
		t.Fatalf("the limits command was run for %v, want the starts' run and its lease's one due run", asks)
	}
}

// The hold of a session that waits for its window is the session's, and
// ending the session (`yardr bead release`, to move its bead to another
// group) ends it. What the provider said does not end with it: a window it
// said was used up keeps the group from starting a session, whatever the
// bead's model, until the reset it gave, whether the cache holds that answer
// stale with its refresh pending or its refresh failed. From the reset on
// nothing says the window is used up, and the group starts a session again.
func TestReleasedWaitingSessionLeavesTheUsedUpWindowGatingUntilItsReset(t *testing.T) {
	t.Parallel()
	s, r, d, b, p, now := holdYard(t)
	ctx := context.Background()
	resets := now.Add(42 * time.Minute)
	p.say(kimiUsed(resets))
	primeLimits(t, d, "kimi", "", nil)
	r.showBead(t, s, b, "idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	if _, _, model, waits := dispatch.ResetWait(handleOfBead(t, s, b)); !waits || model != "k3" {
		t.Fatalf("the handle's wait = %q %v, want the session waiting with k3", model, waits)
	}

	// The release ends the wait, and the hold with it. The answer then goes
	// stale with the next refresh pending.
	_, err := s.Release(ctx, b.ID, "moved to another group", store.Expect{}, "me")
	must(t, err)
	same, err := s.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
	must(t, err)
	other, err := s.CreateBead(ctx, store.NewBead{Title: "z", Metadata: map[string]any{"model": "k2"}}, "me")
	must(t, err)
	p.hold()
	*now = now.Add(6 * time.Minute)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v, want no session after the release: the 5h window was used up until %v", got, resets)
	}
	if n := len(eventKinds(t, s, same.ID, "group_wait")); n != 0 {
		t.Fatalf("group_wait events of the bead of the model that waited = %d, want none: its session is gone", n)
	}
	for _, bead := range []store.Bead{*same, *other} {
		waits := eventKinds(t, s, bead.ID, "quota_wait")
		if len(waits) != 1 || waits[0].Data["resets_at"] != resets.UTC().Format(time.RFC3339) ||
			!strings.Contains(waits[0].Data["reason"].(string), "(last known: used up until 12:42)") {
			t.Fatalf("quota_wait events of %s = %v, want one naming the window as last known and its reset", bead.ID, waits)
		}
	}

	// The refresh fails: what was last said still stands.
	p.fail()
	p.unhold()
	dispatch.WaitLimitsIdle(d)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v, want none while the refresh fails: the window was used up until %v", got, resets)
	}

	// The reset has come, and the cache is stale again: nothing says the
	// window is used up any more.
	p.hold()
	*now = resets.Add(time.Second)
	must(t, d.Reconcile(ctx))
	if got := r.starts(); len(got) != 2 {
		t.Fatalf("starts = %v, want one more session once the reset has come", got)
	}
}

// kimiUsedModel is kimiUsed with the 5h window counting for the model alone:
// a group that runs another model is not gated by it (judge leaves a window
// the source says is for one model alone out of the comparison no route
// wrote), but a session that stopped on it still waits for it (UsedUpForStop
// falls back to it when no other window is used up).
func kimiUsedModel(five time.Time, model string) string {
	return fmt.Sprintf(`{"provider":"Kimi","windows":[`+
		`{"label":"5h","model":%q,"used_percent":100,"resets_at":%q},`+
		`{"label":"Weekly","used_percent":38,"resets_at":%q}]}`,
		model, five.Format(time.RFC3339), five.Add(80*time.Hour).Format(time.RFC3339))
}

// codexOpen is a report with no window used up, for an --else group that
// burns another provider than the held one.
const codexOpen = `{"provider":"Codex","windows":[{"label":"5h","used_percent":9},{"label":"Weekly","used_percent":27}]}`

// elseYard is a yard whose stage-new route names a first and the --else
// group b: a runs kimi sessions with the model k3, and the bead returned is
// started on a and seen working. first is the route's first group with its
// condition, if any. The provider is declared once the session runs, as
// holdYard's is, answering moonshot with moonshot's report and, where the
// yard burns a second provider, openai with openai's; both groups' route
// reads are primed, and the session is stopped on its usage limit, waiting
// for the 5h window with k3. resets is when that window resets.
func elseYard(t *testing.T, first string, b store.Group, moonshot func(time.Time) string, openai string) (s store.API, r *paneRunner, d *dispatch.Dispatcher, p *standIn, b1 store.Bead, now *time.Time, resets time.Time) {
	t.Helper()
	r = &paneRunner{retryRunner: &retryRunner{agentRunner: &agentRunner{statusRunner: &statusRunner{
		fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}, worked: true}}, panes: map[string][2]string{}}
	s = storetest.New(t)
	d = &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": r}, Backoff: time.Hour}
	t.Cleanup(func() { dispatch.WaitLimitsIdle(d) })
	at := noon()
	now = &at
	dispatch.SetClock(d, func() time.Time { return *now })
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Limit: 3,
		Config: map[string]any{"kind": "kimi", "stall": "off", "model": "k3"}}, "me"))
	must(t, s.PutGroup(ctx, b, "me"))
	g := store.ParseRouteGroup(first)
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: g.Name, When: g.When,
		Else: []store.RouteGroup{{Name: "b"}}}, "me")
	must(t, err)
	bead, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx)) // start
	must(t, d.Reconcile(ctx)) // seen working
	if got := r.starts(); !slices.Equal(got, []string{bead.ID}) {
		t.Fatalf("starts = %v, want the first bead on a", got)
	}
	if group, _ := claimedBy(t, s, bead.ID); group != "a" {
		t.Fatalf("claimed by %q, want a", group)
	}
	p = newStandIn(t, s)
	resets = at.Add(42 * time.Minute)
	p.sayFor("moonshot", moonshot(resets))
	if openai != "" {
		p.sayFor("openai", openai)
	}
	primeLimits(t, d, "kimi", "", nil)
	primeGroupLimits(t, s, d, "a")
	primeGroupLimits(t, s, d, "b")
	r.showBead(t, s, *bead, "idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	until, window, model, ok := dispatch.ResetWait(handleOfBead(t, s, *bead))
	if !ok || !until.Equal(resets.Add(time.Minute)) || window != "5h" || model != "k3" {
		t.Fatalf("the handle's wait = %v %q %q %v, want until %v for the 5h window with k3", until, window, model, ok, resets.Add(time.Minute))
	}
	return s, r, d, p, *bead, now, resets
}

// A claim passes a group its model's usage wait holds back over for the
// route's --else group (yardr-g08h): the held group is not chosen in this
// pass, and the claimed event says it was passed over for its hold. When the
// wait clears, the first group is chosen again in its turn; while every
// group waits, the bead waits with the one group_wait event as before. One
// shape per claim path: a route with no condition (this yard's review
// fallback, mem-3z7j) and one with a condition that does not cover the
// used-up window. Each shape is one where the quota does not gate the held
// group — a window for one model alone, which the comparison no route wrote
// leaves out, or a condition on another window — so only the hold stands
// between the bead and the first group.
func TestClaimPassesOverAGroupHeldByAUsageWait(t *testing.T) {
	t.Parallel()
	for _, shape := range []struct {
		name     string
		first    string
		moonshot func(time.Time) string
	}{
		{"no condition", "a", func(resets time.Time) string { return kimiUsedModel(resets, "k3") }},
		{"condition", "a@Weekly<80", kimiUsed},
	} {
		t.Run(shape.name, func(t *testing.T) {
			s, r, d, p, b1, now, resets := elseYard(t, shape.first,
				store.Group{Name: "b", Runner: "herdr", Limit: 1,
					Config: map[string]any{"kind": "codex", "stall": "off"}},
				shape.moonshot, codexOpen)
			ctx := context.Background()
			wakes := resets.Add(time.Minute)

			// Both groups wait — a on its model's hold, b paused — so the
			// bead waits with one group_wait event naming both, and starts
			// nothing.
			zero := 0
			_, err := s.SetGroup(ctx, "b", store.GroupEdit{Limit: &zero}, "me")
			must(t, err)
			b2 := newBead(t, s, "held and paused")
			must(t, d.Reconcile(ctx))
			if got := r.starts(); !slices.Equal(got, []string{b1.ID}) {
				t.Fatalf("starts = %v, want none while both groups wait", got)
			}
			waits := eventKinds(t, s, b2.ID, "group_wait")
			if len(waits) != 1 {
				t.Fatalf("group_wait events = %d, want 1 naming both waits", len(waits))
			}
			if reason, _ := waits[0].Data["reason"].(string); !strings.Contains(reason, "model k3") || !strings.Contains(reason, "paused (limit 0)") {
				t.Fatalf("group_wait reason = %q, want the hold and the pause", reason)
			}

			// b unpaused: the claim passes the held a over for b, and the
			// claimed event says a was passed over for its hold. The choice
			// reads only b's windows: a, passed over, is not judged.
			three := 3
			_, err = s.SetGroup(ctx, "b", store.GroupEdit{Limit: &three}, "me")
			must(t, err)
			asked := p.asks()
			must(t, d.Reconcile(ctx))
			if got := r.starts(); !slices.Equal(got, []string{b1.ID, b2.ID}) {
				t.Fatalf("starts = %v, want the waiting bead started on the --else group", got)
			}
			group, data := claimedBy(t, s, b2.ID)
			if group != "b" {
				t.Fatalf("claimed by %q with %v, want b", group, data)
			}
			passed, _ := data["passed"].([]any)
			if len(passed) != 1 {
				t.Fatalf("passed = %v, want the one held group", data["passed"])
			}
			one, _ := passed[0].(map[string]any)
			if one["group"] != "a" || !strings.Contains(fmt.Sprint(one["reason"]), "model k3") {
				t.Fatalf("passed = %v, want a passed over for its model's hold", one)
			}
			if got := p.asks(); !slices.Equal(got, asked) {
				t.Fatalf("asks = %v, want no further run: the claim read b's cached windows and judged nothing of a", got)
			}

			// The wait clears: the agent is prompted at the wake, and the
			// next bead goes to the first group again.
			*now = wakes.Add(time.Second)
			primeLimits(t, d, "kimi", "", nil)
			b3 := newBead(t, s, "after the wait")
			must(t, d.Reconcile(ctx))
			if len(r.nudges) != 1 || r.nudges[0] != resetLine {
				t.Fatalf("typed %q at the wake, want the reset line", r.nudges)
			}
			if _, _, _, waits := dispatch.ResetWait(handleOfBead(t, s, b1)); waits {
				t.Fatalf("the wait is still on the handle after the prompt: %v", handleOfBead(t, s, b1))
			}
			if got := r.starts(); !slices.Equal(got, []string{b1.ID, b2.ID, b3.ID}) {
				t.Fatalf("starts = %v, want the next bead started once the wait clears", got)
			}
			if group, _ := claimedBy(t, s, b3.ID); group != "a" {
				t.Fatalf("claimed by %q, want a again", group)
			}
		})
	}
}

// While every group of the route is held back for its model's usage wait,
// the bead waits with one group_wait event naming each hold, and starts
// nothing; when the waits clear, the route's first group takes it again.
// The --else group burns the same provider with another model, which the
// used-up window of the first, counting for its model alone, does not gate.
func TestClaimWaitsWhileBothGroupsModelsAreHeld(t *testing.T) {
	t.Parallel()
	s, r, d, p, b1, now, resets := elseYard(t, "a",
		store.Group{Name: "b", Runner: "herdr", Limit: 3,
			Config: map[string]any{"kind": "kimi", "stall": "off", "model": "k2"}},
		func(resets time.Time) string { return kimiUsedModel(resets, "k3") }, "")
	ctx := context.Background()
	wakes := resets.Add(time.Minute)

	// The held a is passed over for b, whose model the used-up window,
	// counting for k3 alone, does not gate.
	b2 := newBead(t, s, "other model")
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b1.ID, b2.ID}) {
		t.Fatalf("starts = %v, want the bead started on the --else group", got)
	}
	if group, _ := claimedBy(t, s, b2.ID); group != "b" {
		t.Fatalf("claimed by %q, want b", group)
	}

	// A session of b stops on the same window, waiting with k2: with both
	// models held back, the next bead waits with one group_wait naming
	// both holds. bx reaches b by a labelled route, so this leg runs
	// whether or not the route's --else is taken above.
	label := "to-b"
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Label: &label, Group: "b"}, "me")
	must(t, err)
	bx, err := s.CreateBead(ctx, store.NewBead{Title: "holds b", Labels: []string{label}}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, bx.ID); group != "b" {
		t.Fatalf("claimed by %q, want b by the labelled route", group)
	}
	r.showBead(t, s, *bx, "idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	if _, _, model, ok := dispatch.ResetWait(handleOfBead(t, s, *bx)); !ok || model != "k2" {
		t.Fatalf("bx's wait model = %q %v, want a wait with k2", model, ok)
	}
	b3 := newBead(t, s, "both held")
	must(t, d.Reconcile(ctx))
	if got := r.starts(); !slices.Equal(got, []string{b1.ID, b2.ID, bx.ID}) {
		t.Fatalf("starts = %v, want none while both models are held back", got)
	}
	waits := eventKinds(t, s, b3.ID, "group_wait")
	if len(waits) != 1 {
		t.Fatalf("group_wait events = %d, want 1 naming both holds", len(waits))
	}
	if reason, _ := waits[0].Data["reason"].(string); !strings.Contains(reason, "model k3") || !strings.Contains(reason, "model k2") {
		t.Fatalf("group_wait reason = %q, want both held models", reason)
	}

	// Both waits clear: each agent is prompted at the wake, and the bead
	// that waited goes to the first group again.
	p.sayFor("moonshot", kimiOpen)
	*now = wakes.Add(time.Second)
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 2 {
		t.Fatalf("nudges = %q, want the reset line for each waiting agent", r.nudges)
	}
	for _, n := range r.nudges {
		if n != resetLine {
			t.Fatalf("nudges = %q, want the reset line twice", r.nudges)
		}
	}
	if got := r.starts(); !slices.Equal(got, []string{b1.ID, b2.ID, bx.ID, b3.ID}) {
		t.Fatalf("starts = %v, want the waiting bead started once the waits clear", got)
	}
	if group, _ := claimedBy(t, s, b3.ID); group != "a" {
		t.Fatalf("claimed by %q, want a again", group)
	}
}

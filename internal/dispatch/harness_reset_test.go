package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// resetLine is what an agent stopped mid-work on kimi's 5h window is told
// when it has reset.
const resetLine = "yardr: the usage limit your harness stopped on (kimi, 5h window) has reset. Go on where you stopped."

// kimiMenu is the end of a Kimi pane that was prompted after a long idle
// spell, as the yardmaster saw it on 7 Oct 2026 and as Kimi Code 2.1.1 draws
// it (its cache-hint-dialog, in place of the input box): the usage limit it
// stopped on still above, the prompt nowhere.
const kimiMenu = " Error: [provider.auth_error] 403 You've reached your\n" +
	" 5-hour usage limit. Please wait for the quota to refresh.\n" +
	" ────────────────────────────────────────────────────────────\n" +
	"  This session has been idle for 44m and is ~198k tokens.\n" +
	"  ↑↓ navigate · Enter select · Esc cancel\n" +
	"  Cache expired — the next message re-sends the entire history at full price.\n" +
	"   ❯ Compact and continue    one-time compact cost · cheapest way to keep this topic\n" +
	"     Start a new session     zero context cost · best for a new task\n" +
	"     Continue as-is          full history kept · highest cost per turn\n" +
	"     Don't ask me again\n" +
	" ────────────────────────────────────────────────────────────\n" +
	" Ask When Needed  K3 thinking: high  ~         ctrl+o expand"

// kimiOpen is a report with no window used up: the 5h window has reset.
const kimiOpen = `{"provider":"Kimi","windows":[{"label":"5h","used_percent":0},{"label":"Weekly","used_percent":38}]}`

// watchRunner is a retryRunner that can say whether a prompt shows in the
// pane as taken, as the herdr runner can.
type watchRunner struct {
	*retryRunner
	shown bool
}

func (w *watchRunner) PromptShown(context.Context, map[string]any, string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.shown, nil
}

// resetYard is a yard with one kimi session that has worked and now works,
// a stand-in provider of limits, and a clock the test moves. The runner can
// type into the agent's pane and knows its first prompt.
func resetYard(t *testing.T, cfg map[string]any) (s store.API, r *retryRunner, d *dispatch.Dispatcher, b store.Bead, p *standIn, now *time.Time) {
	t.Helper()
	r = &retryRunner{agentRunner: &agentRunner{statusRunner: &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}},
		status: "working"}, worked: true}}
	s, d, b, p, now = resetYardOf(t, r, cfg)
	return s, r, d, b, p, now
}

func resetYardOf(t *testing.T, r dispatch.Runner, cfg map[string]any) (s store.API, d *dispatch.Dispatcher, b store.Bead, p *standIn, now *time.Time) {
	t.Helper()
	s = storetest.New(t)
	d = &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	t.Cleanup(func() { dispatch.WaitLimitsIdle(d) })
	at := noon()
	now = &at
	dispatch.SetClock(d, func() time.Time { return *now })
	conf := map[string]any{"kind": "kimi", "stall": "off"}
	for k, v := range cfg {
		conf[k] = v
	}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: conf}, "me"))
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
	return s, d, *bead, p, now
}

// handleOfBead is the handle of the session that works b.
func handleOfBead(t *testing.T, s store.API, b store.Bead) map[string]any {
	t.Helper()
	got, err := s.GetBead(context.Background(), b.ID)
	must(t, err)
	if got.Session == nil {
		t.Fatalf("bead %s has no session", b.ID)
	}
	sess, err := s.GetSession(context.Background(), *got.Session)
	must(t, err)
	return sess.Handle
}

// The 6 Oct case: an agent stops on a usage limit whose window resets in 42
// minutes. The yardmaster is told at once, with when yardr looks again; the
// passes of the wait type nothing and ask the provider nothing; a minute
// after the reset the provider is asked once more and the agent, stopped
// mid-work, is told that the limit has reset. It gets going, and that was
// the one event.
func TestSessionAtItsUsageLimitIsPromptedWhenTheWindowHasReset(t *testing.T) {
	t.Parallel()
	s, r, d, b, p, now := resetYard(t, nil)
	ctx := context.Background()
	resets := now.Add(42 * time.Minute)
	p.say(kimiUsed(resets))
	primeLimits(t, d, "kimi", "", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))

	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 {
		t.Fatalf("session_harness_error events = %d, want 1 at the stop", len(evs))
	}
	wakes := resets.Add(time.Minute)
	hint, _ := evs[0].Data["hint"].(string)
	if evs[0].Data["wakes_at"] != wakes.UTC().Format(time.RFC3339) || evs[0].Data["window"] != "5h" ||
		!strings.HasPrefix(hint, "kimi's 5h window is used up and resets at 12:42 (in 42m); yardr asks again at 12:43 and prompts the agent when the window is open; "+
			"the group starts no further session with this model until then; the agent's harness stopped") {
		t.Fatalf("data = %v, want wakes_at a minute after the reset and the hint saying so", evs[0].Data)
	}

	// The wait: nothing typed, nothing asked, by this daemon or its next.
	for _, by := range []time.Duration{time.Second, 20 * time.Minute, 42 * time.Minute} {
		*now = noon().Add(by)
		must(t, d.Reconcile(ctx))
	}
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	dispatch.SetClock(d2, func() time.Time { return *now })
	must(t, d2.Reconcile(ctx))
	if asks := p.asks(); len(r.nudges) != 0 || len(asks) != 1 {
		t.Fatalf("in the wait: typed %q, the provider run %d times; want nothing typed and the one ask of the stop", r.nudges, len(asks))
	}

	// The wake, first with no fresh answer on this daemon: its cache is
	// cold, so the wake defers — nothing is typed, no retry is spent,
	// the error is not raised again, and harness_reset_at stands for
	// the next pass, once the refresh the read wanted has landed.
	p.say(kimiOpen)
	*now = wakes.Add(time.Second)
	must(t, d2.Reconcile(ctx))
	if len(r.nudges) != 0 || len(eventKinds(t, s, b.ID, "session_harness_retry")) != 0 {
		t.Fatalf("at a cold wake: typed %q; want a defer, nothing typed and no retry spent", r.nudges)
	}
	if h := handleOfBead(t, s, b); h["harness_reset_at"] == nil {
		t.Fatalf("handle = %v, want the wake left standing at a cold read", h)
	}
	primeLimits(t, d2, "kimi", "", nil)
	must(t, d2.Reconcile(ctx))
	must(t, d2.Reconcile(ctx))
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run %d times, want 2: at the stop and at the wake", len(asks))
	}
	if len(r.nudges) != 1 || r.nudges[0] != resetLine {
		t.Fatalf("typed %q, want the line for an agent stopped mid-work, once", r.nudges)
	}
	tries := eventKinds(t, s, b.ID, "session_harness_retry")
	if len(tries) != 1 {
		t.Fatalf("session_harness_retry events = %d, want 1", len(tries))
	}
	if data := tries[0].Data; data["try"] != float64(1) || data["of"] != float64(dispatch.HarnessRetries) ||
		data["reason"] != dispatch.ReasonRateLimited || data["window"] != "5h" || data["prompted"] != resetLine ||
		data["kind"] != "kimi" || data["line"] != "Error: [provider.auth_error] 403 You've reached your" {
		t.Fatalf("session_harness_retry data = %v", data)
	}

	// It works: the watch is over, and nothing more is told or asked.
	r.show("working", " ● Going on with the review.")
	must(t, d2.Reconcile(ctx))
	h := handleOfBead(t, s, b)
	for _, key := range []string{dispatch.HarnessErrorKey, "harness_reset_at", "harness_reset_window", "harness_retry_at", "harness_prompted"} {
		if h[key] != nil {
			t.Fatalf("handle = %v; %s outlived the agent getting going", h, key)
		}
	}
	*now = now.Add(10 * time.Minute)
	must(t, d2.Reconcile(ctx))
	if n, asks := len(eventKinds(t, s, b.ID, "session_harness_error")), p.asks(); n != 1 || len(asks) != 2 || len(r.nudges) != 1 {
		t.Fatalf("events = %d, asks = %d, typed %d; want the one event, two asks and one prompt", n, len(asks), len(r.nudges))
	}
}

// A session of a group with the provider setting is asked as the provider
// at the wake too: the window open, it is prompted.
func TestProviderSessionIsAskedAsTheProviderAtTheWake(t *testing.T) {
	t.Parallel()
	s, r, d, b, p, now := resetYard(t, map[string]any{"provider": "zai"})
	ctx := context.Background()
	resets := now.Add(42 * time.Minute)
	p.say(kimiUsed(resets))
	primeLimits(t, d, "kimi", "zai", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 || evs[0].Data["window"] != "5h" {
		t.Fatalf("session_harness_error events = %v, want one with the 5h window", evs)
	}
	if asks := p.asks(); len(asks) != 1 || asks[0] != "zai" {
		t.Fatalf("at the stop the provider was run for %v, want once, for the setting", asks)
	}
	wakes := resets.Add(time.Minute)
	p.say(kimiOpen)
	*now = wakes.Add(time.Second)
	primeLimits(t, d, "kimi", "zai", nil)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if asks := p.asks(); len(asks) != 2 || asks[1] != "zai" {
		t.Fatalf("the provider was run for %v, want the stop's and the wake's, both for the setting", asks)
	}
	if len(r.nudges) != 1 || r.nudges[0] != resetLine {
		t.Fatalf("typed %q, want the line for an agent stopped mid-work, once", r.nudges)
	}
}

// A session that had done nothing when it stopped gets its first prompt
// again, as one at capacity does: that is known to be the right thing to say.
func TestSessionThatHasDoneNothingGetsItsFirstPromptAtTheReset(t *testing.T) {
	t.Parallel()
	_, r, d, _, p, now := resetYard(t, nil)
	ctx := context.Background()
	r.mu.Lock()
	r.worked = false
	r.mu.Unlock()
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	primeLimits(t, d, "kimi", "", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	p.say(kimiOpen)
	*now = now.Add(44 * time.Minute)
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 1 || r.nudges[0] != "Read brief.md and follow it." {
		t.Fatalf("typed %q, want the first prompt", r.nudges)
	}
}

// A weekly window is days away: nothing is scheduled, the event has no
// wakes_at, the session is the yardmaster's as before and its idle spell
// counts. Nor is one whose reset the provider does not say, or a limit no
// source knows of.
func TestUsageLimitThatResetsLaterThanYardrWaitsFor(t *testing.T) {
	t.Parallel()
	week := noon().Add(80 * time.Hour).Format(time.RFC3339)
	for name, report := range map[string]string{
		"a weekly window": `{"provider":"Kimi","windows":[{"label":"5h","used_percent":100,"resets_at":"` + noon().Add(42*time.Minute).Format(time.RFC3339) +
			`"},{"label":"Weekly","used_percent":100,"resets_at":"` + week + `"}]}`,
		"just over six hours": `{"provider":"Kimi","windows":[{"label":"5h","used_percent":100,"resets_at":"` + noon().Add(6*time.Hour+time.Minute).Format(time.RFC3339) + `"}]}`,
		"no reset said":       `{"provider":"Kimi","windows":[{"label":"5h","used_percent":100}]}`,
		"no window used up":   kimiOpen,
		"no report":           "Kimi: 100%",
	} {
		t.Run(name, func(t *testing.T) {
			s, r, d, b, p, now := resetYard(t, map[string]any{"idle": "30ms"})
			ctx := context.Background()
			p.say(report)
			r.show("idle", kimiLimit)
			must(t, d.Reconcile(ctx))
			evs := eventKinds(t, s, b.ID, "session_harness_error")
			if len(evs) != 1 || evs[0].Data["wakes_at"] != nil || strings.Contains(evs[0].Data["hint"].(string), "yardr asks again") {
				t.Fatalf("session_harness_error = %v, want one with no wake", evs)
			}
			if h := handleOfBead(t, s, b); h["harness_reset_at"] != nil {
				t.Fatalf("handle = %v, want no wait kept", h)
			}
			time.Sleep(40 * time.Millisecond)
			for _, by := range []time.Duration{time.Hour, 7 * time.Hour, 81 * time.Hour} {
				*now = noon().Add(by)
				must(t, d.Reconcile(ctx))
			}
			if asks := p.asks(); len(asks) != 1 || len(r.nudges) != 0 {
				t.Fatalf("the provider was run %d times, typed %q; want the one ask of the stop and nothing typed", len(asks), r.nudges)
			}
			if n := len(eventKinds(t, s, b.ID, "session_idle")); n != 1 {
				t.Fatalf("session_idle events = %d, want 1: its idle spell counts as before", n)
			}
		})
	}
}

// At the wake the window is still used up, and the provider gives a later
// reset: the agent is not prompted. The event is raised again with the new
// time, since what the first said no longer holds, and yardr waits for that.
// The shared-window case also checks that a per-model window does not hold
// the prompt back after the window every model draws from has opened.
func TestWindowStillUsedUpAtTheWakeWaitsForTheLaterReset(t *testing.T) {
	t.Parallel()
	s, r, d, b, p, now := resetYard(t, nil)
	ctx := context.Background()
	first := now.Add(42 * time.Minute)
	p.say(kimiUsed(first))
	primeLimits(t, d, "kimi", "", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))

	later := first.Add(2 * time.Hour)
	p.say(kimiUsed(later))
	*now = first.Add(time.Minute + time.Second)
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(r.nudges) != 0 || len(evs) != 2 || evs[1].Data["wakes_at"] != later.Add(time.Minute).UTC().Format(time.RFC3339) ||
		evs[1].Data["resets_at"] != later.UTC().Format(time.RFC3339) {
		t.Fatalf("typed %q, events = %v; want nothing typed and a second event with the later reset and wake", r.nudges, evs)
	}
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run %d times, want 2: the stop and the wake, whose answer the event carries", len(asks))
	}
	if n := len(eventKinds(t, s, b.ID, "session_harness_retry")); n != 0 {
		t.Fatalf("session_harness_retry events = %d for an agent that was not prompted", n)
	}

	// The later reset comes, the window is open: prompted.
	p.say(kimiOpen)
	*now = later.Add(time.Minute + time.Second)
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 1 || r.nudges[0] != resetLine {
		t.Fatalf("typed %q at the later reset, want the line once", r.nudges)
	}

	// A weekly window used up meanwhile is not waited for.
	s2, r2, d2, b2, p2, now2 := resetYard(t, nil)
	p2.say(kimiUsed(now2.Add(42 * time.Minute)))
	primeLimits(t, d2, "kimi", "", nil)
	r2.show("idle", kimiLimit)
	must(t, d2.Reconcile(ctx))
	p2.say(`{"provider":"Kimi","windows":[{"label":"Weekly","used_percent":100,"resets_at":"` + now2.Add(80*time.Hour).Format(time.RFC3339) + `"}]}`)
	*now2 = now2.Add(44 * time.Minute)
	primeLimits(t, d2, "kimi", "", nil)
	must(t, d2.Reconcile(ctx))
	evs = eventKinds(t, s2, b2.ID, "session_harness_error")
	if len(r2.nudges) != 0 || len(evs) != 2 || evs[1].Data["window"] != "Weekly" || evs[1].Data["wakes_at"] != nil {
		t.Fatalf("typed %q, events = %v; want a second event for the weekly window with no wake", r2.nudges, evs)
	}
	if h := handleOfBead(t, s2, b2); h["harness_reset_at"] != nil {
		t.Fatalf("handle = %v, want no wait kept for a weekly window", h)
	}

	t.Run("shared window before a per-model window", func(t *testing.T) {
		ctx := context.Background()
		s, r, d, b, p, now := resetYard(t, nil)
		told := func() []store.Event { t.Helper(); return eventKinds(t, s, b.ID, "session_harness_error") }
		five, fable := now.Add(30*time.Minute), now.Add(72*time.Hour)
		p.say(kimiFable(100, five, fable))
		primeLimits(t, d, "kimi", "", nil)
		r.show("idle", kimiLimit)
		must(t, d.Reconcile(ctx))
		evs := told()
		if len(evs) != 1 {
			t.Fatalf("session_harness_error events = %v, want one at the stop", evs)
		}
		if hint, _ := evs[0].Data["hint"].(string); evs[0].Data["window"] != "5h" || evs[0].Data["resets_at"] != five.UTC().Format(time.RFC3339) ||
			evs[0].Data["wakes_at"] != five.Add(time.Minute).UTC().Format(time.RFC3339) ||
			!strings.HasPrefix(hint, "kimi's 5h window is used up and resets at 12:30 (in 30m); yardr asks again at 12:31 and prompts the agent when the window is open; ") {
			t.Fatalf("data = %v, want the 5h window with its reset and the wake after it, not Weekly Fable", evs[0].Data)
		}

		// The provider is late: 5h is used up at the wake with a later reset,
		// and that holds the prompt although Weekly Fable resets later still.
		// It is told again as 5h, not as Weekly Fable.
		later := five.Add(time.Hour)
		p.say(kimiFable(100, later, fable))
		*now = five.Add(time.Minute + time.Second)
		primeLimits(t, d, "kimi", "", nil)
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx))
		evs = told()
		if len(r.nudges) != 0 || len(evs) != 2 || evs[1].Data["window"] != "5h" || evs[1].Data["resets_at"] != later.UTC().Format(time.RFC3339) ||
			evs[1].Data["wakes_at"] != later.Add(time.Minute).UTC().Format(time.RFC3339) {
			t.Fatalf("typed %q, events = %v; want nothing typed and the 5h window told again with the later reset and wake", r.nudges, evs)
		}

		// 5h is open and Weekly Fable is not: the agent is prompted for the
		// window it waited for.
		p.say(kimiFable(0, later.Add(5*time.Hour), fable))
		*now = later.Add(time.Minute + time.Second)
		primeLimits(t, d, "kimi", "", nil)
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx))
		if len(r.nudges) != 1 || r.nudges[0] != resetLine {
			t.Fatalf("typed %q, want the reset line once, with Weekly Fable still used up", r.nudges)
		}

		// A Fable session stops again on its own window, which is all that is
		// used up now. It is told of Weekly Fable, with no wake for a reset
		// three days off: one prompt, and it corrects itself.
		r.show("idle", kimiLimit+"\n > "+resetLine+"\n"+kimiLimit)
		*now = now.Add(2*time.Minute + time.Second)
		primeLimits(t, d, "kimi", "", nil)
		must(t, d.Reconcile(ctx))
		evs = told()
		if len(evs) != 3 || evs[2].Data["window"] != "Weekly Fable" || evs[2].Data["resets_at"] != fable.UTC().Format(time.RFC3339) ||
			evs[2].Data["wakes_at"] != nil || evs[2].Data["tries"] != float64(1) {
			t.Fatalf("events = %v; want a third, for Weekly Fable with its reset, no wake, and one try", evs)
		}
		if h := handleOfBead(t, s, b); h["harness_reset_at"] != nil {
			t.Fatalf("handle = %v, want no wait kept for a window days off", h)
		}
		*now = now.Add(7 * time.Hour)
		must(t, d.Reconcile(ctx))
		if len(r.nudges) != 1 || len(told()) != 3 {
			t.Fatalf("typed %q, %d events; want nothing more for a window days off", r.nudges, len(told()))
		}
	})

	t.Run("only a per-model window is used", func(t *testing.T) {
		s, r, d, b, p, now := resetYard(t, nil)
		fable := now.Add(2 * time.Hour)
		p.say(kimiFable(0, now.Add(4*time.Hour), fable))
		primeLimits(t, d, "kimi", "", nil)
		r.show("idle", kimiLimit)
		must(t, d.Reconcile(context.Background()))
		evs := eventKinds(t, s, b.ID, "session_harness_error")
		if len(evs) != 1 || evs[0].Data["window"] != "Weekly Fable" || evs[0].Data["resets_at"] != fable.UTC().Format(time.RFC3339) ||
			evs[0].Data["wakes_at"] != fable.Add(time.Minute).UTC().Format(time.RFC3339) {
			t.Fatalf("session_harness_error = %v; want Weekly Fable with its reset and the wake after it", evs)
		}
		p.say(kimiOpen)
		*now = fable.Add(time.Minute + time.Second)
		primeLimits(t, d, "kimi", "", nil)
		must(t, d.Reconcile(context.Background()))
		must(t, d.Reconcile(context.Background()))
		if want := "yardr: the usage limit your harness stopped on (kimi, Weekly Fable window) has reset. Go on where you stopped."; len(r.nudges) != 1 || r.nudges[0] != want {
			t.Fatalf("typed %q, want %q once", r.nudges, want)
		}
	})
}

// kimiFable is a report with a window for one model beside the shared ones, as
// Claude's has: 5h at the given use and resetting at five, Weekly not used up,
// and Weekly Fable, which counts for Fable alone, used up and resetting at
// fable.
func kimiFable(fivePercent float64, five, fable time.Time) string {
	return fmt.Sprintf(`{"provider":"Kimi","windows":[`+
		`{"label":"5h","used_percent":%v,"resets_at":%q},`+
		`{"label":"Weekly","used_percent":38,"resets_at":%q},`+
		`{"label":"Weekly Fable","model":"Fable","used_percent":100,"resets_at":%q}]}`,
		fivePercent, five.Format(time.RFC3339), five.Add(80*time.Hour).Format(time.RFC3339), fable.Format(time.RFC3339))
}

// The agent is prompted and its harness answers with the limit again: two
// minutes on the error is raised as any is, with how often yardr prompted.
// yardr prompts HarnessRetries times in the session's life and then waits
// for nothing more.
func TestUsageLimitStandingAgainAfterThePromptIsRaised(t *testing.T) {
	t.Parallel()
	s, r, d, b, p, now := resetYard(t, nil)
	ctx := context.Background()
	told := func() []store.Event { t.Helper(); return eventKinds(t, s, b.ID, "session_harness_error") }
	r.show("idle", kimiLimit)
	for try := 1; try <= dispatch.HarnessRetries; try++ {
		// The provider is wrong each time: it says the window opens in ten
		// minutes, and says it is open when asked then.
		p.say(kimiUsed(now.Add(10 * time.Minute)))
		primeLimits(t, d, "kimi", "", nil)
		must(t, d.Reconcile(ctx))
		evs := told()
		if len(evs) != try || evs[try-1].Data["wakes_at"] == nil {
			t.Fatalf("before try %d: events = %v, want %d, the last with a wake", try, evs, try)
		}
		if try > 1 && (evs[try-1].Data["tries"] != float64(try-1) ||
			!strings.HasPrefix(evs[try-1].Data["hint"].(string), fmt.Sprintf("yardr has prompted the agent again %d times in this session; ", try-1))) {
			t.Fatalf("before try %d: data = %v, want tries %d and the hint saying so", try, evs[try-1].Data, try-1)
		}
		p.say(kimiOpen)
		*now = now.Add(11*time.Minute + time.Second)
		primeLimits(t, d, "kimi", "", nil)
		must(t, d.Reconcile(ctx))
		if len(r.nudges) != try {
			t.Fatalf("try %d: typed %q", try, r.nudges)
		}
		// The harness printed the error again under the prompt.
		r.show("idle", kimiLimit+"\n > "+resetLine+"\n"+kimiLimit)
		*now = now.Add(time.Minute)
		must(t, d.Reconcile(ctx))
		if n := len(told()); n != try {
			t.Fatalf("try %d: session_harness_error raised a minute after the prompt; the look is at two", try)
		}
		*now = now.Add(time.Minute + time.Second)
	}
	p.say(kimiUsed(now.Add(10 * time.Minute)))
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	evs := told()
	last := evs[len(evs)-1].Data
	if len(evs) != dispatch.HarnessRetries+1 || last["tries"] != float64(dispatch.HarnessRetries) || last["wakes_at"] != nil ||
		last["reason"] != dispatch.ReasonRateLimited || last["tail"] == nil {
		t.Fatalf("events = %d, the last %v; want %d, the last with tries %d and no further wake", len(evs), last,
			dispatch.HarnessRetries+1, dispatch.HarnessRetries)
	}
	*now = now.Add(12 * time.Minute)
	must(t, d.Reconcile(ctx))
	*now = now.Add(3 * time.Hour)
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != dispatch.HarnessRetries || len(told()) != dispatch.HarnessRetries+1 {
		t.Fatalf("typed %d times, %d events; want %d prompts in the session's life and nothing after", len(r.nudges), len(told()), dispatch.HarnessRetries)
	}
	if n := len(eventKinds(t, s, b.ID, "session_harness_retry")); n != dispatch.HarnessRetries {
		t.Fatalf("session_harness_retry events = %d, want %d", n, dispatch.HarnessRetries)
	}
}

// While a session waits for its window, it is not idle to the group's
// limits: no session_idle at the idle limit, and a group with grace does not
// end it. max_age still applies.
func TestSessionWaitingForItsWindowIsNotIdleAndStillAges(t *testing.T) {
	t.Parallel()
	s, r, d, b, p, now := resetYard(t, map[string]any{"idle": "30ms", "grace": "0", "max_age": "1h"})
	ctx := context.Background()
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	primeLimits(t, d, "kimi", "", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	time.Sleep(50 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if n := len(eventKinds(t, s, b.ID, "session_idle")); n != 0 {
		t.Fatalf("session_idle events = %d for a session that waits for its window", n)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Session == nil {
		t.Fatal("the session was ended at the idle limit and its grace while it waited for its window")
	}
	if n := len(eventKinds(t, s, b.ID, "session_aged")); n != 0 {
		t.Fatalf("session_aged events = %d before max_age", n)
	}

	// The same wait in a group whose max_age has passed: told, and with the
	// group's grace ended, window or none. A session's age is read from the
	// wall clock, not the dispatcher's, so the sleep is all the test asks of
	// it: the grace is one no machine reaches. Without a grace the session
	// is ended at twice max_age, which a loaded machine takes over the
	// passes below (yardr-h991).
	s, r, d, b, p, now = resetYard(t, map[string]any{"idle": "off", "max_age": "1s", "grace": "1h"})
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	primeLimits(t, d, "kimi", "", nil)
	r.show("idle", kimiLimit)
	time.Sleep(time.Second)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if h := handleOfBead(t, s, b); h["harness_reset_at"] == nil {
		t.Fatalf("handle = %v, want the wait kept", h)
	}
	if n := len(eventKinds(t, s, b.ID, "session_aged")); n != 1 {
		t.Fatalf("session_aged events = %d, want 1: max_age applies to a session that waits for its window", n)
	}
}

// A prompt that sits behind a question of the harness is not a prompt taken
// (seen live, 7 Oct 2026: Kimi's menu after a long idle spell). Two minutes
// after the prompt the agent is idle and the prompt does not show in its
// pane: that is told, with the pane's end, the provider is not asked, and
// nothing more is typed, no key pressed.
func TestPromptBehindAHarnessQuestionIsToldNotCountedAsTaken(t *testing.T) {
	t.Parallel()
	r := &watchRunner{retryRunner: &retryRunner{agentRunner: &agentRunner{statusRunner: &statusRunner{
		fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}, worked: true}}}
	s, d, b, p, now := resetYardOf(t, r, nil)
	ctx := context.Background()
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	primeLimits(t, d, "kimi", "", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	p.say(kimiOpen)
	*now = now.Add(44 * time.Minute)
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 1 {
		t.Fatalf("typed %q, want the prompt at the reset", r.nudges)
	}
	r.show("idle", kimiMenu)
	*now = now.Add(time.Minute)
	must(t, d.Reconcile(ctx))
	if n := len(eventKinds(t, s, b.ID, "session_harness_error")); n != 1 {
		t.Fatalf("session_harness_error events = %d a minute after the prompt, want the one of the stop", n)
	}
	*now = now.Add(time.Minute + time.Second)
	must(t, d.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 2 {
		t.Fatalf("session_harness_error events = %d, want a second for the prompt that was not taken", len(evs))
	}
	data := evs[1].Data
	if data["prompt_shown"] != false || data["prompted"] != resetLine || data["tail"] != kimiMenu || data["tries"] != float64(1) ||
		data["reason"] != dispatch.ReasonRateLimited || data["kind"] != "kimi" || data["limits"] != nil || data["wakes_at"] != nil ||
		!strings.Contains(data["hint"].(string), "does not show in its pane") || !strings.Contains(data["hint"].(string), "yardr bead release "+b.ID) ||
		!strings.Contains(data["hint"].(string), "may be asking something first (Kimi asks what to do with a session that sat idle, and keeps the prompt until it is answered). ") {
		t.Fatalf("data = %v", data)
	}
	// Once, and it is left alone: no prompt again, no ask, however long.
	for _, by := range []time.Duration{time.Minute, 20 * time.Minute, 3 * time.Hour} {
		*now = now.Add(by)
		must(t, d.Reconcile(ctx))
	}
	if n, asks := len(eventKinds(t, s, b.ID, "session_harness_error")), p.asks(); n != 2 || len(r.nudges) != 1 || len(asks) != 2 {
		t.Fatalf("events = %d, typed %d times, asks = %d; want 2 events, the one prompt and the asks of the stop and the wake", n, len(r.nudges), len(asks))
	}
}

// What a harness is known to ask before it takes the prompt is its file's
// to say (hints.reset, yardr-5yww): the hint has kimi's sentence for a kimi
// agent, and for a kind whose file has none, or that has no file, it names
// no harness and reads on unbroken.
func TestHintOfAPromptNotTakenHasTheKindsOwnSentence(t *testing.T) {
	t.Parallel()
	const kimis = " (Kimi asks what to do with a session that sat idle, and keeps the prompt until it is answered)"
	plain := dispatch.NotTakenHint("claude")
	if strings.Contains(plain, "Kimi") || !strings.Contains(plain, "may be asking something first. The pane's last lines are in tail.") {
		t.Errorf("claude, whose file has no hint: %q", plain)
	}
	if got := dispatch.NotTakenHint("cursor"); got != plain {
		t.Errorf("cursor, which has no file: %q, want what claude has", got)
	}
	if got, want := dispatch.NotTakenHint("kimi"), strings.Replace(plain, "first.", "first"+kimis+".", 1); got != want {
		t.Errorf("kimi: %q, want %q", got, want)
	}
}

// A prompt that shows in the pane was taken. The limit the agent stopped on
// is still on its screen above the prompt: that is the old error, and an
// agent that is idle again two minutes on is told of for nothing.
func TestPromptTakenWithTheOldErrorAboveItRaisesNothing(t *testing.T) {
	t.Parallel()
	r := &watchRunner{retryRunner: &retryRunner{agentRunner: &agentRunner{statusRunner: &statusRunner{
		fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}, worked: true}}}
	s, d, b, p, now := resetYardOf(t, r, nil)
	ctx := context.Background()
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	primeLimits(t, d, "kimi", "", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	p.say(kimiOpen)
	*now = now.Add(44 * time.Minute)
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	r.mu.Lock()
	r.shown = true
	r.mu.Unlock()
	r.show("idle", kimiLimit+"\n > "+resetLine+"\n ● The review is noted on the bead.\n"+kimiIdle)
	*now = now.Add(2*time.Minute + time.Second)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if evs := eventKinds(t, s, b.ID, "session_harness_error"); len(evs) != 1 {
		t.Fatalf("session_harness_error events = %v, want only the one of the stop", evs)
	}
	if h := handleOfBead(t, s, b); h["harness_prompted"] != nil || h["harness_retry_at"] != nil {
		t.Fatalf("handle = %v, want the watch over", h)
	}
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run %d times, want 2", len(asks))
	}
	// When it works and then stops on the limit anew, that is told.
	r.show("working", " ● Going on.")
	must(t, d.Reconcile(ctx))
	p.say(kimiUsed(now.Add(time.Hour)))
	primeLimits(t, d, "kimi", "", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	if evs := eventKinds(t, s, b.ID, "session_harness_error"); len(evs) != 2 || evs[1].Data["tries"] != float64(1) {
		t.Fatalf("session_harness_error events = %v, want a second for the limit hit anew", evs)
	}
}

// What yardr does not prompt at the wake: a session with text in its input,
// which is someone's, and one whose pane no longer ends in the limit. The
// provider is not asked for either, and the wait is over.
func TestSessionNotPromptedAtTheReset(t *testing.T) {
	t.Parallel()
	for name, at := range map[string]func(r *retryRunner){
		"text in its input":    func(r *retryRunner) { r.mu.Lock(); r.input = "wait, I am looking"; r.mu.Unlock() },
		"the limit has left":   func(r *retryRunner) { r.show("idle", kimiIdle) },
		"another error stands": func(r *retryRunner) { r.show("idle", " Error: [auth.login_required] Please log in.\n"+kimiIdle) },
	} {
		t.Run(name, func(t *testing.T) {
			s, r, d, b, p, now := resetYard(t, nil)
			ctx := context.Background()
			p.say(kimiUsed(now.Add(42 * time.Minute)))
			primeLimits(t, d, "kimi", "", nil)
			r.show("idle", kimiLimit)
			must(t, d.Reconcile(ctx))
			at(r)
			p.say(kimiOpen)
			*now = now.Add(44 * time.Minute)
			must(t, d.Reconcile(ctx))
			must(t, d.Reconcile(ctx))
			*now = now.Add(time.Hour)
			must(t, d.Reconcile(ctx))
			if asks := p.asks(); len(r.nudges) != 0 || len(asks) != 1 {
				t.Fatalf("typed %q, the provider run %d times; want nothing typed and no ask at the wake", r.nudges, len(asks))
			}
			if h := handleOfBead(t, s, b); h["harness_reset_at"] != nil {
				t.Fatalf("handle = %v, want the wait over", h)
			}
			want := 1
			if name == "another error stands" {
				want = 2 // told as any error is
			}
			if evs := eventKinds(t, s, b.ID, "session_harness_error"); len(evs) != want {
				t.Fatalf("session_harness_error events = %d, want %d", len(evs), want)
			}
		})
	}
}

// A provider that gives no answer at the wake does not hold the prompt
// back: the time it gave has come.
func TestProviderFailingAtTheWakeDoesNotHoldThePrompt(t *testing.T) {
	t.Parallel()
	_, r, d, _, p, now := resetYard(t, nil)
	ctx := context.Background()
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	primeLimits(t, d, "kimi", "", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	p.fail()
	*now = now.Add(44 * time.Minute)
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	if asks := p.asks(); len(r.nudges) != 1 || len(asks) != 2 {
		t.Fatalf("typed %q, the provider run %d times; want the prompt and one ask at the wake", r.nudges, len(asks))
	}
}

// crewAtItsLimit is a kimi crew member with one mail, nudged for it, whose
// harness answered with its usage limit: the event of the stop is raised.
func crewAtItsLimit(t *testing.T, resetsIn time.Duration) (s store.API, f *crewRunner, d *dispatch.Dispatcher, p *standIn, now *time.Time, told func() []store.Event) {
	t.Helper()
	s, f, d = crewSetup(t, store.CrewMember{Config: map[string]any{"kind": "kimi"}})
	ctx := context.Background()
	at := noon()
	now = &at
	dispatch.SetClock(d, func() time.Time { return *now })
	p = newStandIn(t, s)
	p.say(kimiUsed(now.Add(resetsIn)))
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	f.show(kimiIdle)
	must(t, s.Mail(ctx, "yardmaster", "review yardr-1my8", "chris"))
	must(t, d.Reconcile(ctx)) // nudged
	primeLimits(t, d, "kimi", "", nil)
	f.show(kimiLimit)
	must(t, d.Reconcile(ctx))
	told = func() []store.Event {
		t.Helper()
		m, err := s.GetCrew(ctx, "yardmaster")
		must(t, err)
		return eventKinds(t, s, m.Journal, "session_harness_error")
	}
	if evs := told(); len(evs) != 1 || evs[0].Data["wakes_at"] != now.Add(resetsIn+time.Minute).UTC().Format(time.RFC3339) {
		t.Fatalf("session_harness_error = %v, want one with wakes_at a minute after the reset", evs)
	}
	return s, f, d, p, now, told
}

// brakeman, 6 Oct 2026: stopped at 23:20 with a review waiting, window reset
// at 00:02. It is prompted at 00:03, with its inbox's count, and goes on.
func TestCrewMemberAtItsUsageLimitIsPromptedWhenTheWindowHasReset(t *testing.T) {
	t.Parallel()
	s, f, d, p, now, told := crewAtItsLimit(t, 42*time.Minute)
	ctx := context.Background()
	agent := agentOf(t, s)
	for _, by := range []time.Duration{time.Minute, 30 * time.Minute, 42 * time.Minute} {
		*now = noon().Add(by)
		must(t, d.Reconcile(ctx))
	}
	if asks := p.asks(); len(f.nudges) != 1 || len(asks) != 1 {
		t.Fatalf("in the wait: nudges %q, the provider run %d times; want the inbox's nudge and the ask of the stop", f.nudges, len(asks))
	}

	// A cold wake defers for a crew member as for a session: nothing
	// more is typed and no retry is spent until the refresh lands.
	p.say(kimiOpen)
	*now = noon().Add(43*time.Minute + time.Second)
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 {
		t.Fatalf("at a cold wake: nudges = %q, want the defer to type nothing", f.nudges)
	}
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	want := agent + ": " + resetLine + " 1 new in your inbox: run yardr inbox to read them, and yardr inbox --ack <seq> through the last you handled."
	if len(f.nudges) != 2 || f.nudges[1] != want {
		t.Fatalf("nudges = %q, want the line at the reset with the inbox's count", f.nudges)
	}
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run %d times, want 2", len(asks))
	}
	m, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	tries := eventKinds(t, s, m.Journal, "session_harness_retry")
	if len(tries) != 1 || tries[0].Data["crew"] != "yardmaster" || tries[0].Data["try"] != float64(1) || tries[0].Data["window"] != "5h" {
		t.Fatalf("session_harness_retry = %v, want one for the crew member", tries)
	}

	// It works, reads its inbox and is idle again: one event in all, and
	// the watch is off its handle.
	f.set(agent, "working")
	*now = now.Add(3 * time.Minute)
	must(t, d.Reconcile(ctx))
	f.show(kimiIdle)
	f.set(agent, "idle")
	must(t, d.Reconcile(ctx))
	*now = now.Add(time.Hour)
	must(t, d.Reconcile(ctx))
	m, err = s.GetCrew(ctx, "yardmaster")
	must(t, err)
	for _, key := range []string{dispatch.HarnessErrorKey, "harness_reset_at", "harness_retry_at", "harness_prompted"} {
		if m.Handle[key] != nil {
			t.Fatalf("handle = %v; %s outlived the agent getting going", m.Handle, key)
		}
	}
	if n, asks := len(told()), p.asks(); n != 1 || len(asks) != 2 || len(f.nudges) != 2 {
		t.Fatalf("events = %d, asks = %d, nudges = %q; want one event, two asks, and no nudge for mail the line told of", n, len(asks), f.nudges)
	}
}

// Kimi's menu, for a crew member: the prompt does not show two minutes on,
// which is told once. Until it shows or the agent works, no nudge is typed
// into that pane: its Enter would choose the menu's first answer.
func TestCrewPromptBehindAHarnessQuestionHoldsItsNudges(t *testing.T) {
	t.Parallel()
	s, f, d, p, now, told := crewAtItsLimit(t, 42*time.Minute)
	ctx := context.Background()
	p.say(kimiOpen)
	*now = noon().Add(43*time.Minute + time.Second)
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 2 {
		t.Fatalf("nudges = %q, want the prompt at the reset", f.nudges)
	}
	f.show(kimiMenu)
	must(t, s.Mail(ctx, "yardmaster", "are you there?", "chris"))
	*now = now.Add(time.Minute)
	must(t, d.Reconcile(ctx))
	if n := len(told()); n != 1 || len(f.nudges) != 2 {
		t.Fatalf("a minute after the prompt: events = %d, nudges = %q; want nothing told or typed yet", n, f.nudges)
	}
	*now = now.Add(time.Minute + time.Second)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	evs := told()
	if len(evs) != 2 || evs[1].Data["prompt_shown"] != false || evs[1].Data["crew"] != "yardmaster" || evs[1].Data["tail"] != kimiMenu ||
		!strings.Contains(evs[1].Data["hint"].(string), "yardr crew restart yardmaster") {
		t.Fatalf("session_harness_error = %v, want a second, for the prompt that was not taken", evs)
	}
	*now = now.Add(30 * time.Minute)
	must(t, d.Reconcile(ctx))
	if n, asks := len(told()), p.asks(); n != 2 || len(f.nudges) != 2 || len(asks) != 2 {
		t.Fatalf("events = %d, nudges = %q, asks = %d; want it told once, nothing typed behind the menu, nothing asked", n, f.nudges, len(asks))
	}

	// A person answers the menu: the prompt shows, and the mail is nudged.
	f.mu.Lock()
	f.shown = true
	f.mu.Unlock()
	f.show(kimiIdle)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 3 || !strings.Contains(f.nudges[2], "new in your inbox") {
		t.Fatalf("nudges = %q, want the inbox's nudge once the prompt has shown", f.nudges)
	}
}

// A crew member's harness answers the prompt with the limit again: raised
// as any error is, and the old one above the prompt alone is not.
func TestCrewUsageLimitStandingAgainAfterThePromptIsRaised(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		pane func(prompt string) string
		want int
	}{
		"the error again below the prompt": {func(p string) string { return kimiLimit + "\n > " + p + "\n" + kimiLimit }, 2},
		"only the old error above it":      {func(p string) string { return kimiLimit + "\n > " + p + "\n ● Reading my inbox.\n" + kimiIdle }, 1},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, d, p, now, told := crewAtItsLimit(t, 42*time.Minute)
			ctx := context.Background()
			p.say(kimiOpen)
			*now = noon().Add(43*time.Minute + time.Second)
			primeLimits(t, d, "kimi", "", nil)
			must(t, d.Reconcile(ctx))
			f.mu.Lock()
			f.shown = true
			f.mu.Unlock()
			f.show(tc.pane(strings.SplitN(f.nudges[1], ": ", 2)[1]))
			*now = now.Add(2*time.Minute + time.Second)
			must(t, d.Reconcile(ctx))
			must(t, d.Reconcile(ctx))
			evs := told()
			if len(evs) != tc.want {
				t.Fatalf("session_harness_error events = %d, want %d", len(evs), tc.want)
			}
			if tc.want == 2 && (evs[1].Data["tries"] != float64(1) || evs[1].Data["reason"] != dispatch.ReasonRateLimited) {
				t.Fatalf("data = %v, want the limit again with tries 1", evs[1].Data)
			}
			m, err := s.GetCrew(ctx, "yardmaster")
			must(t, err)
			if m.Handle["harness_prompted"] != nil {
				t.Fatalf("handle = %v, want the watch over", m.Handle)
			}
		})
	}
}

// A crew member whose window is still used up at the wake is not prompted;
// the event is raised again with the later time.
func TestCrewWindowStillUsedUpAtTheWake(t *testing.T) {
	t.Parallel()
	_, f, d, p, now, told := crewAtItsLimit(t, 42*time.Minute)
	ctx := context.Background()
	later := noon().Add(3 * time.Hour)
	p.say(kimiUsed(later))
	*now = noon().Add(43*time.Minute + time.Second)
	primeLimits(t, d, "kimi", "", nil)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	evs := told()
	if len(f.nudges) != 1 || len(evs) != 2 || evs[1].Data["wakes_at"] != later.Add(time.Minute).UTC().Format(time.RFC3339) {
		t.Fatalf("nudges = %q, events = %v; want nothing typed and a second event with the later wake", f.nudges, evs)
	}
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run %d times, want 2", len(asks))
	}
}

// The provider is not leaned on: with agents yardr could prompt, a session
// and a crew member with mail unread, passes that find nothing stopped on a
// usage limit do not run its command, whatever else they find.
func TestPassWithNothingStoppedAsksNoLimitsOfAgentsYardrCanPrompt(t *testing.T) {
	t.Parallel()
	s, r, d, _, p, now := resetYard(t, nil)
	ctx := context.Background()
	f := &crewRunner{status: map[string]string{}}
	d.Runners["crew"] = f
	_, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "crew", Config: map[string]any{"kind": "kimi"}}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	must(t, s.Mail(ctx, "yardmaster", "review yardr-1my8", "chris"))
	f.show(kimiIdle)
	for _, pane := range []struct{ status, tail string }{
		{"working", " ● Reading the brief"},
		{"idle", kimiIdle},
		{"idle", " Error: [provider.overloaded] The engine is overloaded.\n" + kimiIdle},
		{"idle", kimiMenu[strings.Index(kimiMenu, " ───"):]},
		{"blocked", kimiIdle},
		{"working", " ● Going on"},
	} {
		r.show(pane.status, pane.tail)
		for range 3 {
			*now = now.Add(time.Minute)
			must(t, d.Reconcile(ctx))
		}
	}
	d2 := &dispatch.Dispatcher{Store: s, Runners: d.Runners, Backoff: time.Hour}
	must(t, d2.Reconcile(ctx))
	if asks := p.asks(); asks != nil {
		t.Fatalf("the limits command was run for %v by passes with nothing stopped on a usage limit", asks)
	}
}

// A wake whose read nothing can answer for does not defer: no source
// names the kind any more (or, for a session, its env_file is gone).
// There is no key, no command and nothing to refresh, so the agent is
// prompted at once, one retry is spent, no want is left registered and
// no command is run, however long the test waits. Through both
// harnessReset and crewReset, as the defer is.
func TestWakeNothingCanAnswerForPromptsAtOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	unanswer := func(t *testing.T, s store.API) {
		t.Helper()
		_, err := s.PutSource(ctx, "quokka.yaml",
			"name: quokka\nlimits:\n  command: [provider, \"{provider}\", --json]\n  kinds: { codex: openai }\n", "me")
		must(t, err)
	}
	t.Run("session", func(t *testing.T) {
		s, r, d, b, p, now := resetYard(t, nil)
		resets := now.Add(42 * time.Minute)
		p.say(kimiUsed(resets))
		primeLimits(t, d, "kimi", "", nil)
		r.show("idle", kimiLimit)
		must(t, d.Reconcile(ctx))
		unanswer(t, s)
		*now = resets.Add(time.Minute + time.Second)
		must(t, d.Reconcile(ctx))
		if len(r.nudges) != 1 || r.nudges[0] != resetLine {
			t.Fatalf("typed %q, want the prompt at once: nothing can answer for the kind", r.nudges)
		}
		if n := len(eventKinds(t, s, b.ID, "session_harness_retry")); n != 1 {
			t.Fatalf("session_harness_retry events = %d, want exactly one retry spent", n)
		}
		dispatch.WaitLimitsIdle(d)
		if wanted := dispatch.LimitsWanted(d); len(wanted) != 0 {
			t.Fatalf("wanted keys = %v, want none left by an unresolvable wake", wanted)
		}
		if asks := p.asks(); len(asks) != 1 {
			t.Fatalf("the provider was run %d times, want the stop's one: the wake ran nothing", len(asks))
		}
	})
	t.Run("crew", func(t *testing.T) {
		s, f, d, p, now, _ := crewAtItsLimit(t, 42*time.Minute)
		unanswer(t, s)
		*now = noon().Add(43*time.Minute + time.Second)
		must(t, d.Reconcile(ctx))
		if len(f.nudges) != 2 {
			t.Fatalf("nudges = %q, want the prompt at once: nothing can answer for the kind", f.nudges)
		}
		dispatch.WaitLimitsIdle(d)
		if wanted := dispatch.LimitsWanted(d); len(wanted) != 0 {
			t.Fatalf("wanted keys = %v, want none left by an unresolvable wake", wanted)
		}
		if asks := p.asks(); len(asks) != 1 {
			t.Fatalf("the provider was run %d times, want the stop's one: the wake ran nothing", len(asks))
		}
	})
	t.Run("session sources unreadable", func(t *testing.T) {
		s, r, d, b, p, now := resetYard(t, nil)
		resets := now.Add(42 * time.Minute)
		p.say(kimiUsed(resets))
		primeLimits(t, d, "kimi", "", nil)
		r.show("idle", kimiLimit)
		must(t, d.Reconcile(ctx))

		d.Store = sourcesUnavailable{API: s}
		*now = resets.Add(time.Minute + time.Second)
		must(t, d.Reconcile(ctx))
		if len(r.nudges) != 1 || r.nudges[0] != resetLine {
			t.Fatalf("typed %q, want the prompt at once: Store.Sources is unreadable", r.nudges)
		}
		if n := len(eventKinds(t, s, b.ID, "session_harness_retry")); n != 1 {
			t.Fatalf("session_harness_retry events = %d, want exactly one retry spent", n)
		}
		if asks := p.asks(); len(asks) != 1 {
			t.Fatalf("the provider was run for %v, want the stop's one run", asks)
		}
	})
	t.Run("crew sources unreadable", func(t *testing.T) {
		s, f, d, p, now, told := crewAtItsLimit(t, 42*time.Minute)
		d.Store = sourcesUnavailable{API: s}
		*now = noon().Add(43*time.Minute + time.Second)
		must(t, d.Reconcile(ctx))
		if len(f.nudges) != 2 || !strings.Contains(f.nudges[1], resetLine) {
			t.Fatalf("nudges = %q, want the prompt at once: Store.Sources is unreadable", f.nudges)
		}
		member, err := s.GetCrew(ctx, "yardmaster")
		must(t, err)
		if n := len(eventKinds(t, s, member.Journal, "session_harness_retry")); n != 1 {
			t.Fatalf("session_harness_retry events = %d, want exactly one retry spent", n)
		}
		if len(told()) != 1 {
			t.Fatalf("session_harness_error events = %v, want only the stop event", told())
		}
		if asks := p.asks(); len(asks) != 1 {
			t.Fatalf("the provider was run for %v, want the stop's one run", asks)
		}
	})
	t.Run("session env_file gone", func(t *testing.T) {
		s, r, d, b, p, now := resetYard(t, nil)
		login := loginEnv(t, "g-login")
		must(t, os.WriteFile(login, []byte("YARDR_TEST_LOGIN=g-login\n"), 0o600))
		must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"kind": "kimi", "env_file": login}}, "me"))
		resets := now.Add(42 * time.Minute)
		p.say(kimiUsed(resets))
		primeGroupLimits(t, s, d, "g")
		r.show("idle", kimiLimit)
		must(t, d.Reconcile(ctx))
		must(t, os.Remove(login))
		*now = resets.Add(time.Minute + time.Second)
		must(t, d.Reconcile(ctx))
		if len(r.nudges) != 1 {
			t.Fatalf("typed %q, want the prompt at once: the env_file cannot be read", r.nudges)
		}
		if n := len(eventKinds(t, s, b.ID, "session_harness_retry")); n != 1 {
			t.Fatalf("session_harness_retry events = %d, want exactly one retry spent", n)
		}
		dispatch.WaitLimitsIdle(d)
		if wanted := dispatch.LimitsWanted(d); len(wanted) != 0 {
			t.Fatalf("wanted keys = %v, want none left by an unresolvable wake", wanted)
		}
		if asks := p.asks(); len(asks) != 1 {
			t.Fatalf("the provider was run for %v, want the stop's one: the wake ran nothing", asks)
		}
	})
}

type sourcesUnavailable struct{ store.API }

func (sourcesUnavailable) Sources(context.Context) ([]store.Source, error) {
	return nil, errors.New("sources unavailable")
}

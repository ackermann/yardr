package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A codex pane as it stood on 6 Oct 2026, when a reviewer's harness stopped
// and sat for an hour looking like any idle agent.
const atCapacity = "• Ran yardr prime --bead yardr-x1hy\n" +
	"  └ yardr-x1hy  A session waiting on its own gate is not idle\n" +
	"■ Selected model is at capacity. Please try a different model.\n" +
	"› Ask Codex to do anything\n" +
	"  gpt-5.5 high · 71% context left"

// harnessSetup is a yard with one group of agents of kind, whose runner
// reports status and has a pane to read. The agent works.
func harnessSetup(t *testing.T, kind string, cfg map[string]any) (store.API, *agentRunner, *dispatch.Dispatcher, store.Bead) {
	t.Helper()
	s := storetest.New(t)
	r := &agentRunner{statusRunner: &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}, worked: true}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	conf := map[string]any{"kind": kind}
	for k, v := range cfg {
		conf[k] = v
	}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: conf}))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	must(t, err)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx)) // start
	must(t, d.Reconcile(ctx)) // seen working
	return s, r, d, *b
}

// show sets what the agent's pane ends in and what herdr says of it.
func (a *agentRunner) show(status, tail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.tail = status, tail
}

// A harness that stops on an error of its own is idle to herdr. The yard
// reads its pane on the first pass that finds it idle and says what it
// stopped on, long before the idle limit, once per idle spell.
func TestHarnessErrorIsReportedAtTheFirstIdlePass(t *testing.T) {
	s, r, d, b := harnessSetup(t, "codex", nil) // idle: the default, 30m
	ctx := context.Background()
	told := func() []store.Event { t.Helper(); return eventKinds(t, s, b.ID, "session_harness_error") }
	if n := len(told()); n != 0 {
		t.Fatalf("session_harness_error for a working agent: %d", n)
	}

	r.show("idle", atCapacity)
	must(t, d.Reconcile(ctx))
	evs := told()
	if len(evs) != 1 {
		t.Fatalf("session_harness_error events = %d, want 1 on the first idle pass", len(evs))
	}
	got, _ := s.GetBead(ctx, b.ID)
	data := evs[0].Data
	if data["reason"] != dispatch.ReasonAtCapacity || data["kind"] != "codex" || data["session"] != *got.Session ||
		data["group"] != "g" || data["stage"] != "new" ||
		data["line"] != "■ Selected model is at capacity. Please try a different model." ||
		data["tail"] != atCapacity || !strings.Contains(data["hint"].(string), "yardr bead release "+b.ID) {
		t.Fatalf("session_harness_error data = %v", data)
	}
	if n := len(eventKinds(t, s, b.ID, "session_idle")); n != 0 {
		t.Fatalf("session_idle before its limit: %d", n)
	}

	// Once per spell, and a daemon that replaced itself tells nobody again:
	// the mark is on the session's handle.
	must(t, d.Reconcile(ctx))
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	must(t, d2.Reconcile(ctx))
	must(t, d2.Reconcile(ctx))
	if n := len(told()); n != 1 {
		t.Fatalf("session_harness_error events = %d, want 1 per idle spell", n)
	}

	// Someone gets the agent going; when it stops again, on another error,
	// that is told.
	r.show("working", "• Working")
	must(t, d2.Reconcile(ctx))
	if sess, _ := s.GetSession(ctx, *got.Session); sess.Handle[dispatch.HarnessErrorKey] != nil {
		t.Fatalf("handle = %v; the mark outlived the idle spell", sess.Handle)
	}
	r.show("idle", "■ You've hit your usage limit. Upgrade to Plus to continue using Codex.")
	must(t, d2.Reconcile(ctx))
	if evs = told(); len(evs) != 2 || evs[1].Data["reason"] != dispatch.ReasonRateLimited {
		t.Fatalf("session_harness_error events = %v, want a second one, rate_limited", evs)
	}
}

// What is not a harness's error raises nothing: an agent that only idles,
// one that wrote about an error, an error line of another harness, a kind
// nothing is known of. And a group with idle off still hears of an error.
func TestHarnessErrorOnlyForAKnownLineOfTheKind(t *testing.T) {
	for name, tc := range map[string]struct {
		kind string
		cfg  map[string]any
		tail string
		want string
	}{
		"only idle":             {kind: "codex", tail: "• Committed the change. Waiting for review.\n› Ask Codex to do anything"},
		"writes about an error": {kind: "codex", tail: "• The reviewer saw \"Selected model is at capacity\" last night.\n› Ask Codex to do anything"},
		"another harness's":     {kind: "claude", tail: atCapacity},
		"a kind not known":      {kind: "pi", tail: atCapacity},
		"idle off":              {kind: "codex", cfg: map[string]any{"idle": "off"}, tail: atCapacity, want: dispatch.ReasonAtCapacity},
		"claude, the default kind": {kind: "", tail: "⏺ API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n" +
			"────────\n❯ \n────────\n  ⏵⏵ auto mode on", want: dispatch.ReasonAtCapacity},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, d, b := harnessSetup(t, tc.kind, tc.cfg)
			r.show("idle", tc.tail)
			must(t, d.Reconcile(context.Background()))
			must(t, d.Reconcile(context.Background()))
			evs := eventKinds(t, s, b.ID, "session_harness_error")
			if tc.want == "" && len(evs) != 0 {
				t.Fatalf("session_harness_error = %v, want none", evs[0].Data)
			}
			if tc.want != "" && (len(evs) != 1 || evs[0].Data["reason"] != tc.want) {
				t.Fatalf("session_harness_error = %v, want one with reason %s", evs, tc.want)
			}
		})
	}
}

// session_idle carries the end of the agent's pane, as session_stalled
// does: its reader sees why the agent sits there without reading the pane.
// A harness's error first seen then is told as well.
func TestIdleSessionCarriesItsPane(t *testing.T) {
	s, r, d, b := harnessSetup(t, "codex", map[string]any{"idle": "30ms"})
	ctx := context.Background()
	const waits = "• Done. The gate passed; I wait for the reviewer's answer.\n› Ask Codex to do anything"
	r.show("idle", waits)
	must(t, d.Reconcile(ctx))
	time.Sleep(40 * time.Millisecond)
	// The error comes onto the screen late, as the idle limit passes.
	r.show("idle", atCapacity)
	must(t, d.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_idle")
	if len(evs) != 1 || evs[0].Data["tail"] != atCapacity {
		t.Fatalf("session_idle = %v, want one with the pane's last lines in tail", evs)
	}
	if n := len(eventKinds(t, s, b.ID, "session_harness_error")); n != 1 {
		t.Fatalf("session_harness_error events = %d, want 1 from the pane read for session_idle", n)
	}
}

// A runner with no pane to read gives a session_idle without one.
func TestIdleSessionWithoutAPane(t *testing.T) {
	s := storetest.New(t)
	r := &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "idle"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"idle": "30ms", "stall": "off"}}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	time.Sleep(40 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_idle")
	if len(evs) != 1 {
		t.Fatalf("session_idle events = %d, want 1", len(evs))
	}
	if _, has := evs[0].Data["tail"]; has {
		t.Fatalf("session_idle data = %v; a tail from a runner that has no pane", evs[0].Data)
	}
}

// The lines each harness is known to stop on, as it prints them, and lines
// that only look like one.
func TestHarnessErrorLines(t *testing.T) {
	for _, tc := range []struct{ kind, line, want string }{
		{"codex", "■ Selected model is at capacity. Please try a different model.", dispatch.ReasonAtCapacity},
		{"codex", "■ You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits", dispatch.ReasonRateLimited},
		{"codex", "■ exceeded retry limit, last status: 429 Too Many Requests", dispatch.ReasonRateLimited},
		{"codex", "■ Your access token could not be refreshed. Please log out and sign in again.", dispatch.ReasonNotLoggedIn},
		{"codex", "  Sign in with ChatGPT", dispatch.ReasonNotLoggedIn},
		{"codex", "■ stream disconnected before completion: Incomplete response returned", dispatch.ReasonOther},
		{"codex", "■ unexpected status 503 Service Unavailable", dispatch.ReasonOther},
		{"codex", "• I added a test for the retry limit.", ""},
		{"claude", "Not logged in · Run /login", dispatch.ReasonNotLoggedIn},
		{"claude", "  ⎿  Invalid API key · Please run /login", dispatch.ReasonNotLoggedIn},
		{"claude", "⏺ API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"OAuth token has expired.\"}} · Please run /login", dispatch.ReasonNotLoggedIn},
		{"claude", "  ⎿  You've hit your limit · resets 3pm (Europe/Berlin)", dispatch.ReasonRateLimited},
		{"claude", "  ⎿  5-hour limit reached ∙ resets 3pm", dispatch.ReasonRateLimited},
		{"claude", "⏺ API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\"}}", dispatch.ReasonRateLimited},
		{"claude", "⏺ API Error: Repeated 529 Overloaded errors", dispatch.ReasonAtCapacity},
		{"claude", "⏺ " + modelError, dispatch.ReasonOther},
		{"claude", "  ⎿  Credit balance is too low", dispatch.ReasonOther},
		{"claude", "⏺ API Error: 500 {\"type\":\"error\",\"error\":{\"type\":\"api_error\"}}", dispatch.ReasonOther},
		{"claude", "⏺ The session limit reached its end; see the API Error: 529 above.", ""},
		{"claude", "❯ Why did it say Not logged in?", ""},
	} {
		reason, line, ok := dispatch.HarnessError(tc.kind, "⏺ Reading the brief.\n"+tc.line+"\n\n❯ \n")
		if reason != tc.want || ok != (tc.want != "") || (ok && line != strings.TrimSpace(tc.line)) {
			t.Errorf("%s: %q: reason %q line %q, want %q", tc.kind, tc.line, reason, line, tc.want)
		}
	}
	// Only the pane's end counts: what the agent quoted while it still
	// worked is further up.
	old := "■ Selected model is at capacity. Please try a different model.\n" + strings.Repeat("• Edited a file\n", 12)
	if reason, _, ok := dispatch.HarnessError("codex", old); ok {
		t.Errorf("an error line 13 lines up was taken for what the harness stopped on: %s", reason)
	}
}

// retryRunner is an agentRunner whose agents can be told their first prompt
// again: it knows the prompt, and keeps what was typed.
type retryRunner struct {
	*agentRunner
	nudges []string
	input  string // what is typed in the agent's input and not sent
}

func (n *retryRunner) FirstPrompt(map[string]any) string { return "Read brief.md and follow it." }

func (n *retryRunner) Nudge(_ context.Context, _ map[string]any, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nudges = append(n.nudges, text)
	return nil
}

func (n *retryRunner) InputText(context.Context, map[string]any) (string, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.input, true, nil
}

// retrySetup is a yard with one codex session whose agent answered its
// first prompt with tail and sits idle, having done nothing, and a clock
// the test moves: at(by) puts the dispatcher by after the start.
func retrySetup(t *testing.T, tail string) (s store.API, r *retryRunner, d *dispatch.Dispatcher, b store.Bead, at func(time.Duration)) {
	t.Helper()
	s = storetest.New(t)
	r = &retryRunner{agentRunner: &agentRunner{statusRunner: &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}},
		status: "idle"}, tail: tail}}
	d = &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	var skew time.Duration
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(skew) })
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"kind": "codex"}}))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
	must(t, err)
	bead, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx)) // start
	must(t, d.Reconcile(ctx)) // seen idle, its pane read
	return s, r, d, *bead, func(by time.Duration) { skew = by }
}

// The 6 Oct case (yardr-f7aw): a harness that answers its first prompt with
// "at capacity" is given the prompt again by yardr, five minutes on. The
// agent gets going, and the yardmaster hears nothing: no
// session_harness_error, and no session_stalled for the wait either.
func TestAtCapacityIsPromptedAgain(t *testing.T) {
	s, r, d, b, at := retrySetup(t, atCapacity)
	ctx := context.Background()
	tries := func() []store.Event { t.Helper(); return eventKinds(t, s, b.ID, "session_harness_retry") }
	quiet := func(when string) {
		t.Helper()
		for _, kind := range []string{"session_harness_error", "session_stalled"} {
			if evs := eventKinds(t, s, b.ID, kind); len(evs) != 0 {
				t.Fatalf("%s: %s = %v, want none", when, kind, evs[0].Data)
			}
		}
	}

	// The wait is not over: nothing is typed.
	at(4 * time.Minute)
	must(t, d.Reconcile(ctx))
	if len(tries()) != 0 || len(r.nudges) != 0 {
		t.Fatalf("after 4m: %d tries, typed %q; the wait is 5m", len(tries()), r.nudges)
	}
	quiet("in the wait")

	at(5*time.Minute + time.Second)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	evs := tries()
	if len(evs) != 1 || len(r.nudges) != 1 || r.nudges[0] != "Read brief.md and follow it." {
		t.Fatalf("after 5m: tries = %v, typed %q; want the first prompt given again once", evs, r.nudges)
	}
	got, _ := s.GetBead(ctx, b.ID)
	if data := evs[0].Data; data["try"] != float64(1) || data["of"] != float64(dispatch.HarnessRetries) ||
		data["reason"] != dispatch.ReasonAtCapacity || data["session"] != *got.Session || data["kind"] != "codex" ||
		data["line"] != "■ Selected model is at capacity. Please try a different model." {
		t.Fatalf("session_harness_retry data = %v", data)
	}

	// It works this time, and what it then stops for is its own business.
	r.show("working", "• Reading the brief")
	must(t, d.Reconcile(ctx))
	r.show("idle", "• Done. Reported the outcome.\n› Ask Codex to do anything")
	at(30 * time.Minute)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(tries()) != 1 || len(r.nudges) != 1 {
		t.Fatalf("an agent that got going was prompted again: tries %d, typed %q", len(tries()), r.nudges)
	}
	quiet("after the try worked")
}

// The model stays at capacity: three tries, five minutes apart, each an
// event; then session_harness_error once, saying how often yardr tried. A
// daemon that replaces itself on the way neither starts the count again nor
// forgets the wait.
func TestAtCapacityIsRaisedWhenTheTriesAreUsedUp(t *testing.T) {
	s, r, d, b, at := retrySetup(t, atCapacity)
	ctx := context.Background()
	count := func(kind string) int { t.Helper(); return len(eventKinds(t, s, b.ID, kind)) }

	at(5*time.Minute + time.Second)
	must(t, d.Reconcile(ctx))
	if count("session_harness_retry") != 1 {
		t.Fatalf("tries after 5m = %d, want 1", count("session_harness_retry"))
	}

	var skew time.Duration
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	dispatch.SetClock(d2, func() time.Time { return time.Now().Add(skew) })
	for i, min := range []int{6, 10, 11, 15, 16} {
		skew = time.Duration(min)*time.Minute + 2*time.Second
		must(t, d2.Reconcile(ctx))
		must(t, d2.Reconcile(ctx))
		if want := []int{1, 2, 2, 3, 3}[i]; count("session_harness_retry") != want || len(r.nudges) != want {
			t.Fatalf("after %dm: %d tries, typed %d times; want %d", min, count("session_harness_retry"), len(r.nudges), want)
		}
		if n := count("session_harness_error"); n != 0 {
			t.Fatalf("after %dm: session_harness_error raised with a try still to make or to wait for", min)
		}
	}

	skew = 20*time.Minute + 3*time.Second
	must(t, d2.Reconcile(ctx))
	must(t, d2.Reconcile(ctx))
	skew = 40 * time.Minute
	must(t, d2.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 {
		t.Fatalf("session_harness_error events = %d, want 1 once the tries are used up", len(evs))
	}
	data := evs[0].Data
	if data["reason"] != dispatch.ReasonAtCapacity || data["tries"] != float64(dispatch.HarnessRetries) ||
		data["tail"] != atCapacity || !strings.Contains(data["hint"].(string), "first prompt again 3 times") ||
		!strings.Contains(data["hint"].(string), "yardr bead release "+b.ID) {
		t.Fatalf("session_harness_error data = %v", data)
	}
	if count("session_harness_retry") != dispatch.HarnessRetries || len(r.nudges) != dispatch.HarnessRetries {
		t.Fatalf("%d tries, typed %d times; want %d", count("session_harness_retry"), len(r.nudges), dispatch.HarnessRetries)
	}
}

// Only at_capacity on a first prompt is retried. Another reason, a session
// that has done something (it stopped mid-work, where the first prompt is
// not what it needs), and one with text in its input are raised at once, as
// they always were, and nothing is typed however long they sit.
func TestHarnessErrorThatIsNotPromptedAgain(t *testing.T) {
	const limited = "■ You've hit your usage limit. Upgrade to Plus to continue using Codex.\n› Ask Codex to do anything"
	for name, tc := range map[string]struct {
		tail   string
		worked bool
		input  string
		want   string
	}{
		"another reason": {tail: limited, want: dispatch.ReasonRateLimited},
		"mid-work":       {tail: atCapacity, worked: true, want: dispatch.ReasonAtCapacity},
		"text in input":  {tail: atCapacity, input: "wait, try the other", want: dispatch.ReasonAtCapacity},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, d, b, at := retrySetup(t, "› Ask Codex to do anything")
			ctx := context.Background()
			r.show("working", "• Reading the brief")
			must(t, d.Reconcile(ctx))
			r.mu.Lock()
			r.worked, r.input = tc.worked, tc.input
			r.mu.Unlock()
			r.show("idle", tc.tail)
			must(t, d.Reconcile(ctx))
			evs := eventKinds(t, s, b.ID, "session_harness_error")
			if len(evs) != 1 || evs[0].Data["reason"] != tc.want {
				t.Fatalf("session_harness_error = %v, want one at once with reason %s", evs, tc.want)
			}
			if _, has := evs[0].Data["tries"]; has {
				t.Fatalf("session_harness_error data = %v; tries of a session that was never prompted again", evs[0].Data)
			}
			at(6 * time.Minute)
			must(t, d.Reconcile(ctx))
			at(12 * time.Minute)
			must(t, d.Reconcile(ctx))
			if n := len(eventKinds(t, s, b.ID, "session_harness_retry")); n != 0 || len(r.nudges) != 0 {
				t.Fatalf("%d tries, typed %q; want none", n, r.nudges)
			}
			if n := len(eventKinds(t, s, b.ID, "session_harness_error")); n != 1 {
				t.Fatalf("session_harness_error events = %d, want 1", n)
			}
		})
	}
}

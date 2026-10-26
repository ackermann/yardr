package dispatch_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A codex pane as it stood on 6 Oct 2026, when a reviewer's harness stopped
// and sat for an hour looking like any idle agent.
const atCapacity = "• Ran yardr prime --bead yardr-x1hy\n" +
	"  └ yardr-x1hy  A session waiting on its own tests is not idle\n" +
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
	t.Cleanup(func() { dispatch.WaitLimitsIdle(d) })
	conf := map[string]any{"kind": kind}
	for k, v := range cfg {
		conf[k] = v
	}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: conf}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	s, r, d, b := harnessSetup(t, "codex", map[string]any{"idle": "30ms"})
	ctx := context.Background()
	const waits = "• Done. The tests passed; I wait for the reviewer's answer.\n› Ask Codex to do anything"
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

// The pane of a builder that waits for a command it started in the
// background, as Claude Code 2.1.288 drew it (yardr-6m7z, 13 Oct 2026), and
// the same pane with the shell gone.
const (
	shellRuns = "⏺ The measurement is running in the background; I'll carry on with the per-test tables when it finishes.\n" +
		"✻ Brewed for 48s · done 12:44 AM · 1 shell still running\n" +
		"                                             Update available! Run: brew upgrade claude-code@latest\n" +
		"────────────────────────────────────────\n❯ \n────────────────────────────────────────\n" +
		"  ⏵⏵ auto mode on · 1 shell · ← 1 agent · ↓ to manage"
	shellGone = "⏺ The measurement said 1 shell still running when I looked; what do you want me to do with it?\n" +
		"✻ Brewed for 48s · done 12:44 AM\n" +
		"────────────────────────────────────────\n❯ \n────────────────────────────────────────\n" +
		"  ⏵⏵ auto mode on · ← 1 agent · ↓ to manage"
)

// An agent that waits for a shell of its own is idle to herdr, and its
// harness wakes it when the shell exits (yardr-zjhw: a builder whose depot
// test ran longer than its group's idle was told of every five minutes).
// While its pane says so, as its kind's file declares, no session_idle is
// raised and a grace does not end the session. Once the pane no longer says
// so, the limit applies as to any idle agent.
func TestIdleSessionWithBackgroundWorkIsNotIdle(t *testing.T) {
	t.Parallel()
	s, r, d, b := harnessSetup(t, "claude", map[string]any{"idle": "30ms", "grace": "0", "stall": "off"})
	ctx := context.Background()
	told := func() int { t.Helper(); return len(eventKinds(t, s, b.ID, "session_idle")) }
	r.show("idle", shellRuns)
	must(t, d.Reconcile(ctx))
	for range 3 {
		time.Sleep(40 * time.Millisecond)
		must(t, d.Reconcile(ctx))
	}
	if n := told(); n != 0 {
		t.Fatalf("session_idle for an agent that waits for its background shell: %d", n)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Session == nil {
		t.Fatal("session stopped at its grace while its background shell ran")
	}

	// The shell is gone and the agent still sits there: idle from the last
	// look that saw the shell, told of at the limit and stopped at the grace.
	r.show("idle", shellGone)
	must(t, d.Reconcile(ctx))
	time.Sleep(40 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	if n := told(); n != 1 {
		t.Fatalf("session_idle events = %d, want 1 once the pane shows no background work", n)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Session != nil {
		t.Fatal("session still runs past its grace with no background work")
	}
}

// What a pane says of background work counts for the kind that declares
// it, in what its harness draws around its input: a kind with no such lines
// is told of at the limit whatever its pane shows, and so is an agent
// further up whose pane the words stand, in what it wrote.
func TestBackgroundWorkIsItsKindsToDeclare(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ kind, pane string }{
		"a kind that declares none": {"codex", shellRuns},
		"no background work":        {"claude", shellGone},
		"the agent's own words": {"claude", "⏺ Earlier: done 12:44 AM · 1 shell still running\n" +
			"⏺ one\n⏺ two\n⏺ three\n⏺ four\n⏺ five\n⏺ six\n❯ "},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, d, b := harnessSetup(t, c.kind, map[string]any{"idle": "30ms", "stall": "off"})
			ctx := context.Background()
			r.show("idle", c.pane)
			must(t, d.Reconcile(ctx))
			time.Sleep(40 * time.Millisecond)
			must(t, d.Reconcile(ctx))
			evs := eventKinds(t, s, b.ID, "session_idle")
			if len(evs) != 1 || evs[0].Data["tail"] != c.pane {
				t.Fatalf("session_idle = %v, want one at the limit with the pane in tail", evs)
			}
		})
	}
	if line, ok := dispatch.HarnessBackground("claude", shellRuns); !ok || line != "⏵⏵ auto mode on · 1 shell · ← 1 agent · ↓ to manage" {
		t.Fatalf("background work in the pane = %q, %v; want the status line", line, ok)
	}
}

// tallyRunner is an agentRunner whose agents also take nudges, as herdr's
// do, keeping the nudges and counting the pane reads.
type tallyRunner struct {
	*agentRunner
	mu     sync.Mutex
	nudges []string
	tails  int
}

func (n *tallyRunner) Nudge(_ context.Context, _ map[string]any, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.nudges = append(n.nudges, text)
	return nil
}

func (n *tallyRunner) Tail(ctx context.Context, h map[string]any, lines int) (string, error) {
	n.mu.Lock()
	n.tails++
	n.mu.Unlock()
	return n.agentRunner.Tail(ctx, h, lines)
}

func (n *tallyRunner) told() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string{}, n.nudges...)
}

func (n *tallyRunner) reads() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.tails
}

// The pane of a builder that waits for a command it started in the
// background, as Muse Code 1.4.9 drew it (read live from pane w114:p2, 20
// Oct 2026), and the same pane with the command gone. museRuns1411 is the
// same wait as 1.4.11 draws it, with ◈ for ◆ (read live from pane
// w18H:p2, 21 Oct 2026).
const (
	museRuns = "◆ Still blocked on the gate run — no result yet.\n" +
		"── Voice input (alt(option)+v to start) ──…\n" +
		"❯ \n" +
		"───…\n" +
		"main · ↓ to select\n" +
		"└ ◆ Run depot test gate in worktree  running          27m 03s\n" +
		"  muse-spark-1.3 · max · /tmp/work · Auto-review"
	museRuns1411 = "◆ Still blocked on the gate run — no result yet.\n" +
		"── Voice input (alt(option)+v to start) ──…\n" +
		"❯ \n" +
		"───…\n" +
		"main · ↓ to select\n" +
		"└ ◈ Run depot test gate in worktree  running          27m 03s\n" +
		"  muse-spark-1.3 · max · /tmp/work · Auto-review"
	museGone = "◆ The gate passed; handing on now.\n" +
		"── Voice input (alt(option)+v to start) ──…\n" +
		"❯ \n" +
		"───…\n" +
		"  muse-spark-1.3 · max · /tmp/work · Auto-review"
)

// An agent that waits for a shell of its own is not nudged to hand on
// (yardr-kh12: each answer ended the agent's turn, which started a new
// idle spell, so every pass nudged again). Once the pane no longer says
// so, the nudge goes as ever, once; and a pass with nothing to send reads
// no pane.
func TestIdleSessionWithBackgroundWorkIsNotNudged(t *testing.T) {
	t.Parallel()
	s, r, d, b := harnessSetup(t, "muse", map[string]any{"idle": "1h", "stall": "off"})
	n := &tallyRunner{agentRunner: r}
	d.Runners["fake"] = n
	ctx := context.Background()
	sess := liveSession(t, s, b.ID)
	must(t, s.SetHandle(ctx, sess, map[string]any{"session": sess, "agent": "yard-x-new"}))
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	nudged := func() int { t.Helper(); return len(eventKinds(t, s, b.ID, "session_idle_nudge")) }

	r.show("idle", museRuns)
	must(t, d.Reconcile(ctx))
	// The agent answers the silence, as it answered each nudge: a new
	// spell, still waiting on its shell.
	r.show("working", "◆ Working")
	must(t, d.Reconcile(ctx))
	r.show("idle", museRuns)
	must(t, d.Reconcile(ctx))
	// The same wait as 1.4.11 draws it: a new spell, still no nudge.
	r.show("working", "◆ Working")
	must(t, d.Reconcile(ctx))
	r.show("idle", museRuns1411)
	must(t, d.Reconcile(ctx))
	if got := n.told(); len(got) != 0 || nudged() != 0 {
		t.Fatalf("nudges = %q, session_idle_nudge events = %d; want none while a background shell runs", got, nudged())
	}

	// The shell is gone and the agent still sits there: the nudge goes,
	// once, as today.
	r.show("idle", museGone)
	must(t, d.Reconcile(ctx))
	if got := n.told(); len(got) != 1 || !strings.Contains(got[0], "write your note and hand on") {
		t.Fatalf("nudges = %q, want the handover nudge once the pane shows no background work", got)
	}
	if n := nudged(); n != 1 {
		t.Fatalf("session_idle_nudge events = %d, want 1", n)
	}

	// Past the spell's young looks, a pass with nothing to send reads no
	// pane: the nudge's own read went only when it was due.
	now = now.Add(3 * time.Minute)
	reads := n.reads()
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if got := n.reads(); got != reads {
		t.Fatalf("pane reads = %d, want still %d on passes with nothing to send", got, reads)
	}
	if got := n.told(); len(got) != 1 {
		t.Fatalf("nudges = %q, want still the one", got)
	}
}

// A runner with no pane to read gives a session_idle without one.
func TestIdleSessionWithoutAPane(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	r := &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "idle"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"idle": "30ms", "stall": "off"}}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
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
	t.Parallel()
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
		{"muse", "model failed: server_error: The model failed to generate a response. (request id: 688dfffa-1833-4175-96d6-ae6513c47149, response: resp_6ad2e591f2d29e424980460b)", dispatch.ReasonOther},
		{"muse", "◆ model failed: server_error: The model failed to generate a response. (request id: 688dfffa-1833-4175-96d6-ae6513c47149, response: resp_6ad2e591f2d29e424980460b)", dispatch.ReasonOther},
		{"muse", "model failed: stream protocol error: function_call_arguments.done name mismatch for `fc_0192a3f4` (request id: 9a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d)", dispatch.ReasonOther},
		{"muse", "◆ model failed: stream protocol error: function_call_arguments.done name mismatch for `fc_0192a3f4` (request id: 9a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d)", dispatch.ReasonOther},
		{"muse", "◆ The pane said model failed: server_error last night.", ""},
		{"muse", "❯ Why did it say model failed: server_error?", ""},
		{"kimi", "   Error: [provider.auth_error] 403 You've reached your", dispatch.ReasonRateLimited},
		{"kimi", "   Error: [provider.auth_error] 403 You've reached your 5-hour usage limit. Your quota will reset when the current 5-hour window ends.", dispatch.ReasonRateLimited},
		{"kimi", "   Error: [provider.rate_limit] 429 Too many requests", dispatch.ReasonRateLimited},
		{"kimi", "   Error: [provider.overloaded] 529 The engine is", dispatch.ReasonAtCapacity},
		{"kimi", "   Error: [provider.auth_error] 401 Invalid", dispatch.ReasonNotLoggedIn},
		{"kimi", "   Error: [auth.login_required] Login required", dispatch.ReasonNotLoggedIn},
		{"kimi", "   Error: [provider.api_error] 500 Internal error", ""},
		{"kimi", " ● The pane said Error: [provider.auth_error] 403 You've reached your limit.", ""},
		{"kimi", " 5-hour usage limit. Your quota will reset when the current", ""},
		{"claude", "   Error: [provider.auth_error] 403 You've reached your", ""},
		{"claude", "❯ Why did it say Not logged in?", ""},
	} {
		reason, line, ok := dispatch.HarnessError(tc.kind, "⏺ Reading the brief.\n"+tc.line+"\n\n❯ \n")
		if reason != tc.want || ok != (tc.want != "") || (ok && line != strings.TrimSpace(tc.line)) {
			t.Errorf("%s: %q: reason %q line %q, want %q", tc.kind, tc.line, reason, line, tc.want)
		}
	}
	// Only the pane's end counts: what the agent quoted while it still
	// worked is further up.
	old := "■ Selected model is at capacity. Please try a different model.\n" + strings.Repeat("• Edited a file\n", 20)
	if reason, _, ok := dispatch.HarnessError("codex", old); ok {
		t.Errorf("an error line 21 lines up was taken for what the harness stopped on: %s", reason)
	}
	// Kimi's, broken by Kimi into lines of the pane's width, begins 12
	// lines from the end of a pane of 60 columns, and further up in a
	// narrower one.
	if reason, line, ok := dispatch.HarnessError("kimi", kimiLimit); reason != dispatch.ReasonRateLimited || !ok ||
		line != "Error: [provider.auth_error] 403 You've reached your" {
		t.Errorf("kimi's usage limit: reason %q line %q", reason, line)
	}
	narrow := strings.Replace(kimiLimit, " or upgrade your plan:\n", " or upgrade\n your plan:\n", 1)
	narrow = strings.Replace(narrow, " file with us for diagnosis.", " file with us\n for diagnosis.", 1)
	if reason, _, _ := dispatch.HarnessError("kimi", narrow); reason != dispatch.ReasonRateLimited {
		t.Errorf("kimi's usage limit 14 lines up: reason %q", reason)
	}
}

// The pane of the crew member brakeman (kimi, 60 columns) as herdr read it
// on 6 Oct 2026: its harness had stopped on its usage limit half an hour
// before, with a review waiting, and nobody had heard (yardr-r9sh). Kimi
// broke the error into these lines itself.
const kimiLimit = " ● Used Read (…dr/internal/store/yardrsource.go) · 130 lines\n" +
	"   Error: [provider.auth_error] 403 You've reached your\n" +
	" 5-hour usage limit. Your quota will reset when the current\n" +
	" 5-hour window ends. To continue now, purchase extra usage\n" +
	" or upgrade your plan:\n" +
	" https://www.kimi.com/membership/subscription?tab=quota\n" +
	"   If this persists, run `/export-debug-zip` and share the\n" +
	" file with us for diagnosis. Please don't share it publicly.\n" +
	" ╭──────────────────────────────────────────────────────────╮\n" +
	" │ >                                                        │\n" +
	" ╰──────────────────────────────────────────────────────────╯\n" +
	" Ask When Needed  K3 thinking: high  ~         ctrl+o expand\n" +
	"                                      context: 20% (198k/1M)"

// The pane of a builder (kimi, 60 columns) on 11 Oct 2026, which stopped on
// its usage limit mid-work and of which nobody heard for three and a half
// hours (yardr-re0h): Kimi draws the list of what the agent still meant to
// do under the error, above its input. The line was not what was missed:
// herdr called the agent working, so its pane was never read (see the herdr
// runner's stopped).
const kimiLimitTodo = "       Error: [provider.auth_error] 403 You've reached your\n" +
	"     5-hour usage limit. Your quota will reset when the current\n" +
	"     5-hour window ends. To continue now, purchase extra usage or\n" +
	"     upgrade your plan:\n" +
	"     https://www.kimi.com/membership/subscription?tab=quota\n" +
	"       If this persists, run `/export-debug-zip` and share the\n" +
	"     file with us for diagnosis. Please don't share it publicly.\n" +
	"\n" +
	"     ────────────────────────────────────────────────────────────\n" +
	"       Todo\n" +
	"       ✓ internal/source: runner + command files (command.go)\n" +
	"       ● declaration.go: thread/link keys + refusals\n" +
	"       ○ limits: use source runner, resolve program\n" +
	"       … +3 more (1 done · 2 pending) · ctrl+t to expand\n" +
	"     ╭──────────────────────────────────────────────────────────╮\n" +
	"     │ >                                                        │\n" +
	"     ╰──────────────────────────────────────────────────────────╯\n"

func TestKimiUsageLimitAboveItsTodoPanelIsFound(t *testing.T) {
	t.Parallel()
	if reason, line, ok := dispatch.HarnessError("kimi", kimiLimitTodo); reason != dispatch.ReasonRateLimited || !ok ||
		line != "Error: [provider.auth_error] 403 You've reached your" {
		t.Errorf("kimi's usage limit above its Todo panel: reason %q line %q", reason, line)
	}
}

// kimiIdle is such a pane with nothing wrong in it.
const kimiIdle = " ● The review is noted on the bead.\n" +
	" ╭──────────────────────────────────────────────────────────╮\n" +
	" │ >                                                        │\n" +
	" ╰──────────────────────────────────────────────────────────╯\n" +
	" Ask When Needed  K3 thinking: high  ~         ctrl+o expand"

// A bead's session of kind kimi that stops on its usage limit is told of as
// claude's and codex's are.
func TestKimiSessionAtItsUsageLimitIsReported(t *testing.T) {
	t.Parallel()
	s, r, d, b := harnessSetup(t, "kimi", nil)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(context.Background()))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 || evs[0].Data["reason"] != dispatch.ReasonRateLimited || evs[0].Data["kind"] != "kimi" ||
		evs[0].Data["line"] != "Error: [provider.auth_error] 403 You've reached your" {
		t.Fatalf("session_harness_error = %v, want one, rate_limited, with kimi's line", evs)
	}
}

// The pane of a builder (muse) on 17 Oct 2026, which stopped on a failed
// turn and of which nobody heard for four hours (yardr-thet): the model
// answered nothing, with a request id to find it by. Muse's own message
// begins with ◆, which the match takes off with the other error marks.
const museServerError = "◆ model failed: server_error: The model failed to generate a response. (request id:\n" +
	" 688dfffa-1833-4175-96d6-ae6513c47149, response: resp_6ad2e591f2d29e424980460b)\n" +
	"── Voice input (alt(option)+v to start) ──…\n" +
	"❯ \n" +
	"───…\n" +
	"    muse-spark-1.3 · max · /private/tmp/lyr0/work · Auto-review"

// A bead's session of kind muse that stops on a failed turn is told of as
// claude's, codex's and kimi's are.
func TestMuseSessionOnAFailedTurnIsReported(t *testing.T) {
	t.Parallel()
	s, r, d, b := harnessSetup(t, "muse", nil)
	r.show("idle", museServerError)
	must(t, d.Reconcile(context.Background()))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 || evs[0].Data["reason"] != dispatch.ReasonOther || evs[0].Data["kind"] != "muse" ||
		evs[0].Data["line"] != "◆ model failed: server_error: The model failed to generate a response. (request id:" {
		t.Fatalf("session_harness_error = %v, want one, other, with muse's line", evs)
	}
}

// A crew member has no idle limit, so one whose harness stopped on an error
// was never told of. Its pane is read when it is idle with its inbox
// unread, and the error raises session_harness_error on its journal, with
// the crew member named, to whoever hears that kind: once while it stands.
func TestCrewMemberAtItsUsageLimitIsReported(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{Config: map[string]any{"kind": "kimi"}})
	ctx := context.Background()
	// Who hears the kind; its runner is none of the dispatcher's, so it is
	// an inbox and no session.
	_, err := s.PutCrew(ctx, store.CrewMember{Name: "ears", Runner: "none", On: []string{"session_harness_error"}}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	agent := agentOf(t, s)
	member := func() *store.CrewMember {
		t.Helper()
		r, err := s.GetCrew(ctx, "yardmaster")
		must(t, err)
		return r
	}
	told := func() []store.Event { t.Helper(); return eventKinds(t, s, member().Journal, "session_harness_error") }
	pass := func(want int, when string) {
		t.Helper()
		must(t, d.Reconcile(ctx))
		if evs := told(); len(evs) != want {
			t.Fatalf("%s: session_harness_error events = %d, want %d: %v", when, len(evs), want, evs)
		}
	}

	// Idle with nothing in its inbox is what a crew member mostly is: its
	// pane is not asked, whatever it ends in.
	f.show(kimiIdle)
	pass(0, "idle, an empty inbox, no error")
	f.show(kimiLimit)
	pass(0, "idle, an empty inbox")
	// Idle with mail and no error is nudged, and that is all.
	f.show(kimiIdle)
	must(t, s.Mail(ctx, "yardmaster", "review yardr-1my8", "chris"))
	pass(0, "idle, mail, no error")
	if len(f.nudges) != 1 {
		t.Fatalf("nudges = %v, want the inbox's", f.nudges)
	}

	// The nudge is answered with the usage limit.
	f.show(kimiLimit)
	pass(1, "idle, mail, the usage limit")
	data := told()[0].Data
	if data["crew"] != "yardmaster" || data["kind"] != "kimi" || data["reason"] != dispatch.ReasonRateLimited ||
		data["line"] != "Error: [provider.auth_error] 403 You've reached your" || data["tail"] != kimiLimit ||
		!strings.Contains(data["hint"].(string), "yardr crew restart yardmaster") {
		t.Fatalf("session_harness_error data = %v", data)
	}
	if member().Handle[dispatch.HarnessErrorKey] != dispatch.ReasonRateLimited {
		t.Fatalf("handle = %v, want the reason kept on it", member().Handle)
	}
	// It reaches who hears the kind, as a session's does.
	page, err := s.Inbox(ctx, "ears", 0)
	must(t, err)
	if len(page.Events) != 1 || page.Events[0].Kind != "session_harness_error" {
		t.Fatalf("inbox of who hears session_harness_error = %v, want the event", page.Events)
	}
	// Each new item in its inbox is a nudge, which a stopped harness
	// answers by working for a moment and stopping on the same error: that
	// is not told again.
	f.set(agent, "working")
	pass(1, "working for a moment")
	f.set(agent, "idle")
	pass(1, "stopped on the same error")
	must(t, s.Mail(ctx, "yardmaster", "are you there?", "chris"))
	pass(1, "more mail")
	pass(1, "more mail, a pass later")
	if len(f.nudges) != 2 {
		t.Fatalf("nudges = %v, want one for each mail", f.nudges)
	}

	// A daemon that replaced itself reads the mark and does not tell twice.
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}, PromptSettle: -1, PromptWait: -1}
	must(t, d2.Reconcile(ctx))
	if n := len(told()); n != 1 {
		t.Fatalf("session_harness_error events = %d after a new daemon's pass, want 1", n)
	}

	// A pane seen without the error ends it: the next one is told.
	f.show(kimiIdle)
	pass(1, "got going again")
	if mark := member().Handle[dispatch.HarnessErrorKey]; mark != nil {
		t.Fatalf("handle keeps %v with the error gone from the pane", mark)
	}
	f.show(kimiLimit)
	pass(2, "the usage limit again")
	// So does an inbox that was handled.
	mine, err := s.Inbox(ctx, "yardmaster", 0)
	must(t, err)
	must(t, s.AckInbox(ctx, "yardmaster", mine.Last))
	pass(2, "the inbox handled")
	if mark := member().Handle[dispatch.HarnessErrorKey]; mark != nil {
		t.Fatalf("handle keeps %v with the inbox handled", mark)
	}
}

// The pane is read at the start of a spell and after a nudge, not for as
// long as a crew member leaves its inbox unread: an error drawn into it
// under a nudge long after is still seen.
func TestCrewMemberPaneIsReadAgainAfterANudge(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{Config: map[string]any{"kind": "kimi"}})
	ctx := context.Background()
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	told := func() int { t.Helper(); return len(eventKinds(t, s, r.Journal, "session_harness_error")) }

	f.show(kimiIdle)
	must(t, s.Mail(ctx, "yardmaster", "one", "chris"))
	must(t, d.Reconcile(ctx)) // nudged, and it leaves the mail unread
	now = now.Add(time.Hour)
	f.show(kimiLimit)
	must(t, d.Reconcile(ctx))
	if n := told(); n != 0 {
		t.Fatalf("session_harness_error events = %d; the pane of a crew member idle for an hour was read", n)
	}
	f.show(kimiIdle)
	must(t, s.Mail(ctx, "yardmaster", "two", "chris"))
	must(t, d.Reconcile(ctx)) // nudged again
	f.show(kimiLimit)
	now = now.Add(time.Minute)
	must(t, d.Reconcile(ctx))
	if n := told(); n != 1 {
		t.Fatalf("session_harness_error events = %d, want 1 for the error under the nudge", n)
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
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"kind": "codex"}}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

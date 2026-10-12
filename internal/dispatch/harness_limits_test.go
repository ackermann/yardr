package dispatch_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// standIn is a provider of limits in place of the operator's: a script the
// yard's source "quokka" names for the kinds kimi, codex and pi. It answers
// with what say last set, or exits 1 as a provider does after fail, and
// asks tells for whom it was run, one name per run.
type standIn struct {
	t   *testing.T
	dir string
}

func newStandIn(t *testing.T, s store.API) *standIn {
	t.Helper()
	p := &standIn{t: t, dir: t.TempDir()}
	script := filepath.Join(p.dir, "provider")
	must(t, os.WriteFile(script, []byte("#!/bin/sh\ncd "+p.dir+"\necho \"$1\" >>asked\n"+
		"if [ -f fail ]; then echo 'quokka: kimi: not logged in' >&2; exit 1; fi\ncat answer\n"), 0o700))
	_, err := s.PutSource(context.Background(), "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+
		", \"{provider}\", --json]\n  kinds: { kimi: moonshot, codex: openai, pi: zai }\n", "me")
	must(t, err)
	p.say(`{"provider":"Kimi","windows":[]}`)
	return p
}

func (p *standIn) say(report string) {
	p.t.Helper()
	must(p.t, os.WriteFile(filepath.Join(p.dir, "answer"), []byte(report), 0o600))
}

func (p *standIn) fail() {
	p.t.Helper()
	must(p.t, os.WriteFile(filepath.Join(p.dir, "fail"), nil, 0o600))
}

func (p *standIn) asks() []string {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join(p.dir, "asked"))
	if os.IsNotExist(err) {
		return nil
	}
	must(p.t, err)
	return strings.Fields(string(b))
}

// noon is a time of day at which 42 minutes on is the same day, wherever
// the test runs: tomorrow, 12:00 local.
func noon() time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d+1, 12, 0, 0, 0, time.Local)
}

// kimiUsed is a report as aiquokka gave it on 6 Oct 2026, its 5h window
// used up and resetting at five, the weekly one not.
func kimiUsed(five time.Time) string {
	return fmt.Sprintf(`{"provider":"Kimi","windows":[`+
		`{"label":"5h","used_percent":100,"resets_at":%q,"used":100,"limit":100},`+
		`{"label":"Weekly","used_percent":38,"resets_at":%q,"used":38,"limit":100}]}`,
		five.Format(time.RFC3339), five.Add(80*time.Hour).Format(time.RFC3339))
}

// A session whose harness stopped on a usage limit is told of with the
// window that is used up and when it resets, as the yard's source says it:
// the yardmaster reads the time and does not guess it.
func TestSessionAtItsUsageLimitSaysTheWindowAndItsReset(t *testing.T) {
	s, r, d, b := harnessSetup(t, "kimi", nil)
	now := noon()
	dispatch.SetClock(d, func() time.Time { return now })
	p := newStandIn(t, s)
	resets := now.Add(42 * time.Minute)
	p.say(kimiUsed(resets))

	r.show("idle", kimiLimit)
	must(t, d.Reconcile(context.Background()))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 {
		t.Fatalf("session_harness_error events = %v, want one", evs)
	}
	data := evs[0].Data
	if data["reason"] != dispatch.ReasonRateLimited || data["window"] != "5h" ||
		data["resets_at"] != resets.UTC().Format(time.RFC3339) || data["limits_unknown"] != nil {
		t.Fatalf("data = %v, want rate_limited with the 5h window and its reset", data)
	}
	report, _ := data["limits"].(map[string]any)
	if report["source"] != "quokka" || report["provider"] != "Kimi" || len(report["windows"].([]any)) != 2 {
		t.Fatalf("limits = %v, want the report of quokka with both windows", data["limits"])
	}
	hint, _ := data["hint"].(string)
	if !strings.HasPrefix(hint, "kimi's 5h window is used up and resets at 12:42 (in 42m); the agent's harness stopped") ||
		!strings.Contains(hint, "yardr bead release "+b.ID) {
		t.Fatalf("hint = %q", hint)
	}
	if asks := p.asks(); len(asks) != 1 || asks[0] != "moonshot" {
		t.Fatalf("the provider was run for %v, want once, by its own name for kimi", asks)
	}
	// It stands: the passes after ask nothing.
	must(t, d.Reconcile(context.Background()))
	now = now.Add(5 * time.Minute)
	must(t, d.Reconcile(context.Background()))
	if asks := p.asks(); len(asks) != 1 {
		t.Fatalf("the provider was run %d times for one error, want 1", len(asks))
	}
}

// A session of a group with the provider setting is asked as the provider,
// even where no source's kinds table lists its kind: the event carries the
// report with the window and its reset, and the hint keeps the kind in its
// words.
func TestSessionWithProviderSettingIsAskedAsTheProvider(t *testing.T) {
	s, r, d, b := harnessSetup(t, "claude", map[string]any{"provider": "zai"})
	now := noon()
	dispatch.SetClock(d, func() time.Time { return now })
	p := newStandIn(t, s)
	resets := now.Add(42 * time.Minute)
	p.say(kimiUsed(resets))

	r.show("idle", "  ⎿  You've hit your limit · resets 3pm (Europe/Berlin)")
	must(t, d.Reconcile(context.Background()))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 {
		t.Fatalf("session_harness_error events = %v, want one", evs)
	}
	data := evs[0].Data
	if data["reason"] != dispatch.ReasonRateLimited || data["window"] != "5h" ||
		data["resets_at"] != resets.UTC().Format(time.RFC3339) || data["limits_unknown"] != nil {
		t.Fatalf("data = %v, want rate_limited with the 5h window and its reset", data)
	}
	report, _ := data["limits"].(map[string]any)
	if report["source"] != "quokka" || len(report["windows"].([]any)) != 2 {
		t.Fatalf("limits = %v, want the report of quokka with both windows", data["limits"])
	}
	hint, _ := data["hint"].(string)
	if !strings.HasPrefix(hint, "claude's 5h window is used up and resets at 12:42 (in 42m); the agent's harness stopped") {
		t.Fatalf("hint = %q", hint)
	}
	if asks := p.asks(); len(asks) != 1 || asks[0] != "zai" {
		t.Fatalf("the provider was run for %v, want once, for the setting", asks)
	}
}

// A session of a group whose provider no source's kinds name is told of as
// before, with the reason naming the provider.
func TestSessionWithUnansweredProviderNamesIt(t *testing.T) {
	s, r, d, b := harnessSetup(t, "kimi", map[string]any{"provider": "unanswered"})
	dispatch.SetClock(d, func() time.Time { return noon() })
	newStandIn(t, s)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(context.Background()))
	evs := eventKinds(t, s, b.ID, "session_harness_error")
	if len(evs) != 1 {
		t.Fatalf("session_harness_error events = %v, want one", evs)
	}
	data := evs[0].Data
	unknown, _ := data["limits_unknown"].(string)
	if !strings.Contains(unknown, "no source of this yard answers for provider unanswered") ||
		data["limits"] != nil || data["resets_at"] != nil {
		t.Fatalf("limits_unknown = %q, limits = %v, resets_at = %v; want only the reason naming the provider",
			unknown, data["limits"], data["resets_at"])
	}
	if data["reason"] != dispatch.ReasonRateLimited || data["line"] == nil || data["tail"] != kimiLimit {
		t.Fatalf("data = %v, want the event as it was", data)
	}
}

// A crew member's event carries the same.
func TestCrewMemberAtItsUsageLimitSaysTheWindowAndItsReset(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{Config: map[string]any{"kind": "kimi"}})
	ctx := context.Background()
	now := noon()
	dispatch.SetClock(d, func() time.Time { return now })
	p := newStandIn(t, s)
	resets := now.Add(42 * time.Minute)
	p.say(kimiUsed(resets))
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	f.show(kimiIdle)
	must(t, s.Mail(ctx, "yardmaster", "review yardr-1my8", "chris"))
	must(t, d.Reconcile(ctx)) // nudged
	if asks := p.asks(); asks != nil {
		t.Fatalf("the provider was run for %v with nothing stopped", asks)
	}
	f.show(kimiLimit)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))

	m, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	evs := eventKinds(t, s, m.Journal, "session_harness_error")
	if len(evs) != 1 {
		t.Fatalf("session_harness_error events = %v, want one", evs)
	}
	data := evs[0].Data
	report, _ := data["limits"].(map[string]any)
	if data["crew"] != "yardmaster" || data["window"] != "5h" || data["resets_at"] != resets.UTC().Format(time.RFC3339) ||
		report["source"] != "quokka" {
		t.Fatalf("data = %v, want the crew member, the 5h window, its reset and the report", data)
	}
	hint, _ := data["hint"].(string)
	if !strings.HasPrefix(hint, "kimi's 5h window is used up and resets at 12:42 (in 42m); yardr asks again at 12:43 and prompts the agent when the window is open; the crew member's harness stopped") ||
		!strings.Contains(hint, "yardr crew restart yardmaster") {
		t.Fatalf("hint = %q", hint)
	}
	if asks := p.asks(); len(asks) != 1 {
		t.Fatalf("the provider was run %d times, want 1", len(asks))
	}
}

// A crew member with the provider setting is asked as the provider.
func TestCrewMemberWithProviderSettingIsAskedAsTheProvider(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{Config: map[string]any{"kind": "kimi", "provider": "zai"}})
	ctx := context.Background()
	now := noon()
	dispatch.SetClock(d, func() time.Time { return now })
	p := newStandIn(t, s)
	resets := now.Add(42 * time.Minute)
	p.say(kimiUsed(resets))
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	f.show(kimiIdle)
	must(t, s.Mail(ctx, "yardmaster", "review yardr-1my8", "chris"))
	must(t, d.Reconcile(ctx)) // nudged
	f.show(kimiLimit)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))

	m, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	evs := eventKinds(t, s, m.Journal, "session_harness_error")
	if len(evs) != 1 {
		t.Fatalf("session_harness_error events = %v, want one", evs)
	}
	data := evs[0].Data
	report, _ := data["limits"].(map[string]any)
	if data["crew"] != "yardmaster" || data["window"] != "5h" || data["resets_at"] != resets.UTC().Format(time.RFC3339) ||
		report["source"] != "quokka" {
		t.Fatalf("data = %v, want the crew member, the 5h window, its reset and the report", data)
	}
	if asks := p.asks(); len(asks) != 1 || asks[0] != "zai" {
		t.Fatalf("the provider was run for %v, want once, for the setting", asks)
	}
}

// What the event says when the provider's answer names no time to wait for.
func TestUsageLimitWithNoResetToRead(t *testing.T) {
	later := noon().Add(3 * time.Hour).Format(time.RFC3339)
	for _, c := range []struct {
		name, report string
		fail, none   bool
		window       any
		hint         string
		unknown      string
	}{
		{name: "no window used up",
			report: `{"provider":"Kimi","windows":[{"label":"5h","used_percent":61,"resets_at":"` + later + `"}]}`,
			hint:   "no window of kimi's subscription is used up, says quokka: the limit is another account's, one model's or a short rate limit; the agent's"},
		{name: "used up and no reset said",
			report: `{"provider":"Kimi","windows":[{"label":"5h","used":100,"limit":100,"resets_at":"0001-01-01T00:00:00Z"}]}`,
			window: "5h", hint: "kimi's 5h window is used up, and quokka does not say when it resets; the agent's"},
		{name: "the window that resets last",
			report: `{"provider":"Kimi","windows":[{"label":"5h","used_percent":100,"resets_at":"` + later +
				`"},{"label":"Weekly","used_percent":100,"resets_at":"` + noon().Add(27*time.Hour).Format(time.RFC3339) + `"}]}`,
			window: "Weekly", hint: "kimi's Weekly window is used up and resets at " + noon().Add(27*time.Hour).Format("Mon 2 Jan 15:04") + " (in 1d 3h); the agent's"},
		{name: "a provider that fails", fail: true, hint: "the agent's",
			unknown: "quokka: kimi: not logged in"},
		{name: "a provider that prints no report", report: "Kimi: 5h 100%", hint: "the agent's",
			unknown: "not the JSON object the yard reads"},
		{name: "no source", none: true, hint: "the agent's",
			unknown: "no source of this yard says the limits of kimi"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r, d, b := harnessSetup(t, "kimi", nil)
			now := noon()
			dispatch.SetClock(d, func() time.Time { return now })
			if !c.none {
				p := newStandIn(t, s)
				p.say(c.report)
				if c.fail {
					p.fail()
				}
			}
			r.show("idle", kimiLimit)
			must(t, d.Reconcile(context.Background()))
			evs := eventKinds(t, s, b.ID, "session_harness_error")
			if len(evs) != 1 {
				t.Fatalf("session_harness_error events = %v, want one", evs)
			}
			data := evs[0].Data
			hint, _ := data["hint"].(string)
			if !strings.HasPrefix(hint, c.hint) {
				t.Errorf("hint = %q, want it to begin %q", hint, c.hint)
			}
			if data["window"] != c.window {
				t.Errorf("window = %v, want %v", data["window"], c.window)
			}
			unknown, _ := data["limits_unknown"].(string)
			if c.unknown == "" {
				if unknown != "" || data["limits"] == nil {
					t.Errorf("limits = %v, limits_unknown = %q, want the report and no reason", data["limits"], unknown)
				}
				if c.window == nil && data["resets_at"] != nil {
					t.Errorf("resets_at = %v with no window used up", data["resets_at"])
				}
				return
			}
			// As it was before the yard knew of limits, and why.
			if !strings.Contains(unknown, c.unknown) || data["limits"] != nil || data["resets_at"] != nil {
				t.Errorf("limits_unknown = %q, limits = %v, resets_at = %v; want only the reason, with %q", unknown, data["limits"], data["resets_at"], c.unknown)
			}
			if data["reason"] != dispatch.ReasonRateLimited || data["line"] == nil || data["tail"] != kimiLimit {
				t.Errorf("data = %v, want the event as it was", data)
			}
		})
	}
}

// limitsYard is a yard whose group runs n sessions of kind kimi, all seen
// working, with a stand-in provider and a clock the test moves. cfg joins
// the group's config.
func limitsYard(t *testing.T, n int, cfg map[string]any) (s store.API, r *agentRunner, d *dispatch.Dispatcher, p *standIn, now *time.Time) {
	t.Helper()
	s = storetest.New(t)
	ctx := context.Background()
	r = &agentRunner{statusRunner: &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}, worked: true}
	d = &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	at := noon()
	now = &at
	dispatch.SetClock(d, func() time.Time { return *now })
	p = newStandIn(t, s)
	conf := map[string]any{"kind": "kimi"}
	for k, v := range cfg {
		conf[k] = v
	}
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: n, Config: conf}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	must(t, err)
	for i := range n {
		_, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("bead ", i)}, "me")
		must(t, err)
	}
	must(t, d.Reconcile(ctx)) // all start
	must(t, d.Reconcile(ctx)) // seen working
	if got := len(r.starts()); got != n {
		t.Fatalf("starts = %d, want %d", got, n)
	}
	return s, r, d, p, now
}

// harnessErrors counts the session_harness_error events of the yard.
func harnessErrors(t *testing.T, s store.API) int {
	t.Helper()
	beads, err := s.ListBeads(context.Background(), store.BeadFilter{})
	must(t, err)
	n := 0
	for _, b := range beads {
		n += len(eventKinds(t, s, b.ID, "session_harness_error"))
	}
	return n
}

// A provider is slow and is not leaned on: five sessions of one kind that
// stop on the limit within a minute are one run of its command, each with
// the window in its event. What it said is not kept longer than that
// minute, and a daemon that has just started knows nothing.
func TestSessionsStoppingTogetherAreOneAsk(t *testing.T) {
	s, r, d, p, now := limitsYard(t, 5, nil)
	ctx := context.Background()
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	*now = now.Add(20 * time.Second)
	must(t, d.Reconcile(ctx))
	if n := harnessErrors(t, s); n != 5 {
		t.Fatalf("session_harness_error events = %d, want 5", n)
	}
	beads, err := s.ListBeads(ctx, store.BeadFilter{})
	must(t, err)
	for _, b := range beads {
		if ev := eventKinds(t, s, b.ID, "session_harness_error")[0]; ev.Data["window"] != "5h" {
			t.Fatalf("%s: data = %v, want the 5h window from the one answer", b.ID, ev.Data)
		}
	}
	if asks := p.asks(); len(asks) != 1 {
		t.Fatalf("the provider was run %d times for five sessions that stopped together, want 1", len(asks))
	}

	// They get going and stop again within the minute: still the one answer.
	r.show("working", "")
	must(t, d.Reconcile(ctx))
	*now = now.Add(30 * time.Second)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	if n, asks := harnessErrors(t, s), p.asks(); n != 10 || len(asks) != 1 {
		t.Fatalf("events = %d, asks = %d; want 10 events from 1 ask within the minute", n, len(asks))
	}
	// And after it, one more.
	r.show("working", "")
	must(t, d.Reconcile(ctx))
	*now = now.Add(time.Minute)
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	if n, asks := harnessErrors(t, s), p.asks(); n != 15 || len(asks) != 2 {
		t.Fatalf("events = %d, asks = %d; want 15 events and a second ask after the minute", n, len(asks))
	}
}

// A provider that fails is not run once per session either.
func TestAFailingProviderIsAskedOnceForSessionsStoppingTogether(t *testing.T) {
	s, r, d, p, _ := limitsYard(t, 5, nil)
	p.fail()
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(context.Background()))
	if n, asks := harnessErrors(t, s), p.asks(); n != 5 || len(asks) != 1 {
		t.Fatalf("events = %d, asks = %d; want 5 events and 1 ask", n, len(asks))
	}
}

// Two sessions of a group with the provider setting that stop together are
// one run of the command, asked as the provider.
func TestProviderSessionsStoppingTogetherAreOneAsk(t *testing.T) {
	s, r, d, p, now := limitsYard(t, 2, map[string]any{"provider": "zai"})
	ctx := context.Background()
	p.say(kimiUsed(now.Add(42 * time.Minute)))
	r.show("idle", kimiLimit)
	must(t, d.Reconcile(ctx))
	if n := harnessErrors(t, s); n != 2 {
		t.Fatalf("session_harness_error events = %d, want 2", n)
	}
	beads, err := s.ListBeads(ctx, store.BeadFilter{})
	must(t, err)
	for _, b := range beads {
		if ev := eventKinds(t, s, b.ID, "session_harness_error")[0]; ev.Data["window"] != "5h" {
			t.Fatalf("%s: data = %v, want the 5h window from the one answer", b.ID, ev.Data)
		}
	}
	if asks := p.asks(); len(asks) != 1 || asks[0] != "zai" {
		t.Fatalf("the provider was run for %v, want once, for the setting", asks)
	}
}

// Only a usage limit asks: a harness that stopped on anything else is told
// of as before, and the provider is not run for it.
func TestHarnessErrorThatIsNoUsageLimitAsksNoLimits(t *testing.T) {
	for _, tail := range []string{
		atCapacity,
		"■ Your access token could not be refreshed because your refresh token was revoked.\n› Ask Codex to do anything",
		"■ Quota exceeded. Check your plan and billing details.\n› Ask Codex to do anything",
	} {
		s, r, d, b := harnessSetup(t, "codex", nil)
		p := newStandIn(t, s)
		r.show("idle", tail)
		must(t, d.Reconcile(context.Background()))
		must(t, d.Reconcile(context.Background()))
		evs := eventKinds(t, s, b.ID, "session_harness_error")
		if len(evs) != 1 || evs[0].Data["reason"] == dispatch.ReasonRateLimited {
			t.Fatalf("session_harness_error = %v, want one that is no usage limit", evs)
		}
		if evs[0].Data["limits"] != nil || evs[0].Data["limits_unknown"] != nil {
			t.Errorf("data = %v, want nothing of limits", evs[0].Data)
		}
		if asks := p.asks(); asks != nil {
			t.Errorf("the provider was run for %v, on reason %v", asks, evs[0].Data["reason"])
		}
	}
}

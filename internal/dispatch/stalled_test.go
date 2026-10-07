package dispatch_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// stalledSetup is a running yardmaster with a PromptSettle of three seconds,
// one mail unread, and a clock the test moves.
func stalledSetup(t *testing.T) (s store.API, f *crewRunner, d *dispatch.Dispatcher, r *store.CrewMember, later func(time.Duration)) {
	t.Helper()
	s, f, d = crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	d.PromptSettle = 3 * time.Second
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	must(t, s.Mail(ctx, "yardmaster", "deploy freeze", "chris"))
	return s, f, d, r, func(by time.Duration) {
		t.Helper()
		now = now.Add(by)
		must(t, d.Reconcile(ctx))
	}
}

// A nudge whose runner waited for the agent to start on it and saw nothing
// (herdr's agent_prompt_stalled, yardr-s1em) is looked for at once: in the
// input, it is yardr's own text whose Enter was not taken, and gets a
// second Enter on the pass that typed it, with session_prompt_typed saying
// so, not one PromptSettle and a pass later. It is typed once all the same.
func TestStalledNudgeLeftInTheInputGetsEnterOnTheSamePass(t *testing.T) {
	s, f, _, r, later := stalledSetup(t)
	f.stalls = true
	later(0)
	n, ev := kindCount(t, s, r.Journal, "session_prompt_typed")
	if len(f.nudges) != 1 || f.submits != 1 || n != 1 || f.input != "" {
		t.Fatalf("on the pass that typed it: nudges %q, Enter %d times, session_prompt_typed %d times, input %q",
			f.nudges, f.submits, n, f.input)
	}
	if d := ev.Data; d["crew"] != "yardmaster" || d["text"] != "a nudge" || d["submit"] != float64(1) {
		t.Fatalf("session_prompt_typed = %+v", d)
	}
	for range 3 {
		later(time.Minute)
	}
	if len(f.nudges) != 1 || f.submits != 1 {
		t.Fatalf("after it was sent: nudges %q, Enter %d times", f.nudges, f.submits)
	}
}

// Stalled does not say the text was not taken: an agent that took it and
// was not seen working has an empty input, and no key follows.
func TestStalledNudgeTheAgentTookGetsNoEnter(t *testing.T) {
	s, f, _, r, later := stalledSetup(t)
	f.stalls, f.took = true, true
	later(0)
	later(time.Minute)
	if n, _ := kindCount(t, s, r.Journal, "session_prompt_typed"); len(f.nudges) != 1 || f.submits != 0 || n != 0 {
		t.Fatalf("nudges %q, Enter %d times, session_prompt_typed %d times", f.nudges, f.submits, n)
	}
}

// The Enter of the pass that typed it is the first of the PromptTries the
// safety net sends, PromptSettle apart, before it reports the agent.
func TestStalledNudgeWhoseEnterNeverTakesIsGivenUp(t *testing.T) {
	s, f, _, r, later := stalledSetup(t)
	f.stalls, f.deaf = true, true
	later(0)
	if f.submits != 1 {
		t.Fatalf("Enter sent %d times on the pass that typed the nudge", f.submits)
	}
	later(time.Second)
	if f.submits != 1 {
		t.Fatalf("Enter sent again %d times before PromptSettle had passed", f.submits-1)
	}
	for range 2 * dispatch.PromptTries {
		later(4 * time.Second)
	}
	typed, _ := kindCount(t, s, r.Journal, "session_prompt_typed")
	gaveUp, _ := kindCount(t, s, r.Journal, "session_prompt_gave_up")
	if f.submits != dispatch.PromptTries || typed != dispatch.PromptTries || gaveUp != 1 || len(f.nudges) != 1 {
		t.Fatalf("Enter sent %d times, session_prompt_typed %d, session_prompt_gave_up %d, nudges %q",
			f.submits, typed, gaveUp, f.nudges)
	}
}

// An agent at a dialog is typed nothing (herdr's agent_blocked): the nudge's
// Enter would answer the dialog. The nudge is held and still due, one
// nudge_held says so for as long as it is, and it is typed once the dialog
// is gone; a dialog later is said again.
func TestNudgeForAnAgentAtADialogIsHeldAndSaidOnce(t *testing.T) {
	s, f, _, r, later := stalledSetup(t)
	f.atDialog = true
	for range 3 {
		later(time.Minute)
	}
	n, ev := kindCount(t, s, r.Journal, "nudge_held")
	if len(f.nudges) != 0 || f.submits != 0 || n != 1 {
		t.Fatalf("at a dialog: nudges %q, Enter %d times, nudge_held %d times, want none, none and one", f.nudges, f.submits, n)
	}
	if ev.Data["crew"] != "yardmaster" || !strings.Contains(fmt.Sprint(ev.Data["reason"]), "dialog") {
		t.Fatalf("nudge_held = %+v", ev.Data)
	}
	f.atDialog = false
	later(time.Minute)
	later(time.Minute)
	if len(f.nudges) != 1 || !strings.Contains(f.nudges[0], "1 new in your inbox") {
		t.Fatalf("after the dialog: nudges %q, want the one that was due", f.nudges)
	}
	f.atDialog = true
	must(t, s.Mail(context.Background(), "yardmaster", "second", "chris"))
	later(time.Minute)
	later(time.Minute)
	if n, _ := kindCount(t, s, r.Journal, "nudge_held"); n != 2 || len(f.nudges) != 1 {
		t.Fatalf("at a dialog again: nudge_held %d times, nudges %q", n, f.nudges)
	}
}

// The dispatcher's delivery of a pending first prompt goes the same way.
// One the agent did not start on and that is in its input is submitted on
// the pass that delivered it; one for an agent at a dialog is not typed,
// not counted as a delivery and not called lost.
func TestStalledDeliveryOfAPendingPromptIsSubmittedOnTheSamePass(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.blocked = true
	must(t, d.Reconcile(ctx))
	agent := agentOf(t, s)
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)

	f.set(agent, "idle")
	f.atDialog = true
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	r2, _ := s.GetCrew(ctx, "yardmaster")
	if lost, _ := kindCount(t, s, r.Journal, "session_prompt_lost"); len(f.nudges) != 0 || lost != 0 ||
		r2.Handle[dispatch.PendingPrompt] == nil || r2.Handle["prompt_sends"] != nil {
		t.Fatalf("at a dialog: nudges %q, session_prompt_lost %d times, handle %v", f.nudges, lost, r2.Handle)
	}

	f.atDialog, f.stalls = false, true
	must(t, d.Reconcile(ctx))
	typed, ev := kindCount(t, s, r.Journal, "session_prompt_typed")
	if len(f.nudges) != 1 || f.submits != 1 || typed != 1 || ev.Data["submit"] != float64(1) {
		t.Fatalf("on the pass that delivered it: nudges %q, Enter %d times, session_prompt_typed %d times (%+v)",
			f.nudges, f.submits, typed, ev.Data)
	}
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	prompted, ev := kindCount(t, s, r.Journal, "session_prompted")
	r2, _ = s.GetCrew(ctx, "yardmaster")
	if prompted != 1 || ev.Data["deliveries"] != float64(1) || ev.Data["submits"] != float64(1) ||
		r2.Handle[dispatch.PendingPrompt] != nil || len(f.nudges) != 1 || f.submits != 1 {
		t.Fatalf("after its Enter: session_prompted %d times (%+v), handle %v, nudges %q, Enter %d times",
			prompted, ev.Data, r2.Handle, f.nudges, f.submits)
	}
}

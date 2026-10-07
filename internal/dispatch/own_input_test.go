package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// ownRunner is a retryRunner whose agents take Enter: Start leaves nothing
// pending, as a runner does that saw the first prompt arrive, and the
// input is what the test puts there.
type ownRunner struct {
	*retryRunner
	submits  int  // times Enter was sent
	deaf     bool // Enter does not submit what is in the input
	noInput  bool // the harness's input cannot be told
	unmarked bool // the harness is of a kind whose input mark is not known
	blind    bool // the pane cannot be read
}

func (o *ownRunner) InputText(context.Context, map[string]any) (string, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.blind {
		return "", false, errors.New("pane cannot be read")
	}
	return o.input, !o.noInput, nil
}

func (o *ownRunner) InputMarked(map[string]any) bool { return !o.unmarked }

func (o *ownRunner) PromptTyped(context.Context, map[string]any, string) (bool, error) {
	return false, nil
}

func (o *ownRunner) SubmitPrompt(context.Context, map[string]any) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.submits++
	if !o.deaf {
		o.input = ""
	}
	return nil
}

// ownSetup is a yard with one session whose agent is idle, nothing pending
// on its handle, PromptSettle of three seconds and a clock the test moves
// with pass, which makes one pass by later.
func ownSetup(t *testing.T) (s store.API, r *ownRunner, d *dispatch.Dispatcher, b store.Bead, pass func(by time.Duration)) {
	t.Helper()
	s = storetest.New(t)
	r = &ownRunner{retryRunner: &retryRunner{agentRunner: &agentRunner{statusRunner: &statusRunner{
		fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "idle"}}}}
	d = &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, PromptSettle: 3 * time.Second}
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	must(t, err)
	bead, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx)) // start
	if h := handleOf(t, s, bead.ID); h[dispatch.PendingPrompt] != nil {
		t.Fatalf("a prompt is pending: %v", h)
	}
	return s, r, d, *bead, func(by time.Duration) {
		t.Helper()
		now = now.Add(by)
		must(t, d.Reconcile(ctx))
	}
}

const ownFirst = "Read brief.md and follow it." // retryRunner's first prompt

// Found live (yardr-7y16, 7 Oct): the yardmaster's first prompt sat typed
// in its input with nothing pending on the handle, so nothing looked, and
// the session was idle until a person pressed Enter. An idle agent whose
// input holds the first prompt gets Enter once it has been so for
// PromptSettle, and one session_prompt_typed says which text and why;
// before that, nothing.
func TestFirstPromptLeftInTheInputWithNothingPendingIsSubmitted(t *testing.T) {
	s, r, _, b, pass := ownSetup(t)
	// As a harness shows it: wrapped to the pane's width.
	r.input = ownFirst[:10] + "\n" + ownFirst[10:]
	pass(0)
	pass(2 * time.Second)
	if r.submits != 0 || len(eventKinds(t, s, b.ID, "session_prompt_typed")) != 0 {
		t.Fatalf("Enter sent %d times before the agent had been idle on its text for PromptSettle", r.submits)
	}
	pass(2 * time.Second)
	pass(0)
	pass(time.Minute)
	typed := eventKinds(t, s, b.ID, "session_prompt_typed")
	if r.submits != 1 || len(typed) != 1 || len(r.nudges) != 0 {
		t.Fatalf("Enter sent %d times, %d events, want one of each; typed %q, want nothing", r.submits, len(typed), r.nudges)
	}
	if d := typed[0].Data; d["session"] == nil || d["text"] != "the first prompt" || d["submit"] != float64(1) ||
		d["times"] != nil || !strings.Contains(fmt.Sprint(d["reason"]), "safety net") {
		t.Fatalf("session_prompt_typed = %+v", d)
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompt_gave_up")); n != 0 {
		t.Fatalf("session_prompt_gave_up logged %d times for a text that only needed Enter", n)
	}
}

// A prompt delivered a second time into a harness that still had no input
// is in the input twice. It is yardr's all the same: Enter sends both, and
// the event says so.
func TestFirstPromptTwiceInTheInputIsSubmitted(t *testing.T) {
	s, r, _, b, pass := ownSetup(t)
	r.input = ownFirst + ownFirst
	pass(0)
	pass(4 * time.Second)
	typed := eventKinds(t, s, b.ID, "session_prompt_typed")
	if r.submits != 1 || len(typed) != 1 || typed[0].Data["times"] != float64(2) {
		t.Fatalf("Enter sent %d times, session_prompt_typed = %+v", r.submits, typed)
	}
}

// Enter that the agent does not take is sent PromptTries times, PromptSettle
// apart; then session_prompt_gave_up tells the yardmaster, once, with the
// command that sends Enter by hand, and the session is left alone.
func TestOwnTextThatEnterDoesNotSendIsGivenUp(t *testing.T) {
	s, r, _, b, pass := ownSetup(t)
	r.input, r.deaf = ownFirst, true
	pass(0)
	pass(4 * time.Second)
	pass(2 * time.Second)
	if r.submits != 1 {
		t.Fatalf("Enter sent %d times, want a second only PromptSettle after the first", r.submits)
	}
	for range dispatch.PromptTries + 3 {
		pass(4 * time.Second)
	}
	gaveUp := eventKinds(t, s, b.ID, "session_prompt_gave_up")
	if r.submits != dispatch.PromptTries || len(eventKinds(t, s, b.ID, "session_prompt_typed")) != dispatch.PromptTries || len(gaveUp) != 1 {
		t.Fatalf("Enter sent %d times, gave up %d times; want %d and once", r.submits, len(gaveUp), dispatch.PromptTries)
	}
	if d := gaveUp[0].Data; d["typed"] != true || d["submits"] != float64(dispatch.PromptTries) || d["text"] != "the first prompt" ||
		!strings.HasPrefix(fmt.Sprint(d["hint"]), "herdr pane send-keys ") || !strings.HasSuffix(fmt.Sprint(d["hint"]), " enter") {
		t.Fatalf("session_prompt_gave_up = %+v", d)
	}
}

// Text in the input that is not yardr's alone is someone's (yardr-ps05):
// no key, however long it sits. So is an input yardr cannot tell, the pane
// of a harness whose mark it does not know, one it cannot read, and an
// agent that is not idle.
func TestInputThatIsNotKnownToBeYardrsOwnGetsNoKey(t *testing.T) {
	for name, set := range map[string]func(*ownRunner){
		"a person's text":          func(r *ownRunner) { r.input = "let me just" },
		"a person's words after":   func(r *ownRunner) { r.input = ownFirst + " and then stop" },
		"a person's words before":  func(r *ownRunner) { r.input = "wait. " + ownFirst },
		"half of the prompt":       func(r *ownRunner) { r.input = ownFirst[:12] },
		"an input not told":        func(r *ownRunner) { r.input, r.noInput = ownFirst, true },
		"an unknown kind":          func(r *ownRunner) { r.input, r.unmarked = ownFirst, true },
		"a pane that is not read":  func(r *ownRunner) { r.input, r.blind = ownFirst, true },
		"an agent at a question":   func(r *ownRunner) { r.input, r.status = ownFirst, "blocked" },
		"an agent that is working": func(r *ownRunner) { r.input, r.status = ownFirst, "working" },
	} {
		t.Run(name, func(t *testing.T) {
			s, r, _, b, pass := ownSetup(t)
			set(r)
			for range 5 {
				pass(4 * time.Second)
			}
			pass(time.Hour)
			if n := len(eventKinds(t, s, b.ID, "session_prompt_typed")); r.submits != 0 || n != 0 {
				t.Fatalf("Enter sent %d times, session_prompt_typed logged %d times", r.submits, n)
			}
		})
	}
}

// A crew member goes the same way, with the event on its journal: its first
// prompt, which its runner keeps on the handle at StartCrew, is submitted
// when it is found in the input of the idle agent with nothing pending.
func TestCrewFirstPromptLeftInTheInputIsSubmitted(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	d.PromptSettle = 3 * time.Second
	f.first = "You are the yardmaster. Run yardr prime --crew yardmaster and follow it."
	f.input = f.first
	must(t, d.Reconcile(ctx)) // start
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	if !r.Live() || r.Handle[dispatch.PendingPrompt] != nil {
		t.Fatalf("crew member = %+v", r)
	}
	must(t, d.Reconcile(ctx))
	now = now.Add(2 * time.Second)
	must(t, d.Reconcile(ctx))
	if n, _ := kindCount(t, s, r.Journal, "session_prompt_typed"); f.submits != 0 || n != 0 {
		t.Fatalf("Enter sent %d times before the agent had been idle on its text for PromptSettle", f.submits)
	}
	now = now.Add(2 * time.Second)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	n, ev := kindCount(t, s, r.Journal, "session_prompt_typed")
	if f.submits != 1 || n != 1 || ev.Data["crew"] != "yardmaster" || ev.Data["text"] != "the first prompt" || ev.Data["pane"] != "p-1" {
		t.Fatalf("Enter sent %d times, session_prompt_typed logged %d times: %+v", f.submits, n, ev.Data)
	}

	// A person's text there, later: no key.
	f.input = "some creative.."
	for range 3 {
		now = now.Add(time.Minute)
		must(t, d.Reconcile(ctx))
	}
	if f.submits != 1 || len(f.nudges) != 0 {
		t.Fatalf("a person's text was sent: Enter %d times, nudges %q", f.submits, f.nudges)
	}
}

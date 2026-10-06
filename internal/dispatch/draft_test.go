package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

func kindCount(t *testing.T, s store.API, bead, kind string) (n int, last store.Event) {
	t.Helper()
	evs, err := s.Events(context.Background(), bead, 200)
	must(t, err)
	for _, e := range evs {
		if e.Kind == kind {
			n++
			last = e
		}
	}
	return n, last
}

// Found live (yardr-ps05): a person was typing in the yardmaster's pane
// ("some creative..") when the yardmaster was nudged about its inbox, and the
// yardmaster got "some creative..yardr: 1 new in your inbox ..." as one
// message. A nudge is not typed into an input that holds text: it stays due,
// and is typed once, with the inbox as it then is, when the input is empty.
func TestNudgeWaitsForTextInTheInput(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	before, err := s.GetResident(ctx, "yardmaster")
	must(t, err)

	f.input = "some creative.."
	must(t, s.Mail(ctx, "yardmaster", "deploy freeze", "chris"))
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if len(f.nudges) != 0 || f.submits != 0 {
		t.Fatalf("typed into an input that holds text: nudges %v, Enter sent %d times", f.nudges, f.submits)
	}
	if r, _ := s.GetResident(ctx, "yardmaster"); r.Notified != before.Notified {
		t.Fatalf("a nudge that was not typed is recorded as said: notified %d -> %d", before.Notified, r.Notified)
	}

	// More mail while it waits: the nudge counts the inbox when it is typed.
	must(t, s.Mail(ctx, "yardmaster", "and a second thing", "chris"))
	must(t, d.Reconcile(ctx))
	f.input = ""
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if len(f.nudges) != 1 || !strings.Contains(f.nudges[0], "yardr: 2 new in your inbox") {
		t.Fatalf("nudges after the input emptied = %q, want one that counts both", f.nudges)
	}
}

// A harness whose input the runner cannot tell, and a pane it cannot read,
// say nothing about the input: the nudge is typed as it was before yardr
// looked.
func TestNudgeIsTypedWhenTheInputCannotBeTold(t *testing.T) {
	for name, set := range map[string]func(*residentRunner){
		"no mark":         func(f *residentRunner) { f.noInput = true },
		"pane unreadable": func(f *residentRunner) { f.blind = true },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, d := residentSetup(t, store.Resident{})
			ctx := context.Background()
			must(t, d.Reconcile(ctx))
			f.input = "whatever the pane ends with"
			set(f)
			must(t, s.Mail(ctx, "yardmaster", "deploy freeze", "chris"))
			must(t, d.Reconcile(ctx))
			must(t, d.Reconcile(ctx))
			if len(f.nudges) != 1 {
				t.Fatalf("nudges = %q, want one", f.nudges)
			}
		})
	}
}

// Text that stays is an abandoned draft. It is still not typed after and
// not cleared; one nudge_held event on the resident's journal says so after
// DraftWait, once per wait, and the nudge is typed when the text is gone.
func TestTextLeftInTheInputIsSaidOnceAndNeverTypedAfter(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	must(t, d.Reconcile(ctx))
	r, err := s.GetResident(ctx, "yardmaster")
	must(t, err)

	f.input = "some creative.."
	must(t, s.Mail(ctx, "yardmaster", "deploy freeze", "chris"))
	must(t, d.Reconcile(ctx))
	now = now.Add(9 * time.Minute)
	must(t, d.Reconcile(ctx))
	if n, _ := kindCount(t, s, r.Journal, "nudge_held"); n != 0 {
		t.Fatalf("nudge_held logged %d times before the text was there for 10 minutes", n)
	}
	for range 3 {
		now = now.Add(time.Minute)
		must(t, d.Reconcile(ctx))
	}
	n, ev := kindCount(t, s, r.Journal, "nudge_held")
	if n != 1 || ev.Data["resident"] != "yardmaster" || ev.Data["reason"] == nil {
		t.Fatalf("nudge_held logged %d times, want once, naming the resident: %+v", n, ev)
	}
	if len(f.nudges) != 0 || f.submits != 0 || f.input != "some creative.." {
		t.Fatalf("the draft was touched: nudges %v, Enter %d, input %q", f.nudges, f.submits, f.input)
	}

	f.input = ""
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 {
		t.Fatalf("nudges after the draft went = %q", f.nudges)
	}
	// A new wait says so again, counted from its own start.
	f.input = "another thought"
	must(t, s.Mail(ctx, "yardmaster", "second", "chris"))
	must(t, d.Reconcile(ctx))
	if n, _ := kindCount(t, s, r.Journal, "nudge_held"); n != 1 {
		t.Fatalf("a new draft was said at once: nudge_held logged %d times", n)
	}
	now = now.Add(11 * time.Minute)
	must(t, d.Reconcile(ctx))
	if n, _ := kindCount(t, s, r.Journal, "nudge_held"); n != 2 {
		t.Fatalf("nudge_held logged %d times after a second wait, want 2", n)
	}
}

// The one text in an input that is yardr's to send: its own last nudge,
// alone there because its Enter was not taken. Left, it would hold back
// every nudge after it. It gets Enter once, nothing is typed after it, and
// the next nudge follows when the input is empty.
func TestNudgeLeftInTheInputIsSubmitted(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	must(t, s.Mail(ctx, "yardmaster", "deploy freeze", "chris"))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 {
		t.Fatalf("nudges = %q", f.nudges)
	}
	// As a harness shows it: wrapped to the pane's width.
	nudge := strings.TrimPrefix(f.nudges[0], agentOf(t, s)+": ")
	f.input = nudge[:40] + "\n" + nudge[40:]
	f.deaf = true
	must(t, s.Mail(ctx, "yardmaster", "second", "chris"))
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if f.submits != 1 || len(f.nudges) != 1 {
		t.Fatalf("Enter sent %d times, want once; nudges %q, want none typed after it", f.submits, f.nudges)
	}
	f.input = ""
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 2 {
		t.Fatalf("nudges after the input emptied = %q", f.nudges)
	}

	// With a person's words after it, it is theirs: no Enter.
	f.input = strings.TrimPrefix(f.nudges[1], agentOf(t, s)+": ") + " and what is this?"
	must(t, s.Mail(ctx, "yardmaster", "third", "chris"))
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if f.submits != 1 || len(f.nudges) != 2 {
		t.Fatalf("a person's text was sent: Enter %d times, nudges %q", f.submits, f.nudges)
	}
}

// A later draft that happens to equal yardr's old nudge is still a person's
// text: only an immediately stranded nudge may be submitted as yardr's own.
func TestOldNudgeTextIsNotSubmitted(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{})
	ctx := context.Background()
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	must(t, d.Reconcile(ctx))
	must(t, s.Mail(ctx, "yardmaster", "first", "chris"))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 {
		t.Fatalf("first nudge = %q", f.nudges)
	}

	// The old nudge has long since either been taken or left alone. A person
	// types the same words later; that coincidence must not send Enter.
	now = now.Add(3 * d.Interval)
	f.input = strings.TrimPrefix(f.nudges[0], agentOf(t, s)+": ")
	f.deaf = true
	must(t, s.Mail(ctx, "yardmaster", "second", "chris"))
	must(t, d.Reconcile(ctx))
	if f.submits != 0 || len(f.nudges) != 1 || f.input == "" {
		t.Fatalf("old nudge text was treated as yardr's: Enter %d, nudges %q, input %q", f.submits, f.nudges, f.input)
	}
}

// The request to hand off before a rotation is a nudge too. Held back, it
// was not asked: the grace for the handoff does not run yet.
func TestRotationIsNotAskedIntoTextInTheInput(t *testing.T) {
	s, f, d := residentSetup(t, store.Resident{MaxAge: "1ms", HandoffGrace: "1ms"})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	f.input = "some creative.."
	time.Sleep(5 * time.Millisecond)
	for range 3 {
		must(t, d.Reconcile(ctx))
		time.Sleep(2 * time.Millisecond)
	}
	r, err := s.GetResident(ctx, "yardmaster")
	must(t, err)
	if len(f.nudges) != 0 || r.RotationAskedAt != nil || r.RestartRequested != "" || f.n != 1 {
		t.Fatalf("rotation asked into a draft: nudges %q, asked %v, restart %q, starts %d",
			f.nudges, r.RotationAskedAt, r.RestartRequested, f.n)
	}
	f.input = ""
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 || !strings.Contains(f.nudges[0], "due for rotation") {
		t.Fatalf("nudges = %q", f.nudges)
	}
}

// A bead session gets one thing typed by the dispatcher, its pending first
// prompt, and that waits for text in the input in the same way: nothing is
// sent, the prompt stays pending and is not counted as lost, and one
// nudge_held event on the bead says so after DraftWait.
func TestPendingPromptWaitsForTextInTheInput(t *testing.T) {
	s, r, d, b := nudgeSetup(t)
	ctx := context.Background()
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	r.status, r.input = "idle", "let me just"
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if h := handleOf(t, s, b.ID); len(r.nudges) != 0 || r.submits != 0 || h[dispatch.PendingPrompt] != "read your brief" {
		t.Fatalf("prompt typed into a draft: nudges %q, Enter %d, handle %v", r.nudges, r.submits, h)
	}
	now = now.Add(11 * time.Minute)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if n, ev := kindCount(t, s, b.ID, "nudge_held"); n != 1 || ev.Data["session"] == nil {
		t.Fatalf("nudge_held logged %d times, want once, naming the session: %+v", n, ev)
	}
	if n, _ := kindCount(t, s, b.ID, "session_prompt_lost"); n != 0 {
		t.Fatalf("a prompt that was not sent is logged as lost %d times", n)
	}
	r.input = ""
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if h := handleOf(t, s, b.ID); len(r.nudges) != 1 || h[dispatch.PendingPrompt] != nil {
		t.Fatalf("after the input emptied: nudges %q, handle %v", r.nudges, h)
	}
}

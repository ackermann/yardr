package dispatch_test

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// Found live (yardr-m519): the yardmaster sat at Claude Code's menu of login
// methods, whose selected option carries the mark of the input. Nothing may
// be typed there, and the one event names the login with the command that
// logs in, not text in the input. It is told at once, not after DraftWait,
// and kept on the handle for the crew's status and yardr doctor.
func TestLoginScreenHoldsNudgeAndTellsOnce(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)

	f.login = "Select login method:"
	f.input = "1. Claude account with subscription · Pro, Max, Team, or Enterprise"
	must(t, s.Mail(ctx, "yardmaster", "deploy freeze", "chris"))
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if len(f.nudges) != 0 || f.submits != 0 {
		t.Fatalf("typed into a login screen: nudges %v, Enter sent %d times", f.nudges, f.submits)
	}
	if n, _ := kindCount(t, s, r.Journal, "nudge_held"); n != 1 {
		t.Fatalf("nudge_held events = %d, want the one for the login", n)
	}
	_, ev := kindCount(t, s, r.Journal, "nudge_held")
	if ev.Data["reason"] != dispatch.ReasonNotLoggedIn {
		t.Errorf("reason = %v, want %q", ev.Data["reason"], dispatch.ReasonNotLoggedIn)
	}
	if ev.Data["line"] != "Select login method:" {
		t.Errorf("line = %v, want the login line", ev.Data["line"])
	}
	hint, _ := ev.Data["hint"].(string)
	if !strings.Contains(hint, "run claude in another terminal and log in there") {
		t.Errorf("hint names no login command: %q", hint)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if r.Handle[dispatch.HarnessLoginKey] != "Select login method:" {
		t.Errorf("handle mark = %v, want the login line", r.Handle[dispatch.HarnessLoginKey])
	}

	// Answered in the pane: the nudge goes on the next pass, the mark goes
	// with it, and no second event tells of the login.
	f.login, f.input = "", ""
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if len(f.nudges) != 1 {
		t.Fatalf("nudges after the login = %v, want the one held back", f.nudges)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if _, ok := r.Handle[dispatch.HarnessLoginKey]; ok {
		t.Errorf("handle still marked after the login: %v", r.Handle[dispatch.HarnessLoginKey])
	}
	if n, _ := kindCount(t, s, r.Journal, "nudge_held"); n != 1 {
		t.Errorf("nudge_held events = %d, want still the one", n)
	}
}

// Claude's selected login option can make herdr report idle. The crew state
// and one attention event must still be recorded when no inbox message is
// waiting to cause a nudge.
func TestIdleCrewAtLoginScreenIsReportedWithoutInboxMail(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)

	f.login = "Select login method:"
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if len(f.nudges) != 0 || f.submits != 0 {
		t.Fatalf("typed into a login screen with no inbox mail: nudges %v, Enter sent %d times", f.nudges, f.submits)
	}
	if n, ev := kindCount(t, s, r.Journal, "nudge_held"); n != 1 || ev.Data["reason"] != dispatch.ReasonNotLoggedIn {
		t.Fatalf("nudge_held events = %d, last %+v; want one not_logged_in event", n, ev.Data)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if r.Handle[dispatch.HarnessLoginKey] != "Select login method:" {
		t.Errorf("handle mark = %v, want the login line", r.Handle[dispatch.HarnessLoginKey])
	}
}

// The browser sign-in has no input mark at all: with the first prompt
// pending it is no harness still starting, so no session_harness_starting
// says so. The prompt waits on the login instead.
func TestLoginScreenWithoutInputMarkIsNoStartingHarness(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{})
	f.sent, f.noScreen = true, true
	f.login = "Paste code here if prompted>"
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if len(f.nudges) != 0 {
		t.Fatalf("typed into a login screen: %v", f.nudges)
	}
	if n, _ := kindCount(t, s, r.Journal, "session_harness_starting"); n != 0 {
		t.Fatalf("session_harness_starting events = %d, want none", n)
	}
	n, ev := kindCount(t, s, r.Journal, "nudge_held")
	if n != 1 || ev.Data["reason"] != dispatch.ReasonNotLoggedIn {
		t.Fatalf("nudge_held events = %d, last %+v; want the one for the login", n, ev.Data)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if r.Handle[dispatch.HarnessLoginKey] != "Paste code here if prompted>" {
		t.Fatalf("handle mark = %v, want the login line", r.Handle[dispatch.HarnessLoginKey])
	}

	// Logged in: the screen is up, the prompt arrives, and the mark goes
	// with it.
	f.login, f.noScreen, f.shown = "", false, true
	must(t, d.Reconcile(ctx))
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if _, ok := r.Handle[dispatch.HarnessLoginKey]; ok {
		t.Errorf("handle still marked after the prompt arrived: %v", r.Handle[dispatch.HarnessLoginKey])
	}
	if n, _ := kindCount(t, s, r.Journal, "session_prompted"); n != 1 {
		t.Errorf("session_prompted events = %d, want 1", n)
	}
	if n, _ := kindCount(t, s, r.Journal, "nudge_held"); n != 1 {
		t.Errorf("nudge_held events = %d, want still the one", n)
	}
}

// Where the runtime reads the agent at the login screen as blocked, the
// daemon does not wait for a human: the login is said instead, with the
// kind's own command.
func TestLoginScreenWhileBlockedIsNoWaitForAHuman(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{})
	f.sent, f.blocked = true, true
	f.login = "Select login method:"
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	r.Handle["kind"] = "codex"
	must(t, s.SetCrewHandle(ctx, "yardmaster", r.Handle))
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if n, _ := kindCount(t, s, r.Journal, "crew_blocked"); n != 0 {
		t.Fatalf("crew_blocked events = %d, want none", n)
	}
	n, ev := kindCount(t, s, r.Journal, "nudge_held")
	if n != 1 || ev.Data["reason"] != dispatch.ReasonNotLoggedIn {
		t.Fatalf("nudge_held events = %d, last %+v; want the one for the login", n, ev.Data)
	}
	hint, _ := ev.Data["hint"].(string)
	if !strings.Contains(hint, "run codex in another terminal and log in there") {
		t.Errorf("hint names no login command of the kind: %q", hint)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if r.Handle[dispatch.HarnessLoginKey] != "Select login method:" {
		t.Errorf("handle mark = %v, want the login line", r.Handle[dispatch.HarnessLoginKey])
	}
}

// A Muse pane at its login screen is held the same way, and the hint
// names Muse's own command (yardr-dx5f).
func TestLoginScreenMuseNamesItsOwnCommand(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{})
	f.sent, f.blocked = true, true
	f.login = "Log in with browser · Enter to choose"
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	r.Handle["kind"] = "muse"
	must(t, s.SetCrewHandle(ctx, "yardmaster", r.Handle))
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if len(f.nudges) != 0 || f.submits != 0 {
		t.Fatalf("typed into a login screen: nudges %v, Enter sent %d times", f.nudges, f.submits)
	}
	n, ev := kindCount(t, s, r.Journal, "nudge_held")
	if n != 1 || ev.Data["reason"] != dispatch.ReasonNotLoggedIn {
		t.Fatalf("nudge_held events = %d, last %+v; want the one for the login", n, ev.Data)
	}
	hint, _ := ev.Data["hint"].(string)
	if !strings.Contains(hint, "run muse in another terminal and log in there") {
		t.Errorf("hint names no login command of the kind: %q", hint)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if r.Handle[dispatch.HarnessLoginKey] != "Log in with browser · Enter to choose" {
		t.Errorf("handle mark = %v, want the login line", r.Handle[dispatch.HarnessLoginKey])
	}

	// Logged in and past it: the prime arrives, the mark goes with it,
	// and no second event tells of the login.
	f.login, f.blocked, f.shown = "", false, true
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if n, _ := kindCount(t, s, r.Journal, "session_prompted"); n != 1 {
		t.Fatalf("session_prompted events = %d, want 1", n)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if _, ok := r.Handle[dispatch.HarnessLoginKey]; ok {
		t.Errorf("handle still marked after the login: %v", r.Handle[dispatch.HarnessLoginKey])
	}
	if n, _ := kindCount(t, s, r.Journal, "nudge_held"); n != 1 {
		t.Errorf("nudge_held events = %d, want still the one", n)
	}
}

// An agent that works is past any login screen seen before: the mark goes,
// while the login told of stays told.
func TestWorkingClearsTheLoginMark(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	agent := agentOf(t, s)
	r, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)

	f.login = "Select login method:"
	must(t, s.Mail(ctx, "yardmaster", "deploy freeze", "chris"))
	must(t, d.Reconcile(ctx))
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if r.Handle[dispatch.HarnessLoginKey] == nil {
		t.Fatal("handle not marked at the login screen")
	}
	f.login = ""
	f.set(agent, "working")
	must(t, d.Reconcile(ctx))
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if _, ok := r.Handle[dispatch.HarnessLoginKey]; ok {
		t.Errorf("handle still marked while the agent works: %v", r.Handle[dispatch.HarnessLoginKey])
	}
	if n, _ := kindCount(t, s, r.Journal, "nudge_held"); n != 1 {
		t.Errorf("nudge_held events = %d, want the one for the login", n)
	}
}

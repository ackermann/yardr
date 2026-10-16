package store_test

import (
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// refusals are the move_refused events on bead id.
func refusals(t *testing.T, s store.API, id string) []store.Event {
	t.Helper()
	evs, err := s.Events(t.Context(), id, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if e.Kind == store.MoveRefused {
			out = append(out, e)
		}
	}
	return out
}

// A move the yard refuses to the agent of a session leaves move_refused on
// the bead, once per session and reason: an agent whose done did not take
// sits idle as if it had, and nothing else on the bead says it tried. A
// move that worked, and a refused one by someone in no session, leave none.
func TestRefusedMoveOfASessionsAgentIsLogged(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, g := range []string{"builders", "reviewers"} {
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunning(ctx, builder.ID, map[string]any{"agent": "x-new"}); err != nil {
		t.Fatal(err)
	}
	mine := store.Expect{Session: builder.ID}

	// The owner asks for an outcome its stage has not: refused, and the
	// session lives on.
	if _, err := s.Done(ctx, b.ID, "shipped", "", mine, "x-new"); err == nil {
		t.Fatal("an outcome the stage has not was taken")
	}
	got := refusals(t, s, b.ID)
	if len(got) != 1 {
		t.Fatalf("move_refused events = %+v, want one", got)
	}
	e := got[0]
	if e.Actor != "yard" || e.Data["session"] != builder.ID || e.Data["agent"] != "x-new" || e.Data["group"] != "builders" ||
		e.Data["stage"] != "new" || e.Data["reason"] != store.RefusedNotAllowed ||
		e.Data["command"] != "yardr bead done "+b.ID+" --outcome shipped" ||
		!strings.Contains(e.Data["error"].(string), "has no outcome shipped for stage new") || e.Data["session_bead"] != nil {
		t.Fatalf("move_refused = %s by %s %+v", e.Kind, e.Actor, e.Data)
	}

	// A move that worked says so itself, as advanced.
	if _, err := s.Done(ctx, b.ID, "", "", mine, "x-new"); err != nil {
		t.Fatal(err)
	}
	if got := refusals(t, s, b.ID); len(got) != 1 {
		t.Fatalf("a move that worked logged move_refused: %+v", got)
	}

	// The bead is the reviewer's now. The builder's agent, kept open, says
	// done again: stale, logged once however often and whichever move it
	// tries.
	reviewer, err := s.Claim(ctx, b.ID, "reviewers", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.Done(ctx, b.ID, "", "", mine, "x-new"); err == nil {
			t.Fatal("a stale done was taken")
		}
	}
	for name, move := range map[string]func() (*store.Bead, error){
		"advance": func() (*store.Bead, error) { return s.AdvanceNote(ctx, b.ID, "decide", "", mine, "x-new") },
		"close":   func() (*store.Bead, error) { return s.CloseBead(ctx, b.ID, "done", mine, "x-new") },
		"hold":    func() (*store.Bead, error) { return s.SetHold(ctx, b.ID, true, mine, "x-new") },
		"release": func() (*store.Bead, error) { return s.Release(ctx, b.ID, "", mine, "x-new") },
	} {
		if _, err := move(); err == nil {
			t.Fatalf("a stale %s was taken", name)
		}
	}
	got = refusals(t, s, b.ID)
	if len(got) != 2 {
		t.Fatalf("move_refused events = %+v, want the earlier one and one stale", got)
	}
	e = got[1]
	if e.Data["session"] != builder.ID || e.Data["reason"] != store.RefusedStale || e.Data["command"] != "yardr bead done "+b.ID ||
		!strings.Contains(e.Data["error"].(string), "stale: bead "+b.ID+" is now in review owned by "+reviewer.ID) {
		t.Fatalf("stale move_refused = %+v", e.Data)
	}

	// A person in no session reads the error where they typed the command:
	// a stale snapshot, a move the flow has not, a bead that is not there.
	for name, move := range map[string]func() (*store.Bead, error){
		"stale":       func() (*store.Bead, error) { return s.CloseBead(ctx, b.ID, "x", store.Expect{Stage: "new"}, "mckean") },
		"not allowed": func() (*store.Bead, error) { return s.AdvanceNote(ctx, b.ID, "merged", "", store.Expect{}, "mckean") },
		"outcome":     func() (*store.Bead, error) { return s.Done(ctx, b.ID, "shipped", "", store.Expect{}, "mckean") },
		"no bead":     func() (*store.Bead, error) { return s.Done(ctx, "yard-zzzz", "", "", store.Expect{}, "mckean") },
	} {
		if _, err := move(); err == nil {
			t.Fatalf("%s: a person's move was taken", name)
		}
	}
	if got := refusals(t, s, b.ID); len(got) != 2 {
		t.Fatalf("a person's refused move logged move_refused: %+v", got)
	}

	// The reviewer's own refused move is another session's: told too.
	if _, err := s.Done(ctx, b.ID, "shipped", "", store.Expect{Session: reviewer.ID}, "x-review"); err == nil {
		t.Fatal("an outcome the stage has not was taken")
	}
	if got := refusals(t, s, b.ID); len(got) != 3 || got[2].Data["session"] != reviewer.ID || got[2].Data["agent"] != "x-review" {
		t.Fatalf("the reviewer's refused move: %+v", got)
	}
}

// A refused move of a bead that is not the session's own is logged on the
// bead it named, with whose session it was; one that names no bead at all
// is logged on the session's own.
func TestRefusedMoveOfAnotherBead(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, g := range []string{"builders", "others"} {
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Claim(ctx, a.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Claim(ctx, b.ID, "others", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	pane := store.Expect{Session: owner.ID}

	if _, err := s.SetHold(ctx, b.ID, true, pane, "a-new"); err == nil {
		t.Fatal("a hold of another session's bead was taken")
	}
	got := refusals(t, s, b.ID)
	if len(got) != 1 || got[0].Data["session"] != owner.ID || got[0].Data["session_bead"] != a.ID ||
		got[0].Data["reason"] != store.RefusedStale || got[0].Data["command"] != "yardr bead hold "+b.ID {
		t.Fatalf("move_refused on the other bead = %+v", got)
	}
	if sess, _ := s.GetSession(ctx, other.ID); sess.EndedAt != nil {
		t.Fatalf("the refused hold ended the owner's session: %+v", sess)
	}

	if _, err := s.Done(ctx, "yard-zzzz", "", "", pane, "a-new"); err == nil {
		t.Fatal("a done of no bead was taken")
	}
	got = refusals(t, s, a.ID)
	if len(got) != 1 || got[0].Data["reason"] != store.RefusedNotFound || got[0].Data["command"] != "yardr bead done yard-zzzz" {
		t.Fatalf("move_refused for a bead that is not there = %+v", got)
	}

	// Not a move: resuming a bead that is not held changes nothing and is
	// no claim of work done.
	if _, err := s.SetHold(ctx, b.ID, false, pane, "a-new"); err == nil {
		t.Fatal("an unhold of another session's bead was taken")
	}
	if got := refusals(t, s, b.ID); len(got) != 1 {
		t.Fatalf("a refused unhold logged move_refused: %+v", got)
	}
}

// A manual group's session is a person's claim: their refused move is
// theirs to read, like any person's.
func TestRefusedMoveOfAManualClaimIsNotLogged(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.Claim(ctx, b.ID, "people", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Done(ctx, b.ID, "shipped", "", store.Expect{Session: claim.ID}, "mckean"); err == nil {
		t.Fatal("an outcome the stage has not was taken")
	}
	if got := refusals(t, s, b.ID); len(got) != 0 {
		t.Fatalf("a person's manual claim logged move_refused: %+v", got)
	}
}

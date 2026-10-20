package store_test

import (
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// An exec session counts as its group for a move out of a stage whose
// group lists that group among its members: the release script's group
// moves the bead its stage's people would. The actor names nothing that
// matters; the session's group is what is listed.
func TestExecSessionMovesAsItsListedGroup(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "scripts", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"scripts"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "deciders"}, "me"); err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, w.ID, "scripts", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "to move", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Advance(ctx, b.ID, "review", store.Expect{Session: sess.ID}, "stranger")
	if err != nil || got.Stage != "review" {
		t.Fatalf("an exec session of a listed group moves = %+v, %v", got, err)
	}
}

// The same exec session is refused once its group is no longer listed:
// only what the stage's group lists counts.
func TestExecSessionRefusedWhenItsGroupIsNotListed(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "scripts", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"scripts"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "deciders"}, "me"); err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, w.ID, "scripts", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "to move", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Advance(ctx, b.ID, "review", store.Expect{Session: sess.ID}, "stranger")
	if err != nil || got.Stage != "review" {
		t.Fatalf("an exec session of a listed group moves = %+v, %v", got, err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"jo"}}, "me"); err != nil {
		t.Fatal(err)
	}
	next, err := s.CreateBead(ctx, store.NewBead{Title: "next", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, next.ID, "review", store.Expect{Session: sess.ID}, "stranger"); err == nil ||
		!strings.Contains(err.Error(), "only members of group deciders may move it out of stage new: jo") {
		t.Fatalf("an exec session of an unlisted group moves = %v", err)
	}
}

// An agent session of a listed group is still refused: the intake guard
// stands for runners with a model to talk to, whatever group lists them.
func TestAgentSessionOfListedGroupStillRefused(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "agents", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"agents"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "deciders"}, "me"); err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, w.ID, "agents", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "to move", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, b.ID, "review", store.Expect{Session: sess.ID}, "agents"); err == nil ||
		!strings.Contains(err.Error(), "only members of group deciders may move it out of stage new: agents (this is the session "+sess.ID+" of a bead's agent, whatever actor the command names)") {
		t.Fatalf("an agent session of a listed group moves = %v", err)
	}
}

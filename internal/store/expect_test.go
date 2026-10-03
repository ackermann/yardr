package store_test

import (
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A move that expects a session or a stage the bead no longer has is
// refused and changes nothing: an agent kept or orphaned after its bead
// moved on cannot end the session that owns it now, and a decision made on
// an old snapshot is not applied. Without an expectation (a person acting
// outside any session) nothing is checked.
func TestStaleMovesRefused(t *testing.T) {
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, g := range []string{"builders", "reviewers"} {
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "herdr"}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Done(ctx, b.ID, "", "", store.Expect{Session: builder.ID}, "builder"); err != nil {
		t.Fatalf("owner's own done refused: %v", err)
	}
	reviewer, err := s.Claim(ctx, b.ID, "reviewers", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetBead(ctx, b.ID)

	// The builder's session ended with its done; a second call from it
	// (kept tab, orphaned agent) must not touch the reviewer's work.
	old := store.Expect{Session: builder.ID}
	stale := map[string]func() (*store.Bead, error){
		"done":    func() (*store.Bead, error) { return s.Done(ctx, b.ID, "", "again", old, "builder") },
		"advance": func() (*store.Bead, error) { return s.AdvanceNote(ctx, b.ID, "decide", "again", old, "builder") },
		"close":   func() (*store.Bead, error) { return s.CloseBead(ctx, b.ID, "done", old, "builder") },
		// A review form that snapshotted the bead in new.
		"stage": func() (*store.Bead, error) {
			return s.AdvanceNote(ctx, b.ID, "backlog", "", store.Expect{Stage: "new"}, "mckean")
		},
		"stage close": func() (*store.Bead, error) { return s.CloseBead(ctx, b.ID, "x", store.Expect{Stage: "new"}, "mckean") },
	}
	for name, move := range stale {
		_, err := move()
		want := "stale: bead " + b.ID + " is now in review owned by " + reviewer.ID
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
	if cur, _ := s.GetBead(ctx, b.ID); cur.Revision != before.Revision {
		t.Fatalf("a refused move changed the bead: %+v", cur)
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 0 {
		t.Fatalf("a refused move left a note: %+v", notes)
	}
	if sess, _ := s.GetSession(ctx, reviewer.ID); sess.EndedAt != nil {
		t.Fatalf("a refused move ended the owner's session: %+v", sess)
	}

	// The owner, expecting what is true, moves it.
	got, err := s.Done(ctx, b.ID, "changes", "", store.Expect{Session: reviewer.ID, Stage: "review"}, "reviewer")
	if err != nil || got.Stage != "new" {
		t.Fatalf("owner's done = %+v, %v", got, err)
	}

	// A person with no session acts on an owned bead unchecked.
	third, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if got, err = s.CloseBead(ctx, b.ID, "dropped", store.Expect{}, "mayor"); err != nil || got.Status != "closed" {
		t.Fatalf("mayor's close = %+v, %v", got, err)
	}
	if sess, _ := s.GetSession(ctx, third.ID); sess.EndedAt == nil {
		t.Fatal("close did not end the owning session")
	}
	// Closing again from that ended session is stale too.
	_, err = s.CloseBead(ctx, b.ID, "done", store.Expect{Session: third.ID}, "builder")
	if err == nil || !strings.Contains(err.Error(), "is now in new (closed) owned by no session") {
		t.Fatalf("close from an ended session: %v", err)
	}
}

// The session check is for the bead the session was started for: another
// bead moved from the same pane (a reviewer advancing a follow-up it filed,
// the mayor from its own pane) is checked as the actor's, like a person at
// a shell, except that one owned by a live session is refused rather than
// taken from under its agent.
func TestSessionCheckIsForTheSessionsOwnBead(t *testing.T) {
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, g := range []string{"builders", "reviewers"} {
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "herdr"}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a", Rig: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Rig: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Claim(ctx, a.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	// What a's pane sends: its session, and the bead it was started for.
	pane := store.Expect{Session: owner.ID, Bead: a.ID}

	// b is owned by no session: a's pane moves it, as the actor.
	got, err := s.AdvanceNote(ctx, b.ID, "review", "filed from a", pane, "yard-a-new")
	if err != nil || got.Stage != "review" {
		t.Fatalf("advance of an unowned bead from another bead's pane = %+v, %v", got, err)
	}
	events, _ := s.Events(ctx, b.ID, 1)
	if len(events) != 1 || events[0].Kind != "advanced" || events[0].Actor != "yard-a-new" {
		t.Fatalf("the move is not the actor's: %+v", events)
	}
	if cur, _ := s.GetBead(ctx, a.ID); cur.Session == nil || *cur.Session != owner.ID {
		t.Fatalf("moving b touched a's session: %+v", cur)
	}

	// b owned by a live session: refused, and nothing changes.
	other, err := s.Claim(ctx, b.ID, "reviewers", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetBead(ctx, b.ID)
	want := "stale: bead " + b.ID + " is now in review owned by " + other.ID + "; expected no session (session " + owner.ID + " owns " + a.ID
	for name, move := range map[string]func() (*store.Bead, error){
		"advance": func() (*store.Bead, error) { return s.AdvanceNote(ctx, b.ID, "new", "again", pane, "yard-a-new") },
		"done":    func() (*store.Bead, error) { return s.Done(ctx, b.ID, "", "again", pane, "yard-a-new") },
		"close":   func() (*store.Bead, error) { return s.CloseBead(ctx, b.ID, "x", pane, "yard-a-new") },
		"hold":    func() (*store.Bead, error) { return s.SetHold(ctx, b.ID, true, pane, "yard-a-new") },
	} {
		if _, err := move(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s of a bead another session owns: %v, want %q", name, err, want)
		}
	}
	if cur, _ := s.GetBead(ctx, b.ID); cur.Revision != before.Revision {
		t.Fatalf("a refused move changed the bead: %+v", cur)
	}
	if sess, _ := s.GetSession(ctx, other.ID); sess.EndedAt != nil {
		t.Fatalf("a refused move ended the owner's session: %+v", sess)
	}

	// Its own bead the session still moves, and is still refused on once
	// the bead has moved on.
	if _, err := s.Done(ctx, a.ID, "", "", pane, "yard-a-new"); err != nil {
		t.Fatalf("the owner's own done: %v", err)
	}
	if _, err := s.Done(ctx, a.ID, "", "again", pane, "yard-a-new"); err == nil || !strings.Contains(err.Error(), "owned by no session; expected session "+owner.ID) {
		t.Fatalf("done again from the ended session: %v", err)
	}
}

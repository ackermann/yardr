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
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard"}, "me")
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

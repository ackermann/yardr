package store_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// Every bead write takes the revision its caller read: when the bead has
// changed since, the write is refused with ErrConflict and changes
// nothing, so two writers that read the same bead cannot both apply what
// they decided on it. The current revision is accepted; none expects
// nothing.
func TestStaleRevisionRefused(t *testing.T) {
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	read := b.Revision
	title := "someone else's"
	cur, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title}, store.Expect{Revision: read}, "other")
	if err != nil {
		t.Fatalf("update at the current revision: %v", err)
	}
	if cur.Revision == read {
		t.Fatal("a real change kept the revision")
	}

	old := store.Expect{Revision: read}
	mine := "mine"
	stale := map[string]func() (*store.Bead, error){
		"update": func() (*store.Bead, error) {
			return s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &mine}, old, "me")
		},
		"hold":    func() (*store.Bead, error) { return s.SetHold(ctx, b.ID, true, old, "me") },
		"advance": func() (*store.Bead, error) { return s.AdvanceNote(ctx, b.ID, "review", "", old, "me") },
		// The note is written in the same transaction; it must not
		// survive the refusal, nor be what makes the revision differ.
		"advance with note": func() (*store.Bead, error) { return s.AdvanceNote(ctx, b.ID, "review", "n", old, "me") },
		"done":              func() (*store.Bead, error) { return s.Done(ctx, b.ID, "", "n", old, "me") },
		"close":             func() (*store.Bead, error) { return s.CloseBead(ctx, b.ID, "x", old, "me") },
	}
	for name, write := range stale {
		_, err := write()
		if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "is at revision "+cur.Revision+", not "+read) {
			t.Errorf("%s at a stale revision: %v, want a conflict", name, err)
		}
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Revision != cur.Revision || got.Title != title || got.Hold || got.Stage != "new" {
		t.Fatalf("a refused write changed the bead: %+v", got)
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 0 {
		t.Fatalf("a refused write left a note: %+v", notes)
	}

	// At the current revision a move with a note goes through: the note
	// re-mints the revision inside the write, after the check.
	moved, err := s.Done(ctx, b.ID, "", "ready", store.Expect{Revision: cur.Revision}, "me")
	if err != nil || moved.Stage != "review" {
		t.Fatalf("done at the current revision = %+v, %v", moved, err)
	}
	if _, err := s.SetHold(ctx, b.ID, true, store.Expect{Revision: moved.Revision}, "me"); err != nil {
		t.Fatalf("hold at the current revision: %v", err)
	}
	if _, err := s.SetHold(ctx, b.ID, false, store.Expect{}, "me"); err != nil {
		t.Fatalf("unhold expecting nothing: %v", err)
	}
}

// Claim checks the group's limit inside its own transaction: two
// dispatchers racing for a limit-1 group leave exactly one live session,
// whatever their in-memory counts said.
func TestClaimRespectsGroupLimit(t *testing.T) {
	s, ctx := flowRig(t)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	const n = 8
	beads := make([]string, n)
	for i := range beads {
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		beads[i] = b.ID
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, id := range beads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Claim(ctx, id, "builders", "new", "yard")
		}()
	}
	wg.Wait()
	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, store.ErrAtLimit):
			t.Errorf("claim %s: %v, want ErrAtLimit", beads[i], err)
		}
	}
	live, err := s.Sessions(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if won != 1 || len(live) != 1 {
		t.Fatalf("%d claims won, %d live sessions; want exactly one", won, len(live))
	}

	// A freed slot is claimable again, and a refused claim left its bead
	// unowned.
	if _, err := s.Release(ctx, live[0].Bead, "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range beads {
		if id == live[0].Bead {
			continue
		}
		if b, _ := s.GetBead(ctx, id); b.Group != nil {
			t.Fatalf("a refused claim owns %s: %+v", id, b)
		}
		if _, err := s.Claim(ctx, id, "builders", "new", "yard"); err != nil {
			t.Fatalf("claim after the slot freed: %v", err)
		}
		break
	}
}

// A crew member carries a revision that changes on every write; the writes a
// person makes (define, enable, restart, delete) refuse a stale one with
// ErrConflict and change nothing. The daemon's own bookkeeping is not
// checked, but moves the revision.
func TestCrewRevision(t *testing.T) {
	s, ctx := flowRig(t)
	r, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision == "" {
		t.Fatal("a new crew member has no revision")
	}
	read := r.Revision
	if err := s.MarkNotified(ctx, "yardmaster", 7); err != nil {
		t.Fatal(err)
	}
	now, _ := s.GetCrew(ctx, "yardmaster")
	if now.Revision == read {
		t.Fatal("a write kept the crew member's revision")
	}

	stale := map[string]func() error{
		"put": func() error {
			_, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", MaxAge: "1h", Revision: read}, "me")
			return err
		},
		"enable":  func() error { return s.SetCrewEnabled(ctx, "yardmaster", false, read, "me") },
		"restart": func() error { return s.RequestRestart(ctx, "yardmaster", "why", read, "me") },
		"delete":  func() error { return s.DeleteCrew(ctx, "yardmaster", read, "me") },
		// Expecting a revision of a crew member that does not exist yet.
		"put new": func() error {
			_, err := s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Revision: read}, "me")
			return err
		},
	}
	for name, write := range stale {
		if err := write(); !errors.Is(err, store.ErrConflict) {
			t.Errorf("%s at a stale revision: %v, want a conflict", name, err)
		}
	}
	if got, _ := s.GetCrew(ctx, "yardmaster"); got.Revision != now.Revision || !got.Enabled || got.MaxAge != "" || got.RestartRequested != "" {
		t.Fatalf("a refused write changed the crew member: %+v", got)
	}
	if _, err := s.GetCrew(ctx, "clerk"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused put created a crew member: %v", err)
	}

	if err := s.SetCrewEnabled(ctx, "yardmaster", false, now.Revision, "me"); err != nil {
		t.Fatalf("disable at the current revision: %v", err)
	}
	got, _ := s.GetCrew(ctx, "yardmaster")
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", MaxAge: "1h", Revision: got.Revision}, "me"); err != nil {
		t.Fatalf("put at the current revision: %v", err)
	}
}

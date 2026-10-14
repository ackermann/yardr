package store_test

import (
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A bead needs a title: create refuses an empty one and one of only
// spaces, and creates nothing; update refuses to set one the same way
// (yardr-0fbd).
func TestBeadNeedsATitle(t *testing.T) {
	s, ctx, b := yardrDepot(t)
	before, err := s.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	for _, title := range []string{"", "   "} {
		if _, err := s.CreateBead(ctx, store.NewBead{Title: title, Depot: "yard", Stage: "new"}, "me"); err == nil || !strings.Contains(err.Error(), "a bead needs a title") {
			t.Fatalf("create %q: %v", title, err)
		}
		if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title}, store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "a bead needs a title") {
			t.Fatalf("update to %q: %v", title, err)
		}
	}
	if after, _ := s.ListBeads(ctx, store.BeadFilter{All: true}); len(after) != len(before) {
		t.Fatalf("refused creates left %d beads, want %d", len(after), len(before))
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("refused writes logged %d events", now-head)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Title != b.Title || got.Revision != b.Revision {
		t.Fatalf("refused update changed the bead: %+v", got)
	}
}

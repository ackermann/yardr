package store_test

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// An update of the body or the title of a bead with a live session, by
// anyone but that session, leaves a note for the session in the same
// write: what changed, never the new text. The note is told like any
// other. Anything else a patch changes leaves no note, nor does a write
// by the session itself or one on a bead with no live session.
func TestUpdateBodyTellsTheLiveSession(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 5}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "the shed", Body: "paint it", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	notes := func() []store.Note {
		t.Helper()
		ns, err := s.Notes(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ns
	}
	notedOf := func(seq uint64) store.Event {
		t.Helper()
		evs, err := s.Events(ctx, b.ID, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == "noted" && e.Data["note"] == float64(seq) {
				return e
			}
		}
		t.Fatalf("no noted event for note %d in %+v", seq, evs)
		return store.Event{}
	}

	body := "paint it red"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Body: &body}, store.Expect{}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	ns := notes()
	if len(ns) != 1 {
		t.Fatalf("notes after a body edit = %d, want 1", len(ns))
	}
	n := ns[0]
	if n.Notify != sess.ID {
		t.Fatalf("Notify = %q, want the live session %s", n.Notify, sess.ID)
	}
	if !n.NotifyLive {
		t.Fatalf("note %+v is not for a live session", n)
	}
	if n.Author != "yardmaster" {
		t.Fatalf("Author = %q, want the writer", n.Author)
	}
	if want := "body changed: read the bead again (yardr bead show " + b.ID + ")"; n.Text != want {
		t.Fatalf("Text = %q, want %q", n.Text, want)
	}
	if strings.Contains(n.Text, body) {
		t.Fatalf("the note carries the new text: %q", n.Text)
	}
	if e := notedOf(n.Seq); e.Data["session"] != sess.ID {
		t.Fatalf("noted event data = %v, want session %s", e.Data, sess.ID)
	}

	title := "the red shed"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title}, store.Expect{}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if ns := notes(); len(ns) != 2 || !strings.HasPrefix(ns[1].Text, "title changed: ") {
		t.Fatalf("notes after a title edit = %+v, want a title note", ns)
	}

	bothBody, bothTitle := "paint it blue", "the blue shed"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &bothTitle, Body: &bothBody}, store.Expect{}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if ns := notes(); len(ns) != 3 || !strings.HasPrefix(ns[2].Text, "body and title changed: ") {
		t.Fatalf("notes after editing both = %+v, want a both note", ns)
	}
	if strings.Contains(notes()[2].Text, bothBody) || strings.Contains(notes()[2].Text, bothTitle) {
		t.Fatalf("the both note carries the new text: %q", notes()[2].Text)
	}

	// Labels, priority and metadata change nothing the session works
	// from: no note. Neither does a patch that names the body but
	// changes nothing.
	pri := 3
	for _, p := range []store.BeadPatch{
		{AddLabels: []string{"paint"}},
		{Priority: &pri},
		{Metadata: map[string]any{"color": "blue"}},
		{Body: &bothBody},
		{Title: &bothTitle},
	} {
		if _, err := s.UpdateBead(ctx, b.ID, p, store.Expect{}, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	if ns := notes(); len(ns) != 3 {
		t.Fatalf("notes after label, priority, metadata and no-op edits = %d, want 3", len(ns))
	}

	// The session's own edit tells nothing.
	own := "paint it green"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Body: &own}, store.Expect{Session: sess.ID}, "builder"); err != nil {
		t.Fatal(err)
	}
	if ns := notes(); len(ns) != 3 {
		t.Fatalf("notes after the session's own edit = %d, want 3", len(ns))
	}

	// No live session: no note.
	if err := s.EndSession(ctx, sess.ID, "died", "x"); err != nil {
		t.Fatal(err)
	}
	late := "paint it yellow"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Body: &late}, store.Expect{}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if ns := notes(); len(ns) != 3 {
		t.Fatalf("notes after an edit with no live session = %d, want 3", len(ns))
	}
}

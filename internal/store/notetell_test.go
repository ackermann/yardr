package store_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A plain note on a bead with a live session, written by anyone but that
// session, names the session it is for: the daemon tells it once, when
// idle. The noted event says so with it. A note by the session itself,
// one on a bead with no session, and one through any other write name
// none.
func TestNotedNamesTheSessionTold(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 5}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
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

	n, err := s.AddNotedAs(ctx, b.ID, "narrower now", "yardmaster", "")
	if err != nil {
		t.Fatal(err)
	}
	if n.Notify != sess.ID {
		t.Fatalf("Notify = %q, want the live session %s", n.Notify, sess.ID)
	}
	if e := notedOf(n.Seq); e.Data["session"] != sess.ID {
		t.Fatalf("noted event data = %v, want session %s", e.Data, sess.ID)
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 1 || !notes[0].NotifyLive {
		t.Fatalf("Notes = %+v, want one note for a live session", notes)
	}

	own, err := s.AddNotedAs(ctx, b.ID, "on it", "builder", sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if own.Notify != "" {
		t.Fatalf("the session's own note names %q", own.Notify)
	}
	if e := notedOf(own.Seq); e.Data["session"] != nil {
		t.Fatalf("the own note's event names a session: %v", e.Data)
	}

	if err := s.EndSession(ctx, sess.ID, "died", "x"); err != nil {
		t.Fatal(err)
	}
	after, err := s.AddNotedAs(ctx, b.ID, "too late", "yardmaster", "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Notify != "" {
		t.Fatalf("a note with no live session names %q", after.Notify)
	}

	// Through any other write a note tells nothing, live session or none.
	sess2, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(ctx, b.ID, "the yard's own", "yard"); err != nil {
		t.Fatal(err)
	}
	notes, _ := s.Notes(ctx, b.ID)
	if got := notes[len(notes)-1]; got.Notify != "" {
		t.Fatalf("AddNote with live session %s names %q", sess2.ID, got.Notify)
	}
	// The first note's session has ended since: told to nothing now.
	if notes, _ := s.Notes(ctx, b.ID); notes[0].NotifyLive {
		t.Fatalf("the ended session's note still reads live: %+v", notes[0])
	}
}

// The told session records having read the note, and only it does. An ack
// of a note still untold stands: the session read it from the bead, and
// the telling is cancelled. Acking twice is the note again.
func TestAckNote(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 5}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.AddNotedAs(ctx, b.ID, "narrower now", "yardmaster", "")
	if err != nil {
		t.Fatal(err)
	}
	acked := func() []store.Event {
		t.Helper()
		evs, err := s.Events(ctx, b.ID, 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.Event
		for _, e := range evs {
			if e.Kind == "note_acked" {
				out = append(out, e)
			}
		}
		return out
	}

	if _, err := s.AckNote(ctx, b.ID, n.Seq, "another", "builder"); err == nil {
		t.Fatal("another session acked the note")
	}
	if _, err := s.AckNote(ctx, b.ID, 999, sess.ID, "builder"); err == nil {
		t.Fatal("acked a note the bead has not")
	}
	own, err := s.AddNotedAs(ctx, b.ID, "on it", "builder", sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AckNote(ctx, b.ID, own.Seq, sess.ID, "builder"); err == nil {
		t.Fatal("acked a note told to no session")
	}
	got, err := s.AckNote(ctx, b.ID, n.Seq, sess.ID, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Acked {
		t.Fatalf("acked note = %+v", got)
	}
	if evs := acked(); len(evs) != 1 || evs[0].Data["note"] != float64(n.Seq) || evs[0].Data["session"] != sess.ID {
		t.Fatalf("note_acked events = %+v", evs)
	}
	if _, err := s.AckNote(ctx, b.ID, n.Seq, sess.ID, "builder"); err != nil {
		t.Fatal(err)
	}
	if evs := acked(); len(evs) != 1 {
		t.Fatalf("acking twice logged %d events", len(evs))
	}
	if notes, _ := s.Notes(ctx, b.ID); !notes[0].Acked {
		t.Fatalf("Notes = %+v, want the ack", notes)
	}
}

// Marking a note told records it with its event, once: the second mark
// is a no-op, never a second event. A mark for any session but the
// note's is refused.
func TestMarkNoteTold(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 5}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.AddNotedAs(ctx, b.ID, "narrower now", "yardmaster", "")
	if err != nil {
		t.Fatal(err)
	}
	told := func() []store.Event {
		t.Helper()
		evs, err := s.Events(ctx, b.ID, 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.Event
		for _, e := range evs {
			if e.Kind == "note_told" {
				out = append(out, e)
			}
		}
		return out
	}

	if err := s.MarkNoteTold(ctx, b.ID, n.Seq, "another"); err == nil {
		t.Fatal("marked told for another session")
	}
	if err := s.MarkNoteTold(ctx, b.ID, n.Seq, sess.ID); err != nil {
		t.Fatal(err)
	}
	if evs := told(); len(evs) != 1 || evs[0].Data["note"] != float64(n.Seq) || evs[0].Data["session"] != sess.ID {
		t.Fatalf("note_told events = %+v", evs)
	}
	if err := s.MarkNoteTold(ctx, b.ID, n.Seq, sess.ID); err != nil {
		t.Fatal(err)
	}
	if evs := told(); len(evs) != 1 {
		t.Fatalf("marking twice logged %d events", len(evs))
	}
	if notes, _ := s.Notes(ctx, b.ID); !notes[0].Told {
		t.Fatalf("Notes = %+v, want the telling", notes)
	}
}

// An ack from a session's pane is that session's, whatever session the
// caller names: an agent cannot mark a note for another session read, nor
// cancel its telling, by naming that session.
func TestAckNoteFromAPaneIsThatPanesSession(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 5}, "me"); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := s.Claim(ctx, a.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	// This test process is the shell of a's tab (see expect_test.go).
	me := os.Getpid()
	handle := map[string]any{"agent": "yard-a-new", "shell_pid": me}
	if st, err := peer.Describe(me); err == nil {
		handle["shell_start"] = st.Start
	}
	if err := s.MarkRunning(ctx, mine.ID, handle); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)

	theirs, err := s.AddNotedAs(ctx, b.ID, "narrower now", "yardmaster", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AckNote(pane, b.ID, theirs.Seq, other.ID, "yard-a-new")
	if err == nil || !strings.Contains(err.Error(), "this pane is session "+mine.ID+", not "+other.ID) {
		t.Fatalf("ack as another session from a's pane: %v, want it refused", err)
	}
	notes, err := s.Notes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := notes[len(notes)-1]; n.Acked {
		t.Fatalf("a refused ack marked the note read: %+v", n)
	}

	// Its own note it acks, with or without a session in its environment.
	own, err := s.AddNotedAs(ctx, a.ID, "narrower too", "yardmaster", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.AckNote(pane, a.ID, own.Seq, "", "yard-a-new"); err != nil || !got.Acked {
		t.Fatalf("ack of its own note from the pane: %+v %v", got, err)
	}
}

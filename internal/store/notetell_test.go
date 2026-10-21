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

// noteTellYard opens the yard the note-tell tests share: a group with a
// claimed bead and a note told to its session. events reads back the
// bead's events of one kind, as each test's filter did.
func noteTellYard(t *testing.T) (store.API, context.Context, *store.Bead, *store.Session, *store.Note, func(string) []store.Event) {
	t.Helper()
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
	events := func(kind string) []store.Event {
		t.Helper()
		evs, err := s.Events(ctx, b.ID, 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.Event
		for _, e := range evs {
			if e.Kind == kind {
				out = append(out, e)
			}
		}
		return out
	}
	return s, ctx, b, sess, n, events
}

// A plain note on a bead with a live session, written by anyone but that
// session, names the session it is for: the daemon tells it once, when
// idle. The noted event says so with it. A note by the session itself,
// one on a bead with no session, and one through any other write name
// none.
func TestNotedNamesTheSessionTold(t *testing.T) {
	t.Parallel()
	s, ctx, b, sess, n, _ := noteTellYard(t)
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
	t.Parallel()
	s, ctx, b, sess, n, _ := noteTellYard(t)

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
	if _, err := s.AckNote(ctx, b.ID, n.Seq, sess.ID, "builder"); err != nil {
		t.Fatal(err)
	}
	if notes, _ := s.Notes(ctx, b.ID); !notes[0].Acked {
		t.Fatalf("Notes = %+v, want the ack", notes)
	}
}

// Marking a note told records it with its event, once: the second mark
// is a no-op, never a second event. A mark for any session but the
// note's is refused.
func TestMarkNoteTold(t *testing.T) {
	t.Parallel()
	s, ctx, b, sess, n, events := noteTellYard(t)
	told := func() []store.Event {
		t.Helper()
		return events("note_told")
	}

	if err := s.MarkNoteTold(ctx, b.ID, n.Seq, "another", "typed"); err == nil {
		t.Fatal("marked told for another session")
	}
	if err := s.MarkNoteTold(ctx, b.ID, n.Seq, sess.ID, "typed"); err != nil {
		t.Fatal(err)
	}
	if evs := told(); len(evs) != 1 || evs[0].Data["note"] != float64(n.Seq) || evs[0].Data["session"] != sess.ID || evs[0].Data["delivery"] != "typed" {
		t.Fatalf("note_told events = %+v", evs)
	}
	if err := s.MarkNoteTold(ctx, b.ID, n.Seq, sess.ID, "typed"); err != nil {
		t.Fatal(err)
	}
	if evs := told(); len(evs) != 1 {
		t.Fatalf("marking twice logged %d events", len(evs))
	}
	if notes, _ := s.Notes(ctx, b.ID); !notes[0].Told {
		t.Fatalf("Notes = %+v, want the telling", notes)
	}
}

// Marking a note untold records why neither delivery took it, with its
// event, once. A note already told or read is not marked untold.
func TestMarkNoteUntold(t *testing.T) {
	t.Parallel()
	s, ctx, b, sess, n, events := noteTellYard(t)
	untold := func() []store.Event {
		t.Helper()
		return events("note_untold")
	}

	if err := s.MarkNoteUntold(ctx, b.ID, n.Seq, "another", "no agent"); err == nil {
		t.Fatal("marked untold for another session")
	}
	if err := s.MarkNoteUntold(ctx, b.ID, n.Seq, sess.ID, "no agent in the handle"); err != nil {
		t.Fatal(err)
	}
	if evs := untold(); len(evs) != 1 || evs[0].Data["note"] != float64(n.Seq) || evs[0].Data["session"] != sess.ID ||
		evs[0].Data["reason"] != "no agent in the handle" {
		t.Fatalf("note_untold events = %+v", evs)
	}
	if err := s.MarkNoteUntold(ctx, b.ID, n.Seq, sess.ID, "again"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNoteTold(ctx, b.ID, n.Seq, sess.ID, "typed"); err != nil {
		t.Fatal(err)
	}
	if evs := untold(); len(evs) != 1 {
		t.Fatalf("marking twice logged %d events", len(evs))
	}
	if notes, _ := s.Notes(ctx, b.ID); notes[0].Untold == "" || notes[0].Told {
		t.Fatalf("Notes = %+v, want untold with its reason and not told", notes)
	}
	if events, err := s.Events(ctx, b.ID, 50); err != nil {
		t.Fatal(err)
	} else {
		for _, e := range events {
			if e.Kind == "note_told" {
				t.Fatalf("marking an untold note told logged %+v", e)
			}
		}
	}
}

// An ack from a session's pane is that session's, whatever session the
// caller names: an agent cannot mark a note for another session read, nor
// cancel its telling, by naming that session.
func TestAckNoteFromAPaneIsThatPanesSession(t *testing.T) {
	t.Parallel()
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

// A note written from a session's or a crew member's pane is stored as
// that session's actor or that crew member, whatever --actor says: an
// agent cannot write a note as the operator by naming them. The noted
// event names it too, as the nudge reads it. From no pane the actor
// stands as given, session claim or none.
func TestNoteFromAPaneIsThatPanesActor(t *testing.T) {
	t.Parallel()
	// This test process is the pane's shell (see expect_test.go). Each
	// case has its own yard, so only the pane under test is on record.
	me := os.Getpid()
	shell := func(extra map[string]any) map[string]any {
		h := map[string]any{"shell_pid": me}
		for k, v := range extra {
			h[k] = v
		}
		if st, err := peer.Describe(me); err == nil {
			h["shell_start"] = st.Start
		}
		return h
	}
	notedActor := func(t *testing.T, s store.API, ctx context.Context, bead string, seq uint64) string {
		t.Helper()
		evs, err := s.Events(ctx, bead, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == "noted" && e.Data["note"] == float64(seq) {
				return e.Actor
			}
		}
		t.Fatalf("no noted event for note %d in %+v", seq, evs)
		return ""
	}

	t.Run("herdr session", func(t *testing.T) {
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		actor := b.ID + "-new"
		if err := s.MarkRunning(ctx, sess.ID, shell(map[string]any{"agent": actor})); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, me)

		n, err := s.AddNotedAs(pane, b.ID, "do something else", "op", sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n.Author != actor {
			t.Fatalf("note from the pane as op = %q, want the session's actor %q", n.Author, actor)
		}
		if got := notedActor(t, s, ctx, b.ID, n.Seq); got != actor {
			t.Fatalf("noted event actor = %q, want %q", got, actor)
		}
		if notes, _ := s.Notes(ctx, b.ID); notes[len(notes)-1].Author != actor {
			t.Fatalf("Notes = %+v, want the session's actor", notes)
		}
		// Through the other writes the pane's note is that session's too.
		if err := s.AddNote(pane, b.ID, "the yard's own, supposedly", "op"); err != nil {
			t.Fatal(err)
		}
		if notes, _ := s.Notes(ctx, b.ID); notes[len(notes)-1].Author != actor {
			t.Fatalf("AddNote from the pane as op = %+v, want %q", notes[len(notes)-1], actor)
		}
		if got, err := s.AddNoted(pane, b.ID, "seq and all", "op"); err != nil || got.Author != actor {
			t.Fatalf("AddNoted from the pane as op = %+v, %v, want %q", got, err, actor)
		}
		// A session claim the pane does not bear out is no refusal: the
		// note lands as the pane's session's, and the claim still says
		// who is told.
		stray, err := s.AddNotedAs(pane, b.ID, "words", "op", "no-such-session")
		if err != nil {
			t.Fatalf("note from the pane naming another session: %v", err)
		}
		if stray.Author != actor || stray.Notify != sess.ID {
			t.Fatalf("stray note = %+v, want author %q for session %s", stray, actor, sess.ID)
		}
	})

	t.Run("exec session", func(t *testing.T) {
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		h := map[string]any{"pid": me}
		if st, err := peer.Describe(me); err == nil {
			h["start"] = st.Start
		}
		if err := s.MarkRunning(ctx, sess.ID, h); err != nil {
			t.Fatal(err)
		}
		n, err := s.AddNotedAs(store.WithCaller(ctx, me), b.ID, "do something else", "op", sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		// An exec session's scripts run as the group.
		if n.Author != "builders" {
			t.Fatalf("note from the script's pane as op = %q, want builders", n.Author)
		}
	})

	t.Run("crew member", func(t *testing.T) {
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CrewStarted(ctx, "yardmaster", shell(nil)); err != nil {
			t.Fatal(err)
		}
		n, err := s.AddNotedAs(store.WithCaller(ctx, me), b.ID, "do something else", "op", "")
		if err != nil {
			t.Fatal(err)
		}
		if n.Author != "yardmaster" {
			t.Fatalf("note from the crew pane as op = %q, want yardmaster", n.Author)
		}
	})

	t.Run("no pane", func(t *testing.T) {
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		// The session has no pane on record: a person's terminal.
		n, err := s.AddNotedAs(ctx, b.ID, "words", "op", "")
		if err != nil {
			t.Fatal(err)
		}
		if n.Author != "op" {
			t.Fatalf("note from no pane as op = %q, want op", n.Author)
		}
		// A session claim alone, from no pane, rewrites nothing.
		own, err := s.AddNotedAs(ctx, b.ID, "on it", "op", sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if own.Author != "op" || own.Notify != "" {
			t.Fatalf("claimed note from no pane = %+v, want author op telling none", own)
		}
	})
}

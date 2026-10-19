package store_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// heldEvents counts the held events on the bead.
func heldEvents(t *testing.T, s store.API, ctx context.Context, bead string) int {
	t.Helper()
	evs, err := s.Events(ctx, bead, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "held" {
			n++
		}
	}
	return n
}

// holdBy puts the bead on hold as actor from ctx and answers what it reads
// back as, which must be what the write answered.
func holdBy(t *testing.T, s store.API, ctx context.Context, b *store.Bead, exp store.Expect, actor string) *store.Bead {
	t.Helper()
	wrote, err := s.SetHold(ctx, b.ID, true, exp, actor)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBead(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Hold || wrote.HeldBy != got.HeldBy {
		t.Fatalf("the hold wrote %+v and reads back %+v", wrote, got)
	}
	return got
}

// unhold lifts the hold and requires the holder gone with it, not served
// as an empty field.
func unhold(t *testing.T, s store.API, ctx context.Context, b *store.Bead) {
	t.Helper()
	got, err := s.SetHold(ctx, b.ID, false, store.Expect{}, "op")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hold || got.HeldBy != "" {
		t.Fatalf("after the unhold: %+v, want no hold and no holder", got)
	}
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), "held_by") {
		t.Fatalf("an unheld bead is served as %s, want held_by absent", raw)
	}
}

// A bead on hold says who put it there: its HeldBy is the actor of the
// held event that began the hold, so a person is who they say they are,
// and a session's or a crew member's pane is that session's actor or that
// crew member, whatever --actor says (paneAuthor). Lifting the hold takes
// the holder away.
func TestHoldRecordsWhoHeld(t *testing.T) {
	t.Parallel()

	t.Run("a person", func(t *testing.T) {
		s := storetest.New(t)
		ctx := context.Background()
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		if got := holdBy(t, s, ctx, b, store.Expect{}, "mckean"); got.HeldBy != "mckean" {
			t.Fatalf("held by mckean = %+v", got)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "held"); a != "mckean" {
			t.Fatalf("held event actor = %q, want mckean: the holder is who the event says", a)
		}
		unhold(t, s, ctx, b)
	})

	t.Run("a session's pane", func(t *testing.T) {
		s, ctx, b, sess, actor := paneMoveYard(t)
		pane := store.WithCaller(ctx, os.Getpid())
		// The pane names the operator and is still its session.
		got := holdBy(t, s, pane, b, store.Expect{Session: sess.ID}, "op")
		if got.HeldBy != actor {
			t.Fatalf("held from the pane of %s = %+v, want it held by %q", b.ID, got, actor)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "held"); a != actor {
			t.Fatalf("held event actor = %q, want %q", a, actor)
		}
		// The hold ended the session, so lifting it is no pane's: another
		// bead no session owns is held and lifted from the pane instead,
		// since over the socket every call of this process is the pane's.
		other, err := s.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		if got := holdBy(t, s, pane, other, store.Expect{}, "op"); got.HeldBy != actor {
			t.Fatalf("held from the pane of %s = %+v, want it held by %q", b.ID, got, actor)
		}
		unhold(t, s, pane, other)
	})

	t.Run("a crew member's pane", func(t *testing.T) {
		s := storetest.New(t)
		ctx := context.Background()
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		h := map[string]any{"shell_pid": os.Getpid()}
		if st, err := peer.Describe(os.Getpid()); err == nil {
			h["shell_start"] = st.Start
		}
		if err := s.CrewStarted(ctx, "yardmaster", h); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if got := holdBy(t, s, pane, b, store.Expect{}, "op"); got.HeldBy != "yardmaster" {
			t.Fatalf("held from the yardmaster's pane = %+v, want it held by yardmaster", got)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "held"); a != "yardmaster" {
			t.Fatalf("held event actor = %q, want yardmaster", a)
		}
		unhold(t, s, ctx, b)
	})

	// Holding a held bead changes nothing and logs nothing, so it keeps the
	// holder whose held event began the hold; a hold taken again after the
	// unhold has the new holder.
	t.Run("a second hold", func(t *testing.T) {
		s := storetest.New(t)
		ctx := context.Background()
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		holdBy(t, s, ctx, b, store.Expect{}, "mckean")
		if got := holdBy(t, s, ctx, b, store.Expect{}, "nell"); got.HeldBy != "mckean" {
			t.Fatalf("held again by nell = %+v, want it still held by mckean", got)
		}
		if n := heldEvents(t, s, ctx, b.ID); n != 1 {
			t.Fatalf("%d held events after holding twice, want 1", n)
		}
		unhold(t, s, ctx, b)
		if got := holdBy(t, s, ctx, b, store.Expect{}, "nell"); got.HeldBy != "nell" {
			t.Fatalf("held by nell after the unhold = %+v", got)
		}
		if n := heldEvents(t, s, ctx, b.ID); n != 2 {
			t.Fatalf("%d held events after the second hold, want 2", n)
		}
	})
}

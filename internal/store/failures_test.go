package store_test

import (
	"context"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The failure count lives on the bead: un-holding resets it, and a prune
// that takes the unheld event (and the failed sessions) with it does not
// bring the old failures back. Only a death or failure in the bead's
// current stage counts, and entering a stage starts over.
func TestFailureCountOutlivesTheUnheldEvent(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 5}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "flaky", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	fail := func(state string) {
		t.Helper()
		sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.EndSession(ctx, sess.ID, state, "x"); err != nil {
			t.Fatal(err)
		}
	}
	count := func(stage string) int {
		t.Helper()
		n, err := s.FailureCount(ctx, b.ID, stage)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	fail("died")
	fail("failed")
	fail("aborted") // the runtime's fault, not the bead's
	if n := count("new"); n != 2 {
		t.Fatalf("failures = %d, want 2", n)
	}
	if n := count("review"); n != 0 {
		t.Fatalf("failures in a stage the bead is not in = %d", n)
	}
	// Holding is no failure and forgets none; un-holding grants fresh attempts.
	if _, err := s.SetHold(ctx, b.ID, true, store.Expect{}, "yard"); err != nil {
		t.Fatal(err)
	}
	if n := count("new"); n != 2 {
		t.Fatalf("failures after hold = %d, want 2", n)
	}
	if _, err := s.SetHold(ctx, b.ID, false, store.Expect{}, "human"); err != nil {
		t.Fatal(err)
	}
	if n := count("new"); n != 0 {
		t.Fatalf("failures after unhold = %d, want 0", n)
	}
	// The whole history goes, unheld event and failed sessions included
	// (the newest event always stays, so something must follow the unhold).
	if err := s.AddNote(ctx, b.ID, "cause fixed, retrying", "human"); err != nil {
		t.Fatal(err)
	}
	for _, sess := range mustSessions(t, s) {
		if err := s.MarkStopped(ctx, sess.ID); err != nil {
			t.Fatal(err)
		}
		if err := own.BackdateSession(sess.ID, time.Now().Add(-100*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := own.Backdate(1, head(t, s), time.Now().Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(ctx, keep90, "me")
	if err != nil || res.Sessions != 3 || res.HeldBy != "" {
		t.Fatalf("prune = %+v %v, want the 3 sessions gone and nothing held", res, err)
	}
	if evs, _ := s.Events(ctx, b.ID, 0); len(evs) != 1 || evs[0].Kind != "noted" {
		t.Fatalf("bead events after prune = %+v, want only the note", evs)
	}
	if n := count("new"); n != 0 {
		t.Fatalf("failures after the unheld event was pruned = %d, want 0", n)
	}
	// New failures count from zero; moving on starts over again.
	fail("died")
	if n := count("new"); n != 1 {
		t.Fatalf("failures after a fresh death = %d, want 1", n)
	}
	if _, err := s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if n := count("review"); n != 0 {
		t.Fatalf("failures on entering a stage = %d, want 0", n)
	}
	if n := count("new"); n != 0 {
		t.Fatalf("failures in the stage left = %d, want 0", n)
	}

	// A same-stage transition is a fresh stay too: it clears the failures of
	// a bead no session works, whether an outcome makes it (which also adds
	// a round) or a move by hand.
	putNamed(t, s, "loop", "new -> review on done | new on retry\n")
	loop, err := s.CreateBead(ctx, store.NewBead{Title: "again", Metadata: map[string]any{"flow": "loop"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for how, move := range map[string]func() error{
		"outcome": func() error { _, err := s.Done(ctx, loop.ID, "retry", "", store.Expect{}, "me"); return err },
		"hand":    func() error { _, err := s.Advance(ctx, loop.ID, "new", store.Expect{}, "me"); return err },
	} {
		sess, err := s.Claim(ctx, loop.ID, "g", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.EndSession(ctx, sess.ID, "died", "x"); err != nil {
			t.Fatal(err)
		}
		if n, err := s.FailureCount(ctx, loop.ID, "new"); err != nil || n != 1 {
			t.Fatalf("%s: failures before the same-stage move = %d, %v; want 1", how, n, err)
		}
		if err := move(); err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		if n, err := s.FailureCount(ctx, loop.ID, "new"); err != nil || n != 0 {
			t.Fatalf("%s: failures after the same-stage move = %d, %v; want 0", how, n, err)
		}
	}
}

func mustSessions(t *testing.T, s store.API) []store.Session {
	t.Helper()
	all, err := s.Sessions(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// Claim's limit check with 10k ended sessions in the group.
func BenchmarkClaimWithEndedSessions(b *testing.B) {
	s, own := storetest.NewOwned(b)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 1}, "me"); err != nil {
		b.Fatal(err)
	}
	bead, err := s.CreateBead(ctx, store.NewBead{Title: "b", Stage: "new"}, "me")
	if err != nil {
		b.Fatal(err)
	}
	if err := own.BulkSessions(10000, bead.UUID, "g", "failed", time.Now().Add(-time.Hour)); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sess, err := s.Claim(ctx, bead.ID, "g", "new", "yard")
		if err != nil {
			b.Fatal(err)
		}
		if err := s.EndSession(ctx, sess.ID, "aborted", ""); err != nil {
			b.Fatal(err)
		}
	}
}

package store_test

import (
	"context"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A manual group is a siding when it states any of fold, fold_after and
// fold_max, and what it leaves out is 4 beads, 8h and 6 points. Every reader
// of the keys (bead fold, flow show, the readiness check) asks SidingOf, so
// the rows here are what a siding means to all of them.
func TestSidingOf(t *testing.T) {
	t.Parallel()
	manual := func(config map[string]any) store.Group {
		return store.Group{Name: "siding", Runner: "manual", Config: config}
	}
	rows := []struct {
		name  string
		group store.Group
		want  store.Siding
		ok    bool
	}{
		{"a manual group that states nothing is no siding", manual(nil), store.Siding{}, false},
		{"nor with the keys of a manual group that are not its own", manual(map[string]any{"wait": "1d", "notify": true}), store.Siding{}, false},
		{"fold alone leaves the hours and the points their defaults", manual(map[string]any{"fold": 3}), store.Siding{Fold: 3, After: 8 * time.Hour, Max: 6}, true},
		{"fold_after alone makes a siding of the default count", manual(map[string]any{"fold_after": "2h"}), store.Siding{Fold: 4, After: 2 * time.Hour, Max: 6}, true},
		{"fold_max alone makes a siding of the default count", manual(map[string]any{"fold_max": 10}), store.Siding{Fold: 4, After: 8 * time.Hour, Max: 10}, true},
		{"all three are read", manual(map[string]any{"fold": 5, "fold_after": "1d", "fold_max": 9}), store.Siding{Fold: 5, After: 24 * time.Hour, Max: 9}, true},
		// A group as the store hands it back is JSON: a number is a float64.
		{"a stored number is a float64", manual(map[string]any{"fold": float64(7)}), store.Siding{Fold: 7, After: 8 * time.Hour, Max: 6}, true},
		{"a value no key holds reads as not stated, the group still a siding", manual(map[string]any{"fold": "four", "fold_after": "soon", "fold_max": 1}),
			store.Siding{Fold: 4, After: 8 * time.Hour, Max: 6}, true},
		{"a group of another runner is no siding", store.Group{Name: "g", Runner: "exec", Config: map[string]any{"fold": 4}}, store.Siding{}, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got, ok := store.SidingOf(r.group)
			if got != r.want || ok != r.ok {
				t.Errorf("SidingOf = %+v, %v; want %+v, %v", got, ok, r.want, r.ok)
			}
		})
	}
}

// SidingBeads says, of the beads waiting in a siding, when each entered, what
// it weighs and why a fold would leave it, in the order the definition of
// eligibility lists the reasons (design yardr-9x0n, 3.5). A manual group's
// claim is no reason: it holds the beads of its stage up to its limit, with
// nothing running.
func TestSidingBeads(t *testing.T) {
	t.Parallel()
	s, ctx := storetest.New(t), context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutGroup(ctx, store.Group{Name: "script", Runner: "exec", Limit: 9, Config: map[string]any{"cmd": "true"}}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "siding", Runner: "manual", Limit: 9, Config: map[string]any{"fold": 4}}, "me"))
	made := 0
	newBead := func(labels ...string) *store.Bead {
		t.Helper()
		made++
		b, err := s.CreateBead(ctx, store.NewBead{Title: "part", Stage: "siding", Labels: labels}, "me")
		must(err)
		return b
	}
	read := func(b *store.Bead) store.SidingBead {
		t.Helper()
		got, err := s.SidingBeads(ctx, []string{b.ID})
		if err != nil || len(got) != 1 || got[0].Bead != b.ID {
			t.Fatalf("SidingBeads(%s) = %+v, %v", b.ID, got, err)
		}
		return got[0]
	}
	ask := store.Ask{Question: "Which one?", To: "mckean", Context: []string{"it is open"}}

	t.Run("a bead nothing holds is taken, with when it entered and what it weighs", func(t *testing.T) {
		for labels, points := range map[string]int{"size:small": 1, "size:medium": 2, "size:large": 4, "difficulty:1": 2} {
			b := newBead(labels)
			got := read(b)
			if got.Stuck != "" || got.Points != points || !got.Entered.Equal(b.CreatedAt) {
				t.Errorf("%s: %+v, want taken, %d points, entered %v", labels, got, points, b.CreatedAt)
			}
		}
	})

	t.Run("a held bead is held", func(t *testing.T) {
		b := newBead()
		_, err := s.SetHold(ctx, b.ID, true, store.Expect{}, "me")
		must(err)
		if got := read(b).Stuck; got != "held" {
			t.Errorf("stuck = %q, want held", got)
		}
	})

	t.Run("a bead a session works is claimed", func(t *testing.T) {
		b := newBead()
		_, err := s.Claim(ctx, b.ID, "script", b.Stage, "yard")
		must(err)
		if got := read(b).Stuck; got != "claimed" {
			t.Errorf("stuck = %q, want claimed", got)
		}
	})

	t.Run("a bead the siding's own manual group holds is not claimed", func(t *testing.T) {
		b := newBead()
		_, err := s.Claim(ctx, b.ID, "siding", b.Stage, "yard")
		must(err)
		if held, err := s.GetBead(ctx, b.ID); err != nil || held.Group == nil || *held.Group != "siding" {
			t.Fatalf("the manual group does not hold the bead: %+v, %v", held, err)
		}
		if got := read(b).Stuck; got != "" {
			t.Errorf("stuck = %q, want taken", got)
		}
	})

	t.Run("a bead an open bead blocks is blocked until that closes", func(t *testing.T) {
		blocker := newBead()
		b, err := s.CreateBead(ctx, store.NewBead{Title: "part", Stage: "siding", BlockedBy: []string{blocker.ID}}, "me")
		must(err)
		if got := read(b).Stuck; got != "blocked" {
			t.Errorf("stuck = %q, want blocked", got)
		}
		_, err = s.CloseBead(ctx, blocker.ID, "done", store.Expect{}, "me")
		must(err)
		if got := read(b).Stuck; got != "" {
			t.Errorf("after the blocker closed: stuck = %q, want taken", got)
		}
	})

	t.Run("a question to a person is asked while it is open or put aside, not once answered", func(t *testing.T) {
		b := newBead()
		first, err := s.AddAsk(ctx, b.ID, ask, "yardmaster")
		must(err)
		if got := read(b).Stuck; got != "asked" {
			t.Errorf("open ask: stuck = %q, want asked", got)
		}
		must(s.DeferAsk(ctx, b.ID, first, "mckean"))
		if got := read(b).Stuck; got != "asked" {
			t.Errorf("put aside: stuck = %q, want asked", got)
		}
		must(s.AnswerAsk(ctx, b.ID, first, 0, "this one", "mckean", false))
		if got := read(b).Stuck; got != "" {
			t.Errorf("answered: stuck = %q, want taken", got)
		}
	})

	t.Run("the first reason in the order held, claimed, blocked, asked is the one said", func(t *testing.T) {
		blocked := func() *store.Bead {
			blocker := newBead()
			b, err := s.CreateBead(ctx, store.NewBead{Title: "part", Stage: "siding", BlockedBy: []string{blocker.ID}}, "me")
			must(err)
			return b
		}
		asked := func(b *store.Bead) {
			_, err := s.AddAsk(ctx, b.ID, ask, "yardmaster")
			must(err)
		}

		held := blocked()
		asked(held)
		_, err := s.SetHold(ctx, held.ID, true, store.Expect{}, "me")
		must(err)
		if got := read(held).Stuck; got != "held" {
			t.Errorf("held, blocked and asked: stuck = %q, want held", got)
		}

		claimed := newBead()
		_, err = s.Claim(ctx, claimed.ID, "script", claimed.Stage, "yard")
		must(err)
		asked(claimed)
		if got := read(claimed).Stuck; got != "claimed" {
			t.Errorf("claimed and asked: stuck = %q, want claimed", got)
		}

		// A session that took a bead can have it blocked after.
		worked := newBead()
		_, err = s.Claim(ctx, worked.ID, "script", worked.Stage, "yard")
		must(err)
		must(s.AddDep(ctx, newBead().ID, worked.ID, store.DepBlocks, "", "", "me"))
		if got := read(worked).Stuck; got != "claimed" {
			t.Errorf("claimed and blocked: stuck = %q, want claimed", got)
		}

		both := blocked()
		asked(both)
		if got := read(both).Stuck; got != "blocked" {
			t.Errorf("blocked and asked: stuck = %q, want blocked", got)
		}
	})

	t.Run("the beads come back in the order asked, and one that is not a bead is an error", func(t *testing.T) {
		a, b := newBead(), newBead("size:large")
		got, err := s.SidingBeads(ctx, []string{b.ID, a.ID})
		if err != nil || len(got) != 2 || got[0].Bead != b.ID || got[1].Bead != a.ID || got[0].Points != 4 {
			t.Errorf("SidingBeads = %+v, %v", got, err)
		}
		if _, err := s.SidingBeads(ctx, []string{a.ID, "nope-1"}); err == nil {
			t.Error("a bead that is not there was read")
		}
		if got, err := s.SidingBeads(ctx, nil); err != nil || got == nil || len(got) != 0 {
			t.Errorf("SidingBeads(nil) = %#v, %v; want an empty list", got, err)
		}
	})
}

// A question the stage itself put on a bead's entering is no reason for a fold
// to leave it: closing the bead takes it back (design yardr-4cct). One a
// person asked is.
func TestSidingBeadsIgnoresAStageAsk(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if asks := stageAsks(t, s, b.ID); len(asks) != 2 || asks[0].State != store.AskOpen {
		t.Fatalf("entering review put %d asks, the first %v; want two, open", len(asks), asks[0].State)
	}
	stuck := func() string {
		t.Helper()
		got, err := s.SidingBeads(ctx, []string{b.ID})
		if err != nil || len(got) != 1 {
			t.Fatalf("SidingBeads = %+v, %v", got, err)
		}
		return got[0].Stuck
	}
	if got := stuck(); got != "" {
		t.Errorf("with the stage's asks open: stuck = %q, want taken", got)
	}
	if _, err := s.AddAsk(ctx, b.ID, store.Ask{Question: "Which one?", To: "ada", Context: []string{"it is open"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := stuck(); got != "asked" {
		t.Errorf("with a person's ask open too: stuck = %q, want asked", got)
	}
}

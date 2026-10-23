package dispatch_test

import (
	"context"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// The dispatcher's pass folds a siding that is ready by its clock, and
// wakes at a waiting siding's due, by a fake clock and no sleep: before
// the due nothing folds and the due is armed; at it the eligible beads
// fold, and with the siding empty no due is armed.
func TestPassFoldsASidingAtItsDue(t *testing.T) {
	t.Parallel()
	s, _, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	must(t, s.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"))
	f, err := store.ParseFlow("backlog -> siding | new\nsiding -> ready | backlog\nready -> new | backlog\nnew -> merged\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "siding.f"
	if _, err := s.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	must(t, s.SetFlowPointer(ctx, "web", "", "siding.f", "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "siding", Runner: "manual",
		Config: map[string]any{"fold": 4, "fold_after": "8h"}}, "me"))
	if _, err := s.AddRoute(ctx, store.Route{Stage: "siding", Group: "siding"}, "me"); err != nil {
		t.Fatal(err)
	}
	mkbead := func(title string) *store.Bead {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: title, Depot: "web", Stage: "siding"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a, b := mkbead("first"), mkbead("second")
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })

	must(t, d.Reconcile(ctx))
	for _, m := range []*store.Bead{a, b} {
		if got, err := s.GetBead(ctx, m.ID); err != nil || got.Status != "open" || got.Stage != "siding" {
			t.Fatalf("%s = %+v, %v; want open in siding before the due", m.ID, got, err)
		}
	}
	entered, err := s.SidingBeads(ctx, []string{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	due := entered[0].Entered
	if entered[1].Entered.After(due) {
		due = entered[1].Entered
	}
	due = due.Add(8 * time.Hour)
	if got := dispatch.SidingDue(d); got == nil || !got.Equal(due) {
		t.Fatalf("siding due = %v, want %v", got, due)
	}

	now = due.Add(time.Minute)
	must(t, d.Reconcile(ctx))
	for _, m := range []*store.Bead{a, b} {
		if got, err := s.GetBead(ctx, m.ID); err != nil || got.Status != "closed" {
			t.Fatalf("%s = %+v, %v; want closed past the due", m.ID, got, err)
		}
	}
	open, err := s.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Stage != "ready" {
		t.Fatalf("open beads = %+v, want the carrier in ready", open)
	}
	folded := eventKinds(t, s, open[0].ID, "folded")
	if len(folded) != 1 || folded[0].Data["trigger"] != "wait" || folded[0].Actor != "yard" {
		t.Fatalf("folded events = %+v, want one wait fold by yard", folded)
	}
	if got := dispatch.SidingDue(d); got != nil {
		t.Fatalf("siding due = %v, want none with the siding empty", got)
	}
}

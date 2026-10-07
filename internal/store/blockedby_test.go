package store_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// bead create --blocked-by: the bead and its blocking edges are one write,
// however many blockers there are and whether each is named by its handle or
// its UUID. The bead is never ready before its blockers close.
func TestCreateBlockedBy(t *testing.T) {
	s, ctx := trainDepot(t)
	two, err := s.GetBead(ctx, "web-2")
	if err != nil {
		t.Fatal(err)
	}
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{ID: "web-3", Title: "three", Depot: "web", BlockedBy: []string{"web-1", two.UUID}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// What create returns is the bead as stored, after the edges re-minted it.
	if got, err := s.GetBead(ctx, "web-3"); err != nil || got.Revision != b.Revision {
		t.Fatalf("create returned revision %s, the store has %+v (%v)", b.Revision, got, err)
	}
	ds, err := s.Deps(ctx, "web-3")
	if err != nil {
		t.Fatal(err)
	}
	var edges []string
	for _, d := range ds {
		edges = append(edges, d.From+" "+d.Kind+" "+d.To)
	}
	// Edges made in one write have no order among themselves.
	slices.Sort(edges)
	if !slices.Equal(edges, []string{"web-1 blocks web-3", "web-2 blocks web-3"}) {
		t.Fatalf("edges of the new bead: %v", edges)
	}
	// One created event and one dep_added for each blocker, nothing else.
	if now, _ := s.Head(ctx); now != head+3 {
		t.Fatalf("the create logged %d events, want 3", now-head)
	}
	ready := func() []string {
		t.Helper()
		bs, err := s.Ready(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, b := range bs {
			ids = append(ids, b.ID)
		}
		return ids
	}
	bs, _ := s.OpenBlockers(ctx, "web-3")
	slices.Sort(bs)
	if !slices.Equal(bs, []string{"web-1", "web-2"}) || slices.Contains(ready(), "web-3") {
		t.Fatalf("web-3 is blocked by %v, ready: %v", bs, ready())
	}
	for _, id := range []string{"web-1", "web-2"} {
		if slices.Contains(ready(), "web-3") {
			t.Fatalf("web-3 is ready with %s open", id)
		}
		if _, err := s.CloseBead(ctx, id, "done", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Contains(ready(), "web-3") {
		t.Fatalf("web-3 is not ready with its blockers closed: %v", ready())
	}
}

// A blocker that does not exist, or one named twice, creates nothing: no
// bead, no event, and no edge on a blocker that came before it in the list.
// CheckBlockers gives the same answer without creating.
func TestCreateBlockedByRefused(t *testing.T) {
	s, ctx := trainDepot(t)
	one, err := s.GetBead(ctx, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	for _, tc := range []struct {
		name     string
		blockers []string
		refused  func(error) bool
	}{
		{"unknown", []string{"web-nope"}, func(err error) bool { return errors.Is(err, store.ErrNotFound) }},
		{"unknown after a known one", []string{"web-1", "web-nope"}, func(err error) bool { return errors.Is(err, store.ErrNotFound) }},
		{"named twice", []string{"web-1", one.UUID}, func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "web-1 is named twice")
		}},
	} {
		if err := s.CheckBlockers(ctx, tc.blockers); !tc.refused(err) {
			t.Fatalf("%s: CheckBlockers: %v", tc.name, err)
		}
		_, err := s.CreateBead(ctx, store.NewBead{ID: "web-9", Title: "orphan", Depot: "web", BlockedBy: tc.blockers}, "me")
		if !tc.refused(err) || !strings.Contains(err.Error(), "blocked by: ") {
			t.Fatalf("%s: create: %v", tc.name, err)
		}
		if _, err := s.GetBead(ctx, "web-9"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: the bead was created without its edges: %v", tc.name, err)
		}
		after, _ := s.ListBeads(ctx, store.BeadFilter{All: true})
		if now, _ := s.Head(ctx); len(after) != len(before) || now != head {
			t.Fatalf("%s: a refused create left %d beads (were %d) and %d events", tc.name, len(after), len(before), now-head)
		}
		if got, _ := s.GetBead(ctx, "web-1"); got.Revision != one.Revision {
			t.Fatalf("%s: a refused create touched web-1", tc.name)
		}
		if ds, _ := s.Deps(ctx, "web-1"); len(ds) != 0 {
			t.Fatalf("%s: a refused create left edges on web-1: %v", tc.name, ds)
		}
	}
	if err := s.CheckBlockers(ctx, []string{"web-1", "web-2"}); err != nil {
		t.Fatalf("CheckBlockers of two beads: %v", err)
	}
}

// AllDeps lists the yard's edges each once, whichever beads they touch and
// whether those are open: what Deps lists for a bead from both its ends.
func TestAllDeps(t *testing.T) {
	s, ctx := trainDepot(t)
	if ds, err := s.AllDeps(ctx); err != nil || ds == nil || len(ds) != 0 {
		t.Fatalf("a yard with no edge: %v (%v), want an empty list", ds, err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-3", Title: "three", Depot: "web", BlockedBy: []string{"web-1", "web-2"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "web-1", "web-2", store.DepDiscoveredFrom, "me"); err != nil {
		t.Fatal(err)
	}
	// An edge stays when a bead at its end closes.
	if _, err := s.CloseBead(ctx, "web-1", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	ds, err := s.AllDeps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var edges, byBead []string
	for _, d := range ds {
		edges = append(edges, d.From+" "+d.Kind+" "+d.To)
		if d.CreatedAt.IsZero() {
			t.Errorf("edge %+v has no time", d)
		}
	}
	slices.Sort(edges)
	want := []string{"web-1 blocks web-3", "web-1 discovered-from web-2", "web-2 blocks web-3"}
	if !slices.Equal(edges, want) {
		t.Fatalf("edges of the yard: %v, want %v", edges, want)
	}
	// The same edges as every bead's own list has, without the repeats.
	for _, id := range []string{"web-1", "web-2", "web-3"} {
		own, err := s.Deps(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range own {
			byBead = append(byBead, d.From+" "+d.Kind+" "+d.To)
		}
	}
	slices.Sort(byBead)
	if got := slices.Compact(byBead); !slices.Equal(got, want) {
		t.Fatalf("the beads' own edges: %v, want %v", got, want)
	}
}

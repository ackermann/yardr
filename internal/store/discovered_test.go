package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// discovery is s.Discovery(id) as two lists of handles.
func discovery(t *testing.T, s store.API, id string) (from, ledTo []string) {
	t.Helper()
	d, err := s.Discovery(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range d.From {
		from = append(from, r.ID)
	}
	for _, r := range d.LedTo {
		ledTo = append(ledTo, r.ID)
	}
	return from, ledTo
}

// bead create --discovered-from: the bead and its edge are one write. The
// edge runs origin -> found bead, the origin may be closed and in another
// depot, and the found bead takes nothing from it.
func TestCreateDiscoveredFrom(t *testing.T) {
	s, ctx := trainDepot(t)
	if err := s.PutDepot(ctx, store.Depot{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	p0 := 0
	if _, err := s.UpdateBead(ctx, "web-1", store.BeadPatch{Priority: &p0, AddLabels: []string{"ui"}}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, "web-1", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found, err := s.CreateBead(ctx, store.NewBead{Title: "a leak", Depot: "api", Priority: 2, DiscoveredFrom: "web-1"}, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if found.Depot == nil || *found.Depot != "api" || found.Priority != 2 || len(found.Labels) != 0 || found.Stage != "new" {
		t.Fatalf("the found bead took something from its origin: %+v", found)
	}
	// What create returns is the bead as stored, after the edge re-minted it.
	if got, err := s.GetBead(ctx, found.ID); err != nil || got.Revision != found.Revision {
		t.Fatalf("create returned revision %s, the store has %+v (%v)", found.Revision, got, err)
	}
	ds, err := s.Deps(ctx, found.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].From != "web-1" || ds[0].To != found.ID || ds[0].Kind != store.DepDiscoveredFrom {
		t.Fatalf("edges of the found bead: %+v, want web-1 -discovered-from-> %s", ds, found.ID)
	}
	// One write: the bead's created event and the edge's dep_added, no more.
	if now, _ := s.Head(ctx); now != head+2 {
		t.Fatalf("create with its edge logged %d events, want 2", now-head)
	}

	// A closed origin is an origin still.
	if _, err := s.CloseBead(ctx, "web-2", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	late, err := s.CreateBead(ctx, store.NewBead{Title: "seen late", DiscoveredFrom: "web-2"}, "reviewer")
	if err != nil {
		t.Fatalf("a closed origin was refused: %v", err)
	}
	if from, _ := discovery(t, s, late.ID); !slices.Equal(from, []string{"web-2"}) {
		t.Fatalf("discovered from %v, want web-2", from)
	}
}

// An origin that does not exist refuses the create, and no bead is left
// without the edge it was asked for.
func TestCreateDiscoveredFromUnknownOrigin(t *testing.T) {
	s, ctx := trainDepot(t)
	before, err := s.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	_, err = s.CreateBead(ctx, store.NewBead{ID: "web-9", Title: "orphan", Depot: "web", DiscoveredFrom: "web-nope"}, "me")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown origin: %v, want not found", err)
	}
	if _, err := s.GetBead(ctx, "web-9"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the bead was created without its edge: %v", err)
	}
	after, _ := s.ListBeads(ctx, store.BeadFilter{All: true})
	if now, _ := s.Head(ctx); len(after) != len(before) || now != head {
		t.Fatalf("a refused create left %d beads (were %d) and %d events", len(after), len(before), now-head)
	}
}

// A discovered-from edge is a record: in either direction, with the origin
// open or closed, the same beads are ready and dispatchable, none has a
// blocker, and a train origin gets no child.
func TestDiscoveredFromNeverBlocks(t *testing.T) {
	s, ctx := trainDepot(t)
	state := func() (ready, dispatchable []string) {
		t.Helper()
		r, err := s.Ready(ctx)
		if err != nil {
			t.Fatal(err)
		}
		d, err := s.Dispatchable(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"web-e", "web-1", "web-2"} {
			if bs, _ := s.OpenBlockers(ctx, id); len(bs) != 0 {
				t.Fatalf("%s is blocked by %v", id, bs)
			}
		}
		return ids(r), dispatchableIDs(d)
	}
	wantReady, wantDispatch := state()
	same := func(when string) {
		t.Helper()
		if r, d := state(); !slices.Equal(r, wantReady) || !slices.Equal(d, wantDispatch) {
			t.Fatalf("%s: ready %v, dispatchable %v; want %v and %v", when, r, d, wantReady, wantDispatch)
		}
	}
	// Both directions, and from a train.
	for _, e := range [][2]string{{"web-1", "web-2"}, {"web-2", "web-1"}, {"web-e", "web-1"}} {
		if err := s.AddDep(ctx, e[0], e[1], store.DepDiscoveredFrom, "", "", "me"); err != nil {
			t.Fatal(err)
		}
		same(e[0] + " -discovered-from-> " + e[1])
	}
	if f := family(t, s, "web-e"); len(f.Children) != 0 {
		t.Fatalf("the train took what was found from it as a child: %+v", f)
	}
	if f := family(t, s, "web-1"); f.Train != "" || f.Base != "main" {
		t.Fatalf("a bead found from a train landed on it: %+v", f)
	}

	// Closing an origin frees nothing, because nothing waited.
	if _, err := s.CloseBead(ctx, "web-1", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	drop := func(l []string) []string {
		return slices.DeleteFunc(slices.Clone(l), func(id string) bool { return id == "web-1" })
	}
	wantReady, wantDispatch = drop(wantReady), drop(wantDispatch)
	same("origin web-1 closed")
}

// Both ends of the edge name the other, closed beads too; bead list
// --discovered-from lists what was found from a bead; dep rm removes the
// edge like any other.
func TestDiscoveryAndListing(t *testing.T) {
	s, ctx := trainDepot(t)
	var found []string
	for _, title := range []string{"first", "second"} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: title, DiscoveredFrom: "web-1"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		found = append(found, b.ID)
	}
	if _, err := s.CloseBead(ctx, found[0], "fixed", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	d, err := s.Discovery(ctx, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []store.BeadRef{{ID: found[0], Title: "first", Status: "closed"}, {ID: found[1], Title: "second", Status: "open"}}
	if !slices.Equal(d.LedTo, want) || len(d.From) != 0 {
		t.Fatalf("web-1 led to %+v (from %+v), want %+v", d.LedTo, d.From, want)
	}
	if from, ledTo := discovery(t, s, found[1]); !slices.Equal(from, []string{"web-1"}) || len(ledTo) != 0 {
		t.Fatalf("%s: discovered from %v, led to %v", found[1], from, ledTo)
	}
	// A bead with no such edge has two empty lists, not none.
	if d, err := s.Discovery(ctx, "web-2"); err != nil || d.From == nil || d.LedTo == nil || len(d.From)+len(d.LedTo) != 0 {
		t.Fatalf("web-2: %+v, %v", d, err)
	}

	list := func(f store.BeadFilter) []string {
		t.Helper()
		bs, err := s.ListBeads(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return ids(bs)
	}
	if got := list(store.BeadFilter{DiscoveredFrom: "web-1"}); !slices.Equal(got, found[1:]) {
		t.Fatalf("list --discovered-from web-1 = %v, want the open %v", got, found[1:])
	}
	if got := list(store.BeadFilter{DiscoveredFrom: "web-1", All: true}); !slices.Equal(got, found) {
		t.Fatalf("list -a --discovered-from web-1 = %v, want %v", got, found)
	}
	if got := list(store.BeadFilter{DiscoveredFrom: "web-2", All: true}); len(got) != 0 {
		t.Fatalf("nothing was found from web-2, listed %v", got)
	}
	if _, err := s.ListBeads(ctx, store.BeadFilter{DiscoveredFrom: "web-nope"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("--discovered-from of a missing bead: %v", err)
	}

	if err := s.RemoveDep(ctx, "web-1", found[1], store.DepDiscoveredFrom, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	if from, _ := discovery(t, s, found[1]); len(from) != 0 {
		t.Fatalf("after dep rm, still discovered from %v", from)
	}
	if _, ledTo := discovery(t, s, "web-1"); !slices.Equal(ledTo, found[:1]) {
		t.Fatalf("after dep rm, web-1 led to %v, want %v", ledTo, found[:1])
	}
}

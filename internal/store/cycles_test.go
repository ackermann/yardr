package store_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// beads creates one bead per handle and returns them by handle.
func beads(t *testing.T, s store.API, ids ...string) map[string]*store.Bead {
	t.Helper()
	out := map[string]*store.Bead{}
	for _, id := range ids {
		b, err := s.CreateBead(context.Background(), store.NewBead{ID: id, Title: id}, "me")
		if err != nil {
			t.Fatal(err)
		}
		out[id] = b
	}
	return out
}

// block adds blocker -blocks-> bead for each pair "blocker bead".
func block(t *testing.T, s store.API, pairs ...string) {
	t.Helper()
	for _, p := range pairs {
		from, to, _ := strings.Cut(p, " ")
		if err := s.AddDep(context.Background(), from, to, store.DepBlocks, "", "", "me"); err != nil {
			t.Fatalf("%s blocks %s: %v", from, to, err)
		}
	}
}

// cycleRefused adds from -kind-> to, which must be refused naming cycle, and
// checks that nothing was written: no edge, no event.
func cycleRefused(t *testing.T, s store.API, from, to, kind, cycle string) {
	t.Helper()
	ctx := context.Background()
	edges := func() int {
		snap, err := s.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return len(snap.Deps)
	}
	before, _ := s.EventsSince(ctx, 0, 0)
	n := edges()
	err := s.AddDep(ctx, from, to, kind, "", "", "me")
	if err == nil || !strings.Contains(err.Error(), "cycle of blockers") || !strings.Contains(err.Error(), ": "+cycle) {
		t.Fatalf("%s -%s-> %s: err = %v; want a refusal naming %q", from, kind, to, err, cycle)
	}
	after, _ := s.EventsSince(ctx, 0, 0)
	if edges() != n || len(after) != len(before) {
		t.Fatalf("the refused edge changed the store: %d edges (were %d), %d events (were %d)", edges(), n, len(after), len(before))
	}
}

func readyIDs(t *testing.T, s store.API) []string {
	t.Helper()
	bs, err := s.Ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range bs {
		ids = append(ids, b.ID)
	}
	slices.Sort(ids)
	return ids
}

// Found in an isolated yard (yardr-9wq1): after `dep add A B` and `dep add
// B A` neither bead was ever ready and nothing said why. The edge that
// would close a cycle of blockers is refused, naming the cycle in order,
// the new edge first.
func TestAddDepRefusesACycle(t *testing.T) {
	s := storetest.New(t)
	beads(t, s, "y-a", "y-b", "y-c")
	block(t, s, "y-a y-b")
	cycleRefused(t, s, "y-b", "y-a", store.DepBlocks, "y-b blocks y-a blocks y-b")
	if got := readyIDs(t, s); !slices.Equal(got, []string{"y-a", "y-c"}) {
		t.Fatalf("ready = %v", got)
	}
	block(t, s, "y-b y-c")
	cycleRefused(t, s, "y-c", "y-a", store.DepBlocks, "y-c blocks y-a blocks y-b blocks y-c")
}

// Two ways from one bead to another are no cycle.
func TestAddDepAcceptsADiamond(t *testing.T) {
	s := storetest.New(t)
	beads(t, s, "y-a", "y-b", "y-c", "y-d")
	block(t, s, "y-a y-b", "y-a y-c", "y-b y-d", "y-c y-d")
	if got := readyIDs(t, s); !slices.Equal(got, []string{"y-a"}) {
		t.Fatalf("ready = %v", got)
	}
	cycleRefused(t, s, "y-d", "y-a", store.DepBlocks, "y-d blocks y-a blocks y-b blocks y-d")
}

// A parent edge brings the child's blocks edge on its train, which closes a
// cycle like one asked for: with the train already blocking the child, and
// the other way around once the bead is a child.
func TestAddDepRefusesACycleThroughAParentEdge(t *testing.T) {
	s, ctx := trainDepot(t)
	block(t, s, "web-e web-1")
	cycleRefused(t, s, "web-e", "web-1", store.DepParent, "web-1 blocks web-e blocks web-1")
	if f := family(t, s, "web-e"); len(f.Children) != 0 {
		t.Fatalf("the refused parent edge left children %v", f.Children)
	}
	if err := s.AddDep(ctx, "web-e", "web-2", store.DepParent, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	cycleRefused(t, s, "web-e", "web-2", store.DepBlocks, "web-e blocks web-2 blocks web-e")
	// Through a third bead: the child blocks the train, the train blocks web-3.
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-3", Title: "three", Depot: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	block(t, s, "web-e web-3")
	cycleRefused(t, s, "web-3", "web-2", store.DepBlocks, "web-3 blocks web-2 blocks web-e blocks web-3")
}

// Only blocks edges decide readiness: an edge of any other kind is never
// refused for a cycle, and never part of one.
func TestAddDepOtherKindsAreNoCycle(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	beads(t, s, "y-a", "y-b", "y-c")
	block(t, s, "y-a y-b")
	for _, kind := range []string{"related", "follows", store.DepDiscoveredFrom} {
		for _, e := range [][2]string{{"y-b", "y-a"}, {"y-a", "y-b"}, {"y-b", "y-c"}, {"y-c", "y-b"}} {
			if err := s.AddDep(ctx, e[0], e[1], kind, "", "", "me"); err != nil {
				t.Fatalf("%s -%s-> %s: %v", e[0], kind, e[1], err)
			}
		}
	}
	// y-b and y-c are related both ways; that is no path of blockers.
	block(t, s, "y-c y-a")
	if cycles, err := s.BlockerCycles(ctx); err != nil || len(cycles) != 0 {
		t.Fatalf("cycles = %v, %v", cycles, err)
	}
}

// A closed blocker blocks nothing, but it counts: reopening does not look
// at edges, so a cycle through a closed bead would turn live unseen.
func TestAddDepCountsClosedBeads(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	beads(t, s, "y-a", "y-b")
	block(t, s, "y-a y-b")
	if _, err := s.CloseBead(ctx, "y-a", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	cycleRefused(t, s, "y-b", "y-a", store.DepBlocks, "y-b blocks y-a blocks y-b (y-a closed, and would block again once reopened)")
}

// The walk is part of the write: of two adds that close a cycle between
// them, one is refused, whichever comes second.
func TestAddDepConcurrentAddsCannotBothPass(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	for round := range 20 {
		a, b := "y-a"+string(rune('a'+round)), "y-b"+string(rune('a'+round))
		beads(t, s, a, b)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, e := range [][2]string{{a, b}, {b, a}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = s.AddDep(ctx, e[0], e[1], store.DepBlocks, "", "", "me")
			}()
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatalf("round %d: errs = %v; want exactly one refused", round, errs)
		}
	}
	if cycles, err := s.BlockerCycles(ctx); err != nil || len(cycles) != 0 {
		t.Fatalf("cycles = %v, %v", cycles, err)
	}
}

// A store from before the check may hold cycles. They are found among the
// beads that are not closed, each named from its smallest handle; a chain
// that leads into one or out of it is not part of it. Nothing is removed
// for the user: dep rm on one edge ends the cycle.
func TestBlockerCyclesInAnOldStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	ctx := context.Background()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	bs := beads(t, s, "y-a", "y-b", "y-c", "y-d", "y-e", "y-f", "y-g", "y-in", "y-out")
	// y-b > y-c > y-a > y-b with y-in before and y-out after; y-d <> y-e;
	// y-f <> y-g, with y-g closed.
	block(t, s, "y-in y-b", "y-b y-c", "y-c y-a", "y-a y-out", "y-d y-e", "y-f y-g")
	if _, err := s.CloseBead(ctx, "y-g", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range [][2]string{{"y-a", "y-b"}, {"y-e", "y-d"}, {"y-g", "y-f"}} {
		storetest.OldBlocks(t, path, bs[e[0]].UUID, bs[e[1]].UUID)
	}
	s = storetest.Open(t, path)

	cycles := func() [][]string {
		t.Helper()
		cs, err := s.BlockerCycles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	want := [][]string{{"y-a", "y-b", "y-c", "y-a"}, {"y-d", "y-e", "y-d"}}
	if got := cycles(); !slices.EqualFunc(got, want, slices.Equal[[]string]) {
		t.Fatalf("cycles = %v, want %v", got, want)
	}
	if got := readyIDs(t, s); !slices.Equal(got, []string{"y-f", "y-in"}) {
		t.Fatalf("ready = %v", got)
	}
	// Reopening the closed bead of a cycle that store held makes it live.
	if _, err := s.Reopen(ctx, "y-g", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := cycles(); len(got) != 3 || !slices.Equal(got[2], []string{"y-f", "y-g", "y-f"}) {
		t.Fatalf("after the reopen, cycles = %v", got)
	}
	for _, e := range [][2]string{{"y-a", "y-b"}, {"y-d", "y-e"}, {"y-g", "y-f"}} {
		if err := s.RemoveDep(ctx, e[0], e[1], store.DepBlocks, "", "", "me"); err != nil {
			t.Fatal(err)
		}
	}
	if got := cycles(); len(got) != 0 {
		t.Fatalf("after dep rm, cycles = %v", got)
	}
	if got := readyIDs(t, s); !slices.Equal(got, []string{"y-e", "y-f", "y-in"}) {
		t.Fatalf("after dep rm, ready = %v", got)
	}
}

package store_test

import (
	"slices"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// HoldingDeps lists the blocks edges into a bead that hold it now, with the
// stages they name: the xq35 pair holds one edge in new and both in
// approved, and release (a blocker in the until stage, a closed blocker)
// takes an edge out. Other kinds never hold.
func TestHoldingDepsListsHoldingEdges(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	add := func(from, to, kind, at, until string) {
		t.Helper()
		if err := s.AddDep(ctx, from, to, kind, at, until, "me"); err != nil {
			t.Fatal(err)
		}
	}
	add("y-block", "y-target", store.DepBlocks, "", "review")
	add("y-block", "y-target", store.DepBlocks, "approved", "")
	add("y-plain", "y-target", "related", "", "")
	conds := func() [][2]string {
		t.Helper()
		ds, err := s.HoldingDeps(ctx, "y-target")
		if err != nil {
			t.Fatal(err)
		}
		var out [][2]string
		for _, d := range ds {
			if d.From != "y-block" || d.To != "y-target" || d.Kind != store.DepBlocks {
				t.Fatalf("HoldingDeps = %+v, want only the blocks pair", ds)
			}
			out = append(out, [2]string{d.At, d.Until})
		}
		return out
	}
	// In new only the until edge holds; the at edge and the record do not.
	if got := conds(); !slices.Equal(got, [][2]string{{"", "review"}}) {
		t.Fatalf("holding in new = %v, want the until edge only", got)
	}
	// In approved both hold, the oldest first.
	if _, err := s.Advance(ctx, "y-target", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, "y-target", "approved", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := conds(); !slices.Equal(got, [][2]string{{"", "review"}, {"approved", ""}}) {
		t.Fatalf("holding in approved = %v, want the pair, oldest first", got)
	}
	// A blocker in the until stage releases that edge; closing it releases
	// the other too.
	if _, err := s.Advance(ctx, "y-block", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := conds(); !slices.Equal(got, [][2]string{{"approved", ""}}) {
		t.Fatalf("holding with the blocker in review = %v, want the at edge only", got)
	}
	if _, err := s.CloseBead(ctx, "y-block", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := conds(); len(got) != 0 {
		t.Fatalf("holding with the blocker closed = %v, want none", got)
	}
	// An unknown bead is refused, as Deps refuses it.
	if _, err := s.HoldingDeps(ctx, "y-ghost"); err == nil {
		t.Fatal("HoldingDeps(y-ghost) = nil, want an error")
	}
}

// HoldingDeps names the same blockers OpenBlockers does: the edges behind
// its handles.
func TestHoldingDepsMatchesOpenBlockers(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "y-plain", "y-target", store.DepBlocks, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	ds, err := s.HoldingDeps(ctx, "y-target")
	if err != nil {
		t.Fatal(err)
	}
	bs, err := s.OpenBlockers(ctx, "y-target")
	if err != nil {
		t.Fatal(err)
	}
	var from []string
	for _, d := range ds {
		from = append(from, d.From)
	}
	slices.Sort(from)
	slices.Sort(bs)
	if !slices.Equal(from, bs) {
		t.Fatalf("HoldingDeps from = %v, OpenBlockers = %v", from, bs)
	}
}

// Waiting lists every holding edge of the yard: one entry per holding edge,
// each bead's oldest first, beads by priority, then creation time. A held
// bead, a journal and a memory never wait; an assigned bead waits like an
// unassigned one.
func TestWaitingListsHoldingEdges(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	add := func(from, to, kind, at, until string) {
		t.Helper()
		if err := s.AddDep(ctx, from, to, kind, at, until, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// y-target is assigned before anything holds it, and waits all the
	// same once it does.
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "y-target", "g", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	// The xq35 pair on y-target (in new: only the until edge holds), a
	// plain edge on y-plain, and a record.
	add("y-block", "y-target", store.DepBlocks, "", "review")
	add("y-block", "y-target", store.DepBlocks, "approved", "")
	add("y-block", "y-plain", store.DepBlocks, "", "")
	add("y-plain", "y-target", "related", "", "")
	// A held bead with an open blocker, a journal, a memory and a question
	// with one: none waits.
	for _, nb := range []store.NewBead{
		{ID: "y-held", Title: "held", Depot: "yard", Stage: "new"},
		{ID: "y-journal", Title: "journal", Type: store.TypeJournal, Depot: "yard", Stage: "new"},
		{ID: "y-memory", Title: "memory", Type: store.TypeMemory, Depot: "yard", Stage: "new"},
		{ID: "y-question", Title: "question", Type: store.TypeQuestion, Depot: "yard", Stage: "new"},
	} {
		if _, err := s.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
		add("y-block", nb.ID, store.DepBlocks, "", "")
	}
	if _, err := s.SetHold(ctx, "y-held", true, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// y-plain outranks y-target, so its edge comes first.
	one := 1
	if _, err := s.UpdateBead(ctx, "y-target", store.BeadPatch{Priority: &one}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	ds, err := s.Waiting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type wait struct{ to, from, at, until string }
	var got []wait
	for _, d := range ds {
		got = append(got, wait{d.To, d.From, d.At, d.Until})
	}
	want := []wait{
		{"y-plain", "y-block", "", ""},
		{"y-target", "y-block", "", "review"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Waiting = %+v, want %+v", got, want)
	}
}

// Waiting is the complement of Ready on the gate: an open, unheld bead of
// a working type waits exactly when it is not ready.
func TestWaitingComplementsReady(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatal(err)
	}
	ready, err := s.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := s.Waiting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	isReady := map[string]bool{}
	for _, b := range ready {
		isReady[b.ID] = true
	}
	waits := map[string]bool{}
	for _, d := range ds {
		waits[d.To] = true
	}
	// In new y-target waits on the until edge alone; in approved on both.
	for _, tc := range []struct {
		stage string
		want  int
	}{
		{"new", 1},
		{"approved", 2},
	} {
		if tc.stage == "approved" {
			if _, err := s.Advance(ctx, "y-target", "review", store.Expect{}, "me"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Advance(ctx, "y-target", "approved", store.Expect{}, "me"); err != nil {
				t.Fatal(err)
			}
			ready, err = s.Ready(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ds, err = s.Waiting(ctx)
			if err != nil {
				t.Fatal(err)
			}
			isReady = map[string]bool{}
			for _, b := range ready {
				isReady[b.ID] = true
			}
			waits = map[string]bool{}
			for _, d := range ds {
				waits[d.To] = true
			}
		}
		if isReady["y-target"] || !waits["y-target"] {
			t.Fatalf("in %s: ready=%v waits=%v, want waiting and not ready", tc.stage, isReady["y-target"], waits["y-target"])
		}
		n := 0
		for _, d := range ds {
			if d.To == "y-target" {
				n++
			}
		}
		if n != tc.want {
			t.Fatalf("in %s: %d waiting edges on y-target, want %d", tc.stage, n, tc.want)
		}
		if !isReady["y-block"] || waits["y-block"] || !isReady["y-plain"] || waits["y-plain"] {
			t.Fatalf("blocker and plain bead: ready=%v/%v waits=%v/%v, want ready and not waiting",
				isReady["y-block"], isReady["y-plain"], waits["y-block"], waits["y-plain"])
		}
	}
}

package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// stagedYard is a depot governed by the yardFlow lifecycle, with an open
// blocker, a target and a control bead, all in new.
func stagedYard(t *testing.T) (store.API, context.Context) {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", yardFlow)
	for _, nb := range []store.NewBead{
		{ID: "y-block", Title: "blocker", Depot: "yard", Stage: "new"},
		{ID: "y-target", Title: "target", Depot: "yard", Stage: "new"},
		{ID: "y-plain", Title: "control", Depot: "yard", Stage: "new"},
	} {
		if _, err := s.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx
}

func readyHas(t *testing.T, s store.API, ctx context.Context, id string) bool {
	t.Helper()
	bs, err := s.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(bs, func(b store.Bead) bool { return b.ID == id })
}

func dispatchHas(t *testing.T, s store.API, ctx context.Context, id string) bool {
	t.Helper()
	bs, err := s.Dispatchable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(bs, func(b store.ReadyBead) bool { return b.Bead == id })
}

func blockersOf(t *testing.T, s store.API, ctx context.Context, id string) []string {
	t.Helper()
	bs, err := s.OpenBlockers(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

// An --at approved edge stores and lists: Deps from both ends, AllDeps and
// the snapshot carry it.
func TestStagedBlocksStoresAndLists(t *testing.T) {
	t.Parallel()
	s, ctx := stagedYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"y-target", "y-block"} {
		ds, err := s.Deps(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(ds) != 1 || ds[0].From != "y-block" || ds[0].To != "y-target" ||
			ds[0].Kind != store.DepBlocks || ds[0].At != "approved" {
			t.Fatalf("Deps(%s) = %+v, want the staged edge", id, ds)
		}
	}
	all, err := s.AllDeps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].At != "approved" {
		t.Fatalf("AllDeps = %+v, want the staged edge", all)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Deps) != 1 || snap.Deps[0].At != "approved" {
		t.Fatalf("snapshot deps = %+v, want the staged edge", snap.Deps)
	}
}

// A target held only at approved routes in new and review and waits in
// approved; leaving the stage routes it again. A plain edge holds its
// target in every stage, as before.
func TestStagedBlocksHoldsOnlyInItsStage(t *testing.T) {
	t.Parallel()
	s, ctx := stagedYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "y-block", "y-plain", store.DepBlocks, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	held := func(stage string, want bool) {
		t.Helper()
		if got := blockersOf(t, s, ctx, "y-target"); want != slices.Equal(got, []string{"y-block"}) {
			t.Fatalf("in %s: blocked by %v, want held=%v", stage, got, want)
		}
		if ready, dispatch := readyHas(t, s, ctx, "y-target"), dispatchHas(t, s, ctx, "y-target"); ready == want || dispatch == want {
			t.Fatalf("in %s: ready=%v dispatchable=%v, want held=%v", stage, ready, dispatch, want)
		}
		// The plain edge holds whatever the stage: the control never routes.
		if readyHas(t, s, ctx, "y-plain") || dispatchHas(t, s, ctx, "y-plain") {
			t.Fatalf("in %s: the plain-blocked control routes", stage)
		}
		if got := blockersOf(t, s, ctx, "y-plain"); !slices.Equal(got, []string{"y-block"}) {
			t.Fatalf("in %s: the control is blocked by %v", stage, got)
		}
	}
	held("new", false)
	for _, to := range []string{"review", "approved"} {
		if _, err := s.Advance(ctx, "y-target", to, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		held(to, to == "approved")
	}
	// Out of the stage, the target routes again with its blocker still open.
	if _, err := s.Advance(ctx, "y-target", "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	held("new", false)
}

// Closing the blocker releases a held target, whatever the reason, and
// reopening it holds the target again.
func TestStagedBlocksCloseAndReopen(t *testing.T) {
	t.Parallel()
	s, ctx := stagedYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"review", "approved"} {
		if _, err := s.Advance(ctx, "y-target", to, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if readyHas(t, s, ctx, "y-target") {
		t.Fatal("the target routes in approved with its blocker open")
	}
	if _, err := s.CloseBead(ctx, "y-block", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if !readyHas(t, s, ctx, "y-target") {
		t.Fatal("closing the blocker did not release the target")
	}
	if _, err := s.Reopen(ctx, "y-block", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if readyHas(t, s, ctx, "y-target") {
		t.Fatal("reopening the blocker did not hold the target again")
	}
	// Won't-do releases exactly like merging: the blocker is closed either way.
	if _, err := s.CloseBead(ctx, "y-block", "wontdo", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if !readyHas(t, s, ctx, "y-target") {
		t.Fatal("won't-doing the blocker did not release the target")
	}
}

// The duplicate key is (from, to, kind, at): the same pair may hold in
// several stages, but not twice in one. The cycle check stays on
// (from, to): a staged edge closes a cycle like a plain one.
func TestStagedBlocksDuplicateKeyAndCycles(t *testing.T) {
	t.Parallel()
	s, ctx := stagedYard(t)
	for _, at := range []string{"", "review", "approved"} {
		if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, at, "", "me"); err != nil {
			t.Fatalf("at %q: %v", at, err)
		}
	}
	for _, at := range []string{"", "review", "approved"} {
		err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, at, "", "me")
		if err == nil {
			t.Fatalf("at %q: a duplicate edge was accepted", at)
		}
		if at != "" && !strings.Contains(err.Error(), "at "+at) {
			t.Fatalf("at %q: the refusal names no stage: %v", at, err)
		}
	}
	if ds, _ := s.Deps(ctx, "y-target"); len(ds) != 3 {
		t.Fatalf("Deps(y-target) = %+v, want three edges", ds)
	}

	beads(t, s, "y-a", "y-b")
	if err := s.AddDep(ctx, "y-a", "y-b", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatal(err)
	}
	// Either way back closes the cycle, staged or plain.
	cycleRefused(t, s, "y-b", "y-a", store.DepBlocks, "y-b blocks y-a blocks y-b")
	if err := s.AddDep(ctx, "y-a", "y-b", store.DepBlocks, "review", "", "me"); err != nil {
		t.Fatal(err)
	}
	cycleRefused(t, s, "y-b", "y-a", store.DepBlocks, "y-b blocks y-a blocks y-b")
}

// --at is refused for any other kind, and for a stage the target's flow
// has not got. Where no flow governs the bead, any well-formed stage name
// is accepted.
func TestStagedBlocksRefusals(t *testing.T) {
	t.Parallel()
	s, ctx := stagedYard(t)
	for _, kind := range []string{"related", store.DepParent, store.DepDiscoveredFrom} {
		err := s.AddDep(ctx, "y-block", "y-target", kind, "approved", "", "me")
		if err == nil || !strings.Contains(err.Error(), "--at") || !strings.Contains(err.Error(), "only kind blocks") {
			t.Fatalf("kind %s with --at: err = %v, want a refusal for the kind", kind, err)
		}
		err = s.RemoveDep(ctx, "y-block", "y-target", kind, "approved", "", "me")
		if err == nil || !strings.Contains(err.Error(), "--at") || !strings.Contains(err.Error(), "only kind blocks") {
			t.Fatalf("kind %s with dep rm --at: err = %v, want a refusal for the kind", kind, err)
		}
	}
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "launch", "", "me"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("--at launch: err = %v, want a refusal naming the stage", err)
	}
	if ds, _ := s.Deps(ctx, "y-target"); len(ds) != 0 {
		t.Fatalf("refused edges left %+v", ds)
	}

	// No flow governs beads without a depot: any well-formed name goes.
	beads(t, s, "y-c", "y-d")
	if err := s.AddDep(ctx, "y-c", "y-d", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatalf("no flow, --at approved: %v", err)
	}
	if err := s.AddDep(ctx, "y-c", "y-d", store.DepBlocks, "not a stage", "", "me"); err == nil {
		t.Fatal("no flow, --at 'not a stage': accepted")
	}
}

// dep rm --at removes exactly that edge: the plain edge and the other
// staged edges stay, and readiness follows what is left.
func TestStagedBlocksRemoveExactly(t *testing.T) {
	t.Parallel()
	s, ctx := stagedYard(t)
	for _, at := range []string{"", "review", "approved"} {
		if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, at, "", "me"); err != nil {
			t.Fatal(err)
		}
	}
	at := func() []string {
		t.Helper()
		ds, err := s.Deps(ctx, "y-target")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range ds {
			out = append(out, d.At)
		}
		slices.Sort(out)
		return out
	}
	// Removing the plain edge leaves the staged ones, still holding nowhere
	// while the target sits in new.
	if err := s.RemoveDep(ctx, "y-block", "y-target", store.DepBlocks, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	if got := at(); !slices.Equal(got, []string{"approved", "review"}) {
		t.Fatalf("after removing the plain edge: at = %v", got)
	}
	if !readyHas(t, s, ctx, "y-target") {
		t.Fatal("the target does not route in new with only staged edges")
	}
	if err := s.RemoveDep(ctx, "y-block", "y-target", store.DepBlocks, "review", "", "me"); err != nil {
		t.Fatal(err)
	}
	if got := at(); !slices.Equal(got, []string{"approved"}) {
		t.Fatalf("after removing --at review: at = %v", got)
	}
	// Removing what is not there is a no-op.
	if err := s.RemoveDep(ctx, "y-block", "y-target", store.DepBlocks, "new", "", "me"); err != nil {
		t.Fatal(err)
	}
	if got := at(); !slices.Equal(got, []string{"approved"}) {
		t.Fatalf("after removing --at new: at = %v", got)
	}
	for _, to := range []string{"review", "approved"} {
		if _, err := s.Advance(ctx, "y-target", to, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if readyHas(t, s, ctx, "y-target") {
		t.Fatal("the target routes in approved with the staged edge still there")
	}
	if err := s.RemoveDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatal(err)
	}
	if ds, _ := s.Deps(ctx, "y-target"); len(ds) != 0 {
		t.Fatalf("edges left: %+v", ds)
	}
	if !readyHas(t, s, ctx, "y-target") {
		t.Fatal("removing the staged edge did not release the target in approved")
	}
}

// A target governed by its depot's repository flow validates --at against
// that flow.
func TestStagedBlocksRepoFlow(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t, map[string]string{".yardr/flows/default.flow": yardFlow})
	s, ctx := storetest.New(t), context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "y-block", Title: "blocker", Depot: "yard", Stage: "new"},
		{ID: "y-target", Title: "target", Depot: "yard", Stage: "new"},
	} {
		if _, err := s.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatalf("--at approved under a repository flow: %v", err)
	}
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "launch", "", "me"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("--at launch under a repository flow: err = %v", err)
	}
}

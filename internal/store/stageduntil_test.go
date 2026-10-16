package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// untilYard is a depot governed by outcomeFlow, with an open blocker, a
// target and a second bead, all in new. outcomeFlow's done chain runs
// backlog -> new -> review -> approved and stops there: approved names
// outcomes but no done, and decide leaves only by explicit moves.
func untilYard(t *testing.T) (store.API, context.Context) {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, nb := range []store.NewBead{
		{ID: "y-block", Title: "blocker", Depot: "yard", Stage: "new"},
		{ID: "y-target", Title: "target", Depot: "yard", Stage: "new"},
		{ID: "y-plain", Title: "second", Depot: "yard", Stage: "new"},
	} {
		if _, err := s.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx
}

// The xq35 pair of edges stores and lists: Deps from both ends, AllDeps
// and the snapshot carry until.
func TestUntilBlocksStoresAndLists(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"y-target", "y-block"} {
		ds, err := s.Deps(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(ds) != 2 {
			t.Fatalf("Deps(%s) = %+v, want the pair", id, ds)
		}
		for _, d := range ds {
			if d.From != "y-block" || d.To != "y-target" || d.Kind != store.DepBlocks {
				t.Fatalf("Deps(%s) = %+v, want the pair", id, ds)
			}
		}
		untils := []string{ds[0].Until, ds[1].Until}
		slices.Sort(untils)
		if !slices.Equal(untils, []string{"", "review"}) {
			t.Fatalf("Deps(%s) until = %v, want review on one edge", id, untils)
		}
	}
	all, err := s.AllDeps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("AllDeps = %+v, want the pair", all)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Deps) != 2 {
		t.Fatalf("snapshot deps = %+v, want the pair", snap.Deps)
	}
	n := 0
	for _, d := range snap.Deps {
		if d.Until == "review" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("snapshot deps = %+v, want until review on one edge", snap.Deps)
	}
}

// A blocker past the until stage along done releases: the target routes
// while the blocker is still open in review and in approved, and stays
// routed once the blocker closes. Before the stage, and before a later
// until stage, the target waits.
func TestUntilBlocksReleasesAlongDone(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "y-block", "y-plain", store.DepBlocks, "", "approved", "me"); err != nil {
		t.Fatal(err)
	}
	held := func(blocker string, target, plain bool) {
		t.Helper()
		for id, want := range map[string]bool{"y-target": target, "y-plain": plain} {
			if got := blockersOf(t, s, ctx, id); (len(got) != 0) != want {
				t.Fatalf("blocker in %s: %s blocked by %v, want held=%v", blocker, id, got, want)
			}
			if ready, dispatch := readyHas(t, s, ctx, id), dispatchHas(t, s, ctx, id); ready == want || dispatch == want {
				t.Fatalf("blocker in %s: %s ready=%v dispatchable=%v, want held=%v", blocker, id, ready, dispatch, want)
			}
		}
	}
	held("new", true, true)
	for _, to := range []string{"review", "approved"} {
		if _, err := s.Advance(ctx, "y-block", to, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		held(to, false, to == "review")
	}
	// Into merged the blocker closes, and both stay routed.
	if _, err := s.Advance(ctx, "y-block", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	held("merged", false, false)
}

// The done-route has a direction: until=backlog releases through the
// implicit done to new and on, while until=new holds in backlog, which
// is upstream of it; decide is off both chains and holds both.
func TestUntilBlocksDoneRouteDirection(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "backlog", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "y-block", "y-plain", store.DepBlocks, "", "new", "me"); err != nil {
		t.Fatal(err)
	}
	held := func(blocker string, target, plain bool) {
		t.Helper()
		for id, want := range map[string]bool{"y-target": target, "y-plain": plain} {
			if got := blockersOf(t, s, ctx, id); (len(got) != 0) != want {
				t.Fatalf("blocker in %s: %s blocked by %v, want held=%v", blocker, id, got, want)
			}
			if ready := readyHas(t, s, ctx, id); ready == want {
				t.Fatalf("blocker in %s: %s ready=%v, want held=%v", blocker, id, ready, want)
			}
		}
	}
	// In new both are past their until along done.
	held("new", false, false)
	if _, err := s.Advance(ctx, "y-block", "backlog", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// backlog's implicit done reaches new; new's does not reach back.
	held("backlog", false, true)
	for _, to := range []string{"new", "decide"} {
		if _, err := s.Advance(ctx, "y-block", to, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	held("decide", true, true)
	if _, err := s.Advance(ctx, "y-block", "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	held("new", false, false)
}

// A blocker that goes back holds again at the target's next hand-over,
// without stopping a running session: the claimed target keeps its
// session and its owner, while a fresh claim of a held bead is taken.
func TestUntilBlocksGoingBackHoldsAgain(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "y-block", "y-plain", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "y-target", "builders", "new", "yard"); !errors.Is(err, store.ErrTaken) {
		t.Fatalf("claim while the blocker is in new: err = %v, want taken", err)
	}
	if _, err := s.Advance(ctx, "y-block", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, "y-target", "builders", "new", "yard")
	if err != nil {
		t.Fatalf("claim with the blocker in review: %v", err)
	}
	if err := s.MarkRunning(ctx, sess.ID, map[string]any{"agent": "x-new"}); err != nil {
		t.Fatal(err)
	}
	// Back to new on changes: the target holds again, the session runs on.
	if _, err := s.Advance(ctx, "y-block", "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := blockersOf(t, s, ctx, "y-target"); !slices.Equal(got, []string{"y-block"}) {
		t.Fatalf("after the return: the target is blocked by %v", got)
	}
	if _, err := s.Claim(ctx, "y-plain", "builders", "new", "yard"); !errors.Is(err, store.ErrTaken) {
		t.Fatalf("claim after the return: err = %v, want taken", err)
	}
	if dispatchHas(t, s, ctx, "y-plain") {
		t.Fatal("after the return: the held bead is dispatchable")
	}
	live, err := s.Sessions(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(live, func(x store.Session) bool { return x.ID == sess.ID }) {
		t.Fatal("after the return: the running session ended")
	}
	if b, _ := s.GetBead(ctx, "y-target"); b.Group == nil || *b.Group != "builders" {
		t.Fatalf("after the return: the target is owned by %+v", b.Group)
	}
}

// A single edge with both fields holds at one stage until the blocker
// reaches another: in approved the target waits on a blocker in new and
// routes once it reaches review; anywhere else it routes throughout.
func TestUntilBlocksSingleEdgeWithBoth(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "review", "me"); err != nil {
		t.Fatal(err)
	}
	held := func(target, blocker string, want bool) {
		t.Helper()
		if got := blockersOf(t, s, ctx, "y-target"); (len(got) != 0) != want {
			t.Fatalf("target in %s, blocker in %s: blocked by %v, want held=%v", target, blocker, got, want)
		}
		if ready, dispatch := readyHas(t, s, ctx, "y-target"), dispatchHas(t, s, ctx, "y-target"); ready == want || dispatch == want {
			t.Fatalf("target in %s, blocker in %s: ready=%v dispatchable=%v, want held=%v",
				target, blocker, ready, dispatch, want)
		}
	}
	held("new", "new", false)
	for _, to := range []string{"review", "approved"} {
		if _, err := s.Advance(ctx, "y-target", to, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	held("approved", "new", true)
	if _, err := s.Advance(ctx, "y-block", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	held("approved", "review", false)
	// The blocker goes back: in approved the target waits again.
	if _, err := s.Advance(ctx, "y-block", "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	held("approved", "new", true)
	// Out of the stage, the target routes with its blocker still in new.
	if _, err := s.Advance(ctx, "y-target", "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	held("new", "new", false)
}

// Closing the blocker releases a target held before the until stage,
// whatever the reason, and reopening it holds the target again.
func TestUntilBlocksCloseAndReopen(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	if readyHas(t, s, ctx, "y-target") {
		t.Fatal("the target routes with its blocker in new")
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

// The duplicate key is (from, to, kind, at, until): the same pair may
// release from several stages and hold in several, but not twice on one
// key. The cycle check stays on (from, to): a staged edge closes a cycle
// like a plain one.
func TestUntilBlocksDuplicateKeyAndCycles(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	keys := [][2]string{{"", ""}, {"", "review"}, {"approved", ""}, {"approved", "review"}}
	for _, k := range keys {
		if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, k[0], k[1], "me"); err != nil {
			t.Fatalf("at %q until %q: %v", k[0], k[1], err)
		}
	}
	for _, k := range keys {
		err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, k[0], k[1], "me")
		if err == nil {
			t.Fatalf("at %q until %q: a duplicate edge was accepted", k[0], k[1])
		}
		for flag, stage := range map[string]string{"at": k[0], "until": k[1]} {
			if stage != "" && !strings.Contains(err.Error(), flag+" "+stage) {
				t.Fatalf("at %q until %q: the refusal names no stage: %v", k[0], k[1], err)
			}
		}
	}
	if ds, _ := s.Deps(ctx, "y-target"); len(ds) != 4 {
		t.Fatalf("Deps(y-target) = %+v, want four edges", ds)
	}

	beads(t, s, "y-a", "y-b")
	if err := s.AddDep(ctx, "y-a", "y-b", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	// Either way back closes the cycle, staged or plain.
	cycleRefused(t, s, "y-b", "y-a", store.DepBlocks, "y-b blocks y-a blocks y-b")
	if err := s.AddDep(ctx, "y-a", "y-b", store.DepBlocks, "approved", "review", "me"); err != nil {
		t.Fatal(err)
	}
	cycleRefused(t, s, "y-b", "y-a", store.DepBlocks, "y-b blocks y-a blocks y-b")
}

// --until is refused for any other kind, and for a stage the blocker's
// flow has not got. Where no flow governs the bead, any well-formed stage
// name is accepted.
func TestUntilBlocksRefusals(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	for _, kind := range []string{"related", store.DepParent, store.DepDiscoveredFrom} {
		err := s.AddDep(ctx, "y-block", "y-target", kind, "", "review", "me")
		if err == nil || !strings.Contains(err.Error(), "--until") || !strings.Contains(err.Error(), "only kind blocks") {
			t.Fatalf("kind %s with --until: err = %v, want a refusal for the kind", kind, err)
		}
		err = s.RemoveDep(ctx, "y-block", "y-target", kind, "", "review", "me")
		if err == nil || !strings.Contains(err.Error(), "--until") || !strings.Contains(err.Error(), "only kind blocks") {
			t.Fatalf("kind %s with dep rm --until: err = %v, want a refusal for the kind", kind, err)
		}
	}
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "launch", "me"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("--until launch: err = %v, want a refusal naming the stage", err)
	}
	if ds, _ := s.Deps(ctx, "y-target"); len(ds) != 0 {
		t.Fatalf("refused edges left %+v", ds)
	}

	// No flow governs beads without a depot: any well-formed name goes.
	beads(t, s, "y-c", "y-d")
	if err := s.AddDep(ctx, "y-c", "y-d", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatalf("no flow, --until review: %v", err)
	}
	if err := s.AddDep(ctx, "y-c", "y-d", store.DepBlocks, "", "not a stage", "me"); err == nil {
		t.Fatal("no flow, --until 'not a stage': accepted")
	}
}

// --until validates against the blocker's flow and --at against the
// target's, across depots: each end reads its own.
func TestUntilBlocksEachEndReadsItsOwnFlow(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	if err := s.PutDepot(ctx, store.Depot{Name: "plain", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "plain", "", "backlog -> new\nnew -> merged\nmerged terminal\n")
	for _, nb := range []store.NewBead{
		{ID: "p-block", Title: "blocker", Depot: "plain", Stage: "new"},
		{ID: "p-target", Title: "target", Depot: "plain", Stage: "new"},
	} {
		if _, err := s.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// The blocker's flow lacks review: --until review is refused, whatever
	// the target's.
	if err := s.AddDep(ctx, "p-block", "y-target", store.DepBlocks, "", "review", "me"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("blocker without review: err = %v, want a refusal", err)
	}
	// The target's flow lacks review: --at review is refused, whatever the
	// blocker's.
	if err := s.AddDep(ctx, "y-block", "p-target", store.DepBlocks, "review", "", "me"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("target without review: err = %v, want a refusal", err)
	}
	// Each flag reads its own end: both of these go.
	if err := s.AddDep(ctx, "y-block", "p-target", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatalf("blocker with review: %v", err)
	}
	if err := s.AddDep(ctx, "p-block", "y-target", store.DepBlocks, "review", "", "me"); err != nil {
		t.Fatalf("target with review: %v", err)
	}
}

// Without a flow to walk, only the until stage itself releases: the
// blocker holds everywhere else, whatever the name.
func TestUntilBlocksNoFlowMatchesExactly(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	bs := beads(t, s, "y-c", "y-d")
	stage := bs["y-c"].Stage
	if err := s.AddDep(ctx, "y-c", "y-d", store.DepBlocks, "", stage, "me"); err != nil {
		t.Fatal(err)
	}
	if !readyHas(t, s, ctx, "y-d") {
		t.Fatalf("the target waits with its blocker in %s", stage)
	}
	// No flow governs the blocker, so it may move to any stage.
	other := "elsewhere"
	if stage == other {
		other = "somewhere"
	}
	if _, err := s.Advance(ctx, "y-c", other, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if readyHas(t, s, ctx, "y-d") {
		t.Fatalf("the target routes with its blocker in %s", other)
	}
	if got := blockersOf(t, s, ctx, "y-d"); !slices.Equal(got, []string{"y-c"}) {
		t.Fatalf("the target is blocked by %v", got)
	}
}

// dep rm --until removes exactly that edge: with the at half of the key,
// each combination removes alone, and readiness follows what is left.
func TestUntilBlocksRemoveExactly(t *testing.T) {
	t.Parallel()
	s, ctx := untilYard(t)
	keys := [][2]string{{"", ""}, {"", "review"}, {"approved", ""}, {"approved", "review"}}
	for _, k := range keys {
		if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, k[0], k[1], "me"); err != nil {
			t.Fatal(err)
		}
	}
	stages := func() [][2]string {
		t.Helper()
		ds, err := s.Deps(ctx, "y-target")
		if err != nil {
			t.Fatal(err)
		}
		var out [][2]string
		for _, d := range ds {
			out = append(out, [2]string{d.At, d.Until})
		}
		slices.SortFunc(out, func(a, b [2]string) int {
			if c := strings.Compare(a[0], b[0]); c != 0 {
				return c
			}
			return strings.Compare(a[1], b[1])
		})
		return out
	}
	// Removing the plain edge leaves the staged ones, still holding in new
	// through until: the blocker has reached neither review nor approved.
	if err := s.RemoveDep(ctx, "y-block", "y-target", store.DepBlocks, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"", "review"}, {"approved", ""}, {"approved", "review"}}
	if got := stages(); !slices.Equal(got, want) {
		t.Fatalf("after removing the plain edge: stages = %v", got)
	}
	if readyHas(t, s, ctx, "y-target") {
		t.Fatal("the target routes in new with until edges still there")
	}
	// --at alone does not match an edge that also names until.
	if err := s.RemoveDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "", "me"); err != nil {
		t.Fatal(err)
	}
	want = [][2]string{{"", "review"}, {"approved", "review"}}
	if got := stages(); !slices.Equal(got, want) {
		t.Fatalf("after removing --at approved: stages = %v", got)
	}
	// --until alone does not match an edge that also names at.
	if err := s.RemoveDep(ctx, "y-block", "y-target", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	want = [][2]string{{"approved", "review"}}
	if got := stages(); !slices.Equal(got, want) {
		t.Fatalf("after removing --until review: stages = %v", got)
	}
	// Removing what is not there is a no-op.
	if err := s.RemoveDep(ctx, "y-block", "y-target", store.DepBlocks, "", "approved", "me"); err != nil {
		t.Fatal(err)
	}
	if got := stages(); !slices.Equal(got, want) {
		t.Fatalf("after removing --until approved: stages = %v", got)
	}
	// The last edge holds nowhere while the target sits in new; in
	// approved it holds until the blocker reaches review.
	for _, to := range []string{"review", "approved"} {
		if _, err := s.Advance(ctx, "y-target", to, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if readyHas(t, s, ctx, "y-target") {
		t.Fatal("the target routes in approved with the staged edge still there")
	}
	if err := s.RemoveDep(ctx, "y-block", "y-target", store.DepBlocks, "approved", "review", "me"); err != nil {
		t.Fatal(err)
	}
	if ds, _ := s.Deps(ctx, "y-target"); len(ds) != 0 {
		t.Fatalf("edges left: %+v", ds)
	}
	if !readyHas(t, s, ctx, "y-target") {
		t.Fatal("removing the staged edge did not release the target in approved")
	}
}

// A blocker governed by its depot's repository flow validates --until
// against that flow, and readiness and claims resolve it through the
// repository: the target routes once the blocker reaches review.
func TestUntilBlocksRepoFlow(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t, map[string]string{".yardr/flows/default.flow": outcomeFlow})
	s, ctx := storetest.New(t), context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "manual"}, "me"); err != nil {
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
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatalf("--until review under a repository flow: %v", err)
	}
	if err := s.AddDep(ctx, "y-block", "y-target", store.DepBlocks, "", "launch", "me"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("--until launch under a repository flow: err = %v", err)
	}
	if readyHas(t, s, ctx, "y-target") {
		t.Fatal("the target routes with its blocker in new")
	}
	if _, err := s.Claim(ctx, "y-target", "builders", "new", "yard"); !errors.Is(err, store.ErrTaken) {
		t.Fatalf("claim with the blocker in new: err = %v, want taken", err)
	}
	if _, err := s.Advance(ctx, "y-block", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if !readyHas(t, s, ctx, "y-target") {
		t.Fatal("the blocker reached review and the target still waits")
	}
	if _, err := s.Claim(ctx, "y-target", "builders", "new", "yard"); err != nil {
		t.Fatalf("claim with the blocker in review: %v", err)
	}
}

package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// waitingYard is a depot governed by a four-stage flow with a blocker, a
// target carrying the xq35 pair of edges, and a second bead behind a plain
// edge, all in new. The target outranks the second bead, so listings name
// it first.
func waitingYard(t *testing.T) (store.API, context.Context) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("backlog -> new\nnew -> review\nreview -> approved\napproved -> merged\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yard", "", "yard", "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-1", Title: "blocker", Depot: "yard", Stage: "new"},
		{ID: "yard-2", Title: "target", Depot: "yard", Stage: "new"},
		{ID: "yard-3", Title: "second", Depot: "yard", Stage: "new", Priority: 1},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	add := func(from, to, at, until string) {
		t.Helper()
		if err := st.AddDep(ctx, from, to, store.DepBlocks, at, until, "me"); err != nil {
			t.Fatal(err)
		}
	}
	add("yard-1", "yard-2", "", "review")
	add("yard-1", "yard-2", "approved", "")
	add("yard-1", "yard-3", "", "")
	return st, ctx
}

// dep list prints the stages a staged edge names, and a plain edge exactly
// as before.
func TestDepListPrintsStages(t *testing.T) {
	t.Parallel()
	st, _ := waitingYard(t)
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.depCmd(), "list", "yard-2")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	slices.Sort(lines)
	want := []string{
		"yard-1 -blocks-> yard-2 at approved",
		"yard-1 -blocks-> yard-2 until review",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("dep list yard-2 = %q, want %q", lines, want)
	}
	out, err = run(t, a.depCmd(), "list", "yard-3")
	if err != nil {
		t.Fatal(err)
	}
	if out != "yard-1 -blocks-> yard-3\n" {
		t.Fatalf("dep list yard-3 = %q, want the plain edge", out)
	}
}

// bead show gains blocked-by lines for the edges holding the bead, with
// the stages they name; an edge that holds nothing prints no line.
func TestBeadShowPrintsBlockedBy(t *testing.T) {
	t.Parallel()
	st, ctx := waitingYard(t)
	a := &app{st: st, actor: "me"}
	show := func(id string) string {
		t.Helper()
		out, err := run(t, a.beadCmd(), "show", id)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// In new only the until edge holds.
	if out := show("yard-2"); !strings.Contains(out, "  blocked by yard-1 until review\n") ||
		strings.Contains(out, "blocked by yard-1 at approved") {
		t.Fatalf("bead show yard-2 in new misses the until line:\n%s", out)
	}
	// In approved both hold.
	if _, err := st.Advance(ctx, "yard-2", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Advance(ctx, "yard-2", "approved", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if out := show("yard-2"); !strings.Contains(out, "  blocked by yard-1 until review\n") ||
		!strings.Contains(out, "  blocked by yard-1 at approved\n") {
		t.Fatalf("bead show yard-2 in approved misses a line:\n%s", out)
	}
	// A released edge and a bead with no edges print none.
	if _, err := st.Advance(ctx, "yard-1", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if out := show("yard-2"); strings.Contains(out, "until review") {
		t.Fatalf("bead show yard-2 with the blocker in review still names until:\n%s", out)
	}
	if out := show("yard-1"); strings.Contains(out, "blocked by") {
		t.Fatalf("bead show yard-1 names a blocker it has not got:\n%s", out)
	}
}

// prime's BLOCKED BY carries the conditions, and its Edges print them as
// dep list does; a plain blocker reads exactly as before.
func TestPrimeBlockedByCarriesConditions(t *testing.T) {
	t.Parallel()
	st, ctx := waitingYard(t)
	a := &app{st: st, actor: "me"}
	prime := func(id string) string {
		t.Helper()
		out, err := run(t, a.primeCmd(), "--bead", id)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	if out := prime("yard-3"); !strings.Contains(out, "\nBLOCKED BY (open): yard-1\n") {
		t.Fatalf("prime yard-3 misses the plain line:\n%s", out)
	}
	if _, err := st.Advance(ctx, "yard-2", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Advance(ctx, "yard-2", "approved", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	out := prime("yard-2")
	if !strings.Contains(out, "\nBLOCKED BY (open): yard-1 until review, yard-1 at approved\n") {
		t.Fatalf("prime yard-2 misses the conditions:\n%s", out)
	}
	if !strings.Contains(out, "  yard-1 -blocks-> yard-2 until review\n") ||
		!strings.Contains(out, "  yard-1 -blocks-> yard-2 at approved\n") {
		t.Fatalf("prime yard-2 edges miss the conditions:\n%s", out)
	}
}

// ready --waiting lists one line per holding edge with why, in the bead's
// words; a held bead and a journal never wait. ready without the flag is
// the table it always was.
func TestReadyWaitingListsWhy(t *testing.T) {
	t.Parallel()
	st, ctx := waitingYard(t)
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-4", Title: "journal", Type: store.TypeJournal, Depot: "yard", Stage: "new"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddDep(ctx, "yard-1", "yard-4", store.DepBlocks, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.readyCmd(), "--waiting")
	if err != nil {
		t.Fatal(err)
	}
	want := "yard-2  waits until yard-1 is in review\nyard-3  waits until yard-1 closes\n"
	if out != want {
		t.Fatalf("ready --waiting = %q, want %q", out, want)
	}
	// In approved the target waits on both edges, each in its words.
	if _, err := st.Advance(ctx, "yard-2", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Advance(ctx, "yard-2", "approved", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.readyCmd(), "--waiting")
	if err != nil {
		t.Fatal(err)
	}
	want = "yard-2  waits until yard-1 is in review\n" +
		"yard-2  waits at approved until yard-1 closes\n" +
		"yard-3  waits until yard-1 closes\n"
	if out != want {
		t.Fatalf("ready --waiting in approved = %q, want %q", out, want)
	}
	// A held bead waits nowhere.
	if _, err := st.SetHold(ctx, "yard-3", true, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.readyCmd(), "--waiting")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "yard-3") || strings.Contains(out, "yard-4") {
		t.Fatalf("ready --waiting names a held bead or a journal:\n%s", out)
	}
	// Without the flag, the table it always was: only the blocker is
	// ready, and no line says why anything waits.
	out, err = run(t, a.readyCmd())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "yard-1") || strings.Contains(out, "waits") ||
		strings.Contains(out, "yard-2") || strings.Contains(out, "yard-3") {
		t.Fatalf("ready =\n%s", out)
	}
}

// ready --waiting names a ready bead behind a full group with the route
// that matched it and the group's count and limit, after the dependency
// holds; bead show says the same line of the bead. Tonight's case: the
// bead matched an any-depot label route (priority 1) rather than its
// depot's own (priority 0), and the label route's group was at its limit.
func TestReadyWaitingNamesABeadBehindAFullGroup(t *testing.T) {
	t.Parallel()
	a, st, ran := quotaCLI(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, g := range []store.Group{
		{Name: "muse-builders", Runner: "herdr", Limit: 1, Config: map[string]any{"kind": "claude"}},
		{Name: "muse-hosted", Runner: "herdr", Limit: 5, Config: map[string]any{"kind": "claude"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	depot, label := "yard", "difficulty:1"
	if _, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: &depot, Group: "muse-hosted"}, "me"); err != nil {
		t.Fatal(err)
	}
	labeled, err := st.AddRoute(ctx, store.Route{Stage: "new", Label: &label, Group: "muse-builders", Priority: 1}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// muse-builders at its limit: the filler holds its one slot.
	filler, err := st.CreateBead(ctx, store.NewBead{Title: "filler", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx, filler.ID, "muse-builders", "new", "me", store.Routed{Route: labeled.ID}); err != nil {
		t.Fatal(err)
	}
	bead, err := st.CreateBead(ctx, store.NewBead{Title: "behind", Depot: "yard", Stage: "new", Labels: []string{"difficulty:1"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// A dependency hold still prints as today, first.
	blocker, err := st.CreateBead(ctx, store.NewBead{Title: "blocker", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.CreateBead(ctx, store.NewBead{Title: "target", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddDep(ctx, blocker.ID, target.ID, store.DepBlocks, "", "", "me"); err != nil {
		t.Fatal(err)
	}

	want := "waits for muse-builders (1 of 1) by route " + labeled.ID + " (label difficulty:1, priority 1)"
	out, err := run(t, a.readyCmd(), "--waiting")
	if err != nil {
		t.Fatal(err)
	}
	if wantOut := target.ID + "  waits until " + blocker.ID + " closes\n" + bead.ID + "  new  " + want + "\n"; out != wantOut {
		t.Fatalf("ready --waiting = %q, want %q", out, wantOut)
	}
	if out, err := run(t, a.beadCmd(), "show", bead.ID); err != nil || !strings.Contains(out, "\n  "+want+"\n") {
		t.Fatalf("bead show misses the group line %q: %v\n%s", want, err, out)
	}
	// ready alone is unchanged: no line says why anything waits.
	out, err = run(t, a.readyCmd())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, bead.ID) || strings.Contains(out, "waits") {
		t.Fatalf("ready =\n%s", out)
	}
	if ran() {
		t.Error("ready --waiting ran the limits command")
	}
}

// A bead behind a paused group names the pause instead of a count: there
// are no slots to count against.
func TestReadyWaitingNamesAPausedGroup(t *testing.T) {
	t.Parallel()
	a, st, ran := quotaCLI(t)
	ctx := context.Background()
	if err := st.PutGroup(ctx, store.Group{Name: "g", Runner: "herdr", Limit: 1, Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := st.SetGroup(ctx, "g", store.GroupEdit{Limit: &zero}, "me"); err != nil {
		t.Fatal(err)
	}
	r, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	bead, err := st.CreateBead(ctx, store.NewBead{Title: "behind", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	want := "waits for g (paused) by route " + r.ID + " (priority 0)"
	out, err := run(t, a.readyCmd(), "--waiting")
	if err != nil {
		t.Fatal(err)
	}
	if wantOut := bead.ID + "  new  " + want + "\n"; out != wantOut {
		t.Fatalf("ready --waiting = %q, want %q", out, wantOut)
	}
	if out, err := run(t, a.beadCmd(), "show", bead.ID); err != nil || !strings.Contains(out, "\n  "+want+"\n") {
		t.Fatalf("bead show misses the group line %q: %v\n%s", want, err, out)
	}
	if ran() {
		t.Error("ready --waiting ran the limits command")
	}
}

// A bead behind a full group whose stored wait says a quota holds it
// shows that too, in the same line, from the stored event: nothing is
// asked of the provider.
func TestReadyWaitingShowsTheStoredQuotaInTheSameLine(t *testing.T) {
	t.Parallel()
	a, st, ran := quotaCLI(t)
	ctx := context.Background()
	if err := st.PutGroup(ctx, store.Group{Name: "g", Runner: "herdr", Limit: 1, Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	r, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "g", When: "Weekly<80"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	filler, err := st.CreateBead(ctx, store.NewBead{Title: "filler", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx, filler.ID, "g", "new", "me", store.Routed{Route: r.ID}); err != nil {
		t.Fatal(err)
	}
	bead, err := st.CreateBead(ctx, store.NewBead{Title: "behind", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LogDispatch(ctx, bead.ID, "quota_wait", "yardr", map[string]any{
		"stage": "new", "route": r.ID, "reason": "g passed over (" + weeklyReason + ")",
		"passed": []store.PassedGroup{{Group: "g", Reason: weeklyReason}},
	}); err != nil {
		t.Fatal(err)
	}

	want := "waits for g (1 of 1) by route " + r.ID + " (priority 0); quota: g passed over (" + weeklyReason + ")"
	out, err := run(t, a.readyCmd(), "--waiting")
	if err != nil {
		t.Fatal(err)
	}
	if wantOut := bead.ID + "  new  " + want + "\n"; out != wantOut {
		t.Fatalf("ready --waiting = %q, want %q", out, wantOut)
	}
	if out, err := run(t, a.beadCmd(), "show", bead.ID); err != nil || !strings.Contains(out, "\n  "+want+"\n") {
		t.Fatalf("bead show misses the group line %q: %v\n%s", want, err, out)
	}
	if ran() {
		t.Error("ready --waiting ran the limits command")
	}
}

// ready --waiting --json carries the holding edges with their stages.
func TestReadyWaitingJSON(t *testing.T) {
	t.Parallel()
	st, _ := waitingYard(t)
	j := &app{st: st, actor: "me", json: true}

	out, err := run(t, j.readyCmd(), "--waiting")
	if err != nil {
		t.Fatal(err)
	}
	var got []store.Dep
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("ready --waiting --json = %q (%v)", out, err)
	}
	if len(got) != 2 || got[0].To != "yard-2" || got[0].Until != "review" ||
		got[1].To != "yard-3" || got[1].At != "" || got[1].Until != "" {
		t.Fatalf("ready --waiting --json = %+v", got)
	}
}

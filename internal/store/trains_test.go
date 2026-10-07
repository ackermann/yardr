package store_test

import (
	"context"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// trainDepot is a store with depot web (base main), train web-e and beads web-1
// and web-2.
func trainDepot(t *testing.T) (store.API, context.Context) {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "web-e", Title: "train", Type: store.TypeTrain, Depot: "web"},
		{ID: "web-1", Title: "one", Depot: "web"},
		{ID: "web-2", Title: "two", Depot: "web"},
	} {
		if _, err := s.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx
}

func family(t *testing.T, s store.API, id string) store.Family {
	t.Helper()
	f, err := s.Family(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return *f
}

// BaseOf: a child of a train lands on the train's branch; everything else,
// the train itself included, on the depot's base.
func TestBaseOf(t *testing.T) {
	s, ctx := trainDepot(t)
	if f := family(t, s, "web-1"); f.Base != "main" || f.Train != "" {
		t.Fatalf("before the edge: %+v, want base main and no train", f)
	}
	for _, c := range []string{"web-1", "web-2"} {
		if err := s.AddDep(ctx, "web-e", c, store.DepParent, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if f := family(t, s, "web-1"); f.Base != "yard/web-e" || f.Train != "web-e" {
		t.Fatalf("child: %+v, want base yard/web-e of train web-e", f)
	}
	f := family(t, s, "web-e")
	if f.Base != "main" || f.Train != "" || !slices.Equal(f.Children, []string{"web-1", "web-2"}) {
		t.Fatalf("train: %+v, want base main and children web-1, web-2", f)
	}
	// A depotless bead with no train has no base.
	b, err := s.CreateBead(ctx, store.NewBead{Title: "loose"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if f := family(t, s, b.ID); f.Base != "" {
		t.Fatalf("depotless bead: %+v", f)
	}
	// Removing the edge puts the child back on the depot's base.
	if err := s.RemoveDep(ctx, "web-e", "web-2", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	if f := family(t, s, "web-2"); f.Base != "main" || f.Train != "" {
		t.Fatalf("after removing the edge: %+v", f)
	}
}

func TestParentEdgeRules(t *testing.T) {
	s, ctx := trainDepot(t)
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-f", Title: "other train", Type: store.TypeTrain, Depot: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "api-1", Title: "elsewhere", Depot: "api"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-g", Title: "landed train", Type: store.TypeTrain, Depot: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, "web-g", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "web-e", "web-1", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ from, to, want string }{
		{"web-2", "web-1", "not a train"},                    // parent must be a train
		{"web-f", "web-1", "already a child of train web-e"}, // one train parent at most
		{"web-e", "web-f", "trains do not nest"},
		{"web-e", "api-1", "a child must be in its train's depot"},
		{"web-e", "web-1", "already parent"},  // the same edge twice
		{"web-g", "web-2", "web-g is closed"}, // a landed train takes no more children
	} {
		err := s.AddDep(ctx, c.from, c.to, store.DepParent, "me")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("dep add %s %s -k parent: err = %v, want %q", c.from, c.to, err, c.want)
		}
	}
	if f := family(t, s, "web-1"); f.Train != "web-e" {
		t.Fatalf("a refused edge changed the child: %+v", f)
	}
	// Other kinds between the same beads are not parent edges.
	if err := s.AddDep(ctx, "web-2", "web-1", "blocks", "me"); err != nil {
		t.Fatal(err)
	}
}

// A parent edge from a bead that is not a train (imported, or its type
// changed since) does not make a child: the bead keeps the depot's base. A
// bead left with two train parents has no base until one edge goes.
func TestOnlyTrainParentsCount(t *testing.T) {
	s, ctx := trainDepot(t)
	if err := s.AddDep(ctx, "web-e", "web-1", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	task := "task"
	if _, err := s.UpdateBead(ctx, "web-e", store.BeadPatch{Type: &task}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if f := family(t, s, "web-1"); f.Base != "main" || f.Train != "" {
		t.Fatalf("parent no longer a train: %+v, want the depot's base", f)
	}

	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-f", Title: "train", Type: store.TypeTrain, Depot: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "web-f", "web-1", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	train := store.TypeTrain
	if _, err := s.UpdateBead(ctx, "web-e", store.BeadPatch{Type: &train}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Family(ctx, "web-1"); err == nil || !strings.Contains(err.Error(), "child of two trains") {
		t.Fatalf("two train parents: err = %v", err)
	}
}

// A parent edge makes the child a blocker of its train in the same write, so
// the train is not ready (and nothing routes it) until its last child
// closes; a blocks edge already there is not doubled. Removing the parent
// edge removes the blocks edge with it.
func TestParentEdgeBlocksTheTrain(t *testing.T) {
	s, ctx := trainDepot(t)
	if err := s.AddDep(ctx, "web-2", "web-e", store.DepBlocks, "me"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"web-1", "web-2"} {
		if err := s.AddDep(ctx, "web-e", c, store.DepParent, "me"); err != nil {
			t.Fatal(err)
		}
	}
	blockers := func() []string {
		t.Helper()
		bs, err := s.OpenBlockers(ctx, "web-e")
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(bs)
		return bs
	}
	if got := blockers(); !slices.Equal(got, []string{"web-1", "web-2"}) {
		t.Fatalf("train blocked by %v, want web-1 and web-2 once each", got)
	}
	ds, err := s.Deps(ctx, "web-e")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, d := range ds {
		kinds = append(kinds, d.From+" "+d.Kind+" "+d.To)
	}
	slices.Sort(kinds)
	want := []string{"web-1 blocks web-e", "web-2 blocks web-e", "web-e parent web-1", "web-e parent web-2"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("edges %q, want %q", kinds, want)
	}

	if _, err := s.CloseBead(ctx, "web-1", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := blockers(); !slices.Equal(got, []string{"web-2"}) {
		t.Fatalf("after web-1 closed: blocked by %v, want web-2", got)
	}
	if err := s.RemoveDep(ctx, "web-e", "web-2", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	if got := blockers(); len(got) != 0 {
		t.Fatalf("web-2 is no longer a child, train still blocked by %v", got)
	}
	ready, err := s.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(ready, func(b store.Bead) bool { return b.ID == "web-e" }) {
		t.Fatal("the train is not ready once no open child is left")
	}
}

// Listings carry each bead's train (and each session's bead's), worked out
// from the parent edges when listed, so a table needs no lookup per row.
func TestListingsCarryTheTrain(t *testing.T) {
	s, ctx := trainDepot(t)
	if err := s.AddDep(ctx, "web-e", "web-1", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	trains := func(bs []store.Bead) map[string]string {
		m := map[string]string{}
		for _, b := range bs {
			m[b.ID] = b.Train
		}
		return m
	}
	want := map[string]string{"web-e": "", "web-1": "web-e", "web-2": ""}
	listed, err := s.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := trains(listed); !maps.Equal(got, want) {
		t.Fatalf("bead list trains = %v, want %v", got, want)
	}
	ready, err := s.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The train is blocked by its child, so not ready.
	if got := trains(ready); !maps.Equal(got, map[string]string{"web-1": "web-e", "web-2": ""}) {
		t.Fatalf("ready trains = %v", got)
	}

	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"web-1", "web-2"} {
		if _, err := s.Claim(ctx, id, "builders", "new", "yard"); err != nil {
			t.Fatal(err)
		}
	}
	ss, err := s.Sessions(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, sess := range ss {
		got[sess.Bead] = sess.Train
	}
	if !maps.Equal(got, map[string]string{"web-1": "web-e", "web-2": ""}) {
		t.Fatalf("session trains = %v", got)
	}

	// Derived, not stored: removing the edge clears it from the next listing.
	if err := s.RemoveDep(ctx, "web-e", "web-1", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	listed, err = s.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := trains(listed); got["web-1"] != "" {
		t.Fatalf("after removing the edge: %v", got)
	}
}

// bead list --train: the train first, whatever its priority, then its
// children; nothing else.
func TestListBeadsByTrain(t *testing.T) {
	s, ctx := trainDepot(t)
	for _, c := range []string{"web-1", "web-2"} {
		if err := s.AddDep(ctx, "web-e", c, store.DepParent, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-3", Title: "three", Depot: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	low := 4
	if _, err := s.UpdateBead(ctx, "web-e", store.BeadPatch{Priority: &low}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	bs, err := s.ListBeads(ctx, store.BeadFilter{Train: "web-e"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range bs {
		ids = append(ids, b.ID)
	}
	if !slices.Equal(ids, []string{"web-e", "web-1", "web-2"}) {
		t.Fatalf("bead list --train web-e = %v, want web-e, web-1, web-2", ids)
	}
	if _, err := s.ListBeads(ctx, store.BeadFilter{Train: "web-3"}); err == nil || !strings.Contains(err.Error(), "not a train") {
		t.Fatalf("--train of a plain bead: %v, want not a train", err)
	}
	if _, err := s.ListBeads(ctx, store.BeadFilter{Train: "web-nope"}); err == nil {
		t.Fatal("--train of a missing bead listed")
	}
}

// wagonDepot is trainDepot with the repository's wagon flow governing depot web's
// wagons, and wagon web-p in backlog.
func wagonDepot(t *testing.T) (store.API, context.Context) {
	t.Helper()
	s, ctx := trainDepot(t)
	text, err := os.ReadFile(storetest.RepoFlow(t, "type/wagon"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow(string(text))
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "web.wagon"
	if _, err := s.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "web", store.TypeWagon, "web.wagon", "me"); err != nil {
		t.Fatal(err)
	}
	if b, err := s.CreateBead(ctx, store.NewBead{ID: "web-p", Title: "wagon", Type: store.TypeWagon, Depot: "web"}, "me"); err != nil || b.Stage != "backlog" {
		t.Fatalf("wagon = %+v, %v; want it created in backlog", b, err)
	}
	return s, ctx
}

// A wagon with no train stays where its flow starts: it lands under the light
// gate with no review of its own, which is only safe on a train's branch.
// Neither a move nor an outcome takes it on, nor can one be created further
// along; as a child of a train it moves like any bead.
func TestWagonNeedsATrain(t *testing.T) {
	s, ctx := wagonDepot(t)
	refused := func(err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "is a wagon with no train, so it stays in backlog") ||
			!strings.Contains(err.Error(), "yardr dep add <train> web-p -k parent") {
			t.Fatalf("err = %v, want the wagon refused with what to do", err)
		}
	}
	_, err := s.Advance(ctx, "web-p", "new", store.Expect{}, "me")
	refused(err)
	_, err = s.Done(ctx, "web-p", "", "", store.Expect{}, "me")
	refused(err)
	if b, _ := s.GetBead(ctx, "web-p"); b.Stage != "backlog" {
		t.Fatalf("stage = %s after refused moves", b.Stage)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "late", Type: store.TypeWagon, Depot: "web", Stage: "new"}, "me"); err == nil ||
		!strings.Contains(err.Error(), "a new wagon has no train yet, so it is created in backlog, not new") {
		t.Fatalf("creating a wagon in new = %v, want it refused", err)
	}
	// A parent that is no train does not count (it makes no train's branch).
	if err := s.AddDep(ctx, "web-1", "web-p", store.DepParent, "me"); err == nil {
		t.Fatal("a task took a wagon as its child")
	}

	if err := s.AddDep(ctx, "web-e", "web-p", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	if b, err := s.Advance(ctx, "web-p", "new", store.Expect{}, "me"); err != nil || b.Stage != "new" {
		t.Fatalf("a wagon of a train: %+v, %v", b, err)
	}
	// Having lost its train, it can only be taken back to where it started.
	if err := s.RemoveDep(ctx, "web-e", "web-p", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Done(ctx, "web-p", "", "", store.Expect{}, "me")
	refused(err)
	if b, err := s.Advance(ctx, "web-p", "backlog", store.Expect{}, "me"); err != nil || b.Stage != "backlog" {
		t.Fatalf("back to backlog: %+v, %v", b, err)
	}

	// Other types are not held to it, and a wagon no flow governs has no
	// first stage to stay in: the dispatcher keeps that one from being worked.
	if _, err := s.Advance(ctx, "web-1", "review", store.Expect{}, "me"); err != nil {
		t.Fatalf("a task with no train: %v", err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "bare", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "flowless", Type: store.TypeWagon, Depot: "bare", Stage: "anywhere"}, "me"); err != nil {
		t.Fatalf("a wagon no flow governs: %v", err)
	}
}

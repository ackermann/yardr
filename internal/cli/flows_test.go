package cli

import (
	"context"
	"slices"
	"testing"

	"github.com/mckean/yard/internal/store"
	"github.com/mckean/yard/internal/storetest"
)

func TestFlowViews(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutRig(ctx, store.Rig{Name: "yard", Path: "/tmp/yard"}))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "manual"}))
	must(st.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"mckean", "mayor"}}))
	rig, decision := "yard", "decision"
	_, err := st.AddRoute(ctx, store.Route{Stage: "new", Rig: &rig, Group: "builders"})
	must(err)
	_, err = st.AddRoute(ctx, store.Route{Stage: "open", Type: &decision, Group: "deciders"})
	must(err)

	f, err := store.ParseFlow("new -> review\nreview -> merged | new\nmerged terminal")
	must(err)
	f.Rig = "yard"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	d, err := store.ParseFlow("open -> done\ndone terminal")
	must(err)
	d.Rig, d.Type = "yard", "decision"
	_, err = st.PutFlow(ctx, *d, "me")
	must(err)

	for _, nb := range []store.NewBead{
		{Title: "a", Rig: "yard"}, {Title: "b", Rig: "yard"}, {Title: "c", Rig: "yard", Stage: "review"},
		{Title: "q", Rig: "yard", Type: "decision", Stage: "open"},
	} {
		_, err := st.CreateBead(ctx, nb, "me")
		must(err)
	}

	views, err := flowViews(ctx, st, "yard")
	must(err)
	if len(views) != 2 || views[0].Flow.Type != "" || views[1].Flow.Type != "decision" {
		t.Fatalf("views = %+v", views)
	}
	rv := views[0]
	want := []stageView{
		{Stage: "new", Group: "builders", Next: []string{"review"}, Open: 2},
		{Stage: "review", Next: []string{"merged", "new"}, Open: 1},
		{Stage: "merged", Next: []string{}, Terminal: true},
	}
	for i, w := range want {
		g := rv.Stages[i]
		if g.Stage != w.Stage || g.Group != w.Group || !slices.Equal(g.Next, w.Next) || g.Terminal != w.Terminal || g.Open != w.Open {
			t.Errorf("stage %d = %+v, want %+v", i, g, w)
		}
	}
	// review has no route; the terminal stage needs none.
	if len(rv.Warnings) != 1 || rv.Warnings[0] != "stage review has no route: beads there wait for a human" {
		t.Errorf("warnings = %v", rv.Warnings)
	}
	// The decision bead counts under its own flow, not the rig's.
	dv := views[1]
	if dv.Stages[0].Group != "deciders" || dv.Stages[0].Open != 1 || len(dv.Warnings) != 0 {
		t.Errorf("decision view = %+v", dv)
	}
	// Human stages list their members; a manual group without members is
	// still human, open to anyone.
	if !dv.Stages[0].Human || !slices.Equal(dv.Stages[0].Members, []string{"mckean", "mayor"}) {
		t.Errorf("deciders stage = %+v", dv.Stages[0])
	}
	if !rv.Stages[0].Human || rv.Stages[0].Members != nil || rv.Stages[1].Human {
		t.Errorf("rig stages = %+v", rv.Stages)
	}

	if _, err := flowViews(ctx, st, "nope"); err == nil {
		t.Error("unknown rig accepted")
	}
}

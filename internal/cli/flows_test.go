package cli

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
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
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "manual"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"mckean", "yardmaster"}}, "me"))
	depot, decision := "yard", "decision"
	_, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: &depot, Group: "builders"}, "me")
	must(err)
	_, err = st.AddRoute(ctx, store.Route{Stage: "open", Type: &decision, Group: "deciders"}, "me")
	must(err)

	f, err := store.ParseFlow("new -> review\nreview -> merged | new\nmerged terminal")
	must(err)
	f.Name = "code"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "", "code", "me"))
	d, err := store.ParseFlow("open -> done\ndone terminal")
	must(err)
	d.Name = "decision"
	_, err = st.PutFlow(ctx, *d, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "decision", "decision", "me"))

	for _, nb := range []store.NewBead{
		{Title: "a", Depot: "yard"}, {Title: "b", Depot: "yard"}, {Title: "c", Depot: "yard", Stage: "review"},
		{Title: "q", Depot: "yard", Type: "decision", Stage: "open"},
	} {
		_, err := st.CreateBead(ctx, nb, "me")
		must(err)
	}

	views, err := flowViews(ctx, st, "yard", "")
	must(err)
	if len(views) != 2 || views[0].Type != "" || views[1].Type != "decision" || views[1].Source.String() != "store, type decision" {
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
	// The decision bead counts under its own flow, not the depot's.
	dv := views[1]
	if dv.Stages[0].Group != "deciders" || dv.Stages[0].Open != 1 || len(dv.Warnings) != 0 {
		t.Errorf("decision view = %+v", dv)
	}
	// Human stages list their members; a manual group without members is
	// still human, open to anyone.
	if !dv.Stages[0].Human || !slices.Equal(dv.Stages[0].Members, []string{"mckean", "yardmaster"}) {
		t.Errorf("deciders stage = %+v", dv.Stages[0])
	}
	if !rv.Stages[0].Human || rv.Stages[0].Members != nil || rv.Stages[1].Human {
		t.Errorf("depot stages = %+v", rv.Stages)
	}

	if _, err := flowViews(ctx, st, "nope", ""); err == nil {
		t.Error("unknown depot accepted")
	}

	// flow list: each named flow with what it governs; unused ones govern nothing.
	unused, err := store.ParseFlow("a -> b")
	must(err)
	unused.Name = "spare"
	_, err = st.PutFlow(ctx, *unused, "me")
	must(err)
	var warned bytes.Buffer
	rows, err := flowList(&warned, ctx, st)
	must(err)
	if warned.String() != "" {
		t.Errorf("flow list warned:\n%s", warned.String())
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Name] = r.Source + ": " + strings.Join(r.Governs, ", ")
	}
	wantRows := map[string]string{"code": "store: depot yard", "decision": "store: depot yard type decision", "spare": "store: "}
	if !maps.Equal(got, wantRows) {
		t.Errorf("flow list = %v, want %v", got, wantRows)
	}
}

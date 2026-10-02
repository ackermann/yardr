package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// epicRig is a store with rig web (base main), epic web-e and beads web-1
// and web-2.
func epicRig(t *testing.T) (store.API, context.Context) {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "web-e", Title: "epic", Type: store.TypeEpic, Rig: "web"},
		{ID: "web-1", Title: "one", Rig: "web"},
		{ID: "web-2", Title: "two", Rig: "web"},
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

// BaseOf: a child of an epic lands on the epic's branch; everything else,
// the epic itself included, on the rig's base.
func TestBaseOf(t *testing.T) {
	s, ctx := epicRig(t)
	if f := family(t, s, "web-1"); f.Base != "main" || f.Epic != "" {
		t.Fatalf("before the edge: %+v, want base main and no epic", f)
	}
	for _, c := range []string{"web-1", "web-2"} {
		if err := s.AddDep(ctx, "web-e", c, store.DepParent, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if f := family(t, s, "web-1"); f.Base != "yard/web-e" || f.Epic != "web-e" {
		t.Fatalf("child: %+v, want base yard/web-e of epic web-e", f)
	}
	f := family(t, s, "web-e")
	if f.Base != "main" || f.Epic != "" || !slices.Equal(f.Children, []string{"web-1", "web-2"}) {
		t.Fatalf("epic: %+v, want base main and children web-1, web-2", f)
	}
	// A rigless bead with no epic has no base.
	b, err := s.CreateBead(ctx, store.NewBead{Title: "loose"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if f := family(t, s, b.ID); f.Base != "" {
		t.Fatalf("rigless bead: %+v", f)
	}
	// Removing the edge puts the child back on the rig's base.
	if err := s.RemoveDep(ctx, "web-e", "web-2", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	if f := family(t, s, "web-2"); f.Base != "main" || f.Epic != "" {
		t.Fatalf("after removing the edge: %+v", f)
	}
}

func TestParentEdgeRules(t *testing.T) {
	s, ctx := epicRig(t)
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-f", Title: "other epic", Type: store.TypeEpic, Rig: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRig(ctx, store.Rig{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "api-1", Title: "elsewhere", Rig: "api"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "web-e", "web-1", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ from, to, want string }{
		{"web-2", "web-1", "not an epic"},                   // parent must be an epic
		{"web-f", "web-1", "already a child of epic web-e"}, // one epic parent at most
		{"web-e", "web-f", "epics do not nest"},
		{"web-e", "api-1", "a child must be in its epic's rig"},
		{"web-e", "web-1", "already parent"}, // the same edge twice
	} {
		err := s.AddDep(ctx, c.from, c.to, store.DepParent, "me")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("dep add %s %s -k parent: err = %v, want %q", c.from, c.to, err, c.want)
		}
	}
	if f := family(t, s, "web-1"); f.Epic != "web-e" {
		t.Fatalf("a refused edge changed the child: %+v", f)
	}
	// Other kinds between the same beads are not parent edges.
	if err := s.AddDep(ctx, "web-2", "web-1", "blocks", "me"); err != nil {
		t.Fatal(err)
	}
}

// A parent edge from a bead that is not an epic (imported, or its type
// changed since) does not make a child: the bead keeps the rig's base. A
// bead left with two epic parents has no base until one edge goes.
func TestOnlyEpicParentsCount(t *testing.T) {
	s, ctx := epicRig(t)
	if err := s.AddDep(ctx, "web-e", "web-1", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	task := "task"
	if _, err := s.UpdateBead(ctx, "web-e", store.BeadPatch{Type: &task}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if f := family(t, s, "web-1"); f.Base != "main" || f.Epic != "" {
		t.Fatalf("parent no longer an epic: %+v, want the rig's base", f)
	}

	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-f", Title: "epic", Type: store.TypeEpic, Rig: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, "web-f", "web-1", store.DepParent, "me"); err != nil {
		t.Fatal(err)
	}
	epic := store.TypeEpic
	if _, err := s.UpdateBead(ctx, "web-e", store.BeadPatch{Type: &epic}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Family(ctx, "web-1"); err == nil || !strings.Contains(err.Error(), "child of two epics") {
		t.Fatalf("two epic parents: err = %v", err)
	}
}

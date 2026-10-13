package web_test

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// The routes page says every label a route asks for and the bead
// priorities it takes, and a route that asks for neither says neither.
func TestRoutesPageSaysLabelsAndBeadPriorities(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutGroup(ctx, store.Group{Name: "g", Runner: "manual"}, "me"))
	_, err := f.st.AddRoute(ctx, store.Route{Stage: "plain", Group: "g"}, "me")
	must(t, err)
	if page := f.get("/routes").body; strings.Contains(page, "<dt>label</dt>") || strings.Contains(page, "<dt>beads</dt>") {
		t.Fatalf("a route with no label and no bead priority says one:\n%s", page)
	}
	hard, urgent := "difficulty:3", 1
	_, err = f.st.AddRoute(ctx, store.Route{Stage: "new", Group: "g", Label: &hard, AndLabels: []string{"size:large"}, BeadPriority: &urgent}, "me")
	must(t, err)
	page := f.get("/routes").body
	for _, want := range []string{"<dt>label</dt><dd>difficulty:3, size:large</dd>", "<dt>beads</dt><dd>P0-P1</dd>"} {
		if !strings.Contains(page, want) {
			t.Errorf("the routes page lacks %q:\n%s", want, page)
		}
	}
}

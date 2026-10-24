package cli

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// route add takes --label more than once and --bead-priority, route list
// and group show print them, and the views of a flow and the review queue
// read them as the dispatcher does: a stage of the flow is the route's for
// a bead made with no label and no priority, a bead's own queue the route's
// for that bead.
func TestRouteAddTakesSeveralLabelsAndABeadPriority(t *testing.T) {
	t.Parallel()
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	f, err := store.ParseFlow("new -> merged\nmerged terminal")
	must(err)
	f.Name = "code"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "", "code", "me"))

	ids := map[string]string{}
	for name, args := range map[string][]string{
		"plain":  {"--group", "a"},
		"one":    {"--group", "a", "--label", "difficulty:3"},
		"two":    {"--group", "b", "--label", "difficulty:3", "--label", "size:large"},
		"urgent": {"--group", "people", "--bead-priority", "1"},
	} {
		// A command of its own each time, as each call of yardr is.
		id, err := run(t, a.routeCmd(), append([]string{"add", "--stage", "new"}, args...)...)
		must(err)
		ids[name] = strings.TrimSpace(id)
	}
	if _, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "a", "--bead-priority", "-1"); err == nil ||
		!strings.Contains(err.Error(), "bead priority -1") {
		t.Errorf("route add --bead-priority -1: %v", err)
	}
	routes, err := st.Routes(ctx)
	if err != nil || len(routes) != 4 {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	for _, r := range routes {
		switch r.ID {
		case ids["plain"]:
			if r.ByBead() {
				t.Errorf("the plain route = %+v", r)
			}
		case ids["one"]:
			if r.LabelList() != "difficulty:3" || r.AndLabels != nil || r.BeadPriority != nil {
				t.Errorf("the route with one label = %+v", r)
			}
		case ids["two"]:
			if r.LabelList() != "difficulty:3, size:large" || r.BeadPriority != nil {
				t.Errorf("the route with two labels = %+v", r)
			}
		case ids["urgent"]:
			if r.BeadPriority == nil || *r.BeadPriority != 1 || r.Label != nil {
				t.Errorf("the route for urgent beads = %+v", r)
			}
		}
	}

	out, err := run(t, a.routeCmd(), "list")
	must(err)
	var rows []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		rows = append(rows, strings.Join(strings.Fields(line), " "))
	}
	want := []string{
		"ID SCOPE STAGE TYPE LABEL BEADS ORIGINATOR GROUP NOTE",
		ids["urgent"] + " default new - - P0-P1 any people",
		ids["two"] + " default new - difficulty:3, size:large - - b",
		ids["one"] + " default new - difficulty:3 - - a",
		ids["plain"] + " default new - - - - a",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Errorf("route list =\n%s\nwant\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
	out, err = run(t, a.groupCmd(), "show", "b")
	must(err)
	if !strings.Contains(out, "route "+ids["two"]+": stage new · label difficulty:3, size:large") {
		t.Errorf("group show b does not say the route's labels:\n%s", out)
	}
	out, err = run(t, a.groupCmd(), "show", "people")
	must(err)
	if !strings.Contains(out, "route "+ids["urgent"]+": stage new · beads P0-P1 · originator any") {
		t.Errorf("group show people does not say the route's bead priorities:\n%s", out)
	}

	// The flow's stage is who works a bead made with nothing said: not the
	// people the urgent beads go to, though that route is read first.
	views, err := flowViews(ctx, st, "yard", "")
	must(err)
	if len(views) != 1 || views[0].Stages[0].Group != "a" || views[0].Stages[0].Human {
		t.Fatalf("flow views = %+v, want stage new worked by a", views)
	}
	// The queue of a person holds the urgent bead and no other.
	urgent, err := st.CreateBead(ctx, store.NewBead{Title: "urgent", Depot: "yard", Priority: 1, Labels: []string{"difficulty:3", "size:large"}}, "me")
	must(err)
	_, err = st.CreateBead(ctx, store.NewBead{Title: "plain", Depot: "yard", Priority: 2}, "me")
	must(err)
	_, err = st.CreateBead(ctx, store.NewBead{Title: "hard", Depot: "yard", Priority: 2, Labels: []string{"difficulty:3", "size:large"}}, "me")
	must(err)
	items, err := reviewQueue(ctx, st, "jo", nil)
	must(err)
	if len(items) != 1 || items[0].Bead.ID != urgent.ID {
		t.Errorf("review queue of jo = %+v, want the urgent bead alone", items)
	}
}

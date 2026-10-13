package store_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A route with two labels matches only a bead that has both and is read
// before a route with one of them; a route with a bead priority matches
// beads that urgent and no others; and a route with one label and no
// priority is stored and logged as it always was.
func TestRouteMatchesSeveralLabelsAndABeadsPriority(t *testing.T) {
	s, _ := quotaGroups(t)
	ctx := t.Context()
	add := func(r store.Route) *store.Route {
		t.Helper()
		r.Stage = "new"
		saved, err := s.AddRoute(ctx, r, "jo")
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	hard, one := "difficulty:3", 1
	anyBead := add(store.Route{Group: "people"})
	oneLabel := add(store.Route{Group: "a", Label: &hard})
	evs, err := s.EventsOfKind(ctx, "route_added", 1)
	if err != nil || len(evs) != 1 || evs[0].Data["label"] != hard || evs[0].Data["labels"] != nil || evs[0].Data["bead_priority"] != nil {
		t.Fatalf("route_added of a route with one label = %+v, %v", evs, err)
	}
	if raw, _ := json.Marshal(oneLabel); strings.Contains(string(raw), "and_labels") || strings.Contains(string(raw), "bead_priority") ||
		!strings.Contains(string(raw), `"label":"difficulty:3"`) {
		t.Fatalf("a route with one label is stored as %s", raw)
	}
	// A label given twice counts once, and the labels keep their order.
	twoLabels := add(store.Route{Group: "b", AndLabels: []string{"size:large", hard, "size:large"}})
	if want := []string{"size:large", hard}; !reflect.DeepEqual(twoLabels.Labels(), want) || twoLabels.LabelList() != "size:large, difficulty:3" ||
		twoLabels.Label == nil || *twoLabels.Label != "size:large" {
		t.Fatalf("the route with two labels = %+v, labels %v", twoLabels, twoLabels.Labels())
	}
	evs, err = s.EventsOfKind(ctx, "route_added", 1)
	if err != nil || len(evs) != 1 || evs[0].Data["label"] != "size:large" || fmt.Sprint(evs[0].Data["labels"]) != "[size:large difficulty:3]" {
		t.Fatalf("route_added of a route with two labels = %+v, %v", evs, err)
	}
	urgent := add(store.Route{Group: "b", BeadPriority: &one})
	evs, err = s.EventsOfKind(ctx, "route_added", 1)
	if err != nil || len(evs) != 1 || fmt.Sprint(evs[0].Data["bead_priority"]) != "1" || urgent.BeadPriorities() != "P0-P1" {
		t.Fatalf("route_added of a route with a bead priority = %+v, %v (%s)", evs, err, urgent.BeadPriorities())
	}
	if zero := 0; (store.Route{BeadPriority: &zero}).BeadPriorities() != "P0" || anyBead.BeadPriorities() != "" ||
		anyBead.ByBead() || !oneLabel.ByBead() || !urgent.ByBead() {
		t.Fatalf("what the routes say of themselves: %+v %+v %+v", anyBead, oneLabel, urgent)
	}
	if neg := -1; true {
		if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "a", BeadPriority: &neg}, "jo"); err == nil ||
			!strings.Contains(err.Error(), "bead priority -1") {
			t.Fatalf("a route for bead priority -1: %v", err)
		}
	}

	routes, err := s.Routes(ctx)
	if err != nil || len(routes) != 4 {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	store.SortRoutes(routes)
	if routes[0].ID != twoLabels.ID || routes[3].ID != anyBead.ID {
		t.Fatalf("the route with two labels is not read first, or the one with no filter last: %+v", routes)
	}
	for _, tc := range []struct {
		priority int
		labels   []string
		want     *store.Route
	}{
		{2, nil, anyBead},
		{2, []string{hard}, oneLabel},
		{2, []string{"size:large"}, anyBead},
		{2, []string{hard, "size:large", "topic:x"}, twoLabels},
		{0, nil, urgent},
		{1, nil, urgent},
		{3, nil, anyBead},
		// Two routes with one filter each: the older is read first, as
		// between any two routes that are as specific.
		{1, []string{hard}, oneLabel},
		{1, []string{hard, "size:large"}, twoLabels},
	} {
		b := store.Bead{Stage: "new", Type: "task", Labels: tc.labels, Priority: tc.priority}
		if got := store.MatchRoute(routes, b); got == nil || got.ID != tc.want.ID {
			t.Errorf("a P%d bead with labels %v took %+v, want the route to %s (%s)", tc.priority, tc.labels, got, tc.want.Group, tc.want.ID)
		}
	}

	// What the dispatcher is handed of a ready bead routes as the bead does.
	for _, nb := range []store.NewBead{
		{Title: "urgent", Priority: 1},
		{Title: "plain", Priority: 2},
		{Title: "hard", Priority: 2, Labels: []string{hard, "size:large"}},
	} {
		if _, err := s.CreateBead(ctx, nb, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := s.Dispatchable(ctx)
	if err != nil || len(ready) != 3 {
		t.Fatalf("ready = %+v, %v", ready, err)
	}
	want := map[string]string{"urgent": urgent.ID, "plain": anyBead.ID, "hard": twoLabels.ID}
	for _, rb := range ready {
		// Through JSON, as the daemon reads it from the server.
		raw, _ := json.Marshal(rb)
		var over store.ReadyBead
		if err := json.Unmarshal(raw, &over); err != nil {
			t.Fatal(err)
		}
		whole, err := s.GetBead(ctx, rb.Bead)
		if err != nil {
			t.Fatal(err)
		}
		part := store.Bead{ID: over.Bead, Stage: over.Stage, Type: over.Type, Depot: over.Depot, Labels: over.Labels, Priority: over.Priority}
		a, b := store.MatchRoute(routes, part), store.MatchRoute(routes, *whole)
		if a == nil || b == nil || a.ID != b.ID || a.ID != want[whole.Title] {
			t.Errorf("bead %s (P%d): as a ready bead it takes %+v, whole %+v, want %s", whole.Title, whole.Priority, a, b, want[whole.Title])
		}
	}
}

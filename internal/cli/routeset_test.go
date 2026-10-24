package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// route set changes where a route's beads go and keeps which beads they
// are: the first group with its condition, the further groups and the
// note. The route keeps its id, and what changed is
// said line by line, as group set says it. --json carries the route.
func TestRouteSetRetunesAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	id, err := run(t, a.routeCmd(), "add", "--stage", "new", "--label", "difficulty:3",
		"--group", "a", "--else", "b", "--note", "the plain line")
	if err != nil {
		t.Fatal(err)
	}
	id = strings.TrimSpace(id)

	out, err := run(t, a.routeCmd(), "set", id, "--group", "b@Weekly<80", "--else", "a",
		"--note", "hard work: whatever the week says")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"route " + id + " saved",
		"--group: a -> b@Weekly<80",
		"--else: b -> a",
		"--note: the plain line -> hard work: whatever the week says",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("route set lacks %q:\n%s", want, out)
		}
	}
	routes, err := st.Routes(ctx)
	if err != nil || len(routes) != 1 || routes[0].ID != id {
		t.Fatalf("routes = %+v, %v; want the one route, its id kept", routes, err)
	}
	r := routes[0]
	if r.GroupList() != "b@Weekly<80 else a" || r.Note != "hard work: whatever the week says" {
		t.Errorf("the set route = %+v, want the new chain and note", r)
	}
	if r.Stage != "new" || len(r.Labels()) != 1 || r.Labels()[0] != "difficulty:3" {
		t.Errorf("the set route's beads moved: %+v", r)
	}
	evs, err := st.EventsOfKind(ctx, "route_set", 50)
	if err != nil || len(evs) != 1 {
		t.Fatalf("route_set events = %+v, %v; want one", evs, err)
	}
	was, _ := evs[0].Data["was"].(map[string]any)
	is, _ := evs[0].Data["is"].(map[string]any)
	if was["group"] != "a" || is["group"] != "b" {
		t.Errorf("route_set = %+v, want the route as it was and as it is", evs[0].Data)
	}

	a.json = true
	out, err = run(t, a.routeCmd(), "set", id, "--note", "json")
	if err != nil {
		t.Fatal(err)
	}
	var set store.Route
	if err := json.Unmarshal([]byte(out), &set); err != nil || set.ID != id || set.Note != "json" ||
		set.GroupList() != "b@Weekly<80 else a" || strings.Contains(out, "priority") {
		t.Errorf("route set --json = %s, %v; want the route as it is", out, err)
	}
}

// The stage, type, depot, labels, bead priority and originator are the
// route's identity, which route set refuses with the remove-and-add
// spelling; and a set that names nothing to change says what it takes.
func TestRouteSetRefusesIdentityChanges(t *testing.T) {
	t.Parallel()
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	id, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "a")
	if err != nil {
		t.Fatal(err)
	}
	id = strings.TrimSpace(id)
	for _, flag := range []string{"--stage", "--type", "--depot", "--label", "--bead-priority", "--originator"} {
		value := "x"
		if flag == "--bead-priority" {
			value = "1"
		}
		_, err := run(t, a.routeCmd(), "set", id, flag, value, "--note", "x")
		if err == nil || !strings.Contains(err.Error(), flag+" is which beads the route takes, which route set does not change") ||
			!strings.Contains(err.Error(), "yardr route rm "+id+" and yardr route add") {
			t.Errorf("route set %s %s: %v, want the remove-and-add spelling", flag, value, err)
		}
	}
	if _, err := run(t, a.routeCmd(), "set", id); err == nil ||
		!strings.Contains(err.Error(), "nothing to set: give --group, --else or --note") {
		t.Errorf("route set with nothing to change: %v", err)
	}
	if _, err := run(t, a.routeCmd(), "set", "no-such-route", "--note", "x"); err == nil ||
		!strings.Contains(err.Error(), "route no-such-route: not found") {
		t.Errorf("route set of an unknown route: %v", err)
	}
	// Unknown flags fail before any route can change.
	for _, flag := range []string{"--bogus", "--priority"} {
		if _, err := run(t, a.routeCmd(), "set", id, flag); err == nil ||
			!strings.Contains(err.Error(), "unknown flag: "+flag) {
			t.Errorf("route set %s: %v", flag, err)
		}
	}
	routes, err := st.Routes(ctx)
	if err != nil || len(routes) != 1 || routes[0].Note != "" {
		t.Errorf("a refused set moved the route: %+v, %v", routes, err)
	}
}

// A set that changes nothing says the route is set so already, as group
// set does, and logs nothing; an empty --else drops the further groups
// and an empty --note drops the note.
func TestRouteSetUnchangedAndDropped(t *testing.T) {
	t.Parallel()
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	id, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "a", "--else", "b", "--note", "lined")
	if err != nil {
		t.Fatal(err)
	}
	id = strings.TrimSpace(id)

	out, err := run(t, a.routeCmd(), "set", id, "--group", "a", "--else", "b", "--note", "lined")
	if err != nil {
		t.Fatal(err)
	}
	if out != "route "+id+" unchanged: it is set so already\n" {
		t.Errorf("a set that changes nothing printed %q", out)
	}
	if evs, err := st.EventsOfKind(ctx, "route_set", 50); err != nil || len(evs) != 0 {
		t.Errorf("a set that changes nothing logged route_set: %+v, %v", evs, err)
	}

	out, err = run(t, a.routeCmd(), "set", id, "--else", "", "--note", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--else: b -> (dropped)", "--note: lined -> (dropped)"} {
		if !strings.Contains(out, want) {
			t.Errorf("route set lacks %q:\n%s", want, out)
		}
	}
	routes, err := st.Routes(ctx)
	if err != nil || len(routes) != 1 || routes[0].GroupList() != "a" || routes[0].Note != "" {
		t.Errorf("after dropping: %+v, %v; want the first group alone and no note", routes, err)
	}
}

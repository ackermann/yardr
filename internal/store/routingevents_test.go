package store_test

import (
	"fmt"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A group and a route log an event when they appear and when they go, so
// whoever follows the yard by its events sees the yard's structure change.
func TestGroupsAndRoutesLogEvents(t *testing.T) {
	s, ctx := flowDepot(t)
	last := func(kind string) store.Event {
		t.Helper()
		evs, err := s.EventsOfKind(ctx, kind, 1)
		if err != nil || len(evs) != 1 {
			t.Fatalf("events of kind %s = %+v, %v; want one", kind, evs, err)
		}
		if evs[0].Actor != "jo" || evs[0].Bead != nil {
			t.Fatalf("%s = %+v; want it by jo, of no bead", kind, evs[0])
		}
		return evs[0]
	}
	count := func() int {
		t.Helper()
		n := 0
		for _, kind := range []string{"group_set", "group_removed", "route_added", "route_removed"} {
			evs, err := s.EventsOfKind(ctx, kind, 50)
			if err != nil {
				t.Fatal(err)
			}
			n += len(evs)
		}
		return n
	}
	before := count()

	if err := s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 3, Members: []string{"jo"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if e := last("group_set"); e.Data["group"] != "people" || e.Data["runner"] != "manual" || fmt.Sprint(e.Data["limit"]) != "3" || fmt.Sprint(e.Data["members"]) != "[jo]" {
		t.Fatalf("group_set = %+v", e.Data)
	}

	typ := "task"
	kept, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "people", Type: &typ, Priority: 2}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	e := last("route_added")
	if e.Data["route"] != kept.ID || e.Data["stage"] != "new" || e.Data["group"] != "people" || e.Data["type"] != "task" || fmt.Sprint(e.Data["priority"]) != "2" {
		t.Fatalf("route_added = %+v", e.Data)
	}
	if _, ok := e.Data["depot"]; ok {
		t.Fatalf("route_added names a depot the route does not filter on: %+v", e.Data)
	}

	gone, err := s.AddRoute(ctx, store.Route{Stage: "review", Group: "people"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoute(ctx, gone.ID, "jo"); err != nil {
		t.Fatal(err)
	}
	if e := last("route_removed"); e.Data["route"] != gone.ID || e.Data["stage"] != "review" || e.Data["group"] != "people" {
		t.Fatalf("route_removed = %+v", e.Data)
	}

	if _, err := s.DeleteGroup(ctx, "people", "jo"); err != nil {
		t.Fatal(err)
	}
	if e := last("group_removed"); e.Data["group"] != "people" || fmt.Sprint(e.Data["routes"]) != "["+kept.ID+"]" || fmt.Sprint(e.Data["released"]) != "0" {
		t.Fatalf("group_removed = %+v; want the group and the route that went with it", e.Data)
	}
	if n := count() - before; n != 5 {
		t.Fatalf("%d events; want 5: a group set, two routes added, one removed, the group removed", n)
	}

	// Nothing changed, so nothing is logged.
	if err := s.DeleteRoute(ctx, gone.ID, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteGroup(ctx, "people", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "bad", Runner: "herdr", Members: []string{"jo"}}, "jo"); err == nil {
		t.Fatal("a herdr group with members was stored")
	}
	if n := count() - before; n != 5 {
		t.Fatalf("%d events after removing what is gone and a refused group; want still 5", n)
	}
}

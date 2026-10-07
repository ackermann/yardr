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

	if err := s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 3, Members: []string{"jo"}, Config: map[string]any{"cmd": "/secret/command"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if e := last("group_set"); e.Data["name"] != "people" || e.Data["runner"] != "manual" || fmt.Sprint(e.Data["limit"]) != "3" || fmt.Sprint(e.Data["members"]) != "[jo]" {
		t.Fatalf("group_set = %+v", e.Data)
	} else if _, ok := e.Data["config"]; ok {
		t.Fatalf("group_set exposes runner config: %+v", e.Data)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 4}, "jo"); err != nil {
		t.Fatal(err)
	}
	if e := last("group_set"); e.Data["name"] != "people" || fmt.Sprint(e.Data["limit"]) != "4" {
		t.Fatalf("group_set on replacement = %+v", e.Data)
	}

	typ, depot, label := "task", "yard", "urgent"
	kept, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "people", Type: &typ, Depot: &depot, Label: &label, Priority: 2}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	e := last("route_added")
	if e.Data["id"] != kept.ID || e.Data["stage"] != "new" || e.Data["group"] != "people" || e.Data["type"] != "task" || e.Data["depot"] != "yard" || e.Data["label"] != "urgent" || fmt.Sprint(e.Data["priority"]) != "2" {
		t.Fatalf("route_added = %+v", e.Data)
	}

	gone, err := s.AddRoute(ctx, store.Route{Stage: "review", Group: "people", Type: &typ, Depot: &depot, Label: &label, Priority: 5}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoute(ctx, gone.ID, "jo"); err != nil {
		t.Fatal(err)
	}
	if e := last("route_removed"); e.Data["id"] != gone.ID || e.Data["stage"] != "review" || e.Data["group"] != "people" || e.Data["type"] != "task" || e.Data["depot"] != "yard" || e.Data["label"] != "urgent" || fmt.Sprint(e.Data["priority"]) != "5" {
		t.Fatalf("route_removed = %+v", e.Data)
	}

	if _, err := s.DeleteGroup(ctx, "people", "jo"); err != nil {
		t.Fatal(err)
	}
	if e := last("group_removed"); e.Data["name"] != "people" || fmt.Sprint(e.Data["routes"]) != "["+kept.ID+"]" || fmt.Sprint(e.Data["released"]) != "0" {
		t.Fatalf("group_removed = %+v; want the group and the route that went with it", e.Data)
	}
	if n := count() - before; n != 6 {
		t.Fatalf("%d events; want 6: two group sets, two routes added, one removed, the group removed", n)
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
	if n := count() - before; n != 6 {
		t.Fatalf("%d events after removing what is gone and a refused group; want still 6", n)
	}
}

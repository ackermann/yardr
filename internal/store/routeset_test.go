package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// routeByID reads the routes and returns the one with the id.
func routeByID(t *testing.T, s store.API, ctx context.Context, id string) store.Route {
	t.Helper()
	routes, err := s.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no route %s in %+v", id, routes)
	return store.Route{}
}

// routeSetEvent is the last route_set event, by jo and of no bead.
func routeSetEvent(t *testing.T, s store.API, ctx context.Context) store.Event {
	t.Helper()
	evs, err := s.EventsOfKind(ctx, "route_set", 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events of kind route_set = %+v, %v; want one", evs, err)
	}
	if evs[0].Actor != "jo" || evs[0].Bead != nil {
		t.Fatalf("route_set = %+v; want it by jo, of no bead", evs[0])
	}
	return evs[0]
}

// route set changes where a route's beads go — the first group with its
// condition, the further groups and the note — and keeps
// which beads they are and the route's id, logging route_set with the
// route as it was and as it is.
func TestSetRouteRetunesWhereBeadsGoAndKeepsTheRest(t *testing.T) {
	s, ctx := defaultsYard(t)
	typ, label := "task", "difficulty:3"
	saved := mustRoute(t, s, ctx, store.Route{Stage: "new", Type: &typ, Depot: routePtr("web"),
		Label: &label, Group: "builders", Else: []store.RouteGroup{{Name: "spare"}}})

	note := "hard work: strong group whatever the week says"
	ch, err := s.SetRoute(ctx, saved.ID, store.RouteEdit{
		Group: &store.RouteGroup{Name: "spare", When: "Weekly<target+10"},
		Else:  &[]store.RouteGroup{{Name: "builders"}},
		Note:  &note,
	}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Is.ID != saved.ID {
		t.Errorf("the set route's id = %s, want it kept as %s", ch.Is.ID, saved.ID)
	}
	if ch.Was.GroupList() != "builders else spare" || ch.Was.Note != "" {
		t.Errorf("the route as it was = %+v, want builders else spare with no note", ch.Was)
	}
	if ch.Is.GroupList() != "spare@Weekly<target+10 else builders" || ch.Is.Note != note {
		t.Errorf("the route as it is = %+v, want the new chain with the note", ch.Is)
	}
	for _, r := range []store.Route{ch.Was, ch.Is} {
		if r.Stage != "new" || r.Type == nil || *r.Type != typ || r.Depot == nil || *r.Depot != "web" ||
			len(r.Labels()) != 1 || r.Labels()[0] != label || r.BeadPriority != nil || r.Originator != nil {
			t.Errorf("the set route's beads moved: %+v", r)
		}
	}
	if got := routeByID(t, s, ctx, saved.ID); !reflect.DeepEqual(got, ch.Is) {
		t.Errorf("the route stored = %+v, want %+v", got, ch.Is)
	}
	e := routeSetEvent(t, s, ctx)
	was, _ := e.Data["was"].(map[string]any)
	is, _ := e.Data["is"].(map[string]any)
	if was["group"] != "builders" || is["group"] != "spare" || is["note"] != note ||
		was["priority"] != nil || is["priority"] != nil || is["depot"] != "web" {
		t.Errorf("route_set = %+v, want the route as it was and as it is", e.Data)
	}
}

// A set that changes nothing writes nothing and logs nothing: the route
// comes back as it stands, with no route_set event.
func TestSetRouteThatChangesNothingWritesNothing(t *testing.T) {
	s, ctx := defaultsYard(t)
	note := "the line's own"
	saved := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"),
		Group: "builders", Else: []store.RouteGroup{{Name: "spare"}}, Note: note})
	else_ := []store.RouteGroup{{Name: "spare"}}

	ch, err := s.SetRoute(ctx, saved.ID, store.RouteEdit{
		Group: &store.RouteGroup{Name: "builders"},
		Else:  &else_,
		Note:  &note,
	}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ch.Is, ch.Was) || !reflect.DeepEqual(ch.Is, *saved) {
		t.Errorf("a set that changes nothing came back was %+v, is %+v; want the route as it stands", ch.Was, ch.Is)
	}
	if got := routeByID(t, s, ctx, saved.ID); !reflect.DeepEqual(got, *saved) {
		t.Errorf("the route stored = %+v, want it untouched: %+v", got, *saved)
	}
	if evs, err := s.EventsOfKind(ctx, "route_set", 50); err != nil || len(evs) != 0 {
		t.Errorf("a set that changes nothing logged route_set: %+v, %v", evs, err)
	}
}

// The set holds the groups to what the add holds them to: every group
// named exists and is named once, wait ends the route, each condition
// parses, and a chain or condition names only groups that start agents.
// A refused set leaves the route as it stands.
func TestSetRouteHoldsGroupsAsAnAddDoes(t *testing.T) {
	s, ctx := defaultsYard(t)
	saved := mustRoute(t, s, ctx, store.Route{Stage: "new", Group: "builders"})
	group := func(name, when string) *store.RouteGroup { return &store.RouteGroup{Name: name, When: when} }
	for _, tc := range []struct {
		name string
		edit store.RouteEdit
		want string
	}{
		{"an unknown group", store.RouteEdit{Group: group("ghost", "")}, "group ghost"},
		{"a group named twice", store.RouteEdit{Else: &[]store.RouteGroup{{Name: "builders"}}}, "group builders is named twice"},
		{"wait first", store.RouteEdit{Group: group("wait", "")}, "wait ends a route (--else wait)"},
		{"wait with a condition", store.RouteEdit{Else: &[]store.RouteGroup{{Name: "wait", When: "Weekly<80"}}}, "wait takes no condition"},
		{"wait before the end", store.RouteEdit{Else: &[]store.RouteGroup{{Name: "wait"}, {Name: "spare"}}}, "nothing is tried after wait"},
		{"a condition that does not parse", store.RouteEdit{Group: group("builders", "Weekly")}, "is not <window><<percent used>"},
		{"a further group that starts no agents", store.RouteEdit{Else: &[]store.RouteGroup{{Name: "people"}}}, "group people is manual"},
		{"a condition on a group that starts no agents", store.RouteEdit{Group: group("people", "Weekly<80")}, "group people is manual"},
	} {
		if _, err := s.SetRoute(ctx, saved.ID, tc.edit, "jo"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("a set with %s: %v, want %q", tc.name, err, tc.want)
		}
	}
	if got := routeByID(t, s, ctx, saved.ID); !reflect.DeepEqual(got, *saved) {
		t.Errorf("a refused set left %+v, want the route as it stands: %+v", got, *saved)
	}
	if evs, err := s.EventsOfKind(ctx, "route_set", 50); err != nil || len(evs) != 0 {
		t.Errorf("a refused set logged route_set: %+v, %v", evs, err)
	}
}

// An add that names a standing route's id does not skip that route in
// the tie check: the store mints the new route's id, so a caller-named
// id is not the route's own, and the tie is refused as for any add.
func TestAddRouteNamingAStandingRouteStillRefusesItsTie(t *testing.T) {
	s, ctx := defaultsYard(t)
	standing := mustRoute(t, s, ctx, store.Route{Stage: "new", Group: "builders"})
	if _, err := s.AddRoute(ctx, store.Route{ID: standing.ID, Stage: "new", Group: "spare"}, "jo"); err == nil ||
		!strings.Contains(err.Error(), "route "+standing.ID+" already takes these beads") {
		t.Errorf("an add naming the standing route: %v, want its tie refused naming it", err)
	}
	if routes, err := s.Routes(ctx); err != nil || len(routes) != 1 {
		t.Errorf("routes after a refused add = %+v, %v; want the standing route alone", routes, err)
	}
}

// Setting a route that is not there is not found.
func TestSetRouteOnAnUnknownRoute(t *testing.T) {
	s, ctx := defaultsYard(t)
	note := "nowhere"
	if _, err := s.SetRoute(ctx, "no-such-route", store.RouteEdit{Note: &note}, "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a set of an unknown route: %v, want not found", err)
	}
}

// SetAsideRoutes names, for a depot, every yard default an own route
// sets aside there with the own route that states its key. A depot that
// states nothing sets nothing aside, and any other key leaves the default
// in place.
func TestSetAsideRoutesNamesTheOwnRouteThatSetsEachAside(t *testing.T) {
	s, ctx := defaultsYard(t)
	hard := "difficulty:3"
	plain := mustRoute(t, s, ctx, store.Route{Stage: "new", Group: "builders"})
	labelled := mustRoute(t, s, ctx, store.Route{Stage: "new", Label: &hard, Group: "hard-builders"})
	own := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "spare"})

	routes, err := s.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	aside := store.SetAsideRoutes(routes, "web", "jo")
	if len(aside) != 1 || aside[0].Route.ID != plain.ID || aside[0].Own.ID != own.ID {
		t.Errorf("web sets aside %+v, want the plain default %s set by the own route %s",
			aside, plain.ID, own.ID)
	}
	for _, a := range aside {
		if a.Route.ID == labelled.ID {
			t.Errorf("web sets aside the labelled default %s, want it left in place: no own route states its key", labelled.ID)
		}
	}
	if aside := store.SetAsideRoutes(routes, "other", "jo"); len(aside) != 0 {
		t.Errorf("other sets aside %+v, want nothing: it states no key", aside)
	}
}

// A bead claimed through the route keeps its group until its session
// ends; the next claim takes the new chain.
func TestClaimedBeadKeepsItsGroupWhileTheNextClaimTakesTheNewChain(t *testing.T) {
	s, ctx := defaultsYard(t)
	saved := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders"})
	first, err := s.CreateBead(ctx, store.NewBead{Title: "first", Depot: "web", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, first.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRoute(ctx, saved.ID, store.RouteEdit{Group: &store.RouteGroup{Name: "spare"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession(ctx, sess.ID); err != nil || got.Group != "builders" {
		t.Fatalf("the claimed bead's session = %+v, %v; want it still with builders", got, err)
	}
	second, err := s.CreateBead(ctx, store.NewBead{Title: "second", Depot: "web", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	won := winner(t, s, ctx, store.Bead{Stage: "new", Type: second.Type, Depot: routePtr("web"), Priority: second.Priority})
	if won == nil || won.ID != saved.ID || won.Group != "spare" {
		t.Fatalf("the next bead takes %+v, want the route with its new chain to spare", won)
	}
}

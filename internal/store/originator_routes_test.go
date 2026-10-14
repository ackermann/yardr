package store_test

import (
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// originatorRoutesYard is a yard whose operator is jo, with the user hal,
// a herdr group builders on the operator's login, a herdr group
// hal-builders on hal's, and a manual group humans.
func originatorRoutesYard(t *testing.T) (store.API, func(string) *string) {
	t.Helper()
	s, ctx := flowDepot(t)
	if _, err := s.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, g := range []store.Group{
		{Name: "builders", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "hal-builders", Runner: "herdr", Config: map[string]any{"kind": "claude", "login_for": "hal", "env_file": "/tmp/hal.env"}},
		{Name: "humans", Runner: "manual"},
	} {
		if err := s.PutGroup(ctx, g, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	return s, func(name string) *string { return &name }
}

// An originator route takes only its person's beads; a route without one
// takes only the operator's, however high its priority or specificity; any
// takes every originator's. A named originator counts in specificity and
// ByBead, any counts as none.
func TestRouteMatchesOriginator(t *testing.T) {
	s, str := originatorRoutesYard(t)
	ctx := t.Context()
	hal := "hal"
	any := store.OriginatorAny
	operatorRoute, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	halRoute, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "hal-builders", Originator: &hal}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	anyRoute, err := s.AddRoute(ctx, store.Route{Stage: "review", Group: "hal-builders", Originator: &any}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if operatorRoute.ByBead() || anyRoute.ByBead() || !halRoute.ByBead() {
		t.Fatalf("ByBead: operator %v, any %v, hal %v", operatorRoute.ByBead(), anyRoute.ByBead(), halRoute.ByBead())
	}
	routes, err := s.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store.SortRoutes(routes)
	// In new, hal's route (one filter) reads before the operator's (none).
	if routes[0].ID != halRoute.ID || routes[1].ID != operatorRoute.ID {
		t.Fatalf("sorted new routes = %v, want hal's first", routes)
	}
	for _, tc := range []struct {
		stage, originator, want string
	}{
		{"new", "jo", operatorRoute.ID},
		{"new", "", operatorRoute.ID},
		{"new", "hal", halRoute.ID},
		{"review", "jo", anyRoute.ID},
		{"review", "hal", anyRoute.ID},
		{"review", "", anyRoute.ID},
	} {
		b := store.Bead{Stage: tc.stage, Type: "task", Originator: tc.originator}
		if got := store.MatchRoute(routes, b, "jo"); got == nil || got.ID != tc.want {
			t.Errorf("stage %s originator %q took %+v, want %s", tc.stage, tc.originator, got, tc.want)
		}
	}
	// A general route of higher priority and greater specificity still
	// takes none of another person's beads.
	general, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders", Priority: 10, Label: str("difficulty:3")}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	_ = general
	routes, _ = s.Routes(ctx)
	store.SortRoutes(routes)
	b := store.Bead{Stage: "new", Type: "task", Labels: []string{"difficulty:3"}, Originator: "hal"}
	if got := store.MatchRoute(routes, b, "jo"); got == nil || got.ID != halRoute.ID {
		t.Fatalf("hal's labelled bead took %+v, want hal's route %s", got, halRoute.ID)
	}
	jb := store.Bead{Stage: "new", Type: "task", Labels: []string{"difficulty:3"}, Originator: "jo"}
	if got := store.MatchRoute(routes, jb, "jo"); got == nil || got.Group != "builders" || got.Originator != nil {
		t.Fatalf("jo's labelled bead took %+v, want the general operator route", got)
	}
}

// A route for an originator is personal when its first group declares
// login_for for them, else a subsidy route. The check reads the groups at
// dispatch time.
func TestPersonalOrSubsidyByFirstGroup(t *testing.T) {
	s, _ := originatorRoutesYard(t)
	ctx := t.Context()
	groups, err := s.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	hal := "hal"
	personal := store.Route{Stage: "new", Group: "hal-builders", Originator: &hal}
	subsidy := store.Route{Stage: "new", Group: "builders", Originator: &hal}
	operatorOwn := store.Route{Stage: "new", Group: "builders"}
	if !store.IsPersonalRoute(personal, byName, "hal", "jo") {
		t.Error("hal's route to hal-builders is not personal")
	}
	if store.IsPersonalRoute(subsidy, byName, "hal", "jo") {
		t.Error("hal's route to builders is personal, want subsidy")
	}
	if !store.IsPersonalRoute(operatorOwn, byName, "jo", "jo") {
		t.Error("the operator's route to builders is not personal")
	}
	if !store.IsPersonalRoute(operatorOwn, byName, "", "jo") {
		t.Error("a bead with no originator does not read as the operator's")
	}
	if store.IsPersonalRoute(personal, byName, "jo", "jo") {
		t.Error("the operator's beads on hal-builders read as personal")
	}
}

// login_for names a user that must exist, for herdr groups only; an
// unknown --originator is refused; any needs no user.
func TestLoginForAndOriginatorAreChecked(t *testing.T) {
	s, _ := originatorRoutesYard(t)
	ctx := t.Context()
	if err := s.PutGroup(ctx, store.Group{Name: "bad", Runner: "herdr", Config: map[string]any{"login_for": "mallory"}}, "jo"); err == nil ||
		!strings.Contains(err.Error(), "login_for mallory") {
		t.Errorf("a group for an unknown user: %v", err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "manual", Runner: "manual", Config: map[string]any{"login_for": "hal"}}, "jo"); err == nil ||
		!strings.Contains(err.Error(), "nothing reads it") {
		t.Errorf("a manual group with login_for: %v", err)
	}
	mallory := "mallory"
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders", Originator: &mallory}, "jo"); err == nil ||
		!strings.Contains(err.Error(), "--originator") {
		t.Errorf("a route for an unknown originator: %v", err)
	}
	any := store.OriginatorAny
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders", Originator: &any}, "jo"); err != nil {
		t.Errorf("a route for any: %v", err)
	}
	if _, err := s.AddUser(ctx, "any", "jo"); err == nil || !strings.Contains(err.Error(), "route keyword") {
		t.Errorf("a user named any: %v", err)
	}
}

// A route to a group that starts no agents takes every originator's beads:
// by hand without the flag, as AddRoute defaults it to any.
func TestNonHerdrRoutesTakeEveryOriginator(t *testing.T) {
	s, _ := originatorRoutesYard(t)
	ctx := t.Context()
	saved, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "humans"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Originator == nil || *saved.Originator != store.OriginatorAny {
		t.Fatalf("a manual route without --originator = %+v, want any", saved)
	}
	routes, _ := s.Routes(ctx)
	store.SortRoutes(routes)
	for _, orig := range []string{"jo", "hal", ""} {
		b := store.Bead{Stage: "new", Type: "task", Originator: orig}
		if got := store.MatchRoute(routes, b, "jo"); got == nil || got.ID != saved.ID {
			t.Errorf("originator %q took %+v, want the manual route", orig, got)
		}
	}
	evs, err := s.EventsOfKind(ctx, "route_added", 1)
	if err != nil || len(evs) != 1 || evs[0].Data["originator"] != store.OriginatorAny {
		t.Fatalf("route_added of an any route = %+v, %v", evs, err)
	}
}

// What the dispatcher is handed of a ready bead routes as the bead does,
// originator included.
func TestReadyBeadCarriesOriginator(t *testing.T) {
	s, _ := originatorRoutesYard(t)
	ctx := t.Context()
	hal := "hal"
	operatorRoute, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	halRoute, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "hal-builders", Originator: &hal}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateBead(ctx, store.NewBead{Title: "jo's", For: "jo"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateBead(ctx, store.NewBead{Title: "hal's", For: "hal"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := s.Dispatchable(ctx)
	if err != nil || len(ready) != 2 {
		t.Fatalf("ready = %+v, %v", ready, err)
	}
	routes, _ := s.Routes(ctx)
	store.SortRoutes(routes)
	want := map[string]string{"jo's": operatorRoute.ID, "hal's": halRoute.ID}
	for _, rb := range ready {
		whole, err := s.GetBead(ctx, rb.Bead)
		if err != nil {
			t.Fatal(err)
		}
		if rb.Originator != whole.Originator {
			t.Fatalf("ready bead %s carries %q, the bead %q", whole.Title, rb.Originator, whole.Originator)
		}
		part := store.Bead{ID: rb.Bead, Stage: rb.Stage, Type: rb.Type, Depot: rb.Depot, Labels: rb.Labels, Priority: rb.Priority, Originator: rb.Originator}
		a, b := store.MatchRoute(routes, part, "jo"), store.MatchRoute(routes, *whole, "jo")
		if a == nil || b == nil || a.ID != b.ID || a.ID != want[whole.Title] {
			t.Errorf("bead %s: as a ready bead it takes %+v, whole %+v, want %s", whole.Title, a, b, want[whole.Title])
		}
	}
}

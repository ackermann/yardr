package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// defaultsYard is a yard whose operator is jo, with the user hal, the
// depots yard (from flowDepot), web and other, and the groups the
// yard-default tests route to.
func defaultsYard(t *testing.T) (store.API, context.Context) {
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
	for _, name := range []string{"web", "other"} {
		if err := s.PutDepot(ctx, store.Depot{Name: name, Base: "origin/main"}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []store.Group{
		{Name: "builders", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "spare", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "reviewers", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "web-reviewers", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "pr-reviewers", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "hard-builders", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "people", Runner: "manual", Members: []string{"jo"}},
	} {
		if err := s.PutGroup(ctx, g, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx
}

func routePtr(s string) *string { return &s }

func mustRoute(t *testing.T, s store.API, ctx context.Context, r store.Route) *store.Route {
	t.Helper()
	saved, err := s.AddRoute(ctx, r, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func winner(t *testing.T, s store.API, ctx context.Context, b store.Bead) *store.Route {
	t.Helper()
	routes, err := s.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return store.MatchRoute(routes, b, "jo")
}

// An own route with a default's key sets it aside for that depot alone:
// the depot takes its own route, another depot keeps the default, and a
// bead with no depot has the default.
func TestOwnRouteSetsDefaultAsideForItsDepotAlone(t *testing.T) {
	s, ctx := defaultsYard(t)
	// The default has the higher priority, but an own route's key sets it
	// aside regardless of the ranking each would have alone.
	def := mustRoute(t, s, ctx, store.Route{Stage: "review", Group: "reviewers", Priority: 3})
	own := mustRoute(t, s, ctx, store.Route{Stage: "review", Depot: routePtr("web"), Group: "web-reviewers"})

	routes, err := s.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(rs []store.Route) map[string]bool {
		out := map[string]bool{}
		for _, r := range rs {
			out[r.ID] = true
		}
		return out
	}
	if eff := ids(store.EffectiveRoutes(routes, routePtr("web"), "jo")); !eff[own.ID] || eff[def.ID] {
		t.Errorf("web's effective routes = %v, want its own %s without the default %s", eff, own.ID, def.ID)
	}
	if eff := ids(store.EffectiveRoutes(routes, routePtr("other"), "jo")); !eff[def.ID] || eff[own.ID] {
		t.Errorf("other's effective routes = %v, want the default %s without web's own %s", eff, def.ID, own.ID)
	}
	if eff := ids(store.EffectiveRoutes(routes, nil, "jo")); !eff[def.ID] || eff[own.ID] {
		t.Errorf("a bead with no depot has effective routes %v, want the default alone", eff)
	}
	for depot, want := range map[string]string{"web": own.ID, "other": def.ID} {
		b := store.Bead{Stage: "review", Type: "task", Depot: routePtr(depot), Priority: store.DefaultPriority}
		if got := winner(t, s, ctx, b); got == nil || got.ID != want {
			t.Errorf("a review bead in %s took %+v, want %s", depot, got, want)
		}
	}
	b := store.Bead{Stage: "review", Type: "task", Priority: store.DefaultPriority}
	if got := winner(t, s, ctx, b); got == nil || got.ID != def.ID {
		t.Errorf("a review bead with no depot took %+v, want the default %s", got, def.ID)
	}
}

// An own route with any other key leaves every default in place: a depot
// that routes its plain beads itself still sends its hard beads by the
// yard's difficulty:3 default.
func TestOwnRouteWithAnotherKeyLeavesTheDefaults(t *testing.T) {
	s, ctx := defaultsYard(t)
	hard := "difficulty:3"
	def := mustRoute(t, s, ctx, store.Route{Stage: "new", Label: &hard, Priority: 3, Group: "hard-builders"})
	own := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders"})

	routes, err := s.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, r := range store.EffectiveRoutes(routes, routePtr("web"), "jo") {
		found[r.ID] = true
	}
	if !found[def.ID] || !found[own.ID] {
		t.Fatalf("web's effective routes = %v, want the labelled default and its own plain route", found)
	}
	hardBead := store.Bead{Stage: "new", Type: "task", Depot: routePtr("web"), Labels: []string{hard}, Priority: store.DefaultPriority}
	if got := winner(t, s, ctx, hardBead); got == nil || got.ID != def.ID {
		t.Errorf("a hard bead in web took %+v, want the default %s", got, def.ID)
	}
	plain := store.Bead{Stage: "new", Type: "task", Depot: routePtr("web"), Priority: store.DefaultPriority}
	if got := winner(t, s, ctx, plain); got == nil || got.ID != own.ID {
		t.Errorf("a plain bead in web took %+v, want its own %s", got, own.ID)
	}
}

// The pull-request case: a no-type default and an own route typed pr
// state different keys, so both stand; the typed route wins for a
// pull-request bead and the default for a plain one.
func TestDefaultAndTypedOwnRouteForPullRequests(t *testing.T) {
	s, ctx := defaultsYard(t)
	def := mustRoute(t, s, ctx, store.Route{Stage: "review", Group: "reviewers"})
	pr := "pr"
	own := mustRoute(t, s, ctx, store.Route{Stage: "review", Type: &pr, Depot: routePtr("web"), Group: "pr-reviewers"})

	pull := store.Bead{Stage: "review", Type: "pr", Depot: routePtr("web"), Priority: store.DefaultPriority}
	if got := winner(t, s, ctx, pull); got == nil || got.ID != own.ID {
		t.Errorf("a pull-request bead took %+v, want the typed own route %s", got, own.ID)
	}
	plain := store.Bead{Stage: "review", Type: "task", Depot: routePtr("web"), Priority: store.DefaultPriority}
	if got := winner(t, s, ctx, plain); got == nil || got.ID != def.ID {
		t.Errorf("a plain bead took %+v, want the default %s", got, def.ID)
	}
	away := store.Bead{Stage: "review", Type: "pr", Depot: routePtr("other"), Priority: store.DefaultPriority}
	if got := winner(t, s, ctx, away); got == nil || got.ID != def.ID {
		t.Errorf("a pull-request bead in another depot took %+v, want the default %s", got, def.ID)
	}
}

// Two beads of one depot, one type and one stage, one pinning a flow
// that shares the stage's name and one following the depot's pointer,
// take the same winner: the match reads no flow, and a label on the
// pinned bead is what separates them.
func TestPinnedFlowSharesTheRoute(t *testing.T) {
	s, ctx := defaultsYard(t)
	def := mustRoute(t, s, ctx, store.Route{Stage: "review", Group: "reviewers"})
	pinned := "pinned"
	labelled := mustRoute(t, s, ctx, store.Route{Stage: "review", Depot: routePtr("web"), Label: &pinned, Priority: 5, Group: "web-reviewers"})

	plain := store.Bead{Stage: "review", Type: "task", Depot: routePtr("web"), Priority: store.DefaultPriority}
	withPin := plain
	withPin.Metadata = map[string]any{"flow": "train"}
	if store.PinnedFlow(&plain) != "" || store.PinnedFlow(&withPin) != "train" {
		t.Fatalf("the pins read %q and %q, want none and train", store.PinnedFlow(&plain), store.PinnedFlow(&withPin))
	}
	a, b := winner(t, s, ctx, plain), winner(t, s, ctx, withPin)
	if a == nil || b == nil || a.ID != def.ID || b.ID != def.ID {
		t.Errorf("the unpinned bead took %+v and the pinned one %+v, want the default %s for both", a, b, def.ID)
	}
	withPin.Labels = []string{pinned}
	if got := winner(t, s, ctx, withPin); got == nil || got.ID != labelled.ID {
		t.Errorf("the pinned bead with the label took %+v, want the labelled route %s", got, labelled.ID)
	}
}

// At one priority and as specific as each other, an own route is read
// before a default, whatever their ids: the two state different keys, so
// both stand in the depot's effective set and the scope leg places them.
func TestOwnRouteRanksBeforeDefault(t *testing.T) {
	hard := "difficulty:3"
	routes := []store.Route{
		{ID: "a-default", Stage: "new", Label: &hard, Group: "hard-builders"},
		{ID: "z-own", Stage: "new", Depot: routePtr("web"), Group: "builders"},
	}
	store.SortRoutes(routes)
	if routes[0].ID != "z-own" {
		t.Fatalf("sorted routes = %+v, want the own route first", routes)
	}
	bead := store.Bead{Stage: "new", Type: "task", Depot: routePtr("web"), Labels: []string{hard}, Priority: store.DefaultPriority}
	if got := store.MatchRoute(routes, bead, "jo"); got == nil || got.ID != "z-own" {
		t.Errorf("the bead took %+v, want the own route", got)
	}
	away := store.Bead{Stage: "new", Type: "task", Depot: routePtr("other"), Labels: []string{hard}, Priority: store.DefaultPriority}
	if got := store.MatchRoute(routes, away, "jo"); got == nil || got.ID != "a-default" {
		t.Errorf("the bead in another depot took %+v, want the default", got)
	}
}

// A route that exactly ties one of the same scope is refused, naming the
// standing route, its beads and the fix; the same route again is refused
// as the same route. Another scope, another key or another priority
// stands.
func TestAddRouteRefusesAnExactTie(t *testing.T) {
	t.Run("own against own, another chain", func(t *testing.T) {
		s, ctx := defaultsYard(t)
		first := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders"})
		_, err := s.AddRoute(ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "spare"}, "jo")
		want := []string{"route " + first.ID + " already takes these beads in depot web at priority 0",
			"(new, any type, no label, any bead priority, the operator's beads) to builders",
			"Two routes that take the same beads at the same priority are ordered by id, which nobody chose",
			"yardr route rm " + first.ID}
		if err == nil {
			t.Fatalf("the tying route was added, want a refusal naming %q", want[0])
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("the refusal = %q, want it to say %q", err, w)
			}
		}
		if routes, err := s.Routes(ctx); err != nil || len(routes) != 1 {
			t.Errorf("routes after the refusal = %+v, %v, want the standing route alone", routes, err)
		}
	})
	t.Run("default against default", func(t *testing.T) {
		s, ctx := defaultsYard(t)
		first := mustRoute(t, s, ctx, store.Route{Stage: "new", Group: "builders"})
		_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "spare"}, "jo")
		if err == nil || !strings.Contains(err.Error(), "route "+first.ID+" already takes these beads as a yard default at priority 0") {
			t.Fatalf("the tying default: %v, want it to name %s as a yard default", err, first.ID)
		}
	})
	t.Run("the same route again", func(t *testing.T) {
		s, ctx := defaultsYard(t)
		first := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders"})
		_, err := s.AddRoute(ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders"}, "jo")
		if err == nil || !strings.Contains(err.Error(), "route "+first.ID+" already takes these beads") ||
			!strings.Contains(err.Error(), "the same route") || !strings.Contains(err.Error(), "no add is needed") {
			t.Fatalf("the duplicate: %v, want the same route named", err)
		}
	})
	t.Run("the same route with a changed note", func(t *testing.T) {
		s, ctx := defaultsYard(t)
		first := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders", Note: "old note"})
		_, err := s.AddRoute(ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders", Note: "new note"}, "jo")
		if err == nil || !strings.Contains(err.Error(), "the same route's note differs") ||
			!strings.Contains(err.Error(), "yardr route rm "+first.ID) {
			t.Fatalf("the changed note: %v, want a route replacement command naming %s", err, first.ID)
		}
	})
	t.Run("a quota threshold is the chain, not the key", func(t *testing.T) {
		s, ctx := defaultsYard(t)
		// The yard's live duplicate: the same beads at the same priority,
		// told apart only by the threshold they gate on.
		first := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("yard"), Group: "builders",
			When: "Weekly<target+10", Else: []store.RouteGroup{{Name: "spare"}}})
		_, err := s.AddRoute(ctx, store.Route{Stage: "new", Depot: routePtr("yard"), Group: "builders",
			When: "Weekly<target", Else: []store.RouteGroup{{Name: "spare"}}}, "jo")
		if err == nil || !strings.Contains(err.Error(), "route "+first.ID+" already takes these beads in depot yard") ||
			!strings.Contains(err.Error(), "yardr-builders") && !strings.Contains(err.Error(), "builders@Weekly<target+10 else spare") {
			t.Fatalf("the second threshold: %v, want the first route named with its chain", err)
		}
	})
	t.Run("the labels are a set", func(t *testing.T) {
		s, ctx := defaultsYard(t)
		size, hard := "size:large", "difficulty:3"
		first := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders", Label: &size, AndLabels: []string{hard}})
		_, err := s.AddRoute(ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "spare", Label: &hard, AndLabels: []string{size}}, "jo")
		if err == nil || !strings.Contains(err.Error(), "route "+first.ID+" already takes these beads") {
			t.Fatalf("the same labels in another order: %v, want a tie naming %s", err, first.ID)
		}
	})
	t.Run("naming the operator is naming nobody", func(t *testing.T) {
		s, ctx := defaultsYard(t)
		jo := "jo"
		first := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders", Originator: &jo})
		_, err := s.AddRoute(ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "spare"}, "jo")
		if err == nil || !strings.Contains(err.Error(), "route "+first.ID+" already takes these beads") {
			t.Fatalf("the route naming nobody against the one naming the operator: %v, want a tie naming %s", err, first.ID)
		}
	})
	t.Run("a default and an own route of one key stand", func(t *testing.T) {
		s, ctx := defaultsYard(t)
		mustRoute(t, s, ctx, store.Route{Stage: "new", Group: "builders"})
		own := mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "spare"})
		routes, err := s.Routes(ctx)
		if err != nil || len(routes) != 2 {
			t.Fatalf("routes = %+v, %v, want the default and the own route", routes, err)
		}
		if eff := store.EffectiveRoutes(routes, routePtr("web"), "jo"); len(eff) != 1 || eff[0].ID != own.ID {
			t.Errorf("web's effective routes = %+v, want its own route alone", eff)
		}
	})
	t.Run("another priority stands", func(t *testing.T) {
		s, ctx := defaultsYard(t)
		mustRoute(t, s, ctx, store.Route{Stage: "new", Group: "builders"})
		if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "spare", Priority: -1}, "jo"); err != nil {
			t.Fatalf("the same beads at a lower priority: %v, want it to stand as a standby", err)
		}
	})
}

// A note is stored with the route and carried by its event; a route
// without one is stored, matched and logged as it always was.
func TestRouteNoteIsStoredAndLogged(t *testing.T) {
	s, ctx := defaultsYard(t)
	saved := mustRoute(t, s, ctx, store.Route{Stage: "new", Group: "builders", Note: "hard beads: heavy group or wait"})
	if saved.Note != "hard beads: heavy group or wait" {
		t.Fatalf("the saved route = %+v, want its note kept", saved)
	}
	routes, err := s.Routes(ctx)
	if err != nil || len(routes) != 1 || routes[0].Note != saved.Note {
		t.Fatalf("routes = %+v, %v, want the note read back", routes, err)
	}
	evs, err := s.EventsOfKind(ctx, "route_added", 1)
	if err != nil || len(evs) != 1 || evs[0].Data["note"] != saved.Note {
		t.Fatalf("route_added = %+v, %v, want the note in it", evs, err)
	}
	plain := mustRoute(t, s, ctx, store.Route{Stage: "review", Group: "reviewers"})
	if raw, _ := json.Marshal(plain); strings.Contains(string(raw), "note") {
		t.Errorf("a route without a note is stored as %s, want no note in it", raw)
	}
	evs, err = s.EventsOfKind(ctx, "route_added", 2)
	if err != nil || len(evs) != 2 || evs[1].Data["note"] != nil {
		t.Fatalf("route_added of the plain route = %+v, %v, want no note in it", evs, err)
	}
	bead := store.Bead{Stage: "new", Type: "task", Priority: store.DefaultPriority}
	if got := winner(t, s, ctx, bead); got == nil || got.ID != saved.ID {
		t.Errorf("a bead took %+v, want the noted route %s: the note takes no part in matching", got, saved.ID)
	}
	if err := s.DeleteRoute(ctx, saved.ID, "jo"); err != nil {
		t.Fatal(err)
	}
	removed, err := s.EventsOfKind(ctx, "route_removed", 1)
	if err != nil || len(removed) != 1 || removed[0].Data["note"] != saved.Note {
		t.Fatalf("route_removed = %+v, %v, want the route's note", removed, err)
	}
}

// Label sets can contain arbitrary strings. Length-prefixing their sorted
// values keeps an embedded NUL from making two different sets share a key.
func TestDifferentLabelSetsDoNotCollideInMatchKey(t *testing.T) {
	first := []string{"a\x00b", "c"}
	second := []string{"a", "b\x00c"}
	routes := []store.Route{
		{ID: "default", Stage: "new", Group: "hard-builders", Label: &first[0], AndLabels: first[1:]},
		{ID: "own", Stage: "new", Depot: routePtr("web"), Group: "builders", Label: &second[0], AndLabels: second[1:]},
	}
	bead := store.Bead{Stage: "new", Type: "task", Depot: routePtr("web"), Labels: first, Priority: store.DefaultPriority}
	if got := store.MatchRoute(routes, bead, "jo"); got == nil || got.ID != "default" {
		t.Fatalf("a bead with the default's distinct label set took %+v, want the default", got)
	}
}

// RankLeg names the first ranking leg on which two routes differ, in
// SortRoutes order: priority, specificity, own-before-default, id. For
// every pair of a mixed table in ranked order, the legs before the
// named one tie and the named one differs; every leg is named by some
// pair.
func TestRankLegNamesTheFirstLegOnWhichRoutesDiffer(t *testing.T) {
	beadPri := 1
	hal := "hal"
	routes := []store.Route{
		{ID: "plain-default", Stage: "new", Group: "builders"},
		{ID: "label-default", Stage: "new", Label: routePtr("hard"), Group: "builders"},
		{ID: "typed-default", Stage: "new", Type: routePtr("bug"), Group: "builders"},
		{ID: "pri-default", Stage: "new", Label: routePtr("hard"), Priority: 3, Group: "builders"},
		{ID: "plain-own", Stage: "new", Depot: routePtr("web"), Group: "builders"},
		{ID: "label-own", Stage: "new", Depot: routePtr("web"), Label: routePtr("hard"), Group: "builders"},
		{ID: "exact-own", Stage: "new", Depot: routePtr("web"), Label: routePtr("hard"),
			BeadPriority: &beadPri, Originator: &hal, Group: "builders"},
	}
	if got := store.RouteSpecificity(routes[6]); got != 4 {
		t.Fatalf("the exact own route asks for %d, want 4: the depot, the label, the bead priority and the originator", got)
	}
	any := store.OriginatorAny
	unexact := routes[6]
	unexact.Originator = &any
	if got := store.RouteSpecificity(unexact); got != 3 {
		t.Fatalf("the route with originator any asks for %d, want 3: any counts as none", got)
	}
	store.SortRoutes(routes)
	seen := map[string]bool{}
	for i := range routes {
		for j := i + 1; j < len(routes); j++ {
			a, b := routes[i], routes[j]
			got := store.RankLeg(a, b)
			seen[got] = true
			priTies := a.Priority == b.Priority
			specTies := store.RouteSpecificity(a) == store.RouteSpecificity(b)
			scopeTies := (a.Depot == nil) == (b.Depot == nil)
			var ok bool
			switch got {
			case store.LegPriority:
				ok = !priTies
			case store.LegSpecificity:
				ok = priTies && !specTies
			case store.LegOwnBeforeDefault:
				ok = priTies && specTies && !scopeTies
			case store.LegID:
				ok = priTies && specTies && scopeTies && a.ID != b.ID
			}
			if !ok {
				t.Errorf("RankLeg(%s, %s) = %q, which the pair does not differ on first", a.ID, b.ID, got)
			}
		}
	}
	for _, leg := range []string{store.LegPriority, store.LegSpecificity, store.LegOwnBeforeDefault, store.LegID} {
		if !seen[leg] {
			t.Errorf("no pair is placed by %q", leg)
		}
	}
}

// Route.Matches is the per-route check MatchRoute ranks by: the winner
// matches, every route ranked above it does not, and where nothing
// matches nothing does.
func TestRouteMatchesAgreesWithMatchRoute(t *testing.T) {
	s, ctx := defaultsYard(t)
	hard := "hard"
	mustRoute(t, s, ctx, store.Route{Stage: "new", Group: "builders"})
	mustRoute(t, s, ctx, store.Route{Stage: "new", Label: &hard, Group: "hard-builders", Priority: 3})
	mustRoute(t, s, ctx, store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders", Priority: 1})
	beads := []store.Bead{
		{Stage: "new", Type: "task", Depot: routePtr("web"), Priority: store.DefaultPriority},
		{Stage: "new", Type: "task", Depot: routePtr("web"), Labels: []string{hard}, Priority: store.DefaultPriority},
		{Stage: "new", Type: "task", Depot: routePtr("other"), Labels: []string{hard}, Priority: store.DefaultPriority},
		{Stage: "review", Type: "task", Depot: routePtr("web"), Priority: store.DefaultPriority},
	}
	routes, err := s.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range beads {
		eff := store.EffectiveRoutes(routes, b.Depot, "jo")
		store.SortRoutes(eff)
		w := store.MatchRoute(routes, b, "jo")
		if w == nil {
			for _, r := range eff {
				if r.Matches(b, "jo") {
					t.Errorf("route %s matches %+v, which MatchRoute matches nothing for", r.ID, b)
				}
			}
			continue
		}
		above := true
		for _, r := range eff {
			if r.ID == w.ID {
				above = false
				if !r.Matches(b, "jo") {
					t.Errorf("the winner %s does not match %+v", r.ID, b)
				}
				continue
			}
			if above && r.Matches(b, "jo") {
				t.Errorf("route %s matches %+v above the winner %s", r.ID, b, w.ID)
			}
		}
		if above {
			t.Errorf("the winner %s is not in the effective set of %+v", w.ID, b)
		}
	}
}

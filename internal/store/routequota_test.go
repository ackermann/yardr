package store_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// quotaGroups are two groups that start agents, a and b, and one of
// people.
func quotaGroups(t *testing.T) (store.API, func(first string, further ...string) (*store.Route, error)) {
	t.Helper()
	s, ctx := flowDepot(t)
	for _, g := range []store.Group{
		{Name: "a", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "b", Runner: "herdr", Config: map[string]any{"kind": "codex"}},
		{Name: "people", Runner: "manual", Members: []string{"jo"}},
	} {
		if err := s.PutGroup(ctx, g, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	return s, func(first string, further ...string) (*store.Route, error) {
		g := store.ParseRouteGroup(first)
		r := store.Route{Stage: "new", Group: g.Name, When: g.When}
		for _, e := range further {
			r.Else = append(r.Else, store.ParseRouteGroup(e))
		}
		return s.AddRoute(ctx, r, "jo")
	}
}

// A route keeps its further groups in order, each with its condition, and
// says them as they were typed; a route with one group and no condition is
// stored, listed and logged as it always was.
func TestRouteNamesFurtherGroupsWithConditions(t *testing.T) {
	s, add := quotaGroups(t)
	ctx := t.Context()
	plain, err := add("people")
	if err != nil {
		t.Fatal(err)
	}
	evs, err := s.EventsOfKind(ctx, "route_added", 1)
	if err != nil || len(evs) != 1 || evs[0].Data["group"] != "people" || evs[0].Data["groups"] != nil {
		t.Fatalf("route_added of a route with one group = %+v, %v", evs, err)
	}
	saved, err := add("a@Weekly<80,5h<90", "b")
	if err != nil {
		t.Fatal(err)
	}
	evs, err = s.EventsOfKind(ctx, "route_added", 1)
	if err != nil || len(evs) != 1 || evs[0].Data["group"] != "a" ||
		fmt.Sprint(evs[0].Data["groups"]) != "[map[name:a when:Weekly<80,5h<90] map[name:b]]" {
		t.Fatalf("route_added = %+v, %v", evs, err)
	}
	routes, err := s.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		switch r.ID {
		case plain.ID:
			if want := []store.RouteGroup{{Name: "people"}}; !reflect.DeepEqual(r.Groups(), want) || r.Quotaed() ||
				r.GroupList() != "people" || r.Worker() != "people" || r.Further() != nil {
				t.Fatalf("the plain route: groups %+v, list %q", r.Groups(), r.GroupList())
			}
		case saved.ID:
			want := []store.RouteGroup{{Name: "a", When: "Weekly<80,5h<90"}, {Name: "b"}}
			if !reflect.DeepEqual(r.Groups(), want) || !r.Quotaed() || r.GroupList() != "a@Weekly<80,5h<90 else b" ||
				r.Worker() != "a" || !reflect.DeepEqual(r.Further(), []string{"b"}) || !r.Names("b") || r.Names("people") {
				t.Fatalf("the route: groups %+v, list %q", r.Groups(), r.GroupList())
			}
		default:
			t.Fatalf("a route nobody added: %+v", r)
		}
	}
}

// A further group or a condition is for groups that start agents, every
// group named must exist, and a condition must parse; what is refused
// stores nothing.
func TestAddRouteRefusesWhatAQuotaRouteMayNotName(t *testing.T) {
	s, add := quotaGroups(t)
	for _, tc := range []struct {
		first   string
		further []string
		want    string
	}{
		{"a", []string{"people"}, "group people is manual: --else and a quota condition are for groups that start agents"},
		{"people", []string{"a"}, "group people is manual: --else and a quota condition are for groups that start agents"},
		{"people@Weekly<80", nil, "group people is manual: --else and a quota condition are for groups that start agents"},
		{"a", []string{"ghost"}, "group ghost: not found"},
		{"a", []string{"b", "a"}, "group a is named twice"},
		{"a@Weekly", nil, `condition "Weekly": "Weekly" is not <window><<percent used>`},
		{"a@Weekly<lots", nil, `"Weekly<lots" is not <window><<percent used>`},
		{"a@Weekly<80,5h<0", nil, `"5h<0" is not <window><<percent used>`},
		{"a", []string{"b@<101"}, `group b: condition "<101"`},
		{"a@Weekly<target+lots", nil, `"Weekly<target+lots" is not <window><<percent used>, as in Weekly<80`},
		{"a@Weekly<target+lots", nil, `nor <window><target> with an optional margin, as in Weekly<target+10`},
		{"a@Weekly<target+", nil, `nor <window><target> with an optional margin`},
		{"a@Weekly<targets", nil, `nor <window><target> with an optional margin`},
		{"a@Weekly<Target", nil, `nor <window><target> with an optional margin`},
		{"a@Weekly<lots", nil, `nor <window><target> with an optional margin`},
	} {
		if _, err := add(tc.first, tc.further...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("route %s else %v: %v, want %q", tc.first, tc.further, err, tc.want)
		}
	}
	if _, err := add("a", "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a further group that does not exist: %v, want ErrNotFound", err)
	}
	if routes, err := s.Routes(t.Context()); err != nil || len(routes) != 0 {
		t.Fatalf("routes after the refusals = %+v, %v", routes, err)
	}
}

// A group a route names with --else or a condition cannot be saved with
// another runner; with the same runner it can, and a group only a plain
// route names changes as it always could. A group that goes takes every
// route that names it.
func TestGroupOfAQuotaRouteKeepsItsRunner(t *testing.T) {
	s, add := quotaGroups(t)
	ctx := t.Context()
	if err := s.PutGroup(ctx, store.Group{Name: "c", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := add("c"); err != nil {
		t.Fatal(err)
	}
	r, err := add("a@Weekly<80", "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		err := s.PutGroup(ctx, store.Group{Name: name, Runner: "manual"}, "jo")
		if err == nil || !strings.Contains(err.Error(), "route "+r.ID+" (a@Weekly<80 else b, stage new) names it with --else or a quota condition") ||
			!strings.Contains(err.Error(), "it cannot become manual") {
			t.Fatalf("group %s to manual: %v", name, err)
		}
		if g, _ := s.GetGroup(ctx, name); g.Runner != "herdr" {
			t.Fatalf("group %s became %s", name, g.Runner)
		}
		if err := s.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "kimi"}}, "jo"); err != nil {
			t.Fatalf("group %s saved again as herdr: %v", name, err)
		}
	}
	if err := s.PutGroup(ctx, store.Group{Name: "c", Runner: "manual"}, "jo"); err != nil {
		t.Fatalf("a group only a plain route names, to manual: %v", err)
	}
	if _, err := s.DeleteGroup(ctx, "b", "jo"); err != nil {
		t.Fatal(err)
	}
	routes, err := s.Routes(ctx)
	if err != nil || len(routes) != 1 || routes[0].Worker() != "c" {
		t.Fatalf("routes after b went = %+v, %v; want the one to c", routes, err)
	}
}

// Choose takes the first group whose condition holds from the windows it
// is handed, asks only for groups with a condition and only as far as it
// has to, and says of each group it passed over why.
func TestRouteChoose(t *testing.T) {
	pct := func(p float64) *float64 { return &p }
	early, late := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	claude := func(week, five float64) store.Quota {
		return store.Quota{Provider: "Claude", Windows: []store.QuotaWindow{
			{Label: "5h", UsedPercent: pct(five), ResetsAt: &early},
			{Label: "Weekly", UsedPercent: pct(week), ResetsAt: &late},
			{Label: "Weekly Fable", UsedPercent: pct(100)},
		}}
	}
	// paced is claude with the share of each window that has passed, as a
	// source that knows the pace reports it.
	paced := func(week, five, weekTarget, fiveTarget float64) store.Quota {
		q := claude(week, five)
		q.Windows[0].TargetPercent = pct(fiveTarget)
		q.Windows[1].TargetPercent = pct(weekTarget)
		return q
	}
	route := func(first string, further ...string) store.Route {
		g := store.ParseRouteGroup(first)
		r := store.Route{Group: g.Name, When: g.When}
		for _, e := range further {
			r.Else = append(r.Else, store.ParseRouteGroup(e))
		}
		return r
	}
	for name, tc := range map[string]struct {
		route store.Route
		quota map[string]store.Quota
		want  store.RouteChoice
		asked string
	}{
		"no condition asks nobody": {route("a", "b"), nil, store.RouteChoice{Group: "a"}, ""},
		"under the threshold": {route("a@Weekly<80,5h<90", "b"), map[string]store.Quota{"a": claude(79.9, 10)},
			store.RouteChoice{Group: "a"}, "a"},
		// A window the condition does not name does not gate: Weekly Fable is used up.
		"at the threshold": {route("a@Weekly<80", "b"), map[string]store.Quota{"a": claude(80, 10)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 80% used, the route asks under 80", ResetsAt: &late}}}, "a"},
		"the second passed over too": {route("a@Weekly<80", "b@5h<50", "c"),
			map[string]store.Quota{"a": claude(83.26, 10), "b": claude(1, 50)},
			store.RouteChoice{Group: "c", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 83.3% used, the route asks under 80", ResetsAt: &late},
				{Group: "b", Reason: "Claude's 5h window is 50% used, the route asks under 50", ResetsAt: &early}}}, "a b"},
		"nobody": {route("a@Weekly<80,5h<90"), map[string]store.Quota{"a": claude(90, 95)},
			store.RouteChoice{Passed: []store.PassedGroup{{Group: "a", ResetsAt: &early,
				Reason: "Claude's Weekly window is 90% used, the route asks under 80; Claude's 5h window is 95% used, the route asks under 90"}}}, "a"},
		"every window": {route("a@<90", "b"), map[string]store.Quota{"a": claude(10, 10)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly Fable window is 100% used, the route asks under 90"}}}, "a"},
		"limits not known": {route("a@Weekly<80", "b"), map[string]store.Quota{"a": {Unknown: "no source of this yard answers for provider muse"}},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: no source of this yard answers for provider muse"}, "a"},
		"a window the report lacks": {route("a@Monthly<80,5h<90", "b"), map[string]store.Quota{"a": claude(99, 10)},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: Claude's report has no window labelled Monthly"}, "a"},
		"a window that failed is known": {route("a@Monthly<80,5h<90", "b"), map[string]store.Quota{"a": claude(1, 90)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's 5h window is 90% used, the route asks under 90", ResetsAt: &early}}}, "a"},
		"a window without a percentage": {route("a@Weekly<80"), map[string]store.Quota{"a": {Windows: []store.QuotaWindow{{Label: "Weekly"}}}},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: its provider's report does not say how far the Weekly window is used"}, "a"},
		"under target plus the margin": {route("a@Weekly<target+10", "b"), map[string]store.Quota{"a": paced(20, 10, 17, 50)},
			store.RouteChoice{Group: "a"}, "a"},
		"past target plus the margin": {route("a@Weekly<target+10", "b"), map[string]store.Quota{"a": paced(38, 10, 17, 50)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 38% used, the route asks under target 17% + 10", ResetsAt: &late}}}, "a"},
		"at the target": {route("a@Weekly<target", "b"), map[string]store.Quota{"a": paced(17, 10, 17, 50)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 17% used, the route asks under target 17%", ResetsAt: &late}}}, "a"},
		"a margin below": {route("a@Weekly<target-5", "b"), map[string]store.Quota{"a": paced(11, 10, 17, 50)},
			store.RouteChoice{Group: "a"}, "a"},
		"past target minus the margin": {route("a@Weekly<target-5", "b"), map[string]store.Quota{"a": paced(12, 10, 17, 50)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 12% used, the route asks under target 17% - 5", ResetsAt: &late}}}, "a"},
		"a target and a number in one condition": {route("a@Weekly<target+10,5h<90", "b"), map[string]store.Quota{"a": paced(20, 95, 17, 50)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's 5h window is 95% used, the route asks under 90", ResetsAt: &early}}}, "a"},
		"a window with no target": {route("a@Weekly<target+10", "b"), map[string]store.Quota{"a": claude(99, 10)},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: Claude's report does not say the target of the Weekly window"}, "a"},
	} {
		var asked []string
		got := tc.route.Choose(func(group string) store.Quota {
			asked = append(asked, group)
			return tc.quota[group]
		})
		if !reflect.DeepEqual(got, tc.want) || strings.Join(asked, " ") != tc.asked {
			t.Errorf("%s:\n got %+v (asked %v)\nwant %+v (asked %s)", name, got, asked, tc.want, tc.asked)
		}
	}
	c := route("a@Weekly<80", "b@5h<50").Choose(func(string) store.Quota { return claude(90, 90) })
	if at := c.Resets(); at == nil || !at.Equal(early) {
		t.Errorf("the earliest reset = %v, want %v", at, early)
	}
	if want := "a passed over (Claude's Weekly window is 90% used, the route asks under 80); " +
		"b passed over (Claude's 5h window is 90% used, the route asks under 50)"; c.Why() != want {
		t.Errorf("why = %q", c.Why())
	}
}

// A stage whose route names several groups says so in the sentences about
// who works it; one group reads as it always did.
func TestStageWorkNamesTheFurtherGroups(t *testing.T) {
	one := store.StageWork{Stage: "new", Who: store.WhoAgents, Group: "a"}
	if got, want := one.Arrives("north"), "which agents of the group a work: WORK STARTS ON EACH UNSEEN, on the other yard's text"; got != want {
		t.Errorf("one group: %q", got)
	}
	two := one
	two.Else = []string{"b"}
	if got, want := two.Arrives("north"), "which agents of the group a work, or of the group b while the quota of a is nearly used: "+
		"WORK STARTS ON EACH UNSEEN, on the other yard's text"; got != want {
		t.Errorf("two groups: %q", got)
	}
	two.Else = []string{"b", "c"}
	if got, want := two.Stands("north"), "it arrived in this stage, which agents of the group a work, or of the groups b, then c, "+
		"each while the quota of those before it is nearly used: work starts on it unseen, on the other yard's text"; got != want {
		t.Errorf("three groups: %q", got)
	}
}

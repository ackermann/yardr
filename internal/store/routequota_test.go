package store_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/shared"
	"yardr.dev/yardr/internal/store"
)

// quotaGroups are two groups that start agents, a and b, one of people,
// and one that runs a command.
func quotaGroups(t *testing.T) (store.API, func(first string, further ...string) (*store.Route, error)) {
	t.Helper()
	s, ctx := flowDepot(t)
	for _, g := range []store.Group{
		{Name: "a", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "b", Runner: "herdr", Config: map[string]any{"kind": "codex"}},
		{Name: "people", Runner: "manual", Members: []string{"jo"}},
		{Name: "refinery", Runner: "exec", Config: map[string]any{"cmd": "true"}},
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
	t.Parallel()
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
	t.Parallel()
	s, add := quotaGroups(t)
	for _, tc := range []struct {
		first   string
		further []string
		want    string
	}{
		{"a", []string{"people"}, "group people is manual: --else and a quota condition are for groups that start agents"},
		{"people", []string{"a"}, "group people is manual: --else and a quota condition are for groups that start agents"},
		{"people@Weekly<80", nil, "group people is manual: --else and a quota condition are for groups that start agents"},
		{"people@machine.load<80", nil, "group people is manual: --else and a quota condition are for groups that start agents"},
		{"refinery@Weekly<80", nil, "group refinery starts no agents: its condition names the window Weekly of a provider"},
		{"refinery@machine.load<80,Weekly<80", nil, "group refinery starts no agents: its condition names the window Weekly of a provider"},
		{"refinery@<80", nil, "group refinery starts no agents: its condition names every window of a provider"},
		{"a@machine.load<80", []string{"refinery"}, "group refinery is exec: a route with several groups names only groups that start agents"},
		{"refinery", []string{"a"}, "group refinery is exec: a route with several groups names only groups that start agents"},
		{"wait", nil, "wait ends a route (--else wait): it is not a group to send to"},
		{"wait@Weekly<80", nil, "wait ends a route (--else wait): it is not a group to send to"},
		{"a@Weekly<80", []string{"wait@Weekly<80"}, "wait takes no condition"},
		{"a@Weekly<80", []string{"wait", "b"}, "nothing is tried after wait"},
		{"a", []string{"ghost"}, "group ghost: not found"},
		{"a", []string{"b", "a"}, "group a is named twice"},
		{"a@Weekly", nil, `condition "Weekly": "Weekly" is not <window><<percent used>`},
		{"a@Weekly<lots", nil, `"Weekly<lots" is not <window><<percent used>`},
		{"a@Weekly<80,5h<0", nil, `"5h<0" is not <window><<percent used>`},
		{"a", []string{"b@<101"}, `group b: condition "<101"`},
		{"a@.load<80", nil, `".load<80" is not <window><<percent used>`},
		{"a@Weekly<target+lots", nil, `"Weekly<target+lots" is not <window><<percent used>, as in Weekly<80`},
		{"a@Weekly<target+lots", nil, `nor <window><target> with an optional margin, as in Weekly<target+10`},
		{"a@Weekly<target+", nil, `nor <window><target> with an optional margin`},
		{"a@Weekly<target++1", nil, `nor <window><target> with an optional margin`},
		{"a@Weekly<target-+1", nil, `nor <window><target> with an optional margin`},
		{"a@Weekly<targets", nil, `nor <window><target> with an optional margin`},
		{"a@Weekly<Target", nil, `nor <window><target> with an optional margin`},
		{"a@Weekly<lots", nil, `nor <window><target> with an optional margin`},
		{"a@Weekly<80/0d", nil, `"Weekly<80/0d" is not <window><<percent used>`},
		{"a@Weekly<80/xyz", nil, `"Weekly<80/xyz" is not <window><<percent used>`},
		{"a@Weekly<80/xyz", nil, `either may end in /<duration> above 0, as in Weekly<80/1d`},
		{"a", []string{"b@<90/-5h"}, `group b: condition "<90/-5h"`},
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

// A comparison may end in a duration after a slash, on the number, the
// target, a source's window and every window alike; the days and the Go
// durations are those of the CLI's parseDays, and anything else, or a
// duration too long for a Duration to count, is refused naming the
// comparison.
func TestParseQuotaReadsTheDurationAComparisonMayEndIn(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	for _, tc := range []struct {
		when string
		want []store.QuotaLimit
	}{
		{"Weekly<80", []store.QuotaLimit{{Label: "Weekly", Under: 80}}},
		{"Weekly<80/1d", []store.QuotaLimit{{Label: "Weekly", Under: 80, Within: day}}},
		{"Weekly<80/7d", []store.QuotaLimit{{Label: "Weekly", Under: 80, Within: 7 * day}}},
		{"Weekly<80/20h", []store.QuotaLimit{{Label: "Weekly", Under: 80, Within: 20 * time.Hour}}},
		{"Weekly<80/30m", []store.QuotaLimit{{Label: "Weekly", Under: 80, Within: 30 * time.Minute}}},
		{"Weekly<80/1h30m", []store.QuotaLimit{{Label: "Weekly", Under: 80, Within: 90 * time.Minute}}},
		{"Weekly<100/1d", []store.QuotaLimit{{Label: "Weekly", Under: 100, Within: day}}},
		{"Weekly<target/1d", []store.QuotaLimit{{Label: "Weekly", Target: true, Within: day}}},
		{"Weekly<target+10/1d", []store.QuotaLimit{{Label: "Weekly", Target: true, Margin: 10, Within: day}}},
		{"Weekly<target-5/20h", []store.QuotaLimit{{Label: "Weekly", Target: true, Margin: -5, Within: 20 * time.Hour}}},
		{"machine.load<80/1h", []store.QuotaLimit{{Source: "machine", Label: "load", Under: 80, Within: time.Hour}}},
		{"machine.<80/1h", []store.QuotaLimit{{Source: "machine", Under: 80, Within: time.Hour}}},
		{"<80/1d", []store.QuotaLimit{{Under: 80, Within: day}}},
		// A label may hold a slash: it is left of the "<".
		{"Weekly/Opus<80/1d", []store.QuotaLimit{{Label: "Weekly/Opus", Under: 80, Within: day}}},
		{" Weekly < 80 / 1d ", []store.QuotaLimit{{Label: "Weekly", Under: 80, Within: day}}},
		{"Weekly<80/1d,5h<90,Monthly<target+5/30m", []store.QuotaLimit{
			{Label: "Weekly", Under: 80, Within: day},
			{Label: "5h", Under: 90},
			{Label: "Monthly", Target: true, Margin: 5, Within: 30 * time.Minute}}},
	} {
		got, err := store.ParseQuota(tc.when)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseQuota(%q) = %+v, %v; want %+v", tc.when, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ when, part string }{
		{"Weekly<80/0d", "Weekly<80/0d"},
		{"Weekly<80/0", "Weekly<80/0"},
		{"Weekly<80/0h", "Weekly<80/0h"},
		{"Weekly<80/-1d", "Weekly<80/-1d"},
		{"Weekly<80/-20h", "Weekly<80/-20h"},
		{"Weekly<80/xyz", "Weekly<80/xyz"},
		{"Weekly<80/d", "Weekly<80/d"},
		{"Weekly<80/", "Weekly<80/"},
		{"Weekly<80/ ", "Weekly<80/ "},
		{"Weekly<80/1.5d", "Weekly<80/1.5d"},
		{"Weekly<80/1w", "Weekly<80/1w"},
		{"Weekly<80/1d12h", "Weekly<80/1d12h"},
		{"Weekly<80/20", "Weekly<80/20"},
		{"Weekly<80/1d/2d", "Weekly<80/1d/2d"},
		{"Weekly</1d", "Weekly</1d"},
		{"Weekly<0/1d", "Weekly<0/1d"},
		{"Weekly<101/1d", "Weekly<101/1d"},
		{"Weekly<lots/1d", "Weekly<lots/1d"},
		{"Weekly<target+/1d", "Weekly<target+/1d"},
		{"Weekly<target+10/0d", "Weekly<target+10/0d"},
		{"machine.load<80/0d", "machine.load<80/0d"},
		{"Weekly<80/1d,5h<90/0m", "5h<90/0m"},
		// 106751 days is the most a Duration counts; a number beyond it must
		// not wrap into a duration of some other sign.
		{"Weekly<80/106752d", "Weekly<80/106752d"},
		{"Weekly<80/999999999d", "Weekly<80/999999999d"},
	} {
		_, err := store.ParseQuota(tc.when)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("condition %q: %q is not <window><<percent used>", tc.when, tc.part)) {
			t.Errorf("ParseQuota(%q): %v, want a refusal naming %q", tc.when, err, tc.part)
		}
	}
	if got, err := store.ParseQuota("Weekly<80/106751d"); err != nil || len(got) != 1 || got[0].Within != 106751*day {
		t.Errorf("ParseQuota of the longest duration = %+v, %v", got, err)
	}
}

// One exec group may be gated alone on the windows of sources, and a route
// may end in wait; the marker lists with the route but is none of its
// groups, and no group takes the name.
func TestRouteGatesOneExecGroupAndEndsInWait(t *testing.T) {
	t.Parallel()
	s, add := quotaGroups(t)
	ctx := t.Context()
	r, err := add("refinery@machine.load<80", "wait")
	if err != nil {
		t.Fatal(err)
	}
	if r.GroupList() != "refinery@machine.load<80 else wait" || r.Worker() != "refinery" || r.Further() != nil || !r.Quotaed() {
		t.Fatalf("the route lists as %q, worker %q, further %q", r.GroupList(), r.Worker(), r.Further())
	}
	if _, err := add("a@machine.load<80", "wait"); err != nil {
		t.Fatalf("a group that starts agents gated on a source: %v", err)
	}
	// In a yard of its own: beside the gated route above it would take
	// the same beads at the same priority, which the tie refusal now
	// refuses, and gating alone is what is on trial here.
	_, addAlone := quotaGroups(t)
	if _, err := addAlone("refinery", "wait"); err != nil {
		t.Fatalf("one exec group with wait and no condition: %v", err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "wait", Runner: "herdr"}, "jo"); err == nil ||
		!strings.Contains(err.Error(), "wait ends a route (--else wait), so no group takes the name") {
		t.Fatalf("a group named wait: %v", err)
	}
}

// A group a route gates alone on a source may become exec; one a route with
// several groups names, or one conditioned on a provider's windows, may not.
func TestGroupGatedAloneOnASourceMayBecomeExec(t *testing.T) {
	t.Parallel()
	toExec := func(name string) store.Group {
		return store.Group{Name: name, Runner: "exec", Config: map[string]any{"cmd": "true"}}
	}
	t.Run("gated alone on a source", func(t *testing.T) {
		s, add := quotaGroups(t)
		if _, err := add("a@machine.load<80", "wait"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(t.Context(), toExec("a"), "jo"); err != nil {
			t.Fatalf("to exec: %v", err)
		}
	})
	t.Run("with several groups", func(t *testing.T) {
		s, add := quotaGroups(t)
		if _, err := add("a@machine.load<80", "b"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(t.Context(), toExec("a"), "jo"); err == nil || !strings.Contains(err.Error(), "it cannot become exec") {
			t.Fatalf("to exec: %v", err)
		}
	})
	t.Run("conditioned on a provider", func(t *testing.T) {
		s, add := quotaGroups(t)
		if _, err := add("a@Weekly<80"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(t.Context(), toExec("a"), "jo"); err == nil || !strings.Contains(err.Error(), "it cannot become exec") {
			t.Fatalf("to exec: %v", err)
		}
	})
}

// A group a route names with --else or a condition cannot be saved with
// another runner; with the same runner it can, and a group only a plain
// route names changes as it always could. A group that goes takes every
// route that names it.
func TestGroupOfAQuotaRouteKeepsItsRunner(t *testing.T) {
	t.Parallel()
	s, add := quotaGroups(t)
	ctx := t.Context()
	if err := s.PutGroup(ctx, store.Group{Name: "c", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := add("c"); err != nil {
		t.Fatal(err)
	}
	// For tasks alone beside the plain route to c: two routes that take
	// the same beads are refused as a tie, and the runner rule is what is
	// on trial here.
	task := "task"
	r, err := s.AddRoute(ctx, store.Route{Stage: "new", Type: &task, Group: "a", When: "Weekly<80",
		Else: []store.RouteGroup{{Name: "b"}}}, "jo")
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
// is handed, asks only as far as it has to, and says of each group it
// passed over why. A group with no condition holds while none of the windows
// its provider's models share is used up: a window the source says counts
// for one model alone is not read for it.
func TestRouteChoose(t *testing.T) {
	t.Parallel()
	pct := func(p float64) *float64 { return &p }
	early, late := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	// claude is what a source that says Claude's windows reports: Weekly Fable
	// is used up, with no reset, and counts for Fable alone.
	claude := func(week, five float64) store.Quota {
		return store.Quota{Provider: "Claude", Windows: []store.QuotaWindow{
			{Label: "5h", UsedPercent: pct(five), ResetsAt: &early},
			{Label: "Weekly", UsedPercent: pct(week), ResetsAt: &late},
			{Label: "Weekly Fable", Model: "Fable", UsedPercent: pct(100)},
		}}
	}
	// twoWindows is claude without its per-model window, for the rows that are
	// about the windows every model draws from alone.
	twoWindows := func(week, five float64) store.Quota {
		q := claude(week, five)
		q.Windows = q.Windows[:2]
		return q
	}
	// paced is claude with the share of each window that has passed, as a
	// source that knows the pace reports it.
	paced := func(week, five, weekTarget, fiveTarget float64) store.Quota {
		q := claude(week, five)
		q.Windows[0].TargetPercent = pct(fiveTarget)
		q.Windows[1].TargetPercent = pct(weekTarget)
		return q
	}
	// now is the moment the rows choose at, between the two resets above: the
	// 5h window's has passed, the weekly one's is twelve hours away. A route
	// whose comparisons end in a duration measures its resets from it.
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	in := func(d time.Duration) *time.Time { at := now.Add(d); return &at }
	// resetting is claude with the Weekly window at week percent and resetting
	// the given time from now (negative: already past), the 5h window at five.
	resetting := func(week, five float64, d time.Duration) store.Quota {
		q := twoWindows(week, five)
		q.Windows[1].ResetsAt = in(d)
		return q
	}
	// noReset is claude with a Weekly window the provider says no reset of.
	noReset := func(week float64) store.Quota {
		q := twoWindows(week, 10)
		q.Windows[1].ResetsAt = nil
		return q
	}
	// machine is what the machine source says of itself: load at load
	// percent, swap and disk nearly idle, each with its target.
	machine := func(load float64) store.Quota {
		return store.Quota{Provider: "machine", Windows: []store.QuotaWindow{
			{Label: "load", UsedPercent: pct(load), TargetPercent: pct(80)},
			{Label: "swap", UsedPercent: pct(10), TargetPercent: pct(85)},
			{Label: "disk", UsedPercent: pct(40), TargetPercent: pct(90)},
		}}
	}
	// lastKnown is what a route is handed when no fresh answer is known
	// (Quota.LastKnown): why, and the windows the provider last said were
	// used up. usedUp is the Weekly window so, resetting at late, and
	// reason says it as a comparison that fails on it does.
	lastKnown := func(windows ...store.QuotaWindow) store.Quota {
		return store.Quota{Provider: "Claude", Unknown: "no fresh answer yet", LastKnown: windows}
	}
	usedUp := store.QuotaWindow{Label: "Weekly", UsedPercent: pct(100), ResetsAt: &late}
	reason := func(under string) string {
		return "Claude's Weekly window is 100% used, the route asks under " + under +
			" (last known: used up until " + shared.Clock(late, now) + ")"
	}
	route := func(first string, further ...string) store.Route {
		g := store.ParseRouteGroup(first)
		r := store.Route{Group: g.Name, When: g.When}
		for _, e := range further {
			r.Else = append(r.Else, store.ParseRouteGroup(e))
		}
		return r
	}
	// quota holds the provider's windows by group, and a source's own under
	// the source's name. A group it lists nothing for, a bare fallback after
	// the group a row is about, answers with every window clear and is not
	// recorded in asked.
	allClear := twoWindows(0, 0)
	for name, tc := range map[string]struct {
		route store.Route
		quota map[string]store.Quota
		want  store.RouteChoice
		asked string
	}{
		// A group with no condition is judged by "<100": every window of its
		// provider under 100% used.
		"no condition, no window used up": {route("a"), map[string]store.Quota{"a": twoWindows(40, 10)},
			store.RouteChoice{Group: "a"}, "a"},
		"no condition, a window used up": {route("a"), map[string]store.Quota{"a": twoWindows(100, 10)},
			store.RouteChoice{Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 100% used, the route asks under 100", ResetsAt: &late}}}, "a"},
		"no condition, a window just under": {route("a"), map[string]store.Quota{"a": twoWindows(99.9, 10)},
			store.RouteChoice{Group: "a"}, "a"},
		// A window the source says counts for one model alone holds no group
		// with no condition: Weekly Fable is used up, the windows every model
		// draws from are not.
		"no condition, a per-model window used up": {route("a"), map[string]store.Quota{"a": claude(19, 10)},
			store.RouteChoice{Group: "a"}, "a"},
		"no condition, a shared window used up beside a per-model one": {route("a"), map[string]store.Quota{"a": claude(100, 10)},
			store.RouteChoice{Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 100% used, the route asks under 100", ResetsAt: &late}}}, "a"},
		// A provider that reports only per-model windows holds the group for
		// nothing: the yard does not match a group to a window's model.
		"no condition, only per-model windows": {route("a"),
			map[string]store.Quota{"a": {Provider: "Claude", Windows: claude(0, 0).Windows[2:]}},
			store.RouteChoice{Group: "a"}, "a"},
		"no condition, limits not known": {route("a"), map[string]store.Quota{"a": {Unknown: "no source of this yard answers for provider muse"}},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: no source of this yard answers for provider muse"}, "a"},
		// A provider that reports no window knows of no limit: none is used up,
		// and the claim says nothing, as it does when a condition names windows.
		"no condition, a report with no windows": {route("a"), map[string]store.Quota{"a": {Provider: "Muse"}},
			store.RouteChoice{Group: "a"}, "a"},
		"a condition on every window, a report with no windows": {route("a@<80"), map[string]store.Quota{"a": {Provider: "Muse"}},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: Muse's report has no windows"}, "a"},
		"no conditions, the first used up": {route("a", "b"), map[string]store.Quota{"a": twoWindows(10, 100), "b": twoWindows(10, 10)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's 5h window is 100% used, the route asks under 100", ResetsAt: &early}}}, "a b"},
		"no conditions, none to take it": {route("a", "b"), map[string]store.Quota{"a": twoWindows(100, 10), "b": twoWindows(10, 100)},
			store.RouteChoice{Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 100% used, the route asks under 100", ResetsAt: &late},
				{Group: "b", Reason: "Claude's 5h window is 100% used, the route asks under 100", ResetsAt: &early}}}, "a b"},
		"no conditions, the first holds": {route("a", "b"), map[string]store.Quota{"a": twoWindows(10, 10)},
			store.RouteChoice{Group: "a"}, "a"},
		// A condition names the windows that gate: the 5h window is used up and
		// this one does not name it.
		"a condition replaces it": {route("a@Weekly<100"), map[string]store.Quota{"a": twoWindows(10, 100)},
			store.RouteChoice{Group: "a"}, "a"},
		// A condition that names a per-model window asked for it, as it did
		// before a source could say the model.
		"a condition on a per-model window": {route("a@Weekly Fable<100", "b"), map[string]store.Quota{"a": claude(10, 10)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly Fable window is 100% used, the route asks under 100"}}}, "a"},
		"a further group with no condition is judged too": {route("a@Weekly<80", "b"),
			map[string]store.Quota{"a": twoWindows(85, 10), "b": twoWindows(10, 100)},
			store.RouteChoice{Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 85% used, the route asks under 80", ResetsAt: &late},
				{Group: "b", Reason: "Claude's 5h window is 100% used, the route asks under 100", ResetsAt: &early}}}, "a b"},
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
		// Only the comparison no route wrote skips a per-model window: <90 on
		// every window asked for all of them.
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
		"a source's window under its threshold": {route("a@machine.load<80", "b"), map[string]store.Quota{"machine": machine(70)},
			store.RouteChoice{Group: "a"}, "a@machine"},
		"a source's window past it": {route("a@machine.load<80", "b"), map[string]store.Quota{"machine": machine(93)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "machine's load window is 93% used, the route asks under 80"}}}, "a@machine"},
		"every window of a source": {route("a@machine.<80", "b"), map[string]store.Quota{"machine": machine(93)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "machine's load window is 93% used, the route asks under 80"}}}, "a@machine"},
		"a source and a provider in one condition": {route("a@machine.load<80,Weekly<80", "b"),
			map[string]store.Quota{"machine": machine(70), "a": claude(90, 10)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 90% used, the route asks under 80", ResetsAt: &late}}}, "a@machine a"},
		"a source nothing is known of holds": {route("a@machine.load<80", "b"),
			map[string]store.Quota{"machine": {Unknown: "no source named machine says limits of itself"}},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: no source named machine says limits of itself"}, "a@machine"},
		"wait ends the route": {route("a@Weekly<80", "wait"), map[string]store.Quota{"a": claude(90, 10)},
			store.RouteChoice{Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 90% used, the route asks under 80", ResetsAt: &late}}}, "a"},
		"wait after a group with no condition": {route("a", "wait"), map[string]store.Quota{"a": twoWindows(10, 10)},
			store.RouteChoice{Group: "a"}, "a"},
		"wait after a group with no condition, used up": {route("a", "wait"), map[string]store.Quota{"a": twoWindows(10, 100)},
			store.RouteChoice{Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's 5h window is 100% used, the route asks under 100", ResetsAt: &early}}}, "a"},
		// A comparison that ends in a duration holds a window past its number
		// that resets within it, and the choice says so.
		"past the number, resetting within the duration": {route("a@Weekly<80/1d", "b"), map[string]store.Quota{"a": resetting(90, 10, 20*time.Hour)},
			store.RouteChoice{Group: "a", Held: []string{"Weekly 90% but resets in 20h"}}, "a"},
		"past the number, resetting after the duration": {route("a@Weekly<80/1d", "b"), map[string]store.Quota{"a": resetting(90, 10, 72*time.Hour)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 90% used, the route asks under 80", ResetsAt: in(72 * time.Hour)}}}, "a"},
		"resetting at the duration": {route("a@Weekly<80/1d", "b"), map[string]store.Quota{"a": resetting(90, 10, 24*time.Hour)},
			store.RouteChoice{Group: "a", Held: []string{"Weekly 90% but resets in 1d"}}, "a"},
		"resetting a second after the duration": {route("a@Weekly<80/1d", "b"), map[string]store.Quota{"a": resetting(90, 10, 24*time.Hour+time.Second)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 90% used, the route asks under 80", ResetsAt: in(24*time.Hour + time.Second)}}}, "a"},
		// The provider has not seen the window open yet: its reset has passed,
		// and that is soon enough.
		"a reset already past": {route("a@Weekly<80/1d", "b"), map[string]store.Quota{"a": resetting(100, 10, -2*time.Hour)},
			store.RouteChoice{Group: "a", Held: []string{"Weekly 100% but was to reset 2h ago"}}, "a"},
		// With no reset said there is no time left to know: the number decides.
		"no reset said": {route("a@Weekly<80/1d", "b"), map[string]store.Quota{"a": noReset(90)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 90% used, the route asks under 80"}}}, "a"},
		"under the number, the reset far away": {route("a@Weekly<80/1d", "b"), map[string]store.Quota{"a": resetting(70, 10, 72*time.Hour)},
			store.RouteChoice{Group: "a"}, "a"},
		"minutes within": {route("a@Weekly<80/30m", "b"), map[string]store.Quota{"a": resetting(90, 10, 20*time.Minute)},
			store.RouteChoice{Group: "a", Held: []string{"Weekly 90% but resets in 20m"}}, "a"},
		"minutes beyond": {route("a@Weekly<80/30m", "b"), map[string]store.Quota{"a": resetting(90, 10, 40*time.Minute)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 90% used, the route asks under 80", ResetsAt: in(40 * time.Minute)}}}, "a"},
		"hours within": {route("a@Weekly<80/20h", "b"), map[string]store.Quota{"a": resetting(90, 10, 20*time.Hour)},
			store.RouteChoice{Group: "a", Held: []string{"Weekly 90% but resets in 20h"}}, "a"},
		"the target and a duration": {route("a@Weekly<target+10/1d", "b"), map[string]store.Quota{"a": paced(38, 10, 17, 50)},
			store.RouteChoice{Group: "a", Held: []string{"Weekly 38% but resets in 12h"}}, "a"},
		"the target and a shorter duration": {route("a@Weekly<target+10/6h", "b"), map[string]store.Quota{"a": paced(38, 10, 17, 50)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 38% used, the route asks under target 17% + 10", ResetsAt: &late}}}, "a"},
		// A source's windows may say no reset at all.
		"a source's window and a duration": {route("a@machine.load<80/1h", "b"), map[string]store.Quota{"machine": machine(93)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "machine's load window is 93% used, the route asks under 80"}}}, "a@machine"},
		// Every window the comparison reads is held on its own reset.
		"every window and a duration": {route("a@<80/1d", "b"), map[string]store.Quota{"a": twoWindows(90, 100)},
			store.RouteChoice{Group: "a", Held: []string{
				"5h 100% but was to reset 4d 12h ago",
				"Weekly 90% but resets in 12h"}}, "a"},
		"a duration on one comparison of two": {route("a@Weekly<80/1d,5h<90", "b"), map[string]store.Quota{"a": resetting(90, 10, 20*time.Hour)},
			store.RouteChoice{Group: "a", Held: []string{"Weekly 90% but resets in 20h"}}, "a"},
		// The 5h comparison has no duration, so its reset soon holds nothing:
		// the group is passed over for it alone, and what was held is not said.
		"held beside a comparison that failed": {route("a@Weekly<80/1d,5h<50", "b"), map[string]store.Quota{"a": resetting(90, 60, 20*time.Hour)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's 5h window is 60% used, the route asks under 50", ResetsAt: &early}}}, "a"},
		"held beside a comparison that could not be made": {route("a@Weekly<80/1d,Monthly<80", "b"), map[string]store.Quota{"a": resetting(90, 10, 20*time.Hour)},
			store.RouteChoice{Group: "a", Held: []string{"Weekly 90% but resets in 20h"},
				Unknown: "the limits of a were not known: Claude's report has no window labelled Monthly"}, "a"},
		// A group with no condition is judged by "<100" alone: its window used
		// up resets soon, and that holds nothing.
		"no condition, used up and about to reset": {route("a"), map[string]store.Quota{"a": resetting(100, 10, time.Hour)},
			store.RouteChoice{Passed: []store.PassedGroup{
				{Group: "a", Reason: "Claude's Weekly window is 100% used, the route asks under 100", ResetsAt: in(time.Hour)}}}, "a"},
		// With no fresh answer, a window last known used up until a reset
		// ahead fails the comparisons it answers, and says it is the last
		// known. What it does not answer is not known, as without it.
		"no condition, last known used up": {route("a"), map[string]store.Quota{"a": lastKnown(usedUp)},
			store.RouteChoice{Passed: []store.PassedGroup{{Group: "a", Reason: reason("100"), ResetsAt: &late}}}, "a"},
		"a condition on the window, last known used up": {route("a@Weekly<80", "b"), map[string]store.Quota{"a": lastKnown(usedUp)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{{Group: "a", Reason: reason("80"), ResetsAt: &late}}}, "a"},
		"a condition on another window, last known used up": {route("a@5h<50", "b"), map[string]store.Quota{"a": lastKnown(usedUp)},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: no fresh answer yet"}, "a"},
		"two conditions, one answered by what was last known": {route("a@Weekly<80,5h<50", "b"), map[string]store.Quota{"a": lastKnown(usedUp)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{{Group: "a", Reason: reason("80"), ResetsAt: &late}}}, "a"},
		// A duration is read as for a fresh window: the group takes the bead
		// within it, the note saying the window was last known, and is passed
		// over beyond it.
		"a duration holds what was last known within it": {route("a@Weekly<80/1d", "b"), map[string]store.Quota{"a": lastKnown(usedUp)},
			store.RouteChoice{Group: "a", Held: []string{"Weekly 100% but resets in 12h (last known)"}}, "a"},
		"a duration passes over what was last known beyond it": {route("a@Weekly<80/6h", "b"), map[string]store.Quota{"a": lastKnown(usedUp)},
			store.RouteChoice{Group: "b", Passed: []store.PassedGroup{{Group: "a", Reason: reason("80"), ResetsAt: &late}}}, "a"},
		// A window of one model alone holds no group with no condition, last
		// known or not, and the claim says the limits were not known.
		"no condition, a per-model window last known used up": {route("a"),
			map[string]store.Quota{"a": lastKnown(store.QuotaWindow{Label: "Weekly Fable", Model: "Fable", UsedPercent: pct(100), ResetsAt: &late})},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: no fresh answer yet"}, "a"},
		"no condition, nothing last known": {route("a"), map[string]store.Quota{"a": lastKnown()},
			store.RouteChoice{Group: "a", Unknown: "the limits of a were not known: no fresh answer yet"}, "a"},
	} {
		var asked []string
		got := tc.route.ChooseAt(now, func(group, source string) store.Quota {
			name := group
			if source != "" {
				name = source
			}
			q, listed := tc.quota[name]
			if !listed {
				return allClear
			}
			if source == "" {
				asked = append(asked, group)
			} else {
				asked = append(asked, group+"@"+source)
			}
			return q
		})
		if !reflect.DeepEqual(got, tc.want) || strings.Join(asked, " ") != tc.asked {
			t.Errorf("%s:\n got %+v (asked %v)\nwant %+v (asked %s)", name, got, asked, tc.want, tc.asked)
		}
	}
	c := route("a@Weekly<80", "b@5h<50").Choose(func(_, _ string) store.Quota { return claude(90, 90) })
	if at := c.Resets(); at == nil || !at.Equal(early) {
		t.Errorf("the earliest reset = %v, want %v", at, early)
	}
	if want := "a passed over (Claude's Weekly window is 90% used, the route asks under 80); " +
		"b passed over (Claude's 5h window is 90% used, the route asks under 50)"; c.Why() != want {
		t.Errorf("why = %q", c.Why())
	}
	// The wait of a route with no condition says what the window's reset is,
	// as a condition's does.
	c = route("a").Choose(func(_, _ string) store.Quota { return twoWindows(100, 100) })
	if at := c.Resets(); c.Group != "" || at == nil || !at.Equal(early) ||
		c.Why() != "a passed over (Claude's 5h window is 100% used, the route asks under 100; Claude's Weekly window is 100% used, the route asks under 100)" {
		t.Errorf("a plain route with every window used up chose %+v, resets %v, why %q", c, at, c.Why())
	}
	// A route whose groups start no agents is handed no quota: nothing is
	// asked, and the first group is taken.
	if got := route("a", "b").Choose(nil); !reflect.DeepEqual(got, store.RouteChoice{Group: "a"}) {
		t.Errorf("a nil quota chose %+v, want a", got)
	}
	// Choose measures a reset from the wall clock, as ChooseAt does from the
	// one it is handed.
	soon, far := time.Now().Add(20*time.Hour), time.Now().Add(72*time.Hour)
	for _, tc := range []struct {
		resets time.Time
		group  string
	}{{soon, "a"}, {far, "b"}} {
		c := route("a@Weekly<80/1d", "b").Choose(func(_, _ string) store.Quota {
			return store.Quota{Provider: "Claude", Windows: []store.QuotaWindow{{Label: "Weekly", UsedPercent: pct(90), ResetsAt: &tc.resets}}}
		})
		if c.Group != tc.group || (tc.group == "a") != (len(c.Held) == 1) {
			t.Errorf("Choose with a window at 90%% resetting at %s chose %+v, want %s", tc.resets.Format(time.RFC3339), c, tc.group)
		}
	}
}

// A route is gated when choosing among its groups reads windows: a group
// carries a condition, or starts agents and so is judged by "<100" without
// one. A route of groups that start none reads nothing, whatever it names
// after them; Quotaed is not that question and keeps its answers.
func TestRouteGatedWhenChoosingReadsWindows(t *testing.T) {
	t.Parallel()
	groups := map[string]store.Group{
		"a":        {Name: "a", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		"nokind":   {Name: "nokind", Runner: "herdr"},
		"people":   {Name: "people", Runner: "manual"},
		"refinery": {Name: "refinery", Runner: "exec", Config: map[string]any{"cmd": "true"}},
		"yard":     {Name: "yard", Runner: "peer"},
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
		route   store.Route
		def     string
		gated   bool
		quotaed bool
	}{
		"a group that starts agents":         {route("a"), "", true, false},
		"a herdr group with no kind":         {route("nokind"), "", true, false},
		"a herdr group with no kind, kimi":   {route("nokind"), "kimi", true, false},
		"a manual group":                     {route("people"), "", false, false},
		"an exec group":                      {route("refinery"), "", false, false},
		"a peer group":                       {route("yard"), "", false, false},
		"a group the yard does not have":     {route("ghost"), "", false, false},
		"a condition on an exec group":       {route("refinery@machine.load<80", "wait"), "", true, true},
		"an exec group and wait":             {route("refinery", "wait"), "", false, true},
		"agents with a condition":            {route("a@Weekly<80"), "", true, true},
		"agents with a further group":        {route("a", "nokind"), "", true, true},
		"a further group after a condition":  {route("a@Weekly<80", "wait"), "", true, true},
		"a condition on a group not in hand": {route("ghost@machine.load<80"), "", true, true},
	} {
		if got := tc.route.Gated(groups, tc.def); got != tc.gated {
			t.Errorf("%s: Gated = %v, want %v", name, got, tc.gated)
		}
		if got := tc.route.Quotaed(); got != tc.quotaed {
			t.Errorf("%s: Quotaed = %v, want %v", name, got, tc.quotaed)
		}
	}
}

// A stage whose route names several groups says so in the sentences about
// who works it; one group reads as it always did.
func TestStageWorkNamesTheFurtherGroups(t *testing.T) {
	t.Parallel()
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

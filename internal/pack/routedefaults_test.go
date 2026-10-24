package pack_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

func mustGroup(t *testing.T, st store.API, g store.Group) {
	t.Helper()
	if g.Config == nil && g.Runner == "herdr" {
		g.Config = map[string]any{"prompt": "roles/builder.md", "kind": "claude"}
	}
	if err := st.PutGroup(ctx, g, "me"); err != nil {
		t.Fatal(err)
	}
}

func mustDefaultRoute(t *testing.T, st store.API, r store.Route) *store.Route {
	t.Helper()
	saved, err := st.AddRoute(ctx, r, "me")
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func routeActions(p *pack.Plan) []string {
	var out []string
	for _, o := range p.Objects {
		if o.Kind == "route" {
			out = append(out, o.Action+" "+o.Name)
		}
	}
	return out
}

func hasRouteAction(p *pack.Plan, want string) bool {
	return slices.Contains(routeActions(p), want)
}

func notesHave(p *pack.Plan, parts ...string) bool {
	return slices.ContainsFunc(p.Notes, func(n string) bool {
		for _, part := range parts {
			if !strings.Contains(n, part) {
				return false
			}
		}
		return true
	})
}

// Importing the starter pack into a fresh depot on a yard whose defaults
// already work backlog and decide writes no route for those stages, in
// either flow, and says which default works each. The build, review and
// assembly stages, whose groups are the depot's own, still get routes.
func TestStarterLeavesBacklogAndDecideToYardDefaults(t *testing.T) {
	st, o := yard(t)
	mustGroup(t, st, store.Group{Name: "backlog", Runner: "manual"})
	mustGroup(t, st, store.Group{Name: "decisions", Runner: "manual"})
	backlog := mustDefaultRoute(t, st, store.Route{Stage: "backlog", Group: "backlog"})
	decide := mustDefaultRoute(t, st, store.Route{Stage: "decide", Group: "decisions"})

	plan := build(t, st, load(t, starter), o)
	for _, name := range []string{
		"depot web stage backlog -> backlog", "depot web stage decide -> decisions",
		"depot web stage backlog type train -> backlog", "depot web stage decide type train -> decisions",
	} {
		for _, action := range []string{pack.Create, pack.Change, pack.Unchanged} {
			if hasRouteAction(plan, action+" "+name) {
				t.Errorf("the plan would write route %s though a yard default works it; routes:\n%s", name, strings.Join(routeActions(plan), "\n"))
			}
		}
	}
	if !notesHave(plan, "stage backlog of flow default: worked by yard default "+backlog.ID+" to backlog; no route written") ||
		!notesHave(plan, "stage decide of flow default: worked by yard default "+decide.ID+" to decisions; no route written") {
		t.Errorf("notes do not say which default works backlog and decide:\n%s", strings.Join(plan.Notes, "\n"))
	}
	for _, want := range []string{
		"create depot web stage new -> web-builders",
		"create depot web stage review -> web-reviewers",
		"create depot web stage approved -> web-assembly",
	} {
		if !hasRouteAction(plan, want) {
			t.Errorf("the plan lacks %s; routes:\n%s", want, strings.Join(routeActions(plan), "\n"))
		}
	}

	apply(t, st, plan, answers)
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var own, defaults int
	for _, r := range routes {
		if r.Depot == nil {
			defaults++
			continue
		}
		own++
		if r.Stage == "backlog" || r.Stage == "decide" {
			t.Errorf("the import wrote an own %s route %s to %s over a yard default", r.Stage, r.ID, r.Group)
		}
	}
	if defaults != 2 || own != 9 {
		t.Errorf("routes after the import: %d defaults, %d own, want 2 and 9 (all %d)", defaults, own, len(routes))
	}
	depot := "web"
	if r := store.MatchRoute(routes, store.Bead{Stage: "backlog", Type: "task", Depot: &depot, Priority: store.DefaultPriority}, "me"); r == nil || r.ID != backlog.ID {
		t.Errorf("a backlog bead in web took %+v, want the yard default %s", r, backlog.ID)
	}

	// Re-applying resurrects no copy and removes no default.
	plan = build(t, st, load(t, starter), o)
	for _, a := range routeActions(plan) {
		if strings.HasPrefix(a, pack.Create+" ") || strings.HasPrefix(a, pack.Change+" ") {
			if strings.Contains(a, "stage backlog") || strings.Contains(a, "stage decide") {
				t.Errorf("a re-apply would write %s", a)
			}
		}
	}
	apply(t, st, plan, nil)
	routes, _ = st.Routes(ctx)
	if len(routes) != 11 {
		t.Errorf("routes after a re-apply: %d, want 11", len(routes))
	}
}

// A depot whose binding differs from the default's group gets its own
// route for the stage, which sets the default aside in that depot alone.
func TestPackWritesOwnRouteWhereItsBindingDiffersFromTheDefault(t *testing.T) {
	st, o := yard(t)
	mustGroup(t, st, store.Group{Name: "web-builders", Runner: "herdr"})
	mustGroup(t, st, store.Group{Name: "spare", Runner: "herdr"})
	mustGroup(t, st, store.Group{Name: "people", Runner: "manual"})
	def := mustDefaultRoute(t, st, store.Route{Stage: "new", Group: "spare"})

	plan := build(t, st, load(t, writePack(t, smallFiles(small))), o)
	if !hasRouteAction(plan, "create depot web stage new -> web-builders") {
		t.Fatalf("the plan does not write the depot's own route; routes:\n%s", strings.Join(routeActions(plan), "\n"))
	}
	if notesHave(plan, "stage new of flow default: worked by yard default") {
		t.Errorf("the plan leaves new to the default though its binding differs:\n%s", strings.Join(plan.Notes, "\n"))
	}
	apply(t, st, plan, nil)
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	web, other := "web", "other"
	if err := st.PutDepot(ctx, store.Depot{Name: other, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if r := store.MatchRoute(routes, store.Bead{Stage: "new", Type: "task", Depot: &web, Priority: store.DefaultPriority}, "me"); r == nil || r.Group != "web-builders" || r.Depot == nil {
		t.Errorf("a new bead in web took %+v, want its own route to web-builders", r)
	}
	if r := store.MatchRoute(routes, store.Bead{Stage: "new", Type: "task", Depot: &other, Priority: store.DefaultPriority}, "me"); r == nil || r.ID != def.ID {
		t.Errorf("a new bead in another depot took %+v, want the yard default %s to spare", r, def.ID)
	}
}

// A default restricted to the operator does not work a people stage for
// every originator. The pack writes its any-originator route and leaves the
// narrower default alone.
func TestPackDoesNotLeavePeopleStageToOperatorOnlyDefault(t *testing.T) {
	st, o := yard(t)
	mustGroup(t, st, store.Group{Name: "people", Runner: "manual"})
	operator := "me"
	def := mustDefaultRoute(t, st, store.Route{Stage: "decide", Group: "people", Originator: &operator})

	plan := build(t, st, load(t, writePack(t, smallFiles(small))), o)
	var found bool
	for _, obj := range plan.Objects {
		if obj.Kind == "route" && obj.Route != nil && obj.Route.Depot == "web" && obj.Route.Stage == "decide" && obj.Route.Group == "people" {
			found = true
			if obj.Action != pack.Create || obj.Route.Originator != store.OriginatorAny {
				t.Errorf("people-stage route = %+v, want a new any-originator route", obj)
			}
		}
	}
	if !found {
		t.Fatalf("the plan does not create a people-stage route; routes:\n%s", strings.Join(routeActions(plan), "\n"))
	}
	if !slices.Contains(actions(plan, pack.NotInPack), "route yard default stage decide originator me -> people") {
		t.Errorf("the narrower default %s is not listed as left alone: %v", def.ID, actions(plan, pack.NotInPack))
	}
	apply(t, st, plan, nil)
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	depot := "web"
	bead := store.Bead{Stage: "decide", Type: "task", Depot: &depot, Originator: "another-user", Priority: store.DefaultPriority}
	if r := store.MatchRoute(routes, bead, "me"); r == nil || r.Depot == nil || r.Group != "people" {
		t.Errorf("a non-operator bead in decide took %+v, want the pack's own people route", r)
	}
}

// A stage whose effective winner carries a quota chain is left to that
// chain, of either scope: the pack writes no route over a condition it
// cannot say, and the chain route is listed as not in the pack.
func TestPackLeavesAQuotaWinnerToItsChain(t *testing.T) {
	for _, own := range []bool{false, true} {
		name := "yard default"
		if own {
			name = "own route"
		}
		t.Run(name, func(t *testing.T) {
			st, o := yard(t)
			mustGroup(t, st, store.Group{Name: "web-builders", Runner: "herdr"})
			mustGroup(t, st, store.Group{Name: "spare", Runner: "herdr"})
			mustGroup(t, st, store.Group{Name: "people", Runner: "manual"})
			r := store.Route{Stage: "new", Group: "web-builders", When: "Weekly<80", Else: []store.RouteGroup{{Name: "spare"}}}
			if own {
				depot := "web"
				r.Depot = &depot
			}
			saved := mustDefaultRoute(t, st, r)

			plan := build(t, st, load(t, writePack(t, smallFiles(small))), o)
			for _, a := range routeActions(plan) {
				if (strings.HasPrefix(a, pack.Create+" ") || strings.HasPrefix(a, pack.Change+" ")) && strings.Contains(a, "stage new ") {
					t.Errorf("the plan would write %s over a quota chain", a)
				}
			}
			if !notesHave(plan, "stage new of flow default: worked by "+name+" "+saved.ID, "condition or further groups", "no route written") {
				t.Errorf("notes do not leave the stage to its chain:\n%s", strings.Join(plan.Notes, "\n"))
			}
			if !slices.Contains(actions(plan, pack.NotInPack), "route "+map[bool]string{true: "depot web stage new -> web-builders@Weekly<80 else spare", false: "yard default stage new -> web-builders@Weekly<80 else spare"}[own]) {
				t.Errorf("the quota route is not listed as not in the pack: %v", actions(plan, pack.NotInPack))
			}
			apply(t, st, plan, nil)
			routes, _ := st.Routes(ctx)
			var found bool
			for _, rt := range routes {
				if rt.ID == saved.ID {
					found = true
				}
				if rt.Stage == "new" && rt.ID != saved.ID && !rt.Quotaed() {
					t.Errorf("the import wrote a plain new route %s to %s over the chain", rt.ID, rt.Group)
				}
			}
			if !found {
				t.Errorf("the quota route %s went with the import", saved.ID)
			}
		})
	}
}

// Routes that ask something of the single bead, and routes for stages the
// pack lacks, are left alone of either scope, each listed with its scope.
func TestPackListsBeadShapedAndUnpackedRoutesWithTheirScope(t *testing.T) {
	st, o := yard(t)
	mustGroup(t, st, store.Group{Name: "web-builders", Runner: "herdr"})
	mustGroup(t, st, store.Group{Name: "hard-builders", Runner: "herdr"})
	mustGroup(t, st, store.Group{Name: "spare", Runner: "herdr"})
	mustGroup(t, st, store.Group{Name: "people", Runner: "manual"})
	hard := "difficulty:3"
	mustDefaultRoute(t, st, store.Route{Stage: "new", Label: &hard, Group: "hard-builders"})
	mustDefaultRoute(t, st, store.Route{Stage: "triage", Group: "people"})
	depot := "web"
	if _, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: &depot, Label: &hard, Group: "spare"}, "me"); err != nil {
		t.Fatal(err)
	}

	plan := build(t, st, load(t, writePack(t, smallFiles(small))), o)
	for _, want := range []string{
		"route yard default stage new label difficulty:3 -> hard-builders",
		"route yard default stage triage originator any -> people",
		"route depot web stage new label difficulty:3 -> spare",
	} {
		if !slices.Contains(actions(plan, pack.NotInPack), want) {
			t.Errorf("not in the pack = %v, want %q among them", actions(plan, pack.NotInPack), want)
		}
	}
	before, _ := st.Routes(ctx)
	apply(t, st, plan, nil)
	after, _ := st.Routes(ctx)
	if len(after) < len(before) {
		t.Errorf("routes went from %d to %d: a left-alone route was removed", len(before), len(after))
	}
	for _, r := range before {
		if !slices.ContainsFunc(after, func(a store.Route) bool { return a.ID == r.ID }) {
			t.Errorf("route %s (%s) went with the import", r.ID, r.GroupList())
		}
	}
}

// A stage the pack routes to nobody that a yard default works cannot be
// cleared by an import, which may never remove a default: the plan stops.
func TestPackRefusesToClearAStageAYardDefaultWorks(t *testing.T) {
	st, o := yard(t)
	mustGroup(t, st, store.Group{Name: "people", Runner: "manual"})
	def := mustDefaultRoute(t, st, store.Route{Stage: "triage", Group: "people"})
	dir := t.TempDir()
	text := "name: hand\nflows:\n  default:\n    stages:\n      triage: { manual: [done] }\n      done: { end: true }\ngroups: {}\n"
	if err := os.WriteFile(filepath.Join(dir, pack.File), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, dir), o)
	if len(plan.Refused) != 1 || !strings.Contains(plan.Refused[0], "yard default "+def.ID) || !strings.Contains(plan.Refused[0], "may not remove a yard default") {
		t.Fatalf("refused = %q, want the yard default %s named", plan.Refused, def.ID)
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: pack.Answers{}, Actor: "me"}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("Apply = %v, want the refused plan held", err)
	}
	routes, _ := st.Routes(ctx)
	if !slices.ContainsFunc(routes, func(r store.Route) bool { return r.ID == def.ID }) {
		t.Errorf("the yard default %s went with the refused import", def.ID)
	}
}

// An export of a depot whose stage is worked by a yard default alone
// carries that default's group for the stage.
func TestExportFollowsAYardDefault(t *testing.T) {
	st, home := handYard(t)
	route(t, st, store.Route{Stage: "a", Group: "wide"})
	ex, dir := export(t, st, "web", home, "")
	p := load(t, dir)
	var stage pack.Stage
	for _, s := range p.Flows[0].Stages {
		if s.Name == "a" {
			stage = s
		}
	}
	if stage.Group != "wide" {
		t.Errorf("stage a has group %q in the pack, want the yard default's wide", stage.Group)
	}
	if leftHas(ex, "route yard default stage a") || leftHas(ex, "stage a of flow hand") {
		t.Errorf("the followed default is listed as left out:\n%s", strings.Join(ex.Left, "\n"))
	}
}

// A typed stage the depot's own plain route already works for the pack's
// group gets no typed route: the plain route matches beads of every type,
// so the copy is redundant and a re-apply resurrects none the migration
// removed (yardr-3jst).
func TestPackLeavesTypedStageToOwnPlainRoute(t *testing.T) {
	st, o := yard(t)
	mustGroup(t, st, store.Group{Name: "decisions", Runner: "manual"})
	any := store.OriginatorAny
	depot := "web"
	plain := mustDefaultRoute(t, st, store.Route{Stage: "approved", Depot: &depot, Group: "decisions", Originator: &any})
	tiny := `name: tiny
flows:
  default:
    stages:
      approved: { group: decisions, outcomes: { done: merged } }
      merged: { end: true }
  train:
    type: train
    stages:
      approved: { group: decisions, outcomes: { done: merged } }
      merged: { end: true }
groups:
  decisions: { people: true }
`
	plan := build(t, st, load(t, writePack(t, map[string]string{"pack.yaml": tiny})), o)
	for _, a := range routeActions(plan) {
		if strings.Contains(a, "stage approved type train") && (strings.HasPrefix(a, pack.Create+" ") || strings.HasPrefix(a, pack.Change+" ") || strings.HasPrefix(a, pack.Unchanged+" ")) {
			t.Errorf("the plan would write %s though the depot's plain route covers it; routes:\n%s", a, strings.Join(routeActions(plan), "\n"))
		}
	}
	if !notesHave(plan, "stage approved of flow train", "worked by own plain route "+plain.ID, "to decisions", "no route written") {
		t.Errorf("notes do not leave the typed stage to the plain route:\n%s", strings.Join(plan.Notes, "\n"))
	}
	if !hasRouteAction(plan, "unchanged depot web stage approved -> decisions") {
		t.Errorf("the plain route is not kept as unchanged; routes:\n%s", strings.Join(routeActions(plan), "\n"))
	}
	apply(t, st, plan, nil)
	routes, _ := st.Routes(ctx)
	for _, r := range routes {
		if r.Stage == "approved" && r.Depot != nil && *r.Depot == "web" && r.Type != nil {
			t.Errorf("the import wrote a typed copy %s to %s over the plain route", r.ID, r.Group)
		}
	}
	if r := store.MatchRoute(routes, store.Bead{Stage: "approved", Type: "train", Depot: &depot, Priority: store.DefaultPriority}, "me"); r == nil || r.ID != plain.ID {
		t.Errorf("a train bead in approved took %+v, want the plain route %s", r, plain.ID)
	}
}

// A fresh import writes one route for a stage the pack carries in two
// flows: the typed copy is left to the plan's own plain route for the
// stage, which the default flow's stage writes first.
func TestPackLeavesTypedStageToPlannedPlainRoute(t *testing.T) {
	st, o := yard(t)
	mustGroup(t, st, store.Group{Name: "decisions", Runner: "manual"})
	tiny := `name: tiny
flows:
  default:
    stages:
      approved: { group: decisions, outcomes: { done: merged } }
      merged: { end: true }
  train:
    type: train
    stages:
      approved: { group: decisions, outcomes: { done: merged } }
      merged: { end: true }
groups:
  decisions: { people: true }
`
	plan := build(t, st, load(t, writePack(t, map[string]string{"pack.yaml": tiny})), o)
	for _, a := range routeActions(plan) {
		if strings.Contains(a, "stage approved type train") && (strings.HasPrefix(a, pack.Create+" ") || strings.HasPrefix(a, pack.Change+" ") || strings.HasPrefix(a, pack.Unchanged+" ")) {
			t.Errorf("a fresh import would write %s though its plain route covers it; routes:\n%s", a, strings.Join(routeActions(plan), "\n"))
		}
	}
	if !notesHave(plan, "stage approved of flow train", "worked by own plain route to decisions", "no route written") {
		t.Errorf("notes do not leave the typed stage to the planned plain route:\n%s", strings.Join(plan.Notes, "\n"))
	}
}

// A depot's plain route to another group does not cover the pack's typed
// stage: the typed route is still written. Open is in the train flow
// alone, so no planned plain route covers it either.
func TestPackKeepsTypedStageWherePlainRouteLeadsElsewhere(t *testing.T) {
	st, o := yard(t)
	mustGroup(t, st, store.Group{Name: "decisions", Runner: "manual"})
	mustGroup(t, st, store.Group{Name: "spare", Runner: "manual"})
	any := store.OriginatorAny
	depot := "web"
	if _, err := st.AddRoute(ctx, store.Route{Stage: "open", Depot: &depot, Group: "spare", Originator: &any}, "me"); err != nil {
		t.Fatal(err)
	}
	tiny := `name: tiny
flows:
  default:
    stages:
      approved: { group: decisions, outcomes: { done: merged } }
      merged: { end: true }
  train:
    type: train
    stages:
      open: { group: decisions, outcomes: { done: merged } }
      merged: { end: true }
groups:
  decisions: { people: true }
`
	plan := build(t, st, load(t, writePack(t, map[string]string{"pack.yaml": tiny})), o)
	if !hasRouteAction(plan, "create depot web stage open type train -> decisions") {
		t.Fatalf("the plan does not write the typed route; routes:\n%s", strings.Join(routeActions(plan), "\n"))
	}
	if notesHave(plan, "stage open of flow train", "worked by own plain route") {
		t.Errorf("the plan leaves the typed stage to a plain route for another group:\n%s", strings.Join(plan.Notes, "\n"))
	}
}

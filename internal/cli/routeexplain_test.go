package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// explainYard is a yard whose operator is me, with the depots web and
// other and the groups quotaCLI routes to.
func explainYard(t *testing.T) (*app, store.API, context.Context) {
	t.Helper()
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "other"} {
		if err := st.PutDepot(ctx, store.Depot{Name: name}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return a, st, ctx
}

func explainStrp(s string) *string { return &s }

// route explain names the winner and every matching route in order with
// the leg that placed it below the one above — priority, specificity,
// own before default, id — the routes considered that do not match and
// why, and that no default is set aside for the stage.
func TestRouteExplainNamesTheWinnerAndEveryLeg(t *testing.T) {
	t.Parallel()
	a, st, ctx := explainYard(t)
	fast, hard := "fast", "hard"
	add := func(r store.Route) *store.Route {
		t.Helper()
		saved, err := st.AddRoute(ctx, r, "me")
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	r1 := add(store.Route{Stage: "new", Label: &fast, AndLabels: []string{hard}, Priority: 5, Group: "a"})
	r2 := add(store.Route{Stage: "new", Label: &fast, AndLabels: []string{hard}, Group: "a"})
	r3 := add(store.Route{Stage: "new", Depot: explainStrp("web"), Group: "b"})
	r4 := add(store.Route{Stage: "new", Label: &fast, Group: "b"})
	bug := "bug"
	r5 := add(store.Route{Stage: "new", Type: &bug, Group: "b"})
	ghost := "ghost"
	r6 := add(store.Route{Stage: "new", Label: &ghost, Priority: 9, Group: "b"})
	r7 := add(store.Route{Stage: "review", Group: "people"})

	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Type: "bug",
		Labels: []string{hard, fast}, Priority: store.DefaultPriority}, "me")
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.routeCmd(), "explain", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	// r4 and r5 ask for as much at one priority in one scope: the id
	// decides, whichever it mints.
	fourth, fifth := r4, r5
	if r5.ID < r4.ID {
		fourth, fifth = r5, r4
	}
	fourthDesc, fifthDesc := "new, label fast, priority 0", "new, type bug, priority 0"
	if fourth == r5 {
		fourthDesc, fifthDesc = fifthDesc, fourthDesc
	}
	want := []string{
		"bead " + b.ID + ": stage new, type bug, depot web, labels fast, hard, priority P2, originator me (the operator)",
		"matches in web's effective routes, in order:",
		"  1. " + r1.ID + "  default  new, labels fast, hard, priority 5  -> a  WINNER",
		"  2. " + r2.ID + "  default  new, labels fast, hard, priority 0  -> a  (1 wins on priority: 5 over 0)",
		"  3. " + r3.ID + "  own  new, depot web, priority 0  -> b  (2 wins on specificity: 2 over 1)",
		"  4. " + fourth.ID + "  default  " + fourthDesc + "  -> b  (3 wins: own before default)",
		"  5. " + fifth.ID + "  default  " + fifthDesc + "  -> b  (4 wins on id)",
		"considered and not matching:",
		"  " + r6.ID + "  default  new, label ghost, priority 9: the bead does not have label ghost",
		"  " + r7.ID + "  default  review, priority 0: the bead is in stage new",
		"defaults set aside in web for this stage: none",
		"quota: the winning route names one group with no condition; nothing to ask a provider for",
	}
	if out != strings.Join(want, "\n")+"\n" {
		t.Errorf("route explain %s =\n%s\nwant\n%s", b.ID, out, strings.Join(want, "\n")+"\n")
	}

	// The explanation's winner is the dispatcher's: the first match in
	// the same effective set.
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w := store.MatchRoute(routes, *b, "me"); w == nil || w.ID != r1.ID {
		t.Errorf("MatchRoute took %+v, want the explained winner %s", w, r1.ID)
	}
}

// A pull-request bead takes its depot's typed own route over the plain
// default, by specificity; a plain bead of the depot takes the default,
// and so does a pull-request bead of a depot with no own route.
func TestRouteExplainTypedBeadTakesItsOwnRoute(t *testing.T) {
	t.Parallel()
	a, st, ctx := explainYard(t)
	def, err := st.AddRoute(ctx, store.Route{Stage: "review", Group: "a"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	pr := "pr"
	own, err := st.AddRoute(ctx, store.Route{Stage: "review", Type: &pr, Depot: explainStrp("web"), Group: "b"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	bead := func(depot, typ string) *store.Bead {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: typ + " in " + depot, Depot: depot,
			Type: typ, Stage: "review", Priority: store.DefaultPriority}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	pull, plain, away := bead("web", "pr"), bead("web", "task"), bead("other", "pr")

	out, err := run(t, a.routeCmd(), "explain", pull.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bead " + pull.ID + ": stage review, type pr, depot web, no labels, priority P2, originator me (the operator)",
		"matches in web's effective routes, in order:",
		"  1. " + own.ID + "  own  review, type pr, depot web, priority 0  -> b  WINNER",
		"  2. " + def.ID + "  default  review, priority 0  -> a  (1 wins on specificity: 2 over 0)",
		"considered and not matching: none",
		"defaults set aside in web for this stage: none",
		"quota: the winning route names one group with no condition; nothing to ask a provider for",
	}
	if out != strings.Join(want, "\n")+"\n" {
		t.Errorf("route explain %s =\n%s\nwant\n%s", pull.ID, out, strings.Join(want, "\n")+"\n")
	}

	out, err = run(t, a.routeCmd(), "explain", plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"bead " + plain.ID + ": stage review, type task, depot web, no labels, priority P2, originator me (the operator)",
		"matches in web's effective routes, in order:",
		"  1. " + def.ID + "  default  review, priority 0  -> a  WINNER",
		"considered and not matching:",
		"  " + own.ID + "  own  review, type pr, depot web, priority 0: the bead is of type task",
		"defaults set aside in web for this stage: none",
		"quota: the winning route names one group with no condition; nothing to ask a provider for",
	}
	if out != strings.Join(want, "\n")+"\n" {
		t.Errorf("route explain %s =\n%s\nwant\n%s", plain.ID, out, strings.Join(want, "\n")+"\n")
	}

	out, err = run(t, a.routeCmd(), "explain", away.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  1. "+def.ID+"  default  review, priority 0  -> a  WINNER\n") ||
		strings.Contains(out, own.ID) {
		t.Errorf("route explain %s names %s where the depot has no own route:\n%s", away.ID, own.ID, out)
	}
}

// A depot that states a default's key takes its own route, and the
// default is set aside with its setter, named as route list --depot
// names it; another depot keeps the default.
func TestRouteExplainSetAsideDefault(t *testing.T) {
	t.Parallel()
	a, st, ctx := explainYard(t)
	def, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	own, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: explainStrp("web"), Group: "b", Priority: 2}, "me")
	if err != nil {
		t.Fatal(err)
	}
	here, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Priority: store.DefaultPriority}, "me")
	if err != nil {
		t.Fatal(err)
	}
	there, err := st.CreateBead(ctx, store.NewBead{Title: "y", Depot: "other", Priority: store.DefaultPriority}, "me")
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.routeCmd(), "explain", here.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bead " + here.ID + ": stage new, type task, depot web, no labels, priority P2, originator me (the operator)",
		"matches in web's effective routes, in order:",
		"  1. " + own.ID + "  own  new, depot web, priority 2  -> b  WINNER",
		"considered and not matching: none",
		"1 default set aside in web for this stage: new (no label, the operator's beads) is stated by " + own.ID + " to b at priority 2",
		"quota: the winning route names one group with no condition; nothing to ask a provider for",
	}
	if out != strings.Join(want, "\n")+"\n" {
		t.Errorf("route explain %s =\n%s\nwant\n%s", here.ID, out, strings.Join(want, "\n")+"\n")
	}
	// The set-aside default is named by its key, as in route list
	// --depot: its id stands nowhere in the text.
	if strings.Contains(out, def.ID) {
		t.Errorf("route explain %s names the set-aside default %s:\n%s", here.ID, def.ID, out)
	}

	out, err = run(t, a.routeCmd(), "explain", there.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  1. "+def.ID+"  default  new, priority 0  -> a  WINNER\n") ||
		!strings.Contains(out, "defaults set aside in other for this stage: none\n") {
		t.Errorf("route explain %s does not keep the default:\n%s", there.ID, out)
	}
}

// A conditioned winner is named with its chain in order and the
// claim-time sentence; bead show after the claim agrees with what the
// explain said before it, naming the same route.
func TestRouteExplainConditionedWinnerAndBeadShowAgrees(t *testing.T) {
	t.Parallel()
	a, st, ran := quotaCLI(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	r, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: explainStrp("web"),
		Group: "a", When: "Weekly<80", Else: []store.RouteGroup{{Name: "b"}}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Priority: store.DefaultPriority}, "me")
	if err != nil {
		t.Fatal(err)
	}

	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.routeCmd(), "explain", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bead " + b.ID + ": stage new, type task, depot web, no labels, priority P2, originator me (the operator)",
		"matches in web's effective routes, in order:",
		"  1. " + r.ID + "  own  new, depot web, priority 0  -> a@Weekly<80 else b  WINNER",
		"considered and not matching: none",
		"defaults set aside in web for this stage: none",
		"quota: the winning route names a@Weekly<80 else b; the choice among these is made at the claim, from the windows then",
	}
	if out != strings.Join(want, "\n")+"\n" {
		t.Fatalf("route explain %s =\n%s\nwant\n%s", b.ID, out, strings.Join(want, "\n")+"\n")
	}
	if ran() {
		t.Error("route explain ran the limits command")
	}
	if after, err := st.Head(ctx); err != nil || after != head {
		t.Errorf("route explain moved the event log from %d to %d, %v", head, after, err)
	}

	passed := []store.PassedGroup{{Group: "a", Reason: weeklyReason}}
	if _, err := st.Claim(ctx, b.ID, "b", "new", "yardr", store.Routed{Route: r.ID, Passed: passed}); err != nil {
		t.Fatal(err)
	}
	shown, err := run(t, a.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\n  routed to b by route " + r.ID + ": a passed over (" + weeklyReason + ")\n"; !strings.Contains(shown, want) {
		t.Errorf("bead show after the claim does not agree with the explain, want %q:\n%s", want, shown)
	}
}

// Route explain asks no provider: with a limits command that fails if
// run, on the winning route's own condition, the command succeeds, the
// command never runs, and the event log stands still.
func TestRouteExplainAsksNoProvider(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	script := filepath.Join(dir, "provider")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+dir+"/asked\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+
		", \"{provider}\"]\n  kinds: { claude: anthropic, codex: openai }\n", "me"); err != nil {
		t.Fatal(err)
	}
	for _, g := range []store.Group{
		{Name: "a", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "b", Runner: "herdr", Config: map[string]any{"kind": "codex"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	r, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80",
		Else: []store.RouteGroup{{Name: "b"}}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Priority: store.DefaultPriority}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.routeCmd(), "explain", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  1. "+r.ID+"  default  new, priority 0  -> a@Weekly<80 else b  WINNER\n") {
		t.Errorf("route explain %s does not name the conditioned winner:\n%s", b.ID, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "asked")); err == nil {
		t.Error("route explain ran the limits command")
	}
	if after, err := st.Head(ctx); err != nil || after != head {
		t.Errorf("route explain moved the event log from %d to %d, %v", head, after, err)
	}
}

// A bead pinned to a flow that shares its stage's name takes the same
// winner as its unpinned twin; the pin is named beside the answer it
// does not change.
func TestRouteExplainPinChangesNothing(t *testing.T) {
	t.Parallel()
	a, st, ctx := explainYard(t)
	f, err := store.ParseFlow("new -> review\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "other"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	r, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	hard := "hard"
	labelled, err := st.AddRoute(ctx, store.Route{Stage: "new", Label: &hard, Group: "b", Priority: 1}, "me")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Priority: store.DefaultPriority}, "me")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := st.CreateBead(ctx, store.NewBead{Title: "y", Depot: "web",
		Priority: store.DefaultPriority, Metadata: map[string]any{"flow": "other"}}, "me")
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.routeCmd(), "explain", pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) == 0 || !strings.HasSuffix(lines[0],
		", pinned to flow other (routing reads no flow)") {
		t.Fatalf("route explain %s does not name the pin:\n%s", pinned.ID, out)
	}
	if !strings.Contains(out, "  1. "+r.ID+"  default  new, priority 0  -> a  WINNER\n") {
		t.Errorf("route explain %s takes another winner than its twin's:\n%s", pinned.ID, out)
	}
	twin, err := run(t, a.routeCmd(), "explain", plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The same answer but for the bead's line: the pin changes nothing,
	// not the winner, not the misses.
	if strings.Join(lines[1:], "\n") != strings.Join(strings.Split(strings.TrimSuffix(twin, "\n"), "\n")[1:], "\n") {
		t.Errorf("the pinned bead's answer differs from its twin's:\n%s\ntwin:\n%s", out, twin)
	}
	if !strings.Contains(out, "  "+labelled.ID+"  default  new, label hard, priority 1: the bead does not have label hard\n") {
		t.Errorf("route explain %s does not miss the labelled route as its twin does:\n%s", pinned.ID, out)
	}
}

// A bead no route matches is unrouted: the lack is named, every
// effective route is considered with why it misses, and no quota line
// follows. A bead that is not there fails as bead show does.
func TestRouteExplainBeadWithNoRoute(t *testing.T) {
	t.Parallel()
	a, st, ctx := explainYard(t)
	if _, err := st.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	ghost := "ghost"
	r1, err := st.AddRoute(ctx, store.Route{Stage: "new", Label: &ghost, Group: "a", Priority: 3}, "me")
	if err != nil {
		t.Fatal(err)
	}
	urgent := 0
	r3, err := st.AddRoute(ctx, store.Route{Stage: "new", BeadPriority: &urgent, Group: "a", Priority: 2}, "me")
	if err != nil {
		t.Fatal(err)
	}
	r4, err := st.AddRoute(ctx, store.Route{Stage: "new", Originator: explainStrp("hal"), Group: "a", Priority: 1}, "me")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := st.AddRoute(ctx, store.Route{Stage: "review", Group: "people"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Priority: store.DefaultPriority}, "me")
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.routeCmd(), "explain", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bead " + b.ID + ": stage new, type task, depot web, no labels, priority P2, originator me (the operator)",
		"no route in web's effective routes matches: the bead is unrouted",
		"considered and not matching:",
		"  " + r1.ID + "  default  new, label ghost, priority 3: the bead does not have label ghost",
		"  " + r3.ID + "  default  new, beads P0, priority 2: the bead is P2, less urgent than P0",
		"  " + r4.ID + "  default  new, originator hal, priority 1: the bead is for me (the operator), not hal",
		"  " + r2.ID + "  default  review, priority 0: the bead is in stage new",
		"defaults set aside in web for this stage: none",
	}
	if out != strings.Join(want, "\n")+"\n" {
		t.Errorf("route explain %s =\n%s\nwant\n%s", b.ID, out, strings.Join(want, "\n")+"\n")
	}

	if _, err := run(t, a.routeCmd(), "explain", "ghost-bead"); err == nil {
		t.Error("route explain ghost-bead succeeded, want bead show's failure")
	}
}

// A bead with no depot is explained against the yard defaults alone:
// another depot's own routes stand nowhere, and none is set aside.
func TestRouteExplainBeadWithNoDepot(t *testing.T) {
	t.Parallel()
	a, st, ctx := explainYard(t)
	def, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	own, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: explainStrp("web"), Group: "b"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Priority: store.DefaultPriority}, "me")
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.routeCmd(), "explain", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"bead " + b.ID + ": stage new, type task, no depot, no labels, priority P2, originator me (the operator)",
		"matches in the yard defaults, in order:",
		"  1. " + def.ID + "  default  new, priority 0  -> a  WINNER",
		"considered and not matching: none",
		"defaults set aside: none (the bead names no depot)",
		"quota: the winning route names one group with no condition; nothing to ask a provider for",
	}
	if out != strings.Join(want, "\n")+"\n" {
		t.Errorf("route explain %s =\n%s\nwant\n%s", b.ID, out, strings.Join(want, "\n")+"\n")
	}
	if strings.Contains(out, own.ID) {
		t.Errorf("route explain %s names another depot's own route %s:\n%s", b.ID, own.ID, out)
	}
}

// Route explain --json carries the bead's facts, every match with its
// leg, the winner, the misses with why, the set-aside defaults and the
// winning chain; the winner is MatchRoute's for every bead tried.
func TestRouteExplainJSON(t *testing.T) {
	t.Parallel()
	a, st, ctx := explainYard(t)
	r1, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a", Priority: 5}, "me")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80",
		Else: []store.RouteGroup{{Name: "b"}}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	own, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: explainStrp("web"), Group: "b", Priority: 2}, "me")
	if err != nil {
		t.Fatal(err)
	}
	mkbead := func(depot string) *store.Bead {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: "in " + depot, Depot: depot, Priority: store.DefaultPriority}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	here, there := mkbead("web"), mkbead("other")
	a.json = true

	out, err := run(t, a.routeCmd(), "explain", here.ID)
	if err != nil {
		t.Fatal(err)
	}
	var exp routeExplanation
	if err := json.Unmarshal([]byte(out), &exp); err != nil {
		t.Fatalf("route explain --json %s = %q: %v", here.ID, out, err)
	}
	if exp.Bead.ID != here.ID || exp.Bead.Stage != "new" || exp.Bead.Type != "task" ||
		exp.Bead.Depot == nil || *exp.Bead.Depot != "web" || exp.Bead.Priority != store.DefaultPriority ||
		exp.Bead.Originator != "me" || exp.Bead.Pin != "" {
		t.Errorf("route explain --json bead = %+v, want %s's facts", exp.Bead, here.ID)
	}
	// The own route sets both defaults aside in web: it stands alone.
	if exp.Winner == nil || *exp.Winner != own.ID || len(exp.Matches) != 1 || exp.Matches[0].ID != own.ID ||
		exp.Matches[0].Effective != "own" || exp.Matches[0].Leg != nil {
		t.Errorf("route explain --json matches = %+v, winner %v, want the own route alone",
			exp.Matches, exp.Winner)
	}
	if len(exp.NonMatching) != 0 {
		t.Errorf("route explain --json non_matching = %+v, want none", exp.NonMatching)
	}
	if len(exp.SetAside) != 2 {
		t.Fatalf("route explain --json set_aside = %+v, want the two defaults", exp.SetAside)
	}
	for _, s := range exp.SetAside {
		if s.Own.ID != own.ID {
			t.Errorf("route explain --json set_aside setter = %s, want %s", s.Own.ID, own.ID)
		}
	}
	if exp.Quota == nil || len(exp.Quota.Groups) != 1 || exp.Quota.Groups[0].Name != "b" || exp.Quota.ClaimTime {
		t.Errorf("route explain --json quota = %+v, want the one group decided", exp.Quota)
	}

	out, err = run(t, a.routeCmd(), "explain", there.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &exp); err != nil {
		t.Fatalf("route explain --json %s = %q: %v", there.ID, out, err)
	}
	if exp.Winner == nil || *exp.Winner != r1.ID || len(exp.Matches) != 2 ||
		exp.Matches[0].ID != r1.ID || exp.Matches[1].ID != r2.ID {
		t.Fatalf("route explain --json matches = %+v, winner %v, want the two defaults in order",
			exp.Matches, exp.Winner)
	}
	leg := exp.Matches[1].Leg
	if leg == nil || leg.Above != r1.ID || leg.Leg != store.LegPriority ||
		len(leg.Over) != 2 || leg.Over[0] != 5 || leg.Over[1] != 0 {
		t.Errorf("route explain --json leg = %+v, want priority 5 over 0 above %s", leg, r1.ID)
	}
	if exp.Quota == nil || len(exp.Quota.Groups) != 1 || exp.Quota.ClaimTime {
		t.Errorf("route explain --json quota = %+v, want the winner's one group", exp.Quota)
	}

	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []*store.Bead{here, there} {
		w := store.MatchRoute(routes, *b, "me")
		if w == nil {
			t.Fatalf("MatchRoute matches nothing for %s", b.ID)
		}
		js := &app{st: st, actor: "me", json: true}
		out, err := run(t, js.routeCmd(), "explain", b.ID)
		if err != nil {
			t.Fatal(err)
		}
		var e routeExplanation
		if err := json.Unmarshal([]byte(out), &e); err != nil || e.Winner == nil || *e.Winner != w.ID {
			t.Errorf("route explain --json winner = %+v, %v; MatchRoute took %s", e.Winner, err, w.ID)
		}
	}
}

// missReason names the first filter that fails, in MatchRoute's order,
// and nothing where the route matches: no reason exactly where Matches
// holds.
func TestMissReasonNamesTheFirstFailingFilter(t *testing.T) {
	t.Parallel()
	urgent := 0
	web := store.Bead{Stage: "new", Type: "task", Depot: explainStrp("web"), Priority: store.DefaultPriority}
	for _, tc := range []struct {
		name  string
		route store.Route
		bead  store.Bead
		want  string
	}{
		{"stage", store.Route{Stage: "review", Group: "a"}, web, "the bead is in stage new"},
		{"type", store.Route{Stage: "new", Type: explainStrp("pr"), Group: "a"}, web, "the bead is of type task"},
		{"depot", store.Route{Stage: "new", Depot: explainStrp("other"), Group: "a"}, web, "the bead is in depot web"},
		{"no depot", store.Route{Stage: "new", Depot: explainStrp("web"), Group: "a"},
			store.Bead{Stage: "new", Type: "task", Priority: store.DefaultPriority}, "the bead names no depot"},
		{"label", store.Route{Stage: "new", Label: explainStrp("ghost"), Group: "a"}, web, "the bead does not have label ghost"},
		{"labels", store.Route{Stage: "new", Label: explainStrp("ghost"), AndLabels: []string{"missing"}, Group: "a"},
			web, "the bead does not have labels ghost, missing"},
		{"one label left", store.Route{Stage: "new", Label: explainStrp("ghost"), AndLabels: []string{"missing"}, Group: "a"},
			store.Bead{Stage: "new", Type: "task", Depot: explainStrp("web"), Labels: []string{"ghost"}, Priority: store.DefaultPriority},
			"the bead does not have label missing"},
		{"bead priority", store.Route{Stage: "new", BeadPriority: &urgent, Group: "a"}, web, "the bead is P2, less urgent than P0"},
		{"operator's beads", store.Route{Stage: "new", Group: "a"},
			store.Bead{Stage: "new", Type: "task", Depot: explainStrp("web"), Priority: store.DefaultPriority, Originator: "hal"},
			"the bead is for hal, not the operator"},
		{"another's beads", store.Route{Stage: "new", Originator: explainStrp("hal"), Group: "a"},
			web, "the bead is for me (the operator), not hal"},
		{"match", store.Route{Stage: "new", Group: "a"}, web, ""},
		{"any originator matches", store.Route{Stage: "new", Originator: explainStrp(store.OriginatorAny), Group: "a"},
			store.Bead{Stage: "new", Type: "task", Depot: explainStrp("web"), Priority: store.DefaultPriority, Originator: "hal"}, ""},
	} {
		if got := missReason(tc.route, tc.bead, "me"); got != tc.want {
			t.Errorf("%s: missReason = %q, want %q", tc.name, got, tc.want)
		}
		if matches := tc.route.Matches(tc.bead, "me"); matches != (tc.want == "") {
			t.Errorf("%s: Matches = %v, want %v", tc.name, matches, tc.want == "")
		}
	}
}

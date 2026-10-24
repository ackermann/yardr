package pack_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// export exports depot of the yard at home into a new directory.
func export(t *testing.T, st store.API, depot, home, name string) (*pack.Exported, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	ex, err := pack.Export(ctx, st, pack.ExportOptions{Depot: depot, Home: home, Name: name, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return ex, dir
}

// tree is every file under dir, slash path -> content, with a script's
// mode after its path.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		text, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o111 != 0 {
			rel += " (runs)"
		}
		out[filepath.ToSlash(rel)] = string(text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func leftHas(ex *pack.Exported, parts ...string) bool {
	return slices.ContainsFunc(ex.Left, func(l string) bool {
		for _, p := range parts {
			if !strings.Contains(l, p) {
				return false
			}
		}
		return true
	})
}

// A depot set up from the starter pack and then changed by hand (a role
// edited in the home, a limit changed, a group and a route added) exports
// to a directory that reads as a pack; applied to a fresh yard it gives the
// same flows, routes and prompts, and exporting that yard again writes the
// same directory. The first yard has an operator and a crew member the
// roles name, the second neither: {{operator}} carries the one across, and
// the crew member's name stays plain text and is listed.
func TestExportRoundTrip(t *testing.T) {
	st, o := yard(t)
	if err := st.SetSetting(ctx, "operator", "ann", "me"); err != nil {
		t.Fatal(err)
	}
	applyAs(t, st, build(t, st, load(t, starter), o), answers, "ann")
	roles := config.At(o.Home).PackRoles("starter")
	builder := filepath.Join(roles, "builder.md")
	text, err := os.ReadFile(builder)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "ann") {
		t.Fatalf("the import did not fill the operator into %s", builder)
	}
	if err := os.WriteFile(builder, append(text, "\nAsk ann before you touch the schema. annabel and ann-marie are someone else.\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	limit := 7
	if _, err := st.SetGroup(ctx, "web-builders", store.GroupEdit{Limit: &limit}, "me"); err != nil {
		t.Fatal(err)
	}
	tester := filepath.Join(o.Home, "tester.md")
	if err := os.WriteFile(tester, []byte("# Tester\n\nYou test what the builder built. The yardmaster hears of a failure.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "web-testers", Runner: "herdr", Limit: 2, Config: map[string]any{"prompt": tester, "kind": "claude", "model": "opus"}}, "me"); err != nil {
		t.Fatal(err)
	}
	// The import's route for review, retuned by hand to the testers.
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retuned := false
	for _, r := range routes {
		if r.Stage == "review" && r.Depot != nil && *r.Depot == "web" && r.Group == "web-reviewers" {
			if _, err := st.SetRoute(ctx, r.ID, store.RouteEdit{Group: &store.RouteGroup{Name: "web-testers"}}, "me"); err != nil {
				t.Fatal(err)
			}
			retuned = true
		}
	}
	if !retuned {
		t.Fatalf("no route of web sends review to web-reviewers: %+v", routes)
	}
	// A limit on rounds, changed by hand: the starter sends review-security's
	// changes to decide after three rounds back to new (decide is also where
	// its question goes), and here they go to backlog after two, a stage
	// nothing else of review-security leads to. The starter's other limits,
	// review's changes and the assembly's failed, are the ones it brought.
	flow, err := st.GetFlow(ctx, "starter.default")
	if err != nil {
		t.Fatal(err)
	}
	for stage, lim := range map[string]store.Outcome{"review-security": {Max: 2, Then: "backlog"}} {
		i := slices.IndexFunc(flow.Outcomes[stage], func(o store.Outcome) bool { return o.Name == "changes" })
		flow.Outcomes[stage][i].Max, flow.Outcomes[stage][i].Then = lim.Max, lim.Then
	}
	if _, err := st.PutFlow(ctx, *flow, "me"); err != nil {
		t.Fatal(err)
	}

	ex, d1 := export(t, st, "web", o.Home, "mine")
	p := load(t, d1)
	if p.Name != "mine" || p.Kind != "git" || len(p.Flows) != 2 || p.Flows[0].Name != "default" || p.Flows[1].Name != "train" || p.Flows[1].Type != "train" {
		t.Fatalf("pack = %s %s, flows %+v", p.Name, p.Kind, p.Flows)
	}
	if len(p.Crew) != 0 || len(p.Sources) != 0 || len(p.Declared) != 0 {
		t.Fatalf("the pack carries crew %v, sources %v, declared %v; this export writes none", p.Crew, p.Sources, p.Declared)
	}
	groups := map[string]pack.Group{}
	for _, g := range p.Groups {
		groups[g.Name] = g
	}
	for name, want := range map[string]pack.Group{
		"builders":  {Name: "builders", Agent: "roles/builders.md", Limit: 7},
		"testers":   {Name: "testers", Agent: "roles/testers.md", Limit: 2},
		"assembly":  {Name: "assembly", Run: "scripts/assembly", Limit: 1},
		"backlog":   {Name: "backlog", People: true},
		"decisions": {Name: "decisions", People: true},
	} {
		if got := groups[name]; got.Agent != want.Agent || got.Run != want.Run || got.People != want.People || got.Limit != want.Limit {
			t.Errorf("group %s = %+v, want %+v", name, got, want)
		}
	}
	review := p.Flows[0].Stages[slices.IndexFunc(p.Flows[0].Stages, func(s pack.Stage) bool { return s.Name == "review" })]
	if review.Group != "testers" || review.File != "flows/default/review.md" || len(review.Outcomes) != 4 {
		t.Errorf("stage review = %+v, want the route that wins it today (testers)", review)
	}
	// A limit is written as the mapping and an outcome without one as its
	// stage alone; the stage only a limit sends beads to is no manual move.
	security := p.Flows[0].Stages[slices.IndexFunc(p.Flows[0].Stages, func(s pack.Stage) bool { return s.Name == "review-security" })]
	if want := (store.Outcome{Name: "changes", To: "new", Max: 3, Then: "decide"}); review.Outcomes[2] != want {
		t.Errorf("review changes = %+v, want %+v", review.Outcomes[2], want)
	}
	if want := (store.Outcome{Name: "changes", To: "new", Max: 2, Then: "backlog"}); security.Outcomes[1] != want || len(security.Manual) != 0 {
		t.Errorf("review-security changes = %+v, manual %v, want %+v and no manual move", security.Outcomes[1], security.Manual, want)
	}
	packYAML := tree(t, d1)["pack.yaml"]
	for _, want := range []string{
		"changes: {to: new, max: 3, then: decide}, question: decide}",
		"outcomes: {done: clear, changes: {to: new, max: 2, then: backlog}, question: decide}",
		"outcomes: {done: merged, failed: {to: new, max: 3, then: decide}, question: decide}",
		"outcomes: {done: review, question: decide}",
	} {
		if !strings.Contains(packYAML, want) {
			t.Errorf("pack.yaml lacks %q:\n%s", want, packYAML)
		}
	}
	// The retuned route serves both flows, and no route leads to the
	// reviewers.
	if _, has := groups["reviewers"]; has || len(groups) != 6 {
		t.Errorf("groups = %v, want the six a followed route leads to", groups)
	}
	if !leftHas(ex, "group web-builders", "kind, model") || !leftHas(ex, "group backlog", "members") {
		t.Errorf("left out lacks what the groups keep in the yard:\n%s", strings.Join(ex.Left, "\n"))
	}
	if !leftHas(ex, "roles/testers.md:3: names crew member yardmaster", "plain text", "brings no crew member of that name") {
		t.Errorf("left out lacks the crew member a role names:\n%s", strings.Join(ex.Left, "\n"))
	}
	got, err := os.ReadFile(filepath.Join(d1, "roles", "builders.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "Ask ann ") || !strings.Contains(string(got), "Ask {{operator}} before you touch the schema. annabel and ann-marie are someone else.") {
		t.Errorf("roles/builders.md: the operator is not {{operator}} again as a whole word:\n%s", got)
	}
	if ex.Operator != "ann" || !slices.ContainsFunc(ex.Restored, func(p pack.Place) bool { return p.File == "roles/builders.md" }) {
		t.Errorf("restored = %q %v", ex.Operator, ex.Restored)
	}
	if info, err := os.Stat(filepath.Join(d1, "scripts", "assembly")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("scripts/assembly: %v %v, want a script that runs", info, err)
	}

	// A fresh yard: another depot, no operator, no crew.
	st2 := storetest.New(t)
	if err := st2.PutDepot(ctx, store.Depot{Name: "site", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	o2 := pack.Options{Depot: "site", Home: t.TempDir()}
	apply(t, st2, build(t, st2, p, o2), nil)
	flows, err := st2.DepotFlows(ctx, "site")
	if err != nil || len(flows) != 2 || flows[0].Flow.Name != "mine.default" || flows[1].Type != "train" {
		t.Fatalf("flows of the second yard = %+v, %v", flows, err)
	}
	was, _ := st.DepotFlows(ctx, "web")
	for i := range flows {
		a, b := *was[i].Flow, *flows[i].Flow
		a.Name, b.Name = "", ""
		if a.String() != b.String() {
			t.Errorf("flow %d differs:\n%s\n---\n%s", i, a.String(), b.String())
		}
	}
	routes, _ = st2.Routes(ctx)
	store.SortRoutes(routes)
	site := "site"
	if r := store.MatchRoute(routes, store.Bead{Stage: "review", Depot: &site, Priority: store.DefaultPriority}, ""); r == nil || r.Group != "site-testers" {
		t.Errorf("review in the second yard goes to %+v, want site-testers", r)
	}
	g, err := st2.GetGroup(ctx, "site-testers")
	if err != nil || g.Limit != 2 {
		t.Fatalf("site-testers = %+v, %v", g, err)
	}
	prompt, _ := g.Config["prompt"].(string)
	if text, err := os.ReadFile(prompt); err != nil || !strings.Contains(string(text), "You test what the builder built.") {
		t.Errorf("site-testers reads %s: %q, %v", prompt, text, err)
	}

	_, d2 := export(t, st2, "site", o2.Home, "mine")
	t1, t2 := tree(t, d1), tree(t, d2)
	for name, text := range t1 {
		if t2[name] != text {
			t.Errorf("%s differs after the round trip:\n%s\n--- second:\n%s", name, text, t2[name])
		}
	}
	for name := range t2 {
		if _, ok := t1[name]; !ok {
			t.Errorf("the second export has %s, the first has not", name)
		}
	}
}

// A depot set up from the starter exports its bound group with binds_as
// again, and a yard set up from that export binds the same agent: the
// binding round-trips instead of becoming a group the import asks about.
func TestExportKeepsAgentGroupBindings(t *testing.T) {
	st, o := yard(t)
	apply(t, st, build(t, st, load(t, starter), o), answers)
	rec := record(t, st)
	if i := slices.IndexFunc(rec.Objects, func(po store.PackObject) bool { return po.Kind == "group" && po.Name == "web-security-reviewers" }); i < 0 || rec.Objects[i].Binds != "web-reviewers" {
		t.Fatalf("the record binds nothing as web-reviewers: %+v", rec.Objects)
	}

	_, dir := export(t, st, "web", o.Home, "mine")
	p := load(t, dir)
	i := slices.IndexFunc(p.Groups, func(g pack.Group) bool { return g.Name == "security-reviewers" })
	if i < 0 {
		t.Fatalf("the export has no security-reviewers: %+v", p.Groups)
	}
	if p.Groups[i].BindsAs != "reviewers" {
		t.Errorf("security-reviewers binds as %q, want reviewers", p.Groups[i].BindsAs)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "binds_as: reviewers") {
		t.Errorf("pack.yaml names no binds_as:\n%s", raw)
	}

	st2 := storetest.New(t)
	if err := st2.PutDepot(ctx, store.Depot{Name: "site", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	o2 := pack.Options{Depot: "site", Home: t.TempDir()}
	plan := build(t, st2, p, o2)
	sec := find(t, plan, "group", "site-security-reviewers")
	if sec.Binds != "site-reviewers" || len(sec.Asks) != 0 {
		t.Fatalf("site-security-reviewers = %+v, want a create that binds as site-reviewers and asks nothing", sec)
	}
	for _, id := range questionIDs(plan) {
		if strings.Contains(id, "site-security-reviewers") {
			t.Fatalf("the import asks %s", id)
		}
	}
	apply(t, st2, plan, map[string]string{"group.site-reviewers.kind": "codex", "group.site-reviewers.model": "luna"})
	want := []any{"codex", "luna", nil}
	for _, name := range []string{"site-reviewers", "site-security-reviewers"} {
		if got := agentOf(groupOf(t, st2, name)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s runs %v, want %v", name, got, want)
		}
	}
}

// A bound group the depot changed since its import runs another agent, so
// binds_as would say what is no longer so: it is exported as its own
// agent group, and said.
func TestExportDropsABindingTheDepotChanged(t *testing.T) {
	st, o := yard(t)
	apply(t, st, build(t, st, load(t, starter), o), answers)
	g, err := st.GetGroup(ctx, "web-security-reviewers")
	if err != nil {
		t.Fatal(err)
	}
	g.Config["model"] = "someone-else"
	if err := st.PutGroup(ctx, *g, "me"); err != nil {
		t.Fatal(err)
	}

	ex, dir := export(t, st, "web", o.Home, "mine")
	p := load(t, dir)
	i := slices.IndexFunc(p.Groups, func(g pack.Group) bool { return g.Name == "security-reviewers" })
	if i < 0 {
		t.Fatalf("the export has no security-reviewers: %+v", p.Groups)
	}
	if p.Groups[i].Agent == "" || p.Groups[i].BindsAs != "" {
		t.Errorf("security-reviewers = %+v, want an agent group that binds as none", p.Groups[i])
	}
	if !leftHas(ex, "group web-security-reviewers", "bound as web-reviewers") {
		t.Errorf("left out lacks the dropped binding:\n%s", strings.Join(ex.Left, "\n"))
	}
}

// A bound group whose binding names a group outside the pack is exported
// as its own agent group: binds_as cannot name what the pack has not.
func TestExportDropsABindingToAGroupOutsideThePack(t *testing.T) {
	st, o := yard(t)
	apply(t, st, build(t, st, load(t, starter), o), answers)
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(routes, func(r store.Route) bool { return r.Stage == "review" && r.Group == "web-reviewers" })
	if i < 0 {
		t.Fatalf("no route works review with web-reviewers: %+v", routes)
	}
	if err := st.DeleteRoute(ctx, routes[i].ID, "me"); err != nil {
		t.Fatal(err)
	}

	ex, dir := export(t, st, "web", o.Home, "mine")
	p := load(t, dir)
	if slices.ContainsFunc(p.Groups, func(g pack.Group) bool { return g.Name == "reviewers" }) {
		t.Fatalf("the export has reviewers, which no route leads to: %+v", p.Groups)
	}
	i = slices.IndexFunc(p.Groups, func(g pack.Group) bool { return g.Name == "security-reviewers" })
	if i < 0 {
		t.Fatalf("the export has no security-reviewers: %+v", p.Groups)
	}
	if p.Groups[i].Agent == "" || p.Groups[i].BindsAs != "" {
		t.Errorf("security-reviewers = %+v, want an agent group that binds as none", p.Groups[i])
	}
	if !leftHas(ex, "group web-security-reviewers", "bound as web-reviewers") {
		t.Errorf("left out lacks the dropped binding:\n%s", strings.Join(ex.Left, "\n"))
	}
}

// Nothing of the work is in an export: the depot's files, its beads and
// their notes, the mail, the settings, the depot's test command, and what a
// group keeps of machines and persons. The teacher's depot holds students'
// essays.
func TestExportCarriesNothingOfTheWork(t *testing.T) {
	st := storetest.New(t)
	depotDir := t.TempDir()
	for name, text := range map[string]string{
		"essays/mira.txt": "SECRET-ESSAY my summer by Mira",
		"grades.csv":      "SECRET-GRADES mira,4",
		".yardr/depot.md": "SECRET-RULES never grade after ten",
	} {
		path := filepath.Join(depotDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "essays", Kind: store.DepotDir, Path: depotDir, Test: "SECRET-TEST ./check"}, "me"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := st.SetSetting(ctx, "operator", "SECRET-TEACHER", "me"); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, "../../packs/dir"), pack.Options{Depot: "essays", Home: home})
	apply(t, st, plan, map[string]string{"group.backlog.members": "SECRET-MEMBER", "group.essays-builders.model": "SECRET-MODEL", "group.essays-builders.args": "--SECRET-ARG"})
	b, err := st.CreateBead(ctx, store.NewBead{Title: "SECRET-TITLE correct Mira's essay", Body: "SECRET-BODY she wrote about her summer", Depot: "essays", Labels: []string{"SECRET-LABEL"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddNote(ctx, b.ID, "SECRET-NOTE two commas missing", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "clerk", Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.Mail(ctx, "clerk", "SECRET-MAIL Mira's parents called", "me"); err != nil {
		t.Fatal(err)
	}

	ex, dir := export(t, st, "essays", home, "")
	if ex.Pack != "essays" || ex.Kind != "dir" {
		t.Fatalf("exported %s kind %s", ex.Pack, ex.Kind)
	}
	files := tree(t, dir)
	if len(files) < 8 {
		t.Fatalf("the export wrote %d files, want the pack: %v", len(files), files)
	}
	for name, text := range files {
		for _, s := range []string{"SECRET", "Mira", depotDir, home} {
			if strings.Contains(name, s) || strings.Contains(text, s) {
				t.Errorf("%s holds %q: nothing of the work, the yard's settings or its paths goes in a pack", name, s)
			}
		}
	}
	// The operator was in the installed files, and is a placeholder again.
	if !strings.Contains(files["flows/default/decide.md"], "{{operator}} decides") {
		t.Errorf("flows/default/decide.md = %q", files["flows/default/decide.md"])
	}
	// The rules file is the depot's: said, and not copied.
	if !leftHas(ex, ".yardr/depot.md", "not in the pack") {
		t.Errorf("left out lacks the depot's rules:\n%s", strings.Join(ex.Left, "\n"))
	}
}

// handYard is a yard set up by hand: a flow in the store the depot is
// pointed at, and groups with no route yet.
func handYard(t *testing.T) (store.API, string) {
	t.Helper()
	st := storetest.New(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("a -> b on done\nb -> c on done\nc -> d on done\nd -> e on done\ne -> f on done\nf -> done on done\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name, f.Terminal = "hand", []string{"done"}
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "web", "", "hand", "me"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	role := filepath.Join(home, "role.md")
	if err := os.WriteFile(role, []byte("# A role\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web-one", "web-two", "wide", "other"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Limit: 1, Config: map[string]any{"prompt": role}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return st, home
}

func route(t *testing.T, st store.API, r store.Route) {
	t.Helper()
	if _, err := st.AddRoute(ctx, r, "me"); err != nil {
		t.Fatal(err)
	}
}

// A stage gets the group that works it today only where that is one plain
// route in the depot's effective set. An own route of a default's key sets
// that default aside for this depot alone, so the own route works a stage
// whichever was added first, and a plain yard default alone works its
// stage and is followed. A quotaed winner, and a
// filtered route that wins for only some beads before a whole-flow route,
// leave the stage with no group, and every such route is listed with its
// groups and why.
func TestExportFollowsTheRouteThatWorksTheStage(t *testing.T) {
	st, home := handYard(t)
	depot, label := "web", "hard"
	// a: the depot's route sets aside a yard default of the same key.
	route(t, st, store.Route{Stage: "a", Depot: &depot, Group: "web-one"})
	route(t, st, store.Route{Stage: "a", Group: "wide"})
	// b: a yard default alone: the pack follows it.
	route(t, st, store.Route{Stage: "b", Group: "wide"})
	// c: the same, the default added first.
	route(t, st, store.Route{Stage: "c", Group: "wide"})
	route(t, st, store.Route{Stage: "c", Depot: &depot, Group: "web-one"})
	// d: the depot's route has a further group.
	route(t, st, store.Route{Stage: "d", Depot: &depot, Group: "web-one", Else: []store.RouteGroup{{Name: "other"}}})
	// e: a route by label beside the plain one.
	route(t, st, store.Route{Stage: "e", Depot: &depot, Group: "web-one"})
	route(t, st, store.Route{Stage: "e", Depot: &depot, Label: &label, Group: "web-two"})
	// f: the depot's whole-flow route outranks a yard default by type,
	// which asks as much (its type against the depot), so it cannot win.
	typeTrain := "train"
	route(t, st, store.Route{Stage: "f", Depot: &depot, Group: "web-one"})
	route(t, st, store.Route{Stage: "f", Type: &typeTrain, Group: "web-two"})

	ex, dir := export(t, st, "web", home, "")
	p := load(t, dir)
	got := map[string]string{}
	for _, s := range p.Flows[0].Stages {
		got[s.Name] = s.Group
	}
	for stage, want := range map[string]string{"a": "one", "b": "wide", "c": "one", "d": "", "e": "", "f": "one", "done": ""} {
		if got[stage] != want {
			t.Errorf("stage %s has group %q in the pack, want %q", stage, got[stage], want)
		}
	}
	if len(p.Groups) != 2 {
		t.Errorf("groups = %+v, want the two a followed route leads to", p.Groups)
	}
	for _, want := range [][]string{
		{"stage d of flow hand: no group in the pack", "web-one else other"},
		{"route depot web stage d -> web-one else other", "further groups"},
		{"stage e of flow hand: no group in the pack", "only some of its beads"},
		{"route depot web stage e label hard -> web-two", "label"},
		{"route depot web stage e -> web-one", "another route of the stage"},
		{"route yard default stage f type train -> web-two", "type train"},
		{"group other: not in the pack"},
		{"group web-two: not in the pack"},
	} {
		if !leftHas(ex, want...) {
			t.Errorf("left out lacks %q", want)
		}
	}
	if leftHas(ex, "stage a of flow hand") || leftHas(ex, "route depot web stage a ->") ||
		leftHas(ex, "stage b of flow hand") || leftHas(ex, "route yard default stage b") ||
		leftHas(ex, "stage c of flow hand") || leftHas(ex, "route depot web stage c ->") {
		t.Errorf("a followed stage or route is listed as left out:\n%s", strings.Join(ex.Left, "\n"))
	}
	if t.Failed() {
		t.Log(strings.Join(ex.Left, "\n"))
	}
}

func TestExportTurnsGroupCallsIntoAsks(t *testing.T) {
	st, home := handYard(t)
	if _, err := st.PutSource(ctx, "crm.yaml", crm, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GrantSource(ctx, "crm", store.GrantRead, store.Grantee{Group: "web-one"}, "", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "ada", "me"); err != nil {
		t.Fatal(err)
	}
	// A pack carries the group's plain grants as asks, but must not turn
	// Ada's grant of an update as her into a plain grant on import.
	if _, err := st.GrantSource(ctx, "crm", "note", store.Grantee{Group: "web-one"}, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	depot := "web"
	route(t, st, store.Route{Stage: "a", Depot: &depot, Group: "web-one"})

	ex, dir := export(t, st, "web", home, "")
	p := load(t, dir)
	if len(p.Groups) != 1 || len(p.Groups[0].Calls) != 1 || p.Groups[0].Calls[0].Source != "crm" ||
		!slices.Equal(p.Groups[0].Calls[0].Calls, []string{store.GrantRead}) {
		t.Fatalf("group calls in the pack = %+v", p.Groups)
	}
	if len(p.Declared) != 0 || len(p.Sources) != 0 || len(p.Crew) != 0 {
		t.Fatalf("the export carried source or crew objects: %+v", p)
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(dir, pack.File))), "calls:") {
		t.Fatal("pack.yaml does not preserve the group's source-call ask")
	}
	target := storetest.New(t)
	if err := target.PutDepot(ctx, store.Depot{Name: "site", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.PutSource(ctx, "crm.yaml", crm, "me"); err != nil {
		t.Fatal(err)
	}
	credential(t, target, "crm", "CRM_TOKEN=not-a-real-one\n")
	plan := build(t, target, p, pack.Options{Depot: "site", Home: t.TempDir()})
	if len(plan.Calls) != 1 || !slices.Equal(plan.Calls[0].Asks, []string{store.GrantRead}) || len(plan.Calls[0].Has) != 0 {
		t.Fatalf("the import does not ask again for the calls: %+v", plan.Calls)
	}
	if grants, err := target.SourceGrants(ctx, "crm"); err != nil || len(grants) != 0 {
		t.Fatalf("the export granted source calls: %+v, %v", grants, err)
	}
	if len(ex.Left) == 0 {
		t.Fatal("the example yard should still report other things left out")
	}
}

func TestExportReadsRepositoryFlowsAndRoles(t *testing.T) {
	st := storetest.New(t)
	repo := t.TempDir()
	files := map[string]string{
		".yardr/flows/default.flow":             "new -> nested/review on done\nnested/review -> done on done\ndone terminal\n",
		".yardr/flows/default/nested/review.md": "Review this work from the depot repository.\n",
		".yardr/roles/builder.md":               "# Repository builder role\n",
		"students/essay.txt":                    "PRIVATE-STUDENT-ESSAY\n",
	}
	for name, text := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "--initial-branch=main")
	git("config", "user.name", "Export Test")
	git("config", "user.email", "export-test@example.invalid")
	git("add", ".")
	git("commit", "-m", "repository flow and role")

	if err := st.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotGit, Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	role := store.Group{Name: "web-builders", Runner: "herdr", Limit: 1, Config: map[string]any{"prompt": "roles/builder.md"}}
	if err := st.PutGroup(ctx, role, "me"); err != nil {
		t.Fatal(err)
	}
	depot := "web"
	if _, err := st.AddRoute(ctx, store.Route{Stage: "nested/review", Depot: &depot, Group: role.Name}, "me"); err != nil {
		t.Fatal(err)
	}

	ex, dir := export(t, st, "web", t.TempDir(), "")
	p := load(t, dir)
	if len(p.Flows) != 1 || len(p.Flows[0].Stages) != 3 {
		t.Fatalf("repository flow = %+v", p.Flows)
	}
	var review pack.Stage
	for _, stage := range p.Flows[0].Stages {
		if stage.Name == "nested/review" {
			review = stage
		}
	}
	if review.Group != "builders" || review.File != "flows/default/nested/review.md" {
		t.Fatalf("repository stage = %+v", review)
	}
	if got := mustRead(t, filepath.Join(dir, filepath.FromSlash(review.File))); got != files[".yardr/flows/default/nested/review.md"] {
		t.Errorf("stage file = %q", got)
	}
	if got := mustRead(t, filepath.Join(dir, "roles/builders.md")); got != files[".yardr/roles/builder.md"] {
		t.Errorf("role file = %q", got)
	}
	if leftHas(ex, "PRIVATE-STUDENT-ESSAY") || strings.Contains(strings.Join(treeLines(t, dir), "\n"), "PRIVATE-STUDENT-ESSAY") {
		t.Fatal("a depot work file was copied into the pack")
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func treeLines(t *testing.T, dir string) []string {
	t.Helper()
	files := tree(t, dir)
	var lines []string
	for name, text := range files {
		lines = append(lines, name+"\n"+text)
	}
	return lines
}

// peers is a yard one of whose groups another yard works.
type peers struct {
	store.API
	group string
}

func (p peers) Groups(ctx context.Context) ([]store.Group, error) {
	gs, err := p.API.Groups(ctx)
	for i := range gs {
		if gs[i].Name == p.group {
			gs[i].Runner = store.PeerRunner
		}
	}
	return gs, err
}

// What would make a pack that is wrong without saying so is refused, and
// nothing is written.
func TestExportRefuses(t *testing.T) {
	depot := "web"
	for _, c := range []struct {
		name string
		set  func(t *testing.T, st store.API, home string) store.API
		opts pack.ExportOptions
		want []string
	}{
		{name: "a group another yard works", set: func(t *testing.T, st store.API, home string) store.API {
			route(t, st, store.Route{Stage: "a", Depot: &depot, Group: "web-one"})
			return peers{st, "web-one"}
		}, want: []string{"group web-one", "another yard", "runner peer"}},
		{name: "two groups of one name", set: func(t *testing.T, st store.API, home string) store.API {
			if err := st.PutGroup(ctx, store.Group{Name: "one", Runner: "manual", Limit: 1}, "me"); err != nil {
				t.Fatal(err)
			}
			route(t, st, store.Route{Stage: "a", Depot: &depot, Group: "web-one"})
			route(t, st, store.Route{Stage: "b", Depot: &depot, Group: "one"})
			return st
		}, want: []string{"groups web-one and one would both be one in the pack"}},
		{name: "a command that names no file", set: func(t *testing.T, st store.API, home string) store.API {
			// The file a later word names is not the script: only the
			// command's own word is looked at.
			if err := os.WriteFile(filepath.Join(home, "essay.txt"), []byte("an essay"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := st.PutGroup(ctx, store.Group{Name: "web-land", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "cat " + filepath.Join(home, "essay.txt")}}, "me"); err != nil {
				t.Fatal(err)
			}
			route(t, st, store.Route{Stage: "a", Depot: &depot, Group: "web-land"})
			return st
		}, want: []string{"group web-land runs", "names no file by its full path"}},
		{name: "an agent group with no prompt", set: func(t *testing.T, st store.API, home string) store.API {
			if err := st.PutGroup(ctx, store.Group{Name: "web-bare", Runner: "herdr", Limit: 1}, "me"); err != nil {
				t.Fatal(err)
			}
			route(t, st, store.Route{Stage: "a", Depot: &depot, Group: "web-bare"})
			return st
		}, want: []string{"group web-bare has no prompt"}},
		{name: "a file that holds a placeholder", set: func(t *testing.T, st store.API, home string) store.API {
			if err := os.WriteFile(filepath.Join(home, "role.md"), []byte("# A role\n\nAsk {{boss}}.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			route(t, st, store.Route{Stage: "a", Depot: &depot, Group: "web-one"})
			return st
		}, want: []string{"role.md:3: holds {{boss}}", "no import fills"}},
		{name: "a pack name the rule refuses", set: func(t *testing.T, st store.API, home string) store.API { return st },
			opts: pack.ExportOptions{Name: "my pack"}, want: []string{`pack name "my pack"`}},
		{name: "no such depot", set: func(t *testing.T, st store.API, home string) store.API { return st },
			opts: pack.ExportOptions{Depot: "nope"}, want: []string{"depot nope"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, home := handYard(t)
			api := c.set(t, st, home)
			o := c.opts
			o.Home, o.Dir = home, filepath.Join(t.TempDir(), "out")
			if o.Depot == "" {
				o.Depot = "web"
			}
			_, err := pack.Export(ctx, api, o)
			if err == nil {
				t.Fatal("exported")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error lacks %q: %v", want, err)
				}
			}
			if _, err := os.Stat(o.Dir); !os.IsNotExist(err) {
				t.Errorf("a refused export wrote %s", o.Dir)
			}
		})
	}
}

// A depot with no flow has nothing to export, and a directory with
// anything in it is never written over.
func TestExportNeedsAFlowAndAnEmptyDirectory(t *testing.T) {
	st, o := yard(t)
	if _, err := pack.Export(ctx, st, pack.ExportOptions{Depot: "web", Home: o.Home, Dir: filepath.Join(t.TempDir(), "out")}); err == nil || !strings.Contains(err.Error(), "depot web has no flow") {
		t.Fatalf("err = %v", err)
	}
	st, home := handYard(t)
	dir := t.TempDir()
	// An empty directory is taken.
	if _, err := pack.Export(ctx, st, pack.ExportOptions{Depot: "web", Home: home, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	before := tree(t, dir)
	if _, err := pack.Export(ctx, st, pack.ExportOptions{Depot: "web", Home: home, Dir: dir}); err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("err = %v", err)
	}
	if after := tree(t, dir); len(after) != len(before) || after["pack.yaml"] != before["pack.yaml"] {
		t.Fatalf("the refused export changed %s", dir)
	}
}

// A script group's script is copied as it is, braces and all, and what its
// command sets before it and passes after it stays in the yard, and is said.
// The pack has an env for the variables, but an export writes none: they are
// this yard's choices (where it pushes, how it lands), and a pack that
// carried them would make them the default of every yard that imports it.
func TestExportCopiesTheScriptAlone(t *testing.T) {
	st, home := handYard(t)
	dir := filepath.Join(home, "my scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "land")
	body := "#!/bin/sh\necho \"{{not a placeholder}} $YARDR_BEAD\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "web-land", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "YARDR_LAND=squash YARDR_PUSH=origin '" + script + "' --fast"}}, "me"); err != nil {
		t.Fatal(err)
	}
	depot := "web"
	route(t, st, store.Route{Stage: "a", Depot: &depot, Group: "web-land"})
	ex, out := export(t, st, "web", home, "")
	files := tree(t, out)
	if files["scripts/land (runs)"] != body {
		t.Fatalf("scripts/land = %q; files %v", files["scripts/land (runs)"], files)
	}
	if text := files["pack.yaml"]; strings.Contains(text, "YARDR_") || strings.Contains(text, "env:") || strings.Contains(text, "--fast") {
		t.Errorf("pack.yaml carries the command's variables or arguments:\n%s", text)
	}
	if !leftHas(ex, "group web-land", "script alone") {
		t.Errorf("left out = %v", ex.Left)
	}
}

// A store flow's stage name may hold dots and slashes. One whose stage file
// would leave its flow's directory in the pack (onto a role, say) is
// refused, and nothing is written.
func TestExportRefusesAStageFileOutsideItsFlowsDirectory(t *testing.T) {
	st := storetest.New(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("new -> a/../review on done\na/../review -> done on done\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name, f.Terminal = "hand", []string{"done"}
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "web", "", "hand", "me"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	file := filepath.Join(store.HomeStageDir(home, "hand"), "review.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("Review it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "out")
	_, err = pack.Export(ctx, st, pack.ExportOptions{Depot: "web", Home: home, Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "outside its flow's directory") {
		t.Fatalf("err = %v, want the stage file refused", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the refused export wrote %s: %v", dir, err)
	}
}

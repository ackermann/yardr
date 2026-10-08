package pack_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/packs"
)

var ctx = context.Background()

// yard is a store with one depot, and a home for the pack's files.
func yard(t *testing.T) (store.API, pack.Options) {
	t.Helper()
	st := storetest.New(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	return st, pack.Options{Depot: "web", Home: t.TempDir()}
}

func load(t *testing.T, dir string) *pack.Pack {
	t.Helper()
	p, err := pack.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func build(t *testing.T, st store.API, p *pack.Pack, o pack.Options) *pack.Plan {
	t.Helper()
	plan, err := pack.Build(ctx, st, p, o)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// actions lists a plan's objects of one action as "<kind> <name>".
func actions(p *pack.Plan, action string) []string {
	out := []string{}
	for _, o := range p.Objects {
		if o.Action == action {
			out = append(out, o.Kind+" "+o.Name)
		}
	}
	return out
}

func find(t *testing.T, p *pack.Plan, kind, name string) pack.Object {
	t.Helper()
	for _, o := range p.Objects {
		if o.Kind == kind && o.Name == name {
			return o
		}
	}
	t.Fatalf("the plan has no %s %s; it has %v", kind, name, p.Objects)
	return pack.Object{}
}

// apply applies the plan as pack apply does. answers are the questions'
// answers by id; a question without one gets an agent kind, a directory, or
// the empty answer (the agent's own model, no members).
func apply(t *testing.T, st store.API, p *pack.Plan, answers map[string]string) *pack.Result {
	t.Helper()
	a := pack.Answers{}
	for _, q := range p.Questions {
		switch q.Field {
		case "kind":
			a[q.ID] = "claude"
		case "dir":
			a[q.ID] = t.TempDir()
		default:
			a[q.ID] = ""
		}
	}
	for k, v := range answers {
		a[k] = v
	}
	res, err := pack.Apply(ctx, st, p, pack.ApplyOptions{Answers: a, Actor: "me"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// copyStarter is the starter pack in a directory the test may change.
func copyStarter(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(starter)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "scripts", "merge-local"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStarterIntoAnEmptyYard(t *testing.T) {
	st, o := yard(t)
	plan := build(t, st, load(t, starter), o)

	install := config.At(o.Home).Pack("starter")
	want := []string{
		"file roles/builder.md", "file roles/reviewer.md", "file roles/yardmaster.md", "file scripts/merge-local",
		"file flows/default/backlog.md", "file flows/default/new.md", "file flows/default/review.md", "file flows/default/approved.md", "file flows/default/decide.md",
		"group web-builders", "group web-reviewers", "group web-assembly", "group backlog", "group decisions",
		"flow starter.default",
		"pointer depot web -> starter.default",
		"route depot web stage backlog -> backlog", "route depot web stage new -> web-builders", "route depot web stage review -> web-reviewers",
		"route depot web stage approved -> web-assembly", "route depot web stage decide -> decisions",
		"crew yardmaster",
		"subscription yardmaster yardr all depot=web stage=decide", "subscription yardmaster yardr all depot=web state=done", "subscription yardmaster linear involved",
	}
	if got := actions(plan, pack.Create); !reflect.DeepEqual(got, want) {
		t.Fatalf("create =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(plan.Objects) != len(want) {
		t.Fatalf("an empty yard has only things to create; the plan also has %v %v %v",
			actions(plan, pack.Change), actions(plan, pack.Unchanged), actions(plan, pack.NotInPack))
	}
	var ids []string
	for _, q := range plan.Questions {
		ids = append(ids, q.ID)
	}
	wantQ := []string{
		"group.web-builders.kind", "group.web-builders.model", "group.web-builders.args",
		"group.web-reviewers.kind", "group.web-reviewers.model", "group.web-reviewers.args",
		"group.backlog.members", "group.decisions.members",
		"crew.yardmaster.crew", "crew.yardmaster.kind", "crew.yardmaster.model", "crew.yardmaster.args", "crew.yardmaster.dir",
	}
	if !reflect.DeepEqual(ids, wantQ) {
		t.Fatalf("questions = %v\nwant %v", ids, wantQ)
	}
	if got := find(t, plan, "group", "web-builders").Asks; !reflect.DeepEqual(got, wantQ[:3]) {
		t.Fatalf("web-builders asks %v", got)
	}

	// What each object would become: paths under the yard's home, never
	// the pack's own, so a bead's branch cannot change them.
	b := find(t, plan, "group", "web-builders").Group
	if b.Runner != "herdr" || b.Limit != 5 || b.Config["prompt"] != filepath.Join(install, "roles", "builder.md") {
		t.Fatalf("web-builders = %+v", b)
	}
	r := find(t, plan, "group", "web-assembly").Group
	if r.Runner != "exec" || r.Limit != 1 || r.Config["cmd"] != filepath.Join(install, "scripts", "merge-local") {
		t.Fatalf("web-assembly = %+v", r)
	}
	if g := find(t, plan, "group", "backlog").Group; g.Runner != "manual" {
		t.Fatalf("backlog = %+v", g)
	}
	f := find(t, plan, "file", "scripts/merge-local").File
	if f.To != filepath.Join(install, "scripts", "merge-local") || f.Mode != "0755" || !filepath.IsAbs(f.From) {
		t.Fatalf("merge-local = %+v", f)
	}
	if got := find(t, plan, "flow", "starter.default").Flow.Stages; got[0] != "backlog" || got[5] != "merged" {
		t.Fatalf("flow stages = %v", got)
	}
	m := find(t, plan, "crew", "yardmaster").CrewMember
	if m.Prompt != filepath.Join(install, "roles", "yardmaster.md") || !reflect.DeepEqual(m.On, []string{store.KindAttention}) {
		t.Fatalf("yardmaster = %+v", m)
	}
	if len(plan.Governs) != 1 || plan.Governs[0].Flow != "starter.default" || plan.Governs[0].Now != "none" || plan.Governs[0].Type != "" {
		t.Fatalf("governs = %+v", plan.Governs)
	}
	if len(plan.Refused) != 0 {
		t.Fatalf("refused: %v", plan.Refused)
	}

	// What linear lacks in a yard that has nothing.
	if len(plan.Sources) != 1 || plan.Sources[0].Source != "linear" || plan.Sources[0].CrewMember != "yardmaster" {
		t.Fatalf("sources = %+v", plan.Sources)
	}
	status := map[string]string{}
	for _, n := range plan.Sources[0].Needs {
		status[n.What] = n.Status
	}
	if want := map[string]string{"hook": "missing", "secret_file": "missing", "identity": "missing", "credential": "missing", "listener": "unknown"}; !reflect.DeepEqual(status, want) {
		t.Fatalf("linear needs = %v, want %v", status, want)
	}

	// The listener is the yard's own to tell once its server has said what
	// it takes webhooks on: missing without an address, ok on the setting's.
	listener := func() pack.Need {
		t.Helper()
		for _, n := range build(t, st, load(t, starter), o).Sources[0].Needs {
			if n.What == "listener" {
				return n
			}
		}
		t.Fatal("no listener need")
		return pack.Need{}
	}
	cfg := &config.Config{Home: o.Home}
	said := func(l hooks.Listener) {
		t.Helper()
		if err := hooks.WriteListener(cfg, l); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	said(hooks.Listener{})
	if n := listener(); n.Status != "missing" || !strings.Contains(n.Detail, "yardr settings set hook_addr 127.0.0.1:8790") {
		t.Fatalf("no address: %+v", n)
	}
	if err := st.SetSetting(ctx, "hook_addr", "127.0.0.1:8790", "me"); err != nil {
		t.Fatal(err)
	}
	said(hooks.Listener{Addr: "127.0.0.1:8790", From: hooks.FromSetting})
	if n := listener(); n.Status != "ok" || !strings.Contains(n.Detail, "127.0.0.1:8790 (the hook_addr setting)") {
		t.Fatalf("the setting: %+v", n)
	}
	said(hooks.Listener{Addr: "127.0.0.1:8790", From: hooks.FromSetting, Error: "address already in use"})
	if n := listener(); n.Status != "missing" || !strings.Contains(n.Detail, "could not listen on 127.0.0.1:8790") {
		t.Fatalf("a taken address: %+v", n)
	}
}

var answers = map[string]string{
	"group.web-builders.kind": "claude", "group.web-builders.model": "opus",
	"group.web-reviewers.kind": "codex", "group.web-reviewers.model": "luna",
	"group.backlog.members": "ann,yardmaster",
}

// Importing twice changes nothing the second time; then a changed limit
// and a changed role are two changes and nothing else.
func TestSecondImportChangesNothing(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), answers)

	plan := build(t, st, load(t, dir), o)
	if c, ch, n := actions(plan, pack.Create), actions(plan, pack.Change), actions(plan, pack.NotInPack); len(c)+len(ch)+len(n) != 0 {
		t.Fatalf("a second import would create %v, change %v, and finds %v not in the pack", c, ch, n)
	}
	if len(plan.Objects) != 25 || len(plan.Questions) != 0 {
		t.Fatalf("%d objects, questions %v", len(plan.Objects), plan.Questions)
	}
	if g := plan.Governs; len(g) != 1 || g[0].Now != "starter.default (store)" {
		t.Fatalf("governs = %+v", g)
	}
	// What the yard chose is still there, and in what the plan would write.
	b := find(t, plan, "group", "web-builders").Group
	if b.Config["kind"] != "claude" || b.Config["model"] != "opus" {
		t.Fatalf("web-builders = %+v", b)
	}
	if g := find(t, plan, "group", "backlog").Group; !reflect.DeepEqual(g.Members, []string{"ann", "yardmaster"}) {
		t.Fatalf("backlog = %+v", g)
	}
	routes, _ := st.Routes(ctx)
	if len(routes) != 5 {
		t.Fatalf("%d routes, want 5", len(routes))
	}

	text, err := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(text), "roles/builder.md,  limit: 5", "roles/builder.md,  limit: 2", 1)
	if changed == string(text) {
		t.Fatal("the starter's builders line is not what this test replaces")
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "roles", "reviewer.md"), []byte("review harder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	if got, want := actions(plan, pack.Change), []string{"file roles/reviewer.md", "group web-builders"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("change = %v, want %v", got, want)
	}
	if c, n := actions(plan, pack.Create), actions(plan, pack.NotInPack); len(c)+len(n) != 0 {
		t.Fatalf("create %v, not in the pack %v", c, n)
	}
	if got, want := find(t, plan, "group", "web-builders").Changes, []pack.Diff{{Field: "limit", Old: 5, New: 2}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("web-builders changes = %v", got)
	}
	if got := find(t, plan, "file", "roles/reviewer.md").Changes; len(got) != 1 || got[0].Field != "content" {
		t.Fatalf("reviewer.md changes = %v", got)
	}
	// And applying those two brings the yard level again.
	apply(t, st, plan, nil)
	plan = build(t, st, load(t, dir), o)
	if got := actions(plan, pack.Unchanged); len(got) != 25 {
		t.Fatalf("after applying the change: %d unchanged of %d", len(got), len(plan.Objects))
	}
}

// A yard set up by hand, as the one that builds yardr was: its groups keep
// what the pack does not carry, its routes are recognized by depot and stage,
// and nothing it has is planned as new.
func TestYardSetUpByHand(t *testing.T) {
	st, o := yard(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutGroup(ctx, store.Group{Name: "web-builders", Runner: "herdr", Limit: 5,
		Config: map[string]any{"kind": "claude", "model": "opus", "idle": "5m", "args": []any{"--permission-mode", "auto"}, "prompt": "roles/builder.md"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "web-reviewers", Runner: "herdr", Limit: 3, Config: map[string]any{"kind": "codex", "prompt": "roles/reviewer.md"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "web-assembly", Runner: "exec", Limit: 1,
		Config: map[string]any{"cmd": "YARDR_PUSH=origin YARDR_NOTIFY=boss /old/scripts/merge-local", "log": "/logs/refinery.log"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "web-mergers", Runner: "herdr", Limit: 1, Config: map[string]any{}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "backlog", Runner: "manual", Limit: 50, Members: []string{"ann", "boss"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 50, Members: []string{"ann"}}, "me"))
	depot := "web"
	for stage, group := range map[string]string{"backlog": "backlog", "new": "web-builders", "review": "web-reviewers", "approved": "web-assembly", "decide": "decisions", "merge": "web-mergers"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Depot: &depot, Group: group, Priority: 3}, "me")
		must(err)
	}
	_, err := st.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", Dir: "/home/boss", MaxAge: "6h",
		Config: map[string]any{"kind": "claude", "prompt": "/old/roles/yardmaster.md", "env_file": "/nowhere.env"},
		On:     []string{store.KindAttention}}, "me")
	must(err)
	must(st.AddSubscription(ctx, "boss", store.Subscription{Source: "linear", Scope: "all"}, "me"))
	must(st.AddSubscription(ctx, "boss", store.Subscription{Source: "yardr", Scope: "all", Where: map[string]string{"label": "ui"}}, "me"))

	o.Crew = map[string]string{"yardmaster": "boss"}
	plan := build(t, st, load(t, starter), o)
	install := config.At(o.Home).Pack("starter")

	for _, o := range plan.Objects {
		if o.Action == pack.Create && (o.Kind == "group" || o.Kind == "route" || o.Kind == "crew") {
			t.Errorf("%s %s is planned as new", o.Kind, o.Name)
		}
	}
	if got, want := actions(plan, pack.Change), []string{"group web-builders", "group web-reviewers", "group web-assembly", "crew boss", "subscription boss linear involved"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("change = %v\nwant %v", got, want)
	}
	if got, want := actions(plan, pack.NotInPack), []string{"group web-mergers", "route depot web stage merge -> web-mergers", "subscription boss yardr all label=ui"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("not in the pack = %v\nwant %v", got, want)
	}
	b := find(t, plan, "group", "web-builders")
	if want := []pack.Diff{{Field: "prompt", Old: "roles/builder.md", New: filepath.Join(install, "roles", "builder.md")}}; !reflect.DeepEqual(b.Changes, want) {
		t.Fatalf("web-builders changes = %v", b.Changes)
	}
	if c := b.Group.Config; c["kind"] != "claude" || c["model"] != "opus" || c["idle"] != "5m" || !reflect.DeepEqual(c["args"], []any{"--permission-mode", "auto"}) {
		t.Fatalf("web-builders would lose what the yard chose: %v", c)
	}
	r := find(t, plan, "group", "web-assembly")
	cmd := "YARDR_PUSH=origin YARDR_NOTIFY=boss " + filepath.Join(install, "scripts", "merge-local")
	if want := []pack.Diff{{Field: "cmd", Old: "YARDR_PUSH=origin YARDR_NOTIFY=boss /old/scripts/merge-local", New: cmd}}; !reflect.DeepEqual(r.Changes, want) {
		t.Fatalf("web-assembly changes = %v", r.Changes)
	}
	if r.Group.Config["log"] != "/logs/refinery.log" {
		t.Fatalf("web-assembly = %+v", r.Group)
	}
	// People groups: the pack names no limit and no members, so both stay.
	if g := find(t, plan, "group", "backlog"); g.Action != pack.Unchanged || g.Group.Limit != 50 || !reflect.DeepEqual(g.Group.Members, []string{"ann", "boss"}) {
		t.Fatalf("backlog = %+v %+v", g, g.Group)
	}
	if rt := find(t, plan, "route", "depot web stage new -> web-builders"); rt.Action != pack.Unchanged || rt.Route.Priority != 3 || len(rt.Route.Delete) != 0 {
		t.Fatalf("route new = %+v %+v", rt, rt.Route)
	}
	res := find(t, plan, "crew", "boss")
	if len(res.Changes) != 1 || res.Changes[0].Field != "prompt" || res.CrewMember.Pack != "yardmaster" || len(res.Asks) != 0 {
		t.Fatalf("boss = %+v (it hears the class as the pack has it: no change)", res)
	}
	sub := find(t, plan, "subscription", "boss linear involved")
	if want := []pack.Diff{{Field: "scope", Old: "all", New: "involved"}}; !reflect.DeepEqual(sub.Changes, want) || sub.Subscription.Remove == nil {
		t.Fatalf("linear subscription = %+v", sub)
	}
	for _, n := range plan.Sources[0].Needs {
		if n.What == "credential" && (n.Status != "missing" || !strings.Contains(n.Detail, "LINEAR_API_KEY")) {
			t.Fatalf("credential = %+v", n)
		}
	}

	// Applied, the yard has still one route per stage and what it chose.
	apply(t, st, plan, nil)
	plan = build(t, st, load(t, starter), o)
	if c, ch := actions(plan, pack.Create), actions(plan, pack.Change); len(c)+len(ch) != 0 {
		t.Fatalf("after the import: create %v, change %v", c, ch)
	}
	boss, err := st.GetCrew(ctx, "boss")
	must(err)
	if boss.Dir != "/home/boss" || boss.MaxAge != "6h" || boss.Config["env_file"] != "/nowhere.env" || len(boss.Subscriptions) != 4 {
		t.Fatalf("boss = %+v", boss)
	}
}

// A directory depot has no git base, but its repository flow still governs it
// until the import points it at the pack's stored flow.
func TestDirDepotFlowIsReportedBeforeAndAfterImport(t *testing.T) {
	dir := t.TempDir()
	flowDir := filepath.Join(dir, ".yardr", "flows")
	if err := os.MkdirAll(flowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flowDir, "default.flow"), []byte("new -> done\ndone terminal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := storetest.New(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	o := pack.Options{Depot: "web", Home: t.TempDir()}
	plan := build(t, st, load(t, starter), o)
	if len(plan.Governs) == 0 || plan.Governs[0].Now != "default (repo (directory))" || plan.Governs[0].Flow != "starter.default" || plan.Governs[0].Source != "store" {
		t.Fatalf("flows governing dir depot = %+v", plan.Governs)
	}
	if !slices.ContainsFunc(plan.Notes, func(n string) bool {
		return strings.Contains(n, ".yardr/flows/default.flow") && strings.Contains(n, "governs nothing until the pointer is cleared")
	}) {
		t.Fatalf("dir depot repository flow note missing: %v", plan.Notes)
	}
}

// A depot that carries a flow file for a type is told what governs that
// type after the import, once: the pack's flow for the type where it has
// one (the dir pack's train flow over a dir depot's type/train.flow), and
// the depot's flow from the pack where it has none, since a depot pointer
// takes the type files over. The plan used to add the second line in both
// cases, and so named two flows for a dir depot's trains. Its notes call a
// dir depot's files the directory's: it is no repository.
func TestTypeFileIsGovernedOnceAfterImport(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{
		"default.flow":    "new -> done\ndone terminal\n",
		"type/train.flow": "open -> done\ndone terminal\n",
	} {
		path := filepath.Join(dir, ".yardr", "flows", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st := storetest.New(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "notes", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	o := pack.Options{Depot: "notes", Home: t.TempDir()}
	sub, err := fs.Sub(packs.FS, packs.Dir)
	if err != nil {
		t.Fatal(err)
	}
	dirPack, err := pack.Load(sub, packs.Embedded)
	if err != nil {
		t.Fatal(err)
	}
	governs := func(plan *pack.Plan) []string {
		var out []string
		for _, g := range plan.Governs {
			out = append(out, fmt.Sprintf("%q: %s, now %s", g.Type, g.Flow, g.Now))
		}
		return out
	}
	plan := build(t, st, dirPack, o)
	if got, want := governs(plan), []string{
		`"": dir.default, now default (repo (directory))`,
		`"train": dir.train, now type/train (repo (directory), type train)`,
	}; !slices.Equal(got, want) {
		t.Errorf("the dir pack: governs = %q\nwant %q", got, want)
	}
	n := 0
	for _, note := range plan.Notes {
		if strings.Contains(note, "governs nothing until the pointer is cleared") {
			n++
			if !strings.Contains(note, "depot notes's directory carries .yardr/flows/") || strings.Contains(note, "repository") {
				t.Errorf("a dir depot's flow file is called a repository's: %s", note)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d notes on the directory's flow files, want 2: %q", n, plan.Notes)
	}
	// A pack with no flow for the type: the depot's flow takes it over.
	plan = build(t, st, load(t, starter), o)
	if got, want := governs(plan), []string{
		`"": starter.default, now default (repo (directory))`,
		`"train": starter.default, now type/train (repo (directory), type train)`,
	}; !slices.Equal(got, want) {
		t.Errorf("the starter pack: governs = %q\nwant %q", got, want)
	}
}

// yardr route add mints an id per call, so a yard may hold two routes for
// one depot and stage, and a route to another group cannot be changed in
// place. The plan replaces them all by one.
func TestRoutesAreReplacedNotAdded(t *testing.T) {
	st, o := yard(t)
	apply(t, st, build(t, st, load(t, starter), o), answers)
	depot := "web"
	if err := st.PutGroup(ctx, store.Group{Name: "others", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	extra, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: &depot, Group: "others", Priority: 9}, "me")
	if err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, starter), o)
	if got, want := actions(plan, pack.Change), []string{"route depot web stage new -> web-builders"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("change = %v, want %v", got, want)
	}
	rt := find(t, plan, "route", "depot web stage new -> web-builders")
	if len(rt.Route.Delete) != 2 || !slices.Contains(rt.Route.Delete, extra.ID) {
		t.Fatalf("route = %+v, want both routes of the stage deleted", rt.Route)
	}
	if want := []pack.Diff{{Field: "group", Old: "others", New: "web-builders"}, {Field: "routes", Old: "2 for this stage", New: "1"}}; !reflect.DeepEqual(rt.Changes, want) {
		t.Fatalf("changes = %v", rt.Changes)
	}
	apply(t, st, plan, nil)
	routes, _ := st.Routes(ctx)
	n := 0
	for _, r := range routes {
		if r.Stage == "new" {
			n++
		}
	}
	if n != 1 || len(routes) != 5 {
		t.Fatalf("%d routes for new, %d in all; want 1 and 5", n, len(routes))
	}
}

// A stage with no group replaces any existing route with no route. Leaving
// the old route behind would keep sending beads to a group the pack removed.
func TestStageWithoutGroupRemovesExistingRoute(t *testing.T) {
	st, o := yard(t)
	if err := st.PutGroup(ctx, store.Group{Name: "old", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	depot := "web"
	old, err := st.AddRoute(ctx, store.Route{Stage: "triage", Depot: &depot, Group: "old"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	text := "name: hand\nflows:\n  default:\n    stages:\n      triage: { manual: [done] }\n      done: { end: true }\ngroups: {}\n"
	if err := os.WriteFile(filepath.Join(dir, pack.File), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, dir), o)
	want := "depot web stage triage -> (no route)"
	if got := actions(plan, pack.Change); !slices.Contains(got, "route "+want) {
		t.Fatalf("changes = %v, want %q", got, "route "+want)
	}
	route := find(t, plan, "route", want)
	if route.Route.Group != "" || !slices.Equal(route.Route.Delete, []string{old.ID}) || route.Changes[0].New != "(no route)" {
		t.Fatalf("route operation = %+v", route)
	}
	if got := actions(plan, pack.NotInPack); slices.Contains(got, "route depot web stage triage -> old") {
		t.Fatalf("old route also shown as left alone: %v", got)
	}
	apply(t, st, plan, nil)
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Stage == "triage" && r.Depot != nil && *r.Depot == depot {
			t.Fatalf("route survived import: %+v", r)
		}
	}
}

// An open bead in a stage the pack's flow lacks refuses the import, as the
// store would refuse the pointer (yardr-wna2, yardr-9ssm). A bead of a type
// with its own pointer, a pinned bead and a closed one are not the flow's.
func TestOpenBeadInAStageTheFlowLacks(t *testing.T) {
	st, o := yard(t)
	depot := "web"
	mk := func(nb store.NewBead) string {
		t.Helper()
		nb.Depot = depot
		b, err := st.CreateBead(ctx, nb, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	if _, err := st.PutFlow(ctx, store.Flow{Name: "trains", Stages: []string{"open", "landed"}, Transitions: map[string][]string{"open": {"landed"}}, Terminal: []string{"landed"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "web", "train", "trains", "me"); err != nil {
		t.Fatal(err)
	}
	stray := mk(store.NewBead{Title: "stray", Stage: "triage"})
	mk(store.NewBead{Title: "fine", Stage: "new"})
	mk(store.NewBead{Title: "a train", Type: "train", Stage: "open"})
	mk(store.NewBead{Title: "pinned", Stage: "open", Metadata: map[string]any{"flow": "trains"}})
	closed := mk(store.NewBead{Title: "closed", Stage: "triage"})
	if _, err := st.CloseBead(ctx, closed, "gone", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}

	plan := build(t, st, load(t, starter), o)
	if len(plan.Refused) != 1 || !strings.Contains(plan.Refused[0], "flow starter.default lacks the stage of open beads that would follow it: "+stray+" (triage);") {
		t.Fatalf("refused = %q", plan.Refused)
	}
	if got, want := actions(plan, pack.NotInPack), []string{"pointer depot web type train -> trains"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("not in the pack = %v", got)
	}
	if g := plan.Governs; len(g) != 2 || g[1].Type != "train" || g[1].Flow != "trains" || g[1].InPack {
		t.Fatalf("governs = %+v", g)
	}
	// The store agrees: it refuses the pointer for the same bead.
	if _, err := st.PutFlow(ctx, *find(t, plan, "flow", "starter.default").Flow, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "web", "", "starter.default", "me"); err == nil || !strings.Contains(err.Error(), stray) {
		t.Fatalf("SetFlowPointer = %v, want it refused naming %s", err, stray)
	}
}

// A flow for a type becomes the type's pointer and routes of that type;
// of two flows a depot could follow, one is imported.
func TestTypeFlowsAndFlowChoice(t *testing.T) {
	st, o := yard(t)
	dir := writePack(t, map[string]string{"pack.yaml": `name: two
flows:
  quick:
    stages:
      new:  { group: people, manual: [done] }
      done: { end: true }
  slow:
    stages:
      new:   { group: people, manual: [check] }
      check: { manual: [done] }
      done:  { end: true }
  trains:
    type: train
    stages:
      open:   { group: people, outcomes: { done: landed } }
      landed: { end: true }
groups:
  people: { people: true, limit: 7 }
`})
	plan := build(t, st, load(t, dir), o)
	want := []string{"group people", "flow two.quick", "flow two.trains", "pointer depot web -> two.quick", "pointer depot web type train -> two.trains",
		"route depot web stage new -> people", "route depot web stage open type train -> people"}
	if got := actions(plan, pack.Create); !reflect.DeepEqual(got, want) {
		t.Fatalf("create = %v\nwant %v", got, want)
	}
	if len(plan.Notes) != 1 || !strings.Contains(plan.Notes[0], "flow slow of the pack is not imported") {
		t.Fatalf("notes = %v", plan.Notes)
	}
	if g := find(t, plan, "group", "people").Group; g.Limit != 7 {
		t.Fatalf("people = %+v", g)
	}

	o.Flow = "slow"
	plan = build(t, st, load(t, dir), o)
	find(t, plan, "pointer", "depot web -> two.slow")
	if !slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.Contains(n, "stage check of flow slow names no group") }) {
		t.Fatalf("notes = %v", plan.Notes)
	}
	apply(t, st, plan, nil)
	plan = build(t, st, load(t, dir), o)
	if c, ch := actions(plan, pack.Create), actions(plan, pack.Change); len(c)+len(ch) != 0 {
		t.Fatalf("after the import: create %v, change %v", c, ch)
	}
	rt := find(t, plan, "route", "depot web stage open type train -> people")
	if rt.Action != pack.Unchanged || rt.Route.Type != "train" {
		t.Fatalf("train route = %+v", rt)
	}

	o.Flow = "trains"
	if _, err := pack.Build(ctx, st, load(t, dir), o); err == nil || !strings.Contains(err.Error(), "it has: slow, quick") && !strings.Contains(err.Error(), "it has: quick, slow") {
		t.Fatalf("--flow trains = %v", err)
	}
}

// A flow the yard holds under the pack's name with other rules is shown
// stage by stage.
func TestChangedFlowIsShownByStage(t *testing.T) {
	st, o := yard(t)
	apply(t, st, build(t, st, load(t, starter), o), answers)
	dir := copyStarter(t)
	text, _ := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	changed := strings.Replace(string(text), "manual: [backlog, new] }", "manual: [backlog] }", 1)
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, dir), o)
	f := find(t, plan, "flow", "starter.default")
	if want := []pack.Diff{{Field: "stage decide", Old: "-> backlog | new", New: "-> backlog"}}; f.Action != pack.Change || !reflect.DeepEqual(f.Changes, want) {
		t.Fatalf("flow = %s %v", f.Action, f.Changes)
	}
}

func TestUnknownDepotAndCrew(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"sheriff": "boss"}
	if _, err := pack.Build(ctx, st, load(t, starter), o); err == nil || !strings.Contains(err.Error(), "the pack has no crew member sheriff") {
		t.Fatalf("Build = %v", err)
	}
	o.Crew, o.Depot = nil, "nope"
	if _, err := pack.Build(ctx, st, load(t, starter), o); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("Build = %v", err)
	}
}

// Building a plan writes nothing: not to the store, not under the home.
func TestBuildWritesNothing(t *testing.T) {
	st, o := yard(t)
	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	build(t, st, load(t, starter), o)
	after, _ := st.Head(ctx)
	groups, _ := st.Groups(ctx)
	flows, _ := st.Flows(ctx)
	entries, _ := os.ReadDir(o.Home)
	if after != head || len(groups) != 0 || len(flows) != 0 || len(entries) != 0 {
		t.Fatalf("events %d -> %d, groups %v, flows %v, home %v", head, after, groups, flows, entries)
	}
}

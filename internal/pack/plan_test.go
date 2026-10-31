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
	// Imports run as the operator here, as an operator's import does: a
	// crew member's subscriptions are its own or the operator's to
	// change, and the packs subscribe their crew.
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	return st, pack.Options{Depot: "web", Home: t.TempDir()}
}

// linearDecl is the linear source as packs/linear declares it.
const linearDecl = `name: linear
base: https://api.linear.app
credential:
  env: LINEAR_API_KEY
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Linear-Signature, encoding: hex }
    delivery: { header: Linear-Delivery }
  bodies: unique
  normalize: [linear/normalize]
thread:
  reply: [linear/thread, reply]
  posted: [linear/thread, posted]
  read: [linear/thread, read]
  place: team
  open: [linear/thread, open]
  opened: [linear/thread, opened]
  state: [linear/thread, state]
  states: [started, review, done]
link:
  host: linear.app
  issue: '^/([^/]+)/issue/([A-Z][A-Z0-9]*-[0-9]+)'
  fixed: "https://linear.app/{1}/issue/{2}"
  thread: "{2}"
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// withLinear declares the pack's linear source in the yard: a yard with
// a linear hook has applied the pack.
func withLinear(t *testing.T, st store.API) {
	t.Helper()
	if _, err := st.PutSource(ctx, "linear.yaml", linearDecl, "me"); err != nil {
		t.Fatal(err)
	}
}

// withGitHub declares the pack's github source in the yard.
func withGitHub(t *testing.T, st store.API) {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "..", "packs", "github", "github.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "github.yaml", string(text), "me"); err != nil {
		t.Fatal(err)
	}
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
	return applyAs(t, st, p, answers, "me")
}

// seedGroups writes a plan's groups as the operator me, so an import run
// as a crew member below leaves what names what runs as it is and reaches
// the behaviour under test: a group's cmd is the operator's to change, an
// import by a crew member is not.
func seedGroups(t *testing.T, st store.API, p *pack.Plan) {
	t.Helper()
	for _, o := range p.Objects {
		if o.Kind == "group" && o.Group != nil {
			if err := st.PutGroup(ctx, *o.Group, "me"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// applyAs is apply as actor.
func applyAs(t *testing.T, st store.API, p *pack.Plan, answers map[string]string, actor string) *pack.Result {
	t.Helper()
	res, err := pack.Apply(ctx, st, p, pack.ApplyOptions{Answers: answered(t, p, answers), Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// answered answers every question of the plan as apply does.
func answered(t *testing.T, p *pack.Plan, answers map[string]string) pack.Answers {
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
	return a
}

// copyStarter is the starter pack in a directory the test may change.
func copyStarter(t *testing.T) string { return copyPack(t, starter) }

// copyPack is the pack in src in a directory the test may change.
func copyPack(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
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
		"file roles/builder.md", "file roles/review-security.md", "file roles/reviewer.md", "file roles/yardmaster.md", "file scripts/merge-local",
		"file flows/default/backlog.md", "file flows/default/new.md", "file flows/default/review.md", "file flows/default/review-security.md", "file flows/default/clear.md", "file flows/default/approved.md", "file flows/default/decide.md",
		"file flows/train/in-pr.md", "file flows/train/merge.md",
		"group web-builders", "group web-reviewers", "group web-security-reviewers", "group web-assembly", "group backlog", "group decisions",
		"flow starter.default", "flow starter.train",
		"pointer depot web -> starter.default",
		"pointer depot web type train -> starter.train",
		"route depot web stage backlog -> backlog", "route depot web stage new -> web-builders", "route depot web stage review -> web-reviewers",
		"route depot web stage review-security -> web-security-reviewers",
		"route depot web stage clear -> decisions",
		"route depot web stage approved -> web-assembly", "route depot web stage decide -> decisions",
		// No typed copy for a stage both flows share: the plain route
		// matches beads of every type (yardr-3jst).
		"route depot web stage open type train -> web-builders",
		"route depot web stage in-pr type train -> decisions", "route depot web stage land type train -> web-assembly",
		"route depot web stage merge type train -> decisions",
		"crew yardmaster",
		"subscription yardmaster yardr all depot=web stage=decide", "subscription yardmaster yardr all depot=web stage=clear", "subscription yardmaster yardr all depot=web state=done",
		// The pack's linear subscription is skipped: the yard has no
		// source linear, and the next apply sets it once it is there.
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
	// Nothing is asked of the second review's group: it runs the first's agent.
	wantQ := []string{
		"group.web-builders.kind", "group.web-builders.model", "group.web-builders.args",
		"group.web-reviewers.kind", "group.web-reviewers.model", "group.web-reviewers.args",
		"group.backlog.members", "group.decisions.members",
		"crew.yardmaster.crew", "crew.yardmaster.kind", "crew.yardmaster.model", "crew.yardmaster.args", "crew.yardmaster.dir",
	}
	if !reflect.DeepEqual(ids, wantQ) {
		t.Fatalf("questions = %v\nwant %v", ids, wantQ)
	}
	if sec := find(t, plan, "group", "web-security-reviewers"); sec.Binds != "web-reviewers" || len(sec.Asks) != 0 {
		t.Fatalf("web-security-reviewers = %+v, want it bound as web-reviewers and asked nothing", sec)
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
	// The starter's assembly lands a bead by squash: the pack's env is set
	// before the script of the group the import creates, and check says so.
	ra := find(t, plan, "group", "web-assembly")
	if r := ra.Group; r.Runner != "exec" || r.Limit != 1 || r.Config["cmd"] != "YARDR_LAND=squash "+filepath.Join(install, "scripts", "merge-local") || !slices.Equal(ra.Env, []string{"YARDR_LAND=squash"}) {
		t.Fatalf("web-assembly = %+v, env %v", r, ra.Env)
	}
	if o := find(t, plan, "group", "web-builders"); len(o.Env) != 0 || len(o.Keeps) != 0 {
		t.Fatalf("web-builders = %+v: an agent group has no env", o)
	}
	if g := find(t, plan, "group", "backlog").Group; g.Runner != "manual" || g.Limit != 50 {
		t.Fatalf("backlog = %+v, want a people group at the default of people, 50", g)
	}
	// The one program the starter runs, the script of that group, is named
	// in the plan as a path in the pack, as check prints it above the digest.
	if !slices.Equal(plan.Programs, []string{"scripts/merge-local"}) {
		t.Fatalf("programs = %v, want the starter's assembly script", plan.Programs)
	}
	f := find(t, plan, "file", "scripts/merge-local").File
	if f.To != filepath.Join(install, "scripts", "merge-local") || f.Mode != "0755" || !filepath.IsAbs(f.From) {
		t.Fatalf("merge-local = %+v", f)
	}
	if got := find(t, plan, "flow", "starter.default").Flow.Stages; got[0] != "backlog" || got[7] != "merged" {
		t.Fatalf("flow stages = %v", got)
	}
	m := find(t, plan, "crew", "yardmaster").CrewMember
	if m.Prompt != filepath.Join(install, "roles", "yardmaster.md") || !reflect.DeepEqual(m.On, []string{store.KindAttention}) {
		t.Fatalf("yardmaster = %+v", m)
	}
	if len(plan.Governs) != 2 || plan.Governs[0].Flow != "starter.default" || plan.Governs[0].Now != "none" || plan.Governs[0].Type != "" || plan.Governs[1].Flow != "starter.train" || plan.Governs[1].Now != "the depot's flow" || plan.Governs[1].Type != store.TypeTrain {
		t.Fatalf("governs = %+v", plan.Governs)
	}
	if len(plan.Refused) != 0 {
		t.Fatalf("refused: %v", plan.Refused)
	}

	// What linear lacks in a yard that has nothing: the source itself.
	// Once the yard has it, the hook and the rest are still to set up.
	if len(plan.Sources) != 1 || plan.Sources[0].Source != "linear" || plan.Sources[0].CrewMember != "yardmaster" {
		t.Fatalf("sources = %+v", plan.Sources)
	}
	status := map[string]string{}
	for _, n := range plan.Sources[0].Needs {
		status[n.What] = n.Status
	}
	if want := map[string]string{"source": "missing"}; !reflect.DeepEqual(status, want) {
		t.Fatalf("linear needs = %v, want %v", status, want)
	}
	// What is skipped names the pack that would set it.
	if d := plan.Sources[0].Needs[0].Detail; !strings.Contains(d, "the yard has no source linear (yardr pack apply linear brings it); the subscription is skipped") {
		t.Fatalf("subscription skip = %q", d)
	}
	if m := mapPlan(t, plan, "linear"); m.Action != pack.Skipped || !strings.Contains(m.Skipped, "the yard has no source linear (yardr pack apply linear brings it); the next apply sets the lines") {
		t.Fatalf("map skip = %+v", m)
	}
	withLinear(t, st)
	plan = build(t, st, load(t, starter), o)
	status = map[string]string{}
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
	withLinear(t, st)
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), answers)

	plan := build(t, st, load(t, dir), o)
	if c, ch, n := actions(plan, pack.Create), actions(plan, pack.Change), actions(plan, pack.NotInPack); len(c)+len(ch)+len(n) != 0 {
		t.Fatalf("a second import would create %v, change %v, and finds %v not in the pack", c, ch, n)
	}
	if len(plan.Objects) != 40 || len(plan.Questions) != 0 {
		t.Fatalf("%d objects, questions %v", len(plan.Objects), plan.Questions)
	}
	if g := plan.Governs; len(g) != 2 || g[0].Now != "starter.default (store)" || g[1].Now != "starter.train (store, type train)" {
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
	if len(routes) != 11 {
		t.Fatalf("%d routes, want 11", len(routes))
	}

	text, err := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The pack's env is set on a group the import creates and on no other:
	// with another env the group made from the pack stays as it is, the
	// plan says what it keeps, and applying writes nothing.
	envChanged := strings.Replace(string(text), "env: { YARDR_LAND: squash }", "env: { YARDR_LAND: rebase, YARDR_PUSH: origin }", 1)
	if envChanged == string(text) {
		t.Fatal("the starter's assembly env is not what this test replaces")
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(envChanged), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := "YARDR_LAND=squash " + filepath.Join(config.At(o.Home).Pack("starter"), "scripts", "merge-local")
	plan = build(t, st, load(t, dir), o)
	asm := find(t, plan, "group", "web-assembly")
	if want := []string{"cmd " + cmd + ": the pack's env YARDR_LAND=rebase YARDR_PUSH=origin is set on a new group only"}; asm.Action != pack.Unchanged || !slices.Equal(asm.Keeps, want) || len(asm.Env) != 0 || asm.Group.Config["cmd"] != cmd {
		t.Fatalf("web-assembly = %s, keeps %q, env %v, cmd %q, want it unchanged and keeping %q", asm.Action, asm.Keeps, asm.Env, asm.Group.Config["cmd"], want)
	}
	if res := apply(t, st, plan, nil); res.Wrote() {
		t.Fatalf("an import that only keeps wrote: %+v", res)
	}
	if got := groupOf(t, st, "web-assembly").Config["cmd"]; got != cmd {
		t.Fatalf("web-assembly cmd = %q after the import, want %q", got, cmd)
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
	if got := actions(plan, pack.Unchanged); len(got) != 40 {
		t.Fatalf("after applying the change: %d unchanged of %d", len(got), len(plan.Objects))
	}
}

// A yard set up by hand, as the one that builds yardr was: its groups keep
// what the pack does not carry, its routes are recognized by depot and stage,
// and nothing it has is planned as new.
func TestYardSetUpByHand(t *testing.T) {
	st, o := yard(t)
	withLinear(t, st)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutGroup(ctx, store.Group{Name: "web-builders", Runner: "herdr", Limit: 5,
		Config: map[string]any{"kind": "claude", "model": "opus", "idle": "5m", "args": []any{"--permission-mode", "auto"}, "prompt": "roles/builder.md"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "web-reviewers", Runner: "herdr", Limit: 3, Config: map[string]any{"kind": "codex", "prompt": "roles/reviewer.md"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "web-security-reviewers", Runner: "herdr", Limit: 2, Config: map[string]any{"kind": "claude", "prompt": "roles/review-security.md"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "web-assembly", Runner: "exec", Limit: 1,
		Config: map[string]any{"cmd": "YARDR_PUSH=origin YARDR_NOTIFY=boss /old/scripts/merge-local", "log": "/logs/landing.log"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "web-mergers", Runner: "herdr", Limit: 1, Config: map[string]any{}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "backlog", Runner: "manual", Limit: 50, Members: []string{"ann", "boss"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 50, Members: []string{"ann"}}, "me"))
	depot := "web"
	for stage, group := range map[string]string{"backlog": "backlog", "new": "web-builders", "review": "web-reviewers", "review-security": "web-security-reviewers", "clear": "decisions", "approved": "web-assembly", "decide": "decisions", "merge": "web-mergers"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Depot: &depot, Group: group}, "me")
		must(err)
	}
	train := store.TypeTrain
	for stage, group := range map[string]string{"backlog": "backlog", "open": "web-builders", "review": "web-reviewers", "review-security": "web-security-reviewers", "approved": "web-assembly", "in-pr": "decisions", "land": "web-assembly", "merge": "decisions", "decide": "decisions"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Type: &train, Depot: &depot, Group: group}, "me")
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

	for _, o := range plan.Objects {
		if o.Action == pack.Create && (o.Kind == "group" || o.Kind == "route" || o.Kind == "crew") {
			t.Errorf("%s %s is planned as new", o.Kind, o.Name)
		}
	}
	if got, want := actions(plan, pack.Change), []string{"crew boss", "subscription boss linear involved"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("change = %v\nwant %v", got, want)
	}
	// The hand-made typed copies for stages both flows share are left
	// alone: the plain route covers them (yardr-3jst). The routes were
	// added from maps, so their order is the store's, not the test's.
	if got, want := slices.Sorted(slices.Values(actions(plan, pack.NotInPack))), slices.Sorted(slices.Values([]string{"group web-mergers", "route depot web stage backlog type train originator any -> backlog", "route depot web stage review type train -> web-reviewers", "route depot web stage review-security type train -> web-security-reviewers", "route depot web stage approved type train originator any -> web-assembly", "route depot web stage decide type train originator any -> decisions", "route depot web stage merge -> web-mergers", "subscription boss yardr all label=ui"})); !reflect.DeepEqual(got, want) {
		t.Fatalf("not in the pack = %v\nwant %v", got, want)
	}
	b := find(t, plan, "group", "web-builders")
	if b.Action != pack.Unchanged || len(b.Changes) != 0 || b.Group.Config["prompt"] != "roles/builder.md" {
		t.Fatalf("web-builders = %s %v %v, want the hand's prompt kept", b.Action, b.Changes, b.Group.Config["prompt"])
	}
	if joined := strings.Join(b.Keeps, "\n"); !strings.Contains(joined, "roles/builder.md") || !strings.Contains(joined, "by hand") {
		t.Fatalf("web-builders keeps %q", b.Keeps)
	}
	if c := b.Group.Config; c["kind"] != "claude" || c["model"] != "opus" || c["idle"] != "5m" || !reflect.DeepEqual(c["args"], []any{"--permission-mode", "auto"}) {
		t.Fatalf("web-builders would lose what the yard chose: %v", c)
	}
	r := find(t, plan, "group", "web-assembly")
	if r.Action != pack.Unchanged || len(r.Changes) != 0 || r.Group.Config["cmd"] != "YARDR_PUSH=origin YARDR_NOTIFY=boss /old/scripts/merge-local" {
		t.Fatalf("web-assembly = %s %v %v, want the hand's cmd kept", r.Action, r.Changes, r.Group.Config["cmd"])
	}
	if r.Group.Config["log"] != "/logs/landing.log" {
		t.Fatalf("web-assembly = %+v", r.Group)
	}
	// The pack's script is not set on a group the yard has, said first, and
	// its env is for a group the import creates, said as before.
	want := []string{
		"cmd YARDR_PUSH=origin YARDR_NOTIFY=boss /old/scripts/merge-local: set by hand, and the pack's script scripts/merge-local is not set on it",
		"cmd YARDR_PUSH=origin YARDR_NOTIFY=boss /old/scripts/merge-local: the pack's env YARDR_LAND=squash is set on a new group only",
	}
	if !slices.Equal(r.Keeps, want) || len(r.Env) != 0 {
		t.Fatalf("web-assembly keeps %q, env %v, want %q", r.Keeps, r.Env, want)
	}
	// People groups: the pack names no limit and no members, so both stay.
	if g := find(t, plan, "group", "backlog"); g.Action != pack.Unchanged || g.Group.Limit != 50 || !reflect.DeepEqual(g.Group.Members, []string{"ann", "boss"}) {
		t.Fatalf("backlog = %+v %+v", g, g.Group)
	}
	if rt := find(t, plan, "route", "depot web stage new -> web-builders"); rt.Action != pack.Unchanged || len(rt.Route.Delete) != 0 {
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
	if boss.Dir != "/home/boss" || boss.MaxAge != "6h" || boss.Config["env_file"] != "/nowhere.env" || len(boss.Subscriptions) != 5 {
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
		`"train": starter.train, now type/train (repo (directory), type train)`,
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
	extra, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: &depot, Group: "others"}, "me")
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
	if n != 1 || len(routes) != 11 {
		t.Fatalf("%d routes for new, %d in all; want 1 and 11", n, len(routes))
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
	if got, want := actions(plan, pack.NotInPack), []string{}; !reflect.DeepEqual(got, want) {
		t.Fatalf("not in the pack = %v", got)
	}
	if g := plan.Governs; len(g) != 2 || g[1].Type != "train" || g[1].Flow != "starter.train" || g[1].Now != "trains (store, type train)" || !g[1].InPack {
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
// stage by stage. A limit on rounds is part of its stage's line, so one
// added, one changed once the yard has it and one dropped each show, and the
// same limit again shows nothing.
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

	// The train's review, where the starter sets no limit: its changes go
	// back to open, and with a limit go to decide after max rounds.
	review := func(changes string) pack.Object {
		d := copyStarter(t)
		edit(t, d, "outcomes: { done: review-security, changes: open, question: decide }", "outcomes: { done: review-security, changes: "+changes+", question: decide }")
		plan := build(t, st, load(t, d), o)
		return find(t, plan, "flow", "starter.train")
	}
	line := func(limit string) string {
		return "-> review-security on done | open on changes" + limit + " | decide on question"
	}
	limited := func(rounds int) string { return fmt.Sprintf("{ to: open, max: %d, then: decide }", rounds) }
	added := review(limited(3))
	if want := []pack.Diff{{Field: "stage review", Old: line(""), New: line(" max 3 then decide")}}; added.Action != pack.Change || !reflect.DeepEqual(added.Changes, want) {
		t.Fatalf("a limit added: train = %s %v, want %v", added.Action, added.Changes, want)
	}
	if _, err := st.PutFlow(ctx, *added.Flow, "me"); err != nil {
		t.Fatal(err)
	}
	if same := review(limited(3)); same.Action != pack.Unchanged || len(same.Changes) != 0 {
		t.Fatalf("the same limit again: train = %s %v", same.Action, same.Changes)
	}
	lowered, dropped := review(limited(2)), review("open")
	if want := []pack.Diff{{Field: "stage review", Old: line(" max 3 then decide"), New: line(" max 2 then decide")}}; !reflect.DeepEqual(lowered.Changes, want) {
		t.Fatalf("a limit changed: train = %v, want %v", lowered.Changes, want)
	}
	if want := []pack.Diff{{Field: "stage review", Old: line(" max 3 then decide"), New: line("")}}; !reflect.DeepEqual(dropped.Changes, want) {
		t.Fatalf("a limit dropped: train = %v, want %v", dropped.Changes, want)
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
	flows, _ := st.ListFlows(ctx)
	entries, _ := os.ReadDir(o.Home)
	if after != head || len(groups) != 0 || len(flows) != 0 || len(entries) != 0 {
		t.Fatalf("events %d -> %d, groups %v, flows %v, home %v", head, after, groups, flows, entries)
	}
}

// sharedYard is a yard whose groups every depot's routes use, as the one
// that builds yardr has them, with a second depot to import into.
func sharedYard(t *testing.T) (store.API, pack.Options) {
	t.Helper()
	st, o := yard(t)
	for _, g := range []store.Group{
		{Name: "yardr-builders", Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "claude", "model": "opus", "args": []any{"--permission-mode", "auto"}, "prompt": "/roles/builder.md"}},
		{Name: "yardr-reviewers", Runner: "herdr", Limit: 3, Config: map[string]any{"kind": "codex", "prompt": filepath.Join(config.At(o.Home).Pack("starter"), "roles", "reviewer.md")}},
		{Name: "yardr-refinery", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "/scripts/land"}},
		{Name: "triage", Runner: "manual", Limit: 50, Members: []string{"ann"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return st, o
}

// --group has a group the yard has take the pack's: the plan takes it
// instead of creating the depot's own, the routes lead to it, a role or
// limit that is not the pack's is a line and no change, and applying
// changes nothing of the group. The next import takes it again unasked, and
// no prune removes what was never the pack's.
func TestGroupOfTheYardTakesThePacks(t *testing.T) {
	st, o := sharedYard(t)
	before, _ := st.Groups(ctx)
	o.Groups = map[string]string{"builders": "yardr-builders", "reviewers": "yardr-reviewers", "assembly": "yardr-refinery", "backlog": "triage"}
	plan := build(t, st, load(t, starter), o)

	if got, want := actions(plan, pack.Takes), []string{"group yardr-builders", "group yardr-reviewers", "group yardr-refinery", "group triage"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("takes = %v, want %v", got, want)
	}
	// The second review's group is the depot's own, and runs the agent of
	// the group that takes the pack's reviewers.
	for _, name := range actions(plan, pack.Create) {
		if strings.HasPrefix(name, "group ") && name != "group decisions" && name != "group web-security-reviewers" {
			t.Errorf("the plan creates %s though a group of the yard takes it", name)
		}
	}
	if sec := find(t, plan, "group", "web-security-reviewers"); sec.Binds != "yardr-reviewers" || len(sec.Asks) != 0 || sec.Group.Config["kind"] != "codex" {
		t.Fatalf("web-security-reviewers = %+v %+v, want it bound as yardr-reviewers, which runs codex", sec, sec.Group)
	}
	if !reflect.DeepEqual(plan.Groups, map[string]string{"builders": "yardr-builders", "reviewers": "yardr-reviewers", "security-reviewers": "web-security-reviewers", "assembly": "yardr-refinery", "backlog": "triage", "decisions": "decisions"}) {
		t.Fatalf("groups = %v", plan.Groups)
	}
	for _, name := range []string{"route depot web stage new -> yardr-builders", "route depot web stage review -> yardr-reviewers",
		"route depot web stage approved -> yardr-refinery", "route depot web stage backlog -> triage"} {
		if !slices.Contains(actions(plan, pack.Create), name) {
			t.Errorf("the plan lacks %s; it creates %v", name, actions(plan, pack.Create))
		}
	}
	// The pack's builders have another role and limit 5; its reviewers the
	// role the group has, and its limit.
	b := find(t, plan, "group", "yardr-builders")
	if len(b.Keeps) != 2 || !strings.Contains(b.Keeps[0], "prompt /roles/builder.md: the group is the yard's, and the pack's role roles/builder.md is not set on it") ||
		!strings.Contains(b.Keeps[1], "limit 4: the group is the yard's, and the pack's limit 5 is not set on it") || len(b.Changes) != 0 {
		t.Fatalf("yardr-builders keeps %v, changes %v", b.Keeps, b.Changes)
	}
	if r := find(t, plan, "group", "yardr-reviewers"); len(r.Keeps)+len(r.Changes) != 0 {
		t.Fatalf("yardr-reviewers keeps %v, changes %v", r.Keeps, r.Changes)
	}
	// And the pack's env, which only a group the import creates gets.
	if a := find(t, plan, "group", "yardr-refinery"); len(a.Keeps) != 2 || !strings.Contains(a.Keeps[0], "cmd /scripts/land: the group is the yard's, and the pack's script scripts/merge-local") ||
		a.Keeps[1] != "cmd /scripts/land: the pack's env YARDR_LAND=squash is set on a new group only" || len(a.Env) != 0 {
		t.Fatalf("yardr-refinery keeps %v, env %v", a.Keeps, a.Env)
	}
	// Nothing is asked of a group that is taken: its agent is the yard's.
	for _, q := range plan.Questions {
		if strings.HasPrefix(q.Object, "group ") && q.Object != "group decisions" {
			t.Errorf("the plan asks %s", q.ID)
		}
	}

	apply(t, st, plan, nil)
	after, _ := st.Groups(ctx)
	for _, g := range before {
		if i := slices.IndexFunc(after, func(h store.Group) bool { return h.Name == g.Name }); i < 0 || !reflect.DeepEqual(after[i], g) {
			t.Errorf("group %s changed: %+v, was %+v", g.Name, after, g)
		}
	}
	if len(after) != len(before)+2 {
		t.Fatalf("groups after the import: %d, want the yard's %d, decisions and web-security-reviewers", len(after), len(before))
	}
	if got := groupOf(t, st, "web-security-reviewers"); got.Config["kind"] != "codex" {
		t.Fatalf("web-security-reviewers = %+v, want the agent of yardr-reviewers", got)
	}
	recs, _ := st.Packs(ctx)
	if len(recs) != 1 || recs[0].Groups["builders"] != "yardr-builders" {
		t.Fatalf("records = %+v", recs)
	}
	for _, po := range recs[0].Objects {
		if po.Kind == "group" && po.Name != "decisions" && po.Name != "web-security-reviewers" {
			t.Errorf("the record has group %s as the pack's", po.Name)
		}
	}

	// As at the last import, with no flag.
	o.Groups = nil
	plan = build(t, st, load(t, starter), o)
	if got := actions(plan, pack.Takes); len(got) != 4 {
		t.Fatalf("the next import takes %v", got)
	}
	if c, ch, d := actions(plan, pack.Create), actions(plan, pack.Change), actions(plan, pack.Dropped); len(c)+len(ch)+len(d) != 0 {
		t.Fatalf("the next import would create %v, change %v, drop %v", c, ch, d)
	}

	// The import's own name for the group, where the yard has none of it,
	// says none takes it any more: the depot gets its own, and the yard's
	// is neither dropped nor pruned.
	o.Groups = map[string]string{"builders": "web-builders"}
	plan = build(t, st, load(t, starter), o)
	if got := find(t, plan, "group", "web-builders"); got.Action != pack.Create {
		t.Fatalf("web-builders = %s", got.Action)
	}
	if d := actions(plan, pack.Dropped); slices.Contains(d, "group yardr-builders") {
		t.Fatalf("dropped = %v", d)
	}
	if got := find(t, plan, "route", "depot web stage new -> web-builders"); got.Action != pack.Change {
		t.Fatalf("the route to the builders = %s", got.Action)
	}
}

// A group the flag names is the yard's whatever its name: where the yard
// has a group under the name the import would give the pack's own,
// --group takes it as it is, and applying changes nothing of it. Without
// the flag the import sets the pack's role or script on it, as on any
// group of that name.
func TestGroupFlagTakesTheYardsGroupOfThePacksOwnName(t *testing.T) {
	ownNamed := func(t *testing.T) (store.API, pack.Options) {
		t.Helper()
		st, o := yard(t)
		for _, g := range []store.Group{
			{Name: "web-reviewers", Runner: "herdr", Limit: 3, Config: map[string]any{"kind": "codex", "prompt": "/roles/reviewer.md"}},
			{Name: "web-assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "/scripts/land"}},
		} {
			if err := st.PutGroup(ctx, g, "me"); err != nil {
				t.Fatal(err)
			}
		}
		return st, o
	}

	st, o := ownNamed(t)
	before, _ := st.Groups(ctx)
	o.Groups = map[string]string{"reviewers": "web-reviewers", "assembly": "web-assembly"}
	plan := build(t, st, load(t, starter), o)

	if got, want := actions(plan, pack.Takes), []string{"group web-reviewers", "group web-assembly"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("takes = %v, want %v", got, want)
	}
	if got := plan.Groups["reviewers"]; got != "web-reviewers" {
		t.Fatalf("reviewers -> %s", got)
	}
	for _, name := range []string{"web-reviewers", "web-assembly"} {
		obj := find(t, plan, "group", name)
		if len(obj.Changes) != 0 {
			t.Errorf("%s changes %v though the flag names the yard's group", name, obj.Changes)
		}
		if len(obj.Keeps) == 0 || !strings.Contains(strings.Join(obj.Keeps, "\n"), "the group is the yard's") {
			t.Errorf("%s keeps %v", name, obj.Keeps)
		}
	}
	for _, name := range []string{"route depot web stage review -> web-reviewers", "route depot web stage approved -> web-assembly"} {
		if !slices.Contains(actions(plan, pack.Create), name) {
			t.Errorf("the plan lacks %s; it creates %v", name, actions(plan, pack.Create))
		}
	}

	apply(t, st, plan, nil)
	after, _ := st.Groups(ctx)
	for _, g := range before {
		if i := slices.IndexFunc(after, func(h store.Group) bool { return h.Name == g.Name }); i < 0 || !reflect.DeepEqual(after[i], g) {
			t.Errorf("group %s changed: %+v, was %+v", g.Name, after, g)
		}
	}
	recs, _ := st.Packs(ctx)
	if len(recs) != 1 || recs[0].Groups["reviewers"] != "web-reviewers" || recs[0].Groups["assembly"] != "web-assembly" {
		t.Fatalf("records = %+v", recs)
	}
	for _, po := range recs[0].Objects {
		if po.Kind == "group" && (po.Name == "web-reviewers" || po.Name == "web-assembly") {
			t.Errorf("the record has group %s as the pack's", po.Name)
		}
	}

	// The same without the flag keeps the yard's groups, said under Keeps:
	// a group the pack did not make is never repointed to the pack's.
	st, o = ownNamed(t)
	plan = build(t, st, load(t, starter), o)
	r := find(t, plan, "group", "web-reviewers")
	if r.Action != pack.Unchanged || slices.ContainsFunc(r.Changes, func(d pack.Diff) bool { return d.Field == "prompt" }) {
		t.Fatalf("web-reviewers = %s %v", r.Action, r.Changes)
	}
	if joined := strings.Join(r.Keeps, "\n"); !strings.Contains(joined, "/roles/reviewer.md") || !strings.Contains(joined, "by hand") {
		t.Fatalf("web-reviewers keeps %q", r.Keeps)
	}
	a := find(t, plan, "group", "web-assembly")
	if a.Action != pack.Unchanged || slices.ContainsFunc(a.Changes, func(d pack.Diff) bool { return d.Field == "cmd" }) {
		t.Fatalf("web-assembly = %s %v", a.Action, a.Changes)
	}
	if joined := strings.Join(a.Keeps, "\n"); !strings.Contains(joined, "/scripts/land") || !strings.Contains(joined, "by hand") {
		t.Fatalf("web-assembly keeps %q", a.Keeps)
	}
	apply(t, st, plan, nil)
	got, _ := st.GetGroup(ctx, "web-reviewers")
	if prompt, _ := got.Config["prompt"].(string); prompt != "/roles/reviewer.md" {
		t.Errorf("web-reviewers prompt = %q, want the yard's kept", prompt)
	}
	got, _ = st.GetGroup(ctx, "web-assembly")
	if cmd, _ := got.Config["cmd"].(string); cmd != "/scripts/land" {
		t.Errorf("web-assembly cmd = %q, want the yard's kept", cmd)
	}
}

// A new group is asked about when the yard has one of its kind that could
// take it: agents for an agent group, people for a people group, and never
// for a script group. --group answers, and what it names must be a group of
// the yard of that kind.
func TestImportAsksWhichGroupTakes(t *testing.T) {
	st, o := sharedYard(t)
	plan := build(t, st, load(t, starter), o)
	asks := map[string]pack.Question{}
	for _, q := range plan.Questions {
		if q.Field == "group" {
			asks[q.ID] = q
		}
	}
	if len(asks) != 4 {
		t.Fatalf("asks which group takes %v, want the two agent and the two people groups", asks)
	}
	q := asks["group.web-builders.group"]
	if q.Ask != "which group of the yard takes the pack's builders (--group builders=<name>)? The yard has: yardr-builders, yardr-reviewers." || q.Default != "a new group web-builders" {
		t.Fatalf("question = %+v", q)
	}
	if q := asks["group.backlog.group"]; !strings.HasSuffix(q.Ask, "The yard has: triage.") {
		t.Fatalf("question = %+v", q)
	}
	// Beside the bind, and first.
	if b := find(t, plan, "group", "web-builders"); !reflect.DeepEqual(b.Asks, []string{"group.web-builders.group", "group.web-builders.kind", "group.web-builders.model", "group.web-builders.args"}) {
		t.Fatalf("web-builders asks %v", b.Asks)
	}

	// Told, either way, it is not asked.
	o.Groups = map[string]string{"builders": "yardr-builders", "reviewers": "web-reviewers"}
	plan = build(t, st, load(t, starter), o)
	for _, q := range plan.Questions {
		if q.ID == "group.web-builders.group" || q.ID == "group.web-reviewers.group" {
			t.Errorf("asks %s though --group said", q.ID)
		}
	}
	if r := find(t, plan, "group", "web-reviewers"); r.Action != pack.Create {
		t.Fatalf("web-reviewers = %s", r.Action)
	}

	for flag, want := range map[string]string{
		"coders=yardr-builders":   "the pack has no group coders",
		"builders=nope":           "the yard has no group nope (it has, to take an agent group: yardr-builders, yardr-reviewers)",
		"builders=triage":         "the pack's builders is an agent group, and triage is a people group",
		"backlog=yardr-builders":  "the pack's backlog is a people group, and yardr-builders is an agent group",
		"assembly=yardr-builders": "the pack's assembly is a script group, and yardr-builders is an agent group",
	} {
		from, to, _ := strings.Cut(flag, "=")
		o.Groups = map[string]string{from: to}
		if _, err := pack.Build(ctx, st, load(t, starter), o); err == nil || !strings.Contains(err.Error(), "--group "+flag+": "+want) {
			t.Errorf("--group %s: err = %v, want %q", flag, err, want)
		}
	}

	// A yard with no group of the kind has nothing to offer.
	st, o = yard(t)
	for _, q := range build(t, st, load(t, starter), o).Questions {
		if q.Field == "group" {
			t.Errorf("an empty yard is asked %s", q.ID)
		}
	}
}

// A group that took the pack's at the last import and is another kind of
// group since takes it no more: the import would have to change it.
func TestTakenGroupThatNoLongerFits(t *testing.T) {
	st, o := sharedYard(t)
	o.Groups = map[string]string{"backlog": "triage"}
	apply(t, st, build(t, st, load(t, starter), o), nil)
	if err := st.PutGroup(ctx, store.Group{Name: "triage", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "/bin/true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	o.Groups = nil
	plan := build(t, st, load(t, starter), o)
	if plan.Groups["backlog"] != "backlog" || find(t, plan, "group", "backlog").Action != pack.Create {
		t.Fatalf("backlog -> %s", plan.Groups["backlog"])
	}
	if got := actions(plan, pack.Takes); len(got) != 0 {
		t.Fatalf("takes %v", got)
	}
}

// The plan names a source the yard has nothing of once, as optional, with
// the command that sets it up: the pack apply that brings a source the
// yard lacks, or the hook add that receives one it has in Go. A source the
// yard has, even without a hook, is not among them: its needs read as
// missing.
func TestPlanSaysUnusedSourcesOnceAsOptional(t *testing.T) {
	st, o := yard(t)
	plan := build(t, st, load(t, starter), o)
	optional := map[string]pack.OptionalSource{}
	for _, s := range plan.Optional {
		optional[s.Source] = s
	}
	if len(optional) != 2 {
		t.Fatalf("optional = %+v, want linear and github", plan.Optional)
	}
	if s := optional["linear"]; s.Setup != "yardr pack apply linear" ||
		!strings.Contains(s.Detail, "the yard has no source linear") {
		t.Errorf("linear = %+v", s)
	}
	if s := optional["github"]; s.Setup != "yardr pack apply github" ||
		!strings.Contains(s.Detail, "the yard has no source github") {
		t.Errorf("github = %+v", s)
	}
	// The steps still carry their needs as data; the text says them once.
	if len(plan.Sources) != 1 || len(plan.Maps) != 2 {
		t.Fatalf("sources = %+v, maps = %+v", plan.Sources, plan.Maps)
	}
	withLinear(t, st)
	plan = build(t, st, load(t, starter), o)
	for _, s := range plan.Optional {
		if s.Source == "linear" {
			t.Errorf("a source the yard has is optional: %+v", plan.Optional)
		}
	}
}

// A route by bead priority, or by labels, says something no pack does: the
// pack leaves it where it is, as it leaves a route with one label, and
// names it with what it asks for.
func TestRoutesByBeadPriorityOrLabelsAreLeftAlone(t *testing.T) {
	st, o := yard(t)
	if err := st.PutGroup(ctx, store.Group{Name: "old", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	depot, urgent, hard := "web", 1, "difficulty:3"
	var kept []string
	for _, r := range []store.Route{
		{Stage: "triage", Depot: &depot, Group: "old", BeadPriority: &urgent},
		{Stage: "triage", Depot: &depot, Group: "old", Label: &hard, AndLabels: []string{"size:large"}},
	} {
		saved, err := st.AddRoute(ctx, r, "me")
		if err != nil {
			t.Fatal(err)
		}
		kept = append(kept, saved.ID)
	}
	dir := t.TempDir()
	text := "name: hand\nflows:\n  default:\n    stages:\n      triage: { manual: [done] }\n      done: { end: true }\ngroups: {}\n"
	if err := os.WriteFile(filepath.Join(dir, pack.File), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, dir), o)
	got := actions(plan, pack.NotInPack)
	for _, want := range []string{
		"route depot web stage triage beads P0-P1 originator any -> old",
		"route depot web stage triage label difficulty:3, size:large originator any -> old",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("left alone = %v, want %q among them", got, want)
		}
	}
	apply(t, st, plan, nil)
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, r := range routes {
		have = append(have, r.ID)
	}
	for _, id := range kept {
		if !slices.Contains(have, id) {
			t.Errorf("route %s went with the pack's import: %v", id, have)
		}
	}
}

// A pack is text from outside the yard, and a stage's name is where its
// stage file is written under the home. A name may hold a slash (a store
// flow's may), never a segment that leads out of the flow's directory: such
// a pack is refused when read, and no plan is built from it.
func TestStageNameCannotChooseWhereItsFileIsWritten(t *testing.T) {
	for _, name := range []string{"a/../../../../escaped", "a/../review", "a/./b", "a//b", "a/"} {
		yaml := strings.Replace(small, "review: { group: assembly,", `"`+name+`": { group: assembly, file: flows/x.md,`, 1)
		yaml = strings.Replace(yaml, "done: review,", `done: "`+name+`",`, 1)
		files := smallFiles(yaml)
		files["flows/x.md"] = "written where the pack says\n"
		_, err := pack.LoadDir(writePack(t, files))
		if err == nil || !strings.Contains(err.Error(), strconv.Quote(name)) {
			t.Errorf("stage %q: err = %v, want the name refused", name, err)
		}
	}
	// A name with a slash and no such segment is a pack's to use, and its
	// file stays under the flow's directory.
	yaml := strings.Replace(small, "review: { group: assembly,", `nested/review: { group: assembly, file: flows/x.md,`, 1)
	yaml = strings.Replace(yaml, "done: review,", `done: nested/review,`, 1)
	files := smallFiles(yaml)
	files["flows/x.md"] = "review it\n"
	st, o := yard(t)
	plan := build(t, st, load(t, writePack(t, files)), o)
	dir := store.HomeStageDir(o.Home, pack.FlowName("small", "default"))
	if got, want := find(t, plan, "file", "flows/x.md").File.To, filepath.Join(dir, "nested", "review.md"); got != want {
		t.Errorf("stage file goes to %s, want %s", got, want)
	}
}

// A second pack over the shared names keeps the first pack's groups: after
// starter, planning design leaves web-builders, web-reviewers and
// web-assembly on starter's prompt files and cmd, said under Keeps, and
// applying changes nothing of them. Re-planning design (its groups now, by
// the record) still keeps, while its own file updates stand; re-planning
// starter (the pack that made them) is unchanged with no prompt keep; and a
// hand-changed prompt is kept, said under Keeps.
func TestSecondPackKeepsTheFirstPacksGroups(t *testing.T) {
	st, o := yard(t)
	apply(t, st, build(t, st, load(t, starter), o), nil)
	bPrompt := groupOf(t, st, "web-builders").Config["prompt"].(string)
	rPrompt := groupOf(t, st, "web-reviewers").Config["prompt"].(string)
	aCmd := groupOf(t, st, "web-assembly").Config["cmd"].(string)
	if !strings.Contains(bPrompt, filepath.Join("packs", "starter")) || !strings.Contains(rPrompt, filepath.Join("packs", "starter")) || !strings.Contains(aCmd, filepath.Join("packs", "starter")) {
		t.Fatalf("starter's groups = %q %q %q, want starter's paths", bPrompt, rPrompt, aCmd)
	}

	kept := func(plan *pack.Plan) {
		t.Helper()
		for _, tc := range []struct{ name, want string }{{"web-builders", bPrompt}, {"web-reviewers", rPrompt}} {
			obj := find(t, plan, "group", tc.name)
			if got := obj.Group.Config["prompt"]; got != tc.want {
				t.Errorf("%s prompt = %v, want %v (starter's)", tc.name, got, tc.want)
			}
			if slices.ContainsFunc(obj.Changes, func(d pack.Diff) bool { return d.Field == "prompt" }) {
				t.Errorf("%s changes %v, want no prompt change", tc.name, obj.Changes)
			}
			joined := strings.Join(obj.Keeps, "\n")
			if !strings.Contains(joined, tc.want) || !strings.Contains(joined, "made by pack starter") {
				t.Errorf("%s keeps %q, want starter's prompt named as made by pack starter", tc.name, obj.Keeps)
			}
		}
		obj := find(t, plan, "group", "web-assembly")
		if got := obj.Group.Config["cmd"]; got != aCmd {
			t.Errorf("web-assembly cmd = %v, want %v (starter's)", got, aCmd)
		}
		if slices.ContainsFunc(obj.Changes, func(d pack.Diff) bool { return d.Field == "cmd" }) {
			t.Errorf("web-assembly changes %v, want no cmd change", obj.Changes)
		}
		joined := strings.Join(obj.Keeps, "\n")
		if !strings.Contains(joined, aCmd) || !strings.Contains(joined, "made by pack starter") {
			t.Errorf("web-assembly keeps %q, want starter's cmd named as made by pack starter", obj.Keeps)
		}
	}

	plan := build(t, st, load(t, design), o)
	kept(plan)
	apply(t, st, plan, nil)
	if got := groupOf(t, st, "web-builders").Config["prompt"]; got != bPrompt {
		t.Errorf("after applying design, web-builders prompt = %v, want %v", got, bPrompt)
	}
	if got := groupOf(t, st, "web-assembly").Config["cmd"]; got != aCmd {
		t.Errorf("after applying design, web-assembly cmd = %v, want %v", got, aCmd)
	}
	kept(build(t, st, load(t, design), o))

	// The pack that made them, re-planned, is unchanged with no prompt keep.
	planS := build(t, st, load(t, starter), o)
	for _, name := range []string{"web-builders", "web-reviewers", "web-assembly"} {
		obj := find(t, planS, "group", name)
		if obj.Action != pack.Unchanged {
			t.Errorf("starter re-plan: %s = %s, want unchanged", name, obj.Action)
		}
		for _, k := range obj.Keeps {
			if strings.HasPrefix(k, "prompt ") || strings.HasPrefix(k, "cmd ") {
				t.Errorf("starter re-plan: %s keeps %q, want no prompt or cmd keep", name, k)
			}
		}
	}

	// A hand-changed prompt is kept, said under Keeps.
	st2, o2 := yard(t)
	apply(t, st2, build(t, st2, load(t, starter), o2), nil)
	g := groupOf(t, st2, "web-builders")
	g.Config["prompt"] = "/hand.md"
	if err := st2.PutGroup(ctx, g, "me"); err != nil {
		t.Fatal(err)
	}
	obj := find(t, build(t, st2, load(t, starter), o2), "group", "web-builders")
	if got := obj.Group.Config["prompt"]; got != "/hand.md" {
		t.Errorf("hand-changed prompt = %v, want /hand.md", got)
	}
	if joined := strings.Join(obj.Keeps, "\n"); !strings.Contains(joined, "/hand.md") || !strings.Contains(joined, "by hand") {
		t.Errorf("hand-changed keeps %q, want /hand.md named as by hand", obj.Keeps)
	}

	// The second pack's own file updates stand while its groups keep.
	dir := copyPack(t, design)
	role := filepath.Join(dir, "roles", "builder.md")
	text, err := os.ReadFile(role)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(role, append(text, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	planD := build(t, st, load(t, dir), o)
	if got := find(t, planD, "file", "roles/builder.md").Action; got != pack.Change {
		t.Errorf("design's edited role = %s, want change", got)
	}
	kept(planD)
}

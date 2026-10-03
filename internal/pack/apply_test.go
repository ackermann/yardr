package pack_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// kinds are the kinds of the events logged after seq.
func kinds(t *testing.T, st store.API, after uint64) []string {
	t.Helper()
	evs, err := st.EventsSince(ctx, after, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func head(t *testing.T, st store.API) uint64 {
	t.Helper()
	h, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// edit rewrites the copied pack's pack.yaml, each old text by its new.
func edit(t *testing.T, dir string, pairs ...string) {
	t.Helper()
	path := filepath.Join(dir, "pack.yaml")
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(text)
	for i := 0; i < len(pairs); i += 2 {
		if !strings.Contains(s, pairs[i]) {
			t.Fatalf("pack.yaml lacks %q", pairs[i])
		}
		s = strings.Replace(s, pairs[i], pairs[i+1], 1)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The starter pack into an empty yard makes everything it names, with the
// bindings answered, and records the import. Applied again it changes
// nothing: no object is written, the record stays, and the log gains the
// one event saying so.
func TestApplyToAnEmptyYardThenAgain(t *testing.T) {
	st, o := yard(t)
	o.Residents = map[string]string{"mayor": "boss"}
	res := apply(t, st, build(t, st, load(t, starter), o), answers)
	if res.Created != 20 || res.Changed != 0 || res.Unchanged != 0 || !res.Recorded || !res.Wrote() {
		t.Fatalf("first import = %+v", res)
	}
	g, err := st.GetGroup(ctx, "web-builders")
	if err != nil {
		t.Fatal(err)
	}
	if g.Runner != "herdr" || g.Limit != 5 || g.Config["kind"] != "claude" || g.Config["model"] != "opus" ||
		g.Config["prompt"] != filepath.Join(o.Home, "packs", "starter", "roles", "builder.md") {
		t.Fatalf("web-builders = %+v", g)
	}
	if g, _ := st.GetGroup(ctx, "backlog"); g == nil || !slices.Equal(g.Members, []string{"ann", "mayor"}) {
		t.Fatalf("backlog = %+v", g)
	}
	if info, err := os.Stat(filepath.Join(o.Home, "packs", "starter", "scripts", "merge-local")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("merge-local: %v %v", info, err)
	}
	boss, err := st.GetResident(ctx, "boss")
	if err != nil || len(boss.On) != 12 || len(boss.Subscriptions) != 3 || boss.Config["kind"] != "claude" || boss.Dir == "" {
		t.Fatalf("boss = %+v, %v", boss, err)
	}
	if _, err := st.GetResident(ctx, "mayor"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a resident mayor exists beside boss: %v", err)
	}

	recs, err := st.Packs(ctx)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	rec := recs[0]
	abs, _ := filepath.Abs(starter)
	if rec.Pack != "starter" || rec.Rig != "web" || rec.Source != abs || rec.By != "me" || rec.At.IsZero() || rec.Flow != "default" ||
		rec.Groups["builders"] != "web-builders" || rec.Groups["backlog"] != "backlog" || !reflect.DeepEqual(rec.Residents, map[string]string{"mayor": "boss"}) {
		t.Fatalf("record = %+v", rec)
	}
	// The starter pack is in this repository, so its commit is known.
	if len(rec.Commit) != 40 {
		t.Fatalf("commit = %q", rec.Commit)
	}
	// Everything but the resident, which stays the yard's.
	if len(rec.Objects) != 19 {
		t.Fatalf("%d objects recorded: %+v", len(rec.Objects), rec.Objects)
	}

	// Again, with no flag: the record says who took mayor.
	before := head(t, st)
	o.Residents = nil
	plan := build(t, st, load(t, starter), o)
	if len(plan.Questions) != 0 || len(actions(plan, pack.Unchanged)) != 20 {
		t.Fatalf("second plan: questions %v, unchanged %v", plan.Questions, actions(plan, pack.Unchanged))
	}
	res = apply(t, st, plan, nil)
	if res.Wrote() || res.Recorded || res.Unchanged != 20 {
		t.Fatalf("second import = %+v", res)
	}
	if got := kinds(t, st, before); !slices.Equal(got, []string{"pack_unchanged"}) {
		t.Fatalf("the second import logged %v, want pack_unchanged alone", got)
	}
	again, _ := st.Packs(ctx)
	if !reflect.DeepEqual(again, recs) {
		t.Fatalf("the record changed:\n%+v\n%+v", recs, again)
	}
	routes, _ := st.Routes(ctx)
	if len(routes) != 5 {
		t.Fatalf("%d routes, want 5", len(routes))
	}
}

// A yard set up by hand under the pack's names is overwritten in what the
// pack carries and keeps the rest: the agent, model, arguments and env_file
// of a group, the variables of a script group's command, the members of a
// people group, a resident's identity and directory. No route is added
// beside the one a stage has.
func TestApplyOverAYardSetUpByHand(t *testing.T) {
	st, o := yard(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	rig := "web"
	must(st.PutGroup(ctx, store.Group{Name: "web-builders", Runner: "herdr", Limit: 2,
		Config: map[string]any{"kind": "codex", "model": "luna", "args": []any{"--yolo"}, "env_file": "/secrets.env", "idle": "20m", "prompt": "/old/builder.md"}}))
	must(st.PutGroup(ctx, store.Group{Name: "web-refinery", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "YARDR_PUSH=origin /old/merge-local --fast"}}))
	must(st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"ann"}}))
	for stage, group := range map[string]string{"new": "web-builders", "approved": "web-refinery", "decide": "decisions"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Rig: &rig, Group: group, Priority: 7})
		must(err)
	}
	_, err := st.PutResident(ctx, store.Resident{Name: "boss", Runner: "herdr", Dir: "/home/boss", MaxAge: "6h",
		Config: map[string]any{"kind": "codex", "prompt": "/old/mayor.md", "env_file": "/boss.env"}, On: []string{"held"}}, "me")
	must(err)
	must(st.SetIdentity(ctx, "boss", "linear", store.Identity{ID: "u1", Key: "boss"}, "me"))
	must(st.AddSubscription(ctx, "boss", store.Subscription{Source: "linear", Scope: "all"}, "me"))
	// The same source and scope with a filter is another subscription, and
	// replacing the unfiltered one must leave it.
	must(st.AddSubscription(ctx, "boss", store.Subscription{Source: "linear", Scope: "all", Where: map[string]string{"team": "ENG"}}, "me"))

	o.Residents = map[string]string{"mayor": "boss"}
	res := apply(t, st, build(t, st, load(t, starter), o), nil)
	if !res.Wrote() {
		t.Fatalf("result = %+v", res)
	}
	install := filepath.Join(o.Home, "packs", "starter")

	g, err := st.GetGroup(ctx, "web-builders")
	must(err)
	want := map[string]any{"kind": "codex", "model": "luna", "args": []any{"--yolo"}, "env_file": "/secrets.env", "idle": "20m", "prompt": filepath.Join(install, "roles", "builder.md")}
	if !reflect.DeepEqual(g.Config, want) || g.Limit != 5 {
		t.Fatalf("web-builders = %+v", g)
	}
	g, err = st.GetGroup(ctx, "web-refinery")
	must(err)
	if got, want := g.Config["cmd"], "YARDR_PUSH=origin "+filepath.Join(install, "scripts", "merge-local")+" --fast"; got != want {
		t.Fatalf("cmd = %q, want %q", got, want)
	}
	g, err = st.GetGroup(ctx, "decisions")
	must(err)
	if !slices.Equal(g.Members, []string{"ann"}) {
		t.Fatalf("decisions members = %v", g.Members)
	}
	routes, err := st.Routes(ctx)
	must(err)
	stages := map[string]int{}
	for _, r := range routes {
		stages[r.Stage]++
		if (r.Stage == "new" || r.Stage == "approved" || r.Stage == "decide") && r.Priority != 7 {
			t.Errorf("route %s lost its priority: %+v", r.Stage, r)
		}
	}
	if want := map[string]int{"backlog": 1, "new": 1, "review": 1, "approved": 1, "decide": 1}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("routes per stage = %v, want %v", stages, want)
	}
	boss, err := st.GetResident(ctx, "boss")
	must(err)
	if boss.Dir != "/home/boss" || boss.MaxAge != "6h" || boss.Config["kind"] != "codex" || boss.Config["env_file"] != "/boss.env" ||
		boss.Identities["linear"].ID != "u1" || boss.Config["prompt"] != filepath.Join(install, "roles", "mayor.md") || len(boss.On) != 12 {
		t.Fatalf("boss = %+v", boss)
	}
	var subs []string
	for _, s := range boss.Subscriptions {
		subs = append(subs, s.String())
	}
	slices.Sort(subs)
	if want := []string{"linear all team=ENG", "linear involved", "yardr all rig=web stage=decide", "yardr all rig=web state=done"}; !slices.Equal(subs, want) {
		t.Fatalf("subscriptions = %v, want %v", subs, want)
	}
}

// A pack that dropped a group: the group, its route and its role file,
// which the last import brought, are reported and left; --prune removes
// them, and nothing else, not a group of the rig the pack never had.
func TestDroppedObjectsAreReportedAndPrunedOnlyWhenAsked(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), answers)
	if err := st.PutGroup(ctx, store.Group{Name: "web-extra", Runner: "manual"}); err != nil {
		t.Fatal(err)
	}
	edit(t, dir,
		"outcomes: { done: review, question: decide }", "outcomes: { done: approved, question: decide }",
		"      review:   { group: reviewers, outcomes: { done: approved, changes: new, question: decide } }\n", "",
		"  reviewers: { agent: roles/reviewer.md, limit: 3 }\n", "")

	plan := build(t, st, load(t, dir), o)
	gone := []string{"file roles/reviewer.md", "group web-reviewers", "route rig web stage review -> web-reviewers"}
	if got := actions(plan, pack.Dropped); !slices.Equal(slices.Sorted(slices.Values(got)), gone) {
		t.Fatalf("dropped = %v, want %v", got, gone)
	}
	if got := actions(plan, pack.NotInPack); !slices.Equal(got, []string{"group web-extra"}) {
		t.Fatalf("not in the pack = %v", got)
	}

	res := apply(t, st, plan, nil)
	if len(res.Pruned) != 0 || !slices.Equal(slices.Sorted(slices.Values(res.Left)), gone) {
		t.Fatalf("without --prune: %+v", res)
	}
	reviewer := filepath.Join(o.Home, "packs", "starter", "roles", "reviewer.md")
	if _, err := st.GetGroup(ctx, "web-reviewers"); err != nil {
		t.Fatalf("web-reviewers was removed without --prune: %v", err)
	}
	if _, err := os.Stat(reviewer); err != nil {
		t.Fatal(err)
	}
	// Still reported the next time: the record keeps what was not pruned.
	plan = build(t, st, load(t, dir), o)
	if got := actions(plan, pack.Dropped); len(got) != 3 {
		t.Fatalf("dropped after an import without --prune = %v", got)
	}

	a := pack.Answers{}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, Prune: true, Actor: "me"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slices.Sorted(slices.Values(res.Pruned)), gone) || len(res.Left) != 0 || !res.Recorded {
		t.Fatalf("with --prune: %+v", res)
	}
	if _, err := st.GetGroup(ctx, "web-reviewers"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("web-reviewers after --prune: %v", err)
	}
	if _, err := os.Stat(reviewer); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reviewer.md after --prune: %v", err)
	}
	if _, err := st.GetGroup(ctx, "web-extra"); err != nil {
		t.Fatalf("--prune removed a group the pack never had: %v", err)
	}
	routes, _ := st.Routes(ctx)
	if len(routes) != 4 || slices.ContainsFunc(routes, func(r store.Route) bool { return r.Stage == "review" }) {
		t.Fatalf("routes = %+v", routes)
	}
	plan = build(t, st, load(t, dir), o)
	if got := actions(plan, pack.Dropped); len(got) != 0 {
		t.Fatalf("dropped after --prune = %v", got)
	}
	recs, _ := st.Packs(ctx)
	if len(recs) != 1 || slices.ContainsFunc(recs[0].Objects, func(o store.PackObject) bool { return o.Name == "web-reviewers" }) {
		t.Fatalf("record = %+v", recs)
	}
}

// A people group is the yard's: one that a second rig's import names too,
// or another rig routes to, is held even from a prune.
func TestPruneHoldsAGroupAnotherRigUses(t *testing.T) {
	st, o := yard(t)
	if err := st.PutRig(ctx, store.Rig{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), answers)
	other := o
	other.Rig = "api"
	apply(t, st, build(t, st, load(t, starter), other), answers)

	edit(t, dir,
		"      decide:   { group: decisions, manual: [backlog, new] }", "      decide:   { manual: [backlog, new] }",
		"  decisions: { people: true }\n", "")
	plan := build(t, st, load(t, dir), o)
	g := find(t, plan, "group", "decisions")
	if g.Action != pack.Dropped || !strings.Contains(g.Held, "pack starter in rig api names it too") {
		t.Fatalf("decisions = %+v", g)
	}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Prune: true, Actor: "me"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Left, "group decisions") {
		t.Fatalf("result = %+v", res)
	}
	if _, err := st.GetGroup(ctx, "decisions"); err != nil {
		t.Fatalf("decisions was pruned from under rig api: %v", err)
	}
	// Rig web's route to it went with the stage's group; rig api's stays.
	routes, _ := st.Routes(ctx)
	var rigs []string
	for _, r := range routes {
		if r.Group == "decisions" {
			rigs = append(rigs, *r.Rig)
		}
	}
	if !slices.Equal(rigs, []string{"api"}) {
		t.Fatalf("routes to decisions are of rigs %v, want api alone", rigs)
	}
}

// An import that would strand an open bead is refused before anything is
// written: no file, no group, no event.
func TestApplyRefusesStrandedBeadsChangingNothing(t *testing.T) {
	st, o := yard(t)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "stray", Rig: "web", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, starter), o)
	before := head(t, st)
	_, err = pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: pack.Answers(answers), Actor: "me"})
	if err == nil || !strings.Contains(err.Error(), b.ID+" (triage)") {
		t.Fatalf("err = %v, want a refusal naming %s", err, b.ID)
	}
	if got := kinds(t, st, before); len(got) != 0 {
		t.Fatalf("a refused import logged %v", got)
	}
	groups, _ := st.Groups(ctx)
	recs, _ := st.Packs(ctx)
	if _, serr := os.Stat(plan.Install); len(groups) != 0 || len(recs) != 0 || !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("a refused import left groups %v, records %v, files %v", groups, recs, serr)
	}
}

// A question without an answer, an answer that cannot be used, and a pack
// file changed since the plan was made each stop the import before its
// first write.
func TestApplyChecksBeforeItWrites(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	plan := build(t, st, load(t, dir), o)
	before := head(t, st)
	fails := func(a pack.Answers, want string) {
		t.Helper()
		_, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, Actor: "me"})
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("err = %v, want %q", err, want)
		}
		if got := kinds(t, st, before); len(got) != 0 {
			t.Fatalf("a failed check logged %v", got)
		}
		if _, err := os.Stat(plan.Install); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a failed check wrote files: %v", err)
		}
	}
	fails(pack.Answers{}, "group.web-builders.kind has no answer")
	full := pack.Answers{}
	for _, q := range plan.Questions {
		full[q.ID] = ""
	}
	fails(full, "group web-builders needs an agent kind")
	for _, q := range plan.Questions {
		if q.Field == "kind" {
			full[q.ID] = "claude"
		}
	}
	fails(full, "resident mayor needs a directory")
	full["resident.mayor.dir"] = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "roles", "builder.md"), []byte("changed under the plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fails(full, "builder.md changed since the plan was made")
}

// failing is a store that refuses the nth route added.
type failing struct {
	store.API
	left int
}

func (f *failing) AddRoute(ctx context.Context, r store.Route) (*store.Route, error) {
	if f.left--; f.left == 0 {
		return nil, errors.New("the disk is full")
	}
	return f.API.AddRoute(ctx, r)
}

// A write that fails halfway stops the import: what was written stays,
// nothing is recorded, and applying again finishes it without doing
// anything twice.
func TestApplyAgainFinishesAnImportThatFailedHalfway(t *testing.T) {
	st, o := yard(t)
	plan := build(t, st, load(t, starter), o)
	a := pack.Answers{}
	for _, q := range plan.Questions {
		a[q.ID] = map[string]string{"kind": "claude", "dir": o.Home}[q.Field]
	}
	_, err := pack.Apply(ctx, &failing{API: st, left: 3}, plan, pack.ApplyOptions{Answers: a, Actor: "me"})
	if err == nil || !strings.Contains(err.Error(), "the disk is full") || !strings.Contains(err.Error(), "applying again finishes it") {
		t.Fatalf("err = %v", err)
	}
	routes, _ := st.Routes(ctx)
	groups, _ := st.Groups(ctx)
	recs, _ := st.Packs(ctx)
	if len(routes) != 2 || len(groups) != 5 || len(recs) != 0 {
		t.Fatalf("halfway: %d routes, %d groups, %d records; want 2, 5, 0", len(routes), len(groups), len(recs))
	}

	plan = build(t, st, load(t, starter), o)
	// Left to do: three routes, the resident and its three subscriptions.
	if got := actions(plan, pack.Create); len(got) != 7 {
		t.Fatalf("to create after the failure: %v", got)
	}
	res := apply(t, st, plan, nil)
	if !res.Recorded {
		t.Fatalf("result = %+v", res)
	}
	routes, _ = st.Routes(ctx)
	groups, _ = st.Groups(ctx)
	if len(routes) != 5 || len(groups) != 5 {
		t.Fatalf("after applying again: %d routes, %d groups", len(routes), len(groups))
	}
	if got := actions(build(t, st, load(t, starter), o), pack.Unchanged); len(got) != 20 {
		t.Fatalf("%d unchanged, want 20", len(got))
	}
}

// A running resident whose role or subscriptions change keeps running: the
// plan notes it and the result names it. One the import leaves as it is
// needs no restart.
func TestRunningResidentIsNotRestarted(t *testing.T) {
	st, o := yard(t)
	if _, err := st.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr", Config: map[string]any{"kind": "claude"}, On: []string{"held"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.ResidentStarted(ctx, "mayor", map[string]any{"pane": "p1"}); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, starter), o)
	if !find(t, plan, "resident", "mayor").Resident.Restart ||
		!slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.Contains(n, "yardr resident restart mayor") }) {
		t.Fatalf("plan: %+v, notes %v", find(t, plan, "resident", "mayor").Resident, plan.Notes)
	}
	res := apply(t, st, plan, answers)
	if !slices.Equal(res.Restart, []string{"mayor"}) {
		t.Fatalf("restart = %v", res.Restart)
	}
	r, err := st.GetResident(ctx, "mayor")
	if err != nil || !r.Live() || r.RestartRequested != "" {
		t.Fatalf("mayor = %+v, %v: the import must not restart it", r, err)
	}
	res = apply(t, st, build(t, st, load(t, starter), o), nil)
	if len(res.Restart) != 0 {
		t.Fatalf("restart after an import that changed nothing = %v", res.Restart)
	}
}

// A rig's own role wins over the pack's: where the rig has
// .yardr/roles/<name>.md, a group's prompt stays as it is (a new group
// gets the role by the name the rig's file is found under) and the plan
// says it was kept. The pack's copy is installed all the same, and
// PackRoles sets it on purpose.
func TestRigsOwnRoleIsKept(t *testing.T) {
	st, o := yard(t)
	rigDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rigDir, ".yardr", "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"builder.md", "mayor.md"} {
		if err := os.WriteFile(filepath.Join(rigDir, ".yardr", "roles", name), []byte("the rig's own\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutRig(ctx, store.Rig{Name: "web", Path: rigDir, Kind: store.RigDir}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "web-reviewers", Runner: "herdr", Config: map[string]any{"kind": "codex", "prompt": "roles/reviewer.md"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr", Config: map[string]any{"kind": "claude", "prompt": "/home/roles/mayor.md"}}, "me"); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(o.Home, "packs", "starter")

	plan := build(t, st, load(t, starter), o)
	b := find(t, plan, "group", "web-builders")
	if b.Group.Config["prompt"] != "roles/builder.md" || !slices.Equal(b.Keeps, []string{"prompt roles/builder.md: the rig has its own .yardr/roles/builder.md"}) {
		t.Fatalf("web-builders = %+v, prompt %v", b, b.Group.Config["prompt"])
	}
	// The rig has no reviewer.md of its own, so that group gets the pack's.
	if r := find(t, plan, "group", "web-reviewers"); len(r.Keeps) != 0 || r.Group.Config["prompt"] != filepath.Join(install, "roles", "reviewer.md") {
		t.Fatalf("web-reviewers = %+v", r)
	}
	m := find(t, plan, "resident", "mayor")
	if m.Resident.Prompt != "/home/roles/mayor.md" || len(m.Keeps) != 1 || slices.ContainsFunc(m.Changes, func(d pack.Diff) bool { return d.Field == "prompt" }) {
		t.Fatalf("mayor = %+v", m)
	}
	// Installed all the same, for rigs without their own.
	if f := find(t, plan, "file", "roles/builder.md"); f.Action != pack.Create {
		t.Fatalf("builder.md = %+v", f)
	}
	apply(t, st, plan, answers)
	g, _ := st.GetGroup(ctx, "web-builders")
	r, _ := st.GetResident(ctx, "mayor")
	if g.Config["prompt"] != "roles/builder.md" || r.Config["prompt"] != "/home/roles/mayor.md" {
		t.Fatalf("after the import: builders %v, mayor %v", g.Config["prompt"], r.Config["prompt"])
	}
	if got := actions(build(t, st, load(t, starter), o), pack.Change); len(got) != 0 {
		t.Fatalf("a second import would change %v", got)
	}

	o.PackRoles = true
	plan = build(t, st, load(t, starter), o)
	b = find(t, plan, "group", "web-builders")
	want := []pack.Diff{{Field: "prompt", Old: "roles/builder.md", New: filepath.Join(install, "roles", "builder.md")}}
	if len(b.Keeps) != 0 || !reflect.DeepEqual(b.Changes, want) {
		t.Fatalf("with PackRoles: %+v", b)
	}
	if m := find(t, plan, "resident", "mayor"); m.Resident.Prompt != filepath.Join(install, "roles", "mayor.md") {
		t.Fatalf("with PackRoles: mayor %+v", m.Resident)
	}
}

// Of several flows a rig could follow, the plan asks which; the import
// remembers the answer, so the next one need not be told, and a flow the
// rig is moved off is then reported as dropped.
func TestFlowChoiceIsAskedAndRemembered(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	edit(t, dir, "      merged:   { end: true }\n",
		"      merged:   { end: true }\n  short:\n    stages:\n      new:    { group: builders, outcomes: { done: merged } }\n      merged: { end: true }\n")
	plan := build(t, st, load(t, dir), o)
	if !slices.ContainsFunc(plan.Questions, func(q pack.Question) bool { return q.ID == pack.FlowQuestion && q.Default == "default" }) {
		t.Fatalf("questions = %+v", plan.Questions)
	}
	a := pack.Answers{}
	for _, q := range plan.Questions {
		a[q.ID] = map[string]string{"kind": "claude", "dir": o.Home}[q.Field]
	}
	a[pack.FlowQuestion] = "short"
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, Actor: "me"}); err == nil || !strings.Contains(err.Error(), "the plan is for flow default, not short") {
		t.Fatalf("err = %v", err)
	}

	o.Flow = "short"
	apply(t, st, build(t, st, load(t, dir), o), answers)
	o.Flow = ""
	plan = build(t, st, load(t, dir), o)
	if plan.Flow != "short" || len(plan.Questions) != 0 || len(actions(plan, pack.Create))+len(actions(plan, pack.Change)) != 0 {
		t.Fatalf("flow %q, questions %v, create %v", plan.Flow, plan.Questions, actions(plan, pack.Create))
	}

	o.Flow = "default"
	plan = build(t, st, load(t, dir), o)
	if got := actions(plan, pack.Dropped); !slices.Equal(got, []string{"flow starter.short"}) {
		t.Fatalf("dropped = %v", got)
	}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: pack.Answers(answers), Prune: true, Actor: "me"})
	if err != nil || !slices.Equal(res.Pruned, []string{"flow starter.short"}) {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if _, err := st.GetFlow(ctx, "starter.short"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("starter.short after the prune: %v", err)
	}
}

// Pruning a pack's dropped type-flow pointer must not silently send its
// open beads to the rig's default flow when that flow lacks their stage.
func TestPruneHoldsTypePointerThatWouldStrandBeads(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	text, err := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Add a type flow whose stage does not exist in the default flow.
	withType := strings.Replace(string(text), "groups:\n", "  task-flow:\n    type: task\n    stages:\n      triage: { group: builders, outcomes: { done: merged } }\n      merged: { end: true }\ngroups:\n", 1)
	if withType == string(text) {
		t.Fatal("pack.yaml has no groups section")
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(withType), 0o644); err != nil {
		t.Fatal(err)
	}
	apply(t, st, build(t, st, load(t, dir), o), answers)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "in triage", Rig: "web", Type: "task", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	text = []byte(strings.Replace(string(withType), "  task-flow:\n    type: task\n    stages:\n      triage: { group: builders, outcomes: { done: merged } }\n      merged: { end: true }\n", "", 1))
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), text, 0o644); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, dir), o)
	var dropped pack.Object
	for _, obj := range plan.Objects {
		if obj.Kind == "pointer" && obj.Action == pack.Dropped && obj.Pointer != nil && obj.Pointer.Type == "task" {
			dropped = obj
			break
		}
	}
	if dropped.Held == "" || !strings.Contains(dropped.Held, b.ID+" (triage)") {
		t.Fatalf("type pointer = %+v, want held with the stranded bead", dropped)
	}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: pack.Answers(answers), Prune: true, Actor: "me"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Left, "pointer rig web type task -> starter.task-flow") {
		t.Fatalf("prune result = %+v", res)
	}
	ptrs, err := st.FlowPointers(ctx)
	if err != nil || !slices.Contains(ptrs, store.FlowPointer{Rig: "web", Type: "task", Flow: "starter.task-flow"}) {
		t.Fatalf("type flow pointer after prune = %v, %v", ptrs, err)
	}
}

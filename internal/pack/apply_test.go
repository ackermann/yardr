package pack_test

import (
	"context"
	"errors"
	"fmt"
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
	o.Crew = map[string]string{"yardmaster": "boss"}
	res := apply(t, st, build(t, st, load(t, starter), o), answers)
	if res.Created != 25 || res.Changed != 0 || res.Unchanged != 0 || !res.Recorded || !res.Wrote() {
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
	if g, _ := st.GetGroup(ctx, "backlog"); g == nil || !slices.Equal(g.Members, []string{"ann", "yardmaster"}) {
		t.Fatalf("backlog = %+v", g)
	}
	if info, err := os.Stat(filepath.Join(o.Home, "packs", "starter", "scripts", "merge-local")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("merge-local: %v %v", info, err)
	}
	boss, err := st.GetCrew(ctx, "boss")
	if err != nil || len(boss.On) != 16 || len(boss.Subscriptions) != 3 || boss.Config["kind"] != "claude" || boss.Dir == "" {
		t.Fatalf("boss = %+v, %v", boss, err)
	}
	if _, err := st.GetCrew(ctx, "yardmaster"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a crew member yardmaster exists beside boss: %v", err)
	}

	recs, err := st.Packs(ctx)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	rec := recs[0]
	abs, _ := filepath.Abs(starter)
	if rec.Pack != "starter" || rec.Depot != "web" || rec.Source != abs || rec.By != "me" || rec.At.IsZero() || rec.Flow != "default" ||
		rec.Groups["builders"] != "web-builders" || rec.Groups["backlog"] != "backlog" || !reflect.DeepEqual(rec.Crew, map[string]string{"yardmaster": "boss"}) {
		t.Fatalf("record = %+v", rec)
	}
	// The starter pack is in this repository, so its commit is known.
	if len(rec.Commit) != 40 {
		t.Fatalf("commit = %q", rec.Commit)
	}
	// Everything but the crew member, which stays the yard's.
	if len(rec.Objects) != 24 {
		t.Fatalf("%d objects recorded: %+v", len(rec.Objects), rec.Objects)
	}

	// Again, with no flag: the record says who took yardmaster.
	before := head(t, st)
	o.Crew = nil
	plan := build(t, st, load(t, starter), o)
	if len(plan.Questions) != 0 || len(actions(plan, pack.Unchanged)) != 25 {
		t.Fatalf("second plan: questions %v, unchanged %v", plan.Questions, actions(plan, pack.Unchanged))
	}
	res = apply(t, st, plan, nil)
	if res.Wrote() || res.Recorded || res.Unchanged != 25 {
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
// people group, a crew member's identity and directory. No route is added
// beside the one a stage has.
func TestApplyOverAYardSetUpByHand(t *testing.T) {
	st, o := yard(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	depot := "web"
	must(st.PutGroup(ctx, store.Group{Name: "web-builders", Runner: "herdr", Limit: 2,
		Config: map[string]any{"kind": "codex", "model": "luna", "args": []any{"--yolo"}, "env_file": "/secrets.env", "idle": "20m", "prompt": "/old/builder.md"}}))
	must(st.PutGroup(ctx, store.Group{Name: "web-assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "YARDR_PUSH=origin /old/merge-local --fast"}}))
	must(st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"ann"}}))
	for stage, group := range map[string]string{"new": "web-builders", "approved": "web-assembly", "decide": "decisions"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Depot: &depot, Group: group, Priority: 7})
		must(err)
	}
	_, err := st.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr", Dir: "/home/boss", MaxAge: "6h",
		Config: map[string]any{"kind": "codex", "prompt": "/old/yardmaster.md", "env_file": "/boss.env"}, On: []string{"held"}}, "me")
	must(err)
	must(st.SetIdentity(ctx, "boss", "linear", store.Identity{ID: "u1", Key: "boss"}, "me"))
	must(st.AddSubscription(ctx, "boss", store.Subscription{Source: "linear", Scope: "all"}, "me"))
	// The same source and scope with a filter is another subscription, and
	// replacing the unfiltered one must leave it.
	must(st.AddSubscription(ctx, "boss", store.Subscription{Source: "linear", Scope: "all", Where: map[string]string{"team": "ENG"}}, "me"))

	o.Crew = map[string]string{"yardmaster": "boss"}
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
	g, err = st.GetGroup(ctx, "web-assembly")
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
	boss, err := st.GetCrew(ctx, "boss")
	must(err)
	if boss.Dir != "/home/boss" || boss.MaxAge != "6h" || boss.Config["kind"] != "codex" || boss.Config["env_file"] != "/boss.env" ||
		boss.Identities["linear"].ID != "u1" || boss.Config["prompt"] != filepath.Join(install, "roles", "yardmaster.md") || len(boss.On) != 16 {
		t.Fatalf("boss = %+v", boss)
	}
	var subs []string
	for _, s := range boss.Subscriptions {
		subs = append(subs, s.String())
	}
	slices.Sort(subs)
	if want := []string{"linear all team=ENG", "linear involved", "yardr all depot=web stage=decide", "yardr all depot=web state=done"}; !slices.Equal(subs, want) {
		t.Fatalf("subscriptions = %v, want %v", subs, want)
	}
}

// A pack that dropped a group: the group, its route and its role file,
// which the last import brought, are reported and left; --prune removes
// them, and nothing else, not a group of the depot the pack never had.
func TestDroppedObjectsAreReportedAndPrunedOnlyWhenAsked(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), answers)
	if err := st.PutGroup(ctx, store.Group{Name: "web-extra", Runner: "manual"}); err != nil {
		t.Fatal(err)
	}
	edit(t, dir,
		"outcomes: { done: review, question: decide }", "outcomes: { done: approved, question: decide }",
		"      review:   { group: reviewers, file: flows/default/review.md,   outcomes: { done: approved, changes: new, question: decide } }\n", "",
		"  reviewers: { agent: roles/reviewer.md, limit: 3 }\n", "",
		// The stage map names the stage too, and a pack is refused for that.
		"new: started, review: review, merged: done", "new: started, merged: done")

	plan := build(t, st, load(t, dir), o)
	// The stage's file goes with it: named by where it is in the home, since
	// the pack has no path for it any more.
	gone := []string{"file flows/starter.default/review.md", "file roles/reviewer.md", "group web-reviewers", "route depot web stage review -> web-reviewers"}
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
	stage := filepath.Join(o.Home, "flows", "starter.default", "review.md")
	for _, f := range []string{reviewer, stage} {
		if _, err := os.Stat(f); err != nil {
			t.Fatal(err)
		}
	}
	// Still reported the next time: the record keeps what was not pruned.
	plan = build(t, st, load(t, dir), o)
	if got := actions(plan, pack.Dropped); len(got) != 4 {
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
	for _, f := range []string{reviewer, stage} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s after --prune: %v", f, err)
		}
	}
	// The stages the pack still has keep their files.
	if _, err := os.Stat(filepath.Join(filepath.Dir(stage), "new.md")); err != nil {
		t.Fatalf("--prune removed the stage file of a stage the pack has: %v", err)
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

// A people group is the yard's: one that a second depot's import names too,
// or another depot routes to, is held even from a prune.
func TestPruneHoldsAGroupAnotherDepotUses(t *testing.T) {
	st, o := yard(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), answers)
	other := o
	other.Depot = "api"
	apply(t, st, build(t, st, load(t, starter), other), answers)

	edit(t, dir,
		"      decide:   { group: decisions, file: flows/default/decide.md,   manual: [backlog, new] }", "      decide:   { manual: [backlog, new] }",
		"  decisions: { people: true }\n", "")
	plan := build(t, st, load(t, dir), o)
	g := find(t, plan, "group", "decisions")
	if g.Action != pack.Dropped || !strings.Contains(g.Held, "pack starter in depot api names it too") {
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
		t.Fatalf("decisions was pruned from under depot api: %v", err)
	}
	// Depot web's route to it went with the stage's group; depot api's stays.
	routes, _ := st.Routes(ctx)
	var depots []string
	for _, r := range routes {
		if r.Group == "decisions" {
			depots = append(depots, *r.Depot)
		}
	}
	if !slices.Equal(depots, []string{"api"}) {
		t.Fatalf("routes to decisions are of depots %v, want api alone", depots)
	}
}

// An import that would strand an open bead is refused before anything is
// written: no file, no group, no event.
func TestApplyRefusesStrandedBeadsChangingNothing(t *testing.T) {
	st, o := yard(t)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "stray", Depot: "web", Stage: "triage"}, "me")
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
	fails(full, "crew member yardmaster needs a directory")
	full["crew.yardmaster.dir"] = t.TempDir()
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
	// Left to do: three routes, the crew member and its three subscriptions.
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
	if got := actions(build(t, st, load(t, starter), o), pack.Unchanged); len(got) != 25 {
		t.Fatalf("%d unchanged, want 25", len(got))
	}
}

// A running crew member whose role or subscriptions change keeps running: the
// plan notes it and the result names it. One the import leaves as it is
// needs no restart.
func TestRunningCrewIsNotRestarted(t *testing.T) {
	st, o := yard(t)
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude"}, On: []string{"held"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.CrewStarted(ctx, "yardmaster", map[string]any{"pane": "p1"}); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, starter), o)
	if !find(t, plan, "crew", "yardmaster").CrewMember.Restart ||
		!slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.Contains(n, "yardr crew restart yardmaster") }) {
		t.Fatalf("plan: %+v, notes %v", find(t, plan, "crew", "yardmaster").CrewMember, plan.Notes)
	}
	res := apply(t, st, plan, answers)
	if !slices.Equal(res.Restart, []string{"yardmaster"}) {
		t.Fatalf("restart = %v", res.Restart)
	}
	r, err := st.GetCrew(ctx, "yardmaster")
	if err != nil || !r.Live() || r.RestartRequested != "" {
		t.Fatalf("yardmaster = %+v, %v: the import must not restart it", r, err)
	}
	res = apply(t, st, build(t, st, load(t, starter), o), nil)
	if len(res.Restart) != 0 {
		t.Fatalf("restart after an import that changed nothing = %v", res.Restart)
	}
}

// depotWithRoles is the yard's depot web as a directory that has its own
// builder.md and yardmaster.md, a builders group and a yardmaster prompted as
// a yard set up by hand has them.
func depotWithRoles(t *testing.T, st store.API) {
	t.Helper()
	depotDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(depotDir, ".yardr", "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"builder.md", "yardmaster.md"} {
		if err := os.WriteFile(filepath.Join(depotDir, ".yardr", "roles", name), []byte("the depot's own\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Path: depotDir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "web-builders", Runner: "herdr", Config: map[string]any{"kind": "claude", "prompt": "roles/builder.md"}, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude", "prompt": "/home/roles/yardmaster.md"}}, "me"); err != nil {
		t.Fatal(err)
	}
}

// record is the yard's record of the starter pack in depot web.
func record(t *testing.T, st store.API) store.PackImport {
	t.Helper()
	recs, err := st.Packs(ctx)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	return recs[0]
}

// The pack's roles win by default, as its flow does: a group and a crew member
// are pointed at the pack's copied role though the depot has its own
// .yardr/roles/<name>.md, the plan shows the change of prompt and names the
// depot's file that stops being used, and a second import changes nothing.
func TestPacksRolesWinOverTheDepotsOwn(t *testing.T) {
	st, o := yard(t)
	depotWithRoles(t, st)
	install := filepath.Join(o.Home, "packs", "starter")

	plan := build(t, st, load(t, starter), o)
	b := find(t, plan, "group", "web-builders")
	want := []pack.Diff{{Field: "prompt", Old: "roles/builder.md", New: filepath.Join(install, "roles", "builder.md")}}
	if b.Action != pack.Change || !reflect.DeepEqual(b.Changes, want) || len(b.Keeps) != 0 || !slices.Equal(b.Shadows, []string{".yardr/roles/builder.md"}) {
		t.Fatalf("web-builders = %+v", b)
	}
	// The depot has no reviewer.md of its own: nothing is shadowed there.
	if r := find(t, plan, "group", "web-reviewers"); len(r.Shadows) != 0 || r.Group.Config["prompt"] != filepath.Join(install, "roles", "reviewer.md") {
		t.Fatalf("web-reviewers = %+v", r)
	}
	m := find(t, plan, "crew", "yardmaster")
	want = []pack.Diff{{Field: "prompt", Old: "/home/roles/yardmaster.md", New: filepath.Join(install, "roles", "yardmaster.md")}}
	if m.Action != pack.Change || !slices.Equal(m.Shadows, []string{".yardr/roles/yardmaster.md"}) || len(m.Keeps) != 0 || !reflect.DeepEqual(m.Changes[:1], want) {
		t.Fatalf("yardmaster = %+v", m)
	}
	apply(t, st, plan, nil)
	g, _ := st.GetGroup(ctx, "web-builders")
	r, _ := st.GetCrew(ctx, "yardmaster")
	if g.Config["prompt"] != filepath.Join(install, "roles", "builder.md") || r.Config["prompt"] != filepath.Join(install, "roles", "yardmaster.md") {
		t.Fatalf("after the import: builders %v, yardmaster %v", g.Config["prompt"], r.Config["prompt"])
	}
	if rec := record(t, st); rec.DepotRoles {
		t.Fatalf("the record says the depot's roles were kept: %+v", rec)
	}

	// Again, with no flag: nothing to write, the shadowed file still named.
	plan = build(t, st, load(t, starter), o)
	if got := append(actions(plan, pack.Change), actions(plan, pack.Create)...); len(got) != 0 {
		t.Fatalf("a second import would write %v", got)
	}
	if b := find(t, plan, "group", "web-builders"); !slices.Equal(b.Shadows, []string{".yardr/roles/builder.md"}) {
		t.Fatalf("web-builders, second plan = %+v", b)
	}
	if res := apply(t, st, plan, nil); res.Wrote() || res.Recorded {
		t.Fatalf("the second import: %+v", res)
	}
}

// DepotRoles is the opposite choice: a group or crew member whose depot has its
// own role keeps it. The record remembers the choice, so the next import
// repeats it without the flag and changes nothing; PackRoles undoes it, and
// DepotRoles after that takes a group off the pack's copy again.
func TestDepotRolesKeepsTheDepotsOwnAndIsRemembered(t *testing.T) {
	st, o := yard(t)
	depotWithRoles(t, st)
	// The builders group is new here: it gets the role by the depot's name for it.
	if _, err := st.DeleteGroup(ctx, "web-builders", "me"); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(o.Home, "packs", "starter")

	o.DepotRoles = true
	plan := build(t, st, load(t, starter), o)
	b := find(t, plan, "group", "web-builders")
	if !plan.DepotRoles || b.Action != pack.Create || b.Group.Config["prompt"] != "roles/builder.md" || len(b.Shadows) != 0 ||
		!slices.Equal(b.Keeps, []string{"prompt roles/builder.md: the depot has its own .yardr/roles/builder.md"}) {
		t.Fatalf("web-builders = %+v, prompt %v", b, b.Group.Config["prompt"])
	}
	// The depot has no reviewer.md of its own, so that group gets the pack's.
	if r := find(t, plan, "group", "web-reviewers"); len(r.Keeps) != 0 || r.Group.Config["prompt"] != filepath.Join(install, "roles", "reviewer.md") {
		t.Fatalf("web-reviewers = %+v", r)
	}
	m := find(t, plan, "crew", "yardmaster")
	if m.CrewMember.Prompt != "/home/roles/yardmaster.md" || len(m.Keeps) != 1 || len(m.Shadows) != 0 || slices.ContainsFunc(m.Changes, func(d pack.Diff) bool { return d.Field == "prompt" }) {
		t.Fatalf("yardmaster = %+v", m)
	}
	// Installed all the same, for depots without their own.
	if f := find(t, plan, "file", "roles/builder.md"); f.Action != pack.Create {
		t.Fatalf("builder.md = %+v", f)
	}
	apply(t, st, plan, nil)
	g, _ := st.GetGroup(ctx, "web-builders")
	r, _ := st.GetCrew(ctx, "yardmaster")
	if g.Config["prompt"] != "roles/builder.md" || r.Config["prompt"] != "/home/roles/yardmaster.md" {
		t.Fatalf("after the import: builders %v, yardmaster %v", g.Config["prompt"], r.Config["prompt"])
	}
	if rec := record(t, st); !rec.DepotRoles {
		t.Fatalf("the record does not say the depot's roles were kept: %+v", rec)
	}

	// No flag: the recorded choice, so nothing changes.
	o.DepotRoles = false
	plan = build(t, st, load(t, starter), o)
	if got := append(actions(plan, pack.Change), actions(plan, pack.Create)...); !plan.DepotRoles || len(got) != 0 {
		t.Fatalf("a second import, depot roles %v, would write %v", plan.DepotRoles, got)
	}
	if res := apply(t, st, plan, nil); res.Wrote() || res.Recorded {
		t.Fatalf("the second import: %+v", res)
	}

	// Told anew: the pack's roles, and the record says so afterwards.
	o.PackRoles = true
	plan = build(t, st, load(t, starter), o)
	b = find(t, plan, "group", "web-builders")
	want := []pack.Diff{{Field: "prompt", Old: "roles/builder.md", New: filepath.Join(install, "roles", "builder.md")}}
	if plan.DepotRoles || len(b.Keeps) != 0 || !reflect.DeepEqual(b.Changes, want) || !slices.Equal(b.Shadows, []string{".yardr/roles/builder.md"}) {
		t.Fatalf("with PackRoles: %+v", b)
	}
	if m := find(t, plan, "crew", "yardmaster"); m.CrewMember.Prompt != filepath.Join(install, "roles", "yardmaster.md") {
		t.Fatalf("with PackRoles: yardmaster %+v", m.CrewMember)
	}
	apply(t, st, plan, nil)
	if rec := record(t, st); rec.DepotRoles {
		t.Fatalf("the record after PackRoles: %+v", rec)
	}

	// And back: a group on the pack's copy gets the depot's role by name.
	o.PackRoles, o.DepotRoles = false, true
	b = find(t, build(t, st, load(t, starter), o), "group", "web-builders")
	want = []pack.Diff{{Field: "prompt", Old: filepath.Join(install, "roles", "builder.md"), New: "roles/builder.md"}}
	if !reflect.DeepEqual(b.Changes, want) {
		t.Fatalf("back to DepotRoles: %+v", b)
	}

	o.PackRoles = true
	if _, err := pack.Build(ctx, st, load(t, starter), o); err == nil || !strings.Contains(err.Error(), "opposite choices") {
		t.Fatalf("both choices at once: %v", err)
	}
}

// An installed copy edited by hand is not overwritten silently: the plan
// marks it, and the import refuses it, naming the file and where the edit
// belongs, unless told to overwrite. A copy that differs only because the
// pack moved on is no edit.
func TestHandEditedInstalledFileIsRefused(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), nil)
	to := filepath.Join(o.Home, "packs", "starter", "roles", "builder.md")
	from := filepath.Join(dir, "roles", "builder.md")

	// The pack changes, the copy is as installed: an ordinary change.
	if err := os.WriteFile(from, []byte("the pack's, second edition\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, dir), o)
	if f := find(t, plan, "file", "roles/builder.md"); f.Action != pack.Change || f.Edited || len(plan.EditedFiles()) != 0 {
		t.Fatalf("builder.md after a change of the pack = %+v", f)
	}
	apply(t, st, plan, nil)

	if err := os.WriteFile(to, []byte("edited in the yard\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	if f := find(t, plan, "file", "roles/builder.md"); f.Action != pack.Change || !f.Edited {
		t.Fatalf("builder.md after a hand edit = %+v", f)
	}
	before := head(t, st)
	_, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "me"})
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), to+" was edited by hand") ||
		!strings.Contains(err.Error(), "the edit belongs in the pack's source ("+from+")") {
		t.Fatalf("applying over a hand edit: %v", err)
	}
	if text, _ := os.ReadFile(to); string(text) != "edited in the yard\n" || len(kinds(t, st, before)) != 0 {
		t.Fatalf("the refused import wrote: %q, events %v", text, kinds(t, st, before))
	}

	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "me", Overwrite: true})
	if err != nil || res.Changed != 1 {
		t.Fatalf("told to overwrite: %+v, %v", res, err)
	}
	if text, _ := os.ReadFile(to); string(text) != "the pack's, second edition\n" {
		t.Fatalf("after the overwrite: %q", text)
	}
	if got := actions(build(t, st, load(t, dir), o), pack.Change); len(got) != 0 {
		t.Fatalf("an import after the overwrite would change %v", got)
	}
}

// A hand-edited file the pack has dropped is protected from --prune too.
func TestHandEditedDroppedFileIsRefusedByPrune(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), nil)

	to := filepath.Join(o.Home, "packs", "starter", "roles", "builder.md")
	from := filepath.Join(dir, "roles", "builder.md")
	if err := os.WriteFile(to, []byte("edited in the yard\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "pack.yaml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "builders:  { agent: roles/builder.md", "builders:  { agent: roles/reviewer.md", 1))
	if err := os.WriteFile(manifest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, dir), o)
	f := find(t, plan, "file", "roles/builder.md")
	if f.Action != pack.Dropped || !f.Edited || !strings.Contains(strings.Join(plan.EditedFiles(), " "), "the edit belongs in the pack's source ("+from+")") {
		t.Fatalf("dropped README.md = %+v; edits %v", f, plan.EditedFiles())
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "me", Prune: true}); err == nil || !strings.Contains(err.Error(), to+" was edited by hand") {
		t.Fatalf("pruning the edited file: %v", err)
	}
	if text, err := os.ReadFile(to); err != nil || string(text) != "edited in the yard\n" {
		t.Fatalf("the refused prune changed the copy: %q, %v", text, err)
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "me", Prune: true, Overwrite: true}); err != nil {
		t.Fatalf("pruning with overwrite consent: %v", err)
	}
	if _, err := os.Stat(to); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the explicitly pruned copy remains: %v", err)
	}
}

// Of several flows a depot could follow, the plan asks which; the import
// remembers the answer, so the next one need not be told, and a flow the
// depot is moved off is then reported as dropped.
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
// open beads to the depot's default flow when that flow lacks their stage.
func TestPruneHoldsTypePointerThatWouldStrandBeads(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	text, err := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Add a type flow whose stage does not exist in the default flow. The
	// stage map is for tasks, which would follow that flow: it goes.
	text = []byte(strings.Replace(string(text), "      task: { new: started, review: review, merged: done }", "      bug: { new: started }", 1))
	withType := strings.Replace(string(text), "groups:\n", "  task-flow:\n    type: task\n    stages:\n      triage: { group: builders, outcomes: { done: merged } }\n      merged: { end: true }\ngroups:\n", 1)
	if withType == string(text) {
		t.Fatal("pack.yaml has no groups section")
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(withType), 0o644); err != nil {
		t.Fatal(err)
	}
	apply(t, st, build(t, st, load(t, dir), o), answers)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "in triage", Depot: "web", Type: "task", Stage: "triage"}, "me")
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
	if !slices.Contains(res.Left, "pointer depot web type task -> starter.task-flow") {
		t.Fatalf("prune result = %+v", res)
	}
	ptrs, err := st.FlowPointers(ctx)
	if err != nil || !slices.Contains(ptrs, store.FlowPointer{Depot: "web", Type: "task", Flow: "starter.task-flow"}) {
		t.Fatalf("type flow pointer after prune = %v, %v", ptrs, err)
	}
}

// A new agent group and a new crew member get the permission arguments the
// import was answered with; an empty answer is none. One the yard has keeps
// its own: the plan does not even ask.
func TestApplyGivesNewAgentsTheirArguments(t *testing.T) {
	st, o := yard(t)
	if err := st.PutGroup(ctx, store.Group{Name: "web-reviewers", Runner: "herdr", Limit: 1,
		Config: map[string]any{"kind": "codex", "args": []any{"--mine"}}}); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, copyStarter(t)), o)
	for _, q := range plan.Questions {
		if strings.HasPrefix(q.ID, "group.web-reviewers.") {
			t.Fatalf("asks %s of a group the yard has", q.ID)
		}
	}
	apply(t, st, plan, map[string]string{"group.web-builders.args": "--permission-mode  auto", "crew.yardmaster.args": ""})
	args := func(cfg map[string]any) string {
		if cfg["args"] == nil {
			return ""
		}
		return fmt.Sprint(cfg["args"])
	}
	if g, err := st.GetGroup(ctx, "web-builders"); err != nil || args(g.Config) != "[--permission-mode auto]" {
		t.Fatalf("web-builders = %+v, %v", g, err)
	}
	if g, err := st.GetGroup(ctx, "web-reviewers"); err != nil || args(g.Config) != "[--mine]" {
		t.Fatalf("web-reviewers = %+v, %v", g, err)
	}
	if r, err := st.GetCrew(ctx, "yardmaster"); err != nil || args(r.Config) != "" {
		t.Fatalf("yardmaster = %+v, %v", r, err)
	}
	// Twice is once: the arguments are the yard's now.
	plan = build(t, st, load(t, copyStarter(t)), o)
	if res := apply(t, st, plan, map[string]string{"group.web-builders.args": "--other"}); res.Wrote() {
		t.Fatalf("a second apply wrote: %+v", res)
	}
	if g, _ := st.GetGroup(ctx, "web-builders"); args(g.Config) != "[--permission-mode auto]" {
		t.Fatalf("web-builders after a second apply = %+v", g)
	}
}

// A pack's stage files are copied to where the yard reads a store flow's,
// <home>/flows/<pack>.<flow>/<stage>.md, so after the import every stage a
// bead is worked in has its file. They are recorded like the pack's other
// files: a second import changes nothing, and a copy edited by hand is not
// overwritten unasked.
func TestStageFilesGoWhereTheYardReadsThem(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	plan := build(t, st, load(t, dir), o)
	to := filepath.Join(o.Home, "flows", "starter.default", "new.md")
	f := find(t, plan, "file", "flows/default/new.md")
	if want := (pack.FileOp{From: filepath.Join(dir, "flows", "default", "new.md"), To: to, Mode: "0644", SHA256: f.File.SHA256, Fill: true, Flow: "starter.default", Stage: "new"}); f.Action != pack.Create || *f.File != want {
		t.Fatalf("new.md = %+v, file %+v\nwant %+v", f, f.File, want)
	}
	apply(t, st, plan, answers)

	flow, err := st.GetFlow(ctx, "starter.default")
	if err != nil {
		t.Fatal(err)
	}
	have, stray, err := store.HomeStageFiles(o.Home, flow)
	if err != nil || len(stray) != 0 {
		t.Fatalf("stage files: %v, stray %v", err, stray)
	}
	for _, s := range flow.Stages {
		if (have[s] != "") == flow.IsTerminal(s) {
			t.Errorf("stage %s (terminal %v) has the stage file %q", s, flow.IsTerminal(s), have[s])
		}
	}
	want, _ := os.ReadFile(filepath.Join(dir, "flows", "default", "new.md"))
	if sf, err := store.HomeStageFile(o.Home, "starter.default", "new"); err != nil || sf == nil || sf.Path != to || sf.Text != string(want) {
		t.Fatalf("the yard reads %+v, %v for stage new", sf, err)
	}
	if rec := record(t, st); !slices.ContainsFunc(rec.Objects, func(po store.PackObject) bool {
		return po.Kind == "file" && po.Name == to && po.SHA256 == f.File.SHA256
	}) {
		t.Fatalf("the record lacks %s: %+v", to, rec.Objects)
	}
	plan = build(t, st, load(t, dir), o)
	if got := len(actions(plan, pack.Unchanged)); got != len(plan.Objects) {
		t.Fatalf("a second import would create %v, change %v", actions(plan, pack.Create), actions(plan, pack.Change))
	}

	// A stage file written by hand beside them is the yard's.
	own := filepath.Join(o.Home, "flows", "starter.default", "notes.md")
	if err := os.WriteFile(own, []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("edited in the yard\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	if got := actions(plan, pack.NotInPack); !slices.Equal(got, []string{"file flows/starter.default/notes.md"}) {
		t.Fatalf("not in the pack = %v", got)
	}
	if f := find(t, plan, "file", "flows/default/new.md"); f.Action != pack.Change || !f.Edited {
		t.Fatalf("new.md after a hand edit = %+v", f)
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "me", Prune: true}); err == nil || !strings.Contains(err.Error(), to+" was edited by hand") {
		t.Fatalf("applying over a hand edit: %v", err)
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "me", Prune: true, Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if text, _ := os.ReadFile(to); string(text) != string(want) {
		t.Fatalf("after the overwrite: %q", text)
	}
	if text, _ := os.ReadFile(own); string(text) != "ours\n" {
		t.Fatalf("the import touched a stage file it did not bring: %q", text)
	}
}

// A pack that names no stage file brings none: the plan has only its roles
// and scripts, and nothing is made under the home's flows.
func TestPackWithoutStageFilesBringsNone(t *testing.T) {
	st, o := yard(t)
	plan := build(t, st, load(t, writePack(t, smallFiles(small))), o)
	var files []string
	for _, obj := range plan.Objects {
		if obj.Kind == "file" {
			files = append(files, obj.Name)
		}
	}
	if want := []string{"roles/builder.md", "roles/yardmaster.md", "scripts/land"}; !slices.Equal(files, want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	apply(t, st, plan, nil)
	if _, err := os.Stat(filepath.Join(o.Home, "flows")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the import made %s: %v", filepath.Join(o.Home, "flows"), err)
	}
}

// A stage that keeps its place in the pack and loses its file has the copy
// reported, and removed by a prune. The prune removes a file only where an
// import puts one: a record naming a file elsewhere removes nothing.
func TestPruneRemovesAStageFileThePackDropped(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), answers)
	edit(t, dir, "file: flows/default/decide.md,   ", "")
	to := filepath.Join(o.Home, "flows", "starter.default", "decide.md")

	plan := build(t, st, load(t, dir), o)
	if got := actions(plan, pack.Dropped); !slices.Equal(got, []string{"file flows/starter.default/decide.md"}) {
		t.Fatalf("dropped = %v", got)
	}
	if res := apply(t, st, plan, nil); len(res.Pruned) != 0 || !slices.Equal(res.Left, []string{"file flows/starter.default/decide.md"}) {
		t.Fatalf("without --prune: %+v", res)
	}
	if _, err := os.Stat(to); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Prune: true, Actor: "me"})
	if err != nil || !slices.Equal(res.Pruned, []string{"file flows/starter.default/decide.md"}) {
		t.Fatalf("with --prune: %+v, %v", res, err)
	}
	if _, err := os.Stat(to); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("decide.md after --prune: %v", err)
	}

	for path, want := range map[string]bool{
		filepath.Join(o.Home, "packs", "starter", "roles", "builder.md"): true,
		filepath.Join(o.Home, "flows", "starter.default", "new.md"):      true,
		// Another pack's flow, a flow made by hand, a role of the home, a
		// directory below a flow's, and no markdown.
		filepath.Join(o.Home, "flows", "other.default", "new.md"):        false,
		filepath.Join(o.Home, "flows", "default", "new.md"):              false,
		filepath.Join(o.Home, "roles", "builder.md"):                     false,
		filepath.Join(o.Home, "flows", "starter.default", "x", "new.md"): false,
		filepath.Join(o.Home, "flows", "starter.default", "new.txt"):     false,
		filepath.Join(o.Home, "flows", "starter.default.md"):             false,
	} {
		if got := plan.Removable(path); got != want {
			t.Errorf("Removable(%s) = %v, want %v", path, got, want)
		}
	}
}

// A flow the depot is moved off stays while another depot follows it, and its
// stage files stay with it: that depot's beads are still shown them.
func TestPruneHoldsTheStageFilesOfAFlowAnotherDepotFollows(t *testing.T) {
	st, o := yard(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	dir := copyStarter(t)
	edit(t, dir, "      merged:   { end: true }\n",
		"      merged:   { end: true }\n  short:\n    stages:\n      new:    { group: builders, file: flows/default/new.md, outcomes: { done: merged } }\n      merged: { end: true }\n")
	o.Flow = "default"
	apply(t, st, build(t, st, load(t, dir), o), answers)
	other := o
	other.Depot = "api"
	apply(t, st, build(t, st, load(t, dir), other), answers)

	o.Flow = "short"
	plan := build(t, st, load(t, dir), o)
	f := find(t, plan, "file", "flows/starter.default/new.md")
	if f.Action != pack.Dropped || !strings.Contains(f.Held, "flow starter.default is kept: depot api follows it") {
		t.Fatalf("new.md of the flow depot api follows = %+v", f)
	}
	// The same file of the pack, under the flow the depot follows now.
	if c := find(t, plan, "file", "flows/default/new.md"); c.Action != pack.Create || c.File.To != filepath.Join(o.Home, "flows", "starter.short", "new.md") {
		t.Fatalf("new.md of flow short = %+v, %+v", c, c.File)
	}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Prune: true, Actor: "me"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Left, "file flows/starter.default/new.md") || slices.ContainsFunc(res.Pruned, func(s string) bool { return strings.HasPrefix(s, "file ") }) {
		t.Fatalf("result = %+v", res)
	}
	if sf, err := store.HomeStageFile(o.Home, "starter.default", "new"); err != nil || sf == nil {
		t.Fatalf("depot api's stage file was pruned from under it: %+v, %v", sf, err)
	}
}

// A flow the store refuses to remove (an open bead pins it) keeps its stage
// files: the prune leaves them with the flow, for that bead.
func TestPruneLeavesTheStageFilesOfAFlowItCouldNotRemove(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	edit(t, dir, "      merged:   { end: true }\n",
		"      merged:   { end: true }\n  short:\n    stages:\n      new:    { group: builders, file: flows/default/new.md, outcomes: { done: merged } }\n      merged: { end: true }\n")
	o.Flow = "short"
	apply(t, st, build(t, st, load(t, dir), o), answers)
	if _, err := st.CreateBead(ctx, store.NewBead{Title: "pinned", Depot: "web", Stage: "new", Metadata: map[string]any{"flow": "starter.short"}}, "me"); err != nil {
		t.Fatal(err)
	}

	o.Flow = "default"
	res, err := pack.Apply(ctx, st, build(t, st, load(t, dir), o), pack.ApplyOptions{Answers: pack.Answers(answers), Prune: true, Actor: "me"})
	if err == nil || len(res.PruneErrors) != 1 || !strings.HasPrefix(res.PruneErrors[0], "flow starter.short: ") {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if !slices.Contains(res.Left, "file flows/starter.short/new.md") {
		t.Fatalf("left = %v, pruned %v", res.Left, res.Pruned)
	}
	if sf, err := store.HomeStageFile(o.Home, "starter.short", "new"); err != nil || sf == nil {
		t.Fatalf("the pinned bead's stage file was pruned: %+v, %v", sf, err)
	}
	// The record keeps both, so the next prune tries again.
	if got := actions(build(t, st, load(t, dir), o), pack.Dropped); !slices.Equal(got, []string{"file flows/starter.short/new.md", "flow starter.short"}) {
		t.Fatalf("dropped after the prune = %v", got)
	}
}

// An import fills the placeholders of the pack's roles and stage files with
// the yard's names: the operator, and for each crew member of the pack the
// yard's crew member that takes it. A script is copied as it is. The record
// holds the filled text, so a second import changes nothing and only a real
// hand edit of the filled copy is refused.
func TestImportFillsThePlaceholders(t *testing.T) {
	st, o := yard(t)
	o.Operator, o.Crew = "Jo", map[string]string{"yardmaster": "boss"}
	dir := copyStarter(t)
	script := filepath.Join(dir, "scripts", "merge-local")
	text, _ := os.ReadFile(script)
	if err := os.WriteFile(script, append(text, "# {{operator}} {{anything}}\n"...), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, dir), o)
	if want := map[string]string{"operator": "Jo", "yardmaster": "boss"}; !reflect.DeepEqual(plan.Fill, want) {
		t.Fatalf("fill = %v, want %v", plan.Fill, want)
	}
	if f := find(t, plan, "file", "roles/builder.md"); !f.File.Fill || find(t, plan, "file", "scripts/merge-local").File.Fill {
		t.Fatalf("a role is filled and a script is not: builder.md %+v", f.File)
	}
	apply(t, st, plan, answers)

	builder := filepath.Join(o.Home, "packs", "starter", "roles", "builder.md")
	got, _ := os.ReadFile(builder)
	for _, want := range []string{"never Jo's own session", "`yardr mail boss \"<bead id>: <one line>\"`"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the installed builder.md lacks %q", want)
		}
	}
	stage, _ := os.ReadFile(filepath.Join(o.Home, "flows", "starter.default", "decide.md"))
	if !strings.Contains(string(stage), "Jo decides") {
		t.Errorf("the installed decide.md does not name the operator:\n%s", stage)
	}
	for _, f := range plan.Objects {
		if f.Kind != "file" || !f.File.Fill {
			continue
		}
		if text, _ := os.ReadFile(f.File.To); strings.Contains(string(text), "{{") {
			t.Errorf("%s is installed with a placeholder left", f.File.To)
		}
	}
	if text, _ := os.ReadFile(filepath.Join(o.Home, "packs", "starter", "scripts", "merge-local")); !strings.HasSuffix(string(text), "# {{operator}} {{anything}}\n") {
		t.Error("the script was not copied as it is")
	}
	// The record holds what was installed, not the pack's text.
	for _, po := range record(t, st).Objects {
		if po.Kind == "file" && po.Name == builder && po.SHA256 != find(t, plan, "file", "roles/builder.md").File.SHA256 {
			t.Errorf("the record of builder.md has %s, the plan installed %s", po.SHA256, find(t, plan, "file", "roles/builder.md").File.SHA256)
		}
	}

	// Again: nothing to write, and nothing marked edited.
	again := build(t, st, load(t, dir), pack.Options{Depot: o.Depot, Home: o.Home, Operator: "Jo"})
	if got := append(actions(again, pack.Create), actions(again, pack.Change)...); len(got) != 0 || len(again.EditedFiles()) != 0 {
		t.Fatalf("a second import would write %v, edited %v", got, again.EditedFiles())
	}
	if res := apply(t, st, again, nil); res.Wrote() {
		t.Fatalf("the second import wrote: %+v", res)
	}

	// Another crew member takes yardmaster: the files that name it change, and
	// that is no hand edit.
	o.Crew = map[string]string{"yardmaster": "chief"}
	moved := build(t, st, load(t, dir), o)
	if f := find(t, moved, "file", "roles/builder.md"); f.Action != pack.Change || f.Edited {
		t.Fatalf("builder.md under another crew member = %+v", f)
	}
	if f := find(t, moved, "file", "roles/yardmaster.md"); f.Action != pack.Unchanged {
		t.Fatalf("yardmaster.md names no crew member, yet = %+v", f)
	}
	if len(moved.EditedFiles()) != 0 {
		t.Fatalf("a changed mapping counts as a hand edit: %v", moved.EditedFiles())
	}

	// A hand edit of the filled copy is still refused.
	if err := os.WriteFile(builder, append(got, "edited in the yard\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	edited := build(t, st, load(t, dir), pack.Options{Depot: o.Depot, Home: o.Home, Operator: "Jo"})
	if f := find(t, edited, "file", "roles/builder.md"); f.Action != pack.Change || !f.Edited {
		t.Fatalf("builder.md after a hand edit = %+v", f)
	}
	if _, err := pack.Apply(ctx, st, edited, pack.ApplyOptions{Actor: "me"}); err == nil || !strings.Contains(err.Error(), builder+" was edited by hand") {
		t.Fatalf("applying over a hand edit of a filled copy: %v", err)
	}
}

// Without an operator given, the yard's operator setting is who the files
// name; a yard without one gets "the operator".
func TestOperatorIsTheYardsSetting(t *testing.T) {
	st, o := yard(t)
	if got := build(t, st, load(t, starter), o).Fill["operator"]; got != "the operator" {
		t.Fatalf("operator in a yard without the setting = %q", got)
	}
	if err := st.SetSetting(ctx, "operator", "Jo", "me"); err != nil {
		t.Fatal(err)
	}
	if got := build(t, st, load(t, starter), o).Fill["operator"]; got != "Jo" {
		t.Fatalf("operator = %q, want the setting", got)
	}
}

// What is installed is what the plan was made from: a source that changed
// since is refused, compared as filled.
func TestFilledFileChangedSinceThePlanIsRefused(t *testing.T) {
	st, o := yard(t)
	dir := copyStarter(t)
	plan := build(t, st, load(t, dir), o)
	from := filepath.Join(dir, "roles", "builder.md")
	if err := os.WriteFile(from, []byte("another {{operator}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := pack.Answers{}
	for _, q := range plan.Questions {
		a[q.ID] = "claude"
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, Actor: "me"}); err == nil || !strings.Contains(err.Error(), from+" changed since the plan was made") {
		t.Fatalf("applying a plan whose source changed: %v", err)
	}
}

// The starter pack's crew member was called mayor until it became yardmaster.
// For this release what was written with the old name still means that
// crew member: --crew mayor=<name>, the record of an import made before,
// and {{mayor}} in a role a yard edited. A pack that still has a crew member
// mayor of its own is read as it stands.
func TestThePacksMayorIsItsYardmaster(t *testing.T) {
	st, o := yard(t)
	o.Operator, o.Crew = "Jo", map[string]string{"mayor": "boss"}
	dir := copyStarter(t)
	builder := filepath.Join(dir, "roles", "builder.md")
	text, _ := os.ReadFile(builder)
	if err := os.WriteFile(builder, append(text, "Or ask {{mayor}}.\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, dir), o)
	if want := map[string]string{"yardmaster": "boss"}; !reflect.DeepEqual(plan.Crew, want) {
		t.Fatalf("crew members = %v, want %v", plan.Crew, want)
	}
	apply(t, st, plan, answers)
	got, _ := os.ReadFile(filepath.Join(o.Home, "packs", "starter", "roles", "builder.md"))
	for _, want := range []string{"`yardr mail boss \"<bead id>: <one line>\"`", "Or ask boss.\n"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the installed builder.md lacks %q", want)
		}
	}

	// The record as an import before the rename wrote it.
	rec := record(t, st)
	rec.Crew = map[string]string{"mayor": "boss"}
	if _, err := st.RecordPack(ctx, rec, true, "me"); err != nil {
		t.Fatal(err)
	}
	again := build(t, st, load(t, dir), pack.Options{Depot: o.Depot, Home: o.Home, Operator: "Jo"})
	if want := map[string]string{"yardmaster": "boss"}; !reflect.DeepEqual(again.Crew, want) {
		t.Fatalf("crew members from the old record = %v, want %v", again.Crew, want)
	}
	if got := append(actions(again, pack.Create), actions(again, pack.Change)...); len(got) != 0 {
		t.Fatalf("an import over the old record would write %v", got)
	}
	for _, n := range again.Notes {
		if strings.Contains(n, "no longer does") {
			t.Errorf("the old record reads as another crew member: %s", n)
		}
	}

	// A name the pack never had is still refused.
	o.Crew = map[string]string{"sheriff": "boss"}
	if _, err := pack.Build(ctx, st, load(t, dir), o); err == nil || !strings.Contains(err.Error(), "the pack has no crew member sheriff") {
		t.Fatalf("--crew sheriff=boss: %v", err)
	}

	// A pack of a yard's own that still calls its crew member mayor.
	manifest := filepath.Join(dir, "pack.yaml")
	text, _ = os.ReadFile(manifest)
	own := strings.NewReplacer("  yardmaster:\n", "  mayor:\n", "as: yardmaster", "as: mayor").Replace(string(text))
	if err := os.WriteFile(manifest, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pack.LoadDir(dir); err == nil || !strings.Contains(err.Error(), "placeholder {{yardmaster}}") {
		t.Fatalf("a pack with a crew member mayor fills {{yardmaster}}: %v", err)
	}
}

// The starter pack's group that lands approved beads was called refinery
// until it became assembly. A yard's group keeps its name: one an import
// made before the rename, <depot>-refinery, stays the pack's group, with its
// route, and no <depot>-assembly is made beside it. The record then says so
// under the new name, which is what a later import goes by.
func TestThePacksRefineryIsItsAssembly(t *testing.T) {
	st, o := yard(t)
	// The pack as it was: the same files, the group under its old name.
	before := copyStarter(t)
	manifest := filepath.Join(before, "pack.yaml")
	text, _ := os.ReadFile(manifest)
	was := strings.NewReplacer("group: assembly, ", "group: refinery, ", "  assembly:  {", "  refinery:  {").Replace(string(text))
	if was == string(text) {
		t.Fatal("the starter pack has no group assembly to call refinery")
	}
	if err := os.WriteFile(manifest, []byte(was), 0o644); err != nil {
		t.Fatal(err)
	}
	// A pack that calls its group refinery is read as it stands.
	apply(t, st, build(t, st, load(t, before), o), answers)
	if rec := record(t, st); rec.Groups["refinery"] != "web-refinery" {
		t.Fatalf("the import before the rename recorded %v", rec.Groups)
	}

	plan := build(t, st, load(t, starter), o)
	if got := plan.Groups["assembly"]; got != "web-refinery" {
		t.Fatalf("the pack's assembly is the yard's %q, want web-refinery", got)
	}
	if got := slices.Concat(actions(plan, pack.Create), actions(plan, pack.Change), actions(plan, pack.NotInPack)); len(got) != 0 {
		t.Fatalf("an import over the yard of before would write or drop %v", got)
	}
	if rt := find(t, plan, "route", "depot web stage approved -> web-refinery"); rt.Action != pack.Unchanged {
		t.Fatalf("route approved = %+v", rt)
	}
	apply(t, st, plan, nil)
	if g, _ := st.GetGroup(ctx, "web-assembly"); g != nil {
		t.Fatalf("a second group was made: %+v", g)
	}
	rec := record(t, st)
	if _, old := rec.Groups["refinery"]; old || rec.Groups["assembly"] != "web-refinery" {
		t.Fatalf("the record after the import = %v", rec.Groups)
	}
	// By the record alone, as the release that no longer knows the old name
	// will read it: a group of the new name made since changes nothing.
	if err := st.PutGroup(ctx, store.Group{Name: "web-assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "/other"}}); err != nil {
		t.Fatal(err)
	}
	if got := build(t, st, load(t, starter), o).Groups["assembly"]; got != "web-refinery" {
		t.Fatalf("with a record and both groups the pack's assembly is %q", got)
	}
}

// A yard set up by hand under the old name, with no import behind it: the
// group <depot>-refinery is taken for the pack's assembly, unless the yard
// has a <depot>-assembly too.
func TestAHandMadeRefineryIsThePacksAssembly(t *testing.T) {
	st, o := yard(t)
	depot := "web"
	if err := st.PutGroup(ctx, store.Group{Name: "web-refinery", Runner: "exec", Limit: 1,
		Config: map[string]any{"cmd": "YARDR_PUSH=origin /old/merge-local", "log": "/logs/refinery.log"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRoute(ctx, store.Route{Stage: "approved", Depot: &depot, Group: "web-refinery"}); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, starter), o)
	g := find(t, plan, "group", "web-refinery")
	cmd := "YARDR_PUSH=origin " + filepath.Join(o.Home, "packs", "starter", "scripts", "merge-local")
	if g.Action != pack.Change || g.Group.Config["cmd"] != cmd || g.Group.Config["log"] != "/logs/refinery.log" {
		t.Fatalf("web-refinery = %+v %+v", g, g.Group)
	}
	if rt := find(t, plan, "route", "depot web stage approved -> web-refinery"); rt.Action != pack.Unchanged {
		t.Fatalf("route approved = %+v", rt)
	}
	for _, o := range plan.Objects {
		if strings.Contains(o.Name, "web-assembly") {
			t.Errorf("%s %s is planned beside the yard's group", o.Kind, o.Name)
		}
	}

	if err := st.PutGroup(ctx, store.Group{Name: "web-assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "/new/merge-local"}}); err != nil {
		t.Fatal(err)
	}
	if got := build(t, st, load(t, starter), o).Groups["assembly"]; got != "web-assembly" {
		t.Fatalf("with both groups and no record the pack's assembly is %q", got)
	}
}

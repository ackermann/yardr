package pack_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// starterMap is the stage map the starter pack carries for linear.
var starterMap = map[string]string{"task:new": "started", "task:review": "review", "task:merged": "done"}

// smallSources is small with a stage map for linear, which the refusal
// cases each break in one place.
const smallSources = small + `sources:
  linear:
    as: yardmaster
    hears:
      task: { new: started, review: review, merged: done }
`

// The starter pack carries the block for linear, and every stage it names
// is one of the flow a task follows.
func TestStarterCarriesTheLinearStageMap(t *testing.T) {
	p := load(t, starter)
	if len(p.Sources) != 1 || p.Sources[0].Source != "linear" || p.Sources[0].As != "yardmaster" || !maps.Equal(p.Sources[0].Map, starterMap) {
		t.Fatalf("sources = %+v", p.Sources)
	}
	for key := range p.Sources[0].Map {
		_, stage, _ := strings.Cut(key, ":")
		if !slices.ContainsFunc(p.Flows[0].Stages, func(s pack.Stage) bool { return s.Name == stage }) {
			t.Errorf("%s: flow %s has no such stage", key, p.Flows[0].Name)
		}
	}
}

// A map is read per bead type: a type with a flow of its own is held to
// that flow's stages, any other to a flow a depot could follow. A stage left
// out is no line of the map.
func TestStageMapIsReadPerType(t *testing.T) {
	text := strings.Replace(smallSources, "groups:\n", "  trains:\n    type: train\n    stages:\n      open: { group: builders, outcomes: { done: done } }\n      done: { end: true }\ngroups:\n", 1)
	text = strings.Replace(text, "      task: { new: started, review: review, merged: done }\n", "      task: { review: review }\n      train: { open: started, done: done }\n      bug: { new: started }\n", 1)
	p := load(t, writePack(t, smallFiles(text)))
	want := map[string]string{"task:review": "review", "train:open": "started", "train:done": "done", "bug:new": "started"}
	if len(p.Sources) != 1 || !maps.Equal(p.Sources[0].Map, want) {
		t.Fatalf("sources = %+v, want %v", p.Sources, want)
	}
}

// A pack from before the types were renamed says epic where a flow is for a
// type and where a source's map names one: it is read as train.
func TestPackReadsTheOldTypeWords(t *testing.T) {
	text := strings.Replace(smallSources, "groups:\n", "  epics:\n    type: epic\n    stages:\n      open: { group: builders, outcomes: { done: done } }\n      done: { end: true }\ngroups:\n", 1)
	text = strings.Replace(text, "      task: { new: started, review: review, merged: done }\n", "      task: { review: review }\n      epic: { open: started, done: done }\n", 1)
	p := load(t, writePack(t, smallFiles(text)))
	want := map[string]string{"task:review": "review", "train:open": "started", "train:done": "done"}
	if len(p.Sources) != 1 || !maps.Equal(p.Sources[0].Map, want) {
		t.Fatalf("sources = %+v, want %v", p.Sources, want)
	}
	i := slices.IndexFunc(p.Flows, func(f pack.Flow) bool { return f.Name == "epics" })
	if i < 0 || p.Flows[i].Type != store.TypeTrain {
		t.Fatalf("flows = %+v, want epics for type train", p.Flows)
	}
}

func TestStageMapRefusals(t *testing.T) {
	const trains = "  trains:\n    type: train\n    stages:\n      open: { group: builders, outcomes: { done: done } }\n      done: { end: true }\ngroups:\n"
	cases := []struct {
		name          string
		pairs         []string // replacements in smallSources
		key, msg      string
		line          int
		alsoMentioned string
	}{
		{name: "source without an adapter", pairs: []string{"  linear:\n    as", "  slack:\n    as"}, key: "sources.slack", msg: "no adapter for source slack", line: 23},
		{name: "source yardr cannot set a status in", pairs: []string{"  linear:\n    as", "  token:\n    as"}, key: "sources.token", msg: "yardr cannot set a status in token (it can in ", line: 23, alsoMentioned: "linear"},
		{name: "as naming no crew member of the pack", pairs: []string{"as: yardmaster", "as: boss"}, key: "sources.linear.as", msg: "no crew member boss is declared under crew", line: 24},
		{name: "type no flow of the pack is for", pairs: []string{"  default:\n", "  default:\n    type: train\n"}, key: "sources.linear.hears.task", msg: "no flow of this pack is for beads of type task", line: 27},
		{name: "stage the flow lacks", pairs: []string{"merged: done }\n", "merged: done, approved: done }\n"}, key: "sources.linear.hears.task.approved", msg: "flow default has no stage approved (stages: new, review, decide, merged)", line: 26},
		{name: "stage the type's own flow lacks", pairs: []string{"groups:\n", trains, "      task: { new: started, review: review, merged: done }\n", "      train: { new: started }\n"},
			key: "sources.linear.hears.train.new", msg: "flow trains has no stage new (stages: open, done)", line: 31},
		{name: "unknown word", pairs: []string{"review: review, merged", "review: reviewing, merged"}, key: "sources.linear.hears.task.review", msg: `"reviewing" is no state: one of started, review, done`, line: 26},
		{name: "status by its name in the source", pairs: []string{"merged: done }", `merged: "Done" }`}, key: "sources.linear.hears.task.merged", msg: `"Done" is no state`, line: 26},
		{name: "no as", pairs: []string{"    as: yardmaster\n", ""}, key: "sources.linear.as", msg: "missing", line: 23},
		{name: "no hears", pairs: []string{"    hears:\n      task: { new: started, review: review, merged: done }\n", ""}, key: "sources.linear.hears", msg: "missing", line: 23},
		{name: "empty hears", pairs: []string{"    hears:\n      task: { new: started, review: review, merged: done }\n", "    hears: {}\n"}, key: "sources.linear.hears", msg: "empty", line: 25},
		{name: "unknown key", pairs: []string{"    as: yardmaster\n", "    as: yardmaster\n    comment: true\n"}, key: "sources.linear.comment", msg: "unknown key", line: 25},
		{name: "type given twice", pairs: []string{"merged: done }\n", "merged: done }\n      task: { new: done }\n"}, key: "sources.linear.hears.task", msg: "given twice", line: 27},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := smallSources
			for i := 0; i < len(c.pairs); i += 2 {
				if !strings.Contains(text, c.pairs[i]) {
					t.Fatalf("the pack lacks %q", c.pairs[i])
				}
				text = strings.Replace(text, c.pairs[i], c.pairs[i+1], 1)
			}
			dir := writePack(t, smallFiles(text))
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) || len(errs) != 1 {
				t.Fatalf("errors = %v, want one", err)
			}
			e := errs[0]
			if e.File != filepath.Join(dir, "pack.yaml") || e.Key != c.key || e.Line != c.line || !strings.Contains(e.Msg, c.msg) || !strings.Contains(e.Msg, c.alsoMentioned) {
				t.Fatalf("error = %s\nwant key %s, line %d, message containing %q", e, c.key, c.line, c.msg)
			}
		})
	}
}

// A source that has no status for a word refuses it in a pack as it does
// on yardr hook add --map.
func TestStageMapWordTheSourceHasNoStatusFor(t *testing.T) {
	text := strings.Replace(smallSources, "  linear:\n    as", "  github:\n    as", 1)
	_, err := pack.LoadDir(writePack(t, smallFiles(text)))
	var errs pack.Errors
	if !errors.As(err, &errs) || len(errs) == 0 {
		t.Fatalf("a github map with review loads: %v", err)
	}
	for _, e := range errs {
		if !strings.HasPrefix(e.Key, "sources.github.hears.task.") {
			t.Errorf("error = %s", e)
		}
	}
	// What github can be told is accepted.
	text = strings.Replace(text, "task: { new: started, review: review, merged: done }", "task: { merged: done }", 1)
	if p := load(t, writePack(t, smallFiles(text))); !maps.Equal(p.Sources[0].Map, map[string]string{"task:merged": "done"}) {
		t.Fatalf("sources = %+v", p.Sources)
	}
}

// crew member makes a crew member of the yard; with identified it has an
// identity in linear, which a hook that acts as it needs.
func member(t *testing.T, st store.API, name string, identified bool, config map[string]any) {
	t.Helper()
	if config == nil {
		config = map[string]any{}
	}
	config["kind"] = "claude"
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr", Dir: t.TempDir(), Config: config}, "me"); err != nil {
		t.Fatal(err)
	}
	if identified {
		if err := st.SetIdentity(ctx, name, "linear", store.Identity{ID: "user-" + name}, "me"); err != nil {
			t.Fatal(err)
		}
	}
}

func hook(t *testing.T, st store.API, h store.Hook) {
	t.Helper()
	if h.SecretFile == "" {
		h.SecretFile = "/yard/linear.secret"
	}
	if _, err := st.PutHook(ctx, h, "me"); err != nil {
		t.Fatal(err)
	}
}

func getHook(t *testing.T, st store.API, name string) store.Hook {
	t.Helper()
	h, err := st.GetHook(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return *h
}

func mapPlan(t *testing.T, p *pack.Plan, source string) pack.MapPlan {
	t.Helper()
	i := slices.IndexFunc(p.Maps, func(m pack.MapPlan) bool { return m.Source == source })
	if i < 0 {
		t.Fatalf("the plan has no stage map for %s: %+v", source, p.Maps)
	}
	return p.Maps[i]
}

func need(t *testing.T, m pack.MapPlan, what string) pack.Need {
	t.Helper()
	i := slices.IndexFunc(m.Needs, func(n pack.Need) bool { return n.What == what })
	if i < 0 {
		t.Fatalf("the stage map of %s says nothing of %s: %+v", m.Source, what, m.Needs)
	}
	return m.Needs[i]
}

func mapObjects(p *pack.Plan) []string {
	var out []string
	for _, o := range p.Objects {
		if o.Kind == "map" {
			out = append(out, o.Action+" "+o.Name)
		}
	}
	return out
}

// Into a yard without a hook of the source the import goes through and sets
// nothing for it: the plan names the hook to add, the identity and the
// grant. It makes no hook. Once the yard has the hook, the next apply sets
// the map and the crew member on it, and the one after changes nothing.
func TestStageMapWaitsForTheYardsHook(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, nil)
	p := load(t, starter)

	plan := build(t, st, p, o)
	m := mapPlan(t, plan, "linear")
	if m.Pack != "yardmaster" || m.As != "boss" || !maps.Equal(m.Map, starterMap) || m.Hook != "" || m.Action != pack.NotSet || len(mapObjects(plan)) != 0 {
		t.Fatalf("into a yard without a hook: %+v, objects %v", m, mapObjects(plan))
	}
	if n := need(t, m, "hook"); n.Status != "missing" || !strings.Contains(n.Detail, "yardr hook add linear --source linear --secret-file <file>") || !strings.Contains(n.Detail, "the import makes none") {
		t.Fatalf("hook: %+v", n)
	}
	if n := need(t, m, "identity"); n.Status != "ok" {
		t.Fatalf("identity: %+v", n)
	}
	if n := need(t, m, store.SetState); n.Status != "missing" || !strings.Contains(n.Detail, "The import does not grant it: the operator allows it with yardr crew set boss set_state=linear") {
		t.Fatalf("set_state: %+v", n)
	}
	res := apply(t, st, plan, nil)
	if hs, _ := st.Hooks(ctx); len(hs) != 0 || len(res.Mapped) != 0 || !res.Recorded {
		t.Fatalf("the import made a hook or set a map: hooks %+v, result %+v", hs, res)
	}

	hook(t, st, store.Hook{Name: "lin", Source: "linear"})
	plan = build(t, st, p, o)
	m = mapPlan(t, plan, "linear")
	if m.Hook != "lin" || m.Action != pack.Create || m.Was != nil || !slices.Equal(mapObjects(plan), []string{"create hook lin as boss"}) {
		t.Fatalf("with a hook: %+v, objects %v", m, mapObjects(plan))
	}
	if got := actions(plan, pack.Create); !slices.Equal(got, []string{"map hook lin as boss"}) {
		t.Fatalf("the plan would create %v, want the map alone", got)
	}
	res = apply(t, st, plan, nil)
	h := getHook(t, st, "lin")
	if h.As != "boss" || !maps.Equal(h.Map, starterMap) || h.SecretFile != "/yard/linear.secret" || h.Source != "linear" {
		t.Fatalf("hook = %+v", h)
	}
	if len(res.Mapped) != 1 || res.Mapped[0].Hook != "lin" || res.Mapped[0].As != "boss" || res.Created != 1 || !res.Recorded {
		t.Fatalf("result = %+v", res)
	}
	// It set the map and no more: the crew member may still not set a status.
	if r, _ := st.GetCrew(ctx, "boss"); len(r.MaySetState()) != 0 {
		t.Fatalf("the import let boss set a status in %v", r.MaySetState())
	}
	rec := record(t, st)
	if !slices.ContainsFunc(rec.Objects, func(po store.PackObject) bool {
		return po.Kind == "map" && po.Name == "lin" && po.CrewMember == "boss" && maps.Equal(po.Map, starterMap)
	}) {
		t.Fatalf("the record lacks the map: %+v", rec.Objects)
	}

	// Twice is once.
	plan = build(t, st, p, o)
	if m = mapPlan(t, plan, "linear"); m.Action != pack.Unchanged || len(actions(plan, pack.Create))+len(actions(plan, pack.Change)) != 0 {
		t.Fatalf("second plan: %+v, creates %v, changes %v", m, actions(plan, pack.Create), actions(plan, pack.Change))
	}
	at := head(t, st)
	if res = apply(t, st, plan, nil); res.Wrote() || res.Recorded || len(res.Mapped) != 0 {
		t.Fatalf("second apply = %+v", res)
	}
	if got := kinds(t, st, at); !slices.Equal(got, []string{"pack_unchanged"}) {
		t.Fatalf("second apply logged %v, want pack_unchanged alone", got)
	}
}

// A hook that has another map gets the pack's, and the plan shows both.
func TestStageMapReplacesTheHooksAndThePlanShowsBoth(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, map[string]any{store.SetState: "linear"})
	member(t, st, "clerk", true, nil)
	old := map[string]string{"task:review": "done", "train:open": "started"}
	hook(t, st, store.Hook{Name: "linear", Source: "linear", As: "clerk", Map: old})

	plan := build(t, st, load(t, starter), o)
	m := mapPlan(t, plan, "linear")
	if m.Action != pack.Change || m.Was == nil || m.Was.As != "clerk" || !maps.Equal(m.Was.Map, old) || !maps.Equal(m.Map, starterMap) {
		t.Fatalf("map plan = %+v (was %+v)", m, m.Was)
	}
	if n := need(t, m, store.SetState); n.Status != "ok" {
		t.Fatalf("set_state: %+v", n)
	}
	obj := find(t, plan, "map", "hook linear as boss")
	want := []pack.Diff{
		{Field: "as", Old: "clerk", New: "boss"},
		{Field: "map", Old: "task:review=done train:open=started", New: "task:merged=done task:new=started task:review=review"},
	}
	if obj.Action != pack.Change || !slices.Equal(obj.Changes, want) {
		t.Fatalf("object = %+v", obj)
	}
	res := apply(t, st, plan, nil)
	if h := getHook(t, st, "linear"); h.As != "boss" || !maps.Equal(h.Map, starterMap) {
		t.Fatalf("hook = %+v", h)
	}
	if len(res.Mapped) != 1 {
		t.Fatalf("result = %+v", res)
	}
	// Who takes the pack's crew member is the import's to say: another one
	// changes whom the hook acts as, and the map stays.
	o.Crew = map[string]string{"yardmaster": "clerk"}
	plan = build(t, st, load(t, starter), o)
	if obj := find(t, plan, "map", "hook linear as clerk"); obj.Action != pack.Change || !slices.Equal(obj.Changes, []pack.Diff{{Field: "as", Old: "boss", New: "clerk"}}) {
		t.Fatalf("under --crew yardmaster=clerk: %+v", obj)
	}
}

// A hook acts only as a crew member with an identity in its source, so for one
// without, the import sets nothing and names the command; a crew member the
// import itself makes has none yet.
func TestStageMapNeedsTheCrewIdentity(t *testing.T) {
	st, o := yard(t)
	hook(t, st, store.Hook{Name: "linear", Source: "linear"})
	p := load(t, starter)

	plan := build(t, st, p, o)
	m := mapPlan(t, plan, "linear")
	if m.As != "yardmaster" || m.Action != pack.NotSet || len(mapObjects(plan)) != 0 {
		t.Fatalf("for a crew member the import makes: %+v", m)
	}
	if n := need(t, m, "identity"); n.Status != "missing" || !strings.Contains(n.Detail, "yardr crew identity yardmaster --source linear --id <its id there>") {
		t.Fatalf("identity: %+v", n)
	}
	if n := need(t, m, "hook"); n.Status != "ok" {
		t.Fatalf("hook: %+v", n)
	}
	apply(t, st, plan, nil)
	if h := getHook(t, st, "linear"); h.As != "" || len(h.Map) != 0 {
		t.Fatalf("hook = %+v", h)
	}

	if err := st.SetIdentity(ctx, "yardmaster", "linear", store.Identity{ID: "u-1"}, "me"); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, p, o)
	if m = mapPlan(t, plan, "linear"); m.Action != pack.Create {
		t.Fatalf("with an identity: %+v", m)
	}
	apply(t, st, plan, nil)
	if h := getHook(t, st, "linear"); h.As != "yardmaster" || !maps.Equal(h.Map, starterMap) {
		t.Fatalf("hook = %+v", h)
	}
}

// A map the import was told not to set (the run is not the operator's) is
// neither written nor recorded, and the rest of the pack is imported. The
// operator's next apply sets it.
func TestStageMapRefusedLeavesTheRestOfTheImport(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, nil)
	hook(t, st, store.Hook{Name: "linear", Source: "linear"})
	p := load(t, starter)

	plan := build(t, st, p, o)
	a := pack.Answers{}
	for _, q := range plan.Questions {
		a[q.ID] = map[string]string{"kind": "claude", "dir": t.TempDir()}[q.Field]
	}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, MapRefused: map[string]string{"linear": "not the operator"}, Actor: "builder"})
	if err != nil {
		t.Fatal(err)
	}
	if h := getHook(t, st, "linear"); h.As != "" || len(h.Map) != 0 || len(res.Mapped) != 0 {
		t.Fatalf("a refused map was set: %+v, %+v", h, res)
	}
	if _, err := st.GetGroup(ctx, "web-builders"); err != nil || !res.Recorded {
		t.Fatalf("the rest was not imported: %v, %+v", err, res)
	}
	if rec := record(t, st); slices.ContainsFunc(rec.Objects, func(po store.PackObject) bool { return po.Kind == "map" }) {
		t.Fatalf("a refused map is on record: %+v", rec.Objects)
	}

	plan = build(t, st, p, o)
	if got := actions(plan, pack.Create); !slices.Equal(got, []string{"map hook linear as boss"}) {
		t.Fatalf("the next plan would create %v", got)
	}
	apply(t, st, plan, nil)
	if h := getHook(t, st, "linear"); h.As != "boss" || !maps.Equal(h.Map, starterMap) {
		t.Fatalf("hook = %+v", h)
	}
}

// A pack that drops its block leaves the map on the hook unless told to
// prune, as with its other objects; a prune clears the map and the crew member
// and keeps the hook. A map changed by hand since the import is the yard's.
func TestDroppedStageMapIsPrunedOnlyWhenAsked(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, nil)
	hook(t, st, store.Hook{Name: "linear", Source: "linear"})
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), nil)
	if h := getHook(t, st, "linear"); h.As != "boss" {
		t.Fatalf("hook = %+v", h)
	}
	edit(t, dir, "sources:\n  linear:\n    as: yardmaster\n    hears:\n      task: { new: started, review: review, merged: done }\n", "")

	plan := build(t, st, load(t, dir), o)
	if got := actions(plan, pack.Dropped); !slices.Equal(got, []string{"map hook linear as boss"}) || len(plan.Maps) != 0 {
		t.Fatalf("dropped = %v, maps %+v", got, plan.Maps)
	}
	res := apply(t, st, plan, nil)
	if h := getHook(t, st, "linear"); h.As != "boss" || !maps.Equal(h.Map, starterMap) || !slices.Equal(res.Left, []string{"map hook linear as boss"}) {
		t.Fatalf("without --prune: hook %+v, left %v", h, res.Left)
	}

	plan = build(t, st, load(t, dir), o)
	a := pack.Answers{}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, Prune: true, Actor: "me"})
	if err != nil {
		t.Fatal(err)
	}
	h := getHook(t, st, "linear")
	if h.As != "" || len(h.Map) != 0 || h.Source != "linear" || h.SecretFile != "/yard/linear.secret" || !slices.Equal(res.Pruned, []string{"map hook linear as boss"}) {
		t.Fatalf("with --prune: hook %+v, pruned %v", h, res.Pruned)
	}
	if got := actions(build(t, st, load(t, dir), o), pack.Dropped); len(got) != 0 {
		t.Fatalf("after the prune, dropped = %v", got)
	}

	// Set by hand since the import: not the pack's to remove.
	st2, o2 := yard(t)
	o2.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st2, "boss", true, nil)
	hook(t, st2, store.Hook{Name: "linear", Source: "linear"})
	dir = copyStarter(t)
	apply(t, st2, build(t, st2, load(t, dir), o2), nil)
	hook(t, st2, store.Hook{Name: "linear", Source: "linear", As: "boss", Map: map[string]string{"task:new": "done"}})
	edit(t, dir, "sources:\n  linear:\n    as: yardmaster\n    hears:\n      task: { new: started, review: review, merged: done }\n", "")
	if got := actions(build(t, st2, load(t, dir), o2), pack.Dropped); len(got) != 0 {
		t.Fatalf("a map set by hand is dropped: %v", got)
	}
}

// With several hooks of one source the map goes on one, and the plan says
// which: every hook with a map would move the issue.
func TestStageMapGoesOnOneHookOfTheSource(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, nil)
	hook(t, st, store.Hook{Name: "b-linear", Source: "linear"})
	hook(t, st, store.Hook{Name: "a-linear", Source: "linear"})
	plan := build(t, st, load(t, starter), o)
	if m := mapPlan(t, plan, "linear"); m.Hook != "a-linear" {
		t.Fatalf("hook = %s", m.Hook)
	}
	if !slices.ContainsFunc(plan.Notes, func(n string) bool {
		return strings.Contains(n, "several hooks of source linear (a-linear, b-linear): the pack's stage map goes on a-linear alone")
	}) {
		t.Fatalf("notes = %v", plan.Notes)
	}
	apply(t, st, plan, nil)
	if h := getHook(t, st, "b-linear"); h.As != "" || len(h.Map) != 0 {
		t.Fatalf("the other hook got a map: %+v", h)
	}
}

// Building a plan sets nothing on the hook.
func TestStageMapPlanWritesNothing(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, nil)
	hook(t, st, store.Hook{Name: "linear", Source: "linear"})
	at := head(t, st)
	build(t, st, load(t, starter), o)
	if got := kinds(t, st, at); len(got) != 0 {
		t.Fatalf("a plan logged %v", got)
	}
	if h := getHook(t, st, "linear"); h.As != "" || len(h.Map) != 0 {
		t.Fatalf("hook = %+v", h)
	}
}

// A pack from before the crew had the name declares it under residents and
// lists the kinds a crew member hears of as that release logged them: it is
// read as crew, and the kinds as new events carry them.
func TestPackReadsTheOldCrewWords(t *testing.T) {
	dir := copyStarter(t)
	file := filepath.Join(dir, "pack.yaml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(b), "\ncrew:\n", "\nresidents:\n", 1)
	text = strings.Replace(text, "crew_gave_up", "resident_gave_up", 1)
	if text == string(b) || strings.Contains(text, "crew_gave_up") {
		t.Fatal("the starter pack has no crew: with crew_gave_up to say in the old words")
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	p := load(t, dir)
	if len(p.Crew) != 1 || p.Crew[0].Name != "yardmaster" {
		t.Fatalf("crew = %+v", p.Crew)
	}
	if ev := p.Crew[0].Events; !slices.Contains(ev, "crew_gave_up") || slices.Contains(ev, "resident_gave_up") {
		t.Fatalf("events = %v, want crew_gave_up for resident_gave_up", ev)
	}
}

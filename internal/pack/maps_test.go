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
var starterMap = map[string]string{"task:new": "started", "task:review": "review", "task:review-security": "review", "task:merged": "done"}

// smallSources is small with a stage map for linear, which the refusal
// cases each break in one place.
const smallSources = small + `sources:
  linear:
    as: yardmaster
    hears:
      task: { new: started, review: review, merged: done }
`

// flowlessSources is a pack with no flows, groups or crew, carrying sources
// lines alone for the depot's own flow, as ci-red does.
const flowlessSources = `name: flowless
kind: git
sources:
  github:
    makes:
      - { where: "event=workflow_run conclusion=failure head_branch=main", bead: { type: task, stage: new } }
    says:
      - { where: "event=workflow_run conclusion=success", outcome: done }
`

// A pack with no flows at all carries sources lines for the depot's own
// flow: they load instead of being refused, and the plan holds them to the
// flow the depot follows. A pack that has flows but none for the type is
// still refused (TestStageMapRefusals).
func TestSourcesWithoutFlowsLoad(t *testing.T) {
	p := load(t, writePack(t, map[string]string{"pack.yaml": flowlessSources}))
	if !p.YardWide() || len(p.Sources) != 1 {
		t.Fatalf("yard wide = %v, sources = %+v", p.YardWide(), p.Sources)
	}
	m := p.Sources[0]
	if m.Source != "github" || len(m.Makes) != 1 || len(m.Says) != 1 {
		t.Fatalf("source = %+v", m)
	}
	if m.Makes[0].Type != "task" || m.Makes[0].Stage != "new" {
		t.Fatalf("makes = %+v", m.Makes)
	}
	if m.Says[0].Outcome != "done" {
		t.Fatalf("says = %+v", m.Says)
	}
	// hears with as loads too, naming a crew member of the pack's, which
	// hears no stages of a flow the pack has none of.
	heard := load(t, writePack(t, map[string]string{"pack.yaml": `name: flowless-hears
kind: git
crew:
  yardmaster:
    hears:
      events: [held]
      done: true
sources:
  github:
    as: yardmaster
    hears:
      task: { new: started }
`}))
	if heard.YardWide() {
		t.Fatalf("a pack with a crew member is yard-wide: %+v", heard.Sources)
	}
	if len(heard.Sources) != 1 || !maps.Equal(heard.Sources[0].Map, map[string]string{"task:new": "started"}) {
		t.Fatalf("sources = %+v", heard.Sources)
	}
}

// The starter pack carries the block for linear, and every stage it names
// is one of the flow a task follows.
func TestStarterCarriesTheLinearStageMap(t *testing.T) {
	p := load(t, starter)
	if len(p.Sources) != 2 || p.Sources[0].Source != "linear" || p.Sources[0].As != "yardmaster" || !maps.Equal(p.Sources[0].Map, starterMap) {
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

// A flow pack names a source it does not carry: hears, says and makes are
// read for any name, and the import sets them once the yard has the
// source, skipping them until then.
func TestStageMapForAnotherNameLoads(t *testing.T) {
	text := small + `sources:
  acme:
    as: yardmaster
    hears:
      task: { new: started, review: review, merged: done }
    says:
      - { where: "event=issue action=opened team=ENG", outcome: done }
    makes:
      - { where: "event=issue action=opened", bead: { type: task, stage: new } }
`
	p := load(t, writePack(t, smallFiles(text)))
	if len(p.Sources) != 1 {
		t.Fatalf("sources = %+v", p.Sources)
	}
	m := p.Sources[0]
	if m.Source != "acme" || m.As != "yardmaster" || len(m.Map) != 3 || len(m.Says) != 1 || len(m.Makes) != 1 {
		t.Fatalf("source = %+v", m)
	}
}

// A GitHub source's stage may add a label, where an issue has no status
// between open and closed.
func TestStageMapTakesALabelOnGitHub(t *testing.T) {
	text := strings.Replace(smallSources, "  linear:\n", "  github:\n", 1)
	text = strings.Replace(text, "review: review,", `review: "label:In Review",`, 1)
	p := load(t, writePack(t, smallFiles(text)))
	want := map[string]string{"task:new": "started", "task:review": "label:In Review", "task:merged": "done"}
	if len(p.Sources) != 1 || p.Sources[0].Source != "github" || !maps.Equal(p.Sources[0].Map, want) {
		t.Fatalf("sources = %+v, want %v", p.Sources, want)
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
		{name: "source yardr cannot set a status in", pairs: []string{"  linear:\n    as", "  token:\n    as"}, key: "sources.token", msg: "yardr cannot set a status in token (it can in none of the yard's sources)", line: 23},
		{name: "a source with nothing said of it", pairs: []string{"  linear:\n    as: yardmaster\n    hears:\n      task: { new: started, review: review, merged: done }\n", "  linear: {}\n"}, key: "sources.linear", msg: "empty: say what the source hears", line: 23},
		{name: "as naming no crew member of the pack", pairs: []string{"as: yardmaster", "as: boss"}, key: "sources.linear.as", msg: "no crew member boss is declared under crew", line: 24},
		{name: "type no flow of the pack is for", pairs: []string{"  default:\n", "  default:\n    type: train\n"}, key: "sources.linear.hears.task", msg: "no flow of this pack is for beads of type task", line: 27},
		{name: "stage the flow lacks", pairs: []string{"merged: done }\n", "merged: done, approved: done }\n"}, key: "sources.linear.hears.task.approved", msg: "flow default has no stage approved (stages: new, review, decide, merged)", line: 26},
		{name: "stage the type's own flow lacks", pairs: []string{"groups:\n", trains, "      task: { new: started, review: review, merged: done }\n", "      train: { new: started }\n"},
			key: "sources.linear.hears.train.new", msg: "flow trains has no stage new (stages: open, done)", line: 31},
		{name: "unknown word", pairs: []string{"review: review, merged", "review: reviewing, merged"}, key: "sources.linear.hears.task.review", msg: `"reviewing" is no state: one of started, review, done`, line: 26},
		{name: "a label without a name", pairs: []string{"review: review, merged", "review: \"label:\", merged"}, key: "sources.linear.hears.task.review", msg: `"label:" is no state`, line: 26},
		{name: "status by its name in the source", pairs: []string{"merged: done }", `merged: "Done" }`}, key: "sources.linear.hears.task.merged", msg: `"Done" is no state`, line: 26},
		{name: "no as", pairs: []string{"    as: yardmaster\n", ""}, key: "sources.linear.as", msg: "missing", line: 23},
		{name: "no hears", pairs: []string{"    hears:\n      task: { new: started, review: review, merged: done }\n", ""}, key: "sources.linear.hears", msg: "missing", line: 23},
		{name: "empty hears", pairs: []string{"    hears:\n      task: { new: started, review: review, merged: done }\n", "    hears: {}\n"}, key: "sources.linear.hears", msg: "empty", line: 25},
		{name: "unknown key", pairs: []string{"    as: yardmaster\n", "    as: yardmaster\n    comment: true\n"}, key: "sources.linear.comment", msg: "unknown key", line: 25},
		{name: "says is no list", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    says: { where: x }\n"}, key: "sources.github.says", msg: "want a list of lines", line: 28},
		{name: "empty says", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    says: []\n"}, key: "sources.github.says", msg: "empty", line: 28},
		{name: "a line without an outcome", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    says:\n      - { where: \"event=pull_request\" }\n"}, key: "sources.github.says[0].outcome", msg: "missing", line: 29},
		{name: "a where that parses wrong", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    says:\n      - { where: \"event pull_request\", outcome: done }\n"}, key: "sources.github.says[0].where", msg: `"event": want key=value`, line: 29},
		{name: "a where key the source has not", pairs: []string{"merged: done }\n", "merged: done }\n  token:\n    says:\n      - { where: \"kind=held\", outcome: done }\n"}, key: "sources.token.says[0]", msg: `cannot filter token on "kind"`, line: 29},
		{name: "an outcome no stage has", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    says:\n      - { where: \"event=pull_request\", outcome: landed }\n"}, key: "sources.github.says[0].outcome", msg: "no stage has an outcome landed", line: 29},
		{name: "an outcome the named stage has not", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    says:\n      - { where: \"event=pull_request stage=new\", outcome: changes }\n"}, key: "sources.github.says[0].outcome", msg: "stage new has no outcome changes (it has: done, question)", line: 29},
		{name: "a stage the flow lacks", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    says:\n      - { where: \"event=ci stage=in-pr\", outcome: done }\n"}, key: "sources.github.says[0].outcome", msg: "no stage in-pr", line: 29},
		{name: "a status in a source that has none", pairs: []string{"merged: done }\n", "merged: done }\n  token:\n    as: yardmaster\n    says:\n      - { where: \"event=deploy\", outcome: done }\n"}, key: "sources.token.as", msg: "yardr cannot set a status in token", line: 28},
		{name: "makes is no list", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    makes: { where: x }\n"}, key: "sources.github.makes", msg: "want a list of lines", line: 28},
		{name: "a make without a bead", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    makes:\n      - { where: \"event=issues\" }\n"}, key: "sources.github.makes[0].bead", msg: "missing", line: 29},
		{name: "a make without a stage", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    makes:\n      - { where: \"event=issues\", bead: { type: task } }\n"}, key: "sources.github.makes[0].bead.stage", msg: "missing", line: 29},
		{name: "a make filtered on the bead's stage", pairs: []string{"merged: done }\n", "merged: done }\n  token:\n    makes:\n      - { where: \"event=issue stage=new\", bead: { type: task, stage: new } }\n"}, key: "sources.token.makes[0]", msg: `cannot filter token on "stage"`, line: 29},
		{name: "a make filtered on the bead's type", pairs: []string{"merged: done }\n", "merged: done }\n  token:\n    makes:\n      - { where: \"event=issue type=task\", bead: { type: task, stage: new } }\n"}, key: "sources.token.makes[0]", msg: "redundant with the bead the line makes", line: 29},
		{name: "a make in a stage the flow lacks", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    makes:\n      - { where: \"event=issues\", bead: { type: task, stage: triage } }\n"}, key: "sources.github.makes[0].bead.stage", msg: "flow default: no stage triage a bead can be made in (stages: decide, new, review)", line: 29},
		{name: "a make in a stage that ends the flow", pairs: []string{"merged: done }\n", "merged: done }\n  github:\n    makes:\n      - { where: \"event=issues\", bead: { type: task, stage: merged } }\n"}, key: "sources.github.makes[0].bead.stage", msg: "no stage merged a bead can be made in", line: 29},
		{name: "a make of a type no flow is for", pairs: []string{"  default:\n", "  default:\n    type: train\n", "      task: { new", "      train: { new", "merged: done }\n", "merged: done }\n  github:\n    makes:\n      - { where: \"event=issues\", bead: { type: issue, stage: new } }\n"}, key: "sources.github.makes[0].bead.type", msg: "no flow of this pack is for beads of type issue", line: 30},
		{name: "a says line of a type no flow is for", pairs: []string{"  default:\n", "  default:\n    type: train\n", "      task: { new", "      train: { new", "merged: done }\n", "merged: done }\n  github:\n    says:\n      - { where: \"event=issues type=issue\", outcome: done }\n"}, key: "sources.github.says[0].outcome", msg: "no flow of this pack is for beads of type issue", line: 30},
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

// A map word loads for a source the pack does not carry: the yard's
// source is the judge of what it has a status for, when the plan is made
// (TestStageMapSkipsLinesThatDoNotFitTheSource).
func TestStageMapWordLoadsForAnUndeclaredSource(t *testing.T) {
	text := strings.Replace(smallSources, "  linear:\n    as", "  github:\n    as", 1)
	p := load(t, writePack(t, smallFiles(text)))
	want := map[string]string{"task:new": "started", "task:review": "review", "task:merged": "done"}
	if len(p.Sources) != 1 || !maps.Equal(p.Sources[0].Map, want) {
		t.Fatalf("sources = %+v, want %v", p.Sources, want)
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
	if h.Source == "linear" {
		withLinear(t, st)
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

// A pack with no flows has its lines held to the flow the depot follows
// when the plan is made: a line that flow lacks the stage or the outcome
// for refuses the plan, in the same words the reader refuses a pack's own
// lines. A line for a type no flow governs is left to the store, which
// judges it when an event arrives.
func TestSourcesWithoutFlowsAreHeldToTheDepotsFlow(t *testing.T) {
	const line = "new -> review on done | decide on question\nreview -> merged on done\ndecide -> new\nmerged terminal\n"
	const open = "open -> shut on done\nshut terminal\n"
	const heard = `name: flowless-hears
kind: git
crew:
  yardmaster:
    hears:
      events: [held]
      done: true
sources:
  github:
    as: yardmaster
    hears:
      task: { STAGE: started }
`
	cases := []struct {
		name, yaml, flow string
		want             []string // refused substrings; none: the plan is not refused
	}{
		{name: "a make in a stage the flow has", yaml: flowlessSources, flow: line},
		{name: "a make in a stage the flow lacks", yaml: flowlessSources, flow: open,
			want: []string{"sources.github.makes[0]: no bead of type task could be made there: flow line: no stage new a bead can be made in (stages: open)"}},
		{name: "a make where no flow governs", yaml: flowlessSources},
		{name: "a says line the flow routes", yaml: flowlessSources, flow: line},
		{name: "a says outcome no stage has", yaml: strings.Replace(flowlessSources, "outcome: done", "outcome: landed", 1), flow: line,
			want: []string{"sources.github.says[0]: no bead of depot web could take it: flow line: no stage has an outcome landed, so the flow would refuse it every time"}},
		{name: "a hears stage the flow has", yaml: strings.Replace(heard, "STAGE", "new", 1), flow: line},
		{name: "a hears stage the flow lacks", yaml: strings.Replace(heard, "STAGE", "approved", 1), flow: line,
			want: []string{"sources.github.hears.task.approved: flow line has no stage approved (stages: new, review, decide, merged)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, o := yard(t)
			if c.flow != "" {
				f, err := store.ParseFlow(c.flow)
				if err != nil {
					t.Fatal(err)
				}
				f.Name = "line"
				if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
					t.Fatal(err)
				}
				if err := st.SetFlowPointer(ctx, "web", "", "line", "me"); err != nil {
					t.Fatal(err)
				}
			}
			p := load(t, writePack(t, map[string]string{"pack.yaml": c.yaml}))
			plan := build(t, st, p, o)
			if len(c.want) == 0 {
				if len(plan.Refused) != 0 {
					t.Fatalf("refused = %v, want none", plan.Refused)
				}
				return
			}
			for _, w := range c.want {
				if !slices.ContainsFunc(plan.Refused, func(r string) bool { return strings.Contains(r, w) }) {
					t.Fatalf("refused = %v, want one with %q", plan.Refused, w)
				}
			}
		})
	}
}

// check without --depot plans a pack with no flows, groups or crew: the
// lines it would set on the yard's hooks, each waiting for the depot, whose
// flow they are held to once one is named. Anything more takes the depot.
func TestSourcesWithoutFlowsWaitForTheDepot(t *testing.T) {
	st, o := yard(t)
	o.Depot = ""
	p := load(t, writePack(t, map[string]string{"pack.yaml": flowlessSources}))
	plan := build(t, st, p, o)
	if len(plan.Refused) != 0 {
		t.Fatalf("refused = %v", plan.Refused)
	}
	for _, want := range []string{
		`the makes line "conclusion=failure event=workflow_run head_branch=main -> task:new" waits for the depot`,
		`the says line "conclusion=success event=workflow_run -> done" waits for the depot`,
	} {
		if !slices.ContainsFunc(plan.Notes, func(n string) bool { return strings.Contains(n, want) }) {
			t.Fatalf("notes = %v, want one with %q", plan.Notes, want)
		}
	}
	// A pack that names a depot's own is planned for a depot, never without.
	if _, err := pack.Build(ctx, st, load(t, starter), o); err == nil || !strings.Contains(err.Error(), "plan it with --depot <depot>") {
		t.Fatalf("Build without a depot = %v, want it refused", err)
	}
}

// A flow pack names a source it does not carry. Into a yard without one
// the import goes through and skips the lines, saying so; the next apply
// sets them once the yard has the source.
func TestStageMapWaitsForTheYardsSource(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, nil)
	p := load(t, writePack(t, smallFiles(small+`sources:
  acme:
    as: yardmaster
    hears:
      task: { new: started, review: review, merged: done }
    says:
      - { where: "event=issue action=opened", outcome: done }
`)))

	plan := build(t, st, p, o)
	m := mapPlan(t, plan, "acme")
	if m.Pack != "yardmaster" || m.As != "boss" || m.Action != pack.Skipped || len(mapObjects(plan)) != 0 {
		t.Fatalf("into a yard without the source: %+v, objects %v", m, mapObjects(plan))
	}
	if !strings.Contains(m.Skipped, "the yard has no source acme") || !strings.Contains(m.Skipped, "the next apply sets the lines once it is there") {
		t.Fatalf("skipped = %q", m.Skipped)
	}
	res := apply(t, st, plan, nil)
	if len(res.Mapped) != 0 {
		t.Fatalf("the import set the lines: %+v", res)
	}

	if _, err := st.PutSource(ctx, "acme.yaml", cmdDecl, "me"); err != nil {
		t.Fatal(err)
	}
	hook(t, st, store.Hook{Name: "acme", Source: "acme"})
	if err := st.SetIdentity(ctx, "boss", "acme", store.Identity{ID: "u-boss"}, "me"); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, p, o)
	m = mapPlan(t, plan, "acme")
	if m.Hook != "acme" || m.Action != pack.Create || m.Skipped != "" || !slices.Equal(mapObjects(plan), []string{"create hook acme as boss"}) {
		t.Fatalf("with the source: %+v, objects %v", m, mapObjects(plan))
	}
	apply(t, st, plan, nil)
	h := getHook(t, st, "acme")
	if h.As != "boss" || !maps.Equal(h.Map, map[string]string{"task:new": "started", "task:review": "review", "task:merged": "done"}) || len(h.Says) != 1 {
		t.Fatalf("hook = %+v", h)
	}
	plan = build(t, st, p, o)
	if m = mapPlan(t, plan, "acme"); m.Action != pack.Unchanged {
		t.Fatalf("second plan: %+v", m)
	}
}

// The lines are held to the yard's source: a status it has none of, a key
// its events carry none of, and a map for a source that moves no issue are
// all skipped, with why, and set once they fit.
func TestStageMapSkipsLinesThatDoNotFitTheSource(t *testing.T) {
	narrow := strings.Replace(cmdDecl, "states: [started, review, done]", "states: [started, done]", 1)
	cases := []struct {
		name, decl, sources, want string
	}{
		{name: "a status the source has none of", decl: narrow, sources: `  acme:
    as: yardmaster
    hears:
      task: { new: started, review: review }
`, want: `"review" is no state of acme`},
		{name: "a key the source's events carry none of", decl: cmdDecl, sources: `  acme:
    says:
      - { where: "event=issue priority=high", outcome: done }
`, want: `cannot filter acme on "priority"`},
		{name: "a map for a source that moves no issue", decl: pushover, sources: `  pushover:
    as: yardmaster
    hears:
      task: { new: started }
`, want: "moves no issue"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, o := yard(t)
			name := "acme"
			if c.decl == pushover {
				name = "pushover"
			}
			if _, err := st.PutSource(ctx, name+".yaml", c.decl, "me"); err != nil {
				t.Fatal(err)
			}
			p := load(t, writePack(t, smallFiles(small+"sources:\n"+c.sources)))
			plan := build(t, st, p, o)
			m := mapPlan(t, plan, name)
			if m.Action != pack.Skipped || !strings.Contains(m.Skipped, c.want) || len(mapObjects(plan)) != 0 {
				t.Fatalf("map = %+v, objects %v, want skipped with %q", m, mapObjects(plan), c.want)
			}
		})
	}
}

// Into a yard without a hook of the source the import goes through and sets
// nothing for it: the plan names the hook to add, the identity and the
// grant. It makes no hook. Once the yard has the hook, the next apply sets
// the map and the crew member on it, and the one after changes nothing.
func TestStageMapWaitsForTheYardsHook(t *testing.T) {
	st, o := yard(t)
	withLinear(t, st)
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
		{Field: "map", Old: "task:review=done train:open=started", New: "task:merged=done task:new=started task:review-security=review task:review=review"},
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
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, MapRefused: map[string]string{"linear": "not the operator"}, Actor: "boss"})
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
	edit(t, dir, "sources:\n  linear:\n    as: yardmaster\n    hears:\n      task: { new: started, review: review, review-security: review, merged: done }\n", "",
		githubSays, "")

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
	edit(t, dir, "sources:\n  linear:\n    as: yardmaster\n    hears:\n      task: { new: started, review: review, review-security: review, merged: done }\n", "",
		githubSays, "")
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

// githubSays is the starter's block for github, as its pack.yaml has it.
const githubSays = `  github:
    as: yardmaster
    says:
      - { where: "event=pull_request action=closed merged=true type=train", outcome: merged, note: "PR merged" }
      - { where: "event=pull_request action=closed merged=false type=train", outcome: question, note: "PR closed unmerged: reopen, or drop the train?" }
      - { where: "event=pull_request_review action=submitted state=changes_requested sender=user type=train", outcome: changes, note: "Changes requested on the PR" }
      - { where: "event=workflow_run event=check_suite action=completed conclusion=failure conclusion=timed_out stage=review stage=review-security type=train", outcome: changes, note: "CI failed on the head of the PR" }
      - { where: "event=ci conclusion=success stage=in-pr type=train", outcome: done }
      - { where: "event=ci conclusion=failure stage=in-pr type=train", outcome: changes }
`

// starterSays is the table the starter pack carries for github, as yardr
// hook add --says takes its lines.
func starterSays(t *testing.T) []store.Say {
	t.Helper()
	var out []store.Say
	for _, l := range []string{
		"event=pull_request action=closed merged=true type=train -> merged: PR merged",
		"event=pull_request action=closed merged=false type=train -> question: PR closed unmerged: reopen, or drop the train?",
		"event=pull_request_review action=submitted state=changes_requested sender=user type=train -> changes: Changes requested on the PR",
		"event=workflow_run event=check_suite action=completed conclusion=failure conclusion=timed_out stage=review stage=review-security type=train -> changes: CI failed on the head of the PR",
		"event=ci conclusion=success stage=in-pr type=train -> done",
		"event=ci conclusion=failure stage=in-pr type=train -> changes",
	} {
		s, err := store.ParseSay(l)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// The starter pack carries the table for github with the crew member the
// reads act as and no map, in the order written, and its flow has an edge
// for every outcome.
func TestStarterCarriesWhatGitHubSays(t *testing.T) {
	p := load(t, starter)
	if len(p.Sources) != 2 || p.Sources[1].Source != "github" || p.Sources[1].As != "yardmaster" || len(p.Sources[1].Map) != 0 || !store.SameSays(p.Sources[1].Says, starterSays(t)) {
		t.Fatalf("sources = %+v", p.Sources)
	}
	var trainFlow *pack.Flow
	for i := range p.Flows {
		if p.Flows[i].Type == store.TypeTrain {
			trainFlow = &p.Flows[i]
		}
	}
	if trainFlow == nil {
		t.Fatal("starter has no train flow")
	}
	for _, say := range p.Sources[1].Says {
		if !slices.ContainsFunc(trainFlow.Stages, func(s pack.Stage) bool {
			return slices.ContainsFunc(s.Outcomes, func(o store.Outcome) bool { return o.Name == say.Outcome })
		}) {
			t.Errorf("%s: flow %s has no edge for %s", say, p.Flows[0].Name, say.Outcome)
		}
	}
	for _, stage := range []string{"approved", "in-pr", "land"} {
		i := slices.IndexFunc(trainFlow.Stages, func(s pack.Stage) bool { return s.Name == stage })
		if i < 0 || !slices.ContainsFunc(trainFlow.Stages[i].Outcomes, func(o store.Outcome) bool { return o.Name == "merged" && o.To == "merged" }) {
			t.Errorf("train flow does not route merged from %s", stage)
		}
	}
}

// The import sets a source's table on the yard's hook of it and the crew
// member its reads act as, and needs the hook and that member's identity.
// A table set otherwise is replaced, and the plan shows both; a pack that
// drops the block has it cleared only by a prune, and only while it is as
// the import left it, the crew member with it, the hook kept.
func TestPackSetsWhatTheSourceSaysOnTheYardsHook(t *testing.T) {
	st, o := yard(t)
	withGitHub(t, st)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, nil)
	if err := st.SetIdentity(ctx, "boss", "github", store.Identity{ID: "boss-bot"}, "me"); err != nil {
		t.Fatal(err)
	}
	hook(t, st, store.Hook{Name: "gh", Source: "github"})
	dir := copyStarter(t)
	want := starterSays(t)

	plan := build(t, st, load(t, dir), o)
	i := slices.IndexFunc(plan.Maps, func(m pack.MapPlan) bool { return m.Source == "github" })
	if i < 0 {
		t.Fatalf("maps = %+v", plan.Maps)
	}
	mp := plan.Maps[i]
	if mp.Action != pack.Create || mp.As != "boss" || mp.Hook != "gh" || !store.SameSays(mp.Says, want) || len(mp.Needs) != 2 || mp.Needs[0].What != "hook" || mp.Needs[0].Status != "ok" || mp.Needs[1].What != "identity" || mp.Needs[1].Status != "ok" {
		t.Fatalf("plan = %+v", mp)
	}
	if got := actions(plan, pack.Create); !slices.Contains(got, "map hook gh as boss") {
		t.Fatalf("create = %v", got)
	}
	res := apply(t, st, plan, nil)
	if h := getHook(t, st, "gh"); !store.SameSays(h.Says, want) || h.As != "boss" {
		t.Fatalf("hook = %+v", h)
	}
	if !slices.ContainsFunc(res.Mapped, func(m pack.MapOp) bool { return m.Hook == "gh" && store.SameSays(m.Says, want) }) {
		t.Fatalf("mapped = %+v", res.Mapped)
	}
	if got := find(t, build(t, st, load(t, dir), o), "map", "hook gh as boss"); got.Action != pack.Unchanged {
		t.Fatalf("the next plan: %+v", got)
	}

	// A table set by hand is replaced by the pack's, and the plan shows both.
	byHand := want[:1]
	hook(t, st, store.Hook{Name: "gh", Source: "github", As: "boss", Says: byHand})
	plan = build(t, st, load(t, dir), o)
	mp = plan.Maps[i]
	if mp.Action != pack.Change || mp.Was == nil || !store.SameSays(mp.Was.Says, byHand) {
		t.Fatalf("plan over another table = %+v", mp)
	}
	apply(t, st, plan, nil)
	if h := getHook(t, st, "gh"); !store.SameSays(h.Says, want) || h.As != "boss" {
		t.Fatalf("hook = %+v", h)
	}

	// Dropped: left without a prune; with one the table and the crew member
	// go and the hook stays.
	edit(t, dir, githubSays, "")
	plan = build(t, st, load(t, dir), o)
	if got := actions(plan, pack.Dropped); !slices.Equal(got, []string{"map hook gh as boss"}) {
		t.Fatalf("dropped = %v", got)
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: pack.Answers{}, Prune: true, Actor: "me"}); err != nil {
		t.Fatal(err)
	}
	if h := getHook(t, st, "gh"); h.Says != nil || h.As != "" || h.Source != "github" {
		t.Fatalf("with --prune: hook %+v", h)
	}
}

// A says line is held to every flow of the pack, not to the train's alone:
// the bead an event is about may be of any type, so a line passes when some
// flow has its outcome in the stage it names, and one no flow could take is
// refused with each flow and what it lacks for that stage.
func TestSaysIsCheckedAgainstEveryFlow(t *testing.T) {
	text := strings.Replace(small, "groups:\n", "  issues:\n    type: issue\n    stages:\n      triage: { group: people, outcomes: { accepted: new, dropped: closed } }\n      new:    { group: builders, outcomes: { done: closed } }\n      closed: { end: true }\ngroups:\n", 1)
	says := func(lines string) string { return text + "sources:\n  github:\n    says:\n" + lines }

	// A stage of one flow only, a stage of each of two, an outcome only the
	// other flow has, and stages of different flows on one line.
	p := load(t, writePack(t, smallFiles(says(`      - { where: "event=issues action=labeled label=accepted stage=triage", outcome: accepted }
      - { where: "event=issues action=closed", outcome: dropped }
      - { where: "event=pull_request action=closed stage=new", outcome: question }
      - { where: "event=issues action=closed stage=review stage=new", outcome: done }
`))))
	if len(p.Sources) != 1 || len(p.Sources[0].Says) != 4 {
		t.Fatalf("sources = %+v", p.Sources)
	}

	for line, want := range map[string][]string{
		`{ where: "event=issues stage=triage", outcome: landed }`: {"no bead of this pack could take it",
			"flow default: no stage triage", "flow issues: stage triage has no outcome landed (it has: accepted, dropped)"},
		`{ where: "event=issues", outcome: landed }`: {"flow default: no stage has an outcome landed", "flow issues: no stage has an outcome landed"},
		// Each stage a line names must be some flow's with the outcome.
		`{ where: "event=issues stage=triage stage=review", outcome: accepted }`: {"flow default: stage review has no outcome accepted (it has: done)", "flow issues: no stage review"},
	} {
		_, err := pack.LoadDir(writePack(t, smallFiles(says("      - "+line+"\n"))))
		var errs pack.Errors
		if !errors.As(err, &errs) || len(errs) != 1 || errs[0].Key != "sources.github.says[0].outcome" {
			t.Fatalf("%s: errors = %v, want one at the outcome", line, err)
		}
		for _, w := range want {
			if !strings.Contains(errs[0].Msg, w) {
				t.Errorf("%s: %s\nwant it to say %q", line, errs[0].Msg, w)
			}
		}
	}
}

// A pack carries what a source's events make beside what they say: lines of
// where and bead, held to the flow of the pack the bead's type follows. The
// import sets them on the yard's hook of the source, shows what the hook
// had, and a prune clears them while they are as the import left them.
func TestPackSetsWhatTheSourceMakesOnTheYardsHook(t *testing.T) {
	text := strings.Replace(small, "groups:\n", "  issues:\n    type: issue\n    stages:\n      triage: { group: people, outcomes: { accepted: new, dropped: closed } }\n      new:    { group: builders, outcomes: { done: closed } }\n      closed: { end: true }\ngroups:\n", 1)
	const block = `sources:
  github:
    makes:
      - { where: "event=issues action=opened action=reopened", bead: { type: issue, stage: triage } }
      - { where: "event=pull_request action=opened", bead: { type: task, stage: review, depot: web } }
    says:
      - { where: "event=issues action=closed", outcome: dropped }
`
	var want []store.Make
	for _, l := range []string{"event=issues action=opened action=reopened -> issue:triage", "event=pull_request action=opened -> task:review@web"} {
		m, err := store.ParseMake(l)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, m)
	}
	dir := writePack(t, smallFiles(text+block))
	p := load(t, dir)
	if len(p.Sources) != 1 || p.Sources[0].As != "" || !store.SameMakes(p.Sources[0].Makes, want) || len(p.Sources[0].Says) != 1 {
		t.Fatalf("sources = %+v", p.Sources)
	}
	// The type's own flow is the one asked: a task has no triage.
	_, err := pack.LoadDir(writePack(t, smallFiles(text+strings.Replace(block, "type: issue, stage: triage", "type: task, stage: triage", 1))))
	var errs pack.Errors
	if !errors.As(err, &errs) || len(errs) != 1 || errs[0].Key != "sources.github.makes[0].bead.stage" || !strings.Contains(errs[0].Msg, "flow default: no stage triage") {
		t.Fatalf("a task made in triage: %v", err)
	}

	st, o := yard(t)
	withGitHub(t, st)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, nil)
	byHand := want[1:]
	hook(t, st, store.Hook{Name: "gh", Source: "github", Makes: byHand})
	plan := build(t, st, p, o)
	i := slices.IndexFunc(plan.Maps, func(m pack.MapPlan) bool { return m.Source == "github" })
	if i < 0 {
		t.Fatalf("maps = %+v", plan.Maps)
	}
	mp := plan.Maps[i]
	if mp.Action != pack.Change || mp.Hook != "gh" || !store.SameMakes(mp.Makes, want) || mp.Was == nil || !store.SameMakes(mp.Was.Makes, byHand) {
		t.Fatalf("plan = %+v", mp)
	}
	obj := find(t, plan, "map", "hook gh")
	if !slices.ContainsFunc(obj.Changes, func(d pack.Diff) bool {
		now, _ := d.New.(string)
		return d.Field == "makes" && d.Old == "action=opened event=pull_request -> task:review@web" && strings.HasPrefix(now, "action=opened action=reopened event=issues -> issue:triage; ")
	}) {
		t.Fatalf("changes = %+v", obj.Changes)
	}
	res := apply(t, st, plan, nil)
	if h := getHook(t, st, "gh"); !store.SameMakes(h.Makes, want) || len(h.Says) != 1 {
		t.Fatalf("hook = %+v", h)
	}
	if !slices.ContainsFunc(res.Mapped, func(m pack.MapOp) bool { return m.Hook == "gh" && store.SameMakes(m.Makes, want) }) {
		t.Fatalf("mapped = %+v", res.Mapped)
	}
	if got := find(t, build(t, st, load(t, dir), o), "map", "hook gh"); got.Action != pack.Unchanged {
		t.Fatalf("the next plan: %+v", got)
	}

	// Dropped: cleared with a prune, the rest of the hook kept.
	edit(t, dir, block, "")
	plan = build(t, st, load(t, dir), o)
	if got := actions(plan, pack.Dropped); !slices.Contains(got, "map hook gh") {
		t.Fatalf("dropped = %v", got)
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: pack.Answers{}, Prune: true, Actor: "me"}); err != nil {
		t.Fatal(err)
	}
	if h := getHook(t, st, "gh"); h.Makes != nil || h.Says != nil || h.Source != "github" {
		t.Fatalf("with --prune: hook %+v", h)
	}
}

// githubYard is a yard with a github hook gh that boss acts as, which the
// packs' tables are set on, and a second depot web2.
func githubYard(t *testing.T) (store.API, pack.Options) {
	t.Helper()
	st, o := yard(t)
	withGitHub(t, st)
	o.Crew = map[string]string{"yardmaster": "boss"}
	member(t, st, "boss", true, nil)
	if err := st.SetIdentity(ctx, "boss", "github", store.Identity{ID: "boss-bot"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "web2", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	hook(t, st, store.Hook{Name: "gh", Source: "github"})
	return st, o
}

// say reads a line as yardr hook add --says takes it.
func say(t *testing.T, line string) store.Say {
	t.Helper()
	s, err := store.ParseSay(line)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// githubOf is the pack's block for github.
func githubOf(t *testing.T, dir string) pack.StageMap {
	t.Helper()
	for _, sm := range load(t, dir).Sources {
		if sm.Source == "github" {
			return sm
		}
	}
	t.Fatalf("%s has no block for github", dir)
	return pack.StageMap{}
}

// An import sets the pack's lines among the hook's and takes off none but
// those the pack set at its last import and no longer gives: the operator's
// lines stay where they stand, the plan never offers them as drops, and
// nothing is asked for them. A line the pack gains goes where the pack wrote
// it (first match counts), the last after the rest.
func TestPackImportKeepsTheHooksOtherLines(t *testing.T) {
	st, o := githubYard(t)
	dir := copyStarter(t)
	apply(t, st, build(t, st, load(t, dir), o), nil)

	// The operator's lines: one in front of the pack's, one among them.
	first := say(t, "event=pull_request action=closed merged=false type=pr -> dropped")
	among := say(t, "event=pull_request action=labeled label=hold -> question")
	was := starterSays(t)
	have := slices.Concat([]store.Say{first}, was[:3], []store.Say{among}, was[3:])
	hook(t, st, store.Hook{Name: "gh", Source: "github", As: "boss", Says: have})

	// The pack lets go of its second line for another in its place, and gains
	// one after its last.
	edit(t, dir, `      - { where: "event=pull_request action=closed merged=false type=train", outcome: question, note: "PR closed unmerged: reopen, or drop the train?" }
`, `      - { where: "event=pull_request action=labeled label=drop type=train", outcome: question }
`, `      - { where: "event=ci conclusion=failure stage=in-pr type=train", outcome: changes }
`, `      - { where: "event=ci conclusion=failure stage=in-pr type=train", outcome: changes }
      - { where: "event=pull_request action=labeled label=last type=train", outcome: question }
`)
	replaced := say(t, "event=pull_request action=labeled label=drop type=train -> question")
	last := say(t, "event=pull_request action=labeled label=last type=train -> question")
	next := slices.Concat([]store.Say{first}, was[:1], []store.Say{replaced}, was[2:3], []store.Say{among}, was[3:], []store.Say{last})

	plan := build(t, st, load(t, dir), o)
	mp := mapPlan(t, plan, "github")
	if mp.Action != pack.Change || !slices.Equal(mp.Drops, []pack.HookDrop{{Table: "says", Line: was[1].String()}}) {
		t.Fatalf("plan = %+v, want the pack's second line the one drop", mp)
	}
	want := []pack.Diff{{Field: "says", Old: strings.Join(store.SayLines(have), "; "), New: strings.Join(store.SayLines(next), "; ")}}
	if got := find(t, plan, "map", "hook gh as boss").Changes; !slices.Equal(got, want) {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
	// Nothing is refused for the operator's lines, and none is told as one to
	// drop.
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: answered(t, plan, nil), Actor: "me"}); err != nil {
		t.Fatalf("applying: %v", err)
	}
	if h := getHook(t, st, "gh"); !store.SameSays(h.Says, next) {
		t.Fatalf("hook = %v\nwant %v", store.SayLines(h.Says), store.SayLines(next))
	}
	// The record keeps the pack's lines alone, and a second import is once.
	if rec := record(t, st); !slices.ContainsFunc(rec.Objects, func(po store.PackObject) bool {
		return po.Kind == "map" && po.Name == "gh" && store.SameSays(po.Says, githubOf(t, dir).Says)
	}) {
		t.Fatalf("record = %+v", rec.Objects)
	}
	if got := mapPlan(t, build(t, st, load(t, dir), o), "github"); got.Action != pack.Unchanged || len(got.Drops) != 0 {
		t.Fatalf("the next plan: %+v", got)
	}

	// The source block can stay after the pack lets go of its last table.
	// Its own lines still have to come off before the new empty table is
	// recorded, while the operator's lines stay.
	owned := slices.Clone(githubOf(t, dir).Says)
	path := filepath.Join(dir, "pack.yaml")
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents := string(text)
	start := strings.LastIndex(contents, "    says:\n")
	if start < 0 {
		t.Fatal("the copied starter pack has no github says table")
	}
	emptySays := contents[:start] + "    hears:\n      train: { in-pr: started }\n"
	if err := os.WriteFile(path, []byte(emptySays), 0o644); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	mp = mapPlan(t, plan, "github")
	if mp.Action != pack.Change || len(mp.Drops) != len(owned) || slices.ContainsFunc(mp.Drops, func(d pack.HookDrop) bool {
		return d.Table != "says" || !slices.Contains(store.SayLines(owned), d.Line)
	}) {
		t.Fatalf("plan for the empty table = %+v, want the pack's %d old lines dropped", mp, len(owned))
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: answered(t, plan, nil), Actor: "me"}); err != nil {
		t.Fatalf("applying the empty table: %v", err)
	}
	if h := getHook(t, st, "gh"); !store.SameSays(h.Says, []store.Say{first, among}) {
		t.Fatalf("hook after the pack drops its last lines = %v, want the operator's lines", store.SayLines(h.Says))
	}
}

// Two packs set their lines on the one hook of a source, in either order and
// into either depot: each import keeps every line of the other where it
// stands and adds its own after the rest, a plan of the second shows lines
// to add and none to drop, and a pack that lets go of a line takes off that
// one alone, the other pack's lines with it staying.
func TestTwoPacksShareOneHooksTables(t *testing.T) {
	for _, c := range []struct {
		name          string
		first, second string // pack directories
	}{
		{"contributors then the starter", contributors, starter},
		{"the starter then contributors", starter, contributors},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, o := githubYard(t)
			o2 := o
			o2.Depot = "web2"
			dirs := map[string]string{c.first: copyPack(t, c.first), c.second: copyPack(t, c.second)}
			first, second := dirs[c.first], dirs[c.second]
			all := slices.Concat(githubOf(t, first).Says, githubOf(t, second).Says)
			// Only contributors makes beads.
			makes := githubOf(t, dirs[contributors]).Makes

			apply(t, st, build(t, st, load(t, first), o), nil)
			plan := build(t, st, load(t, second), o)
			mp := mapPlan(t, plan, "github")
			if mp.Action != pack.Change || len(mp.Drops) != 0 {
				t.Fatalf("the second pack's plan: %+v, want lines to add and none to drop", mp)
			}
			apply(t, st, plan, nil)
			if h := getHook(t, st, "gh"); !store.SameSays(h.Says, all) || !store.SameMakes(h.Makes, makes) {
				t.Fatalf("hook = %v\nmakes %v\nwant %v\nand %v", store.SayLines(h.Says), store.MakeLines(h.Makes), store.SayLines(all), store.MakeLines(makes))
			}
			// Both are once, and into the second depot too.
			for _, dir := range []string{first, second} {
				for _, o := range []pack.Options{o, o2} {
					plan := build(t, st, load(t, dir), o)
					if mp := mapPlan(t, plan, "github"); mp.Action != pack.Unchanged || len(mp.Drops) != 0 {
						t.Fatalf("%s again into %s: %+v", dir, o.Depot, mp)
					}
					if o.Depot == "web2" {
						apply(t, st, plan, nil)
					}
				}
			}
			if h := getHook(t, st, "gh"); !store.SameSays(h.Says, all) || !store.SameMakes(h.Makes, makes) {
				t.Fatalf("hook after both into the second depot = %v", store.SayLines(h.Says))
			}

			// Each pack lets go of a line: that one goes, the rest stays.
			cdir, sdir := dirs[contributors], dirs[starter]
			edit(t, cdir, `      - { where: "event=issues action=closed stage=triage stage=backlog stage=new stage=review stage=approved stage=decide type=issue", outcome: dropped }
`, "")
			gone := say(t, "event=issues action=closed stage=triage stage=backlog stage=new stage=review stage=approved stage=decide type=issue -> dropped")
			plan = build(t, st, load(t, cdir), o)
			if mp := mapPlan(t, plan, "github"); !slices.Equal(mp.Drops, []pack.HookDrop{{Table: "says", Line: gone.String()}}) {
				t.Fatalf("contributors' drops = %+v, want %s alone", mp.Drops, gone)
			}
			apply(t, st, plan, nil)
			all = slices.DeleteFunc(all, func(s store.Say) bool { return s.String() == gone.String() })
			if h := getHook(t, st, "gh"); !store.SameSays(h.Says, all) {
				t.Fatalf("hook = %v", store.SayLines(h.Says))
			}
			edit(t, sdir, `      - { where: "event=ci conclusion=failure stage=in-pr type=train", outcome: changes }
`, "")
			gone = say(t, "event=ci conclusion=failure stage=in-pr type=train -> changes")
			plan = build(t, st, load(t, sdir), o)
			if mp := mapPlan(t, plan, "github"); !slices.Equal(mp.Drops, []pack.HookDrop{{Table: "says", Line: gone.String()}}) {
				t.Fatalf("the starter's drops = %+v, want %s alone", mp.Drops, gone)
			}
			apply(t, st, plan, nil)
			all = slices.DeleteFunc(all, func(s store.Say) bool { return s.String() == gone.String() })
			if h := getHook(t, st, "gh"); !store.SameSays(h.Says, all) || !store.SameMakes(h.Makes, makes) {
				t.Fatalf("hook = %v", store.SayLines(h.Says))
			}

			// The contributors still carries the source, but no makes table:
			// its last import's makes are taken off while both packs' says stay.
			oldMakes := slices.Clone(githubOf(t, cdir).Makes)
			path := filepath.Join(cdir, "pack.yaml")
			text, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			contents := string(text)
			start := strings.Index(contents, "    makes:\n")
			if start < 0 {
				t.Fatal("the copied contributors pack has no makes table")
			}
			relEnd := strings.Index(contents[start:], "\n    # SAYS")
			if relEnd < 0 {
				t.Fatal("the copied contributors pack has no makes table")
			}
			end := start + relEnd
			if err := os.WriteFile(path, []byte(contents[:start]+contents[end:]), 0o644); err != nil {
				t.Fatal(err)
			}
			plan = build(t, st, load(t, cdir), o)
			if mp := mapPlan(t, plan, "github"); mp.Action != pack.Change || len(mp.Drops) != len(oldMakes) || slices.ContainsFunc(mp.Drops, func(d pack.HookDrop) bool {
				return d.Table != "makes" || !slices.Contains(store.MakeLines(oldMakes), d.Line)
			}) {
				t.Fatalf("contributors' plan for the empty makes table = %+v, want its %d old lines dropped", mp, len(oldMakes))
			}
			apply(t, st, plan, nil)
			if h := getHook(t, st, "gh"); len(h.Makes) != 0 || !store.SameSays(h.Says, all) {
				t.Fatalf("hook after contributors drops makes = %v / %v", store.SayLines(h.Says), store.MakeLines(h.Makes))
			}
		})
	}
}

// A pack that drops its block from the sources has its own lines, those it
// set and the hook still has, removed by a prune, and only those: another
// pack's lines stay, and so do the crew member and the map, which the other
// pack acts as too.
func TestPrunedBlockTakesOnlyItsOwnLines(t *testing.T) {
	st, o := githubYard(t)
	cdir := copyPack(t, contributors)
	apply(t, st, build(t, st, load(t, cdir), o), nil)
	apply(t, st, build(t, st, load(t, starter), o), nil)
	mine := githubOf(t, cdir)
	h := getHook(t, st, "gh")
	if want := slices.Concat(mine.Says, starterSays(t)); !store.SameSays(h.Says, want) || h.As != "boss" || !maps.Equal(h.Map, mine.Map) {
		t.Fatalf("hook = %+v", h)
	}

	// The pack's source block is the last thing in its pack.yaml.
	text, err := os.ReadFile(filepath.Join(cdir, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdir, "pack.yaml"), text[:strings.Index(string(text), "\nsources:\n")+1], 0o644); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, cdir), o)
	// The starter's plain routes stand for the typed ones of the pack's, which
	// are dropped too: the block is the one under test.
	if got := actions(plan, pack.Dropped); !slices.Contains(got, "map hook gh as boss") {
		t.Fatalf("dropped = %v", got)
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: answered(t, plan, nil), Actor: "me"}); err != nil {
		t.Fatal(err)
	}
	if got := getHook(t, st, "gh"); !store.SameSays(got.Says, h.Says) || !store.SameMakes(got.Makes, h.Makes) {
		t.Fatalf("without --prune: hook = %+v", got)
	}

	plan = build(t, st, load(t, cdir), o)
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: answered(t, plan, nil), Prune: true, Actor: "me"}); err != nil {
		t.Fatal(err)
	}
	got := getHook(t, st, "gh")
	if !store.SameSays(got.Says, starterSays(t)) || got.Makes != nil || got.As != "boss" || !maps.Equal(got.Map, mine.Map) {
		t.Fatalf("with --prune: hook = %+v, want the starter's lines alone, and the crew member and the map kept", got)
	}
	if mp := mapPlan(t, build(t, st, load(t, starter), o), "github"); mp.Action != pack.Unchanged {
		t.Fatalf("the starter's plan after: %+v", mp)
	}
}

// The table two packs share routes an event by the type of the bead it is
// about, whichever pack came first: a pull request closed unmerged asks a
// person about a train and drops a contributor's pull request, a run of CI
// that failed in review is noted for a train with the words of its pack and
// says nothing to a pull request, whose CI is the yard's own, and the lines of
// one pack say nothing to the beads of the other.
func TestSharedTableRoutesByTheBeadsType(t *testing.T) {
	ev := func(kv ...string) map[string][]string {
		fields := map[string][]string{}
		for i := 0; i < len(kv); i += 2 {
			fields[kv[i]] = []string{kv[i+1]}
		}
		return fields
	}
	pull := func(action, merged string) map[string][]string {
		return ev("event", "pull_request", "action", action, "merged", merged)
	}
	ci := func(event, conclusion string) map[string][]string {
		return ev("event", event, "action", "completed", "conclusion", conclusion)
	}
	changes := ev("event", "pull_request_review", "action", "submitted", "state", "changes_requested", "sender", "user")
	bot := ev("event", "pull_request", "action", "synchronize", "sender", "bot")
	// The hook once each pack has been imported, whichever came first.
	var hooks []store.Hook
	for _, order := range [][]string{{contributors, starter}, {starter, contributors}} {
		st, o := githubYard(t)
		for _, dir := range order {
			apply(t, st, build(t, st, load(t, dir), o), nil)
		}
		hooks = append(hooks, getHook(t, st, "gh"))
	}
	for _, c := range []struct {
		what          string
		fields        map[string][]string
		stage, typ    string
		outcome, note string
	}{
		{"closed unmerged, a train", pull("closed", "false"), "in-pr", "train", "question", "PR closed unmerged: reopen, or drop the train?"},
		{"closed unmerged, a pull request", pull("closed", "false"), "triage", "pr", "dropped", ""},
		{"closed unmerged, a task that tracks it", pull("closed", "false"), "new", "task", "", ""},
		{"merged, a train", pull("closed", "true"), "approved", "train", "merged", "PR merged"},
		{"merged, a pull request", pull("closed", "true"), "approved", "pr", "merged", ""},
		{"merged under review, a pull request", pull("closed", "true"), "review", "pr", "merged", ""},
		{"closed unmerged under review, a pull request", pull("closed", "false"), "review", "pr", "dropped", ""},
		{"merged in the wait for CI, a pull request", pull("closed", "true"), "ciwait", "pr", "merged", ""},
		{"closed unmerged in the wait for CI, a pull request", pull("closed", "false"), "ciwait", "pr", "dropped", ""},
		{"merged while it waits for its contributor, a pull request", pull("closed", "true"), "contributor", "pr", "merged", ""},
		{"closed unmerged while it waits for its contributor, a pull request", pull("closed", "false"), "contributor", "pr", "dropped", ""},
		{"a run failed in review, a train", ci("workflow_run", "failure"), "review", "train", "changes", "CI failed on the head of the PR"},
		{"a run failed in review, a pull request", ci("workflow_run", "failure"), "review", "pr", "", ""},
		{"a suite failed in review, a train", ci("check_suite", "failure"), "review", "train", "changes", "CI failed on the head of the PR"},
		{"a suite failed in review, a pull request", ci("check_suite", "failure"), "review", "pr", "", ""},
		{"a run failed in the wait for CI, a pull request", ci("workflow_run", "failure"), "ciwait", "pr", "", ""},
		{"a run failed in the wait for CI, a train", ci("workflow_run", "failure"), "ciwait", "train", "", ""},
		{"a run succeeded in the wait for CI, a pull request", ci("workflow_run", "success"), "ciwait", "pr", "", ""},
		{"a run succeeded in the wait for CI, a train", ci("workflow_run", "success"), "ciwait", "train", "", ""},
		{"CI read green, a train", ev("event", "ci", "action", "completed", "conclusion", "success"), "in-pr", "train", "done", ""},
		{"CI read green, a pull request", ev("event", "ci", "action", "completed", "conclusion", "success"), "in-pr", "pr", "", ""},
		{"changes requested, a train", changes, "review", "train", "changes", "Changes requested on the PR"},
		{"changes requested, a pull request", changes, "review", "pr", "", ""},
		{"pushed under review, a pull request", ev("event", "pull_request", "action", "synchronize"), "review", "pr", "changes", "the PR changed under review"},
		{"pushed under review, a train", ev("event", "pull_request", "action", "synchronize"), "review", "train", "", ""},
		{"a bot pushed under review, a pull request", bot, "review", "pr", "pushed", "a bot pushed to the PR"},
		{"a bot pushed in the wait for CI, a pull request", bot, "ciwait", "pr", "pushed", "a bot pushed to the PR"},
		{"a bot pushed under review, a train", bot, "review", "train", "", ""},
		{"an issue closed, an issue", ev("event", "issues", "action", "closed"), "triage", "issue", "dropped", ""},
		{"an issue closed, a task that tracks it", ev("event", "issues", "action", "closed"), "new", "task", "", ""},
	} {
		for i, h := range hooks {
			var outcome, note string
			data := map[string]any{"type": c.fields["event"][0], "action": c.fields["action"][0], "fields": c.fields}
			if say := h.Said(data, c.stage, c.typ); say != nil {
				outcome, note = say.Outcome, say.Note
			}
			if outcome != c.outcome || note != c.note {
				t.Errorf("%s (order %d): says %q %q, want %q %q", c.what, i, outcome, note, c.outcome, c.note)
			}
		}
	}
}

// A line both packs give is on the hook once, and stays when one of them lets
// go of it: the other still sets it. The pack that is the last to let go of it
// takes it off.
func TestALineTwoPacksGiveStaysWhileOneDoes(t *testing.T) {
	st, o := githubYard(t)
	both := say(t, "event=issues action=labeled label=shared -> question")
	const line = `      - { where: "event=issues action=labeled label=shared", outcome: question }` + "\n"
	const after = `      - { where: "event=pull_request action=closed merged=false type=pr", outcome: dropped }` + "\n"
	cdir, sdir := copyPack(t, contributors), copyStarter(t)
	edit(t, cdir, after, after+line)
	edit(t, sdir, `      - { where: "event=ci conclusion=failure stage=in-pr type=train", outcome: changes }`+"\n",
		`      - { where: "event=ci conclusion=failure stage=in-pr type=train", outcome: changes }`+"\n"+line)
	for _, dir := range []string{cdir, sdir} {
		apply(t, st, build(t, st, load(t, dir), o), nil)
	}
	count := func() int {
		n := 0
		for _, s := range getHook(t, st, "gh").Says {
			if s.String() == both.String() {
				n++
			}
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("the line both packs give is on the hook %d times, want once", n)
	}

	// The contributors let go of it: the starter still gives it.
	edit(t, cdir, line, "")
	plan := build(t, st, load(t, cdir), o)
	if mp := mapPlan(t, plan, "github"); len(mp.Drops) != 0 {
		t.Fatalf("contributors' drops = %+v, want none: the starter gives the line", mp.Drops)
	}
	apply(t, st, plan, nil)
	if n := count(); n != 1 {
		t.Fatalf("the line is on the hook %d times after the contributors let go of it, want once", n)
	}

	// The starter lets go of it too: nobody gives it, and it is taken off.
	edit(t, sdir, line, "")
	plan = build(t, st, load(t, sdir), o)
	if mp := mapPlan(t, plan, "github"); !slices.Equal(mp.Drops, []pack.HookDrop{{Table: "says", Line: both.String()}}) {
		t.Fatalf("the starter's drops = %+v, want the line", mp.Drops)
	}
	apply(t, st, plan, nil)
	if n := count(); n != 0 {
		t.Fatalf("the line is on the hook %d times after both let go of it, want none", n)
	}
}

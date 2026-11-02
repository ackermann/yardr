package pack_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/docs"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/testload"
	"yardr.dev/yardr/packs"
)

// Applying a pack from a directory asks git which commit the directory is at
// (Commit), and these tests apply a great many: on macOS each ask is two execs,
// the system's shim and the git it runs, so the real git goes first on PATH
// (testload.PreferRealGit). Inherited Git settings reach those calls, and
// GIT_EXEC_PATH can make the shim report a directory unrelated to the git it
// runs, so what the pane or hook that runs the tests sets for git goes first.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") {
			os.Unsetenv(name)
		}
	}
	testload.PreferRealGit()
	os.Exit(m.Run())
}

// starter is the pack the repository ships, and these tests' fixture.
const starter = "../../packs/starter"

// contributors is the pack for a public repository's issues and pull
// requests, whose github lines share the starter's hook.
const contributors = "../../packs/contributors"

// design is the pack for a question of direction, drafted as a document
// and landed by the same script as the starter's beads.
const design = "../../packs/design"

// website is the starter's line with a deploy stage, for a website on the
// user's own Void platform; these tests hold its deploy script to the
// reference one.
const website = "../../packs/website"

// writePack makes a pack directory from files (path -> content); a path
// under scripts/ is made executable.
func writePack(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "scripts/") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// small is a valid pack the strictness cases each break in one place.
const small = `name: small
flows:
  default:
    stages:
      new:    { group: builders, outcomes: { done: review, question: decide }, manual: [decide] }
      review: { group: assembly, outcomes: { done: merged } }
      decide: { group: people, manual: [new] }
      merged: { end: true }
groups:
  builders: { agent: roles/builder.md, limit: 2 }
  assembly: { run: scripts/land }
  people:   { people: true }
crew:
  yardmaster:
    role: roles/yardmaster.md
    hears:
      events: [held]
      stages: [decide]
      done: true
    subscribe:
      linear: involved
`

func smallFiles(yaml string) map[string]string {
	return map[string]string{"pack.yaml": yaml, "roles/builder.md": "build\n", "roles/yardmaster.md": "yardmaster\n", "scripts/land": "#!/bin/sh\n"}
}

func TestStarterPackLoads(t *testing.T) {
	p, err := pack.LoadDir(starter)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "starter" || len(p.Flows) != 2 || len(p.Groups) != 6 || len(p.Crew) != 1 {
		t.Fatalf("starter = %+v", p)
	}
	f := p.Flows[0]
	var stages []string
	for _, s := range f.Stages {
		stages = append(stages, s.Name)
	}
	if want := []string{"backlog", "new", "review", "review-security", "clear", "approved", "decide", "merged"}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want the file's order %v", stages, want)
	}
	// The four ways back to new, one from each stage that can send a bead
	// back, are limited to 3 rounds, after which a bead goes to decide.
	review := f.Stages[2]
	if want := []store.Outcome{{Name: "done", To: "review-security"}, {Name: "done-safe", To: "clear"}, {Name: "changes", To: "new", Max: 3, Then: "decide"}, {Name: "question", To: "decide"}}; !reflect.DeepEqual(review.Outcomes, want) {
		t.Fatalf("review outcomes = %v, want the file's order %v", review.Outcomes, want)
	}
	// The second review is another group's, which runs the first's agent
	// unless the import binds it.
	second := f.Stages[3]
	if want := []store.Outcome{{Name: "done", To: "clear"}, {Name: "changes", To: "new", Max: 3, Then: "decide"}, {Name: "question", To: "decide"}}; second.Group != "security-reviewers" || !reflect.DeepEqual(second.Outcomes, want) {
		t.Fatalf("review-security = %+v, want the group security-reviewers and the outcomes %v", second, want)
	}
	landing := f.Stages[5]
	if want := []store.Outcome{{Name: "done", To: "merged"}, {Name: "failed", To: "new", Max: 3, Then: "decide"}, {Name: "question", To: "decide"}}; landing.Group != "assembly" || !reflect.DeepEqual(landing.Outcomes, want) {
		t.Fatalf("approved = %+v, want the group assembly and the outcomes %v", landing, want)
	}
	// The train has no default limit: its rounds back to open come from a
	// pull request's events and from rebases, and the yard that runs trains
	// is the one to pick a number for them.
	for _, s := range p.Flows[1].Stages {
		for _, o := range s.Outcomes {
			if o.Max != 0 || o.Then != "" {
				t.Errorf("train stage %s outcome %s carries a limit (max %d then %s), and the train has none by default", s.Name, o.Name, o.Max, o.Then)
			}
		}
	}
	if g := p.Groups[2]; g.Name != "security-reviewers" || g.Agent != "roles/review-security.md" || g.BindsAs != "reviewers" {
		t.Fatalf("group %+v, want security-reviewers with the review-security role, bound as reviewers", g)
	}
	// The assembly lands a bead by squash, which is its script's option and
	// the pack's to set on the group the import creates; no other group has one.
	for _, g := range p.Groups {
		var want map[string]string
		if g.Name == "assembly" {
			want = map[string]string{"YARDR_LAND": "squash"}
		}
		if !reflect.DeepEqual(g.Env, want) {
			t.Errorf("group %s env = %v, want %v", g.Name, g.Env, want)
		}
	}
	if want := []string{"roles/builder.md", "roles/review-security.md", "roles/reviewer.md", "roles/yardmaster.md", "scripts/merge-local"}; !reflect.DeepEqual(p.Files(), want) {
		t.Fatalf("files = %v, want %v", p.Files(), want)
	}
	// Every stage a bead is worked in has its stage file.
	for _, s := range f.Stages {
		want := "flows/default/" + s.Name + ".md"
		if s.End {
			want = ""
		}
		if s.File != want {
			t.Errorf("stage %s names the stage file %q, want %q", s.Name, s.File, want)
		}
	}
	m := p.Crew[0]
	if m.Name != "yardmaster" || m.Role != "roles/yardmaster.md" || !m.Done || !reflect.DeepEqual(m.Stages, []string{"decide", "clear"}) ||
		!reflect.DeepEqual(m.Subscribe, []pack.Source{{Name: "linear", Scope: "involved"}}) || !reflect.DeepEqual(m.Events, []string{store.KindAttention}) {
		t.Fatalf("yardmaster = %+v", m)
	}
	// It asks for what its role tells the yardmaster to do: open a thread in
	// the source it subscribes to.
	if !reflect.DeepEqual(m.OpenThreads, []string{"linear"}) {
		t.Fatalf("the starter's yardmaster asks to open threads in %v", m.OpenThreads)
	}
	// hears.events replaces the list, so a trouble kind the pack leaves out
	// never reaches the yardmaster: a bead stranded by a flow change must,
	// which the class the pack names has.
	if !slices.Contains(store.AttentionKinds, "stranded") {
		t.Fatalf("the starter's yardmaster does not hear stranded: %v is %v", m.Events, store.AttentionKinds)
	}
}

// The starter's flow, as the store would keep it, is the flow yardr depot
// init writes: the same rules, written in the pack's format, and the limits
// on the rounds back to new in the words a flow file has them.
func TestStarterFlowIsTheDepotInitFlow(t *testing.T) {
	p, err := pack.LoadDir(starter)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Flows[0].StoreFlow("x")
	want := `backlog -> new | decide
new -> review on done | decide on question | backlog
review -> review-security on done | clear on done-safe | new on changes max 3 then decide | decide on question
review-security -> clear on done | new on changes max 3 then decide | decide on question
clear -> approved on done | new on changes max 3 then decide | decide on question
approved -> merged on done | new on failed max 3 then decide | decide on question
decide -> backlog | new
merged terminal
`
	if got.String() != want {
		t.Fatalf("flow =\n%s\nwant\n%s", got.String(), want)
	}
}

// A reviewed bead waits in clear for the person's yes before it lands: done
// lands it from approved, changes sends it back to the builder, and question
// parks it in decide. Clear's changes counts with the reviews' and the
// assembly's failed in the one count for new, so a bead sent back twice from
// review and once from clear has used its rounds: driven here through Done on
// the starter's own flow, the next changes from clear goes to decide instead.
func TestStarterClearWaitsForThePersonsYes(t *testing.T) {
	p, err := pack.LoadDir(starter)
	if err != nil {
		t.Fatal(err)
	}
	var clear pack.Stage
	for _, s := range p.Flows[0].Stages {
		if s.Name == "clear" {
			clear = s
		}
	}
	if want := []store.Outcome{{Name: "done", To: "approved"}, {Name: "changes", To: "new", Max: 3, Then: "decide"}, {Name: "question", To: "decide"}}; clear.Group != "decisions" || clear.File != "flows/default/clear.md" || !reflect.DeepEqual(clear.Outcomes, want) {
		t.Fatalf("clear = %+v, want the decisions group, its stage file and the outcomes %v", clear, want)
	}
	// Every way back to new carries the one limit, so the count is the
	// stage's, whichever stage sent the bead back.
	for _, s := range p.Flows[0].Stages {
		for _, o := range s.Outcomes {
			if o.Max == 0 {
				continue
			}
			if o.To != "new" || o.Max != 3 || o.Then != "decide" {
				t.Fatalf("%s %s = %+v, want back to new, max 3, then decide", s.Name, o.Name, o)
			}
		}
	}

	st, _ := yard(t)
	if _, err := st.PutFlow(ctx, p.Flows[0].StoreFlow("starter.default"), "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "web", "", "starter.default", "me"); err != nil {
		t.Fatal(err)
	}
	make := func() string {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	report := func(id, outcome, want string) {
		t.Helper()
		from, err := st.GetBead(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.Done(ctx, id, outcome, "note", store.Expect{}, "me")
		if err != nil || got.Stage != want {
			t.Fatalf("%s from %s = %+v, %v; want stage %s", outcome, from.Stage, got, err, want)
		}
	}
	toClear := func(id string) {
		t.Helper()
		report(id, "done", "review")
		report(id, "done", "review-security")
		report(id, "done", "clear")
	}

	// Twice back from review, once back from clear: the next changes, from
	// clear, is the fourth round back to new, and goes to decide instead.
	id := make()
	report(id, "done", "review")
	report(id, "changes", "new")
	report(id, "done", "review")
	report(id, "changes", "new")
	report(id, "done", "review")
	report(id, "done", "review-security")
	report(id, "done", "clear")
	report(id, "changes", "new")
	toClear(id)
	report(id, "changes", "decide")
	if got, err := st.GetBead(ctx, id); err != nil || got.Rounds["new"] != 0 {
		t.Fatalf("rounds after the redirect = %+v, %v; want new back at zero", got, err)
	}
	if evs, err := st.Events(ctx, id, 2); err != nil || len(evs) != 2 || evs[0].Kind != "advanced" ||
		evs[0].Data["from"] != "clear" || evs[0].Data["outcome"] != "changes" || evs[0].Data["rounds"] != float64(3) ||
		evs[0].Data["instead_of"] != "new" || evs[1].Kind != store.RoundsReached {
		t.Fatalf("last events = %+v, %v; want the redirect past new and its rounds_reached", evs, err)
	}

	// Done from clear lands the bead from approved; question parks it in
	// decide for a longer look.
	land := make()
	toClear(land)
	report(land, "done", "approved")
	ask := make()
	toClear(ask)
	report(ask, "question", "decide")
}

// The starter's approval stages ask the person who works them: clear, in-pr
// and merge each carry a choice template over the stage's outcomes, so a
// bead waiting for a yes shows among the person's decisions. In-pr's merged
// is left out of its options: only the hook fires it, when the pull request
// is merged. Driven here through an import of the starter into an empty
// yard: the three files parse and resolve under their flows, and a bead
// done into clear gets one open ask per member of the decisions group.
func TestStarterApprovalStagesAsk(t *testing.T) {
	home := t.TempDir()
	st := storetest.NewIn(t, home)
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	o := pack.Options{Depot: "web", Home: home}
	apply(t, st, build(t, st, load(t, starter), o), answers)

	// Each approval stage's file carries a choice template over the
	// stage's outcomes: what flow show lists, with no warning beside it.
	for _, tc := range []struct {
		flow, stage string
		options     []string
	}{
		{"starter.default", "clear", []string{"done", "changes", "question"}},
		{"starter.train", "in-pr", []string{"done", "changes", "question"}},
		{"starter.train", "merge", []string{"done", "rework", "question"}},
	} {
		f, err := st.GetFlow(ctx, tc.flow)
		if err != nil {
			t.Fatal(err)
		}
		sf, err := store.HomeStageFile(home, tc.flow, tc.stage)
		if err != nil {
			t.Fatal(err)
		}
		if sf == nil {
			t.Fatalf("%s %s has no stage file", tc.flow, tc.stage)
		}
		if broken := sf.BrokenAsk(f, tc.stage); broken != "" {
			t.Fatalf("%s %s: %s", tc.flow, tc.stage, broken)
		}
		tmpl, err := sf.Ask()
		if err != nil || tmpl == nil {
			t.Fatalf("%s %s ask = %+v, %v; want a template", tc.flow, tc.stage, tmpl, err)
		}
		if tmpl.Kind != store.AskChoice {
			t.Fatalf("%s %s kind = %q, want choice", tc.flow, tc.stage, tmpl.Kind)
		}
		var got []string
		for _, o := range tmpl.Options {
			got = append(got, o.Outcome)
		}
		if !slices.Equal(got, tc.options) {
			t.Fatalf("%s %s options = %v, want %v", tc.flow, tc.stage, got, tc.options)
		}
		if tmpl.Recommend != 0 {
			t.Fatalf("%s %s recommends option %d; the person decides", tc.flow, tc.stage, tmpl.Recommend)
		}
	}

	// A bead done into clear gets one open ask per member of the
	// decisions group, offering the stage's outcomes.
	for _, u := range []string{"ada", "hal"} {
		if _, err := st.AddUser(ctx, u, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 50, Members: []string{"ada", "hal"}}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if b, err = st.Done(ctx, b.ID, "done", "note", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if b.Stage != "clear" {
		t.Fatalf("stage = %s, want clear", b.Stage)
	}
	notes, err := st.Notes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	asks := store.ResolveAsks(notes, time.Now())
	if len(asks) != 2 {
		t.Fatalf("%d asks, want one per member of decisions", len(asks))
	}
	for i, r := range asks {
		if r.AskErr != nil || r.Note.Author != "yard" || r.State != store.AskOpen {
			t.Fatalf("ask %d: %+v", i, r)
		}
		a := r.Ask
		if a.Stage != "clear" || a.Entry == 0 {
			t.Fatalf("ask %d: stage %q entry %d", i, a.Stage, a.Entry)
		}
		var labels []string
		for _, o := range a.Options {
			labels = append(labels, o.Label)
		}
		if want := []string{"done", "changes", "question"}; !slices.Equal(labels, want) {
			t.Fatalf("ask %d: options %v, want %v", i, labels, want)
		}
		if want := []string{"done", "changes", "question"}; !slices.Equal(a.Move, want) {
			t.Fatalf("ask %d: move %v, want %v", i, a.Move, want)
		}
		if a.Recommend != 0 {
			t.Fatalf("ask %d: recommends option %d; the person decides", i, a.Recommend)
		}
	}
	if asks[0].Ask.To != "ada" || asks[1].Ask.To != "hal" {
		t.Fatalf("to = %s, %s", asks[0].Ask.To, asks[1].Ask.To)
	}
}

// Stage order is the file's, and so is the order of a stage's outcomes:
// neither a map's order nor the alphabet's. The first stage is where a
// bead starts, so sorting would move it (yardr-8tn3 was that trap in JSON
// flows). An outcome with a limit on rounds (aa, written as a mapping) keeps
// its place and carries the limit, and the stage the limit sends beads to is
// the stage's last transition, as the store's flow has it.
func TestStageOrderIsFileOrder(t *testing.T) {
	dir := writePack(t, map[string]string{"pack.yaml": `name: order
flows:
  default:
    stages:
      zulu:  { outcomes: { zz: mike, aa: { to: alpha, max: 2, then: bravo }, mm: zulu } }
      mike:  { manual: [alpha] }
      alpha: { manual: [zulu] }
      bravo: { end: true }
`})
	p, err := pack.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := p.Flows[0].StoreFlow("order.default")
	if want := []string{"zulu", "mike", "alpha", "bravo"}; !reflect.DeepEqual(f.Stages, want) {
		t.Fatalf("stages = %v, want %v", f.Stages, want)
	}
	if want := []store.Outcome{{Name: "zz", To: "mike"}, {Name: "aa", To: "alpha", Max: 2, Then: "bravo"}, {Name: "mm", To: "zulu"}}; !reflect.DeepEqual(f.Outcomes["zulu"], want) {
		t.Fatalf("outcomes = %v, want %v", f.Outcomes["zulu"], want)
	}
	if want := []string{"mike", "alpha", "zulu", "bravo"}; !reflect.DeepEqual(f.Transitions["zulu"], want) {
		t.Fatalf("transitions = %v, want %v", f.Transitions["zulu"], want)
	}
	// A flow file writes the stage a limit sends beads to with the limit, not
	// as an edge of its own.
	if got := f.String(); !strings.HasPrefix(got, "zulu -> mike on zz | alpha on aa max 2 then bravo | zulu on mm\n") {
		t.Fatalf("flow =\n%s", got)
	}
}

func TestStrictness(t *testing.T) {
	cases := []struct {
		name     string
		old, new string            // one replacement in small
		files    map[string]string // added or, with "", removed
		key, msg string
		line     int
	}{
		{name: "unknown top-level key", old: "groups:", new: "model: opus\ngroups:", key: "model", msg: "unknown key", line: 9},
		{name: "unknown stage key", old: "merged: { end: true }", new: "merged: { end: true, colour: red }", key: "flows.default.stages.merged.colour", msg: "unknown key", line: 8},
		{name: "unknown group key", old: "limit: 2", new: "limit: 2, model: opus", key: "groups.builders.model", msg: "unknown key", line: 10},
		{name: "unknown crew member key", old: "    role: roles/yardmaster.md", new: "    role: roles/yardmaster.md\n    identity: x", key: "crew.yardmaster.identity", msg: "unknown key", line: 16},
		// The sketch's own mistake: "on" for outcomes and for what a
		// crew member hears.
		{name: "boolean key on", old: "outcomes: { done: merged }", new: "on: { done: merged }", key: "flows.default.stages.review.on", msg: "reads the bare key on as a boolean", line: 6},
		{name: "boolean key yes as an outcome", old: "outcomes: { done: merged }", new: "outcomes: { yes: merged }", key: "flows.default.stages.review.outcomes.yes", msg: "as a boolean", line: 6},
		{name: "boolean key true", old: "    hears:", new: "    true:", key: "crew.yardmaster.true", msg: "as a boolean", line: 16},
		{name: "boolean word as a stage", old: "manual: [new]", new: "manual: [no]", key: "flows.default.stages.decide.manual", msg: "reads the bare word no as a boolean", line: 7},
		{name: "yes is not true", old: "end: true", new: "end: yes", key: "flows.default.stages.merged.end", msg: "want true or false", line: 8},
		{name: "undeclared group", old: "group: assembly", new: "group: refiners", key: "flows.default.stages.review.group", msg: "no group refiners is declared", line: 6},
		{name: "unknown outcome stage", old: "question: decide }", new: "question: triage }", key: "flows.default.stages.new.outcomes.question", msg: "no stage triage in this flow", line: 5},
		{name: "unknown manual stage", old: "manual: [new]", new: "manual: [backlog]", key: "flows.default.stages.decide.manual", msg: "no stage backlog in this flow", line: 7},
		// An outcome with a limit on rounds is a mapping: to, max, then.
		{name: "outcome is a list", old: "question: decide }", new: "question: [decide] }", key: "flows.default.stages.new.outcomes.question", msg: "want a string, got a list", line: 5},
		{name: "unknown key of an outcome", old: "question: decide }", new: "question: { to: decide, after: 3 } }", key: "flows.default.stages.new.outcomes.question.after", msg: "unknown key (known here: to, max, then)", line: 5},
		{name: "limited outcome without a to", old: "question: decide }", new: "question: { max: 2, then: merged } }", key: "flows.default.stages.new.outcomes.question.to", msg: "missing: an outcome written as a mapping says the stage it leads to", line: 5},
		{name: "limited outcome to a stage the flow lacks", old: "question: decide }", new: "question: { to: triage, max: 2, then: merged } }", key: "flows.default.stages.new.outcomes.question.to", msg: "no stage triage in this flow (stages: new, review, decide, merged)", line: 5},
		{name: "then names no stage of the flow", old: "question: decide }", new: "question: { to: decide, max: 2, then: triage } }", key: "flows.default.stages.new.outcomes.question.then", msg: "no stage triage in this flow (stages: new, review, decide, merged)", line: 5},
		{name: "max of none", old: "question: decide }", new: "question: { to: decide, max: 0, then: merged } }", key: "flows.default.stages.new.outcomes.question.max", msg: "want a number of rounds, 1 or more, got int 0", line: 5},
		{name: "max is no number", old: "question: decide }", new: "question: { to: decide, max: three, then: merged } }", key: "flows.default.stages.new.outcomes.question.max", msg: `want a number of rounds, 1 or more, got "three"`, line: 5},
		{name: "max without then", old: "question: decide }", new: "question: { to: decide, max: 2 } }", key: "flows.default.stages.new.outcomes.question.max", msg: "max needs a then", line: 5},
		{name: "then without max", old: "question: decide }", new: "question: { to: decide, then: merged } }", key: "flows.default.stages.new.outcomes.question.then", msg: "then needs a max", line: 5},
		{name: "then is where the outcome leads", old: "question: decide }", new: "question: { to: decide, max: 2, then: decide } }", key: "flows.default.stages.new.outcomes.question.then", msg: "then is the stage the outcome already leads to", line: 5},
		{name: "limit on done", old: "done: review,", new: "done: { to: review, max: 2, then: decide },", key: "flows.default.stages.new.outcomes.done.max", msg: "done takes no limit", line: 5},
		{name: "flow without an end", old: "merged: { end: true }", new: "merged: { group: people }", key: "flows.default.stages", msg: "has no end", line: 4},
		{name: "end stage with a way out", old: "merged: { end: true }", new: "merged: { end: true, manual: [new] }", key: "flows.default.stages.merged", msg: "closes the bead on entry", line: 8},
		{name: "group of two kinds", old: "people:   { people: true }", new: "people:   { people: true, run: scripts/land }", key: "groups.people", msg: "exactly one of", line: 12},
		{name: "group of no kind", old: "people:   { people: true }", new: "people:   { limit: 3 }", key: "groups.people", msg: "this one has none", line: 12},
		{name: "people false is no kind", old: "people:   { people: true }", new: "people:   { people: false }", key: "groups.people", msg: "this one has none", line: 12},
		{name: "limit below one", old: "limit: 2", new: "limit: 0", key: "groups.builders.limit", msg: "1 or more", line: 10},
		// env sets variables in the cmd of a script: an agent group has no
		// cmd and a people group runs nothing, and a group of the wrong
		// kind has its own error alone.
		{name: "env beside agent", old: "limit: 2", new: "limit: 2, env: { A: b }", key: "groups.builders.env", msg: "env is for a group with run:", line: 10},
		{name: "env beside people", old: "people:   { people: true }", new: "people:   { people: true, env: { A: b } }", key: "groups.people.env", msg: "env is for a group with run:", line: 12},
		{name: "env on a group of no kind", old: "people:   { people: true }", new: "people:   { limit: 3, env: { A: b } }", key: "groups.people", msg: "this one has none", line: 12},
		{name: "env on a group of two kinds", old: "people:   { people: true }", new: "people:   { people: true, run: scripts/land, env: { A: b } }", key: "groups.people", msg: "agent: <role file>, run: <script>, people: true; this one has people and run", line: 12},
		{name: "env is a mapping", old: "run: scripts/land", new: "run: scripts/land, env: [A]", key: "groups.assembly.env", msg: "want a mapping", line: 11},
		{name: "env name with a dash", old: "run: scripts/land", new: "run: scripts/land, env: { YARDR-LAND: squash }", key: "groups.assembly.env.YARDR-LAND", msg: `name "YARDR-LAND": use letters, digits and '_', and no digit first`, line: 11},
		{name: "env name with a digit first", old: "run: scripts/land", new: "run: scripts/land, env: { 1A: x }", key: "groups.assembly.env.1A", msg: `name "1A"`, line: 11},
		{name: "env name given twice", old: "run: scripts/land", new: "run: scripts/land, env: { A: x, A: y }", key: "groups.assembly.env.A", msg: "given twice", line: 11},
		{name: "env value is a number", old: "run: scripts/land", new: "run: scripts/land, env: { A: 1 }", key: "groups.assembly.env.A", msg: "want a string, got int 1", line: 11},
		{name: "env value is a boolean", old: "run: scripts/land", new: "run: scripts/land, env: { A: true }", key: "groups.assembly.env.A", msg: "as a boolean, not a string", line: 11},
		{name: "env value is a list", old: "run: scripts/land", new: "run: scripts/land, env: { A: [x] }", key: "groups.assembly.env.A", msg: "want a string, got a list", line: 11},
		{name: "env value is nothing", old: "run: scripts/land", new: "run: scripts/land, env: { A: }", key: "groups.assembly.env.A", msg: "want a string, got nothing", line: 11},
		{name: "env value holds a line break", old: "run: scripts/land", new: `run: scripts/land, env: { A: "x\ny" }`, key: "groups.assembly.env.A", msg: "U+000A, which is not printable", line: 11},
		{name: "env value holds an escape", old: "run: scripts/land", new: `run: scripts/land, env: { A: "x\e[2Ky" }`, key: "groups.assembly.env.A", msg: "U+001B, which is not printable", line: 11},
		{name: "missing role", files: map[string]string{"roles/builder.md": ""}, key: "groups.builders.agent", msg: "role roles/builder.md is missing", line: 10},
		{name: "missing crew member role", files: map[string]string{"roles/yardmaster.md": ""}, key: "crew.yardmaster.role", msg: "role roles/yardmaster.md is missing", line: 15},
		{name: "missing script", files: map[string]string{"scripts/land": ""}, key: "groups.assembly.run", msg: "script scripts/land is missing", line: 11},
		{name: "absolute path", old: "agent: roles/builder.md", new: "agent: /etc/passwd", key: "groups.builders.agent", msg: "relative to its directory, not absolute", line: 10},
		{name: "path leaving the pack", old: "agent: roles/builder.md", new: "agent: roles/../../builder.md", key: "groups.builders.agent", msg: "leaves the pack's directory", line: 10},
		{name: "missing stage file", old: "review: { group: assembly,", new: "review: { group: assembly, file: flows/review.md,", key: "flows.default.stages.review.file", msg: "stage file flows/review.md is missing", line: 6},
		{name: "empty stage file", old: "review: { group: assembly,", new: "review: { group: assembly, file: flows/review.md,", files: map[string]string{"flows/review.md": " \n"}, key: "flows.default.stages.review.file", msg: "stage file flows/review.md is empty", line: 6},
		{name: "stage file outside the pack", old: "review: { group: assembly,", new: "review: { group: assembly, file: ../review.md,", key: "flows.default.stages.review.file", msg: "leaves the pack's directory", line: 6},
		{name: "stage file of an end stage", old: "merged: { end: true }", new: "merged: { end: true, file: roles/yardmaster.md }", key: "flows.default.stages.merged", msg: "so it has no file", line: 8},
		{name: "stage no flow has", old: "stages: [decide]", new: "stages: [triage]", key: "crew.yardmaster.hears.stages", msg: "no flow of this pack has a stage triage", line: 18},
		{name: "unknown scope", old: "linear: involved", new: "linear: everything", key: "crew.yardmaster.subscribe.linear", msg: "one of involved, mentions, all", line: 21},
		{name: "a source no thread can be opened in", old: "    role: roles/yardmaster.md", new: "    role: roles/yardmaster.md\n    open_threads: [github, token]", key: "crew.yardmaster.open_threads", msg: "yardr cannot open threads in token (it can in none of the yard's sources)", line: 16},
		{name: "open_threads is a list", old: "    role: roles/yardmaster.md", new: "    role: roles/yardmaster.md\n    open_threads: linear", key: "crew.yardmaster.open_threads", msg: "want a list", line: 16},
		{name: "key given twice", old: "groups:\n", new: "groups:\n  people: { people: true }\n", key: "groups.people", msg: "given twice", line: 13},
		{name: "no name", old: "name: small\n", new: "", key: "name", msg: "missing", line: 1},
		{name: "kind of no depot", old: "name: small\n", new: "name: small\nkind: svn\n", key: "kind", msg: `kind "svn": a pack is for a depot of kind git or dir`, line: 2},
		{name: "kind is a string", old: "name: small\n", new: "name: small\nkind: [git, dir]\n", key: "kind", msg: "want a string", line: 2},
		// api names the pack API the pack is written for. One above what
		// this yardr reads is the pack's one mistake, wherever the key
		// stands and whatever else is wrong with the pack: the rest would
		// be this yardr's mistakes, not the pack's.
		{name: "api above what this yardr reads", old: "name: small\n", new: "name: small\napi: 4\n", key: "api", msg: "this pack is written for pack API 4, and this yardr reads up to 3: upgrade yardr (yardr upgrade)", line: 2},
		{name: "api far above", old: "name: small\n", new: "name: small\napi: 1000\n", key: "api", msg: "this pack is written for pack API 1000, and this yardr reads up to 3", line: 2},
		{name: "api above, beside a key this yardr lacks", old: "name: small\n", new: "name: small\napi: 4\nmodel: opus\n", key: "api", msg: "upgrade yardr", line: 2},
		{name: "api above, beside a file that is missing", old: "name: small\n", new: "name: small\napi: 4\n", files: map[string]string{"roles/builder.md": ""}, key: "api", msg: "upgrade yardr", line: 2},
		{name: "api above, last in the file and beside a scope that is wrong", old: "      linear: involved\n", new: "      linear: everything\napi: 4\n", key: "api", msg: "this pack is written for pack API 4", line: 22},
		{name: "api of none", old: "name: small\n", new: "name: small\napi: 0\n", key: "api", msg: "want a whole number, 1 or more (the pack API this pack is written for, docs/pack-api.md), got int 0", line: 2},
		{name: "api below zero", old: "name: small\n", new: "name: small\napi: -1\n", key: "api", msg: "got int -1", line: 2},
		{name: "api is a string", old: "name: small\n", new: "name: small\napi: \"1\"\n", key: "api", msg: `got "1"`, line: 2},
		{name: "api is a fraction", old: "name: small\n", new: "name: small\napi: 1.5\n", key: "api", msg: "got float 1.5", line: 2},
		{name: "api is a list", old: "name: small\n", new: "name: small\napi: [1]\n", key: "api", msg: "got a list", line: 2},
		{name: "api is nothing", old: "name: small\n", new: "name: small\napi:\n", key: "api", msg: "got nothing", line: 2},
		{name: "api given twice", old: "name: small\n", new: "name: small\napi: 1\napi: 1\n", key: "api", msg: "given twice", line: 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := small
			if c.old != "" {
				if !strings.Contains(text, c.old) {
					t.Fatalf("the case replaces %q, which the pack does not have", c.old)
				}
				text = strings.Replace(text, c.old, c.new, 1)
			}
			files := smallFiles(text)
			for name, content := range c.files {
				if content == "" {
					delete(files, name)
				} else {
					files[name] = content
				}
			}
			dir := writePack(t, files)
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("Load = %v, want the pack's errors", err)
			}
			if len(errs) != 1 {
				t.Fatalf("errors = %v, want one", errs)
			}
			e := errs[0]
			if e.File != filepath.Join(dir, "pack.yaml") || e.Key != c.key || e.Line != c.line || !strings.Contains(e.Msg, c.msg) {
				t.Fatalf("error = %s\nwant key %s, line %d, message containing %q", e, c.key, c.line, c.msg)
			}
			// The printed error carries all three.
			if want := filepath.Join(dir, "pack.yaml") + ":"; !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), c.key+": ") {
				t.Fatalf("printed %q", err.Error())
			}
		})
	}
}

// A script group's env is the options of its script as the pack names them:
// strings as they are, wherever in the group the key stands, and nothing for
// an empty mapping, which is no mistake beside any kind of group.
func TestGroupEnvIsRead(t *testing.T) {
	for _, c := range []struct {
		name, old, new string
		group          string // the group read
		want           map[string]string
	}{
		{name: "beside run", old: "run: scripts/land", new: "run: scripts/land, env: { YARDR_LAND: squash }", group: "assembly", want: map[string]string{"YARDR_LAND": "squash"}},
		{name: "before run", old: "{ run: scripts/land }", new: "{ env: { YARDR_LAND: squash }, run: scripts/land }", group: "assembly", want: map[string]string{"YARDR_LAND": "squash"}},
		{name: "strings as they are", old: "run: scripts/land", new: `run: scripts/land, env: { a_1: "hello 'big' world", _B: "0", C: "$HOME; rm -rf /" }`, group: "assembly",
			want: map[string]string{"a_1": "hello 'big' world", "_B": "0", "C": "$HOME; rm -rf /"}},
		{name: "an empty mapping beside run", old: "run: scripts/land", new: "run: scripts/land, env: {}", group: "assembly"},
		{name: "an empty mapping beside agent", old: "limit: 2", new: "limit: 2, env: {}", group: "builders"},
		{name: "an empty mapping beside people", old: "people:   { people: true }", new: "people:   { people: true, env: {} }", group: "people"},
		{name: "none", old: "name: small", new: "name: small", group: "assembly"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := pack.LoadDir(writePack(t, smallFiles(strings.Replace(small, c.old, c.new, 1))))
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(p.Groups, func(g pack.Group) bool { return g.Name == c.group })
			if i < 0 {
				t.Fatalf("the pack has no group %s", c.group)
			}
			if got := p.Groups[i].Env; !reflect.DeepEqual(got, c.want) {
				t.Errorf("env = %#v, want %#v", got, c.want)
			}
		})
	}
}

// A pack says which kind of depot it is for, or says nothing and fits
// either: the two yardr carries each say theirs.
func TestKindIsTheDepotKindThePackIsFor(t *testing.T) {
	for text, want := range map[string]string{
		small: "",
		strings.Replace(small, "name: small\n", "name: small\nkind: git\n", 1): store.DepotGit,
		strings.Replace(small, "name: small\n", "name: small\nkind: dir\n", 1): store.DepotDir,
	} {
		p, err := pack.LoadDir(writePack(t, smallFiles(text)))
		if err != nil || p.Kind != want {
			t.Errorf("kind = %q, %v, want %q", p.Kind, err, want)
		}
	}
	for name, want := range map[string]string{packs.Starter: store.DepotGit, packs.Dir: store.DepotDir} {
		sub, err := fs.Sub(packs.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		p, err := pack.Load(sub, name)
		if err != nil || p.Kind != want {
			t.Errorf("pack %s: kind = %q, %v, want %q", name, p.Kind, err, want)
		}
	}
}

// A pack says which pack API it is written for, or says nothing and is
// written for API 1, as every pack was before the key.
func TestAPIIsTheNumberThePackSaysItIsWrittenFor(t *testing.T) {
	if pack.API != 3 {
		t.Fatalf("pack.API = %d: this test knows APIs 1, 2 and 3, and so does the page's list of what each number added", pack.API)
	}
	for name, tc := range map[string]struct {
		text string
		api  int
	}{
		"no key":               {text: small, api: 1},
		"api first":            {text: strings.Replace(small, "name: small\n", "name: small\napi: 1\n", 1), api: 1},
		"api 2":                {text: strings.Replace(small, "name: small\n", "name: small\napi: 2\n", 1), api: 2},
		"api 3":                {text: strings.Replace(small, "name: small\n", "name: small\napi: 3\n", 1), api: 3},
		"api last in the file": {text: small + "api: 1\n", api: 1},
	} {
		if p, err := pack.LoadDir(writePack(t, smallFiles(tc.text))); err != nil || p.API != tc.api {
			t.Errorf("%s: api = %d, %v, want %d", name, p.API, err, tc.api)
		}
	}
}

// The page lists what each API number added, under a heading of its own, and
// this yardr reads up to the last of them: a number raised without its
// heading, or a heading without the number, fails here, so the page cannot
// say less than yardr reads or more.
func TestPackAPIPageListsEveryNumberYardrReads(t *testing.T) {
	section, ok := docs.Section("pack-api", "what each number added")
	if !ok {
		t.Fatal("docs/pack-api.md is not a page the binary carries, or has no section on what each number added")
	}
	var got []int
	for line := range strings.Lines(section) {
		if rest, ok := strings.CutPrefix(line, "### API "); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				t.Fatalf("the heading %q is no API number", strings.TrimSpace(line))
			}
			got = append(got, n)
		}
	}
	want := make([]int, pack.API)
	for i := range want {
		want[i] = i + 1
	}
	if !slices.Equal(got, want) {
		t.Errorf("the page has headings for API %v, and this yardr reads %v (pack.API is %d)", got, want, pack.API)
	}
}

// A quoted key is a name whatever it spells, so a pack that must call an
// outcome "yes" can.
func TestQuotedBooleanWordIsAName(t *testing.T) {
	text := strings.Replace(small, "outcomes: { done: merged }", `outcomes: { "yes": merged }`, 1)
	p, err := pack.LoadDir(writePack(t, smallFiles(text)))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Flows[0].Stages[1].Outcomes; len(got) != 1 || got[0].Name != "yes" {
		t.Fatalf("outcomes = %v", got)
	}
}

// Every mistake is named in one run, in file order.
func TestAllErrorsAreReported(t *testing.T) {
	text := strings.NewReplacer("group: assembly", "group: refiners", "linear: involved", "linear: everything").Replace(small)
	_, err := pack.LoadDir(writePack(t, smallFiles(text)))
	var errs pack.Errors
	if !errors.As(err, &errs) || len(errs) != 2 || errs[0].Line > errs[1].Line {
		t.Fatalf("errors = %v", err)
	}
}

// A script is installed executable because a run: group names it, whatever
// its mode in the pack: a pack yardr embeds has no modes, and a checkout
// may have lost them.
func TestScriptNeedNotBeExecutableInThePack(t *testing.T) {
	dir := writePack(t, smallFiles(small))
	if err := os.Chmod(filepath.Join(dir, "scripts", "land"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pack.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir = %v", err)
	}
}

// A link could name a file outside the pack, which a clone elsewhere lacks.
func TestRoleMustBeARegularFile(t *testing.T) {
	files := smallFiles(small)
	delete(files, "roles/builder.md")
	dir := writePack(t, files)
	outside := filepath.Join(t.TempDir(), "builder.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "roles", "builder.md")); err != nil {
		t.Skip(err)
	}
	if _, err := pack.LoadDir(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Load = %v", err)
	}
}

func TestNoPackFile(t *testing.T) {
	if _, err := pack.LoadDir(t.TempDir()); err == nil || !strings.Contains(err.Error(), "a pack is a directory with a pack.yaml") {
		t.Fatalf("Load = %v", err)
	}
}

// Every pack's merge-local is a copy of the reference assembly
// (contrib/assembly/merge-local), which a yard set up by hand installs from
// there: a change to one must be made to all five. An embedded file has no
// mode, so that each is executable in the source is held here.
func TestPackMergeLocalsAreTheReferenceAssembly(t *testing.T) {
	want, err := os.ReadFile("../../contrib/assembly/merge-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{starter, design, contributors, website} {
		path := filepath.Join(p, "scripts", "merge-local")
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from contrib/assembly/merge-local: change all five", path)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s must be executable: %v", path, err)
		}
	}
}

// The website pack's deploy script is the reference deploy's copy, as its
// merge-local is the assembler's: a depot that takes the pack deploys with
// what contrib/deploy carries. Both scripts are executable in the pack,
// since an import installs a run: group from them.
func TestWebsiteDeployVoidIsTheReferenceDeploy(t *testing.T) {
	got, err := os.ReadFile(filepath.Join(website, "scripts", "deploy-void"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../contrib/deploy/deploy-void")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Error("packs/website/scripts/deploy-void differs from contrib/deploy/deploy-void: change both")
	}
	for _, name := range []string{"deploy-void", "merge-local"} {
		info, err := os.Stat(filepath.Join(website, "scripts", name))
		if err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Errorf("packs/website/scripts/%s must be executable: %v", name, err)
		}
	}
}

// The builder's screenshot recipe quotes the reference script's flags, so
// the two change together: the size the role names is the window the script
// shoots, and the flags it quotes are the script's.
func TestBuilderRoleQuotesTheShotFlags(t *testing.T) {
	role, err := os.ReadFile(filepath.Join(starter, "roles", "builder.md"))
	if err != nil {
		t.Fatal(err)
	}
	shot, err := os.ReadFile("../../contrib/shot/shot")
	if err != nil {
		t.Fatal(err)
	}
	size := regexp.MustCompile(`--window-size=(\d+),(\d+)`).FindStringSubmatch(string(shot))
	if size == nil {
		t.Fatal("contrib/shot/shot names no --window-size=W,H")
	}
	if want := size[1] + "x" + size[2]; !strings.Contains(string(role), want) {
		t.Errorf("packs/starter/roles/builder.md does not name %s, the window contrib/shot/shot shoots", want)
	}
	for _, flag := range []string{"--headless", "--screenshot=", "--window-size=" + size[1] + "," + size[2]} {
		if !strings.Contains(string(shot), flag) {
			t.Errorf("contrib/shot/shot does not carry %s", flag)
		}
		if !strings.Contains(string(role), flag) {
			t.Errorf("packs/starter/roles/builder.md does not quote %s", flag)
		}
	}
}

// The builder's recipe names the shot's Chrome bound, so the two change
// together: the bound the role promises is the script's SHOT_TIMEOUT
// default.
func TestBuilderRoleNamesTheShotTimeout(t *testing.T) {
	role, err := os.ReadFile(filepath.Join(starter, "roles", "builder.md"))
	if err != nil {
		t.Fatal(err)
	}
	shot, err := os.ReadFile("../../contrib/shot/shot")
	if err != nil {
		t.Fatal(err)
	}
	def := regexp.MustCompile(`SHOT_TIMEOUT:=(\d+)`).FindStringSubmatch(string(shot))
	if def == nil {
		t.Fatal("contrib/shot/shot names no SHOT_TIMEOUT default")
	}
	if def[1] != "60" {
		t.Errorf("contrib/shot/shot bounds Chrome at %s s, want the minute the role promises", def[1])
	}
	for _, want := range []string{"SHOT_TIMEOUT", "gets a minute"} {
		if !strings.Contains(string(role), want) {
			t.Errorf("packs/starter/roles/builder.md does not name %s", want)
		}
	}
}

// A stage's file is read as the pack's other paths are: relative to its
// directory, cleaned, and a stage without the key has none.
func TestStageFileIsRead(t *testing.T) {
	files := smallFiles(strings.Replace(small, "review: { group: assembly,", "review: { group: assembly, file: ./flows/review.md,", 1))
	files["flows/review.md"] = "review it\n"
	p, err := pack.LoadDir(writePack(t, files))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Flows[0].Stages {
		if want := map[string]string{"review": "flows/review.md"}[s.Name]; s.File != want {
			t.Errorf("stage %s names the stage file %q, want %q", s.Name, s.File, want)
		}
	}
	// A stage file is no role or script: it goes with its flow.
	if slices.Contains(p.Files(), "flows/review.md") {
		t.Fatalf("files = %v", p.Files())
	}
}

// A role or stage file may name the operator and the pack's crew in
// double braces, and nothing else: an import fills those, so any other
// placeholder would reach an agent as written. Each is named by file and
// line. A script is copied as it is, so its braces are its own.
func TestUnknownPlaceholderIsRefused(t *testing.T) {
	files := smallFiles(strings.Replace(small, "new:    { group: builders,", "new:    { group: builders, file: flows/new.md,", 1))
	files["roles/builder.md"] = "ask {{operator}}\nmail {{yardmaster}}\n"
	files["flows/new.md"] = "{{operator}} decides\n"
	files["scripts/land"] = "#!/bin/sh\necho {{.Branch}} {{lead}}\n"
	dir := writePack(t, files)
	if _, err := pack.LoadDir(dir); err != nil {
		t.Fatalf("a pack using only its own placeholders: %v", err)
	}

	files["roles/builder.md"] = "ask {{operator}}\nmail {{lead}}, or {{ operator }}\n"
	files["flows/new.md"] = "one\ntwo\n{{boss}} decides\n"
	dir = writePack(t, files)
	_, err := pack.LoadDir(dir)
	var errs pack.Errors
	if !errors.As(err, &errs) || len(errs) != 3 {
		t.Fatalf("Load = %v, want three errors", err)
	}
	for i, want := range []pack.Error{
		{File: filepath.Join(dir, "roles", "builder.md"), Line: 2, Msg: "placeholder {{lead}}: an import fills {{operator}}, {{yardmaster}} (the operator and the pack's crew) and nothing else"},
		{File: filepath.Join(dir, "roles", "builder.md"), Line: 2, Msg: "placeholder {{ operator }}: an import fills {{operator}}, {{yardmaster}} (the operator and the pack's crew) and nothing else"},
		{File: filepath.Join(dir, "flows", "new.md"), Line: 3, Msg: "placeholder {{boss}}: an import fills {{operator}}, {{yardmaster}} (the operator and the pack's crew) and nothing else"},
	} {
		if errs[i] != want {
			t.Errorf("error %d = %+v\nwant      %+v", i, errs[i], want)
		}
	}
	if want := filepath.Join(dir, "roles", "builder.md") + ":2: placeholder {{lead}}"; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("printed %q, want it to start %q", err.Error(), want)
	}
}

// The starter uses the placeholders it may: its roles name the operator and
// its crew member, which is what an import fills.
func TestStarterNamesItsPlaceholders(t *testing.T) {
	p, err := pack.LoadDir(starter)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Placeholders(), []string{"operator", "yardmaster"}; !slices.Equal(got, want) {
		t.Fatalf("placeholders = %v, want %v", got, want)
	}
	text, _ := os.ReadFile(filepath.Join(starter, "roles", "builder.md"))
	if !strings.Contains(string(text), "{{operator}}") || !strings.Contains(string(text), "yardr mail {{yardmaster}} ") {
		t.Fatal("packs/starter/roles/builder.md no longer names {{operator}} and {{yardmaster}}")
	}
}

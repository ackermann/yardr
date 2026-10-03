package pack_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// starter is the pack the repository ships, and these tests' fixture.
const starter = "../../packs/starter"

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
      review: { group: refinery, outcomes: { done: merged } }
      decide: { group: people, manual: [new] }
      merged: { end: true }
groups:
  builders: { agent: roles/builder.md, limit: 2 }
  refinery: { run: scripts/land }
  people:   { people: true }
residents:
  mayor:
    role: roles/mayor.md
    hears:
      events: [held]
      stages: [decide]
      done: true
    subscribe:
      linear: involved
`

func smallFiles(yaml string) map[string]string {
	return map[string]string{"pack.yaml": yaml, "roles/builder.md": "build\n", "roles/mayor.md": "mayor\n", "scripts/land": "#!/bin/sh\n"}
}

func TestStarterPackLoads(t *testing.T) {
	p, err := pack.Load(starter)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "starter" || len(p.Flows) != 1 || len(p.Groups) != 5 || len(p.Residents) != 1 {
		t.Fatalf("starter = %+v", p)
	}
	f := p.Flows[0]
	var stages []string
	for _, s := range f.Stages {
		stages = append(stages, s.Name)
	}
	if want := []string{"backlog", "new", "review", "approved", "decide", "merged"}; !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages = %v, want the file's order %v", stages, want)
	}
	review := f.Stages[2]
	if want := []store.Outcome{{Name: "done", To: "approved"}, {Name: "changes", To: "new"}, {Name: "question", To: "decide"}}; !reflect.DeepEqual(review.Outcomes, want) {
		t.Fatalf("review outcomes = %v, want the file's order %v", review.Outcomes, want)
	}
	if want := []string{"roles/builder.md", "roles/mayor.md", "roles/reviewer.md", "scripts/merge-local"}; !reflect.DeepEqual(p.Files(), want) {
		t.Fatalf("files = %v, want %v", p.Files(), want)
	}
	m := p.Residents[0]
	if m.Name != "mayor" || m.Role != "roles/mayor.md" || !m.Done || !reflect.DeepEqual(m.Stages, []string{"decide"}) ||
		!reflect.DeepEqual(m.Subscribe, []pack.Source{{Name: "linear", Scope: "involved"}}) || len(m.Events) != 12 {
		t.Fatalf("mayor = %+v", m)
	}
}

// The starter's flow, as the store would keep it, is the flow yardr rig
// init writes: the same rules, written in the pack's format.
func TestStarterFlowIsTheRigInitFlow(t *testing.T) {
	p, err := pack.Load(starter)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Flows[0].StoreFlow("x")
	want := `backlog -> new | decide
new -> review on done | decide on question | backlog
review -> approved on done | new on changes | decide on question
approved -> merged on done | new on failed | decide on question
decide -> backlog | new
merged terminal
`
	if got.String() != want {
		t.Fatalf("flow =\n%s\nwant\n%s", got.String(), want)
	}
}

// Stage order is the file's, and so is the order of a stage's outcomes:
// neither a map's order nor the alphabet's. The first stage is where a
// bead starts, so sorting would move it (yardr-8tn3 was that trap in JSON
// flows).
func TestStageOrderIsFileOrder(t *testing.T) {
	dir := writePack(t, map[string]string{"pack.yaml": `name: order
flows:
  default:
    stages:
      zulu:  { outcomes: { zz: mike, aa: alpha, mm: zulu } }
      mike:  { manual: [alpha] }
      alpha: { manual: [zulu] }
      bravo: { end: true }
`})
	p, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := p.Flows[0].StoreFlow("order.default")
	if want := []string{"zulu", "mike", "alpha", "bravo"}; !reflect.DeepEqual(f.Stages, want) {
		t.Fatalf("stages = %v, want %v", f.Stages, want)
	}
	if want := []store.Outcome{{Name: "zz", To: "mike"}, {Name: "aa", To: "alpha"}, {Name: "mm", To: "zulu"}}; !reflect.DeepEqual(f.Outcomes["zulu"], want) {
		t.Fatalf("outcomes = %v, want %v", f.Outcomes["zulu"], want)
	}
	if want := []string{"mike", "alpha", "zulu"}; !reflect.DeepEqual(f.Transitions["zulu"], want) {
		t.Fatalf("transitions = %v, want %v", f.Transitions["zulu"], want)
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
		{name: "unknown resident key", old: "    role: roles/mayor.md", new: "    role: roles/mayor.md\n    identity: x", key: "residents.mayor.identity", msg: "unknown key", line: 16},
		// The sketch's own mistake: "on" for outcomes and for what a
		// resident hears.
		{name: "boolean key on", old: "outcomes: { done: merged }", new: "on: { done: merged }", key: "flows.default.stages.review.on", msg: "reads the bare key on as a boolean", line: 6},
		{name: "boolean key yes as an outcome", old: "outcomes: { done: merged }", new: "outcomes: { yes: merged }", key: "flows.default.stages.review.outcomes.yes", msg: "as a boolean", line: 6},
		{name: "boolean key true", old: "    hears:", new: "    true:", key: "residents.mayor.true", msg: "as a boolean", line: 16},
		{name: "boolean word as a stage", old: "manual: [new]", new: "manual: [no]", key: "flows.default.stages.decide.manual", msg: "reads the bare word no as a boolean", line: 7},
		{name: "yes is not true", old: "end: true", new: "end: yes", key: "flows.default.stages.merged.end", msg: "want true or false", line: 8},
		{name: "undeclared group", old: "group: refinery", new: "group: refiners", key: "flows.default.stages.review.group", msg: "no group refiners is declared", line: 6},
		{name: "unknown outcome stage", old: "question: decide }", new: "question: triage }", key: "flows.default.stages.new.outcomes.question", msg: "no stage triage in this flow", line: 5},
		{name: "unknown manual stage", old: "manual: [new]", new: "manual: [backlog]", key: "flows.default.stages.decide.manual", msg: "no stage backlog in this flow", line: 7},
		{name: "flow without an end", old: "merged: { end: true }", new: "merged: { group: people }", key: "flows.default.stages", msg: "has no end", line: 4},
		{name: "end stage with a way out", old: "merged: { end: true }", new: "merged: { end: true, manual: [new] }", key: "flows.default.stages.merged", msg: "closes the bead on entry", line: 8},
		{name: "group of two kinds", old: "people:   { people: true }", new: "people:   { people: true, run: scripts/land }", key: "groups.people", msg: "exactly one of", line: 12},
		{name: "group of no kind", old: "people:   { people: true }", new: "people:   { limit: 3 }", key: "groups.people", msg: "this one has none", line: 12},
		{name: "people false is no kind", old: "people:   { people: true }", new: "people:   { people: false }", key: "groups.people", msg: "this one has none", line: 12},
		{name: "limit below one", old: "limit: 2", new: "limit: 0", key: "groups.builders.limit", msg: "1 or more", line: 10},
		{name: "missing role", files: map[string]string{"roles/builder.md": ""}, key: "groups.builders.agent", msg: "role roles/builder.md is missing", line: 10},
		{name: "missing resident role", files: map[string]string{"roles/mayor.md": ""}, key: "residents.mayor.role", msg: "role roles/mayor.md is missing", line: 15},
		{name: "missing script", files: map[string]string{"scripts/land": ""}, key: "groups.refinery.run", msg: "script scripts/land is missing", line: 11},
		{name: "absolute path", old: "agent: roles/builder.md", new: "agent: /etc/passwd", key: "groups.builders.agent", msg: "relative to its directory, not absolute", line: 10},
		{name: "path leaving the pack", old: "agent: roles/builder.md", new: "agent: roles/../../builder.md", key: "groups.builders.agent", msg: "leaves the pack's directory", line: 10},
		{name: "stage no flow has", old: "stages: [decide]", new: "stages: [triage]", key: "residents.mayor.hears.stages", msg: "no flow of this pack has a stage triage", line: 18},
		{name: "source without an adapter", old: "linear: involved", new: "slack: involved", key: "residents.mayor.subscribe.slack", msg: "no adapter for source slack", line: 21},
		{name: "unknown scope", old: "linear: involved", new: "linear: everything", key: "residents.mayor.subscribe.linear", msg: "one of involved, mentions, all", line: 21},
		{name: "key given twice", old: "groups:\n", new: "groups:\n  people: { people: true }\n", key: "groups.people", msg: "given twice", line: 13},
		{name: "no name", old: "name: small\n", new: "", key: "name", msg: "missing", line: 1},
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
			_, err := pack.Load(dir)
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

// A quoted key is a name whatever it spells, so a pack that must call an
// outcome "yes" can.
func TestQuotedBooleanWordIsAName(t *testing.T) {
	text := strings.Replace(small, "outcomes: { done: merged }", `outcomes: { "yes": merged }`, 1)
	p, err := pack.Load(writePack(t, smallFiles(text)))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Flows[0].Stages[1].Outcomes; len(got) != 1 || got[0].Name != "yes" {
		t.Fatalf("outcomes = %v", got)
	}
}

// Every mistake is named in one run, in file order.
func TestAllErrorsAreReported(t *testing.T) {
	text := strings.NewReplacer("group: refinery", "group: refiners", "linear: involved", "slack: involved").Replace(small)
	_, err := pack.Load(writePack(t, smallFiles(text)))
	var errs pack.Errors
	if !errors.As(err, &errs) || len(errs) != 2 || errs[0].Line > errs[1].Line {
		t.Fatalf("errors = %v", err)
	}
}

func TestScriptMustBeExecutable(t *testing.T) {
	dir := writePack(t, smallFiles(small))
	if err := os.Chmod(filepath.Join(dir, "scripts", "land"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pack.Load(dir); err == nil || !strings.Contains(err.Error(), "groups.refinery.run: script scripts/land is not executable") {
		t.Fatalf("Load = %v", err)
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
	if _, err := pack.Load(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Load = %v", err)
	}
}

func TestNoPackFile(t *testing.T) {
	if _, err := pack.Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "a pack is a directory with a pack.yaml") {
		t.Fatalf("Load = %v", err)
	}
}

// The starter's roles and its merge-local are copies of the roles yardr
// embeds (roles/*.md) and of the reference refinery. Until one is read
// from the other, a change to either must be made to both.
func TestStarterMatchesItsSources(t *testing.T) {
	pairs := map[string]string{"scripts/merge-local": "../../contrib/refinery/merge-local"}
	roles, err := filepath.Glob("../../roles/*.md")
	if err != nil || len(roles) == 0 {
		t.Fatalf("no roles/*.md: %v", err)
	}
	for _, r := range roles {
		pairs["roles/"+filepath.Base(r)] = r
	}
	for in, source := range pairs {
		got, err := os.ReadFile(filepath.Join(starter, filepath.FromSlash(in)))
		if err != nil {
			t.Errorf("the starter pack lacks %s, a copy of %s: %v", in, source, err)
			continue
		}
		want, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("packs/starter/%s differs from %s: change both", in, strings.TrimPrefix(source, "../../"))
		}
	}
	have, _ := filepath.Glob(filepath.Join(starter, "roles", "*.md"))
	if len(have) != len(roles) {
		t.Errorf("packs/starter/roles has %d roles, roles/ has %d", len(have), len(roles))
	}
	info, err := os.Stat(filepath.Join(starter, "scripts", "merge-local"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("packs/starter/scripts/merge-local must be executable: %v", err)
	}
}

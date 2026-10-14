package pack_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
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
	if p.Name != "starter" || len(p.Flows) != 2 || len(p.Groups) != 5 || len(p.Crew) != 1 {
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
	if want := []string{"roles/builder.md", "roles/reviewer.md", "roles/yardmaster.md", "scripts/merge-local"}; !reflect.DeepEqual(p.Files(), want) {
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
	if m.Name != "yardmaster" || m.Role != "roles/yardmaster.md" || !m.Done || !reflect.DeepEqual(m.Stages, []string{"decide"}) ||
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
// init writes: the same rules, written in the pack's format.
func TestStarterFlowIsTheDepotInitFlow(t *testing.T) {
	p, err := pack.LoadDir(starter)
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
	p, err := pack.LoadDir(dir)
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
		{name: "flow without an end", old: "merged: { end: true }", new: "merged: { group: people }", key: "flows.default.stages", msg: "has no end", line: 4},
		{name: "end stage with a way out", old: "merged: { end: true }", new: "merged: { end: true, manual: [new] }", key: "flows.default.stages.merged", msg: "closes the bead on entry", line: 8},
		{name: "group of two kinds", old: "people:   { people: true }", new: "people:   { people: true, run: scripts/land }", key: "groups.people", msg: "exactly one of", line: 12},
		{name: "group of no kind", old: "people:   { people: true }", new: "people:   { limit: 3 }", key: "groups.people", msg: "this one has none", line: 12},
		{name: "people false is no kind", old: "people:   { people: true }", new: "people:   { people: false }", key: "groups.people", msg: "this one has none", line: 12},
		{name: "limit below one", old: "limit: 2", new: "limit: 0", key: "groups.builders.limit", msg: "1 or more", line: 10},
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

// The starter's merge-local is a copy of the reference assembly
// (contrib/assembly/merge-local), which a yard set up by hand installs from
// there: a change to either must be made to both. An embedded file has no
// mode, so that it is executable in the source is held here.
func TestStarterMergeLocalIsTheReferenceAssembly(t *testing.T) {
	got, err := os.ReadFile(filepath.Join(starter, "scripts", "merge-local"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../contrib/assembly/merge-local")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Error("packs/starter/scripts/merge-local differs from contrib/assembly/merge-local: change both")
	}
	info, err := os.Stat(filepath.Join(starter, "scripts", "merge-local"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("packs/starter/scripts/merge-local must be executable: %v", err)
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

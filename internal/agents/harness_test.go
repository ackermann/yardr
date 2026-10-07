package agents

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// The files yardr is built with are read (a file that is not would have
// stopped the test binary at its start), every one is of the shape this
// yardr reads, and they are offered in the order the forms have always had,
// claude first: that one is what a yard is created on when nothing says
// otherwise.
func TestHarnessFiles(t *testing.T) {
	var kinds []string
	for _, h := range Harnesses {
		kinds = append(kinds, h.Kind)
		if h.Version != FileVersion || h.Label == "" {
			t.Errorf("%s: version %d, label %q", h.Kind, h.Version, h.Label)
		}
		if modelLists[h.Kind] == nil {
			t.Errorf("%s has a file and no way to list its models (modelLists)", h.Kind)
		}
		if h.Flags.Model != "--model" {
			t.Errorf("%s model flag %q, want --model", h.Kind, h.Flags.Model)
		}
	}
	if want := []string{"claude", "codex", "grok", "kimi", "opencode", "pi"}; !slices.Equal(kinds, want) {
		t.Errorf("kinds with a file: %v, want %v", kinds, want)
	}
	for kind := range modelLists {
		if _, ok := Lookup(kind); !ok {
			t.Errorf("%s lists its models and has no file", kind)
		}
	}
}

// A kind with no file is the unknown kind: no marks, no error lines, no
// flags, no model source, no dialog answered.
func TestKindWithNoFile(t *testing.T) {
	for _, kind := range []string{"gemini", "cursor", "nope", ""} {
		h, ok := Lookup(kind)
		if ok || h.Marks != (Marks{}) || h.Errors != nil || h.Model != nil || h.Trust || h.Label != "" ||
			h.Flags.Unattended != nil || h.Flags.Always != nil || h.Flags.Permissions != nil {
			t.Errorf("%q: %v, %+v; want nothing known", kind, ok, h)
		}
		if UnattendedArgs(kind) != nil || VersionArgs(kind) != nil || OneShotArgs(kind, "x") != nil {
			t.Errorf("%q has flags", kind)
		}
		if ms, err := h.Models(t.Context()); ms != nil || err != nil {
			t.Errorf("%q lists models: %v, %v", kind, ms, err)
		}
	}
	if got := InstallHint("cursor"); got != "see cursor's own documentation; it must be on PATH as cursor-agent" {
		t.Errorf("install hint of a kind with no file: %q", got)
	}
	if got := InstallHint("kimi"); got != "see Kimi's own documentation; it must be on PATH as kimi" {
		t.Errorf("install hint of a kind whose file names none: %q", got)
	}
}

func TestKindConfigDefault(t *testing.T) {
	for _, c := range []struct {
		cfg  map[string]any
		want string
	}{
		{nil, DefaultKind},
		{map[string]any{}, DefaultKind},
		{map[string]any{"kind": ""}, DefaultKind},
		{map[string]any{"kind": "codex"}, "codex"},
		{map[string]any{"kind": 42}, DefaultKind},
	} {
		if got := Kind(c.cfg); got != c.want {
			t.Errorf("Kind(%v) = %q, want %q", c.cfg, got, c.want)
		}
	}
}

// What only one file says: the dialog yardr answers is Claude Code's, and
// the flag every agent gets is Codex's.
func TestWhatOneKindSays(t *testing.T) {
	for _, h := range Harnesses {
		if h.Trust != (h.Kind == "claude") {
			t.Errorf("%s: trust %v", h.Kind, h.Trust)
		}
		want := []string(nil)
		if h.Kind == "codex" {
			want = []string{"--no-daemon"}
		}
		if !slices.Equal(h.Flags.Always, want) {
			t.Errorf("%s: always %v, want %v", h.Kind, h.Flags.Always, want)
		}
	}
}

func load(t *testing.T, files map[string]string) ([]Harness, error) {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return Load(fsys)
}

// A kind with a file that the display order does not name follows those it
// names, by name; and a kind is found by its aliases too.
func TestLoadOrderAndAliases(t *testing.T) {
	hs, err := load(t, map[string]string{
		"qwen.yaml":   "version: 1\nlabel: Qwen\n",
		"pi.yaml":     "version: 1\nlabel: pi\n",
		"amp.yaml":    "version: 1\nlabel: Amp\naliases: [ampcode]\n",
		"claude.yaml": "version: 1\nlabel: Claude Code\n",
		"notes.txt":   "not a harness",
	})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, h := range hs {
		kinds = append(kinds, h.Kind)
	}
	if want := []string{"claude", "pi", "amp", "qwen"}; !slices.Equal(kinds, want) {
		t.Errorf("order %v, want %v", kinds, want)
	}
	if by := index(hs); by["ampcode"].Kind != "amp" || by["amp"].Label != "Amp" {
		t.Errorf("by name: %+v", by)
	}
}

// A file yardr cannot go by is refused whole, with what is wrong with it.
func TestLoadRefuses(t *testing.T) {
	for name, c := range map[string]struct{ file, body, want string }{
		"another shape":            {"claude.yaml", "version: 2\nlabel: x\n", "version 2: this yardr reads version 1"},
		"no version":               {"claude.yaml", "label: x\n", "version 0"},
		"a field not known":        {"claude.yaml", "version: 1\nlabel: x\nscreens: []\n", "field screens not found"},
		"no herdr kind":            {"nope.yaml", "version: 1\nlabel: x\n", "nope is no kind herdr starts"},
		"no label":                 {"claude.yaml", "version: 1\n", "no label"},
		"a reason unknown":         {"claude.yaml", "version: 1\nlabel: x\nerrors: [{reason: broken, pattern: '^x'}]\n", `reason "broken"`},
		"a pattern unread":         {"claude.yaml", "version: 1\nlabel: x\nerrors: [{reason: other, pattern: '^(x'}]\n", "missing closing )"},
		"an empty pattern":         {"claude.yaml", "version: 1\nlabel: x\nerrors: [{reason: other}]\n", `pattern ""`},
		"marks, no input":          {"claude.yaml", "version: 1\nlabel: x\nmarks: {message: '>'}\n", "without input"},
		"any session's":            {"claude.yaml", "version: 1\nlabel: x\nmodel: {home: .x, glob: 'a/*.jsonl', field: model}\n", "has no {session}"},
		"model, no field":          {"claude.yaml", "version: 1\nlabel: x\nmodel: {home: .x, glob: '{session}.jsonl'}\n", "home and field are needed"},
		"a nameless preset":        {"claude.yaml", "version: 1\nlabel: x\nflags: {permissions: [{args: [--yes]}]}\n", "without label or args"},
		"model flag is not a flag": {"claude.yaml", "version: 1\nlabel: x\nflags: {model: model}\n", "is not a flag"},
	} {
		if _, err := load(t, map[string]string{c.file: c.body}); err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), c.file) {
			t.Errorf("%s: %v, want %q and the file's name", name, err, c.want)
		}
	}
	if _, err := load(t, map[string]string{
		"amp.yaml":    "version: 1\nlabel: Amp\naliases: [claude]\n",
		"claude.yaml": "version: 1\nlabel: Claude Code\n",
	}); err == nil || !strings.Contains(err.Error(), "claude is already amp's name") {
		t.Errorf("an alias that is another kind: %v", err)
	}
}

// The model of a session is read from one line of the agent's own file: the
// string at the field's path, in a line of the type asked for. A line of
// another type, one without the field, one that is not JSON and a name the
// harness made up itself name none.
func TestModelIn(t *testing.T) {
	claude, _ := Lookup("claude")
	codex, _ := Lookup("codex")
	for _, c := range []struct {
		src        *ModelSource
		line, want string
	}{
		{claude.Model, `{"type":"assistant","message":{"model":"claude-fable-5-1"}}`, "claude-fable-5-1"},
		{claude.Model, `{"type":"user","message":{"model":"claude-fable-5-1"}}`, ""},
		{claude.Model, `{"type":"assistant","message":{"model":"<synthetic>"}}`, ""},
		{claude.Model, `{"type":"assistant","message":"hello"}`, ""},
		{claude.Model, `{"type":"assistant","message":{"model":7}}`, ""},
		{claude.Model, `{"type":"assistant","mess`, ""},
		{claude.Model, ``, ""},
		{codex.Model, `{"type":"turn_context","payload":{"model":"gpt-6-luna"}}`, "gpt-6-luna"},
		{codex.Model, `{"type":"session_meta","payload":{"model":"other"}}`, ""},
		{&ModelSource{Field: "model"}, `{"type":"anything","model":"m"}`, "m"},
	} {
		if got := c.src.ModelIn([]byte(c.line)); got != c.want {
			t.Errorf("%s in %s: %q, want %q", c.src.Field, c.line, got, c.want)
		}
	}
	if got, want := codex.Model.Pattern("/h", "x1"), "/h/sessions/*/*/*/rollout-*-x1.jsonl"; got != want {
		t.Errorf("pattern %q, want %q", got, want)
	}
}

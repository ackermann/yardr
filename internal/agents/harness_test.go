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
// flags, no model source, no dialog answered, no login screen named.
func TestKindWithNoFile(t *testing.T) {
	for _, kind := range []string{"gemini", "cursor", "nope", ""} {
		h, ok := Lookup(kind)
		if ok || h.Marks != (Marks{}) || h.Errors != nil || h.Dialogs != nil || h.Login != nil || h.LoginRecipe != nil || h.Background != nil || h.Model != nil || h.Trust || h.Label != "" ||
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
// the flags every agent gets are Codex's: no shared daemon (yardr-8bhk), and
// no upgrade of itself at start (yardr-bcx5).
func TestWhatOneKindSays(t *testing.T) {
	for _, h := range Harnesses {
		if h.Trust != (h.Kind == "claude") {
			t.Errorf("%s: trust %v", h.Kind, h.Trust)
		}
		want := []string(nil)
		if h.Kind == "codex" {
			want = []string{"--no-daemon", "--config=check_for_update_on_startup=false"}
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

// Kimi's question to a session that sat idle past its cache is the one
// dialog a file names (yardr-vsen): either of the lines it may begin with
// is the cache menu's, and no other kind has a dialog.
func TestDialogs(t *testing.T) {
	for _, h := range Harnesses {
		if (len(h.Dialogs) > 0) != (h.Kind == "kimi" || h.Kind == "claude") {
			t.Errorf("%s: dialogs %+v; only kimi's file and claude's name one", h.Kind, h.Dialogs)
		}
	}
	// Claude Code's offer to set auto mode up (yardr-xk55), by its title as
	// the pane of a builder showed it; the same words mid-sentence are not it.
	claude, _ := Lookup("claude")
	for line, want := range map[string]string{
		"Teach auto mode about your environment?":                "auto_mode_setup",
		"Auto mode works better when it knows your environment.": "",
		"❯ 1. Yes": "",
		"It asked: Teach auto mode about your environment? I said no": "",
	} {
		got := ""
		for _, d := range claude.Dialogs {
			if d.Matches(line) {
				got = d.Dialog
			}
		}
		if got != want {
			t.Errorf("claude: %q is of dialog %q, want %q", line, got, want)
		}
	}
	kimi, _ := Lookup("kimi")
	of := func(line string) string {
		for _, d := range kimi.Dialogs {
			if d.Matches(line) {
				return d.Dialog
			}
		}
		return ""
	}
	for line, want := range map[string]string{
		"This session has been idle for 44m and is ~198k tokens.":                     "cache_menu",
		"Cache expired — the next message re-sends the entire history at full price.": "cache_menu",
		"❯ Compact and continue    one-time compact cost":                             "",
		"I saw that This session has been idle for a while":                           "",
	} {
		if got := of(line); got != want {
			t.Errorf("%q is of dialog %q, want %q", line, got, want)
		}
	}
}

// Claude Code's login screen is the one a file names (yardr-m519): its menu
// of login methods and its browser sign-in, as 2.1.288 drew them with no
// credentials. No other kind names one.
func TestLogin(t *testing.T) {
	for _, h := range Harnesses {
		if (len(h.Login) > 0) != (h.Kind == "claude") {
			t.Errorf("%s: login %+v; only claude's file names one", h.Kind, h.Login)
		}
	}
	claude, _ := Lookup("claude")
	at := func(line string) bool {
		for _, l := range claude.Login {
			if l.Matches(line) {
				return true
			}
		}
		return false
	}
	for line, want := range map[string]bool{
		"Select login method:":         true,
		"Paste code here if prompted>": true,
		"❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise": false,
		"Browser didn't open? Use the url below to sign in (c to copy)":         false,
		"Not logged in · Run /login":                                            false,
		"Please run /login to continue":                                         false,
	} {
		if got := at(line); got != want {
			t.Errorf("%q is of the login screen: %v, want %v", line, got, want)
		}
	}
}

// Claude Code's lines for a shell its agent left running in the background
// are the ones a file names (yardr-zjhw): the line that ends the turn and
// the status line, as 2.1.288 drew them in two builders' panes. No other
// kind names one.
func TestBackground(t *testing.T) {
	for _, h := range Harnesses {
		if (len(h.Background) > 0) != (h.Kind == "claude") {
			t.Errorf("%s: background %+v; only claude's file names one", h.Kind, h.Background)
		}
	}
	claude, _ := Lookup("claude")
	runs := func(line string) bool {
		for _, b := range claude.Background {
			if b.Matches(line) {
				return true
			}
		}
		return false
	}
	for line, want := range map[string]bool{
		"✻ Brewed for 48s · done 12:44 AM · 1 shell still running":     true,
		"✻ Sautéed for 8m 17s · done 1:16 AM · 2 shells still running": true,
		"⏵⏵ auto mode on · 1 shell · ← 1 agent · ↓ to manage":          true,
		"1 shell · ↓ to manage":                                        true,
		"⏵⏵ auto mode on · 3 shells":                                   true,
		"✻ Brewed for 48s · done 12:44 AM":                             false,
		"⏵⏵ auto mode on · ← 1 agent · ↓ to manage":                    false,
		"⏺ The depot test runs in 1 shell; I wait for it.":             false,
		"⏺ I saw 1 shell still running and stopped it.":                false,
		"✻ Waiting for 1 background agent to finish":                   false,
	} {
		if got := runs(line); got != want {
			t.Errorf("%q says background work runs: %v, want %v", line, got, want)
		}
	}
}

// The one hint a file has is kimi's, for a prompt at a reset that its cache
// menu keeps (yardr-5yww): the sentence the shared hint had until then,
// word for word. No other kind adds to a hint.
func TestHints(t *testing.T) {
	for _, h := range Harnesses {
		want := Hints{}
		if h.Kind == "kimi" {
			want.Reset = "Kimi asks what to do with a session that sat idle, and keeps the prompt until it is answered"
		}
		if h.Hints != want {
			t.Errorf("%s: hints %+v, want %+v", h.Kind, h.Hints, want)
		}
	}
	if h, _ := Lookup("cursor"); h.Hints != (Hints{}) {
		t.Errorf("a kind with no file has hints: %+v", h.Hints)
	}
}

// Claude Code's login command is the one recipe (yardr-p7ex): the lines
// `claude auth login` of 2.1.288 printed on pipes, with no credentials. The
// address is the one group of a whole line and nothing but https, and the
// line it waits on is read before it has ended. No other kind has a recipe.
func TestLoginRecipe(t *testing.T) {
	for _, h := range Harnesses {
		if (h.LoginRecipe != nil) != (h.Kind == "claude") {
			t.Errorf("%s: login recipe %+v; only claude's file has one", h.Kind, h.LoginRecipe)
		}
	}
	claude, _ := Lookup("claude")
	r := claude.LoginRecipe
	if strings.Join(r.Argv, " ") != "auth login" || len(r.Methods) != 2 {
		t.Fatalf("recipe: %+v", r)
	}
	if m, ok := r.Method("Anthropic Console (API usage billing)"); !ok || strings.Join(m.Args, " ") != "--console" {
		t.Errorf("the console's method: %v, %+v", ok, m)
	}
	if _, ok := r.Method("--console"); ok {
		t.Error("a flag is taken for a method's label")
	}
	const page = "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c&state=x"
	for line, want := range map[string]string{
		"If the browser didn't open, visit: " + page:                    page,
		"  If the browser didn’t open, visit: " + page + "\r":           page,
		"If the browser didn't open, visit: http://claude.com/a":        "",
		"If the browser didn't open, visit: file:///etc/passwd":         "",
		"If the browser didn't open, visit: " + page + " and more":      "",
		"Opening browser to sign in…":                                   "",
		"Browser didn't open? Use the url below to sign in (c to copy)": "",
	} {
		if got := r.Page(line); got != want {
			t.Errorf("the page of %q: %q, want %q", line, got, want)
		}
	}
	for line, want := range map[string]bool{
		"Paste code here if prompted > ": true,
		"Paste code here if prompted":    true,
		"Paste code here if prom":        false,
		"Opening browser to sign in…":    false,
	} {
		if got := r.Waits(line); got != want {
			t.Errorf("waits on %q: %v, want %v", line, got, want)
		}
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
		"a dialog with no word":    {"claude.yaml", "version: 1\nlabel: x\ndialogs: [{pattern: '^x'}]\n", "no word to call the dialog by"},
		"a dialog's pattern":       {"claude.yaml", "version: 1\nlabel: x\ndialogs: [{dialog: menu, pattern: '^(x'}]\n", "dialogs: pattern"},
		"a dialog with no pattern": {"claude.yaml", "version: 1\nlabel: x\ndialogs: [{dialog: menu}]\n", `dialogs: pattern ""`},
		"a login's pattern":        {"claude.yaml", "version: 1\nlabel: x\nlogin: [{pattern: '^(x'}]\n", "login: pattern"},
		"a login with no pattern":  {"claude.yaml", "version: 1\nlabel: x\nlogin: [{}]\n", `login: pattern ""`},
		"a recipe with no command": {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, open_url: '(https://x)'}\n", "login_recipe: no argv"},
		"a recipe not verified":    {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {argv: [login], open_url: '(https://x)'}\n", "login_recipe: verified.from"},
		"a recipe with no address": {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, argv: [login], open_url: 'visit'}\n", "open_url: pattern \"visit\" needs one group"},
		"a recipe's input":         {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, argv: [login], open_url: '(x)', input: '^(x'}\n", "input: pattern"},
		"a method with no flag":    {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, argv: [login], open_url: '(x)', methods: [{label: a}]}\n", "methods: one without label or args"},
		"a background's pattern":   {"claude.yaml", "version: 1\nlabel: x\nbackground: [{pattern: '^(x'}]\n", "background: pattern"},
		"background, no pattern":   {"claude.yaml", "version: 1\nlabel: x\nbackground: [{}]\n", `background: pattern ""`},
		"a hint not known":         {"claude.yaml", "version: 1\nlabel: x\nhints: {idle: x}\n", "field idle not found"},
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

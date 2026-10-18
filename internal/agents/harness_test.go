package agents

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
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
	if want := []string{"claude", "codex", "grok", "kimi", "opencode", "pi", "kiro", "muse"}; !slices.Equal(kinds, want) {
		t.Errorf("kinds with a file: %v, want %v", kinds, want)
	}
	wantProvider := map[string]string{"claude": "Anthropic", "codex": "OpenAI", "grok": "xAI", "kimi": "Moonshot", "kiro": "AWS", "muse": "Meta"}
	for _, h := range Harnesses {
		if h.Provider != wantProvider[h.Kind] {
			t.Errorf("%s: provider %q, want %q", h.Kind, h.Provider, wantProvider[h.Kind])
		}
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
		if ok || h.Marks != (Marks{}) || h.Errors != nil || h.Dialogs != nil || h.Login != nil || h.LoginRecipe != nil || h.Background != nil || h.Model != nil || h.Trust != nil || h.Label != "" || h.Provider != "" ||
			h.Flags.Unattended != nil || h.Flags.Always != nil || h.Flags.Permissions != nil {
			t.Errorf("%q: %v, %+v; want nothing known", kind, ok, h)
		}
		if UnattendedArgs(kind) != nil || VersionArgs(kind) != nil || OneShotArgs(kind, "x") != nil || TrustEnv(kind, false) != nil {
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

// What only some files say every agent runs with: Codex's are no shared
// daemon (yardr-8bhk) and no upgrade of itself at start (yardr-bcx5), and
// Kiro's is the chat subcommand, so its flags are chat's (yardr-7wq2).
func TestAlwaysFlags(t *testing.T) {
	for _, h := range Harnesses {
		want := []string(nil)
		switch h.Kind {
		case "codex":
			want = []string{"--no-daemon", "--config=check_for_update_on_startup=false"}
		case "kiro":
			want = []string{"chat"}
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

// The trust dialogs yardr answers itself: Claude Code's folder-trust
// question, Kimi's, Codex's and Muse's, each the text that marks it, the
// answers that accept it and the mark of its selected answer, as the
// harness prints them (yardr-bgnn, yardr-mdz4, yardr-q7rd). Kimi names two
// answers: 2.2.0 renamed 'Trust this folder' to 'Trust and continue'
// (yardr-k9ew). No other kind names one.
func TestTrust(t *testing.T) {
	for _, h := range Harnesses {
		if (h.Trust != nil) != (h.Kind == "claude" || h.Kind == "kimi" || h.Kind == "codex" || h.Kind == "muse") {
			t.Errorf("%s: trust %+v; only claude's, kimi's, codex's and muse's files name one", h.Kind, h.Trust)
		}
	}
	claude, _ := Lookup("claude")
	if claude.Trust == nil || claude.Trust.Dialog != "Quick safety check:" || !slices.Equal(claude.Trust.Answers, []string{"Yes, I trust this folder"}) || claude.Trust.Cursor != "❯" {
		t.Errorf("claude: trust %+v", claude.Trust)
	}
	kimi, _ := Lookup("kimi")
	if kimi.Trust == nil || kimi.Trust.Dialog != "Trust this folder?" || !slices.Equal(kimi.Trust.Answers, []string{"Trust this folder", "Trust and continue"}) || kimi.Trust.Cursor != "❯" || !slices.Equal(kimi.Trust.Env, []string{"KIMI_CODE_TRUST_WORKSPACE=1"}) || !slices.Equal(kimi.Trust.EnvNames, []string{"KIMI_CODE_TRUST_WORKSPACE"}) {
		t.Errorf("kimi: trust %+v", kimi.Trust)
	}
	codex, _ := Lookup("codex")
	if codex.Trust == nil || codex.Trust.Dialog != "Trust this folder?" || !slices.Equal(codex.Trust.Answers, []string{"1. Trust and continue"}) || codex.Trust.Cursor != "›" {
		t.Errorf("codex: trust %+v", codex.Trust)
	}
	muse, _ := Lookup("muse")
	if muse.Trust == nil || muse.Trust.Dialog != "Do you trust this workspace?" || !slices.Equal(muse.Trust.Answers, []string{"1  Trust and continue"}) || muse.Trust.Cursor != "⟩" {
		t.Errorf("muse: trust %+v", muse.Trust)
	}
}

// Kimi 2.2.0 can skip the trust dialog with KIMI_CODE_TRUST_WORKSPACE=1,
// but the runner sets this only when automatic trust is enabled. The name,
// but not the value, is exposed in JSON.
func TestTrustEnv(t *testing.T) {
	for _, h := range Harnesses {
		if (h.Trust != nil && len(h.Trust.Env) > 0) != (h.Kind == "kimi") {
			t.Errorf("%s: trust env %v; only kimi's file names any", h.Kind, h.Trust)
		}
	}
	if got := TrustEnv("kimi", true); !slices.Equal(got, []string{"KIMI_CODE_TRUST_WORKSPACE=1"}) {
		t.Errorf("kimi: trust env %v", got)
	}
	if got := TrustEnv("kimi", false); !slices.Equal(got, []string{"KIMI_CODE_TRUST_WORKSPACE="}) {
		t.Errorf("kimi trust=false: trust env %v", got)
	}
	if got := TrustEnv("cursor", true); got != nil {
		t.Errorf("a kind with no file has trust env: %v", got)
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

// Claude Code's login screen is one a file names (yardr-m519): its menu
// of login methods and its browser sign-in, as 2.1.288 drew them with no
// credentials. Muse's is the other (yardr-dx5f): its two login methods,
// each with and without the hint that follows the selection, and its
// footer, as 1.4.9 drew them. No other kind names one.
func TestLogin(t *testing.T) {
	for _, h := range Harnesses {
		if (len(h.Login) > 0) != (h.Kind == "claude" || h.Kind == "muse") {
			t.Errorf("%s: login %+v; only claude's and muse's files name one", h.Kind, h.Login)
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
	muse, _ := Lookup("muse")
	museAt := func(line string) bool {
		for _, l := range muse.Login {
			if l.Matches(line) {
				return true
			}
		}
		return false
	}
	for line, want := range map[string]bool{
		"Log in with browser · Enter to choose":          true,
		"Log in with browser":                            true,
		"Set an API key · Enter to choose":               true,
		"Set an API key":                                 true,
		"↓↑ to select · Esc to quit":                     true,
		"Muse Code 1.4.9":                                false,
		"❯ Log in with browser · Enter to choose":        false,
		"Use Up/Down or 1/2, then Enter. Esc quits.":     false,
		"muse-spark-1.3 · max · /tmp/work · Auto-review": false,
	} {
		if got := museAt(line); got != want {
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

// The models the files suggest a person pick (yardr-tv3s): each the id the
// kind's --model flag takes and the label to show it by, with the one
// frontier model a yardmaster should run on marked. Claude Code's are its
// --model aliases, which do not go stale when a version ships; the other
// ids were read from the harness's own config or session files, as each
// file's comment says. A file may name none: grok, opencode and pi name
// none, and a kind with no file has no list.
func TestOffers(t *testing.T) {
	for kind, want := range map[string][]Offer{
		"claude": {
			{Model: "opus", Label: "Opus 5.5", Recommended: "yardmaster"},
			{Model: "fable", Label: "Fable 5.1"},
			{Model: "sonnet", Label: "Sonnet 5"},
		},
		"codex": {
			{Model: "gpt-6-luna", Label: "GPT-6 Luna", Recommended: "yardmaster"},
			{Model: "gpt-6-astra", Label: "GPT-6 Astra"},
		},
		"kimi": {
			{Model: "kimi-code/k3", Label: "K3", Recommended: "yardmaster"},
			{Model: "kimi-code/k3-256k", Label: "K3-256k"},
			{Model: "kimi-code/kimi-for-coding", Label: "K2.8 Preview"},
		},
		"muse": {
			{Model: "muse-spark-1.3-contributor", Label: "Spark 1.3 Contributor", Recommended: "yardmaster"},
			{Model: "muse-wonder-1.0", Label: "Wonder 1.0"},
		},
		"kiro": {
			{Model: "claude-opus-5.5", Label: "Opus 5.5", Recommended: "yardmaster"},
			{Model: "auto", Label: "Auto"},
			{Model: "claude-sonnet-4.5", Label: "Sonnet 4.5"},
		},
	} {
		h, _ := Lookup(kind)
		if !slices.Equal(h.Offers, want) {
			t.Errorf("%s: offers %+v, want %+v", kind, h.Offers, want)
		}
	}
	for _, h := range Harnesses {
		marked := 0
		for _, o := range h.Offers {
			if o.Recommended != "" {
				marked++
			}
		}
		want := 0
		if len(h.Offers) > 0 {
			want = 1
		} else if h.Kind == "claude" || h.Kind == "codex" || h.Kind == "kimi" || h.Kind == "muse" || h.Kind == "kiro" {
			t.Errorf("%s names no model to pick from", h.Kind)
		}
		if marked != want {
			t.Errorf("%s: %d recommended of %d offers, want %d", h.Kind, marked, len(h.Offers), want)
		}
	}
	if h, _ := Lookup("cursor"); h.Offers != nil {
		t.Errorf("a kind with no file offers models: %+v", h.Offers)
	}
	// A file with the list passes the check, and one without it does too.
	hs, err := load(t, map[string]string{
		"claude.yaml": "version: 1\nlabel: x\noffers: [{model: a, label: A, recommended: yardmaster}, {model: b, label: B}]\n",
		"pi.yaml":     "version: 1\nlabel: pi\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hs[0].Offers) != 2 || hs[0].Offers[0].Recommended != "yardmaster" || hs[1].Offers != nil {
		t.Errorf("loaded offers: %+v, %+v", hs[0].Offers, hs[1].Offers)
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
		"another shape":                    {"claude.yaml", "version: 2\nlabel: x\n", "version 2: this yardr reads version 1"},
		"no version":                       {"claude.yaml", "label: x\n", "version 0"},
		"a field not known":                {"claude.yaml", "version: 1\nlabel: x\nscreens: []\n", "field screens not found"},
		"no herdr kind":                    {"nope.yaml", "version: 1\nlabel: x\n", "nope is no kind herdr starts"},
		"no label":                         {"claude.yaml", "version: 1\n", "no label"},
		"a reason unknown":                 {"claude.yaml", "version: 1\nlabel: x\nerrors: [{reason: broken, pattern: '^x'}]\n", `reason "broken"`},
		"a pattern unread":                 {"claude.yaml", "version: 1\nlabel: x\nerrors: [{reason: other, pattern: '^(x'}]\n", "missing closing )"},
		"an empty pattern":                 {"claude.yaml", "version: 1\nlabel: x\nerrors: [{reason: other}]\n", `pattern ""`},
		"a dialog with no word":            {"claude.yaml", "version: 1\nlabel: x\ndialogs: [{pattern: '^x'}]\n", "no word to call the dialog by"},
		"a dialog's pattern":               {"claude.yaml", "version: 1\nlabel: x\ndialogs: [{dialog: menu, pattern: '^(x'}]\n", "dialogs: pattern"},
		"a dialog with no pattern":         {"claude.yaml", "version: 1\nlabel: x\ndialogs: [{dialog: menu}]\n", `dialogs: pattern ""`},
		"a login's pattern":                {"claude.yaml", "version: 1\nlabel: x\nlogin: [{pattern: '^(x'}]\n", "login: pattern"},
		"a login with no pattern":          {"claude.yaml", "version: 1\nlabel: x\nlogin: [{}]\n", `login: pattern ""`},
		"a recipe with no command":         {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, open_url: '(https://x)'}\n", "login_recipe: no argv"},
		"a recipe not verified":            {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {argv: [login], open_url: '(https://x)'}\n", "login_recipe: verified.from"},
		"a recipe with no address":         {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, argv: [login], open_url: 'visit'}\n", "open_url: pattern \"visit\" needs one group"},
		"a recipe's input":                 {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, argv: [login], open_url: '(x)', input: '^(x'}\n", "input: pattern"},
		"a method with no flag":            {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, argv: [login], open_url: '(x)', methods: [{label: a}]}\n", "methods: one without label or args"},
		"a background's pattern":           {"claude.yaml", "version: 1\nlabel: x\nbackground: [{pattern: '^(x'}]\n", "background: pattern"},
		"background, no pattern":           {"claude.yaml", "version: 1\nlabel: x\nbackground: [{}]\n", `background: pattern ""`},
		"a hint not known":                 {"claude.yaml", "version: 1\nlabel: x\nhints: {idle: x}\n", "field idle not found"},
		"marks, no input":                  {"claude.yaml", "version: 1\nlabel: x\nmarks: {message: '>'}\n", "without input"},
		"marks, placeholder without input": {"claude.yaml", "version: 1\nlabel: x\nmarks: {placeholder: 'ask'}\n", "without input"},
		"trust, no answers":                {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Are you sure?'}\n", "names its text, its accepting answers and its cursor"},
		"trust, answers empty":             {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Are you sure?', answers: [], cursor: '>'}\n", "names its text, its accepting answers and its cursor"},
		"trust, no dialog":                 {"claude.yaml", "version: 1\nlabel: x\ntrust: {answers: ['Yes']}\n", "names its text, its accepting answers and its cursor"},
		"trust, no cursor":                 {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Are you sure?', answers: ['Yes']}\n", "names its text, its accepting answers and its cursor"},
		"trust env, no KEY=VALUE":          {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Trust?', answers: ['Yes'], cursor: '>', env: ['JUSTAKEY']}\n", "trust.env: entry 1: want KEY=VALUE"},
		"trust env, bad key":               {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Trust?', answers: ['Yes'], cursor: '>', env: ['1BAD=x']}\n", "trust.env: entry 1: want KEY=VALUE"},
		"any session's":                    {"claude.yaml", "version: 1\nlabel: x\nmodel: {home: .x, glob: 'a/*.jsonl', field: model}\n", "has no {session}"},
		"model, no field":                  {"claude.yaml", "version: 1\nlabel: x\nmodel: {home: .x, glob: '{session}.jsonl'}\n", "home and field are needed"},
		"a nameless preset":                {"claude.yaml", "version: 1\nlabel: x\nflags: {permissions: [{args: [--yes]}]}\n", "without label or args"},
		"model flag is not a flag":         {"claude.yaml", "version: 1\nlabel: x\nflags: {model: model}\n", "is not a flag"},
		"two recommended":                  {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, label: A, recommended: yardmaster}, {model: b, label: B, recommended: yardmaster}]\n", "offers: 2 recommended, want exactly one"},
		"none recommended":                 {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, label: A}, {model: b, label: B}]\n", "offers: 0 recommended, want exactly one"},
		"an offer without a model":         {"claude.yaml", "version: 1\nlabel: x\noffers: [{label: A, recommended: yardmaster}, {model: b, label: B}]\n", "offers: one without a model id"},
		"an offer without a label":         {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, recommended: yardmaster}, {model: b, label: B}]\n", "offers: one without a label"},
		"too few offers":                   {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, label: A, recommended: yardmaster}]\n", "offers: 1 entries, want 2 to 4"},
		"too many offers":                  {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, label: A, recommended: yardmaster}, {model: b, label: B}, {model: c, label: C}, {model: d, label: D}, {model: e, label: E}]\n", "offers: 5 entries, want 2 to 4"},
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
// harness made up itself name none. Muse's lines name their type
// "payload_type" (type_field): its startup metadata and its nested
// permission records name none.
func TestModelIn(t *testing.T) {
	claude, _ := Lookup("claude")
	codex, _ := Lookup("codex")
	muse, _ := Lookup("muse")
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
		{muse.Model, `{"payload_type":"run.model.configured","payload":{"record":{"model_id":"muse-spark-1.3-contributor"}}}`, "muse-spark-1.3-contributor"},
		{muse.Model, `{"payload_type":"runtime.session.metadata","payload":{"record":{"model_id":"muse-spark-1.3"}}}`, ""},
		{muse.Model, `{"children":[{"child_index":0,"record_json":"{\"payload_type\":\"runtime.session.permission_format_declared\"}"}]}`, ""},
		{muse.Model, `{"type":"run.model.configured","payload":{"record":{"model_id":"muse-spark-1.3"}}}`, ""},
		{muse.Model, `{"payload_type":"run.model.configured","payload":{"record":{"model_id":7}}}`, ""},
		{muse.Model, `{"payload_type":"run.model.configured`, ""},
		{&ModelSource{Field: "model"}, `{"type":"anything","model":"m"}`, "m"},
		{&ModelSource{TypeField: "kind", Type: "a", Field: "model"}, `{"kind":"a","model":"m"}`, "m"},
		{&ModelSource{TypeField: "kind", Type: "a", Field: "model"}, `{"kind":"b","model":"m"}`, ""},
	} {
		if got := c.src.ModelIn([]byte(c.line)); got != c.want {
			t.Errorf("%s in %s: %q, want %q", c.src.Field, c.line, got, c.want)
		}
	}
	if got, want := codex.Model.Pattern("/h", "x1"), "/h/sessions/*/*/*/rollout-*-x1.jsonl"; got != want {
		t.Errorf("pattern %q, want %q", got, want)
	}
	if got, want := muse.Model.Pattern("/h", "x1"), "/h/sessions/*/*/*/x1/session.jsonl"; got != want {
		t.Errorf("pattern %q, want %q", got, want)
	}
}

// A trust dialog's answers arrive as one string or a list, and the file's
// old `answer` still reads as one (yardr-tzkk): a renamed answer is one
// added line, and a file written either way reads. Both keys at once is
// refused, as a key the dialog has none of.
func TestTrustAnswerForms(t *testing.T) {
	file := func(trust string) map[string]string {
		return map[string]string{"claude.yaml": "version: 1\nlabel: x\ntrust:\n" + trust}
	}
	for _, c := range []struct {
		name  string
		trust string
		want  []string
	}{
		{"a list", "  dialog: 'Sure?'\n  answers: ['Yes', 'Yes please']\n  cursor: '>'\n", []string{"Yes", "Yes please"}},
		{"one string", "  dialog: 'Sure?'\n  answers: 'Yes'\n  cursor: '>'\n", []string{"Yes"}},
		{"the old answer", "  dialog: 'Sure?'\n  answer: 'Yes'\n  cursor: '>'\n", []string{"Yes"}},
		{"a list under the old answer", "  dialog: 'Sure?'\n  answer: ['Yes', 'Yes please']\n  cursor: '>'\n", []string{"Yes", "Yes please"}},
	} {
		hs, err := load(t, file(c.trust))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := hs[0].Trust.Answers; !slices.Equal(got, c.want) {
			t.Errorf("%s: answers %q, want %q", c.name, got, c.want)
		}
	}
	for name, trust := range map[string]string{
		"both keys":                    "  dialog: 'Sure?'\n  answer: 'Yes'\n  answers: ['Yes']\n  cursor: '>'\n",
		"answers a number":             "  dialog: 'Sure?'\n  answers: 3\n  cursor: '>'\n",
		"a number among answers":       "  dialog: 'Sure?'\n  answers: ['Yes', 3]\n  cursor: '>'\n",
		"a key the dialog has none of": "  dialog: 'Sure?'\n  answers: ['Yes']\n  cursor: '>'\n  colour: red\n",
	} {
		if _, err := load(t, file(trust)); err == nil {
			t.Errorf("%s: loaded, want refused", name)
		} else if name == "both keys" && !strings.Contains(err.Error(), "under two names") {
			t.Errorf("%s: error %q, want it to name the two keys", name, err)
		} else if name == "a key the dialog has none of" && !strings.Contains(err.Error(), "field colour not found") {
			t.Errorf("%s: error %q, want it to name the key", name, err)
		}
	}
}

// One line of a pane shows a trust dialog's mark at either edge, and one
// of its answers exact with the cursor off, the cursor saying which is
// selected. The runner answers by this, and the daemon tells a dialog with
// none of the answers by it.
func TestTrustShowsLines(t *testing.T) {
	claude, _ := Lookup("claude")
	yes := claude.Trust.Answers[0]
	for line, want := range map[string]bool{
		"Quick safety check:": true,
		"Quick safety check: Is this a project you created or one you trust?": true,
		"own code). Quick safety check:":                                      true,
		"Quick safety checklist:":                                             false,
		"the Quick safety check: is over":                                     false,
		" ❯ " + yes:                                                           false,
	} {
		if got := claude.Trust.ShowsDialog(line); got != want {
			t.Errorf("ShowsDialog(%q) = %v, want %v", line, got, want)
		}
	}
	for _, c := range []struct {
		line     string
		answer   string
		selected bool
	}{
		{" ❯ " + yes, yes, true},
		{"   " + yes, yes, false},
		{yes, yes, false},
		{"   No, exit", "", false},
		{"❯ Please choose " + yes, "", false},
		{"I typed: " + yes + ", honest", "", false},
		{"Quick safety check:", "", false},
	} {
		if answer, selected := claude.Trust.ShowsAnswer(c.line); answer != c.answer || selected != c.selected {
			t.Errorf("ShowsAnswer(%q) = %q, %v; want %q, %v", c.line, answer, selected, c.answer, c.selected)
		}
	}
}

// The installed harness's version is the first line its file's version
// flags print; "" when there is nothing to ask or no answer to read.
func TestHarnessVersion(t *testing.T) {
	ctx := context.Background()
	if got := HarnessVersion(ctx, "cursor"); got != "" {
		t.Errorf("a kind with no file: version %q, want none", got)
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if got := HarnessVersion(ctx, "kimi"); got != "" {
		t.Errorf("a harness not installed here: version %q, want none", got)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "kimi"), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("#!/bin/sh\necho\necho 'kimi-code 2.9.9 (ae6bbc1 2026-10-20)'\necho noise\n")
	if got, want := HarnessVersion(ctx, "kimi"), "kimi-code 2.9.9 (ae6bbc1 2026-10-20)"; got != want {
		t.Errorf("version %q, want %q", got, want)
	}
	write("#!/bin/sh\necho boom >&2\nexit 1\n")
	if got := HarnessVersion(ctx, "kimi"); got != "" {
		t.Errorf("a failing harness: version %q, want none", got)
	}
	write("#!/bin/sh\nsleep 30\n")
	old := harnessVersionTimeout
	harnessVersionTimeout = 50 * time.Millisecond
	defer func() { harnessVersionTimeout = old }()
	if got := HarnessVersion(ctx, "kimi"); got != "" {
		t.Errorf("a hanging harness: version %q, want none", got)
	}
}

// Only claude names where its context use is read: the last assistant
// line's message.usage, whose input and cache tokens add up to what the
// session holds. The other kinds have no context-based rotation.
func TestContextSource(t *testing.T) {
	for _, h := range Harnesses {
		if h.Kind == "claude" {
			if h.Context == nil || h.Context.Type != "assistant" || h.Context.Usage != "message.usage" {
				t.Fatalf("claude's context source = %+v", h.Context)
			}
			continue
		}
		if h.Context != nil {
			t.Errorf("%s names a context source: %+v", h.Kind, h.Context)
		}
	}
	claude, _ := Lookup("claude")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":5,"output_tokens":7}}}`,
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error"}]}}`,
		`not json at all`,
		`{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_read_input_tokens":200,"output_tokens":3}}}`,
	}
	var used int
	var ok bool
	for _, line := range lines {
		if n, has := claude.Context.UsageIn([]byte(line)); has {
			used, ok = n, true
		}
	}
	if !ok || used != 202 {
		t.Fatalf("last use = %d, %v; want 202, true", used, ok)
	}
	// A usage object with none of the fields is no reading.
	if n, ok := claude.Context.UsageIn([]byte(`{"type":"assistant","message":{"usage":{"output_tokens":3}}}`)); ok || n != 0 {
		t.Fatalf("output tokens alone = %d, %v; want 0, false", n, ok)
	}
	// A file that names its own token fields and type key is read by those.
	custom := ContextSource{Type: "turn", TypeField: "kind", Usage: "use", Tokens: []string{"total_tokens"}}
	if n, ok := custom.UsageIn([]byte(`{"kind":"turn","use":{"total_tokens":9}}`)); !ok || n != 9 {
		t.Fatalf("custom source = %d, %v; want 9, true", n, ok)
	}
	if _, ok := custom.UsageIn([]byte(`{"type":"turn","use":{"total_tokens":9}}`)); ok {
		t.Fatal("the type is read from type_field, not type")
	}
}

// A context source without a usage path, without the model source whose
// session file it reads, or with an empty token field is refused, not
// half read.
func TestContextSourceChecked(t *testing.T) {
	model := "model:\n  home: .x\n  glob: s/{session}.jsonl\n  field: m.model\n"
	for name, body := range map[string]string{
		"no usage": "version: 1\nlabel: Q\n" + model + "context:\n  type: assistant\n",
		"no model": "version: 1\nlabel: Q\ncontext:\n  type: assistant\n  usage: message.usage\n",
		"no field": "version: 1\nlabel: Q\n" + model + "context:\n  type: assistant\n  usage: message.usage\n  tokens: [input_tokens, '']\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, map[string]string{"qwen.yaml": body}); err == nil {
				t.Fatalf("loaded: %s", body)
			}
		})
	}
	if _, err := load(t, map[string]string{"qwen.yaml": "version: 1\nlabel: Q\n" + model +
		"context:\n  type: assistant\n  usage: message.usage\n"}); err != nil {
		t.Fatalf("a whole source is refused: %v", err)
	}
}

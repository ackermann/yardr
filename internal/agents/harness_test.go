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
		if ok || h.Marks != (Marks{}) || h.Errors != nil || h.Dialogs != nil || h.Login != nil || h.LoginRecipe != nil || h.LoginEnv != nil || h.Background != nil || h.Model != nil || h.Trust != nil || h.ApiKey != nil || h.Onboarding != nil || h.Label != "" || h.Provider != "" ||
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

// The lines for a shell an agent left running in the background are the
// ones a file names: Claude Code's (yardr-zjhw: the line that ends the
// turn and the status line, as 2.1.288 drew them in two builders' panes)
// and Muse Code's (yardr-kh12: the status line under its input, read live
// from a builder's pane, 20 Oct 2026, 1.4.9). No other kind names one.
func TestBackground(t *testing.T) {
	for _, h := range Harnesses {
		if (len(h.Background) > 0) != (h.Kind == "claude" || h.Kind == "muse") {
			t.Errorf("%s: background %+v; only claude's and muse's files name one", h.Kind, h.Background)
		}
	}
	runs := func(h Harness, line string) bool {
		for _, b := range h.Background {
			if b.Matches(line) {
				return true
			}
		}
		return false
	}
	claude, _ := Lookup("claude")
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
		if got := runs(claude, line); got != want {
			t.Errorf("claude: %q says background work runs: %v, want %v", line, got, want)
		}
	}
	muse, _ := Lookup("muse")
	for line, want := range map[string]bool{
		"└ ◆ Run depot test gate in worktree  running          27m 03s":           true,
		"└ ◆ Build the docs  running  5s":                                         true,
		"└ ◈ Re-run depot gate on commit  running                     5m 34s":     true,
		"└ Managed command · running · Work: work.v2.managed_bash.sha256.eb19...": false,
		"└ (ctrl+b to send to background)":                                        false,
		"◇ Checked terminal bash session — running (7s · esc to interrupt)":       false,
		"Run depot test gate in worktree  running  14m 01s":                       false,
		" Backgrounded · Run depot test gate in worktree · 5m · ctrl+o":           false,
		"◆ Ran command · Verify gate tests still … · ✓ · 0.2s · ctrl+o":           false,
		"main · ↓ to select":                               false,
		"muse-spark-1.3 · max · /tmp/work · Auto-review":   false,
		"◆ Still blocked on the gate run — no result yet.": false,
		"◆ The build is still running 5m":                  false,
	} {
		if got := runs(muse, line); got != want {
			t.Errorf("muse: %q says background work runs: %v, want %v", line, got, want)
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

// Only claude's file names the variables of an API key and the dialog
// drawn for one: the other kinds are logged in some other way, and their
// questions are left for a human.
func TestLoginEnv(t *testing.T) {
	for _, h := range Harnesses {
		if (h.LoginEnv != nil) != (h.Kind == "claude") {
			t.Errorf("%s: login_env %v; only claude's file names one", h.Kind, h.LoginEnv)
		}
		if (h.ApiKey != nil) != (h.Kind == "claude") {
			t.Errorf("%s: api_key %+v; only claude's file names one", h.Kind, h.ApiKey)
		}
	}
	claude, _ := Lookup("claude")
	if !slices.Equal(claude.LoginEnv, []string{"ANTHROPIC_API_KEY"}) {
		t.Errorf("claude's login_env: %v", claude.LoginEnv)
	}
	k := claude.ApiKey
	if k.Dialog != "Do you want to use this API key?" || !slices.Equal(k.Answers, []string{"Yes"}) || k.Cursor != "❯" {
		t.Errorf("claude's api_key: %+v", k)
	}
}

// The first-start screens a kind's file names for yardr to answer in turn
// in a fresh home (yardr-dffh): Claude Code's theme picker and its security
// notes, each as the pty read them. The picker opens with its cursor on
// Dark mode, which the ✔ marks as the current theme; the security notes
// name no cursor and are answered with Enter alone. No other kind names
// any.
func TestOnboarding(t *testing.T) {
	for _, h := range Harnesses {
		if (len(h.Onboarding) > 0) != (h.Kind == "claude") {
			t.Errorf("%s: onboarding %+v; only claude's file names any", h.Kind, h.Onboarding)
		}
	}
	claude, _ := Lookup("claude")
	want := []Trust{
		{Dialog: "Choose the text style", Answers: []string{"✔ Dark mode"}, Cursor: "❯"},
		{Dialog: "Security notes:", Answers: []string{"Press Enter to continue…"}},
	}
	if len(claude.Onboarding) != len(want) {
		t.Fatalf("claude's onboarding: %+v, want %d entries", claude.Onboarding, len(want))
	}
	for i, w := range want {
		got := claude.Onboarding[i]
		if got.Dialog != w.Dialog || !slices.Equal(got.Answers, w.Answers) || got.Cursor != w.Cursor || len(got.Env) != 0 {
			t.Errorf("claude's onboarding entry %d: %+v, want %+v", i+1, got, w)
		}
	}
}

// A file yardr cannot go by is refused whole, with what is wrong with it.
func TestLoadRefuses(t *testing.T) {
	// A usage entry reads the session file the model source names.
	model := "model: {home: .x, glob: '{session}.jsonl', field: m}\n"
	for name, c := range map[string]struct{ file, body, want string }{
		"another shape":                       {"claude.yaml", "version: 2\nlabel: x\n", "version 2: this yardr reads version 1"},
		"no version":                          {"claude.yaml", "label: x\n", "version 0"},
		"a field not known":                   {"claude.yaml", "version: 1\nlabel: x\nscreens: []\n", "field screens not found"},
		"no herdr kind":                       {"nope.yaml", "version: 1\nlabel: x\n", "nope is no kind herdr starts"},
		"no label":                            {"claude.yaml", "version: 1\n", "no label"},
		"a reason unknown":                    {"claude.yaml", "version: 1\nlabel: x\nerrors: [{reason: broken, pattern: '^x'}]\n", `reason "broken"`},
		"a pattern unread":                    {"claude.yaml", "version: 1\nlabel: x\nerrors: [{reason: other, pattern: '^(x'}]\n", "missing closing )"},
		"an empty pattern":                    {"claude.yaml", "version: 1\nlabel: x\nerrors: [{reason: other}]\n", `pattern ""`},
		"a dialog with no word":               {"claude.yaml", "version: 1\nlabel: x\ndialogs: [{pattern: '^x'}]\n", "no word to call the dialog by"},
		"a dialog's pattern":                  {"claude.yaml", "version: 1\nlabel: x\ndialogs: [{dialog: menu, pattern: '^(x'}]\n", "dialogs: pattern"},
		"a dialog with no pattern":            {"claude.yaml", "version: 1\nlabel: x\ndialogs: [{dialog: menu}]\n", `dialogs: pattern ""`},
		"a login's pattern":                   {"claude.yaml", "version: 1\nlabel: x\nlogin: [{pattern: '^(x'}]\n", "login: pattern"},
		"a login with no pattern":             {"claude.yaml", "version: 1\nlabel: x\nlogin: [{}]\n", `login: pattern ""`},
		"a recipe with no command":            {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, open_url: '(https://x)'}\n", "login_recipe: no argv"},
		"a recipe not verified":               {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {argv: [login], open_url: '(https://x)'}\n", "login_recipe: verified.from"},
		"a recipe with no address":            {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, argv: [login], open_url: 'visit'}\n", "open_url: pattern \"visit\" needs one group"},
		"a recipe's input":                    {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, argv: [login], open_url: '(x)', input: '^(x'}\n", "input: pattern"},
		"a method with no flag":               {"claude.yaml", "version: 1\nlabel: x\nlogin_recipe: {verified: {from: x 1, on: 2026-10-13}, argv: [login], open_url: '(x)', methods: [{label: a}]}\n", "methods: one without label or args"},
		"a background's pattern":              {"claude.yaml", "version: 1\nlabel: x\nbackground: [{pattern: '^(x'}]\n", "background: pattern"},
		"background, no pattern":              {"claude.yaml", "version: 1\nlabel: x\nbackground: [{}]\n", `background: pattern ""`},
		"a hint not known":                    {"claude.yaml", "version: 1\nlabel: x\nhints: {idle: x}\n", "field idle not found"},
		"marks, no input":                     {"claude.yaml", "version: 1\nlabel: x\nmarks: {message: '>'}\n", "without input"},
		"marks, placeholder without input":    {"claude.yaml", "version: 1\nlabel: x\nmarks: {placeholder: 'ask'}\n", "without input"},
		"marks, hint colour without input":    {"claude.yaml", "version: 1\nlabel: x\nmarks: {placeholder_sgr: '38;2;1;2;3'}\n", "without input"},
		"marks, hint colour no colour":        {"claude.yaml", "version: 1\nlabel: x\nmarks: {input: '>', placeholder_sgr: 'grey'}\n", `placeholder_sgr "grey"`},
		"marks, hint colour too short":        {"claude.yaml", "version: 1\nlabel: x\nmarks: {input: '>', placeholder_sgr: '38;2;1;2'}\n", "want 38;2;r;g;b or 38;5;n"},
		"marks, hint colour out of range":     {"claude.yaml", "version: 1\nlabel: x\nmarks: {input: '>', placeholder_sgr: '38;5;999'}\n", "want 38;2;r;g;b or 38;5;n"},
		"trust, no answers":                   {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Are you sure?'}\n", "names its text, its accepting answers and its cursor"},
		"trust, answers empty":                {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Are you sure?', answers: [], cursor: '>'}\n", "names its text, its accepting answers and its cursor"},
		"trust, no dialog":                    {"claude.yaml", "version: 1\nlabel: x\ntrust: {answers: ['Yes']}\n", "names its text, its accepting answers and its cursor"},
		"trust, no cursor":                    {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Are you sure?', answers: ['Yes']}\n", "names its text, its accepting answers and its cursor"},
		"trust env, no KEY=VALUE":             {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Trust?', answers: ['Yes'], cursor: '>', env: ['JUSTAKEY']}\n", "trust.env: entry 1: want KEY=VALUE"},
		"trust env, bad key":                  {"claude.yaml", "version: 1\nlabel: x\ntrust: {dialog: 'Trust?', answers: ['Yes'], cursor: '>', env: ['1BAD=x']}\n", "trust.env: entry 1: want KEY=VALUE"},
		"any session's":                       {"claude.yaml", "version: 1\nlabel: x\nmodel: {home: .x, glob: 'a/*.jsonl', field: model}\n", "has no {session}"},
		"model, no field":                     {"claude.yaml", "version: 1\nlabel: x\nmodel: {home: .x, glob: '{session}.jsonl'}\n", "home and field are needed"},
		"a nameless preset":                   {"claude.yaml", "version: 1\nlabel: x\nflags: {permissions: [{args: [--yes]}]}\n", "without label or args"},
		"model flag is not a flag":            {"claude.yaml", "version: 1\nlabel: x\nflags: {model: model}\n", "is not a flag"},
		"two recommended":                     {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, label: A, recommended: yardmaster}, {model: b, label: B, recommended: yardmaster}]\n", "offers: 2 recommended, want exactly one"},
		"none recommended":                    {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, label: A}, {model: b, label: B}]\n", "offers: 0 recommended, want exactly one"},
		"an offer without a model":            {"claude.yaml", "version: 1\nlabel: x\noffers: [{label: A, recommended: yardmaster}, {model: b, label: B}]\n", "offers: one without a model id"},
		"an offer without a label":            {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, recommended: yardmaster}, {model: b, label: B}]\n", "offers: one without a label"},
		"too few offers":                      {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, label: A, recommended: yardmaster}]\n", "offers: 1 entries, want 2 to 4"},
		"too many offers":                     {"claude.yaml", "version: 1\nlabel: x\noffers: [{model: a, label: A, recommended: yardmaster}, {model: b, label: B}, {model: c, label: C}, {model: d, label: D}, {model: e, label: E}]\n", "offers: 5 entries, want 2 to 4"},
		"login_env, no variable":              {"claude.yaml", "version: 1\nlabel: x\nlogin_env: ['1BAD']\n", `login_env: "1BAD" is no name of a variable`},
		"login_env, named twice":              {"claude.yaml", "version: 1\nlabel: x\nlogin_env: [A_KEY, A_KEY]\n", `login_env: "A_KEY" is named twice`},
		"api_key, no answers":                 {"claude.yaml", "version: 1\nlabel: x\napi_key: {dialog: 'Use it?'}\n", "names its text, its accepting answers and its cursor"},
		"api_key, no dialog":                  {"claude.yaml", "version: 1\nlabel: x\napi_key: {answers: ['Yes']}\n", "names its text, its accepting answers and its cursor"},
		"api_key, no cursor":                  {"claude.yaml", "version: 1\nlabel: x\napi_key: {dialog: 'Use it?', answers: ['Yes']}\n", "names its text, its accepting answers and its cursor"},
		"api_key, env":                        {"claude.yaml", "version: 1\nlabel: x\napi_key: {dialog: 'Use it?', answers: ['Yes'], cursor: '>', env: ['A=1']}\n", "api_key takes no env"},
		"onboarding, no answers":              {"claude.yaml", "version: 1\nlabel: x\nonboarding: [{dialog: 'Pick one'}]\n", "onboarding: entry 1: a screen yardr answers names its text and its accepting answers"},
		"onboarding, no dialog":               {"claude.yaml", "version: 1\nlabel: x\nonboarding: [{answers: ['Yes']}]\n", "onboarding: entry 1: a screen yardr answers names its text and its accepting answers"},
		"onboarding, env":                     {"claude.yaml", "version: 1\nlabel: x\nonboarding: [{dialog: 'Pick one', answers: ['Yes'], env: ['A=1']}]\n", "onboarding: entry 1 takes no env"},
		"usage, no model":                     {"claude.yaml", "version: 1\nlabel: x\nusage: [{usage: u, input: i}]\n", "usage: without a model source"},
		"usage, no path":                      {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{input: i}]\n", "usage: entry 1: usage is needed"},
		"usage, no fields":                    {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{usage: u}]\n", "usage: entry 1: none of input, output, cache_read and cache_write names a field"},
		"usage, a field not known":            {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{usage: u, input: i, total: t}]\n", "field total not found"},
		"usage, an unknown read":              {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{usage: u, input: i, read: all}]\n", `usage: entry 1: read "all" is neither last nor sum`},
		"usage, a key and no sum":             {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{usage: u, input: i, key: id}]\n", "usage: entry 1: key counts one line for each of its values under read: sum"},
		"usage, a key and read last":          {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{usage: u, input: i, key: id, read: last}]\n", "usage: entry 1: key counts one line for each of its values under read: sum"},
		"usage, an unknown at_unit":           {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{usage: u, input: i, at: t, at_unit: ns}]\n", `usage: entry 1: at_unit "ns" is none of s, ms, us`},
		"usage, cache in input of none":       {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{usage: u, input: i, cache_in_input: true}]\n", "usage: entry 1: cache_in_input names no cache_read"},
		"usage, cache in input without input": {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{usage: u, cache_read: c, cache_in_input: true}]\n", "usage: entry 1: cache_in_input names no input"},
		"usage, the second entry is wrong":    {"claude.yaml", "version: 1\nlabel: x\n" + model + "usage: [{usage: u, input: i}, {usage: u}]\n", "usage: entry 2:"},
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

// A file names its hint's colour as SGR parameters, read in canonical
// shape; muse's is the grey its 1.4.11 hint is drawn in, and Kiro keeps
// its named words alone.
func TestPlaceholderSGR(t *testing.T) {
	hs, err := load(t, map[string]string{
		"claude.yaml": "version: 1\nlabel: x\nmarks: {input: '>', placeholder_sgr: '38;5;007'}\n",
	})
	if err != nil || len(hs) != 1 || hs[0].Marks.PlaceholderSGR != "38;5;7" {
		t.Fatalf("a padded colour: %+v, %v; want 38;5;7", hs, err)
	}
	muse, ok := Lookup("muse")
	if !ok || muse.Marks.PlaceholderSGR != "38;2;103;108;116" {
		t.Errorf("muse's placeholder_sgr: %q, %v", muse.Marks.PlaceholderSGR, ok)
	}
	kiro, _ := Lookup("kiro")
	if kiro.Marks.PlaceholderSGR != "" {
		t.Errorf("kiro's placeholder_sgr: %q; its named words still tell it", kiro.Marks.PlaceholderSGR)
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
	// The scripts sleep by absolute path: PATH above holds the fake tool
	// alone, so a bare sleep would fail and answer at once instead. The
	// hanging one execs its sleep, so the timeout kills the sleeper itself
	// instead of leaving it holding the output pipe open.
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "kimi"), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A script's start alone is slow under load, so the answering cases run
	// under a bound far longer than a person's (the doctor tests' shape);
	// the slow stand-in below runs under the bound itself, and only the
	// hanging case runs short.
	old := harnessVersionTimeout
	harnessVersionTimeout = 30 * time.Second
	defer func() { harnessVersionTimeout = old }()
	write("#!/bin/sh\necho\necho 'kimi-code 2.9.9 (ae6bbc1 2026-10-20)'\necho noise\n")
	if got, want := HarnessVersion(ctx, "kimi"), "kimi-code 2.9.9 (ae6bbc1 2026-10-20)"; got != want {
		t.Errorf("version %q, want %q", got, want)
	}
	write("#!/bin/sh\necho boom >&2\nexit 1\n")
	if got := HarnessVersion(ctx, "kimi"); got != "" {
		t.Errorf("a failing harness: version %q, want none", got)
	}
	// Past the old five seconds, within the twenty now allowed: a slow
	// answer still yields its version.
	harnessVersionTimeout = old
	write("#!/bin/sh\n/bin/sleep 6\necho 'kimi-code 2.9.9 (ae6bbc1 2026-10-20)'\n")
	if got, want := HarnessVersion(ctx, "kimi"), "kimi-code 2.9.9 (ae6bbc1 2026-10-20)"; got != want {
		t.Errorf("a slow harness: version %q, want %q", got, want)
	}
	write("#!/bin/sh\nexec /bin/sleep 30\n")
	harnessVersionTimeout = 50 * time.Millisecond
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

// What a session used is read from lines of the agent's own file. These are
// trimmed from two real transcripts, read on 22 Oct 2026: the builder session
// of yardr-mnc8 (Claude Code 2.1.288) and the review of the same bead
// (codex-cli 0.160.1). What is left out is what no reading names: message
// content, bookkeeping keys, the rate limits and the text of the user line.
var (
	// The first lines of the builder session: the user's, then 8 assistant
	// lines that are 5 messages, since the lines of one message repeat its
	// usage (the first message has one line, the second two, the third three).
	claudeLines = []string{
		`{"type":"user","timestamp":"2026-10-21T19:34:43.883Z","message":{"role":"user","content":"(the brief)"}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:48.246Z","message":{"id":"msg_011CgFweBRXKGoR2hEx6yoiJ","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":19126,"cache_read_input_tokens":27018,"output_tokens":107,"output_tokens_details":{"thinking_tokens":0}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:52.398Z","message":{"id":"msg_011CgFweY4kkJxVFnA6eEYRm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":13583,"cache_read_input_tokens":46144,"output_tokens":168,"output_tokens_details":{"thinking_tokens":19}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:53.257Z","message":{"id":"msg_011CgFweY4kkJxVFnA6eEYRm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":13583,"cache_read_input_tokens":46144,"output_tokens":168,"output_tokens_details":{"thinking_tokens":19}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:55.873Z","message":{"id":"msg_011CgFwerz3BxAP2kKW4BjFm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":1802,"cache_read_input_tokens":59727,"output_tokens":339,"output_tokens_details":{"thinking_tokens":52}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:56.499Z","message":{"id":"msg_011CgFwerz3BxAP2kKW4BjFm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":1802,"cache_read_input_tokens":59727,"output_tokens":339,"output_tokens_details":{"thinking_tokens":52}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:34:57.579Z","message":{"id":"msg_011CgFwerz3BxAP2kKW4BjFm","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":1802,"cache_read_input_tokens":59727,"output_tokens":339,"output_tokens_details":{"thinking_tokens":52}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:35:00.199Z","message":{"id":"msg_011CgFwfBJctjAPdtfzqaDWx","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":5325,"cache_read_input_tokens":61529,"output_tokens":137,"output_tokens_details":{"thinking_tokens":0}}}}`,
		`{"type":"assistant","timestamp":"2026-10-21T19:35:02.968Z","message":{"id":"msg_011CgFwfN32WcU1xs6fcMw4q","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":1079,"cache_read_input_tokens":66854,"output_tokens":180,"output_tokens_details":{"thinking_tokens":0}}}}`,
	}
	// The last line of that file, whole: Claude Code's own totals as it exited,
	// one entry for each model it called.
	claudeCostState = `{"type":"cost-state","sessionId":"4b176be6-9160-4463-9056-17f3f0c8a34f","totalCostUSD":8.198233400000003,"totalAPIDuration":901193,"totalAPIDurationWithoutRetries":898403,"totalToolDuration":332809,"totalLinesAdded":474,"totalLinesRemoved":0,"totalDuration":2155171,"startTime":1792611279681,"modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":960,"outputTokens":23,"thinkingTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"webSearchRequests":0,"costUSD":0.001075},"claude-opus-5-5":{"inputTokens":752,"outputTokens":74978,"thinkingTokens":17031,"cacheReadInputTokens":23562912,"cacheCreationInputTokens":247751,"webSearchRequests":0,"costUSD":8.197158400000003}},"hasUnknownModelCost":false}`
	// The review session: lines that are no count (the turn's start, its
	// context, and a per-call record that carries a usage object but no
	// payload.type), then the session's first, 27th and last token_count. The
	// last is the session's total: 11,714,314 input, 11,487,872 of them
	// cached, and 45,824 output.
	codexLines = []string{
		`{"timestamp":"2026-10-21T20:10:45.638Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-10-21T20:10:48.457Z","ordinal":7,"type":"turn_context","payload":{"model":"gpt-6-luna"}}`,
		`{"timestamp":"2026-10-21T20:10:53.936Z","ordinal":15,"type":"token_usage_record","payload":{"response_id":"resp_0a65e4e92bef6c0a016ad91c4ad7188191af4581bc55501c19","usage":{"input_tokens":18104,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":368,"reasoning_output_tokens":62,"total_tokens":18472}}}`,
		`{"timestamp":"2026-10-21T20:10:55.310Z","ordinal":21,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":18104,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":368,"reasoning_output_tokens":62,"total_tokens":18472},"last_token_usage":{"input_tokens":18104,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":368,"reasoning_output_tokens":62,"total_tokens":18472},"model_context_window":258400}}}`,
		`{"timestamp":"2026-10-21T20:14:55.079Z","ordinal":291,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2879571,"cached_input_tokens":2709248,"cache_write_input_tokens":0,"output_tokens":25036,"reasoning_output_tokens":17421,"total_tokens":2904607},"last_token_usage":{"input_tokens":147789,"cached_input_tokens":146688,"cache_write_input_tokens":0,"output_tokens":450,"reasoning_output_tokens":211,"total_tokens":148239},"model_context_window":258400}}}`,
		`{"timestamp":"2026-10-21T20:34:17.164Z","ordinal":681,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":11714314,"cached_input_tokens":11487872,"cache_write_input_tokens":0,"output_tokens":45824,"reasoning_output_tokens":33352,"total_tokens":11760138},"last_token_usage":{"input_tokens":194899,"cached_input_tokens":193408,"cache_write_input_tokens":0,"output_tokens":114,"reasoning_output_tokens":33,"total_tokens":195013},"model_context_window":258400}}}`,
	}
	// A token_count that carries no info. Constructed: none of the files read
	// has one, but the key is there for a call that has not been counted yet.
	codexNoInfo = `{"timestamp":"2026-10-21T20:34:18.000Z","ordinal":682,"type":"event_msg","payload":{"type":"token_count","info":null}}`
)

// tally is what src reads from lines as the runner reads a file: each line
// through UsageIn, and those that read added in the order of the file.
func tally(src UsageSource, lines ...string) (Usage, bool) {
	t := src.Tally()
	for _, line := range lines {
		if l, ok := src.UsageIn([]byte(line)); ok {
			t.Add(l)
		}
	}
	return t.Usage()
}

// Claude's file and Codex's say where a session's count is read, and their
// own entries, run over lines of real transcripts, give the figures the design
// reads off the whole files (docs/design/2026-10-21-yardr-qbqq-session-tokens.md,
// section 2): Claude Code's own totals with its side calls added to the main
// model's, the message lines of the fallback counted once for each message.id
// and not once for each line, and Codex's last token_count with the cache taken
// off its input. No other kind names a source yet.
func TestUsageSources(t *testing.T) {
	for _, h := range Harnesses {
		if want := map[string]int{"claude": 2, "codex": 1}[h.Kind]; len(h.Usage) != want {
			t.Errorf("%s names %d usage sources, want %d", h.Kind, len(h.Usage), want)
		}
	}

	claude, _ := Lookup("claude")
	totals, lines := claude.Usage[0], claude.Usage[1]
	if totals.Partial || !lines.Partial {
		t.Errorf("partial: cost-state %v, message lines %v; only the lines fall short of the session", totals.Partial, lines.Partial)
	}
	session := append(slices.Clone(claudeLines), claudeCostState)

	// Opus 752 / 74,978 / 23,562,912 / 247,751, and Haiku's 960 and 23 for the
	// calls that have no line of their own.
	if got, ok := tally(totals, session...); !ok || got != (Usage{Input: 752 + 960, Output: 74978 + 23, CacheRead: 23562912, CacheWrite: 247751}) {
		t.Errorf("cost-state reads %+v, %v", got, ok)
	}
	// A session still running has written no cost-state line: that is no count,
	// not a count of 0, so the caller can wait for it.
	if got, ok := tally(totals, claudeLines...); ok {
		t.Errorf("a file with no cost-state line reads %+v", got)
	}
	l, ok := totals.UsageIn([]byte(claudeCostState))
	if !ok || !l.At.IsZero() || l.Key != "" {
		t.Errorf("cost-state line: %+v, %v; it has no time of its own and no key", l, ok)
	}

	// 5 messages in 8 lines: counted once for each message.id, not once for each line.
	if got, ok := tally(lines, session...); !ok || got != (Usage{Input: 10, Output: 931, CacheRead: 261272, CacheWrite: 40915}) {
		t.Errorf("message lines read %+v, %v; want each of the 5 messages once", got, ok)
	}
	every := lines
	every.Key = ""
	if got, _ := tally(every, session...); got != (Usage{Input: 16, Output: 1777, CacheRead: 426870, CacheWrite: 58102}) {
		t.Errorf("every line read %+v; this is what the key keeps out", got)
	}
	l, ok = lines.UsageIn([]byte(claudeLines[1]))
	if want := time.Date(2026, 10, 21, 19, 34, 48, 246_000_000, time.UTC); !ok || !l.At.Equal(want) || l.Key != "msg_011CgFweBRXKGoR2hEx6yoiJ" ||
		l.Usage != (Usage{Input: 2, Output: 107, CacheRead: 27018, CacheWrite: 19126}) {
		t.Errorf("first assistant line: %+v, %v", l, ok)
	}
	for _, line := range []string{claudeLines[0], claudeCostState} {
		if l, ok := lines.UsageIn([]byte(line)); ok {
			t.Errorf("the message lines read %s: %+v", line[:20], l)
		}
	}

	// The context source reads the lines it always did: the last assistant
	// line's input and cache tokens, 2 + 1,079 + 66,854.
	var used int
	for _, line := range session {
		if n, has := claude.Context.UsageIn([]byte(line)); has {
			used = n
		}
	}
	if used != 2+1079+66854 {
		t.Errorf("claude's context use over the same lines = %d, want 67935", used)
	}

	codex, _ := Lookup("codex")
	last := codex.Usage[0]
	if last.Partial {
		t.Error("codex's last token_count falls short of the session")
	}
	// No token_count yet is no count; each one after is the running total, so
	// the last read is the session's, its input without the cache.
	if got, ok := tally(last, codexLines[:3]...); ok {
		t.Errorf("lines before the first token_count read %+v", got)
	}
	for _, c := range []struct {
		upto int
		want Usage
	}{
		{4, Usage{Input: 18104, Output: 368}},
		{5, Usage{Input: 2879571 - 2709248, Output: 25036, CacheRead: 2709248}},
		{6, Usage{Input: 11714314 - 11487872, Output: 45824, CacheRead: 11487872}},
	} {
		if got, ok := tally(last, codexLines[:c.upto]...); !ok || got != c.want {
			t.Errorf("first %d lines read %+v, %v; want %+v", c.upto, got, ok, c.want)
		}
	}
	// A token_count with no info is passed over, and so is the per-call record
	// whose usage object sits beside no payload.type: neither takes the place
	// of the total before it.
	for _, line := range []string{codexNoInfo, codexLines[2]} {
		if l, ok := last.UsageIn([]byte(line)); ok {
			t.Errorf("%s read: %+v", line[:60], l)
		}
	}
	if got, _ := tally(last, append(slices.Clone(codexLines), codexNoInfo)...); got.Output != 45824 {
		t.Errorf("a token_count with no info after the total changed it to %+v", got)
	}
	l, ok = last.UsageIn([]byte(codexLines[5]))
	if want := time.Date(2026, 10, 21, 20, 34, 17, 164_000_000, time.UTC); !ok || !l.At.Equal(want) {
		t.Errorf("last token_count's time: %v, %v; want %v", l.At, ok, want)
	}

	// Every unit a file may name is one it loads with.
	for _, unit := range []string{"s", "ms", "us"} {
		body := "version: 1\nlabel: Q\nmodel: {home: .x, glob: '{session}.jsonl', field: m}\nusage: [{usage: u, input: i, at: t, at_unit: " + unit + "}]\n"
		if hs, err := load(t, map[string]string{"qwen.yaml": body}); err != nil || len(hs[0].Usage) != 1 || hs[0].Usage[0].AtUnit != unit {
			t.Errorf("at_unit %s: %+v, %v", unit, hs, err)
		}
	}
}

// One line of the session file reads as the counts at the source's Usage path
// in a line of its Type, in the five ways a UsageSource goes beyond a
// ContextSource: a type in a path of keys, a * that adds up a map, the four
// counts by name (the cache taken off the input when the harness counts it
// there), a key that tells a line from a repeat, and a time.
func TestUsageIn(t *testing.T) {
	for _, c := range []struct {
		name string
		src  UsageSource
		line string
		want UsageLine
		ok   bool
	}{
		{"a type in one key", UsageSource{Type: "turn", TypeField: "kind", Usage: "use", Input: "in"}, `{"kind":"turn","use":{"in":3}}`, UsageLine{Usage: Usage{Input: 3}}, true},
		{"the type is read from type_field", UsageSource{Type: "turn", TypeField: "kind", Usage: "use", Input: "in"}, `{"type":"turn","use":{"in":3}}`, UsageLine{}, false},
		{"the key is type by default", UsageSource{Type: "turn", Usage: "use", Input: "in"}, `{"type":"turn","use":{"in":3}}`, UsageLine{Usage: Usage{Input: 3}}, true},
		{"a type in a path", UsageSource{Type: "x", TypeField: "a.b", Usage: "u", Input: "i"}, `{"a":{"b":"x"},"u":{"i":3}}`, UsageLine{Usage: Usage{Input: 3}}, true},
		{"another type in a path", UsageSource{Type: "x", TypeField: "a.b", Usage: "u", Input: "i"}, `{"a":{"b":"y"},"u":{"i":3}}`, UsageLine{}, false},
		{"a path through what is no object", UsageSource{Type: "x", TypeField: "a.b", Usage: "u", Input: "i"}, `{"a":"x","u":{"i":3}}`, UsageLine{}, false},
		{"a path with no type named reads any line", UsageSource{Usage: "u", Input: "i"}, `{"u":{"i":1}}`, UsageLine{Usage: Usage{Input: 1}}, true},

		{"every value of a map", UsageSource{Usage: "m.*", Input: "i", Output: "o"}, `{"m":{"a":{"i":1,"o":10},"b":{"i":2},"c":7}}`, UsageLine{Usage: Usage{Input: 3, Output: 10}}, true},
		{"a * in the middle of the path", UsageSource{Usage: "m.*.n", Input: "i"}, `{"m":{"a":{"n":{"i":1}},"b":{"n":{"i":2}},"c":{"x":{"i":9}}}}`, UsageLine{Usage: Usage{Input: 3}}, true},
		{"a * where there is no map", UsageSource{Usage: "m.*", Input: "i"}, `{"m":5}`, UsageLine{}, false},
		{"a * over an empty map", UsageSource{Usage: "m.*", Input: "i"}, `{"m":{}}`, UsageLine{}, false},

		{"a count the object lacks reads 0", UsageSource{Usage: "u", Input: "i", Output: "o"}, `{"u":{"o":5}}`, UsageLine{Usage: Usage{Output: 5}}, true},
		{"a count no field is named for reads 0", UsageSource{Usage: "u", Input: "i"}, `{"u":{"i":4,"o":9}}`, UsageLine{Usage: Usage{Input: 4}}, true},
		{"none of the fields", UsageSource{Usage: "u", Input: "i", Output: "o"}, `{"u":{"x":5}}`, UsageLine{}, false},
		{"a field that is no number", UsageSource{Usage: "u", Input: "i"}, `{"u":{"i":"5"}}`, UsageLine{}, false},
		{"all four", UsageSource{Usage: "u", Input: "i", Output: "o", CacheRead: "r", CacheWrite: "w"}, `{"u":{"i":1,"o":2,"r":3,"w":4}}`, UsageLine{Usage: Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}}, true},
		{"the cache is in the input", UsageSource{Usage: "u", Input: "i", CacheRead: "c", CacheInInput: true}, `{"u":{"i":100,"c":40}}`, UsageLine{Usage: Usage{Input: 60, CacheRead: 40}}, true},
		{"the cache is beside the input", UsageSource{Usage: "u", Input: "i", CacheRead: "c"}, `{"u":{"i":100,"c":40}}`, UsageLine{Usage: Usage{Input: 100, CacheRead: 40}}, true},
		{"the path is not an object", UsageSource{Usage: "u", Input: "i"}, `{"u":[{"i":1}]}`, UsageLine{}, false},

		{"a key", UsageSource{Usage: "u", Input: "i", Read: "sum", Key: "m.id"}, `{"m":{"id":"a"},"u":{"i":1}}`, UsageLine{Usage: Usage{Input: 1}, Key: "a"}, true},
		{"no key in the line", UsageSource{Usage: "u", Input: "i", Read: "sum", Key: "m.id"}, `{"m":{},"u":{"i":1}}`, UsageLine{}, false},
		{"a key that is no string", UsageSource{Usage: "u", Input: "i", Read: "sum", Key: "m.id"}, `{"m":{"id":7},"u":{"i":1}}`, UsageLine{}, false},
		{"an empty key", UsageSource{Usage: "u", Input: "i", Read: "sum", Key: "m.id"}, `{"m":{"id":""},"u":{"i":1}}`, UsageLine{}, false},

		{"not JSON", UsageSource{Usage: "u", Input: "i"}, `nope`, UsageLine{}, false},
		{"cut short", UsageSource{Usage: "u", Input: "i"}, `{"u":{"i":`, UsageLine{}, false},
		{"no line", UsageSource{Usage: "u", Input: "i"}, ``, UsageLine{}, false},
		{"null", UsageSource{Usage: "u", Input: "i"}, `null`, UsageLine{}, false},
		{"an array", UsageSource{Usage: "u", Input: "i"}, `[{"u":{"i":1}}]`, UsageLine{}, false},
	} {
		got, ok := c.src.UsageIn([]byte(c.line))
		if ok != c.ok || got != c.want {
			t.Errorf("%s: %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
	}

	// The line's time, whatever way the file gives it: the same instant as an
	// RFC 3339 string (Claude's and Codex's timestamp, with an offset too) and
	// as Unix time in microseconds, milliseconds or seconds (1792616839253217
	// is 21:07:19.253217 UTC on 21 Oct 2026, as Muse stamps its lines).
	instant := time.Date(2026, 10, 21, 21, 7, 19, 253_217_000, time.UTC)
	for _, c := range []struct {
		name, at, unit, line string
		want                 time.Time
	}{
		{"RFC 3339", "timestamp", "", `{"timestamp":"2026-10-21T21:07:19.253217Z","u":{"i":1}}`, instant},
		{"RFC 3339 with an offset", "timestamp", "", `{"timestamp":"2026-10-21T22:07:19.253217+01:00","u":{"i":1}}`, instant},
		{"RFC 3339, milliseconds", "timestamp", "", `{"timestamp":"2026-10-21T21:07:19.253Z","u":{"i":1}}`, instant.Add(-217 * time.Microsecond)},
		{"microseconds", "t", "us", `{"t":1792616839253217,"u":{"i":1}}`, instant},
		{"milliseconds", "t", "ms", `{"t":1792616839253,"u":{"i":1}}`, instant.Add(-217 * time.Microsecond)},
		{"seconds", "t", "s", `{"t":1792616839,"u":{"i":1}}`, instant.Add(-253_217 * time.Microsecond)},
		{"seconds by default", "t", "", `{"t":1792616839,"u":{"i":1}}`, instant.Add(-253_217 * time.Microsecond)},
		{"seconds with a fraction", "t", "s", `{"t":1792616839.5,"u":{"i":1}}`, instant.Add(246_783 * time.Microsecond)},
		{"in a path", "meta.at", "us", `{"meta":{"at":1792616839253217},"u":{"i":1}}`, instant},
	} {
		src := UsageSource{Usage: "u", Input: "i", At: c.at, AtUnit: c.unit}
		if got, ok := src.UsageIn([]byte(c.line)); !ok || !got.At.Equal(c.want) || got.Usage.Input != 1 {
			t.Errorf("%s: %v, %v; want %v", c.name, got.At, ok, c.want)
		}
	}
	// A time that is not there, or does not read, leaves the counts and no time.
	for name, c := range map[string]struct {
		src  UsageSource
		line string
	}{
		"no at named":             {UsageSource{Usage: "u", Input: "i"}, `{"timestamp":"2026-10-21T21:07:19Z","u":{"i":1}}`},
		"none in the line":        {UsageSource{Usage: "u", Input: "i", At: "timestamp"}, `{"u":{"i":1}}`},
		"no time":                 {UsageSource{Usage: "u", Input: "i", At: "timestamp"}, `{"timestamp":"yesterday","u":{"i":1}}`},
		"not a string":            {UsageSource{Usage: "u", Input: "i", At: "timestamp"}, `{"timestamp":true,"u":{"i":1}}`},
		"a number as RFC 3339":    {UsageSource{Usage: "u", Input: "i", At: "timestamp"}, `{"timestamp":"1792616839","u":{"i":1}}`},
		"a unit no file may name": {UsageSource{Usage: "u", Input: "i", At: "t", AtUnit: "ns"}, `{"t":1792616839253217000,"u":{"i":1}}`},
	} {
		if got, ok := c.src.UsageIn([]byte(c.line)); !ok || !got.At.IsZero() || got.Usage.Input != 1 {
			t.Errorf("%s: %+v, %v; want the count and no time", name, got, ok)
		}
	}
}

// A tally keeps what the source's read says: the last line that read, the sum
// of every line, or the sum of the last line for each key; none at all while
// no line has read.
func TestUsageTally(t *testing.T) {
	lines := []string{
		`{"u":{"i":1},"id":"a"}`,
		`{"u":{"i":10},"id":"b"}`,
		`{"nothing":"here"}`,
		`{"u":{"i":100},"id":"a"}`,
		`{"u":{"i":1000}}`, // no id
	}
	for _, c := range []struct {
		name  string
		src   UsageSource
		lines []string
		want  Usage
		ok    bool
	}{
		{"last, the default", UsageSource{Usage: "u", Input: "i"}, lines, Usage{Input: 1000}, true},
		{"last, by name", UsageSource{Usage: "u", Input: "i", Read: "last"}, lines[:4], Usage{Input: 100}, true},
		{"sum", UsageSource{Usage: "u", Input: "i", Read: "sum"}, lines, Usage{Input: 1111}, true},
		{"sum, once for each key, the last line of each", UsageSource{Usage: "u", Input: "i", Read: "sum", Key: "id"}, lines, Usage{Input: 110}, true},
		{"sum, once for each key, the first line of one key", UsageSource{Usage: "u", Input: "i", Read: "sum", Key: "id"}, lines[:2], Usage{Input: 11}, true},
		{"no line", UsageSource{Usage: "u", Input: "i"}, nil, Usage{}, false},
		{"no line that reads", UsageSource{Usage: "u", Input: "i", Read: "sum"}, lines[2:3], Usage{}, false},
		{"no line that has a key", UsageSource{Usage: "u", Input: "i", Read: "sum", Key: "id"}, lines[2:3], Usage{}, false},
		{"a line of 0 is a count", UsageSource{Usage: "u", Input: "i", Read: "sum"}, []string{`{"u":{"i":0}}`}, Usage{}, true},
	} {
		if got, ok := tally(c.src, c.lines...); got != c.want || ok != c.ok {
			t.Errorf("%s: %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

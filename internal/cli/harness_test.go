package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/agents"
)

// yardr harness prints what this build knows of the harness kinds: on a
// home with no yard and no terminal, creating nothing there. list names
// each kind with a file and those without; show prints one file as yardr
// reads it, says so for a kind herdr starts that has none, and refuses a
// kind herdr does not start.
func TestHarnessListAndShow(t *testing.T) {
	user := noHome(t)
	requireNonInteractive(t)
	var errs bytes.Buffer
	ask := func(want int, args ...string) string {
		t.Helper()
		out, code := executeOut(&app{stderr: &errs}, args...)
		if code != want {
			t.Fatalf("yardr %s: exit %d, want %d\n%s", strings.Join(args, " "), code, want, out)
		}
		return out
	}
	out := ask(0, "harness", "list")
	wantText(t, out, "KIND", "claude    Claude Code  v1    pane marks, error lines, dialogs told of, login screen, background work, reported model, context use, token count, trust dialog, onboarding screens answered, unattended flags,",
		"codex     Codex        v1    pane marks, error lines, dialogs told of, reported model, token count, trust dialog, unattended flags, model flag, flags always passed,",
		"muse      Muse Code    v1    pane marks, error lines, login screen, background work, reported model, token count, trust dialog, unattended flags,",
		"kimi      Kimi         v1    pane marks, error lines, dialogs told of, hints, trust dialog, trust environment, unattended flags,",
		"pi        pi           v1    model flag, version flag, one-shot call\n",
		"No file, so started as herdr starts it and nothing more known: gemini, cursor,")
	var listed struct {
		Harnesses []agents.Harness `json:"harnesses"`
	}
	if err := json.Unmarshal([]byte(ask(0, "harness", "list", "--json")), &listed); err != nil || len(listed.Harnesses) != len(agents.Harnesses) {
		t.Fatalf("list --json: %v, %d harnesses", err, len(listed.Harnesses))
	}

	out = ask(0, "harness", "show", "codex")
	wantText(t, out, "codex  Codex, by OpenAI  (harnesses/codex.yaml, version 1)", "get it: npm install -g @openai/codex",
		"on a Mac without a terminal: version 0.162.1 is fetched, checked against its sha256 and moved to ~/.local/bin/codex",
		"(verified from https://github.com/openai/codex/blob/main/README.md on 2026-10-13;",
		"trust: yardr answers its folder-trust dialog", "input begins with:    ›", "a message it took:    ›",
		"unattended: --dangerously-bypass-approvals-and-sandbox", "always:     --no-daemon",
		"model flag: --model",
		"one shot:   exec --skip-git-repo-check <prompt>", `permission "approve for me": --approve-for-me  (automatic review`,
		"~/.codex (or $CODEX_HOME)/sessions/*/*/*/rollout-*-{session}.jsonl: payload.model of the first line of type turn_context",
		"at_capacity    ^(Selected model is at capacity|Flex capacity unavailable)",
		"Dialogs it draws in place of its input: told of at once (nudge_held), never answered",
		"rate_limit_switch  ^Approaching rate limits",
		`rate_limit_switch  ^Switch to gpt-6-luna for lower credit usage\?`,
		"Models it offers", "gpt-6-luna (GPT-6 Luna), recommended for a yardmaster", "gpt-6-astra (GPT-6 Astra)",
		"Tokens a session used, counted once it has stopped\n  ~/.codex (or $CODEX_HOME)/sessions/*/*/*/rollout-*-{session}.jsonl\n",
		"  1. the last line whose payload.type is token_count that has numbers at payload.info.total_token_usage\n",
		"     counts: input input_tokens (includes the cache read), output output_tokens, cache read cached_input_tokens, cache write cache_write_input_tokens\n",
		"     time:   from timestamp, RFC 3339 when a string or Unix time in seconds\n")
	wantText(t, ask(0, "harness", "show", "kimi"), "trust: yardr answers its folder-trust dialog",
		"trust environment (automatic only): KIMI_CODE_TRUST_WORKSPACE",
		"after the box's edge: │", "a message it took:    ✨",
		"no source known: nothing is recorded and nothing compared",
		"Dialogs it draws in place of its input: told of at once (nudge_held), never answered",
		"cache_menu  ^This session has been idle for",
		"What it adds to a hint of yardr's",
		"reset (a prompt at a usage limit's reset that it has not taken): Kimi asks what to do with a session that sat idle,")
	if out := ask(0, "harness", "show", "codex"); strings.Contains(out, "adds to a hint") ||
		strings.Contains(out, "Login screen") || strings.Contains(out, "Background work") ||
		strings.Contains(out, "The context its agent holds") || strings.Contains(out, "First-start screens") {
		t.Errorf("show codex lists hints, a login screen, background work, a context use or first-start screens, and its file has none:\n%s", out)
	}
	if out := ask(0, "harness", "show", "kimi"); strings.Contains(out, "The context its agent holds") ||
		strings.Contains(out, "Tokens a session used") || strings.Contains(out, "First-start screens") {
		t.Errorf("show kimi lists a context use, a token count or first-start screens, and its file has none:\n%s", out)
	}
	wantText(t, ask(0, "harness", "show", "claude"), "trust: yardr answers its folder-trust dialog",
		"Login screen, drawn in place of its input: told of at once (nudge_held), never typed into",
		"  ^Select login method:", "  ^Paste code here if prompted",
		"Background work of its agent, drawn around its input: not idle to the idle limit while it shows",
		`   · \d+ shells? still running$`,
		"Dialogs it draws in place of its input: told of at once (nudge_held), never answered",
		`auto_mode_setup  ^Teach auto mode about your environment\?`)
	// Claude's file names a context, two entries of usage and three
	// first-start screens: each is printed in the order the file has it, with
	// what yardr does with it.
	wantText(t, ask(0, "harness", "show", "claude"),
		"The context its agent holds: a crew member is asked for a handoff at its --context-handoff percent of the window\n"+
			"  ~/.claude (or $CLAUDE_CONFIG_DIR)/projects/*/{session}.jsonl: input_tokens + cache_read_input_tokens + cache_creation_input_tokens at message.usage of the last line of type assistant that has any of them\n",
		"Tokens a session used, counted once it has stopped: the first entry that finds a line is the reading\n"+
			"  ~/.claude (or $CLAUDE_CONFIG_DIR)/projects/*/{session}.jsonl\n"+
			"  1. the last line of type cost-state that has numbers at modelUsage.* (the * adds up every key there)\n"+
			"     counts: input inputTokens, output outputTokens, cache read cacheReadInputTokens, cache write cacheCreationInputTokens\n"+
			"     time:   none of its own, so a count that stops at a time leaves it out\n"+
			"  2. every line of type assistant that has numbers at message.usage, the last for each message.id, added up (partial: it reads short of what the session used)\n"+
			"     counts: input input_tokens, output output_tokens, cache read cache_read_input_tokens, cache write cache_creation_input_tokens\n"+
			"     time:   from timestamp, RFC 3339 when a string or Unix time in seconds\n",
		"First-start screens, drawn before its input is up: yardr answers each once, in turn\n"+
			`  "Choose the text style": the cursor ❯ is moved onto "✔ Dark mode", then Enter`+"\n"+
			`  "Security notes:": Enter, once "Press Enter to continue…" shows`+"\n"+
			`  "Try the new fullscreen renderer?": the cursor ❯ is moved onto "2. Not now", then Enter`+"\n")
	var shown agents.Harness
	showJSON := ask(0, "harness", "show", "kimi", "--json")
	if err := json.Unmarshal([]byte(showJSON), &shown); err != nil ||
		shown.Kind != "kimi" || shown.Marks.Edge != "│" || len(shown.Errors) != 4 || shown.Errors[0].Reason != agents.ReasonRateLimited {
		t.Fatalf("show kimi --json: %v, %+v", err, shown)
	}
	if shown.Trust == nil || len(shown.Trust.EnvNames) != 1 || shown.Trust.EnvNames[0] != "KIMI_CODE_TRUST_WORKSPACE" ||
		strings.Contains(showJSON, "KIMI_CODE_TRUST_WORKSPACE=1") {
		t.Fatalf("show kimi --json exposes the trust environment: %s", showJSON)
	}
	if len(shown.Offers) != 3 || shown.Offers[0] != (agents.Offer{Model: "kimi-code/k3", Label: "K3", Recommended: "yardmaster"}) {
		t.Fatalf("show kimi --json offers: %+v", shown.Offers)
	}
	if shown.Provider != "Moonshot" {
		t.Fatalf("show kimi --json provider: %q", shown.Provider)
	}
	wantText(t, ask(0, "harness", "show", "kimi"), "kimi-code/k3 (K3), recommended for a yardmaster")
	wantText(t, ask(0, "harness", "show", "muse"), "muse  Muse Code, by Meta  (harnesses/muse.yaml, version 1)",
		"trust: yardr answers its folder-trust dialog", "input begins with:    ❯", "a message it took:    ❯",
		"before an error line: any of ◆", "unattended: --yolo", "model flag: --model", "one shot:   exec <prompt>",
		`permission "yolo": --yolo`, `permission "no approval": --disable-approval`,
		"~/.local/share/muse/sessions/*/*/*/{session}/session.jsonl: payload.record.model_id of the first line whose payload_type is run.model.configured",
		"Login screen, drawn in place of its input: told of at once (nudge_held), never typed into",
		"  ^(Log in with browser|Set an API key)", "  ^↓↑ to select · Esc to quit",
		"other   ^model failed:",
		"muse-spark-1.3-contributor (Spark 1.3 Contributor), recommended for a yardmaster", "muse-wonder-1.0 (Wonder 1.0)",
		"  1. every line whose payload.event.kind is goal_usage_attribution that has numbers at payload.event.record.quantity, added up\n",
		"     counts: input input_tokens (includes the cache read), output output_tokens, cache read cached_tokens, cache write none (reads as 0)\n",
		"     time:   from recorded_at, RFC 3339 when a string or Unix time in microseconds\n")
	wantText(t, ask(0, "harness", "show", "kiro"), "kiro  Kiro, by AWS  (harnesses/kiro.yaml, version 1)",
		"get it: curl -fsSL https://cli.kiro.dev/install | bash (https://kiro.dev/cli)",
		"trust: yardr answers no dialog of it", "input begins with:    ›", "a message it took:    no mark known",
		"an empty input shows: ask a question or describe a task", "before an error line: any of •▸✓✗⏸╰",
		"unattended: --trust-tools=*", "always:     chat", "model flag: --model", "one shot:   chat --no-interactive <prompt>",
		`permission "allow all tools": --trust-tools=*`, `permission "trust all tools": --trust-all-tools`,
		"no source known: nothing is recorded and nothing compared",
		"none known: nothing is reported, and session_idle with its tail is what there is",
		"claude-opus-5.5 (Opus 5.5), recommended for a yardmaster", "auto (Auto)", "claude-sonnet-4.5 (Sonnet 4.5)")
	wantText(t, ask(0, "harness", "show", "claude"), "claude  Claude Code, by Anthropic  (harnesses/claude.yaml, version 1)")
	wantText(t, ask(0, "harness", "show", "pi"), "Models it offers", "none named: --model takes any id",
		"pi  pi  (harnesses/pi.yaml, version 1)")
	shown = agents.Harness{}
	if err := json.Unmarshal([]byte(ask(0, "harness", "show", "pi", "--json")), &shown); err != nil || shown.Provider != "" {
		t.Fatalf("show pi --json: %v, provider %q", err, shown.Provider)
	}

	// A kind's fetch for a Mac is printed as its file has it, every target;
	// a kind with none says it is installed by hand.
	if err := json.Unmarshal([]byte(ask(0, "harness", "show", "claude", "--json")), &shown); err != nil || shown.Fetch == nil ||
		shown.Fetch.Path != "~/.local/bin/claude" || len(shown.Fetch.Targets) != 2 || shown.Fetch.Verified.On == "" ||
		shown.Provider != "Anthropic" ||
		!strings.HasPrefix(shown.Fetch.Targets["darwin/arm64"].URL, "https://downloads.claude.ai/claude-code-releases/"+shown.Fetch.Version+"/") {
		t.Fatalf("show claude --json: %v, fetch %+v, provider %q", err, shown.Fetch, shown.Provider)
	}
	wantText(t, ask(0, "harness", "show", "claude"), "is fetched, checked against its sha256 and run with `install "+shown.Fetch.Version+"`, which puts it at ~/.local/bin/claude")
	wantText(t, ask(0, "harness", "show", "kimi"), "on a Mac without a terminal: no way known, it is installed by hand")
	shown = agents.Harness{}
	if err := json.Unmarshal([]byte(ask(0, "harness", "show", "kimi", "--json")), &shown); err != nil || shown.Fetch != nil {
		t.Fatalf("show kimi --json: %v, fetch %+v", err, shown.Fetch)
	}

	// The text says everything the file says (docs/groups.md): each top-level
	// key --json prints for a kind has its words in the text. A key the
	// files gain later and the text has no section for fails here, naming
	// the key, so no key goes unprinted as context, usage and onboarding did
	// (yardr-x394).
	said := map[string]string{
		"kind": "(harnesses/", "version": ", version ", "label": "(harnesses/", "provider": ", by ",
		"aliases": "also asked for as: ", "install": "get it: ", "fetch": "on a Mac without a terminal: version ",
		"trust": "trust: yardr answers", "marks": "Marks its pane is read by", "flags": "Flags\n",
		"offers": "Models it offers", "model": "The model its agent reports", "context": "The context its agent holds",
		"usage": "Tokens a session used", "errors": "Error lines it stops on", "dialogs": "Dialogs it draws",
		"login": "Login screen", "login_recipe": "Login led by a program", "login_env": "API key it runs with",
		"api_key": "Use-this-API-key dialog", "onboarding": "First-start screens", "background": "Background work",
		"hints": "What it adds to a hint",
	}
	for _, h := range agents.Harnesses {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal([]byte(ask(0, "harness", "show", h.Kind, "--json")), &keys); err != nil {
			t.Fatalf("show %s --json: %v", h.Kind, err)
		}
		text := ask(0, "harness", "show", h.Kind)
		for key := range keys {
			if words, ok := said[key]; !ok {
				t.Errorf("show %s --json prints %q and the text has no section for it: print one in printHarness, then add its words to said", h.Kind, key)
			} else if !strings.Contains(text, words) {
				t.Errorf("show %s --json prints %q and the text lacks %q:\n%s", h.Kind, key, words, text)
			}
		}
	}

	// A synthetic file covers shapes the embedded harnesses do not: an
	// explicit context token list and type key, usage times in milliseconds
	// and seconds, an entry with no type, and multiple accepted answers.
	h := agents.Harness{
		Kind: "synthetic", Label: "Synthetic", Version: 1,
		Model:   &agents.ModelSource{Home: ".syn", Glob: "s/{session}.jsonl", Field: "model"},
		Context: &agents.ContextSource{Type: "turn", TypeField: "kind", Usage: "info", Tokens: []string{"prompt", "cached"}},
		Usage: []agents.UsageSource{
			{Usage: "totals", Input: "in", At: "at", AtUnit: "ms"},
			{Type: "done", Usage: "totals", Read: "sum", Output: "out", At: "at", AtUnit: "s"},
		},
		Onboarding: []agents.Trust{{Dialog: "Pick one", Answers: []string{"Yes", "Sure"}, Cursor: ">"}},
	}
	var synthetic bytes.Buffer
	printHarness(&synthetic, h, true)
	wantText(t, synthetic.String(),
		"The context its agent holds: a crew member is asked for a handoff at its --context-handoff percent of the window\n"+
			"  ~/.syn/s/{session}.jsonl: prompt + cached at info of the last line whose kind is turn that has any of them\n",
		"Tokens a session used, counted once it has stopped: the first entry that finds a line is the reading\n"+
			"  ~/.syn/s/{session}.jsonl\n"+
			"  1. the last line that has numbers at totals\n"+
			"     counts: input in, output none (reads as 0), cache read none (reads as 0), cache write none (reads as 0)\n"+
			"     time:   from at, RFC 3339 when a string or Unix time in milliseconds\n"+
			"  2. every line of type done that has numbers at totals, added up\n"+
			"     counts: input none (reads as 0), output out, cache read none (reads as 0), cache write none (reads as 0)\n"+
			"     time:   from at, RFC 3339 when a string or Unix time in seconds\n",
		"First-start screens, drawn before its input is up: yardr answers each once, in turn\n"+
			`  "Pick one": the cursor > is moved onto "Yes" or "Sure", then Enter`+"\n")

	// A file with none of the three prints none of their headings.
	h.Context, h.Usage, h.Onboarding = nil, nil, nil
	synthetic.Reset()
	printHarness(&synthetic, h, true)
	for _, heading := range []string{"The context its agent holds", "Tokens a session used", "First-start screens"} {
		if strings.Contains(synthetic.String(), heading) {
			t.Errorf("a file with no context, usage or onboarding prints %q:\n%s", heading, synthetic.String())
		}
	}

	wantText(t, ask(0, "harness", "show", "cursor"), "cursor: no harness file in this build of yardr.")
	if err := json.Unmarshal([]byte(ask(0, "harness", "show", "cursor", "--json")), &shown); err != nil || shown.Kind != "cursor" || shown.Version != 0 {
		t.Fatalf("show cursor --json: %v, %+v", err, shown)
	}
	if ask(1, "harness", "show", "nope"); !strings.Contains(errs.String(), "nope is no harness kind; herdr starts: claude, codex,") {
		t.Errorf("a kind herdr does not start: %q", errs.String())
	}

	if entries, err := os.ReadDir(user); err != nil || len(entries) != 0 {
		t.Errorf("yardr harness left %d entries in a home with no yard: %v", len(entries), err)
	}
}

// doctor --json carries each harness kind's offered models on its machine
// item, the same list harness show --json carries: what a program offers a
// person choosing a model (footplate's setup reads doctor). A kind whose
// file names none carries none.
func TestDoctorOffers(t *testing.T) {
	requireNonInteractive(t)
	fakeTools(t, map[string]string{"claude": "echo 2.1.288\n", "codex": "echo 0.160.1\n", "kimi": "echo 2.1.1\n", "grok": "echo 1\n", "pi": "echo 1\n", "muse": "echo 1.4.9\n", "kiro-cli": "echo 2.32.0\n"})
	stub(t, &loginShell, func() string { return "" })
	m := machineOf(t)
	if got := m["claude"].Offers; len(got) != 3 || got[0] != (agents.Offer{Model: "opus", Label: "Opus 5.5", Recommended: "yardmaster"}) {
		t.Errorf("claude offers: %+v", got)
	}
	if got := m["codex"].Offers; len(got) != 2 || got[0].Recommended != "yardmaster" || got[1].Recommended != "" {
		t.Errorf("codex offers: %+v", got)
	}
	if got := m["kimi"].Offers; len(got) != 3 || got[0].Model != "kimi-code/k3" {
		t.Errorf("kimi offers: %+v", got)
	}
	if got := m["muse"].Offers; len(got) != 2 || got[0] != (agents.Offer{Model: "muse-spark-1.3-contributor", Label: "Spark 1.3 Contributor", Recommended: "yardmaster"}) {
		t.Errorf("muse offers: %+v", got)
	}
	if got := m["kiro-cli"].Offers; len(got) != 3 || got[0] != (agents.Offer{Model: "claude-opus-5.5", Label: "Opus 5.5", Recommended: "yardmaster"}) {
		t.Errorf("kiro offers: %+v", got)
	}
	for _, kind := range []string{"grok", "pi", "git", "herdr"} {
		if got := m[kind].Offers; got != nil {
			t.Errorf("%s offers: %+v, want none", kind, got)
		}
	}
	raw, err := json.Marshal(m["claude"])
	if err != nil || !strings.Contains(string(raw), `"offers":[{"model":"opus","label":"Opus 5.5","recommended":"yardmaster"}`) {
		t.Errorf("the wire carries: %s, %v", raw, err)
	}
}

// doctor --json carries each harness kind's label and provider on its
// machine item, from the kind's file, as it carries its offers: what a
// program shows a person choosing a provider (footplate's setup reads
// doctor). A kind whose file names no provider carries none, and git and
// herdr carry neither.
func TestDoctorHarnessLabels(t *testing.T) {
	requireNonInteractive(t)
	fakeTools(t, map[string]string{"claude": "echo 2.1.288\n", "codex": "echo 0.160.1\n", "kimi": "echo 2.1.1\n", "grok": "echo 1\n", "opencode": "echo 1\n", "pi": "echo 1\n", "muse": "echo 1.4.9\n", "kiro-cli": "echo 2.32.0\n"})
	stub(t, &loginShell, func() string { return "" })
	m := machineOf(t)
	want := map[string][2]string{
		"claude": {"Claude Code", "Anthropic"}, "codex": {"Codex", "OpenAI"},
		"kimi": {"Kimi", "Moonshot"}, "grok": {"Grok", "xAI"},
		"opencode": {"opencode", ""}, "pi": {"pi", ""},
		"muse": {"Muse Code", "Meta"}, "kiro-cli": {"Kiro", "AWS"},
	}
	for program, w := range want {
		if got := m[program]; got.Label != w[0] || got.Provider != w[1] {
			t.Errorf("%s: label %q, provider %q, want %q, %q", program, got.Label, got.Provider, w[0], w[1])
		}
	}
	for _, program := range []string{"git", "herdr"} {
		if got := m[program]; got.Label != "" || got.Provider != "" {
			t.Errorf("%s: label %q, provider %q, want none", program, got.Label, got.Provider)
		}
	}
	raw, err := json.Marshal(m["claude"])
	if err != nil || !strings.Contains(string(raw), `"label":"Claude Code"`) || !strings.Contains(string(raw), `"provider":"Anthropic"`) {
		t.Errorf("the wire carries: %s, %v", raw, err)
	}
	if raw, err := json.Marshal(m["pi"]); err != nil || strings.Contains(string(raw), `"provider"`) {
		t.Errorf("a kind with no provider carries one on the wire: %s, %v", raw, err)
	}
}

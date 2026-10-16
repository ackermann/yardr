package cli

import (
	"bytes"
	"encoding/json"
	"io"
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
	setInteractive(t, false)
	ask := func(want int, args ...string) string {
		t.Helper()
		var code int
		out := capture(t, func() { code = execute(&app{}, args) })
		if code != want {
			t.Fatalf("yardr %s: exit %d, want %d\n%s", strings.Join(args, " "), code, want, out)
		}
		return out
	}
	out := ask(0, "harness", "list")
	wantText(t, out, "KIND", "claude    Claude Code  v1    pane marks, error lines, dialogs told of, login screen, background work, reported model, trust dialog,",
		"codex     Codex        v1    pane marks, error lines, reported model, trust dialog, unattended flags, model flag, flags always passed,",
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
		"Models it offers", "gpt-6-luna (GPT-6 Luna), recommended for a yardmaster", "gpt-6-astra (GPT-6 Astra)")
	wantText(t, ask(0, "harness", "show", "kimi"), "trust: yardr answers its folder-trust dialog",
		"after the box's edge: │", "a message it took:    ✨",
		"no source known: nothing is recorded and nothing compared",
		"Dialogs it draws in place of its input: told of at once (nudge_held), never answered",
		"cache_menu  ^This session has been idle for",
		"What it adds to a hint of yardr's",
		"reset (a prompt at a usage limit's reset that it has not taken): Kimi asks what to do with a session that sat idle,")
	if out := ask(0, "harness", "show", "codex"); strings.Contains(out, "Dialogs it draws") || strings.Contains(out, "adds to a hint") ||
		strings.Contains(out, "Login screen") || strings.Contains(out, "Background work") {
		t.Errorf("show codex lists dialogs, hints, a login screen or background work, and its file has none:\n%s", out)
	}
	wantText(t, ask(0, "harness", "show", "claude"), "trust: yardr answers its folder-trust dialog",
		"Login screen, drawn in place of its input: told of at once (nudge_held), never typed into",
		"  ^Select login method:", "  ^Paste code here if prompted",
		"Background work of its agent, drawn around its input: not idle to the idle limit while it shows",
		`   · \d+ shells? still running$`,
		"Dialogs it draws in place of its input: told of at once (nudge_held), never answered",
		`auto_mode_setup  ^Teach auto mode about your environment\?`)
	var shown agents.Harness
	if err := json.Unmarshal([]byte(ask(0, "harness", "show", "kimi", "--json")), &shown); err != nil ||
		shown.Kind != "kimi" || shown.Marks.Edge != "│" || len(shown.Errors) != 4 || shown.Errors[0].Reason != agents.ReasonRateLimited {
		t.Fatalf("show kimi --json: %v, %+v", err, shown)
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
		"none known: nothing is reported, and session_idle with its tail is what there is",
		"muse-spark-1.3-contributor (Spark 1.3 Contributor), recommended for a yardmaster", "muse-wonder-1.0 (Wonder 1.0)")
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

	wantText(t, ask(0, "harness", "show", "cursor"), "cursor: no harness file in this build of yardr.")
	if err := json.Unmarshal([]byte(ask(0, "harness", "show", "cursor", "--json")), &shown); err != nil || shown.Kind != "cursor" || shown.Version != 0 {
		t.Fatalf("show cursor --json: %v, %+v", err, shown)
	}
	var errs bytes.Buffer
	stub(t, &stderr, io.Writer(&errs))
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
	newHome(t)
	setInteractive(t, false)
	fakeTools(t, map[string]string{"claude": "echo 2.1.288\n", "codex": "echo 0.160.1\n", "kimi": "echo 2.1.1\n", "grok": "echo 1\n", "pi": "echo 1\n", "muse": "echo 1.4.9\n"})
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
	newHome(t)
	setInteractive(t, false)
	fakeTools(t, map[string]string{"claude": "echo 2.1.288\n", "codex": "echo 0.160.1\n", "kimi": "echo 2.1.1\n", "grok": "echo 1\n", "opencode": "echo 1\n", "pi": "echo 1\n", "muse": "echo 1.4.9\n"})
	stub(t, &loginShell, func() string { return "" })
	m := machineOf(t)
	want := map[string][2]string{
		"claude": {"Claude Code", "Anthropic"}, "codex": {"Codex", "OpenAI"},
		"kimi": {"Kimi", "Moonshot"}, "grok": {"Grok", "xAI"},
		"opencode": {"opencode", ""}, "pi": {"pi", ""},
		"muse": {"Muse Code", "Meta"},
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

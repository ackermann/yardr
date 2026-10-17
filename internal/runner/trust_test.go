package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// trustDialog is Claude Code 2.1.288's folder-trust dialog as herdr's
// `pane read --source visible` prints it at 80 columns, the cursor on the
// answer `on`. The question wraps: its parenthetical tail lands wherever
// the pane's width puts it, so only the question's start can mark it.
func trustDialog(on string) string {
	claude, _ := agents.Lookup("claude")
	mark := func(answer string) string {
		if answer == on {
			return " ❯ " + answer
		}
		return "   " + answer
	}
	return `b1   yard/b1  ❯ claude --permission-mode auto
────────────────────────────────────────────
 Accessing workspace:

 /private/tmp/y1z/wt/r1/b1

 Quick safety check: Is this a project you created or one you trust? (Like your
 own code, a well-known open source project, or work from your team). If not,
 take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

` + mark("No, exit") + "\n" + mark(claude.Trust.Answers[0]) + `

 Enter to confirm · Esc to cancel
`
}

// narrowTrustDialog is Claude Code's dialog at a pane narrow enough that
// the question fits one row: the mark matches there too.
func narrowTrustDialog(on string) string {
	claude, _ := agents.Lookup("claude")
	mark := func(answer string) string {
		if answer == on {
			return " ❯ " + answer
		}
		return "   " + answer
	}
	return "Quick safety check: Is this a project you created or one you trust?\n" + mark("No, exit") + "\n" + mark(claude.Trust.Answers[0]) + "\n"
}

// kimiTrustDialog is Kimi Code 2.1.1's folder-trust dialog as a pty read it,
// the cursor on the answer `on`.
func kimiTrustDialog(on string) string {
	kimi, _ := agents.Lookup("kimi")
	mark := func(answer string) string {
		if answer == on {
			return "   ❯ " + answer
		}
		return "     " + answer
	}
	return ` ────────────────────────────────────────────
  Trust this folder?
  ↑↓ navigate · Enter select · Esc exit

  /private/tmp/y1z/wt/r1/b1

  Project-level MCP servers are disabled until you explicitly choose Trust.
  Trust starts the listed project MCP targets and remembers this folder.

` + mark(kimi.Trust.Answers[0]) + `
     Enable project MCP servers. Remembered for this folder.

` + mark("Don't trust") + `
     Exit Kimi Code. Asked again next launch.
`
}

// kimiTrustDialog22 is Kimi Code 2.2.0's folder-trust dialog as a pane
// tail showed it (yardr-k9ew), the cursor on the answer `on`: the answer
// renamed to 'Trust and continue' and 'Don't trust' dropped for 'Exit'.
func kimiTrustDialog22(on string) string {
	kimi, _ := agents.Lookup("kimi")
	mark := func(answer string) string {
		if answer == on {
			return "   ❯ " + answer
		}
		return "     " + answer
	}
	return ` ────────────────────────────────────────────
  Trust this folder?
  ↑↓ navigate · Enter select · Esc exit

  /private/tmp/y1z/wt/r1/b1

  No project integrations or instructions to activate.
  Trust is remembered for this folder, including future
  project config changes.
  Tool approvals follow your permission settings.

` + mark(kimi.Trust.Answers[1]) + `
` + mark("Exit") + `
`
}

// kimiTrustDialogUnknown is a Kimi folder-trust dialog whose answers this
// yardr does not know (yardr-tzkk): a later version renamed them again,
// the cursor on the answer `on`.
func kimiTrustDialogUnknown(on string) string {
	mark := func(answer string) string {
		if answer == on {
			return "   ❯ " + answer
		}
		return "     " + answer
	}
	return ` ────────────────────────────────────────────
  Trust this folder?
  ↑↓ navigate · Enter select · Esc exit

  /private/tmp/y1z/wt/r1/b1

  No project integrations or instructions to activate.

` + mark("Trust this folder and remember it") + `
` + mark("Exit without trusting") + `
`
}

// codexTrustDialog is Codex's folder-trust dialog as a review session's
// pane showed it (codex-cli 0.160.1, 15 Oct 2026): one long paragraph,
// wrapped by the pane, then the answers, the cursor on the answer `on`.
func codexTrustDialog(on string) string {
	codex, _ := agents.Lookup("codex")
	mark := func(answer string) string {
		if answer == on {
			return "› " + answer
		}
		return "  " + answer
	}
	return `  Trust this folder? Codex can read, edit, and run files here, subject to your permission settings. Folder settings can run code automatically, even without a model request. Continue only if you trust
  these files. Your trust decision will be saved.
` + mark(codex.Trust.Answers[0]) + `
` + mark("2. Quit") + `
`
}

// museTrustDialog is Muse Code 1.4.9's folder-trust dialog as a pty read
// it, the cursor on the answer `on`. The dialog opens with its cursor on
// the accepting answer, and the cursor wraps neither way.
func museTrustDialog(on string) string {
	muse, _ := agents.Lookup("muse")
	mark := func(answer string) string {
		if answer == on {
			return "⟩ " + answer
		}
		return "  " + answer
	}
	return `Do you trust this workspace?
Workspace: /private/tmp/y1z/wt/r1/b1

Trusting allows project-local skills, rules, hooks, and plugin config to load before the model runs.
Only trust this workspace when you trust its contents.
` + mark(muse.Trust.Answers[0]) + `
` + mark("2  Quit") + `

Use Up/Down or 1/2, then Enter. Esc quits.
`
}

// trustHerdr is a fake herdr whose agent stops at the folder-trust dialog:
// agent start says agent_not_ready, the pane shows screen, `down` toggles
// it between no and yes as the real one wraps, redrawing after `redraw`
// seconds, and Enter answers it. `after` is the agent's status once Enter
// is sent. Every call is logged.
func trustHerdr(t *testing.T, no, yes, screen, after string) (h *Herdr, calls func() string) {
	t.Helper()
	return slowTrustHerdr(t, no, yes, screen, after, "0")
}

func slowTrustHerdr(t *testing.T, no, yes, screen, after, redraw string) (h *Herdr, calls func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	for name, text := range map[string]string{"no": no, "yes": yes, "screen": screen} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h = &Herdr{Bin: fakeHerdrBin(t, `cd `+dir+`
echo "$@" >> calls
case "$1 $2" in
  "agent start") echo '{"error":{"code":"agent_not_ready","message":"agent b1 is blocked during startup and is not ready for prompts"},"id":"x"}' >&2; exit 1 ;;
  "pane read") cat screen ;;
  "pane send-keys")
    case "$4" in
      down) toggle() { sleep `+redraw+`
          if cmp -s screen no; then cp yes next; elif cmp -s screen yes; then cp no next; else return; fi
          mv next screen; }
        if [ `+redraw+` = 0 ]; then toggle; else toggle >/dev/null 2>&1 & fi ;;
      enter) if cmp -s screen yes; then : > screen; touch answered; else touch wrong; fi ;;
    esac ;; # as herdr: nothing printed
  "agent get")
    if [ -e answered ]; then echo '{"result":{"agent":{"agent_status":"`+after+`"}}}'
    else echo '{"result":{"agent":{"agent_status":"blocked"}}}'; fi ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(dir, "wrong")); err == nil {
			t.Error("Enter was sent with the cursor off the accepting answer")
		}
	})
	return h, func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

func shortTrustWait(t *testing.T) {
	t.Helper()
	old := trustWait
	trustWait = 400 * time.Millisecond
	t.Cleanup(func() { trustWait = old })
}

// Found live (yardr-1znz): the first builder of a depot Claude Code had not
// seen stopped at the folder-trust dialog until someone answered it in its
// pane; (yardr-bgnn) every Kimi builder stopped at its own in its new
// worktree; and (yardr-mdz4) the first Codex review stopped at its own.
// yardr answers the dialog the kind's file names, moving the cursor onto
// the accepting answer first, and the agent gets its first prompt at once.
func TestStartAgentAcceptsFolderTrust(t *testing.T) {
	shortTrustWait(t)
	claude, _ := agents.Lookup("claude")
	kimi, _ := agents.Lookup("kimi")
	codex, _ := agents.Lookup("codex")
	muse, _ := agents.Lookup("muse")
	for _, c := range []struct {
		name     string
		entry    agents.Trust
		dialog   func(on string) string
		off, yes string
		start    string
		moves    bool // the cursor starts off the answer, so down is sent
	}{
		{"claude", *claude.Trust, trustDialog, "No, exit", claude.Trust.Answers[0], "No, exit", true},
		{"kimi, cursor on the answer", *kimi.Trust, kimiTrustDialog, "Don't trust", kimi.Trust.Answers[0], kimi.Trust.Answers[0], false},
		{"kimi, cursor off the answer", *kimi.Trust, kimiTrustDialog, "Don't trust", kimi.Trust.Answers[0], "Don't trust", true},
		{"kimi 2.2.0, cursor on the answer", *kimi.Trust, kimiTrustDialog22, "Exit", kimi.Trust.Answers[1], kimi.Trust.Answers[1], false},
		{"kimi 2.2.0, cursor off the answer", *kimi.Trust, kimiTrustDialog22, "Exit", kimi.Trust.Answers[1], "Exit", true},
		{"codex, cursor on the answer", *codex.Trust, codexTrustDialog, "2. Quit", codex.Trust.Answers[0], codex.Trust.Answers[0], false},
		{"codex, cursor off the answer", *codex.Trust, codexTrustDialog, "2. Quit", codex.Trust.Answers[0], "2. Quit", true},
		{"muse, cursor on the answer", *muse.Trust, museTrustDialog, "2  Quit", muse.Trust.Answers[0], muse.Trust.Answers[0], false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, calls := trustHerdr(t, c.dialog(c.off), c.dialog(c.yes), c.dialog(c.start), "idle")
			handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
				map[string]any{"agent": "b1", "pane": "w1:p1"}, &c.entry)
			if err != nil {
				t.Fatal(err)
			}
			if handle["trust_accepted"] != true || handle[store.PendingPrompt] != nil {
				t.Fatalf("handle = %v\n%s", handle, calls())
			}
			got := calls()
			down, enter, prompt := strings.Index(got, "pane send-keys w1:p1 down"), strings.Index(got, "pane send-keys w1:p1 enter"),
				strings.Index(got, "agent prompt b1 read the brief")
			if enter < 0 || prompt < enter || (down >= 0) != c.moves || (c.moves && enter < down) {
				t.Fatalf("want enter%s, then the prompt:\n%s", map[bool]string{true: " after down", false: ", no down"}[c.moves], got)
			}
		})
	}
}

// Found live: the dialog redraws a moment after a key, and a second `down`
// sent before it did wrapped the cursor back to "No, exit", so the dialog
// was never answered. One `down` per redraw.
func TestStartAgentTrustWaitsForRedraw(t *testing.T) {
	// The dialog does redraw, so no limit is waited for: on a busy machine
	// the fake's 0.3s can be several, which the 2s of a real yard would
	// take for a dialog that never redrew.
	old, oldRedraw := trustWait, trustRedrawWait
	trustWait, trustRedrawWait = 5*time.Second, time.Minute
	t.Cleanup(func() { trustWait, trustRedrawWait = old, oldRedraw })
	claude, _ := agents.Lookup("claude")
	h, calls := slowTrustHerdr(t, trustDialog("No, exit"), trustDialog(claude.Trust.Answers[0]), trustDialog("No, exit"), "idle", "0.3")
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, claude.Trust)
	if err != nil {
		t.Fatal(err)
	}
	got := calls()
	if handle["trust_accepted"] != true || strings.Count(got, " down") != 1 || !strings.Contains(got, "agent prompt") {
		t.Fatalf("handle = %v\n%s", handle, got)
	}
}

// An agent that is not idle soon after the dialog (slow, or at a next
// question) keeps its prompt pending for the dispatcher.
func TestStartAgentTrustThenNotReady(t *testing.T) {
	shortTrustWait(t)
	claude, _ := agents.Lookup("claude")
	h, calls := trustHerdr(t, trustDialog("No, exit"), trustDialog(claude.Trust.Answers[0]), trustDialog("No, exit"), "blocked")
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, claude.Trust)
	if err != nil {
		t.Fatal(err)
	}
	if handle["trust_accepted"] != true || handle[store.PendingPrompt] != "read the brief" {
		t.Fatalf("handle = %v", handle)
	}
	if strings.Contains(calls(), "agent prompt") {
		t.Fatalf("prompted an agent that was not ready:\n%s", calls())
	}
}

// Only the trust dialog is answered, and only when asked to: any other
// question, a group with trust=false, a dialog whose cursor cannot be put
// on the accepting answer, and either half of the dialog without the other
// are left for a human, no key sent that could answer something else.
func TestStartAgentLeavesOtherQuestions(t *testing.T) {
	shortTrustWait(t)
	claude, _ := agents.Lookup("claude")
	kimi, _ := agents.Lookup("kimi")
	codex, _ := agents.Lookup("codex")
	muse, _ := agents.Lookup("muse")
	stuck := strings.ReplaceAll(trustDialog("No, exit"), "❯ No, exit", "> No, exit")
	cases := []struct {
		name, screen string
		trust        *agents.Trust
		keys         string // the last key that may have been sent, "" for none
	}{
		{"another question", " Do you want to allow this?\n ❯ Yes\n   No\n", claude.Trust, ""},
		{"the question's lead inside a longer word", "Quick safety checklist:\n❯ Yes, I trust this folder\n", claude.Trust, ""},
		{"trust=false", trustDialog("No, exit"), nil, ""},
		{"the answer's words inside other text", "I typed: Yes, I trust this folder, honest\n", claude.Trust, ""},
		{"the answer at the end of a decorated line", "Quick safety check: " + claude.Trust.Dialog + "\n❯ Please choose " + claude.Trust.Answers[0] + "\n", claude.Trust, ""},
		{"the answer without the question", "❯ Yes, I trust this folder\n", claude.Trust, ""},
		{"kimi's answer without its title", "I typed: Trust this folder\n", kimi.Trust, ""},
		{"kimi 2.2.0's answer without its title", "I typed: Trust and continue\n", kimi.Trust, ""},
		{"kimi's title without its answer", "Trust this folder?\n", kimi.Trust, ""},
		{"codex, another question", " Do you want to allow this?\n › Yes\n   No\n", codex.Trust, ""},
		{"codex's title only inside another question", "A different dialog says Trust this folder? in its explanation\n› 1. Trust and continue\n", codex.Trust, ""},
		{"codex's answer without its question", "› 1. Trust and continue\n", codex.Trust, ""},
		{"codex's question without its answer", "Trust this folder? Codex can read, edit, and run files here\n", codex.Trust, ""},
		{"codex's answer at the end of a decorated line", "Trust this folder?\n› Please choose 1. Trust and continue\n", codex.Trust, ""},
		{"muse's answer without its question", "⟩ 1  Trust and continue\n", muse.Trust, ""},
		{"muse's question without its answer", "Do you trust this workspace?\n", muse.Trust, ""},
		{"cursor never reaches the answer", stuck, claude.Trust, "down"},
	}
	for _, c := range cases {
		h, calls := trustHerdr(t, trustDialog("No, exit"), trustDialog(claude.Trust.Answers[0]), c.screen, "idle")
		handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
			map[string]any{"agent": "b1", "pane": "w1:p1"}, c.trust)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := calls()
		if handle[store.PendingPrompt] != "read the brief" || handle["trust_accepted"] != nil ||
			strings.Contains(got, "enter") || strings.Contains(got, "agent prompt") ||
			(c.keys == "" && strings.Contains(got, "send-keys")) {
			t.Errorf("%s: handle = %v\n%s", c.name, handle, got)
		}
	}
}

// A harness that renamed its trust answers again (yardr-tzkk) is left for
// a human, no key sent that could answer something else: the known dialog
// with none of its known answers is the daemon's to tell of
// (session_harness_dialog), not the runner's to answer.
func TestStartAgentLeavesRenamedTrustAnswer(t *testing.T) {
	shortTrustWait(t)
	kimi, _ := agents.Lookup("kimi")
	for _, start := range []string{"Trust this folder and remember it", "Exit without trusting"} {
		h, calls := trustHerdr(t, kimiTrustDialog("Don't trust"), kimiTrustDialog(kimi.Trust.Answers[0]),
			kimiTrustDialogUnknown(start), "idle")
		handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
			map[string]any{"agent": "b1", "pane": "w1:p1"}, kimi.Trust)
		if err != nil {
			t.Fatal(err)
		}
		if handle[store.PendingPrompt] != "read the brief" || handle["trust_accepted"] != nil {
			t.Errorf("cursor on %q: handle = %v", start, handle)
		}
		if got := calls(); strings.Contains(got, "send-keys") || strings.Contains(got, "agent prompt") {
			t.Errorf("cursor on %q: a key was sent:\n%s", start, got)
		}
	}
}

// The dispatcher's later passes answer a trust dialog that drew only after
// the start looked (yardr-v3mp): AcceptTrust answers the dialog of the
// config's kind by the same rule as the start, and nothing else.
func TestAcceptTrustAnswersLateDialog(t *testing.T) {
	shortTrustWait(t)
	claude, _ := agents.Lookup("claude")
	codex, _ := agents.Lookup("codex")
	muse, _ := agents.Lookup("muse")
	yes := trustDialog(claude.Trust.Answers[0])
	narrowYes := narrowTrustDialog(claude.Trust.Answers[0])
	codexYes := codexTrustDialog(codex.Trust.Answers[0])
	museYes := museTrustDialog(muse.Trust.Answers[0])
	cases := []struct {
		name   string
		screen string
		handle map[string]any
		cfg    map[string]any
		want   bool
		keys   bool // a key may have been sent
	}{
		{"the dialog, drawn late", yes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, true, true},
		{"the dialog at a narrow width", narrowYes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, true, true},
		{"the dialog, kind unset", yes, map[string]any{"pane": "w1:p1"}, nil, true, true},
		{"codex's dialog, drawn late", codexYes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "codex"}, true, true},
		{"muse's dialog, drawn late", museYes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "muse"}, true, true},
		{"not drawn yet", "", map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, false, false},
		{"another question", " Do you want to allow this?\n ❯ Yes\n", map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, false, false},
		{"trust=false", yes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude", "trust": false}, false, false},
		{"a kind with no dialog", yes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "grok"}, false, false},
		{"no pane", yes, map[string]any{"agent": "b1"}, map[string]any{"kind": "claude"}, false, false},
	}
	for _, c := range cases {
		no, yesScreen := trustDialog("No, exit"), yes
		if c.screen == narrowYes {
			no, yesScreen = narrowTrustDialog("No, exit"), narrowYes
		}
		if kind, _ := c.cfg["kind"].(string); kind == "codex" {
			no, yesScreen = codexTrustDialog("2. Quit"), codexYes
		}
		if kind, _ := c.cfg["kind"].(string); kind == "muse" {
			no, yesScreen = museTrustDialog("2  Quit"), museYes
		}
		h, calls := trustHerdr(t, no, yesScreen, c.screen, "idle")
		if got := h.AcceptTrust(context.Background(), c.handle, c.cfg); got != c.want {
			t.Errorf("%s: AcceptTrust = %v, want %v\n%s", c.name, got, c.want, calls())
		}
		got := calls()
		if c.want && !strings.Contains(got, "pane send-keys w1:p1 enter") {
			t.Errorf("%s: no enter sent:\n%s", c.name, got)
		}
		if !c.keys && strings.Contains(got, "send-keys") {
			t.Errorf("%s: a key was sent:\n%s", c.name, got)
		}
	}
}

func TestTrustEntry(t *testing.T) {
	claude, _ := agents.Lookup("claude")
	kimi, _ := agents.Lookup("kimi")
	codex, _ := agents.Lookup("codex")
	muse, _ := agents.Lookup("muse")
	cases := []struct {
		cfg  map[string]any
		want *agents.Trust
	}{
		{nil, claude.Trust}, // kind defaults to claude
		{map[string]any{"kind": "claude"}, claude.Trust},
		{map[string]any{"kind": "claude", "trust": false}, nil},
		{map[string]any{"kind": "claude", "trust": true}, claude.Trust},
		{map[string]any{"kind": "kimi"}, kimi.Trust},
		{map[string]any{"kind": "kimi", "trust": false}, nil},
		{map[string]any{"kind": "codex"}, codex.Trust},
		{map[string]any{"kind": "codex", "trust": false}, nil},
		{map[string]any{"kind": "muse"}, muse.Trust},
		{map[string]any{"kind": "muse", "trust": false}, nil},
		{map[string]any{"kind": "grok"}, nil},
	}
	for _, c := range cases {
		got := trustEntry(c.cfg)
		if got != c.want {
			t.Errorf("trustEntry(%v) = %+v, want %+v", c.cfg, got, c.want)
		}
	}
}

package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
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

// apiKeyDialog is Claude Code 2.1.288's use-this-API-key dialog as herdr's
// `pane read --source visible` prints it, the cursor on the answer `on`
// ("Yes" or "No (recommended)"). Read live under a pty, 20 Oct 2026, with
// a stand-in key in a fresh home: the dialog opens with its cursor on No,
// and the key shows masked to its last 20 characters.
func apiKeyDialog(on string) string {
	mark := func(answer string) string {
		if answer == on {
			return " ❯ " + answer
		}
		return "   " + answer
	}
	return ` Detected a custom API key in your environment
 ANTHROPIC_API_KEY: sk-ant-...key-0000000000000000
 Do you want to use this API key?
` + mark("Yes") + "\n" + mark("No (recommended)") + `
 Enter to confirm · Esc to cancel
`
}

// themeDialog is Claude Code 2.1.288's theme picker as herdr's `pane read
// --source visible` prints it at 80 columns in a fresh home, the cursor on
// the option `on`: the first screen of a first start, before any login and
// before any input. Read live under a pty, 21 Oct 2026: the dialog opens
// with its cursor on Dark mode, which the ✔ marks as the current theme,
// and the cursor moves with the arrow keys while the ✔ stays where it is.
func themeDialog(on string) string {
	claude, _ := agents.Lookup("claude")
	dark := claude.Onboarding[0].Answers[0]
	mark := func(option string) string {
		if option == dark {
			if option == on {
				return " ❯ ✔ Dark mode"
			}
			return "   ✔ Dark mode"
		}
		if option == on {
			return " ❯   " + option
		}
		return "     " + option
	}
	return `Welcome to Claude Code v2.1.288

Let's get started.

Choose the text style that looks best with your terminal
To change this later, run /theme

` + mark("Auto (match terminal)") + "\n" + mark(dark) + "\n" + mark("Light mode") + "\n" +
		mark("Dark mode (colorblind-friendly)") + "\n" + mark("Light mode (colorblind-friendly)") + "\n" +
		mark("Dark mode (ANSI colors only)") + "\n" + mark("Light mode (ANSI colors only)") + "\n"
}

// securityDialog is Claude Code 2.1.288's security notes as herdr's `pane
// read --source visible` prints them at 80 columns in a fresh home: the
// screen after the theme picker (and, with a key set, the API-key dialog),
// answered with Enter alone. Read live under a pty, 21 Oct 2026, with a
// stand-in key.
func securityDialog() string {
	return ` Security notes:

 1. Claude can make mistakes.
    You're responsible for Claude's actions and should always
    review them, especially when running code.

 2. Due to prompt injection risks, only use it with code you trust
    Learn more: https://code.claude.com/docs/en/security

 Press Enter to continue…
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

// Found live (yardr-1znz): the first builder of a depot Claude Code had not
// seen stopped at the folder-trust dialog until someone answered it in its
// pane; (yardr-bgnn) every Kimi builder stopped at its own in its new
// worktree; and (yardr-mdz4) the first Codex review stopped at its own.
// yardr answers the dialog the kind's file names, moving the cursor onto
// the accepting answer first, and the agent gets its first prompt at once.
func TestStartAgentAcceptsFolderTrust(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			h, calls := trustHerdr(t, c.dialog(c.off), c.dialog(c.yes), c.dialog(c.start), "idle")
			handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
				map[string]any{"agent": "b1", "pane": "w1:p1"}, &c.entry, nil, nil)
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

// An agent started with an API key in its environment and no login stops
// at the harness's use-this-API-key dialog (yardr-q9t9). yardr answers
// Yes, moving the cursor onto it first, and the agent gets its first
// prompt at once; the folder-trust dialog beside it is untouched.
func TestStartAgentAcceptsAPIKey(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	for _, c := range []struct {
		name  string
		start string
		moves bool // the cursor starts off Yes, so down is sent
	}{
		{"cursor off Yes", "No (recommended)", true},
		{"cursor on Yes", "Yes", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			no, yes := apiKeyDialog("No (recommended)"), apiKeyDialog("Yes")
			h, calls := trustHerdr(t, no, yes, apiKeyDialog(c.start), "idle")
			handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
				map[string]any{"agent": "b1", "pane": "w1:p1"}, claude.Trust, claude.ApiKey, nil)
			if err != nil {
				t.Fatal(err)
			}
			if handle["key_accepted"] != true || handle["trust_accepted"] != nil || handle[store.PendingPrompt] != nil {
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

// An agent started in a fresh home stops at the harness's onboarding
// screens (yardr-dffh): the theme picker, then the security notes. yardr
// answers each in turn, moving the picker's cursor onto its default first,
// and the agent gets its first prompt at once; the security notes take
// Enter alone, with no cursor moved. The folder-trust and API-key dialogs
// beside them are untouched.
func TestStartAgentAcceptsOnboarding(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	dark := claude.Onboarding[0].Answers[0]
	for _, c := range []struct {
		name           string
		entry          int
		no, yes, start string
		moves          bool // the cursor starts off the answer, so down is sent
	}{
		{"theme, cursor on Dark mode", 0, themeDialog("Auto (match terminal)"), themeDialog(dark), themeDialog(dark), false},
		{"theme, cursor off Dark mode", 0, themeDialog("Auto (match terminal)"), themeDialog(dark), themeDialog("Auto (match terminal)"), true},
		{"security notes", 1, securityDialog(), securityDialog(), securityDialog(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, calls := trustHerdr(t, c.no, c.yes, c.start, "idle")
			handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
				map[string]any{"agent": "b1", "pane": "w1:p1"}, claude.Trust, claude.ApiKey, claude.Onboarding)
			if err != nil {
				t.Fatal(err)
			}
			if handle[dispatch.OnboardingAccepted(c.entry)] != true || handle["trust_accepted"] != nil ||
				handle["key_accepted"] != nil || handle[store.PendingPrompt] != nil {
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

// Only the use-this-API-key dialog is answered, and only when asked to:
// any other question, a kind with no dialog, and either half of the dialog
// without the other are left for a human, no key sent that could answer
// something else. `trust=false` leaves it answered: that flag is folder
// trust's.
func TestStartAgentLeavesOtherKeyQuestions(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	no, yes := apiKeyDialog("No (recommended)"), apiKeyDialog("Yes")
	cases := []struct {
		name, screen string
		key          *agents.Trust
	}{
		{"another question with Yes", " Do you want to allow this?\n ❯ Yes\n", claude.ApiKey},
		{"the folder-trust dialog", trustDialog("No, exit"), claude.ApiKey},
		{"the question without its answer", "Do you want to use this API key?\n ❯ No (recommended)\n", claude.ApiKey},
		{"the answer without the question", "❯ Yes\n", claude.ApiKey},
		{"no dialog named", yes, nil},
	}
	for _, c := range cases {
		h, calls := trustHerdr(t, no, yes, c.screen, "idle")
		handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
			map[string]any{"agent": "b1", "pane": "w1:p1"}, nil, c.key, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := calls()
		if handle[store.PendingPrompt] != "read the brief" || handle["key_accepted"] != nil || handle["trust_accepted"] != nil ||
			strings.Contains(got, "enter") || strings.Contains(got, "agent prompt") || strings.Contains(got, "send-keys") {
			t.Errorf("%s: handle = %v\n%s", c.name, handle, got)
		}
	}
	// trust=false is folder trust's alone: the key dialog is answered.
	h, _ := trustHerdr(t, no, yes, no, "idle")
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, nil, keyEntry(map[string]any{"kind": "claude", "trust": false}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if handle["key_accepted"] != true {
		t.Fatalf("with trust=false: handle = %v", handle)
	}
}

// Found live: the dialog redraws a moment after a key, and a second `down`
// sent before it did wrapped the cursor back to "No, exit", so the dialog
// was never answered. One `down` per redraw.
func TestStartAgentTrustWaitsForRedraw(t *testing.T) {
	// Serial: lengthens the trust waits past what TestMain shortens.
	// The dialog does redraw, so no limit is waited for: on a busy machine
	// the fake's 0.3s can be several, which the 2s of a real yard would
	// take for a dialog that never redrew.
	old, oldRedraw := trustWait, trustRedrawWait
	trustWait, trustRedrawWait = 5*time.Second, time.Minute
	t.Cleanup(func() { trustWait, trustRedrawWait = old, oldRedraw })
	claude, _ := agents.Lookup("claude")
	h, calls := slowTrustHerdr(t, trustDialog("No, exit"), trustDialog(claude.Trust.Answers[0]), trustDialog("No, exit"), "idle", "0.3")
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, claude.Trust, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := calls()
	if handle["trust_accepted"] != true || strings.Count(got, " down") != 1 || !strings.Contains(got, "agent prompt") {
		t.Fatalf("handle = %v\n%s", handle, got)
	}
}

// Found live for folder trust: the dialog redraws a moment after a key,
// and a second `down` sent before it did moved the cursor away again, so
// the dialog was never answered. One `down` per redraw, for the theme
// picker as for folder trust.
func TestStartAgentOnboardingWaitsForRedraw(t *testing.T) {
	// Serial: lengthens the trust waits past what TestMain shortens.
	// The dialog does redraw, so no limit is waited for: on a busy machine
	// the fake's 0.3s can be several, which the 2s of a real yard would
	// take for a dialog that never redrew.
	old, oldRedraw := trustWait, trustRedrawWait
	trustWait, trustRedrawWait = 5*time.Second, time.Minute
	t.Cleanup(func() { trustWait, trustRedrawWait = old, oldRedraw })
	claude, _ := agents.Lookup("claude")
	dark := claude.Onboarding[0].Answers[0]
	h, calls := slowTrustHerdr(t, themeDialog("Auto (match terminal)"), themeDialog(dark), themeDialog("Auto (match terminal)"), "idle", "0.3")
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, nil, nil, claude.Onboarding)
	if err != nil {
		t.Fatal(err)
	}
	got := calls()
	if handle[dispatch.OnboardingAccepted(0)] != true || strings.Count(got, " down") != 1 || !strings.Contains(got, "agent prompt") {
		t.Fatalf("handle = %v\n%s", handle, got)
	}
}

// An agent that is not idle soon after the dialog (slow, or at a next
// question) keeps its prompt pending for the dispatcher.
func TestStartAgentTrustThenNotReady(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	h, calls := trustHerdr(t, trustDialog("No, exit"), trustDialog(claude.Trust.Answers[0]), trustDialog("No, exit"), "blocked")
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, claude.Trust, nil, nil)
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
	t.Parallel()
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
			map[string]any{"agent": "b1", "pane": "w1:p1"}, c.trust, nil, nil)
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
	t.Parallel()
	kimi, _ := agents.Lookup("kimi")
	for _, start := range []string{"Trust this folder and remember it", "Exit without trusting"} {
		h, calls := trustHerdr(t, kimiTrustDialog("Don't trust"), kimiTrustDialog(kimi.Trust.Answers[0]),
			kimiTrustDialogUnknown(start), "idle")
		handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
			map[string]any{"agent": "b1", "pane": "w1:p1"}, kimi.Trust, nil, nil)
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

// A harness that renamed its onboarding answers again is left for a
// human, no key sent that could answer something else: the known screen
// with none of its known answers is the daemon's to tell of, not the
// runner's to answer.
func TestStartAgentLeavesRenamedOnboardingAnswer(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	dark := claude.Onboarding[0].Answers[0]
	renamedTheme := strings.Replace(themeDialog("Light mode"), "✔ Dark mode", "Dark theme", 1)
	renamedSecurity := strings.Replace(securityDialog(), "Press Enter to continue…", "Press Return to continue…", 1)
	for name, screen := range map[string]string{"theme": renamedTheme, "security notes": renamedSecurity} {
		h, calls := trustHerdr(t, themeDialog("Auto (match terminal)"), themeDialog(dark), screen, "idle")
		handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
			map[string]any{"agent": "b1", "pane": "w1:p1"}, nil, nil, claude.Onboarding)
		if err != nil {
			t.Fatal(err)
		}
		if handle[store.PendingPrompt] != "read the brief" || handle[dispatch.OnboardingAccepted(0)] != nil ||
			handle[dispatch.OnboardingAccepted(1)] != nil {
			t.Errorf("%s: handle = %v", name, handle)
		}
		if got := calls(); strings.Contains(got, "send-keys") || strings.Contains(got, "agent prompt") {
			t.Errorf("%s: a key was sent:\n%s", name, got)
		}
	}
}

// shownDialogHerdr is a fake herdr whose agent drew its startup dialog
// before the start looked: agent start succeeds, the pane shows screen,
// `down` toggles it between no and yes as the real one wraps, and Enter
// answers it. `agent prompt` is a tripwire: the start must answer the
// dialog instead of delivering into it.
func shownDialogHerdr(t *testing.T, no, yes, screen string) (h *Herdr, calls func() string) {
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
  "agent start") echo '{"result":{"type":"agent_started"}}' ;;
  "pane read") cat screen ;;
  "pane send-keys")
    case "$4" in
      down) if cmp -s screen no; then cp yes screen; elif cmp -s screen yes; then cp no screen; fi ;;
      enter) if cmp -s screen yes; then : > screen; touch answered; else touch wrong; fi ;;
    esac ;; # as herdr: nothing printed
  "agent prompt") touch prompted; echo '{"error":{"code":"agent_blocked","message":"at a dialog"},"id":"x"}' >&2; exit 1 ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(dir, "wrong")); err == nil {
			t.Error("Enter was sent with the cursor off the accepting answer")
		}
		if _, err := os.Stat(filepath.Join(dir, "prompted")); err == nil {
			t.Error("the first prompt was delivered into the showing dialog")
		}
	})
	return h, func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

// Found live (yardr-gkyy): the yardmaster's trust dialog drew before its
// start looked, so the screen read as up and the first prompt was typed
// into the dialog; its Enter selected "No, exit" and the session died
// three seconds old. A dialog showing at the start is answered instead of
// talked over: the prompt stays pending and no delivery is made. With
// trust=false the dialog still gates the delivery, and nothing is
// answered: the human answers it in the pane.
func TestStartAgentAnswersDialogShownAtStart(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	yes := claude.Trust.Answers[0]
	drawing := " Quick safety check: Is this a project you\n ❯ \n"
	for _, c := range []struct {
		name, screen string
		trust        *agents.Trust
		key          *agents.Trust
		mark         bool // the answer lands on the handle
		moves        bool // down is sent before enter
	}{
		{"cursor off the answer", trustDialog("No, exit"), claude.Trust, nil, true, true},
		{"cursor on the answer", trustDialog(yes), claude.Trust, nil, true, false},
		{"trust=false gates the delivery", trustDialog("No, exit"), nil, claude.ApiKey, false, false},
		{"the dialog still drawing", drawing, claude.Trust, nil, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, calls := shownDialogHerdr(t, trustDialog("No, exit"), trustDialog(yes), c.screen)
			handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
				map[string]any{"agent": "b1", "pane": "w1:p1", harnessKind: "claude"}, c.trust, c.key, nil)
			if err != nil {
				t.Fatal(err)
			}
			if handle[store.PendingPrompt] != "read the brief" || (handle["trust_accepted"] == true) != c.mark {
				t.Fatalf("handle = %v\n%s", handle, calls())
			}
			got := calls()
			down, enter := strings.Index(got, "pane send-keys w1:p1 down"), strings.Index(got, "pane send-keys w1:p1 enter")
			switch {
			case strings.Contains(got, "agent prompt"):
				t.Fatalf("delivered into the showing dialog:\n%s", got)
			case (enter >= 0) != c.mark:
				t.Fatalf("enter sent = %v, want answer = %v:\n%s", enter >= 0, c.mark, got)
			case (down >= 0) != c.moves:
				t.Fatalf("down sent = %v, want moves = %v:\n%s", down >= 0, c.moves, got)
			case c.moves && enter < down:
				t.Fatalf("enter before down:\n%s", got)
			}
		})
	}
}

// startupShown sees every startup dialog of the kind by its mark, drawn or
// drawing, and nothing else: another question and the bare input gate
// nothing, nor does an unknown kind or one with no dialog to answer.
func TestStartupShown(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	dark := claude.Onboarding[0].Answers[0]
	for _, c := range []struct {
		name, kind, screen string
		want               bool
	}{
		{"trust, drawn", "claude", trustDialog("No, exit"), true},
		{"trust, drawing", "claude", " Quick safety check: Is this a project you\n ❯ \n", true},
		{"api key", "claude", apiKeyDialog("No (recommended)"), true},
		{"theme picker", "claude", themeDialog(dark), true},
		{"security notes", "claude", securityDialog(), true},
		{"another question", "claude", " Do you want to allow this?\n ❯ Yes\n", false},
		{"the bare input", "claude", " ❯ \n", false},
		{"unknown kind", "nope", trustDialog("No, exit"), false},
		{"a kind with no dialog", "pi", trustDialog("No, exit"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, _ := shownDialogHerdr(t, c.screen, c.screen, c.screen)
			if got := h.startupShown(context.Background(), "w1:p1", c.kind); got != c.want {
				t.Errorf("startupShown(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// The dispatcher's later passes answer a trust dialog that drew only after
// the start looked (yardr-v3mp): AcceptTrust answers the dialog of the
// config's kind by the same rule as the start, and nothing else.
func TestAcceptTrustAnswersLateDialog(t *testing.T) {
	t.Parallel()
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

// The dispatcher's later passes answer a use-this-API-key dialog that drew
// only after the start looked, by the same rule as the start, and nothing
// else. `trust=false` leaves it answered.
func TestAcceptKeyAnswersLateDialog(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	no, yes := apiKeyDialog("No (recommended)"), apiKeyDialog("Yes")
	cases := []struct {
		name   string
		screen string
		handle map[string]any
		cfg    map[string]any
		want   bool
		keys   bool // a key may have been sent
	}{
		{"the dialog, drawn late", yes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, true, true},
		{"the dialog, kind unset", yes, map[string]any{"pane": "w1:p1"}, nil, true, true},
		{"trust=false", yes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude", "trust": false}, true, true},
		{"not drawn yet", "", map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, false, false},
		{"another question", " Do you want to allow this?\n ❯ Yes\n", map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, false, false},
		{"the folder-trust dialog", trustDialog(claude.Trust.Answers[0]), map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, false, false},
		{"a kind with no dialog", yes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "grok"}, false, false},
		{"no pane", yes, map[string]any{"agent": "b1"}, map[string]any{"kind": "claude"}, false, false},
	}
	for _, c := range cases {
		h, calls := trustHerdr(t, no, yes, c.screen, "idle")
		if got := h.AcceptKey(context.Background(), c.handle, c.cfg); got != c.want {
			t.Errorf("%s: AcceptKey = %v, want %v\n%s", c.name, got, c.want, calls())
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

// The dispatcher's later passes answer an onboarding screen that drew only
// after the start looked, by the same rule as the start, and nothing else.
// `trust=false` leaves them answered, as it leaves the API-key dialog.
func TestAcceptOnboardingAnswersLateDialog(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	dark := claude.Onboarding[0].Answers[0]
	themeYes, security := themeDialog(dark), securityDialog()
	question := "Choose the text style that looks best with your terminal"
	wrappedTheme := strings.Replace(themeYes, question, "Choose the text style that looks best with\n your terminal", 1)
	wrappedOff := strings.Replace(themeDialog("Auto (match terminal)"), question, "Choose the text style that looks best with\n your terminal", 1)
	cases := []struct {
		name   string
		screen string
		handle map[string]any
		cfg    map[string]any
		entry  int
		want   bool
		keys   bool // a key may have been sent
	}{
		{"theme, drawn late", themeYes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 0, true, true},
		{"theme, question wrapped", wrappedTheme, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 0, true, true},
		{"theme, kind unset", themeYes, map[string]any{"pane": "w1:p1"}, nil, 0, true, true},
		{"security notes, drawn late", security, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 1, true, true},
		{"security notes, kind unset", security, map[string]any{"pane": "w1:p1"}, nil, 1, true, true},
		{"theme, trust=false", themeYes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude", "trust": false}, 0, true, true},
		{"security notes, trust=false", security, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude", "trust": false}, 1, true, true},
		{"not drawn yet", "", map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 0, false, false},
		{"another question", " Do you want to allow this?\n ❯ Yes\n", map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 0, false, false},
		{"the folder-trust dialog", trustDialog(claude.Trust.Answers[0]), map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 0, false, false},
		{"the folder-trust dialog, security entry", trustDialog(claude.Trust.Answers[0]), map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 1, false, false},
		{"theme asked of the security entry", themeYes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 1, false, false},
		{"security notes asked of the theme entry", security, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 0, false, false},
		{"entry out of range", themeYes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, 2, false, false},
		{"a kind with no screens", themeYes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "grok"}, 0, false, false},
		{"no pane", themeYes, map[string]any{"agent": "b1"}, map[string]any{"kind": "claude"}, 0, false, false},
	}
	for _, c := range cases {
		no, yes := themeDialog("Auto (match terminal)"), themeYes
		if c.screen == security {
			no, yes = security, security
		}
		if c.screen == wrappedTheme {
			no, yes = wrappedOff, wrappedTheme
		}
		h, calls := trustHerdr(t, no, yes, c.screen, "idle")
		if got := h.AcceptOnboarding(context.Background(), c.handle, c.cfg, c.entry); got != c.want {
			t.Errorf("%s: AcceptOnboarding = %v, want %v\n%s", c.name, got, c.want, calls())
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
	t.Parallel()
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

func TestKeyEntry(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	cases := []struct {
		cfg  map[string]any
		want *agents.Trust
	}{
		{nil, claude.ApiKey}, // kind defaults to claude
		{map[string]any{"kind": "claude"}, claude.ApiKey},
		{map[string]any{"kind": "claude", "trust": false}, claude.ApiKey},
		{map[string]any{"kind": "claude", "trust": true}, claude.ApiKey},
		{map[string]any{"kind": "kimi"}, nil},
		{map[string]any{"kind": "codex"}, nil},
		{map[string]any{"kind": "grok"}, nil},
	}
	for _, c := range cases {
		got := keyEntry(c.cfg)
		if got != c.want {
			t.Errorf("keyEntry(%v) = %+v, want %+v", c.cfg, got, c.want)
		}
	}
}

func TestOnboardingEntries(t *testing.T) {
	t.Parallel()
	claude, _ := agents.Lookup("claude")
	for _, c := range []struct {
		cfg  map[string]any
		want []agents.Trust
	}{
		{nil, claude.Onboarding}, // kind defaults to claude
		{map[string]any{"kind": "claude"}, claude.Onboarding},
		{map[string]any{"kind": "claude", "trust": false}, claude.Onboarding},
		{map[string]any{"kind": "claude", "trust": true}, claude.Onboarding},
		{map[string]any{"kind": "kimi"}, nil},
		{map[string]any{"kind": "codex"}, nil},
		{map[string]any{"kind": "grok"}, nil},
	} {
		got := onboardingEntries(c.cfg)
		if len(got) != len(c.want) {
			t.Errorf("onboardingEntries(%v) names %d screens, want %d", c.cfg, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i].Dialog != c.want[i].Dialog || got[i].Cursor != c.want[i].Cursor ||
				!slices.Equal(got[i].Answers, c.want[i].Answers) {
				t.Errorf("onboardingEntries(%v)[%d] = %+v, want %+v", c.cfg, i, got[i], c.want[i])
			}
		}
	}
}

// A Claude session started in its own home never records the trust answer:
// the accept is honoured for the session while the store keeps
// hasTrustDialogAccepted false, so every start draws the dialog again
// (probed live, yardr-88nb). yardr seeds the true itself before such a
// start; a true written beforehand is honoured and kept.
func TestSeedClaudeHomeTrust(t *testing.T) {
	t.Parallel()
	const dir = "/Users/ada"
	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), ".claude.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, c := range []struct {
		name                string
		body                string
		wantSeeded, wantErr bool
		wantUnchanged       bool
		wantFragment        string
	}{
		{"already true writes nothing",
			`{"numStartups": 3, "projects": {"/Users/ada": {"hasTrustDialogAccepted": true}}}`,
			false, false, true, ""},
		{"false becomes true",
			`{"numStartups": 3, "projects": {"/Users/ada": {"hasTrustDialogAccepted": false, "lastVersionBase": "2.1.288"}, "/Users/ada/work": {"hasTrustDialogAccepted": true}}}`,
			true, false, false, ""},
		{"a missing entry is created",
			`{"projects": {"/Users/ada/work": {"hasTrustDialogAccepted": true}}}`,
			true, false, false, ""},
		{"missing projects are created",
			`{}`,
			true, false, false, ""},
		{"broken json is an error",
			`{oops`,
			false, true, true, ""},
		{"trailing json is an error",
			`{"projects": {}} {}`,
			false, true, true, ""},
		{"projects of the wrong shape are an error",
			`{"projects": []}`,
			false, true, true, ""},
		{"an entry of the wrong shape is an error",
			`{"projects": {"/Users/ada": false}}`,
			false, true, true, ""},
		{"large unrelated numbers stay exact",
			`{"futureSetting": 9007199254740993, "projects": {"/Users/ada": {"hasTrustDialogAccepted": false}}}`,
			true, false, false, `"futureSetting": 9007199254740993`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path := write(t, c.body)
			seeded, err := seedClaudeHomeTrust(path, dir)
			if seeded != c.wantSeeded || (err != nil) != c.wantErr {
				t.Fatalf("seedClaudeHomeTrust() = %v, %v, want %v, err %v", seeded, err, c.wantSeeded, c.wantErr)
			}
			body, ferr := os.ReadFile(path)
			if ferr != nil {
				t.Fatal(ferr)
			}
			if c.wantUnchanged && string(body) != c.body {
				t.Fatalf("store rewritten, want it untouched:\n%s", body)
			}
			if c.wantFragment != "" && !strings.Contains(string(body), c.wantFragment) {
				t.Fatalf("store lost exact JSON value %s:\n%s", c.wantFragment, body)
			}
			if c.wantErr || c.wantUnchanged {
				return
			}
			var store struct {
				NumStartups int `json:"numStartups"`
				Projects    map[string]struct {
					Trust   bool   `json:"hasTrustDialogAccepted"`
					Version string `json:"lastVersionBase"`
				} `json:"projects"`
			}
			if err := json.Unmarshal(body, &store); err != nil {
				t.Fatalf("seeded store does not parse: %v", err)
			}
			if !store.Projects[dir].Trust {
				t.Errorf("the entry for %s is not trusted:\n%s", dir, body)
			}
			if strings.Contains(c.body, "numStartups") && store.NumStartups != 3 {
				t.Errorf("top-level keys not kept:\n%s", body)
			}
			if strings.Contains(c.body, "/Users/ada/work") && !store.Projects["/Users/ada/work"].Trust {
				t.Errorf("a sibling entry not kept:\n%s", body)
			}
			if strings.Contains(c.body, "lastVersionBase") && store.Projects[dir].Version != "2.1.288" {
				t.Errorf("the entry's other keys not kept:\n%s", body)
			}
		})
	}
	// A store that is not there is left alone: the harness writes it
	// itself, and the next start seeds that one.
	t.Run("a missing store is left alone", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), ".claude.json")
		if seeded, err := seedClaudeHomeTrust(path, dir); seeded || err != nil {
			t.Fatalf("seedClaudeHomeTrust() = %v, %v, want false, nil", seeded, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("store created: %v", err)
		}
	})
	// The seed keeps the file's mode.
	t.Run("the mode is kept", func(t *testing.T) {
		t.Parallel()
		path := write(t, `{"projects": {"/Users/ada": {"hasTrustDialogAccepted": false}}}`)
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if seeded, err := seedClaudeHomeTrust(path, dir); !seeded || err != nil {
			t.Fatalf("seedClaudeHomeTrust() = %v, %v, want true, nil", seeded, err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %v, %v, want 0640", info.Mode(), err)
		}
	})
}

// The seed goes only to a Claude session starting in its own home with
// automatic trust: anything else is left for the harness to record.
func TestMaybeSeedHomeTrust(t *testing.T) {
	// No t.Parallel: HOME is process-wide.
	home := t.TempDir()
	t.Setenv("HOME", home)
	key := realPath(home)
	claude := map[string]any{"kind": "claude"}
	write := func(dir, body string) string {
		t.Helper()
		path := filepath.Join(dir, ".claude.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	storeOf := func(path string) map[string]any {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var store map[string]any
		if err := json.Unmarshal(body, &store); err != nil {
			t.Fatalf("store does not parse: %v", err)
		}
		return store
	}
	trustOf := func(path, dir string) any {
		t.Helper()
		projects, _ := storeOf(path)["projects"].(map[string]any)
		entry, _ := projects[dir].(map[string]any)
		return entry["hasTrustDialogAccepted"]
	}
	h := &Herdr{}
	t.Run("seeds the home dir", func(t *testing.T) {
		path := write(home, `{"projects": {"`+key+`": {"hasTrustDialogAccepted": false}}}`)
		h.maybeSeedHomeTrust(claude, home, nil)
		if trustOf(path, key) != true {
			t.Fatalf("the home dir is not trusted: %v", storeOf(path))
		}
		// Twice is once: the second start finds the true and writes nothing.
		before, _ := os.ReadFile(path)
		h.maybeSeedHomeTrust(claude, home, nil)
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Fatalf("the store rewritten although trusted:\n%s", after)
		}
	})
	t.Run("kind defaults to claude", func(t *testing.T) {
		path := write(home, `{"projects": {"`+key+`": {"hasTrustDialogAccepted": false}}}`)
		h.maybeSeedHomeTrust(map[string]any{}, home, nil)
		if trustOf(path, key) != true {
			t.Fatalf("the home dir is not trusted: %v", storeOf(path))
		}
	})
	for _, c := range []struct {
		name string
		cfg  map[string]any
		env  []string
	}{
		{"another directory", claude, nil},
		{"another kind", map[string]any{"kind": "kimi"}, nil},
		{"trust=false", map[string]any{"kind": "claude", "trust": false}, nil},
		{"a redirected config", claude, []string{"CLAUDE_CONFIG_DIR=" + t.TempDir()}},
	} {
		t.Run(c.name+" is left alone", func(t *testing.T) {
			dir := home
			if c.name == "another directory" {
				dir = filepath.Join(home, "work")
			}
			body := `{"projects": {"` + key + `": {"hasTrustDialogAccepted": false}}}`
			path := write(home, body)
			h.maybeSeedHomeTrust(c.cfg, dir, c.env)
			if after, _ := os.ReadFile(path); string(after) != body {
				t.Fatalf("the store rewritten:\n%s", after)
			}
		})
	}
	t.Run("an empty redirect is no redirect", func(t *testing.T) {
		path := write(home, `{"projects": {"`+key+`": {"hasTrustDialogAccepted": false}}}`)
		h.maybeSeedHomeTrust(claude, home, []string{"CLAUDE_CONFIG_DIR="})
		if trustOf(path, key) != true {
			t.Fatalf("the home dir is not trusted: %v", storeOf(path))
		}
	})
	t.Run("the session HOME wins", func(t *testing.T) {
		other := t.TempDir()
		otherKey := realPath(other)
		otherStore := write(other, `{"projects": {"`+otherKey+`": {"hasTrustDialogAccepted": false}}}`)
		homeBody := `{"projects": {"` + key + `": {"hasTrustDialogAccepted": false}}}`
		homeStore := write(home, homeBody)
		h.maybeSeedHomeTrust(claude, other, []string{"HOME=" + other})
		if trustOf(otherStore, otherKey) != true {
			t.Fatalf("the session home is not trusted: %v", storeOf(otherStore))
		}
		if after, _ := os.ReadFile(homeStore); string(after) != homeBody {
			t.Fatalf("the wrong store rewritten:\n%s", after)
		}
	})
	t.Run("a missing store is fine", func(t *testing.T) {
		if err := os.Remove(filepath.Join(home, ".claude.json")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		h.maybeSeedHomeTrust(claude, home, nil) // must not fail the start
		if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
			t.Fatalf("store created: %v", err)
		}
	})
}

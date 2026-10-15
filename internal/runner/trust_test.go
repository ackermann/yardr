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
// `pane read --source visible` prints it, the cursor on the answer `on`.
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

 Quick safety check: Is this a project you created or one you trust?

 Security guide

` + mark("No, exit") + "\n" + mark(claude.Trust.Answer) + `

 Enter to confirm · Esc to cancel
`
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

` + mark(kimi.Trust.Answer) + `
     Enable project MCP servers. Remembered for this folder.

` + mark("Don't trust") + `
     Exit Kimi Code. Asked again next launch.
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
// pane; and (yardr-bgnn) every Kimi builder stopped at its own in its new
// worktree. yardr answers the dialog the kind's file names, moving the
// cursor onto the accepting answer first, and the agent gets its first
// prompt at once.
func TestStartAgentAcceptsFolderTrust(t *testing.T) {
	shortTrustWait(t)
	claude, _ := agents.Lookup("claude")
	kimi, _ := agents.Lookup("kimi")
	for _, c := range []struct {
		name       string
		entry      agents.Trust
		dialog     func(on string) string
		off, start string
		moves      bool // the cursor starts off the answer, so down is sent
	}{
		{"claude", *claude.Trust, trustDialog, "No, exit", "No, exit", true},
		{"kimi, cursor on the answer", *kimi.Trust, kimiTrustDialog, "Don't trust", kimi.Trust.Answer, false},
		{"kimi, cursor off the answer", *kimi.Trust, kimiTrustDialog, "Don't trust", "Don't trust", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, calls := trustHerdr(t, c.dialog(c.off), c.dialog(c.entry.Answer), c.dialog(c.start), "idle")
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
	h, calls := slowTrustHerdr(t, trustDialog("No, exit"), trustDialog(claude.Trust.Answer), trustDialog("No, exit"), "idle", "0.3")
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
	h, calls := trustHerdr(t, trustDialog("No, exit"), trustDialog(claude.Trust.Answer), trustDialog("No, exit"), "blocked")
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
	stuck := strings.ReplaceAll(trustDialog("No, exit"), "❯ No, exit", "> No, exit")
	cases := []struct {
		name, screen string
		trust        *agents.Trust
		keys         string // the last key that may have been sent, "" for none
	}{
		{"another question", " Do you want to allow this?\n ❯ Yes\n   No\n", claude.Trust, ""},
		{"trust=false", trustDialog("No, exit"), nil, ""},
		{"the answer's words inside other text", "I typed: Yes, I trust this folder, honest\n", claude.Trust, ""},
		{"the answer at the end of a decorated line", "Quick safety check: " + claude.Trust.Dialog + "\n❯ Please choose " + claude.Trust.Answer + "\n", claude.Trust, ""},
		{"the answer without the question", "❯ Yes, I trust this folder\n", claude.Trust, ""},
		{"kimi's answer without its title", "I typed: Trust this folder\n", kimi.Trust, ""},
		{"kimi's title without its answer", "Trust this folder?\n", kimi.Trust, ""},
		{"cursor never reaches the answer", stuck, claude.Trust, "down"},
	}
	for _, c := range cases {
		h, calls := trustHerdr(t, trustDialog("No, exit"), trustDialog(claude.Trust.Answer), c.screen, "idle")
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

// The dispatcher's later passes answer a trust dialog that drew only after
// the start looked (yardr-v3mp): AcceptTrust answers the dialog of the
// config's kind by the same rule as the start, and nothing else.
func TestAcceptTrustAnswersLateDialog(t *testing.T) {
	shortTrustWait(t)
	claude, _ := agents.Lookup("claude")
	yes := trustDialog(claude.Trust.Answer)
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
		{"not drawn yet", "", map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, false, false},
		{"another question", " Do you want to allow this?\n ❯ Yes\n", map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude"}, false, false},
		{"trust=false", yes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "claude", "trust": false}, false, false},
		{"a kind with no dialog", yes, map[string]any{"pane": "w1:p1"}, map[string]any{"kind": "codex"}, false, false},
		{"no pane", yes, map[string]any{"agent": "b1"}, map[string]any{"kind": "claude"}, false, false},
	}
	for _, c := range cases {
		h, calls := trustHerdr(t, trustDialog("No, exit"), yes, c.screen, "idle")
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
		{map[string]any{"kind": "codex"}, nil},
	}
	for _, c := range cases {
		got := trustEntry(c.cfg)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("trustEntry(%v) = %+v, want %+v", c.cfg, got, c.want)
		}
	}
}

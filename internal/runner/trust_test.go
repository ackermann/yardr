package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
)

// trustDialog is Claude Code 2.1.288's folder-trust dialog as herdr's
// `pane read --source visible` prints it, the cursor on the answer `on`.
func trustDialog(on string) string {
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

` + mark("No, exit") + "\n" + mark(trustYes) + `

 Enter to confirm · Esc to cancel
`
}

// trustHerdr is a fake herdr whose agent stops at the folder-trust dialog:
// agent start says agent_not_ready, the pane shows the dialog with its
// cursor on "No, exit", which `down` moves to the other answer and back as
// the real one wraps, redrawing after `redraw` seconds. `after` is the
// agent's status once Enter is sent. Every call is logged.
func trustHerdr(t *testing.T, screen, after string) (h *Herdr, calls func() string) {
	t.Helper()
	return slowTrustHerdr(t, screen, after, "0")
}

func slowTrustHerdr(t *testing.T, screen, after, redraw string) (h *Herdr, calls func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	for name, text := range map[string]string{"no": trustDialog("No, exit"), "yes": trustDialog(trustYes), "screen": screen} {
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
// pane. yardr answers it, moving the cursor off "No, exit" first, and the
// agent gets its first prompt at once.
func TestStartAgentAcceptsFolderTrust(t *testing.T) {
	shortTrustWait(t)
	h, calls := trustHerdr(t, trustDialog("No, exit"), "idle")
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if handle["trust_accepted"] != true || handle[dispatch.PendingPrompt] != nil {
		t.Fatalf("handle = %v\n%s", handle, calls())
	}
	got := calls()
	down, enter, prompt := strings.Index(got, "pane send-keys w1:p1 down"), strings.Index(got, "pane send-keys w1:p1 enter"),
		strings.Index(got, "agent prompt b1 read the brief")
	if down < 0 || enter < down || prompt < enter {
		t.Fatalf("want down, enter, then the prompt:\n%s", got)
	}
}

// Found live: the dialog redraws a moment after a key, and a second `down`
// sent before it did wrapped the cursor back to "No, exit", so the dialog
// was never answered. One `down` per redraw.
func TestStartAgentTrustWaitsForRedraw(t *testing.T) {
	old := trustWait
	trustWait = 5 * time.Second
	t.Cleanup(func() { trustWait = old })
	h, calls := slowTrustHerdr(t, trustDialog("No, exit"), "idle", "0.3")
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
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
	h, calls := trustHerdr(t, trustDialog("No, exit"), "blocked")
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if handle["trust_accepted"] != true || handle[dispatch.PendingPrompt] != "read the brief" {
		t.Fatalf("handle = %v", handle)
	}
	if strings.Contains(calls(), "agent prompt") {
		t.Fatalf("prompted an agent that was not ready:\n%s", calls())
	}
}

// Only the trust dialog is answered, and only when asked to: any other
// question, a group with trust=false and a dialog whose cursor cannot be
// put on the accepting answer are left for a human, no key sent that could
// answer something else.
func TestStartAgentLeavesOtherQuestions(t *testing.T) {
	shortTrustWait(t)
	stuck := strings.ReplaceAll(trustDialog("No, exit"), "❯ No, exit", "> No, exit")
	cases := []struct {
		name, screen string
		trust        bool
		keys         string // the last key that may have been sent, "" for none
	}{
		{"another question", " Do you want to allow this?\n ❯ Yes\n   No\n", true, ""},
		{"trust=false", trustDialog("No, exit"), false, ""},
		{"the answer's words inside other text", "I typed: Yes, I trust this folder, honest\n", true, ""},
		{"cursor never reaches the answer", stuck, true, "down"},
	}
	for _, c := range cases {
		h, calls := trustHerdr(t, c.screen, "idle")
		handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
			map[string]any{"agent": "b1", "pane": "w1:p1"}, c.trust)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := calls()
		if handle[dispatch.PendingPrompt] != "read the brief" || handle["trust_accepted"] != nil ||
			strings.Contains(got, "enter") || strings.Contains(got, "agent prompt") ||
			(c.keys == "" && strings.Contains(got, "send-keys")) {
			t.Errorf("%s: handle = %v\n%s", c.name, handle, got)
		}
	}
}

func TestTrusts(t *testing.T) {
	cases := []struct {
		cfg  map[string]any
		want bool
	}{
		{nil, true}, // kind defaults to claude
		{map[string]any{"kind": "claude"}, true},
		{map[string]any{"kind": "claude", "trust": false}, false},
		{map[string]any{"kind": "claude", "trust": true}, true},
		{map[string]any{"kind": "codex"}, false},
	}
	for _, c := range cases {
		if got := trusts(c.cfg); got != c.want {
			t.Errorf("trusts(%v) = %v, want %v", c.cfg, got, c.want)
		}
	}
}

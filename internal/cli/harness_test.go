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
	wantText(t, out, "KIND", "claude    Claude Code  v1    pane marks, error lines, reported model, trust dialog, unattended flags,",
		"codex     Codex        v1    pane marks, error lines, reported model, unattended flags, model flag, flags always passed,",
		"pi        pi           v1    model flag, version flag, one-shot call\n",
		"No file, so started as herdr starts it and nothing more known: gemini, cursor,")
	var listed struct {
		Harnesses []agents.Harness `json:"harnesses"`
	}
	if err := json.Unmarshal([]byte(ask(0, "harness", "list", "--json")), &listed); err != nil || len(listed.Harnesses) != len(agents.Harnesses) {
		t.Fatalf("list --json: %v, %d harnesses", err, len(listed.Harnesses))
	}

	out = ask(0, "harness", "show", "codex")
	wantText(t, out, "codex  Codex  (harnesses/codex.yaml, version 1)", "get it: npm install -g @openai/codex",
		"trust: yardr answers no dialog of it", "input begins with:    ›", "a message it took:    ›",
		"unattended: --dangerously-bypass-approvals-and-sandbox", "always:     --no-daemon",
		"model flag: --model",
		"one shot:   exec --skip-git-repo-check <prompt>", `permission "approve for me": --approve-for-me  (automatic review`,
		"~/.codex (or $CODEX_HOME)/sessions/*/*/*/rollout-*-{session}.jsonl: payload.model of the first line of type turn_context",
		"at_capacity    ^(Selected model is at capacity|Flex capacity unavailable)")
	wantText(t, ask(0, "harness", "show", "kimi"), "after the box's edge: │", "a message it took:    ✨",
		"no source known: nothing is recorded and nothing compared")
	wantText(t, ask(0, "harness", "show", "claude"), "trust: yardr answers its folder-trust dialog")
	var shown agents.Harness
	if err := json.Unmarshal([]byte(ask(0, "harness", "show", "kimi", "--json")), &shown); err != nil ||
		shown.Kind != "kimi" || shown.Marks.Edge != "│" || len(shown.Errors) != 4 || shown.Errors[0].Reason != agents.ReasonRateLimited {
		t.Fatalf("show kimi --json: %v, %+v", err, shown)
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

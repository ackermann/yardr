package runner

import (
	"context"
	"strings"
	"testing"
)

// Claude Code's login screens as 2.1.288 drew them with no credentials (12
// Oct 2026, throwaway HOME, past the theme picker): first the menu of login
// methods, then, once one is picked, the browser sign-in with a code to
// paste. Transcribed from the terminal's stream into lines, as `herdr pane
// read` shows them; the sign-in URL's query is redacted to its shape.
//
// The menu's selected option carries the mark of the input, so without the
// login lines the screen reads as text in the input (see below), and the
// browser screen, which has no mark at all, as a harness still starting.
const (
	claudeLoginMenu = `Claude Code can be used with your Claude subscription or billed based on API usage through your Console account.
Select login method:
❯ 1. Claude account with subscription · Pro, Max, Team, or Enterprise
  2. Anthropic Console account · API usage billing
  3. 3rd-party platform · Amazon Bedrock, Microsoft Foundry, Google Vertex AI
`
	claudeLoginBrowser = `✳ Opening browser to sign in…
✢
Browser didn't open? Use the url below to sign in (c to copy)

https://claude.com/cai/oauth/authorize?…
Paste code here if prompted>
`
)

func TestLoginLine(t *testing.T) {
	below := strings.Repeat("⏺ talked about the brief\n", 25)
	for _, c := range []struct {
		name, kind, pane, want string
	}{
		{"the menu of methods", "claude", claudeLoginMenu, "Select login method:"},
		{"the browser sign-in", "claude", claudeLoginBrowser, "Paste code here if prompted>"},
		// Above the theme screen it waited at first: the login below it counts.
		{"theme screen above, menu below", "claude", "Choose the text style that looks best with your terminal\n❯ Dark mode\n" + claudeLoginMenu, "Select login method:"},
		{"an empty input", "claude", "────\n❯ \n────\n  ⏵⏵ auto mode on", ""},
		{"an error it stopped on", "claude", "⏺ API Error: 401\n\n❯ \n", ""},
		// The same words further up are what the agent printed while it
		// still worked, not the screen it sits at now.
		{"login words above the pane's end", "claude", "Select login method:\n" + below + "────\n❯ \n", ""},
		{"another kind's pane", "kimi", claudeLoginMenu, ""},
		{"a kind with no file", "acme", claudeLoginMenu, ""},
		{"no kind", "", claudeLoginMenu, ""},
	} {
		if got := loginLine(c.pane, c.kind); got != c.want {
			t.Errorf("%s: loginLine = %q, want %q", c.name, got, c.want)
		}
	}
	// Why the menu misreads without the login lines: its selected option
	// is text in the input, and the pane reads as the harness's own
	// screen, so the first prompt is typed into the menu.
	if text, known := inputText(claudeLoginMenu, "claude"); !known || !strings.Contains(text, "1. Claude account with subscription") {
		t.Errorf("the menu's option reads as input %q, known %v", text, known)
	}
}

func TestInputLogin(t *testing.T) {
	h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "pane read") printf '%s\n' 'Select login method:' '❯ 1. Claude account' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	handle := map[string]any{"agent": "b1", "pane": "w1:p1", harnessKind: "claude"}
	if line, err := h.InputLogin(context.Background(), handle); err != nil || line != "Select login method:" {
		t.Errorf("InputLogin = %q, %v; want the login line", line, err)
	}
	// A kind with no login screen in its file is answered without
	// reading: the pane read fails, and nothing is known.
	failing := &Herdr{Bin: deafHerdrBin(t, `echo '{"error":{"code":"timeout","message":"no answer"}}' >&2; exit 1`)}
	if line, err := failing.InputLogin(context.Background(), map[string]any{"agent": "b1", "pane": "w1:p1", harnessKind: "kimi"}); err != nil || line != "" {
		t.Errorf("a kind with no login screen: InputLogin = %q, %v", line, err)
	}
	if line, err := (&Herdr{}).InputLogin(context.Background(), map[string]any{harnessKind: "claude"}); err == nil || line != "" {
		t.Errorf("a handle without a pane: InputLogin = %q, %v", line, err)
	}
}

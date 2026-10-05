package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// Screens as `herdr pane read --format ansi` gave them (herdr 0.9.0): the
// input of Claude Code 2.1.288 between its two rules, with a no-break space
// after the mark, and Codex's above an empty line and its footer. Each run
// of styled text begins with ESC[0m; lines end in CR LF.
const (
	rule         = "\x1b[0m\x1b[38;5;7m────────────────\x1b[0m\r\n"
	claudeFooter = "  \x1b[0m\x1b[38;5;11m⏵⏵ auto mode on\x1b[0m\x1b[38;5;7m (shift+tab to cycle)\x1b[0m\r\n"
	codexFooter  = " \r\n  \x1b[0m\x1b[38;2;246;226;183mGPT-6-Astra low\x1b[0m · \x1b[0m\x1b[38;2;171;223;167m~/work\x1b[0m\r\n" +
		"  \x1b[0m\x1b[1m?\x1b[0m for shortcuts\r\n"

	claudeEmpty       = rule + "❯ \r\n" + rule + claudeFooter
	claudeDraft       = rule + "❯ some creative..\r\n" + rule + claudeFooter
	claudePlaceholder = rule + "❯ \x1b[0m\x1b[2mTry \"fix typecheck errors\"\x1b[0m\r\n" + rule + claudeFooter
	codexEmpty        = "\x1b[0m\x1b[1m›\x1b[0m \r\n" + codexFooter
	codexDraft        = "\x1b[0m\x1b[1m›\x1b[0m half a thought\r\n" + codexFooter
	codexPlaceholder  = "\x1b[0m\x1b[1m›\x1b[0m \x1b[0m\x1b[2mAsk Codex to do anything\x1b[0m\r\n" + codexFooter
	// A harness with a mark yardr does not know: its input cannot be told.
	noMark = "> typed here, or printed: who knows\r\n"
)

func TestInputText(t *testing.T) {
	taken := "\x1b[0m\x1b[38;5;7m❯ \x1b[0mearlier message, sent\r\n\r\n⏺ An answer.\r\n\r\n"
	for name, c := range map[string]struct {
		pane, text string
		known      bool
	}{
		"claude empty":       {claudeEmpty, "", true},
		"claude draft":       {claudeDraft, "some creative..", true},
		"claude placeholder": {claudePlaceholder, "", true},
		"codex empty":        {codexEmpty, "", true},
		"codex draft":        {codexDraft, "half a thought", true},
		"codex placeholder":  {codexPlaceholder, "", true},
		"no mark":            {noMark, "", false},
		// The input is what follows the last mark: a message the agent took
		// begins with the mark too, further up.
		"sent message above an empty input": {taken + claudeEmpty, "", true},
		"sent message above a draft":        {taken + claudeDraft, "some creative..", true},
		// A draft of several lines, wrapped by the harness or with a
		// person's own line breaks, down to the rule.
		"draft of two lines": {rule + "❯ first line\r\n  second line\r\n" + rule + claudeFooter, "first line\nsecond line", true},
		"draft begun with an empty first line": {
			rule + "❯ \r\n  second line\r\n" + rule + claudeFooter, "second line", true},
		// A person who types a placeholder's words has typed text: the
		// attribute is asked, not the wording.
		"placeholder's words, typed": {"\x1b[0m\x1b[1m›\x1b[0m Ask Codex to do anything\r\n" + codexFooter, "Ask Codex to do anything", true},
		// A 2 among a colour's numbers is not the faint attribute.
		"coloured text, rgb":     {"› \x1b[0m\x1b[38;2;2;2;2mtyped\x1b[0m\r\n" + codexFooter, "typed", true},
		"coloured text, indexed": {"› \x1b[0m\x1b[38;5;2mtyped\x1b[0m\r\n" + codexFooter, "typed", true},
		"faint ended by 22":      {"› \x1b[2mTry this\x1b[22m typed\r\n" + codexFooter, "typed", true},
		"faint and bold at once": {"› \x1b[1;2mTry this\x1b[m\r\n" + codexFooter, "", true},
	} {
		text, known := inputText(c.pane)
		if text != c.text || known != c.known {
			t.Errorf("%s: inputText = %q, %v; want %q, %v", name, text, known, c.text, c.known)
		}
	}
}

// inputHerdr is a fake herdr whose agent is idle and takes every prompt it
// is sent: a prompt shows in the pane as a sent message, begun with mark as
// the harness begins one. Below what it took the pane shows the screen the
// returned function sets, the agent's input as herdr reads it. Every call
// is logged.
func inputHerdr(t *testing.T, mark string) (h *Herdr, calls func() string, screen func(string)) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	screen = func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "screen"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	screen(claudeEmpty)
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
  "agent prompt") echo "`+mark+` $4" >> taken; echo '{"result":{"type":"ok"}}' ;;
  "pane read") cat taken 2>/dev/null; cat screen ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }, screen
}

// Found live (yardr-ps05): a person was typing in the mayor's pane when the
// yard nudged the mayor, and their half sentence went out with the nudge as
// one message. With text in the input no key is sent and the nudge stays
// due; when the input is empty it is typed, once. A placeholder is not
// text, and a harness whose input cannot be told is nudged as before.
func TestNudgeIsNotTypedIntoAnInputThatHoldsText(t *testing.T) {
	for name, c := range map[string]struct {
		mark, screen string
		held         bool
	}{
		"claude draft":       {"❯", claudeDraft, true},
		"codex draft":        {"›", codexDraft, true},
		"claude placeholder": {"❯", claudePlaceholder, false},
		"codex placeholder":  {"›", codexPlaceholder, false},
		"codex empty":        {"›", codexEmpty, false},
		"no mark":            {">", noMark, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, calls, screen := inputHerdr(t, c.mark)
			ctx := context.Background()
			s := storetest.New(t)
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
			if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			pass := func() {
				t.Helper()
				for range 3 {
					if err := d.Reconcile(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			pass()
			if r, err := s.GetResident(ctx, "mayor"); err != nil || !r.Live() || r.Handle[dispatch.PendingPrompt] != nil {
				t.Fatalf("resident = %+v %v", r, err)
			}
			typed := func() int { return strings.Count(calls(), "agent prompt") - 1 } // less its first prompt
			keys := func() int { return strings.Count(calls(), "send-") }

			screen(c.screen)
			if err := s.Mail(ctx, "mayor", "deploy freeze", "chris"); err != nil {
				t.Fatal(err)
			}
			pass()
			if !c.held {
				if typed() != 1 || !strings.Contains(calls(), "yardr: 1 new in your inbox") {
					t.Fatalf("nudge typed %d times, want once:\n%s", typed(), calls())
				}
				return
			}
			if typed() != 0 || keys() != 0 {
				t.Fatalf("keys sent to an input that holds text:\n%s", calls())
			}
			screen(claudeEmpty)
			pass()
			if typed() != 1 || keys() != 0 || !strings.Contains(calls(), "yardr: 1 new in your inbox") {
				t.Fatalf("after the input emptied the nudge was typed %d times, want once:\n%s", typed(), calls())
			}
		})
	}
}

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
// returned function sets, the agent's input as herdr reads it, which is
// empty at first: with its styling when that is asked for (--format ansi),
// and as herdr reads it otherwise, without. Every call is logged.
func inputHerdr(t *testing.T, mark, empty string) (h *Herdr, calls func() string, screen func(string)) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	screen = func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "screen"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	screen(empty)
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
  "agent prompt") echo "`+mark+` $4" >> taken; echo '{"result":{"type":"ok"}}' ;;
  "pane read") cat taken 2>/dev/null
    case "$*" in
      *ansi*) cat screen ;;
      *) sed "s/$(printf '\033')\[[0-9;]*m//g" screen ;;
    esac ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }, screen
}

// Found live (yardr-ps05): a person was typing in the yardmaster's pane when
// the yard nudged the yardmaster, and their half sentence went out with the
// nudge as one message. With text in the input no key is sent and the nudge
// stays due; when the input is empty it is typed, once. A placeholder is not
// text, and a harness whose input cannot be told is nudged as before. Each
// crew member is of the kind whose pane it shows: its first prompt is told
// as taken by that kind's mark (see promptPlace).
func TestNudgeIsNotTypedIntoAnInputThatHoldsText(t *testing.T) {
	for name, c := range map[string]struct {
		kind, mark, empty, screen string
		held                      bool
	}{
		"claude draft":       {"claude", "❯", claudeEmpty, claudeDraft, true},
		"codex draft":        {"codex", "›", codexEmpty, codexDraft, true},
		"claude placeholder": {"claude", "❯", claudeEmpty, claudePlaceholder, false},
		"codex placeholder":  {"codex", "›", codexEmpty, codexPlaceholder, false},
		"codex empty":        {"codex", "›", codexEmpty, codexEmpty, false},
		"no mark":            {"kimi", ">", noMark, noMark, false},
	} {
		t.Run(name, func(t *testing.T) {
			h, calls, screen := inputHerdr(t, c.mark, c.empty)
			ctx := context.Background()
			s := storetest.New(t)
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
			if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir(),
				Config: map[string]any{"kind": c.kind}}, "me"); err != nil {
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
			if r, err := s.GetCrew(ctx, "yardmaster"); err != nil || !r.Live() || r.Handle[dispatch.PendingPrompt] != nil {
				t.Fatalf("crew member = %+v %v", r, err)
			}
			typed := func() int { return strings.Count(calls(), "agent prompt") - 1 } // less its first prompt
			keys := func() int { return strings.Count(calls(), "send-") }

			screen(c.screen)
			if err := s.Mail(ctx, "yardmaster", "deploy freeze", "chris"); err != nil {
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
			screen(c.empty)
			pass()
			if typed() != 1 || keys() != 0 || !strings.Contains(calls(), "yardr: 1 new in your inbox") {
				t.Fatalf("after the input emptied the nudge was typed %d times, want once:\n%s", typed(), calls())
			}
		})
	}
}

// Found live (yardr-7y16, 7 Oct): a crew member's first prompt sat typed in
// its input with "shown, look 1" on the handle and nothing pending, and the
// yardmaster was idle until a person pressed Enter. With the agent idle and
// its input holding the first prompt, wrapped as the harness wraps it, the
// dispatcher sends Enter and says so on the journal; with the input empty
// again, nothing more. The pane of a kind whose mark yardr does not know
// gets no key, whatever its last marked line holds, nor does a person's
// text after the prompt.
func TestOwnTextLeftInTheInputOfAnIdleAgentIsSubmitted(t *testing.T) {
	for name, c := range map[string]struct {
		kind, mark, empty string
		after             string // typed after the prompt, by a person
		enters            int
	}{
		"claude":                 {"claude", "❯", claudeEmpty, "", 1},
		"codex":                  {"codex", "›", codexEmpty, "", 1},
		"unknown kind":           {"kimi", "❯", claudeEmpty, "", 0},
		"a person's words after": {"claude", "❯", claudeEmpty, " and one more thing", 0},
	} {
		t.Run(name, func(t *testing.T) {
			h, calls, screen := inputHerdr(t, c.mark, c.empty)
			ctx := context.Background()
			s := storetest.New(t)
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
			if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir(),
				Config: map[string]any{"kind": c.kind}}, "me"); err != nil {
				t.Fatal(err)
			}
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			r, err := s.GetCrew(ctx, "yardmaster")
			if err != nil || !r.Live() || r.Handle[dispatch.PendingPrompt] != nil {
				t.Fatalf("crew member = %+v %v", r, err)
			}
			prompt := h.FirstPrompt(r.Handle)
			if !strings.HasPrefix(prompt, CrewPromptLead) {
				t.Fatalf("the handle does not say what the crew member was first told: %v", r.Handle)
			}
			pane, _ := r.Handle["pane"].(string)
			enters := func() int { return strings.Count(calls(), "pane send-keys "+pane+" enter") }

			half := len(prompt) / 2
			input := c.mark + " " + prompt[:half] + "\r\n  " + prompt[half:] + c.after + "\r\n"
			if c.kind == "codex" {
				screen(input + codexFooter)
			} else {
				screen(rule + input + rule + claudeFooter)
			}
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if enters() != c.enters || strings.Count(calls(), "agent prompt") != 1 {
				t.Fatalf("Enter sent %d times, want %d, and the prompt typed once:\n%s", enters(), c.enters, calls())
			}
			evs, err := s.Events(ctx, r.Journal, 200)
			if err != nil {
				t.Fatal(err)
			}
			typed := 0
			for _, e := range evs {
				if e.Kind == "session_prompt_typed" {
					typed++
					if e.Data["text"] != "the first prompt" || e.Data["crew"] != "yardmaster" || e.Data["pane"] != pane {
						t.Fatalf("session_prompt_typed = %+v", e.Data)
					}
				}
			}
			if typed != c.enters {
				t.Fatalf("session_prompt_typed logged %d times, want %d", typed, c.enters)
			}
			screen(c.empty)
			for range 3 {
				if err := d.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if enters() != c.enters {
				t.Fatalf("Enter sent to an empty input: %d times in all\n%s", enters(), calls())
			}
		})
	}
}

// A handle from before the kind was written is of no kind yardr knows.
func TestInputMarked(t *testing.T) {
	h := &Herdr{}
	for kind, want := range map[string]bool{"claude": true, "codex": true, "kimi": false, "": false} {
		handle := map[string]any{}
		if kind != "" {
			handle[harnessKind] = kind
		}
		if got := h.InputMarked(handle); got != want {
			t.Errorf("InputMarked(kind %q) = %v, want %v", kind, got, want)
		}
	}
}

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
//
// Kimi Code 2.1.1's is the brakeman's pane of 7 Oct 2026, narrowed: the
// input in its box, the cursor a space in reverse video, and the message it
// took above. The box that holds a draft is as Kimi drew one in a terminal
// of its own on the same day, its second line as it wraps a long one.
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

	kimiEdge   = "\x1b[0m\x1b[38;2;90;90;90m│\x1b[0m"
	kimiCursor = "\x1b[0m\x1b[7m \x1b[0m"
	kimiTop    = " \x1b[0m\x1b[38;2;90;90;90m╭──────────────────────────╮\x1b[0m\r\n"
	kimiFooter = " \x1b[0m\x1b[38;2;90;90;90m╰──────────────────────────╯\x1b[0m\r\n" +
		" \x1b[0m\x1b[1m\x1b[38;2;232;168;56mNever Ask\x1b[0m  \x1b[0m\x1b[38;2;224;224;224mK3 thinking: high\x1b[0m  \x1b[0m\x1b[38;2;136;136;136m~\x1b[0m\r\n" +
		"         \x1b[0m\x1b[38;2;224;224;224mcontext: 3% (22.2k/1M)\x1b[0m\r\n"
	kimiEmpty = kimiTop + " " + kimiEdge + " > " + kimiCursor + "                       " + kimiEdge + "\r\n" + kimiFooter
	kimiDraft = kimiTop + " " + kimiEdge + " > half a thought that is  " + kimiEdge + "\r\n" +
		" " + kimiEdge + "   long enough to wrap" + kimiCursor + "   " + kimiEdge + "\r\n" + kimiFooter
	kimiTaken = " \x1b[0m\x1b[1m\x1b[38;2;255;203;107m✨ earlier message,        \x1b[0m\r\n" +
		"    \x1b[0m\x1b[1m\x1b[38;2;255;203;107msent\x1b[0m                     \r\n \r\n" +
		" \x1b[0m\x1b[38;2;136;136;136m● \x1b[0m\x1b[3m\x1b[38;2;136;136;136mAn answer.\x1b[0m\r\n \r\n"
)

// inputScreen is the input of a harness of kind as its pane shows it, with
// lines typed there: Kimi's in its box, Codex's above its footer, and
// Claude Code's between its rules, which is also what a kind yardr knows no
// marks for is shown with.
func inputScreen(kind string, lines ...string) string {
	switch kind {
	case "kimi":
		s := kimiTop
		for i, l := range lines {
			s += " " + kimiEdge + map[bool]string{true: " > ", false: "   "}[i == 0] + l + " " + kimiEdge + "\r\n"
		}
		return s + kimiFooter
	case "codex":
		return "› " + strings.Join(lines, "\r\n  ") + "\r\n" + codexFooter
	}
	return rule + "❯ " + strings.Join(lines, "\r\n  ") + "\r\n" + rule + claudeFooter
}

func TestInputText(t *testing.T) {
	taken := "\x1b[0m\x1b[38;5;7m❯ \x1b[0mearlier message, sent\r\n\r\n⏺ An answer.\r\n\r\n"
	for name, c := range map[string]struct {
		kind, pane, text string
		known            bool
	}{
		"claude empty":       {"claude", claudeEmpty, "", true},
		"claude draft":       {"claude", claudeDraft, "some creative..", true},
		"claude placeholder": {"claude", claudePlaceholder, "", true},
		"codex empty":        {"codex", codexEmpty, "", true},
		"codex draft":        {"codex", codexDraft, "half a thought", true},
		"codex placeholder":  {"codex", codexPlaceholder, "", true},
		"no mark":            {"acme", noMark, "", false},
		// The input is what follows the last mark: a message the agent took
		// begins with the mark too, further up.
		"sent message above an empty input": {"claude", taken + claudeEmpty, "", true},
		"sent message above a draft":        {"claude", taken + claudeDraft, "some creative..", true},
		// A draft of several lines, wrapped by the harness or with a
		// person's own line breaks, down to the rule.
		"draft of two lines": {"claude", rule + "❯ first line\r\n  second line\r\n" + rule + claudeFooter, "first line\nsecond line", true},
		"draft begun with an empty first line": {
			"claude", rule + "❯ \r\n  second line\r\n" + rule + claudeFooter, "second line", true},
		// A person who types a placeholder's words has typed text: the
		// attribute is asked, not the wording.
		"placeholder's words, typed": {"codex", "\x1b[0m\x1b[1m›\x1b[0m Ask Codex to do anything\r\n" + codexFooter, "Ask Codex to do anything", true},
		// A 2 among a colour's numbers is not the faint attribute.
		"coloured text, rgb":     {"codex", "› \x1b[0m\x1b[38;2;2;2;2mtyped\x1b[0m\r\n" + codexFooter, "typed", true},
		"coloured text, indexed": {"codex", "› \x1b[0m\x1b[38;5;2mtyped\x1b[0m\r\n" + codexFooter, "typed", true},
		"faint ended by 22":      {"codex", "› \x1b[2mTry this\x1b[22m typed\r\n" + codexFooter, "typed", true},
		"faint and bold at once": {"codex", "› \x1b[1;2mTry this\x1b[m\r\n" + codexFooter, "", true},
		// Kimi's input is in a box, whose edges are no text; its mark comes
		// after the left one, and a message it took has another.
		"kimi empty": {"kimi", kimiEmpty, "", true},
		"kimi draft": {"kimi", kimiDraft, "half a thought that is\nlong enough to wrap", true},
		"kimi, sent message above an empty input": {"kimi", kimiTaken + kimiEmpty, "", true},
		"kimi, sent message above a draft":        {"kimi", kimiTaken + kimiDraft, "half a thought that is\nlong enough to wrap", true},
		"kimi, a draft that ends in an edge":      {"kimi", inputScreen("kimi", "a │ b │"), "a │ b │", true},
		// Kimi's box is no other kind's input, and a line that begins with
		// Kimi's mark and no edge is not Kimi's.
		"kimi's box, another kind": {"acme", kimiDraft, "", false},
		"kimi, no box drawn":       {"kimi", noMark, "", false},
		// Without its own mark a pane is read by the others': enough to
		// type nothing after it.
		"kimi, a line with Claude Code's mark": {"kimi", claudeDraft, "some creative..", true},
		"claude, a line with Codex's mark":     {"claude", codexDraft, "half a thought", true},
		"no kind, Claude Code's draft":         {"", claudeDraft, "some creative..", true},
		// Its own mark tells before another's further down.
		"claude, Codex's mark below its input": {"claude", claudeDraft + "› printed\r\n", "some creative..", true},
	} {
		text, known := inputText(c.pane, c.kind)
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
		"kimi draft":         {"kimi", "✨", kimiEmpty, kimiDraft, true},
		"kimi empty":         {"kimi", "✨", kimiEmpty, kimiEmpty, false},
		"no mark":            {"acme", ">", noMark, noMark, false},
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
//
// The brakeman's Kimi Code was such a kind until yardr-8twv; its input is
// read in its box now, and its first prompt left there is submitted.
func TestOwnTextLeftInTheInputOfAnIdleAgentIsSubmitted(t *testing.T) {
	for name, c := range map[string]struct {
		kind, mark, empty string
		after             string // typed after the prompt, by a person
		enters            int
	}{
		"claude":                       {"claude", "❯", claudeEmpty, "", 1},
		"codex":                        {"codex", "›", codexEmpty, "", 1},
		"kimi":                         {"kimi", "✨", kimiEmpty, "", 1},
		"unknown kind":                 {"acme", "❯", claudeEmpty, "", 0},
		"a person's words after":       {"claude", "❯", claudeEmpty, " and one more thing", 0},
		"kimi, a person's words after": {"kimi", "✨", kimiEmpty, " and one more thing", 0},
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
			screen(inputScreen(c.kind, prompt[:half], prompt[half:]+c.after))
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
	for kind, want := range map[string]bool{"claude": true, "codex": true, "kimi": true, "acme": false, "": false} {
		handle := map[string]any{}
		if kind != "" {
			handle[harnessKind] = kind
		}
		if got := h.InputMarked(handle); got != want {
			t.Errorf("InputMarked(kind %q) = %v, want %v", kind, got, want)
		}
	}
}

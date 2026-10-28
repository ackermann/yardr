package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

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
//
// Muse Code 1.4.9's is a pty read of 15 Oct 2026, narrowed: the input one
// line under a voice-input divider and above a rule and the footer, the
// mark coloured and no placeholder drawn, a message it took above with
// the same mark, and the line it works behind begun with ◆.
//
// Muse Code 1.4.11's hint is a herdr pane read --format ansi of 21 Oct
// 2026, narrowed: the input line carries the amber mark and, after it,
// the hint in a truecolor grey (38;2;103;108;116), not faint, which comes
// and goes. A word typed in a scratch pane the same day is drawn in
// 38;2;204;211;219, not the hint's grey.
//
// Kiro CLI 2.25's is the idle pane of awslabs/cli-agent-orchestrator's TUI
// fixtures, narrowed: the input under the status line, begun with ›, an
// empty one showing the placeholder in gray (not faint) with ↵ after it
// and the copy hint under it, a message it took above as a plain indented
// paragraph, and the line it works behind begun with › too.
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

	museHead   = "  Muse Code 1.4.9\r\n\r\n"
	museHead11 = "  Muse Code 1.4.11\r\n\r\n"
	museVoice  = "── Voice input (alt(option)+v to start) ──\r\n"
	musePrompt = "\x1b[0m\x1b[38;2;90;160;255m❯ \x1b[39m\r\n"
	museRule   = "────────────────────────────────────────\r\n"
	museFooter = "  muse-spark-1.3 · max · /tmp/work · Auto-review\r\n"
	museEmpty  = museHead + museVoice + musePrompt + museRule + museFooter
	museDraft  = museHead + museVoice + "❯ half a thought\r\n" + museRule + museFooter
	// The 1.4.11 hint of the read above; the styled draft and the draft
	// with hint-coloured words after it are the scratch pane's.
	museHint = museHead11 + museVoice +
		"\x1b[0m\x1b[38;2;251;191;36m❯ \x1b[0m\x1b[38;2;103;108;116mAsk to monitor a running build or log and get pinged on changes\x1b[0m\r\n" +
		museRule + museFooter
	museDraftStyled = museHead11 + museVoice +
		"\x1b[0m\x1b[38;2;251;191;36m❯ \x1b[0m\x1b[38;2;204;211;219mhelloverify\x1b[0m\r\n" +
		museRule + museFooter
	museDraftHintAfter = museHead11 + museVoice +
		"❯ half a thought \x1b[0m\x1b[38;2;103;108;116msuggested rest\x1b[0m\r\n" +
		museRule + museFooter
	museTaken   = "❯ hi\r\n\r\n◆ echo: hi\r\n\r\n"
	museWorking = museHead + "❯ hi\r\n\r\n◆ Thinking (3s · esc to interrupt)\r\n\r\n" +
		museVoice + musePrompt + museRule + museFooter

	kiroSep     = "\x1b[0m\x1b[38;2;38;38;38m────────────────────────────────────────\x1b[0m\r\n"
	kiroStatus  = "developer · auto · ◔ 3%              ~/project · (main)\r\n"
	kiroIdle    = "›  \x1b[0m\x1b[38;2;158;158;158mask a question or describe a task ↵\x1b[0m\r\n"
	kiroCopy    = "  /copy to clipboard\r\n"
	kiroEmpty   = kiroSep + kiroStatus + "\r\n" + kiroIdle + kiroCopy
	kiroDraft   = kiroSep + kiroStatus + "\r\n" + "›  half a thought\r\n"
	kiroTaken   = "  earlier message, sent\r\n\r\n• An answer.\r\n\r\n▸ Credits: 0.24 • Time: 3s\r\n\r\n"
	kiroWorking = "›  Kiro is working · 10s · Type to steer · Ctrl+S to queue\r\n"
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
	t.Parallel()
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
		// Muse's input is one line under its voice divider and above a
		// rule and the footer; a message it took carries the same mark,
		// and the line it works behind another.
		"muse empty": {"muse", museEmpty, "", true},
		"muse draft": {"muse", museDraft, "half a thought", true},
		// Muse 1.4.11 draws its hint in a grey no faint style carries, so
		// the kind names the colour: the hint reads empty, a styled draft
		// reads its text, and hint-coloured words after a draft take none
		// of it. Another kind keeps the colour's words as text.
		"muse, 1.4.11 hint reads empty":           {"muse", museHint, "", true},
		"muse, 1.4.11 styled draft":               {"muse", museDraftStyled, "helloverify", true},
		"muse, draft with hint-coloured words":    {"muse", museDraftHintAfter, "half a thought", true},
		"codex, another kind's hint colour":       {"codex", "› \x1b[0m\x1b[38;2;103;108;116mgrey words\x1b[0m\r\n" + codexFooter, "grey words", true},
		"muse, sent message above an empty input": {"muse", museTaken + museEmpty, "", true},
		"muse, sent message above a draft":        {"muse", museTaken + museDraft, "half a thought", true},
		"muse, mid-turn":                          {"muse", museWorking, "", true},
		// Kiro's input is under its status line, begun with ›; an empty
		// one shows its gray placeholder (not faint) with ↵ and the copy
		// hint, and a message it took above is a plain indented
		// paragraph.
		"kiro empty": {"kiro", kiroEmpty, "", true},
		"kiro draft": {"kiro", kiroDraft, "half a thought", true},
		"kiro, sent message above an empty input": {"kiro", kiroTaken + kiroEmpty, "", true},
		"kiro, sent message above a draft":        {"kiro", kiroTaken + kiroDraft, "half a thought", true},
		// Versions capitalize and punctuate the placeholder variously;
		// the words are what tell it.
		"kiro, placeholder capitalized with a comma": {"kiro", kiroSep + kiroStatus + "\r\n" + "›  Ask a question, or describe a task ↵\r\n" + kiroCopy, "", true},
		// A draft that continues the whole placeholder is still text.
		"kiro, draft continues placeholder": {"kiro", kiroSep + kiroStatus + "\r\n" + "›  ask a question or describe a task about this\r\n", "ask a question or describe a task about this", true},
		// The busy line also begins with ›, but the later input stays the
		// empty placeholder while Kiro works.
		"kiro, mid-turn with empty input": {"kiro", kiroWorking + kiroEmpty, "", true},
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

// kimiCaptured is the end of the brakeman's pane as `herdr pane read --source
// recent-unwrapped --lines 200 --format ansi` gave it on 13 Oct 2026 (Kimi
// Code 2.1.1, herdr 0.9.0), its input empty, in a pane 186 columns wide. The
// input's row has a cell after its right edge and so fills the pane, and the
// read joins it to the bottom of the box; the blank row above the box is
// joined to its top, and the two rows of the footer to each other.
var kimiCaptured = strings.Repeat(" ", 189) + "Nothing else pending; back to idle." + strings.Repeat(" ", 147) + "\r\n" +
	strings.Repeat(" ", 187) + "\x1b[0m\x1b[38;2;90;90;90m╭" + strings.Repeat("─", 182) + "╮\x1b[0m\r\n" +
	" " + kimiEdge + " > " + kimiCursor + strings.Repeat(" ", 178) + "\x1b[0m\x1b[38;2;90;90;90m│ \x1b[0m \x1b[0m\x1b[38;2;90;90;90m╰" + strings.Repeat("─", 182) + "╯\x1b[0m\r\n" +
	" \x1b[0m\x1b[1m\x1b[38;2;232;168;56mNever Ask\x1b[0m  \x1b[0m\x1b[38;2;224;224;224mK3 thinking: high\x1b[0m  \x1b[0m\x1b[38;2;136;136;136m~\x1b[0m" + strings.Repeat(" ", 112) +
	"\x1b[0m\x1b[38;2;136;136;136mctrl+o expand\x1b[0m\x1b[38;2;107;107;107m | /init: generate AGENTS.md \x1b[0m" + strings.Repeat(" ", 163) + "\x1b[0m\x1b[38;2;224;224;224mcontext: 4% (34.9k/1M)\x1b[0m"

// kimiPane is a Kimi pane 80 columns wide with lines typed in its input, as
// an unwrapped read gives it: each row of the box fills the pane, with a
// cell after its right edge as in kimiCaptured, and each of joins says
// whether the read joined a row to what is below it, the box's top first
// ("jj-": the top to the first row and that to the second, whose own line
// ends before the bottom). A row that is not named is not joined. A text is
// padded by its cells, as the harness does.
func kimiPane(joins string, lines ...string) string {
	const inner = 77
	end := func(i int) string {
		if i < len(joins) && joins[i] == 'j' {
			return ""
		}
		return "\n"
	}
	s := " ✨ earlier message\n\n ● An answer.\n\n ╭" + strings.Repeat("─", inner) + "╮ " + end(0)
	for i, l := range lines {
		l = map[bool]string{true: " > ", false: "   "}[i == 0] + l
		s += " │" + l + strings.Repeat(" ", inner-ansi.StringWidth(l)) + "│ " + end(i+1)
	}
	return s + " ╰" + strings.Repeat("─", inner) + "╯\n Never Ask  K3 thinking: high  ~\n context: 4% (34.9k/1M)\n"
}

// Found live (yardr-w9vj): the brakeman, a Kimi, sat idle with an empty
// input, and the yard held its nudges for hours: "the agent's input has held
// text". The unwrapped read had joined the input's row to the bottom of its
// box, and the bottom and the footer were read as text. The rotation ask was
// among the nudges held, so the brakeman never rotated. The rows of a box
// are read one by one however the read joined them, and nothing after the
// box's bottom is the input's.
func TestInputTextOfABoxReadUnwrapped(t *testing.T) {
	t.Parallel()
	if text, ok := inputText(kimiCaptured, "kimi"); text != "" || !ok {
		t.Errorf("the captured pane: inputText = %q, %v; want an empty input", text, ok)
	}
	two := []string{"Read /Users/x/repo/.git/worktrees/b1/", "yardr/brief-1x7.md and follow it."}
	for name, c := range map[string]struct {
		joins string
		lines []string
		text  string
	}{
		"empty, rows apart":                  {"", []string{""}, ""},
		"empty, row joined to the bottom":    {"-j", []string{""}, ""},
		"empty, all joined":                  {"jj", []string{""}, ""},
		"one row, rows apart":                {"", []string{"half a thought"}, "half a thought"},
		"one row, joined to the bottom":      {"-j", []string{"half a thought"}, "half a thought"},
		"one row, joined to the top":         {"j-", []string{"half a thought"}, "half a thought"},
		"one row, all joined":                {"jj", []string{"half a thought"}, "half a thought"},
		"two rows, rows apart":               {"", two, strings.Join(two, "\n")},
		"two rows, all joined":               {"-jj", two, strings.Join(two, "\n")},
		"two rows, joined to each other":     {"-j-", two, strings.Join(two, "\n")},
		"two rows, the second to the bottom": {"--j", two, strings.Join(two, "\n")},
		"two rows, the top joined too":       {"jjj", two, strings.Join(two, "\n")},
		// An edge in the text is text: a row ends where the box does.
		"edges in the text, joined": {"-jj", []string{"a │ b │", "│  │ c"}, "a │ b │\n│  │ c"},
		"only an edge, joined":      {"-j", []string{"│"}, "│"},
		// A wide character takes two columns, and an accent none.
		"wide characters, joined": {"-jj", []string{"日本語 ✨ gro\u0308ße", "weiter"}, "日本語 ✨ gro\u0308ße\nweiter"},
		// What is drawn faint is no text in a row that was joined either.
		"a placeholder, joined":   {"-j", []string{"\x1b[2mAsk anything\x1b[0m"}, ""},
		"text and a hint, joined": {"-j", []string{"typed\x1b[2m and suggested\x1b[0m"}, "typed"},
	} {
		pane := kimiPane(c.joins, c.lines...)
		if text, ok := inputText(pane, "kimi"); text != c.text || !ok {
			t.Errorf("%s: inputText = %q, %v; want %q\n%s", name, text, ok, c.text, pane)
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
  "agent prompt") echo "`+mark+` $4" >> taken; answer working ;;
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

// These harness offers are dismissed with Esc as soon as they appear, once
// per appearance. Codex's rate-limit picker has no cancel action, so Esc
// declines it without choosing and Enter is never sent: Enter would switch
// the model. Claude Code's offer only asks whether it may change settings.
func TestInputDialogDismissesOffersWithEsc(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		kind, offer, empty, dialog, line string
	}{
		"claude auto mode": {
			kind: "claude", empty: claudeEmpty,
			offer: "────────────────────────────────────────────────────────────\n" +
				" Teach auto mode about your environment?\n" +
				"\n Auto mode works better when it knows your environment. Takes about a minute.\n" +
				"\n ❯ 1. Yes\n   2. Not now\n   3. Don't show again\n" +
				"\n Enter to confirm · Esc to cancel\n",
			dialog: "auto_mode_setup", line: "Teach auto mode about your environment?",
		},
		"codex rate-limit switch": {
			kind: "codex", empty: codexEmpty,
			offer: "■ You’ve hit your usage limit for gpt-6-astra. Switch to another model now, or try again at Oct 28th, 2026 2:08 AM.\n" +
				"  Approaching rate limits\n" +
				"  Switch to gpt-6-luna for lower credit usage?\n" +
				"\n" +
				"\n" +
				"› 1. Switch to gpt-6-luna                   Fast and affordable model for easier\n" +
				"                                            tasks.\n" +
				"  2. Keep current model\n" +
				"  3. Keep current model (never show again)  Hide future rate limit reminders\n" +
				"                                            about switching models\n" +
				"\n" +
				"  enter select · esc back\n",
			dialog: "rate_limit_switch", line: "Approaching rate limits",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			pane := filepath.Join(dir, "pane")
			if err := os.WriteFile(pane, []byte(tc.offer), 0o600); err != nil {
				t.Fatal(err)
			}
			bin := deafHerdrBin(t, `case "$1 $2" in
  "pane read") cat `+pane+` ;;
  "pane send-keys") echo "$3 $4" >> `+log+` ;;
esac`)
			h := &Herdr{Bin: bin}
			handle := map[string]any{"pane": "p1", harnessKind: tc.kind}
			for i := 0; i < 2; i++ { // a delayed repaint must not receive a second Esc
				dialog, line, err := h.InputDialog(context.Background(), handle)
				if err != nil || dialog != tc.dialog || line != tc.line {
					t.Fatalf("InputDialog = %q, %q, %v; want %q, %q", dialog, line, err, tc.dialog, tc.line)
				}
			}
			got, err := os.ReadFile(log)
			if err != nil || strings.TrimSpace(string(got)) != "p1 escape" {
				t.Fatalf("keys sent = %q, %v; want one escape", got, err)
			}
			if strings.Contains(string(got), "enter") {
				t.Fatalf("keys sent = %q; Enter would choose the selected offer", got)
			}
			if err := os.WriteFile(pane, []byte(tc.empty), 0o600); err != nil {
				t.Fatal(err)
			}
			if dialog, _, err := h.InputDialog(context.Background(), handle); err != nil || dialog != "" {
				t.Fatalf("after the dialog went away, InputDialog = %q, %v", dialog, err)
			}
			if err := os.WriteFile(pane, []byte(tc.offer), 0o600); err != nil {
				t.Fatal(err)
			}
			if dialog, _, err := h.InputDialog(context.Background(), handle); err != nil || dialog != tc.dialog {
				t.Fatalf("second offer, InputDialog = %q, %v; want %q", dialog, err, tc.dialog)
			}
			if got, err := os.ReadFile(log); err != nil || strings.Count(string(got), "escape") != 2 ||
				strings.Contains(string(got), "enter") {
				t.Fatalf("keys after a second offer = %q, %v; want one escape per offer and no enter", got, err)
			}
		})
	}
}

// Found live (yardr-ps05): a person was typing in the yardmaster's pane when
// the yard nudged the yardmaster, and their half sentence went out with the
// nudge as one message. With text in the input no key is sent and the nudge
// stays due; when the input is empty it is typed, once. A placeholder is not
// text, and a harness whose input cannot be told is nudged as before. Each
// crew member is of the kind whose pane it shows: its first prompt is told
// as taken by that kind's mark (see promptPlace).
func TestNudgeIsNotTypedIntoAnInputThatHoldsText(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
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
			if r, err := s.GetCrew(ctx, "yardmaster"); err != nil || !r.Live() || r.Handle[store.PendingPrompt] != nil {
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
	t.Parallel()
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
			t.Parallel()
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
			if err != nil || !r.Live() || r.Handle[store.PendingPrompt] != nil {
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

// nudgedHerdr is a fake herdr whose Claude Code is idle and takes its first
// prompt. What it does with a later one the returned function sets: "stall"
// leaves the text in its input, the Enter not taken, and herdr sees it start
// on nothing; "refuse" has it at a dialog, where herdr types nothing; and ""
// takes it. Enter in the pane submits what is in the input. Every call is
// logged.
func nudgedHerdr(t *testing.T) (h *Herdr, calls func() string, mode func(string)) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	mode = func(m string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mode("")
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
  "agent prompt")
    case "$(cat mode)" in
      refuse) answer refused ;;
      stall) printf '%s' "$4" > input; answer idle ;;
    esac
    echo "❯ $4" >> taken; answer working ;;
  "pane send-keys")
    if [ "$4" = enter ] && [ -s input ]; then echo "❯ $(cat input)" >> taken; : > input; fi ;; # as herdr: nothing printed
  "pane read") cat taken 2>/dev/null; echo "────"; echo "❯ $(cat input 2>/dev/null)"; echo "────"; echo "  ? for shortcuts" ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }, mode
}

// A nudge goes out with herdr's wait, as the first prompt does (yardr-s1em).
// One herdr saw the agent start on nothing for, and that the pane shows in
// the input, gets its Enter on the pass that typed it, and
// session_prompt_typed says so: before, the pass after PromptSettle found
// it. One for an agent at a dialog is not typed: no key is sent, one
// nudge_held says so, and it is typed once the dialog is gone.
func TestNudgeGoesOutWithHerdrsWait(t *testing.T) {
	t.Parallel()
	h, calls, mode := nudgedHerdr(t)
	ctx := context.Background()
	s := storetest.New(t)
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: time.Hour, PromptWait: -1}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir(),
		Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	pass := func(n int) {
		t.Helper()
		for range n {
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	pass(3)
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil || !r.Live() || r.Handle[store.PendingPrompt] != nil || r.Handle[dispatch.PromptLook] != "shown, herdr working" {
		t.Fatalf("crew member = %+v %v", r, err)
	}
	pane, _ := r.Handle["pane"].(string)
	nudges := func() int { return strings.Count(calls(), "agent prompt "+r.Handle["agent"].(string)+" yardr: ") }
	enters := func() int { return strings.Count(calls(), "pane send-keys "+pane+" enter") }
	events := func(kind string) (n int, last store.Event) {
		t.Helper()
		evs, err := s.Events(ctx, r.Journal, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == kind {
				n, last = n+1, e
			}
		}
		return n, last
	}

	mode("stall")
	if err := s.Mail(ctx, "yardmaster", "deploy freeze", "chris"); err != nil {
		t.Fatal(err)
	}
	pass(1)
	typed, ev := events("session_prompt_typed")
	if nudges() != 1 || enters() != 1 || typed != 1 || ev.Data["text"] != "a nudge" || ev.Data["crew"] != "yardmaster" {
		t.Fatalf("on the pass that typed the nudge: typed %d times, Enter %d times, session_prompt_typed %d times (%+v)\n%s",
			nudges(), enters(), typed, ev.Data, calls())
	}
	if !strings.Contains(calls(), " --wait --until working --until blocked --timeout 6000\n") {
		t.Fatalf("the nudge was not given to herdr to wait on:\n%s", calls())
	}
	pass(3)
	if nudges() != 1 || enters() != 1 {
		t.Fatalf("after its Enter: typed %d times, Enter %d times\n%s", nudges(), enters(), calls())
	}

	mode("refuse")
	if err := s.Mail(ctx, "yardmaster", "second", "chris"); err != nil {
		t.Fatal(err)
	}
	pass(3)
	if held, ev := events("nudge_held"); enters() != 1 || held != 1 || ev.Data["pane"] != pane || !strings.Contains(fmt.Sprint(ev.Data["reason"]), "dialog") {
		t.Fatalf("at a dialog: Enter %d times in all, nudge_held %d times (%+v)\n%s", enters(), held, ev.Data, calls())
	}
	asked := nudges()
	mode("")
	pass(3)
	if nudges() != asked+1 || enters() != 1 {
		t.Fatalf("after the dialog: the nudge was given to herdr %d more times, want once; Enter %d times in all\n%s",
			nudges()-asked, enters(), calls())
	}
}

// idleKimi is a fake herdr whose Kimi Code is idle and takes its first
// prompt. What it does with a later one the returned function sets: "menu"
// has its cache menu drawn in place of its input already; "asks" has it
// answer the next prompt with that menu and keep the prompt behind it, as
// Kimi Code 2.1.1 does after a long idle spell; and "" takes it. Every call
// is logged.
func idleKimi(t *testing.T) (h *Herdr, calls func() string, mode func(string)) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	mode = func(m string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mode("")
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
  "agent prompt")
    case "$(cat mode)" in
      menu) answer idle ;;
      asks) echo menu > mode; answer idle ;;
    esac
    echo " ✨ $4" >> taken; answer working ;;
  "pane read")
    cat taken 2>/dev/null
    if [ "$(cat mode)" = menu ]; then
      echo " ────────────────────────────────────────────────────────────"
      echo "  This session has been idle for 44m and is ~198k tokens."
      echo "  ↑↓ navigate · Enter select · Esc cancel"
      echo "  Cache expired — the next message re-sends the entire history at full price."
      echo "   ❯ Compact and continue    one-time compact cost · cheapest way to keep this topic"
      echo "     Start a new session     zero context cost · best for a new task"
      echo "     Continue as-is          full history kept · highest cost per turn"
      echo "     Don't ask me again"
      echo " ────────────────────────────────────────────────────────────"
    else
      echo " ╭──────────────────────────────────────╮"
      echo " │ >                                    │"
      echo " ╰──────────────────────────────────────╯"
    fi
    echo " Ask When Needed  K3 thinking: high  ~         ctrl+o expand" ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }, mode
}

// Found live (yardr-vsen): a crew member on Kimi Code sat behind Kimi's
// menu to a session that was idle past its cache, and nobody was told. The
// pass that finds the menu where the input is types nothing, sends no key
// and raises one nudge_held with reason dialog and the menu's first line; a
// second nudge into the open menu raises nothing; and with the menu
// answered the nudge is typed. A pane without the menu is nudged as ever.
func TestNudgeAtKimisCacheMenuIsToldOfOnce(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, mode string
		typed      int // nudges given to herdr while the menu is open
	}{
		{"the menu is open when the nudge is due", "menu", 0},
		{"the nudge is what Kimi answers with the menu", "asks", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, calls, mode := idleKimi(t)
			ctx := context.Background()
			s := storetest.New(t)
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: time.Hour, PromptWait: -1}
			if _, err := s.PutCrew(ctx, store.CrewMember{Name: "brakeman", Runner: "herdr", Dir: t.TempDir(),
				Config: map[string]any{"kind": "kimi"}}, "me"); err != nil {
				t.Fatal(err)
			}
			pass := func(n int) {
				t.Helper()
				for range n {
					if err := d.Reconcile(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			pass(3)
			r, err := s.GetCrew(ctx, "brakeman")
			if err != nil || !r.Live() || r.Handle[store.PendingPrompt] != nil {
				t.Fatalf("crew member = %+v %v", r, err)
			}
			pane, _ := r.Handle["pane"].(string)
			nudges := func() int { return strings.Count(calls(), "agent prompt "+r.Handle["agent"].(string)+" yardr: ") }
			keys := func() int { return strings.Count(calls(), "pane send-keys "+pane) }
			held := func() (n int, last store.Event) {
				t.Helper()
				evs, err := s.Events(ctx, r.Journal, 200)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range evs {
					if e.Kind == "nudge_held" {
						n, last = n+1, e
					}
				}
				return n, last
			}
			// A pane without the menu behaves as today: the nudge is typed.
			if err := s.Mail(ctx, "brakeman", "first", "chris"); err != nil {
				t.Fatal(err)
			}
			pass(2)
			if n, _ := held(); nudges() != 1 || n != 0 {
				t.Fatalf("without the menu: nudged %d times, nudge_held %d times; want one and none\n%s", nudges(), n, calls())
			}

			mode(c.mode)
			if err := s.Mail(ctx, "brakeman", "deploy freeze", "chris"); err != nil {
				t.Fatal(err)
			}
			pass(1)
			n, ev := held()
			if n != 1 || ev.Data["reason"] != "dialog" || ev.Data["dialog"] != "cache_menu" || ev.Data["pane"] != pane || ev.Data["crew"] != "brakeman" ||
				ev.Data["text"] != "This session has been idle for 44m and is ~198k tokens." {
				t.Fatalf("on the pass that met the menu: nudge_held %d times, want once with the dialog and its line: %+v\n%s", n, ev.Data, calls())
			}
			if err := s.Mail(ctx, "brakeman", "second", "chris"); err != nil {
				t.Fatal(err)
			}
			pass(3)
			if n, _ := held(); n != 1 || nudges() != 1+c.typed || keys() != 0 {
				t.Fatalf("with the menu open: nudge_held %d times, nudged %d times in all, %d keys sent; want 1, %d and none\n%s",
					n, nudges(), keys(), 1+c.typed, calls())
			}

			mode("")
			pass(2)
			if n, _ := held(); n != 1 || nudges() != 2+c.typed {
				t.Fatalf("with the menu answered: nudge_held %d times, nudged %d times in all; want 1 and %d\n%s", n, nudges(), 2+c.typed, calls())
			}
		})
	}
}

// A handle from before the kind was written is of no kind yardr knows.
func TestInputMarked(t *testing.T) {
	t.Parallel()
	h := &Herdr{}
	for kind, want := range map[string]bool{"claude": true, "codex": true, "kimi": true, "kiro": true, "acme": false, "": false} {
		handle := map[string]any{}
		if kind != "" {
			handle[harnessKind] = kind
		}
		if got := h.InputMarked(handle); got != want {
			t.Errorf("InputMarked(kind %q) = %v, want %v", kind, got, want)
		}
	}
}

// A pane's text is its agent's to draw: a box as wide as the agent likes,
// with rows of nothing but edges, or thousands of rows joined in one line.
// The daemon reads it on every pass, so reading it must not take longer by
// the square of its width (it took minutes at this one).
func TestRowsOfALineAsLongAsAnAgentLikes(t *testing.T) {
	t.Parallel()
	const width = 100000
	bottom := "╰" + strings.Repeat("─", width-2) + "╯"
	for name, row := range map[string]string{
		"edges alone": strings.Repeat("│", width-1) + "x" + strings.Repeat("│", width),
		"many rows":   strings.Repeat("│"+strings.Repeat("x", 98)+"│", 20000),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w, b := width, bottom
			if name == "many rows" {
				w, b = 100, "╰"+strings.Repeat("─", 98)+"╯"
			}
			done := make(chan int, 1)
			go func() {
				rows, _ := rowsOf("kimi", []string{"│ > hi", row, b}, []string{"│ > hi", row, b})
				done <- len(rows)
			}()
			select {
			case n := <-done:
				if name == "many rows" && n != 20002 {
					t.Errorf("rows of a box %d wide: %d, want 20002", w, n)
				}
			case <-time.After(20 * time.Second):
				t.Fatalf("a box %d wide was not read in 20s", w)
			}
		})
	}
}

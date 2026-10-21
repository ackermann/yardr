package runner

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
)

// HarnessScreen reports whether the pane of handle shows the screen of its
// harness, which is known by the mark of its input (dispatch.ScreenWatcher).
// known is false where yardr cannot tell: a kind whose mark it does not
// know, a handle without a pane, a pane that cannot be read.
//
// Found live (yardr-bcx5): three Codex sessions started while a release of
// Codex was out, and each upgraded itself in its pane before it drew
// anything (`brew upgrade --cask codex`, for minutes). herdr had them idle,
// and the first prompt was typed into brew's output, then three times more
// by the dispatcher in thirty seconds, and given up. A pane that has not
// shown the harness's input is no place to type: the prompt waits for it.
func (h *Herdr) HarnessScreen(ctx context.Context, handle map[string]any) (up, known bool) {
	kind, _ := handle[harnessKind].(string)
	m, ok := marksOf(kind)
	pane, _ := handle["pane"].(string)
	if !ok || pane == "" {
		return false, false
	}
	text, err := h.readPane(ctx, pane, promptScan)
	if err != nil {
		return false, false
	}
	lines, _ := rowsOf(kind, strings.Split(text, "\n"), nil)
	return slices.ContainsFunc(lines, m.begins), true
}

// inputMarks begin the first line of a harness's input: Claude Code's "❯"
// and Codex's "›". Both draw their input at the bottom of the pane and begin
// a message they took with the same mark, further up. They are what a pane
// is read by when the marks of its own kind do not tell.
var inputMarks = []string{"❯", "›"}

// paneMarks are the marks the pane of a harness kind is read by: input
// begins the first line of its input, and message a message it took, which
// it echoes above the input. edge, for a kind that draws its input in a
// box, is the side of the box: it comes before the input mark, and before
// and after each line of the input's text, of which it is no part.
// placeholder is the input's hint when it holds nothing, for a harness
// that draws one in no faint style; placeholderSGR is the colour such a
// hint is drawn in, for a harness whose hint's words are not fixed.
type paneMarks struct{ input, message, edge, placeholder, placeholderSGR string }

// marksOf is the marks of a harness kind, and whether yardr knows them:
// its file (harnesses/<kind>.yaml, `marks`) names the mark of its input.
// Claude Code and Codex begin a message they took with the mark of their
// input; Kimi Code, whose input is a box, does not, and Kiro names no mark
// for one, whose echo is a plain paragraph.
func marksOf(kind string) (paneMarks, bool) {
	h, _ := agents.Lookup(kind)
	m := h.Marks
	return paneMarks{input: m.Input, message: m.Message, edge: m.Edge, placeholder: m.Placeholder, placeholderSGR: m.PlaceholderSGR}, m.Input != ""
}

// begins reports whether line is the first of the input.
func (m paneMarks) begins(line string) bool {
	if m.edge != "" && !strings.HasPrefix(strings.TrimSpace(line), m.edge) {
		return false
	}
	return strings.HasPrefix(m.unboxed(line), m.input)
}

// unboxed is line without the white space around it and, for a kind that
// draws its input in a box, without the box's edges.
func (m paneMarks) unboxed(line string) string {
	line = strings.TrimSpace(line)
	if m.edge == "" {
		return line
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, m.edge), m.edge))
}

// rowsOf is the lines of a pane of a harness of kind, each row of its
// input's box on a line of its own; typed, the same lines without what is
// drawn faint (see unstyled) or nil, is cut where plain is. The lines of a
// kind that draws no box are given back as they are.
//
// An unwrapped read joins a row that fills the pane's width to the row
// below it, and whether a row of the box fills it is as Kimi last drew it.
// Found live (yardr-w9vj, Kimi Code 2.1.1, herdr 0.9.0): the row of an empty
// input was read as one line with the bottom of its box,
//
//	│ >                        │  ╰──────────────────────────╯
//
// which left the bottom and the footer after it as the input's text, so the
// input held text for as long as it was empty and no nudge was typed. The
// top of the box stood on a line with the blank row above it.
//
// The box tells where its rows end: each is as wide as its bottom, from
// edge to edge. A line that is no row and no rule of that width, or goes on
// with something else, is left whole from there on.
func rowsOf(kind string, plain, typed []string) (rows, typedRows []string) {
	m, _ := marksOf(kind)
	edge := []rune(m.edge)
	width := boxWidth(plain)
	if len(edge) != 1 || width == 0 {
		return plain, typed
	}
	for i, l := range plain {
		rs := []rune(l)
		cuts := rowCuts(rs, edge[0], width)
		if len(cuts) < 2 {
			rows = append(rows, l)
			if typed != nil {
				typedRows = append(typedRows, typed[i])
			}
			continue
		}
		var ts []rune
		if typed != nil {
			ts = []rune(typed[i])
		}
		for _, c := range cuts {
			rows = append(rows, string(rs[c[0]:c[1]]))
			if typed != nil {
				typedRows = append(typedRows, string(ts[c[0]:c[1]]))
			}
		}
	}
	return rows, typedRows
}

// boxWidth is the width of the box a pane's input is drawn in, corner to
// corner: that of the last bottom of a box that ends a line, 0 for a pane
// with none. The input is the last box a harness draws.
func boxWidth(plain []string) int {
	for i := len(plain) - 1; i >= 0; i-- {
		rs := []rune(strings.TrimSpace(plain[i]))
		j := len(rs) - 1
		if j < 1 || rs[j] != '╯' {
			continue
		}
		for j--; j > 0 && rs[j] == '─'; j-- {
		}
		if rs[j] == '╰' {
			return len(rs) - j
		}
	}
	return 0
}

// rowCuts is where the rows of a box width wide, joined in line, begin and
// end: a row runs from an edge to the edge width cells on, and the box's top
// and bottom are rules of that width. White space before a row is cut with
// it. What is neither is one last part, down to the line's end.
func rowCuts(line []rune, edge rune, width int) (cuts [][2]int) {
	for from := 0; ; {
		at := from
		for at < len(line) && unicode.IsSpace(line[at]) {
			at++
		}
		if at == len(line) {
			return cuts
		}
		end := -1
		switch {
		case line[at] == edge:
			// Cells, not runes: a row with a wide character has fewer runes
			// than the box has columns. An edge short of the width is of the
			// text. The cells are counted on from edge to edge, each part of
			// the line once: the line is an agent's to draw, as long as it
			// likes, and the daemon reads it on every pass.
			cells, last := 0, at
			for k := at + 1; k < len(line) && end < 0; k++ {
				if line[k] != edge {
					continue
				}
				cells += ansi.StringWidth(string(line[last : k+1]))
				last = k + 1
				switch {
				case cells == width:
					end = k + 1
				case cells > width:
					k = len(line)
				}
			}
		case at+width <= len(line) && isRule(string(line[at:at+width])) &&
			(at+width == len(line) || unicode.IsSpace(line[at+width])):
			end = at + width
		}
		if end < 0 {
			return append(cuts, [2]int{from, len(line)})
		}
		cuts = append(cuts, [2]int{from, end})
		from = end
	}
}

// promptPlace finds prompt in the text of a pane whose harness is of kind:
// taken when it shows above the agent's input (the harness echoed it as a
// sent message), typed when it shows only in the input. The input is what
// the pane holds from its last line that begins with an input mark: a
// harness draws its input below everything it has printed, so a prompt it
// took has an input, empty or not, after it.
//
// For a kind whose marks yardr knows, only they tell. A pane without the
// input mark is of a harness that has not drawn its input yet, and a prompt
// that shows there is bare: the keys echoed by the pane before the harness
// read them, which says nothing of what the harness did with them. Such an
// echo stays in the pane, above the input the harness draws later, so above
// the input the prompt is taken only where it begins a line with the
// message mark, as a message the harness took does.
//
// A pane of any other kind without a marked line is of a harness whose
// input yardr cannot tell from its transcript; a prompt that shows there is
// taken as arrived, as it was before yardr told the two apart.
func promptPlace(text, prompt, kind string) (taken, typed, bare bool) {
	lines, _ := rowsOf(kind, strings.Split(text, "\n"), nil)
	m, known := marksOf(kind)
	input := -1
	for i, l := range lines {
		if known {
			if m.begins(l) {
				input = i
			}
			continue
		}
		for _, mark := range inputMarks {
			if strings.HasPrefix(strings.TrimSpace(l), mark) {
				input = i
			}
		}
	}
	switch {
	case input < 0 && known:
		return false, false, showsPrompt(text, prompt)
	case input < 0:
		return showsPrompt(text, prompt), false, false
	case !known:
		taken = showsPrompt(strings.Join(lines[:input], "\n"), prompt)
	default:
		for i, l := range lines[:input] {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, m.message) {
				continue
			}
			message := strings.TrimPrefix(l, m.message) + "\n" + strings.Join(lines[i+1:input], "\n")
			if beginsWithPrompt(message, prompt) {
				taken = true
				break
			}
		}
	}
	if taken {
		return true, false, false
	}
	// A box's edges are between the lines of a prompt it wraps.
	in := make([]string, 0, len(lines)-input)
	for _, l := range lines[input:] {
		in = append(in, m.unboxed(l))
	}
	return false, showsPrompt(strings.Join(in, "\n"), prompt), false
}

// InputText is what is typed in the input of the session's agent and not
// sent: a person's unfinished message, or a prompt whose Enter was not
// taken. A harness's placeholder is not text. known is false for a harness
// whose input yardr cannot tell (no line with an input mark), and an error
// says the pane could not be read; either way nothing is known about the
// input.
//
// Found live (yardr-ps05): a person was typing in the yardmaster's pane when
// the yard nudged the yardmaster. The nudge went into the same input, and its
// Enter sent their half sentence and the nudge as one message. So what types
// into an input looks here first.
func (h *Herdr) InputText(ctx context.Context, handle map[string]any) (text string, known bool, err error) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return "", false, fmt.Errorf("handle has no pane")
	}
	styled, err := h.readPaneStyled(ctx, pane, promptScan)
	if err != nil {
		return "", false, err
	}
	kind, _ := handle[harnessKind].(string)
	text, known = inputText(styled, kind)
	return text, known, nil
}

// inputText finds, in a pane of a harness of kind read with its styling,
// the text of the agent's input: what follows the mark on the pane's last
// line that begins with one (see promptPlace), and the lines after it down
// to the first empty line or rule, which is where Codex's and Claude Code's
// input ends and their footer begins, as the bottom of its box is for
// Kimi's, whose rows are read one to a line however the read joined them
// (see rowsOf). ok is false for a pane with no such line.
//
// The mark is the kind's own. Where the pane shows none of it, or the kind
// is one yardr knows no marks for, any of inputMarks is taken for one: what
// follows may be text the harness printed, and yardr types nothing after it
// all the same. Kimi's question to a session that sat idle is drawn in place
// of its box, with "❯" before the answer Enter would choose.
//
// Faint text (SGR 2) is left out: it is how Claude Code and Codex draw what
// is in the input and was not typed. Read live with `herdr pane read
// --format ansi` (herdr 0.9.0), Claude Code 2.1.288 shows `❯ ESC[2mTry "fix
// typecheck errors"` and Codex `› ESC[2mAsk Codex to do anything`, and each
// shows what a person typed there with no attribute at all, as Kimi Code
// 2.1.1 does. The attribute is asked, not the wording: Claude Code's
// placeholder is a different suggestion each time, and a person may type a
// placeholder's very words. A kind whose hint is drawn in no faint style
// and whose words are not fixed names its hint's colour instead
// (harnesses/<kind>.yaml, `marks.placeholder_sgr`): text drawn in it is
// left out the same way, for that kind only. Muse Code 1.4.11 draws its
// hint in 38;2;103;108;116 and what a person typed in 38;2;204;211;219
// (read live, 21 Oct 2026).
func inputText(styled, kind string) (text string, ok bool) {
	kindMarks, _ := marksOf(kind)
	plain, typed := unstyled(styled, kindMarks.placeholderSGR)
	plain, typed = rowsOf(kind, plain, typed)
	input, m := inputLine(plain, kind)
	if input < 0 {
		return "", false
	}
	if m.placeholder != "" && isPlaceholder(m.unboxed(typed[input]), m.input, m.placeholder) {
		// The input holds only the harness's gray hint (Kiro's): it
		// reads empty, the hints under it with it.
		return "", true
	}
	var lines []string
	for i := input; i < len(plain); i++ {
		if i > input && (strings.TrimSpace(plain[i]) == "" || isRule(plain[i])) {
			break
		}
		l := m.unboxed(typed[i])
		if i == input {
			l = strings.TrimPrefix(l, m.input)
		}
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n"), true
}

// isPlaceholder reports whether line, the first line of an input without
// its box's edges, shows the harness's placeholder and nothing typed: the
// mark taken off, the words match the placeholder. Kiro draws
// its empty input's gray hint ("ask a question or describe a task", a ↵
// after it, hints under it) in no faint style, so reads that drop faint
// text keep it, and without this an empty input reads as one that holds
// text. The match ignores punctuation and case: versions capitalize and
// punctuate the hint variously ("Ask a question, or describe a task").
// Comparing the whole hint keeps a draft that continues with those words.
func isPlaceholder(line, mark, placeholder string) bool {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), mark))
	norm := func(s string) string {
		words := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				return unicode.ToLower(r)
			}
			if unicode.IsSpace(r) {
				return ' '
			}
			return -1
		}, s)
		return strings.Join(strings.Fields(words), " ")
	}
	return norm(line) == norm(placeholder)
}

// inputLine finds, in the lines of a pane of a harness of kind, the first
// line of the agent's input and the marks it is read by (see inputText);
// -1 for a pane with no such line.
func inputLine(plain []string, kind string) (input int, m paneMarks) {
	m, known := marksOf(kind)
	input = -1
	if known {
		for i, l := range plain {
			if m.begins(l) {
				input = i
			}
		}
	}
	if input < 0 {
		for i, l := range plain {
			for _, mark := range inputMarks {
				if strings.HasPrefix(strings.TrimSpace(l), mark) {
					input, m = i, paneMarks{input: mark}
				}
			}
		}
	}
	return input, m
}

// dialogScan is how many lines above the input mark a dialog's lines are
// looked for: Kimi's cache menu has three above its first answer.
const dialogScan = 12

// InputDialog names the dialog the harness of the session's agent has
// drawn where its input is, by the word its kind's file has for it
// (harnesses/<kind>.yaml, `dialogs`), and gives the dialog's first line;
// "" for a pane that shows none (dispatch.DialogReader). The pane of a
// kind with no dialog in its file is not read.
//
// Found live (yardr-vsen): Kimi Code answers a message that follows a long
// idle spell with a menu drawn in place of its input, and keeps the message
// until the menu is answered. The menu's selected answer reads as text in
// the input (see inputText), so nothing was typed into it, and nothing told
// anyone that the agent sat there.
func (h *Herdr) InputDialog(ctx context.Context, handle map[string]any) (dialog, line string, err error) {
	kind, _ := handle[harnessKind].(string)
	if hn, _ := agents.Lookup(kind); len(hn.Dialogs) == 0 {
		return "", "", nil
	}
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return "", "", fmt.Errorf("handle has no pane")
	}
	styled, err := h.readPaneStyled(ctx, pane, promptScan)
	if err != nil {
		return "", "", err
	}
	dialog, line = inputDialog(styled, kind)
	// This offer only asks whether Claude Code may change the operator's
	// settings. Esc cancels it without changing anything, so dismiss it as
	// soon as it appears; leaving it open can make an otherwise idle session
	// look stuck. Remember the answer until a read sees the dialog go away,
	// since the pane may take a pass to redraw after the key.
	h.dialogMu.Lock()
	if dialog == "auto_mode_setup" {
		if h.dismissedDialogs == nil {
			h.dismissedDialogs = map[string]string{}
		}
		alreadyDismissed := h.dismissedDialogs[pane] == dialog
		h.dismissedDialogs[pane] = dialog
		h.dialogMu.Unlock()
		if !alreadyDismissed {
			if err := h.sendKey(ctx, pane, "escape"); err != nil {
				h.dialogMu.Lock()
				delete(h.dismissedDialogs, pane)
				h.dialogMu.Unlock()
				return "", "", err
			}
		}
		return dialog, line, nil
	}
	delete(h.dismissedDialogs, pane)
	h.dialogMu.Unlock()
	return dialog, line, nil
}

var _ dispatch.LoginReader = (*Herdr)(nil)

// InputLogin names the line of the login screen the harness of the
// session's agent sits at (harnesses/<kind>.yaml, `login`); "" for a pane
// that shows none. The pane of a kind with no login screen in its file is
// not read.
func (h *Herdr) InputLogin(ctx context.Context, handle map[string]any) (line string, err error) {
	kind, _ := handle[harnessKind].(string)
	if hn, _ := agents.Lookup(kind); len(hn.Login) == 0 {
		return "", nil
	}
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return "", fmt.Errorf("handle has no pane")
	}
	text, err := h.readPane(ctx, pane, promptScan)
	if err != nil {
		return "", err
	}
	return loginLine(text, kind), nil
}

// inputDialog finds, in a pane of a harness of kind read with its styling,
// the dialog the input mark stands in: the first line that one of the
// kind's dialogs matches among those around the mark, which are the lines
// above it up to a rule (dialogScan at most) and the lines of the input
// itself. A dialog's words further up, in what the agent printed, are no
// dialog: a harness draws its question where its input is, and a rule or
// the edge of a box is between its input and its transcript.
func inputDialog(styled, kind string) (dialog, line string) {
	hn, _ := agents.Lookup(kind)
	plain, _ := unstyled(styled, "")
	plain, _ = rowsOf(kind, plain, nil)
	input, m := inputLine(plain, kind)
	if input < 0 || len(hn.Dialogs) == 0 {
		return "", ""
	}
	from := input
	for from > 0 && input-from < dialogScan && !isRule(plain[from-1]) {
		from--
	}
	for i := from; i < len(plain); i++ {
		if i > input && (strings.TrimSpace(plain[i]) == "" || isRule(plain[i])) {
			break
		}
		l := m.unboxed(plain[i])
		for _, d := range hn.Dialogs {
			if d.Matches(l) {
				return d.Dialog, l
			}
		}
	}
	return "", ""
}

// loginLines is how many of the pane's last lines a login screen is looked
// for in. The screen is what the harness draws now, at the end; the same
// words further up are what the agent printed while it still worked.
const loginLines = 20

// loginLine finds, in the text of a pane whose harness is of kind, the line
// of its login screen: the last line one of the kind's login lines matches
// among the pane's end, "" for a pane that shows none.
func loginLine(text, kind string) string {
	hn, _ := agents.Lookup(kind)
	if len(hn.Login) == 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, seen := len(lines)-1, 0; i >= 0 && seen < loginLines; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		seen++
		for _, ln := range hn.Login {
			if ln.Matches(l) {
				return l
			}
		}
	}
	return ""
}

// isRule reports whether line is drawn of box-drawing characters alone, as
// the rules above and below Claude Code's input are.
func isRule(line string) bool {
	line = strings.TrimSpace(line)
	for _, r := range line {
		if r < 0x2500 || r > 0x257f {
			return false
		}
	}
	return line != ""
}

// unstyled splits a pane read with its styling into lines, twice: plain is
// each line's text, and typed the same with a space for each character that
// is drawn faint or, for a kind that names its hint's colour, in that
// colour (hint, "" for none), so that what is cut out of one is cut out of
// the other at the same place (see rowsOf). Every
// escape sequence is dropped; of SGR only faint (2) and what ends it (22,
// 0) are followed, and the foreground colour (38), which 0 and 39 end. The
// numbers after 38, 48 and 58 are a colour's, so a 2 among them
// (`38;2;r;g;b`, `38;5;2`) is not the attribute.
func unstyled(styled, hint string) (plain, typed []string) {
	var p, t strings.Builder
	faint := false
	fg := ""
	flush := func() {
		plain, typed = append(plain, p.String()), append(typed, t.String())
		p.Reset()
		t.Reset()
	}
	rs := []rune(styled)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\n':
			flush()
		case r == '\r':
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == '[':
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j < len(rs) && rs[j] == 'm' {
				params := string(rs[i+2 : j])
				faint = sgrFaint(params, faint)
				fg = sgrFG(params, fg)
			}
			i = j
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == ']':
			// An OSC (a title, a hyperlink) runs to BEL or ESC \.
			j := i + 2
			for j < len(rs) && rs[j] != 0x07 && !(rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\') {
				j++
			}
			if j < len(rs) && rs[j] == 0x1b {
				j++
			}
			i = j
		case r == 0x1b:
			i++ // a two-character escape
		default:
			p.WriteRune(r)
			if faint || (hint != "" && fg == hint) {
				r = ' '
			}
			t.WriteRune(r)
		}
	}
	flush()
	return plain, typed
}

// sgrFaint is whether text is faint after the SGR parameters params, given
// whether it was before.
func sgrFaint(params string, faint bool) bool {
	if params == "" {
		return false // ESC[m is ESC[0m
	}
	codes := strings.Split(params, ";")
	for i := 0; i < len(codes); i++ {
		// A colon carries a parameter's own arguments (38:2::r:g:b): only
		// what is before it is the code.
		code, _, _ := strings.Cut(codes[i], ":")
		switch code {
		case "", "0", "22":
			faint = false
		case "2":
			faint = true
		case "38", "48", "58":
			if codes[i] != code || i+1 >= len(codes) {
				break
			}
			switch codes[i+1] {
			case "5":
				i += 2
			case "2":
				i += 4
			}
		}
	}
	return faint
}

// sgrFG is the foreground colour text is drawn in after the SGR parameters
// params, given what it was before: `38;2;r;g;b` or `38;5;n` in canonical
// shape, "" for the default. 0 and an empty sequence draw everything as
// default again, 39 the foreground alone; other attributes leave it. A
// colour with numbers that are none is left as it was.
func sgrFG(params string, fg string) string {
	if params == "" {
		return "" // ESC[m is ESC[0m
	}
	codes := strings.Split(params, ";")
	for i := 0; i < len(codes); i++ {
		// A colon carries a parameter's own arguments (38:2::r:g:b): only
		// what is before it is the code.
		code, _, _ := strings.Cut(codes[i], ":")
		switch code {
		case "", "0", "39":
			fg = ""
		case "38":
			if codes[i] != code || i+1 >= len(codes) {
				break
			}
			switch codes[i+1] {
			case "5":
				if i+2 < len(codes) {
					if n, ok := sgrNum(codes[i+2]); ok {
						fg = fmt.Sprintf("38;5;%d", n)
					}
				}
				i += 2
			case "2":
				if i+4 < len(codes) {
					r, rok := sgrNum(codes[i+2])
					g, gok := sgrNum(codes[i+3])
					b, bok := sgrNum(codes[i+4])
					if rok && gok && bok {
						fg = fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
					}
				}
				i += 4
			}
		case "48", "58":
			if codes[i] != code || i+1 >= len(codes) {
				break
			}
			switch codes[i+1] {
			case "5":
				i += 2
			case "2":
				i += 4
			}
		}
	}
	return fg
}

// sgrNum is s as an SGR colour number, and whether it is one.
func sgrNum(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 255 {
		return 0, false
	}
	return n, true
}

// promptLead is how much of a prompt, counted without its white space, is
// looked for in a pane: enough to tell it from anything else there, and
// short enough that an agent which shortens what it echoes still shows it.
const promptLead = 24

// showsPrompt reports whether a pane's text holds the start of prompt.
// White space is left out on both sides: an agent wraps a long prompt to
// its pane's width, in the middle of a brief's path if it must.
func showsPrompt(text, prompt string) bool {
	lead := promptLeadOf(prompt)
	return lead != "" && strings.Contains(strings.Join(strings.Fields(text), ""), lead)
}

// beginsWithPrompt is showsPrompt for a text that must begin with the
// prompt, not only hold it.
func beginsWithPrompt(text, prompt string) bool {
	lead := promptLeadOf(prompt)
	return lead != "" && strings.HasPrefix(strings.Join(strings.Fields(text), ""), lead)
}

func promptLeadOf(prompt string) string {
	lead := []rune(strings.Join(strings.Fields(prompt), ""))
	if len(lead) > promptLead {
		lead = lead[:promptLead]
	}
	return string(lead)
}

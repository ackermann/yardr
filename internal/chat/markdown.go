package chat

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// A mail's text is drawn as a small part of Markdown, the part a crew
// member writes without thinking of it: fenced code blocks and inline code,
// bullets and numbered items, headings, bold and italic. Everything else is
// text as written, wrapped as wrap does it, so a mail with none of these is
// drawn as it was before there was any of this.
//
// It is a renderer of these few forms and no Markdown parser. A parser
// (Glamour is Charm's) reflows a paragraph's lines into one, sets margins
// of its own and draws links, tables and images its way, and its wrapping
// would have to agree with wrap and with the lead of every line. Here a
// line of the text stays a line, and what is no form is not touched.
//
// The text is data: nothing in it is followed or fetched, a link stays the
// characters it is, and what another yard wrote has had its escape
// sequences taken out before it comes here (plain).

// The kinds of a run of a line's text, which add up: bold code in a
// heading is all three.
const (
	strong uint8 = 1 << iota
	em
	code
)

// run is a stretch of a line drawn one way.
type run struct {
	text string
	kind uint8
}

const (
	// bullet is drawn for an item's - or *.
	bullet = "•"
	// codeLead stands before a line of a code block, and codeMore before
	// what of it did not fit the row above: a line of code is never
	// reflowed as prose, so where it breaks is marked.
	codeLead = "  "
	codeMore = "↪ "
	tab      = "    "
)

// markdown is a mail's text as lines of at most w columns behind lead, as
// wrap gives them, with the forms above drawn. In a terminal without
// colour, where nothing can be bold either, a heading and the marks inside
// a line stay as written, which is Markdown's own plain look; a code block
// is told apart by its indentation there, and a list hangs as it does with
// colour.
func markdown(text string, w int, lead string) []string {
	width := max(w-ansi.StringWidth(lead), 1)
	styled := lipgloss.ColorProfile() != termenv.Ascii
	var rows []string
	// Lines that are no form wait here and are wrapped together, in the
	// one call wrap makes for a whole text.
	var prose []string
	flush := func() {
		if len(prose) > 0 {
			rows = append(rows, strings.Split(ansi.Wrap(strings.Join(prose, "\n"), width, ""), "\n")...)
			prose = nil
		}
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if in, ch, n, ok := fence(line); ok {
			flush()
			// To the fence that closes it, and to the end of the text
			// when none does: a block still being written is code too.
			end := i + 1
			for end < len(lines) && !closes(lines[end], ch, n) {
				end++
			}
			rows = append(rows, codeRows(lines[i+1:end], in, width)...)
			i = end
			continue
		}
		if in, mark, rest, ok := item(line); ok {
			flush()
			rows = append(rows, hang(in+mark+" ", inline(rest, 0, styled), width)...)
			continue
		}
		if !styled {
			prose = append(prose, line)
			continue
		}
		if rest, ok := heading(line); ok {
			flush()
			rows = append(rows, hang("", inline(rest, strong, true), width)...)
			continue
		}
		if runs := inline(line, 0, true); len(runs) > 1 || len(runs) == 1 && runs[0].kind != 0 {
			flush()
			rows = append(rows, hang("", runs, width)...)
			continue
		}
		prose = append(prose, line)
	}
	flush()
	pad := strings.Repeat(" ", ansi.StringWidth(lead))
	for i := range rows {
		if i == 0 {
			rows[i] = lead + rows[i]
		} else {
			rows[i] = pad + rows[i]
		}
	}
	return rows
}

// fence reports whether line opens a code block: three or more backticks
// or tildes, and after them at most the name of a language. in is how far
// it is indented (a block under a list's item), ch and n the mark and how
// many of it, which the closing fence has to match.
func fence(line string) (in int, ch byte, n int, ok bool) {
	t := strings.TrimLeft(line, " \t")
	if len(t) < 3 || t[0] != '`' && t[0] != '~' {
		return 0, 0, 0, false
	}
	ch, n = t[0], count(t, t[0])
	// Backticks after the fence's own: code within a line, not a block.
	if n < 3 || ch == '`' && strings.IndexByte(t[n:], '`') >= 0 {
		return 0, 0, 0, false
	}
	return ansi.StringWidth(untab(line[:len(line)-len(t)])), ch, n, true
}

// closes reports whether line closes a block that n of ch opened.
func closes(line string, ch byte, n int) bool {
	t := strings.TrimSpace(line)
	return len(t) >= n && count(t, ch) == len(t)
}

// count is how many of ch s begins with.
func count(s string, ch byte) int {
	n := 0
	for n < len(s) && s[n] == ch {
		n++
	}
	return n
}

// untab is s with its tabs as spaces: a tab has no width a terminal and
// this view agree on.
func untab(s string) string { return strings.ReplaceAll(s, "\t", tab) }

// codeRows are the lines of a code block as rows of at most width columns:
// each line a row with its indentation, less that of the fence, and one
// longer than the row broken where the row ends, the rest marked.
func codeRows(lines []string, in, width int) []string {
	in = max(min(in, width-len(codeLead)-1), 0) // a row narrower than the fence is indented
	gap := strings.Repeat(" ", in)
	room := max(width-in-len(codeLead), 1)
	var out []string
	for _, line := range lines {
		line = untab(line)
		line = line[min(in, len(line)-len(strings.TrimLeft(line, " "))):]
		for i, row := range strings.Split(ansi.Hardwrap(line, room, true), "\n") {
			switch {
			case i > 0:
				out = append(out, gap+look.faint.Render(codeMore)+look.code.Render(row))
			case row == "":
				out = append(out, "")
			default:
				out = append(out, gap+codeLead+look.code.Render(row))
			}
		}
	}
	return out
}

// item reports whether line is an item of a list, and gives its
// indentation, its mark as drawn and its text. A bullet is a - or a * and a
// space; a + is none, since a line of a diff begins so. A numbered item
// keeps its number as written.
func item(line string) (in, mark, rest string, ok bool) {
	t := strings.TrimLeft(line, " \t")
	in = untab(line[:len(line)-len(t)])
	n := 0
	for n < len(t) && n < 9 && t[n] >= '0' && t[n] <= '9' {
		n++
	}
	switch {
	case n > 0 && n < len(t) && (t[n] == '.' || t[n] == ')'):
		mark, rest = t[:n+1], t[n+1:]
	case t != "" && (t[0] == '-' || t[0] == '*'):
		mark, rest = bullet, t[1:]
	default:
		return "", "", "", false
	}
	text := strings.TrimLeft(rest, " \t")
	// No space after the mark is no item (-1, *.go, 3.5), and a line of
	// marks alone is a rule (* * *).
	if text == rest || strings.Trim(text, "-*_ \t") == "" {
		return "", "", "", false
	}
	return in, mark, text, true
}

// heading reports whether line is a heading, one to six # and a space at
// its start, and gives its text. The level is not drawn: every heading is
// a bold line of the text's own size.
func heading(line string) (string, bool) {
	t := strings.TrimLeft(line, " ")
	n := count(t, '#')
	if len(line)-len(t) > 3 || n < 1 || n > 6 {
		return "", false
	}
	text := strings.TrimLeft(t[n:], " \t")
	if text == t[n:] || text == "" {
		return "", false
	}
	return text, true
}

// inline is s as runs: code between backticks, bold between ** or __,
// italic between * or _, and the rest as it is, every run of the kind base
// besides its own. With marks false it is s whole, as written.
//
// A mark counts only where it stands as one: at the edge of a word, with
// text right inside it and its partner on the same line. So a_b_c, 2 * 3,
// *.go and src/**/x.go stay what they are.
func inline(s string, base uint8, marks bool) []run {
	if !marks {
		return []run{{s, base}}
	}
	var out []run
	from := 0 // where the text not yet given begins
	text := func(to int) {
		if to > from {
			out = append(out, run{s[from:to], base})
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		if c != '`' && c != '*' && c != '_' {
			i++
			continue
		}
		n := count(s[i:], c)
		if c == '`' {
			if j := ticks(s, i+n, n); j >= 0 {
				text(i)
				out = append(out, run{codeText(s[i+n : j]), base | code})
				i, from = j+n, j+n
				continue
			}
		} else if n <= 3 && opens(s, i, n) {
			if j := partner(s, i+n, c, n); j >= 0 {
				text(i)
				out = append(out, inline(s[i+n:j], base|[]uint8{em, strong, em | strong}[n-1], true)...)
				i, from = j+n, j+n
				continue
			}
		}
		i += n
	}
	text(len(s))
	return out
}

// ticks is where the n backticks stand that close code begun before from,
// or -1.
func ticks(s string, from, n int) int {
	for j := from; j < len(s); {
		if s[j] != '`' {
			j++
			continue
		}
		m := count(s[j:], '`')
		if m == n && j > from {
			return j
		}
		j += m
	}
	return -1
}

// codeText is what stood between backticks, less the one space that sets a
// backtick of the code's own apart from them.
func codeText(s string) string {
	if len(s) > 2 && s[0] == ' ' && s[len(s)-1] == ' ' && strings.TrimSpace(s) != "" {
		return s[1 : len(s)-1]
	}
	return s
}

// opens reports whether the n marks at s[i] open bold or italic: nothing
// before them but a space or an opening bracket or quote, and after them
// text, not a space and not what follows the star of a glob.
func opens(s string, i, n int) bool {
	if i > 0 && !strings.ContainsRune(" \t([{\"'", rune(s[i-1])) {
		return false
	}
	return i+n < len(s) && !strings.ContainsRune(" \t/.,;:!?)]}", rune(s[i+n]))
}

// partner is where the n marks c stand that close what opened before from,
// or -1: text right before them, and after them what ends a word (ends).
// Code on the way is passed over.
func partner(s string, from int, c byte, n int) int {
	for j := from; j < len(s); {
		switch s[j] {
		case '`':
			m := count(s[j:], '`')
			if k := ticks(s, j+m, m); k >= 0 {
				j = k
			}
			j += m
		case c:
			m := count(s[j:], c)
			if m == n && j > from && !strings.ContainsRune(" \t/([{", rune(s[j-1])) && ends(s[j+m:]) {
				return j
			}
			j += m
		default:
			j++
		}
	}
	return -1
}

// ends reports whether rest, what follows closing marks, begins with what
// ends a word: nothing, a space, or punctuation with no letter right after
// it, which __init__.py has.
func ends(rest string) bool {
	if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
		return true
	}
	if !strings.ContainsRune(".,;:!?)]}\"'-", rune(rest[0])) {
		return false
	}
	return len(rest) == 1 || rest[1] < 0x80 && !isWord(rest[1])
}

// isWord reports whether c is a letter or a digit of ASCII.
func isWord(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// hang is runs wrapped to width behind first, the rows after the first
// under the text and not under the mark: an item's hanging indent. Each
// row carries its own styles whole, since a row is joined to others (the
// crew's column) and a style left open would run into them.
func hang(first string, runs []run, width int) []string {
	lead := ansi.StringWidth(first)
	if lead >= width {
		// No room to hang in: the mark is text of the first row.
		runs, first, lead = append([]run{{text: first}}, runs...), "", 0
	}
	var text strings.Builder
	var kinds []uint8 // of each byte of text
	styledAny := false
	for _, r := range runs {
		text.WriteString(r.text)
		for range len(r.text) {
			kinds = append(kinds, r.kind)
		}
		styledAny = styledAny || r.kind != 0
	}
	src := text.String()
	rows := strings.Split(ansi.Wrap(src, max(width-lead, 1), ""), "\n")
	pad := strings.Repeat(" ", lead)
	// A row is the text less the spaces it was broken at, so each of its
	// bytes is found again in the text, in order, and has that one's kind.
	at := 0
	for i, row := range rows {
		if styledAny {
			var b strings.Builder
			start, kind := 0, uint8(0)
			for k := 0; k <= len(row); k++ {
				var now uint8
				if k < len(row) {
					for at < len(src) && src[at] != row[k] {
						at++
					}
					if at < len(src) {
						now = kinds[at]
						at++
					}
				}
				if k == len(row) || now != kind {
					b.WriteString(look.span(kind).Render(row[start:k]))
					start, kind = k, now
				}
			}
			row = b.String()
		}
		if i == 0 {
			rows[i] = first + row
		} else {
			rows[i] = pad + row
		}
	}
	return rows
}

package chat

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"yardr.dev/yardr/internal/store"
)

const (
	// Wide is the width from which the crew are a column on the
	// left. Narrower, they fold into one line on top, and the
	// conversation has the whole width.
	Wide = 100
	// listWidth is that column: a mark, the name, the state and the
	// unread count.
	listWidth = 32
	nameWidth = 11
	// warnAt is the share of a mail's size from which the line above the
	// input counts the bytes.
	warnAt = 3 * store.MaxMailBytes / 4
)

// keys are the keys of the line at the bottom, each with what it does.
var keys = [][2]string{
	{"enter", "send"}, {"^j", "new line"}, {"tab", "next member"},
	{"pgup/pgdn", "scroll"}, {"^g", "pane"}, {"^c", "quit"},
}

// yardKeys are the keys of that line on the yard's row: those that do
// something where there is no line to type in and no pane.
var yardKeys = [][2]string{keys[2], keys[3], keys[5]}

// Keys is that line as text, and YardKeys the one of the yard's row. They
// fit 80 columns.
var (
	Keys     = keysLine(keys, lipgloss.NewStyle(), lipgloss.NewStyle())
	YardKeys = keysLine(yardKeys, lipgloss.NewStyle(), lipgloss.NewStyle())
)

// keysLine is the line of keys, a key in one style and its word in the
// other, as the help under a form tells them apart.
func keysLine(keys [][2]string, key, word lipgloss.Style) string {
	items := make([]string, len(keys))
	for i, k := range keys {
		items[i] = key.Render(k[0]) + " " + word.Render(k[1])
	}
	return strings.Join(items, "  ")
}

// narrow reports whether the crew are folded into a line.
func (m *Model) narrow() bool { return m.width < Wide }

// layout is which of the view's rows a terminal has the height for. One
// too low for them all gives them up in this order until the view fits:
// the keys, the line of notices, the rows of the input down to one, the
// rule above the input, the rule under the crew's line and that line
// last. The title and one line of the conversation stay, and the input's
// last row goes only where there is room for nothing but those two.
type layout struct {
	keys, notice bool
	input        int  // the rows of the line to type in
	rule         bool // the rule above the input, on the yard's row under the notifications
	foldRule     bool // the rule under the crew's line
	fold         bool // the crew's line
}

// fixed is the rows of the layout that are not the conversation's, the
// title among them.
func (l layout) fixed() int {
	n := 1 + l.input
	for _, has := range []bool{l.keys, l.notice, l.rule, l.foldRule, l.fold} {
		if has {
			n++
		}
	}
	return n
}

// layoutOf is the layout of a terminal of height rows, the crew
// folded or not, on the yard's row or a crew member's. The yard's row has no
// input.
func layoutOf(height int, folded, yard bool) layout {
	l := layout{keys: true, notice: true, input: inputRows, rule: true, foldRule: folded, fold: folded}
	if yard {
		l.input = 0
	}
	for l.fixed()+1 > height {
		switch {
		case l.keys:
			l.keys = false
		case l.notice:
			l.notice = false
		case l.input > 1:
			l.input--
		case l.rule:
			l.rule = false
		case l.foldRule:
			l.foldRule = false
		case l.fold:
			l.fold = false
		case l.input > 0:
			l.input = 0
		default:
			return l // the title and the conversation's line: never given up
		}
	}
	return l
}

func (m *Model) layout() layout { return layoutOf(m.height, m.narrow(), m.yard) }

// areaRows is the height between the title and the input: the terminal's
// less the rows of the layout that are not the conversation's.
func (m *Model) areaRows() int {
	return max(m.height-m.layout().fixed(), 1)
}

// bodyRows is the height the conversation has: the area's, less the
// notice of a crew member that waits in its pane and how the last giving
// of a secret ended.
func (m *Model) bodyRows() int {
	w := m.bodyWidth()
	return max(m.areaRows()-len(m.waits(w))-len(m.saidLines(w)), 1)
}

// minBody is how much of the conversation stays in view above the notice
// of a crew member that waits in its pane: the notice gives up the pane's
// lines for it, the oldest first.
const minBody = 3

// waits is the notice above the input for a selected crew member that waits
// in its pane (Waits), as lines of at most w columns, and nothing for any
// other: what yardr knows, the pane's last lines, and the key that goes
// there. It is no mail and nowhere kept: it is there while the crew member
// waits.
func (m *Model) waits(w int) []string {
	r := m.member(m.sel)
	if r == nil || !Waits(*r) {
		return nil
	}
	out := wrap(Known(*r), w, "! ", look.warn)
	how := "opens its pane; answer there and come back"
	if !m.opt.InHerdr || m.opt.Focus == nil {
		how = "names the command that opens its pane; answer there and come back"
	}
	// The key as on the keys' line, the words as there too.
	last := wrap(PaneKey+" "+how, w, "  ")
	for i, l := range last {
		if rest, ok := strings.CutPrefix(l, "  "+PaneKey); ok && i == 0 {
			last[i] = "  " + look.key.Render(PaneKey) + look.word.Render(rest)
		} else {
			last[i] = look.word.Render(l)
		}
	}
	area := m.areaRows()
	var pane []string
	if m.watched.name == r.Name {
		pane = m.watched.lines
	}
	if room := max(area-minBody-len(out)-len(last), 0); len(pane) > room {
		pane = pane[len(pane)-room:]
	}
	for _, l := range pane {
		out = append(out, look.faint.Render(fit("  │ "+l, w)))
	}
	out = append(out, last...)
	// A terminal too low for it all keeps the conversation's last line.
	return out[:min(len(out), max(area-1, 0))]
}

// inert is a pane's text as at most n lines to show, the last that say
// something: text and nothing else. A pane holds what a harness, or
// whatever it ran, printed, and none of it is to reach the terminal of the
// view as more than letters: escape sequences are taken out whole, then
// every control character that is left, and the marks that turn the
// direction of text around.
func inert(text string, n int) []string {
	var out []string
	for _, l := range strings.Split(strings.ToValidUTF8(text, ""), "\n") {
		l = strings.Map(func(r rune) rune {
			switch {
			case r == '\t':
				return ' '
			case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
				return -1
			}
			return r
		}, ansi.Strip(l))
		if l = strings.TrimRight(l, " "); strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out[max(len(out)-n, 0):]
}

// bodyWidth is the width the conversation has.
func (m *Model) bodyWidth() int {
	if m.narrow() {
		return max(m.width, 10)
	}
	return m.width - listWidth - 3
}

// clamp keeps the scroll within the conversation.
func (m *Model) clamp() {
	m.scroll = max(0, min(m.scroll, len(m.lines(m.bodyWidth()))-m.bodyRows()))
}

// lines are the selected conversation as lines of at most w columns: each
// mail's time and sender, its text, the images it carries, and under a mail
// the user sent where it stands.
func (m *Model) lines(w int) []string {
	if m.yard {
		return m.noteLines(w)
	}
	if m.sel == "" {
		return []string{look.faint.Render(fit(fmt.Sprintf("The yard has no crew: %s crew add", m.opt.Yardr), w))}
	}
	// Above the conversation with one of a linked yard: who that is.
	out := m.about(w)
	c := m.convo[m.sel]
	if len(c) == 0 {
		text := fmt.Sprintf("No mail between you and %s yet.", m.sel)
		switch {
		case !m.loaded[m.sel]:
			text = "…"
		case m.sel == m.opt.Expect:
			text = fmt.Sprintf("%s is starting: it reads its role first, and its first message appears here. "+
				"You can write to it already.", m.sel)
			return append(wrap(text, w, "", look.faint), m.questions(w)...)
		}
		return append(append(out, look.faint.Render(fit(text, w))), m.questions(w)...)
	}
	r := m.member(m.sel)
	for i, l := range c {
		if i > 0 {
			out = append(out, "")
		}
		from, text := look.member, l.Text
		switch {
		case l.From == m.opt.Me:
			from = look.own
		case strings.Contains(l.From, "@"):
			// Another yard's text, as a pane's lines and a notification's.
			text = plain(text)
		}
		head := look.faint.Render(clock(l.At)) + "  " + from.Render(store.ViaMark(l.From, l.Via))
		if l.From == m.opt.Me && l.To != m.sel {
			// In the row of a user of this yard, what went to a name
			// trusted as them instead says so.
			head += look.faint.Render(" to " + l.To)
		}
		out = append(out, fit(head, w))
		if text != "" {
			out = append(out, markdown(text, w, "  ")...)
		}
		// The images it carries, by where they are: the view shows none.
		if files := store.FilesLine(m.opt.Home, l.Files); files != "" {
			out = append(out, wrap(files, w, "  ", look.faint)...)
		}
		if l.From != m.opt.Me {
			continue
		}
		// One line for a run of mails that stand alike: under the last
		// read, and under the last of those still waiting.
		if next := m.nextSent(c, i); next == nil || next.Read != l.Read || next.Delivery != l.Delivery {
			style := look.standing(l.Read, r)
			if l.Delivery == store.MailFailed {
				style = look.warn
			}
			out = append(out, wrap(Standing(l, r, m.opt.Yardr), w, "  · ", style)...)
		}
	}
	return append(out, m.questions(w)...)
}

// questions are the selected party's asks addressed to the user, under its
// mail: each a question with its options and its state, answered with
// yardr bead answer, and a secret ask also here (secretCard). Nothing on
// the yard's row, and nothing from anyone else.
func (m *Model) questions(w int) []string {
	if m.yard || m.sel == "" {
		return nil
	}
	var mine []store.ResolvedAsk
	for _, r := range m.asks {
		if r.Note.Author == m.sel && r.Ask.To == m.opt.Me {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	out := []string{"", fmt.Sprintf("questions from %s for you:", m.sel)}
	for _, r := range mine {
		out = append(out, "")
		// An answer from another yard says whose word it is, as mail
		// does; m.as is whom the links trust as a user of this yard.
		for _, line := range m.secretCard(r, r.LinesMarked(m.as)) {
			out = append(out, wrap(line, w, "")...)
		}
	}
	return out
}

// nextSent is the next mail after c[i] that the user sent.
func (m *Model) nextSent(c []store.Letter, i int) *store.Letter {
	for j := i + 1; j < len(c); j++ {
		if c[j].From == m.opt.Me {
			return &c[j]
		}
	}
	return nil
}

// wrap is text as lines of at most w columns behind lead, a line of the
// text staying a line; style, when given, is applied to each.
func wrap(text string, w int, lead string, style ...lipgloss.Style) []string {
	width := max(w-ansi.StringWidth(lead), 1)
	pad := strings.Repeat(" ", ansi.StringWidth(lead))
	var out []string
	for i, line := range strings.Split(ansi.Wrap(strings.TrimRight(text, "\n"), width, ""), "\n") {
		if i == 0 {
			line = lead + line
		} else {
			line = pad + line
		}
		if len(style) > 0 {
			line = style[0].Render(line)
		}
		out = append(out, line)
	}
	return out
}

// fit cuts s to w columns.
func fit(s string, w int) string { return ansi.Truncate(s, max(w, 1), "…") }

// pad fills s with spaces to w columns, cutting what is longer.
func pad(s string, w int) string {
	s = fit(s, w)
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

// column is the yard's row, the crew and those of the linked yards as the
// wide view's left column, one a line. The yard's row has no state: its count stands where
// a crew member's does.
func (m *Model) column() []string {
	yardLabel := m.yardLabel()
	mark, name, unread := "  ", pad(yardLabel, nameWidth), ""
	if m.yard {
		mark, name = look.selected.Render("> "), look.selected.Render(name)
	}
	if n := m.Notifications(); n > 0 {
		unread = look.unread.Render(fmt.Sprint(n))
	}
	out := []string{pad(mark+name+" "+pad("", len(Failed))+" "+unread, listWidth)}
	for _, r := range m.crew {
		mark, name := "  ", pad(r.Name, nameWidth)
		if r.Name == m.sel {
			mark, name = look.selected.Render("> "), look.selected.Render(name)
		}
		unread := ""
		if n := m.Unread(r.Name); n > 0 {
			unread = look.unread.Render(fmt.Sprint(n))
		}
		state := StateOf(r)
		out = append(out, pad(mark+name+" "+look.state(state).Render(pad(state, len(Failed)))+" "+unread, listWidth))
	}
	// One of a linked yard has no state here: the address, which names
	// the yard, has that room too.
	for _, row := range m.Linked() {
		mark, name := "  ", pad(row, nameWidth+1+len(Failed))
		if row == m.sel {
			mark, name = look.selected.Render("> "), look.selected.Render(name)
		}
		unread := ""
		if n := m.Unread(row); n > 0 {
			unread = look.unread.Render(fmt.Sprint(n))
		}
		out = append(out, pad(mark+name+" "+unread, listWidth))
	}
	return out
}

// folded is the yard's row, the crew and those of the linked yards as one
// line of w columns: the selected one in brackets, each crew member with its
// state and, in parentheses, the unread count. When they do not all fit, those before
// the selected one give way first.
func (m *Model) folded(w int) string {
	yard := m.yardLabel()
	if n := m.Notifications(); n > 0 {
		yard += " " + look.unread.Render(fmt.Sprintf("(%d)", n))
	}
	if m.yard {
		yard = look.selected.Render("[") + look.selected.Render(yard) + look.selected.Render("]")
	}
	items := []string{yard}
	sel := 0
	for i, r := range m.crew {
		name, state, open, shut := r.Name, StateOf(r), "", ""
		if r.Name == m.sel {
			name, open, shut, sel = look.selected.Render(name), look.selected.Render("["), look.selected.Render("]"), i+1
		}
		item := open + name + " " + look.state(state).Render(state)
		if n := m.Unread(r.Name); n > 0 {
			item += " " + look.unread.Render(fmt.Sprintf("(%d)", n))
		}
		items = append(items, item+shut)
	}
	for i, name := range m.Linked() {
		open, shut := "", ""
		if name == m.sel {
			open, shut, sel = look.selected.Render("["), look.selected.Render("]"), i+1+len(m.crew)
		}
		item := open + name
		if name == m.sel {
			item = open + look.selected.Render(name)
		}
		if n := m.Unread(name); n > 0 {
			item += " " + look.unread.Render(fmt.Sprintf("(%d)", n))
		}
		items = append(items, item+shut)
	}
	from, lead := 0, ""
	line := strings.Join(items, "  ")
	for ansi.StringWidth(lead+line) > w && from < sel {
		from++
		lead = "… "
		line = strings.Join(items[from:], "  ")
	}
	return fit(lead+line, w)
}

// rule is a line across w columns with text near its right end.
func rule(w int, text string) string {
	if text == "" || ansi.StringWidth(text)+6 > w {
		return look.line.Render(strings.Repeat("─", w))
	}
	return look.line.Render(strings.Repeat("─", w-ansi.StringWidth(text)-4)) + " " + text + " " + look.line.Render("──")
}

// count is what the rule above the line says of the text in it: nothing
// while it is short, its bytes towards one mail's size before that is
// reached, where the eye is when typing, and past it the number of mails
// it will be sent as.
func (m *Model) count() string {
	text := strings.TrimSpace(m.input.Value())
	if n := len(split(text)); n > 1 {
		return look.faint.Render(fmt.Sprintf("%d bytes, sent as %d mails", len(text), n))
	}
	if len(text) >= warnAt {
		return look.warn.Render(fmt.Sprintf("%d of %d bytes", len(text), store.MaxMailBytes))
	}
	return ""
}

// View is the screen: the title, the crew, the conversation with the
// newest mail in view, the line to type in, a line of notices and the
// keys. On the yard's row the notifications stand for the conversation,
// and there is no line to type in. A terminal too low for all of it has
// what its layout keeps.
func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "" // no size yet
	}
	w := max(m.width, 10)
	title := look.title.Render("yardr chat") + look.faint.Render(" · "+m.opt.Me)
	if m.down {
		title += "  " + look.warn.Render("the yardr server is away: waiting for it, nothing here is lost")
	}
	out := []string{fit(title, w)}

	rows, bw := m.bodyRows(), m.bodyWidth()
	all := m.lines(bw)
	end := len(all) - max(0, min(m.scroll, len(all)-rows))
	// The conversation, filled to its height, and under it the notice of a
	// crew member that waits in its pane: right above the input.
	body := make([]string, rows, m.areaRows())
	copy(body, all[max(0, end-rows):end])
	body = append(body, m.waits(bw)...)
	body = append(body, m.saidLines(bw)...)
	rows = len(body)
	lay := m.layout()
	if m.narrow() {
		if lay.fold {
			out = append(out, m.folded(w))
		}
		if lay.foldRule {
			out = append(out, rule(w, ""))
		}
		out = append(out, body...)
	} else {
		col := m.column()
		for i := 0; i < rows; i++ {
			left, right := strings.Repeat(" ", listWidth), ""
			if i < len(col) {
				left = col[i]
			}
			if i < len(body) {
				right = body[i]
			}
			out = append(out, left+look.line.Render(" │ ")+right)
		}
	}

	keys := keys
	switch {
	case m.secret != nil:
		keys = secretKeys
	case m.yard:
		keys = yardKeys
	}
	if lay.rule {
		text := ""
		switch {
		case m.secret != nil:
			text = look.faint.Render("a secret: what you type is not shown")
		case !m.yard:
			text = m.count()
		}
		out = append(out, rule(w, text))
	}
	switch {
	case lay.input == 0:
	case m.secret != nil:
		out = append(out, m.secretView(w, lay.input))
	default:
		out = append(out, m.input.View())
	}

	if lay.notice {
		note := m.notice
		if note == "" {
			if n, from := m.others(); n > 0 {
				note = fmt.Sprintf("%d unread from %s, no crew member: %s inbox", n, strings.Join(from, ", "), m.opt.Yardr)
			}
		}
		out = append(out, fit(note, w))
	}
	if lay.keys {
		out = append(out, fit(keysLine(keys, look.key, look.word), w))
	}
	return strings.Join(out, "\n")
}

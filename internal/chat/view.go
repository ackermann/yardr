package chat

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"yardr.dev/yardr/internal/store"
)

const (
	// Wide is the width from which the residents are a column on the
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

// Keys is the line of keys at the bottom. It fits 80 columns.
const Keys = "enter send  ^j new line  tab next resident  pgup/pgdn scroll  ^g pane  ^c quit"

var (
	dim  = lipgloss.NewStyle().Faint(true)
	bold = lipgloss.NewStyle().Bold(true)
	warn = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

// narrow reports whether the residents are folded into a line.
func (m *Model) narrow() bool { return m.width < Wide }

// bodyRows is the height the conversation has: the terminal's less the
// title, the rule and the input, the line of notices and the keys, and,
// folded, the residents' line and its rule.
func (m *Model) bodyRows() int {
	fixed := 1 + 1 + inputRows + 1 + 1
	if m.narrow() {
		fixed += 2
	}
	return max(m.height-fixed, 1)
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
// mail's time and sender, its text, and under a mail the user sent where
// it stands.
func (m *Model) lines(w int) []string {
	if m.sel == "" {
		return []string{dim.Render(fit(fmt.Sprintf("The yard has no residents: %s resident add", m.opt.Yardr), w))}
	}
	c := m.convo[m.sel]
	if len(c) == 0 {
		text := fmt.Sprintf("No mail between you and %s yet.", m.sel)
		if !m.loaded[m.sel] {
			text = "…"
		}
		return []string{dim.Render(fit(text, w))}
	}
	r := m.resident(m.sel)
	var out []string
	for i, l := range c {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, fit(dim.Render(clock(l.At))+"  "+bold.Render(l.From), w))
		out = append(out, wrap(l.Text, w, "  ")...)
		if l.From != m.opt.Me {
			continue
		}
		// One line for a run of mails that stand alike: under the last
		// read, and under the last of those still waiting.
		if next := m.nextSent(c, i); next == nil || next.Read != l.Read {
			out = append(out, wrap(Standing(l, r, m.opt.Yardr), w, "  · ", dim)...)
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

// column is the residents as the wide view's left column, one a line.
func (m *Model) column() []string {
	var out []string
	for _, r := range m.residents {
		mark, name := "  ", pad(r.Name, nameWidth)
		if r.Name == m.sel {
			mark, name = "> ", bold.Render(name)
		}
		unread := ""
		if n := m.Unread(r.Name); n > 0 {
			unread = bold.Render(fmt.Sprint(n))
		}
		out = append(out, pad(mark+name+" "+pad(StateOf(r), len(Failed))+" "+unread, listWidth))
	}
	return out
}

// folded is the residents as one line of w columns: the selected one in
// brackets, each with its state and, in parentheses, its unread count.
// When they do not all fit, those before the selected one give way first.
func (m *Model) folded(w int) string {
	var items []string
	sel := 0
	for i, r := range m.residents {
		item := r.Name + " " + StateOf(r)
		if n := m.Unread(r.Name); n > 0 {
			item += fmt.Sprintf(" (%d)", n)
		}
		if r.Name == m.sel {
			item, sel = bold.Render("["+item+"]"), i
		}
		items = append(items, item)
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
		return dim.Render(strings.Repeat("─", w))
	}
	return dim.Render(strings.Repeat("─", w-ansi.StringWidth(text)-4)) + " " + text + " " + dim.Render("──")
}

// View is the screen: the title, the residents, the conversation with the
// newest mail in view, the line to type in, a line of notices and the
// keys.
func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "" // no size yet
	}
	w := max(m.width, 10)
	title := bold.Render("yardr chat") + dim.Render(" · "+m.opt.Me)
	if m.down {
		title += "  " + warn.Render("the yardr server is away: waiting for it, nothing here is lost")
	}
	out := []string{fit(title, w)}

	rows, bw := m.bodyRows(), m.bodyWidth()
	all := m.lines(bw)
	end := len(all) - max(0, min(m.scroll, len(all)-rows))
	body := all[max(0, end-rows):end]
	if m.narrow() {
		out = append(out, m.folded(w), rule(w, ""))
		for i := 0; i < rows; i++ {
			line := ""
			if i < len(body) {
				line = body[i]
			}
			out = append(out, line)
		}
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
			out = append(out, left+dim.Render(" │ ")+right)
		}
	}

	// The limit is counted before it is reached, where the eye is when
	// typing.
	count := ""
	if n := len(m.input.Value()); n >= warnAt {
		count = warn.Render(fmt.Sprintf("%d of %d bytes", n, store.MaxMailBytes))
	}
	out = append(out, rule(w, count), m.input.View())

	note := m.notice
	if note == "" {
		if n, from := m.others(); n > 0 {
			note = fmt.Sprintf("%d unread from %s, no resident: %s inbox", n, strings.Join(from, ", "), m.opt.Yardr)
		}
	}
	out = append(out, fit(note, w), dim.Render(fit(Keys, w)))
	return strings.Join(out, "\n")
}

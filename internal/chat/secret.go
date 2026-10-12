package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"yardr.dev/yardr/internal/store"
)

// A secret ask is the one question the view answers itself (docs/chat.md):
// SecretKey replaces the line to type in with a field that shows a dot for
// each character, and enter gives what was typed to the yard's server
// (GiveSecret), which writes it to the file the source's credential is read
// from. What is typed is in that field and in the call, and in nothing the
// view shows: not in the line for mail, not in a notice, not in an error.

// SecretStore is what giving a secret takes of the yard. It is not part of
// Store: that is what a device may be served over the yard's listener, and
// this call is served on the yard's own socket and on nothing else
// (docs/api.md, rpc.LocalCalls), so what is typed here crosses no network
// the socket does not.
type SecretStore interface {
	GiveSecret(ctx context.Context, id string, ask uint64, values map[string]string, actor string) (*store.SourceKeyed, error)
}

// SecretKey is the key that opens the field, as the view names it.
const SecretKey = "ctrl+o"

// NoAnswer is what every client says of a giving that ended without the
// yard's answer: the connection broke, and on which side of the file's
// write is not known. It is never said to be sent.
const NoAnswer = "the yard did not answer. The value may be written. Give it again."

// hidden stands in for a value in a text that is shown.
const hidden = "•••"

// secretField is the open field: the ask it answers, the names it asks
// for one after the other, and what was typed for those before the one in
// the field.
type secretField struct {
	bead   string
	ask    uint64
	source string
	env    []string
	values map[string]string
	in     textinput.Model
}

// name is the variable the field asks for now.
func (s *secretField) name() string { return s.env[len(s.values)] }

// label is what stands before the field: the name asked for now, which of
// them it is where there are several, and the source.
func (s *secretField) label() string {
	label := s.name()
	if len(s.env) > 1 {
		label += fmt.Sprintf(" (%d of %d)", len(s.values)+1, len(s.env))
	}
	return label + " for " + s.source + "  "
}

// fit gives the field the columns its label leaves of w; a value longer
// than that scrolls in them.
func (s *secretField) fit(w int) {
	s.in.Width = max(w-len([]rune(s.label()))-1, 4)
	s.in.SetCursor(s.in.Position())
}

// gaveMsg is how a giving ended, as the text the view shows of it: the
// yard's line, its refusal, or NoAnswer. bad says it was not the first. The
// view keeps the last (Model.said) above the line until the next key that
// does something: whole, on as many lines as it takes, since the end of a
// refusal is where it says what to do.
type gaveMsg struct {
	text string
	bad  bool
}

// secretKeys are the keys of the line at the bottom while the field is
// open.
var secretKeys = [][2]string{{"enter", "send it to the yard"}, {"esc", "leave, nothing is sent"}, {"^c", "quit"}}

// givable is the secret ask of the selected conversation that SecretKey
// answers: the oldest that waits for its value, open or put aside, and
// with none of those the newest that was given, which typing again
// replaces. Nil where the conversation shows none.
func (m *Model) givable() *store.ResolvedAsk {
	var again *store.ResolvedAsk
	for i, r := range m.asks {
		if r.Note.Author != m.sel || r.Ask.To != m.opt.Me || r.Ask.Secret == nil || len(r.Ask.Secret.Env) == 0 {
			continue
		}
		switch r.State {
		case store.AskDeclined:
		case store.AskAnswered:
			again = &m.asks[i]
		default:
			return &m.asks[i]
		}
	}
	return again
}

// secretCard is the card of a secret ask as the view shows it: the yard's
// lines, the last of which names the terminal's command, with the key that
// types the value here before it. A view that cannot give (Options.Secrets)
// leaves the lines as they are.
func (m *Model) secretCard(r store.ResolvedAsk, lines []string) []string {
	if m.opt.Secrets == nil || r.AskErr != nil || r.Ask.Secret == nil || len(lines) == 0 {
		return lines
	}
	if cmd, ok := strings.CutPrefix(lines[len(lines)-1], "  answer with: "); ok {
		lines[len(lines)-1] = "  " + SecretKey + " types it here · or in a terminal: " + cmd
	}
	return lines
}

// ask opens the field for the secret ask the selected conversation shows,
// and says that there is nothing to give where it shows none.
func (m *Model) ask() tea.Cmd {
	m.said = gaveMsg{}
	m.clamp()
	r := m.givable()
	switch {
	case m.yard || m.sel == "":
		m.notice = "nothing to give here: a secret is asked for in a crew member's conversation"
		return nil
	case r == nil:
		m.notice = fmt.Sprintf("nothing to give: %s asks you for no secret", m.sel)
		return nil
	case m.opt.Secrets == nil:
		m.notice = fmt.Sprintf("a secret is given in a terminal here: %s bead answer %s %d --secret", m.opt.Yardr, r.Note.Bead, r.Note.Seq)
		return nil
	}
	in := textinput.New()
	in.Prompt = ""
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	in.Cursor.Style = look.cursor
	m.secret = &secretField{bead: r.Note.Bead, ask: r.Note.Seq, source: r.Ask.Secret.Source,
		env: r.Ask.Secret.Env, values: map[string]string{}, in: in}
	m.secret.fit(max(m.width, 10))
	m.notice = ""
	m.clamp()
	return m.secret.in.Focus()
}

// secretKey takes a key while the field is open. Every key but those that
// leave, send or scroll is the field's: none of them selects another row
// or reaches the line for mail.
func (m *Model) secretKey(k tea.KeyMsg) tea.Cmd {
	s := m.secret
	switch k.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		m.secret = nil
		m.notice = "nothing was sent"
		return nil
	case "pgup":
		m.scroll += max(m.bodyRows()-1, 1)
		m.clamp()
		return nil
	case "pgdown":
		m.scroll -= max(m.bodyRows()-1, 1)
		m.clamp()
		return nil
	case "enter":
		// A pasted value ends in the line break of what it was copied
		// from, which the field keeps as a space; no key begins or ends
		// with one.
		v := strings.TrimSpace(s.in.Value())
		if v == "" {
			m.notice = "nothing typed: esc leaves, and nothing is sent"
			return nil
		}
		s.values[s.name()] = v
		s.in.Reset()
		m.notice = ""
		if len(s.values) < len(s.env) {
			s.fit(max(m.width, 10)) // the next name
			return nil
		}
		return m.give()
	}
	var cmd tea.Cmd
	s.in, cmd = s.in.Update(k)
	m.notice = ""
	return cmd
}

// give sends what was typed to the yard and closes the field, whatever the
// yard answers. What comes back is made the text the view shows here, where
// the values still are: a value the answer repeated is taken out of it, so
// that none reaches the view even from a server that says one back.
func (m *Model) give() tea.Cmd {
	s := m.secret
	m.secret = nil
	st, ctx, me, yardr := m.opt.Secrets, m.ctx, m.opt.Me, m.opt.Yardr
	return func() tea.Msg {
		keyed, err := st.GiveSecret(ctx, s.bead, s.ask, s.values, me)
		switch {
		case errors.Is(err, store.ErrNoAnswer):
			return gaveMsg{text: NoAnswer, bad: true}
		case err != nil:
			return gaveMsg{text: without(err.Error(), s.values), bad: true}
		}
		text := fmt.Sprintf("%s gave %s · it is set in %s", me, strings.Join(keyed.Given, " and "), keyed.File)
		if keyed.Missing != "" {
			text += " · " + keyed.Missing
		}
		text += fmt.Sprintf(" · %s source check %s", yardr, s.source)
		return gaveMsg{text: without(text, s.values)}
	}
}

// without is text with every value of values taken out.
func without(text string, values map[string]string) string {
	for _, v := range values {
		text = strings.ReplaceAll(text, v, hidden)
	}
	return text
}

// saidLines are the last giving's end as lines of at most w columns, above
// the line to type in; a terminal too low for them keeps the
// conversation's last line.
func (m *Model) saidLines(w int) []string {
	if m.said.text == "" {
		return nil
	}
	style := look.read
	if m.said.bad {
		style = look.warn
	}
	// What a server said reaches the terminal as text and nothing else.
	out := wrap(strings.Join(inert(m.said.text, paneRows), " "), w, "", style)
	return out[:min(len(out), max(m.areaRows()-len(m.waits(w))-1, 0))]
}

// secretView is the field as the rows the line to type in has: the name
// asked for and its source, a dot for each character typed, and under them
// what the keys do.
func (m *Model) secretView(w, rows int) string {
	s := m.secret
	out := []string{fit(look.prompt.Render(s.label())+s.in.View(), w),
		look.faint.Render(fit("enter sends it to the yard · esc leaves, and nothing is sent", w))}
	for len(out) < rows {
		out = append(out, "")
	}
	return strings.Join(out[:rows], "\n")
}

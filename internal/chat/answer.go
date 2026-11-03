package chat

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"yardr.dev/yardr/internal/store"
)

// A question is answered from the view with AnswerKey (docs/chat.md): it
// replaces the line to type in with a field, in the place and the shape of
// the secret's (secret.go), and enter gives what was typed to the yard as
// yardr bead answer does: a pick with any words, words alone, or the
// approval of a run ask. The view adds no rule to the yard's, and shows what
// the yard refuses in the yard's words.
//
// What is typed in the line for mail stays mail, a bare number included: a
// mail can be "1", and an answer is a deliberate act, so nothing in the line
// is ever read as one. Putting an ask aside and declining it are flags of the
// command and no card's answers, so they stay there; a secret has SecretKey;
// and a setup ask takes its account with the pick, which is typed in a
// terminal.

// AnswerStore is what answering an ask takes of the yard. It is not part of
// Store, as SecretStore is not: ApproveAsk is the operator's alone to make
// (docs/api.md, rpc.OperatorCalls), and a view that was given no AnswerStore
// leaves every ask to the terminal, as its card then says. AnswerAsk is a
// pick or words, and ApproveAsk the approval of a run ask, which AnswerAsk
// refuses: the two calls yardr bead answer makes, on the yard's own socket.
type AnswerStore interface {
	AnswerAsk(ctx context.Context, id string, ask uint64, option int, words, actor string, weighed bool) error
	ApproveAsk(ctx context.Context, id string, ask uint64, actor string, weighed bool, values map[string]string) (*store.AskApproved, error)
}

// AnswerKey is the key that opens the field, as the view names it. It is
// neither the line's (the textarea's map holds ctrl+a for the line's start,
// and no ctrl+r) nor another of the view's, and no terminal takes it for a
// flow control or a suspend.
const AnswerKey = "ctrl+r"

// answerField is the open field: the asks it offers, the one it answers once
// that is known, and the box a number or words are typed in.
type answerField struct {
	// among are the asks the field offers, oldest first. While it holds
	// several and none is chosen, the field asks for the number of one.
	among []store.ResolvedAsk
	// on is the ask the field answers, nil while it asks which.
	on *store.ResolvedAsk
	in textinput.Model
}

// choose makes the ask i of among the one the field answers.
func (f *answerField) choose(i int) { f.on = &f.among[i] }

// approves says the ask answered is a run ask. Nothing is typed for it but
// its number: a command that runs is approved by a person who typed the number
// of the ask back, whatever the card weighs.
func (f *answerField) approves() bool { return f.on != nil && f.on.Ask.Run != nil }

// numbers are the asks the field asks the number of, the first few where
// there are many: the label must leave room for what is typed after it, and
// the cards above the line print every number.
func (f *answerField) numbers() string {
	const most = 6
	var nums []string
	for _, r := range f.among[:min(len(f.among), most)] {
		nums = append(nums, strconv.FormatUint(r.Note.Seq, 10))
	}
	if len(f.among) > most {
		nums = append(nums, "…")
	}
	return strings.Join(nums, ", ")
}

// label is what stands before the field: which ask is asked for, or the ask
// answered and what it takes.
func (f *answerField) label() string {
	switch {
	case f.on == nil:
		return "answer which ask: " + f.numbers() + "  "
	case f.approves():
		weight := ""
		if len(f.on.Ask.Weight) > 0 {
			weight = ", the card is weighty"
		}
		return fmt.Sprintf("type %d to approve it%s  ", f.on.Note.Seq, weight)
	case len(f.on.Ask.Options) > 0:
		pick := "1"
		if n := len(f.on.Ask.Options); n > 1 {
			pick = fmt.Sprintf("1-%d", n)
		}
		return fmt.Sprintf("ask %d: pick %s [words]  ", f.on.Note.Seq, pick)
	}
	return fmt.Sprintf("ask %d: words  ", f.on.Note.Seq)
}

// hint is what enter does, under the field.
func (f *answerField) hint() string {
	switch {
	case f.on == nil:
		return "enter goes to that ask · esc leaves, and nothing is sent"
	case f.approves():
		return "enter approves it · esc leaves, and nothing is sent"
	}
	return "enter answers the ask · esc leaves, and nothing is sent"
}

// rule is what the rule above the field says: of a card that weighs, that the
// number shows as it is typed, which is the secret's gate over again.
func (f *answerField) rule() string {
	if f.approves() && len(f.on.Ask.Weight) > 0 {
		return "a weighty card: its number shows as you type it"
	}
	return ""
}

// keys are the keys of the line at the bottom while the field is open.
func (f *answerField) keys() [][2]string {
	enter := "answer the ask"
	switch {
	case f.on == nil:
		enter = "go to that ask"
	case f.approves():
		enter = "approve it"
	}
	return [][2]string{{"enter", enter}, {"esc", "leave, nothing is sent"}, {"^c", "quit"}}
}

// fit gives the field the columns its label leaves of w; what is typed
// longer than that scrolls in them.
func (f *answerField) fit(w int) {
	f.in.Width = max(w-len([]rune(f.label()))-1, 4)
	f.in.SetCursor(f.in.Position())
}

// stands says the ask can still be answered: it is open, or put aside, which
// an answer ends early.
func stands(r store.ResolvedAsk) bool {
	return r.AskErr == nil && (r.State == store.AskOpen || r.State == store.AskAside)
}

// offered says the field answers the ask. It leaves out what another key or
// the terminal answers: a secret (SecretKey), a setup (its pick takes the
// account typed with it), a depot-push approval, and a run ask whose card
// asks for values in the clear, which the field has no place for.
func offered(r store.ResolvedAsk) bool {
	a := r.Ask
	switch {
	case !stands(r), a.Secret != nil, a.Setup != nil, a.DepotPush != nil:
		return false
	case a.Run != nil:
		return len(a.Run.Ask) == 0
	}
	return true
}

// answerable are the asks of the selected conversation that AnswerKey
// answers, oldest first, and whether the conversation shows any ask that
// stands, so that the notice can say where an ask the field leaves is
// answered.
func (m *Model) answerable() (among []store.ResolvedAsk, standing bool) {
	for _, r := range m.asks {
		if r.Note.Author != m.sel || r.Ask.To != m.opt.Me || !stands(r) {
			continue
		}
		standing = true
		if offered(r) {
			among = append(among, r)
		}
	}
	return among, standing
}

// answerCard is the card of an ask the field answers as the view shows it:
// the yard's lines, the last of which names the terminal's command, with the
// key that answers it here before it. A view that cannot answer
// (Options.Answers) leaves the lines as they are. It changes that last line
// alone, as secretCard does, so the WEIGHTY lines after the head stay where
// questions looks for them; a card whose last line is anything but the yard's
// command (an answered one, a declined one) is not changed.
func (m *Model) answerCard(r store.ResolvedAsk, lines []string) []string {
	if m.opt.Answers == nil || !offered(r) || len(lines) == 0 {
		return lines
	}
	last := len(lines) - 1
	if cmd, ok := strings.CutPrefix(lines[last], "  answer with: "); ok {
		lines[last] = "  " + AnswerKey + " answers it here · or in a terminal: " + cmd
	} else if cmd, ok := strings.CutPrefix(lines[last], "  approve with: "); ok {
		lines[last] = "  " + AnswerKey + " approves it here · or in a terminal: " + cmd
	}
	return lines
}

// answer opens the field for the asks the selected conversation shows that
// it answers, and says why where there are none.
func (m *Model) answer() tea.Cmd {
	m.said = gaveMsg{}
	m.clamp()
	among, standing := m.answerable()
	switch {
	case m.yard || m.sel == "":
		m.notice = "nothing to answer here: a question is answered in a crew member's conversation"
		return nil
	case m.opt.Answers == nil:
		m.notice = fmt.Sprintf("a question is answered in a terminal here: %s bead answer <bead> <ask> <n>, as its card says", m.opt.Yardr)
		return nil
	case standing && len(among) == 0:
		m.notice = fmt.Sprintf("nothing to answer here: what %s waits on is answered as its card says (%s gives a secret)", m.sel, SecretKey)
		return nil
	case len(among) == 0:
		m.notice = fmt.Sprintf("nothing to answer: %s asks you nothing that waits", m.sel)
		return nil
	}
	in := textinput.New()
	in.Prompt = ""
	in.Cursor.Style = look.cursor
	f := &answerField{among: among, in: in}
	if len(among) == 1 {
		f.choose(0)
	}
	f.fit(max(m.width, 10))
	m.answering = f
	m.notice = ""
	m.clamp()
	return f.in.Focus()
}

// answerKey takes a key while the field is open. Every key but those that
// leave, send or scroll is the field's: none of them selects another row or
// reaches the line for mail.
func (m *Model) answerKey(k tea.KeyMsg) tea.Cmd {
	f := m.answering
	switch k.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc":
		m.answering = nil
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
		return m.answered()
	}
	var cmd tea.Cmd
	f.in, cmd = f.in.Update(k)
	m.notice = ""
	return cmd
}

// answered is enter in the field: it goes on to the ask a number chose, and
// gives the yard what was typed for the ask once there is one. A number that
// names none of the asks, and a pick that is no number, leave the field as it
// is with a notice, since nothing was sent; a wrong number at an approval's
// gate closes it, as the secret's does. What the yard refuses is the yard's to
// say, and closes it too (answerWith).
func (m *Model) answered() tea.Cmd {
	f := m.answering
	typed := strings.TrimSpace(f.in.Value())
	if typed == "" {
		m.notice = "nothing typed: esc leaves, and nothing is sent"
		return nil
	}
	switch {
	case f.on == nil:
		seq, err := strconv.ParseUint(typed, 10, 64)
		i := slices.IndexFunc(f.among, func(r store.ResolvedAsk) bool { return r.Note.Seq == seq })
		if err != nil || i < 0 {
			m.notice = fmt.Sprintf("%q is none of the asks to answer: %s", typed, f.numbers())
			return nil
		}
		f.choose(i)
		f.in.Reset()
		f.fit(max(m.width, 10))
		m.notice = ""
		return nil
	case f.approves():
		if number := strconv.FormatUint(f.on.Note.Seq, 10); typed != number {
			m.answering = nil
			m.notice = fmt.Sprintf("%s was not typed back: nothing was sent", number)
			return nil
		}
		return m.approveWith(*f.on)
	case len(f.on.Ask.Options) > 0:
		first := strings.Fields(typed)[0]
		n, err := strconv.Atoi(first)
		if err != nil || n < 1 {
			m.notice = fmt.Sprintf("%q is no option number: type 1 to %d, then words if you like", first, len(f.on.Ask.Options))
			return nil
		}
		return m.answerWith(*f.on, n, strings.TrimSpace(strings.TrimPrefix(typed, first)))
	}
	return m.answerWith(*f.on, 0, typed)
}

// answerWith sends the pick, with any words, or the words alone, to the yard as
// the user, and closes the field whatever the yard answers. It is the call
// yardr bead answer makes, with the same arguments: the pick, the words, the
// user, and no word of weight. Only a pick on a mapping card that writes an
// identity is weighty, and the yard writes those cards (no conversation shows
// one), so the yard would refuse such a pick in its own words.
func (m *Model) answerWith(r store.ResolvedAsk, option int, words string) tea.Cmd {
	m.answering = nil
	st, ctx, me := m.opt.Answers, m.ctx, m.opt.Me
	return func() tea.Msg {
		if err := st.AnswerAsk(ctx, r.Note.Bead, r.Note.Seq, option, words, me, false); err != nil {
			return gaveMsg{text: err.Error(), bad: true}
		}
		if option == 0 {
			return gaveMsg{text: fmt.Sprintf("%s answered ask %d", me, r.Note.Seq)}
		}
		return gaveMsg{text: fmt.Sprintf("%s picked %d on ask %d", me, option, r.Note.Seq)}
	}
}

// approveWith sends the approval of a run ask to the yard as the user, and closes
// the field whatever the yard answers. The number was typed back to get here,
// which is the word that its person saw the card's weight (weighed), and
// nothing else sends it. What the command printed is on the card once the
// asks are read again (Update, gaveMsg).
func (m *Model) approveWith(r store.ResolvedAsk) tea.Cmd {
	m.answering = nil
	st, ctx, me := m.opt.Answers, m.ctx, m.opt.Me
	return func() tea.Msg {
		done, err := st.ApproveAsk(ctx, r.Note.Bead, r.Note.Seq, me, true, nil)
		if err != nil {
			return gaveMsg{text: err.Error(), bad: true}
		}
		text := fmt.Sprintf("%s approved ask %d", me, r.Note.Seq)
		if done != nil {
			text += ", and the yard ran it: " + store.AskRan{Argv: done.Argv}.Command()
		}
		return gaveMsg{text: text}
	}
}

// answerView is the field as the rows the line to type in has: the ask
// asked for or answered and what it takes, what is typed, and under them what
// the keys do.
func (m *Model) answerView(w, rows int) string {
	f := m.answering
	out := []string{fit(look.prompt.Render(f.label())+f.in.View(), w), look.faint.Render(fit(f.hint(), w))}
	for len(out) < rows {
		out = append(out, "")
	}
	return strings.Join(out[:rows], "\n")
}

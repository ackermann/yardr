// Package chat is the view behind yardr chat: the yard's residents, each
// with what its session is doing and how much of its mail is unread, the
// conversation with the selected one, and a line to write to it
// (docs/chat.md).
//
// It shows what the store keeps and sends through it: a conversation is
// store.API's Conversation, a line sent is its Mail, and what changes
// afterwards (mail, mail being read, a session starting, stopping or
// changing what it does) comes from the event stream (Follow), never from
// asking again in a loop. The model here knows no terminal: Update takes
// messages and View returns text, which is how it is tested.
package chat

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"yardr.dev/yardr/internal/store"
)

// Store is what the view asks of the yard; store.API has it all.
type Store interface {
	Head(ctx context.Context) (uint64, error)
	Residents(ctx context.Context) ([]store.Resident, error)
	GetUser(ctx context.Context, name string) (*store.User, error)
	UserInbox(ctx context.Context, name string, limit int) (*store.Mailbox, error)
	AckUserInbox(ctx context.Context, name string, through uint64) error
	Conversation(ctx context.Context, a, b string, after uint64) ([]store.Letter, error)
	Mail(ctx context.Context, to, text, actor string) error
}

// Options are what the command knows and the model cannot find out.
type Options struct {
	// Me is the user the view is for: the sender of what is typed, and
	// the one whose mail opening a conversation acknowledges.
	Me string
	// Select is the resident selected first; "" or one the yard does not
	// have selects the first of the list.
	Select string
	// Yardr is the command's name in the hints the view gives.
	Yardr string
	// InHerdr says the view runs in a pane of herdr, where Focus brings
	// the pane of the agent named to the front. Anywhere else the key for
	// it names the herdr command instead.
	InHerdr bool
	Focus   func(ctx context.Context, agent string) error
}

// State is what the view starts from, read once (Load).
type State struct {
	// Head is the newest event when the rest was read; the stream is
	// followed from after it. What happens while the rest is read comes
	// again from the stream, and applying it twice changes nothing.
	Head      uint64
	Residents []store.Resident
	// Cursor is how far the user's mail is acknowledged, and Unread the
	// mail after it.
	Cursor uint64
	Unread []store.Letter
}

// Load reads the state a view for the user me starts from.
func Load(ctx context.Context, st Store, me string) (*State, error) {
	head, err := st.Head(ctx)
	if err != nil {
		return nil, err
	}
	s := &State{Head: head}
	if s.Residents, err = st.Residents(ctx); err != nil {
		return nil, err
	}
	u, err := st.GetUser(ctx, me)
	if err != nil {
		return nil, err
	}
	s.Cursor = u.Cursor
	// The count first, then that many: a user's inbox lists the unread
	// before the read, and the read are not wanted here.
	box, err := st.UserInbox(ctx, me, 1)
	if err != nil {
		return nil, err
	}
	if box.Unread > 1 {
		if box, err = st.UserInbox(ctx, me, box.Unread); err != nil {
			return nil, err
		}
	}
	for _, l := range box.Mail {
		if !l.Read {
			s.Unread = append(s.Unread, l)
		}
	}
	return s, nil
}

// EventMsg is an event of the yard's stream, and StreamMsg says the stream
// was lost (Up false) or is back: what Follow hands the program.
type (
	EventMsg  store.Event
	StreamMsg struct{ Up bool }
)

type (
	convoMsg struct {
		name    string
		letters []store.Letter
		err     error
	}
	residentsMsg struct {
		head      uint64
		residents []store.Resident
		err       error
	}
	// sentMsg is how a send ended: sent of the text's of mails went to
	// to, and when err stopped it, rest is the text that did not.
	sentMsg struct {
		to       string
		sent, of int
		rest     string
		err      error
	}
	ackedMsg struct {
		through uint64
		err     error
	}
	focusMsg struct {
		name string
		err  error
	}
)

// waiting is one unread mail to the user: who sent it, and whether the
// view has shown it (its conversation was open).
type waiting struct {
	from string
	seen bool
}

// inputRows is the height of the line to type in.
const inputRows = 3

// Model is the view's state. It is a tea.Model.
type Model struct {
	ctx context.Context
	st  Store
	opt Options

	residents []store.Resident
	sel       string // the selected resident's name
	// statusAt is the seq of the resident_status event a resident's
	// status was last taken from: a list of the residents read before it
	// does not put an older status back.
	statusAt map[string]uint64

	// convo is the mail between the user and each party, oldest first,
	// from the store once the conversation was opened (loaded) and from
	// the stream since.
	convo  map[string][]store.Letter
	loaded map[string]bool

	// unread is the user's unread mail by seq. cursor is how far the
	// store has it acknowledged, asked how far the view has asked for.
	unread map[uint64]*waiting
	cursor uint64
	asked  uint64

	down   bool // the stream is lost
	input  textarea.Model
	width  int
	height int
	scroll int // lines the conversation is scrolled up from its end
	notice string
}

// New is the view for the user opt.Me over what Load read.
func New(ctx context.Context, st Store, opt Options, s *State) *Model {
	if opt.Yardr == "" {
		opt.Yardr = "yardr"
	}
	m := &Model{ctx: ctx, st: st, opt: opt, residents: s.Residents, cursor: s.Cursor, asked: s.Cursor,
		statusAt: map[string]uint64{}, convo: map[string][]store.Letter{}, loaded: map[string]bool{},
		unread: map[uint64]*waiting{}}
	for _, l := range s.Unread {
		m.unread[l.Seq] = &waiting{from: l.From}
	}
	in := textarea.New()
	in.Prompt = "> "
	in.ShowLineNumbers = false
	in.CharLimit = 0 // the limit is in mails, kept in Update
	in.MaxHeight = 0 // and it is the only one: a long text has many lines
	in.SetHeight(inputRows)
	in.FocusedStyle.CursorLine = lipgloss.NewStyle()
	look.input(&in)
	// Enter sends, so a line break has keys of its own: ctrl+j is a line
	// feed in every terminal, alt+enter is what most also pass on.
	in.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "alt+enter"))
	in.Focus()
	m.input = in
	if m.resident(opt.Select) != nil {
		m.sel = opt.Select
	} else if len(m.residents) > 0 {
		m.sel = m.residents[0].Name
	}
	m.opened()
	return m
}

// Init starts the cursor and reads the first conversation.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.open())
}

// resident is the resident name, nil when the yard has none of it.
func (m *Model) resident(name string) *store.Resident {
	for i := range m.residents {
		if m.residents[i].Name == name {
			return &m.residents[i]
		}
	}
	return nil
}

// Selected is the selected resident's name, "" in a yard without any.
func (m *Model) Selected() string { return m.sel }

// Unread is how many mails from name the user has not seen.
func (m *Model) Unread(name string) int {
	n := 0
	for _, w := range m.unread {
		if w.from == name && !w.seen {
			n++
		}
	}
	return n
}

// others are the senders of unread mail who are no residents, in name
// order, and how many mails that is: the view shows residents only, so it
// says where the rest is read.
func (m *Model) others() (int, []string) {
	n, from := 0, []string{}
	for _, w := range m.unread {
		if m.resident(w.from) != nil {
			continue
		}
		n++
		if !slices.Contains(from, w.from) {
			from = append(from, w.from)
		}
	}
	slices.Sort(from)
	return n, from
}

// The states a resident is listed with.
const (
	NotRunning = "not running"
	Working    = "working"
	Idle       = "idle"
	Blocked    = "blocked"
	Failed     = "failed to start"
	// Running is a session of which the daemon has not said more yet, or
	// whose runtime cannot tell.
	Running = "running"
)

// StateOf is the state r is listed with: whether its session runs, and
// what the daemon last saw it do. A resident whose last start failed is
// failed to start, parked for good or waiting for the next try.
func StateOf(r store.Resident) string {
	switch {
	case r.State == "failed" || (!r.Live() && r.Enabled && r.Failures > 0):
		return Failed
	case !r.Live():
		return NotRunning
	}
	switch r.Status {
	case "working":
		return Working
	case "idle":
		return Idle
	case "blocked":
		return Blocked
	}
	return Running
}

// PaneKey is the key that goes to the selected resident's pane, as the
// view names it in a sentence.
const PaneKey = "ctrl+g"

// Standing is where a mail the user sent stands: read, or what its
// receiver's state means for it. r is nil for a receiver the yard no
// longer has.
func Standing(l store.Letter, r *store.Resident, yardr string) string {
	switch {
	case l.Read:
		return "read"
	case r == nil:
		return "sent"
	}
	n := r.Name
	switch StateOf(*r) {
	case Working:
		return fmt.Sprintf("sent; %s is working and reads mail when idle", n)
	case Idle:
		return fmt.Sprintf("sent; %s is idle", n)
	case Blocked:
		return fmt.Sprintf("%s waits at a prompt in its pane and cannot read mail (%s goes there)", n, PaneKey)
	case NotRunning:
		return fmt.Sprintf("%s is not running: %s resident start %s", n, yardr, n)
	case Failed:
		return fmt.Sprintf("%s failed to start: %s resident show %s", n, yardr, n)
	}
	return "sent"
}

// Update takes a key, a size, an event of the stream or the answer to
// something the view asked of the store.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(msg.Width, 10))
		m.clamp()
		return m, nil
	case tea.KeyMsg:
		return m, m.key(msg)
	case EventMsg:
		return m, m.event(store.Event(msg))
	case StreamMsg:
		m.down = !msg.Up
		return m, nil
	case convoMsg:
		if msg.err != nil {
			m.notice = fmt.Sprintf("the conversation with %s could not be read: %v", msg.name, msg.err)
			return m, nil
		}
		m.loaded[msg.name] = true
		for _, l := range msg.letters {
			m.add(msg.name, l)
		}
		m.clamp()
		return m, m.ack()
	case residentsMsg:
		if msg.err != nil {
			m.notice = fmt.Sprintf("the residents could not be read: %v", msg.err)
			return m, nil
		}
		return m, m.listed(msg.head, msg.residents)
	case sentMsg:
		if msg.err != nil {
			m.notice = fmt.Sprintf("not sent to %s: %v", msg.to, msg.err)
			if msg.of > 1 {
				m.notice = fmt.Sprintf("%d of %d mails went to %s, the rest is in the line: %v", msg.sent, msg.of, msg.to, msg.err)
			}
			// Nothing typed is lost to a failed send: what did not go is
			// back in the line, before what was typed since.
			if typed := m.input.Value(); typed != "" {
				msg.rest += "\n\n" + typed
			}
			m.input.SetValue(msg.rest)
			if msg.sent == 0 {
				return m, nil
			}
		}
		// The mail also comes from the stream. Reading it here shows it
		// when the stream is lost at this moment, and costs what is new.
		return m, m.read(msg.to)
	case ackedMsg:
		if msg.err != nil {
			m.asked = m.cursor
			m.notice = fmt.Sprintf("your mail could not be marked read: %v", msg.err)
			return m, nil
		}
		m.acked(msg.through)
		return m, nil
	case focusMsg:
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m.notice = fmt.Sprintf("herdr shows %s now; this view goes on in its own pane", msg.name)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "ctrl+c":
		// Leaving ends the view and nothing else: the residents are the
		// daemon's to keep.
		return tea.Quit
	case "tab":
		return m.step(1)
	case "shift+tab":
		return m.step(-1)
	case "pgup":
		m.scroll += max(m.bodyRows()-1, 1)
		m.clamp()
		return nil
	case "pgdown":
		m.scroll -= max(m.bodyRows()-1, 1)
		m.clamp()
		return nil
	case "ctrl+g":
		return m.pane()
	case "enter":
		return m.send()
	}
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if now := m.input.Value(); now == before {
		return cmd
	} else if len(split(strings.TrimSpace(now))) > MaxParts {
		// Refused whole: half a pasted text is not what was meant.
		m.input.SetValue(before)
		m.notice = fmt.Sprintf("a text is sent as at most %d mails (about %d KB): send this one and go on in the next",
			MaxParts, MaxParts*store.MaxMailBytes>>10)
		return cmd
	}
	m.notice = ""
	return cmd
}

// step selects the resident by residents further down the list, around
// its ends.
func (m *Model) step(by int) tea.Cmd {
	n := len(m.residents)
	if n < 2 {
		return nil
	}
	i := slices.IndexFunc(m.residents, func(r store.Resident) bool { return r.Name == m.sel })
	m.sel = m.residents[((i+by)%n+n)%n].Name
	m.notice = ""
	m.opened()
	return m.open()
}

// opened is what selecting a resident does to the view itself: its
// conversation is shown from its end, and the unread mail in it is seen.
func (m *Model) opened() {
	m.scroll = 0
	if m.sel != "" {
		m.input.Placeholder = "Write to " + m.sel
	}
	for _, w := range m.unread {
		if w.from == m.sel {
			w.seen = true
		}
	}
}

// open reads the selected conversation if it was not read before, and
// acknowledges what opening it showed.
func (m *Model) open() tea.Cmd {
	if m.sel == "" {
		return nil
	}
	if m.loaded[m.sel] {
		return m.ack()
	}
	return tea.Batch(m.read(m.sel), m.ack())
}

// read asks the store for the conversation with name after the last mail
// the view has of it.
func (m *Model) read(name string) tea.Cmd {
	var after uint64
	if c := m.convo[name]; m.loaded[name] && len(c) > 0 {
		after = c[len(c)-1].Seq
	}
	st, ctx, me := m.st, m.ctx, m.opt.Me
	return func() tea.Msg {
		letters, err := st.Conversation(ctx, me, name, after)
		return convoMsg{name: name, letters: letters, err: err}
	}
}

// add puts l into the conversation with name, in order; a mail it has
// already (the stream and the store both tell of each) only gains what l
// knows more: that it was read.
func (m *Model) add(name string, l store.Letter) {
	c := m.convo[name]
	i, found := slices.BinarySearchFunc(c, l.Seq, func(have store.Letter, seq uint64) int {
		switch {
		case have.Seq < seq:
			return -1
		case have.Seq > seq:
			return 1
		}
		return 0
	})
	if found {
		c[i].Read = c[i].Read || l.Read
		return
	}
	m.convo[name] = slices.Insert(c, i, l)
	if name == m.sel {
		m.scroll = 0 // the newest in view
	}
}

// ack acknowledges the user's mail as far as the view has shown it: through
// the newest seen mail before the oldest that was not. A user's inbox has
// one position for all its senders, so mail of a conversation that was not
// opened holds back what came after it; it is never marked read unseen.
func (m *Model) ack() tea.Cmd {
	seqs := make([]uint64, 0, len(m.unread))
	for seq := range m.unread {
		seqs = append(seqs, seq)
	}
	slices.Sort(seqs)
	var through uint64
	for _, seq := range seqs {
		if !m.unread[seq].seen {
			break
		}
		through = seq
	}
	if through <= m.asked {
		return nil
	}
	m.asked = through
	st, ctx, me := m.st, m.ctx, m.opt.Me
	return func() tea.Msg {
		return ackedMsg{through: through, err: st.AckUserInbox(ctx, me, through)}
	}
}

// acked takes the user's mail through seq as read, by this view or by
// anything else that acknowledged it (yardr inbox --ack).
func (m *Model) acked(through uint64) {
	m.cursor = max(m.cursor, through)
	m.asked = max(m.asked, m.cursor)
	for seq := range m.unread {
		if seq <= through {
			delete(m.unread, seq)
		}
	}
}

// send mails what is typed to the selected resident: as one mail when one
// holds it, else as its parts, in order, each numbered (split). A part that
// is not taken ends the send, so that no later part arrives without it.
func (m *Model) send() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	to := m.sel
	if text == "" || to == "" {
		return nil
	}
	m.input.Reset()
	m.notice = ""
	st, ctx, me := m.st, m.ctx, m.opt.Me
	parts := split(text)
	return func() tea.Msg {
		for i, p := range parts {
			if err := st.Mail(ctx, to, p.mail(i, len(parts)), me); err != nil {
				return sentMsg{to: to, sent: i, of: len(parts), rest: text[p.at:], err: err}
			}
		}
		return sentMsg{to: to, sent: len(parts), of: len(parts)}
	}
}

// pane goes to the selected resident's pane: in herdr it is brought to
// the front, elsewhere the view says which herdr command opens it.
func (m *Model) pane() tea.Cmd {
	r := m.resident(m.sel)
	if r == nil {
		return nil
	}
	agent, _ := r.Handle["agent"].(string)
	if !r.Live() || agent == "" {
		m.notice = fmt.Sprintf("%s has no pane, it is not running: %s resident start %s", r.Name, m.opt.Yardr, r.Name)
		return nil
	}
	if !m.opt.InHerdr || m.opt.Focus == nil {
		m.notice = fmt.Sprintf("%s's pane: herdr agent attach %s (in herdr: herdr agent focus %s)", r.Name, agent, agent)
		return nil
	}
	focus, ctx, name := m.opt.Focus, m.ctx, r.Name
	return func() tea.Msg { return focusMsg{name: name, err: focus(ctx, agent)} }
}

// event applies one event of the stream.
func (m *Model) event(e store.Event) tea.Cmd {
	switch {
	case e.Kind == "mail":
		return m.mail(e)
	case e.Kind == "mail_read":
		reader, _ := e.Data["reader"].(string)
		through := number(e.Data["through"])
		if reader == m.opt.Me {
			m.acked(through)
			return nil
		}
		c := m.convo[reader]
		for i := range c {
			if c[i].To == reader && c[i].Seq <= through {
				c[i].Read = true
			}
		}
	case e.Kind == "resident_status":
		name, _ := e.Data["resident"].(string)
		status, _ := e.Data["status"].(string)
		if r := m.resident(name); r != nil && r.Live() {
			r.Status = status
			m.statusAt[name] = e.Seq
		}
	case strings.HasPrefix(e.Kind, "resident_"):
		// A session started, stopped or failed to start, or the list
		// itself changed: one read of the residents for the event, since
		// what they now are is more than the event carries.
		st, ctx := m.st, m.ctx
		return func() tea.Msg {
			head, err := st.Head(ctx)
			if err != nil {
				return residentsMsg{err: err}
			}
			rs, err := st.Residents(ctx)
			return residentsMsg{head: head, residents: rs, err: err}
		}
	}
	return nil
}

// mail takes a mail event: one the user sent, or one to the user. Mail
// that left for another yard, or came from one, is no conversation here.
func (m *Model) mail(e store.Event) tea.Cmd {
	if _, left := e.Data["to_peer"]; left {
		return nil
	}
	if _, came := e.Data["from_peer"]; came {
		return nil
	}
	to, _ := e.Data["to"].(string)
	text, _ := e.Data["text"].(string)
	toUser, _ := e.Data["to_user"].(bool)
	l := store.Letter{Seq: e.Seq, At: e.At, From: e.Actor, To: to, Text: text}
	me := m.opt.Me
	switch {
	case e.Actor == me:
		m.add(to, l)
	case to == me && toUser:
		l.Read = e.Seq <= m.cursor
		m.add(e.Actor, l)
		if !l.Read && m.unread[e.Seq] == nil {
			// In the open conversation it is seen as it arrives.
			m.unread[e.Seq] = &waiting{from: e.Actor, seen: e.Actor == m.sel}
			return m.ack()
		}
	}
	return nil
}

// listed takes the residents as read at head. A status the stream told
// of since is newer than the read and stays. When the selected resident
// is gone the first is selected.
func (m *Model) listed(head uint64, rs []store.Resident) tea.Cmd {
	for i := range rs {
		if old := m.resident(rs[i].Name); old != nil && rs[i].Live() && m.statusAt[rs[i].Name] > head {
			rs[i].Status = old.Status
		}
	}
	m.residents = rs
	if m.resident(m.sel) != nil {
		return nil
	}
	m.sel = ""
	if len(rs) > 0 {
		m.sel = rs[0].Name
	}
	m.opened()
	return m.open()
}

// number is a seq from an event's data: a uint64 from the store itself,
// a float64 once the event came over the socket as JSON.
func number(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case float64:
		return uint64(n)
	case int:
		return uint64(n)
	case int64:
		return uint64(n)
	}
	return 0
}

// mailTime is how a mail's time is shown, as yardr mail --with shows it.
const mailTime = "01-02 15:04"

func clock(t time.Time) string { return t.Local().Format(mailTime) }

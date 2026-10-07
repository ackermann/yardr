// Package chat is the view behind yardr chat: the yard's crew, each
// with what its session is doing and how much of its mail is unread, the
// conversation with the selected one, and a line to write to it
// (docs/chat.md). Above the crew is the yard itself: the user's
// notifications, which are no conversation and take no answer (yard.go).
//
// It shows what the store keeps and sends through it: a conversation is
// store.API's Conversation, a line sent is its Mail, and what changes
// afterwards (mail, mail being read, a session starting, stopping or
// changing what it does) comes from the event stream (Follow), never from
// asking again in a loop. The model here knows no terminal: Update takes
// messages and View returns text, which is how it is tested.
//
// One thing is not the store's: the last lines of the pane of a crew member
// that waits there for a person (Waits). They are read from the runtime,
// through Options.Tail, while such a crew member is selected.
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
	Crew(ctx context.Context) ([]store.CrewMember, error)
	GetUser(ctx context.Context, name string) (*store.User, error)
	UserInbox(ctx context.Context, name string, limit int) (*store.Mailbox, error)
	AckUserInbox(ctx context.Context, name string, through uint64) error
	Conversation(ctx context.Context, a, b string, after uint64) ([]store.Letter, error)
	Mail(ctx context.Context, to, text, actor string) error
	UserNotifications(ctx context.Context, name string, limit int) (*store.NotificationPage, error)
	AckUserNotifications(ctx context.Context, name string, through uint64) error
}

// Options are what the command knows and the model cannot find out.
type Options struct {
	// Me is the user the view is for: the sender of what is typed, and
	// the one whose mail opening a conversation acknowledges.
	Me string
	// Select is the crew member selected first; "" or one the yard does not
	// have selects the first of the list.
	Select string
	// Yardr is the command's name in the hints the view gives.
	Yardr string
	// InHerdr says the view runs in a pane of herdr, where Focus brings
	// the pane of the agent named to the front. Anywhere else the key for
	// it names the herdr command instead.
	InHerdr bool
	Focus   func(ctx context.Context, agent string) error
	// Tail reads the last lines a session's pane shows, blank ones left
	// out, for the notice of a crew member that waits in it (Waits). Without
	// it, and when it fails, the notice has no lines of the pane.
	Tail func(ctx context.Context, handle map[string]any, lines int) (string, error)
	// Refresh is how often the pane is read again while that notice
	// shows: what a harness asks can change with the crew member still
	// blocked, and no event tells of it. 0 reads it when the crew member's
	// state changes and not in between.
	Refresh time.Duration
	// Expect is a crew member whose first mail is on its way: its session was
	// asked to greet the user when it started (the onboarding's yardmaster).
	// Until there is mail between the two, its conversation says so.
	Expect string
}

// State is what the view starts from, read once (Load).
type State struct {
	// Head is the newest event when the rest was read; the stream is
	// followed from after it. What happens while the rest is read comes
	// again from the stream, and applying it twice changes nothing.
	Head uint64
	Crew []store.CrewMember
	// Cursor is how far the user's mail is acknowledged, and Unread the
	// mail after it.
	Cursor uint64
	Unread []store.Letter
	// Notifications are all the user's inbox keeps of them, oldest first,
	// and NotificationCursor how far they are acknowledged: a position of
	// its own, apart from the mail's.
	Notifications      []store.Notification
	NotificationCursor uint64
}

// Load reads the state a view for the user me starts from.
func Load(ctx context.Context, st Store, me string) (*State, error) {
	head, err := st.Head(ctx)
	if err != nil {
		return nil, err
	}
	s := &State{Head: head}
	if s.Crew, err = st.Crew(ctx); err != nil {
		return nil, err
	}
	u, err := st.GetUser(ctx, me)
	if err != nil {
		return nil, err
	}
	s.Cursor, s.NotificationCursor = u.Cursor, u.NotificationCursor
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
	// All of them: the store bounds what a user's inbox keeps.
	page, err := st.UserNotifications(ctx, me, 0)
	if err != nil {
		return nil, err
	}
	s.Notifications = page.Notifications
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
	crewMsg struct {
		head uint64
		crew []store.CrewMember
		err  error
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
	// tailMsg is the watched pane as read for the watch gen (see paneOf),
	// and refreshMsg the time to read it again.
	tailMsg struct {
		gen  int
		text string
		err  error
	}
	refreshMsg struct{ gen int }
)

// paneOf is what the view has of the pane of the selected crew member while
// it waits there: its last lines, as text and nothing else (inert). gen
// numbers the watch: selecting another crew member, or this one going on,
// starts a new one, and what comes back for an older is dropped. busy says
// a read or the wait for the next is under way, so that one watch reads
// once at a time.
type paneOf struct {
	name  string
	lines []string
	gen   int
	busy  bool
}

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

	crew []store.CrewMember
	sel  string // the selected crew member's name, "" on the yard's row
	// yard says the yard's row is selected: the notifications and no
	// conversation (yard.go).
	yard bool
	// statusAt is the seq of the crew_status event a crew member's
	// status was last taken from: a list of the crew read before it
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

	// notes are the user's notifications, oldest first. noteCursor is how
	// far the store has them acknowledged and noteAsked how far the view
	// has asked for, as cursor and asked are for the mail.
	notes      []store.Notification
	noteCursor uint64
	noteAsked  uint64

	down   bool // the stream is lost
	input  textarea.Model
	width  int
	height int
	scroll int // lines the conversation is scrolled up from its end
	notice string
	// watched is the pane of the selected crew member while it waits there.
	watched paneOf
}

// New is the view for the user opt.Me over what Load read.
func New(ctx context.Context, st Store, opt Options, s *State) *Model {
	if opt.Yardr == "" {
		opt.Yardr = "yardr"
	}
	m := &Model{ctx: ctx, st: st, opt: opt, crew: s.Crew, cursor: s.Cursor, asked: s.Cursor,
		statusAt: map[string]uint64{}, convo: map[string][]store.Letter{}, loaded: map[string]bool{},
		unread: map[uint64]*waiting{},
		notes:  s.Notifications, noteCursor: s.NotificationCursor, noteAsked: s.NotificationCursor}
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
	if m.member(opt.Select) != nil {
		m.sel = opt.Select
	} else if len(m.crew) > 0 {
		m.sel = m.crew[0].Name
	}
	m.opened()
	return m
}

// Init starts the cursor, reads the first conversation and, when the
// selected crew member waits in its pane, that pane.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.open(), m.watch())
}

// crew member is the crew member name, nil when the yard has none of it.
func (m *Model) member(name string) *store.CrewMember {
	for i := range m.crew {
		if m.crew[i].Name == name {
			return &m.crew[i]
		}
	}
	return nil
}

// Expects is the crew member whose first mail the view was told is on its
// way (Options.Expect).
func (m *Model) Expects() string { return m.opt.Expect }

// Selected is the selected crew member's name, "" in a yard without any and
// on the yard's row (OnYard).
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

// others are the senders of unread mail who are no crew members, in name
// order, and how many mails that is: the view shows crew members only, so it
// says where the rest is read.
func (m *Model) others() (int, []string) {
	n, from := 0, []string{}
	for _, w := range m.unread {
		if m.member(w.from) != nil {
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

// The states a crew member is listed with.
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
// what the daemon last saw it do. A crew member whose last start failed is
// failed to start, parked for good or waiting for the next try.
func StateOf(r store.CrewMember) string {
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

// PaneKey is the key that goes to the selected crew member's pane, as the
// view names it in a sentence.
const PaneKey = "ctrl+g"

// Waits reports whether r's session waits in its pane for a person: its
// runtime reads it blocked, or its first prompt is still to be delivered
// and it is not working on one (a harness at a login or a dialog takes no
// prompt, and a runtime may read it idle there). Such a crew member reads no
// mail until someone answered it.
func Waits(r store.CrewMember) bool {
	if !r.Live() {
		return false
	}
	return r.Status == "blocked" || (r.Handle[store.PendingPrompt] != nil && r.Status != "working")
}

// MayWaitAt is what a harness that does not go on may wait at, as far as
// yardr can tell without reading its pane. One text for the onboarding's
// diagnosis and the view's notice.
const MayWaitAt = "may wait at a login, a question or a system dialog"

// NotReady is what is known of a session whose harness, of the kind
// named, did not take its first prompt.
func NotReady(kind string) string {
	return fmt.Sprintf("%s was started in the pane but is not ready for its first prompt: it %s", kind, MayWaitAt)
}

// AtPrompt says that the crew member named waits in its pane: the start of
// the line under a mail it cannot read, and of the notice above the input.
func AtPrompt(name string) string { return name + " waits at a prompt in its pane" }

// Known is the one line of what yardr knows about a crew member that waits
// in its pane (Waits).
func Known(r store.CrewMember) string {
	kind, _ := r.Config["kind"].(string)
	if kind == "" {
		kind = "its harness"
	}
	if r.Status != "blocked" {
		return NotReady(kind)
	}
	return fmt.Sprintf("%s: %s %s", AtPrompt(r.Name), kind, MayWaitAt)
}

// Standing is where a mail the user sent stands: read, or what its
// receiver's state means for it. r is nil for a receiver the yard no
// longer has.
func Standing(l store.Letter, r *store.CrewMember, yardr string) string {
	switch {
	case l.Read:
		return "read"
	case r == nil:
		return "sent"
	}
	n := r.Name
	if Waits(*r) {
		return fmt.Sprintf("%s and cannot read mail (%s goes there)", AtPrompt(n), PaneKey)
	}
	switch StateOf(*r) {
	case Working:
		return fmt.Sprintf("sent; %s is working and reads mail when idle", n)
	case Idle:
		return fmt.Sprintf("sent; %s is idle", n)
	case NotRunning:
		return fmt.Sprintf("%s is not running: %s crew start %s", n, yardr, n)
	case Failed:
		return fmt.Sprintf("%s failed to start: %s crew show %s", n, yardr, n)
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
		// The input has the rows a crew member's layout gives it at this
		// height, whichever row is selected now: the yard's has no input.
		m.input.SetHeight(max(layoutOf(m.height, m.narrow(), false).input, 1))
		m.clamp()
		return m, nil
	case tea.KeyMsg:
		return m, m.key(msg)
	case EventMsg:
		// Whatever the event changed, the pane watched is the one of the
		// crew member now selected, if it waits there.
		cmd := m.event(store.Event(msg))
		return m, tea.Batch(cmd, m.watch())
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
	case crewMsg:
		if msg.err != nil {
			m.notice = fmt.Sprintf("the crew could not be read: %v", msg.err)
			return m, nil
		}
		cmd := m.listed(msg.head, msg.crew)
		return m, tea.Batch(cmd, m.watch())
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
	case notesMsg:
		if msg.err != nil {
			m.notice = fmt.Sprintf("the notifications could not be read: %v", msg.err)
			return m, nil
		}
		return m, m.noted(msg.notes)
	case notesAckedMsg:
		if msg.err != nil {
			m.noteAsked = m.noteCursor
			m.notice = fmt.Sprintf("your notifications could not be marked read: %v", msg.err)
			return m, nil
		}
		m.notesAcked(msg.through)
		return m, nil
	case focusMsg:
		if msg.err != nil {
			m.notice = msg.err.Error()
		} else {
			m.notice = fmt.Sprintf("herdr shows %s now; this view goes on in its own pane", msg.name)
		}
		return m, nil
	case tailMsg:
		if msg.gen != m.watched.gen {
			return m, nil // of a watch that is over
		}
		// A pane that cannot be read has no lines: the notice stands
		// without them.
		m.watched.lines = nil
		if msg.err == nil {
			m.watched.lines = inert(msg.text, paneRows)
		}
		m.clamp()
		if m.opt.Refresh <= 0 {
			m.watched.busy = false
			return m, nil
		}
		gen := m.watched.gen
		return m, tea.Tick(m.opt.Refresh, func(time.Time) tea.Msg { return refreshMsg{gen: gen} })
	case refreshMsg:
		if msg.gen != m.watched.gen {
			return m, nil
		}
		return m, m.tail()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "ctrl+c":
		// Leaving ends the view and nothing else: the crew are the
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
	if m.yard {
		// Nothing typed here goes anywhere, and the view says so once
		// rather than keep it for whoever is selected next.
		m.notice = "nobody to answer here: the yard reports, tab goes to a crew member"
		return nil
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

// step selects the row by rows further down the list, around its ends:
// the yard's row is the first, the crew follow.
func (m *Model) step(by int) tea.Cmd {
	n := 1 + len(m.crew)
	i := 0
	if !m.yard {
		// In a yard without crew members nothing is selected, and the yard's
		// row is the next either way.
		i = 1 + slices.IndexFunc(m.crew, func(r store.CrewMember) bool { return r.Name == m.sel })
	}
	i = ((i+by)%n + n) % n
	m.yard, m.sel = i == 0, ""
	if !m.yard {
		m.sel = m.crew[i-1].Name
	}
	m.notice = ""
	m.opened()
	return tea.Batch(m.open(), m.watch())
}

// opened is what selecting a crew member does to the view itself: its
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
// acknowledges what opening it showed: on the yard's row the
// notifications, and no mail.
func (m *Model) open() tea.Cmd {
	if m.yard {
		return m.ackNotes()
	}
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

// send mails what is typed to the selected crew member: as one mail when one
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

// pane goes to the selected crew member's pane: in herdr it is brought to
// the front, elsewhere the view says which herdr command opens it.
func (m *Model) pane() tea.Cmd {
	r := m.member(m.sel)
	if r == nil {
		return nil
	}
	agent, _ := r.Handle["agent"].(string)
	if !r.Live() || agent == "" {
		m.notice = fmt.Sprintf("%s has no pane, it is not running: %s crew start %s", r.Name, m.opt.Yardr, r.Name)
		return nil
	}
	if !m.opt.InHerdr || m.opt.Focus == nil {
		m.notice = fmt.Sprintf("%s's pane: herdr agent attach %s (in herdr: herdr agent focus %s)", r.Name, agent, agent)
		return nil
	}
	focus, ctx, name := m.opt.Focus, m.ctx, r.Name
	return func() tea.Msg { return focusMsg{name: name, err: focus(ctx, agent)} }
}

// paneRows is how many of the pane's last lines the notice quotes.
const paneRows = 6

// watch keeps the pane of the selected crew member read while it waits there
// (Waits), and forgets it once it does not: the notice goes with it. It is
// asked after everything that can change who is selected or what they do.
func (m *Model) watch() tea.Cmd {
	r := m.member(m.sel)
	if r == nil || !Waits(*r) {
		if m.watched.name != "" {
			m.watched = paneOf{gen: m.watched.gen + 1}
			m.clamp()
		}
		return nil
	}
	if m.watched.name == r.Name && m.watched.busy {
		return nil
	}
	if m.watched.name != r.Name {
		m.watched = paneOf{name: r.Name, gen: m.watched.gen + 1}
		m.clamp()
	}
	return m.tail()
}

// tail reads the watched pane's last lines.
func (m *Model) tail() tea.Cmd {
	r := m.member(m.watched.name)
	if r == nil || m.opt.Tail == nil {
		m.watched.busy = false
		return nil
	}
	m.watched.busy = true
	tail, ctx, gen, handle := m.opt.Tail, m.ctx, m.watched.gen, r.Handle
	return func() tea.Msg {
		text, err := tail(ctx, handle, paneRows)
		return tailMsg{gen: gen, text: text, err: err}
	}
}

// event applies one event of the stream.
func (m *Model) event(e store.Event) tea.Cmd {
	if store.Notified(e, m.opt.Me) != "" {
		// The event says that it became a notification for the user, and
		// not the text yardr made of it: that is read from the store. No
		// event is both that and one of those below.
		return m.readNotes()
	}
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
	case e.Kind == "notifications_read":
		if reader, _ := e.Data["reader"].(string); reader == m.opt.Me {
			m.notesAcked(number(e.Data["through"]))
		}
	case store.EventKind(e.Kind) == "crew_status":
		name := store.EventCrew(e.Data)
		status, _ := e.Data["status"].(string)
		if r := m.member(name); r != nil && r.Live() {
			r.Status = status
			m.statusAt[name] = e.Seq
		}
	case strings.HasPrefix(store.EventKind(e.Kind), "crew_"), strings.HasPrefix(e.Kind, "session_prompt") && store.EventCrew(e.Data) != "":
		// A session started, stopped or failed to start, the list itself
		// changed, or a crew member's first prompt arrived or was given up
		// (its handle then no longer holds it, or says so): one read of
		// the crew for the event, since what they now are is more
		// than the event carries.
		st, ctx := m.st, m.ctx
		return func() tea.Msg {
			head, err := st.Head(ctx)
			if err != nil {
				return crewMsg{err: err}
			}
			rs, err := st.Crew(ctx)
			return crewMsg{head: head, crew: rs, err: err}
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

// listed takes the crew as read at head. A status the stream told
// of since is newer than the read and stays. When the selected crew member
// is gone the first is selected.
func (m *Model) listed(head uint64, rs []store.CrewMember) tea.Cmd {
	for i := range rs {
		if old := m.member(rs[i].Name); old != nil && rs[i].Live() && m.statusAt[rs[i].Name] > head {
			rs[i].Status = old.Status
		}
	}
	m.crew = rs
	if m.yard || m.member(m.sel) != nil {
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

package chat

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"yardr.dev/yardr/internal/store"
)

// The yard's row is the first of the list, above the crew: what the
// yard reports to the user (store.Notification), apart from every
// conversation. It is a row and no third column, since a row folds into the
// line of a narrow terminal with the crew, is reached with the key
// that reaches them, and costs the conversation no width.
//
// It differs from a crew member's row in what it is not: it has no state, no
// pane, and no line to type in, since nobody sent a notification and nobody
// reads an answer. Its unread count is of notifications only, and a
// crew member's of that crew member's mail only. Opening it acknowledges the
// notifications (AckUserNotifications) and leaves the mail's position
// where it is.

// YardRow is the name of that row.
const YardRow = "yard"

// yardLabel keeps the system row distinct if an older yard already has a
// crew member named "yard". New crew members may keep their names; only this
// display label changes in the collision case.
func (m *Model) yardLabel() string {
	if m.member(YardRow) != nil {
		return "yard-notify"
	}
	return YardRow
}

type (
	// notesMsg is the user's notifications as the store has them, and
	// notesAckedMsg how acknowledging them ended.
	notesMsg struct {
		notes []store.Notification
		err   error
	}
	notesAckedMsg struct {
		through uint64
		err     error
	}
)

// OnYard reports whether the yard's row is selected.
func (m *Model) OnYard() bool { return m.yard }

// Notifications is how many notifications the user has not seen.
func (m *Model) Notifications() int {
	n := 0
	for _, note := range m.notes {
		if note.Seq > m.noteAsked {
			n++
		}
	}
	return n
}

// readNotes asks the store for the user's notifications, all of them: an
// event tells that it became one, and yardr's text for it is the store's.
func (m *Model) readNotes() tea.Cmd {
	st, ctx, me := m.st, m.ctx, m.opt.Me
	return func() tea.Msg {
		page, err := st.UserNotifications(ctx, me, 0, me)
		if err != nil {
			return notesMsg{err: err}
		}
		return notesMsg{notes: page.Notifications}
	}
}

// noted takes the notifications as read from the store. On the yard's row
// they are seen as they arrive, and acknowledged.
func (m *Model) noted(notes []store.Notification) tea.Cmd {
	// Two reads may answer out of order: the store only ever adds newer
	// ones, so a list that ends before the one held is the older read.
	if n, have := len(notes), len(m.notes); have > 0 && (n == 0 || notes[n-1].Seq < m.notes[have-1].Seq) {
		return nil
	}
	m.notes = notes
	for _, note := range notes {
		if note.Read {
			m.noteCursor = max(m.noteCursor, note.Seq)
		}
	}
	m.noteAsked = max(m.noteAsked, m.noteCursor)
	if !m.yard {
		return nil
	}
	m.scroll = 0 // the newest in view
	m.clamp()
	return m.ackNotes()
}

// ackNotes acknowledges the user's notifications through the newest: the
// yard's row shows them all. The mail has its own position (ack), which
// this does not move.
func (m *Model) ackNotes() tea.Cmd {
	if len(m.notes) == 0 {
		return nil
	}
	through := m.notes[len(m.notes)-1].Seq
	if through <= m.noteAsked {
		return nil
	}
	m.noteAsked = through
	st, ctx, me := m.st, m.ctx, m.opt.Me
	return func() tea.Msg {
		return notesAckedMsg{through: through, err: st.AckUserNotifications(ctx, me, through, me)}
	}
}

// notesAcked takes the user's notifications through seq as read, by this
// view or by anything else that acknowledged them (yardr inbox).
func (m *Model) notesAcked(through uint64) {
	m.noteCursor = max(m.noteCursor, through)
	m.noteAsked = max(m.noteAsked, m.noteCursor)
	for i := range m.notes {
		m.notes[i].Read = m.notes[i].Seq <= m.noteCursor
	}
}

// noteLines are the notifications as lines of at most w columns, newest
// last: each one's time and the bead it is about, on a line of their own so
// that the bead's id is whole and can be copied, then yardr's text for it.
// A notification about no bead (a hook's) names its source there.
func (m *Model) noteLines(w int) []string {
	if len(m.notes) == 0 {
		// The command on a line of its own: it is there to be copied.
		out := wrap("No notifications. The yard reports here what you subscribe to, for one when beads land:", w, "", look.faint)
		return append(out, look.faint.Render(fit(fmt.Sprintf("  %s subscribe %s yardr --done", m.opt.Yardr, m.opt.Me), w)))
	}
	var out []string
	for i, n := range m.notes {
		if i > 0 {
			out = append(out, "")
		}
		// What a source outside the yard wrote reaches the terminal as
		// text and nothing else, as a pane's lines do.
		text := strings.Join(inert(n.Text, 1), "")
		about := n.Source
		if n.Bead != "" {
			about = n.Bead
			text = strings.TrimPrefix(text, n.Bead+" ") // said on the line above
		}
		out = append(out, fit(look.faint.Render(clock(n.At))+"  "+look.bead.Render(about), w))
		out = append(out, wrap(text, w, "  ")...)
	}
	return out
}

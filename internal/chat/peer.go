package chat

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"yardr.dev/yardr/internal/store"
)

// After the crew the list has those the linked yards expose to this one
// (docs/peers.md): the roster each peer last said, a row for each name,
// <name>@<peer>. Such a row is a conversation as a crew member's is, and
// what is typed in it goes out as yardr mail <name>@<peer> does: queued for
// the peer, under the user's name. The link's send mail is the only grant
// a person needs.
//
// It differs from a crew member's row in what this yard does not know: it
// has no state and no pane, and mail to it is never read as far as this
// yard learns, only queued, sent or given up (store.Letter.Delivery).

// rows are the names of the list under the yard's row, in order: the crew,
// then the addresses of those the linked yards expose.
func (m *Model) rows() []string {
	out := make([]string, 0, len(m.crew)+len(m.linked))
	for _, r := range m.crew {
		out = append(out, r.Name)
	}
	for _, e := range m.linked {
		out = append(out, e.Address())
	}
	return out
}

// party is the one of a linked yard with the address, nil when no peer
// exposes it.
func (m *Model) party(address string) *store.PeerParty {
	for i := range m.linked {
		if m.linked[i].Address() == address {
			return &m.linked[i]
		}
	}
	return nil
}

// Linked are the addresses of those the linked yards expose, as listed.
func (m *Model) Linked() []string { return m.rows()[len(m.crew):] }

// Away is where a mail the user sent to another yard stands: this yard
// knows of its delivery and nothing of its reading.
func Away(l store.Letter, yardr string) string {
	_, peer, _ := strings.Cut(l.To, "@")
	switch l.Delivery {
	case store.MailQueued:
		return fmt.Sprintf("queued for %s: the daemon delivers it", peer)
	case store.MailFailed:
		return fmt.Sprintf("not delivered: %s inbox says why", yardr)
	}
	return fmt.Sprintf("delivered to %s, which does not say when it is read", peer)
}

// about is what stands above the conversation with the selected one of a
// linked yard, as lines of at most w columns: what they are there and the
// line their yard gives of what they do, then whatever keeps mail from
// arriving, or the answer from coming back. Nothing for a crew member.
func (m *Model) about(w int) []string {
	e := m.party(m.sel)
	if e == nil {
		return nil
	}
	is := "a crew member"
	if e.Kind == store.RosterUser {
		is = "a person"
	}
	text := fmt.Sprintf("%s of the linked yard %s", is, e.Peer)
	if e.Role != "" {
		text += ": " + e.Role
	}
	out := wrap(text, w, "", look.faint)
	me := m.opt.Me
	var answers bool
	for _, p := range m.links {
		if p.Name == e.Peer {
			answers = slices.Contains(p.Receive, store.PeerMail) && slices.Contains(p.Expose, me)
		}
	}
	// The command on a line of its own: it is there to be copied, by the
	// operator.
	amiss, mend := "", ""
	switch {
	case !e.Sends:
		amiss = fmt.Sprintf("this yard's link sends no mail to %s. The operator allows it:", e.Peer)
		mend = fmt.Sprintf("%s peer allow %s --send mail", m.opt.Yardr, e.Peer)
	case !e.Mail:
		amiss = fmt.Sprintf("%s takes no mail from this yard yet, so what you write is refused there", e.Peer)
	case !answers:
		amiss = fmt.Sprintf("%s cannot answer you: this yard's link does not let %s write to you. The operator allows it:", e.Name, e.Peer)
		mend = fmt.Sprintf("%s peer allow %s --receive mail --expose %s", m.opt.Yardr, e.Peer, me)
	}
	if amiss != "" {
		out = append(out, wrap(amiss, w, "! ", look.warn)...)
	}
	if mend != "" {
		out = append(out, look.warn.Render(fit("  "+mend, w)))
	}
	return append(out, "")
}

// plain is text another yard wrote as it reaches the terminal: letters and
// nothing else, line by line (inert), an empty line staying one.
func plain(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(inert(l, 1), "")
	}
	return strings.Join(lines, "\n")
}

// link takes an event of a link. The roster a peer says changes without an
// event of its own, so every one is the occasion to read the linked yards
// again, and one that tells how a message to a peer ended (sent, given up)
// to read the open conversations with that yard again, whole: where the
// user's mail stands is what changed.
func (m *Model) link(e store.Event) tea.Cmd {
	st, ctx, me := m.st, m.ctx, m.opt.Me
	cmds := []tea.Cmd{func() tea.Msg {
		ps, err := st.Peers(ctx)
		return linksMsg{peers: ps, err: err}
	}}
	peer, _ := e.Data["peer"].(string)
	if e.Kind != "peer_message_sent" && e.Kind != "peer_message_failed" || peer == "" {
		return cmds[0]
	}
	for name := range m.loaded {
		if _, of, _ := strings.Cut(name, "@"); of != peer {
			continue
		}
		cmds = append(cmds, func() tea.Msg {
			letters, err := st.Conversation(ctx, me, name, 0)
			return convoMsg{name: name, letters: letters, err: err}
		})
	}
	return tea.Batch(cmds...)
}

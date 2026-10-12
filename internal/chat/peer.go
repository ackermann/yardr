package chat

import (
	"fmt"
	"maps"
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

// One of them the link trusts as a user of this yard (store.Peer.Trust) has
// no row of their own: the name is how that user spoke, so its mail is in
// the conversation with the user, whose row stands where the name's would
// (thread). That row is the one place a user of this yard has a row here.
// What is typed in it is an answer, and goes to where the last message came
// from (reaches): to the trusted name at its yard when that wrote last, to
// the user in this yard otherwise. One mail to one place, never a copy to
// the other.

// trusted are the grants of the links ps, the user of this yard by the
// address that speaks as them.
func trusted(ps []store.Peer) map[string]string { return store.TrustedAs(ps) }

// thread is the row the mail with name is in: the user a linked yard's
// name is trusted as, else name itself.
func (m *Model) thread(name string) string {
	if as := m.as[name]; as != "" {
		return as
	}
	return name
}

// voices are the addresses the linked yards expose that speak as the user
// name, as listed; none for a crew member and for an address.
func (m *Model) voices(name string) []string {
	var out []string
	for _, e := range m.linked {
		if m.as[e.Address()] == name {
			out = append(out, e.Address())
		}
	}
	return out
}

// reaches is whom mail typed in the row name goes to: name itself, or, in
// the row of a user of this yard, the address of the name trusted as them
// that wrote the newest mail there which is not the viewer's own. A person
// who answers reaches the one who wrote, as yardr reply does for mail.
func (m *Model) reaches(name string) string {
	c := m.convo[name]
	for i := len(c) - 1; i >= 0; i-- {
		if c[i].From == m.opt.Me {
			continue
		}
		if m.as[c[i].From] == name {
			return c[i].From
		}
		break
	}
	return name
}

// rows are the names of the list under the yard's row, in order: the crew,
// then the addresses of those the linked yards expose, a user of this yard
// standing once for the addresses trusted as them.
func (m *Model) rows() []string {
	out := make([]string, 0, len(m.crew)+len(m.linked))
	for _, r := range m.crew {
		out = append(out, r.Name)
	}
	for _, e := range m.linked {
		if name := m.thread(e.Address()); !slices.Contains(out[len(m.crew):], name) {
			out = append(out, name)
		}
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

// Linked are the rows after the crew: the addresses of those the linked
// yards expose, and the users of this yard some of them are trusted as.
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
// arriving, or the answer from coming back. For a user of this yard that
// names of linked yards are trusted as: those names, and where what is
// typed goes next. Nothing for a crew member.
func (m *Model) about(w int) []string {
	if from := m.voices(m.sel); len(from) > 0 {
		goes := fmt.Sprintf("%s in this yard", m.sel)
		if to := m.reaches(m.sel); to != m.sel {
			goes = to + ", who wrote last"
		}
		text := fmt.Sprintf("a person of this yard, also trusted as %s: mail from that name is in this conversation, and what you write goes to %s",
			strings.Join(from, ", "), goes)
		return append(wrap(text, w, "", look.faint), "")
	}
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
	st, ctx := m.st, m.ctx
	cmds := []tea.Cmd{func() tea.Msg {
		ps, err := st.Peers(ctx)
		return linksMsg{peers: ps, err: err}
	}}
	peer, _ := e.Data["peer"].(string)
	if e.Kind != "peer_message_sent" && e.Kind != "peer_message_failed" || peer == "" {
		return cmds[0]
	}
	for name := range m.loaded {
		if slices.ContainsFunc(append(m.voices(name), name), func(a string) bool { return strings.HasSuffix(a, "@"+peer) }) {
			cmds = append(cmds, m.reread(name, 0))
		}
	}
	return tea.Batch(cmds...)
}

// regroup takes the linked yards as read again. When whom they trust as
// whom changed, the mail of a name belongs in another conversation than the
// one it was put in, so the conversations are forgotten and the open one is
// read again. The selection goes with a name that is trusted newly, to the
// user's row.
func (m *Model) regroup(ps []store.Peer) tea.Cmd {
	as := trusted(ps)
	moved := !maps.Equal(as, m.as)
	m.links, m.linked, m.as = ps, store.PeerParties(ps), as
	if !moved {
		return m.reselect()
	}
	m.convo, m.loaded = map[string][]store.Letter{}, map[string]bool{}
	m.sel = m.thread(m.sel)
	if !m.yard && !slices.Contains(m.rows(), m.sel) {
		return m.reselect()
	}
	m.opened()
	return m.open()
}

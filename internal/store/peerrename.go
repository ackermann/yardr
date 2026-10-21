package store

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A peer's name is this yard's own label for the link (docs/peers.md): the
// other yard is known by its key and hears nothing of the name. So the name
// can change, and everything this yard keys by it moves in the write that
// changes it: the record, the outbox, the ids taken, every crew member
// setting that names peers (peerSettings), and the label of the open beads
// it sent.
//
// The event log is not rewritten: an event keeps the name the peer had
// when it was logged. The peer's record says which names it had and until
// which event (Was), and since which event the link is (Since), so what
// reads mail back from the log (an inbox, a conversation) knows which of
// today's peers a name in an event meant, or that it meant none of them: a
// yard added later under a name that became free is another yard.

// PeerWas is a name a peer had: up to the peer_renamed event Until, which
// was logged At.
type PeerWas struct {
	Name  string    `json:"name"`
	Until uint64    `json:"until"`
	At    time.Time `json:"at"`
}

// peerNames are the yard's peers, asked which of them an event meant.
type peerNames []Peer

func peersIn(tx *bolt.Tx) (peerNames, error) {
	var ps peerNames
	err := each(tx, bPeers, "", func(_ []byte, p *Peer) error {
		ps = append(ps, *p)
		return nil
	})
	return ps, err
}

// now is what the peer the event seq calls name is called today: "" when
// it is none of the yard's peers any more, or never was one.
func (ps peerNames) now(name string, seq uint64) string {
	for _, p := range ps {
		from := p.Since
		for _, w := range p.Was {
			if w.Name == name && seq > from && seq < w.Until {
				return p.Name
			}
			from = w.Until
		}
		if p.Name == name && seq > from {
			return p.Name
		}
	}
	return ""
}

// peer is the peer the event seq calls name, nil when now has none.
func (ps peerNames) peer(name string, seq uint64) *Peer {
	if is := ps.now(name, seq); is != "" {
		for i := range ps {
			if ps[i].Name == is {
				return &ps[i]
			}
		}
	}
	return nil
}

// renamedTo ends the error for a name that is no peer when it was one's
// until a rename: whoever still has the old address is told the new one,
// and reaches nothing under the old.
func renamedTo(tx *bolt.Tx, name string) string {
	var last PeerWas
	is := ""
	_ = each(tx, bPeers, "", func(_ []byte, p *Peer) error {
		for _, w := range p.Was {
			if w.Name == name && w.Until > last.Until {
				last, is = w, p.Name
			}
		}
		return nil
	})
	if is == "" {
		return ""
	}
	return fmt.Sprintf(": the yard that was %s here until %s is %s now", name, last.At.Local().Format("01-02 15:04"), is)
}

// RenamePeer changes the name this yard has for the peer name to to. The
// link stays what it is: its key, its URL, what it carries and what it has
// counted. What waits for the peer still waits, what was taken from it is
// still known as taken, and a crew member that may mail it still may, all
// under the new name and in this one write. The crew that may are
// told by mail from the yard, since the address they knew reaches nothing
// now. It logs peer_renamed.
//
// The old name is free afterwards, and nothing is left under it.
func (s *Store) RenamePeer(ctx context.Context, name, to, actor string) (*Peer, error) {
	var p *Peer
	err := s.write(func(w *wtx) (err error) {
		p, err = w.renamePeer(ctx, name, to, actor, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// renamePeer is RenamePeer inside the transaction, so that an approved run
// ask renames the peer in the write that records its approval: by is then
// the ask it was approved on, which the event carries.
func (w *wtx) renamePeer(ctx context.Context, name, to, actor string, by *approvedOn) (p *Peer, err error) {
	if err := w.operatorOnly(ctx, actor, "rename a peer", Askable); err != nil {
		return nil, err
	}
	if p, err = loadPeer(w.Tx, name); err != nil {
		return nil, err
	}
	if err := checkName("peer", to); err != nil {
		return nil, err
	}
	switch {
	case to == name:
		return nil, fmt.Errorf("peer %s has that name already", name)
	case w.Bucket(bPeers).Get([]byte(to)) != nil:
		return nil, fmt.Errorf("peer %s is added already: two links cannot share a name. Rename or remove that one first", to)
	}
	data := map[string]any{"peer": to, "from": name, "to": to}
	out := w.Bucket(bPeerOut)
	waiting := keys(w.Tx, bPeerOut, join(name, ""))
	for _, k := range waiting {
		var m PeerOut
		if _, err := get(w.Tx, bPeerOut, k, &m); err != nil {
			return nil, err
		}
		if err := out.Delete([]byte(k)); err != nil {
			return nil, err
		}
		// Under the number it has: the peer's questions count by it.
		m.Peer = to
		if err := put(w.Tx, bPeerOut, join(to, seqKey(m.Seq)), m); err != nil {
			return nil, err
		}
	}
	if len(waiting) > 0 {
		data["messages"] = len(waiting)
	}
	// Kept, or a message handed over again would be taken twice.
	seen := w.Bucket(bPeerSeen)
	for _, k := range keys(w.Tx, bPeerSeen, join(name, "")) {
		at := bytes.Clone(seen.Get([]byte(k)))
		if err := seen.Delete([]byte(k)); err != nil {
			return nil, err
		}
		if err := seen.Put([]byte(join(to, k[len(name)+1:])), at); err != nil {
			return nil, err
		}
	}
	had, err := renamePeerSettings(w.Tx, name, to)
	if err != nil {
		return nil, err
	}
	crew := had[MailPeers]
	if len(crew) > 0 {
		data["crew"] = crew
	}
	// A subscription that filters on the yard, to the source peer by peer=
	// or to any source by one of its crew in speaker=, is given the new
	// name too.
	if subs, err := renamePeerFilters(w.Tx, name, to); err != nil {
		return nil, err
	} else if len(subs) > 0 {
		data["subscriptions"] = subs
	}
	// A hook's says and makes lines filter on the envelope's fields as a
	// subscription does, speaker= among them: those that name one of the
	// yard's crew follow the new name too.
	if hooks, err := renamePeerHooks(w.Tx, name, to); err != nil {
		return nil, err
	} else if len(hooks) > 0 {
		data["hooks"] = hooks
	}
	// The beads it sent say where they came from by its key, so their
	// line follows unasked; their label is a name, and is changed.
	if err := w.relabelPeerBeads(p, name, to, actor); err != nil {
		return nil, err
	}
	// A group that sends its beads there names it, and is given the new
	// name: the beads that are away say whose they are by its key.
	if groups, err := renamePeerGroups(w.Tx, name, to); err != nil {
		return nil, err
	} else if len(groups) > 0 {
		data["groups"] = groups
	}
	if by != nil {
		data["approved"] = by
	}
	if err := w.event("", "peer_renamed", actor, data); err != nil {
		return nil, err
	}
	if err := w.Bucket(bPeers).Delete([]byte(name)); err != nil {
		return nil, err
	}
	p.Was = append(p.Was, PeerWas{Name: name, Until: w.Bucket(bEvents).Sequence(), At: now()})
	p.Name = to
	if err := put(w.Tx, bPeers, to, p); err != nil {
		return nil, err
	}
	for _, r := range crew {
		text := fmt.Sprintf("the linked yard %s is called %s here now: write to its crew as <name>@%s. Nothing is reached as <name>@%s any more.", name, to, to, name)
		if err := w.event("", "mail", "yard", map[string]any{"to": r, "text": text}); err != nil {
			return nil, err
		}
	}
	if err := outboxOf(w.Tx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// peersToday gives mail from another yard in evs the name its peer has
// now, where the peer was renamed since: the sender is <name>@<peer>, and
// an address that reaches nothing, or in time another yard, is not what a
// crew member is to answer. The name the event was logged with is kept as
// from_peer_was.
//
// Mail from a yard that is no peer any more has no address at all: its
// name is free, and a yard added under it later is another yard, which an
// answer to <name>@<peer> would reach. The actor and from_peer are empty
// then, from_peer_unlinked says why, and from_peer_was keeps the name for
// a reader (MailSender).
func peersToday(tx *bolt.Tx, evs []Event) error {
	var ps peerNames
	for i, e := range evs {
		was, _ := e.Data["from_peer"].(string)
		if e.Kind != "mail" || was == "" {
			continue
		}
		if ps == nil {
			var err error
			if ps, err = peersIn(tx); err != nil {
				return err
			}
		}
		switch is := ps.now(was, e.Seq); {
		case is == "":
			evs[i].Actor = ""
			evs[i].Data["from_peer"], evs[i].Data["from_peer_was"], evs[i].Data["from_peer_unlinked"] = "", was, true
		case is != was:
			from, _ := e.Data["from"].(string)
			evs[i].Actor = from + "@" + is
			evs[i].Data["from_peer"], evs[i].Data["from_peer_was"] = is, was
		}
	}
	return nil
}

// FromOutside is whether e is mail that came from another yard, linked
// still or not: what carries OutsideMail where a crew member reads it.
func FromOutside(e Event) bool {
	if e.Kind != "mail" {
		return false
	}
	peer, _ := e.Data["from_peer"].(string)
	return peer != "" || unlinked(e)
}

func unlinked(e Event) bool {
	gone, _ := e.Data["from_peer_unlinked"].(bool)
	return gone
}

// MailSender is who the mail e of an inbox is from, as its reader is
// told: the actor, <name>@<peer> for mail from a linked yard. Mail from a
// yard that is linked no more names no address, since none reaches its
// sender: the name the sender gave, and what the yard was called here.
func MailSender(e Event) string {
	if !unlinked(e) {
		return e.Actor
	}
	from, _ := e.Data["from"].(string)
	was, _ := e.Data["from_peer_was"].(string)
	return fmt.Sprintf("%s at a yard no longer linked (was %s)", from, was)
}

// Delivery of mail to another yard's crew member (Letter.Delivery).
const (
	MailQueued = "queued"
	MailSent   = "sent"
	MailFailed = "failed"
)

// sent is what this yard knows of the mail it wrote to one peer: by the
// mail's id, how its delivery stands when it is not with the peer, and by
// id and hash why a file of it was not taken.
type sent struct{ delivery, untaken map[string]string }

// sentTo reads it for the peer p: the outbox, and the log since the link
// was added.
func sentTo(tx *bolt.Tx, p *Peer) (*sent, error) {
	// What still waits for the peer, or was given up: the rest it has.
	delivery, untaken := map[string]string{}, map[string]string{}
	if err := each(tx, bPeerOut, join(p.Name, ""), func(_ []byte, m *PeerOut) error {
		delivery[m.ID] = MailQueued
		if m.Failed != nil {
			delivery[m.ID] = MailFailed
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// One given up long ago is gone from the outbox (prunePeers). Its
	// peer_message_failed was logged after its mail, so it is in the log
	// for as long as the mail is.
	failed := join("peer_message_failed", "")
	for _, k := range keys(tx, bEventsKd, failed) {
		var e Event
		if ok, err := get(tx, bEvents, k[len(failed):], &e); err != nil {
			return nil, err
		} else if !ok || e.Seq <= p.Since {
			continue
		}
		if id, _ := e.Data["id"].(string); id != "" {
			delivery[id] = MailFailed
		}
		// A file given up says which mail it was for, which went on
		// without it (giveUp): the reason as that mail's sender was told.
		if mail, _ := e.Data["mail"].(string); mail != "" {
			hash, _ := e.Data["hash"].(string)
			reason, _ := e.Data["reason"].(string)
			peer, _ := e.Data["peer"].(string)
			untaken[join(mail, hash)] = strings.TrimPrefix(reason, "peer "+peer+": ")
		}
	}
	return &sent{delivery, untaken}, nil
}

// letter is the mail e, which its actor wrote to the peer, as a
// conversation shows it: to is the address it went to, as the peer is
// called now.
func (s *sent) letter(e Event, to string) Letter {
	text, _ := e.Data["text"].(string)
	id, _ := e.Data["id"].(string)
	l := Letter{Seq: e.Seq, At: e.At, From: e.Actor, To: to, Text: text, Delivery: s.delivery[id], Files: EventFiles(e)}
	if l.Delivery == "" {
		l.Delivery = MailSent
	}
	for i, f := range l.Files {
		l.Files[i].NotTaken = s.untaken[join(id, f.Hash)]
	}
	return l
}

// peerConversation is Conversation with a crew member of a linked yard,
// with being <crew member>@<peer>: the mail me wrote there and the mail that
// came from there for me, after seq after, before seq before, and the newest
// limit of it (Conversation). It goes by the link, not by the
// name: mail from before a rename is in it, under the name the peer has
// now, and mail of an earlier yard that had the name is not.
//
// It reads the mail of the log since the link was added, by the kind
// index.
func peerConversation(tx *bolt.Tx, me, with string, after uint64, limit int, before uint64) ([]Letter, bool, error) {
	member, name, _ := strings.Cut(with, "@")
	p, err := loadPeer(tx, name)
	if err != nil {
		return nil, false, err
	}
	ps, err := peersIn(tx)
	if err != nil {
		return nil, false, err
	}
	sent, err := sentTo(tx, p)
	if err != nil {
		return nil, false, err
	}
	rd := &readers{tx: tx}
	var mail []string
	c := tx.Bucket(bEventsKd).Cursor()
	prefix := []byte(join("mail", ""))
	for k, _ := c.Seek([]byte(join("mail", seqKey(max(after, p.Since)+1)))); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		if before > 0 && string(k[len(prefix):]) >= seqKey(before) {
			break
		}
		mail = append(mail, string(k[len(prefix):]))
	}
	return newest(mail, limit, func(k string) (Letter, bool, error) {
		var e Event
		if ok, err := get(tx, bEvents, k, &e); err != nil || !ok {
			return Letter{}, false, err
		}
		to, _ := e.Data["to"].(string)
		left, _ := e.Data["to_peer"].(string)
		came, _ := e.Data["from_peer"].(string)
		from, _ := e.Data["from"].(string)
		switch {
		case left != "" && e.Actor == me && to == member && ps.now(left, e.Seq) == name:
			l := sent.letter(e, with)
			reactions, err := rd.reactionsFor(e.Seq)
			if err != nil {
				return Letter{}, false, err
			}
			l.Reactions = reactions
			return l, true, nil
		case came != "" && to == me && from == member && ps.now(came, e.Seq) == name:
			l, err := rd.letter(e)
			l.From = with
			return l, err == nil, err
		}
		return Letter{}, false, nil
	})
}

// migratePeerSince gives every peer of a store from before a link could be
// renamed (schema 4) the event it was added with, from the log: mail of an
// earlier yard that had its name is then not read as its own. A peer whose
// peer_added is gone from the log keeps 0, the whole log.
func migratePeerSince(tx *bolt.Tx, from uint64) error {
	if from >= 5 || tx.Bucket(bPeers) == nil {
		return nil
	}
	ps, err := peersIn(tx)
	if err != nil {
		return err
	}
	prefix := join("peer_added", "")
	added := keys(tx, bEventsKd, prefix)
	for _, p := range ps {
		if p.Since != 0 {
			continue
		}
		for i := len(added) - 1; i >= 0; i-- {
			var e Event
			if ok, err := get(tx, bEvents, added[i][len(prefix):], &e); err != nil {
				return err
			} else if name, _ := e.Data["peer"].(string); ok && name == p.Name {
				p.Since = e.Seq - 1
				break
			}
		}
		if p.Since == 0 {
			continue
		}
		if err := put(tx, bPeers, p.Name, p); err != nil {
			return err
		}
	}
	return nil
}

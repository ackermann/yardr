package store

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// PeerAsk is an ask of a person of a linked yard (docs/peers.md): the
// question stands among their decisions there, and their pick comes back
// onto the bead it was asked on. It is queued in the outbox for the peer
// in the write that stores the ask (AddAsk), and crosses as any message
// does. One kind covers both directions: an answer is a message of kind
// ask whose Re is the ask message's id, as a pong is a ping with Re.
const PeerAsk = "ask"

// PeerAskVersion is the v of the ask bodies this build writes. A body of
// another v is refused (RefusedVersion), as a bead's is.
const PeerAskVersion = 1

// Why an ask that came in was not taken, as the sender is told, beside
// RefusedKind and RefusedTooLarge.
const (
	// RefusedNoUser is an ask for a name that is exposed and no user of
	// this yard has: crew members have no decisions queue.
	RefusedNoUser = "no_such_user"
	// RefusedNotAsk is a body that is no ask: no question, a sender's
	// name that is none, a bead that is none.
	RefusedNotAsk = "not_ask"
)

// peerAsk is the body of a message of kind PeerAsk carrying the question:
// v, the bead's handle, title, topic and stage, the bead's body cut at
// 16 KB saying so, the ask's seq, asker and since, the ask's JSON whole,
// and each about-bead as id, title and stage. To is the person of the
// peer's yard who must answer, From the crew member or user of this yard
// who asked.
type peerAsk struct {
	V      int           `json:"v"`
	To     string        `json:"to"`
	From   string        `json:"from"`
	Bead   string        `json:"bead"`
	Title  string        `json:"title"`
	Topic  string        `json:"topic,omitempty"`
	Stage  string        `json:"stage"`
	Labels []string      `json:"labels,omitempty"`
	Body   string        `json:"body,omitempty"`
	Seq    uint64        `json:"seq"`
	Since  time.Time     `json:"since"`
	Ask    Ask           `json:"ask"`
	About  []peerAskBead `json:"about,omitempty"`
}

// peerAskBead is one about-bead of a cross-yard ask, as carried: id,
// title and stage, text the other yard reads but never follows.
type peerAskBead struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Stage string `json:"stage"`
}

// bPeerAsks holds the foreign asks this yard took, by the peer's key and
// the message id: the peer by its key, never by the name this yard has
// for it, so a rename changes nothing in it. It is no part of any bead:
// what anyone may edit of a bead cannot change whose question it is.
var bPeerAsks = []byte("peer_asks")

func init() { allBuckets = append(allBuckets, bPeerAsks) }

// foreignAsk is a question another yard asked a person of this yard: the
// message id, the peer by its key, the sender's bead, title, topic, stage
// and cut body, the ask's seq, asker, since and JSON, and the about-beads
// as text. TakenAt is when this yard took it, by its own clock: UserAsks
// places it by then, never by the sender's clock.
type foreignAsk struct {
	Message string        `json:"message"`
	Key     string        `json:"key"`
	To      string        `json:"to"`
	From    string        `json:"from"`
	Bead    string        `json:"bead"`
	Title   string        `json:"title"`
	Topic   string        `json:"topic,omitempty"`
	Stage   string        `json:"stage"`
	Labels  []string      `json:"labels,omitempty"`
	Body    string        `json:"body,omitempty"`
	Seq     uint64        `json:"seq"`
	Since   time.Time     `json:"since"`
	Ask     Ask           `json:"ask"`
	About   []peerAskBead `json:"about,omitempty"`
	TakenAt time.Time     `json:"taken_at"`
}

// checkAsk decides whether the ask m brings from p is taken: it returns
// the ask, or why not. Every ask that comes counts toward the peer's hour
// beside mail, the refused too, so a yard that is refused cannot keep
// asking who lives here. A refusal is logged as peer_message_refused, but
// of those over the limit only the first of the hour: the rest say nothing
// new.
//
// The names the link exposes to p (Peer.Expose) are the only ones an ask
// reaches, and only a user of this yard has a decisions queue: anything
// else is refused as not exposed whether or not anyone here has it, and an
// exposed name nobody has as a user as no such user.
func (w *wtx) checkAsk(p *Peer, m PeerMessage, way string) (*peerAsk, string, error) {
	p.hour(now())
	p.Mails++
	extra := map[string]any{"direction": "in", "way": way}
	var ask peerAsk
	err := json.Unmarshal(m.Body, &ask)
	if err == nil {
		ask.Title = cleanTitle(ask.Title)
	}
	reason := ""
	switch {
	case p.Mails > PeerMailLimit:
		reason = RefusedRate
		if p.MailLimited {
			return nil, reason, nil
		}
		p.MailLimited = true
		extra["limit"], extra["until"] = PeerMailLimit, p.MailSince.Add(PeerMailWindow)
	case len(m.Body) > maxPeerBody(m.Kind):
		reason = RefusedTooLarge
	case err != nil:
		reason = RefusedNotAsk
	case ask.V != PeerAskVersion:
		extra["v"], extra["known"] = ask.V, PeerAskVersion
		reason = RefusedVersion
	case m.Re != "":
		// An answer, whose Re is the ask message's id: yardr-1kcv
		// takes them. Until it lands none is taken, and saying
		// not_ask tells the sender it is no question.
		reason = RefusedNotAsk
	case !peerMailName.MatchString(ask.From):
		reason = RefusedNotAsk
	case !validID.MatchString(ask.Bead) || isUUID(ask.Bead):
		reason = RefusedNotAsk
	case ask.Title == "" || !utf8.ValidString(ask.Title):
		reason = RefusedNotAsk
	case !peerStage.MatchString(ask.Stage):
		reason = RefusedNotAsk
	case !utf8.ValidString(ask.Topic) || !utf8.ValidString(ask.Body) ||
		slices.ContainsFunc(ask.Labels, func(l string) bool { return !utf8.ValidString(l) }):
		reason = RefusedNotAsk
	case ask.Seq == 0 || ask.Since.IsZero():
		reason = RefusedNotAsk
	case !validAsk(ask.To, ask.Ask):
		reason = RefusedNotAsk
	case slices.ContainsFunc(ask.About, func(b peerAskBead) bool {
		return !validID.MatchString(b.ID) || isUUID(b.ID) || !utf8.ValidString(b.Title) || !peerStage.MatchString(b.Stage)
	}):
		reason = RefusedNotAsk
	case askBytes(ask.Ask) > MaxPeerMailBytes || len(ask.Body) > MaxPeerNoteBytes:
		reason = RefusedTooLarge
	case !peerMailName.MatchString(ask.To) || !slices.Contains(p.Expose, ask.To):
		reason = RefusedNotExposed
		// Whom it asked for is the operator's to see, when it is a name.
		if peerMailName.MatchString(ask.To) {
			extra["to"] = ask.To
		}
	case w.Bucket(bUsers).Get([]byte(ask.To)) != nil:
		return &ask, "", nil
	default:
		// Exposed and no user since, which unexpose sees to for one
		// removed by name: said as no such user, since crew members
		// have no decisions queue either.
		reason = RefusedNoUser
		extra["to"] = ask.To
	}
	extra["reason"] = reason
	return nil, reason, w.event("", "peer_message_refused", "peer:"+p.Name, peerEvent(p.Name, m, extra))
}

// validAsk reports whether a is an ask to holds: a question, context, the
// options each naming its consequence, the recommendation with its why,
// and the beads it is about. To is the person of this yard who must
// answer; the ask's To is that person at the sender's yard for this one,
// person@peer as the sender calls this yard.
func validAsk(to string, a Ask) bool {
	if strings.TrimSpace(a.Question) == "" || !utf8.ValidString(a.Question) {
		return false
	}
	askTo, peer, ok := strings.Cut(a.To, "@")
	if !ok || askTo != to || peer == "" || strings.Count(a.To, "@") != 1 {
		return false
	}
	context := 0
	for _, line := range a.Context {
		if !utf8.ValidString(line) {
			return false
		}
		if strings.TrimSpace(line) != "" {
			context++
		}
	}
	if context == 0 {
		return false
	}
	for i, o := range a.Options {
		if strings.TrimSpace(o.Label) == "" || strings.TrimSpace(o.Consequence) == "" ||
			!utf8.ValidString(o.Label) || !utf8.ValidString(o.Consequence) {
			return false
		}
		if strings.EqualFold(o.Label, YouDecide) && i != len(a.Options)-1 {
			return false
		}
	}
	if len(a.Options) > 0 && a.Options[len(a.Options)-1].Label != YouDecide {
		return false
	}
	switch {
	case a.Recommend < 0 || a.Recommend > len(a.Options):
		return false
	case a.Recommend != 0 && a.Options[a.Recommend-1].Label == YouDecide:
		return false
	case a.Recommend != 0 && strings.TrimSpace(a.Why) == "":
		return false
	case a.Recommend == 0 && strings.TrimSpace(a.Why) != "":
		return false
	case !utf8.ValidString(a.Why):
		return false
	}
	seen := map[string]bool{}
	for _, b := range a.About {
		if !validID.MatchString(b) || isUUID(b) || seen[b] {
			return false
		}
		seen[b] = true
	}
	return true
}

// askBytes is what an ask's question, context and options together weigh:
// over mail's 16 KB the ask is refused as too large.
func askBytes(a Ask) int {
	n := len(a.Question)
	for _, line := range a.Context {
		n += len(line)
	}
	for _, o := range a.Options {
		n += len(o.Label) + len(o.Consequence)
	}
	return n
}

// deliverAsk keeps the ask m brought from p as a foreign ask, keyed by the
// peer's key, and logs its arrival as a noted event naming the foreign
// bead bead@peer, which clients receive as stored: footplate re-reads
// UserAsks on it with no change. The peer part is this yard's own name for
// the link, which the sender cannot choose.
func (w *wtx) deliverAsk(p *Peer, m PeerMessage, ask *peerAsk) error {
	rec := foreignAsk{
		Message: m.ID, Key: p.Key, To: ask.To, From: ask.From,
		Bead: ask.Bead, Title: ask.Title, Topic: ask.Topic, Stage: ask.Stage,
		Labels: ask.Labels, Body: ask.Body, Seq: ask.Seq, Since: ask.Since,
		Ask: ask.Ask, About: ask.About, TakenAt: now(),
	}
	if err := put(w.Tx, bPeerAsks, join(p.Key, m.ID), rec); err != nil {
		return err
	}
	return w.event(ask.Bead+"@"+p.Name, "noted", ask.From+"@"+p.Name, map[string]any{"ask": ask.Seq})
}

// foreignAsksOf are the foreign asks taken from the peer whose key is key,
// as they are today: none of an earlier link with the same yard.
func foreignAsksOf(tx *bolt.Tx, key string) ([]foreignAsk, error) {
	var out []foreignAsk
	err := each(tx, bPeerAsks, "", func(_ []byte, rec *foreignAsk) error {
		if rec.Key == key {
			out = append(out, *rec)
		}
		return nil
	})
	return out, err
}

// dropPeerAsks drops the foreign asks taken from the peer with p's key:
// after peer rm a queue must stay answerable, and kept-but-dead questions
// are worse than mail, which is only history. It answers them by the user
// they were for, so each gets one mail listing theirs as text.
func (w *wtx) dropPeerAsks(p *Peer) (map[string][]foreignAsk, error) {
	var keys []string
	byUser := map[string][]foreignAsk{}
	err := each(w.Tx, bPeerAsks, "", func(k []byte, rec *foreignAsk) error {
		if rec.Key != p.Key {
			return nil
		}
		keys = append(keys, string(k))
		byUser[rec.To] = append(byUser[rec.To], *rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if err := w.Bucket(bPeerAsks).Delete([]byte(k)); err != nil {
			return nil, err
		}
	}
	for _, recs := range byUser {
		slices.SortFunc(recs, func(a, b foreignAsk) int {
			if a.TakenAt.Equal(b.TakenAt) {
				return strings.Compare(a.Message, b.Message)
			}
			if a.TakenAt.Before(b.TakenAt) {
				return -1
			}
			return 1
		})
	}
	return byUser, nil
}

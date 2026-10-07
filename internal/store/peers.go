package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/yardkey"
)

// Two yards are linked when the operator of each has added the other as a
// peer, by its public key (docs/peers.md). Yards share no store: each stays
// the only writer of its own, and they send each other messages. A message
// to a peer waits in this yard's outbox for it until it is delivered: the
// daemon posts it to a peer that can be called (push), a peer that cannot
// asks for what waits (pull). What comes in is taken once, by its id.
var (
	bPeers    = []byte("peers")     // <name>
	bPeerOut  = []byte("peer_out")  // <peer>\x00<seq>: messages still to deliver
	bPeerSeen = []byte("peer_seen") // <peer>\x00<message id>: when it was taken
)

func init() { allBuckets = append(allBuckets, bPeers, bPeerOut, bPeerSeen) }

// MaxPeerBody bounds a message's body.
const MaxPeerBody = 256 << 10

// PeerPing is a ping, which the other yard answers with a message of the
// same kind that names it (Re), the pong. A ping therefore needs the kind
// allowed both ways on both sides.
const PeerPing = "ping"

// PeerKinds are the kinds of messages a link can be allowed to carry.
// PeerMail is in peermail.go and PeerBead, which allows the answer to a
// bead too, in peerbeads.go. They are also what this build says it
// understands (Says).
var PeerKinds = []string{PeerBead, PeerMail, PeerPing}

// knownKind reports whether this build knows a message of kind: one a link
// can be allowed to carry, or the answer to a bead, which goes with the
// bead's word. Any other is a newer yardr's.
func knownKind(kind string) bool {
	return kind == PeerBeadReturn || slices.Contains(PeerKinds, kind)
}

// PeerSays is what a yard states of its build with every signed request
// and every answer to one: the kinds it understands (PeerKinds, there) and
// its yardr. Two yards are not upgraded on the same day, and a yard is
// upgraded long after it was linked, so it is said every time and not at
// pairing. A build from before it was said ignores the field and says
// nothing.
type PeerSays struct {
	Understands []string `json:"understands"`
	Yardr       string   `json:"yardr,omitempty"`
}

// Says is what this build states of itself to a peer; yardr names the
// build, "" when it has no name (a test binary).
func Says(yardr string) *PeerSays {
	return &PeerSays{Understands: slices.Clone(PeerKinds), Yardr: yardr}
}

// PeerSaid is what a peer last said, and when this yard heard it.
type PeerSaid struct {
	PeerSays
	At time.Time `json:"at"`
}

// Knows reports whether the peer said its build understands kind; a nil
// PeerSaid, a peer that has not said, knows of none.
func (s *PeerSaid) Knows(kind string) bool {
	return s != nil && slices.Contains(s.Understands, kind)
}

const (
	// maxPeerSaidKinds bounds the kinds kept of what a peer said.
	maxPeerSaidKinds = 32
	// peerSaidEvery is how old the time kept with what a peer said may
	// grow while it says the same: a peer says it with every question, and a
	// write for each would wake the daemon each time.
	peerSaidEvery = time.Hour
	// peerHeardEvery is how old the time a peer was last heard from may
	// grow while it keeps being heard, for the same reason.
	peerHeardEvery = time.Minute
)

// clean is what is kept of what a peer said, which is the peer's text: the
// kinds that are a kind's few plain characters, each once and sorted, and a
// build of a version's characters. nil when it named no kinds at all, as a
// yardr from before this was said does not.
func (s *PeerSays) clean() *PeerSays {
	if s == nil || s.Understands == nil {
		return nil
	}
	out := &PeerSays{Understands: []string{}}
	for _, k := range s.Understands {
		if len(out.Understands) < maxPeerSaidKinds && peerKind.MatchString(k) && !slices.Contains(out.Understands, k) {
			out.Understands = append(out.Understands, k)
		}
	}
	slices.Sort(out.Understands)
	if peerBuild.MatchString(s.Yardr) {
		out.Yardr = s.Yardr
	}
	return out
}

// heard keeps on p that its yard was heard from, with a request or an
// answer signed with its key, and what it said then (said). It reports
// whether p changed and is to be saved: the time it was heard at is kept
// to peerHeardEvery.
func (p *Peer) heard(says *PeerSays) bool {
	changed := p.said(says)
	if p.HeardAt == nil || !now().Before(p.HeardAt.Add(peerHeardEvery)) {
		at := now()
		p.HeardAt, changed = &at, true
	}
	return changed
}

// said keeps on p what its yard said of its build, and reports whether p
// changed. One that
// said nothing is an older yardr (again, when it had said before): nothing
// is kept of it. What it says again unchanged is not saved again until the
// time kept with it is peerSaidEvery old.
func (p *Peer) said(says *PeerSays) bool {
	says = says.clean()
	switch {
	case says == nil:
		if p.Said == nil {
			return false
		}
		p.Said = nil
		return true
	case p.Said != nil && p.Said.Yardr == says.Yardr && slices.Equal(p.Said.Understands, says.Understands) &&
		now().Before(p.Said.At.Add(peerSaidEvery)):
		return false
	}
	p.Said = &PeerSaid{PeerSays: *says, At: now()}
	return true
}

// The ways a message crosses a link, as its events name them: posted by the
// sender, or asked for by the receiver.
const (
	PeerPush = "push"
	PeerPull = "pull"
)

// pullBatch bounds what one pull hands over; the rest waits for the next.
const (
	pullBatch      = 32
	pullBatchBytes = 1 << 20
)

// Peer is another yard this one is linked with, under a name of this yard's
// choosing. Nothing a peer says of itself is trusted: Calls is kept to show
// the operator, and the key alone tells whose a request is.
type Peer struct {
	Name string `json:"name"`
	// Key is its public key (yardkey.Public).
	Key string `json:"key"`
	// URL is where it can be called; empty for a yard that cannot be, which
	// calls this one and asks for what waits.
	URL string `json:"url,omitempty"`
	// Calls is what the yard called itself in its invite.
	Calls string `json:"calls,omitempty"`
	// Send and Receive are the kinds of messages the link carries, each
	// direction on its own. A new link carries none.
	Send    []string  `json:"send"`
	Receive []string  `json:"receive"`
	AddedAt time.Time `json:"added_at"`
	// Since is the last event logged before the link was added, and Was
	// the names it had before a rename, oldest first: together they say
	// which events that name a peer mean this one (peerrename.go).
	Since uint64    `json:"since,omitempty"`
	Was   []PeerWas `json:"was,omitempty"`
	// Pulled is the highest sequence number of the peer's outbox this yard
	// has taken by asking, which its next question starts after.
	Pulled uint64 `json:"pulled,omitempty"`
	// Refused are the messages taken by asking that this yard did not
	// take, by their sequence number in the peer's outbox, with the reason:
	// the next question tells the peer, which has no other way to know.
	Refused map[uint64]string `json:"refused,omitempty"`
	// Mails counts the mail that came from the peer in the hour that began
	// at MailSince, and MailLimited says that the hour's refusal is logged
	// (PeerMailLimit).
	MailSince   *time.Time `json:"mail_since,omitempty"`
	Mails       int        `json:"mails,omitempty"`
	MailLimited bool       `json:"mail_limited,omitempty"`
	// Intake is where the beads it sends go; nil, and each is refused
	// (peerbeads.go). Build is the yardr its last bead said it runs: its
	// own word, kept to show the operator.
	Intake *PeerIntake `json:"intake,omitempty"`
	Build  string      `json:"build,omitempty"`
	// Said is what its yard last said its build understands, and when; nil
	// for one that has not said, which is an older yardr. Its own word,
	// which may be stale: kept to show the operator, and nothing is
	// refused by it.
	Said *PeerSaid `json:"said,omitempty"`
	// HeardAt is when the yard was last heard from: a request it signed
	// that this yard took, or its signed answer to one of this yard's. Kept
	// to the minute (peerHeardEvery).
	HeardAt *time.Time `json:"heard_at,omitempty"`
	// Away counts this yard's beads that are at the peer now, on a peer
	// read by its name, as Beads is. Read only.
	Away int `json:"away"`
	// Beads counts the open beads that came from it, on a peer read by its
	// name (GetPeer): a listing, which the daemon asks for on every pass,
	// does not count them. Read only.
	Beads int `json:"beads"`
	// Waiting and Failed count the outbox for it, and Error is what the
	// last try of its oldest waiting message said. Read only.
	Waiting int    `json:"waiting"`
	Failed  int    `json:"failed"`
	Error   string `json:"error,omitempty"`
}

// PeerMessage is what crosses a link: an id the sender mints, by which the
// receiver takes it once, a kind, the id of the message it answers, if any,
// and a JSON body. Seq is its place in the sender's outbox, set where a
// pull hands it over.
type PeerMessage struct {
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	Re   string          `json:"re,omitempty"`
	Body json.RawMessage `json:"body"`
	Seq  uint64          `json:"seq,omitempty"`
}

// PeerOut is a message in the outbox for a peer.
type PeerOut struct {
	Peer string `json:"peer"`
	PeerMessage
	At time.Time `json:"at"`
	// Attempts counts the posts that failed, Error is what the last one
	// said, and RetryAt when the next is due.
	Attempts int        `json:"attempts,omitempty"`
	Error    string     `json:"error,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
	// Failed is when the message was given up. It stays in the outbox, out
	// of the way of those behind it, until the peer is removed or a prune
	// finds it old enough (prunePeers).
	Failed *time.Time `json:"failed,omitempty"`
	// Tries is how often the message is posted before it is given up, when
	// that is not the daemon's own count: a bead's, set by its group
	// (GroupTries).
	Tries int `json:"tries,omitempty"`
}

// PeerAllow changes what a link carries: kinds to allow and to stop
// allowing, each direction on its own.
type PeerAllow struct {
	Send      []string `json:"send,omitempty"`
	Receive   []string `json:"receive,omitempty"`
	NoSend    []string `json:"no_send,omitempty"`
	NoReceive []string `json:"no_receive,omitempty"`
}

// PeerBatch is a peer's answer to a pull, as the daemon got it: the store
// checks the peer's signature on it itself (PeerPulled), so nothing on the
// socket can hand in a message no peer sent.
type PeerBatch struct {
	// RequestSig is the signature of the request this answers.
	RequestSig string `json:"request_sig"`
	Signature  string `json:"signature"`
	Body       []byte `json:"body"`
	// Told are the refusals (Peer.Refused, by sequence number) the request
	// carried, which the peer has now heard of. It is the daemon's own
	// word, not the peer's: all it can do is stop a refusal being told
	// again.
	Told []uint64 `json:"told,omitempty"`
}

// PeerAnswer is a signed answer to a posted message. The store checks its
// signature before keeping what it says, as it does for a pull page.
type PeerAnswer struct {
	Status     int    `json:"status"`
	RequestSig string `json:"request_sig"`
	Signature  string `json:"signature"`
	Body       []byte `json:"body"`
}

// PeerPage is the body of an answer to a pull. Push says that the yard
// asked posts what it has for the asker, so there is nothing to ask for.
type PeerPage struct {
	Messages []PeerMessage `json:"messages"`
	Push     bool          `json:"push,omitempty"`
	// Says is what the yard asked states of its build, which the receiver
	// adds to the page it answers with.
	Says *PeerSays `json:"says,omitempty"`
}

// PeerTaken says what became of a message that came in.
type PeerTaken struct {
	ID string `json:"id"`
	// Duplicate: the id was taken before, and this one dropped.
	Duplicate bool `json:"duplicate,omitempty"`
	// Refused is why the message was not taken ("" when it was): this build
	// does not know its kind (RefusedUnknownKind), the link does not carry
	// it this way (RefusedKind), or one of the reasons mail (peermail.go)
	// or a bead (peerbeads.go) is refused for.
	Refused string `json:"refused,omitempty"`
}

var (
	peerMessageID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	peerKind      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// check refuses a message that is not one: on the way out, and before
// anything of one that came in is kept. What a peer sends is data, so its
// id and kind are held to a few plain characters before they become keys
// and event fields.
func (m PeerMessage) check() error {
	switch {
	case !peerMessageID.MatchString(m.ID):
		return fmt.Errorf("peer message: id of up to 64 letters, digits, - and _ wanted")
	case !peerKind.MatchString(m.Kind):
		return fmt.Errorf("peer message %s: kind of lowercase letters, digits, - and _ wanted", m.ID)
	case m.Re != "" && !peerMessageID.MatchString(m.Re):
		return fmt.Errorf("peer message %s: re is no message id", m.ID)
	case len(m.Body) > MaxPeerBody:
		return fmt.Errorf("peer message %s: body of %d bytes exceeds %d", m.ID, len(m.Body), MaxPeerBody)
	case !json.Valid(m.Body):
		return fmt.Errorf("peer message %s: the body is not JSON", m.ID)
	}
	return nil
}

func newPeerMessageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func checkKinds(kinds ...[]string) error {
	for _, ks := range kinds {
		for _, k := range ks {
			if !slices.Contains(PeerKinds, k) {
				return fmt.Errorf("%q is no kind of message between yards: there is %s", k, strings.Join(PeerKinds, ", "))
			}
		}
	}
	return nil
}

func loadPeer(tx *bolt.Tx, name string) (*Peer, error) {
	p := new(Peer)
	if ok, err := get(tx, bPeers, name, p); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("peer %s: %w%s", name, ErrNotFound, renamedTo(tx, name))
	}
	return p, nil
}

// loadPeerOf is the peer name when its key is key, the one a request's
// signature was checked against. The receiver finds the peer by its key in
// one read and takes the request in another transaction: between the two
// the name may have been given to another yard (a rename or a removal, then
// an add), and what one yard signed is never taken as the other's. A name
// whose key is another's is no peer to the one who asks.
func loadPeerOf(tx *bolt.Tx, name, key string) (*Peer, error) {
	p, err := loadPeer(tx, name)
	if err != nil {
		return nil, err
	}
	if p.Key != key {
		return nil, fmt.Errorf("peer %s: %w: it is not the yard the request is signed by", name, ErrNotFound)
	}
	return p, nil
}

// AddPeer records another yard as a peer. The link carries nothing until
// AllowPeer says what. A name or a key that is a peer already is refused
// (peerFree).
func (s *Store) AddPeer(ctx context.Context, p Peer, actor string) (*Peer, error) {
	p = Peer{Name: p.Name, Key: p.Key, URL: p.URL, Calls: p.Calls, Send: []string{}, Receive: []string{}, AddedAt: now()}
	err := s.write(func(w *wtx) error {
		if err := peerFree(w.Tx, &p); err != nil {
			return err
		}
		p.Since = w.Bucket(bEvents).Sequence()
		if err := put(w.Tx, bPeers, p.Name, p); err != nil {
			return err
		}
		data := map[string]any{"peer": p.Name, "key": p.Key}
		if p.URL != "" {
			data["url"] = p.URL
		}
		return w.event("", "peer_added", actor, data)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// outboxOf fills in what p's outbox holds.
func outboxOf(tx *bolt.Tx, p *Peer) error {
	p.Waiting, p.Failed, p.Error = 0, 0, ""
	return each(tx, bPeerOut, join(p.Name, ""), func(_ []byte, m *PeerOut) error {
		switch {
		case m.Failed != nil:
			p.Failed++
		default:
			if p.Waiting == 0 {
				p.Error = m.Error
			}
			p.Waiting++
		}
		return nil
	})
}

func (s *Store) Peers(ctx context.Context) ([]Peer, error) {
	out := []Peer{}
	err := s.read(func(tx *bolt.Tx) error {
		err := each(tx, bPeers, "", func(_ []byte, p *Peer) error {
			out = append(out, *p)
			return nil
		})
		for i := range out {
			if err == nil {
				err = outboxOf(tx, &out[i])
			}
		}
		return err
	})
	return out, err
}

func (s *Store) GetPeer(ctx context.Context, name string) (*Peer, error) {
	var p *Peer
	err := s.read(func(tx *bolt.Tx) (err error) {
		if p, err = loadPeer(tx, name); err != nil {
			return err
		}
		if err := outboxOf(tx, p); err != nil {
			return err
		}
		if err := awayAt(tx, p); err != nil {
			return err
		}
		return beadsOf(tx, p)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// PeerByKey is the peer whose public key is key. It is not on the API: the
// receiver, in the process that owns the store, is who asks.
func (s *Store) PeerByKey(ctx context.Context, key string) (*Peer, error) {
	var found *Peer
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bPeers, "", func(_ []byte, p *Peer) error {
			if p.Key == key {
				found = p
			}
			return nil
		})
	})
	if err == nil && found == nil {
		err = fmt.Errorf("peer with that key: %w", ErrNotFound)
	}
	return found, err
}

// DeletePeer removes the link: nothing more is taken from the yard or sent
// to it. What waited for it is dropped, and peer_removed says how many. A
// bead of this yard that was there ends its stage as undelivered: no
// return can come for it.
func (s *Store) DeletePeer(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		p, err := loadPeer(w.Tx, name)
		if err != nil {
			return err
		}
		if err := w.Bucket(bPeers).Delete([]byte(name)); err != nil {
			return err
		}
		data := map[string]any{"peer": name}
		if out := keys(w.Tx, bPeerOut, join(name, "")); len(out) > 0 {
			data["messages_dropped"] = len(out)
			for _, k := range out {
				if err := w.Bucket(bPeerOut).Delete([]byte(k)); err != nil {
					return err
				}
			}
		}
		// A yard added again under the name starts as a new link.
		for _, k := range keys(w.Tx, bPeerSeen, join(name, "")) {
			if err := w.Bucket(bPeerSeen).Delete([]byte(k)); err != nil {
				return err
			}
		}
		// And no crew member may mail it, or release its beads: whoever is
		// added under the name next is another yard, which the operator
		// has not allowed anyone.
		lost, err := dropPeerSettings(w.Tx, name)
		if err != nil {
			return err
		}
		if len(lost[MailPeers]) > 0 {
			data["crew"] = lost[MailPeers]
		}
		if len(lost[ReleasePeers]) > 0 {
			data["releasers"] = lost[ReleasePeers]
		}
		// The beads it sent stay, and say whose they were; nothing is
		// returned for them.
		if n, err := unlinkPeerBeads(w.Tx, p); err != nil {
			return err
		} else if n > 0 {
			data["beads"] = n
		}
		if n, err := w.unsendPeerBeads(p, fmt.Sprintf("the link with %s was removed while the bead was there", name), true); err != nil {
			return err
		} else if n > 0 {
			data["beads_away"] = n
		}
		return w.event("", "peer_removed", actor, data)
	})
}

// AllowPeer changes the kinds of messages the link with name carries. A
// link that stops carrying beads out carries no return in either, so the
// beads of this yard that are at the peer end their stage as undelivered.
func (s *Store) AllowPeer(ctx context.Context, name string, ch PeerAllow, actor string) (*Peer, error) {
	if err := checkKinds(ch.Send, ch.Receive, ch.NoSend, ch.NoReceive); err != nil {
		return nil, err
	}
	change := func(have, add, drop []string) []string {
		out := []string{}
		for _, k := range append(slices.Clone(have), add...) {
			if !slices.Contains(drop, k) && !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
		slices.Sort(out)
		return out
	}
	var p *Peer
	err := s.write(func(w *wtx) (err error) {
		if p, err = loadPeer(w.Tx, name); err != nil {
			return err
		}
		sent := p.carries(PeerBead, false)
		p.Send, p.Receive = change(p.Send, ch.Send, ch.NoSend), change(p.Receive, ch.Receive, ch.NoReceive)
		if err := put(w.Tx, bPeers, name, p); err != nil {
			return err
		}
		data := map[string]any{"peer": name, "send": p.Send, "receive": p.Receive}
		if sent && !p.carries(PeerBead, false) {
			n, err := w.unsendPeerBeads(p, fmt.Sprintf("the link with %s stopped carrying %s out while the bead was there", name, PeerBead), false)
			if err != nil {
				return err
			}
			if n > 0 {
				data["beads_away"] = n
			}
		}
		if err := w.event("", "peer_allowed", actor, data); err != nil {
			return err
		}
		return outboxOf(w.Tx, p)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// SetPeerURL changes where this yard calls the peer name: at url, held to
// what AddPeer holds one to (cleanURL), or with "" never, so that the peer
// calls this yard and asks for what waits. The link stays what it is: its
// name, its key, what it carries and what it has counted. What waits for
// the peer still waits, as it is: the daemon reads the peer on every pass,
// so a message is posted to the new address at its next try, and one for a
// peer no longer called is handed over when the peer asks. The other yard
// hears nothing of it. It logs peer_url_set.
//
// A URL the peer has already is refused, as a rename to the name it has is.
func (s *Store) SetPeerURL(ctx context.Context, name, url, actor string) (*Peer, error) {
	var p *Peer
	err := s.write(func(w *wtx) (err error) {
		if p, err = loadPeer(w.Tx, name); err != nil {
			return err
		}
		was := p.URL
		p.URL = url
		if err := p.cleanURL(); err != nil {
			return err
		}
		switch {
		case p.URL != was:
		case was == "":
			return fmt.Errorf("peer %s has no URL already: this yard never calls it", name)
		default:
			return fmt.Errorf("peer %s is called at %s already", name, was)
		}
		if err := put(w.Tx, bPeers, name, p); err != nil {
			return err
		}
		data := map[string]any{"peer": name}
		if p.URL != "" {
			data["url"] = p.URL
		}
		if was != "" {
			data["was"] = was
		}
		if err := w.event("", "peer_url_set", actor, data); err != nil {
			return err
		}
		return outboxOf(w.Tx, p)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// peerEvent is what every event about a message says: the peer, the kind
// and the id, and the message it answers. Never the body.
func peerEvent(peer string, m PeerMessage, extra map[string]any) map[string]any {
	data := map[string]any{"peer": peer, "kind": m.Kind, "id": m.ID}
	if m.Re != "" {
		data["re"] = m.Re
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

// queue puts m in the outbox for p, or logs it as refused when the link
// does not carry its kind out, and says which.
func (w *wtx) queuePeer(p *Peer, m PeerMessage, actor string) (*PeerOut, error) {
	if !p.carries(m.Kind, false) {
		return nil, w.event("", "peer_message_refused", actor, peerEvent(p.Name, m, map[string]any{"direction": "out", "reason": RefusedKind}))
	}
	seq, err := w.Bucket(bPeerOut).NextSequence()
	if err != nil {
		return nil, err
	}
	m.Seq = seq
	out := &PeerOut{Peer: p.Name, PeerMessage: m, At: now()}
	w.queued = append(w.queued, p.Key)
	return out, put(w.Tx, bPeerOut, join(p.Name, seqKey(seq)), out)
}

// PeerSend puts a message of kind for the peer name in its outbox, under an
// id minted here, for the daemon to deliver or the peer to ask for. A kind
// the link does not carry out is refused, and logged as
// peer_message_refused.
func (s *Store) PeerSend(ctx context.Context, name, kind string, body json.RawMessage, actor string) (*PeerOut, error) {
	switch kind {
	case PeerMail:
		// MailPeer is what checks that the sender may mail the peer.
		return nil, fmt.Errorf("mail goes to a peer's crew member with yardr mail <crew member>@%s", name)
	case PeerBead, PeerBeadReturn:
		// Either would be a word about a bead from whoever can reach the
		// socket: the yard says both itself, from its own beads.
		return nil, fmt.Errorf("a %s message is the yard's own to send to %s, from its own beads: it is not sent by hand", kind, name)
	}
	if len(body) == 0 {
		body = json.RawMessage("{}")
	}
	m := PeerMessage{ID: newPeerMessageID(), Kind: kind, Body: body}
	if err := m.check(); err != nil {
		return nil, err
	}
	var out *PeerOut
	err := s.write(func(w *wtx) error {
		p, err := loadPeer(w.Tx, name)
		if err != nil {
			return err
		}
		out, err = w.queuePeer(p, m, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("peer %s: the link does not carry %s out: the operator allows it with yardr peer allow %s --send %s", name, kind, name, kind)
	}
	return out, nil
}

// PeerOutbox lists the messages waiting for the peer name, oldest first, up
// to limit (0: all). Those given up are left out.
func (s *Store) PeerOutbox(ctx context.Context, name string, limit int) ([]PeerOut, error) {
	out := []PeerOut{}
	err := s.read(func(tx *bolt.Tx) error {
		if _, err := loadPeer(tx, name); err != nil {
			return err
		}
		return each(tx, bPeerOut, join(name, ""), func(_ []byte, m *PeerOut) error {
			if m.Failed == nil && (limit <= 0 || len(out) < limit) {
				out = append(out, *m)
			}
			return nil
		})
	})
	return out, err
}

// changePeerOut reads the outbox message for fn, which ends it (done) or
// leaves a changed record to keep.
func (s *Store) changePeerOut(name string, seq uint64, fn func(w *wtx, m *PeerOut) (done bool, err error)) error {
	key := join(name, seqKey(seq))
	return s.write(func(w *wtx) error {
		var m PeerOut
		if ok, err := get(w.Tx, bPeerOut, key, &m); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("message %d for peer %s: %w", seq, name, ErrNotFound)
		}
		done, err := fn(w, &m)
		if err != nil {
			return err
		}
		if done {
			return w.Bucket(bPeerOut).Delete([]byte(key))
		}
		return put(w.Tx, bPeerOut, key, m)
	})
}

func (m *PeerOut) sent(w *wtx, way string) error {
	extra := map[string]any{"way": way}
	if m.Attempts > 0 {
		extra["attempts"] = m.Attempts + 1
	}
	if err := w.sentDelivered(m); err != nil {
		return err
	}
	return w.event("", "peer_message_sent", "yard", peerEvent(m.Peer, m.PeerMessage, extra))
}

// PeerDelivered ends the outbox message seq for name: the peer took it, by
// a post of the daemon's. It logs peer_message_sent.
func (s *Store) PeerDelivered(ctx context.Context, name string, seq uint64) error {
	return s.changePeerOut(name, seq, func(w *wtx, m *PeerOut) (bool, error) {
		if m.Failed != nil {
			return false, fmt.Errorf("message %d for peer %s was given up", seq, name)
		}
		return true, m.sent(w, PeerPush)
	})
}

// PeerSendFailed records a post that failed and when the next is due, or
// with final that the message is given up: it stays, marked, and
// peer_message_failed says why. No event is logged for a try.
func (s *Store) PeerSendFailed(ctx context.Context, name string, seq uint64, reason string, retryAt time.Time, final bool) error {
	return s.changePeerOut(name, seq, func(w *wtx, m *PeerOut) (bool, error) {
		if m.Failed != nil {
			return false, nil
		}
		at := retryAt.UTC()
		m.Attempts, m.Error, m.RetryAt = m.Attempts+1, reason, &at
		if !final {
			return false, nil
		}
		return false, m.giveUp(w, reason, nil, false)
	})
}

// PeerRefused gives the outbox message seq for name up because the peer
// said, signed, that it does not take it: no further try changes that.
// reason is the peer's refusal in this yard's words (PeerRefusal). A bead
// it was sending ends its stage as refused, where one given up after its
// tries (PeerSendFailed) ends as undelivered.
func (s *Store) PeerRefused(ctx context.Context, name string, seq uint64, reason string) error {
	return s.changePeerOut(name, seq, func(w *wtx, m *PeerOut) (bool, error) {
		if m.Failed != nil {
			return false, nil
		}
		m.Attempts++
		return false, m.giveUp(w, reason, nil, true)
	})
}

// giveUp marks the message as given up, for reason: it stays in the outbox
// until it is pruned, peer_message_failed says why, mail's sender is told
// (undelivered), and a bead ends its stage (sentGivenUp). refused says the
// peer refused it, where otherwise it was not reached.
func (m *PeerOut) giveUp(w *wtx, reason string, extra map[string]any, refused bool) error {
	gone := now()
	m.Failed, m.RetryAt, m.Error = &gone, nil, reason
	data := map[string]any{"attempts": m.Attempts, "reason": reason}
	for k, v := range extra {
		data[k] = v
	}
	if err := w.event("", "peer_message_failed", "yard", peerEvent(m.Peer, m.PeerMessage, data)); err != nil {
		return err
	}
	if err := w.sentGivenUp(m, reason, refused); err != nil {
		return err
	}
	return w.undelivered(m, reason)
}

// PeerHandOver answers a pull by the peer name, whose question was signed
// with key (loadPeerOf): what it says it has (up to
// after) is ended as sent, and the next of what waits for it is handed
// over. A peer this yard calls gets nothing this way, only the word that
// it is posted to: one message would otherwise cross twice.
//
// refused names, by sequence number, what the peer got and did not take,
// with its reason: such a message is given up, not sent, as one a post was
// answered 403 for is. The reason is the peer's word, and only one this
// yard knows is repeated (PeerRefusal).
//
// A peer asks again as soon as it is answered, so a question that ends
// nothing writes nothing: no write, no pass of the daemon for it.
//
// It is not on the API: only the receiver, which has checked the peer's
// signature on the question, may end a message as sent by pull.
func (s *Store) PeerHandOver(ctx context.Context, name, key string, after uint64, refused map[uint64]string) (*PeerPage, error) {
	var page *PeerPage
	var taken []PeerOut
	hand := func(tx *bolt.Tx) error {
		page, taken = &PeerPage{Messages: []PeerMessage{}}, nil // a batched write may run this twice
		p, err := loadPeerOf(tx, name, key)
		if err != nil {
			return err
		}
		if p.URL != "" {
			page.Push = true
			return nil
		}
		size := 0
		return each(tx, bPeerOut, join(name, ""), func(_ []byte, m *PeerOut) error {
			switch {
			case m.Failed != nil:
			case m.Seq <= after:
				taken = append(taken, *m)
			case len(page.Messages) < pullBatch && (size == 0 || size+len(m.Body) <= pullBatchBytes):
				size += len(m.Body)
				page.Messages = append(page.Messages, m.PeerMessage)
			}
			return nil
		})
	}
	if err := s.read(hand); err != nil || len(taken) == 0 {
		return page, err
	}
	// Read again in the write: the outbox may have changed between the two,
	// and the name may be another yard's.
	err := s.write(func(w *wtx) error {
		if err := hand(w.Tx); err != nil {
			return err
		}
		for _, m := range taken {
			key := join(name, seqKey(m.Seq))
			if reason, no := refused[m.Seq]; no {
				if err := m.giveUp(w, "refused by the peer: "+PeerRefusal(m.Peer, m.Kind, reason), map[string]any{"way": PeerPull}, true); err != nil {
					return err
				}
				if err := put(w.Tx, bPeerOut, key, m); err != nil {
					return err
				}
				continue
			}
			if err := m.sent(w, PeerPull); err != nil {
				return err
			}
			if err := w.Bucket(bPeerOut).Delete([]byte(key)); err != nil {
				return err
			}
		}
		return nil
	})
	return page, err
}

// receive takes the messages a peer sent, each once: one of an id taken
// before is dropped, one of a kind this build does not know (a newer
// yardr's) or the link does not carry in is refused and logged, and every
// other is logged as peer_message_received. A refusal is of that message
// alone: those that came with it are taken. What is
// no message at all fails a post whole, and is passed by in a pull. A ping is
// answered here, in the same write, with a pong in the outbox for the peer,
// and mail is put in its crew member's inbox, or refused (checkMail). A bead
// becomes a bead of this yard, or is refused (checkBead); tk is what
// that needs of its intake's depot (taking) and own this yard's key,
// which a bead that has been here before names. The return of a bead ends
// the stay of this yard's bead at the peer (returnedBead). No other body is
// kept: what a peer sends is data.
//
// It changes p (the count of its mail, the build a bead names), which the
// caller saves.
func (w *wtx) receivePeer(p *Peer, msgs []PeerMessage, way string, tk *taking, own string) ([]PeerTaken, error) {
	out := make([]PeerTaken, 0, len(msgs))
	actor := "peer:" + p.Name
	seen := w.Bucket(bPeerSeen)
	for _, m := range msgs {
		if err := m.check(); err != nil {
			if way == PeerPush {
				return nil, err
			}
			// Asked for, it is passed by: answered with an error it would
			// be handed over again on every question, and hold up all
			// that waits behind it. Nothing of it is kept, its id and
			// kind included: they are what is wrong with it.
			if err := w.event("", "peer_message_refused", actor, map[string]any{"peer": p.Name, "direction": "in", "reason": "not_a_message", "way": way, "seq": m.Seq}); err != nil {
				return nil, err
			}
			out = append(out, PeerTaken{Refused: "not_a_message"})
			continue
		}
		t := PeerTaken{ID: m.ID}
		key := []byte(join(p.Name, m.ID))
		switch {
		case seen.Get(key) != nil:
			t.Duplicate = true
		case !knownKind(m.Kind):
			// Said apart from a kind the link does not carry: no peer allow
			// here lets it in, only a newer yardr.
			t.Refused = RefusedUnknownKind
			extra := map[string]any{"direction": "in", "reason": t.Refused, "way": way, "detail": "this yard's yardr does not know " + m.Kind}
			if err := w.event("", "peer_message_refused", actor, peerEvent(p.Name, m, extra)); err != nil {
				return nil, err
			}
		case !p.carries(m.Kind, true):
			t.Refused = RefusedKind
			if err := w.event("", "peer_message_refused", actor, peerEvent(p.Name, m, map[string]any{"direction": "in", "reason": t.Refused, "way": way})); err != nil {
				return nil, err
			}
		default:
			var mail *peerMail
			var bead *arrival
			var err error
			switch m.Kind {
			case PeerMail:
				mail, t.Refused, err = w.checkMail(p, m, way)
			case PeerBead:
				bead, t.Refused, err = w.checkBead(p, m, way, tk, own)
			case PeerBeadReturn:
				t.Refused, err = w.returnedBead(p, m, way)
			}
			if err != nil {
				return nil, err
			} else if t.Refused != "" {
				break
			}
			at := make([]byte, 8)
			binary.BigEndian.PutUint64(at, uint64(now().Unix()))
			if err := seen.Put(key, at); err != nil {
				return nil, err
			}
			if err := w.event("", "peer_message_received", actor, peerEvent(p.Name, m, map[string]any{"way": way})); err != nil {
				return nil, err
			}
			// A message that answers another is never answered, so two
			// yards cannot keep each other busy.
			if m.Kind == PeerPing && m.Re == "" {
				pong := PeerMessage{ID: newPeerMessageID(), Kind: PeerPing, Re: m.ID, Body: json.RawMessage("{}")}
				if _, err := w.queuePeer(p, pong, "yard"); err != nil {
					return nil, err
				}
			}
			if mail != nil {
				if err := w.deliverMail(p, m, mail); err != nil {
					return nil, err
				}
			}
			if bead != nil {
				if err := w.arriveBead(p, m, bead, way); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// PeerReceive takes messages the peer name posted (see receivePeer), signed
// with key: the name is taken only while it is that key's (loadPeerOf). It is
// not on the API: only the receiver, in the process that owns the store,
// has checked whose they are, so no client can hand in one of its own.
func (s *Store) PeerReceive(ctx context.Context, name, key string, msgs []PeerMessage) ([]PeerTaken, error) {
	var out []PeerTaken
	tk, own := s.taking(ctx, name, msgs), s.yardKey()
	err := s.write(func(w *wtx) error {
		p, err := loadPeerOf(w.Tx, name, key)
		if err != nil {
			return err
		}
		if out, err = w.receivePeer(p, msgs, PeerPush, tk, own); err != nil {
			return err
		}
		return put(w.Tx, bPeers, name, p)
	})
	if err == nil {
		tk.keep(ctx, s)
	}
	return out, err
}

// PeerHeard keeps what the peer name said of its build with a request
// signed with key (Peer.heard); says is nil for one that said nothing. A
// peer asks every few seconds and says the same each time, so it writes
// only what changed. Like PeerReceive it is not on the API: the receiver
// has checked whose the request is.
func (s *Store) PeerHeard(ctx context.Context, name, key string, says *PeerSays) error {
	changed := false
	hear := func(tx *bolt.Tx) (*Peer, error) {
		p, err := loadPeerOf(tx, name, key)
		if err != nil {
			return nil, err
		}
		changed = p.heard(says)
		return p, nil
	}
	err := s.read(func(tx *bolt.Tx) error {
		_, err := hear(tx)
		return err
	})
	if err != nil || !changed {
		return err
	}
	return s.write(func(w *wtx) error {
		p, err := hear(w.Tx)
		if err != nil || !changed {
			return err
		}
		return put(w.Tx, bPeers, name, p)
	})
}

// PeerPostAnswered keeps what a peer says in its signed answer to a post.
func (s *Store) PeerPostAnswered(ctx context.Context, name string, answer PeerAnswer) error {
	var says *PeerSays
	changed := false
	check := func(tx *bolt.Tx) (*Peer, error) {
		p, err := loadPeer(tx, name)
		if err != nil {
			return nil, err
		}
		if err := yardkey.VerifyResponse(p.Key, answer.Status, answer.RequestSig, answer.Signature, answer.Body); err != nil {
			return nil, fmt.Errorf("peer %s: %w", name, err)
		}
		var reply struct {
			Says *PeerSays `json:"says"`
		}
		if err := json.Unmarshal(answer.Body, &reply); err != nil {
			return nil, fmt.Errorf("peer %s: its answer is not JSON", name)
		}
		says = reply.Says
		changed = p.heard(says)
		return p, nil
	}
	err := s.read(func(tx *bolt.Tx) error {
		_, err := check(tx)
		return err
	})
	if err != nil || !changed {
		return err
	}
	return s.write(func(w *wtx) error {
		p, err := check(w.Tx)
		if err != nil || !changed {
			return err
		}
		return put(w.Tx, bPeers, name, p)
	})
}

// PeerPulled takes what the peer name handed over when the daemon asked.
// The peer's signature on the answer is checked here, against the key the
// link was made with, so the daemon is trusted with nothing: what it passes
// on is the peer's or is refused. The next question starts after the
// highest sequence number taken. It returns what became of each message,
// and whether the peer posts instead.
//
// What was not taken is kept with its reason (Peer.Refused) for the next
// question to tell the peer, and what b says the peer has been told of is
// forgotten.
//
// The answer also says what the peer's build understands, which is kept
// (Peer.heard): it is under the same signature.
//
// An answer that hands over nothing, told of nothing and says nothing new
// writes nothing: the daemon asks again as soon as it is answered, and a
// write would wake it for nothing.
func (s *Store) PeerPulled(ctx context.Context, name string, b PeerBatch) (*PeerPulled, error) {
	var page PeerPage
	told, said := false, false
	check := func(tx *bolt.Tx) (*Peer, error) {
		p, err := loadPeer(tx, name)
		if err != nil {
			return nil, err
		}
		if err := yardkey.VerifyResponse(p.Key, 200, b.RequestSig, b.Signature, b.Body); err != nil {
			return nil, fmt.Errorf("peer %s: %w", name, err)
		}
		page = PeerPage{}
		if err := json.Unmarshal(b.Body, &page); err != nil {
			return nil, fmt.Errorf("peer %s: its answer is not a page of messages", name)
		}
		told = false
		for _, seq := range b.Told {
			if _, ok := p.Refused[seq]; ok {
				told = true
			}
		}
		said = p.heard(page.Says)
		return p, nil
	}
	err := s.read(func(tx *bolt.Tx) error {
		_, err := check(tx)
		return err
	})
	res := &PeerPulled{Taken: []PeerTaken{}, Push: page.Push}
	if err != nil {
		return nil, err
	}
	if len(page.Messages) == 0 && !told && !said {
		return res, nil
	}
	tk, own := s.taking(ctx, name, page.Messages), s.yardKey()
	err = s.write(func(w *wtx) error {
		// Checked again in the write: the peer may have been removed, or
		// added anew with another key, between the two.
		p, err := check(w.Tx)
		if err != nil {
			return err
		}
		if res.Taken, err = w.receivePeer(p, page.Messages, PeerPull, tk, own); err != nil {
			return err
		}
		for _, seq := range b.Told {
			delete(p.Refused, seq)
		}
		for i, m := range page.Messages {
			p.Pulled = max(p.Pulled, m.Seq)
			if no := res.Taken[i].Refused; no != "" && m.Seq > 0 {
				if p.Refused == nil {
					p.Refused = map[uint64]string{}
				}
				p.Refused[m.Seq] = no
			}
		}
		if len(p.Refused) == 0 {
			p.Refused = nil
		}
		return put(w.Tx, bPeers, name, p)
	})
	if err != nil {
		return nil, err
	}
	tk.keep(ctx, s)
	return res, nil
}

// PeerPulled is what a pull brought.
type PeerPulled struct {
	Taken []PeerTaken `json:"taken"`
	Push  bool        `json:"push,omitempty"`
}

// PeerKeep is the least a link's seen ids and its messages given up are
// kept, however short the retention of events is set. An id forgotten is
// taken again as new, so it must outlast every way the message can come
// back: a sender gives up after 24 tries, most of a day, and a pull hands a
// message over again only until the next question.
const PeerKeep = 7 * 24 * time.Hour

// PeerPruned counts what a prune removed of one link: the ids of messages
// taken from the peer (Seen) and the messages for it that were given up
// (Failed).
type PeerPruned struct {
	Seen   int `json:"seen,omitempty"`
	Failed int `json:"failed,omitempty"`
}

// prunePeers removes up to pruneBatch of the ids taken from peers before
// cutoff, and as many of the outbox messages given up before it, with
// their bodies, and counts them in res by peer; more reports that a full
// batch went. A message that still waits is never removed by its age: it
// is the daemon's to deliver or give up.
//
// A message of a forgotten id that comes again is taken as new. Nothing
// sends one that late (PeerKeep).
func (s *Store) prunePeers(cutoff time.Time, res *PruneResult) (more bool, err error) {
	var counted map[string]PeerPruned
	err = s.write(func(w *wtx) error {
		more, counted = false, map[string]PeerPruned{} // a batched write may run this twice
		count := func(key []byte, seen, failed int) {
			name, _, _ := bytes.Cut(key, []byte{0})
			c := counted[string(name)]
			c.Seen, c.Failed = c.Seen+seen, c.Failed+failed
			counted[string(name)] = c
		}
		var old [][]byte
		seen := w.Bucket(bPeerSeen)
		if err := seen.ForEach(func(k, v []byte) error {
			if len(v) != 8 || int64(binary.BigEndian.Uint64(v)) >= cutoff.Unix() {
				return nil
			}
			if len(old) == pruneBatch {
				more = true
				return errStop
			}
			old = append(old, bytes.Clone(k))
			return nil
		}); err != nil && !errors.Is(err, errStop) {
			return err
		}
		for _, k := range old {
			if err := seen.Delete(k); err != nil {
				return err
			}
			count(k, 1, 0)
		}
		old = nil
		if err := each(w.Tx, bPeerOut, "", func(k []byte, m *PeerOut) error {
			if m.Failed == nil || !m.Failed.Before(cutoff) {
				return nil
			}
			if len(old) == pruneBatch {
				more = true
				return errStop
			}
			old = append(old, bytes.Clone(k))
			return nil
		}); err != nil && !errors.Is(err, errStop) {
			return err
		}
		for _, k := range old {
			if err := w.Bucket(bPeerOut).Delete(k); err != nil {
				return err
			}
			count(k, 0, 1)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	for name, c := range counted {
		if res.Peers == nil {
			res.Peers = map[string]PeerPruned{}
		}
		was := res.Peers[name]
		res.Peers[name] = PeerPruned{Seen: was.Seen + c.Seen, Failed: was.Failed + c.Failed}
	}
	return more, nil
}

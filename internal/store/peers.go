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

// MaxPeerBody bounds a message's body, of every kind but a file's
// (maxPeerBody).
const MaxPeerBody = 256 << 10

// PeerPing is a ping, which the other yard answers with a message of the
// same kind that names it (Re), the pong. A ping therefore needs the kind
// allowed both ways on both sides.
const PeerPing = "ping"

// PeerKinds are the kinds of messages a link can be allowed to carry.
// PeerMail is in peermail.go, PeerFile in peerfiles.go, PeerAsk in
// peerasks.go, PeerBead, which allows the answer to a bead too, in
// peerbeads.go, and PeerThread, which allows what is said of a bead while it
// is away, in peerthread.go. They are also what this build says it
// understands (Says).
var PeerKinds = []string{PeerAsk, PeerBead, PeerFile, PeerMail, PeerPing, PeerThread}

// knownKind reports whether this build knows a message of kind: one a link
// can be allowed to carry, the answer to a bead, which goes with the bead's
// word, or a bead event, a read or a note, which go with PeerThread. That word is
// the kind of no message itself. Any other is a newer yardr's.
func knownKind(kind string) bool {
	switch kind {
	case PeerBeadReturn, PeerBeadEvent, PeerBeadRead, PeerBeadNote:
		return true
	case PeerThread:
		return false
	}
	return slices.Contains(PeerKinds, kind)
}

// PeerSays is what a yard states of its build with every signed request
// and every answer to one: the kinds it understands (PeerKinds, there) and
// its yardr. Two yards are not upgraded on the same day, and a yard is
// upgraded long after it was linked, so it is said every time and not at
// pairing. A build from before it was said ignores the field and says
// nothing.
//
// Roster is who of the yard the peer it speaks to may see and write to
// (Peer.Expose, peerroster.go): said with the rest, so the other yard has
// it as it is now, and one left out of it is gone there with the next
// exchange.
type PeerSays struct {
	Understands []string          `json:"understands"`
	Yardr       string            `json:"yardr,omitempty"`
	Roster      []PeerRosterEntry `json:"roster,omitempty"`
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
// build of a version's characters, and the roster held to what one is
// (cleanRoster). nil when it named no kinds at all, as a yardr from before
// this was said does not.
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
	out.Roster = cleanRoster(s.Roster)
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
		p.distrust()
		return true
	case p.Said != nil && p.Said.Yardr == says.Yardr && slices.Equal(p.Said.Understands, says.Understands) &&
		slices.Equal(p.Said.Roster, says.Roster) && now().Before(p.Said.At.Add(peerSaidEvery)):
		return false
	}
	p.Said = &PeerSaid{PeerSays: *says, At: now()}
	p.distrust()
	return true
}

// The ways a message crosses a link, as its events name them: posted by the
// sender, or asked for by the receiver.
const (
	PeerPush = "push"
	PeerPull = "pull"
)

// pullBatch bounds what one pull hands over; the rest waits for the next.
// A message larger than pullBatchBytes, which only a file is, is handed
// over on its own.
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
	Send    []string `json:"send"`
	Receive []string `json:"receive"`
	// Expose names the crew members and users of this yard the peer may
	// see and write to, sorted; nobody on a new link. Roster is what this
	// yard says of them to the peer (peerroster.go), on a peer the store
	// hands out. Read only.
	Expose []string          `json:"expose,omitempty"`
	Roster []PeerRosterEntry `json:"roster,omitempty"`
	// Trust names those of the peer's yard whose mail counts as a user of
	// this yard speaking, and whose answer to a question of this yard is
	// marked as that user's word: the name on the peer's roster -> the
	// user (peertrust.go). untrusted are the names what the peer said just
	// took out of it (distrust), for the write that saves the peer to log.
	Trust     map[string]string `json:"trust,omitempty"`
	untrusted []string
	// Spoke are the grants of Trust the link has no more, oldest first:
	// what was written to such a name while it was trusted stays in the
	// user's conversations (history, peertrust.go).
	Spoke []PeerSpoke `json:"spoke,omitempty"`
	// Reach names those of the peer's yard an ask for a user of this yard
	// is also sent to, and whose answer to it is taken: the name -> the
	// user, each a name Trust has as that user (peertrust.go). unreached
	// are the names what the peer said just took out of it.
	Reach     map[string]string `json:"reach,omitempty"`
	unreached []string
	// Identities say which senders of the sources this yard hears by
	// identity are crew members of the peer's yard: a delivery from such a
	// sender is kept with the speaker <crew>@<peer>, and read as that
	// yard's mail is (peeridentity.go). Sorted by source, then crew. It goes
	// with the link: a yard added under the name later starts with none.
	Identities []PeerIdentity `json:"identities,omitempty"`
	AddedAt    time.Time      `json:"added_at"`
	// Since is the last event logged before the link was added, and Was
	// the names it had before a rename, oldest first: together they say
	// which events that name a peer mean this one (peerrename.go).
	Since uint64    `json:"since,omitempty"`
	Was   []PeerWas `json:"was,omitempty"`
	// Pulled is the highest sequence number of the peer's outbox this yard
	// has taken by asking, which its next question starts after.
	Pulled uint64 `json:"pulled,omitempty"`
	// Refused are the messages taken by asking that this yard did not
	// take, by their sequence number in the peer's outbox, with why: the
	// next question tells the peer, which has no other way to know.
	Refused map[uint64]Refusal `json:"refused,omitempty"`
	// MailLimit is how much mail the yard takes from the peer in an hour,
	// where the operator set it (PeerAllow.MailLimit); 0 on a link that
	// takes PeerMailLimit. Read it as MailsAnHour.
	MailLimit int `json:"mail_limit,omitempty"`
	// Mails counts the mail that came from the peer in the hour that began
	// at MailSince, and MailLimited says that the hour's refusal is logged
	// (MailsAnHour). MailTold are those of this yard told in that hour
	// that mail for them was refused (mailRefused). Files and FileLimited
	// are the same of its files, in the same hour (FilesAnHour).
	MailSince   *time.Time `json:"mail_since,omitempty"`
	Mails       int        `json:"mails,omitempty"`
	MailLimited bool       `json:"mail_limited,omitempty"`
	MailTold    []string   `json:"mail_told,omitempty"`
	Files       int        `json:"files,omitempty"`
	FileLimited bool       `json:"file_limited,omitempty"`
	// Events and EventLimited are the same of its bead events, in the same
	// hour (PeerEventLimit).
	Events       int  `json:"events,omitempty"`
	EventLimited bool `json:"event_limited,omitempty"`
	// Arrivals and ArrivalLimited are the same of the beads it sent, taken
	// or refused, in the same hour (PeerBeadLimit).
	Arrivals       int  `json:"arrivals,omitempty"`
	ArrivalLimited bool `json:"arrival_limited,omitempty"`
	// Intakes is where the beads it sends go, at most one per depot, sorted
	// by it; empty, and each is refused (peerbeads.go). Build is the yardr
	// its last bead said it runs: its own word, kept to show the operator.
	Intakes []PeerIntake `json:"intakes,omitempty"`
	Build   string       `json:"build,omitempty"`
	// Said is what its yard last said its build understands, and when; nil
	// for one that has not said, which is an older yardr. Its own word,
	// which may be stale: kept to show the operator, and nothing is
	// refused by it.
	Said *PeerSaid `json:"said,omitempty"`
	// HeardAt is when the yard was last heard from: a request it signed
	// that this yard took, or its signed answer to one of this yard's. Kept
	// to the minute (peerHeardEvery), and exactly when messages came with
	// it (receivePeer).
	HeardAt *time.Time `json:"heard_at,omitempty"`
	// Away counts this yard's beads that are at the peer now, on a peer
	// read by its name, as Beads is. Read only.
	Away int `json:"away"`
	// Beads counts the open beads that came from it, on a peer read by its
	// name (GetPeer): a listing, which the daemon asks for on every pass,
	// does not count them. Read only.
	Beads int `json:"beads"`
	// OpenByDepot counts those open beads by the depot they arrived in, on
	// a peer read by its name as Beads is. Read only.
	OpenByDepot map[string]int `json:"open_by_depot,omitempty"`
	// ArriveByDepot is who works a bead each intake takes in the stage it
	// arrives in, by the routes as they are, on a peer read by its name as
	// Beads is. Read only.
	ArriveByDepot []PeerIntakeArrive `json:"arrive_by_depot,omitempty"`
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
// allowing, each direction on its own, and whom of this yard the peer may
// see and write to (Peer.Expose), by name. Trust and NoTrust change whose
// mail from the peer counts as a user of this yard speaking (Peer.Trust):
// a name of the peer's roster -> the user, and the names to trust no more.
// Reach and NoReach change, the same way, whom of the peer an ask for a
// user of this yard is also sent to (Peer.Reach). MailLimit is how much
// mail the link takes from the peer in an hour from now on
// (Peer.MailLimit), 1 to MaxPeerMailLimit; 0 leaves it as it is.
type PeerAllow struct {
	Send      []string          `json:"send,omitempty"`
	Receive   []string          `json:"receive,omitempty"`
	NoSend    []string          `json:"no_send,omitempty"`
	NoReceive []string          `json:"no_receive,omitempty"`
	Expose    []string          `json:"expose,omitempty"`
	NoExpose  []string          `json:"no_expose,omitempty"`
	Trust     map[string]string `json:"trust,omitempty"`
	NoTrust   []string          `json:"no_trust,omitempty"`
	Reach     map[string]string `json:"reach,omitempty"`
	NoReach   []string          `json:"no_reach,omitempty"`
	MailLimit int               `json:"mail_limit,omitempty"`
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
	// it this way (RefusedKind), or one of the reasons mail (peermail.go),
	// a bead (peerbeads.go) or an ask (peerasks.go) is refused for.
	Refused string `json:"refused,omitempty"`
	// Limit and Until are the hour's, for mail refused over it
	// (RefusedRate): what the link takes and when the hour ends, which the
	// sender is told with the refusal.
	Limit int        `json:"limit,omitempty"`
	Until *time.Time `json:"until,omitempty"`
}

// A Refusal is why a message that came in was not taken: the reason, and
// for mail over the peer's hour (RefusedRate) the hour's limit and when it
// ends, which the sender is told with it.
type Refusal struct {
	Reason string     `json:"reason"`
	Limit  int        `json:"limit,omitempty"`
	Until  *time.Time `json:"until,omitempty"`
}

// UnmarshalJSON reads a refusal stored as a bare reason, from before one
// over the hour carried its limit and when it ends: a yard upgraded with a
// refusal still untold reads the whole of what it stored.
func (no *Refusal) UnmarshalJSON(raw []byte) error {
	var reason string
	if err := json.Unmarshal(raw, &reason); err == nil {
		no.Reason, no.Limit, no.Until = reason, 0, nil
		return nil
	}
	type refusal Refusal
	var v refusal
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	*no = Refusal(v)
	return nil
}

var (
	peerMessageID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	peerKind      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	// ErrPeerNotFound means the posted request's peer name and key no longer
	// name one another. Other ErrNotFound errors while taking a message are
	// errors keeping it, not an unknown peer.
	ErrPeerNotFound = errors.New("peer not found")
)

// ErrNotMessage is a posted message that is not one (check): the one error
// of PeerReceive that is the message's fault and that no other try changes.
// Any other is this yard's, which could not keep it, and the sender's to
// try again.
var ErrNotMessage = errors.New("not a message")

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
	case len(m.Body) > maxPeerBody(m.Kind):
		return fmt.Errorf("peer message %s: body of %d bytes exceeds %d", m.ID, len(m.Body), maxPeerBody(m.Kind))
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
// (peerFree). Who a yard is linked with, and what it takes from them, is
// the operator's to say (operatorOnly), here and in every change of a link
// that widens it: not for a crew member's session to grant.
func (s *Store) AddPeer(ctx context.Context, p Peer, actor string) (*Peer, error) {
	p = Peer{Name: p.Name, Key: p.Key, URL: p.URL, Calls: p.Calls, Send: []string{}, Receive: []string{}, AddedAt: now()}
	err := s.write(func(w *wtx) error {
		// Alone: a link is whom the yard talks to.
		if err := w.operatorOnly(ctx, actor, "add a peer", Alone); err != nil {
			return err
		}
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
			if err == nil {
				err = rosterOf(tx, &out[i])
			}
		}
		return err
	})
	for i := range out {
		readRoles(out[i].Roster)
	}
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
		if err := rosterOf(tx, p); err != nil {
			return err
		}
		return beadsOf(tx, p)
	})
	if err != nil {
		return nil, err
	}
	readRoles(p.Roster)
	s.arrive(ctx, p)
	return p, nil
}

// PeerByKey is the peer whose public key is key, with the roster this yard
// says to it, which the receiver's answer carries. It is not on the API:
// the receiver, in the process that owns the store, is who asks.
func (s *Store) PeerByKey(ctx context.Context, key string) (*Peer, error) {
	var found *Peer
	err := s.read(func(tx *bolt.Tx) error {
		err := each(tx, bPeers, "", func(_ []byte, p *Peer) error {
			if p.Key == key {
				found = p
			}
			return nil
		})
		if err != nil || found == nil {
			return err
		}
		return rosterOf(tx, found)
	})
	if err == nil && found == nil {
		err = fmt.Errorf("peer with that key: %w", ErrNotFound)
	}
	if found != nil {
		readRoles(found.Roster)
	}
	return found, err
}

// DeletePeer removes the link: nothing more is taken from the yard or sent
// to it. What waited for it is dropped, and peer_removed says how many. A
// bead of this yard that was there ends its stage as undelivered: no
// return can come for it. The questions it asked are dropped too, and each
// person they were for gets one mail listing theirs as text. Unlinking is
// the operator's to say (operatorOnly), as linking is: not for a crew
// member's session to grant.
func (s *Store) DeletePeer(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		// Alone: a link is whom the yard talks to.
		if err := w.operatorOnly(ctx, actor, "remove a peer", Alone); err != nil {
			return err
		}
		p, err := loadPeer(w.Tx, name)
		if err != nil {
			return err
		}
		if err := w.Bucket(bPeers).Delete([]byte(name)); err != nil {
			return err
		}
		data := map[string]any{"peer": name}
		// The identities go with the record. The event says which, because
		// those senders are heard as unmapped from now on, and a yard added
		// again under the name starts with none.
		if len(p.Identities) > 0 {
			data["identities"] = p.Identities
		}
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
		// And no crew member may mail it: whoever is added under the name
		// next is another yard, which the operator has not allowed anyone.
		lost, err := dropPeerSettings(w.Tx, name)
		if err != nil {
			return err
		}
		if len(lost[MailPeers]) > 0 {
			data["crew"] = lost[MailPeers]
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
		dropped, err := w.dropPeerAsks(p)
		if err != nil {
			return err
		}
		nAsks := 0
		for user, recs := range dropped {
			nAsks += len(recs)
			if w.Bucket(bUsers).Get([]byte(user)) == nil {
				continue
			}
			var text string
			if len(recs) == 1 {
				r := recs[0]
				text = fmt.Sprintf("The linked yard %s was removed. 1 question from it was dropped: %q on %s@%s from %s@%s.",
					name, r.Ask.Question, r.Bead, name, r.From, name)
			} else {
				lines := []string{fmt.Sprintf("The linked yard %s was removed. %d questions from it were dropped:", name, len(recs))}
				for _, r := range recs {
					lines = append(lines, fmt.Sprintf("- %q on %s@%s from %s@%s", r.Ask.Question, r.Bead, name, r.From, name))
				}
				text = strings.Join(lines, "\n")
			}
			if err := w.event("", "mail", "yard", map[string]any{"to": user, "to_user": true, "text": text}); err != nil {
				return err
			}
		}
		if nAsks > 0 {
			data["asks"] = nAsks
		}
		return w.event("", "peer_removed", actor, data)
	})
}

// AllowPeer changes the kinds of messages the link with name carries. A
// link that stops carrying beads out carries no return in either, so the
// beads of this yard that are at the peer end their stage as undelivered.
// A kind the link is to carry from now is the operator's to allow
// (operatorOnly), a run ask may propose it, whichever the kind, for the
// operator to approve (runask.go); stopping one is anyone's.
//
// It changes whom of this yard the peer may see and write to as well
// (Peer.Expose): to expose a name is the operator's too, and the name must
// be a crew member's or a user's. To stop exposing one is anyone's, and the
// name may be of somebody gone.
//
// And whose mail from the peer counts as a user of this yard speaking
// (Peer.Trust, peertrust.go), under the same rule: to trust a name is the
// operator's, to stop is anyone's.
//
// And whom of the peer an ask for a user of this yard is also sent to
// (Peer.Reach), under the same rule again. Trust is changed first, so one
// command may give both. Giving reach sends nothing that is already asked:
// an ask is copied in the write that stores it (AddAsk).
//
// And how much mail the link takes from the peer in an hour
// (Peer.MailLimit), under the same rule once more: to take more than it
// does is the operator's, to take less is anyone's. What the peer sent in
// the hour that runs stays counted, and the next refusal is logged and
// told anew, since it is by another limit.
func (s *Store) AllowPeer(ctx context.Context, name string, ch PeerAllow, actor string) (*Peer, error) {
	var p *Peer
	err := s.write(func(w *wtx) (err error) {
		p, err = w.allowPeer(ctx, name, ch, actor, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	readRoles(p.Roster)
	return p, nil
}

type peerAllowPermission struct {
	send, receive []string
	expose, trust bool
	reach         bool
	mailLimit     int
}

// checkAllowPeer is the permission and link check for AllowPeer. A run
// ask uses this same check for the asker's and operator's legs, so the
// class of a mixed change cannot differ between the row and the command.
func (w *wtx) checkAllowPeer(ctx context.Context, name string, check peerAllowPermission, actor string) (*Peer, error) {
	// The parts that expose or trust names stay alone even when this same
	// command also widens a mail or ping kind.
	switch {
	case check.expose:
		if err := w.operatorOnly(ctx, actor, "expose someone of the yard to a peer", Alone); err != nil {
			return nil, err
		}
	case check.trust:
		if err := w.operatorOnly(ctx, actor, "trust a name of a peer as a user of the yard", Alone); err != nil {
			return nil, err
		}
	case check.reach:
		if err := w.operatorOnly(ctx, actor, "send a user's asks to a name of a peer", Alone); err != nil {
			return nil, err
		}
	}
	if len(check.send)+len(check.receive) > 0 {
		// Askable, whichever the kind. Bead lets another yard start work
		// here, and ask, file and thread carry its words deeper into the
		// yard, which is why they were alone: the card names what the link
		// carries before and after, and an approval that reaches the yard
		// through that peer's own reach is refused (approveAsk), so a peer
		// cannot widen its own link.
		if err := w.operatorOnly(ctx, actor, "allow a peer's messages", Askable); err != nil {
			return nil, err
		}
	}
	p, err := loadPeer(w.Tx, name)
	if err != nil {
		return nil, err
	}
	if check.mailLimit > p.MailsAnHour() {
		if err := w.operatorOnly(ctx, actor, "take more mail from a peer", Askable); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// changeList is the list have with add put in and drop taken out, sorted,
// each once: what a link carries or exposes after a peer allow, which the
// write stores and a run ask's card names beside what it holds now.
func changeList(have, add, drop []string) []string {
	out := []string{}
	for _, k := range append(slices.Clone(have), add...) {
		if !slices.Contains(drop, k) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// allowPeer is AllowPeer inside the transaction, so that an approved run
// ask makes the change in the write that records its approval (ApproveAsk):
// by is then the ask it was approved on, which the event carries.
func (w *wtx) allowPeer(ctx context.Context, name string, ch PeerAllow, actor string, by *approvedOn) (p *Peer, err error) {
	if err := checkKinds(ch.Send, ch.Receive, ch.NoSend, ch.NoReceive); err != nil {
		return nil, err
	}
	if ch.MailLimit < 0 || ch.MailLimit > MaxPeerMailLimit {
		return nil, fmt.Errorf("peer %s: a link takes 1 to %d mails an hour, not %d", name, MaxPeerMailLimit, ch.MailLimit)
	}
	p, err = w.checkAllowPeer(ctx, name, peerAllowPermission{
		send: ch.Send, receive: ch.Receive,
		expose: len(ch.Expose) > 0, trust: len(ch.Trust) > 0, reach: len(ch.Reach) > 0,
		mailLimit: ch.MailLimit,
	}, actor)
	if err != nil {
		return nil, err
	}
	change := changeList
	if ch.MailLimit > 0 && ch.MailLimit != p.MailsAnHour() {
		// The default is kept as no setting, so it is the default's
		// still should that change.
		if p.MailLimit = ch.MailLimit; p.MailLimit == PeerMailLimit {
			p.MailLimit = 0
		}
		p.MailLimited, p.FileLimited, p.MailTold = false, false, nil
	}
	had := len(p.Reach)
	if err := p.trust(w.Tx, ch.Trust, ch.NoTrust); err != nil {
		return nil, err
	}
	if err := p.reach(ch.Reach, ch.NoReach); err != nil {
		return nil, err
	}
	for _, who := range ch.Expose {
		if err := exposable(w.Tx, who); err != nil {
			return nil, err
		}
	}
	sent := p.carries(PeerBead, false)
	p.Send, p.Receive = change(p.Send, ch.Send, ch.NoSend), change(p.Receive, ch.Receive, ch.NoReceive)
	switch p.Expose = change(p.Expose, ch.Expose, ch.NoExpose); {
	case len(p.Expose) == 0:
		p.Expose = nil
	case len(p.Expose) > MaxPeerExpose && len(ch.Expose) > 0:
		return nil, fmt.Errorf("peer %s: a link exposes at most %d names, and this would make it %d", name, MaxPeerExpose, len(p.Expose))
	}
	if err := put(w.Tx, bPeers, name, p); err != nil {
		return nil, err
	}
	data := map[string]any{"peer": name, "send": p.Send, "receive": p.Receive}
	if len(ch.Expose)+len(ch.NoExpose) > 0 {
		data["expose"] = change(p.Expose, nil, nil)
	}
	if len(ch.Trust)+len(ch.NoTrust) > 0 {
		data["trust"] = p.Trusted()
	}
	// With trust too: a name trusted no more is reached no more.
	if len(ch.Reach)+len(ch.NoReach) > 0 || had != len(p.Reach) {
		data["reach"] = p.Reaching()
	}
	if ch.MailLimit > 0 {
		data["mail_limit"] = p.MailsAnHour()
	}
	if sent && !p.carries(PeerBead, false) {
		n, err := w.unsendPeerBeads(p, fmt.Sprintf("the link with %s stopped carrying %s out while the bead was there", name, PeerBead), false)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			data["beads_away"] = n
		}
	}
	if by != nil {
		data["approved"] = by
	}
	if err := w.event("", "peer_allowed", actor, data); err != nil {
		return nil, err
	}
	if err := rosterOf(w.Tx, p); err != nil {
		return nil, err
	}
	return p, outboxOf(w.Tx, p)
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
		// Alone: the address is where the yard's calls go.
		if err := w.operatorOnly(ctx, actor, "change where a peer is called", Alone); err != nil {
			return err
		}
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
// peer_message_refused. From a session's or a crew member's pane the send
// is recorded as that session's actor or that crew member, whatever actor
// says; from anywhere else, as given (paneAuthor).
func (s *Store) PeerSend(ctx context.Context, name, kind string, body json.RawMessage, actor string) (*PeerOut, error) {
	switch kind {
	case PeerMail:
		// MailPeer is what checks that the sender may mail the peer.
		return nil, fmt.Errorf("mail goes to a peer's crew member with yardr mail <crew member>@%s", name)
	case PeerFile:
		// A file crosses for the mail that names it, and is checked there.
		return nil, fmt.Errorf("a file goes to a peer with the mail that carries it: yardr mail <crew member>@%s --file <path>", name)
	case PeerBead, PeerBeadReturn, PeerBeadEvent:
		// Each would be a word about a bead from whoever can reach the
		// socket: the yard says them itself, from its own beads.
		return nil, fmt.Errorf("a %s message is the yard's own to send to %s, from its own beads: it is not sent by hand", kind, name)
	case PeerBeadRead:
		// PeerRead is what checks that the reader may read at the peer.
		return nil, fmt.Errorf("a bead that is at %s is read with yardr thread show <bead>", name)
	case PeerBeadNote:
		// PeerNote is what checks that the writer may write at the peer.
		return nil, fmt.Errorf("a note goes on %s's bead with yardr reply <bead> \"<text>\"", name)
	case PeerThread:
		return nil, fmt.Errorf("%s is what a link is allowed to carry of a bead that is away, and the kind of no message: the yard sends what it covers itself", kind)
	case PeerAsk:
		// An ask crosses from the yard's own asks, asked with the command:
		// a socket client names no question of its own.
		return nil, fmt.Errorf("an ask goes to a person of a peer's yard with yardr bead ask <id> \"<question>\" --to <person>@%s", name)
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
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
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
		return false, m.giveUp(w, reason, nil, false, 0, nil)
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
		return false, m.giveUp(w, reason, nil, true, 0, nil)
	})
}

// PeerRefusedRate gives the outbox message seq for name up because the peer
// takes no more mail this hour: its refusal over the link's limit, with
// what the link takes and when the hour ends. The writer is told both, by
// mail from the yard naming the hour's end, and the mail event keeps them
// (undelivered). A message of another kind is given up with the peer's
// reason in this yard's words, and the hour dropped: it is none of mail's.
func (s *Store) PeerRefusedRate(ctx context.Context, name string, seq uint64, limit int, until time.Time) error {
	return s.changePeerOut(name, seq, func(w *wtx, m *PeerOut) (bool, error) {
		if m.Failed != nil {
			return false, nil
		}
		m.Attempts++
		if m.Kind != PeerMail {
			return false, m.giveUp(w, "peer "+name+": refused by the peer: "+PeerRefusal(name, m.Kind, RefusedRate), nil, true, 0, nil)
		}
		return false, m.giveUp(w, "peer "+name+": refused by the peer: "+PeerRefusalRate(limit, until), nil, true, limit, &until)
	})
}

// PeerSendDisallowed drops the outbox message seq for name, whose kind the
// link no longer carries out: it was queued under a word the operator
// switched off since. It is given up, logged as peer_message_failed with
// why, and never tried again; nothing is re-queued. The allowance is read
// again here, in the write: allowed anew meanwhile, the message is left
// waiting, and false says so. One already given up is left as it is.
func (s *Store) PeerSendDisallowed(ctx context.Context, name string, seq uint64) (bool, error) {
	dropped := false
	err := s.changePeerOut(name, seq, func(w *wtx, m *PeerOut) (bool, error) {
		if m.Failed != nil {
			return false, nil
		}
		p, err := loadPeer(w.Tx, name)
		if err != nil {
			return false, err
		}
		if p.carries(m.Kind, false) {
			return false, nil
		}
		dropped = true
		return false, m.giveUp(w, notCarriedReason(name, m.Kind), nil, false, 0, nil)
	})
	return dropped, err
}

// notCarriedReason says why a message that waited is dropped at delivery:
// the link stopped carrying its kind out after it was queued. It names the
// word the operator switched, as PeerRefusal does, not the message's kind.
func notCarriedReason(peer, kind string) string {
	word := kind
	switch kind {
	case PeerBeadEvent, PeerBeadRead, PeerBeadNote:
		word = PeerThread
	case PeerBeadReturn:
		word = PeerBead
	}
	return fmt.Sprintf("the link with %s stopped carrying %s out while the message waited", peer, word)
}

// giveUp marks the message as given up, for reason: it stays in the outbox
// until it is pruned, peer_message_failed says why, mail's sender is told
// (undelivered), a bead ends its stage (sentGivenUp), an ask's asker or
// an answer's person is told (askGivenUp), and so is whoever asked to read
// a bead (readGivenUp) or wrote a note on one (noteGivenUp). refused says the
// peer refused it, where otherwise it was not reached. limit and until are
// the hour's, for mail refused over it: the event of the mail that was not
// delivered keeps them. A file stays without its bytes: nothing sends it
// again. The mail it was for goes on without it, so its event names that
// mail and the file's hash, and the mail's sender is told of the file.
func (m *PeerOut) giveUp(w *wtx, reason string, extra map[string]any, refused bool, limit int, until *time.Time) error {
	gone := now()
	m.Failed, m.RetryAt, m.Error = &gone, nil, reason
	data := map[string]any{"attempts": m.Attempts, "reason": reason}
	told, fileHash := m, ""
	if m.Kind == PeerFile {
		m.Body = withoutData(m.Body)
		if told, fileHash = w.carrier(m); told != nil {
			data["hash"], data["mail"] = fileHash, told.ID
		}
	}
	for k, v := range extra {
		data[k] = v
	}
	if err := w.event("", "peer_message_failed", "yard", peerEvent(m.Peer, m.PeerMessage, data)); err != nil {
		return err
	}
	if err := w.sentGivenUp(m, reason, refused); err != nil {
		return err
	}
	if err := w.askGivenUp(m, reason, refused); err != nil {
		return err
	}
	if err := w.readGivenUp(m, reason); err != nil {
		return err
	}
	if err := w.noteGivenUp(m, reason); err != nil {
		return err
	}
	return w.undelivered(told, fileHash, reason, limit, until)
}

// PeerHandOver answers a pull by the peer name, whose question was signed
// with key (loadPeerOf): what it says it has (up to
// after) is ended as sent, and the next of what waits for it is handed
// over. A peer this yard calls gets nothing this way, only the word that
// it is posted to: one message would otherwise cross twice.
//
// refused names, by sequence number, what the peer got and did not take,
// with why: such a message is given up, not sent, as one a post was
// answered 403 for is. The reason is the peer's word, and only one this
// yard knows is repeated (PeerRefusal). Mail refused over the hour carries
// its limit and when it ends, which the writer is told with it.
//
// What waits of a kind the link no longer sends is handed over to nobody:
// it is dropped, logged as peer_message_failed with why, and never tried
// again, as one the daemon would post is (PeerSendDisallowed).
//
// A peer asks again as soon as it is answered, so a question that ends
// nothing writes nothing: no write, no pass of the daemon for it.
//
// It is not on the API: only the receiver, which has checked the peer's
// signature on the question, may end a message as sent by pull.
func (s *Store) PeerHandOver(ctx context.Context, name, key string, after uint64, refused map[uint64]Refusal) (*PeerPage, error) {
	var page *PeerPage
	var taken, dropped []PeerOut
	hand := func(tx *bolt.Tx) error {
		page, taken, dropped = &PeerPage{Messages: []PeerMessage{}}, nil, nil // a batched write may run this twice
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
			case !p.carries(m.Kind, false):
				dropped = append(dropped, *m)
			case len(page.Messages) < pullBatch && (size == 0 || size+len(m.Body) <= pullBatchBytes):
				size += len(m.Body)
				page.Messages = append(page.Messages, m.PeerMessage)
			}
			return nil
		})
	}
	if err := s.read(hand); err != nil || len(taken) == 0 && len(dropped) == 0 {
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
			if no, is := refused[m.Seq]; is {
				reason, limit, until := "refused by the peer: "+PeerRefusal(m.Peer, m.Kind, no.Reason), 0, (*time.Time)(nil)
				if m.Kind == PeerMail && no.Reason == RefusedRate && no.Limit > 0 && no.Until != nil {
					reason, limit, until = "refused by the peer: "+PeerRefusalRate(no.Limit, *no.Until), no.Limit, no.Until
				}
				if err := m.giveUp(w, reason, map[string]any{"way": PeerPull}, true, limit, until); err != nil {
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
		for _, m := range dropped {
			if err := m.giveUp(w, notCarriedReason(name, m.Kind), map[string]any{"way": PeerPull}, false, 0, nil); err != nil {
				return err
			}
			if err := put(w.Tx, bPeerOut, join(name, seqKey(m.Seq)), m); err != nil {
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
// and mail is put in the inbox of the crew member or user it names, or
// refused (checkMail). A file is a blob of this yard by then, or refused
// (fl, which looked at the files before the write: filing). A bead
// becomes a bead of this yard, or is refused (checkBead); tk is what
// that needs of its intake's depot (taking) and own this yard's key,
// which a bead that has been here before names. The return of a bead ends
// the stay of this yard's bead at the peer (returnedBead), and a bead event
// is kept on the record of that stay (saidOfBead). A read of a bead that
// came from the peer is answered, and the answer to a read of this yard's
// replaces the one copy kept of it (readBead). A note for a bead that came
// from the peer is written on it, and sends it back to the first stage of
// its flow where people work that stage (noteBead). An ask becomes
// a foreign ask in the person's queue, and one that answers an ask of this
// yard a note of kind answer on its bead, or is refused (checkAsk). No
// other body is kept: what a peer sends is data.
//
// It changes p (the count of its mail and of its bead events, the build a
// bead names, when it was heard from), which the caller saves.
func (w *wtx) receivePeer(p *Peer, msgs []PeerMessage, way string, tk *taking, fl *filing, own string) ([]PeerTaken, error) {
	if fl.err != nil {
		return nil, fl.err
	}
	if len(msgs) > 0 {
		// Heard now, not to the minute (peerHeardEvery): p is saved with
		// its messages anyway, and what is kept of one carries its own
		// time, which a time heard that lags it would seem to contradict.
		at := now()
		p.HeardAt = &at
	}
	out := make([]PeerTaken, 0, len(msgs))
	actor := "peer:" + p.Name
	seen := w.Bucket(bPeerSeen)
	for _, m := range msgs {
		if err := m.check(); err != nil {
			// An ask over the message cap is refused as too large,
			// with its reason, not passed by as no message: it is
			// otherwise a message, with an id and a body that reads.
			if m.Kind != PeerAsk || len(m.Body) <= maxPeerBody(m.Kind) ||
				!peerMessageID.MatchString(m.ID) || !peerKind.MatchString(m.Kind) ||
				(m.Re != "" && !peerMessageID.MatchString(m.Re)) || !json.Valid(m.Body) {
				if way == PeerPush {
					return nil, fmt.Errorf("%w: %w", ErrNotMessage, err)
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
			var ask *peerAsk
			var no Refusal
			var err error
			switch m.Kind {
			case PeerMail:
				mail, no, err = w.checkMail(p, m, way)
				t.Refused, t.Limit, t.Until = no.Reason, no.Limit, no.Until
			case PeerFile:
				t.Refused, err = w.checkFile(p, m, way, fl)
			case PeerBead:
				bead, t.Refused, err = w.checkBead(p, m, way, tk, own)
			case PeerBeadReturn:
				t.Refused, err = w.returnedBead(p, m, way)
			case PeerBeadEvent:
				t.Refused, err = w.saidOfBead(p, m, way)
			case PeerBeadRead:
				t.Refused, err = w.readBead(p, m, way)
			case PeerBeadNote:
				t.Refused, err = w.noteBead(p, m, way, tk)
			case PeerAsk:
				ask, t.Refused, err = w.checkAsk(p, m, way, tk)
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
				if err := w.deliverMail(p, m, mail, fl); err != nil {
					return nil, err
				}
			}
			if bead != nil {
				if err := w.arriveBead(p, m, bead, way); err != nil {
					return nil, err
				}
			}
			if ask != nil {
				if err := w.deliverAsk(p, m, ask); err != nil {
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
	unlock := s.lockPeerFiles(msgs)
	defer unlock()
	var out []PeerTaken
	tk, fl, own := s.taking(ctx, name, msgs), s.filing(name, msgs), s.yardKey()
	err := s.write(func(w *wtx) error {
		p, err := loadPeerOf(w.Tx, name, key)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrPeerNotFound, err)
		}
		if out, err = w.receivePeer(p, msgs, PeerPush, tk, fl, own); err != nil {
			return err
		}
		return put(w.Tx, bPeers, name, p)
	})
	if err == nil {
		tk.keep(ctx, s)
	}
	return out, err
}

// lockPeerFiles keeps filing's blob writes in step with the quota in the
// transaction that takes the same batch. A batch without files needs no lock.
func (s *Store) lockPeerFiles(msgs []PeerMessage) func() {
	if !slices.ContainsFunc(msgs, func(m PeerMessage) bool { return m.Kind == PeerFile }) {
		return func() {}
	}
	s.peerFileMu.Lock()
	return s.peerFileMu.Unlock
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
		if err := w.distrusted(p); err != nil {
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
		if err := w.distrusted(p); err != nil {
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
// What was not taken is kept with why (Peer.Refused) for the next
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
	unlock := s.lockPeerFiles(page.Messages)
	defer unlock()
	tk, fl, own := s.taking(ctx, name, page.Messages), s.filing(name, page.Messages), s.yardKey()
	err = s.write(func(w *wtx) error {
		// Checked again in the write: the peer may have been removed, or
		// added anew with another key, between the two.
		p, err := check(w.Tx)
		if err != nil {
			return err
		}
		// Before its mail is taken: a name the answer takes off the roster
		// speaks as nobody in the mail the same answer hands over.
		if err := w.distrusted(p); err != nil {
			return err
		}
		if res.Taken, err = w.receivePeer(p, page.Messages, PeerPull, tk, fl, own); err != nil {
			return err
		}
		for _, seq := range b.Told {
			delete(p.Refused, seq)
		}
		for i, m := range page.Messages {
			p.Pulled = max(p.Pulled, m.Seq)
			if t := res.Taken[i]; t.Refused != "" && m.Seq > 0 {
				if p.Refused == nil {
					p.Refused = map[uint64]Refusal{}
				}
				p.Refused[m.Seq] = Refusal{Reason: t.Refused, Limit: t.Limit, Until: t.Until}
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

package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A pairing links two yards from a short code one operator reads to the
// other (docs/peers.md, pairing_messages.go). The yard that shows the code is
// the host: its side is here, in the process that owns the store, because
// that is where the other yard's calls arrive.
//
// An open pairing is held in memory and nowhere else. Nothing of it is in
// the store file until both operators have confirmed, and the code never
// is: a server that stops takes the pairing with it, and the operator opens
// another.

// The states of a Pairing.
const (
	PairOpened = "open"   // the code is shown; the other yard has not called
	PairCalled = "called" // it has, and the two operators compare the check
	PairDone   = "done"   // both confirmed: the peer is saved
	PairEnded  = "ended"  // nothing was saved; Reason says why
)

// PairOffer is what the host's operator opens a pairing with: what this
// yard says of itself (its invite, with the URL it is reached at), and As,
// the name to save the other yard under when not the one it gives.
type PairOffer struct {
	Name string `json:"name"`
	Key  string `json:"key"`
	URL  string `json:"url"`
	As   string `json:"as,omitempty"`
}

// Pairing is the host's pairing as its operator sees it. It never holds the
// code: PairOpen alone returns that, once.
type Pairing struct {
	ID      string    `json:"id"`
	State   string    `json:"state"`
	Expires time.Time `json:"expires"`
	// Tries counts the wrong codes.
	Tries int `json:"wrong_codes,omitempty"`
	// Once the other yard has called: the name it is saved under, what it
	// calls itself, its key, and the check to compare with its operator's.
	Peer  string `json:"peer,omitempty"`
	Calls string `json:"calls,omitempty"`
	Key   string `json:"key,omitempty"`
	Check string `json:"check,omitempty"`
	// Confirmed is this yard's operator's word on the check, PeerConfirmed
	// the other's.
	Confirmed     bool `json:"confirmed,omitempty"`
	PeerConfirmed bool `json:"peer_confirmed,omitempty"`
	// Reason is why it ended without a link (one of pairing's reasons), and
	// Detail what there is to say beyond it.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// PairingCode is a pairing just opened, with the code to read to the other
// yard's operator.
type PairingCode struct {
	Pairing
	Code string `json:"code"`
}

// hostPairing is the one pairing a yard has open, or had last.
type hostPairing struct {
	Pairing
	offer PairOffer
	actor string
	// code is the code as both sides compute from it, until a yard that
	// knows it has revealed itself; it is forgotten then, and when the
	// pairing ends.
	code string
	// commit, key and nonce are of the last first message taken: its
	// commitment, the key stretched from the code for it, and the nonce
	// this yard answered with.
	commit string
	key    []byte
	nonce  string
	// session names the pairing to the other yard once it has called.
	session string
}

// pairings holds the yard's pairing. Its lock is taken before any of the
// store's, never after.
type pairings struct {
	mu  sync.Mutex
	cur *hostPairing
}

// forget drops everything of the code.
func (p *hostPairing) forget() { p.code, p.key, p.commit, p.nonce = "", nil, "", "" }

// event is what every event of a pairing says. Never the code.
func (p *hostPairing) event(extra map[string]any) map[string]any {
	data := map[string]any{"pairing": p.ID, "role": "host"}
	if p.Peer != "" {
		data["peer"], data["calls"], data["key"] = p.Peer, p.Calls, p.Key
	}
	if p.Tries > 0 {
		data["wrong_codes"] = p.Tries
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

// endPairing ends the pairing without a link, for reason, and logs it.
func (s *Store) endPairing(ctx context.Context, p *hostPairing, reason, detail, actor string) {
	p.State, p.Reason, p.Detail = PairEnded, reason, detail
	p.forget()
	// The pairing has ended whether or not the event is written.
	_ = s.LogEvent(ctx, "", "peer_pair_refused", actor, p.event(map[string]any{"reason": reason}))
}

// pairing is the yard's pairing as it is now, with s.pair.mu held: one
// whose time ran out is ended here, by whoever looks first.
func (s *Store) pairing(ctx context.Context) *hostPairing {
	p := s.pair.cur
	if p != nil && (p.State == PairOpened || p.State == PairCalled) && !now().Before(p.Expires) {
		s.endPairing(ctx, p, TimedOut, "", "yard")
	}
	return p
}

// cleanURL holds p's URL to where a yard can be called and keeps it in the
// one form (CleanURL); none is a yard that is never called. It is
// the one check of a peer's URL: what an add refuses, a later change of the
// URL refuses in the same words (SetPeerURL).
func (p *Peer) cleanURL() error {
	if p.URL == "" {
		return nil
	}
	u, err := CleanURL(p.URL)
	if err != nil {
		return fmt.Errorf("peer %s: %w", p.Name, err)
	}
	p.URL = u
	return nil
}

// peerFree refuses a peer that cannot be added: a name that is none, a key
// that is none, and a name or a key that is a peer already. A key replaced
// under a name would hand the link to someone else, so that takes removing
// the peer first.
func peerFree(tx *bolt.Tx, p *Peer) error {
	if err := checkName("peer", p.Name); err != nil {
		return err
	}
	if _, err := ParsePublic(p.Key); err != nil {
		return fmt.Errorf("peer %s: %w", p.Name, err)
	}
	if err := p.cleanURL(); err != nil {
		return err
	}
	if tx.Bucket(bPeers).Get([]byte(p.Name)) != nil {
		return fmt.Errorf("peer %s is added already; to replace it, remove it first (its waiting messages go with it)", p.Name)
	}
	var same string
	err := each(tx, bPeers, "", func(_ []byte, o *Peer) error {
		if o.Key == p.Key {
			same = o.Name
		}
		return nil
	})
	if err != nil {
		return err
	}
	if same != "" {
		return fmt.Errorf("peer %s: that key is peer %s's already", p.Name, same)
	}
	return nil
}

// CheckPeer says whether p could be added as a peer, in the words AddPeer
// refuses it with: a pairing asks before its operator is asked anything.
func (s *Store) CheckPeer(ctx context.Context, p Peer) error {
	return s.read(func(tx *bolt.Tx) error { return peerFree(tx, &p) })
}

// pairs is what every step of a pairing its operator takes is, to
// operatorOnly: a pairing ends in a link, which is theirs to make.
const pairs = "pair this yard with another"

// PairPeer saves the yard a pairing ended with as a peer: AddPeer, and the
// link carries ping both ways and nothing else, which is what the ping that
// proves a pairing needs. It logs what the two commands would
// (peer_added, peer_allowed) and peer_paired. This is the calling side's;
// the host's is saved by the pairing itself.
func (s *Store) PairPeer(ctx context.Context, p Peer, actor string) (*Peer, error) {
	if err := s.mayRead(ctx, actor, ActPair, Object{Kind: KindPeer}); err != nil {
		return nil, err
	}
	return s.pairPeer(p, actor, map[string]any{"role": "joiner"})
}

func (s *Store) pairPeer(p Peer, actor string, data map[string]any) (*Peer, error) {
	p = Peer{Name: p.Name, Key: p.Key, URL: p.URL, Calls: p.Calls, Send: []string{PeerPing}, Receive: []string{PeerPing}, AddedAt: now()}
	err := s.write(func(w *wtx) error {
		if err := peerFree(w.Tx, &p); err != nil {
			return err
		}
		p.Since = w.Bucket(bEvents).Sequence()
		if err := put(w.Tx, bPeers, p.Name, p); err != nil {
			return err
		}
		added := map[string]any{"peer": p.Name, "key": p.Key}
		paired := map[string]any{"peer": p.Name, "key": p.Key, "calls": p.Calls}
		if p.URL != "" {
			added["url"], paired["url"] = p.URL, p.URL
		}
		for k, v := range data {
			paired[k] = v
		}
		if err := w.event("", "peer_added", actor, added); err != nil {
			return err
		}
		if err := w.event("", "peer_allowed", actor, map[string]any{"peer": p.Name, "send": p.Send, "receive": p.Receive}); err != nil {
			return err
		}
		return w.event("", "peer_paired", actor, paired)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// pairRefusals are the reasons the calling side of a pairing may log.
var pairRefusals = []string{WrongCode, Declined, PeerDeclined, TimedOut,
	Closed, AlreadyPeer, BadName, Own}

// PairRefused logs that a pairing this yard called another for ended
// without a link (peer_pair_refused): reason is one of pairing's, and peer
// what the other yard calls itself, when it got that far. The host's side
// logs its own.
func (s *Store) PairRefused(ctx context.Context, reason, peer, actor string) error {
	if !slices.Contains(pairRefusals, reason) {
		return fmt.Errorf("%q is no reason a pairing ends for", reason)
	}
	data := map[string]any{"role": "joiner", "reason": reason}
	if peer != "" {
		if !ValidName(peer) {
			return fmt.Errorf("%q is no yard's name", peer)
		}
		data["calls"] = peer
	}
	return s.LogEvent(ctx, "", "peer_pair_refused", actor, data)
}

// PairOpen opens a pairing and returns its code, good for TTL and
// for one pairing. A yard has one open at a time.
func (s *Store) PairOpen(ctx context.Context, o PairOffer, actor string) (*PairingCode, error) {
	if err := s.mayRead(ctx, actor, ActPair, Object{Kind: KindPeer}); err != nil {
		return nil, err
	}
	if err := (Party{Nonce: NewNonce(), Name: o.Name, Key: o.Key, URL: o.URL}).Valid(); err != nil {
		return nil, err
	} else if o.URL == "" {
		return nil, errors.New("pairing: the yard that shows the code is the one that is called, and has given no URL")
	}
	if o.As != "" {
		// Said now, not when the other yard has called.
		if err := checkName("peer", o.As); err != nil {
			return nil, err
		}
		if _, err := s.GetPeer(ctx, o.As); err == nil {
			return nil, fmt.Errorf("peer %s is added already; to replace it, remove it first (its waiting messages go with it)", o.As)
		}
	}
	s.pair.mu.Lock()
	defer s.pair.mu.Unlock()
	if p := s.pairing(ctx); p != nil && (p.State == PairOpened || p.State == PairCalled) {
		return nil, fmt.Errorf("a pairing is open already, until %s: a yard has one at a time (yardr peer pair --end ends it)", p.Expires.Local().Format("15:04:05"))
	}
	code := NewCode()
	clean, err := CleanCode(code)
	if err != nil {
		return nil, err
	}
	p := &hostPairing{Pairing: Pairing{ID: newPeerMessageID(), State: PairOpened, Expires: now().Add(TTL)},
		offer: o, actor: actor, code: clean}
	if err := s.LogEvent(ctx, "", "peer_pair_opened", actor, p.event(map[string]any{"expires": p.Expires, "url": o.URL})); err != nil {
		return nil, err
	}
	s.pair.cur = p
	return &PairingCode{Pairing: p.Pairing, Code: code}, nil
}

// pairingOf is the pairing id names ("" for whichever there is), with
// s.pair.mu held.
func (s *Store) pairingOf(ctx context.Context, id string) (*hostPairing, error) {
	p := s.pairing(ctx)
	if p == nil || (id != "" && p.ID != id) {
		return nil, fmt.Errorf("that pairing: %w (a pairing lives in the server's memory, and ends with it)", ErrNotFound)
	}
	return p, nil
}

// GetPairing is the host's pairing as it is now: id names it, "" is
// whichever the yard has.
func (s *Store) GetPairing(ctx context.Context, id string) (*Pairing, error) {
	s.pair.mu.Lock()
	defer s.pair.mu.Unlock()
	p, err := s.pairingOf(ctx, id)
	if err != nil {
		return nil, err
	}
	out := p.Pairing
	return &out, nil
}

// PairAnswer is the host's operator's word on the check: yes confirms it,
// and the peer is saved once the other yard's operator has confirmed too;
// no ends the pairing with nothing saved on either side.
func (s *Store) PairAnswer(ctx context.Context, id string, yes bool, actor string) (*Pairing, error) {
	if err := s.mayRead(ctx, actor, ActPair, Object{Kind: KindPeer}); err != nil {
		return nil, err
	}
	s.pair.mu.Lock()
	defer s.pair.mu.Unlock()
	p, err := s.pairingOf(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case p.State == PairOpened:
		return nil, errors.New("the other yard has not called yet: there is no check to answer")
	case p.State == PairCalled && !yes:
		s.endPairing(ctx, p, Declined, "", actor)
	case p.State == PairCalled:
		p.Confirmed, p.actor = true, actor
		if p.PeerConfirmed {
			s.finishPairing(ctx, p)
		}
	}
	out := p.Pairing
	return &out, nil
}

// PairName changes the name the yard that called is saved under, while its
// operator's word on the check is still owed: a form asks for the name once
// it knows what the other yard calls itself. A name that cannot be a peer's
// here is refused, and the pairing stays as it was.
func (s *Store) PairName(ctx context.Context, id, name, actor string) (*Pairing, error) {
	if err := s.mayRead(ctx, actor, ActPair, Object{Kind: KindPeer}); err != nil {
		return nil, err
	}
	if err := checkName("peer", name); err != nil {
		return nil, err
	}
	s.pair.mu.Lock()
	defer s.pair.mu.Unlock()
	p, err := s.pairingOf(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.State != PairCalled || p.Confirmed {
		return nil, errors.New("the other yard's name is given once it has called and before the check is confirmed")
	}
	if err := s.CheckPeer(ctx, Peer{Name: name, Key: p.Key}); err != nil {
		return nil, err
	}
	p.Peer = name
	out := p.Pairing
	return &out, nil
}

// PairClose ends the host's pairing, if it is still open: nothing is
// saved, and the code is good for nothing.
func (s *Store) PairClose(ctx context.Context, id, actor string) error {
	if err := s.mayRead(ctx, actor, ActPair, Object{Kind: KindPeer}); err != nil {
		return err
	}
	s.pair.mu.Lock()
	defer s.pair.mu.Unlock()
	p, err := s.pairingOf(ctx, id)
	if err != nil {
		return err
	}
	if p.State == PairOpened || p.State == PairCalled {
		s.endPairing(ctx, p, Closed, "", actor)
	}
	return nil
}

// finishPairing saves the other yard as a peer, both operators having
// confirmed. A name or key taken since the call ends the pairing instead.
func (s *Store) finishPairing(ctx context.Context, p *hostPairing) {
	peer := Peer{Name: p.Peer, Key: p.Key, Calls: p.Calls}
	if _, err := s.pairPeer(peer, p.actor, map[string]any{"role": "host", "pairing": p.ID}); err != nil {
		s.endPairing(ctx, p, AlreadyPeer, err.Error(), p.actor)
		return
	}
	p.State = PairDone
}

// refusedCall is the answer to a call the pairing cannot take as it is.
func refusedCall(p *hostPairing) error {
	switch {
	case p == nil:
		return &Refused{Reason: NoPairing}
	case p.State == PairCalled || p.State == PairDone:
		return &Refused{Reason: Used}
	case p.State == PairEnded && p.Reason == TimedOut:
		return &Refused{Reason: TimedOut}
	case p.State == PairEnded:
		return &Refused{Reason: NoPairing}
	}
	return nil
}

// wrongCode counts a message made without the code, and ends the pairing
// at MaxTries of them.
func (s *Store) wrongCode(ctx context.Context, p *hostPairing) error {
	p.Tries++
	if p.Tries >= MaxTries {
		s.endPairing(ctx, p, WrongCode, "", "yard")
	}
	return &Refused{Reason: WrongCode}
}

// PairHello takes the first message of a yard that calls with a code, and
// answers with this yard's side. One that does not know the code is
// refused and counted; a later one that does takes an earlier one's place,
// until one has revealed itself.
//
// This and the three below are not on the API: they are the other yard's
// calls, which only the receiver, in the process that owns the store, takes.
func (s *Store) PairHello(ctx context.Context, h Hello) (*PairingOffer, error) {
	s.pair.mu.Lock()
	defer s.pair.mu.Unlock()
	p := s.pairing(ctx)
	if err := refusedCall(p); err != nil {
		return nil, err
	}
	if err := h.Valid(); err != nil {
		return nil, err
	}
	// The stretch is the cost of a guess, and this yard pays it too, once a
	// call: with the lock held, so the tries are counted one by one.
	key := PairingKey(p.code, h.Commit)
	if knows, _ := h.Check(key); !knows {
		return nil, s.wrongCode(ctx, p)
	}
	p.commit, p.key, p.nonce = h.Commit, key, NewNonce()
	offer := NewPairingOffer(key, h.Commit, s.hostParty(p))
	return &offer, nil
}

func (s *Store) hostParty(p *hostPairing) Party {
	return Party{Nonce: p.nonce, Name: p.offer.Name, Key: p.offer.Key, URL: p.offer.URL}
}

// PairReveal takes the second message: who the calling yard is. The code
// is used with it, and forgotten. It returns the session the confirmations
// name the pairing by, or refuses a yard that cannot be added here, in
// which case the pairing has ended and its Detail says why in AddPeer's
// words.
func (s *Store) PairReveal(ctx context.Context, r Reveal) (session string, err error) {
	s.pair.mu.Lock()
	defer s.pair.mu.Unlock()
	p := s.pairing(ctx)
	if err := refusedCall(p); err != nil {
		return "", err
	}
	if p.commit == "" || r.Commit != p.commit {
		return "", &Refused{Reason: Superseded}
	}
	t, knows, err := r.Check(p.key, s.hostParty(p))
	if !knows && err == nil {
		return "", s.wrongCode(ctx, p)
	}
	if err != nil {
		if knows {
			// It knows the code and reveals what it did not commit to.
			_ = s.wrongCode(ctx, p)
		}
		return "", err
	}
	p.forget()
	p.Calls, p.Key, p.Check, p.session = t.Joiner.Name, t.Joiner.Key, t.Check(), t.Session()
	p.Peer = p.offer.As
	if p.Peer == "" {
		p.Peer = p.Calls
	}
	refuse := func(reason, detail string) (string, error) {
		s.endPairing(ctx, p, reason, detail, "yard")
		return "", &Refused{Reason: reason}
	}
	if p.Key == p.offer.Key {
		return refuse(Own, "the yard that called has this yard's own key")
	}
	if p.offer.As == "" && checkName("peer", p.Peer) != nil {
		return refuse(BadName, fmt.Sprintf("the other yard calls itself %q, which is no name for a peer here: open the pairing again with --name", p.Calls))
	}
	if err := s.CheckPeer(ctx, Peer{Name: p.Peer, Key: p.Key}); err != nil {
		return refuse(AlreadyPeer, err.Error())
	}
	p.State, p.Expires = PairCalled, now().Add(ConfirmTTL)
	return p.session, nil
}

// PairCaller is the public key of the yard that called in the pairing
// session names: what its confirmations must be signed with.
func (s *Store) PairCaller(ctx context.Context, session string) (key string, err error) {
	s.pair.mu.Lock()
	defer s.pair.mu.Unlock()
	p := s.pairing(ctx)
	if p == nil || p.session == "" || p.session != session {
		return "", &Refused{Reason: NoPairing}
	}
	return p.Key, nil
}

// PairConfirmed takes the calling yard's word on the check, which the
// receiver has found signed with its key, and says how the pairing stands.
// Its yes, once this yard's operator has said yes too, saves it as a peer;
// its no ends the pairing. It asks again, with the same word, until the
// answer is no longer Waiting.
func (s *Store) PairConfirmed(ctx context.Context, session string, yes bool) (*PairingOutcome, error) {
	s.pair.mu.Lock()
	defer s.pair.mu.Unlock()
	p := s.pairing(ctx)
	if p == nil || p.session == "" || p.session != session {
		return nil, &Refused{Reason: NoPairing}
	}
	if p.State == PairCalled {
		switch {
		case !yes:
			s.endPairing(ctx, p, PeerDeclined, "", "yard")
		case !p.PeerConfirmed:
			p.PeerConfirmed = true
			if p.Confirmed {
				s.finishPairing(ctx, p)
			}
		}
	}
	switch p.State {
	case PairDone:
		return &PairingOutcome{State: Confirmed}, nil
	case PairEnded:
		return &PairingOutcome{State: Ended, Reason: p.Reason}, nil
	}
	return &PairingOutcome{State: Waiting}, nil
}

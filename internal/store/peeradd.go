package store

import (
	"context"
	"errors"
)

// errOwnPeerInvite is the refusal shared by peer add and its run-ask card.
var errOwnPeerInvite = errors.New("that invite is this yard's own: give it to the other yard's operator, and add theirs here")

// AddPeer records another yard as a peer. The link carries nothing until
// AllowPeer says what. A name or a key that is a peer already is refused
// (peerFree). Who a yard is linked with, and what it takes from them, is
// the operator's to say (operatorOnly), here and in every change of a link
// that widens it: not for a crew member's session to grant. A run ask may
// propose it, for the operator to approve (runask.go).
func (s *Store) AddPeer(ctx context.Context, p Peer, actor string) (*Peer, error) {
	var out *Peer
	err := s.write(func(w *wtx) error {
		var err error
		out, err = w.addPeer(ctx, p, actor, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// addPeer is AddPeer inside the transaction, so that an approved run ask
// links the peer in the write that records its approval: by is then the
// ask it was approved on, which the event carries.
func (w *wtx) addPeer(ctx context.Context, p Peer, actor string, by *approvedOn) (*Peer, error) {
	// A new link carries nothing, whatever the caller holds.
	p = Peer{Name: p.Name, Key: p.Key, URL: p.URL, Calls: p.Calls, Send: []string{}, Receive: []string{}, AddedAt: now()}
	if err := w.checkPeerInvite(p.Key); err != nil {
		return nil, err
	}
	// Askable: the card names the peer, the yard and its key, and where
	// this yard calls it, and an approval that reaches the yard through
	// that peer's own reach is refused (approveAsk), so a peer cannot
	// add its own link.
	if err := w.may(ctx, actor, ActAddPeer, Object{Kind: KindPeer, Name: p.Name, Ask: Askable}); err != nil {
		return nil, err
	}
	if err := peerFree(w.Tx, &p); err != nil {
		return nil, err
	}
	p.Since = w.Bucket(bEvents).Sequence()
	if err := put(w.Tx, bPeers, p.Name, p); err != nil {
		return nil, err
	}
	data := map[string]any{"peer": p.Name, "key": p.Key}
	if p.URL != "" {
		data["url"] = p.URL
	}
	if by != nil {
		data["approved"] = by
	}
	if err := w.event("", "peer_added", actor, data); err != nil {
		return nil, err
	}
	return &p, nil
}

// checkPeerInvite refuses a link back to this yard, when the process that
// owns the store said where its own key is (SetYard).
func (w *wtx) checkPeerInvite(key string) error {
	if w.store != nil && key != "" && w.store.yardKey() == key {
		return errOwnPeerInvite
	}
	return nil
}

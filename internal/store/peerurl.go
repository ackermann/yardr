package store

import (
	"context"
	"fmt"
)

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
// Where a peer is called is the operator's to say (operatorOnly), since the
// address is where the yard's calls go: not for a crew member's session to
// grant. A run ask may propose it, for the operator to approve (runask.go).
func (s *Store) SetPeerURL(ctx context.Context, name, url, actor string) (*Peer, error) {
	var p *Peer
	err := s.write(func(w *wtx) (err error) {
		p, err = w.setPeerURL(ctx, name, url, actor, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// setPeerURL is SetPeerURL inside the transaction, so that an approved run
// ask moves the peer in the write that records its approval: by is then the
// ask it was approved on, which the event carries.
func (w *wtx) setPeerURL(ctx context.Context, name, url, actor string, by *approvedOn) (*Peer, error) {
	// Askable: the card shows the address now and the one proposed, each
	// whole, and an approval that reaches the yard through that peer's own
	// reach is refused (approveAsk), so a peer cannot say where its own
	// link is called.
	if err := w.may(ctx, actor, ActChangePeerAddress, Object{Kind: KindPeer, Name: name, Ask: Askable}); err != nil {
		return nil, err
	}
	p, err := loadPeer(w.Tx, name)
	if err != nil {
		return nil, err
	}
	was := p.URL
	p.URL = url
	if err := p.cleanURL(); err != nil {
		return nil, err
	}
	switch {
	case p.URL != was:
	case was == "":
		return nil, fmt.Errorf("peer %s has no URL already: this yard never calls it", name)
	default:
		return nil, fmt.Errorf("peer %s is called at %s already", name, was)
	}
	if err := put(w.Tx, bPeers, name, p); err != nil {
		return nil, err
	}
	data := map[string]any{"peer": name}
	if p.URL != "" {
		data["url"] = p.URL
	}
	if was != "" {
		data["was"] = was
	}
	if by != nil {
		data["approved"] = by
	}
	if err := w.event("", "peer_url_set", actor, data); err != nil {
		return nil, err
	}
	if err := outboxOf(w.Tx, p); err != nil {
		return nil, err
	}
	return p, nil
}

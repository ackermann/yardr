package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"
)

// A link can say who a sender is in a source that hears by identity when
// the sender is a crew member of the peer's yard, not a user of this one.
// The mayor of a linked yard comments in Linear under an id of its own, and
// mapping that id to one of this yard's users would make the other yard's
// word a user's here. The operator maps it to <crew>@<peer> instead
// (Peer.Identities, yardr peer identity). A delivery from the id is kept
// with that speaker (hooks.Receiver.speaker) and reaches crew as mail from
// the same address does, from another yard: information, not instructions
// (OutsideMail). The mapping names no user and lends the sender no user's
// standing. No name of a user has an "@" (SpeakerIsForeign).

// PeerIdentity says that the sender with the id ID in Source, a source
// that hears by identity, is the crew member Crew of the peer's yard.
type PeerIdentity struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	Crew   string `json:"crew"`
}

// IsIn reports whether id is the sender the identity maps in source. An id
// matches exactly, as a user's does (Identity.is); an identity has no key.
func (i PeerIdentity) IsIn(source, id string) bool {
	return id != "" && i.Source == source && i.ID == id
}

// Speaker is how a delivery from the identity names who spoke, on the link
// called peer: the address mail from the crew member has.
func (i PeerIdentity) Speaker(peer string) string { return i.Crew + "@" + peer }

// comparePeerIdentity orders a link's identities by source, then crew.
func comparePeerIdentity(a, b PeerIdentity) int {
	return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Crew, b.Crew), cmp.Compare(a.ID, b.ID))
}

// SetPeerIdentity says that the sender id.ID in id.Source is the crew member
// id.Crew of the peer name's yard, in place of the id that crew member had
// in that source. It refuses a source that hears nobody by identity, where
// no delivery would be heard as anyone, and an id that names a speaker
// already, a user of this yard or a crew member of a linked one. One id
// names one speaker, so hearing never has to choose between two. It logs
// peer_identity_set. Mapping an id clears it from the source's ignore list
// in the same write, as mapping a user does (setUserIdentity): a mapped
// sender is never also ignored, and the operator has answered for this one
// anew.
//
// Only the operator says who a sender is (operatorOnly, Alone): the mapping
// is the consent to hear the sender, and it decides how their words are
// read. Taking one back is the operator's too.
func (s *Store) SetPeerIdentity(ctx context.Context, name string, id PeerIdentity, actor string) (*Peer, error) {
	switch {
	case id.Source == "":
		return nil, fmt.Errorf("identity: source is required")
	case id.ID == "":
		return nil, fmt.Errorf("identity: id is required")
	case !peerMailName.MatchString(id.Crew):
		return nil, fmt.Errorf("crew member %q of a peer: use letters, digits, '.', '_', '-' or '/' (starting with a letter or digit), as the name it has in its own yard", id.Crew)
	}
	var p *Peer
	err := s.write(func(w *wtx) (err error) {
		// Alone: it decides whose word a sender's is, which no card may.
		if err := w.operatorOnly(ctx, actor, "say who a peer's crew member is in a source", Alone); err != nil {
			return err
		}
		if p, err = loadPeer(w.Tx, name); err != nil {
			return err
		}
		// A builtin source has no declaration, and none hears by identity.
		switch _, d, err := sourceIn(w.Tx, id.Source); {
		case errors.Is(err, ErrNotFound):
			return fmt.Errorf("peer %s: %s is no source this yard declares, and only a declared source hears by identity (yardr source add)", name, id.Source)
		case err != nil:
			return err
		case !d.HearsByIdentity():
			return fmt.Errorf("peer %s: %s hears no sender by identity (its declaration has no hear key with by: identity), so a delivery from it is not heard as %s@%s", name, id.Source, id.Crew, name)
		}
		holder, err := speakerHolding(w.Tx, id.Source, id.ID, name, id.Crew)
		if err != nil {
			return err
		} else if holder != "" {
			return fmt.Errorf("peer %s: %s in %s names %s already: an id in a source names one speaker of the yard", name, id.ID, id.Source, holder)
		}
		data := map[string]any{"peer": name, "crew": id.Crew, "source": id.Source, "id": id.ID}
		at := slices.IndexFunc(p.Identities, func(have PeerIdentity) bool { return have.Source == id.Source && have.Crew == id.Crew })
		if at < 0 {
			p.Identities = append(p.Identities, id)
		} else {
			if was := p.Identities[at].ID; was != id.ID {
				data["was"] = was
			}
			p.Identities[at] = id
		}
		slices.SortFunc(p.Identities, comparePeerIdentity)
		if err := put(w.Tx, bPeers, name, p); err != nil {
			return err
		}
		if err := w.event("", "peer_identity_set", actor, data); err != nil {
			return err
		}
		if _, err := w.clearIgnored(id.Source, id.ID, actor, nil); err != nil {
			return err
		}
		return outboxOf(w.Tx, p)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// RemovePeerIdentity takes back who the crew member of the peer name's yard
// is in source. A delivery from the id is then heard as any unmapped
// sender's is, as the hook's unknown word says. It logs
// peer_identity_removed, and is the operator's alone, as setting one is.
func (s *Store) RemovePeerIdentity(ctx context.Context, name, crew, source, actor string) (*Peer, error) {
	var p *Peer
	err := s.write(func(w *wtx) (err error) {
		if err := w.operatorOnly(ctx, actor, "take back who a peer's crew member is in a source", Alone); err != nil {
			return err
		}
		if p, err = loadPeer(w.Tx, name); err != nil {
			return err
		}
		at := slices.IndexFunc(p.Identities, func(have PeerIdentity) bool { return have.Source == source && have.Crew == crew })
		if at < 0 {
			return fmt.Errorf("peer %s has no %s identity for %s: %w", name, source, crew, ErrNotFound)
		}
		gone := p.Identities[at]
		if p.Identities = slices.Delete(p.Identities, at, at+1); len(p.Identities) == 0 {
			p.Identities = nil
		}
		if err := put(w.Tx, bPeers, name, p); err != nil {
			return err
		}
		if err := w.event("", "peer_identity_removed", actor, map[string]any{"peer": name, "crew": crew, "source": source, "id": gone.ID}); err != nil {
			return err
		}
		return outboxOf(w.Tx, p)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// PeerIdentities are the identities of every link, by the link's name; a
// link with none is left out. The receiver reads them beside the users', to
// name who spoke in a delivery heard by identity. That happens on every such
// delivery, so it is not Peers, which counts each link's outbox and reads
// the role files of its roster. It is not on the API: the receiver, in the
// process that owns the store, asks.
func (s *Store) PeerIdentities(ctx context.Context) (map[string][]PeerIdentity, error) {
	out := map[string][]PeerIdentity{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bPeers, "", func(_ []byte, p *Peer) error {
			if len(p.Identities) > 0 {
				out[p.Name] = p.Identities
			}
			return nil
		})
	})
	return out, err
}

// speakerHolding is the speaker, besides the crew member of the peer that is
// being given it, whom a delivery from id in source would be heard as: a
// user whose identity matches it, else a crew member of a linked yard mapped
// to it. "" for nobody. It reads what the receiver reads (User.IsIn,
// PeerIdentity.IsIn), so a clash here is an id hearing could not choose a
// speaker for.
func speakerHolding(tx *bolt.Tx, source, id, peer, crew string) (string, error) {
	var holder string
	err := each(tx, bUsers, "", func(_ []byte, u *User) error {
		if holder == "" && u.IsIn(source, id) {
			holder = u.Name
		}
		return nil
	})
	if err != nil || holder != "" {
		return holder, err
	}
	err = each(tx, bPeers, "", func(_ []byte, p *Peer) error {
		for _, i := range p.Identities {
			if holder == "" && i.IsIn(source, id) && (p.Name != peer || i.Crew != crew) {
				holder = i.Speaker(p.Name)
			}
		}
		return nil
	})
	return holder, err
}

// peerHolding is the crew member of a linked yard, as <crew>@<peer>, whom a
// delivery from the sender the user identity id names in source would also
// be heard as: the reverse of speakerHolding, for a user's new identity. ""
// for nobody.
func peerHolding(tx *bolt.Tx, source string, id Identity) (string, error) {
	var holder string
	err := each(tx, bPeers, "", func(_ []byte, p *Peer) error {
		for _, i := range p.Identities {
			if holder == "" && i.Source == source && id.is(i.ID) {
				holder = i.Speaker(p.Name)
			}
		}
		return nil
	})
	return holder, err
}

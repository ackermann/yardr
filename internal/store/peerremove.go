package store

import (
	"context"
	"fmt"
	"strings"
)

// DeletePeer removes the link: nothing more is taken from the yard or sent
// to it. What waited for it is dropped, and peer_removed says how many. A
// bead of this yard that was there ends its stage as undelivered: no
// return can come for it. The questions it asked are dropped too, and each
// person they were for gets one mail listing theirs as text. Unlinking is
// the operator's to say (operatorOnly), as linking is: not for a crew
// member's session to grant. A run ask may propose it, for the operator
// to approve (runask.go).
func (s *Store) DeletePeer(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		return w.deletePeer(ctx, name, actor, nil)
	})
}

// deletePeer is DeletePeer inside the transaction, so that an approved run
// ask removes the peer in the write that records its approval: by is then
// the ask it was approved on, which the event carries.
func (w *wtx) deletePeer(ctx context.Context, name, actor string, by *approvedOn) error {
	// Askable: the card names the link and what goes with it, and an
	// approval that reaches the yard through that peer's own reach is
	// refused (approveAsk), so a peer cannot remove its own link.
	if err := w.may(ctx, actor, ActRemovePeer, Object{Kind: KindPeer, Name: name, Ask: Askable}); err != nil {
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
	if by != nil {
		data["approved"] = by
	}
	return w.event("", "peer_removed", actor, data)
}

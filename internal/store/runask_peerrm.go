// The peer rm row of the run-ask list (runList): removing a link with
// its intakes, its identities and what is queued for it. It has what a
// row has: how its words are read into one stored form, the command's
// own check as the asker, the card the yard writes from its records, why
// an approval of an older card is refused, and the store function that
// makes the change. It is about one peer's link, so an approval that
// came through that peer's own reach is refused (runCommand.link): a
// peer does not remove its own link on its own word.
package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// parsePeerRm reads the words of peer rm <name>.
func parsePeerRm(rest []string) ([]string, error) {
	const use = "yardr peer rm <name>"
	if len(rest) != 1 {
		return nil, fmt.Errorf("--run takes %s: one peer, not %d words", use, len(rest))
	}
	return []string{rest[0]}, nil
}

// checkPeerRm is removing a peer's own check as the asker.
func checkPeerRm(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.may(ctx, actor, ActRemovePeer, Object{Kind: KindPeer, Name: argv[2], Ask: Askable})
}

// peerRmCard is the card of peer rm <name>: the link goes with its
// intakes, its identities and what is queued for it, each named with its
// count, as deletePeer drops them. The crew that may mail its crew,
// the beads it sent that stay, the beads of this yard that end as
// undelivered and its questions that are dropped stand beside them, and
// adding it again starts a new link from a fresh invite. The approval is
// held to the link's key whole and to its address, intakes and
// identities (Seen).
func (w *wtx) peerRmCard(argv []string, asking bool) (AskRun, error) {
	name := argv[2]
	p, err := loadPeer(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	queued := len(keys(w.Tx, bPeerOut, join(name, "")))
	sentences := []string{fmt.Sprintf("removes the link with %s (%s): its %s, its %s and %s queued for it go.",
		name, peerAs(p), countNoun(len(p.Intakes), "intake"), countNoun(len(p.Identities), "identity"), countNoun(queued, "message"))}
	var may []string
	if err := each(w.Tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if slices.Contains(r.MayMail(), name) {
			may = append(may, r.Name)
		}
		return nil
	}); err != nil {
		return AskRun{}, err
	}
	if len(may) > 0 {
		sentences = append(sentences, fmt.Sprintf("%s may mail its crew now, and can no longer.", strings.Join(may, ", ")))
	}
	if recs, err := peerBeadsOf(w.Tx, p.Key); err != nil {
		return AskRun{}, err
	} else if len(recs) == 1 {
		sentences = append(sentences, "1 bead it sent stays, and says its link is removed: nothing is returned for it.")
	} else if len(recs) > 1 {
		sentences = append(sentences, fmt.Sprintf("%d beads it sent stay, and say their link is removed: nothing is returned for them.", len(recs)))
	}
	if err := awayAt(w.Tx, p); err != nil {
		return AskRun{}, err
	}
	if p.Away == 1 {
		sentences = append(sentences, "1 bead of this yard is there, and ends its stage as undelivered.")
	} else if p.Away > 1 {
		sentences = append(sentences, fmt.Sprintf("%d beads of this yard are there, and end their stage as undelivered.", p.Away))
	}
	nAsks := 0
	if err := each(w.Tx, bPeerAsks, "", func(_ []byte, rec *foreignAsk) error {
		if rec.Key == p.Key {
			nAsks++
		}
		return nil
	}); err != nil {
		return AskRun{}, err
	}
	if nAsks == 1 {
		sentences = append(sentences, "1 question it asked is dropped, and the person it was for gets one mail listing it.")
	} else if nAsks > 1 {
		sentences = append(sentences, fmt.Sprintf("%d questions it asked are dropped, and each person they were for gets one mail listing theirs.", nAsks))
	}
	sentences = append(sentences, "Adding it again starts a new link, from a fresh invite the other yard makes: with no intakes, no identities and nothing queued.")
	return AskRun{
		Argv:   argv,
		Does:   sentences,
		Back:   runCommandLine([]string{"peer", "add", name, "<invite>"}),
		BackBy: "only the operator",
		Seen: map[string]string{
			"peer " + name:                 p.Key,
			"peer " + name + " address":    seen(p.URL),
			"peer " + name + " intakes":    seen(p.Intakes),
			"peer " + name + " identities": seen(p.Identities),
		},
	}, nil
}

// peerRmStale says why the ask seq for peer rm <name> is approved no
// more: the link is another one under the same name, its address,
// intakes or identities moved since, or what goes with it is not what
// the card says.
func (w *wtx) peerRmStale(seq uint64, was, now AskRun) error {
	name := was.Argv[2]
	if err := w.peerKeyStale(seq, name, was, now); err != nil {
		return err
	}
	if key := "peer " + name + " address"; was.Seen[key] != now.Seen[key] {
		e := lastEvent(w.Tx, "peer_url_set", naming("peer", name))
		return fmt.Errorf("ask %d was written when the link with %s was called at another address: it was set since%s. %s", seq, name, since(e), nothingRan)
	}
	if key := "peer " + name + " intakes"; was.Seen[key] != now.Seen[key] {
		e := lastEvent(w.Tx, "peer_intake", naming("peer", name))
		return fmt.Errorf("ask %d was written when the link with %s held other intakes: they were changed since%s. %s", seq, name, since(e), nothingRan)
	}
	if key := "peer " + name + " identities"; was.Seen[key] != now.Seen[key] {
		match := func(e Event) bool { return e.Data["peer"] == name }
		set, removed := lastEvent(w.Tx, "peer_identity_set", match), lastEvent(w.Tx, "peer_identity_removed", match)
		switch {
		case set != nil && (removed == nil || set.Seq > removed.Seq):
			return fmt.Errorf("ask %d was written when the link with %s held other identities: one was set since%s. %s", seq, name, since(set), nothingRan)
		case removed != nil:
			return fmt.Errorf("ask %d was written when the link with %s held other identities: one was removed since%s. %s", seq, name, since(removed), nothingRan)
		}
		return fmt.Errorf("ask %d was written when the link with %s held other identities. %s", seq, name, nothingRan)
	}
	return staleCard(seq, was, now)
}

// runPeerRm removes the peer as actor through deletePeer, and answers
// what the command prints at a terminal.
func runPeerRm(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	if err := w.deletePeer(ctx, argv[2], actor, by); err != nil {
		return "", err
	}
	return argv[2] + ": removed", nil
}

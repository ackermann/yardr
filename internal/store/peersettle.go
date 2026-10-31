package store

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// How a copied ask ended in the yard it was asked in, as a settle says it.
const (
	SettledAnswered = "answered"
	SettledDeclined = "declined"
	// SettledTakenBack is the asker's doing, not the person's: a build from
	// before it refuses the word as no ask, and the question stays open
	// there until a late answer is refused.
	SettledTakenBack = "taken_back"
	// SettledAside is no end: the ask was put aside there, until ReturnAt,
	// and still takes an answer afterwards. A build from before it refuses
	// the word as no ask, and the question stays open there.
	SettledAside = "aside"
)

// settleCopies tells the yards an ask for a user of this yard was copied
// to that it ended here, in the write that records the answer ans on the
// bead row: one message of kind ask for each, whose Re is the copy's
// message, saying settled (peerAnswer). The yard that takes it marks its
// foreign ask with that end, so a question answered in one yard does not
// stay open in the others, where a later pick would replace the answer.
//
// by is the link the answer came over and name who gave it there, nil and
// "" for an answer given in this yard: that name knows, and is told
// nothing. A put-aside is told as SettledAside with the ask's return here,
// and keeps the copies active: the ask is not ended, and the pick that
// ends it settles them then. A newer pick on an ask that has one is not
// sent as another settle; the ended copy records stay for late-answer checks.
//
// An ask that is taken back (TakeBackAsk) is told the same way, as
// SettledTakenBack, and to the yard of an ask addressed to <person>@<peer>
// as well: that yard is otherwise told of no end, and learns of a pick or
// a decline given here by hand when its own late answer is refused.
//
// Only a name the link still gives reach for the user is told, over a link
// that carries ask out: the grant is what sends anything of the user's
// there. A settle waits on nothing, so one not sent, or given up
// (askGivenUp), tells nobody.
func (w *wtx) settleCopies(row *beadRow, ans Answer, by *Peer, name string) error {
	key := join(row.UUID, seqKey(ans.Ask))
	var copies []askCopy
	if ok, err := get(w.Tx, bAskCopies, key, &copies); err != nil || !ok {
		return err
	}
	settled := SettledAnswered
	var returnAt *time.Time
	switch {
	case ans.TakeBack:
		settled = SettledTakenBack
	case ans.Decline:
		settled = SettledDeclined
	case ans.Later:
		settled = SettledAside
		at, err := laterReturn(w.Tx, row, ans.Ask)
		if err != nil {
			return err
		}
		returnAt = &at
	}
	body, err := json.Marshal(peerAnswer{V: PeerAskVersion, Bead: row.ID, Seq: ans.Ask, Settled: settled, ReturnAt: returnAt})
	if err != nil {
		return err
	}
	for i := range copies {
		c := copies[i]
		if c.Ended {
			continue
		}
		if settled != SettledAside {
			copies[i].Ended = true
		}
		if by != nil && c.Key == by.Key && c.Name == name {
			continue
		}
		p, err := peerByKey(w.Tx, c.Key)
		if err != nil {
			return err
		}
		switch {
		case p == nil || !p.carries(PeerAsk, false):
			continue
		case c.As == "" && !ans.TakeBack:
			// Addressed there: only its asker taking it back is told.
			continue
		case c.As != "" && p.reaches(c.Name) != c.As:
			continue
		}
		m := PeerMessage{ID: newPeerMessageID(), Kind: PeerAsk, Re: c.Message, Body: body}
		if err := m.check(); err != nil {
			return err
		}
		if _, err := w.queuePeer(p, m, "yard"); err != nil {
			return err
		}
	}
	if settled != SettledAside {
		return put(w.Tx, bAskCopies, key, copies)
	}
	return nil
}

// laterReturn is when the put-aside ask seq on the bead row comes back:
// 24 hours after the Later answer that stands for it, as ResolveAsks
// reads it. It is read from the stored notes, never from the clock, so
// every yard is told the same time the ask carries here. The Later was
// recorded in this write, so it is there.
func laterReturn(tx *bolt.Tx, row *beadRow, seq uint64) (time.Time, error) {
	notes, err := beadNotes(tx, row.UUID)
	if err != nil {
		return time.Time{}, err
	}
	var latestSeq uint64
	var at time.Time
	for _, n := range notes {
		if n.Kind != NoteAnswer {
			continue
		}
		a, err := ParseAnswer(n)
		if err != nil || a.Ask != seq || !a.Later {
			continue
		}
		// Ask state follows note order, even if the machine's clock moved
		// backwards between two put-aside answers.
		if n.Seq > latestSeq {
			latestSeq = n.Seq
			at = n.At
		}
	}
	if at.IsZero() {
		return time.Time{}, fmt.Errorf("ask %d on bead %s has no put-aside answer", seq, row.ID)
	}
	return at.Add(LaterFor), nil
}

// takeSettle takes p's word that the ask it copied to this yard with the
// message m.Re ended there, or says why not: the foreign ask is marked with
// that end as one whose late answer was refused is (answerGivenUp), so it
// moves to the person's decided with the words that say where it ended and
// takes no further answer. What the person did with it here stays under
// it. Nobody is mailed: nothing of theirs failed.
//
// An aside is that yard's word that it was put aside there instead: the
// foreign ask is marked with its return, so it stands in the person's
// decided as put aside there until then, and still takes a pick, words or
// a decline here, which land home as they do. One without its return is
// refused as no ask.
//
// A settle for no foreign ask of p's, or for one that names another bead
// or seq than the message it answers carried, is dropped, taken and not
// refused: the yard that sent it waits on nothing, and is told nothing of
// what this yard holds. So is one for an ask that is marked already. Only
// a word that is neither end nor aside is refused, as no ask.
func (w *wtx) takeSettle(p *Peer, m PeerMessage, a peerAnswer) (string, error) {
	outcome := ""
	switch a.Settled {
	case SettledAnswered:
		outcome = RefusedAskAnswered
	case SettledDeclined:
		outcome = RefusedAskDeclined
	case SettledTakenBack:
		outcome = RefusedAskTakenBack
	case SettledAside:
		if a.ReturnAt == nil || a.ReturnAt.IsZero() {
			return RefusedNotAsk, nil
		}
	default:
		return RefusedNotAsk, nil
	}
	key := join(p.Key, m.Re)
	var rec foreignAsk
	if ok, err := get(w.Tx, bPeerAsks, key, &rec); err != nil || !ok || rec.Bead != a.Bead || rec.Seq != a.Seq || rec.Outcome != "" {
		return "", err
	}
	at := now()
	if a.Settled == SettledAside {
		rec.AsideAt, rec.AsideUntil = &at, a.ReturnAt
	} else {
		rec.Outcome, rec.OutcomeAt = outcome, &at
		rec.AsideAt, rec.AsideUntil = nil, nil
	}
	if err := put(w.Tx, bPeerAsks, key, rec); err != nil {
		return "", err
	}
	// Put aside there: what is still queued for the ask goes, and its
	// return is news too, told when it comes back.
	if a.Settled == SettledAside {
		if err := dropFootAsk(w.Tx, rec.To, "", 0, key); err != nil {
			return "", err
		}
		if err := w.queueFootRemoteAside(rec.To, key, *a.ReturnAt); err != nil {
			return "", err
		}
	}
	// The queue changed: a client reads UserAsks again on it.
	return "", w.event(rec.Bead+"@"+p.Name, "noted", "yard", map[string]any{"ask": rec.Seq})
}

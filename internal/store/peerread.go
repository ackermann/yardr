package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// While a bead of this yard is at another, someone here may read what was
// written on that yard's bead (docs/peers.md, "Reading the other yard's
// bead"). This file is both sides of that: the question the asking yard
// queues and the one copy it keeps of the answer (PeerRead, peerRead), and
// what the working yard answers with (readBead, answerRead).
//
// PeerBeadRead is the kind of both messages, which PeerThread allows. The
// question names the PeerBead message of the stay in its Re and says
// nothing else; the answer names the question in its Re and holds what a
// return would carry at that moment. A link carries either only when it
// sends and receives PeerThread: the question goes one way and its answer
// the other (Peer.carries).
const PeerBeadRead = "bead-read"

const (
	// RefusedNoSuchBead is the refusal of a read of a bead that is not an
	// open bead of the peer that asks: the one reason whether the bead
	// exists, came from another peer, or has ended, so that the answer says
	// nothing of what this yard holds.
	RefusedNoSuchBead = "no_such_bead"
	// RefusedNotRead is the refusal of a read that is none: a body that
	// does not read, no message named in its Re, or an answer whose handle
	// or stage is not the plain characters one is held to.
	RefusedNotRead = "not_a_read"
)

// bPeerReads holds, by the bead's UUID, what this yard keeps of reading a
// bead that is at another yard: the questions that wait for an answer, and
// the last answer. It is beside the record of the stay (bPeerSent) and not
// in it, so that the peer's words are in one place that goes when the stay
// ends (dropPeerRead), and never on the bead.
var bPeerReads = []byte("peer_reads")

func init() { allBuckets = append(allBuckets, bPeerReads) }

// PeerReadMessage is the body of a message of kind PeerBeadRead. The
// question is V alone. The answer has Bead, the working yard's handle for
// its bead, the Stage it is in there, whether it is Held, and Notes, what
// was written there, oldest first, with NotesLeftOut counting the older
// ones that did not fit: what a return holds, without an outcome.
type PeerReadMessage struct {
	V            int        `json:"v"`
	Bead         string     `json:"bead,omitempty"`
	Stage        string     `json:"stage,omitempty"`
	Held         *bool      `json:"held,omitempty"`
	Notes        []PeerNote `json:"notes,omitempty"`
	NotesLeftOut int        `json:"notes_left_out,omitempty"`
}

// peerAsked is a question about a bead that waits for its answer: the
// message, who asked, and when.
type peerAsked struct {
	ID string    `json:"id"`
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// peerRead is what this yard keeps of reading the bead Bead while it is at
// the peer whose key is Key, in the stay its message Message began: Asked
// are the questions that wait for an answer, oldest first, and the rest is
// the last answer, which each answer replaces: the question it answered
// (Re), the peer's handle, stage and hold, the notes, and when it came.
type peerRead struct {
	Bead    string      `json:"bead"`
	Key     string      `json:"key"`
	Message string      `json:"message"`
	Asked   []peerAsked `json:"asked,omitempty"`

	Re           string     `json:"re,omitempty"`
	Theirs       string     `json:"theirs,omitempty"`
	Stage        string     `json:"stage,omitempty"`
	Held         bool       `json:"held,omitempty"`
	Notes        []PeerNote `json:"notes,omitempty"`
	NotesLeftOut int        `json:"notes_left_out,omitempty"`
	At           *time.Time `json:"at,omitempty"`
}

// PeerReading is what yardr thread show prints of a bead that is at
// another yard: this yard's bead and its title, the peer as it is called
// today and when it was last heard from, the question that waits for its
// answer (ID, "" for none), and the last answer this yard has kept, nil
// when the peer has answered none in this stay.
type PeerReading struct {
	Bead    string          `json:"bead"`
	Title   string          `json:"title"`
	Peer    string          `json:"peer"`
	HeardAt *time.Time      `json:"heard_at,omitempty"`
	ID      string          `json:"id,omitempty"`
	Answer  *PeerReadAnswer `json:"answer,omitempty"`
}

// PeerReadAnswer is a peer's answer to a read, as this yard keeps it: the
// question it answered (Re), the peer's handle for its bead, the stage it
// is in there and whether it is held, the notes written there, each by the
// name the peer gave, and when the answer came. All of it is the peer's
// word, which nothing here checks, but for a note that someone of this
// yard wrote there (PeerNote.Here), whose name and text are what this yard
// sent.
type PeerReadAnswer struct {
	Re           string     `json:"re"`
	Bead         string     `json:"bead"`
	Stage        string     `json:"stage"`
	Held         bool       `json:"held"`
	Notes        []PeerNote `json:"notes"`
	NotesLeftOut int        `json:"notes_left_out,omitempty"`
	At           time.Time  `json:"at"`
}

func peerReadOf(tx *bolt.Tx, uuid string) (*peerRead, error) {
	rec := new(peerRead)
	if ok, err := get(tx, bPeerReads, uuid, rec); err != nil || !ok {
		return nil, err
	}
	return rec, nil
}

// dropPeerRead forgets what this yard kept of reading the bead uuid at
// another yard, the questions and the copy of the last answer: the stay
// has ended, and what the peer wrote is on the bead as notes when its
// return brought it.
func dropPeerRead(tx *bolt.Tx, uuid string) error {
	return tx.Bucket(bPeerReads).Delete([]byte(uuid))
}

// awayStay is the open stay of the bead row at another yard, with that
// yard, or why it has none: reading, and a note on the other yard's bead
// (PeerNote), are of a bead that is away.
func awayStay(tx *bolt.Tx, row *beadRow) (*peerSent, *Peer, error) {
	sent, err := peerSentOf(tx, row.UUID)
	if err != nil {
		return nil, nil, err
	}
	if sent == nil {
		return nil, nil, fmt.Errorf("bead %s is not at another yard and never was: there is no bead of another yard to read or to answer on", row.ID)
	}
	was := fmt.Sprintf("bead %s is not at another yard any more: its stay at %s", row.ID, or(peerNamed(tx, sent.Key, sent.Was), "a yard no longer linked"))
	if sent.EndedAt != nil && sent.Outcome != OutcomeTakenBack {
		return nil, nil, fmt.Errorf("%s ended %s, as %s. What was written there came with the return and is among the bead's notes (yardr bead show %s)",
			was, stamp(*sent.EndedAt), sent.Outcome, row.ID)
	}
	if left, err := leftStay(tx, sent); err != nil {
		return nil, nil, err
	} else if left || sent.EndedAt != nil {
		return nil, nil, fmt.Errorf("%s was over when the bead left the stage %s it was sent from", was, sent.Stage)
	}
	p, err := peerByKey(tx, sent.Key)
	if err != nil {
		return nil, nil, err
	}
	if p == nil || sent.Was != "" {
		return nil, nil, fmt.Errorf("%s has no link left to reach it over", was)
	}
	return sent, p, nil
}

// mayReadPeer says why actor may not read a bead that is at peer: reading
// needs what writing to a peer needs (MailPeer). A crew member needs the
// peer in its MailPeers setting, a user of the yard needs nothing, and
// anyone else must be the operator: a bead's agent reads nothing of
// another yard beyond what its own bead holds.
func mayReadPeer(ctx context.Context, tx *bolt.Tx, actor, peer string) error {
	return mayAtPeer(ctx, tx, actor, peer, "read a bead that is at")
}

// mayAtPeer is the rule of mayReadPeer for what: reading a bead that is at
// peer, or writing on that yard's bead for it (PeerNote).
func mayAtPeer(ctx context.Context, tx *bolt.Tx, actor, peer, what string) error {
	var r CrewMember
	if ok, err := get(tx, bCrew, actor, &r); err != nil {
		return err
	} else if ok && !slices.Contains(r.MayMail(), peer) {
		return fmt.Errorf("crew member %s may not %s %s: its definition does not allow that yard. The operator allows it with yardr crew set %s %s=%s",
			actor, what, peer, actor, MailPeers, strings.Join(append(r.MayMail(), peer), ","))
	} else if !ok && tx.Bucket(bUsers).Get([]byte(actor)) == nil {
		if _, this, err := notOperator(ctx, tx, actor); err != nil {
			return err
		} else if this != "" {
			return fmt.Errorf("only a crew member allowed %s (yardr crew set <crew member> %s=%s) or a user of the yard (yardr user list) may %s another yard; this is %s",
				peer, MailPeers, peer, what, this)
		}
	}
	return nil
}

// reading is what is shown of the bead row at p, from what this yard keeps
// (rec, nil for nothing): the answer only when it is of the stay sent.
func reading(row *beadRow, p *Peer, sent *peerSent, rec *peerRead) *PeerReading {
	out := &PeerReading{Bead: row.ID, Title: row.Title, Peer: p.Name, HeardAt: p.HeardAt}
	if rec == nil || rec.Message != sent.Message || rec.At == nil {
		return out
	}
	notes := rec.Notes
	if notes == nil {
		notes = []PeerNote{}
	}
	out.Answer = &PeerReadAnswer{Re: rec.Re, Bead: rec.Theirs, Stage: rec.Stage, Held: rec.Held, Notes: notes, NotesLeftOut: rec.NotesLeftOut, At: *rec.At}
	return out
}

// PeerRead asks the yard the bead is at what was written on its bead: a
// message of kind PeerBeadRead in the outbox for that peer, which the
// daemon delivers or the peer asks for. The answer comes as a message that
// names this one (peer_message_received with its id as re) and replaces
// the copy this yard keeps (PeerReadKept). It returns the question's id
// with what is kept now.
//
// It is refused, with nothing queued, for a bead that is not away, for an
// actor who may not (mayReadPeer), and over a link that does not carry
// PeerThread both ways. While a question about the bead still waits to be
// delivered, no second one is queued behind it: its id is returned.
func (s *Store) PeerRead(ctx context.Context, bead, actor string) (*PeerReading, error) {
	body, err := json.Marshal(PeerReadMessage{V: PeerBeadVersion})
	if err != nil {
		return nil, err
	}
	var out *PeerReading
	err = s.write(func(w *wtx) error {
		row, err := w.loadBead(bead)
		if err != nil {
			return err
		}
		sent, p, err := awayStay(w.Tx, row)
		if err != nil {
			return err
		}
		if err := mayReadPeer(ctx, w.Tx, actor, p.Name); err != nil {
			return err
		}
		if !p.carries(PeerBeadRead, false) {
			return fmt.Errorf("peer %s: the link does not carry %s both ways, and a read is a question out and its answer in: the operator allows it with yardr peer allow %s --send %s --receive %s",
				p.Name, PeerThread, p.Name, PeerThread, PeerThread)
		}
		rec, err := peerReadOf(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		if rec == nil || rec.Message != sent.Message || rec.Key != p.Key {
			// What was kept is of an earlier stay.
			rec = &peerRead{Bead: row.UUID, Key: p.Key, Message: sent.Message}
		}
		out = reading(row, p, sent, rec)
		for _, q := range rec.Asked {
			if queued(w.Tx, p.Name, q.ID) {
				out.ID = q.ID
				return nil
			}
		}
		m := PeerMessage{ID: newPeerMessageID(), Kind: PeerBeadRead, Re: sent.Message, Body: body}
		if err := m.check(); err != nil {
			return err
		}
		if _, err := w.queuePeer(p, m, actor); err != nil {
			return err
		}
		rec.Asked = append(rec.Asked, peerAsked{ID: m.ID, By: actor, At: now()})
		out.ID = m.ID
		return put(w.Tx, bPeerReads, row.UUID, rec)
	})
	return out, err
}

// PeerReadKept is what this yard has kept of reading the bead at the yard
// it is at: the last answer of this stay, for the same actors PeerRead
// asks for. It asks the peer nothing.
func (s *Store) PeerReadKept(ctx context.Context, bead, actor string) (*PeerReading, error) {
	var out *PeerReading
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, bead)
		if err != nil {
			return err
		}
		sent, p, err := awayStay(tx, row)
		if err != nil {
			return err
		}
		if err := mayReadPeer(ctx, tx, actor, p.Name); err != nil {
			return err
		}
		rec, err := peerReadOf(tx, row.UUID)
		if err != nil {
			return err
		}
		out = reading(row, p, sent, rec)
		return nil
	})
	return out, err
}

// notesSince are the notes of the bead uuid written after the note after,
// oldest first, as they cross a link: for a bead that came from a peer,
// with after the last note that came with it, what was written here.
func notesSince(tx *bolt.Tx, uuid string, after uint64) ([]PeerNote, error) {
	var notes []PeerNote
	err := each(tx, bNotes, join(uuid, ""), func(_ []byte, n *Note) error {
		if n.Seq > after {
			notes = append(notes, PeerNote{Author: n.Author, At: n.At, Text: n.Text})
		}
		return nil
	})
	return notes, err
}

// readBead takes the message m of kind PeerBeadRead that p sent: a
// question about a bead of this yard that came from p, which is answered
// (answerRead), or the answer to a question this yard asked, which
// replaces the copy kept for the bead (answeredRead). It returns why one
// that is neither was not taken.
//
// Every question counts toward the peer's hour beside its mail and asks
// (Peer.MailsAnHour), the refused too, and so does an answer nobody asked
// for; of those over the limit only the first of the hour is logged. An
// answer to a question of this yard is not counted: this yard asked for
// it. It changes p (the count), which the caller saves.
func (w *wtx) readBead(p *Peer, m PeerMessage, way string) (string, error) {
	actor := "peer:" + p.Name
	extra := map[string]any{"direction": "in", "way": way}
	refuse := func(reason string) (string, error) {
		extra["reason"] = reason
		return reason, w.event("", "peer_message_refused", actor, peerEvent(p.Name, m, extra))
	}
	var r PeerReadMessage
	parse := json.Unmarshal(m.Body, &r)
	var rec *peerRead
	if parse == nil && r.Bead != "" && m.Re != "" {
		// An answer: to which question of this yard, if any.
		err := each(w.Tx, bPeerReads, "", func(_ []byte, kept *peerRead) error {
			if kept.Key == p.Key && slices.ContainsFunc(kept.Asked, func(q peerAsked) bool { return q.ID == m.Re }) {
				rec = kept
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	if rec == nil {
		p.hour(now())
		if p.Mails++; p.Mails > p.MailsAnHour() {
			if p.MailLimited {
				return RefusedRate, nil
			}
			p.MailLimited = true
			extra["limit"], extra["until"] = p.MailsAnHour(), p.MailSince.Add(PeerMailWindow)
			return refuse(RefusedRate)
		}
	}
	switch {
	case parse != nil || m.Re == "":
		return refuse(RefusedNotRead)
	case r.V != PeerBeadVersion:
		extra["v"], extra["known"] = r.V, PeerBeadVersion
		return refuse(RefusedVersion)
	case rec != nil:
		return w.answeredRead(p, m, r, rec, way, refuse)
	case r.Bead != "":
		// Nobody here asked: it is dropped, and said once in the log.
		return "", w.event("", "peer_bead_read", actor, map[string]any{"peer": p.Name, "id": m.ID, "re": m.Re, "way": way,
			"stale": true, "reason": "it answers no question this yard asked of " + p.Name})
	}
	found, err := w.answerRead(p, m, way)
	if err != nil || found {
		return "", err
	}
	return refuse(RefusedNoSuchBead)
}

// beadFor is the bead of this yard that came from p in the message re, with
// its record: one that came over the link as it is today, open and not
// returned. Both are nil for any other, whether the bead exists, came from
// another peer, or has ended: a read of it and a note for it have the one
// refusal (RefusedNoSuchBead).
func beadFor(tx *bolt.Tx, p *Peer, re string) (*peerBead, *beadRow, error) {
	recs, err := peerBeadsOf(tx, p.Key)
	if err != nil {
		return nil, nil, err
	}
	i := slices.IndexFunc(recs, func(rec peerBead) bool {
		return rec.Message == re && rec.Direction == "in" && rec.ReturnedAt == nil
	})
	if i < 0 {
		return nil, nil, nil
	}
	row := new(beadRow)
	if ok, err := get(tx, bBeads, recs[i].Bead, row); err != nil || !ok || row.Status != "open" {
		return nil, nil, err
	}
	return &recs[i], row, nil
}

// answerRead answers the question m that p asked about the bead its Re
// names: a bead that came from p over the link as it is today, open and
// not returned. The answer is what the return would carry at this moment,
// without an outcome: the notes written here, the newest kept within the
// limits a return has, and the stage and hold. It is queued for p in this
// write and peer_bead_read is logged on the bead, with how many notes went
// and none of their text. It reports whether there is such a bead: for any
// other the caller has one refusal.
func (w *wtx) answerRead(p *Peer, m PeerMessage, way string) (bool, error) {
	rec, row, err := beadFor(w.Tx, p, m.Re)
	if err != nil || rec == nil {
		return false, err
	}
	notes, err := notesSince(w.Tx, row.UUID, rec.Notes)
	if err != nil {
		return false, err
	}
	held := row.Hold
	ans := PeerReadMessage{V: PeerBeadVersion, Bead: row.ID, Stage: row.Stage, Held: &held}
	body, left := fitNotes(notes, func(kept []PeerNote, left int) any {
		ans.Notes, ans.NotesLeftOut = kept, left
		return ans
	})
	answer := PeerMessage{ID: newPeerMessageID(), Kind: PeerBeadRead, Re: m.ID, Body: body}
	if err := answer.check(); err != nil {
		return false, err
	}
	if _, err := w.queuePeer(p, answer, "yard"); err != nil {
		return false, err
	}
	data := map[string]any{"peer": p.Name, "id": m.ID, "re": m.Re, "answer": answer.ID, "to": rec.Theirs, "notes": len(notes) - left, "way": way}
	if left > 0 {
		data["notes_left_out"] = left
	}
	return true, w.event(row.UUID, "peer_bead_read", "peer:"+p.Name, data)
}

// answeredRead takes the answer r that p sent to a question of this yard
// about the bead of rec: it replaces the one copy kept for the bead, and
// peer_bead_read is logged on the bead, with how many notes came and none
// of their text. Nothing else changes: an answer writes no note on the
// bead, moves nothing and reaches no inbox. One that comes when the stay
// is over is dropped with what was kept, and logged with stale.
//
// What the peer says is held to what a return is: a handle and a stage of
// plain characters, and notes cut and counted as a return's are.
func (w *wtx) answeredRead(p *Peer, m PeerMessage, r PeerReadMessage, rec *peerRead, way string, refuse func(string) (string, error)) (string, error) {
	if !validID.MatchString(r.Bead) || isUUID(r.Bead) || !peerStage.MatchString(r.Stage) || r.Held == nil {
		return refuse(RefusedNotRead)
	}
	actor := "peer:" + p.Name
	data := map[string]any{"peer": p.Name, "id": m.ID, "re": m.Re, "from": r.Bead, "stage": r.Stage, "held": *r.Held, "way": way}
	sent, err := peerSentOf(w.Tx, rec.Bead)
	if err != nil {
		return "", err
	}
	over := sent == nil || sent.Message != rec.Message || sent.Key != p.Key || sent.Was != "" || sent.EndedAt != nil
	if !over {
		if over, err = leftStay(w.Tx, sent); err != nil {
			return "", err
		}
	}
	if over {
		if err := dropPeerRead(w.Tx, rec.Bead); err != nil {
			return "", err
		}
		data["stale"], data["reason"] = true, "the bead's stay at "+p.Name+" is over"
		return "", w.event(rec.Bead, "peer_bead_read", actor, data)
	}
	notes := slices.DeleteFunc(slices.Clone(r.Notes), func(n PeerNote) bool { return strings.TrimSpace(n.Text) == "" })
	at := now()
	for i, n := range notes {
		// As the notes that come with a return are written (peerNotes): one
		// that someone here wrote there is theirs, a name that is none is
		// someone, and a time that has not been is now.
		by, here := sent.wroteHere(n)
		if notes[i].Here = here; here {
			notes[i].Author = by
		} else if !peerMailName.MatchString(n.Author) {
			notes[i].Author = "someone"
		}
		if notes[i].At = n.At.UTC(); n.At.IsZero() || n.At.After(at) {
			notes[i].At = at
		}
	}
	slices.SortStableFunc(notes, func(a, b PeerNote) int { return a.At.Compare(b.At) })
	kept, left := keepNotes(notes)
	rec.Asked = slices.DeleteFunc(rec.Asked, func(q peerAsked) bool { return q.ID == m.Re })
	rec.Re, rec.Theirs, rec.Stage, rec.Held, rec.At = m.Re, r.Bead, r.Stage, *r.Held, &at
	rec.Notes, rec.NotesLeftOut = kept, left+max(0, r.NotesLeftOut)
	if err := put(w.Tx, bPeerReads, rec.Bead, rec); err != nil {
		return "", err
	}
	data["notes"] = len(kept)
	if rec.NotesLeftOut > 0 {
		data["notes_left_out"] = rec.NotesLeftOut
	}
	return "", w.event(rec.Bead, "peer_bead_read", actor, data)
}

// readGivenUp tells whoever asked the question m, which was given up, that
// no answer will come, as mail from the yard: a crew member, or a user.
// The question waits for nothing more. An answer of this yard's that was
// given up is the asking yard's to miss, and tells nobody here.
func (w *wtx) readGivenUp(m *PeerOut, reason string) error {
	if m.Kind != PeerBeadRead {
		return nil
	}
	rec, err := peerReadOf(w.Tx, sentBead(m.Re))
	if err != nil || rec == nil {
		return err
	}
	i := slices.IndexFunc(rec.Asked, func(q peerAsked) bool { return q.ID == m.ID })
	if i < 0 {
		return nil
	}
	asked := rec.Asked[i]
	rec.Asked = slices.Delete(rec.Asked, i, i+1)
	if err := put(w.Tx, bPeerReads, rec.Bead, rec); err != nil {
		return err
	}
	var row beadRow
	if ok, err := get(w.Tx, bBeads, rec.Bead, &row); err != nil || !ok {
		return err
	}
	reason = strings.TrimPrefix(reason, "peer "+m.Peer+": ")
	data := map[string]any{"to": asked.By, "undelivered": m.ID,
		"text": fmt.Sprintf("your read of %s at %s of %s was not answered: %s", row.ID, m.Peer, asked.At.Local().Format("01-02 15:04"), reason)}
	switch {
	case w.Bucket(bCrew).Get([]byte(asked.By)) != nil:
	case w.Bucket(bUsers).Get([]byte(asked.By)) != nil:
		data["to_user"] = true
	default:
		return nil
	}
	return w.event("", "mail", "yard", data)
}

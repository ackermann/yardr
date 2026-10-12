package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// While a bead of one yard is worked by another, the working yard tells the
// asking yard how its bead stands (docs/peers.md, "While a bead is at the
// other yard"). This file is both sides of that: the message the working
// yard queues with each of five events of its bead (tellPeer), and what the
// asking yard keeps of one it takes (saidOfBead).
//
// PeerThread is the word a link is allowed by, each direction on its own. It
// is the kind of no message: it covers PeerBeadEvent, which goes the way the
// word is allowed (Peer.carries), from the working yard that sends thread
// to the asking yard that receives it.
const (
	PeerThread    = "thread"
	PeerBeadEvent = "bead-event"
)

// PeerEventLimit is how many bead events a yard takes from one peer within
// PeerMailWindow, counted apart from its mail and asks. What comes after is
// refused (RefusedRate) until the hour has passed, and the peer gives each
// up: nothing waits on an event.
const PeerEventLimit = 240

// RefusedNotEvent is the refusal of a bead event that is none: a body that
// does not read, a word that is no event, a stage or a handle that is not
// the plain characters one is held to, or no bead message named in its re.
const RefusedNotEvent = "not_an_event"

// The events of a bead that came from a peer which that peer is told, as
// the message names them. Each is said with the event of this yard that it
// comes from (peerEventOf, and moveBead for PeerEventAdvanced).
const (
	PeerEventArrived  = "arrived"
	PeerEventAdvanced = "advanced"
	PeerEventHeld     = "held"
	PeerEventUnheld   = "unheld"
	PeerEventGaveUp   = "gave_up"
)

// PeerEvents are the words a bead event may say: one that says another is
// refused (RefusedNotEvent).
var PeerEvents = []string{PeerEventArrived, PeerEventAdvanced, PeerEventHeld, PeerEventUnheld, PeerEventGaveUp}

// peerEventOf is the word a peer is told for an event of this yard on a
// bead that came from it. An advanced event is not among them: whether the
// stage it entered ends the bead is the flow's to say, which moveBead has
// read and the event has not, and a bead that ended is told by its return.
var peerEventOf = map[string]string{
	"peer_bead_arrived": PeerEventArrived,
	"held":              PeerEventHeld,
	"unheld":            PeerEventUnheld,
	"gave_up":           PeerEventGaveUp,
}

// PeerEventMessage is the body of a message of kind PeerBeadEvent, which
// names the PeerBead message of the stay it is about in the message's Re.
// Bead is the working yard's handle for its bead, Event one of PeerEvents,
// Stage the stage the bead is in there and Held whether it is held, at At.
// From is the stage it left, of an advanced one. It carries no text and no
// name of anyone in the working yard.
type PeerEventMessage struct {
	V     int       `json:"v"`
	Bead  string    `json:"bead"`
	Event string    `json:"event"`
	Stage string    `json:"stage"`
	From  string    `json:"from,omitempty"`
	Held  bool      `json:"held"`
	At    time.Time `json:"at"`
}

// tellPeer queues a bead event for the yard the bead uuid came from, in the
// write that logged the event of this yard it is about: how the bead stands
// now, with from the stage it left for an advanced one. It queues nothing
// for a bead of this yard's own, one that is returned (the return said how
// it ended), one whose link is removed, or one whose link does not send
// PeerThread: such a link is as it was before the word, and logs nothing of
// what it did not send.
func (w *wtx) tellPeer(uuid, event, from string) error {
	rec, err := peerBeadOf(w.Tx, uuid)
	if err != nil || rec == nil || rec.Direction != "in" || rec.ReturnedAt != nil || rec.Was != "" {
		return err
	}
	p, err := peerByKey(w.Tx, rec.Key)
	if err != nil || p == nil || !p.carries(PeerBeadEvent, false) {
		return err
	}
	var row beadRow
	if ok, err := get(w.Tx, bBeads, uuid, &row); err != nil || !ok {
		return err
	}
	body, err := json.Marshal(PeerEventMessage{V: PeerBeadVersion, Bead: row.ID, Event: event, Stage: row.Stage, From: from, Held: row.Hold, At: now()})
	if err != nil {
		return err
	}
	m := PeerMessage{ID: newPeerMessageID(), Kind: PeerBeadEvent, Re: rec.Message, Body: body}
	if err := m.check(); err != nil {
		return err
	}
	_, err = w.queuePeer(p, m, "yard")
	return err
}

// saidOfBead takes the bead event m that p sent about a bead of this yard
// that is at it: the record of the bead's stay learns the peer's handle for
// its bead, the stage it is in there, whether it is held, and when the peer
// said so, peer_bead_event is logged on the bead, and the event is one
// delivery of the source peer (peerDelivered). Nothing else of the bead
// changes: an event moves nothing, ends no stage and reports no
// outcome. One for a stay that is no open stay of a bead at p (another
// peer's, one that ended, a bead taken back) changes nothing and is logged
// with stale, as a late return is.
//
// Every event that comes counts toward the peer's hour of them
// (PeerEventLimit), the stale and the refused too; of those over the limit
// only the first of the hour is logged. It returns why an event that is
// none was not taken, and changes p (the count), which the caller saves.
func (w *wtx) saidOfBead(p *Peer, m PeerMessage, way string) (string, error) {
	actor := "peer:" + p.Name
	extra := map[string]any{"direction": "in", "way": way}
	refuse := func(reason string) (string, error) {
		extra["reason"] = reason
		return reason, w.event("", "peer_message_refused", actor, peerEvent(p.Name, m, extra))
	}
	p.hour(now())
	if p.Events++; p.Events > PeerEventLimit {
		if p.EventLimited {
			return RefusedRate, nil
		}
		p.EventLimited = true
		extra["limit"], extra["until"] = PeerEventLimit, p.MailSince.Add(PeerMailWindow)
		return refuse(RefusedRate)
	}
	var head struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(m.Body, &head); err != nil {
		return refuse(RefusedNotEvent)
	}
	if head.V == nil || *head.V != PeerBeadVersion {
		if head.V != nil {
			extra["v"] = *head.V
		}
		extra["known"] = PeerBeadVersion
		return refuse(RefusedVersion)
	}
	// Held is required: a missing or null value must not be mistaken for
	// false, since that would make an incomplete peer statement look valid.
	var held struct {
		Value *bool `json:"held"`
	}
	if err := json.Unmarshal(m.Body, &held); err != nil || held.Value == nil {
		return refuse(RefusedNotEvent)
	}
	var e PeerEventMessage
	if err := json.Unmarshal(m.Body, &e); err != nil || m.Re == "" || !slices.Contains(PeerEvents, e.Event) ||
		!validID.MatchString(e.Bead) || isUUID(e.Bead) || !peerStage.MatchString(e.Stage) || (e.From != "" && !peerStage.MatchString(e.From)) {
		return refuse(RefusedNotEvent)
	}
	if e.Event != PeerEventAdvanced {
		e.From = "" // only a move left a stage
	}
	data := map[string]any{"peer": p.Name, "id": m.ID, "re": m.Re, "from": e.Bead, "event": e.Event, "stage": e.Stage, "held": e.Held, "way": way}
	if e.From != "" {
		data["left"] = e.From
	}
	stale := func(bead, why string) (string, error) {
		data["stale"], data["reason"] = true, why
		return "", w.event(bead, "peer_bead_event", actor, data)
	}
	rec, err := peerSentOf(w.Tx, sentBead(m.Re))
	if err != nil {
		return "", err
	}
	if rec == nil || rec.Message != m.Re || rec.Key != p.Key || rec.Was != "" {
		bead := ""
		if rec != nil && rec.Key == p.Key && rec.Was == "" {
			bead = rec.Bead
		}
		return stale(bead, "it is about no bead this yard has at "+p.Name)
	}
	if rec.EndedAt != nil {
		return stale(rec.Bead, "the bead's stay at "+p.Name+" had ended, as "+rec.Outcome)
	}
	if left, err := leftStay(w.Tx, rec); err != nil {
		return "", err
	} else if left {
		return stale(rec.Bead, fmt.Sprintf("the bead has left the stage %s it was sent from", rec.Stage))
	}
	// When it was said is the peer's word: one that is not a time that has
	// been is when this yard heard it.
	at := e.At.UTC()
	if at.IsZero() || at.After(now()) {
		at = now()
	}
	rec.Theirs, rec.There, rec.ThereHeld, rec.Said = e.Bead, e.Stage, e.Held, &at
	if rec.Delivered == nil {
		// The peer has the bead, whatever became of the answer to its post.
		heard := now()
		rec.Delivered = &heard
	}
	if err := put(w.Tx, bPeerSent, rec.Bead, rec); err != nil {
		return "", err
	}
	if err := w.event(rec.Bead, "peer_bead_event", actor, data); err != nil {
		return "", err
	}
	// And whoever subscribed to the source peer hears of it.
	return "", w.peerDelivered(p, rec, e)
}

// There is the second line yardr bead show has for a bead that is at
// another yard, once that yard has said how its bead stands: its handle,
// the stage it is in there and whether it is held, as the peer said and
// when. "" for a bead whose peer has said nothing, and for a stay that has
// ended, whose first line says how.
func (p BeadPeer) There() string {
	if p.Direction != "out" || p.SaidAt == nil || p.ReturnedAt != nil {
		return ""
	}
	held := ""
	if p.ThereHeld {
		held = ", held"
	}
	return fmt.Sprintf("there it is %s, in the stage %s%s (as %s said %s)", p.Bead, p.ThereStage, held, or(p.Peer, p.Was), stamp(*p.SaidAt))
}

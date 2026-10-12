package store

import "time"

// PeerAsk is an ask of a person of a linked yard (docs/peers.md): the
// question stands among their decisions there, and their pick comes back
// onto the bead it was asked on. It is queued in the outbox for the peer
// in the write that stores the ask (AddAsk), and crosses as any message
// does. One kind covers both directions: an answer is a message of kind
// ask whose Re is the ask message's id, as a pong is a ping with Re.
const PeerAsk = "ask"

// PeerAskVersion is the v of the ask bodies this build writes. A body of
// another v is refused (RefusedVersion), as a bead's is.
const PeerAskVersion = 1

// peerAsk is the body of a message of kind PeerAsk carrying the question:
// v, the bead's handle, title, topic and stage, the bead's body cut at
// 16 KB saying so, the ask's seq, asker and since, the ask's JSON whole,
// and each about-bead as id, title and stage. To is the person of the
// peer's yard who must answer, From the crew member or user of this yard
// who asked.
type peerAsk struct {
	V      int           `json:"v"`
	To     string        `json:"to"`
	From   string        `json:"from"`
	Bead   string        `json:"bead"`
	Title  string        `json:"title"`
	Topic  string        `json:"topic,omitempty"`
	Stage  string        `json:"stage"`
	Labels []string      `json:"labels,omitempty"`
	Body   string        `json:"body,omitempty"`
	Seq    uint64        `json:"seq"`
	Since  time.Time     `json:"since"`
	Ask    Ask           `json:"ask"`
	About  []peerAskBead `json:"about,omitempty"`
}

// peerAskBead is one about-bead of a cross-yard ask, as carried: id,
// title and stage, text the other yard reads but never follows.
type peerAskBead struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Stage string `json:"stage"`
}

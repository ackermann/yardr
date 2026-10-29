package store

import "context"

// A call that came through a device (docs/users.md, "Devices") is made as
// the device's user, and says how it came: every event the call writes as
// that user carries "via", the device's name, and so does a note it puts on
// a bead. Whoever reads the writer reads the mark with it, an agent too:
// "mckean (via phone)". Down the link from the edge the mark says that
// too: "phone via edge". It is a record of the way in, nothing is decided
// by it.

type viaKey struct{}

type via struct{ user, device string }

// WithVia records on ctx that the call is made by user through device: what
// the listener's handler does for a call it let in (rpc.UserServer). Nothing
// on the socket sets it.
func WithVia(ctx context.Context, user, device string) context.Context {
	return context.WithValue(ctx, viaKey{}, via{user, device})
}

// The way a call came in is on its context, set by the door it came through
// and by no argument of the call: the socket marks it with the process at
// its other end (WithCaller), the listener with the token of the device that
// made it (WithVia), and a door that took a call over a link marks it with
// the link (WithReach). An approval reads it to know its own path
// (approveAsk). It is a statement by the door, as the pid is: a call that
// names a user or a peer in its arguments says nothing of how it came.
// The reach of the link a call came through is the peer under reachKey: a
// name of that yard, taken as a user of this one by the link's grant
// (Peer.Reach, peertrust.go), made it. It is what a door sets that takes
// a user's word from another yard, and it is the one mark that says so:
// the user and the device the call is made as are this yard's own, and
// the link would otherwise be lost behind them. Only a test sets it
// (WithReach in export_test.go).
//
// No peer-reach door approves an ask today: ApproveAsk is refused for a
// bead of another yard, and an answer that comes home over a peer reach
// cannot approve (answerable). The mark also keeps an approval from
// changing the peer link it came through (runCommand.link).
type reachKey struct{}

// reachOf is the peer whose reach the call came through, "" for a call that
// came through none: the socket's, and a device's on the listener.
func reachOf(ctx context.Context) string {
	peer, _ := ctx.Value(reachKey{}).(string)
	return peer
}

// writeVia is write for a call a device may make: what it writes as the
// device's user is marked (wtx.event, wtx.addNoted).
func (s *Store) writeVia(ctx context.Context, fn func(*wtx) error) error {
	v, ok := ctx.Value(viaKey{}).(via)
	if !ok {
		return s.write(fn)
	}
	return s.write(func(w *wtx) error {
		w.via = v
		return fn(w)
	})
}

// viaOf is the device a write by actor came through in this transaction, ""
// for a write made on the yard's own machine and for what the yard writes
// beside the user's own (a refusal it mails, a notification).
func (w *wtx) viaOf(actor string) string {
	if w.via.device == "" || actor != w.via.user {
		return ""
	}
	return w.via.device
}

// Via is the device the event e was written through, "" for any other.
func Via(e Event) string {
	v, _ := e.Data["via"].(string)
	return v
}

// WitnessTerminal is how an approval made on the yard's socket is
// witnessed: at a terminal on the yard's machine, outside an agent's
// pane. A strong card's approval records it on the answer's event, beside
// the via a device's approval would carry; a device never carries one
// (design yardr-2nb1 5.1).
const WitnessTerminal = "terminal"

// witnessTerminal marks a strong card's approval as witnessed at a
// terminal, for the answer's event: on the socket alone, never through a
// device, which carries no witness. An approval calls it just before it
// records its answer, so the answer's event alone carries the mark.
func (w *wtx) witnessTerminal(stored []AskWeight, actor string) {
	if strongWeight(stored) && w.viaOf(actor) == "" {
		w.witness = WitnessTerminal
	}
}

// addWitnessed records the approval's answer as addNoted does, witnessed
// terminal where a strong card was approved on the socket.
func (w *wtx) addWitnessed(row *beadRow, text, actor string, stored []AskWeight) (*Note, error) {
	w.witnessTerminal(stored, actor)
	n, err := w.addNoted(row, NoteAnswer, text, actor)
	// Keep the witness on the answer event alone, even if this write grows
	// another event after recording the answer.
	w.witness = ""
	return n, err
}

// ViaMark is who with the device a write came through behind it,
// "mckean (via phone)", and who alone for a write that came through none.
func ViaMark(who, device string) string {
	if device == "" {
		return who
	}
	return who + " (via " + device + ")"
}

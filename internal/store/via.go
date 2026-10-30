package store

import (
	"context"
	"time"
)

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
// device's user is marked (wtx.event, wtx.addNoted), and a fresh sign-in
// the call carries is read by the strong card's gate (gateRefusal).
func (s *Store) writeVia(ctx context.Context, fn func(*wtx) error) error {
	v, hasVia := ctx.Value(viaKey{}).(via)
	fw, hasFresh := FreshWitnessOf(ctx)
	if !hasVia && !hasFresh {
		return s.write(fn)
	}
	return s.write(func(w *wtx) error {
		if hasVia {
			w.via = v
		}
		if hasFresh {
			w.fresh, w.hasFresh = fw, true
		}
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

// WitnessFreshSignIn is how a strong card's approval in the hosted web is
// witnessed: the gateway authenticated the operator inside its
// privileged-session window, in a strong card's sign-in rather than a
// standing login. One such sign-in may witness several strong cards inside
// the window.
const WitnessFreshSignIn = "fresh sign-in"

// FreshWitness is the door's statement that the call carries a fresh
// sign-in: who the gateway authenticated, and when (its auth_time claim).
// The web sets it from the token it verified on the request that approves
// (WithFreshWitness); the socket sets none, and a device cannot carry one,
// since the listener reads no such statement. No approval argument carries
// it, so a device that adds a field gains nothing.
type FreshWitness struct {
	User     string
	AuthTime time.Time
}

type freshWitnessKey struct{}

// WithFreshWitness records on ctx that the call carries the gateway's
// fresh authentication of user at authTime: what the web does for the
// approval its request makes, and what the socket's server reads back
// from the header the call crossed with.
func WithFreshWitness(ctx context.Context, user string, authTime time.Time) context.Context {
	return context.WithValue(ctx, freshWitnessKey{}, FreshWitness{User: user, AuthTime: authTime})
}

// FreshWitnessOf is the call's fresh witness, ok false where it carries
// none: the socket's, and a device's.
func FreshWitnessOf(ctx context.Context) (fw FreshWitness, ok bool) {
	fw, ok = ctx.Value(freshWitnessKey{}).(FreshWitness)
	return fw, ok
}

// witnessStrong marks a strong card's approval as witnessed, for the
// answer's event: a fresh sign-in where the call carried one the gate
// took, else a terminal, on the socket alone, never through a device,
// which carries no witness. An approval calls it just before it records
// its answer, so the answer's event alone carries the mark.
func (w *wtx) witnessStrong(stored []AskWeight, actor string) {
	if StrongWeight(stored) && w.viaOf(actor) == "" {
		w.witness = WitnessTerminal
		if w.hasFresh {
			w.witness = WitnessFreshSignIn
		}
	}
}

// addWitnessed records the approval's answer as addNoted does, witnessed
// where a strong card was approved on the socket: a fresh sign-in for one
// the hosted web approved, else a terminal.
func (w *wtx) addWitnessed(row *beadRow, text, actor string, stored []AskWeight) (*Note, error) {
	w.witnessStrong(stored, actor)
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

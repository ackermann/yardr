package store

import "context"

// A call that came through a device (docs/users.md, "Devices") is made as
// the device's user, and says how it came: every event the call writes as
// that user carries "via", the device's name, and so does a note it puts on
// a bead. Whoever reads the writer reads the mark with it, an agent too:
// "mckean (via phone)". It is a record of the way in, nothing is decided by
// it.

type viaKey struct{}

type via struct{ user, device string }

// WithVia records on ctx that the call is made by user through device: what
// the listener's handler does for a call it let in (rpc.UserServer). Nothing
// on the socket sets it.
func WithVia(ctx context.Context, user, device string) context.Context {
	return context.WithValue(ctx, viaKey{}, via{user, device})
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

// ViaMark is who with the device a write came through behind it,
// "mckean (via phone)", and who alone for a write that came through none.
func ViaMark(who, device string) string {
	if device == "" {
		return who
	}
	return who + " (via " + device + ")"
}

package store

import (
	"context"
	"errors"

	bolt "go.etcd.io/bbolt"
)

// MoveRefused is the event a move refused to a session's agent leaves on
// the bead (see Store.refused).
const MoveRefused = "move_refused"

// Why a move was refused, as move_refused says it. RefusedOther is a move
// that failed for a reason the store does not name: the agent's command did
// not take either way.
const (
	RefusedStale      = "stale"       // the session no longer owns the bead, or the bead changed (Expect)
	RefusedNotAllowed = "not_allowed" // the bead's flow has no such move or outcome
	RefusedNotMember  = "not_member"  // the stage's group has members, and the agent is none
	RefusedNotFound   = "not_found"   // no such bead: logged on the session's own
	RefusedOther      = "other"
)

// refusal is a move's error with the reason move_refused gives for it,
// where the error is no sentinel's. It reads as the error it wraps.
type refusal struct {
	reason string
	error
}

func (r refusal) Unwrap() error { return r.error }

// refuse gives err the reason a move_refused says; nil stays nil.
func refuse(reason string, err error) error {
	if err == nil {
		return nil
	}
	return refusal{reason, err}
}

func refusedWhy(err error) string {
	var r refusal
	switch {
	case errors.As(err, &r):
		return r.reason
	case errors.Is(err, ErrConflict):
		return RefusedStale
	case errors.Is(err, ErrNotFound):
		return RefusedNotFound
	}
	return RefusedOther
}

// refused logs move_refused when the move command of bead id failed with
// cause for the agent of a session, and returns cause. An agent whose done
// did not take sits idle with "done" as its pane's last word, and nothing
// on the bead says it tried: a move that works ends the session, so this
// event is the only trace of one that did not. The session is the one the
// move was checked as (the caller's pane, else the one exp names, see
// Expect). A person or a resident runs in no session and a manual session
// is a person's claim: their refused commands log nothing, they read the
// error themselves.
//
// At most one per bead, session and reason, so an agent that retries the
// same refused command says it once. The event is on the bead the move
// named, or on the session's own when there is no such bead.
func (s *Store) refused(ctx context.Context, id, command string, exp Expect, actor string, cause error) error {
	if cause == nil || callerOf(ctx) <= 0 && exp.Session == "" {
		return cause
	}
	reason := refusedWhy(cause)
	// Best effort: the move's own error is what the caller is owed.
	_ = s.write(func(w *wtx) error {
		sess, err := moverSession(w.Tx, callerOf(ctx), exp)
		if err != nil || sess == nil || sess.Manual() {
			return err
		}
		bead, err := resolve(w.Tx, id)
		if err != nil {
			bead = sess.Bead
		}
		for _, k := range keys(w.Tx, bEventsBy, bead+"\x00") {
			var e Event
			if _, err := get(w.Tx, bEvents, k[len(bead)+1:], &e); err != nil {
				return err
			}
			if e.Kind == MoveRefused && e.Data["session"] == sess.ID && e.Data["reason"] == reason {
				return nil
			}
		}
		agent, _ := sess.Handle["agent"].(string)
		data := map[string]any{"session": sess.ID, "agent": or(agent, actor), "group": sess.Group, "stage": sess.Stage,
			"command": command, "reason": reason, "error": cause.Error(),
			"hint": "the agent ran this command and the yard refused it, so the bead did not move for it; " +
				"an agent that took it for done sits idle. error says why"}
		if bead != sess.Bead {
			data["session_bead"] = handlesIn(w.Tx).of(sess.Bead)
		}
		return w.event(bead, MoveRefused, "yard", data)
	})
	return cause
}

// moverSession is the session a move is checked as (see callerSession), or
// nil: no pane of a session made the call and exp names none the store
// knows.
func moverSession(tx *bolt.Tx, pid int, exp Expect) (*Session, error) {
	if pane, err := paneSession(tx, pid); err != nil || pane != nil {
		return pane, err
	}
	if exp.Session == "" {
		return nil, nil
	}
	sess := new(Session)
	if ok, err := get(tx, bSessions, exp.Session, sess); err != nil || !ok {
		return nil, err
	}
	return sess, nil
}

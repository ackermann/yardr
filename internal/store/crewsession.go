package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// This file keeps one session of a crew member: the line session ("") or a
// conversation session (its user). Each For method is its line sibling in
// crew.go for one session; the sibling calls it with "". A user with no
// conversation (unserved, or no env_for_ key) is refused: its mail falls
// back to the line slice.

// changeCrewSessionAt is changeCrewAt for one session: fn sees the member
// and the session's runtime state, and what it changes is stored under a
// fresh revision. A conversation's row is made where missing, starting its
// cursors at the line session's (ensureConversation); a user with no
// conversation and no row is refused. A row whose conversation the config
// no longer declares stays writable, so the daemon can stop what still
// runs of it.
func (s *Store) changeCrewSessionAt(name, user, rev string, fn func(*wtx, *CrewMember, *CrewSession) error) error {
	return s.write(func(w *wtx) error {
		var r CrewMember
		ok, err := get(w.Tx, bCrew, name, &r)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("crew member %s: %w", name, ErrNotFound)
		}
		if err := checkCrewRev(name, r.Revision, rev); err != nil {
			return err
		}
		st := &r.CrewSession
		if user != "" {
			_, row := r.Conversations[user]
			if !r.HasConversation(user) && !row {
				return fmt.Errorf("crew member %s has no conversation session for %s", name, user)
			}
			cs := r.ensureConversation(user)
			st = &cs
		}
		if err := fn(w, &r, st); err != nil {
			return err
		}
		if user != "" {
			r.Conversations[user] = *st
		}
		r.Revision = newRevision()
		return put(w.Tx, bCrew, name, r)
	})
}

// changeCrewSession is changeCrewSessionAt expecting no revision: the
// daemon's own bookkeeping writes through it unchecked.
func (s *Store) changeCrewSession(name, user string, fn func(*wtx, *CrewMember, *CrewSession) error) error {
	return s.changeCrewSessionAt(name, user, "", fn)
}

// sessionData is data with the session named where it is a conversation's:
// the journal's events say which session started, stopped or is blocked.
func sessionData(user string, data map[string]any) map[string]any {
	if user != "" {
		data["user"] = user
	}
	return data
}

// CrewStartingFor is CrewStarting for one session. The daemon's alone to
// record a starting handle: pane checks trust it before the session starts.
// A nil handle clears one when yardr down closes a start in progress.
func (s *Store) CrewStartingFor(ctx context.Context, name, user string, handle map[string]any) error {
	if handle != nil {
		if err := s.daemonOnly(ctx, "CrewStartingFor"); err != nil {
			return err
		}
	}
	return s.changeCrewSession(name, user, func(_ *wtx, _ *CrewMember, st *CrewSession) error {
		st.Starting = handle
		return nil
	})
}

// CrewStartedFor is CrewStarted for one session. The daemon's alone
// (daemonOnly): only it starts crew sessions.
func (s *Store) CrewStartedFor(ctx context.Context, name, user string, handle map[string]any) error {
	if err := s.daemonOnly(ctx, "CrewStartedFor"); err != nil {
		return err
	}
	return s.changeCrewSession(name, user, func(w *wtx, r *CrewMember, st *CrewSession) error {
		t := now()
		st.Starting = nil
		st.PrevStartedAt, st.StartedAt = st.StartedAt, &t
		st.State, st.Status, st.Handle, st.RetryAfter = "running", "", handle, nil
		st.RestartRequested, st.RestartRequestedAt, st.RotationAskedAt = "", nil, nil
		// The new session sees its unread slice in its prime; only events
		// after this point warrant a nudge.
		if k, _ := w.Bucket(bEvents).Cursor().Last(); k != nil {
			st.Notified = max(st.Notified, binary.BigEndian.Uint64(k))
		}
		return w.event(r.Journal, "crew_started", "yard", sessionData(user, map[string]any{"crew": name, "handle": handle}))
	})
}

// CrewSettledFor is CrewSettled for one session. The daemon's alone
// (daemonOnly): only it watches crew sessions live.
func (s *Store) CrewSettledFor(ctx context.Context, name, user string) error {
	if err := s.daemonOnly(ctx, "CrewSettledFor"); err != nil {
		return err
	}
	return s.changeCrewSession(name, user, func(_ *wtx, _ *CrewMember, st *CrewSession) error {
		st.Failures = 0
		return nil
	})
}

// CrewStatusFor is CrewStatus for one session. The daemon's alone
// (daemonOnly): only it sees what a crew session is doing.
func (s *Store) CrewStatusFor(ctx context.Context, name, user, status string) error {
	if err := s.daemonOnly(ctx, "CrewStatusFor"); err != nil {
		return err
	}
	if !slices.Contains(CrewStatuses, status) {
		return fmt.Errorf("crew member status %q: it is one of %s", status, strings.Join(CrewStatuses, ", "))
	}
	var r CrewMember
	if err := s.read(func(tx *bolt.Tx) error {
		_, err := get(tx, bCrew, name, &r)
		return err
	}); err != nil {
		return err
	}
	if r.Session(user).Status == status {
		return nil // no write, and no new revision, for what did not change
	}
	return s.changeCrewSession(name, user, func(w *wtx, r *CrewMember, st *CrewSession) error {
		if st.State != "running" || st.Status == status {
			return nil
		}
		was := st.Status
		st.Status = status
		return w.event("", "crew_status", "yard", sessionData(user, map[string]any{"crew": name, "status": status, "was": was}))
	})
}

// SetCrewHandleFor is SetCrewHandle for one session. The daemon's alone
// (daemonOnly): the pane checks trust the handle.
func (s *Store) SetCrewHandleFor(ctx context.Context, name, user string, handle map[string]any) error {
	if err := s.daemonOnly(ctx, "SetCrewHandleFor"); err != nil {
		return err
	}
	return s.changeCrewSession(name, user, func(_ *wtx, _ *CrewMember, st *CrewSession) error {
		st.Handle = handle
		return nil
	})
}

// CrewStoppedFor is CrewStopped for one session.
func (s *Store) CrewStoppedFor(ctx context.Context, name, user, reason string) error {
	return s.changeCrewSession(name, user, func(w *wtx, r *CrewMember, st *CrewSession) error {
		if st.State != "running" {
			return nil
		}
		st.State, st.Status, st.Handle = "stopped", "", nil
		return w.event(r.Journal, "crew_stopped", "yard", sessionData(user, map[string]any{"crew": name, "reason": reason}))
	})
}

// CrewGoneFor is CrewGone for one session. The daemon's alone
// (daemonOnly): only it reaps crew sessions.
func (s *Store) CrewGoneFor(ctx context.Context, name, user, reason string) error {
	if err := s.daemonOnly(ctx, "CrewGoneFor"); err != nil {
		return err
	}
	return s.changeCrewSession(name, user, func(w *wtx, r *CrewMember, st *CrewSession) error {
		if st.State != "running" {
			return nil
		}
		if len(st.Handle) > 0 {
			st.Left = append(st.Left, st.Handle)
		}
		st.State, st.Status, st.Handle = "stopped", "", nil
		return w.event(r.Journal, "crew_stopped", "yard", sessionData(user, map[string]any{"crew": name, "reason": reason}))
	})
}

// CrewLeftFor is CrewLeft for one session. The daemon's alone
// (daemonOnly): only it closes what a crew session left.
func (s *Store) CrewLeftFor(ctx context.Context, name, user string, left []map[string]any) error {
	if err := s.daemonOnly(ctx, "CrewLeftFor"); err != nil {
		return err
	}
	return s.changeCrewSession(name, user, func(_ *wtx, _ *CrewMember, st *CrewSession) error {
		st.Left = left
		return nil
	})
}

// CrewStartFailedFor is CrewStartFailed for one session. The daemon's
// alone (daemonOnly): only it starts crew sessions.
func (s *Store) CrewStartFailedFor(ctx context.Context, name, user, reason string, backoff time.Duration) error {
	if err := s.daemonOnly(ctx, "CrewStartFailedFor"); err != nil {
		return err
	}
	return s.changeCrewSession(name, user, func(w *wtx, r *CrewMember, st *CrewSession) error {
		st.Failures++
		// What the failed start opened, the daemon has closed as far as it
		// could: a pane it could not close may still hold the agent, and is
		// the crew member's for as long as its shell runs (paneCrew). A
		// shell without a start time cannot be told from a later process
		// with its pid, so that one is taken off.
		if _, known := uintIn(st.Starting["shell_start"]); !known {
			st.Starting = nil
		}
		retry := now().Add(backoff)
		st.RetryAfter, st.State = &retry, "stopped"
		kind := "crew_start_failed"
		if st.Failures >= MaxStartFails {
			st.State, kind = "failed", "crew_gave_up"
		}
		return w.event(r.Journal, kind, "yard", sessionData(user, map[string]any{"crew": name, "reason": reason, "failures": st.Failures}))
	})
}

// RequestRestartFor is RequestRestart for one session. A rev other than ""
// must be the crew member's current revision.
func (s *Store) RequestRestartFor(ctx context.Context, name, user, reason, rev, actor string) error {
	return s.changeCrewSessionAt(name, user, rev, func(w *wtx, r *CrewMember, st *CrewSession) error {
		t := now()
		st.RestartRequested, st.RestartRequestedAt = reason, &t
		return w.event(r.Journal, "crew_restart_requested", actor, sessionData(user, map[string]any{"crew": name, "reason": reason}))
	})
}

// MarkRotationAskedFor is MarkRotationAsked for one session. The
// daemon's alone (daemonOnly): only it rotates crew sessions.
func (s *Store) MarkRotationAskedFor(ctx context.Context, name, user string) error {
	if err := s.daemonOnly(ctx, "MarkRotationAskedFor"); err != nil {
		return err
	}
	return s.changeCrewSession(name, user, func(w *wtx, r *CrewMember, st *CrewSession) error {
		t := now()
		st.RotationAskedAt = &t
		return w.event(r.Journal, "crew_rotation_asked", "yard", sessionData(user, map[string]any{"crew": name}))
	})
}

// MarkNotifiedFor is MarkNotified for one session. The daemon's alone
// (daemonOnly): only it nudges crew sessions.
func (s *Store) MarkNotifiedFor(ctx context.Context, name, user string, seq uint64) error {
	if err := s.daemonOnly(ctx, "MarkNotifiedFor"); err != nil {
		return err
	}
	return s.changeCrewSession(name, user, func(_ *wtx, _ *CrewMember, st *CrewSession) error {
		st.Notified = max(st.Notified, seq)
		return nil
	})
}

// AckInboxFor is AckInbox for one session's slice: acking the line's
// slice leaves a conversation's unread, and acking a conversation's the
// line's.
func (s *Store) AckInboxFor(ctx context.Context, name, user string, through uint64) error {
	return s.changeCrewSession(name, user, func(w *wtx, r *CrewMember, st *CrewSession) error {
		was := st.Cursor
		st.Cursor = max(st.Cursor, through)
		st.Notified = max(st.Notified, st.Cursor)
		return w.crewMailRead(r, user, was, st.Cursor)
	})
}

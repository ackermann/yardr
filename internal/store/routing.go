package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Group is whoever works a stage. Runner names the implementation (herdr,
// exec, manual, ...); Config is runner-specific and opaque to the engine.
type Group struct {
	Name   string         `json:"name"`
	Runner string         `json:"runner"`
	Config map[string]any `json:"config"`
	Limit  int            `json:"limit"`
}

func (s *Store) PutGroup(ctx context.Context, g Group) error {
	if g.Config == nil {
		g.Config = map[string]any{}
	}
	if g.Limit < 1 {
		g.Limit = 1
	}
	return s.write(func(w *wtx) error { return put(w.Tx, bGroups, g.Name, g) })
}

func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	out := []Group{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bGroups, "", func(_ []byte, g *Group) error {
			out = append(out, *g)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetGroup(ctx context.Context, name string) (*Group, error) {
	g := new(Group)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bGroups, name, g)
		if err == nil && !ok {
			err = fmt.Errorf("group %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// DeleteGroup removes the group and the routes that send work to it.
func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	return s.write(func(w *wtx) error {
		var drop []string
		if err := each(w.Tx, bRoutes, "", func(k []byte, r *Route) error {
			if r.Group == name {
				drop = append(drop, string(k))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range drop {
			if err := w.Bucket(bRoutes).Delete([]byte(k)); err != nil {
				return err
			}
		}
		return w.Bucket(bGroups).Delete([]byte(name))
	})
}

// Route sends open, unblocked beads in Stage to Group. Nil fields match any.
type Route struct {
	ID       string  `json:"id"`
	Stage    string  `json:"stage"`
	Type     *string `json:"type,omitempty"`
	Rig      *string `json:"rig,omitempty"`
	Label    *string `json:"label,omitempty"`
	Group    string  `json:"group"`
	Priority int     `json:"priority"`
}

func (s *Store) AddRoute(ctx context.Context, r Route) (*Route, error) {
	err := s.write(func(w *wtx) error {
		if w.Bucket(bGroups).Get([]byte(r.Group)) == nil {
			return fmt.Errorf("group %s: %w", r.Group, ErrNotFound)
		}
		if r.Rig != nil && w.Bucket(bRigs).Get([]byte(*r.Rig)) == nil {
			return fmt.Errorf("rig %s: %w", *r.Rig, ErrNotFound)
		}
		r.ID = newEdgeID()
		return put(w.Tx, bRoutes, r.ID, r)
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) Routes(ctx context.Context) ([]Route, error) {
	out := []Route{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bRoutes, "", func(_ []byte, r *Route) error {
			out = append(out, *r)
			return nil
		})
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stage != out[j].Stage {
			return out[i].Stage < out[j].Stage
		}
		return out[i].Priority > out[j].Priority
	})
	return out, err
}

func (s *Store) DeleteRoute(ctx context.Context, id string) error {
	return s.write(func(w *wtx) error { return w.Bucket(bRoutes).Delete([]byte(id)) })
}

type Session struct {
	ID        string         `json:"id"`
	Bead      string         `json:"bead"`
	Group     string         `json:"group"`
	Stage     string         `json:"stage"`
	Runner    string         `json:"runner,omitempty"` // the group's runner when it started
	State     string         `json:"state"`            // starting running done released died failed
	Handle    map[string]any `json:"handle,omitempty"`
	Error     *string        `json:"error,omitempty"`
	Stopped   bool           `json:"stopped"`
	Cleaned   bool           `json:"cleaned,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   *time.Time     `json:"ended_at,omitempty"`
}

func (s Session) live() bool { return s.State == "starting" || s.State == "running" }

// Claim atomically assigns an unowned bead to group and opens a session.
// It returns ErrTaken if the bead is missing, owned, in another stage, or
// blocked or held: anything that changed since the caller read it.
func (s *Store) Claim(ctx context.Context, beadID, group, stage, actor string) (*Session, error) {
	var sess *Session
	err := s.write(func(w *wtx) error {
		row, err := w.loadBead(beadID)
		if err != nil {
			return ErrTaken
		}
		if row.Group != nil || row.Stage != stage || !ready(w.Tx, &row.Bead) {
			return ErrTaken
		}
		var g Group
		if ok, err := get(w.Tx, bGroups, group, &g); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("group %s: %w", group, ErrNotFound)
		}
		sess = &Session{ID: newEdgeID(), Bead: beadID, Group: group, Stage: stage, Runner: g.Runner,
			State: "starting", StartedAt: now()}
		if err := put(w.Tx, bSessions, sess.ID, sess); err != nil {
			return err
		}
		row.Group, row.Session = &group, &sess.ID
		if err := w.mintBead(row, actor); err != nil {
			return err
		}
		return w.event(beadID, "claimed", actor, map[string]any{"group": group, "stage": stage, "session": sess.ID})
	})
	return sess, err
}

// changeSession applies fn to a session inside a write transaction.
func (s *Store) changeSession(id string, fn func(*wtx, *Session) error) error {
	return s.write(func(w *wtx) error {
		var sess Session
		ok, err := get(w.Tx, bSessions, id, &sess)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		if err := fn(w, &sess); err != nil {
			return err
		}
		return put(w.Tx, bSessions, id, sess)
	})
}

// MarkRunning records the runner handle once the runner has started.
func (s *Store) MarkRunning(ctx context.Context, sessionID string, handle map[string]any) error {
	return s.changeSession(sessionID, func(w *wtx, sess *Session) error {
		if sess.State != "starting" {
			return nil
		}
		sess.State, sess.Handle = "running", handle
		return w.event(sess.Bead, "started", "yard", map[string]any{"session": sessionID, "handle": handle})
	})
}

// EndSession ends a live session as 'died' or 'failed' and, if the bead is
// still owned by it, returns the bead for routing. Ending an ended session
// is a no-op.
func (s *Store) EndSession(ctx context.Context, sessionID, state, reason string) error {
	if state != "died" && state != "failed" {
		return fmt.Errorf("EndSession: state %q not allowed", state)
	}
	return s.changeSession(sessionID, func(w *wtx, sess *Session) error {
		if !sess.live() {
			return nil
		}
		t := now()
		sess.State, sess.Error, sess.EndedAt = state, &reason, &t
		old, err := w.loadBead(sess.Bead)
		if err != nil {
			return err
		}
		if old.Session != nil && *old.Session == sessionID {
			row := cloneRow(old)
			row.Group, row.Session = nil, nil
			if _, err := w.saveBead(old, row, "yard"); err != nil {
				return err
			}
		}
		return w.event(sess.Bead, "session_"+state, "yard", map[string]any{"session": sessionID, "reason": reason})
	})
}

func (s *Store) MarkStopped(ctx context.Context, sessionID string) error {
	return s.changeSession(sessionID, func(_ *wtx, sess *Session) error {
		sess.Stopped = true
		return nil
	})
}

// SetHandle records runner resources on a session without changing state,
// so teardown can clean up after a failed start.
func (s *Store) SetHandle(ctx context.Context, sessionID string, handle map[string]any) error {
	return s.changeSession(sessionID, func(_ *wtx, sess *Session) error {
		sess.Handle = handle
		return nil
	})
}

// MarkCleaned records that a closed bead's workspace was handled, so it is
// attempted once.
func (s *Store) MarkCleaned(ctx context.Context, sessionID string) error {
	return s.changeSession(sessionID, func(_ *wtx, sess *Session) error {
		sess.Cleaned = true
		return nil
	})
}

func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	sess := new(Session)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bSessions, id, sess)
		if err == nil && !ok {
			err = fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) sessionsWhere(match func(*bolt.Tx, *Session) bool) ([]Session, error) {
	out := []Session{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
			if match(tx, sess) {
				out = append(out, *sess)
			}
			return nil
		})
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, err
}

// Sessions lists live sessions, or every session when all is set.
func (s *Store) Sessions(ctx context.Context, all bool) ([]Session, error) {
	return s.sessionsWhere(func(_ *bolt.Tx, sess *Session) bool { return all || sess.live() })
}

// Unstopped lists ended sessions whose runner resources are still up.
func (s *Store) Unstopped(ctx context.Context) ([]Session, error) {
	return s.sessionsWhere(func(_ *bolt.Tx, sess *Session) bool { return !sess.live() && !sess.Stopped })
}

// ClosedWithWorkspace lists stopped sessions of closed beads whose runner
// left something on disk (handle.worktree) that has not been cleaned up.
func (s *Store) ClosedWithWorkspace(ctx context.Context) ([]Session, error) {
	return s.sessionsWhere(func(tx *bolt.Tx, sess *Session) bool {
		if sess.Cleaned || !sess.Stopped || sess.Handle["worktree"] == nil {
			return false
		}
		var b beadRow
		ok, _ := get(tx, bBeads, sess.Bead, &b)
		return ok && b.Status == "closed"
	})
}

// FailureCount counts failed or died sessions for bead in stage since a
// human last un-held it, so un-holding grants fresh attempts.
func (s *Store) FailureCount(ctx context.Context, bead, stage string) (int, error) {
	var since time.Time
	evs, err := s.Events(ctx, bead, 1<<30)
	if err != nil {
		return 0, err
	}
	for _, e := range evs {
		if e.Kind == "unheld" {
			since = e.At
		}
	}
	failed, err := s.sessionsWhere(func(_ *bolt.Tx, sess *Session) bool {
		return sess.Bead == bead && sess.Stage == stage && (sess.State == "died" || sess.State == "failed") &&
			sess.StartedAt.After(since)
	})
	return len(failed), err
}

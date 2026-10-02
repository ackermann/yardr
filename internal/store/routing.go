package store

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
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
	// Members are the actors who may move a bead out of a stage this group
	// works (see Advance). Only manual groups have them; none means anyone.
	// A guardrail against mistakes, not security: anyone can set YARDR_ACTOR.
	Members []string `json:"members,omitempty"`
}

func (s *Store) PutGroup(ctx context.Context, g Group) error {
	if g.Config == nil {
		g.Config = map[string]any{}
	}
	if g.Limit < 1 {
		g.Limit = 1
	}
	g.Members = dedupeOrdered(g.Members)
	if len(g.Members) > 0 && g.Runner != "manual" {
		return fmt.Errorf("group %s: members are only for manual groups (this one is %s)", g.Name, g.Runner)
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

// DeleteGroup removes the group and the routes that send work to it. Its
// live sessions end as released and their beads go back to routing in the
// same transaction: a bead owned by a deleted group would otherwise sit in
// its stage until released by hand, since no route can claim it. The
// dispatcher's teardown stops the runner resources. It reports how many
// beads it released.
func (s *Store) DeleteGroup(ctx context.Context, name, actor string) (int, error) {
	released := 0
	err := s.write(func(w *wtx) error {
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
		var live []Session
		if err := each(w.Tx, bSessions, "", func(_ []byte, sess *Session) error {
			if sess.Group == name && sess.live() {
				live = append(live, *sess)
			}
			return nil
		}); err != nil {
			return err
		}
		reason := "group " + name + " deleted"
		for _, sess := range live {
			owned := false
			if _, err := w.changeBead(sess.Bead, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
				if b.Session == nil || *b.Session != sess.ID {
					return "", nil, nil
				}
				owned = true
				return "released", map[string]any{"reason": reason}, w.endSession(b, "released")
			}); err != nil {
				return err
			}
			if owned {
				released++
				continue
			}
			// The bead moved on without this session; end it so it is not live.
			t := now()
			sess.State, sess.Error, sess.EndedAt = "released", &reason, &t
			if err := put(w.Tx, bSessions, sess.ID, sess); err != nil {
				return err
			}
		}
		return w.Bucket(bGroups).Delete([]byte(name))
	})
	return released, err
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

// SortRoutes orders routes so the first match wins: higher priority first,
// then the more specific route (more filters set), then by id for stability.
func SortRoutes(rs []Route) {
	spec := func(r Route) int {
		n := 0
		for _, p := range []*string{r.Type, r.Rig, r.Label} {
			if p != nil {
				n++
			}
		}
		return n
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Priority != rs[j].Priority {
			return rs[i].Priority > rs[j].Priority
		}
		if si, sj := spec(rs[i]), spec(rs[j]); si != sj {
			return si > sj
		}
		return rs[i].ID < rs[j].ID
	})
}

// MatchRoute returns the first route (in SortRoutes order) that applies to b.
func MatchRoute(sorted []Route, b Bead) *Route {
	for i := range sorted {
		r := &sorted[i]
		if r.Stage != b.Stage {
			continue
		}
		if r.Type != nil && *r.Type != b.Type {
			continue
		}
		if r.Rig != nil && (b.Rig == nil || *r.Rig != *b.Rig) {
			continue
		}
		if r.Label != nil && !slices.Contains(b.Labels, *r.Label) {
			continue
		}
		return r
	}
	return nil
}

// stageGroup is the group a bead answers to in its stage: the group working
// it, else the one the routes would send it to (a manual group at its limit
// leaves beads waiting unclaimed). Nil when neither exists.
func stageGroup(tx *bolt.Tx, b *beadRow) (*Group, error) {
	name := ""
	if b.Group != nil {
		name = *b.Group
	} else {
		var routes []Route
		if err := each(tx, bRoutes, "", func(_ []byte, r *Route) error {
			routes = append(routes, *r)
			return nil
		}); err != nil {
			return nil, err
		}
		SortRoutes(routes)
		if r := MatchRoute(routes, b.Bead); r != nil {
			name = r.Group
		}
	}
	if name == "" {
		return nil, nil
	}
	g := new(Group)
	if ok, err := get(tx, bGroups, name, g); err != nil || !ok {
		return nil, err
	}
	return g, nil
}

// checkMembers refuses moving a bead out of a stage whose group has members
// unless actor is one of them, naming who may.
func checkMembers(g *Group, id, stage, actor string) error {
	if g == nil || len(g.Members) == 0 || slices.Contains(g.Members, actor) {
		return nil
	}
	return fmt.Errorf("bead %s: only members of group %s may move it out of stage %s: %s (you are %q)",
		id, g.Name, stage, strings.Join(g.Members, ", "), actor)
}

func (s *Store) DeleteRoute(ctx context.Context, id string) error {
	return s.write(func(w *wtx) error { return w.Bucket(bRoutes).Delete([]byte(id)) })
}

type Session struct {
	ID        string         `json:"id"`
	Bead      string         `json:"bead"` // stored as the UUID, returned as the handle
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
		stored := Session{ID: newEdgeID(), Bead: row.UUID, Group: group, Stage: stage, Runner: g.Runner,
			State: "starting", StartedAt: now()}
		if err := put(w.Tx, bSessions, stored.ID, stored); err != nil {
			return err
		}
		row.Group, row.Session = &group, &stored.ID
		if err := w.mintBead(row, actor); err != nil {
			return err
		}
		sess = &stored
		sess.Bead = row.ID
		return w.event(row.UUID, "claimed", actor, map[string]any{"group": group, "stage": stage, "session": stored.ID})
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
		if ok {
			sess.Bead = handlesIn(tx).of(sess.Bead)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// sessionsWhere returns matching sessions by start time. match sees the
// session as stored, naming its bead by UUID.
func (s *Store) sessionsWhere(match func(*bolt.Tx, *Session) bool) ([]Session, error) {
	out := []Session{}
	err := s.read(func(tx *bolt.Tx) error {
		h := handlesIn(tx)
		return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
			if match(tx, sess) {
				sess.Bead = h.of(sess.Bead)
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
func (s *Store) FailureCount(ctx context.Context, ref, stage string) (int, error) {
	b, err := s.GetBead(ctx, ref)
	if err != nil {
		return 0, err
	}
	bead := b.UUID
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

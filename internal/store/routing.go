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
// exec, manual, peer); Config is runner-specific and opaque to the engine,
// but for a peer group's, which the store reads to send the bead
// (peersend.go).
type Group struct {
	Name   string         `json:"name"`
	Runner string         `json:"runner"`
	Config map[string]any `json:"config"`
	Limit  int            `json:"limit"`
	// Members are the actors who may move a bead out of a stage this group
	// works (see Advance). Only manual groups have them; none means anyone.
	// A guardrail against mistakes, not security: anyone can set YARDR_ACTOR.
	Members []string `json:"members,omitempty"`
	// Subscriptions deliver events to each member that is a resident. Kept
	// when the group is redefined.
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
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
	return s.write(func(w *wtx) error {
		var old Group
		if _, err := get(w.Tx, bGroups, g.Name, &old); err != nil {
			return err
		}
		g.Subscriptions = old.Subscriptions
		if len(g.Subscriptions) > 0 && g.Runner != "manual" {
			return fmt.Errorf("group %s has subscriptions, which only manual groups may: unsubscribe it first", g.Name)
		}
		if err := checkPeerGroup(w.Tx, &g); err != nil {
			return err
		}
		return put(w.Tx, bGroups, g.Name, g)
	})
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
		// Batch reruns this closure when another call in the batch fails, so
		// the count starts over with each attempt.
		released = 0
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
		live, err := s.liveSessions(w.Tx, name)
		if err != nil {
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
			if err := putSession(w.Tx, &sess); err != nil {
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
	State     string         `json:"state"`            // starting running done released held died failed aborted
	Handle    map[string]any `json:"handle,omitempty"`
	Error     *string        `json:"error,omitempty"`
	Stopped   bool           `json:"stopped"`
	Cleaned   bool           `json:"cleaned,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   *time.Time     `json:"ended_at,omitempty"`
	// Epic is the handle of the epic the session's bead is a child of, set
	// on listed sessions (see Bead.Epic); never stored.
	Epic string `json:"epic,omitempty"`
}

// Manual reports whether the session is a manual group's claim: nothing
// runs in it, a person works the bead. A session from before sessions
// recorded their runner is known by its handle.
func (s Session) Manual() bool {
	return s.Runner == "manual" || s.Runner == "" && s.Handle["manual"] != nil
}

func (s Session) live() bool { return s.State == "starting" || s.State == "running" }

// liveKey is the session's entry in the live index, under its group so a
// claim counts a group's sessions by prefix.
func (s Session) liveKey() []byte { return []byte(join(s.Group, s.ID)) }

// putSession writes sess and keeps it in, or out of, the live index. Every
// write of a session goes through here, so the index is exact.
func putSession(tx *bolt.Tx, sess *Session) error {
	if err := put(tx, bSessions, sess.ID, sess); err != nil {
		return err
	}
	if sess.live() {
		return tx.Bucket(bSessLive).Put(sess.liveKey(), []byte{})
	}
	return tx.Bucket(bSessLive).Delete(sess.liveKey())
}

// liveSessions reads the sessions that are starting or running, of one
// group or ("") of all, through the live index: the store keeps every
// session until it is pruned, and nothing on the dispatcher's path should
// scale with that.
func (s *Store) liveSessions(tx *bolt.Tx, group string) ([]Session, error) {
	var out []Session
	prefix := ""
	if group != "" {
		prefix = group + "\x00"
	}
	for _, k := range keys(tx, bSessLive, prefix) {
		id := k[strings.LastIndexByte(k, 0)+1:]
		var sess Session
		if ok, err := get(tx, bSessions, id, &sess); err != nil {
			return nil, err
		} else if ok {
			out = append(out, sess)
		}
	}
	s.sessionReads.Add(int64(len(out)))
	return out, nil
}

// indexSessions builds the live index, once, for a store from before it.
func indexSessions(tx *bolt.Tx) error {
	if tx.Bucket(bSessLive) != nil || tx.Bucket(bSessions) == nil {
		return nil
	}
	if _, err := tx.CreateBucket(bSessLive); err != nil {
		return err
	}
	return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
		if !sess.live() {
			return nil
		}
		return tx.Bucket(bSessLive).Put(sess.liveKey(), []byte{})
	})
}

// Claim atomically assigns an unowned bead to group and opens a session.
// It returns ErrTaken if the bead is missing, owned, in another stage, or
// blocked or held: anything that changed since the caller read it; and
// ErrAtLimit if the group already has as many live sessions as its limit.
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
		// The limit is checked here, in the claim's own transaction, so two
		// dispatchers (or a dispatcher and a stale in-memory count) cannot
		// both take the last slot. The dispatcher's own count is a fast path.
		// The live index answers it without reading the sessions that ended.
		live := len(keys(w.Tx, bSessLive, group+"\x00"))
		s.sessionReads.Add(int64(live))
		if live >= max(g.Limit, 1) {
			return fmt.Errorf("group %s runs %d of %d: %w", group, live, max(g.Limit, 1), ErrAtLimit)
		}
		stored := Session{ID: newEdgeID(), Bead: row.UUID, Group: group, Stage: stage, Runner: g.Runner,
			State: "starting", StartedAt: now()}
		if err := putSession(w.Tx, &stored); err != nil {
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
		return putSession(w.Tx, &sess)
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
// still owned by it, returns the bead for routing. A death or failure in
// the bead's current stage counts against it (see FailureCount). 'aborted'
// is a start the runtime could not make (herdr down): it returns the bead
// the same way but is no failure of the bead's, so it is not counted.
// Ending an ended session is a no-op.
func (s *Store) EndSession(ctx context.Context, sessionID, state, reason string) error {
	if state != "died" && state != "failed" && state != "aborted" {
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
		row := cloneRow(old)
		if old.Session != nil && *old.Session == sessionID {
			row.Group, row.Session = nil, nil
		}
		if state != "aborted" && sess.Stage == row.Stage {
			row.Failures++
		}
		if _, err := w.saveBead(old, row, "yard"); err != nil {
			return err
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
				sess.Epic = epicOf(tx, sess.Bead)
				sess.Bead = h.of(sess.Bead)
				out = append(out, *sess)
			}
			return nil
		})
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, err
}

// Sessions lists live sessions, or every session when all is set. The
// live ones come from their index, so the dispatcher's pass does not read
// every session the store keeps.
func (s *Store) Sessions(ctx context.Context, all bool) ([]Session, error) {
	if all {
		return s.sessionsWhere(func(_ *bolt.Tx, sess *Session) bool { return true })
	}
	out := []Session{}
	err := s.read(func(tx *bolt.Tx) error {
		live, err := s.liveSessions(tx, "")
		if err != nil {
			return err
		}
		h := handlesIn(tx)
		for _, sess := range live {
			sess.Epic = epicOf(tx, sess.Bead)
			sess.Bead = h.of(sess.Bead)
			out = append(out, sess)
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, err
}

// Unstopped lists ended sessions whose runner resources are still up.
func (s *Store) Unstopped(ctx context.Context) ([]Session, error) {
	return s.sessionsWhere(func(_ *bolt.Tx, sess *Session) bool { return !sess.live() && !sess.Stopped })
}

// ClosedWithWorkspace lists stopped sessions of closed beads whose runner
// left something on disk that has not been cleaned up: a worktree
// (handle.worktree), or a brief in a dir rig's directory (handle.dir).
func (s *Store) ClosedWithWorkspace(ctx context.Context) ([]Session, error) {
	return s.sessionsWhere(func(tx *bolt.Tx, sess *Session) bool {
		if sess.Cleaned || !sess.Stopped || (sess.Handle["worktree"] == nil && sess.Handle["dir"] == nil) {
			return false
		}
		var b beadRow
		ok, _ := get(tx, bBeads, sess.Bead, &b)
		return ok && b.Status == "closed"
	})
}

// FailureCount is how many sessions have died or failed for the bead in
// stage since it entered the stage or a human last un-held it, so
// un-holding grants fresh attempts. The count lives on the bead (see
// beadRow.Failures): it does not depend on the unheld event or the ended
// sessions, which the log and the session table prune. It is 0 for any
// stage but the bead's current one.
func (s *Store) FailureCount(ctx context.Context, ref, stage string) (int, error) {
	n := 0
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, ref)
		if err != nil {
			return err
		}
		if row.Stage == stage {
			n = row.Failures
		}
		return nil
	})
	return n, err
}

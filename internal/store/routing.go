package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"sort"
	"strconv"
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
	// Subscriptions deliver events to each member that is a crew member. Kept
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
	Depot    *string `json:"depot,omitempty"`
	Label    *string `json:"label,omitempty"`
	Group    string  `json:"group"`
	Priority int     `json:"priority"`
}

func (s *Store) AddRoute(ctx context.Context, r Route) (*Route, error) {
	if r.Type != nil {
		typ := BeadType(*r.Type)
		r.Type = &typ
	}
	err := s.write(func(w *wtx) error {
		if w.Bucket(bGroups).Get([]byte(r.Group)) == nil {
			return fmt.Errorf("group %s: %w", r.Group, ErrNotFound)
		}
		if r.Depot != nil && w.Bucket(bDepots).Get([]byte(*r.Depot)) == nil {
			return fmt.Errorf("depot %s: %w", *r.Depot, ErrNotFound)
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
		for _, p := range []*string{r.Type, r.Depot, r.Label} {
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
		if r.Depot != nil && (b.Depot == nil || *r.Depot != *b.Depot) {
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
	// Train is the handle of the train the session's bead is a child of, set
	// on listed sessions (see Bead.Train); never stored.
	Train string `json:"train,omitempty"`
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

// startKey is the session's entry in the index by start: the microsecond
// it started at, big-endian so the keys are in that order, then its id.
// beadKey is its entry in the index by bead: the same under its bead, so a
// bead's sessions are one prefix, in the order they started.
func (s Session) startKey() []byte {
	return append(startStamp(s.StartedAt), s.ID...)
}

func (s Session) beadKey() []byte {
	return append([]byte(s.Bead+"\x00"), s.startKey()...)
}

// startStamp is t as the two indexes key it; stampLen is its length.
func startStamp(t time.Time) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(t.UnixMicro()))
}

const stampLen = 8

// paneHandle names the two places a handle records a pane's process: the
// pid and the key of its start time.
var paneHandle = [][2]string{{"pid", "start"}, {"shell_pid", "shell_start"}}

// paneKeys are the session's entries in the pane index, one for each pid
// its handle names.
func (s Session) paneKeys() []string {
	var out []string
	for _, k := range paneHandle {
		if p, ok := pidIn(s.Handle[k[0]]); ok {
			if key := join(strconv.Itoa(p), s.ID); !slices.Contains(out, key) {
				out = append(out, key)
			}
		}
	}
	return out
}

// unstopped reports an ended session whose runner resources are still up,
// and workspaceLeft a stopped one whose runner left something on disk that
// has not been cleaned up: a worktree (handle.worktree), or a brief in a
// dir depot's directory (handle.dir).
func (s Session) unstopped() bool { return !s.live() && !s.Stopped }

func (s Session) workspaceLeft() bool {
	return s.Stopped && !s.Cleaned && (s.Handle["worktree"] != nil || s.Handle["dir"] != nil)
}

// pending reports a session the daemon is not done with: one Unstopped or
// ClosedWithWorkspace may list. It is the two listings' own conditions and
// nothing more, so the index answers as a walk of every session would in
// any state a session row can be in.
func (s Session) pending() bool { return s.unstopped() || s.workspaceLeft() }

// putSession writes sess and keeps it in, or out of, the live, pane and
// pending indexes, and in those by bead and by start. Every write of a session by a running store goes
// through here, so the indexes are exact; the move to UUIDs, which writes
// the sessions without it, fills them itself.
func putSession(tx *bolt.Tx, sess *Session) error {
	// The pane index is keyed by what the handle names, and a write may
	// replace the handle: the entries of the row as it was go with it.
	var old Session
	had, err := get(tx, bSessions, sess.ID, &old)
	if err != nil {
		return err
	}
	if err := put(tx, bSessions, sess.ID, sess); err != nil {
		return err
	}
	if sess.live() {
		err = tx.Bucket(bSessLive).Put(sess.liveKey(), []byte{})
	} else {
		err = tx.Bucket(bSessLive).Delete(sess.liveKey())
	}
	if err != nil {
		return err
	}
	// Most writes change neither the pids nor whether the session is
	// pending: those indexes are written only where they differ.
	was, is := old.paneKeys(), sess.paneKeys()
	for _, k := range was {
		if !slices.Contains(is, k) {
			if err := tx.Bucket(bSessPane).Delete([]byte(k)); err != nil {
				return err
			}
		}
	}
	for _, k := range is {
		if !slices.Contains(was, k) {
			if err := tx.Bucket(bSessPane).Put([]byte(k), []byte{}); err != nil {
				return err
			}
		}
	}
	switch pend := tx.Bucket(bSessPend); {
	case sess.pending() && !(had && old.pending()):
		err = pend.Put([]byte(sess.ID), []byte{})
	case !sess.pending() && had && old.pending():
		err = pend.Delete([]byte(sess.ID))
	}
	if err != nil {
		return err
	}
	// A session keeps its bead and its start, so these two are written
	// with its first write; one that did change leaves no entry behind.
	if had && bytes.Equal(old.beadKey(), sess.beadKey()) {
		return nil
	}
	if had {
		if err := tx.Bucket(bSessBead).Delete(old.beadKey()); err != nil {
			return err
		}
		if err := tx.Bucket(bSessTime).Delete(old.startKey()); err != nil {
			return err
		}
	}
	if err := tx.Bucket(bSessBead).Put(sess.beadKey(), []byte{}); err != nil {
		return err
	}
	return tx.Bucket(bSessTime).Put(sess.startKey(), []byte{})
}

// indexSessionOrder builds the indexes by bead and by start, once, for a
// store from before them.
func indexSessionOrder(tx *bolt.Tx) error {
	if tx.Bucket(bSessions) == nil || tx.Bucket(bSessBead) != nil && tx.Bucket(bSessTime) != nil {
		return nil
	}
	for _, b := range [][]byte{bSessBead, bSessTime} {
		if tx.Bucket(b) != nil {
			if err := tx.DeleteBucket(b); err != nil {
				return err
			}
		}
		if _, err := tx.CreateBucket(b); err != nil {
			return err
		}
	}
	return fillSessionOrder(tx)
}

// fillSessionOrder puts every session in the indexes by bead and by start.
func fillSessionOrder(tx *bolt.Tx) error {
	return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
		if err := tx.Bucket(bSessBead).Put(sess.beadKey(), []byte{}); err != nil {
			return err
		}
		return tx.Bucket(bSessTime).Put(sess.startKey(), []byte{})
	})
}

// indexPanesAndPending builds the pane and pending indexes, once, for a
// store from before them.
func indexPanesAndPending(tx *bolt.Tx) error {
	if tx.Bucket(bSessions) == nil || tx.Bucket(bSessPane) != nil && tx.Bucket(bSessPend) != nil {
		return nil
	}
	for _, b := range [][]byte{bSessPane, bSessPend} {
		if tx.Bucket(b) != nil {
			if err := tx.DeleteBucket(b); err != nil {
				return err
			}
		}
		if _, err := tx.CreateBucket(b); err != nil {
			return err
		}
	}
	return fillPanesAndPending(tx)
}

// fillPanesAndPending puts every session in the pane index under the pids
// its handle names, and the pending ones in the pending index.
func fillPanesAndPending(tx *bolt.Tx) error {
	return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
		for _, k := range sess.paneKeys() {
			if err := tx.Bucket(bSessPane).Put([]byte(k), []byte{}); err != nil {
				return err
			}
		}
		if !sess.pending() {
			return nil
		}
		return tx.Bucket(bSessPend).Put([]byte(sess.ID), []byte{})
	})
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
	return fillLiveSessions(tx)
}

// fillLiveSessions puts every session that is starting or running in the
// live index.
func fillLiveSessions(tx *bolt.Tx) error {
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
// session as stored, naming its bead by UUID. With pending, match keeps
// only sessions the daemon is not done with, and only those are read,
// through their index, which has the keys of the sessions bucket and so
// its order: the store keeps every session until it is pruned, and the
// dispatcher's pass should not scale with that.
func (s *Store) sessionsWhere(pending bool, match func(*bolt.Tx, *Session) bool) ([]Session, error) {
	out := []Session{}
	err := s.read(func(tx *bolt.Tx) error {
		h := handlesIn(tx)
		see := func(_ []byte, sess *Session) error {
			if match(tx, sess) {
				sess.Train = trainOf(tx, sess.Bead)
				sess.Bead = h.of(sess.Bead)
				out = append(out, *sess)
			}
			return nil
		}
		if !pending {
			return each(tx, bSessions, "", see)
		}
		ids := keys(tx, bSessPend, "")
		s.pendingReads.Add(int64(len(ids)))
		for _, id := range ids {
			sess := new(Session)
			if ok, err := get(tx, bSessions, id, sess); err != nil {
				return err
			} else if !ok {
				continue
			}
			if err := see(nil, sess); err != nil {
				return err
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, err
}

// Sessions lists live sessions, or every session when all is set. The
// live ones come from their index, so the dispatcher's pass does not read
// every session the store keeps.
func (s *Store) Sessions(ctx context.Context, all bool) ([]Session, error) {
	if all {
		return s.sessionsWhere(false, func(_ *bolt.Tx, sess *Session) bool { return true })
	}
	out := []Session{}
	err := s.read(func(tx *bolt.Tx) error {
		live, err := s.liveSessions(tx, "")
		if err != nil {
			return err
		}
		h := handlesIn(tx)
		for _, sess := range live {
			sess.Train = trainOf(tx, sess.Bead)
			sess.Bead = h.of(sess.Bead)
			out = append(out, sess)
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, err
}

// SessionFilter says which sessions ListSessions lists.
type SessionFilter struct {
	All bool // include ended
	// Bead keeps the sessions of this bead (a handle or UUID).
	Bead string
	// Running leaves out a manual group's claims, which nothing runs in.
	Running bool
	// Since keeps the sessions that started at it or later.
	Since time.Time
	// Limit keeps the newest that many of what the rest kept; 0 is all.
	Limit int
}

// ListSessions lists the sessions f keeps, by start time. Without All they
// are live ones, read through their index as Sessions reads them. With it
// they are read newest first through the index by start, or through the
// index by bead for one bead's, and the read ends at Limit or at Since: the
// store keeps every session until it is pruned, and neither a bead's
// sessions nor the newest few should cost what all of them do.
func (s *Store) ListSessions(ctx context.Context, f SessionFilter) ([]Session, error) {
	out := []Session{}
	err := s.read(func(tx *bolt.Tx) error {
		bead := ""
		if f.Bead != "" {
			u, err := resolve(tx, f.Bead)
			if err != nil {
				return err
			}
			bead = u
		}
		h := handlesIn(tx)
		keep := func(sess *Session) {
			sess.Train = trainOf(tx, sess.Bead)
			sess.Bead = h.of(sess.Bead)
			out = append(out, *sess)
		}
		if !f.All {
			live, err := s.liveSessions(tx, "")
			if err != nil {
				return err
			}
			sort.SliceStable(live, func(i, j int) bool { return live[i].StartedAt.After(live[j].StartedAt) })
			for _, sess := range live {
				if f.Limit > 0 && len(out) == f.Limit {
					break
				}
				if (bead == "" || sess.Bead == bead) && !(f.Running && sess.Manual()) && !sess.StartedAt.Before(f.Since) {
					keep(&sess)
				}
			}
			return nil
		}
		bucket, prefix := bSessTime, []byte{}
		if bead != "" {
			bucket, prefix = bSessBead, []byte(bead+"\x00")
		}
		var floor []byte
		if !f.Since.IsZero() {
			floor = startStamp(f.Since)
		}
		n := 0
		defer func() { s.orderReads.Add(int64(n)) }()
		c := tx.Bucket(bucket).Cursor()
		// The last key under the prefix is the one before the next
		// prefix's first; ids never contain NUL, so that is <bead>\x01.
		k, _ := c.Last()
		if bead != "" {
			if k, _ = c.Seek([]byte(bead + "\x01")); k == nil {
				k, _ = c.Last()
			} else {
				k, _ = c.Prev()
			}
		}
		for ; k != nil && bytes.HasPrefix(k, prefix) && (f.Limit <= 0 || len(out) < f.Limit); k, _ = c.Prev() {
			at := k[len(prefix):]
			if len(at) < stampLen || floor != nil && bytes.Compare(at[:stampLen], floor) < 0 {
				break
			}
			sess := new(Session)
			if ok, err := get(tx, bSessions, string(at[stampLen:]), sess); err != nil {
				return err
			} else if !ok {
				continue
			}
			n++
			if !(f.Running && sess.Manual()) {
				keep(sess)
			}
		}
		return nil
	})
	slices.Reverse(out)
	return out, err
}

// Unstopped lists ended sessions whose runner resources are still up.
func (s *Store) Unstopped(ctx context.Context) ([]Session, error) {
	return s.sessionsWhere(true, func(_ *bolt.Tx, sess *Session) bool { return sess.unstopped() })
}

// ClosedWithWorkspace lists stopped sessions of closed beads whose runner
// left something on disk that has not been cleaned up: a worktree
// (handle.worktree), or a brief in a dir depot's directory (handle.dir).
func (s *Store) ClosedWithWorkspace(ctx context.Context) ([]Session, error) {
	return s.sessionsWhere(true, func(tx *bolt.Tx, sess *Session) bool {
		if !sess.workspaceLeft() {
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

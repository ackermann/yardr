package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Retention is how long Prune keeps what it may remove; zero keeps it for
// good.
type Retention struct {
	Events       time.Duration `json:"events"`        // the event log, and the sessions that ended
	HookPayloads time.Duration `json:"hook_payloads"` // raw webhook bodies, up to MaxHookPayload each
	// Inbox bounds how long an event a running crew member has not read holds
	// the log back (see Prune); zero is forever.
	Inbox time.Duration `json:"inbox"`
}

// DefaultRetention keeps events for 90 days and webhook bodies, which only
// `yardr hook show` reads, for 30; an unread inbox event holds the log back
// for 180 days at most.
var DefaultRetention = Retention{Events: 90 * 24 * time.Hour, HookPayloads: 30 * 24 * time.Hour, Inbox: 180 * 24 * time.Hour}

// PruneResult is what one Prune removed, and what held the log back.
type PruneResult struct {
	Events       int    `json:"events"`
	HookPayloads int    `json:"hook_payloads"`
	Sessions     int    `json:"sessions"`
	HeldBy       string `json:"held_by,omitempty"` // why events past their age were kept
	// Expired counts, per crew member, the unread inbox events (mail and
	// deliveries) that went with the log; each crew member's journal gets an
	// inbox_expired event saying so.
	Expired map[string]int `json:"expired,omitempty"`
	// Peers counts, per peer, what went of its link (prunePeers).
	Peers map[string]PeerPruned `json:"peers,omitempty"`
	// Blobs counts the blobs no letter named any more (pruneBlobs).
	Blobs int `json:"blobs,omitempty"`
}

// pruneBatch bounds the events one write transaction removes, so a first
// prune of a large log does not hold the write lock for long.
const pruneBatch = 1000

// Prune removes events older than keep.Events, the sessions that ended
// before then, and webhook bodies older than keep.HookPayloads. It never
// removes the newest event (the head that sequences continue from), an
// event in a running crew member's inbox it has yet to read (unless older than
// keep.Inbox) nor any event after that, nor anything since the oldest live
// session or running crew member session began (its prime and history read
// back to there). A stopped or disabled crew member holds nothing back: what
// it has not read in a retention's time goes, and its journal is told how
// much (inbox_expired). An ended session goes once the daemon has stopped
// its runner resources and, if it left a worktree, cleaned up after the
// bead closed; its bead and the bead's notes stay, being state, not log.
// Webhook bodies are not events: the event keeps the envelope that routing
// reads, so a body goes by age alone. A link's seen ids and the messages
// given up for its peer go at the age of events too, though never younger
// than PeerKeep (prunePeers). A blob goes when no letter left in the log
// names it, whatever keep says (pruneBlobs). Logs a store_pruned event when
// anything was removed. Pruning the store is the daemon's or the
// operator's to do (daemonOrOperator): the automatic prune runs in-process,
// and anyone else asking must be the operator.
func (s *Store) Prune(ctx context.Context, keep Retention, actor string) (*PruneResult, error) {
	if err := s.read(func(tx *bolt.Tx) error {
		resolved, err := paneAuthor(tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		return s.daemonOrOperator(ctx, tx, actor, "prune the store", Alone)
	}); err != nil {
		return nil, err
	}
	res := &PruneResult{Expired: map[string]int{}}
	if keep.Events > 0 {
		var unreadBound time.Time // zero: unread events hold back for good
		if keep.Inbox > 0 {
			unreadBound = now().Add(-keep.Inbox)
		}
		floor, cutoff, held, err := s.pruneFloor(now().Add(-keep.Events), unreadBound)
		if err != nil {
			return nil, err
		}
		res.HeldBy = held
		for {
			n, more, err := s.pruneEvents(floor, cutoff, res.Expired)
			res.Events += n
			if err != nil {
				return res, err
			}
			if !more {
				break
			}
		}
		for {
			n, more, err := s.pruneSessions(now().Add(-keep.Events))
			res.Sessions += n
			if err != nil {
				return res, err
			}
			if !more {
				break
			}
		}
		for {
			more, err := s.prunePeers(now().Add(-max(keep.Events, PeerKeep)), res)
			if err != nil {
				return res, err
			}
			if !more {
				break
			}
		}
	}
	if keep.HookPayloads > 0 {
		cutoff := now().Add(-keep.HookPayloads)
		for {
			n, more, err := s.prunePayloads(cutoff)
			res.HookPayloads += n
			if err != nil {
				return res, err
			}
			if !more {
				break
			}
		}
	}
	// After the events: a blob goes once the last letter that named it has.
	var err error
	if res.Blobs, err = s.pruneBlobs(); err != nil {
		return res, err
	}
	if len(res.Expired) == 0 {
		res.Expired = nil
	}
	for name, n := range res.Expired {
		// The crew member learns what it never read, in its own history.
		r, err := s.GetCrew(ctx, name)
		if errors.Is(err, ErrNotFound) {
			continue // deleted since its inbox entries were counted
		} else if err != nil {
			return res, err
		}
		if err := s.LogEvent(ctx, r.Journal, "inbox_expired", actor, map[string]any{"crew": name, "events": n}); err != nil {
			return res, err
		}
	}
	if res.Events+res.HookPayloads+res.Sessions+len(res.Peers)+res.Blobs == 0 {
		return res, nil
	}
	data := map[string]any{"events": res.Events, "hook_payloads": res.HookPayloads, "sessions": res.Sessions}
	if res.Peers != nil {
		// Counts, as every event about a link: never an id or a body.
		data["peers"] = res.Peers
	}
	if res.Blobs > 0 {
		data["blobs"] = res.Blobs
	}
	if res.HeldBy != "" {
		data["held_by"] = res.HeldBy
	}
	if res.Expired != nil {
		data["expired"] = res.Expired
	}
	return res, s.LogEvent(ctx, "", "store_pruned", actor, data)
}

// firstUnread is the oldest event in r's inbox it has not acknowledged and
// that is not older than bound (a zero bound is no bound): what it has
// left unread longer than that no longer holds the log back. Only inbox
// events count: the cursor of a crew member nothing was sent to does not
// move, and must not pin the log. With one cursor per session, it is the
// oldest unread of the live sessions' slices.
func firstUnread(tx *bolt.Tx, r *CrewMember, bound time.Time) (uint64, bool, error) {
	seq, found := uint64(0), false
	for _, sc := range liveSlices(r) {
		for _, k := range inboxKeysFrom(tx, r, sc.cursor) {
			var e Event
			if ok, err := get(tx, bEvents, k, &e); err != nil {
				return 0, false, err
			} else if !ok || e.At.Before(bound) {
				continue
			}
			evs := []Event{e}
			named(tx, evs)
			if forSlice(r, sc.user, evs[0]) {
				if !found || e.Seq < seq {
					seq, found = e.Seq, true
				}
				break // keys are in seq order: the first unread pins
			}
		}
	}
	return seq, found, nil
}

// sliceCursorAt is one session's inbox position: the user ("": the line
// session's) with the cursor its slice reads at.
type sliceCursorAt struct {
	user   string
	cursor uint64
}

// liveSlices are the inbox positions of r's live sessions: the line
// session's where it runs, and each live conversation's.
func liveSlices(r *CrewMember) []sliceCursorAt {
	var out []sliceCursorAt
	if r.CrewSession.Live() {
		out = append(out, sliceCursorAt{cursor: r.Cursor})
	}
	for _, user := range r.ConversationUsers() {
		if cs := r.Conversations[user]; cs.Live() {
			out = append(out, sliceCursorAt{user: user, cursor: cs.Cursor})
		}
	}
	return out
}

// slicesAll are the inbox positions of every session that may hold unread:
// the line session's and each conversation's, live or not.
func slicesAll(r *CrewMember) []sliceCursorAt {
	out := []sliceCursorAt{{cursor: r.Cursor}}
	for _, user := range r.ConversationUsers() {
		cursor := r.Cursor
		if cs, ok := r.Conversations[user]; ok {
			cursor = cs.Cursor
		}
		out = append(out, sliceCursorAt{user: user, cursor: cursor})
	}
	return out
}

// liveSessions are the runtime states of r's live sessions: the line
// session's where it runs, and each live conversation's. A row whose
// conversation the config no longer declares reads as no session: the
// daemon stops it on its next pass.
func liveSessions(r *CrewMember) []CrewSession {
	var out []CrewSession
	if r.CrewSession.Live() {
		out = append(out, r.CrewSession)
	}
	for _, user := range r.ConversationUsers() {
		if cs := r.Conversations[user]; cs.Live() {
			out = append(out, cs)
		}
	}
	return out
}

// pruneFloor works out what Prune must keep: every event from seq floor on,
// and every event at or after cutoff, which moves back to the start of the
// oldest live session. Only a running crew member holds the log back, with
// its unread inbox (within unreadBound) and its session's start: one that
// is stopped or disabled would otherwise pin the log for as long as it
// stays so. held says what, if anything, kept events older than the
// asked-for cutoff. Cursors only move forward and new sessions start after
// now, so the floor stays valid while Prune runs; a crew member that starts
// between here and the batch is the one exception, and loses at most what
// it left unread for a whole retention.
func (s *Store) pruneFloor(cutoff, unreadBound time.Time) (floor uint64, _ time.Time, held string, err error) {
	err = s.read(func(tx *bolt.Tx) error {
		k, _ := tx.Bucket(bEvents).Cursor().Last()
		if k == nil {
			return nil
		}
		floor = binary.BigEndian.Uint64(k) // the head stays
		if err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
			if len(liveSlices(r)) == 0 {
				return nil
			}
			if seq, ok, err := firstUnread(tx, r, unreadBound); err != nil {
				return err
			} else if ok && seq < floor {
				floor, held = seq, "crew member "+r.Name+" has not read event "+fmt.Sprint(seq)
			}
			for _, st := range liveSessions(r) {
				if st.StartedAt != nil && st.StartedAt.Before(cutoff) {
					cutoff, held = *st.StartedAt, "crew member "+r.Name+" has run since "+st.StartedAt.Format(time.RFC3339)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		live, err := s.liveSessions(tx, "")
		if err != nil {
			return err
		}
		for _, sess := range live {
			// A session holds the log for its runner's prime and history,
			// which read back to its start. A manual group's claim starts no
			// agent and reads nothing, so a bead waiting for a person holds
			// nothing, however long it waits.
			if sess.Manual() {
				continue
			}
			if sess.StartedAt.Before(cutoff) {
				cutoff, held = sess.StartedAt, "session "+sess.ID+" has run since "+sess.StartedAt.Format(time.RFC3339)
			}
		}
		return nil
	})
	return floor, cutoff, held, err
}

// pruneEvents removes up to pruneBatch of the oldest events that are both
// before floor and older than cutoff, with their index entries and webhook
// bodies; more reports that a full batch went. An inbox entry a crew member
// had not read yet is counted against it in expired.
func (s *Store) pruneEvents(floor uint64, cutoff time.Time, expired map[string]int) (n int, more bool, err error) {
	var counted map[string]int
	err = s.write(func(w *wtx) error {
		n, more, counted = 0, false, map[string]int{} // a batched write may run this twice
		members := map[string]*CrewMember{}
		if err := each(w.Tx, bCrew, "", func(_ []byte, r *CrewMember) error {
			members[r.Name] = r
			return nil
		}); err != nil {
			return err
		}
		var dead []Event
		c := w.Bucket(bEvents).Cursor()
		for k, raw := c.First(); k != nil && binary.BigEndian.Uint64(k) < floor; k, raw = c.Next() {
			var e Event
			if err := json.Unmarshal(raw, &e); err != nil {
				return err
			}
			// The log is in time order, so the first young event ends it.
			if !e.At.Before(cutoff) {
				break
			}
			if len(dead) == pruneBatch {
				more = true
				break
			}
			dead = append(dead, e)
		}
		for _, e := range dead {
			k := seqKey(e.Seq)
			type entry struct {
				b   []byte
				key string
			}
			del := []entry{{bEvents, k}, {bEventsKd, join(e.Kind, k)}, {bHookPayloads, k}}
			if e.Bead != nil {
				del = append(del, entry{bEventsBy, join(*e.Bead, k)})
			}
			for _, name := range inboxOf(e) {
				del = append(del, entry{bInbox, join(name, k)})
				// Unread in the slice it belongs to: an event is in one
				// slice, so it counts once.
				if r, ok := members[name]; ok {
					evs := []Event{e}
					named(w.Tx, evs) // forSlice compares handles
					for _, sc := range slicesAll(r) {
						if forSlice(r, sc.user, evs[0]) {
							if e.Seq > sc.cursor {
								counted[name]++
							}
							break
						}
					}
				}
			}
			for _, d := range del {
				if err := w.Bucket(d.b).Delete([]byte(d.key)); err != nil {
					return err
				}
			}
		}
		n = len(dead)
		if n == 0 {
			return nil
		}
		// A redelivery whose first delivery is gone from the log is new
		// again: drop the dedupe keys that point at removed events.
		oldest := dead[n-1].Seq + 1
		var stale [][]byte
		seen := w.Bucket(bHookSeen)
		if err := seen.ForEach(func(k, v []byte) error {
			if len(v) == 8 && binary.BigEndian.Uint64(v) < oldest {
				stale = append(stale, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range stale {
			if err := seen.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		for name, c := range counted {
			expired[name] += c
		}
	}
	return n, more, err
}

// pruneSessions removes up to pruneBatch of the sessions that ended before
// cutoff and that the daemon is done with: their runner resources stopped
// and, when they left a worktree, cleaned up (which waits for the bead to
// close). A live session is never removed, whatever its age.
func (s *Store) pruneSessions(cutoff time.Time) (n int, more bool, err error) {
	err = s.write(func(w *wtx) error {
		n, more = 0, false
		var dead []Session
		if err := each(w.Tx, bSessions, "", func(_ []byte, sess *Session) error {
			if sess.live() || !sess.Stopped || (sess.Handle["worktree"] != nil && !sess.Cleaned) {
				return nil
			}
			ended := sess.StartedAt
			if sess.EndedAt != nil {
				ended = *sess.EndedAt
			}
			if !ended.Before(cutoff) {
				return nil
			}
			if len(dead) == pruneBatch {
				more = true
				return errStop
			}
			dead = append(dead, *sess)
			return nil
		}); err != nil && !errors.Is(err, errStop) {
			return err
		}
		for _, sess := range dead {
			if err := w.Bucket(bSessions).Delete([]byte(sess.ID)); err != nil {
				return err
			}
			if err := w.Bucket(bSessLive).Delete(sess.liveKey()); err != nil {
				return err
			}
			// Stopped with a brief left in a dir depot is pruned while
			// pending, and any session may still name its pane.
			if err := w.Bucket(bSessPend).Delete([]byte(sess.ID)); err != nil {
				return err
			}
			for _, k := range sess.paneKeys() {
				if err := w.Bucket(bSessPane).Delete([]byte(k)); err != nil {
					return err
				}
			}
			if err := w.Bucket(bSessBead).Delete(sess.beadKey()); err != nil {
				return err
			}
			if err := w.Bucket(bSessTime).Delete(sess.startKey()); err != nil {
				return err
			}
		}
		n = len(dead)
		return nil
	})
	return n, more, err
}

// errStop ends an each early, without error.
var errStop = errors.New("stop")

// prunePayloads removes up to pruneBatch of the oldest webhook bodies whose
// event is older than cutoff (or already pruned).
func (s *Store) prunePayloads(cutoff time.Time) (n int, more bool, err error) {
	err = s.write(func(w *wtx) error {
		n, more = 0, false
		var dead [][]byte
		c := w.Bucket(bHookPayloads).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			var e Event
			if ok, err := get(w.Tx, bEvents, string(k), &e); err != nil {
				return err
			} else if ok && !e.At.Before(cutoff) {
				break
			}
			if len(dead) == pruneBatch {
				more = true
				break
			}
			dead = append(dead, append([]byte(nil), k...))
		}
		for _, k := range dead {
			if err := w.Bucket(bHookPayloads).Delete(k); err != nil {
				return err
			}
		}
		n = len(dead)
		return nil
	})
	return n, more, err
}

// CompactResult is the store file's size before and after Compact.
type CompactResult struct {
	Before int64 `json:"before"`
	After  int64 `json:"after"`
}

// Compact rewrites the store file at path without its free pages: bbolt
// reuses the space Prune frees but never gives it back to the filesystem.
// It needs the file to itself, so it fails with ErrLocked while a server
// holds it; the copy replaces the file only once it is complete.
func Compact(path string) (*CompactResult, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	src, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second, ReadOnly: true})
	if errors.Is(err, bolt.ErrTimeout) {
		return nil, fmt.Errorf("%s: %w", path, ErrLocked)
	} else if err != nil {
		return nil, err
	}
	defer src.Close()
	if err := checkSchema(src); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	tmp := path + ".compact"
	_ = os.Remove(tmp) // left by a compaction that did not finish
	dst, err := bolt.Open(tmp, st.Mode().Perm(), &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if err := bolt.Compact(dst, src, 64<<20); err != nil {
		dst.Close()
		os.Remove(tmp)
		return nil, err
	}
	if err := dst.Close(); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &CompactResult{Before: st.Size(), After: after.Size()}, nil
}

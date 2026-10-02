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
	Events       time.Duration `json:"events"`        // the event log
	HookPayloads time.Duration `json:"hook_payloads"` // raw webhook bodies, up to MaxHookPayload each
}

// DefaultRetention keeps events for 90 days and webhook bodies, which only
// `yardr hook show` reads, for 30.
var DefaultRetention = Retention{Events: 90 * 24 * time.Hour, HookPayloads: 30 * 24 * time.Hour}

// PruneResult is what one Prune removed, and what held the log back.
type PruneResult struct {
	Events       int    `json:"events"`
	HookPayloads int    `json:"hook_payloads"`
	HeldBy       string `json:"held_by,omitempty"` // why events past their age were kept
}

// pruneBatch bounds the events one write transaction removes, so a first
// prune of a large log does not hold the write lock for long.
const pruneBatch = 1000

// Prune removes events older than keep.Events and webhook bodies older
// than keep.HookPayloads. It never removes the newest event (the head that
// sequences continue from), an event in a resident's inbox it has yet to
// read, nor any event after that, nor anything since the oldest live session or
// resident session began (its prime and history read back to there).
// Webhook bodies are not events: the event keeps the envelope that routing
// reads, so a body goes by age alone. Logs a store_pruned event when
// anything was removed.
func (s *Store) Prune(ctx context.Context, keep Retention, actor string) (*PruneResult, error) {
	res := &PruneResult{}
	if keep.Events > 0 {
		floor, cutoff, held, err := s.pruneFloor(now().Add(-keep.Events))
		if err != nil {
			return nil, err
		}
		res.HeldBy = held
		for {
			n, more, err := s.pruneEvents(floor, cutoff)
			res.Events += n
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
	if res.Events+res.HookPayloads == 0 {
		return res, nil
	}
	data := map[string]any{"events": res.Events, "hook_payloads": res.HookPayloads}
	if res.HeldBy != "" {
		data["held_by"] = res.HeldBy
	}
	return res, s.LogEvent(ctx, "", "store_pruned", actor, data)
}

// firstUnread is the oldest event in r's inbox it has not acknowledged.
// Only inbox events count: the cursor of a resident nothing was sent to
// does not move, and must not pin the log.
func firstUnread(tx *bolt.Tx, r *Resident) (uint64, bool, error) {
	for _, k := range inboxKeys(tx, r) {
		var e Event
		if ok, err := get(tx, bEvents, k, &e); err != nil {
			return 0, false, err
		} else if !ok {
			continue
		}
		evs := []Event{e}
		named(tx, evs)
		if forResident(r, evs[0]) {
			return e.Seq, true, nil
		}
	}
	return 0, false, nil
}

// pruneFloor works out what Prune must keep: every event from seq floor on,
// and every event at or after cutoff, which moves back to the start of the
// oldest live session. held says what, if anything, kept events older than
// the asked-for cutoff. Cursors only move forward and new sessions start
// after now, so the floor stays valid while Prune runs.
func (s *Store) pruneFloor(cutoff time.Time) (floor uint64, _ time.Time, held string, err error) {
	err = s.read(func(tx *bolt.Tx) error {
		k, _ := tx.Bucket(bEvents).Cursor().Last()
		if k == nil {
			return nil
		}
		floor = binary.BigEndian.Uint64(k) // the head stays
		if err := each(tx, bResidents, "", func(_ []byte, r *Resident) error {
			if seq, ok, err := firstUnread(tx, r); err != nil {
				return err
			} else if ok && seq < floor {
				floor, held = seq, "resident "+r.Name+" has not read event "+fmt.Sprint(seq)
			}
			if r.Live() && r.StartedAt != nil && r.StartedAt.Before(cutoff) {
				cutoff, held = *r.StartedAt, "resident "+r.Name+" has run since "+r.StartedAt.Format(time.RFC3339)
			}
			return nil
		}); err != nil {
			return err
		}
		return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
			if sess.live() && sess.StartedAt.Before(cutoff) {
				cutoff, held = sess.StartedAt, "session "+sess.ID+" has run since "+sess.StartedAt.Format(time.RFC3339)
			}
			return nil
		})
	})
	return floor, cutoff, held, err
}

// pruneEvents removes up to pruneBatch of the oldest events that are both
// before floor and older than cutoff, with their index entries and webhook
// bodies; more reports that a full batch went.
func (s *Store) pruneEvents(floor uint64, cutoff time.Time) (n int, more bool, err error) {
	err = s.write(func(w *wtx) error {
		n, more = 0, false // a batched write may run this twice
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
	return n, more, err
}

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

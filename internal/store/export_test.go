package store

import (
	"encoding/binary"
	"maps"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// MentionCompiles is how many @mention patterns the store has compiled.
func (s *Store) MentionCompiles() int64 { return s.mentions.compiled.Load() }

// InboxReads is how many events Inbox has decoded.
func (s *Store) InboxReads() int64 { return s.inboxReads.Load() }

// SessionReads is how many sessions were read through the live index.
func (s *Store) SessionReads() int64 { return s.sessionReads.Load() }

// BulkSessions stores n ended, stopped sessions of group for bead (a UUID)
// in state that ended at ended, as a long-running yard leaves behind.
func (s *Store) BulkSessions(n int, bead, group, state string, ended time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for i := 0; i < n; i++ {
			sess := Session{ID: newEdgeID(), Bead: bead, Group: group, Stage: "new", State: state,
				Stopped: true, StartedAt: ended.Add(-time.Minute), EndedAt: &ended}
			if err := putSession(tx, &sess); err != nil {
				return err
			}
		}
		return nil
	})
}

// DropSessionIndex removes the live session index, as a store from before
// it lacks it.
func (s *Store) DropSessionIndex() error {
	return s.db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket(bSessLive) })
}

// LiveIndex lists the session ids in the live index.
func (s *Store) LiveIndex() (ids []string) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		for _, k := range keys(tx, bSessLive, "") {
			ids = append(ids, k[strings.LastIndexByte(k, 0)+1:])
		}
		return nil
	})
	return ids
}

// BeadReads is how many beads the listings have decoded.
func (s *Store) BeadReads() int64 { return s.beadReads.Load() }

// DropOpenIndex removes the index of open beads, as a store from before it
// lacks it.
func (s *Store) DropOpenIndex() error {
	return s.db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket(bBeadOpen) })
}

// OpenIndex lists the UUIDs in the index of open beads, and OpenWalk those
// of the beads a walk of every bead finds open.
func (s *Store) OpenIndex() (ids []string) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		ids = keys(tx, bBeadOpen, "")
		return nil
	})
	return ids
}

func (s *Store) OpenWalk() (ids []string) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		return each(tx, bBeads, "", func(k []byte, r *beadRow) error {
			if r.Status == "open" {
				ids = append(ids, string(k))
			}
			return nil
		})
	})
	return ids
}

// BulkEvents logs n events of kind in a few large transactions, for tests
// that need a long log quickly.
func (s *Store) BulkEvents(n int, kind string, data map[string]any) error {
	for n > 0 {
		batch := min(n, 10000)
		err := s.db.Update(func(tx *bolt.Tx) error {
			w := &wtx{Tx: tx}
			for i := 0; i < batch; i++ {
				if err := w.event("", kind, "test", maps.Clone(data)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		n -= batch
	}
	return nil
}

// Backdate moves the time of events first..last (inclusive) to at.
func (s *Store) Backdate(first, last uint64, at time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for seq := first; seq <= last; seq++ {
			var e Event
			if ok, err := get(tx, bEvents, seqKey(seq), &e); err != nil {
				return err
			} else if !ok {
				continue
			}
			e.At = at
			if err := put(tx, bEvents, seqKey(seq), e); err != nil {
				return err
			}
		}
		return nil
	})
}

// BackdateSession moves the start of session id to at, and its end, if it
// has ended, to a minute later.
func (s *Store) BackdateSession(id string, at time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var sess Session
		if _, err := get(tx, bSessions, id, &sess); err != nil {
			return err
		}
		sess.StartedAt = at
		if sess.EndedAt != nil {
			ended := at.Add(time.Minute)
			sess.EndedAt = &ended
		}
		return putSession(tx, &sess)
	})
}

// IndexEntries counts the inbox and kind index entries for seq.
func (s *Store) IndexEntries(seq uint64) (n int) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bInbox, bEventsKd, bEventsBy} {
			_ = tx.Bucket(b).ForEach(func(k, _ []byte) error {
				if len(k) >= 8 && string(k[len(k)-8:]) == seqKey(seq) {
					n++
				}
				return nil
			})
		}
		return nil
	})
	return n
}

// DropIndexes makes the store what a build before the indexes wrote: no
// inbox or kind index, and stamped with schema 2.
func (s *Store) DropIndexes() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bInbox, bEventsKd} {
			if err := tx.DeleteBucket(b); err != nil {
				return err
			}
		}
		return tx.Bucket(bMeta).Put(kSchema, binary.BigEndian.AppendUint64(nil, 2))
	})
}

// HasBucket reports whether the store file has a top-level bucket name.
func (s *Store) HasBucket(name string) (ok bool) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		ok = tx.Bucket([]byte(name)) != nil
		return nil
	})
	return ok
}

// BulkNotify notifies user n times in one transaction, each as of a seq
// of its own taken from the log's, for tests that need a full inbox
// quickly. It returns the seqs.
func (s *Store) BulkNotify(user string, n int) (seqs []uint64, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		w := &wtx{Tx: tx}
		for range n {
			seq, err := tx.Bucket(bEvents).NextSequence()
			if err != nil {
				return err
			}
			note := Notification{Seq: seq, At: now(), Source: SourceYardr, Kind: "noted", Text: "bulk (" + reasonSlot + ")"}
			if err := w.notify(note, map[string]any{user: ReasonAll}); err != nil {
				return err
			}
			seqs = append(seqs, seq)
		}
		return nil
	})
	return seqs, err
}

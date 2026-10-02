package store

import (
	"maps"
	"time"

	bolt "go.etcd.io/bbolt"
)

// MentionCompiles is how many @mention patterns the store has compiled.
func (s *Store) MentionCompiles() int64 { return s.mentions.compiled.Load() }

// InboxReads is how many events Inbox has decoded.
func (s *Store) InboxReads() int64 { return s.inboxReads.Load() }

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

// BackdateSession moves the start of session id to at.
func (s *Store) BackdateSession(id string, at time.Time) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var sess Session
		if _, err := get(tx, bSessions, id, &sess); err != nil {
			return err
		}
		sess.StartedAt = at
		return put(tx, bSessions, id, sess)
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

// DropIndexes removes the inbox and kind indexes, as a store from before
// them lacks them.
func (s *Store) DropIndexes() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bInbox, bEventsKd} {
			if err := tx.DeleteBucket(b); err != nil {
				return err
			}
		}
		return nil
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

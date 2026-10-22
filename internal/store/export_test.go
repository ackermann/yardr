package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/peer"
)

// BeadReads is how many beads listings and Stranded decoded. Tests use it
// to check they do not walk every bead for every question.
func (s *Store) BeadReads() int64 { return s.beadReads.Load() }

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

// PutSession writes sess as it is, through the indexes and with no check
// that its bead is there, so a test can have a session the store never made
// itself, one whose bead is gone.
func (s *Store) PutSession(sess Session) error {
	return s.db.Update(func(tx *bolt.Tx) error { return putSession(tx, &sess) })
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

// OrderReads is how many sessions were read through the indexes by bead
// and by start.
func (s *Store) OrderReads() int64 { return s.orderReads.Load() }

// DropSessionOrderIndexes removes the indexes by bead and by start, as a
// store from before them lacks them.
func (s *Store) DropSessionOrderIndexes() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bSessBead); err != nil {
			return err
		}
		return tx.DeleteBucket(bSessTime)
	})
}

// SessionOrderIndex lists the keys of the indexes by bead and by start,
// and SessionOrderIndexWalk the keys a walk of every session would put
// there.
func (s *Store) SessionOrderIndex() (byBead, byStart []string) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		byBead, byStart = keys(tx, bSessBead, ""), keys(tx, bSessTime, "")
		return nil
	})
	return byBead, byStart
}

func (s *Store) SessionOrderIndexWalk() (byBead, byStart []string) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
			byBead, byStart = append(byBead, string(sess.beadKey())), append(byStart, string(sess.startKey()))
			return nil
		})
	})
	sort.Strings(byBead)
	sort.Strings(byStart)
	return byBead, byStart
}

// PendingReads is how many sessions were read through the pending index.
func (s *Store) PendingReads() int64 { return s.pendingReads.Load() }

// DropPaneAndPendingIndexes removes the pane and the pending index, as a
// store from before them lacks them.
func (s *Store) DropPaneAndPendingIndexes() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bSessPane); err != nil {
			return err
		}
		return tx.DeleteBucket(bSessPend)
	})
}

// PaneIndex and PendingIndex list the keys of the two indexes, and
// PaneIndexWalk and PendingIndexWalk the keys a walk of every session
// would put there.
func (s *Store) PaneIndex() (ks []string) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		ks = keys(tx, bSessPane, "")
		return nil
	})
	return ks
}

func (s *Store) PendingIndex() (ks []string) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		ks = keys(tx, bSessPend, "")
		return nil
	})
	return ks
}

func (s *Store) PaneIndexWalk() (ks []string) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
			ks = append(ks, sess.paneKeys()...)
			return nil
		})
	})
	sort.Strings(ks)
	return ks
}

func (s *Store) PendingIndexWalk() (ks []string) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		return each(tx, bSessions, "", func(k []byte, sess *Session) error {
			if !sess.live() && !sess.Stopped ||
				sess.Stopped && !sess.Cleaned && (sess.Handle["worktree"] != nil || sess.Handle["dir"] != nil) ||
				sess.Stopped && sess.Handle["count_due"] != nil {
				ks = append(ks, string(k))
			}
			return nil
		})
	})
	return ks
}

// Pane is what the store takes for the pane of a process: the session and
// the start time its handle recorded, if it did.
type Pane struct {
	Session string
	Start   uint64
	Known   bool
}

// PaneOf is the pane of the process pid by the pane index, and PaneSession
// the session a call from pid is checked as.
func (s *Store) PaneOf(pid int) (out *Pane, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		pn, err := paneOf(tx, pid)
		if pn != nil {
			out = &Pane{pn.sess.ID, pn.start, pn.known}
		}
		return err
	})
	return out, err
}

func (s *Store) PaneSession(pid int) (sess *Session, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		sess, err = paneSession(tx, pid)
		return err
	})
	return sess, err
}

// panesWalk is every session's pane by pid, as paneSession built it before
// the pane index: a walk of every session the store keeps.
func panesWalk(tx *bolt.Tx) (map[int]pane, error) {
	panes := map[int]pane{}
	err := each(tx, bSessions, "", func(_ []byte, sess *Session) error {
		for _, k := range [][2]string{{"pid", "start"}, {"shell_pid", "shell_start"}} {
			p, ok := pidIn(sess.Handle[k[0]])
			if !ok {
				continue
			}
			if prev, dup := panes[p]; dup && (prev.sess.live() || !sess.live() && prev.sess.StartedAt.After(sess.StartedAt)) {
				continue
			}
			start, known := uintIn(sess.Handle[k[1]])
			panes[p] = pane{sess: sess, start: start, known: known}
		}
		return nil
	})
	return panes, err
}

// PaneOfWalk and PaneSessionWalk answer as PaneOf and PaneSession, by that
// walk.
func (s *Store) PaneOfWalk(pid int) (out *Pane, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		panes, err := panesWalk(tx)
		if pn, ok := panes[pid]; ok {
			out = &Pane{pn.sess.ID, pn.start, pn.known}
		}
		return err
	})
	return out, err
}

func (s *Store) PaneSessionWalk(pid int) (sess *Session, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		if pid <= 0 {
			return nil
		}
		panes, err := panesWalk(tx)
		if err != nil || len(panes) == 0 {
			return err
		}
		for _, p := range peer.Ancestors(pid) {
			pn, ok := panes[p]
			if ok && (pn.known && peer.Alive(p, pn.start) || !pn.known && pn.sess.live()) {
				sess = pn.sess
				return nil
			}
		}
		return nil
	})
	return sess, err
}

// UnstoppedWalk, ClosedWithWorkspaceWalk and UncountedWalk list what
// Unstopped, ClosedWithWorkspace and Uncounted do, as they did before the
// pending index: by a walk of every session the store keeps.
func (s *Store) UnstoppedWalk() ([]Session, error) {
	return s.sessionsWhere(false, func(_ *bolt.Tx, sess *Session) bool { return !sess.live() && !sess.Stopped })
}

func (s *Store) UncountedWalk() ([]Session, error) {
	return s.sessionsWhere(false, func(_ *bolt.Tx, sess *Session) bool { return sess.Stopped && sess.Handle["count_due"] != nil })
}

func (s *Store) ClosedWithWorkspaceWalk() ([]Session, error) {
	return s.sessionsWhere(false, func(tx *bolt.Tx, sess *Session) bool {
		if sess.Cleaned || !sess.Stopped || (sess.Handle["worktree"] == nil && sess.Handle["dir"] == nil) {
			return false
		}
		var b beadRow
		ok, _ := get(tx, bBeads, sess.Bead, &b)
		return ok && b.Status == "closed"
	})
}

// StrandedWalk is Stranded as it was before the open index: over a walk of
// every bead, closed ones too.
func (s *Store) StrandedWalk(ctx context.Context, depot string) ([]StrandedBead, error) {
	return s.stranded(ctx, depot, func(tx *bolt.Tx) ([]*beadRow, error) {
		var rows []*beadRow
		err := each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
			rows = append(rows, r)
			return nil
		})
		s.beadReads.Add(int64(len(rows)))
		return rows, err
	})
}

// CloneBead is the copy of b a listing hands out.
func CloneBead(b *Bead) Bead { return b.clone() }

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

// Backdate moves the time of events first..last (inclusive) to at, and the
// time of the dedupe keys they recorded: a key is recorded with its event.
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
		seen := tx.Bucket(bHookSeen)
		moved := map[string][]byte{}
		if err := seen.ForEach(func(k, v []byte) error {
			if seq, _ := seenRecord(v); seq >= first && seq <= last {
				moved[string(k)] = seenValue(seq, at)
			}
			return nil
		}); err != nil {
			return err
		}
		for k, v := range moved {
			if err := seen.Put([]byte(k), v); err != nil {
				return err
			}
		}
		return nil
	})
}

// UnstampHookSeen rewrites the dedupe key as a build from before keys had a
// time wrote it: the seq of its event, and no more.
func (s *Store) UnstampHookSeen(key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		seen := tx.Bucket(bHookSeen)
		v := seen.Get([]byte(key))
		if v == nil {
			return fmt.Errorf("no dedupe key %q", key)
		}
		seq, _ := seenRecord(v)
		return seen.Put([]byte(key), []byte(seqKey(seq)))
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

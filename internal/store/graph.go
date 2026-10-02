package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

type Dep struct {
	From      string    `json:"from"` // must close first (for kind 'blocks')
	To        string    `json:"to"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// depRow is a stored edge: from -kind-> to, both bead UUIDs.
type depRow struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	Revision  string    `json:"revision"`
	LastActor string    `json:"last_actor"`
}

// AddDep relates from -> to. For kind 'blocks', `to` is not ready until
// `from` closes. The edge gets its own revision, and both beads, whose
// representations list or count it, are re-minted.
func (s *Store) AddDep(ctx context.Context, fromRef, toRef, kind, actor string) error {
	return s.write(func(w *wtx) error {
		fromRow, err := w.loadBead(fromRef)
		if err != nil {
			return err
		}
		toRow, err := w.loadBead(toRef)
		if err != nil {
			return err
		}
		from, to := fromRow.UUID, toRow.UUID
		if from == to {
			return fmt.Errorf("a bead cannot depend on itself")
		}
		for _, k := range keys(w.Tx, bDepFrom, from+"\x00") {
			var d depRow
			if _, err := get(w.Tx, bDeps, k[len(from)+1:], &d); err != nil {
				return err
			}
			if d.To == to && d.Kind == kind {
				return fmt.Errorf("%s already %s %s", fromRow.ID, kind, toRow.ID)
			}
		}
		// Ids are never reused: a removed edge's id stays retired.
		d := &depRow{ID: newEdgeID(), From: from, To: to, Kind: kind, CreatedAt: now(), CreatedBy: actor}
		if err := w.mintDep(d, actor); err != nil {
			return err
		}
		if err := w.Bucket(bDepFrom).Put([]byte(join(from, d.ID)), []byte{}); err != nil {
			return err
		}
		if err := w.Bucket(bDepTo).Put([]byte(join(to, d.ID)), []byte{}); err != nil {
			return err
		}
		if err := w.mintTouched(from, actor); err != nil {
			return err
		}
		if err := w.mintTouched(to, actor); err != nil {
			return err
		}
		return w.event(to, "dep_added", actor, map[string]any{"from": fromRow.ID, "kind": kind})
	})
}

func (w *wtx) mintDep(d *depRow, actor string) error {
	d.Revision, d.LastActor = newRevision(), actor
	if err := put(w.Tx, bDeps, d.ID, d); err != nil {
		return err
	}
	return w.version("dep", d.ID, d.Revision, actor, d, false)
}

// newEdgeID is 20 lowercase alphanumerics from a fresh ULID's randomness.
func newEdgeID() string {
	r := []byte(newRevision())
	for i, c := range r {
		if c >= 'A' && c <= 'Z' {
			r[i] = c + 'a' - 'A'
		}
	}
	return string(r[6:])
}

// RemoveDep deletes the edge, leaving a tombstone version that cites its
// final revision. Removing an edge that does not exist is a no-op.
func (s *Store) RemoveDep(ctx context.Context, fromRef, toRef, kind, actor string) error {
	return s.write(func(w *wtx) error {
		fromRow, err := w.loadBead(fromRef)
		if err != nil {
			return err
		}
		toRow, err := w.loadBead(toRef)
		if err != nil {
			return err
		}
		from, to := fromRow.UUID, toRow.UUID
		removed := false
		for _, k := range keys(w.Tx, bDepFrom, from+"\x00") {
			id := k[len(from)+1:]
			var d depRow
			if _, err := get(w.Tx, bDeps, id, &d); err != nil {
				return err
			}
			if d.To != to || d.Kind != kind {
				continue
			}
			if err := w.version("dep", id, newRevision(), actor, d, true); err != nil {
				return err
			}
			for _, del := range []struct {
				b   []byte
				key string
			}{{bDeps, id}, {bDepFrom, join(from, id)}, {bDepTo, join(to, id)}} {
				if err := w.Bucket(del.b).Delete([]byte(del.key)); err != nil {
					return err
				}
			}
			removed = true
		}
		if !removed {
			return nil
		}
		if err := w.mintTouched(from, actor); err != nil {
			return err
		}
		if err := w.mintTouched(to, actor); err != nil {
			return err
		}
		return w.event(to, "dep_removed", actor, map[string]any{"from": fromRow.ID, "kind": kind})
	})
}

// Deps returns every edge touching the bead, in either direction.
func (s *Store) Deps(ctx context.Context, ref string) ([]Dep, error) {
	out := []Dep{}
	err := s.read(func(tx *bolt.Tx) error {
		id, err := resolve(tx, ref)
		if err != nil {
			return err
		}
		h := handlesIn(tx)
		for _, b := range [][]byte{bDepFrom, bDepTo} {
			for _, k := range keys(tx, b, id+"\x00") {
				var d depRow
				if _, err := get(tx, bDeps, k[len(id)+1:], &d); err != nil {
					return err
				}
				out = append(out, Dep{From: h.of(d.From), To: h.of(d.To), Kind: d.Kind, CreatedAt: d.CreatedAt})
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}

// OpenBlockers lists the handles of beads that block ref and are not closed.
func (s *Store) OpenBlockers(ctx context.Context, ref string) ([]string, error) {
	var out []string
	err := s.read(func(tx *bolt.Tx) error {
		id, err := resolve(tx, ref)
		if err != nil {
			return err
		}
		h := handlesIn(tx)
		for _, u := range openBlockers(tx, id) {
			out = append(out, h.of(u))
		}
		return nil
	})
	if out == nil {
		out = []string{}
	}
	return out, err
}

// Note kinds: plain evidence on a bead, and a resident's journal entries.
const (
	NoteNote    = "note"
	NoteJournal = "journal"
	NoteHandoff = "handoff"
)

type Note struct {
	Bead   string    `json:"bead"`           // stored as the UUID, returned as the handle
	Kind   string    `json:"kind,omitempty"` // "" or note, journal, handoff
	Author string    `json:"author"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
	Seq    uint64    `json:"seq"` // order of notes, store-wide
}

func (s *Store) AddNote(ctx context.Context, id, text, actor string) error {
	return s.write(func(w *wtx) error { return w.addNote(id, NoteNote, text, actor) })
}

func (w *wtx) addNote(id, kind, text, actor string) error {
	row, err := w.loadBead(id)
	if err != nil {
		return err
	}
	seq, err := w.Bucket(bNotes).NextSequence()
	if err != nil {
		return err
	}
	n := Note{Bead: row.UUID, Kind: kind, Author: actor, Text: text, At: now(), Seq: seq}
	if err := put(w.Tx, bNotes, join(row.UUID, seqKey(seq)), n); err != nil {
		return err
	}
	// The bead's representation counts its notes (bd's comment_count).
	row.Notes++
	if err := w.mintBead(row, actor); err != nil {
		return err
	}
	evKind := "noted"
	if kind == NoteJournal || kind == NoteHandoff {
		evKind = kind
	}
	return w.event(row.UUID, evKind, actor, nil)
}

func (s *Store) Notes(ctx context.Context, ref string) ([]Note, error) {
	out := []Note{}
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, ref)
		if err != nil {
			return err
		}
		return each(tx, bNotes, row.UUID+"\x00", func(_ []byte, n *Note) error {
			n.Bead = row.ID
			out = append(out, *n)
			return nil
		})
	})
	return out, err
}

type Rig struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Base string `json:"base"`
	// Check overrides the repo's .yardr/check as the rig's merge gate: a
	// command run with sh -c in the bead's worktree (see package rigcheck).
	Check string `json:"check,omitempty"`
}

func (s *Store) PutRig(ctx context.Context, r Rig) error {
	return s.write(func(w *wtx) error { return put(w.Tx, bRigs, r.Name, r) })
}

func (s *Store) Rigs(ctx context.Context) ([]Rig, error) {
	out := []Rig{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bRigs, "", func(_ []byte, r *Rig) error {
			out = append(out, *r)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetRig(ctx context.Context, name string) (*Rig, error) {
	r := new(Rig)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bRigs, name, r)
		if err == nil && !ok {
			err = fmt.Errorf("rig %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// SnapBead and SnapDep are the full records the BDP server serves, with the
// actor of their latest version for attribution. Edges name beads by
// handle, like everything else the store returns.
type SnapBead struct {
	Bead
	LastActor *string `json:"last_actor"`
	Notes     int     `json:"notes"`
}

type SnapDep struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Kind      string    `json:"kind"`
	Revision  string    `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy *string   `json:"created_by"`
	LastActor *string   `json:"last_actor"`
}

type Snapshot struct {
	Beads []SnapBead
	Deps  []SnapDep
}

// Snapshot reads every bead and edge in one read transaction: a single
// consistent state of the yard.
func (s *Store) Snapshot(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{Beads: []SnapBead{}, Deps: []SnapDep{}}
	err := s.read(func(tx *bolt.Tx) error {
		if err := each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
			snap.Beads = append(snap.Beads, SnapBead{Bead: r.Bead, LastActor: ptr(r.LastActor), Notes: r.Notes})
			return nil
		}); err != nil {
			return err
		}
		h := handlesIn(tx)
		return each(tx, bDeps, "", func(_ []byte, d *depRow) error {
			snap.Deps = append(snap.Deps, SnapDep{ID: d.ID, From: h.of(d.From), To: h.of(d.To), Kind: d.Kind,
				Revision: d.Revision, CreatedAt: d.CreatedAt, CreatedBy: ptr(d.CreatedBy), LastActor: ptr(d.LastActor)})
			return nil
		})
	})
	return snap, err
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// DepBlocks is the edge kind that keeps its target from being ready until
// its source closes.
const DepBlocks = "blocks"

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
// `from` closes. Kind 'parent' makes `to` a child of epic `from` (see
// checkParent) and, in the same write, a blocker of it (`to -blocks->
// from`, unless that edge is there already): an epic is not ready while a
// child is open, so nothing routes it until its last child closes. The edge
// gets its own revision, and both beads, whose representations list or
// count it, are re-minted.
func (s *Store) AddDep(ctx context.Context, fromRef, toRef, kind, actor string) error {
	if err := checkName("dep kind", kind); err != nil {
		return err
	}
	return s.write(func(w *wtx) error {
		fromRow, err := w.loadBead(fromRef)
		if err != nil {
			return err
		}
		toRow, err := w.loadBead(toRef)
		if err != nil {
			return err
		}
		if fromRow.UUID == toRow.UUID {
			return fmt.Errorf("a bead cannot depend on itself")
		}
		if kind == DepParent {
			if err := checkParent(w.Tx, fromRow, toRow); err != nil {
				return err
			}
		}
		if hasEdge(w.Tx, fromRow.UUID, toRow.UUID, kind) {
			return fmt.Errorf("%s already %s %s", fromRow.ID, kind, toRow.ID)
		}
		if err := w.addDep(fromRow, toRow, kind, actor); err != nil {
			return err
		}
		if kind == DepParent && !hasEdge(w.Tx, toRow.UUID, fromRow.UUID, DepBlocks) {
			return w.addDep(toRow, fromRow, DepBlocks, actor)
		}
		return nil
	})
}

// hasEdge reports whether the edge from -kind-> to (bead UUIDs) exists.
func hasEdge(tx *bolt.Tx, from, to, kind string) bool {
	for _, d := range edges(tx, bDepFrom, from, kind) {
		if d.To == to {
			return true
		}
	}
	return false
}

// addDep stores the edge from -kind-> to and logs dep_added on to.
func (w *wtx) addDep(fromRow, toRow *beadRow, kind, actor string) error {
	from, to := fromRow.UUID, toRow.UUID
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
}

func (w *wtx) mintDep(d *depRow, actor string) error {
	d.Revision, d.LastActor = newRevision(), actor
	return put(w.Tx, bDeps, d.ID, d)
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

// RemoveDep deletes the edge; the dep_removed event is its record.
// Removing an edge that does not exist is a no-op. Removing a child from
// its epic (kind parent) removes the blocks edge AddDep paired with it, so
// the epic no longer waits on a bead that is not its child.
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
		removed, err := w.removeDep(fromRow, toRow, kind, actor)
		if err != nil || !removed || kind != DepParent || fromRow.Type != TypeEpic {
			return err
		}
		_, err = w.removeDep(toRow, fromRow, DepBlocks, actor)
		return err
	})
}

// removeDep deletes the edges from -kind-> to and logs dep_removed on to;
// it reports whether there was one.
func (w *wtx) removeDep(fromRow, toRow *beadRow, kind, actor string) (bool, error) {
	from, to := fromRow.UUID, toRow.UUID
	removed := false
	for _, k := range keys(w.Tx, bDepFrom, from+"\x00") {
		id := k[len(from)+1:]
		var d depRow
		if _, err := get(w.Tx, bDeps, id, &d); err != nil {
			return false, err
		}
		if d.To != to || d.Kind != kind {
			continue
		}
		for _, del := range []struct {
			b   []byte
			key string
		}{{bDeps, id}, {bDepFrom, join(from, id)}, {bDepTo, join(to, id)}} {
			if err := w.Bucket(del.b).Delete([]byte(del.key)); err != nil {
				return false, err
			}
		}
		removed = true
	}
	if !removed {
		return false, nil
	}
	if err := w.mintTouched(from, actor); err != nil {
		return false, err
	}
	if err := w.mintTouched(to, actor); err != nil {
		return false, err
	}
	return true, w.event(to, "dep_removed", actor, map[string]any{"from": fromRow.ID, "kind": kind})
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
	// The note's seq lets subscriptions match mentions in this note alone.
	return w.event(row.UUID, evKind, actor, map[string]any{"note": seq})
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
	// AllowNoCheck lets a rig with neither Check nor .yardr/check pass its
	// gate; without it, no gate fails.
	AllowNoCheck bool `json:"allow_no_check,omitempty"`
}

// PutRig adds or replaces a rig, and logs rig_updated with what it now
// holds: a --check override replaces the repo's gate, so it must not change
// unseen.
func (s *Store) PutRig(ctx context.Context, r Rig, actor string) error {
	if err := checkName("rig name", r.Name); err != nil {
		return err
	}
	return s.write(func(w *wtx) error {
		if err := put(w.Tx, bRigs, r.Name, r); err != nil {
			return err
		}
		return w.event("", "rig_updated", actor, map[string]any{
			"rig": r.Name, "path": r.Path, "base": r.Base, "check": r.Check, "allow_no_check": r.AllowNoCheck})
	})
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

// RigRemoval is what DeleteRig took out of the store with the rig.
type RigRemoval struct {
	Rig          string        `json:"rig"`
	Routes       []string      `json:"routes"`        // ids of the routes that matched --rig <name>
	FlowPointers []FlowPointer `json:"flow_pointers"` // the rig's and its types' flow pointers
}

// DeleteRig removes a rig with the routes that match it and its flow
// pointers, and logs rig_removed. It refuses while the rig has open beads
// or live sessions, and names them: a bead whose rig is gone could not be
// routed, worked or merged. Closed beads keep the rig's name as history.
// Only the store changes: the clone, its worktrees and its .yardr/ are not
// touched.
func (s *Store) DeleteRig(ctx context.Context, name, actor string) (*RigRemoval, error) {
	var out *RigRemoval
	err := s.write(func(w *wtx) error {
		// Batch reruns this closure when another call in the batch fails, so
		// what was removed starts over with each attempt.
		out = &RigRemoval{Rig: name, Routes: []string{}, FlowPointers: []FlowPointer{}}
		if w.Bucket(bRigs).Get([]byte(name)) == nil {
			return fmt.Errorf("rig %s: %w", name, ErrNotFound)
		}
		users, err := rigUsers(w.Tx, name)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("rig %s is in use by %s; close the beads and end the sessions first", name, strings.Join(users, ", "))
		}
		if err := each(w.Tx, bRoutes, "", func(k []byte, r *Route) error {
			if r.Rig != nil && *r.Rig == name {
				out.Routes = append(out.Routes, string(k))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range out.Routes {
			if err := w.Bucket(bRoutes).Delete([]byte(k)); err != nil {
				return err
			}
		}
		// Pointer keys are <rig>\x00<type>, so the rig's own pointer and its
		// types' share the prefix; another rig whose name extends this one
		// (web, web2) does not.
		var ptrKeys []string
		if err := each(w.Tx, bFlowPtrs, name+"\x00", func(k []byte, p *FlowPointer) error {
			ptrKeys = append(ptrKeys, string(k))
			out.FlowPointers = append(out.FlowPointers, *p)
			return nil
		}); err != nil {
			return err
		}
		for _, k := range ptrKeys {
			if err := w.Bucket(bFlowPtrs).Delete([]byte(k)); err != nil {
				return err
			}
		}
		if err := w.Bucket(bRigs).Delete([]byte(name)); err != nil {
			return err
		}
		ptrs := make([]string, 0, len(out.FlowPointers))
		for _, p := range out.FlowPointers {
			ptrs = append(ptrs, p.String())
		}
		return w.event("", "rig_removed", actor, map[string]any{
			"rig": name, "routes": out.Routes, "flow_pointers": ptrs})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// rigUsers names what still needs the rig: its open beads, and the live
// sessions working them.
func rigUsers(tx *bolt.Tx, name string) ([]string, error) {
	var out []string
	err := each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
		if r.Status != "closed" && r.Rig != nil && *r.Rig == name {
			out = append(out, "bead "+r.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(tx, bSessions, "", func(_ []byte, sess *Session) error {
		if !sess.live() {
			return nil
		}
		var b beadRow
		if ok, err := get(tx, bBeads, sess.Bead, &b); err != nil {
			return err
		} else if ok && b.Rig != nil && *b.Rig == name {
			out = append(out, "session "+sess.ID+" (bead "+b.ID+")")
		}
		return nil
	})
	return out, err
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

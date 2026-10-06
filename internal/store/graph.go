package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// DepBlocks is the edge kind that keeps its target from being ready until
// its source closes.
const DepBlocks = "blocks"

// DepDiscoveredFrom is the edge kind from a bead to each bead that was
// found while working on it (`bead create --discovered-from`): the record of
// where follow-up work came from. It has the direction of blocks and parent,
// the earlier bead first, so BDP serves it as bd does: the found bead is the
// link's source and "depends on" its origin. It is a record only: it never
// blocks, and nothing routes, groups or lands by it.
const DepDiscoveredFrom = "discovered-from"

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
// `from` closes. Kind 'parent' makes `to` a child of train `from` (see
// checkParent) and, in the same write, a blocker of it (`to -blocks->
// from`, unless that edge is there already): a train is not ready while a
// child is open, so nothing routes it until its last child closes. The edge
// gets its own revision, and both beads, whose representations list or
// count it, are re-minted.
//
// A blocks edge that would close a cycle of blockers is refused, naming the
// cycle: if all its beads were open, none would be ready. That is
// the edge asked for with kind blocks, and the child's edge a parent edge
// brings. Any other kind decides no readiness and is never refused for it.
// The walk is part of this write, so of two adds that would close a cycle
// between them the second sees the first's edge.
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
		childBlocks := kind == DepParent && !hasEdge(w.Tx, toRow.UUID, fromRow.UUID, DepBlocks)
		switch {
		case kind == DepBlocks:
			if err := checkCycle(w.Tx, fromRow, toRow, ""); err != nil {
				return err
			}
		case childBlocks:
			if err := checkCycle(w.Tx, toRow, fromRow, "a child blocks its train, so "); err != nil {
				return err
			}
		}
		if err := w.addDep(fromRow, toRow, kind, actor); err != nil {
			return err
		}
		if childBlocks {
			return w.addDep(toRow, fromRow, DepBlocks, actor)
		}
		return nil
	})
}

// newBlockers reads the beads refs name as the blockers of a bead about to
// be created, in the order given. It refuses one that does not exist and
// one named twice (by its handle and its UUID, say), as AddDep refuses an
// edge that is already there.
func newBlockers(tx *bolt.Tx, refs []string) ([]*beadRow, error) {
	rows, seen := make([]*beadRow, 0, len(refs)), map[string]bool{}
	for _, ref := range refs {
		row, err := loadBead(tx, ref)
		if err != nil {
			return nil, fmt.Errorf("blocked by: %w", err)
		}
		if seen[row.UUID] {
			return nil, fmt.Errorf("blocked by: %s is named twice", row.ID)
		}
		seen[row.UUID] = true
		rows = append(rows, row)
	}
	return rows, nil
}

// CheckBlockers reports why CreateBead would refuse refs as NewBead.BlockedBy:
// a bead that does not exist, or one named twice. bead create asks it before
// it creates: a server from before BlockedBy does not know the question,
// where its CreateBead would drop the field and create the bead unblocked.
func (s *Store) CheckBlockers(ctx context.Context, refs []string) error {
	return s.read(func(tx *bolt.Tx) error {
		_, err := newBlockers(tx, refs)
		return err
	})
}

// checkCycle refuses the edge from -blocks-> to when to already blocks
// from, directly or through other beads: "a blocks b blocks c blocks a",
// in handles, the new edge first. Closed beads count like open ones. A
// closed blocker blocks nothing today, but Reopen does not look at edges:
// were a cycle through a closed bead let in, reopening that bead would make
// it live with nothing said. So the walk is over every bead to reaches by
// blocks edges, closed ones included.
func checkCycle(tx *bolt.Tx, from, to *beadRow, why string) error {
	path := pathTo(to.UUID, from.UUID, func(u string) []string {
		var next []string
		for _, d := range edges(tx, bDepFrom, u, DepBlocks) {
			next = append(next, d.To)
		}
		return next
	})
	if path == nil {
		return nil
	}
	h := handlesIn(tx)
	cycle, closed := []string{from.ID}, []string{}
	for _, u := range path {
		cycle = append(cycle, h.of(u))
		var row beadRow
		if ok, _ := get(tx, bBeads, u, &row); ok && row.Status == "closed" {
			closed = append(closed, row.ID)
		}
	}
	note := ""
	if len(closed) > 0 {
		note = fmt.Sprintf(" (%s closed, and would block again once reopened)", strings.Join(closed, ", "))
	}
	return fmt.Errorf("%s%s blocks %s would close a cycle of blockers: %s%s",
		why, from.ID, to.ID, strings.Join(cycle, " blocks "), note)
}

// pathTo is a shortest path from -> ... -> to along next, both ends
// included; nil when there is none. With from == to it is a cycle through
// from, which is then named at both ends.
func pathTo(from, to string, next func(string) []string) []string {
	prev := map[string]string{from: ""}
	for queue := []string{from}; len(queue) > 0; queue = queue[1:] {
		u := queue[0]
		for _, n := range next(u) {
			if n == to {
				path := []string{to}
				for x := u; x != ""; x = prev[x] {
					path = append(path, x)
				}
				slices.Reverse(path)
				return path
			}
			if _, seen := prev[n]; !seen {
				prev[n] = u
				queue = append(queue, n)
			}
		}
	}
	return nil
}

// BlockerCycles finds the cycles of blocks edges among beads that are not
// closed: every bead in one waits on itself and is never ready. AddDep
// refuses the edge that would close one, so these come from a store written
// before it did, or from a bead reopened into one that store held. Each is
// a list of handles in blocking order, starting and ending with its
// smallest: a blocks b blocks a. Beads that block each other in more than
// one way (a knot of several cycles) are reported as one cycle through
// their smallest handle; the rest shows once that one is broken. Nothing is
// changed: yardr dep rm removes an edge.
func (s *Store) BlockerCycles(ctx context.Context) ([][]string, error) {
	out := [][]string{}
	err := s.read(func(tx *bolt.Tx) error {
		open := map[string]string{} // UUID -> handle
		if err := each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
			if r.Status != "closed" {
				open[r.UUID] = r.ID
			}
			return nil
		}); err != nil {
			return err
		}
		next := map[string][]string{}
		if err := each(tx, bDeps, "", func(_ []byte, d *depRow) error {
			if d.Kind == DepBlocks && open[d.From] != "" && open[d.To] != "" {
				next[d.From] = append(next[d.From], d.To)
			}
			return nil
		}); err != nil {
			return err
		}
		byHandle := func(a, b string) int { return cmp.Compare(open[a], open[b]) }
		nodes := make([]string, 0, len(next))
		for u, to := range next {
			slices.SortFunc(to, byHandle)
			nodes = append(nodes, u)
		}
		slices.SortFunc(nodes, byHandle)
		for _, knot := range knots(nodes, next) {
			in := map[string]bool{}
			for _, u := range knot {
				in[u] = true
			}
			start := slices.MinFunc(knot, byHandle)
			cycle := pathTo(start, start, func(u string) []string {
				var within []string
				for _, n := range next[u] {
					if in[n] {
						within = append(within, n)
					}
				}
				return within
			})
			for i, u := range cycle {
				cycle[i] = open[u]
			}
			if cycle != nil {
				out = append(out, cycle)
			}
		}
		slices.SortFunc(out, func(a, b []string) int { return cmp.Compare(a[0], b[0]) })
		return nil
	})
	return out, err
}

// knots are the sets of nodes that all reach each other along next (the
// strongly connected components, by Tarjan's algorithm), without those of
// one node that has no edge to itself: every node of a knot is on a cycle.
// The walk keeps its own stack: a chain of blockers may be as long as the
// store.
func knots(nodes []string, next map[string][]string) [][]string {
	type frame struct {
		u string
		i int // the next of next[u] to look at
	}
	var out [][]string
	index, low, onStack := map[string]int{}, map[string]int{}, map[string]bool{}
	var stack []string
	for _, root := range nodes {
		if _, seen := index[root]; seen {
			continue
		}
		visit := func(u string) frame {
			index[u], low[u] = len(index), len(index)
			stack, onStack[u] = append(stack, u), true
			return frame{u: u}
		}
		for work := []frame{visit(root)}; len(work) > 0; {
			f := &work[len(work)-1]
			if f.i < len(next[f.u]) {
				n := next[f.u][f.i]
				f.i++
				if _, seen := index[n]; !seen {
					work = append(work, visit(n))
				} else if onStack[n] {
					low[f.u] = min(low[f.u], index[n])
				}
				continue
			}
			u := f.u
			work = work[:len(work)-1]
			if len(work) > 0 {
				p := work[len(work)-1].u
				low[p] = min(low[p], low[u])
			}
			if low[u] != index[u] {
				continue
			}
			var knot []string
			for {
				n := stack[len(stack)-1]
				stack, onStack[n] = stack[:len(stack)-1], false
				knot = append(knot, n)
				if n == u {
					break
				}
			}
			if len(knot) > 1 || slices.Contains(next[u], u) {
				out = append(out, knot)
			}
		}
	}
	return out
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

// newEdgeID is the last 20 lowercase alphanumerics of a fresh ULID: its last
// 4 timestamp characters and 16 random characters.
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
// its train (kind parent) removes the blocks edge AddDep paired with it, so
// the train no longer waits on a bead that is not its child.
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
		if err != nil || !removed || kind != DepParent || fromRow.Type != TypeTrain {
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

// BeadRef names a bead on another bead's page: enough to show it without
// reading it.
type BeadRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// Discovery is a bead's discovered-from edges, both ways, each list oldest
// edge first.
type Discovery struct {
	// From are the beads this one was found from. bead create gives one;
	// dep add can add more.
	From []BeadRef `json:"discovered_from"`
	// LedTo are the beads found from this one, open and closed.
	LedTo []BeadRef `json:"led_to"`
}

// Discovery returns where ref was found from and what was found from it.
func (s *Store) Discovery(ctx context.Context, ref string) (*Discovery, error) {
	d := &Discovery{From: []BeadRef{}, LedTo: []BeadRef{}}
	err := s.read(func(tx *bolt.Tx) error {
		id, err := resolve(tx, ref)
		if err != nil {
			return err
		}
		refs := func(index []byte, other func(depRow) string) []BeadRef {
			out := []BeadRef{}
			for _, e := range edges(tx, index, id, DepDiscoveredFrom) {
				var row beadRow
				if ok, _ := get(tx, bBeads, other(e), &row); ok {
					out = append(out, BeadRef{ID: row.ID, Title: row.Title, Status: row.Status})
				}
			}
			return out
		}
		d.From = refs(bDepTo, func(e depRow) string { return e.From })
		d.LedTo = refs(bDepFrom, func(e depRow) string { return e.To })
		return nil
	})
	return d, err
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

// Rig kinds. A git rig is a repository: beads work on branches in
// worktrees, start from Base and land on it, and the rig's .yardr/ is read
// from the base. A dir rig is a plain directory (documents, images, a small
// business's files): work happens in the directory itself, there is no
// base, and .yardr/ is read from the directory as it is.
const (
	RigGit = "git"
	RigDir = "dir"
)

type Rig struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Kind is RigGit or RigDir. Rigs stored before kinds existed are git
	// rigs (migrateRigKinds).
	Kind string `json:"kind"`
	Base string `json:"base"`
	// Check overrides the repo's .yardr/check as the rig's merge gate: a
	// command run with sh -c in the bead's worktree (see package rigcheck).
	Check string `json:"check,omitempty"`
	// CheckLight is the rig's light gate, beside Check: the override of the
	// repo's .yardr/check-light (see package rigcheck).
	CheckLight string `json:"check_light,omitempty"`
	// LightSlot: the rig's light gate takes one of the machine's gate slots,
	// as its full gate always does (see package rigcheck). For a rig whose
	// light gate is not light.
	LightSlot bool `json:"light_slot,omitempty"`
	// AllowNoCheck lets a rig with neither Check nor .yardr/check pass its
	// gate; without it, no gate fails.
	AllowNoCheck bool `json:"allow_no_check,omitempty"`
}

// IsDir reports whether the rig is a directory, not a repository.
func (r *Rig) IsDir() bool { return r.Kind == RigDir }

// PutRig adds or replaces a rig, and logs rig_updated with what it now
// holds: a --check override replaces the repo's gate, so it must not change
// unseen. A rig given no kind is a git rig.
func (s *Store) PutRig(ctx context.Context, r Rig, actor string) error {
	if err := checkName("rig name", r.Name); err != nil {
		return err
	}
	switch r.Kind {
	case "":
		r.Kind = RigGit
	case RigGit:
	case RigDir:
		if r.Base != "" {
			return fmt.Errorf("rig %s is a directory rig: it has no base (got %q)", r.Name, r.Base)
		}
	default:
		return fmt.Errorf("rig %s: kind %q is neither %s nor %s", r.Name, r.Kind, RigGit, RigDir)
	}
	return s.write(func(w *wtx) error {
		if err := put(w.Tx, bRigs, r.Name, r); err != nil {
			return err
		}
		return w.event("", "rig_updated", actor, map[string]any{
			"rig": r.Name, "path": r.Path, "kind": r.Kind, "base": r.Base, "check": r.Check, "check_light": r.CheckLight, "light_slot": r.LightSlot, "allow_no_check": r.AllowNoCheck})
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
		users, err := s.rigUsers(w.Tx, name)
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
		// The record of what was imported into the rig goes with it: its
		// routes and pointers just did.
		for _, k := range keys(w.Tx, bPacks, name+"\x00") {
			if err := w.Bucket(bPacks).Delete([]byte(k)); err != nil {
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
func (s *Store) rigUsers(tx *bolt.Tx, name string) ([]string, error) {
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
	live, err := s.liveSessions(tx, "")
	if err != nil {
		return nil, err
	}
	for _, sess := range live {
		var b beadRow
		if ok, err := get(tx, bBeads, sess.Bead, &b); err != nil {
			return nil, err
		} else if ok && b.Rig != nil && *b.Rig == name {
			out = append(out, "session "+sess.ID+" (bead "+b.ID+")")
		}
	}
	return out, nil
}

// SnapBead, SnapDep and SnapLink are the full records the BDP server serves,
// with the actor of their latest version for attribution. Edges and links
// name beads by handle, like everything else the store returns.
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

// SnapLink is a bead's link to an outside issue (see Link).
type SnapLink struct {
	ID        string    `json:"id"`
	Bead      string    `json:"bead"`
	Kind      string    `json:"kind"`
	Source    string    `json:"source"`
	URL       string    `json:"url"`
	Thread    string    `json:"thread,omitempty"`
	Revision  string    `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy *string   `json:"created_by"`
	LastActor *string   `json:"last_actor"`
}

type Snapshot struct {
	Beads []SnapBead
	Deps  []SnapDep
	Links []SnapLink
}

// Snapshot reads every bead, edge and link in one read transaction: a
// single consistent state of the yard.
func (s *Store) Snapshot(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{Beads: []SnapBead{}, Deps: []SnapDep{}, Links: []SnapLink{}}
	err := s.read(func(tx *bolt.Tx) error {
		if err := each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
			snap.Beads = append(snap.Beads, SnapBead{Bead: r.Bead, LastActor: ptr(r.LastActor), Notes: r.Notes})
			return nil
		}); err != nil {
			return err
		}
		h := handlesIn(tx)
		if err := each(tx, bDeps, "", func(_ []byte, d *depRow) error {
			snap.Deps = append(snap.Deps, SnapDep{ID: d.ID, From: h.of(d.From), To: h.of(d.To), Kind: d.Kind,
				Revision: d.Revision, CreatedAt: d.CreatedAt, CreatedBy: ptr(d.CreatedBy), LastActor: ptr(d.LastActor)})
			return nil
		}); err != nil {
			return err
		}
		return each(tx, bLinks, "", func(_ []byte, l *linkRow) error {
			snap.Links = append(snap.Links, SnapLink{ID: l.ID, Bead: h.of(l.Bead), Kind: l.Kind, Source: l.Source,
				URL: l.URL, Thread: l.Thread, Revision: l.Revision, CreatedAt: l.CreatedAt,
				CreatedBy: ptr(l.CreatedBy), LastActor: ptr(l.LastActor)})
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

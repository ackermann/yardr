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
	// At is the stage the edge holds its target in (kind blocks only, from
	// dep add --at): empty holds in every stage, as before.
	At string `json:"at,omitempty"`
	// Until is the stage that releases the edge's blocker (kind blocks
	// only, from dep add --until): empty releases only on close, as
	// before. While the blocker is in that stage, or past it along done,
	// the edge holds nothing.
	Until string `json:"until,omitempty"`
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
	At        string    `json:"at,omitempty"`
	Until     string    `json:"until,omitempty"`
}

// AddDep relates from -> to. For kind 'blocks', `to` is not ready until
// `from` closes. Kind 'parent' makes `to` a child of train `from` (see
// checkParent) and, in the same write, a blocker of it (`to -blocks->
// from`, unless that edge is there already): a train is not ready while a
// child is open, so nothing routes it until its last child closes. The edge
// gets its own revision, and both beads, whose representations list or
// count it, are re-minted.
//
// At (dep add --at) holds a blocks edge's target only while it is in that
// stage: empty holds in every stage, as before. It is refused for any other
// kind, and unless it names a stage of the target's own flow (any
// well-formed stage name where no flow governs it). Until (dep add
// --until) releases the edge's blocker once it reaches that stage, or a
// stage its done-route leads to from there, so the target routes while its
// blocker is still open; it is refused for any other kind, and unless it
// names a stage of the blocker's own flow. The duplicate key is (from, to,
// kind, at, until), so one blocker can hold its target in several stages
// and release from several; the cycle check stays on (from, to).
//
// A blocks edge that would close a cycle of blockers is refused, naming the
// cycle: if all its beads were open, none would be ready. That is
// the edge asked for with kind blocks, and the child's edge a parent edge
// brings. Any other kind decides no readiness and is never refused for it.
// The walk is part of this write, so of two adds that would close a cycle
// between them the second sees the first's edge.
func (s *Store) AddDep(ctx context.Context, fromRef, toRef, kind, at, until, actor string) error {
	if err := checkName("dep kind", kind); err != nil {
		return err
	}
	if at != "" && kind != DepBlocks {
		return fmt.Errorf("--at %q: only kind blocks holds at a stage, not %q", at, kind)
	}
	if until != "" && kind != DepBlocks {
		return fmt.Errorf("--until %q: only kind blocks releases at a stage, not %q", until, kind)
	}
	// The target's and the blocker's repository flows, read before the
	// write: git must not run inside one. The two beads may live in
	// different depots, so each edge end reads its own. A bead that does
	// not resolve leaves no repo; the write below refuses it as it always
	// has.
	var targetRepo, blockerRepo *RepoFlows
	if at != "" {
		if pre, err := s.GetBead(ctx, toRef); err == nil {
			if targetRepo, err = s.repoFor(ctx, pre.Depot, pre.Type, PinnedFlow(pre)); err != nil {
				return err
			}
		}
	}
	var fromDepot string
	if until != "" {
		if pre, err := s.GetBead(ctx, fromRef); err == nil {
			if pre.Depot != nil {
				fromDepot = *pre.Depot
			}
			if blockerRepo, err = s.repoFor(ctx, pre.Depot, pre.Type, PinnedFlow(pre)); err != nil {
				return err
			}
		}
	}
	// The repository flows the write's gate evaluation resolves blockers'
	// flows through, read before the write: git must not run inside one.
	// A blocks edge can flip its target's gate; a parent edge only its
	// train's, through the paired edge below.
	var repos map[string]*RepoFlows
	switch kind {
	case DepBlocks:
		repos = s.gateRepos(ctx, toRef)
		if until != "" && blockerRepo != nil && fromDepot != "" {
			if repos == nil {
				repos = map[string]*RepoFlows{}
			}
			repos[fromDepot] = blockerRepo
		}
	case DepParent:
		repos = s.gateRepos(ctx, fromRef)
	}
	look := &untilLooks{repos: repos, memo: flowMemo{}}
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
		if at != "" {
			if err := checkEdgeStage(w.Tx, toRow, "at", at, targetRepo); err != nil {
				return err
			}
		}
		if until != "" {
			if err := checkEdgeStage(w.Tx, fromRow, "until", until, blockerRepo); err != nil {
				return err
			}
		}
		if kind == DepParent {
			if err := checkParent(w.Tx, fromRow, toRow); err != nil {
				return err
			}
		}
		if hasEdge(w.Tx, fromRow.UUID, toRow.UUID, kind, at, until) {
			stages := ""
			if at != "" {
				stages += " at " + at
			}
			if until != "" {
				stages += " until " + until
			}
			return fmt.Errorf("%s already %s %s%s", fromRow.ID, kind, toRow.ID, stages)
		}
		childBlocks := kind == DepParent && !hasEdge(w.Tx, toRow.UUID, fromRow.UUID, DepBlocks, "", "")
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
		if kind == DepBlocks {
			before := gateHolders(w.Tx, &toRow.Bead, look)
			if err := w.addDep(fromRow, toRow, kind, at, until, actor); err != nil {
				return err
			}
			return w.logGateFlip(toRow, before, gateHolders(w.Tx, &toRow.Bead, look), actor)
		}
		if err := w.addDep(fromRow, toRow, kind, at, until, actor); err != nil {
			return err
		}
		if childBlocks {
			before := gateHolders(w.Tx, &fromRow.Bead, look)
			if err := w.addDep(toRow, fromRow, DepBlocks, "", "", actor); err != nil {
				return err
			}
			return w.logGateFlip(fromRow, before, gateHolders(w.Tx, &fromRow.Bead, look), actor)
		}
		return nil
	})
}

// checkEdgeStage refuses a blocks edge's staged flag (at on the target,
// until on the blocker) unless it names a stage of row's own flow. Where
// no flow governs the bead it may move to any stage, so any well-formed
// stage name is accepted. repo is row's repository flows as read before
// the write.
func checkEdgeStage(tx *bolt.Tx, row *beadRow, flag, stage string, repo *RepoFlows) error {
	f, _, err := resolveFlow(tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
	if err != nil {
		return changedMeanwhile(err)
	}
	if f == nil {
		if !validStage.MatchString(stage) {
			return fmt.Errorf("--%s %q: use letters, digits, '.', '_', '/' or '-'", flag, stage)
		}
		return nil
	}
	if !slices.Contains(f.Stages, stage) {
		return fmt.Errorf("--%s %q: flow %s (bead %s) has no such stage", flag, stage, f.Name, row.ID)
	}
	return nil
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

// hasEdge reports whether the edge from -kind-> to at until (bead UUIDs)
// exists.
func hasEdge(tx *bolt.Tx, from, to, kind, at, until string) bool {
	for _, d := range edges(tx, bDepFrom, from, kind) {
		if d.To == to && d.At == at && d.Until == until {
			return true
		}
	}
	return false
}

// addDep stores the edge from -kind-> to at until and logs dep_added on to.
func (w *wtx) addDep(fromRow, toRow *beadRow, kind, at, until, actor string) error {
	from, to := fromRow.UUID, toRow.UUID
	// Ids are never reused: a removed edge's id stays retired.
	d := &depRow{ID: newEdgeID(), From: from, To: to, Kind: kind, At: at, Until: until, CreatedAt: now(), CreatedBy: actor}
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

// RemoveDep deletes the edge from -kind-> to at until (dep rm --at
// --until); the dep_removed event is its record. Removing an edge that does
// not exist is a no-op. Removing a child from its train (kind parent)
// removes the plain blocks edge AddDep paired with it, so the train no
// longer waits on a bead that is not its child; a staged edge between the
// two is left alone.
func (s *Store) RemoveDep(ctx context.Context, fromRef, toRef, kind, at, until, actor string) error {
	if at != "" && kind != DepBlocks {
		return fmt.Errorf("--at %q: only kind blocks holds at a stage, not %q", at, kind)
	}
	if until != "" && kind != DepBlocks {
		return fmt.Errorf("--until %q: only kind blocks releases at a stage, not %q", until, kind)
	}
	// The repository flows the write's gate evaluation resolves blockers'
	// flows through, read before the write: git must not run inside one.
	// Removing a blocks edge can flip its target's gate; removing a
	// parent edge only its train's, through the paired edge below.
	var repos map[string]*RepoFlows
	switch kind {
	case DepBlocks:
		repos = s.gateRepos(ctx, toRef)
	case DepParent:
		repos = s.gateRepos(ctx, fromRef)
	}
	look := &untilLooks{repos: repos, memo: flowMemo{}}
	return s.write(func(w *wtx) error {
		fromRow, err := w.loadBead(fromRef)
		if err != nil {
			return err
		}
		toRow, err := w.loadBead(toRef)
		if err != nil {
			return err
		}
		if kind == DepBlocks {
			before := gateHolders(w.Tx, &toRow.Bead, look)
			removed, err := w.removeDep(fromRow, toRow, kind, at, until, actor)
			if err != nil || !removed {
				return err
			}
			return w.logGateFlip(toRow, before, gateHolders(w.Tx, &toRow.Bead, look), actor)
		}
		removed, err := w.removeDep(fromRow, toRow, kind, at, until, actor)
		if err != nil || !removed || kind != DepParent || fromRow.Type != TypeTrain {
			return err
		}
		before := gateHolders(w.Tx, &fromRow.Bead, look)
		if _, err := w.removeDep(toRow, fromRow, DepBlocks, "", "", actor); err != nil {
			return err
		}
		return w.logGateFlip(fromRow, before, gateHolders(w.Tx, &fromRow.Bead, look), actor)
	})
}

// removeDep deletes the edge from -kind-> to at until and logs dep_removed
// on to; it reports whether there was one.
func (w *wtx) removeDep(fromRow, toRow *beadRow, kind, at, until, actor string) (bool, error) {
	from, to := fromRow.UUID, toRow.UUID
	removed := false
	for _, k := range keys(w.Tx, bDepFrom, from+"\x00") {
		id := k[len(from)+1:]
		var d depRow
		if _, err := get(w.Tx, bDeps, id, &d); err != nil {
			return false, err
		}
		if d.To != to || d.Kind != kind || d.At != at || d.Until != until {
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
				out = append(out, Dep{From: h.of(d.From), To: h.of(d.To), Kind: d.Kind, CreatedAt: d.CreatedAt, At: d.At, Until: d.Until})
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}

// AllDeps lists every edge of the yard, each once, the oldest first: what
// Deps lists for one bead, for a reader that draws them all and would
// otherwise ask for every bead's and get each edge from both its ends.
func (s *Store) AllDeps(ctx context.Context) ([]Dep, error) {
	out := []Dep{}
	err := s.read(func(tx *bolt.Tx) error {
		h := handlesIn(tx)
		return each(tx, bDeps, "", func(_ []byte, d *depRow) error {
			out = append(out, Dep{From: h.of(d.From), To: h.of(d.To), Kind: d.Kind, CreatedAt: d.CreatedAt, At: d.At, Until: d.Until})
			return nil
		})
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

// OpenBlockers lists the handles of beads that hold ref now: its blockers
// that are unreleased, through edges that hold in its stage (see ready).
func (s *Store) OpenBlockers(ctx context.Context, ref string) ([]string, error) {
	var out []string
	err := s.withUntilRepos(ctx, func(look *untilLooks) error {
		out = nil
		return s.read(func(tx *bolt.Tx) error {
			row, err := loadBead(tx, ref)
			if err != nil {
				return err
			}
			h := handlesIn(tx)
			for _, u := range openBlockers(tx, &row.Bead, look) {
				out = append(out, h.of(u))
			}
			return nil
		})
	})
	if out == nil {
		out = []string{}
	}
	return out, err
}

// HoldingDeps lists the blocks edges into ref that hold it now: the edges
// behind OpenBlockers, oldest first, with the stages they name. It is what
// bead show and prime print the conditions from.
func (s *Store) HoldingDeps(ctx context.Context, ref string) ([]Dep, error) {
	out := []Dep{}
	err := s.withUntilRepos(ctx, func(look *untilLooks) error {
		out = []Dep{}
		return s.read(func(tx *bolt.Tx) error {
			row, err := loadBead(tx, ref)
			if err != nil {
				return err
			}
			h := handlesIn(tx)
			for _, d := range edges(tx, bDepTo, row.UUID, DepBlocks) {
				if !holds(tx, &row.Bead, &d, look) {
					continue
				}
				out = append(out, Dep{From: h.of(d.From), To: h.of(d.To), Kind: d.Kind, CreatedAt: d.CreatedAt, At: d.At, Until: d.Until})
			}
			return nil
		})
	})
	return out, err
}

// Note kinds: plain evidence on a bead, and a crew member's journal entries.
const (
	NoteNote    = "note"
	NoteJournal = "journal"
	NoteHandoff = "handoff"
)

type Note struct {
	Bead   string    `json:"bead"`           // stored as the UUID, returned as the handle
	Kind   string    `json:"kind,omitempty"` // "" or note, journal, handoff, ask, answer
	Author string    `json:"author"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
	Seq    uint64    `json:"seq"` // order of notes, store-wide
	// Via is the device the note was written through (WithVia), "" for one
	// written on the yard's own machine.
	Via string `json:"via,omitempty"`
	// Notify is the session the note is for: the bead's live session when
	// a plain note was written through AddNotedAs, unless the writer was
	// that session itself. "" when the bead had no live session, or the
	// note went through any other write. The daemon tells that session
	// once, when its agent is idle, and never interrupts a turn; if the
	// session ends first, nothing is sent.
	Notify string `json:"notify,omitempty"`
	// Told says the session's agent was nudged about the note
	// (MarkNoteTold, the note_told event), and Acked that the session
	// recorded having read it (AckNote, the note_acked event). A note
	// told and not acked when the session ends shows as such, so the
	// reviewer and the yardmaster know the work may not reflect it.
	Told  bool `json:"told,omitempty"`
	Acked bool `json:"acked,omitempty"`
	// NotifyLive is whether Notify names a live session: set on the notes
	// Notes returns, never stored. A note still untold whose session has
	// ended is told to nothing, ever.
	NotifyLive bool `json:"notify_live,omitempty"`
	// Files are the images the note carries, as PutBlob answered them, in
	// the order the note was written with them; nil on a note without
	// any. Only a plain note written with yardr bead note --file has any.
	Files []BlobRef `json:"files,omitempty"`
}

func (s *Store) AddNote(ctx context.Context, id, text, actor string) error {
	_, err := s.AddNoted(ctx, id, text, actor)
	return err
}

// AddNoted is AddNote that answers the note it wrote, seq and all, for
// whoever says what was written (yardr bead note).
func (s *Store) AddNoted(ctx context.Context, id, text, actor string) (*Note, error) {
	// A bead's handle never holds an "@" (validID).
	if bead, peer, foreign := strings.Cut(id, "@"); foreign {
		return nil, s.refuseForeignNote(bead, peer)
	}
	var out *Note
	err := s.writeVia(ctx, func(w *wtx) error {
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		out, err = w.addNoted(row, NoteNote, text, actor)
		return err
	})
	return out, err
}

// AddNotedAs is AddNoted as the session: a plain note that tells the
// bead's live session unless the writer is that session itself. session
// is "" outside one. The note names the session it is for (Notify, and
// "session" on its noted event), and the daemon nudges that session's
// agent about it once, when idle. Notes through any other write tell
// nothing: a move's note ends the session in the same write, asks and
// answers have their own flows, and the daemon notes past its sessions'
// ends. files are the images the note carries, each by its hash as PutBlob
// answered it.
func (s *Store) AddNotedAs(ctx context.Context, id, text, actor, session string, files ...string) (*Note, error) {
	// A bead's handle never holds an "@" (validID).
	if bead, peer, foreign := strings.Cut(id, "@"); foreign {
		return nil, s.refuseForeignNote(bead, peer)
	}
	if len(files) > 0 && strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("a note with images needs words: say what the picture shows")
	}
	refs, err := s.noteFiles(files)
	if err != nil {
		return nil, err
	}
	var out *Note
	err = s.writeVia(ctx, func(w *wtx) error {
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		notify := ""
		if row.Session != nil && *row.Session != session {
			notify = *row.Session
		}
		out, err = w.addNotedNotify(row, NoteNote, text, actor, notify, refs)
		return err
	})
	return out, err
}

// AckNote records that session has read the bead's note seq (yardr bead
// note --ack): the session the note was told to, and no other. An ack of
// a note still untold cancels its telling: the session read it from the
// bead. Acking twice is the note again, not a second event.
//
// session is the caller's word ($YARDR_SESSION). A call that comes from a
// session's pane (WithCaller, as a move is checked: see Expect) acks as
// that pane's session whatever session says, and one naming another is
// refused: an agent cannot mark a note for another session read, nor
// cancel its telling.
func (s *Store) AckNote(ctx context.Context, id string, seq uint64, session, actor string) (*Note, error) {
	var out *Note
	err := s.writeVia(ctx, func(w *wtx) error {
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		pane, err := paneSession(w.Tx, callerOf(ctx))
		if err != nil {
			return err
		}
		if pane != nil {
			if session != "" && session != pane.ID {
				return fmt.Errorf("%w: note %d on %s: this pane is session %s, not %s", ErrConflict, seq, row.ID, pane.ID, session)
			}
			session = pane.ID
		}
		var n Note
		ok, err := get(w.Tx, bNotes, join(row.UUID, seqKey(seq)), &n)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("bead %s has no note %d: %w", row.ID, seq, ErrNotFound)
		}
		if n.Notify == "" {
			return fmt.Errorf("note %d on %s was told to no session: nothing to ack", seq, row.ID)
		}
		if n.Notify != session {
			return fmt.Errorf("note %d on %s was told to session %s: ack it as that session", seq, row.ID, n.Notify)
		}
		if !n.Acked {
			n.Acked = true
			if err := put(w.Tx, bNotes, join(row.UUID, seqKey(seq)), n); err != nil {
				return err
			}
			if err := w.event(row.UUID, "note_acked", actor, map[string]any{"note": seq, "session": session}); err != nil {
				return err
			}
		}
		n.Bead = row.ID
		out = &n
		return nil
	})
	return out, err
}

// MarkNoteTold records that the daemon nudged session's agent about the
// bead's note seq (the note_told event). Telling twice is nothing twice:
// the second mark is a no-op, never a second event.
func (s *Store) MarkNoteTold(ctx context.Context, id string, seq uint64, session string) error {
	return s.write(func(w *wtx) error {
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		var n Note
		ok, err := get(w.Tx, bNotes, join(row.UUID, seqKey(seq)), &n)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("bead %s has no note %d: %w", row.ID, seq, ErrNotFound)
		}
		if n.Notify != session {
			return fmt.Errorf("note %d on %s is for session %s, not %s", seq, row.ID, n.Notify, session)
		}
		if n.Told {
			return nil
		}
		n.Told = true
		if err := put(w.Tx, bNotes, join(row.UUID, seqKey(seq)), n); err != nil {
			return err
		}
		return w.event(row.UUID, "note_told", "yard", map[string]any{"note": seq, "session": session})
	})
}

// refuseForeignNote refuses words on a bead@peer ref: the bead lives in
// the other yard, where no note of this one can stand. Words for the
// asker go as mail to asker@peer; the refusal names whom.
func (s *Store) refuseForeignNote(bead, peer string) error {
	var recs []foreignAsk
	var p *Peer
	err := s.read(func(tx *bolt.Tx) error {
		var err error
		p, err = loadPeer(tx, peer)
		if err != nil {
			return err
		}
		recs, err = foreignAsksForBead(tx, p, bead)
		return err
	})
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("bead %s@%s: no question from %s stands on it: %w", bead, p.Name, p.Name, ErrNotFound)
	}
	var askers []string
	seen := map[string]bool{}
	for _, rec := range recs {
		who := rec.From + "@" + p.Name
		if !seen[who] {
			seen[who] = true
			askers = append(askers, "yardr mail "+who+` "..."`)
		}
	}
	return fmt.Errorf("bead %s@%s is a question %s asked: words for the asker go as mail, %s",
		bead, p.Name, p.Name, strings.Join(askers, " or "))
}

func (w *wtx) addNote(id, kind, text, actor string) error {
	row, err := w.loadBead(id)
	if err != nil {
		return err
	}
	_, err = w.addNoted(row, kind, text, actor)
	return err
}

// addNoted adds the note to the bead row and answers it, seq and all.
func (w *wtx) addNoted(row *beadRow, kind, text, actor string) (*Note, error) {
	return w.addNotedNotify(row, kind, text, actor, "", nil)
}

// addNotedNotify is addNoted that names the session a plain note is for:
// the daemon tells it once, when idle. notify is "" for no session, and
// for any kind but a plain note. files are the images the note carries, as
// PutBlob answered them, and only a plain note has any.
func (w *wtx) addNotedNotify(row *beadRow, kind, text, actor, notify string, files []BlobRef) (*Note, error) {
	if kind != NoteNote {
		notify, files = "", nil
	}
	seq, err := w.Bucket(bNotes).NextSequence()
	if err != nil {
		return nil, err
	}
	n := Note{Bead: row.UUID, Kind: kind, Author: actor, Text: text, At: now(), Seq: seq, Via: w.viaOf(actor), Notify: notify, Files: files}
	if err := put(w.Tx, bNotes, join(row.UUID, seqKey(seq)), n); err != nil {
		return nil, err
	}
	// The bead's representation counts its notes (bd's comment_count).
	row.Notes++
	if err := w.mintBead(row, actor); err != nil {
		return nil, err
	}
	evKind := "noted"
	if kind == NoteJournal || kind == NoteHandoff {
		evKind = kind
	}
	// The note's seq lets subscriptions match mentions in this note alone.
	// "session" says whose session is told of it, when one is.
	data := map[string]any{"note": seq}
	if notify != "" {
		data["session"] = notify
	}
	if err := w.event(row.UUID, evKind, actor, data); err != nil {
		return nil, err
	}
	out := n
	out.Bead = row.ID
	return &out, nil
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
			if n.Notify != "" {
				var sess Session
				if ok, err := get(tx, bSessions, n.Notify, &sess); err != nil {
					return err
				} else {
					n.NotifyLive = ok && sess.live()
				}
			}
			out = append(out, *n)
			return nil
		})
	})
	return out, err
}

// Depot kinds. A git depot is a repository: beads work on branches in
// worktrees, start from Base and land on it, and the depot's .yardr/ is read
// from the base. A dir depot is a plain directory (documents, images, a small
// business's files): work happens in the directory itself, there is no
// base, and .yardr/ is read from the directory as it is.
const (
	DepotGit = "git"
	DepotDir = "dir"
)

// DepotsDir is the directory of a yard's home that `yardr depot add <name>
// <url>` clones into.
const DepotsDir = "depots"

type Depot struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Kind is DepotGit or DepotDir. Depots stored before kinds existed are git
	// depots (migrateDepotKinds).
	Kind string `json:"kind"`
	Base string `json:"base"`
	// Test is the command a developer runs to test the project, with sh -c
	// semantics for whoever runs it, or the path of a script in the
	// repository that does. yardr never runs it: a bead's prime prints it
	// and a script group's command gets it as YARDR_TEST. Until schema 19 a
	// depot had a check beside it, which yardr ran (migrateChecks).
	Test string `json:"test,omitempty"`
}

// IsDir reports whether the depot is a directory, not a repository.
func (r *Depot) IsDir() bool { return r.Kind == DepotDir }

// PutDepot adds or replaces a depot, and logs depot_updated with what it now
// holds: what tests a depot must not change unseen. A depot given no kind is
// a git depot.
func (s *Store) PutDepot(ctx context.Context, r Depot, actor string) error {
	if err := checkName("depot name", r.Name); err != nil {
		return err
	}
	switch r.Kind {
	case "":
		r.Kind = DepotGit
	case DepotGit:
	case DepotDir:
		if r.Base != "" {
			return fmt.Errorf("depot %s is a directory depot: it has no base (got %q)", r.Name, r.Base)
		}
	default:
		return fmt.Errorf("depot %s: kind %q is neither %s nor %s", r.Name, r.Kind, DepotGit, DepotDir)
	}
	return s.write(func(w *wtx) error {
		if err := put(w.Tx, bDepots, r.Name, r); err != nil {
			return err
		}
		return w.event("", "depot_updated", actor, map[string]any{
			"depot": r.Name, "path": r.Path, "kind": r.Kind, "base": r.Base, "test": r.Test})
	})
}

func (s *Store) Depots(ctx context.Context) ([]Depot, error) {
	out := []Depot{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bDepots, "", func(_ []byte, r *Depot) error {
			out = append(out, *r)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetDepot(ctx context.Context, name string) (*Depot, error) {
	r := new(Depot)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bDepots, name, r)
		if err == nil && !ok {
			err = fmt.Errorf("depot %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// DepotRemoval is what DeleteDepot took out of the store with the depot.
type DepotRemoval struct {
	Depot        string        `json:"depot"`
	Routes       []string      `json:"routes"`        // ids of the routes that matched --depot <name>
	FlowPointers []FlowPointer `json:"flow_pointers"` // the depot's and its types' flow pointers
}

// DeleteDepot removes a depot with the routes that match it and its flow
// pointers, and logs depot_removed. It refuses while the depot has open beads
// or live sessions, and names them: a bead whose depot is gone could not be
// routed, worked or merged. Closed beads keep the depot's name as history.
// Only the store changes: the clone, its worktrees and its .yardr/ are not
// touched.
func (s *Store) DeleteDepot(ctx context.Context, name, actor string) (*DepotRemoval, error) {
	var out *DepotRemoval
	err := s.write(func(w *wtx) error {
		// Batch reruns this closure when another call in the batch fails, so
		// what was removed starts over with each attempt.
		out = &DepotRemoval{Depot: name, Routes: []string{}, FlowPointers: []FlowPointer{}}
		if w.Bucket(bDepots).Get([]byte(name)) == nil {
			return fmt.Errorf("depot %s: %w", name, ErrNotFound)
		}
		users, err := s.depotUsers(w.Tx, name)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("depot %s is in use by %s; close the beads and end the sessions first", name, strings.Join(users, ", "))
		}
		if err := each(w.Tx, bRoutes, "", func(k []byte, r *Route) error {
			if r.Depot != nil && *r.Depot == name {
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
		// Pointer keys are <depot>\x00<type>, so the depot's own pointer and its
		// types' share the prefix; another depot whose name extends this one
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
		// The record of what was imported into the depot goes with it: its
		// routes and pointers just did.
		for _, k := range keys(w.Tx, bPacks, name+"\x00") {
			if err := w.Bucket(bPacks).Delete([]byte(k)); err != nil {
				return err
			}
		}
		if err := w.Bucket(bDepots).Delete([]byte(name)); err != nil {
			return err
		}
		ptrs := make([]string, 0, len(out.FlowPointers))
		for _, p := range out.FlowPointers {
			ptrs = append(ptrs, p.String())
		}
		return w.event("", "depot_removed", actor, map[string]any{
			"depot": name, "routes": out.Routes, "flow_pointers": ptrs})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// depotUsers names what still needs the depot: its open beads, and the live
// sessions working them.
func (s *Store) depotUsers(tx *bolt.Tx, name string) ([]string, error) {
	var out []string
	err := each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
		if r.Status != "closed" && r.Depot != nil && *r.Depot == name {
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
		} else if ok && b.Depot != nil && *b.Depot == name {
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
	At        string    `json:"at,omitempty"`
	Until     string    `json:"until,omitempty"`
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
				Revision: d.Revision, CreatedAt: d.CreatedAt, CreatedBy: ptr(d.CreatedBy), LastActor: ptr(d.LastActor), At: d.At, Until: d.Until})
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

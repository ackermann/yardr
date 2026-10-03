package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"
)

// Epics. An epic is work that spans several beads and lands on the base as
// one unit. It owns a branch, Branch(epic), and its children (beads with a
// parent edge from it: `yardr dep add <epic> <child> -k parent`) branch
// from that branch and land on it instead of the rig's base.
const (
	TypeEpic = "epic"
	// DepParent is the edge kind from an epic to each of its children.
	DepParent = "parent"
)

// Branch is the git branch bead handle is worked on. It is built from the
// handle, which never changes (a migrated bead keeps its old id as handle),
// so a bead's branch and worktree stay valid for its whole life.
func Branch(handle string) string { return "yard/" + handle }

// Family is a bead's place among epics.
type Family struct {
	// Epic is the handle of the epic the bead is a child of; "" for none.
	Epic string `json:"epic,omitempty"`
	// Base is BaseOf(bead): what its branch starts from and lands on.
	// Branch(Epic) for a child of an epic; for everything else, the epic
	// itself included, the rig's base ("" without a rig, and always "" in
	// a dir rig, which has no branches).
	Base string `json:"base,omitempty"`
	// Children are an epic's children, oldest edge first by CreatedAt, then id.
	Children []string `json:"children,omitempty"`
}

// Family returns where ref stands among epics: its parent epic, its base
// and, for an epic, its children.
func (s *Store) Family(ctx context.Context, ref string) (*Family, error) {
	var f *Family
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, ref)
		if err != nil {
			return err
		}
		f, err = familyOf(tx, row)
		return err
	})
	return f, err
}

func familyOf(tx *bolt.Tx, row *beadRow) (*Family, error) {
	h := handlesIn(tx)
	f := &Family{}
	epic, err := parentEpic(tx, row.UUID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", row.ID, err)
	}
	var r Rig
	if row.Rig != nil {
		if _, err := get(tx, bRigs, *row.Rig, &r); err != nil {
			return nil, err
		}
	}
	switch {
	case r.IsDir():
		// A dir rig has no branches: an epic only groups its children,
		// who work in the rig's directory like any bead.
		if epic != nil {
			f.Epic = epic.ID
		}
	case epic != nil:
		f.Epic, f.Base = epic.ID, Branch(epic.ID)
	default:
		f.Base = r.Base
	}
	if row.Type == TypeEpic {
		for _, d := range edges(tx, bDepFrom, row.UUID, DepParent) {
			f.Children = append(f.Children, h.of(d.To))
		}
	}
	return f, nil
}

// parentEpic is the epic bead u is a child of, nil when it has none. Only a
// parent edge from a bead of type epic counts: a parent edge from anything
// else (one imported from beads, say) leaves the bead on the rig's base, as
// it always was.
func parentEpic(tx *bolt.Tx, u string) (*beadRow, error) {
	var epic *beadRow
	for _, d := range edges(tx, bDepTo, u, DepParent) {
		row := new(beadRow)
		if ok, err := get(tx, bBeads, d.From, row); err != nil || !ok || row.Type != TypeEpic {
			continue
		}
		if epic != nil {
			// dep add refuses this; only a type changed to epic later gets here.
			return nil, fmt.Errorf("child of two epics, %s and %s; remove one parent edge", epic.ID, row.ID)
		}
		epic = row
	}
	return epic, nil
}

// epicOf is the handle of the epic bead u is a child of, for listings: ""
// for none, and also for a bead in two epics, which Family reports.
func epicOf(tx *bolt.Tx, u string) string {
	if epic, err := parentEpic(tx, u); err == nil && epic != nil {
		return epic.ID
	}
	return ""
}

// edges are bead u's edges of kind in one direction (bDepFrom: from u,
// bDepTo: to u), oldest first by CreatedAt, then by id. The index's key
// order is not that: an edge id keeps only the last 4 characters of its
// ULID's timestamp (see newEdgeID), which wrap every 2^20 ms, about 17.5
// minutes.
func edges(tx *bolt.Tx, index []byte, u, kind string) []depRow {
	var out []depRow
	for _, k := range keys(tx, index, u+"\x00") {
		var d depRow
		if ok, _ := get(tx, bDeps, k[len(u)+1:], &d); ok && d.Kind == kind {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b depRow) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// checkParent refuses a parent edge from -> to that would not make to a
// child of one open epic: from must be an epic that is not closed (a
// closed epic has landed, or was dropped; a child added now would branch
// from and land on a branch nothing integrates again), to not already a
// child of one, and both in the same rig (the child branches from the
// epic's branch in that rig's repository).
func checkParent(tx *bolt.Tx, from, to *beadRow) error {
	if from.Type != TypeEpic {
		return fmt.Errorf("%s is a %s, not an epic: only an epic can be a parent", from.ID, from.Type)
	}
	if from.Status == "closed" {
		return fmt.Errorf("epic %s is closed: a closed epic takes no more children", from.ID)
	}
	if to.Type == TypeEpic {
		return fmt.Errorf("%s is an epic: epics do not nest", to.ID)
	}
	if rigOf(from) != rigOf(to) {
		return fmt.Errorf("epic %s is in rig %q and %s in rig %q: a child must be in its epic's rig",
			from.ID, rigOf(from), to.ID, rigOf(to))
	}
	epic, err := parentEpic(tx, to.UUID)
	if err != nil {
		return fmt.Errorf("%s: %w", to.ID, err)
	}
	if epic != nil && epic.UUID != from.UUID {
		return fmt.Errorf("%s is already a child of epic %s; a bead has at most one parent epic", to.ID, epic.ID)
	}
	return nil
}

func rigOf(row *beadRow) string {
	if row.Rig == nil {
		return ""
	}
	return *row.Rig
}

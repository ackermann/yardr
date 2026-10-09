package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Trains. A train is work that spans several beads and lands on the base as
// one unit. It owns a branch, Branch(train), and its children (beads with a
// parent edge from it: `yardr dep add <train> <child> -k parent`) branch
// from that branch and land on it instead of the depot's base.
const (
	TypeTrain = "train"
	// DepParent is the edge kind from a train to each of its children.
	DepParent = "parent"
)

// Wagons. A wagon is a piece of a train that is not worth having on the base
// by itself: it lands on its train's branch with no review of its own and
// tested for what it changed alone, and the whole suite and the one review
// are the train's (docs/line.md, "Wagons"). That is only safe under a train, so a
// wagon with no parent train stays where its flow starts: checkWagon refuses
// to move or create it anywhere else, and the dispatcher starts no agent or
// script for one that got there some other way (its parent edge removed
// after it moved, or no flow governing it).
const TypeWagon = "wagon"

// typeCar is what main called a wagon between two releases, in a store of
// schema 10 to 12. No release had the word, so it is no alias: migrateWagons
// renames what such a yard stored, and flowNames finds a flow whose name
// ends in it.
const typeCar = "car"

// flowNames are the names a flow called name is looked up under: name, and
// when it ends in car or wagon, the same name with the other word. A yard
// that ran main between two releases may have a flow or a pointer named for
// a car (typeCar).
func flowNames(name string) []string {
	i := strings.LastIndexAny(name, "./")
	head, last := name[:i+1], name[i+1:]
	names := []string{name}
	switch last {
	case TypeWagon:
		names = append(names, head+typeCar)
	case typeCar:
		names = append(names, head+TypeWagon)
	}
	return names
}

// checkWagon refuses putting row, a wagon, in stage of its flow f while it is
// no child of a train. The flow's first stage is always allowed: it is
// where a wagon waits for its train, and where it is taken back to.
func checkWagon(tx *bolt.Tx, f *Flow, row *beadRow, stage string) error {
	// No flow, no stage it starts in: the dispatcher alone keeps such a
	// wagon from being worked.
	if row.Type != TypeWagon || f == nil || stage == firstStage(f) {
		return nil
	}
	const why = "a wagon lands with no review of its own, which is only safe on a train's branch"
	// A new bead has no UUID and no edges yet: it cannot be a child.
	if row.UUID == "" {
		return fmt.Errorf("a new wagon has no train yet, so it is created in %s, not %s: %s. Create it without a stage, make it a child (yardr dep add <train> <wagon> -k parent), then advance it",
			firstStage(f), stage, why)
	}
	if train, err := parentTrain(tx, row.UUID); err != nil {
		return fmt.Errorf("%s: %w", row.ID, err)
	} else if train != nil {
		return nil
	}
	return fmt.Errorf("bead %s is a wagon with no train, so it stays in %s: %s. Make it a child first (yardr dep add <train> %s -k parent), or make it a task (yardr bead update %s --type task)",
		row.ID, firstStage(f), why, row.ID, row.ID)
}

// Branch is the git branch bead handle is worked on. It is built from the
// handle, which never changes (a migrated bead keeps its old id as handle),
// so a bead's branch and worktree stay valid for its whole life.
func Branch(handle string) string { return "yard/" + handle }

// Family is a bead's place among trains.
type Family struct {
	// Train is the handle of the train the bead is a child of; "" for none.
	Train string `json:"train,omitempty"`
	// Base is BaseOf(bead): what its branch starts from and lands on.
	// Branch(Train) for a child of a train; the commit a linked yard named
	// for a bead that came from it (PeerGit.BaseCommit); for everything
	// else, the train itself included, the depot's base ("" without a depot,
	// and always "" in a dir depot, which has no branches).
	Base string `json:"base,omitempty"`
	// Children are a train's children, oldest edge first by CreatedAt, then id.
	Children []string `json:"children,omitempty"`
}

// Family returns where ref stands among trains: its parent train, its base
// and, for a train, its children.
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
	train, err := parentTrain(tx, row.UUID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", row.ID, err)
	}
	var r Depot
	if row.Depot != nil {
		if _, err := get(tx, bDepots, *row.Depot, &r); err != nil {
			return nil, err
		}
	}
	switch {
	case r.IsDir():
		// A dir depot has no branches: a train only groups its children,
		// who work in the depot's directory like any bead.
		if train != nil {
			f.Train = train.ID
		}
	case train != nil:
		f.Train, f.Base = train.ID, Branch(train.ID)
	default:
		f.Base = r.Base
		// A bead that came from a yard which said where its work starts
		// starts there: this yard's base may be anywhere by now.
		if rec, err := peerBeadOf(tx, row.UUID); err != nil {
			return nil, err
		} else if rec != nil && rec.Git != nil && rec.Git.BaseCommit != "" {
			f.Base = rec.Git.BaseCommit
		}
	}
	if row.Type == TypeTrain {
		for _, d := range edges(tx, bDepFrom, row.UUID, DepParent) {
			f.Children = append(f.Children, h.of(d.To))
		}
	}
	return f, nil
}

// parentTrain is the train bead u is a child of, nil when it has none. Only a
// parent edge from a bead of type train counts: a parent edge from anything
// else (one imported from beads, say) leaves the bead on the depot's base, as
// it always was.
func parentTrain(tx *bolt.Tx, u string) (*beadRow, error) {
	var train *beadRow
	for _, d := range edges(tx, bDepTo, u, DepParent) {
		row := new(beadRow)
		if ok, err := get(tx, bBeads, d.From, row); err != nil || !ok || row.Type != TypeTrain {
			continue
		}
		if train != nil {
			// dep add refuses this; only a type changed to train later gets here.
			return nil, fmt.Errorf("child of two trains, %s and %s; remove one parent edge", train.ID, row.ID)
		}
		train = row
	}
	return train, nil
}

// trainOf is the handle of the train bead u is a child of, for listings: ""
// for none, and also for a bead in two trains, which Family reports.
func trainOf(tx *bolt.Tx, u string) string {
	if train, err := parentTrain(tx, u); err == nil && train != nil {
		return train.ID
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
// child of one open train: from must be a train that is not closed (a
// closed train has landed, or was dropped; a child added now would branch
// from and land on a branch nothing integrates again), to not already a
// child of one, and both in the same depot (the child branches from the
// train's branch in that depot's repository).
func checkParent(tx *bolt.Tx, from, to *beadRow) error {
	if from.Type != TypeTrain {
		return fmt.Errorf("%s is a %s, not a train: only a train can be a parent", from.ID, from.Type)
	}
	if from.Status == "closed" {
		return fmt.Errorf("train %s is closed: a closed train takes no more children", from.ID)
	}
	if to.Type == TypeTrain {
		return fmt.Errorf("%s is a train: trains do not nest", to.ID)
	}
	if depotOf(from) != depotOf(to) {
		return fmt.Errorf("train %s is in depot %q and %s in depot %q: a child must be in its train's depot",
			from.ID, depotOf(from), to.ID, depotOf(to))
	}
	train, err := parentTrain(tx, to.UUID)
	if err != nil {
		return fmt.Errorf("%s: %w", to.ID, err)
	}
	if train != nil && train.UUID != from.UUID {
		return fmt.Errorf("%s is already a child of train %s; a bead has at most one parent train", to.ID, train.ID)
	}
	return nil
}

func depotOf(row *beadRow) string {
	if row.Depot == nil {
		return ""
	}
	return *row.Depot
}

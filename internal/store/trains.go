package store

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Trains. A train is work that spans several beads and lands on the base as
// one unit. It owns a branch, Branch(train), and its children (beads with a
// parent edge from it: `yardr dep add <train> <child> -k parent`) branch
// from that branch and land on it instead of the rig's base.
const (
	TypeTrain = "train"
	// DepParent is the edge kind from a train to each of its children.
	DepParent = "parent"
)

// Cars. A car is a piece of a train that is not worth having on the base
// by itself: it lands on its train's branch under the rig's light gate, with
// no review of its own, and the full gate and the one review are the
// train's (docs/pipeline.md, "Cars"). That is only safe under a train, so a
// car with no parent train stays where its flow starts: checkCar refuses
// to move or create it anywhere else, and the dispatcher starts no agent or
// script for one that got there some other way (its parent edge removed
// after it moved, or no flow governing it).
const TypeCar = "car"

// Until schema 10 a train was an epic and a car a part (migrateTrains). The
// old words are still read for this release, wherever a type, a filter or a
// flow's name is given: BeadType, whereKey, flowNames.
const (
	oldTypeTrain = "epic"
	oldTypeCar   = "part"
)

// BeadType is the bead type t names: t itself, or the type that replaced
// it when t is one an earlier release used.
func BeadType(t string) string {
	switch t {
	case oldTypeTrain:
		return TypeTrain
	case oldTypeCar:
		return TypeCar
	}
	return t
}

// oldBeadType is the type t was called until schema 10, "" when it had no
// other name.
func oldBeadType(t string) string {
	switch t {
	case TypeTrain:
		return oldTypeTrain
	case TypeCar:
		return oldTypeCar
	}
	return ""
}

// OldTrainKinds are the event kinds that said epic and part until schema
// 10, each with the kind new events carry. Events already in the log keep
// the old kind; a filter that names one means the new.
var OldTrainKinds = map[string]string{
	"epic_branched": "train_branched", "bad_epic": "bad_train", "part_without_epic": "car_without_train",
}

// mapWords is a hook's stage map (MapKey) with the bead types an earlier
// release used replaced, and whether any was. A line under the new word
// stands when both are given.
func mapWords(m map[string]string) (map[string]string, bool) {
	changed := false
	for k, state := range m {
		typ, stage, ok := strings.Cut(k, ":")
		if !ok || BeadType(typ) == typ {
			continue
		}
		if !changed {
			m, changed = maps.Clone(m), true
		}
		delete(m, k)
		if _, has := m[MapKey(BeadType(typ), stage)]; !has {
			m[MapKey(BeadType(typ), stage)] = state
		}
	}
	return m, changed
}

// whereWords is a subscription's filter with the words an earlier release
// used replaced, and whether any was: those of trains (trainWhere) and
// those of the crew (crewWhere).
func whereWords(where map[string]string) (map[string]string, bool) {
	where, trains := trainWhere(where)
	where, crew := crewWhere(where)
	return where, trains || crew
}

// trainWhere is a subscription's filter with the words trains had until
// schema 10 replaced, and whether any was: the key epic is train, type=epic
// and type=part name the types as they are now, and a kind that said epic
// or part names the kind new events carry (OldTrainKinds).
func trainWhere(where map[string]string) (map[string]string, bool) {
	out, changed := where, false
	set := func(old, k, v string) {
		if !changed {
			out, changed = maps.Clone(where), true
		}
		delete(out, old)
		out[k] = v
	}
	for k, v := range where {
		switch {
		case k == oldTypeTrain:
			if _, has := where[TypeTrain]; has {
				v = where[TypeTrain]
			}
			set(k, TypeTrain, v)
		case k == "type" && BeadType(v) != v:
			set(k, k, BeadType(v))
		case k == "kind" && OldTrainKinds[v] != "":
			set(k, k, OldTrainKinds[v])
		}
	}
	return out, changed
}

// flowNames are the names a flow called name is looked up under: name, and
// when it ends in a type that was renamed (yardr.epic, web.part, or the new
// words), the same name with the other spelling. So a rig's pointer at
// yardr.epic finds yardr.train.flow once the repository renamed its file,
// and one at yardr.train finds the yardr.epic the store still has. For this
// release only.
func flowNames(name string) []string {
	i := strings.LastIndexAny(name, "./")
	head, last := name[:i+1], name[i+1:]
	if other := oldBeadType(last); other != "" {
		return []string{name, head + other}
	}
	if other := BeadType(last); other != last {
		return []string{name, head + other}
	}
	return []string{name}
}

// checkCar refuses putting row, a car, in stage of its flow f while it is
// no child of a train. The flow's first stage is always allowed: it is
// where a car waits for its train, and where it is taken back to.
func checkCar(tx *bolt.Tx, f *Flow, row *beadRow, stage string) error {
	// No flow, no stage it starts in: the dispatcher alone keeps such a
	// car from being worked.
	if row.Type != TypeCar || f == nil || stage == firstStage(f) {
		return nil
	}
	const why = "a car lands under the light gate with no review of its own, which is only safe on a train's branch"
	// A new bead has no UUID and no edges yet: it cannot be a child.
	if row.UUID == "" {
		return fmt.Errorf("a new car has no train yet, so it is created in %s, not %s: %s. Create it without a stage, make it a child (yardr dep add <train> <car> -k parent), then advance it",
			firstStage(f), stage, why)
	}
	if train, err := parentTrain(tx, row.UUID); err != nil {
		return fmt.Errorf("%s: %w", row.ID, err)
	} else if train != nil {
		return nil
	}
	return fmt.Errorf("bead %s is a car with no train, so it stays in %s: %s. Make it a child first (yardr dep add <train> %s -k parent), or make it a task (yardr bead update %s --type task)",
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
	// else, the train itself included, the rig's base ("" without a rig,
	// and always "" in a dir rig, which has no branches).
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
	var r Rig
	if row.Rig != nil {
		if _, err := get(tx, bRigs, *row.Rig, &r); err != nil {
			return nil, err
		}
	}
	switch {
	case r.IsDir():
		// A dir rig has no branches: a train only groups its children,
		// who work in the rig's directory like any bead.
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
// else (one imported from beads, say) leaves the bead on the rig's base, as
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
// child of one, and both in the same rig (the child branches from the
// train's branch in that rig's repository).
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
	if rigOf(from) != rigOf(to) {
		return fmt.Errorf("train %s is in rig %q and %s in rig %q: a child must be in its train's rig",
			from.ID, rigOf(from), to.ID, rigOf(to))
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

func rigOf(row *beadRow) string {
	if row.Rig == nil {
		return ""
	}
	return *row.Rig
}

package store

import (
	"maps"

	bolt "go.etcd.io/bbolt"
)

// oldTypeTrain and oldTypeWagon are the bead types as a store from before
// schema 10 holds them.
const (
	oldTypeTrain = "epic"
	oldTypeWagon = "part"
)

// migrateTrains renames the bead types epic and part, as they were until
// schema 10, to train and wagon wherever the store holds one: beads, routes,
// flow pointers, hooks' stage maps, the filters of subscriptions (where
// the key epic becomes train too, and a kind that said epic or part the
// kind new events carry) and the records of imported packs. What the
// operator named stays: a flow called web.epic keeps its name, as does a
// pointer at it or a bead's pin that names it while the store has that flow
// (migrateFlowPointers). Events already logged are not rewritten.
func migrateTrains(tx *bolt.Tx, from uint64) error {
	if from >= 10 {
		return nil
	}
	return renameTypes(tx, oldBeadType, trainWhere)
}

// oldBeadType is the bead type t names in a store from before schema 10: t
// itself, or the type that replaced it.
func oldBeadType(t string) string {
	switch t {
	case oldTypeTrain:
		return TypeTrain
	case oldTypeWagon:
		return TypeWagon
	}
	return t
}

// trainWhere is a subscription's filter with the words trains had until
// schema 10 replaced, and whether any was: the key epic is train, type=epic
// and type=part name the types as they are now, and a kind that said epic
// or part names the kind new events carry (oldTrainKinds).
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
		case k == "type" && oldBeadType(v) != v:
			set(k, k, oldBeadType(v))
		case k == "kind" && oldTrainKinds[v] != "":
			set(k, k, oldTrainKinds[v])
		}
	}
	return out, changed
}

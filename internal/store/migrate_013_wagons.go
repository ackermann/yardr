package store

import (
	"maps"

	bolt "go.etcd.io/bbolt"
)

// migrateWagons renames the bead type car to wagon in a store of schema 10
// to 12: main called a wagon a car between two releases, and a yard that
// ran it then stored the word in every place migrateTrains writes a type,
// and the kind car_without_train in a subscription's filter. No release had
// the word, so nothing reads it afterwards but flowNames.
func migrateWagons(tx *bolt.Tx, from uint64) error {
	if from < 10 || from >= 13 {
		return nil
	}
	typ := func(t string) string {
		if t == typeCar {
			return TypeWagon
		}
		return t
	}
	where := func(where map[string]string) (map[string]string, bool) {
		v, changed := where["type"], false
		out := where
		if v == typeCar {
			out, changed = maps.Clone(where), true
			out["type"] = TypeWagon
		}
		if where["kind"] == typeCar+"_without_train" {
			if !changed {
				out, changed = maps.Clone(where), true
			}
			out["kind"] = TypeWagon + "_without_train"
		}
		return out, changed
	}
	return renameTypes(tx, typ, where)
}

package store

import (
	bolt "go.etcd.io/bbolt"
)

// migrateDepotKinds stamps every depot stored without a kind as a git depot,
// which every depot was before dir depots. It writes nothing once each has one.
func migrateDepotKinds(tx *bolt.Tx) error {
	var old []*Depot
	if err := each(tx, bDepots, "", func(_ []byte, r *Depot) error {
		if r.Kind == "" {
			old = append(old, r)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, r := range old {
		r.Kind = DepotGit
		if err := put(tx, bDepots, r.Name, r); err != nil {
			return err
		}
	}
	return nil
}

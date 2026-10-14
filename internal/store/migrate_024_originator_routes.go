package store

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

// migrateOriginatorRoutes gives every route from before the originator
// filter to a group that starts no agents every originator's beads: until
// schema 24 a route named no person it is for, and matched any bead. A
// script, people or another yard burn no local subscription, so they keep
// taking all. A route to agents keeps no originator, which is the
// operator's alone, as every bead then was. No revision is minted and no
// event is logged: the reading of every route is what it was.
func migrateOriginatorRoutes(tx *bolt.Tx, from uint64) error {
	if from >= 24 {
		return nil
	}
	runners := map[string]string{}
	if err := tx.Bucket(bGroups).ForEach(func(k, raw []byte) error {
		var g Group
		if err := json.Unmarshal(raw, &g); err != nil {
			return err
		}
		runners[string(k)] = g.Runner
		return nil
	}); err != nil {
		return err
	}
	type change struct {
		key string
		r   Route
	}
	var changes []change
	if err := tx.Bucket(bRoutes).ForEach(func(k, raw []byte) error {
		var r Route
		if err := json.Unmarshal(raw, &r); err != nil || r.Originator != nil {
			return err
		}
		if runners[r.Worker()] == "herdr" {
			return nil
		}
		any := OriginatorAny
		r.Originator = &any
		changes = append(changes, change{string(k), r})
		return nil
	}); err != nil {
		return err
	}
	for _, c := range changes {
		if err := put(tx, bRoutes, c.key, &c.r); err != nil {
			return err
		}
	}
	return nil
}

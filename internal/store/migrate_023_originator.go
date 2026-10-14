package store

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

// migrateOriginator gives every bead from before the originator field the
// operator as originator: until schema 23 a bead named no person it is
// for, and empty reads as the operator's, which these keep as written.
// Yards with no operator are left alone: their beads keep reading as the
// operator's. No revision is minted and no event is logged: the reading
// of every bead is what it was.
func migrateOriginator(tx *bolt.Tx, from uint64) error {
	if from >= 23 {
		return nil
	}
	operator, err := operatorIn(tx)
	if err != nil || operator == "" {
		return err
	}
	type change struct {
		key string
		row beadRow
	}
	var changes []change
	if err := tx.Bucket(bBeads).ForEach(func(k, raw []byte) error {
		var r beadRow
		if err := json.Unmarshal(raw, &r); err != nil || r.Originator != "" {
			return err
		}
		r.Originator = operator
		changes = append(changes, change{string(k), r})
		return nil
	}); err != nil {
		return err
	}
	for _, c := range changes {
		if err := put(tx, bBeads, c.key, &c.row); err != nil {
			return err
		}
	}
	return nil
}

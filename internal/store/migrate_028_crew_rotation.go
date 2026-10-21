package store

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

// migrateCrewRotation moves the forced rotation to a day (schema 28): a
// crew member whose stored max_age is 6h, the default yardr init wrote
// until this build, is moved to 24h. Rotation by context use asks past
// 60% of the window instead (ContextHandoff, unset below), so the clock
// is a failsafe for a quiet session, not the rotation itself. Any other
// max_age, and none, is kept: they were set on purpose.
func migrateCrewRotation(tx *bolt.Tx, from uint64) error {
	if from >= 28 {
		return nil
	}
	type change struct {
		key string
		r   CrewMember
	}
	var changes []change
	err := tx.Bucket(bCrew).ForEach(func(k, raw []byte) error {
		var r CrewMember
		if err := json.Unmarshal(raw, &r); err != nil || r.MaxAge != "6h" {
			return err
		}
		r.MaxAge = "24h"
		changes = append(changes, change{string(k), r})
		return nil
	})
	if err != nil {
		return err
	}
	for _, c := range changes {
		if err := put(tx, bCrew, c.key, c.r); err != nil {
			return err
		}
	}
	return nil
}

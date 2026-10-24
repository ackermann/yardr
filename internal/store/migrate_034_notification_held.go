package store

import (
	bolt "go.etcd.io/bbolt"
)

// migrateNotificationsHeld remakes every notification of a user who is not
// the operator from what its person is told of the event (schema 34): rows
// written before the hold of old approval sentences kept the sentence whole,
// naming the typed value or path the yard ran with. The remake is 31's
// (remakeNotificationsTold): operator rows untouched, pruned-event rows bare.
// What changed is WithoutHeld, which now reads an old unmarked approval of a
// setting or a source add as held exactly as if it were marked, so the remade
// rows read as the event without its sentence. The asker's old rows are remade
// told like every other non-operator row: resolving askers for history would
// add a bead-notes lookup to a migration for an edge case whose failures land
// on told anyway.
func migrateNotificationsHeld(tx *bolt.Tx, from uint64) error {
	if from >= 34 {
		return nil
	}
	return remakeNotificationsTold(tx)
}

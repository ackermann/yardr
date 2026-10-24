package store

import (
	bolt "go.etcd.io/bbolt"
)

// migrateNotificationsTakebackHeld remakes every notification of a user who
// is not the operator from what its person is told of the event (schema 33):
// rows written before the hold of old login-takeback sentences kept the
// sentence whole, carrying the tail of the login command. The remake is 31's
// (remakeNotificationsTold): operator rows untouched, hook rows untouched,
// pruned-event rows bare. What changed is WithoutHeld, which now reads an old
// unmarked login takeback as held exactly as if it were marked, so the remade
// rows read as the event without its sentence. The asker's old rows are remade
// told like every other non-operator row: resolving askers for history would
// add a bead-notes lookup to a migration for an edge case whose failures land
// on told anyway.
func migrateNotificationsTakebackHeld(tx *bolt.Tx, from uint64) error {
	if from >= 33 {
		return nil
	}
	return remakeNotificationsTold(tx)
}

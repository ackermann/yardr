package store

import (
	bolt "go.etcd.io/bbolt"
)

// migrateIndexes builds the inbox and kind indexes (schema 3) for a store
// written by a build before them, going by the stamp rather than by whether
// the buckets exist: the indexes shipped before the schema was bumped, so a
// schema-2 build without them may have appended events to a store that has
// them, and only a full rebuild makes the indexes whole again. A rebuild
// writes the same keys an ingest would, so running it twice is harmless.
func migrateIndexes(tx *bolt.Tx, from uint64) error {
	if from >= 3 || tx.Bucket(bEvents) == nil {
		return nil
	}
	for _, b := range [][]byte{bInbox, bEventsKd} {
		if tx.Bucket(b) != nil {
			if err := tx.DeleteBucket(b); err != nil {
				return err
			}
		}
		if _, err := tx.CreateBucket(b); err != nil {
			return err
		}
	}
	return each(tx, bEvents, "", func(_ []byte, e *Event) error { return index(tx, *e) })
}

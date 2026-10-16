package store

import "testing"

// The ordered list reaches the stamped schema: a new migration takes the
// next version number, and the stamp bump comes with its entry.
func TestOpenMigrationsReachSchemaVersion(t *testing.T) {
	t.Parallel()
	var max uint64
	for _, m := range openMigrations("", false, false) {
		if m.version > max {
			max = m.version
		}
	}
	if max != schemaVersion {
		t.Fatalf("highest migration version = %d, schemaVersion = %d", max, schemaVersion)
	}
}

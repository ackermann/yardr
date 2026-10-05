package store

import (
	"context"
	"path/filepath"
	"testing"
)

// A store waits for the disk on every commit unless YARDR_STORE_NOSYNC is
// exactly 1: the variable is for tests' throwaway yards, and anything else
// in it (0, which a test run uses to keep the sync, or nothing) must leave a
// real yard's store safe.
func TestOpenSyncsUnlessToldNotTo(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "true": false, "1": true} {
		t.Setenv("YARDR_"+NoSyncEnv, value)
		t.Setenv("YARD_"+NoSyncEnv, "")
		s, err := Open(filepath.Join(t.TempDir(), "yard.db"))
		if err != nil {
			t.Fatal(err)
		}
		if got := s.db.NoSync; got != want {
			t.Errorf("YARDR_%s=%q: store opened with NoSync %v, want %v", NoSyncEnv, value, got, want)
		}
		if err := s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

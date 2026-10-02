package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A backup is taken while the store serves, and opens as a store holding
// what was committed.
func TestBackupWhileServing(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	b, err := s.CreateBead(ctx, store.NewBead{Title: "kept"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := os.WriteFile(path, []byte("old backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := s.Backup(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != path || res.Head != head || res.Bytes == 0 {
		t.Fatalf("backup result = %+v, head %d", res, head)
	}
	if st, err := os.Stat(path); err != nil || st.Size() != res.Bytes || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", st, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".backup.db.tmp-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	// The store kept serving: a write after the backup is not in it.
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "later"}, "me"); err != nil {
		t.Fatal(err)
	}
	copy := storetest.Open(t, path)
	got, err := copy.GetBead(ctx, b.ID)
	if err != nil || got.Title != "kept" {
		t.Fatalf("bead in backup = %+v, %v", got, err)
	}
	if bs, _ := copy.ListBeads(ctx, store.BeadFilter{}); len(bs) != 1 {
		t.Fatalf("backup holds %d beads, want 1", len(bs))
	}

	if _, err := s.Backup(ctx, "relative.db"); err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("relative path: %v", err)
	}
}

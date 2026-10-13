package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// The dated copies of a directory are the files named as BackupName names
// one, oldest first, and nothing else that is there.
func TestDatedBackups(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 13, 14, 22, 33, 0, time.FixedZone("CEST", 2*3600))
	if got := store.BackupName(at); got != "yard-20261013T122233Z.db" {
		t.Fatalf("BackupName = %s", got)
	}
	for _, name := range []string{store.BackupName(at), store.BackupName(at.Add(-time.Hour)),
		"yard.db.before-the-move", "yard-20261013.db", "yard-20261313T122233Z.db", ".yard-20261013T122233Z.db.tmp-1", "yard-20261013T122233Z.db.bak"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.DatedBackups(dir)
	if err != nil || len(got) != 2 || !got[0].At.Equal(at.Add(-time.Hour)) || !got[1].At.Equal(at) ||
		got[1].Path != filepath.Join(dir, "yard-20261013T122233Z.db") {
		t.Fatalf("DatedBackups = %+v, %v", got, err)
	}
	if got, err := store.DatedBackups(filepath.Join(dir, "none")); err != nil || len(got) != 0 {
		t.Fatalf("a directory that is not there: %+v, %v", got, err)
	}
}

// The plan is the three settings as they are read: never unless asked,
// seven kept, in the home's backups.
func TestBackupPlanOf(t *testing.T) {
	if p := store.BackupPlanOf(map[string]string{}, "/y"); p.Every != 0 || p.Keep != 7 || p.Dir != "/y/backups" {
		t.Fatalf("unset: %+v", p)
	}
	if p := store.BackupPlanOf(map[string]string{"backup_every": "0", "backup_keep": "3", "backup_dir": "/mnt/b"}, "/y"); p.Every != 0 || p.Keep != 3 || p.Dir != "/mnt/b" {
		t.Fatalf("0: %+v", p)
	}
	if p := store.BackupPlanOf(map[string]string{"backup_every": "1d"}, "/y"); p.Every != 24*time.Hour {
		t.Fatalf("1d: %+v", p)
	}
	s := storetest.New(t)
	ctx := context.Background()
	for key, bad := range map[string]string{"backup_every": "daily", "backup_keep": "0", "backup_dir": "backups"} {
		if err := s.SetSetting(ctx, key, bad, "me"); err == nil {
			t.Errorf("%s=%s was taken", key, bad)
		}
	}
	for key, good := range map[string]string{"backup_every": "0", "backup_keep": "1", "backup_dir": "/mnt/b"} {
		if err := s.SetSetting(ctx, key, good, "me"); err != nil {
			t.Errorf("%s=%s: %v", key, good, err)
		}
	}
}

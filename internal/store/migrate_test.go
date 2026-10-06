package store_test

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"
	"yardr.dev/yardr/internal/store"
)

var indexBuckets = []string{"inbox", "events_by_kind"}

// indexKeys lists every key of each index bucket in the file at path (nil
// for a bucket that is not there).
func indexKeys(t *testing.T, path string) map[string][]string {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string][]string{}
	_ = db.View(func(tx *bolt.Tx) error {
		for _, name := range indexBuckets {
			b := tx.Bucket([]byte(name))
			if b == nil {
				continue
			}
			var keys []string
			_ = b.ForEach(func(k, _ []byte) error {
				keys = append(keys, string(k))
				return nil
			})
			out[name] = keys
		}
		return nil
	})
	return out
}

func schemaOf(t *testing.T, path string) uint64 {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v uint64
	_ = db.View(func(tx *bolt.Tx) error {
		v = binary.BigEndian.Uint64(tx.Bucket([]byte("meta")).Get([]byte("schema")))
		return nil
	})
	return v
}

// copyFile copies the store file at src to dst, so a migration runs on a
// copy and the fixture stays as it was.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A store stamped with schema 2 comes out of Open at the current schema with its
// inbox and kind indexes built, whether a build before the indexes wrote
// it (no index buckets) or a schema-2 build with the indexes did and an
// older one appended to it since (index buckets missing entries).
func TestSchema2StoreMigratesTo3(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.db")
	s, err := store.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A set-up yard, so reopening it migrates nothing but the indexes.
	if err := s.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second"} {
		if err := s.Mail(ctx, "mayor", text, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := schemaOf(t, fixture); got != 5 {
		t.Fatalf("a new store is stamped %d; want 5", got)
	}
	want := indexKeys(t, fixture)
	if len(want["inbox"]) != 2 || len(want["events_by_kind"]) == 0 {
		t.Fatalf("fixture indexes = %v", want)
	}

	cases := map[string]func(tx *bolt.Tx) error{
		"no indexes": func(tx *bolt.Tx) error {
			for _, name := range indexBuckets {
				if err := tx.DeleteBucket([]byte(name)); err != nil {
					return err
				}
			}
			return nil
		},
		"indexes missing entries": func(tx *bolt.Tx) error {
			for _, name := range indexBuckets {
				b := tx.Bucket([]byte(name))
				k, _ := b.Cursor().Last()
				if err := b.Delete(k); err != nil {
					return err
				}
			}
			return nil
		},
	}
	for name, age := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "yard.db")
			copyFile(t, fixture, path)
			db, err := bolt.Open(path, 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				if err := age(tx); err != nil {
					return err
				}
				return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 2))
			})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if got := indexKeys(t, path); slices.Equal(got["inbox"], want["inbox"]) {
				t.Fatal("the aged fixture still has its inbox index")
			}

			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			page, err := s.Inbox(ctx, "mayor", 0)
			if err != nil || page.Total != 2 {
				t.Fatalf("inbox after migration = %+v %v", page, err)
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if got := schemaOf(t, path); got != 5 {
				t.Fatalf("schema after Open = %d; want 5", got)
			}
			got := indexKeys(t, path)
			for _, name := range indexBuckets {
				if !slices.Equal(got[name], want[name]) {
					t.Errorf("%s after migration = %v\nwant %v", name, got[name], want[name])
				}
			}
			if got := schemaOf(t, fixture); got != 5 || !slices.Equal(indexKeys(t, fixture)["inbox"], want["inbox"]) {
				t.Fatal("the fixture changed; the migration ran on the copy")
			}
		})
	}
}

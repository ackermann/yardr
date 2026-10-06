package store_test

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
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
	if _, err := s.PutResident(ctx, store.Resident{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second"} {
		if err := s.Mail(ctx, "yardmaster", text, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := schemaOf(t, fixture); got != 9 {
		t.Fatalf("a new store is stamped %d; want 9", got)
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
			page, err := s.Inbox(ctx, "yardmaster", 0)
			if err != nil || page.Total != 2 {
				t.Fatalf("inbox after migration = %+v %v", page, err)
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if got := schemaOf(t, path); got != 9 {
				t.Fatalf("schema after Open = %d; want 9", got)
			}
			got := indexKeys(t, path)
			for _, name := range indexBuckets {
				if !slices.Equal(got[name], want[name]) {
					t.Errorf("%s after migration = %v\nwant %v", name, got[name], want[name])
				}
			}
			if got := schemaOf(t, fixture); got != 9 || !slices.Equal(indexKeys(t, fixture)["inbox"], want["inbox"]) {
				t.Fatal("the fixture changed; the migration ran on the copy")
			}
		})
	}
}

// bucketDump is every key and value of the named buckets of the store file
// at path, as bucket\x00key -> value.
func bucketDump(t *testing.T, path string, buckets ...string) map[string]string {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]string{}
	_ = db.View(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			_ = tx.Bucket([]byte(name)).ForEach(func(k, v []byte) error {
				out[name+"\x00"+string(k)] = string(v)
				return nil
			})
		}
		return nil
	})
	return out
}

// A store of schema 8, from when the yard's coordinating resident was its
// mayor, comes out of Open with the setting that names it under yardmaster
// and nothing else changed: the resident keeps its name and its prompt's
// path, the record of a pack that called its resident mayor stays as that
// import wrote it, and the events already logged are not rewritten. A yard
// from before the setting whose resident is called mayor gets the setting,
// since the default name is no longer that one.
func TestSchema8StoreRenamesTheMayorSetting(t *testing.T) {
	ctx := context.Background()
	kept := []string{"residents", "packs", "rigs", "users"}
	cases := []struct {
		name     string
		resident string
		setting  string // the mayor setting of the old store, "" for none
		want     string // the yardmaster setting after Open, "" for none
	}{
		{"the setting names a resident of another name", "boss", "boss", "boss"},
		{"the setting names the resident mayor", "mayor", "mayor", "mayor"},
		{"no setting, and a resident mayor", "mayor", "", "mayor"},
		{"no setting, and no resident mayor", "lead", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "yard.db")
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.PutRig(ctx, store.Rig{Name: "web", Path: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PutResident(ctx, store.Resident{Name: tc.resident, Runner: "herdr",
				Config: map[string]any{"kind": "claude", "prompt": "/home/jo/.yardr/roles/mayor.md"}}, "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.AddSubscription(ctx, tc.resident, store.Subscription{Source: "linear", Scope: "involved"}, "me"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecordPack(ctx, store.PackImport{Pack: "starter", Rig: "web", Source: "/packs/starter",
				Groups: map[string]string{"builders": "web-builders"}, Residents: map[string]string{"mayor": tc.resident},
				Objects: []store.PackObject{{Kind: "file", Name: "/home/jo/.yardr/packs/starter/roles/mayor.md"}}}, true, "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.Mail(ctx, tc.resident, "the mayor setting is next", "me"); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			// Age it: the setting as a build of schema 8 stored it, under
			// that build's stamp.
			db, err := bolt.Open(path, 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				if tc.setting != "" {
					if err := tx.Bucket([]byte("settings")).Put([]byte("mayor"), []byte(`"`+tc.setting+`"`)); err != nil {
						return err
					}
				}
				return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 8))
			})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			before := bucketDump(t, path, append(kept, "events", "settings")...)

			for _, pass := range []string{"the upgrade", "the next start"} {
				s, err = store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				all, err := s.Settings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, old := all["mayor"]; old || all["yardmaster"] != tc.want || all["default_kind"] != "claude" {
					t.Fatalf("%s: settings = %v, want yardmaster %q and no mayor", pass, all, tc.want)
				}
				if page, err := s.Inbox(ctx, tc.resident, 0); err != nil || page.Total != 1 {
					t.Fatalf("%s: %s's inbox = %+v %v", pass, tc.resident, page, err)
				}
				if err := s.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if got := schemaOf(t, path); got != 9 {
				t.Fatalf("schema after Open = %d; want 9", got)
			}
			after := bucketDump(t, path, append(kept, "events", "settings")...)
			added := 0
			for k, v := range after {
				was, ok := before[k]
				switch {
				case ok && was != v:
					t.Errorf("%q changed:\n was %s\n now %s", k, was, v)
				case !ok && k == "settings\x00yardmaster":
				case !ok && strings.HasPrefix(k, "events\x00") && strings.Contains(v, `"settings_migrated"`):
					added++
				case !ok:
					t.Errorf("%q was added: %s", k, v)
				}
			}
			for k := range before {
				if _, ok := after[k]; !ok && k != "settings\x00mayor" {
					t.Errorf("%q is gone", k)
				}
			}
			// One event says what the upgrade did, and the next start adds
			// none.
			if want := map[bool]int{true: 1, false: 0}[tc.want != ""]; added != want {
				t.Errorf("%d settings_migrated events, want %d", added, want)
			}
		})
	}
}

// The setting's old key still sets and unsets it, under the new one.
func TestTheMayorSettingIsTheYardmasterSetting(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, "mayor", "boss", "me"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.Settings(ctx); all["yardmaster"] != "boss" || len(all) != 1 {
		t.Fatalf("settings = %v, want yardmaster boss alone", all)
	}
	if err := s.SetSetting(ctx, "mayor", "", "me"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.Settings(ctx); len(all) != 0 {
		t.Fatalf("settings after the unset = %v", all)
	}
}

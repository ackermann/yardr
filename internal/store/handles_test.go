package store_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestMintedBeadsHaveUUIDAndHandle(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	var prev *store.Bead
	for i := 0; i < 20; i++ {
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Prefix: "yard"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if len(b.UUID) != 36 || b.UUID[14] != '7' {
			t.Fatalf("uuid %q is not a UUIDv7", b.UUID)
		}
		if !strings.HasPrefix(b.ID, "yard-") || len(b.ID) < len("yard-")+store.MinPrefix {
			t.Fatalf("handle %q", b.ID)
		}
		if prev != nil && b.UUID <= prev.UUID {
			t.Fatalf("uuid %s minted after %s sorts before it", b.UUID, prev.UUID)
		}
		prev = b
	}
	// Explicit ids are the handle as given; a UUID-shaped one is refused.
	if b, err := s.CreateBead(ctx, store.NewBead{Title: "j", ID: "mem-1"}, "me"); err != nil || b.ID != "mem-1" || b.UUID == "" {
		t.Fatalf("explicit: %+v %v", b, err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "u", ID: prev.UUID}, "me"); err == nil {
		t.Fatal("accepted a UUID as a handle")
	}
	// Listing follows creation order (equal priority), not handle order.
	all, err := s.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].UUID < all[i-1].UUID {
			t.Fatalf("list not in creation order at %d", i)
		}
	}
}

func TestResolveBeadReferences(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	mk := func(id string) *store.Bead {
		b, err := s.CreateBead(ctx, store.NewBead{Title: id, ID: id}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a1, a2, z := mk("pfx-abcd1"), mk("pfx-abcd2"), mk("pfx-zzzz9")
	short := mk("pfx-1")
	mk("pfx-10")

	for ref, want := range map[string]*store.Bead{
		"pfx-abcd1":              a1, // handle
		a2.UUID:                  a2, // full id
		strings.ToUpper(a2.UUID): a2,
		"pfx-zzzz":               z,     // unique prefix, 4 characters
		"pfx-1":                  short, // an exact handle wins over being a prefix of pfx-10
	} {
		got, err := s.GetBead(ctx, ref)
		if err != nil || got.UUID != want.UUID {
			t.Fatalf("%s: got %+v, %v; want %s", ref, got, err, want.ID)
		}
		if got.ID != want.ID {
			t.Fatalf("%s: returned id %s, want the handle %s", ref, got.ID, want.ID)
		}
	}

	_, err := s.GetBead(ctx, "pfx-abcd")
	if !errors.Is(err, store.ErrAmbiguous) || !strings.Contains(err.Error(), "pfx-abcd1") ||
		!strings.Contains(err.Error(), "pfx-abcd2") {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	_, err = s.GetBead(ctx, "pfx-zzz") // unique, but too short to match on
	if err == nil || errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "at least 4") {
		t.Fatalf("too short prefix: %v", err)
	}
	for _, ref := range []string{"pfx-qqqq", "zzzz9", "", "00000000-0000-7000-8000-000000000000"} {
		if _, err := s.GetBead(ctx, ref); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%q: %v", ref, err)
		}
	}

	// Every command taking a bead resolves it the same way, and returns
	// handles, whatever form came in.
	if err := s.AddDep(ctx, "pfx-zzzz", a1.UUID, "blocks", "me"); err != nil {
		t.Fatal(err)
	}
	deps, err := s.Deps(ctx, "pfx-abcd1")
	if err != nil || len(deps) != 1 || deps[0].From != z.ID || deps[0].To != a1.ID {
		t.Fatalf("deps = %+v %v", deps, err)
	}
	if bl, err := s.OpenBlockers(ctx, a1.UUID); err != nil || !slices.Equal(bl, []string{z.ID}) {
		t.Fatalf("blockers = %v %v", bl, err)
	}
	if err := s.AddNote(ctx, "pfx-zzzz", "n", "me"); err != nil {
		t.Fatal(err)
	}
	if ns, err := s.Notes(ctx, z.UUID); err != nil || len(ns) != 1 || ns[0].Bead != z.ID {
		t.Fatalf("notes = %+v %v", ns, err)
	}
	evs, err := s.Events(ctx, "pfx-zzzz", 0)
	if err != nil || len(evs) == 0 {
		t.Fatalf("events = %v %v", evs, err)
	}
	for _, e := range evs {
		if e.Bead == nil || *e.Bead != z.ID {
			t.Fatalf("event names bead %v, want %s", e.Bead, z.ID)
		}
	}
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, "pfx-zzzz", "g", "new", "me")
	if err != nil || sess.Bead != z.ID {
		t.Fatalf("claim = %+v %v", sess, err)
	}
	if got, err := s.GetSession(ctx, sess.ID); err != nil || got.Bead != z.ID {
		t.Fatalf("session = %+v %v", got, err)
	}
	if _, err := s.Advance(ctx, "pfx-abcd", "x", store.Expect{}, "me"); !errors.Is(err, store.ErrAmbiguous) {
		t.Fatalf("advance on an ambiguous prefix: %v", err)
	}
}

// A store from before UUIDs is refused, as it is: the migration from it
// went after v0.4.0, and the migrations that are left would stamp a store
// whose beads no handle or UUID finds.
func TestCounterIDStoreRefusesToOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("beads"))
		if err != nil {
			return err
		}
		return b.Put([]byte("yard-1"), []byte(`{"id":"yard-1","title":"first","type":"task","stage":"new","status":"open"}`))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.Open(path)
	if !errors.Is(err, store.ErrCounterIDs) || !strings.Contains(err.Error(), "v0.4.0") {
		t.Fatalf("Open = %v; want ErrCounterIDs naming the release that migrates it", err)
	}
	db, err = bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("meta")) != nil || tx.Bucket([]byte("beads")).Get([]byte("yard-1")) == nil {
			t.Error("the refused store was written to")
		}
		return nil
	})
}

func TestNewerSchemaStoreRefusesToOpen(t *testing.T) {
	// An older yardr (a downgrade) must not run its migrations over a store
	// a newer one wrote (yard-r4z9).
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stamped uint64
	err = db.Update(func(tx *bolt.Tx) error {
		m := tx.Bucket([]byte("meta"))
		stamped = binary.BigEndian.Uint64(m.Get([]byte("schema")))
		return m.Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, stamped+1))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if stamped == 0 {
		t.Fatal("a new store was not stamped with its schema")
	}

	_, err = store.Open(path)
	if !errors.Is(err, store.ErrNewerSchema) || !strings.Contains(err.Error(), fmt.Sprintf("schema %d", stamped+1)) {
		t.Fatalf("Open = %v; want ErrNewerSchema naming the schema", err)
	}
	db, err = bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.View(func(tx *bolt.Tx) error {
		if v := binary.BigEndian.Uint64(tx.Bucket([]byte("meta")).Get([]byte("schema"))); v != stamped+1 {
			t.Errorf("refused store's schema rewritten to %d", v)
		}
		return nil
	})
}

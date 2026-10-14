package store_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
)

// A store from before the originator filter gains any on its routes to
// groups that start no agents; its routes to agents keep no originator,
// which is the operator's alone, as every bead then was.
func TestSchema23StoreGainsRouteOriginators(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "humans", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	agent, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	manual, err := s.AddRoute(ctx, store.Route{Stage: "decide", Group: "humans"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// The settings migration is not this test's: mark the yard set up so
	// the reopen logs nothing but what the route migration would.
	if err := s.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Age it: the routes as a build of schema 23 stored them, under that
	// build's stamp.
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		type old struct {
			k, v []byte
		}
		var olds []old
		routes := tx.Bucket([]byte("routes"))
		if err := routes.ForEach(func(k, raw []byte) error {
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			delete(row, "originator")
			v, err := json.Marshal(row)
			if err != nil {
				return err
			}
			olds = append(olds, old{append([]byte(nil), k...), v})
			return nil
		}); err != nil {
			return err
		}
		for _, o := range olds {
			if err := routes.Put(o.k, o.v); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 23))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		routes, err := s.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]store.Route{}
		for _, r := range routes {
			byID[r.ID] = r
		}
		if r := byID[agent.ID]; r.Originator != nil {
			t.Fatalf("%s: the agent route carries %+v, want none", pass, r.Originator)
		}
		if r := byID[manual.ID]; r.Originator == nil || *r.Originator != store.OriginatorAny {
			t.Fatalf("%s: the manual route carries %+v, want any", pass, r.Originator)
		}
		headAfter, _ := s.Head(ctx)
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if got := schemaOf(t, path); got != 24 {
			t.Fatalf("%s: schema = %d; want 24", pass, got)
		}
		if headAfter != head {
			t.Fatalf("%s logged %d events", pass, headAfter-head)
		}
	}
}

package store_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
)

// A store from before the named order holds a priority on every route; it
// opens with the same routes, under the same ids, and no priority. Of two
// routes of one scope and key, which the old ranking kept apart by the
// number, the one that never won is removed and told as route_removed:
// the higher priority keeps its route whatever the ids say.
func TestSchema34StoreDropsRoutePriorities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"builders", "spare"} {
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	p0, claude := 0, "claude"
	priority := map[string]int{}
	add := func(r store.Route, pri int) string {
		t.Helper()
		saved, err := s.AddRoute(ctx, r, "jo")
		if err != nil {
			t.Fatal(err)
		}
		priority[saved.ID] = pri
		return saved.ID
	}
	reserve := add(store.Route{Stage: "new", BeadPriority: &p0, Group: "builders", Note: "P0 reserve"}, 7)
	named := add(store.Route{Stage: "new", Label: &claude, Group: "builders"}, 4)
	plain := add(store.Route{Stage: "new", Group: "builders"}, 0)
	ownLow := add(store.Route{Stage: "new", Depot: routePtr("web"), Group: "builders"}, 2)
	// The settings migration is not this test's: mark the yard set up so
	// the reopen logs nothing but what the route migration would.
	if err := s.SetSetting(ctx, "default_kind", "claude", "jo"); err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Age it: each route with the priority a build of schema 34 stored,
	// and two the add would now refuse: a standby beside the plain
	// default, and an own route of web's key at a higher priority under
	// an id that sorts after the one it outranked.
	standby, ownHigh := "0000standby", "zzzzownhigh"
	priority[standby], priority[ownHigh] = -1, 5
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		routes := tx.Bucket([]byte("routes"))
		rows := map[string]map[string]any{
			standby: {"id": standby, "stage": "new", "group": "spare"},
			ownHigh: {"id": ownHigh, "stage": "new", "depot": "web", "group": "spare"},
		}
		if err := routes.ForEach(func(k, raw []byte) error {
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			rows[string(k)] = row
			return nil
		}); err != nil {
			return err
		}
		for id, row := range rows {
			row["priority"] = priority[id]
			v, err := json.Marshal(row)
			if err != nil {
				return err
			}
			if err := routes.Put([]byte(id), v); err != nil {
				return err
			}
		}
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, 34))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{reserve, named, plain, ownHigh}
	slices.Sort(want)
	for _, pass := range []string{"the upgrade", "the next start"} {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		routes, err := s.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range routes {
			ids = append(ids, r.ID)
			if r.ID == reserve && (r.BeadPriority == nil || *r.BeadPriority != 0 || r.Note != "P0 reserve") {
				t.Errorf("%s: the reserve route reads %+v, want its filter and note kept", pass, r)
			}
		}
		slices.Sort(ids)
		if !slices.Equal(ids, want) {
			t.Errorf("%s: routes %v, want %v: the old winner of each key kept, the standby and the outranked own route gone", pass, ids, want)
		}
		removed, err := s.EventsOfKind(ctx, "route_removed", 10)
		if err != nil {
			t.Fatal(err)
		}
		gone := map[string]string{}
		for _, e := range removed {
			if e.Actor != "yard" {
				t.Errorf("%s: route_removed by %s, want yard", pass, e.Actor)
			}
			reason, _ := e.Data["reason"].(string)
			gone[e.Data["id"].(string)] = reason
		}
		if len(gone) != 2 || !strings.Contains(gone[standby], "route "+plain+" takes the same beads") ||
			!strings.Contains(gone[ownLow], "route "+ownHigh+" takes the same beads") {
			t.Errorf("%s: route_removed = %v, want the standby set aside for %s and %s for %s", pass, gone, plain, ownLow, ownHigh)
		}
		headAfter, _ := s.Head(ctx)
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if got := schemaOf(t, path); got != 36 {
			t.Fatalf("%s: schema = %d; want 36", pass, got)
		}
		if headAfter != head+2 {
			t.Errorf("%s logged %d events, want the two removals", pass, headAfter-head)
		}
		for k, v := range bucketDump(t, path, "routes") {
			if strings.Contains(v, `"priority"`) {
				t.Errorf("%s: route %s is stored as %s, still carrying a priority", pass, k, v)
			}
		}
	}
}

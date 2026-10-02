package storetest

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// LegacyCreated is when the first bead of LegacyDB was created; the others
// follow at the offsets noted there.
var LegacyCreated = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// LegacyDB writes a store file in the layout before beads had UUIDs:
// beads keyed by counter ids minted from the counters bucket, and every
// record naming beads by those ids. It holds
//
//   - rig yard; beads yard-1 (+0), mayor-journal (+30m, the journal of
//     resident mayor), yard-2 (+1h) and yard-10 (+2h); counters yard=10
//   - edge dep1: yard-1 blocks yard-2
//   - one note on yard-2 ("hello", seq 1)
//   - session s1 of yard-2, ended and stopped, with a worktree
//   - events 1..5: created yard-1, created yard-2, dep_added yard-2,
//     noted yard-2, and mail with no bead
//   - a retained version of yard-1
func LegacyDB(t testing.TB, path string) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := func(d time.Duration) string { return LegacyCreated.Add(d).Format(time.RFC3339Nano) }
	seq := func(n uint64) string { return string(binary.BigEndian.AppendUint64(nil, n)) }
	bead := func(id, title, typ string, created time.Duration, notes int) map[string]any {
		b := map[string]any{"id": id, "title": title, "type": typ, "stage": "new", "status": "open",
			"priority": 2, "labels": []string{}, "created_at": at(created), "updated_at": at(created),
			"created_by": "me", "revision": "01REV" + id, "last_actor": "me", "notes": notes}
		if typ == "journal" {
			b["stage"] = "journal"
		} else {
			b["rig"] = "yard"
		}
		return b
	}
	event := func(n uint64, bead, kind string, data map[string]any) map[string]any {
		e := map[string]any{"seq": n, "id": fmt.Sprint(n), "kind": kind, "actor": "me",
			"data": data, "at": at(time.Duration(n) * time.Minute)}
		if bead != "" {
			e["bead"] = bead
		}
		return e
	}
	put := map[string]map[string]any{
		"rigs": {"yard": map[string]any{"name": "yard", "path": "/tmp/yard", "base": "origin/main"}},
		"beads": {
			"yard-1":        bead("yard-1", "first", "task", 0, 0),
			"mayor-journal": bead("mayor-journal", "Journal of mayor", "journal", 30*time.Minute, 0),
			"yard-2":        bead("yard-2", "second", "task", time.Hour, 1),
			"yard-10":       bead("yard-10", "tenth", "task", 2*time.Hour, 0),
		},
		"counters": {"yard": 10},
		"deps": {"dep1": map[string]any{"id": "dep1", "from": "yard-1", "to": "yard-2", "kind": "blocks",
			"created_at": at(time.Hour), "created_by": "me", "revision": "01REVDEP1", "last_actor": "me"}},
		"dep_from": {"yard-1\x00dep1": nil},
		"dep_to":   {"yard-2\x00dep1": nil},
		"notes": {"yard-2\x00" + seq(1): map[string]any{"bead": "yard-2", "author": "me", "text": "hello",
			"at": at(time.Hour), "seq": 1}},
		"sessions": {"s1": map[string]any{"id": "s1", "bead": "yard-2", "group": "g", "stage": "new",
			"state": "done", "stopped": true, "handle": map[string]any{"worktree": "/tmp/yard/yard-2"},
			"started_at": at(time.Hour)}},
		"events": {
			seq(1): event(1, "yard-1", "created", map[string]any{"title": "first"}),
			seq(2): event(2, "yard-2", "created", map[string]any{"title": "second"}),
			seq(3): event(3, "yard-2", "dep_added", map[string]any{"from": "yard-1", "kind": "blocks"}),
			seq(4): event(4, "yard-2", "noted", nil),
			seq(5): event(5, "", "mail", map[string]any{"to": "mayor", "text": "hi"}),
		},
		"events_by_bead": {"yard-1\x00" + seq(1): nil, "yard-2\x00" + seq(2): nil, "yard-2\x00" + seq(3): nil,
			"yard-2\x00" + seq(4): nil},
		"versions": {"bead/yard-1\x00" + seq(1): map[string]any{"revision": "01REVyard-1", "actor": "me",
			"at": at(0), "snapshot": map[string]any{"id": "yard-1"}}},
		"residents": {"mayor": map[string]any{"name": "mayor", "runner": "manual", "config": map[string]any{},
			"dir": "/tmp", "on": []string{}, "enabled": true, "journal": "mayor-journal", "state": "stopped"}},
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for name, kvs := range put {
			b, err := tx.CreateBucket([]byte(name))
			if err != nil {
				return err
			}
			for k, v := range kvs {
				raw := []byte{}
				if v != nil {
					if raw, err = json.Marshal(v); err != nil {
						return err
					}
				}
				if err := b.Put([]byte(k), raw); err != nil {
					return err
				}
			}
			if err := b.SetSequence(uint64(len(kvs))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

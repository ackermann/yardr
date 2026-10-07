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
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}); err != nil {
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

// A store from before UUIDs is migrated once on open, after a backup: each
// bead keeps its counter id as its handle (so its branch is unchanged),
// gets a UUIDv7 from its created_at, and every reference survives.
func TestMigrateCounterIDs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	storetest.LegacyDB(t, path)
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".pre-uuid-*")
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}

	b1, err := s.GetBead(ctx, "yard-1")
	if err != nil || b1.ID != "yard-1" || b1.Title != "first" {
		t.Fatalf("yard-1 = %+v %v", b1, err)
	}
	if ms := uuidMillis(t, b1.UUID); ms != storetest.LegacyCreated.UnixMilli() {
		t.Fatalf("uuid time %d, want created_at %d", ms, storetest.LegacyCreated.UnixMilli())
	}
	if got, err := s.GetBead(ctx, b1.UUID); err != nil || got.ID != "yard-1" {
		t.Fatalf("by uuid: %+v %v", got, err)
	}
	all, err := s.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil || !slices.Equal(ids(all), []string{"yard-1", "yard-2", "yard-10"}) {
		t.Fatalf("beads in creation order = %v %v", ids(all), err)
	}
	// The open index is filled by the keys the migration gave the beads.
	if index, walk := s.OpenIndex(), s.OpenWalk(); len(index) == 0 || !slices.Equal(index, walk) {
		t.Fatalf("open index after the migration = %v, a walk finds %v", index, walk)
	}
	if open, err := s.ListBeads(ctx, store.BeadFilter{}); err != nil || !slices.Equal(ids(open), ids(all)) {
		t.Fatalf("open beads = %v %v, want %v", ids(open), err, ids(all))
	}

	deps, err := s.Deps(ctx, "yard-2")
	if err != nil || len(deps) != 1 || deps[0].From != "yard-1" || deps[0].To != "yard-2" || deps[0].Kind != "blocks" {
		t.Fatalf("deps = %+v %v", deps, err)
	}
	ready, _ := s.Ready(ctx)
	if !slices.Equal(ids(ready), []string{"yard-1", "yard-10"}) {
		t.Fatalf("ready = %v (yard-2 is blocked by yard-1)", ids(ready))
	}
	notes, err := s.Notes(ctx, "yard-2")
	if err != nil || len(notes) != 1 || notes[0].Text != "hello" || notes[0].Bead != "yard-2" {
		t.Fatalf("notes = %+v %v", notes, err)
	}
	if err := s.AddNote(ctx, "yard-2", "after", "me"); err != nil {
		t.Fatal(err)
	}
	if notes, _ = s.Notes(ctx, "yard-2"); len(notes) != 2 || notes[1].Text != "after" {
		t.Fatalf("notes after adding = %+v", notes)
	}
	sessions, err := s.Sessions(ctx, true)
	if err != nil || len(sessions) != 1 || sessions[0].Bead != "yard-2" {
		t.Fatalf("sessions = %+v %v", sessions, err)
	}
	// The session's indexes are filled too: it names a pane, and its
	// worktree waits for the bead to close.
	if pane, pending := s.PaneIndex(), s.PendingIndex(); !slices.Equal(pane, []string{"4242\x00s1"}) || !slices.Equal(pending, []string{"s1"}) {
		t.Fatalf("after the migration the pane index = %q, the pending index = %q", pane, pending)
	}
	if bead, start := s.SessionOrderIndex(); len(bead) != 1 || len(start) != 1 {
		t.Fatalf("after the migration the index by bead = %q, by start = %q, want the session in each", bead, start)
	}
	if own, err := s.ListSessions(ctx, store.SessionFilter{All: true, Bead: "yard-2"}); err != nil || len(own) != 1 || own[0].ID != "s1" {
		t.Fatalf("sessions of yard-2 after the migration = %+v %v, want s1", own, err)
	}
	evs, err := s.Events(ctx, "yard-2", 0)
	if err != nil || len(evs) != 4 { // created, dep_added, noted, noted
		t.Fatalf("events = %+v %v", evs, err)
	}
	for _, e := range evs {
		if *e.Bead != "yard-2" {
			t.Fatalf("event %d names %s", e.Seq, *e.Bead)
		}
	}
	if evs[1].Kind != "dep_added" || evs[1].Data["from"] != "yard-1" {
		t.Fatalf("dep_added event = %+v", evs[1])
	}
	allEvents, err := s.Events(ctx, "", 0)
	// The legacy yard has a depot and no default_kind: opening it also wrote
	// settings_migrated (yard-w6np), and a second one for the crew member
	// mayor, whom the migrations by the stamp made its yardmaster.
	if err != nil || len(allEvents) != 8 || allEvents[4].Kind != "mail" || allEvents[4].Bead != nil ||
		allEvents[5].Kind != "settings_migrated" || allEvents[6].Data[store.YardmasterSetting] != "mayor" {
		t.Fatalf("global event lost during migration: %+v %v", allEvents, err)
	}
	// The mail from before is in the inbox of whom it was sent to: the
	// index it is read through is built on this open.
	if page, err := s.Inbox(ctx, "mayor", 10); err != nil || len(page.Events) != 1 || page.Events[0].Seq != 5 {
		t.Fatalf("mayor's inbox after the migration = %+v %v, want the mail (event 5)", page, err)
	}
	// The workspace of the closed bead is still found through its session.
	if _, err := s.CloseBead(ctx, "yard-2", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if ws, err := s.ClosedWithWorkspace(ctx); err != nil || len(ws) != 1 || ws[0].ID != "s1" {
		t.Fatalf("closed with workspace = %+v %v", ws, err)
	}
	// The crew member's journal (an explicit id) still works.
	if err := s.Journal(ctx, "mayor", store.NoteJournal, "still here", "mayor"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Notes(ctx, "mayor-journal"); len(n) != 1 {
		t.Fatalf("journal notes = %+v", n)
	}
	// New beads get random handles, not the next counter, and sort after
	// the migrated ones.
	nb, err := s.CreateBead(ctx, store.NewBead{Title: "new", Depot: "yard"}, "me")
	if err != nil || nb.ID == "yard-11" || !strings.HasPrefix(nb.ID, "yard-") || nb.UUID <= b1.UUID {
		t.Fatalf("new bead = %+v %v", nb, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Migration runs once: a second run would mint new UUIDs.
	s2 := storetest.Open(t, path)
	if again, err := s2.GetBead(ctx, "yard-1"); err != nil || again.UUID != b1.UUID {
		t.Fatalf("yard-1 after reopening = %+v %v", again, err)
	}

	// The backup is the old store as it was.
	db, err := bolt.Open(backups[0], 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("counters")) == nil || tx.Bucket([]byte("beads")).Get([]byte("yard-1")) == nil {
			t.Fatal("backup is not the counter-id store")
		}
		return nil
	})
}

// legacyEdit changes the file LegacyDB wrote before it is opened: fn gets
// the bucket of that name, made when the file has none.
func legacyEdit(t *testing.T, path, bucket string, fn func(b *bolt.Bucket) error) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return err
		}
		return fn(b)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A session that was running in a store from before UUIDs is in the live
// index after the migration: the migration makes the index's bucket, so
// Open builds none, and a session in no index is counted by no group's
// limit (yardr-bw2k).
func TestMigrateCounterIDsIndexesLiveSessions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	storetest.LegacyDB(t, path)
	legacyEdit(t, path, "sessions", func(b *bolt.Bucket) error {
		return b.Put([]byte("s2"), []byte(`{"id":"s2","bead":"yard-1","group":"g","stage":"new","state":"running",`+
			`"started_at":"2026-09-01T13:00:00Z"}`))
	})
	s := storetest.Open(t, path)
	live, err := s.Sessions(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, sess := range live {
		want = append(want, sess.ID)
	}
	if index := s.LiveIndex(); !slices.Equal(index, []string{"s2"}) || !slices.Equal(index, want) {
		t.Fatalf("live index after the migration = %v, the live sessions = %v, want s2", index, want)
	}
	all, err := s.Sessions(ctx, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("sessions = %+v %v", all, err)
	}
}

// A store from before UUIDs takes the migrations that go by the schema
// stamp too, as a store that was opened by every build in turn did: the
// words an earlier release stored are the ones this one reads (yardr-bw2k).
func TestMigrateCounterIDsRunsTheStampedMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	storetest.LegacyDB(t, path)
	legacyEdit(t, path, "settings", func(b *bolt.Bucket) error {
		return b.Put([]byte("mayor"), []byte(`"boss"`))
	})
	legacyEdit(t, path, "beads", func(b *bolt.Bucket) error {
		raw := strings.Replace(string(b.Get([]byte("yard-10"))), `"type":"task"`, `"type":"epic"`, 1)
		if err := b.Put([]byte("yard-10"), []byte(raw)); err != nil {
			return err
		}
		raw = strings.Replace(string(b.Get([]byte("mayor-journal"))), `"labels":[]`, `"labels":["resident:mayor"]`, 1)
		return b.Put([]byte("mayor-journal"), []byte(raw))
	})
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, old := settings["mayor"]; old || settings[store.SettingKey("mayor")] != "boss" || store.SettingKey("mayor") == "mayor" {
		t.Fatalf("settings after the migration = %v, want %s=boss and no mayor", settings, store.SettingKey("mayor"))
	}
	b, err := s.GetBead(ctx, "yard-10")
	if err != nil || b.Type != store.BeadType("epic") || b.Type == "epic" {
		t.Fatalf("yard-10 after the migration = %+v %v, want type %s", b, err, store.BeadType("epic"))
	}
	j, err := s.GetBead(ctx, "mayor-journal")
	if want := []string{store.CrewLabel("resident:mayor")}; err != nil || !slices.Equal(j.Labels, want) || want[0] == "resident:mayor" {
		t.Fatalf("the journal after the migration = %+v %v, want the labels %v", j, err, want)
	}
	// Each of the migrations ran once, and none again on the next open.
	evs, err := s.Events(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	types := 0
	for _, e := range evs {
		if e.Kind == "types_migrated" {
			types++
		}
	}
	if types != 1 {
		t.Fatalf("%d types_migrated events, want 1: %+v", types, evs)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s2 := storetest.Open(t, path)
	if again, err := s2.Events(ctx, "", 0); err != nil || len(again) != len(evs) {
		t.Fatalf("events after reopening = %d %v, want %d", len(again), err, len(evs))
	}
}

func uuidMillis(t *testing.T, u string) int64 {
	t.Helper()
	h := strings.ReplaceAll(u, "-", "")[:12]
	var ms int64
	for _, c := range h {
		ms = ms<<4 | int64(strings.IndexRune("0123456789abcdef", c))
	}
	return ms
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

package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mckean/yard/internal/store"
	"github.com/mckean/yard/internal/storetest"
	bolt "go.etcd.io/bbolt"
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
	if _, err := s.Advance(ctx, "pfx-abcd", "x", "me"); !errors.Is(err, store.ErrAmbiguous) {
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
	// The workspace of the closed bead is still found through its session.
	if _, err := s.CloseBead(ctx, "yard-2", "done", "me"); err != nil {
		t.Fatal(err)
	}
	if ws, err := s.ClosedWithWorkspace(ctx); err != nil || len(ws) != 1 || ws[0].ID != "s1" {
		t.Fatalf("closed with workspace = %+v %v", ws, err)
	}
	// The resident's journal (an explicit id) still works.
	if err := s.Journal(ctx, "mayor", store.NoteJournal, "still here", "mayor"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Notes(ctx, "mayor-journal"); len(n) != 1 {
		t.Fatalf("journal notes = %+v", n)
	}
	// New beads get random handles, not the next counter, and sort after
	// the migrated ones.
	nb, err := s.CreateBead(ctx, store.NewBead{Title: "new", Rig: "yard"}, "me")
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

func uuidMillis(t *testing.T, u string) int64 {
	t.Helper()
	h := strings.ReplaceAll(u, "-", "")[:12]
	var ms int64
	for _, c := range h {
		ms = ms<<4 | int64(strings.IndexRune("0123456789abcdef", c))
	}
	return ms
}

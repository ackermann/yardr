package store_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// ageBeads rewrites the beads of the store file at path, which no process
// may have open, with old, and stamps the file with schema: the beads as a
// build of that schema stored them, for the next open to migrate. It
// returns how many beads it changed.
func ageBeads(t *testing.T, path string, schema uint64, old *strings.Replacer) int {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := 0
	err = db.Update(func(tx *bolt.Tx) error {
		b, rows := tx.Bucket([]byte("beads")), map[string]string{}
		_ = b.ForEach(func(k, v []byte) error {
			if aged := old.Replace(string(v)); aged != string(v) {
				rows[string(k)] = aged
			}
			return nil
		})
		for k, v := range rows {
			if err := b.Put([]byte(k), []byte(v)); err != nil {
				return err
			}
		}
		n = len(rows)
		return tx.Bucket([]byte("meta")).Put([]byte("schema"), binary.BigEndian.AppendUint64(nil, schema))
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The index of open beads holds exactly the beads a walk of every bead
// finds open, after each of a few hundred random writes: creates (with
// edges, in a flow and in none, memories), moves, a flow's terminal stage,
// closes, reopens, holds, notes, edges, claims and their sessions' ends,
// and the two migrations that rewrite the beads bucket without minting (the
// depot's word at schema 11, the crew's at schema 10). Beads are never
// deleted, so there is no delete to drive.
func TestOpenIndexMatchesAWalkAfterEveryWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := store.Open(path)
	must(err)
	defer func() { _ = s.Close(ctx) }()
	must(s.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotDir, Path: t.TempDir()}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 1000}))
	f, err := store.ParseFlow("new -> review on done\nreview -> done on done\ndone terminal\n")
	must(err)
	f.Name = "web"
	_, err = s.PutFlow(ctx, *f, "me")
	must(err)
	must(s.SetFlowPointer(ctx, "web", "", "web", "me"))
	_, err = s.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "manual"}, "me")
	must(err)

	const seed = 1
	rng := rand.New(rand.NewSource(seed))
	var beads, sessions []string
	stages := map[string]string{} // of the beads no flow governs
	pick := func() string {
		if len(beads) == 0 {
			return "none"
		}
		return beads[rng.Intn(len(beads))]
	}
	made := func(b *store.Bead, err error) error {
		if err == nil {
			beads = append(beads, b.ID)
		}
		return err
	}
	changed := func(_ *store.Bead, err error) error { return err }
	ops := []struct {
		name string
		do   func() error
	}{
		{"create", func() error {
			b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Stage: "new", Priority: rng.Intn(4)}, "me")
			if err == nil {
				stages[b.ID] = "new"
			}
			return made(b, err)
		}},
		{"create in a flow", func() error {
			return made(s.CreateBead(ctx, store.NewBead{Title: "t", Depot: "web"}, "me"))
		}},
		{"create blocked", func() error {
			return made(s.CreateBead(ctx, store.NewBead{Title: "t", Depot: "web", BlockedBy: []string{pick()}, DiscoveredFrom: pick()}, "me"))
		}},
		{"remember", func() error {
			return made(s.Remember(ctx, store.NewMemory{Title: "m", Scope: "crew:boss"}, "boss"))
		}},
		{"advance", func() error {
			id := pick()
			if stages[id] == "" {
				return fmt.Errorf("a flow governs %s", id)
			}
			to := []string{"new", "review", "decide"}[rng.Intn(3)]
			return changed(s.Advance(ctx, id, to, store.Expect{}, "me"))
		}},
		{"done", func() error { return changed(s.Done(ctx, pick(), "", "a note with it", store.Expect{}, "me")) }},
		{"close", func() error { return changed(s.CloseBead(ctx, pick(), "enough", store.Expect{}, "me")) }},
		{"reopen", func() error { return changed(s.Reopen(ctx, pick(), store.Expect{}, "me")) }},
		{"hold", func() error { return changed(s.SetHold(ctx, pick(), true, store.Expect{}, "me")) }},
		{"unhold", func() error { return changed(s.SetHold(ctx, pick(), false, store.Expect{}, "me")) }},
		{"update", func() error {
			p := rng.Intn(4)
			return changed(s.UpdateBead(ctx, pick(), store.BeadPatch{Priority: &p, AddLabels: []string{"l"}}, store.Expect{}, "me"))
		}},
		{"note", func() error { return s.AddNote(ctx, pick(), "a note", "me") }},
		{"journal", func() error { return s.Journal(ctx, "boss", store.NoteJournal, "a turn", "boss") }},
		{"dep", func() error { return s.AddDep(ctx, pick(), pick(), "blocks", "me") }},
		{"undep", func() error { return s.RemoveDep(ctx, pick(), pick(), "blocks", "me") }},
		{"claim", func() error {
			b, err := s.GetBead(ctx, pick())
			if err != nil {
				return err
			}
			sess, err := s.Claim(ctx, b.ID, "g", b.Stage, "yard")
			if err == nil {
				sessions = append(sessions, sess.ID)
			}
			return err
		}},
		{"release", func() error { return changed(s.Release(ctx, pick(), "back", store.Expect{}, "me")) }},
		{"session died", func() error {
			if len(sessions) == 0 {
				return fmt.Errorf("no session")
			}
			return s.EndSession(ctx, sessions[rng.Intn(len(sessions))], "died", "gone")
		}},
	}
	// reopenAs closes the store, ages its beads to schema and opens it
	// again, which migrates them back.
	reopenAs := func(schema uint64, kind string, old *strings.Replacer) {
		t.Helper()
		must(s.Close(ctx))
		aged := ageBeads(t, path, schema, old)
		if aged == 0 {
			t.Fatalf("no bead to age to schema %d", schema)
		}
		s, err = store.Open(path)
		must(err)
		evs, err := s.Events(ctx, "", 0)
		must(err)
		i := slices.IndexFunc(evs, func(e store.Event) bool { return e.Kind == kind })
		if i < 0 || evs[i].Data["beads"] != float64(aged) {
			t.Fatalf("opening the store aged to schema %d: %d beads aged, %s says %v", schema, aged, kind, evs)
		}
	}
	same := func(after string) {
		t.Helper()
		index, walk := s.OpenIndex(), s.OpenWalk()
		if !slices.Equal(index, walk) {
			t.Fatalf("seed %d, after %s: the open index has %d beads, a walk finds %d open\nindex %v\nwalk  %v", seed, after, len(index), len(walk), index, walk)
		}
	}
	same("setup")
	ok := map[string]int{}
	for step := 0; step < 600; step++ {
		switch step {
		case 200:
			reopenAs(11, "depots_migrated", strings.NewReplacer(`"depot":"web"`, `"rig":"web"`))
			same("the depot rename")
		case 400:
			reopenAs(10, "crew_migrated", strings.NewReplacer(`"scope:crew:boss"`, `"scope:resident:boss"`, `"crew:boss"`, `"resident:boss"`))
			same("the crew rename")
		}
		op := ops[rng.Intn(len(ops))]
		if op.do() == nil {
			ok[op.name]++
		}
		same(fmt.Sprintf("step %d, %s", step, op.name))
	}
	for _, op := range ops {
		if ok[op.name] == 0 {
			t.Errorf("seed %d: %s never succeeded, so the test did not drive it", seed, op.name)
		}
	}
	open, all := len(s.OpenWalk()), 0
	for _, typ := range []string{"", store.TypeMemory, store.TypeJournal} {
		bs, err := s.ListBeads(ctx, store.BeadFilter{All: true, Type: typ})
		must(err)
		all += len(bs)
	}
	if open == 0 || open == all {
		t.Fatalf("%d of %d beads are open: the test wants both kinds", open, all)
	}
}

// A store from before the open index gets it built on open, from the beads
// as they are, and the listings of open beads read through it.
func TestOpenIndexBuiltForOldStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var open []string
	for i := 0; i < 6; i++ {
		b, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i), Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			open = append(open, b.ID)
		} else if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DropOpenIndex(); err != nil {
		t.Fatal(err)
	}
	s.Close(ctx)
	s = storetest.Open(t, path)
	if index, walk := s.OpenIndex(), s.OpenWalk(); len(index) != 3 || !slices.Equal(index, walk) {
		t.Fatalf("open index after reopen = %v, a walk finds %v", index, walk)
	}
	for name, list := range map[string]func() ([]store.Bead, error){
		"bead list": func() ([]store.Bead, error) { return s.ListBeads(ctx, store.BeadFilter{}) },
		"ready":     func() ([]store.Bead, error) { return s.Ready(ctx) },
	} {
		if bs, err := list(); err != nil || !slices.Equal(ids(bs), open) {
			t.Errorf("%s after reopen = %v %v, want %v", name, ids(bs), err, open)
		}
	}
}

// A listing that can return no closed bead reads the open ones only, however
// many closed ones the store holds; one that can (-a) reads them all.
func TestOpenListingsDoNotReadClosedBeads(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t, filepath.Join(t.TempDir(), "yard.db"))
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotDir, Path: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	const total, kept = 40, 4
	var open []string
	for i := 0; i < total; i++ {
		b, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i), Stage: "new", Depot: "web"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if i%(total/kept) == 0 {
			open = append(open, b.ID)
		} else if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Remember(ctx, store.NewMemory{Title: "a memory", Scope: "all"}, "me"); err != nil {
		t.Fatal(err)
	}
	// The memory is open too: it is read, and kept only where asked for.
	for _, c := range []struct {
		name  string
		list  func() ([]store.Bead, error)
		reads int64
		want  []string
	}{
		{"ready", func() ([]store.Bead, error) { return s.Ready(ctx) }, kept + 1, open},
		{"dispatchable", func() ([]store.Bead, error) { return s.Dispatchable(ctx) }, kept + 1, open},
		{"bead list", func() ([]store.Bead, error) { return s.ListBeads(ctx, store.BeadFilter{}) }, kept + 1, open},
		{"bead list --stage", func() ([]store.Bead, error) { return s.ListBeads(ctx, store.BeadFilter{Stage: "new"}) }, kept + 1, open},
		{"bead list --depot", func() ([]store.Bead, error) { return s.ListBeads(ctx, store.BeadFilter{Depot: "web"}) }, kept + 1, open},
		{"bead list -a", func() ([]store.Bead, error) { return s.ListBeads(ctx, store.BeadFilter{All: true}) }, total + 1, nil},
	} {
		before := s.BeadReads()
		bs, err := c.list()
		if err != nil {
			t.Fatal(err)
		}
		if got := s.BeadReads() - before; got != c.reads {
			t.Errorf("%s read %d beads, want %d", c.name, got, c.reads)
		}
		if c.want != nil && !slices.Equal(ids(bs), c.want) {
			t.Errorf("%s = %v, want %v", c.name, ids(bs), c.want)
		}
		if c.want == nil && len(bs) != total {
			t.Errorf("%s lists %d beads, want %d", c.name, len(bs), total)
		}
	}
	before := s.BeadReads()
	if ms, err := s.Recall(ctx, "", ""); err != nil || len(ms) != 1 {
		t.Fatalf("recall = %+v %v", ms, err)
	}
	if got := s.BeadReads() - before; got != kept+1 {
		t.Errorf("recall read %d beads, want %d", got, kept+1)
	}
}

package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// An edge id keeps 4 characters of its ULID's timestamp, which wrap every
// 2^20 ms: an edge made after a wrap has a smaller id than one made before
// it. A train's children and edges() are oldest first all the same.
func TestEdgesOldestFirstAcrossIDWrap(t *testing.T) {
	// 3 ms before the wrap; each write below is 2 ms after the last.
	const wrap = 1 << 20
	at := time.UnixMilli(1_700_000_000_000/wrap*wrap + wrap - 3)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })

	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := context.Background()
	if err := s.PutDepot(ctx, Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	uuid := map[string]string{}
	children := []string{"web-1", "web-2", "web-3"}
	for _, nb := range []NewBead{
		{ID: "web-e", Title: "train", Type: TypeTrain, Depot: "web"},
		{ID: "web-1", Title: "one", Depot: "web"},
		{ID: "web-2", Title: "two", Depot: "web"},
		{ID: "web-3", Title: "three", Depot: "web"},
	} {
		b, err := s.CreateBead(ctx, nb, "me")
		if err != nil {
			t.Fatal(err)
		}
		uuid[nb.ID] = b.UUID
	}
	// web-1 before the wrap, web-2 and web-3 after it.
	for _, c := range children {
		if err := s.AddDep(ctx, "web-e", c, DepParent, "", "me"); err != nil {
			t.Fatal(err)
		}
		at = at.Add(2 * time.Millisecond)
	}

	f, err := s.Family(ctx, "web-e")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Children, children) {
		t.Fatalf("children %v, want %v", f.Children, children)
	}
	var want []string
	for _, c := range children {
		want = append(want, uuid[c])
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		var parents, blocks, ids []string
		for _, d := range edges(tx, bDepFrom, uuid["web-e"], DepParent) {
			parents, ids = append(parents, d.To), append(ids, d.ID)
		}
		// A parent edge adds child -blocks-> train: the same order, read
		// through the other index.
		for _, d := range edges(tx, bDepTo, uuid["web-e"], DepBlocks) {
			blocks = append(blocks, d.From)
		}
		if !slices.Equal(parents, want) || !slices.Equal(blocks, want) {
			t.Errorf("edges from the train to %v, blocking it from %v, want both %v", parents, blocks, want)
		}
		if slices.IsSorted(ids) {
			t.Errorf("edge ids %v are in order: none is past the wrap, so this tests nothing", ids)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Edges made at the same moment are listed by id.
func TestEdgesOfOneMomentByID(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })

	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := context.Background()
	a, err := s.CreateBead(ctx, NewBead{Title: "a"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		b, err := s.CreateBead(ctx, NewBead{Title: "b"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddDep(ctx, a.ID, b.ID, DepBlocks, "", "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.View(func(tx *bolt.Tx) error {
		var ids []string
		for _, d := range edges(tx, bDepFrom, a.UUID, DepBlocks) {
			ids = append(ids, d.ID)
		}
		if len(ids) != 8 || !slices.IsSorted(ids) {
			t.Errorf("edge ids %v, want 8 in order", ids)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

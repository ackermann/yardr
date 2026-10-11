package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// AddDep's walk at its worst on a store of 5000 beads: they block each
// other in one chain, and the edge asked for is from the last to the
// first, so the walk passes every bead before it refuses.
func BenchmarkAddDepCycleWalk(b *testing.B) {
	const n = 5000
	s, err := Open(filepath.Join(b.TempDir(), "y.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close(context.Background())
	s.db.NoSync = true // the fixture's 10000 writes, not what is measured
	ctx := context.Background()
	for i := range n {
		if _, err := s.CreateBead(ctx, NewBead{ID: fmt.Sprintf("y-%04d", i), Title: "b"}, "me"); err != nil {
			b.Fatal(err)
		}
		if i > 0 {
			if err := s.AddDep(ctx, fmt.Sprintf("y-%04d", i-1), fmt.Sprintf("y-%04d", i), DepBlocks, "", "", "me"); err != nil {
				b.Fatal(err)
			}
		}
	}
	first, last := "y-0000", fmt.Sprintf("y-%04d", n-1)
	b.Run("refused", func(b *testing.B) {
		for range b.N {
			if err := s.AddDep(ctx, last, first, DepBlocks, "", "", "me"); err == nil || !strings.Contains(err.Error(), "cycle") {
				b.Fatal(err)
			}
		}
	})
	b.Run("doctor", func(b *testing.B) {
		for range b.N {
			if cs, err := s.BlockerCycles(ctx); err != nil || len(cs) != 0 {
				b.Fatal(cs, err)
			}
		}
	})
}

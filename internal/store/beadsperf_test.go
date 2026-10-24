package store_test

import (
	"context"
	"fmt"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// seedListBeads gives the yard n open beads across three stages: the shape
// a listing reads, where every row is held to may(see) for the viewer.
func seedListBeads(t testing.TB, st store.API, n int) {
	t.Helper()
	ctx := context.Background()
	stages := []string{"backlog", "new", "review"}
	for i := range n {
		_, err := st.CreateBead(ctx, store.NewBead{
			Title:  fmt.Sprintf("bead %d", i),
			Stage:  stages[i%len(stages)],
			Labels: []string{"difficulty:2"},
		}, "me")
		if err != nil {
			t.Fatal(err)
		}
	}
}

// ListBeads over a few hundred beads answers in bounded time with the
// viewer's see asked per bead: the core allows all without reading, so
// the ask is one map lookup beside the decode. Run against the base to
// show it is not measurably slower there.
func BenchmarkListBeadsViewer(b *testing.B) {
	st := storetest.New(b)
	seedListBeads(b, st, 300)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.ListBeads(ctx, store.BeadFilter{Viewer: "ada"}); err != nil {
			b.Fatal(err)
		}
	}
}

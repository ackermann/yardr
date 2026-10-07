package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Batch reruns every call in a batch after one of them fails, so DeleteGroup
// must count released beads from zero on each attempt.
func TestDeleteGroupCountsOnceWhenBatchRetries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := context.Background()
	_ = s.PutGroup(ctx, Group{Name: "gone", Runner: "manual"}, "me")
	b, _ := s.CreateBead(ctx, NewBead{Title: "a"}, "me")
	if _, err := s.Claim(ctx, b.ID, "gone", "new", "yard"); err != nil {
		t.Fatal(err)
	}

	// Hold batches open until two calls are in, DeleteGroup first, then one
	// that fails and so makes bbolt run DeleteGroup's closure again.
	s.db.MaxBatchSize, s.db.MaxBatchDelay = 2, 10*time.Second
	type res struct {
		n   int
		err error
	}
	done := make(chan res, 1)
	go func() {
		n, err := s.DeleteGroup(ctx, "gone", "yardmaster")
		done <- res{n, err}
	}()
	time.Sleep(100 * time.Millisecond)
	boom := errors.New("boom")
	if err := s.db.Batch(func(*bolt.Tx) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("failing batch call = %v", err)
	}
	r := <-done
	if r.err != nil || r.n != 1 {
		t.Fatalf("DeleteGroup = %d, %v; want 1", r.n, r.err)
	}
}

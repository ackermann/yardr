package store

import (
	"context"
	"maps"
	"path/filepath"
	"testing"
)

// A bead open before Rounds has no keys, and its visited list says which
// stages it had reached: an outcome into one of them is a round, whether
// the bead has no key at all or keys for other stages only. A stage the
// list lacks is a first arrival, at zero: the record is made before the
// move lists the stage as visited, or every arrival would read as a return.
// A move by hand into a stage the list holds gives it its key and adds
// nothing.
func TestRoundsOfABeadFromBefore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(ctx) })
	f, err := ParseFlow("new -> review on done | new on retry\nreview -> approved on done | new on changes\napproved -> new on failed\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "loop"
	if _, err := s.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, NewBead{Title: "old", Metadata: map[string]any{"flow": "loop"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// forget makes the bead one stored before Rounds: its visited list
	// stays, its keys go.
	forget := func() {
		t.Helper()
		if err := s.write(func(w *wtx) error {
			row, err := w.loadBead(b.ID)
			if err != nil {
				return err
			}
			row.Rounds = nil
			return w.mintBead(row, "me")
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []struct {
		forget  bool
		outcome string // the outcome reported; "" for a move by hand to the stage hand
		hand    string
		stage   string
		want    map[string]int
		why     string
	}{
		{false, "done", "", "review", map[string]int{"new": 0, "review": 0}, "reached by outcome for the first time"},
		{true, "changes", "", "new", map[string]int{"new": 1}, "back in a stage the list holds, with no key at all"},
		{true, "", "review", "review", map[string]int{"review": 0}, "by hand into a stage the list holds: its key, no round"},
		{false, "changes", "", "new", map[string]int{"review": 0, "new": 1}, "back in a stage the list holds, with keys for others"},
		{true, "retry", "", "new", map[string]int{"new": 1}, "a same-stage outcome, with no key at all"},
		{false, "done", "", "review", map[string]int{"new": 1, "review": 1}, "back in a stage the list holds, with keys for others"},
		{false, "done", "", "approved", map[string]int{"new": 1, "review": 1, "approved": 0}, "a stage the list lacks is no return"},
	} {
		if step.forget {
			forget()
		}
		var got *Bead
		if step.hand != "" {
			got, err = s.Advance(ctx, b.ID, step.hand, Expect{}, "me")
		} else {
			got, err = s.Done(ctx, b.ID, step.outcome, "", Expect{}, "me")
		}
		if err != nil || got.Stage != step.stage {
			t.Fatalf("%s: moved to %+v, %v; want the bead in %s", step.why, got, err, step.stage)
		}
		read, err := s.GetBead(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(read.Rounds, step.want) {
			t.Fatalf("%s: rounds = %v, want %v", step.why, read.Rounds, step.want)
		}
	}
}

package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A store from before the indexes by bead and by start gets them built on
// open, from the sessions as they are, and lists a bead's sessions through
// them (yardr-wav4).
func TestSessionOrderIndexesBuiltForOldStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 5}); err != nil {
		t.Fatal(err)
	}
	// Two beads: one worked twice, its first session ended, and one once.
	var beads, ids []string
	for i := 0; i < 2; i++ {
		b, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i), Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		beads = append(beads, b.ID)
	}
	for _, b := range []string{beads[0], beads[1], beads[0]} {
		sess, err := s.Claim(ctx, b, "g", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
		if len(ids) == 1 {
			if err := s.EndSession(ctx, sess.ID, "died", "gone"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.DropSessionOrderIndexes(); err != nil {
		t.Fatal(err)
	}
	s.Close(ctx)
	s = storetest.Open(t, path)
	sameSessionAnswers(t, s, nil, "opening a store without the indexes")
	if bead, start := s.SessionOrderIndex(); len(bead) != 3 || len(start) != 3 {
		t.Fatalf("after reopen the index by bead = %q, by start = %q, want 3 keys in each", bead, start)
	}
	before := s.OrderReads()
	got, err := s.ListSessions(ctx, store.SessionFilter{All: true, Bead: beads[0]})
	if want := []string{ids[0], ids[2]}; err != nil || !slices.Equal(sessionIDs(got), want) {
		t.Fatalf("sessions of %s after reopen = %v %v, want %v", beads[0], sessionIDs(got), err, want)
	}
	if read := s.OrderReads() - before; read != 2 {
		t.Fatalf("listing the bead's sessions read %d through the index, want its 2", read)
	}
}

// A bead's sessions, and the newest of all, are read through their indexes:
// 10k sessions of another bead, or older ones, are not looked at.
func TestListSessionsReadsWhatItLists(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 4}); err != nil {
		t.Fatal(err)
	}
	old, err := s.CreateBead(ctx, store.NewBead{Title: "old", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := own.BulkSessions(10000, old.UUID, "g", "failed", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
		if err := s.EndSession(ctx, sess.ID, "died", "gone"); err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	every, err := s.Sessions(ctx, true)
	if err != nil || len(every) != 10004 {
		t.Fatalf("sessions = %d %v, want 10004", len(every), err)
	}
	for _, c := range []struct {
		name  string
		f     store.SessionFilter
		reads int64
		want  []string
	}{
		{"one bead's", store.SessionFilter{All: true, Bead: b.ID}, 4, append(ids, live.ID)},
		{"one bead's newest two", store.SessionFilter{All: true, Bead: b.ID, Limit: 2}, 2, []string{ids[2], live.ID}},
		{"one bead's live session", store.SessionFilter{Bead: b.ID}, 4, []string{live.ID}},
		{"the newest 200", store.SessionFilter{All: true, Limit: 200}, 200, sessionIDs(every[len(every)-200:])},
		{"those of the last half hour", store.SessionFilter{All: true, Since: time.Now().Add(-30 * time.Minute)}, 4, append(ids, live.ID)},
		{"every one", store.SessionFilter{All: true}, 10004, sessionIDs(every)},
		{"the live ones", store.SessionFilter{}, 0, []string{live.ID}},
	} {
		before := own.OrderReads()
		got, err := s.ListSessions(ctx, c.f)
		if err != nil || !slices.Equal(sessionIDs(got), c.want) {
			t.Fatalf("%s: %d sessions %v, want %d", c.name, len(got), err, len(c.want))
		}
		if read := own.OrderReads() - before; read != c.reads {
			t.Fatalf("%s: read %d sessions through the indexes, want %d", c.name, read, c.reads)
		}
	}
	if _, err := s.ListSessions(ctx, store.SessionFilter{All: true, Bead: "nope-1"}); err == nil {
		t.Fatal("sessions of a bead that does not exist: no error")
	}
}

// A pruned session leaves the indexes by bead and by start.
func TestPruneRemovesSessionOrderEntries(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := own.BulkSessions(5, b.UUID, "g", "failed", time.Now().Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Prune(ctx, store.Retention{Events: 90 * 24 * time.Hour}, "me"); err != nil || res.Sessions != 5 {
		t.Fatalf("prune = %+v %v, want 5 sessions", res, err)
	}
	if bead, start := own.SessionOrderIndex(); len(bead) != 0 || len(start) != 0 {
		t.Fatalf("after the prune the index by bead = %q, by start = %q, want both empty", bead, start)
	}
}

// bead list -a with a limit reads the newest beads and no more, and lists
// them in the order the whole listing has them (yardr-wav4).
func TestListBeadsNewestReadsWhatItLists(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t, filepath.Join(t.TempDir(), "yard.db"))
	const total = 300
	var open []string
	for i := 0; i < total; i++ {
		b, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i), Stage: "new", Priority: i % 3}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if i%10 == 0 {
			open = append(open, b.ID)
		} else if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	every, err := s.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil || len(every) != total {
		t.Fatalf("bead list --all = %d %v, want %d", len(every), err, total)
	}
	// The newest n of bs by creation, in the order bs has them.
	newest := func(bs []store.Bead, n int) []string {
		at := []time.Time{}
		for _, b := range bs {
			at = append(at, b.CreatedAt)
		}
		slices.SortFunc(at, func(a, b time.Time) int { return b.Compare(a) })
		out := []string{}
		for _, b := range bs {
			if len(at) <= n || !b.CreatedAt.Before(at[n-1]) {
				out = append(out, b.ID)
			}
		}
		return out
	}
	ids := func(bs []store.Bead) []string {
		out := []string{}
		for _, b := range bs {
			out = append(out, b.ID)
		}
		return out
	}
	openBeads, err := s.ListBeads(ctx, store.BeadFilter{})
	if err != nil || len(openBeads) != len(open) {
		t.Fatalf("bead list = %d %v, want %d", len(openBeads), err, len(open))
	}
	for _, c := range []struct {
		name  string
		f     store.BeadFilter
		reads int64
		want  []string
	}{
		{"-a -n 50", store.BeadFilter{All: true, Limit: 50}, 50, newest(every, 50)},
		{"-a -n 1000", store.BeadFilter{All: true, Limit: 1000}, total, ids(every)},
		{"-n 5", store.BeadFilter{Limit: 5}, 5, newest(openBeads, 5)},
		{"-a --since a day", store.BeadFilter{All: true, Since: time.Now().Add(-24 * time.Hour)}, total, ids(every)},
		{"-a --since the future", store.BeadFilter{All: true, Since: time.Now().Add(time.Hour)}, 0, []string{}},
	} {
		before := s.BeadReads()
		bs, err := s.ListBeads(ctx, c.f)
		if err != nil || !slices.Equal(ids(bs), c.want) {
			t.Fatalf("bead list %s = %d beads %v, want %d\n got %v\nwant %v", c.name, len(bs), err, len(c.want), ids(bs), c.want)
		}
		if read := s.BeadReads() - before; read != c.reads {
			t.Fatalf("bead list %s read %d beads, want %d", c.name, read, c.reads)
		}
	}
}

package dispatch_test

import (
	"context"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// barrierRunner is a fakeRunner whose Status calls block until the
// test releases them, counting how many are in flight at once: the
// asks reap fans out are seen overlapping, and never past its bound.
type barrierRunner struct {
	*fakeRunner
	release  chan struct{}
	blocking atomic.Bool
	statuses sync.Map // session id -> the status Status reports for it

	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (r *barrierRunner) Status(_ context.Context, h map[string]any) (string, error) {
	id, _ := h["session"].(string)
	if r.blocking.Load() {
		n := r.inFlight.Add(1)
		for {
			if m := r.maxSeen.Load(); n <= m || r.maxSeen.CompareAndSwap(m, n) {
				break
			}
		}
		<-r.release
		r.inFlight.Add(-1)
	}
	st, _ := r.statuses.Load(id)
	if st == nil {
		return "working", nil
	}
	return st.(string), nil
}

// Reap asks its running sessions' runners side by side, at most eight
// at once, and applies what they answer in session order: blocked
// sessions are reported once, gone ones ended, the rest left live,
// exactly as the serial asking applied them.
func TestReapJudgesSessionsSideBySide(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	r := &barrierRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, release: make(chan struct{})}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 20}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	const beads = 12
	for i := 0; i < beads; i++ {
		_, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
		must(t, err)
	}
	// One start per group is in flight at a time, so the twelve take
	// some passes to all stand.
	for range 20 {
		must(t, d.Reconcile(ctx))
		if live, err := s.Sessions(ctx, false); err == nil && len(live) == beads {
			break
		}
	}
	live, err := s.Sessions(ctx, false)
	must(t, err)
	if len(live) != beads {
		t.Fatalf("live sessions = %d, want %d started", len(live), beads)
	}
	// Two blocked, two gone, the rest working.
	blocked := map[string]string{} // bead -> session
	gone := map[string]string{}
	wantEvents := make([]string, 0, 4)
	for i, sess := range live {
		switch {
		case i < 2:
			r.statuses.Store(sess.ID, "blocked")
			blocked[sess.Bead] = sess.ID
			wantEvents = append(wantEvents, sess.ID)
		case i < 4:
			r.statuses.Store(sess.ID, "")
			gone[sess.Bead] = sess.ID
			wantEvents = append(wantEvents, sess.ID)
		default:
			r.statuses.Store(sess.ID, "working")
		}
	}

	r.blocking.Store(true)
	done := make(chan error, 1)
	go func() { done <- d.Reconcile(ctx) }()
	// The asks overlap up to the bound and no further: eight in flight
	// is the effect waited for, and a ninth joining it is watched for
	// on the way to releasing them.
	deadline := time.Now().Add(dispatch.WaitFuse)
	for r.maxSeen.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.maxSeen.Load(); got != 8 {
		t.Fatalf("status calls in flight = %d, want 8 held at the bound", got)
	}
	stable := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(stable) {
		if got := r.maxSeen.Load(); got != 8 {
			t.Fatalf("status calls in flight = %d past the bound of 8", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(r.release)
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(dispatch.WaitFuse):
		t.Fatal("the fanned-out pass did not finish")
	}
	r.blocking.Store(false)
	if got := r.maxSeen.Load(); got != 8 {
		t.Fatalf("most status calls at once = %d, want 8", got)
	}

	// Applied as the serial order applied them.
	for bead := range blocked {
		if n := len(eventKinds(t, s, bead, "session_blocked")); n != 1 {
			t.Fatalf("session_blocked for %s = %d, want 1", bead, n)
		}
	}
	for bead := range gone {
		if n := len(eventKinds(t, s, bead, "session_died")); n != 1 {
			t.Fatalf("session_died for %s = %d, want 1", bead, n)
		}
	}
	live, err = s.Sessions(ctx, false)
	must(t, err)
	if len(live) != beads-2 {
		t.Fatalf("live sessions after the pass = %d, want %d (the gone two ended)", len(live), beads-2)
	}
	events, err := s.Events(ctx, "", 500)
	must(t, err)
	wantSet := make(map[string]bool, len(wantEvents))
	for _, id := range wantEvents {
		wantSet[id] = true
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	// Sessions returned by the store are the pass's order. Their observable
	// events must keep that order even though the runner asks overlap.
	var gotEvents []string
	for _, event := range events {
		if event.Kind != "session_blocked" && event.Kind != "session_died" {
			continue
		}
		id, _ := event.Data["session"].(string)
		if wantSet[id] {
			gotEvents = append(gotEvents, id)
		}
	}
	if !slices.Equal(gotEvents, wantEvents) {
		t.Fatalf("applied session events = %v, want the session order %v", gotEvents, wantEvents)
	}

	// Once more, unblocked: the blocked two are not reported again.
	must(t, d.Reconcile(ctx))
	for bead := range blocked {
		if n := len(eventKinds(t, s, bead, "session_blocked")); n != 1 {
			t.Fatalf("session_blocked for %s after another pass = %d, want still 1", bead, n)
		}
	}
}

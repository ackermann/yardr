package store_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// sameSessionAnswers fails unless the store answers through its pane and
// pending indexes as a walk of every session does: the two indexes hold
// the keys a walk would put there, the pane of each of pids and the session
// a call from it is checked as are the walk's, and so are Unstopped and
// ClosedWithWorkspace.
func sameSessionAnswers(t *testing.T, s *store.Store, pids []int, after string) {
	t.Helper()
	ctx := context.Background()
	if index, walk := s.PaneIndex(), s.PaneIndexWalk(); !slices.Equal(index, walk) {
		t.Fatalf("after %s: the pane index differs from a walk\nindex %q\nwalk  %q", after, index, walk)
	}
	if index, walk := s.PendingIndex(), s.PendingIndexWalk(); !slices.Equal(index, walk) {
		t.Fatalf("after %s: the pending index differs from a walk\nindex %q\nwalk  %q", after, index, walk)
	}
	for _, pid := range pids {
		got, err := s.PaneOf(pid)
		want, werr := s.PaneOfWalk(pid)
		if err != nil || werr != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("after %s: pane of pid %d = %+v %v, a walk finds %+v %v", after, pid, got, err, want, werr)
		}
		sess, err := s.PaneSession(pid)
		wsess, werr := s.PaneSessionWalk(pid)
		if err != nil || werr != nil || !reflect.DeepEqual(sess, wsess) {
			t.Fatalf("after %s: session of pid %d = %+v %v, a walk finds %+v %v", after, pid, sess, err, wsess, werr)
		}
	}
	for name, lists := range map[string][2]func() ([]store.Session, error){
		"Unstopped":           {func() ([]store.Session, error) { return s.Unstopped(ctx) }, s.UnstoppedWalk},
		"ClosedWithWorkspace": {func() ([]store.Session, error) { return s.ClosedWithWorkspace(ctx) }, s.ClosedWithWorkspaceWalk},
	} {
		got, err := lists[0]()
		want, werr := lists[1]()
		if err != nil || werr != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("after %s: %s = %v %v, a walk finds %v %v", after, name, sessionIDs(got), err, sessionIDs(want), werr)
		}
	}
}

func sessionIDs(ss []store.Session) []string {
	out := []string{}
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

// The pane and pending indexes answer as a walk of every session does,
// after each of a few hundred random writes: claims, starts and handles
// with a pid, a shell's, both, one pid for both or none, with a start time
// or without, with a worktree, a dir or nothing on disk, pids used again by
// later sessions and by a session's next handle, ends of every kind
// (died, failed, released, the bead closed under it), stops (of live
// sessions too), cleans, start times moved, prunes, and opens of the file
// as a build from before the indexes left it. Two of the pids are this
// process and its parent, so a pane is found alive as well.
func TestSessionIndexesMatchAWalkAfterEveryWrite(t *testing.T) {
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
	must(s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 1000}))

	const seed = 1
	rng := rand.New(rand.NewSource(seed))
	// Pids no process has, each named by many sessions, and two that run.
	pids := []int{os.Getpid(), os.Getppid(), 4000001, 4000002, 4000003, 4000004}
	starts := map[int]uint64{}
	for _, pid := range pids[:2] {
		if st, err := peer.Describe(pid); err == nil {
			starts[pid] = st.Start
		}
	}
	// pidAndStart picks a pid and what a handle says of its start: the true
	// one where the process runs, a wrong one, or (ok false) none.
	pidAndStart := func() (pid int, start uint64, ok bool) {
		pid = pids[rng.Intn(len(pids))]
		switch rng.Intn(3) {
		case 0:
			return pid, 0, false
		case 1:
			return pid, uint64(rng.Intn(5)), true
		}
		return pid, starts[pid], true
	}
	handle := func() map[string]any {
		h := map[string]any{}
		if rng.Intn(4) > 0 {
			pid, start, ok := pidAndStart()
			h["pid"] = pid
			if ok {
				h["start"] = start
			}
		}
		if rng.Intn(4) > 0 {
			pid, start, ok := pidAndStart()
			h["shell_pid"] = pid
			if ok {
				h["shell_start"] = start
			}
		}
		switch rng.Intn(3) {
		case 0:
			h["worktree"] = "/tmp/w"
		case 1:
			h["dir"] = "/tmp/d"
		}
		return h
	}
	var beads, sessions []string
	bead := func() string {
		if len(beads) == 0 {
			return "none"
		}
		return beads[rng.Intn(len(beads))]
	}
	session := func() string {
		if len(sessions) == 0 {
			return "none"
		}
		// Mostly a late one: the early ones are soon done with and pruned.
		if n := len(sessions); n > 8 && rng.Intn(3) > 0 {
			return sessions[n-1-rng.Intn(8)]
		}
		return sessions[rng.Intn(len(sessions))]
	}
	// is fails the op unless the session was in state before it, so an op
	// that the store takes as a no-op does not count as driven.
	is := func(id string, want func(*store.Session) bool) error {
		sess, err := s.GetSession(ctx, id)
		if err == nil && !want(sess) {
			err = fmt.Errorf("session %s is not in the state for it", id)
		}
		return err
	}
	live := func(sess *store.Session) bool { return sess.State == "starting" || sess.State == "running" }
	changed := func(_ *store.Bead, err error) error { return err }
	ops := []struct {
		name string
		do   func() error
	}{
		{"claim a new bead", func() error {
			b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Stage: "new"}, "me")
			if err != nil {
				return err
			}
			beads = append(beads, b.ID)
			sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
			if err == nil {
				sessions = append(sessions, sess.ID)
			}
			return err
		}},
		{"claim again", func() error {
			b, err := s.GetBead(ctx, bead())
			if err != nil {
				return err
			}
			sess, err := s.Claim(ctx, b.ID, "g", b.Stage, "yard")
			if err == nil {
				sessions = append(sessions, sess.ID)
			}
			return err
		}},
		{"start", func() error {
			id := session()
			if err := is(id, func(sess *store.Session) bool { return sess.State == "starting" }); err != nil {
				return err
			}
			return s.MarkRunning(ctx, id, handle())
		}},
		{"set the handle", func() error { return s.SetHandle(ctx, session(), handle()) }},
		{"die", func() error {
			id := session()
			if err := is(id, live); err != nil {
				return err
			}
			return s.EndSession(ctx, id, []string{"died", "failed", "aborted"}[rng.Intn(3)], "gone")
		}},
		{"release", func() error { return changed(s.Release(ctx, bead(), "back", store.Expect{}, "me")) }},
		{"close the bead", func() error { return changed(s.CloseBead(ctx, bead(), "enough", store.Expect{}, "me")) }},
		{"reopen the bead", func() error { return changed(s.Reopen(ctx, bead(), store.Expect{}, "me")) }},
		{"stop", func() error {
			id := session()
			if err := is(id, func(sess *store.Session) bool { return !live(sess) && !sess.Stopped }); err != nil {
				return err
			}
			return s.MarkStopped(ctx, id)
		}},
		{"stop a live one", func() error {
			id := session()
			if err := is(id, live); err != nil {
				return err
			}
			return s.MarkStopped(ctx, id)
		}},
		{"clean", func() error {
			id := session()
			if err := is(id, func(sess *store.Session) bool { return sess.Stopped && !sess.Cleaned }); err != nil {
				return err
			}
			return s.MarkCleaned(ctx, id)
		}},
		{"move the start", func() error {
			return s.BackdateSession(session(), time.Now().Add(-time.Duration(rng.Intn(72))*time.Hour))
		}},
		{"prune", func() error {
			res, err := s.Prune(ctx, store.Retention{Events: 36 * time.Hour}, "me")
			if err == nil && res.Sessions == 0 {
				err = fmt.Errorf("no session to prune")
			}
			return err
		}},
	}
	same := func(after string) {
		t.Helper()
		sameSessionAnswers(t, s, pids, fmt.Sprintf("seed %d, %s", seed, after))
	}
	same("setup")
	ok := map[string]int{}
	var seen struct{ pane, shared, unstopped, workspace, pruned int }
	for step := 0; step < 600; step++ {
		if step == 200 || step == 400 {
			must(s.DropPaneAndPendingIndexes())
			must(s.Close(ctx))
			s, err = store.Open(path)
			must(err)
			same(fmt.Sprintf("step %d, opening the store without the indexes", step))
		}
		op := ops[rng.Intn(len(ops))]
		if op.do() == nil {
			ok[op.name]++
		}
		same(fmt.Sprintf("step %d, %s", step, op.name))

		// What the comparisons above were made on: the test wants each kind.
		for _, pid := range pids[:2] {
			if sess, _ := s.PaneSession(pid); sess != nil {
				seen.pane++
			}
		}
		named := map[int]int{}
		for _, k := range s.PaneIndex() {
			var pid int
			fmt.Sscanf(k, "%d", &pid)
			if named[pid]++; named[pid] == 2 {
				seen.shared++
			}
		}
		if un, _ := s.Unstopped(ctx); len(un) > 0 {
			seen.unstopped++
		}
		if ws, _ := s.ClosedWithWorkspace(ctx); len(ws) > 0 {
			seen.workspace++
		}
	}
	for _, op := range ops {
		if ok[op.name] == 0 {
			t.Errorf("seed %d: %s never succeeded, so the test did not drive it", seed, op.name)
		}
	}
	all, err := s.Sessions(ctx, true)
	must(err)
	seen.pruned = len(sessions) - len(all)
	if seen.pane == 0 && peer.HaveStat() || seen.shared == 0 || seen.unstopped == 0 || seen.workspace == 0 || seen.pruned == 0 || len(all) == 0 {
		t.Fatalf("seed %d: the steps that found a running pane, a pid two sessions name, an unstopped session, a closed bead's workspace: %+v, and %d sessions are left; the test wants each", seed, seen, len(all))
	}
}

// A store from before the pane and pending indexes gets them built on
// open, from the sessions as they are, and answers through them.
func TestPaneAndPendingIndexesBuiltForOldStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 5}); err != nil {
		t.Fatal(err)
	}
	me, _ := peer.Describe(os.Getpid())
	// A running session in this process's pane, one that died with its
	// runner still up, and one stopped with a worktree of a closed bead.
	var ids []string
	for i, h := range []map[string]any{
		{"shell_pid": os.Getpid(), "shell_start": me.Start},
		{"pid": 4000001, "start": 7},
		{"pid": 4000001, "worktree": "/tmp/w"},
	} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i), Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRunning(ctx, sess.ID, h); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
		switch i {
		case 1:
			err = s.EndSession(ctx, sess.ID, "died", "gone")
		case 2:
			if _, err = s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me"); err == nil {
				err = s.MarkStopped(ctx, sess.ID)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DropPaneAndPendingIndexes(); err != nil {
		t.Fatal(err)
	}
	s.Close(ctx)
	s = storetest.Open(t, path)
	sameSessionAnswers(t, s, []int{os.Getpid(), 4000001}, "opening a store without the indexes")
	if pending := s.PendingIndex(); len(pending) != 2 || len(s.PaneIndex()) != 3 {
		t.Fatalf("after reopen the pending index = %q, the pane index = %q", pending, s.PaneIndex())
	}
	if peer.HaveStat() {
		if sess, err := s.PaneSession(os.Getpid()); err != nil || sess == nil || sess.ID != ids[0] {
			t.Fatalf("session of this process's pane after reopen = %+v %v, want %s", sess, err, ids[0])
		}
	}
	if un, err := s.Unstopped(ctx); err != nil || !slices.Equal(sessionIDs(un), ids[1:2]) {
		t.Fatalf("unstopped after reopen = %v %v, want %v", sessionIDs(un), err, ids[1:2])
	}
	if ws, err := s.ClosedWithWorkspace(ctx); err != nil || !slices.Equal(sessionIDs(ws), ids[2:]) {
		t.Fatalf("closed with workspace after reopen = %v %v, want %v", sessionIDs(ws), err, ids[2:])
	}
}

// A session's pane entries follow its handle: a handle that names another
// pid, or none, leaves no entry under the old one, so the next session to
// name that pid is alone there.
func TestPaneIndexFollowsTheHandle(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t, filepath.Join(t.TempDir(), "yard.db"))
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 5}); err != nil {
		t.Fatal(err)
	}
	claim := func() string {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		return sess.ID
	}
	a := claim()
	if err := s.SetHandle(ctx, a, map[string]any{"pid": 4000001}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunning(ctx, a, map[string]any{"pid": 4000002, "shell_pid": 4000003}); err != nil {
		t.Fatal(err)
	}
	want := []string{"4000002\x00" + a, "4000003\x00" + a}
	if got := s.PaneIndex(); !slices.Equal(got, want) {
		t.Fatalf("pane index after the handle changed = %q, want %q", got, want)
	}
	b := claim()
	if err := s.MarkRunning(ctx, b, map[string]any{"pid": 4000001}); err != nil {
		t.Fatal(err)
	}
	if pn, err := s.PaneOf(4000001); err != nil || pn == nil || pn.Session != b {
		t.Fatalf("pane of the reused pid = %+v %v, want session %s", pn, err, b)
	}
	if err := s.SetHandle(ctx, a, map[string]any{"worktree": "/tmp/w"}); err != nil {
		t.Fatal(err)
	}
	if got, want := s.PaneIndex(), []string{"4000001\x00" + b}; !slices.Equal(got, want) {
		t.Fatalf("pane index after the handle lost its pids = %q, want %q", got, want)
	}
	sameSessionAnswers(t, s, []int{4000001, 4000002, 4000003}, "the handles changed")
}

// The dispatcher's two listings read the sessions it is not done with,
// however many the store keeps.
func TestPendingListingsDoNotReadDoneSessions(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 3}); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := own.BulkSessions(10000, b.UUID, "g", "failed", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession(ctx, sess.ID, "died", "gone"); err != nil {
		t.Fatal(err)
	}
	before := own.PendingReads()
	if un, err := s.Unstopped(ctx); err != nil || len(un) != 1 || un[0].ID != sess.ID {
		t.Fatalf("unstopped = %v %v, want [%s]", sessionIDs(un), err, sess.ID)
	}
	if ws, err := s.ClosedWithWorkspace(ctx); err != nil || len(ws) != 0 {
		t.Fatalf("closed with workspace = %v %v, want none", sessionIDs(ws), err)
	}
	if read := own.PendingReads() - before; read != 2 {
		t.Fatalf("the two listings read %d sessions, want the pending one, twice", read)
	}
}

package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

func ids(bs []store.Bead) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.ID
	}
	return out
}

func TestCreateAndGetBead(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "web", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "fix login", Rig: "web", Labels: []string{"ui", "ui"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.ID, "web-") || len(b.ID) != len("web-")+store.MinPrefix || b.Stage != "new" || b.Status != "open" || b.Type != "task" {
		t.Fatalf("unexpected bead: %+v", b)
	}
	if !slices.Equal(b.Labels, []string{"ui"}) {
		t.Fatalf("labels not deduped: %v", b.Labels)
	}
	if b.Rig == nil || *b.Rig != "web" {
		t.Fatalf("rig = %v", b.Rig)
	}
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "second"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b2.ID, "yd-") {
		t.Fatalf("id = %s, want yd-<random>", b2.ID)
	}
	if _, err := s.GetBead(ctx, "nope-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing bead err = %v", err)
	}
}

// Rig names, bead types and dep kinds end up in paths, type URLs and route
// matches, so a slash, whitespace or capitals are refused where they enter.
func TestNamesAreValidated(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	for _, bad := range []string{"", "a/b", "my rig", "Web", "-x", ".hidden", "tab\tx"} {
		if err := s.PutRig(ctx, store.Rig{Name: bad, Base: "main"}, "me"); err == nil {
			t.Errorf("rig name %q accepted", bad)
		}
		if bad == "" {
			continue // an empty type is the default, task
		}
		if _, err := s.CreateBead(ctx, store.NewBead{Title: "x", Type: bad}, "me"); err == nil {
			t.Errorf("bead type %q accepted", bad)
		}
	}
	if err := s.PutRig(ctx, store.Rig{Name: "web.v2_x-1", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a", Type: "tech-debt"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "b"}, "me")
	bug := "Bug"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Type: &bug}, store.Expect{}, "me"); err == nil {
		t.Error("bead update to type Bug accepted")
	}
	for _, bad := range []string{"", "depends on", "Blocks", "x/y"} {
		if err := s.AddDep(ctx, a.ID, b.ID, bad, "me"); err == nil {
			t.Errorf("dep kind %q accepted", bad)
		}
	}
	if err := s.AddDep(ctx, a.ID, b.ID, "discovered-from", "me"); err != nil {
		t.Fatal(err)
	}
}

// A rig's --check override survives the store (and the RPC client).
func TestRigKeepsCheckOverride(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "web", Path: "/tmp/web", Base: "main", Check: "make test"}, "me"); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetRig(ctx, "web")
	if err != nil || r.Check != "make test" {
		t.Fatalf("rig = %+v, %v", r, err)
	}
	// The override replaces the repo's gate, so setting or dropping it is
	// an event, with who did it.
	if err := s.PutRig(ctx, store.Rig{Name: "web", Path: "/tmp/web", Base: "main", AllowNoCheck: true}, "op"); err != nil {
		t.Fatal(err)
	}
	if r, err = s.GetRig(ctx, "web"); err != nil || r.Check != "" || !r.AllowNoCheck {
		t.Fatalf("rig = %+v, %v", r, err)
	}
	evs, err := s.Events(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range evs {
		if e.Kind == "rig_updated" {
			got = append(got, fmt.Sprintf("%s %v %v %v", e.Actor, e.Data["rig"], e.Data["check"], e.Data["allow_no_check"]))
		}
	}
	if want := []string{"me web make test false", "op web  true"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rig_updated events = %q, want %q", got, want)
	}
}

// rig rm takes the rig's routes and flow pointers with it, leaves other rigs'
// alone (web2 shares web's prefix), and is refused, with nothing deleted,
// while an open bead or a live session still needs the rig.
func TestDeleteRig(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, rig := range []string{"web", "web2"} {
		must(s.PutRig(ctx, store.Rig{Name: rig, Path: "/tmp/" + rig, Base: "main"}, "me"))
	}
	must(s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}))
	web, web2 := "web", "web2"
	mine, err := s.AddRoute(ctx, store.Route{Stage: "new", Rig: &web, Group: "builders"})
	must(err)
	other, err := s.AddRoute(ctx, store.Route{Stage: "new", Rig: &web2, Group: "builders"})
	must(err)
	anyRig, err := s.AddRoute(ctx, store.Route{Stage: "review", Group: "builders"})
	must(err)
	f, err := store.ParseFlow("new -> review\nreview -> done\ndone terminal")
	must(err)
	f.Name = "code"
	_, err = s.PutFlow(ctx, *f, "me")
	must(err)
	must(s.SetFlowPointer(ctx, "web", "", "code", "me"))
	must(s.SetFlowPointer(ctx, "web", "bug", "code", "me"))
	must(s.SetFlowPointer(ctx, "web2", "", "code", "me"))

	// A closed bead is history, not a user.
	done, err := s.CreateBead(ctx, store.NewBead{Title: "done", Rig: "web"}, "me")
	must(err)
	_, err = s.CloseBead(ctx, done.ID, "merged", store.Expect{}, "me")
	must(err)
	// An open bead, and then a session working it, each refuse the removal.
	open, err := s.CreateBead(ctx, store.NewBead{Title: "open", Rig: "web"}, "me")
	must(err)
	intact := func(step string) {
		t.Helper()
		if _, err := s.GetRig(ctx, "web"); err != nil {
			t.Fatalf("%s: rig gone: %v", step, err)
		}
		rs, _ := s.Routes(ctx)
		ps, _ := s.FlowPointers(ctx)
		if len(rs) != 3 || len(ps) != 3 {
			t.Fatalf("%s: routes %d, pointers %d; refused removal deleted something", step, len(rs), len(ps))
		}
	}
	_, err = s.DeleteRig(ctx, "web", "me")
	if err == nil || !strings.Contains(err.Error(), "rig web is in use by bead "+open.ID) || strings.Contains(err.Error(), done.ID) {
		t.Fatalf("open bead: err = %v", err)
	}
	intact("open bead")
	sess, err := s.Claim(ctx, open.ID, "builders", "new", "yard")
	must(err)
	_, err = s.DeleteRig(ctx, "web", "me")
	if err == nil || !strings.Contains(err.Error(), "session "+sess.ID+" (bead "+open.ID+")") {
		t.Fatalf("live session: err = %v", err)
	}
	intact("live session")
	must(s.EndSession(ctx, sess.ID, "aborted", "test"))
	if _, err := s.DeleteRig(ctx, "web", "me"); err == nil || !strings.Contains(err.Error(), "bead "+open.ID) || strings.Contains(err.Error(), "session "+sess.ID) {
		t.Fatalf("ended session, bead still open: err = %v", err)
	}
	_, err = s.CloseBead(ctx, open.ID, "dropped", store.Expect{}, "me")
	must(err)

	got, err := s.DeleteRig(ctx, "web", "op")
	must(err)
	if got.Rig != "web" || !slices.Equal(got.Routes, []string{mine.ID}) || len(got.FlowPointers) != 2 ||
		got.FlowPointers[0].String() != "rig web" || got.FlowPointers[1].String() != "rig web type bug" {
		t.Fatalf("removal = %+v", got)
	}
	if _, err := s.GetRig(ctx, "web"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rig after rm: %v", err)
	}
	rs, err := s.Rigs(ctx)
	if err != nil || len(rs) != 1 || rs[0].Name != "web2" {
		t.Fatalf("rigs = %+v, %v", rs, err)
	}
	routes, _ := s.Routes(ctx)
	var left []string
	for _, r := range routes {
		left = append(left, r.ID)
	}
	slices.Sort(left)
	want := []string{other.ID, anyRig.ID}
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Fatalf("routes left = %v, want %v", left, want)
	}
	ptrs, _ := s.FlowPointers(ctx)
	if len(ptrs) != 1 || ptrs[0].Rig != "web2" {
		t.Fatalf("pointers left = %+v", ptrs)
	}
	// Closed beads keep their rig as history.
	if b, err := s.GetBead(ctx, done.ID); err != nil || b.Rig == nil || *b.Rig != "web" {
		t.Fatalf("closed bead after rm: %+v, %v", b, err)
	}
	evs, err := s.Events(ctx, "", 50)
	must(err)
	i := slices.IndexFunc(evs, func(e store.Event) bool { return e.Kind == "rig_removed" })
	if i < 0 || evs[i].Actor != "op" || evs[i].Data["rig"] != "web" || fmt.Sprint(evs[i].Data["routes"]) != fmt.Sprintf("[%s]", mine.ID) ||
		fmt.Sprint(evs[i].Data["flow_pointers"]) != "[rig web rig web type bug]" {
		t.Fatalf("rig_removed event = %+v", evs)
	}
	if _, err := s.DeleteRig(ctx, "web", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second rm: %v", err)
	}
	if _, err := s.DeleteRig(ctx, "nope", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown rig: %v", err)
	}
}

func TestReadyRespectsBlocksHoldAndClose(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a, _ := s.CreateBead(ctx, store.NewBead{Title: "a"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "b"}, "me")
	c, _ := s.CreateBead(ctx, store.NewBead{Title: "c"}, "me")
	if err := s.AddDep(ctx, a.ID, b.ID, "blocks", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, a.ID, c.ID, "related", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, a.ID, b.ID, "blocks", "me"); err == nil {
		t.Fatal("duplicate edge accepted")
	}
	if err := s.AddDep(ctx, a.ID, "yd-99", "blocks", "me"); err == nil {
		t.Fatal("edge to missing bead accepted")
	}
	ready, _ := s.Ready(ctx)
	if got := ids(ready); !slices.Equal(got, []string{a.ID, c.ID}) {
		t.Fatalf("ready = %v", got)
	}
	if _, err := s.SetHold(ctx, c.ID, true, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, a.ID, "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	ready, _ = s.Ready(ctx)
	if got := ids(ready); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("ready after close+hold = %v", got)
	}
}

func TestClaimIsExclusive(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "web/fixers", Runner: "fake", Limit: 5}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, taken := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Claim(ctx, b.ID, "web/fixers", "new", "yard")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, store.ErrTaken):
				taken++
			default:
				t.Errorf("claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || taken != 7 {
		t.Fatalf("wins=%d taken=%d", wins, taken)
	}
	ss, _ := s.Sessions(ctx, true)
	if len(ss) != 1 {
		t.Fatalf("losing claims left %d sessions", len(ss))
	}
	// Stage must match: a bead that moved on cannot be claimed for its old stage.
	b2, _ := s.CreateBead(ctx, store.NewBead{Title: "y", Stage: "review"}, "me")
	if _, err := s.Claim(ctx, b2.ID, "web/fixers", "new", "yard"); !errors.Is(err, store.ErrTaken) {
		t.Fatalf("stale-stage claim err = %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	_ = s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"})
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")

	sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunning(ctx, sess.ID, map[string]any{"agent": "x"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetBead(ctx, b.ID)
	if got.Group == nil || *got.Group != "g" || got.Session == nil || *got.Session != sess.ID {
		t.Fatalf("bead not owned: %+v", got)
	}

	// Advancing ends the session and frees the bead for the next stage.
	if _, err := s.Advance(ctx, b.ID, "review", "agent"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetBead(ctx, b.ID)
	if got.Stage != "review" || got.Group != nil || got.Session != nil {
		t.Fatalf("after advance: %+v", got)
	}
	ss, _ := s.GetSession(ctx, sess.ID)
	if ss.State != "done" || ss.EndedAt == nil {
		t.Fatalf("session after advance: %+v", ss)
	}
	un, _ := s.Unstopped(ctx)
	if len(un) != 1 {
		t.Fatalf("unstopped = %d", len(un))
	}
	_ = s.MarkStopped(ctx, sess.ID)
	if un, _ = s.Unstopped(ctx); len(un) != 0 {
		t.Fatalf("unstopped after stop = %d", len(un))
	}

	// A dead session hands its bead back.
	sess2, err := s.Claim(ctx, b.ID, "g", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession(ctx, sess2.ID, "died", "pane exited"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetBead(ctx, b.ID)
	if got.Group != nil || got.Stage != "review" {
		t.Fatalf("after death: %+v", got)
	}
	// Ending twice is a no-op, not a second event.
	_ = s.EndSession(ctx, sess2.ID, "died", "again")
	evs, _ := s.Events(ctx, b.ID, 100)
	kinds := make([]string, len(evs))
	for i, e := range evs {
		kinds[i] = e.Kind
	}
	want := []string{"created", "claimed", "started", "advanced", "claimed", "session_died"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
}

// Deleting a group releases the beads its live sessions hold, in the same
// transaction, so no bead is left owned by a group that no longer exists.
func TestDeleteGroupReleasesClaimedBeads(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	_ = s.PutGroup(ctx, store.Group{Name: "gone", Runner: "manual"})
	_ = s.PutGroup(ctx, store.Group{Name: "kept", Runner: "manual"})
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "gone"}); err != nil {
		t.Fatal(err)
	}
	a, _ := s.CreateBead(ctx, store.NewBead{Title: "a"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "b"}, "me")
	sa, err := s.Claim(ctx, a.ID, "gone", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := s.Claim(ctx, b.ID, "kept", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.DeleteGroup(ctx, "gone", "mayor")
	if err != nil || n != 1 {
		t.Fatalf("DeleteGroup = %d, %v; want 1", n, err)
	}
	if _, err := s.GetGroup(ctx, "gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("group still there: %v", err)
	}
	if rs, _ := s.Routes(ctx); len(rs) != 0 {
		t.Fatalf("routes left: %+v", rs)
	}
	got, _ := s.GetBead(ctx, a.ID)
	if got.Group != nil || got.Session != nil || got.Stage != "new" {
		t.Fatalf("bead still owned: %+v", got)
	}
	ss, _ := s.GetSession(ctx, sa.ID)
	if ss.State != "released" || ss.EndedAt == nil {
		t.Fatalf("session after group rm: %+v", ss)
	}
	if un, _ := s.Unstopped(ctx); len(un) != 1 || un[0].ID != sa.ID {
		t.Fatalf("unstopped = %+v, want the released session for teardown", un)
	}
	evs, _ := s.Events(ctx, a.ID, 100)
	last := evs[len(evs)-1]
	if last.Kind != "released" || last.Actor != "mayor" || last.Data["reason"] != "group gone deleted" {
		t.Fatalf("last event = %+v", last)
	}

	// Other groups' claims are untouched.
	got, _ = s.GetBead(ctx, b.ID)
	if got.Session == nil || *got.Session != sb.ID {
		t.Fatalf("other group's bead released: %+v", got)
	}

	// Deleting a group with nothing claimed releases nothing.
	if n, err := s.DeleteGroup(ctx, "gone", "mayor"); err != nil || n != 0 {
		t.Fatalf("second DeleteGroup = %d, %v", n, err)
	}
}

func TestRoutesAndUpdate(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "missing"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("route to missing group err = %v", err)
	}
	_ = s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: map[string]any{"kind": "claude"}})
	typ := "bug"
	r, err := s.AddRoute(ctx, store.Route{Stage: "new", Type: &typ, Group: "g", Priority: 3})
	if err != nil {
		t.Fatal(err)
	}
	rs, _ := s.Routes(ctx)
	if len(rs) != 1 || rs[0].ID != r.ID || *rs[0].Type != "bug" || rs[0].Rig != nil {
		t.Fatalf("routes = %+v", rs)
	}
	g, _ := s.GetGroup(ctx, "g")
	if g.Config["kind"] != "claude" || g.Limit != 1 {
		t.Fatalf("group = %+v", g)
	}

	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x", Labels: []string{"a"}}, "me")
	title := "y"
	b, err = s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title, AddLabels: []string{"b"},
		RemoveLabel: []string{"a"}, Metadata: map[string]any{"pr": 12}}, store.Expect{}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b.Title != "y" || !slices.Equal(b.Labels, []string{"b"}) || b.Metadata["pr"] == nil {
		t.Fatalf("after update: %+v", b)
	}
	if _, err := s.UpdateBead(ctx, "yd-404", store.BeadPatch{Title: &title}, store.Expect{}, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing err = %v", err)
	}
}

func TestWakeAndTail(t *testing.T) {
	s := storetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake, err := s.Wake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tail, err := s.TailEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	x, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(3 * time.Second):
		t.Fatal("no wake after bead create")
	}
	select {
	case e := <-tail:
		if e.Kind != "created" || e.Bead == nil || *e.Bead != x.ID {
			t.Fatalf("tail event = %+v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no tailed event")
	}
}

// One process owns the store: a second open fails fast, and concurrent
// writes through the owner all land.
func TestSingleOwnerAndConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	s := storetest.Open(t, path)
	if _, err := store.Open(path); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("second open err = %v, want ErrLocked", err)
	}
	ctx := context.Background()
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "busy"}, "me")
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.AddNote(ctx, b.ID, "note", "me")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 40 {
		t.Fatalf("notes = %d", len(notes))
	}
}

func TestRevisionsFollowBDPRules(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	b, err := s.CreateBead(ctx, store.NewBead{Title: "A"}, "me")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(err)
	seen := map[string]bool{b.Revision: true}
	if b.Revision == "" {
		t.Fatal("created bead has no revision")
	}

	// A no-op write mints nothing and writes no event.
	same := "A"
	nb, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &same}, store.Expect{}, "me")
	must(err)
	if nb.Revision != b.Revision || !nb.UpdatedAt.Equal(b.UpdatedAt) {
		t.Fatalf("no-op changed revision/updated_at: %s -> %s", b.Revision, nb.Revision)
	}
	_, err = s.SetHold(ctx, b.ID, false, store.Expect{}, "me") // already unheld
	must(err)

	// A -> B -> A: three distinct revisions.
	rev := b.Revision
	for _, title := range []string{"B", "A"} {
		nb, err = s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title}, store.Expect{}, "me")
		must(err)
		if seen[nb.Revision] {
			t.Fatalf("revision %s reused after changing title to %s", nb.Revision, title)
		}
		seen[nb.Revision] = true
		rev = nb.Revision
	}
	// Lifecycle moves mint too.
	nb, err = s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me")
	must(err)
	if nb.Revision == rev || seen[nb.Revision] {
		t.Fatal("close did not mint a fresh revision")
	}
	// Closing again is a no-op.
	again, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me")
	must(err)
	if again.Revision != nb.Revision {
		t.Fatal("re-closing minted a revision")
	}
	evs, _ := s.Events(ctx, b.ID, 100)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if want := []string{"created", "updated", "updated", "closed"}; !slices.Equal(kinds, want) {
		t.Fatalf("events = %v, want %v (no-ops must not log)", kinds, want)
	}
}

func TestExplicitIDs(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	b, err := s.CreateBead(ctx, store.NewBead{Title: "a", ID: "demo-a"}, "me")
	if err != nil || b.ID != "demo-a" {
		t.Fatalf("explicit id: %v %v", b, err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "again", ID: "demo-a"}, "me"); err == nil {
		t.Fatal("reused an existing id")
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "bad", ID: "a/b"}, "me"); err == nil {
		t.Fatal("accepted an id with a slash")
	}
}

// Every record in a snapshot carries the actor of its latest version
// (once broken by a SurrealDB LIMIT-in-subquery bug; kept as a guard).
func TestLastActorOnEveryRow(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	var first string
	for i := 0; i < 4; i++ {
		b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, fmt.Sprintf("actor-%d", i))
		if i == 0 {
			first = b.ID
		} else {
			if err := s.AddDep(ctx, first, b.ID, "blocks", "linker"); err != nil {
				t.Fatal(err)
			}
		}
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range snap.Beads {
		if b.LastActor == nil || *b.LastActor == "" {
			t.Fatalf("bead %s has no last actor", b.ID)
		}
	}
	if len(snap.Deps) != 3 {
		t.Fatalf("deps = %d", len(snap.Deps))
	}
	for _, d := range snap.Deps {
		if d.LastActor == nil || *d.LastActor != "linker" {
			t.Fatalf("dep %s last actor = %v", d.ID, d.LastActor)
		}
	}
}

func TestNotesAndEdgesMintTheBeadsTheyChange(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a, _ := s.CreateBead(ctx, store.NewBead{Title: "a"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "b"}, "me")
	if err := s.AddNote(ctx, a.ID, "hello", "me"); err != nil {
		t.Fatal(err)
	}
	a2, _ := s.GetBead(ctx, a.ID)
	if a2.Revision == a.Revision {
		t.Fatal("a note did not mint the bead")
	}
	if err := s.AddDep(ctx, a.ID, b.ID, "blocks", "me"); err != nil {
		t.Fatal(err)
	}
	a3, _ := s.GetBead(ctx, a.ID)
	b3, _ := s.GetBead(ctx, b.ID)
	if a3.Revision == a2.Revision || b3.Revision == b.Revision {
		t.Fatal("an edge did not mint both beads")
	}
	snap, _ := s.Snapshot(ctx)
	for _, sb := range snap.Beads {
		if want := map[string]int{a.ID: 1, b.ID: 0}[sb.ID]; sb.Notes != want {
			t.Fatalf("%s notes = %d, want %d", sb.ID, sb.Notes, want)
		}
	}
}

func TestSubscribeResumesWithoutGapsOrDuplicates(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint(i)}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// Subscribe from 2: backlog 3..5, then live events, in order.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := s.Subscribe(sctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < 3; i++ {
			_, _ = s.CreateBead(ctx, store.NewBead{Title: "live"}, "me")
		}
	}()
	var got []uint64
	for e := range ch {
		got = append(got, e.Seq)
		if len(got) == 6 {
			cancel()
			break
		}
	}
	if !slices.Equal(got, []uint64{3, 4, 5, 6, 7, 8}) {
		t.Fatalf("seqs = %v", got)
	}

	// A subscriber that went away resumes from its last seq: nothing lost.
	_, _ = s.CreateBead(ctx, store.NewBead{Title: "while away"}, "me")
	ch2, _ := s.Subscribe(ctx, got[len(got)-1])
	select {
	case e := <-ch2:
		if e.Seq != 9 || e.Kind != "created" {
			t.Fatalf("resumed at %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("resume delivered nothing")
	}
}

func TestSlowSubscriberIsDroppedNotBlocking(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	ch, _ := s.Subscribe(ctx, 0) // never read
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	for i := 0; i < 5000; i++ { // more than the subscriber buffer
		if err := s.LogDispatch(ctx, b.ID, "session_prompted", "me", nil); err != nil {
			t.Fatal(err)
		}
	}
	// Writers were never blocked; the subscriber's stream ends after what
	// it had buffered, and it resumes from its last seq.
	n := 0
	for range ch {
		n++
	}
	if n == 0 || n >= 5001 {
		t.Fatalf("slow subscriber received %d events; want a truncated stream", n)
	}
}

// Batched writers commit together but return in any order; subscribers
// must still see every event exactly once, in seq order.
func TestSubscribeOrderUnderConcurrentWriters(t *testing.T) {
	s := storetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me") // seq 1
	ch, err := s.Subscribe(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 50
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.LogDispatch(ctx, b.ID, "session_prompted", "me", nil)
		}()
	}
	wg.Wait()
	var got []uint64
	timeout := time.After(5 * time.Second)
	for len(got) < writers {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed after %v", got)
			}
			got = append(got, e.Seq)
		case <-timeout:
			t.Fatalf("got %d of %d events: %v", len(got), writers, got)
		}
	}
	for i, seq := range got {
		if seq != uint64(i+2) {
			t.Fatalf("event %d has seq %d; stream = %v", i, seq, got)
		}
	}
}

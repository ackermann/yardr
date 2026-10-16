package store

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// arrivalsTest is a store with a clock the test sets, a builders group,
// a manual deciders group with members, and routes for new and decide.
type arrivalsTest struct {
	*Store
	now time.Time
}

func newArrivalsTest(t testing.TB) *arrivalsTest {
	t.Helper()
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	y := &arrivalsTest{now: time.Date(2099, 1, 1, 12, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return y.now }
	t.Cleanup(func() { clock = time.Now })
	var err error
	if y.Store, err = Open(filepath.Join(t.TempDir(), "y.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { y.Close(context.Background()) })
	ctx := context.Background()
	if err := y.PutGroup(ctx, Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(ctx, Group{Name: "deciders", Runner: "manual", Limit: 50, Members: []string{"nell", "yardmaster"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddRoute(ctx, Route{Stage: "new", Group: "builders"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddRoute(ctx, Route{Stage: "decide", Group: "deciders"}, "me"); err != nil {
		t.Fatal(err)
	}
	return y
}

func (y *arrivalsTest) at(t time.Time) *arrivalsTest {
	y.now = t
	return y
}

func (y *arrivalsTest) create(t testing.TB, title, stage string) *Bead {
	t.Helper()
	b, err := y.CreateBead(context.Background(), NewBead{Title: title, ID: title, Stage: stage}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// flowDepot makes a directory depot pointed at a store flow of the given
// stages, in order, so beads created in the depot travel them.
func (y *arrivalsTest) flowDepot(t testing.TB, depot, flow string, stages ...string) {
	t.Helper()
	ctx := context.Background()
	if err := y.PutDepot(ctx, Depot{Name: depot, Path: t.TempDir(), Kind: DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	var rules []string
	for i := 0; i+1 < len(stages); i++ {
		rules = append(rules, stages[i]+" -> "+stages[i+1])
	}
	f, err := ParseFlow(strings.Join(rules, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	f.Name = flow
	if _, err := y.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetFlowPointer(ctx, depot, "", flow, "me"); err != nil {
		t.Fatal(err)
	}
}

// travel creates a bead in a depot's stage, or without a depot when depot
// is "".
func (y *arrivalsTest) travel(t testing.TB, id, depot, stage string) *Bead {
	t.Helper()
	nb := NewBead{Title: id, ID: id, Stage: stage}
	if depot != "" {
		nb.Depot = depot
	}
	b, err := y.CreateBead(context.Background(), nb, "me")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func arrivalIDs(rows []arrivalRowForTest) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Bead)
	}
	return out
}

// arrivalRowForTest names the served row's fields without importing eta
// in the test's helpers; the tests below read the real rows.
type arrivalRowForTest = struct {
	Bead     string
	Title    string
	Platform string
	Line     string
	Expected string
	Remark   string
	Tone     string
}

func TestArrivalsServesTravelling(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	t0 := y.now
	y.at(t0).create(t, "wharf-a", "new")
	y.at(t0.Add(time.Hour)).create(t, "wharf-b", "backlog")
	y.at(t0.Add(2*time.Hour)).create(t, "wharf-c", "decide")
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.Bead)
	}
	// The backlog is not on the board (board: "the backlog is not on
	// the board"); the rest oldest stay first, with no ETA yet.
	if !slices.Equal(ids, []string{"wharf-a", "wharf-c"}) {
		t.Fatalf("beads = %v, want wharf-a, wharf-c", ids)
	}
	if rows[0].Platform != "new" || rows[0].Title != "wharf-a" {
		t.Fatalf("row = %+v", rows[0])
	}
	if rows[0].Expected != "—" || rows[0].Remark != "" || rows[0].Tone != "none" {
		t.Fatalf("no ETA, no remark: %+v", rows[0])
	}
	// At a people's stage it waits for who is routed there (board: "at
	// a people's stage it waits for who is routed there, in yellow").
	if rows[1].Remark != "waits for nell, yardmaster" || rows[1].Tone != "amber" {
		t.Fatalf("waits: %+v", rows[1])
	}
}

func TestArrivalsRunningFirst(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	t0 := y.now
	y.at(t0).create(t, "wharf-a", "new")
	y.at(t0.Add(time.Hour))
	b := y.create(t, "wharf-b", "new")
	sess, err := y.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.Bead)
	}
	// Board: "a running session first, then what is queued".
	if !slices.Equal(ids, []string{"wharf-b", "wharf-a"}) {
		t.Fatalf("beads = %v, want running first", ids)
	}
}

func TestArrivalsFaultFromLatestEndedSession(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	// A session that came to no good end is the red block (board: "a
	// hold and a session that came to no good end are the red block").
	b := y.create(t, "wharf-a", "new")
	sess, err := y.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.EndSession(ctx, sess.ID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	if len(rows) != 1 || rows[0].Remark != "stalled" || rows[0].Tone != "red" {
		t.Fatalf("failed: %+v", rows)
	}
	// A session at work now is the news, not how the one before it
	// ended: claimed again, no stalled remark, and running first.
	if _, err := y.Claim(ctx, b.ID, "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	board, err = y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows = board.Rows
	if len(rows) != 1 || rows[0].Remark != "" {
		t.Fatalf("running: %+v", rows)
	}
}

func TestArrivalsWellEndClearsFault(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	b := y.create(t, "wharf-a", "new")
	sess, err := y.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.EndSession(ctx, sess.ID, "died", "gone"); err != nil {
		t.Fatal(err)
	}
	// Ended well since: done is no fault, whatever came before.
	y.at(y.now.Add(time.Minute))
	sess2, err := y.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess2
	if _, err := y.Advance(ctx, b.ID, "decide", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	// In decide now: waits, not stalled.
	if rows[0].Remark != "waits for nell, yardmaster" {
		t.Fatalf("after done: %+v", rows[0])
	}
}

func TestArrivalsHeldFirst(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	b := y.create(t, "wharf-a", "decide")
	sess, err := y.Claim(ctx, b.ID, "builders", "decide", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.EndSession(ctx, sess.ID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetHold(ctx, b.ID, true, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	// Board: "held before stalled before waiting before late".
	if len(rows) != 1 || rows[0].Remark != "held" || rows[0].Tone != "red" {
		t.Fatalf("held: %+v", rows)
	}
}

func TestArrivalsWaitsWithoutMembers(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	if err := y.PutGroup(ctx, Group{Name: "lonely", Runner: "manual", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	// Decide is routed to deciders; lonely has no stage. Route review
	// to lonely and stand there.
	if _, err := y.AddRoute(ctx, Route{Stage: "review", Group: "lonely"}, "me"); err != nil {
		t.Fatal(err)
	}
	y.create(t, "wharf-a", "review")
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	// Board: "No members listed: the group itself."
	if len(rows) != 1 || rows[0].Remark != "waits for lonely" || rows[0].Tone != "amber" {
		t.Fatalf("waits: %+v", rows)
	}
}

func TestArrivalsWaitsForClaimedGroupAfterRouteRemoval(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	b := y.create(t, "wharf-a", "decide")
	if _, err := y.Claim(ctx, b.ID, "deciders", "decide", "yard"); err != nil {
		t.Fatal(err)
	}
	routes, err := y.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Stage == "decide" {
			if err := y.DeleteRoute(ctx, route.ID, "me"); err != nil {
				t.Fatal(err)
			}
		}
	}

	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	if len(rows) != 1 || rows[0].Remark != "waits for nell, yardmaster" || rows[0].Tone != "amber" {
		t.Fatalf("claimed manual group after route removal: %+v", rows)
	}
}

func TestArrivalsLate(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	// Ten beads through new in 30m stays, so the cell sits on the
	// sample floor; then one waiting bead, aged past the p90.
	t0 := y.now
	for i := 0; i < 10; i++ {
		y.at(t0.Add(time.Duration(i) * time.Hour))
		b, err := y.CreateBead(ctx, NewBead{Title: "seed", Stage: "new", Labels: []string{"difficulty:2"}}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := y.Claim(ctx, b.ID, "builders", "new", "yard"); err != nil {
			t.Fatal(err)
		}
		y.at(y.now.Add(30 * time.Minute))
		if _, err := y.Advance(ctx, b.ID, "decide", Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	y.at(t0.Add(11 * time.Hour))
	b, err := y.CreateBead(ctx, NewBead{Title: "wharf-a", ID: "wharf-a", Stage: "new", Labels: []string{"difficulty:2"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	y.at(t0.Add(17 * time.Hour))
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	var row *struct {
		Remark string
		Tone   string
	}
	for i := range rows {
		if rows[i].Bead == b.ID {
			row = &struct {
				Remark string
				Tone   string
			}{rows[i].Remark, rows[i].Tone}
		}
	}
	// Board: "past its usual time it is late, in yellow".
	if row == nil || row.Remark != "late" || row.Tone != "amber" {
		t.Fatalf("late: %+v in %+v", row, rows)
	}
}

func TestArrivalsCap(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	t0 := y.now
	for i := 0; i < 20; i++ {
		y.at(t0.Add(time.Duration(i) * time.Minute))
		y.create(t, "wharf-"+string(rune('a'+i/10))+string(rune('0'+i%10)), "new")
	}
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	if len(rows) != 15 {
		t.Fatalf("%d rows, want 15", len(rows))
	}
}

func TestArrivalsLine(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	if err := y.PutDepot(ctx, Depot{Name: "wharf", Path: t.TempDir(), Kind: DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.CreateBead(ctx, NewBead{Title: "wharf-a", ID: "wharf-a", Stage: "new", Depot: "wharf"}, "me"); err != nil {
		t.Fatal(err)
	}
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := board.Rows
	if len(rows) != 1 || rows[0].Line != "wharf" || rows[0].Platform != "new" {
		t.Fatalf("line: %+v", rows)
	}
}

func TestArrivalsPlatformsFollowFlowOrder(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	y.flowDepot(t, "wharf", "wharf-flow", "new", "review", "decide")
	y.travel(t, "wharf-a", "wharf", "decide")
	y.travel(t, "wharf-b", "wharf", "new")
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The flow's stage order, not the alphabet's and not the rows':
	// review stands between, with no row on it.
	if !slices.Equal(board.Platforms, []string{"new", "decide"}) {
		t.Fatalf("platforms = %v, want new, decide", board.Platforms)
	}
	if len(board.Rows) != 2 {
		t.Fatalf("%d rows, want 2", len(board.Rows))
	}
}

func TestArrivalsPlatformsTwoDepotsAgree(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	y.flowDepot(t, "wharf", "wharf-flow", "new", "decide")
	y.flowDepot(t, "quay", "quay-flow", "new", "decide")
	y.travel(t, "wharf-a", "wharf", "decide")
	y.travel(t, "quay-a", "quay", "new")
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(board.Platforms, []string{"new", "decide"}) {
		t.Fatalf("platforms = %v, want new, decide", board.Platforms)
	}
}

func TestArrivalsPlatformsMostBeadsDepotWins(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	y.flowDepot(t, "wharf", "wharf-flow", "new", "review", "decide")
	y.flowDepot(t, "quay", "quay-flow", "new", "decide", "review", "verify")
	y.travel(t, "wharf-a", "wharf", "new")
	y.travel(t, "wharf-b", "wharf", "review")
	y.travel(t, "wharf-c", "wharf", "decide")
	y.travel(t, "quay-a", "quay", "decide")
	y.travel(t, "quay-b", "quay", "verify")
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Three travelling in wharf against two in quay: the shared stages
	// keep wharf's order, and quay's extra stage stands where its own
	// flow puts it, after review.
	if !slices.Equal(board.Platforms, []string{"new", "review", "verify", "decide"}) {
		t.Fatalf("platforms = %v, want new, review, verify, decide", board.Platforms)
	}
}

func TestArrivalsPlatformsUnknownLast(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	y.flowDepot(t, "wharf", "wharf-flow", "new", "review")
	y.travel(t, "wharf-a", "wharf", "review")
	y.travel(t, "wharf-b", "wharf", "new")
	// No depot, so no flow knows elsewhere: as a row's platform from a
	// flow since changed, it stands last.
	y.travel(t, "stray-a", "", "elsewhere")
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(board.Platforms, []string{"new", "review", "elsewhere"}) {
		t.Fatalf("platforms = %v, want new, review, elsewhere", board.Platforms)
	}
	if len(board.Rows) != 3 {
		t.Fatalf("%d rows, want 3", len(board.Rows))
	}
}

func TestArrivalsPlatformsCoverServedRows(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	y.flowDepot(t, "wharf", "wharf-flow", "new", "decide")
	t0 := y.now
	for i := 0; i < 15; i++ {
		y.at(t0.Add(time.Duration(i) * time.Minute))
		y.travel(t, "wharf-"+string(rune('a'+i)), "wharf", "new")
	}
	// The newest stay, past the board's cap: served nowhere, so covered
	// nowhere, though the flow knows decide.
	y.at(t0.Add(time.Hour))
	y.travel(t, "wharf-z", "wharf", "decide")
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Rows) != 15 {
		t.Fatalf("%d rows, want 15", len(board.Rows))
	}
	if !slices.Equal(board.Platforms, []string{"new"}) {
		t.Fatalf("platforms = %v, want new alone", board.Platforms)
	}
}

func TestArrivalsPlatformsTypedFlow(t *testing.T) {
	y := newArrivalsTest(t)
	ctx := context.Background()
	y.flowDepot(t, "wharf", "wharf-flow", "new", "decide")
	f, err := ParseFlow("new -> approved")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "song-flow"
	if _, err := y.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetFlowPointer(ctx, "wharf", "song", "song-flow", "me"); err != nil {
		t.Fatal(err)
	}
	for _, b := range []NewBead{
		{Title: "song-a", ID: "song-a", Type: "song", Depot: "wharf", Stage: "new"},
		{Title: "song-b", ID: "song-b", Type: "song", Depot: "wharf", Stage: "approved"},
		{Title: "wharf-a", ID: "wharf-a", Depot: "wharf", Stage: "decide"},
	} {
		if _, err := y.CreateBead(ctx, b, "me"); err != nil {
			t.Fatal(err)
		}
	}
	board, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Two songs travel new, approved against one task's new, decide: the
	// depot's order is the songs', with decide where the depot's own flow
	// puts it, after new.
	if !slices.Equal(board.Platforms, []string{"new", "decide", "approved"}) {
		t.Fatalf("platforms = %v, want new, decide, approved", board.Platforms)
	}
}

func TestMergeStageOrder(t *testing.T) {
	for _, c := range []struct{ base, next, want []string }{
		{nil, []string{"new", "decide"}, []string{"new", "decide"}},
		{[]string{"new", "review", "decide"}, []string{"new", "decide", "review"}, []string{"new", "review", "decide"}},
		{[]string{"new", "review"}, []string{"new", "review", "verify"}, []string{"new", "review", "verify"}},
		{[]string{"new", "decide"}, []string{"new", "verify", "decide"}, []string{"new", "verify", "decide"}},
		{[]string{"new", "decide"}, []string{"triage", "new", "decide"}, []string{"triage", "new", "decide"}},
		{[]string{"new"}, []string{"build", "sign"}, []string{"new", "build", "sign"}},
		{[]string{"a", "b"}, []string{"b", "x", "a"}, []string{"a", "b", "x"}},
	} {
		if got := mergeStageOrder(c.base, c.next); !slices.Equal(got, c.want) {
			t.Errorf("mergeStageOrder(%v, %v) = %v, want %v", c.base, c.next, got, c.want)
		}
	}
}

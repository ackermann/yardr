package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/store"
)

// etaTest is a store with a clock the test sets: now is what the helpers
// estimate at. The store's own cell tests keep their internal helper of
// the same name, with its row reads.
type etaTest struct {
	*store.Store
	now time.Time
}

// newETATest sets the fake clock and YARDR_STORE_NOSYNC, so its callers
// stay sequential.
func newETATest(t testing.TB) *etaTest {
	t.Helper()
	t.Setenv("YARDR_"+store.NoSyncEnv, "1")
	y := &etaTest{now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	store.SetClockForTest(func() time.Time { return y.now })
	t.Cleanup(func() { store.SetClockForTest(time.Now) })
	var err error
	if y.Store, err = store.Open(filepath.Join(t.TempDir(), "y.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { y.Close(context.Background()) })
	ctx := context.Background()
	if err := y.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	return y
}

func (y *etaTest) at(t time.Time) *etaTest {
	y.now = t
	return y
}

func (y *etaTest) create(t testing.TB, title, stage string, labels ...string) *store.Bead {
	t.Helper()
	b, err := y.CreateBead(context.Background(), store.NewBead{Title: title, Stage: stage, Labels: labels}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (y *etaTest) claim(t testing.TB, id, group, stage string) *store.Session {
	t.Helper()
	s, err := y.Claim(context.Background(), id, group, stage, "yard")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (y *etaTest) advance(t testing.TB, id, stage string) {
	t.Helper()
	if _, err := y.Advance(context.Background(), id, stage, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
}

// Estimate and EstimateMany answer at the test's clock, over the test
// store.
func (y *etaTest) Estimate(ctx context.Context, id string) (*eta.Result, error) {
	return eta.EstimateViaStore(ctx, y.Store, id, y.now)
}

func (y *etaTest) EstimateMany(ctx context.Context, ids []string) (map[string]*eta.Result, error) {
	return eta.EstimateManyViaStore(ctx, y.Store, ids, y.now)
}

// estimateTest is a yard with a flow, routes and clocked cells: ten beads
// that passed through new and review in 30m stays under builders, so the
// cells sit exactly on the sample floor with a 30m median and p90, and ten
// beads sitting unheld in new ahead of whatever is estimated next.
type estimateTest struct {
	*etaTest
	flow string
}

func newEstimateTest(t testing.TB) *estimateTest {
	t.Helper()
	y := &estimateTest{etaTest: newETATest(t), flow: "code"}
	ctx := context.Background()
	f, err := store.ParseFlow("new -> review on done\nreview -> approved on done\napproved terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = y.flow
	if _, err := y.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"new", "review", "approved"} {
		if _, err := y.AddRoute(ctx, store.Route{Stage: stage, Group: "builders"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	t0 := y.now
	for i := range 10 {
		y.at(t0.Add(time.Duration(i) * time.Hour))
		// Pinned to the flow, so the move into approved closes the
		// bead: passers seed the cells without standing ahead.
		b, err := y.CreateBead(ctx, store.NewBead{Title: fmt.Sprintf("seed %d", i), Stage: "new",
			Labels: []string{"difficulty:2"}, Metadata: map[string]any{"flow": y.flow}}, "me")
		if err != nil {
			t.Fatal(err)
		}
		y.claim(t, b.ID, "builders", "new")
		y.at(y.now.Add(30*time.Minute)).advance(t, b.ID, "review")
		y.claim(t, b.ID, "builders", "review")
		y.at(y.now.Add(30*time.Minute)).advance(t, b.ID, "approved")
	}
	for i := range 10 {
		y.at(t0.Add(11*time.Hour+time.Duration(i)*time.Minute)).
			create(t, fmt.Sprintf("sitter %d", i), "new", "difficulty:2")
	}
	y.at(t0.Add(12 * time.Hour))
	return y
}

// target creates an unheld bead in new under the flow, the newest
// dispatchable bead.
func (y *estimateTest) target(t testing.TB, title string, labels ...string) *store.Bead {
	t.Helper()
	b, err := y.CreateBead(context.Background(), store.NewBead{Title: title, Stage: "new",
		Labels: labels, Metadata: map[string]any{"flow": y.flow}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStoreEstimateLine(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newEstimateTest(t)
	ctx := context.Background()
	b := y.target(t, "the bead", "difficulty:2")
	r, err := y.Estimate(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.ETA == nil {
		t.Fatalf("no ETA estimated: %+v", r)
	}
	// Ten ahead beads at 30m medians over 10 slots wait half an hour;
	// new and review contribute their 30m medians, approved is arrival.
	want := "eta usual 1h 30m (queue ~30m behind 10 beads for builders; new ~30m; review ~30m)"
	if r.ETA.Line != want {
		t.Fatalf("line:\n got %q\nwant %q", r.ETA.Line, want)
	}
	if r.ETA.Low != "1h 30m" || r.ETA.High != "1h 30m" || r.ETA.Queue != "30m" || r.ETA.Ahead != 10 {
		t.Fatalf("range: %+v", r.ETA)
	}
	if len(r.ETA.Stages) != 2 || r.ETA.Stages[0].Group != "builders" || r.ETA.Samples != 10 {
		t.Fatalf("stages: %+v", r.ETA)
	}
	if r.Late != nil {
		t.Fatalf("late at age zero: %+v", r.Late)
	}
}

func TestStoreEstimateLate(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newEstimateTest(t)
	ctx := context.Background()
	made := y.now
	waiting := y.target(t, "waiting", "difficulty:2")
	running := y.target(t, "running", "difficulty:2")
	y.claim(t, running.ID, "builders", "new")
	y.at(made.Add(6 * time.Hour))

	r, err := y.Estimate(ctx, waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Late == nil {
		t.Fatalf("waiting bead not late: %+v", r)
	}
	want := "late: waiting in new 6h, past the usual 30m (p90 entry-to-exit for difficulty:2 in builders)"
	if r.Late.Line != want {
		t.Fatalf("line:\n got %q\nwant %q", r.Late.Line, want)
	}

	r, err = y.Estimate(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.Late == nil {
		t.Fatalf("running bead not late: %+v", r)
	}
	want = "late: running in new 6h, past the usual 30m (p90 claim-to-exit for difficulty:2 in builders)"
	if r.Late.Line != want {
		t.Fatalf("line:\n got %q\nwant %q", r.Late.Line, want)
	}
	// Claimed, so no queue; the running stage's remainder is spent, so
	// the range is review's median alone.
	if r.ETA == nil || r.ETA.Queue != "" || r.ETA.Low != "30m" {
		t.Fatalf("held eta: %+v", r.ETA)
	}
}

func TestStoreEstimateMany(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newEstimateTest(t)
	ctx := context.Background()
	a := y.target(t, "first", "difficulty:2")
	b := y.target(t, "second", "difficulty:2")
	ests, err := y.EstimateMany(ctx, []string{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	// One shared snapshot: the first bead's ten ahead are the seeds,
	// the second's one more, the first bead itself.
	if ests[a.ID] == nil || ests[a.ID].ETA == nil || ests[a.ID].ETA.Ahead != 10 {
		t.Fatalf("first: %+v", ests[a.ID])
	}
	if ests[b.ID] == nil || ests[b.ID].ETA == nil || ests[b.ID].ETA.Ahead != 11 {
		t.Fatalf("second: %+v", ests[b.ID])
	}
	if _, err := y.EstimateMany(ctx, []string{"yardr-0000"}); err == nil {
		t.Fatal("unknown id estimated without an error")
	}
	if ests, err := y.EstimateMany(ctx, nil); err != nil || len(ests) != 0 {
		t.Fatalf("empty response: %v, %v", ests, err)
	}
}

func TestStoreEstimateThinQueue(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newEstimateTest(t)
	ctx := context.Background()
	b := y.target(t, "the bead", "difficulty:2")
	r, err := y.Estimate(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.ETA == nil || r.ETA.Low == "" || r.ETA.Line == "" {
		t.Fatalf("estimate without ETA: %+v", r.ETA)
	}
	if r.Late != nil {
		t.Fatalf("estimate late at age zero: %+v", r.Late)
	}
	// Under another label with no samples the stages are thin, but the
	// queue behind clocked beads is still clocked: a queue-only ETA.
	thin := y.target(t, "thin", "difficulty:9")
	r, err = y.Estimate(ctx, thin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.ETA == nil || r.ETA.Queue == "" || len(r.ETA.Stages) != 0 {
		t.Fatalf("thin estimate: %+v", r.ETA)
	}
	if r.Late != nil {
		t.Fatalf("thin estimate late: %+v", r.Late)
	}
}

// A non-operator bead's ETA names the group the store's match sends it
// to: the operator-only route is skipped for hal's bead, the any route
// taken. The any route sorts after the operator's, so the estimate must
// skip that route to reach it; the ten sitters are the operator's, so
// none stands ahead of it.
func TestStoreEstimateOriginatorRoutes(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newEstimateTest(t)
	ctx := context.Background()
	if _, err := y.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(ctx, store.Group{Name: "hal-builders", Runner: "herdr", Limit: 10}, "jo"); err != nil {
		t.Fatal(err)
	}
	anyOriginator := store.OriginatorAny
	if _, err := y.AddRoute(ctx, store.Route{Stage: "new", Group: "hal-builders", Originator: &anyOriginator}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Ten passers claimed by hal-builders clock its cell, as the seeds
	// clocked builders'.
	for i := range 10 {
		y.at(y.now.Add(time.Duration(i) * time.Minute))
		b, err := y.CreateBead(ctx, store.NewBead{Title: fmt.Sprintf("hal seed %d", i), Stage: "new",
			Labels: []string{"difficulty:2"}, Metadata: map[string]any{"flow": y.flow}}, "jo")
		if err != nil {
			t.Fatal(err)
		}
		y.claim(t, b.ID, "hal-builders", "new")
		y.at(y.now.Add(30*time.Minute)).advance(t, b.ID, "review")
		y.claim(t, b.ID, "builders", "review")
		y.at(y.now.Add(30*time.Minute)).advance(t, b.ID, "approved")
	}
	y.at(y.now.Add(time.Hour))
	b, err := y.CreateBead(ctx, store.NewBead{Title: "hal's", Stage: "new", For: "hal",
		Labels: []string{"difficulty:2"}, Metadata: map[string]any{"flow": y.flow}}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	r, err := y.Estimate(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil || r.ETA == nil || len(r.ETA.Stages) == 0 || r.ETA.Stages[0].Group != "hal-builders" {
		t.Fatalf("hal's ETA names %+v, want hal-builders first", r)
	}
	if r.ETA.Ahead != 0 {
		t.Fatalf("hal's queue counts the operator's sitters: %+v", r.ETA)
	}
}

func TestStoreEstimateAbsent(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	ctx := context.Background()
	f, err := store.ParseFlow("new -> review on done\nreview terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "code"
	if _, err := y.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := y.CreateBead(ctx, store.NewBead{Title: "the bead", Stage: "new",
		Labels: []string{"difficulty:2"}, Metadata: map[string]any{"flow": "code"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// No samples anywhere and nothing ahead: nothing to say.
	if r, err := y.Estimate(ctx, b.ID); err != nil || r != nil {
		t.Fatalf("estimated with no history: %+v, %v", r, err)
	}
}

// One response's estimate over 200 open beads: one shared queue snapshot
// and an estimate per bead.
func BenchmarkEstimateResponse200(b *testing.B) {
	y := newEstimateTest(b)
	ctx := context.Background()
	var ids []string
	for i := range 200 {
		tb := y.target(b, fmt.Sprintf("bead %d", i), "difficulty:2")
		ids = append(ids, tb.ID)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := y.EstimateMany(ctx, ids); err != nil {
			b.Fatal(err)
		}
	}
}

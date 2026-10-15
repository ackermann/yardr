package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

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
	f, err := ParseFlow("new -> review on done\nreview -> approved on done\napproved terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = y.flow
	if _, err := y.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"new", "review", "approved"} {
		if _, err := y.AddRoute(ctx, Route{Stage: stage, Group: "builders"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	t0 := y.now
	for i := range 10 {
		y.at(t0.Add(time.Duration(i) * time.Hour))
		// Pinned to the flow, so the move into approved closes the
		// bead: passers seed the cells without standing ahead.
		b, err := y.CreateBead(ctx, NewBead{Title: fmt.Sprintf("seed %d", i), Stage: "new",
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
func (y *estimateTest) target(t testing.TB, title string, labels ...string) *Bead {
	t.Helper()
	b, err := y.CreateBead(context.Background(), NewBead{Title: title, Stage: "new",
		Labels: labels, Metadata: map[string]any{"flow": y.flow}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStoreEstimateLine(t *testing.T) {
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

func TestStoreBeadViewCarriesEstimate(t *testing.T) {
	y := newEstimateTest(t)
	ctx := context.Background()
	b := y.target(t, "the bead", "difficulty:2")
	v, err := y.Bead(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.ETA == nil || v.ETA.Low == "" || v.ETA.Line == "" {
		t.Fatalf("view without ETA: %+v", v.ETA)
	}
	if v.Late != nil {
		t.Fatalf("view late at age zero: %+v", v.Late)
	}
	r, err := y.Estimate(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.ETA.Line != v.ETA.Line {
		t.Fatalf("view line %q != estimate line %q", v.ETA.Line, r.ETA.Line)
	}
	// Under another label with no samples the stages are thin, but the
	// queue behind clocked beads is still clocked: a queue-only ETA.
	thin := y.target(t, "thin", "difficulty:9")
	v, err = y.Bead(ctx, thin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.ETA == nil || v.ETA.Queue == "" || len(v.ETA.Stages) != 0 {
		t.Fatalf("thin view: %+v", v.ETA)
	}
	if v.Late != nil {
		t.Fatalf("thin view late: %+v", v.Late)
	}
}

// A non-operator bead's ETA names the group the store's match sends it
// to: the operator-only route is skipped for hal's bead, the any route
// taken. The any route sorts after the operator's, so the estimate must
// skip that route to reach it; the ten sitters are the operator's, so
// none stands ahead of it.
func TestStoreEstimateOriginatorRoutes(t *testing.T) {
	y := newEstimateTest(t)
	ctx := context.Background()
	if _, err := y.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetSetting(ctx, OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(ctx, Group{Name: "hal-builders", Runner: "herdr", Limit: 10}, "jo"); err != nil {
		t.Fatal(err)
	}
	anyOriginator := OriginatorAny
	if _, err := y.AddRoute(ctx, Route{Stage: "new", Group: "hal-builders", Originator: &anyOriginator, Priority: -1}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Ten passers claimed by hal-builders clock its cell, as the seeds
	// clocked builders'.
	for i := range 10 {
		y.at(y.now.Add(time.Duration(i) * time.Minute))
		b, err := y.CreateBead(ctx, NewBead{Title: fmt.Sprintf("hal seed %d", i), Stage: "new",
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
	b, err := y.CreateBead(ctx, NewBead{Title: "hal's", Stage: "new", For: "hal",
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
	y := newETATest(t)
	ctx := context.Background()
	f, err := ParseFlow("new -> review on done\nreview terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "code"
	if _, err := y.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddRoute(ctx, Route{Stage: "new", Group: "builders"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := y.CreateBead(ctx, NewBead{Title: "the bead", Stage: "new",
		Labels: []string{"difficulty:2"}, Metadata: map[string]any{"flow": "code"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// No samples anywhere and nothing ahead: nothing to say.
	if r, err := y.Estimate(ctx, b.ID); err != nil || r != nil {
		t.Fatalf("estimated with no history: %+v, %v", r, err)
	}
	v, err := y.Bead(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.ETA != nil || v.Late != nil {
		t.Fatalf("view estimated with no history: %+v %+v", v.ETA, v.Late)
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

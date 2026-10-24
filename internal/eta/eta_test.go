package eta

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func testGroups() []Group {
	return []Group{
		{Name: "builders", Runner: "herdr", Limit: 2},
		{Name: "reviewers", Runner: "herdr", Limit: 1},
		{Name: "people", Runner: "manual", Limit: 1},
		{Name: "far", Runner: "peer", Limit: 1, Peer: "north"},
	}
}

func routeFor(stage, group string) Route {
	return Route{Stage: stage, Groups: []string{group}}
}

func testBead(id, stage string) Bead {
	return Bead{ID: id, Stage: stage, Type: "task", Labels: []string{"difficulty:2"}, Priority: 2,
		StayStart: testNow.Add(-30 * time.Minute)}
}

func testFlow() *Flow {
	// approved is manual with no way out: the walk names it and ends,
	// with no stop note past a leg that already says unclocked.
	return &Flow{
		Stages: []string{"new", "review", "approved"},
		Outcomes: map[string][]Outcome{
			"new":    {{Name: "done", To: "review"}},
			"review": {{Name: "done", To: "approved"}},
		},
		Transitions: map[string][]string{
			"new":    {"review"},
			"review": {"approved"},
		},
	}
}

// clocked answers med, p90 from a cell with samples well above the floor.
func clocked(med, p90 time.Duration) Cell {
	return Cell{ClaimMedian: med, ClaimP90: p90, EntryMedian: med, EntryP90: p90,
		ClaimN: 42, EntryN: 42, Stays: 42}
}

func cellMap(m map[string]Cell) Cells {
	return func(group, label, stage string) Cell {
		return m[group+"\x00"+label+"\x00"+stage]
	}
}

func lines(t *testing.T, r *Result) (eta, late string) {
	t.Helper()
	if r == nil || r.ETA == nil {
		t.Fatal("no ETA estimated")
	}
	lateLine := ""
	if r.Late != nil {
		lateLine = r.Late.Line
	}
	return r.ETA.Line, lateLine
}

// The design's section 3.4 line, over cells that produce its parts: a
// numbered queue, two clocked stages and a manual stage after them.
func TestEstimateLine(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	groups := testGroups()
	routes := []Route{routeFor("new", "builders"), routeFor("review", "builders"), routeFor("approved", "people")}
	cells := cellMap(map[string]Cell{
		"builders\x00difficulty:2\x00new":    clocked(30*time.Minute, 2*time.Hour),
		"builders\x00difficulty:2\x00review": clocked(20*time.Minute, time.Hour),
		"builders\x00difficulty:3\x00new":    clocked(time.Hour, 2*time.Hour),
	})
	q := Queue{
		Order: []Queued{
			{Bead: "yardr-aa11", Stage: "new", Type: "task", Labels: []string{"difficulty:3"}, Priority: 2},
			{Bead: "yardr-bb22", Stage: "new", Type: "task", Labels: []string{"difficulty:3"}, Priority: 2},
			{Bead: "yardr-cc33", Stage: "new", Type: "task", Labels: []string{"difficulty:3"}, Priority: 2},
			{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
		},
		Live: []Live{
			{Bead: "yardr-dd44", Group: "builders", Stage: "new", StartedAt: testNow.Add(-30 * time.Minute), Labels: []string{"difficulty:2"}},
		},
	}
	// The live session's remainder is spent, freeing its slot now; the
	// three ahead beads at hour medians over 2 slots wait an hour.
	r := Estimate(b, testFlow(), routes, groups, cells, q, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 1h 50m–4h (queue ~1h behind 3 beads for builders; new ~30m; review ~20m; approved after your decision)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	if r.ETA.Low != "1h 50m" || r.ETA.High != "4h" || r.ETA.Queue != "1h" || r.ETA.Ahead != 3 {
		t.Fatalf("range: %+v", r.ETA)
	}
	if len(r.ETA.Stages) != 2 || r.ETA.Stages[0].Group != "builders" || r.ETA.Stages[1].Median != "20m" {
		t.Fatalf("stages: %+v", r.ETA.Stages)
	}
	if !reflect.DeepEqual(r.ETA.Unclocked, []string{"approved"}) {
		t.Fatalf("unclocked: %q", r.ETA.Unclocked)
	}
	if r.ETA.Samples != 42 {
		t.Fatalf("samples: %d", r.ETA.Samples)
	}

	// A templated route may end in wait. The marker is not a group that
	// needs to exist, and the ETA should use the resolved first group.
	depot := "web"
	templated := testBead("yardr-aa11", "new")
	templated.Depot = &depot
	resolved := Estimate(templated, nil,
		[]Route{{Stage: "new", Groups: []string{"{depot}-builders", "wait"}}},
		[]Group{{Name: "web-builders", Runner: "herdr"}},
		cellMap(map[string]Cell{"web-builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)}),
		Queue{}, testNow, "")
	if resolved == nil || resolved.ETA == nil || len(resolved.ETA.Stages) != 1 ||
		resolved.ETA.Stages[0].Group != "web-builders" {
		t.Fatalf("templated route ending in wait estimated as %+v, want web-builders", resolved)
	}
}

func TestEstimateJSON(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	routes := []Route{routeFor("new", "builders"), routeFor("review", "builders"), routeFor("approved", "people")}
	cells := cellMap(map[string]Cell{
		"builders\x00difficulty:2\x00new":    clocked(30*time.Minute, 2*time.Hour),
		"builders\x00difficulty:2\x00review": clocked(20*time.Minute, time.Hour),
	})
	q := Queue{Order: []Queued{{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2}}}
	r := Estimate(b, testFlow(), routes, testGroups(), cells, q, testNow, "")
	if r == nil || r.ETA == nil {
		t.Fatal("no ETA estimated")
	}
	raw, err := json.Marshal(r.ETA)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"low", "high", "stages", "unclocked", "samples", "line"}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Fatalf("eta JSON lacks %q: %s", k, raw)
		}
	}
	// No queue with a free slot: queue and ahead stay absent.
	if _, ok := got["queue"]; ok {
		t.Fatalf("zero queue present: %s", raw)
	}
	if _, ok := got["ahead"]; ok {
		t.Fatalf("zero ahead present: %s", raw)
	}
	if got["low"] != "50m" || got["high"] != "3h" || got["samples"] != 42.0 {
		t.Fatalf("eta JSON: %s", raw)
	}
}

func TestEstimateOmitsBothLines(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	routes := []Route{routeFor("new", "builders"), routeFor("review", "builders"), routeFor("approved", "people")}
	// Nothing clocked anywhere: no cells at all.
	q := Queue{Order: []Queued{{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2}}}
	if r := Estimate(b, testFlow(), routes, testGroups(), cellMap(nil), q, testNow, ""); r != nil {
		t.Fatalf("estimated with nothing clocked: %+v", r)
	}
	closed := b
	closed.Closed = true
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	if r := Estimate(closed, testFlow(), routes, testGroups(), cells, q, testNow, ""); r != nil {
		t.Fatalf("estimated a closed bead: %+v", r)
	}
}

func TestWalkFork(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{
		Stages:      []string{"new", "review", "decide"},
		Transitions: map[string][]string{"new": {"review", "decide"}},
	}
	routes := []Route{routeFor("new", "builders"), routeFor("review", "builders"), routeFor("decide", "people")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(30*time.Minute, time.Hour)})
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 30m–1h (new ~30m; then review or decide)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestWalkImplicitDone(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	// No outcomes named: the single way out is the implicit done.
	flow := &Flow{
		Stages:      []string{"new", "review"},
		Transitions: map[string][]string{"new": {"review"}},
	}
	routes := []Route{routeFor("new", "builders"), routeFor("review", "builders")}
	cells := cellMap(map[string]Cell{
		"builders\x00difficulty:2\x00new":    clocked(30*time.Minute, time.Hour),
		"builders\x00difficulty:2\x00review": clocked(20*time.Minute, time.Hour),
	})
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	// review has no way out at all: the walk stops past it, and the leg
	// is clocked, so the stop is named.
	want := "eta usual 50m–2h (new ~30m; review ~20m; unclocked after review)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestWalkNamedNonDoneStops(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	// The only way out of new is named, and it is not done: the walk
	// stops there instead of implying a transition bead done won't take.
	flow := &Flow{
		Stages:      []string{"new", "backlog"},
		Outcomes:    map[string][]Outcome{"new": {{Name: "question", To: "backlog"}}},
		Transitions: map[string][]string{"new": {"backlog"}},
	}
	routes := []Route{routeFor("new", "builders")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(30*time.Minute, time.Hour)})
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 30m–1h (new ~30m; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestWalkLoopCaps(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{
		Stages: []string{"new", "review"},
		Outcomes: map[string][]Outcome{
			"new":    {{Name: "done", To: "review"}},
			"review": {{Name: "done", To: "new"}},
		},
		Transitions: map[string][]string{"new": {"review"}, "review": {"new"}},
	}
	routes := []Route{routeFor("new", "builders"), routeFor("review", "builders")}
	cells := cellMap(map[string]Cell{
		"builders\x00difficulty:2\x00new":    clocked(30*time.Minute, time.Hour),
		"builders\x00difficulty:2\x00review": clocked(30*time.Minute, time.Hour),
	})
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	// One full loop plus one step past two stages: new, review, new.
	want := "eta usual 1h 30m–3h (new ~30m; review ~30m; new ~30m)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestWalkPeerStop(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := testFlow()
	routes := []Route{routeFor("new", "builders"), routeFor("review", "far"), routeFor("approved", "people")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(30*time.Minute, time.Hour)})
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 30m–1h (new ~30m; then review on yard north)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestWalkUnroutedStop(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := testFlow()
	// No route for review: the walk names it and stops.
	routes := []Route{routeFor("new", "builders"), routeFor("approved", "people")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(30*time.Minute, time.Hour)})
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 30m–1h (new ~30m; then review, unrouted)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestWalkTerminal(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := testFlow()
	flow.Terminal = []string{"approved"}
	routes := []Route{routeFor("new", "builders"), routeFor("review", "builders"), routeFor("approved", "people")}
	cells := cellMap(map[string]Cell{
		"builders\x00difficulty:2\x00new":    clocked(30*time.Minute, time.Hour),
		"builders\x00difficulty:2\x00review": clocked(20*time.Minute, time.Hour),
	})
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	// approved closes the bead on entry: arrival, not a leg.
	want := "eta usual 50m–2h (new ~30m; review ~20m)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestWalkNoFlow(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	routes := []Route{routeFor("new", "builders")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(30*time.Minute, time.Hour)})
	r := Estimate(b, nil, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 30m–1h (new ~30m; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestWalkManualCurrent(t *testing.T) {
	b := testBead("yardr-9c2k", "approved")
	flow := &Flow{
		Stages:      []string{"review", "approved", "merged"},
		Outcomes:    map[string][]Outcome{"approved": {{Name: "done", To: "merged"}}},
		Transitions: map[string][]string{"approved": {"merged"}},
		Terminal:    []string{"merged"},
	}
	// A manual current stage contributes no time and the walk continues
	// past it; no queue is simulated for people.
	routes := []Route{routeFor("approved", "people"), routeFor("merged", "people")}
	r := Estimate(b, flow, routes, testGroups(), cellMap(nil), Queue{}, testNow, "")
	if r != nil {
		t.Fatalf("estimated with only a manual stage ahead: %+v", r)
	}
}

func TestRangeStretch(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{Stages: []string{"new"}, Terminal: []string{}}
	routes := []Route{routeFor("new", "builders")}
	c := clocked(time.Hour, 2*time.Hour)
	c.Stays, c.SendBacks = 40, 10 // a quarter sent back: stretch 1.25
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": c})
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	// new has no way out and is clocked, so the stop is named.
	want := "eta usual 1h 15m–2h 30m (new ~1h 15m; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestRangeCurrentRemainder(t *testing.T) {
	now := testNow
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "builders")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	// Claimed and running for 20m: both ends lose the elapsed run.
	group := "builders"
	start := now.Add(-20 * time.Minute)
	b := testBead("yardr-9c2k", "new")
	b.Group, b.SessionStart = &group, &start
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, now, "")
	line, _ := lines(t, r)
	want := "eta usual 40m–1h 40m (new ~40m; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	// Running past both ends floors them at zero: a zero range prints once.
	start = now.Add(-3 * time.Hour)
	b.SessionStart = &start
	r = Estimate(b, flow, routes, testGroups(), cells, Queue{}, now, "")
	line, _ = lines(t, r)
	want = "eta usual under a minute (new ~under a minute; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestPerStageFloor(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := testFlow()
	routes := []Route{routeFor("new", "builders"), routeFor("review", "builders"), routeFor("approved", "people")}
	c := clocked(30*time.Minute, time.Hour)
	thin := clocked(20*time.Minute, time.Hour)
	thin.ClaimN, thin.EntryN = 9, 9
	cells := cellMap(map[string]Cell{
		"builders\x00difficulty:2\x00new":    c,
		"builders\x00difficulty:2\x00review": thin,
	})
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	line, _ := lines(t, r)
	// The thin stage is named, contributes nothing, and takes no stop
	// note after it; approved ends the walk without one either.
	want := "eta usual 30m–1h (new ~30m; review: too new to time; approved after your decision)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	if !reflect.DeepEqual(r.ETA.Unclocked, []string{"review", "approved"}) {
		t.Fatalf("unclocked: %q", r.ETA.Unclocked)
	}
	if len(r.ETA.Stages) != 1 {
		t.Fatalf("stages: %+v", r.ETA.Stages)
	}
}

func TestQueueFreeSlotZeroWait(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "builders")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	// Limit 2, no live sessions, one ahead bead: the dispatcher claims
	// both in the same pass, so the wait is zero and the queue part is
	// omitted, while the ahead cell still stands behind the range.
	q := Queue{Order: []Queued{
		{Bead: "yardr-aa11", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
		{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
	}}
	r := Estimate(b, flow, routes, testGroups(), cells, q, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 1h–2h (new ~1h; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	if r.ETA.Queue != "" || r.ETA.Ahead != 0 {
		t.Fatalf("queue shown for a zero wait: %+v", r.ETA)
	}
}

func TestQueueLiveRemainder(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "builders")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	// Both slots busy: one frees in 10m, the other in 50m; nothing ahead.
	q := Queue{
		Order: []Queued{{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2}},
		Live: []Live{
			{Bead: "yardr-aa11", Group: "builders", Stage: "new", StartedAt: testNow.Add(-50 * time.Minute), Labels: []string{"difficulty:2"}},
			{Bead: "yardr-bb22", Group: "builders", Stage: "new", StartedAt: testNow.Add(-10 * time.Minute), Labels: []string{"difficulty:2"}},
		},
	}
	r := Estimate(b, flow, routes, testGroups(), cells, q, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 1h 10m–2h 10m (queue ~10m for builders; new ~1h; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	if r.ETA.Queue != "10m" || r.ETA.Ahead != 0 {
		t.Fatalf("queue: %+v", r.ETA)
	}
}

func TestQueueAheadBehindFullSlots(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "reviewers")}
	groups := testGroups()
	cells := cellMap(map[string]Cell{"reviewers\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	// Limit 1 with a live session 10m in, and two ahead beads at hour
	// medians: 50m + 60m + 60m before a slot opens.
	q := Queue{
		Order: []Queued{
			{Bead: "yardr-aa11", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
			{Bead: "yardr-bb22", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
			{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
		},
		Live: []Live{
			{Bead: "yardr-cc33", Group: "reviewers", Stage: "new", StartedAt: testNow.Add(-10 * time.Minute), Labels: []string{"difficulty:2"}},
		},
	}
	r := Estimate(b, flow, routes, groups, cells, q, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 3h 50m–4h 50m (queue ~2h 50m behind 2 beads for reviewers; new ~1h; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestQueueAheadCountsOwnOriginator(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	b.Originator = "hal"
	flow := &Flow{Stages: []string{"new"}}
	any := OriginatorAny
	routes := []Route{
		{Stage: "new", Groups: []string{"builders"}},
		{Stage: "new", Originator: &any, Groups: []string{"hal-builders"}},
	}
	groups := []Group{{Name: "builders", Runner: "herdr", Limit: 1}, {Name: "hal-builders", Runner: "herdr", Limit: 1}}
	cells := cellMap(map[string]Cell{
		"builders\x00difficulty:2\x00new":     clocked(time.Hour, 2*time.Hour),
		"hal-builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour),
	})
	// The operator's bead ahead routes to builders; only hal's stands
	// ahead of hal's on hal-builders.
	q := Queue{Order: []Queued{
		{Bead: "yardr-aa11", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2, Originator: "jo"},
		{Bead: "yardr-bb22", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2, Originator: "hal"},
		{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2, Originator: "hal"},
	}}
	r := Estimate(b, flow, routes, groups, cells, q, testNow, "jo")
	line, _ := lines(t, r)
	want := "eta usual 2h–3h (queue ~1h behind 1 bead for hal-builders; new ~1h; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	if r.ETA.Queue != "1h" || r.ETA.Ahead != 1 {
		t.Fatalf("queue: %+v", r.ETA)
	}
}

func TestQueueUnclockedAhead(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "builders")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	// The ahead bead has another label with no samples: the queue is
	// unclocked, while the clocked stage still shows.
	q := Queue{Order: []Queued{
		{Bead: "yardr-aa11", Stage: "new", Type: "task", Labels: []string{"difficulty:3"}, Priority: 2},
		{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
	}}
	r := Estimate(b, flow, routes, testGroups(), cells, q, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 1h–2h (behind 1 bead, too new to time; new ~1h; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	// The unclocked queue stands behind nothing in the range.
	if r.ETA.Samples != 42 {
		t.Fatalf("samples: %d", r.ETA.Samples)
	}
}

func TestQueueCapped(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "builders")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	q := Queue{
		Order:   []Queued{{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2}},
		Running: 4, Max: 4, Capped: true,
	}
	r := Estimate(b, flow, routes, testGroups(), cells, q, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 1h–2h (behind the yard's cap; new ~1h; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
}

func TestQueuePaused(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "builders")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	order := []Queued{{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2}}

	// A paused yard names the pause instead of numbering a queue that
	// cannot move, whatever runs.
	q := Queue{Order: order, Running: 0, Max: 0, Capped: true}
	r := Estimate(b, flow, routes, testGroups(), cells, q, testNow, "")
	line, _ := lines(t, r)
	if want := "eta usual 1h–2h (yard paused (max_sessions 0); new ~1h; unclocked after new)"; line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	if r.ETA.Queue != "" || r.ETA.Ahead != 0 {
		t.Fatalf("paused queue numbered: queue %q ahead %d", r.ETA.Queue, r.ETA.Ahead)
	}

	// So does a paused group: no slot opens, so no wait is simulated.
	paused := testGroups()
	paused[0].Limit = 0
	r = Estimate(b, flow, routes, paused, cells, Queue{Order: order}, testNow, "")
	line, _ = lines(t, r)
	if want := "eta usual 1h–2h (group builders paused (limit 0); new ~1h; unclocked after new)"; line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	if r.ETA.Queue != "" || r.ETA.Ahead != 0 {
		t.Fatalf("paused queue numbered: queue %q ahead %d", r.ETA.Queue, r.ETA.Ahead)
	}

	// A paused group is the direct blocker even when the yard is paused too.
	pausedQueue := Queue{Order: order, Running: 0, Max: 0, Capped: true}
	r = Estimate(b, flow, routes, paused, cells, pausedQueue, testNow, "")
	line, _ = lines(t, r)
	if want := "eta usual 1h–2h (group builders paused (limit 0); new ~1h; unclocked after new)"; line != want {
		t.Fatalf("both pauses line:\n got %q\nwant %q", line, want)
	}
}

func TestQueueYardPauseLeavesManualAndPeerAlone(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	// The yard pause only holds work that starts an agent or script here.
	// Manual and peer groups can still take their bead, as dispatch does.
	for _, group := range []Group{
		{Name: "people", Runner: "manual", Limit: 2},
		{Name: "far", Runner: "peer", Limit: 2, Peer: "north"},
	} {
		t.Run(group.Name, func(t *testing.T) {
			order := []Queued{{Bead: b.ID, Stage: "new", Type: "task", Labels: b.Labels, Priority: b.Priority}}
			q := Queue{Order: order, Max: 0, Capped: true}
			wait := simulateQueue(b, group, cellMap(nil), q, testNow, func(Queued) (Group, bool) {
				return group, true
			})
			if wait.part != "" || wait.wait != 0 {
				t.Fatalf("yard pause blocked %s work: %+v", group.Runner, wait)
			}
		})
	}
}

func TestQueueSkipped(t *testing.T) {
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "builders"), routeFor("approved", "people")}
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	q := Queue{Order: []Queued{
		{Bead: "yardr-aa11", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
		{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
	}}
	// Claimed: its queue is zero.
	group := "builders"
	start := testNow.Add(-time.Minute)
	held := testBead("yardr-9c2k", "new")
	held.Group, held.SessionStart = &group, &start
	r := Estimate(held, flow, routes, testGroups(), cells, q, testNow, "")
	line, _ := lines(t, r)
	if strings.Contains(line, "queue") {
		t.Fatalf("queue simulated for a held bead: %q", line)
	}
	// Not in the dispatchable listing: it cannot be claimed now.
	absent := testBead("yardr-zz99", "new")
	r = Estimate(absent, flow, routes, testGroups(), cells, q, testNow, "")
	line, _ = lines(t, r)
	if strings.Contains(line, "queue") || strings.Contains(line, "behind") {
		t.Fatalf("queue simulated for a bead outside the listing: %q", line)
	}
	// A manual stage waits for a person, not a slot.
	manual := testBead("yardr-9c2k", "approved")
	mq := Queue{Order: []Queued{{Bead: "yardr-9c2k", Stage: "approved", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2}}}
	r = Estimate(manual, flow, routes, testGroups(), cells, mq, testNow, "")
	if r != nil {
		t.Fatalf("queue simulated for a manual stage: %+v", r)
	}
}

func TestQueueUncertainLive(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "reviewers")}
	thinLive := clocked(time.Hour, 2*time.Hour)
	thinLive.ClaimN, thinLive.EntryN = 9, 9
	cells := cellMap(map[string]Cell{
		"reviewers\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour),
		"reviewers\x00difficulty:3\x00new": thinLive,
	})
	// Limit 1, one live session 10m in on a thin cell: its remainder is
	// a guess, so the queue shows its number beside the clocked stage
	// without standing behind the range.
	q := Queue{
		Order: []Queued{{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2}},
		Live: []Live{
			{Bead: "yardr-aa11", Group: "reviewers", Stage: "new", StartedAt: testNow.Add(-10 * time.Minute), Labels: []string{"difficulty:3"}},
		},
	}
	r := Estimate(b, flow, routes, testGroups(), cells, q, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 1h 50m–2h 50m (queue ~50m for reviewers; new ~1h; unclocked after new)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	if r.ETA.Samples != 42 {
		t.Fatalf("samples: %d", r.ETA.Samples)
	}
	// The same queue with a thin stage too: nothing clocked, no line.
	thinStage := clocked(time.Hour, 2*time.Hour)
	thinStage.ClaimN, thinStage.EntryN = 9, 9
	cells = cellMap(map[string]Cell{
		"reviewers\x00difficulty:2\x00new": thinStage,
		"reviewers\x00difficulty:3\x00new": thinLive,
	})
	if r := Estimate(b, flow, routes, testGroups(), cells, q, testNow, ""); r != nil {
		t.Fatalf("uncertain queue alone estimated: %+v", r)
	}
}

func TestQueueClockedLiveOnly(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	flow := &Flow{Stages: []string{"new"}}
	routes := []Route{routeFor("new", "reviewers")}
	// Limit 1, one live session 10m in on a clocked cell: the queue
	// alone is clocked, and the line is only the queue and the thin
	// stage it names. The target reads another label with no samples.
	b.Labels = []string{"difficulty:9"}
	cells := cellMap(map[string]Cell{"reviewers\x00difficulty:2\x00new": clocked(time.Hour, 2*time.Hour)})
	q := Queue{
		Order: []Queued{{Bead: "yardr-9c2k", Stage: "new", Type: "task", Labels: []string{"difficulty:9"}, Priority: 2}},
		Live: []Live{
			{Bead: "yardr-aa11", Group: "reviewers", Stage: "new", StartedAt: testNow.Add(-10 * time.Minute), Labels: []string{"difficulty:2"}},
		},
	}
	r := Estimate(b, flow, routes, testGroups(), cells, q, testNow, "")
	line, _ := lines(t, r)
	want := "eta usual 50m (queue ~50m for reviewers; new: too new to time)"
	if line != want {
		t.Fatalf("line:\n got %q\nwant %q", line, want)
	}
	if r.ETA.Samples != 42 || r.ETA.Queue != "50m" || r.ETA.Ahead != 0 {
		t.Fatalf("queue-only eta: %+v", r.ETA)
	}
	if len(r.ETA.Stages) != 0 {
		t.Fatalf("stages: %+v", r.ETA.Stages)
	}
}

func TestLateWaiting(t *testing.T) {
	b := testBead("yardr-9c2k", "new")
	b.StayStart = testNow.Add(-6 * time.Hour)
	routes := []Route{routeFor("new", "builders")}
	c := clocked(time.Hour, time.Hour)
	c.EntryMedian, c.EntryP90, c.EntryN = 2*time.Hour, 2*time.Hour, 42
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": c})
	flow := &Flow{Stages: []string{"new"}}
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	if r == nil || r.Late == nil {
		t.Fatalf("no late mark: %+v", r)
	}
	want := "late: waiting in new 6h, past the usual 2h (p90 entry-to-exit for difficulty:2 in builders)"
	if r.Late.Line != want {
		t.Fatalf("line:\n got %q\nwant %q", r.Late.Line, want)
	}
	if r.Late.Clock != "waiting" || r.Late.InStage != "6h" || r.Late.P90 != "2h" {
		t.Fatalf("late: %+v", r.Late)
	}
	// At the p90 exactly it is not past it.
	b.StayStart = testNow.Add(-2 * time.Hour)
	if r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, ""); r == nil || r.Late != nil {
		t.Fatalf("late at the p90: %+v", r)
	}
}

func TestLateRunning(t *testing.T) {
	group := "builders"
	start := testNow.Add(-6 * time.Hour)
	b := testBead("yardr-9c2k", "new")
	b.Group, b.SessionStart = &group, &start
	routes := []Route{routeFor("new", "builders")}
	c := clocked(time.Hour, 2*time.Hour)
	c.EntryP90 = 5 * time.Hour // running lateness uses claim-to-exit, not stay time.
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": c})
	flow := &Flow{Stages: []string{"new"}}
	r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, "")
	if r == nil || r.Late == nil {
		t.Fatalf("no late mark: %+v", r)
	}
	want := "late: running in new 6h, past the usual 2h (p90 claim-to-exit for difficulty:2 in builders)"
	if r.Late.Line != want {
		t.Fatalf("line:\n got %q\nwant %q", r.Late.Line, want)
	}
	if r.Late.P90 != "2h" {
		t.Fatalf("running p90: got %q, want claim-to-exit p90 2h", r.Late.P90)
	}
	// Under the p90 it is not late.
	start = testNow.Add(-time.Hour)
	b.SessionStart = &start
	if r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, ""); r == nil || r.Late != nil {
		t.Fatalf("late under the p90: %+v", r)
	}
}

func TestLateNever(t *testing.T) {
	routes := []Route{routeFor("new", "builders"), routeFor("approved", "people"), routeFor("deploy", "far")}
	c := clocked(time.Hour, 2*time.Hour)
	c.EntryMedian, c.EntryP90, c.EntryN = 2*time.Hour, 2*time.Hour, 42
	cells := cellMap(map[string]Cell{"builders\x00difficulty:2\x00new": c})
	flow := &Flow{Stages: []string{"new"}}
	old := testNow.Add(-6 * time.Hour)

	cases := map[string]Bead{}
	held := testBead("yardr-9c2k", "new")
	held.Hold = true
	held.StayStart = old
	cases["held"] = held

	thin := testBead("yardr-9c2k", "new")
	thin.Labels = []string{"difficulty:3"} // no samples under it
	thin.StayStart = old
	cases["thin cell"] = thin

	manual := testBead("yardr-9c2k", "approved")
	manual.StayStart = old
	cases["manual stage"] = manual

	peer := testBead("yardr-9c2k", "deploy")
	peer.StayStart = old
	cases["peer stage"] = peer

	unrouted := testBead("yardr-9c2k", "nowhere")
	unrouted.StayStart = old
	cases["unrouted stage"] = unrouted

	for name, b := range cases {
		if r := Estimate(b, flow, routes, testGroups(), cells, Queue{}, testNow, ""); r != nil && r.Late != nil {
			t.Fatalf("%s: late: %+v", name, r.Late)
		}
	}
	// Running with no session start: no clock to compare.
	group := "builders"
	nosess := testBead("yardr-9c2k", "new")
	nosess.Group = &group
	nosess.StayStart = old
	if r := Estimate(nosess, flow, routes, testGroups(), cells, Queue{}, testNow, ""); r != nil && r.Late != nil {
		t.Fatalf("no session start: late: %+v", r.Late)
	}
}

// Mirrors eta keeps of store and limits meanings, pinned through Estimate:
// the floor, the done word, the difficulty label, route matching and spans.
package eta_test

import (
	"testing"
	"time"

	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

func TestFloorParity(t *testing.T) {
	if eta.SampleFloor != store.ETASampleFloor {
		t.Fatalf("eta.SampleFloor %d != store.ETASampleFloor %d", eta.SampleFloor, store.ETASampleFloor)
	}
	if store.DefaultOutcome != "done" {
		t.Fatalf("store.DefaultOutcome %q is not the word eta walks", store.DefaultOutcome)
	}
	if eta.OriginatorAny != store.OriginatorAny {
		t.Fatalf("eta.OriginatorAny %q != store.OriginatorAny %q", eta.OriginatorAny, store.OriginatorAny)
	}
}

// The walk follows the outcome the store calls done, whatever word that is.
func TestDoneWordParity(t *testing.T) {
	flow := &eta.Flow{
		Stages:      []string{"new", "review"},
		Outcomes:    map[string][]eta.Outcome{"new": {{Name: store.DefaultOutcome, To: "review"}}},
		Transitions: map[string][]string{"new": {"review"}},
	}
	routes := []eta.Route{{Stage: "new", Groups: []string{"builders"}}, {Stage: "review", Groups: []string{"builders"}}}
	groups := []eta.Group{{Name: "builders", Runner: "herdr", Limit: 1}}
	cells := func(string, string, string) eta.Cell {
		return eta.Cell{ClaimMedian: time.Hour, ClaimP90: 2 * time.Hour, ClaimN: 42, Stays: 42}
	}
	b := eta.Bead{ID: "yardr-9c2k", Stage: "new", Type: "task", Priority: 2}
	r := eta.Estimate(b, flow, routes, groups, cells, eta.Queue{}, time.Now(), "")
	if r == nil || r.ETA == nil || len(r.ETA.Stages) != 2 {
		t.Fatalf("walk did not follow %q: %+v", store.DefaultOutcome, r)
	}
}

// The cells eta reads are keyed by the label the store reads them by.
func TestDifficultyParity(t *testing.T) {
	for _, labels := range [][]string{
		{"difficulty:3"},
		{"size:big", "difficulty:2"},
		{"difficulty:1", "difficulty:2"},
		{"size:big"},
		nil,
	} {
		var queried []string
		cells := func(_, label, _ string) eta.Cell {
			queried = append(queried, label)
			return eta.Cell{ClaimMedian: time.Hour, ClaimP90: 2 * time.Hour, ClaimN: 42, Stays: 42}
		}
		b := eta.Bead{ID: "yardr-9c2k", Stage: "new", Type: "task", Labels: labels, Priority: 2}
		flow := &eta.Flow{Stages: []string{"new"}}
		routes := []eta.Route{{Stage: "new", Groups: []string{"builders"}}}
		groups := []eta.Group{{Name: "builders", Runner: "herdr", Limit: 1}}
		if r := eta.Estimate(b, flow, routes, groups, cells, eta.Queue{}, time.Now(), ""); r == nil {
			t.Fatalf("labels %q: no estimate", labels)
		}
		want := store.DifficultyLabel(labels)
		for _, q := range queried {
			if q != want {
				t.Fatalf("labels %q: eta queried %q, store says %q", labels, q, want)
			}
		}
	}
}

func strptr(s string) *string { return &s }
func intptr(n int) *int       { return &n }

func toEtaRoute(r store.Route) eta.Route {
	var names []string
	for _, g := range r.Groups() {
		names = append(names, g.Name)
	}
	return eta.Route{Stage: r.Stage, Type: r.Type, Depot: r.Depot, Labels: r.Labels(), BeadPriority: r.BeadPriority, Originator: r.Originator, Groups: names}
}

// eta matches the route the store matches: the first in match order the
// bead's stage, type, depot, labels and priority all satisfy. Neither
// side names an originator here; the beads read as the operator's.
func TestMatchParity(t *testing.T) {
	routes := []store.Route{
		{ID: "r1", Stage: "new", Label: strptr("difficulty:2"), AndLabels: []string{"size:big"}, Group: "builders"},
		{ID: "r2", Stage: "new", Label: strptr("difficulty:2"), Group: "reviewers"},
		{ID: "r3", Stage: "new", Type: strptr("task"), Depot: strptr("yardr"), BeadPriority: intptr(1), Group: "builders"},
	}
	store.SortRoutes(routes)
	var eroutes []eta.Route
	for _, r := range routes {
		eroutes = append(eroutes, toEtaRoute(r))
	}
	groups := []eta.Group{
		{Name: "builders", Runner: "herdr", Limit: 1},
		{Name: "reviewers", Runner: "herdr", Limit: 1},
		{Name: "people", Runner: "manual", Limit: 1},
	}
	flow := &eta.Flow{Stages: []string{"new"}}
	beads := []store.Bead{
		{Stage: "new", Type: "task", Labels: []string{"difficulty:2", "size:big"}, Priority: 2},
		{Stage: "new", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
		{Stage: "new", Type: "task", Depot: strptr("yardr"), Labels: []string{"difficulty:2"}, Priority: 1},
		{Stage: "new", Type: "task", Depot: strptr("yardr"), Labels: []string{"difficulty:2"}, Priority: 2},
		{Stage: "new", Type: "design", Labels: nil, Priority: 2},
		{Stage: "elsewhere", Type: "task", Labels: []string{"difficulty:2"}, Priority: 2},
	}
	for i, sb := range beads {
		want, ok := "", false
		if m := store.MatchRoute(routes, sb, ""); m != nil {
			want, ok = m.Worker(), true
		}
		var queried []string
		cells := func(group, _, _ string) eta.Cell {
			queried = append(queried, group)
			return eta.Cell{}
		}
		b := eta.Bead{ID: "yardr-9c2k", Stage: sb.Stage, Type: sb.Type, Depot: sb.Depot, Labels: sb.Labels, Priority: sb.Priority, Originator: sb.Originator}
		r := eta.Estimate(b, flow, eroutes, groups, cells, eta.Queue{}, time.Now(), "")
		switch {
		case !ok && r != nil:
			t.Fatalf("bead %d: store matches nothing, eta estimated %+v", i, r)
		case ok && len(queried) == 0:
			t.Fatalf("bead %d: store matches %s, eta matched nothing", i, want)
		case ok && queried[0] != want:
			t.Fatalf("bead %d: store matches %s, eta queried %s", i, want, queried[0])
		}
	}
}

// eta matches the route the store matches when originators differ: a
// named route takes only its person's beads, any takes every
// originator's, and a route without one takes only the operator's,
// however high its priority or specificity. A bead without one reads as
// the operator's.
func TestMatchOriginatorParity(t *testing.T) {
	hal := "hal"
	any := store.OriginatorAny
	routes := []store.Route{
		{ID: "r-op", Stage: "new", Label: strptr("difficulty:3"), Group: "builders"},
		{ID: "r-any", Stage: "new", Group: "hal-builders", Originator: &any},
		{ID: "r-review-op", Stage: "review", Priority: 10, Group: "builders"},
		{ID: "r-hal", Stage: "review", Group: "hal-builders", Originator: &hal},
	}
	store.SortRoutes(routes)
	var eroutes []eta.Route
	for _, r := range routes {
		eroutes = append(eroutes, toEtaRoute(r))
	}
	groups := []eta.Group{
		{Name: "builders", Runner: "herdr", Limit: 1},
		{Name: "hal-builders", Runner: "herdr", Limit: 1},
	}
	flow := &eta.Flow{Stages: []string{"new", "review"}}
	beads := []store.Bead{
		{Stage: "new", Type: "task", Labels: []string{"difficulty:3"}, Priority: 2, Originator: "hal"},
		{Stage: "new", Type: "task", Labels: []string{"difficulty:3"}, Priority: 2, Originator: "jo"},
		{Stage: "new", Type: "task", Labels: []string{"difficulty:3"}, Priority: 2},
		{Stage: "new", Type: "task", Priority: 2, Originator: "hal"},
		{Stage: "new", Type: "task", Priority: 2, Originator: "mallory"},
		{Stage: "review", Type: "task", Priority: 2, Originator: "hal"},
		{Stage: "review", Type: "task", Priority: 2, Originator: "jo"},
		{Stage: "review", Type: "task", Priority: 2},
		{Stage: "review", Type: "task", Priority: 2, Originator: "mallory"},
	}
	for i, sb := range beads {
		want, ok := "", false
		if m := store.MatchRoute(routes, sb, "jo"); m != nil {
			want, ok = m.Worker(), true
		}
		var queried []string
		cells := func(group, _, _ string) eta.Cell {
			queried = append(queried, group)
			return eta.Cell{}
		}
		b := eta.Bead{ID: "yardr-9c2k", Stage: sb.Stage, Type: sb.Type, Depot: sb.Depot, Labels: sb.Labels, Priority: sb.Priority, Originator: sb.Originator}
		r := eta.Estimate(b, flow, eroutes, groups, cells, eta.Queue{}, time.Now(), "jo")
		switch {
		case !ok && r != nil:
			t.Fatalf("bead %d: store matches nothing, eta estimated %+v", i, r)
		case ok && len(queried) == 0:
			t.Fatalf("bead %d: store matches %s, eta matched nothing", i, want)
		case ok && queried[0] != want:
			t.Fatalf("bead %d: store matches %s, eta queried %s", i, want, queried[0])
		}
	}
}

// A manual match resolves its runner too: the stage walks as a manual leg.
func TestMatchManualParity(t *testing.T) {
	sr := store.Route{ID: "r1", Stage: "new", Group: "people"}
	if m := store.MatchRoute([]store.Route{sr}, store.Bead{Stage: "new", Type: "task", Priority: 2}, ""); m == nil || m.Worker() != "people" {
		t.Fatalf("store matches %v", m)
	}
	flow := &eta.Flow{
		Stages:      []string{"new", "review"},
		Outcomes:    map[string][]eta.Outcome{"new": {{Name: "done", To: "review"}}},
		Transitions: map[string][]string{"new": {"review"}},
	}
	eroutes := []eta.Route{toEtaRoute(sr), {Stage: "review", Groups: []string{"builders"}}}
	groups := []eta.Group{{Name: "builders", Runner: "herdr", Limit: 1}, {Name: "people", Runner: "manual", Limit: 1}}
	cells := func(string, string, string) eta.Cell {
		return eta.Cell{ClaimMedian: time.Hour, ClaimP90: 2 * time.Hour, ClaimN: 42, Stays: 42}
	}
	b := eta.Bead{ID: "yardr-9c2k", Stage: "new", Type: "task", Priority: 2}
	r := eta.Estimate(b, flow, eroutes, groups, cells, eta.Queue{}, time.Now(), "")
	if r == nil || r.ETA == nil || len(r.ETA.Unclocked) != 1 || r.ETA.Unclocked[0] != "new" {
		t.Fatalf("manual match did not walk a manual leg: %+v", r)
	}
}

// eta's spans are limits' spans, through the estimated range.
func TestSpanParity(t *testing.T) {
	for _, d := range []time.Duration{0, 30 * time.Second, 59 * time.Second, time.Minute, 90 * time.Minute,
		2 * time.Hour, 25 * time.Hour, 36 * time.Hour, 48 * time.Hour, 30 * 24 * time.Hour} {
		cells := func(string, string, string) eta.Cell {
			return eta.Cell{ClaimMedian: d, ClaimP90: d, ClaimN: 42, Stays: 42}
		}
		b := eta.Bead{ID: "yardr-9c2k", Stage: "new", Type: "task", Priority: 2}
		flow := &eta.Flow{Stages: []string{"new"}}
		routes := []eta.Route{{Stage: "new", Groups: []string{"builders"}}}
		groups := []eta.Group{{Name: "builders", Runner: "herdr", Limit: 1}}
		r := eta.Estimate(b, flow, routes, groups, cells, eta.Queue{}, time.Now(), "")
		if r == nil || r.ETA == nil {
			t.Fatalf("span %v: no estimate", d)
		}
		if want := limits.Span(d); r.ETA.Low != want || r.ETA.High != want {
			t.Fatalf("span %v: eta %q/%q, limits %q", d, r.ETA.Low, r.ETA.High, want)
		}
	}
}

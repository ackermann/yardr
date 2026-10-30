// Meanings eta keeps in step with store, pinned through Estimate: the
// floor, the done word and the difficulty label.
package eta_test

import (
	"testing"
	"time"

	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/store"
)

func TestFloorParity(t *testing.T) {
	if eta.SampleFloor != store.ETASampleFloor {
		t.Fatalf("eta.SampleFloor %d != store.ETASampleFloor %d", eta.SampleFloor, store.ETASampleFloor)
	}
	if store.DefaultOutcome != "done" {
		t.Fatalf("store.DefaultOutcome %q is not the word eta walks", store.DefaultOutcome)
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

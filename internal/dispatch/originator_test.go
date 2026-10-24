package dispatch_test

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A bead whose originator has no route waits with the unrouted event naming
// whose beads have no route; a general route for the operator takes none
// of them, however high its priority.
func TestUnroutedNamesOriginator(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	must(t, s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	if _, err := s.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "jo"))
	must(t, s.PutGroup(ctx, store.Group{Name: "hal-builders", Runner: "fake", Limit: 5}, "jo"))
	jo, hal := "jo", "hal"
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders", Originator: &jo}, "jo")
	must(t, err)
	halBead, err := s.CreateBead(ctx, store.NewBead{Title: "hal's", For: "hal"}, "jo")
	must(t, err)
	joBead, err := s.CreateBead(ctx, store.NewBead{Title: "jo's"}, "jo")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 1 || got[0] != joBead.ID {
		t.Fatalf("starts = %v, want jo's bead alone", got)
	}
	evs, err := s.Events(ctx, halBead.ID, 20)
	must(t, err)
	var reason string
	for _, e := range evs {
		if e.Kind == "unrouted" {
			reason, _ = e.Data["reason"].(string)
		}
	}
	if !strings.Contains(reason, `originator "hal"`) || !strings.Contains(reason, `stage "new"`) {
		t.Fatalf("hal's unrouted reason = %q, want stage and originator hal", reason)
	}
	// Hal's own route takes hal's beads and none of jo's.
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "hal-builders", Originator: &hal}, "jo")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 2 || got[1] != halBead.ID {
		t.Fatalf("starts = %v, want hal's bead next", got)
	}
}

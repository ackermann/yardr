package store

import (
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// A bead answers to the group that claimed it, whichever of its route's
// groups that is; before the claim, and for a stage it is not in yet, the
// route's groups are named in order and nothing is chosen.
func TestStageGroupOfARouteWithSeveralGroups(t *testing.T) {
	// Sequential: the package's fake clock (via newHomeYard).
	y := newHomeYard(t)
	for _, name := range []string{"a", "b"} {
		if err := y.PutGroup(y.ctx, Group{Name: name, Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := y.AddRoute(y.ctx, Route{Stage: "new", Group: "a", When: "Weekly<80", Else: []RouteGroup{{Name: "b"}}}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := y.CreateBead(y.ctx, NewBead{Title: "x"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	look := func() (group string, work StageWork) {
		t.Helper()
		if err := y.read(func(tx *bolt.Tx) error {
			row, err := loadBead(tx, b.ID)
			if err != nil {
				return err
			}
			g, err := stageGroup(tx, row)
			if err != nil || g == nil {
				return err
			}
			group = g.Name
			routes, err := sortedRoutes(tx)
			if err != nil {
				return err
			}
			work, err = stageWork(tx, routes, row.Bead, "new")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return group, work
	}
	group, work := look()
	if group != "a" || work.Who != WhoAgents || work.Group != "a" || !slices.Equal(work.Else, []string{"b"}) || len(work.Members) != 0 {
		t.Fatalf("before the claim: group %q, work %+v; want a, then b, worked by agents", group, work)
	}
	// The dispatcher chose b: a's window was past the threshold.
	if _, err := y.Claim(y.ctx, b.ID, "b", "new", "yard", Routed{Route: "r", Passed: []PassedGroup{{Group: "a", Reason: "why"}}}); err != nil {
		t.Fatal(err)
	}
	if group, _ := look(); group != "b" {
		t.Fatalf("once claimed the bead answers to %q, want b", group)
	}
}

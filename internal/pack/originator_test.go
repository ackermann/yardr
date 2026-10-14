package pack_test

import (
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// A pack routes an agent stage to the operator's beads alone, and a stage a
// script or people work to every originator's: the plan carries any for
// those, and the import stores it.
func TestPackRoutesScriptAndPeopleStagesToAny(t *testing.T) {
	st, o := yard(t)
	p := load(t, starter)
	plan := build(t, st, p, o)
	byYard := map[string]pack.Group{}
	for _, g := range p.Groups {
		byYard[plan.Groups[g.Name]] = g
	}
	for _, obj := range plan.Objects {
		if obj.Kind != "route" || obj.Route == nil || obj.Route.Group == "" {
			continue
		}
		g, ok := byYard[obj.Route.Group]
		if !ok {
			t.Fatalf("route to %s names no pack group", obj.Route.Group)
		}
		want := ""
		if g.People || g.Run != "" {
			want = store.OriginatorAny
		}
		if obj.Route.Originator != want {
			t.Errorf("stage %s to %s plans originator %q, want %q", obj.Route.Stage, g.Name, obj.Route.Originator, want)
		}
	}
	apply(t, st, plan, answers)
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	for _, r := range routes {
		g := byName[r.Worker()]
		switch g.Runner {
		case "herdr":
			if r.Originator != nil {
				t.Errorf("agent route %s to %s carries originator %+v, want none (the operator's)", r.ID, g.Name, r.Originator)
			}
		default:
			if r.Originator == nil || *r.Originator != store.OriginatorAny {
				t.Errorf("route %s to %s (%s) carries %+v, want any", r.ID, g.Name, g.Runner, r.Originator)
			}
		}
	}
}

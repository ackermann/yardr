package pack

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/store"
)

func (b *planner) flows(flows []Flow) error {
	have, err := b.st.ListFlows(b.ctx)
	if err != nil {
		return err
	}
	pointers, err := b.st.FlowPointers(b.ctx)
	if err != nil {
		return err
	}
	// The flows that govern the depot now. A checkout git cannot read has
	// none to show; the import does not need it, so it is a note.
	now := map[string]string{}
	depotFlows, err := b.st.DepotFlows(b.ctx, b.o.Depot)
	if err != nil {
		b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("the flows governing depot %s now could not be read: %v", b.o.Depot, err))
	}
	for _, rf := range depotFlows {
		now[rf.Type] = rf.Flow.Name + " (" + rf.Source.String() + ")"
	}

	want := map[string]store.Flow{} // by store name
	after := slices.Clone(pointers)
	var ptrObjects []Object
	for _, f := range flows {
		sf := f.StoreFlow(FlowName(b.p.Name, f.Name))
		want[sf.Name] = sf
		var changes []Diff
		i := slices.IndexFunc(have, func(h store.Flow) bool { return h.Name == sf.Name })
		if i >= 0 {
			changes = flowDiff(have[i], sf)
		}
		b.add(Object{Kind: "flow", Name: sf.Name, Action: action(i >= 0, changes), Changes: changes, Flow: &sf})

		ptr := store.FlowPointer{Depot: b.o.Depot, Type: f.Type, Flow: sf.Name}
		changes = nil
		j := slices.IndexFunc(after, func(p store.FlowPointer) bool { return p.Depot == ptr.Depot && p.Type == ptr.Type })
		if j >= 0 {
			if after[j].Flow != ptr.Flow {
				changes = []Diff{{"flow", after[j].Flow, ptr.Flow}}
			}
			after[j] = ptr
		} else {
			after = append(after, ptr)
		}
		ptrObjects = append(ptrObjects, Object{Kind: "pointer", Name: ptr.String() + " -> " + ptr.Flow, Action: action(j >= 0, changes), Changes: changes, Pointer: &ptr})
		g := Governs{Type: f.Type, Flow: sf.Name, Source: "store", Now: now[f.Type], InPack: true}
		if g.Now == "" {
			g.Now = "none"
			if f.Type != "" {
				g.Now = "the depot's flow"
			}
		}
		b.plan.Governs = append(b.plan.Governs, g)
	}
	for _, o := range ptrObjects {
		b.add(o)
	}
	for _, p := range pointers {
		if p.Depot != b.o.Depot || slices.ContainsFunc(flows, func(f Flow) bool { return f.Type == p.Type }) {
			continue
		}
		b.add(Object{Kind: "pointer", Name: p.String() + " -> " + p.Flow, Action: NotInPack, Pointer: &p})
		b.plan.Governs = append(b.plan.Governs, Governs{Type: p.Type, Flow: p.Flow, Source: "store", Now: now[p.Type]})
	}
	// A depot pointer in the store wins over the repository as a whole: its
	// default.flow and its type files stop governing, though they stay.
	own := slices.IndexFunc(after, func(p store.FlowPointer) bool { return p.Depot == b.o.Depot && p.Type == "" })
	// A dir depot is no repository: its flow files are in the directory.
	where := "repository"
	if b.depot.IsDir() {
		where = "directory"
	}
	for _, rf := range depotFlows {
		if own < 0 || len(flows) == 0 || rf.Source.Kind != "repo" {
			continue
		}
		what := "the depot's beads"
		if rf.Type != "" {
			what = "beads of type " + rf.Type
			// The depot's flow takes the type over, unless the type has a
			// pointer of its own afterwards: that one governs it, and is
			// listed above.
			if !slices.ContainsFunc(after, func(p store.FlowPointer) bool { return p.Depot == b.o.Depot && p.Type == rf.Type }) {
				b.plan.Governs = append(b.plan.Governs, Governs{Type: rf.Type, Flow: after[own].Flow, Source: "store", Now: now[rf.Type]})
			}
		}
		b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("depot %s's %s carries %s (%s), which governs %s now. The import points the depot at a flow in the store, and a store pointer wins over the %s's flow files: the file stays where it is and governs nothing until the pointer is cleared (yardr depot flow %s --clear).",
			b.o.Depot, where, rf.Source.Path, rf.Source.String(), what, where, b.o.Depot))
	}
	return b.strays(want, after)
}

// flowDiff compares a stored flow with the pack's, stage by stage in the
// line format a flow file is written in.
func flowDiff(old, new store.Flow) []Diff {
	var out []Diff
	if !slices.Equal(old.Stages, new.Stages) {
		out = append(out, Diff{"stages", strings.Join(old.Stages, ", "), strings.Join(new.Stages, ", ")})
	}
	line := func(f store.Flow, s string) string {
		switch {
		case f.IsTerminal(s):
			return "terminal"
		case !slices.Contains(f.Stages, s):
			return "no such stage"
		case len(f.Transitions[s]) == 0:
			return "no way out"
		}
		return "-> " + strings.Join(f.Edges(s), " | ")
	}
	stages := slices.Clone(new.Stages)
	for _, s := range old.Stages {
		if !slices.Contains(stages, s) {
			stages = append(stages, s)
		}
	}
	for _, s := range stages {
		if o, n := line(old, s), line(new, s); o != n {
			out = append(out, Diff{"stage " + s, o, n})
		}
	}
	return out
}

// strays refuses the plan while an open bead that would follow one of the
// pack's flows sits in a stage that flow lacks: the store refuses the
// pointer and the flow for the same reason (store.SetFlowPointer,
// store.PutFlow), and an import that stopped halfway there would leave the
// yard between two states. want are the pack's flows by store name, after
// the pointers as the import would leave them.
func (b *planner) strays(want map[string]store.Flow, after []store.FlowPointer) error {
	beads, err := b.st.ListBeads(b.ctx, store.BeadFilter{})
	if err != nil {
		return err
	}
	pointer := func(depot, typ string) string {
		for _, p := range after {
			if p.Depot == depot && p.Type == typ {
				return p.Flow
			}
		}
		return ""
	}
	lost := map[string][]string{}
	for _, bead := range beads {
		if bead.Status == "closed" || store.NotWork[bead.Type] {
			continue
		}
		name := store.PinnedFlow(&bead)
		if name == "" && bead.Depot != nil {
			if name = pointer(*bead.Depot, bead.Type); name == "" {
				name = pointer(*bead.Depot, "")
			}
		}
		if f, ok := want[name]; ok && !slices.Contains(f.Stages, bead.Stage) {
			lost[name] = append(lost[name], fmt.Sprintf("%s (%s)", bead.ID, bead.Stage))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(lost)) {
		b.plan.Refused = append(b.plan.Refused, fmt.Sprintf("flow %s lacks the stage of open beads that would follow it: %s; advance them to a stage the flow has, or pin them to the flow they follow now (yardr bead update <id> --meta flow=<name>)",
			name, strings.Join(lost[name], ", ")))
	}
	return nil
}

func (b *planner) routes(flows []Flow) error {
	have, err := b.st.Routes(b.ctx)
	if err != nil {
		return err
	}
	store.SortRoutes(have)
	taken := map[string]bool{}
	for _, f := range flows {
		for _, s := range f.Stages {
			if s.Group == "" {
				if !s.End {
					b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("stage %s of flow %s names no group: the pack routes it to nobody, and a bead there waits for a hand", s.Name, f.Name))
				}
				// A stage in the flow with no group still replaces the yard's
				// route for that depot, stage and type. Leaving that route in
				// place would make the stage look unassigned in the plan while
				// beads continued going to its old group.
				var old []store.Route
				for _, r := range have {
					if r.Stage == s.Name && r.Depot != nil && *r.Depot == b.o.Depot && !r.ByBead() && deref(r.Type) == f.Type {
						old = append(old, r)
						taken[r.ID] = true
					}
				}
				if len(old) > 0 {
					name := "depot " + b.o.Depot + " stage " + s.Name
					if f.Type != "" {
						name += " type " + f.Type
					}
					var deletes []string
					for _, r := range old {
						deletes = append(deletes, r.ID)
					}
					changes := []Diff{{"group", old[0].Group, "(no route)"}}
					if len(old) > 1 {
						changes = append(changes, Diff{"routes", fmt.Sprintf("%d for this stage", len(old)), "0"})
					}
					b.add(Object{Kind: "route", Name: name + " -> (no route)", Action: Change, Changes: changes,
						Route: &RouteOp{Stage: s.Name, Depot: b.o.Depot, Type: f.Type, Delete: deletes}})
				}
				continue
			}
			g := b.p.Groups[slices.IndexFunc(b.p.Groups, func(g Group) bool { return g.Name == s.Group })]
			op := &RouteOp{Stage: s.Name, Depot: b.o.Depot, Type: f.Type, Group: b.plan.Groups[g.Name]}
			// A stage a script or people work takes every originator's
			// beads, since none burns a local subscription; an agent stage
			// takes the operator's alone, and each person's route is added
			// by hand.
			if g.People || g.Run != "" {
				op.Originator = store.OriginatorAny
			}
			// The depot's routes for the stage (and type), routes by label,
			// bead priority or named originator aside: those say something
			// the pack does not.
			var old []store.Route
			for _, r := range have {
				if r.Stage == s.Name && r.Depot != nil && *r.Depot == b.o.Depot && !r.ByBead() && deref(r.Type) == f.Type {
					old = append(old, r)
					taken[r.ID] = true
				}
			}
			var changes []Diff
			if len(old) > 0 {
				// The priority is the yard's: the pack carries none.
				op.Priority = old[0].Priority
				if old[0].Group != op.Group {
					changes = append(changes, Diff{"group", old[0].Group, op.Group})
				}
				if len(old) > 1 {
					changes = append(changes, Diff{"routes", fmt.Sprintf("%d for this stage", len(old)), "1"})
				}
				if len(changes) > 0 {
					for _, r := range old {
						op.Delete = append(op.Delete, r.ID)
					}
				}
			}
			name := "depot " + b.o.Depot + " stage " + s.Name
			if f.Type != "" {
				name += " type " + f.Type
			}
			b.add(Object{Kind: "route", Name: name + " -> " + op.Group, Action: action(len(old) > 0, changes), Changes: changes, Route: op})
		}
	}
	for _, r := range have {
		if r.Depot == nil || *r.Depot != b.o.Depot || taken[r.ID] {
			continue
		}
		name := "depot " + b.o.Depot + " stage " + r.Stage
		if r.Type != nil {
			name += " type " + *r.Type
		}
		if l := r.LabelList(); l != "" {
			name += " label " + l
		}
		if p := r.BeadPriorities(); p != "" {
			name += " beads " + p
		}
		if r.Originator != nil {
			name += " originator " + *r.Originator
		}
		b.add(Object{Kind: "route", Name: name + " -> " + r.GroupList(), Action: NotInPack,
			Route: &RouteOp{Stage: r.Stage, Depot: b.o.Depot, Type: deref(r.Type), Group: r.Worker(), Priority: r.Priority}})
	}
	return nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

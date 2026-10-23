package pack

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// maps plans the stage map of each source the pack carries one for. The map
// goes on the yard's hook of that source, as the yard's crew member that takes
// the pack's; a hook that has another map gets the pack's, and the plan
// shows both. Nothing is planned for a source the yard has no hook of, or
// whose crew member has no identity in it (the store refuses such a hook, and
// says why): the import sets nothing for it then, and the next one does.
// The tables of what a source's events say and make go on the same hook,
// among the lines it has: the pack sets its own and leaves the rest, another
// pack's and the operator's (merged). A says outcome is reported as the
// hook's as when it has one, so a table whose outcomes cross a stage of
// people wants the pack's as; a make needs the hook alone, as no crew member
// acts when an event makes a bead. A pack with no flows of its own had its
// lines held to nothing when it was read: they are held here, to the flows
// that govern the depot's beads (holdLines), or wait for the depot where the
// plan names none (waitLines).
func (b *planner) maps() error {
	const ok, missing = "ok", "missing"
	if len(b.p.Sources) == 0 {
		return nil
	}
	// The flows a pack with no flows of its own holds its lines to: the
	// depot's own, and one per governed type. Where the depot's flows
	// cannot be read, the flows step said so already, and the lines go
	// unchecked: the store judges them when an event arrives.
	var gov []Flow
	if len(b.p.Flows) == 0 && b.o.Depot != "" {
		if rfs, err := b.st.DepotFlows(b.ctx, b.o.Depot); err == nil {
			for _, rf := range rfs {
				gov = append(gov, depotFlow(rf.Type, *rf.Flow))
			}
		}
	}
	hs, err := b.st.Hooks(b.ctx)
	if err != nil {
		return err
	}
	for _, sm := range b.p.Sources {
		as := b.plan.Crew[sm.As]
		mp := MapPlan{Source: sm.Source, Pack: sm.As, As: as, Map: maps.Clone(sm.Map), Says: slices.Clone(sm.Says), Makes: slices.Clone(sm.Makes), Action: NotSet}
		var of []store.Hook
		for _, h := range hs {
			if h.Source == sm.Source {
				of = append(of, h)
			}
		}
		if len(of) > 0 {
			mp.Hook = of[0].Name
		}
		if len(b.p.Flows) == 0 {
			if b.o.Depot == "" {
				b.waitLines(sm)
			} else {
				b.holdLines(sm, gov)
			}
		}
		// The yard's source of the name, in Go or declared: without one
		// the lines are skipped, and the next apply sets them once it is
		// there; with one they are held to it, and skipped while they do
		// not fit.
		builtin, decl, unreadable, err := b.yardSource(sm.Source)
		if err != nil {
			return err
		}
		skip := ""
		switch {
		case !builtin && unreadable:
			skip = fmt.Sprintf("the yard's declaration of %s is not read by this yardr", sm.Source)
		case !builtin && decl == nil:
			skip = fmt.Sprintf("the yard has no source %s%s; the next apply sets the lines once it is there", sm.Source, packFor(sm.Source))
		default:
			if err := checkSourceLines(sm, builtin, decl); err != nil {
				skip = err.Error() + "; the lines stay unset until they fit the source"
			}
		}
		if skip != "" {
			mp.Action, mp.Skipped = Skipped, skip
			b.plan.Maps = append(b.plan.Maps, mp)
			continue
		}
		if len(of) == 0 {
			mp.Needs = append(mp.Needs, Need{"hook", missing, fmt.Sprintf("no hook receives %s, and the import makes none: yardr hook add %s --source %s --secret-file <file>, then apply the pack again", sm.Source, sm.Source, sm.Source)})
		} else {
			mp.Needs = append(mp.Needs, Need{"hook", ok, "hook " + mp.Hook})
			if len(of) > 1 {
				// Every hook with a map moves the issue, so one of them has it.
				names := make([]string, len(of))
				for i, h := range of {
					names[i] = h.Name
				}
				b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("the yard has several hooks of source %s (%s): the pack's stage map goes on %s alone", sm.Source, strings.Join(names, ", "), mp.Hook))
			}
		}
		var r store.CrewMember
		if sm.As == "" {
			// The table alone: nothing is done as a crew member.
		} else if have, err := b.st.GetCrew(b.ctx, as); err == nil {
			r = *have
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		identified := false
		if sm.As == "" {
			identified = true
		} else if id := r.Identities[sm.Source]; id.ID != "" || id.Key != "" {
			identified = true
			mp.Needs = append(mp.Needs, Need{"identity", ok, "crew member " + as + " has an identity in " + sm.Source})
		} else {
			mp.Needs = append(mp.Needs, Need{"identity", missing, fmt.Sprintf("crew member %s has no identity in %s, and a hook acts only as one that has: yardr crew identity %s --source %s --id <its id there>, then apply the pack again", as, sm.Source, as, sm.Source)})
		}
		if sm.As == "" || len(sm.Map) == 0 {
			// The tables alone, or acts-as for the reads: no issue is
			// moved, so nothing is granted.
		} else if slices.Contains(r.MaySetState(), sm.Source) {
			mp.Needs = append(mp.Needs, Need{store.SetState, ok, "crew member " + as + " may set a status in " + sm.Source})
		} else {
			mp.Needs = append(mp.Needs, Need{store.SetState, missing, fmt.Sprintf("crew member %s may not set a status in %s, so no issue is moved. The import does not grant it: the operator allows it with yardr crew set %s %s=%s",
				as, sm.Source, as, store.SetState, strings.Join(append(r.MaySetState(), sm.Source), ","))})
		}
		if mp.Hook != "" && identified {
			h := of[0]
			var changes []Diff
			// Of the hook, what the pack carries a part for: the rest is
			// the yard's, and stays.
			had := false
			if sm.As != "" {
				if h.As != as {
					changes = append(changes, Diff{"as", h.As, as})
				}
				had = h.As != ""
			}
			if len(sm.Map) > 0 {
				if !maps.Equal(h.Map, sm.Map) {
					changes = append(changes, Diff{"map", strings.Join(MapLines(h.Map), " "), strings.Join(MapLines(sm.Map), " ")})
				}
				had = had || len(h.Map) > 0
			}
			op := &MapOp{Hook: h.Name, Source: sm.Source, Says: slices.Clone(sm.Says), Makes: slices.Clone(sm.Makes)}
			op.DropSays, op.DropMakes = b.ownDrops(h, sm)
			if len(sm.Says) > 0 || len(op.DropSays) > 0 {
				if next := merged(h.Says, op.DropSays, sm.Says); !store.SameSays(h.Says, next) {
					changes = append(changes, Diff{"says", strings.Join(store.SayLines(h.Says), "; "), strings.Join(store.SayLines(next), "; ")})
				}
				had = had || len(h.Says) > 0
			}
			if len(sm.Makes) > 0 || len(op.DropMakes) > 0 {
				if next := merged(h.Makes, op.DropMakes, sm.Makes); !store.SameMakes(h.Makes, next) {
					changes = append(changes, Diff{"makes", strings.Join(store.MakeLines(h.Makes), "; "), strings.Join(store.MakeLines(next), "; ")})
				}
				had = had || len(h.Makes) > 0
			}
			mp.Action = action(had, changes)
			if mp.Action == Change {
				mp.Was = &MapOp{Hook: h.Name, Source: h.Source, As: h.As, Map: h.Map, Says: h.Says, Makes: h.Makes}
				for _, l := range store.SayLines(op.DropSays) {
					mp.Drops = append(mp.Drops, HookDrop{Table: "says", Line: l})
				}
				for _, l := range store.MakeLines(op.DropMakes) {
					mp.Drops = append(mp.Drops, HookDrop{Table: "makes", Line: l})
				}
			}
			if sm.As != "" {
				op.As = as
				if len(sm.Map) > 0 {
					op.Map = maps.Clone(sm.Map)
				}
			}
			b.add(Object{Kind: "map", Name: mapName(h.Name, op.As), Action: mp.Action, Changes: changes, Map: op})
		}
		b.plan.Maps = append(b.plan.Maps, mp)
	}
	return nil
}

// depotFlow is the flow that governs the depot's beads of type typ as a
// pack flow, so the lines of a pack with no flows of its own are held to it
// with the same checks the reader holds a pack's lines to its flows. A
// stage's outcomes are the store's, which already say done where a single
// way out offers it, as stageOutcomes does for a pack's.
func depotFlow(typ string, f store.Flow) Flow {
	out := Flow{Name: f.Name, Type: typ}
	for _, s := range f.Stages {
		out.Stages = append(out.Stages, Stage{Name: s, Outcomes: f.OutcomesFrom(s), End: f.IsTerminal(s)})
	}
	return out
}

// holdLines holds the source's lines to the flows that govern the depot's
// beads, gov, for a pack with no flows of its own. A line for a type none
// governs is left to the store, which judges it when an event arrives; a
// line the governing flow lacks the stage or the outcome for refuses the
// plan, in the same words the reader refuses a pack's own lines.
func (b *planner) holdLines(sm StageMap, gov []Flow) {
	for i, m := range sm.Makes {
		flows := flowsFor(gov, m.Type)
		if len(flows) == 0 {
			continue
		}
		if err := unmade(m, flows); err != nil {
			b.plan.Refused = append(b.plan.Refused, fmt.Sprintf("sources.%s.makes[%d]: %v", sm.Source, i, err))
		}
	}
	for i, say := range sm.Says {
		check := say
		if named, ok := say.Where[store.SayType]; ok {
			var kept []string
			for _, typ := range store.WhereValues(named) {
				if len(flowsFor(gov, typ)) > 0 {
					kept = append(kept, typ)
				}
			}
			if len(kept) == 0 {
				continue
			}
			check.Where = maps.Clone(say.Where)
			check.Where[store.SayType] = store.JoinWhere(kept)
		}
		if err := unrouted(check, gov); err != nil {
			b.plan.Refused = append(b.plan.Refused, fmt.Sprintf("sources.%s.says[%d]: no bead of depot %s could take it: %v, so the flow would refuse it every time", sm.Source, i, b.o.Depot, err))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(sm.Map)) {
		typ, stage, _ := strings.Cut(k, ":")
		flows := flowsFor(gov, typ)
		if len(flows) == 0 {
			continue
		}
		if !hasStage(flows, stage) {
			b.plan.Refused = append(b.plan.Refused, fmt.Sprintf("sources.%s.hears.%s.%s: %s stage %s (stages: %s)", sm.Source, typ, stage, lacking(flows), stage, strings.Join(stageNames(flows), ", ")))
		}
	}
}

// waitLines notes that the source's lines wait for the depot: with no depot
// named no flow governs them yet, and each is held to the flow the depot
// follows once one is.
func (b *planner) waitLines(sm StageMap) {
	for _, l := range store.SayLines(sm.Says) {
		b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("the says line %q waits for the depot: its outcome is held to the flow the depot follows", l))
	}
	for _, l := range store.MakeLines(sm.Makes) {
		b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("the makes line %q waits for the depot: its stage is held to the flow the depot follows", l))
	}
	for _, l := range MapLines(sm.Map) {
		b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("the hears line %q waits for the depot: its stage is held to the flow the depot follows", l))
	}
}

// ownDrops are the hook's says and makes lines the import takes off: the
// pack's own, those it set at its last import into this depot and no longer
// gives. The pack evolving drops its lines freely; a line it never set, or
// that another pack's last import lists too, is not its to drop and stays,
// the operator's hand lines with it. A table the pack has stopped giving is
// still asked when its last import recorded lines there, so those lines can
// be taken off before the record is replaced.
func (b *planner) ownDrops(h store.Hook, sm StageMap) (says []store.Say, makes []store.Make) {
	var prevSays []store.Say
	var prevMakes []store.Make
	if b.rec != nil {
		for _, po := range b.rec.Objects {
			if po.Kind == "map" && po.Name == h.Name {
				prevSays, prevMakes = po.Says, po.Makes
				break
			}
		}
	}
	other := b.others(h.Name)
	says = leftOf(h.Says, prevSays, sm.Says, other.says)
	makes = leftOf(h.Makes, prevMakes, sm.Makes, other.makes)
	return says, makes
}

// otherPacks is what the last imports of other packs set on a hook: their
// says and makes lines, as yardr hook add takes them, and whether one acts on
// the hook as a crew member. It is theirs, and an import of this pack takes
// none of it off.
type otherPacks struct {
	says, makes []string
	as          bool
}

// others reads them from the records. The same pack imported into another
// depot is not another pack: it is this one.
func (b *planner) others(hook string) (out otherPacks) {
	for _, r := range b.all {
		if r.Pack == b.p.Name {
			continue
		}
		for _, po := range r.Objects {
			if po.Kind == "map" && po.Name == hook {
				out.says = append(out.says, store.SayLines(po.Says)...)
				out.makes = append(out.makes, store.MakeLines(po.Makes)...)
				out.as = out.as || po.CrewMember != ""
			}
		}
	}
	return out
}

// leftOf are the lines of prev that the table have has and neither the pack
// still gives (want) nor another pack's last import lists (elsewhere): the
// pack's own, which it let go of. In the order of have.
func leftOf[L fmt.Stringer](have, prev, want []L, elsewhere []string) []L {
	was := lineSet(prev)
	for _, l := range elsewhere {
		delete(was, l)
	}
	for l := range lineSet(want) {
		delete(was, l)
	}
	var out []L
	for _, l := range have {
		if was[l.String()] {
			out = append(out, l)
		}
	}
	return out
}

// merged is a hook's table once the pack's lines are set on it, lines being
// the hook's says or makes: the table as it is (have), less the pack's own
// lines it no longer gives (drop), with the pack's lines (want) set in it.
// The first line an event matches counts, so the pack's lines keep the order
// the pack wrote them in, and the rest of the table keeps its own:
//
//   - the pack's lines the table has stand in the places the pack's lines
//     stood in, so an import of the same lines changes nothing, and one that
//     only reorders them reorders them alone;
//   - a line the pack gains goes before the pack line that follows it, where
//     the pack wrote it, and one that follows its last line the table has
//     goes last, after the rest; with none of its lines there, all go last;
//   - a line of another pack or of the operator keeps its place.
func merged[L fmt.Stringer](have, drop, want []L) []L {
	dropped, wanted, on := lineSet(drop), lineSet(want), lineSet(have)
	// fill are the pack's lines the table has, in the pack's order, and
	// add[n] the lines it lacks that come before fill[n] in the pack, the
	// last of them after the last of fill. A line the pack gives twice
	// stands once.
	var fill []L
	add := [][]L{nil}
	seen := map[string]bool{}
	for _, l := range want {
		k := l.String()
		switch {
		case seen[k]:
		case on[k]:
			fill = append(fill, l)
			add = append(add, nil)
		default:
			add[len(add)-1] = append(add[len(add)-1], l)
		}
		seen[k] = true
	}
	out := make([]L, 0, len(have)+len(want))
	n := 0
	placed := map[string]bool{}
	for _, l := range have {
		switch k := l.String(); {
		case wanted[k]:
			// A line the table has twice leaves its second place empty.
			if placed[k] {
				continue
			}
			placed[k] = true
			if n < len(fill) {
				out = append(append(out, add[n]...), fill[n])
				n++
			}
		case dropped[k]:
		default:
			out = append(out, l)
		}
	}
	return append(out, add[len(fill)]...)
}

// lineSet are the lines as they are written, for membership.
func lineSet[L fmt.Stringer](lines []L) map[string]bool {
	out := make(map[string]bool, len(lines))
	for _, l := range lines {
		out[l.String()] = true
	}
	return out
}

// optional names the sources the pack could use that the yard has nothing
// of: no source of the name and no hook of it. A source the pack itself
// declares is not among them: the import adds it. Each carries the command
// that sets it up: the pack apply that brings a source the yard lacks, or
// the hook add that receives one it has in Go.
func (b *planner) optional() error {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, pr := range b.p.Crew {
		for _, s := range pr.Subscribe {
			add(s.Name)
		}
		for _, s := range pr.OpenThreads {
			add(s)
		}
	}
	for _, sm := range b.p.Sources {
		add(sm.Source)
	}
	hs, err := b.st.Hooks(b.ctx)
	if err != nil {
		return err
	}
	hooked := map[string]bool{}
	for _, h := range hs {
		hooked[h.Source] = true
	}
	for _, name := range names {
		if ownSource(name) {
			continue
		}
		if hooked[name] || slices.ContainsFunc(b.p.Declared, func(d Declared) bool { return d.Name == name }) {
			continue
		}
		if _, err := b.st.GetSource(b.ctx, name); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, ok := hooks.AdapterFor(name, nil, ""); ok {
			setup := fmt.Sprintf("yardr hook add %s --source %s --secret-file <file>", name, name)
			b.plan.Optional = append(b.plan.Optional, OptionalSource{
				Source: name,
				Setup:  setup,
				Detail: fmt.Sprintf("no hook receives %s: %s", name, setup),
			})
			continue
		}
		s := OptionalSource{Source: name, Detail: fmt.Sprintf("the yard has no source %s", name)}
		// packFor is " (yardr pack apply <name> brings it)", or "" for a
		// source no pack brings, which no command sets up.
		if p := packFor(name); p != "" {
			s.Detail += p
			s.Setup = fmt.Sprintf("yardr pack apply %s", name)
		}
		b.plan.Optional = append(b.plan.Optional, s)
	}
	return nil
}

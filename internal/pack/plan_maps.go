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
// and need the hook alone: no crew member acts when an event moves a bead
// or makes one.
func (b *planner) maps() error {
	const ok, missing = "ok", "missing"
	if len(b.p.Sources) == 0 {
		return nil
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
			if len(sm.Says) > 0 {
				if !store.SameSays(h.Says, sm.Says) {
					changes = append(changes, Diff{"says", strings.Join(store.SayLines(h.Says), "; "), strings.Join(store.SayLines(sm.Says), "; ")})
				}
				had = had || len(h.Says) > 0
			}
			if len(sm.Makes) > 0 {
				if !store.SameMakes(h.Makes, sm.Makes) {
					changes = append(changes, Diff{"makes", strings.Join(store.MakeLines(h.Makes), "; "), strings.Join(store.MakeLines(sm.Makes), "; ")})
				}
				had = had || len(h.Makes) > 0
			}
			mp.Action = action(had, changes)
			if mp.Action == Change {
				mp.Was = &MapOp{Hook: h.Name, Source: h.Source, As: h.As, Map: h.Map, Says: h.Says, Makes: h.Makes}
				mp.Drops = b.hookDrops(h, sm)
			}
			op := &MapOp{Hook: h.Name, Source: sm.Source, Says: slices.Clone(sm.Says), Makes: slices.Clone(sm.Makes)}
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

// hookDrops are the hook's says and makes lines setting the pack's tables
// would drop: those the pack does not give, beyond the pack's own lines
// from its last import into this depot, which the pack evolving drops
// freely. Each carries whose it is: another pack's import that left it, or
// the operator's hand. Only the tables the pack carries are asked: a table
// it does not carry stays as the yard has it.
func (b *planner) hookDrops(h store.Hook, sm StageMap) []HookDrop {
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
	var out []HookDrop
	if len(sm.Says) > 0 {
		want := map[string]bool{}
		for _, l := range store.SayLines(sm.Says) {
			want[l] = true
		}
		had := map[string]bool{}
		for _, l := range store.SayLines(prevSays) {
			had[l] = true
		}
		for _, l := range store.SayLines(h.Says) {
			if want[l] || had[l] {
				continue
			}
			out = append(out, HookDrop{Table: "says", Line: l, Owner: b.lineOwner(h.Name, l, true)})
		}
	}
	if len(sm.Makes) > 0 {
		want := map[string]bool{}
		for _, l := range store.MakeLines(sm.Makes) {
			want[l] = true
		}
		had := map[string]bool{}
		for _, l := range store.MakeLines(prevMakes) {
			had[l] = true
		}
		for _, l := range store.MakeLines(h.Makes) {
			if want[l] || had[l] {
				continue
			}
			out = append(out, HookDrop{Table: "makes", Line: l, Owner: b.lineOwner(h.Name, l, false)})
		}
	}
	return out
}

// lineOwner is whose the hook's line is: another pack's import that left it
// there, or the operator's hand. says says which table to read the records'
// lines from.
func (b *planner) lineOwner(hook, line string, says bool) string {
	var owners []string
	for _, r := range b.all {
		if r.Pack == b.p.Name && r.Depot == b.o.Depot {
			continue // the pack's own last import is not another's
		}
		for _, po := range r.Objects {
			if po.Kind != "map" || po.Name != hook {
				continue
			}
			var lines []string
			if says {
				lines = store.SayLines(po.Says)
			} else {
				lines = store.MakeLines(po.Makes)
			}
			if slices.Contains(lines, line) {
				owners = append(owners, "pack "+r.Pack+" in "+inDepot(r.Depot))
				break
			}
		}
	}
	if len(owners) > 0 {
		slices.Sort(owners)
		return strings.Join(slices.Compact(owners), ", ")
	}
	return "the operator's"
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

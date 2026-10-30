package server

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// packOptions are the options a plan for depot is built with from the
// mappings a reading or a card names: the terminal's shaping flags, held
// in the store's shape so the card and the carrying out build alike.
func packOptions(depot, home string, m store.Mappings) pack.Options {
	return pack.Options{Depot: depot, Flow: m.Flow, Crew: m.Crew, Groups: m.Groups, Bound: m.Bound(), Home: home}
}

// mappingAnswers are what the mappings answer of a plan: the binds and
// members as answers to its questions, and the open-threads and calls as
// the apply's grants, each as the terminal's flag of the same words gives
// it, with no terminal to ask in. A mapping that names what the pack has
// not, or answers one twice, is refused in the flag's words.
func mappingAnswers(ctx context.Context, yard store.API, plan *pack.Plan, m store.Mappings, home string) (pack.Answers, map[string]map[string]bool, map[string]map[string]bool, error) {
	settings, err := yard.Settings(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	answers, err := bindAnswers(plan, m, home, settings[store.PermissionsPresetSetting])
	if err != nil {
		return nil, nil, nil, err
	}
	open, err := openAnswersFor(plan, m)
	if err != nil {
		return nil, nil, nil, err
	}
	calls, err := callAnswersFor(plan, m)
	if err != nil {
		return nil, nil, nil, err
	}
	return answers, open, calls, nil
}

// bindAnswers reads the binds and members into answers to the plan's
// questions, as the terminal's packAnswers does unattended: a new crew
// member works in the yard's home, a group still asked about is new, and
// a kind with no preset in its bind gets the yard's own arguments.
func bindAnswers(plan *pack.Plan, m store.Mappings, home, preset string) (pack.Answers, error) {
	answers := pack.Answers{}
	asked := map[string]bool{}
	for _, q := range plan.Questions {
		asked[q.ID] = true
		if strings.HasPrefix(q.ID, "crew.") && q.Field == "dir" {
			answers[q.ID] = home
		}
		if _, name, _ := strings.Cut(q.Object, " "); q.Field == "group" {
			answers[q.ID] = name
		}
	}
	set := func(id, v string) {
		if asked[id] {
			answers[id] = v
		}
	}
	for _, kv := range m.Binds {
		name, v, ok := strings.Cut(kv, "=")
		v, label, hasLabel := strings.Cut(v, ",")
		kind, model, _ := strings.Cut(v, ":")
		if !ok || name == "" || kind == "" {
			return nil, fmt.Errorf("--bind %q: want <group>=<kind>[:<model>][,<preset>]", kv)
		}
		obj, names := "group", plan.Groups
		if rest, is := strings.CutPrefix(name, "crew:"); is {
			obj, names, name = "crew", plan.Crew, rest
		}
		yard, ok := names[name]
		if !ok {
			return nil, fmt.Errorf("--bind %q: the pack has no %s %s (it has: %s)", kv, obj, name, strings.Join(slices.Sorted(maps.Keys(names)), ", "))
		}
		set(obj+"."+yard+".kind", kind)
		set(obj+"."+yard+".model", model)
		if hasLabel {
			args, err := presetArgsFor(kind, label)
			if err != nil {
				return nil, fmt.Errorf("--bind %q: %w", kv, err)
			}
			set(obj+"."+yard+".args", strings.Join(args, " "))
		}
	}
	for _, kv := range m.Members {
		name, v, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("--members %q: want <group>=a,b", kv)
		}
		yard, ok := plan.Groups[name]
		if !ok {
			return nil, fmt.Errorf("--members %q: the pack has no group %s (it has: %s)", kv, name, strings.Join(slices.Sorted(maps.Keys(plan.Groups)), ", "))
		}
		set("group."+yard+".members", v)
	}
	for _, q := range plan.Questions {
		kind, bound := answers[strings.TrimSuffix(q.ID, "args")+"kind"]
		if _, done := answers[q.ID]; q.Field == "args" && bound && !done {
			answers[q.ID] = strings.Join(yardArgsFor(kind, preset), " ")
		}
	}
	return answers, nil
}

// openAnswersFor are the open-threads the mappings grant or refuse, by
// the yard's crew member: a flagged source the plan may grant is a yes,
// one it asks for that the flag leaves out is a recorded no.
func openAnswersFor(plan *pack.Plan, m store.Mappings) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	named := map[string][]string{}
	for _, kv := range m.OpenThreads {
		name, v, ok := strings.Cut(kv, "=")
		if !ok || name == "" || v == "" {
			return nil, fmt.Errorf("--open-threads %q: want <pack's crew member>=<source>[,<source>], or =none", kv)
		}
		if _, ok := plan.Crew[name]; !ok {
			return nil, fmt.Errorf("--open-threads %q: the pack has no crew member %s (it has: %s)", kv, name, strings.Join(slices.Sorted(maps.Keys(plan.Crew)), ", "))
		}
		i := slices.IndexFunc(plan.OpenThreads, func(ot pack.OpenThreads) bool { return ot.Pack == name })
		if i < 0 {
			return nil, fmt.Errorf("--open-threads %q: the pack asks for no source its crew member %s may open threads in (open_threads)", kv, name)
		}
		if _, twice := named[name]; twice {
			return nil, fmt.Errorf("--open-threads %q: %s is answered twice", kv, name)
		}
		named[name] = []string{}
		if v == "none" {
			continue
		}
		for _, src := range strings.Split(v, ",") {
			if src = strings.TrimSpace(src); !slices.Contains(plan.OpenThreads[i].Wants, src) {
				return nil, fmt.Errorf("--open-threads %q: the pack does not ask that %s may open threads in %q (it asks for: %s)", kv, name, src, strings.Join(plan.OpenThreads[i].Wants, ", "))
			}
			named[name] = append(named[name], src)
		}
	}
	for _, ot := range plan.OpenThreads {
		yes, flagged := named[ot.Pack]
		if !flagged {
			continue
		}
		answers := map[string]bool{}
		for _, src := range ot.Wants {
			switch {
			case slices.Contains(yes, src) && ot.Grantable(src):
				answers[src] = true
			case slices.Contains(ot.Asks, src):
				answers[src] = false
			}
		}
		if len(answers) > 0 {
			out[ot.CrewMember] = answers
		}
	}
	return out, nil
}

// callAnswersFor are the calls the mappings grant or refuse, by grantee
// and call, as openAnswersFor is for threads.
func callAnswersFor(plan *pack.Plan, m store.Mappings) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	named := map[int][]string{}
	for _, kv := range m.Calls {
		name, v, ok := strings.Cut(kv, "=")
		src, words, ok2 := strings.Cut(v, ":")
		if !ok || !ok2 || name == "" || src == "" || words == "" {
			return nil, fmt.Errorf("--calls %q: want <group>=<source>:<call>[,<call>] with read for every read call, crew:<crew member>=… for a crew member, or <source>:none", kv)
		}
		member, isCrew := strings.CutPrefix(name, "crew:")
		names, what := plan.Groups, "group"
		if isCrew {
			names, what = plan.Crew, "crew member"
		} else {
			member = name
		}
		if _, ok := names[member]; !ok {
			return nil, fmt.Errorf("--calls %q: the pack has no %s %s (it has: %s)", kv, what, member, strings.Join(slices.Sorted(maps.Keys(names)), ", "))
		}
		i := slices.IndexFunc(plan.Calls, func(c pack.Calls) bool { return callsFlagOf(c) == name && c.Source == src })
		if i < 0 {
			return nil, fmt.Errorf("--calls %q: the pack asks for no call of the source %s for its %s %s (calls)", kv, src, what, member)
		}
		if _, twice := named[i]; twice {
			return nil, fmt.Errorf("--calls %q: %s is answered twice for the source %s", kv, name, src)
		}
		named[i] = []string{}
		if words == "none" {
			continue
		}
		for _, w := range strings.Split(words, ",") {
			if w = strings.TrimSpace(w); !slices.Contains(plan.Calls[i].Wants, w) {
				return nil, fmt.Errorf("--calls %q: the pack does not ask that %s may call %q of %s (it asks for: %s)", kv, name, w, src, strings.Join(plan.Calls[i].Wants, ", "))
			}
			named[i] = append(named[i], w)
		}
	}
	for i, c := range plan.Calls {
		yes, flagged := named[i]
		if !flagged {
			continue
		}
		who := c.Grantee().String()
		for _, w := range c.Wants {
			switch {
			case slices.Contains(yes, w) && c.Grantable(w):
				if out[who] == nil {
					out[who] = map[string]bool{}
				}
				out[who][pack.CallKey(c.Source, w)] = true
			case slices.Contains(c.Asks, w):
				if out[who] == nil {
					out[who] = map[string]bool{}
				}
				out[who][pack.CallKey(c.Source, w)] = false
			}
		}
	}
	return out, nil
}

// callsFlagOf is the name a calls request has in --calls, as the
// terminal names it.
func callsFlagOf(c pack.Calls) string {
	if c.CrewMember != "" {
		return "crew:" + c.Pack
	}
	return c.Pack
}

// presetArgsFor are the arguments of kind's permission preset called
// label, as a bind names it; none is no arguments.
func presetArgsFor(kind, label string) ([]string, error) {
	if store.PresetLabel(label) == "none" {
		return nil, nil
	}
	if perm, ok := agents.Preset(kind, label); ok {
		return perm.Args, nil
	}
	have := make([]string, 0, len(agents.PresetLabels(kind))+1)
	for _, l := range agents.PresetLabels(kind) {
		have = append(have, store.PresetLabel(l))
	}
	return nil, fmt.Errorf("%s has no permission preset %q (it has: %s)", kind, label, strings.Join(append(have, "none"), ", "))
}

// yardArgsFor are the arguments a new agent of kind gets unattended:
// the yard preset's for its kind where the yard was made with one, else
// the kind's own unattended arguments.
func yardArgsFor(kind, preset string) []string {
	if preset != "" {
		if perm, ok := agents.Preset(kind, preset); ok {
			return perm.Args
		}
	}
	return agents.UnattendedArgs(kind)
}

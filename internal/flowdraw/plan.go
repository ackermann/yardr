package flowdraw

import (
	"slices"

	"yardr.dev/yardr/internal/store"
)

// plan is what of a flow is drawn where, before any coordinate: which
// stages are platforms on the line and in what order, which are sidings,
// which transitions the track itself carries and which need a line of their
// own. It is a function of the flow alone, so a flow file, a pack and a
// depot draw the same line for the same flow, whoever works its stages.
type plan struct {
	// line is the platforms from left to right: the order a bead travels
	// from the first stage, the terminal stages last.
	line []string
	// sidings are the stages off the line: no outcome leads out of one, so
	// only a person moves a bead on from there.
	sidings []string
	// track says, per platform but the last, whether the track to the next
	// platform carries a transition: the stage's way on. Where it does not,
	// the two only stand next to each other.
	track []bool
	// loops are the transitions between platforms the track does not carry,
	// drawn below it: one per pair of stages.
	loops []loop
	// spurs are the transitions into and out of a siding.
	spurs []spur
	// signals are the platforms that carry a signal.
	signals []string
}

// loop is the transitions from one platform to another that are drawn below
// the track: back to an earlier platform (review to new on changes), to
// itself, or forward past the next one.
type loop struct {
	from, to string
	// outcomes are the outcomes that take it, in the flow's order; none for
	// a move only made by hand.
	outcomes []string
}

// spur joins a platform and a siding: into the siding when in, out of it
// onto the line otherwise.
type spur struct {
	siding, stage string
	in            bool
	outcomes      []string
}

// Changes is the outcome a reviewer reports to send work back. A stage that
// offers it is a review, and carries a signal.
const Changes = "changes"

// way is where a bead in stage goes when its work ends well: the stage of
// the outcome done, else of the first outcome, else (a stage no outcome
// leads out of) its first transition.
func way(f *store.Flow, stage string) (string, bool) {
	outs := f.Outcomes[stage]
	for _, o := range outs {
		if o.Name == store.DefaultOutcome {
			return o.To, true
		}
	}
	if len(outs) > 0 {
		return outs[0].To, true
	}
	if next := f.Transitions[stage]; len(next) > 0 {
		return next[0], true
	}
	return "", false
}

func planOf(f *store.Flow) plan {
	var p plan
	if len(f.Stages) == 0 {
		return p
	}
	// The main path: from the first stage, each stage's way on, until a
	// terminal stage or a stage already passed.
	path := []string{f.Stages[0]}
	for cur := f.Stages[0]; !f.IsTerminal(cur); {
		next, ok := way(f, cur)
		if !ok || slices.Contains(path, next) {
			break
		}
		path = append(path, next)
		cur = next
	}
	// Every stage in the order a walk from the first stage reaches it, a
	// stage's transitions in the order the flow gives them; then the stages
	// nothing leads to, as the flow lists them.
	order := []string{f.Stages[0]}
	for i := 0; i < len(order); i++ {
		for _, to := range f.Transitions[order[i]] {
			if !slices.Contains(order, to) {
				order = append(order, to)
			}
		}
		if i == len(order)-1 {
			for _, s := range f.Stages {
				if !slices.Contains(order, s) {
					order = append(order, s)
					break
				}
			}
		}
	}
	for _, s := range order {
		if !slices.Contains(path, s) && !f.IsTerminal(s) && len(f.Outcomes[s]) == 0 {
			p.sidings = append(p.sidings, s)
		} else {
			p.line = append(p.line, s)
		}
	}
	// The walk may reach a stage of the main path before one that precedes
	// it there (a move by hand that skips ahead). The path's stages keep the
	// places they hold between them, in the path's order; a stage off the
	// path keeps its own.
	next := 0
	for i, s := range p.line {
		if slices.Contains(path, s) {
			p.line[i] = path[next]
			next++
		}
	}
	slices.SortStableFunc(p.line, func(a, b string) int {
		switch ta, tb := f.IsTerminal(a), f.IsTerminal(b); {
		case ta == tb:
			return 0
		case tb:
			return -1
		}
		return 1
	})

	at := func(s string) int { return slices.Index(p.line, s) }
	p.track = make([]bool, max(len(p.line)-1, 0))
	for i, s := range p.line {
		if slices.ContainsFunc(f.Outcomes[s], func(o store.Outcome) bool { return o.Name == Changes }) {
			p.signals = append(p.signals, s)
		}
		on, ok := way(f, s)
		carried := ok && slices.Contains(path, s) && i+1 < len(p.line) && p.line[i+1] == on
		if carried {
			p.track[i] = true
		}
		for _, to := range targets(f, s) {
			outs := outcomesTo(f, s, to)
			switch {
			case carried && to == on:
			case slices.Contains(p.sidings, to):
				p.spurs = append(p.spurs, spur{siding: to, stage: s, in: true, outcomes: outs})
			case at(to) >= 0:
				p.loops = append(p.loops, loop{from: s, to: to, outcomes: outs})
			}
		}
	}
	// A move from one siding to another is not drawn: both are a person's
	// to move a bead out of, and the flow's text says where to.
	for _, s := range p.sidings {
		for _, to := range targets(f, s) {
			if at(to) >= 0 {
				p.spurs = append(p.spurs, spur{siding: s, stage: to})
			}
		}
	}
	return p
}

// outcomesTo names the outcomes that take a bead from one stage to another.
func outcomesTo(f *store.Flow, from, to string) []string {
	var names []string
	for _, o := range f.Outcomes[from] {
		if o.To == to {
			names = append(names, o.Name)
		}
	}
	return names
}

// targets is the stages a stage leads to, each once: a flow file that names
// a stage for two outcomes lists it twice.
func targets(f *store.Flow, stage string) []string {
	var out []string
	for _, to := range f.Transitions[stage] {
		if !slices.Contains(out, to) {
			out = append(out, to)
		}
	}
	return out
}

// Package eta estimates when a bead arrives: its ETA as a usual range from
// the durations the yard keeps per group, label and stage, and its late
// mark when a stage runs past its usual time (design yardr-hrah).
//
// Estimate is pure over its inputs — the bead, its flow, the yard's routes
// and groups, the answered cells, and one response's queue snapshot — so
// the CLI, the web view and the socket share one arithmetic and one pair
// of JSON shapes instead of reimplementing them. The store answers those
// inputs from keyed reads and the listings it already returns sorted or
// indexed; nothing here scans the log.
package eta

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/shared"
)

// SampleFloor is the durations a cell needs before it times a stage: below
// it the stage is unclocked and the estimate says so. The same floor as
// store.ETASampleFloor, which this package cannot import (the store reads
// this package for its JSON shapes); eta_test pins the two equal.
const SampleFloor = 10

// doneOutcome is the flow's default way on. The same word as
// store.DefaultOutcome; eta_test pins the two equal.
const doneOutcome = "done"

// OriginatorAny is the originator a route names to take every
// originator's beads: the shared keyword.
const OriginatorAny = shared.OriginatorAny

// A Cell is one group, label and stage's answered durations: the medians
// and p90s of its entry-to-exit and claim-to-exit rings with the samples
// behind each, and the stay and send-back counts the per-stage stretch is
// computed from. The store answers it from its cells.
type Cell struct {
	EntryMedian time.Duration
	EntryP90    time.Duration
	ClaimMedian time.Duration
	ClaimP90    time.Duration
	EntryN      int
	ClaimN      int
	Stays       int
	SendBacks   int
}

// Cells answers the cell of group, label and stage: zero with none recorded.
type Cells func(group, label, stage string) Cell

// A Bead is the bead estimated: what its row says, read where it is shown.
type Bead struct {
	ID       string
	Stage    string
	Type     string
	Depot    *string
	Labels   []string
	Priority int
	// Originator is the user the bead is for, whose subscription builds
	// it. Empty reads as the operator's, as on the bead.
	Originator string
	Hold       bool
	Closed     bool
	// Group holds the bead, nil when none does; the queue is only
	// simulated for a bead none holds, and the late clock is the waiting
	// one then, the running one otherwise.
	Group *string
	// StayStart is when its current stay in its stage started.
	StayStart time.Time
	// SessionStart is when its live session started, nil when unheld or
	// unknown; the running clock and the current stage's remainder read it.
	SessionStart *time.Time
}

// A Flow is the stages a bead goes through, in the order they were
// written, with the named ways work in a stage can end.
type Flow struct {
	Stages      []string
	Outcomes    map[string][]Outcome
	Transitions map[string][]string
	Terminal    []string
}

// An Outcome is one named way work in a stage can end and where it leads.
type Outcome struct {
	Name string
	To   string
}

// A Route is one route's match and its groups in the order they are tried:
// the first works the stage, the rest are quota fallbacks the estimate
// cannot predict, so it names the first. Routes arrive in match order.
type Route struct {
	Stage        string
	Type         *string
	Depot        *string
	Labels       []string
	BeadPriority *int
	// Originator matches beads for this user alone; OriginatorAny
	// matches every originator's, and nil the operator's alone.
	Originator *string
	Groups     []string
}

// A Group is one pool of agents, one command, people, or another yard.
type Group struct {
	Name   string
	Runner string // herdr, exec, manual or peer
	Limit  int
	// Peer is the yard a peer group sends to, "" otherwise.
	Peer string
}

// A Queue is one response's shared snapshot of the yard's queues: the
// dispatchable listing in its order, the live sessions with their beads'
// labels, and the yard-wide session count against its cap.
type Queue struct {
	Order   []Queued
	Live    []Live
	Running int // live sessions running something on this machine
	// Max is the yard's max_sessions where Capped says it is set; unset,
	// nothing is capped, and 0 pauses the yard: nothing starts.
	Max    int
	Capped bool
}

// A Queued is one dispatchable bead: ready, and owned by no group yet.
type Queued struct {
	Bead     string
	Stage    string
	Type     string
	Depot    *string
	Labels   []string
	Priority int
	// Originator is whose subscription the bead builds on. Empty reads
	// as the operator's, as on the bead.
	Originator string
}

// A Live is one live session, with its bead's labels for its remainder.
type Live struct {
	Bead      string
	Group     string
	Stage     string
	StartedAt time.Time
	Labels    []string
}

// A Result is one bead's estimate: its ETA and its late mark, each nil
// when there is nothing to say.
type Result struct {
	ETA  *ETA  `json:"eta,omitempty"`
	Late *Late `json:"late,omitempty"`
}

// An ETA is the bead's arrival as a usual range: the stretched medians
// summed for the low end, the stretched p90s for the high end, each a
// component-wise heuristic, never a promise. Stages names each clocked
// machine stage's primary group, since quota fallbacks may take the bead
// elsewhere; Samples is the smallest cell count behind the range.
type ETA struct {
	Low       string     `json:"low"`
	High      string     `json:"high"`
	Queue     string     `json:"queue,omitempty"`
	Ahead     int        `json:"ahead,omitempty"`
	Stages    []ETAStage `json:"stages,omitempty"`
	Unclocked []string   `json:"unclocked,omitempty"`
	Samples   int        `json:"samples"`
	// Line is the estimate as bead show prints it. It travels with the
	// shape because the CLI reads the estimate over the socket, where
	// only the exported fields arrive.
	Line string `json:"line"`
}

// An ETAStage is one clocked machine stage's contribution to the range.
type ETAStage struct {
	Stage  string `json:"stage"`
	Group  string `json:"group"`
	Median string `json:"median"`
}

// A Late is the bead's running past its usual time: its stage age against
// the matching clock's p90. Clock is waiting (stage age against whole
// stays) or running (run age against claimed runs).
type Late struct {
	Stage   string `json:"stage"`
	Clock   string `json:"clock"`
	InStage string `json:"in_stage"`
	P90     string `json:"p90"`
	// Line is the mark as bead show prints it; see ETA.Line.
	Line string `json:"line"`
}

// Estimate answers b's ETA and late mark at now: nil when there is
// nothing to say. Operator is the yard's operator, which beads and routes
// without an originator read as. The queue snapshot q is shared by every
// bead of one response, so a page reads the dispatchable listing and the
// live sessions once, not once per bead.
func Estimate(b Bead, flow *Flow, routes []Route, groups []Group, cells Cells, q Queue, now time.Time, operator string) *Result {
	if b.Closed {
		return nil
	}
	byName := make(map[string]Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}
	// primary is the group the routes would send a bead like b in stage
	// to: the first of the matching route's groups, with the depot template
	// resolved. A default whose resolved group is missing serves no depot,
	// as in the store's effective set.
	primary := func(stage, typ string, depot *string, labels []string, priority int, originator string) (Group, bool) {
		eff := shared.EffectiveRoutes(routes, viewRoute, depot, operator)
		var resolved []Route
		for _, r := range eff {
			if !slices.ContainsFunc(r.Groups, shared.IsTemplatedGroup) {
				resolved = append(resolved, r)
				continue
			}
			if depot == nil {
				continue
			}
			serves := true
			for _, g := range r.Groups {
				if g == "wait" {
					continue // wait ends the route; it is not a group to resolve.
				}
				if _, ok := byName[shared.ResolveGroup(g, *depot)]; !ok {
					serves = false
					break
				}
			}
			if !serves {
				continue
			}
			out := r
			out.Groups = make([]string, len(r.Groups))
			for i, g := range r.Groups {
				out.Groups[i] = shared.ResolveGroup(g, *depot)
			}
			resolved = append(resolved, out)
		}
		r := shared.Match(resolved, viewRoute, shared.Bead{Stage: stage, Type: typ, Depot: depot,
			Labels: labels, Priority: priority, Originator: originator}, operator)
		if r == nil || len(r.Groups) == 0 {
			return Group{}, false
		}
		g, ok := byName[r.Groups[0]]
		return g, ok
	}
	primaryOf := func(stage string) (Group, bool) {
		return primary(stage, b.Type, b.Depot, b.Labels, b.Priority, b.Originator)
	}
	label := difficulty(b.Labels)
	legs, stop := walkPath(b, flow, primaryOf)

	var minN int
	var anyCell bool
	consider := func(n int) {
		if !anyCell || n < minN {
			minN, anyCell = n, true
		}
	}

	var parts []string
	var wait time.Duration
	var ahead int
	showQueue, queueClocked := false, false
	if b.Group == nil {
		if g, ok := primaryOf(b.Stage); ok && machineRunner(g.Runner) {
			qw := simulateQueue(b, g, cells, q, now, func(qb Queued) (Group, bool) {
				return primary(qb.Stage, qb.Type, qb.Depot, qb.Labels, qb.Priority, qb.Originator)
			})
			// An uncertain queue — a live remainder stood on a thin
			// cell — still shows its number beside clocked stages,
			// but neither triggers the line alone nor sets samples.
			if qw.clocked {
				for _, n := range qw.ns {
					consider(n)
				}
			}
			if qw.part != "" {
				parts = append(parts, qw.part)
			}
			wait, ahead = qw.wait, qw.ahead
			showQueue = qw.clean && qw.wait > 0
			queueClocked = qw.clocked && qw.wait > 0
		}
	}

	var low, high time.Duration
	var stages []ETAStage
	var unclocked []string
	clocked := false
	lastUnclocked := false
	for i, lg := range legs {
		if lg.manual {
			parts = append(parts, lg.stage+" after your decision")
			unclocked = append(unclocked, lg.stage)
			lastUnclocked = true
			continue
		}
		c := cells(lg.group, label, lg.stage)
		if c.ClaimN < SampleFloor {
			parts = append(parts, lg.stage+": too new to time")
			unclocked = append(unclocked, lg.stage)
			lastUnclocked = true
			continue
		}
		lastUnclocked = false
		med, p90 := c.ClaimMedian, c.ClaimP90
		if i == 0 && b.Group != nil && b.SessionStart != nil {
			// The current stage is already running: what is left of
			// both ends of its range, floored at zero.
			age := now.Sub(*b.SessionStart)
			med = max(med-age, 0)
			p90 = max(p90-age, 0)
		}
		stretch := 1.0
		if c.Stays > 0 {
			stretch = 1 + float64(c.SendBacks)/float64(c.Stays)
		}
		med, p90 = scale(med, stretch), scale(p90, stretch)
		low, high = low+med, high+p90
		parts = append(parts, lg.stage+" ~"+shared.Span(med))
		stages = append(stages, ETAStage{Stage: lg.stage, Group: lg.group, Median: shared.Span(med)})
		consider(c.ClaimN)
		clocked = true
	}
	// A walk that ends past an unclocked last leg says so once: the leg's
	// own part already names it, so "unclocked after" adds nothing.
	if stop != "" && (len(legs) == 0 || stop != "unclocked after "+legs[len(legs)-1].stage || !lastUnclocked) {
		parts = append(parts, stop)
	}

	var eta *ETA
	if clocked || queueClocked {
		low, high = low+wait, high+wait
		line := "eta usual " + shared.Span(low) + "–" + shared.Span(high)
		if shared.Span(low) == shared.Span(high) {
			line = "eta usual " + shared.Span(low)
		}
		eta = &ETA{Low: shared.Span(low), High: shared.Span(high), Stages: stages, Unclocked: unclocked, Samples: minN,
			Line: line + " (" + strings.Join(parts, "; ") + ")"}
		if showQueue {
			eta.Queue, eta.Ahead = shared.Span(wait), ahead
		}
	}
	late := lateMark(b, label, primaryOf, cells, now)
	if eta == nil && late == nil {
		return nil
	}
	return &Result{ETA: eta, Late: late}
}

// A leg is one counted stage of the walk: a machine stage and its primary
// group, or a manual stage, which contributes no time.
type leg struct {
	stage  string
	group  string
	manual bool
}

// walkPath is the stages still ahead of b along its flow's done outcomes:
// from its stage, follow each stage's done, counting every step, up to one
// full loop plus one step past the flow's stage count. stop names where a
// walk that ends anywhere but arrival ends: a fork, a peer stage, an
// unrouted stage, or a stage with no done way out. Without a flow only
// the current stage is counted, and the rest is unclocked past it.
func walkPath(b Bead, flow *Flow, primary func(stage string) (Group, bool)) (legs []leg, stop string) {
	maxSteps := 1
	if flow != nil {
		maxSteps = len(flow.Stages) + 1
	}
	stage := b.Stage
	for {
		if flow != nil && slices.Contains(flow.Terminal, stage) {
			return legs, "" // arrival
		}
		if len(legs) >= maxSteps {
			return legs, "" // one loop and one step past the count
		}
		g, ok := primary(stage)
		if !ok {
			return legs, "then " + stage + ", unrouted"
		}
		switch {
		case g.Runner == "peer":
			return legs, "then " + stage + " on yard " + g.Peer
		case g.Runner == "manual":
			legs = append(legs, leg{stage: stage, manual: true})
		default:
			legs = append(legs, leg{stage: stage, group: g.Name})
		}
		if flow == nil {
			return legs, "unclocked after " + stage
		}
		next, fork, ok := doneNext(flow, stage)
		switch {
		case ok:
			stage = next
		case fork:
			return legs, "then " + orJoin(flow.Transitions[stage])
		default:
			return legs, "unclocked after " + stage
		}
	}
}

// doneNext is where a bead in stage goes on its done outcome: explicit, or
// the single way out where the stage names no outcomes. fork says the
// stage is a fork with no done way on.
func doneNext(flow *Flow, stage string) (next string, fork, ok bool) {
	for _, o := range flow.Outcomes[stage] {
		if o.Name == doneOutcome {
			return o.To, false, true
		}
	}
	if next := flow.Transitions[stage]; len(next) == 1 && len(flow.Outcomes[stage]) == 0 {
		return next[0], false, true
	}
	if len(flow.Transitions[stage]) > 1 {
		return "", true, false
	}
	return "", false, false
}

// orJoin is ss as a fork names them: "review or decide".
func orJoin(ss []string) string {
	if len(ss) <= 2 {
		return strings.Join(ss, " or ")
	}
	return strings.Join(ss[:len(ss)-1], ", ") + " or " + ss[len(ss)-1]
}

// A queueWait is the simulated queue: the wait, the beads ahead, the
// sample counts behind the wait, and the queue's parenthetical part.
type queueWait struct {
	wait  time.Duration
	ahead int
	ns    []int
	part  string
	// clean says the simulation completed: a bead the listing does not
	// hold, the yard's cap, and an unclocked bead ahead of it leave the
	// queue unnumbered instead. clocked says every live remainder stood
	// on a clocked cell; ahead beads are clocked whenever clean.
	clean   bool
	clocked bool
}

// simulateQueue predicts when a slot of g opens for b: the group's slots
// beside the live sessions' remainders, with the beads ahead of b in
// dispatch order placed in order onto the earliest-free slot. The part is
// the numbered wait, what names it unclocked instead, or "" for a zero
// wait, which the line omits.
func simulateQueue(b Bead, g Group, cells Cells, q Queue, now time.Time, primary func(Queued) (Group, bool)) queueWait {
	at := -1
	for i, qb := range q.Order {
		if qb.Bead == b.ID {
			at = i
			break
		}
	}
	if at < 0 {
		// Not dispatchable: it cannot be claimed now, and the routing
		// line says why, so the queue names nothing.
		return queueWait{}
	}
	if g.Limit == 0 {
		return queueWait{part: "group " + g.Name + " paused (limit 0)"}
	}
	// max_sessions counts only runners that start an agent or script on
	// this machine; manual claims and peer work can still start under it.
	if g.Runner != "manual" && g.Runner != "peer" && q.Capped && q.Running >= q.Max {
		if q.Max == 0 {
			return queueWait{part: "yard paused (max_sessions 0)"}
		}
		return queueWait{part: "behind the yard's cap"}
	}
	var out queueWait
	out.clocked = true
	slots := max(g.Limit, 1)
	free := make([]time.Duration, slots)
	for _, s := range q.Live {
		if s.Group != g.Name {
			continue
		}
		c := cells(g.Name, difficulty(s.Labels), s.Stage)
		out.ns = append(out.ns, c.ClaimN)
		if c.ClaimN < SampleFloor {
			out.clocked = false
		}
		// The remaining median, floored at zero; a cell with no
		// samples answers a zero median, freeing its slot now.
		if rem := c.ClaimMedian - now.Sub(s.StartedAt); rem > 0 {
			i := earliest(free)
			free[i] = max(free[i], rem)
		}
	}
	var aheads []Queued
	for _, qb := range q.Order[:at] {
		if pg, ok := primary(qb); ok && pg.Name == g.Name {
			aheads = append(aheads, qb)
		}
	}
	out.ahead = len(aheads)
	for _, qb := range aheads {
		c := cells(g.Name, difficulty(qb.Labels), qb.Stage)
		out.ns = append(out.ns, c.ClaimN)
		if c.ClaimN < SampleFloor {
			out.part = "behind " + beads(out.ahead) + ", too new to time"
			out.clocked = false
			return out
		}
		free[earliest(free)] += c.ClaimMedian
	}
	out.wait = slices.Min(free)
	out.clean = true
	if out.wait <= 0 {
		return out
	}
	if out.ahead == 0 {
		out.part = "queue ~" + shared.Span(out.wait) + " for " + g.Name
		return out
	}
	out.part = "queue ~" + shared.Span(out.wait) + " behind " + beads(out.ahead) + " for " + g.Name
	return out
}

// earliest is the index of the slot that frees first.
func earliest(free []time.Duration) int {
	i := 0
	for j := range free {
		if free[j] < free[i] {
			i = j
		}
	}
	return i
}

// beads is n as the queue counts it: "1 bead", "3 beads".
func beads(n int) string {
	if n == 1 {
		return "1 bead"
	}
	return fmt.Sprintf("%d beads", n)
}

// lateMark is b's late mark at now, nil when it is not late: past the
// matching clock's p90 for its group and label. A held bead, a people's
// stage, a peer stage and an unrouted stage are never late.
func lateMark(b Bead, label string, primary func(stage string) (Group, bool), cells Cells, now time.Time) *Late {
	if b.Hold {
		return nil
	}
	g, ok := primary(b.Stage)
	if !ok || g.Runner == "manual" || g.Runner == "peer" {
		return nil
	}
	if b.Group == nil {
		if b.StayStart.IsZero() {
			return nil
		}
		age := now.Sub(b.StayStart)
		c := cells(g.Name, label, b.Stage)
		if c.EntryN < SampleFloor || age <= c.EntryP90 {
			return nil
		}
		return &Late{Stage: b.Stage, Clock: "waiting", InStage: shared.Span(age), P90: shared.Span(c.EntryP90),
			Line: fmt.Sprintf("late: waiting in %s %s, past the usual %s (p90 entry-to-exit for %s in %s)",
				b.Stage, shared.Span(age), shared.Span(c.EntryP90), label, g.Name)}
	}
	if b.SessionStart == nil {
		return nil
	}
	age := now.Sub(*b.SessionStart)
	c := cells(*b.Group, label, b.Stage)
	if c.ClaimN < SampleFloor || age <= c.ClaimP90 {
		return nil
	}
	return &Late{Stage: b.Stage, Clock: "running", InStage: shared.Span(age), P90: shared.Span(c.ClaimP90),
		Line: fmt.Sprintf("late: running in %s %s, past the usual %s (p90 claim-to-exit for %s in %s)",
			b.Stage, shared.Span(age), shared.Span(c.ClaimP90), label, *b.Group)}
}

// viewRoute is r as matching reads it: the shared shape, adapted at the
// boundary, since neither route type moves below the store.
func viewRoute(r Route) shared.Route {
	return shared.Route{Stage: r.Stage, Type: r.Type, Depot: r.Depot, Labels: r.Labels,
		BeadPriority: r.BeadPriority, Originator: r.Originator}
}

// difficulty is the bead's estimate label for its cells: its difficulty:
// label, else the "-" row. It must equal store.DifficultyLabel, which
// this package cannot import; eta_test pins the two equal.
func difficulty(labels []string) string {
	for _, l := range labels {
		if strings.HasPrefix(l, "difficulty:") {
			return l
		}
	}
	return "-"
}

// machineRunner says whether the runner works machine stages: herdr and
// exec do, manual and peer do not.
func machineRunner(runner string) bool { return runner == "herdr" || runner == "exec" }

// scale stretches d by the cell's send-back share.
func scale(d time.Duration, by float64) time.Duration {
	return time.Duration(float64(d) * by)
}

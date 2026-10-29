// Estimates, arrivals and flows over the store API: a bead's ETA and late
// mark, the arrivals board and the flows view, computed here from the
// figures the store answers (ETACell, BeadStays and the listings) so the
// store stays a leaf and the CLI, the web view and the socket share one
// arithmetic. Nothing here scans the log.
package eta

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// EstimateViaStore answers one bead's ETA and late mark at now: nil when
// there is nothing to say. It is one response's estimate: the queue
// snapshot behind it is read once, and EstimateManyViaStore shares one
// snapshot across many beads.
func EstimateViaStore(ctx context.Context, st store.API, id string, now time.Time) (*Result, error) {
	ests, err := EstimateManyViaStore(ctx, st, []string{id}, now)
	if err != nil {
		return nil, err
	}
	return ests[id], nil
}

// EstimateManyViaStore answers each bead's ETA and late mark at now over
// one shared queue snapshot: the dispatchable listing, the live sessions
// and the cap are read once per response, not once per bead. It answers
// every id it is asked, nil where a bead has nothing to say; an unknown
// id fails the whole response, as GetBead fails its caller.
func EstimateManyViaStore(ctx context.Context, st store.API, ids []string, now time.Time) (map[string]*Result, error) {
	out := make(map[string]*Result, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q, operator, err := estimateQueue(ctx, st)
	if err != nil {
		return nil, err
	}
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	store.SortRoutes(routes)
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	eroutes := make([]Route, len(routes))
	for i, r := range routes {
		var names []string
		for _, g := range r.Groups() {
			names = append(names, g.Name)
		}
		eroutes[i] = Route{Stage: r.Stage, Type: r.Type, Depot: r.Depot,
			Labels: r.Labels(), BeadPriority: r.BeadPriority, Originator: r.Originator, Groups: names}
	}
	egroups := make([]Group, len(groups))
	for i, g := range groups {
		egroups[i] = Group{Name: g.Name, Runner: g.Runner, Limit: g.Limit, Peer: g.AtPeer()}
	}
	// Cells are read once per response, not once per bead that stands
	// behind them: every bead's queue simulation walks the same ahead
	// beads and live sessions.
	memo := map[etaCellKey]Cell{}
	cells := func(group, label, stage string) Cell {
		k := etaCellKey{group, label, stage}
		if c, ok := memo[k]; ok {
			return c
		}
		c, err := st.ETACell(ctx, group, label, stage)
		if err != nil {
			return Cell{}
		}
		out := Cell{EntryMedian: c.EntryMedian, EntryP90: c.EntryP90,
			ClaimMedian: c.ClaimMedian, ClaimP90: c.ClaimP90,
			EntryN: c.EntryN, ClaimN: c.ClaimN, Stays: c.Stays, SendBacks: c.SendBacks}
		memo[k] = out
		return out
	}
	stays, err := st.BeadStays(ctx, ids)
	if err != nil {
		return nil, err
	}
	// A response's beads follow a handful of flows: each is resolved
	// once per depot, type and pin, as stranded resolves them. Each
	// resolution may shell to git for the depot's checkout, which cost
	// a page an invocation per bead.
	flows := map[flowAsk]*Flow{}
	for _, id := range ids {
		b, err := st.GetBead(ctx, id)
		if err != nil {
			return nil, err
		}
		ask := flowAsk{typ: b.Type, pin: store.PinnedFlow(b)}
		if b.Depot != nil {
			ask.depot, ask.hasDepot = *b.Depot, true
		}
		flow, ok := flows[ask]
		if !ok {
			// A flow that does not resolve governs nothing: the
			// estimate counts the current stage, and the rest is
			// unclocked past it.
			if next, _ := st.NextStages(ctx, id); next != nil && next.Flow != nil {
				f := next.Flow
				outs := make(map[string][]Outcome, len(f.Outcomes))
				for stage, os := range f.Outcomes {
					for _, o := range os {
						outs[stage] = append(outs[stage], Outcome{Name: o.Name, To: o.To})
					}
				}
				flow = &Flow{Stages: slices.Clone(f.Stages), Outcomes: outs,
					Transitions: maps.Clone(f.Transitions), Terminal: slices.Clone(f.Terminal)}
			}
			flows[ask] = flow
		}
		out[id] = Estimate(estimateBead(b, stays[id]), flow, eroutes, egroups, cells, q, now, operator)
	}
	return out, nil
}

// etaCellKey is a cell's key within one response's reads.
type etaCellKey struct {
	group, label, stage string
}

// flowAsk is a flow's key within one response's reads: the depot, type and
// pin the estimate resolves it by.
type flowAsk struct {
	depot    string
	hasDepot bool
	typ, pin string
}

// estimateBead is b as the estimate reads it: the bead with its stay's
// clocks, which the served bead does not carry.
func estimateBead(b *store.Bead, stay store.BeadStay) Bead {
	return Bead{ID: b.ID, Stage: b.Stage, Type: b.Type, Depot: b.Depot, Labels: b.Labels,
		Priority: b.Priority, Originator: b.Originator, Hold: b.Hold, Closed: b.Status == "closed", Group: b.Group,
		StayStart: stay.StayStart, SessionStart: stay.SessionStart}
}

// estimateQueue is one response's shared queue snapshot: the dispatchable
// listing in its order, the live sessions with their beads' labels, and
// the yard-wide session count against its cap, as the dispatcher counts
// it (live sessions running something on this machine). It also answers
// the yard's operator, read from the same settings as the cap.
func estimateQueue(ctx context.Context, st store.API) (Queue, string, error) {
	var q Queue
	disp, err := st.Dispatchable(ctx)
	if err != nil {
		return q, "", err
	}
	for _, r := range disp {
		q.Order = append(q.Order, Queued{Bead: r.Bead, Stage: r.Stage, Type: r.Type,
			Depot: r.Depot, Labels: r.Labels, Priority: r.Priority, Originator: r.Originator})
	}
	live, err := st.Sessions(ctx, false)
	if err != nil {
		return q, "", err
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return q, "", err
	}
	byName := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}
	for _, sess := range live {
		var labels []string
		if b, err := st.GetBead(ctx, sess.Bead); err == nil {
			labels = b.Labels
		}
		q.Live = append(q.Live, Live{Bead: sess.Bead, Group: sess.Group,
			Stage: sess.Stage, StartedAt: sess.StartedAt, Labels: labels})
		runner := sess.RunnerName()
		if runner == "" {
			runner = byName[sess.Group].Runner
		}
		if store.RunsHere(runner) {
			q.Running++
		}
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return q, "", err
	}
	q.Max, q.Capped = store.MaxSessions(settings)
	return q, settings[store.OperatorSetting], nil
}

// ArrivalsViaStore answers the board at now: the rows beside the platforms
// in the order the beads travel, so a reader groups the rows by the served
// list without knowing the flows. The rows are the open beads still
// travelling — everything but the backlog — up to ArrivalLimit, in
// arrival order, with the expected arrival as the yard's printed range
// and the remark. It is what footplate draws: the yard computes once so
// the readers draw without computing (design footplate-8v5t, section 3).
// The board holds what viewer may see, "" for the yard itself.
// The order and the remark are the board's, ported in the eta package
// from signalbox src/board.ts.
func ArrivalsViaStore(ctx context.Context, st store.API, viewer string, now time.Time) (*ArrivalBoard, error) {
	beads, err := st.ListBeads(ctx, store.BeadFilter{Viewer: viewer})
	if err != nil {
		return nil, err
	}
	var travelling []store.Bead
	for _, b := range beads {
		if b.Stage == "backlog" {
			continue
		}
		travelling = append(travelling, b)
	}
	if len(travelling) == 0 {
		return &ArrivalBoard{Rows: []ArrivalRow{}, Platforms: []string{}}, nil
	}
	inputs, err := arrivalInputs(ctx, st, travelling, now)
	if err != nil {
		return nil, err
	}
	rows := Arrivals(inputs)
	return &ArrivalBoard{Rows: rows, Platforms: arrivalPlatforms(ctx, st, travelling, rows)}, nil
}

// arrivalInputs answers each bead as the arrivals view reads it: with its
// stay start, running hold, fault, blocker, wait and estimate beside it.
// The board reads the travelling beads; the flows view reads every bead
// it draws, and both rows come from the same inputs.
func arrivalInputs(ctx context.Context, st store.API, beads []store.Bead, now time.Time) ([]ArrivalBead, error) {
	var ids []string
	for _, b := range beads {
		ids = append(ids, b.ID)
	}
	ests, err := EstimateManyViaStore(ctx, st, ids, now)
	if err != nil {
		return nil, err
	}
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	store.SortRoutes(routes)
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	operator := settings[store.OperatorSetting]
	live, err := st.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	running := make(map[string]bool, len(live))
	for _, sess := range live {
		running[sess.ID] = true
	}
	stays, err := st.BeadStays(ctx, ids)
	if err != nil {
		return nil, err
	}
	inputs := make([]ArrivalBead, 0, len(beads))
	for _, b := range beads {
		in := ArrivalBead{ID: b.ID, Title: b.Title, Stage: b.Stage,
			StayStart: stays[b.ID].StayStart, Hold: b.Hold, HeldBy: b.HeldBy}
		if b.Depot != nil {
			in.Depot = *b.Depot
		}
		if b.Session != nil && running[*b.Session] {
			in.Running = true
		}
		if est := ests[b.ID]; est != nil {
			if est.ETA != nil {
				in.Low, in.High = est.ETA.Low, est.ETA.High
			}
			if est.Late != nil {
				in.Late, in.LateBy = true, est.Late.Overrun()
			}
		}
		// A session at work now is the news, not how the one before
		// it ended: the fault is the latest ended session's alone.
		if !in.Running {
			faulted, err := arrivalFaulted(ctx, st, b.ID)
			if err != nil {
				return nil, err
			}
			in.Fault = faulted
		}
		// The first open blocker is the first "blocked by" bead show
		// prints: the oldest holding edge's blocker.
		holding, err := st.HoldingDeps(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		if len(holding) > 0 {
			in.BlockedBy = holding[0].From
		}
		// A claimed bead answers to the group working it, even if a route
		// changed since the claim. Unclaimed beads answer to the route, as
		// the waits view does.
		group := ""
		if b.Group != nil {
			group = *b.Group
		} else if r := store.MatchRoute(routes, b, operator, byName); r != nil {
			group = r.Worker()
		}
		if g, ok := byName[group]; ok && g.Runner == "manual" {
			if len(g.Members) > 0 {
				in.WaitsFor = strings.Join(g.Members, ", ")
			} else {
				in.WaitsFor = g.Name
			}
		}
		inputs = append(inputs, in)
	}
	return inputs, nil
}

// arrivalFaulted reports whether the bead's latest session came to no
// good end: died, failed or aborted. A bead whose latest session went
// well — at work, or ended as it should — has none, whatever came
// before. The same ends as signalbox src/project.ts faults.
func arrivalFaulted(ctx context.Context, st store.API, id string) (bool, error) {
	sessions, err := st.ListSessions(ctx, store.SessionFilter{All: true, Bead: id, Limit: 1})
	if err != nil {
		return false, err
	}
	if len(sessions) == 0 {
		return false, nil
	}
	switch sessions[0].State {
	case "died", "failed", "aborted":
		return true, nil
	}
	return false, nil
}

// arrivalPlatforms orders the platforms the served rows stand on in the
// order the beads travel: each depot's flow stage order, as the web's
// flows page draws it, with the depot holding the most travelling beads
// first. A stage no flow knows — a row's platform from a flow since
// changed — stands last, in row order.
func arrivalPlatforms(ctx context.Context, st store.API, travelling []store.Bead, rows []ArrivalRow) []string {
	byDepot := map[string][]store.Bead{}
	for _, b := range travelling {
		if b.Depot != nil {
			byDepot[*b.Depot] = append(byDepot[*b.Depot], b)
		}
	}
	var depots []stageOrder
	for name, beads := range byDepot {
		if stages := arrivalDepotStages(ctx, st, name, beads); len(stages) > 0 {
			depots = append(depots, stageOrder{name: name, beads: len(beads), stages: stages})
		}
	}
	order := mergeStageOrders(depots)
	known := map[string]bool{}
	for _, st := range order {
		known[st] = true
	}
	onRow := map[string]bool{}
	var inRowOrder []string
	for _, r := range rows {
		if !onRow[r.Platform] {
			onRow[r.Platform] = true
			inRowOrder = append(inRowOrder, r.Platform)
		}
	}
	platforms := []string{}
	for _, st := range order {
		if onRow[st] {
			platforms = append(platforms, st)
		}
	}
	for _, st := range inRowOrder {
		if !known[st] {
			platforms = append(platforms, st)
		}
	}
	return platforms
}

// arrivalDepotStages is the order depot's travelling beads travel: its
// flows' stage orders, the flow governing the most of them first. A bead
// pinning its own flow travels that one, not the depot's, and governs
// none, as on the web's flows page, where it stands off the line. A depot
// whose flows cannot be read, or which none of its beads travels, has no
// order: its rows' platforms stand last unless another depot's flow knows
// them.
func arrivalDepotStages(ctx context.Context, st store.API, depot string, beads []store.Bead) []string {
	flows, err := st.DepotFlows(ctx, depot)
	if err != nil {
		return nil
	}
	typed := map[string]bool{}
	own := false
	for _, f := range flows {
		if f.Type == "" {
			own = true
		} else {
			typed[f.Type] = true
		}
	}
	governed := map[string]int{}
	for i := range beads {
		if store.PinnedFlow(&beads[i]) != "" {
			continue
		}
		switch {
		case typed[beads[i].Type]:
			governed[beads[i].Type]++
		case own:
			governed[""]++
		}
	}
	var orders []stageOrder
	for _, f := range flows {
		if n := governed[f.Type]; n > 0 {
			orders = append(orders, stageOrder{name: f.Type, beads: n, stages: f.Flow.Stages})
		}
	}
	return mergeStageOrders(orders)
}

// stageOrder is a flow's stages with the travelling beads behind them: a
// depot's, or one of a depot's flows'.
type stageOrder struct {
	name   string // the depot, or the depot flow's type
	beads  int
	stages []string
}

// mergeStageOrders folds stage orders into one travel order, most
// travelling beads first; ties break by name, so the board stands still
// while counts agree.
func mergeStageOrders(orders []stageOrder) []string {
	slices.SortFunc(orders, func(a, b stageOrder) int {
		if a.beads != b.beads {
			return cmp.Compare(b.beads, a.beads)
		}
		return strings.Compare(a.name, b.name)
	})
	var out []string
	for _, o := range orders {
		out = mergeStageOrder(out, o.stages)
	}
	return out
}

// mergeStageOrder folds the stage order next into base: the stages both
// know keep base's order, and next's extra stages stand where next puts
// them — after the last of its own stages before each that the order
// already holds, or before the first it holds after it when none comes
// before, or at the end when they share none. An extra already folded
// holds too, so next's own order stands between its extras.
func mergeStageOrder(base, next []string) []string {
	out := slices.Clone(base)
	holds := map[string]bool{}
	for _, st := range out {
		holds[st] = true
	}
	for i, st := range next {
		if holds[st] {
			continue
		}
		holds[st] = true
		at, anchored := len(out), false
		for _, before := range next[:i] {
			if holds[before] {
				at, anchored = slices.Index(out, before)+1, true
			}
		}
		if !anchored {
			for _, after := range next[i+1:] {
				if j := slices.Index(out, after); j >= 0 {
					at = j
					break
				}
			}
		}
		out = slices.Insert(out, at, st)
	}
	return out
}

// FlowsViaStore answers the flows view at now: the depots each with its
// flows, each flow its stages in travel order, each stage its beads, each
// flow its landings of the trailing 24 hours, the per-depot Elsewhere rows,
// the unassigned rows, and the travelling totals. Its stages, beads and
// lamps are /flows and the panel as served; its unassigned rows are the open
// beads outside the backlog with no depot. An omitted viewer is the yard
// itself; a viewer's board holds only the beads they may see, landings
// among them.
func FlowsViaStore(ctx context.Context, st store.API, viewer string, now time.Time) (*store.FlowsBoard, error) {
	depots, err := st.Depots(ctx)
	if err != nil {
		return nil, err
	}
	beads, err := st.ListBeads(ctx, store.BeadFilter{Viewer: viewer})
	if err != nil {
		return nil, err
	}
	inputs, err := arrivalInputs(ctx, st, beads, now)
	if err != nil {
		return nil, err
	}
	rows := make(map[string]ArrivalRow, len(inputs))
	for _, in := range inputs {
		rows[in.ID] = ArrivalRowFor(in)
	}
	live, err := st.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	// A manual group's claim is a person's turn, not work in progress,
	// as on Home and the line.
	manual := map[string]bool{}
	bySession := make(map[string]store.Session, len(live))
	for _, sess := range live {
		if sess.Manual() {
			manual[sess.ID] = true
		}
		bySession[sess.ID] = sess
	}
	// When each bead entered its stage, the stay's clock, which the
	// served bead does not carry.
	var ids []string
	for _, b := range beads {
		ids = append(ids, b.ID)
	}
	stays, err := st.BeadStays(ctx, ids)
	if err != nil {
		return nil, err
	}
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	store.SortRoutes(routes)
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	byGroup := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		byGroup[g.Name] = g
	}
	// A taken seat is one live session of the group, wherever its bead
	// stands, counted once: the same count the claim refuses against.
	taken := map[string]int{}
	for _, sess := range live {
		taken[sess.Group]++
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	operator := settings[store.OperatorSetting]
	lamps, err := flowLamps(ctx, st, beads, live, routes, groups, operator)
	if err != nil {
		return nil, err
	}
	landed, err := flowLandings(ctx, st, viewer, now)
	if err != nil {
		return nil, err
	}
	row := func(b store.Bead) store.FlowBead {
		r := rows[b.ID]
		fb := store.FlowBead{Bead: b.ID, Title: b.Title, Expected: r.Expected,
			Remark: r.Remark, Tone: r.Tone, HeldBy: r.HeldBy, Working: b.Session != nil && !manual[*b.Session]}
		// Who works the bead and when it entered its stage, where a live
		// session claimed it. A manual group's claim names no worker, but
		// the stay still has its clock. The group, kind and model are the
		// session's own, as the claim recorded them, never the group's
		// current config.
		if b.Session != nil {
			if sess, ok := bySession[*b.Session]; ok {
				if !sess.Manual() {
					if worker := sess.RunnerShown(); worker != "" {
						fb.Worker = worker
						fb.Group, fb.Kind, fb.Model = sess.Group, sess.Kind, sess.Model
					}
				}
				if entry := stays[b.ID].StayStart; !entry.IsZero() {
					fb.EnteredStageAt = &entry
				}
			}
		}
		return fb
	}
	out := &store.FlowsBoard{Depots: []store.FlowsDepot{}, Unassigned: []store.FlowBead{}}
	var unassigned []store.Bead
	byDepot := map[string][]store.Bead{}
	for _, b := range beads {
		if b.Stage != "backlog" {
			out.Travelling++
		}
		if b.Depot == nil {
			if b.Stage != "backlog" {
				unassigned = append(unassigned, b)
			}
			continue
		}
		byDepot[*b.Depot] = append(byDepot[*b.Depot], b)
	}
	newestFirstBeads(unassigned)
	for _, b := range unassigned {
		out.Unassigned = append(out.Unassigned, row(b))
	}
	for _, depot := range depots {
		fd := store.FlowsDepot{Name: depot.Name, Kind: depot.Kind, Flows: []store.FlowsFlow{}, Elsewhere: []store.FlowBead{}}
		// As on /flows: a depot whose flows cannot be read still shows
		// its beads, and the other depots their flows.
		rfs, err := st.DepotFlows(ctx, depot.Name)
		if err != nil {
			fd.Error = err.Error()
		}
		for _, rf := range rfs {
			ff := store.FlowsFlow{Name: rf.Flow.Name, Type: rf.Type, Source: rf.Source.String(), Stages: []store.FlowsStage{}, Landings: []store.FlowLanding{}}
			for _, st := range rf.Flow.Stages {
				fs := store.FlowsStage{Stage: st, Lamp: "none", Terminal: rf.Flow.IsTerminal(st), Beads: []store.FlowBead{}}
				// Routes may match on type; the depot's own flow is shown
				// for the default type, as `yardr flow show` does. The
				// probe names no originator, so it reads as the operator's.
				probe := store.Bead{Stage: st, Type: cmp.Or(rf.Type, "task"), Depot: &depot.Name, Priority: store.DefaultPriority}
				if r := store.MatchRoute(routes, probe, operator, byGroup); r != nil {
					fs.Group = r.Worker()
					if g, ok := byGroup[fs.Group]; ok {
						if g.Runner == "manual" {
							fs.Human = true
						}
						limit, n := g.Limit, taken[fs.Group]
						fs.Limit, fs.Taken = &limit, &n
					}
				}
				ff.Stages = append(ff.Stages, fs)
			}
			fd.Flows = append(fd.Flows, ff)
		}
		// The depot's landings under their flows, newest first, capped
		// per flow. A landing in no section of the flows as they stand
		// now (a flow changed since it landed) has no flow to stand
		// under.
		byFlow := map[int][]store.FlowLanding{}
		for _, l := range landed[depot.Name] {
			if f, _ := flowSection(&l.bead, rfs); f >= 0 {
				byFlow[f] = append(byFlow[f], store.FlowLanding{Bead: l.bead.ID, Title: l.bead.Title, LandedAt: l.at})
			}
		}
		for f, rows := range byFlow {
			slices.SortStableFunc(rows, func(a, b store.FlowLanding) int {
				if c := b.LandedAt.Compare(a.LandedAt); c != 0 {
					return c
				}
				return cmp.Compare(a.Bead, b.Bead)
			})
			fd.Flows[f].Landings = append(fd.Flows[f].Landings, rows[:min(len(rows), flowLandingsPerFlow)]...)
		}
		var elsewhere []store.Bead
		onStage := map[[2]int][]store.Bead{}
		for _, b := range byDepot[depot.Name] {
			if b.Stage != "backlog" {
				fd.Travelling++
			}
			if f, st := flowSection(&b, rfs); f >= 0 {
				k := [2]int{f, st}
				onStage[k] = append(onStage[k], b)
			} else {
				elsewhere = append(elsewhere, b)
			}
		}
		for f := range fd.Flows {
			for st := range fd.Flows[f].Stages {
				bs := onStage[[2]int{f, st}]
				newestFirstBeads(bs)
				// The stage shows its best state: no lamp while a bead
				// is worked with no fault of its own, else its worst
				// wait. A session waiting on a prompt keeps its claim,
				// so a blocked bead is worked but not moving: it lights
				// the fault below.
				moving, fault, turn := false, false, false
				for _, b := range bs {
					fb := row(b)
					fd.Flows[f].Stages[st].Beads = append(fd.Flows[f].Stages[st].Beads, fb)
					if lamps[b.ID] == "fault" {
						fault = true
						continue
					}
					if lamps[b.ID] == "turn" {
						turn = true
					}
					if fb.Working {
						moving = true
					}
				}
				switch {
				case moving:
					// No lamp: the readers draw a worked stage as moving.
				case fault:
					fd.Flows[f].Stages[st].Lamp = "fault"
				case turn:
					fd.Flows[f].Stages[st].Lamp = "turn"
				}
			}
		}
		newestFirstBeads(elsewhere)
		for _, b := range elsewhere {
			fd.Elsewhere = append(fd.Elsewhere, row(b))
		}
		out.Depots = append(out.Depots, fd)
	}
	return out, nil
}

// landedBead is a bead that entered merged with when it did.
type landedBead struct {
	bead store.Bead
	at   time.Time
}

// flowLandingsWindow is how many of the most recent advanced events Flows
// reads to find the trailing 24 hours' landings: about two days at
// current rates, so the window always holds the day it keeps. It reads
// through the index by kind, so what it costs is what it returns, and
// keeps the day in memory; the bead side has no closed-at index, so it
// would scan every bead ever instead.
const flowLandingsWindow = 3000

// flowLandingsPerFlow caps the landings served under one flow: past that
// it is a log, not a glance.
const flowLandingsPerFlow = 30

// flowLandingsAfter is how far back a landing counts: the trailing 24
// hours, not the calendar day, so no timezone semantics.
const flowLandingsAfter = 24 * time.Hour

// flowLandings is what landed under each depot's flows in the trailing
// 24 hours, by depot: the beads whose advanced event entered merged since
// then, with when. What the viewer may not see is left out, as if it
// never landed, as the board leaves it out; a bead with no depot has no
// flow to land under.
func flowLandings(ctx context.Context, st store.API, viewer string, now time.Time) (map[string][]landedBead, error) {
	evs, err := st.EventsOfKind(ctx, "advanced", flowLandingsWindow)
	if err != nil {
		return nil, err
	}
	since := now.Add(-flowLandingsAfter)
	type pending struct {
		handle string
		at     time.Time
	}
	var pend []pending
	for _, e := range evs {
		if e.Bead == nil {
			continue
		}
		if to, _ := e.Data["to"].(string); to != "merged" {
			continue
		}
		if e.At.Before(since) {
			continue
		}
		pend = append(pend, pending{*e.Bead, e.At})
	}
	out := map[string][]landedBead{}
	for _, p := range pend {
		b, err := st.GetBead(ctx, p.handle)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		if b.Depot == nil {
			continue
		}
		if st.May(ctx, viewer, store.ActSee, store.Object{Kind: store.KindBead, Name: b.ID}) != nil {
			continue
		}
		out[*b.Depot] = append(out[*b.Depot], landedBead{bead: *b, at: p.at})
	}
	return out, nil
}

// flowSection is where the depot's bead stands: the indexes of its flow
// and stage, or -1 for its Elsewhere. As the web's placer: a bead that
// pins its flow follows that one, not the depot's, and stands off the
// line, as does one in no stage of the flow governing its type (or the
// depot's own, untyped).
func flowSection(b *store.Bead, rfs []store.DepotFlow) (flow, stage int) {
	if store.PinnedFlow(b) == "" {
		own, typed := -1, -1
		for i, rf := range rfs {
			switch rf.Type {
			case "":
				own = i
			case b.Type:
				typed = i
			}
		}
		if typed < 0 {
			typed = own
		}
		if typed >= 0 {
			if st := slices.Index(rfs[typed].Flow.Stages, b.Stage); st >= 0 {
				return typed, st
			}
		}
	}
	return -1, -1
}

// newestFirstBeads orders beads by their last change, the newest first,
// as the web's flows page orders its cards.
func newestFirstBeads(bs []store.Bead) {
	slices.SortStableFunc(bs, func(a, b store.Bead) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// flowLamps answers each bead's lamp: fault where a session waits on a
// prompt or the bead lacks what its route needs; turn where the bead is
// held or only a person's turn waits, a manual group's stage or an open
// ask to the operator; none where nothing waits. A bead blocked only
// behind another bead lights none: ordinary waiting.
// The panel's fault/turn rule, without the words: a wait that is only an
// ask, with a manual reason if any, is the person's turn, and a bead
// whose reasons are all manual is parked, so both light turn.
func flowLamps(ctx context.Context, st store.API, beads []store.Bead, live []store.Session, routes []store.Route, groups []store.Group, operator string) (map[string]string, error) {
	manualGroup := map[string]bool{}
	byName := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
		if g.Runner == "manual" {
			manualGroup[g.Name] = true
		}
	}
	stranded, err := st.Stranded(ctx, "")
	if err != nil {
		return nil, err
	}
	lacking := map[string]bool{}
	for _, sb := range stranded {
		lacking[sb.Bead] = true
	}
	// The sessions that can wait on a prompt, by bead. A manual group's
	// claim is none of them: nothing runs in it, so nothing asks.
	agents := map[string][]store.Session{}
	for _, sess := range live {
		if !sess.Manual() {
			agents[sess.Bead] = append(agents[sess.Bead], sess)
		}
	}
	// The open asks to the operator, on the yard's open beads: a question
	// from another yard stands in no row, as on the panel. A yard that
	// names no operator, or no user by that name, waits without asks.
	asked := map[string]bool{}
	if operator != "" {
		q, err := st.UserAsks(ctx, operator, operator, operator)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
		} else {
			open := map[string]bool{}
			for _, b := range beads {
				open[b.ID] = true
			}
			for _, o := range q.Open {
				if open[o.Bead] && !strings.Contains(o.Bead, "@") {
					asked[o.Bead] = true
				}
			}
		}
	}
	out := make(map[string]string, len(beads))
	for _, b := range beads {
		fault := lacking[b.ID]
		for _, sess := range agents[b.ID] {
			if blocked, err := flowBlocked(ctx, st, sess); err != nil {
				return nil, err
			} else if blocked {
				fault = true
				break
			}
		}
		if fault {
			out[b.ID] = "fault"
			continue
		}
		// The group working the bead, else the one its stage is routed
		// to: a manual group at its limit leaves beads waiting unclaimed.
		group := ""
		if b.Group != nil {
			group = *b.Group
		} else if r := store.MatchRoute(routes, b, operator, byName); r != nil {
			group = r.Worker()
		}
		if b.Hold || manualGroup[group] || asked[b.ID] {
			out[b.ID] = "turn"
			continue
		}
		out[b.ID] = "none"
	}
	return out, nil
}

// flowBlocked reports whether a session waits on a permission prompt or a
// question: the dispatcher's last word on it is session_blocked. The yard
// keeps that nowhere but in the bead's events, which are read at the web
// reader's depth, so the two cannot disagree.
func flowBlocked(ctx context.Context, st store.API, sess store.Session) (bool, error) {
	evs, err := st.Events(ctx, sess.Bead, 200)
	if err != nil {
		return false, err
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if k := evs[i].Kind; (k == "session_blocked" || k == "session_unblocked") && evs[i].Data["session"] == sess.ID {
			return k == "session_blocked", nil
		}
	}
	return false, nil
}

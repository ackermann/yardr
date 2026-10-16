package store

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/eta"
)

// Arrivals answers the board: the rows beside the platforms in the order
// the beads travel, so a reader groups the rows by the served list
// without knowing the flows. The rows are the open beads still
// travelling — everything but the backlog — up to eta.ArrivalLimit, in
// arrival order, with the expected arrival as the yard's printed range
// and the remark. It is what footplate draws: the yard computes once so
// the readers draw without computing (design footplate-8v5t, section 3).
// The order and the remark are the board's, ported in the eta package
// from signalbox src/board.ts.
func (s *Store) Arrivals(ctx context.Context) (*eta.ArrivalBoard, error) {
	beads, err := s.ListBeads(ctx, BeadFilter{})
	if err != nil {
		return nil, err
	}
	var travelling []Bead
	for _, b := range beads {
		if b.Stage == "backlog" {
			continue
		}
		travelling = append(travelling, b)
	}
	if len(travelling) == 0 {
		return &eta.ArrivalBoard{Rows: []eta.ArrivalRow{}, Platforms: []string{}}, nil
	}
	var ids []string
	for _, b := range travelling {
		ids = append(ids, b.ID)
	}
	ests, err := s.EstimateMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	routes, err := s.Routes(ctx)
	if err != nil {
		return nil, err
	}
	SortRoutes(routes)
	groups, err := s.Groups(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	operator := settings[OperatorSetting]
	live, err := s.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	running := make(map[string]bool, len(live))
	for _, sess := range live {
		running[sess.ID] = true
	}
	stays, err := s.arrivalStays(ctx, ids)
	if err != nil {
		return nil, err
	}
	inputs := make([]eta.ArrivalBead, 0, len(travelling))
	for _, b := range travelling {
		in := eta.ArrivalBead{ID: b.ID, Title: b.Title, Stage: b.Stage,
			StayStart: stays[b.ID], Hold: b.Hold}
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
			in.Late = est.Late != nil
		}
		// A session at work now is the news, not how the one before
		// it ended: the fault is the latest ended session's alone.
		if !in.Running {
			faulted, err := s.arrivalFaulted(ctx, b.ID)
			if err != nil {
				return nil, err
			}
			in.Fault = faulted
		}
		// A claimed bead answers to the group working it, even if a route
		// changed since the claim. Unclaimed beads answer to the route, as
		// the waits view does.
		group := ""
		if b.Group != nil {
			group = *b.Group
		} else if r := MatchRoute(routes, b, operator); r != nil {
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
	rows := eta.Arrivals(inputs)
	return &eta.ArrivalBoard{Rows: rows, Platforms: s.arrivalPlatforms(ctx, travelling, rows)}, nil
}

// arrivalPlatforms orders the platforms the served rows stand on in the
// order the beads travel: each depot's flow stage order, as the web's
// flows page draws it, with the depot holding the most travelling beads
// first. A stage no flow knows — a row's platform from a flow since
// changed — stands last, in row order.
func (s *Store) arrivalPlatforms(ctx context.Context, travelling []Bead, rows []eta.ArrivalRow) []string {
	byDepot := map[string][]Bead{}
	for _, b := range travelling {
		if b.Depot != nil {
			byDepot[*b.Depot] = append(byDepot[*b.Depot], b)
		}
	}
	var depots []stageOrder
	for name, beads := range byDepot {
		if stages := s.arrivalDepotStages(ctx, name, beads); len(stages) > 0 {
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
func (s *Store) arrivalDepotStages(ctx context.Context, depot string, beads []Bead) []string {
	flows, err := s.DepotFlows(ctx, depot)
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
		if PinnedFlow(&beads[i]) != "" {
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

// arrivalStays answers each bead's stay start: when its current stay in
// its stage started.
func (s *Store) arrivalStays(ctx context.Context, ids []string) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(ids))
	err := s.read(func(tx *bolt.Tx) error {
		for _, id := range ids {
			row, err := loadBead(tx, id)
			if err != nil {
				return err
			}
			out[id] = row.StayStart
		}
		return nil
	})
	return out, err
}

// arrivalFaulted reports whether the bead's latest session came to no
// good end: died, failed or aborted. A bead whose latest session went
// well — at work, or ended as it should — has none, whatever came
// before. The same ends as signalbox src/project.ts faults.
func (s *Store) arrivalFaulted(ctx context.Context, id string) (bool, error) {
	sessions, err := s.ListSessions(ctx, SessionFilter{All: true, Bead: id, Limit: 1})
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

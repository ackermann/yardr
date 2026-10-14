package store

import (
	"context"
	"maps"
	"slices"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/eta"
)

// Estimate answers one bead's ETA and late mark: nil when there is nothing
// to say. It is one response's estimate: the queue snapshot behind it is
// read once, and EstimateMany shares one snapshot across many beads.
func (s *Store) Estimate(ctx context.Context, id string) (*eta.Result, error) {
	ests, err := s.EstimateMany(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	return ests[id], nil
}

// EstimateMany answers each bead's ETA and late mark over one shared queue
// snapshot: the dispatchable listing, the live sessions and the cap are
// read once per response, not once per bead. It answers every id it is
// asked, nil where a bead has nothing to say; an unknown id fails the
// whole response, as GetBead fails its caller.
func (s *Store) EstimateMany(ctx context.Context, ids []string) (map[string]*eta.Result, error) {
	out := make(map[string]*eta.Result, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q, err := s.etaQueue(ctx)
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
	eroutes := make([]eta.Route, len(routes))
	for i, r := range routes {
		var names []string
		for _, g := range r.Groups() {
			names = append(names, g.Name)
		}
		eroutes[i] = eta.Route{Stage: r.Stage, Type: r.Type, Depot: r.Depot,
			Labels: r.Labels(), BeadPriority: r.BeadPriority, Groups: names}
	}
	egroups := make([]eta.Group, len(groups))
	for i, g := range groups {
		egroups[i] = eta.Group{Name: g.Name, Runner: g.Runner, Limit: g.Limit, Peer: g.AtPeer()}
	}
	// Cells are read once per response, not once per bead that stands
	// behind them: every bead's queue simulation walks the same ahead
	// beads and live sessions.
	memo := map[etaCellKey]eta.Cell{}
	cells := func(group, label, stage string) eta.Cell {
		k := etaCellKey{group, label, stage}
		if c, ok := memo[k]; ok {
			return c
		}
		c, err := s.ETACell(ctx, group, label, stage)
		if err != nil {
			return eta.Cell{}
		}
		out := eta.Cell{EntryMedian: c.EntryMedian, EntryP90: c.EntryP90,
			ClaimMedian: c.ClaimMedian, ClaimP90: c.ClaimP90,
			EntryN: c.EntryN, ClaimN: c.ClaimN, Stays: c.Stays, SendBacks: c.SendBacks}
		memo[k] = out
		return out
	}
	now := now()
	for _, id := range ids {
		b, err := s.GetBead(ctx, id)
		if err != nil {
			return nil, err
		}
		// A flow that does not resolve governs nothing: the estimate
		// counts the current stage, and the rest is unclocked past it.
		var flow *eta.Flow
		if next, _ := s.NextStages(ctx, id); next != nil && next.Flow != nil {
			f := next.Flow
			outs := make(map[string][]eta.Outcome, len(f.Outcomes))
			for stage, os := range f.Outcomes {
				for _, o := range os {
					outs[stage] = append(outs[stage], eta.Outcome{Name: o.Name, To: o.To})
				}
			}
			flow = &eta.Flow{Stages: slices.Clone(f.Stages), Outcomes: outs,
				Transitions: maps.Clone(f.Transitions), Terminal: slices.Clone(f.Terminal)}
		}
		eb, err := s.etaBead(ctx, b)
		if err != nil {
			return nil, err
		}
		out[id] = eta.Estimate(eb, flow, eroutes, egroups, cells, q, now)
	}
	return out, nil
}

// etaCellKey is a cell's key within one response's reads.
type etaCellKey struct {
	group, label, stage string
}

// etaBead is b as the estimate reads it: the bead with its stay's clocks,
// which the served bead does not carry.
func (s *Store) etaBead(ctx context.Context, b *Bead) (eta.Bead, error) {
	eb := eta.Bead{ID: b.ID, Stage: b.Stage, Type: b.Type, Depot: b.Depot, Labels: b.Labels,
		Priority: b.Priority, Hold: b.Hold, Closed: b.Status == "closed", Group: b.Group}
	if eb.Closed {
		return eb, nil
	}
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, b.ID)
		if err != nil {
			return err
		}
		eb.StayStart = row.StayStart
		if row.StayClaimAt != nil {
			eb.SessionStart = row.StayClaimAt
		}
		return nil
	})
	if err != nil {
		return eta.Bead{}, err
	}
	// The running clock is the live session's start; the claim's stamp
	// stands in where no live session answers.
	if b.Session != nil {
		if sess, err := s.GetSession(ctx, *b.Session); err == nil && sess.live() {
			eb.SessionStart = &sess.StartedAt
		}
	}
	return eb, nil
}

// etaQueue is one response's shared queue snapshot: the dispatchable
// listing in its order, the live sessions with their beads' labels, and
// the yard-wide session count against its cap, as the dispatcher counts
// it (live sessions running something on this machine).
func (s *Store) etaQueue(ctx context.Context) (eta.Queue, error) {
	var q eta.Queue
	disp, err := s.Dispatchable(ctx)
	if err != nil {
		return q, err
	}
	for _, r := range disp {
		q.Order = append(q.Order, eta.Queued{Bead: r.Bead, Stage: r.Stage, Type: r.Type,
			Depot: r.Depot, Labels: r.Labels, Priority: r.Priority})
	}
	live, err := s.Sessions(ctx, false)
	if err != nil {
		return q, err
	}
	groups, err := s.Groups(ctx)
	if err != nil {
		return q, err
	}
	byName := make(map[string]Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}
	for _, sess := range live {
		var labels []string
		if b, err := s.GetBead(ctx, sess.Bead); err == nil {
			labels = b.Labels
		}
		q.Live = append(q.Live, eta.Live{Bead: sess.Bead, Group: sess.Group,
			Stage: sess.Stage, StartedAt: sess.StartedAt, Labels: labels})
		runner := sess.RunnerName()
		if runner == "" {
			runner = byName[sess.Group].Runner
		}
		if RunsHere(runner) {
			q.Running++
		}
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return q, err
	}
	q.Max = MaxSessions(settings)
	return q, nil
}

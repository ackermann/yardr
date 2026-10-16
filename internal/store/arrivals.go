package store

import (
	"context"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/eta"
)

// Arrivals answers the board's rows: the open beads still travelling —
// everything but the backlog — up to eta.ArrivalLimit, in arrival
// order, with the expected arrival as the yard's printed range and the
// remark. It is what footplate draws: the yard computes the rows once
// so the readers draw without computing (design footplate-8v5t,
// section 3). The order and the remark are the board's, ported in the
// eta package from signalbox src/board.ts.
func (s *Store) Arrivals(ctx context.Context) ([]eta.ArrivalRow, error) {
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
		return []eta.ArrivalRow{}, nil
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
		if r := MatchRoute(routes, b, operator); r != nil {
			if g, ok := byName[r.Worker()]; ok && g.Runner == "manual" {
				if len(g.Members) > 0 {
					in.WaitsFor = strings.Join(g.Members, ", ")
				} else {
					in.WaitsFor = g.Name
				}
			}
		}
		inputs = append(inputs, in)
	}
	return eta.Arrivals(inputs), nil
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

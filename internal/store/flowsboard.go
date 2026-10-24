package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/eta"
)

// FlowsBoard is what the yard serves for the flows view: its depots as
// lines, each with its flows, each flow its stages in travel order with
// the open beads on each and its lamp, and the beads travelling with no
// line beside them. It is what footplate's timetable draws: the yard
// computes the view once so the readers draw without computing (design
// footplate-zj5f, section 3.4). The depots, flows, stages and beads are
// the web's /flows page as served; the lamps are its panel's; each bead's
// expected arrival, remark and tone are the Arrivals row's, reused, not
// recomputed.
type FlowsBoard struct {
	// Travelling counts the yard's open beads outside the backlog: the
	// depots' totals plus the unassigned count. Complete, not capped.
	Travelling int          `json:"travelling"`
	Depots     []FlowsDepot `json:"depots"`
	// Unassigned are the open beads outside the backlog with no depot,
	// the newest change first, each row shaped as a flow bead.
	Unassigned []FlowBead `json:"unassigned"`
}

// FlowsDepot is one depot as the flows view shows it: its flows, and the
// open beads in no stage of them.
type FlowsDepot struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Travelling counts the depot's open beads outside the backlog.
	Travelling int         `json:"travelling"`
	Flows      []FlowsFlow `json:"flows"`
	// Elsewhere are the depot's open beads in no stage of the flows
	// above: a bead that pins a flow of its own, one a changed flow left
	// behind, and every bead of a depot with no flow. The newest change
	// first, as on /flows.
	Elsewhere []FlowBead `json:"elsewhere"`
	// Error is why the depot's flows could not be read; its beads are
	// then all in Elsewhere, as on /flows.
	Error string `json:"error,omitempty"`
}

// FlowsFlow is one flow of a depot: the depot's own (Type "") or a bead
// type's.
type FlowsFlow struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Source is where the flow comes from, as `yardr flow show` says it:
	// "store", "repo@0f415ee".
	Source string       `json:"source"`
	Stages []FlowsStage `json:"stages"`
}

// FlowsStage is one stage of a flow and the open beads in it.
type FlowsStage struct {
	Stage string `json:"stage"`
	// Group is the group a bead of the flow's type would be routed to in
	// this stage, "" when no route matches. Human says it is a manual
	// group.
	Group string `json:"group"`
	Human bool   `json:"human"`
	// Lamp is the stage's wait light: fault where a held, blocked or
	// stranded bead stands on it, turn where only a person's turn does,
	// none otherwise. The panel's two lamps, served.
	Lamp string `json:"lamp"`
	// Terminal is a stage that closes the bead entering it, as the web's
	// stage says: a reader draws the buffer stop from this, never from the
	// last place.
	Terminal bool `json:"terminal"`
	// Limit is the routed group's seat limit, 0 pausing the group; Taken
	// is how many of the group's seats live sessions hold across the whole
	// yard, the same number on every stage routed to the group. Both are
	// absent where no route matches, so a reader can tell an unrouted
	// stage from a paused one.
	Limit *int       `json:"limit,omitempty"`
	Taken *int       `json:"taken,omitempty"`
	Beads []FlowBead `json:"beads"`
}

// FlowBead is one bead of the flows view: the bead with its Arrivals row.
type FlowBead struct {
	Bead     string `json:"bead"`
	Title    string `json:"title"`
	Expected string `json:"expected"`
	Remark   string `json:"remark"`
	Tone     string `json:"tone"`
	// HeldBy is who holds a held bead, as the Arrivals row says it;
	// absent where the bead is not held and where the holder is not known.
	HeldBy string `json:"held_by,omitempty"`
	// Working is a bead an agent's or a command's session has claimed; a
	// manual group's claim is a person's turn and no work in progress, as
	// on Home and the line.
	Working bool `json:"working"`
	// Worker is who works the bead, as `yardr session list` shows the
	// claiming session: the runner with the agent's name or pid. Absent
	// where no session claimed the bead, and on a manual group's claim,
	// which names no worker.
	Worker string `json:"worker,omitempty"`
	// Group is the group working the bead, as the claiming session
	// recorded it at claim. Absent wherever Worker is absent, so a reader
	// can tell a worked bead from either field.
	Group string `json:"group,omitempty"`
	// Kind and Model are the harness kind and model the claiming session
	// started with, as it recorded them at claim and served unchanged: a
	// route or group change after the claim does not move them. Absent
	// where Worker is, and where the session recorded none: an exec or
	// peer claim serves no kind, and no model where the group named none.
	Kind  string `json:"kind,omitempty"`
	Model string `json:"model,omitempty"`
	// EnteredStageAt is when the bead entered its stage, so a reader can
	// say how long it has stood there. Absent where no session claimed
	// the bead, and where the stay has no clock.
	EnteredStageAt *time.Time `json:"entered_stage_at,omitempty"`
}

// Flows answers the flows view: the depots each with its flows, each flow
// its stages in travel order, each stage its beads, the per-depot
// Elsewhere rows, the unassigned rows, and the travelling totals. Its
// stages, beads and lamps are /flows and the panel as served; its
// unassigned rows are the open beads outside the backlog with no depot.
func (s *Store) Flows(ctx context.Context) (*FlowsBoard, error) {
	depots, err := s.Depots(ctx)
	if err != nil {
		return nil, err
	}
	beads, err := s.ListBeads(ctx, BeadFilter{})
	if err != nil {
		return nil, err
	}
	inputs, err := s.arrivalInputs(ctx, beads)
	if err != nil {
		return nil, err
	}
	rows := make(map[string]eta.ArrivalRow, len(inputs))
	dependencyBlocked := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		rows[in.ID] = eta.ArrivalRowFor(in)
		dependencyBlocked[in.ID] = in.BlockedBy != ""
	}
	live, err := s.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	// A manual group's claim is a person's turn, not work in progress,
	// as on Home and the line.
	manual := map[string]bool{}
	bySession := make(map[string]Session, len(live))
	for _, sess := range live {
		if sess.Manual() {
			manual[sess.ID] = true
		}
		bySession[sess.ID] = sess
	}
	// When each bead entered its stage, the stay's clock, which the
	// served bead does not carry.
	stay := map[string]time.Time{}
	if err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
			stay[r.UUID] = r.StayStart
			return nil
		})
	}); err != nil {
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
	byGroup := make(map[string]Group, len(groups))
	for _, g := range groups {
		byGroup[g.Name] = g
	}
	// A taken seat is one live session of the group, wherever its bead
	// stands, counted once: the same count the claim refuses against.
	taken := map[string]int{}
	for _, sess := range live {
		taken[sess.Group]++
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	operator := settings[OperatorSetting]
	lamps, err := s.flowLamps(ctx, beads, live, routes, groups, operator, dependencyBlocked)
	if err != nil {
		return nil, err
	}
	row := func(b Bead) FlowBead {
		r := rows[b.ID]
		fb := FlowBead{Bead: b.ID, Title: b.Title, Expected: r.Expected,
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
				if entry := stay[b.UUID]; !entry.IsZero() {
					fb.EnteredStageAt = &entry
				}
			}
		}
		return fb
	}
	out := &FlowsBoard{Depots: []FlowsDepot{}, Unassigned: []FlowBead{}}
	var unassigned []Bead
	byDepot := map[string][]Bead{}
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
		fd := FlowsDepot{Name: depot.Name, Kind: depot.Kind, Flows: []FlowsFlow{}, Elsewhere: []FlowBead{}}
		// As on /flows: a depot whose flows cannot be read still shows
		// its beads, and the other depots their flows.
		rfs, err := s.DepotFlows(ctx, depot.Name)
		if err != nil {
			fd.Error = err.Error()
		}
		for _, rf := range rfs {
			ff := FlowsFlow{Name: rf.Flow.Name, Type: rf.Type, Source: rf.Source.String(), Stages: []FlowsStage{}}
			for _, st := range rf.Flow.Stages {
				fs := FlowsStage{Stage: st, Lamp: "none", Terminal: rf.Flow.IsTerminal(st), Beads: []FlowBead{}}
				// Routes may match on type; the depot's own flow is shown
				// for the default type, as `yardr flow show` does. The
				// probe names no originator, so it reads as the operator's.
				probe := Bead{Stage: st, Type: cmp.Or(rf.Type, "task"), Depot: &depot.Name, Priority: DefaultPriority}
				if r := MatchRoute(routes, probe, operator, byGroup); r != nil {
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
		var elsewhere []Bead
		onStage := map[[2]int][]Bead{}
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
				fault, turn := false, false
				for _, b := range bs {
					fd.Flows[f].Stages[st].Beads = append(fd.Flows[f].Stages[st].Beads, row(b))
					switch lamps[b.ID] {
					case "fault":
						fault = true
					case "turn":
						turn = true
					}
				}
				switch {
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

// flowSection is where the depot's bead stands: the indexes of its flow
// and stage, or -1 for its Elsewhere. As the web's placer: a bead that
// pins its flow follows that one, not the depot's, and stands off the
// line, as does one in no stage of the flow governing its type (or the
// depot's own, untyped).
func flowSection(b *Bead, rfs []DepotFlow) (flow, stage int) {
	if PinnedFlow(b) == "" {
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
func newestFirstBeads(bs []Bead) {
	slices.SortStableFunc(bs, func(a, b Bead) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// flowLamps answers each bead's lamp: fault where held, blocked by a
// dependency, blocked on a prompt or stranded; turn where only a person's
// turn waits, a manual group's stage or an open ask to the operator; none
// where nothing waits.
// The panel's fault/turn rule, without the words: a wait that is only an
// ask, with a manual reason if any, is the person's turn, and a bead
// whose reasons are all manual is parked, so both light turn.
func (s *Store) flowLamps(ctx context.Context, beads []Bead, live []Session, routes []Route, groups []Group, operator string, dependencyBlocked map[string]bool) (map[string]string, error) {
	manualGroup := map[string]bool{}
	byName := make(map[string]Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
		if g.Runner == "manual" {
			manualGroup[g.Name] = true
		}
	}
	stranded, err := s.Stranded(ctx, "")
	if err != nil {
		return nil, err
	}
	lacking := map[string]bool{}
	for _, sb := range stranded {
		lacking[sb.Bead] = true
	}
	// The sessions that can wait on a prompt, by bead. A manual group's
	// claim is none of them: nothing runs in it, so nothing asks.
	agents := map[string][]Session{}
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
		q, err := s.UserAsks(ctx, operator)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
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
		fault := b.Hold || dependencyBlocked[b.ID] || lacking[b.ID]
		for _, sess := range agents[b.ID] {
			if blocked, err := s.flowBlocked(ctx, sess); err != nil {
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
		} else if r := MatchRoute(routes, b, operator, byName); r != nil {
			group = r.Worker()
		}
		if manualGroup[group] || asked[b.ID] {
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
func (s *Store) flowBlocked(ctx context.Context, sess Session) (bool, error) {
	evs, err := s.Events(ctx, sess.Bead, 200)
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

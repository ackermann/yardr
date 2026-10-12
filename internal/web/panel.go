package web

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"slices"

	"yardr.dev/yardr/internal/flowdraw"
	"yardr.dev/yardr/internal/store"
)

// panel reports whether the request asks for the yard panel (?panel=1), the
// flows drawn as track with the beads on them, after ?all=1 on /beads.
func panel(r *http.Request) bool {
	switch r.URL.Query().Get("panel") {
	case "", "0", "false":
		return false
	}
	return true
}

// PanelDepot is one depot as the panel shows it: each flow that governs it
// drawn as track, and the open beads in no stage of them for the off-the-line
// row. It is HTML-only: the JSON of /flows is Flows either way.
type PanelDepot struct {
	Name  string
	Kind  string
	Flows []PanelFlow
	// Elsewhere are the depot's open beads in no stage of the flows above,
	// the newest change first, as on /flows.
	Elsewhere []Card
	Error     string
}

// PanelFlow is one flow of a depot drawn with the yard on it.
type PanelFlow struct {
	Name   string
	Type   string
	Source string
	// SVG is what flowdraw.Panel drew, safe to send unescaped: every name
	// from the yard in it is escaped there.
	SVG template.HTML
}

// Panel is the yard panel: every depot's flows drawn as track with the open
// beads standing on them as blocks. The template calls it; the JSON never
// carries it.
func (d *Flows) Panel() []PanelDepot {
	out := make([]PanelDepot, 0, len(d.Depots))
	for i, depot := range d.Depots {
		pd := PanelDepot{Name: depot.Name, Kind: depot.Kind, Flows: []PanelFlow{},
			Elsewhere: depot.Elsewhere, Error: depot.Error}
		for j, col := range depot.Flows {
			pd.Flows = append(pd.Flows, d.panelFlow(i, j, col))
		}
		out = append(out, pd)
	}
	return out
}

// panelFlow draws depot i's jth flow with the yard on it: the flow as
// `yardr flow show` reads it, with the groups the routes put at its stages,
// and the open beads of each stage as blocks.
func (d *Flows) panelFlow(i, j int, col FlowColumn) PanelFlow {
	pf := PanelFlow{Name: col.Name, Type: col.Type, Source: col.Source}
	if i >= len(d.rfs) || j >= len(d.rfs[i]) || d.rfs[i][j].Flow == nil {
		return pf
	}
	rf := d.rfs[i][j]
	diagram := flowdraw.Diagram{Flow: *rf.Flow, Groups: map[string]flowdraw.Group{}}
	for _, st := range col.Stages {
		if st.Group == "" {
			continue
		}
		if k := slices.IndexFunc(d.groups, func(g store.Group) bool { return g.Name == st.Group }); k >= 0 {
			diagram.Groups[st.Stage] = flowdraw.GroupOf(d.groups[k])
		}
	}
	standing := flowdraw.Standing{Blocks: map[string][]flowdraw.Block{},
		Lamps: map[string]flowdraw.Lamp{}, Noroute: map[string]bool{}}
	sections := map[string]string{}
	for _, st := range col.Stages {
		sections[st.Stage] = st.Section
		// A terminal stage closes the bead that enters it, so no bead ever
		// waits there for a route: only the open stages can be unrouted.
		if st.Group == "" && !st.Terminal {
			standing.Noroute[st.Stage] = true
		}
		if l, ok := d.lamps[st.Section]; ok {
			standing.Lamps[st.Stage] = flowdraw.Lamp(l)
		}
		for _, c := range st.Beads {
			m := d.marks[c.ID]
			b := flowdraw.Block{ID: c.ID, Working: c.Working, Train: c.Train,
				Held: c.Held, GaveUp: m.gaveUp, Blocked: m.blocked,
				Stalled: m.stalled, Dep: m.dep, FailedBack: m.failedBack}
			if c.Working {
				b.Group = c.Group
			}
			standing.Blocks[st.Stage] = append(standing.Blocks[st.Stage], b)
		}
	}
	standing.Stage = func(stage string) string { return "/flows#" + sections[stage] }
	standing.Bead = func(id string) string { return "/beads/" + url.PathEscape(id) }
	pf.SVG = template.HTML(flowdraw.Panel(diagram, standing))
	return pf
}

// gaveUp reports whether a held bead's hold is after repeated failures: a
// gave_up event stands after its last held.
func (r *reader) gaveUp(ctx context.Context, bead string, hold bool) bool {
	return hold && r.gaveUpAfter(ctx, bead) != nil
}

// stalled reports whether a session's last word is session_stalled: the
// dispatcher's report that its agent sat idle since its first prompt and
// did nothing. A later blocked or unblocked says the agent asked and is
// then the last word instead; any other event is none of them.
func (r *reader) stalled(ctx context.Context, sess store.Session) bool {
	evs := r.eventsOf(ctx, sess.Bead)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Data["session"] != sess.ID {
			continue
		}
		switch {
		case evs[i].Kind == "session_stalled":
			return true
		case evs[i].Kind == "session_blocked" || evs[i].Kind == "session_unblocked":
			return false
		case sessionEnded(evs[i].Kind):
			return false
		}
	}
	return false
}

// failedBack reports whether a bead is back because something failed: its
// last move carries outcome failed or changes. A move by hand carries no
// outcome, and a bead never moved has no last one.
func (r *reader) failedBack(ctx context.Context, bead string) bool {
	evs := r.eventsOf(ctx, bead)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind != "advanced" {
			continue
		}
		o, _ := evs[i].Data["outcome"].(string)
		return o == "failed" || o == "changes"
	}
	return false
}

// panelMarks reads the panel's blocked vocabulary for d: per bead what
// colours its block, per stage whether a wait lamp burns. It runs only when
// the panel is asked for, so /flows keeps the reads it had. The JSON never
// carries what it reads.
func (s *Server) panelMarks(ctx context.Context, d *Flows) error {
	sessions, err := s.st.Sessions(ctx, false)
	if err != nil {
		return err
	}
	beads, err := s.st.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		return err
	}
	waits, parked, err := s.waits(ctx, &yard{beads: beads, sessions: sessions})
	if err != nil {
		return err
	}
	waiting := map[string]bool{}
	for _, w := range waits {
		waiting[w.Bead] = true
	}
	resting := map[string]bool{}
	for _, w := range parked {
		resting[w.Bead] = true
	}
	d.lamps = map[string]flowLamp{}
	for _, depot := range d.Depots {
		for _, col := range depot.Flows {
			for _, st := range col.Stages {
				fault, turn := false, false
				for _, c := range st.Beads {
					if waiting[c.ID] {
						fault = true
						break
					}
					if resting[c.ID] {
						turn = true
					}
				}
				switch {
				case fault:
					d.lamps[st.Section] = lampFault
				case turn:
					d.lamps[st.Section] = lampTurn
				}
			}
		}
	}
	agents := map[string][]store.Session{}
	for _, sess := range sessions {
		if !isManual(sess) {
			agents[sess.Bead] = append(agents[sess.Bead], sess)
		}
	}
	rd := s.newReader(nil)
	d.marks = map[string]panelMark{}
	seen := map[string]Card{}
	for _, depot := range d.Depots {
		for _, col := range depot.Flows {
			for _, st := range col.Stages {
				for _, c := range st.Beads {
					seen[c.ID] = c
				}
			}
		}
	}
	for id, c := range seen {
		var m panelMark
		for _, sess := range agents[id] {
			if rd.blocked(ctx, sess) {
				m.blocked = true
			}
			if rd.stalled(ctx, sess) {
				m.stalled = true
			}
		}
		m.gaveUp = rd.gaveUp(ctx, id, c.Held)
		m.failedBack = rd.failedBack(ctx, id)
		if blockers, err := s.st.OpenBlockers(ctx, id); err != nil {
			s.log.Error("web: open blockers", "bead", id, "err", err)
		} else if len(blockers) > 0 {
			m.dep = blockers[0]
		}
		d.marks[id] = m
	}
	return nil
}

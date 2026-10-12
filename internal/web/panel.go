package web

import (
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
	standing := flowdraw.Standing{Blocks: map[string][]flowdraw.Block{}}
	sections := map[string]string{}
	for _, st := range col.Stages {
		sections[st.Stage] = st.Section
		for _, c := range st.Beads {
			b := flowdraw.Block{ID: c.ID, Working: c.Working, Train: c.Train}
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

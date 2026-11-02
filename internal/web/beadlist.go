package web

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"time"

	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/store"
)

// Beads is the /beads page: the open beads, the newest change first.
type Beads struct {
	// Depot and Stage are the filters of the query string, "" for none.
	Depot string `json:"depot,omitempty"`
	Stage string `json:"stage,omitempty"`
	// All says the closed beads are listed too (?all=1), every one, as
	// `yardr bead list --all` lists them.
	All   bool      `json:"all,omitempty"`
	Beads []BeadRow `json:"beads"`
	// Depots and Stages are what the filters can be set to: the yard's depots,
	// and the stages that hold a listed bead (of Depot, when it is set).
	Depots []string `json:"depots"`
	Stages []string `json:"stages"`
	// DepotCounts and StageCounts say, for each name of Depots and of Stages,
	// how many beads the page lists once that name is chosen, the other
	// filter kept: a depot's under Stage, a stage's under Depot. AnyDepot
	// and AnyStage are the same for each row's "all", which clears that
	// filter alone. So each count is the length of the list its own link
	// opens. (Not "all": that is the closed beads' flag above.)
	DepotCounts map[string]int `json:"depot_counts"`
	StageCounts map[string]int `json:"stage_counts"`
	AnyDepot    int            `json:"any_depot"`
	AnyStage    int            `json:"any_stage"`
	Head        uint64         `json:"head"`
}

// BeadRow is one bead of /beads: the bead as `yardr bead list --json`
// prints it, and beside its fields the two a card has that the bead alone
// does not say.
type BeadRow struct {
	store.Bead
	// Held is the bead's hold, by the name the cards of every other page
	// have it under.
	Held bool `json:"held,omitempty"`
	// Working is a bead an agent's or a command's session has claimed.
	Working bool `json:"working"`
	// ETA and Late are the bead's estimate, absent when there is nothing
	// to say; the same shapes `bead show --json` carries, which
	// signalbox's departure board reads here.
	ETA  *eta.ETA  `json:"eta,omitempty"`
	Late *eta.Late `json:"late,omitempty"`
}

// Card is the bead as the page shows it.
func (r BeadRow) Card() Card {
	c := card(&r.Bead, nil, nil)
	c.Working = r.Working
	c.ETA, c.Late = r.ETA, r.Late
	return c
}

// Link is the page with its filters set to depot and stage.
func (b Beads) Link(depot, stage string) string {
	q := url.Values{}
	if b.All {
		q.Set("all", "1")
	}
	if depot != "" {
		q.Set("depot", depot)
	}
	if stage != "" {
		q.Set("stage", stage)
	}
	if len(q) == 0 {
		return "/beads"
	}
	return "/beads?" + q.Encode()
}

// Self is the page as it was asked for, which it asks for again when
// something happens in the yard.
func (b Beads) Self() string { return b.Link(b.Depot, b.Stage) }

// tally sets the counts of the filters' links from beads, the listed set
// before either filter is applied: a bead counts for a depot when it stands
// in the stage filter, and for a stage when it stands in the depot filter,
// as the page the link opens would list it. A depot with no bead has its
// zero, so every name of Depots has a count.
func (b *Beads) tally(beads []store.Bead) {
	b.DepotCounts, b.StageCounts = make(map[string]int, len(b.Depots)), map[string]int{}
	for _, name := range b.Depots {
		b.DepotCounts[name] = 0
	}
	for i := range beads {
		bead := &beads[i]
		if b.Stage == "" || bead.Stage == b.Stage {
			b.AnyDepot++
			if bead.Depot != nil {
				b.DepotCounts[*bead.Depot]++
			}
		}
		if b.Depot == "" || in(bead, b.Depot) {
			b.AnyStage++
			b.StageCounts[bead.Stage]++
		}
	}
}

func (s *Server) handleBeads(w http.ResponseWriter, r *http.Request) {
	ctx, q := r.Context(), r.URL.Query()
	d := Beads{Depot: q.Get("depot"), Stage: q.Get("stage"), All: all(r), Beads: []BeadRow{}, Depots: []string{}, Stages: []string{}}
	var err error
	if d.Head, err = s.st.Head(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	// One read for the list and for the shell's line beside it.
	y, err := s.readYard(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, depot := range y.depots {
		d.Depots = append(d.Depots, depot.Name)
	}
	beads := y.beads
	if d.All {
		// The line beside the list still counts the open ones.
		if beads, err = s.st.ListBeads(ctx, store.BeadFilter{All: true, Viewer: s.acting(ctx)}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	manual := manualClaims(y.sessions)
	// The rows' estimates over one shared queue snapshot: every bead on
	// the page reads the same response. Closed beads have no estimate,
	// so ?all=1 reads none for them.
	var ids []string
	for i := range beads {
		if beads[i].Status == "open" {
			ids = append(ids, beads[i].ID)
		}
	}
	ests, err := s.estimates(ctx, s.st, ids, time.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, b := range beads {
		if d.Depot != "" && !in(&b, d.Depot) {
			continue
		}
		d.Stages = append(d.Stages, b.Stage)
		if d.Stage == "" || b.Stage == d.Stage {
			row := BeadRow{Bead: b, Held: b.Hold, Working: working(&b, manual)}
			if est := ests[b.ID]; est != nil {
				row.ETA, row.Late = est.ETA, est.Late
			}
			d.Beads = append(d.Beads, row)
		}
	}
	slices.Sort(d.Stages)
	d.Stages = slices.Compact(d.Stages)
	d.tally(beads)
	// The newest change first, as the cards of newestFirst.
	slices.SortStableFunc(d.Beads, func(a, b BeadRow) int {
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), cmp.Compare(a.ID, b.ID))
	})
	s.respond(w, r, Page{Template: "beads", Title: "Beads", Menu: "beads", Data: d, yard: y, head: d.Head, stream: "seq"})
}

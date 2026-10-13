package web

import (
	"net/http"

	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// The configuration lists of the yard: each answers what its list command
// prints with --json, under the route's own name, and the yard's head from
// before the list was read, as the pages' JSON has it. Each has a page
// under the Config entry, with /config as their hub. docs/web.md ("The
// lists") has the rule they follow.

// Depots is /depots: `yardr depot list --json`.
type Depots struct {
	Depots []store.Depot `json:"depots"`
	Head   uint64        `json:"head"`
}

// Groups is /groups: `yardr group list --json`, each row with the
// subscription it burns beside the command's fields.
type Groups struct {
	Groups []GroupRow `json:"groups"`
	Head   uint64     `json:"head"`
}

// GroupRow is one group of /groups: the command's row, and the resolved
// provider beside it, as CrewRow carries it.
type GroupRow struct {
	store.Group
	// Provider is the subscription the group burns (providerOf); absent
	// where none is known and for a group of another runner.
	Provider string `json:"provider,omitempty"`
}

// providerOf is the subscription a herdr group or crew member with cfg
// burns: its provider setting when set, even where no source answers for
// it, else its kind's mapping through the first source by name that lists
// it. "" for any other runner and where neither answers. It reads the
// sources' tables and runs no command.
func providerOf(runner string, cfg map[string]any, as []limits.Answerer) string {
	if runner != "herdr" {
		return ""
	}
	kind, _ := cfg["kind"].(string)
	setting, _ := cfg["provider"].(string)
	// Resolve names the setting where one is set, else the mapping where
	// one answers: exactly what the row carries.
	_, name, _ := limits.Resolve(as, kind, setting)
	return name
}

// Routes is /routes: `yardr route list --json`, in match order.
type Routes struct {
	Routes []store.Route `json:"routes"`
	Head   uint64        `json:"head"`
}

// Peers is /peers: `yardr peer list --json`.
type Peers struct {
	Peers []store.Peer `json:"peers"`
	Head  uint64       `json:"head"`
}

// DepEnd is one end of an edge of /deps: the bead's title, stage and
// status, what the id the edge carries does not say.
type DepEnd struct {
	Title  string `json:"title,omitempty"`
	Stage  string `json:"stage,omitempty"`
	Status string `json:"status,omitempty"`
}

// DepRow is one edge of /deps: the command's row, with each end's bead
// beside it, as GroupRow carries its provider.
type DepRow struct {
	store.Dep
	FromBead DepEnd `json:"from_bead"`
	ToBead   DepEnd `json:"to_bead"`
}

// DepGroup is one blocked bead of /deps: the bead and the edges that
// hold it, the oldest first.
type DepGroup struct {
	Bead Card     `json:"bead"`
	Deps []DepRow `json:"deps"`
}

// Deps is /deps: what waits on what now — the edges with an open end,
// the oldest first, grouped by the blocked bead for the page; with
// ?all=1 every edge of the yard, the ones between closed beads too.
type Deps struct {
	Deps   []DepRow   `json:"deps"`
	Groups []DepGroup `json:"groups"`
	// All is ?all=1, the closed-only edges too, as bead list --all.
	All bool `json:"all"`
	// Total is the yard's edges in all, what ?all=1 lists: the page's
	// toggle shows when there is anything behind it.
	Total int    `json:"total"`
	Head  uint64 `json:"head"`
}

// Self is the page as it was asked for, which it asks for again when
// something happens in the yard.
func (d Deps) Self() string {
	if d.All {
		return "/deps?all=1"
	}
	return "/deps"
}

// all reports whether the request asks for the whole list (?all=1), as a
// list command's --all does: the closed beads, the ended sessions.
func all(r *http.Request) bool {
	switch r.URL.Query().Get("all") {
	case "", "0", "false":
		return false
	}
	return true
}

// listedPage answers r with one list that has a page: read reads it, wrap
// puts it and the head from before the read into the route's answer, and
// the page renders that answer, live on the shell's stream. A list the
// store has nothing for is empty, never null.
func listedPage[T any](s *Server, w http.ResponseWriter, r *http.Request, read func() ([]T, error), wrap func([]T, uint64) any, template, title string) {
	// The head first, as on a page: the stream after it brings whatever
	// changed while the list was read.
	head, err := s.st.Head(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := read()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if rows == nil {
		rows = []T{}
	}
	s.respond(w, r, Page{Template: template, Title: title, Menu: "config", Data: wrap(rows, head), head: head, stream: "seq"})
}

func (s *Server) handleDepots(w http.ResponseWriter, r *http.Request) {
	listedPage(s, w, r, func() ([]store.Depot, error) { return s.st.Depots(r.Context()) },
		func(rows []store.Depot, head uint64) any { return Depots{rows, head} }, "depots", "Depots")
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	listedPage(s, w, r, func() ([]GroupRow, error) {
		gs, err := s.st.Groups(r.Context())
		if err != nil {
			return nil, err
		}
		as, err := limits.Answerers(r.Context(), s.st, s.home)
		if err != nil {
			return nil, err
		}
		rows := make([]GroupRow, 0, len(gs))
		for _, g := range gs {
			rows = append(rows, GroupRow{Group: g, Provider: providerOf(g.Runner, g.Config, as)})
		}
		return rows, nil
	}, func(rows []GroupRow, head uint64) any { return Groups{rows, head} }, "groups", "Groups")
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	listedPage(s, w, r, func() ([]store.Route, error) {
		rs, err := s.st.Routes(r.Context())
		store.SortRoutes(rs)
		return rs, err
	}, func(rows []store.Route, head uint64) any { return Routes{rows, head} }, "routes", "Routes")
}

// Config is /config: the hub of the yard's configuration pages, the links
// to the six lists. It is static links with no live part of its own; the
// shell's stream keeps the column beside it.
type Config struct {
	Links []ConfigLink `json:"links"`
	Head  uint64       `json:"head"`
}

// ConfigLink is one page of the hub: where it is and what it lists.
type ConfigLink struct {
	Title string `json:"title"`
	Path  string `json:"path"`
	Blurb string `json:"blurb"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	head, err := s.st.Head(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := Config{Head: head, Links: []ConfigLink{
		{Title: "Depots", Path: "/depots", Blurb: "the repositories and directories beads are worked in"},
		{Title: "Groups", Path: "/groups", Blurb: "who works a stage"},
		{Title: "Routes", Path: "/routes", Blurb: "which group a stage sends its beads to, first match wins"},
		{Title: "Peers", Path: "/peers", Blurb: "the yards this one is linked with"},
		{Title: "Deps", Path: "/deps", Blurb: "the edges with an open end, grouped by the blocked bead"},
		{Title: "Limits", Path: "/limits", Blurb: "what the subscriptions the yard burns still carry"},
	}}
	s.respond(w, r, Page{Template: "config", Title: "Config", Menu: "config", Data: out, head: head})
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	listedPage(s, w, r, func() ([]store.Peer, error) { return s.st.Peers(r.Context()) },
		func(rows []store.Peer, head uint64) any { return Peers{rows, head} }, "peers", "Peers")
}

func (s *Server) handleDeps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := Deps{All: all(r), Deps: []DepRow{}, Groups: []DepGroup{}}
	var err error
	if d.Head, err = s.st.Head(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	edges, err := s.st.AllDeps(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Total = len(edges)
	beads, err := s.st.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sessions, err := s.st.Sessions(ctx, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	manual := manualClaims(sessions)
	byID := make(map[string]*store.Bead, len(beads))
	for i := range beads {
		byID[beads[i].ID] = &beads[i]
	}
	end := func(id string) DepEnd {
		b, ok := byID[id]
		if !ok {
			return DepEnd{}
		}
		return DepEnd{Title: b.Title, Stage: b.Stage, Status: b.Status}
	}
	// What waits on what now: an edge with no open end waits on nothing.
	// ?all=1 keeps those too.
	for _, e := range edges {
		from, to := end(e.From), end(e.To)
		if !d.All && from.Status != "open" && to.Status != "open" {
			continue
		}
		d.Deps = append(d.Deps, DepRow{Dep: e, FromBead: from, ToBead: to})
	}
	// Grouped by the blocked bead, the longest waiting first: the edges
	// come oldest first, so the first edge seen for a bead is its oldest.
	at := map[string]int{}
	for _, row := range d.Deps {
		// A parent edge points from the train to its child, but the train
		// is the bead held by that child. Other edge kinds group under To:
		// for discovered-from, that keeps the origin with its discoveries.
		blocked := row.To
		if row.Kind == store.DepParent {
			blocked = row.From
		}
		i, ok := at[blocked]
		if !ok {
			c := Card{ID: blocked}
			if b := byID[blocked]; b != nil {
				c = card(b, manual)
			}
			i, at[blocked] = len(d.Groups), len(d.Groups)
			d.Groups = append(d.Groups, DepGroup{Bead: c})
		}
		d.Groups[i].Deps = append(d.Groups[i].Deps, row)
	}
	s.respond(w, r, Page{Template: "deps", Title: "Deps", Menu: "config", Data: d, head: d.Head, stream: "seq"})
}

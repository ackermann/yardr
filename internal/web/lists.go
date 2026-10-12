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

// Deps is /deps: every edge of the yard, each once, as `yardr dep list
// <bead> --json` prints one bead's.
type Deps struct {
	Deps []store.Dep `json:"deps"`
	Head uint64      `json:"head"`
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
		{Title: "Deps", Path: "/deps", Blurb: "every edge of the yard, each once"},
		{Title: "Limits", Path: "/limits", Blurb: "what the subscriptions the yard burns still carry"},
	}}
	s.respond(w, r, Page{Template: "config", Title: "Config", Menu: "config", Data: out, head: head})
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	listedPage(s, w, r, func() ([]store.Peer, error) { return s.st.Peers(r.Context()) },
		func(rows []store.Peer, head uint64) any { return Peers{rows, head} }, "peers", "Peers")
}

func (s *Server) handleDeps(w http.ResponseWriter, r *http.Request) {
	listedPage(s, w, r, func() ([]store.Dep, error) { return s.st.AllDeps(r.Context()) },
		func(rows []store.Dep, head uint64) any { return Deps{rows, head} }, "deps", "Deps")
}

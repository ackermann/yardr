package web

import (
	"net/http"

	"yardr.dev/yardr/internal/store"
)

// The configuration lists of the yard: each answers what its list command
// prints with --json, under the route's own name, and the yard's head from
// before the list was read, as the pages' JSON has it. The depot, group
// and route lists have a page under the Config entry, with /config as
// their hub; the peer and edge lists are JSON whatever the request
// accepts. docs/web.md ("The lists") has the rule they follow.

// Depots is /depots: `yardr depot list --json`.
type Depots struct {
	Depots []store.Depot `json:"depots"`
	Head   uint64        `json:"head"`
}

// Groups is /groups: `yardr group list --json`.
type Groups struct {
	Groups []store.Group `json:"groups"`
	Head   uint64        `json:"head"`
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

// listed answers r with one list of the yard: read reads it, and wrap puts
// it and the head from before the read into the route's answer. A list the
// store has nothing for is empty, never null.
func listed[T any](s *Server, w http.ResponseWriter, r *http.Request, read func() ([]T, error), wrap func([]T, uint64) any) {
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
	// The yard moves under the list; a stored copy is a stale one.
	w.Header().Set("Cache-Control", "no-store")
	s.data(w, r, http.StatusOK, wrap(rows, head))
}

// listedPage answers r with one list that has a page: read reads it, wrap
// puts it and the head from before the read into the route's answer, and
// the page renders that answer, live on the shell's stream. A list the
// store has nothing for is empty, never null. The JSON is what listed
// answers for a list with no page, field for field.
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
	listedPage(s, w, r, func() ([]store.Group, error) { return s.st.Groups(r.Context()) },
		func(rows []store.Group, head uint64) any { return Groups{rows, head} }, "groups", "Groups")
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	listedPage(s, w, r, func() ([]store.Route, error) {
		rs, err := s.st.Routes(r.Context())
		store.SortRoutes(rs)
		return rs, err
	}, func(rows []store.Route, head uint64) any { return Routes{rows, head} }, "routes", "Routes")
}

// Config is /config: the hub of the yard's configuration pages, the links
// to the lists that have one. The lists still without a route join it
// later. It is static links with no live part of its own; the shell's
// stream keeps the column beside it.
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
	}}
	s.respond(w, r, Page{Template: "config", Title: "Config", Menu: "config", Data: out, head: head})
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	listed(s, w, r, func() ([]store.Peer, error) { return s.st.Peers(r.Context()) },
		func(rows []store.Peer, head uint64) any { return Peers{rows, head} })
}

func (s *Server) handleDeps(w http.ResponseWriter, r *http.Request) {
	listed(s, w, r, func() ([]store.Dep, error) { return s.st.AllDeps(r.Context()) },
		func(rows []store.Dep, head uint64) any { return Deps{rows, head} })
}

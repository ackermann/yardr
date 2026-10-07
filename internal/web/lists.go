package web

import (
	"net/http"

	"yardr.dev/yardr/internal/store"
)

// The lists of the yard that have no page: each answers what its list
// command prints with --json, under the route's own name, and the yard's
// head from before the list was read, as the pages' JSON has it. They are
// JSON whatever the request accepts: there is no page to pick instead.
// docs/web.md ("The lists") has the rule they follow.

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

func (s *Server) handleDepots(w http.ResponseWriter, r *http.Request) {
	listed(s, w, r, func() ([]store.Depot, error) { return s.st.Depots(r.Context()) },
		func(rows []store.Depot, head uint64) any { return Depots{rows, head} })
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	listed(s, w, r, func() ([]store.Group, error) { return s.st.Groups(r.Context()) },
		func(rows []store.Group, head uint64) any { return Groups{rows, head} })
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	listed(s, w, r, func() ([]store.Route, error) {
		rs, err := s.st.Routes(r.Context())
		store.SortRoutes(rs)
		return rs, err
	}, func(rows []store.Route, head uint64) any { return Routes{rows, head} })
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	listed(s, w, r, func() ([]store.Peer, error) { return s.st.Peers(r.Context()) },
		func(rows []store.Peer, head uint64) any { return Peers{rows, head} })
}

func (s *Server) handleDeps(w http.ResponseWriter, r *http.Request) {
	listed(s, w, r, func() ([]store.Dep, error) { return s.st.AllDeps(r.Context()) },
		func(rows []store.Dep, head uint64) any { return Deps{rows, head} })
}

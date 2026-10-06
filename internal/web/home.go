package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"yardr.dev/yardr/internal/store"
)

// Home is the yard's home page: its depots and where each depot's open beads
// stand, and the feed of what happened.
type Home struct {
	Yard   string      `json:"yard"`
	Depots []HomeDepot `json:"depots"`
	Feed
	// sessions are the live sessions the counts were made with, for the
	// line, which counts them too.
	sessions []store.Session
}

// HomeDepot is one depot's open beads, counted per stage of the flows that
// govern the depot.
type HomeDepot struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Open, Working and Held count the depot's open beads: all of them,
	// those an agent's or a command's session has claimed, and those held.
	// A manual group's claim is a person's turn and no work in progress.
	Open    int        `json:"open"`
	Working int        `json:"working"`
	Held    int        `json:"held"`
	Flows   []HomeFlow `json:"flows"`
	// Elsewhere counts the open beads in a stage their depot's flow lacks: a
	// bead pinned to another flow, or one a changed flow left behind.
	Elsewhere []StageCount `json:"elsewhere,omitempty"`
	// Error is why the depot's flows could not be read (a checkout git cannot
	// read); its beads are then all counted in Elsewhere.
	Error string `json:"error,omitempty"`
}

// HomeFlow is one flow of a depot: the depot's own (Type "") or a bead type's.
type HomeFlow struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	// Stages are the flow's stages an open bead can sit in, in the flow's
	// order: a terminal stage closes the bead that enters it.
	Stages []StageCount `json:"stages"`
}

type StageCount struct {
	Stage string `json:"stage"`
	Open  int    `json:"open"`
}

// overview reads the yard for the home page and for the shell's line.
func (s *Server) overview(ctx context.Context) (*Home, error) {
	depots, err := s.st.Depots(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := s.st.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	out := &Home{Yard: s.yardName(ctx), Depots: []HomeDepot{}, sessions: sessions}
	manual := manualClaims(sessions)
	for _, depot := range depots {
		beads, err := s.st.ListBeads(ctx, store.BeadFilter{Depot: depot.Name})
		if err != nil {
			return nil, err
		}
		hr := HomeDepot{Name: depot.Name, Kind: depot.Kind, Flows: []HomeFlow{}}
		// One depot whose repository cannot be read must not take the page
		// down with it: the others still show.
		flows, err := s.st.DepotFlows(ctx, depot.Name)
		if err != nil {
			hr.Error = err.Error()
		}
		// byType maps a bead type to the index of its flow; the depot's own
		// flow, when it has one, takes every other type.
		byType, own := map[string]int{}, -1
		for _, rf := range flows {
			hf := HomeFlow{Name: rf.Flow.Name, Type: rf.Type, Stages: []StageCount{}}
			for _, st := range rf.Flow.Stages {
				if !slices.Contains(rf.Flow.Terminal, st) {
					hf.Stages = append(hf.Stages, StageCount{Stage: st})
				}
			}
			if rf.Type == "" {
				own = len(hr.Flows)
			} else {
				byType[rf.Type] = len(hr.Flows)
			}
			hr.Flows = append(hr.Flows, hf)
		}
		for _, b := range beads {
			hr.Open++
			if working(&b, manual) {
				hr.Working++
			}
			if b.Hold {
				hr.Held++
			}
			i, ok := byType[b.Type]
			if !ok {
				i = own
			}
			if i < 0 || !count(hr.Flows[i].Stages, b.Stage) {
				if !count(hr.Elsewhere, b.Stage) {
					hr.Elsewhere = append(hr.Elsewhere, StageCount{Stage: b.Stage, Open: 1})
				}
			}
		}
		slices.SortFunc(hr.Elsewhere, func(a, b StageCount) int {
			if a.Stage < b.Stage {
				return -1
			}
			return 1
		})
		out.Depots = append(out.Depots, hr)
	}
	return out, nil
}

// count adds one to stage's count in counts, and reports whether it is there.
func count(counts []StageCount, stage string) bool {
	for i := range counts {
		if counts[i].Stage == stage {
			counts[i].Open++
			return true
		}
	}
	return false
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	var before uint64
	if v := r.URL.Query().Get("before"); v != "" {
		var err error
		if before, err = strconv.ParseUint(v, 10, 64); err != nil || before == 0 {
			s.respond(w, r, Page{Status: http.StatusBadRequest, Template: "error", Title: "Bad request",
				Data: errorData{Error: "before is an event's seq, not " + v}})
			return
		}
	}
	// The feed first: the line is read after it, so a bead the feed shows
	// created is never missing from the counts beside it.
	feed, err := s.feed(r.Context(), before)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ov, err := s.overview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ov.Feed = *feed
	l, err := s.line(r.Context(), ov)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respond(w, r, Page{Template: "home", Title: "Home", Menu: "home", Data: ov, line: l, head: feed.Head})
}

// Unbuilt is the Data of a menu page no child of the train has built yet.
type Unbuilt struct {
	Page  string `json:"page"`
	Built bool   `json:"built"`
}

func (s *Server) handleUnbuilt(name string) http.HandlerFunc {
	title := name
	for _, m := range menu {
		if m.Name == name {
			title = m.Label
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// 501, not 200: a client reading JSON must not take the stub for
		// an empty list.
		s.respond(w, r, Page{Status: http.StatusNotImplemented, Template: "unbuilt", Title: title, Menu: name,
			Data: Unbuilt{Page: name}})
	}
}

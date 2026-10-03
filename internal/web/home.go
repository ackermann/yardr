package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"yardr.dev/yardr/internal/store"
)

// Home is the yard's home page: its rigs and where each rig's open beads
// stand, and the feed of what happened.
type Home struct {
	Yard string    `json:"yard"`
	Rigs []HomeRig `json:"rigs"`
	Feed
}

// HomeRig is one rig's open beads, counted per stage of the flows that
// govern the rig.
type HomeRig struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Open, Working and Held count the rig's open beads: all of them,
	// those a session has claimed, and those held.
	Open    int        `json:"open"`
	Working int        `json:"working"`
	Held    int        `json:"held"`
	Flows   []HomeFlow `json:"flows"`
	// Elsewhere counts the open beads in a stage their rig's flow lacks: a
	// bead pinned to another flow, or one a changed flow left behind.
	Elsewhere []StageCount `json:"elsewhere,omitempty"`
	// Error is why the rig's flows could not be read (a checkout git cannot
	// read); its beads are then all counted in Elsewhere.
	Error string `json:"error,omitempty"`
}

// HomeFlow is one flow of a rig: the rig's own (Type "") or a bead type's.
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

// Line is the summary of the line the right column shows on every page.
type Line struct {
	Open    int       `json:"open"`
	Working int       `json:"working"`
	Held    int       `json:"held"`
	Rigs    []LineRig `json:"rigs"`
}

type LineRig struct {
	Name string `json:"name"`
	Open int    `json:"open"`
}

func (h *Home) line() *Line {
	l := &Line{Rigs: []LineRig{}}
	for _, r := range h.Rigs {
		l.Open += r.Open
		l.Working += r.Working
		l.Held += r.Held
		l.Rigs = append(l.Rigs, LineRig{Name: r.Name, Open: r.Open})
	}
	return l
}

// overview reads the yard for the home page and for the shell's line.
func (s *Server) overview(ctx context.Context) (*Home, error) {
	rigs, err := s.st.Rigs(ctx)
	if err != nil {
		return nil, err
	}
	out := &Home{Yard: s.yardName(ctx), Rigs: []HomeRig{}}
	for _, rig := range rigs {
		beads, err := s.st.ListBeads(ctx, store.BeadFilter{Rig: rig.Name})
		if err != nil {
			return nil, err
		}
		hr := HomeRig{Name: rig.Name, Kind: rig.Kind, Flows: []HomeFlow{}}
		// One rig whose repository cannot be read must not take the page
		// down with it: the others still show.
		flows, err := s.st.RigFlows(ctx, rig.Name)
		if err != nil {
			hr.Error = err.Error()
		}
		// byType maps a bead type to the index of its flow; the rig's own
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
			if b.Session != nil {
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
		out.Rigs = append(out.Rigs, hr)
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
	s.respond(w, r, Page{Template: "home", Title: "Home", Menu: "home", Data: ov, line: ov.line()})
}

// handleLine is the right column as a page of its own, for a window with no
// room for the column.
func (s *Server) handleLine(w http.ResponseWriter, r *http.Request) {
	ov, err := s.overview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	l := ov.line()
	s.respond(w, r, Page{Template: "line", Title: "Line", Menu: "line", Data: l, line: l})
}

// Unbuilt is the Data of a menu page no child of the epic has built yet.
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

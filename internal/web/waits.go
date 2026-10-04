package web

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Line is the right column, the same on every page: the beads that wait for
// a person, and under them the line in numbers.
type Line struct {
	// Waits are the open beads something stops: held, blocked on a prompt
	// or stranded. The one that began to wait last comes first.
	Waits []Waiting `json:"waits"`
	// Parked are the open beads nothing stops but that no agent will move:
	// their only reason is a stage of a manual group. In the same order.
	Parked []Waiting `json:"parked"`
	Open   int       `json:"open"`
	// Working counts the beads an agent's or a command's session has
	// claimed; a manual group's claim is not work in progress.
	Working int `json:"working"`
	Held    int `json:"held"`
	// Sessions counts the sessions running: a manual group's claim is no
	// process and is not among them.
	Sessions int `json:"sessions"`
	// Stages are the stages that hold an open bead, summed over the rigs, in
	// the order of their flows.
	Stages []StageCount `json:"stages"`
	Rigs   []LineRig    `json:"rigs"`
}

type LineRig struct {
	Name string `json:"name"`
	Open int    `json:"open"`
}

// Waiting is one bead that waits for a person, with every reason it does.
type Waiting struct {
	Bead  string `json:"bead"`
	Title string `json:"title"`
	Rig   string `json:"rig,omitempty"`
	Stage string `json:"stage"`
	// Since is when the bead last changed: about when it began to wait.
	Since time.Time `json:"since"`
	Why   []Reason  `json:"why"`
}

// Reason is one cause a bead waits for a person.
type Reason struct {
	// Kind is held, blocked, stranded or manual. A bead whose reasons are
	// all manual is parked.
	Kind string `json:"kind"`
	// Text says it in the yard's words: "waits for decisions in decide".
	Text string `json:"text"`
	// Note is what was said with it: the reason a hold was given.
	Note string `json:"note,omitempty"`
}

// Alarm reports whether the reason is something gone wrong (a hold, a
// prompt nobody answers) and not a stage that is a person's by design.
func (r Reason) Alarm() bool { return r.Kind == "held" || r.Kind == "blocked" }

// line reads the right column. ov is the overview when the handler has read
// it already, nil otherwise.
func (s *Server) line(ctx context.Context, ov *Home) (*Line, error) {
	if ov == nil {
		var err error
		if ov, err = s.overview(ctx); err != nil {
			return nil, err
		}
	}
	l := &Line{Stages: []StageCount{}, Rigs: []LineRig{}}
	add := func(counts []StageCount) {
		for _, c := range counts {
			if c.Open == 0 {
				continue
			}
			if i := slices.IndexFunc(l.Stages, func(sc StageCount) bool { return sc.Stage == c.Stage }); i >= 0 {
				l.Stages[i].Open += c.Open
			} else {
				l.Stages = append(l.Stages, c)
			}
		}
	}
	for _, r := range ov.Rigs {
		l.Open += r.Open
		l.Working += r.Working
		l.Held += r.Held
		l.Rigs = append(l.Rigs, LineRig{Name: r.Name, Open: r.Open})
		for _, f := range r.Flows {
			add(f.Stages)
		}
		add(r.Elsewhere)
	}
	for _, sess := range ov.sessions {
		if !isManual(sess) {
			l.Sessions++
		}
	}
	var err error
	if l.Waits, l.Parked, err = s.waits(ctx, ov.sessions); err != nil {
		return nil, err
	}
	return l, nil
}

// waits lists the open beads that wait for a person: held, in a session
// that is blocked on a prompt, stranded in a stage their flow lacks, or in
// a stage routed to a manual group. Those with no reason but the last are
// parked, and listed apart: nothing went wrong with them. sessions are the
// live ones.
func (s *Server) waits(ctx context.Context, sessions []store.Session) (waits, parked []Waiting, err error) {
	beads, err := s.st.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		return nil, nil, err
	}
	groups, err := s.st.Groups(ctx)
	if err != nil {
		return nil, nil, err
	}
	manual := map[string]store.Group{}
	for _, g := range groups {
		if g.Runner == "manual" {
			manual[g.Name] = g
		}
	}
	routes, err := s.st.Routes(ctx)
	if err != nil {
		return nil, nil, err
	}
	store.SortRoutes(routes)
	// A rig whose repository cannot be read must not take the column, and
	// with it every page, down: its beads are then not known to be stranded.
	stranded, err := s.st.Stranded(ctx, "")
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, err
		}
		s.log.Error("web: stranded beads", "err", err)
	}
	rd := s.newReader(nil)
	waits, parked = []Waiting{}, []Waiting{}
	for _, b := range beads {
		var why []Reason
		if b.Hold {
			why = append(why, rd.held(ctx, b.ID))
		}
		for _, sess := range sessions {
			if sess.Bead == b.ID && rd.blocked(ctx, sess) {
				why = append(why, Reason{Kind: "blocked", Text: "its session waits on a prompt: " + sessionName(sess)})
			}
		}
		if i := slices.IndexFunc(stranded, func(sb store.StrandedBead) bool { return sb.Bead == b.ID }); i >= 0 {
			why = append(why, Reason{Kind: "stranded",
				Text: fmt.Sprintf("stranded in %s, a stage flow %s lacks", b.Stage, stranded[i].Flow)})
		}
		// The group working the bead, else the one its stage is routed to: a
		// manual group at its limit leaves beads waiting unclaimed.
		group := ""
		if b.Group != nil {
			group = *b.Group
		} else if r := store.MatchRoute(routes, b); r != nil {
			group = r.Group
		}
		if g, ok := manual[group]; ok {
			text := "waits for " + g.Name
			if len(g.Members) > 0 {
				text += " (" + strings.Join(g.Members, ", ") + ")"
			}
			why = append(why, Reason{Kind: "manual", Text: text + " in " + b.Stage})
		}
		if len(why) == 0 {
			continue
		}
		w := Waiting{Bead: b.ID, Title: b.Title, Stage: b.Stage, Since: b.UpdatedAt, Why: why}
		if b.Rig != nil {
			w.Rig = *b.Rig
		}
		if !slices.ContainsFunc(why, func(r Reason) bool { return r.Kind != "manual" }) {
			parked = append(parked, w)
			continue
		}
		waits = append(waits, w)
	}
	latest := func(a, b Waiting) int { return b.Since.Compare(a.Since) }
	slices.SortStableFunc(waits, latest)
	slices.SortStableFunc(parked, latest)
	return waits, parked, nil
}

// held says why a bead is held: who held it, and the note they left right
// before (`yardr bead note ... && yardr bead hold`), as the feed shows it.
func (r *reader) held(ctx context.Context, bead string) Reason {
	out := Reason{Kind: "held", Text: "held"}
	evs := r.eventsOf(ctx, bead)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == "held" {
			out.Text, out.Note = "held by "+evs[i].Actor, short(r.said(ctx, evs[i]))
			break
		}
	}
	return out
}

// blocked reports whether a session waits on a permission prompt or a
// question: the dispatcher's last word on it is session_blocked. The yard
// keeps that nowhere but in the bead's events.
func (r *reader) blocked(ctx context.Context, sess store.Session) bool {
	evs := r.eventsOf(ctx, sess.Bead)
	for i := len(evs) - 1; i >= 0; i-- {
		if k := evs[i].Kind; (k == "session_blocked" || k == "session_unblocked") && evs[i].Data["session"] == sess.ID {
			return k == "session_blocked"
		}
	}
	return false
}

// sessionName is what a person looks for to find a session: the agent's
// name in its runner when there is one, else the session's id.
func sessionName(sess store.Session) string {
	if agent, _ := sess.Handle["agent"].(string); agent != "" {
		return agent
	}
	return sess.ID
}

// handleLine is the right column as a page of its own, for a window with no
// room for the column.
func (s *Server) handleLine(w http.ResponseWriter, r *http.Request) {
	head, err := s.st.Head(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	l, err := s.line(r.Context(), nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respond(w, r, Page{Template: "line", Title: "Line", Menu: "line", Data: l, line: l, head: head})
}

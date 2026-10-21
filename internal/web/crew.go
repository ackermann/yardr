package web

import (
	"net/http"
	"strings"
	"time"

	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// Crew is the crew's page: every crew member of the yard, by name.
// Their journals and handoffs are their own and are not shown.
type Crew struct {
	Crew []CrewRow `json:"crew"`
	// Head is the yard's newest seq when the page was read, before its crew.
	Head uint64 `json:"head"`
}

// CrewRow is one crew member: whether it runs, since when, and how much
// of its inbox it has not read.
type CrewRow struct {
	store.CrewMember
	Name string `json:"name"`
	// State is stopped, running or failed.
	State string `json:"state"`
	// Status is what the running session is doing, as the daemon last saw
	// it: working, idle, blocked or unknown; absent when none runs.
	Status  string `json:"status,omitempty"`
	Running bool   `json:"running"`
	// Enabled is false for a crew member the daemon was told not to keep up.
	Enabled bool `json:"enabled"`
	// Since is when the running session started; absent when none runs.
	Since *time.Time `json:"since,omitempty"`
	// Up is how long that is, as the page shows it: "3h 4m".
	Up string `json:"up,omitempty"`
	// Agent is what it runs: its runner, kind and model.
	Agent string `json:"agent"`
	// Provider is the subscription it burns (providerOf); absent where
	// none is known and for a crew member of another runner.
	Provider string `json:"provider,omitempty"`
	// Unread counts the events in its inbox it has not acknowledged.
	Unread int `json:"unread"`
	// Restart is why a restart was asked for and has not happened yet.
	Restart string `json:"restart,omitempty"`
}

// Dot is the status dot's class: running, held for one that failed, none
// for one that is stopped.
func (r CrewRow) Dot() string {
	switch {
	case r.Running:
		return "running"
	case r.State == "failed":
		return "held"
	}
	return ""
}

// agentOf is the crew member's runner with the kind and model it runs, as
// `yardr crew list` shows it.
func agentOf(r store.CrewMember) string {
	parts := []string{r.Runner}
	for _, k := range []string{"kind", "model"} {
		if v, _ := r.Config[k].(string); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " · ")
}

func (s *Server) handleCrew(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head, err := s.st.Head(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rs, err := s.st.Crew(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	as, err := limits.Answerers(ctx, s.st, s.home)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := Crew{Crew: []CrewRow{}, Head: head}
	for _, res := range rs {
		row := CrewRow{CrewMember: res, Name: res.Name, State: res.State, Status: res.Status, Running: res.Live(), Enabled: res.Enabled,
			Agent: agentOf(res), Restart: res.RestartRequested}
		row.Provider = providerOf(res.Runner, res.Config, as)
		if res.Live() && res.StartedAt != nil {
			row.Since, row.Up = res.StartedAt, age(time.Since(*res.StartedAt))
		}
		// The count alone: the events are the crew member's to read.
		inbox, err := s.st.Inbox(ctx, res.Name, 1)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		row.Unread = inbox.Total
		out.Crew = append(out.Crew, row)
	}
	s.respond(w, r, Page{Template: "crew", Title: "Crew", Menu: "crew", Data: out, head: head})
}

package web

import (
	"net/http"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Residents is the residents page: every resident of the yard, by name.
// Their journals and handoffs are their own and are not shown.
type Residents struct {
	Residents []ResidentRow `json:"residents"`
}

// ResidentRow is one resident: whether it runs, since when, and how much
// of its inbox it has not read.
type ResidentRow struct {
	Name string `json:"name"`
	// State is stopped, running or failed.
	State   string `json:"state"`
	Running bool   `json:"running"`
	// Enabled is false for a resident the daemon was told not to keep up.
	Enabled bool `json:"enabled"`
	// Since is when the running session started; absent when none runs.
	Since *time.Time `json:"since,omitempty"`
	// Up is how long that is, as the page shows it: "3h 4m".
	Up string `json:"up,omitempty"`
	// Agent is what it runs: its runner, kind and model.
	Agent string `json:"agent"`
	// Unread counts the events in its inbox it has not acknowledged.
	Unread int `json:"unread"`
	// Restart is why a restart was asked for and has not happened yet.
	Restart string `json:"restart,omitempty"`
}

// Dot is the status dot's class: running, held for one that failed, none
// for one that is stopped.
func (r ResidentRow) Dot() string {
	switch {
	case r.Running:
		return "running"
	case r.State == "failed":
		return "held"
	}
	return ""
}

// agentOf is the resident's runner with the kind and model it runs, as
// `yardr resident list` shows it.
func agentOf(r store.Resident) string {
	parts := []string{r.Runner}
	for _, k := range []string{"kind", "model"} {
		if v, _ := r.Config[k].(string); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " · ")
}

func (s *Server) handleResidents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head, err := s.st.Head(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rs, err := s.st.Residents(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := Residents{Residents: []ResidentRow{}}
	for _, res := range rs {
		row := ResidentRow{Name: res.Name, State: res.State, Running: res.Live(), Enabled: res.Enabled,
			Agent: agentOf(res), Restart: res.RestartRequested}
		if res.Live() && res.StartedAt != nil {
			row.Since, row.Up = res.StartedAt, age(time.Since(*res.StartedAt))
		}
		// The count alone: the events are the resident's to read.
		inbox, err := s.st.Inbox(ctx, res.Name, 1)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		row.Unread = inbox.Total
		out.Residents = append(out.Residents, row)
	}
	s.respond(w, r, Page{Template: "residents", Title: "Residents", Menu: "residents", Data: out, head: head})
}

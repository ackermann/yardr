package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Sessions is the sessions page: the sessions running now, the oldest
// first, as `yardr session list` has them.
type Sessions struct {
	Sessions []SessionRow `json:"sessions"`
}

// SessionRow is one running session and the bead it works.
type SessionRow struct {
	ID    string `json:"id"`
	Bead  string `json:"bead"`
	Title string `json:"title,omitempty"`
	Epic  string `json:"epic,omitempty"`
	Stage string `json:"stage"`
	Group string `json:"group"`
	// Runner is the runner that started it and what it runs there: "herdr
	// yardr-68k3-new", "exec pid 4411", "manual".
	Runner string `json:"runner"`
	// State is starting or running.
	State string `json:"state"`
	// Blocked says the session waits on a permission prompt or a question.
	Blocked   bool      `json:"blocked,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// Age is how long ago it started, as the page shows it: "3h 4m".
	Age string `json:"age"`
}

// Dot is the status dot's class: held for a session that waits on a
// prompt, running for one an agent runs, none for a manual group's, which
// is a person's turn and no process.
func (r SessionRow) Dot() string {
	switch {
	case r.Blocked:
		return "held"
	case r.Runner == "manual":
		return ""
	}
	return "running"
}

// runnerOf is the runner that owns a session and what it runs there, as
// `yardr session list` shows it. A session from before sessions recorded
// their runner is known by its handle.
func runnerOf(s store.Session) string {
	runner := s.Runner
	if runner == "" && s.Handle["manual"] != nil {
		runner = "manual"
	}
	if v, ok := s.Handle["agent"]; ok {
		if runner == "" {
			runner = "herdr"
		}
		return fmt.Sprintf("%s %v", runner, v)
	}
	if v, ok := s.Handle["pid"]; ok {
		if runner == "" {
			runner = "exec"
		}
		return fmt.Sprintf("%s pid %v", runner, v)
	}
	return runner
}

// age says a duration the way a person would: its two largest units.
func age(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d/time.Second), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return strings.TrimSuffix(fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute)), " 0m")
	}
	return strings.TrimSuffix(fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour)), " 0h")
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head, err := s.st.Head(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	live, err := s.st.Sessions(ctx, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rd := s.newReader(nil)
	out := Sessions{Sessions: []SessionRow{}}
	for _, sess := range live {
		out.Sessions = append(out.Sessions, SessionRow{
			ID: sess.ID, Bead: sess.Bead, Title: rd.title(ctx, sess.Bead), Epic: sess.Epic,
			Stage: sess.Stage, Group: sess.Group, Runner: runnerOf(sess), State: sess.State,
			Blocked: rd.blocked(ctx, sess), StartedAt: sess.StartedAt, Age: age(time.Since(sess.StartedAt)),
		})
	}
	s.respond(w, r, Page{Template: "sessions", Title: "Sessions", Menu: "sessions", Data: out, head: head})
}

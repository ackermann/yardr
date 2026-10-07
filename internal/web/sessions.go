package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
)

// Sessions is the sessions page: the sessions running now, the oldest
// first, as `yardr session list` has them, and apart from them the claims
// of manual groups, which that list has too but which run nothing.
type Sessions struct {
	// Sessions are the sessions an agent or a command runs.
	Sessions []SessionRow `json:"sessions"`
	// Manual are the beads a manual group has claimed: a person's turn.
	Manual []SessionRow `json:"manual"`
	// List are the sessions as `yardr session list --json` prints them,
	// the claims of manual groups among them; with ?all=1 the ended ones
	// too, every one (`session list -a --all`). The rows above say a
	// session for the page, and name its runner and state as the page does.
	List []dispatch.ListedSession `json:"list"`
	// Head is the yard's newest seq when the page was read, before its
	// sessions: the stream after it brings what happened since.
	Head uint64 `json:"head"`
}

// SessionRow is one running session, or one manual group's claim, and the
// bead it works.
type SessionRow struct {
	ID    string `json:"id"`
	Bead  string `json:"bead"`
	Title string `json:"title,omitempty"`
	Train string `json:"train,omitempty"`
	Stage string `json:"stage"`
	Group string `json:"group"`
	// Runner is the runner that started it and what it runs there: "herdr
	// yardr-68k3-new", "exec pid 4411", "manual".
	Runner string `json:"runner"`
	// State is starting or running; claimed for a manual group's claim,
	// which the store keeps as a running session.
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

// isManual reports whether a session is a manual group's claim: nothing
// runs in it, a person works the bead.
func isManual(s store.Session) bool { return s.Manual() }

// runnerOf is the runner that owns a session and what it runs there, as
// `yardr session list` shows it. A session from before sessions recorded
// their runner is known by its handle.
func runnerOf(s store.Session) string {
	runner := s.Runner
	if s.Manual() {
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
	out := Sessions{Sessions: []SessionRow{}, Manual: []SessionRow{}, Head: head}
	for _, sess := range live {
		row := SessionRow{
			ID: sess.ID, Bead: sess.Bead, Title: rd.title(ctx, sess.Bead), Train: sess.Train,
			Stage: sess.Stage, Group: sess.Group, Runner: runnerOf(sess), State: sess.State,
			StartedAt: sess.StartedAt, Age: age(time.Since(sess.StartedAt)),
		}
		if isManual(sess) {
			row.State = "claimed"
			out.Manual = append(out.Manual, row)
			continue
		}
		row.Blocked = rd.blocked(ctx, sess)
		out.Sessions = append(out.Sessions, row)
	}
	// The list command's own read, so the list is in its order.
	listed, err := s.st.ListSessions(ctx, store.SessionFilter{All: all(r)})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	gates, err := rigcheck.Gates(s.home)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out.List = dispatch.Listed(listed, gates)
	s.respond(w, r, Page{Template: "sessions", Title: "Sessions", Menu: "sessions", Data: out, head: head})
}

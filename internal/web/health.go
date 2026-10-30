package web

import (
	"context"
	"net/http"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// Health is GET /health: the yard's liveness facts for a monitor, a
// proxy or footplate — the same facts yardr upgrade --check verifies, minus
// the verdict. It answers 200 whenever the view serves it: status says
// whether the yard is whole ("ok") or answers with something missing
// ("degraded"), so a proxy keeps routing to a view that serves reads over a
// yard whose daemon is down. Only a yard it cannot read at all fails,
// through the error path. Nothing it says is a secret: builds, a pass time
// and booleans. The store's head sequence is deliberately not among them.
type Health struct {
	Status string       `json:"status"`
	Server HealthServer `json:"server"`
	Daemon HealthDaemon `json:"daemon"`
	View   HealthView   `json:"view"`
}

type HealthServer struct {
	Build string     `json:"build,omitempty"`
	Since *time.Time `json:"since,omitempty"`
}

type HealthDaemon struct {
	Running bool `json:"running"`
	// Build is the build of the daemon that last started or replaced,
	// absent when no start was ever recorded.
	Build string `json:"build,omitempty"`
	// LastPass is when the daemon last completed a pass, absent when it
	// never stamped one.
	LastPass *time.Time `json:"last_pass,omitempty"`
}

type HealthView struct {
	Build string `json:"build"`
}

// describeServer is the server probe /health reads; tests replace it.
var describeServer = server.Describe

// handleHealth answers GET /health directly as JSON, with no template and
// no rail entry. It reads nothing but what it reports: the server through
// the probe, the daemon's lock and pass stamp from the home, the daemon's
// build from its start events, and the view's own build.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	home := config.At(s.home)
	data := Health{Status: "ok", View: HealthView{Build: version.Current()}}
	whole := true
	if info, answers := describeServer(ctx, home); answers {
		data.Server.Build = info.Revision
		if !info.Since.IsZero() {
			since := info.Since
			data.Server.Since = &since
		}
	} else {
		whole = false
	}
	if data.Daemon.Running = config.DaemonRunning(home.DaemonLock()); !data.Daemon.Running {
		whole = false
	}
	build, err := daemonBuild(ctx, s.st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Daemon.Build = build; build == "" {
		whole = false
	}
	if stamp, err := config.ReadPassStamp(s.home); err == nil {
		at := stamp.At
		data.Daemon.LastPass = &at
	} else {
		whole = false
	}
	if !whole {
		data.Status = "degraded"
	}
	// The yard moves under the answer; a stored copy is a stale one.
	w.Header().Set("Cache-Control", "no-store")
	s.data(w, r, http.StatusOK, data)
}

// daemonBuild is the build the daemon runs: the to of the latest
// daemon_replaced or daemon_started event, "" when neither was ever logged.
// A daemon started fresh before daemon_started existed, and never replaced
// since, has no build on record; it needs a restart, not a guess.
func daemonBuild(ctx context.Context, st store.API) (string, error) {
	var latest *store.Event
	for _, kind := range []string{"daemon_replaced", "daemon_started"} {
		evs, err := st.EventsOfKind(ctx, kind, 1)
		if err != nil {
			return "", err
		}
		if len(evs) > 0 && (latest == nil || evs[0].Seq > latest.Seq) {
			latest = &evs[0]
		}
	}
	if latest == nil {
		return "", nil
	}
	build, _ := latest.Data["to"].(string)
	return build, nil
}

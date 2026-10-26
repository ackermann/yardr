package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// KeepEdge keeps a link of role to the configured edge and serves h over it.
// It follows the edge setting without a restart and loads the yard's key
// from PeerKey. An empty role keeps the default yard link and its edge.state;
// the web role reports events only, since that file describes the server.
// The function returned closes the link and waits for it.
func KeepEdge(ctx context.Context, c *config.Config, st store.API, role string, h http.Handler, log *slog.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var (
			current  string
			stopLink = func() {}
		)
		apply := func() {
			all, err := st.Settings(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("edge: the edge setting is not read", "err", err)
				}
				return
			}
			v := all[store.EdgeSetting]
			if v == current {
				return
			}
			stopLink()
			stopLink, current = func() {}, v
			if role == "" {
				os.Remove(c.EdgeState())
			}
			if v == "" {
				return
			}
			e, err := store.ParseEdge(v)
			if err != nil {
				log.Warn("edge: the edge setting is not one this build reads; no link", "err", err)
				return
			}
			key, err := yardkey.Load(c.PeerKey())
			if err != nil {
				log.Warn("edge: the yard's key is not read; no link", "err", err)
				return
			}
			stopLink = startLink(ctx, c, st, &edgelink.Link{Edge: e, Key: key, Role: role, Handler: h, Log: log})
		}
		// The stream ends whenever the server does, and a server that is
		// replacing itself does not answer for a moment: subscribe again
		// from the last event seen, asking again every second until it
		// answers, as the dispatcher's wake stream and the quiet run's
		// trigger do. A web view lives across every server replace, so
		// without this its link keeps whatever edge it had until the
		// view is restarted.
		var after uint64
		have, told := false, false
		// pause says why the server could not be asked, once for each
		// time it could not, and waits to ask again.
		pause := func(what string, err error) {
			if !told {
				told = true
				log.Warn("edge: cannot "+what+"; asking again", "err", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
		for ctx.Err() == nil {
			if !have {
				head, err := st.Head(ctx)
				if err != nil {
					pause("read the event log", err)
					continue
				}
				after, have = head, true
			}
			events, err := st.Subscribe(ctx, after)
			if err != nil {
				pause("watch the yard's events", err)
				continue
			}
			told = false
			// Subscribed first, so nothing committed while applying is
			// missed: it is on the stream behind this pass.
			apply()
			for e := range events {
				after = e.Seq
				if e.Kind == "setting_changed" && e.Data["key"] == store.EdgeSetting {
					apply()
				}
			}
		}
		stopLink()
		if role == "" {
			os.Remove(c.EdgeState())
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// linkTest, when set, changes a link before it runs: a test's client that
// trusts its stand-in edge, and the timing shortened.
var linkTest func(*edgelink.Link)

// startLink runs l until the function it returns is called, and tells each
// change of it.
func startLink(ctx context.Context, c *config.Config, st store.API, l *edgelink.Link) (stop func()) {
	if linkTest != nil {
		linkTest(l)
	}
	l.Told = func(s edgelink.State) {
		role := l.Role
		if role == "" {
			role = "yard"
			if err := edgelink.WriteState(c, s); err != nil {
				l.Log.Warn("edge: the link's state is not written", "err", err)
			}
		}
		kind, data := "edge_link_up", map[string]any{"url": s.URL, "handle": s.Handle, "role": role}
		if !s.Up {
			kind, data["reason"], data["detail"] = "edge_link_down", s.Reason, s.Detail
		}
		// The link's own context may be the one ending: the event is the
		// yard's all the same, and the store is open until KeepEdge returns.
		if err := st.LogDispatch(context.Background(), "", kind, "yard", data); err != nil {
			l.Log.Warn(kind+" event", "err", err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// withoutPeers is h less /peers/: a linked yard's requests are not carried
// through the edge (design yardr-hosted-050n, section 4), whose operator
// would then read the peers' mail.
func withoutPeers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/peers" || strings.HasPrefix(r.URL.Path, "/peers/") {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

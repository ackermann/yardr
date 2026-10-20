package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// keepEdge keeps the yard's link to its edge (design yardr-hosted-050n, 3.2)
// while the edge setting names one: it reads the setting now and again at
// each change of it, so yardr edge connect has the running server dial and
// yardr edge disconnect has it close the link, with no restart. What comes
// down the link is served by h, the listener's own mux, less /peers/.
//
// The link is told as events (edge_link_up, edge_link_down with its reason)
// and in the home's edge.state, which yardr status and yardr doctor read.
// The function returned closes the link and waits for it.
func keepEdge(ctx context.Context, c *config.Config, st *store.Store, h http.Handler, log *slog.Logger) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	served := withoutPeers(h)
	go func() {
		defer close(done)
		var events <-chan store.Event
		head, err := st.Head(ctx)
		if err == nil {
			events, err = st.Subscribe(ctx, head)
		}
		if err != nil && ctx.Err() == nil {
			log.Warn("edge: not subscribed to the yard's settings; the edge setting is read at the server's next start", "err", err)
		}
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
			os.Remove(c.EdgeState())
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
			stopLink = startLink(ctx, c, st, &edgelink.Link{Edge: e, Key: key, Handler: served, Log: log})
		}
		apply()
		for {
			select {
			case <-ctx.Done():
				stopLink()
				os.Remove(c.EdgeState())
				return
			case ev, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if ev.Kind == "setting_changed" && ev.Data["key"] == store.EdgeSetting {
					apply()
				}
			}
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
func startLink(ctx context.Context, c *config.Config, st *store.Store, l *edgelink.Link) (stop func()) {
	if linkTest != nil {
		linkTest(l)
	}
	l.Told = func(s edgelink.State) {
		if err := edgelink.WriteState(c, s); err != nil {
			l.Log.Warn("edge: the link's state is not written", "err", err)
		}
		kind, data := "edge_link_up", map[string]any{"url": s.URL, "handle": s.Handle}
		if !s.Up {
			kind, data["reason"], data["detail"] = "edge_link_down", s.Reason, s.Detail
		}
		// The link's own context may be the one ending: the event is the
		// yard's all the same, and the store is open until keepEdge returns.
		if err := st.LogEvent(context.Background(), "", kind, "yard", data); err != nil {
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

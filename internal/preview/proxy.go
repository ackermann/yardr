package preview

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Handler is the supervisor's yard-side endpoint: the edge reaches a
// preview through it, never directly. It serves /preview/<app>/... —
// the path past the app is forwarded whole, so an ordinary web app
// written for / runs unmodified — checks the app is running (starting
// an approved, stopped one on demand), counts the preview's requests
// in flight, records the request's moment, and streams the answer
// back, WebSockets included. No edge credential is forwarded: the
// router strips its own names, and this drops the same names itself,
// so the yard-side rule does not depend on the router having stripped
// first.
func (s *Supervisor) Handler() http.Handler {
	return http.HandlerFunc(s.servePreview)
}

// edgeHeaders are the names a request through the edge carries that must
// never reach the app.
var edgeHeaders = []string{"X-Edge-Token", "X-Edge-JWT", "Edge-Yard", "X-Edge-Refusal"}

var edgeCookies = []string{"ory_kratos_session", "edge_yard", "__Host-edge_preview"}

func (s *Supervisor) servePreview(w http.ResponseWriter, r *http.Request) {
	app, rest, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/preview/"), "/")
	if !ok {
		rest = ""
	}
	if app == "" || store.CheckPreviewApp(app) != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	p, err := s.Store.GetPreview(ctx, app)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch p.State {
	case store.PreviewPending, store.PreviewRefused:
		http.NotFound(w, r)
		return
	case store.PreviewCrashed, store.PreviewFailed:
		http.Error(w, fmt.Sprintf("preview %s is %s", app, p.State), http.StatusServiceUnavailable)
		return
	}
	if !s.Running(app) {
		// On-demand start of an approved, stopped preview: its person has
		// arrived, so start it and wait for its port.
		if p.Approval == nil {
			http.NotFound(w, r)
			return
		}
		if p.State == store.PreviewStopped || p.State == store.PreviewApproved {
			d := ""
			_, _ = s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{State: store.PreviewApproved, StateDetail: &d})
		}
		if err := s.Start(ctx, app); err != nil {
			http.Error(w, "preview "+app+": "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		if !s.waitPort(ctx, p.Port) {
			http.Error(w, "preview "+app+" is starting", http.StatusServiceUnavailable)
			return
		}
	}
	if !s.enter(app) {
		// The 21st request in flight waits two seconds, then is
		// answered with the supervisor's own 503.
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		if !s.enter(app) {
			http.Error(w, "preview "+app+": too many requests in flight", http.StatusServiceUnavailable)
			return
		}
	}
	defer s.leave(app)
	now := time.Now()
	_, _ = s.Store.UpdatePreview(ctx, app, store.PreviewUpdate{LastRequest: &now})

	host := loopbackHost(p.Port)
	if host == "" {
		http.Error(w, "preview "+app+" is not answering", http.StatusBadGateway)
		return
	}
	target, _ := url.Parse("http://" + net.JoinHostPort(host, strconv.Itoa(p.Port)))
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + rest
			stripEdgeCredentials(pr.Out.Header)
			pr.Out.Header.Set("X-Preview-App", app)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "preview "+app+" is not answering", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// waitPort waits until the preview's port answers or the start wait
// runs out.
func (s *Supervisor) waitPort(ctx context.Context, port int) bool {
	deadline := time.Now().Add(defaultStartWait)
	if s.BindWait > 0 {
		deadline = time.Now().Add(s.BindWait + defaultStartWait)
	}
	for time.Now().Before(deadline) {
		if loopbackHost(port) != "" {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(25 * time.Millisecond):
		}
	}
	return false
}

// enter counts one more request in flight for app, refusing past the
// cap.
func (s *Supervisor) enter(app string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[app] >= store.PreviewMaxInFlight {
		return false
	}
	s.inflight[app]++
	return true
}

func (s *Supervisor) leave(app string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[app] > 0 {
		s.inflight[app]--
	}
}

// stripEdgeCredentials removes edge headers and cookies from a request
// before forwarding it, keeping the app's own cookies.
func stripEdgeCredentials(h http.Header) {
	for _, name := range edgeHeaders {
		h.Del(name)
	}
	if c := stripEdgeCookies(h.Get("Cookie")); c != "" {
		h.Set("Cookie", c)
	} else {
		h.Del("Cookie")
	}
}

func stripEdgeCookies(v string) string {
	var kept []string
	for _, part := range strings.Split(v, ";") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if !slices.Contains(edgeCookies, name) {
			kept = append(kept, strings.TrimSpace(part))
		}
	}
	return strings.Join(kept, "; ")
}

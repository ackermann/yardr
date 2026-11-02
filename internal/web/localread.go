package web

// The local-read listener is the view's second door, for a process on the
// yard's own machine that reads the view's JSON (a dashboard's server-side
// fetch, a script). In JWT mode the view's own door asks every request but
// /health for the gateway's token, which such a reader holds none of; a
// token minted for it would be a secret to store, rotate and leak, for a
// reader that is inside the machine's trust already. So the same server
// answers on a second loopback address instead, with no token and no
// cookie: the trust is the machine's, as the view's own local reading was
// before JWT mode (docs/web.md: the view has no login, and who may read the
// yard locally is the machine's business).
//
// It is a reader's door and no more. It serves the routes below as JSON
// and the stream, to GET and HEAD; it serves no page, no form and no blob,
// and its reader acts as nobody, as a visitor to a view that acts as no one
// does.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// localReadRoutes registers the routes the local-read listener serves, by
// the view's own handlers: the lists and the bead a reader draws the yard
// from, and the stream that follows it. A route of the view is not served
// here until it is named here, so a page added to the view is not one a
// reader with no token can reach. What is left out is left out on purpose:
// pages, forms and blobs are for a browser; /pairing and the sign-in
// returns are a signed-in person's; /limits runs the operator's commands;
// and /peers, /config, /previews and /telemetry are for the operator.
func (s *Server) localReadRoutes() {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /depots", s.handleDepots},
		{"GET /flows", s.handleFlows},
		{"GET /groups", s.handleGroups},
		{"GET /routes", s.handleRoutes},
		{"GET /crew", s.handleCrew},
		{"GET /beads", s.handleBeads},
		{"GET /beads/{id}", s.handleBead},
		{"GET /sessions", s.handleSessions},
		{"GET /deps", s.handleDeps},
		{"GET /events", s.handleLocalEvents},
	} {
		s.localMux.HandleFunc(route.pattern, asJSON(route.handler))
	}
	// Every other path says what the listener is, in the JSON a reader
	// parses, and not in the mux's plain 404.
	s.localMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.data(w, r, http.StatusNotFound, errorData{Error: "the local-read listener serves the view's read-only JSON and its stream, and no route at " + r.URL.Path})
	})
}

// LocalReadHandler serves the local-read listener. It is fenced as the view
// and PagesHandler are by the Host rules, with the same 421s, and by the
// address it listens on, which the caller keeps on loopback under every flag
// (Listen with unsafe off). It asks for no gateway token even in JWT mode,
// and not for the cookie of a view that acts, and so it acts as no one.
// Anything but GET and HEAD is 405, whatever the path, and a path it does
// not serve is 404.
func (s *Server) LocalReadHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.knownHost(r.Host) {
			s.misdirected(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "the local-read listener takes GET and HEAD only", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", s.policy(r))
		h.Set("X-Content-Type-Options", "nosniff")
		s.localMux.ServeHTTP(w, r)
	})
}

// ListenLocalRead opens addr for the local-read listener. It listens on
// loopback under every flag: the view's own address may be widened by
// --unsafe, but this one asks for no token, so nothing but the machine guards
// it, and an address off loopback is refused, as is a name that turns out to
// listen elsewhere.
func ListenLocalRead(addr string) (net.Listener, error) {
	if err := CheckLocalReadAddr(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	// localhost is a name, and a hosts file may point it anywhere.
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, localReadRefused(addr, fmt.Sprintf("it listens on %s", ln.Addr()))
	}
	return ln, nil
}

// CheckLocalReadAddr refuses an address for the local-read listener that is
// not on the loopback interface. It has no unsafe to say otherwise (Listen's
// refusal ends in "unless --unsafe is given", which would be wrong here).
func CheckLocalReadAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--local-read %s: %w", addr, err)
	}
	switch {
	case host == "":
		return localReadRefused(addr, "an address with no host listens on every interface")
	case strings.EqualFold(host, "localhost"):
		// The name may be redirected by the hosts file or resolver. Check
		// its answers before the store is opened; ListenLocalRead checks the
		// address it actually bound too.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || !localReadIPsAreLoopback(ips) {
			return localReadRefused(addr, "localhost does not resolve only to loopback addresses")
		}
	case !loopbackIP(host):
		return localReadRefused(addr, host+" is not a loopback address")
	}
	return nil
}

func localReadIPsAreLoopback(ips []net.IPAddr) bool {
	if len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !ip.IP.IsLoopback() {
			return false
		}
	}
	return true
}

func localReadRefused(addr, why string) error {
	return fmt.Errorf("--local-read %s refused: %s, and the local-read listener asks for no token, so whoever reaches it reads the yard. "+
		"It listens on loopback only (127.0.0.1, ::1 or localhost), whatever --unsafe says of the view", addr, why)
}

// asJSON is h asked for JSON whatever the request's Accept says. Every
// handler here ends in respond, which renders a page for a client that does
// not prefer JSON, and this listener serves no page.
func asJSON(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("Accept", "application/json")
		h(w, r)
	}
}

// handleLocalEvents is the stream for a local reader: the events as JSON,
// as the view sends them to a client that asks for json. The other formats
// are HTML fragments for the view's own pages, which this listener has none
// of.
func (s *Server) handleLocalEvents(w http.ResponseWriter, r *http.Request) {
	if f := r.URL.Query().Get("format"); f != "" && f != "json" {
		http.Error(w, "the local-read listener streams JSON: format is json, not "+f, http.StatusBadRequest)
		return
	}
	s.handleEvents(w, r)
}

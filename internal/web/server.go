// Package web serves a read-only view of the yard for a browser: HTML
// rendered here with html/template, and the same data as JSON to whoever
// asks for it. It is a client of the store like any other command and
// changes nothing.
//
// A page is one template in templates/ and one handler that builds the
// page's data once and hands it to respond; docs/web.md says how to add one.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"yardr.dev/yardr/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

// menu is the left menu, in order. The line entry is the phone's way to
// the right column, which has no room there; the stylesheet shows it only
// where that column is hidden.
var menu = []menuItem{
	{Name: "home", Label: "Home", Path: "/"},
	{Name: "yard", Label: "Yard", Path: "/flows?panel=1"},
	{Name: "flows", Label: "Flows", Path: "/flows"},
	{Name: "beads", Label: "Beads", Path: "/beads"},
	{Name: "sessions", Label: "Sessions", Path: "/sessions"},
	{Name: "crew", Label: "Crew", Path: "/crew"},
	{Name: "config", Label: "Config", Path: "/config"},
	{Name: "line", Label: "Line", Path: "/line", Aside: true},
}

type menuItem struct {
	Name, Label, Path string
	// Aside marks the entry that stands in for the right column.
	Aside bool
}

// unbuilt are the menu's pages no child of the train has built yet: each
// answers with the "not built yet" page. A child that builds one removes it
// here and registers its handler in routes. Every page of the menu is
// built now; a new entry starts here.
var unbuilt = []string{}

// Server is the web view of one yard.
type Server struct {
	st store.API
	// home is the yard's home directory, whose name is the yard's when the
	// `name` setting is unset.
	home  string
	log   *slog.Logger
	mux   *http.ServeMux
	pages map[string]*template.Template
	// partials are the templates that are part of a page and no page
	// themselves; the stream renders one alone.
	partials *template.Template
	assets   map[string]asset
	// streams counts the open /events streams.
	streams     atomic.Int64
	streamWrite time.Duration
	// streamMu guards streamsBy, the open streams per address (streamKey),
	// and streamsOff, those of them from off loopback; streamMax and
	// streamPerAddr are what enter lets them come to.
	streamMu      sync.Mutex
	streamsBy     map[string]int
	streamsOff    int
	streamMax     int
	streamPerAddr int
	// network is set when the view is served off loopback (--unsafe): a
	// request may then name any IP in its Host header, or one of names,
	// this machine's host names in lower case: its own, and the ones its
	// operator gave (--host).
	network bool
	names   []string
	// limitsMu guards limitsBy, the last answer per harness kind and the
	// run on its way, if one is; limitsEvery is how long one ask stands.
	limitsMu    sync.Mutex
	limitsBy    map[string]*limitsEntry
	limitsEvery time.Duration
}

// NewServer serves the yard behind st, whose home directory is home.
func NewServer(st store.API, home string, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{st: st, home: home, log: log, mux: http.NewServeMux(), streamWrite: streamWrite,
		streamsBy: map[string]int{}, streamMax: streamMax, streamPerAddr: streamPerAddr,
		limitsBy: map[string]*limitsEntry{}, limitsEvery: defaultLimitsEvery}
	var err error
	if s.assets, err = loadAssets(); err != nil {
		return nil, err
	}
	if s.pages, err = s.loadPages(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

// partial are the files of templates/ that define a part of a page, not a
// page: every page is parsed with them.
var partial = []string{"shell", "aside", "entry", "card"}

// loadPages parses every template of templates/ with the shell, one set per
// page: each page defines "content", so two pages cannot share a set.
func (s *Server) loadPages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{"asset": s.assetURL, "clock": clock}
	var parts []string
	for _, name := range partial {
		parts = append(parts, "templates/"+name+".html")
	}
	shell, err := template.New("shell.html").Funcs(funcs).ParseFS(templateFS, parts...)
	if err != nil {
		return nil, err
	}
	if s.partials, err = shell.Clone(); err != nil {
		return nil, err
	}
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		if slices.Contains(partial, name) {
			continue
		}
		t, err := shell.Clone()
		if err != nil {
			return nil, err
		}
		if pages[name], err = t.ParseFS(templateFS, f); err != nil {
			return nil, err
		}
	}
	return pages, nil
}

// routes registers every page. Patterns name GET, so the mux refuses every
// other method itself: the view changes nothing.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /line", s.handleLine)
	s.mux.HandleFunc("GET /events", s.handleEvents)
	s.mux.HandleFunc("GET /sessions", s.handleSessions)
	s.mux.HandleFunc("GET /crew", s.handleCrew)
	s.mux.HandleFunc("GET /flows", s.handleFlows)
	s.mux.HandleFunc("GET /beads", s.handleBeads)
	s.mux.HandleFunc("GET /beads/{id}", s.handleBead)
	// The configuration lists (lists.go): three have a page under the
	// Config entry, with /config as their hub.
	s.mux.HandleFunc("GET /config", s.handleConfig)
	s.mux.HandleFunc("GET /depots", s.handleDepots)
	s.mux.HandleFunc("GET /groups", s.handleGroups)
	s.mux.HandleFunc("GET /routes", s.handleRoutes)
	// The lists that have no page (lists.go, limits.go).
	s.mux.HandleFunc("GET /peers", s.handlePeers)
	s.mux.HandleFunc("GET /deps", s.handleDeps)
	s.mux.HandleFunc("GET /limits", s.handleLimits)
	for _, name := range unbuilt {
		s.mux.HandleFunc("GET /"+name, s.handleUnbuilt(name))
	}
	s.mux.HandleFunc("GET /assets/{file}", s.handleAsset)
	// A browser asks for one unbidden; the view has no image, and an empty
	// answer keeps its console clear of a 404.
	s.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusNoContent)
	})
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		s.respond(w, r, Page{Status: http.StatusNotFound, Template: "error", Title: "Not found",
			Data: errorData{Error: "no page at " + r.URL.Path}})
	})
}

// policy lets the browser load and reach this server only, so a page that
// names another host by mistake fails where it is seen instead of calling
// out. The page has no inline script or style, and nothing may frame it.
const policy = "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A page on another site can make the browser ask this server under a
	// name of its own that resolves to this machine (DNS rebinding) and
	// read the answer, since there is no authentication. The Host header
	// then carries that name, so a name that is not this machine's is
	// refused.
	if !s.knownHost(r.Host) {
		as, hint := "localhost or 127.0.0.1", ""
		if s.network {
			as = strings.Join(append([]string{"an IP address"}, s.names...), " or ")
			// The name may be this machine's all the same (a tailnet's, the
			// LAN's DNS, a proxy's): only its operator can say so. What is
			// no host name is not offered back as one.
			if name, err := CheckHost(hostOf(r.Host)); err == nil {
				hint = ". If that name is this machine's, start the view with --host " + name
			}
		}
		http.Error(w, "yardr web does not answer to another host's name: ask for it as "+as+", not "+r.Host+hint,
			http.StatusMisdirectedRequest)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", policy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

// Page is what a handler answers with: Data is the page's whole content,
// sent as it is to a client that asks for JSON and rendered by Template
// inside the shell for everyone else.
type Page struct {
	// Status defaults to 200.
	Status int
	// Template is the page's file in templates/, without .html.
	Template string
	// Title is the browser's title for the page; the yard's name follows it.
	Title string
	// Menu is the name of the menu entry the page belongs to, "" for none.
	Menu string
	Data any
	// line is the shell's line, for a handler that has read the yard
	// already; respond reads it otherwise.
	line *Line
	// yard is the yard's open work, for a handler that has read it and
	// not the line: respond makes the line from it instead of reading the
	// beads again.
	yard *yard
	// head is the yard's newest seq from before the handler read the yard,
	// for a handler that read it; respond reads it otherwise.
	head uint64
	// stream is the ?format= of the page's stream, for a page that hangs
	// more on it than the shell does: card on /flows, seq on a page that
	// only reads itself again, home on / for the entries its feed keeps.
	// "" is the feed's entries.
	stream string
	// Wide is a page with no right column: its centre runs to the
	// window's edge. The JSON never carries it.
	Wide bool
}

// errorData is the Data of every error page, so an error reads the same in
// JSON whichever handler met it.
type errorData struct {
	Error string `json:"error"`
}

// view is what the shell's templates see: the page, and what the shell
// itself shows around it.
type view struct {
	Page
	Yard    string
	Entries []menuItem
	Aside   *Line
	// Head is the seq the page's stream starts after. It is read before
	// what the page shows, so the stream brings everything the page lacks.
	Head uint64
	// Stream is the stream's ?format=, "" for the feed's entries.
	Stream string
}

// respond answers r with p, as JSON or as HTML (see wantsJSON). Every
// handler ends here, so the two never show different data: the handler
// builds p.Data once and does not know which was asked for.
func (s *Server) respond(w http.ResponseWriter, r *http.Request, p Page) {
	if p.Status == 0 {
		p.Status = http.StatusOK
	}
	// The answer depends on Accept: a cache must not hand one for the other.
	w.Header().Set("Vary", "Accept")
	// The yard moves under the page; a stored copy is a stale one.
	w.Header().Set("Cache-Control", "no-store")
	if wantsJSON(r.Header.Values("Accept")) {
		s.data(w, r, p.Status, p.Data)
		return
	}
	t := s.pages[p.Template]
	if t == nil {
		s.fail(w, r, fmt.Errorf("no template %q", p.Template))
		return
	}
	// The shell shows the yard's name and the line on every page.
	v := view{Page: p, Entries: menu, Yard: s.yardName(r.Context()), Aside: p.line, Head: p.head, Stream: p.stream}
	if v.Aside == nil {
		var err error
		// A handler's own head stands: it is from before the handler read
		// the yard, and one read here would skip what happened since.
		if v.Head == 0 {
			if v.Head, err = s.st.Head(r.Context()); err != nil {
				s.fail(w, r, err)
				return
			}
		}
		ov, err := s.overview(r.Context(), p.yard)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if v.Aside, err = s.line(r.Context(), ov); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	// Rendered whole before the first byte: a template that fails halfway
	// must not leave half a page under a 200.
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "shell.html", v); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(p.Status)
	_, _ = w.Write(buf.Bytes())
}

// data answers r with v as JSON, as the commands print it with --json.
func (s *Server) data(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// fail answers a handler's error: 404 for what the store does not have,
// 500 for everything else, on the error page. A request that ended because
// the client left is not reported at all.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	status := http.StatusInternalServerError
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	} else {
		s.log.Error("web: "+r.URL.Path, "err", err)
	}
	p := Page{Status: status, Template: "error", Title: http.StatusText(status), Data: errorData{Error: err.Error()}}
	if s.pages[p.Template] == nil || wantsJSON(r.Header.Values("Accept")) {
		// The error page itself is what is missing or what failed: plain
		// JSON cannot fail the same way.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(p.Data)
		return
	}
	var buf bytes.Buffer
	if terr := s.pages[p.Template].ExecuteTemplate(&buf, "shell.html", view{Page: p, Entries: menu, Yard: s.yardName(r.Context())}); terr != nil {
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// yardName is the yard's name as its crew carry it in herdr: the
// `name` setting, else the home's directory name (~/.yardr reads as yardr).
func (s *Server) yardName(ctx context.Context) string {
	if all, err := s.st.Settings(ctx); err == nil && all["name"] != "" {
		return all["name"]
	}
	return strings.TrimLeft(filepath.Base(s.home), ".")
}

// Listen opens addr for the web view, which has no authentication yet and
// so listens on loopback only: any other address is refused, and so is a
// name that turns out to listen elsewhere. With unsafe every address is
// opened, the ones that listen on every interface too.
func Listen(addr string, unsafe bool) (net.Listener, error) {
	if err := CheckAddr(addr, unsafe); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	// localhost is a name, and a hosts file may point it anywhere.
	if tcp, ok := ln.Addr().(*net.TCPAddr); !unsafe && (!ok || !tcp.IP.IsLoopback()) {
		_ = ln.Close()
		return nil, refused(addr, fmt.Sprintf("it listens on %s", ln.Addr()))
	}
	return ln, nil
}

// CheckAddr refuses a listen address that is not on the loopback
// interface, saying why. With unsafe it refuses only what is no address.
func CheckAddr(addr string, unsafe bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--addr %s: %w", addr, err)
	}
	if unsafe {
		return nil
	}
	switch ip := net.ParseIP(host); {
	case host == "":
		return refused(addr, "an address with no host listens on every interface")
	case ip == nil && !strings.EqualFold(host, "localhost"):
		return refused(addr, host+" is not a loopback address")
	case ip != nil && !ip.IsLoopback():
		return refused(addr, host+" is not a loopback address")
	}
	return nil
}

func refused(addr, why string) error {
	return fmt.Errorf("--addr %s refused: %s, and yardr web has no authentication yet, so whoever reaches it reads the whole yard. "+
		"It listens on loopback only (127.0.0.1, ::1 or localhost) unless --unsafe is given", addr, why)
}

// AllowNetwork is --unsafe for the requests: the view is served off
// loopback, so a request may name the machine by any IP or by hostname,
// its host name, with or without .local. Every other name is still
// refused: one that resolves here is what DNS rebinding looks like, and an
// IP literal cannot be rebound. hosts are the names the operator says are
// this machine's too (--host, each as CheckHost returns it): one the
// machine cannot work out by itself, as a tailnet's or a proxy's. Call it
// before the server serves.
func (s *Server) AllowNetwork(hostname string, hosts ...string) {
	s.network = true
	s.names = nil
	if name := strings.TrimSuffix(strings.ToLower(hostname), ".local"); name != "" {
		s.names = []string{name, name + ".local"}
	}
	for _, h := range hosts {
		if h = strings.ToLower(h); h != "" && !slices.Contains(s.names, h) {
			s.names = append(s.names, h)
		}
	}
}

// Names are the host names a request may name the machine by when the view
// is served off loopback, besides any IP address and the loopback names.
func (s *Server) Names() []string { return slices.Clone(s.names) }

// CheckHost returns name as a request's Host header is compared with it,
// in lower case and without the dot that ends a full name, or says why it
// is no host name. A wildcard is none: a view that answers to every name
// has no guard against DNS rebinding left.
func CheckHost(name string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(name), ".")
	switch {
	case host == "":
		return "", fmt.Errorf("--host %q refused: it is no name", name)
	case strings.Contains(host, "*"):
		return "", fmt.Errorf("--host %s refused: a wildcard would answer to any site's name, and yardr web has no authentication yet. Name each host", name)
	case strings.Contains(host, ":"):
		return "", fmt.Errorf("--host %s refused: give the name alone, without a scheme or a port (an IP address is answered already)", name)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			return "", fmt.Errorf("--host %s refused: it is no host name (letters, digits, - and _, in parts joined by dots)", name)
		}
	}
	return host, nil
}

// knownHost reports whether a request's Host header names this machine's
// loopback interface or, served off loopback, the machine itself: an IP
// literal or one of its host names.
func (s *Server) knownHost(hostport string) bool {
	host := hostOf(hostport)
	if ip := net.ParseIP(host); ip != nil {
		return s.network || ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost") || s.network && slices.Contains(s.names, strings.ToLower(host))
}

// hostOf is the host of a Host header, without its port and without the
// brackets of an IPv6 literal.
func hostOf(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

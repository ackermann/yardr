// Package web serves a view of the yard for a browser: HTML rendered here
// with html/template, and the same data as JSON to whoever asks for it.
// Without --as it only reads the yard; with --as <user> the talk page
// sends mail as that user, and a bead's page answers its asks and sends
// the bead on, and nothing else takes a write.
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
// where that column is hidden. Phone marks the entries the phone's bottom
// bar shows; the rest open under its More entry, in the same order.
var menu = []menuItem{
	{Name: "home", Label: "Home", Path: "/", Phone: true},
	{Name: "talk", Label: "Talk", Path: "/talk", Phone: true},
	{Name: "yard", Label: "Yard", Path: "/flows?panel=1"},
	{Name: "flows", Label: "Flows", Path: "/flows"},
	{Name: "beads", Label: "Beads", Path: "/beads", Phone: true},
	{Name: "sessions", Label: "Sessions", Path: "/sessions"},
	{Name: "crew", Label: "Crew", Path: "/crew"},
	{Name: "config", Label: "Config", Path: "/config"},
	{Name: "line", Label: "Line", Path: "/line", Aside: true, Phone: true},
}

type menuItem struct {
	Name, Label, Path string
	// Aside marks the entry that stands in for the right column.
	Aside bool
	// Phone marks an entry the phone's bottom bar shows itself; the
	// rest open under its More entry.
	Phone bool
}

// moreCurrent is whether name is a menu entry the phone's bottom bar
// hides: its More entry then stands for the current page.
func moreCurrent(name string) bool {
	for _, m := range menu {
		if m.Name == name {
			return !m.Phone
		}
	}
	return false
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
	// request may then name any IP in its Host header. names are the host
	// names a request may name, in lower case: the ones the operator gave
	// (--host) and, with network, the machine's own.
	network bool
	names   []string
	// limitsMu guards limitsBy, the last answer per provider and the
	// run on its way, if one is; limitsEvery is how long one ask stands.
	limitsMu    sync.Mutex
	limitsBy    map[string]*limitsEntry
	limitsEvery time.Duration
	// as is the user the view acts as (--as), "" when it acts as none:
	// the talk page sends its mail as them, and a bead's page answers its
	// asks and sends it on; no other page takes a write.
	as string
	// pagesMux serves the sandbox origin: a note's pages for running,
	// and nothing else. pagesAddr is the address it listens on, as
	// bound, which the bead page's run links are built from; pagesHost
	// is the public name those links use behind a proxy of yours
	// (--pages-host), "" for none.
	pagesMux  *http.ServeMux
	pagesAddr string
	pagesHost string
}

// NewServer serves the yard behind st, whose home directory is home.
func NewServer(st store.API, home string, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{st: st, home: home, log: log, mux: http.NewServeMux(), streamWrite: streamWrite,
		streamsBy: map[string]int{}, streamMax: streamMax, streamPerAddr: streamPerAddr,
		limitsBy: map[string]*limitsEntry{}, limitsEvery: defaultLimitsEvery,
		pagesMux: http.NewServeMux(), pagesAddr: store.DefaultWebPagesAddr}
	var err error
	if s.assets, err = loadAssets(); err != nil {
		return nil, err
	}
	if s.pages, err = s.loadPages(); err != nil {
		return nil, err
	}
	s.routes()
	s.pagesRoutes()
	return s, nil
}

// partial are the files of templates/ that define a part of a page, not a
// page: every page is parsed with them.
var partial = []string{"shell", "aside", "entry", "card"}

// loadPages parses every template of templates/ with the shell, one set per
// page: each page defines "content", so two pages cannot share a set.
func (s *Server) loadPages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{"asset": s.assetURL, "clock": clock, "inc": inc}
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
// other method itself: without --as the view changes nothing. The routes
// that take a write are registered by ActAs, with --as only.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /talk", s.handleTalk)
	s.mux.HandleFunc("GET /line", s.handleLine)
	s.mux.HandleFunc("GET /events", s.handleEvents)
	s.mux.HandleFunc("GET /sessions", s.handleSessions)
	s.mux.HandleFunc("GET /crew", s.handleCrew)
	s.mux.HandleFunc("GET /flows", s.handleFlows)
	s.mux.HandleFunc("GET /beads", s.handleBeads)
	s.mux.HandleFunc("GET /beads/{id}", s.handleBead)
	s.mux.HandleFunc("GET /blobs/{hash}", s.handleBlob)
	// The configuration lists (lists.go, limits.go): each has a page under
	// the Config entry, with /config as their hub.
	s.mux.HandleFunc("GET /config", s.handleConfig)
	s.mux.HandleFunc("GET /depots", s.handleDepots)
	s.mux.HandleFunc("GET /groups", s.handleGroups)
	s.mux.HandleFunc("GET /routes", s.handleRoutes)
	s.mux.HandleFunc("GET /peers", s.handlePeers)
	s.mux.HandleFunc("GET /deps", s.handleDeps)
	s.mux.HandleFunc("GET /limits", s.handleLimits)
	for _, name := range unbuilt {
		s.mux.HandleFunc("GET /"+name, s.handleUnbuilt(name))
	}
	s.mux.HandleFunc("GET /assets/{file}", s.handleAsset)
	// The yard's liveness facts for a monitor, a proxy or footplate: a
	// machine route, answered directly as JSON, with no template and no
	// menu entry.
	s.mux.HandleFunc("GET /health", s.handleHealth)
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

// pagesRoutes registers the sandbox origin's only route: a note's
// pages for running, served by handleRun. Everything else is the
// mux's own 404: the origin answers no yard route at all, and no
// page's bytes outside the run wrapper.
func (s *Server) pagesRoutes() {
	s.pagesMux.HandleFunc("GET /run/{hash}", s.handleRun)
}

// policy lets the browser load and reach this server only, so a page that
// names another host by mistake fails where it is seen instead of calling
// out. The page has no inline script or style, and nothing may frame it.
// A view that acts has the talk page's form and the beads' own, which post
// to the view itself; one that does not act has no form, and the policy
// says so.
func (s *Server) policy() string {
	form := "form-action 'none'"
	if s.as != "" {
		form = "form-action 'self'"
	}
	return "default-src 'self'; base-uri 'none'; " + form + "; frame-ancestors 'none'"
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A page on another site can make the browser ask this server under a
	// name of its own that resolves to this machine (DNS rebinding) and
	// read the answer, since there is no authentication. The Host header
	// then carries that name, so a name that is not this machine's is
	// refused.
	if !s.knownHost(r.Host) {
		s.misdirected(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", s.policy())
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

// misdirected answers a request whose Host header names no host of this
// machine's with 421, saying which names are answered. extra are answered
// names beyond the view's own: the sandbox origin's public one.
func (s *Server) misdirected(w http.ResponseWriter, r *http.Request, extra ...string) {
	as, hint := "localhost or 127.0.0.1", ""
	if s.network {
		as = "an IP address"
	}
	as = strings.Join(append(append([]string{as}, s.names...), extra...), " or ")
	// The name may be this machine's all the same (a tailnet's, the
	// LAN's DNS, a proxy's): only its operator can say so. What is no
	// host name is not offered back as one, and neither is an IP
	// address, which a view on loopback answers to under no flag but
	// --unsafe.
	host := hostOf(r.Host)
	if name, err := CheckHost(host); err == nil && net.ParseIP(host) == nil {
		hint = ". If that name is this machine's, start the view with --host " + name
	}
	http.Error(w, "yardr web does not answer to another host's name: ask for it as "+as+", not "+r.Host+hint,
		http.StatusMisdirectedRequest)
}

// PagesHandler serves the sandbox origin: a note's pages for running,
// and nothing else. It is fenced as the view itself is — the loopback,
// --host and --unsafe rules with the same 421s — and answers to the
// origin's public name (--pages-host) besides.
func (s *Server) PagesHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.knownHost(r.Host) && !s.knownPagesHost(r.Host) {
			var extra []string
			if s.pagesHost != "" {
				extra = []string{s.pagesHost}
			}
			s.misdirected(w, r, extra...)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", runPolicy)
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		s.pagesMux.ServeHTTP(w, r)
	})
}

// knownPagesHost reports whether a request's Host header is the sandbox
// origin's public name (--pages-host): the name a proxy of yours routes
// to the origin, which is no name of the view's.
func (s *Server) knownPagesHost(hostport string) bool {
	return s.pagesHost != "" && strings.EqualFold(hostOf(hostport), s.pagesHost)
}

// SetPagesAddr is the sandbox origin's address as bound, which the bead
// page's run links are built from. Call it before the server serves.
func (s *Server) SetPagesAddr(addr string) { s.pagesAddr = addr }

// SetPagesHost is the sandbox origin's public name (--pages-host), which
// run links use behind a proxy of yours and the origin answers to. Call
// it before the server serves.
func (s *Server) SetPagesHost(host string) { s.pagesHost = host }

// runBase is the sandbox origin as the browser that asked with hostport
// reaches it, without a scheme: a reader on this machine goes straight
// to where the origin listens, and a reader elsewhere goes by the name
// it reached the view by — the origin's public one where one is given —
// on the origin's port, which a proxy forwards beside the view's.
func (s *Server) runBase(hostport string) string {
	host, port, err := net.SplitHostPort(s.pagesAddr)
	if err != nil {
		host, port, _ = net.SplitHostPort(store.DefaultWebPagesAddr)
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		// The origin listens on every interface (--unsafe): a reader
		// here reaches it on loopback.
		host = "127.0.0.1"
	}
	if h := hostOf(hostport); h == "" || strings.EqualFold(h, "localhost") || loopbackIP(h) {
		return "//" + net.JoinHostPort(host, port)
	}
	if s.pagesHost != "" {
		// The page was reached through a proxy under the view's name;
		// the origin's own name is reached on the same port.
		if _, reqport, err := net.SplitHostPort(hostport); err == nil {
			return "//" + net.JoinHostPort(s.pagesHost, reqport)
		}
		return "//" + s.pagesHost
	}
	return "//" + net.JoinHostPort(hostOf(hostport), port)
}

// loopbackIP reports whether host is an IP address on loopback.
func loopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	// MoreCurrent is whether the phone's More entry stands for the
	// current page: one the bottom bar hides.
	MoreCurrent bool
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
	v := view{Page: p, Entries: menu, Yard: s.yardName(r.Context()), Aside: p.line, Head: p.head, Stream: p.stream, MoreCurrent: moreCurrent(p.Menu)}
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
	if terr := s.pages[p.Template].ExecuteTemplate(&buf, "shell.html", view{Page: p, Entries: menu, Yard: s.yardName(r.Context()), MoreCurrent: moreCurrent(p.Menu)}); terr != nil {
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
// opened, the ones that listen on every interface too. whose is the flag
// that gave the address, as a refusal names it.
func Listen(whose, addr string, unsafe bool) (net.Listener, error) {
	if err := CheckAddr(whose, addr, unsafe); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	// localhost is a name, and a hosts file may point it anywhere.
	if tcp, ok := ln.Addr().(*net.TCPAddr); !unsafe && (!ok || !tcp.IP.IsLoopback()) {
		_ = ln.Close()
		return nil, refused(whose, addr, fmt.Sprintf("it listens on %s", ln.Addr()))
	}
	return ln, nil
}

// CheckAddr refuses a listen address that is not on the loopback
// interface, saying why as whose refused it. With unsafe it refuses only
// what is no address.
func CheckAddr(whose, addr string, unsafe bool) error {
	return store.CheckWebAddr(whose, addr, unsafe)
}

// refused is the refusal of addr, for why, as whose refused it: the
// store's, where the check is for the web_addr setting.
func refused(whose, addr, why string) error { return store.WebAddrRefused(whose, addr, why) }

// AllowNetwork is --unsafe for the requests: the view is served off
// loopback, so a request may name the machine by any IP or by hostname,
// its host name, with or without .local. Every other name is still
// refused: one that resolves here is what DNS rebinding looks like, and an
// IP literal cannot be rebound. hosts are answered as AllowHosts answers
// them. Call it before the server serves.
func (s *Server) AllowNetwork(hostname string, hosts ...string) {
	s.network = true
	s.names = nil
	if name := strings.TrimSuffix(strings.ToLower(hostname), ".local"); name != "" {
		s.names = []string{name, name + ".local"}
	}
	s.addNames(hosts)
}

// AllowHosts is --host without --unsafe: the view stays on loopback and
// answers to hosts too, the names the operator says are this machine's
// (each as CheckHost returns it): one the machine cannot work out by
// itself, as a tailnet's or a proxy's, which reaches a loopback view
// through a proxy that forwards the Host header as it came. Nothing is
// added to them: no IP address off loopback, and not the machine's own
// host name, which only its operator can say resolves nowhere else. Call
// it before the server serves.
func (s *Server) AllowHosts(hosts ...string) {
	s.network = false
	s.names = nil
	s.addNames(hosts)
}

// ActAs is --as: the view acts as user, whom the caller has read from the
// yard's users. It registers the POST routes, which without it do not
// exist: the mux answers a POST as it answers every other method. Call it
// before the server serves.
func (s *Server) ActAs(user string) {
	s.as = user
	s.mux.HandleFunc("POST /talk", s.handleTalkPost)
	s.mux.HandleFunc("POST /beads/{id}", s.handleBeadPost)
}

// As is the user the view acts as, "" when it acts as none.
func (s *Server) As() string { return s.as }

func (s *Server) addNames(hosts []string) {
	for _, h := range hosts {
		if h = strings.ToLower(h); h != "" && !slices.Contains(s.names, h) {
			s.names = append(s.names, h)
		}
	}
}

// Names are the host names a request may name the machine by, besides the
// loopback names and, when the view is served off loopback, any IP address.
func (s *Server) Names() []string { return slices.Clone(s.names) }

// CheckHost returns name as a request's Host header is compared with it,
// in lower case and without the dot that ends a full name, or says why it
// is no host name. A wildcard is none: a view that answers to every name
// has no guard against DNS rebinding left.
func CheckHost(name string) (string, error) { return store.CheckWebHost("--host", name) }

// knownHost reports whether a request's Host header names this machine's
// loopback interface, one of the host names it answers to or, served off
// loopback, the machine by any IP literal.
func (s *Server) knownHost(hostport string) bool {
	host := hostOf(hostport)
	if ip := net.ParseIP(host); ip != nil {
		return s.network || ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost") || slices.Contains(s.names, strings.ToLower(host))
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

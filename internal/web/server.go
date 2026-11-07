// Package web serves a view of the yard for a browser: HTML rendered here
// with html/template, and the same data as JSON to whoever asks for it.
// Without --as it only reads the yard; with --as <user> the talk page
// sends mail as that user, a bead's page answers its asks and sends the
// bead on, the Previews page approves, refuses, stops and restarts, and
// the Telemetry page turns the telemetry switch on or off where that
// user is the operator, and nothing else takes a write, and only for a
// browser that holds the view's cookie (viewtoken.go). In JWT mode each
// request acts as its own verified user instead.
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

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

// unbuilt are the rail's pages no child of the train has built yet: each
// answers with the "not built yet" page. A child that builds one removes it
// here and registers its handler in routes. Every page of the rail is
// built now; a new entry starts here.
var unbuilt = []string{}

// Server is the web view of one yard.
type Server struct {
	st store.API
	// estimates answers beads' estimates over the store; tests set
	// canned ones.
	estimates func(ctx context.Context, st store.API, ids []string, now time.Time) (map[string]*eta.Result, error)
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
	// limitsMu guards limitsBy and sourceLimitsBy, the last answer per
	// provider and per source that answers for itself, each with the run
	// on its way, if one is; limitsEvery is how long one ask stands.
	limitsMu       sync.Mutex
	limitsBy       map[string]*limitsEntry[ProviderLimits]
	sourceLimitsBy map[string]*limitsEntry[SourceLimits]
	limitsEvery    time.Duration
	// as is the user the view acts as (--as), "" when it acts as none:
	// the talk page sends its mail as them, and a bead's page answers its
	// asks and sends it on; no other page takes a write.
	as string
	// token is the secret a view that acts as a user asks a browser for
	// (ActAs), nil for one that does not and for one in JWT mode.
	token *viewToken
	// pagesMux serves the sandbox origin: a note's blobs under the
	// scripted policy, and nothing else. pagesAddr is the address it
	// listens on, as bound, which the bead page's run links are built
	// from; pagesHost is the public name those links use behind a
	// proxy of yours (--pages-host), "" for none.
	pagesMux  *http.ServeMux
	pagesAddr string
	pagesHost string
	// localMux serves the local-read listener (LocalReadHandler): the
	// view's read-only JSON routes and its stream, and nothing else.
	localMux *http.ServeMux
	// jwt verifies a gateway's token per request (UseJWT), nil when the
	// view takes no JWT mode.
	jwt *jwtVerifier
	// freshMu guards freshPending, the strong-card sign-ins waiting at
	// the gateway, and freshStrong, the authentication time each
	// operator's last consumed challenge returned (approve.go).
	freshMu      sync.Mutex
	freshPending map[string]freshChallenge
	freshStrong  map[string]freshAuth
}

// NewServer serves the yard behind st, whose home directory is home.
func NewServer(st store.API, home string, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{st: st, home: home, log: log, mux: http.NewServeMux(), streamWrite: streamWrite,
		streamsBy: map[string]int{}, streamMax: streamMax, streamPerAddr: streamPerAddr,
		limitsBy: map[string]*limitsEntry[ProviderLimits]{}, sourceLimitsBy: map[string]*limitsEntry[SourceLimits]{},
		limitsEvery: defaultLimitsEvery, estimates: eta.EstimateManyViaStore,
		pagesMux: http.NewServeMux(), pagesAddr: store.DefaultWebPagesAddr, localMux: http.NewServeMux(),
		freshPending: map[string]freshChallenge{}, freshStrong: map[string]freshAuth{}}
	var err error
	if s.assets, err = loadAssets(); err != nil {
		return nil, err
	}
	if s.pages, err = s.loadPages(); err != nil {
		return nil, err
	}
	s.routes()
	s.pagesRoutes()
	s.localReadRoutes()
	return s, nil
}

// partial are the files of templates/ that define a part of a page, not a
// page: every page is parsed with them.
var partial = []string{"shell", "aside", "entry", "card", "ask", "csrf"}

// loadPages parses every template of templates/ with the shell, one set per
// page: each page defines "content", so two pages cannot share a set.
func (s *Server) loadPages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{"asset": s.assetURL, "blobURL": blobURL, "clock": clock, "clockTime": clockTime, "csrf": s.csrfValue, "inc": inc, "markAuthor": markAuthor, "strong": store.StrongWeight}
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
// other method itself: without --as or JWT mode the view changes nothing.
// The routes that take a write are registered by ActAs (--as) or UseJWT
// (JWT mode).
func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /talk", s.handleTalk)
	s.mux.HandleFunc("GET /line", s.handleLine)
	s.mux.HandleFunc("GET /events", s.handleEvents)
	s.mux.HandleFunc("GET /sessions", s.handleSessions)
	s.mux.HandleFunc("GET /crew", s.handleCrew)
	s.mux.HandleFunc("GET /previews", s.handlePreviews)
	s.mux.HandleFunc("GET /flows", s.handleFlows)
	s.mux.HandleFunc("GET /beads", s.handleBeads)
	s.mux.HandleFunc("GET /beads/{id}", s.handleBead)
	// One file of the full text behind a summary card (packfiles.go).
	s.mux.HandleFunc("GET /beads/{id}/asks/{ask}/files/{n}", s.handlePackFile)
	// The gateway's return after a strong card's sign-in: the one-use
	// state the approve minted, bound to its card (approve.go).
	s.mux.HandleFunc("GET /beads/{id}/fresh/{ask}/{state}", s.handleFresh)
	s.mux.HandleFunc("GET /blobs/{hash}", s.handleBlob)
	s.mux.HandleFunc("GET /blobs/{hash}/{name}", s.handleBlob)
	// The configuration lists (lists.go, limits.go): each has a page under
	// the Config entry, with /config as their hub.
	s.mux.HandleFunc("GET /pairing", s.handlePairing)
	s.mux.HandleFunc("GET /pairing.png", s.handlePairingPNG)
	s.mux.HandleFunc("GET /config", s.handleConfig)
	s.mux.HandleFunc("GET /depots", s.handleDepots)
	s.mux.HandleFunc("GET /groups", s.handleGroups)
	s.mux.HandleFunc("GET /routes", s.handleRoutes)
	s.mux.HandleFunc("GET /peers", s.handlePeers)
	s.mux.HandleFunc("GET /deps", s.handleDeps)
	s.mux.HandleFunc("GET /limits", s.handleLimits)
	s.mux.HandleFunc("GET /telemetry", s.handleTelemetry)
	for _, name := range unbuilt {
		s.mux.HandleFunc("GET /"+name, s.handleUnbuilt(name))
	}
	s.mux.HandleFunc("GET /assets/{file}", s.handleAsset)
	// The yard's liveness facts for a monitor, a proxy or footplate: a
	// machine route, answered directly as JSON, with no template and no
	// rail entry.
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

// pagesRoutes registers the sandbox origin's only route: a note's blobs,
// served by handlePagesBlob. Everything else is the mux's own 404: the
// origin answers no yard route at all.
func (s *Server) pagesRoutes() {
	s.pagesMux.HandleFunc("GET /blobs/{hash}", s.handlePagesBlob)
	s.pagesMux.HandleFunc("GET /blobs/{hash}/{name}", s.handlePagesBlob)
}

// policy lets the browser load and reach this server only, so a page that
// names another host by mistake fails where it is seen instead of calling
// out. The page has no inline script or style, and nothing may frame it.
// A request that acts has the talk page's form and the beads' own, which
// post to the view itself; one that does not act has no form, and the
// policy says so. In JWT mode that is per request: a request whose token
// resolved to a user may post, and /health, which carries none, may not.
func (s *Server) policy(r *http.Request) string {
	form := "form-action 'none'"
	if s.acting(r.Context()) != "" {
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
	// In JWT mode every request proves itself with the gateway's token,
	// but the health check, which carries no user and says no secret.
	if r.URL.Path != "/health" {
		var ok bool
		if r, ok = s.verifyJWT(w, r); !ok {
			return
		}
	}
	// A view that acts as a user takes its writes from the browser that
	// holds its cookie, and acts for no other (viewtoken.go).
	if s.token != nil {
		var ok bool
		if r, ok = s.admit(w, r); !ok {
			return
		}
	}
	h := w.Header()
	h.Set("Content-Security-Policy", s.policy(r))
	h.Set("X-Content-Type-Options", "nosniff")
	// same-origin keeps the referrer off other sites as no-referrer
	// did, and lets a plain form POST carry its origin: under
	// no-referrer a browser sends Origin null for a navigate-mode POST
	// (Fetch, "append a request Origin header"), which ownOrigin
	// refuses.
	h.Set("Referrer-Policy", "same-origin")
	s.mux.ServeHTTP(w, r)
}

// verifyJWT is the request with its verified gateway identity, or the
// refusal already answered: in JWT mode every request proves itself with
// the gateway's token. Both listeners ask it — the view's ServeHTTP,
// which spares the health check, and the sandbox origin's PagesHandler —
// so a blob has no tokenless path.
func (s *Server) verifyJWT(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if s.jwt == nil {
		return r, true
	}
	id, code, msg := s.jwt.verify(r)
	if code != 0 {
		if code == http.StatusServiceUnavailable {
			// No keys: the next fetch may have them.
			w.Header().Set("Retry-After", "60")
		}
		http.Error(w, msg, code)
		return nil, false
	}
	// The email table is the start's: a user removed since is read now,
	// so no restart stands between an uninvite and the view refusing
	// them. A store that cannot be read refuses nothing here; the page
	// it is read for fails as it does without JWT mode.
	if _, removed := s.jwtUserStatus(r.Context(), id.user); removed {
		http.Error(w, "the gateway's token acts as "+id.user+", who is no longer a user of the yard", http.StatusForbidden)
		return nil, false
	}
	return r.WithContext(context.WithValue(r.Context(), jwtUserKey{}, id)), true
}

// jwtUserStatus reads the mapped user now, so an open event stream follows
// removals too. Other store errors keep the request's existing behaviour.
func (s *Server) jwtUserStatus(ctx context.Context, user string) (operator, removed bool) {
	u, err := s.st.GetUser(ctx, user)
	if errors.Is(err, store.ErrNotFound) {
		return false, true
	}
	return err == nil && u.IsOperator(), false
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

// PagesHandler serves the sandbox origin: a note's blobs under the
// scripted policy, and nothing else. It is fenced as the view itself is
// — the loopback, --host and --unsafe rules with the same 421s, and in
// JWT mode the gateway's token with the same 401s — and answers to the
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
		// In JWT mode the gateway's token guards the sandbox origin
		// too: the same verify as the view's, so a blob has no
		// tokenless path.
		verified, ok := s.verifyJWT(w, r)
		if !ok {
			return
		}
		r = verified
		h := w.Header()
		h.Set("Content-Security-Policy", scriptedPolicy)
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		// no-referrer stays here: the sandbox origin is opaque, has no
		// forms (form-action 'none'), and must leak nothing.
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
	// Menu is the name the rail knows the page by: its mode's tile
	// stands open and its column row is marked current. "" for none.
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
	// inbox is the acting user's inbox read, for a handler that has read
	// it already; respond reads it otherwise, once, for the rail's Crew
	// pill. The JSON never carries it.
	inbox *store.Mailbox
	// stream is the ?format= of the page's stream, for a page that hangs
	// more on it than the shell does: card on /flows, seq on a page that
	// only reads itself again, home on / for the entries its feed keeps.
	// "" is the feed's entries.
	stream string
	// Wide is the Yard panel: its centre runs to the window's edge.
	// The pages without the aside do the same by the stylesheet.
	// The JSON never carries it.
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
	Yard  string
	Rail  rail
	Aside *Line
	// Needs counts what waits for the visitor, as home lists it: the
	// header's line on a page without the aside, nothing at zero.
	Needs int
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
	// The shell shows the yard's name on every page: the aside beside
	// home and the line, and the header's count elsewhere.
	v := view{Page: p, Yard: s.yardName(r.Context()), Aside: p.line, Head: p.head, Stream: p.stream}
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
		// Home and the line carry the aside and need no count; they
		// pass their line and never reach here.
		v.Needs = len(attention(ov, v.Aside))
	}
	// The rail's Crew pill beside the line: the acting user's aggregate
	// unread, read once per request — the talk page passes its own read
	// through — and not read at all where the view acts as no user. A
	// visitor the yard holds no inbox for reads with no pill, as a yard
	// whose asks cannot be read waits without them.
	inbox := p.inbox
	if inbox == nil {
		if as := s.acting(r.Context()); as != "" {
			var err error
			if inbox, err = s.st.UserInbox(r.Context(), as, 1, as); err != nil {
				if r.Context().Err() != nil {
					s.fail(w, r, err)
					return
				}
				s.log.Error("web: user inbox", "err", err)
				inbox = nil
			}
		}
	}
	v.Rail = s.rail(r.Context(), p.Menu, r.URL.EscapedPath(), v.Aside, inbox)
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
	v := view{Page: p, Yard: s.yardName(r.Context())}
	// An error page stands under the rail with no open mode and no pills;
	// it still carries the header's count when the yard can answer it.
	// Keep the error itself renderable when those reads failed.
	v.Rail = s.rail(r.Context(), "", r.URL.EscapedPath(), nil, nil)
	if ov, overviewErr := s.overview(r.Context(), nil); overviewErr == nil {
		if l, lineErr := s.line(r.Context(), ov); lineErr == nil {
			v.Needs = len(attention(ov, l))
		}
	}
	var buf bytes.Buffer
	if terr := s.pages[p.Template].ExecuteTemplate(&buf, "shell.html", v); terr != nil {
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
// yard's users, for a browser that holds the view's cookie. It registers the
// POST routes, which without it do not exist: the mux answers a POST as it
// answers every other method. It returns the token that buys the cookie,
// which the caller keeps for the operator alone (a view that acts has no
// way to be without one): the one in the home's file for user
// (config.WebToken), made there by the first serve that acts as them and
// taken by every serve after, until yardr web revoke replaces it. A cookie
// lasts cookieAge from when the token bought it, web_cookie_days' default
// where it is 0. Call it before the server serves.
func (s *Server) ActAs(user string, cookieAge time.Duration) (token string, err error) {
	file := config.At(s.home).WebToken(user)
	if token, err = LoadViewToken(file); err != nil {
		return "", fmt.Errorf("the web view's token for %s: %w", user, err)
	}
	if cookieAge <= 0 {
		cookieAge = store.DefaultWebCookieDays * 24 * time.Hour
	}
	s.as = user
	s.token = &viewToken{file: file, maxAge: cookieAge, now: time.Now}
	s.postRoutes()
	return token, nil
}

// postRoutes registers the routes that take a write: the talk page's
// form, the bead page's own, pairing's, and the Previews page's. ActAs
// and UseJWT each register them once, and never together: a view acts
// as its fixed user or per request, not both.
func (s *Server) postRoutes() {
	s.mux.HandleFunc("POST /talk", s.handleTalkPost)
	s.mux.HandleFunc("POST /beads/{id}", s.handleBeadPost)
	s.mux.HandleFunc("POST /pairing", s.handlePairingPost)
	s.mux.HandleFunc("POST /previews", s.handlePreviewsPost)
	s.mux.HandleFunc("POST /telemetry", s.handleTelemetryPost)
}

// As is the user the view acts as, "" when it acts as none.
func (s *Server) As() string { return s.as }

// acting is the user the request acts as: the request's verified JWT user
// in JWT mode, else the view's fixed one (--as) for a request that holds
// its cookie, and no one for the rest, which read as on a view with no --as.
// Every write path and every gate that names a user reads this, so two
// visitors act as themselves on one view.
func (s *Server) acting(ctx context.Context) string {
	if s.jwt != nil {
		return jwtIdentityOf(ctx).user
	}
	if s.token != nil && ctx.Value(tokenHeldKey{}) == nil {
		return ""
	}
	return s.as
}

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

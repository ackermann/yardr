package bdp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits are advertised in discovery and enforced.
type Limits struct {
	PageDefault, PageMax int
	TargetBytes          int
	Selector             SelectorLimits
}

var DefaultLimits = Limits{
	PageDefault: 50, PageMax: 200, TargetBytes: 4096,
	Selector: SelectorLimits{Bytes: 1024, Depth: 16, Nodes: 128},
}

// Server answers the BDP Read profile for one Scope.
type Server struct {
	scope     string // canonical, ends in "/"
	scopePath string
	origin    string
	load      func(ctx context.Context) (*Graph, error)
	limits    Limits
	log       *slog.Logger
	pages     *pageCache
}

// NewServer serves the Scope at scope (an absolute http(s) URL ending in
// "/"), reading a fresh Graph per request from load.
func NewServer(scope string, load func(ctx context.Context) (*Graph, error), limits Limits, log *slog.Logger) (*Server, error) {
	u, err := url.Parse(scope)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		!strings.HasSuffix(u.Path, "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("scope must be an absolute http(s) URL ending in / with no query")
	}
	if u.String() != scope {
		return nil, errors.New("scope is not in canonical form: " + u.String())
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{scope: scope, scopePath: u.EscapedPath(), origin: u.Scheme + "://" + u.Host,
		load: load, limits: limits, log: log,
		pages: newPageCache(10*time.Minute, 2000)}, nil
}

// problem codes of the Read profile: code -> family, status, retry.
var problems = map[string]struct {
	family string
	status int
	retry  string
}{
	"invalid-parameter":       {"request", 400, "never"},
	"resource-not-found":      {"not-found", 404, "after-state-change"},
	"cursor-expired":          {"gone", 410, "after-state-change"},
	"foreign-view":            {"conflict", 409, "after-state-change"},
	"request-too-large":       {"size", 413, "never"},
	"limit-exceeded":          {"size", 413, "never"},
	"temporarily-unavailable": {"unavailable", 503, "after-delay"},
}

type problemError struct{ code string }

func (e problemError) Error() string { return e.code }

func fail(code string) error { return problemError{code} }

// response is what a route produces before shared HTTP rules apply.
type response struct {
	status  int
	body    any // marshaled as JSON; nil for none
	etag    string
	headers map[string]string
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// A request target is printable ASCII (RFC 9112); raw octets outside
	// it are malformed at the transport level: 400 with no body.
	for i := 0; i < len(r.RequestURI); i++ {
		if c := r.RequestURI[i]; c < 0x21 || c > 0x7e {
			w.Header().Set("Content-Length", "0")
			w.Header().Set("Connection", "close")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	resp, err := s.route(r)
	if err != nil {
		var p problemError
		if !errors.As(err, &p) {
			s.log.Error("bdp request failed", "target", r.RequestURI, "err", err)
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.writeProblem(w, r, p.code)
		return
	}
	s.writeSuccess(w, r, resp)
}

func (s *Server) writeProblem(w http.ResponseWriter, r *http.Request, code string) {
	def := problems[code]
	body, _ := json.Marshal(map[string]any{
		"type":   "https://github.com/gastownhall/bdp/problems/" + def.family,
		"code":   code,
		"retry":  def.retry,
		"status": def.status,
	})
	h := w.Header()
	h.Set("Content-Type", "application/problem+json")
	h.Set("Cache-Control", "private, no-store")
	if def.retry == "after-delay" {
		h.Set("Retry-After", "1")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(def.status)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// writeSuccess applies negotiation, then If-Match, then If-None-Match,
// after routing decided the request succeeds.
func (s *Server) writeSuccess(w http.ResponseWriter, r *http.Request, resp response) {
	h := w.Header()
	for k, v := range resp.headers {
		h.Set(k, v)
	}
	var body []byte
	if resp.body != nil {
		var err error
		if body, err = marshal(resp.body); err != nil {
			s.log.Error("bdp encode failed", "err", err)
			h.Set("Content-Length", "0")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		h.Set("Content-Type", "application/json")
		h.Set("Vary", "Accept")
	}
	h.Set("Cache-Control", "private, no-store")
	if resp.etag != "" {
		h.Set("ETag", resp.etag)
	}
	status := resp.status
	switch {
	case body != nil && !acceptsJSON(r.Header.Values("Accept")):
		status = http.StatusNotAcceptable
		h.Del("ETag")
	default:
		status = conditional(r.Header, resp.etag, status)
	}
	if status != resp.status {
		h.Del("Content-Type")
		if status == http.StatusNotModified {
			h.Del("Content-Length")
		} else {
			h.Set("Content-Length", "0")
		}
		w.WriteHeader(status)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead && body != nil {
		w.Write(body)
	}
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// route maps a raw request target to a response or a problem.
func (s *Server) route(r *http.Request) (response, error) {
	raw := r.RequestURI
	if len(raw) > s.limits.TargetBytes {
		return response{}, fail("request-too-large")
	}
	// Absolute-form and scheme-relative targets name an authority; identity
	// comes from the configured Scope, so only the path and query count.
	if m := absoluteForm.FindStringIndex(raw); m != nil {
		raw = raw[m[1]:]
	} else if strings.HasPrefix(raw, "//") {
		end := strings.IndexAny(raw[2:], "/?")
		if end < 0 {
			raw = ""
		} else {
			raw = raw[2+end:]
		}
	}
	if raw == "" || raw[0] == '?' {
		raw = "/" + raw
	}
	if strings.ContainsAny(raw, "#\\") || !strings.HasPrefix(raw, "/") {
		return response{}, fail("resource-not-found")
	}
	path, rawQuery, _ := strings.Cut(raw, "?")
	if !strings.HasPrefix(path, s.scopePath) {
		return response{}, fail("resource-not-found")
	}
	rel := path[len(s.scopePath):]
	q, err := parseQuery(rawQuery)
	if err != nil {
		return response{}, err
	}

	switch rel {
	case "":
		if err := q.only(); err != nil {
			return response{}, err
		}
		return response{status: http.StatusNoContent, headers: map[string]string{
			"Link": `<bdp.json>; rel="service-desc"; type="application/json"`}}, nil
	case "bdp.json":
		if err := q.only(); err != nil {
			return response{}, err
		}
		return response{status: 200, body: s.discovery()}, nil
	case "beads/", "links/", "types/":
		return s.collection(r.Context(), strings.TrimSuffix(rel, "/"), q)
	}

	root, rest, ok := strings.Cut(rel, "/")
	if !ok || (root != "beads" && root != "links" && root != "types") {
		return response{}, fail("resource-not-found")
	}
	for _, seg := range strings.Split(rest, "/") {
		if _, err := DecodeSegment(seg); err != nil {
			return response{}, fail("resource-not-found")
		}
	}
	id := s.scope + rel
	// Parameters are validated before the lookup, so a bad request is a
	// 400 whether or not the resource exists.
	view := q.get("view")
	switch {
	case root == "types" || view == "":
		if err := q.only(); err != nil {
			return response{}, err
		}
	case view == "properties":
		if err := q.only("view"); err != nil {
			return response{}, err
		}
	case view == "links" && root == "beads":
		if err := q.only("view", "direction", "limit", "cursor"); err != nil {
			return response{}, err
		}
	default:
		return response{}, fail("invalid-parameter")
	}
	g, err := s.graph(r.Context())
	if err != nil {
		return response{}, err
	}
	if root == "types" {
		d, ok := g.descriptors[id]
		if !ok {
			return response{}, fail("resource-not-found")
		}
		return response{status: 200, body: d, headers: map[string]string{
			"Link": `<https://github.com/gastownhall/bdp/schemas/bdp-v0.schema.json#/$defs/typeDescriptor>; rel="describedby"; type="application/schema+json"`}}, nil
	}
	rec, ok := g.byID[id]
	if !ok || rec.link != (root == "links") {
		return response{}, fail("resource-not-found")
	}
	switch view {
	case "properties":
		return response{status: 200, body: rec.Properties, etag: etag(rec.Revision)}, nil
	case "links":
		return s.beadLinks(g, rec, q)
	}
	return response{status: 200, body: rec, etag: etag(rec.Revision)}, nil
}

var absoluteForm = regexp.MustCompile(`^(?i:https?)://[^/?#]*`)

func (s *Server) graph(ctx context.Context) (*Graph, error) {
	g, err := s.load(ctx)
	if err != nil {
		s.log.Error("bdp load failed", "err", err)
		return nil, fail("temporarily-unavailable")
	}
	return g, nil
}

func (s *Server) discovery() map[string]any {
	return map[string]any{
		"bdpVersion": "0",
		"profile":    "read",
		"scope":      s.scope,
		"beads":      s.scope + "beads/",
		"links":      s.scope + "links/",
		"types":      s.scope + "types/",
		"order":      "canonical-uri",
		"limits": map[string]any{
			"page":    map[string]int{"defaultItems": s.limits.PageDefault, "maximumItems": s.limits.PageMax},
			"request": map[string]int{"targetBytes": s.limits.TargetBytes},
			"selector": map[string]int{"bytes": s.limits.Selector.Bytes, "depth": s.limits.Selector.Depth,
				"nodes": s.limits.Selector.Nodes},
		},
	}
}

// page is one consistent, ordered result set being handed out in pages.
type page struct {
	projection string // the continuation URL without its cursor
	items      []any
	offset     int
	limit      int
}

func (s *Server) collection(ctx context.Context, name string, q query) (response, error) {
	allowed := map[string][]string{
		"beads": {"type", "conformsTo", "selector", "limit", "cursor"},
		"links": {"type", "conformsTo", "source", "target", "endpoint", "selector", "limit", "cursor"},
		"types": {"limit", "cursor"},
	}[name]
	if err := q.only(allowed...); err != nil {
		return response{}, err
	}
	limit, err := s.limit(q)
	if err != nil {
		return response{}, err
	}
	// Validate every filter before touching state, so a bad request is a
	// 400 whether or not it carries a cursor.
	var typ, conforms string
	for _, p := range []struct {
		name string
		dst  *string
	}{{"type", &typ}, {"conformsTo", &conforms}} {
		if v := q.get(p.name); v != "" || q.has(p.name) {
			t, err := s.typeParam(v)
			if err != nil {
				return response{}, err
			}
			*p.dst = t
		}
	}
	endpoints := map[string]string{}
	for _, name := range []string{"source", "target", "endpoint"} {
		if q.has(name) {
			e, err := s.endpointParam(q.get(name))
			if err != nil {
				return response{}, err
			}
			endpoints[name] = e
		}
	}
	var sel selNode
	if q.has("selector") {
		sel, err = ParseSelector(q.get("selector"), s.limits.Selector)
		if errors.Is(err, errSelectorLimit) {
			return response{}, fail("limit-exceeded")
		} else if err != nil {
			return response{}, fail("invalid-parameter")
		}
	}

	// The continuation URL is canonical: parameters in a fixed order.
	proj := s.scope + name + "/"
	var params []string
	add := func(k, v string) { params = append(params, url.QueryEscape(k)+"="+url.QueryEscape(v)) }
	if typ != "" {
		add("type", typ)
	}
	if conforms != "" {
		add("conformsTo", conforms)
	}
	for _, k := range []string{"source", "target", "endpoint"} {
		if v, ok := endpoints[k]; ok {
			add(k, v)
		}
	}
	if q.has("selector") {
		add("selector", q.get("selector"))
	}
	if q.has("limit") {
		add("limit", strconv.Itoa(limit))
	}
	if len(params) > 0 {
		proj += "?" + strings.Join(params, "&")
	}
	if q.has("cursor") {
		return s.continuePage(proj, q.get("cursor"))
	}

	g, err := s.graph(ctx)
	if err != nil {
		return response{}, err
	}
	var items []any
	switch name {
	case "types":
		for _, t := range g.Types {
			items = append(items, t)
		}
	case "beads", "links":
		src := g.Beads
		if name == "links" {
			src = g.Links
		}
		for _, rec := range src {
			if typ != "" && rec.Type != typ {
				continue
			}
			if conforms != "" && !g.ConformsTo(rec.Type, conforms) {
				continue
			}
			if e, ok := endpoints["source"]; ok && rec.Source != e {
				continue
			}
			if e, ok := endpoints["target"]; ok && rec.Target != e {
				continue
			}
			if e, ok := endpoints["endpoint"]; ok && rec.Source != e && rec.Target != e {
				continue
			}
			if sel != nil && !Match(sel, candidate(rec)) {
				continue
			}
			items = append(items, rec)
		}
	}
	return s.firstPage(proj, items, limit), nil
}

func (s *Server) beadLinks(g *Graph, bead *Record, q query) (response, error) {
	dir := "both"
	if q.has("direction") {
		dir = q.get("direction")
		if dir != "inbound" && dir != "outbound" && dir != "both" {
			return response{}, fail("invalid-parameter")
		}
	}
	limit, err := s.limit(q)
	if err != nil {
		return response{}, err
	}
	proj := bead.ID + "?view=links"
	if q.has("direction") {
		proj += "&direction=" + dir
	}
	if q.has("limit") {
		proj += "&limit=" + strconv.Itoa(limit)
	}
	if q.has("cursor") {
		return s.continuePage(proj, q.get("cursor"))
	}
	var items []any
	for _, l := range g.Links {
		out, in := l.Source == bead.ID, l.Target == bead.ID
		if (dir == "outbound" && out) || (dir == "inbound" && in) || (dir == "both" && (out || in)) {
			items = append(items, l)
		}
	}
	return s.firstPage(proj, items, limit), nil
}

func (s *Server) limit(q query) (int, error) {
	if !q.has("limit") {
		return s.limits.PageDefault, nil
	}
	v := q.get("limit")
	if !positiveInt.MatchString(v) {
		return 0, fail("invalid-parameter")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n > s.limits.PageMax {
		return 0, fail("limit-exceeded")
	}
	return n, nil
}

var positiveInt = regexp.MustCompile(`^[1-9][0-9]*$`)

func (s *Server) firstPage(proj string, items []any, limit int) response {
	if items == nil {
		items = []any{}
	}
	return s.servePage(&page{projection: proj, items: items, limit: limit})
}

func (s *Server) continuePage(proj, token string) (response, error) {
	p, ok := s.pages.get(token)
	if !ok {
		return response{}, fail("cursor-expired")
	}
	if p.projection != proj {
		return response{}, fail("invalid-parameter")
	}
	return s.servePage(p), nil
}

func (s *Server) servePage(p *page) response {
	end := min(p.offset+p.limit, len(p.items))
	var next any // JSON null on the last page
	if end < len(p.items) {
		token := s.pages.put(&page{projection: p.projection, items: p.items, offset: end, limit: p.limit})
		sep := "?"
		if strings.Contains(p.projection, "?") {
			sep = "&"
		}
		next = p.projection + sep + "cursor=" + token
	}
	return response{status: 200, body: map[string]any{"items": p.items[p.offset:end], "next": next}}
}

// typeParam accepts an absolute Type URL; one inside the Scope must be
// spelled canonically.
func (s *Server) typeParam(v string) (string, error) {
	u, err := url.Parse(v)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Fragment != "" {
		return "", fail("invalid-parameter")
	}
	if strings.HasPrefix(v, s.scope) {
		if !s.canonicalLocal(strings.TrimPrefix(v, s.scope), "types") {
			return "", fail("invalid-parameter")
		}
	}
	return v, nil
}

// endpointParam normalizes a local bead id or an in-Scope URL to the
// canonical absolute URL; any other absolute URI is compared as given.
func (s *Server) endpointParam(v string) (string, error) {
	if v == "" {
		return "", fail("invalid-parameter")
	}
	if !uriScheme.MatchString(v) {
		if !s.canonicalLocal(v, "beads") {
			return "", fail("invalid-parameter")
		}
		return s.scope + v, nil
	}
	// An in-Scope URL must be the canonical spelling of a local bead id;
	// anything else is an opaque external reference.
	if u, err := url.Parse(v); err == nil && strings.EqualFold(u.Scheme+"://"+u.Host, s.origin) &&
		strings.HasPrefix(u.EscapedPath(), s.scopePath) {
		if u.User != nil || !strings.HasPrefix(v, s.scope) ||
			!s.canonicalLocal(strings.TrimPrefix(v, s.scope), "beads") {
			return "", fail("invalid-parameter")
		}
	}
	return v, nil
}

var uriScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

func (s *Server) canonicalLocal(local, root string) bool {
	r, rest, ok := strings.Cut(local, "/")
	if !ok || r != root || strings.ContainsAny(rest, "?#") {
		return false
	}
	for _, seg := range strings.Split(rest, "/") {
		if _, err := DecodeSegment(seg); err != nil {
			return false
		}
	}
	return true
}

// candidate is the selector's view of a record, as plain JSON values.
func candidate(r *Record) any {
	doc := map[string]any{"id": r.ID, "type": r.Type, "properties": r.Properties}
	if r.link {
		doc["source"], doc["target"] = r.Source, r.Target
	}
	b, _ := json.Marshal(doc)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// etag quotes a revision; one with bytes an entity-tag cannot carry is
// base64url-encoded, as the reference server does.
func etag(rev string) string {
	if rev == "" {
		return ""
	}
	for i := 0; i < len(rev); i++ {
		if c := rev[i]; c < 0x21 || c == 0x22 || c > 0x7e {
			return `"bdp-b64_` + base64.RawURLEncoding.EncodeToString([]byte(rev)) + `"`
		}
	}
	return `"` + rev + `"`
}

// query is a parsed query string that rejects repeated parameters.
type query map[string]string

func parseQuery(raw string) (query, error) {
	q := query{}
	if raw == "" {
		return q, nil
	}
	for _, part := range strings.Split(raw, "&") {
		k, v, _ := strings.Cut(part, "=")
		key, err1 := url.QueryUnescape(k)
		val, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil || key == "" {
			return nil, fail("invalid-parameter")
		}
		if _, dup := q[key]; dup {
			return nil, fail("invalid-parameter")
		}
		q[key] = val
	}
	return q, nil
}

func (q query) get(k string) string { return q[k] }
func (q query) has(k string) bool   { _, ok := q[k]; return ok }

// only fails unless every parameter present is one of allowed.
func (q query) only(allowed ...string) error {
	for k := range q {
		ok := false
		for _, a := range allowed {
			ok = ok || a == k
		}
		if !ok {
			return fail("invalid-parameter")
		}
	}
	return nil
}

// pageCache holds result sets for cursors, so every page of one listing
// reflects one snapshot. Expired or evicted cursors are cursor-expired.
type pageCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	items map[string]pageEntry
	order []string
}

type pageEntry struct {
	p       *page
	expires time.Time
}

func newPageCache(ttl time.Duration, max int) *pageCache {
	return &pageCache{ttl: ttl, max: max, items: map[string]pageEntry{}}
}

func (c *pageCache) put(p *page) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for len(c.order) > 0 && (len(c.order) >= c.max || now.After(c.items[c.order[0]].expires)) {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
	c.items[token] = pageEntry{p: p, expires: now.Add(c.ttl)}
	c.order = append(c.order, token)
	return token
}

func (c *pageCache) get(token string) (*page, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[token]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.p, true
}

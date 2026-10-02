package bdp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/bdp"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

const scope = "https://beads.test/acme/"

type fixture struct {
	t  *testing.T
	st store.API
	ts *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	st := storetest.New(t)
	srv, err := bdp.NewServer(scope, func(ctx context.Context) (*bdp.Graph, error) {
		snap, err := st.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		return bdp.BuildGraph(scope, snap), nil
	}, bdp.Limits{PageDefault: 2, PageMax: 5, TargetBytes: 512,
		Selector: bdp.SelectorLimits{Bytes: 200, Depth: 4, Nodes: 20}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(srv)
	ts.Listener = bdp.BodylessTransportErrors(ts.Listener)
	ts.Start()
	t.Cleanup(ts.Close)
	return &fixture{t: t, st: st, ts: ts}
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

// do sends a raw request target (path relative to the Scope, or absolute).
func (f *fixture) do(method, target string, headers ...string) resp {
	f.t.Helper()
	if strings.HasPrefix(target, scope) {
		target = strings.TrimPrefix(target, "https://beads.test")
	} else {
		target = "/acme/" + target
	}
	req, err := http.NewRequest(method, f.ts.URL, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.URL.Opaque = target // send the target byte for byte
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, b}
}

func (f *fixture) get(target string, headers ...string) resp { return f.do("GET", target, headers...) }

// getRaw sends target exactly as given on the request line.
func (f *fixture) getRaw(target string) resp {
	f.t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.ts.URL, "http://"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: beads.test\r\nConnection: close\r\n\r\n", target)
	r, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, b}
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return m
}

func (r resp) problem(t *testing.T, status int, code string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d (body %s)", r.status, status, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	if got := r.json(t)["code"]; got != code {
		t.Fatalf("code = %v, want %s", got, code)
	}
}

func (f *fixture) bead(title, typ string) *store.Bead {
	b, err := f.st.CreateBead(context.Background(), store.NewBead{Title: title, Type: typ}, "tester")
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func TestDiscovery(t *testing.T) {
	f := newFixture(t)
	r := f.get("")
	if r.status != 204 || r.header.Get("Link") != `<bdp.json>; rel="service-desc"; type="application/json"` {
		t.Fatalf("root: %d %v", r.status, r.header)
	}
	d := f.get("bdp.json").json(t)
	if d["bdpVersion"] != "0" || d["profile"] != "read" || d["scope"] != scope ||
		d["beads"] != scope+"beads/" || d["order"] != "canonical-uri" {
		t.Fatalf("discovery = %v", d)
	}
	f.get("bdp.json?x=1").problem(t, 400, "invalid-parameter")
}

func TestBeadRecordAndHTTPRules(t *testing.T) {
	f := newFixture(t)
	b := f.bead("Write the spec", "task")

	r := f.get("beads/" + b.ID)
	if r.status != 200 || r.header.Get("Content-Type") != "application/json" ||
		r.header.Get("Cache-Control") != "private, no-store" || r.header.Get("Vary") != "Accept" {
		t.Fatalf("headers: %d %v", r.status, r.header)
	}
	rec := r.json(t)
	if rec["id"] != scope+"beads/"+b.ID || rec["type"] != "https://work.example/types/task" ||
		rec["revision"] != b.Revision {
		t.Fatalf("record = %v", rec)
	}
	if a := rec["attribution"].(map[string]any); a["principal"] != "tester" || a["status"] != "claimed" {
		t.Fatalf("attribution = %v", a)
	}
	props := rec["properties"].(map[string]any)
	if props["status"] != "open" || props["title"] != "Write the spec" || !strings.HasSuffix(props["created_at"].(string), "Z") {
		t.Fatalf("properties = %v", props)
	}
	if props["comment_count"] != float64(0) || props["dependency_count"] != float64(0) {
		t.Fatalf("bd counts = %v", props)
	}
	etag := r.header.Get("ETag")
	if etag != `"`+b.Revision+`"` {
		t.Fatalf("etag = %s", etag)
	}

	// view=properties is the properties object, same validator.
	pr := f.get("beads/" + b.ID + "?view=properties")
	if pr.json(t)["title"] != "Write the spec" || pr.header.Get("ETag") != etag {
		t.Fatalf("properties view: %s %v", pr.body, pr.header)
	}

	// Conditional requests.
	if r := f.get("beads/"+b.ID, "If-None-Match", etag); r.status != 304 || len(r.body) != 0 {
		t.Fatalf("If-None-Match match: %d", r.status)
	}
	if r := f.get("beads/"+b.ID, "If-None-Match", `W/`+etag); r.status != 304 {
		t.Fatalf("weak If-None-Match: %d", r.status)
	}
	if r := f.get("beads/"+b.ID, "If-Match", `"other"`); r.status != 412 || len(r.body) != 0 {
		t.Fatalf("If-Match mismatch: %d", r.status)
	}
	if r := f.get("beads/"+b.ID, "If-Match", etag); r.status != 200 {
		t.Fatalf("If-Match match: %d", r.status)
	}
	if r := f.get("", "If-None-Match", "*"); r.status != 304 {
		t.Fatalf("root If-None-Match *: %d", r.status)
	}

	// Negotiation: most specific range wins; 406 is bodyless.
	for accept, want := range map[string]int{
		"application/json":           200,
		"*/*":                        200,
		"text/html":                  406,
		"application/json;q=0, */*":  406,
		"application/json;charset=x": 406,
		"text/html, application/*":   200,
	} {
		if r := f.get("beads/"+b.ID, "Accept", accept); r.status != want || (want == 406 && len(r.body) != 0) {
			t.Errorf("Accept %q: %d, want %d", accept, r.status, want)
		}
	}
	// Errors are not negotiated.
	f.get("beads/nope", "Accept", "text/html").problem(t, 404, "resource-not-found")

	// HEAD: same headers, no body.
	h := f.do("HEAD", "beads/"+b.ID)
	if h.status != 200 || len(h.body) != 0 || h.header.Get("Content-Length") != itoa(len(r.body)) {
		t.Fatalf("HEAD: %d len=%s body=%d", h.status, h.header.Get("Content-Length"), len(h.body))
	}
	// Other methods: 405, Allow, no body.
	if r := f.do("POST", "beads/"+b.ID); r.status != 405 || r.header.Get("Allow") != "GET, HEAD" || len(r.body) != 0 {
		t.Fatalf("POST: %d %v", r.status, r.header)
	}
}

func TestRequestTargetsAndParameters(t *testing.T) {
	f := newFixture(t)
	b := f.bead("x", "task")
	for _, target := range []string{
		"beads/" + strings.Replace(b.ID, "y", "%79", 1), // non-canonical escape
		"beads/" + b.ID + "/",                           // empty segment
		"beads",                                         // collection without slash
		"beads/./" + b.ID,
		"beads/%2F",
		"nothing/here",
		"links/" + b.ID, // a bead id under links/
	} {
		f.get(target).problem(t, 404, "resource-not-found")
	}
	// Authority in the request target is ignored; the Scope decides identity.
	for _, target := range []string{"//evil.invalid/acme/beads/" + b.ID, "http://evil.invalid/acme/beads/" + b.ID} {
		r := f.getRaw(target)
		if r.status != 200 || r.json(t)["id"] != scope+"beads/"+b.ID {
			t.Fatalf("%s: %d %s", target, r.status, r.body)
		}
	}
	// A control byte is rejected by the transport: 400 with no body.
	for _, suffix := range []string{"\x01", "\xc3(", "\xc3\xa9"} {
		if r := f.getRaw("/acme/beads/" + b.ID + suffix); r.status != 400 || len(r.body) != 0 {
			t.Fatalf("raw octets %q: %d %q", suffix, r.status, r.body)
		}
	}
	f.get("beads/?bogus=1").problem(t, 400, "invalid-parameter")
	f.get("beads/?limit=1&limit=2").problem(t, 400, "invalid-parameter")
	f.get("beads/?limit=0").problem(t, 400, "invalid-parameter")
	f.get("beads/?limit=6").problem(t, 413, "limit-exceeded")
	f.get("beads/"+b.ID+"?view=nope").problem(t, 400, "invalid-parameter")
	f.get("beads/missing?view=nope").problem(t, 400, "invalid-parameter") // parameters before lookup
	f.get("beads/?type=not-a-url").problem(t, 400, "invalid-parameter")
	f.get("beads/?x="+strings.Repeat("a", 600)).problem(t, 413, "request-too-large")
}

func TestLinksFollowBDPDirection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	blocker := f.bead("API", "task")
	waiting := f.bead("UI", "task")
	if err := f.st.AddDep(ctx, blocker.ID, waiting.ID, "blocks", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddDep(ctx, blocker.ID, waiting.ID, "related", "tester"); err != nil {
		t.Fatal(err)
	}
	links := f.get("links/?type=" + url.QueryEscape("https://work.example/types/blocks")).json(t)["items"].([]any)
	if len(links) != 1 {
		t.Fatalf("blocks links = %v", links)
	}
	l := links[0].(map[string]any)
	// "UI depends on API": source is the waiting bead, target its blocker.
	if l["source"] != scope+"beads/"+waiting.ID || l["target"] != scope+"beads/"+blocker.ID || l["revision"] == "" {
		t.Fatalf("link = %v", l)
	}
	if r := f.get(strings.TrimPrefix(l["id"].(string), scope)); r.json(t)["id"] != l["id"] {
		t.Fatalf("link GET: %s", r.body)
	}

	out := f.get("beads/" + waiting.ID + "?view=links&direction=outbound").json(t)["items"].([]any)
	in := f.get("beads/" + waiting.ID + "?view=links&direction=inbound").json(t)["items"].([]any)
	if len(out) != 2 || len(in) != 0 {
		t.Fatalf("outbound %d inbound %d", len(out), len(in))
	}
	f.get("beads/"+waiting.ID+"?view=links&direction=sideways").problem(t, 400, "invalid-parameter")

	// Endpoint filters accept local ids and absolute URLs alike.
	for _, src := range []string{"beads/" + waiting.ID, scope + "beads/" + waiting.ID} {
		if n := len(f.get("links/?source=" + url.QueryEscape(src)).json(t)["items"].([]any)); n != 2 {
			t.Fatalf("source=%s matched %d", src, n)
		}
	}
	f.get("links/?source="+url.QueryEscape(scope+"beads/%79d-1")).problem(t, 400, "invalid-parameter")

	// The local kind gets a Scope-local Type with a descriptor.
	desc := f.get("types/related").json(t)
	if desc["describes"] != "link" || desc["source"] == nil {
		t.Fatalf("descriptor = %v", desc)
	}
	// conformsTo follows the reference chain blocks -> dependency ->
	// relationship; a minted local kind claims no conformance (as in bd).
	rel := url.QueryEscape("https://work.example/types/relationship")
	if n := len(f.get("links/?conformsTo=" + rel).json(t)["items"].([]any)); n != 1 {
		t.Fatalf("conformsTo relationship matched %d, want only the blocks link", n)
	}
}

func TestPaginationIsOneSnapshot(t *testing.T) {
	f := newFixture(t)
	var beads []*store.Bead
	for _, title := range []string{"a", "b", "c", "d", "e"} {
		beads = append(beads, f.bead(title, "task"))
	}
	first := f.get("beads/").json(t) // default page size 2
	items := first["items"].([]any)
	next, _ := first["next"].(string)
	if len(items) != 2 || next == "" {
		t.Fatalf("first page = %v", first)
	}
	var all []string
	for _, it := range items {
		all = append(all, it.(map[string]any)["id"].(string))
	}
	// Change the yard mid-listing: later pages still show the old state.
	// Handles are random, so pick a bead the first page did not list.
	later := beads[0]
	for _, b := range beads {
		if !slices.Contains(all, scope+"beads/"+b.ID) {
			later = b
		}
	}
	title := "changed"
	if _, err := f.st.UpdateBead(context.Background(), later.ID, store.BeadPatch{Title: &title}, store.Expect{}, "x"); err != nil {
		t.Fatal(err)
	}
	f.bead("f", "task")

	for next != "" {
		page := f.get(next).json(t)
		for _, it := range page["items"].([]any) {
			m := it.(map[string]any)
			all = append(all, m["id"].(string))
			if m["properties"].(map[string]any)["title"] == "changed" {
				t.Fatal("a later page saw a change made after the listing started")
			}
		}
		next, _ = page["next"].(string)
	}
	if len(all) != 5 {
		t.Fatalf("listed %d beads, want the 5 of the snapshot: %v", len(all), all)
	}
	for i := 1; i < len(all); i++ {
		if bdp.CompareIDs(all[i-1], all[i]) >= 0 {
			t.Fatalf("not in canonical order: %v", all)
		}
	}

	f.get("beads/?cursor=nonexistent").problem(t, 410, "cursor-expired")
	// Reusing a cursor with different parameters is invalid.
	cur := f.get("beads/").json(t)["next"].(string)
	f.get(cur+"&limit=3").problem(t, 400, "invalid-parameter")
}

func TestSelector(t *testing.T) {
	f := newFixture(t)
	f.bead("open one", "task")
	b := f.bead("closed one", "bug")
	if _, err := f.st.CloseBead(context.Background(), b.ID, "done", store.Expect{}, "x"); err != nil {
		t.Fatal(err)
	}
	sel := func(s string) string { return "beads/?limit=5&selector=" + url.QueryEscape(s) }
	got := f.get(sel(`$[?@.properties.status == 'closed']`)).json(t)["items"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["id"] != scope+"beads/"+b.ID {
		t.Fatalf("status == closed: %v", got)
	}
	cases := map[string]int{
		`$[?@.properties.priority >= 0 && !(@.properties.status == 'closed')]`:               1,
		`$[?@.type == 'https://work.example/types/bug' || @.properties.title == 'open one']`: 2,
		`$[?@.properties.close_reason]`:                                                      1, // existence
		`$[?@.properties.labels[0]]`:                                                         0,
		`$[?@.properties.missing == null]`:                                                   0, // Nothing is not null
	}
	for s, want := range cases {
		if n := len(f.get(sel(s)).json(t)["items"].([]any)); n != want {
			t.Errorf("%s matched %d, want %d", s, n, want)
		}
	}
	for _, bad := range []string{`$[?@.a == @.b]`, `$[?length(@.a) > 1]`, `$..x`, `$[?@..x]`, `$[?'lit']`} {
		f.get(sel(bad)).problem(t, 400, "invalid-parameter")
	}
	f.get(sel(`$[?@.a.b.c.d.e == 1]`)).problem(t, 413, "limit-exceeded")
}

func TestCompareIDsUsesUTF16(t *testing.T) {
	// U+FF5E (BMP) sorts after U+1F600 in UTF-16 (surrogate 0xD83D) but
	// before it in UTF-8 byte order.
	if bdp.CompareIDs("a\U0001F600", "a～") >= 0 {
		t.Fatal("supplementary character must sort before U+FF5E by code unit")
	}
	if bdp.CompareIDs("a", "b") >= 0 || bdp.CompareIDs("b", "b") != 0 {
		t.Fatal("ascii order")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// countingSource counts the snapshots CachedLoader takes.
type countingSource struct {
	store.API
	snapshots int
}

func (c *countingSource) Snapshot(ctx context.Context) (*store.Snapshot, error) {
	c.snapshots++
	return c.API.Snapshot(ctx)
}

// Requests share one built graph until the store's head moves.
func TestCachedLoaderSnapshotsOnlyWhenTheHeadMoves(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "one"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	src := &countingSource{API: st}
	srv, err := bdp.NewServer(scope, bdp.CachedLoader(scope, src), bdp.DefaultLimits, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	get := func(path string) {
		t.Helper()
		r, err := http.Get(ts.URL + "/acme/" + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("GET %s: %d", path, r.StatusCode)
		}
	}
	get("beads/" + b.ID)
	get("beads/" + b.ID + "?view=properties")
	if src.snapshots != 1 {
		t.Fatalf("%d snapshots across two requests, want 1", src.snapshots)
	}
	b2, err := st.CreateBead(ctx, store.NewBead{Title: "two"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	get("beads/" + b2.ID)
	if src.snapshots != 2 {
		t.Fatalf("%d snapshots after a write, want 2", src.snapshots)
	}
}

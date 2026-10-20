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

// sources are the sources a bead can link to, as yardr bdp serve hands them
// (hooks.LinkSources).
var sources = []string{"github", "linear"}

type fixture struct {
	t  *testing.T
	st store.API
	ts *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	st := storetest.New(t)
	srv, err := bdp.NewServer(scope, sources, func(ctx context.Context) (*bdp.Graph, error) {
		snap, err := st.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		return bdp.BuildGraph(scope, sources, snap), nil
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
		if strings.EqualFold(headers[i], "Host") {
			// The client sends the Host header from the request's field,
			// never from its headers.
			req.Host = headers[i+1]
			continue
		}
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
	// A train is the reference vocabulary's epic, as it was served when
	// yardr called it one, and bd's: issue_type says the same word, so a
	// client that groups on it finds a yard's trains.
	train := f.bead("the feature", store.TypeTrain)
	if rec := f.get("beads/" + train.ID).json(t); rec["type"] != "https://work.example/types/epic" ||
		rec["properties"].(map[string]any)["issue_type"] != "epic" {
		t.Fatalf("a train's record = %v", rec)
	}
	// A wagon is no reference type: it has one this yard mints, under the
	// word yardr calls it.
	wagon := f.bead("a piece of it", store.TypeWagon)
	if rec := f.get("beads/" + wagon.ID).json(t); rec["type"] != scope+"types/wagon" ||
		rec["properties"].(map[string]any)["issue_type"] != "wagon" {
		t.Fatalf("a wagon's record = %v", rec)
	}
	// BDP's record members as its schema has them since its common metadata
	// and revision provenance: no `attribution`, which it refuses now.
	if a, _ := rec["revisionProvenance"].(map[string]any)["actor"].(map[string]any); a["id"] != "tester" || a["basis"] != "writer-supplied" || len(a) != 2 {
		t.Fatalf("revisionProvenance = %v", rec["revisionProvenance"])
	}
	if m, ok := rec["metadata"].(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("metadata = %v, want {}", rec["metadata"])
	}
	if _, old := rec["attribution"]; old {
		t.Fatalf("record still carries attribution: %v", rec)
	}
	props := rec["properties"].(map[string]any)
	if props["status"] != "open" || props["issue_type"] != "task" || props["title"] != "Write the spec" || !strings.HasSuffix(props["created_at"].(string), "Z") {
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
	if err := f.st.AddDep(ctx, blocker.ID, waiting.ID, "blocks", "", "", "tester"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddDep(ctx, blocker.ID, waiting.ID, "related", "", "", "tester"); err != nil {
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

// A discovered-from edge is served as bd's: the found bead is the source and
// "depends on" its origin, in a Scope-local Type (BDP's reference vocabulary
// has none) that conforms to nothing, so no client takes it for a blocker.
func TestDiscoveredFromLink(t *testing.T) {
	f := newFixture(t)
	origin := f.bead("API", "task")
	found, err := f.st.CreateBead(context.Background(), store.NewBead{Title: "a leak", DiscoveredFrom: origin.ID}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	typ := scope + "types/discovered-from"
	links := f.get("links/?type=" + url.QueryEscape(typ)).json(t)["items"].([]any)
	if len(links) != 1 {
		t.Fatalf("discovered-from links = %v", links)
	}
	l := links[0].(map[string]any)
	if l["type"] != typ || l["source"] != scope+"beads/"+found.ID || l["target"] != scope+"beads/"+origin.ID {
		t.Fatalf("link = %v, want source the found bead and target its origin", l)
	}
	desc := f.get("types/discovered-from").json(t)
	if desc["describes"] != "link" || len(desc["conformsTo"].([]any)) != 0 || !strings.Contains(desc["description"].(string), "never blocks") {
		t.Fatalf("descriptor = %v", desc)
	}
	for _, c := range []string{"dependency", "relationship", "blocks"} {
		q := url.QueryEscape("https://work.example/types/" + c)
		if n := len(f.get("links/?conformsTo=" + q).json(t)["items"].([]any)); n != 0 {
			t.Fatalf("conformsTo %s matched %d links, want none", c, n)
		}
	}
	// bd's row of the found bead lists the edge among its dependencies.
	props := f.get("beads/" + found.ID).json(t)["properties"].(map[string]any)
	deps := props["dependencies"].([]any)
	if len(deps) != 1 {
		t.Fatalf("dependencies = %v", deps)
	}
	if d := deps[0].(map[string]any); d["issue_id"] != found.ID || d["depends_on_id"] != origin.ID || d["type"] != "discovered-from" {
		t.Fatalf("dependency row = %v", d)
	}
}

// Journals and memories are open beads in the store, but not work: a bd-style
// client computes readiness from status and blockers, so serving them would
// list a crew member's journal as ready. They are not served, nor their links.
func TestJournalsAndMemoriesAreNotServed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	work := f.bead("real work", "task")
	journal := f.bead("Journal of yardmaster", store.TypeJournal)
	memory := f.bead("how we deploy", store.TypeMemory)
	if err := f.st.AddDep(ctx, work.ID, memory.ID, "related", "", "", "tester"); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for next := "beads/?limit=5"; next != ""; {
		page := f.get(next).json(t)
		for _, it := range page["items"].([]any) {
			ids = append(ids, it.(map[string]any)["id"].(string))
		}
		next, _ = page["next"].(string)
	}
	if !slices.Equal(ids, []string{scope + "beads/" + work.ID}) {
		t.Fatalf("beads served = %v, want only the task", ids)
	}
	for _, id := range []string{journal.ID, memory.ID} {
		f.get("beads/"+id).problem(t, 404, "resource-not-found")
	}
	if links := f.get("links/").json(t)["items"].([]any); len(links) != 0 {
		t.Fatalf("links to a memory served: %v", links)
	}
	props := f.get("beads/" + work.ID + "?view=properties").json(t)
	if props["dependent_count"] != float64(0) {
		t.Fatalf("task counts the memory as a dependent: %v", props)
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
		// Common metadata is a member of the candidate, and empty.
		`$[?@.metadata]`:   2,
		`$[?@.metadata.x]`: 0,
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
	srv, err := bdp.NewServer(scope, sources, bdp.CachedLoader(scope, sources, src), bdp.DefaultLimits, nil)
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

// A bead's link to an outside issue is a Link from the bead to the issue's
// URL, an out-of-Scope endpoint, typed relates or tracks-<source>; every
// tracks-<source> conforms to tracks. Discovery advertises that a bead
// tracks at most one issue per source, and nothing about the issue's end.
func TestIssueLinks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const lin, gh, other = "https://linear.app/acme/issue/YRD-1", "https://github.com/acme/webapp/issues/7", "https://linear.app/acme/issue/YRD-2"
	one, two := f.bead("API", "task"), f.bead("UI", store.TypeTrain)
	journal := f.bead("yardmaster's journal", store.TypeJournal)
	for _, l := range []struct {
		bead string
		nl   store.NewLink
	}{
		{one.ID, store.NewLink{Kind: store.LinkTracks, Source: "linear", URL: lin, Thread: "issue-1"}},
		{one.ID, store.NewLink{Kind: store.LinkTracks, Source: "github", URL: gh}},
		{two.ID, store.NewLink{Kind: store.LinkRelates, Source: "linear", URL: lin}},
		{journal.ID, store.NewLink{Kind: store.LinkRelates, Source: "linear", URL: other}},
	} {
		if _, err := f.st.AddLink(ctx, l.bead, l.nl, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	items := func(target string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for next := any(target); next != nil; {
			page := f.get(strings.TrimPrefix(next.(string), scope)).json(t)
			for _, it := range page["items"].([]any) {
				out = append(out, it.(map[string]any))
			}
			next = page["next"]
		}
		return out
	}
	q := url.QueryEscape

	// Every tracked issue, whatever its source, through the parent Type.
	tracked := items("links/?conformsTo=" + q(scope+"types/tracks"))
	if len(tracked) != 2 {
		t.Fatalf("conformsTo tracks = %v", tracked)
	}
	byType := map[string]map[string]any{}
	for _, l := range tracked {
		byType[l["type"].(string)] = l
	}
	l := byType[scope+"types/tracks-linear"]
	if l == nil || l["source"] != scope+"beads/"+one.ID || l["target"] != lin || l["revision"] == "" ||
		!strings.HasPrefix(l["id"].(string), scope+"links/") {
		t.Fatalf("linear tracks link = %v (of %v)", l, tracked)
	}
	if p := l["properties"].(map[string]any); p["source"] != "linear" || p["thread"] != "issue-1" || len(p) != 2 {
		t.Fatalf("properties = %v", p)
	}
	if a, _ := l["revisionProvenance"].(map[string]any)["actor"].(map[string]any); a["id"] != "tester" || a["basis"] != "writer-supplied" {
		t.Fatalf("revisionProvenance = %v", l["revisionProvenance"])
	}
	if m, ok := l["metadata"].(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("metadata = %v, want {}", l["metadata"])
	}
	g := byType[scope+"types/tracks-github"]
	if g == nil || g["target"] != gh {
		t.Fatalf("github tracks link = %v", g)
	}
	// The thread id is there only when known.
	if p := g["properties"].(map[string]any); p["source"] != "github" || len(p) != 1 {
		t.Fatalf("properties = %v", p)
	}
	if r := f.get(strings.TrimPrefix(l["id"].(string), scope)); r.json(t)["target"] != lin || r.header.Get("ETag") == "" {
		t.Fatalf("link GET: %s", r.body)
	}

	// The issue's URL is an endpoint to filter on, compared as given; the
	// journal's link is not served, as the journal is not.
	if got := items("links/?target=" + q(lin)); len(got) != 2 {
		t.Fatalf("target=%s matched %v", lin, got)
	}
	rel := items("links/?type=" + q(scope+"types/relates"))
	if len(rel) != 1 || rel[0]["source"] != scope+"beads/"+two.ID || rel[0]["target"] != lin {
		t.Fatalf("relates links = %v", rel)
	}
	if got := items("links/?target=" + q(other)); len(got) != 0 {
		t.Fatalf("a journal's link is served: %v", got)
	}
	if got := items("links/?type=" + q(scope+"types/tracks")); len(got) != 0 {
		t.Fatalf("a link is typed plain tracks: %v", got)
	}
	// A bead's links view has its issue links, outbound.
	if out, in := items("beads/"+one.ID+"?view=links&direction=outbound"), items("beads/"+one.ID+"?view=links&direction=inbound"); len(out) != 2 || len(in) != 0 {
		t.Fatalf("outbound %v inbound %v", out, in)
	}

	// The Types resolve, linked to or not, and are in the inventory.
	for name, parents := range map[string]int{"tracks": 0, "relates": 0, "tracks-linear": 1, "tracks-github": 1} {
		d := f.get("types/" + name).json(t)
		c := d["conformsTo"].([]any)
		if d["id"] != scope+"types/"+name || d["describes"] != "link" || len(c) != parents || d["source"] == nil || d["target"] == nil {
			t.Fatalf("descriptor %s = %v", name, d)
		}
		if parents == 1 && c[0] != scope+"types/tracks" {
			t.Fatalf("%s conforms to %v", name, c)
		}
	}
	empty := newFixture(t)
	if d := empty.get("types/tracks-linear").json(t); d["id"] != scope+"types/tracks-linear" {
		t.Fatalf("descriptor on a yard with no link = %v", d)
	}
	inventory := map[string]bool{}
	for _, ty := range items("types/") {
		inventory[ty["id"].(string)] = true
	}
	for _, name := range []string{"tracks", "relates", "tracks-linear", "tracks-github"} {
		if !inventory[scope+"types/"+name] {
			t.Fatalf("types/ lacks %s: %v", name, inventory)
		}
	}

	// One maximum per source, each on the bead's end; none on the issue's.
	max := empty.get("bdp.json").json(t)["maximumEndpointMultiplicity"].([]any)
	got := map[string]bool{}
	for _, m := range max {
		m := m.(map[string]any)
		if m["endpoint"] != "source" || m["max"] != float64(1) || len(m) != 3 {
			t.Fatalf("maximum = %v", m)
		}
		got[m["linkConformsTo"].(string)] = true
	}
	if len(max) != 2 || !got[scope+"types/tracks-linear"] || !got[scope+"types/tracks-github"] {
		t.Fatalf("maximumEndpointMultiplicity = %v", max)
	}
	// What is advertised holds of what is served.
	perBead := map[string]int{}
	for _, l := range items("links/?conformsTo=" + q(scope+"types/tracks-linear")) {
		perBead[l["source"].(string)]++
	}
	for bead, n := range perBead {
		if n > 1 {
			t.Fatalf("%s is the source of %d tracks-linear links", bead, n)
		}
	}
}

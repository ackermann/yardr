package web_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

func TestMain(m *testing.M) { storetest.Main(m) }

type fixture struct {
	t  *testing.T
	st store.API
	ts *httptest.Server
}

// newFixture serves a yard whose home is .../.depot and that has no `name`
// setting, so its name is depot.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := storetest.New(t)
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &fixture{t: t, st: st, ts: ts}
}

type resp struct {
	status int
	header http.Header
	body   string
}

func (f *fixture) do(method, target string, headers ...string) resp {
	f.t.Helper()
	req, err := http.NewRequest(method, f.ts.URL+target, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
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
	return resp{r.StatusCode, r.Header, string(b)}
}

func (f *fixture) get(target string, headers ...string) resp {
	f.t.Helper()
	return f.do(http.MethodGet, target, headers...)
}

// populate gives the yard two rigs: line, with a flow of its own and one
// for its bug beads, and loose, with no flow.
//
//	line   task beads: backlog 2 (one held), new 1; a closed one, not counted
//	       bug beads:  triage 1
//	loose  one bead in stage inbox
func (f *fixture) populate() {
	f.t.Helper()
	ctx := context.Background()
	must := func(err error) {
		f.t.Helper()
		if err != nil {
			f.t.Fatal(err)
		}
	}
	for _, rig := range []string{"line", "loose"} {
		must(f.st.PutRig(ctx, store.Rig{Name: rig, Base: "origin/main"}, "me"))
	}
	flow := func(name, typ, text string) {
		f.t.Helper()
		fl, err := store.ParseFlow(text)
		must(err)
		fl.Name = name
		_, err = f.st.PutFlow(ctx, *fl, "me")
		must(err)
		must(f.st.SetFlowPointer(ctx, "line", typ, name, "me"))
	}
	flow("code", "", "backlog -> new\nnew -> review | backlog\nreview -> merged | new\nmerged terminal\n")
	flow("bugs", "bug", "triage -> fixed\nfixed terminal\n")
	bead := func(rig, typ, stage, title string) *store.Bead {
		f.t.Helper()
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: title, Type: typ, Stage: stage, Rig: rig}, "me")
		must(err)
		return b
	}
	bead("line", "task", "backlog", "one")
	held := bead("line", "task", "backlog", "two")
	bead("line", "task", "new", "three")
	closed := bead("line", "task", "new", "four")
	bead("line", "bug", "triage", "five")
	bead("loose", "task", "inbox", "six")
	_, err := f.st.SetHold(ctx, held.ID, true, store.Expect{}, "me")
	must(err)
	_, err = f.st.CloseBead(ctx, closed.ID, "dropped", store.Expect{}, "me")
	must(err)
}

var wantHome = web.Home{Yard: "depot", Rigs: []web.HomeRig{
	{Name: "line", Kind: store.RigGit, Open: 4, Held: 1, Flows: []web.HomeFlow{
		{Name: "code", Stages: []web.StageCount{{Stage: "backlog", Open: 2}, {Stage: "new", Open: 1}, {Stage: "review"}}},
		{Name: "bugs", Type: "bug", Stages: []web.StageCount{{Stage: "triage", Open: 1}}},
	}},
	{Name: "loose", Kind: store.RigGit, Open: 1, Flows: []web.HomeFlow{},
		Elsewhere: []web.StageCount{{Stage: "inbox", Open: 1}}},
}}

// The home page as JSON: the yard's name, its rigs, and per rig the open
// beads per stage of its flows. A terminal stage holds no open bead and is
// left out; a closed bead is not counted.
func TestHomeJSON(t *testing.T) {
	f := newFixture(t)
	f.populate()
	r := f.get("/", "Accept", "application/json")
	if r.status != 200 || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, type %q: %s", r.status, r.header.Get("Content-Type"), r.body)
	}
	var got web.Home
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	// The feed has tests of its own (feed_test.go).
	if len(got.Events) == 0 || got.Head == 0 {
		t.Errorf("home has %d events and head %d, want the feed", len(got.Events), got.Head)
	}
	got.Feed = web.Feed{}
	if !reflect.DeepEqual(got, wantHome) {
		t.Errorf("home\n got %+v\nwant %+v", got, wantHome)
	}
	if v := r.header.Get("Vary"); v != "Accept" {
		t.Errorf("Vary = %q: one URL answers two ways, a cache must know", v)
	}
}

// The same request without the header is the page, showing the same data.
func TestHomeHTML(t *testing.T) {
	f := newFixture(t)
	f.populate()
	r := f.get("/")
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	text := squash(r.body)
	for _, want := range []string{
		"<title>Home · depot</title>",
		`<a class="yard" href="/">depot</a>`,
		"<h2>line</h2>", "<h2>loose</h2>",
		"flow code</h3>", "flow bugs · bug beads</h3>",
		`<span class="num">2</span><span class="stage">backlog</span>`,
		`<span class="num">1</span><span class="stage">new</span>`,
		`<li class="zero"><span class="num">0</span><span class="stage">review</span>`,
		`<span class="num">1</span><span class="stage">triage</span>`,
		"4 open", `1 held`,
		"off the flow", `<span class="num">1</span><span class="stage">inbox</span>`,
		`<a href="/" aria-current="page">Home</a>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(text, "merged") {
		t.Error("the page shows the terminal stage merged, which holds no open bead")
	}
}

// squash drops the whitespace between tags, so a test reads the markup and
// not its indentation.
func squash(html string) string {
	return regexp.MustCompile(`>\s+<`).ReplaceAllString(html, "><")
}

// The yard's name is the `name` setting when there is one.
func TestYardNameSetting(t *testing.T) {
	f := newFixture(t)
	if err := f.st.SetSetting(context.Background(), "name", "north", "me"); err != nil {
		t.Fatal(err)
	}
	var got web.Home
	if err := json.Unmarshal([]byte(f.get("/", "Accept", "application/json").body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Yard != "north" || len(got.Rigs) != 0 || got.Rigs == nil {
		t.Errorf("got %+v, want yard north and an empty list of rigs", got)
	}
	if r := f.get("/"); !strings.Contains(r.body, "This yard has no rig yet.") {
		t.Errorf("an empty yard's page does not say so: %s", r.body)
	}
}

// A rig whose repository flows cannot be read is still shown, and must not
// hide the other rigs. Its open beads are counted under "off the flow".
type failingRigFlows struct {
	store.API
	rig string
}

func (s failingRigFlows) RigFlows(ctx context.Context, rig string) ([]store.RigFlow, error) {
	if rig == s.rig {
		return nil, errors.New("repository is unavailable")
	}
	return s.API.RigFlows(ctx, rig)
}

func TestFlowReadErrorKeepsOtherRigs(t *testing.T) {
	base := storetest.New(t)
	ctx := context.Background()
	for _, name := range []string{"broken", "healthy"} {
		if err := base.PutRig(ctx, store.Rig{Name: name, Base: "origin/main"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, rig := range []string{"broken", "healthy"} {
		if _, err := base.CreateBead(ctx, store.NewBead{Title: rig + " bead", Type: "task", Stage: "new", Rig: rig}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := web.NewServer(failingRigFlows{API: base, rig: "broken"}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	req.Header.Set("Accept", "application/json")
	srv.ServeHTTP(r, req)
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body)
	}
	var got web.Home
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rigs) != 2 {
		t.Fatalf("got rigs %+v, want broken and healthy", got.Rigs)
	}
	broken := got.Rigs[0]
	if broken.Name != "broken" || broken.Error != "repository is unavailable" || len(broken.Flows) != 0 ||
		!reflect.DeepEqual(broken.Elsewhere, []web.StageCount{{Stage: "new", Open: 1}}) {
		t.Errorf("broken rig = %+v", broken)
	}
	healthy := got.Rigs[1]
	if healthy.Name != "healthy" || healthy.Error != "" || healthy.Open != 1 ||
		!reflect.DeepEqual(healthy.Elsewhere, []web.StageCount{{Stage: "new", Open: 1}}) {
		t.Errorf("healthy rig = %+v", healthy)
	}
}

// Which of the two a request gets: HTML unless it names JSON above HTML.
func TestNegotiation(t *testing.T) {
	f := newFixture(t)
	for accept, wantJSON := range map[string]bool{
		"":    false,
		"*/*": false, // curl
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8": false, // a browser
		"application/json":                  true,
		"application/json, text/plain, */*": true,
		"application/*":                     true,
		"text/html;q=0.5, application/json": true,
		"application/json;q=0.5, text/html": false,
		"application/json;q=0":              false,
		"application/json;q=x":              false,
		"APPLICATION/JSON ; Q=1.0":          true,
	} {
		var headers []string
		if accept != "" {
			headers = []string{"Accept", accept}
		}
		r := f.get("/", headers...)
		if got := r.header.Get("Content-Type") == "application/json"; got != wantJSON || r.status != 200 {
			t.Errorf("Accept %q: status %d, JSON %v, want %v", accept, r.status, got, wantJSON)
		}
	}
}

// The line page is the right column's data, for a window without the column.
func TestLine(t *testing.T) {
	f := newFixture(t)
	f.populate()
	var got web.Line
	if err := json.Unmarshal([]byte(f.get("/line", "Accept", "application/json").body), &got); err != nil {
		t.Fatal(err)
	}
	want := web.Line{Open: 5, Held: 1, Rigs: []web.LineRig{{Name: "line", Open: 4}, {Name: "loose", Open: 1}},
		Stages: []web.StageCount{{Stage: "backlog", Open: 2}, {Stage: "new", Open: 1}, {Stage: "triage", Open: 1}, {Stage: "inbox", Open: 1}}}
	// What waits has tests of its own (line_test.go).
	if len(got.Waits) != 1 {
		t.Errorf("%d beads wait, want the held one: %+v", len(got.Waits), got.Waits)
	}
	got.Waits = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("line\n got %+v\nwant %+v", got, want)
	}
	// Every page carries it in the right column, the unbuilt ones too.
	for _, path := range []string{"/", "/line", "/beads"} {
		if text := squash(f.get(path).body); !strings.Contains(text, `<dt>open</dt><dd>5</dd>`) {
			t.Errorf("%s does not show the line's 5 open beads", path)
		}
	}
}

// Every menu entry answers; the pages later children add say so, in both
// forms, under a status no client takes for an empty answer.
func TestMenuPages(t *testing.T) {
	f := newFixture(t)
	links := regexp.MustCompile(`<li[^>]*><a href="([^"]+)"`).FindAllStringSubmatch(f.get("/").body, -1)
	if len(links) != 6 {
		t.Fatalf("the menu has %d entries, want 6: %v", len(links), links)
	}
	for _, l := range links {
		path := l[1]
		r := f.get(path)
		switch path {
		case "/", "/line", "/sessions", "/residents":
			if r.status != 200 {
				t.Errorf("%s: status %d", path, r.status)
			}
		default:
			if r.status != http.StatusNotImplemented || !strings.Contains(r.body, "Not built yet.") {
				t.Errorf("%s: status %d, want 501 and the not-built page", path, r.status)
			}
			j := f.get(path, "Accept", "application/json")
			var got web.Unbuilt
			if err := json.Unmarshal([]byte(j.body), &got); err != nil || j.status != http.StatusNotImplemented ||
				got != (web.Unbuilt{Page: strings.TrimPrefix(path, "/")}) {
				t.Errorf("%s as JSON: status %d, %+v (%v)", path, j.status, got, err)
			}
		}
		if !strings.Contains(r.body, `<a href="`+path+`" aria-current="page">`) {
			t.Errorf("%s does not mark its own menu entry", path)
		}
	}
}

func TestNotFound(t *testing.T) {
	f := newFixture(t)
	if r := f.get("/nowhere"); r.status != 404 || !strings.Contains(r.body, "no page at /nowhere") || !strings.Contains(r.body, `class="menu"`) {
		t.Errorf("status %d, want 404 inside the shell: %s", r.status, r.body)
	}
	r := f.get("/nowhere", "Accept", "application/json")
	var got map[string]string
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 404 || got["error"] != "no page at /nowhere" {
		t.Errorf("JSON: status %d, %v (%v)", r.status, got, err)
	}
	if r := f.get("/favicon.ico"); r.status != http.StatusNoContent {
		t.Errorf("/favicon.ico: status %d, want 204: the view has no image and a browser asks anyway", r.status)
	}
	if r := f.get("/assets/README.md"); r.status != 404 {
		t.Errorf("the assets' README is served (status %d): it is not an asset", r.status)
	}
}

// The view reads the yard and changes nothing: no page takes a write.
func TestReadOnly(t *testing.T) {
	f := newFixture(t)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/", "/line", "/beads", "/assets/yard.css", "/nowhere"} {
			if r := f.do(method, path); r.status != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status %d, want 405", method, path, r.status)
			}
		}
	}
}

// An address off the loopback interface is refused, and the message says
// why: there is no authentication yet.
func TestRefusedAddress(t *testing.T) {
	for _, addr := range []string{":8791", "0.0.0.0:8791", "192.168.1.10:8791", "[::]:8791", "example.com:80", "[2001:db8::1]:80"} {
		err := web.CheckLoopback(addr)
		if err == nil {
			t.Errorf("%s: accepted", addr)
			continue
		}
		for _, want := range []string{addr, "refused", "no authentication", "loopback"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal lacks %q: %v", addr, want, err)
			}
		}
		if ln, err := web.Listen(addr); err == nil {
			ln.Close()
			t.Errorf("%s: Listen opened it", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8791", "127.0.0.1:0", "[::1]:8791", "localhost:8791", "127.8.8.8:1"} {
		if err := web.CheckLoopback(addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if err := web.CheckLoopback("127.0.0.1"); err == nil || strings.Contains(err.Error(), "refused") {
		t.Errorf("an address with no port: %v, want the parse error", err)
	}
	ln, err := web.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

// A request that reached the loopback listener under another host's name
// (DNS rebinding from a page elsewhere) is refused before any page is built.
func TestForeignHostRefused(t *testing.T) {
	f := newFixture(t)
	f.populate()
	for _, host := range []string{"evil.example", "evil.example:8791", "127.0.0.1.evil.example", "10.0.0.1:8791"} {
		r := f.get("/", "Host", host, "Accept", "application/json")
		if r.status != http.StatusMisdirectedRequest || strings.Contains(r.body, "loose") {
			t.Errorf("Host %s: status %d, body %q", host, r.status, r.body)
		}
	}
	for _, host := range []string{"localhost:8791", "localhost", "LOCALHOST:1", "127.0.0.1:8791", "[::1]:8791"} {
		if r := f.get("/", "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d", host, r.status)
		}
	}
}

var otherHost = regexp.MustCompile(`(?i)\b(?:https?:)?//[^\s"'<>()\\]+`)

// The page loads with the network otherwise off: nothing the server sends,
// HTML, CSS or JS, names a URL on another host, and the policy it sends
// keeps the browser to this one.
func TestNoOtherHost(t *testing.T) {
	f := newFixture(t)
	f.populate()
	self, err := url.Parse(f.ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	queue := []string{"/", "/nowhere"}
	ref := regexp.MustCompile(`(?:href|src)="([^"]+)"`)
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		seen[path] = true
		r := f.get(path)
		ctype := r.header.Get("Content-Type")
		for _, m := range otherHost.FindAllString(r.body, -1) {
			// "//" also opens a comment in the scripts; a URL has a host.
			u, err := url.Parse(m)
			if strings.HasPrefix(m, "//") && (err != nil || !strings.Contains(u.Host, ".")) {
				continue
			}
			if err != nil || u.Host != self.Host {
				t.Errorf("%s (%s) names another host: %s", path, ctype, m)
			}
		}
		if !strings.HasPrefix(ctype, "text/html") {
			continue
		}
		if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: Content-Security-Policy %q does not keep the browser to this host", path, csp)
		}
		if strings.Contains(r.body, "<style") || strings.Contains(r.body, " style=") || regexp.MustCompile(`<script(?:\s+defer)?>`).MatchString(r.body) {
			t.Errorf("%s has inline style or script, which the policy refuses", path)
		}
		for _, m := range ref.FindAllStringSubmatch(r.body, -1) {
			if !strings.HasPrefix(m[1], "/") || strings.HasPrefix(m[1], "//") {
				t.Errorf("%s links to %q, not a path on this server", path, m[1])
				continue
			}
			queue = append(queue, strings.ReplaceAll(m[1], "&amp;", "&"))
		}
	}
	var assets, pages int
	for path := range seen {
		if strings.HasPrefix(path, "/assets/") {
			assets++
		} else {
			pages++
		}
	}
	// The walk must have reached what it is there to check.
	if assets != 3 || pages != 7 {
		t.Errorf("walked %d assets and %d pages, want 3 and 7: %v", assets, pages, seen)
	}
}

// Assets are served with their type and a checksum in the URL that lets a
// browser keep them for good.
func TestAssets(t *testing.T) {
	f := newFixture(t)
	page := f.get("/").body
	for name, ctype := range map[string]string{
		"yard.css":            "text/css; charset=utf-8",
		"htmx.min.js":         "text/javascript; charset=utf-8",
		"htmx-ext-sse.min.js": "text/javascript; charset=utf-8",
	} {
		m := regexp.MustCompile(`"(/assets/` + regexp.QuoteMeta(name) + `\?v=[0-9a-f]{12})"`).FindStringSubmatch(page)
		if m == nil {
			t.Errorf("the page does not load %s by a versioned URL", name)
			continue
		}
		r := f.get(m[1])
		if r.status != 200 || r.header.Get("Content-Type") != ctype || !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: status %d, type %q, cache %q", m[1], r.status, r.header.Get("Content-Type"), r.header.Get("Cache-Control"))
		}
		disk, err := os.ReadFile(filepath.Join("assets", name))
		if err != nil || r.body != string(disk) {
			t.Errorf("%s is not the file in assets/ (%v)", name, err)
		}
		// Without the version it may be any binary's: the browser asks again.
		if r := f.get("/assets/" + name); r.status != 200 || r.header.Get("Cache-Control") != "no-cache" {
			t.Errorf("/assets/%s unversioned: status %d, cache %q", name, r.status, r.header.Get("Cache-Control"))
		}
		if r := f.get(m[1], "If-None-Match", r.header.Get("ETag")); r.status != http.StatusNotModified {
			t.Errorf("%s with its ETag: status %d, want 304", name, r.status)
		}
	}
}

// The vendored scripts are the files assets/README.md says they are.
func TestVendoredAssets(t *testing.T) {
	readme, err := os.ReadFile("assets/README.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile("(?m)^\\| `([^`]+\\.js)` \\|.*\\| `([0-9a-f]{64})` \\|$").FindAllStringSubmatch(string(readme), -1)
	if len(rows) != 2 {
		t.Fatalf("the README pins %d scripts, want 2", len(rows))
	}
	for _, row := range rows {
		body, err := os.ReadFile(filepath.Join("assets", row[1]))
		if err != nil {
			t.Error(err)
			continue
		}
		if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != row[2] {
			t.Errorf("%s: sha256 %x, the README says %s", row[1], sum, row[2])
		}
	}
	scripts, _ := filepath.Glob("assets/*.js")
	if len(scripts) != 2 {
		t.Errorf("assets/ has %d scripts, want the two the README pins: %v", len(scripts), scripts)
	}
}

// The stylesheet carries the epic's tokens, in both themes.
func TestTokens(t *testing.T) {
	css, err := os.ReadFile("assets/yard.css")
	if err != nil {
		t.Fatal(err)
	}
	dark, light, ok := strings.Cut(string(css), "@media (prefers-color-scheme: light)")
	if !ok {
		t.Fatal("no light theme")
	}
	light, _, _ = strings.Cut(light, "\n}\n")
	for theme, tokens := range map[*string]map[string]string{
		&dark: {"ground": "#17171a", "panel": "#1e1e22", "hairline": "#26262b", "hairline-strong": "#35353d",
			"ink": "#eae8ee", "dim": "#cdccd2", "faint": "#b0afb6", "accent": "#a9ceff", "landed": "#cfa548",
			"working": "var(--accent)", "running": "#6db58a", "held": "#e0735f", "done": "var(--faint)"},
		&light: {"ground": "#efece5", "panel": "#e7e3da", "ink": "#15140f", "accent": "#3c5777", "landed": "#8d6830"},
	} {
		for name, value := range tokens {
			if !strings.Contains(*theme, "--"+name+": "+value+";") {
				t.Errorf("token --%s is not %s", name, value)
			}
		}
	}
	// Square corners everywhere; circles only for status dots.
	radii := regexp.MustCompile(`(?m)^(.*)border-radius:\s*([^;]+);`).FindAllStringSubmatch(string(css), -1)
	for _, r := range radii {
		if r[2] != "0" && !strings.HasPrefix(strings.TrimSpace(r[1]), ".dot ") {
			t.Errorf("a corner that is not square: %s", r[0])
		}
	}
}

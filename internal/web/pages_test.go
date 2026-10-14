package web_test

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// pagesFixture serves a yard's view and its sandbox origin side by side,
// both reading the blobs where the yard keeps them, as production does:
// the store's home is the view's.
type pagesFixture struct {
	t       *testing.T
	st      store.API
	srv     *web.Server
	home    string
	view    *httptest.Server
	sandbox *httptest.Server
}

func newPagesFixture(t *testing.T) *pagesFixture {
	t.Helper()
	dir := t.TempDir()
	st := storetest.Open(t, filepath.Join(dir, "yard.db"))
	srv, err := web.NewServer(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &pagesFixture{t: t, st: st, srv: srv, home: dir}
	f.view = httptest.NewServer(srv)
	t.Cleanup(func() { shut(f.view) })
	f.sandbox = httptest.NewServer(srv.PagesHandler())
	t.Cleanup(func() { shut(f.sandbox) })
	return f
}

func (f *pagesFixture) do(ts *httptest.Server, method, target string, headers ...string) resp {
	f.t.Helper()
	req, err := http.NewRequest(method, ts.URL+target, nil)
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

func (f *pagesFixture) get(target string, headers ...string) resp {
	f.t.Helper()
	return f.do(f.view, http.MethodGet, target, headers...)
}

func (f *pagesFixture) getPages(target string, headers ...string) resp {
	f.t.Helper()
	return f.do(f.sandbox, http.MethodGet, target, headers...)
}

// getJSON reads target on the view as JSON into v and returns the answer.
func (f *pagesFixture) getJSON(target string, v any) resp {
	f.t.Helper()
	r := f.get(target, "Accept", "application/json")
	if r.status != 200 {
		f.t.Fatalf("%s: status %d: %s", target, r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		f.t.Fatalf("%s: %v: %s", target, err, r.body)
	}
	return r
}

// notedPage puts a page and an image on a note of a new bead, and returns
// the bead with their refs.
func (f *pagesFixture) notedPage(t *testing.T) (bead string, page, shot *store.BlobRef) {
	t.Helper()
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door"}, "me")
	must(t, err)
	shot, err = f.st.PutBlob(ctx, shotPNG)
	must(t, err)
	page, err = f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", shot.Hash, page.Hash)
	must(t, err)
	return b.ID, page, shot
}

// The sandbox origin answers no yard route: every page of the view,
// and a page's bytes outside the run wrapper, is its mux's own 404
// there.
func TestSandboxAnswersNoYardRoute(t *testing.T) {
	f := newPagesFixture(t)
	_, page, _ := f.notedPage(t)
	paths := []string{"/", "/beads", "/beads/some-bead", "/talk", "/health", "/assets/yard.css", "/favicon.ico", "/events", "/run", "/run/", "/blobs/" + page.Hash}
	for _, path := range paths {
		r := f.getPages(path)
		if r.status != 404 {
			t.Errorf("%s: status %d, want 404", path, r.status)
		}
		if ctype := r.header.Get("Content-Type"); !strings.HasPrefix(ctype, "text/plain") {
			t.Errorf("%s answers as %s, want no yard page", path, ctype)
		}
	}
	assertRunPolicy(t, f.getPages("/").header.Get("Content-Security-Policy"))
}

// The sandbox origin is fenced as the view itself is: the same names
// answer, and any other is the same 421.
func TestSandboxHostRulesMatchTheView(t *testing.T) {
	f := newPagesFixture(t)
	_, page, _ := f.notedPage(t)
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		if r := f.getPages("/run/"+page.Hash, "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d, want 200", host, r.status)
		}
	}
	f.srv.AllowHosts("yard.example.net")
	if r := f.getPages("/run/"+page.Hash, "Host", "yard.example.net"); r.status != 200 {
		t.Errorf("Host yard.example.net: status %d, want 200", r.status)
	}
	// What the view refuses, the origin refuses with the same words.
	for _, host := range []string{"evil.example.net:8789", "10.0.0.1:8789"} {
		view := f.get("/blobs/"+page.Hash, "Host", host)
		pages := f.getPages("/run/"+page.Hash, "Host", host)
		if view.status != http.StatusMisdirectedRequest || pages.status != http.StatusMisdirectedRequest {
			t.Errorf("Host %s: view %d, origin %d, want 421 on both", host, view.status, pages.status)
		}
		if view.body != pages.body {
			t.Errorf("Host %s: the origin's 421 differs from the view's:\n%s\n%s", host, pages.body, view.body)
		}
	}
}

// The sandbox origin answers to its public name (--pages-host) besides
// the view's names, in any case and with any port; the view itself
// answers to no name of the origin's.
func TestSandboxAnswersToPagesHost(t *testing.T) {
	f := newPagesFixture(t)
	_, page, _ := f.notedPage(t)
	f.srv.SetPagesHost("pages.example.net")
	for _, host := range []string{"pages.example.net", "PAGES.example.net", "pages.example.net:443"} {
		if r := f.getPages("/run/"+page.Hash, "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d, want 200", host, r.status)
		}
	}
	if r := f.get("/blobs/"+page.Hash, "Host", "pages.example.net"); r.status != http.StatusMisdirectedRequest {
		t.Errorf("the view answers to the origin's name: status %d, want 421", r.status)
	}
	r := f.getPages("/run/"+page.Hash, "Host", "evil.example.net")
	if r.status != http.StatusMisdirectedRequest || !strings.Contains(r.body, "or pages.example.net") {
		t.Errorf("the origin's 421 names no public name: status %d: %s", r.status, r.body)
	}
}

// A note's page carries run beside its static link, opening the run
// wrapper on the sandbox origin; a reader on this machine goes straight
// to where the origin listens. The JSON names the files as the notes
// have them, with no run base.
func TestBeadPageNoteRunLink(t *testing.T) {
	f := newPagesFixture(t)
	bead, page, _ := f.notedPage(t)

	var got map[string]any
	f.getJSON("/beads/"+bead, &got)
	for _, key := range []string{"RunBase", "runBase", "run_base"} {
		if _, ok := got[key]; ok {
			t.Errorf("the JSON carries %q", key)
		}
	}

	body := f.get("/beads/" + bead).body
	if want := `<a href="/blobs/` + page.Hash + `">page ` + page.Hash[:8] + `</a>`; !strings.Contains(body, want) {
		t.Errorf("the page lacks its static link %s", want)
	}
	if want := `<a href="//127.0.0.1:8789/run/` + page.Hash + `">run</a>`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %s", want)
	}
}

// A reader elsewhere goes to the sandbox origin by the name it reached
// the view by, on the origin's port — the origin's public name where
// one is given — and a reader here goes to the address the origin
// listens on, whatever the view was reached by.
func TestRunLinkBase(t *testing.T) {
	f := newPagesFixture(t)
	bead, page, _ := f.notedPage(t)
	f.srv.AllowHosts("yard.example.net")
	run := func(host string) string {
		t.Helper()
		body := f.get("/beads/"+bead, "Host", host).body
		link := `<a href="`
		start := strings.Index(body, link+`//`)
		if start < 0 {
			t.Fatalf("Host %s: the page carries no run link", host)
		}
		rest := body[start+len(link):]
		href, _, _ := strings.Cut(rest, `"`)
		return href
	}
	for _, c := range []struct {
		host string
		want string
	}{
		{"127.0.0.1", "//127.0.0.1:8789/run/" + page.Hash},
		{"localhost:1234", "//127.0.0.1:8789/run/" + page.Hash},
		{"yard.example.net", "//yard.example.net:8789/run/" + page.Hash},
	} {
		if got := run(c.host); got != c.want {
			t.Errorf("Host %s: run %s, want %s", c.host, got, c.want)
		}
	}
	f.srv.SetPagesHost("pages.example.net")
	for _, c := range []struct {
		host string
		want string
	}{
		// A reader here still goes straight to the origin.
		{"127.0.0.1", "//127.0.0.1:8789/run/" + page.Hash},
		// A reader there goes by the origin's public name, on the
		// port the view was reached by.
		{"yard.example.net", "//pages.example.net/run/" + page.Hash},
		{"yard.example.net:8443", "//pages.example.net:8443/run/" + page.Hash},
	} {
		if got := run(c.host); got != c.want {
			t.Errorf("Host %s: run %s, want %s", c.host, got, c.want)
		}
	}
	f.srv.SetPagesAddr("127.0.0.1:9001")
	if got := run("localhost"); got != "//127.0.0.1:9001/run/"+page.Hash {
		t.Errorf("the origin on 127.0.0.1:9001: run %s", got)
	}
	if got := run("yard.example.net"); got != "//pages.example.net/run/"+page.Hash {
		t.Errorf("the origin on 127.0.0.1:9001, by name: run %s", got)
	}
	// The origin on every interface (--unsafe): a reader here reaches
	// it on loopback.
	f.srv.SetPagesAddr("0.0.0.0:9001")
	if got := run("127.0.0.1"); got != "//127.0.0.1:9001/run/"+page.Hash {
		t.Errorf("the origin on 0.0.0.0:9001: run %s", got)
	}
}

// The run wrapper holds the response's policy to the run policy,
// exactly: strict like the scripted policy, but with no sandbox
// directive — the frame's own sandbox attribute confines the page —
// and frame-src 'self', which stops every navigation away.
func assertRunPolicy(t *testing.T, csp string) {
	t.Helper()
	if want := "default-src 'none'; script-src 'unsafe-inline'; connect-src 'none'; frame-src 'self'; child-src 'none'; worker-src 'none'; webrtc 'block'; object-src 'none'; font-src 'none'; media-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"; csp != want {
		t.Fatalf("Content-Security-Policy %q, want %q", csp, want)
	}
	if strings.Contains(csp, "sandbox") {
		t.Errorf("the policy carries a sandbox directive")
	}
	for _, token := range []string{"allow-scripts", "allow-same-origin", "allow-top-navigation", "allow-popups"} {
		if strings.Contains(csp, token) {
			t.Errorf("the policy carries %s", token)
		}
	}
	got := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		name, vals, _ := strings.Cut(strings.TrimSpace(d), " ")
		got[name] = vals
	}
	for directive, want := range map[string]string{
		"script-src": "'unsafe-inline'",
		// Fetch, frames, workers and WebRTC each forbidden; only
		// data: images and inline styles render beside the markup.
		"connect-src": "'none'",
		"frame-src":   "'self'",
		"child-src":   "'none'",
		"worker-src":  "'none'",
		"webrtc":      "'block'",
		"img-src":     "data:",
		"style-src":   "'unsafe-inline'",
		"default-src": "'none'",
		// Never embedded, never posted from.
		"frame-ancestors": "'none'",
		"form-action":     "'none'",
		"base-uri":        "'none'",
	} {
		if got[directive] != want {
			t.Errorf("%s %q, want %q", directive, got[directive], want)
		}
	}
}

// A note's page runs in the run wrapper: the page's bytes in a
// sandboxed srcdoc frame, under the shim, filling the window, with
// the run policy. The wrapper itself has no script and no link.
func TestRunWrapsNotePage(t *testing.T) {
	f := newPagesFixture(t)
	_, page, _ := f.notedPage(t)

	r := f.getPages("/run/" + page.Hash)
	if r.status != 200 || r.header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("the run page: status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	assertRunPolicy(t, r.header.Get("Content-Security-Policy"))
	for _, h := range [][2]string{
		{"Cross-Origin-Resource-Policy", "same-origin"},
		{"X-Content-Type-Options", "nosniff"},
		{"Referrer-Policy", "no-referrer"},
		{"Cache-Control", "no-store"},
	} {
		if got := r.header.Get(h[0]); got != h[1] {
			t.Errorf("the run page's %s %q, want %q", h[0], got, h[1])
		}
	}
	if want := `<title>run ` + page.Hash[:8] + `</title>`; !strings.Contains(r.body, want) {
		t.Errorf("the run page lacks %s", want)
	}
	if !strings.Contains(r.body, `sandbox="allow-scripts"`) {
		t.Error("the run page's frame is not sandboxed to scripts")
	}
	if n := strings.Count(r.body, "<script"); n != 0 {
		t.Errorf("the run page carries %d script elements, want none outside the frame", n)
	}
	// The frame holds the shim before the page's own bytes, escaped
	// for the attribute and parsing back to them exactly.
	attr, _, _ := strings.Cut(r.body[strings.Index(r.body, `srcdoc="`)+len(`srcdoc="`):], `"`)
	srcdoc := html.UnescapeString(attr)
	shim := `<script>try{delete window.RTCPeerConnection;delete window.webkitRTCPeerConnection;}catch(e){}window.RTCPeerConnection=undefined;window.webkitRTCPeerConnection=undefined;</script>`
	if want := "<!DOCTYPE html>" + shim + string(pageHTML); srcdoc != want {
		t.Errorf("the frame holds %q, want the shim before the page's bytes", srcdoc)
	}
	if strings.Count(shim, "</script") != 1 || strings.Count(shim, "<script") != 1 {
		t.Error("the shim carries a script tag of its own")
	}
	r = f.do(f.sandbox, "HEAD", "/run/"+page.Hash)
	if r.status != 200 || r.header.Get("Content-Type") != "text/html; charset=utf-8" || r.body != "" {
		t.Errorf("HEAD the run page: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	assertRunPolicy(t, r.header.Get("Content-Security-Policy"))
}

// The wrapper carries bytes that would break out of an unescaped
// attribute — quotes, entities, tags — escaped, parsing back exactly.
func TestRunEscapesPageBytes(t *testing.T) {
	f := newPagesFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door"}, "me")
	must(t, err)
	evil := []byte("<!DOCTYPE html><html><body><p title=\"a&b\">x</p><script>var s=\"</iframe>\"</script></body></html>")
	ref, err := f.st.PutBlob(ctx, evil)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", ref.Hash)
	must(t, err)

	r := f.getPages("/run/" + ref.Hash)
	if r.status != 200 {
		t.Fatalf("the run page: status %d", r.status)
	}
	attr, _, _ := strings.Cut(r.body[strings.Index(r.body, `srcdoc="`)+len(`srcdoc="`):], `"`)
	if srcdoc := html.UnescapeString(attr); !strings.HasSuffix(srcdoc, string(evil)) {
		t.Errorf("the frame holds %q, want it ending in the page's bytes", srcdoc)
	}
	if n := strings.Count(r.body, "<script"); n != 0 {
		t.Errorf("the run page carries %d script elements, want none outside the frame", n)
	}
}

// Anything the origin has no wrapper for — a mail's file, one the
// yard never held or no longer holds, a name that is no hash —
// answers plainly, as its blob route does.
func TestRunMissing(t *testing.T) {
	f := newPagesFixture(t)
	ctx := context.Background()
	_, page, _ := f.notedPage(t)
	mailedPage, err := f.st.PutBlob(ctx, pageFence)
	must(t, err)
	if _, err := f.st.AddUser(ctx, "sam", "me"); err != nil {
		t.Fatal(err)
	}
	must(t, f.st.Mail(ctx, "sam", "the fence, drawn", "me", mailedPage.Hash))
	must(t, os.Remove(store.BlobPath(f.home, page.Hash)))

	for _, hash := range []string{mailedPage.Hash, strings.Repeat("0", 64), "nope", page.Hash} {
		r := f.getPages("/run/" + hash)
		if r.status != 404 || !strings.Contains(r.body, "no such blob") {
			t.Errorf("/run/%s: status %d, want a plain 404: %s", hash, r.status, r.body)
		}
		if ctype := r.header.Get("Content-Type"); !strings.HasPrefix(ctype, "text/plain") {
			t.Errorf("/run/%s answers as %s, want no yard page", hash, ctype)
		}
	}
	if r := f.do(f.sandbox, "POST", "/run/"+strings.Repeat("0", 64)); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", r.status)
	}
}

// A page the yard no longer holds is the Missing line, with neither its
// static link nor its run link.
func TestRunLinkMissing(t *testing.T) {
	f := newPagesFixture(t)
	bead, page, _ := f.notedPage(t)
	must(t, os.Remove(store.BlobPath(f.home, page.Hash)))
	body := f.get("/beads/" + bead).body
	if want := `page ` + page.Hash[:8] + `: the yard no longer has it`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %q", want)
	}
	if strings.Contains(body, ">run</a>") || strings.Contains(body, "/blobs/"+page.Hash) {
		t.Error("the page links what the yard no longer holds")
	}
}

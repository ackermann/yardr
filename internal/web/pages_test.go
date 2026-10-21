package web_test

import (
	"context"
	"encoding/json"
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
	return servePagesFixture(t, st, srv, dir)
}

// newPagesJWTFixture serves a yard's view and its sandbox origin side by
// side in JWT mode, against the stub's keys and the users table.
func newPagesJWTFixture(t *testing.T, jwks *jwksStub, users map[string]string) *pagesFixture {
	t.Helper()
	dir := t.TempDir()
	st := storetest.Open(t, filepath.Join(dir, "yard.db"))
	srv, err := web.NewServer(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.UseJWT(context.Background(), web.JWTConfig{
		Issuer: jwtTestIssuer, Audience: jwtTestAudience,
		JWKSURL: jwks.ts.URL, Users: users,
	}); err != nil {
		t.Fatal(err)
	}
	return servePagesFixture(t, st, srv, dir)
}

// servePagesFixture serves srv's view and its sandbox origin side by side.
func servePagesFixture(t *testing.T, st store.API, srv *web.Server, dir string) *pagesFixture {
	t.Helper()
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
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", store.BlobFile{Hash: shot.Hash}, store.BlobFile{Hash: page.Hash})
	must(t, err)
	return b.ID, page, shot
}

// The sandbox origin serves a note's blobs with their bytes and type,
// under the scripted policy: a page runs its inline scripts there, and
// nothing else. Anything else — a mail's file, one the yard never held
// or no longer holds, a name that is no hash — answers plainly: the
// origin answers no yard route, not even the yard's error page.
func TestSandboxServesNoteBlobs(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t)
	ctx := context.Background()
	_, page, shot := f.notedPage(t)
	mailed, err := f.st.PutBlob(ctx, shotJPEG)
	must(t, err)
	mailedPage, err := f.st.PutBlob(ctx, pageFence)
	must(t, err)
	if _, err := f.st.AddUser(ctx, "sam", "me"); err != nil {
		t.Fatal(err)
	}
	must(t, f.st.Mail(ctx, "sam", "paint it", "me", store.BlobFile{Hash: mailed.Hash}))
	must(t, f.st.Mail(ctx, "sam", "the fence, drawn", "me", store.BlobFile{Hash: mailedPage.Hash}))

	r := f.getPages("/blobs/" + page.Hash)
	if r.status != 200 || r.header.Get("Content-Type") != "text/html; charset=utf-8" || r.body != string(pageHTML) {
		t.Errorf("the note's page: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	if etag := r.header.Get("ETag"); etag != `"`+page.Hash+`"` {
		t.Errorf("the page's ETag %q, want the hash", etag)
	}
	assertScriptedPolicy(t, r.header.Get("Content-Security-Policy"))
	for _, h := range [][2]string{
		{"Cross-Origin-Resource-Policy", "same-origin"},
		{"X-Content-Type-Options", "nosniff"},
		{"Referrer-Policy", "no-referrer"},
	} {
		if got := r.header.Get(h[0]); got != h[1] {
			t.Errorf("the page's %s %q, want %q", h[0], got, h[1])
		}
	}
	if cookie := r.header.Get("Set-Cookie"); cookie != "" {
		t.Errorf("the page sets a cookie %q, want none", cookie)
	}
	r = f.getPages("/blobs/" + shot.Hash)
	if r.status != 200 || r.header.Get("Content-Type") != "image/png" || r.body != string(shotPNG) {
		t.Errorf("the note's image: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	assertScriptedPolicy(t, r.header.Get("Content-Security-Policy"))
	if cookie := r.header.Get("Set-Cookie"); cookie != "" {
		t.Errorf("the image sets a cookie %q, want none", cookie)
	}
	// A file downloads the same way as on the main origin; only a page
	// runs there.
	report, err := f.st.PutBlob(ctx, []byte("%PDF-1.4 the report"))
	must(t, err)
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "the report"}, "me")
	must(t, err)
	if _, err := f.st.AddNotedAs(ctx, b.ID, "the report, filed", "me", "", store.BlobFile{Hash: report.Hash, Name: "report.pdf"}); err != nil {
		t.Fatal(err)
	}
	r = f.getPages("/blobs/" + report.Hash)
	if r.status != 200 || r.body != "%PDF-1.4 the report" {
		t.Fatalf("the note's file: status %d, %d bytes", r.status, len(r.body))
	}
	if got, want := r.header.Get("Content-Disposition"), "attachment; filename*=UTF-8''report.pdf"; got != want {
		t.Errorf("the file's Content-Disposition %q, want %q", got, want)
	}
	r = f.getPages("/blobs/" + report.Hash + "/from-the-board.pdf")
	if got, want := r.header.Get("Content-Disposition"), "attachment; filename*=UTF-8''from-the-board.pdf"; r.status != 200 || got != want {
		t.Errorf("the named file: status %d, Content-Disposition %q, want %q", r.status, got, want)
	}
	if r := f.getPages("/blobs/" + report.Hash + "/a%2Fb.pdf"); r.status != 404 || !strings.Contains(r.body, "no such blob") {
		t.Errorf("a bad name: status %d, want a plain 404: %s", r.status, r.body)
	}
	r = f.do(f.sandbox, "HEAD", "/blobs/"+page.Hash)
	if r.status != 200 || r.header.Get("Content-Type") != "text/html; charset=utf-8" || r.body != "" {
		t.Errorf("HEAD the page: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	assertScriptedPolicy(t, r.header.Get("Content-Security-Policy"))
	if cookie := r.header.Get("Set-Cookie"); cookie != "" {
		t.Errorf("HEAD the page sets a cookie %q, want none", cookie)
	}
	for _, hash := range []string{mailed.Hash, mailedPage.Hash, strings.Repeat("0", 64), "nope"} {
		r := f.getPages("/blobs/" + hash)
		if r.status != 404 || !strings.Contains(r.body, "no such blob") {
			t.Errorf("/blobs/%s: status %d, want a plain 404: %s", hash, r.status, r.body)
		}
		if ctype := r.header.Get("Content-Type"); !strings.HasPrefix(ctype, "text/plain") {
			t.Errorf("/blobs/%s answers as %s, want no yard page", hash, ctype)
		}
	}
	if r := f.do(f.sandbox, "POST", "/blobs/"+page.Hash); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", r.status)
	}
}

// The view's pages answer Referrer-Policy same-origin and the sandbox
// origin's no-referrer: the same note page, served on both origins,
// carries its origin off the view only back to the view, and nothing
// off the sandbox. same-origin is what lets a plain form POST carry
// the view's own Origin (under no-referrer a browser sends null for a
// navigate-mode POST: Fetch, "append a request Origin header").
func TestReferrerPolicyViewAndSandbox(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t)
	_, page, _ := f.notedPage(t)
	r := f.get("/blobs/" + page.Hash)
	if r.status != 200 {
		t.Fatalf("the view's page: status %d, want 200: %s", r.status, r.body)
	}
	if got := r.header.Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("the view's page: Referrer-Policy %q, want same-origin", got)
	}
	r = f.getPages("/blobs/" + page.Hash)
	if r.status != 200 {
		t.Fatalf("the sandbox page: status %d, want 200: %s", r.status, r.body)
	}
	if got := r.header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("the sandbox page: Referrer-Policy %q, want no-referrer", got)
	}
}

// In JWT mode the sandbox origin takes the gateway's token too: a request
// without one is 401, as on the view, and only a verified one is served,
// under the scripted policy. The origin stays fenced and note-only: an
// unknown Host is 421, a mail's file 404s even with a token.
func TestSandboxRequiresJWT(t *testing.T) {
	t.Parallel()
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	f := newPagesJWTFixture(t, jwks, map[string]string{"jo@example.net": "jo"})
	header := web.DefaultJWTHeader
	ctx := context.Background()
	_, page, _ := f.notedPage(t)
	mailed, err := f.st.PutBlob(ctx, shotJPEG)
	must(t, err)
	if _, err := f.st.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	must(t, f.st.Mail(ctx, "jo", "paint it", "me", store.BlobFile{Hash: mailed.Hash}))
	token := signRSA(t, keys.a, "a", jwtClaims("jo@example.net"))

	// Without a token the origin refuses as the view does.
	if r := f.getPages("/blobs/" + page.Hash); r.status != 401 {
		t.Errorf("no token on the origin: status %d, want 401: %s", r.status, r.body)
	}
	if r := f.get("/blobs/" + page.Hash); r.status != 401 {
		t.Errorf("no token on the view: status %d, want 401: %s", r.status, r.body)
	}
	if r := f.getPages("/blobs/"+page.Hash, header, "junk"); r.status != 401 {
		t.Errorf("a junk token on the origin: status %d, want 401: %s", r.status, r.body)
	}
	// With a token the note's page serves under the scripted policy.
	r := f.getPages("/blobs/"+page.Hash, header, token)
	if r.status != 200 || r.header.Get("Content-Type") != "text/html; charset=utf-8" || r.body != string(pageHTML) {
		t.Errorf("the note's page: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	assertScriptedPolicy(t, r.header.Get("Content-Security-Policy"))
	if cookie := r.header.Get("Set-Cookie"); cookie != "" {
		t.Errorf("the page sets a cookie %q, want none", cookie)
	}
	// A mail's file stays 404 with a token: the origin serves notes only.
	if r := f.getPages("/blobs/"+mailed.Hash, header, token); r.status != 404 {
		t.Errorf("the mail's file with a token: status %d, want 404: %s", r.status, r.body)
	}
	// An unknown Host is still 421, before any token is read.
	if r := f.getPages("/blobs/"+page.Hash, "Host", "evil.example.net"); r.status != http.StatusMisdirectedRequest {
		t.Errorf("Host evil.example.net: status %d, want 421: %s", r.status, r.body)
	}
}

// assertScriptedPolicy holds the response's policy to the scripted policy
// of the design (section 3.5), exactly. These are header checks: inline
// scripts run, and fetch, frames, workers and form posts are forbidden
// by the policy; WebRTC is blocked only in browsers that enforce its
// directive. The absent allow-top-navigation and allow-popups tokens do
// not stop a top-level page from navigating itself; docs/web.md records
// that limit.
func assertScriptedPolicy(t *testing.T, csp string) {
	t.Helper()
	if want := "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; connect-src 'none'; frame-src 'none'; child-src 'none'; worker-src 'none'; webrtc 'block'; object-src 'none'; font-src 'none'; media-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"; csp != want {
		t.Fatalf("Content-Security-Policy %q, want %q", csp, want)
	}
	for _, token := range []string{"allow-same-origin", "allow-top-navigation", "allow-popups"} {
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
		// Scripts run, but the sandbox keeps them from the yard:
		// no same origin and no yard API.
		"sandbox":    "allow-scripts",
		"script-src": "'unsafe-inline'",
		// Fetch, frames and workers are forbidden; only data: images
		// and inline styles render beside the markup. WebRTC depends
		// on browser support for its directive, noted above.
		"connect-src": "'none'",
		"frame-src":   "'none'",
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

// The sandbox origin answers no yard route: every page of the view is
// its mux's own 404 there.
func TestSandboxAnswersNoYardRoute(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t)
	for _, path := range []string{"/", "/beads", "/beads/some-bead", "/talk", "/health", "/assets/yard.css", "/favicon.ico", "/events"} {
		r := f.getPages(path)
		if r.status != 404 {
			t.Errorf("%s: status %d, want 404", path, r.status)
		}
		if ctype := r.header.Get("Content-Type"); !strings.HasPrefix(ctype, "text/plain") {
			t.Errorf("%s answers as %s, want no yard page", path, ctype)
		}
	}
	if csp := f.getPages("/").header.Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox allow-scripts;") {
		t.Errorf("the 404's Content-Security-Policy %q, want the scripted policy", csp)
	}
}

// The sandbox origin is fenced as the view itself is: the same names
// answer, and any other is the same 421.
func TestSandboxHostRulesMatchTheView(t *testing.T) {
	t.Parallel()
	f := newPagesFixture(t)
	_, _, shot := f.notedPage(t)
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		if r := f.getPages("/blobs/"+shot.Hash, "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d, want 200", host, r.status)
		}
	}
	f.srv.AllowHosts("yard.example.net")
	if r := f.getPages("/blobs/"+shot.Hash, "Host", "yard.example.net"); r.status != 200 {
		t.Errorf("Host yard.example.net: status %d, want 200", r.status)
	}
	// What the view refuses, the origin refuses with the same words.
	for _, host := range []string{"evil.example.net:8789", "10.0.0.1:8789"} {
		view := f.get("/blobs/"+shot.Hash, "Host", host)
		pages := f.getPages("/blobs/"+shot.Hash, "Host", host)
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
	t.Parallel()
	f := newPagesFixture(t)
	_, _, shot := f.notedPage(t)
	f.srv.SetPagesHost("pages.example.net")
	for _, host := range []string{"pages.example.net", "PAGES.example.net", "pages.example.net:443"} {
		if r := f.getPages("/blobs/"+shot.Hash, "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d, want 200", host, r.status)
		}
	}
	if r := f.get("/blobs/"+shot.Hash, "Host", "pages.example.net"); r.status != http.StatusMisdirectedRequest {
		t.Errorf("the view answers to the origin's name: status %d, want 421", r.status)
	}
	r := f.getPages("/blobs/"+shot.Hash, "Host", "evil.example.net")
	if r.status != http.StatusMisdirectedRequest || !strings.Contains(r.body, "or pages.example.net") {
		t.Errorf("the origin's 421 names no public name: status %d: %s", r.status, r.body)
	}
}

// A note's page carries run beside its static link, opening the same
// bytes on the sandbox origin; a reader on this machine goes straight
// to where the origin listens. The JSON names the files as the notes
// have them, with no run base.
func TestBeadPageNoteRunLink(t *testing.T) {
	t.Parallel()
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
	if want := `<a href="//127.0.0.1:8789/blobs/` + page.Hash + `">run</a>`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %s", want)
	}
}

// A reader elsewhere goes to the sandbox origin by the name it reached
// the view by, on the origin's port — the origin's public name where
// one is given — and a reader here goes to the address the origin
// listens on, whatever the view was reached by.
func TestRunLinkBase(t *testing.T) {
	t.Parallel()
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
		{"127.0.0.1", "//127.0.0.1:8789/blobs/" + page.Hash},
		{"localhost:1234", "//127.0.0.1:8789/blobs/" + page.Hash},
		{"yard.example.net", "//yard.example.net:8789/blobs/" + page.Hash},
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
		{"127.0.0.1", "//127.0.0.1:8789/blobs/" + page.Hash},
		// A reader there goes by the origin's public name, on the
		// port the view was reached by.
		{"yard.example.net", "//pages.example.net/blobs/" + page.Hash},
		{"yard.example.net:8443", "//pages.example.net:8443/blobs/" + page.Hash},
	} {
		if got := run(c.host); got != c.want {
			t.Errorf("Host %s: run %s, want %s", c.host, got, c.want)
		}
	}
	f.srv.SetPagesAddr("127.0.0.1:9001")
	if got := run("localhost"); got != "//127.0.0.1:9001/blobs/"+page.Hash {
		t.Errorf("the origin on 127.0.0.1:9001: run %s", got)
	}
	if got := run("yard.example.net"); got != "//pages.example.net/blobs/"+page.Hash {
		t.Errorf("the origin on 127.0.0.1:9001, by name: run %s", got)
	}
	// The origin on every interface (--unsafe): a reader here reaches
	// it on loopback.
	f.srv.SetPagesAddr("0.0.0.0:9001")
	if got := run("127.0.0.1"); got != "//127.0.0.1:9001/blobs/"+page.Hash {
		t.Errorf("the origin on 0.0.0.0:9001: run %s", got)
	}
}

// A page the yard no longer holds is the Missing line, with neither its
// static link nor its run link.
func TestRunLinkMissing(t *testing.T) {
	t.Parallel()
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

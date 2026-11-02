package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// localReadYard is a yard in JWT mode with one user, jo, and a bead, at its
// view's door and at its local-read listener's, and a gateway token for jo.
func localReadYard(t *testing.T) (f *pagesFixture, b *store.Bead, jo string) {
	t.Helper()
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	f = newPagesJWTFixture(t, jwks, map[string]string{"jo@example.net": "jo"})
	_, err := f.st.AddUser(context.Background(), "jo", "jo")
	must(t, err)
	return f, bead(t, f.st, "one"), signRSA(t, keys.a, "a", jwtClaims("jo@example.net"))
}

// In JWT mode the view's own door asks every request for the gateway's
// token, and the local-read listener asks for none: each route a local
// reader draws the yard from answers it as JSON with no credential, while
// the same route on the view is 401 without a token and served with a good
// one. Held in one table so that a local handler that took the JWT path,
// and a view that dropped it, each fail here.
func TestLocalReadServesWithoutAToken(t *testing.T) {
	t.Parallel()
	f, b, jo := localReadYard(t)
	for _, path := range []string{
		"/depots", "/flows", "/groups", "/routes", "/crew", "/beads", "/beads?all=1",
		"/beads/" + b.ID, "/sessions?all=1", "/deps",
	} {
		local := f.getLocal(path)
		var got map[string]any
		if local.status != 200 || json.Unmarshal([]byte(local.body), &got) != nil {
			t.Errorf("local %s with no token: status %d, want 200 and JSON: %.200s", path, local.status, local.body)
		}
		if ct := local.header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("local %s: Content-Type %q, want application/json", path, ct)
		}
		if r := f.get(path, "Accept", "application/json"); r.status != http.StatusUnauthorized {
			t.Errorf("view %s with no token: status %d, want 401: %.200s", path, r.status, r.body)
		}
		if r := f.get(path, "Accept", "application/json", web.DefaultJWTHeader, jo); r.status != 200 {
			t.Errorf("view %s with a token: status %d, want 200: %.200s", path, r.status, r.body)
		}
	}
	if r := f.getLocal("/beads?all=1"); !strings.Contains(r.body, b.ID) {
		t.Errorf("the local reader's beads do not list %s: %.200s", b.ID, r.body)
	}
	// A token on the local door is none of its business: it reads the same
	// with a bad one.
	if r := f.getLocal("/depots", web.DefaultJWTHeader, "junk"); r.status != 200 {
		t.Errorf("local /depots with a junk token: status %d, want 200", r.status)
	}
	// HEAD reads too.
	if r := f.do(f.local, http.MethodHead, "/depots"); r.status != 200 || r.body != "" {
		t.Errorf("local HEAD /depots: status %d with %d bytes, want 200 and none", r.status, len(r.body))
	}
	// The reader is nobody: a policy with no form to post, as a request that
	// resolved no user has on the view.
	if csp := f.getLocal("/depots").header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'none'") {
		t.Errorf("local Content-Security-Policy %q, want form-action 'none'", csp)
	}
}

// The local-read listener carries the stream with no credential, and the
// view's own door still refuses it. Its reader is nobody: the stream does
// not end at its first event (a request with no user is no removed one),
// carries what the yard tells anyone, and holds back mail that the view
// tells its parties alone.
func TestLocalReadStream(t *testing.T) {
	t.Parallel()
	f, _, jo := localReadYard(t)
	ctx := context.Background()
	_, err := f.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo")
	must(t, err)
	head, err := f.st.Head(ctx)
	must(t, err)
	after := "/events?format=json&after=" + strconv.FormatUint(head, 10)

	if r := f.get(after); r.status != http.StatusUnauthorized {
		t.Fatalf("the view's stream with no token: status %d, want 401", r.status)
	}
	local := openStream(t, f.local.URL+after)
	viaView := openStream(t, f.view.URL+after, web.DefaultJWTHeader, jo)

	// readTo is the data of the frames up to the event with seq, as they
	// came, which the stream carries once and in order.
	readTo := func(s *stream, seq uint64) (data []string) {
		t.Helper()
		for {
			fr := s.next(t)
			if data = append(data, fr.data); fr.id >= seq {
				return data
			}
		}
	}
	headAfter := func() uint64 {
		t.Helper()
		seq, err := f.st.Head(ctx)
		must(t, err)
		return seq
	}
	b := bead(t, f.st, "two")
	for name, s := range map[string]*stream{"local": local, "view": viaView} {
		if got := strings.Join(readTo(s, headAfter()), "\n"); !strings.Contains(got, `"bead":"`+b.ID+`"`) {
			t.Fatalf("%s stream: %q, want the creation of %s", name, got, b.ID)
		}
	}
	// Mail between jo and the yardmaster: jo's own stream is told it, and the
	// local reader is not, though its stream goes on.
	must(t, f.st.Mail(ctx, "yardmaster", "paint the shed", "jo"))
	mailed := headAfter()
	if got := strings.Join(readTo(viaView, mailed), "\n"); !strings.Contains(got, "paint the shed") {
		t.Fatalf("jo's stream: %q, want the mail", got)
	}
	if got := strings.Join(readTo(local, mailed), "\n"); strings.Contains(got, "paint the shed") {
		t.Fatalf("local stream: %q, want the mail held back", got)
	}
	c := bead(t, f.st, "three")
	if got := strings.Join(readTo(local, headAfter()), "\n"); !strings.Contains(got, `"bead":"`+c.ID+`"`) {
		t.Fatalf("local stream after the mail: %q, want the creation of %s", got, c.ID)
	}

	// Only JSON is streamed here: the other formats are the view's own
	// pages' fragments.
	for _, format := range []string{"html", "card", "seq", "home", "yaml"} {
		if r := f.getLocal("/events?format=" + format); r.status != http.StatusBadRequest || !strings.Contains(r.body, "format is json, not "+format) {
			t.Errorf("local /events?format=%s: status %d, want 400 naming the format: %s", format, r.status, r.body)
		}
	}
}

// The local-read listener is a reader's and nothing more. Anything but GET
// and HEAD is 405 on every path, a path it does not serve is 404 in the
// JSON it speaks, whatever the view serves there, and a request that asks
// for HTML is still answered as JSON: it never serves a page, a form or a
// blob. A Host header that is no host of this machine's is 421, as on the
// view.
func TestLocalReadRefuses(t *testing.T) {
	t.Parallel()
	f, b, _ := localReadYard(t)
	ctx := context.Background()
	blob, err := f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the page", "me", "", store.BlobFile{Hash: blob.Hash})
	must(t, err)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		for _, path := range []string{"/", "/talk", "/beads/" + b.ID, "/pairing", "/previews", "/depots", "/events", "/nothing"} {
			r := f.do(f.local, method, path)
			if r.status != http.StatusMethodNotAllowed || r.header.Get("Allow") != "GET, HEAD" {
				t.Errorf("local %s %s: status %d, Allow %q, want 405 and GET, HEAD", method, path, r.status, r.header.Get("Allow"))
			}
		}
	}

	// Pages, forms, blobs and the view's other routes are not served here,
	// and neither is what a reader of the view would guess at.
	for _, path := range []string{
		"/", "/talk", "/line", "/sessions/", "/previews", "/limits", "/telemetry", "/config", "/peers", "/pairing", "/pairing.png",
		"/health", "/favicon.ico", "/assets/app.css", "/blobs/" + blob.Hash, "/blobs/" + blob.Hash + "/the-page.html",
		"/beads/" + b.ID + "/fresh/1/state", "/beads/" + b.ID + "/asks/1/files/0",
		"/api/snapshot", "/api/bead/" + b.ID, "/events/", "/depots/", "/beads/",
	} {
		r := f.getLocal(path)
		var got struct{ Error string }
		if r.status != http.StatusNotFound || json.Unmarshal([]byte(r.body), &got) != nil || !strings.Contains(got.Error, "no route at "+path) {
			t.Errorf("local %s: status %d, want 404 in JSON naming the path: %.200s", path, r.status, r.body)
		}
	}

	// A client that wants a page gets the data: the same answer, as JSON.
	for _, accept := range []string{"text/html", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", "*/*", ""} {
		r := f.getLocal("/beads/"+b.ID, "Accept", accept)
		var got map[string]any
		if r.status != 200 || r.header.Get("Content-Type") != "application/json" || json.Unmarshal([]byte(r.body), &got) != nil {
			t.Errorf("local /beads/%s asking for %q: status %d, type %q, want 200 and JSON: %.100s", b.ID, accept, r.status, r.header.Get("Content-Type"), r.body)
		}
	}
	// So is an error: the page that is missing is not an error page.
	if r := f.getLocal("/beads/nope", "Accept", "text/html"); r.status != http.StatusNotFound || r.header.Get("Content-Type") != "application/json" {
		t.Errorf("local /beads/nope asking for HTML: status %d, type %q, want 404 and JSON: %.100s", r.status, r.header.Get("Content-Type"), r.body)
	}

	// The Host rules are the view's: a name that is not this machine's is 421
	// (a page on another site must not read the yard through a visitor's
	// browser), and the loopback names are answered.
	if r := f.getLocal("/depots", "Host", "evil.example"); r.status != http.StatusMisdirectedRequest {
		t.Errorf("local /depots under a foreign Host: status %d, want 421", r.status)
	}
	if r := f.getLocal("/depots", "Host", "localhost:8793"); r.status != 200 {
		t.Errorf("local /depots under localhost: status %d, want 200", r.status)
	}
}

// A view that acts (--as) takes its writes from a browser that holds its
// cookie. The local-read listener asks for no cookie and trades no token for
// one, and its reader acts as no one.
func TestLocalReadAsksNoCookie(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	srv, err := web.NewServer(st, t.TempDir(), nil)
	must(t, err)
	token := srv.ActAs("jo")
	local := httptest.NewServer(srv.LocalReadHandler())
	t.Cleanup(func() { shut(local) })
	f := &pagesFixture{t: t, st: st, srv: srv, local: local}

	for _, target := range []string{"/depots", "/depots?token=" + token} {
		r := f.getLocal(target)
		if r.status != 200 || r.header.Get("Set-Cookie") != "" {
			t.Errorf("local %s on a view that acts: status %d, Set-Cookie %q, want 200 and no cookie", target, r.status, r.header.Get("Set-Cookie"))
		}
		if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'none'") {
			t.Errorf("local %s: Content-Security-Policy %q, want form-action 'none' for a reader who acts as no one", target, csp)
		}
	}
}

package web_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"rsc.io/qr"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
	"yardr.dev/yardr/internal/yardkey"
)

// pairingYard serves a yard in JWT mode where ada and bob are signed in,
// connected to an edge: the pairing page's home. It hands back the fixture
// and each user's token.
func pairingYard(t *testing.T) (f *fixture, ada, bob string) {
	t.Helper()
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	f = jwtFixture(t, jwks, "", map[string]string{"ada@example.net": "ada", "bob@example.net": "bob"})
	ctx := context.Background()
	for _, name := range []string{"ada", "bob"} {
		if _, err := f.st.AddUser(ctx, name, "me"); err != nil {
			t.Fatal(err)
		}
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edge := store.Edge{URL: "https://my.yardr.dev", Key: yardkey.Public(key), Handle: "mckean", Name: "edgeyard"}
	if err := f.st.SetSetting(ctx, store.EdgeSetting, edge.String(), "me"); err != nil {
		t.Fatal(err)
	}
	// The yard's own name, which the QR names beside the edge's address,
	// is the name setting's, not the edge's name for it.
	if err := f.st.SetSetting(ctx, store.NameSetting, "depot", "me"); err != nil {
		t.Fatal(err)
	}
	ada = signRSA(t, keys.a, "a", jwtClaims("ada@example.net"))
	bob = signRSA(t, keys.a, "a", jwtClaims("bob@example.net"))
	return f, ada, bob
}

// pairingDo sends one request to the pairing yard's view as the token's
// user, with a form when form is not nil, and does not follow redirects.
func pairingDo(t *testing.T, f *fixture, token, method, target string, form url.Values) resp {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, f.ts.URL+target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", f.ts.URL)
	}
	if token != "" {
		req.Header.Set(web.DefaultJWTHeader, token)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

func pairingGet(t *testing.T, f *fixture, token, target string) resp {
	t.Helper()
	return pairingDo(t, f, token, http.MethodGet, target, nil)
}

// The signed-in user opens a pairing and sees its QR, its code and the way
// to end it; a reload shows the same code again, and ending closes it.
func TestPairingPageOpensShowsAndEnds(t *testing.T) {
	t.Parallel()
	f, ada, _ := pairingYard(t)

	r := pairingGet(t, f, ada, "/pairing")
	if r.status != 200 || !strings.Contains(r.body, "Pair one of your own devices") || strings.Contains(r.body, "pairing-code") ||
		!strings.Contains(r.body, `href="/pairing" aria-current="page"`) {
		t.Fatalf("the page with none open: status %d: %s", r.status, r.body)
	}
	r = pairingDo(t, f, ada, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"phone"}})
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/pairing" {
		t.Fatalf("opening: status %d at %q: %s", r.status, r.header.Get("Location"), r.body)
	}
	stored, err := f.st.DevicePairCodeSelf(context.Background(), "ada")
	if err != nil {
		t.Fatal(err)
	}
	r = pairingGet(t, f, ada, "/pairing")
	if r.status != 200 || !strings.Contains(r.body, stored.Device) || !strings.Contains(r.body, stored.Code) ||
		!strings.Contains(r.body, `src="/pairing.png"`) || !strings.Contains(r.body, "End pairing") {
		t.Fatalf("the page with one open: status %d: %s", r.status, r.body)
	}
	// A reload is the same page, code and all.
	if again := pairingGet(t, f, ada, "/pairing"); again.body != r.body {
		t.Fatalf("a reload differs:\n%s\n%s", again.body, r.body)
	}
	// The QR is the pairing's: every module of the PNG is the code of the
	// edge's address, the code and the yard's name.
	want, err := qr.Encode("https://my.yardr.dev#"+stored.Code+"#depot", qr.M)
	if err != nil {
		t.Fatal(err)
	}
	r = pairingGet(t, f, ada, "/pairing.png")
	if r.status != 200 || r.header.Get("Content-Type") != "image/png" {
		t.Fatalf("the QR: status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader([]byte(r.body)))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != (want.Size+8)*want.Scale || b.Dy() != (want.Size+8)*want.Scale {
		t.Fatalf("the QR is %dx%d, want %d square", b.Dx(), b.Dy(), (want.Size+8)*want.Scale)
	}
	for y := 0; y < want.Size; y++ {
		for x := 0; x < want.Size; x++ {
			px, py := (x+4)*want.Scale+want.Scale/2, (y+4)*want.Scale+want.Scale/2
			r16, _, _, _ := img.At(px, py).RGBA()
			if black := r16 < 0x8000; black != want.Black(x, y) {
				t.Fatalf("the QR's module %d,%d is black %v, want %v", x, y, black, want.Black(x, y))
			}
		}
	}

	r = pairingDo(t, f, ada, http.MethodPost, "/pairing", url.Values{"action": {"end"}})
	if r.status != http.StatusSeeOther {
		t.Fatalf("ending: status %d: %s", r.status, r.body)
	}
	r = pairingGet(t, f, ada, "/pairing")
	if r.status != 200 || !strings.Contains(r.body, "Pair one of your own devices") || strings.Contains(r.body, "pairing-code") {
		t.Fatalf("the page after ending: status %d: %s", r.status, r.body)
	}
	if r := pairingGet(t, f, ada, "/pairing.png"); r.status != http.StatusNotFound {
		t.Fatalf("the QR after ending: status %d, want 404", r.status)
	}
}

// A second signed-in user sees no pairing while one is open, is refused a
// second opening in plain words that name no one's device, and cannot end
// it; the owner's pairing stands through all of it.
func TestPairingPageSecondUser(t *testing.T) {
	t.Parallel()
	f, ada, bob := pairingYard(t)
	if r := pairingDo(t, f, ada, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"phone"}}); r.status != http.StatusSeeOther {
		t.Fatalf("opening: status %d: %s", r.status, r.body)
	}
	r := pairingGet(t, f, bob, "/pairing")
	if r.status != 200 || !strings.Contains(r.body, "Pair one of your own devices") || strings.Contains(r.body, "pairing-code") {
		t.Fatalf("the second user's page: status %d: %s", r.status, r.body)
	}
	if r := pairingGet(t, f, bob, "/pairing.png"); r.status != http.StatusNotFound {
		t.Fatalf("the second user's QR: status %d, want 404", r.status)
	}
	r = pairingDo(t, f, bob, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"laptop"}})
	if r.status != 200 || !strings.Contains(r.body, "a pairing is already open, try again in a few minutes") ||
		strings.Contains(r.body, "ada") {
		t.Fatalf("the second user's opening: status %d: %s", r.status, r.body)
	}
	r = pairingDo(t, f, bob, http.MethodPost, "/pairing", url.Values{"action": {"end"}})
	if r.status != 200 || !strings.Contains(r.body, "there is no pairing open to end") {
		t.Fatalf("the second user's ending: status %d: %s", r.status, r.body)
	}
	if _, err := f.st.DevicePairCodeSelf(context.Background(), "ada"); err != nil {
		t.Fatalf("the owner's pairing after the second user's tries: %v", err)
	}
}

// The pairing routes are the signed-in view's alone: a view with no JWT
// mode refuses the page, its forms and the QR, in ActAs dress too.
func TestPairingRefusedWithoutJWT(t *testing.T) {
	t.Parallel()
	plain := newFixture(t)
	r := plain.get("/pairing")
	if r.status != http.StatusForbidden || !strings.Contains(r.body, "signed-in view only") || strings.Contains(r.body, `href="/pairing"`) {
		t.Errorf("a plain view's page: status %d: %s", r.status, r.body)
	}
	if r := plain.get("/pairing.png"); r.status != http.StatusForbidden {
		t.Errorf("a plain view's QR: status %d, want 403", r.status)
	}
	// An acting view registers the POST route, and refuses it the same way.
	srv, err := web.NewServer(plain.st, plain.home, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := srv.ActAs("ada")
	acting := servePagesFixture(t, plain.st, srv, plain.home)
	if r := acting.get("/pairing"); r.status != http.StatusForbidden || strings.Contains(r.body, `href="/pairing"`) {
		t.Errorf("an acting view's page: status %d, want 403", r.status)
	}
	actingUser := &fixture{t: t, st: plain.st, ts: acting.view, home: plain.home}
	actingUser.signIn(srv, token)
	r = actingUser.postForm("/pairing", url.Values{"action": {"open"}, "device": {"phone"}}, "Origin", acting.view.URL)
	if r.status != http.StatusForbidden || !strings.Contains(r.body, "signed-in view only") {
		t.Errorf("an acting view's open: status %d, want 403: %s", r.status, r.body)
	}
	if _, err := plain.st.DevicePairCodeSelf(context.Background(), "ada"); err == nil {
		t.Error("an acting view opened a pairing without JWT mode")
	}
}

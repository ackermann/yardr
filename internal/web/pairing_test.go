package web_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"rsc.io/qr"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
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
	edge := store.Edge{URL: "https://my.yardr.dev", Key: store.Public(key), Handle: "mckean", Name: "edgeyard"}
	if err := f.st.SetSetting(ctx, store.EdgeSetting, edge.String(), "me"); err != nil {
		t.Fatal(err)
	}
	// The QR names the yard as the edge spells it, handle/name from the
	// edge setting; the name setting's bare name stands where no edge
	// setting does.
	if err := f.st.SetSetting(ctx, store.NameSetting, "depot", "me"); err != nil {
		t.Fatal(err)
	}
	ada = signRSA(t, keys.a, "a", jwtClaims("ada@example.net"))
	bob = signRSA(t, keys.a, "a", jwtClaims("bob@example.net"))
	return f, ada, bob
}

// pairingDo sends one request to the pairing yard's view as the token's
// user, with a form when form is not nil, and does not follow redirects.
// headers are header name and value in pairs, as the fixture's own do.
func pairingDo(t *testing.T, f *fixture, token, method, target string, form url.Values, headers ...string) resp {
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
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
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

// pairingPNGHolds checks the pairing QR endpoint serves a PNG whose every
// module is the code of want.
func pairingPNGHolds(t *testing.T, f *fixture, token, want string) {
	t.Helper()
	code, err := qr.Encode(want, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	r := pairingGet(t, f, token, "/pairing.png")
	if r.status != 200 || r.header.Get("Content-Type") != "image/png" {
		t.Fatalf("the QR: status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader([]byte(r.body)))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != (code.Size+8)*code.Scale || b.Dy() != (code.Size+8)*code.Scale {
		t.Fatalf("the QR is %dx%d, want %d square", b.Dx(), b.Dy(), (code.Size+8)*code.Scale)
	}
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			px, py := (x+4)*code.Scale+code.Scale/2, (y+4)*code.Scale+code.Scale/2
			r16, _, _, _ := img.At(px, py).RGBA()
			if black := r16 < 0x8000; black != code.Black(x, y) {
				t.Fatalf("the QR's module %d,%d is black %v, want %v", x, y, black, code.Black(x, y))
			}
		}
	}
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
		!strings.Contains(r.body, `src="/pairing.png"`) || !strings.Contains(r.body, "the yard mckean/edgeyard") ||
		!strings.Contains(r.body, "End pairing") {
		t.Fatalf("the page with one open: status %d: %s", r.status, r.body)
	}
	// A reload is the same page, code and all.
	if again := pairingGet(t, f, ada, "/pairing"); again.body != r.body {
		t.Fatalf("a reload differs:\n%s\n%s", again.body, r.body)
	}
	// The QR is the pairing's: every module of the PNG is the code of the
	// edge's address, the code and the yard's handle/name.
	pairingPNGHolds(t, f, ada, "https://my.yardr.dev#"+stored.Code+"#mckean/edgeyard")

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
	ctx := context.Background()
	// With no edge setting, the page keeps the bare yard name and offers no
	// QR to scan; the endpoint still holds that name beside the code.
	if err := f.st.SetSetting(ctx, store.EdgeSetting, "", "me"); err != nil {
		t.Fatal(err)
	}
	if r = pairingDo(t, f, ada, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"phone"}}); r.status != http.StatusSeeOther {
		t.Fatalf("opening: status %d: %s", r.status, r.body)
	}
	stored, err = f.st.DevicePairCodeSelf(ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	r = pairingGet(t, f, ada, "/pairing")
	if r.status != 200 || !strings.Contains(r.body, stored.Code) ||
		strings.Contains(r.body, `src="/pairing.png"`) || !strings.Contains(r.body, "no QR to scan") {
		t.Fatalf("the page with no edge: status %d: %s", r.status, r.body)
	}
	pairingPNGHolds(t, f, ada, "#"+stored.Code+"#depot")
	if r = pairingDo(t, f, ada, http.MethodPost, "/pairing", url.Values{"action": {"end"}}); r.status != http.StatusSeeOther {
		t.Fatalf("ending the no-edge pairing: status %d: %s", r.status, r.body)
	}

	// An edge value from before yards had names holds no name, and reads as
	// named as its account: the page and QR name handle/handle.
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edge := store.Edge{URL: "https://my.yardr.dev", Key: store.Public(key), Handle: "mckean"}
	if err := f.st.SetSetting(ctx, store.EdgeSetting, edge.String(), "me"); err != nil {
		t.Fatal(err)
	}
	if r = pairingDo(t, f, ada, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"phone"}}); r.status != http.StatusSeeOther {
		t.Fatalf("opening: status %d: %s", r.status, r.body)
	}
	stored, err = f.st.DevicePairCodeSelf(ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	r = pairingGet(t, f, ada, "/pairing")
	if r.status != 200 || !strings.Contains(r.body, "the yard mckean/mckean") {
		t.Fatalf("the page for a nameless edge value: status %d: %s", r.status, r.body)
	}
	pairingPNGHolds(t, f, ada, "https://my.yardr.dev#"+stored.Code+"#mckean/mckean")
}

// A second signed-in user sees no pairing while one is open, is refused a
// second opening in the words that name whose it is, and cannot end it;
// the owner's pairing stands through all of it.
func TestPairingPageSecondUser(t *testing.T) {
	t.Parallel()
	f, ada, bob := pairingYard(t)
	if r := pairingDo(t, f, ada, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"phone"}}); r.status != http.StatusSeeOther {
		t.Fatalf("opening: status %d: %s", r.status, r.body)
	}
	stored, err := f.st.DevicePairCodeSelf(context.Background(), "ada")
	if err != nil {
		t.Fatal(err)
	}
	r := pairingGet(t, f, bob, "/pairing")
	if r.status != 200 || !strings.Contains(r.body, "Pair one of your own devices") || strings.Contains(r.body, "pairing-code") {
		t.Fatalf("the second user's page: status %d: %s", r.status, r.body)
	}
	if r := pairingGet(t, f, bob, "/pairing.png"); r.status != http.StatusNotFound {
		t.Fatalf("the second user's QR: status %d, want 404", r.status)
	}
	const want = "a pairing is already open for ada, try again in a few minutes"
	r = pairingDo(t, f, bob, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"laptop"}})
	if r.status != 200 || !strings.Contains(r.body, want) || strings.Contains(r.body, stored.Code) {
		t.Fatalf("the second user's opening: status %d: %s", r.status, r.body)
	}
	// The same refusal in JSON: the error alone names the holder.
	r = pairingDo(t, f, bob, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"laptop"}}, "Accept", "application/json")
	var got web.Pairing
	if r.status != 200 || r.header.Get("Content-Type") != "application/json" ||
		json.Unmarshal([]byte(r.body), &got) != nil || got.Error != want || got.Open || got.Code != "" {
		t.Fatalf("the second user's opening as JSON: status %d, type %q: %s", r.status, r.header.Get("Content-Type"), r.body)
	}
	r = pairingDo(t, f, bob, http.MethodPost, "/pairing", url.Values{"action": {"end"}})
	if r.status != 200 || !strings.Contains(r.body, "there is no pairing open to end") {
		t.Fatalf("the second user's ending: status %d: %s", r.status, r.body)
	}
	if _, err := f.st.DevicePairCodeSelf(context.Background(), "ada"); err != nil {
		t.Fatalf("the owner's pairing after the second user's tries: %v", err)
	}
}

// A member-mapped user - the yard user an invite made, with the invited
// address mapped to them in web_jwt_users - opens a pairing of their own
// through the pairing page, for their yard user alone: another signed-in
// user sees none of it and cannot open or end over it.
func TestPairingPageMemberMappedUser(t *testing.T) {
	t.Parallel()
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	f := jwtFixture(t, jwks, "", map[string]string{"ada@example.net": "ada", "eve@example.net": "eve"})
	ctx := context.Background()
	if _, err := f.st.AddUser(ctx, "ada", "me"); err != nil {
		t.Fatal(err)
	}
	// The invite's yard writes standing, as the carrier leaves them:
	// the member user and the web_jwt_users entry for their address
	// (InviteTeammate's own write is held in the store's tests).
	if _, err := f.st.AddUser(ctx, "eve", "me"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetSetting(ctx, store.WebJWTUsersSetting, "eve@example.net=eve", "me"); err != nil {
		t.Fatal(err)
	}
	if all, _ := f.st.Settings(ctx); all[store.WebJWTUsersSetting] != "eve@example.net=eve" {
		t.Fatalf("the mapping the member signs in under = %q", all[store.WebJWTUsersSetting])
	}
	if u, err := f.st.GetUser(ctx, "eve"); err != nil || u.IsOperator() {
		t.Fatalf("the member user = %+v, %v", u, err)
	}
	ada := signRSA(t, keys.a, "a", jwtClaims("ada@example.net"))
	eve := signRSA(t, keys.a, "a", jwtClaims("eve@example.net"))

	r := pairingDo(t, f, eve, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"phone"}})
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/pairing" {
		t.Fatalf("the member opening a pairing: status %d at %q: %s", r.status, r.header.Get("Location"), r.body)
	}
	stored, err := f.st.DevicePairCodeSelf(ctx, "eve")
	if err != nil || stored.Device != "phone" {
		t.Fatalf("the member's pairing = %+v, %v", stored, err)
	}
	r = pairingGet(t, f, eve, "/pairing")
	if r.status != 200 || !strings.Contains(r.body, stored.Code) || !strings.Contains(r.body, "phone") {
		t.Fatalf("the member's page: status %d: %s", r.status, r.body)
	}
	// It is eve's alone: ada has no pairing, sees no code, and can
	// neither open a second one nor end eve's.
	if _, err := f.st.DevicePairCodeSelf(ctx, "ada"); err == nil {
		t.Fatal("ada holds a pairing: the member's pairing is not hers alone")
	}
	if r := pairingGet(t, f, ada, "/pairing.png"); r.status != http.StatusNotFound {
		t.Fatalf("ada's QR for the member's pairing: status %d, want 404", r.status)
	}
	r = pairingDo(t, f, ada, http.MethodPost, "/pairing", url.Values{"action": {"open"}, "device": {"laptop"}})
	if r.status != 200 || !strings.Contains(r.body, "a pairing is already open for eve, try again in a few minutes") || strings.Contains(r.body, stored.Code) {
		t.Fatalf("ada opening over the member's pairing: status %d: %s", r.status, r.body)
	}
	r = pairingDo(t, f, ada, http.MethodPost, "/pairing", url.Values{"action": {"end"}})
	if r.status != 200 || !strings.Contains(r.body, "there is no pairing open to end") {
		t.Fatalf("ada ending the member's pairing: status %d: %s", r.status, r.body)
	}
	if _, err := f.st.DevicePairCodeSelf(ctx, "eve"); err != nil {
		t.Fatalf("the member's pairing after ada's tries: %v", err)
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

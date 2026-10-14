package web_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

const (
	jwtTestIssuer   = "https://team.cloudflareaccess.com"
	jwtTestAudience = "aud-tag-123"
	jwtTestEmail    = "mckean@example.net"
)

// jwtTestKeys are the keys the JWT tests sign with: a and d are published,
// b rotates in, c is never published.
type jwtTestKeys struct {
	a, b, c *rsa.PrivateKey
	d       *ecdsa.PrivateKey
}

var loadJWTTestKeys = sync.OnceValues(func() (jwtTestKeys, error) {
	var out jwtTestKeys
	var err error
	if out.a, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		return out, err
	}
	if out.b, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		return out, err
	}
	if out.c, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		return out, err
	}
	if out.d, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		return out, err
	}
	return out, nil
})

func mustJWTTestKeys(t *testing.T) jwtTestKeys {
	t.Helper()
	keys, err := loadJWTTestKeys()
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// rsaJWK is the public key as a JWKS entry under kid.
func rsaJWK(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// ecJWK is the public key as a JWKS entry under kid.
func ecJWK(kid string, pub *ecdsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "EC", "kid": kid, "alg": "ES256", "use": "sig", "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(pub.X.Bytes()),
		"y": base64.RawURLEncoding.EncodeToString(pub.Y.Bytes()),
	}
}

// jwksDoc is keys as a JWKS document.
func jwksDoc(t *testing.T, keys ...map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// jwksStub serves a key set the test rotates, counting its fetches.
type jwksStub struct {
	t       *testing.T
	ts      *httptest.Server
	fetches atomic.Int64

	mu     sync.Mutex
	doc    string
	status int
	delay  time.Duration
}

func newJWKStub(t *testing.T, doc string) *jwksStub {
	t.Helper()
	s := &jwksStub{t: t, doc: doc}
	s.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.fetches.Add(1)
		s.mu.Lock()
		delay, status, doc := s.delay, s.status, s.doc
		s.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, doc)
	}))
	t.Cleanup(s.ts.Close)
	return s
}

func (s *jwksStub) setDoc(doc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc, s.status = doc, 0
}

func (s *jwksStub) breakWith(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// jwtServer builds a server for st in JWT mode against the stub's keys,
// carrying the header ("" for the default) and the users table.
func jwtServer(t *testing.T, st store.API, jwks *jwksStub, header string, users map[string]string) (*web.Server, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".depot")
	srv, err := web.NewServer(st, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.UseJWT(context.Background(), web.JWTConfig{
		Issuer: jwtTestIssuer, Audience: jwtTestAudience,
		JWKSURL: jwks.ts.URL, Header: header, Users: users,
	}); err != nil {
		t.Fatal(err)
	}
	return srv, home
}

// serveJWT serves a yard in JWT mode against the stub's keys, carrying the
// header ("" for the default) and the users table.
func serveJWT(t *testing.T, jwks *jwksStub, header string, users map[string]string) (*httptest.Server, *web.Server) {
	t.Helper()
	srv, _ := jwtServer(t, storetest.New(t), jwks, header, users)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return ts, srv
}

// jwtFixture serves a yard in JWT mode as serveJWT does, and hands back
// the fixture for its store.
func jwtFixture(t *testing.T, jwks *jwksStub, header string, users map[string]string) *fixture {
	t.Helper()
	st := storetest.New(t)
	srv, home := jwtServer(t, st, jwks, header, users)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return &fixture{t: t, st: st, ts: ts, home: home}
}

// jwtClaims is a valid token's claims for email.
func jwtClaims(email string) jwt.MapClaims {
	return jwt.MapClaims{
		"iss": jwtTestIssuer, "aud": jwtTestAudience,
		"exp":   time.Now().Add(5 * time.Minute).Unix(),
		"email": email,
	}
}

func signRSA(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func signEC(t *testing.T, key *ecdsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func jwtGet(t *testing.T, ts *httptest.Server, target string, headers ...string) resp {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	// Errorf, not Fatal: concurrent requests call this off the test's
	// goroutine, where Fatal must not run.
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("GET %s: %v", target, err)
		return resp{}
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

// In JWT mode a request with a token the test key signed is served; a
// missing header or an invalid token is 401, an unmapped email 403, and
// /health stays open without a token.
func TestJWTModeServesAndRefuses(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey), ecJWK("d", &keys.d.PublicKey)))
	ts, _ := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	header := web.DefaultJWTHeader

	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwtClaims(jwtTestEmail))
	hs.Header["kid"] = "a"
	hmac, err := hs.SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"x"}`)) + "."

	with := func(email string, set func(jwt.MapClaims)) string {
		claims := jwtClaims(email)
		set(claims)
		return signRSA(t, keys.a, "a", claims)
	}
	for _, c := range []struct {
		name   string
		target string
		token  string
		// noHeader sends no header at all; token "" otherwise sends an
		// empty one, which is the same refusal.
		noHeader bool
		want     int
		wantBody string
	}{
		{"valid RS256", "/", signRSA(t, keys.a, "a", jwtClaims(jwtTestEmail)), false, 200, ""},
		{"valid ES256", "/", signEC(t, keys.d, "d", jwtClaims(jwtTestEmail)), false, 200, ""},
		{"email matches without regard to case", "/", signRSA(t, keys.a, "a", jwtClaims("McKean@Example.NET")), false, 200, ""},
		{"email_verified true", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["email_verified"] = true }), false, 200, ""},
		{"audience among several", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["aud"] = []string{"other", jwtTestAudience} }), false, 200, ""},
		{"/health stays open", "/health", "", true, 200, ""},

		{"no header", "/", "", true, 401, "missing"},
		{"empty header", "/", "", false, 401, "missing"},
		{"junk", "/", "junk", false, 401, "malformed"},
		{"two parts", "/", "a.b", false, 401, "malformed"},
		{"bad signature", "/", signRSA(t, keys.c, "a", jwtClaims(jwtTestEmail)), false, 401, "bad signature"},
		{"unknown key", "/", signRSA(t, keys.c, "zzz", jwtClaims(jwtTestEmail)), false, 401, "unknown key"},
		{"no kid with two keys", "/", signRSA(t, keys.a, "", jwtClaims(jwtTestEmail)), false, 401, "unknown key"},
		{"expired", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-5 * time.Minute).Unix() }), false, 401, "expired"},
		{"expired within the minute's leeway", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-30 * time.Second).Unix() }), false, 200, ""},
		{"expired past the leeway", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-90 * time.Second).Unix() }), false, 401, "expired"},
		{"not yet valid", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(5 * time.Minute).Unix() }), false, 401, "not yet valid"},
		{"wrong issuer", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["iss"] = "https://other.example.com" }), false, 401, "another issuer's"},
		{"missing issuer", "/", with(jwtTestEmail, func(c jwt.MapClaims) { delete(c, "iss") }), false, 401, "another issuer's"},
		{"wrong audience", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["aud"] = "other" }), false, 401, "for another audience"},
		{"missing audience", "/", with(jwtTestEmail, func(c jwt.MapClaims) { delete(c, "aud") }), false, 401, "for another audience"},
		{"missing expiry", "/", with(jwtTestEmail, func(c jwt.MapClaims) { delete(c, "exp") }), false, 401, "it never expires"},
		{"email unverified", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["email_verified"] = false }), false, 401, "unverified"},
		{"email_verified not a boolean", "/", with(jwtTestEmail, func(c jwt.MapClaims) { c["email_verified"] = "true" }), false, 401, "unverified"},
		{"missing email", "/", with("", func(c jwt.MapClaims) { delete(c, "email") }), false, 401, "no email"},
		{"HS256", "/", hmac, false, 401, "signed with HS256"},
		{"none", "/", none, false, 401, "signed with none"},

		{"unmapped email", "/", signRSA(t, keys.a, "a", jwtClaims("stranger@example.net")), false, 403, "no user"},
	} {
		var got resp
		if c.noHeader {
			got = jwtGet(t, ts, c.target)
		} else {
			got = jwtGet(t, ts, c.target, header, c.token)
		}
		if got.status != c.want || (c.wantBody != "" && !strings.Contains(got.body, c.wantBody)) {
			t.Errorf("%s: status %d, body %q, want %d with %q", c.name, got.status, got.body, c.want, c.wantBody)
		}
	}
	if got := jwtGet(t, ts, "/", header, signRSA(t, keys.a, "a", jwtClaims("stranger@example.net"))); !strings.Contains(got.body, "web_jwt_users") {
		t.Errorf("the 403 names no setting: %q", got.body)
	}
}

// A token with no key id takes the key when exactly one is cached.
func TestJWTNoKIDTakesTheOnlyKey(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	ts, _ := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, signRSA(t, keys.a, "", jwtClaims(jwtTestEmail))); got.status != 200 {
		t.Fatalf("status %d: %s", got.status, got.body)
	}
}

// On Authorization a Bearer prefix is stripped, and a token without one is
// taken as it is; on any other header a Bearer word is part of the token,
// and no valid token carries one.
func TestJWTAuthorizationBearer(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	users := map[string]string{jwtTestEmail: "mckean"}
	token := signRSA(t, keys.a, "a", jwtClaims(jwtTestEmail))

	ts, _ := serveJWT(t, jwks, "Authorization", users)
	for _, c := range []struct {
		name  string
		value string
		want  int
	}{
		{"bearer", "Bearer " + token, 200},
		{"bearer in lower case", "bearer " + token, 200},
		{"no scheme", token, 200},
		{"bearer junk", "Bearer junk", 401},
	} {
		if got := jwtGet(t, ts, "/", "Authorization", c.value); got.status != c.want {
			t.Errorf("%s: status %d, want %d: %s", c.name, got.status, c.want, got.body)
		}
	}

	plain, _ := serveJWT(t, jwks, "", users)
	if got := jwtGet(t, plain, "/", web.DefaultJWTHeader, "Bearer "+token); got.status != 401 {
		t.Errorf("a Bearer word on %s: status %d, want 401", web.DefaultJWTHeader, got.status)
	}
}

// An unknown key id refetches, but at most once a minute: misses cannot
// burst the issuer.
func TestJWTUnknownKIDRefetchBounded(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	ts, _ := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	if n := jwks.fetches.Load(); n != 1 {
		t.Fatalf("starting fetched %d times, want 1", n)
	}
	unknown := signRSA(t, keys.c, "zzz", jwtClaims(jwtTestEmail))
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, unknown); got.status != 401 {
		t.Fatalf("status %d, want 401: %s", got.status, got.body)
	}
	if n := jwks.fetches.Load(); n != 2 {
		t.Fatalf("an unknown key id fetched %d times, want 2", n)
	}
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, unknown); got.status != 401 {
		t.Fatalf("status %d, want 401: %s", got.status, got.body)
	}
	if n := jwks.fetches.Load(); n != 2 {
		t.Fatalf("a second miss fetched, %d times now, want 2", n)
	}
}

// A rotation lands without a restart: the unknown key id refetches, and
// the token verifies against the new set.
func TestJWTRotationLandsWithoutRestart(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	ts, _ := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	jwks.setDoc(jwksDoc(t, rsaJWK("a", &keys.a.PublicKey), rsaJWK("b", &keys.b.PublicKey)))
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, signRSA(t, keys.b, "b", jwtClaims(jwtTestEmail))); got.status != 200 {
		t.Fatalf("status %d, want 200: %s", got.status, got.body)
	}
	if n := jwks.fetches.Load(); n != 2 {
		t.Fatalf("fetched %d times, want 2 (start and the rotation)", n)
	}
}

// When a refresh fails the cached keys keep serving: a known key id is
// served, an unknown one refused, and neither hangs on the issuer.
func TestJWTStaleKeysServeWhenRefreshFails(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	ts, _ := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	jwks.breakWith(http.StatusInternalServerError)
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, signRSA(t, keys.a, "a", jwtClaims(jwtTestEmail))); got.status != 200 {
		t.Fatalf("a known key id: status %d, want 200: %s", got.status, got.body)
	}
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, signRSA(t, keys.c, "zzz", jwtClaims(jwtTestEmail))); got.status != 401 {
		t.Fatalf("an unknown key id: status %d, want 401: %s", got.status, got.body)
	}
}

// One in-flight fetch is shared by every handler: while a slow issuer
// answers, concurrent misses wait for its keys instead of fetching or
// refusing each alone.
func TestJWTSharedInFlightFetch(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	ts, _ := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	jwks.setDoc(jwksDoc(t, rsaJWK("a", &keys.a.PublicKey), rsaJWK("b", &keys.b.PublicKey)))
	jwks.mu.Lock()
	jwks.delay = 300 * time.Millisecond
	jwks.mu.Unlock()
	token := signRSA(t, keys.b, "b", jwtClaims(jwtTestEmail))
	const n = 10
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = jwtGet(t, ts, "/", web.DefaultJWTHeader, token).status
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Errorf("request %d: status %d, want 200", i, code)
		}
	}
	if n := jwks.fetches.Load(); n != 2 {
		t.Errorf("fetched %d times, want 2 (start and one shared refresh)", n)
	}
}

// JWT mode starts with keys, never without: an unreachable, broken,
// oversized or keyless set refuses UseJWT, as does a config without an
// issuer, an audience or a keys' URL.
func TestUseJWTRefuses(t *testing.T) {
	keys := mustJWTTestKeys(t)
	serve := func(t *testing.T, cfg web.JWTConfig) error {
		t.Helper()
		srv, err := web.NewServer(storetest.New(t), filepath.Join(t.TempDir(), ".depot"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return srv.UseJWT(context.Background(), cfg)
	}
	base := web.JWTConfig{Issuer: jwtTestIssuer, Audience: jwtTestAudience}
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	for _, c := range []struct {
		name string
		cfg  web.JWTConfig
		want string
	}{
		{"no issuer", web.JWTConfig{Audience: "a", JWKSURL: closed.URL}, "an issuer"},
		{"no audience", web.JWTConfig{Issuer: "i", JWKSURL: closed.URL}, "an audience"},
		{"no keys' URL", web.JWTConfig{Issuer: "i", Audience: "a"}, "a JWKS URL"},
		{"unreachable", web.JWTConfig{Issuer: base.Issuer, Audience: base.Audience, JWKSURL: closed.URL}, "could not fetch"},
	} {
		if err := serve(t, c.cfg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	bodies := []struct {
		name string
		doc  string
		want string
	}{
		{"not JSON", "junk", "not a key set"},
		{"no usable keys", jwksDoc(t), "no usable keys"},
		{"an unusable key", jwksDoc(t, map[string]any{"kty": "oct", "kid": "s", "k": "c2VjcmV0"}), "no usable keys"},
		{"a broken key", jwksDoc(t, map[string]any{"kty": "RSA", "kid": "a", "n": "!!!", "e": "AQAB"}), "no usable keys"},
		{"oversized", strings.Repeat("x", (1<<20)+1), "over the 1 MiB bound"},
	}
	for _, c := range bodies {
		jwks := newJWKStub(t, c.doc)
		cfg := base
		cfg.JWKSURL = jwks.ts.URL
		if err := serve(t, cfg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	jwks.breakWith(http.StatusInternalServerError)
	cfg := base
	cfg.JWKSURL = jwks.ts.URL
	if err := serve(t, cfg); err == nil || !strings.Contains(err.Error(), "http 500") {
		t.Errorf("a 500: %v, want http 500", err)
	}
}

// An allowed loopback HTTP JWKS endpoint cannot redirect key retrieval to
// a public cleartext URL and make the verifier trust keys fetched without
// TLS.
func TestUseJWTRefusesInsecureJWKSRedirect(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://keys.example.invalid/jwks", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	srv, err := web.NewServer(storetest.New(t), filepath.Join(t.TempDir(), ".depot"), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = srv.UseJWT(context.Background(), web.JWTConfig{
		Issuer: jwtTestIssuer, Audience: jwtTestAudience, JWKSURL: redirect.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "non-loopback JWKS URL") {
		t.Fatalf("UseJWT = %v, want an insecure JWKS redirect refusal", err)
	}
}

// An hour's keys are replaced on a request; when the fetch fails the old
// ones keep serving, and the next hour fetches again.
func TestJWTHourlyRefresh(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	ts, srv := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	jwks.setDoc(jwksDoc(t, rsaJWK("b", &keys.b.PublicKey)))
	srv.AgeJWTKeys(2 * time.Hour)
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, signRSA(t, keys.b, "b", jwtClaims(jwtTestEmail))); got.status != 200 {
		t.Fatalf("status %d, want 200: %s", got.status, got.body)
	}
	if n := jwks.fetches.Load(); n != 2 {
		t.Fatalf("fetched %d times, want 2 (start and the hour)", n)
	}
	// The old key is gone with the old set: rotation by replacement.
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, signRSA(t, keys.a, "a", jwtClaims(jwtTestEmail))); got.status != 401 {
		t.Fatalf("the retired key: status %d, want 401", got.status)
	}

	jwks.breakWith(http.StatusInternalServerError)
	srv.AgeJWTKeys(2 * time.Hour)
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, signRSA(t, keys.b, "b", jwtClaims(jwtTestEmail))); got.status != 200 {
		t.Fatalf("a failed refresh: status %d, want 200 from cache: %s", got.status, got.body)
	}
	if n := jwks.fetches.Load(); n != 3 {
		t.Fatalf("fetched %d times, want 3 (start, the hour, the failed hour)", n)
	}
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, signRSA(t, keys.b, "b", jwtClaims(jwtTestEmail))); got.status != 200 {
		t.Fatalf("after a failed refresh: status %d, want 200: %s", got.status, got.body)
	}
	if n := jwks.fetches.Load(); n != 3 {
		t.Fatalf("fetched %d times, want still 3: the failed hour waits out its hour", n)
	}
}

// With no usable keys at all the view refuses with 503, and says when to
// try again.
func TestJWTNoKeysIs503(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	ts, srv := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	srv.DropJWTKeys()
	got := jwtGet(t, ts, "/", web.DefaultJWTHeader, signRSA(t, keys.a, "a", jwtClaims(jwtTestEmail)))
	if got.status != http.StatusServiceUnavailable || !strings.Contains(got.body, "no keys") {
		t.Fatalf("status %d, body %q, want 503 with no keys", got.status, got.body)
	}
	if retry := got.header.Get("Retry-After"); retry == "" {
		t.Fatal("the 503 carries no Retry-After")
	}
}

// The header the view reads with web_jwt_header unset is the setting's
// default: the two literals must not drift apart.
func TestJWTHeaderDefaultMatchesSetting(t *testing.T) {
	set, ok := store.SettingOf(store.WebJWTHeaderSetting)
	if !ok || set.Default != web.DefaultJWTHeader {
		t.Fatalf("web_jwt_header defaults to %q, the view reads %q unset", set.Default, web.DefaultJWTHeader)
	}
}

// The shared refresh is every handler's, not the request's that started
// it: a caller who hangs up mid-fetch does not abort it, so no one with a
// token-shaped header can keep a rotation from landing by cancelling one
// unknown-key request a minute.
func TestJWTRefreshOutlivesItsRequest(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	ts, _ := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	jwks.setDoc(jwksDoc(t, rsaJWK("a", &keys.a.PublicKey), rsaJWK("b", &keys.b.PublicKey)))
	jwks.mu.Lock()
	jwks.delay = 300 * time.Millisecond
	jwks.mu.Unlock()
	token := signRSA(t, keys.b, "b", jwtClaims(jwtTestEmail))
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(web.DefaultJWTHeader, signRSA(t, keys.c, "b", jwtClaims(jwtTestEmail)))
	hangUp := &http.Client{Timeout: 50 * time.Millisecond}
	if r, err := hangUp.Do(req); err == nil {
		r.Body.Close()
		t.Fatal("the hung-up request was answered before the slow fetch")
	}
	deadline := time.Now().Add(3 * time.Second)
	for jwks.fetches.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	jwks.mu.Lock()
	jwks.delay = 0
	jwks.mu.Unlock()
	if got := jwtGet(t, ts, "/", web.DefaultJWTHeader, token); got.status != 200 {
		t.Fatalf("after the hang-up, the rotated key: status %d, want 200: %s", got.status, got.body)
	}
}

// In JWT mode two tokens with two mapped emails act as their own users on
// one view: talk sends as each and shows each their own conversation, each
// answers only the asks to them, a bead sends on only for the member it
// waits for, and the home and line queue shows each visitor only the asks
// addressed to them. The POST routes answer (no 405), and the policy opens
// form-action 'self' on a request with a resolved user.
func TestJWTTwoUsersActAsThemselves(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	f := jwtFixture(t, jwks, "", map[string]string{
		"jo@example.net": "jo", "kay@example.net": "kay",
	})
	header := web.DefaultJWTHeader
	ctx := context.Background()
	must(t, func() error { _, err := f.st.AddUser(ctx, "jo", "jo"); return err }())
	must(t, func() error { _, err := f.st.AddUser(ctx, "kay", "kay"); return err }())
	must(t, func() error {
		_, err := f.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo")
		return err
	}())
	must(t, f.st.Mail(ctx, "yardmaster", "paint the shed", "jo"))
	must(t, f.st.Mail(ctx, "jo", "it is painted", "yardmaster"))
	must(t, f.st.Mail(ctx, "yardmaster", "fix the gate", "kay"))
	must(t, f.st.Mail(ctx, "kay", "it is fixed", "yardmaster"))
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "jo"))
	door, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Depot: "home"}, "jo")
	must(t, err)
	doorAsk, err := f.st.AddAsk(ctx, door.ID, store.Ask{Question: "Move the links now?", To: "jo",
		Context: []string{"The links file declares the new source.", "The old source goes away Friday."}}, "yardmaster")
	must(t, err)
	gate, err := f.st.CreateBead(ctx, store.NewBead{Title: "a gate", Stage: "new", Depot: "home"}, "kay")
	must(t, err)
	gateAsk, err := f.st.AddAsk(ctx, gate.ID, store.Ask{Question: "Oil the hinges?", To: "kay",
		Context: []string{"The gate sticks in wet weather.", "Oil is in the shed."}}, "yardmaster")
	must(t, err)
	// The door waits in decide for decisions, whose only member is jo.
	actFlow(t, f)
	if _, err := f.st.Advance(ctx, door.ID, "decide", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}

	jo := signRSA(t, keys.a, "a", jwtClaims("jo@example.net"))
	kay := signRSA(t, keys.a, "a", jwtClaims("kay@example.net"))

	// Each sees their own conversation, with the form that writes to it.
	r := f.get("/talk", header, jo)
	if r.status != 200 || !strings.Contains(r.body, "paint the shed") ||
		strings.Contains(r.body, "fix the gate") ||
		!strings.Contains(r.body, "Mail to yardmaster as jo") ||
		!strings.Contains(r.body, `<form id="send" method="post"`) {
		t.Errorf("jo's talk: status %d, want jo's letters and the form, and none of kay's:\n%s", r.status, r.body)
	}
	r = f.get("/talk", header, kay)
	if r.status != 200 || !strings.Contains(r.body, "fix the gate") ||
		strings.Contains(r.body, "paint the shed") ||
		!strings.Contains(r.body, "Mail to yardmaster as kay") {
		t.Errorf("kay's talk: status %d, want kay's letters and none of jo's:\n%s", r.status, r.body)
	}
	// The policy opens per request: 'self' where a user resolved, 'none'
	// on /health, which carries none.
	if csp := f.get("/", header, jo).header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("with a resolved user, Content-Security-Policy %q, want form-action 'self'", csp)
	}
	if csp := f.get("/health").header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'none'") {
		t.Errorf("on /health, Content-Security-Policy %q, want form-action 'none'", csp)
	}

	// Talk posts as the token's user: the new letter stands in kay's
	// conversation and none of jo's.
	form := url.Values{"text": {"oil them twice"}}
	if r := f.postForm("/talk?with=yardmaster", form, "Origin", f.ts.URL, header, kay); r.status != http.StatusSeeOther {
		t.Fatalf("kay's talk post: status %d, want 303", r.status)
	}
	letters, _, err := f.st.Conversation(ctx, "kay", "yardmaster", 0, 50, 0)
	must(t, err)
	if last := letters[len(letters)-1]; last.From != "kay" || last.Text != "oil them twice" {
		t.Fatalf("kay's conversation ends with %+v, want her mail", last)
	}
	letters, _, err = f.st.Conversation(ctx, "jo", "yardmaster", 0, 50, 0)
	must(t, err)
	for _, l := range letters {
		if l.Text == "oil them twice" {
			t.Fatalf("kay's mail stands in jo's conversation: %+v", l)
		}
	}

	// Jo's door page answers her ask and sends the door on; kay's does
	// neither.
	r = f.get("/beads/"+door.ID, header, jo)
	for _, want := range []string{`<h3 class="label">asks for jo</h3>`, "Move the links now?", `<h3 class="label">send on as jo</h3>`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("jo's door page lacks %q:\n%s", want, r.body)
		}
	}
	r = f.get("/beads/"+door.ID, header, kay)
	for _, want := range []string{"asks for", "move-form", "send on as"} {
		if strings.Contains(r.body, want) {
			t.Errorf("kay's door page answers or moves with %q:\n%s", want, r.body)
		}
	}

	// The home and line queue shows each visitor only the asks addressed
	// to them: the door's manual wait stands for both, but its ask only
	// for jo.
	askBeads := func(ws []web.Waiting) map[string]bool {
		out := map[string]bool{}
		for _, w := range ws {
			for _, reason := range w.Why {
				if reason.Kind == "ask" {
					out[w.Bead] = true
				}
			}
		}
		return out
	}
	homeOf := func(token string) web.Home {
		t.Helper()
		r := f.get("/", "Accept", "application/json", header, token)
		var h web.Home
		if err := json.Unmarshal([]byte(r.body), &h); err != nil {
			t.Fatal("home is no JSON")
		}
		return h
	}
	lineOf := func(token string) web.Line {
		t.Helper()
		r := f.get("/line", "Accept", "application/json", header, token)
		var l web.Line
		if err := json.Unmarshal([]byte(r.body), &l); err != nil {
			t.Fatal("the line is no JSON")
		}
		return l
	}
	if h := homeOf(jo); h.As != "jo" || len(askBeads(h.Attention)) != 1 || !askBeads(h.Attention)[door.ID] {
		t.Errorf("jo's home: as %q, asks on %v, want as jo and the door's alone", h.As, askBeads(h.Attention))
	}
	if h := homeOf(kay); h.As != "kay" || len(askBeads(h.Attention)) != 1 || !askBeads(h.Attention)[gate.ID] {
		t.Errorf("kay's home: as %q, asks on %v, want as kay and the gate's alone", h.As, askBeads(h.Attention))
	}
	if l := lineOf(jo); l.As != "jo" || len(askBeads(l.Waits)) != 1 || !askBeads(l.Waits)[door.ID] || len(askBeads(l.Parked)) != 0 {
		t.Errorf("jo's line: as %q, waits %v, parked %v, want as jo and the door's wait alone",
			l.As, askBeads(l.Waits), askBeads(l.Parked))
	}
	if l := lineOf(kay); l.As != "kay" || len(askBeads(l.Waits)) != 1 || !askBeads(l.Waits)[gate.ID] || len(askBeads(l.Parked)) != 0 {
		t.Errorf("kay's line: as %q, waits %v, parked %v, want as kay and the gate's wait alone",
			l.As, askBeads(l.Waits), askBeads(l.Parked))
	}

	// Each answers only the asks to them: jo's answer lands as her, her
	// answer to kay's ask is refused, and kay's lands.
	rev := revOf(t, f, door.ID)
	form = url.Values{"rev": {rev}, "ask": {strconv.FormatUint(doorAsk, 10)}, "action": {"answer"}, "words": {"tonight"}}
	if r := f.postForm("/beads/"+door.ID, form, "Origin", f.ts.URL, header, jo); r.status != http.StatusSeeOther {
		t.Fatalf("jo's answer: status %d, want 303", r.status)
	}
	v, err := f.st.Bead(ctx, door.ID)
	must(t, err)
	if len(v.Asks) != 1 || v.Asks[0].State != "answered" {
		t.Fatalf("the door's ask: %+v", v.Asks)
	}
	notes, err := f.st.Notes(ctx, door.ID)
	must(t, err)
	if last := notes[len(notes)-1]; last.Author != "jo" {
		t.Errorf("the answer is %s's, want jo's", last.Author)
	}
	rev = revOf(t, f, gate.ID)
	form = url.Values{"rev": {rev}, "ask": {strconv.FormatUint(gateAsk, 10)}, "action": {"answer"}, "words": {"no"}}
	r = f.postForm("/beads/"+gate.ID, form, "Origin", f.ts.URL, header, jo)
	if r.status != 200 || !strings.Contains(r.body, "asked of another") {
		t.Errorf("jo answering kay's ask: status %d, want the refusal:\n%s", r.status, r.body)
	}
	v, err = f.st.Bead(ctx, gate.ID)
	must(t, err)
	if len(v.Asks) != 1 || v.Asks[0].State != "open" {
		t.Fatalf("kay's ask: %+v, want it still open", v.Asks)
	}
	if r := f.postForm("/beads/"+gate.ID, form, "Origin", f.ts.URL, header, kay); r.status != http.StatusSeeOther {
		t.Fatalf("kay's answer: status %d, want 303", r.status)
	}

	// And sends on only what waits for them: kay's move is refused, jo's
	// closes the door.
	rev = revOf(t, f, door.ID)
	form = url.Values{"rev": {rev}, "outcome": {"done"}, "note": {"looks right"}}
	r = f.postForm("/beads/"+door.ID, form, "Origin", f.ts.URL, header, kay)
	if r.status != 200 || !strings.Contains(r.body, "does not wait for kay") {
		t.Errorf("kay's move: status %d, want the refusal:\n%s", r.status, r.body)
	}
	if r := f.postForm("/beads/"+door.ID, form, "Origin", f.ts.URL, header, jo); r.status != http.StatusSeeOther {
		t.Fatalf("jo's move: status %d, want 303", r.status)
	}
	b, err := f.st.GetBead(ctx, door.ID)
	must(t, err)
	if b.Status != "closed" {
		t.Errorf("the door is %s in %s, want it closed", b.Status, b.Stage)
	}
}

// In JWT mode the event stream ends at the token's expiry: the client reads
// what was sent and then the end, and reconnects with a fresh token. A
// token already past its expiry — verified within the minute's leeway —
// ends at once.
func TestJWTStreamEndsAtExpiry(t *testing.T) {
	keys := mustJWTTestKeys(t)
	jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
	ts, srv := serveJWT(t, jwks, "", map[string]string{jwtTestEmail: "mckean"})
	header := web.DefaultJWTHeader

	// stream reads a stream under a token expiring at exp until its end,
	// and how long the end took to come.
	stream := func(exp time.Time) (string, time.Duration, error) {
		t.Helper()
		claims := jwtClaims(jwtTestEmail)
		claims["exp"] = exp.Unix()
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/events?format=json&after=0", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(header, signRSA(t, keys.a, "a", claims))
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			body, _ := io.ReadAll(r.Body)
			t.Fatalf("status %d, want 200: %s", r.StatusCode, body)
		}
		type out struct {
			body string
			err  error
		}
		done := make(chan out, 1)
		go func() {
			b, err := io.ReadAll(r.Body)
			done <- out{string(b), err}
		}()
		start := time.Now()
		select {
		case o := <-done:
			return o.body, time.Since(start), o.err
		case <-time.After(15 * time.Second):
			return "", 0, errors.New("the stream outlived the token")
		}
	}
	// The end comes at the expiry, neither at once nor never. Give the
	// request enough time to verify and start the stream before that expiry.
	body, took, err := stream(time.Now().Add(5 * time.Second))
	must(t, err)
	if !strings.HasPrefix(body, "retry: 500\n\n") {
		t.Errorf("the stream sent %q, want the retry and then the end", body)
	}
	if took < 500*time.Millisecond {
		t.Errorf("the stream ended after %v, want it to last the token out", took)
	}
	settle(t, "the stream outlived the token", func() bool { return srv.Streams() == 0 })
	// Past its expiry the stream ends at once, with the retry alone.
	body, _, err = stream(time.Now().Add(-30 * time.Second))
	must(t, err)
	if !strings.HasPrefix(body, "retry: 500\n\n") {
		t.Errorf("a token past its expiry sent %q, want the retry and then the end", body)
	}
	settle(t, "the stream outlived the token", func() bool { return srv.Streams() == 0 })
}

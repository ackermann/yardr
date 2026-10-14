package web_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// serveJWT serves a yard in JWT mode against the stub's keys, carrying the
// header ("" for the default) and the users table.
func serveJWT(t *testing.T, jwks *jwksStub, header string, users map[string]string) (*httptest.Server, *web.Server) {
	t.Helper()
	st := storetest.New(t)
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.UseJWT(context.Background(), web.JWTConfig{
		Issuer: jwtTestIssuer, Audience: jwtTestAudience,
		JWKSURL: jwks.ts.URL, Header: header, Users: users,
	}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return ts, srv
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

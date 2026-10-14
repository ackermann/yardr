package web

// JWT mode is the web view behind an auth gateway (Cloudflare Access,
// oauth2-proxy, Pomerium), where every request but /health carries a token
// the gateway signed. ServeHTTP verifies it here, against the keys fetched
// from the yard's web_jwt_jwks, and maps its email to a yard user through
// web_jwt_users; docs/web.md ("Behind an auth gateway") says what each
// gateway sends.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"yardr.dev/yardr/internal/store"
)

// DefaultJWTHeader is the header carrying the gateway's token when the
// yard's web_jwt_header is unset.
const DefaultJWTHeader = "Cf-Access-Jwt-Assertion"

// JWT bounds: a fetch answers within the timeout or not at all; a key set
// over the bound is no key set; expiry and not-before allow a minute of
// clock skew; the keys are replaced hourly and, on an unknown key id, at
// most once a minute.
const (
	jwtFetchTimeout       = 10 * time.Second
	jwtKeysMax            = 1 << 20
	jwtLeeway             = time.Minute
	jwtRefreshEvery       = time.Hour
	jwtUnknownKIDCooldown = time.Minute
)

// JWTConfig is JWT mode as the yard's web_jwt_* settings shape it: the
// issuer and audience a token must carry, the URL its keys are fetched
// from, the header carrying it, and whose token acts as whom. Users holds
// lower-cased emails (store.ParseWebJWTUsers).
type JWTConfig struct {
	Issuer   string
	Audience string
	JWKSURL  string
	Header   string
	Users    map[string]string
}

// jwtUserKey is the request's verified JWT user (RequestUser).
type jwtUserKey struct{}

// RequestUser is the yard user the request's verified gateway token mapped
// to, "" when the view is not in JWT mode or the request carried no
// verified token (/health, and every request with JWT mode off).
func RequestUser(r *http.Request) string {
	user, _ := r.Context().Value(jwtUserKey{}).(string)
	return user
}

// UseJWT turns JWT mode on: every request but /health must carry a token
// the gateway signed, verified against cfg, whose email maps to a user of
// the yard. It fetches the keys first: JWT mode starts with keys, never
// without. Call it before the server serves.
func (s *Server) UseJWT(ctx context.Context, cfg JWTConfig) error {
	switch {
	case cfg.Issuer == "":
		return errors.New("JWT mode needs an issuer (web_jwt_issuer)")
	case cfg.Audience == "":
		return errors.New("JWT mode needs an audience (web_jwt_audience)")
	case cfg.JWKSURL == "":
		return errors.New("JWT mode needs a JWKS URL (web_jwt_jwks)")
	}
	header := cfg.Header
	if header == "" {
		header = DefaultJWTHeader
	}
	v := &jwtVerifier{
		issuer: cfg.Issuer, audience: cfg.Audience, url: cfg.JWKSURL,
		header: header, bearer: strings.EqualFold(header, "Authorization"),
		users: cfg.Users, log: s.log,
		client: &http.Client{Timeout: jwtFetchTimeout},
		keys:   map[string]any{},
	}
	keys, err := v.fetchKeys(ctx)
	if err != nil {
		return fmt.Errorf("could not fetch the keys: %w", err)
	}
	v.mu.Lock()
	// The first fetch is the start's, not a refresh's: an unknown key id
	// refetches right away, and the cooldown bounds the ones after it.
	v.keys, v.fetched = keys, time.Now()
	v.mu.Unlock()
	s.jwt = v
	return nil
}

// jwtVerifier verifies a gateway's tokens against its cached keys.
type jwtVerifier struct {
	issuer   string
	audience string
	url      string
	header   string
	// bearer strips a Bearer prefix: the token arrives as one on
	// Authorization only.
	bearer bool
	users  map[string]string
	log    *slog.Logger
	client *http.Client
	// mu guards keys, the public keys by key id; fetched, the last fetch
	// that replaced them; attempted, the last fetch whatever came of it;
	// fetching, the in-flight fetch every handler shares; and fetchErr,
	// what the last fetch failed with.
	mu        sync.Mutex
	keys      map[string]any
	fetched   time.Time
	attempted time.Time
	fetching  chan struct{}
	fetchErr  error
}

// verify is the request's mapped user, or the refusal's code and sentence:
// a missing header or an invalid token is 401, an email no user has is
// 403, and no usable keys at all is 503.
func (v *jwtVerifier) verify(r *http.Request) (string, int, string) {
	raw := r.Header.Get(v.header)
	if v.bearer {
		raw = cutBearer(raw)
	}
	if raw == "" {
		return "", http.StatusUnauthorized, "the gateway's token is missing: the " + v.header + " header is not on the request"
	}
	kid, alg, claims, ok := peekToken(raw)
	if !ok {
		return "", http.StatusUnauthorized, "the gateway's token is invalid: malformed"
	}
	// What the gateways sign with: anything else is refused before any
	// key is read, so no token picks its own algorithm.
	switch alg {
	case "RS256", "ES256":
	default:
		return "", http.StatusUnauthorized, "the gateway's token is invalid: signed with " + alg
	}
	// Presence only: the values are checked after the signature proves
	// them, but a token without them cannot verify, so no key is read.
	for _, missing := range []struct{ claim, why string }{
		{"exp", "it never expires"},
		{"aud", "for another audience"},
		{"iss", "another issuer's"},
	} {
		if _, ok := claims[missing.claim]; !ok {
			return "", http.StatusUnauthorized, "the gateway's token is invalid: " + missing.why
		}
	}
	// An hour's keys are replaced when the fetch answers; when it fails
	// the old ones keep serving.
	v.refreshIfDue(r.Context(), jwtRefreshEvery, jwtRefreshEvery)
	key, keys := v.keyFor(r.Context(), kid)
	switch {
	case keys == 0:
		v.mu.Lock()
		err := v.fetchErr
		v.mu.Unlock()
		v.log.Warn("no keys verify the gateway's tokens", "err", err)
		return "", http.StatusServiceUnavailable, "no keys verify the gateway's tokens: none could be fetched"
	case key == nil:
		return "", http.StatusUnauthorized, "the gateway's token is invalid: signed with an unknown key"
	}
	verified := jwt.MapClaims{}
	_, err := v.parser().ParseWithClaims(raw, verified, func(*jwt.Token) (any, error) { return key, nil })
	if err != nil {
		return "", http.StatusUnauthorized, "the gateway's token is invalid: " + invalidWhy(err)
	}
	email, _ := verified["email"].(string)
	if email == "" {
		return "", http.StatusUnauthorized, "the gateway's token is invalid: it names no email"
	}
	// The issuer's email is trusted (docs/web.md states whose): only a
	// claim that says otherwise refuses the token.
	if ev, ok := verified["email_verified"]; ok && ev != true {
		return "", http.StatusUnauthorized, "the gateway's token is invalid: its email is unverified"
	}
	user, ok := v.users[strings.ToLower(email)]
	if !ok {
		return "", http.StatusForbidden, "the gateway's token names " + email + ", and the yard has no user with that email (" + store.WebJWTUsersSetting + ")"
	}
	return user, 0, ""
}

// parser verifies a token's signature, issuer, audience, expiry and
// not-before, with a minute of leeway; a token with no expiry is none the
// gateways mint, and is refused.
func (v *jwtVerifier) parser() *jwt.Parser {
	return jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256", "ES256"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithLeeway(jwtLeeway),
		jwt.WithExpirationRequired(),
	)
}

// peekToken is the key id, the algorithm and the claims the token names,
// before anything of it is trusted: the lookup needs the id, and
// verification still proves the signature.
func peekToken(raw string) (kid, alg string, claims jwt.MapClaims, ok bool) {
	claims = jwt.MapClaims{}
	tok, _, err := new(jwt.Parser).ParseUnverified(raw, claims)
	if err != nil {
		return "", "", nil, false
	}
	alg, _ = tok.Header["alg"].(string)
	if alg == "" {
		return "", "", nil, false
	}
	kid, _ = tok.Header["kid"].(string)
	return kid, alg, claims, true
}

// cutBearer is the token of an Authorization value: the word after Bearer,
// or the value itself when it names no scheme.
func cutBearer(v string) string {
	if len(v) > 7 && strings.EqualFold(v[:6], "bearer") && v[6] == ' ' {
		return strings.TrimSpace(v[7:])
	}
	return v
}

// invalidWhy is why the token failed verification, in the refusal's words.
func invalidWhy(err error) string {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return "expired"
	case errors.Is(err, jwt.ErrTokenNotValidYet), errors.Is(err, jwt.ErrTokenUsedBeforeIssued):
		return "not yet valid"
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return "another issuer's"
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		return "for another audience"
	case errors.Is(err, jwt.ErrTokenMalformed):
		return "malformed"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return "bad signature"
	default:
		return "invalid"
	}
}

// keyFor is the key kid names, fetching once when no cached key is his, so
// a rotation lands without a restart. A token with no kid takes the key
// when exactly one is cached, else none. It also says how many keys are
// cached: none at all is the 503, an unknown one of some the 401.
func (v *jwtVerifier) keyFor(ctx context.Context, kid string) (any, int) {
	if key, n := v.lookup(kid); key != nil || n == 0 {
		return key, n
	}
	// An unknown key id: at most one fetch a minute answers these, shared
	// by every handler, so misses cannot burst the issuer; a failed
	// refresh keeps the cached keys serving.
	v.refreshIfDue(ctx, 0, jwtUnknownKIDCooldown)
	return v.lookup(kid)
}

// lookup is the key kid names among the cached keys, and how many those
// are.
func (v *jwtVerifier) lookup(kid string) (any, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if kid == "" && len(v.keys) == 1 {
		for _, key := range v.keys {
			return key, 1
		}
	}
	return v.keys[kid], len(v.keys)
}

// refreshIfDue replaces the cached keys when they are older than maxAge and
// no fetch ran in the last cooldown: one in-flight fetch is shared by every
// handler, and the cached keys keep serving when it fails. A refresh that
// carries no usable key is a failure too.
func (v *jwtVerifier) refreshIfDue(ctx context.Context, maxAge, cooldown time.Duration) {
	v.mu.Lock()
	for v.fetching != nil {
		done := v.fetching
		v.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
		v.mu.Lock()
	}
	if time.Since(v.fetched) < maxAge || time.Since(v.attempted) < cooldown {
		v.mu.Unlock()
		return
	}
	v.attempted = time.Now()
	v.fetching = make(chan struct{})
	v.mu.Unlock()
	keys, err := v.fetchKeys(ctx)
	v.mu.Lock()
	if v.fetchErr = err; err != nil {
		v.log.Warn("the gateway's keys could not be refreshed; the cached keys keep serving", "err", err)
	} else {
		v.keys, v.fetched = keys, time.Now()
	}
	close(v.fetching)
	v.fetching = nil
	v.mu.Unlock()
}

// fetchKeys is the key set at the verifier's URL: fetched with a short
// timeout, read up to the bound, and held to RSA and EC public keys.
func (v *jwtVerifier) fetchKeys(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yardr-web")
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, jwtKeysMax+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > jwtKeysMax {
		return nil, errors.New("over the 1 MiB bound")
	}
	return parseJWKS(raw)
}

// parseJWKS is the set's RSA and EC public keys by key id. Keys of any
// other kind, and keys with no id in a set of more than one, are skipped:
// a token names its key, and one key needs no name.
func parseJWKS(raw []byte) (map[string]any, error) {
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("not a key set: %w", err)
	}
	var list []struct {
		kid string
		key any
	}
	for _, k := range set.Keys {
		var (
			key any
			err error
		)
		switch k.Kty {
		case "RSA":
			key, err = jwkRSA(k.N, k.E)
		case "EC":
			key, err = jwkEC(k.Crv, k.X, k.Y)
		default:
			continue
		}
		if err != nil {
			continue
		}
		list = append(list, struct {
			kid string
			key any
		}{k.Kid, key})
	}
	out := map[string]any{}
	for _, u := range list {
		if u.kid == "" && len(list) != 1 {
			continue
		}
		out[u.kid] = u.key
	}
	if len(out) == 0 {
		return nil, errors.New("no usable keys in it (RSA or EC keys)")
	}
	return out, nil
}

// jwkRSA is the key a JWK's RSA modulus and exponent name.
func jwkRSA(n, e string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil || len(nb) == 0 {
		return nil, errors.New("no modulus")
	}
	eb, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil || len(eb) == 0 {
		return nil, errors.New("no exponent")
	}
	ei := new(big.Int).SetBytes(eb)
	if !ei.IsInt64() || ei.Int64() <= 0 {
		return nil, errors.New("no exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(ei.Int64())}, nil
}

// jwkEC is the key a JWK's curve and coordinates name.
func jwkEC(crv, x, y string) (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("curve %s", crv)
	}
	xb, err := base64.RawURLEncoding.DecodeString(x)
	if err != nil || len(xb) == 0 {
		return nil, errors.New("no x")
	}
	yb, err := base64.RawURLEncoding.DecodeString(y)
	if err != nil || len(yb) == 0 {
		return nil, errors.New("no y")
	}
	key := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(xb), Y: new(big.Int).SetBytes(yb)}
	if !curve.IsOnCurve(key.X, key.Y) {
		return nil, fmt.Errorf("not a point on %s", crv)
	}
	return key, nil
}

package web

// JWT mode is the web view behind an auth gateway (Cloudflare Access,
// oauth2-proxy, Pomerium), where every request but /health carries a token
// the gateway signed. ServeHTTP verifies it here, against the keys fetched
// from the yard's web_jwt_jwks, and maps its email to a yard user through
// web_jwt_users; docs/web.md ("Behind an auth gateway") says what each
// gateway sends. It keeps beside the user the time the token says the
// gateway last authenticated the person (auth_time), which is all that
// tells a session signed in just now from a standing one (freshSession).

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
	"math"
	"math/big"
	"net"
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
// over the bound is no key set; expiry, not-before and an authentication
// time ahead of now allow a minute of clock skew; the keys are replaced
// hourly and, on an unknown key id, at most once a minute.
const (
	jwtFetchTimeout       = 10 * time.Second
	jwtKeysMax            = 1 << 20
	jwtLeeway             = time.Minute
	jwtRefreshEvery       = time.Hour
	jwtUnknownKIDCooldown = time.Minute
)

// authTimeMax is the last second an auth_time may name, 9999-12-31T23:59:59Z,
// the last that RFC 3339 writes: a claim past it names no time a gateway
// signed, and is kept from the conversion to a time.
const authTimeMax = 253402300799

// JWTConfig is JWT mode as the yard's web_jwt_* settings shape it: the
// issuer and audience a token must carry, the URL its keys are fetched
// from, the header carrying it, and whose token acts as whom. Users holds
// lower-cased emails (store.ParseWebJWTUsers).
//
// PrivilegedSessionMaxAge is how long after a person last authenticated
// at the gateway the view still calls their session fresh. It is the
// gateway's own limit, Kratos's privileged_session_max_age on a hosted
// yard, copied here by whoever configures both and never chosen by the
// yard; zero is unset, and with it no session is fresh.
type JWTConfig struct {
	Issuer                  string
	Audience                string
	JWKSURL                 string
	Header                  string
	Users                   map[string]string
	PrivilegedSessionMaxAge time.Duration
}

// jwtIdentity is what a verified gateway token resolves to: the yard user
// its email maps to, when the token expires, and when the gateway last
// authenticated the person (its auth_time claim), the zero time where the
// token names none. It rides the request's context under jwtUserKey.
type jwtIdentity struct {
	user     string
	exp      time.Time
	authTime time.Time
}

// jwtUserKey is the request's verified JWT identity (RequestExpiry).
type jwtUserKey struct{}

// jwtIdentityOf is the identity the request's verified gateway token
// resolved to, the zero one when the view is not in JWT mode or the
// request carried no verified token (/health, and every request with JWT
// mode off).
func jwtIdentityOf(ctx context.Context) jwtIdentity {
	id, _ := ctx.Value(jwtUserKey{}).(jwtIdentity)
	return id
}

// RequestExpiry is when the request's verified gateway token expires, the
// zero time when the request carried no verified token.
func RequestExpiry(r *http.Request) time.Time { return jwtIdentityOf(r.Context()).exp }

// freshAt is whether the identity's authentication time counts as a fresh
// sign-in at now: the window has not run out on it, and it is later than
// since, the time a card's challenge was raised. A token with no such time
// is never fresh. Where the window is unset, or since is zero because no
// challenge was raised, there is nothing to be fresh against, and both read
// as not fresh, never as always. A time ahead of now by more than the skew
// jwtLeeway allows is none a gateway signed.
func (id jwtIdentity) freshAt(now time.Time, window time.Duration, since time.Time) bool {
	switch {
	case id.authTime.IsZero(), window <= 0, since.IsZero():
		return false
	case id.authTime.After(now.Add(jwtLeeway)):
		return false
	}
	return !id.authTime.Before(now.Add(-window)) && id.authTime.After(since)
}

// freshSession is whether the request is a fresh sign-in of an operator
// after since: it acts as an operator, and the gateway's token says it
// authenticated them no longer ago than its privileged-session window and
// later than since. A standing session is none however new its token is:
// the authentication time is the gateway's word for when the person last
// proved who they are, and a token's issue time says only when it was
// minted. Whom a card is asked of is for the gate that approves it.
func (s *Server) freshSession(ctx context.Context, since time.Time) bool {
	if s.jwt == nil || !s.isOperator(ctx) {
		return false
	}
	return jwtIdentityOf(ctx).freshAt(time.Now(), s.jwt.privilegedSessionMaxAge, since)
}

// UseJWT turns JWT mode on: every request but /health must carry a token
// the gateway signed, verified against cfg, whose email maps to a user of
// the yard, and each request acts as its own user. It registers the POST
// routes, as ActAs does — registration is by mode, identity is by request —
// and fetches the keys first: JWT mode starts with keys, never without.
// Call it before the server serves.
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
		users: cfg.Users, privilegedSessionMaxAge: cfg.PrivilegedSessionMaxAge, log: s.log,
		client: &http.Client{Timeout: jwtFetchTimeout, CheckRedirect: checkJWKSRedirect},
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
	s.postRoutes()
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
	// privilegedSessionMaxAge is JWTConfig's, read by freshSession.
	privilegedSessionMaxAge time.Duration
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

// verify is the request's identity, or the refusal's code and sentence:
// a missing header or an invalid token is 401, an email no user has is
// 403, and no usable keys at all is 503.
func (v *jwtVerifier) verify(r *http.Request) (jwtIdentity, int, string) {
	deny := func(code int, msg string) (jwtIdentity, int, string) { return jwtIdentity{}, code, msg }
	raw := r.Header.Get(v.header)
	if v.bearer {
		raw = cutBearer(raw)
	}
	if raw == "" {
		return deny(http.StatusUnauthorized, "the gateway's token is missing: the "+v.header+" header is not on the request")
	}
	kid, alg, claims, ok := peekToken(raw)
	if !ok {
		return deny(http.StatusUnauthorized, "the gateway's token is invalid: malformed")
	}
	// What the gateways sign with: anything else is refused before any
	// key is read, so no token picks its own algorithm.
	switch alg {
	case "RS256", "ES256":
	default:
		return deny(http.StatusUnauthorized, "the gateway's token is invalid: signed with "+alg)
	}
	// Presence only: the values are checked after the signature proves
	// them, but a token without them cannot verify, so no key is read.
	for _, missing := range []struct{ claim, why string }{
		{"exp", "it never expires"},
		{"aud", "for another audience"},
		{"iss", "another issuer's"},
	} {
		if _, ok := claims[missing.claim]; !ok {
			return deny(http.StatusUnauthorized, "the gateway's token is invalid: "+missing.why)
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
		return deny(http.StatusServiceUnavailable, "no keys verify the gateway's tokens: none could be fetched")
	case key == nil:
		return deny(http.StatusUnauthorized, "the gateway's token is invalid: signed with an unknown key")
	}
	verified := jwt.MapClaims{}
	_, err := v.parser().ParseWithClaims(raw, verified, func(*jwt.Token) (any, error) { return key, nil })
	if err != nil {
		return deny(http.StatusUnauthorized, "the gateway's token is invalid: "+invalidWhy(err))
	}
	email, _ := verified["email"].(string)
	if email == "" {
		return deny(http.StatusUnauthorized, "the gateway's token is invalid: it names no email")
	}
	// The issuer's email is trusted (docs/web.md states whose): only a
	// claim that says otherwise refuses the token.
	if ev, ok := verified["email_verified"]; ok && ev != true {
		return deny(http.StatusUnauthorized, "the gateway's token is invalid: its email is unverified")
	}
	user, ok := v.users[strings.ToLower(email)]
	if !ok {
		return deny(http.StatusForbidden, "the gateway's token names "+email+", and the yard has no user with that email ("+store.WebJWTUsersSetting+")")
	}
	// Expiry was required and verified above; the stream closes at it.
	var exp time.Time
	if at, _ := verified.GetExpirationTime(); at != nil {
		exp = at.Time
	}
	return jwtIdentity{user: user, exp: exp, authTime: authTimeOf(verified)}, 0, ""
}

// authTimeOf is when the token says the gateway last authenticated the
// person: its auth_time claim, a number of seconds since the epoch (OpenID
// Connect Core 1.0, section 2). The zero time is none: no claim, one that is
// not a number, or one that names no time a gateway signs (the epoch or
// before, or past authTimeMax). No other claim stands in for it: a token's
// iat says when it was minted, and a standing session mints new tokens all
// day. A claim that cannot be read refuses nothing, since gateways whose
// tokens carry none are served all the same.
func authTimeOf(claims jwt.MapClaims) time.Time {
	secs, ok := claims["auth_time"].(float64)
	// A positive test, so that a NaN fails as well.
	if !ok || !(secs > 0 && secs <= authTimeMax) {
		return time.Time{}
	}
	whole, frac := math.Modf(secs)
	return time.Unix(int64(whole), int64(frac*float64(time.Second)))
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
	// The fetch is every waiter's, not the request's that started it: a
	// caller who hangs up does not abort it (the client's timeout bounds
	// it), or one cancelled miss a minute would keep a rotation out.
	keys, err := v.fetchKeys(context.WithoutCancel(ctx))
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

// checkJWKSRedirect keeps a redirect to another insecure URL from undoing
// the web_jwt_jwks setting's HTTPS requirement. A loopback HTTP endpoint is
// only for tests, and the router's is a hosted yard's keys; neither can
// redirect key retrieval to a public HTTP host or downgrade an HTTPS fetch.
func checkJWKSRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme == "https" {
		return nil
	}
	if req.URL.Scheme != "http" || (!jwksLoopbackHost(req.URL.Hostname()) && !jwksRouterHost(req.URL.Hostname())) {
		return errors.New("refusing an HTTP redirect to a non-loopback JWKS URL")
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" {
		return errors.New("refusing an HTTPS-to-HTTP JWKS redirect")
	}
	return nil
}

func jwksLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// jwksRouterHost reports whether host is the hosted router's name, whose
// keys the web_jwt_jwks setting lets come over plain http.
func jwksRouterHost(host string) bool {
	return strings.EqualFold(host, "router")
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
	// ParseUncompressedPublicKey takes the point at the field's width and
	// refuses one that is off the curve or is the point at infinity, as the
	// IsOnCurve this replaces did. A coordinate is the integer it names,
	// though, and a set whose producer dropped its leading zero bytes, or
	// added some, has always been taken: so each goes in at the field's width
	// unless the integer has no room in the field.
	bits := curve.Params().BitSize
	xi, yi := new(big.Int).SetBytes(xb), new(big.Int).SetBytes(yb)
	if xi.BitLen() > bits || yi.BitLen() > bits {
		return nil, fmt.Errorf("not a point on %s", crv)
	}
	size := (bits + 7) / 8
	point := make([]byte, 1+2*size)
	point[0] = 4 // uncompressed
	xi.FillBytes(point[1 : 1+size])
	yi.FillBytes(point[1+size:])
	key, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil, fmt.Errorf("not a point on %s", crv)
	}
	return key, nil
}

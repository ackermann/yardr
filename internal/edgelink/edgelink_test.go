package edgelink

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/yardkey"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// edgeAt serves a stand-in edge over TLS, as the real one is reached: the
// client it returns trusts the stand-in's certificate and no other.
func edgeAt(t *testing.T) (*standin.Edge, *httptest.Server) {
	t.Helper()
	edge, err := standin.NewEdge()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(edge)
	t.Cleanup(srv.Close)
	return edge, srv
}

// clock moves a minute each time a request is signed by it, and is what the
// stand-in judges the request by, so a test sees what the yard's signature
// says without waiting for it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Minute)
	return c.t
}

func (c *clock) Peek() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A yard fetches the edge's key, enrols with a code, shows the code, asks
// again until the person has approved it and is given its handle. Every
// call is signed to the key that was fetched, and at its own moment: the
// edge refuses a signature more than MaxSkew old, and the person has longer
// than that to approve.
func TestEnrolIsSignedToTheFetchedKeyAtEachCallsOwnMoment(t *testing.T) {
	t.Parallel()
	edge, srv := edgeAt(t)
	clk := &clock{t: time.Now()}
	edge.Behave(func(b *standin.EdgeBehaviour) { b.ApproveAfter, b.Now = 3, clk.Peek })
	c := &Client{URL: srv.URL, HTTP: srv.Client(), Poll: time.Millisecond, Now: clk.Now}
	yard := newKey(t)

	key, err := c.Key(context.Background())
	if err != nil || key != edge.PublicKey() {
		t.Fatalf("Key = %q, %v; want the edge's %q", key, err, edge.PublicKey())
	}
	en, err := c.Enrol(context.Background(), key, yard, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if en.Code != "WQ4F-7K2M" || en.Handle != "" || time.Until(en.Expires) < 9*time.Minute {
		t.Fatalf("after the first call: %+v, want the code and no handle", en)
	}
	if err := en.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if en.Handle != "mckean" || en.Email != "chris@mckean.co" {
		t.Errorf("approved as %q by %q, want mckean by chris@mckean.co", en.Handle, en.Email)
	}
	calls := edge.State().Enrols
	if len(calls) != 4 {
		t.Fatalf("the edge took %d enrolling calls, want the first and three more while the person approved", len(calls))
	}
	for i, call := range calls {
		if call.Key != yardkey.Public(yard) || call.Name != "laptop" {
			t.Errorf("call %d came from %q as %q, want this yard's key and name", i, call.Key, call.Name)
		}
		if want := fmt.Sprint(clk.Peek().Add(time.Duration(i-3) * time.Minute).Unix()); call.Time != want {
			t.Errorf("call %d is signed at %s, want %s: its own moment, a minute after the one before", i, call.Time, want)
		}
	}
}

// answersSigned serves the same signed answer, to every call, as the edge
// whose key is key would.
func answersSigned(t *testing.T, key ed25519.PrivateKey, status int, v any) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := json.Marshal(v)
		w.Header().Set(yardkey.HeaderSig, yardkey.SignResponse(key, status, r.Header.Get(yardkey.HeaderSig), body))
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The expiry of a code is shown to the person and decides nothing: an edge
// that gives it in another form, or not at all, still has its code taken.
func TestEnrolTakesACodeWhateverItsExpiryIs(t *testing.T) {
	t.Parallel()
	edgeKey := newKey(t)
	for name, expires := range map[string]any{"seconds": 600, "words": "soon", "null": nil, "a time": "2026-10-20T09:24:03Z"} {
		srv := answersSigned(t, edgeKey, http.StatusOK, map[string]any{"code": "WQ4F-7K2M", "expires": expires})
		c := &Client{URL: srv.URL, HTTP: srv.Client()}
		en, err := c.Enrol(context.Background(), yardkey.Public(edgeKey), newKey(t), "laptop")
		if err != nil || en.Code != "WQ4F-7K2M" {
			t.Errorf("%s: Enrol = %+v, %v; want the code taken", name, en, err)
			continue
		}
		if known := !en.Expires.IsZero(); known != (name == "a time") {
			t.Errorf("%s: the expiry is %v, known %v", name, en.Expires, known)
		}
	}
	srv := answersSigned(t, edgeKey, http.StatusOK, map[string]any{"code": "WQ4F-7K2M"})
	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	if en, err := c.Enrol(context.Background(), yardkey.Public(edgeKey), newKey(t), "laptop"); err != nil || en.Code == "" || !en.Expires.IsZero() {
		t.Errorf("Enrol, with no expiry at all = %+v, %v", en, err)
	}
}

// A yard the edge knows already is answered with its handle at once, and has
// nothing to wait for.
func TestEnrolAnsweredWithAHandleNeedsNoWait(t *testing.T) {
	t.Parallel()
	edge, srv := edgeAt(t)
	edge.Behave(func(b *standin.EdgeBehaviour) { b.ApproveAfter = 0 })
	c := &Client{URL: srv.URL, HTTP: srv.Client(), Poll: time.Millisecond}
	en, err := c.Enrol(context.Background(), edge.PublicKey(), newKey(t), "laptop")
	if err != nil || en.Handle != "mckean" || en.Code != "" {
		t.Fatalf("Enrol = %+v, %v; want the handle and no code", en, err)
	}
	if err := en.Wait(context.Background()); err != nil || len(edge.State().Enrols) != 1 {
		t.Errorf("Wait = %v after %d calls, want it to ask no more", err, len(edge.State().Enrols))
	}
}

// The key is fetched over HTTPS, and plain http is refused off this machine
// before anything is sent: over http the key would be whoever's is between.
// Loopback is a stand-in on this machine, and is taken.
func TestKeyRefusesPlainHTTPExceptOnLoopback(t *testing.T) {
	t.Parallel()
	nothing := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("a request was made to %s", r.URL)
		return nil, errors.New("no network in this test")
	})
	for _, url := range []string{"http://my.yardr.dev", "http://192.0.2.7:8080", "http://edge.example.net"} {
		c := &Client{URL: url, HTTP: &http.Client{Transport: nothing}}
		if _, err := c.Key(context.Background()); err == nil || !strings.Contains(err.Error(), "http is for a stand-in on this machine only") {
			t.Errorf("Key at %s = %v, want plain http refused", url, err)
		}
	}
	edge, err := standin.NewEdge()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(edge)
	t.Cleanup(srv.Close)
	c := &Client{URL: srv.URL}
	if key, err := c.Key(context.Background()); err != nil || key != edge.PublicKey() {
		t.Errorf("Key at the loopback stand-in %s = %q, %v", srv.URL, key, err)
	}
}

// A server whose certificate this machine does not trust is not the edge,
// and nothing is signed to it: the enrolling call never reaches it.
func TestKeyRefusesACertificateThisMachineDoesNotTrust(t *testing.T) {
	t.Parallel()
	edge, srv := edgeAt(t)
	// No client of the test's: the system's roots, which do not hold the
	// stand-in's certificate.
	c := &Client{URL: srv.URL}
	_, err := c.Key(context.Background())
	var cert *tls.CertificateVerificationError
	if !errors.As(err, &cert) {
		t.Fatalf("Key against a certificate nobody vouches for = %v, want a certificate that does not verify", err)
	}
	if !strings.Contains(err.Error(), "is not trusted by this machine") || !strings.Contains(err.Error(), "nothing was signed") {
		t.Errorf("the refusal does not say that the certificate is not trusted and that nothing was signed: %v", err)
	}
	if n := len(edge.State().Enrols); n != 0 {
		t.Errorf("the stand-in took %d enrolling calls", n)
	}
}

// An answer signed by a key other than the one fetched is refused, whatever
// it says: the first call and every one after it.
func TestEnrolRefusesAnAnswerSignedByAnotherKey(t *testing.T) {
	t.Parallel()
	edge, srv := edgeAt(t)
	c := &Client{URL: srv.URL, HTTP: srv.Client(), Poll: time.Millisecond}
	key, err := c.Key(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	other := newKey(t)
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Signer = other })
	if _, err := c.Enrol(context.Background(), key, newKey(t), "laptop"); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("Enrol, answered by another key = %v, want ErrUnsigned", err)
	}

	// Signed rightly at first and wrongly later: the edge that was fetched is
	// not the one that answers the poll.
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Signer, b.ApproveAfter = b.Served, 5 })
	en, err := c.Enrol(context.Background(), key, newKey(t), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Signer = other })
	if err := en.Wait(context.Background()); !errors.Is(err, ErrUnsigned) || en.Handle != "" {
		t.Errorf("Wait, answered by another key = %v with handle %q, want ErrUnsigned and no handle", err, en.Handle)
	}
}

// An answer is bound to the call it answers: one the edge signed to an
// earlier call is no answer to a later one.
func TestEnrolRefusesAnAnswerSignedToAnotherCall(t *testing.T) {
	t.Parallel()
	edge, err := standin.NewEdge()
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Now()}
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Now = clk.Peek })
	var mu sync.Mutex
	var firstBody []byte
	var firstSig string
	// Something between the yard and the edge that plays the edge's first
	// answer, whole, to every call after it.
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if firstBody == nil {
			rec := httptest.NewRecorder()
			edge.ServeHTTP(rec, r)
			firstBody, firstSig = rec.Body.Bytes(), rec.Header().Get(yardkey.HeaderSig)
		}
		w.Header().Set(yardkey.HeaderSig, firstSig)
		w.Write(firstBody)
	}))
	t.Cleanup(front.Close)
	// WaitFor keeps a regression from waiting out the default: a replayed
	// code is never approved.
	c := &Client{URL: front.URL, HTTP: front.Client(), Poll: time.Millisecond, Now: clk.Now, WaitFor: 5 * time.Second}
	en, err := c.Enrol(context.Background(), edge.PublicKey(), newKey(t), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := en.Wait(context.Background()); !errors.Is(err, ErrUnsigned) {
		t.Errorf("Wait, answered with the first answer again = %v, want ErrUnsigned", err)
	}
}

// A redirect is not followed: the address is the edge's own, and a call that
// went on to another would take the yard's trust, or its signature, there.
func TestKeyDoesNotFollowARedirect(t *testing.T) {
	t.Parallel()
	other, otherSrv := edgeAt(t)
	moved := httptest.NewTLSServer(http.RedirectHandler(otherSrv.URL+KeyPath, http.StatusFound))
	t.Cleanup(moved.Close)
	c := &Client{URL: moved.URL, HTTP: moved.Client()}
	if key, err := c.Key(context.Background()); err == nil || !strings.Contains(err.Error(), "answered 302") {
		t.Errorf("Key behind a redirect = %q, %v, want it refused as a 302", key, err)
	}
	if n := other.State().KeyFetches; n != 0 {
		t.Errorf("the redirect was followed: %d fetches of the key at %s", n, otherSrv.URL)
	}
}

// What answers at /edge/key must be the key as text, and nothing else is
// taken for one.
func TestKeyRefusesWhatIsNoKey(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]string{
		"a page":        "<html>Welcome</html>",
		"a JSON object": `{"key":"` + yardkey.Public(newKey(t)) + `"}`,
		"too short":     "AAAA",
		"nothing":       "",
		"too big":       strings.Repeat("A", maxAnswer+1),
	} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, answer) }))
		t.Cleanup(srv.Close)
		c := &Client{URL: srv.URL, HTTP: srv.Client()}
		if key, err := c.Key(context.Background()); err == nil || !strings.Contains(err.Error(), "is no edge's key") {
			t.Errorf("%s: Key = %q, %v, want it refused as no key", name, key, err)
		}
	}
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	if _, err := c.Key(context.Background()); err == nil || !strings.Contains(err.Error(), "answered 404") {
		t.Errorf("Key at a server with no edge = %v, want its 404 said", err)
	}
}

// The edge's refusals reach the person in its words, cut to what a terminal
// shows; an answer that is not signed by the edge, in none, since its words
// would be anyone's.
func TestEnrolRefusalsAndAnswersItDoesNotRead(t *testing.T) {
	t.Parallel()
	edgeKey := newKey(t)
	signed := func(status int, v any) *httptest.Server { return answersSigned(t, edgeKey, status, v) }
	unsigned := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "origin down", http.StatusBadGateway) }))
	t.Cleanup(unsigned.Close)
	for name, tc := range map[string]struct {
		srv  *httptest.Server
		want string
	}{
		"a refusal":           {signed(http.StatusGone, map[string]string{"refused": "the code expired"}), "the edge refused this yard: the code expired (410)"},
		"a control character": {signed(http.StatusForbidden, map[string]string{"refused": "no\x1b[2J\x07 thanks"}), "the edge refused this yard: no[2J thanks (403)"},
		"no reason":           {signed(http.StatusForbidden, map[string]string{}), "the edge refused this yard (403)"},
		"neither":             {signed(http.StatusOK, map[string]string{}), "holds neither a code nor a handle"},
		"a code too long":     {signed(http.StatusOK, map[string]string{"code": strings.Repeat("X", maxText+1)}), "not text a person can type"},
		"a code with escapes": {signed(http.StatusOK, map[string]string{"code": "AB\x1b[2JCD"}), "not text a person can type"},
		"not an object":       {signed(http.StatusOK, []string{"code"}), "no JSON object"},
		"a bad gateway":       {unsigned, "answered 502, not signed with the key this yard pinned"},
	} {
		c := &Client{URL: tc.srv.URL, HTTP: tc.srv.Client()}
		_, err := c.Enrol(context.Background(), yardkey.Public(edgeKey), newKey(t), "laptop")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Enrol = %v, want %q", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "origin down") {
			t.Errorf("%s: an unsigned answer's text reached the error: %v", name, err)
		}
	}
	gone := signed(http.StatusGone, map[string]string{"refused": "expired"})
	c := &Client{URL: gone.URL, HTTP: gone.Client()}
	var refused *Refused
	if _, err := c.Enrol(context.Background(), yardkey.Public(edgeKey), newKey(t), "laptop"); !errors.As(err, &refused) || refused.Status != http.StatusGone || refused.Reason != "expired" {
		t.Errorf("Enrol, refused with 410 = %v, want a *Refused with the status and the reason", err)
	}
}

// The wait ends with the caller's context, and with the edge's refusal when
// it says the code is gone; until then it keeps asking.
func TestWaitEndsWithTheCallerOrTheEdgesRefusal(t *testing.T) {
	t.Parallel()
	edge, srv := edgeAt(t)
	edge.Behave(func(b *standin.EdgeBehaviour) { b.ApproveAfter = 1 << 20 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hc, asked := srv.Client(), 0
	base := hc.Transport
	hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if asked++; asked == 4 {
			cancel()
		}
		return base.RoundTrip(r)
	})
	c := &Client{URL: srv.URL, HTTP: hc, Poll: time.Millisecond}
	en, err := c.Enrol(ctx, edge.PublicKey(), newKey(t), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := en.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait, cancelled = %v, want context.Canceled", err)
	}
	if asked < 4 {
		t.Errorf("Wait asked %d times before it was cancelled, want it to keep asking", asked)
	}

	// The edge says the code is gone: a refusal, and the wait is over.
	c = &Client{URL: srv.URL, HTTP: srv.Client(), Poll: time.Millisecond}
	en, err = c.Enrol(context.Background(), edge.PublicKey(), newKey(t), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Refuse = http.StatusGone })
	var refused *Refused
	if err := en.Wait(context.Background()); !errors.As(err, &refused) || refused.Status != http.StatusGone || refused.Reason != "expired" {
		t.Errorf("Wait, with the code expired = %v, want the edge's 410 expired", err)
	}

	// The person never comes: the wait is over when its time is up, which is
	// the wait's own end and not the caller's.
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Refuse = 0 })
	c = &Client{URL: srv.URL, HTTP: srv.Client(), Poll: time.Millisecond, WaitFor: 20 * time.Millisecond}
	en, err = c.Enrol(context.Background(), edge.PublicKey(), newKey(t), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := en.Wait(context.Background()); !errors.Is(err, ErrNotApproved) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Wait, with no one approving = %v, want ErrNotApproved", err)
	}
}

// A yard that disconnects asks the edge to forget its key, signed as the
// enrolment was; the edge having no row for it is as good as having dropped
// it, and anything else is a refusal the caller says.
func TestDrop(t *testing.T) {
	t.Parallel()
	edge, srv := edgeAt(t)
	c := &Client{URL: srv.URL, HTTP: srv.Client()}
	yard := newKey(t)

	if err := c.Drop(context.Background(), edge.PublicKey(), yard); err != nil {
		t.Fatalf("Drop = %v", err)
	}
	if got := edge.State().Drops; len(got) != 1 || got[0] != yardkey.Public(yard) {
		t.Errorf("the edge was asked to drop %v, want this yard's key", got)
	}
	for _, tc := range []struct {
		status int
		want   string
	}{{http.StatusNotFound, ""}, {http.StatusForbidden, "the edge refused this yard (403)"}, {http.StatusInternalServerError, "(500)"}} {
		edge.Behave(func(b *standin.EdgeBehaviour) { b.DisconnectStatus = tc.status })
		err := c.Drop(context.Background(), edge.PublicKey(), yard)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("Drop, answered %d = %v, want %q", tc.status, err, tc.want)
		}
	}
	// An edge that is not the one pinned is not taken at its word.
	other := newKey(t)
	edge.Behave(func(b *standin.EdgeBehaviour) { b.DisconnectStatus, b.Signer = 0, other })
	if err := c.Drop(context.Background(), edge.PublicKey(), yard); !errors.Is(err, ErrUnsigned) {
		t.Errorf("Drop, answered by another key = %v, want ErrUnsigned", err)
	}
}

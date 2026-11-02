package yards

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/testload"
	"yardr.dev/yardr/internal/yardkey"
)

// heldYard is a receiver and its peer b, whose pulls it holds. The tests below
// serve its handler b's signed pulls straight and read the receiver's map of
// the pulls it holds open, as only a test in this package can.
type heldYard struct {
	rc   *Receiver
	to   string             // the receiver's public key, which b signs its pulls for
	peer ed25519.PrivateKey // b's key
}

func newHeldYard(t *testing.T) *heldYard {
	t.Helper()
	api, own := storetest.NewOwned(t)
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "a.key")
	a, _, err := yardkey.Ensure(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := yardkey.Ensure(filepath.Join(dir, "b.key"))
	if err != nil {
		t.Fatal(err)
	}
	// b has no URL at a, which then holds b's pulls rather than answering
	// them at once as it does a peer it posts to.
	if _, err = api.AddPeer(context.Background(), store.Peer{Name: "b", Key: store.Public(b)}, "jo"); err != nil {
		t.Fatal(err)
	}
	return &heldYard{rc: NewReceiver(own, keyFile, nil), to: store.Public(a), peer: b}
}

// heldPull is a pull of b's served to the handler: over closes once the
// handler has returned, and kill cancels the request's context as a closed
// connection cancels it.
type heldPull struct {
	rec  *httptest.ResponseRecorder
	kill context.CancelFunc
	over chan struct{}
}

// pull serves the handler a signed pull of b's in a goroutine. It asks the
// longest wait a pull may: it is far from over when its asker goes.
func (y *heldYard) pull(t *testing.T) *heldPull {
	t.Helper()
	uri := Path + "?after=0&wait=60"
	ts, sig := yardkey.SignRequest(y.peer, y.to, http.MethodGet, uri, time.Now(), nil)
	asker, kill := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, uri, nil).WithContext(asker)
	req.Header.Set(yardkey.HeaderKey, store.Public(y.peer))
	req.Header.Set(yardkey.HeaderTime, ts)
	req.Header.Set(yardkey.HeaderSig, sig)
	p := &heldPull{rec: httptest.NewRecorder(), kill: kill, over: make(chan struct{})}
	go func() { defer close(p.over); y.rc.Handler().ServeHTTP(p.rec, req) }()
	t.Cleanup(func() { kill(); <-p.over })
	return p
}

// held is how many pulls the receiver holds open.
func (y *heldYard) held() int {
	y.rc.mu.Lock()
	defer y.rc.mu.Unlock()
	return len(y.rc.held)
}

// heldFuse is how long a wait on a held pull is given: a fuse against a hang,
// not a window the effect has to fit in.
func heldFuse() time.Duration { return testload.Scale(2 * time.Minute) }

// awaitHeld waits for the receiver to hold one pull, which is p; p over before
// that fails the test with what it answered.
func (y *heldYard) awaitHeld(t *testing.T, p *heldPull) {
	t.Helper()
	fuse := heldFuse()
	for end := time.Now().Add(fuse); y.held() != 1; time.Sleep(time.Millisecond) {
		select {
		case <-p.over:
			t.Fatalf("the pull was over before it was held: %d %s", p.rec.Code, p.rec.Body)
		default:
		}
		if time.Now().After(end) {
			t.Fatalf("the pull was not held within %s", fuse)
		}
	}
}

// A pull held for a peer is forgotten when its asker goes away. Left in the
// receiver's map it would count against maxHeld until the peer asked again,
// and a peer that never does would keep the slot for good. No answer shows
// it: the peer's next pull replaces its own entry whether or not the old one
// was released, so the dispatch tests, which see only answers, cannot tell.
// The map is read here, in the package that owns it, after the handler is
// served the request straight with the context a closed connection cancels.
func TestHeldPullForgottenWhenItsAskerGoesAway(t *testing.T) {
	y := newHeldYard(t)
	p := y.pull(t)
	y.awaitHeld(t, p)
	p.kill()
	fuse := heldFuse()
	select {
	case <-p.over:
	case <-time.After(fuse):
		t.Fatalf("the pull was not over within %s of its asker going away", fuse)
	}
	// ServeHTTP returns after the handler's deferred calls, so a release that
	// is going to happen has happened by now, and the count needs no wait.
	if n := y.held(); n != 0 {
		t.Fatalf("the receiver holds %d pulls after the only one's asker went away, want none", n)
	}
}

// A pull the same peer's next has ended releases as its handler returns, when
// the next has already taken its place in the receiver's map, and must leave
// that place alone. A release that deleted the peer's entry whatever it held
// would have the receiver forget the peer's latest pull while it waits: a
// third pull would not end it (a peer has one held, see hold), and it would
// not count against maxHeld. The first pull is answered empty whichever way
// it releases and the second goes on waiting, so the map is read here, as
// above, once the first handler has returned.
func TestHeldPullSupersededLeavesItsSuccessorHeld(t *testing.T) {
	y := newHeldYard(t)
	first := y.pull(t)
	y.awaitHeld(t, first)
	second := y.pull(t)
	fuse := heldFuse()
	select {
	case <-first.over:
	case <-time.After(fuse):
		t.Fatalf("the first pull was not over within %s of the second's coming", fuse)
	}
	// The second's hold ends the first and takes its place in the same step, so
	// the second is registered once the first is over and the count needs no
	// wait. A second that is over already ended itself, and its answer says why;
	// the count would only say that it is gone.
	select {
	case <-second.over:
		t.Fatalf("the second pull was over with the first: %d %s", second.rec.Code, second.rec.Body)
	default:
	}
	if n := y.held(); n != 1 {
		t.Fatalf("the receiver holds %d pulls after the peer's second ended its first, want the second alone", n)
	}
	second.kill()
	select {
	case <-second.over:
	case <-time.After(fuse):
		t.Fatalf("the second pull was not over within %s of its asker going away", fuse)
	}
	if n := y.held(); n != 0 {
		t.Fatalf("the receiver holds %d pulls after the second's asker went away, want none", n)
	}
}

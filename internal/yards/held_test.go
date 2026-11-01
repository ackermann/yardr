package yards

import (
	"context"
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

// A pull held for a peer is forgotten when its asker goes away. Left in the
// receiver's map it would count against maxHeld until the peer asked again,
// and a peer that never does would keep the slot for good. No answer shows
// it: the peer's next pull replaces its own entry whether or not the old one
// was released, so the dispatch tests, which see only answers, cannot tell.
// The map is read here, in the package that owns it, after the handler is
// served the request straight with the context a closed connection cancels.
func TestHeldPullForgottenWhenItsAskerGoesAway(t *testing.T) {
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
	rc := NewReceiver(own, keyFile, nil)

	// The longest wait a pull may ask: it is far from over when its asker goes.
	uri := Path + "?after=0&wait=60"
	ts, sig := yardkey.SignRequest(b, store.Public(a), http.MethodGet, uri, time.Now(), nil)
	asker, kill := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, uri, nil).WithContext(asker)
	req.Header.Set(yardkey.HeaderKey, store.Public(b))
	req.Header.Set(yardkey.HeaderTime, ts)
	req.Header.Set(yardkey.HeaderSig, sig)
	rec := httptest.NewRecorder()
	over := make(chan struct{})
	go func() { defer close(over); rc.Handler().ServeHTTP(rec, req) }()
	t.Cleanup(func() { kill(); <-over })

	held := func() int {
		rc.mu.Lock()
		defer rc.mu.Unlock()
		return len(rc.held)
	}
	// The waits are fuses against a hang, not windows the effect has to fit in.
	fuse := testload.Scale(2 * time.Minute)
	for end := time.Now().Add(fuse); held() != 1; time.Sleep(time.Millisecond) {
		select {
		case <-over:
			t.Fatalf("the pull was over before it was held: %d %s", rec.Code, rec.Body)
		default:
		}
		if time.Now().After(end) {
			t.Fatalf("the pull was not held within %s", fuse)
		}
	}
	kill()
	select {
	case <-over:
	case <-time.After(fuse):
		t.Fatalf("the pull was not over within %s of its asker going away", fuse)
	}
	// ServeHTTP returns after the handler's deferred calls, so a release that
	// is going to happen has happened by now, and the count needs no wait.
	if n := held(); n != 0 {
		t.Fatalf("the receiver holds %d pulls after the only one's asker went away, want none", n)
	}
}

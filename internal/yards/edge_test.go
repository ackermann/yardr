package yards_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/store"
)

// A peer whose URL is an edge URL is called under its base: the requests go
// to <base>/peers/messages and <base>/peers/pair/..., and the signatures
// still hold, since the edge strips the base before the yard sees them. Every
// request asks that its connection not be kept.
func TestCallerUnderBasePath(t *testing.T) {
	cheapKeys(t)
	a, b := newYard(t), newYard(t)
	ctx := context.Background()
	const prefix = "/peers/acct/b"
	var mu sync.Mutex
	var outer []string
	var kept []string
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		outer = append(outer, r.Method+" "+r.URL.RequestURI())
		if !r.Close {
			kept = append(kept, r.Method+" "+r.URL.RequestURI())
		}
		mu.Unlock()
		rest, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = rest
		r2.RequestURI = rest
		if r.URL.RawQuery != "" {
			r2.RequestURI += "?" + r.URL.RawQuery
		}
		b.rc.Handler().ServeHTTP(w, r2)
	}))
	t.Cleanup(edge.Close)
	base := edge.URL + prefix

	a.add(t, "b", b, true, ping, ping)
	toB, err := a.api.SetPeerURL(ctx, "b", base, "jo")
	if err != nil {
		t.Fatal(err)
	}
	b.add(t, "a", a, false, ping, ping)

	m := store.PeerMessage{ID: "m1", Kind: "ping", Body: json.RawMessage(`{}`)}
	if err := a.caller().Push(ctx, *toB, m); err != nil {
		t.Fatalf("Push under a base path = %v", err)
	}
	sent, err := b.api.PeerSend(ctx, "a", "ping", nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := a.caller().Pull(ctx, *toB, 0, 0)
	if err != nil {
		t.Fatalf("Pull under a base path = %v", err)
	}
	// Two: the pong the push was answered with, then the ping.
	if res, err := a.api.PeerPulled(ctx, "b", batch); err != nil || len(res.Taken) != 2 || res.Taken[1].ID != sent.ID {
		t.Fatalf("PeerPulled = %+v, %v", res, err)
	}

	opened, err := b.own.PairOpen(ctx, store.PairOffer{Name: "b", Key: b.pub(), URL: base}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	code, err := pairing.CleanCode(opened.Code)
	if err != nil {
		t.Fatal(err)
	}
	// A third yard pairs: a is b's peer already, and a peer cannot pair again.
	c := newYard(t)
	called, err := c.caller().PairCall(ctx, base, code, "c", nil)
	if err != nil {
		t.Fatalf("PairCall under a base path = %v", err)
	}
	if called.Host.URL != base || called.URL != base {
		t.Fatalf("the pairing is at host %q, link %q, want the base %q", called.Host.URL, called.URL, base)
	}
	out, err := c.caller().PairAnswer(ctx, called, true)
	if err != nil || out.State != pairing.Waiting {
		t.Fatalf("the confirm under a base path = %+v, %v", out, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(kept) != 0 {
		t.Errorf("requests that keep their connection: %q", kept)
	}
	for _, want := range []string{
		"POST " + prefix + "/peers/messages",
		"POST " + prefix + "/peers/pair/hello",
		"POST " + prefix + "/peers/pair/reveal",
		"POST " + prefix + "/peers/pair/confirm",
	} {
		if !slices.Contains(outer, want) {
			t.Errorf("no request to %s among %q", want, outer)
		}
	}
	var pull string
	for _, p := range outer {
		if rest, ok := strings.CutPrefix(p, "GET "+prefix+"/peers/messages?"); ok {
			pull = rest
		}
	}
	if pull == "" || !strings.Contains(pull, "after=0") {
		t.Errorf("no pull under the base among %q", outer)
	}
}

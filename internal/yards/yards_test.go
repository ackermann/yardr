package yards_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

// yard is a yard in this process: its store, its key and its receiver on a
// listener of its own.
type yard struct {
	api     store.API
	own     *store.Store
	key     ed25519.PrivateKey
	keyFile string
	rc      *yards.Receiver
	srv     *httptest.Server
	now     time.Time
}

func newYard(t *testing.T) *yard {
	t.Helper()
	api, own := storetest.NewOwned(t)
	y := &yard{api: api, own: own, keyFile: filepath.Join(t.TempDir(), "peer.key"), now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	var err error
	if y.key, _, err = yardkey.Ensure(y.keyFile); err != nil {
		t.Fatal(err)
	}
	y.rc = yards.NewReceiver(own, y.keyFile, nil)
	y.rc.SetClock(func() time.Time { return y.now })
	y.srv = httptest.NewServer(y.rc.Handler())
	t.Cleanup(y.srv.Close)
	return y
}

func (y *yard) pub() string { return yardkey.Public(y.key) }

func (y *yard) caller() *yards.Caller {
	return &yards.Caller{Key: y.key, Now: func() time.Time { return y.now }}
}

// add records other as a peer of y under name, with or without its URL, and
// allows the kinds each way.
func (y *yard) add(t *testing.T, name string, other *yard, url bool, send, receive []string) store.Peer {
	t.Helper()
	p := store.Peer{Name: name, Key: other.pub()}
	if url {
		p.URL = other.srv.URL
	}
	ctx := context.Background()
	if _, err := y.api.AddPeer(ctx, p, "jo"); err != nil {
		t.Fatal(err)
	}
	got, err := y.api.AllowPeer(ctx, name, store.PeerAllow{Send: send, Receive: receive}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return *got
}

func (y *yard) events(t *testing.T, kind string) []store.Event {
	t.Helper()
	evs, err := y.api.Events(context.Background(), "", 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

var ping = []string{"ping"}

// request is a request to y as from signs it, which a test may then change.
type request struct {
	method, path string
	key, ts, sig string
	body         []byte
}

func signed(from, to *yard, method, path string, body []byte) *request {
	ts, sig := yardkey.SignRequest(from.key, to.pub(), method, path, to.now, body)
	return &request{method: method, path: path, key: from.pub(), ts: ts, sig: sig, body: body}
}

func (r *request) send(t *testing.T, to *yard) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(r.method, to.srv.URL+r.path, bytes.NewReader(r.body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(yardkey.HeaderKey, r.key)
	req.Header.Set(yardkey.HeaderTime, r.ts)
	req.Header.Set(yardkey.HeaderSig, r.sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func message(id string) []byte {
	b, _ := json.Marshal(store.PeerMessage{ID: id, Kind: "ping", Body: json.RawMessage(`{"secret":"hunter2"}`)})
	return b
}

// Every request that is not a peer's is answered 401, in the same words
// whatever was wrong with it, unsigned, and nothing of it is kept: a key no
// link has, a signature by another key, a changed body, an old timestamp, a
// request signed for another yard.
func TestReceiverRefusesWhatIsNoPeers(t *testing.T) {
	a, b, c := newYard(t), newYard(t), newYard(t)
	b.add(t, "a", a, true, ping, ping)

	stranger := signed(c, b, "POST", yards.Path, message("m1")) // a third yard with its own key, not added
	wrongKey := signed(c, b, "POST", yards.Path, message("m1"))
	wrongKey.key = a.pub() // says it is a, signed by c
	changed := signed(a, b, "POST", yards.Path, message("m1"))
	changed.body = message("m2")
	old := signed(a, b, "POST", yards.Path, message("m1"))
	old.ts, old.sig = yardkey.SignRequest(a.key, b.pub(), "POST", yards.Path, b.now.Add(-yardkey.MaxSkew-time.Second), old.body)
	ahead := signed(a, b, "POST", yards.Path, message("m1"))
	ahead.ts, ahead.sig = yardkey.SignRequest(a.key, b.pub(), "POST", yards.Path, b.now.Add(yardkey.MaxSkew+time.Second), ahead.body)
	redated := signed(a, b, "POST", yards.Path, message("m1"))
	redated.ts = "1"
	forOther := signed(a, c, "POST", yards.Path, message("m1")) // a's request to c, played to b
	otherPath := signed(a, b, "GET", yards.Path+"?after=0", nil)
	otherPath.path = yards.Path + "?after=5"
	unsigned := &request{method: "POST", path: yards.Path, body: message("m1")}

	var first string
	for name, r := range map[string]*request{
		"a yard that was not added": stranger, "a wrong key": wrongKey, "a changed body": changed, "an old timestamp": old,
		"a timestamp ahead": ahead, "a changed timestamp": redated, "a request for another yard": forOther,
		"a changed query": otherPath, "no signature": unsigned,
	} {
		resp, body := r.send(t, b)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
		if resp.Header.Get(yardkey.HeaderSig) != "" {
			t.Errorf("%s: the refusal is signed", name)
		}
		if first == "" {
			first = string(body)
		}
		if string(body) != first {
			t.Errorf("%s: answered %q, others %q: the answer tells them apart", name, body, first)
		}
	}
	got := b.rc.Rejected()
	if got["unknown_peer"] != 2 || got["unauthorized"] != 7 {
		t.Fatalf("rejected = %v", got)
	}
	for _, kind := range []string{"peer_message_received", "peer_message_refused"} {
		if evs := b.events(t, kind); len(evs) != 0 {
			t.Fatalf("%s after refusals only: %+v", kind, evs)
		}
	}
	if p, _ := b.api.GetPeer(context.Background(), "a"); p.Waiting != 0 {
		t.Fatalf("a refused ping was answered: %+v", p)
	}

	// The same requests, as a signed them, are taken.
	if resp, _ := signed(a, b, "POST", yards.Path, message("m1")).send(t, b); resp.StatusCode != http.StatusOK {
		t.Fatalf("a's own request: %d", resp.StatusCode)
	}
	// Removed, a is no peer any more.
	if err := b.api.DeletePeer(context.Background(), "a", "jo"); err != nil {
		t.Fatal(err)
	}
	if resp, body := signed(a, b, "POST", yards.Path, message("m5")).send(t, b); resp.StatusCode != http.StatusUnauthorized || string(body) != first {
		t.Fatalf("a removed peer's request: %d %q", resp.StatusCode, body)
	}
}

// A posted message is taken once: the request played again, and the same
// id in a new request, are answered 200 and dropped. A kind the link does
// not carry in is answered 403 and logged. Every answer to a peer is signed.
func TestReceiverPost(t *testing.T) {
	a, b := newYard(t), newYard(t)
	b.add(t, "a", a, true, nil, nil)
	answer := func(r *request, want int) map[string]any {
		t.Helper()
		resp, body := r.send(t, b)
		if resp.StatusCode != want {
			t.Fatalf("status %d (%s), want %d", resp.StatusCode, body, want)
		}
		if err := yardkey.VerifyResponse(b.pub(), resp.StatusCode, r.sig, resp.Header.Get(yardkey.HeaderSig), body); err != nil {
			t.Fatalf("the %d answer is not b's: %v", resp.StatusCode, err)
		}
		var v map[string]any
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := signed(a, b, "POST", yards.Path, message("m1"))
	if got := answer(first, http.StatusForbidden); got["refused"] != "kind_not_allowed" {
		t.Fatalf("a ping with nothing allowed = %v", got)
	}
	refused := b.events(t, "peer_message_refused")
	if len(refused) != 1 || refused[0].Data["peer"] != "a" || refused[0].Data["id"] != "m1" || refused[0].Data["direction"] != "in" {
		t.Fatalf("peer_message_refused = %+v", refused)
	}
	if b.rc.Rejected()["refused"] != 1 {
		t.Fatalf("rejected = %v", b.rc.Rejected())
	}

	if _, err := b.api.AllowPeer(context.Background(), "a", store.PeerAllow{Send: ping, Receive: ping}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got := answer(first, http.StatusOK); got["taken"] != true {
		t.Fatalf("the ping once allowed = %v", got)
	}
	// The very request again, as someone who recorded it would send it.
	if got := answer(first, http.StatusOK); got["duplicate"] != true {
		t.Fatalf("the request played again = %v", got)
	}
	// The same id in a request of its own: a sender that tries again.
	b.now = b.now.Add(time.Minute)
	if got := answer(signed(a, b, "POST", yards.Path, message("m1")), http.StatusOK); got["duplicate"] != true {
		t.Fatalf("the same id again = %v", got)
	}
	if evs := b.events(t, "peer_message_received"); len(evs) != 1 || evs[0].Data["id"] != "m1" || evs[0].Data["way"] != "push" {
		t.Fatalf("peer_message_received = %+v", evs)
	}
	if p, _ := b.api.GetPeer(context.Background(), "a"); p.Waiting != 1 {
		t.Fatalf("pongs waiting = %+v, want the one", p)
	}
	if b.rc.Rejected()["duplicate"] != 2 {
		t.Fatalf("rejected = %v", b.rc.Rejected())
	}
	// Recorded and played after the five minutes: refused like any other.
	b.now = b.now.Add(yardkey.MaxSkew)
	if resp, _ := first.send(t, b); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the request played late: %d", resp.StatusCode)
	}

	for name, body := range map[string][]byte{
		"not JSON":         []byte("ping"),
		"a path for an id": []byte(`{"id":"../x","kind":"ping","body":{}}`),
		"no body":          []byte(`{"id":"m7","kind":"ping"}`),
	} {
		if got := answer(signed(a, b, "POST", yards.Path, body), http.StatusBadRequest); got["error"] == nil {
			t.Errorf("%s = %v", name, got)
		}
	}
	// A body at the limit is taken; a request past it is not read.
	atLimit, _ := json.Marshal(store.PeerMessage{ID: "big", Kind: "ping", Body: json.RawMessage(`"` + strings.Repeat("x", store.MaxPeerBody-2) + `"`)})
	if got := answer(signed(a, b, "POST", yards.Path, atLimit), http.StatusOK); got["taken"] != true {
		t.Fatalf("a body of %d bytes = %v", store.MaxPeerBody, got)
	}
	over, _ := json.Marshal(store.PeerMessage{ID: "bigger", Kind: "ping", Body: json.RawMessage(`"` + strings.Repeat("x", store.MaxPeerBody+8<<10) + `"`)})
	if resp, _ := signed(a, b, "POST", yards.Path, over).send(t, b); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a request past the limit: %d", resp.StatusCode)
	}
	oneOver, _ := json.Marshal(store.PeerMessage{ID: "over", Kind: "ping", Body: json.RawMessage(`"` + strings.Repeat("x", store.MaxPeerBody-1) + `"`)})
	answer(signed(a, b, "POST", yards.Path, oneOver), http.StatusBadRequest)

	for _, e := range append(b.events(t, "peer_message_received"), b.events(t, "peer_message_refused")...) {
		if raw, _ := json.Marshal(e); strings.Contains(string(raw), "hunter2") {
			t.Fatalf("an event carries the body: %s", raw)
		}
	}
	// Nothing but the two requests is served.
	for _, r := range []*request{signed(a, b, "PUT", yards.Path, nil), signed(a, b, "GET", "/peers/a", nil), signed(a, b, "GET", "/peers/", nil)} {
		if resp, _ := r.send(t, b); resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d", r.method, r.path, resp.StatusCode)
		}
	}
}

// A yard with no key of its own, or one whose key file is open to others,
// takes nothing: it could sign no answer.
func TestReceiverWithoutAKey(t *testing.T) {
	a, b := newYard(t), newYard(t)
	b.add(t, "a", a, true, ping, ping)
	if err := os.Chmod(b.keyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if resp, _ := signed(a, b, "POST", yards.Path, message("m1")).send(t, b); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("with a key file open to others: %d", resp.StatusCode)
	}
	if err := os.Remove(b.keyFile); err != nil {
		t.Fatal(err)
	}
	if resp, _ := signed(a, b, "POST", yards.Path, message("m1")).send(t, b); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("with no key: %d", resp.StatusCode)
	}
	if b.rc.Rejected()["no_key"] != 2 || len(b.events(t, "peer_message_received")) != 0 {
		t.Fatalf("rejected = %v", b.rc.Rejected())
	}
}

// The Caller posts a message the peer takes once, is told when the peer
// refuses it, and trusts no answer the peer did not sign.
func TestCallerPush(t *testing.T) {
	a, b := newYard(t), newYard(t)
	ctx := context.Background()
	toB := a.add(t, "b", b, true, ping, ping)
	b.add(t, "a", a, true, nil, nil)
	m := store.PeerMessage{ID: "m1", Kind: "ping", Body: json.RawMessage(`{}`), Seq: 12}
	err := a.caller().Push(ctx, toB, m)
	if !errors.Is(err, yards.ErrRefused) || !strings.Contains(err.Error(), "does not carry ping in") {
		t.Fatalf("Push of a kind b does not take = %v", err)
	}
	if _, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{Receive: ping}, "jo"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // the second is the duplicate: b has it either way
		if err := a.caller().Push(ctx, toB, m); err != nil {
			t.Fatalf("Push %d = %v", i, err)
		}
	}
	if evs := b.events(t, "peer_message_received"); len(evs) != 1 {
		t.Fatalf("b received %+v", evs)
	}

	// b does not know a: its 401 is nobody's signed word, so worth a retry.
	stranger := newYard(t)
	toB.Key = b.pub()
	if err := stranger.caller().Push(ctx, toB, m); err == nil || errors.Is(err, yards.ErrRefused) || !strings.Contains(err.Error(), "answered 401, not signed with its key") {
		t.Fatalf("Push by a yard b has not added = %v", err)
	}
	// Something between the two that answers 200, or 403, for b.
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(yardkey.HeaderSig, yardkey.SignResponse(stranger.key, status, r.Header.Get(yardkey.HeaderSig), []byte(`{"taken":true}`)))
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"taken":true}`))
		}))
		err := a.caller().Push(ctx, store.Peer{Name: "b", Key: b.pub(), URL: fake.URL}, m)
		fake.Close()
		if err == nil || errors.Is(err, yards.ErrRefused) || !strings.Contains(err.Error(), "not signed with its key") {
			t.Fatalf("Push answered %d by someone else = %v", status, err)
		}
	}
	if err := a.caller().Push(ctx, store.Peer{Name: "b", Key: b.pub()}, m); err == nil || !strings.Contains(err.Error(), "has no URL") {
		t.Fatalf("Push to a peer without a URL = %v", err)
	}
	b.srv.Close()
	if err := a.caller().Push(ctx, toB, m); err == nil || errors.Is(err, yards.ErrRefused) {
		t.Fatalf("Push to a yard that is down = %v", err)
	}
}

// A yard that cannot be called asks: it gets what waits for it, the store
// takes it on the peer's signature, and its next question ends what it has
// as sent.
func TestCallerPull(t *testing.T) {
	a, b := newYard(t), newYard(t) // b cannot be called: a has no URL for it
	ctx := context.Background()
	a.add(t, "b", b, false, ping, ping)
	toA := b.add(t, "a", a, true, ping, ping)
	var ids []string
	for i := 0; i < 2; i++ {
		m, err := a.api.PeerSend(ctx, "b", "ping", nil, "jo")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	batch, err := b.caller().Pull(ctx, toA, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.api.PeerPulled(ctx, "a", batch)
	if err != nil || len(res.Taken) != 2 || res.Taken[0].ID != ids[0] || res.Push {
		t.Fatalf("PeerPulled = %+v, %v", res, err)
	}
	if evs := b.events(t, "peer_message_received"); len(evs) != 2 || evs[0].Data["way"] != "pull" || evs[0].Data["peer"] != "a" {
		t.Fatalf("b received %+v", evs)
	}
	if len(a.events(t, "peer_message_sent")) != 0 {
		t.Fatal("a logged its messages as sent before b said it has them")
	}
	p, _ := b.api.GetPeer(ctx, "a")
	if p.Pulled == 0 || p.Waiting != 2 {
		t.Fatalf("b's place and pongs = %+v", p)
	}
	batch, err = b.caller().Pull(ctx, toA, p.Pulled)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := b.api.PeerPulled(ctx, "a", batch); err != nil || len(res.Taken) != 0 {
		t.Fatalf("the second pull = %+v, %v", res, err)
	}
	if sent := a.events(t, "peer_message_sent"); len(sent) != 2 || sent[0].Data["way"] != "pull" || sent[0].Data["id"] != ids[0] {
		t.Fatalf("a's peer_message_sent = %+v", sent)
	}
	if p, _ := a.api.GetPeer(ctx, "b"); p.Waiting != 0 {
		t.Fatalf("a's outbox = %+v", p)
	}

	// A page from someone who is not a is not b's to take.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"messages":[{"id":"forged","kind":"ping","body":{},"seq":99}]}`))
	}))
	defer fake.Close()
	if _, err := b.caller().Pull(ctx, store.Peer{Name: "a", Key: a.pub(), URL: fake.URL}, 0); err == nil || !strings.Contains(err.Error(), "not signed with its key") {
		t.Fatalf("Pull answered by someone else = %v", err)
	}
	// A yard that is called is told so, and handed nothing by asking.
	c := newYard(t)
	a.add(t, "c", c, true, ping, ping)
	toAfromC := c.add(t, "a", a, true, ping, ping)
	if _, err := a.api.PeerSend(ctx, "c", "ping", nil, "jo"); err != nil {
		t.Fatal(err)
	}
	batch, err = c.caller().Pull(ctx, toAfromC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := c.api.PeerPulled(ctx, "a", batch); err != nil || !res.Push || len(res.Taken) != 0 {
		t.Fatalf("a pull by a yard that is posted to = %+v, %v", res, err)
	}
	// GET wants a number.
	if resp, _ := signed(b, a, "GET", yards.Path+"?after=x", nil).send(t, a); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("after=x: %d", resp.StatusCode)
	}
}

// Mail the peer refuses is refused with its reason, which the Caller says
// in this yard's words: a posted one by the 403 it is answered with, one
// asked for by the next question.
func TestMailRefusedWithAReason(t *testing.T) {
	a, b := newYard(t), newYard(t)
	ctx := context.Background()
	mail := []string{"mail"}
	toB := a.add(t, "b", b, true, mail, mail)
	b.add(t, "a", a, false, mail, mail)
	if _, err := b.api.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	post := func(id, to, text string) error {
		body, _ := json.Marshal(map[string]string{"to": to, "from": "mayor", "text": text})
		return a.caller().Push(ctx, toB, store.PeerMessage{ID: id, Kind: "mail", Body: body})
	}
	if err := post("m1", "mayor", "hi"); err != nil {
		t.Fatalf("mail for b's mayor = %v", err)
	}
	for want, m := range map[string][2]string{
		"it has no such resident":          {"clerk", "hi"},
		"the text is larger than it takes": {"mayor", strings.Repeat("x", store.MaxPeerMailBytes+1)},
		"it does not read the mail":        {"mayor", ""},
	} {
		if err := post("r-"+m[0]+strings.Repeat("x", len(m[1])%7), m[0], m[1]); !errors.Is(err, yards.ErrRefused) || !strings.HasSuffix(err.Error(), want) || strings.Contains(err.Error(), "mayor") {
			t.Errorf("mail to %s of %d bytes = %v, want %q", m[0], len(m[1]), err, want)
		}
	}
	if got := b.rc.Rejected()["refused"]; got != 3 {
		t.Fatalf("b refused %d", got)
	}

	// b's mail for a waits to be asked for. a takes the first and refuses
	// the second, and says so with its next question.
	if _, err := a.api.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"mayor", "clerk"} {
		if _, err := b.api.MailPeer(ctx, to, "a", "hello", "jo"); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := a.caller().Pull(ctx, toB, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := a.api.PeerPulled(ctx, "b", batch); err != nil || len(res.Taken) != 2 || res.Taken[1].Refused != store.RefusedNoResident {
		t.Fatalf("PeerPulled = %+v, %v", res, err)
	}
	p, _ := a.api.GetPeer(ctx, "b")
	if batch, err = a.caller().Pull(ctx, *p, p.Pulled); err != nil || len(batch.Told) != 1 {
		t.Fatalf("the question that tells = %+v, %v", batch, err)
	}
	if _, err := a.api.PeerPulled(ctx, "b", batch); err != nil {
		t.Fatal(err)
	}
	if p, _ := a.api.GetPeer(ctx, "b"); len(p.Refused) != 0 {
		t.Fatalf("a tells it again: %+v", p)
	}
	failed := b.events(t, "peer_message_failed")
	if len(b.events(t, "peer_message_sent")) != 1 || len(failed) != 1 || failed[0].Data["reason"] != "refused by the peer: it has no such resident" {
		t.Fatalf("b's events: sent %+v, failed %+v", b.events(t, "peer_message_sent"), failed)
	}
	// What is no refusal in a question is passed by, and the question answered.
	if resp, _ := signed(a, b, "GET", yards.Path+"?after=0&refused=x,7,:,9:"+strings.Repeat("y", 40), nil).send(t, b); resp.StatusCode != http.StatusOK {
		t.Fatalf("a question with refusals that are none: %d", resp.StatusCode)
	}
}

// between is the yard's store with something done to it after a request's
// peer is found by its key and before the request is taken: the two are
// separate transactions, and the operator may act in between.
type between struct {
	*store.Store
	do func()
}

func (s *between) PeerByKey(ctx context.Context, key string) (*store.Peer, error) {
	p, err := s.Store.PeerByKey(ctx, key)
	if do := s.do; do != nil {
		s.do = nil
		do()
	}
	return p, err
}

// A peer renamed, and its name given to another yard, while its request is
// under way: the request is answered as one of no peer, unsigned, and
// nothing is taken or ended under the name, which is the other yard's. The
// sender keeps its message and its next try is taken under the new name.
func TestReceiverPeerRenamedAndNameGivenAway(t *testing.T) {
	ctx := context.Background()
	// b's peer a becomes b2 and c becomes a, once, inside the next request.
	yards3 := func(t *testing.T) (a, b, c *yard, toB store.Peer, st *between) {
		t.Helper()
		a, b, c = newYard(t), newYard(t), newYard(t)
		toB = a.add(t, "b", b, true, ping, ping)
		b.add(t, "a", a, false, ping, ping)
		st = &between{Store: b.own}
		b.srv.Close()
		b.rc = yards.NewReceiver(st, b.keyFile, nil)
		b.rc.SetClock(func() time.Time { return b.now })
		b.srv = httptest.NewServer(b.rc.Handler())
		t.Cleanup(b.srv.Close)
		toB.URL = b.srv.URL
		st.do = func() {
			if _, err := b.api.RenamePeer(ctx, "a", "b2", "jo"); err != nil {
				t.Error(err)
			}
			b.add(t, "a", c, false, ping, ping)
		}
		return a, b, c, toB, st
	}
	unknown := func(t *testing.T, b *yard, err error) {
		t.Helper()
		if err == nil || errors.Is(err, yards.ErrRefused) || !strings.Contains(err.Error(), "answered 401, not signed with its key") {
			t.Fatalf("the request under way = %v", err)
		}
		if got := b.rc.Rejected(); got["unknown_peer"] != 1 || len(got) != 1 {
			t.Fatalf("rejected = %v", got)
		}
	}

	t.Run("post", func(t *testing.T) {
		a, b, c, toB, _ := yards3(t)
		m := store.PeerMessage{ID: "m1", Kind: "ping", Body: json.RawMessage(`{}`)}
		unknown(t, b, a.caller().Push(ctx, toB, m))
		if evs := append(b.events(t, "peer_message_received"), b.events(t, "peer_message_refused")...); len(evs) != 0 {
			t.Fatalf("b took or refused %+v", evs)
		}
		for _, name := range []string{"a", "b2"} { // no pong waits for either
			if p, err := b.api.GetPeer(ctx, name); err != nil || p.Waiting != 0 {
				t.Fatalf("peer %s = %+v, %v", name, p, err)
			}
		}
		// The sender was told nothing it can end the message on, tries
		// again, and is taken as b2.
		if err := a.caller().Push(ctx, toB, m); err != nil {
			t.Fatalf("the next try = %v", err)
		}
		// The id is not one c has sent: its own m1 is taken, not dropped.
		resp, body := signed(c, b, "POST", yards.Path, message("m1")).send(t, b)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"taken":true`) {
			t.Fatalf("m1 from the yard that has the name = %d %s", resp.StatusCode, body)
		}
		evs := b.events(t, "peer_message_received")
		if len(evs) != 2 || evs[0].Data["peer"] != "b2" || evs[1].Data["peer"] != "a" {
			t.Fatalf("peer_message_received = %+v", evs)
		}
	})

	t.Run("get", func(t *testing.T) {
		a, b, _, toB, st := yards3(t)
		forA, err := b.api.PeerSend(ctx, "a", "ping", nil, "jo")
		if err != nil {
			t.Fatal(err)
		}
		var forC *store.PeerOut
		swap := st.do
		st.do = func() {
			swap()
			if forC, err = b.api.PeerSend(ctx, "a", "ping", nil, "jo"); err != nil {
				t.Error(err)
			}
		}
		// a says it has everything: under the name a that would end c's.
		_, err = a.caller().Pull(ctx, toB, 1<<32)
		unknown(t, b, err)
		if evs := b.events(t, "peer_message_sent"); len(evs) != 0 {
			t.Fatalf("peer_message_sent = %+v", evs)
		}
		if out, err := b.api.PeerOutbox(ctx, "a", 0); err != nil || len(out) != 1 || out[0].ID != forC.ID {
			t.Fatalf("what waits for the yard that has the name = %+v, %v", out, err)
		}
		// Asked again, a gets its own.
		batch, err := a.caller().Pull(ctx, toB, 0)
		if err != nil {
			t.Fatalf("the next question = %v", err)
		}
		var page store.PeerPage
		if err := json.Unmarshal(batch.Body, &page); err != nil || len(page.Messages) != 1 || page.Messages[0].ID != forA.ID {
			t.Fatalf("the next question's page = %s, %v", batch.Body, err)
		}
	})
}

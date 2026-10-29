package edgelink

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	scripttest.Remove()
	os.Exit(code)
}

// A yard whose hooks come down the link: a receiver over a throwaway store,
// serving an acme hook shaped like Linear's (an hmac over the body, a
// delivery header, a body timestamp in ms with a 1m window, bodies unique)
// and a token hook, with the link's handler being the receiver's.
type queuedYard struct {
	*linked
	st     store.API
	rc     *hooks.Receiver
	now    time.Time
	served chan struct{}
}

// handlerBarrier holds the first handler after it has finished, which has
// already recorded the delivery, so the test can close the link before its
// ack is sent.
type handlerBarrier struct {
	once        sync.Once // the first handler to get here is the one held
	entered     chan struct{}
	released    chan struct{}
	releaseOnce sync.Once // release closes released once, however many ask
}

// release lets the held handler go. closeLinkBeforeAck does, and so does the
// test's cleanup (queuedLinkWithBarrier): a test that fails before it gets
// there would otherwise leave the handler held for ever, and the link's stop
// waits for the handlers it serves. Whichever comes second finds it done.
func (b *handlerBarrier) release() {
	b.releaseOnce.Do(func() { close(b.released) })
}

const queuedNormalize = `#!/bin/sh
cat > "$1"
printf '%s' '{"type":"ping","action":"create","text":"hi","actor":{"id":"u1","name":"Ann"},"fields":{"team":["ENG"],"queue":["triage"]}}'
`

func queuedDecl(stdin string) string {
	return fmt.Sprintf(`name: acme
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Acme-Signature, encoding: hex, prefix: "sha256=" }
    delivery: { header: Acme-Delivery }
    timestamp: { body: sent, unit: ms, window: 1m }
  bodies: unique
  normalize: [normalize, %s]
  keys: [team, queue]
`, stdin)
}

const (
	queuedSecret = "acme_wh_test_secret"
	cronSecret   = "cron_test_token"
)

func queuedLink(t *testing.T) *queuedYard {
	return queuedLinkWithBarrier(t, nil)
}

func queuedLinkWithBarrier(t *testing.T, barrier *handlerBarrier) *queuedYard {
	t.Helper()
	home := t.TempDir()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	stdin := filepath.Join(home, "stdin.json")
	text := queuedDecl(stdin)
	d, err := source.Parse("acme.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	declDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(declDir, "normalize"), []byte(queuedNormalize), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := st.PutSource(ctx, "acme.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := func(name, secret string) string {
		t.Helper()
		f := filepath.Join(home, name+".secret")
		if err := os.WriteFile(f, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if _, err := st.PutHook(ctx, store.Hook{Name: "acme", Source: "acme", SecretFile: secretFile("acme", queuedSecret)}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutHook(ctx, store.Hook{Name: "cron", Source: "token", SecretFile: secretFile("cron", cronSecret)}, "me"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rc := hooks.NewReceiver(own, home, nil)
	served := make(chan struct{}, 8)
	handler := rc.Handler()
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		served <- struct{}{}
		if barrier != nil {
			barrier.once.Do(func() {
				close(barrier.entered)
				<-barrier.released
			})
		}
	})
	y := linkTo(t, func(l *Link) { l.Handler = wrapped }, nil)
	if barrier != nil {
		// Registered after linkTo's stop, so it runs before it: stop waits
		// for the handlers the link serves, and the held one would keep it
		// waiting, to go test's timeout, for a test that failed before
		// closeLinkBeforeAck.
		t.Cleanup(barrier.release)
	}
	return &queuedYard{linked: y, st: st, rc: rc, now: now, served: served}
}

func queuedSign(body []byte) string {
	m := hmac.New(sha256.New, []byte(queuedSecret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func queuedBody(sent time.Time) []byte {
	b, _ := json.Marshal(map[string]any{"sent": sent.UnixMilli(), "note": "hello"})
	return b
}

// queued is the delivery as the edge sends one it kept: with queued set and
// the edge-signed time it received it, signed over both.
func queued(t *testing.T, l *standin.EdgeLink, path string, h http.Header, body []byte, received time.Time) standin.EdgeFrame {
	t.Helper()
	f := l.Frame(http.MethodPost, path, h, body, true)
	f.Received = received.UTC().Format(time.RFC3339)
	f.Sig = l.Sign(nil, f)
	return f
}

// acked is the next id the yard acks on l: the edge deletes a queued
// delivery on its ack.
func acked(t *testing.T, l *standin.EdgeLink) string {
	t.Helper()
	select {
	case id := <-l.AckEvents():
		return id
	case <-time.After(10 * time.Second):
		t.Fatal("the yard never acked the queued delivery")
		return ""
	}
}

func (y *queuedYard) hookEvents(t *testing.T) []store.Event {
	t.Helper()
	evs, err := y.st.Events(context.Background(), "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if e.Kind == "hook" {
			out = append(out, e)
		}
	}
	return out
}

// recorded waits for a completed hook handler, whose store write is already
// durable, then checks the number of deliveries recorded so far.
func (y *queuedYard) recorded(t *testing.T, n int) {
	t.Helper()
	select {
	case <-y.served:
	case <-time.After(10 * time.Second):
		t.Fatal("the hook handler did not finish")
	}
	if evs := y.hookEvents(t); len(evs) != n {
		t.Fatalf("want %d recorded deliveries, have %d: %+v", n, len(evs), evs)
	}
}

func closeLinkBeforeAck(t *testing.T, l *standin.EdgeLink, barrier *handlerBarrier) {
	t.Helper()
	defer barrier.release()
	// The handler has recorded the delivery and is held before answer can
	// send its ack. Closing the edge side cancels the request context first.
	select {
	case <-barrier.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler did not reach the ack barrier")
	}
	l.Close()
	select {
	case <-l.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the edge did not observe the closed link")
	}
	if got := l.Acks(); len(got) != 0 {
		t.Fatalf("the first ack was not lost: %v", got)
	}
}

func (y *queuedYard) acmeHeaders(body []byte, delivery string) http.Header {
	h := http.Header{}
	h.Set("Acme-Signature", "sha256="+queuedSign(body))
	h.Set("Acme-Delivery", delivery)
	h.Set("Content-Type", "application/json")
	return h
}

// A delivery shaped like Linear's that the edge kept for ten minutes
// verifies through the link: the yard checks its timestamp against the
// edge-signed received instead of its own clock, records it and acks it,
// with no response for the sender that is long gone. The same body sent
// straight to the listener ten minutes late is refused.
func TestLinkChecksAQueuedDeliveryAgainstTheEdgesTime(t *testing.T) {
	t.Parallel()
	y := queuedLink(t)
	l := y.next(t)
	sent := y.now.Add(-10 * time.Minute)
	body := queuedBody(sent)
	f := queued(t, l, "/hooks/acme", y.acmeHeaders(body, "q-1"), body, sent)
	send(t, l, f)
	if id := acked(t, l); id != f.ID {
		t.Fatalf("acked %q, want %q", id, f.ID)
	}
	for _, a := range l.Answers() {
		if a.ID == f.ID {
			t.Fatalf("a queued delivery was answered: %+v", a)
		}
	}
	if evs := y.hookEvents(t); len(evs) != 1 || evs[0].Data["delivery"] != "q-1" {
		t.Fatalf("recorded %+v", evs)
	}
	// The same body straight to the listener, ten minutes late, is refused.
	req := httptest.NewRequest(http.MethodPost, "/hooks/acme", bytes.NewReader(body))
	req.Header = y.acmeHeaders(body, "q-1")
	rec := httptest.NewRecorder()
	y.rc.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ten minutes late to the listener: %d, want 401", rec.Code)
	}
	if evs := y.hookEvents(t); len(evs) != 1 {
		t.Fatalf("the refused delivery recorded: %+v", evs)
	}
}

// A queued delivery the yard refuses is acked all the same: the edge
// deletes it instead of sending it again on every connect.
func TestLinkAcksAQueuedDeliveryItRefuses(t *testing.T) {
	t.Parallel()
	y := queuedLink(t)
	l := y.next(t)
	body := queuedBody(y.now)
	h := y.acmeHeaders(body, "q-bad")
	h.Set("Acme-Signature", "sha256="+"0b0b0b")
	f := queued(t, l, "/hooks/acme", h, body, y.now)
	send(t, l, f)
	if id := acked(t, l); id != f.ID {
		t.Fatalf("acked %q, want %q", id, f.ID)
	}
	if evs := y.hookEvents(t); len(evs) != 0 {
		t.Fatalf("a refused delivery recorded: %+v", evs)
	}
	// A token delivery that names no id is refused as well, since nothing
	// could tell its resend from a new delivery, and acked all the same.
	tok := http.Header{}
	tok.Set("Authorization", "Bearer "+cronSecret)
	g := queued(t, l, "/hooks/cron", tok, []byte(`{"text":"backup ok"}`), y.now)
	send(t, l, g)
	if id := acked(t, l); id != g.ID {
		t.Fatalf("acked %q, want %q", id, g.ID)
	}
	if evs := y.hookEvents(t); len(evs) != 0 {
		t.Fatalf("a token delivery with no id recorded: %+v", evs)
	}
}

// A delivery to a hook with a delivery id, sent over the link a second time
// after its ack was lost, is recorded once: the edge sends the kept entry
// again on the next connect, under its own id, and the yard's dedupe drops
// the second.
func TestLinkRecordsARedeliveryAfterALostAckOnce(t *testing.T) {
	t.Parallel()
	barrier := &handlerBarrier{entered: make(chan struct{}), released: make(chan struct{})}
	y := queuedLinkWithBarrier(t, barrier)
	first := y.next(t)
	sent := y.now
	body := queuedBody(sent)
	h := y.acmeHeaders(body, "q-1")
	f := queued(t, first, "/hooks/acme", h, body, sent)
	send(t, first, f)
	y.recorded(t, 1)
	// The ack was lost with the socket: the test closes the link without
	// waiting for it, and plays the edge sending the kept entry again on
	// the next connect, under its own id.
	closeLinkBeforeAck(t, first, barrier)
	second := y.next(t)
	g := queued(t, second, "/hooks/acme", h, body, sent)
	g.ID = f.ID
	g.Sig = second.Sign(nil, g)
	send(t, second, g)
	if id := acked(t, second); id != f.ID {
		t.Fatalf("acked %q, want %q", id, f.ID)
	}
	if evs := y.hookEvents(t); len(evs) != 1 {
		t.Fatalf("the redelivery recorded again: %+v", evs)
	}
	if got := second.Acks(); len(got) != 1 || got[0] != f.ID {
		t.Fatalf("acked %v, want [%s]", got, f.ID)
	}
}

// A token hook takes no delivery without its id, so every delivery it records
// has a dedupe key: the same delivery sent again after its ack was lost is
// recorded once, as an acme delivery is, and the link delivers it exactly
// once. The count is pinned, so a hook that records a delivery with no key,
// and so at least once, is seen.
func TestLinkRecordsATokenDeliveryOnceAfterALostAck(t *testing.T) {
	t.Parallel()
	barrier := &handlerBarrier{entered: make(chan struct{}), released: make(chan struct{})}
	y := queuedLinkWithBarrier(t, barrier)
	first := y.next(t)
	body := []byte(`{"text":"backup ok"}`)
	h := http.Header{}
	h.Set("Authorization", "Bearer "+cronSecret)
	h.Set("Yard-Delivery", "cron-1")
	h.Set("Content-Type", "application/json")
	f := queued(t, first, "/hooks/cron", h, body, y.now)
	send(t, first, f)
	y.recorded(t, 1)
	closeLinkBeforeAck(t, first, barrier)
	second := y.next(t)
	g := queued(t, second, "/hooks/cron", h, body, y.now)
	g.ID = f.ID
	g.Sig = second.Sign(nil, g)
	send(t, second, g)
	if id := acked(t, second); id != f.ID {
		t.Fatalf("acked %q, want %q", id, f.ID)
	}
	if evs := y.hookEvents(t); len(evs) != 1 {
		t.Fatalf("want 1 recorded delivery, have %d: %+v", len(evs), evs)
	}
}

// A test that fails before closeLinkBeforeAck fails with its message, and
// does not hang the package until go test's timeout: it leaves the first
// handler held at the barrier, and the link's stop, which its end runs, waits
// for the handlers the link serves. The subtest ends as such a test does,
// with the handler held, and the cleanup queuedLinkWithBarrier registered
// must have freed it by then. When it has not, the stop it is stuck in is
// freed late by the fuse below, which fails the subtest first, so the test
// fails after the fuse instead of hanging.
func TestQueuedLinkWithBarrierFreesAHeldHandlerWhenItsTestEnds(t *testing.T) {
	t.Parallel()
	barrier := &handlerBarrier{entered: make(chan struct{}), released: make(chan struct{})}
	t.Run("ends with the handler held", func(t *testing.T) {
		y := queuedLinkWithBarrier(t, barrier)
		first := y.next(t)
		body := queuedBody(y.now)
		send(t, first, queued(t, first, "/hooks/acme", y.acmeHeaders(body, "q-1"), body, y.now))
		select {
		case <-barrier.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the handler did not reach the ack barrier")
		}
		// The fuse starts as the body ends, where the cleanups begin, and the
		// first of them is the release. The subtest cannot finish before the
		// fuse has released the handler, so its Error is never too late.
		go func() {
			select {
			case <-barrier.released:
			case <-time.After(10 * time.Second):
				t.Error("the test's cleanup did not free the handler held at the barrier: the link's stop waited on it")
				barrier.release()
			}
		}()
	})
}

// A live delivery is answered with a response for its sender, and no ack:
// acks are for queued deliveries, which the edge deletes on them.
func TestLinkAnswersALiveDeliveryWithAResponseAndNoAck(t *testing.T) {
	t.Parallel()
	y := queuedLink(t)
	l := y.next(t)
	body := queuedBody(y.now)
	f := l.Frame(http.MethodPost, "/hooks/acme", y.acmeHeaders(body, "q-live"), body, false)
	if a := deliver(t, l, f); a.Status != http.StatusOK {
		t.Fatalf("a live delivery answered %+v", a)
	}
	if got := l.Acks(); len(got) != 0 {
		t.Fatalf("a live delivery acked: %v", got)
	}
	if evs := y.hookEvents(t); len(evs) != 1 {
		t.Fatalf("recorded %+v", evs)
	}
}

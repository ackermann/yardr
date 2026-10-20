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
	st  store.API
	rc  *hooks.Receiver
	now time.Time
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
	rc.SetClock(func() time.Time { return now })
	y := linkTo(t, func(l *Link) { l.Handler = rc.Handler() }, nil)
	return &queuedYard{linked: y, st: st, rc: rc, now: now}
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

// recorded waits for the yard to have recorded n deliveries: the store is
// written by the handler, and the test that closes the link before the ack
// waits on this instead.
func (y *queuedYard) recorded(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if evs := y.hookEvents(t); len(evs) == n {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("want %d recorded deliveries, have %d", n, len(evs))
		}
		time.Sleep(10 * time.Millisecond)
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
}

// A delivery to a hook with a delivery id, sent over the link a second time
// after its ack was lost, is recorded once: the edge sends the kept entry
// again on the next connect, under its own id, and the yard's dedupe drops
// the second.
func TestLinkRecordsARedeliveryAfterALostAckOnce(t *testing.T) {
	t.Parallel()
	y := queuedLink(t)
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
	first.Close()
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

// A token hook with no delivery id has no dedupe key, so the same delivery
// sent again after its ack was lost is recorded again: for those hooks the
// link delivers at least once (design yardr-hosted-050n, 3.4). The count is
// pinned exactly, so a change to at-least-once is seen.
func TestLinkRecordsATokenDeliveryTwiceAfterALostAck(t *testing.T) {
	t.Parallel()
	y := queuedLink(t)
	first := y.next(t)
	body := []byte(`{"text":"backup ok"}`)
	h := http.Header{}
	h.Set("Authorization", "Bearer "+cronSecret)
	h.Set("Content-Type", "application/json")
	f := queued(t, first, "/hooks/cron", h, body, y.now)
	send(t, first, f)
	y.recorded(t, 1)
	first.Close()
	second := y.next(t)
	g := queued(t, second, "/hooks/cron", h, body, y.now)
	g.ID = f.ID
	g.Sig = second.Sign(nil, g)
	send(t, second, g)
	if id := acked(t, second); id != f.ID {
		t.Fatalf("acked %q, want %q", id, f.ID)
	}
	if evs := y.hookEvents(t); len(evs) != 2 {
		t.Fatalf("want 2 recorded deliveries, have %d: %+v", len(evs), evs)
	}
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

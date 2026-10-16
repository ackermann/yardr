package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

// A yard takes PeerEventLimit bead events from one peer within an hour,
// the stale ones counted, apart from its mail. What comes after is refused
// as rate_limited, with one event for all of it, until that hour has
// passed.
func TestPeerEventLimit(t *testing.T) {
	// Sequential: the package's fake clock.
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })

	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ctx := context.Background()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPeer(ctx, Peer{Name: "north", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(ctx, "north", PeerAllow{Receive: []string{PeerThread}}, "jo"); err != nil {
		t.Fatal(err)
	}
	n := 0
	receive := func() string {
		t.Helper()
		n++
		body, _ := json.Marshal(PeerEventMessage{V: 1, Bead: "web-0wzb", Event: PeerEventHeld, Stage: "build", Held: true, At: at})
		got, err := s.PeerReceive(ctx, "north", yardkey.Public(k), []PeerMessage{{ID: fmt.Sprintf("e%d", n), Kind: PeerBeadEvent, Re: "nope", Body: body}})
		if err != nil {
			t.Fatal(err)
		}
		return got[0].Refused
	}
	refusals := func() (n int) {
		t.Helper()
		evs, err := s.Events(ctx, "", 2000)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == "peer_message_refused" && e.Data["reason"] == RefusedRate {
				if e.Data["kind"] != PeerBeadEvent || e.Data["limit"] != float64(PeerEventLimit) {
					t.Fatalf("the refusal = %+v", e)
				}
				n++
			}
		}
		return n
	}
	if PeerEventLimit != 240 {
		t.Fatalf("a yard takes %d bead events an hour, want 240", PeerEventLimit)
	}
	for i := 0; i < PeerEventLimit; i++ {
		at = at.Add(time.Second)
		if got := receive(); got != "" {
			t.Fatalf("event %d of the hour = %q", i+1, got)
		}
	}
	for i := 0; i < 5; i++ {
		at = at.Add(time.Second)
		if got := receive(); got != RefusedRate {
			t.Fatalf("event %d over the limit = %q", i+1, got)
		}
	}
	if got := refusals(); got != 1 {
		t.Fatalf("%d events for the bead events over the limit, want 1", got)
	}
	// They are not the peer's mail: its hour of that is untouched.
	p, err := s.GetPeer(ctx, "north")
	if err != nil || p.Mails != 0 || p.MailLimited || p.Events != PeerEventLimit+5 || !p.EventLimited {
		t.Fatalf("the peer's counts = %+v, %v", p, err)
	}
	// One second short of the hour since the first, and then the hour.
	at = time.Date(2026, 10, 12, 13, 0, 0, 0, time.UTC)
	if got := receive(); got != RefusedRate {
		t.Fatalf("an event a second before the hour has passed = %q", got)
	}
	at = at.Add(time.Second)
	if got := receive(); got != "" {
		t.Fatalf("an event once the hour has passed = %q", got)
	}
	for i := 0; i < PeerEventLimit; i++ {
		receive()
	}
	if got := refusals(); got != 2 {
		t.Fatalf("%d events for two hours over the limit, want 2", got)
	}
}

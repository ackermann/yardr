package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestPeerBeadEarlierWithEqualArrivalTimes(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	first := peerBead{At: at, Order: 1}
	second := peerBead{At: at, Order: 2}
	if !peerBeadEarlier(first, second) || peerBeadEarlier(second, first) {
		t.Fatal("same-time arrivals were not ordered by their receive sequence")
	}
}

// A yard takes PeerBeadLimit beads from one peer within an hour, those it
// refuses counted, apart from its mail and its bead events: the 31st is
// refused as rate_limited, with one event for all that follow, until that
// hour has passed. So a flow of the other yard that routes refused back
// into the stage this yard works cannot keep it busy.
func TestPeerBeadLimit(t *testing.T) {
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
	k := newKey(t)
	if _, err := s.AddPeer(ctx, Peer{Name: "north", Key: Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	// The link takes beads and has no intake: each is refused, and counted.
	if _, err := s.AllowPeer(ctx, "north", PeerAllow{Receive: []string{PeerBead}}, "jo"); err != nil {
		t.Fatal(err)
	}
	n := 0
	receive := func() string {
		t.Helper()
		n++
		body, _ := json.Marshal(PeerBeadMessage{V: PeerBeadVersion, Bead: "web-1", Stage: "build", Title: "Add the export"})
		got, err := s.PeerReceive(ctx, "north", Public(k), []PeerMessage{{ID: fmt.Sprintf("b%d", n), Kind: PeerBead, Body: body}})
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
				if e.Data["kind"] != PeerBead || e.Data["limit"] != float64(PeerBeadLimit) {
					t.Fatalf("the refusal = %+v", e)
				}
				n++
			}
		}
		return n
	}
	if PeerBeadLimit != 30 {
		t.Fatalf("a yard takes %d beads an hour, want 30", PeerBeadLimit)
	}
	for i := 0; i < PeerBeadLimit; i++ {
		at = at.Add(time.Second)
		if got := receive(); got != RefusedNoIntake {
			t.Fatalf("bead %d of the hour = %q", i+1, got)
		}
	}
	for i := 0; i < 3; i++ {
		at = at.Add(time.Second)
		if got := receive(); got != RefusedRate {
			t.Fatalf("bead %d over the limit = %q", i+1, got)
		}
	}
	if got := refusals(); got != 1 {
		t.Fatalf("%d events for the beads over the limit, want 1", got)
	}
	// They are not the peer's mail or its bead events.
	p, err := s.GetPeer(ctx, "north")
	if err != nil || p.Mails != 0 || p.Events != 0 || p.Arrivals != PeerBeadLimit+3 || !p.ArrivalLimited {
		t.Fatalf("the peer's counts = %+v, %v", p, err)
	}
	if got := PeerRefusal("north", PeerBead, RefusedRate); got != "it has taken as many beads from this yard as it takes in an hour" {
		t.Fatalf("what the sender is told = %q", got)
	}
	// One second short of the hour since the first, and then the hour.
	at = time.Date(2026, 10, 12, 13, 0, 0, 0, time.UTC)
	if got := receive(); got != RefusedRate {
		t.Fatalf("a bead a second before the hour has passed = %q", got)
	}
	at = at.Add(time.Second)
	if got := receive(); got != RefusedNoIntake {
		t.Fatalf("a bead once the hour has passed = %q", got)
	}
}

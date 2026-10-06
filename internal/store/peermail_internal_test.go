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

// A yard takes PeerMailLimit mails from one peer within an hour, counted
// from the first. What comes after is refused, with one event for all of
// it, until that hour has passed.
func TestPeerMailLimit(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
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
	if _, err := s.AllowPeer(ctx, "north", PeerAllow{Receive: []string{PeerMail}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutResident(ctx, Resident{Name: "mayor", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	n := 0
	receive := func(to string) string {
		t.Helper()
		n++
		body, _ := json.Marshal(peerMail{To: to, From: "mayor", Text: "hi"})
		got, err := s.PeerReceive(ctx, "north", yardkey.Public(k), []PeerMessage{{ID: fmt.Sprintf("m%d", n), Kind: PeerMail, Body: body}})
		if err != nil {
			t.Fatal(err)
		}
		return got[0].Refused
	}
	count := func(kind, reason string) (n int) {
		t.Helper()
		evs, err := s.Events(ctx, "", 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == kind && (reason == "" || e.Data["reason"] == reason) {
				n++
			}
		}
		return n
	}
	// Mail for nobody counts too: a yard cannot go on asking who lives here.
	if got := receive("nobody"); got != RefusedNoResident {
		t.Fatalf("mail for nobody = %q", got)
	}
	for i := 1; i < PeerMailLimit; i++ {
		at = at.Add(time.Minute)
		if got := receive("mayor"); got != "" {
			t.Fatalf("mail %d of the hour = %q", i+1, got)
		}
	}
	for i := 0; i < 5; i++ {
		at = at.Add(time.Minute)
		if got := receive("mayor"); got != RefusedRate {
			t.Fatalf("mail %d over the limit = %q", i+1, got)
		}
	}
	if got := count("peer_message_refused", RefusedRate); got != 1 {
		t.Fatalf("%d events for the mail over the limit, want 1", got)
	}
	if got := count("mail", ""); got != PeerMailLimit-1 {
		t.Fatalf("%d mails delivered, want %d", got, PeerMailLimit-1)
	}
	// One second short of the hour since the first, and then the hour.
	at = time.Date(2026, 10, 5, 12, 59, 59, 0, time.UTC)
	if got := receive("mayor"); got != RefusedRate {
		t.Fatalf("mail a second before the hour has passed = %q", got)
	}
	at = at.Add(time.Second)
	if got := receive("mayor"); got != "" {
		t.Fatalf("mail once the hour has passed = %q", got)
	}
	if got := count("mail", ""); got != PeerMailLimit {
		t.Fatalf("%d mails delivered, want %d", got, PeerMailLimit)
	}
	// The next hour's refusal is an event of its own.
	for i := 0; i < PeerMailLimit+1; i++ {
		receive("mayor")
	}
	if got := count("peer_message_refused", RefusedRate); got != 2 {
		t.Fatalf("%d events for two hours over the limit, want 2", got)
	}
}

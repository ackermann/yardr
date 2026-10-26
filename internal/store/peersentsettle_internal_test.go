package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

// A bead whose stay at a peer ended but whose outcome the settler has not
// applied is settled by the next open's first sweep: what is owed is read
// from the file, not from the dropped nudge, so closing with one queued
// loses nothing.
func TestPeerSentSettlesAcrossClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "y.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "peer.key")
	if _, _, err := yardkey.Ensure(keyFile); err != nil {
		t.Fatal(err)
	}
	north, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetYard(keyFile, "test")
	if _, err := s.AddPeer(ctx, Peer{Name: "north", Key: yardkey.Public(north)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, Depot{Name: "web", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	f, err := ParseFlow("build -> review on done | decide on refused | decide on undelivered\nreview -> merged\ndecide -> build\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "away"
	if _, err := s.PutFlow(ctx, *f, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "web", "", "away", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(ctx, "north", PeerAllow{Send: []string{"bead"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, Group{Name: "at-north", Runner: PeerRunner, Limit: 2, Config: map[string]any{"peer": "north"}}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, NewBead{Title: "Add the export", Depot: "web", Stage: "build"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "at-north", "build", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeerBeadSend(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunning(ctx, sess.ID, map[string]any{"peer": "north"}); err != nil {
		t.Fatal(err)
	}
	// The stay ends as endSent leaves it, but written directly, with no
	// nudge: the settler is owed a bead it was never woken for.
	if err := s.write(func(w *wtx) error {
		rec, err := peerSentOf(w.Tx, b.UUID)
		if err != nil || rec == nil {
			t.Fatal("no stay to end")
		}
		at := now()
		rec.Outcome, rec.EndedAt = "done", &at
		return put(w.Tx, bPeerSent, rec.Bead, rec)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	// Moved on with no further write: the reopened store's first sweep
	// settles what the close dropped.
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := s.GetBead(ctx, b.ID)
		if err == nil && got.Stage == "review" {
			evs, err := s.Events(ctx, b.ID, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range evs {
				if e.Kind == "advanced" && e.Actor == "peer:north" && e.Data["outcome"] == "done" {
					return
				}
			}
			t.Fatalf("no advanced event for the move: %+v", evs)
		}
		if time.Now().After(deadline) {
			t.Fatalf("bead %s not settled across the close: %+v, %v", b.ID, got, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

// A peer says what it understands with every request, every few seconds.
// The time kept with it is when it was last saved, and it is saved again,
// unchanged, only once that is an hour old.
func TestPeerSaidWhen(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
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
	key := yardkey.Public(k)
	if _, err := s.AddPeer(ctx, Peer{Name: "north", Key: key}, "jo"); err != nil {
		t.Fatal(err)
	}
	start := at
	for _, step := range []struct {
		after time.Duration
		want  time.Time
	}{
		{0, start},
		{30 * time.Minute, start},
		{59 * time.Minute, start},
		{time.Hour, start.Add(time.Hour)},
		{61 * time.Minute, start.Add(time.Hour)},
	} {
		at = start.Add(step.after)
		if err := s.PeerHeard(ctx, "north", key, Says("v1.2.3")); err != nil {
			t.Fatal(err)
		}
		p, err := s.GetPeer(ctx, "north")
		if err != nil || p.Said == nil || !p.Said.At.Equal(step.want) {
			t.Fatalf("said again after %s: %+v, %v; want said at %s", step.after, p.Said, err, step.want)
		}
	}
}

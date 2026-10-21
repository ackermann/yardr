package runner

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A yard without a key can send its peers nothing, which is the group's to
// be told of and no failure of the bead: the start is refused as blocked.
// The runner is one of the built-in ones, and its session one that waits.
func TestPeerRunner(t *testing.T) {
	t.Parallel()
	r, ok := All(t.TempDir(), nil, nil, nil)[store.PeerRunner].(*Peer)
	if !ok {
		t.Fatal("the peer runner is not one of the built-in runners")
	}
	if w, ok := dispatch.Runner(r).(dispatch.Waiter); !ok || w.WaitLimit() != PeerWait {
		t.Fatal("a peer session is not one that waits")
	}
	if alive, err := r.Alive(context.Background(), nil); !alive || err != nil {
		t.Fatalf("Alive = %v, %v", alive, err)
	}
	job := dispatch.Job{Bead: store.Bead{ID: "web-1"}, Group: store.Group{Name: "at-north", Runner: store.PeerRunner, Config: map[string]any{"peer": "north"}}}
	if _, err := r.Start(context.Background(), job); err == nil || !strings.Contains(err.Error(), "no store") {
		t.Fatalf("Start without a store: %v", err)
	}
	r.Store = storetest.New(t)
	if _, err := r.Start(context.Background(), job); !dispatch.IsBlocked(err) || !strings.Contains(err.Error(), "yardr peer key") {
		t.Fatalf("Start in a yard without a key: %v", err)
	}
}

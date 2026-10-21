package store_test

import (
	"slices"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// ListSessions with Running lists what this yard runs: a manual group's
// claim, which a person holds, and a peer group's, which another yard
// works, are left out. Without the flag both are listed (yardr-an0p).
func TestListSessionsRunningLeavesOutHeldAndPeer(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "backlog", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPeer(ctx, store.Peer{Name: "north", Key: otherKey(t)}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{"bead"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "north", Runner: "peer", Config: map[string]any{"peer": "north"}}, "me"); err != nil {
		t.Fatal(err)
	}
	claim := func(title, group string) {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: title, Depot: "yard", Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Claim(ctx, b.ID, group, "new", "me"); err != nil {
			t.Fatal(err)
		}
	}
	claim("worked here", "builders")
	claim("held by a person", "backlog")
	claim("worked by another yard", "north")

	groups := func(ss []store.Session) []string {
		var out []string
		for _, sess := range ss {
			out = append(out, sess.Group)
		}
		slices.Sort(out)
		return out
	}
	// Both reads honour the flag: the live sessions, and every session
	// with All, which ListSessions reads through another index.
	for _, all := range []bool{false, true} {
		got, err := s.ListSessions(ctx, store.SessionFilter{Running: true, All: all})
		if err != nil {
			t.Fatal(err)
		}
		if g := groups(got); !slices.Equal(g, []string{"builders"}) {
			t.Errorf("Running, All=%v: groups = %v, want [builders]", all, g)
		}
		got, err = s.ListSessions(ctx, store.SessionFilter{All: all})
		if err != nil {
			t.Fatal(err)
		}
		if g := groups(got); !slices.Equal(g, []string{"backlog", "builders", "north"}) {
			t.Errorf("unfiltered, All=%v: groups = %v, want the held and peer claims too", all, g)
		}
	}
}

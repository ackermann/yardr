package web

import (
	"context"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// Home's feed keeps what a person wants on a phone and leaves the rest one
// tap away behind "All activity": the kinds, without the store. A move needs
// the routes and the event beside it, so moves are tried over HTTP
// (homefeed_test.go).
func TestHomeKeepsKinds(t *testing.T) {
	rd := readerOf(nil, nil)
	ctx := context.Background()
	for _, c := range []struct {
		kind string
		data map[string]any
		want bool
	}{
		// Kept: a fault arriving and lifting, a prompt waiting and
		// answered, failures, a landing or a close, words, a release, a
		// reopen, a firing timer. An ask stands as its note.
		{"held", nil, true},
		{"unheld", nil, true},
		{"session_blocked", nil, true},
		{"session_unblocked", nil, true},
		{"gave_up", nil, true},
		{"crew_gave_up", nil, true},
		{"resident_gave_up", nil, true},
		{"session_harness_error", nil, true},
		{"session_failed", nil, true},
		{"session_died", nil, true},
		{"closed", nil, true},
		{"noted", nil, true},
		{"released", nil, true},
		{"reopened", nil, true},
		{"timer", nil, true},
		// Left out: the yard breathing, and bookkeeping whose place is
		// the bead's page. A crew member's turn is /crew's even at a
		// prompt, and a stalled or refused session stays the panel's.
		{"created", nil, false},
		{"claimed", nil, false},
		{"started", nil, false},
		{"updated", nil, false},
		{"dep_added", nil, false},
		{"subscribed", nil, false},
		{"workspace_removed", nil, false},
		{"daemon_replaced", nil, false},
		{"server_replaced", nil, false},
		{"crew_started", nil, false},
		{"crew_stopped", nil, false},
		{"crew_rotation_asked", nil, false},
		{"crew_status", map[string]any{"status": "blocked", "was": "working"}, false},
		{"crew_status", map[string]any{"status": "idle", "was": "blocked"}, false},
		{"resident_status", map[string]any{"status": "blocked", "was": "working"}, false},
		{"session_prompted", nil, false},
		{"session_idle", nil, false},
		{"session_stalled", nil, false},
		{"session_prompt_gave_up", nil, false},
		{"move_refused", nil, false},
		{"unrouted", nil, false},
		{"stranded", nil, false},
		{"no_runner", nil, false},
		{"crew_blocked", nil, false},
		{"waited", nil, false},
		{"nudge_held", nil, false},
		{"mail", map[string]any{"to": "jo"}, false},
		{"mail", map[string]any{"to": "jo", "timer": "t1"}, false},
		{"timer_set", nil, false},
	} {
		if got := inHomeFeed(ctx, rd, store.Event{Kind: c.kind, Data: c.data}); got != c.want {
			t.Errorf("%s: kept %v, want %v", c.kind, got, c.want)
		}
	}
}

// Home's feed keeps a note by a user or a crew member and leaves out one by
// the bead's own agent (<bead>-<stage>). A script group's (the landing's)
// needs the groups, so it is tried over HTTP (homefeed_test.go).
func TestHomeKeepsNotes(t *testing.T) {
	rd := readerOf(nil, nil)
	ctx := context.Background()
	bead := "b-1"
	for _, c := range []struct {
		actor string
		want  bool
	}{
		{"me", true},
		{"yardmaster", true},
		{"b-1-new", false},
		{"b-1-review", false},
	} {
		e := store.Event{Kind: "noted", Bead: &bead, Actor: c.actor}
		if got := inHomeFeed(ctx, rd, e); got != c.want {
			t.Errorf("noted by %s: kept %v, want %v", c.actor, got, c.want)
		}
	}
}

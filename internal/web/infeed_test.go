package web

import (
	"testing"

	"yardr.dev/yardr/internal/store"
)

// The feed leaves out what a view of the residents follows and nobody
// reads as news: mail being read, and a resident's turns beginning and
// ending. A resident stopping at a prompt, or leaving it, is news. So is a
// timer firing, and nothing else of a timer.
func TestFeedLeavesOutTurnsAndReading(t *testing.T) {
	status := func(was, now string) store.Event {
		return store.Event{Kind: "resident_status", Data: map[string]any{"resident": "mayor", "status": now, "was": was}}
	}
	for _, c := range []struct {
		e    store.Event
		want bool
	}{
		{store.Event{Kind: "mail_read"}, false},
		{status("idle", "working"), false},
		{status("", "idle"), false},
		{status("working", "blocked"), true},
		{status("blocked", "idle"), true},
		{store.Event{Kind: "mail"}, true},
		{store.Event{Kind: store.NoteJournal}, false},
		// Of a timer only the firing, and that once: the mail it is to a
		// user is the same news.
		{store.Event{Kind: "timer_set"}, false},
		{store.Event{Kind: "timer_dropped"}, false},
		{store.Event{Kind: "timer_removed"}, false},
		{store.Event{Kind: "timer"}, true},
		{store.Event{Kind: "mail", Data: map[string]any{"to": "jo", "timer": "t1"}}, false},
	} {
		if got := inFeed(c.e); got != c.want {
			t.Errorf("%s %v in the feed: %v, want %v", c.e.Kind, c.e.Data, got, c.want)
		}
	}
}

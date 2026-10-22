package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A stopped session whose agent's count is due is listed by Uncounted, which
// reads the pending index and not every session. RecordUsage then finishes
// its count in one write: the usage on the handle, the due entry off it, a
// session_usage event on the bead, and the session gone from Uncounted. A
// second call changes nothing, and a call that fails leaves nothing: no event
// for a session that is not there, and, for a bead that is gone, the handle
// as it was.
func TestRecordUsage(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	due := map[string]any{"stopped_at": "2026-10-22T08:00:00Z"}
	// session makes a session of a bead of its own, ended with the handle
	// and stopped or not.
	session := func(title string, handle map[string]any, stopped bool) store.Session {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: title, Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetHandle(ctx, sess.ID, handle); err != nil {
			t.Fatal(err)
		}
		if err := s.EndSession(ctx, sess.ID, "failed", "x"); err != nil {
			t.Fatal(err)
		}
		if stopped {
			if err := s.MarkStopped(ctx, sess.ID); err != nil {
				t.Fatal(err)
			}
		}
		return *sess
	}
	counted := session("counted", map[string]any{"agent_session": "a1", "count_due": due}, true)
	plain := session("not due", map[string]any{"agent_session": "a2"}, true)
	unstopped := session("not stopped", map[string]any{"agent_session": "a3", "count_due": due}, false)

	ids := func(ss []store.Session) []string {
		out := []string{}
		for _, sess := range ss {
			out = append(out, sess.ID)
		}
		return out
	}
	uncounted := func() []string {
		t.Helper()
		ss, err := s.Uncounted(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ids(ss)
	}
	// Only the stopped session with the entry is listed, and the listing
	// read the two pending ones (it and the unstopped), not the session that
	// is done.
	read := own.PendingReads()
	if got := uncounted(); !slices.Equal(got, []string{counted.ID}) {
		t.Fatalf("Uncounted = %v, want only %s: the %s session is not due and %s not stopped", got, counted.ID, plain.ID, unstopped.ID)
	}
	if n := own.PendingReads() - read; n != 2 {
		t.Errorf("Uncounted read %d sessions through the pending index, want the 2 that are pending", n)
	}

	usage := map[string]any{"input": 1000, "fresh_input": 400, "output": 50, "cache_read": 600, "cache_write": 10, "read_at": "2026-10-22T08:05:00Z"}
	if err := s.RecordUsage(ctx, counted.ID, usage); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	got, err := s.GetSession(ctx, counted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, got.Handle["usage"], usage) {
		t.Errorf("handle usage = %v, want %v", got.Handle["usage"], usage)
	}
	if _, has := got.Handle["count_due"]; has || got.Handle["agent_session"] != "a1" {
		t.Errorf("handle = %v, want count_due off it and the rest of it kept", got.Handle)
	}
	if left := uncounted(); len(left) != 0 {
		t.Errorf("Uncounted after the call = %v, want none", left)
	}
	if pend := own.PendingIndex(); slices.Contains(pend, counted.ID) {
		t.Errorf("pending index after the call = %q, want %s out of it", pend, counted.ID)
	}
	events := func(sess store.Session) []store.Event {
		t.Helper()
		var out []store.Event
		evs, err := s.Events(ctx, sess.Bead, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == "session_usage" {
				out = append(out, e)
			}
		}
		return out
	}
	evs := events(counted)
	if len(evs) != 1 {
		t.Fatalf("session_usage events of the bead = %d, want 1", len(evs))
	}
	if e := evs[0]; e.Actor != "yard" || e.Data["session"] != counted.ID || e.Data["group"] != "g" || e.Data["stage"] != "new" ||
		!sameJSON(t, e.Data["usage"], usage) {
		t.Errorf("session_usage = %+v, want the yard's, with the session, group, stage and the usage", e)
	}

	// A second call, even with no count, is nothing: the first stands and
	// the log has no second event.
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LogDispatch(ctx, counted.Bead, "session_usage", "yardmaster", map[string]any{
		"session": counted.ID, "usage": map[string]any{"input": 999999},
	}); err == nil {
		t.Fatal("LogDispatch took a session_usage event; only RecordUsage may write one")
	}
	if now, err := s.Head(ctx); err != nil || now != head {
		t.Errorf("log head after a refused session_usage = %d %v, want %d: no event", now, err, head)
	}
	if err := s.RecordUsage(ctx, counted.ID, nil); err != nil {
		t.Fatalf("RecordUsage again: %v", err)
	}
	again, err := s.GetSession(ctx, counted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, again.Handle["usage"], usage) || len(events(counted)) != 1 {
		t.Errorf("after the second call usage = %v with %d events, want the first count and 1 event", again.Handle["usage"], len(events(counted)))
	}
	if now, err := s.Head(ctx); err != nil || now != head {
		t.Errorf("log head after the second call = %d %v, want %d: nothing new", now, err, head)
	}

	// A session that is not there leaves no event, and a count of nothing
	// is refused: it would end the wait with neither a count nor a reason.
	if err := s.RecordUsage(ctx, "no-such-session", usage); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RecordUsage of a session that is not there = %v, want not found", err)
	}
	if err := s.RecordUsage(ctx, unstopped.ID, nil); err == nil {
		t.Error("RecordUsage with no usage was taken")
	}
	// The write that fails after the handle is changed, for a bead that is
	// gone, undoes the handle: the session is still due.
	if err := own.PutSession(store.Session{ID: "orphan", Bead: "no-such-bead", Group: "g", Stage: "new", State: "failed", Stopped: true,
		Handle: map[string]any{"count_due": due}, StartedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUsage(ctx, "orphan", usage); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RecordUsage of a session of a bead that is gone = %v, want not found", err)
	}
	orphan, err := s.GetSession(ctx, "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if _, has := orphan.Handle["usage"]; has || orphan.Handle["count_due"] == nil {
		t.Errorf("handle after the failed call = %v, want it as it was, with count_due and no usage", orphan.Handle)
	}
	if left := uncounted(); !slices.Equal(left, []string{"orphan"}) {
		t.Errorf("Uncounted after the failed call = %v, want the orphan, still due", left)
	}
	if now, err := s.Head(ctx); err != nil || now != head {
		t.Errorf("log head after the failed calls = %d %v, want %d: no event", now, err, head)
	}
}

// sameJSON reports whether a and b are the same value to a JSON reader, so a
// count the caller gave with ints is the one read back as numbers.
func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	x, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(x) == string(y)
}

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

// MarkCountDue leaves the count of a session yardr down stopped due, as the
// dispatcher's teardown does for one it stops: count_due on the handle with
// when it stopped, read by Uncounted, and, for a group that keeps its agents,
// the time up to which the count reads. A session with no handle started no
// agent and keeps none due; one that is not there is not found.
func TestMarkCountDue(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "keep", Runner: "fake", Limit: 10, Config: map[string]any{"keep": true}}, "me"); err != nil {
		t.Fatal(err)
	}
	// session ends one of group's with handle, stopped as down leaves it.
	session := func(title, group string, handle map[string]any) store.Session {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: title, Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, group, "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetHandle(ctx, sess.ID, handle); err != nil {
			t.Fatal(err)
		}
		if err := s.EndSession(ctx, sess.ID, "aborted", "stopped by yardr down"); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkStopped(ctx, sess.ID); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSession(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		return *got
	}
	plain := session("plain", "g", map[string]any{"agent": "a1", "agent_session": "s1"})
	kept := session("kept", "keep", map[string]any{"agent": "a2", "agent_session": "s2"})
	bare := session("bare", "g", nil)

	before := time.Now()
	if err := s.MarkCountDue(ctx, plain.ID); err != nil {
		t.Fatalf("MarkCountDue: %v", err)
	}
	if err := s.MarkCountDue(ctx, kept.ID); err != nil {
		t.Fatalf("MarkCountDue of a kept session: %v", err)
	}
	if err := s.MarkCountDue(ctx, bare.ID); err != nil {
		t.Fatalf("MarkCountDue of a session with no handle: %v", err)
	}
	after := time.Now()

	due := func(id string) map[string]any {
		t.Helper()
		sess, err := s.GetSession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := sess.Handle[store.CountDue].(map[string]any)
		return out
	}
	d := due(plain.ID)
	if len(d) != 1 {
		t.Fatalf("count_due of a plain session = %v, want stopped_at alone", d)
	}
	at, _ := d[store.CountStoppedAt].(string)
	stopped, err := time.Parse(time.RFC3339Nano, at)
	if err != nil || stopped.Before(before.Add(-time.Second)) || stopped.After(after.Add(time.Second)) {
		t.Errorf("stopped_at = %q (%v), want a time of this test", at, err)
	}
	if got, err := s.GetSession(ctx, plain.ID); err != nil || got.Handle["agent_session"] != "s1" {
		t.Errorf("handle after the call = %v, %v: want the rest of it kept", got.Handle, err)
	}
	k := due(kept.ID)
	if len(k) != 2 {
		t.Fatalf("count_due of a kept session = %v, want stopped_at and until", k)
	}
	untilAt, _ := k[store.CountUntil].(string)
	until, err := time.Parse(time.RFC3339Nano, untilAt)
	if err != nil || kept.EndedAt == nil || !until.Equal(kept.EndedAt.Add(2*time.Minute)) {
		t.Errorf("until = %q (%v), want two minutes after the session ended (%v)", untilAt, err, kept.EndedAt)
	}
	if d := due(bare.ID); d != nil {
		t.Errorf("count_due of a session with no handle = %v, want none", d)
	}
	uncounted, err := s.Uncounted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, sess := range uncounted {
		ids = append(ids, sess.ID)
	}
	slices.Sort(ids)
	want := []string{kept.ID, plain.ID}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Errorf("Uncounted = %v, want %v: the session with no handle is not due", ids, want)
	}
	if err := s.MarkCountDue(ctx, "no-such-session"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("MarkCountDue of a session that is not there = %v, want not found", err)
	}
}

// A crew session that ended is listed in Uncounted by the write that ended it,
// for the line session and a conversation's alike, and only when it ran an
// agent: one with no handle has nothing to count. RecordCrewUsage then finishes
// the count in one write, taking its entry off and logging a crew_usage event
// on no bead, and leaves the others as they were. A second call for the same
// id changes nothing, and a call that cannot record fails without a trace.
func TestRecordCrewUsage(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	serveYardmaster(t, s, ctx, "", map[string]string{"hal": "/tmp/hal.env"})

	crew := func() *store.CrewMember {
		t.Helper()
		r, err := s.GetCrew(ctx, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// run starts a session of user ("": the line session) with handle and ends
	// it, by being gone or being stopped. It answers when the session started.
	run := func(user string, handle map[string]any, gone bool) time.Time {
		t.Helper()
		if err := s.CrewStartedFor(ctx, "yardmaster", user, handle); err != nil {
			t.Fatal(err)
		}
		started := *crew().Session(user).StartedAt
		end := s.CrewStoppedFor
		if gone {
			end = s.CrewGoneFor
		}
		if err := end(ctx, "yardmaster", user, "done"); err != nil {
			t.Fatal(err)
		}
		return started
	}
	// waiting is the ids of the sessions of user waiting for their count.
	waiting := func(user string) []string {
		t.Helper()
		out := []string{}
		for _, due := range crew().Session(user).Uncounted {
			out = append(out, due.AgentSession)
		}
		return out
	}
	events := func() []store.Event {
		t.Helper()
		evs, err := s.EventsOfKind(ctx, "crew_usage", 100)
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}

	before := time.Now().Truncate(time.Microsecond)
	started := run("", map[string]any{"agent": "ym-1", "kind": "claude", "agent_session": "a1"}, false)
	run("", map[string]any{"agent": "ym-2", "kind": "claude", "agent_session": "a2"}, true)
	run("", nil, false)                                                                          // no agent was started: nothing to count
	run("", map[string]any{"agent": "ym-4", "kind": "muse"}, false)                              // the runtime named no id
	run("hal", map[string]any{"agent": "ym-hal", "kind": "codex", "agent_session": "h1"}, false) // a conversation's session
	if got := waiting(""); !slices.Equal(got, []string{"a1", "a2", ""}) {
		t.Fatalf("waiting for the line session = %q, want a1, a2 and the one with no id", got)
	}
	if got := waiting("hal"); !slices.Equal(got, []string{"h1"}) {
		t.Fatalf("waiting for hal = %q, want h1, and none on the line session's list", got)
	}
	// The entry holds what the handle held, and when the session began and ended.
	first := crew().Uncounted[0]
	if first.Kind != "claude" || first.StartedAt == nil || !first.StartedAt.Equal(started) {
		t.Fatalf("first entry = %+v, want claude, started at %v", first, started)
	}
	if first.StoppedAt.Before(before) || first.StoppedAt.After(time.Now()) {
		t.Fatalf("stopped_at = %v, want a time of this test", first.StoppedAt)
	}
	if len(events()) != 0 {
		t.Fatalf("crew_usage events before any count: %d", len(events()))
	}

	usage := map[string]any{"input": 1000, "fresh_input": 400, "output": 50, "cache_read": 600, "cache_write": 10, "read_at": "2026-10-22T08:05:00Z"}
	if err := s.RecordCrewUsage(ctx, "yardmaster", "", "a2", usage); err != nil {
		t.Fatalf("RecordCrewUsage: %v", err)
	}
	if got := waiting(""); !slices.Equal(got, []string{"a1", ""}) {
		t.Fatalf("waiting after counting a2 = %q, want the others as they were", got)
	}
	evs := events()
	if len(evs) != 1 {
		t.Fatalf("crew_usage events = %d, want 1", len(evs))
	}
	if e := evs[0]; e.Actor != "yard" || e.Bead != nil || e.Data["member"] != "yardmaster" || e.Data["kind"] != "claude" ||
		e.Data["agent_session"] != "a2" || !sameJSON(t, e.Data["usage"], usage) || e.Data["user"] != nil {
		t.Errorf("crew_usage = %+v, want the yard's, on no bead, of yardmaster's claude a2 with the usage and no user", e)
	}
	if len(crew().Left) != 1 {
		t.Errorf("left = %v: counting a session is no part of closing what it left", crew().Left)
	}

	// A second call for the same id is nothing: no event, and not even a new
	// revision of the crew member.
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rev := crew().Revision
	if err := s.RecordCrewUsage(ctx, "yardmaster", "", "a2", map[string]any{"input": 999999}); err != nil {
		t.Fatalf("RecordCrewUsage again: %v", err)
	}
	if err := s.RecordCrewUsage(ctx, "yardmaster", "", "a2", nil); err != nil {
		t.Fatalf("RecordCrewUsage again with no count: %v", err)
	}
	if now, err := s.Head(ctx); err != nil || now != head || len(events()) != 1 || crew().Revision != rev {
		t.Errorf("after the second call: log head %d (%v), %d events, revision changed %v; want nothing new", now, err, len(events()), crew().Revision != rev)
	}

	// A count of nothing is refused while the entry waits, and leaves it.
	if err := s.RecordCrewUsage(ctx, "yardmaster", "", "a1", nil); err == nil {
		t.Error("RecordCrewUsage with no usage was taken")
	}
	if err := s.RecordCrewUsage(ctx, "no-such-member", "", "a1", usage); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RecordCrewUsage of a crew member that is not there = %v, want not found", err)
	}
	if err := s.RecordCrewUsage(ctx, "yardmaster", "no-such-user", "a1", usage); err == nil {
		t.Error("RecordCrewUsage of a conversation session that is not there succeeded")
	}
	if got := waiting(""); !slices.Equal(got, []string{"a1", ""}) || len(events()) != 1 {
		t.Fatalf("after the refused calls: waiting %q, %d events: want no change", got, len(events()))
	}

	// The session with no id is told apart by having none, and says why there
	// is no count; a conversation's carries its user, and the line session's
	// entries stay. The config no longer declares hal's conversation by now, and
	// its row stays writable, as it is for stopping what still ran of it.
	why := map[string]any{"missing": "no session id", "read_at": "2026-10-22T08:06:00Z"}
	if err := s.RecordCrewUsage(ctx, "yardmaster", "", "", why); err != nil {
		t.Fatal(err)
	}
	serveYardmaster(t, s, ctx, "", nil)
	if err := s.RecordCrewUsage(ctx, "yardmaster", "hal", "h1", usage); err != nil {
		t.Fatal(err)
	}
	if got := waiting(""); !slices.Equal(got, []string{"a1"}) || len(waiting("hal")) != 0 {
		t.Fatalf("waiting = %q, hal's %q: want a1 alone", got, waiting("hal"))
	}
	evs = events()
	if len(evs) != 3 {
		t.Fatalf("crew_usage events = %d, want 3", len(evs))
	}
	if e := evs[1]; e.Data["kind"] != "muse" || e.Data["agent_session"] != "" || !sameJSON(t, e.Data["usage"], why) {
		t.Errorf("the count with no id = %+v, want muse, no id and the reason", e.Data)
	}
	if e := evs[2]; e.Data["user"] != "hal" || e.Data["kind"] != "codex" || e.Data["agent_session"] != "h1" {
		t.Errorf("hal's count = %+v, want hal's codex h1", e.Data)
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

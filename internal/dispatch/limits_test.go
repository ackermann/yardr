package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A provider of limits is slow, and may be bounded in how often it answers:
// it is asked where something stopped on a limit, or a person asks, and
// never by a pass that finds nothing stopped. With a source that says the
// limits of the agents' kind, passes that start a session, find it working
// and find it idle with nothing wrong do not run its command once.
func TestPassWithNothingStoppedAsksNoLimits(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	asked, script := filepath.Join(dir, "asked"), filepath.Join(dir, "provider")
	scripttest.Write(t, script, "#!/bin/sh\necho \"$1\" >>"+asked+"\necho '{\"provider\":\"Kimi\",\"windows\":[]}'\n")
	_, err := s.PutSource(ctx, "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+", \"{provider}\", --json]\n  kinds: { kimi: kimi }\n", "me")
	must(t, err)

	r := &agentRunner{statusRunner: &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}, worked: true}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 2, Config: map[string]any{"kind": "kimi"}}, "me"))
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	must(t, err)
	for _, title := range []string{"x", "y"} {
		_, err := s.CreateBead(ctx, store.NewBead{Title: title}, "me")
		must(t, err)
	}
	must(t, d.Reconcile(ctx)) // both start
	must(t, d.Reconcile(ctx)) // seen working
	if n := len(r.starts()); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
	r.show("idle", "› Ask Kimi to do anything")
	must(t, d.Reconcile(ctx)) // idle, and stopped on nothing
	must(t, d.Reconcile(ctx))
	// A daemon that has just started, too.
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, Backoff: time.Hour}
	must(t, d2.Reconcile(ctx))
	if b, err := os.ReadFile(asked); err == nil {
		t.Fatalf("the limits command was run by a pass with nothing stopped, for: %q", b)
	}
}

// A session whose agent said it waits, not stuck, is neither told nor
// prodded while its snooze lasts, however far past the idle limit the
// clock moves. When the snooze ends the key goes, the count of
// consecutive snoozes stays, and the idle count starts fresh from the
// next pass: the handover nudge goes, then session_idle one limit on.
func TestSnoozedSessionIsNotIdleAndCountsFreshAfter(t *testing.T) {
	t.Parallel()
	s, r, d, b := harnessSetup(t, "muse", map[string]any{"idle": "1m", "stall": "off", "grace": "0"})
	n := &tallyRunner{agentRunner: r}
	d.Runners["fake"] = n
	ctx := context.Background()
	sess := liveSession(t, s, b.ID)
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	snooze := map[string]any{"session": sess, "agent": "yard-x-new",
		"snooze_until": now.Add(10 * time.Minute).UTC().Format(time.RFC3339), "snooze_count": 1}
	must(t, s.SetHandle(ctx, sess, snooze))
	idles := func() int { t.Helper(); return len(eventKinds(t, s, b.ID, "session_idle")) }
	nudges := func() int { t.Helper(); return len(eventKinds(t, s, b.ID, "session_idle_nudge")) }

	r.show("idle", museGone)
	must(t, d.Reconcile(ctx))
	now = now.Add(9 * time.Minute) // past the idle limit, inside the snooze
	must(t, d.Reconcile(ctx))
	if idles() != 0 || nudges() != 0 || len(n.told()) != 0 {
		t.Fatalf("session_idle = %d, session_idle_nudge = %d, nudges typed = %d; want none while snoozed",
			idles(), nudges(), len(n.told()))
	}

	now = now.Add(2 * time.Minute) // the snooze is over
	must(t, d.Reconcile(ctx))
	h := handleOfBead(t, s, b)
	if h["snooze_until"] != nil {
		t.Errorf("handle still carries snooze_until = %v after the snooze ended", h["snooze_until"])
	}
	if c := snoozeCount(h); c != 1 {
		t.Errorf("snooze_count = %d after the snooze ended, want 1: only a pane that changed breaks the run", c)
	}
	if idles() != 0 || nudges() != 0 {
		t.Fatalf("session_idle = %d, session_idle_nudge = %d on the pass the snooze ended; want none yet",
			idles(), nudges())
	}

	// The idle count starts fresh from the next pass: the handover nudge
	// goes there, and session_idle one limit after it.
	must(t, d.Reconcile(ctx))
	if got := n.told(); len(got) != 1 || !strings.Contains(got[0], "write your note and hand on") {
		t.Fatalf("nudges = %q, want the handover nudge once the snooze is over", got)
	}
	now = now.Add(61 * time.Second)
	must(t, d.Reconcile(ctx))
	if idles() != 1 {
		t.Fatalf("session_idle events = %d, want 1 one limit after the snooze ended", idles())
	}
}

// The group's age limit is not moved by a snooze: a snoozed session past
// max_age is told with session_aged, and the wait is kept.
func TestSnoozedSessionStillAges(t *testing.T) {
	t.Parallel()
	s, r, d, b := harnessSetup(t, "muse", map[string]any{"idle": "off", "stall": "off", "max_age": "1s", "grace": "1h"})
	ctx := context.Background()
	sess := liveSession(t, s, b.ID)
	must(t, s.SetHandle(ctx, sess, map[string]any{"session": sess, "agent": "yard-x-new",
		"snooze_until": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "snooze_count": 1}))
	r.show("idle", museGone)
	time.Sleep(time.Second) // age is read from the wall clock, not the dispatcher's
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if n := len(eventKinds(t, s, b.ID, "session_aged")); n != 1 {
		t.Fatalf("session_aged events = %d, want 1: max_age applies to a snoozed session", n)
	}
	if h := handleOfBead(t, s, b); h["snooze_until"] == nil {
		t.Fatalf("handle = %v, want the snooze kept past max_age", h)
	}
}

// The count of snoozes in a row stands through idle passes and ends when
// the agent is seen working: a harness at work redraws its screen, so its
// pane changed, which is what breaks the run.
func TestSnoozeRunEndsWhenThePaneChanges(t *testing.T) {
	t.Parallel()
	s, r, d, b := harnessSetup(t, "muse", map[string]any{"idle": "1h", "stall": "off"})
	ctx := context.Background()
	sess := liveSession(t, s, b.ID)
	must(t, s.SetHandle(ctx, sess, map[string]any{"session": sess,
		"snooze_until": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "snooze_count": 3}))
	r.show("idle", museGone)
	must(t, d.Reconcile(ctx))
	if c := snoozeCount(handleOfBead(t, s, b)); c != 3 {
		t.Fatalf("snooze_count = %d after an idle pass, want 3", c)
	}
	r.show("working", "◆ Working")
	must(t, d.Reconcile(ctx))
	if h := handleOfBead(t, s, b); h["snooze_until"] != nil || h["snooze_count"] != nil {
		t.Fatalf("handle = %v after the agent worked, want the snooze and its count gone", h)
	}
}

// snoozeCount is the run of consecutive snoozes a handle carries. A handle
// read back has passed through JSON, which made the count a float.
func snoozeCount(h map[string]any) int {
	switch n := h["snooze_count"].(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return -1
}

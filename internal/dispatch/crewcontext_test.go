package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Past 60% of the window an idle crew session is asked once for a
// handoff, and never restarted for it: no grace, no forced restart. The
// nudge writes crew_context_asked, the reading is recorded for crew show,
// and a new session is asked again.
func TestContextAsksOnceAndNeverForces(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{HandoffGrace: "1ms"})
	ctx := context.Background()
	f.ctxOK, f.ctxUsed = true, 120000 // 60% of the 200k window
	must(t, d.Reconcile(ctx))         // start
	must(t, d.Reconcile(ctx))         // idle at 60%: asked
	if len(f.nudges) != 1 || !strings.HasPrefix(f.nudges[0], "yardmaster-1: Your context is 60% of the window. ") {
		t.Fatalf("nudges %v", f.nudges)
	}
	r, _ := s.GetCrew(ctx, "yardmaster")
	if r.ContextAskedAt == nil || r.ContextPct != 60 || r.ContextWindow != 200000 || r.ContextAt == nil {
		t.Fatalf("after the ask: %+v", r.CrewSession)
	}
	evs := eventKinds(t, s, r.Journal, "crew_context_asked")
	if len(evs) != 1 || evs[0].Data["pct"] != float64(60) || evs[0].Data["window"] != float64(200000) {
		t.Fatalf("crew_context_asked events = %+v", evs)
	}
	f.ctxUsed = 190000 // heavier still: asked already, and no restart after any grace
	time.Sleep(5 * time.Millisecond)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 || f.n != 1 {
		t.Fatalf("nudges %v starts %d, want the one ask and no restart", f.nudges, f.n)
	}
	// A new session is asked again.
	must(t, s.Journal(ctx, "yardmaster", store.NoteHandoff, "Next: nothing", "me"))
	must(t, d.Reconcile(ctx)) // restart requested ...
	must(t, d.Reconcile(ctx)) // ... and carried out
	must(t, d.Reconcile(ctx)) // the new session, idle at 95%: asked
	if len(f.nudges) != 2 || f.n != 2 {
		t.Fatalf("nudges %v starts %d, want the new session asked", f.nudges, f.n)
	}
}

// Below the threshold nothing is asked, but the reading is still
// recorded. A custom threshold and a 1m window are read from the member,
// and 0 asks never.
func TestContextThresholds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("below 60 nothing is asked", func(t *testing.T) {
		t.Parallel()
		s, f, d := crewSetup(t, store.CrewMember{})
		f.ctxOK, f.ctxUsed = true, 100000 // 50%
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx))
		if len(f.nudges) != 0 {
			t.Fatalf("nudges %v", f.nudges)
		}
		if r, _ := s.GetCrew(ctx, "yardmaster"); r.ContextAskedAt != nil || r.ContextPct != 50 || r.ContextAt == nil {
			t.Fatalf("reading = %+v", r.CrewSession)
		}
	})
	t.Run("a custom threshold", func(t *testing.T) {
		t.Parallel()
		_, f, d := crewSetup(t, store.CrewMember{ContextHandoff: "80"})
		f.ctxOK, f.ctxUsed = true, 120000 // 60%: under it
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx))
		f.ctxUsed = 160000 // 80%: asked
		must(t, d.Reconcile(ctx))
		if len(f.nudges) != 1 || !strings.Contains(f.nudges[0], "Your context is 80% of the window. ") {
			t.Fatalf("nudges %v", f.nudges)
		}
	})
	t.Run("a 1m window", func(t *testing.T) {
		t.Parallel()
		s, f, d := crewSetup(t, store.CrewMember{Config: map[string]any{"model": "opus[1m]"}})
		f.ctxOK, f.ctxUsed = true, 600000 // 60% of a million
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx))
		if len(f.nudges) != 1 {
			t.Fatalf("nudges %v", f.nudges)
		}
		if r, _ := s.GetCrew(ctx, "yardmaster"); r.ContextPct != 60 || r.ContextWindow != 1000000 {
			t.Fatalf("reading = %+v", r.CrewSession)
		}
	})
	t.Run("0 asks never", func(t *testing.T) {
		t.Parallel()
		s, f, d := crewSetup(t, store.CrewMember{ContextHandoff: "0"})
		f.ctxOK, f.ctxUsed = true, 190000
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx))
		if len(f.nudges) != 0 {
			t.Fatalf("nudges %v", f.nudges)
		}
		if r, _ := s.GetCrew(ctx, "yardmaster"); r.ContextAskedAt != nil || r.ContextPct != 95 {
			t.Fatalf("reading = %+v", r.CrewSession)
		}
	})
}

// A kind with no context source is never read for one and never nudged,
// whatever the session holds: the 24h failsafe alone.
func TestContextWithoutASourceNeverNudges(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.ctxUsed = 190000 // ... but the kind names no source
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 0 {
		t.Fatalf("nudges %v", f.nudges)
	}
	if r, _ := s.GetCrew(ctx, "yardmaster"); r.ContextAskedAt != nil || r.ContextAt != nil {
		t.Fatalf("a reading without a source: %+v", r.CrewSession)
	}
}

// The use is read on an idle pass alone: a working session is not read,
// and a nudge held by text in the input asks on a later pass, not twice.
func TestContextReadWhenIdleAskedWhenTyped(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.ctxOK, f.ctxUsed = true, 190000
	must(t, d.Reconcile(ctx)) // start
	agent := agentOf(t, s)
	f.set(agent, "working")
	must(t, d.Reconcile(ctx))
	if f.ctxCalls != 0 {
		t.Fatalf("a working session was read %d times", f.ctxCalls)
	}
	f.set(agent, "idle")
	f.input = "half a thought" // the nudge is held, and nothing recorded as asked
	must(t, d.Reconcile(ctx))
	if r, _ := s.GetCrew(ctx, "yardmaster"); r.ContextAskedAt != nil || r.ContextPct != 95 {
		t.Fatalf("held nudge: %+v", r.CrewSession)
	}
	f.input = ""
	must(t, d.Reconcile(ctx))
	if r, _ := s.GetCrew(ctx, "yardmaster"); len(f.nudges) != 1 || r.ContextAskedAt == nil {
		t.Fatalf("nudges %v, asked at %v", f.nudges, r.ContextAskedAt)
	}
}

// A session already asked for its age is not asked for its use too: the
// rotation's nudge says it.
func TestContextNotAskedPastMaxAge(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{MaxAge: "1ms", HandoffGrace: "1h"})
	ctx := context.Background()
	f.ctxOK, f.ctxUsed = true, 190000
	must(t, d.Reconcile(ctx))
	time.Sleep(5 * time.Millisecond)
	must(t, d.Reconcile(ctx)) // past max_age: asked for its age
	must(t, d.Reconcile(ctx)) // ... and not for its use
	if len(f.nudges) != 1 || !strings.Contains(f.nudges[0], "due for rotation") {
		t.Fatalf("nudges %v", f.nudges)
	}
	if r, _ := s.GetCrew(ctx, "yardmaster"); r.RotationAskedAt == nil || r.ContextAskedAt != nil || r.ContextPct != 95 {
		t.Fatalf("asked = %+v", r.CrewSession)
	}
}

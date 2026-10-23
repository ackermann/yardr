package dispatch

import (
	"context"
	"errors"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// The keys of count_due (store.CountDue), the entry on a stopped session's
// handle that says its agent's count of tokens is due: when the daemon stopped
// the session, and, for one whose agent was left running (a group with keep),
// the time up to which the count reads. The runner's Usage reads the second.
const (
	CountStoppedAt = "stopped_at"
	CountUntil     = "until"
)

// keptGrace is how long past the end of a session whose agent was left
// running (a group with keep) the agent's work still counts for the session:
// the call that moved the bead is recorded after the move, and an agent
// finishes the turn it is in with a last answer (the last token line of a
// review was written 67 ms after the yard saw its bead move). What a person
// does in the kept pane after that is not the bead's, however late the count
// is read.
const keptGrace = 2 * time.Minute

// usageWait is how long a stopped session's agent is given to write the
// total it counts itself (Claude Code writes its cost-state as the process
// exits, and a killed process never does) before the count is read from the
// lines it wrote along the way, which fall short of the session.
const usageWait = 10 * time.Minute

// ErrUsageNotYet is what a UsageReader answers while the first reading of a
// kind finds no line: the agent may not have written its total yet. The
// dispatcher asks again on a later pass, and after usageWait allows the
// readings that fall short.
var ErrUsageNotYet = errors.New("the session file holds no usage line yet")

// UsageReader is implemented by runners that read what a stopped session's
// agent used, from the agent's own session file by the id the dispatcher kept
// on the handle (AgentSession), without asking the agent's runtime anything:
// it no longer knows an agent whose tab is closed. A runner that starts no
// agent (manual, exec) does not implement it, and so its sessions are never
// counted.
//
// u is the count, partial when the reading falls short of what the session
// used. missing is why there is no count and none will come ("no session id",
// "no file", …), which the record keeps. err is for what to try again on a
// later pass: ErrUsageNotYet while fallback is false and the first reading
// finds no line, or the file could not be read. fallback allows the readings
// after the first.
type UsageReader interface {
	Usage(ctx context.Context, handle map[string]any, fallback bool) (u agents.Usage, partial bool, missing string, err error)
}

// countable reports whether the ended session s has an agent whose count is to
// be read: its runner reads one (UsageReader) and started something (a
// handle), as Stop is only called for a session that has. A session of a
// runner that starts no agent is never due a count, so it logs no
// session_usage and is never among those the report says it could not count.
func countable(r Runner, s store.Session) bool {
	_, ok := r.(UsageReader)
	return ok && len(s.Handle) > 0
}

// lastAgentSession is the id of the agent of the ended session s, asked of
// its runner once more before Stop closes the agent's tab: a session that
// ended before a pass recorded its id (agentSession) has no other chance,
// since the runtime knows no agent after. "" when the handle has the id
// already, when the runner names none, or when s is not counted.
func (d *Dispatcher) lastAgentSession(ctx context.Context, r Runner, s store.Session) string {
	ider, ok := r.(SessionIDer)
	if !ok || !countable(r, s) || s.Handle[AgentSession] != nil {
		return ""
	}
	id, err := ider.SessionID(ctx, s.Handle)
	if err != nil {
		return ""
	}
	return id
}

// markCountDue marks the count of s, a session whose agent was just stopped,
// as due, in the one write that also keeps id, the session id lastAgentSession
// found. It is written whether or not the handle has an id, so a session whose
// runtime never named one ends with a count that says so (usage). It is written
// after Stop and not before it: a Stop that fails is tried again on a later
// pass, and the wait for the agent's total is counted from the stop that
// happened.
//
// For a group that keeps its agents running, until fixes the time the count
// reads up to, from the session's own end and not from the clock, so a pass
// that is run again after a daemon died halfway writes the same cutoff.
func (d *Dispatcher) markCountDue(ctx context.Context, r Runner, s store.Session, g store.Group, id string) error {
	if !countable(r, s) {
		return nil
	}
	now := d.now()
	due := map[string]any{CountStoppedAt: now.UTC().Format(time.RFC3339Nano)}
	if g.Config["keep"] == true {
		ended := now
		if s.EndedAt != nil {
			ended = *s.EndedAt
		}
		due[CountUntil] = ended.Add(keptGrace).UTC().Format(time.RFC3339Nano)
	}
	h := withHandle(s.Handle, store.CountDue, due)
	if id != "" {
		h[AgentSession] = id
	}
	return d.Store.SetHandle(ctx, s.ID, h)
}

// usage ends the count of every stopped session that is due one with one
// result, recorded by the store in the write that finishes the count
// (store.RecordUsage): what the agent used, or why there is no count. It runs
// before teardown, so a session stopped in one pass is first read in the next:
// its agent has had that long to write its last lines.
func (d *Dispatcher) usage(ctx context.Context, groups map[string]store.Group) error {
	due, err := d.Store.Uncounted(ctx)
	if err != nil {
		return err
	}
	for _, s := range due {
		r, ok := d.runnerFor(s, groups).(UsageReader)
		if !ok {
			continue // a runner that is gone leaves the session as it is, as reap does
		}
		d.readUsage(ctx, r, s)
	}
	return nil
}

// readUsage reads the count of the stopped session s and records it. It waits,
// on the dispatcher's clock, for the cutoff of a kept session to pass (lines
// up to it may still be coming) and for the agent's own total to be written,
// and records the result on the pass that has one; an unreadable file is tried
// again on the next.
func (d *Dispatcher) readUsage(ctx context.Context, r UsageReader, s store.Session) {
	due, _ := s.Handle[store.CountDue].(map[string]any)
	stopped, _ := handleTime(due, CountStoppedAt)
	until, kept := handleTime(due, CountUntil)
	now := d.now()
	if kept && now.Before(until) {
		d.lookAt(until)
		return
	}
	fallbackAt := stopped.Add(usageWait)
	got, partial, missing, err := r.Usage(ctx, s.Handle, !now.Before(fallbackAt))
	said := "usage:" + s.ID
	switch {
	case errors.Is(err, ErrUsageNotYet):
		if now.Before(fallbackAt) {
			d.lookAt(fallbackAt)
		}
		return
	case err != nil:
		// Say it once per session and cause, not on every pass.
		if msg := err.Error(); d.unknown[said] != msg {
			d.unknown[said] = msg
			d.Log.Warn("session usage unread", "session", s.ID, "bead", s.Bead, "err", err)
		}
		return
	}
	delete(d.unknown, said)
	rec := map[string]any{"read_at": now.UTC().Format(time.RFC3339)}
	if missing != "" {
		rec["missing"] = missing
	} else {
		rec["input"], rec["fresh_input"] = got.Input, got.FreshInput
		rec["output"], rec["cache_read"], rec["cache_write"] = got.Output, got.CacheRead, got.CacheWrite
		// Only what is true is said: a count that falls short of the session,
		// and one that stops at the cutoff of an agent left running.
		if partial {
			rec["partial"] = true
		}
		if kept {
			rec["kept"] = true
		}
	}
	if err := d.Store.RecordUsage(ctx, s.ID, rec); err != nil {
		d.Log.Warn("record session usage", "session", s.ID, "bead", s.Bead, "err", err)
	}
}

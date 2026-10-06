package dispatch

import (
	"context"
	"fmt"
	"maps"
	"time"

	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
)

// Group config for how long a bead session may sit idle or run at all
// before the mayor hears of it. A stuck agent otherwise holds its bead and
// its group's slot forever without anyone noticing.
//
//	idle     how long the agent may sit idle (herdr status idle or done)
//	         before a session_idle event, which carries the end of its
//	         pane as `tail`; default 30m. An agent whose harness stopped
//	         on an error is told apart sooner, at the start of the spell
//	         and whatever idle is set to (session_harness_error, see
//	         harness.go). An agent whose
//	         bead has a gate running (yardr rig check) is not idle but
//	         waiting for it: the count starts when the gate has ended
//	max_age  how long a session may run before a session_aged event;
//	         default 4h
//	grace    when set, a session still idle (or still running) this long
//	         after its event is stopped and counted as failed. Unset, an
//	         idle session is only reported (an agent waiting on a human is
//	         idle too), while a session that has run AgeCapFactor times
//	         max_age is stopped anyway, since nothing a bead needs takes
//	         that long, and the bead is released with a note saying so
//	stall    how long an agent may sit idle after its first prompt, having
//	         done nothing, before a session_stalled event; default 2m (see
//	         stall.go)
//
// "off" or "0" turns a limit off. The defaults apply to runners that report
// an agent's status (herdr); for others (exec, manual) a limit applies only
// when the group sets it, and idle never does, since they cannot tell.
const (
	DefaultIdleLimit = 30 * time.Minute
	DefaultAgeLimit  = 4 * time.Hour
	// AgeCapFactor times max_age is the hard cap on a session's age when
	// the group sets no grace.
	AgeCapFactor = 2
)

// AgedAt is a handle key: when session_aged was raised for the session. It
// is kept on the handle, not in memory, so a daemon that replaces itself
// does not tell the mayor again.
const AgedAt = "aged_at"

// limits applies g's idle and age limits to the live session s, raising
// session_idle and session_aged once each and stopping the session after
// the grace period, or at the age cap when there is none. It reports
// whether s is still live.
func (d *Dispatcher) limits(ctx context.Context, r Runner, s *store.Session, g store.Group) bool {
	if s.State != "running" {
		return true
	}
	_, agent := r.(Statuser)
	idle, idleOn := limit(g.Config, "idle", DefaultIdleLimit, agent)
	age, ageOn := limit(g.Config, "max_age", DefaultAgeLimit, agent)
	grace, stops := limit(g.Config, "grace", 0, false)
	now := time.Now()

	// Before the limits, so the marks these leave on the handle are not lost
	// to the one session_aged leaves below.
	d.model(ctx, r, s, g)
	switch st := d.status[s.ID]; st {
	case "idle", "done":
		d.stall(ctx, r, s, g)
	case "unknown":
		// herdr cannot tell: neither idle nor going.
	default:
		delete(d.fresh, s.ID) // it got going
	}

	if agent {
		st := d.status[s.ID]
		idleNow := st == "idle" || st == "done"
		if _, gating := d.gateOf(s.Bead); idleNow && gating {
			// It waits for its gate, which takes longer than a builders'
			// limit: nothing to tell the mayor, and nothing to stop. The
			// spell is forgotten, so the count starts again at the first pass
			// that finds the gate gone.
			delete(d.idleSince, s.ID)
			delete(d.idleTold, s.ID)
		} else if idleNow {
			since, ok := d.idleSince[s.ID]
			if !ok {
				// Counted from the first pass that saw it idle; a restarted
				// daemon starts the count again.
				since = now
				d.idleSince[s.ID] = now
			}
			idleFor := now.Sub(since)
			tell := idleOn && idleFor >= idle && !d.idleTold[s.ID]
			// The pane says why an agent is idle, where a status cannot: it
			// is read while the spell is young, for a harness that stopped
			// on an error (harness.go), and once more for session_idle.
			tail, read := "", false
			if tell || (idleFor <= harnessLook && s.Handle[HarnessErrorKey] == nil) {
				if tail, read = d.paneTail(ctx, r, *s); read {
					d.harnessError(ctx, s, g, tail)
				}
			}
			if tell {
				d.idleTold[s.ID] = true
				d.Log.Warn("session idle", "session", s.ID, "bead", s.Bead, "idle", round(idleFor))
				extra := map[string]any{}
				if read {
					extra["tail"] = tail
				}
				if look := s.Handle[PromptLook]; look != nil {
					extra[PromptLook] = look
				}
				d.limitEvent(ctx, *s, "session_idle", "idle", idleFor, idle, grace, stops, extra)
			}
			if idleOn && stops && idleFor >= idle+grace {
				d.end(ctx, *s, "failed", fmt.Sprintf("idle for %s (limit %s, grace %s)",
					round(idleFor), idle, grace))
				return false
			}
		} else {
			delete(d.idleSince, s.ID)
			delete(d.idleTold, s.ID)
			d.harnessOver(ctx, s)
		}
	}

	if ageOn {
		ranFor := now.Sub(s.StartedAt)
		hardCap := AgeCapFactor * age
		if ranFor >= age && s.Handle[AgedAt] == nil {
			d.Log.Warn("session aged", "session", s.ID, "bead", s.Bead, "age", round(ranFor))
			// An aged session is always stopped eventually: after the
			// grace, else at the cap. The event says when.
			stopAfter := grace
			if !stops {
				stopAfter = hardCap - age
			}
			d.limitEvent(ctx, *s, "session_aged", "age", ranFor, age, stopAfter, true, nil)
			h := maps.Clone(s.Handle)
			if h == nil {
				h = map[string]any{}
			}
			h[AgedAt] = now.UTC().Format(time.RFC3339)
			if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
				d.Log.Warn("mark session aged", "session", s.ID, "err", err)
			}
		}
		if stops && ranFor >= age+grace {
			d.end(ctx, *s, "failed", fmt.Sprintf("ran for %s (limit %s, grace %s)",
				round(ranFor), age, grace))
			return false
		}
		if !stops && ranFor >= hardCap {
			// No grace to say when a long run becomes a stuck one, so a
			// generous default does: the note tells the next agent (or the
			// mayor, reading the bead) why its predecessor stopped.
			reason := fmt.Sprintf("ran for %s, past the hard cap of %s (%d x max_age %s, no grace set)",
				round(ranFor), hardCap, AgeCapFactor, age)
			d.end(ctx, *s, "failed", reason)
			note := fmt.Sprintf("Session %s stopped by the yard: %s. The bead is released for another attempt; "+
				"if the work needs longer, raise the group's max_age.", s.ID, reason)
			if err := d.Store.AddNote(ctx, s.Bead, note, actor); err != nil {
				d.Log.Warn("note on capped session", "session", s.ID, "bead", s.Bead, "err", err)
			}
			return false
		}
	}
	return true
}

// gateOf is the gate running for bead, if one is: a yardr rig check in its
// worktree, as the yard's home records it (rigcheck.Gates). The home is read
// once a pass, and only when a session is idle. One that cannot be read has
// no gates: the idle watch then goes on as it did without them.
func (d *Dispatcher) gateOf(bead string) (rigcheck.Gate, bool) {
	if !d.gatingRead {
		d.gatingRead = true
		gates, err := rigcheck.Gates(d.Home)
		if err != nil {
			d.Log.Warn("read the running gates", "home", d.Home, "err", err)
		}
		d.gating = gates
	}
	return rigcheck.GateOf(d.gating, bead)
}

// limitEvent logs a limit's event; extra is what this one carries beside
// what every limit's does (session_idle: the end of the agent's pane).
func (d *Dispatcher) limitEvent(ctx context.Context, s store.Session, kind, key string, got, lim, grace time.Duration, stops bool, extra map[string]any) {
	data := map[string]any{"session": s.ID, "group": s.Group, "stage": s.Stage,
		key: round(got).String(), "limit": lim.String()}
	maps.Copy(data, extra)
	if stops {
		data["stop_after"] = grace.String()
	}
	if err := d.Store.LogDispatch(ctx, s.Bead, kind, actor, data); err != nil {
		d.Log.Warn(kind+" event", "session", s.ID, "err", err)
	}
}

// limit reads a duration ("30m", or a number of seconds) from group config.
// Unset or unreadable, it is def, on when byDefault. "off" turns it off, and
// so does 0, except for grace, where 0 means stop at once.
func limit(cfg map[string]any, key string, def time.Duration, byDefault bool) (time.Duration, bool) {
	var dur time.Duration
	switch v := cfg[key].(type) {
	case string:
		if v == "off" {
			return 0, false
		}
		if v == "0" {
			break
		}
		parsed, err := time.ParseDuration(v)
		if err != nil || parsed < 0 {
			return def, byDefault
		}
		dur = parsed
	case float64:
		if v < 0 {
			return def, byDefault
		}
		dur = time.Duration(v * float64(time.Second))
	default:
		return def, byDefault
	}
	if dur == 0 && key != "grace" {
		return 0, false
	}
	return dur, true
}

// round keeps a duration readable in events and logs.
func round(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(time.Second)
}

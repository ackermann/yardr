package dispatch

import (
	"context"
	"fmt"
	"maps"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Group config for how long a bead session may sit idle or run at all
// before the mayor hears of it. A stuck agent otherwise holds its bead and
// its group's slot forever without anyone noticing.
//
//	idle     how long the agent may sit idle (herdr status idle or done)
//	         before a session_idle event; default 30m
//	max_age  how long a session may run before a session_aged event;
//	         default 4h
//	grace    when set, a session still idle (or still running) this long
//	         after its event is stopped and counted as failed; unset, it is
//	         only reported
//
// "off" or "0" turns a limit off. The defaults apply to runners that report
// an agent's status (herdr); for others (exec, manual) a limit applies only
// when the group sets it, and idle never does, since they cannot tell.
const (
	DefaultIdleLimit = 30 * time.Minute
	DefaultAgeLimit  = 4 * time.Hour
)

// AgedAt is a handle key: when session_aged was raised for the session. It
// is kept on the handle, not in memory, so a daemon that replaces itself
// does not tell the mayor again.
const AgedAt = "aged_at"

// limits applies g's idle and age limits to the live session s, raising
// session_idle and session_aged once each and stopping the session after
// the grace period. It reports whether s is still live.
func (d *Dispatcher) limits(ctx context.Context, r Runner, s store.Session, g store.Group) bool {
	if s.State != "running" {
		return true
	}
	_, agent := r.(Statuser)
	idle, idleOn := limit(g.Config, "idle", DefaultIdleLimit, agent)
	age, ageOn := limit(g.Config, "max_age", DefaultAgeLimit, agent)
	grace, stops := limit(g.Config, "grace", 0, false)
	now := time.Now()

	if idleOn && agent {
		if st := d.status[s.ID]; st == "idle" || st == "done" {
			since, ok := d.idleSince[s.ID]
			if !ok {
				// Counted from the first pass that saw it idle; a restarted
				// daemon starts the count again.
				since = now
				d.idleSince[s.ID] = now
			}
			idleFor := now.Sub(since)
			if idleFor >= idle && !d.idleTold[s.ID] {
				d.idleTold[s.ID] = true
				d.Log.Warn("session idle", "session", s.ID, "bead", s.Bead, "idle", round(idleFor))
				d.limitEvent(ctx, s, "session_idle", "idle", idleFor, idle, grace, stops)
			}
			if stops && idleFor >= idle+grace {
				d.end(ctx, s, "failed", fmt.Sprintf("idle for %s (limit %s, grace %s)",
					round(idleFor), idle, grace))
				return false
			}
		} else {
			delete(d.idleSince, s.ID)
			delete(d.idleTold, s.ID)
		}
	}

	if ageOn {
		ranFor := now.Sub(s.StartedAt)
		if ranFor >= age && s.Handle[AgedAt] == nil {
			d.Log.Warn("session aged", "session", s.ID, "bead", s.Bead, "age", round(ranFor))
			d.limitEvent(ctx, s, "session_aged", "age", ranFor, age, grace, stops)
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
			d.end(ctx, s, "failed", fmt.Sprintf("ran for %s (limit %s, grace %s)",
				round(ranFor), age, grace))
			return false
		}
	}
	return true
}

func (d *Dispatcher) limitEvent(ctx context.Context, s store.Session, kind, key string, got, lim, grace time.Duration, stops bool) {
	data := map[string]any{"session": s.ID, "group": s.Group, "stage": s.Stage,
		key: round(got).String(), "limit": lim.String()}
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

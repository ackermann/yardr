package dispatch

import (
	"context"
	"fmt"
	"maps"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Group config for how long a bead session may sit idle or run at all
// before the yardmaster hears of it. A stuck agent otherwise holds its bead and
// its group's slot forever without anyone noticing.
//
//	idle     how long the agent may sit idle (herdr status idle or done)
//	         before a session_idle event, which carries the end of its
//	         pane as `tail`; default 30m. An agent whose harness stopped
//	         on an error is told apart sooner, at the start of the spell
//	         and whatever idle is set to (session_harness_error, see
//	         harness.go, which also says when yardr prompts it again
//	         itself). An agent that waits for the window of a usage
//	         limit to reset is not idle, until yardr has looked at it
//	         then. Neither is one whose pane says, at the limit, that
//	         work it started in the background still runs (a harness's
//	         `background` lines, harness.go): the spell starts again
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
//
// A peer group's session is a bead at another yard (Waiter): its max_age
// is 24h unless set, session_aged is raised at it once, and neither grace
// nor the cap stops it.
const (
	DefaultIdleLimit = 30 * time.Minute
	DefaultAgeLimit  = 4 * time.Hour
	// AgeCapFactor times max_age is the hard cap on a session's age when
	// the group sets no grace.
	AgeCapFactor = 2
)

// AgedAt is a handle key: when session_aged was raised for the session. It
// is kept on the handle, not in memory, so a daemon that replaces itself
// does not tell the yardmaster again.
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
	// A session that waits for another yard is told of at its age and
	// never stopped for it, grace or none.
	waiter, waits := r.(Waiter)
	if waits {
		age, ageOn = limit(g.Config, "max_age", waiter.WaitLimit(), true)
		stops = false
	}
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
		if due, waits := handleTime(s.Handle, harnessResetAt); idleNow && waits {
			// It waits for the window of its usage limit to reset, which the
			// yardmaster was told of with the time, and yardr prompts it then
			// (harness_reset.go): not an idle spell, and nothing to stop
			// before the window opens.
			delete(d.idleSince, s.ID)
			delete(d.idleTold, s.ID)
			delete(d.idleNudged, s.ID)
			if d.now().Before(due) {
				d.lookAt(due)
			} else {
				d.harnessReset(ctx, r, s, g)
			}
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
			// on an error (harness.go), when a try for one is due, and once
			// more for session_idle.
			tail, read := "", false
			if d.harnessLooks(s, idleFor) || tell {
				if tail, read = d.paneTail(ctx, r, *s); read {
					d.harnessError(ctx, r, s, g, tail)
				}
			}
			if _, busy := HarnessBackground(harnessKind(g.Config), tail); tell && read && busy {
				// It waits for a shell of its own, and its harness wakes it
				// when that exits (yardr-zjhw: a builder whose depot test ran
				// longer than idle was told of every five minutes). The spell
				// is counted from this look, so the pane is read again one
				// limit on, and a grace does not end it meanwhile.
				d.Log.Info("session idle with background work running: not told", "session", s.ID, "bead", s.Bead,
					"idle", round(idleFor))
				d.idleSince[s.ID] = now
				tell, idleFor = false, 0
			}
			if !d.idleTold[s.ID] && s.Handle[HarnessErrorKey] == nil {
				// Before the idle limit counts: one prompt to hand on,
				// while the session can still act on it itself.
				d.idleNudge(ctx, r, s)
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
			delete(d.idleNudged, s.ID)
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
			var extra map[string]any
			if waits {
				extra = map[string]any{"read": waiter.WaitRead(s.Bead)}
			}
			d.limitEvent(ctx, *s, "session_aged", "age", ranFor, age, stopAfter, !waits, extra)
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
		if !stops && !waits && ranFor >= hardCap {
			// No grace to say when a long run becomes a stuck one, so a
			// generous default does: the note tells the next agent (or the
			// yardmaster, reading the bead) why its predecessor stopped.
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

// idleNudge sends the idle session s one prompt when its bead is still in
// the stage it works and it wrote no note of its own on it: it finished
// its turn without handing on, and sits until told. The nudge goes before
// the idle limit counts, once per idle spell, through the same delivery as
// a note's telling (typed where the pane shows a prompt, through herdr
// where it shows none); the session_idle_nudge event says which way.
//
// A session still in its stall window, one judged stalled, one with a
// first prompt on its way to a pane that shows one, and one that waits for
// a usage limit's reset are not nudged: the first has not had its turn,
// the second cannot act, the third is starting, and the fourth is told at
// its reset. One with a note already due is told of that instead.
func (d *Dispatcher) idleNudge(ctx context.Context, r Runner, s *store.Session) {
	if d.idleNudged[s.ID] {
		return
	}
	if _, fresh := d.fresh[s.ID]; fresh {
		return
	}
	if s.Handle[StalledAt] != nil {
		return
	}
	if _, ok := r.(Nudger); !ok {
		return
	}
	agent, _ := s.Handle["agent"].(string)
	if agent == "" {
		// No agent to name its own notes by: without one the check
		// below cannot tell them from anyone's.
		return
	}
	if s.Handle[store.PendingPrompt] != nil && d.paneHasPrompt(ctx, r, s.Handle) {
		return
	}
	if s.Handle[harnessPrompted] != nil {
		return
	}
	if _, waits := handleTime(s.Handle, harnessResetAt); waits {
		return
	}
	b, err := d.Store.GetBead(ctx, s.Bead)
	if err != nil || b.Stage != s.Stage {
		return
	}
	notes, err := d.Store.Notes(ctx, s.Bead)
	if err != nil {
		return
	}
	for _, n := range notes {
		if n.Notify == s.ID && !n.Told && !n.Acked && n.Untold == "" {
			return
		}
		if n.Author == agent {
			return
		}
	}
	text := fmt.Sprintf("yardr: your bead is still in %s: write your note and hand on, or say what blocks you.", s.Stage)
	told, delivery, err := d.nudgeSession(ctx, r, s, text)
	if err != nil {
		// For good, as a note's telling that fails: give up for this
		// spell, so one bad handle does not log on every pass.
		d.Log.Warn("idle nudge could not be sent", "session", s.ID, "bead", s.Bead, "err", err)
		d.idleNudged[s.ID] = true
		return
	}
	if !told {
		return // held, or herdr down: due on a later pass
	}
	d.idleNudged[s.ID] = true
	d.Log.Info("idle session nudged to hand on", "session", s.ID, "bead", s.Bead, "delivery", delivery)
	data := map[string]any{"session": s.ID, "stage": s.Stage, "delivery": delivery}
	if pane, _ := s.Handle["pane"].(string); pane != "" {
		data["pane"] = pane
	}
	_ = d.Store.LogDispatch(ctx, s.Bead, "session_idle_nudge", actor, data)
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

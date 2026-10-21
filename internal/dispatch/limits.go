package dispatch

import (
	"context"
	"fmt"
	"maps"
	"strings"
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
//	         then. Nor is one whose agent said it waits, not stuck
//	         (`yardr session snooze`): no idle spell is counted while
//	         its snooze lasts. Neither is one whose pane says, at the
//	         limit, that work it started in the background still runs
//	         (a harness's `background` lines, harness.go): the spell
//	         starts again
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
//	still    how long an agent herdr calls working may show a pane whose
//	         end stands unchanged before it counts as idle, whatever it
//	         shows; default 10m (see stillIdle). A harness at work redraws
//	         its screen, so a still pane is not working.
//
// "off" or "0" turns a limit off. The defaults apply to runners that report
// an agent's status (herdr); for others (exec, manual) a limit applies only
// when the group sets it, and idle and still never do, since they cannot tell.
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
	// DefaultStillLimit is how long an agent herdr calls working may show
	// a pane whose end stands unchanged before it counts as idle.
	DefaultStillLimit = 10 * time.Minute
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
		idleAt := d.now()
		idleNow := st == "idle" || st == "done"
		if st != "working" {
			delete(d.still, s.ID)
		} else if d.stillIdle(ctx, r, s, g, idleAt) {
			// Its pane has stood unchanged past the group's still
			// limit: it counts as idle, whatever herdr says, and
			// everything below holds for it.
			if _, ok := d.idleSince[s.ID]; !ok {
				d.Log.Info("session's pane unchanged while working: counted as idle",
					"session", s.ID, "bead", s.Bead)
			}
			idleNow = true
		}
		if due, waits := handleTime(s.Handle, harnessResetAt); idleNow && waits {
			// It waits for the window of its usage limit to reset, which the
			// yardmaster was told of with the time, and yardr prompts it then
			// (harness_reset.go): not an idle spell, and nothing to stop
			// before the window opens.
			delete(d.idleSince, s.ID)
			delete(d.idleTold, s.ID)
			delete(d.idleNudged, s.ID)
			delete(d.idleOutcomeTold, s.ID)
			if d.now().Before(due) {
				d.lookAt(due)
			} else {
				d.harnessReset(ctx, r, s, g)
			}
		} else if due, snoozed := handleTime(s.Handle, snoozeUntil); idleNow && snoozed {
			// Its agent said it waits, not stuck (yardr session snooze):
			// not an idle spell while the snooze lasts, and nothing is
			// told or prodded. When the snooze ends the key goes and the
			// idle count starts fresh from the next pass. The age limit
			// below is not moved by it.
			delete(d.idleSince, s.ID)
			delete(d.idleTold, s.ID)
			delete(d.idleNudged, s.ID)
			delete(d.idleOutcomeTold, s.ID)
			if d.now().Before(due) {
				d.lookAt(due)
			} else {
				d.unsnooze(ctx, s)
			}
		} else if idleNow {
			since, ok := d.idleSince[s.ID]
			if !ok {
				// Counted from the first pass that saw it idle; a restarted
				// daemon starts the count again.
				since = idleAt
				d.idleSince[s.ID] = idleAt
			}
			idleFor := idleAt.Sub(since)
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
				d.idleSince[s.ID] = idleAt
				tell, idleFor = false, 0
			}
			if !d.idleTold[s.ID] && s.Handle[HarnessErrorKey] == nil {
				// Before the idle limit counts: one prompt to hand on,
				// while the session can still act on it itself.
				d.idleNudge(ctx, r, s, g, tail, read)
			}
			if tell && s.Handle[HarnessErrorKey] == nil && d.outcomeIdle(ctx, r, s) {
				// Its last note is its own done note: the pane has the
				// outcomes now, and session_idle waits for a later pass.
				// A grace does not end it meanwhile: the line is its last
				// chance before the yardmaster hears.
				tell, idleFor = false, 0
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
			delete(d.idleOutcomeTold, s.ID)
			d.harnessOver(ctx, s)
			if st == "working" {
				d.snoozeOver(ctx, s)
			}
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

// unsnooze ends the snooze of s, whose time is up: the key goes, and the
// idle count starts fresh from the next pass. The count of consecutive
// snoozes stays: only a pane that changed breaks the run (snoozeOver).
func (d *Dispatcher) unsnooze(ctx context.Context, s *store.Session) {
	h := maps.Clone(s.Handle)
	if h == nil {
		h = map[string]any{}
	}
	delete(h, snoozeUntil)
	if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
		d.Log.Warn("end a session's snooze", "session", s.ID, "err", err)
		return
	}
	s.Handle = h
}

// snoozeOver ends the snooze of s, whose agent is working again: the wait
// is over, and the count of consecutive snoozes starts again. A harness
// at work redraws its screen, so a session seen working is one whose pane
// changed, which is what breaks the run; a pane that stands while herdr
// says working (stillIdle) keeps it, as do the statuses that say nothing
// either way.
func (d *Dispatcher) snoozeOver(ctx context.Context, s *store.Session) {
	if s.Handle[snoozeUntil] == nil && s.Handle[snoozeCount] == nil {
		return
	}
	h := maps.Clone(s.Handle)
	if h == nil {
		h = map[string]any{}
	}
	delete(h, snoozeUntil)
	delete(h, snoozeCount)
	if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
		d.Log.Warn("end a session's snooze", "session", s.ID, "err", err)
		return
	}
	s.Handle = h
}

// stillPane is the end of a working agent's pane as last seen, and when it
// was first seen as it is.
type stillPane struct {
	tail  string
	since time.Time
}

// stillIdle reports whether s, which herdr calls working, counts as idle:
// the end of its pane has stood unchanged for the group's still limit
// (default 10m; "off" or "0" turns it off). A harness at work redraws its
// screen (a spinner, the seconds a turn has taken, streamed text), so a
// pane that stands is not working: its harness stopped on something no
// error line knows, and without this neither session_harness_error nor
// session_idle would ever fire for it. Only bead sessions are judged, and
// only runners with a pane to read; what is remembered is this process's,
// and a daemon that replaced itself counts again.
func (d *Dispatcher) stillIdle(ctx context.Context, r Runner, s *store.Session, g store.Group, now time.Time) bool {
	lim, on := limit(g.Config, "still", DefaultStillLimit, true)
	t, ok := r.(Tailer)
	if !on || !ok {
		delete(d.still, s.ID)
		return false
	}
	tail, err := t.Tail(ctx, s.Handle, stallTail)
	if err != nil {
		delete(d.still, s.ID)
		return false // a pane that cannot be read says nothing either way
	}
	if was, ok := d.still[s.ID]; ok && was.tail == tail {
		return now.Sub(was.since) >= lim
	}
	if d.still == nil {
		d.still = map[string]stillPane{}
	}
	d.still[s.ID] = stillPane{tail: tail, since: now}
	return false
}

// idleNudge sends the idle session s one prompt when its bead is still in
// the stage it works: it finished its turn without handing on, and sits
// until told. The nudge goes before the idle limit counts, once per idle
// spell, through the same delivery as a note's telling (typed where the
// pane shows a prompt, through herdr where it shows none); the
// session_idle_nudge event says which way.
//
// One with no note of its own is asked for the note and the handover. One
// with a note of its own gets none here: without a test line it is still
// at work, and with one its outcome line goes at the idle limit, as a last
// chance before session_idle (see outcomeIdle).
//
// A session still in its stall window, one judged stalled, one with a
// first prompt still unsubmitted, and one that waits for a usage limit's
// reset are not nudged: the first has not had its turn, the second cannot
// act, the third is starting, and the fourth is told at its reset. One
// with a note already due is told of that instead. One whose pane says
// that work it started in the background still runs is not nudged
// either: its harness wakes it when that exits. The pane is looked at for
// that only when a nudge would otherwise be sent, reusing the tail the
// pass read already where there is one.
//
// tail and read are that tail: the end of the pane as read for the
// spell's other looks on this pass, and whether one was read.
func (d *Dispatcher) idleNudge(ctx context.Context, r Runner, s *store.Session, g store.Group, tail string, read bool) {
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
	if s.Handle[store.PendingPrompt] != nil {
		// The first prompt goes first, whatever the pane shows: a nudge
		// typed while it is still unsubmitted sits in the same input,
		// also unsubmitted, and holds the prompt back (yardr-dfgj).
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
		// Herdr names an agent for its bead and stage, so a retry can have
		// the same author as its predecessor. Only notes written after this
		// session started are this session's own.
		if n.Author == agent && n.At.After(s.StartedAt) {
			return
		}
	}
	if !read {
		// The nudge is due: only now is the pane read, so a pass with
		// nothing to send reads nothing new.
		tail, read = d.paneTail(ctx, r, *s)
	}
	if _, busy := HarnessBackground(harnessKind(g.Config), tail); read && busy {
		// It waits for a shell of its own, and its harness wakes it when
		// that exits (yardr-kh12: a builder whose depot test ran in the
		// background answered every nudge, and each answer started a new
		// idle spell). No nudge, and none marked: once the shell is gone
		// the nudge goes as ever.
		return
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

// outcomeIdle sends the idle session s one line with the outcomes its stage
// offers and their commands, when its bead's last note is its own done note:
// it wrote everything but the outcome and stopped. The line goes at the idle
// limit instead of session_idle, once per idle spell; only if the session
// stays idle after it does session_idle reach the inbox on a later pass. It
// reports whether the line was sent, and session_idle is suppressed for it.
//
// The commands are the brief's: the outcomes come from the same NextStages
// the brief's list is drawn from, and each command is store.DoneCommand, so
// the pane names what the brief named. A session whose last note is someone
// else's, whose own last note carries no test line, or that cannot act gets
// none, and session_idle goes as today.
func (d *Dispatcher) outcomeIdle(ctx context.Context, r Runner, s *store.Session) bool {
	if d.idleOutcomeTold[s.ID] {
		return false
	}
	if _, fresh := d.fresh[s.ID]; fresh {
		return false
	}
	if s.Handle[StalledAt] != nil {
		return false
	}
	if _, ok := r.(Nudger); !ok {
		return false
	}
	agent, _ := s.Handle["agent"].(string)
	if agent == "" {
		return false
	}
	if s.Handle[store.PendingPrompt] != nil {
		return false
	}
	if s.Handle[harnessPrompted] != nil {
		return false
	}
	if _, waits := handleTime(s.Handle, harnessResetAt); waits {
		return false
	}
	b, err := d.Store.GetBead(ctx, s.Bead)
	if err != nil || b.Stage != s.Stage {
		return false
	}
	notes, err := d.Store.Notes(ctx, s.Bead)
	if err != nil || len(notes) == 0 {
		return false
	}
	for _, n := range notes {
		if n.Notify == s.ID && !n.Told && !n.Acked && n.Untold == "" {
			return false
		}
	}
	last := notes[len(notes)-1]
	if last.Author != agent || !last.At.After(s.StartedAt) || !hasTestLine(last.Text) {
		return false
	}
	text := d.outcomeLine(ctx, s.Bead)
	told, delivery, err := d.nudgeSession(ctx, r, s, text)
	if err != nil {
		d.Log.Warn("idle outcome line could not be sent", "session", s.ID, "bead", s.Bead, "err", err)
		d.idleNudged[s.ID] = true
		return false
	}
	if !told {
		return false
	}
	d.idleNudged[s.ID] = true
	d.idleOutcomeTold[s.ID] = true
	d.Log.Info("idle session sent its outcomes before session_idle", "session", s.ID, "bead", s.Bead, "delivery", delivery)
	data := map[string]any{"session": s.ID, "stage": s.Stage, "delivery": delivery}
	if pane, _ := s.Handle["pane"].(string); pane != "" {
		data["pane"] = pane
	}
	_ = d.Store.LogDispatch(ctx, s.Bead, "session_idle_nudge", actor, data)
	return true
}

// outcomeLine is the one line an idle session whose last note is its own
// done note is sent: the outcomes its stage offers, each with the command
// that reports it, as the brief lists them. Without a flow, or a stage
// with no outcomes, it names done alone.
func (d *Dispatcher) outcomeLine(ctx context.Context, bead string) string {
	if ns, err := d.Store.NextStages(ctx, bead); err == nil && len(ns.Outcomes) > 0 {
		parts := make([]string, 0, len(ns.Outcomes))
		for _, o := range ns.Outcomes {
			parts = append(parts, fmt.Sprintf("`%s` with `%s`", o.Name, store.DoneCommand(bead, o.Name)))
		}
		return fmt.Sprintf("yardr: your note is on the bead; report the outcome: %s.", strings.Join(parts, ", "))
	}
	return fmt.Sprintf("yardr: your note is on the bead; report the outcome: `%s`.", store.DoneCommand(bead, store.DefaultOutcome))
}

// hasTestLine reports whether the note carries a test line: a line of its
// own starting with "test:", as a builder's finish note ends with. By line,
// not by Contains, which would also match "latest:".
func hasTestLine(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "test:") {
			return true
		}
	}
	return false
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

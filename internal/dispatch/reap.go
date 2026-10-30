package dispatch

import (
	"context"
	"sync"
	"time"

	"yardr.dev/yardr/internal/store"
)

// reapAskBound is how many sessions reap judges at once: enough that
// one slow runner call no longer stacks behind every session before
// it, few enough that a sick runtime is not asked by the whole yard at
// once. It is fixed, not a setting: the bound is part of how a pass
// behaves, as the per-call bounds it sits beside are.
const reapAskBound = 8

// reapOutcome is what one worker judged of one running session: the
// runner's answers and the prompt pass's result, with the time of each
// runner ask, and nothing applied. The pass applies outcomes in
// session order (see reap), so everything the serial loop used to do
// with the dispatcher's maps, the store and the session's later steps
// happens there, in the order it always did.
type reapOutcome struct {
	status string // the runner's status; "" with no error is a gone agent
	err    error  // the judging ask failed: liveness is unknown
	alive  bool   // the runner reports the work still running

	// The pending prompt's pass, where the session had one: its result,
	// whether it answered the session's trust dialog, and the prompt
	// memory it kept (see reapClone). hasPass is false where the
	// session had no prompt pending: ownInput's effect is kept for the
	// apply, with the pass's memory as it always has.
	hasPass bool
	pass    promptPass
	trusted bool
	mem     reapMemory
	own     *ownInputEffect

	// tail is the end of the pane, read in the worker where the status
	// newly turned blocked (on the pass's status snapshot), for the
	// session_blocked the apply logs if the transition stands.
	tail     string
	tailRead bool

	calls []callTime // each runner ask's kind and time, in the order made
}

// reapMemory is what one session's judging kept in the
// dispatcher's memory, as a reap outcome carries it from its worker to
// the apply: each entry's final word on the session's key, present or
// gone, and when the pass next wants looking at for it.
type reapMemory struct {
	promptIdle    time.Time
	hasPromptIdle bool
	draft         *draft
	hasDraft      bool
	dialog        string
	hasDialog     bool
	login         string
	hasLogin      bool
	nudge         ownNudge
	hasNudge      bool
	unsent        *unsent
	hasUnsent     bool
	lookDue       *time.Time
}

// reapClone is a Dispatcher carrying only the per-session memory the
// worker's prompt and own-input checks read: their results come back in
// the outcome, so the pass's maps and store are written by no worker. The
// clone shares the dispatcher's clock, read through d.now, so a test that
// set the clock decides the clone's time too. What neither check reads (the
// store, the start workers, every other map) is not carried, and defaults is
// never called on a clone.
func (d *Dispatcher) reapClone(key string) *Dispatcher {
	c := &Dispatcher{
		Log:          d.Log,
		Interval:     d.Interval,
		PromptSettle: d.PromptSettle,
		PromptWait:   d.PromptWait,
		DraftWait:    d.DraftWait,
		promptIdle:   map[string]time.Time{},
		drafts:       map[string]*draft{},
		dialogs:      map[string]string{},
		logins:       map[string]string{},
		nudged:       map[string]ownNudge{},
		unsent:       map[string]*unsent{},
	}
	now := d.now
	c.clock.Store(&now)
	if since, ok := d.promptIdle[key]; ok {
		c.promptIdle[key] = since
	}
	if dr := d.drafts[key]; dr != nil {
		cp := *dr
		c.drafts[key] = &cp
	}
	if dialog, ok := d.dialogs[key]; ok {
		c.dialogs[key] = dialog
	}
	if line, ok := d.logins[key]; ok {
		c.logins[key] = line
	}
	if nudge, ok := d.nudged[key]; ok {
		c.nudged[key] = nudge
	}
	if u := d.unsent[key]; u != nil {
		copy := *u
		c.unsent[key] = &copy
	}
	return c
}

// reapMemoryFor is the clone's final word on key, for its outcome.
func (c *Dispatcher) reapMemoryFor(key string) reapMemory {
	var m reapMemory
	m.promptIdle, m.hasPromptIdle = c.promptIdle[key]
	m.draft, m.hasDraft = c.drafts[key]
	m.dialog, m.hasDialog = c.dialogs[key]
	m.login, m.hasLogin = c.logins[key]
	m.nudge, m.hasNudge = c.nudged[key]
	m.unsent, m.hasUnsent = c.unsent[key]
	m.lookDue = c.lookDue
	return m
}

// applyReapMemory makes m, one session's memory as its worker
// left it, the pass's own, in the apply: each entry is kept or dropped
// as the worker left it, and a look the check asked for is asked of the
// pass.
func (d *Dispatcher) applyReapMemory(key string, m reapMemory) {
	if m.hasPromptIdle {
		d.promptIdle[key] = m.promptIdle
	} else {
		delete(d.promptIdle, key)
	}
	if m.hasDraft {
		d.drafts[key] = m.draft
	} else {
		delete(d.drafts, key)
	}
	if m.hasDialog {
		d.dialogs[key] = m.dialog
	} else {
		delete(d.dialogs, key)
	}
	if m.hasLogin {
		d.logins[key] = m.login
	} else {
		delete(d.logins, key)
	}
	if m.hasNudge {
		d.nudged[key] = m.nudge
	} else {
		delete(d.nudged, key)
	}
	if m.hasUnsent {
		d.unsent[key] = m.unsent
	} else {
		delete(d.unsent, key)
	}
	if m.lookDue != nil {
		d.lookAt(*m.lookDue)
	}
}

// reap ends sessions whose runner reports them gone and returns the live
// session count per group.
//
// Running sessions are judged side by side (judgeSession, at most
// reapAskBound at once): one slow runner call then costs the pass about
// that call, not itself stacked behind every session before it. The
// judging only asks: no map of the pass's, no store write and none of
// limits, holdBack, tellNotes or end runs in a worker. The outcomes are
// applied below in the sessions' own order, exactly as the serial loop
// applied them, and starting sessions and runner-less ones keep their
// serial path there untouched: they make no runner ask.
func (d *Dispatcher) reap(ctx context.Context, groups map[string]store.Group) (map[string]int, error) {
	sessions, err := d.Store.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	active := map[string]int{}
	d.paused = map[string]map[string]string{}
	d.running = 0
	// live counts s for its group, and for the yard when it runs here.
	live := func(s store.Session) {
		active[s.Group]++
		name := runnerName(s, groups)
		if store.RunsHere(name) {
			d.running++
		}
		if name == "herdr" {
			d.noteHome(beadHome(s, name))
		}
	}
	// unknown counts s live and says why its liveness is unknown, once
	// per session and cause, not on every pass.
	unknown := func(s store.Session, err error) {
		if msg := err.Error(); d.unknown[s.ID] != msg {
			d.unknown[s.ID] = msg
			d.Log.Warn("liveness unknown", "session", s.ID, "err", err)
		}
		live(s)
	}

	outcomes := make([]*reapOutcome, len(sessions))
	sem := make(chan struct{}, reapAskBound)
	var wg sync.WaitGroup
	for i, s := range sessions {
		r := d.runnerFor(s, groups)
		if r == nil || s.State == "starting" {
			continue
		}
		// The status snapshot decides only whether the worker reads the
		// pane for a blocked transition; the apply re-reads the map, and
		// no worker writes it, so the two agree.
		prev, seen := d.status[s.ID]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out := d.judgeSession(ctx, s, r, groups[s.Group].Config, prev, seen)
			outcomes[i] = &out
		}()
	}
	wg.Wait()

	for i, s := range sessions {
		r := d.runnerFor(s, groups)
		out := outcomes[i]
		if out != nil {
			for _, c := range out.calls {
				d.timing.ask(s.ID, c.name, c.took)
			}
		}
		switch {
		case r == nil:
			live(s) // cannot judge; leave it alone
		case s.State == "starting":
			// A start runs on its group's worker now, and may still be
			// running healthily: one a live worker of this daemon owns
			// is counted live at any age, since the longest healthy
			// start sums past five minutes of bounded calls (design
			// section 2.3). The five minutes judge only a session no
			// worker owns, which is exactly the dead-dispatcher case
			// this rule was written for: a daemon restarted over the
			// previous daemon's in-flight start fails it as before,
			// and teardown closes what its handle names.
			if d.startIsOwned(s.ID) {
				live(s)
			} else if d.now().Sub(s.StartedAt) > 5*time.Minute {
				d.end(ctx, s, "failed", "start interrupted")
			} else {
				live(s)
			}
		default:
			if out == nil {
				live(s) // never judged; leave it alone
				continue
			}
			if out.err != nil {
				unknown(s, out.err)
				continue
			}
			alive := out.alive
			if _, ok := r.(Statuser); ok && out.status != "" {
				trusted := false
				if out.hasPass {
					pass := out.pass
					p, _ := s.Handle[store.PendingPrompt].(string)
					// The memory the prompt pass kept on its clone is
					// the pass's now, as it was when the serial loop's
					// own pass kept it: before the handle is stored,
					// so a store that fails below leaves it kept.
					d.applyReapMemory(s.ID, out.mem)
					if pass.handle != nil {
						if err := d.Store.SetHandle(ctx, s.ID, pass.handle); err != nil {
							unknown(s, err)
							continue
						}
					}
					d.promptEvents(ctx, s.Bead, map[string]any{"session": s.ID}, s.Handle, p, pass)
					if pass.handle != nil {
						s.Handle = pass.handle
					}
					if pass.arrived {
						d.prompted(r, s.ID)
					}
					trusted = pass.trusted
				} else {
					d.applyReapMemory(s.ID, out.mem)
					d.applyOwnInputEffect(ctx, s.Bead, out.own)
				}
				if !trusted {
					// A trust dialog answered on this pass (trusted)
					// records no blocked turn: no human waits, and a
					// session still blocked on the next pass is
					// reported then, so the status map waits with it.
					prev, seen := d.status[s.ID]
					if !seen {
						blocked, err := d.wasBlocked(ctx, s)
						if err != nil {
							unknown(s, err)
							continue
						}
						if blocked {
							prev = "blocked"
						}
					}
					d.status[s.ID] = out.status
					if out.status != prev {
						if out.status == "blocked" {
							d.Log.Warn("session blocked; needs a human", "session", s.ID, "bead", s.Bead)
							data := map[string]any{"session": s.ID, "handle": s.Handle}
							// What the agent asks is in its pane: with
							// it, whoever is told can answer without
							// reading the pane first. The worker read
							// it, judging this transition stood.
							if out.tailRead {
								data["tail"] = out.tail
							}
							_ = d.Store.LogDispatch(ctx, s.Bead, "session_blocked", actor, data)
						} else if prev == "blocked" {
							_ = d.Store.LogDispatch(ctx, s.Bead, "session_unblocked", actor, map[string]any{"session": s.ID})
						}
					}
				}
			}
			delete(d.unknown, s.ID)
			if alive && d.limits(ctx, r, &s, groups[s.Group]) {
				live(s)
				d.holdBack(ctx, s, groups[s.Group])
				// After the limits, so a prompt the reset gave on this pass
				// holds a note's nudge back, not the other way around.
				if err := d.tellNotes(ctx, r, &s); err != nil {
					return nil, err
				}
				continue
			}
			if alive {
				continue // stopped by limits, which ended it
			}
			d.end(ctx, s, "died", "runner reports the work is gone")
		}
	}
	return active, nil
}

// judgeSession asks the runner about s for one reap worker and times
// each ask (see askLog): the status call, the pending prompt's pane
// reads and typings where the session has a prompt pending, and the
// pane's tail where the status newly turned blocked. It writes nothing
// of the pass's and nothing to the store: the prompt pass runs on a
// clone carrying only s's memory (reapClone) and its result,
// memory, handle and timings come back in the outcome for the apply. With
// no pending prompt, ownInputWork asks on the same clone and returns its
// memory and deferred log and event effects.
// prev and seen are the pass's status for s as the worker started, and
// decide only the tail read; the apply judges the transition itself.
// The pane cache the pass put on ctx is shared as it stands: it guards
// its snapshots, and a worker still takes at most one of a pane.
func (d *Dispatcher) judgeSession(ctx context.Context, s store.Session, r Runner, cfg map[string]any, prev string, seen bool) (out reapOutcome) {
	ctx, log := withAskLog(ctx, d.now)
	defer func() { out.calls = log.calls }()
	st, ok := r.(Statuser)
	if !ok {
		done := log.timer(askStatus)
		alive, err := r.Alive(ctx, s.Handle)
		done()
		out.alive, out.err = alive, err
		return out
	}
	done := log.timer(askStatus)
	status, err := st.Status(ctx, s.Handle)
	done()
	out.status = status
	if err != nil || status == "" {
		out.err = err
		return out
	}
	out.alive = true
	clone := d.reapClone(s.ID)
	if p, ok := s.Handle[store.PendingPrompt].(string); ok {
		// Looked at on every pass, not only of an idle agent: one that took
		// the prompt late is working on it, and by the time it is idle again
		// the prompt may have left what the runner reads of its pane. No
		// counted-but-untaken bypass: a bead session has no status history
		// in the store to rest the answer on, so a counted send stays shut.
		pass := clone.pendingPrompt(ctx, r, s.ID, s.Handle, p, status, cfg, true, false)
		out.pass, out.hasPass, out.trusted = pass, true, pass.trusted
	} else {
		_, out.own = clone.ownInputWork(ctx, r, s.ID, s.Bead, map[string]any{"session": s.ID}, s.Handle, status)
	}
	out.mem = clone.reapMemoryFor(s.ID)
	if status == "blocked" && !out.trusted && (!seen || prev != "blocked") {
		out.tail, out.tailRead = d.paneTail(ctx, r, s)
	}
	return out
}

// wasBlocked reports whether the last the bead's events say about s is
// session_blocked. d.status does not outlive the daemon, so a dispatcher
// that sees a session for the first time asks the events: a session that
// left its prompt while no daemon ran still gets its session_unblocked, and
// one that is still at it is not reported a second time. Like the web view,
// it reads the bead's latest returnsPage events.
func (d *Dispatcher) wasBlocked(ctx context.Context, s store.Session) (bool, error) {
	evs, err := d.Store.Events(ctx, s.Bead, returnsPage)
	if err != nil {
		return false, err
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if k := evs[i].Kind; (k == "session_blocked" || k == "session_unblocked") && evs[i].Data["session"] == s.ID {
			return k == "session_blocked", nil
		}
	}
	return false, nil
}

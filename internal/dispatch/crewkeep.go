package dispatch

import (
	"context"
	"maps"
	"slices"
	"time"

	"yardr.dev/yardr/internal/store"
)

// How crew keeps its sessions beside the pass (see crew in crew.go):
// the work of one pass in member order, one worker's keep of one
// session on its per-session clone, and the memory the pass merges
// back as each worker leaves it.

// crewKeep is one session's work on one crew pass, in member order: the
// session to keep (keepSession, through starter), or, with stop set, a
// session the crew member no longer keeps, to stop for that reason.
type crewKeep struct {
	session memberSession
	runner  Runner
	starter CrewRunner
	paused  bool
	stop    string
}

// crewOutcome is what one crew worker did with one session: the keep's
// error, the session's pass memory as the worker left it, the runner
// asks it timed, and the starts it asked for. The pass applies outcomes
// in member order (see crew), so everything the serial loop used to do
// with the pass's maps happens there, in the order it always did.
type crewOutcome struct {
	err    error
	mem    crewMemory
	calls  []callTime
	starts []crewJob
}

// crewJobs is the pass's crew work, in member order: for each member its
// sessions to keep, then the sessions it no longer keeps, to stop.
func (d *Dispatcher) crewJobs(ctx context.Context, rs []store.CrewMember, paused bool) []crewKeep {
	// The line session runs where the operator is served; a conversation
	// session for each served user with its env set.
	operator := d.operatorName(ctx)
	var jobs []crewKeep
	for _, r := range rs {
		runner := d.Runners[r.Runner]
		starter, ok := runner.(CrewRunner)
		if !ok {
			d.reportCrew(r.Name, "runner "+r.Runner+" cannot run crew members")
			continue
		}
		var sessions []memberSession
		if r.Serves(operator) {
			sessions = append(sessions, newMemberSession(r, ""))
		}
		for _, user := range r.ConversationUsers() {
			sessions = append(sessions, newMemberSession(r, user))
		}
		for i := range sessions {
			jobs = append(jobs, crewKeep{session: sessions[i], runner: runner, starter: starter, paused: paused})
		}
		if paused {
			continue
		}
		// Sessions the crew member no longer keeps stop: the line's where the
		// operator is not served, and a conversation the config no longer
		// declares. Its row lingers, unread by every slice.
		if !r.Serves(operator) && (r.Live() || len(r.Handle) > 0) {
			s := newMemberSession(r, "")
			jobs = append(jobs, crewKeep{session: s, runner: runner, stop: "the crew member no longer serves the operator"})
		}
		for _, user := range slices.Sorted(maps.Keys(r.Conversations)) {
			cs := r.Conversations[user]
			if !r.HasConversation(user) && (cs.Live() || len(cs.Handle) > 0) {
				s := newMemberSession(r, user)
				jobs = append(jobs, crewKeep{session: s, runner: runner, stop: "the crew member no longer keeps a conversation session for " + user})
			}
		}
	}
	return jobs
}

// keepCrewSession is one crew worker's keep of one session, timed as
// reap's workers are (see askLog): the session's whole keep chain runs
// here, in its own order, on a per-session clone (crewClone), and its
// memory, timed asks and asked-for starts come back in the outcome for
// the pass to apply. The pane cache the pass put on ctx is shared as
// it stands: it guards its snapshots, and a worker still takes at most
// one of a pane.
func (d *Dispatcher) keepCrewSession(ctx context.Context, job crewKeep) (out crewOutcome) {
	ctx, log := withAskLog(ctx, d.now)
	defer func() { out.calls = log.calls }()
	s := job.session
	clone := d.crewClone(s.key(), s.member.Name, &out.starts)
	if job.stop != "" {
		out.err = clone.stopCrew(ctx, job.runner, &s, job.stop)
	} else {
		out.err = clone.keepSession(ctx, job.runner, job.starter, &s, job.paused)
	}
	out.mem = clone.crewMemoryFor(s.key())
	return out
}

// crewMemory is what one session's keep kept in the dispatcher's
// memory, as a crew outcome carries it from its worker to the apply:
// reap's memory for the session, its runner status, whether its agent
// has ever been seen working, and when its pane was last read in, each
// entry's final word on the session's key, present or gone.
type crewMemory struct {
	reapMemory
	status    string
	hasStatus bool
	worked    bool
	hasWorked bool
	idleSince time.Time
	hasIdle   bool
}

// crewClone is a Dispatcher carrying only the per-session memory the
// worker's keep reads, on reapClone's pattern extended with the
// session's status, work history and last harness look: their results
// come back in the outcome, so the pass's maps are written by no
// worker. What the keep shares with the pass it reaches through host
// instead (the limits cache, the home snapshot: each carries its own
// lock, and the refresher already runs beside the pass), and what it
// asks the pass to do comes back in the outcome: a start it wants
// (queued, in crewQueued) and when it next wants looking at (lookDue,
// in the memory). The clone shares the dispatcher's clock, read
// through d.now, so a test that set the clock decides the clone's time
// too. What the keep never reads (the start workers, every other map)
// is not carried.
func (d *Dispatcher) crewClone(key, member string, queued *[]crewJob) *Dispatcher {
	c := &Dispatcher{
		Store:           d.Store,
		Runners:         d.Runners,
		Log:             d.Log,
		Interval:        d.Interval,
		Home:            d.Home,
		PromptSettle:    d.PromptSettle,
		PromptWait:      d.PromptWait,
		DraftWait:       d.DraftWait,
		host:            d,
		crewQueued:      queued,
		promptIdle:      map[string]time.Time{},
		drafts:          map[string]*draft{},
		dialogs:         map[string]string{},
		logins:          map[string]string{},
		nudged:          map[string]ownNudge{},
		unsent:          map[string]*unsent{},
		status:          map[string]string{},
		crewWorkHistory: map[string]bool{},
		idleSince:       map[string]time.Time{},
		crewInFlight:    map[string]string{},
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
		cp := *u
		c.unsent[key] = &cp
	}
	if status, ok := d.status[key]; ok {
		c.status[key] = status
	}
	if worked, ok := d.crewWorkHistory[key]; ok {
		c.crewWorkHistory[key] = worked
	}
	if since, ok := d.idleSince[key]; ok {
		c.idleSince[key] = since
	}
	if startKey, ok := d.crewInFlight[member]; ok {
		c.crewInFlight[member] = startKey
	}
	return c
}

// crewMemoryFor is the clone's final word on key, for its outcome.
func (c *Dispatcher) crewMemoryFor(key string) crewMemory {
	m := crewMemory{reapMemory: c.reapMemoryFor(key)}
	m.status, m.hasStatus = c.status[key]
	m.worked, m.hasWorked = c.crewWorkHistory[key]
	m.idleSince, m.hasIdle = c.idleSince[key]
	return m
}

// applyCrewMemory makes m, one session's memory as its worker left
// it, the pass's own, in the apply.
func (d *Dispatcher) applyCrewMemory(key string, m crewMemory) {
	d.applyReapMemory(key, m.reapMemory)
	if m.hasStatus {
		d.status[key] = m.status
	} else {
		delete(d.status, key)
	}
	if m.hasWorked {
		d.crewWorkHistory[key] = m.worked
	} else {
		delete(d.crewWorkHistory, key)
	}
	if m.hasIdle {
		d.idleSince[key] = m.idleSince
	} else {
		delete(d.idleSince, key)
	}
}

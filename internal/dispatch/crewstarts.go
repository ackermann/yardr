package dispatch

import (
	"context"
)

// Crew starts run off the pass too, one worker per crew member (design
// 2026-10-26-yardr-frbj section 3.1, where crew follows bead starts), on
// the machinery of starts.go: keepSession still makes every decision, and
// only the StartCrew call moves. A pass that waited on it waited on every
// member after it, on the route and on every later step, so one hung fetch
// or herdr call in a member's start stalled the yard; on a worker it
// stalls only that member's next session, which the pass has not handed
// over yet.
//
// The workers share Run's lifecycle with the bead workers (startMu, the
// context their starts derive from, the loop's poke), and differ where a
// crew start differs from a bead's. The runner already writes
// CrewStartingFor itself, and the worker adds CrewStartedFor, so the
// store holds the session's state before the pass hears of it. There is no
// SetHandle twin: what a failed start left open is stopped by the drain's
// crewStartFailed, in the pass, as the inline start did. There is no owned
// set either: reap walks bead sessions only, and nothing judges a crew
// start no worker owns (the early-exit path keys on StartedAt and
// store.MinSessionLife), so a start in flight needs no record for reap.
//
// The slot is the member, not the session: the line session and every
// conversation session of one member go through the one worker, one start
// in flight, serial as before. keepSession hands over for a member only
// while it has nothing in flight and nothing is queued behind a busy
// worker: a later pass asks again. A pass run alone settles the starts it
// handed over before it returns (settleCrewStarts), as a pass that started
// them inline did; under Run it never waits.

// crewJob is one crew session keepSession decided needs a start, handed to
// its member's worker: the session as the pass left it (the member's
// definition, its user and a mirror of the session's state), the runner
// that starts it and the same runner as the pass reads it through, which
// the drain stops and closes leftovers through.
type crewJob struct {
	session memberSession
	runner  Runner
	starter CrewRunner
}

// crewCompletion is what a worker posts when one crew start is done:
// enough for the pass to count a failed start, clear what the session
// before this one left in its maps and close what gone sessions left open,
// in the order the outcomes arrived. The store already holds what the start
// did to the session; this is only the pass's follow-up.
type crewCompletion struct {
	session memberSession
	runner  Runner
	handle  map[string]any
	// err is StartCrew's. storeErr is CrewStartedFor's on a start that
	// succeeded: the agent runs, though the store does not say so.
	err      error
	storeErr error
}

// crewWorker is one member's worker: its job channel carries the one job
// being handed over. The buffer is hand-over slack, not a queue, as a bead
// worker's is: keepSession hands over for a member only while that member
// has nothing in flight (see crewInFlight), and again only after the pass
// has drained that start's completion, so the worker never holds a second
// job and never starts one on its own motion.
type crewWorker struct {
	jobs chan crewJob
}

// initCrewStartsLocked makes the crew workers' maps and channel, where they
// are not yet. d.startMu is held, or no worker runs yet (defaults).
func (d *Dispatcher) initCrewStartsLocked() {
	if d.crewWorkers == nil {
		d.crewWorkers = map[string]*crewWorker{}
	}
	if d.crewInFlight == nil {
		d.crewInFlight = map[string]string{}
	}
	if d.crewDone == nil {
		d.crewDone = make(chan crewCompletion, 64)
	}
}

// handCrewStart hands cj to the worker of its member under Run, starting
// the worker lazily where none runs. A pass run alone starts a one-shot
// goroutine on the pass's context instead, as handStart does. The caller is
// keepSession, which has already decided the session needs a start and
// checked its member has nothing in flight.
func (d *Dispatcher) handCrewStart(ctx context.Context, cj crewJob) {
	name := cj.session.member.Name
	d.crewInFlight[name] = cj.session.key()
	d.startMu.Lock()
	d.initCrewStartsLocked()
	runOwned := d.startRunOwned
	if !runOwned {
		d.startMu.Unlock()
		go func() { d.postCrewCompletion(d.runCrewStart(ctx, cj)) }()
		return
	}
	startCtx := d.startCtx
	if startCtx == nil {
		startCtx = ctx
	}
	w, ok := d.crewWorkers[name]
	if !ok {
		w = &crewWorker{jobs: make(chan crewJob, 1)}
		d.crewWorkers[name] = w
		go d.runCrewWorker(startCtx, name, w)
	}
	d.startMu.Unlock()
	w.jobs <- cj
}

// runCrewWorker is one member's worker under Run: take one job at a time
// and run its start until Run's context ends. A pass run alone uses a
// one-shot goroutine instead (handCrewStart above).
func (d *Dispatcher) runCrewWorker(ctx context.Context, name string, w *crewWorker) {
	defer func() {
		d.startMu.Lock()
		if d.crewWorkers[name] == w {
			delete(d.crewWorkers, name)
		}
		d.startMu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case cj := <-w.jobs:
			d.postCrewCompletion(d.runCrewStart(ctx, cj))
			d.startMu.Lock()
			owned := d.startRunOwned
			d.startMu.Unlock()
			if !owned {
				return
			}
		}
	}
}

// runCrewStart is one crew start on its worker: call the runner, record
// what it opened, and write the start to the store. It touches no pass map:
// the failure's count, the cleared maps and what gone sessions left open
// are applied by the drain (drainCrewCompletions) in the pass.
//
// The context carries no overall bound of its own, for the reason a bead's
// start has none (runStart): the per-call bounds stay the only bounds, as
// they are on the pass today.
func (d *Dispatcher) runCrewStart(ctx context.Context, cj crewJob) crewCompletion {
	s := cj.session
	def := s.member
	handle, err := cj.starter.StartCrew(ctx, def, s.user)
	comp := crewCompletion{session: s, runner: cj.runner, handle: handle, err: err}
	if err != nil {
		if def.Runner == "herdr" && hasHomeTab(handle) {
			// StartCrew can open the tab and then fail while recording its
			// handle. Keep that partial start for home-loss cleanup too.
			d.noteHomeStarted(startedCrewHome(def.Name, s.user, def.Runner, handle))
		}
		return comp
	}
	if def.Runner == "herdr" {
		// The pane is open from here, whatever the store says next.
		d.noteHomeStarted(startedCrewHome(def.Name, s.user, def.Runner, handle))
	}
	comp.storeErr = d.Store.CrewStartedFor(ctx, def.Name, s.user, handle)
	return comp
}

// postCrewCompletion publishes the pass-owned follow-up after a crew
// start's store writes. A pass run alone calls it from a one-shot
// goroutine; Run's member workers call it before taking another job.
func (d *Dispatcher) postCrewCompletion(comp crewCompletion) {
	d.startMu.Lock()
	notify := d.startNotify
	done := d.crewDone
	d.startMu.Unlock()
	// Every handed-over start posts its completion: the channel holds one
	// per member in flight, so this never waits in practice, and a daemon
	// stopping with it undrained loses nothing by the goroutine going with
	// the process: the store holds the session, as for a bead's start.
	done <- comp
	if notify != nil {
		notify() // a start landed: its drain can run, and the crew behind it
	}
}

// drainCrewCompletions applies the completions the member workers have
// posted, in the order they arrived, at the head of a pass and nowhere else:
// a failed start is counted, and a started session's old memory cleared,
// here, before the crew reads the store and before any later start for the
// member. Until this drain the member stays in flight, so nothing is handed
// to its worker in the gap between a failure and the backoff it records.
func (d *Dispatcher) drainCrewCompletions(ctx context.Context) {
	for {
		select {
		case comp := <-d.crewDone:
			d.applyCrewCompletion(ctx, comp)
		default:
			return
		}
	}
}

// settleCrewStarts is a pass run alone finishing the crew starts it handed
// over: with no Run loop to wake for the next pass's drain, it drains their
// completions as they land, until none is in flight. Under Run it does
// nothing, as settleStarts does not.
func (d *Dispatcher) settleCrewStarts(ctx context.Context) {
	d.startMu.Lock()
	owned := d.startRunOwned
	d.startMu.Unlock()
	if owned {
		return
	}
	for len(d.crewInFlight) > 0 {
		comp := <-d.crewDone
		d.applyCrewCompletion(ctx, comp)
	}
}

// applyCrewCompletion is one drained completion's pass-side follow-up:
// clear the member's in-flight record, then what keepSession did with the
// result inline. A failed start is counted whole (crewStartFailed: the
// backoff, the status key, what it left open stopped); a started one is
// logged, forgets what the session before it left in the pass's maps, and
// closes what gone sessions left open (closeLeft), unless the store never
// heard of it.
func (d *Dispatcher) applyCrewCompletion(ctx context.Context, comp crewCompletion) {
	s := comp.session
	name := s.member.Name
	if d.crewInFlight[name] == s.key() {
		delete(d.crewInFlight, name)
	}
	if comp.err != nil {
		if err := d.crewStartFailed(ctx, comp.runner, &s, comp.handle, "", comp.err); err != nil {
			d.Log.Error("crew", "crew", name, "session", s.label(), "err", err)
		}
		return
	}
	d.Log.Info("crew session started", "crew", name, "session", s.label())
	key := s.key()
	delete(d.status, key)
	delete(d.promptIdle, key)
	delete(d.idleSince, key)
	delete(d.drafts, key)
	delete(d.dialogs, key)
	delete(d.logins, key)
	delete(d.nudged, key)
	delete(d.unsent, key)
	delete(d.crewWorkHistory, key)
	if comp.storeErr != nil {
		d.Log.Error("crew", "crew", name, "session", s.label(), "err", comp.storeErr)
		return
	}
	if err := d.closeLeft(ctx, comp.runner, &s); err != nil {
		d.Log.Error("crew", "crew", name, "session", s.label(), "err", err)
	}
}

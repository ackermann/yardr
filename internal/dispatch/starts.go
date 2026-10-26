package dispatch

import (
	"context"

	"yardr.dev/yardr/internal/store"
)

// Bead starts run off the pass, one worker per group (design
// 2026-10-26-yardr-frbj section 3.1): route still makes every decision and
// claims the bead in the pass, and only the runner.Start call moves. A
// pass that waited on Start waited on every later step and the next pass
// behind it, so one hung fetch or herdr call stalled reaping, crew and
// every other group; on a worker it stalls only its own group's next
// bead, which the pass has not claimed yet.
//
// The workers are modelled on the peers' pullers (peers.go): goroutines
// owned by Run, holding no pass state, waking the loop when they finish.
// The store stays the only durable truth: the worker writes its start's
// outcome there first, and posts one small completion the pass drains at
// the head of the next Reconcile to apply what only the pass owns (the
// group's outage, the idle maps, the first-prompt watch). A daemon that
// dies between the worker's write and that pass loses nothing: the
// completion was never the record, the session is. A pass run alone has
// no loop to wake for the drain, so it settles the starts it handed over
// before it returns (settleStarts), as a pass that started them inline
// did; under Run it never waits.

// startOutcome is the class of one finished start, as the pass classifies
// a Start error today: what the drain owes the group and the session.
type startOutcome int

const (
	startSucceeded startOutcome = iota
	startBlockedBead
	startRuntime
	startFailed
)

// startJob is one claimed bead handed to its group's worker: the finished
// Job route built, and the runner that starts it.
type startJob struct {
	job    Job
	runner Runner
}

// startCompletion is what a worker posts when one start is done: enough
// for the pass to run the outage, forget and prompt bookkeeping it owns,
// in the order the outcomes arrived. The store already holds the outcome
// itself; this is only the pass's follow-up.
type startCompletion struct {
	group   store.Group
	bead    string
	session string
	runner  Runner
	handle  map[string]any
	outcome startOutcome
	err     error
}

// startWorker is one group's worker: its job channel carries the one job
// being handed over. The buffer is hand-over slack, not a queue: route
// sends for a group only while that group has nothing in flight (see
// startInFlight), and sends again only after the pass has drained that
// start's completion, so the worker never holds a second claimed job and
// never starts one on its own motion.
type startWorker struct {
	jobs chan startJob
}

// startStartWorkers is Run's start of the workers' shared state, kept
// until Run ends (stopStartWorkers): the context every worker derives
// its starts from, and the loop's poke for each completion that lands.
func (d *Dispatcher) startStartWorkers(ctx context.Context, poke func()) {
	d.startMu.Lock()
	defer d.startMu.Unlock()
	d.initStartsLocked()
	d.startCtx, d.startRunOwned, d.startNotify = ctx, true, poke
}

// stopStartWorkers ends Run's ownership of the workers, as Run ends: a
// start still in flight loses nothing, since what is owed is the session
// in the store, judged by the next daemon's reap as any unowned start is.
func (d *Dispatcher) stopStartWorkers() {
	d.startMu.Lock()
	defer d.startMu.Unlock()
	d.startCtx, d.startRunOwned, d.startNotify = nil, false, nil
}

// initStartsLocked makes the workers' maps and channels, where they are
// not yet. d.startMu is held, or no worker runs yet (defaults).
func (d *Dispatcher) initStartsLocked() {
	if d.startOwned == nil {
		d.startOwned = map[string]bool{}
	}
	if d.startWorkers == nil {
		d.startWorkers = map[string]*startWorker{}
	}
	if d.startInFlight == nil {
		d.startInFlight = map[string]string{}
	}
	if d.startDone == nil {
		d.startDone = make(chan startCompletion, 64)
	}
}

// handStart hands sj to the worker of its job's group under Run, starting
// the worker lazily where none runs. A pass run alone starts a one-shot
// goroutine instead: it uses the pass's context and has no worker to race
// with its next pass while exiting. The caller is route, which has already
// claimed the bead and checked the group has nothing in flight.
func (d *Dispatcher) handStart(ctx context.Context, sj startJob) {
	group := sj.job.Group.Name
	d.startInFlight[group] = sj.job.Session.ID
	d.startMu.Lock()
	d.initStartsLocked()
	runOwned := d.startRunOwned
	if !runOwned {
		d.startMu.Unlock()
		go func() { d.postStartCompletion(d.runStart(ctx, sj)) }()
		return
	}
	startCtx := d.startCtx
	if startCtx == nil {
		startCtx = ctx
	}
	w, ok := d.startWorkers[group]
	if !ok {
		w = &startWorker{jobs: make(chan startJob, 1)}
		d.startWorkers[group] = w
		go d.runStartWorker(startCtx, group, w)
	}
	d.startMu.Unlock()
	w.jobs <- sj
}

// runStartWorker is one group's worker under Run: take one job at a time
// and run its start until Run's context ends. A pass run alone uses a
// one-shot goroutine instead (handStart above).
func (d *Dispatcher) runStartWorker(ctx context.Context, group string, w *startWorker) {
	defer func() {
		d.startMu.Lock()
		if d.startWorkers[group] == w {
			delete(d.startWorkers, group)
		}
		d.startMu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case sj := <-w.jobs:
			d.postStartCompletion(d.runStart(ctx, sj))
			d.startMu.Lock()
			owned := d.startRunOwned
			d.startMu.Unlock()
			if !owned {
				return
			}
		}
	}
}

// runStart is one start on its worker: call the runner, record what it
// opened, and write the outcome to the store. It touches no pass map:
// the outage, the idle state and the prompt watch are applied by the
// drain (drainStartCompletions) in the pass.
//
// The context carries no overall bound of its own, and that is a
// derivation, not an omission: the longest healthy herdr start sums well
// past ten minutes of calls each inside its own bound (design section
// 2.3), so any aggregate short enough to end a wedged start sooner could
// also end a slow but healthy one. The per-call bounds stay the only
// bounds, as they are on the pass today.
func (d *Dispatcher) runStart(ctx context.Context, sj startJob) startCompletion {
	job := sj.job
	sess := job.Session
	d.startMu.Lock()
	d.initStartsLocked()
	d.startOwned[sess.ID] = true
	d.startMu.Unlock()

	handle, err := sj.runner.Start(ctx, job)
	// A start can open its pane and then fail, or open it and succeed:
	// either way what it opened is open from here, whatever the store
	// says next, so a herdr start goes in the home snapshot before any
	// store write is attempted, and whether or not that write succeeds
	// (see noteHomeStarted, already written for a caller that is not
	// the pass): on success as the pass recorded it, and on a failure
	// where the handle holds the tab it opened.
	if job.Group.Runner == "herdr" && (err == nil || hasHomeTab(handle)) {
		d.noteHomeStarted(startedBeadHome(job.Bead.ID, job.Bead.Stage, job.Group.Name, sess.ID, job.Group.Runner, handle))
	}
	comp := startCompletion{group: job.Group, bead: job.Bead.ID, session: sess.ID, runner: sj.runner, handle: handle, err: err}
	switch {
	case err == nil:
		comp.outcome = startSucceeded
		if merr := d.Store.MarkRunning(ctx, sess.ID, handle); merr != nil {
			d.Log.Error("mark running", "session", sess.ID, "err", merr)
			// Keep what Start created where the store can still hold
			// it, as a partial handle is kept below: teardown and
			// reap read the handle from the store, and a session
			// left starting with none could close nothing.
			if len(handle) > 0 {
				if serr := d.Store.SetHandle(ctx, sess.ID, handle); serr != nil {
					d.Log.Error("keep handle of a session that could not be marked running", "session", sess.ID, "err", serr)
				}
			}
		}
	case isBeadBlocked(err):
		// The runner could not safely start this bead: keep the failure
		// on the bead, with the reason, and wait for a person to fix and
		// release it; it counts against neither retries nor the group.
		comp.outcome = startBlockedBead
		if endErr := d.Store.EndSession(ctx, sess.ID, "aborted", err.Error()); endErr != nil {
			d.Log.Error("end blocked session", "session", sess.ID, "err", endErr)
		}
		if len(handle) > 0 {
			_ = d.Store.SetHandle(ctx, sess.ID, handle)
		}
		_ = d.Store.LogDispatch(ctx, job.Bead.ID, "session_blocked", actor, map[string]any{
			"session": sess.ID, "stage": job.Bead.Stage, "reason": err.Error(),
		})
		if _, holdErr := d.Store.SetHold(ctx, job.Bead.ID, true, store.Expect{}, actor); holdErr != nil {
			d.Log.Error("hold bead after blocked start", "bead", job.Bead.ID, "err", holdErr)
		}
	case IsRuntime(err) || IsBlocked(err):
		// Not the bead's fault: abort the session (no failure) and leave
		// the bead for the next try. The group's outage is the drain's
		// to record, before any later claim for the group.
		comp.outcome = startRuntime
		if endErr := d.Store.EndSession(ctx, sess.ID, "aborted", err.Error()); endErr != nil {
			d.Log.Error("end session", "session", sess.ID, "err", endErr)
		}
		if len(handle) > 0 {
			_ = d.Store.SetHandle(ctx, sess.ID, handle)
		}
	default:
		comp.outcome = startFailed
		d.Log.Info("session ended", "session", sess.ID, "bead", job.Bead.ID, "state", "failed", "reason", err.Error())
		if endErr := d.Store.EndSession(ctx, sess.ID, "failed", err.Error()); endErr != nil {
			d.Log.Error("end session", "session", sess.ID, "err", endErr)
		}
		// Keep what Start created so teardown can clean it up.
		if len(handle) > 0 {
			_ = d.Store.SetHandle(ctx, sess.ID, handle)
		}
	}

	d.startMu.Lock()
	delete(d.startOwned, sess.ID)
	d.startMu.Unlock()
	return comp
}

// postStartCompletion publishes the pass-owned follow-up after a start's
// store writes. Standalone passes call it from a one-shot goroutine; Run's
// group workers call it before taking another job.
func (d *Dispatcher) postStartCompletion(comp startCompletion) {
	d.startMu.Lock()
	notify := d.startNotify
	done := d.startDone
	d.startMu.Unlock()
	// Every handed-over start posts its completion: the channel holds
	// one per group in flight, so this never waits in practice, and a
	// daemon stopping with it undrained loses nothing by the goroutine
	// going with the process — the session in the store is the record,
	// judged by the next daemon's reap.
	done <- comp
	if notify != nil {
		notify() // a start landed: its drain can run, and route behind it
	}
}

// startIsOwned says whether a live worker of this daemon owns session
// id's start: reap reads it under the same mutex the workers write it
// under, and no pass map crosses over.
func (d *Dispatcher) startIsOwned(id string) bool {
	d.startMu.Lock()
	defer d.startMu.Unlock()
	return d.startOwned[id]
}

// drainStartCompletions applies the completions the workers have posted,
// in the order they arrived, at the head of a pass and nowhere else: a
// group's outage is recognised here, before reap and before any new
// claim for that group, rather than at the instant its start failed.
// Until this drain the group stays in flight, so no job sits in the gap
// between a failure and the outage it records, as a queued job would.
func (d *Dispatcher) drainStartCompletions(ctx context.Context) {
	for {
		select {
		case comp := <-d.startDone:
			d.applyStartCompletion(ctx, comp)
		default:
			return
		}
	}
}

// settleStarts is a pass run alone finishing what it handed over: with
// no Run loop to wake for the next pass's drain, it drains its own
// starts' completions as they land, until none is in flight. Under Run
// it does nothing: the loop is woken by each completion and drains at
// the head of the pass it wakes, so a Run pass never waits on a start.
func (d *Dispatcher) settleStarts(ctx context.Context) {
	d.startMu.Lock()
	owned := d.startRunOwned
	d.startMu.Unlock()
	if owned {
		return
	}
	for len(d.startInFlight) > 0 {
		comp := <-d.startDone
		d.applyStartCompletion(ctx, comp)
	}
}

// applyStartCompletion is one drained completion's pass-side follow-up:
// clear the group's in-flight record, then what the outcome owes (the
// outage, the backoff, the forgotten idle state, the prompt watch). The
// claim's counts need no repair here: the pass that claimed repaired
// them, and this pass rebuilds both from reap regardless.
func (d *Dispatcher) applyStartCompletion(ctx context.Context, comp startCompletion) {
	if d.startInFlight[comp.group.Name] == comp.session {
		delete(d.startInFlight, comp.group.Name)
	}
	switch comp.outcome {
	case startSucceeded:
		d.runtimeUp(ctx, comp.group)
		// A prompt the runner could not deliver yet is watched from
		// its delivery (see alive).
		if comp.handle[store.PendingPrompt] == nil {
			d.prompted(comp.runner, comp.session)
		}
	case startBlockedBead:
		d.forget(comp.session)
	case startRuntime:
		d.runtimeDown(ctx, comp.group, comp.err)
		d.forget(comp.session)
	case startFailed:
		// As end does for a session the pass itself ends: wait out
		// the backoff before the bead is dispatched again.
		d.retryAfter[comp.bead] = d.now().Add(d.Backoff)
		d.forget(comp.session)
	}
}

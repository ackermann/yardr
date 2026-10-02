// Package dispatch routes ready beads to groups and starts their runners.
//
// Live notifications only wake the loop; every pass re-reads state from the
// store and acts idempotently, so a missed notification costs a delay, never
// a lost bead, and a restart is just another pass.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// Job is everything a runner needs to start work on one bead.
type Job struct {
	Bead    store.Bead
	Group   store.Group
	Rig     *store.Rig // nil when the bead has no rig
	Session store.Session
	// Next lists where the bead may go from its stage when a flow governs
	// it; nil without a flow, where any stage is allowed.
	Next []NextStage
	// Outcomes are how the work in the bead's stage can end, each with the
	// stage its flow sends it to; empty when the flow names none, and the
	// agent then picks from Next.
	Outcomes []NextOutcome
}

// NextStage is one stage a flow allows the bead to move to, with who would
// pick it up there, so a brief can say when to choose it.
type NextStage struct {
	Stage    string
	Terminal bool   // entering it closes the bead
	Group    string // the group routed for the stage; "" when none is
	Runner   string // that group's runner
}

// NextOutcome is one outcome the bead's stage offers and where it leads.
type NextOutcome struct {
	Outcome string
	NextStage
}

// Runner starts and supervises whatever works a bead: an agent, a process,
// a notification. The handle it returns is stored on the session and handed
// back on later calls, so runners keep no state the store does not have.
type Runner interface {
	Start(ctx context.Context, job Job) (handle map[string]any, err error)
	// Alive reports whether the work is still running. An error means
	// "unknown" (the runtime is unreachable) and never ends a session.
	Alive(ctx context.Context, handle map[string]any) (bool, error)
	// Stop tears down runner resources after the session ended.
	Stop(ctx context.Context, handle map[string]any) error
}

type Dispatcher struct {
	Store   store.API
	Runners map[string]Runner
	Log     *slog.Logger
	// Interval is the safety-net reconcile period (default 30s).
	Interval time.Duration
	// MaxAttempts failed or died sessions for one bead+stage before the bead
	// is put on hold for a human (default 3).
	MaxAttempts int
	// Backoff before re-dispatching a bead whose session failed (default 30s).
	Backoff time.Duration
	// Binary, when set, is the daemon's own executable: when a new build is
	// installed over it, Run finishes its pass and execs that build. The
	// sessions live in the store, so the new daemon carries on.
	Binary *version.Binary
	// CheckBinary is how often Binary is checked between passes (default 10s).
	CheckBinary time.Duration

	retryAfter map[string]time.Time
	reported   map[string]string // bead -> last unroutable reason, to log once
	status     map[string]string // session -> last runner status
	unknown    map[string]string // session -> last liveness error logged
}

const actor = "yard"

func (d *Dispatcher) defaults() {
	if d.Interval == 0 {
		d.Interval = 30 * time.Second
	}
	if d.MaxAttempts == 0 {
		d.MaxAttempts = 3
	}
	if d.Backoff == 0 {
		d.Backoff = 30 * time.Second
	}
	if d.CheckBinary == 0 {
		d.CheckBinary = 10 * time.Second
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.retryAfter == nil {
		d.retryAfter = map[string]time.Time{}
		d.reported = map[string]string{}
		d.status = map[string]string{}
		d.unknown = map[string]string{}
	}
}

// PendingPrompt is a handle key: the first prompt a runner could not deliver
// because the agent was not ready (often blocked on a question a human must
// answer, such as trusting a folder). The dispatcher delivers it once the
// agent is idle.
const PendingPrompt = "pending_prompt"

// Statuser is implemented by runners that can tell more than alive/dead.
// A session whose status turns "blocked" (the agent waits on a permission
// prompt or a question) gets a session_blocked event so a human can step in.
type Statuser interface {
	Status(ctx context.Context, handle map[string]any) (string, error)
}

// Finisher is implemented by runners that leave resources behind after a
// session (a git worktree). Finish is called once per session after its bead
// closes. It must refuse to destroy uncommitted work, returning an error
// that explains what was kept.
type Finisher interface {
	Finish(ctx context.Context, handle map[string]any) error
}

// ErrNothingLeft is returned by Finish when another session of the same bead
// already cleaned up (stages share a worktree).
var ErrNothingLeft = errors.New("nothing left to clean up")

// Waker is implemented by runners whose runtime can push events (an agent
// exited, a pane closed). Like store notifications, a wake only triggers a
// pass; Alive decides what actually ended.
type Waker interface {
	Wake(ctx context.Context) (<-chan struct{}, error)
}

// Run reconciles on start, on every store change, on every runner wake-up
// and every Interval.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.defaults()
	if d.Binary != nil {
		// The mayor sees in events why a build installed over ours was not
		// taken.
		d.Binary.Skipped = func(rev, reason string) {
			d.Log.Warn("not replacing the yardr daemon", "candidate", version.Show(rev), "reason", reason)
			if err := d.Store.LogEvent(context.Background(), "", "replace_skipped", actor,
				map[string]any{"process": "daemon", "running": version.Show(d.Binary.Running), "candidate": version.Show(rev), "reason": reason}); err != nil {
				d.Log.Warn("replace_skipped event", "err", err)
			}
		}
	}
	wake := make(chan struct{}, 1)
	poke := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	forward := func(src <-chan struct{}) {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-src:
				if !ok {
					return
				}
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}
	// The store's wake stream ends if its server restarts: reconnect, and
	// wake once afterwards since changes may have been missed meanwhile.
	go func() {
		for first := true; ctx.Err() == nil; first = false {
			if ch, err := d.Store.Wake(ctx); err == nil {
				if !first {
					poke()
				}
				forward(ch)
			} else if first {
				d.Log.Warn("store wake-ups unavailable; retrying", "err", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	for name, r := range d.Runners {
		if w, ok := r.(Waker); ok {
			ch, err := w.Wake(ctx)
			if err != nil {
				d.Log.Warn("runner events unavailable; relying on the reconcile timer", "runner", name, "err", err)
				continue
			}
			go forward(ch)
		}
	}
	d.noteReplaced(ctx)
	var check <-chan time.Time
	if d.Binary != nil {
		t := time.NewTicker(d.CheckBinary)
		defer t.Stop()
		check = t.C
	}
	tick := time.NewTicker(d.Interval)
	defer tick.Stop()
	for {
		if err := d.Reconcile(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.Log.Error("reconcile", "err", err)
		}
		if d.replace() {
			return nil
		}
	wait:
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-check:
				if d.replace() {
					return nil
				}
			case <-tick.C:
				break wait
			case <-wake:
				// Let a burst of writes (a transaction touching several tables)
				// settle into one pass.
				time.Sleep(100 * time.Millisecond)
				select {
				case <-wake:
				default:
				}
				break wait
			}
		}
	}
}

// replace execs a new build of the daemon if one is installed. It is only
// called between passes; it returns true only when a test's fake exec
// "succeeded". A failed exec is logged and the old build carries on.
func (d *Dispatcher) replace() bool {
	if d.Binary == nil {
		return false
	}
	to, ok := d.Binary.Replacement()
	if !ok {
		return false
	}
	from := version.Show(d.Binary.Running)
	d.Log.Info("replacing the yardr daemon", "from", from, "to", version.Show(to), "binary", d.Binary.Path)
	// The new build logs daemon_replaced once it runs.
	if err := d.Binary.Replace(config.Env("REPLACED_FROM", from)...); err != nil {
		d.Log.Error("cannot replace the yardr daemon; running on", "binary", d.Binary.Path, "err", err)
		return false
	}
	return true
}

// noteReplaced logs daemon_replaced when this process is the build a
// previous daemon exec'd.
func (d *Dispatcher) noteReplaced(ctx context.Context) {
	from := config.Getenv("REPLACED_FROM")
	if from == "" {
		return
	}
	config.Unsetenv("REPLACED_FROM")
	to := ""
	if d.Binary != nil {
		to = d.Binary.Running
	}
	d.Log.Info("yardr daemon replaced", "from", from, "to", version.Show(to))
	if err := d.Store.LogDispatch(ctx, "", "daemon_replaced", actor,
		map[string]any{"from": from, "to": version.Show(to)}); err != nil {
		d.Log.Warn("daemon_replaced event", "err", err)
	}
}

// Reconcile performs one idempotent pass: reap dead sessions, tear down
// finished ones, then route and start unowned ready beads.
func (d *Dispatcher) Reconcile(ctx context.Context) error {
	d.defaults()
	groups, err := d.Store.Groups(ctx)
	if err != nil {
		return err
	}
	byName := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}

	active, err := d.reap(ctx, byName)
	if err != nil {
		return err
	}
	if err := d.teardown(ctx, byName); err != nil {
		return err
	}
	if err := d.cleanup(ctx, byName); err != nil {
		return err
	}
	if err := d.residents(ctx); err != nil {
		return err
	}
	return d.route(ctx, byName, active)
}

// reap ends sessions whose runner reports them gone and returns the live
// session count per group.
func (d *Dispatcher) reap(ctx context.Context, groups map[string]store.Group) (map[string]int, error) {
	sessions, err := d.Store.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	active := map[string]int{}
	for _, s := range sessions {
		r := d.runnerFor(s, groups)
		switch {
		case r == nil:
			active[s.Group]++ // cannot judge; leave it alone
		case s.State == "starting":
			// Start runs inside a pass; a 'starting' session seen by a later
			// pass means the dispatcher died mid-start.
			if time.Since(s.StartedAt) > 5*time.Minute {
				d.end(ctx, s, "failed", "start interrupted")
			} else {
				active[s.Group]++
			}
		default:
			alive, err := d.alive(ctx, r, s)
			if err != nil {
				// Say it once per session and cause, not on every pass.
				if msg := err.Error(); d.unknown[s.ID] != msg {
					d.unknown[s.ID] = msg
					d.Log.Warn("liveness unknown", "session", s.ID, "err", err)
				}
				active[s.Group]++
				continue
			}
			delete(d.unknown, s.ID)
			if alive {
				active[s.Group]++
				continue
			}
			d.end(ctx, s, "died", "runner reports the work is gone")
		}
	}
	return active, nil
}

// runnerFor is the runner that started s, so a session is always judged by
// its own runner even after its group switches to another. Sessions from
// before sessions recorded their runner are recognized by their handle.
func (d *Dispatcher) runnerFor(s store.Session, groups map[string]store.Group) Runner {
	name := s.Runner
	if name == "" {
		switch {
		case s.Handle["manual"] != nil:
			name = "manual"
		case s.Handle["pid"] != nil:
			name = "exec"
		case s.Handle["agent"] != nil:
			name = "herdr"
		default:
			name = groups[s.Group].Runner
		}
	}
	return d.Runners[name]
}

// alive asks the runner about s and reports blocked agents once per episode.
func (d *Dispatcher) alive(ctx context.Context, r Runner, s store.Session) (bool, error) {
	st, ok := r.(Statuser)
	if !ok {
		return r.Alive(ctx, s.Handle)
	}
	status, err := st.Status(ctx, s.Handle)
	if err != nil || status == "" {
		return false, err
	}
	if p, ok := s.Handle[PendingPrompt].(string); ok && (status == "idle" || status == "done") {
		if n, ok := r.(Nudger); ok {
			if err := n.Nudge(ctx, s.Handle, p); err != nil {
				return true, nil // try again next pass
			}
		}
		h := withoutPending(s.Handle)
		if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
			return true, err
		}
		_ = d.Store.LogDispatch(ctx, s.Bead, "session_prompted", actor, map[string]any{"session": s.ID})
	}
	if prev := d.status[s.ID]; status != prev {
		d.status[s.ID] = status
		if status == "blocked" {
			d.Log.Warn("session blocked; needs a human", "session", s.ID, "bead", s.Bead)
			_ = d.Store.LogDispatch(ctx, s.Bead, "session_blocked", actor, map[string]any{
				"session": s.ID, "handle": s.Handle})
		} else if prev == "blocked" {
			_ = d.Store.LogDispatch(ctx, s.Bead, "session_unblocked", actor, map[string]any{"session": s.ID})
		}
	}
	return true, nil
}

func (d *Dispatcher) end(ctx context.Context, s store.Session, state, reason string) {
	d.Log.Info("session ended", "session", s.ID, "bead", s.Bead, "state", state, "reason", reason)
	if err := d.Store.EndSession(ctx, s.ID, state, reason); err != nil {
		d.Log.Error("end session", "session", s.ID, "err", err)
	}
	d.retryAfter[s.Bead] = time.Now().Add(d.Backoff)
}

// teardown stops runner resources for sessions that ended, unless the group
// asks to keep them (config keep=true) for inspection.
func (d *Dispatcher) teardown(ctx context.Context, groups map[string]store.Group) error {
	ended, err := d.Store.Unstopped(ctx)
	if err != nil {
		return err
	}
	for _, s := range ended {
		g := groups[s.Group]
		if r := d.runnerFor(s, groups); r != nil && g.Config["keep"] != true && len(s.Handle) > 0 {
			if err := r.Stop(ctx, s.Handle); err != nil {
				d.Log.Warn("stop", "session", s.ID, "err", err)
				continue
			}
		}
		if err := d.Store.MarkStopped(ctx, s.ID); err != nil {
			return err
		}
	}
	return nil
}

// cleanup removes what runners left behind for beads that are now closed,
// unless the group sets cleanup=false.
func (d *Dispatcher) cleanup(ctx context.Context, groups map[string]store.Group) error {
	done, err := d.Store.ClosedWithWorkspace(ctx)
	if err != nil {
		return err
	}
	for _, s := range done {
		g := groups[s.Group]
		if f, ok := d.runnerFor(s, groups).(Finisher); ok && g.Config["cleanup"] != false {
			if err := f.Finish(ctx, s.Handle); errors.Is(err, ErrNothingLeft) {
				// another stage's session already handled it
			} else if err != nil {
				d.Log.Warn("workspace kept", "bead", s.Bead, "session", s.ID, "err", err)
				_ = d.Store.LogDispatch(ctx, s.Bead, "workspace_kept", actor, map[string]any{
					"session": s.ID, "worktree": s.Handle["worktree"], "reason": err.Error()})
			} else {
				_ = d.Store.LogDispatch(ctx, s.Bead, "workspace_removed", actor, map[string]any{
					"session": s.ID, "worktree": s.Handle["worktree"]})
			}
		}
		if err := d.Store.MarkCleaned(ctx, s.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) route(ctx context.Context, groups map[string]store.Group, active map[string]int) error {
	beads, err := d.Store.Dispatchable(ctx)
	if err != nil {
		return err
	}
	if len(beads) == 0 {
		return nil
	}
	routes, err := d.Store.Routes(ctx)
	if err != nil {
		return err
	}
	SortRoutes(routes)
	rigs := map[string]*store.Rig{}

	for _, b := range beads {
		if t, ok := d.retryAfter[b.ID]; ok && time.Now().Before(t) {
			continue
		}
		r := Match(routes, b)
		if r == nil {
			d.reportOnce(ctx, b, "unrouted", fmt.Sprintf("no route for stage %q type %q", b.Stage, b.Type))
			continue
		}
		g, ok := groups[r.Group]
		runner := d.Runners[g.Runner]
		if !ok || runner == nil {
			d.reportOnce(ctx, b, "no_runner", fmt.Sprintf("group %q has no usable runner %q", r.Group, g.Runner))
			continue
		}
		if active[g.Name] >= g.Limit {
			continue
		}
		fails, err := d.Store.FailureCount(ctx, b.ID, b.Stage)
		if err != nil {
			return err
		}
		if fails >= d.MaxAttempts {
			d.Log.Warn("holding bead after repeated failures", "bead", b.ID, "stage", b.Stage, "failures", fails)
			if _, err := d.Store.SetHold(ctx, b.ID, true, actor); err != nil {
				return err
			}
			_ = d.Store.LogDispatch(ctx, b.ID, "gave_up", actor, map[string]any{
				"stage": b.Stage, "failures": fails, "hint": "fix the cause, then `yardr bead unhold`"})
			continue
		}

		var rig *store.Rig
		if b.Rig != nil {
			if rig = rigs[*b.Rig]; rig == nil {
				if rig, err = d.Store.GetRig(ctx, *b.Rig); err != nil {
					d.reportOnce(ctx, b, "bad_rig", err.Error())
					continue
				}
				rigs[*b.Rig] = rig
			}
		}

		// A bead whose flow does not resolve (a pin naming nothing, a broken
		// repo flow, a rig checkout git cannot read) stays put until fixed;
		// the beads after it still dispatch.
		next, outcomes, err := d.nextStages(ctx, b, routes, groups)
		if err != nil {
			d.reportOnce(ctx, b, "bad_flow", err.Error())
			continue
		}

		sess, err := d.Store.Claim(ctx, b.ID, g.Name, b.Stage, actor)
		if errors.Is(err, store.ErrTaken) {
			continue
		} else if err != nil {
			return err
		}
		delete(d.reported, b.ID)
		active[g.Name]++
		d.Log.Info("dispatching", "bead", b.ID, "stage", b.Stage, "group", g.Name, "session", sess.ID)

		handle, err := runner.Start(ctx, Job{Bead: b, Group: g, Rig: rig, Session: *sess, Next: next, Outcomes: outcomes})
		if err != nil {
			d.end(ctx, *sess, "failed", err.Error())
			active[g.Name]--
			// Keep what Start created so teardown can clean it up.
			if len(handle) > 0 {
				_ = d.Store.SetHandle(ctx, sess.ID, handle)
			}
			continue
		}
		if err := d.Store.MarkRunning(ctx, sess.ID, handle); err != nil {
			return err
		}
	}
	return nil
}

// nextStages is where b's flow lets it go next, and the outcomes its stage
// offers, each with the group that would work it there; nil when no flow
// governs b.
func (d *Dispatcher) nextStages(ctx context.Context, b store.Bead, routes []store.Route, groups map[string]store.Group) ([]NextStage, []NextOutcome, error) {
	ns, err := d.Store.NextStages(ctx, b.ID)
	if err != nil || ns.Flow == nil {
		return nil, nil, err
	}
	at := func(st string) NextStage {
		n := NextStage{Stage: st, Terminal: ns.Flow.IsTerminal(st)}
		moved := b
		moved.Stage = st
		if r := Match(routes, moved); r != nil && !n.Terminal {
			n.Group, n.Runner = r.Group, groups[r.Group].Runner
		}
		return n
	}
	next := make([]NextStage, 0, len(ns.Stages))
	for _, st := range ns.Stages {
		next = append(next, at(st))
	}
	var outcomes []NextOutcome
	for _, o := range ns.Outcomes {
		outcomes = append(outcomes, NextOutcome{Outcome: o.Name, NextStage: at(o.To)})
	}
	return next, outcomes, nil
}

func (d *Dispatcher) reportOnce(ctx context.Context, b store.Bead, kind, reason string) {
	key := kind + "|" + b.Stage + "|" + reason
	if d.reported[b.ID] == key {
		return
	}
	d.reported[b.ID] = key
	d.Log.Warn(kind, "bead", b.ID, "reason", reason)
	_ = d.Store.LogDispatch(ctx, b.ID, kind, actor, map[string]any{"stage": b.Stage, "reason": reason})
}

// SortRoutes orders routes so the first match wins; see store.SortRoutes.
func SortRoutes(rs []store.Route) { store.SortRoutes(rs) }

// Match returns the first route (in SortRoutes order) that applies to b.
func Match(sorted []store.Route, b store.Bead) *store.Route { return store.MatchRoute(sorted, b) }

func withoutPending(h map[string]any) map[string]any {
	out := make(map[string]any, len(h))
	for k, v := range h {
		if k != PendingPrompt {
			out[k] = v
		}
	}
	return out
}

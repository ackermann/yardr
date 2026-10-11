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
	"net/http"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// Job is everything a runner needs to start work on one bead.
type Job struct {
	Bead    store.Bead
	Group   store.Group
	Depot   *store.Depot // nil when the bead has no depot
	Session store.Session
	// Train is the train the bead is a child of ("" for none), and Base what
	// its branch starts from and lands on: the train's branch for a child,
	// else the depot's base (see store.Family).
	Train, Base string
	// Children are a train's children, for its brief; nil for other beads.
	Children []string
	// Next lists where the bead may go from its stage when a flow governs
	// it; nil without a flow, where any stage is allowed.
	Next []NextStage
	// Outcomes are how the work in the bead's stage can end, each with the
	// stage its flow sends it to; empty when the flow names none, and the
	// agent then picks from Next.
	Outcomes []NextOutcome
	// StageFile is what the bead's flow says about the stage it is in, when
	// the flow is in the depot's repository and has a stage file for it.
	// StoreFlow names the flow when it is in the store instead: its stage
	// files are in the yard's home, read when the brief is written.
	StageFile *store.StageFile
	StoreFlow string
	// FlowMap is the bead's flow as a short map with the path the bead
	// took, for its brief; nil when no flow governs it. A store flow's
	// lines are filled in from the yard's home when the brief is written.
	FlowMap *store.FlowMap
	// Calls are the calls of declared sources the session may make (yardr
	// call), for its brief; empty when its group is granted none.
	Calls []store.Callable
	// Pull is the pull request the bead tracks; nil when it tracks none, and
	// for a train, whose own pull request is of the yard's branch. A runner
	// that gives the bead a worktree makes it stand on the pull request's
	// head (see runner.Herdr).
	Pull *Pull
}

// Pull is a pull request a bead tracks: somebody else's change, which the
// bead's sessions read and do not write.
type Pull struct {
	URL    string // as the bead's link has it: https://github.com/owner/repo/pull/N
	Number int
	// Head is the commit the session's worktree was put on, the pull
	// request's head when the session started. The runner sets it; it is
	// empty where there is no worktree.
	Head string
}

// pullOf is the pull request among a bead's links: the one it tracks, of a
// source that has pull requests. An issue it tracks is none, and neither is
// a link that only relates.
func (d *Dispatcher) pullOf(ctx context.Context, links []store.Link) *Pull {
	for _, l := range links {
		if l.Kind != store.LinkTracks {
			continue
		}
		p, ok := hooks.PullerFor(l.Source, d.declOf(ctx, l.Source))
		if !ok {
			continue
		}
		thread := p.PullOf(l.URL)
		if thread == "" {
			continue
		}
		_, num, _ := strings.Cut(thread, "#")
		if n, err := strconv.Atoi(num); err == nil {
			return &Pull{URL: l.URL, Number: n}
		}
	}
	return nil
}

// NextStage is one stage a flow allows the bead to move to, with who would
// pick it up there, so a brief can say when to choose it.
type NextStage struct {
	Stage    string
	Terminal bool   // entering it closes the bead
	Group    string // the group routed for the stage; "" when none is
	Runner   string // that group's runner
	Peer     string // the linked yard that works the stage, for a peer group
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
	// Backoff before re-dispatching a bead whose session failed (default
	// 30s). It is also the first wait before a bead its flow sent back to
	// the stage it was in (open -> open on failed) is dispatched there
	// again; that wait doubles with each such return in a row, up to
	// BackoffMax (default 1h), so an outage the stage's script keeps
	// running into (GitHub down) is retried less and less often rather
	// than on every pass.
	Backoff, BackoffMax time.Duration
	// RuntimeBackoff is how long a group whose runtime is down (a Start
	// failed with a RuntimeError) is left alone before the next try
	// (default 30s); it doubles on every further outage, up to
	// RuntimeBackoffMax (default 5m).
	RuntimeBackoff, RuntimeBackoffMax time.Duration
	// Home is the yard's home: what a crew member's env_command is told, and
	// where it runs for a crew member without a directory.
	Home string
	// Binary, when set, is the daemon's own executable: when a new build is
	// installed over it, Run finishes its pass and execs that build. The
	// sessions live in the store, so the new daemon carries on.
	Binary *version.Binary
	// CheckBinary is how often Binary is checked between passes (default 10s).
	CheckBinary time.Duration
	// Retention is how long the store keeps events, ended sessions,
	// webhook bodies and what a link remembers of its messages
	// (store.PeerKeep), and how long an unread inbox event holds them; the
	// daemon prunes on start and every PruneEvery (default 1h). Zero keeps
	// everything and never prunes.
	Retention  store.Retention
	PruneEvery time.Duration
	// PRHead tells the head commit of a train's pull request when no
	// pull_request event has (see github.go); nil asks gh.
	PRHead func(ctx context.Context, q PRQuery) (string, error)

	// CI reads what GitHub says of the checks on a commit of a train's
	// pull request (see ci.go); nil asks gh.
	CI func(ctx context.Context, q CIQuery) (CIChecks, error)

	// DepotRepo tells the repository a depot is of at a source (GitHub's
	// owner/name), "" when it is of none there: where an event makes a bead
	// by a line that names no depot (see makes.go); nil reads the depot's
	// origin remote.
	DepotRepo func(ctx context.Context, q DepotRepoQuery) (string, error)

	// IssueTries is how often a tracked issue's move is tried before it is
	// given up (default 24: with the default Backoff and BackoffMax, most of
	// a day). See issues.go.
	IssueTries int

	// PeerKey is the file of the yard's key, with which requests to its
	// peers are signed; PeerHTTP makes them (nil: http.DefaultClient).
	// PeerTries is how often a message is posted to a peer before it is
	// given up (default 24, as IssueTries; a bead's message carries its
	// group's own count). PeerHold is how long a peer is asked to hold a
	// question it has nothing to answer with (default 30s; whole seconds,
	// and under one asks for no hold), and PeerPoll how long a peer that
	// holds none is left before it is asked again, and how often what
	// waits for a peer is looked at between passes (default 5s). See
	// peers.go.
	PeerKey   string
	PeerHTTP  *http.Client
	PeerTries int
	PeerHold  time.Duration
	PeerPoll  time.Duration

	// PromptSettle is how long an agent has to have been idle before a
	// pending first prompt is delivered to it (default 3s), and PromptWait
	// how long after a delivery the prompt may take to show before it
	// counts as lost (default 10s, as the herdr runner waits for the one it
	// sends itself). Negative: no wait. See pendingPrompt.
	PromptSettle, PromptWait time.Duration
	// DraftWait is how long text may sit in an agent's input, holding back
	// what yardr has to type there, before an event says so (default 10m).
	// See inputHeld.
	DraftWait time.Duration

	ghCursor uint64 // the last event put to its hook's table (github.go)
	// madeTo is hook -> the seq of the last event its table of what its
	// events make was put to, nil until the log was read for it
	// (makes.go).
	madeTo     map[string]uint64
	madeCursor uint64 // last event included in madeTo
	// issueWait is source -> when it is asked again after a call that
	// failed, so its other moves do not each wait out a timeout.
	issueWait map[string]time.Time
	// pushWait is the same for the sources that tell users outside the
	// yard, and pushDue when the next push is due between passes. See
	// pushes.go.
	pushWait map[string]time.Time
	pushDue  *time.Time
	// peerCalls is whether any peer can be called, as of the last pass.
	// peerWait is peer -> when it is called again after a request that
	// failed. peerErr and peerKeyErr are the last errors said, so each is
	// said once. Both are by the peer's key: its name can change, and be
	// another yard's later.
	peerCalls  bool
	peerWait   map[string]time.Time
	peerErr    map[string]string
	peerKeyErr string
	// pulling is the context the peers' pullers run in, nil outside Run: a
	// pass run alone asks its peers itself. pullersOf is peer key -> its
	// puller, and poke wakes Run's loop. See peers.go.
	pulling   context.Context
	pullersOf map[string]puller
	poke      func()
	// fetchErr is the last error of asking the store which branches came
	// back from peers (fetches), said once.
	fetchErr string
	// basesKept is depot -> the bases of beads from peers its refs were last
	// made to keep (bases), nil until the first pass; basesErr is the last
	// error of asking the store for them, said once.
	basesKept map[string]keptBases
	basesErr  string
	// ciRead is train whose CI is read (ci.go) -> its revision when it was read and
	// acted on; an event about the train's pull request removes it, so the
	// state is read again. ciWait is train -> the wait after a read GitHub
	// did not answer.
	ciRead map[string]string
	ciWait map[string]*outage

	// timerDue is when the earliest timer left is due, as of the last pass
	// (see timers); nil with none.
	timerDue *time.Time

	// promptDue is when a pending prompt is next to be delivered or looked
	// for, as of the last pass (see pendingPrompt); nil with none.
	// promptIdle is session (or "crew:<name>") -> when its agent was
	// first seen idle with a prompt that has not been delivered yet.
	promptDue  *time.Time
	promptIdle map[string]time.Time
	// drafts is session (or "crew:<name>") -> the text found in its
	// agent's input when yardr had something to type there. See inputHeld.
	// dialogs is the same -> the dialog of its harness that was told of
	// and has not been seen gone. See dialogAt.
	// nudged is the same -> the last nudge yardr typed and when, and unsent
	// the same -> text of yardr's own found in its agent's input. See
	// ownInput.
	drafts  map[string]*draft
	dialogs map[string]string
	nudged  map[string]ownNudge
	unsent  map[string]*unsent

	// limitsAsked is provider -> what it last said of its limits, kept for
	// limitsMemory. See harness_limits.go.
	limitsAsked map[string]limitsAnswer

	clock      func() time.Time // tests only; see now
	retryAfter map[string]time.Time
	reported   map[string]string  // bead -> last unroutable reason, to log once
	stays      map[string]stay    // bead -> its stay in a manual group's stage (waits.go)
	status     map[string]string  // session -> last runner status
	unknown    map[string]string  // session -> last liveness error logged
	down       map[string]*outage // group -> its runtime's current outage
	idleSince  map[string]time.Time
	idleTold   map[string]bool // session -> session_idle raised this idle spell
	// fresh is session -> when its agent got its first prompt, for the
	// sessions seen idle on every pass since (see stall).
	fresh map[string]time.Time
	// paused is group -> model -> the live session that stalled with it, or
	// waits with it for a usage limit's window, as of this pass: route starts
	// no further session with that model (see holdBack).
	paused map[string]map[string]string
}

// outage is a group whose runner could not start anything: the runtime is
// down, so its beads wait, untouched, until the next try.
type outage struct {
	until time.Time
	delay time.Duration
}

// RuntimeError marks a Start error as the runtime's (herdr missing or down,
// a workspace it could not create), not the bead's. Every bead of the group
// would fail the same way, so the dispatcher does not count it against the
// bead: it reports the group's runtime down and backs off instead.
type RuntimeError struct{ Err error }

func (e *RuntimeError) Error() string { return e.Err.Error() }
func (e *RuntimeError) Unwrap() error { return e.Err }

// Runtime wraps err as a RuntimeError; nil stays nil.
func Runtime(err error) error {
	if err == nil {
		return nil
	}
	return &RuntimeError{Err: err}
}

// IsRuntime reports whether err is, or wraps, a RuntimeError.
func IsRuntime(err error) bool {
	var re *RuntimeError
	return errors.As(err, &re)
}

// BlockedError marks a Start error as the session's own configuration
// refusing it (an env_file that is missing or open to others): no retry
// fixes it until a human does, and every session of the same group or
// crew member would fail alike, so it is reported rather than counted against
// a bead.
type BlockedError struct{ Err error }

func (e *BlockedError) Error() string { return e.Err.Error() }
func (e *BlockedError) Unwrap() error { return e.Err }

// Blocked wraps err as a BlockedError; nil stays nil.
func Blocked(err error) error {
	if err == nil {
		return nil
	}
	return &BlockedError{Err: err}
}

// IsBlocked reports whether err is, or wraps, a BlockedError.
func IsBlocked(err error) bool {
	var be *BlockedError
	return errors.As(err, &be)
}

// BeadBlockedError marks a Start failure that needs this bead to wait for a
// person, rather than another retry or holding every bead in the group. The
// dispatcher ends the unstarted session, records session_blocked with the
// reason, and holds the bead until it is released.
type BeadBlockedError struct{ Err error }

func (e *BeadBlockedError) Error() string { return e.Err.Error() }
func (e *BeadBlockedError) Unwrap() error { return e.Err }

// BeadBlocked wraps err as a bead-specific blocked Start error; nil stays nil.
func BeadBlocked(err error) error {
	if err == nil {
		return nil
	}
	return &BeadBlockedError{Err: err}
}

func isBeadBlocked(err error) bool {
	var be *BeadBlockedError
	return errors.As(err, &be)
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
	if d.BackoffMax == 0 {
		d.BackoffMax = time.Hour
	}
	if d.RuntimeBackoff == 0 {
		d.RuntimeBackoff = 30 * time.Second
	}
	if d.RuntimeBackoffMax == 0 {
		d.RuntimeBackoffMax = 5 * time.Minute
	}
	if d.IssueTries == 0 {
		d.IssueTries = 24
	}
	if d.PeerTries == 0 {
		d.PeerTries = 24
	}
	if d.PeerHold == 0 {
		d.PeerHold = 30 * time.Second
	}
	if d.PeerPoll == 0 {
		d.PeerPoll = 5 * time.Second
	}
	if d.CheckBinary == 0 {
		d.CheckBinary = 10 * time.Second
	}
	if d.PruneEvery == 0 {
		d.PruneEvery = time.Hour
	}
	if d.PromptSettle == 0 {
		d.PromptSettle = defaultPromptSettle
	}
	if d.PromptWait == 0 {
		d.PromptWait = defaultPromptWait
	}
	if d.DraftWait == 0 {
		d.DraftWait = defaultDraftWait
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.retryAfter == nil {
		d.retryAfter = map[string]time.Time{}
		d.reported = map[string]string{}
		d.status = map[string]string{}
		d.unknown = map[string]string{}
		d.down = map[string]*outage{}
		d.idleSince = map[string]time.Time{}
		d.idleTold = map[string]bool{}
		d.fresh = map[string]time.Time{}
		d.promptIdle = map[string]time.Time{}
		d.drafts = map[string]*draft{}
		d.dialogs = map[string]string{}
		d.nudged = map[string]ownNudge{}
		d.unsent = map[string]*unsent{}
	}
}

// PromptUnconfirmed is a handle key next to store.PendingPrompt: the runner sent
// the prompt and took it as arrived on a sign that can be wrong (the agent
// was reported working, or its pane could not be read) without seeing it
// taken. The dispatcher counts it as sent and looks again (see
// pendingPrompt).
const PromptUnconfirmed = "prompt_unconfirmed"

// PromptLook is a handle key: what the runner saw of the first prompt it
// sent, and what its runtime said of it ("typed, herdr stalled"). It stays
// on the handle so the started event, and a session_stalled or
// session_idle after it, say why the prompt was or was not left pending.
const PromptLook = "prompt_look"

// Statuser is implemented by runners that can tell more than alive/dead.
// A session whose status turns "blocked" (the agent waits on a permission
// prompt or a question) gets a session_blocked event so a human can step in,
// with the end of its pane as `tail` where the runner can read one (Tailer).
type Statuser interface {
	Status(ctx context.Context, handle map[string]any) (string, error)
}

// Waiter is implemented by a runner whose session is a wait for work done
// elsewhere (another yard's): WaitLimit is its max_age when the group sets
// none, at which session_aged is raised once so the yardmaster hears. Nothing
// stops such a session by its age: ending it would send the work out
// again while the other side may still be at it.
type Waiter interface {
	WaitLimit() time.Duration
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
		// The yardmaster sees in events why a build installed over ours was not
		// taken.
		d.Binary.Skipped = func(rev, reason string) {
			d.Log.Warn("not replacing the yardr daemon", "candidate", version.Show(rev), "reason", reason)
			if err := d.Store.LogDispatch(context.Background(), "", "replace_skipped", actor,
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
	var prune <-chan time.Time
	if d.Retention != (store.Retention{}) {
		d.prune(ctx)
		t := time.NewTicker(d.PruneEvery)
		defer t.Stop()
		prune = t.C
	}
	tick := time.NewTicker(d.Interval)
	defer tick.Stop()
	// A yard that cannot be called gets its messages by asking, which is
	// not a pass's to wait on: each peer has a puller, started by the pass
	// that first sees the peer, and stopped with Run.
	pulling, stopPulling := context.WithCancel(ctx)
	defer stopPulling()
	d.pulling, d.pullersOf, d.poke = pulling, nil, poke
	// What a post that failed is tried again by is looked at more often
	// than the yard is reconciled. It makes no request while nothing is
	// due.
	poll := time.NewTicker(d.PeerPoll)
	defer poll.Stop()
	for {
		if err := d.Reconcile(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.Log.Error("reconcile", "err", err)
		}
		if d.replace(ctx) {
			return nil
		}
		// A timer or a pending prompt is due between passes: wake for it, and
		// no sooner than a second from now, so one that cannot be fired is
		// not spun on.
		var due <-chan time.Time
		var timer *time.Timer
		next := d.timerDue
		if d.promptDue != nil && (next == nil || d.promptDue.Before(*next)) {
			next = d.promptDue
		}
		if d.pushDue != nil && (next == nil || d.pushDue.Before(*next)) {
			next = d.pushDue
		}
		if next != nil {
			timer = time.NewTimer(max(next.Sub(d.now()), time.Second))
			due = timer.C
		}
	wait:
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-due:
				break wait
			case <-check:
				if d.replace(ctx) {
					return nil
				}
			case <-prune:
				d.prune(ctx)
			case <-poll.C:
				if d.peerCalls {
					if err := d.peers(ctx); err != nil && ctx.Err() == nil {
						d.Log.Warn("peers", "err", err)
					}
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
		if timer != nil {
			timer.Stop()
		}
	}
}

// prune keeps the store's log within d.Retention (see store.Prune).
func (d *Dispatcher) prune(ctx context.Context) {
	res, err := d.Store.Prune(ctx, d.Retention, actor)
	if err != nil {
		d.Log.Warn("prune store", "err", err)
		return
	}
	if res.Events+res.HookPayloads+res.Sessions+len(res.Peers) > 0 {
		d.Log.Info("pruned store", "events", res.Events, "sessions", res.Sessions, "hook_payloads", res.HookPayloads,
			"held_by", res.HeldBy, "expired", res.Expired, "peers", res.Peers)
	}
}

// replace execs a new build of the daemon if one is installed. It is only
// called between passes, and never once ctx has ended: a daemon told to
// stop while a pass ran stops, rather than come back up as the new build.
// It returns true only when a test's fake exec "succeeded". A failed exec
// is logged and the old build carries on.
func (d *Dispatcher) replace(ctx context.Context) bool {
	if d.Binary == nil || ctx.Err() != nil {
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
// finished ones, apply the github rules to new hook events, read the CI of
// trains in in-pr, move the issues beads track, tell users outside the yard
// of what landed in their inboxes, route and start unowned ready beads.
func (d *Dispatcher) Reconcile(ctx context.Context) error {
	d.defaults()
	d.promptDue, d.pushDue = nil, nil
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
	// Before the crew, so one a timer is for is nudged in this pass.
	d.timers(ctx)
	// By the same clock, and before the crew for the same reason.
	d.waits(ctx, byName)
	if err := d.crew(ctx); err != nil {
		return err
	}
	// Before routing, so a train the rules move to land is dispatched to
	// its merger in this pass.
	if err := d.github(ctx); err != nil {
		return err
	}
	if err := d.ci(ctx); err != nil {
		return err
	}
	if err := d.issues(ctx); err != nil {
		return err
	}
	if err := d.pushes(ctx); err != nil {
		return err
	}
	// Before anything is started: what a bead from a peer starts from is
	// kept by its ref when its worktree is made.
	d.bases(ctx)
	if err := d.route(ctx, byName, active); err != nil {
		return err
	}
	// Last: a peer that does not answer holds up nothing of this yard's,
	// and neither does a remote. What a peer's return brought in this pass
	// is fetched in it.
	err = d.peers(ctx)
	d.fetches(ctx)
	return err
}

// timers fires the timers that are due and drops those whose awaited
// event came first (store.FireTimers), by the dispatcher's clock, and
// notes when the next is due so Run wakes for it. One that was due while
// no daemon ran fires on the first pass of the next, marked late.
func (d *Dispatcher) timers(ctx context.Context) {
	pass, err := d.Store.FireTimers(ctx, d.now())
	if err != nil {
		// The next pass tries again: a timer is late rather than lost.
		d.Log.Warn("timers", "err", err)
		return
	}
	d.timerDue = pass.Next
	if pass.Fired+pass.Dropped > 0 {
		d.Log.Info("timers", "fired", pass.Fired, "dropped", pass.Dropped)
	}
}

// reap ends sessions whose runner reports them gone and returns the live
// session count per group.
func (d *Dispatcher) reap(ctx context.Context, groups map[string]store.Group) (map[string]int, error) {
	sessions, err := d.Store.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	active := map[string]int{}
	d.paused = map[string]map[string]string{}
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
			alive, err := d.alive(ctx, r, &s)
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
			if alive && d.limits(ctx, r, &s, groups[s.Group]) {
				active[s.Group]++
				d.holdBack(ctx, s, groups[s.Group])
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

// runnerFor is the runner that started s, so a session is always judged by
// its own runner even after its group switches to another. Sessions from
// before sessions recorded their runner are recognized by their handle.
func (d *Dispatcher) runnerFor(s store.Session, groups map[string]store.Group) Runner {
	name := s.RunnerName()
	if name == "" {
		name = groups[s.Group].Runner
	}
	return d.Runners[name]
}

// alive asks the runner about s and reports blocked agents once per episode.
// s.Handle is updated with what it stores about a pending prompt.
func (d *Dispatcher) alive(ctx context.Context, r Runner, s *store.Session) (bool, error) {
	st, ok := r.(Statuser)
	if !ok {
		return r.Alive(ctx, s.Handle)
	}
	status, err := st.Status(ctx, s.Handle)
	if err != nil || status == "" {
		return false, err
	}
	if p, ok := s.Handle[store.PendingPrompt].(string); ok {
		// Looked at on every pass, not only of an idle agent: one that took
		// the prompt late is working on it, and by the time it is idle again
		// the prompt may have left what the runner reads of its pane.
		pass := d.pendingPrompt(ctx, r, s.ID, s.Handle, p, status)
		if pass.handle != nil {
			if err := d.Store.SetHandle(ctx, s.ID, pass.handle); err != nil {
				return true, err
			}
		}
		d.promptEvents(ctx, s.Bead, map[string]any{"session": s.ID}, s.Handle, p, pass)
		if pass.handle != nil {
			s.Handle = pass.handle
		}
		if pass.arrived {
			d.prompted(r, s.ID)
		}
	} else {
		d.ownInput(ctx, r, s.ID, s.Bead, map[string]any{"session": s.ID}, s.Handle, status)
	}
	prev, seen := d.status[s.ID]
	if !seen {
		blocked, err := d.wasBlocked(ctx, *s)
		if err != nil {
			return false, err
		}
		if blocked {
			prev = "blocked"
		}
	}
	d.status[s.ID] = status
	if status != prev {
		if status == "blocked" {
			d.Log.Warn("session blocked; needs a human", "session", s.ID, "bead", s.Bead)
			data := map[string]any{"session": s.ID, "handle": s.Handle}
			// What the agent asks is in its pane: with it, whoever is told
			// can answer without reading the pane first.
			if tail, read := d.paneTail(ctx, r, *s); read {
				data["tail"] = tail
			}
			_ = d.Store.LogDispatch(ctx, s.Bead, "session_blocked", actor, data)
		} else if prev == "blocked" {
			_ = d.Store.LogDispatch(ctx, s.Bead, "session_unblocked", actor, map[string]any{"session": s.ID})
		}
	}
	return true, nil
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

func (d *Dispatcher) end(ctx context.Context, s store.Session, state, reason string) {
	d.Log.Info("session ended", "session", s.ID, "bead", s.Bead, "state", state, "reason", reason)
	if err := d.Store.EndSession(ctx, s.ID, state, reason); err != nil {
		d.Log.Error("end session", "session", s.ID, "err", err)
	}
	d.retryAfter[s.Bead] = d.now().Add(d.Backoff)
	d.forget(s.ID)
}

// returnsPage bounds the events backingOff reads per bead: the returns it
// counts are the latest, and a bead with more events than this since it
// last entered its stage otherwise is retried as if it had this many.
const returnsPage = 200

// backingOff reports whether b is to wait before being dispatched again:
// its flow has sent it back to the stage it is in n times in a row (an
// advance from and to b.Stage, as open -> open on failed when integrating
// a train fails for a reason no child fixes), and the last was less than
// Backoff doubled n-1 times, up to BackoffMax, ago. The count is read from
// the bead's events, so a restarted daemon keeps waiting, and it starts
// over when the bead enters the stage any other way or a human un-holds
// it. Each wait is logged once on the bead as a backoff event.
func (d *Dispatcher) backingOff(ctx context.Context, b store.Bead) (bool, error) {
	evs, err := d.Store.Events(ctx, b.ID, returnsPage)
	if err != nil {
		return false, err
	}
	n, last := 0, time.Time{}
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		if e.Kind == "unheld" {
			break
		}
		if e.Kind != "advanced" {
			continue
		}
		if e.Data["from"] != b.Stage || e.Data["to"] != b.Stage {
			break
		}
		if n == 0 {
			last = e.At
		}
		n++
	}
	if n == 0 {
		return false, nil
	}
	wait := min(d.Backoff<<min(n-1, 30), d.BackoffMax)
	until := last.Add(wait)
	if !d.now().Before(until) {
		return false, nil
	}
	d.reportOnce(ctx, b, "backoff", fmt.Sprintf("sent back to %s %d times in a row; next try at %s", b.Stage, n, until.UTC().Format(time.RFC3339)))
	return true, nil
}

// now is the dispatcher's clock, time.Now unless a test sets clock.
func (d *Dispatcher) now() time.Time {
	if d.clock != nil {
		return d.clock()
	}
	return time.Now()
}

// forget drops what the dispatcher remembers about an ended session.
func (d *Dispatcher) forget(id string) {
	delete(d.status, id)
	delete(d.unknown, id)
	delete(d.idleSince, id)
	delete(d.idleTold, id)
	delete(d.fresh, id)
	delete(d.promptIdle, id)
	delete(d.drafts, id)
	delete(d.dialogs, id)
	delete(d.nudged, id)
	delete(d.unsent, id)
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
	ready, err := d.Store.Dispatchable(ctx)
	if err != nil {
		return err
	}
	if len(ready) == 0 {
		return nil
	}
	routes, err := d.Store.Routes(ctx)
	if err != nil {
		return err
	}
	SortRoutes(routes)
	depots := map[string]*store.Depot{}
	// Beads in a stage their flow lacks: a repository flow lost the stage on
	// the base. The stage's route may well still be there, and would hand
	// the bead to its old group with the flow's first stage as the only way
	// on, so it is not routed at all until someone advances it.
	lost, err := d.Store.Stranded(ctx, "")
	if err != nil {
		return err
	}
	lostByID := make(map[string]store.StrandedBead, len(lost))
	for _, b := range lost {
		lostByID[b.Bead] = b
	}

	for _, rb := range ready {
		// What a server of a build from before ReadyBead answers with
		// decodes to this (store.ReadyBead says why): nothing is started
		// until it has replaced itself.
		if rb.Bead == "" {
			return errors.New("the store listed a ready bead without its handle: the server is of another build")
		}
		// As much of the bead as deciding reads: most ready beads wait on
		// a group at its limit, so the bead itself is read when it starts.
		b := store.Bead{ID: rb.Bead, Stage: rb.Stage, Type: rb.Type, Depot: rb.Depot, Labels: rb.Labels}
		if rb.Model != "" {
			b.Metadata = map[string]any{"model": rb.Model}
		}
		if stranded, ok := lostByID[b.ID]; ok {
			d.reportStranded(ctx, b, stranded)
			continue
		}
		if t, ok := d.retryAfter[b.ID]; ok && d.now().Before(t) {
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
		if o := d.down[g.Name]; o != nil && time.Now().Before(o.until) {
			continue
		}
		// A session of the group sits stalled with this model, or waits
		// with it for a usage limit's window to reset: another would end
		// the same way (see holdBack).
		if _, held := d.paused[g.Name][modelFor(g, b)]; held {
			continue
		}
		// Last, since it reads the bead's events: only a bead that would
		// otherwise dispatch now pays for it, not every ready bead in
		// backlog on every pass.
		if wait, err := d.backingOff(ctx, b); err != nil {
			return err
		} else if wait {
			continue
		}
		fails, err := d.Store.FailureCount(ctx, b.ID, b.Stage)
		if err != nil {
			return err
		}
		if fails >= d.MaxAttempts {
			d.Log.Warn("holding bead after repeated failures", "bead", b.ID, "stage", b.Stage, "failures", fails)
			if _, err := d.Store.SetHold(ctx, b.ID, true, store.Expect{}, actor); err != nil {
				return err
			}
			_ = d.Store.LogDispatch(ctx, b.ID, "gave_up", actor, map[string]any{
				"stage": b.Stage, "failures": fails, "hint": "fix the cause, then `yardr bead unhold`"})
			continue
		}

		var depot *store.Depot
		if b.Depot != nil {
			if depot = depots[*b.Depot]; depot == nil {
				if depot, err = d.Store.GetDepot(ctx, *b.Depot); err != nil {
					d.reportOnce(ctx, b, "bad_depot", err.Error())
					continue
				}
				depots[*b.Depot] = depot
			}
		}

		// A bead whose flow does not resolve (a pin naming nothing, a broken
		// repo flow, a depot checkout git cannot read) stays put until fixed;
		// the beads after it still dispatch.
		next, outcomes, stage, err := d.nextStages(ctx, b, routes, groups)
		if err != nil {
			d.reportOnce(ctx, b, "bad_flow", err.Error())
			continue
		}

		// A child of two trains (possible only when a type changed to train
		// later) has no base until a parent edge goes.
		fam, err := d.Store.Family(ctx, b.ID)
		if err != nil {
			d.reportOnce(ctx, b, "bad_train", err.Error())
			continue
		}

		// A wagon with no train is moved nowhere by the store, so one found
		// here lost its train after it moved (its parent edge was removed).
		// Nothing works it until it has one again: its flow has no review,
		// and its builder's brief would name the depot's base to land on. People still get it: someone has to see it to fix it.
		if b.Type == store.TypeWagon && fam.Train == "" && g.Runner != "manual" {
			d.reportOnce(ctx, b, "wagon_without_train", fmt.Sprintf(
				"a wagon with no train is not started; make it a child (yardr dep add <train> %s -k parent) or a task (yardr bead update %s --type task)", b.ID, b.ID))
			continue
		}

		// The bead as it is now. One that moved or went since it was listed
		// is not claimed, as Claim would refuse it.
		whole, err := d.Store.GetBead(ctx, b.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return err
		}
		if whole.Stage != b.Stage {
			continue
		}
		b = *whole
		// A runner is handed the bead as a listing shows it, with its train
		// on it, which GetBead leaves off.
		b.Train = fam.Train

		// Before the session is claimed: one that cannot tell whether its
		// bead tracks a pull request would start on the base and review
		// nothing.
		var pull *Pull
		if b.Type != store.TypeTrain {
			links, err := d.Store.Links(ctx, b.ID)
			if err != nil {
				return err
			}
			pull = d.pullOf(ctx, links)
		}

		sess, err := d.Store.Claim(ctx, b.ID, g.Name, b.Stage, actor)
		if errors.Is(err, store.ErrTaken) || errors.Is(err, store.ErrAtLimit) {
			// ErrAtLimit: another dispatcher, or a session this pass could
			// not see, holds the group's last slot.
			continue
		} else if err != nil {
			return err
		}
		delete(d.reported, b.ID)
		active[g.Name]++
		d.Log.Info("dispatching", "bead", b.ID, "stage", b.Stage, "group", g.Name, "session", sess.ID)

		// What the brief says the session may call. A brief without it is
		// still a brief: yardr call --list says the same.
		var calls []store.Callable
		if may, err := d.Store.SessionCalls(ctx, sess.ID); err != nil {
			d.Log.Warn("the calls of the session's sources", "session", sess.ID, "err", err)
		} else {
			calls = may.Calls
		}

		handle, err := runner.Start(ctx, Job{Bead: b, Group: g, Depot: depot, Session: *sess, Train: fam.Train, Base: fam.Base,
			Children: fam.Children, Next: next, Outcomes: outcomes, StageFile: stage.file, StoreFlow: stage.storeFlow, FlowMap: stage.flowMap, Calls: calls, Pull: pull})
		if isBeadBlocked(err) {
			// The runner could not safely start this bead. Keep the failure on
			// the bead, with the reason, and wait for a person to fix and release
			// it; do not count it against retries or the rest of the group.
			if endErr := d.Store.EndSession(ctx, sess.ID, "aborted", err.Error()); endErr != nil {
				d.Log.Error("end blocked session", "session", sess.ID, "err", endErr)
			}
			d.forget(sess.ID)
			if len(handle) > 0 {
				_ = d.Store.SetHandle(ctx, sess.ID, handle)
			}
			_ = d.Store.LogDispatch(ctx, b.ID, "session_blocked", actor, map[string]any{
				"session": sess.ID, "stage": b.Stage, "reason": err.Error(),
			})
			if _, holdErr := d.Store.SetHold(ctx, b.ID, true, store.Expect{}, actor); holdErr != nil {
				d.Log.Error("hold bead after blocked start", "bead", b.ID, "err", holdErr)
			}
			active[g.Name]--
			continue
		}
		if IsRuntime(err) || IsBlocked(err) {
			// Not the bead's fault: abort the session (no failure), leave
			// the bead for the next try and stop trying the group a while.
			// A blocked group (its env_file refused) is reported the same
			// way, with the reason, until someone fixes the file.
			d.runtimeDown(ctx, g, err)
			if err := d.Store.EndSession(ctx, sess.ID, "aborted", err.Error()); err != nil {
				d.Log.Error("end session", "session", sess.ID, "err", err)
			}
			d.forget(sess.ID)
			active[g.Name]--
			if len(handle) > 0 {
				_ = d.Store.SetHandle(ctx, sess.ID, handle)
			}
			continue
		}
		if err != nil {
			d.end(ctx, *sess, "failed", err.Error())
			active[g.Name]--
			// Keep what Start created so teardown can clean it up.
			if len(handle) > 0 {
				_ = d.Store.SetHandle(ctx, sess.ID, handle)
			}
			continue
		}
		d.runtimeUp(ctx, g)
		if err := d.Store.MarkRunning(ctx, sess.ID, handle); err != nil {
			return err
		}
		// A prompt the runner could not deliver yet is watched from its
		// delivery (see alive).
		if handle[store.PendingPrompt] == nil {
			d.prompted(runner, sess.ID)
		}
	}
	return nil
}

// runtimeDown notes that g's runtime could not start anything. The first
// failure of an outage is logged as runtime_down for the group, so the
// yardmaster hears of it once; later ones only lengthen the wait, doubling up
// to RuntimeBackoffMax.
func (d *Dispatcher) runtimeDown(ctx context.Context, g store.Group, err error) {
	o := d.down[g.Name]
	if o == nil {
		o = &outage{delay: d.RuntimeBackoff}
		d.down[g.Name] = o
		d.Log.Warn("runtime down; the group's beads wait", "group", g.Name, "runner", g.Runner, "err", err)
		if lerr := d.Store.LogDispatch(ctx, "", "runtime_down", actor, map[string]any{
			"group": g.Name, "runner": g.Runner, "reason": err.Error()}); lerr != nil {
			d.Log.Warn("runtime_down event", "err", lerr)
		}
	} else {
		o.delay = min(2*o.delay, d.RuntimeBackoffMax)
	}
	o.until = time.Now().Add(o.delay)
}

// runtimeUp ends g's outage, if it had one, once a Start got past the
// runtime.
func (d *Dispatcher) runtimeUp(ctx context.Context, g store.Group) {
	if d.down[g.Name] == nil {
		return
	}
	delete(d.down, g.Name)
	d.Log.Info("runtime back", "group", g.Name, "runner", g.Runner)
	_ = d.Store.LogDispatch(ctx, "", "runtime_up", actor, map[string]any{"group": g.Name, "runner": g.Runner})
}

// nextStages is where b's flow lets it go next, and the outcomes its stage
// offers, each with the group that would work it there, and where the
// stage file of the stage it is in comes from, with the map of the flow;
// nil when no flow governs b.
func (d *Dispatcher) nextStages(ctx context.Context, b store.Bead, routes []store.Route, groups map[string]store.Group) ([]NextStage, []NextOutcome, stageDoc, error) {
	ns, err := d.Store.NextStages(ctx, b.ID)
	if err != nil || ns.Flow == nil {
		return nil, nil, stageDoc{}, err
	}
	at := func(st string) NextStage {
		n := NextStage{Stage: st, Terminal: ns.Flow.IsTerminal(st)}
		moved := b
		moved.Stage = st
		if r := Match(routes, moved); r != nil && !n.Terminal {
			n.Group, n.Runner, n.Peer = r.Group, groups[r.Group].Runner, groups[r.Group].AtPeer()
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
	// The map is drawn now, as the bead is handed out, so a bead sent back
	// starts its new session with the path that brought it here.
	fm, err := d.Store.FlowMap(ctx, b.ID)
	if err != nil {
		return nil, nil, stageDoc{}, err
	}
	doc := stageDoc{file: ns.StageFile, flowMap: fm}
	if ns.Source.InStore() {
		doc.storeFlow = ns.Flow.Name
	}
	return next, outcomes, doc, nil
}

// stageDoc is where a bead's stage file comes from: the repository flow's
// file, or the store flow whose files are in the yard's home; and the map
// of the flow the stage is in.
type stageDoc struct {
	file      *store.StageFile
	storeFlow string
	flowMap   *store.FlowMap
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

// reportStranded logs one stranded event for a bead in a stage its flow
// lacks. The flow's commit is in the event but not in what makes it new:
// the base moves with every landing, and a bead still stranded under the
// same flow has nothing new to say.
func (d *Dispatcher) reportStranded(ctx context.Context, b store.Bead, sb store.StrandedBead) {
	key := "stranded|" + sb.Stage + "|" + sb.Flow
	if d.reported[b.ID] == key {
		return
	}
	d.reported[b.ID] = key
	d.Log.Warn("stranded", "bead", b.ID, "stage", sb.Stage, "flow", sb.Flow, "source", sb.Source.String())
	_ = d.Store.LogDispatch(ctx, b.ID, "stranded", actor, map[string]any{
		"stage": sb.Stage, "flow": sb.Flow, "source": sb.Source.String(),
		"reason": fmt.Sprintf("stage %q is not in flow %s (%s); the bead waits until it is advanced to a stage the flow has", sb.Stage, sb.Flow, sb.Source),
		"hint":   fmt.Sprintf("yardr bead advance %s --to <stage>", b.ID)})
}

// SortRoutes orders routes so the first match wins; see store.SortRoutes.
func SortRoutes(rs []store.Route) { store.SortRoutes(rs) }

// Match returns the first route (in SortRoutes order) that applies to b.
func Match(sorted []store.Route, b store.Bead) *store.Route { return store.MatchRoute(sorted, b) }

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
	"sync"
	"sync/atomic"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/telemetry"
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
	URL    string // as the bead's link has it
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
	// Else are the further groups of the stage's route, in order: the
	// bead is not there yet, so none is chosen, and the quota when it gets
	// there is not known. They start agents, as Group then does.
	Else []string
}

// NextOutcome is one outcome the bead's stage offers and where it leads.
type NextOutcome struct {
	Outcome string
	// NextStage is where reporting the outcome takes this bead now, with who
	// picks it up there: the stage a limit sends it to once its rounds are
	// used, not the flow's own edge.
	NextStage
	// Limit is the outcome as the bead's rounds stand for it, for an outcome
	// that has a limit: which round reporting it would be, or that its rounds
	// are used. Nil for one that has none.
	Limit *store.OutcomeNow
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
	// running into (the source down) is retried less and less often rather
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
	// Server, when set, makes sure a server answers and starts one if none
	// does (server.Connect). The daemon connected to the server that ran
	// when it started and only dials after, so a server that is gone (a
	// replace whose new build did not come up, a stop, a crash) would leave
	// it failing on a socket nothing listens on until someone restarted it:
	// Run asks from the wake stream when it notices the server is gone, and
	// after each pass that failed.
	Server func(ctx context.Context) error
	// StreamRetry is how long the wake stream and the quiet run's event
	// stream wait before they ask a server that did not answer again
	// (default 1s).
	StreamRetry time.Duration
	// Retention is how long the store keeps events, ended sessions,
	// webhook bodies and what a link remembers of its messages
	// (store.PeerKeep), and how long an unread inbox event holds them; the
	// daemon prunes on start and every PruneEvery (default 1h). Zero keeps
	// everything and never prunes.
	Retention  store.Retention
	PruneEvery time.Duration
	// PRHead tells the head commit of a train's pull request when no event
	// has told it (see rules.go); nil reads through the source's pr
	// command.
	PRHead func(ctx context.Context, q PRQuery) (string, error)

	// CI reads the checks on a commit of a train's pull request (see
	// ci.go); nil reads through the source's ci command.
	CI func(ctx context.Context, q CIQuery) (CIChecks, error)

	// DepotRepo tells the repository a depot is of at a source
	// (owner/name), "" when it is of none there: where an event makes a bead
	// by a line that names no depot (see makes.go); nil reads the depot's
	// origin remote.
	DepotRepo func(ctx context.Context, q DepotRepoQuery) (string, error)

	// IssueTries is how often a tracked issue's move is tried before it is
	// given up (default 24: with the default Backoff and BackoffMax, most of
	// a day). See issues.go.
	IssueTries int

	// PushTimeout bounds one send from a pass, which holds up routing while
	// it waits (default 10s). A picture the source does not take leaves the
	// words as much again (notify's fallbackTimeout), so a pass can wait
	// twice this. See pushes.go.
	PushTimeout time.Duration

	// TelemetryURL is where the telemetry pass posts the daily batch, and
	// TelemetryTimeout bounds that one request (default 10s, which holds
	// the pass up for no longer). Empty, the URL is the compiled-in
	// endpoint (telemetry.Endpoint), which no setting, flag or variable
	// can change: only a test sets it, to a stand-in listener, so that no
	// test reaches the real project. See telemetry.go.
	TelemetryURL     string
	TelemetryTimeout time.Duration

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
	// QuietRun runs one whole-suite run on a quiet machine for a depot and
	// says how it ended (see quiet.go); nil runs the depot's test command
	// with YARDR_LANDING=1 in a worktree of its base. Tests set it, so no
	// real suite runs.
	QuietRun func(ctx context.Context, depot store.Depot) (QuietResult, error)

	// LeadLogin starts the leader of a harness's login the yard leads for
	// a crew member at its login screen (harnesslogin.go); nil runs this
	// binary's `yardr harness login <kind> --json`. HarnessInstalled says
	// whether a kind's command is here to be logged in; nil looks on PATH.
	// Tests set both, so no harness runs.
	LeadLogin        func(ctx context.Context, kind string) (*LoginLeader, error)
	HarnessInstalled func(kind string) bool
	// leads are the logins the yard leads, by kind, and loginRests the
	// kinds whose last one ended without a login (harnesslogin.go).
	leads      map[string]*harnessLead
	loginRests map[string]loginRest

	quietMu      sync.Mutex
	quietRunning bool // a quiet whole-suite run is in flight (quiet.go)

	ruleCursor uint64 // the last event put to its hook's table (rules.go)
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
	// state is read again. ciWait is train -> the wait after a read the
	// source did not answer.
	ciRead map[string]string
	ciWait map[string]*outage

	// backupDown is the outage of the store's dated copies: the last one
	// failed, which was told once, and the next is tried at its until
	// (see backups); nil while they are made, or none is asked for.
	backupDown *outage
	// backupDownDir is the directory whose failure backupDown represents.
	// A changed backup_dir is a new target and must be tried at once.
	backupDownDir string

	// telemetryTold is the last local fault of the telemetry pass that was
	// said, so each is said once until it is gone. See telemetry.
	telemetryTold string

	// timerDue is when the earliest timer left is due, as of the last pass
	// (see timers); nil with none.
	timerDue *time.Time

	// sidingDue is when the earliest siding that can fold is due, as of
	// the last pass (see sidings); nil with none.
	sidingDue *time.Time

	// lookDue is when the dispatcher next has work due, as of the last pass;
	// nil when nothing needs a pass before the regular interval.
	// promptIdle is session (or "crew:<name>") -> when its agent was
	// first seen idle with a prompt that has not been delivered yet.
	// timing is how long the steps of the pass in progress took, nil
	// outside a pass (see passtime.go).
	lookDue    *time.Time
	promptIdle map[string]time.Time
	timing     *passTiming
	// drafts is session (or "crew:<name>") -> the text found in its
	// agent's input when yardr had something to type there. See inputHeld.
	// dialogs is the same -> the dialog of its harness that was told of
	// and has not been seen gone. See dialogAt.
	// logins is the same -> the line of the login screen it sits at that
	// was told of. See loginAt.
	// nudged is the same -> the last nudge yardr typed and when, and unsent
	// the same -> text of yardr's own found in its agent's input. See
	// ownInput.
	drafts  map[string]*draft
	dialogs map[string]string
	logins  map[string]string
	nudged  map[string]ownNudge
	unsent  map[string]*unsent

	// limitsAsked is provider -> what it last said of its limits, kept for
	// limitsMemory. See harness_limits.go.
	limitsAsked map[string]limitsAnswer
	// limitsMu guards limitsAsked and limitsWants alone: the pass reads
	// the cache through it and the background refresher stores into it,
	// running its commands outside it (limits_refresh.go). limitsWants
	// is the refresher's leased wants, and the rest its scheduler's
	// state: limitsWake nudges it, limitsCancel stops it, limitsLoop
	// says it runs, limitsRunOwned keeps it for Run's life, and
	// limitsNotify is Run's poke, called as each answer lands.
	limitsMu    sync.Mutex
	limitsWants map[string]*limitWant
	// limitsClock is the refresher's view of the dispatcher's clock,
	// stamped by each read and pass (the goroutines that may read
	// d.now()) so the scheduler and its runs never read the clock
	// itself: a clock a test moves under the daemon is the test's own
	// variable, and a background read of it would race its writes.
	limitsClock    atomic.Int64
	limitsWake     chan struct{}
	limitsCancel   context.CancelFunc
	limitsLoop     bool
	limitsLoopGen  int
	limitsRunOwned bool
	limitsNotify   func()

	// startMu guards the start workers' shared state alone (starts.go):
	// the owned set a worker records its session in for reap, and the
	// workers themselves. startInFlight is the pass's own record of the
	// one session each group has in flight, which no worker touches;
	// startDone carries their completions to the drain at the head of
	// the next pass, and the rest is Run's ownership of the workers, as
	// limitsRunOwned and limitsNotify are the refresher's.
	startMu       sync.Mutex
	startOwned    map[string]bool
	startWorkers  map[string]*startWorker
	startInFlight map[string]string // group -> session with a start in flight
	startDone     chan startCompletion
	startCtx      context.Context
	startRunOwned bool
	startNotify   func()
	// The crew's workers (crewstarts.go) share startMu and Run's
	// ownership above, and keep the same three things: crewWorkers, one
	// per member, under startMu; crewInFlight, the pass's own record of
	// the session (its memberSession.key) each member has in flight,
	// which no worker touches; and crewDone, their completions for the
	// drain at the head of the next pass.
	crewWorkers  map[string]*crewWorker
	crewInFlight map[string]string // crew member -> key of the session with a start in flight
	crewDone     chan crewCompletion
	// host is the pass's dispatcher, on a crew worker's clone (see
	// crewClone in crew.go): the state a worker shares with the pass (the
	// limits cache, the home snapshot) is reached through it, under those
	// states' own locks, never through the clone's copies. crewQueued is
	// the same clone's record of the starts its keep asked for, which the
	// pass hands in member order after the join; nil on the pass itself,
	// where a start is handed at once.
	host       *Dispatcher
	crewQueued *[]crewJob

	// clock is the time a test has set (SetClock), nil for the wall clock.
	// A test sets it while Run's goroutines read it, so it is read and
	// written atomically; see now.
	clock      atomic.Pointer[func() time.Time]
	retryAfter map[string]time.Time
	reported   map[string]map[string]string // bead -> kind -> last unroutable reason, to log once per kind
	stays      map[string]stay              // bead -> its stay in a manual group's stage (waits.go)
	// quietChecked is when the hooks were last judged quiet (hookquiet.go).
	quietChecked    time.Time
	status          map[string]string  // session -> last runner status
	unknown         map[string]string  // session -> last liveness error logged
	down            map[string]*outage // group -> its runtime's current outage
	idleSince       map[string]time.Time
	idleTold        map[string]bool // session -> session_idle raised this idle spell
	idleNudged      map[string]bool // session -> idle nudge sent this idle spell
	idleOutcomeTold map[string]bool // session -> outcome line sent this idle spell
	// still is session -> the end of its working pane as last seen, and
	// when it was first seen as it is: what stillIdle judges by.
	still map[string]stillPane
	// fresh is session -> when its agent got its first prompt, for the
	// sessions seen idle on every pass since (see stall).
	fresh map[string]time.Time
	// crewWorkHistory caches whether the current crew session has ever been
	// seen working. A missing entry is reconstructed from crew_status events
	// before startup trust can be answered, so daemon restarts keep the guard.
	crewWorkHistory map[string]bool
	// paused is group -> model -> the live session that stalled with it, or
	// waits with it for a usage limit's window, as of this pass: route starts
	// no further session with that model (see holdBack).
	paused map[string]map[string]string
	// running is how many live sessions run something on this machine
	// (store.RunsHere), over all groups, as of this pass: what route holds
	// against the yard's max_sessions.
	running int
	// home are the herdr sessions of the last pass that ran through, with
	// their handles, plus every session started since; pass is the one in
	// progress, nil outside a pass. See HomeSessions.
	homeMu sync.Mutex
	home   map[string]HomeSession
	pass   map[string]HomeSession
}

// outage is a group whose runner could not start anything: the runtime is
// down, so its beads wait, untouched, until the next try.
type outage struct {
	until time.Time
	delay time.Duration
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
	if d.PushTimeout == 0 {
		d.PushTimeout = 10 * time.Second
	}
	if d.TelemetryTimeout == 0 {
		d.TelemetryTimeout = telemetry.Timeout
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
		d.reported = map[string]map[string]string{}
		d.status = map[string]string{}
		d.unknown = map[string]string{}
		d.down = map[string]*outage{}
		d.idleSince = map[string]time.Time{}
		d.idleTold = map[string]bool{}
		d.idleNudged = map[string]bool{}
		d.idleOutcomeTold = map[string]bool{}
		d.still = map[string]stillPane{}
		d.fresh = map[string]time.Time{}
		d.promptIdle = map[string]time.Time{}
		d.drafts = map[string]*draft{}
		d.dialogs = map[string]string{}
		d.logins = map[string]string{}
		d.nudged = map[string]ownNudge{}
		d.unsent = map[string]*unsent{}
	}
	if d.crewWorkHistory == nil {
		d.crewWorkHistory = map[string]bool{}
	}
	d.startMu.Lock()
	d.initStartsLocked()
	d.initCrewStartsLocked()
	d.startMu.Unlock()
}

// PromptUnconfirmed is a handle key next to store.PendingPrompt: the runner sent
// the prompt and took it as arrived on a sign that can be wrong (the agent
// was reported working, or its pane could not be read) without seeing it
// taken. The dispatcher counts it as sent and looks again (see
// pendingPrompt).
const PromptUnconfirmed = "prompt_unconfirmed"

// PromptStarted is a handle key next to store.PendingPrompt: the runtime saw
// the agent start to work on the keys of the prompt's last delivery (herdr's
// wait on `agent prompt`). It is what tells a prompt that left the pane from
// one that was lost (see pendingPrompt).
const PromptStarted = "prompt_started"

// PromptLook is a handle key: what the runner saw of the first prompt it
// sent, and what its runtime said of it ("typed, herdr stalled"). It stays
// on the handle so the started event, and a session_stalled or
// session_idle after it, say why the prompt was or was not left pending.
const PromptLook = "prompt_look"

// TrustAccepted is a handle key: yardr answered the session's folder-trust
// dialog itself, at its start or on a later pass (see TrustAcceptor). It
// stays on the handle as the record of it.
const TrustAccepted = "trust_accepted"

// KeyAccepted is a handle key: yardr answered the session's
// use-this-API-key dialog itself, at its start or on a later pass (see
// TrustAcceptor). It stays on the handle as the record of it, beside
// TrustAccepted: the two dialogs stand at different times, so each is
// answered once.
const KeyAccepted = "key_accepted"

// OnboardingAccepted is the handle key recording that yardr answered the
// session's onboarding screen of entry i itself, at its start or on a
// later pass (see TrustAcceptor). It stays on the handle as the record of
// it. The entries stand at different times, around the use-this-API-key
// dialog, so each is answered once, under its own mark.
func OnboardingAccepted(i int) string { return "onboarding_accepted_" + strconv.Itoa(i) }

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
// again while the other side may still be at it. WaitRead is the command
// that reads how the work on bead stands where it is done, which that
// event names: whoever heard nothing for that long learns from it how to
// ask.
type Waiter interface {
	WaitLimit() time.Duration
	WaitRead(bead string) string
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
			} else {
				// The stream had lived, so the server went away mid-run:
				// ask for one again before the wait, instead of after the
				// next pass fails its retries. The first failure starts
				// nothing: boot already connected, and a starting server
				// must not be raced.
				d.ensureServer(ctx)
			}
			select {
			case <-ctx.Done():
			case <-time.After(d.streamRetry()):
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
	d.noteStart(ctx)
	// The whole suite's backstop (quiet.go): event-driven, beside the
	// reconcile loop, never judged by its tick.
	go d.runQuietTrigger(ctx)
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
	// The limits refresher (limits_refresh.go): the pass only reads its
	// cache, so its scheduler runs beside the loop for Run's life, and
	// warms the keys the yard is known to need before the first pass.
	d.startLimitsRefresher(ctx, poke)
	defer d.stopLimitsRefresher()
	d.warmLimits(ctx)
	// The start workers (starts.go, crewstarts.go): route hands each claim
	// to its group's worker, and the crew each session it keeps to its
	// member's, and they run beside this loop for Run's life and wake it as
	// each start lands. The crew's share what this sets (the context their
	// starts derive from, the poke), so it is one start for both.
	d.startStartWorkers(ctx, poke)
	defer d.stopStartWorkers()
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
			d.ensureServer(ctx)
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
		if d.lookDue != nil && (next == nil || d.lookDue.Before(*next)) {
			next = d.lookDue
		}
		if d.pushDue != nil && (next == nil || d.pushDue.Before(*next)) {
			next = d.pushDue
		}
		if d.sidingDue != nil && (next == nil || d.sidingDue.Before(*next)) {
			next = d.sidingDue
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

// ensureServer asks Server for a server that answers, since the failure
// that led here may be that none does. It costs a call where one does;
// where none did, the wake stream's next reconnect wakes the loop for a
// pass that works. A server that cannot be started is said and asked for
// again on the next failure, not on a timer of its own.
func (d *Dispatcher) ensureServer(ctx context.Context) {
	if d.Server == nil {
		return
	}
	if err := d.Server(ctx); err != nil && ctx.Err() == nil {
		d.Log.Warn("no yardr server answers, and one could not be started", "err", err)
	}
}

// streamRetry is how long a stream waits before it asks again a server
// that did not answer.
func (d *Dispatcher) streamRetry() time.Duration {
	if d.StreamRetry > 0 {
		return d.StreamRetry
	}
	return time.Second
}

// replace execs a new build of the daemon if one is installed. It is only
// called between passes, and never once ctx has ended: a daemon told to
// stop while a pass ran stops, rather than come back up as the new build.
// It waits out a start in flight on its worker: exec'ing mid-start would
// abandon a session this daemon claimed, whose handle the new build
// could only judge as an interrupted start, or (for a crew member) an
// agent the new build would find under the member's name and take for a
// second session. The completion wakes the loop, and the replacement is
// taken by the check after that pass.
// It returns true only when a test's fake exec "succeeded". A failed exec
// is logged and the old build carries on.
func (d *Dispatcher) replace(ctx context.Context) bool {
	if d.Binary == nil || ctx.Err() != nil {
		return false
	}
	if len(d.startInFlight) > 0 || len(d.crewInFlight) > 0 {
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

// noteStart logs which start this process is: daemon_replaced when a
// previous daemon exec'd this build, daemon_started on a fresh start, each
// with the build it runs, so the health check names the daemon's build
// without guessing.
func (d *Dispatcher) noteStart(ctx context.Context) {
	to := ""
	if d.Binary != nil {
		to = d.Binary.Running
	}
	if from := config.Getenv("REPLACED_FROM"); from != "" {
		config.Unsetenv("REPLACED_FROM")
		d.Log.Info("yardr daemon replaced", "from", from, "to", version.Show(to))
		if err := d.Store.LogDispatch(ctx, "", "daemon_replaced", actor,
			map[string]any{"from": from, "to": version.Show(to)}); err != nil {
			d.Log.Warn("daemon_replaced event", "err", err)
		}
		return
	}
	d.Log.Info("yardr daemon started", "build", version.Show(to))
	if err := d.Store.LogDispatch(ctx, "", "daemon_started", actor,
		map[string]any{"to": version.Show(to)}); err != nil {
		d.Log.Warn("daemon_started event", "err", err)
	}
}

// Reconcile performs one idempotent pass: reap dead sessions, record the
// tokens the stopped ones used, tear down finished ones, apply the hook rules
// to new hook events, read the CI of trains in in-pr, move the issues beads
// track, tell users outside the yard of what landed in their inboxes, route
// and start unowned ready beads, make the copy of the store that is due.
//
// Each step is marked by name as it begins, so a pass that ran past one
// Interval says where its time went (passtime.go).
func (d *Dispatcher) Reconcile(ctx context.Context) error {
	d.defaults()
	pass := d.timePass()
	defer d.endPass(pass)
	// A pass is a moment the refresher may have work by: a key that came
	// due since its scheduler last looked is started now, not when its
	// timer next fires. The pass itself still only reads the cache.
	pass.step("stamp_limits_clock")
	d.stampLimitsClock()
	pass.step("nudge_limits_refresher")
	d.nudgeLimitsRefresher()
	// Starts that finished since the last pass: their outcomes are
	// already the store's, and what they owe the pass (the outage, the
	// idle state, the prompt watch) is applied here, before reap reads
	// the sessions and before any new claim for their groups. The crew's
	// are drained beside them (a failed start counted, a started session's
	// old memory cleared), before the crew reads the store.
	pass.step("drain_starts")
	d.drainStartCompletions(ctx)
	pass.step("drain_crew_starts")
	d.drainCrewCompletions(ctx)
	pass.step("begin_home")
	d.beginHomePass()
	// One snapshot of each pane for the whole pass, whatever checks read
	// from it (see PaneCache).
	pass.step("pane_cache")
	ctx = WithPaneCache(ctx)
	d.lookDue, d.pushDue = nil, nil
	pass.step("groups")
	groups, err := d.Store.Groups(ctx)
	if err != nil {
		return err
	}
	byName := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}

	pass.step("reap")
	active, err := d.reap(ctx, byName)
	if err != nil {
		return err
	}
	pass.live(active)
	// Before teardown: a session it stops is counted from the next pass on,
	// once its agent has had the time between to write its last lines.
	pass.step("usage")
	if err := d.usage(ctx, byName); err != nil {
		return err
	}
	pass.step("teardown")
	if err := d.teardown(ctx, byName); err != nil {
		return err
	}
	pass.step("cleanup")
	if err := d.cleanup(ctx, byName); err != nil {
		return err
	}
	// Before the crew, so one a timer is for is nudged in this pass.
	pass.step("timers")
	d.timers(ctx)
	// By the same clock, and before the crew for the same reason. The two
	// share one listing of the open beads and the yard's routes.
	pass.step("pass_lists")
	beads, routes, operator, ok := d.passLists(ctx, byName)
	if ok {
		pass.step("waits")
		d.waits(ctx, byName, beads, routes, operator)
		pass.step("sidings")
		d.sidings(ctx, byName, beads, routes)
	}
	// By the same clock: a hook gone quiet tells the yardmaster once.
	pass.step("quiet_hooks")
	d.quietHooks(ctx)
	// While the upgrade gate pauses dispatch the pass still reconciles,
	// and still stamps, but the crew and the route below start nothing.
	pass.step("gate")
	paused := d.gatePaused()
	pass.step("crew")
	if err := d.crew(ctx, paused); err != nil {
		return err
	}
	// Before routing, so a train the rules move to land is dispatched to
	// its merger in this pass.
	pass.step("rules")
	if err := d.rules(ctx); err != nil {
		return err
	}
	pass.step("ci")
	if err := d.ci(ctx); err != nil {
		return err
	}
	pass.step("issues")
	if err := d.issues(ctx); err != nil {
		return err
	}
	pass.step("pushes")
	if err := d.pushes(ctx); err != nil {
		return err
	}
	// Before anything is started: what a bead from a peer starts from is
	// kept by its ref when its worktree is made.
	pass.step("bases")
	d.bases(ctx)
	pass.step("route")
	if err := d.route(ctx, byName, active, paused); err != nil {
		return err
	}
	// A pass run alone settles the starts it just handed over (starts.go,
	// and the crew's in crewstarts.go), so the steps below see their
	// sessions as a pass that started them inline did; under Run their
	// completions wake the loop instead.
	pass.step("settle_starts")
	d.settleStarts(ctx)
	pass.step("settle_crew_starts")
	d.settleCrewStarts(ctx)
	// After the work is started: a copy of a large store takes a while.
	pass.step("backups")
	d.backups(ctx)
	// Once a day at most, and bounded: a push (pushes.go) waits as long.
	pass.step("telemetry")
	d.telemetry(ctx)
	// Last: a peer that does not answer holds up nothing of this yard's,
	// and neither does a remote. What a peer's return brought in this pass
	// is fetched in it.
	pass.step("peers")
	err = d.peers(ctx)
	pass.step("fetches")
	d.fetches(ctx)
	// The pass ran: publish the herdr sessions it saw and started, then
	// stamp it, whatever the peers said.
	pass.step("end_home")
	d.endHomePass()
	pass.step("stamp")
	d.stampPass()
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

// runnerFor is the runner that started s, so a session is always judged by
// its own runner even after its group switches to another. Sessions from
// before sessions recorded their runner are recognized by their handle.
func (d *Dispatcher) runnerFor(s store.Session, groups map[string]store.Group) Runner {
	return d.Runners[runnerName(s, groups)]
}

// runnerName names the runner that started s (see runnerFor).
func runnerName(s store.Session, groups map[string]store.Group) string {
	if name := s.RunnerName(); name != "" {
		return name
	}
	return groups[s.Group].Runner
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

// now is the dispatcher's clock, time.Now unless a test sets clock. Every
// pass reads it, so it is one atomic load, and a test may set the clock
// while a pass runs.
func (d *Dispatcher) now() time.Time {
	if c := d.clock.Load(); c != nil {
		return (*c)()
	}
	return time.Now()
}

// forget drops what the dispatcher remembers about an ended session.
func (d *Dispatcher) forget(id string) {
	delete(d.status, id)
	delete(d.status, "noteheld:"+id)
	delete(d.unknown, id)
	delete(d.idleSince, id)
	delete(d.idleTold, id)
	delete(d.idleNudged, id)
	delete(d.idleOutcomeTold, id)
	delete(d.still, id)
	delete(d.fresh, id)
	delete(d.promptIdle, id)
	delete(d.drafts, id)
	delete(d.dialogs, id)
	delete(d.nudged, id)
	delete(d.unsent, id)
}

// teardown stops runner resources for sessions that ended, unless the group
// asks to keep them (config keep=true) for inspection. On the way it marks the
// count of a session whose agent is counted as due (markCountDue), having asked
// the runner once more for the id of the agent's session while its tab is still
// open (lastAgentSession).
func (d *Dispatcher) teardown(ctx context.Context, groups map[string]store.Group) error {
	ended, err := d.Store.Unstopped(ctx)
	if err != nil {
		return err
	}
	for _, s := range ended {
		g := groups[s.Group]
		r := d.runnerFor(s, groups)
		id := d.lastAgentSession(ctx, r, s)
		if r != nil && g.Config["keep"] != true && len(s.Handle) > 0 {
			if err := r.Stop(ctx, s.Handle); err != nil {
				d.Log.Warn("stop", "session", s.ID, "err", err)
				continue
			}
		}
		if err := d.markCountDue(ctx, r, s, g, id); err != nil {
			return err
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

func (d *Dispatcher) route(ctx context.Context, groups map[string]store.Group, active map[string]int, paused bool) error {
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
	// Read on every pass that has a bead to start, so a change of the
	// setting holds from the next one.
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		return err
	}
	most, capped := store.MaxSessions(settings)
	depots := map[string]*store.Depot{}
	// Beads in a stage their flow lacks: a repository flow lost the stage on
	// the base. The stage's route may well still be there, and would hand
	// the bead to its old group with the flow's first stage as the only way
	// on, so it is not routed at all until someone advances it. The pass
	// times the lookup: it decodes every open bead and, for a bead whose
	// flow is in its repository, reads that checkout with git.
	done := d.timing.call("stranded")
	lost, err := d.Store.Stranded(ctx, "")
	done()
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
		b := store.Bead{ID: rb.Bead, Stage: rb.Stage, Type: rb.Type, Depot: rb.Depot, Labels: rb.Labels, Priority: rb.Priority, Originator: rb.Originator}
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
		operator := settings[store.OperatorSetting]
		r := Match(routes, b, operator, groups)
		if r == nil {
			d.reportOnce(ctx, b, "unrouted", fmt.Sprintf("no route for stage %q type %q originator %q", b.Stage, b.Type, store.BeadOriginator(b, operator)))
			continue
		}
		// Which of the route's groups: the first whose quota condition
		// holds. A route whose groups start no agents and name no condition
		// asks nobody (chooseGroup).
		choice, ok := d.chooseGroup(ctx, b, r, groups, active, settings[store.DefaultKindSetting])
		if !ok {
			continue
		}
		g, ok := groups[choice.Group]
		runner := d.Runners[g.Runner]
		if !ok || runner == nil {
			d.reportOnce(ctx, b, "no_runner", fmt.Sprintf("group %q has no usable runner %q", choice.Group, g.Runner))
			continue
		}
		if w, waits := d.waitReason(g, b, active); waits {
			d.reportGroupWait(ctx, b, r, []waitedGroup{w})
			continue
		}
		// The yard's cap, beside the group's: after what makes the bead
		// wait for its group, so capped is said of a bead the cap alone
		// keeps from starting. At 0 the yard is paused: nothing starts,
		// whatever runs, and the event says paused instead of counting.
		if capped && store.RunsHere(g.Runner) && d.running >= most {
			reason := fmt.Sprintf(
				"the yard runs as many sessions as max_sessions (%d) allows; the bead starts when one ends, or the setting is raised (yardr settings set max_sessions <n>)", most)
			if most == 0 {
				reason = "max_sessions is 0 (paused); the bead starts when the setting is raised (yardr settings set max_sessions <n>)"
			}
			d.reportOnce(ctx, b, "capped", reason)
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
		next, outcomes, stage, err := d.nextStages(ctx, b, routes, groups, operator)
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

		if paused {
			// The upgrade gate: say what would be dispatched, and
			// claim nothing.
			d.Log.Info("would dispatch, paused by the upgrade gate",
				"bead", b.ID, "stage", b.Stage, "group", g.Name)
			continue
		}
		// Timed whole, its write included: the repository flows of the
		// bead's blockers are read inside it, before the write, and the
		// store does not say how long they took.
		done := d.timing.call("claim")
		sess, err := d.Store.Claim(ctx, b.ID, g.Name, b.Stage, actor,
			store.Routed{Route: r.ID, Passed: choice.Passed, Held: choice.Held, Unknown: choice.Unknown})
		done()
		if errors.Is(err, store.ErrTaken) || errors.Is(err, store.ErrAtLimit) {
			// ErrAtLimit: another dispatcher, or a session this pass could
			// not see, holds the group's last slot.
			continue
		} else if err != nil {
			return err
		}
		delete(d.reported, b.ID)
		// The claim holds the group's slot from here, in the store and
		// in this pass's counts alike; the next pass rebuilds both from
		// reap whatever the start comes to, so no outcome repairs them.
		active[g.Name]++
		if store.RunsHere(g.Runner) {
			d.running++
		}
		d.Log.Info("dispatching", "bead", b.ID, "stage", b.Stage, "group", g.Name, "session", sess.ID)

		// What the brief says the session may call. A brief without it is
		// still a brief: yardr call --list says the same.
		var calls []store.Callable
		if may, err := d.Store.SessionCalls(ctx, sess.ID); err != nil {
			d.Log.Warn("the calls of the session's sources", "session", sess.ID, "err", err)
		} else {
			calls = may.Calls
		}

		// Hand the finished job to the group's worker and move to the
		// next bead: the worker runs Start beside the pass (starts.go),
		// writes the outcome to the store, and posts the completion the
		// next pass drains. Nothing is queued behind it: waitReason has
		// already held this group's later beads for the busy worker,
		// and holds them until that completion is drained.
		d.handStart(ctx, startJob{
			job: Job{Bead: b, Group: g, Depot: depot, Session: *sess, Train: fam.Train, Base: fam.Base,
				Children: fam.Children, Next: next, Outcomes: outcomes, StageFile: stage.file, StoreFlow: stage.storeFlow, FlowMap: stage.flowMap, Calls: calls, Pull: pull},
			runner: runner,
		})
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
// nil when no flow governs b. Operator is whose subscription a bead with no
// originator builds on.
func (d *Dispatcher) nextStages(ctx context.Context, b store.Bead, routes []store.Route, groups map[string]store.Group, operator string) ([]NextStage, []NextOutcome, stageDoc, error) {
	// Both lookups resolve the bead's flow, from its repository when it is
	// there, which runs git at least once: the pass times them.
	done := d.timing.call("next_stages")
	ns, err := d.Store.NextStages(ctx, b.ID)
	done()
	if err != nil || ns.Flow == nil {
		return nil, nil, stageDoc{}, err
	}
	at := func(st string) NextStage {
		n := NextStage{Stage: st, Terminal: ns.Flow.IsTerminal(st)}
		moved := b
		moved.Stage = st
		if r := Match(routes, moved, operator, groups); r != nil && !n.Terminal {
			g := groups[r.Worker()]
			n.Group, n.Runner, n.Peer, n.Else = r.Worker(), g.Runner, g.AtPeer(), r.Further()
		}
		return n
	}
	next := make([]NextStage, 0, len(ns.Stages))
	for _, st := range ns.Stages {
		next = append(next, at(st))
	}
	var outcomes []NextOutcome
	for _, o := range ns.Outcomes {
		n := NextOutcome{Outcome: o.Name, NextStage: at(o.Goes)}
		if o.Max > 0 {
			n.Limit = &o
		}
		outcomes = append(outcomes, n)
	}
	// The map is drawn now, as the bead is handed out, so a bead sent back
	// starts its new session with the path that brought it here.
	done = d.timing.call("flow_map")
	fm, err := d.Store.FlowMap(ctx, b.ID)
	done()
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

// noted marks bead as reported for kind with key, and says whether it was
// already marked so. Each kind keeps its own slot, so a bead that waits
// for its group on one pass and its quota on the next reports each once,
// instead of each overwriting the other and both firing again. The claim
// clears every slot of the bead at once.
func (d *Dispatcher) noted(bead, kind, key string) bool {
	slot := d.reported[bead]
	if slot == nil {
		slot = map[string]string{}
		d.reported[bead] = slot
	}
	if slot[kind] == key {
		return true
	}
	slot[kind] = key
	return false
}

func (d *Dispatcher) reportOnce(ctx context.Context, b store.Bead, kind, reason string) {
	if d.noted(b.ID, kind, b.Stage+"|"+reason) {
		return
	}
	d.Log.Warn(kind, "bead", b.ID, "reason", reason)
	_ = d.Store.LogDispatch(ctx, b.ID, kind, actor, map[string]any{"stage": b.Stage, "reason": reason})
}

// reportStranded logs one stranded event for a bead in a stage its flow
// lacks. The flow's commit is in the event but not in what makes it new:
// the base moves with every landing, and a bead still stranded under the
// same flow has nothing new to say.
func (d *Dispatcher) reportStranded(ctx context.Context, b store.Bead, sb store.StrandedBead) {
	if d.noted(b.ID, "stranded", sb.Stage+"|"+sb.Flow) {
		return
	}
	d.Log.Warn("stranded", "bead", b.ID, "stage", sb.Stage, "flow", sb.Flow, "source", sb.Source.String())
	_ = d.Store.LogDispatch(ctx, b.ID, "stranded", actor, map[string]any{
		"stage": sb.Stage, "flow": sb.Flow, "source": sb.Source.String(),
		"reason": fmt.Sprintf("stage %q is not in flow %s (%s); the bead waits until it is advanced to a stage the flow has", sb.Stage, sb.Flow, sb.Source),
		"hint":   fmt.Sprintf("yardr bead advance %s --to <stage>", b.ID)})
}

// SortRoutes orders routes so the first match wins; see store.SortRoutes.
func SortRoutes(rs []store.Route) { store.SortRoutes(rs) }

// Match returns the first route (in SortRoutes order) that applies to b,
// with the depot template resolved. Operator is whose subscription a bead
// with no originator builds on, and groups the yard's by name.
func Match(sorted []store.Route, b store.Bead, operator string, groups map[string]store.Group) *store.Route {
	return store.MatchRoute(sorted, b, operator, groups)
}

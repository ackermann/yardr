package dispatch

import (
	"context"
	"slices"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// SetClock makes d read the time from clock, so a test can let a backoff
// elapse without waiting for it.
func SetClock(d *Dispatcher, clock func() time.Time) { d.clock = clock }

// RememberSourceAnswer keeps what the source named source said at at as
// this daemon's memory of it, as if cachedSource had asked it then. A test
// stands in with it for an answer no script can print: one that says how
// long it holds (limits.Report.HoldsFor), as the built-in machine's does.
func RememberSourceAnswer(d *Dispatcher, source string, at time.Time, r *limits.Report) {
	d.limitsMu.Lock()
	defer d.limitsMu.Unlock()
	d.initLimitsLocked()
	d.limitsAsked[source+"\x00"] = limitsAnswer{at: at, report: r}
}

// WaitLimitsIdle waits until the limits refresher has no run in flight
// and no wanted key due to start one, by the dispatcher's clock: the
// effect a test waits on instead of the command's wall time. It gives
// up after 15 seconds, leaving the test's own assertions to say what
// did not land.
func WaitLimitsIdle(d *Dispatcher) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		d.limitsMu.Lock()
		idle := true
		running := false
		for _, w := range d.limitsWants {
			if w.running {
				running = true
				idle = false
				break
			}
			if ans, ok := d.limitsAsked[w.key]; !ok || !fresh(ans, d.now(), w.age) {
				idle = false // due, not yet started
				break
			}
		}
		if !running && !d.limitsLoop {
			idle = true // no scheduler will start anything (Run has ended)
		}
		d.limitsMu.Unlock()
		if idle || time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// PrimeLimits reads kind's limits through the cache at a stop's age, as
// a stopped agent's read would, and waits for the refresh it wants to
// land, so the test's next pass reads a fresh answer. Nothing is primed
// where nothing answers: the read is unresolvable and wants nothing.
func PrimeLimits(d *Dispatcher, ctx context.Context, kind, provider string, env []string) {
	d.cachedLimits(ctx, kind, provider, limitsMemory, env)
	WaitLimitsIdle(d)
}

// PrimeRouteLimits reads group g's limits through the cache at a
// route's age, as routeQuota would (its kind, provider and env_file),
// and waits for the refresh to land.
func PrimeRouteLimits(d *Dispatcher, ctx context.Context, g store.Group, def string) {
	ReadRouteLimitsForTest(d, ctx, g, def)
	WaitLimitsIdle(d)
}

// ReadRouteLimitsForTest makes a route-age cache read without waiting for
// its refresh, for a test that checks whether a fresh read kept a lease.
func ReadRouteLimitsForTest(d *Dispatcher, ctx context.Context, g store.Group, def string) {
	kind := g.Kind(def)
	if kind == "" {
		kind = agents.DefaultKind
	}
	provider, _ := g.Config["provider"].(string)
	env, err := groupEnv(g)
	if err != nil {
		return
	}
	d.cachedLimits(ctx, kind, provider, routeLimitsMemory, env)
}

// PrimeSourceLimits reads the source named source through the cache at
// a route's age, as sourceQuota would, and waits for it to land.
func PrimeSourceLimits(d *Dispatcher, ctx context.Context, source string) {
	d.cachedSource(ctx, source, routeLimitsMemory)
	WaitLimitsIdle(d)
}

// LimitsRunsInFlight is how many keys' refreshes are running, for a
// test that waits for one key's run to land while another's still hangs.
func LimitsRunsInFlight(d *Dispatcher) int {
	d.limitsMu.Lock()
	defer d.limitsMu.Unlock()
	n := 0
	for _, w := range d.limitsWants {
		if w.running {
			n++
		}
	}
	return n
}

// WarmLimitsForTest warms the cache as Run's start does, and waits for
// the warmed keys' refreshes to land, so a test's first pass reads them.
func WarmLimitsForTest(d *Dispatcher, ctx context.Context) {
	d.warmLimits(ctx)
	WaitLimitsIdle(d)
}

// LimitsWanted is the refresher's wanted keys, sorted, for a test that
// says a read left no want (an unresolvable one) or that a lease ended.
func LimitsWanted(d *Dispatcher) []string {
	d.limitsMu.Lock()
	defer d.limitsMu.Unlock()
	out := make([]string, 0, len(d.limitsWants))
	for key := range d.limitsWants {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

// SidingDue is when the earliest siding that can fold is due, as of the
// last pass; nil with none.
func SidingDue(d *Dispatcher) *time.Time { return d.sidingDue }

// NotTakenHint begins the hint for a prompt at a reset that an agent of a
// kind did not take.
var NotTakenHint = notTakenHint

// PushReady is when a message for what landed from oldest to newest may go.
var PushReady = pushReady

// QuietMaybe judges the yard for a quiet run now, as one relevant event
// would (quiet.go).
func QuietMaybe(d *Dispatcher, ctx context.Context) { d.maybeQuiet(ctx) }

// QuietReady judges one depot for a quiet run at the threshold n, as
// maybeQuiet does for each depot, and says how many landings it counted.
func QuietReady(d *Dispatcher, ctx context.Context, depot store.Depot, n int) (bool, int, error) {
	return d.quietReady(ctx, depot, n)
}

// QuietTrigger runs the quiet run's event trigger until ctx ends.
func QuietTrigger(d *Dispatcher, ctx context.Context) { d.runQuietTrigger(ctx) }

// QuietRelevant says whether an event can change a quiet run's judging.
var QuietRelevant = quietRelevant

// QuietPackages names the packages a whole-suite output failed.
var QuietPackages = quietPackages

// QuietTests names the tests a whole-suite output failed, capped.
var QuietTests = quietTests

// QuietOutputs slices a whole-suite output per failing package, by package.
var QuietOutputs = quietOutputs

// QuietRunOutputs is what a red run keeps of its output: the packages'
// slices, or the whole output under QuietRunKey where none is named.
var QuietRunOutputs = quietRunOutputs

// QuietRunKey is the key a red run that names no package keeps its output
// under.
const QuietRunKey = quietRunKey

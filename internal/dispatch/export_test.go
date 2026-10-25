package dispatch

import (
	"context"
	"time"

	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// SetClock makes d read the time from clock, so a test can let a backoff
// elapse without waiting for it.
func SetClock(d *Dispatcher, clock func() time.Time) { d.clock = clock }

// RememberSourceAnswer keeps what the source named source said at at as
// this daemon's memory of it, as if askSource had asked it then. A test
// stands in with it for an answer no script can print: one that says how
// long it holds (limits.Report.HoldsFor), as the built-in machine's does.
func RememberSourceAnswer(d *Dispatcher, source string, at time.Time, r *limits.Report) {
	if d.limitsAsked == nil {
		d.limitsAsked = map[string]limitsAnswer{}
	}
	d.limitsAsked[source+"\x00"] = limitsAnswer{at: at, report: r}
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

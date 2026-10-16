package dispatch

import (
	"context"
	"time"
)

// SetClock makes d read the time from clock, so a test can let a backoff
// elapse without waiting for it.
func SetClock(d *Dispatcher, clock func() time.Time) { d.clock = clock }

// NotTakenHint begins the hint for a prompt at a reset that an agent of a
// kind did not take.
var NotTakenHint = notTakenHint

// PushReady is when a message for what landed from oldest to newest may go.
var PushReady = pushReady

// QuietMaybe judges the yard for a quiet run now, as one relevant event
// would (quiet.go).
func QuietMaybe(d *Dispatcher, ctx context.Context) { d.maybeQuiet(ctx) }

// QuietTrigger runs the quiet run's event trigger until ctx ends.
func QuietTrigger(d *Dispatcher, ctx context.Context) { d.runQuietTrigger(ctx) }

// QuietRelevant says whether an event can change a quiet run's judging.
var QuietRelevant = quietRelevant

// QuietPackages names the packages a whole-suite output failed.
var QuietPackages = quietPackages

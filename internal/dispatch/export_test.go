package dispatch

import "time"

// SetClock makes d read the time from clock, so a test can let a backoff
// elapse without waiting for it.
func SetClock(d *Dispatcher, clock func() time.Time) { d.clock = clock }

// NotTakenHint begins the hint for a prompt at a reset that an agent of a
// kind did not take.
var NotTakenHint = notTakenHint

// PushReady is when a message for what landed from oldest to newest may go.
var PushReady = pushReady

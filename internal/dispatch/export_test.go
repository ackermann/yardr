package dispatch

import "time"

// SetClock makes d read the time from clock, so a test can let a backoff
// elapse without waiting for it.
func SetClock(d *Dispatcher, clock func() time.Time) { d.clock = clock }

// GhCI is the CI read that asks gh, for a test with a gh of its own on PATH.
var GhCI = ghCI

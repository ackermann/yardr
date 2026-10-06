package dispatch

import (
	"time"

	"yardr.dev/yardr/internal/store"
)

// SetClock makes d read the time from clock, so a test can let a backoff
// elapse without waiting for it.
func SetClock(d *Dispatcher, clock func() time.Time) { d.clock = clock }

// PushReady is when a message for what landed from oldest to newest may go.
var PushReady = pushReady

// GhCI is the CI read that asks gh, for a test with a gh of its own on PATH.
var GhCI = ghCI

// GHEventTrain is the train a github hook event is read as being about.
func GHEventTrain(e store.Event) string { return readGHEvent(e).train }

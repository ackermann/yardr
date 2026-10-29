package hooks

import "time"

// SetClock replaces the clock, so a test can refuse past a window or sign
// within one without waiting for it.
func (rc *Receiver) SetClock(now func() time.Time) { rc.now = now }

// Wait waits for the fetches of files under way: a fetch that takes
// nothing records nothing, so its end is seen no other way.
func (rc *Receiver) Wait() { rc.fetching.Wait() }

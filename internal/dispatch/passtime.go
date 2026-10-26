package dispatch

import (
	"log/slog"
	"time"
)

// What a pass still waits on is measured by the daemon that waits (design
// 2026-10-26-yardr-frbj section 2.6 lists what is left): Reconcile marks
// each of its steps by name as it begins it, and a pass that ran past one
// Interval says in one line where its time went. A pass inside the interval
// says nothing. The line is names and numbers only, so nothing of a bead, a
// session's handle or a credential can reach the log through it.

// passTiming is how long the steps of the pass in progress took. A mark
// (step) ends the step before it and the pass's end (endPass) ends the last,
// so a pass that returns early on an error still counts the step it stopped
// in. It reads the dispatcher's clock, so a test that sets the clock decides
// what a step took and sleeps for none of it. The pass owns it, as it owns
// the maps of Dispatcher: no other goroutine reads or writes it, and a nil
// one, which is what a step run outside a pass has, records nothing.
type passTiming struct {
	now     func() time.Time
	started time.Time
	// running is the step in progress and since when; "" between the end
	// of the pass and its first mark.
	running string
	since   time.Time
	steps   []stepTime
	// calls are the store calls the pass times inside a step, in the order
	// they were first made, each summed over the pass (call).
	calls []*callTime
	// sessions is how many sessions the pass found live: what the steps
	// that ask a runner about each of them grow with.
	sessions int
}

// stepTime is how long one step of the pass took.
type stepTime struct {
	name string
	took time.Duration
}

// callTime is the time of one kind of store call, summed over the pass, and
// how many times it was made.
type callTime struct {
	name  string
	took  time.Duration
	calls int
}

// timePass starts timing the pass that begins now.
func (d *Dispatcher) timePass() *passTiming {
	p := &passTiming{now: d.now}
	p.started = p.now()
	d.timing = p
	return p
}

// step begins the step named name, and ends the one before it.
func (p *passTiming) step(name string) {
	if p == nil {
		return
	}
	now := p.now()
	p.end(now)
	p.running, p.since = name, now
}

// end ends the step in progress at now.
func (p *passTiming) end(now time.Time) {
	if p.running == "" {
		return
	}
	p.steps = append(p.steps, stepTime{name: p.running, took: now.Sub(p.since)})
	p.running = ""
}

// call times one store call a step makes: the func it returns ends the call,
// and its time is summed under name with the pass's other calls of that
// kind. The pass times the calls the design names as waits of their own
// (route's flow lookups and its claim) here, at the pass's call sites,
// because what they wait on is the store's, and the store's API does not
// move for measurement. A call's time is inside its step's, not beside it.
func (p *passTiming) call(name string) func() {
	if p == nil {
		return func() {}
	}
	start := p.now()
	return func() {
		c := p.callTo(name)
		c.took += p.now().Sub(start)
		c.calls++
	}
}

// callTo is the sum of the calls named name, started by the first of them.
func (p *passTiming) callTo(name string) *callTime {
	for _, c := range p.calls {
		if c.name == name {
			return c
		}
	}
	c := &callTime{name: name}
	p.calls = append(p.calls, c)
	return c
}

// live notes how many sessions the pass found live, from the count per
// group that reap returns.
func (p *passTiming) live(active map[string]int) {
	if p == nil {
		return
	}
	for _, n := range active {
		p.sessions += n
	}
}

// endPass ends the pass's timing. A pass that ran past Interval logs one
// Warn, since a slow pass is an anomaly and a fast one is not worth a line:
// how long it took, how many sessions it found live, each step's time in the
// order the steps ran, and the total and count of each timed store call.
func (d *Dispatcher) endPass(p *passTiming) {
	d.timing = nil
	now := p.now()
	p.end(now)
	total := now.Sub(p.started)
	if total <= d.Interval {
		return
	}
	attrs := make([]any, 0, 3+len(p.steps)+len(p.calls))
	attrs = append(attrs, slog.Duration("took", millis(total)), slog.Duration("interval", d.Interval),
		slog.Int("sessions", p.sessions))
	for _, s := range p.steps {
		attrs = append(attrs, slog.Duration(s.name, millis(s.took)))
	}
	for _, c := range p.calls {
		attrs = append(attrs, slog.Group(c.name, slog.Duration("took", millis(c.took)), slog.Int("calls", c.calls)))
	}
	d.Log.Warn("slow pass", attrs...)
}

// millis is d rounded to the millisecond, which is as fine as a pass's steps are read.
func millis(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

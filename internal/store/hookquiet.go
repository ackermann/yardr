package store

import (
	"slices"
	"time"
)

// HookQuiet is the kind of the event the daemon logs when a hook that used
// to deliver has delivered nothing for longer than its usual gap (Quiet):
// the webhook is off at the source, or cannot reach this yard. It is about
// no bead, and, like the upgrade's failure kinds, matches a yardr
// subscription on kind alone (deliverBeadless), so `yardr all
// kind=hook_quiet` carries it; it is of the class attention too, so the
// yardmaster hears it through its default --on.
const HookQuiet = "hook_quiet"

// The usual gap of a hook's deliveries (QuietGap): the median interval
// between its accepted deliveries over QuietWindow, times three, with a
// floor of QuietFloor. With fewer than two deliveries in the window, the
// last QuietLookback deliveries give the rhythm instead.
const (
	QuietFloor    = time.Hour
	QuietWindow   = 7 * 24 * time.Hour
	QuietLookback = 20
	// QuietRead caps the accepted deliveries one look at the hooks reads
	// to judge quiet by: the rhythm of the last week, for every hook.
	QuietRead = 1000
)

// QuietGap is the hook's usual gap at now from its accepted delivery times,
// in any order: the median interval between those of the last week, times
// three, with a floor of an hour. The week is where the rhythm is read
// when the hook delivers; with fewer than two deliveries in it, the last
// twenty deliveries give the rhythm instead, so a hook that went quiet
// days ago is still judged by how it delivered, and a hook that delivers
// rarely by its own long rhythm; with fewer than two ever, the floor.
func QuietGap(deliveries []time.Time, now time.Time) time.Duration {
	week := make([]time.Time, 0, len(deliveries))
	recent := make([]time.Time, 0, len(deliveries))
	for _, t := range deliveries {
		if t.After(now) {
			continue
		}
		recent = append(recent, t)
		if !t.Before(now.Add(-QuietWindow)) {
			week = append(week, t)
		}
	}
	if len(week) >= 2 {
		return gapOf(week)
	}
	if len(recent) >= 2 {
		slices.SortFunc(recent, time.Time.Compare)
		if len(recent) > QuietLookback {
			recent = recent[len(recent)-QuietLookback:]
		}
		return gapOf(recent)
	}
	return QuietFloor
}

// gapOf is three times the median interval between the delivery times,
// with a floor of an hour. At least two times are needed.
func gapOf(times []time.Time) time.Duration {
	ordered := slices.Clone(times)
	slices.SortFunc(ordered, time.Time.Compare)
	gaps := make([]time.Duration, 0, len(ordered)-1)
	for i := 1; i < len(ordered); i++ {
		if d := ordered[i].Sub(ordered[i-1]); d > 0 {
			gaps = append(gaps, d)
		}
	}
	if len(gaps) == 0 {
		return QuietFloor
	}
	slices.Sort(gaps)
	median := gaps[len(gaps)/2]
	if len(gaps)%2 == 0 {
		median = (gaps[len(gaps)/2-1] + gaps[len(gaps)/2]) / 2
	}
	return max(3*median, QuietFloor)
}

// Quiet judges the hook at now from its accepted delivery times (QuietGap
// for the rhythm): whether it is quiet, and what its usual gap is. A hook
// is quiet when it accepted a delivery before and has accepted none since
// for longer than its usual gap. One that never delivered is not quiet:
// there is no rhythm to judge it by. Whether its deliveries are refused
// (LastRefused) or blocked is the caller's to ask first: a hook the source
// reaches is not a hook the source cannot reach.
func Quiet(last *time.Time, deliveries []time.Time, now time.Time) (quiet bool, gap time.Duration) {
	gap = QuietGap(deliveries, now)
	if last == nil {
		return false, gap
	}
	// Hooks() and EventsOfKind() are separate reads. A delivery may commit
	// between them, so the event log can know of a newer acceptance than
	// the hook snapshot. Use it as the cutoff too, or that race can report a
	// hook quiet just after it delivered.
	accepted := *last
	for _, at := range deliveries {
		if !at.After(now) && at.After(accepted) {
			accepted = at
		}
	}
	return now.Sub(accepted) > gap, gap
}

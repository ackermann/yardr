package store

import (
	"bytes"
	"context"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
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
)

// quietScan caps the events one hook's walk of its kind inspects
// (EventsOfHook): the walk is newest-first over the kind's index, so a
// hook that delivers rarely is found past a busy hook's many, while the
// walk itself stays short. Tests stub it to crowd a hook out.
var quietScan = 10000

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
	// Hooks() and EventsOfHook() are separate reads. A delivery may commit
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

// HookEvents is one hook's recent events of one kind (EventsOfHook), oldest
// first. Complete says whether they are all in hand: the hook's last
// `limit` are, or the kind's index is walked to its end. When a busy
// hook's events crowd the walk past quietScan, Complete is false: what the
// hook did is unknown, and it is not judged quiet.
type HookEvents struct {
	Events   []Event `json:"events"`
	Complete bool    `json:"complete"`
}

// EventsOfHook returns the hook's most recent events of one kind, oldest
// first, at most limit of them: the deliveries (kind "hook") quiet is
// judged from, or the hook_quiet tells of the spell. It walks the kind's
// index newest-first and keeps the hook's own, so one hook's many events
// never crowd another's out of a shared read; the walk stops after
// quietScan events, and Complete says whether the hook's own are all in
// hand (see HookEvents).
func (s *Store) EventsOfHook(ctx context.Context, kind, hook string, limit int) (*HookEvents, error) {
	out := &HookEvents{}
	if limit <= 0 {
		limit = 50
	}
	if hook == "" {
		out.Complete = true
		return out, nil
	}
	err := s.read(func(tx *bolt.Tx) error {
		prefix := []byte(kind + "\x00")
		c := tx.Bucket(bEventsKd).Cursor()
		// The key after the kind's last: the one before it is that last.
		k, _ := c.Seek([]byte(kind + "\x01"))
		if k == nil {
			k, _ = c.Last()
		} else {
			k, _ = c.Prev()
		}
		scanned := 0
		for ; k != nil && bytes.HasPrefix(k, prefix) && len(out.Events) < limit && scanned < quietScan; k, _ = c.Prev() {
			scanned++
			var e Event
			if ok, err := get(tx, bEvents, string(k[len(prefix):]), &e); err != nil {
				return err
			} else if !ok {
				continue
			}
			if name, _ := e.Data["hook"].(string); name != hook {
				continue
			}
			out.Events = append(out.Events, e)
		}
		out.Complete = len(out.Events) == limit || k == nil || !bytes.HasPrefix(k, prefix)
		named(tx, out.Events)
		return nil
	})
	slices.Reverse(out.Events)
	return out, err
}

// QuietGaps judges each hook that accepted a delivery before and is neither
// blocked nor last refused: whether it delivered nothing for longer than
// its usual gap (Quiet), from its own accepted deliveries in the log. It
// returns the usual gap by hook name for the quiet ones. The log is read
// per hook (EventsOfHook), so a busy hook's deliveries never crowd
// another's rhythm out; a hook whose own the walk did not reach is not
// judged. The rhythm is read from the hook's last QuietLookback
// deliveries: a hook that delivers rarely has its whole recent rhythm in
// hand, and one that delivers that often is not quiet.
func QuietGaps(ctx context.Context, st API, hs []Hook, now time.Time) (map[string]time.Duration, error) {
	quiet := map[string]time.Duration{}
	for _, h := range hs {
		if h.Accepted == nil || h.Blocked != "" || h.LastRefused() {
			continue
		}
		read, err := st.EventsOfHook(ctx, "hook", h.Name, QuietLookback)
		if err != nil {
			return nil, err
		}
		if !read.Complete {
			continue
		}
		at := make([]time.Time, 0, len(read.Events))
		for _, e := range read.Events {
			at = append(at, e.At)
		}
		if is, gap := Quiet(h.Accepted, at, now); is {
			quiet[h.Name] = gap
		}
	}
	return quiet, nil
}

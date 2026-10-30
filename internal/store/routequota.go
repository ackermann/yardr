package store

import "time"

// lastKnownNote is what a failed comparison says of a window read as
// last known: a window used up until a reset still ahead says so, and
// any other window of a recent report is only the last known.
func lastKnownNote(w QuotaWindow, now time.Time) string {
	if w.UsedPercent != nil && *w.UsedPercent >= 100 && w.ResetsAt != nil && w.ResetsAt.After(now) {
		return " (last known: used up until " + Clock(*w.ResetsAt, now) + ")"
	}
	return " (last known)"
}

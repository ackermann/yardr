package store

import (
	"slices"
	"time"
)

// StartsAgents reports whether the sessions of g are agents, which burn a
// provider: a group of a kind (def being the yard's default_kind, Kind), or
// a herdr group that names none. A route's condition reads the windows of
// such a group's provider; a group that starts no agents has none.
func (g Group) StartsAgents(def string) bool {
	return g.Kind(def) != "" || g.Runner == "herdr"
}

// withProviderWindows is a group's condition, parsed, as Choose judges it.
// One that names no window of the group's provider, only windows of sources
// (machine.load<80), is also asked what a group with no condition is
// (noWindowUsedUp): the provider's windows go on gating at 100%, since a
// session started into one stops on the limit at once, and the sources' do
// not replace them. One that names any window of the provider, or every
// window, says which of them gate, and no other does.
func withProviderWindows(parsed []QuotaLimit) []QuotaLimit {
	if slices.ContainsFunc(parsed, func(l QuotaLimit) bool { return l.Source == "" }) {
		return parsed
	}
	return slices.Concat(parsed, noWindowUsedUp)
}

// lastKnownNote is what a failed comparison says of a window read as
// last known: a window used up until a reset still ahead says so, and
// any other window of a recent report is only the last known.
func lastKnownNote(w QuotaWindow, now time.Time) string {
	if w.UsedPercent != nil && *w.UsedPercent >= 100 && w.ResetsAt != nil && w.ResetsAt.After(now) {
		return " (last known: used up until " + Clock(*w.ResetsAt, now) + ")"
	}
	return " (last known)"
}

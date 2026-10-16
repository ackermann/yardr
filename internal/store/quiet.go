package store

import "strconv"

// The whole-suite run on a quiet machine (docs/testing.md R6) is the
// daemon's, and these are the events it tells it with. A run is about no
// bead: it tests a depot's base, so, like the upgrade's failure kinds, it
// matches a yardr subscription on kind alone (deliverBeadless). QuietFailed
// is of the class attention too, so the yardmaster hears of a red base
// through its default --on: the base is red with no bead to send back, and
// somebody must be told.
const (
	QuietStarted = "quiet_started"
	QuietPassed  = "quiet_passed"
	QuietFailed  = "quiet_failed"
)

// QuietLandingsSetting is how many code landings on a depot's base pass
// before the whole suite is owed again on a quiet machine.
const QuietLandingsSetting = "quiet_landings"

// DefaultQuietLandings is the threshold while the setting is unset: ten
// landings, about two hours of landings at the 60-140 a day the yard saw
// on 14-16 Oct 2026 (docs/design/2026-10-16-yardr-635g-test-time.md
// section 2.2), so a quiet spell is used within hours, not days, and a
// busy spell is not chased landing by landing.
const DefaultQuietLandings = 10

// QuietLandings is the quiet_landings of settings: how many code landings
// since the last whole-suite run arm the next one. An unset, unreadable or
// negative value reads as the default; 0 turns the quiet run off, the
// whole suite then running only where someone runs it (YARDR_LANDING=1).
func QuietLandings(settings map[string]string) int {
	v, ok := settings[QuietLandingsSetting]
	if !ok || v == "" {
		return DefaultQuietLandings
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return DefaultQuietLandings
	}
	return n
}

package store

import "time"

// Clock is a time as a person reads it on the day of now: "00:02", and with
// the day for a time that is not today, "Sat 10 Oct 09:02". Both are local.
func Clock(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Mon 2 Jan 15:04")
}

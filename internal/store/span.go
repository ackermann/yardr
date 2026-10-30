package store

import (
	"fmt"
	"time"
)

// RoughSpan is a length of time as prose reads it, in its two largest
// units: 42m, 3h 5m, 3d 9h. It rounds to the minute; under one reads as
// under a minute. Span beside it prints a duration as ParseSpan reads it.
func RoughSpan(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d/time.Hour)%24, int(d/time.Minute)%60
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	case mins > 0:
		return fmt.Sprintf("%dm", mins)
	}
	return "under a minute"
}

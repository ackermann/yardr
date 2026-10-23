// Package testload sizes the waits of a test to the machine it runs on. A
// fuse against a hang, or the deadline of a poll, is written for an idle box;
// the gate, which already tells a loaded machine from a quiet one, gives the
// race leg of a run a factor (YARDR_TEST_LOAD), and Scale makes the wait that
// many times longer. A wait that is met returns early, so only a red test
// burns the whole of a scaled one: the factor costs a green run nothing.
// What a test measures does not go through here: a deadline that is an
// input of the program under test is the test's to set, and the pause
// between two looks is no fuse.
package testload

import (
	"math"
	"strconv"
	"time"

	"yardr.dev/yardr/internal/config"
)

// Env names the variable in which the gate gives a run its factor, as
// config.Getenv takes the name: YARDR_TEST_LOAD, a decimal number from 1 up.
const Env = "TEST_LOAD"

// factor is the run's factor: 1 where the variable is missing or is no number
// from 1 up. A factor under 1 would cut a fuse short of its idle-box value,
// and an infinite one would leave a hang to the test binary's own timeout, so
// neither is taken for what it says. The gate never sets either.
func factor() float64 {
	f, err := strconv.ParseFloat(config.Getenv(Env), 64)
	if err != nil || !(f >= 1) || math.IsInf(f, 1) {
		return 1
	}
	return f
}

// Scale is d made as many times longer as the run's factor says. A wait that
// is not positive is returned as it is, and a product too long for a
// Duration stops at the longest one: a conversion that wrapped would give a
// negative wait, which ends at once.
func Scale(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	scaled := float64(d) * factor()
	if scaled >= math.MaxInt64 {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(scaled)
}

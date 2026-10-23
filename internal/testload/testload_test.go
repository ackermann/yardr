package testload

import (
	"math"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
)

// The gate exports YARDR_TEST_LOAD (.yardr/test), so the name is a contract.
func TestEnvIsTheGatesVariable(t *testing.T) {
	if got := config.Key(Env); got != "YARDR_TEST_LOAD" {
		t.Fatalf("the variable is %s, want YARDR_TEST_LOAD", got)
	}
}

func TestScale(t *testing.T) {
	const wait = 10 * time.Second
	for _, c := range []struct {
		name  string
		value string
		d     time.Duration
		want  time.Duration
	}{
		{"unset", "", wait, wait},
		{"one", "1", wait, wait},
		{"a fraction of a core over", "2.5", wait, 25 * time.Second},
		{"the gate's cap", "4", wait, 40 * time.Second},
		{"a whole minute", "4.0", time.Minute, 4 * time.Minute},
		{"no number", "heavy", wait, wait},
		{"a number and more", "2x", wait, wait},
		{"not a number", "NaN", wait, wait},
		{"infinite", "Inf", wait, wait},
		{"under one", "0.5", wait, wait},
		{"zero", "0", wait, wait},
		{"negative", "-3", wait, wait},
		{"too long for a duration", "1e300", wait, time.Duration(math.MaxInt64)},
		{"no wait", "3", 0, 0},
		{"a wait before now", "3", -time.Second, -time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(config.Key(Env), c.value)
			if got := Scale(c.d); got != c.want {
				t.Errorf("Scale(%s) with YARDR_TEST_LOAD=%q = %s, want %s", c.d, c.value, got, c.want)
			}
		})
	}
}

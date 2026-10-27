// Package testload sizes the waits of a test to the machine it runs on. A
// fuse against a hang, or the deadline of a poll, is written for an idle box;
// Scale makes the wait as many times longer as the machine is loaded, by the
// larger of two factors: the one the gate gives the race leg (YARDR_TEST_LOAD),
// read as the leg started, and the machine's own, read as the wait is armed. A
// leg that starts on a quiet box and meets a load of fifty on the way is sized
// by the second, and so is a run the gate gave no factor. A wait that is met
// returns early, so only a red test burns the whole of a scaled one: the factor
// costs a green run nothing. What a test measures does not go through here: a
// deadline that is an input of the program under test is the test's to set, and
// the pause between two looks is no fuse. Loaded is the same factor read as a
// verdict, for the rare test that must tell a slow machine from a slow program.
package testload

import (
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
)

// Env names the variable in which the gate gives a run its factor, as
// config.Getenv takes the name: YARDR_TEST_LOAD, a decimal number from 1 up.
const Env = "TEST_LOAD"

// The variables that pin the machine, as config.Getenv takes the names:
// YARDR_TEST_LOADAVG and YARDR_TEST_CORES stand in for its one-minute load
// average and its core count, as they do for the gate's own tests (machineload
// in .yardr/test), so that what a test finds does not depend on what else the
// machine is running.
const (
	loadavgEnv = "TEST_LOADAVG"
	coresEnv   = "TEST_CORES"
)

// maxFactor is the most the machine's own load widens a wait: the cap the gate
// puts on its factor (sizewaits in .yardr/test), so that one rule sizes a wait
// whichever of the two it comes from.
const maxFactor = 4

// machineLoad reads the machine's one-minute load average and its core count;
// ok is false where either cannot be read. It is a variable for the tests of
// this package to replace; a test of any other suite pins the machine with the
// variables above.
var machineLoad = readMachine

// factor is the run's factor: the larger of the gate's, which it gave the race
// leg as the leg started, and the machine's as it is now. Never the smaller: a
// leg that began in a load keeps its factor when the load has gone, and a
// machine that has become loaded since widens a wait the gate sized to a quiet
// one.
func factor() float64 {
	return max(gateFactor(), machineFactor())
}

// gateFactor is the factor the gate gave the run: 1 where the variable is
// missing or is no number from 1 up. A factor under 1 would cut a fuse short of
// its idle-box value, and an infinite one would leave a hang to the test
// binary's own timeout, so neither is taken for what it says. The gate never
// sets either.
func gateFactor() float64 {
	f, err := strconv.ParseFloat(config.Getenv(Env), 64)
	if err != nil || !(f >= 1) || math.IsInf(f, 1) {
		return 1
	}
	return f
}

// machineFactor is the machine's load over its cores as it reads now, from 1 to
// 4, the gate's own rule. It is read at each call and kept nowhere: a reading
// kept is the load of an earlier minute, the staleness this is for. A machine
// that is quiet, or whose load cannot be read, is 1: an unknown load never
// widens a wait, as the gate's does not. The reading is checked here as well as
// where it is taken, since a test may replace it with any.
func machineFactor() float64 {
	avg, cores, ok := machineLoad()
	if !ok || cores == 0 || !(avg >= 0) || math.IsInf(avg, 1) {
		return 1
	}
	return min(max(avg/float64(cores), 1), maxFactor)
}

// Loaded reports whether the run's factor is above 1: the gate found the
// machine loaded when it gave the leg its factor, or its load is past its cores
// as this is asked. A test that skips what only load explains (a browser that
// missed its deadline) asks this, so that one measure is the one every suite
// shares. A load that cannot be read excuses nothing, as it passes no package
// at the gate.
func Loaded() bool {
	return factor() > 1
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

// readMachine is the machine as the gate reads it (machineload in .yardr/test):
// /proc/loadavg where the system keeps one, sysctl's vm.loadavg elsewhere, and
// the cores Go counts (its nproc and hw.ncpu). The pins stand in for both, so
// with one set and not the other the reading is unknown, as there.
func readMachine() (avg float64, cores uint64, ok bool) {
	text, count := "", strconv.Itoa(runtime.NumCPU())
	if a, c := config.Getenv(loadavgEnv), config.Getenv(coresEnv); a != "" || c != "" {
		text, count = a, c
	} else if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		text = string(b)
	} else if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
		text = string(out)
	}
	avg, ok = loadAverage(text)
	cores, err := strconv.ParseUint(strings.TrimSpace(count), 10, 64)
	return avg, cores, ok && err == nil && cores > 0
}

// loadAverage is the first number of what the system prints for the load:
// /proc/loadavg ("0.52 0.58 0.59 1/467 12345"), sysctl -n vm.loadavg on darwin
// ("{ 3.52 3.61 3.40 }"), or a bare number. A number no machine could have,
// one that is negative or not finite, is no reading.
func loadAverage(text string) (float64, bool) {
	f := strings.Fields(strings.Trim(text, "{} \n"))
	if len(f) == 0 {
		return 0, false
	}
	avg, err := strconv.ParseFloat(f[0], 64)
	return avg, err == nil && avg >= 0 && !math.IsInf(avg, 0)
}

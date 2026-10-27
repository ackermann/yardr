package testload

import (
	"math"
	"os"
	"runtime"
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

// reading is what the machine reads, for a test to stand in for it: the zero
// value is a machine whose load cannot be read.
type reading struct {
	avg   float64
	cores uint64
	ok    bool
}

// stubMachine makes the machine read as r for the rest of the test, and gives
// the reading back to change in place, as the machine's load changes.
func stubMachine(t *testing.T, r reading) *reading {
	t.Helper()
	now, was := &r, machineLoad
	machineLoad = func() (float64, uint64, bool) { return now.avg, now.cores, now.ok }
	t.Cleanup(func() { machineLoad = was })
	return now
}

func TestScale(t *testing.T) {
	const wait = 10 * time.Second
	// check is the whole path of a factor for one wait: the gate's variable,
	// what the machine reads, and the wait made longer by the larger of the two.
	check := func(t *testing.T, value string, m reading, d, want time.Duration) {
		t.Helper()
		t.Setenv(config.Key(Env), value)
		stubMachine(t, m)
		if got := Scale(d); got != want {
			t.Errorf("Scale(%s) with YARDR_TEST_LOAD=%q and the machine reading %+v = %s, want %s", d, value, m, got, want)
		}
		// Loaded is the same factor read as a verdict: it is above 1 where
		// a wait is made longer, and a value Scale does not take is none.
		if got, want := Loaded(), Scale(wait) > wait; got != want {
			t.Errorf("Loaded() with YARDR_TEST_LOAD=%q and the machine reading %+v = %v, want %v (Scale(%s) = %s)", value, m, got, want, wait, Scale(wait))
		}
	}

	// The gate's variable alone: a machine whose load cannot be read adds
	// nothing, so what is here is what the variable says.
	for _, c := range []struct {
		name  string
		value string
		d     time.Duration
		want  time.Duration
	}{
		{"unset", "", wait, wait},
		{"one", "1", wait, wait},
		{"a tenth over", "1.1", wait, 11 * time.Second},
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
		t.Run(c.name, func(t *testing.T) { check(t, c.value, reading{}, c.d, c.want) })
	}

	// The machine's own load, with and without the gate's: the larger of the
	// two, from 1 to 4 where it is the machine's, and nothing where it cannot
	// be read (yardr-hsvx).
	for _, c := range []struct {
		name    string
		value   string // the gate's YARDR_TEST_LOAD
		machine reading
		want    time.Duration
	}{
		{"a loaded machine and no variable", "", reading{25, 10, true}, 25 * time.Second},
		{"a variable under the machine's", "2", reading{35, 10, true}, 35 * time.Second},
		{"a variable over the machine's", "3", reading{20, 10, true}, 30 * time.Second},
		{"a variable equal to the machine's", "2.5", reading{25, 10, true}, 25 * time.Second},
		{"a quiet machine and no variable", "", reading{1.5, 10, true}, wait},
		{"a quiet machine does not shrink the variable", "2.5", reading{0.5, 10, true}, 25 * time.Second},
		{"as much load as cores", "", reading{10, 10, true}, wait},
		{"a tenth past the cores", "", reading{11, 10, true}, 11 * time.Second},
		{"the machine's cap", "", reading{100, 10, true}, 40 * time.Second},
		{"just past the cap", "", reading{41, 10, true}, 40 * time.Second},
		{"the cap and the variable's", "4", reading{100, 10, true}, 40 * time.Second},
		{"a load that cannot be read", "2.5", reading{}, 25 * time.Second},
		{"a load that cannot be read and no variable", "", reading{}, wait},
		{"numbers the machine did not give", "2", reading{50, 10, false}, 20 * time.Second},
		{"a load on no cores", "", reading{25, 0, true}, wait},
		{"a load that is no number", "", reading{math.NaN(), 10, true}, wait},
		{"a load that is infinite", "", reading{math.Inf(1), 10, true}, wait},
		{"a negative load", "", reading{-5, 10, true}, wait},
		{"a variable that is no number", "heavy", reading{25, 10, true}, 25 * time.Second},
		{"a variable under one and a quiet machine", "0.5", reading{1, 10, true}, wait},
	} {
		t.Run(c.name, func(t *testing.T) { check(t, c.value, c.machine, wait, c.want) })
	}

	// The machine's factor on its own is the gate's rule, the load over the
	// cores from 1 to 4. Scale cannot show the floor, since the gate's factor
	// is never under 1 and wins over a lower one.
	t.Run("the machine's own factor", func(t *testing.T) {
		now := stubMachine(t, reading{})
		for _, c := range []struct {
			machine reading
			want    float64
		}{
			{reading{}, 1},
			{reading{0.3, 10, true}, 1},
			{reading{10, 10, true}, 1},
			{reading{25, 10, true}, 2.5},
			{reading{40, 10, true}, 4},
			{reading{100, 10, true}, 4},
		} {
			*now = c.machine
			if got := machineFactor(); got != c.want {
				t.Errorf("machineFactor() with the machine reading %+v = %v, want %v", c.machine, got, c.want)
			}
		}
	})

	// The machine is read at every call: a fuse armed after the load has come
	// is sized to it, and one armed after it has gone is not. A reading kept
	// would give the second the load of the first, which is the staleness this
	// package is for.
	t.Run("read at every call", func(t *testing.T) {
		t.Setenv(config.Key(Env), "")
		now := stubMachine(t, reading{})
		for _, step := range []struct {
			machine reading
			want    time.Duration
		}{
			{reading{}, wait},
			{reading{25, 10, true}, 25 * time.Second},
			{reading{40, 10, true}, 40 * time.Second},
			{reading{5, 10, true}, wait},
			{reading{}, wait},
		} {
			*now = step.machine
			if got := Scale(wait); got != step.want {
				t.Errorf("Scale(%s) with the machine reading %+v = %s, want %s", wait, step.machine, got, step.want)
			}
		}
	})

	// A machine a test pins reaches Scale through the reading the package takes
	// by default, which the stubs above stand in for: a loaded one widens a
	// wait, and a quiet one is a quiet one whatever the real machine is doing.
	t.Run("a pinned machine", func(t *testing.T) {
		t.Setenv(config.Key(Env), "")
		t.Setenv(config.Key(loadavgEnv), "25")
		t.Setenv(config.Key(coresEnv), "10")
		if got := Scale(wait); got != 25*time.Second {
			t.Errorf("Scale(%s) with the machine pinned at load 25 on 10 cores = %s, want %s", wait, got, 25*time.Second)
		}
		if !Loaded() {
			t.Error("Loaded() with the machine pinned at load 25 on 10 cores is false")
		}
		t.Setenv(config.Key(loadavgEnv), "1.5")
		if got := Scale(wait); got != wait {
			t.Errorf("Scale(%s) with the machine pinned at load 1.5 on 10 cores = %s, want %s", wait, got, wait)
		}
		if Loaded() {
			t.Error("Loaded() with the machine pinned at load 1.5 on 10 cores is true")
		}
	})
}

// The machine a test pins is the one this package reads: the pins stand in for
// both numbers, in whichever form the system prints the load, and a pin that is
// no reading is none, so that the real machine never shows through a test that
// meant to hide it.
func TestReadMachineWithPins(t *testing.T) {
	for _, c := range []struct {
		name       string
		avg, cores string
		want       reading
	}{
		{"loaded", "72.4", "10", reading{72.4, 10, true}},
		{"quiet", "0.7", "8", reading{0.7, 8, true}},
		{"idle", "0", "4", reading{0, 4, true}},
		{"as /proc/loadavg prints it", "0.52 0.58 0.59 1/467 12345\n", "10", reading{0.52, 10, true}},
		{"as sysctl prints it", "{ 3.52 3.61 3.40 }\n", "10", reading{3.52, 10, true}},
		{"as nproc prints it", "72.4", " 10\n", reading{72.4, 10, true}},
		{"no load", "", "10", reading{}},
		{"no cores", "72.4", "", reading{}},
		{"no number", "unknown", "10", reading{}},
		{"not a number", "NaN", "10", reading{}},
		{"infinite", "Inf", "10", reading{}},
		{"negative", "-1", "10", reading{}},
		{"braces and nothing", "{ }", "10", reading{}},
		{"no cores at all", "72.4", "0", reading{}},
		{"a fraction of a core", "72.4", "2.5", reading{}},
		{"negative cores", "72.4", "-2", reading{}},
		{"cores in words", "72.4", "ten", reading{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(config.Key(loadavgEnv), c.avg)
			t.Setenv(config.Key(coresEnv), c.cores)
			avg, cores, ok := readMachine()
			if ok != c.want.ok || ok && (avg != c.want.avg || cores != c.want.cores) {
				t.Errorf("readMachine() at load %q on %q cores = %v, %v, %v, want %+v", c.avg, c.cores, avg, cores, ok, c.want)
			}
		})
	}
}

// Where the yard runs, the system's own reading comes through: a reader that
// never found one would leave every machine quiet, and no wait would ever be
// widened by the load.
func TestSystemLoadIsRead(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
	case "linux":
		if _, err := os.Stat("/proc/loadavg"); err != nil {
			t.Skip("this linux keeps no /proc/loadavg")
		}
	default:
		t.Skip("the yard runs on linux and darwin")
	}
	t.Setenv(config.Key(loadavgEnv), "")
	t.Setenv(config.Key(coresEnv), "")
	avg, cores, ok := readMachine()
	if !ok || avg < 0 || cores != uint64(runtime.NumCPU()) {
		t.Errorf("the system's load reading is %v on %v cores, ok=%v", avg, cores, ok)
	}
}

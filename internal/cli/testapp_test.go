package cli

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// testEnv is the environment of a run a test makes on the yard in home ("" for
// a run whose config the test hands its app): the home, the setting
// variables blank as TestMain blanks them in the process, and env as
// KEY=VALUE besides. The run reads this map and, from the process, only its
// process facts (see processFacts), so the test sets nothing on the process
// and may run beside others.
func testEnv(t *testing.T, home string, env ...string) map[string]string {
	t.Helper()
	m, err := testEnvOf(home, env...)
	if err != nil {
		t.Fatalf("testEnv: %v", err)
	}
	return m
}

// testEnvOf is testEnv's map, or why a test may not have it. A process fact
// in env is refused: the map never carries one, and a copy there would
// disagree with what the packages below the CLI read from the process. A
// test that sets one does it with t.Setenv, and runs alone.
func testEnvOf(home string, env ...string) (map[string]string, error) {
	m := map[string]string{"YARDR_WEB_ADDR": "", "YARDR_BDP_ADDR": ""}
	if home != "" {
		m["YARDR_HOME"] = home
	}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		switch {
		case !ok:
			return nil, fmt.Errorf("%q is not KEY=VALUE", kv)
		case slices.Contains(processFacts, k):
			return nil, fmt.Errorf("%s is a process fact, which the packages below the CLI read from the process: set it with t.Setenv, and the test runs alone", k)
		}
		m[k] = v
	}
	return m, nil
}

// testYard is testEnv as the environment of the runs on st that the helpers
// taking the store alone make (as, onboardTo; see appOn), and returns it.
func testYard(t *testing.T, st store.API, home string, env ...string) map[string]string {
	t.Helper()
	m := testEnv(t, home, env...)
	rememberEnv(t, st, m)
	return m
}

// testApp is an app on st in testYard's environment.
func testApp(t *testing.T, st store.API, home string, env ...string) *app {
	t.Helper()
	return &app{st: st, env: testYard(t, st, home, env...)}
}

// yardEnvs are the environments of a store's runs, by store, for the helpers
// that take a store and nothing more (as, mustAs, onboardTo): a test names
// its yard's environment once, with testYard (testApp does) or yardEnv, and
// calls them as it did. A run on a store with none reads the process's.
var yardEnvs sync.Map // store.API -> map[string]string

// envOf is the environment of st's runs, if a test named one. A store that
// cannot be a map's key (a struct holding a slice, by value) has none.
func envOf(st store.API) (map[string]string, bool) {
	if st == nil || !reflect.TypeOf(st).Comparable() {
		return nil, false
	}
	env, ok := yardEnvs.Load(st)
	if !ok {
		return nil, false
	}
	return env.(map[string]string), true
}

// rememberEnv makes env that of st's runs until the test ends.
func rememberEnv(t *testing.T, st store.API, env map[string]string) {
	t.Helper()
	if st == nil {
		return
	}
	if !reflect.TypeOf(st).Comparable() {
		t.Fatalf("the store %T cannot name the environment of its runs: it is not comparable", st)
	}
	yardEnvs.Store(st, env)
	t.Cleanup(func() { yardEnvs.Delete(st) })
}

// yardEnv is the environment of the runs on st that the helpers taking it
// alone make, to read and change in place of t.Setenv: what testYard gave the
// yard, or, for a yard whose home the process still names (userYard), the
// process's as it is at this call.
func yardEnv(t *testing.T, st store.API) map[string]string {
	t.Helper()
	if env, ok := envOf(st); ok {
		return env
	}
	env := environMap(os.Environ())
	rememberEnv(t, st, env)
	return env
}

// appOn is a new app on st, in the environment of its runs (see yardEnv): a
// new one for each command, as each run of yardr is.
func appOn(st store.API) *app {
	a := &app{st: st}
	if env, ok := envOf(st); ok {
		a.env = env
	}
	return a
}

// A test's environment holds what it names and the blanks, and never a
// process fact: that is read from the process, where the map's copy would
// say something else.
func TestTestEnvRefusesAProcessFact(t *testing.T) {
	t.Parallel()
	for _, fact := range processFacts {
		if _, err := testEnvOf("/y", fact+"=x"); err == nil || !strings.Contains(err.Error(), fact+" is a process fact") {
			t.Errorf("%s: %v, want it refused", fact, err)
		}
	}
	if _, err := testEnvOf("/y", "USER"); err == nil {
		t.Error("an entry with no value was taken")
	}
	got, err := testEnvOf("/y", "USER=kim", "HERDR_ENV=", "GITHUB_TOKEN=t")
	want := map[string]string{"YARDR_HOME": "/y", "YARDR_WEB_ADDR": "", "YARDR_BDP_ADDR": "", "USER": "kim", "HERDR_ENV": "", "GITHUB_TOKEN": "t"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, %v; want %v", got, err, want)
	}
	if got, _ := testEnvOf(""); got["YARDR_HOME"] != "" || len(got) != 2 {
		t.Errorf("with no home the env names none: %v", got)
	}
}

// The helpers that take a store alone run in the environment its test named,
// and in the process's where it named none; another yard's environment is
// not theirs.
func TestAppOnRunsInTheYardsEnvironment(t *testing.T) {
	t.Parallel()
	st, other := storetest.New(t), storetest.New(t)
	if a := appOn(st); a.env != nil || a.st != st {
		t.Fatalf("a store with no environment named: app %+v", a)
	}
	a := testApp(t, st, "/y", "YARDR_CREW=yardmaster")
	if got := appOn(st); got == a || got.st != st || got.getenv("YARDR_CREW") != "yardmaster" || got.yardrenv("HOME") != "/y" {
		t.Fatalf("the yard's runs: %+v, want a new app in %v", got, a.env)
	}
	if appOn(other).env != nil {
		t.Error("another yard's runs have this yard's environment")
	}
	yardEnv(t, st)["YARDR_CREW"] = ""
	if got := appOn(st).getenv("YARDR_CREW"); got != "" || a.getenv("YARDR_CREW") != "" {
		t.Errorf("a change to the yard's environment is not seen: %q, %q", got, a.getenv("YARDR_CREW"))
	}
	// A yard with no environment named gets the process's, as of the call
	// that asks, to change in its place.
	env := yardEnv(t, other)
	env["YARDR_CREW"] = "clerk"
	if appOn(other).getenv("YARDR_CREW") != "clerk" || len(env) < 2 {
		t.Errorf("the process's environment as a yard's: %v", env)
	}
}

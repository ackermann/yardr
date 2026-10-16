package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// failing is a store whose write named by on fails: "setting <key>" or
// "crew". It is how a test cuts init short after the step before.
type failing struct {
	store.API
	on string
}

var errCut = errors.New("cut short")

func (f *failing) SetSetting(ctx context.Context, key, value, actor string) error {
	if f.on == "setting "+key {
		return errCut
	}
	return f.API.SetSetting(ctx, key, value, actor)
}

func (f *failing) PutCrew(ctx context.Context, def store.CrewMember, actor string) (*store.CrewMember, error) {
	if f.on == "crew" {
		return nil, errCut
	}
	return f.API.PutCrew(ctx, def, actor)
}

// startsYardmaster replaces the daemon's start with one that counts and,
// given a handle, records the session of the crew member named as the daemon
// would.
func startsYardmaster(t *testing.T, st store.API, name string, handle map[string]any) *int {
	t.Helper()
	n := 0
	stub(t, &spawnDaemon, func(*config.Config) (<-chan error, error) {
		n++
		if handle != nil {
			if err := st.CrewStarted(context.Background(), name, handle); err != nil {
				t.Errorf("%s not started: %v", name, err)
			}
		}
		return nil, nil
	})
	return &n
}

// wholeYard fails unless the home holds the yard a clean init makes: the
// three settings, the yardmaster on the harness, and its role from the
// default pack, naming the operator and the yardmaster, as its prompt.
func wholeYard(t *testing.T, st store.API, home, harness, operator, yardmaster string) {
	t.Helper()
	ctx := context.Background()
	got, _ := st.Settings(ctx)
	if got["default_kind"] != harness || got["operator"] != operator || got["yardmaster"] != yardmaster {
		t.Errorf("settings = %v", got)
	}
	dir := config.At(home).PackRoles("starter")
	rs, _ := st.Crew(ctx)
	if len(rs) != 1 || rs[0].Name != yardmaster || rs[0].Config["kind"] != harness || rs[0].Config["prompt"] != filepath.Join(dir, "yardmaster.md") {
		t.Errorf("crew members = %+v", rs)
	}
	have, err := os.ReadFile(filepath.Join(dir, "yardmaster.md"))
	if err != nil {
		t.Errorf("yardmaster.md: %v", err)
	} else if !bytes.Equal(have, packRole(t, "starter", "roles/yardmaster.md", operator, yardmaster)) {
		t.Errorf("yardmaster.md does not name %s and %s", operator, yardmaster)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
	if _, err := os.Stat(filepath.Join(home, "roles")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("init made %s: %v", filepath.Join(home, "roles"), err)
	}
}

// Init cut short after each of its steps leaves what it wrote; init again
// finishes the yard from it, says what it found and what it added, and takes
// what the first run stored for the flags the second does not give.
func TestInitFinishesAfterAFailedRun(t *testing.T) {
	first := []string{"init", "--harness", "codex", "--operator", "Jo", "--yardmaster", "boss"}
	for _, tc := range []struct {
		name string
		// on is the store's write that fails; "" for the role, where a
		// directory in the prompt's place fails the read of it.
		on    string
		again []string
		// setUp: the home counts as a yard after the failed run.
		setUp        bool
		operator     string
		found, added []string
	}{
		// The role the failed run wrote names Jo and is nobody's prompt yet:
		// the next run, told Kim, writes it again.
		{name: "after the role, told another operator", on: "setting operator", again: []string{"init", "--harness", "codex", "--operator", "Kim", "--yardmaster", "boss"}, operator: "Kim",
			found: []string{"the role prompt packs/starter/roles/yardmaster.md in"},
			added: []string{"the role prompt yardmaster.md", "the operator and yardmaster settings", "the crew member boss", "the default_kind setting"}},
		{name: "after the role", on: "setting operator", again: first, operator: "Jo",
			found: []string{"the role prompt packs/starter/roles/yardmaster.md in"},
			added: []string{"the operator and yardmaster settings", "the crew member boss", "the default_kind setting"}},
		{name: "after the operator setting", on: "setting yardmaster", again: []string{"init", "--harness", "codex", "--yardmaster", "boss"}, operator: "Jo",
			found: []string{"the role prompt packs/starter/roles/yardmaster.md in", "the setting operator Jo"},
			added: []string{"the operator and yardmaster settings", "the crew member boss"}},
		{name: "after the settings", on: "crew", again: []string{"init", "--harness", "codex"}, operator: "Jo",
			found: []string{"the settings operator Jo and yardmaster boss"},
			added: []string{"the crew member boss", "the default_kind setting"}},
		// The yardmaster is there, so the home counts as a yard, and
		// default_kind is not: the harness is the yardmaster's.
		{name: "after the yardmaster", on: "setting default_kind", again: []string{"init"}, setUp: true, operator: "Jo",
			found: []string{"the settings operator Jo and yardmaster boss", "the crew member boss"},
			added: []string{"the default_kind setting, which marks the yard set up"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := newHome(t)
			t.Setenv("USER", "kim")
			st := &failing{API: storetest.New(t), on: tc.on}
			a := &app{st: st, actor: "me"}
			ctx := context.Background()
			if out, err := run(t, a.rootCmd(), first...); err == nil {
				t.Fatalf("the first run did not fail:\n%s", out)
			} else if !errors.Is(err, errCut) {
				t.Fatal(err)
			}
			if got, _ := st.Settings(ctx); got["default_kind"] != "" {
				t.Fatalf("the failed run marked the yard set up: %v", got)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := a.initialized(ctx, cfg); ok != tc.setUp || err != nil {
				t.Fatalf("initialized = %v %v", ok, err)
			}
			st.on = ""
			t.Setenv("YARDR_ACTOR", tc.operator)

			out, err := run(t, a.rootCmd(), tc.again...)
			if err != nil {
				t.Fatalf("%v: %v\n%s", tc.again, err, out)
			}
			wholeYard(t, st, home, "codex", tc.operator, "boss")
			if !strings.Contains(out, "yard in "+home+" finished: part of it was there") {
				t.Errorf("the second run does not say it finished a yard:\n%s", out)
			}
			for label, want := range map[string][]string{"  found      ": tc.found, "  added      ": tc.added} {
				line := ""
				for _, l := range strings.Split(out, "\n") {
					if strings.HasPrefix(l, label) {
						line = l
					}
				}
				for _, w := range want {
					if !strings.Contains(line, w) {
						t.Errorf("%q lacks %q:\n%s", label, w, out)
					}
				}
			}
			// And once more: a yard, set up again, nothing found or added.
			out, err = run(t, a.rootCmd(), "init", "--actor", tc.operator)
			if err != nil || !strings.Contains(out, "set up again") || strings.Contains(out, "  found ") {
				t.Fatalf("third run: %v\n%s", err, out)
			}
			wholeYard(t, st, home, "codex", tc.operator, "boss")
		})
	}
}

// keysOf are the keys a former was asked, without the keyless fields.
func keysOf(f *former) []string {
	var keys []string
	for _, k := range f.asked {
		if k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// resumeYard is a terminal on a fresh home with the programs a yard needs,
// a service manager nothing is installed in, and a former to answer with.
func resumeYard(t *testing.T, answers map[string]any) (string, *former) {
	t.Helper()
	home := newHome(t)
	setInteractive(t, true)
	stubLookPath(t, "claude", "git", "herdr")
	stubServices(t, "darwin")
	stubWeb(t)
	stubFocus(t)
	captureStderr(t)
	f := &former{t: t, answers: answers, goes: []string{goYardmaster}}
	stubAsk(t, f.ask)
	return home, f
}

// The form after an init that was cut short before the yardmaster was added
// says what that run left, and the yardmaster's name starts at what it
// stored. "Your name" starts at who the terminal acts as, here the Jo that
// run was told.
func TestOnboardingAfterAFailedRun(t *testing.T) {
	home, f := resumeYard(t, nil)
	st := &failing{API: storetest.New(t), on: "crew"}
	if _, err := run(t, (&app{st: st}).rootCmd(), "init", "--harness", "claude", "--operator", "Jo", "--yardmaster", "boss"); !errors.Is(err, errCut) {
		t.Fatal(err)
	}
	st.on = ""
	started := startsYardmaster(t, st, "boss", yardmasterHandle())
	stubPane(t, answeredPane)
	t.Setenv("YARDR_ACTOR", "Jo")

	out, err := run(t, (&app{st: st}).rootCmd())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wholeYard(t, st, home, "claude", "Jo", "boss")
	for _, s := range []string{"An earlier setup of this home did not reach its end. It left the role prompt packs/starter/roles/yardmaster.md",
		"the settings operator Jo and yardmaster boss", "finished: part of it was there", "  added      the crew member boss",
		"boss is running, in herdr"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if *started != 1 || !slices.Equal(keysOf(f), []string{"style", "about", "try", "login", "web", "link", "go"}) {
		t.Fatalf("%d daemons started, asked %q", *started, keysOf(f))
	}
}

// Bare yardr on a yard an init left without its default_kind (the yardmaster
// was added, so the home counts as a yard) finishes it: it says what is
// there, asks, adds the setting, starts the yard and goes on as the
// onboarding does.
func TestBareYardrFinishesAYardWithoutDefaultKind(t *testing.T) {
	home, f := resumeYard(t, nil)
	st := &failing{API: storetest.New(t), on: "setting default_kind"}
	if _, err := run(t, (&app{st: st}).rootCmd(), "init", "--harness", "claude", "--operator", "Jo", "--yardmaster", "boss"); !errors.Is(err, errCut) {
		t.Fatal(err)
	}
	st.on = ""
	started := startsYardmaster(t, st, "boss", yardmasterHandle())
	stubPane(t, answeredPane)
	t.Setenv("YARDR_ACTOR", "Jo")

	out, err := run(t, (&app{st: st}).rootCmd())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wholeYard(t, st, home, "claude", "Jo", "boss")
	for _, s := range []string{"its onboarding did not reach its end", "FAULT  settings    default_kind is not set", "FAULT  daemon",
		"finished: part of it was there", "  added      the default_kind setting", "boss is running, in herdr"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "Usage:") {
		t.Errorf("help after the onboarding:\n%s", out)
	}
	if *started != 1 || !slices.Equal(keysOf(f), []string{"finish", "try", "login", "web", "link", "go"}) {
		t.Fatalf("%d daemons started, asked %q", *started, keysOf(f))
	}
}

// A yard whose daemon could not be started is taken up by bare yardr: the
// daemon is started, the yardmaster comes up, and the questions the first run
// never reached are asked. A service installed in between is not asked for.
func TestBareYardrStartsAYardThatDidNotStart(t *testing.T) {
	home, f := resumeYard(t, nil)
	st := storetest.New(t)
	// TestMain's start fails: "tests start no daemon".
	out, err := run(t, (&app{st: st}).rootCmd(), "init", "--harness", "claude", "--start", "--wait", "30ms")
	if err == nil || err.Error() != "yardmaster is not up" || !strings.Contains(out, "the daemon could not be started") {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, _, unitPath, err := unitFile(cfg, svcDaemon)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("installed"), 0o644); err != nil {
		t.Fatal(err)
	}
	started := startsYardmaster(t, st, "yardmaster", yardmasterHandle())
	stubPane(t, answeredPane)

	out, err = run(t, (&app{st: st}).rootCmd())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, s := range []string{"The yard in " + home + " has no depot yet, and its onboarding did not reach its end",
		"FAULT  daemon      not running", "FAULT  yardmaster  yardmaster is not running: the daemon starts it",
		"The daemon is started, in the background", "yardmaster is running, in herdr"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	// The yard lacked nothing: no summary of it, and no help.
	if strings.Contains(out, "set up again") || strings.Contains(out, "Usage:") {
		t.Errorf("output:\n%s", out)
	}
	// The run that could not start the daemon asked about the harness too.
	if *started != 1 || !slices.Equal(keysOf(f), []string{"try", "finish", "try", "web", "link", "go"}) {
		t.Fatalf("%d daemons started, asked %q", *started, keysOf(f))
	}
}

// A daemon that started and a yardmaster that never came up: init --start again
// waits again and completes, and so does bare yardr, which says that the
// session runs without an answer and starts no second daemon.
func TestTheNextRunWaitsForAYardmasterThatWasNotUp(t *testing.T) {
	home, f := resumeYard(t, nil)
	st := storetest.New(t)
	started := startsYardmaster(t, st, "yardmaster", yardmasterHandle())
	stubPane(t, "sh$ claude\nNot logged in")
	out, err := run(t, (&app{st: st}).rootCmd(), "init", "--harness", "claude", "--start", "--wait", "30ms")
	if err == nil || *started != 1 || !strings.Contains(out, "has not answered its first prompt within 30ms") {
		t.Fatalf("first run: %d started, %v\n%s", *started, err, out)
	}
	// In a terminal that run offered to ask the harness first and then the
	// yardmaster's pane, and was told no and not now.
	if !slices.Equal(keysOf(f), []string{"try", "pane"}) {
		t.Fatalf("first run: asked %q", keysOf(f))
	}
	f.asked = nil
	holdDaemon(t, home) // the daemon the first run started

	// Still silent: bare yardr says so, and no ends it there.
	f.answers = map[string]any{"finish": false}
	out, err = run(t, (&app{st: st}).rootCmd())
	if err != nil || !strings.Contains(out, "note   yardmaster  yardmaster's session runs, and it has not answered its first prompt") ||
		!strings.Contains(out, "Left as it is. To finish later: yardr --home "+home+" init --start") || strings.Contains(out, "Usage:") {
		t.Fatalf("no: %v\n%s", err, out)
	}
	if !slices.Equal(keysOf(f), []string{"finish"}) {
		t.Fatalf("asked %q", keysOf(f))
	}

	// The harness is logged in; the yardmaster answers.
	stubPane(t, answeredPane)
	f.answers, f.asked = nil, nil
	// Bare yardr finds nothing to finish on a yard that runs: help.
	if out, err = run(t, (&app{st: st}).rootCmd()); err != nil || len(f.asked) != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("a yard that runs: asked %q, %v\n%s", f.asked, err, out)
	}
	out, err = run(t, (&app{st: st}).rootCmd(), "init", "--start", "--wait", "30ms")
	if err != nil || *started != 1 || !strings.Contains(out, "The daemon was running already.") || !strings.Contains(out, "yardmaster is running, in herdr") {
		t.Fatalf("init --start again: %d started, %v\n%s", *started, err, out)
	}
}

// The same from the form: a yardmaster that did not come up ends the onboarding
// with how it is taken up, and bare yardr then goes on to its end.
func TestOnboardingIsTakenUpWhereTheYardmasterDidNotComeUp(t *testing.T) {
	home, f := resumeYard(t, nil)
	st := storetest.New(t)
	// The form waits yardmasterWait for the yardmaster; one that is disabled
	// ends the wait at once.
	stub(t, &spawnDaemon, func(*config.Config) (<-chan error, error) {
		if err := st.SetCrewEnabled(context.Background(), "yardmaster", false, "", "me"); err != nil {
			t.Error(err)
		}
		return nil, nil
	})
	out, err := run(t, (&app{st: st}).rootCmd())
	if err != nil || !strings.Contains(out, "yardmaster is stopped (disabled)") ||
		!strings.Contains(out, "Once that is set right, run yardr --home "+home+" in this terminal again: it goes on from here.") {
		t.Fatalf("form: %v\n%s", err, out)
	}
	if got := keysOf(f); !slices.Equal(got, []string{"style", "about", "try"}) {
		t.Fatalf("asked %q after a yardmaster that is not up", got)
	}
	stored, err := st.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCrewEnabled(context.Background(), "yardmaster", true, "", stored[store.OperatorSetting]); err != nil {
		t.Fatal(err)
	}
	started := startsYardmaster(t, st, "yardmaster", yardmasterHandle())
	stubPane(t, answeredPane)
	f.asked = nil
	out, err = run(t, (&app{st: st}).rootCmd())
	if err != nil || *started != 1 || !strings.Contains(out, "yardmaster is running, in herdr") || !slices.Equal(keysOf(f), []string{"finish", "try", "login", "web", "link", "go"}) {
		t.Fatalf("again: asked %q, %v\n%s", keysOf(f), err, out)
	}
}

// Bare yardr takes up nothing where it is not the onboarding's to finish: a
// yard with a depot, a yardmaster stopped on purpose, an agent's session, --json,
// no terminal. It is help there, and asks nothing.
func TestBareYardrLeavesOtherYardsAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, st store.API, home string)
	}{
		{"a depot", func(t *testing.T, st store.API, _ string) {
			if err := st.PutDepot(context.Background(), store.Depot{Name: "web", Path: shortDir(t, "yg"), Kind: store.DepotDir}, "me"); err != nil {
				t.Fatal(err)
			}
		}},
		{"a stopped yardmaster", func(t *testing.T, st store.API, home string) {
			if err := st.SetCrewEnabled(context.Background(), "yardmaster", false, "", "me"); err != nil {
				t.Fatal(err)
			}
			holdDaemon(t, home)
		}},
		{"an agent", func(t *testing.T, _ store.API, _ string) { t.Setenv("YARDR_CREW", "yardmaster") }},
		{"no terminal", func(t *testing.T, _ store.API, _ string) { setInteractive(t, false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, f := resumeYard(t, nil)
			st := storetest.New(t)
			if _, err := run(t, (&app{st: st}).rootCmd(), "init", "--harness", "claude"); err != nil {
				t.Fatal(err)
			}
			started := startsYardmaster(t, st, "yardmaster", yardmasterHandle())
			tc.prep(t, st, home)
			out, err := run(t, (&app{st: st}).rootCmd())
			if err != nil || len(f.asked) != 0 || *started != 0 || !strings.Contains(out, "Usage:") {
				t.Fatalf("asked %q, %d started, %v\n%s", f.asked, *started, err, out)
			}
		})
	}
}

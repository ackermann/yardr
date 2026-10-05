package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// standIn is the isolated yard's stand-in claude (contrib/isolated-yard/fake),
// built once for the tests that call it.
var standIn struct {
	once sync.Once
	bin  string
	err  error
}

// notLoggedIn is what the stand-in prints when its one-shot call fails.
const notLoggedIn = "Not logged in · Please run /login"

// asker is a terminal on a fresh home whose claude is the stand-in: it
// answers a one-shot call with "ok", or fails it with notLoggedIn while the
// file fails exists, and records every call. The form's questions are
// answered by f; the daemon, once started, brings the mayor up.
type asker struct {
	t       *testing.T
	st      store.API
	f       *former
	dir     string // holds bin/claude, agents.log and print-fails
	started *int
	chats   *[]string
}

func newAsker(t *testing.T, f *former) *asker {
	t.Helper()
	standIn.once.Do(func() {
		dir, err := os.MkdirTemp("", "ystandin")
		if err != nil {
			standIn.err = err
			return
		}
		standIn.bin = filepath.Join(dir, "claude")
		if out, err := exec.Command("go", "build", "-o", standIn.bin, "yardr.dev/yardr/contrib/isolated-yard/fake").CombinedOutput(); err != nil {
			standIn.err = &exec.Error{Name: "go build the stand-in: " + string(out), Err: err}
		}
	})
	if standIn.err != nil {
		t.Fatal(standIn.err)
	}
	a := &asker{t: t, st: storetest.New(t), f: f, dir: t.TempDir()}
	f.t = t
	bin, err := os.ReadFile(standIn.bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(a.dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(a.dir, "bin", "claude")
	if err := os.WriteFile(claude, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	newHome(t)
	actAsOperator(t)
	setInteractive(t, true)
	stub(t, &lookPath, func(name string) (string, error) {
		switch name {
		case "claude":
			return claude, nil
		case "git", "herdr", "cursor-agent":
			return "/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	})
	stub(t, &tryHarnessCall, realTry)
	stubServices(t, "darwin")
	stubWeb(t)
	stubFocus(t)
	captureStderr(t)
	t.Setenv("HERDR_ENV", "")
	stubAsk(t, f.ask)
	a.started = stubStart(t, a.st, mayorHandle())
	stubPane(t, answeredPane)
	a.chats = stubChat(t)
	return a
}

// fails makes the stand-in's one-shot call fail as a harness without a
// login does, and login makes it answer again.
func (a *asker) fails() {
	a.t.Helper()
	if err := os.WriteFile(filepath.Join(a.dir, "print-fails"), []byte("claude 2.1\n"+notLoggedIn+"\n"), 0o644); err != nil {
		a.t.Fatal(err)
	}
}

func (a *asker) login() { os.Remove(filepath.Join(a.dir, "print-fails")) }

// calls is how often the stand-in was called, as it recorded it.
func (a *asker) calls() int {
	log, _ := os.ReadFile(filepath.Join(a.dir, "agents.log"))
	return strings.Count(string(log), ` args=["-p" "`+tryPrompt+`"]`)
}

// onboard runs bare yardr: the form on a home with no yard, or the taking
// up of one that did not start.
func (a *asker) onboard() string {
	a.t.Helper()
	out, err := run(a.t, (&app{st: a.st}).rootCmd())
	if err != nil {
		a.t.Fatalf("%v\n%s", err, out)
	}
	return out
}

// The form asks, before it starts the mayor, whether to ask the harness for
// one real answer (yardr-hd59; before, a harness without a login was found
// only by a mayor that sat silent in its pane). Check makes the call doctor
// --try makes, says what it answered above the start, and the mayor starts.
func TestOnboardingAsksTheHarnessFirst(t *testing.T) {
	a := newAsker(t, &former{answers: map[string]any{"try": true}, goes: []string{goMayor}})
	out := a.onboard()
	wantText(t, out, "Asking claude for one answer (up to 45s).\nclaude answers (ok).\n\nThe daemon is started, in the background",
		"mayor is running, in herdr")
	if a.calls() != 1 || *a.started != 1 || !slices.Equal(*a.chats, []string{"mayor, expecting mayor"}) {
		t.Fatalf("%d calls, %d daemons started, chats %v\n%s", a.calls(), *a.started, *a.chats, out)
	}
	if keys := keysOf(a.f); !slices.Contains(keys, "try") || slices.Contains(keys, "tried") {
		t.Fatalf("asked %q", keys)
	}
	if strings.Contains(out, "did not answer") {
		t.Errorf("a harness that answered is said not to:\n%s", out)
	}
}

// Skip makes no call, and the start goes on as it did before the question.
func TestOnboardingSkipsTheCall(t *testing.T) {
	a := newAsker(t, &former{answers: map[string]any{"try": false}, goes: []string{goMayor}})
	a.fails() // a call would say so
	out := a.onboard()
	if a.calls() != 0 || *a.started != 1 || strings.Contains(out, "Asking claude") || strings.Contains(out, "did not answer") {
		t.Fatalf("%d calls, %d daemons started\n%s", a.calls(), *a.started, out)
	}
	wantText(t, out, "mayor is running, in herdr")
	if !slices.Contains(keysOf(a.f), "try") {
		t.Fatalf("asked %q", keysOf(a.f))
	}
}

// A harness that does not answer is named before any pane exists: how the
// call ended, the last lines it printed and what they usually mean. "Try
// again", once it is set right, asks it again, and the mayor starts.
func TestOnboardingTriesTheHarnessAgain(t *testing.T) {
	a := newAsker(t, &former{answers: map[string]any{"try": true}, goes: []string{goMayor}, tries: []string{tryAgain, tryAgain}})
	a.fails()
	// The person logs in before the second "Try again".
	asked := 0
	stubAsk(t, func(fields ...huh.Field) error {
		if fields[0].GetKey() == "tried" {
			if asked++; asked == 2 {
				a.login()
			}
			if *a.started != 0 {
				t.Errorf("the daemon was started before the harness answered")
			}
		}
		return a.f.ask(fields...)
	})
	out := a.onboard()
	fault := "\nclaude did not answer: the call failed (exit status 1).\n\nThe last lines it printed:\n\n  | claude 2.1\n  | " + notLoggedIn + "\n\n" +
		"That usually means claude is not logged in, or its login has run out: run claude in another terminal\nand log in there.\n"
	wantText(t, out, fault, "claude answers (ok).\n\nThe daemon is started", "mayor is running, in herdr")
	if n := strings.Count(out, fault); n != 2 {
		t.Errorf("the fault is said %d times, want 2:\n%s", n, out)
	}
	if a.calls() != 3 || asked != 2 || *a.started != 1 || len(*a.chats) != 1 {
		t.Fatalf("%d calls, asked %d times, %d daemons started, chats %v\n%s", a.calls(), asked, *a.started, *a.chats, out)
	}
}

// "Go on anyway" starts the mayor on the harness as it is, after one call.
func TestOnboardingGoesOnWithoutAnAnswer(t *testing.T) {
	a := newAsker(t, &former{answers: map[string]any{"try": true}, goes: []string{goMayor}, tries: []string{tryGoOn}})
	a.fails()
	out := a.onboard()
	wantText(t, out, "claude did not answer: the call failed (exit status 1).", "  | "+notLoggedIn, "The daemon is started", "mayor is running, in herdr")
	if a.calls() != 1 || *a.started != 1 {
		t.Fatalf("%d calls, %d daemons started\n%s", a.calls(), *a.started, out)
	}
}

// "Stop here" starts nothing and says how to come back; so does a form left
// with ctrl+c, at either question. The yard is there, and bare yardr takes
// it up: the question again, and with a harness that answers the mayor.
func TestOnboardingStopsBeforeTheMayor(t *testing.T) {
	stopped := func(a *asker, out string, calls int) {
		t.Helper()
		wantText(t, out, "\nStopped before mayor was started. The yard is there, and nothing runs on it.\n"+
			"Once claude answers (`"+here("yardr doctor --try")+"` asks it again, for a model call), this starts the yard\n"+
			"and waits for mayor:\n\n  "+here("yardr init --start")+"\n\n"+
			here("yardr")+" in a terminal does the same and goes on from here. `"+here("yardr setup")+"` takes the rest of\n"+
			"the setup as forms, with mayor or without.\n")
		if strings.Contains(out, "\n\n\nStopped") {
			t.Errorf("two blank lines above the stop:\n%s", out)
		}
		if a.calls() != calls || *a.started != 0 || len(*a.chats) != 0 {
			t.Fatalf("%d calls, %d daemons started, chats %v\n%s", a.calls(), *a.started, *a.chats, out)
		}
		for _, s := range []string{"The daemon is started", "Start the daemon in a terminal pane", "doctor` says of it:"} {
			if strings.Contains(out, s) {
				t.Errorf("output has %q after a stop:\n%s", s, out)
			}
		}
		if defaultKind(context.Background(), a.st) != "claude" {
			t.Errorf("the yard is not there after a stop")
		}
	}
	a := newAsker(t, &former{answers: map[string]any{"try": true}, goes: []string{goMayor}})
	a.fails()
	out := a.onboard()
	wantText(t, out, "  | "+notLoggedIn)
	stopped(a, out, 1)

	// Bare yardr on the yard that was left: it is taken up, and asks again.
	a.login()
	a.f.asked = nil
	out = a.onboard()
	wantText(t, out, "has no rig yet, and its onboarding did not reach its end", "claude answers (ok).", "mayor is running, in herdr")
	if got := keysOf(a.f); !slices.Equal(got, []string{"finish", "try", "login", "web", "link", "go"}) || a.calls() != 2 || *a.started != 1 {
		t.Fatalf("asked %q, %d calls, %d daemons started\n%s", got, a.calls(), *a.started, out)
	}

	for key, calls := range map[string]int{"try": 0, "tried": 1} {
		a := newAsker(t, &former{answers: map[string]any{"try": true}, tries: []string{tryAgain}})
		a.fails()
		a.f.abort = func(f huh.Field) bool { return f.GetKey() == key }
		stopped(a, a.onboard(), calls)
	}
}

// yardr init --start asks the same in a terminal, and nothing where no
// form runs: without a terminal, as JSON and in an agent's session it makes
// no call, and the start is as it was.
func TestInitStartAsksTheHarnessInATerminalOnly(t *testing.T) {
	a := newAsker(t, &former{answers: map[string]any{"try": true}})
	a.fails()
	start := func(args ...string) string {
		t.Helper()
		// Each start is the first: no session of the mayor from the last.
		if _, err := a.st.GetResident(context.Background(), "mayor"); err == nil {
			if err := a.st.ResidentStopped(context.Background(), "mayor", "test"); err != nil {
				t.Fatal(err)
			}
		}
		out, err := run(t, (&app{st: a.st}).rootCmd(), append([]string{"init", "--harness", "claude", "--start"}, args...)...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return out
	}
	setInteractive(t, false)
	wantText(t, start(), "mayor is running, in herdr")
	setInteractive(t, true)
	wantText(t, start("--json"), `"running": true`)
	t.Setenv("YARDR_SESSION", "s1")
	wantText(t, start(), "mayor is running, in herdr")
	t.Setenv("YARDR_SESSION", "")
	if a.calls() != 0 || len(keysOf(a.f)) != 0 {
		t.Fatalf("%d calls, asked %q where no form runs", a.calls(), keysOf(a.f))
	}

	// In a terminal: the question, the call, and "Stop here" by default.
	out := start()
	wantText(t, out, "  | "+notLoggedIn, "Stopped before mayor was started.")
	if a.calls() != 1 || !slices.Equal(keysOf(a.f), []string{"try", "tried"}) {
		t.Fatalf("%d calls, asked %q\n%s", a.calls(), keysOf(a.f), out)
	}
}

// Nothing is asked where no call can be made or no start is this run's: a
// kind with no one-shot form (doctor's "not tried"), a harness that is not
// on PATH, a mayor that runs or is stopped, and a daemon that runs already,
// which starts the mayor whatever would be answered.
func TestNoQuestionWithoutACallOrAStart(t *testing.T) {
	start := func(a *asker, harness string) {
		t.Helper()
		out, _ := run(t, (&app{st: a.st}).rootCmd(), "init", "--harness", harness, "--start", "--wait", "30ms")
		if a.calls() != 0 || slices.Contains(keysOf(a.f), "try") || strings.Contains(out, "Asking") {
			t.Fatalf("%d calls, asked %q\n%s", a.calls(), keysOf(a.f), out)
		}
	}
	t.Run("no one-shot form", func(t *testing.T) {
		a := newAsker(t, &former{answers: map[string]any{"try": true}})
		start(a, "cursor")
		if *a.started != 1 {
			t.Fatalf("%d daemons started", *a.started)
		}
	})
	t.Run("not on PATH", func(t *testing.T) {
		a := newAsker(t, &former{answers: map[string]any{"try": true}})
		stubLookPath(t, "git", "herdr")
		start(a, "claude")
	})
	t.Run("the mayor runs", func(t *testing.T) {
		a := newAsker(t, &former{answers: map[string]any{"try": true}})
		setInteractive(t, false)
		start(a, "claude")
		setInteractive(t, true)
		start(a, "claude")
	})
	t.Run("the mayor is stopped", func(t *testing.T) {
		a := newAsker(t, &former{answers: map[string]any{"try": true}})
		if _, err := run(t, (&app{st: a.st}).rootCmd(), "init", "--harness", "claude"); err != nil {
			t.Fatal(err)
		}
		if err := a.st.SetResidentEnabled(context.Background(), "mayor", false, "", "me"); err != nil {
			t.Fatal(err)
		}
		start(a, "claude")
	})
	t.Run("a daemon runs", func(t *testing.T) {
		a := newAsker(t, &former{answers: map[string]any{"try": true}})
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(cfg.Home, 0o755); err != nil {
			t.Fatal(err)
		}
		unlock, err := lockDaemon(cfg.DaemonLock(), 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		start(a, "claude")
		if *a.started != 0 {
			t.Fatalf("%d daemons started beside the one that runs", *a.started)
		}
	})
}

// What a call that did not answer usually means is said only where what it
// printed tells: a flag the harness does not know, no login, a call that
// did not end. Anything else stands for itself.
func TestWhatNoAnswerMeans(t *testing.T) {
	for _, c := range []struct {
		t    tryCheck
		want string
	}{
		{tryCheck{State: tryFails, printed: "error: unknown option '-p'\nUsage: claude login"}, "does not know what yardr calls it with (claude -p <prompt>)"},
		{tryCheck{State: tryFails, printed: "error: unexpected argument '--skip-git-repo-check' found"}, "`claude --help` says what it takes."},
		{tryCheck{State: tryFails, printed: notLoggedIn}, "claude is not logged in, or its login has run out"},
		{tryCheck{State: tryFails, printed: "ERROR: refresh token was already used\nPlease log out and sign in again."}, "is not logged in"},
		{tryCheck{State: tryFails, printed: "API Error: 401 authentication_error"}, "is not logged in"},
		{tryCheck{State: tryHangs, printed: "thinking"}, "It may wait at something nobody sees here"},
		{tryCheck{State: tryHangs, printed: "Please log in to continue"}, "is not logged in"},
		{tryCheck{State: tryFails, printed: "overloaded_error: try again later"}, ""},
		{tryCheck{State: tryFails}, ""},
	} {
		got := c.t.means("claude", "claude")
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%+v means %q, want %q", c.t, got, c.want)
		}
	}
	// A call that did not end, and one that printed nothing.
	stub(t, &tryTimeout, 2*time.Second)
	out := capture(t, func() { tryCheck{State: tryHangs}.print("kimi", "kimi") })
	wantText(t, out, "\nkimi did not answer: the call did not end within 2s.\nIt printed nothing.\n\nIt may wait at something nobody sees here",
		"Run kimi once in\nanother terminal and answer what it asks.\n")
}

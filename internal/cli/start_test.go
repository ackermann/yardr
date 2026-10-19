package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// yardmasterHandle is what the herdr runner records for a started yardmaster.
func yardmasterHandle() map[string]any {
	return map[string]any{"agent": "y-yardmaster", "workspace": "w2", "tab": "w2:t1", "pane": "w2:p1"}
}

// stubStart replaces the daemon's start with one that counts and, given a
// handle, records the yardmaster's session as the daemon would; nil leaves the
// yardmaster stopped. It returns the count.
func stubStart(t *testing.T, st store.API, handle map[string]any) *int {
	t.Helper()
	n := 0
	old := spawnDaemon
	spawnDaemon = func([]string, *config.Config) (<-chan error, error) {
		n++
		if handle != nil {
			if err := st.CrewStarted(context.Background(), "yardmaster", handle); err != nil {
				t.Errorf("yardmaster not started: %v", err)
			}
		}
		return nil, nil
	}
	t.Cleanup(func() { spawnDaemon = old })
	return &n
}

// answeredPane is a pane whose agent printed something after its first
// prompt.
const answeredPane = "sh$ claude\n❯ You are the yardr crew member \"yardmaster\". Run `yardr prime --crew yardmaster` and follow it.\n⏺ Reading my prime.\n❯"

// stubPane makes every pane read give text.
func stubPane(t *testing.T, text string) {
	t.Helper()
	old := paneTail
	paneTail = func(context.Context, map[string]any, int) (string, error) { return text, nil }
	t.Cleanup(func() { paneTail = old })
}

// stubFocus records the agents brought to the front.
func stubFocus(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := focusAgent
	focusAgent = func(_ context.Context, agent string) error { got = append(got, agent); return nil }
	t.Cleanup(func() { focusAgent = old })
	return &got
}

// answerNo answers a form page's confirm with no, as the n key does.
func answerNo(fields ...huh.Field) {
	for _, f := range fields {
		f.WithKeyMap(huh.NewDefaultKeyMap())
		f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	}
}

// The form, accepted with its defaults, starts the daemon, waits for the
// yardmaster and prints where it is; "Talk to the yardmaster", the answer its
// last question starts at, opens the chat on the yardmaster in the same
// terminal (yardr-8j3n; it brought the yardmaster's pane to the front
// before). The yardmaster is in the session that was asked to greet, so the
// view expects its mail.
func TestOnboardingStartsTheYard(t *testing.T) {
	home := newHome(t)
	actAsOperator(t)
	chats := stubChat(t)
	setInteractive(t, true)
	stubLookPath(t, "claude", "git", "herdr")
	asked := 0
	stubAsk(t, func(...huh.Field) error { asked++; return nil })
	st := storetest.New(t)
	started := stubStart(t, st, yardmasterHandle())
	stubPane(t, answeredPane)
	focused := stubFocus(t)
	t.Setenv("HERDR_ENV", "")

	out, err := run(t, (&app{st: st}).rootCmd())
	if err != nil {
		t.Fatal(err)
	}
	// Harness, the yardmaster's page, create, start, whether to ask the harness
	// first, the daemon at login, the web view, a link with another yard,
	// open: no page for what is missing.
	if asked != 10 || *started != 1 {
		t.Fatalf("%d pages, %d daemons started\n%s", asked, *started, out)
	}
	for _, s := range []string{"yard created", "The daemon is started, in the background; it logs to " + home + "/daemon.log",
		"yardmaster is running, in herdr", "agent      y-yardmaster", `workspace  "y yardmaster" (w2), tab w2:t1, pane w2:p1`,
		"herdr agent focus y-yardmaster", "herdr agent attach y-yardmaster",
		"Talk to it in any terminal", "\n  " + here("yardr chat") + "\n  " + here("yardr setup") + "\n",
		"You left the chat; yardmaster keeps running. To come back: " + here("yardr chat") + "\n"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "Start the daemon in a terminal pane") {
		t.Errorf("still told to start the daemon:\n%s", out)
	}
	if len(*focused) != 0 || !slices.Equal(*chats, []string{"yardmaster, expecting yardmaster"}) {
		t.Fatalf("focused %v, chats %v\n%s", *focused, *chats, out)
	}
	actor, aerr := config.Actor()
	if aerr != nil {
		t.Fatal(aerr)
	}
	if r, err := st.GetCrew(context.Background(), "yardmaster"); err != nil || runner.Greets(*r) != "" || r.Config[runner.GreetKey] != actor {
		t.Fatalf("the yardmaster, once started, greets %q again (config %v): %v", runner.Greets(*r), r.Config, err)
	}
}

// "Start the yard now?" answered no starts no daemon and says how to.
func TestOnboardingNoStart(t *testing.T) {
	newHome(t)
	setInteractive(t, true)
	stubLookPath(t, "claude", "git", "herdr")
	asked := 0
	stubAsk(t, func(fields ...huh.Field) error {
		if asked++; asked == 5 {
			answerNo(fields...)
		}
		return nil
	})
	st := storetest.New(t)
	started := stubStart(t, st, yardmasterHandle())

	out, err := run(t, (&app{st: st}).rootCmd())
	if err != nil {
		t.Fatal(err)
	}
	if asked != 5 || *started != 0 {
		t.Fatalf("%d pages, %d daemons started\n%s", asked, *started, out)
	}
	if r, _ := st.GetCrew(context.Background(), "yardmaster"); r == nil || r.Live() {
		t.Fatalf("yardmaster = %+v", r)
	}
	if !strings.Contains(out, "Start the daemon in a terminal pane of its own") || !strings.Contains(out, here("yardr init --start")) ||
		strings.Contains(out, "is running, in herdr") {
		t.Fatalf("output:\n%s", out)
	}
}

// Before the first page the form names what is not on PATH and what for.
// Without herdr the answer starts at stop, and stopping creates nothing;
// without git only, it starts at go on.
func TestOnboardingPreflight(t *testing.T) {
	newHome(t)
	setInteractive(t, true)
	stubLookPath(t, "claude")
	asked := 0
	stubAsk(t, func(...huh.Field) error { asked++; return nil })
	st := storetest.New(t)
	started := stubStart(t, st, yardmasterHandle())

	_, err := run(t, (&app{st: st}).rootCmd())
	if err == nil || !strings.Contains(err.Error(), "nothing was created") || asked != 1 || *started != 0 {
		t.Fatalf("no herdr: %d pages, err %v", asked, err)
	}
	if rs, _ := st.Crew(context.Background()); len(rs) != 0 {
		t.Fatalf("crew members = %v", rs)
	}

	stubLookPath(t, "claude", "herdr")
	stubPane(t, answeredPane)
	stubFocus(t)
	asked = 0
	text, err := run(t, (&app{st: st}).rootCmd())
	if err != nil || asked != 11 || *started != 1 {
		t.Fatalf("no git: %d pages, %d started, %v\n%s", asked, *started, err, text)
	}
	if !strings.Contains(text, "git is not on PATH: a depot is a git repository") || strings.Contains(text, "herdr is not on PATH") {
		t.Fatalf("output:\n%s", text)
	}
}

// yardr init starts nothing by default; --start starts the daemon, waits
// and prints the yardmaster's place, as JSON too.
func TestInitStart(t *testing.T) {
	newHome(t)
	stubLookPath(t, "claude", "git", "herdr")
	st := storetest.New(t)
	started := stubStart(t, st, yardmasterHandle())
	stubPane(t, answeredPane)
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.rootCmd(), "init", "--harness", "claude")
	if err != nil || *started != 0 || !strings.Contains(out, here("yardr daemon")) {
		t.Fatalf("plain init: %d started, %v\n%s", *started, err, out)
	}
	errs := captureStderr(t)
	a.stderr = errs
	out, err = run(t, a.rootCmd(), "init", "--start")
	if err != nil || *started != 1 {
		t.Fatalf("--start: %d started, %v\n%s", *started, err, out)
	}
	if !strings.Contains(out, "yardmaster is running, in herdr") || !strings.Contains(out, "pane w2:p1") {
		t.Fatalf("output:\n%s", out)
	}
	// No terminal: the progress is one line, on stderr.
	if got := errs.String(); got != "waiting for yardmaster to start (up to 2m0s)\n" {
		t.Fatalf("progress = %q", got)
	}

	out, err = run(t, (&app{st: st, actor: "me", json: true}).rootCmd(), "init", "--start", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Daemon bool         `json:"daemon"`
		Start  *startResult `json:"start"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if res.Start == nil || !res.Start.Running || res.Start.Yardmaster == nil || res.Start.Yardmaster.Agent != "y-yardmaster" ||
		res.Start.Yardmaster.Workspace != "y yardmaster" || res.Start.Yardmaster.Pane != "w2:p1" {
		t.Fatalf("start = %+v", res.Start)
	}
}

// A daemon running on the home is left alone: none is started beside it. The
// yardmaster it never starts (this one only holds the lock) ends the wait on
// the timeout, with what is known and how to go on, and exit 1.
func TestInitStartWithRunningDaemon(t *testing.T) {
	home := newHome(t)
	stubLookPath(t, "claude", "git", "herdr")
	st := storetest.New(t)
	started := stubStart(t, st, yardmasterHandle())
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDaemon(home+"/daemon.lock", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude", "--start", "--wait", "30ms")
	if *started != 0 {
		t.Fatalf("%d daemons started beside the running one", *started)
	}
	if err == nil || err.Error() != "yardmaster is not up" {
		t.Fatalf("err = %v", err)
	}
	for _, s := range []string{"The daemon was running already.", "yardmaster did not start within 30ms.",
		"no start has failed so far", here("yardr crew list"), here("yardr init --start")} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "sets up the rest with you") {
		t.Errorf("a yardmaster that is not there sets up nothing:\n%s", out)
	}
}

// The wait is for the yardmaster's own output, not for a session that exists: a
// session whose harness never took its first prompt, or whose pane stays
// blank, ends on the timeout, and the message quotes the pane's last lines.
func TestWaitForTheYardmastersOutput(t *testing.T) {
	const prompt = "You are the yardr crew member \"yardmaster\". Run `/bin/yardr prime --crew yardmaster` and follow it."
	for _, c := range []struct {
		name    string
		pending bool
		status  string
		pane    string
		up      bool
		want    []string
	}{
		{name: "answered", pane: answeredPane, up: true},
		{name: "working", status: "working", pane: "", up: true},
		// herdr calls this one idle, and its session runs.
		{name: "not logged in", status: "idle", pane: "sh$ claude\nWelcome\nNot logged in · Run /login\n" + prompt,
			want: []string{"yardmaster's session is running, but it has not answered its first prompt within 30ms.",
				"The last lines of its pane:", "  | Not logged in · Run /login",
				"the first prompt went into the pane, and claude printed nothing after it",
				"herdr agent focus y-yardmaster", "yardr crew restart yardmaster"}},
		{name: "prompt not taken", pending: true, pane: "sh$ claude\nDo you trust this folder?",
			want: []string{"has not answered its first prompt within 30ms.", "  | Do you trust this folder?",
				"claude was started in the pane but is not ready for its first prompt"}},
		{name: "login screen without the prompt", pane: "Select login method:",
			want: []string{"has not answered its first prompt", "  | Select login method:"}},
		{name: "blank pane", pane: "",
			want: []string{"has not answered its first prompt", "its pane shows nothing", "herdr agent attach y-yardmaster"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			newHome(t)
			stubLookPath(t, "claude", "git", "herdr")
			st := storetest.New(t)
			handle := yardmasterHandle()
			if c.pending {
				handle[store.PendingPrompt] = prompt
			}
			stubStart(t, st, handle)
			stubPane(t, c.pane)
			old := agentStatus
			agentStatus = func(context.Context, map[string]any) (string, error) { return c.status, nil }
			t.Cleanup(func() { agentStatus = old })
			out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude", "--start", "--wait", "30ms")
			if c.up != (err == nil) {
				t.Fatalf("err = %v\n%s", err, out)
			}
			for _, s := range c.want {
				if s = here(s); !strings.Contains(out, s) {
					t.Errorf("output lacks %q:\n%s", s, out)
				}
			}
			if !c.up && strings.Contains(out, "is running, in herdr") {
				t.Errorf("a silent yardmaster is reported as up:\n%s", out)
			}
		})
	}
}

// A yardmaster whose harness has not drawn its screen — the runner left the
// first prompt pending, "no screen, not typed" on its handle — is told of
// once that has held for the settle, on stderr with the pane's last lines
// and how to reach the pane, and the wait goes on to its own end. Before
// the settle nothing is said, nor of a yardmaster that comes up.
func TestWaitSaysTheHarnessWaitsInItsPane(t *testing.T) {
	const prompt = "You are the yardr crew member \"yardmaster\". Run `/bin/yardr prime --crew yardmaster` and follow it."
	screenHandle := func() map[string]any {
		h := yardmasterHandle()
		h[store.PendingPrompt] = prompt
		h[dispatch.PromptLook] = runner.PromptNotTyped
		return h
	}
	shortenWait := func(t *testing.T, poll, settle time.Duration) {
		t.Helper()
		oldPoll, oldSettle := pollEvery, screenSettle
		pollEvery, screenSettle = poll, settle
		t.Cleanup(func() { pollEvery, screenSettle = oldPoll, oldSettle })
	}
	const line = "yardmaster's harness waits for you in its pane: answer it there, the prompt is typed once it is up."

	t.Run("at a screen", func(t *testing.T) {
		newHome(t)
		stubLookPath(t, "claude", "git", "herdr")
		st := storetest.New(t)
		stubStart(t, st, screenHandle())
		stubPane(t, "sh$ claude\nWelcome\nSelect login method:")
		shortenWait(t, 5*time.Millisecond, 30*time.Millisecond)
		errs := captureStderr(t)
		out, err := run(t, (&app{st: st, actor: "me", stderr: errs}).rootCmd(), "init", "--harness", "claude", "--start", "--wait", "300ms")
		if err == nil {
			t.Fatalf("err = nil\n%s", out)
		}
		got := errs.String()
		if n := strings.Count(got, line); n != 1 {
			t.Errorf("the line is said %d times, want once:\n%s", n, got)
		}
		for _, s := range []string{"  | Select login method:", "herdr agent focus y-yardmaster", "herdr agent attach y-yardmaster"} {
			if !strings.Contains(got, s) {
				t.Errorf("stderr lacks %q:\n%s", s, got)
			}
		}
		// The wait went on after it: it ended on the timeout, as today.
		if !strings.Contains(out, "has not answered its first prompt within 300ms.") {
			t.Errorf("the wait did not go on to its own end:\n%s", out)
		}
	})

	t.Run("before the settle", func(t *testing.T) {
		newHome(t)
		stubLookPath(t, "claude", "git", "herdr")
		st := storetest.New(t)
		stubStart(t, st, screenHandle())
		stubPane(t, "sh$ claude\nWelcome\nSelect login method:")
		shortenWait(t, 5*time.Millisecond, 10*time.Second)
		errs := captureStderr(t)

		out, err := run(t, (&app{st: st, actor: "me", stderr: errs}).rootCmd(), "init", "--harness", "claude", "--start", "--wait", "30ms")
		if err == nil || !strings.Contains(out, "has not answered its first prompt within 30ms.") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		if strings.Contains(errs.String(), line) {
			t.Errorf("said before the settle:\n%s", errs.String())
		}
	})

	t.Run("comes up", func(t *testing.T) {
		newHome(t)
		stubLookPath(t, "claude", "git", "herdr")
		st := storetest.New(t)
		stubStart(t, st, yardmasterHandle())
		stubPane(t, answeredPane)
		shortenWait(t, 5*time.Millisecond, 30*time.Millisecond)
		errs := captureStderr(t)

		out, err := run(t, (&app{st: st, actor: "me", stderr: errs}).rootCmd(), "init", "--harness", "claude", "--start")
		if err != nil || !strings.Contains(out, "yardmaster is running, in herdr") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		if strings.Contains(errs.String(), line) {
			t.Errorf("said of a yardmaster that came up:\n%s", errs.String())
		}
	})
}

// A harness that comes up late ends the wait when its output appears.
func TestWaitEndsOnFirstOutput(t *testing.T) {
	newHome(t)
	stubLookPath(t, "claude", "git", "herdr")
	st := storetest.New(t)
	stubStart(t, st, yardmasterHandle())
	reads := 0
	old := paneTail
	paneTail = func(context.Context, map[string]any, int) (string, error) {
		if reads++; reads < 4 {
			return "sh$ claude", nil
		}
		return answeredPane, nil
	}
	t.Cleanup(func() { paneTail = old })
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude", "--start")
	if err != nil || reads < 4 || !strings.Contains(out, "yardmaster is running, in herdr") {
		t.Fatalf("%d reads, %v\n%s", reads, err, out)
	}
}

// What cannot come by waiting ends the wait at once: a start that failed with
// herdr or the harness not on PATH, and a yardmaster yardr gave up on. Each
// says what is known, the last failure's reason among it.
func TestWaitEndsOnAFailedStart(t *testing.T) {
	ctx := context.Background()
	home := newHome(t)
	st := storetest.New(t)
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	op := stored[store.OperatorSetting]
	// This daemon holds the lock, as a real one does while it runs.
	var unlock func()
	old := spawnDaemon
	spawnDaemon = func([]string, *config.Config) (<-chan error, error) {
		if unlock, err = lockDaemon(cfg.DaemonLock(), 0); err != nil {
			return nil, err
		}
		return nil, st.CrewStartFailed(ctx, "yardmaster", `herdr workspace.create: exec: "herdr": executable file not found in $PATH`, time.Minute)
	}
	t.Cleanup(func() { spawnDaemon = old })

	stubLookPath(t, "git")
	began := time.Now()
	res := a.startYard(ctx, cfg, st, "yardmaster", time.Minute, io.Discard)
	unlock()
	if res.Running || time.Since(began) > 10*time.Second || res.Problem != "yardmaster cannot start: herdr and claude are not on PATH." {
		t.Fatalf("after %v: %+v", time.Since(began), res)
	}
	if next := strings.Join(res.Next, "\n"); strings.Contains(next, "doctor --try") {
		t.Errorf("a harness that is not there is not one to try:\n%s", next)
	}
	known := strings.Join(res.Known, "\n")
	for _, s := range []string{"herdr is not on PATH: it starts every agent", "claude is not on PATH: it is the harness yardmaster runs on",
		"the last start failed with: herdr workspace.create", "logs to " + home + "/daemon.log"} {
		if !strings.Contains(known, s) {
			t.Errorf("known lacks %q:\n%s", s, known)
		}
	}

	// Everything on PATH, and the daemon gave up.
	stubLookPath(t, "claude", "git", "herdr")
	spawnDaemon = func([]string, *config.Config) (<-chan error, error) {
		for i := 0; i < store.MaxStartFails; i++ {
			if err := st.CrewStartFailed(ctx, "yardmaster", "herdr agent start: boom", time.Minute); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	res = a.startYard(ctx, cfg, st, "yardmaster", time.Minute, io.Discard)
	if res.Running || !strings.Contains(res.Problem, "the daemon gave up after") ||
		!strings.Contains(strings.Join(res.Next, "\n"), here("yardr crew start yardmaster")) ||
		!strings.Contains(strings.Join(res.Known, "\n"), "the last start failed with: herdr agent start: boom") {
		t.Fatalf("%+v", res)
	}
	// A start that failed with every program there names the call that says
	// whether the harness works (yardr-aa7y); one that failed for want of a
	// program did not.
	if next := strings.Join(res.Next, "\n"); !strings.Contains(next, "see whether claude answers at all, outside a pane: "+here("yardr doctor --try")) {
		t.Errorf("next lacks doctor --try:\n%s", next)
	}

	// A start that failed because herdr has no server says nothing of the
	// harness, which was never started: doctor's herdr line is what helps.
	if err := st.CrewStartFailed(ctx, "yardmaster", "server_not_running: no herdr server is running", time.Minute); err != nil {
		t.Fatal(err)
	}
	spawnDaemon = func([]string, *config.Config) (<-chan error, error) { return nil, nil }
	res = a.startYard(ctx, cfg, st, "yardmaster", time.Minute, io.Discard)
	if next := strings.Join(res.Next, "\n"); res.Running || strings.Contains(next, "doctor --try") {
		t.Errorf("herdr's outage is no reason to try the harness: %+v", res)
	}

	// A daemon that cannot be started at all.
	spawnDaemon = func([]string, *config.Config) (<-chan error, error) { return nil, errors.New("fork: no") }
	if res = a.startYard(ctx, cfg, st, "yardmaster", time.Minute, io.Discard); res.DaemonStarted ||
		res.Problem != "the daemon could not be started: fork: no" || !strings.Contains(res.Next[0], here("yardr daemon")) {
		t.Fatalf("%+v", res)
	}

	// A daemon that ends right away, with no other holding the lock.
	spawnDaemon = func([]string, *config.Config) (<-chan error, error) {
		ch := make(chan error, 1)
		ch <- errors.New("exit status 1")
		return ch, st.SetCrewEnabled(ctx, "yardmaster", true, "", op)
	}
	if res = a.startYard(ctx, cfg, st, "yardmaster", time.Minute, io.Discard); res.Problem != "the daemon stopped right after it started." ||
		!strings.Contains(strings.Join(res.Known, "\n"), "no daemon is running on this yard") {
		t.Fatalf("%+v", res)
	}
}

// The background daemon keeps the starter's environment but not its
// identity, and works for the home it was started for.
func TestDaemonEnv(t *testing.T) {
	t.Parallel()
	got := daemonEnv([]string{"PATH=/bin", "HERDR_SOCKET_PATH=/s", "YARDR_ACTOR=a", "YARDR_BEAD=b", "YARDR_SESSION=s",
		"YARDR_CREW=r", "YARDR_HOME=/elsewhere", "YARDR_EVENT_RETENTION=9d", "TOKEN=x"}, "/y")
	want := []string{"PATH=/bin", "HERDR_SOCKET_PATH=/s", "YARDR_EVENT_RETENTION=9d", "TOKEN=x", "YARDR_HOME=/y"}
	if !slices.Equal(got, want) {
		t.Fatalf("env = %v", got)
	}
}

// A start that failed before the upgrade is in the journal under the kind
// that release logged, and is still the reason doctor and init give.
func TestLastStartFailureReadsTheOldKind(t *testing.T) {
	t.Parallel()
	st, owned := storetest.NewOwned(t)
	ctx := context.Background()
	r, err := st.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := owned.LogEvent(ctx, r.Journal, "resident_start_failed", "yard", map[string]any{"reason": "claude: not logged in"}); err != nil {
		t.Fatal(err)
	}
	if got := lastStartFailure(ctx, st, *r); got != "claude: not logged in" {
		t.Fatalf("the last failed start = %q", got)
	}
}

// A yardmaster that answered its first prompt with more than a screenful
// has the prompt above its screen, and the read that moves nothing does not
// show it. Only then is the pane's history fetched, which herdr may scroll
// the pane for; a pane that shows the prompt is read the once (yardr-dsmt).
func TestUpReadsHistoryOnlyWhenThePromptLeftTheScreen(t *testing.T) {
	r := store.CrewMember{Name: "yardmaster", Handle: yardmasterHandle()}
	for _, c := range []struct {
		name, screen, history string
		up                    bool
		histories             int
	}{
		{"answered, on the screen", answeredPane, "", true, 0},
		{"on the screen, no answer", "❯ " + runner.CrewPromptLead + "\"yardmaster\".", answeredPane, false, 0},
		{"answered, above the screen", "⏺ The yard is quiet.\n❯", answeredPane, true, 1},
		{"nowhere", "sh$ claude", "sh$ claude", false, 1},
	} {
		histories := 0
		stub(t, &paneTail, func(context.Context, map[string]any, int) (string, error) { return c.screen, nil })
		stub(t, &paneHistory, func(context.Context, map[string]any, int) (string, error) {
			histories++
			return c.history, nil
		})
		if got := up(context.Background(), r); got != c.up || histories != c.histories {
			t.Errorf("%s: up = %v after %d history reads, want %v after %d", c.name, got, histories, c.up, c.histories)
		}
	}
}

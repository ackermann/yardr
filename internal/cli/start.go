package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// yardmasterWait is how long the onboarding waits for the yardmaster before
// it says what it knows instead. A start takes seconds; the rest is room for
// a harness that is slow to come up or a first failed try (the daemon retries
// after 30s). A variable so a test of the form, which has no --wait, need not
// sit it out.
var yardmasterWait = 2 * time.Minute

// pollEvery is how often the wait looks at the yardmaster; tests shorten it.
var pollEvery = 500 * time.Millisecond

// screenSettle is how long the yardmaster's handle must say its harness has
// not drawn its screen (the first prompt pending, not typed) before the wait
// says the harness waits in its pane: a harness that is only slow to draw is
// not reported. A variable so a test need not sit it out.
var screenSettle = 10 * time.Second

// preflight runs before the form's first page: it names what is not on
// PATH and what it is needed for (basicNeeds, as yardr doctor does), and lets
// the user stop or go on. The answer starts at "go on" only when herdr is
// there: without herdr the yard this form creates cannot start its yardmaster.
func preflight() error {
	var gone []string
	goOn := true
	for _, n := range basicNeeds() {
		if n.Found {
			continue
		}
		fmt.Printf("%s is not on PATH: %s.\n  Get it: %s\n", n.Program, n.For, n.Get)
		gone = append(gone, n.Program)
		if n.Program == "herdr" {
			goOn = false
		}
	}
	if len(gone) == 0 {
		return nil
	}
	fmt.Println()
	if err := ask(huh.NewConfirm().Title("Go on without " + strings.Join(gone, " and ") + "?").
		Description("Stopping creates nothing: install what is missing and run yardr again.").
		Affirmative("Go on").Negative("Stop").Value(&goOn)); err != nil {
		return err
	}
	if !goOn {
		return huh.ErrUserAborted
	}
	return nil
}

// yardmasterPlace is where the yardmaster's session runs, as herdr names it.
type yardmasterPlace struct {
	Agent       string `json:"agent"`        // herdr agent list's name
	Workspace   string `json:"workspace"`    // the workspace's label
	WorkspaceID string `json:"workspace_id"` // and its id, the tab's and the pane's
	Tab         string `json:"tab"`
	Pane        string `json:"pane"`
}

// startResult is what starting the yard came to: the daemon, and the
// yardmaster's place once its session runs or, when it does not, what is
// known and how to go on.
type startResult struct {
	DaemonStarted bool   `json:"daemon_started"`       // by this run; false when one ran already
	DaemonLog     string `json:"daemon_log,omitempty"` // where the one this run started logs
	// Running: the yardmaster's session runs and it answered its first prompt
	// (see waitYardmaster).
	Running bool `json:"running"`
	// Yardmaster is where the session is, once there is one, up or not.
	Yardmaster *yardmasterPlace `json:"yardmaster,omitempty"`
	// Pane is the last lines of the yardmaster's pane when it did not come up:
	// where a harness says why it does nothing.
	Pane string `json:"pane,omitempty"`
	// Chat is the command for the conversation with the yardmaster, once it is
	// up: where its first message goes, and where a terminal is taken.
	Chat    string   `json:"chat,omitempty"`
	Problem string   `json:"problem,omitempty"`
	Known   []string `json:"known,omitempty"`
	Next    []string `json:"next,omitempty"`
	// said: the daemon's line was printed already, before the question that
	// offers the yardmaster's pane (offerPane), and for a yardmaster that is
	// not up the problem and its pane with it; print does not say them twice.
	said bool
}

// spawnDaemon starts `yardr daemon` for the home, detached from this
// terminal and logging to the home's daemon.log (spawn). exited gets the
// daemon's end, should it come. Tests replace it.
//
// Unlike the server, the daemon keeps the starter's environment: it is what
// a daemon in a pane of its own has, and the runners need it (herdr's
// socket, the PATH an exec group's command runs with). See daemonEnv.
var spawnDaemon = func(cfg *config.Config) (exited <-chan error, err error) {
	_, exited, _, err = spawn(cfg, kindOf(svcDaemon), nil)
	return exited, err
}

// daemonEnv is the environment a daemon started in the background gets,
// from the starter's environ: all of it but what names the starter (its
// actor, bead, session, stage, group, crew member: init may be run by an agent, and the
// daemon is nobody's session), with the home it works for in place of
// whatever YARDR_HOME said.
func daemonEnv(environ []string, home string) []string {
	var env []string
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); !config.IsVar(k, "HOME", "ACTOR", "BEAD", "SESSION", "CREW", "STAGE", "GROUP") {
			env = append(env, kv)
		}
	}
	return append(env, config.Env("HOME", home)...)
}

// focusAgent brings an agent's pane to the front in herdr; tests replace it.
var focusAgent = func(ctx context.Context, agent string) error {
	out, err := exec.CommandContext(ctx, "herdr", "agent", "focus", agent).CombinedOutput()
	if err != nil {
		return fmt.Errorf("herdr agent focus %s: %w: %s", agent, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// attachAgent opens an agent's pane in this terminal, as herdr's direct
// attach does, and returns when the person leaves it; tests replace it.
// The pane's program gets the keys, so it is the terminal's own that herdr
// is given.
var attachAgent = func(ctx context.Context, agent string) error {
	cmd := exec.CommandContext(ctx, "herdr", "agent", "attach", agent)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("herdr agent attach %s: %w", agent, err)
	}
	return nil
}

// detachKeys leave a pane opened with herdr agent attach, and gotoKeys open
// herdr's list of panes, as herdr documents them ("Direct terminal attach"
// and "Keyboard" in its docs) for a prefix that was not rebound.
const (
	detachKeys = "ctrl+b q"
	gotoKeys   = "ctrl+b g"
)

// paneTail is the last lines a session's pane shows, blank ones left out;
// tests replace it.
var paneTail = func(ctx context.Context, handle map[string]any, lines int) (string, error) {
	return (&runner.Herdr{Bin: "herdr"}).Tail(ctx, handle, lines)
}

// agentStatus is herdr's reading of a session's agent (idle, working,
// blocked, done, unknown; "" when it is gone); tests replace it.
var agentStatus = func(ctx context.Context, handle map[string]any) (string, error) {
	return (&runner.Herdr{Bin: "herdr"}).Status(ctx, handle)
}

const (
	// paneLines is how much of the yardmaster's pane a failed wait quotes.
	paneLines = 6
	// paneScan is how far back the wait looks for the first prompt.
	paneScan = 200
)

// startYard starts the daemon in the background, unless one runs on the home
// already, and waits up to wait for the yardmaster (waitYardmaster). It never
// fails: what went wrong is in the result, in words for the person reading.
func (a *app) startYard(ctx context.Context, cfg *config.Config, st store.API, yardmaster string, wait time.Duration) *startResult {
	res := &startResult{}
	var exited <-chan error
	if !daemonRunning(cfg.DaemonLock()) {
		var err error
		if exited, err = spawnDaemon(cfg); err != nil {
			res.Problem = "the daemon could not be started: " + err.Error()
			res.Next = []string{fmt.Sprintf("start it in a terminal pane of its own: %s daemon", yardrAt(cfg.Home))}
			return res
		}
		res.DaemonStarted, res.DaemonLog = true, cfg.DaemonLog()
	}
	a.awaitYardmaster(ctx, cfg, st, yardmaster, wait, exited, res)
	return res
}

// awaitYardmaster waits up to wait for the yardmaster (waitYardmaster) and
// writes into res how that ended: where the yardmaster is, and for one that
// is not up the problem, its pane's last lines, what is known and how to go
// on.
func (a *app) awaitYardmaster(ctx context.Context, cfg *config.Config, st store.API, yardmaster string, wait time.Duration, exited <-chan error, res *startResult) {
	r, problem := waitYardmaster(ctx, cfg, st, yardmaster, wait, exited)
	res.describe(ctx, cfg, st, r, yardmaster, problem)
}

// describe writes into s what is known of the yardmaster as last read (r, nil
// when it was not): up when problem is "", else not up for that reason.
func (s *startResult) describe(ctx context.Context, cfg *config.Config, st store.API, r *store.CrewMember, yardmaster, problem string) {
	s.Running, s.Yardmaster, s.Chat, s.Problem, s.Pane, s.Known, s.Next = false, nil, "", "", "", nil, nil
	if r != nil && r.Live() {
		all, _ := st.Settings(ctx)
		s.Yardmaster = placeOf(*r, runner.YardName(cfg.Home, all))
	}
	if problem == "" {
		s.Running, s.Chat = true, yardrAt(cfg.Home)+" chat"
		return
	}
	s.Problem = problem
	if s.Yardmaster != nil {
		s.Pane, _ = paneTail(ctx, r.Handle, paneLines)
	}
	s.Known, s.Next = diagnose(ctx, cfg, st, r, yardmaster, s)
}

// How the question after a harness that did not answer is answered.
const (
	tryAgain = "again"
	tryGoOn  = "go"
	tryStop  = "stop"
)

// askHarness is the question before the yardmaster is started: whether to ask
// its harness for one real answer first, outside a pane (tryHarnessCall,
// the call yardr doctor --try makes). A harness that is not logged in then
// says so here, with what it printed, before a session sits at a screen
// nobody sees. One that does not answer is asked again once the person has
// set it right in another terminal, or the start goes on all the same, or
// stops here: it reports false for that last one, and a form left with
// ctrl+c is the same answer.
//
// It asks only a person at a terminal, and only when the start is this run's
// to make or leave: no daemon runs yet (one that does starts the yardmaster
// whatever is answered here), and the yardmaster is enabled, has no session
// and was not given up on. A kind with no one-shot form, and a harness that
// is not on PATH (the start says so), are not asked about. The call costs a
// model call, so it is made on a yes only.
func (a *app) askHarness(ctx context.Context, cfg *config.Config, st store.API, yardmaster string) (goOn bool, err error) {
	if a.json || !interactive() || agentSession() || daemonRunning(cfg.DaemonLock()) {
		return true, nil
	}
	r, err := st.GetCrew(ctx, yardmaster)
	if err != nil || !r.Enabled || r.Live() || r.State == "failed" {
		return true, nil // the wait says what is wrong with it
	}
	kind, _ := r.Config["kind"].(string)
	kind = or(kind, defaultKind(ctx, st))
	exe := agents.Executable(kind)
	if agents.OneShotArgs(kind, tryPrompt) == nil || exe == "" {
		return true, nil
	}
	path, err := lookPath(exe)
	if err != nil {
		return true, nil
	}
	check := true
	fmt.Println()
	err = ask(huh.NewConfirm().Key("try").Title("Check that " + kind + " answers?").
		Description(fmt.Sprintf("It makes one real call (it costs a model call), outside a pane: a %s that is not logged in\n"+
			"says so here, before %s is started on it.", kind, yardmaster)).
		Affirmative("Check").Negative("Skip").Value(&check))
	switch {
	case errors.Is(err, huh.ErrUserAborted):
		stoppedBefore(cfg, yardmaster, kind)
		return false, nil
	case err != nil:
		return false, err
	case !check:
		return true, nil
	}
	for ctx.Err() == nil {
		fmt.Printf("Asking %s for one answer (up to %s).\n", kind, tryTimeout)
		t := tryHarnessCall(ctx, kind, path)
		switch t.State {
		case tryOK:
			fmt.Printf("%s answers (%s).\n\n", kind, or(t.Output, "no output"))
			return true, nil
		case tryNotTried:
			fmt.Printf("%s was not asked: %s.\n\n", kind, t.Output)
			return true, nil
		}
		t.print(kind, exe)
		pick := tryAgain
		fmt.Println()
		err := ask(huh.NewSelect[string]().Key("tried").Title(kind+" did not answer").
			Description(fmt.Sprintf("%s runs on it: started now, it would likely sit in its pane the same way.", yardmaster)).
			Options(huh.NewOption("Try again: once you have set it right in another terminal", tryAgain),
				huh.NewOption("Go on anyway: start "+yardmaster+" on it", tryGoOn),
				huh.NewOption("Stop here: the yard stays, and nothing is started on it", tryStop)).Value(&pick))
		switch {
		case errors.Is(err, huh.ErrUserAborted) || (err == nil && pick == tryStop):
			stoppedBefore(cfg, yardmaster, kind)
			return false, nil
		case err != nil:
			return false, err
		case pick == tryGoOn:
			return true, nil
		}
	}
	return false, ctx.Err()
}

// print says of a call that failed or did not end what it came to: how it
// ended, the last lines the harness printed and, where they tell, what that
// usually means.
func (t tryCheck) print(kind, exe string) {
	if t.State == tryHangs {
		fmt.Printf("\n%s did not answer: the call did not end within %s.\n", kind, tryTimeout)
	} else {
		fmt.Printf("\n%s did not answer: the call failed (%s).\n", kind, or(t.err, "no error"))
	}
	if t.printed == "" {
		fmt.Println("It printed nothing.")
	} else {
		fmt.Printf("\nThe last lines it printed:\n\n")
		for _, l := range strings.Split(t.printed, "\n") {
			fmt.Printf("  | %s\n", l)
		}
	}
	if means := t.means(kind, exe); means != "" {
		fmt.Printf("\n%s\n", means)
	}
}

// What a harness prints when it is called with a flag it does not know, and
// when it has no login to call its model with, in lower case. They are what
// the harnesses yardr was tried with print; one that says it another way
// gets no sentence, and its lines stand for themselves.
var (
	saysNoFlag  = []string{"unknown option", "unknown flag", "unknown argument", "unknown command", "unrecognized", "unexpected argument", "invalid option", "no such option"}
	saysNoLogin = []string{"not logged in", "/login", "log in", "logged out", "log out", "sign in", "signed out", "login", "unauthorized", "401",
		"authenticat", "api key", "api_key", "credential"}
)

// means is what a call that did not answer usually means, in plain words,
// where what it printed tells: no login, or a flag this version of the
// harness does not know. "" where it does not tell.
func (t tryCheck) means(kind, exe string) string {
	said := strings.ToLower(t.printed)
	has := func(words []string) bool {
		return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(said, w) })
	}
	switch {
	case has(saysNoFlag):
		return fmt.Sprintf("That usually means this %s does not know what yardr calls it with (%s): a version other than\n"+
			"the one yardr knows. `%s --help` says what it takes.", exe, strings.Join(append([]string{exe}, agents.OneShotArgs(kind, "<prompt>")...), " "), exe)
	case has(saysNoLogin):
		return fmt.Sprintf("That usually means %s is not logged in, or its login has run out: run %s in another terminal\n"+
			"and log in there.", kind, exe)
	case t.State == tryHangs:
		return fmt.Sprintf("It may wait at something nobody sees here: a login, a question, a system dialog. Run %s once in\n"+
			"another terminal and answer what it asks.", exe)
	}
	return ""
}

// stoppedBefore says, after "Stop here", what is there and how to come
// back.
func stoppedBefore(cfg *config.Config, yardmaster, kind string) {
	yardr := yardrAt(cfg.Home)
	fmt.Printf("Stopped before %s was started. The yard is there, and nothing runs on it.\n"+
		"Once %s answers (`%s doctor --try` asks it again, for a model call), this starts the yard\n"+
		"and waits for %s:\n\n  %s init --start\n\n"+
		"%s in a terminal does the same and goes on from here. `%s setup` takes the rest of\n"+
		"the setup as forms, with %s or without.\n", yardmaster, kind, yardr, yardmaster, yardr, yardr, yardr, yardmaster)
}

// How the question that offers the yardmaster's pane is answered.
const (
	paneOpen  = "open"
	paneLater = "later"
)

// offerPane is the way to a yardmaster that is live but not up, and back: its
// harness waits at something yardr does not answer (a login, a dialog, a
// question), and only the person can, in its pane. It says what is known,
// shows the pane's last lines and asks; "Open the yardmaster's pane" attaches
// to the pane in this terminal and, once the person has left it, waits for
// the yardmaster as the start does and writes into res how that ended. Still
// not up, it asks again. It returns when the yardmaster is up, has no session
// any more, or the answer is "Not now": res says which, and the caller goes
// on as it does after a start.
//
// It asks only where a chat could open (chats), since that is where it
// leads. yardr types nothing into the pane: what the harness asks there is
// the person's to answer.
func (a *app) offerPane(ctx context.Context, cfg *config.Config, st store.API, yardmaster string, wait time.Duration, res *startResult) error {
	for a.chats() && !res.Running && res.Yardmaster != nil && res.Yardmaster.Agent != "" && ctx.Err() == nil {
		res.printHead()
		agent, pick := res.Yardmaster.Agent, paneOpen
		known := "its harness may wait at a login, a question or a system dialog"
		if len(res.Known) > 0 {
			known = res.Known[0]
		}
		fmt.Println()
		err := ask(huh.NewSelect[string]().Key("pane").Title("Open "+yardmaster+"'s pane?").
			Description(known+".\nOnly you can answer it there; yardr types nothing into the pane.").
			Options(huh.NewOption("Open the yardmaster's pane", paneOpen), huh.NewOption("Not now", paneLater)).Value(&pick))
		switch {
		case errors.Is(err, huh.ErrUserAborted) || (err == nil && pick != paneOpen):
			return nil
		case err != nil:
			return err
		}
		if os.Getenv("HERDR_ENV") != "" {
			// In a pane of herdr there is nothing to attach: herdr shows the
			// yardmaster's pane in place of this one, as yardr chat's key for
			// it does, and this run waits behind it.
			if err := focusAgent(ctx, agent); err != nil {
				fmt.Printf("%s's pane could not be brought to the front: %v\n", yardmaster, err)
				return nil
			}
			fmt.Printf("herdr shows %s's pane now. To come back to this one: %s opens herdr's list of panes.\n"+
				"This pane waits for %s meanwhile, and opens the chat once it is up.\n", yardmaster, gotoKeys, yardmaster)
		} else {
			fmt.Printf("Opening %s's pane here. To come back: %s (press ctrl+b, release, then q).\n"+
				"Leaving that way stops nothing: %s keeps running, and this goes on.\n", yardmaster, detachKeys, yardmaster)
			if err := attachAgent(ctx, agent); err != nil {
				fmt.Printf("\n%s's pane could not be opened: %v\n", yardmaster, err)
				return nil
			}
			fmt.Printf("\nBack from %s's pane.\n", yardmaster)
		}
		fmt.Printf("Waiting for %s to answer its first prompt (up to %s).\n", yardmaster, wait)
		again := &startResult{DaemonStarted: res.DaemonStarted, DaemonLog: res.DaemonLog}
		a.awaitYardmaster(ctx, cfg, st, yardmaster, wait, nil, again)
		*res = *again
		// The daemon's line stands above; a yardmaster still not up is said
		// anew, with its pane as it is now.
		res.said = true
		if !res.Running {
			res.printProblem()
		}
	}
	return nil
}

// waitYardmaster polls the yardmaster until it is up, and returns "" then. Up
// is more than a session that exists: a harness on PATH can sit dead in a
// running session (a quarantined binary behind a dialog nobody sees, a CLI at
// "Not logged in"), and herdr reads any process of the harness's name that
// printed something as an idle agent. So the wait is for the yardmaster's
// first output: what its pane shows after its first prompt (see up).
//
// Otherwise it returns why the wait ended: the time is up, the daemon is
// gone, yardr gave up on the yardmaster, or a start failed for want of a
// program that waiting will not bring. Before that, a harness sitting at a
// screen of its own — the first prompt pending, not typed (pendingNoScreen)
// for the settle — is told of once, with the pane and how to reach it; the
// wait goes on. Progress goes to stderr: a line that counts on a terminal,
// one line otherwise.
func waitYardmaster(ctx context.Context, cfg *config.Config, st store.API, yardmaster string, wait time.Duration, exited <-chan error) (*store.CrewMember, string) {
	tty := false
	if f, ok := stderr.(*os.File); ok {
		tty = term.IsTerminal(int(f.Fd()))
	}
	if tty {
		// The counting line is wiped once the wait is over: what follows
		// says how it ended.
		defer fmt.Fprint(stderr, "\r\033[K")
	} else {
		fmt.Fprintf(stderr, "waiting for %s to start (up to %s)\n", yardmaster, wait)
	}
	began := time.Now()
	var r *store.CrewMember
	var screenSince time.Time // when the handle first said no screen; zero while it does not
	saidScreen := false
	for {
		var err error
		if r, err = st.GetCrew(ctx, yardmaster); err != nil {
			return nil, fmt.Sprintf("%s could not be read: %v", yardmaster, err)
		}
		elapsed := time.Since(began)
		if tty {
			fmt.Fprintf(stderr, "\rWaiting for %s to start… %ds ", yardmaster, int(elapsed.Seconds()))
		}
		switch {
		case r.Live() && up(ctx, *r):
			return r, ""
		case r.Live() && elapsed >= wait:
			return r, fmt.Sprintf("%s's session is running, but it has not answered its first prompt within %s.", yardmaster, wait)
		case r.Live():
			// Its harness is still coming up, or sits at something. When
			// what the runner recorded says the harness has not drawn its
			// screen (the first prompt pending, not typed for want of it)
			// and that has held for the settle, say once where it waits;
			// the prompt is typed once the screen is up, and the wait goes
			// on as today.
			if pendingNoScreen(*r) {
				if screenSince.IsZero() {
					screenSince = time.Now()
				}
				if !saidScreen && time.Since(screenSince) >= screenSettle {
					saidScreen = true
					if tty {
						fmt.Fprint(stderr, "\r\033[K")
					}
					sayHarnessWaits(ctx, cfg, st, r, yardmaster)
				}
			} else {
				screenSince = time.Time{}
			}
		case !r.Enabled:
			return r, fmt.Sprintf("%s is stopped (disabled), so the daemon does not start it.", yardmaster)
		case r.State == "failed":
			return r, fmt.Sprintf("%s could not be started: the daemon gave up after %d tries.", yardmaster, r.Failures)
		case r.Failures > 0 && len(absent(*r)) > 0:
			return r, fmt.Sprintf("%s cannot start: %s not on PATH.", yardmaster, strings.Join(absent(*r), " and ")+isAre(absent(*r)))
		case elapsed >= wait:
			return r, fmt.Sprintf("%s did not start within %s.", yardmaster, wait)
		}
		select {
		case <-ctx.Done():
			return r, "interrupted while waiting for " + yardmaster + "."
		case <-exited:
			// A daemon that lost the lock to another is no failure: that
			// other one starts the yardmaster.
			exited = nil
			if !daemonRunning(cfg.DaemonLock()) {
				return r, "the daemon stopped right after it started."
			}
		case <-time.After(pollEvery):
		}
	}
}

// pendingNoScreen reports whether r's handle says its harness has not drawn
// its screen, so its first prompt is pending and was not typed: what the
// runner records for a harness that sits at a screen of its own (a welcome,
// a login). The dispatcher types the prompt once the screen is up, which
// takes the pending prompt off the handle and ends the state.
func pendingNoScreen(r store.CrewMember) bool {
	return r.Handle[store.PendingPrompt] != nil && r.Handle[dispatch.PromptLook] == runner.PromptNotTyped
}

// sayHarnessWaits says, the once during the wait, that the yardmaster's
// harness waits for the person in its pane: what the pane shows and how to
// reach it, as the question that offers the pane does (offerPane). It goes
// to stderr, with the wait's progress: stdout is the result's (and JSON's).
func sayHarnessWaits(ctx context.Context, cfg *config.Config, st store.API, r *store.CrewMember, yardmaster string) {
	fmt.Fprintf(stderr, "\n%s's harness waits for you in its pane: answer it there, the prompt is typed once it is up.\n", yardmaster)
	if tail, err := paneTail(ctx, r.Handle, paneLines); err == nil && tail != "" {
		fmt.Fprint(stderr, "\nThe last lines of its pane:\n\n")
		for _, l := range strings.Split(tail, "\n") {
			fmt.Fprintf(stderr, "  | %s\n", l)
		}
	}
	all, _ := st.Settings(ctx)
	if p := placeOf(*r, runner.YardName(cfg.Home, all)); p.Agent != "" {
		fmt.Fprintf(stderr, "\nTo reach it:\n\n"+
			"  herdr agent focus %s     # in herdr: brings its pane to the front\n"+
			"  herdr agent attach %s    # from any other terminal: opens its pane there\n", p.Agent, p.Agent)
	}
}

// up reports whether a running crew member answered its first prompt: the
// prompt was delivered (the handle holds one herdr could not deliver as
// pending) and the agent is working on it, or its pane shows output below
// it.
func up(ctx context.Context, r store.CrewMember) bool {
	if r.Handle[store.PendingPrompt] != nil {
		return false
	}
	if s, err := agentStatus(ctx, r.Handle); err == nil && s == "working" {
		return true
	}
	text, err := paneTail(ctx, r.Handle, paneScan)
	return err == nil && promptAnswered(text)
}

// promptAnswered reports whether a pane's text goes on after the crew member's first
// prompt. A pane without the prompt has no answer to it: a harness at a
// login screen takes the keys and shows none of them.
func promptAnswered(text string) bool {
	i := strings.LastIndex(text, runner.CrewPromptLead)
	if i < 0 {
		return false
	}
	rest := text[i:]
	if j := strings.Index(rest, runner.CrewPromptEnd); j >= 0 {
		rest = rest[j+len(runner.CrewPromptEnd):]
	} else if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[j:]
	} else {
		rest = ""
	}
	return strings.TrimSpace(rest) != ""
}

func isAre(list []string) string {
	if len(list) == 1 {
		return " is"
	}
	return " are"
}

// absent are the programs the crew member's session needs that are not on
// PATH: herdr and its harness.
func absent(r store.CrewMember) []string {
	var out []string
	kind, _ := r.Config["kind"].(string)
	for _, exe := range []string{"herdr", agents.Executable(kind)} {
		if _, err := lookPath(exe); exe != "" && err != nil {
			out = append(out, exe)
		}
	}
	return out
}

// placeOf reads where a running crew member is from the handle the runner
// recorded; the workspace's label is the one the runner gave it.
func placeOf(r store.CrewMember, yard string) *yardmasterPlace {
	return &yardmasterPlace{Agent: show(r.Handle["agent"]), Workspace: runner.CrewLabel(yard, r.Name),
		WorkspaceID: show(r.Handle["workspace"]), Tab: show(r.Handle["tab"]), Pane: show(r.Handle["pane"])}
}

// diagnose says what is known about a yardmaster that did not start, and how to
// go on, in plain words. r is the yardmaster as last read, nil when it was not.
func diagnose(ctx context.Context, cfg *config.Config, st store.API, r *store.CrewMember, yardmaster string, res *startResult) (known, next []string) {
	yardr := yardrAt(cfg.Home)
	gaveUp, started := false, res.DaemonStarted
	// ask is the harness to ask for one real answer: of a session that sits
	// silent, or of a start that failed with every program there. The pane
	// and the reason say what happened; the call says whether the harness
	// works at all, without a pane.
	ask := ""
	if r != nil && r.Live() {
		// A session, and a harness that does not answer in it: the pane
		// says why, and the person has to go there.
		kind, _ := r.Config["kind"].(string)
		if r.Handle[store.PendingPrompt] != nil {
			known = append(known, chat.NotReady(kind))
		} else {
			known = append(known, fmt.Sprintf("the first prompt went into the pane, and %s printed nothing after it", kind))
		}
		if res.Pane == "" {
			known = append(known, "its pane shows nothing")
		}
		ask = kind
		p := res.Yardmaster
		next = append(next,
			fmt.Sprintf("look at its pane (agent %s, workspace %q, pane %s): herdr agent focus %s in herdr, "+
				"herdr agent attach %s from any other terminal (%s leaves it again, and stops nothing)", p.Agent, p.Workspace, p.Pane, p.Agent, p.Agent, detachKeys))
		if r.Handle[store.PendingPrompt] != nil {
			next = append(next, fmt.Sprintf("answer what it asks there, or log %s in; the daemon gives %s its first prompt once %s is ready", kind, yardmaster, kind))
		} else {
			next = append(next, fmt.Sprintf("set right what it shows there (log %s in, say), then have the daemon start %s afresh: %s crew restart %s",
				kind, yardmaster, yardr, yardmaster))
		}
	} else if r != nil {
		gaveUp = r.State == "failed"
		kind, _ := r.Config["kind"].(string)
		for _, exe := range absent(*r) {
			if exe == "herdr" {
				known = append(known, "herdr is not on PATH: it starts every agent, so nothing runs until it is installed")
			} else {
				known = append(known, fmt.Sprintf("%s is not on PATH: it is the harness %s runs on (%s)", exe, yardmaster, kind))
			}
		}
		if reason := lastStartFailure(ctx, st, *r); reason != "" {
			known = append(known, "the last start failed with: "+reason)
			// Not when herdr had no server for it: the harness was never
			// started, and doctor's own line says what is wrong.
			if len(absent(*r)) == 0 && !strings.Contains(reason, "server_not_running") && !strings.Contains(reason, "server_unavailable") {
				ask = kind
			}
		} else if r.Enabled {
			known = append(known, "no start has failed so far: the daemon may still be at it")
		}
	}
	switch {
	case !daemonRunning(cfg.DaemonLock()):
		known = append(known, "no daemon is running on this yard")
		next = append(next, fmt.Sprintf("start it in a terminal pane of its own, where you see what it does: %s daemon", yardr))
	case started:
		known = append(known, "the daemon runs in the background and logs to "+cfg.DaemonLog())
	default:
		known = append(known, "the daemon was running already; what it logs is where it was started "+
			"(its pane, or "+cfg.DaemonLog()+" when yardr started it)")
	}
	switch {
	case r != nil && r.Live():
	case r != nil && !r.Enabled:
		next = append(next, fmt.Sprintf("let it run again: %s crew start %s", yardr, yardmaster))
	case gaveUp:
		next = append(next, fmt.Sprintf("once that is fixed, have the daemon try again: %s crew start %s", yardr, yardmaster))
	case r != nil && len(absent(*r)) > 0:
		next = append(next, fmt.Sprintf("install %s where PATH finds it; the daemon looks again on the PATH it was started with, "+
			"every try, and %s crew list shows when %s runs", strings.Join(absent(*r), " and "), yardr, yardmaster))
	default:
		next = append(next, fmt.Sprintf("the daemon keeps trying; see whether %s runs: %s crew list", yardmaster, yardr))
	}
	if ask != "" {
		if k, n := tryAfterFailure(ctx, ask); k != "" {
			known = append(known, k)
			if n != "" {
				next = append(next, n)
			}
		} else {
			next = append(next, tryHint(yardr, ask))
		}
	}
	next = append(next, fmt.Sprintf("this waits for it again and prints where it is: %s init --start", yardr))
	return known, next
}

// tryAfterFailure makes the one real call yardr doctor --try makes to the
// harness of kind (tryHarnessCall), once, after a start that failed on it,
// and says what it came to: the harness's answer for what is known, and where
// it tells, what to do for how to go on. It costs a model call, as the call
// before the start does, and is made without asking, with a terminal or
// without: the failure is reported with the finding, not with a hint at it.
// "" when no call can be made: yardr knows no one-shot form for the kind, or
// its harness is not on PATH here.
func tryAfterFailure(ctx context.Context, kind string) (string, string) {
	exe := agents.Executable(kind)
	if agents.OneShotArgs(kind, tryPrompt) == nil || exe == "" {
		return "", ""
	}
	path, err := lookPath(exe)
	if err != nil {
		return "", ""
	}
	switch t := tryHarnessCall(ctx, kind, path); t.State {
	case tryOK:
		return fmt.Sprintf("one real call to %s outside a pane answered (%s)", kind, or(t.Output, "no output")), ""
	case tryFails:
		return fmt.Sprintf("one real call to %s outside a pane failed: %s", kind, or(t.Output, "no output")),
			strings.ReplaceAll(t.means(kind, exe), "\n", " ")
	case tryHangs:
		text := fmt.Sprintf("one real call to %s outside a pane did not end within %s", kind, tryTimeout)
		if t.Output != "" {
			text += "; it printed: " + t.Output
		}
		return text, strings.ReplaceAll(t.means(kind, exe), "\n", " ")
	}
	return "", ""
}

// tryHint is the step that names yardr doctor --try, for a crew member on
// harness kind whose start failed or whose session does not answer, when the
// call itself cannot be made here (tryAfterFailure).
func tryHint(yardr, kind string) string {
	return fmt.Sprintf("see whether %s answers at all, outside a pane: %s doctor --try makes one real call to it "+
		"and prints what it says (a login that is needed, a flag it does not know); it costs a model call", or(kind, "the harness"), yardr)
}

// lastStartFailure is the reason of the crew member's latest failed start, ""
// when none is logged.
func lastStartFailure(ctx context.Context, st store.API, r store.CrewMember) string {
	evs, err := st.Events(ctx, r.Journal, 50)
	if err != nil {
		return ""
	}
	var seq uint64
	reason := ""
	for _, e := range evs {
		switch store.EventKind(e.Kind) {
		case "crew_start_failed", "crew_gave_up", "crew_blocked":
			if s, _ := e.Data["reason"].(string); s != "" && e.Seq >= seq {
				seq, reason = e.Seq, s
			}
		}
	}
	return reason
}

// print says how the start ended: where the yardmaster is and how to get there,
// or what is known and how to go on.
func (s *startResult) print(yardmaster string) {
	s.printHead()
	if !s.Running {
		s.printKnownNext()
		return
	}
	fmt.Println()
	s.Yardmaster.print(yardmaster)
}

// printKnownNext prints what is known and how to go on, for a yardmaster that
// is not up: after the head (print), or after the problem and its pane where
// the head was said already (the setup's way to a silent yardmaster).
func (s *startResult) printKnownNext() {
	if len(s.Known) > 0 {
		fmt.Println("\nWhat is known:")
		for _, k := range s.Known {
			fmt.Printf("  - %s\n", k)
		}
	}
	if len(s.Next) > 0 {
		fmt.Println("\nHow to go on:")
		for _, n := range s.Next {
			fmt.Printf("  - %s\n", n)
		}
	}
}

// printHead says, once, what print starts with: the daemon, and of a yardmaster
// that is not up the problem and the last lines of its pane.
func (s *startResult) printHead() {
	if s.said {
		return
	}
	s.said = true
	if s.DaemonStarted {
		fmt.Printf("The daemon is started, in the background; it logs to %s.\n", s.DaemonLog)
	} else if s.Running || len(s.Known) > 0 {
		fmt.Println("The daemon was running already.")
	}
	if !s.Running {
		s.printProblem()
	}
}

// printProblem says why the yardmaster is not up, with the last lines of its
// pane.
func (s *startResult) printProblem() {
	fmt.Printf("\n%s\n", s.Problem)
	if s.Pane != "" {
		fmt.Printf("\nThe last lines of its pane:\n\n")
		for _, l := range strings.Split(s.Pane, "\n") {
			fmt.Printf("  | %s\n", l)
		}
	}
}

// print says where the running yardmaster is, as herdr names it, and how to get
// there.
func (p *yardmasterPlace) print(yardmaster string) {
	fmt.Printf("%s is running, in herdr:\n\n"+
		"  agent      %s\n"+
		"  workspace  %q (%s), tab %s, pane %s\n\n"+
		"To get there:\n\n"+
		"  herdr agent focus %s     # in herdr: brings its pane to the front\n"+
		"  herdr agent attach %s    # from any other terminal: opens its pane there\n",
		yardmaster, p.Agent, p.Workspace, p.WorkspaceID, p.Tab, p.Pane, p.Agent, p.Agent)
}

// openYardmaster brings the yardmaster's pane to the front. herdr focuses a
// pane for any process that asks over its socket; what the person then sees
// depends on where they sit, which the answer says.
func openYardmaster(ctx context.Context, yardmaster string, p *yardmasterPlace) {
	if err := focusAgent(ctx, p.Agent); err != nil {
		fmt.Printf("%s's pane could not be brought to the front: %v\n", yardmaster, err)
		return
	}
	if os.Getenv("HERDR_ENV") != "" {
		fmt.Printf("herdr shows %s now.\n", yardmaster)
	} else {
		fmt.Printf("%s is in front in herdr; this terminal is not in herdr, so open it with `herdr`,\n"+
			"or `herdr agent attach %s` for its pane alone.\n", yardmaster, p.Agent)
	}
}

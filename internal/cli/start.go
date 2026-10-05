package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
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

// mayorWait is how long the onboarding waits for the mayor before it says
// what it knows instead. A start takes seconds; the rest is room for
// a harness that is slow to come up or a first failed try (the daemon
// retries after 30s). A variable so a test of the form, which has no
// --wait, need not sit it out.
var mayorWait = 2 * time.Minute

// pollEvery is how often the wait looks at the mayor; tests shorten it.
var pollEvery = 500 * time.Millisecond

// preflight runs before the form's first page: it names what is not on
// PATH and what it is needed for (basicNeeds, as yardr doctor does), and lets
// the user stop or go on. The answer starts at "go on" only when herdr is
// there: without herdr the yard this form creates cannot start its mayor.
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

// mayorPlace is where the mayor's session runs, as herdr names it.
type mayorPlace struct {
	Agent       string `json:"agent"`        // herdr agent list's name
	Workspace   string `json:"workspace"`    // the workspace's label
	WorkspaceID string `json:"workspace_id"` // and its id, the tab's and the pane's
	Tab         string `json:"tab"`
	Pane        string `json:"pane"`
}

// startResult is what starting the yard came to: the daemon, and the mayor's
// place once its session runs or, when it does not, what is known and how
// to go on.
type startResult struct {
	DaemonStarted bool   `json:"daemon_started"`       // by this run; false when one ran already
	DaemonLog     string `json:"daemon_log,omitempty"` // where the one this run started logs
	// Running: the mayor's session runs and it answered its first prompt
	// (see waitMayor).
	Running bool `json:"running"`
	// Mayor is where the session is, once there is one, up or not.
	Mayor *mayorPlace `json:"mayor,omitempty"`
	// Pane is the last lines of the mayor's pane when it did not come up:
	// where a harness says why it does nothing.
	Pane string `json:"pane,omitempty"`
	// Chat is the command for the conversation with the mayor, once it is
	// up: where its first message goes, and where a terminal is taken.
	Chat    string   `json:"chat,omitempty"`
	Problem string   `json:"problem,omitempty"`
	Known   []string `json:"known,omitempty"`
	Next    []string `json:"next,omitempty"`
}

// spawnDaemon starts `yardr daemon` for the home, detached from this
// terminal and logging to the home's daemon.log, the way server.Start starts
// the server. exited gets the daemon's end, should it come. Tests replace
// it.
//
// Unlike the server, the daemon keeps the starter's environment: it is what
// a daemon in a pane of its own has, and the runners need it (herdr's
// socket, the PATH an exec group's command runs with). See daemonEnv.
var spawnDaemon = func(cfg *config.Config) (exited <-chan error, err error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(cfg.DaemonLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	cmd := exec.Command(self, "daemon")
	cmd.Env = daemonEnv(os.Environ(), cfg.Home)
	cmd.Dir = cfg.Home
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done, nil
}

// daemonEnv is the environment a daemon started in the background gets,
// from the starter's environ: all of it but what names the starter (its
// actor, bead, session, stage, group, resident: init may be run by an agent, and the
// daemon is nobody's session), with the home it works for in place of
// whatever YARDR_HOME said.
func daemonEnv(environ []string, home string) []string {
	var env []string
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); !config.IsVar(k, "HOME", "ACTOR", "BEAD", "SESSION", "RESIDENT", "STAGE", "GROUP") {
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
	// paneLines is how much of the mayor's pane a failed wait quotes.
	paneLines = 6
	// paneScan is how far back the wait looks for the first prompt.
	paneScan = 200
)

// startYard starts the daemon in the background, unless one runs on the
// home already, and waits up to wait for the mayor (waitMayor). It never
// fails: what went wrong is in the result, in words for the person reading.
func (a *app) startYard(ctx context.Context, cfg *config.Config, st store.API, mayor string, wait time.Duration) *startResult {
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
	r, problem := waitMayor(ctx, cfg, st, mayor, wait, exited)
	if r != nil && r.Live() {
		all, _ := st.Settings(ctx)
		res.Mayor = placeOf(*r, runner.YardName(cfg.Home, all))
	}
	if problem == "" {
		res.Running, res.Chat = true, yardrAt(cfg.Home)+" chat"
		return res
	}
	res.Problem = problem
	if res.Mayor != nil {
		res.Pane, _ = paneTail(ctx, r.Handle, paneLines)
	}
	res.Known, res.Next = diagnose(ctx, cfg, st, r, mayor, res)
	return res
}

// waitMayor polls the mayor until it is up, and returns "" then. Up is more
// than a session that exists: a harness on PATH can sit dead in a running
// session (a quarantined binary behind a dialog nobody sees, a CLI at "Not
// logged in"), and herdr reads any process of the harness's name that
// printed something as an idle agent. So the wait is for the mayor's first
// output: what its pane shows after its first prompt (see up).
//
// Otherwise it returns why the wait ended: the time is up, the daemon is
// gone, yardr gave up on the mayor, or a start failed for want of a program
// that waiting will not bring. Progress goes to stderr: a line that counts
// on a terminal, one line otherwise.
func waitMayor(ctx context.Context, cfg *config.Config, st store.API, mayor string, wait time.Duration, exited <-chan error) (*store.Resident, string) {
	tty := false
	if f, ok := stderr.(*os.File); ok {
		tty = term.IsTerminal(int(f.Fd()))
	}
	if tty {
		// The counting line is wiped once the wait is over: what follows
		// says how it ended.
		defer fmt.Fprint(stderr, "\r\033[K")
	} else {
		fmt.Fprintf(stderr, "waiting for %s to start (up to %s)\n", mayor, wait)
	}
	began := time.Now()
	var r *store.Resident
	for {
		var err error
		if r, err = st.GetResident(ctx, mayor); err != nil {
			return nil, fmt.Sprintf("%s could not be read: %v", mayor, err)
		}
		elapsed := time.Since(began)
		if tty {
			fmt.Fprintf(stderr, "\rWaiting for %s to start… %ds ", mayor, int(elapsed.Seconds()))
		}
		switch {
		case r.Live() && up(ctx, *r):
			return r, ""
		case r.Live() && elapsed >= wait:
			return r, fmt.Sprintf("%s's session is running, but it has not answered its first prompt within %s.", mayor, wait)
		case r.Live():
			// Its harness is still coming up, or sits at something.
		case !r.Enabled:
			return r, fmt.Sprintf("%s is stopped (disabled), so the daemon does not start it.", mayor)
		case r.State == "failed":
			return r, fmt.Sprintf("%s could not be started: the daemon gave up after %d tries.", mayor, r.Failures)
		case r.Failures > 0 && len(absent(*r)) > 0:
			return r, fmt.Sprintf("%s cannot start: %s not on PATH.", mayor, strings.Join(absent(*r), " and ")+isAre(absent(*r)))
		case elapsed >= wait:
			return r, fmt.Sprintf("%s did not start within %s.", mayor, wait)
		}
		select {
		case <-ctx.Done():
			return r, "interrupted while waiting for " + mayor + "."
		case <-exited:
			// A daemon that lost the lock to another is no failure: that
			// other one starts the mayor.
			exited = nil
			if !daemonRunning(cfg.DaemonLock()) {
				return r, "the daemon stopped right after it started."
			}
		case <-time.After(pollEvery):
		}
	}
}

// up reports whether a running resident answered its first prompt: the
// prompt was delivered (the handle holds one herdr could not deliver as
// pending) and the agent is working on it, or its pane shows output below
// it.
func up(ctx context.Context, r store.Resident) bool {
	if r.Handle[dispatch.PendingPrompt] != nil {
		return false
	}
	if s, err := agentStatus(ctx, r.Handle); err == nil && s == "working" {
		return true
	}
	text, err := paneTail(ctx, r.Handle, paneScan)
	return err == nil && promptAnswered(text)
}

// promptAnswered reports whether a pane's text goes on after the resident's first
// prompt. A pane without the prompt has no answer to it: a harness at a
// login screen takes the keys and shows none of them.
func promptAnswered(text string) bool {
	i := strings.LastIndex(text, runner.ResidentPromptLead)
	if i < 0 {
		return false
	}
	rest := text[i:]
	if j := strings.Index(rest, runner.ResidentPromptEnd); j >= 0 {
		rest = rest[j+len(runner.ResidentPromptEnd):]
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

// absent are the programs the resident's session needs that are not on
// PATH: herdr and its harness.
func absent(r store.Resident) []string {
	var out []string
	kind, _ := r.Config["kind"].(string)
	for _, exe := range []string{"herdr", agents.Executable(kind)} {
		if _, err := lookPath(exe); exe != "" && err != nil {
			out = append(out, exe)
		}
	}
	return out
}

// placeOf reads where a running resident is from the handle the runner
// recorded; the workspace's label is the one the runner gave it.
func placeOf(r store.Resident, yard string) *mayorPlace {
	return &mayorPlace{Agent: show(r.Handle["agent"]), Workspace: runner.ResidentLabel(yard, r.Name),
		WorkspaceID: show(r.Handle["workspace"]), Tab: show(r.Handle["tab"]), Pane: show(r.Handle["pane"])}
}

// diagnose says what is known about a mayor that did not start, and how to
// go on, in plain words. r is the mayor as last read, nil when it was not.
func diagnose(ctx context.Context, cfg *config.Config, st store.API, r *store.Resident, mayor string, res *startResult) (known, next []string) {
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
		if r.Handle[dispatch.PendingPrompt] != nil {
			known = append(known, chat.NotReady(kind))
		} else {
			known = append(known, fmt.Sprintf("the first prompt went into the pane, and %s printed nothing after it", kind))
		}
		if res.Pane == "" {
			known = append(known, "its pane shows nothing")
		}
		ask = kind
		p := res.Mayor
		next = append(next,
			fmt.Sprintf("look at its pane (agent %s, workspace %q, pane %s): herdr agent focus %s in herdr, "+
				"herdr agent attach %s from any other terminal", p.Agent, p.Workspace, p.Pane, p.Agent, p.Agent))
		if r.Handle[dispatch.PendingPrompt] != nil {
			next = append(next, fmt.Sprintf("answer what it asks there, or log %s in; the daemon gives %s its first prompt once %s is ready", kind, mayor, kind))
		} else {
			next = append(next, fmt.Sprintf("set right what it shows there (log %s in, say), then have the daemon start %s afresh: %s resident restart %s",
				kind, mayor, yardr, mayor))
		}
	} else if r != nil {
		gaveUp = r.State == "failed"
		kind, _ := r.Config["kind"].(string)
		for _, exe := range absent(*r) {
			if exe == "herdr" {
				known = append(known, "herdr is not on PATH: it starts every agent, so nothing runs until it is installed")
			} else {
				known = append(known, fmt.Sprintf("%s is not on PATH: it is the harness %s runs on (%s)", exe, mayor, kind))
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
		next = append(next, fmt.Sprintf("let it run again: %s resident start %s", yardr, mayor))
	case gaveUp:
		next = append(next, fmt.Sprintf("once that is fixed, have the daemon try again: %s resident start %s", yardr, mayor))
	case r != nil && len(absent(*r)) > 0:
		next = append(next, fmt.Sprintf("install %s where PATH finds it; the daemon looks again on the PATH it was started with, "+
			"every try, and %s resident list shows when %s runs", strings.Join(absent(*r), " and "), yardr, mayor))
	default:
		next = append(next, fmt.Sprintf("the daemon keeps trying; see whether %s runs: %s resident list", mayor, yardr))
	}
	if ask != "" {
		next = append(next, tryHint(yardr, ask))
	}
	next = append(next, fmt.Sprintf("this waits for it again and prints where it is: %s init --start", yardr))
	return known, next
}

// tryHint is the step that names yardr doctor --try, for a resident on
// harness kind whose start failed or whose session does not answer.
func tryHint(yardr, kind string) string {
	return fmt.Sprintf("see whether %s answers at all, outside a pane: %s doctor --try makes one real call to it "+
		"and prints what it says (a login that is needed, a flag it does not know); it costs a model call", or(kind, "the harness"), yardr)
}

// lastStartFailure is the reason of the resident's latest failed start, ""
// when none is logged.
func lastStartFailure(ctx context.Context, st store.API, r store.Resident) string {
	evs, err := st.Events(ctx, r.Journal, 50)
	if err != nil {
		return ""
	}
	var seq uint64
	reason := ""
	for _, e := range evs {
		switch e.Kind {
		case "resident_start_failed", "resident_gave_up", "resident_blocked":
			if s, _ := e.Data["reason"].(string); s != "" && e.Seq >= seq {
				seq, reason = e.Seq, s
			}
		}
	}
	return reason
}

// print says how the start ended: where the mayor is and how to get there,
// or what is known and how to go on.
func (s *startResult) print(mayor string) {
	if s.DaemonStarted {
		fmt.Printf("The daemon is started, in the background; it logs to %s.\n", s.DaemonLog)
	} else if s.Running || len(s.Known) > 0 {
		fmt.Println("The daemon was running already.")
	}
	if !s.Running {
		fmt.Printf("\n%s\n", s.Problem)
		if s.Pane != "" {
			fmt.Printf("\nThe last lines of its pane:\n\n")
			for _, l := range strings.Split(s.Pane, "\n") {
				fmt.Printf("  | %s\n", l)
			}
		}
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
		return
	}
	fmt.Println()
	s.Mayor.print(mayor)
}

// print says where the running mayor is, as herdr names it, and how to get
// there.
func (p *mayorPlace) print(mayor string) {
	fmt.Printf("%s is running, in herdr:\n\n"+
		"  agent      %s\n"+
		"  workspace  %q (%s), tab %s, pane %s\n\n"+
		"To get there:\n\n"+
		"  herdr agent focus %s     # in herdr: brings its pane to the front\n"+
		"  herdr agent attach %s    # from any other terminal: opens its pane there\n",
		mayor, p.Agent, p.Workspace, p.WorkspaceID, p.Tab, p.Pane, p.Agent, p.Agent)
}

// openMayor brings the mayor's pane to the front. herdr focuses a pane for
// any process that asks over its socket; what the person then sees depends
// on where they sit, which the answer says.
func openMayor(ctx context.Context, mayor string, p *mayorPlace) {
	if err := focusAgent(ctx, p.Agent); err != nil {
		fmt.Printf("%s's pane could not be brought to the front: %v\n", mayor, err)
		return
	}
	if os.Getenv("HERDR_ENV") != "" {
		fmt.Printf("herdr shows %s now.\n", mayor)
	} else {
		fmt.Printf("%s is in front in herdr; this terminal is not in herdr, so open it with `herdr`,\n"+
			"or `herdr agent attach %s` for its pane alone.\n", mayor, p.Agent)
	}
}

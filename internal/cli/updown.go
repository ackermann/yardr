package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

// yardr up and yardr down are the yard's switch: one word that starts, or
// stops, everything a yard needs. Neither is a second way to start or stop
// a part. up is herdr's own `herdr server`, the server every command
// starts (connect) and yardr daemon start; down is yardr daemon stop, what
// the daemon does to a session it ends (the runner's Stop), and yardr
// server stop. What they add is the order, the agents on the way down, and
// one answer for all of it.

// The parts of a yard up and down speak of, beside the long-running
// commands, which are named as they are (svcDaemon and the rest).
const (
	partHerdr  = "herdr"
	partServer = "server"
)

// yardPart is one part of the yard in the answer of up and of down: whether
// it runs now, what this run did to it, and what it could not do, with the
// one thing to do about that.
type yardPart struct {
	Part    string `json:"part"` // herdr server daemon web bdp otel
	Running bool   `json:"running"`
	// Started and Stopped are what this run did; neither for a part it
	// found as it should be, or could not change.
	Started bool   `json:"started,omitempty"`
	Stopped bool   `json:"stopped,omitempty"`
	PID     int    `json:"pid,omitempty"`
	URL     string `json:"url,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Error is what could not be done, and Do the one thing to do about it.
	Error string `json:"error,omitempty"`
	Do    string `json:"do,omitempty"`
}

// partNames are the parts in words, as the lines of up and down begin.
var partNames = map[string]string{partHerdr: "herdr's server", partServer: "server"}

func (p yardPart) name() string {
	if n, ok := partNames[p.Part]; ok {
		return n
	}
	return what(p.Part)
}

// line prints the part: did is what to say of one this run changed or found
// as it should be.
func (p yardPart) line(did string) {
	if p.Error != "" {
		fmt.Printf("%-15s NOT %s\n", p.name(), p.Error)
		if p.Do != "" {
			fmt.Printf("%-15s   %s\n", "", p.Do)
		}
		return
	}
	if p.PID != 0 {
		did += fmt.Sprintf(" (pid %d)", p.PID)
	}
	if p.URL != "" {
		did += ": " + p.URL
	}
	if p.Detail != "" {
		did += ": " + p.Detail
	}
	fmt.Printf("%-15s %s\n", p.name(), did)
}

// upReport is the answer of yardr up.
type upReport struct {
	Home string `json:"home"`
	// Up is whether the three a yard works with run now: herdr's server,
	// the yard's server and the daemon.
	Up    bool       `json:"up"`
	Parts []yardPart `json:"parts"`
	// Login are the login services, with --login: what was installed,
	// what was found, what was left.
	Login []loginUnit `json:"login,omitempty"`
}

// spawnHerdr starts herdr's headless server, `herdr server`, detached from
// this terminal as the yard's own server is, logging to log; tests replace
// it. It is the server of the herdr session the yard names when it names
// one (runner.HerdrCommand). One server serves every yard of the machine
// and the person's own panes, so it gets nothing that names this yard or
// its caller (no YARDR_*), and runs in the person's home.
var spawnHerdr = func(bin, log string) (exited <-chan error, err error) {
	cmd := runner.HerdrCommand(context.Background(), bin, "server")
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return config.IsVar(k)
	})
	if dir, err := os.UserHomeDir(); err == nil {
		cmd.Dir = dir
	}
	return server.SpawnDetached(cmd, log)
}

// herdrLog is where a herdr server yardr up started logs.
func herdrLog(cfg *config.Config) string { return filepath.Join(cfg.Home, "herdr-server.log") }

// stopServer stops the yard's server, as yardr server stop does; tests
// replace it.
var stopServer = server.Stop

// yardRunners are the runners the yard's sessions were started by, for down
// to ask each about its agent and to stop it as the daemon would; tests
// replace it.
var yardRunners = func(home string, st store.API) map[string]dispatch.Runner {
	return runner.All(home, st, slog.New(slog.DiscardHandler), nil)
}

// herdrProgram is the herdr on this machine: the one on PATH, else the one
// the user's login shell finds, as yardr doctor looks for a program
// (loginShellFinds). A window starts yardr without what a profile adds to
// PATH, and what up starts there (herdr's server, the daemon, and through
// them every agent) would lack it too, so with a herdr only the login shell
// finds this process takes the login shell's PATH after its own. looked is
// where it was looked for, for a herdr that is nowhere.
func herdrProgram(ctx context.Context) (path, looked string) {
	if p, err := lookPath("herdr"); err == nil {
		return p, ""
	}
	looked = "PATH (" + os.Getenv("PATH") + ")"
	shell := loginShell()
	if shell == "" {
		return "", looked + "; SHELL names no login shell to ask"
	}
	looked += " and the login shell " + shell
	p := loginShellFinds(ctx, []string{"herdr"})["herdr"]
	if p == "" {
		return "", looked
	}
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for _, d := range append(filepath.SplitList(loginShellPATH(ctx)), filepath.Dir(p)) {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	os.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
	return p, looked
}

func (a *app) upCmd() *cobra.Command {
	var login bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start everything the yard needs that is not running",
		Long: `Bring up what the yard needs and is not running, in this order:

  herdr's server  the program that runs every agent's pane. One that answers
                  is left as it is; else yardr starts herdr's headless
                  server (herdr server) in the background, logging to
                  <home>/herdr-server.log. It is the server of the herdr
                  session the yard names (the herdr_session setting), else
                  of this shell's.
  server          the process that owns the yard's store
  daemon          as yardr daemon start does. It starts the crew, the
                  yardmaster first, and dispatches the beads that wait:
                  those yardr down stopped are worked again.
  web view        only when the yard's web_addr setting names its address,
  BDP server      and likewise bdp_addr: as yardr web start and yardr bdp
                  start do. The telemetry export is not started.

A part that runs already is left alone, so yardr up is safe to repeat. Each
part has one line: running already, started, or NOT with what kept it and the
one thing to do about it. The daemon is not started while herdr's server
does not answer: every start of an agent would fail and count against it.

herdr is looked for on PATH, then with your login shell, as yardr doctor
looks: a window starts yardr without what your profile adds to PATH. When
only the login shell finds it, what up starts gets that shell's PATH.

--json prints the same as data: each part with running, started, pid, and
error and do for what could not be started; up is whether herdr's server,
the server and the daemon all run. The exit status is 0 when they do, 1
when one does not, 2 on a home with no yard.

--login makes it stay: what up starts is started again at every login, by a
launchd agent each on macOS, a systemd user unit on Linux.

  herdr's server  one service for the user, not for the yard: one server
                  serves every yard. It runs the herdr your login shell
                  finds, with that shell's PATH, which the agents' panes
                  inherit. A second yard finds it and writes nothing. A
                  service for herdr's server that yardr did not write (one
                  saved by hand, under any name) is left as it is, and
                  yardr writes none beside it. A server that runs now is not
                  restarted: the service starts it from the next login on.
  daemon          as yardr daemon install does: the daemon that runs is
                  stopped, and the service's runs in its place.
  web view        only with web_addr set, and the BDP server with bdp_addr,
                  as yardr web install and yardr bdp install do.

Each has a line under "At login": installed, installed already, or left as
it is, with the file. Both restart on a failure only: herdr server stop and
herdr update are not fought, and a server stopped by hand stays stopped
until the next login or the next yardr up. An update replaces herdr's
program where the service's file names it, so the service runs the new one
from its next start. yardr down --login removes them. With --json the same
is "login": each with part, result (installed, found, left), file, loaded,
foreign (the files that are not yardr's) and error. The exit status is 1
too when a service could not be installed.`,
		Example: `  yardr up
  yardr up --login                       # and at every login from now on
  yardr up --json | jq -r '.parts[] | select(.error) | .do'
  yardr --home /srv/yard up`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			rep, err := a.up(c.Context(), cfg, login)
			if err != nil {
				return err
			}
			if err := a.out(rep, rep.print); err != nil {
				return err
			}
			if !rep.Up || loginFailed(rep.Login) {
				return errCheckFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&login, "login", false, "start it all at every login too: install the login services of herdr's server and the daemon")
	return cmd
}

// up brings the yard's parts up and reports each. An error is one that
// kept it from looking at all (no yard in the home); what could not be
// started is in the report. With login each part is installed as a login
// service too, and a part that does not run is started by its service.
func (a *app) up(ctx context.Context, cfg *config.Config, login bool) (*upReport, error) {
	if a.st == nil {
		if _, err := os.Stat(cfg.DBPath()); errors.Is(err, os.ErrNotExist) {
			return nil, notInitialized{cfg.Home}
		}
	}
	if login {
		if managerOf() == "" {
			return nil, errNoLogin()
		}
		useLoginPATH(ctx)
	}
	rep := &upReport{Home: cfg.Home}
	yardr := yardrAt(cfg.Home)

	// The yard's server is asked first, though its line comes second: the
	// herdr session the yard's panes are in is a setting in its store.
	srv := yardPart{Part: partServer}
	was := serverRunning(ctx, cfg)
	st, err := a.store(ctx)
	if err == nil {
		var set bool
		if set, err = a.initialized(ctx, cfg); err == nil && !set {
			return nil, notInitialized{cfg.Home}
		}
	}
	var settings map[string]string
	if err == nil {
		settings, err = st.Settings(ctx)
	}
	if err != nil {
		srv.Error, srv.Do = err.Error(), "read "+cfg.LogFile()+", then: "+yardr+" up"
	} else {
		srv.Running, srv.Started = true, !was
		if info, ok := serverInfo(ctx, cfg); ok {
			srv.PID = info.PID
		}
		runner.UseSession(settings[store.HerdrSessionSetting])
	}

	herdr, herdrLogin := a.upHerdr(ctx, cfg, login)
	rep.Parts = append(rep.Parts, herdr, srv)
	if herdrLogin != nil {
		rep.Login = append(rep.Login, *herdrLogin)
	}

	daemon := yardPart{Part: svcDaemon}
	// With --login the daemon is its service's: one that is not installed
	// and loaded is installed, which stops a daemon that runs, as yardr
	// daemon install does.
	install := false
	if login {
		s := stateOf(ctx, cfg, svcDaemon)
		if install = !s.Installed || !s.Loaded; !install {
			rep.Login = append(rep.Login, loginUnit{Part: svcDaemon, Manager: s.Manager, Name: s.Name, File: s.File, Result: loginFound, Loaded: true})
		}
	}
	notInstalled := func() {
		if install {
			rep.Login = append(rep.Login, loginUnit{Part: svcDaemon, Error: "not installed: the daemon could not be started", Do: yardr + " up --login, once it can"})
		}
	}
	start := func() {
		pid, err := kindOf(svcDaemon).start(cfg, nil, "")
		if err != nil {
			daemon.Error, daemon.Do = err.Error(), "read "+cfg.DaemonLog()+", then: "+yardr+" up"
		} else {
			daemon.Running, daemon.Started, daemon.PID = true, true, pid
		}
	}
	running := config.DaemonRunning(cfg.DaemonLock())
	switch {
	case running && (!install || !srv.Running || !herdr.Running):
		daemon.Running, daemon.PID = true, config.DaemonPID(cfg.DaemonLock())
		notInstalled()
	case !srv.Running:
		daemon.Error, daemon.Do = "the server does not run, and the daemon works on its store", yardr+" up, once the server starts"
		notInstalled()
	case !herdr.Running:
		daemon.Error = "herdr's server does not run: every agent the daemon started would fail, and count as a failed start"
		daemon.Do = yardr + " up, once herdr's server runs"
		notInstalled()
	case install:
		l := a.loginInstall(ctx, cfg, kindOf(svcDaemon), nil, func() bool { return config.DaemonRunning(cfg.DaemonLock()) })
		rep.Login = append(rep.Login, l)
		if config.DaemonRunning(cfg.DaemonLock()) {
			daemon.Running, daemon.Started, daemon.PID = true, true, config.DaemonPID(cfg.DaemonLock())
		} else {
			// The yard is to be up all the same.
			start()
		}
	default:
		start()
	}
	rep.Parts = append(rep.Parts, daemon)

	// The parts whose address the yard's settings name: the setting says
	// that the yard has the part.
	for _, k := range longRunnings {
		if k.Setting == "" {
			continue
		}
		p := yardPart{Part: k.Name}
		addr := settings[k.Setting]
		if v := k.EnvAddr(cfg); addr != "" && v != "" {
			addr = v // as yardr web start takes it
		}
		// With --login a part the yard has is its service's too.
		install := login && addr != ""
		if install && serviceInstalled(cfg, k.Name) {
			s := stateOf(ctx, cfg, k.Name)
			rep.Login = append(rep.Login, loginUnit{Part: k.Name, Manager: s.Manager, Name: s.Name, File: s.File, Result: loginFound, Loaded: s.Loaded})
			install = false
		}
		if run := k.running(cfg); len(run) > 0 && !install {
			p.Running, p.PID = true, run[0].PID
			if run[0].Addr != "" {
				p.URL = webURL(run[0].Addr)
			}
		} else if addr != "" {
			err := error(nil)
			if k.Name == svcWeb {
				err = store.CheckWebAddr("--addr", addr, false)
			}
			var pid int
			args, started := []string{"--" + k.Addr, addr}, false
			switch {
			case err != nil && install:
				rep.Login = append(rep.Login, loginUnit{Part: k.Name, Error: "not installed: " + err.Error(), Do: yardr + " up --login"})
			case install:
				rep.Login = append(rep.Login, a.loginInstall(ctx, cfg, k, args, func() bool { return answers(addr) }))
				if started = answers(addr); started {
					if run := k.running(cfg); len(run) > 0 {
						pid = run[0].PID
					}
				}
			}
			// One its service did not start is started all the same.
			if err == nil && !started {
				pid, err = k.start(cfg, args, addr)
			}
			if err != nil {
				p.Error, p.Do = err.Error(), fmt.Sprintf("%s %s start", yardr, k.Name)
			} else {
				p.Running, p.Started, p.PID, p.URL = true, true, pid, webURL(addr)
			}
		}
		rep.Parts = append(rep.Parts, p)
	}
	rep.Up = herdr.Running && srv.Running && daemon.Running
	return rep, nil
}

// upHerdr is herdr's server for up: found running, or started. With login
// it is its login service too, nil without: written unless one is there,
// and what starts a server that does not run.
func (a *app) upHerdr(ctx context.Context, cfg *config.Config, login bool) (p yardPart, l *loginUnit) {
	p = yardPart{Part: partHerdr}
	again := yardrAt(cfg.Home) + " up"
	if login {
		l = &loginUnit{Part: partHerdr}
		again += " --login"
	}
	notInstalled := func(why string) {
		if l != nil {
			l.Error, l.Do = "not installed: "+why, again
		}
	}
	bin, looked := herdrProgram(ctx)
	if bin == "" {
		p.Error = "herdr is not installed, or not where yardr looks: " + looked
		p.Do = "install herdr (https://herdr.dev), then: " + again
		notInstalled("herdr is not installed")
		return p, l
	}
	if looked != "" {
		p.Detail = "herdr is " + bin + ", which your login shell finds and this PATH does not; what is started here gets the login shell's PATH"
	}
	check := herdrServer(ctx, bin)
	if check.State == herdrUnknown {
		p.Error, p.Do = "herdr could not be asked for its server: "+check.Output, "run "+bin+" status server, and see what it says"
		notInstalled("herdr could not be asked whether its server runs")
		return p, l
	}
	var u *unit
	if l != nil {
		u = herdrLoginToWrite(ctx, cfg, bin, l)
	}
	if check.State == herdrUp {
		p.Running = true
		if u != nil {
			installHerdrLogin(ctx, u, l, false)
		}
		return p, l
	}
	log := herdrLog(cfg)
	var exited <-chan error
	if u != nil && installHerdrLogin(ctx, u, l, true) {
		// The manager has started it, and keeps what it prints.
		if log = u.Log; log == "" {
			log = "journalctl --user -u " + u.Name
		}
	} else {
		var err error
		if exited, err = spawnHerdr(bin, log); err != nil {
			p.Error, p.Do = "herdr server could not be started: "+err.Error(), "run herdr in a terminal: it starts its server"
			return p, l
		}
	}
	for deadline := time.Now().Add(startWait); time.Now().Before(deadline); {
		select {
		case <-exited:
			p.Error, p.Do = "herdr server ended right after it started", "read "+log+", or run herdr in a terminal: it starts its server"
			return p, l
		case <-time.After(pollEvery / 5):
		}
		if herdrServer(ctx, bin).State == herdrUp {
			p.Running, p.Started = true, true
			return p, l
		}
	}
	p.Error, p.Do = fmt.Sprintf("herdr server was started and does not answer after %s", startWait), "read "+log
	return p, l
}

func (r *upReport) print() {
	for _, p := range r.Parts {
		switch {
		case p.Error != "" || p.Started:
			p.line("started")
		case p.Running:
			p.line("running already")
		}
		// A part the yard does not have (no address set) has no line.
	}
	printLogin(r.Login)
	yardr := yardrAt(r.Home)
	if !r.Up {
		fmt.Printf("\nThe yard in %s is not up: see NOT above.\n", r.Home)
		return
	}
	fmt.Printf("\nThe yard in %s is up. The daemon starts the crew and dispatches the beads that wait.\n", r.Home)
	fmt.Printf("What runs: %s status. Talk to the yardmaster: %s chat. Stop it all: %s down\n", yardr, yardr, yardr)
	switch {
	case loginFailed(r.Login):
		fmt.Println("It does not all start at login: see NOT under \"At login\".")
	case len(r.Login) > 0:
		fmt.Printf("It starts at every login from now on. To end that: %s down --login\n", yardr)
	}
}

// The states of a yardAgent, as herdr names what an agent does, and gone
// for one whose pane is not there any more.
const (
	agentWorking = "working"
	agentIdle    = "idle"
	agentBlocked = "blocked"
	agentUnknown = "unknown"
	agentGone    = "gone"
)

// yardAgent is an agent the yard started and down stops: a bead's session
// or a crew member.
type yardAgent struct {
	Kind string `json:"kind"` // bead crew
	// Name is the bead, or the crew member.
	Name string `json:"name"`
	// For is the user of a crew member's conversation session.
	For   string `json:"for,omitempty"`
	Stage string `json:"stage,omitempty"`
	Group string `json:"group,omitempty"`
	// Agent is herdr's name for it; "" for a script's session.
	Agent string `json:"agent,omitempty"`
	// State is what it is doing: working (in the middle of a turn), idle,
	// blocked (it waits at a question in its pane), unknown (it could not
	// be asked) or gone.
	State   string `json:"state"`
	Stopped bool   `json:"stopped"`
	// Handoff is whether a crew member wrote its handoff when asked to.
	Handoff bool   `json:"handoff,omitempty"`
	Error   string `json:"error,omitempty"`
	Do      string `json:"do,omitempty"`

	session string
	runner  string
	handle  map[string]any
	// starting is a crew member whose handle is that of a start underway.
	starting bool
}

// busy is whether stopping it now may cost something: it is in a turn, at
// a question, or could not be asked.
func (g yardAgent) busy() bool {
	return g.State == agentWorking || g.State == agentBlocked || g.State == agentUnknown
}

// String is the agent as the warning and a failure name it.
func (g yardAgent) String() string {
	if g.Kind == "crew" {
		name := "crew member " + g.Name
		if g.For != "" {
			name += " for " + g.For
		}
		return name
	}
	return fmt.Sprintf("bead %s (stage %s, group %s)", g.Name, g.Stage, g.Group)
}

// key distinguishes a crew member's line session from each conversation;
// bead session IDs are unique already.
func (g yardAgent) key() string {
	if g.Kind == "crew" {
		return "crew\x00" + g.Name + "\x00" + g.For
	}
	return "bead\x00" + g.session
}

// downReport is the answer of yardr down.
type downReport struct {
	Home string `json:"home"`
	// Down is whether the yard is stopped now: no daemon, no server, and
	// no agent of it left that down could not stop.
	Down bool `json:"down"`
	// Confirm is true when nothing was stopped because the agents below
	// have to be agreed to first: the same command with --yes stops them.
	Confirm bool `json:"confirm"`
	// Agents are the agents the yard started: those it would stop
	// (Confirm), else those it stopped or could not.
	Agents []yardAgent `json:"agents"`
	// Working are those of them in the middle of something, and Idle those
	// that are not.
	Working int        `json:"working"`
	Idle    int        `json:"idle"`
	Parts   []yardPart `json:"parts"`
	// Login are the login services, with --login: what was removed, what
	// was not there, what was left. Empty while nothing was stopped.
	Login []loginUnit `json:"login,omitempty"`
}

// handoffWait is how long down waits by default for the crew to write
// their handoffs: room for one turn of an agent, and short for someone who
// pressed Stop. The crew's own handoff_grace, 15 minutes, is for a rotation
// nobody waits on.
const handoffWait = 2 * time.Minute

func (a *app) downCmd() *cobra.Command {
	var yes, login bool
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop the yard: its agents, the daemon and the server",
		Long: `Stop everything of this yard that runs, in this order:

  daemon          as yardr daemon stop does, first: nothing is dispatched
                  and no crew member started while the rest is stopped
  agents          every agent this yard started, and no other pane in
                  herdr: the crew members and the beads' sessions
  web view, BDP server, telemetry export
                  each that runs: without the yard's server it answers
                  with an error only
  server          as yardr server stop does

herdr's server is never stopped: other yards and your own panes run in it.

A bead whose session is stopped loses nothing but the agent's unfinished
turn. Its worktree and branch stay, it is back in its stage's queue, and its
count of failed attempts is as before (the session ends as aborted, as one
does that herdr could not start), so stopping a yard never holds a bead.
After yardr up the daemon dispatches it again, into the same worktree.

A crew member is asked to write its handoff first (yardr crew handoff) and
is stopped once it has, or after --handoff-wait (default 2m; 0 asks for
none). Its next session, after yardr up, starts from the handoff.

Before it stops anything, down says what it will stop: the agents in the
middle of something first, each by its bead or crew name (working; blocked,
which waits at a question in its pane; unknown, which could not be asked),
then how many are idle.

  - With one of them in the middle of something, a terminal is asked
    whether to go on. Without a terminal (stdin closed, a script) down
    refuses, stops nothing and exits 1. --yes answers yes for both.
  - Idle agents alone are not asked about: nothing of theirs is lost, and
    down says how many it stops.
  - --json never asks. Without --yes, on a yard with any agent, it stops
    nothing and prints them as "agents" with "confirm": true, for a program
    to put the question itself and call again with --yes. With no agent it
    stops the yard.

yardr down is safe to repeat: what is stopped already is said to be. Each
part has one line, and what could not be stopped is marked NOT with the one
thing to do about it; the exit status is then 1.

--login ends what yardr up --login began: once the yard is stopped, its
login services are removed, so that it stays stopped after the next login.
Those are the daemon's, the web view's with web_addr set and the BDP
server's with bdp_addr (one installed by yardr web install on a yard
without the setting is left, and said), and yardr's service for herdr's
server. That last one is the user's, not the yard's: while another yard of
yours starts its daemon at login it is left, and down says which yard; a
service for herdr's server that yardr did not write is never touched. herdr's
server itself runs on. Each has a line under "At login", and is in "login"
with --json: part, result (removed, absent, left), file and error. Nothing
is removed while nothing was stopped (a question not agreed to, a daemon
that could not be stopped).`,
		Example: `  yardr down
  yardr down --yes                       # a script: no question
  yardr down --json | jq '.agents'       # what it would stop; stops nothing
  yardr down --json --yes
  yardr down --yes --handoff-wait 0      # at once: the crew writes no handoff
  yardr down --login                     # and not started at login any more`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			scope := loginKept
			if login {
				scope = loginOfUp
			}
			rep, err := a.down(c.Context(), cfg, yes, wait, scope, nil)
			if err != nil {
				return err
			}
			if err := a.out(rep, rep.print); err != nil {
				return err
			}
			if !rep.Down && !rep.Confirm || loginFailed(rep.Login) {
				return errCheckFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "stop the agents without asking, working ones too")
	cmd.Flags().BoolVar(&login, "login", false, "remove the yard's login services too, and herdr's server's when no other yard needs it")
	cmd.Flags().DurationVar(&wait, "handoff-wait", handoffWait, "how long the crew gets to write its handoffs before it is stopped; 0 asks for none")
	return cmd
}

// errDownRefused ends a down that stopped nothing because nobody could be
// asked about the agents at work.
type errDownRefused struct {
	busy  int
	yardr string
	// cmd is the command that stops them with --yes: down, unless another
	// that stops the yard by it says its own.
	cmd string
}

func (e errDownRefused) Error() string {
	return fmt.Sprintf("%s in the middle of something and nobody to ask (no terminal): nothing was stopped. To stop them all the same: %s %s --yes",
		isAre1(e.busy, "agent"), e.yardr, or(e.cmd, "down"))
}

// isAre1 is "1 agent is", "3 agents are".
func isAre1(n int, one string) string {
	if n == 1 {
		return "1 " + one + " is"
	}
	return fmt.Sprintf("%d %ss are", n, one)
}

// loginScope is which of the yard's login services down removes once the
// yard is stopped.
type loginScope int

const (
	loginKept loginScope = iota
	// loginOfUp are those yardr up --login installs: down --login.
	loginOfUp
	// loginAll is every one yardr wrote for the yard, whichever command
	// did: yardr uninstall.
	loginAll
)

// down stops the yard and reports each part and agent. An error is one
// that kept it from stopping anything. first, when given, runs once the
// yard's store answers and before anything is asked or stopped: an error
// of it ends down with the yard as it was found.
func (a *app) down(ctx context.Context, cfg *config.Config, yes bool, wait time.Duration, login loginScope, first func(store.API) error) (*downReport, error) {
	if a.st == nil {
		if _, err := os.Stat(cfg.DBPath()); errors.Is(err, os.ErrNotExist) {
			return nil, notInitialized{cfg.Home}
		}
	}
	switch {
	case managerOf() != "":
	case login == loginOfUp:
		return nil, errNoLogin()
	default:
		// A system yardr writes no login service for has none to remove.
		login = loginKept
	}
	rep := &downReport{Home: cfg.Home, Agents: []yardAgent{}}
	yardr := yardrAt(cfg.Home)
	serverWas := serverRunning(ctx, cfg)
	// The agents are in the store, so a server that was stopped is started
	// to ask it: agents can outlive it, and one that runs starts it again.
	st, err := a.store(ctx)
	if err != nil {
		return nil, err
	}
	if set, err := a.initialized(ctx, cfg); err != nil {
		return nil, err
	} else if !set {
		return nil, notInitialized{cfg.Home}
	}
	restoreInspectionServer := func() error {
		if serverWas {
			return nil
		}
		return stopServer(cfg)
	}
	if first != nil {
		if err := first(st); err != nil {
			if cleanupErr := restoreInspectionServer(); cleanupErr != nil {
				return nil, fmt.Errorf("%w; the yard server, started to read the yard, could not be stopped: %v", err, cleanupErr)
			}
			return nil, err
		}
	}
	// Where herdr is, for the runners that ask it: found as up finds it.
	herdrBin, _ := herdrProgram(ctx)
	herdr := yardPart{Part: partHerdr, Detail: "left running: other yards and your own panes are in it"}
	noHerdr := false
	if herdrBin == "" {
		herdr.Detail = "not found on this machine"
	} else {
		check := herdrServer(ctx, herdrBin)
		herdr.Running, noHerdr = check.State == herdrUp, check.State == herdrDown
		if !herdr.Running {
			herdr.Detail = "not running"
		}
	}
	runners := yardRunners(cfg.Home, st)
	agents, err := yardAgents(ctx, st, runners, noHerdr)
	if err != nil {
		return nil, err
	}
	rep.count(agents)
	daemon := yardPart{Part: svcDaemon, Running: config.DaemonRunning(cfg.DaemonLock()), PID: config.DaemonPID(cfg.DaemonLock())}
	// asFound is the answer of a down that stopped no agent: the parts as
	// they are.
	asFound := func(srv yardPart) []yardPart {
		return append(append([]yardPart{daemon}, others(cfg)...), srv, herdr)
	}

	// Agents that are gone are no agents to ask about. The list is useful
	// even when --yes answered the question for a script: it is still what
	// this command is about to stop.
	hasAgents := rep.Working+rep.Idle > 0
	if hasAgents && !a.json {
		rep.warn(agents)
	}
	if !yes && hasAgents {
		switch {
		case a.json:
			rep.Confirm, rep.Agents = true, agents
			srv := yardPart{Part: partServer, Running: true}
			if err := restoreInspectionServer(); err != nil {
				srv.Running, srv.Error, srv.Do = true, err.Error(), yardr+" server stop"
			} else if !serverWas {
				// As it was found: nothing is to have changed.
				srv.Running = false
			}
			rep.Parts = asFound(srv)
			return rep, nil
		case rep.Working > 0 && !interactive():
			refused := errDownRefused{busy: rep.Working, yardr: yardr}
			if err := restoreInspectionServer(); err != nil {
				return nil, fmt.Errorf("%w; the yard server, started to read its agents, could not be stopped: %v", refused, err)
			}
			return nil, refused
		case rep.Working > 0:
			goOn := false
			if err := ask(huh.NewConfirm().Title(fmt.Sprintf("Stop the yard, with %s in the middle of something?", plural(rep.Working, "agent"))).
				Description("What they have not committed stays in their worktrees; the turn they are in is lost.").
				Affirmative("Stop them").Negative("Leave the yard running").Value(&goOn)); err != nil && !errors.Is(err, huh.ErrUserAborted) {
				if cleanupErr := restoreInspectionServer(); cleanupErr != nil {
					return nil, fmt.Errorf("%v; the yard server, started to read its agents, could not be stopped: %w", err, cleanupErr)
				}
				return nil, err
			}
			if !goOn {
				if err := restoreInspectionServer(); err != nil {
					return nil, fmt.Errorf("nothing was stopped; the yard server, started to read its agents, could not be stopped: %w", err)
				}
				return nil, errors.New("nothing was stopped")
			}
		default:
			// Idle agents alone do not ask; the warning above is enough.
		}
	}

	// The daemon first: one that runs dispatches a bead whose session was
	// stopped, and starts a crew member that was, within the pass.
	pid, err := stopDaemon(cfg, stopWait)
	if err != nil {
		daemon.Error, daemon.Do = err.Error(), fmt.Sprintf("%s daemon stop --wait 2m, then: %s down", yardr, yardr)
		rep.Agents = agents
		rep.Parts = asFound(yardPart{Part: partServer, Running: true})
		return rep, nil
	}
	daemon.Running, daemon.Stopped, daemon.PID = false, pid != 0, pid
	// Read again: the daemon's last pass may have started one more.
	if agents, err = yardAgents(ctx, st, runners, noHerdr); err != nil {
		return nil, err
	}
	rep.count(agents)
	a.handoffs(ctx, st, runners, agents, wait)
	for i := range agents {
		stopAgent(ctx, st, runners, &agents[i], noHerdr, yardr)
	}
	rep.Agents = agents

	// What else of the yard runs serves nothing without its server: a web
	// view left running answers every request with an error.
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	var stopped []yardPart
	for _, k := range longRunnings[1:] {
		p := yardPart{Part: k.Name}
		run, err := k.stop(cfg, stopWait)
		switch {
		case err != nil:
			p.Running, p.Error, p.Do = true, err.Error(), fmt.Sprintf("%s %s stop", yardr, k.Name)
		case len(run) > 0:
			p.Stopped, p.PID = true, run[0].PID
			if settings[k.Setting] == "" {
				// up starts a part by the setting that names its address.
				p.Detail = fmt.Sprintf("yardr up does not start it again: %s %s start", yardr, k.Name)
			}
		}
		stopped = append(stopped, p)
	}

	// The services go once nothing of theirs runs, and before the server
	// does: the herdr session whose server one of them starts is the yard's
	// setting.
	if login != loginKept {
		runner.UseSession(settings[store.HerdrSessionSetting])
		rep.Login = downLogin(ctx, cfg, settings, login == loginAll)
	}

	srv := yardPart{Part: partServer}
	info, _ := serverInfo(ctx, cfg)
	if err := stopServer(cfg); err != nil {
		srv.Running, srv.Error, srv.Do = true, err.Error(), yardr+" server stop"
	} else {
		// One started above only to be asked was not running.
		if srv.Stopped = serverWas; serverWas {
			srv.PID = info.PID
		}
	}
	rep.Parts = append(append([]yardPart{daemon}, stopped...), srv, herdr)
	rep.Down = !srv.Running && !daemon.Running &&
		!slices.ContainsFunc(agents, func(g yardAgent) bool { return !g.Stopped }) &&
		!slices.ContainsFunc(stopped, func(p yardPart) bool { return p.Error != "" })
	return rep, nil
}

// others are the long-running parts after the daemon (the first of
// longRunnings) as they are now.
func others(cfg *config.Config) []yardPart {
	var out []yardPart
	for _, k := range longRunnings[1:] {
		p := yardPart{Part: k.Name}
		if run := k.running(cfg); len(run) > 0 {
			p.Running, p.PID = true, run[0].PID
		}
		out = append(out, p)
	}
	return out
}

// count sets how many of agents are in the middle of something and how
// many are idle.
func (r *downReport) count(agents []yardAgent) {
	r.Working, r.Idle = 0, 0
	for _, g := range agents {
		switch {
		case g.busy():
			r.Working++
		case g.State == agentIdle:
			r.Idle++
		}
	}
}

// warn says what down is about to stop: the agents in the middle of
// something by name, the idle ones as a count.
func (r *downReport) warn(agents []yardAgent) {
	fmt.Printf("yardr down stops %s of the yard in %s:\n", plural(r.Working+r.Idle, "agent"), r.Home)
	for _, g := range agents {
		if g.busy() {
			fmt.Printf("  %-8s %s\n", g.State, g)
		}
	}
	if r.Idle > 0 {
		fmt.Printf("  %d idle\n", r.Idle)
	}
	fmt.Println("A bead's worktree and branch stay, and it is worked again after yardr up; a crew member is asked for its handoff first.")
}

// yardAgents are the agents the yard started that the store has as running:
// the crew members with a session or with a start underway, and the sessions of beads that run
// something on this machine (not a manual group's claim, which is a
// person's turn, nor a peer group's wait). Each is asked what it is doing,
// by the runner that started it; with herdr's server down none of herdr's
// is there to ask. Those in the middle of something come first.
func yardAgents(ctx context.Context, st store.API, runners map[string]dispatch.Runner, herdrDown bool) ([]yardAgent, error) {
	crew, err := st.Crew(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := st.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	groupRunners := make(map[string]string, len(groups))
	for _, g := range groups {
		groupRunners[g.Name] = g.Runner
	}
	agents := []yardAgent{}
	for _, r := range crew {
		appendCrewSession := func(user string, session store.CrewSession) {
			switch {
			case session.Live():
				agents = append(agents, yardAgent{Kind: "crew", Name: r.Name, For: user, runner: r.Runner, handle: session.Handle})
			case session.Starting["tab"] != nil:
				// A start that was underway when its daemon was stopped: the
				// pane is open and its agent may run, and no session was
				// recorded (found trying this in an isolated yard).
				agents = append(agents, yardAgent{Kind: "crew", Name: r.Name, For: user, runner: r.Runner, handle: session.Starting, starting: true})
			}
		}
		appendCrewSession("", r.CrewSession)
		for user, session := range r.Conversations {
			appendCrewSession(user, session)
		}
	}
	for _, s := range sessions {
		if !sessionRunsHere(s, groupRunners) {
			continue
		}
		g := yardAgent{Kind: "bead", Name: s.Bead, Stage: s.Stage, Group: s.Group, session: s.ID, runner: s.RunnerName(), handle: s.Handle}
		if g.runner == "" {
			g.runner = groupRunners[s.Group]
		}
		agents = append(agents, g)
	}
	for i := range agents {
		g := &agents[i]
		g.Agent, _ = g.handle["agent"].(string)
		g.State = agentState(ctx, runners[g.runner], g.handle, g.runner == "herdr" && herdrDown)
	}
	// What a failed start left on the crew member stays there once its pane
	// is closed: no agent, and nothing the store is to be told.
	agents = slices.DeleteFunc(agents, func(g yardAgent) bool { return g.starting && g.State == agentGone })
	slices.SortStableFunc(agents, func(x, y yardAgent) int {
		switch {
		case x.busy() == y.busy():
			return 0
		case x.busy():
			return -1
		}
		return 1
	})
	return agents, nil
}

// agentState is what the agent of handle does, as its runner says. A
// script's session has no turns: one that runs is working.
func agentState(ctx context.Context, r dispatch.Runner, handle map[string]any, down bool) string {
	if down {
		return agentGone
	}
	if r == nil {
		return agentUnknown
	}
	if s, ok := r.(dispatch.Statuser); ok {
		switch status, err := s.Status(ctx, handle); {
		case err != nil:
			return agentUnknown
		case status == "":
			return agentGone
		case status == agentWorking, status == agentBlocked:
			return status
		case status == agentIdle, status == "done":
			return agentIdle
		}
		return agentUnknown
	}
	switch alive, err := r.Alive(ctx, handle); {
	case err != nil:
		return agentUnknown
	case !alive:
		return agentGone
	}
	return agentWorking
}

// handoffs asks each crew member among agents for its handoff and waits up
// to wait, for all of them together, until each has written one or is
// gone. One that could not be asked (it is at a dialog, its runner types
// nothing) is not waited for. A wait of 0 asks for none.
func (a *app) handoffs(ctx context.Context, st store.API, runners map[string]dispatch.Runner, agents []yardAgent, wait time.Duration) {
	if wait <= 0 {
		return
	}
	asked := map[string]time.Time{}
	for _, g := range agents {
		n, ok := runners[g.runner].(dispatch.Nudger)
		if g.Kind != "crew" || !ok || g.State == agentGone || g.State == agentUnknown {
			continue
		}
		at := time.Now()
		err := n.Nudge(ctx, g.handle, fmt.Sprintf(
			"The yard is being stopped (yardr down). Write your handoff now: yardr crew handoff %s \"<what you are doing, what is next, what to watch>\". "+
				"Your session is stopped once it is written, or in %s without one; your next session starts from it.", g.Name, wait))
		// Typed and not seen to be taken is typed: the handoff says whether
		// it was.
		if err == nil || errors.Is(err, dispatch.ErrPromptStalled) {
			asked[g.key()] = at
		}
	}
	if len(asked) == 0 {
		return
	}
	if !a.json {
		names := slices.Sorted(func(yield func(string) bool) {
			for n := range asked {
				if !yield(n) {
					return
				}
			}
		})
		fmt.Fprintf(stderr, "waiting up to %s for the handoff of %s\n", wait, strings.Join(names, ", "))
	}
	for deadline := time.Now().Add(wait); len(asked) > 0 && time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(pollEvery) {
		for i := range agents {
			g := &agents[i]
			at, waiting := asked[g.key()]
			if g.Kind != "crew" || !waiting {
				continue
			}
			r, err := st.GetCrew(ctx, g.Name)
			var session store.CrewSession
			if err == nil {
				session = r.Session(g.For)
			}
			switch {
			case err != nil || !session.Live():
				delete(asked, g.key())
			case session.LastHandoffAt != nil && !session.LastHandoffAt.Before(at):
				g.Handoff = true
				delete(asked, g.key())
			}
		}
	}
}

// stopAgent stops g as the daemon stops a session it ends: by its runner,
// which for herdr closes the agent's tab and leaves the worktree. Then the
// store is told. A crew member is stopped as yardr crew stop's is, and
// stays enabled, so the next daemon starts it. A bead's session ends as
// aborted: the bead is free for its stage's queue again and no failed
// attempt is counted (store.EndSession), which a session the next daemon
// found gone would be. With herdr's server down there is no pane to close,
// and the store is told all the same.
func stopAgent(ctx context.Context, st store.API, runners map[string]dispatch.Runner, g *yardAgent, herdrDown bool, yardr string) {
	fail := func(err error) {
		g.Error, g.Do = err.Error(), yardr+" down, once more"
	}
	if r := runners[g.runner]; r != nil && len(g.handle) > 0 && !(g.runner == "herdr" && herdrDown) {
		if err := r.Stop(ctx, g.handle); err != nil {
			fail(fmt.Errorf("%s could not be stopped: %w", g, err))
			return
		}
	}
	const reason = "stopped by yardr down"
	if g.starting {
		// Its pane is closed, so the start that was underway is over: left
		// on the crew member it would stand for a pane of the yard's.
		if err := st.CrewStartingFor(ctx, g.Name, g.For, nil); err != nil {
			fail(err)
			return
		}
		g.Stopped = true
		return
	}
	if g.Kind == "crew" {
		if err := st.CrewStoppedFor(ctx, g.Name, g.For, reason); err != nil {
			fail(err)
			return
		}
		g.Stopped = true
		return
	}
	if err := st.EndSession(ctx, g.session, "aborted", reason); err != nil {
		fail(err)
		return
	}
	// Its pane is closed: the next daemon has nothing of it to tear down.
	if err := st.MarkStopped(ctx, g.session); err != nil {
		fail(err)
		return
	}
	g.Stopped = true
}

func (r *downReport) print() {
	r.lines()
	if !r.Down {
		fmt.Printf("\nThe yard in %s is not down: see NOT above.\n", r.Home)
		return
	}
	fmt.Printf("\nThe yard in %s is down. Start it again: %s up\n", r.Home, yardrAt(r.Home))
}

// lines prints what was stopped, a line a part, and the login services.
func (r *downReport) lines() {
	var beads, crew, handoffs int
	for _, g := range r.Agents {
		switch {
		case g.Error != "":
			fmt.Printf("%-15s NOT stopped: %s\n%-15s   %s\n", "agents", g.Error, "", g.Do)
		case !g.Stopped:
			// Left as it was: the daemon could not be stopped before it.
		case g.State == agentGone:
			// No agent was left of it; the store was told.
		case g.Kind == "crew":
			crew++
			if g.Handoff {
				handoffs++
			}
		default:
			beads++
		}
	}
	for _, p := range r.Parts {
		switch {
		case p.Part == svcDaemon:
			// The agents' line is after the daemon's, as they were stopped.
			if p.Error != "" || p.Stopped {
				p.line("stopped")
			} else {
				p.line("was not running")
			}
			if beads+crew > 0 {
				line := fmt.Sprintf("stopped %d", beads+crew)
				if beads > 0 {
					line += fmt.Sprintf(": %s, back in the queue with worktree, branch and attempts as they were", plural(beads, "bead session"))
				}
				if crew > 0 {
					wrote := "none wrote a handoff"
					if handoffs > 0 {
						wrote = fmt.Sprintf("%d wrote a handoff", handoffs)
					}
					line += fmt.Sprintf("%s %s (%s)", map[bool]string{true: ";", false: ":"}[beads > 0], plural(crew, "crew session"), wrote)
				}
				fmt.Printf("%-15s %s\n", "agents", line)
			} else if p.Error == "" && !slices.ContainsFunc(r.Agents, func(g yardAgent) bool { return g.Error != "" }) {
				fmt.Printf("%-15s none was running\n", "agents")
			}
		case p.Part == partHerdr:
			fmt.Printf("%-15s %s\n", p.name(), p.Detail)
		case p.Error != "" || p.Stopped:
			p.line("stopped")
		case p.Part == partServer && p.Running:
			p.line("left running: the agents are not stopped while the daemon would start them again")
		case p.Part == partServer:
			p.line("was not running")
		}
		// A part that did not run and is none of the yard's three has no line.
	}
	printLogin(r.Login)
}

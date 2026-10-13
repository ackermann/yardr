package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// doctorReport is what yardr doctor found, in its three parts: what the
// machine has, how the yard runs, and how far its setup is.
type doctorReport struct {
	Home string `json:"home"`
	// Yard is false on a home with no yard yet: then there is no State and
	// no Optional, and no step is done.
	Yard     bool           `json:"yard"`
	Machine  []need         `json:"machine"`
	State    *yardState     `json:"state,omitempty"`
	Steps    []setupStep    `json:"steps"`
	Optional []optionalPart `json:"optional,omitempty"`
	// Next is the first setup step not done; absent when all are.
	Next *setupStep `json:"next,omitempty"`
	// Faults are what the yard needs and lacks, one line each. Any of them
	// makes the exit status 1.
	Faults []string `json:"faults"`
	// Tried is whether each harness was given one real call (--try): the
	// machine's `try` says what each came to.
	Tried bool `json:"tried"`
	// Skipped are the checks that were not made, by name, on a yard whose
	// server is not running: what they ask is in the store, and doctor
	// starts no server to read it.
	Skipped []string `json:"skipped,omitempty"`
}

// The states of a yard's server, as doctor reports them.
const (
	serverUp          = "running"
	serverStopped     = "not_running"
	serverUnreachable = "unreachable"
)

// skippedWhy is what stands for a check on a yard whose server is not
// running.
const skippedWhy = "the server is not running"

// storeChecks are the checks that read the store, as Skipped names them:
// the lines of the yard's part, the setup steps, and the optional parts.
var storeChecks = []string{"yardmaster", "crew", "held", "cycles", "mail", "names", "config", "codex", "steps", "hooks", "sources", "peers", "packs", "limits"}

// yardState is the second part: what of the yard runs.
type yardState struct {
	// Server is "running", "not_running" (nothing else of the yard is read
	// then but the daemon, which the home says) or "unreachable".
	Server      string           `json:"server"`
	ServerError string           `json:"server_error,omitempty"`
	Daemon      bool             `json:"daemon"`
	Yardmaster  *yardmasterState `json:"yardmaster,omitempty"`
	// Crew is how many the yard has; Failing those that do not start.
	Crew    int           `json:"crew"`
	Failing []failingCrew `json:"failing"`
	// Login are the crew at their harness's login screen: their session
	// runs, and the harness is not logged in.
	Login []loginCrew `json:"login,omitempty"`
	Held  []string    `json:"held"`
	// Cycles are the beads that block each other in a circle, each as
	// handles in blocking order (see store.BlockerCycles): never ready.
	Cycles [][]string `json:"cycles,omitempty"`
	// Mail is what waits in the inbox of the user the command acts as;
	// absent when who acts is no user.
	Mail *mailState `json:"mail,omitempty"`
	// Parties are what is to know about the yard's names: a crew member named
	// as the operator, members of groups that are no party of the yard.
	Parties []string `json:"parties,omitempty"`
	// EnvCommands are the crew whose env_command names a program
	// this command does not find.
	EnvCommands []string `json:"env_commands,omitempty"`
	// CopiedOn are the crew whose --on is a copy of a default list of
	// before the class, each with the command that names the class instead
	// (copiedDefault).
	CopiedOn []string `json:"copied_on,omitempty"`
	// Config is what the store would refuse of what the yard has stored:
	// config keys and settings nothing reads, values their readers take as
	// not set (configNotes). Stored before the store checked them.
	Config []string `json:"config,omitempty"`
	// CodexTrust are the depots a group of kind codex works whose root
	// Codex's own config does not trust, each with the lines that trust it
	// (codexTrustNotes).
	CodexTrust []string `json:"codex_trust,omitempty"`
	// What readState read to say the above, kept for yardr status, which
	// lists it: every crew member, and the open beads.
	crew []store.CrewMember
	open []store.Bead
}

// yardmasterState is the yardmaster: whether the crew member exists, and its
// session.
type yardmasterState struct {
	Name       string `json:"name"`
	CrewMember bool   `json:"crew"`
	Enabled    bool   `json:"enabled"`
	Running    bool   `json:"running"`
	State      string `json:"state,omitempty"` // the crew member's: stopped running failed
	Agent      string `json:"agent,omitempty"` // herdr's name for its session
	Failures   int    `json:"failures,omitempty"`
	Reason     string `json:"reason,omitempty"` // of its last failed start
}

// failingCrew is a crew member whose starts fail.
type failingCrew struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Failures int    `json:"failures"`
	Reason   string `json:"reason,omitempty"`
}

// loginCrew is a crew member at its harness's login screen: the kind it runs
// on and the line of the screen the daemon saw.
type loginCrew struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Line string `json:"line"`
}

// optionalPart is something a yard runs without: configured or not, never
// a fault.
type optionalPart struct {
	Key        string `json:"key"`
	Title      string `json:"title"`
	Configured bool   `json:"configured"`
	Detail     string `json:"detail"`
	// Skipped is whether it was not looked at, on a yard whose server is
	// not running: Detail says so, and Configured says nothing.
	Skipped bool `json:"skipped,omitempty"`
	// Notes are what to know about it as it is configured.
	Notes []string `json:"notes,omitempty"`
	// Views are the web views that run now, on the web view's part only.
	Views []webView `json:"views,omitempty"`
}

// serverRunning reports whether the home's server answers; tests replace it.
var serverRunning = server.Running

// loginService reports whether the daemon is installed as a login service
// for the home (yardr daemon install); tests replace it.
var loginService = func(cfg *config.Config) bool { return serviceInstalled(cfg, svcDaemon) }

// webService reports whether the web view is installed as a login service
// for the home (yardr web install): nothing else keeps it running. Tests
// replace it.
var webService = func(cfg *config.Config) bool { return serviceInstalled(cfg, svcWeb) }

// userHomeDir is the person's home directory; tests replace it.
var userHomeDir = os.UserHomeDir

func (a *app) doctorCmd() *cobra.Command {
	var try bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "What this yard has and what it lacks: programs, what runs, setup steps (read-only)",
		Long: `Print what the yard has and what it lacks, in three parts:

  Needed on this machine  git, herdr and each harness the yard runs on (its
                          default_kind and every kind a group or crew member
                          names): on PATH or not, with where to get what is
                          missing, and where your login shell finds it when
                          this shell's PATH does not. herdr is asked whether
                          its server runs. A harness that is found is run
                          with its version flag: on PATH is not "works", and
                          a version is not an answer (--try).
  The yard                the server, the daemon, the yardmaster and its session,
                          crew members whose starts fail, held beads.
  Setup                   the steps from a yard to a first merged bead, each
                          done or todo, and the command for the first one
                          not done. Then what a yard runs without, as
                          configured or not configured: the web view, hooks,
                          sources, peers, packs, limits, the daemon as a
                          login service.

It is read-only: it installs nothing (it prints where to get a program and
never runs that), changes nothing in the store and starts no daemon and no
server. It runs each harness's version flag, for up to ` + versionTimeout.String() + ` each; a kind
yardr knows no version flag for is not run. On a yard whose server is not
running it says so, checks the machine and the daemon, and lists what only
the store knows (the yardmaster, the crew, held beads, the setup steps,
hooks, sources, peers, packs, limits) as skipped: that is a note, not a fault. On a home with
no yard it says so, checks the machine and creates nothing. For a program
that is not on PATH it runs your login shell ($SHELL -l -c) once, to ask
where that finds it.

--try makes one real call to each harness: its one-shot form (claude -p,
codex exec, ...) with a prompt that asks for one word, stdin closed, for up
to ` + tryTimeout.String() + ` each. It says what the harness printed when that fails: a login
that is needed, a flag it does not know. It costs a model call per harness,
on your account with it, so doctor never makes it unasked. A kind yardr
knows no one-shot form for is reported as not tried. With a source that says
limits (yardr limits --help) it also asks that source, once for each kind
the yard runs on that the source answers for, for up to ` + limits.Timeout.String() + ` each: without
--try doctor says which source answers for which kinds and asks nothing.
With declared sources (yardr source --help) it also makes the check call of
each that names one, as yardr source check does, for up to ` + source.Timeout.String() + ` each:
without --try doctor reads the file its credential is in and how its last
call ended, and sends nothing. On
a yard whose server is not running --try is refused: which harnesses the
yard runs on is in its store.

The exit status is 1 when something the yard needs is missing or not working
(the lines marked FAULT), 0 otherwise. A step not done, a held bead, a stopped
yardmaster and anything not configured are no fault. docs/doctor.md lists
every line.`,
		Example: `  yardr doctor
  yardr doctor --try                           # and one real call to each harness (costs a model call)
  yardr doctor --json | jq -r '.next.next'     # the command for the next setup step`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			rep, err := a.doctor(c.Context(), cfg, try)
			if err != nil {
				return err
			}
			if err := a.out(rep, rep.print); err != nil {
				return err
			}
			if len(rep.Faults) > 0 {
				return errCheckFailed // the report said what
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&try, "try", false, "make one real call to each harness and say what it answered (costs a model call each); also ask the limits source and make each declared source's check call")
	return cmd
}

// doctor reads the three parts. It fails only on what stops it from reading
// at all; everything it finds wrong is in the report. With try it makes one
// real call to each harness (see try).
//
// It starts no server (yardr-sb74): a command asked what is wrong changes
// nothing of what runs to answer. As yardr status does, it asks the home
// whether it holds a store, then whether a server answers, and only one that
// answers is asked about the yard. A store in hand already (the onboarding's)
// is a server that answered.
func (a *app) doctor(ctx context.Context, cfg *config.Config, try bool) (*doctorReport, error) {
	if a.st == nil {
		if _, err := os.Stat(cfg.DBPath()); errors.Is(err, fs.ErrNotExist) {
			return a.examine(ctx, cfg, try, false, nil)
		}
		if !serverRunning(ctx, cfg) {
			if try {
				yardr := yardrAt(cfg.Home)
				return nil, fmt.Errorf("doctor --try needs the yard's server, which is not running: which harnesses the yard runs on "+
					"is in its store, and doctor starts no server. Start it (%s server start) and run it again; "+
					"%s doctor without --try says what it can without one", yardr, yardr)
			}
			return stoppedYard(ctx, cfg), nil
		}
		// A client of the server that answered, not a.store: that one
		// starts a server when this one has stopped in between.
		a.st, a.cfg = rpc.NewClient(cfg.Socket()), cfg
	}
	set, err := a.initialized(ctx, cfg)
	return a.examine(ctx, cfg, try, set, err)
}

// forStoppedYard is what a harness on this machine is listed for while the
// yard's server is not running: none is known to be needed, so none is a
// fault.
const forStoppedYard = "a harness on this machine; which ones the yard runs on is not known while its server is not running"

// stoppedYard is the report on a yard whose server is not running: what the
// machine and the home say without the store, and every check that needs
// the store as skipped. Whether the store was ever set up is not known
// then; as for yardr status, it is reported as the yard it most likely is.
func stoppedYard(ctx context.Context, cfg *config.Config) *doctorReport {
	rep := &doctorReport{Home: cfg.Home, Yard: true, Faults: []string{}, Steps: []setupStep{}, Skipped: storeChecks}
	rep.Machine = machineNeeds(ctx, nil, false)
	for i, n := range rep.Machine {
		if n.Harness != "" && n.For == forNewYard {
			rep.Machine[i].For = forStoppedYard
		}
	}
	rep.State = &yardState{Server: serverStopped, Daemon: daemonRunning(cfg.DaemonLock()), Failing: []failingCrew{}, Held: []string{}}
	skip := func(key, title string) optionalPart {
		return optionalPart{Key: key, Title: title, Skipped: true, Detail: skippedWhy}
	}
	// The login service's notes on its depots are the store's too, and left
	// out.
	rep.Optional = []optionalPart{webPart(cfg), skip("hooks", "hooks"), skip("sources", "sources"), skip("peers", "peers"), skip("packs", "packs"), skip("limits", "limits"), servicePart(cfg), herdrLoginPart(cfg)}
	rep.faults()
	return rep
}

// examine is doctor for a home already asked whether it holds a yard (set,
// or err when its server cannot be reached).
func (a *app) examine(ctx context.Context, cfg *config.Config, try, set bool, err error) (*doctorReport, error) {
	rep := &doctorReport{Home: cfg.Home, Faults: []string{}, Tried: try}
	if err != nil {
		// A yard whose server cannot be reached: the machine's part still
		// says something, and the fault is the server's.
		rep.Yard = true
		rep.Machine = machineNeeds(ctx, nil, try)
		rep.State = &yardState{Server: serverUnreachable, ServerError: err.Error(), Daemon: daemonRunning(cfg.DaemonLock()),
			Failing: []failingCrew{}, Held: []string{}}
		rep.Steps = []setupStep{}
		rep.faults()
		return rep, nil
	}
	if !set {
		rep.Machine = machineNeeds(ctx, nil, try)
		rep.Steps = noSetup()
		rep.Next = nextStep(rep.Steps)
		rep.faults()
		return rep, nil
	}
	rep.Yard = true
	st, err := a.store(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	crew, err := st.Crew(ctx)
	if err != nil {
		return nil, err
	}
	def := defaultKind(ctx, st)
	uses := harnessUses(def, groups, crew)
	rep.Machine = machineNeeds(ctx, uses, try)
	if rep.State, err = readState(ctx, cfg, st, crew); err != nil {
		return nil, err
	}
	if rep.State.Mail, err = a.readMail(ctx, st); err != nil {
		return nil, err
	}
	if rep.State.Parties, err = partyNotes(ctx, st, crew, groups, yardrAt(cfg.Home)); err != nil {
		return nil, err
	}
	if rep.State.Config, err = configNotes(ctx, st, crew, groups, yardrAt(cfg.Home)); err != nil {
		return nil, err
	}
	if rep.State.CodexTrust, err = codexTrustNotes(ctx, st, groups); err != nil {
		return nil, err
	}
	if rep.Steps, err = setupSteps(ctx, st); err != nil {
		return nil, err
	}
	rep.Next = nextStep(rep.Steps)
	if rep.Optional, err = optionalParts(ctx, cfg, st, limitUses(def, groups, crew), try); err != nil {
		return nil, err
	}
	rep.faults()
	return rep, nil
}

// readState is the second part, read from a yard whose server answers: the
// daemon, the yardmaster and its session, the crew whose starts fail, the
// held beads, and the beads that block each other in a circle. It is what
// doctor prints, and what bare yardr goes by to tell
// a yard whose onboarding is to finish (resume).
func readState(ctx context.Context, cfg *config.Config, st store.API, crew []store.CrewMember) (*yardState, error) {
	state := &yardState{Server: serverUp, Daemon: daemonRunning(cfg.DaemonLock()), Crew: len(crew),
		Failing: []failingCrew{}, Held: []string{}, crew: crew}
	yardmaster := yardmasterOf(ctx, st)
	def := defaultKind(ctx, st)
	state.Yardmaster = &yardmasterState{Name: yardmaster}
	for _, r := range crew {
		// At its login screen with a session that runs: the yardmaster's
		// own line says it runs, and this one says its harness is not
		// logged in.
		if line, _ := r.Handle[dispatch.HarnessLoginKey].(string); r.Live() && line != "" {
			state.Login = append(state.Login, loginCrew{Name: r.Name, Kind: loginKindOf(def, r), Line: line})
		}
		reason := ""
		failing := r.State == "failed" || (r.Enabled && !r.Live() && r.Failures > 0)
		if failing {
			reason = lastStartFailure(ctx, st, r)
		}
		if r.Name == yardmaster {
			state.Yardmaster = &yardmasterState{Name: yardmaster, CrewMember: true, Enabled: r.Enabled, Running: r.Live(), State: r.State,
				Agent: show(r.Handle["agent"]), Failures: r.Failures, Reason: reason}
			if !r.Live() {
				state.Yardmaster.Agent = ""
			}
		} else if failing {
			state.Failing = append(state.Failing, failingCrew{r.Name, r.State, r.Failures, reason})
		}
		// Not run here: it may ask a vault to unlock. Only whether its
		// program is there, as this command finds it; the daemon's PATH is
		// its own.
		if err := config.EnvCommandMissing(cmp.Or(r.Dir, cfg.Home), r.Config); err != nil {
			state.EnvCommands = append(state.EnvCommands, fmt.Sprintf("crew member %s does not start without its environment: %v", r.Name, err))
		}
		if n := copiedDefault(r, yardrAt(cfg.Home)); n != "" {
			state.CopiedOn = append(state.CopiedOn, n)
		}
	}
	open, err := st.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		return nil, err
	}
	state.open = open
	for _, b := range open {
		if b.Hold {
			state.Held = append(state.Held, b.ID)
		}
	}
	if state.Cycles, err = st.BlockerCycles(ctx); err != nil {
		return nil, err
	}
	return state, nil
}

// loginKindOf is the harness kind r runs on: the kind the runner started it
// as (the handle's "kind", as the herdr runner writes it), the kind its
// config names, the yard's default, or the runner's own.
func loginKindOf(def string, r store.CrewMember) string {
	if kind, _ := r.Handle["kind"].(string); kind != "" {
		return kind
	}
	if kind, _ := r.Config["kind"].(string); kind != "" {
		return kind
	}
	if def != "" {
		return def
	}
	return agents.DefaultKind
}

// optionalParts are what a yard runs without, each configured or not. uses
// are the harness kinds the yard runs on with their provider settings; with
// try, the source that says their limits is asked (see limitsPart) and each
// declared source's check call is made (see sourcesPart).
func optionalParts(ctx context.Context, cfg *config.Config, st store.API, uses []limitUse, try bool) ([]optionalPart, error) {
	yardr := yardrAt(cfg.Home)
	web := webPart(cfg)
	hs, err := st.Hooks(ctx)
	if err != nil {
		return nil, err
	}
	hook := optionalPart{Key: "hooks", Title: "hooks",
		Detail: fmt.Sprintf("no source outside the yard reaches it; %s hook add --help (docs/subscriptions.md)", yardr)}
	if len(hs) > 0 {
		var names []string
		for _, h := range hs {
			names = append(names, fmt.Sprintf("%s (%s)", h.Name, h.Source))
			if h.Blocked != "" {
				hook.Notes = append(hook.Notes, fmt.Sprintf("hook %s is blocked: %s", h.Name, h.Blocked))
			}
			if h.LastRefused() {
				hook.Notes = append(hook.Notes, lastRefused(h))
			}
		}
		hook.Configured, hook.Detail = true, strings.Join(names, ", ")
	}
	// The receiver's address is the server's, so what the server said: with
	// hooks, always; without, when it has an address all the same, since one
	// it cannot listen on (another yard has it) is to be changed either way.
	if l, known := hooks.ReadListener(cfg); len(hs) > 0 || known && l.Addr != "" {
		_, text := receiver(ctx, cfg, st)
		hook.Notes = append(hook.Notes, text)
	}
	packs, err := st.Packs(ctx)
	if err != nil {
		return nil, err
	}
	pack := optionalPart{Key: "packs", Title: "packs",
		Detail: fmt.Sprintf("no pack is imported; %s pack apply <dir> --depot <depot> (docs/packs.md)", yardr)}
	if len(packs) > 0 {
		var names []string
		for _, p := range packs {
			names = append(names, fmt.Sprintf("%s into %s", p.Pack, p.Depot))
		}
		pack.Configured, pack.Detail = true, strings.Join(names, ", ")
	}
	svc := servicePart(cfg)
	if svc.Configured {
		depots, err := st.Depots(ctx)
		if err != nil {
			return nil, err
		}
		if home, err := userHomeDir(); goos == "darwin" && err == nil {
			for _, r := range protectedDepots(depots, home) {
				svc.Notes = append(svc.Notes, "depot "+r+" is in a folder macOS protects (~/Desktop, ~/Documents, ~/Downloads): "+
					"a launchd agent needs its own access there (System Settings, Privacy & Security, Files and Folders), "+
					"which a daemon started in a terminal has from the terminal")
			}
		}
	}
	srcs, err := sourcesPart(ctx, cfg, st, try)
	if err != nil {
		return nil, err
	}
	peers, err := peerPart(ctx, cfg, st)
	if err != nil {
		return nil, err
	}
	lim, err := limitsPart(ctx, cfg, st, uses, try)
	if err != nil {
		return nil, err
	}
	return []optionalPart{web, hook, srcs, peers, pack, lim, svc, herdrLoginPart(cfg)}, nil
}

// servicePart is the daemon's login service as the home says it is,
// without the notes on the yard's depots.
func servicePart(cfg *config.Config) optionalPart {
	yardr := yardrAt(cfg.Home)
	if loginService(cfg) {
		return optionalPart{Key: "login_service", Title: "login service", Configured: true,
			Detail: "the daemon starts at login" + fileOf(cfg, svcDaemon)}
	}
	return optionalPart{Key: "login_service", Title: "login service",
		Detail: fmt.Sprintf("the daemon runs while its terminal or the session that started it does (%s daemon, %s init --start); "+
			"it does not come back after a reboot; %s daemon install starts it at login", yardr, yardr, yardr)}
}

// line is the part as printed under Optional.
func (p optionalPart) line() string {
	state := "not configured"
	if p.Configured {
		state = "configured"
	}
	if p.Skipped {
		state = "skipped"
	}
	return fmt.Sprintf("  %-14s  %s: %s", state, p.Title, p.Detail)
}

// at is " at http://<addr>/" for a web view's address, "" for none.
func at(addr string) string {
	if addr == "" {
		return ""
	}
	return " at http://" + addr + "/"
}

// fileOf is " (<file>)" for the home's service of kind, once written.
func fileOf(cfg *config.Config, kind string) string {
	if !serviceInstalled(cfg, kind) {
		return ""
	}
	_, _, path, _ := unitFile(cfg, kind)
	return " (" + path + ")"
}

// The tags a line of the report starts with.
const (
	tagOK    = "ok"
	tagFault = "FAULT"
	tagNote  = "note"
)

// docLine is one line of the first two parts: its tag, what it is about,
// and what was found.
type docLine struct{ tag, name, text string }

// line is a need as the report prints it, without what a real call to it
// came to (see lines).
func (n need) line() docLine {
	if !n.Found && n.LoginPath != "" {
		// Installed, and not where this shell looks: nothing is to get.
		return docLine{tagFault, n.Program, fmt.Sprintf("not on PATH: %s. Found at %s by your login shell, which this shell's PATH does not have: "+
			"add %s to PATH here, or for the daemon's service run `yardr daemon install` from a login shell",
			n.For, n.LoginPath, filepath.Dir(n.LoginPath))}
	}
	if !n.Found {
		return docLine{tagFault, n.Program, fmt.Sprintf("not on PATH: %s. Get it: %s", n.For, n.Get)}
	}
	if s := n.Server; s != nil {
		// Which session was asked: the yard's when its setting names one,
		// else this shell's (yardr status says what that means).
		in, start := "", "run herdr in a terminal"
		if s.Session != nil {
			in = "; " + s.Session.String()
			if s.Session.From == runner.SessionSet {
				start = fmt.Sprintf("run herdr session attach %s in a terminal", s.Session.Name)
			}
		}
		switch s.State {
		case herdrUp:
			return docLine{tagOK, n.Program, n.Path + ", its server is running" + in}
		case herdrDown:
			return docLine{tagFault, n.Program, fmt.Sprintf("%s: on PATH, but its server is not running (%s): the daemon cannot start agents until it is. "+
				"Start it: %s (ctrl+b q detaches, the server stays); on a machine without a desktop, "+
				"run `herdr server`, its headless server, as a service (docs/install.md, \"herdr's server\")%s", n.Path, or(s.Output, "no output"), start, in)}
		default:
			return docLine{tagNote, n.Program, fmt.Sprintf("%s: on PATH; whether its server is running could not be asked: %s%s", n.Path, or(s.Output, "no output"), in)}
		}
	}
	if n.Harness == "" {
		return docLine{tagOK, n.Program, n.Path}
	}
	bad := tagFault
	if n.Optional {
		bad = tagNote
	}
	head := fmt.Sprintf("%s, %s: ", n.Path, n.For)
	// What yardr itself knows of the kind, beside what the machine has.
	file := "; no harness file (yardr harness list)"
	if n.HarnessFile != 0 {
		file = fmt.Sprintf("; harness file v%d", n.HarnessFile)
	}
	if len(n.HarnessAlways) > 0 {
		file += "; always flags: " + strings.Join(n.HarnessAlways, " ")
	}
	flag := "its version flag"
	switch v := n.Version; {
	case v == nil || v.State == versionNotChecked:
		return docLine{tagOK, n.Program, head + "not checked, yardr knows no version flag for " + n.Harness + file}
	case v.State == versionAnswers:
		return docLine{tagOK, n.Program, head + "answers " + flag + " (" + or(v.Output, "no output") + ")" + file}
	case v.State == versionHangs:
		text := head + fmt.Sprintf("on PATH, but it did not answer %s within %s", flag, versionTimeout)
		if goos == "darwin" {
			text += ". On macOS that can be a quarantined binary waiting on a Gatekeeper dialog nobody sees: " +
				"run " + n.Program + " once in a terminal and answer it"
		}
		return docLine{bad, n.Program, text + file}
	default:
		return docLine{bad, n.Program, head + "on PATH, but " + flag + " fails: " + or(v.Output, "no output") + file}
	}
}

// lines are a need as the report prints it: its line and, for a harness
// under --try, what one real call to it came to.
func (n need) lines() []docLine {
	out := []docLine{n.line()}
	t := n.Try
	if t == nil {
		return out
	}
	bad := tagFault
	if n.Optional {
		bad = tagNote
	}
	switch t.State {
	case tryOK:
		out = append(out, docLine{tagOK, n.Program, "tried: one real call answered (" + or(t.Output, "no output") + ")"})
	case tryHangs:
		text := fmt.Sprintf("tried: one real call did not end within %s", tryTimeout)
		if t.Output != "" {
			text += "; it printed: " + t.Output
		}
		out = append(out, docLine{bad, n.Program, text})
	case tryFails:
		out = append(out, docLine{bad, n.Program, "tried: one real call failed: " + or(t.Output, "no output")})
	default:
		out = append(out, docLine{tagNote, n.Program, "not tried: " + t.Output})
	}
	return out
}

// machineLines are the first part as the report prints it. Without --try
// its last line says what the lines above do not: no harness was asked for
// an answer.
func (r *doctorReport) machineLines() []docLine {
	lines := []docLine{}
	harness := false
	for _, n := range r.Machine {
		lines = append(lines, n.lines()...)
		harness = harness || n.Harness != "" && n.Found
	}
	// Not on a yard whose server is not running: --try is refused there.
	if harness && !r.Tried && len(r.Skipped) == 0 {
		lines = append(lines, docLine{tagNote, "harness", fmt.Sprintf("on PATH is not \"works\": no harness was asked for an answer. "+
			"%s doctor --try makes one real call to each (it costs a model call per harness)", yardrAt(r.Home))})
	}
	return lines
}

// lines are the second part as the report prints it.
func (s *yardState) lines(home string) []docLine {
	yardr := yardrAt(home)
	var out []docLine
	switch s.Server {
	case serverUnreachable:
		return append(out, docLine{tagFault, "server", "cannot be reached: " + s.ServerError})
	case serverStopped:
		// A note: a stopped yard is a state a yard can be in, and nothing
		// is wrong with it.
		out = append(out, docLine{tagNote, "server", fmt.Sprintf("not running (%s server start, or any command but status and doctor, starts it)", yardr)})
	default:
		out = append(out, docLine{tagOK, "server", "running"})
	}
	if s.Daemon {
		out = append(out, docLine{tagOK, "daemon", "running"})
	} else {
		out = append(out, docLine{tagFault, "daemon", fmt.Sprintf("not running: no bead is dispatched and no crew member starts. "+
			"Start it in a pane of its own (%s daemon) or in the background (%s init --start)", yardr, yardr)})
	}
	if s.Server == serverStopped {
		// The lines every report has, so that its shape stays the same.
		for _, name := range []string{"yardmaster", "crew", "held"} {
			out = append(out, docLine{tagNote, name, "skipped: " + skippedWhy})
		}
		return out
	}
	switch m := s.Yardmaster; {
	case m == nil:
	case !m.CrewMember:
		out = append(out, docLine{tagFault, "yardmaster", fmt.Sprintf("no crew member %s: %s init adds it", m.Name, yardr)})
	case m.Running:
		out = append(out, docLine{tagOK, "yardmaster", fmt.Sprintf("%s is running (herdr agent %s)", m.Name, or(m.Agent, "unknown"))})
	case !m.Enabled:
		out = append(out, docLine{tagNote, "yardmaster", fmt.Sprintf("%s is stopped (disabled). Let it run again: %s crew start %s", m.Name, yardr, m.Name)})
	case m.Failures > 0 || m.State == "failed":
		out = append(out, docLine{tagFault, "yardmaster", fmt.Sprintf("%s is not running: %s. Once that is fixed: %s crew start %s",
			m.Name, failed(m.State, m.Failures, m.Reason), yardr, m.Name)})
	case !s.Daemon:
		out = append(out, docLine{tagFault, "yardmaster", m.Name + " is not running: the daemon starts it"})
	default:
		out = append(out, docLine{tagFault, "yardmaster", fmt.Sprintf("%s is not running, and no start has failed: the daemon may still be at it (%s crew list)", m.Name, yardr)})
	}
	for _, f := range s.Failing {
		out = append(out, docLine{tagFault, "crew", fmt.Sprintf("%s is not running: %s. Once that is fixed: %s crew start %s",
			f.Name, failed(f.State, f.Failures, f.Reason), yardr, f.Name)})
	}
	if len(s.Failing) == 0 {
		out = append(out, docLine{tagOK, "crew", fmt.Sprintf("%d, none failing to start", s.Crew)})
	}
	for _, l := range s.Login {
		out = append(out, docLine{tagFault, "crew", fmt.Sprintf("%s is not logged in: run %s in another terminal and log in there; "+
			"then answer the login in its pane or start it fresh (%s crew restart %s)",
			l.Name, agents.Executable(l.Kind), yardr, l.Name)})
	}
	if len(s.Held) == 0 {
		out = append(out, docLine{tagOK, "held", "no bead is held"})
	} else {
		out = append(out, docLine{tagNote, "held", fmt.Sprintf("%d held, waiting for someone: %s (%s bead show <id>; %s bead unhold <id> resumes it)",
			len(s.Held), strings.Join(s.Held, ", "), yardr, yardr)})
	}
	// dep add refuses these, so they are from a store written before it
	// did. No fault: the yard runs, and only a person knows which edge is
	// the wrong one.
	for _, c := range s.Cycles {
		out = append(out, docLine{tagNote, "cycle", fmt.Sprintf("%s: these beads wait on each other and are never ready. "+
			"Remove the edge that is wrong: %s dep rm <blocker> <bead>", strings.Join(c, " blocks "), yardr)})
	}
	if s.Mail != nil {
		out = append(out, s.Mail.line(yardr))
	}
	for _, n := range s.EnvCommands {
		out = append(out, docLine{tagNote, "crew", n})
	}
	for _, n := range s.CopiedOn {
		out = append(out, docLine{tagNote, "crew", n})
	}
	for _, n := range s.Parties {
		out = append(out, docLine{tagNote, "names", n})
	}
	for _, n := range s.Config {
		out = append(out, docLine{tagNote, "config", n})
	}
	for _, n := range s.CodexTrust {
		out = append(out, docLine{tagNote, "codex", n})
	}
	return out
}

// configNotes are what yardr doctor notes about the yard's stored config:
// everything the store would refuse were it written now (a group's or a
// crew member's config key nothing reads, a value its reader takes as not
// set, a setting nothing reads), each with how it is removed. Notes, not
// faults: the store took them when it checked nothing, and the yard runs
// as it did.
func configNotes(ctx context.Context, st store.API, crew []store.CrewMember, groups []store.Group, yardr string) ([]string, error) {
	var notes []string
	for _, g := range groups {
		for _, err := range store.ConfigProblems(store.GroupConfig, g.Name, g.Runner, g.Config) {
			notes = append(notes, fmt.Sprintf("%v. Change it: %s group set %s <key>=<value>, or --unset <key>", err, yardr, g.Name))
		}
	}
	for _, r := range crew {
		for _, err := range store.ConfigProblems(store.CrewConfig, r.Name, r.Runner, r.Config) {
			notes = append(notes, fmt.Sprintf("%v. Change it: %s crew set %s <key>=<value>, or --unset <key>", err, yardr, r.Name))
		}
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	for _, k := range sortedKeys(all) {
		if _, ok := store.SettingOf(k); !ok {
			notes = append(notes, fmt.Sprintf("setting %s is stored and nothing reads it (there is: %s). Remove it: %s settings set %s \"\"",
				k, strings.Join(store.SettingKeys(), ", "), yardr, k))
		}
	}
	return notes, nil
}

// failed says how a crew member's starts fail.
func failed(state string, failures int, reason string) string {
	text := fmt.Sprintf("%d failed starts so far", failures)
	if state == "failed" {
		text = fmt.Sprintf("the daemon gave up after %d tries", failures)
	}
	if reason != "" {
		text += ", the last with: " + reason
	}
	return text
}

// lines are the report's first two parts as it prints them.
func (r *doctorReport) lines() []docLine {
	lines := r.machineLines()
	if r.State != nil {
		lines = append(lines, r.State.lines(r.Home)...)
	}
	return lines
}

// silentYardmaster is the line for a yardmaster whose session runs and that
// is not up (see up): doctor itself reads no pane, so the onboarding adds it.
func silentYardmaster(yardmaster string) docLine {
	return docLine{tagNote, "yardmaster", yardmaster + "'s session runs, and it has not answered its first prompt"}
}

// doctorSays prints what yardr doctor says is wrong with the yard, for an
// onboarding that could not get to the yardmaster: the report's FAULT lines and
// the yardmaster's own line, as doctor prints them, and how to read the rest.
func (a *app) doctorSays(ctx context.Context, cfg *config.Config) error {
	rep, err := a.doctor(ctx, cfg, false)
	if err != nil {
		return err
	}
	var say []docLine
	for _, l := range rep.lines() {
		if l.tag == tagFault || l.name == "yardmaster" {
			say = append(say, l)
		}
	}
	if m := rep.State; m != nil && m.Yardmaster != nil && m.Yardmaster.Running {
		st, err := a.store(ctx)
		if err != nil {
			return err
		}
		if r, err := st.GetCrew(ctx, m.Yardmaster.Name); err == nil && r.Live() && !up(ctx, *r) {
			say = append(say, silentYardmaster(r.Name))
		}
	}
	yardr := yardrAt(cfg.Home)
	fmt.Printf("\n`%s doctor` says of it:\n\n", yardr)
	printLines(say)
	fmt.Printf("\n`%s doctor` prints the whole of it, and is how to tell when it is set right.\n", yardr)
	return nil
}

// faults collects the report's faults from its first two parts.
func (r *doctorReport) faults() {
	for _, l := range r.lines() {
		if l.tag == tagFault {
			r.Faults = append(r.Faults, l.name+": "+l.text)
		}
	}
}

func printLines(lines []docLine) {
	for _, l := range lines {
		fmt.Printf("  %-5s  %-10s  %s\n", l.tag, l.name, l.text)
	}
}

// print is the report for a person. docs/doctor.md lists every line it can
// print; a line added here is added there.
func (r *doctorReport) print() {
	yardr := yardrAt(r.Home)
	if r.Yard {
		fmt.Printf("yardr doctor: the yard in %s\n", r.Home)
	} else {
		fmt.Printf("yardr doctor: no yard in %s yet. Create one with `%s init --harness <kind>`, or run yardr in a terminal "+
			"for the onboarding. Nothing was created.\n", r.Home, yardr)
	}
	fmt.Println("\nNeeded on this machine")
	printLines(r.machineLines())
	if r.State != nil {
		fmt.Println("\nThe yard")
		printLines(r.State.lines(r.Home))
	}
	if len(r.Steps) > 0 {
		fmt.Println("\nSetup")
		printSteps(r.Steps)
	} else if len(r.Skipped) > 0 {
		fmt.Printf("\nSetup\n  %-5s  skipped: %s\n", tagNote, skippedWhy)
	}
	if len(r.Optional) > 0 {
		fmt.Println("\nOptional")
		for _, p := range r.Optional {
			fmt.Println(p.line())
			for _, n := range p.Notes {
				fmt.Printf("  %-14s  %s: %s\n", tagNote, p.Title, n)
			}
		}
	}
	fmt.Println()
	switch len(r.Faults) {
	case 0:
		if len(r.Skipped) > 0 {
			fmt.Println("Nothing that was checked is missing; what the yard's store holds was not checked (skipped above).")
			break
		}
		fmt.Println("Nothing the yard needs is missing.")
	case 1:
		fmt.Println("1 fault: something the yard needs is missing or not working (FAULT above).")
	default:
		fmt.Printf("%d faults: things the yard needs are missing or not working (FAULT above).\n", len(r.Faults))
	}
	switch {
	case !r.Yard:
	case len(r.Steps) == 0:
	case r.Next == nil:
		fmt.Println("The setup is complete.")
	default:
		printNextStep(r.Home, r.Next)
	}
}

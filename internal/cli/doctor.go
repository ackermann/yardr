package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/server"
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
}

// yardState is the second part: what of the yard runs.
type yardState struct {
	// Server is "running", "started" (it was not running, and this command
	// started it as the first command on a yard does) or "unreachable".
	Server      string      `json:"server"`
	ServerError string      `json:"server_error,omitempty"`
	Daemon      bool        `json:"daemon"`
	Mayor       *mayorState `json:"mayor,omitempty"`
	// Residents is how many the yard has; Failing those that do not start.
	Residents int               `json:"residents"`
	Failing   []failingResident `json:"failing"`
	Held      []string          `json:"held"`
	// Mail is what waits in the inbox of the user the command acts as;
	// absent when who acts is no user.
	Mail *mailState `json:"mail,omitempty"`
	// Parties are what is to know about the yard's names: a resident named
	// as the operator, members of groups that are no party of the yard.
	Parties []string `json:"parties,omitempty"`
}

// mayorState is the mayor: whether the resident exists, and its session.
type mayorState struct {
	Name     string `json:"name"`
	Resident bool   `json:"resident"`
	Enabled  bool   `json:"enabled"`
	Running  bool   `json:"running"`
	State    string `json:"state,omitempty"` // the resident's: stopped running failed
	Agent    string `json:"agent,omitempty"` // herdr's name for its session
	Failures int    `json:"failures,omitempty"`
	Reason   string `json:"reason,omitempty"` // of its last failed start
}

// failingResident is a resident whose starts fail.
type failingResident struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Failures int    `json:"failures"`
	Reason   string `json:"reason,omitempty"`
}

// optionalPart is something a yard runs without: configured or not, never
// a fault.
type optionalPart struct {
	Key        string `json:"key"`
	Title      string `json:"title"`
	Configured bool   `json:"configured"`
	Detail     string `json:"detail"`
	// Notes are what to know about it as it is configured.
	Notes []string `json:"notes,omitempty"`
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
                          default_kind and every kind a group or resident
                          names): on PATH or not, with where to get what is
                          missing, and where your login shell finds it when
                          this shell's PATH does not. herdr is asked whether
                          its server runs. A harness that is found is run
                          with its version flag: on PATH is not "works", and
                          a version is not an answer (--try).
  The yard                the server, the daemon, the mayor and its session,
                          residents whose starts fail, held beads.
  Setup                   the steps from a yard to a first merged bead, each
                          done or todo, and the command for the first one
                          not done. Then what a yard runs without, as
                          configured or not configured: the web view, hooks,
                          peers, packs, the daemon as a login service.

It is read-only: it installs nothing (it prints where to get a program and
never runs that), changes nothing in the store and starts no daemon. It runs
each harness's version flag, for up to ` + versionTimeout.String() + ` each; a kind yardr knows no
version flag for is not run. On a yard whose server is not running it starts
the server, as any command does. On a home with no yard it says so, checks
the machine and creates nothing. For a program that is not on PATH it runs
your login shell ($SHELL -l -c) once, to ask where that finds it.

--try makes one real call to each harness: its one-shot form (claude -p,
codex exec, ...) with a prompt that asks for one word, stdin closed, for up
to ` + tryTimeout.String() + ` each. It says what the harness printed when that fails: a login
that is needed, a flag it does not know. It costs a model call per harness,
on your account with it, so doctor never makes it unasked. A kind yardr
knows no one-shot form for is reported as not tried.

The exit status is 1 when something the yard needs is missing or not working
(the lines marked FAULT), 0 otherwise. A step not done, a held bead, a
stopped mayor and anything not configured are no fault. docs/doctor.md lists
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
	cmd.Flags().BoolVar(&try, "try", false, "make one real call to each harness and say what it answered (costs a model call each)")
	return cmd
}

// doctor reads the three parts. It fails only on what stops it from reading
// at all; everything it finds wrong is in the report. With try it makes one
// real call to each harness (see try).
func (a *app) doctor(ctx context.Context, cfg *config.Config, try bool) (*doctorReport, error) {
	rep := &doctorReport{Home: cfg.Home, Faults: []string{}, Tried: try}
	// Before the store is asked: connecting starts a server that is not
	// running.
	was := serverRunning(ctx, cfg)
	set, err := a.initialized(ctx, cfg)
	if err != nil {
		// A yard whose server cannot be reached: the machine's part still
		// says something, and the fault is the server's.
		rep.Yard = true
		rep.Machine = machineNeeds(ctx, nil, try)
		rep.State = &yardState{Server: "unreachable", ServerError: err.Error(), Daemon: daemonRunning(cfg.DaemonLock()),
			Failing: []failingResident{}, Held: []string{}}
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
	residents, err := st.Residents(ctx)
	if err != nil {
		return nil, err
	}
	rep.Machine = machineNeeds(ctx, harnessUses(defaultKind(ctx, st), groups, residents), try)
	if rep.State, err = readState(ctx, cfg, st, residents); err != nil {
		return nil, err
	}
	if !was {
		rep.State.Server = "started"
	}
	if rep.State.Mail, err = a.readMail(ctx, st); err != nil {
		return nil, err
	}
	if rep.State.Parties, err = partyNotes(ctx, st, residents, groups, yardrAt(cfg.Home)); err != nil {
		return nil, err
	}
	if rep.Steps, err = setupSteps(ctx, st); err != nil {
		return nil, err
	}
	rep.Next = nextStep(rep.Steps)
	if rep.Optional, err = optionalParts(ctx, cfg, st); err != nil {
		return nil, err
	}
	rep.faults()
	return rep, nil
}

// readState is the second part, read from a yard whose server answers: the
// daemon, the mayor and its session, the residents whose starts fail, the
// held beads. It is what doctor prints, and what bare yardr goes by to tell
// a yard whose onboarding is to finish (resume).
func readState(ctx context.Context, cfg *config.Config, st store.API, residents []store.Resident) (*yardState, error) {
	state := &yardState{Server: "running", Daemon: daemonRunning(cfg.DaemonLock()), Residents: len(residents),
		Failing: []failingResident{}, Held: []string{}}
	mayor := yardMayor(ctx, st)
	state.Mayor = &mayorState{Name: mayor}
	for _, r := range residents {
		reason := ""
		failing := r.State == "failed" || (r.Enabled && !r.Live() && r.Failures > 0)
		if failing {
			reason = lastStartFailure(ctx, st, r)
		}
		if r.Name == mayor {
			state.Mayor = &mayorState{Name: mayor, Resident: true, Enabled: r.Enabled, Running: r.Live(), State: r.State,
				Agent: show(r.Handle["agent"]), Failures: r.Failures, Reason: reason}
			if !r.Live() {
				state.Mayor.Agent = ""
			}
		} else if failing {
			state.Failing = append(state.Failing, failingResident{r.Name, r.State, r.Failures, reason})
		}
	}
	open, err := st.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		return nil, err
	}
	for _, b := range open {
		if b.Hold {
			state.Held = append(state.Held, b.ID)
		}
	}
	return state, nil
}

// optionalParts are what a yard runs without, each configured or not.
func optionalParts(ctx context.Context, cfg *config.Config, st store.API) ([]optionalPart, error) {
	yardr := yardrAt(cfg.Home)
	web := optionalPart{Key: "web", Title: "web view",
		Detail: fmt.Sprintf("nothing keeps it running; %s web serve shows the yard in a browser while it runs, "+
			"%s web install keeps it running from login on (docs/web.md)", yardr, yardr)}
	if webService(cfg) {
		web.Configured, web.Detail = true, "kept running as a login service"+at(webServiceAddr(cfg))+fileOf(cfg, svcWeb)
	}
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
		Detail: fmt.Sprintf("no pack is imported; %s pack apply <dir> --rig <rig> (docs/packs.md)", yardr)}
	if len(packs) > 0 {
		var names []string
		for _, p := range packs {
			names = append(names, fmt.Sprintf("%s into %s", p.Pack, p.Rig))
		}
		pack.Configured, pack.Detail = true, strings.Join(names, ", ")
	}
	svc := optionalPart{Key: "login_service", Title: "login service",
		Detail: fmt.Sprintf("the daemon runs while its terminal or the session that started it does (%s daemon, %s init --start); "+
			"it does not come back after a reboot; %s daemon install starts it at login", yardr, yardr, yardr)}
	if loginService(cfg) {
		svc.Configured, svc.Detail = true, "the daemon starts at login"+fileOf(cfg, svcDaemon)
		rigs, err := st.Rigs(ctx)
		if err != nil {
			return nil, err
		}
		if home, err := userHomeDir(); goos == "darwin" && err == nil {
			for _, r := range protectedRigs(rigs, home) {
				svc.Notes = append(svc.Notes, "rig "+r+" is in a folder macOS protects (~/Desktop, ~/Documents, ~/Downloads): "+
					"a launchd agent needs its own access there (System Settings, Privacy & Security, Files and Folders), "+
					"which a daemon started in a terminal has from the terminal")
			}
		}
	}
	peers, err := peerPart(ctx, cfg, st)
	if err != nil {
		return nil, err
	}
	return []optionalPart{web, hook, peers, pack, svc}, nil
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
		switch s.State {
		case herdrUp:
			return docLine{tagOK, n.Program, n.Path + ", its server is running"}
		case herdrDown:
			return docLine{tagFault, n.Program, fmt.Sprintf("%s: on PATH, but its server is not running (%s): the daemon cannot start agents until it is. "+
				"Start it: run herdr in a terminal (ctrl+b q detaches, the server stays); on a machine without a desktop, "+
				"run `herdr server`, its headless server, as a service (docs/install.md, \"herdr's server\")", n.Path, or(s.Output, "no output"))}
		default:
			return docLine{tagNote, n.Program, fmt.Sprintf("%s: on PATH; whether its server is running could not be asked: %s", n.Path, or(s.Output, "no output"))}
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
	flag := "its version flag"
	switch v := n.Version; {
	case v == nil || v.State == versionNotChecked:
		return docLine{tagOK, n.Program, head + "not checked, yardr knows no version flag for " + n.Harness}
	case v.State == versionAnswers:
		return docLine{tagOK, n.Program, head + "answers " + flag + " (" + or(v.Output, "no output") + ")"}
	case v.State == versionHangs:
		text := head + fmt.Sprintf("on PATH, but it did not answer %s within %s", flag, versionTimeout)
		if goos == "darwin" {
			text += ". On macOS that can be a quarantined binary waiting on a Gatekeeper dialog nobody sees: " +
				"run " + n.Program + " once in a terminal and answer it"
		}
		return docLine{bad, n.Program, text}
	default:
		return docLine{bad, n.Program, head + "on PATH, but " + flag + " fails: " + or(v.Output, "no output")}
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
	if harness && !r.Tried {
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
	case "unreachable":
		return append(out, docLine{tagFault, "server", "cannot be reached: " + s.ServerError})
	case "started":
		out = append(out, docLine{tagOK, "server", "was not running; this command started it, as the first command on a yard does"})
	default:
		out = append(out, docLine{tagOK, "server", "running"})
	}
	if s.Daemon {
		out = append(out, docLine{tagOK, "daemon", "running"})
	} else {
		out = append(out, docLine{tagFault, "daemon", fmt.Sprintf("not running: no bead is dispatched and no resident starts. "+
			"Start it in a pane of its own (%s daemon) or in the background (%s init --start)", yardr, yardr)})
	}
	switch m := s.Mayor; {
	case m == nil:
	case !m.Resident:
		out = append(out, docLine{tagFault, "mayor", fmt.Sprintf("no resident %s: %s init adds it", m.Name, yardr)})
	case m.Running:
		out = append(out, docLine{tagOK, "mayor", fmt.Sprintf("%s is running (herdr agent %s)", m.Name, or(m.Agent, "unknown"))})
	case !m.Enabled:
		out = append(out, docLine{tagNote, "mayor", fmt.Sprintf("%s is stopped (disabled). Let it run again: %s resident start %s", m.Name, yardr, m.Name)})
	case m.Failures > 0 || m.State == "failed":
		out = append(out, docLine{tagFault, "mayor", fmt.Sprintf("%s is not running: %s. Once that is fixed: %s resident start %s",
			m.Name, failed(m.State, m.Failures, m.Reason), yardr, m.Name)})
	case !s.Daemon:
		out = append(out, docLine{tagFault, "mayor", m.Name + " is not running: the daemon starts it"})
	default:
		out = append(out, docLine{tagFault, "mayor", fmt.Sprintf("%s is not running, and no start has failed: the daemon may still be at it (%s resident list)", m.Name, yardr)})
	}
	for _, f := range s.Failing {
		out = append(out, docLine{tagFault, "resident", fmt.Sprintf("%s is not running: %s. Once that is fixed: %s resident start %s",
			f.Name, failed(f.State, f.Failures, f.Reason), yardr, f.Name)})
	}
	if len(s.Failing) == 0 {
		out = append(out, docLine{tagOK, "residents", fmt.Sprintf("%d, none failing to start", s.Residents)})
	}
	if len(s.Held) == 0 {
		out = append(out, docLine{tagOK, "held", "no bead is held"})
	} else {
		out = append(out, docLine{tagNote, "held", fmt.Sprintf("%d held, waiting for someone: %s (%s bead show <id>; %s bead unhold <id> resumes it)",
			len(s.Held), strings.Join(s.Held, ", "), yardr, yardr)})
	}
	if s.Mail != nil {
		out = append(out, s.Mail.line(yardr))
	}
	for _, n := range s.Parties {
		out = append(out, docLine{tagNote, "names", n})
	}
	return out
}

// failed says how a resident's starts fail.
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

// silentMayor is the line for a mayor whose session runs and that is not up
// (see up): doctor itself reads no pane, so the onboarding adds it.
func silentMayor(mayor string) docLine {
	return docLine{tagNote, "mayor", mayor + "'s session runs, and it has not answered its first prompt"}
}

// doctorSays prints what yardr doctor says is wrong with the yard, for an
// onboarding that could not get to the mayor: the report's FAULT lines and
// the mayor's own line, as doctor prints them, and how to read the rest.
func (a *app) doctorSays(ctx context.Context, cfg *config.Config) error {
	rep, err := a.doctor(ctx, cfg, false)
	if err != nil {
		return err
	}
	var say []docLine
	for _, l := range rep.lines() {
		if l.tag == tagFault || l.name == "mayor" {
			say = append(say, l)
		}
	}
	if m := rep.State; m != nil && m.Mayor != nil && m.Mayor.Running {
		st, err := a.store(ctx)
		if err != nil {
			return err
		}
		if r, err := st.GetResident(ctx, m.Mayor.Name); err == nil && r.Live() && !up(ctx, *r) {
			say = append(say, silentMayor(r.Name))
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
		fmt.Printf("  %-5s  %-9s  %s\n", l.tag, l.name, l.text)
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
	}
	if len(r.Optional) > 0 {
		fmt.Println("\nOptional")
		for _, p := range r.Optional {
			state := "not configured"
			if p.Configured {
				state = "configured"
			}
			fmt.Printf("  %-14s  %s: %s\n", state, p.Title, p.Detail)
			for _, n := range p.Notes {
				fmt.Printf("  %-14s  %s: %s\n", tagNote, p.Title, n)
			}
		}
	}
	fmt.Println()
	switch len(r.Faults) {
	case 0:
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

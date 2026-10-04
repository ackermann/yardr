package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
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
	return &cobra.Command{
		Use:   "doctor",
		Short: "What this yard has and what it lacks: programs, what runs, setup steps (read-only)",
		Long: `Print what the yard has and what it lacks, in three parts:

  Needed on this machine  git, herdr and each harness the yard runs on (its
                          default_kind and every kind a group or resident
                          names): on PATH or not, with where to get what is
                          missing. A harness that is found is run with its
                          version flag: on PATH is not "works".
  The yard                the server, the daemon, the mayor and its session,
                          residents whose starts fail, held beads.
  Setup                   the steps from a yard to a first merged bead, each
                          done or todo, and the command for the first one
                          not done. Then what a yard runs without, as
                          configured or not configured: the web view, hooks,
                          packs, the daemon as a login service.

It is read-only: it installs nothing (it prints where to get a program and
never runs that), changes nothing in the store and starts no daemon. It runs
each harness's version flag, for up to ` + versionTimeout.String() + ` each; a kind yardr knows no
version flag for is not run. On a yard whose server is not running it starts
the server, as any command does. On a home with no yard it says so, checks
the machine and creates nothing.

The exit status is 1 when something the yard needs is missing or not working
(the lines marked FAULT), 0 otherwise. A step not done, a held bead, a
stopped mayor and anything not configured are no fault. docs/doctor.md lists
every line.`,
		Example: `  yardr doctor
  yardr doctor --json | jq -r '.next.next'     # the command for the next setup step`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			rep, err := a.doctor(c.Context(), cfg)
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
}

// doctor reads the three parts. It fails only on what stops it from reading
// at all; everything it finds wrong is in the report.
func (a *app) doctor(ctx context.Context, cfg *config.Config) (*doctorReport, error) {
	rep := &doctorReport{Home: cfg.Home, Faults: []string{}}
	// Before the store is asked: connecting starts a server that is not
	// running.
	was := serverRunning(ctx, cfg)
	set, err := a.initialized(ctx, cfg)
	if err != nil {
		// A yard whose server cannot be reached: the machine's part still
		// says something, and the fault is the server's.
		rep.Yard = true
		rep.Machine = machineNeeds(ctx, nil)
		rep.State = &yardState{Server: "unreachable", ServerError: err.Error(), Daemon: daemonRunning(cfg.DaemonLock()),
			Failing: []failingResident{}, Held: []string{}}
		rep.Steps = []setupStep{}
		rep.faults()
		return rep, nil
	}
	if !set {
		rep.Machine = machineNeeds(ctx, nil)
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
	rep.Machine = machineNeeds(ctx, harnessUses(defaultKind(ctx, st), groups, residents))
	rep.State = &yardState{Server: "running", Daemon: daemonRunning(cfg.DaemonLock()), Residents: len(residents),
		Failing: []failingResident{}, Held: []string{}}
	if !was {
		rep.State.Server = "started"
	}
	mayor := yardMayor(ctx, st)
	rep.State.Mayor = &mayorState{Name: mayor}
	for _, r := range residents {
		reason := ""
		failing := r.State == "failed" || (r.Enabled && !r.Live() && r.Failures > 0)
		if failing {
			reason = lastStartFailure(ctx, st, r)
		}
		if r.Name == mayor {
			rep.State.Mayor = &mayorState{Name: mayor, Resident: true, Enabled: r.Enabled, Running: r.Live(), State: r.State,
				Agent: show(r.Handle["agent"]), Failures: r.Failures, Reason: reason}
			if !r.Live() {
				rep.State.Mayor.Agent = ""
			}
		} else if failing {
			rep.State.Failing = append(rep.State.Failing, failingResident{r.Name, r.State, r.Failures, reason})
		}
	}
	open, err := st.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		return nil, err
	}
	for _, b := range open {
		if b.Hold {
			rep.State.Held = append(rep.State.Held, b.ID)
		}
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

// optionalParts are what a yard runs without, each configured or not.
func optionalParts(ctx context.Context, cfg *config.Config, st store.API) ([]optionalPart, error) {
	env := homeEnv(cfg.Home)
	web := optionalPart{Key: "web", Title: "web view",
		Detail: fmt.Sprintf("nothing keeps it running; %syardr web serve shows the yard in a browser while it runs, "+
			"%syardr web install keeps it running from login on (docs/web.md)", env, env)}
	if webService(cfg) {
		web.Configured, web.Detail = true, "kept running as a login service"+at(webServiceAddr(cfg))+fileOf(cfg, svcWeb)
	}
	hooks, err := st.Hooks(ctx)
	if err != nil {
		return nil, err
	}
	hook := optionalPart{Key: "hooks", Title: "hooks",
		Detail: fmt.Sprintf("no source outside the yard reaches it; %syardr hook add --help (docs/subscriptions.md)", env)}
	if len(hooks) > 0 {
		var names []string
		for _, h := range hooks {
			names = append(names, fmt.Sprintf("%s (%s)", h.Name, h.Source))
			if h.Blocked != "" {
				hook.Notes = append(hook.Notes, fmt.Sprintf("hook %s is blocked: %s", h.Name, h.Blocked))
			}
		}
		hook.Configured, hook.Detail = true, strings.Join(names, ", ")
		if cfg.HookAddr == "" {
			// The receiver's address is the server's, set where it was
			// started: this command's environment only hints at it.
			hook.Notes = append(hook.Notes, "YARDR_HOOK_ADDR is not set in this environment: the server receives hooks only when it was started with it")
		}
	}
	packs, err := st.Packs(ctx)
	if err != nil {
		return nil, err
	}
	pack := optionalPart{Key: "packs", Title: "packs",
		Detail: fmt.Sprintf("no pack is imported; %syardr pack apply <dir> --rig <rig> (docs/packs.md)", env)}
	if len(packs) > 0 {
		var names []string
		for _, p := range packs {
			names = append(names, fmt.Sprintf("%s into %s", p.Pack, p.Rig))
		}
		pack.Configured, pack.Detail = true, strings.Join(names, ", ")
	}
	svc := optionalPart{Key: "login_service", Title: "login service",
		Detail: fmt.Sprintf("the daemon runs while its terminal or the session that started it does (%syardr daemon, %syardr init --start); "+
			"it does not come back after a reboot; %syardr daemon install starts it at login", env, env, env)}
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
	return []optionalPart{web, hook, pack, svc}, nil
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

// line is a need as the report prints it.
func (n need) line() docLine {
	if !n.Found {
		return docLine{tagFault, n.Program, fmt.Sprintf("not on PATH: %s. Get it: %s", n.For, n.Get)}
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

// lines are the second part as the report prints it.
func (s *yardState) lines(home string) []docLine {
	env := homeEnv(home)
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
			"Start it in a pane of its own (%syardr daemon) or in the background (%syardr init --start)", env, env)})
	}
	switch m := s.Mayor; {
	case m == nil:
	case !m.Resident:
		out = append(out, docLine{tagFault, "mayor", fmt.Sprintf("no resident %s: %syardr init adds it", m.Name, env)})
	case m.Running:
		out = append(out, docLine{tagOK, "mayor", fmt.Sprintf("%s is running (herdr agent %s)", m.Name, or(m.Agent, "unknown"))})
	case !m.Enabled:
		out = append(out, docLine{tagNote, "mayor", fmt.Sprintf("%s is stopped (disabled). Let it run again: %syardr resident start %s", m.Name, env, m.Name)})
	case m.Failures > 0 || m.State == "failed":
		out = append(out, docLine{tagFault, "mayor", fmt.Sprintf("%s is not running: %s. Once that is fixed: %syardr resident start %s",
			m.Name, failed(m.State, m.Failures, m.Reason), env, m.Name)})
	case !s.Daemon:
		out = append(out, docLine{tagFault, "mayor", m.Name + " is not running: the daemon starts it"})
	default:
		out = append(out, docLine{tagFault, "mayor", fmt.Sprintf("%s is not running, and no start has failed: the daemon may still be at it (%syardr resident list)", m.Name, env)})
	}
	for _, f := range s.Failing {
		out = append(out, docLine{tagFault, "resident", fmt.Sprintf("%s is not running: %s. Once that is fixed: %syardr resident start %s",
			f.Name, failed(f.State, f.Failures, f.Reason), env, f.Name)})
	}
	if len(s.Failing) == 0 {
		out = append(out, docLine{tagOK, "residents", fmt.Sprintf("%d, none failing to start", s.Residents)})
	}
	if len(s.Held) == 0 {
		return append(out, docLine{tagOK, "held", "no bead is held"})
	}
	return append(out, docLine{tagNote, "held", fmt.Sprintf("%d held, waiting for someone: %s (%syardr bead show <id>; %syardr bead unhold <id> resumes it)",
		len(s.Held), strings.Join(s.Held, ", "), env, env)})
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

// faults collects the report's faults from its first two parts.
func (r *doctorReport) faults() {
	lines := []docLine{}
	for _, n := range r.Machine {
		lines = append(lines, n.line())
	}
	if r.State != nil {
		lines = append(lines, r.State.lines(r.Home)...)
	}
	for _, l := range lines {
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
	env := homeEnv(r.Home)
	if r.Yard {
		fmt.Printf("yardr doctor: the yard in %s\n", r.Home)
	} else {
		fmt.Printf("yardr doctor: no yard in %s yet. Create one with `%syardr init --harness <kind>`, or run yardr in a terminal "+
			"for the onboarding. Nothing was created.\n", r.Home, env)
	}
	fmt.Println("\nNeeded on this machine")
	var lines []docLine
	for _, n := range r.Machine {
		lines = append(lines, n.line())
	}
	printLines(lines)
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

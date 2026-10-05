package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/web"
)

// A yard runs without a service manager: the daemon in a pane or in the
// background, the web view while its terminal does. Neither comes back
// after a reboot. A login service is the same command, started by the
// system's own manager at login: a launchd agent on macOS, a systemd user
// unit on Linux. yardr writes the file, loads it, and removes it again; it
// is nothing else, and every yard has its own (the name carries the home).

// The services a yard can have.
const (
	svcDaemon = "daemon"
	svcWeb    = "web"
)

// The service managers yardr writes for.
const (
	launchd = "launchd"
	systemd = "systemd"
)

// unit is one login service of a yard: what the manager runs, with what,
// and the file that says so.
type unit struct {
	Kind    string // daemon, web
	Manager string // launchd, systemd
	// Name is the launchd label, or the systemd unit's name.
	Name string
	Path string // the file
	Home string
	// Program and Args are the command; Env its whole environment besides
	// what the manager sets itself, as KEY=value in the order of the keys.
	Program string
	Args    []string
	Env     []string
	Log     string
}

// serviceDir is where the manager of this system reads a user's services
// from; tests replace it.
var serviceDir = func() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	if goos == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents"), nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "systemd", "user"), nil
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// serviceCtl runs the service manager's own command (launchctl, systemctl)
// and returns what it printed; tests replace it.
var serviceCtl = func(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// executable is the yardr a service runs: this one; tests replace it.
var executable = os.Executable

// signalDaemon tells the daemon of pid to stop; tests replace it.
var signalDaemon = func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// notSupported is a system yardr writes no login service for.
type notSupported struct{ kind string }

func (e notSupported) Error() string {
	byHand := "`yardr daemon` in a terminal of its own, or `yardr init --start` for one in the background"
	if e.kind == svcWeb {
		byHand = "`yardr web serve` in a terminal, or `yardr web start` for one in the background"
	}
	return fmt.Sprintf("a login service is not supported on %s: yardr writes a launchd agent on macOS and a systemd user unit on Linux. "+
		"Run it by hand: %s", goos, byHand)
}

// managerOf is the service manager of this system, "" where yardr knows none.
func managerOf() string {
	switch goos {
	case "darwin":
		return launchd
	case "linux":
		return systemd
	}
	return ""
}

var notIDChar = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// homeID names a home in a service's name: the directory's name, for the
// person reading, and the start of its path's hash, so that two homes of
// one name are two services.
func homeID(home string) string {
	sum := sha256.Sum256([]byte(resolve(home)))
	name := strings.Trim(notIDChar.ReplaceAllString(filepath.Base(home), "-"), "-_")
	return or(name, "yard") + "-" + hex.EncodeToString(sum[:4])
}

// unitFile is the manager, the name and the file of the home's service of
// kind, written or not.
func unitFile(cfg *config.Config, kind string) (manager, name, path string, err error) {
	manager = managerOf()
	if manager == "" {
		return "", "", "", notSupported{kind}
	}
	dir, err := serviceDir()
	if err != nil {
		return "", "", "", err
	}
	if manager == launchd {
		name = "dev.yardr." + kind + "." + homeID(cfg.Home)
		return manager, name, filepath.Join(dir, name+".plist"), nil
	}
	name = "yardr-" + kind + "-" + homeID(cfg.Home) + ".service"
	return manager, name, filepath.Join(dir, name), nil
}

// serviceInstalled reports whether the home's service of kind is written.
func serviceInstalled(cfg *config.Config, kind string) bool {
	_, _, path, err := unitFile(cfg, kind)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// serviceEnv is the environment written into a service, from the
// installer's environ: what a detached server gets (PATH, on which herdr,
// git and the harnesses are found, HOME, the settings the server reads, the
// home), the daemon's own retention setting, and where herdr's socket is
// when the installer's herdr is not at its default place. Nothing else of
// the shell it was installed from: no credential, no identity.
func serviceEnv(environ []string, home string) []string {
	env := server.ServerEnv(environ, home)
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); k == "HERDR_SOCKET_PATH" || k == "XDG_CONFIG_HOME" || config.IsVar(k, "INBOX_RETENTION") {
			env = append(env, kv)
		}
	}
	sort.Strings(env)
	return env
}

// newUnit is the home's service of kind as this yardr would write it now.
func newUnit(cfg *config.Config, kind string, args []string, log string) (*unit, error) {
	manager, name, path, err := unitFile(cfg, kind)
	if err != nil {
		return nil, err
	}
	self, err := executable()
	if err != nil {
		return nil, err
	}
	return &unit{Kind: kind, Manager: manager, Name: name, Path: path, Home: cfg.Home, Program: self,
		Args: args, Env: serviceEnv(os.Environ(), cfg.Home), Log: log}, nil
}

// what is the service's kind in words.
func what(kind string) string {
	if kind == svcWeb {
		return "web view"
	}
	return kind
}

// render is the unit's file.
func (u *unit) render() string {
	if u.Manager == launchd {
		return u.plist()
	}
	return u.unitFile()
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// plist is the unit as a launchd agent. It is started when it is loaded and
// at every login, and started again only when it ends with a failure: a
// daemon told to stop (yardr daemon stop) stays stopped until the next
// login. Its children are not the manager's to end with it (the store's
// server, which the daemon may have started, serves every other command).
func (u *unit) plist() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
`)
	fmt.Fprintf(&b, "<!-- Written by `yardr %s install` for the yard in %s; `yardr %s uninstall` removes it. -->\n",
		u.Kind, xmlText(strings.ReplaceAll(u.Home, "--", "- -")), u.Kind)
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	key := func(k string) { fmt.Fprintf(&b, "\t<key>%s</key>\n", xmlText(k)) }
	str := func(k, v string) { key(k); fmt.Fprintf(&b, "\t<string>%s</string>\n", xmlText(v)) }
	str("Label", u.Name)
	key("ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, a := range append([]string{u.Program}, u.Args...) {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlText(a))
	}
	b.WriteString("\t</array>\n")
	key("EnvironmentVariables")
	b.WriteString("\t<dict>\n")
	for _, kv := range u.Env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlText(k), xmlText(v))
	}
	b.WriteString("\t</dict>\n")
	str("WorkingDirectory", u.Home)
	str("StandardOutPath", u.Log)
	str("StandardErrorPath", u.Log)
	key("RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("KeepAlive")
	b.WriteString("\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	key("AbandonProcessGroup")
	b.WriteString("\t<true/>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// unitWord is s as one word of a systemd unit's line: quoted, with what
// systemd would expand in it (a % specifier; in a command line a $ too)
// written so that it is not.
func unitWord(s string, command bool) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "%", "%%")
	if command {
		s = strings.ReplaceAll(s, "$", "$$")
	}
	return `"` + s + `"`
}

// unitFile is the unit as a systemd user unit, to the same effect as the
// launchd agent: started at login, started again only after a failure, and
// stopped alone (KillMode=process), not with whatever it started.
func (u *unit) unitFile() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by `yardr %s install` for the yard in %s; `yardr %s uninstall` removes it.\n", u.Kind, u.Home, u.Kind)
	fmt.Fprintf(&b, "[Unit]\nDescription=yardr %s for %s\n\n[Service]\n", what(u.Kind), strings.ReplaceAll(u.Home, "%", "%%"))
	words := []string{unitWord(u.Program, true)}
	for _, a := range u.Args {
		words = append(words, unitWord(a, true))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(words, " "))
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", strings.ReplaceAll(u.Home, "%", "%%"))
	for _, kv := range u.Env {
		fmt.Fprintf(&b, "Environment=%s\n", unitWord(kv, false))
	}
	log := strings.ReplaceAll(u.Log, "%", "%%")
	fmt.Fprintf(&b, "StandardOutput=append:%s\nStandardError=append:%s\n", log, log)
	b.WriteString("Restart=on-failure\nRestartSec=10\nKillMode=process\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

// domain is the launchd domain of this user's login session.
func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// loaded reports whether the manager has the unit: launchd knows it, or
// systemd starts it at login.
func (u *unit) loaded(ctx context.Context) bool {
	var err error
	if u.Manager == launchd {
		_, err = serviceCtl(ctx, "launchctl", "print", domain()+"/"+u.Name)
	} else {
		_, err = serviceCtl(ctx, "systemctl", "--user", "is-enabled", u.Name)
	}
	return err == nil
}

// loadCommands are the manager's command lines that start the unit now and
// at every login.
func (u *unit) loadCommands() [][]string {
	if u.Manager == launchd {
		return [][]string{{"launchctl", "bootstrap", domain(), u.Path}}
	}
	return [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", u.Name},
		{"systemctl", "--user", "restart", u.Name}}
}

// unloadCommand is the manager's command line that stops the unit and no
// longer starts it at login.
func (u *unit) unloadCommand() []string {
	if u.Manager == launchd {
		return []string{"launchctl", "bootout", domain() + "/" + u.Name}
	}
	return []string{"systemctl", "--user", "disable", "--now", u.Name}
}

func shellLine(args []string) string {
	words := make([]string, len(args))
	for i, a := range args {
		words[i] = shellQuote(a)
	}
	return strings.Join(words, " ")
}

func shellLines(cmds [][]string) string {
	lines := make([]string, len(cmds))
	for i, c := range cmds {
		lines[i] = shellLine(c)
	}
	return strings.Join(lines, " && ")
}

func ctl(ctx context.Context, args []string) error {
	if out, err := serviceCtl(ctx, args[0], args[1:]...); err != nil {
		return fmt.Errorf("%s: %w: %s", shellLine(args), err, out)
	}
	return nil
}

// unload stops the unit at its manager, when the manager has it, and waits
// until launchd has let go of it: a bootstrap right after a bootout fails
// while the old job is still ending.
func (u *unit) unload(ctx context.Context) (bool, error) {
	if !u.loaded(ctx) {
		return false, nil
	}
	if err := ctl(ctx, u.unloadCommand()); err != nil {
		return false, err
	}
	for i := 0; u.Manager == launchd && i < 50 && u.loaded(ctx); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	return true, nil
}

// serviceResult is what an install did.
type serviceResult struct {
	Home    string   `json:"home"`
	Kind    string   `json:"kind"`
	Manager string   `json:"manager"`
	Name    string   `json:"name"`
	File    string   `json:"file"`
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Env     []string `json:"env"`
	Log     string   `json:"log"`
	// Address is where the web view listens.
	Address string `json:"address,omitempty"`
	// Loaded: the manager has it and has started it. False under
	// --file-only, when Load is how to.
	Loaded bool   `json:"loaded"`
	Load   string `json:"load,omitempty"`
	// StoppedPID is the daemon that ran before and was stopped for the
	// service's own.
	StoppedPID int      `json:"stopped_pid,omitempty"`
	Warnings   []string `json:"warnings"`
}

// install writes the unit's file and, unless fileOnly, hands it to the
// manager, which starts it now and at every login. An install over an
// installed service replaces it. For the daemon, one that runs already is
// stopped first: the yard has one daemon, and the service's would end on
// the lock the other holds.
func (u *unit) install(ctx context.Context, cfg *config.Config, fileOnly bool) (*serviceResult, error) {
	res := &serviceResult{Home: u.Home, Kind: u.Kind, Manager: u.Manager, Name: u.Name, File: u.Path, Program: u.Program, Args: u.Args,
		Env: u.Env, Log: u.Log, Load: shellLines(u.loadCommands()), Warnings: []string{}}
	if !fileOnly && u.Manager == systemd {
		if _, err := lookPath("systemctl"); err != nil {
			return nil, fmt.Errorf("systemctl is not on PATH: this system has no systemd for yardr to load the unit into. "+
				"--file-only writes %s and loads nothing", u.Path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
		return nil, err
	}
	if !fileOnly {
		if _, err := u.unload(ctx); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(u.Path, []byte(u.render()), 0o644); err != nil {
		return nil, err
	}
	if fileOnly {
		return res, nil
	}
	if u.Kind == svcDaemon {
		pid, err := stopDaemon(cfg, stopWait)
		if err != nil {
			return nil, fmt.Errorf("%w; the service is written (%s) and not loaded: %s", err, u.Path, res.Load)
		}
		res.StoppedPID = pid
	}
	for _, c := range u.loadCommands() {
		if err := ctl(ctx, c); err != nil {
			return nil, fmt.Errorf("%w; the service is written (%s) and not loaded", err, u.Path)
		}
	}
	res.Loaded, res.Load = true, ""
	return res, nil
}

func (r *serviceResult) print() {
	kind := "launchd agent"
	if r.Manager == systemd {
		kind = "systemd user unit"
	}
	fmt.Printf("%s %s written: %s\n\n  runs     %s\n", kind, r.Name, r.File, shellLine(append([]string{r.Program}, r.Args...)))
	for i, kv := range r.Env {
		head := "         "
		if i == 0 {
			head = "  with   "
		}
		fmt.Printf("%s  %s\n", head, kv)
	}
	fmt.Printf("  logs to  %s\n\n", r.Log)
	for _, w := range r.Warnings {
		fmt.Printf("Mind: %s\n\n", w)
	}
	if r.StoppedPID != 0 {
		fmt.Printf("The daemon that was running (pid %d) is stopped: the service runs the yard's one daemon now.\n", r.StoppedPID)
	}
	switch {
	case !r.Loaded && r.Manager == launchd:
		fmt.Printf("Not loaded (--file-only): nothing is started now. launchd loads it at your next login, or now with:\n\n  %s\n", r.Load)
	case !r.Loaded:
		fmt.Printf("Not loaded (--file-only): nothing is started, now or at login. To load it:\n\n  %s\n", r.Load)
	default:
		fmt.Printf("%s has started the %s and starts it at every login.\n", r.Manager, what(r.Kind))
		if r.Manager == systemd {
			fmt.Println("A user unit runs while you are logged in; `loginctl enable-linger` keeps it from boot to shutdown.")
		}
	}
	if r.Address != "" && r.Loaded {
		fmt.Printf("\nThe web view: http://%s/\n", r.Address)
	}
	fmt.Printf("\nTo remove it: %s %s uninstall\n", yardrAt(r.Home), r.Kind)
}

// removeResult is what an uninstall did.
type removeResult struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	File string `json:"file"`
	// Removed is false when no service was installed.
	Removed  bool `json:"removed"`
	Unloaded bool `json:"unloaded"`
}

// uninstall takes the home's service of kind from its manager, which stops
// what the service ran, and removes its file: all an install made.
func (a *app) uninstall(ctx context.Context, cfg *config.Config, kind string, fileOnly bool) error {
	manager, name, path, err := unitFile(cfg, kind)
	if err != nil {
		return err
	}
	u := &unit{Kind: kind, Manager: manager, Name: name, Path: path, Home: cfg.Home}
	res := &removeResult{Kind: kind, Name: name, File: path}
	yardr := yardrAt(cfg.Home)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return a.out(res, func() {
			fmt.Printf("No %s service is installed for the yard in %s (%s is not there). Nothing was changed.\n", what(kind), cfg.Home, path)
		})
	} else if err != nil {
		return err
	}
	if !fileOnly {
		if res.Unloaded, err = u.unload(ctx); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	res.Removed = true
	if !fileOnly && manager == systemd {
		_ = ctl(ctx, []string{"systemctl", "--user", "daemon-reload"})
	}
	return a.out(res, func() {
		fmt.Printf("removed %s\n", path)
		switch {
		case fileOnly:
			fmt.Printf("Not unloaded (--file-only): what %s runs of it now is left running. To unload it: %s\n", manager, shellLine(u.unloadCommand()))
		case res.Unloaded && kind == svcDaemon:
			fmt.Printf("%s no longer has %s and has stopped the daemon it ran. The sessions it started carry on in herdr;\n"+
				"nothing is dispatched until a daemon runs: %s init --start, or %s daemon in a pane of its own.\n", manager, name, yardr, yardr)
		case res.Unloaded:
			fmt.Printf("%s no longer has %s and has stopped the web view it ran.\n", manager, name)
		default:
			fmt.Printf("%s did not have %s loaded; nothing was stopped.\n", manager, name)
		}
	})
}

// stopWait is how long a stop waits for the daemon to end: it finishes the
// pass it is in first.
const stopWait = 30 * time.Second

// daemonPID is the pid of the daemon that holds the home's lock, 0 when
// none does.
func daemonPID(lock string) int {
	if !daemonRunning(lock) {
		return 0
	}
	b, err := os.ReadFile(lock)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// stopDaemon tells the home's daemon to stop and waits until it has let go
// of the lock. It returns the pid it stopped, 0 when no daemon ran.
func stopDaemon(cfg *config.Config, wait time.Duration) (int, error) {
	lock := cfg.DaemonLock()
	if !daemonRunning(lock) {
		return 0, nil
	}
	pid := daemonPID(lock)
	if pid <= 0 {
		return 0, fmt.Errorf("a daemon holds %s, and the file names no pid to stop", lock)
	}
	if err := signalDaemon(pid); err != nil {
		return 0, fmt.Errorf("stop the daemon (pid %d): %w", pid, err)
	}
	for deadline := time.Now().Add(wait); daemonRunning(lock); time.Sleep(pollEvery / 5) {
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("the daemon (pid %d) was told to stop and still holds %s after %s", pid, lock, wait)
		}
	}
	return pid, nil
}

// serviceState is a service as yardr daemon status reports it.
type serviceState struct {
	Supported bool   `json:"supported"`
	Manager   string `json:"manager,omitempty"`
	Name      string `json:"name,omitempty"`
	File      string `json:"file,omitempty"`
	Installed bool   `json:"installed"`
	Loaded    bool   `json:"loaded"`
}

func stateOf(ctx context.Context, cfg *config.Config, kind string) serviceState {
	manager, name, path, err := unitFile(cfg, kind)
	if err != nil {
		return serviceState{}
	}
	s := serviceState{Supported: true, Manager: manager, Name: name, File: path, Installed: serviceInstalled(cfg, kind)}
	if s.Installed {
		s.Loaded = (&unit{Manager: manager, Name: name}).loaded(ctx)
	}
	return s
}

func (s serviceState) line() string {
	switch {
	case !s.Supported:
		return "not supported on " + goos
	case !s.Installed:
		return "not installed"
	case s.Loaded:
		return fmt.Sprintf("installed and loaded: %s starts it at login (%s)", s.Manager, s.File)
	}
	return fmt.Sprintf("installed, not loaded in %s now (%s)", s.Manager, s.File)
}

// daemonState is the daemon as yardr daemon status and yardr status report
// it: whether one holds the home's lock, and its login service.
type daemonState struct {
	Running bool         `json:"running"`
	PID     int          `json:"pid"`
	Service serviceState `json:"service"`
}

func readDaemon(ctx context.Context, cfg *config.Config) daemonState {
	return daemonState{Running: daemonRunning(cfg.DaemonLock()), PID: daemonPID(cfg.DaemonLock()), Service: stateOf(ctx, cfg, svcDaemon)}
}

// line is whether the daemon runs, in words.
func (d daemonState) line() string {
	if d.Running {
		return fmt.Sprintf("running (pid %d)", d.PID)
	}
	return "not running"
}

// protectedNote is what a daemon started at login cannot do in the rigs
// named, and what to grant. macOS only.
func protectedNote(rigs []string, program string) string {
	return fmt.Sprintf("%s in a folder macOS protects (~/Desktop, ~/Documents, ~/Downloads). A daemon started at login "+
		"does not have your terminal's access there: until yardr has its own, work in such a rig can fail with \"operation not permitted\". "+
		"Grant it in System Settings, Privacy & Security: Full Disk Access (add %s), or Files and Folders once macOS has asked. "+
		"A new build of yardr can need the grant again (docs/configuration.md, \"Login services\")",
		list(rigs)+isAre(rigs), program)
}

// daemonWarnings are what the daemon as a service will lack, as far as it
// shows now: a program not on the PATH written into it, and on macOS the
// rigs it has no access to.
func (a *app) daemonWarnings(ctx context.Context, cfg *config.Config, program string) ([]string, error) {
	var out []string
	needs := basicNeeds()
	set, err := a.initialized(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if set {
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
		for _, u := range harnessUses(defaultKind(ctx, st), groups, residents) {
			needs = append(needs, harnessNeed(u.kind, "the harness of "+strings.Join(u.by, ", ")))
		}
		rigs, err := st.Rigs(ctx)
		if err != nil {
			return nil, err
		}
		if home, err := userHomeDir(); goos == "darwin" && err == nil {
			if in := protectedRigs(rigs, home); len(in) > 0 {
				for i := range in {
					in[i] = "rig " + in[i]
				}
				out = append(out, protectedNote(in, program))
			}
		}
	}
	for _, n := range needs {
		if !n.Found {
			out = append(out, fmt.Sprintf("%s is not on the PATH written into the service (%s). Install it there and run this again from a shell that finds it",
				n.Program, n.For))
		}
	}
	return out, nil
}

// daemonService adds install, uninstall, stop and status to yardr daemon.
func (a *app) daemonService(cmd *cobra.Command) {
	var fileOnly bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Start the daemon at login: a launchd agent (macOS) or a systemd user unit (Linux)",
		Long: `Write the daemon as a login service of this user and load it: a launchd
agent in ~/Library/LaunchAgents on macOS, a systemd user unit in
~/.config/systemd/user on Linux. The service manager starts the daemon now
and at every login, and starts it again when it ends with a failure. A daemon
that runs already is stopped first: a yard has one.

The service is this yard's: its name carries the home, so a second yard gets
a service of its own. Its file holds the yardr that ran the install (by its
path), YARDR_HOME, and the PATH of the shell it was installed from, on which
herdr, git and the harnesses have to be found. It gets nothing else of that
shell: an install says what is not on that PATH. It logs to
<home>/daemon.log.

On macOS it also says which rigs are in ~/Desktop, ~/Documents or
~/Downloads, where a daemon started at login has no access until it is
granted its own. On any other system it says that it is not supported and how
to run the daemon by hand.

--file-only writes the file and loads nothing. docs/configuration.md,
"Login services", says what a reboot and an upgrade then do.`,
		Example: `  yardr daemon install
  yardr daemon install --file-only      # write the file, load nothing
  yardr daemon status
  yardr daemon uninstall`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			u, err := newUnit(cfg, svcDaemon, []string{"daemon"}, cfg.DaemonLog())
			if err != nil {
				return err
			}
			warn, err := a.daemonWarnings(c.Context(), cfg, u.Program)
			if err != nil {
				return err
			}
			res, err := u.install(c.Context(), cfg, fileOnly)
			if err != nil {
				return err
			}
			res.Warnings = append(res.Warnings, warn...)
			return a.out(res, res.print)
		},
	}
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the daemon's login service: unload it, which stops its daemon, and delete its file",
		Long: `Take the daemon's login service from its manager and delete its file: all
that yardr daemon install made. The manager stops the daemon the service
ran; the sessions that daemon started carry on in herdr. A daemon started any
other way is not the service's and is left running.

--file-only deletes the file and unloads nothing.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return a.uninstall(c.Context(), cfg, svcDaemon, fileOnly)
		},
	}
	for _, c := range []*cobra.Command{install, uninstall} {
		c.Flags().BoolVar(&fileOnly, "file-only", false, "write or delete the service's file only; leave the service manager alone")
	}
	var wait time.Duration
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the yard's daemon",
		Long: `Tell the daemon that holds the home's lock to stop, and wait until it has:
it finishes the pass it is in. The sessions it started carry on in herdr, and
nothing is dispatched until a daemon runs again.

A daemon that runs as a login service stays stopped too: the service starts
it again at the next login, or when it ends with a failure, and a stop is
none. yardr init --start starts one in the background before that.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			pid, err := stopDaemon(cfg, wait)
			if err != nil {
				return err
			}
			return a.out(map[string]any{"home": cfg.Home, "stopped": pid != 0, "pid": pid}, func() {
				if pid == 0 {
					fmt.Printf("no daemon is running on the yard in %s\n", cfg.Home)
					return
				}
				fmt.Printf("daemon stopped (pid %d). Start one again: %s init --start\n", pid, yardrAt(cfg.Home))
			})
		},
	}
	stop.Flags().DurationVar(&wait, "wait", stopWait, "how long to wait for it to end")
	status := &cobra.Command{
		Use:   "status",
		Short: "Report whether the daemon runs, and whether it is a login service",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			d := readDaemon(c.Context(), cfg)
			return a.out(struct {
				Home string `json:"home"`
				daemonState
			}{cfg.Home, d}, func() {
				fmt.Printf("daemon   %s\n", d.line())
				fmt.Printf("service  %s\n", d.Service.line())
			})
		},
	}
	cmd.AddCommand(install, uninstall, stop, status)
}

// webAddrInFile finds the address a written web service listens on.
var webAddrInFile = regexp.MustCompile(`--addr(?:</string>\s*<string>|"\s+")([^<"\s]+)`)

// webServiceAddr is the address the home's web service listens on, "" when
// there is none or its file does not say.
func webServiceAddr(cfg *config.Config) string {
	_, _, path, err := unitFile(cfg, svcWeb)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if m := webAddrInFile.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// webLocalAddr is where the web view listens unless told otherwise.
const webLocalAddr = "127.0.0.1:8791"

// webLog is where a web view that no terminal runs writes its log.
func webLog(cfg *config.Config) string { return filepath.Join(cfg.Home, "web.log") }

// spawnWeb starts `yardr web serve` for the home on addr, detached from this
// terminal and logging to the home's web.log, as server.Start starts the
// server and with as little of the starter's environment: the view reads
// the store and nothing else. Tests replace it.
var spawnWeb = func(cfg *config.Config, addr string) (pid int, exited <-chan error, stop func() error, err error) {
	self, err := os.Executable()
	if err != nil {
		return 0, nil, nil, err
	}
	logf, err := os.OpenFile(webLog(cfg), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, nil, nil, err
	}
	defer logf.Close()
	cmd := exec.Command(self, "web", "serve", "--addr", addr)
	cmd.Env = server.ServerEnv(os.Environ(), cfg.Home)
	cmd.Dir = cfg.Home
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd.Process.Pid, done, cmd.Process.Kill, nil
}

// answers reports whether something accepts connections on addr; tests
// replace it.
var answers = func(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// webStartWait is how long a started web view gets to listen.
var webStartWait = 10 * time.Second

// startWeb starts the web view in the background and returns once it
// listens.
func startWeb(cfg *config.Config, addr string) (int, error) {
	if answers(addr) {
		return 0, fmt.Errorf("something listens on %s already: a web view that runs, perhaps (http://%s/). --addr takes another port", addr, addr)
	}
	pid, exited, stop, err := spawnWeb(cfg, addr)
	if err != nil {
		return 0, err
	}
	for deadline := time.Now().Add(webStartWait); time.Now().Before(deadline); {
		select {
		case <-exited:
			return 0, fmt.Errorf("the web view ended right after it started; see %s", webLog(cfg))
		case <-time.After(pollEvery / 5):
		}
		if answers(addr) {
			return pid, nil
		}
	}
	stopErr := stop()
	if stopErr != nil && !errors.Is(stopErr, os.ErrProcessDone) {
		return 0, fmt.Errorf("the web view (pid %d) did not listen on %s within %s; could not stop it: %v; see %s", pid, addr, webStartWait, stopErr, webLog(cfg))
	}
	return 0, fmt.Errorf("the web view (pid %d) did not listen on %s within %s; stopped it; see %s", pid, addr, webStartWait, webLog(cfg))
}

// webService adds install, uninstall and start to yardr web.
func (a *app) webServiceCmds(cmd *cobra.Command) {
	var addr string
	var fileOnly bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Keep the web view running from login on: a launchd agent (macOS) or a systemd user unit (Linux)",
		Long: `Write yardr web serve as a login service of this user and load it, as
yardr daemon install does for the daemon: the service manager starts the view
now and at every login. It prints the view's address and logs to
<home>/web.log.

A service listens on loopback only: --addr takes 127.0.0.1, ::1 or localhost
with a port, and there is no --unsafe here, nor the --host that goes with
it. To reach the view from another machine, forward the port over ssh
(docs/web.md).

--file-only writes the file and loads nothing.`,
		Example: `  yardr web install                       # http://127.0.0.1:8791/
  yardr web install --addr 127.0.0.1:9000
  yardr web uninstall`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// Never off loopback, whatever a later yardr web serve allows.
			if err := web.CheckAddr(addr, false); err != nil {
				return fmt.Errorf("%w. A service never listens off loopback", err)
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			u, err := newUnit(cfg, svcWeb, []string{"web", "serve", "--addr", addr}, webLog(cfg))
			if err != nil {
				return err
			}
			res, err := u.install(c.Context(), cfg, fileOnly)
			if err != nil {
				return err
			}
			res.Address = addr
			return a.out(res, res.print)
		},
	}
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the web view's login service: unload it, which stops the view, and delete its file",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return a.uninstall(c.Context(), cfg, svcWeb, fileOnly)
		},
	}
	for _, c := range []*cobra.Command{install, uninstall} {
		c.Flags().BoolVar(&fileOnly, "file-only", false, "write or delete the service's file only; leave the service manager alone")
	}
	start := &cobra.Command{
		Use:   "start",
		Short: "Start the web view in the background, on loopback, and print its address",
		Long: `Start yardr web serve detached from this terminal, logging to
<home>/web.log, wait until it listens and print its address. It runs until
it is killed or the machine restarts; yardr web install keeps one running
from login on instead. It listens on loopback only.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := web.CheckAddr(addr, false); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			pid, err := startWeb(cfg, addr)
			if err != nil {
				return err
			}
			return a.out(map[string]any{"address": addr, "url": "http://" + addr + "/", "pid": pid, "log": webLog(cfg)}, func() {
				fmt.Printf("The web view: http://%s/\n\nIt runs in the background (pid %d) and logs to %s. To stop it: kill %d\n",
					addr, pid, webLog(cfg), pid)
			})
		},
	}
	for _, c := range []*cobra.Command{install, start} {
		c.Flags().StringVar(&addr, "addr", webLocalAddr, "listen address, on loopback")
	}
	cmd.AddCommand(install, uninstall, start)
}

// offerServices asks, once the yard runs, for the two things it runs
// without and a person may want from the first day: the daemon at login,
// and the web view. Both start at no. Each yes ends in the command that
// does it, shown before it runs; a command that fails says so and the
// onboarding goes on, since the yard runs either way. What the yard has
// already is not asked for again: an onboarding taken up after a run that
// got this far (resume) finds the service installed, or the view answering.
func (a *app) offerServices(ctx context.Context, cfg *config.Config) error {
	yardr := yardrAt(cfg.Home)
	try := func(args ...string) {
		fmt.Printf("\n  $ %s\n\n", commandLine(cfg.Home, args))
		if err := a.sub(ctx, args...); err != nil {
			fmt.Printf("yardr: %v\nThe yard runs without it; `%s` tries again.\n", err, commandLine(cfg.Home, args))
		}
	}
	manager := managerOf()
	fmt.Println()
	if manager != "" && !loginService(cfg) {
		login := false
		desc := fmt.Sprintf("The daemon runs in the background now, and does not come back after a reboot.\n"+
			"Yes installs it as a login service (%s), which starts it at every login.\n"+
			"Later: %s daemon install", serviceKind(manager), yardr)
		if manager == launchd {
			desc += "\n\nOn macOS a daemon started at login does not have this terminal's access to ~/Desktop,\n" +
				"~/Documents and ~/Downloads: a repository there needs a grant to yardr of its own."
		}
		if err := ask(huh.NewConfirm().Key("login").Title("Start the daemon at login?").Description(desc).Value(&login)); err != nil {
			return err
		}
		if login {
			try("daemon", "install")
		}
	}
	if webService(cfg) || answers(webLocalAddr) {
		return nil
	}
	view := false
	if err := ask(huh.NewConfirm().Key("web").Title("Start the web view now?").
		Description(fmt.Sprintf("The yard in a browser, read-only, on this machine only: http://%s/ (docs/web.md).\n"+
			"Later: %s web start", webLocalAddr, yardr)).Value(&view)); err != nil || !view {
		return err
	}
	atLogin := false
	if manager != "" {
		if err := ask(huh.NewConfirm().Key("web_login").Title("And at login?").
			Description(fmt.Sprintf("Yes installs the web view as a login service too (%s). No starts it for now:\n"+
				"it runs until it is killed or the machine restarts.", serviceKind(manager))).Value(&atLogin)); err != nil {
			return err
		}
	}
	if atLogin {
		try("web", "install")
	} else {
		try("web", "start")
	}
	return nil
}

// serviceKind is what yardr writes for a manager, in words.
func serviceKind(manager string) string {
	if manager == launchd {
		return "a launchd agent"
	}
	return "a systemd user unit"
}

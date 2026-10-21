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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"io"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

// A yard runs without a service manager: the daemon in a pane or in the
// background, the web view while its terminal does. Neither comes back
// after a reboot. A login service is the same command, started by the
// system's own manager at login: a launchd agent on macOS, a systemd user
// unit on Linux. yardr writes the file, loads it, and removes it again; it
// is nothing else, and every yard has its own (the name carries the home).

// The service managers yardr writes for.
const (
	launchd = "launchd"
	systemd = "systemd"
)

// unit is one login service of a yard: what the manager runs, with what,
// and the file that says so.
type unit struct {
	Kind    string // the long-running command it runs: daemon, web, bdp, otel; or herdr, for its server (login.go)
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
	// Mode is the file's: 0600 for a kind whose environment may hold a
	// credential (the telemetry export's headers).
	Mode os.FileMode
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

// notSupported is a system yardr writes no login service for.
type notSupported struct{ kind string }

func (e notSupported) Error() string {
	k := kindOf(e.kind)
	return fmt.Sprintf("a login service is not supported on %s: yardr writes a launchd agent on macOS and a systemd user unit on Linux. "+
		"Run it by hand: `yardr %s` in a terminal of its own, or `yardr %s start` for one in the background", goos, strings.Join(k.command(), " "), k.Name)
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

// resolveHome is resolve for a home that may no longer exist: EvalSymlinks
// fails when nothing is there, and a service installed while the home
// existed must keep its name after the home is removed by hand, or
// uninstall no longer finds it. Only what exists can hold a symlink, so
// resolving the longest prefix that does and appending the rest names what
// EvalSymlinks would have, had the home been there.
func resolveHome(home string) string {
	if r, err := filepath.EvalSymlinks(home); err == nil {
		return r
	} else if !errors.Is(err, fs.ErrNotExist) {
		return home
	}
	var rest []string
	dir := home
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return home
		}
		rest = append(rest, filepath.Base(dir))
		dir = parent
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r
		} else if !errors.Is(err, fs.ErrNotExist) {
			return home
		}
	}
}

// homeID names a home in a service's name: the directory's name, for the
// person reading, and the start of its path's hash, so that two homes of
// one name are two services.
func homeID(home string) string {
	sum := sha256.Sum256([]byte(resolveHome(home)))
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
// installer's environ for a kind: what a detached server gets (PATH, on which herdr,
// git and the harnesses are found, HOME, the settings the server reads, the
// home), the daemon's own retention setting, and where herdr's socket is
// when the installer's herdr is not at its default place (a yard that names
// its herdr session needs none of it: the daemon reads the herdr_session
// setting itself, see runner.Herdr), and the variables the kind reads
// itself. Nothing else of the shell it was installed from: no
// credential but the kind's own, no identity.
func serviceEnv(environ []string, home string, k *longRunning) []string {
	env := append(server.ServerEnv(environ, home), k.vars(environ)...)
	// These process hooks help tests stop a detached process, but a login
	// service outlives the process that installed it.
	env = slices.DeleteFunc(env, func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return config.IsVar(key, config.TestOwnerEnv, config.TestHomeCheckEnv)
	})
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); k == "HERDR_SOCKET_PATH" || k == "XDG_CONFIG_HOME" || config.IsVar(k, "INBOX_RETENTION") {
			env = append(env, kv)
		}
	}
	sort.Strings(env)
	return env
}

// newUnit is the home's service of the kind as this yardr would write it
// now, to run it with args, from environ, the installing run's environment
// (see app.environ).
func newUnit(environ []string, cfg *config.Config, k *longRunning, args []string) (*unit, error) {
	manager, name, path, err := unitFile(cfg, k.Name)
	if err != nil {
		return nil, err
	}
	self, err := executable()
	if err != nil {
		return nil, err
	}
	u := &unit{Kind: k.Name, Manager: manager, Name: name, Path: path, Home: cfg.Home, Program: self,
		Args: append(k.command(), args...), Env: serviceEnv(environ, cfg.Home, k), Log: k.Log(cfg), Mode: 0o644}
	if k.Vars != nil {
		u.Mode = 0o600
	}
	return u, nil
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
	if u.Kind == svcHerdr {
		fmt.Fprintf(&b, "<!-- %s -->\n", herdrWritten)
	} else {
		fmt.Fprintf(&b, "<!-- Written by `yardr %s install` for the yard in %s; `yardr %s uninstall` removes it. -->\n",
			u.Kind, xmlText(strings.ReplaceAll(u.Home, "--", "- -")), u.Kind)
	}
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
	if u.Log != "" {
		str("StandardOutPath", u.Log)
		str("StandardErrorPath", u.Log)
	}
	key("RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("KeepAlive")
	b.WriteString("\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	if u.Kind != svcHerdr {
		key("AbandonProcessGroup")
		b.WriteString("\t<true/>\n")
	}
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
	if u.Kind == svcHerdr {
		fmt.Fprintf(&b, "# %s\n[Unit]\nDescription=herdr server\n\n[Service]\n", herdrWritten)
	} else {
		fmt.Fprintf(&b, "# Written by `yardr %s install` for the yard in %s; `yardr %s uninstall` removes it.\n", u.Kind, u.Home, u.Kind)
		fmt.Fprintf(&b, "[Unit]\nDescription=yardr %s for %s\n\n[Service]\n", what(u.Kind), strings.ReplaceAll(u.Home, "%", "%%"))
	}
	words := []string{unitWord(u.Program, true)}
	for _, a := range u.Args {
		words = append(words, unitWord(a, true))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(words, " "))
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", strings.ReplaceAll(u.Home, "%", "%%"))
	for _, kv := range u.Env {
		fmt.Fprintf(&b, "Environment=%s\n", unitWord(kv, false))
	}
	if u.Kind == svcHerdr {
		// Its output is the journal's, and its panes end with it, as herdr
		// server stop ends them.
		b.WriteString("Restart=on-failure\n\n[Install]\nWantedBy=default.target\n")
		return b.String()
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

// restartCommand is the manager's command line that restarts the unit
// where it runs: the view on the newly installed build, after an upgrade.
func (u *unit) restartCommand() []string {
	if u.Manager == launchd {
		return []string{"launchctl", "kickstart", "-k", domain() + "/" + u.Name}
	}
	return []string{"systemctl", "--user", "restart", u.Name}
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
	// Address is where it listens, for a kind that does.
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

// privateLogFile makes the service's log file with 0600 when none is
// there, and chmods an existing one to it.
func privateLogFile(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("make the service's log %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("make the service's log %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil { // past the umask
		return fmt.Errorf("chmod the service's log %s to 0600: %w", path, err)
	}
	return nil
}

// install writes the unit's file, makes its log with 0600 (an existing
// one is chmodded to it) and, unless fileOnly, hands it to the manager,
// which starts it now and at every login. An install over an installed
// service replaces it. For a kind the yard has one of (the daemon), one
// that runs already is stopped first: the service's would end on the lock
// the other holds.
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
	if err := os.WriteFile(u.Path, []byte(u.render()), u.Mode); err != nil {
		return nil, err
	}
	// The manager writes the service's log to the file as it is, but
	// makes a missing one with its own mode; made here it is 0600, as
	// the logs yardr writes itself are, and an existing one is chmodded
	// to it, before the manager sees the service.
	if u.Log != "" {
		if err := privateLogFile(u.Log); err != nil {
			return nil, fmt.Errorf("%w; the service is written (%s)", err, u.Path)
		}
	}
	if fileOnly {
		return res, nil
	}
	if k := kindOf(u.Kind); k.Lock != nil {
		run, err := k.stop(cfg, stopWait)
		if err != nil {
			return nil, fmt.Errorf("%w; the service is written (%s) and not loaded: %s", err, u.Path, res.Load)
		}
		if len(run) > 0 {
			res.StoppedPID = run[0].PID
		}
	}
	for _, c := range u.loadCommands() {
		if err := ctl(ctx, c); err != nil {
			return nil, fmt.Errorf("%w; the service is written (%s) and not loaded", err, u.Path)
		}
	}
	res.Loaded, res.Load = true, ""
	return res, nil
}

func (r *serviceResult) print(w io.Writer) {
	kind := "launchd agent"
	if r.Manager == systemd {
		kind = "systemd user unit"
	}
	fmt.Fprintf(w, "%s %s written: %s\n\n  runs     %s\n", kind, r.Name, r.File, shellLine(append([]string{r.Program}, r.Args...)))
	for i, kv := range r.Env {
		head := "         "
		if i == 0 {
			head = "  with   "
		}
		fmt.Fprintf(w, "%s  %s\n", head, kv)
	}
	fmt.Fprintf(w, "  logs to  %s\n\n", r.Log)
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "Mind: %s\n\n", warning)
	}
	if r.StoppedPID != 0 {
		fmt.Fprintf(w, "The %[1]s that was running (pid %[2]d) is stopped: the service runs the yard's one %[1]s now.\n", what(r.Kind), r.StoppedPID)
	}
	switch {
	case !r.Loaded && r.Manager == launchd:
		fmt.Fprintf(w, "Not loaded (--file-only): nothing is started now. launchd loads it at your next login, or now with:\n\n  %s\n", r.Load)
	case !r.Loaded:
		fmt.Fprintf(w, "Not loaded (--file-only): nothing is started, now or at login. To load it:\n\n  %s\n", r.Load)
	default:
		fmt.Fprintf(w, "%s has started the %s and starts it at every login.\n", r.Manager, what(r.Kind))
		if r.Manager == systemd {
			fmt.Fprintln(w, "A user unit runs while you are logged in; `loginctl enable-linger` keeps it from boot to shutdown.")
		}
	}
	if r.Address != "" && r.Loaded {
		fmt.Fprintf(w, "\nThe %s: %s\n", what(r.Kind), webURL(r.Address))
	}
	fmt.Fprintf(w, "\nTo remove it: %s %s uninstall\n", yardrAt(r.Home), r.Kind)
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

// removeUnit takes the home's service of kind from its manager, which stops
// what the service ran, and removes its file: all an install made. With no
// file there it changes nothing, and the result says so.
func removeUnit(ctx context.Context, cfg *config.Config, kind string, fileOnly bool) (*unit, *removeResult, error) {
	manager, name, path, err := unitFile(cfg, kind)
	if err != nil {
		return nil, nil, err
	}
	u := &unit{Kind: kind, Manager: manager, Name: name, Path: path, Home: cfg.Home}
	res := &removeResult{Kind: kind, Name: name, File: path}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return u, res, nil
	} else if err != nil {
		return nil, nil, err
	}
	if !fileOnly {
		if res.Unloaded, err = u.unload(ctx); err != nil {
			return nil, nil, err
		}
	}
	if err := os.Remove(path); err != nil {
		return nil, nil, err
	}
	res.Removed = true
	if !fileOnly && manager == systemd {
		_ = ctl(ctx, []string{"systemctl", "--user", "daemon-reload"})
	}
	return u, res, nil
}

// uninstall is removeUnit as yardr <kind> uninstall says it.
func (a *app) uninstall(w io.Writer, ctx context.Context, cfg *config.Config, kind string, fileOnly bool) error {
	u, res, err := removeUnit(ctx, cfg, kind, fileOnly)
	if err != nil {
		return err
	}
	k := kindOf(kind)
	manager, name, path := u.Manager, u.Name, u.Path
	yardr := yardrAt(cfg.Home)
	if !res.Removed {
		return a.out(w, res, func(w io.Writer) {
			fmt.Fprintf(w, "No %s service is installed for the yard in %s (%s is not there). Nothing was changed.\n", what(kind), cfg.Home, path)
		})
	}
	return a.out(w, res, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s\n", path)
		switch {
		case fileOnly:
			fmt.Fprintf(w, "Not unloaded (--file-only): what %s runs of it now is left running. To unload it: %s\n", manager, shellLine(u.unloadCommand()))
		case res.Unloaded && k.Without != "":
			fmt.Fprintf(w, "%s no longer has %s and has stopped the %s it ran. %s\n", manager, name, k.What, fmt.Sprintf(k.Without, yardr))
		case res.Unloaded:
			fmt.Fprintf(w, "%s no longer has %s and has stopped the %s it ran.\n", manager, name, k.What)
		default:
			fmt.Fprintf(w, "%s did not have %s loaded; nothing was stopped.\n", manager, name)
		}
	})
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

// line is the service in words. kind is the long-running command it runs,
// and yardr the yardr to print it under: a service that is not installed
// says the command that installs it, on the same line.
func (s serviceState) line(kind, yardr string) string {
	switch {
	case !s.Supported:
		return "not supported on " + goos
	case !s.Installed:
		return fmt.Sprintf("not installed (%s %s install)", yardr, kind)
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
	return daemonState{Running: config.DaemonRunning(cfg.DaemonLock()), PID: config.DaemonPID(cfg.DaemonLock()), Service: stateOf(ctx, cfg, svcDaemon)}
}

// line is whether the daemon runs, in words.
func (d daemonState) line() string {
	if d.Running {
		return fmt.Sprintf("running (pid %d)", d.PID)
	}
	return "not running"
}

// protectedNote is what a daemon started at login cannot do in the depots
// named, and what to grant. macOS only.
func protectedNote(depots []string, program string) string {
	return fmt.Sprintf("%s in a folder macOS protects (~/Desktop, ~/Documents, ~/Downloads). A daemon started at login "+
		"does not have your terminal's access there: until yardr has its own, work in such a depot can fail with \"operation not permitted\". "+
		"Grant it in System Settings, Privacy & Security: Full Disk Access (add %s), or Files and Folders once macOS has asked. "+
		"A new build of yardr can need the grant again (docs/configuration.md, \"Login services\")",
		list(depots)+isAre(depots), program)
}

// daemonWarnings are what the daemon as a service will lack, as far as it
// shows now: a program not on the PATH written into it, and on macOS the
// depots it has no access to.
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
		crew, err := st.Crew(ctx)
		if err != nil {
			return nil, err
		}
		for _, u := range harnessUses(defaultKind(ctx, st), groups, crew) {
			needs = append(needs, harnessNeed(u.kind, "the harness of "+strings.Join(u.by, ", ")))
		}
		depots, err := st.Depots(ctx)
		if err != nil {
			return nil, err
		}
		if home, err := userHomeDir(); goos == "darwin" && err == nil {
			if in := protectedDepots(depots, home); len(in) > 0 {
				for i := range in {
					in[i] = "depot " + in[i]
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

// addrInFile finds the address a written service listens on.
var addrInFile = regexp.MustCompile(`--addr(?:</string>\s*<string>|"\s+")([^<"\s]+)`)

// webServiceAddr is the address the home's web service listens on, "" when
// there is none or its file does not say.
func webServiceAddr(cfg *config.Config) string { return serviceAddr(cfg, svcWeb) }

// serviceAddr is the same for the home's service of kind.
func serviceAddr(cfg *config.Config, kind string) string {
	_, _, path, err := unitFile(cfg, kind)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if m := addrInFile.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// webLocalAddr is where the web view listens unless told otherwise.
const webLocalAddr = store.DefaultWebAddr

// webPagesLocalAddr is where the view's sandbox origin listens unless
// told otherwise.
const webPagesLocalAddr = store.DefaultWebPagesAddr

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

// offerServices asks, once the yard runs, for the things it runs without
// and a person may want from the first day: the daemon at login, the web
// view, and a link with another yard (offerLink). All start at no. Each yes ends in the command that
// does it, shown before it runs; a command that fails says so and the
// onboarding goes on, since the yard runs either way. What the yard has
// already is not asked for again: an onboarding taken up after a run that
// got this far (resume) finds the service installed, or the view answering.
func (a *app) offerServices(w, warn io.Writer, ctx context.Context, cfg *config.Config) error {
	yardr := yardrAt(cfg.Home)
	try := func(args ...string) {
		fmt.Fprintf(w, "\n  $ %s\n\n", commandLine(cfg.Home, args))
		if err := a.sub(w, warn, ctx, args...); err != nil {
			fmt.Fprintf(w, "yardr: %v\nThe yard runs without it; `%s` tries again.\n", err, commandLine(cfg.Home, args))
		}
	}
	manager := managerOf()
	fmt.Fprintln(w)
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
		return a.offerLink(w, warn, ctx, cfg)
	}
	view := false
	if err := ask(huh.NewConfirm().Key("web").Title("Start the web view now?").
		Description(fmt.Sprintf("The yard in a browser, read-only, on this machine only: http://%s/ (docs/web.md).\n"+
			"Later: %s web start", webLocalAddr, yardr)).Value(&view)); err != nil {
		return err
	} else if !view {
		return a.offerLink(w, warn, ctx, cfg)
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
	return a.offerLink(w, warn, ctx, cfg)
}

// offerLink asks whether to link the yard with another, and opens the
// pairing form on a yes (pairForm). It is asked once a run, of the operator
// alone, since only the operator pairs, and not on a yard that has a peer.
// A pairing that fails says so and the setup goes on.
func (a *app) offerLink(w, warn io.Writer, ctx context.Context, cfg *config.Config) error {
	if a.linkAsked {
		return nil
	}
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	if ps, err := st.Peers(ctx); err != nil || len(ps) > 0 {
		return err
	}
	if _, is, err := a.operator(ctx, st); err != nil || !is {
		return err
	}
	a.linkAsked = true
	yardr, link := yardrAt(cfg.Home), false
	if err := ask(huh.NewConfirm().Key("link").Title("Link with another yard?").
		Description(fmt.Sprintf("Two linked yards send each other mail between their crew: yours and a colleague's, or\n"+
			"yours on two machines. Yes opens a short form, which the other yard's operator runs there too.\n"+
			"Later: %s peer pair", yardr)).Value(&link)); err != nil || !link {
		return err
	}
	if err := a.pairForm(w, warn, ctx, cfg, st); err != nil {
		fmt.Fprintf(w, "yardr: %v\nThe yard runs without it; `%s peer pair` tries again.\n", err, yardr)
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

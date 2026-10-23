package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/source"
)

// yardr up --login is yardr up that stays: what it starts is started again
// at every login, by the login services yardr daemon install and yardr web
// install write (service.go) and one more, for herdr's server. That one is
// not a yard's: one herdr server serves every yard of the user and the
// person's own panes, so its service has no home in its name, a second
// yard finds it, and yardr down --login removes it only with the last yard
// that starts at login. A service for herdr's server that yardr did not
// write (docs/install.md has two to save by hand) is the person's: found,
// said, and left.

// svcHerdr is the kind of the login service for herdr's server: a unit as
// the long-running commands have one, of a program that is not yardr's.
const svcHerdr = "herdr"

// loginSessionName is the component herdr and the yard's herdr_session
// setting accept. HERDR_SESSION is inherited input, so check it before it
// becomes part of a service filename.
var loginSessionName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// herdrWritten is the first comment of the service's file, by which yardr
// knows the file as its own. It holds no "--": a plist's comment takes none.
const herdrWritten = "Written by yardr for herdr's server, which serves every yard of this user: " +
	"`yardr up` with its login flag writes it, `yardr down` with the same flag removes it."

// systemServiceDirs are where a service for every user of the machine is
// read from, beside the user's own (serviceDir); tests replace it.
var systemServiceDirs = func() []string {
	if goos == "darwin" {
		return []string{"/Library/LaunchAgents", "/Library/LaunchDaemons"}
	}
	return []string{"/etc/systemd/user", "/etc/systemd/system"}
}

// errNoLogin is --login on a system yardr writes no login service for.
func errNoLogin() error {
	return fmt.Errorf("--login is not supported on %s: yardr writes a launchd agent on macOS and a systemd user unit on Linux. "+
		"docs/install.md, \"Keep it running after a reboot\", says what a service for herdr's server has to hold", goos)
}

// loginSession is the herdr session whose server a login service runs: the
// one this process's calls go to (the yard's herdr_session setting, else
// the environment's), "" for herdr's first, with what names it to herdr. A
// socket of no session herdr knows is no server a login could start.
func loginSession() (name string, env []string, err error) {
	in := runner.SessionInUse()
	switch {
	case filepath.Clean(in.Socket) == filepath.Clean(runner.SessionSocket("")):
		return "", nil, nil
	case in.Name == "":
		return "", nil, fmt.Errorf("HERDR_SOCKET_PATH names a socket of no herdr session (%s), and a login service starts the server of one", in.Socket)
	}
	return in.Name, []string{"HERDR_SESSION=" + in.Name, "HERDR_SOCKET_PATH=" + in.Socket}, nil
}

// herdrUnitFile is the manager, the name and the file of the user's service
// for the server of the herdr session, written or not.
func herdrUnitFile(session string) (manager, name, path string, err error) {
	if session != "" && (!loginSessionName.MatchString(session) || session == "." || session == "..") {
		return "", "", "", fmt.Errorf("herdr session %q: a session name has letters, digits, '.', '_' and '-'; it cannot name a login service file", session)
	}
	manager = managerOf()
	if manager == "" {
		return "", "", "", errNoLogin()
	}
	dir, err := serviceDir()
	if err != nil {
		return "", "", "", err
	}
	if manager == launchd {
		name = "dev.yardr.herdr"
		if session != "" {
			name += "." + session
		}
		return manager, name, filepath.Join(dir, name+".plist"), nil
	}
	name = "yardr-herdr"
	if session != "" {
		name += "-" + session
	}
	name += ".service"
	return manager, name, filepath.Join(dir, name), nil
}

// herdrUnit is the service for the server of the herdr at bin as this yardr
// would write it now: the files docs/install.md shows. Its PATH is this
// process's, which up --login has made the login shell's (useLoginPATH):
// the agents' panes inherit the server's environment. They inherit the
// proxy and CA variables of environ, the run's, the same way: on a box
// behind a company's proxy an agent reaches its model through them or not
// at all.
func herdrUnit(environ []string, bin string) (*unit, error) {
	session, env, err := loginSession()
	if err != nil {
		return nil, err
	}
	manager, name, path, err := herdrUnitFile(session)
	if err != nil {
		return nil, err
	}
	home, err := userHomeDir()
	if err != nil {
		return nil, err
	}
	if bin, err = filepath.Abs(bin); err != nil {
		return nil, err
	}
	env = append(env, "PATH="+os.Getenv("PATH"))
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		// Where herdr keeps its sessions and their sockets.
		env = append(env, "XDG_CONFIG_HOME="+dir)
	}
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); source.IsEgressVar(k) {
			env = append(env, kv)
		}
	}
	sort.Strings(env)
	u := &unit{Kind: svcHerdr, Manager: manager, Name: name, Path: path, Home: home, Program: bin, Args: []string{"server"}, Env: env}
	if manager == launchd {
		// launchd keeps no output; systemd's journal does.
		u.Log = filepath.Join(home, "Library", "Logs", name+".log")
	}
	return u, nil
}

// ownHerdr reports whether the file at path is a service yardr wrote for
// herdr's server.
func ownHerdr(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.Contains(b, []byte(herdrWritten[:strings.Index(herdrWritten, ",")]))
}

// herdrServerLine finds `herdr server` as a service's file runs it: two
// strings of a plist's ProgramArguments, or the words of a unit's
// ExecStart, quoted or not.
var herdrServerLine = regexp.MustCompile(`(^|[/\s">=])herdr(</string>\s*<string>|"?[ \t]+"?)server\b`)

// foreignHerdr are the service files on this machine that run the server of
// the herdr session and are not yardr's: the user's own (the hand-saved
// ones of docs/install.md, contrib/server-yard's, under any name) and the
// machine's. own is where yardr's would be; a file there that yardr did not
// write is the person's too. A service its manager knows from a file
// elsewhere, and a plist in the binary format, are not found.
func foreignHerdr(session, own string) []string {
	var out []string
	dir, err := serviceDir()
	if err != nil {
		return nil
	}
	for _, dir := range append([]string{dir}, systemServiceDirs()...) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			if ext := filepath.Ext(path); e.IsDir() || ext != ".plist" && ext != ".service" {
				continue
			}
			// A service's file is small; nothing larger is read.
			if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
				continue
			}
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if path == own {
				if !ownHerdr(path) {
					out = append(out, path)
				}
				continue
			}
			if !herdrServerLine.Match(b) {
				continue
			}
			// One that names a session is that session's server, and one
			// that names none is the first's.
			named := bytes.Contains(b, []byte("HERDR_SESSION")) || bytes.Contains(b, []byte("HERDR_SOCKET_PATH"))
			if session == "" && named || session != "" && !bytes.Contains(b, []byte(session)) {
				continue
			}
			out = append(out, path)
		}
	}
	return out
}

// workingDir finds the home in a yard's service file.
var workingDir = regexp.MustCompile(`(?m)<key>WorkingDirectory</key>\s*<string>([^<]*)</string>|^WorkingDirectory=(.*)$`)

// otherLoginYards are the yards of this user, beside cfg's, whose daemon
// starts at login: each has its daemon's service in the user's directory,
// whichever command wrote it, and a daemon that starts at login needs
// herdr's server then. They are named by their homes.
func otherLoginYards(cfg *config.Config) []string {
	manager, own, _, err := unitFile(cfg, svcDaemon)
	if err != nil {
		return nil
	}
	dir, err := serviceDir()
	if err != nil {
		return nil
	}
	pattern := "dev.yardr." + svcDaemon + ".*.plist"
	if manager == systemd {
		pattern = "yardr-" + svcDaemon + "-*.service"
	}
	paths, _ := filepath.Glob(filepath.Join(dir, pattern))
	var out []string
	for _, p := range paths {
		if strings.TrimSuffix(filepath.Base(p), ".plist") == own {
			continue
		}
		home := p
		if b, err := os.ReadFile(p); err == nil {
			if m := workingDir.FindSubmatch(b); m != nil {
				home = string(m[1]) + string(m[2])
			}
		}
		out = append(out, home)
	}
	return out
}

// useLoginPATH gives this process the PATH of the user's login shell, with
// what its own has beyond it after: what up --login writes into a service
// is found at login as the person's terminal finds it, whatever started
// this yardr (a window has no profile's PATH). Without a login shell to ask
// the PATH stays as it is.
func useLoginPATH(ctx context.Context) {
	dirs := filepath.SplitList(loginShellPATH(ctx))
	if len(dirs) == 0 {
		return
	}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	os.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

// What up --login and down --login did about a login service.
const (
	loginInstalled = "installed" // up wrote it
	loginFound     = "found"     // up found yardr's own, and wrote nothing
	loginLeft      = "left"      // not yardr's to write or to remove, or still needed
	loginRemoved   = "removed"   // down removed it
	loginAbsent    = "absent"    // down found none
)

// loginUnit is one login service in the answer of up --login and of down
// --login.
type loginUnit struct {
	Part    string `json:"part"` // herdr daemon web bdp
	Manager string `json:"manager,omitempty"`
	Name    string `json:"name,omitempty"`
	File    string `json:"file,omitempty"`
	// Result is what this run did: installed, found or left for up;
	// removed, absent or left for down. "" with Error.
	Result string `json:"result,omitempty"`
	// Loaded is whether the manager has it now, as yardr daemon status
	// says it.
	Loaded bool `json:"loaded"`
	// Foreign are the service files for herdr's server that are not
	// yardr's, for which it was left.
	Foreign  []string `json:"foreign,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
	Do       string   `json:"do,omitempty"`
}

func (l *loginUnit) of(u *unit) { l.Manager, l.Name, l.File = u.Manager, u.Name, u.Path }

// printLogin prints the login services under their own heading.
func printLogin(w io.Writer, units []loginUnit) {
	if len(units) == 0 {
		return
	}
	fmt.Fprintln(w, "\nAt login:")
	for _, l := range units {
		name := yardPart{Part: l.Part}.name()
		if l.Error != "" {
			fmt.Fprintf(w, "%-15s NOT %s\n", name, l.Error)
			if l.Do != "" {
				fmt.Fprintf(w, "%-15s   %s\n", "", l.Do)
			}
			continue
		}
		did := map[string]string{loginInstalled: "installed", loginFound: "installed already", loginLeft: "left as it is",
			loginRemoved: "removed", loginAbsent: "was not installed"}[l.Result]
		if l.File != "" {
			did += ": " + l.File
		}
		if l.Detail != "" {
			did += ": " + l.Detail
		}
		fmt.Fprintf(w, "%-15s %s\n", name, did)
		for _, warning := range l.Warnings {
			fmt.Fprintf(w, "%-15s   mind: %s\n", "", warning)
		}
	}
}

func loginFailed(units []loginUnit) bool {
	return slices.ContainsFunc(units, func(l loginUnit) bool { return l.Error != "" })
}

// herdrLoginToWrite is the service up --login has to write for the server
// of the herdr at bin, nil when it has none to write: l then says what was
// found (yardr's own, or one that is not and is left) or what kept it.
func herdrLoginToWrite(ctx context.Context, cfg *config.Config, environ []string, bin string, l *loginUnit) *unit {
	again := yardrAt(cfg.Home) + " up --login"
	u, err := herdrUnit(environ, bin)
	if err != nil {
		l.Error, l.Do = err.Error(), fmt.Sprintf("%s settings set herdr_session <name>, then: %s", yardrAt(cfg.Home), again)
		return nil
	}
	session, _, _ := loginSession()
	if foreign := foreignHerdr(session, u.Path); len(foreign) > 0 {
		l.Result, l.Foreign = loginLeft, foreign
		l.Detail = fmt.Sprintf("a service for herdr's server that is not yardr's is there (%s), and yardr writes none beside it", strings.Join(foreign, ", "))
		return nil
	}
	l.of(u)
	if ownHerdr(u.Path) {
		// Every yard of the user has the one: a second yard writes nothing.
		l.Result, l.Loaded = loginFound, u.loaded(ctx)
		if !l.Loaded && u.Manager == launchd {
			l.Detail = "launchd starts it from the next login on"
		}
		return nil
	}
	if u.Manager == systemd {
		if _, err := lookPath("systemctl"); err != nil {
			l.Error, l.Do = "systemctl is not on PATH: this system has no systemd to start herdr's server at login", "run herdr's server under what keeps a program running here (docs/install.md)"
			return nil
		}
	}
	return u
}

// installHerdrLogin writes u and has the manager start it at login. With
// start it is started now too, and that is what it returns; without, a
// server runs already and is left as it is: beside it the service's would
// end against the socket (docs/install.md).
func installHerdrLogin(ctx context.Context, u *unit, l *loginUnit, start bool) bool {
	err := os.MkdirAll(filepath.Dir(u.Path), 0o755)
	if err == nil && u.Log != "" {
		// launchd makes the file, not its directory. Without it the server
		// runs and what it prints is lost.
		_ = os.MkdirAll(filepath.Dir(u.Log), 0o755)
	}
	if err == nil {
		err = u.write()
	}
	if err != nil {
		l.Error = err.Error()
		return false
	}
	l.Result = loginInstalled
	cmds := u.loadCommands()
	if !start {
		l.Detail = "the server that runs now is left as it is; " + u.Manager + " starts herdr's server from the next login on"
		// launchd loads what is in the directory at login; systemd has to
		// be told, and starts nothing by that.
		cmds = nil
		if u.Manager == systemd {
			cmds = [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", u.Name}}
		}
	}
	for _, c := range cmds {
		if err := ctl(ctx, c); err != nil {
			l.Error, l.Do = fmt.Sprintf("%v; the service is written (%s) and not loaded", err, u.Path), shellLines(u.loadCommands())
			return false
		}
	}
	l.Loaded = len(cmds) > 0
	return start
}

// loginInstall installs the home's login service of k, as yardr <kind>
// install does, and waits until what the manager starts is up. One of the
// kind that runs is stopped first: the daemon by the install itself, any
// other here, since the service's would not get its address.
func (a *app) loginInstall(ctx context.Context, cfg *config.Config, k *longRunning, args []string, up func() bool) loginUnit {
	l := loginUnit{Part: k.Name}
	again := yardrAt(cfg.Home) + " up --login"
	u, err := newUnit(a.environ(), cfg, k, args)
	if err != nil {
		l.Error = err.Error()
		return l
	}
	l.of(u)
	if k.Warn != nil {
		if l.Warnings, err = k.Warn(a, ctx, cfg, u.Program); err != nil {
			l.Error, l.Do = err.Error(), again
			return l
		}
	}
	if k.Lock == nil {
		if _, err := k.stop(cfg, stopWait); err != nil {
			l.Error, l.Do = err.Error(), fmt.Sprintf("%s %s stop, then: %s", yardrAt(cfg.Home), k.Name, again)
			return l
		}
	}
	if _, err := u.install(ctx, cfg, false); err != nil {
		l.Error, l.Do = err.Error(), again
		return l
	}
	l.Result, l.Loaded = loginInstalled, true
	for deadline := time.Now().Add(startWait); !up(); time.Sleep(pollEvery / 5) {
		if time.Now().After(deadline) {
			l.Error = fmt.Sprintf("%s has the %s's service and the %s does not run after %s", u.Manager, k.What, k.What, startWait)
			l.Do = "read " + k.Log(cfg)
			break
		}
	}
	return l
}

// downLogin removes what up --login installs for the yard: the daemon's
// login service, that of each part whose address the yard's settings name,
// and herdr's when no other yard needs it. With all it removes every login
// service written for the yard, those yardr <kind> install wrote too: what
// yardr uninstall takes from the machine.
func downLogin(ctx context.Context, cfg *config.Config, settings map[string]string, all bool) []loginUnit {
	var out []loginUnit
	yardr := yardrAt(cfg.Home)
	for _, k := range longRunnings {
		l := loginUnit{Part: k.Name}
		if k.Name != svcDaemon && (k.Setting == "" || settings[k.Setting] == "") {
			// Not one up --login installs: yardr <kind> install did, if it
			// is there.
			if !serviceInstalled(cfg, k.Name) || k.Setting == "" && !all {
				continue
			}
		}
		if !all && k.Name != svcDaemon && settings[k.Setting] == "" {
			_, l.Name, l.File, _ = unitFile(cfg, k.Name)
			l.Result = loginLeft
			l.Detail = fmt.Sprintf("the yard's %s setting is unset, so it is not one yardr up --login installs: %s %s uninstall removes it", k.Setting, yardr, k.Name)
			out = append(out, l)
			continue
		}
		u, res, err := removeUnit(ctx, cfg, k.Name, false)
		switch {
		case err != nil:
			l.Error, l.Do = err.Error(), fmt.Sprintf("%s %s uninstall", yardr, k.Name)
		case res.Removed:
			l.of(u)
			l.Result = loginRemoved
		default:
			l.Result = loginAbsent
		}
		out = append(out, l)
	}
	return append(out, downHerdrLogin(ctx, cfg))
}

// downHerdrLogin removes yardr's login service for herdr's server, unless
// another yard of the user still starts at login and needs it. The server
// itself is never stopped, as down never stops it: the file is removed and
// the manager told not to start it again, and what runs runs on.
func downHerdrLogin(ctx context.Context, cfg *config.Config) loginUnit {
	l := loginUnit{Part: partHerdr}
	session, _, _ := loginSession()
	manager, name, path, err := herdrUnitFile(session)
	if err != nil {
		l.Error = err.Error()
		return l
	}
	if !ownHerdr(path) {
		l.Result = loginAbsent
		if foreign := foreignHerdr(session, path); len(foreign) > 0 {
			l.Result, l.Foreign = loginLeft, foreign
			l.Detail = fmt.Sprintf("the service for herdr's server is not yardr's (%s)", strings.Join(foreign, ", "))
		}
		return l
	}
	u := &unit{Kind: svcHerdr, Manager: manager, Name: name, Path: path}
	l.of(u)
	if others := otherLoginYards(cfg); len(others) > 0 {
		l.Result, l.Loaded = loginLeft, u.loaded(ctx)
		l.Detail = fmt.Sprintf("still needed: %s at login too (%s)", map[bool]string{true: "another yard starts", false: "other yards start"}[len(others) == 1],
			strings.Join(others, ", "))
		return l
	}
	if manager == systemd {
		// Without --now: the server and its panes run on.
		if err := ctl(ctx, []string{"systemctl", "--user", "disable", name}); err != nil && u.loaded(ctx) {
			l.Error, l.Do = err.Error(), "systemctl --user disable "+name
			return l
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		l.Error = err.Error()
		return l
	}
	if manager == systemd {
		_ = ctl(ctx, []string{"systemctl", "--user", "daemon-reload"})
	}
	l.Result = loginRemoved
	l.Detail = "herdr's server is left running, and no longer starts at login"
	return l
}

// herdrLogin is the login service for herdr's server as yardr status and
// yardr doctor report it: yardr's own, or the files of one that is not.
type herdrLogin struct {
	serviceState
	Foreign []string `json:"foreign,omitempty"`
}

// herdrLoginState reads it. ask is whether the manager is asked if it has
// yardr's loaded; doctor goes by the files alone, as for the daemon's.
func herdrLoginState(ctx context.Context, ask bool) herdrLogin {
	session, _, _ := loginSession()
	manager, name, path, err := herdrUnitFile(session)
	if err != nil {
		return herdrLogin{}
	}
	s := herdrLogin{serviceState: serviceState{Supported: true, Manager: manager, Name: name, File: path, Installed: ownHerdr(path)}}
	switch {
	case !s.Installed:
		s.Foreign = foreignHerdr(session, path)
	case ask:
		s.Loaded = (&unit{Manager: manager, Name: name}).loaded(ctx)
	}
	return s
}

// line is the service in words, as yardr status prints it; yardr is the
// yardr to print the installing command under.
func (s herdrLogin) line(yardr string) string {
	switch {
	case !s.Supported:
		return "not supported on " + goos
	case len(s.Foreign) > 0:
		return fmt.Sprintf("one of your own, not yardr's (%s)", strings.Join(s.Foreign, ", "))
	case !s.Installed:
		return fmt.Sprintf("not installed (%s up --login)", yardr)
	case s.Loaded:
		return fmt.Sprintf("installed and loaded: %s starts herdr's server at login (%s)", s.Manager, s.File)
	case s.Manager == launchd:
		return fmt.Sprintf("installed: launchd starts herdr's server from the next login on (%s)", s.File)
	}
	return fmt.Sprintf("installed, not enabled in %s now (%s)", s.Manager, s.File)
}

// herdrLoginPart is the login service for herdr's server as doctor lists
// it under Optional: configured with yardr's own (yardr up --login) or with
// one that is not yardr's, which yardr finds by what the file runs.
// `yardr daemon install` and `yardr setup` print the same line.
func herdrLoginPart(cfg *config.Config) optionalPart {
	p := optionalPart{Key: "herdr_login_service", Title: "login service for herdr's server"}
	yardr := yardrAt(cfg.Home)
	const docs = `docs/install.md, "Keep it running after a reboot"`
	switch s := herdrLoginState(context.Background(), false); {
	case !s.Supported:
		p.Detail = fmt.Sprintf("yardr writes no login service on %s; %s, says what one for herdr's server has to hold", goos, docs)
	case len(s.Foreign) > 0:
		p.Configured = true
		p.Detail = fmt.Sprintf("a service that is not yardr's starts herdr's server (%s); yardr leaves it as it is", strings.Join(s.Foreign, ", "))
	case s.Installed:
		p.Configured = true
		p.Detail = fmt.Sprintf("herdr's server starts at login (%s); %s down --login removes it with the last yard that starts at login", s.File, yardr)
	default:
		p.Detail = fmt.Sprintf("herdr's server does not start at login, and after a reboot no agent starts until it runs; "+
			"%s up --login installs its login service and the daemon's (%s)", yardr, docs)
	}
	return p
}

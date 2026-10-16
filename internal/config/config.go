// Package config resolves where yardr keeps its state. --home, then
// YARDR_HOME, override the default ~/.yardr so tests and agents can use
// their own instance.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Only this file knows the prefix of yardr's variables.
const prefix = "YARDR_"

// Getenv returns YARDR_<name>.
func Getenv(name string) string { return os.Getenv(prefix + name) }

// Env returns name=value as the KEY=value of a child's environment.
func Env(name, value string) []string { return []string{prefix + name + "=" + value} }

// IsVar reports whether key is one of the named yardr variables
// (IsVar("YARDR_BEAD", "BEAD", "SESSION")); with no names, any of them.
func IsVar(key string, names ...string) bool {
	name, ok := strings.CutPrefix(key, prefix)
	if !ok {
		return false
	}
	return len(names) == 0 || slices.ContainsFunc(names, func(n string) bool {
		return n == name
	})
}

// ServerSettings are the yardr variables the yard's servers read where
// they start: the addresses the webhook receiver, web view, the view's
// sandbox origin and BDP server take, the sandbox origin's public name,
// and the user the web view acts as; how long the server keeps events
// and webhook bodies; and where it serves its profiles when measured
// (PPROF, internal/server). They are the yard's, set by whoever starts a
// server by hand, and never the work's (see WithoutServerSettings).
var ServerSettings = []string{"HOOK_ADDR", "WEB_ADDR", "WEB_PAGES_ADDR", "WEB_PAGES_HOST", "WEB_AS", "BDP_ADDR", "EVENT_RETENTION", "HOOK_PAYLOAD_RETENTION", "PPROF"}

// WithoutServerSettings is environ without the ServerSettings: the
// environment of what a yard starts for work (a
// session, a script). A server started from there, by a test or an agent's
// trial, would otherwise listen on this yard's webhook, web or BDP address,
// and on macOS a listener on 127.0.0.1 takes the loopback deliveries of
// one on all addresses.
func WithoutServerSettings(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return IsVar(k, ServerSettings...)
	})
}

// BlankServerSettings is the ServerSettings set to nothing, which reads as
// unset (see Getenv), for an environment yardr can only add to: a herdr
// pane starts with herdr's own, and that may be the shell's the yard's
// server was started from.
func BlankServerSettings() []string {
	var env []string
	for _, name := range ServerSettings {
		env = append(env, Env(name, "")...)
	}
	return env
}

// Unsetenv removes YARDR_<name>, so children do not inherit a value meant
// only for this process.
func Unsetenv(name string) { os.Unsetenv(prefix + name) }

type Config struct {
	Home string // state directory: yard.db, yard.sock, logs, worktrees
	// HookAddr is YARDR_HOOK_ADDR, the TCP address the server receives
	// webhooks on (e.g. 127.0.0.1:8790) whatever the yard's hook_addr
	// setting says; empty leaves it to the setting.
	HookAddr string
	// WebAddr is YARDR_WEB_ADDR, and BDPAddr is YARDR_BDP_ADDR; each
	// overrides its service's setting when nonempty.
	WebAddr string
	BDPAddr string
	// WebPagesAddr is YARDR_WEB_PAGES_ADDR, the address the web view's
	// sandbox origin listens on whatever web_pages_addr says, and
	// WebPagesHost is YARDR_WEB_PAGES_HOST, the public name run links
	// use for it whatever web_pages_host says; empty leaves each to
	// its setting.
	WebPagesAddr string
	WebPagesHost string
}

// UseHome makes dir the home of this process and of every process it
// starts: the --home flag. It is made absolute here and put in the
// environment, in place of what YARDR_HOME said, so Load
// finds it and a server, daemon, agent or script started from here is in the
// same yard without each start having to pass it on.
func UseHome(dir string) error {
	if dir == "" {
		return errors.New("--home needs a directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, kv := range Env("HOME", abs) {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

// MaxSocket bounds the socket path: a unix socket's path must fit in
// sun_path, 108 bytes on Linux and 104 on macOS, so with room to spare.
const MaxSocket = 100

// Load resolves the home (--home by way of UseHome, else YARDR_HOME, else
// DefaultHome), creating it on first use.
// A home whose socket path would not fit a unix socket is refused before
// anything is created in it. The home is made absolute here, once: every
// process this one starts (the server, agents, scripts) gets it in its
// environment and runs somewhere else, where a relative YARDR_HOME=y1
// would name a different directory, or none, and exit 2 on the first
// command.
func Load() (*Config, error) {
	c, err := LoadReadOnly()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.Home, 0o700); err != nil {
		return nil, err
	}
	return c, nil
}

// LoadReadOnly resolves the home as Load does but creates nothing: for
// commands that must leave a home with no yard as they found it (doctor,
// which says that it creates nothing, status, which says that it is
// read-only, and server status, which only asks whether the server
// answers). A home whose socket path would not fit a unix socket is
// refused, as by Load.
func LoadReadOnly() (*Config, error) {
	home := Getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = DefaultHome(h)
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	c := &Config{Home: home, HookAddr: Getenv("HOOK_ADDR"), WebAddr: Getenv("WEB_ADDR"), BDPAddr: Getenv("BDP_ADDR"),
		WebPagesAddr: Getenv("WEB_PAGES_ADDR"), WebPagesHost: Getenv("WEB_PAGES_HOST")}
	if n := len(c.Socket()); n > MaxSocket {
		return nil, fmt.Errorf("yardr home %s is too long: its socket %s would be %d bytes, and a unix socket path "+
			"allows %d; give --home (or YARDR_HOME) a shorter directory, such as /tmp/y1", home, c.Socket(), n, MaxSocket)
	}
	return c, nil
}

// DefaultHome is <userHome>/.yardr.
func DefaultHome(userHome string) string { return filepath.Join(userHome, ".yardr") }

// IsDefaultHome reports whether home is the one yardr uses when neither
// --home nor YARDR_HOME is given, so a command for it needs no flag. Paths are compared
// resolved: YARDR_HOME=~/.yardr/ or a symlink to it is the default too.
func IsDefaultHome(home string) bool {
	h, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return resolve(home) == resolve(DefaultHome(h))
}

// resolve is p made absolute with its symlinks followed, or as clean as it
// gets where it does not exist.
func resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func (c *Config) DBPath() string  { return filepath.Join(c.Home, "yard.db") }
func (c *Config) Socket() string  { return filepath.Join(c.Home, "yard.sock") }
func (c *Config) PIDFile() string { return filepath.Join(c.Home, "server.pid") }

// PIDStart is the file beside the pidfile holding the start time of the
// process it names, by which a stop tells the server from a later process
// with its pid (see server.Stop). Compared for equality only, never read
// as a time.
func (c *Config) PIDStart() string { return c.PIDFile() + ".start" }
func (c *Config) LogFile() string  { return filepath.Join(c.Home, "server.log") }

// HookState is where the server says what its webhook receiver listens on
// (see server.HookListener).
func (c *Config) HookState() string { return filepath.Join(c.Home, "hooks.state") }

// PeerKey is the file holding the yard's private key, by which other yards
// know it (see internal/yardkey). It is read as a secret file is: mode 0600.
func (c *Config) PeerKey() string { return filepath.Join(c.Home, "peer.key") }

// DaemonLock is the file the daemon holds a flock on while it runs. It
// names the holder's pid and the start time it recorded (see DaemonStart).
func (c *Config) DaemonLock() string { return filepath.Join(c.Home, "daemon.lock") }

// DaemonRunning reports whether a process holds an exclusive flock on lock,
// the daemon on its DaemonLock or a long-running command on its record,
// without taking it or creating the file.
func DaemonRunning(lock string) bool {
	f, err := os.Open(lock)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// DaemonPID is the pid of the process that holds lock, 0 when none does.
func DaemonPID(lock string) int {
	if !DaemonRunning(lock) {
		return 0
	}
	b, err := os.ReadFile(lock)
	if err != nil {
		return 0
	}
	var pid int
	fmt.Sscan(string(b), &pid)
	return pid
}

// DaemonStart is the start time the process holding lock recorded beside
// its pid, by which a stop verifies the pid before signalling it. It is
// false when no process holds the lock, or the holder recorded no start
// time: a daemon from before starts were recorded, or one the OS would
// not describe.
func DaemonStart(lock string) (uint64, bool) {
	if !DaemonRunning(lock) {
		return 0, false
	}
	b, err := os.ReadFile(lock)
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, false
	}
	start, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return start, true
}

// DaemonPass is the file the daemon stamps with its build and the time at
// the end of every completed pass (see PassStamp).
func (c *Config) DaemonPass() string { return filepath.Join(c.Home, "daemon.pass") }

// UpgradeGate is the file an upgrade writes while it checks the build it
// installed (see UpgradeGate). While it names a live process the daemon's
// passes claim nothing and start no agent.
func (c *Config) UpgradeGate() string { return filepath.Join(c.Home, "upgrade.gate") }

// UpgradeFailed is the file a failed upgrade leaves when the yard is down
// (see UpgradeFailed), until the next successful upgrade clears it.
func (c *Config) UpgradeFailed() string { return filepath.Join(c.Home, "upgrade.failed") }

// BeforeInstallBackup is the pre-install copy an upgrade takes of the
// yard's store: in backups, where the daemon's pruning never touches it,
// so a rollback never depends on backup_dir.
func (c *Config) BeforeInstallBackup() string {
	return filepath.Join(c.Home, "backups", "yard.db.before-install")
}

// DaemonLog is where a daemon started in the background (the onboarding,
// yardr init --start) writes what one in a pane of its own prints there.
func (c *Config) DaemonLog() string { return filepath.Join(c.Home, "daemon.log") }

// At is the home in dir for its paths alone, for the code that is handed a
// home by its directory (a runner, a pack's plan) and loads no Config.
func At(dir string) *Config { return &Config{Home: dir} }

// WebLog, BDPLog and OTelLog are where yardr web, yardr bdp and yardr otel
// log when no terminal runs them (start, install), as DaemonLog is the
// daemon's.
func (c *Config) WebLog() string  { return filepath.Join(c.Home, "web.log") }
func (c *Config) BDPLog() string  { return filepath.Join(c.Home, "bdp.log") }
func (c *Config) OTelLog() string { return filepath.Join(c.Home, "otel.log") }

// WebViews, BDPServers and OTelExports are the directories in which the
// home's running web views, BDP servers and telemetry exports each hold a
// record of their own while they run, as the daemon holds DaemonLock.
func (c *Config) WebViews() string    { return filepath.Join(c.Home, "webviews") }
func (c *Config) BDPServers() string  { return filepath.Join(c.Home, "bdpservers") }
func (c *Config) OTelExports() string { return filepath.Join(c.Home, "otelexports") }

// OTelState is where yardr otel export keeps what it has sent.
func (c *Config) OTelState() string { return filepath.Join(c.Home, "otel-export.json") }

// Logs is where the yard keeps output from exec groups that have no log of
// their own.
func (c *Config) Logs() string { return filepath.Join(c.Home, "logs") }

// Packs is where the yard keeps its copies of the packs it imported, Pack
// the copy of one and PackRoles the role prompts in it.
func (c *Config) Packs() string                { return filepath.Join(c.Home, "packs") }
func (c *Config) Pack(name string) string      { return filepath.Join(c.Packs(), name) }
func (c *Config) PackRoles(name string) string { return filepath.Join(c.Pack(name), "roles") }

// Roles is where a yard from before the packs kept its role prompts.
func (c *Config) Roles() string { return filepath.Join(c.Home, "roles") }

// Flows is where the home keeps the stage files of flows that no depot's
// repository holds, a directory for each flow.
func (c *Config) Flows() string { return filepath.Join(c.Home, "flows") }

// Worktrees is where sessions get their worktrees, unless a depot says
// otherwise.
func (c *Config) Worktrees() string { return filepath.Join(c.Home, "worktrees") }

// currentUser is user.Current as a variable, so a test can be a machine
// whose user lookup blocks (yardr-tbm4).
var currentUser = user.Current

// UserLookupTimeout bounds the OS user lookup in LookupUsername, and so
// in Actor and the login name. Under Codex's sandbox getpwuid can hang
// (in notify_register_check), past signals, so an unbounded lookup hangs
// the command past killing. A variable so the tests fail in milliseconds:
// this package's, and internal/cli's, which cannot see an unexported one
// (yardr-tbm4, yardr-8ms7).
var UserLookupTimeout = 2 * time.Second

// errUserLookupTimeout is the LookupUsername error when the lookup does not
// answer in time. Callers fall through to their next name; Actor, which has
// none, fails instead, with how to name one.
var errUserLookupTimeout = errors.New("the system's user lookup did not answer")

// LookupUsername is the username lookup answers with, bounded by
// UserLookupTimeout: errUserLookupTimeout when it does not answer in time,
// the lookup's error when it answers with none. The one bounded lookup,
// shared by Actor and the login name (yardr-tbm4, yardr-8ms7).
func LookupUsername(lookup func() (*user.User, error)) (string, error) {
	type lookedUp struct {
		name string
		err  error
	}
	found := make(chan lookedUp, 1)
	go func() {
		u, err := lookup()
		if err != nil {
			found <- lookedUp{err: err}
			return
		}
		found <- lookedUp{name: u.Username}
	}()
	select {
	case r := <-found:
		if r.err != nil {
			return "", r.err
		}
		return r.name, nil
	case <-time.After(UserLookupTimeout):
		// The lookup stays stuck where it is; the buffered send lets it
		// go, and the goroutine with it, if it ever answers.
		return "", errUserLookupTimeout
	}
}

// Actor names who is acting: YARDR_ACTOR, else the login name ($USER,
// then the OS user's), else "unknown". A named actor never reaches the
// OS: the lookup runs only when neither names one. It fails when the
// lookup does not answer in time, with how to name one, instead of
// hanging; a lookup that answers with no user is "unknown", as before.
func Actor() (string, error) {
	if a := Getenv("ACTOR"); a != "" {
		return a, nil
	}
	if u := os.Getenv("USER"); u != "" {
		return u, nil
	}
	name, err := LookupUsername(currentUser)
	if errors.Is(err, errUserLookupTimeout) {
		return "", errors.New("no actor: set YARDR_ACTOR or USER; the system's user lookup did not answer")
	}
	if err != nil {
		return "unknown", nil
	}
	return name, nil
}

// ParseRetention reads how long something is kept: a number of days
// ("90d"), a Go duration ("720h"), or "off" or "0" to keep it for good (0).
func ParseRetention(v string) (time.Duration, error) {
	if v == "off" || v == "0" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("retention %q: use a number of days (90d), a duration (720h) or off", v)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("retention %q: use a number of days (90d), a duration (720h) or off", v)
	}
	return d, nil
}

// Retention reads YARDR_<name> with ParseRetention, def when unset.
func Retention(name string, def time.Duration) (time.Duration, error) {
	v := Getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := ParseRetention(v)
	if err != nil {
		return 0, fmt.Errorf("%s%s: %w", prefix, name, err)
	}
	return d, nil
}

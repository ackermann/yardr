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

// ServerSettings are the yardr variables the yard's server reads where it
// starts: the address it takes webhooks on and how long it keeps events and
// webhook bodies, and where it serves its profiles when it is measured
// (PPROF, internal/server). They are the yard's, set by whoever starts its
// server by hand, and never the work's (see WithoutServerSettings).
var ServerSettings = []string{"HOOK_ADDR", "EVENT_RETENTION", "HOOK_PAYLOAD_RETENTION", "PPROF"}

// WithoutServerSettings is environ without the ServerSettings: the
// environment of what a yard starts for work (a
// session, a script). A server started from there, by a test or an agent's
// trial, would otherwise listen on this yard's webhook address,
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
	c := &Config{Home: home, HookAddr: Getenv("HOOK_ADDR")}
	if n := len(c.Socket()); n > MaxSocket {
		return nil, fmt.Errorf("yardr home %s is too long: its socket %s would be %d bytes, and a unix socket path "+
			"allows %d; give --home (or YARDR_HOME) a shorter directory, such as /tmp/y1", home, c.Socket(), n, MaxSocket)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
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
func (c *Config) LogFile() string { return filepath.Join(c.Home, "server.log") }

// HookState is where the server says what its webhook receiver listens on
// (see server.HookListener).
func (c *Config) HookState() string { return filepath.Join(c.Home, "hooks.state") }

// PeerKey is the file holding the yard's private key, by which other yards
// know it (see internal/yardkey). It is read as a secret file is: mode 0600.
func (c *Config) PeerKey() string { return filepath.Join(c.Home, "peer.key") }

// DaemonLock is the file the daemon holds a flock on while it runs.
func (c *Config) DaemonLock() string { return filepath.Join(c.Home, "daemon.lock") }

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

// Actor names who is acting: YARDR_ACTOR, else the login name ($USER,
// then the OS user's), else "unknown".
func Actor() string {
	if a := Getenv("ACTOR"); a != "" {
		return a
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "unknown"
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

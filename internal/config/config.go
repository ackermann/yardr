// Package config resolves where yardr keeps its state. YARDR_HOME overrides
// the default ~/.yardr so tests and agents can use their own instance.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The tool was called yard before it became yardr. For one release its
// environment is still read under the old names, and set under both for the
// processes it starts, so scripts and running agents written against yard
// keep working. Only this file knows the old prefix.
const (
	prefix    = "YARDR_"
	oldPrefix = "YARD_"
)

// Getenv returns YARDR_<name>, else the old YARD_<name>.
func Getenv(name string) string {
	if v := os.Getenv(prefix + name); v != "" {
		return v
	}
	return os.Getenv(oldPrefix + name)
}

// Env returns name=value as KEY=value pairs for a child's environment,
// under the new name and the old one.
func Env(name, value string) []string {
	return []string{prefix + name + "=" + value, oldPrefix + name + "=" + value}
}

// IsVar reports whether key is one of the named yardr variables, under
// the new prefix or the old (IsVar("YARD_BEAD", "BEAD", "SESSION")); with
// no names, any of them.
func IsVar(key string, names ...string) bool {
	name, ok := strings.CutPrefix(key, prefix)
	if !ok {
		if name, ok = strings.CutPrefix(key, oldPrefix); !ok {
			return false
		}
	}
	return len(names) == 0 || slices.Contains(names, name)
}

// Unsetenv removes YARDR_<name> and the old YARD_<name>, so children do not
// inherit a value meant only for this process.
func Unsetenv(name string) {
	os.Unsetenv(prefix + name)
	os.Unsetenv(oldPrefix + name)
}

type Config struct {
	Home string // state directory: yard.db, yard.sock, logs, worktrees
	// HookAddr is the TCP address the server receives webhooks on
	// (YARDR_HOOK_ADDR, e.g. 127.0.0.1:8790); empty means no receiver.
	HookAddr string
}

// MaxSocket bounds the socket path: a unix socket's path must fit in
// sun_path, 108 bytes on Linux and 104 on macOS, so with room to spare.
const MaxSocket = 100

// Load resolves YARDR_HOME (default DefaultHome), creating it on first use.
// A home whose socket path would not fit a unix socket is refused before
// anything is created in it. The home is made absolute here, once: every
// process this one starts (the server, agents, the gate) gets it in its
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
			"allows %d; set YARDR_HOME to a shorter directory, such as /tmp/y1", home, c.Socket(), n, MaxSocket)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	return c, nil
}

// DefaultHome is <userHome>/.yardr, unless only the old <userHome>/.yard
// exists: then that, so an existing yard keeps its store, worktrees and the
// herdr panes pointing into it. It is never moved automatically; a server
// running there and agents with paths into it would break.
func DefaultHome(userHome string) string {
	home := filepath.Join(userHome, ".yardr")
	if _, err := os.Stat(home); errors.Is(err, fs.ErrNotExist) {
		if st, err := os.Stat(LegacyHome(userHome)); err == nil && st.IsDir() {
			return LegacyHome(userHome)
		}
	}
	return home
}

// IsDefaultHome reports whether home is the one yardr uses when YARDR_HOME
// is not set, so a command for it needs no variable. Paths are compared
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

// LegacyHome is where yard kept its state before the rename.
func LegacyHome(userHome string) string { return filepath.Join(userHome, ".yard") }

// IsLegacyHome reports whether home is the old ~/.yard, which can be moved
// to ~/.yardr once nothing runs from it.
func (c *Config) IsLegacyHome() bool {
	h, err := os.UserHomeDir()
	return err == nil && filepath.Clean(c.Home) == LegacyHome(h)
}

// The file names inside the home stay as they were, so moving ~/.yard to
// ~/.yardr is a plain mv.
func (c *Config) DBPath() string  { return filepath.Join(c.Home, "yard.db") }
func (c *Config) Socket() string  { return filepath.Join(c.Home, "yard.sock") }
func (c *Config) PIDFile() string { return filepath.Join(c.Home, "server.pid") }
func (c *Config) LogFile() string { return filepath.Join(c.Home, "server.log") }

// DaemonLock is the file the daemon holds a flock on while it runs.
func (c *Config) DaemonLock() string { return filepath.Join(c.Home, "daemon.lock") }

// DaemonLog is where a daemon started in the background (the onboarding,
// yardr init --start) writes what one in a pane of its own prints there.
func (c *Config) DaemonLog() string { return filepath.Join(c.Home, "daemon.log") }

// Actor names who is acting: YARDR_ACTOR, else the OS user.
func Actor() string {
	if a := Getenv("ACTOR"); a != "" {
		return a
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

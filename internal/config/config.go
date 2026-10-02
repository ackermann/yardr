// Package config resolves where yardr keeps its state. YARDR_HOME overrides
// the default ~/.yardr so tests and agents can use their own instance.
package config

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
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

// Load resolves YARDR_HOME (default DefaultHome), creating it on first use.
func Load() (*Config, error) {
	home := Getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = DefaultHome(h)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	return &Config{Home: home, HookAddr: Getenv("HOOK_ADDR")}, nil
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

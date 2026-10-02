// Package config resolves where yard keeps its state. YARD_HOME overrides
// the default ~/.yard so tests and agents can use their own instance.
package config

import (
	"os"
	"os/user"
	"path/filepath"
)

type Config struct {
	Home string // state directory: yard.db, yard.sock, logs, worktrees
	// HookAddr is the TCP address the server receives webhooks on
	// (YARD_HOOK_ADDR, e.g. 127.0.0.1:8790); empty means no receiver.
	HookAddr string
}

// Load resolves YARD_HOME (default ~/.yard), creating it on first use.
func Load() (*Config, error) {
	home := os.Getenv("YARD_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(h, ".yard")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	return &Config{Home: home, HookAddr: os.Getenv("YARD_HOOK_ADDR")}, nil
}

func (c *Config) DBPath() string  { return filepath.Join(c.Home, "yard.db") }
func (c *Config) Socket() string  { return filepath.Join(c.Home, "yard.sock") }
func (c *Config) PIDFile() string { return filepath.Join(c.Home, "server.pid") }
func (c *Config) LogFile() string { return filepath.Join(c.Home, "server.log") }

// Actor names who is acting: YARD_ACTOR, else the OS user.
func Actor() string {
	if a := os.Getenv("YARD_ACTOR"); a != "" {
		return a
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "unknown"
}

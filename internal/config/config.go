// Package config resolves where yard keeps its state and how it reaches the
// store. Everything is overridable by environment so tests and agents can
// point at their own instance.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

const (
	DefaultAddr = "127.0.0.1:19875"
	Namespace   = "yard"
	Database    = "yard"
)

type Config struct {
	Home string // state directory: data/, credentials, logs, prompts
	Addr string // host:port of the SurrealDB server
	User string
	Pass string
}

// Load reads YARD_HOME (default ~/.yard), YARD_ADDR and the credentials file,
// creating the home directory and a random root password on first use.
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
	addr := os.Getenv("YARD_ADDR")
	if addr == "" {
		addr = DefaultAddr
	}
	c := &Config{Home: home, Addr: addr}
	if err := c.loadCredentials(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Endpoint() string  { return "ws://" + c.Addr }
func (c *Config) HealthURL() string { return "http://" + c.Addr + "/health" }
func (c *Config) DataDir() string   { return filepath.Join(c.Home, "data") }
func (c *Config) PIDFile() string   { return filepath.Join(c.Home, "surreal.pid") }
func (c *Config) LogFile() string   { return filepath.Join(c.Home, "surreal.log") }

// IsLocal reports whether the server is one yard may start itself.
func (c *Config) IsLocal() bool {
	return strings.HasPrefix(c.Addr, "127.0.0.1:") || strings.HasPrefix(c.Addr, "localhost:")
}

func (c *Config) loadCredentials() error {
	if u, p := os.Getenv("YARD_USER"), os.Getenv("YARD_PASS"); u != "" && p != "" {
		c.User, c.Pass = u, p
		return nil
	}
	path := filepath.Join(c.Home, "credentials")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		buf := make([]byte, 18)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		b = []byte("root:" + hex.EncodeToString(buf) + "\n")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	user, pass, ok := strings.Cut(strings.TrimSpace(string(b)), ":")
	if !ok {
		return fmt.Errorf("%s: want user:password", path)
	}
	c.User, c.Pass = user, pass
	return nil
}

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

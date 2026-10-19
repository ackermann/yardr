package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/config"
)

// processFacts are the variables a run's environment never carries: what
// packages below the CLI read from the process itself (the user's home,
// PATH, TMPDIR, herdr's session), or what a command sets on the process
// for those readers (up's merged PATH). A copy in the run's map would
// disagree with what they read; environ adds them as the process holds
// them when a child is started. Declared credentials are read from the
// process too, but their names are open-ended: the map carries the ones
// the process had, which is what the process still holds.
var processFacts = []string{"HOME", "PATH", "TMPDIR", "HERDR_SOCKET_PATH", "HERDR_SESSION", "XDG_CONFIG_HOME", "XDG_STATE_HOME"}

// environMap is environ (KEY=VALUE) as a run's environment, without the
// process facts.
func environMap(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || slices.Contains(processFacts, k) {
			continue
		}
		env[k] = v
	}
	return env
}

// getenv is the run's value of key, a full key ("YARDR_ACTOR", "USER").
// A run with no environment of its own (a test's app, a direct subcommand
// call) reads the process's; one with an environment reads it alone, and a
// key it lacks is unset, not the process's.
func (a *app) getenv(key string) string {
	if a.env == nil {
		return os.Getenv(key)
	}
	return a.env[key]
}

// yardrenv is the run's YARDR_<name>, as config.Getenv is the process's.
func (a *app) yardrenv(name string) string { return a.getenv(config.Key(name)) }

// setenv sets key in the run's environment, and so in that of every child
// it starts. A run with none of its own takes the process's first, so
// that the rest of it is not lost.
func (a *app) setenv(key, value string) {
	if a.env == nil {
		a.env = environMap(os.Environ())
	}
	a.env[key] = value
}

// useHome makes dir the home of this run and of every child it starts:
// the --home flag. It is made absolute here, against the process's
// directory, because the children run elsewhere; the process's own
// environment is left alone (config.UseHome is the process's form).
func (a *app) useHome(dir string) error {
	if dir == "" {
		return errors.New("--home needs a directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	a.setenv(config.Key("HOME"), abs)
	return nil
}

// environ is the environment a child of this run starts with: the run's
// own, and the process facts as the process holds them now, not as they
// were when the run began, so up's PATH, merged with the login shell's
// before it starts anything, reaches what it starts.
func (a *app) environ() []string {
	if a.env == nil {
		return os.Environ()
	}
	keys := make([]string, 0, len(a.env))
	for k := range a.env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	env := make([]string, 0, len(keys)+len(processFacts))
	for _, k := range keys {
		env = append(env, k+"="+a.env[k])
	}
	for _, k := range processFacts {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// config is the yard's home as this run knows it, from the run's
// environment: resolved, and the home created, once. A test may hand one
// in.
func (a *app) config() (*config.Config, error) {
	if a.cfg == nil {
		cfg, err := config.LoadWith(a.yardrenv)
		if err != nil {
			return nil, err
		}
		a.cfg = cfg
	}
	return a.cfg, nil
}

// configReadOnly is the yard's home as config resolves it, creating
// nothing, for the commands that must leave a home with no yard as they
// found it (see config.LoadReadOnly).
func (a *app) configReadOnly() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	return config.LoadReadOnlyWith(a.yardrenv)
}

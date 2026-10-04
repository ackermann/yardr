package cli

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
)

func TestMain(m *testing.M) {
	// A builder's pane and a refinery script run the gate with their own
	// session's identity in the environment. Commands read it (a move from
	// YARDR_SESSION expects that session to own the bead), so it must not
	// leak into these tests; a test that wants it sets it.
	for _, name := range []string{"SESSION", "BEAD", "RESIDENT", "ACTOR"} {
		config.Unsetenv(name)
	}
	// No test starts a real daemon or talks to a real herdr: the test
	// binary is not yardr, and the herdr on PATH is somebody's own. A test
	// of the start replaces these (stubStart).
	spawnDaemon = func(*config.Config) (<-chan error, error) {
		return nil, errors.New("tests start no daemon")
	}
	paneTail = func(context.Context, map[string]any, int) (string, error) {
		return "", errors.New("tests read no pane")
	}
	agentStatus = func(context.Context, map[string]any) (string, error) {
		return "", errors.New("tests ask no herdr")
	}
	focusAgent = func(context.Context, string) error { return errors.New("tests focus no pane") }
	listModels = func(context.Context, string) ([]string, error) { return nil, errors.New("tests run no agent") }
	// Nor does a test load a service into this machine's launchd or systemd,
	// write into its LaunchAgents, start a web view, dial its port or signal a
	// process: a
	// test of the services replaces these (stubServices).
	services, err := os.MkdirTemp("", "ysvc")
	if err != nil {
		panic(err)
	}
	serviceDir = func() (string, error) { return services, nil }
	serviceCtl = func(context.Context, string, ...string) (string, error) {
		return "", errors.New("tests ask no service manager")
	}
	signalDaemon = func(int) error { return errors.New("tests signal no process") }
	spawnWeb = func(*config.Config, string) (int, <-chan error, func() error, error) {
		return 0, nil, nil, errors.New("tests start no web view")
	}
	answers = func(string) bool { return false }
	pollEvery = 5 * time.Millisecond
	code := m.Run()
	os.RemoveAll(services)
	os.Exit(code)
}

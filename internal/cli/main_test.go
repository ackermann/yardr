package cli

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// askHerdrServer is herdrServer as yardr has it, for a test that gives it a
// stand-in herdr to ask.
var askHerdrServer func(context.Context, string) serverCheck

// realTry is tryHarnessCall as yardr has it, for a test that gives it a
// stand-in harness to call.
var realTry func(context.Context, string, string) tryCheck

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
	attachAgent = func(context.Context, string) error { return errors.New("tests attach to no pane") }
	// Nor does doctor ask that herdr for its server or run this machine's
	// login shell: a test of either replaces these.
	askHerdrServer = herdrServer
	herdrServer = func(context.Context, string) serverCheck { return serverCheck{State: herdrUp} }
	loginShell = func() string { return "" }
	// Nor does a test call a harness that is somebody's own, and costs
	// them a model call: a test of the call puts it back (realTry).
	realTry = tryHarnessCall
	tryHarnessCall = func(context.Context, string, string) tryCheck {
		return tryCheck{State: tryNotTried, Output: "tests call no harness"}
	}
	// Nor does a test take over the terminal with the chat's view: a test
	// of the hand-over replaces this (stubChat).
	runChat = func(context.Context, store.API, *chat.Model, uint64) error { return nil }
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

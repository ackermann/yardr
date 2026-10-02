package cli

import (
	"os"
	"testing"

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
	os.Exit(m.Run())
}

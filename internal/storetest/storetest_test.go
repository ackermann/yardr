package storetest

import (
	"os"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
)

// NameTestBinary says what the server and the daemon read of it: this
// process as the owner, running, and a tick they take. They take a malformed
// owner for a warning and no more, so a change to either side of the format
// would leave every server of the binary ownerless with no test red but this.
func TestNameTestBinary(t *testing.T) {
	// Serial: sets the process's environment, and puts it back.
	for _, name := range []string{config.TestOwnerEnv, config.TestHomeCheckEnv} {
		old, had := os.LookupEnv(config.Key(name))
		t.Cleanup(func() {
			if had {
				os.Setenv(config.Key(name), old)
			} else {
				os.Unsetenv(config.Key(name))
			}
		})
	}
	NameTestBinary()
	pid, start, ok := config.TestOwner()
	if !ok || pid != os.Getpid() || !peer.Alive(pid, start) {
		t.Errorf("TestOwner() = %d, %d, %v after NameTestBinary: want this process (pid %d), running", pid, start, ok, os.Getpid())
	}
	if every, ok := config.TestHomeCheck(); !ok || every != time.Second {
		t.Errorf("TestHomeCheck() = %v, %v after NameTestBinary: want 1s", every, ok)
	}
}

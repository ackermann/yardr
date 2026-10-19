package cli

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/peer"
)

// A daemon a test starts stops when the test process is gone, as the server
// does: a test binary killed by its timeout runs no cleanup and leaves the
// daemon behind otherwise. A sleep stands in for the test process, which no
// test can kill and survive.
func TestDaemonStopsWhenItsTestOwnerIsGone(t *testing.T) {
	sleep := exec.Command("sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sleep.Process.Kill(); sleep.Wait() })
	var begin uint64
	if st, err := peer.Describe(sleep.Process.Pid); err == nil {
		begin = st.Start
	}
	// The daemon's watch reads the test-only owner from the process.
	t.Setenv("YARDR_TEST_OWNER", fmt.Sprintf("%d %d", sleep.Process.Pid, begin))
	_, ctx, logged := daemonHome(t)
	sleep.Process.Kill()
	sleep.Wait()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon was not told to stop")
	}
	// The line is written before the context ends.
	if n := strings.Count(logged.String(), "the test that started"); n != 1 {
		t.Fatalf("want one line about the test owner, got %d:\n%s", n, logged.String())
	}
}

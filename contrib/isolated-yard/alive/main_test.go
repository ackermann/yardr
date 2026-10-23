package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
)

// alive answers the watchdog by the owner YARDR_TEST_OWNER names: 0 while it
// runs, 1 once it is gone, 2 for a name it cannot use. The watchdog takes a
// yard down for a 1 or a 2 and for nothing else, so each of the three has to
// be what it is said to be, and a pid that was reused is not the owner.
func TestAliveSaysWhetherTheOwnerRuns(t *testing.T) {
	self, err := peer.Describe(os.Getpid())
	if err != nil {
		t.Skipf("this system tells nothing of a process's start: %v", err)
	}
	// An owner that has ended: a sleep, described while it ran, then killed
	// and reaped.
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	ended, err := peer.Describe(sleep.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	sleep.Process.Kill()
	sleep.Wait()

	for _, c := range []struct {
		name, owner string
		want        int
	}{
		{"this process, running", fmt.Sprintf("%d %d", os.Getpid(), self.Start), 0},
		{"a process that has ended", fmt.Sprintf("%d %d", sleep.Process.Pid, ended.Start), 1},
		{"a pid that is another process's by now", fmt.Sprintf("%d %d", os.Getpid(), self.Start+1), 1},
		{"no owner", "", 2},
		{"a pid alone", "123", 2},
		{"three fields", "1 2 3", 2},
		{"a pid that is no number", "x 5", 2},
		{"a pid of zero", "0 5", 2},
		{"a start that is no number", "123 x", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(config.Key(config.TestOwnerEnv), c.owner)
			var stderr strings.Builder
			if got := run(&stderr); got != c.want {
				t.Errorf("alive says %d for %q, want %d (stderr %q)", got, c.owner, c.want, stderr.String())
			}
			if c.want == 2 && !strings.Contains(stderr.String(), config.Key(config.TestOwnerEnv)) {
				t.Errorf("alive gave %q for the 2, want it to name %s", stderr.String(), config.Key(config.TestOwnerEnv))
			}
			if c.want != 2 && stderr.Len() != 0 {
				t.Errorf("alive wrote %q for a %d, want nothing", stderr.String(), c.want)
			}
		})
	}
}

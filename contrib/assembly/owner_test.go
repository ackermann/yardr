package assembly

// A server a test starts stops by itself when the test process is gone,
// however the test ended: the watch YARDR_TEST_OWNER sets up, end to end
// through the real CLI.

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/testload"
)

// The finding's case is a SIGKILLed test binary that runs no cleanup; no
// test can kill itself and check the server after, so a sleep process stands
// in for the test: the server is started through the real CLI with the owner
// naming the sleep, the sleep is SIGKILLed, and no `yardr server run` from
// the test's binary may remain past the watch interval. The watch ticks on
// the test-only tick, not the real minute, so the test takes seconds; the
// home is left intact throughout, so only the owner watch can stop it.
func TestServerStopsWhenItsTestOwnerIsGone(t *testing.T) {
	t.Parallel()
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Home: home}
	t.Cleanup(func() {
		// If the watch failed, stop the orphan by hand, as the finding did.
		if err := server.Stop(cfg); err != nil {
			t.Log(err)
		}
		os.RemoveAll(home)
	})
	sleep := exec.Command("sleep", "300")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sleep.Process.Kill(); sleep.Wait() })
	var start uint64
	if st, err := peer.Describe(sleep.Process.Pid); err == nil {
		start = st.Start
	}
	env := append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test",
		fmt.Sprintf("YARDR_TEST_OWNER=%d %d", sleep.Process.Pid, start),
		"YARDR_TEST_HOME_CHECK=1s")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(yardrBin, args...)
		cmd.Dir, cmd.Env = home, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("yardr %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--harness", "claude", "--dir", home, "--operator", "test")
	run("bead", "list") // starts the server through the real CLI
	pidFile, err := os.ReadFile(cfg.PIDFile())
	if err != nil {
		t.Fatalf("the server wrote no pidfile: %v", err)
	}
	serverPID, err := strconv.Atoi(strings.TrimSpace(string(pidFile)))
	if err != nil {
		t.Fatalf("bad pidfile %q: %v", pidFile, err)
	}
	var serverStart uint64
	if b, err := os.ReadFile(cfg.PIDStart()); err == nil {
		serverStart, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	}
	sleep.Process.Kill()
	sleep.Wait()
	fuse := testload.Scale(90 * time.Second)
	for deadline := time.Now().Add(fuse); peer.Alive(serverPID, serverStart); {
		if time.Now().After(deadline) {
			t.Fatalf("the server (pid %d) outlived its owner past %s", serverPID, fuse)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// It went by the owner watch, not by a crash: the home stands, and the
	// log says which watch stopped it.
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("the home is gone: %v", err)
	}
	logged, err := os.ReadFile(cfg.LogFile())
	if err != nil {
		t.Fatalf("the server wrote no log: %v", err)
	}
	if n := strings.Count(string(logged), "the test that started the yardr server is gone"); n != 1 {
		t.Fatalf("want one owner line in the server log, got %d:\n%s", n, logged)
	}
}

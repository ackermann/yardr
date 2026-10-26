package server

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/version"
)

// What every other test of a replace stands in for with a fake exec, done
// for real: the old build serves, a newer one is installed over its binary,
// and the process execs it and comes back, same pid, with a socket that
// answers. Both builds are this test binary, started as the server by
// TestReplaceImage; which one a copy is, is what the rev file beside it says.
const (
	replaceHomeEnv = "YARDR_TEST_REPLACE_HOME"
	replaceBinsEnv = "YARDR_TEST_REPLACE_BINS"
)

// TestReplaceImage is no test of its own: it is the process
// TestRealExecReplaceKeepsTheSocket starts, and the one that process execs.
// It serves the home in replaceHomeEnv as the build the rev file in
// replaceBinsEnv names, and replaces itself when another is installed over
// its binary there.
func TestReplaceImage(t *testing.T) {
	home, bins := os.Getenv(replaceHomeEnv), os.Getenv(replaceBinsEnv)
	if home == "" || bins == "" {
		t.Skip("run by TestRealExecReplaceKeepsTheSocket")
	}
	revOf := func() string {
		b, _ := os.ReadFile(filepath.Join(bins, "rev"))
		return strings.TrimSpace(string(b))
	}
	rev := revOf()
	bin, err := version.Watch(filepath.Join(bins, "yardr"), rev)
	if err != nil {
		t.Fatal(err)
	}
	bin.ReadBuild = func(string) (version.Build, error) { return version.Build{Revision: revOf()}, nil }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := &runner{c: &config.Config{Home: home}, log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		bin: bin, rev: rev, check: 50 * time.Millisecond}
	if err := r.run(ctx); err != nil {
		t.Fatal(err)
	}
}

// installBuild makes a copy of the test binary the build rev: written beside
// bins/yardr, stamped mtime and renamed over it, as go install does, with
// the rev file written first so that no one reads the new binary beside the
// old name.
func installBuild(t *testing.T, bins, rev string, mtime time.Time) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	tmp := filepath.Join(bins, "yardr.tmp")
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bins, "rev"), []byte(rev), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(bins, "yardr")); err != nil {
		t.Fatal(err)
	}
}

func TestRealExecReplaceKeepsTheSocket(t *testing.T) {
	// A build made without a VCS stamp carries no revision, and only its
	// file's time says it is newer: it was taken for a stop, and the server
	// ended, its socket and pidfile gone, instead of execing it.
	for _, tc := range []struct{ name, old, new string }{
		{"with a revision", "old", "new"},
		{"without a revision", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) { realExecReplace(t, tc.old, tc.new) })
	}
}

func realExecReplace(t *testing.T, oldRev, newRev string) {
	c := home(t)
	bins := t.TempDir()
	now := time.Now()
	installBuild(t, bins, oldRev, now)

	cmd := exec.Command(filepath.Join(bins, "yardr"), "-test.run=^TestReplaceImage$", "-test.count=1")
	logf, err := os.Create(filepath.Join(bins, "image.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	cmd.Stdout, cmd.Stderr = logf, logf
	// The image stops with this test binary however it ends, as a server a
	// test starts does (storetest.NameTestBinary), and looks sooner for it.
	var start uint64
	if st, err := peer.Describe(os.Getpid()); err == nil {
		start = st.Start
	}
	cmd.Env = append(os.Environ(), replaceHomeEnv+"="+c.Home, replaceBinsEnv+"="+bins,
		config.Key(config.TestOwnerEnv)+"="+strconv.Itoa(os.Getpid())+" "+strconv.FormatUint(start, 10),
		config.Key(config.TestHomeCheckEnv)+"=1s")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(bins, "image.log"))
			t.Logf("the server image's log:\n%s", b)
		}
	})

	ctx := context.Background()
	asked := rpc.NewClient(c.Socket())
	owner := func() (int, string) {
		pid, rev, err := asked.Owner(ctx)
		if err != nil {
			return 0, ""
		}
		return pid, rev
	}
	waitFor(t, "the old build did not answer", func() bool {
		pid, rev := owner()
		return pid == cmd.Process.Pid && rev == oldRev
	})

	installBuild(t, bins, newRev, now.Add(time.Minute))
	// The image looks at its binary on a short tick, and a client of the new
	// build is refused once and retries until the new build answers. The new
	// build says it replaced the old one, and that is what is waited for: a
	// build with no revision has no other word to be told apart by.
	newer := client(c, newRev)
	var told bool
	waitFor(t, "the new build did not say it replaced the old one", func() bool {
		evs, err := newer.EventsSince(ctx, 0, 0)
		if err != nil {
			return false
		}
		for _, e := range evs {
			told = told || (e.Kind == "server_replaced" && e.Data["from"] == version.Show(oldRev) && e.Data["to"] == version.Show(newRev))
		}
		return told
	})
	if pid, rev := owner(); pid != cmd.Process.Pid || rev != newRev {
		t.Fatalf("after the replace the socket is answered by pid %d, build %q; want pid %d, build %q (same process, exec'd)",
			pid, rev, cmd.Process.Pid, newRev)
	}
	if fi, err := os.Stat(c.Socket()); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket after the replace: %v, %v", fi, err)
	}
	if !accepts(c) {
		t.Fatal("the socket does not take a connection after the replace")
	}
	if b, _ := os.ReadFile(c.PIDFile()); strings.TrimSpace(string(b)) != strconv.Itoa(cmd.Process.Pid) {
		t.Fatalf("pidfile after the replace = %q, want %d", b, cmd.Process.Pid)
	}
}

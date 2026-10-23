package dispatch

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestLandingsSinceQuietKeepsOlderDepotMarker(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	if err := s.PutDepot(ctx, store.Depot{Name: "target", Base: "main"}, "review"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "before quiet run", Depot: "target"}, "review")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, b.ID, "merged", store.Expect{}, "review"); err != nil {
		t.Fatal(err)
	}
	if err := s.LogDispatch(ctx, "", store.QuietStarted, "review", map[string]any{"depot": "target"}); err != nil {
		t.Fatal(err)
	}
	// Other depots can run often enough that a bounded global tail no
	// longer contains target's marker.
	for i := 0; i < 501; i++ {
		if err := s.LogDispatch(ctx, "", store.QuietStarted, "review", map[string]any{"depot": "other"}); err != nil {
			t.Fatal(err)
		}
	}

	since, err := (&Dispatcher{Store: s}).landingsSinceQuiet(ctx, "target")
	if err != nil {
		t.Fatal(err)
	}
	if since != 0 {
		t.Fatalf("landings since target's quiet run = %d, want 0", since)
	}
}

func TestQuietCommandCancelsItsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := quietCommand(ctx, "sh", "-c", "sleep 60 & echo $!; wait")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("child pid %q: %v", line, err)
	}

	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("canceled command succeeded")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(child, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("check child process %d: %v", child, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived cancellation", child)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// quietDepot is a repository whose base holds check.sh, the depot's test
// command, with the script as its text. HOME is a directory of the test's own,
// which the quiet run's race lock is under. It returns the depot and git in
// its repository.
func quietDepot(t *testing.T, script string) (store.Depot, func(args ...string) string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "check.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	git("add", "check.sh")
	git("commit", "-m", "a base")
	return store.Depot{Name: "fixture", Path: repo, Kind: store.DepotGit, Base: "main", Test: "check.sh"}, git
}

func TestExecQuietRunRemovesWorktreeAfterCancellation(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("READY", ready)
	depot, git := quietDepot(t, "printf ready > \"$READY\"\nexec sleep 60\n")
	repo := depot.Path

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (&Dispatcher{}).execQuietRun(ctx, depot)
		done <- err
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("quiet test command did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled quiet run succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("quiet run did not return after cancellation")
	}

	worktrees := git("worktree", "list", "--porcelain")
	var paths []string
	for _, line := range strings.Split(worktrees, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, path)
		}
	}
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != resolvedRepo {
		t.Fatalf("worktrees after canceled run = %v, want only %s", paths, resolvedRepo)
	}
}

// The quiet run's tests start with no descriptor above 2 that the daemon
// holds, as a landing's do (the exec runner's test of it is
// TestExecStartsWithNoInheritedFile in internal/runner): nothing sits between
// the daemon and the depot's command here, as the landing script does for a
// landing, so the run itself keeps them out. The test holds files open with
// close-on-exec cleared, as libc leaves its own, and the depot's command
// probes every one.
func TestExecQuietRunStartsTestsWithNoInheritedFile(t *testing.T) {
	// Serial: it sets HOME, and clears close-on-exec on files of the process,
	// which the sweep of any other start would mark again before this one's.
	record := filepath.Join(t.TempDir(), "fds")
	var probes []string
	for i := 0; i < 3; i++ {
		f, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_SETFD, 0); errno != 0 {
			t.Fatal(errno)
		}
		probes = append(probes, strconv.Itoa(int(f.Fd())))
	}
	t.Setenv("RECORD", record)
	depot, _ := quietDepot(t, "for probe in "+strings.Join(probes, " ")+"; do"+
		` [ -e /dev/fd/$probe ] || [ -L /dev/fd/$probe ] || continue; echo "leaked $probe"; done >"$RECORD"`+"\n"+
		`echo done >>"$RECORD"`+"\n")

	if _, err := (&Dispatcher{}).execQuietRun(context.Background(), depot); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(record); err != nil || string(got) != "done\n" {
		t.Errorf("the tests held descriptors %s open, and the probe says %q (%v), want only done", strings.Join(probes, ", "), got, err)
	}
}

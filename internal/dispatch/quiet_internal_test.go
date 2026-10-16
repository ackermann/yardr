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

func TestExecQuietRunRemovesWorktreeAfterCancellation(t *testing.T) {
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
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("READY", ready)
	if err := os.WriteFile(filepath.Join(repo, "check.sh"), []byte("printf ready > \"$READY\"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("add", "check.sh")
	git("commit", "-m", "a waiting base")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	depot := store.Depot{Name: "fixture", Path: repo, Kind: store.DepotGit, Base: "main", Test: "check.sh"}
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

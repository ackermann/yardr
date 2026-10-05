package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// With herdr missing, every Start fails as a runtime outage: no bead is
// held or charged a failure, and each group gets one runtime_down event.
func TestHerdrMissingHoldsNoBead(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	// A PATH with git and nothing else: no herdr anywhere.
	gitBin, err := exec.LookPath("git")
	mustT(t, err)
	path := t.TempDir()
	mustT(t, os.Symlink(gitBin, filepath.Join(path, "git")))
	t.Setenv("PATH", path)
	h := &Herdr{Bin: "herdr", WorktreeRoot: t.TempDir()}
	d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"herdr": h},
		Backoff: time.Nanosecond, RuntimeBackoff: time.Nanosecond, MaxAttempts: 1}
	mustT(t, st.PutRig(ctx, store.Rig{Name: "r", Path: repo, Base: "main"}, "me"))
	for _, g := range []string{"builders", "reviewers"} {
		mustT(t, st.PutGroup(ctx, store.Group{Name: g, Runner: "herdr", Limit: 2}))
	}
	_, _ = st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"})
	_, _ = st.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"})
	rig := "r"
	var beads []*store.Bead
	for i, stage := range []string{"new", "new", "review"} {
		b, err := st.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i), Stage: stage, Rig: rig}, "me")
		mustT(t, err)
		beads = append(beads, b)
	}
	for range 4 {
		mustT(t, d.Reconcile(ctx))
		time.Sleep(time.Millisecond)
	}
	for _, b := range beads {
		got, _ := st.GetBead(ctx, b.ID)
		if got.Hold || got.Group != nil {
			t.Fatalf("bead %s held or kept by the outage: %+v", b.ID, got)
		}
	}
	evs, _ := st.EventsSince(ctx, 0, 0)
	down := map[string]int{}
	for _, e := range evs {
		switch e.Kind {
		case "runtime_down":
			down[fmt.Sprint(e.Data["group"])]++
			if !strings.Contains(fmt.Sprint(e.Data["reason"]), "herdr") {
				t.Errorf("runtime_down reason does not name herdr: %v", e.Data)
			}
		case "session_failed", "gave_up", "held":
			t.Fatalf("outage counted against a bead: %s %v", e.Kind, e.Data)
		}
	}
	if down["builders"] != 1 || down["reviewers"] != 1 || len(down) != 2 {
		t.Fatalf("runtime_down per group = %v, want one each", down)
	}
}

// hangingGit puts a fake git first on PATH and returns the file it writes
// GIT_TERMINAL_PROMPT to. It exits at once unless FAKE_GIT_HANG is set, in
// which case it sleeps well past any deadline a test sets. The two are apart
// because a deadline short enough to test can pass before the script has run
// its first line (process start under a full -race suite takes seconds), so
// what git was told is read from a call nothing cuts off.
func hangingGit(t *testing.T) (envOut string) {
	t.Helper()
	bin := t.TempDir()
	envOut = filepath.Join(t.TempDir(), "env")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$GIT_TERMINAL_PROMPT\" > " + envOut +
		"\nif [ -n \"$FAKE_GIT_HANG\" ]; then exec sleep 30; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return envOut
}

// noPrompt fails the test unless the fake git ran and was told never to
// prompt.
func noPrompt(t *testing.T, envOut string) {
	t.Helper()
	got, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("fake git never ran: %v", err)
	}
	if strings.TrimSpace(string(got)) != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, want 0", got)
	}
}

// A fetch of the base that hangs (a dead remote, a credential prompt) is cut
// off at the deadline, and git is told never to prompt.
func TestFetchBaseHasADeadline(t *testing.T) {
	envOut := hangingGit(t)
	rig := &store.Rig{Path: t.TempDir(), Base: "origin/main"}
	(&Herdr{}).fetchBase(context.Background(), rig)
	noPrompt(t, envOut)

	// Well short of the 30s sleep. Whether the fake has started by then
	// does not matter: the fetch is cut off either way.
	t.Setenv("FAKE_GIT_HANG", "1")
	h := &Herdr{FetchTimeout: 2 * time.Second}
	start := time.Now()
	h.fetchBase(context.Background(), rig)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("fetch returned after %s; the deadline did not hold", took)
	}
	if FetchTimeout != 60*time.Second {
		t.Fatalf("FetchTimeout = %s", FetchTimeout)
	}
}

// Errors herdr itself reports (an error object) stay the bead's; only a
// herdr that cannot be run or reached is a runtime outage.
func TestHerdrErrorsClassified(t *testing.T) {
	ctx := context.Background()
	h := &Herdr{Bin: fakeHerdrBin(t, `case "$3" in
  known) echo '{"error":{"code":"agent_exists","message":"taken"}}' >&2; exit 1 ;;
  *)     echo 'connection refused' >&2; exit 1 ;;
esac`)}
	if _, err := h.run(ctx, "agent", "start", "known"); err == nil || dispatch.IsRuntime(err) {
		t.Fatalf("herdr error object classified as runtime: %v", err)
	}
	if _, err := h.run(ctx, "agent", "start", "x"); !dispatch.IsRuntime(err) {
		t.Fatalf("unreachable herdr not a runtime error: %v", err)
	}
	missing := &Herdr{Bin: filepath.Join(t.TempDir(), "herdr")}
	if _, err := missing.run(ctx, "agent", "list"); !dispatch.IsRuntime(err) {
		t.Fatalf("missing herdr not a runtime error: %v", err)
	}
}

// herdr's own error object for a server that is down or not ready
// (server_not_running, server_unavailable) is a runtime outage like an
// unreachable herdr, not the bead's failure, and Start gives up on it at
// once instead of falling back to re-adding the worktree by hand.
func TestHerdrDownErrorObjectIsAnOutage(t *testing.T) {
	ctx := context.Background()
	for _, code := range []string{"server_not_running", "server_unavailable"} {
		h := &Herdr{Bin: fakeHerdrBin(t, `echo '{"id":"cli:x","error":{"code":"`+code+`","message":"no herdr server is running"}}' >&2; exit 1`)}
		_, err := h.run(ctx, "agent", "list")
		if !dispatch.IsRuntime(err) {
			t.Fatalf("%s not a runtime error: %v", code, err)
		}
		var herr *herdrError
		if !errors.As(err, &herr) || herr.Code != code {
			t.Fatalf("%s: the herdr error is no longer in the chain: %v", code, err)
		}
		// Status must say "unknown" (an error), never "gone".
		if _, err := h.Status(ctx, map[string]any{"agent": "a"}); err == nil {
			t.Fatalf("%s: Status reported the agent gone", code)
		}
	}

	// What yardr doctor asks (yardr-aa7y): no server at the socket is told
	// from a server that is there and not ready, and from one that answers.
	for body, want := range map[string][2]bool{ // down, an error
		`echo '{"id":"cli:x","error":{"code":"server_not_running","message":"no herdr server is running"}}' >&2; exit 1`: {true, true},
		`echo '{"id":"cli:x","error":{"code":"server_unavailable","message":"starting"}}' >&2; exit 1`:                   {false, true},
		`echo '{"id":"cli:x","result":{"agents":[]}}'`:                                                                   {false, false},
	} {
		down, err := (&Herdr{Bin: fakeHerdrBin(t, body)}).ServerUp(ctx)
		if down != want[0] || (err != nil) != want[1] {
			t.Errorf("ServerUp with %s: down %v, err %v", body, down, err)
		}
	}

	// Through Start: no fallback to `git worktree add`, which would make a
	// checkout herdr knows nothing about.
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := t.TempDir()
	h := &Herdr{WorktreeRoot: root, Bin: fakeHerdrBin(t,
		`echo '{"id":"cli:x","error":{"code":"server_not_running","message":"no herdr server is running"}}' >&2; exit 1`)}
	job := dispatch.Job{
		Bead:    store.Bead{ID: "r-1", Stage: "new"},
		Group:   store.Group{Name: "builders"},
		Rig:     &store.Rig{Name: "r", Path: repo, Base: "main"},
		Session: store.Session{ID: "s1"},
	}
	_, err := h.Start(ctx, job)
	if !dispatch.IsRuntime(err) {
		t.Fatalf("Start with herdr down: %v, want a runtime error", err)
	}
	if _, serr := os.Stat(filepath.Join(root, "r", "r-1")); serr == nil {
		t.Fatal("Start added a worktree by hand while herdr was down")
	}
}

// Every git call Start and Finish make is cut off at a deadline, like the
// fetch, and never waits on a credential prompt.
func TestGitCallsHaveADeadline(t *testing.T) {
	envOut := hangingGit(t)
	if err := git(context.Background(), t.TempDir(), "worktree", "add", "x", "y"); err != nil {
		t.Fatalf("fake git: %v", err)
	}
	noPrompt(t, envOut)

	t.Setenv("FAKE_GIT_HANG", "1")
	was := checkoutTimeout
	checkoutTimeout = 2 * time.Second // see TestFetchBaseHasADeadline on the margin
	t.Cleanup(func() { checkoutTimeout = was })

	start := time.Now()
	err := git(context.Background(), t.TempDir(), "worktree", "add", "x", "y")
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("git returned after %s; the deadline did not hold", took)
	}
	if err == nil {
		t.Fatal("a git call cut off at the deadline returned no error")
	}
	if was != 2*time.Minute {
		t.Fatalf("checkoutTimeout = %s", was)
	}
}

package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// quietYard is a yard with one git depot, a threshold of two landings and
// a stand-in whole-suite run.
type quietYard struct {
	s     store.API
	d     *dispatch.Dispatcher
	mu    sync.Mutex
	calls int
	block chan struct{} // when set, the stand-in run waits on it
	res   dispatch.QuietResult
	err   error
}

func newQuietYard(t *testing.T) *quietYard {
	t.Helper()
	y := &quietYard{res: dispatch.QuietResult{Commit: "abc123", Wall: time.Second, Load: "1.00"}}
	y.s = storetest.New(t)
	ctx := context.Background()
	// A git depot, as the store asks of one: a repository on its base.
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"commit", "--allow-empty", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	must(t, y.s.PutDepot(ctx, store.Depot{Name: "yard", Path: repo, Kind: store.DepotGit, Base: "main", Test: ".yardr/test"}, "me"))
	must(t, y.s.SetSetting(ctx, store.QuietLandingsSetting, "2", "me"))
	y.d = &dispatch.Dispatcher{Store: y.s, Log: slog.New(slog.DiscardHandler), Home: t.TempDir(),
		QuietRun: func(ctx context.Context, depot store.Depot) (dispatch.QuietResult, error) {
			y.mu.Lock()
			y.calls++
			block := y.block
			y.mu.Unlock()
			if block != nil {
				select {
				case <-block:
				case <-ctx.Done():
				}
			}
			return y.res, y.err
		}}
	return y
}

func (y *quietYard) runCalls() int {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.calls
}

// land puts one more bead of the depot in merged, as a landing does.
func (y *quietYard) land(t *testing.T, title string) {
	t.Helper()
	ctx := context.Background()
	b, err := y.s.CreateBead(ctx, store.NewBead{Title: title, Depot: "yard"}, "me")
	must(t, err)
	_, err = y.s.Advance(ctx, b.ID, "merged", store.Expect{}, "me")
	must(t, err)
}

// waitQuiet waits for the depot's quiet runs to be told of as kind says.
func waitQuiet(t *testing.T, s store.API, kind string, n int) []store.Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		evs, err := s.EventsOfKind(context.Background(), kind, 50)
		must(t, err)
		var mine []store.Event
		for _, e := range evs {
			if e.Data["depot"] == "yard" {
				mine = append(mine, e)
			}
		}
		if len(mine) >= n {
			return mine
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d %s events for the depot, got %d", n, kind, len(mine))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQuietPackagesAndRelevance(t *testing.T) {
	out := "ok  \tyardr.dev/yardr/internal/cli\t1s\nFAIL\tyardr.dev/yardr/internal/store\t2s\nFAIL yardr.dev/yardr/internal/web 3s\nFAIL\tyardr.dev/yardr/internal/store\t2s\n"
	want := []string{"yardr.dev/yardr/internal/store", "yardr.dev/yardr/internal/web"}
	if got := dispatch.QuietPackages(out); !slices.Equal(got, want) {
		t.Fatalf("QuietPackages = %v, want %v", got, want)
	}
	for kind, want := range map[string]bool{
		"advanced": true, "closed": true, "session_died": true,
		"session_failed": true, "session_aborted": true,
		"claimed": false, "noted": false, "quiet_passed": false,
	} {
		if got := dispatch.QuietRelevant(store.Event{Kind: kind}); got != want {
			t.Errorf("QuietRelevant(%s) = %v, want %v", kind, got, want)
		}
	}
}

// The run fires when the yard is quiet and owed, and only then: below the
// threshold nothing starts, however quiet the yard.
func TestQuietFiresOnlyWhenOwedAndQuiet(t *testing.T) {
	y := newQuietYard(t)
	ctx := context.Background()
	dispatch.QuietMaybe(y.d, ctx)
	if n := y.runCalls(); n != 0 {
		t.Fatalf("run started with no landings, calls = %d", n)
	}
	y.land(t, "one")
	dispatch.QuietMaybe(y.d, ctx)
	if n := y.runCalls(); n != 0 {
		t.Fatalf("run started below the threshold, calls = %d", n)
	}
	y.land(t, "two")
	dispatch.QuietMaybe(y.d, ctx)
	evs := waitQuiet(t, y.s, store.QuietPassed, 1)
	if n := y.runCalls(); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
	if got := evs[0].Data["commit"]; got != "abc123" {
		t.Fatalf("quiet_passed commit = %v, want the run's base commit", got)
	}
	// The count starts over: two more landings owe the next run.
	dispatch.QuietMaybe(y.d, ctx)
	if n := y.runCalls(); n != 1 {
		t.Fatalf("run repeated with nothing new landed, calls = %d", n)
	}
}

// A threshold reached while the yard is busy is not lost: a bead in
// approved, then a live session, each hold the run, and it fires when the
// last of them clears, with no landing in between.
func TestQuietThresholdWaitsForQuietAndIsNotLost(t *testing.T) {
	y := newQuietYard(t)
	ctx := context.Background()
	y.land(t, "one")
	y.land(t, "two")

	waiting, err := y.s.CreateBead(ctx, store.NewBead{Title: "waiting", Depot: "yard"}, "me")
	must(t, err)
	_, err = y.s.Advance(ctx, waiting.ID, "approved", store.Expect{}, "me")
	must(t, err)
	dispatch.QuietMaybe(y.d, ctx)
	if n := y.runCalls(); n != 0 {
		t.Fatalf("run started with a bead in approved, calls = %d", n)
	}

	// Approved empties, but a builder session is live: still busy.
	_, err = y.s.Advance(ctx, waiting.ID, "review", store.Expect{}, "me")
	must(t, err)
	must(t, y.s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake"}, "me"))
	work, err := y.s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard"}, "me")
	must(t, err)
	sess, err := y.s.Claim(ctx, work.ID, "builders", "new", "me")
	must(t, err)
	dispatch.QuietMaybe(y.d, ctx)
	if n := y.runCalls(); n != 0 {
		t.Fatalf("run started with a session live, calls = %d", n)
	}

	must(t, y.s.EndSession(ctx, sess.ID, "failed", "done for the test"))
	dispatch.QuietMaybe(y.d, ctx)
	waitQuiet(t, y.s, store.QuietPassed, 1)
	if n := y.runCalls(); n != 1 {
		t.Fatalf("calls = %d, want the held run fired once the yard went quiet", n)
	}
}

// While one run runs, further judging starts no second; the trigger
// re-arms when it finishes, and the finished run's count leaves nothing
// owed.
func TestQuietRunIsSingleFlight(t *testing.T) {
	y := newQuietYard(t)
	y.block = make(chan struct{})
	ctx := context.Background()
	y.land(t, "one")
	y.land(t, "two")
	dispatch.QuietMaybe(y.d, ctx)
	deadline := time.Now().Add(10 * time.Second)
	for y.runCalls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := y.runCalls(); n != 1 {
		t.Fatalf("calls = %d, want the run started", n)
	}
	dispatch.QuietMaybe(y.d, ctx)
	dispatch.QuietMaybe(y.d, ctx)
	close(y.block)
	waitQuiet(t, y.s, store.QuietPassed, 1)
	// Let the re-arm after the finish judge too: it owes nothing.
	time.Sleep(50 * time.Millisecond)
	if n := y.runCalls(); n != 1 {
		t.Fatalf("calls = %d, want single-flight: one run only", n)
	}
}

// A red run is told as quiet_failed naming its packages and its base
// commit, so the yard hears of a red base with no bead to send back.
func TestQuietRedRunNamesPackagesAndCommit(t *testing.T) {
	y := newQuietYard(t)
	y.res = dispatch.QuietResult{Commit: "red123", Wall: time.Second, Load: "2.00",
		Packages: []string{"yardr.dev/yardr/internal/store"}}
	y.err = errors.New("the whole suite failed")
	ctx := context.Background()
	y.land(t, "one")
	y.land(t, "two")
	dispatch.QuietMaybe(y.d, ctx)
	evs := waitQuiet(t, y.s, store.QuietFailed, 1)
	if got := evs[0].Data["commit"]; got != "red123" {
		t.Fatalf("quiet_failed commit = %v, want red123", got)
	}
	pkgs, _ := evs[0].Data["packages"].([]any)
	if len(pkgs) != 1 || pkgs[0] != "yardr.dev/yardr/internal/store" {
		t.Fatalf("quiet_failed packages = %v, want the failing package named", evs[0].Data["packages"])
	}
}

// The trigger itself, on the stream: landings committed after it starts
// fire the run with no judging by hand and no clock waited on.
func TestQuietTriggerFiresOnLandingEvents(t *testing.T) {
	y := newQuietYard(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dispatch.QuietTrigger(y.d, ctx)
	// The trigger judges once as it starts, when nothing is owed.
	time.Sleep(50 * time.Millisecond)
	if n := y.runCalls(); n != 0 {
		t.Fatalf("run started at trigger start with nothing owed, calls = %d", n)
	}
	y.land(t, "one")
	y.land(t, "two")
	waitQuiet(t, y.s, store.QuietPassed, 1)
	if n := y.runCalls(); n != 1 {
		t.Fatalf("calls = %d, want the landing events to fire one run", n)
	}
}

// The real run, as a drill: a fixture depot whose base carries a failing
// check at its head commit. The run tests that commit in its own
// worktree, and the yard is told it failed, with the package and commit.
func TestQuietExecRunAgainstARedBase(t *testing.T) {
	ctx := context.Background()
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
	must(t, os.WriteFile(filepath.Join(repo, "check.sh"),
		[]byte("echo 'FAIL\\tyardr.dev/fixture/bad\\t0.1s'\nexit 1\n"), 0o755))
	git("add", "check.sh")
	git("commit", "-m", "a red base")
	red := git("rev-parse", "HEAD")
	t.Logf("drill red commit %s", red)

	s := storetest.New(t)
	must(t, s.PutDepot(ctx, store.Depot{Name: "yard", Path: repo, Kind: store.DepotGit, Base: "main", Test: "check.sh"}, "me"))
	must(t, s.SetSetting(ctx, store.QuietLandingsSetting, "1", "me"))
	d := &dispatch.Dispatcher{Store: s, Log: slog.New(slog.DiscardHandler), Home: t.TempDir()}
	b, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprintf("landing %d", 1), Depot: "yard"}, "me")
	must(t, err)
	_, err = s.Advance(ctx, b.ID, "merged", store.Expect{}, "me")
	must(t, err)
	dispatch.QuietMaybe(d, ctx)
	evs := waitQuiet(t, s, store.QuietFailed, 1)
	if got := evs[0].Data["commit"]; got != red {
		t.Fatalf("quiet_failed commit = %v, want the red base %s", got, red)
	}
	pkgs, _ := evs[0].Data["packages"].([]any)
	if len(pkgs) != 1 || pkgs[0] != "yardr.dev/fixture/bad" {
		t.Fatalf("quiet_failed packages = %v, want the fixture's failing package", evs[0].Data["packages"])
	}
	// The depot's own checkout was never the worktree: it is still clean
	// and on its base.
	if out := git("status", "--porcelain"); out != "" {
		t.Fatalf("depot checkout left dirty by the quiet run: %s", out)
	}
}

// The real run again, on a green base under /tmp as the daemon makes
// its worktrees (no TMPDIR of a test harness): the check compares git's
// resolved toplevel with the directory it runs in, and the run resolves
// its worktree before adding it, so the two name it the same way.
func TestQuietExecRunResolvesItsWorktree(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	must(t, os.WriteFile(filepath.Join(repo, "check.sh"),
		[]byte("[ \"$(git rev-parse --show-toplevel)\" = \"$PWD\" ]\n"), 0o755))
	git("add", "check.sh")
	git("commit", "-m", "a green base")

	s := storetest.New(t)
	must(t, s.PutDepot(ctx, store.Depot{Name: "yard", Path: repo, Kind: store.DepotGit, Base: "main", Test: "check.sh"}, "me"))
	must(t, s.SetSetting(ctx, store.QuietLandingsSetting, "1", "me"))
	d := &dispatch.Dispatcher{Store: s, Log: slog.New(slog.DiscardHandler), Home: t.TempDir()}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "landing", Depot: "yard"}, "me")
	must(t, err)
	_, err = s.Advance(ctx, b.ID, "merged", store.Expect{}, "me")
	must(t, err)
	dispatch.QuietMaybe(d, ctx)
	waitQuiet(t, s, store.QuietPassed, 1)
}

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
	"yardr.dev/yardr/internal/yardkey"
)

// quietYard is a yard with one git depot, a threshold of two landings and
// a stand-in whole-suite run.
type quietYard struct {
	s       store.API
	d       *dispatch.Dispatcher
	mu      sync.Mutex
	calls   int
	started chan struct{} // the stand-in suite has entered its run
	block   chan struct{} // when set, the stand-in run waits on it
	res     dispatch.QuietResult
	err     error
}

func newQuietYard(t *testing.T) *quietYard {
	t.Helper()
	y := &quietYard{res: dispatch.QuietResult{Commit: "abc123", Wall: time.Second, Load: "1.00"},
		started: make(chan struct{}, 1)}
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
			select {
			case y.started <- struct{}{}:
			default:
			}
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

// putGroups gives the yard a group of each runner it has: the backlog,
// whose beads wait for a person (manual), builders (herdr), an assembly
// (exec) and a stage the yard at airy works (peer).
func (y *quietYard) putGroups(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "airy.key"))
	must(t, err)
	_, err = y.s.AddPeer(ctx, store.Peer{Name: "airy", Key: yardkey.Public(key)}, "me")
	must(t, err)
	_, err = y.s.AllowPeer(ctx, "airy", store.PeerAllow{Send: []string{store.PeerBead}}, "me")
	must(t, err)
	for _, g := range []store.Group{
		{Name: "backlog", Runner: "manual", Limit: 50},
		{Name: "at-airy", Runner: store.PeerRunner, Limit: 5, Config: map[string]any{store.GroupPeer: "airy"}},
		{Name: "builders", Runner: "herdr", Limit: 5},
		{Name: "assembly", Runner: "exec", Limit: 5, Config: map[string]any{"cmd": "true"}},
	} {
		must(t, y.s.PutGroup(ctx, g, "me"))
	}
}

// claim opens a live session in group on a bead of its own, as the
// dispatcher's claim does. Nothing starts: the judging reads only the
// sessions the store holds.
func (y *quietYard) claim(t *testing.T, group string) store.Session {
	t.Helper()
	ctx := context.Background()
	b, err := y.s.CreateBead(ctx, store.NewBead{Title: "held in " + group}, "me")
	must(t, err)
	sess, err := y.s.Claim(ctx, b.ID, group, b.Stage, "me")
	must(t, err)
	return *sess
}

// ready is quietReady's answer for the yard's depot at the yard's own
// threshold. The landings must already be owed, so that an answer of no is
// about the yard's sessions and not about the count.
func (y *quietYard) ready(t *testing.T) bool {
	t.Helper()
	ctx := context.Background()
	depot, err := y.s.GetDepot(ctx, "yard")
	must(t, err)
	settings, err := y.s.Settings(ctx)
	must(t, err)
	n := store.QuietLandings(settings)
	ready, since, err := dispatch.QuietReady(y.d, ctx, *depot, n)
	must(t, err)
	if since < n {
		t.Fatalf("%d landings counted, want the %d the threshold owes", since, n)
	}
	return ready
}

// waitQuiet waits for the depot's quiet runs to be told of as kind says.
func waitQuiet(t *testing.T, s store.API, kind string, n int) []store.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := s.Subscribe(ctx, 0)
	must(t, err)
	var mine []store.Event
	for e := range ch {
		if e.Kind == kind && e.Data["depot"] == "yard" {
			mine = append(mine, e)
			if len(mine) >= n {
				return mine
			}
		}
	}
	t.Fatalf("waited for %d %s events for the depot, got %d", n, kind, len(mine))
	return nil
}

type quietScanStore struct {
	store.API
	firstScan chan struct{}
	once      sync.Once
}

func (s *quietScanStore) EventsSince(ctx context.Context, after uint64, limit int) ([]store.Event, error) {
	events, err := s.API.EventsSince(ctx, after, limit)
	s.once.Do(func() { close(s.firstScan) })
	return events, err
}

type quietLiveSessionsStore struct {
	store.API
	sawLive chan struct{}
	once    sync.Once
}

func (s *quietLiveSessionsStore) Sessions(ctx context.Context, all bool) ([]store.Session, error) {
	sessions, err := s.API.Sessions(ctx, all)
	if len(sessions) > 0 {
		s.once.Do(func() { close(s.sawLive) })
	}
	return sessions, err
}

func TestQuietPackagesAndRelevance(t *testing.T) {
	out := "ok  \tyardr.dev/yardr/internal/cli\t1s\nFAIL\tyardr.dev/yardr/internal/store\t2s\nFAIL yardr.dev/yardr/internal/web 3s\nFAIL\tyardr.dev/yardr/internal/store\t2s\n"
	want := []string{"yardr.dev/yardr/internal/store", "yardr.dev/yardr/internal/web"}
	if got := dispatch.QuietPackages(out); !slices.Equal(got, want) {
		t.Fatalf("QuietPackages = %v, want %v", got, want)
	}
	for kind, want := range map[string]bool{
		"advanced": true, "closed": true, "session_died": true,
		"held": true, "released": true, "group_removed": true,
		"session_failed": true, "session_aborted": true,
		"claimed": false, "noted": false, "quiet_passed": false,
	} {
		if got := dispatch.QuietRelevant(store.Event{Kind: kind}); got != want {
			t.Errorf("QuietRelevant(%s) = %v, want %v", kind, got, want)
		}
	}
	if !dispatch.QuietRelevant(store.Event{Kind: "setting_changed", Data: map[string]any{"key": store.QuietLandingsSetting}}) {
		t.Error("quiet_landings setting change is not relevant to quiet-run judging")
	}
	if dispatch.QuietRelevant(store.Event{Kind: "setting_changed", Data: map[string]any{"key": store.NameSetting}}) {
		t.Error("unrelated setting change is relevant to quiet-run judging")
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

	waiting, err := y.s.CreateBead(ctx, store.NewBead{Title: "waiting"}, "me")
	must(t, err)
	// This bead has no depot, but the approval queue is yard-wide.
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
	work, err := y.s.CreateBead(ctx, store.NewBead{Title: "work"}, "me")
	must(t, err)
	// Nor does a builder session need to belong to this depot to keep the
	// whole-suite run off a busy machine.
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

// A yard's backlog holds dozens of beads, each claimed by a manual group
// that runs nothing: a person's turn is no load on the machine. Nor is a
// bead the yard at airy works, claimed by a peer group. With only those
// live the yard is quiet and the owed run starts; it never did while every
// live session counted (yardr-hqsn).
func TestQuietIgnoresClaimsThatRunNothingHere(t *testing.T) {
	t.Parallel()
	y := newQuietYard(t)
	y.putGroups(t)
	for range 3 {
		y.claim(t, "backlog")
	}
	y.claim(t, "at-airy")
	y.land(t, "one")
	y.land(t, "two")

	if !y.ready(t) {
		t.Fatal("the yard is busy with only a manual group's and a peer group's claims live")
	}
	dispatch.QuietMaybe(y.d, context.Background())
	waitQuiet(t, y.s, store.QuietPassed, 1)
	if n := y.runCalls(); n != 1 {
		t.Fatalf("calls = %d, want the owed run started", n)
	}
}

// A live session that runs something here, an agent or a script, keeps the
// yard busy, whatever manual claims sit beside it; once it ends, those
// claims alone leave the yard quiet.
func TestQuietWaitsForASessionThatRunsHere(t *testing.T) {
	t.Parallel()
	y := newQuietYard(t)
	y.putGroups(t)
	y.claim(t, "backlog")
	y.land(t, "one")
	y.land(t, "two")

	for _, group := range []string{"builders", "assembly"} {
		sess := y.claim(t, group)
		if y.ready(t) {
			t.Errorf("the yard is quiet with a %s session live", sess.Runner)
		}
		must(t, y.s.EndSession(context.Background(), sess.ID, "failed", "done for the test"))
		if !y.ready(t) {
			t.Errorf("the yard is busy after the %s session ended, with only a manual claim live", sess.Runner)
		}
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
	select {
	case <-y.started:
	case <-time.After(10 * time.Second):
		t.Fatal("quiet run did not start")
	}
	if n := y.runCalls(); n != 1 {
		t.Fatalf("calls = %d, want the run started", n)
	}
	dispatch.QuietMaybe(y.d, ctx)
	dispatch.QuietMaybe(y.d, ctx)
	close(y.block)
	waitQuiet(t, y.s, store.QuietPassed, 1)
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
	watch := &quietScanStore{API: y.s, firstScan: make(chan struct{})}
	y.d.Store = watch
	go dispatch.QuietTrigger(y.d, ctx)
	// The initial pass has read the zero landing count and is returning;
	// subscriptions are already live, so the following moves must wake it.
	select {
	case <-watch.firstScan:
	case <-time.After(10 * time.Second):
		t.Fatal("quiet trigger did not make its initial landing scan")
	}
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

// A hold ends a live session through a bead event, not a session_* event.
// The threshold that waited on that session must rejudge when the event
// arrives, even if nothing else happens in the yard.
func TestQuietTriggerFiresWhenSessionIsHeld(t *testing.T) {
	y := newQuietYard(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	must(t, y.s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake"}, "me"))
	work, err := y.s.CreateBead(ctx, store.NewBead{Title: "work"}, "me")
	must(t, err)
	_, err = y.s.Claim(ctx, work.ID, "builders", "new", "me")
	must(t, err)
	y.land(t, "one")
	y.land(t, "two")

	watch := &quietLiveSessionsStore{API: y.s, sawLive: make(chan struct{})}
	y.d.Store = watch
	go dispatch.QuietTrigger(y.d, ctx)
	select {
	case <-watch.sawLive:
	case <-time.After(10 * time.Second):
		t.Fatal("quiet trigger did not inspect the live session")
	}
	if n := y.runCalls(); n != 0 {
		t.Fatalf("run started while a session was live, calls = %d", n)
	}

	_, err = y.s.SetHold(ctx, work.ID, true, store.Expect{}, "me")
	must(t, err)
	waitQuiet(t, y.s, store.QuietPassed, 1)
	if n := y.runCalls(); n != 1 {
		t.Fatalf("calls = %d, want the held event to fire the pending run", n)
	}
}

func TestQuietThresholdChangeRejudgesThroughEventStream(t *testing.T) {
	y := newQuietYard(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch := &quietScanStore{API: y.s, firstScan: make(chan struct{})}
	y.d.Store = watch
	go dispatch.QuietTrigger(y.d, ctx)
	select {
	case <-watch.firstScan:
	case <-time.After(10 * time.Second):
		t.Fatal("quiet trigger did not make its initial landing scan")
	}
	y.land(t, "one")
	must(t, y.s.SetSetting(ctx, store.QuietLandingsSetting, "1", "me"))
	waitQuiet(t, y.s, store.QuietPassed, 1)
	if n := y.runCalls(); n != 1 {
		t.Fatalf("calls = %d, want the threshold-change event to fire one run", n)
	}
}

// The real run, as a drill: a fixture depot whose base carries a failing
// check at its head commit. The run tests that commit in its own
// worktree, and the yard is told it failed, with the package and commit.
func TestQuietExecRunAgainstARedBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

// The real run with the daemon started under server settings of its own:
// the suite is work the yard starts, so it inherits the process
// environment without those settings (a server a test started from them
// would listen on this yard's addresses), and with the rest untouched.
func TestQuietExecRunWithholdsServerSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YARDR_WEB_ADDR", "127.0.0.1:1")
	t.Setenv("YARDR_HOOK_ADDR", "127.0.0.1:2")
	t.Setenv("QUIET_SENTINEL", "kept")
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
		[]byte("[ -z \"$YARDR_WEB_ADDR\" ] && [ -z \"$YARDR_HOOK_ADDR\" ] && [ \"$QUIET_SENTINEL\" = kept ]\n"), 0o755))
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

// The real run judges R1 strictly: it starts the gate with YARDR_R1_STRICT,
// which the quiet run alone sets, so a package over 60 s alone fails
// however loaded the machine instead of loud-passing as at a landing.
func TestQuietExecRunSetsStrictR1(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
		[]byte("[ \"$YARDR_R1_STRICT\" = 1 ] && [ \"$YARDR_LANDING\" = 1 ] && [ \"$YARDR_RACE_HELD\" = 1 ]\n"), 0o755))
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

// The real run again, on a green base under /tmp as the daemon makes
// its worktrees (no TMPDIR of a test harness): the check compares git's
// resolved toplevel with the directory it runs in, and the run resolves
// its worktree before adding it, so the two name it the same way.
func TestQuietExecRunResolvesItsWorktree(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	userHome := t.TempDir()
	yardHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("YARDR_HOME", yardHome)
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
		[]byte("[ \"$(git rev-parse --show-toplevel)\" = \"$PWD\" ] && [ -f \"$HOME/.yardr/race.lock\" ] && [ ! -e \"$YARDR_HOME/race.lock\" ]\n"), 0o755))
	git("add", "check.sh")
	git("commit", "-m", "a green base")

	s := storetest.New(t)
	must(t, s.PutDepot(ctx, store.Depot{Name: "yard", Path: repo, Kind: store.DepotGit, Base: "main", Test: "check.sh"}, "me"))
	must(t, s.SetSetting(ctx, store.QuietLandingsSetting, "1", "me"))
	d := &dispatch.Dispatcher{Store: s, Log: slog.New(slog.DiscardHandler), Home: yardHome}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "landing", Depot: "yard"}, "me")
	must(t, err)
	_, err = s.Advance(ctx, b.ID, "merged", store.Expect{}, "me")
	must(t, err)
	dispatch.QuietMaybe(d, ctx)
	waitQuiet(t, s, store.QuietPassed, 1)
	lock, err := os.Stat(filepath.Join(userHome, ".yardr", "race.lock"))
	if err != nil || lock.Mode().Perm() != 0o600 {
		t.Fatalf("machine race lock = %v, %v; want mode 0600 under HOME", lock, err)
	}
	if _, err := os.Stat(filepath.Join(yardHome, "race.lock")); !os.IsNotExist(err) {
		t.Fatalf("race lock appeared under the configured yard home: %v", err)
	}
}

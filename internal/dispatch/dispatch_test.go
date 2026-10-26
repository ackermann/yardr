package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestMain(m *testing.M) {
	// The tests run side by side: each owns its state (a store, a home and
	// servers of its own), and what the yards' own git calls see is the
	// same for every test, set here once -- git reads nothing of the
	// developer's. The serial tests are those that set process environment
	// (t.Setenv), which parallel tests cannot share; each says so.
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// The bound a source command gets (source.Timeout, 30 s) is read by every
	// test that runs one, so it is set here once, as the environment is, and
	// not by a test. A script's start alone took longer than the daemon's
	// 10 s under load (see pushFuse), and 30 s is little margin over that:
	// only a hang should cut a command here.
	source.Timeout = pushFuse
	code := m.Run()
	scripttest.Remove()
	os.Exit(code)
}

// fakeRunner records starts and lets tests kill sessions.
type fakeRunner struct {
	mu       sync.Mutex
	started  []string // bead ids
	jobs     []dispatch.Job
	dead     map[string]bool
	stopped  []string
	failNext error
	asked    int // Alive calls
}

func (f *fakeRunner) Start(_ context.Context, j dispatch.Job) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return nil, err
	}
	f.started = append(f.started, j.Bead.ID)
	f.jobs = append(f.jobs, j)
	return map[string]any{"session": j.Session.ID}, nil
}

func (f *fakeRunner) Alive(_ context.Context, h map[string]any) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	return !f.dead[h["session"].(string)], nil
}

func (f *fakeRunner) Stop(_ context.Context, h map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, h["session"].(string))
	return nil
}

func (f *fakeRunner) starts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.started)
}

func setup(t *testing.T) (store.API, *fakeRunner, *dispatch.Dispatcher) {
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f},
		Backoff: time.Nanosecond}
	return s, f, d
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestBeadThroughStages(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 2}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "fake"}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"}, "me")
	must(t, err)

	b, _ := s.CreateBead(ctx, store.NewBead{Title: "feature"}, "me")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx)) // idempotent: no second start
	if got := f.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v", got)
	}
	got, _ := s.GetBead(ctx, b.ID)
	if got.Group == nil || *got.Group != "builders" {
		t.Fatalf("not assigned to builders: %+v", got)
	}

	// The builder hands off; the next pass routes to reviewers and tears
	// down the builder session.
	firstSession := *got.Session
	_, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "builder")
	must(t, err)
	must(t, d.Reconcile(ctx))
	got, _ = s.GetBead(ctx, b.ID)
	if got.Group == nil || *got.Group != "reviewers" {
		t.Fatalf("not routed to reviewers: %+v", got)
	}
	if !slices.Contains(f.stopped, firstSession) {
		t.Fatalf("builder session not stopped: %v", f.stopped)
	}

	// Closing ends it; nothing more is dispatched.
	_, err = s.CloseBead(ctx, b.ID, "merged", store.Expect{}, "reviewer")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if n := len(f.starts()); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
}

// A bead under a flow starts with the stages it may move to and who works
// each, so its brief can list them; a bead without a flow gets none.
func TestJobCarriesNextStages(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutDepot(ctx, store.Depot{Name: "web", Path: "/tmp/web"}, "me"))
	flow, err := store.ParseFlow("new -> review on done | backlog | dropped on abandoned | new on retry max 2 then review\nreview -> merged\ndropped terminal\nmerged terminal\n")
	must(t, err)
	flow.Name = "web"
	_, err = s.PutFlow(ctx, *flow, "me")
	must(t, err)
	must(t, s.SetFlowPointer(ctx, "web", "", "web", "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 3}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual"}, "me"))
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"}, "me")
	must(t, err)
	// A route for a terminal stage is moot: entering it closes the bead.
	_, err = s.AddRoute(ctx, store.Route{Stage: "dropped", Group: "reviewers"}, "me")
	must(t, err)

	inFlow, err := s.CreateBead(ctx, store.NewBead{Title: "flowed", Depot: "web"}, "me")
	must(t, err)
	free, err := s.CreateBead(ctx, store.NewBead{Title: "free"}, "me")
	must(t, err)
	// A bead that has used the rounds its retry allows before it is handed out.
	retried, err := s.CreateBead(ctx, store.NewBead{Title: "retried", Depot: "web"}, "me")
	must(t, err)
	for range 2 {
		_, err = s.Done(ctx, retried.ID, "retry", "", store.Expect{}, "me")
		must(t, err)
	}
	// One start per pass for the group (starts.go): three passes start
	// the three beads, as the daemon's loop is woken per completion.
	for range 3 {
		must(t, d.Reconcile(ctx))
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	jobs := map[string]dispatch.Job{}
	for _, j := range f.jobs {
		jobs[j.Bead.ID] = j
	}
	want := []dispatch.NextStage{
		{Stage: "review", Group: "reviewers", Runner: "manual"},
		{Stage: "backlog"},
		{Stage: "dropped", Terminal: true},
		{Stage: "new", Group: "builders", Runner: "fake"},
	}
	if got := jobs[inFlow.ID].Next; !reflect.DeepEqual(got, want) {
		t.Fatalf("next = %+v, want %+v", got, want)
	}
	// Outcomes carry the same view of where they lead, and for a limited one
	// the round it would be.
	retry := store.Outcome{Name: "retry", To: "new", Max: 2, Then: "review"}
	wantOut := []dispatch.NextOutcome{
		{Outcome: "done", NextStage: want[0]},
		{Outcome: "abandoned", NextStage: want[2]},
		{Outcome: "retry", NextStage: want[3], Limit: &store.OutcomeNow{Outcome: retry, Goes: "new", Round: 1}},
	}
	if got := jobs[inFlow.ID].Outcomes; !reflect.DeepEqual(got, wantOut) {
		t.Fatalf("outcomes = %+v, want %+v", got, wantOut)
	}
	// Once the rounds are used the limit's stage is where it leads, and who
	// picks it up is who works that stage, not the stage it would go back to.
	wantOut[2] = dispatch.NextOutcome{Outcome: "retry", NextStage: want[0], Limit: &store.OutcomeNow{Outcome: retry, Goes: "review", Used: 2}}
	if got := jobs[retried.ID].Outcomes; !reflect.DeepEqual(got, wantOut) {
		t.Fatalf("outcomes of the bead whose rounds are used = %+v, want %+v", got, wantOut)
	}
	if j, ok := jobs[free.ID]; !ok || j.Next != nil || j.Outcomes != nil {
		t.Fatalf("bead without a flow: started %v, next %+v, outcomes %+v", ok, j.Next, j.Outcomes)
	}
}

// A bead whose flow does not resolve is reported once and left where it
// is; it does not stop the pass, so the beads after it still dispatch.
func TestBadFlowSkipsOnlyThatBead(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	must(t, os.MkdirAll(filepath.Join(repo, ".yardr/flows"), 0o755))
	must(t, os.WriteFile(filepath.Join(repo, ".yardr/flows/alt.flow"), []byte("new -> review\n"), 0o644))
	git("add", "-A")
	git("commit", "-q", "-m", "alt flow")
	must(t, s.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)

	// Pinned to a repo flow that then leaves the base: the pin names nothing.
	bad, err := s.CreateBead(ctx, store.NewBead{Title: "pinned", Depot: "web", Metadata: map[string]any{"flow": "alt"}}, "me")
	must(t, err)
	git("rm", "-q", ".yardr/flows/alt.flow")
	git("commit", "-q", "-m", "drop alt")
	good, err := s.CreateBead(ctx, store.NewBead{Title: "free"}, "me")
	must(t, err)

	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{good.ID}) {
		t.Fatalf("starts = %v, want only %s", got, good.ID)
	}
	if got, _ := s.GetBead(ctx, bad.ID); got.Group != nil || got.Stage != "new" || got.Hold {
		t.Fatalf("bad bead moved: %+v", got)
	}
	evs, err := s.Events(ctx, bad.ID, 0)
	must(t, err)
	var reports []store.Event
	for _, e := range evs {
		if e.Kind == "bad_flow" {
			reports = append(reports, e)
		}
	}
	if len(reports) != 1 {
		t.Fatalf("bad_flow events = %+v, want exactly one", reports)
	}
	if r, _ := reports[0].Data["reason"].(string); !strings.Contains(r, "pins flow alt") {
		t.Fatalf("bad_flow reason = %q", r)
	}
}

func TestLimitsBlockersAndRouteSpecificity(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "general", Runner: "fake", Limit: 1}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "bugs", Runner: "fake", Limit: 5}, "me"))
	bug := "bug"
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "general"}, "me")
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Type: &bug, Group: "bugs"}, "me")

	t1, _ := s.CreateBead(ctx, store.NewBead{Title: "t1"}, "me")
	t2, _ := s.CreateBead(ctx, store.NewBead{Title: "t2"}, "me")
	b1, _ := s.CreateBead(ctx, store.NewBead{Title: "b1", Type: "bug"}, "me")
	b2, _ := s.CreateBead(ctx, store.NewBead{Title: "b2", Type: "bug"}, "me")
	must(t, s.AddDep(ctx, b1.ID, b2.ID, "blocks", "", "", "me"))

	must(t, d.Reconcile(ctx))
	// general has limit 1: only t1; bugs: b1 (b2 is blocked by b1).
	// The two groups start concurrently, so their global order is not part
	// of this test's contract.
	got := f.starts()
	slices.Sort(got)
	want := []string{t1.ID, b1.ID}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("starts = %v", got)
	}
	_, _ = s.CloseBead(ctx, b1.ID, "done", store.Expect{}, "me")
	_, _ = s.CloseBead(ctx, t1.ID, "done", store.Expect{}, "me")
	must(t, d.Reconcile(ctx))
	got = f.starts()
	slices.Sort(got)
	want = []string{t1.ID, b1.ID, t2.ID, b2.ID}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("starts after closes = %v", got)
	}
}

func TestDeadSessionIsRetriedThenHeld(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	d.MaxAttempts = 2
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "flaky"}, "me")

	// Attempt 1 dies.
	must(t, d.Reconcile(ctx))
	got, _ := s.GetBead(ctx, b.ID)
	f.dead[*got.Session] = true
	must(t, d.Reconcile(ctx)) // reaps, then re-dispatches (attempt 2)
	if n := len(f.starts()); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
	// Attempt 2 dies too: with MaxAttempts=2 the bead is held for a human.
	got, _ = s.GetBead(ctx, b.ID)
	f.dead[*got.Session] = true
	must(t, d.Reconcile(ctx))
	got, _ = s.GetBead(ctx, b.ID)
	if !got.Hold || got.HeldBy != "yard" || got.Group != nil {
		t.Fatalf("bead not held by the yard after %d failures: %+v", d.MaxAttempts, got)
	}
	evs, _ := s.Events(ctx, b.ID, 100)
	if evs[len(evs)-1].Kind != "gave_up" {
		t.Fatalf("last event = %s", evs[len(evs)-1].Kind)
	}

	// A human un-holds: attempts reset, so one failed start is retried.
	time.Sleep(10 * time.Millisecond)
	_, err := s.SetHold(ctx, b.ID, false, store.Expect{}, "human")
	must(t, err)
	f.failNext = errors.New("boom")
	must(t, d.Reconcile(ctx)) // start fails (attempt 1 since unhold)
	must(t, d.Reconcile(ctx)) // retried
	got, _ = s.GetBead(ctx, b.ID)
	if got.Group == nil || got.Hold || got.HeldBy != "" {
		t.Fatalf("not re-dispatched after unhold: %+v", got)
	}
	if n := len(f.starts()); n != 3 {
		t.Fatalf("successful starts = %d, want 3", n)
	}
}

func TestUnroutedIsLoggedOnce(t *testing.T) {
	t.Parallel()
	s, _, d := setup(t)
	ctx := context.Background()
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "orphan", Stage: "limbo"}, "me")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	evs, _ := s.Events(ctx, b.ID, 100)
	n := 0
	for _, e := range evs {
		if e.Kind == "unrouted" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("unrouted events = %d", n)
	}
}

// A question (the bead of bead ask --new) is nobody's to build: a route
// that takes every bead of its stage starts nothing on it, and with no
// route it is not unrouted. A task beside it is both (yardr-c9z6).
func TestQuestionIsNeverRouted(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.SetSetting(ctx, "operator", "mckean", "me"))
	ask := store.Ask{Question: "Rename 'depot'?", To: "mckean", Context: []string{"The glossary keeps it."}}
	kinds := func(id string) []string {
		t.Helper()
		evs, err := s.Events(ctx, id, 0)
		must(t, err)
		var out []string
		for _, e := range evs {
			out = append(out, e.Kind)
		}
		return out
	}

	asked, err := s.AskNew(ctx, store.NewBead{}, ask, "yardmaster")
	must(t, err)
	task, err := s.CreateBead(ctx, store.NewBead{Title: "a task"}, "yardmaster")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := kinds(asked.Bead.ID); slices.Contains(got, "unrouted") {
		t.Fatalf("the question's events = %v, want no unrouted", got)
	}
	if got := kinds(task.ID); !slices.Contains(got, "unrouted") {
		t.Fatalf("the task's events = %v, want unrouted: no route leads from new", got)
	}

	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{task.ID}) {
		t.Fatalf("starts = %v, want the task alone", got)
	}
	// Answered, it is still nobody's to build: its asker closes it.
	must(t, s.AddAnswer(ctx, asked.Bead.ID, store.Answer{Ask: asked.Ask, Words: "keep it"}, "mckean"))
	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 1 {
		t.Fatalf("starts after the answer = %v, want the task alone", got)
	}
	if got, _ := s.GetBead(ctx, asked.Bead.ID); got.Group != nil || got.Status != "open" || got.Stage != "new" {
		t.Fatalf("the question after the answer = %+v", got)
	}
	_, err = s.CloseBead(ctx, asked.Bead.ID, "kept the word", store.Expect{}, "yardmaster")
	must(t, err)

	// One the caller placed in a stage is a task there, routed as any.
	placed, err := s.AskNew(ctx, store.NewBead{Stage: "new"}, ask, "yardmaster")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{task.ID, placed.Bead.ID}) || placed.Bead.Type != "task" {
		t.Fatalf("starts = %v, the placed bead a %s; want it started as a task", got, placed.Bead.Type)
	}
}

func TestRunWakesOnChange(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	d.Interval = time.Hour // only live notifications can trigger the pass
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	done := make(chan error)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)

	b, _ := s.CreateBead(ctx, store.NewBead{Title: "push"}, "me")
	deadline := time.Now().Add(3 * time.Second)
	for len(f.starts()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts = %v; live wake did not dispatch", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMatchOrder(t *testing.T) {
	t.Parallel()
	bug, web, p0 := "bug", "web", 0
	rs := []store.Route{
		{ID: "a", Stage: "new", Group: "any"},
		{ID: "b", Stage: "new", Type: &bug, Group: "bugs"},
		{ID: "c", Stage: "new", Type: &bug, Depot: &web, Group: "webbugs"},
		{ID: "d", Stage: "new", BeadPriority: &p0, Group: "urgent"},
	}
	dispatch.SortRoutes(rs)
	b := store.Bead{Stage: "new", Type: "bug", Depot: &web}
	if r := dispatch.Match(rs, b, "", nil); r.Group != "urgent" {
		t.Fatalf("urgency should win, got %s", r.Group)
	}
	rs = rs[1:] // drop urgent (sorted first)
	if r := dispatch.Match(rs, b, "", nil); r.Group != "webbugs" {
		t.Fatalf("most specific should win, got %s", r.Group)
	}
	if r := dispatch.Match(rs, store.Bead{Stage: "new", Type: "task"}, "", nil); r.Group != "any" {
		t.Fatalf("fallback should match, got %s", r.Group)
	}
	if r := dispatch.Match(rs, store.Bead{Stage: "other"}, "", nil); r != nil {
		t.Fatalf("wrong stage matched %s", r.Group)
	}
}

// statusRunner is a fakeRunner that also reports agent status.
type statusRunner struct {
	*fakeRunner
	status string
}

func (s *statusRunner) Status(context.Context, map[string]any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, nil
}

func TestBlockedSessionIsReportedOnce(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	r := &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")

	must(t, d.Reconcile(ctx)) // start
	r.status = "blocked"
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r.status = "working"
	must(t, d.Reconcile(ctx))
	r.status = "" // agent gone
	must(t, d.Reconcile(ctx))

	evs, _ := s.Events(ctx, b.ID, 100)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"created", "claimed", "started", "session_blocked", "session_unblocked", "session_died"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
}

// A dispatcher's memory of a session's status ends with the daemon. A new
// one takes the bead's events for it: a session that left its prompt
// meanwhile is reported unblocked, once, and one still at its prompt is not
// reported again.
func TestBlockedSessionAcrossRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, status string
		want         []string
	}{
		{"left its prompt", "working", []string{"created", "claimed", "started", "session_blocked", "session_unblocked"}},
		{"still blocked", "blocked", []string{"created", "claimed", "started", "session_blocked"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.New(t)
			r := &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}
			runners := map[string]dispatch.Runner{"fake": r}
			d := &dispatch.Dispatcher{Store: s, Runners: runners}
			ctx := context.Background()
			must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
			_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
			b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")

			must(t, d.Reconcile(ctx)) // start
			r.status = "blocked"
			must(t, d.Reconcile(ctx))

			r.status = tc.status
			d = &dispatch.Dispatcher{Store: s, Runners: runners} // the daemon restarted
			must(t, d.Reconcile(ctx))
			must(t, d.Reconcile(ctx))

			evs, _ := s.Events(ctx, b.ID, 100)
			var kinds []string
			for _, e := range evs {
				kinds = append(kinds, e.Kind)
			}
			if !slices.Equal(kinds, tc.want) {
				t.Fatalf("events = %v, want %v", kinds, tc.want)
			}
		})
	}
}

// finishRunner is a fakeRunner that leaves a "worktree" behind.
type finishRunner struct {
	*fakeRunner
	finished []string
}

func (f *finishRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	h, err := f.fakeRunner.Start(ctx, j)
	if h != nil {
		h["worktree"] = "/wt/" + j.Bead.ID
	}
	return h, err
}

func (f *finishRunner) Finish(_ context.Context, h map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if slices.Contains(f.finished, h["worktree"].(string)) {
		return dispatch.ErrNothingLeft
	}
	f.finished = append(f.finished, h["worktree"].(string))
	return nil
}

func TestWorkspaceCleanedWhenBeadClosesLater(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	agents := &finishRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}}
	human := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"agents": agents, "human": human}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "agents"}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "me", Runner: "human"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	_, _ = s.AddRoute(ctx, store.Route{Stage: "fix", Group: "builders"}, "me")
	_, _ = s.AddRoute(ctx, store.Route{Stage: "review", Group: "me"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")

	// build -> review -> back to fix -> review again
	must(t, d.Reconcile(ctx))
	for _, stage := range []string{"review", "fix", "review"} {
		_, err := s.Advance(ctx, b.ID, stage, store.Expect{}, "someone")
		must(t, err)
		must(t, d.Reconcile(ctx))
	}
	if len(agents.finished) != 0 {
		t.Fatalf("cleaned up while the bead is open: %v", agents.finished)
	}
	// The human closes it; the builders' worktree goes, once.
	_, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if !slices.Equal(agents.finished, []string{"/wt/" + b.ID}) {
		t.Fatalf("finished = %v", agents.finished)
	}
	evs, _ := s.Events(ctx, b.ID, 100)
	n := 0
	for _, e := range evs {
		if e.Kind == "workspace_removed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("workspace_removed events = %d, want 1", n)
	}
}

// A session is judged by the runner that started it, even after its group
// switches runners; new work then goes to the new runner.
func TestSessionKeepsItsRunnerWhenGroupSwitches(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	old := &fakeRunner{dead: map[string]bool{}}
	nu := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"old": old, "new": nu},
		Backoff: time.Nanosecond}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "old", Limit: 5}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	a, _ := s.CreateBead(ctx, store.NewBead{Title: "a"}, "me")
	must(t, d.Reconcile(ctx)) // a starts on old

	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "new", Limit: 5}, "me"))
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "b"}, "me")
	must(t, d.Reconcile(ctx))
	if !slices.Equal(old.starts(), []string{a.ID}) || !slices.Equal(nu.starts(), []string{b.ID}) {
		t.Fatalf("starts: old %v, new %v", old.starts(), nu.starts())
	}
	sess, _ := s.Sessions(ctx, false)
	if len(sess) != 2 {
		t.Fatalf("live sessions = %d", len(sess))
	}
	if old.asked == 0 {
		t.Fatal("the old runner was never asked about its session")
	}
	// The old runner, not the new one, decides that a's session ended.
	got, _ := s.GetBead(ctx, a.ID)
	old.dead[*got.Session] = true
	must(t, d.Reconcile(ctx))
	if live, _ := s.Sessions(ctx, false); len(live) != 2 { // a died, then was re-dispatched on new
		t.Fatalf("live sessions after a died = %d", len(live))
	}
	if !slices.Equal(nu.starts(), []string{b.ID, a.ID}) {
		t.Fatalf("a was not re-dispatched to the new runner: %v", nu.starts())
	}
}

// Deleting a group must not strand the beads its sessions hold: once its
// route is gone, nothing would ever claim them again (yard-30).
func TestGroupRmReleasesClaimedBeadsToTheNewRoute(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "approvers", Runner: "fake"}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "assembly", Runner: "fake"}, "me"))
	r, err := s.AddRoute(ctx, store.Route{Stage: "approved", Group: "approvers"}, "me")
	must(t, err)
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "ship it", Stage: "approved"}, "me")
	must(t, d.Reconcile(ctx))
	got, _ := s.GetBead(ctx, b.ID)
	if got.Group == nil || *got.Group != "approvers" {
		t.Fatalf("not claimed by approvers: %+v", got)
	}
	old := *got.Session

	// Re-route: the claimed bead keeps its group.
	must(t, s.DeleteRoute(ctx, r.ID, "me"))
	_, err = s.AddRoute(ctx, store.Route{Stage: "approved", Group: "assembly"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got, _ := s.GetBead(ctx, b.ID); got.Group == nil || *got.Group != "approvers" {
		t.Fatalf("re-route took a claimed bead: %+v", got)
	}

	n, err := s.DeleteGroup(ctx, "approvers", "yardmaster")
	must(t, err)
	if n != 1 {
		t.Fatalf("released = %d, want 1", n)
	}
	must(t, d.Reconcile(ctx))
	got, _ = s.GetBead(ctx, b.ID)
	if got.Group == nil || *got.Group != "assembly" {
		t.Fatalf("not routed to assembly after group rm: %+v", got)
	}
	if !slices.Contains(f.stopped, old) {
		t.Fatalf("deleted group's session not stopped: %v", f.stopped)
	}
	if fails, _ := s.FailureCount(ctx, b.ID, "approved"); fails != 0 {
		t.Fatalf("group rm counted as %d failure(s)", fails)
	}
}

// A bead claims through a templated default to its depot's resolved group,
// and the claimed event names that group and the route; a depot without the
// group claims nothing.
func TestClaimThroughTemplatedDefaultNamesResolvedGroup(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	for _, depot := range []string{"web", "other", "third"} {
		must(t, s.PutDepot(ctx, store.Depot{Name: depot}, "me"))
	}
	must(t, s.PutGroup(ctx, store.Group{Name: "web-assembly", Runner: "fake"}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "other-assembly", Runner: "fake"}, "me"))
	r, err := s.AddRoute(ctx, store.Route{Stage: "approved", Group: "{depot}-assembly"}, "me")
	must(t, err)
	web, _ := s.CreateBead(ctx, store.NewBead{Title: "web lands", Depot: "web", Stage: "approved"}, "me")
	other, _ := s.CreateBead(ctx, store.NewBead{Title: "other lands", Depot: "other", Stage: "approved"}, "me")
	third, _ := s.CreateBead(ctx, store.NewBead{Title: "third waits", Depot: "third", Stage: "approved"}, "me")
	must(t, d.Reconcile(ctx))
	for _, tc := range []struct {
		bead, group string
	}{
		{web.ID, "web-assembly"},
		{other.ID, "other-assembly"},
	} {
		group, data := claimedBy(t, s, tc.bead)
		if group != tc.group || data["group"] != tc.group || data["route"] != r.ID {
			t.Errorf("bead %s claimed by %q with %v, want %s by route %s", tc.bead, group, data, tc.group, r.ID)
		}
	}
	if group, _ := claimedBy(t, s, third.ID); group != "" {
		t.Errorf("bead %s claimed by %q, want nobody: no group third-assembly", third.ID, group)
	}
	if got := f.starts(); len(got) != 2 {
		t.Errorf("starts = %v, want the two beads with groups", got)
	}
}

// pruneCounter records the retentions the dispatcher prunes with.
type pruneCounter struct {
	store.API
	mu    sync.Mutex
	calls []store.Retention
}

func (p *pruneCounter) Prune(ctx context.Context, keep store.Retention, actor string) (*store.PruneResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, keep)
	p.mu.Unlock()
	return p.API.Prune(ctx, keep, actor)
}

func (p *pruneCounter) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// The daemon prunes on start and every PruneEvery, and not at all without
// a retention.
func TestRunPrunesOnATimer(t *testing.T) {
	t.Parallel()
	for _, keep := range []store.Retention{{}, store.DefaultRetention} {
		s, _, d := setup(t)
		pc := &pruneCounter{API: s}
		d.Store, d.Retention, d.PruneEvery, d.Interval = pc, keep, 50*time.Millisecond, time.Hour
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error)
		go func() { done <- d.Run(ctx) }()
		if keep == (store.Retention{}) {
			// Nothing to wait for: a window in which a prune would have
			// shown. Load can only make it miss one, never report one.
			time.Sleep(300 * time.Millisecond)
		} else {
			// The second prune is the timer's. How long the daemon takes to
			// reach it is the machine's business (a slow disk, a loaded one:
			// yardr-km55), so the test waits for the count, not for a time.
			for deadline := time.Now().Add(time.Minute); pc.count() < 2 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			}
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		n := pc.count()
		if keep == (store.Retention{}) && n != 0 {
			t.Fatalf("pruned %d times without a retention", n)
		}
		if keep != (store.Retention{}) && (n < 2 || pc.calls[0] != keep) {
			t.Fatalf("pruned %d times with %+v, want at least 2 with %+v", n, pc.calls, keep)
		}
	}
}

// The job tells the runner a child's train and base; any other bead gets the
// depot's base. A bead whose train is ambiguous is not started.
func TestJobCarriesTrainAndBase(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	repo := t.TempDir()
	cmd := exec.Command("git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "A")
	must(t, exec.Command("git", "init", "-q", "-b", "main", repo).Run())
	must(t, cmd.Run())
	must(t, s.PutDepot(ctx, store.Depot{Name: "r", Path: repo, Base: "main"}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	for _, nb := range []store.NewBead{
		{ID: "r-e", Title: "train", Type: store.TypeTrain, Depot: "r", Stage: "backlog"},
		{ID: "r-1", Title: "child", Depot: "r"},
		{ID: "r-2", Title: "plain", Depot: "r"},
	} {
		_, err := s.CreateBead(ctx, nb, "me")
		must(t, err)
	}
	must(t, s.AddDep(ctx, "r-e", "r-1", store.DepParent, "", "", "me"))
	// r-3 ends up a child of two trains: r-e's type changes away and back
	// around a second parent edge.
	_, err = s.CreateBead(ctx, store.NewBead{ID: "r-g", Title: "train", Type: store.TypeTrain, Depot: "r", Stage: "backlog"}, "me")
	must(t, err)
	_, err = s.CreateBead(ctx, store.NewBead{ID: "r-3", Title: "two trains", Depot: "r"}, "me")
	must(t, err)
	must(t, s.AddDep(ctx, "r-e", "r-3", store.DepParent, "", "", "me"))
	task, train := "task", store.TypeTrain
	_, err = s.UpdateBead(ctx, "r-e", store.BeadPatch{Type: &task}, store.Expect{}, "me")
	must(t, err)
	must(t, s.AddDep(ctx, "r-g", "r-3", store.DepParent, "", "", "me"))
	_, err = s.UpdateBead(ctx, "r-e", store.BeadPatch{Type: &train}, store.Expect{}, "me")
	must(t, err)

	// One start per pass for the group (starts.go): the third pass is
	// the first to reach r-3, behind the two starts before it.
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if evs := eventKinds(t, s, "r-3", "bad_train"); len(evs) != 1 {
		t.Fatalf("bad_train events for r-3 = %+v, want one", evs)
	}
	got := map[string][2]string{}
	for _, j := range f.jobs {
		got[j.Bead.ID] = [2]string{j.Train, j.Base}
	}
	if want := map[string][2]string{"r-1": {"r-e", "yard/r-e"}, "r-2": {"", "main"}}; !maps.Equal(got, want) {
		t.Fatalf("jobs (train, base) = %v, want %v", got, want)
	}
}

// A repository flow that loses a stage on the base strands the open beads
// in it. The stage's route is still there, so the bead used to be claimed
// as before and its agent offered the flow's first stage as the only way on:
// it rejoined the flow there without anyone deciding so. Now it is reported
// once, claimed by nobody, and routed again once someone advances it to a
// stage the flow has.
func TestStrandedBeadIsReportedOnceAndNotRouted(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	flow := func(text, msg string) {
		t.Helper()
		must(t, os.MkdirAll(filepath.Join(repo, ".yardr/flows"), 0o755))
		must(t, os.WriteFile(filepath.Join(repo, ".yardr/flows/default.flow"), []byte(text), 0o644))
		git("add", "-A")
		git("commit", "-q", "-m", msg)
	}
	git("init", "-q", "-b", "main")
	flow("new -> triage | review\ntriage -> review\nreview -> merged\nmerged terminal\n", "flow with triage")
	must(t, s.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	for _, st := range []string{"new", "triage"} {
		_, err := s.AddRoute(ctx, store.Route{Stage: st, Group: "builders"}, "me")
		must(t, err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "in triage", Depot: "web", Stage: "triage"}, "me")
	must(t, err)

	flow("new -> review\nreview -> merged\nmerged terminal\n", "drop triage")
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if got := f.starts(); len(got) != 0 {
		t.Fatalf("starts = %v, want none: the bead's stage is not in its flow", got)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Group != nil || got.Stage != "triage" || got.Hold {
		t.Fatalf("stranded bead changed: stage %s group %v hold %v", got.Stage, got.Group, got.Hold)
	}
	evs, err := s.Events(ctx, b.ID, 0)
	must(t, err)
	var reports []store.Event
	for _, e := range evs {
		if e.Kind == "stranded" {
			reports = append(reports, e)
		}
		if e.Kind == "unrouted" || e.Kind == "bad_flow" {
			t.Errorf("stranded bead also reported as %s: %v", e.Kind, e.Data)
		}
	}
	if len(reports) != 1 {
		t.Fatalf("stranded events = %+v, want exactly one over three passes", reports)
	}
	if data := reports[0].Data; data["stage"] != "triage" || data["flow"] != "default" || !strings.HasPrefix(fmt.Sprint(data["source"]), "repo@") {
		t.Fatalf("stranded event data = %v, want stage triage, flow default, source repo@<commit>", data)
	}

	// The base moving on without touching the flow's stages says nothing new.
	must(t, os.WriteFile(filepath.Join(repo, "README"), []byte("x"), 0o644))
	git("add", "-A")
	git("commit", "-q", "-m", "unrelated")
	must(t, d.Reconcile(ctx))
	if evs, _ = s.Events(ctx, b.ID, 0); len(evs) == 0 {
		t.Fatal("no events")
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "stranded" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("stranded events after an unrelated base commit = %d, want 1", n)
	}

	// Someone advances it to a stage the flow has: it is routed again.
	_, err = s.Advance(ctx, b.ID, "new", store.Expect{}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts after advancing to new = %v, want %s", got, b.ID)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Group == nil || *got.Group != "builders" || got.Stage != "new" {
		t.Fatalf("advanced bead: stage %s group %v", got.Stage, got.Group)
	}
}

// A job carries what its brief needs for the stage file: a repository
// flow's file as read from the base, or the name of the store flow, whose
// stage files are in the yard's home. It carries the map of the flow too,
// drawn as the bead is handed out; a bead no flow governs has none.
func TestJobCarriesTheStageFile(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	for name, text := range map[string]string{
		".yardr/flows/default.flow":   "new -> review\nreview -> merged\nmerged terminal\n",
		".yardr/flows/default/new.md": "Build it.\n",
		".yardr/flows/code/new.md":    "not the store flow's\n",
	} {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755))
		must(t, os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644))
	}
	git("add", "-A")
	git("commit", "-q", "-m", "flow")
	must(t, s.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "me"))
	must(t, s.PutDepot(ctx, store.Depot{Name: "api", Path: repo, Base: "main"}, "me"))
	code, err := store.ParseFlow("new -> shipped\nshipped terminal\n")
	must(t, err)
	code.Name = "code"
	_, err = s.PutFlow(ctx, *code, "me")
	must(t, err)
	must(t, s.SetFlowPointer(ctx, "api", "", "code", "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	inRepo, err := s.CreateBead(ctx, store.NewBead{Title: "repo flow", Depot: "web"}, "me")
	must(t, err)
	inStore, err := s.CreateBead(ctx, store.NewBead{Title: "store flow", Depot: "api"}, "me")
	must(t, err)
	none, err := s.CreateBead(ctx, store.NewBead{Title: "no flow"}, "me")
	must(t, err)
	for range 3 { // one start per pass for the group (starts.go)
		must(t, d.Reconcile(ctx))
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	jobs := map[string]dispatch.Job{}
	for _, j := range f.jobs {
		jobs[j.Bead.ID] = j
	}
	if j := jobs[inRepo.ID]; j.StageFile == nil || j.StageFile.Text != "Build it.\n" || j.StoreFlow != "" {
		t.Errorf("repository flow: stage file %+v, store flow %q", j.StageFile, j.StoreFlow)
	}
	if j := jobs[inStore.ID]; j.StageFile != nil || j.StoreFlow != "code" {
		t.Errorf("store flow: stage file %+v, store flow %q", j.StageFile, j.StoreFlow)
	}
	if j := jobs[none.ID]; j.StageFile != nil || j.StoreFlow != "" {
		t.Errorf("no flow: stage file %+v, store flow %q", j.StageFile, j.StoreFlow)
	}
	if m := jobs[inRepo.ID].FlowMap; m == nil || m.Name != "default" || len(m.Stages) != 3 || !m.Stages[0].Current || m.Stages[0].Summary != "Build it." || len(m.Path) != 0 {
		t.Errorf("repository flow: map %+v", m)
	}
	if m := jobs[inStore.ID].FlowMap; m == nil || m.Name != "code" || len(m.Stages) != 2 {
		t.Errorf("store flow: map %+v", m)
	}
	if m := jobs[none.ID].FlowMap; m != nil {
		t.Errorf("no flow: map %+v", m)
	}
	if len(jobs) != 3 {
		t.Errorf("jobs = %d, want 3", len(jobs))
	}
}

// A pass over a store with no writable beads does not start another job.
func TestPassDoesNotStartAnotherJobAtLimit(t *testing.T) {
	t.Parallel()
	s, _ := storetest.NewOwned(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}, Backoff: time.Nanosecond}
	ctx := context.Background()
	must(t, s.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotDir, Path: t.TempDir()}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 1}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	const open = 12
	for i := range open - 2 {
		_, err := s.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i), Body: "what to do", Depot: "web"}, "me")
		must(t, err)
	}
	later := time.Now().Add(time.Hour)
	for _, at := range []*time.Time{nil, &later} {
		_, err := s.Remember(ctx, store.NewMemory{Title: "a memory", Scope: "all", Expires: at}, "me")
		must(t, err)
	}
	// The group takes one bead, and is at its limit from then on.
	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 1 {
		t.Fatalf("starts = %v, want one", got)
	}
	for range 2 {
		must(t, d.Reconcile(ctx))
	}
	if got := f.starts(); len(got) != 1 {
		t.Fatalf("starts = %v, want still one: the group is at its limit", got)
	}
}

// The pass decides on what Dispatchable says of a bead and reads the bead
// when it starts it: the runner is handed all of it, with its train.
func TestStartedBeadIsReadWhole(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotDir, Path: t.TempDir()}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	train, err := s.CreateBead(ctx, store.NewBead{Title: "a train", Type: store.TypeTrain, Depot: "web", Stage: "open"}, "me")
	must(t, err)
	wagon, err := s.CreateBead(ctx, store.NewBead{Title: "a wagon", Body: "what to do", Depot: "web",
		Labels: []string{"ui"}, Metadata: map[string]any{"model": "big", "note": "kept"}}, "me")
	must(t, err)
	must(t, s.AddDep(ctx, train.ID, wagon.ID, store.DepParent, "", "", "me"))
	must(t, d.Reconcile(ctx))
	if len(f.jobs) != 1 {
		t.Fatalf("starts = %v, want the wagon", f.starts())
	}
	want, err := s.GetBead(ctx, wagon.ID)
	must(t, err)
	got := f.jobs[0].Bead
	if got.ID != wagon.ID || got.Title != "a wagon" || got.Body != "what to do" || got.UUID != want.UUID ||
		!slices.Equal(got.Labels, []string{"ui"}) || got.Metadata["note"] != "kept" || got.Metadata["model"] != "big" ||
		got.Train != train.ID || f.jobs[0].Train != train.ID || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("the bead the runner was handed = %+v\nwant %+v with train %s", got, *want, train.ID)
	}
}

// otherBuild answers Dispatchable as a server from before ReadyBead does to
// a daemon of this build: its beads, whose handle this build does not find.
type otherBuild struct{ store.API }

func (o otherBuild) Dispatchable(ctx context.Context) ([]store.ReadyBead, error) {
	rs, err := o.API.Dispatchable(ctx)
	for i := range rs {
		rs[i].Bead = ""
	}
	return rs, err
}

// A daemon whose server is still the build before it starts nothing from an
// answer it cannot read, and says so, instead of claiming beads by no handle.
func TestPassRefusesAnotherBuildsReadyBeads(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 2}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "feature"}, "me")
	must(t, err)
	d.Store = otherBuild{s}
	if err := d.Reconcile(ctx); err == nil || !strings.Contains(err.Error(), "another build") {
		t.Fatalf("pass against another build's server = %v, want it refused as such", err)
	}
	if got := f.starts(); len(got) != 0 {
		t.Fatalf("starts = %v, want none", got)
	}
	d.Store = s
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("starts once the server is this build = %v, want %s", got, b.ID)
	}
}

// A route for urgent beads and a route for two labels take the ready beads
// they are for: the dispatcher is handed each bead's priority and labels.
func TestRouteByBeadPriorityAndLabels(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	for _, g := range []string{"general", "reserve", "heavy"} {
		must(t, s.PutGroup(ctx, store.Group{Name: g, Runner: "fake", Limit: 5}, "me"))
	}
	hard, one := "difficulty:3", 1
	for _, r := range []store.Route{
		{Stage: "new", Group: "general"},
		{Stage: "new", Group: "general", Label: &hard},
		{Stage: "new", Group: "heavy", Label: &hard, AndLabels: []string{"size:large"}},
		{Stage: "new", Group: "reserve", BeadPriority: &one},
	} {
		_, err := s.AddRoute(ctx, r, "me")
		must(t, err)
	}
	want := map[string]string{}
	for _, c := range []struct {
		nb    store.NewBead
		group string
	}{
		{store.NewBead{Title: "p0", Priority: 0}, "reserve"},
		{store.NewBead{Title: "p1", Priority: 1, Labels: []string{hard, "size:large"}}, "reserve"},
		{store.NewBead{Title: "p2", Priority: 2}, "general"},
		{store.NewBead{Title: "hard", Priority: 2, Labels: []string{hard}}, "general"},
		{store.NewBead{Title: "hard and large", Priority: 3, Labels: []string{"size:large", "x", hard}}, "heavy"},
	} {
		b, err := s.CreateBead(ctx, c.nb, "me")
		must(t, err)
		want[b.ID] = c.group
	}
	for range 2 { // one start per group per pass (starts.go)
		must(t, d.Reconcile(ctx))
	}
	if got := f.starts(); len(got) != len(want) {
		t.Fatalf("starts = %v, want every bead", got)
	}
	for id, group := range want {
		b, err := s.GetBead(ctx, id)
		must(t, err)
		if b.Group == nil || *b.Group != group {
			t.Errorf("bead %s (P%d, %v) is with %v, want %s", b.Title, b.Priority, b.Labels, b.Group, group)
		}
	}
}

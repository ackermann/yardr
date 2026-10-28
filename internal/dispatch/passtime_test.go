package dispatch_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// passClock is the dispatcher's clock for a test of the pass's timing: it
// moves only when something the pass calls spends time on it, so a step is
// slow by what the test charges it and the test sleeps for none of it.
type passClock struct {
	epoch time.Time
	spent atomic.Int64 // nanoseconds the clock has moved

	mu    sync.Mutex
	costs map[string]time.Duration // call -> what charge spends for it
}

func (c *passClock) now() time.Time { return c.epoch.Add(time.Duration(c.spent.Load())) }

// price sets what the next calls named call spend; zero is fast again.
func (c *passClock) price(call string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.costs == nil {
		c.costs = map[string]time.Duration{}
	}
	c.costs[call] = d
}

// charge spends call's price: the call took that long.
func (c *passClock) charge(call string) {
	c.mu.Lock()
	d := c.costs[call]
	c.mu.Unlock()
	c.spent.Add(int64(d))
}

// chargedRunner is the fake runner with a liveness answer that takes time
// and a handle that carries something to leak.
type chargedRunner struct {
	*fakeRunner
	clock *passClock
}

func (r *chargedRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	h, err := r.fakeRunner.Start(ctx, j)
	if h != nil {
		h["pane"] = leakHandle
	}
	return h, err
}

func (r *chargedRunner) Alive(ctx context.Context, h map[string]any) (bool, error) {
	r.clock.charge("alive")
	return r.fakeRunner.Alive(ctx, h)
}

// chargedStore is a store whose session list, flow lookups and claim take
// time, and whose session list can fail.
type chargedStore struct {
	store.API
	clock        *passClock
	sessionsFail atomic.Bool
}

func (s *chargedStore) Sessions(ctx context.Context, all bool) ([]store.Session, error) {
	if s.sessionsFail.Load() {
		s.clock.charge("sessions")
		return nil, errors.New("the store did not answer")
	}
	return s.API.Sessions(ctx, all)
}

func (s *chargedStore) Stranded(ctx context.Context, depot string) ([]store.StrandedBead, error) {
	s.clock.charge("stranded")
	return s.API.Stranded(ctx, depot)
}

func (s *chargedStore) NextStages(ctx context.Context, id string) (*store.NextStages, error) {
	s.clock.charge("next_stages")
	return s.API.NextStages(ctx, id)
}

func (s *chargedStore) FlowMap(ctx context.Context, id string) (*store.FlowMap, error) {
	s.clock.charge("flow_map")
	return s.API.FlowMap(ctx, id)
}

func (s *chargedStore) Claim(ctx context.Context, bead, group, stage, actor string, via ...store.Routed) (*store.Session, error) {
	s.clock.charge("claim")
	return s.API.Claim(ctx, bead, group, stage, actor, via...)
}

// What the pass handles and must not put in the line that says where its
// time went: a bead's title and body, a session's handle and a group's
// credential.
const (
	leakTitle  = "title-6c1e47"
	leakBody   = "body-b92f03"
	leakHandle = "handle-71fa5d"
	leakSecret = "secret-c03d88"
)

// slowPasses are the lines of the log that say a pass ran long.
func slowPasses(log string) []string {
	var lines []string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, `msg="slow pass"`) {
			lines = append(lines, l)
		}
	}
	return lines
}

// A pass that ran past one Interval says in one line where its time went,
// and a pass inside it says nothing: a step made slow on purpose is named
// with its time, and so are the store calls route makes for a bead (its flow
// lookups and its claim), which are inside route's time and counted. A pass
// that fails names the step it stopped in. The line is names and numbers: it
// carries nothing of the beads, sessions and group the pass handled.
func TestSlowPassNamesTheStepsItsTimeWentTo(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	clock := &passClock{epoch: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)}
	dispatch.SetClock(d, clock.now)
	d.Interval = 10 * time.Second
	var logged lockedWriter
	d.Log = slog.New(slog.NewTextHandler(&logged, nil))
	charged := &chargedStore{API: s, clock: clock}
	d.Store = charged
	d.Runners["fake"] = &chargedRunner{fakeRunner: f, clock: clock}

	envFile := filepath.Join(t.TempDir(), "env")
	must(t, os.WriteFile(envFile, []byte("API_KEY="+leakSecret+"\n"), 0o600))
	must(t, s.PutDepot(ctx, store.Depot{Name: "web", Path: "/tmp/web"}, "me"))
	flow, err := store.ParseFlow("new -> review on done\nreview -> merged\nmerged terminal\n")
	must(t, err)
	flow.Name = "web"
	_, err = s.PutFlow(ctx, *flow, "me")
	must(t, err)
	must(t, s.SetFlowPointer(ctx, "web", "", "web", "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5,
		Config: map[string]any{"env_file": envFile}}, "me"))
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	first, err := s.CreateBead(ctx, store.NewBead{Title: leakTitle, Body: leakBody, Depot: "web"}, "me")
	must(t, err)

	// A pass inside the interval says nothing, though it claims and starts.
	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 1 {
		t.Fatalf("starts = %v, want the first bead started", got)
	}
	if lines := slowPasses(logged.String()); len(lines) != 0 {
		t.Fatalf("a fast pass logged %d slow pass lines:\n%s", len(lines), logged.String())
	}

	// A step made slow on purpose: asking the runner about the one session
	// takes 12 s, so reap does, and the pass is over its 10 s.
	clock.price("alive", 12*time.Second)
	must(t, d.Reconcile(ctx))
	lines := slowPasses(logged.String())
	if len(lines) != 1 {
		t.Fatalf("a pass of 12 s logged %d slow pass lines, want 1:\n%s", len(lines), logged.String())
	}
	for _, want := range []string{" took=12s", " interval=10s", " sessions=1", " reap=12s", " route=0s", " stamp=0s"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("slow pass line lacks %q:\n%s", want, lines[0])
		}
	}
	// Every step is named, in the order the pass runs them.
	at := 0
	for _, step := range []string{"stamp_limits_clock", "nudge_limits_refresher", "drain_starts", "drain_crew_starts",
		"begin_home", "pane_cache", "groups", "reap", "usage", "teardown", "cleanup", "timers", "pass_lists",
		"waits", "sidings", "quiet_hooks", "gate", "crew", "rules", "ci", "issues", "pushes", "bases", "route",
		"settle_starts", "settle_crew_starts", "backups", "telemetry", "peers", "fetches", "end_home", "stamp"} {
		i := strings.Index(lines[0][at:], " "+step+"=")
		if i < 0 {
			t.Fatalf("slow pass line has no %s after position %d:\n%s", step, at, lines[0])
		}
		at += i + 1
	}

	// A fast pass after it adds no line.
	clock.price("alive", 0)
	must(t, d.Reconcile(ctx))
	if lines := slowPasses(logged.String()); len(lines) != 1 {
		t.Fatalf("a fast pass after a slow one: %d slow pass lines, want still 1:\n%s", len(lines), logged.String())
	}

	// Route's lookups are timed at its call sites and counted: a second bead
	// under a flow takes a lookup of each kind and a claim, 11 s together.
	second, err := s.CreateBead(ctx, store.NewBead{Title: leakTitle, Body: leakBody, Depot: "web"}, "me")
	must(t, err)
	clock.price("stranded", time.Second)
	clock.price("next_stages", 2*time.Second)
	clock.price("flow_map", 3*time.Second)
	clock.price("claim", 5*time.Second)
	must(t, d.Reconcile(ctx))
	lines = slowPasses(logged.String())
	if len(lines) != 2 {
		t.Fatalf("a pass of 11 s in route logged %d slow pass lines in all, want 2:\n%s", len(lines), logged.String())
	}
	for _, want := range []string{" took=11s", " route=11s", " reap=0s",
		" stranded.took=1s stranded.calls=1", " next_stages.took=2s next_stages.calls=1",
		" flow_map.took=3s flow_map.calls=1", " claim.took=5s claim.calls=1"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("slow pass line lacks %q:\n%s", want, lines[1])
		}
	}
	if got := f.starts(); len(got) != 2 {
		t.Fatalf("starts = %v, want the second bead started too", got)
	}

	// A pass that fails in a step still says where its time went, up to the
	// step it stopped in.
	clock.price("sessions", 11*time.Second)
	charged.sessionsFail.Store(true)
	if err := d.Reconcile(ctx); err == nil {
		t.Fatal("a pass whose session list failed returned no error")
	}
	lines = slowPasses(logged.String())
	if len(lines) != 3 {
		t.Fatalf("a failed pass of 11 s logged %d slow pass lines in all, want 3:\n%s", len(lines), logged.String())
	}
	if !strings.Contains(lines[2], " reap=11s") || strings.Contains(lines[2], " usage=") || strings.Contains(lines[2], " route=") {
		t.Errorf("a pass that failed in reap should name reap and no later step:\n%s", lines[2])
	}

	// Names and numbers only: nothing of what the pass handled is in the lines.
	sessions, err := s.Sessions(ctx, false)
	must(t, err)
	secrets := []string{leakTitle, leakBody, leakHandle, leakSecret, envFile, first.ID, second.ID}
	for _, sess := range sessions {
		secrets = append(secrets, sess.ID)
	}
	for _, line := range lines {
		for _, leak := range secrets {
			if strings.Contains(line, leak) {
				t.Errorf("slow pass line carries %q:\n%s", leak, line)
			}
		}
	}
}

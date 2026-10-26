package dispatch_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// waitCrew polls until the session of crew member name for user ("": the
// line session) satisfies cond: the effect a test waits on while a crew
// start runs beside its passes, never the wall time the start takes.
func waitCrew(t *testing.T, s store.API, name, user, what string, cond func(store.CrewSession) bool) store.CrewSession {
	t.Helper()
	deadline := time.Now().Add(dispatch.WaitFuse)
	for time.Now().Before(deadline) {
		r, err := s.GetCrew(context.Background(), name)
		must(t, err)
		if st := r.Session(user); cond(st) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("crew member %s (session of %q) never %s", name, user, what)
	return store.CrewSession{}
}

func crewRuns(st store.CrewSession) bool { return st.Live() }

// waitEvent polls until the journal or bead id has an event of kind.
func waitEvent(t *testing.T, s store.API, id, kind string) {
	t.Helper()
	deadline := time.Now().Add(dispatch.WaitFuse)
	for time.Now().Before(deadline) {
		if len(eventKinds(t, s, id, kind)) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never had a %s event", id, kind)
}

// nudge makes a store write no route takes, which wakes a daemon's loop for
// a pass. A write made before Run's wake subscription is up wakes nothing
// (Store.Wake delivers the commits after it), and a loop with a long
// Interval then waits for its next tick, so a test that needs a pass nudges
// until one has run (passRan, or the gate's awaitPass).
func nudge(t *testing.T, s store.API) *store.Bead {
	t.Helper()
	b, err := s.CreateBead(context.Background(), store.NewBead{Title: "wake", Stage: "limbo"}, "me")
	must(t, err)
	return b
}

// passRan has the daemon run a pass for a nudge of ours and waits until it
// has, as wakePass does; where a nudge woke nothing it nudges again. A call
// made after an earlier one returned is answered by a later pass: the pass
// that answered the earlier call had read its beads before it answered, so
// it did not take the nudge made since, and the next pass begins only after
// that one has ended.
func passRan(t *testing.T, s store.API) {
	t.Helper()
	deadline := time.Now().Add(dispatch.WaitFuse)
	for tries := 1; time.Now().Before(deadline); tries++ {
		b := nudge(t, s)
		// How long a nudge gets before the next is made, not what the test
		// waits for: a pass that is only slow is nudged once more, no harm.
		again := time.Now().Add(time.Second)
		for time.Now().Before(again) {
			if len(eventKinds(t, s, b.ID, "unrouted")) > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Logf("nudge %d woke no pass within a second; nudging again", tries)
	}
	t.Fatal("the daemon ran no pass for any nudge")
}

// awaitEntered waits for the held crew start to say it is under way, and
// names its crew member.
func awaitEntered(t *testing.T, f *crewRunner) string {
	t.Helper()
	select {
	case name := <-f.entered:
		return name
	case <-time.After(dispatch.WaitFuse):
		t.Fatal("the held crew start never began")
	}
	return ""
}

// aliveGate is a fakeRunner whose Alive waits for a token while a test has
// armed it, so the test can hold a pass inside reap, past the drain at its
// head and before the crew, while a crew start finishes beside it.
type aliveGate struct {
	*fakeRunner
	mu   sync.Mutex
	gate chan struct{} // a token lets one Alive go on; closed, all do
	in   chan struct{} // says an Alive is waiting
}

func (r *aliveGate) arm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gate, r.in = make(chan struct{}), make(chan struct{}, 1)
}

func (r *aliveGate) Alive(ctx context.Context, h map[string]any) (bool, error) {
	r.mu.Lock()
	gate, in := r.gate, r.in
	r.mu.Unlock()
	if gate != nil {
		select {
		case in <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return r.fakeRunner.Alive(ctx, h)
}

// awaitPass nudges the daemon until a pass stops in Alive, and answers the
// last nudge: the pass, which reads the beads after the gate lets it go,
// takes every one of them.
func (r *aliveGate) awaitPass(t *testing.T, s store.API) *store.Bead {
	t.Helper()
	deadline := time.Now().Add(dispatch.WaitFuse)
	for tries := 1; time.Now().Before(deadline); tries++ {
		b := nudge(t, s)
		select {
		case <-r.in:
			return b
		case <-time.After(time.Second): // how long a nudge gets, as in passRan
			t.Logf("nudge %d woke no pass within a second; nudging again", tries)
		}
	}
	t.Fatal("no pass reached the gate")
	return nil
}

// crewStartsYard is a daemon's yard for the tests below: the yardmaster,
// whose start the fake holds until the test releases it, with a
// conversation session for hal behind its line session; a member zed that
// sorts after it in the crew's order, so a start held inline would stall it;
// and a bead group on a runner of its own, with one ready bead.
func crewStartsYard(t *testing.T) (s store.API, f *crewRunner, d *dispatch.Dispatcher, bead string) {
	t.Helper()
	s, f, d = crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.hold, f.entered, f.release = "yardmaster", make(chan string, 16), make(chan struct{})
	_, err := s.PutCrew(ctx, store.CrewMember{Name: "zed", Runner: "fake", Enabled: true}, "me")
	must(t, err)
	_, err = s.AddUser(ctx, "hal", "me")
	must(t, err)
	serveCrew(t, s, "", map[string]string{"hal": "/tmp/hal.env"})
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "beads", Limit: 2}, "me"))
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b1"}, "me")
	must(t, err)
	d.Runners["beads"] = &fakeRunner{dead: map[string]bool{}}
	d.Interval = time.Hour
	return s, f, d, b.ID
}

// One crew member's StartCrew held past any pass's patience leaves the pass
// free (the crew's twin of TestBlockedStartDoesNotBlockOtherGroup): the
// member after it starts and so does a bead, from the same daemon. The held
// member starts nothing more, not even its conversation session, which
// waits behind its line session's start as one start per member always has,
// and no later pass calls the agent being started a second session; once
// released it starts each session exactly once.
func TestBlockedCrewStartDoesNotBlockOtherMember(t *testing.T) {
	t.Parallel()
	s, f, d, bead := crewStartsYard(t)
	ctx := context.Background()
	stop := runDaemon(t, d)
	defer stop()

	if got := awaitEntered(t, f); got != "yardmaster" {
		t.Fatalf("held start is for %s, want yardmaster", got)
	}
	waitCrew(t, s, "zed", "", "ran behind a held start", crewRuns)
	waitSessionState(t, s, bead, "running")
	// A later pass finds the start in flight and leaves it be.
	passRan(t, s)
	y, err := s.GetCrew(ctx, "yardmaster")
	must(t, err)
	if y.Live() || y.Conversations["hal"].Live() {
		t.Fatalf("a member whose start is held runs: %+v", y)
	}
	if n := f.startCalls("yardmaster"); n != 1 {
		t.Fatalf("yardmaster starts = %d, want 1 while held", n)
	}
	if n := f.startCalls("yardmaster:hal"); n != 0 {
		t.Fatalf("hal's conversation started %d times behind the line session's start", n)
	}
	if blocked := eventKinds(t, s, y.Journal, "crew_blocked"); len(blocked) != 0 {
		t.Fatalf("a pass took the agent being started for a second session: %+v", blocked)
	}

	close(f.release)
	waitCrew(t, s, "yardmaster", "", "ran once released", crewRuns)
	waitCrew(t, s, "yardmaster", "hal", "ran after the line session", crewRuns)
	if line, hal, zed := f.startCalls("yardmaster"), f.startCalls("yardmaster:hal"), f.startCalls("zed"); line != 1 || hal != 1 || zed != 1 {
		t.Fatalf("starts: yardmaster %d, hal %d, zed %d; want one each", line, hal, zed)
	}
}

// A start that finishes while a pass is under way is the next pass's to
// apply. The store already says the session runs, but the pass that reads
// it leaves it alone, since what the session before it left in the pass's
// maps is cleared only by the drain; and nothing rides behind the finished
// start, so the member's other session waits for that drain too.
func TestCrewStartFinishedMidPassIsLeftToTheNextPass(t *testing.T) {
	t.Parallel()
	s, f, d, bead := crewStartsYard(t)
	gate := &aliveGate{fakeRunner: &fakeRunner{dead: map[string]bool{}}}
	d.Runners["beads"] = gate
	stop := runDaemon(t, d)
	defer stop()

	awaitEntered(t, f)
	waitSessionState(t, s, bead, "running") // its session is reaped on every pass

	// A pass that stops in reap, past the drain at its head, before the crew.
	gate.arm()
	wake := gate.awaitPass(t, s)
	// The held start finishes while it is there.
	close(f.release)
	waitCrew(t, s, "yardmaster", "", "ran once released", crewRuns)
	gate.gate <- struct{}{}
	waitEvent(t, s, wake.ID, "unrouted") // the pass has been through the crew
	st := waitCrew(t, s, "yardmaster", "", "ran", crewRuns)
	if st.Status != "" {
		t.Fatalf("a pass kept a session whose start it had not applied yet: status %q", st.Status)
	}
	if n := f.startCalls("yardmaster:hal"); n != 0 {
		t.Fatalf("hal's conversation started %d times before the line session's start was applied", n)
	}

	// From here the passes run free: the next applies the start, keeps the
	// session it started and starts the conversation behind it.
	close(gate.gate)
	waitCrew(t, s, "yardmaster", "", "was seen idle", func(st store.CrewSession) bool { return st.Status == "idle" })
	waitCrew(t, s, "yardmaster", "hal", "ran after the line session", crewRuns)
}

// A pass run alone gives the crew start it handed over its own context, so
// cancelling the pass ends the start and the pass (the crew's twin of
// TestStandaloneReconcileUsesPassContext), and the completion of the
// cancelled start is applied by the next pass, which leaves no start in
// flight. The first pass is not asked to settle it: a crew start is handed
// over early in the pass, so the cancel lands on the steps after it, which
// through the socket client stop on the context and return before the
// settle (a store in the process never looks at it). A bead's start is
// handed over in the last step, which is why its twin settles whatever the
// store.
func TestStandaloneReconcileCrewStartUsesPassContext(t *testing.T) {
	t.Parallel()
	_, f, d := crewSetup(t, store.CrewMember{})
	f.hold, f.entered, f.release = "yardmaster", make(chan string, 1), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pass := make(chan error, 1)
	go func() { pass <- d.Reconcile(ctx) }()
	awaitEntered(t, f)
	cancel()
	select {
	case <-pass: // whatever it returned: its context is gone
	case <-time.After(dispatch.WaitFuse):
		t.Fatal("the pass did not end once its context was cancelled")
	}

	// Released first, so that a start the next pass made by mistake would
	// not hold the test up; the count says whether it did.
	close(f.release)
	must(t, d.Reconcile(context.Background()))
	if members := dispatch.CrewStartsInFlight(d); len(members) != 0 {
		t.Fatalf("the next pass left crew starts in flight: %v", members)
	}
	if n := f.startCalls("yardmaster"); n != 1 {
		t.Fatalf("yardmaster starts = %d, want the one the cancelled pass made", n)
	}
}

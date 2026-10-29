package dispatch_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// crewCountRunner stands in for the herdr runner on what the count of a crew
// session needs of it, as countRunner does for a bead's session: its runtime
// names the id of an agent's session while the agent runs and not after, as
// herdr names none for an agent that is gone or whose tab is closed, and the
// count is read by the herdr runner's own Usage from the session files the test
// writes under a temp directory. For everything else it is a crewRunner.
type crewCountRunner struct {
	*crewRunner
	files  *countRunner // writes an agent's session file, where its harness keeps it
	reader *runner.Herdr
	kind   string
	noID   bool // the runtime names no id for its agents, as for muse
	asked  int  // SessionID calls
}

func (c *crewCountRunner) StartCrew(ctx context.Context, r store.CrewMember, user string) (map[string]any, error) {
	h, err := c.crewRunner.StartCrew(ctx, r, user)
	if err == nil {
		h["kind"] = c.kind // the herdr runner's handle says it
	}
	return h, err
}

func (c *crewCountRunner) SessionID(_ context.Context, h map[string]any, _ time.Time) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked++
	agent, _ := h["agent"].(string)
	if c.noID || c.status[agent] == "" {
		return "", nil
	}
	return agentID(agent), nil
}

func (c *crewCountRunner) Usage(ctx context.Context, h map[string]any, startedAt time.Time, fallback bool) (agents.Usage, bool, string, error) {
	return c.reader.Usage(ctx, h, startedAt, fallback)
}

// crewCountDispatcher is a dispatcher over s, and a replacement for one when the
// store has had another: it remembers nothing of the daemon before it.
func crewCountDispatcher(s store.API, c *crewCountRunner) *dispatch.Dispatcher {
	return &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": c}, PromptSettle: -1, PromptWait: -1}
}

// crewCountSetup is a yard whose crew member yardmaster runs agents of kind
// through the stand-in.
func crewCountSetup(t *testing.T, kind string) (store.API, *crewCountRunner, *dispatch.Dispatcher) {
	t.Helper()
	s := storetest.New(t)
	home := t.TempDir()
	c := &crewCountRunner{crewRunner: &crewRunner{status: map[string]string{}}, kind: kind,
		files:  &countRunner{kind: kind, home: home},
		reader: &runner.Herdr{AgentHomes: map[string]string{"claude": home, "codex": home, "muse": home}}}
	_, err := s.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "fake"}, "me")
	must(t, err)
	return s, c, crewCountDispatcher(s, c)
}

// crewSessionOf is the state of the line session ("") or of the conversation
// session of user of the crew member yardmaster.
func crewSessionOf(t *testing.T, s store.API, user string) store.CrewSession {
	t.Helper()
	r, err := s.GetCrew(context.Background(), "yardmaster")
	must(t, err)
	return r.Session(user)
}

// crewAgent is the agent of the running session of user.
func crewAgent(t *testing.T, s store.API, user string) string {
	t.Helper()
	st := crewSessionOf(t, s, user)
	if !st.Live() {
		t.Fatalf("the session of %q is %s, want it running", user, st.State)
	}
	return st.Handle["agent"].(string)
}

// handOff has the running session of agent write its handoff: the next pass
// that finds it idle stops it and starts its next.
func handOff(t *testing.T, s store.API, agent string) {
	t.Helper()
	_, err := s.Journal(context.Background(), "yardmaster", store.NoteHandoff, "Next: review", agent)
	must(t, err)
}

// crewCount is what a crew_usage event says, without the time it was read at.
type crewCount struct {
	Member, User, Kind, Agent string
	Usage                     map[string]any
}

// crewCounted are the yard's crew_usage events, oldest first. One on a bead, or
// with a read_at that is no time, fails the test.
func crewCounted(t *testing.T, s store.API) []crewCount {
	t.Helper()
	evs, err := s.EventsOfKind(context.Background(), "crew_usage", 100)
	must(t, err)
	var out []crewCount
	for _, e := range evs {
		if e.Bead != nil {
			t.Fatalf("a crew_usage event on bead %s", *e.Bead)
		}
		u, _ := e.Data["usage"].(map[string]any)
		at, _ := u["read_at"].(string)
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			t.Fatalf("read_at %q: %v", at, err)
		}
		delete(u, "read_at")
		c := crewCount{Usage: u}
		c.Member, _ = e.Data["member"].(string)
		c.User, _ = e.Data["user"].(string)
		c.Kind, _ = e.Data["kind"].(string)
		c.Agent, _ = e.Data["agent_session"].(string)
		out = append(out, c)
	}
	return out
}

func wantCrewCounted(t *testing.T, s store.API, want ...crewCount) {
	t.Helper()
	if got := crewCounted(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("crew_usage events = %+v, want %+v", got, want)
	}
}

// A crew session that ends is counted to its member whatever ends it, on the
// pass after, by the id of its agent's session that the daemon kept while it
// ran: its handle is the next session's by then and its agent is unknown to the
// runtime. The line session is counted when it is rotated and when it is gone,
// and a conversation session with its user. The count is logged once, however
// many passes and daemons follow, and nothing waits for it afterwards.
func TestCrewSessionIsCountedToItsMember(t *testing.T) {
	t.Parallel()
	rotated := func(t *testing.T, s store.API, c *crewCountRunner, d *dispatch.Dispatcher, agent string) {
		handOff(t, s, agent)
		passes(t, d, 1) // stopped, and the next session started
	}
	gone := func(t *testing.T, s store.API, c *crewCountRunner, d *dispatch.Dispatcher, agent string) {
		c.set(agent, "")
		passes(t, d, 1) // noticed, and the next session started
	}
	for name, tc := range map[string]struct {
		user string
		// started is the passes the session needs to start and have the id of
		// its agent kept. A member starts one session a pass (one start in
		// flight per member, crewstarts.go), the line session first, so a
		// conversation's is the line's two passes and one more.
		started int
		end     func(t *testing.T, s store.API, c *crewCountRunner, d *dispatch.Dispatcher, agent string)
	}{
		"rotated":                        {"", 2, rotated},
		"gone":                           {"", 2, gone},
		"a conversation session is gone": {"hal", 3, gone},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, c, d := crewCountSetup(t, "codex")
			// Past the time a session has to live for its start to count: one gone
			// sooner is a failed start, and is backed off.
			dispatch.SetClock(d, func() time.Time { return time.Now().Add(2 * store.MinSessionLife) })
			if tc.user != "" {
				_, err := s.AddUser(context.Background(), tc.user, "me")
				must(t, err)
				serveCrew(t, s, "", map[string]string{tc.user: "/tmp/" + tc.user + ".env"})
			}
			passes(t, d, tc.started) // started, and the id of its agent kept
			agent := crewAgent(t, s, tc.user)
			if h := crewSessionOf(t, s, tc.user).Handle; h[dispatch.AgentSession] != agentID(agent) || h["kind"] != "codex" {
				t.Fatalf("handle = %v, want the id of the agent's session and its kind", h)
			}
			c.files.write(t, agent, tokenCount(time.Now(), 1000, 400, 50))

			tc.end(t, s, c, d, agent)
			st := crewSessionOf(t, s, tc.user)
			if st.Handle["agent"] == agent {
				t.Fatalf("handle = %v: the session that ended is still the one it holds", st.Handle)
			}
			if len(st.Uncounted) != 1 || st.Uncounted[0].AgentSession != agentID(agent) || st.Uncounted[0].Kind != "codex" {
				t.Fatalf("waiting for a count: %+v, want the session that ended", st.Uncounted)
			}
			wantCrewCounted(t, s) // read on the pass after the one that stopped it

			passes(t, d, 1)
			want := crewCount{Member: "yardmaster", User: tc.user, Kind: "codex", Agent: agentID(agent), Usage: counts(1000, 600, 50, 400, 0)}
			wantCrewCounted(t, s, want)
			for _, user := range []string{"", tc.user} {
				if left := crewSessionOf(t, s, user).Uncounted; len(left) != 0 {
					t.Fatalf("the session of %q still waits for %+v", user, left)
				}
			}
			passes(t, d, 2)
			passes(t, crewCountDispatcher(s, c), 2)
			wantCrewCounted(t, s, want)
		})
	}
}

// A crew session whose agent the runtime named no session id for (Muse today)
// has no file to find: it is counted missing, once, on the pass after it ended.
func TestCrewSessionWithoutAnIDIsCountedMissing(t *testing.T) {
	t.Parallel()
	s, c, d := crewCountSetup(t, "muse")
	c.noID = true
	passes(t, d, 2)
	agent := crewAgent(t, s, "")
	if h := crewSessionOf(t, s, "").Handle; h[dispatch.AgentSession] != nil {
		t.Fatalf("handle = %v, want no id", h)
	}
	handOff(t, s, agent)
	passes(t, d, 1)
	if due := crewSessionOf(t, s, "").Uncounted; len(due) != 1 || due[0].AgentSession != "" || due[0].Kind != "muse" {
		t.Fatalf("waiting for a count: %+v, want the muse session, with no id", due)
	}
	wantCrewCounted(t, s)
	passes(t, d, 1)
	want := crewCount{Member: "yardmaster", Kind: "muse", Usage: map[string]any{"missing": "no session id"}}
	wantCrewCounted(t, s, want)
	passes(t, d, 2)
	passes(t, crewCountDispatcher(s, c), 2)
	wantCrewCounted(t, s, want)
}

// The id of a crew session's agent is asked for in the first 15 minutes after
// its start and not after, so a kind the runtime names none for is not asked on
// every pass for ever. A session that ends without one is asked once more as
// it is stopped, before its tab is closed, which is its last chance: the count
// is read by the id it gives.
func TestCrewAgentSessionIsAskedOnceMoreAtItsStop(t *testing.T) {
	t.Parallel()
	s, c, d := crewCountSetup(t, "codex")
	now := time.Now().Add(20 * time.Minute)
	dispatch.SetClock(d, func() time.Time { return now })
	passes(t, d, 3) // started 20 minutes ago by the dispatcher's clock
	agent := crewAgent(t, s, "")
	if h := crewSessionOf(t, s, "").Handle; c.asked != 0 || h[dispatch.AgentSession] != nil {
		t.Fatalf("asked %d times, handle %v: a session past the window is not asked", c.asked, h)
	}
	c.files.write(t, agent, tokenCount(time.Now(), 100, 0, 10))
	handOff(t, s, agent)
	passes(t, d, 1)
	if due := crewSessionOf(t, s, "").Uncounted; c.asked != 1 || len(due) != 1 || due[0].AgentSession != agentID(agent) {
		t.Fatalf("asked %d times, waiting %+v: want one ask at the stop, and the id on the entry", c.asked, due)
	}
	passes(t, d, 1)
	wantCrewCounted(t, s, crewCount{Member: "yardmaster", Kind: "codex", Agent: agentID(agent), Usage: counts(100, 100, 10, 0, 0)})
}

// The agent's own total is waited for, on the dispatcher's clock, for ten
// minutes from the stop the store recorded, and then the lines it wrote along
// the way are counted and said to fall short: the rules of a bead's session.
func TestCrewTotalNotYetWrittenIsWaitedFor(t *testing.T) {
	t.Parallel()
	s, c, d := crewCountSetup(t, "claude")
	now := time.Now().Add(2 * store.MinSessionLife)
	dispatch.SetClock(d, func() time.Time { return now })
	passes(t, d, 2)
	agent := crewAgent(t, s, "")
	c.files.write(t, agent, claudeMessage(now, "m1", 3, 10, 100, 20), claudeMessage(now, "m2", 2, 7, 50, 0))
	handOff(t, s, agent)
	passes(t, d, 1) // stopped
	stopped := crewSessionOf(t, s, "").Uncounted[0].StoppedAt
	for _, after := range []time.Duration{0, time.Minute, 9*time.Minute + 59*time.Second} {
		now = stopped.Add(after)
		passes(t, d, 1)
		wantCrewCounted(t, s)
	}
	now = stopped.Add(10 * time.Minute)
	passes(t, d, 1)
	wantCrewCounted(t, s, crewCount{Member: "yardmaster", Kind: "claude", Agent: agentID(agent), Usage: counts(5, 5, 17, 150, 20, "partial")})
}

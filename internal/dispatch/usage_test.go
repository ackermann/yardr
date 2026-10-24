package dispatch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// countRunner stands in for the herdr runner on what the count of a session
// needs of it. Its runtime names the id of an agent's session until the
// agent's tab is closed (Stop) and not after, as herdr does; and the count is
// read by the herdr runner's own Usage from the session files the test writes
// under a temp directory, so what the dispatcher puts on the handle is what
// the runner reads, and the harness files of the kinds are the real ones.
type countRunner struct {
	*fakeRunner
	reader *runner.Herdr
	kind   string
	home   string

	noID     bool            // the runtime names no id for its agents, as for muse
	gone     map[string]bool // session -> its agent's tab is closed
	asked    int             // SessionID calls
	stopErr  error           // the next Stop fails with it
	usageErr error           // Usage fails with it
}

func (c *countRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	h, err := c.fakeRunner.Start(ctx, j)
	if err == nil {
		h["kind"] = agents.Kind(j.Group.Config)
	}
	return h, err
}

func (c *countRunner) SessionID(_ context.Context, h map[string]any, _ time.Time) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked++
	session, _ := h["session"].(string)
	if c.noID || c.gone[session] {
		return "", nil
	}
	return agentID(session), nil
}

func (c *countRunner) Stop(ctx context.Context, h map[string]any) error {
	c.mu.Lock()
	err := c.stopErr
	c.stopErr = nil
	if err == nil {
		c.gone[h["session"].(string)] = true
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.fakeRunner.Stop(ctx, h)
}

func (c *countRunner) Usage(ctx context.Context, h map[string]any, startedAt time.Time, fallback bool) (agents.Usage, bool, string, error) {
	c.mu.Lock()
	err := c.usageErr
	c.mu.Unlock()
	if err != nil {
		return agents.Usage{}, false, "", err
	}
	return c.reader.Usage(ctx, h, startedAt, fallback)
}

// set changes what the runtime does under the lock the dispatcher's calls take.
func (c *countRunner) set(f func(c *countRunner)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c)
}

// agentID is the id of the session an agent of the yard's session keeps.
func agentID(session string) string { return "agent-" + session }

// write adds lines to the session file of the agent of session, where its
// harness keeps it.
func (c *countRunner) write(t *testing.T, session string, lines ...string) {
	t.Helper()
	id := agentID(session)
	path := filepath.Join(c.home, "projects", "p", id+".jsonl")
	if c.kind == "codex" {
		path = filepath.Join(c.home, "sessions", "2026", "10", "22", "rollout-2026-10-22T10-00-00-"+id+".jsonl")
	}
	if c.kind == "muse" {
		path = filepath.Join(c.home, "sessions", "2026", "10", "24", id, "session.jsonl")
	}
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	must(t, err)
	defer f.Close()
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	must(t, err)
}

// tokenCount is the line codex writes after a call: its running total.
func tokenCount(at time.Time, input, cached, output int) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d}}}}`,
		at.UTC().Format(time.RFC3339Nano), input, cached, output)
}

// claudeMessage is a line claude writes for a content block of a message; a
// message of several blocks repeats its usage on each.
func claudeMessage(at time.Time, id string, input, output, read, write int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`,
		at.UTC().Format(time.RFC3339Nano), id, input, output, read, write)
}

// claudeTotal is the line claude writes as its process exits.
func claudeTotal(input, output, read, write int) string {
	return fmt.Sprintf(`{"type":"cost-state","modelUsage":{"claude-opus-5-5":{"inputTokens":%d,"outputTokens":%d,"cacheReadInputTokens":%d,"cacheCreationInputTokens":%d}}}`,
		input, output, read, write)
}

// museMeta is what muse writes first for a session: a retained_frame wrapper
// with no time of its own, then the metadata that names the worktree.
func museMeta(wt string, at time.Time) []string {
	us := at.UnixMicro()
	return []string{
		fmt.Sprintf(`{"retained_frame":"session_permission_transaction","children":[{"child_index":0,"record_json":"{\"sequence\":1,\"recorded_at\":%d}"}]}`, us-56000),
		fmt.Sprintf(`{"sequence":3,"recorded_at":%d,"payload_type":"runtime.session.metadata","payload":{"record":{"workspace_root":%q}}}`, us, wt),
	}
}

// museConfigured is the line that names the model the run was started with.
func museConfigured(model string, at time.Time) string {
	return fmt.Sprintf(`{"recorded_at":%d,"payload_type":"run.model.configured","payload":{"record":{"model_id":%q}}}`, at.UnixMicro(), model)
}

// museUsage is the line muse writes after a call, with its tokens.
func museUsage(at time.Time, input, output, cached int) string {
	return fmt.Sprintf(`{"recorded_at":%d,"payload":{"event":{"kind":"goal_usage_attribution","record":{"usage_family":"provider","quantity":{"input_tokens":%d,"output_tokens":%d,"cached_tokens":%d}}}}}`, at.UnixMicro(), input, output, cached)
}

// countDispatcher is a dispatcher over s, and a replacement for one when the
// store has had another: it remembers nothing of the daemon before it.
func countDispatcher(s store.API, r *countRunner) *dispatch.Dispatcher {
	return &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}}
}

// countSetup is a yard whose group g works stage new with agents of kind,
// cfg more of the group's config, and limit sessions at a time.
func countSetup(t *testing.T, kind string, cfg map[string]any, limit int) (store.API, *countRunner, *dispatch.Dispatcher) {
	t.Helper()
	s := storetest.New(t)
	home := t.TempDir()
	r := &countRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, kind: kind, home: home, gone: map[string]bool{},
		reader: &runner.Herdr{AgentHomes: map[string]string{"claude": home, "codex": home, "muse": home}}}
	ctx := context.Background()
	config := map[string]any{"kind": kind}
	maps.Copy(config, cfg)
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: limit, Config: config}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	must(t, err)
	return s, r, countDispatcher(s, r)
}

// sessionOf is the id of the bead's live session.
func sessionOf(t *testing.T, s store.API, bead string) string {
	t.Helper()
	id, _ := sessionHandle(t, s, bead)["session"].(string)
	if id == "" {
		t.Fatalf("bead %s has no live session with a handle", bead)
	}
	return id
}

// endSession ends the bead's session the way an agent does, by closing the
// bead. The next pass tears the session down.
func endSession(t *testing.T, s store.API, bead string) {
	t.Helper()
	_, err := s.CloseBead(context.Background(), bead, "merged", store.Expect{}, "builder")
	must(t, err)
}

func getSession(t *testing.T, s store.API, id string) *store.Session {
	t.Helper()
	sess, err := s.GetSession(context.Background(), id)
	must(t, err)
	return sess
}

// passes runs n passes of d.
func passes(t *testing.T, d *dispatch.Dispatcher, n int) {
	t.Helper()
	for range n {
		must(t, d.Reconcile(context.Background()))
	}
}

// counts is what a session_usage event holds for a count of these figures,
// as the store hands it back (numbers read from JSON): flags name the keys
// that are true.
func counts(input, fresh, output, read, write int, flags ...string) map[string]any {
	m := map[string]any{"input": float64(input), "fresh_input": float64(fresh), "output": float64(output),
		"cache_read": float64(read), "cache_write": float64(write)}
	for _, f := range flags {
		m[f] = true
	}
	return m
}

// recorded is the usage of each of the bead's session_usage events by the
// session it is for, without the time it was read at; two events for one
// session fail the test. It also holds the session's own record to the same
// usage and to no due entry, as one write leaves them.
func recorded(t *testing.T, s store.API, bead string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, e := range eventKinds(t, s, bead, "session_usage") {
		session, _ := e.Data["session"].(string)
		if _, twice := out[session]; twice {
			t.Fatalf("two session_usage events for session %s", session)
		}
		u, _ := e.Data["usage"].(map[string]any)
		out[session] = u
		sess := getSession(t, s, session)
		if !reflect.DeepEqual(sess.Handle["usage"], u) || sess.Handle[store.CountDue] != nil {
			t.Fatalf("session %s: handle usage %v and due %v, event %v", session, sess.Handle["usage"], sess.Handle[store.CountDue], u)
		}
		at, _ := u["read_at"].(string)
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			t.Fatalf("session %s: read_at %q: %v", session, at, err)
		}
		delete(u, "read_at")
	}
	return out
}

// wantRecorded fails the test unless the bead's session_usage events are
// exactly want, by session.
func wantRecorded(t *testing.T, s store.API, bead string, want map[string]map[string]any) {
	t.Helper()
	if got := recorded(t, s, bead); !reflect.DeepEqual(got, want) {
		t.Fatalf("session_usage of %s = %v, want %v", bead, got, want)
	}
}

func uncounted(t *testing.T, s store.API) []string {
	t.Helper()
	ss, err := s.Uncounted(context.Background())
	must(t, err)
	var ids []string
	for _, sess := range ss {
		ids = append(ids, sess.ID)
	}
	return ids
}

// A session's agent has its id on the handle once the runtime names one,
// whether or not it ever reports a model: this agent never does. A daemon
// that replaces itself finds the id there, and when the session has ended
// and the runtime knows no agent any longer, the count is still read by it,
// and logged once.
func TestAgentSessionIsKeptWithoutAModel(t *testing.T) {
	t.Parallel()
	s, r, d := countSetup(t, "codex", nil, 1)
	b := newBead(t, s, "x")
	passes(t, d, 1) // start
	session := sessionOf(t, s, b.ID)
	if h := sessionHandle(t, s, b.ID); h[dispatch.AgentSession] != nil {
		t.Fatalf("handle = %v before the first look", h)
	}
	passes(t, d, 1)
	h := sessionHandle(t, s, b.ID)
	if h[dispatch.AgentSession] != agentID(session) || h[dispatch.ModelReported] != nil || h[dispatch.ModelAsked] != nil {
		t.Fatalf("handle = %v, want the id and no model", h)
	}
	asked := r.asked
	passes(t, d, 2)
	if r.asked != asked {
		t.Fatalf("the runtime was asked for the id %d times more after it was kept", r.asked-asked)
	}

	// The agent writes its total, the daemon is replaced, and the agent's tab
	// is gone from the runtime by the time its session ends.
	r.write(t, session, tokenCount(time.Now(), 1000, 400, 50))
	r.set(func(c *countRunner) { c.gone[session] = true })
	endSession(t, s, b.ID)
	d2 := countDispatcher(s, r)
	passes(t, d2, 1) // torn down, its count due
	if got := uncounted(t, s); len(got) != 1 || got[0] != session {
		t.Fatalf("Uncounted = %v, want the one session", got)
	}
	passes(t, d2, 1)
	wantRecorded(t, s, b.ID, map[string]map[string]any{session: counts(1000, 600, 50, 400, 0)})
	if r.asked != asked {
		t.Fatalf("the runtime was asked for an id it had kept: %d calls", r.asked-asked)
	}
	if got := uncounted(t, s); len(got) != 0 {
		t.Fatalf("Uncounted = %v after the count", got)
	}
}

// An agent is asked for its id for as long after the start as for its model
// and not after, so a kind the runtime names no id for is not asked on every
// pass for ever. A session that ended without its id on the handle is asked
// once more before its tab is closed, which is its last chance: the count is
// read by the id it gives.
func TestAgentSessionIsAskedOnceMoreAtTeardown(t *testing.T) {
	t.Parallel()
	s, r, d := countSetup(t, "codex", nil, 1)
	now := time.Now().Add(20 * time.Minute)
	dispatch.SetClock(d, func() time.Time { return now })
	b := newBead(t, s, "x")
	passes(t, d, 2) // started 20 minutes ago by the dispatcher's clock
	session := sessionOf(t, s, b.ID)
	if h := sessionHandle(t, s, b.ID); r.asked != 0 || h[dispatch.AgentSession] != nil {
		t.Fatalf("asked %d times, handle %v: a session past the window is not asked", r.asked, h)
	}
	r.write(t, session, tokenCount(time.Now(), 100, 0, 10))
	endSession(t, s, b.ID)
	passes(t, d, 1)
	if r.asked != 1 {
		t.Fatalf("asked %d times at teardown, want once", r.asked)
	}
	h := getSession(t, s, session).Handle
	if h[dispatch.AgentSession] != agentID(session) || h[store.CountDue] == nil || !slices.Contains(r.stopped, session) {
		t.Fatalf("handle = %v, stopped %v: want the id, a due count, and the tab closed", h, r.stopped)
	}
	passes(t, d, 1)
	wantRecorded(t, s, b.ID, map[string]map[string]any{session: counts(100, 100, 10, 0, 0)})
}

// Every session that ran an agent ends with one session_usage, whatever the
// number of passes and daemons after it.
func TestEachSessionIsCountedOnce(t *testing.T) {
	t.Parallel()
	s, r, d := countSetup(t, "codex", nil, 2)
	a, b := newBead(t, s, "a"), newBead(t, s, "b")
	passes(t, d, 2) // both start, and their ids are kept
	sa, sb := sessionOf(t, s, a.ID), sessionOf(t, s, b.ID)
	r.write(t, sa, tokenCount(time.Now(), 1000, 400, 50))
	r.write(t, sb, tokenCount(time.Now(), 2000, 0, 80))
	endSession(t, s, a.ID)
	endSession(t, s, b.ID)
	passes(t, d, 4)
	passes(t, countDispatcher(s, r), 2)
	wantRecorded(t, s, a.ID, map[string]map[string]any{sa: counts(1000, 600, 50, 400, 0)})
	wantRecorded(t, s, b.ID, map[string]map[string]any{sb: counts(2000, 2000, 80, 0, 0)})
	if got := uncounted(t, s); len(got) != 0 {
		t.Fatalf("Uncounted = %v", got)
	}
}

// The agent's own total is waited for, on the dispatcher's clock, for ten
// minutes after the stop: a total that comes in time is the count, and one
// that does not leaves the lines it wrote along the way, marked partial.
func TestTotalNotYetWrittenIsWaitedForOnTheDispatchersClock(t *testing.T) {
	t.Parallel()
	total := counts(752, 752, 74978, 23562912, 247751)
	for name, tc := range map[string]struct {
		written time.Duration // after the stop, when the agent writes its total; 0 for never
		want    map[string]any
	}{
		"never written":       {want: counts(5, 5, 17, 150, 20, "partial")},
		"written in time":     {written: 9*time.Minute + 59*time.Second, want: total},
		"written at the wait": {written: 10 * time.Minute, want: total},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, r, d := countSetup(t, "claude", nil, 1)
			now := time.Now()
			dispatch.SetClock(d, func() time.Time { return now })
			b := newBead(t, s, "x")
			passes(t, d, 2)
			session := sessionOf(t, s, b.ID)
			// Three lines of two messages: the first message is on two of them.
			r.write(t, session, claudeMessage(now, "m1", 3, 10, 100, 20), claudeMessage(now, "m1", 3, 10, 100, 20),
				claudeMessage(now, "m2", 2, 7, 50, 0))
			endSession(t, s, b.ID)
			passes(t, d, 1) // stopped at now
			stopped := now
			for _, after := range []time.Duration{0, time.Minute, 9*time.Minute + 59*time.Second, 10 * time.Minute} {
				now = stopped.Add(after)
				if tc.written != 0 && after == tc.written {
					r.write(t, session, claudeTotal(752, 74978, 23562912, 247751))
				}
				passes(t, d, 1)
				counted := len(recorded(t, s, b.ID)) != 0
				if want := (tc.written != 0 && after >= tc.written) || after >= 10*time.Minute; counted != want {
					t.Fatalf("counted = %v %s after the stop, want %v", counted, after, want)
				}
			}
			wantRecorded(t, s, b.ID, map[string]map[string]any{session: tc.want})
		})
	}
}

// matchRunner is a countRunner whose runtime names no id for its agents, as
// herdr names none for Muse panes: the id and the model come from the real
// runner's worktree match over the session files the test writes. The handle
// carries no agent key, so the match goes by the handle's kind and asks the
// runtime nothing.
type matchRunner struct {
	*countRunner
}

func (m *matchRunner) SessionID(ctx context.Context, h map[string]any, startedAt time.Time) (string, error) {
	return m.reader.SessionID(ctx, h, startedAt)
}

func (m *matchRunner) Model(ctx context.Context, h map[string]any, startedAt time.Time) (string, error) {
	return m.reader.Model(ctx, h, startedAt)
}

// A session the runtime names no id for is matched by its worktree when one
// file qualifies, and counted missing when none does. A matched session's
// id and model are recorded while it runs, then its count is read by that id.
func TestSessionWithoutAnIDIsMatchedOrMissing(t *testing.T) {
	t.Parallel()
	t.Run("no qualifying file", func(t *testing.T) {
		t.Parallel()
		s, r, d := countSetup(t, "muse", nil, 1)
		r.noID = true
		b := newBead(t, s, "x")
		passes(t, d, 2)
		session := sessionOf(t, s, b.ID)
		if h := sessionHandle(t, s, b.ID); h[dispatch.AgentSession] != nil {
			t.Fatalf("handle = %v", h)
		}
		endSession(t, s, b.ID)
		passes(t, d, 1) // torn down
		if got := recorded(t, s, b.ID); len(got) != 0 {
			t.Fatalf("session_usage in the pass that stopped it: %v", got)
		}
		if got := uncounted(t, s); len(got) != 1 || got[0] != session {
			t.Fatalf("Uncounted = %v, want the session", got)
		}
		passes(t, d, 1)
		want := map[string]map[string]any{session: {"missing": "no session id"}}
		wantRecorded(t, s, b.ID, want)
		passes(t, d, 2)
		passes(t, countDispatcher(s, r), 2)
		wantRecorded(t, s, b.ID, want)
	})
	t.Run("worktree match", func(t *testing.T) {
		t.Parallel()
		s, r, _ := countSetup(t, "muse", nil, 1)
		m := &matchRunner{r}
		d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": m}}
		ctx := context.Background()
		b := newBead(t, s, "x")
		passes(t, d, 1) // start
		session := sessionOf(t, s, b.ID)
		wt := t.TempDir()
		h := maps.Clone(sessionHandle(t, s, b.ID))
		h["worktree"] = wt // the herdr runner's handle says where the bead is worked
		must(t, s.SetHandle(ctx, session, h))
		started := getSession(t, s, session).StartedAt
		r.write(t, session, append(museMeta(wt, started.Add(4*time.Second)),
			museConfigured("muse-wonder-1.0", started.Add(5*time.Second)),
			museUsage(started.Add(time.Minute), 3000, 110, 1900))...)
		passes(t, d, 1)
		if h := sessionHandle(t, s, b.ID); h[dispatch.AgentSession] != agentID(session) || h[dispatch.ModelReported] != "muse-wonder-1.0" {
			t.Fatalf("handle = %v, want the matched id and model", h)
		}
		endSession(t, s, b.ID)
		passes(t, d, 1) // torn down, its count due
		if got := recorded(t, s, b.ID); len(got) != 0 {
			t.Fatalf("session_usage in the pass that stopped it: %v", got)
		}
		passes(t, d, 1)
		wantRecorded(t, s, b.ID, map[string]map[string]any{session: counts(3000, 1100, 110, 1900, 0)})
	})
}

// Only the herdr runner reads a count; a manual group's session is a person
// working the bead and an exec group's is a script, neither with an agent or
// a transcript, so neither is due one: they log no session_usage and are never
// listed as uncounted. Nor is a session that started nothing (no handle) or
// whose runner the yard no longer has: Countable, which the dispatcher and
// yardr down both ask, says so.
func TestSessionsWithoutAnAgentAreNotCounted(t *testing.T) {
	t.Parallel()
	for name, r := range map[string]dispatch.Runner{
		"manual": &runner.Manual{}, "exec": &runner.Exec{}, "peer": &runner.Peer{}, "herdr": &runner.Herdr{}} {
		if _, reads := r.(dispatch.UsageReader); reads != (name == "herdr") {
			t.Errorf("the %s runner reads a count: %v", name, reads)
		}
		if got := dispatch.Countable(r, map[string]any{"agent": "a1"}); got != (name == "herdr") {
			t.Errorf("Countable(%s runner, a started session) = %v", name, got)
		}
	}
	for name, countable := range map[string]bool{
		"no handle":    dispatch.Countable(&runner.Herdr{}, nil),
		"empty handle": dispatch.Countable(&runner.Herdr{}, map[string]any{}),
		"no runner":    dispatch.Countable(nil, map[string]any{"agent": "a1"}),
	} {
		if countable {
			t.Errorf("Countable with %s = true", name)
		}
	}
	cfg := map[string]map[string]any{"exec": {"cmd": "true"}}
	for _, name := range []string{"exec", "manual"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := storetest.New(t)
			f := &fakeRunner{dead: map[string]bool{}}
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{name: f}}
			ctx := context.Background()
			must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: name, Limit: 1, Config: cfg[name]}, "me"))
			_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
			must(t, err)
			b := newBead(t, s, "x")
			passes(t, d, 2)
			session := sessionOf(t, s, b.ID)
			endSession(t, s, b.ID)
			passes(t, d, 3)
			sess := getSession(t, s, session)
			if !sess.Stopped || sess.Handle[store.CountDue] != nil || sess.Handle[dispatch.AgentSession] != nil || sess.Handle["usage"] != nil {
				t.Fatalf("session = %+v: want stopped, with no count in its handle", sess)
			}
			if n := len(eventKinds(t, s, b.ID, "session_usage")); n != 0 || len(uncounted(t, s)) != 0 {
				t.Fatalf("%d session_usage events, uncounted %v", n, uncounted(t, s))
			}
		})
	}
}

// An agent left running (a group with keep) is counted up to a cutoff fixed at
// its stop, two minutes after the session ended: what it did in the grace
// counts, since the call that moved the bead is recorded after the move, and
// what it does after does not, however late the read is or whichever daemon
// reads it. The read waits for the cutoff on the dispatcher's clock. The
// count carries kept.
func TestKeptSessionIsCountedUpToItsCutoff(t *testing.T) {
	t.Parallel()
	for name, replaced := range map[string]bool{"read by the daemon that stopped it": false, "read by a daemon that replaced it": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, r, d := countSetup(t, "codex", map[string]any{"keep": true}, 1)
			now := time.Now()
			dispatch.SetClock(d, func() time.Time { return now })
			b := newBead(t, s, "x")
			passes(t, d, 2)
			session := sessionOf(t, s, b.ID)
			r.write(t, session, tokenCount(now, 1000, 400, 50))
			endSession(t, s, b.ID)
			passes(t, d, 1) // marked stopped, its agent left running
			sess := getSession(t, s, session)
			due, _ := sess.Handle[store.CountDue].(map[string]any)
			until, err := time.Parse(time.RFC3339Nano, fmt.Sprint(due[dispatch.CountUntil]))
			if err != nil || sess.EndedAt == nil || !until.Equal(sess.EndedAt.Add(2*time.Minute)) || !sess.Stopped || len(r.stopped) != 0 {
				t.Fatalf("session = %+v, due %v: want stopped, not closed, and until two minutes after it ended (%v)", sess, due, err)
			}
			// A line in the grace, and one that a person's use of the pane writes
			// after it and before the read.
			r.write(t, session, tokenCount(sess.EndedAt.Add(30*time.Second), 1500, 600, 80), tokenCount(until.Add(time.Second), 9000, 4000, 900))
			reader := d
			if replaced {
				reader = countDispatcher(s, r)
				dispatch.SetClock(reader, func() time.Time { return now })
			}
			now = until.Add(-time.Millisecond)
			passes(t, reader, 2)
			if len(recorded(t, s, b.ID)) != 0 {
				t.Fatal("counted before the cutoff had passed")
			}
			now = until
			passes(t, reader, 1)
			wantRecorded(t, s, b.ID, map[string]map[string]any{session: counts(1500, 900, 80, 600, 0, "kept")})
		})
	}
}

// A Stop that fails is tried again on a later pass, and leaves nothing due:
// the wait for the agent's total is counted from the stop that happened and
// not from the first try, which may be hours before.
func TestCountIsDueFromTheStopThatHappened(t *testing.T) {
	t.Parallel()
	s, r, d := countSetup(t, "claude", nil, 1)
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	b := newBead(t, s, "x")
	passes(t, d, 2)
	session := sessionOf(t, s, b.ID)
	r.write(t, session, claudeMessage(now, "m1", 3, 10, 100, 20))
	endSession(t, s, b.ID)
	r.set(func(c *countRunner) { c.stopErr = errors.New("herdr is down") })
	passes(t, d, 1)
	if sess := getSession(t, s, session); sess.Stopped || sess.Handle[store.CountDue] != nil || len(uncounted(t, s)) != 0 {
		t.Fatalf("session = %+v after a Stop that failed", sess)
	}
	now = now.Add(time.Hour)
	passes(t, d, 1) // stopped
	due, _ := getSession(t, s, session).Handle[store.CountDue].(map[string]any)
	if at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(due[dispatch.CountStoppedAt])); err != nil || !at.Equal(now) {
		t.Fatalf("stopped_at %v (%v), want %v", due[dispatch.CountStoppedAt], err, now)
	}
	passes(t, d, 1)
	if len(recorded(t, s, b.ID)) != 0 {
		t.Fatal("counted by the lines at once: the wait for the total was counted from the first try")
	}
	now = now.Add(10 * time.Minute)
	passes(t, d, 1)
	wantRecorded(t, s, b.ID, map[string]map[string]any{session: counts(3, 3, 10, 100, 20, "partial")})
}

// A session file that cannot be read says nothing about the count: it is read
// again on a later pass, and said once, and the session is not recorded as
// one with no count.
func TestUnreadableSessionFileIsReadAgain(t *testing.T) {
	t.Parallel()
	s, r, d := countSetup(t, "codex", nil, 1)
	var log bytes.Buffer
	d.Log = slog.New(slog.NewTextHandler(&log, nil))
	b := newBead(t, s, "x")
	passes(t, d, 2)
	session := sessionOf(t, s, b.ID)
	r.write(t, session, tokenCount(time.Now(), 100, 0, 10))
	r.set(func(c *countRunner) { c.usageErr = errors.New("permission denied") })
	endSession(t, s, b.ID)
	passes(t, d, 4)
	if got := uncounted(t, s); len(got) != 1 || len(recorded(t, s, b.ID)) != 0 {
		t.Fatalf("Uncounted = %v, recorded %v: want the session still due", got, recorded(t, s, b.ID))
	}
	if n := strings.Count(log.String(), "session usage unread"); n != 1 {
		t.Fatalf("said %d times that the usage is unread, want once:\n%s", n, log.String())
	}
	r.set(func(c *countRunner) { c.usageErr = nil })
	passes(t, d, 1)
	wantRecorded(t, s, b.ID, map[string]map[string]any{session: counts(100, 100, 10, 0, 0)})
}

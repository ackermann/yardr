package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
)

// paneRunner stands in for herdr's runner: the agents of a yard by herdr's
// name for each, with what each is doing, and what yardr down did to them.
type paneRunner struct {
	mu sync.Mutex
	// status is what each agent is doing; one that is not listed is gone.
	status map[string]string
	// fail are the agents that cannot be stopped.
	fail map[string]bool
	// stopped and nudged are the agents down stopped and typed to, in order.
	stopped, nudged []string
	// onNudge is what an agent does with what it is typed.
	onNudge func(agent, text string)
	// onStart starts a session; nil starts nothing, as down's stand-in does.
	onStart func(dispatch.Job) (map[string]any, error)
}

func (p *paneRunner) Start(_ context.Context, j dispatch.Job) (map[string]any, error) {
	p.mu.Lock()
	onStart := p.onStart
	p.mu.Unlock()
	if onStart == nil {
		return nil, errors.New("down starts nothing")
	}
	return onStart(j)
}

func (p *paneRunner) Status(_ context.Context, h map[string]any) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status[h["agent"].(string)], nil
}

func (p *paneRunner) Alive(ctx context.Context, h map[string]any) (bool, error) {
	s, err := p.Status(ctx, h)
	return s != "", err
}

func (p *paneRunner) Stop(_ context.Context, h map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	agent := h["agent"].(string)
	if p.fail[agent] {
		return errors.New("herdr tab close: the tab is busy")
	}
	p.stopped = append(p.stopped, agent)
	delete(p.status, agent)
	return nil
}

// countRunner is paneRunner as a runner that reads what an agent used: down
// only asks whether it does, never for a count. It asks by a type assertion
// (dispatch.Countable), so a Usage that fell out of step with the interface
// would build and leave no count due; the assertion below makes it fail the
// build instead.
type countRunner struct{ *paneRunner }

var _ dispatch.UsageReader = countRunner{}

func (countRunner) Usage(context.Context, map[string]any, time.Time, bool) (store.Usage, bool, string, error) {
	return store.Usage{}, false, "", nil
}

type failedCountDueStore struct{ store.API }

func (failedCountDueStore) MarkCountDue(context.Context, string) error {
	return errors.New("count unavailable")
}

func (p *paneRunner) Nudge(_ context.Context, h map[string]any, text string) error {
	agent := h["agent"].(string)
	p.mu.Lock()
	p.nudged = append(p.nudged, agent)
	p.mu.Unlock()
	if p.onNudge != nil {
		p.onNudge(agent, text)
	}
	return nil
}

// downYard is a yard as init leaves it whose yardmaster runs, with a herdr
// group builders and a bead per name in beads, each with a running session
// of that group whose agent is named as the bead is; status is what each
// agent is doing, the yardmaster's under its own name. It returns the home,
// the store, the stand-in for herdr's runner, the beads and an app per call,
// and counts the stops of the yard's server.
func downYard(t *testing.T, status map[string]string, beads ...string) (home string, st store.API, panes *paneRunner, ids map[string]string, a func(json bool) *app, serverStops *int) {
	t.Helper()
	home, st, _ = statusYard(t, "")
	testYard(t, st, home)
	ctx := context.Background()
	panes = &paneRunner{status: status, fail: map[string]bool{}}
	stub(t, &yardRunners, func(string, store.API) map[string]dispatch.Runner {
		return map[string]dispatch.Runner{"herdr": panes}
	})
	serverStops = new(int)
	stub(t, &stopServer, func(*config.Config) error { *serverStops++; return nil })
	if err := st.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster", "tab": "w1:t1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "repo"}, "me"); err != nil {
		t.Fatal(err)
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 9, Config: map[string]any{"kind": "claude"}}, settings[store.OperatorSetting]); err != nil {
		t.Fatal(err)
	}
	ids = map[string]string{}
	for _, name := range beads {
		b, err := st.CreateBead(ctx, store.NewBead{Title: name, Depot: "repo"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = b.ID
		dispatched(t, st, b.ID, name)
	}
	return home, st, panes, ids, func(json bool) *app {
		a := appOn(st)
		a.actor = "me"
		a.json = json
		return a
	}, serverStops
}

// dispatched gives bead a running session of builders with the agent named, as
// the daemon does when it dispatches it.
func dispatched(t *testing.T, st store.API, bead, agent string) {
	t.Helper()
	ctx := context.Background()
	b, err := st.GetBead(ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.Claim(ctx, bead, "builders", b.Stage, "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkRunning(ctx, sess.ID, map[string]any{"agent": agent, "tab": agent + ":t1", "worktree": "/w/" + agent}); err != nil {
		t.Fatal(err)
	}
}

// queued fails unless bead is back in its stage's queue as it was before
// its session: owned by no group, not held, and with no failed attempt
// counted.
func queued(t *testing.T, st store.API, bead string) {
	t.Helper()
	ctx := context.Background()
	b, err := st.GetBead(ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.FailureCount(ctx, bead, b.Stage)
	if err != nil {
		t.Fatal(err)
	}
	if b.Group != nil || b.Session != nil || b.Hold || n != 0 || b.Status != "open" {
		t.Fatalf("bead %s after down: group %v session %v hold %v failures %d status %s", bead, b.Group, b.Session, b.Hold, n, b.Status)
	}
}

// With an agent in the middle of a turn and nobody to ask, down refuses and
// stops nothing; its warning names the working agent by its bead and counts
// the idle ones. --json hands the same list to a program, and stops nothing
// either. --yes stops every agent the yard started, the daemon first and the
// server last, and no other: the beads are back in their queue with no
// failed attempt, the crew member wrote its handoff and stays enabled.
func TestDownStopsTheYardsAgents(t *testing.T) {
	home, st, panes, ids, a, serverStops := downYard(t,
		map[string]string{"yardmaster": "idle", "busy": "working", "quiet": "idle"}, "busy", "quiet")
	ctx := context.Background()
	// A person's own claim is no agent of the yard's.
	if err := st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 9}, "me"); err != nil {
		t.Fatal(err)
	}
	mine, err := st.CreateBead(ctx, store.NewBead{Title: "mine", Depot: "repo"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx, mine.ID, "people", mine.Stage, "yard"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a(false).rootCmd(), "down")
	var refused errDownRefused
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "1 agent is in the middle of something") ||
		!strings.Contains(err.Error(), hereAt(home, "yardr down --yes")) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out,
		"yardr down stops 3 agents of the yard in "+home+":\n",
		"  working  bead "+ids["busy"]+" (stage new, group builders)\n",
		"  2 idle\n")
	if len(panes.stopped)+len(panes.nudged)+*serverStops != 0 {
		t.Fatalf("a refused down stopped %v, nudged %v, stopped the server %d times", panes.stopped, panes.nudged, *serverStops)
	}

	out, err = run(t, a(true).rootCmd(), "down", "--json")
	var rep downReport
	if jerr := json.Unmarshal([]byte(out), &rep); err != nil || jerr != nil {
		t.Fatalf("down --json: %v %v\n%s", err, jerr, out)
	}
	if !rep.Confirm || rep.Down || rep.Working != 1 || rep.Idle != 2 || len(rep.Agents) != 3 ||
		rep.Agents[0].Name != ids["busy"] || rep.Agents[0].State != "working" || rep.Agents[0].Kind != "bead" || rep.Agents[0].Stopped {
		t.Fatalf("down --json: %+v", rep)
	}
	if len(panes.stopped)+*serverStops != 0 {
		t.Fatalf("down --json without --yes stopped %v", panes.stopped)
	}

	// The yardmaster writes its handoff when it is asked to.
	panes.onNudge = func(agent, text string) {
		if !strings.Contains(text, "yardr crew handoff yardmaster") {
			t.Errorf("the nudge names no handoff: %s", text)
		}
		if _, err := st.Journal(ctx, agent, store.NoteHandoff, "stopping", agent); err != nil {
			t.Error(err)
		}
	}
	out, err = run(t, a(false).rootCmd(), "down", "--yes")
	if err != nil {
		t.Fatalf("down --yes: %v\n%s", err, out)
	}
	wantText(t, out,
		"yardr down stops 3 agents of the yard in "+home+":\n",
		"  working  bead "+ids["busy"]+" (stage new, group builders)\n",
		"  2 idle\n",
		"daemon          was not running\n",
		"agents          stopped 3: 2 bead sessions, back in the queue with worktree, branch and attempts as they were; 1 crew session (1 wrote a handoff)\n",
		"server          stopped (pid 4711)\n",
		"herdr's server  left running: other yards and your own panes are in it\n",
		"The yard in "+home+" is down. Start it again: "+hereAt(home, "yardr up")+"\n")
	slices.Sort(panes.stopped)
	if !slices.Equal(panes.stopped, []string{"busy", "quiet", "yardmaster"}) || !slices.Equal(panes.nudged, []string{"yardmaster"}) || *serverStops != 1 {
		t.Fatalf("stopped %v, nudged %v, server stops %d", panes.stopped, panes.nudged, *serverStops)
	}
	queued(t, st, ids["busy"])
	queued(t, st, ids["quiet"])
	if b, _ := st.GetBead(ctx, mine.ID); b.Group == nil || *b.Group != "people" {
		t.Fatalf("a person's claim was taken away: %+v", b)
	}
	if r, _ := st.GetCrew(ctx, "yardmaster"); r.Live() || !r.Enabled || r.Failures != 0 {
		t.Fatalf("the yardmaster after down: %+v", r)
	}

	// Again, on a yard that is down: nothing to stop, and no question.
	out, err = run(t, a(false).rootCmd(), "down")
	if err != nil || !strings.Contains(out, "agents          none was running\n") || len(panes.stopped) != 3 {
		t.Fatalf("a second down: %v, stopped %v\n%s", err, panes.stopped, out)
	}

	t.Run("count due", downLeavesACountDueForTheSessionsItStops)
	t.Run("failed count write stays retryable", func(t *testing.T) {
		ctx := context.Background()
		_, st, panes, ids, _, _ := downYard(t, map[string]string{"yardmaster": "idle", "busy": "idle"}, "busy")
		sessions, err := st.ListSessions(ctx, store.SessionFilter{All: true, Bead: ids["busy"]})
		if err != nil || len(sessions) != 1 {
			t.Fatalf("sessions = %v, %v, want one", sessions, err)
		}
		sess := sessions[0]
		g := &yardAgent{Kind: "bead", Name: sess.Bead, runner: "herdr", session: sess.ID, handle: sess.Handle}
		stopAgent(ctx, failedCountDueStore{st}, map[string]dispatch.Runner{"herdr": countRunner{panes}}, g, false, "yardr")
		if g.Stopped || !strings.Contains(g.Error, "count unavailable") {
			t.Fatalf("agent after count write failed: %+v, want the stop to report the error", g)
		}
		unstopped, err := st.Unstopped(ctx)
		if err != nil || len(unstopped) != 1 || unstopped[0].ID != sess.ID {
			t.Fatalf("unstopped after count write failed = %v, %v, want session %s for the dispatcher to retry", unstopped, err, sess.ID)
		}
	})
}

// A bead's session that down stops is left with its count due, as one the
// dispatcher stops is: count_due on the handle, up to a cutoff for a group
// that keeps its agents. An exec session is stopped with none due, and a
// person's claim is left as it was.
func downLeavesACountDueForTheSessionsItStops(t *testing.T) {
	t.Helper()
	_, st, panes, ids, a, _ := downYard(t, map[string]string{"yardmaster": "idle", "busy": "idle", "kept": "idle"}, "busy")
	stub(t, &yardRunners, func(string, store.API) map[string]dispatch.Runner {
		return map[string]dispatch.Runner{"herdr": countRunner{panes}}
	})
	ctx := context.Background()
	if err := st.PutGroup(ctx, store.Group{Name: "keepers", Runner: "herdr", Limit: 9, Config: map[string]any{"keep": true}}, "me"); err != nil {
		t.Fatal(err)
	}
	running := func(title, group string, handle map[string]any) string {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: title, Depot: "repo"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := st.Claim(ctx, b.ID, group, "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.MarkRunning(ctx, sess.ID, handle); err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	kept := running("kept", "keepers", map[string]any{"agent": "kept", "tab": "kept:t1", "worktree": "/w/kept"})
	if err := st.PutGroup(ctx, store.Group{Name: "sh", Runner: "exec", Limit: 9, Config: map[string]any{"cmd": "true"}}, "Jo"); err != nil {
		t.Fatal(err)
	}
	execBead := running("exec", "sh", map[string]any{"pid": 4242})
	if err := st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Limit: 9}, "me"); err != nil {
		t.Fatal(err)
	}
	mine, err := st.CreateBead(ctx, store.NewBead{Title: "mine", Depot: "repo"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx, mine.ID, "people", mine.Stage, "yard"); err != nil {
		t.Fatal(err)
	}

	if out, err := run(t, a(false).rootCmd(), "down", "--yes", "--handoff-wait", "0"); err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	slices.Sort(panes.stopped)
	if !slices.Equal(panes.stopped, []string{"busy", "kept", "yardmaster"}) {
		t.Fatalf("stopped %v, want the two agents and the yardmaster", panes.stopped)
	}
	session := func(bead string) store.Session {
		t.Helper()
		ss, err := st.ListSessions(ctx, store.SessionFilter{All: true, Bead: bead})
		if err != nil || len(ss) != 1 {
			t.Fatalf("sessions of %s = %v, %v, want one", bead, ss, err)
		}
		return ss[0]
	}
	busy := session(ids["busy"])
	due, _ := busy.Handle[store.CountDue].(map[string]any)
	if !busy.Stopped || len(due) != 1 {
		t.Fatalf("busy session = %+v, due %v: want stopped, with stopped_at alone", busy, due)
	}
	if at, _ := due[store.CountStoppedAt].(string); at == "" {
		t.Errorf("count_due of a plain session = %v, want stopped_at", due)
	} else if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
		t.Errorf("stopped_at = %q, want a time: %v", at, err)
	}
	ks := session(kept)
	kdue, _ := ks.Handle[store.CountDue].(map[string]any)
	untilAt, _ := kdue[store.CountUntil].(string)
	until, err := time.Parse(time.RFC3339Nano, untilAt)
	if !ks.Stopped || err != nil || ks.EndedAt == nil || !until.Equal(ks.EndedAt.Add(2*time.Minute)) {
		t.Fatalf("kept session = %+v, due %v: want stopped, with until two minutes after it ended (%v)", ks, kdue, err)
	}
	if s := session(execBead); !s.Stopped || s.Handle[store.CountDue] != nil {
		t.Errorf("exec session = %+v: want stopped, with no count due", s)
	}
	if s := session(mine.ID); s.Stopped || s.Handle[store.CountDue] != nil {
		t.Errorf("manual session = %+v: want it as it was, with no count due", s)
	}
	if b, _ := st.GetBead(ctx, mine.ID); b.Group == nil || *b.Group != "people" {
		t.Errorf("manual claim after down: group %v, want people", b.Group)
	}
	uncounted, err := st.Uncounted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var dueIDs []string
	for _, s := range uncounted {
		dueIDs = append(dueIDs, s.Bead)
	}
	slices.Sort(dueIDs)
	want := []string{ids["busy"], kept}
	slices.Sort(want)
	if !slices.Equal(dueIDs, want) {
		t.Errorf("Uncounted beads = %v, want %v", dueIDs, want)
	}
}

// A crew member's conversation is a separate herdr agent and must stop
// with its line session when the yard goes down.
func TestDownStopsCrewConversationSessions(t *testing.T) {
	_, st, panes, _, a, _ := downYard(t, map[string]string{"yardmaster": "idle", "conversation": "idle"})
	ctx := context.Background()
	mustAs(t, st, "Jo", "user", "add", "hal")
	mustAs(t, st, "Jo", "crew", "set", "yardmaster", "for=Jo,hal", "env_for_hal=/tmp/hal.env")
	if err := st.CrewStartedFor(ctx, "yardmaster", "hal", map[string]any{"agent": "conversation", "tab": "w2:t1"}); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a(true).rootCmd(), "down", "--yes", "--handoff-wait", "0", "--json")
	if err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	var rep downReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("down --json: %v\n%s", err, out)
	}
	if len(rep.Agents) != 2 {
		t.Fatalf("down agents = %+v, want line and hal conversation", rep.Agents)
	}
	sawConversation := false
	for _, agent := range rep.Agents {
		if agent.Kind == "crew" && agent.For == "hal" && agent.Stopped {
			sawConversation = true
		}
	}
	if !sawConversation {
		t.Errorf("down report omits stopped hal conversation: %+v", rep.Agents)
	}
	slices.Sort(panes.stopped)
	if !slices.Equal(panes.stopped, []string{"conversation", "yardmaster"}) {
		t.Errorf("stopped agents = %v, want both crew sessions", panes.stopped)
	}
	r, err := st.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if r.Live() || r.Session("hal").Live() {
		t.Errorf("crew sessions still running after down: line=%+v hal=%+v", r.CrewSession, r.Session("hal"))
	}
}

// Three downs do not hold a bead: each ends its session as one the runtime
// could not keep, which counts no failed attempt, where three sessions that
// died would have held it (yardr daemon --max-attempts 3).
func TestThreeDownsHoldNoBead(t *testing.T) {
	_, st, panes, ids, a, _ := downYard(t, map[string]string{"b": "working"}, "b")
	for i := 0; i < 3; i++ {
		if i > 0 {
			// yardr up: the daemon dispatches it again.
			dispatched(t, st, ids["b"], "b")
			panes.status["b"] = "working"
		}
		if out, err := run(t, a(false).rootCmd(), "down", "--yes", "--handoff-wait", "0"); err != nil {
			t.Fatalf("down %d: %v\n%s", i+1, err, out)
		}
		queued(t, st, ids["b"])
	}
	// The yardmaster's pane, whose agent was gone, is closed with the first.
	if !slices.Equal(panes.stopped, []string{"b", "yardmaster", "b", "b"}) || len(panes.nudged) != 0 {
		t.Fatalf("stopped %v, nudged %v", panes.stopped, panes.nudged)
	}
	sessions, err := st.ListSessions(context.Background(), store.SessionFilter{All: true, Bead: ids["b"]})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.State != "aborted" || !s.Stopped {
			t.Fatalf("session %s: state %s, stopped %v", s.ID, s.State, s.Stopped)
		}
	}
}

// Idle agents alone are not asked about: down says how many it stops and
// stops them, with nobody to ask and no --yes. A program is handed them all
// the same (--json, see TestDownStopsTheYardsAgents).
func TestDownDoesNotAskAboutIdleAgents(t *testing.T) {
	_, st, panes, ids, a, _ := downYard(t, map[string]string{"yardmaster": "idle", "quiet": "idle"}, "quiet")
	out, err := run(t, a(false).rootCmd(), "down", "--handoff-wait", "20ms")
	if err != nil || len(panes.stopped) != 2 {
		t.Fatalf("down: %v, stopped %v\n%s", err, panes.stopped, out)
	}
	wantText(t, out, "  2 idle\n", "1 crew session (none wrote a handoff)\n")
	queued(t, st, ids["quiet"])
}

// In a terminal down asks before it stops an agent in the middle of a turn:
// a no leaves the yard as it is, a yes stops it.
func TestDownAsksInATerminal(t *testing.T) {
	_, st, panes, ids, a, _ := downYard(t, map[string]string{"b": "working"}, "b")
	stub(t, &interactive, func() bool { return true })
	// The question starts at no: left as it is, nothing is stopped.
	asked := 0
	stub(t, &ask, func(fields ...huh.Field) error { asked++; return nil })
	out, err := run(t, a(false).rootCmd(), "down", "--handoff-wait", "0")
	if err == nil || err.Error() != "nothing was stopped" || len(panes.stopped) != 0 || asked != 1 {
		t.Fatalf("answered no: %v, asked %d times, stopped %v\n%s", err, asked, panes.stopped, out)
	}
	if b, _ := st.GetBead(context.Background(), ids["b"]); b.Session == nil {
		t.Fatal("answered no, and the bead lost its session")
	}
	stub(t, &ask, func(fields ...huh.Field) error {
		for _, f := range fields {
			f.WithKeyMap(huh.NewDefaultKeyMap())
			f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
		}
		return nil
	})
	if out, err := run(t, a(false).rootCmd(), "down", "--handoff-wait", "0"); err != nil || !slices.Equal(panes.stopped, []string{"b", "yardmaster"}) {
		t.Fatalf("answered yes: %v, stopped %v\n%s", err, panes.stopped, out)
	}
}

// If reading the agent list had to start the yard's server, declining the
// warning leaves the yard as it was: the temporary server is stopped too.
func TestDownDeclineStopsInspectionServer(t *testing.T) {
	_, _, _, _, a, serverStops := downYard(t, map[string]string{"b": "working"}, "b")
	stub(t, &serverRunning, func(context.Context, *config.Config) bool { return false })
	stub(t, &interactive, func() bool { return true })
	stub(t, &ask, func(...huh.Field) error { return nil })
	out, err := run(t, a(false).rootCmd(), "down", "--handoff-wait", "0")
	if err == nil || err.Error() != "nothing was stopped" || *serverStops != 1 {
		t.Fatalf("declined down: %v, server stops %d\n%s", err, *serverStops, out)
	}
}

// A service that could not be stopped keeps down from reporting success,
// even when the daemon, agents and yard server did stop.
func TestDownFailsWhenOptionalServiceCannotStop(t *testing.T) {
	home, _, _, _, a, _ := downYard(t, map[string]string{"yardmaster": "idle"})
	cfg := config.At(home)
	clear, err := kindOf(svcBDP).announce(cfg, map[string]any{"addr": "127.0.0.1:8765"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clear)
	stub(t, &signalStop, func(int) error { return errors.New("permission denied") })
	out, err := run(t, a(false).rootCmd(), "down", "--yes", "--handoff-wait", "0")
	if !errors.Is(err, errCheckFailed) || !strings.Contains(out, "BDP server") || !strings.Contains(out, "NOT stop the BDP server") || !strings.Contains(out, "is not down: see NOT above.") {
		t.Fatalf("down with a running BDP server: %v\n%s", err, out)
	}
}

// An agent that cannot be stopped keeps its session, is said to be NOT
// stopped with what to do, and the exit status is 1; the rest is stopped.
func TestDownSaysWhatItCouldNotStop(t *testing.T) {
	home, st, panes, ids, a, _ := downYard(t, map[string]string{"stuck": "idle", "fine": "idle"}, "stuck", "fine")
	panes.fail["stuck"] = true
	out, err := run(t, a(false).rootCmd(), "down", "--yes", "--handoff-wait", "0")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out,
		"agents          NOT stopped: bead "+ids["stuck"]+" (stage new, group builders) could not be stopped: herdr tab close: the tab is busy\n",
		"                  "+hereAt(home, "yardr down")+", once more\n",
		"is not down: see NOT above.\n")
	if b, _ := st.GetBead(context.Background(), ids["stuck"]); b.Session == nil {
		t.Fatal("a session whose agent still runs was ended")
	}
	queued(t, st, ids["fine"])

	rep := downJSON(t, a(true), "--yes")
	i := slices.IndexFunc(rep.Agents, func(g yardAgent) bool { return g.Name == ids["stuck"] })
	if rep.Down || rep.Confirm || i < 0 || rep.Agents[i].Stopped || rep.Agents[i].Error == "" || rep.Agents[i].Do == "" {
		t.Fatalf("json: %+v", rep)
	}
}

// downJSON runs yardr down --json with flags.
func downJSON(t *testing.T, a *app, flags ...string) downReport {
	t.Helper()
	out, _ := run(t, a.rootCmd(), append([]string{"down", "--json", "--handoff-wait", "0"}, flags...)...)
	var rep downReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("down --json: %v\n%s", err, out)
	}
	return rep
}

// With herdr's server down no pane is left to close: the sessions are ended
// all the same, so that the next daemon does not find them gone and count
// each as a failed attempt.
func TestDownWithHerdrsServerDown(t *testing.T) {
	_, st, panes, ids, a, _ := downYard(t, map[string]string{"b": "working"}, "b")
	stub(t, &herdrServer, func(context.Context, string) serverCheck { return serverCheck{State: herdrDown} })
	out, err := run(t, a(false).rootCmd(), "down")
	if err != nil || len(panes.stopped) != 0 {
		t.Fatalf("down: %v, stopped %v\n%s", err, panes.stopped, out)
	}
	wantText(t, out, "herdr's server  not running\n")
	queued(t, st, ids["b"])
}

// A crew member whose start was underway when its daemon was stopped has a
// pane and no session: down closes that pane too.
func TestDownStopsAStartThatWasUnderway(t *testing.T) {
	_, st, panes, _, a, _ := downYard(t, map[string]string{"starting": "idle"})
	ctx := context.Background()
	if err := st.CrewStopped(ctx, "yardmaster", "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.CrewStarting(ctx, "yardmaster", map[string]any{"agent": "starting", "tab": "w9:t1"}); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a(false).rootCmd(), "down", "--handoff-wait", "0"); err != nil || !slices.Equal(panes.stopped, []string{"starting"}) {
		t.Fatalf("down: %v, stopped %v\n%s", err, panes.stopped, out)
	}
	if r, _ := st.GetCrew(ctx, "yardmaster"); r.Starting != nil {
		t.Fatalf("the start is still underway to the store: %v", r.Starting)
	}
	if err := st.CrewStarting(ctx, "yardmaster", map[string]any{"agent": "long gone", "tab": "w9:t1"}); err != nil {
		t.Fatal(err)
	}
	// What a start left whose pane is gone is no agent.
	if rep := downJSON(t, a(true)); rep.Confirm || len(rep.Agents) != 0 {
		t.Fatalf("a pane that is gone: %+v", rep)
	}
}

// upYard is a yard as init leaves it with herdr on PATH, whose server
// answers, and no daemon. It counts what up starts: herdr's server, which
// answers once it was started unless it ran already, and the daemon, which
// holds the home's lock from then.
func upYard(t *testing.T, herdrRuns bool) (home string, st store.API, a func(json bool) *app, herdrStarts, daemonStarts *int) {
	t.Helper()
	home, st, _ = statusYard(t, "")
	testYard(t, st, home)
	stubLookPath(t, "claude", "git", "herdr")
	herdrStarts, daemonStarts = new(int), new(int)
	stub(t, &herdrServer, func(context.Context, string) serverCheck {
		if herdrRuns || *herdrStarts > 0 {
			return serverCheck{State: herdrUp}
		}
		return serverCheck{State: herdrDown, Output: "server_not_running"}
	})
	stub(t, &spawnHerdr, func(bin, log string) (<-chan error, error) {
		if bin != "/bin/herdr" || log != filepath.Join(home, "herdr-server.log") {
			t.Errorf("herdr server started as %s, logging to %s", bin, log)
		}
		*herdrStarts++
		return nil, nil
	})
	stub(t, &spawn, func(_ []string, cfg *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		if k.Name != svcDaemon || len(args) != 0 {
			t.Errorf("up started yardr %s %v", k.Name, args)
		}
		*daemonStarts++
		unlock, err := lockDaemon(cfg.DaemonLock(), 0)
		if err != nil {
			return 0, nil, nil, err
		}
		t.Cleanup(unlock)
		return os.Getpid(), nil, func() error { return nil }, nil
	})
	return home, st, func(json bool) *app {
		a := appOn(st)
		a.actor = "me"
		a.json = json
		return a
	}, herdrStarts, daemonStarts
}

// From nothing up starts herdr's server and the daemon, in that order with
// the yard's server between, and says so; a second up finds all of it
// running and starts nothing.
func TestUpStartsWhatDoesNotRun(t *testing.T) {
	home, _, a, herdrStarts, daemonStarts := upYard(t, false)
	stub(t, &serverRunning, func(context.Context, *config.Config) bool { return false })
	out, err := run(t, a(false).rootCmd(), "up")
	if err != nil || *herdrStarts != 1 || *daemonStarts != 1 {
		t.Fatalf("up: %v, herdr started %d times, the daemon %d\n%s", err, *herdrStarts, *daemonStarts, out)
	}
	wantText(t, out,
		"herdr's server  started\nserver          started (pid 4711)\ndaemon          started (pid ",
		"The yard in "+home+" is up.",
		"Stop it all: "+hereAt(home, "yardr down")+"\n")
	if strings.Contains(out, "web view") || strings.Contains(out, "BDP") {
		t.Errorf("up speaks of a part the yard has no address for:\n%s", out)
	}
	if strings.Contains(out, herdrInstall) {
		t.Errorf("up says how to get herdr, which is on PATH:\n%s", out)
	}

	stub(t, &serverRunning, func(context.Context, *config.Config) bool { return true })
	out, err = run(t, a(false).rootCmd(), "up")
	if err != nil || *herdrStarts != 1 || *daemonStarts != 1 {
		t.Fatalf("a second up: %v, herdr started %d times, the daemon %d\n%s", err, *herdrStarts, *daemonStarts, out)
	}
	wantText(t, out, "herdr's server  running already\nserver          running already (pid 4711)\ndaemon          running already (pid ")

	out, err = run(t, a(true).rootCmd(), "up", "--json")
	var rep upReport
	if jerr := json.Unmarshal([]byte(out), &rep); err != nil || jerr != nil {
		t.Fatalf("up --json: %v %v\n%s", err, jerr, out)
	}
	var parts []string
	for _, p := range rep.Parts {
		parts = append(parts, p.Part)
		if p.Started || p.Error != "" || p.Running != (p.Part == partHerdr || p.Part == partServer || p.Part == svcDaemon) {
			t.Errorf("part %+v", p)
		}
	}
	if !rep.Up || !slices.Equal(parts, []string{"herdr", "server", "daemon", "web", "bdp"}) {
		t.Fatalf("up --json: %+v", rep)
	}
}

// Without herdr up says where it looked and what to do, the installer's
// command with its page among it as doctor says it, and starts no daemon:
// every agent it started would fail and count as a failed start.
func TestUpWithoutHerdr(t *testing.T) {
	home, _, a, herdrStarts, daemonStarts := upYard(t, false)
	stubLookPath(t, "claude", "git")
	t.Setenv("PATH", "/usr/bin:/bin")
	out, err := run(t, a(false).rootCmd(), "up")
	if !errors.Is(err, errCheckFailed) || *herdrStarts+*daemonStarts != 0 {
		t.Fatalf("up: %v, herdr started %d times, the daemon %d\n%s", err, *herdrStarts, *daemonStarts, out)
	}
	wantText(t, out,
		"herdr's server  NOT herdr is not installed, or not where yardr looks: PATH (/usr/bin:/bin); SHELL names no login shell to ask\n",
		"                  install herdr with "+herdrInstall+" (more: https://herdr.dev), then: "+hereAt(home, "yardr up")+"\n",
		"server          running already (pid 4711)\n",
		"daemon          NOT herdr's server does not run",
		"is not up: see NOT above.\n")
}

// A window starts yardr without what a profile adds to PATH. up asks the
// login shell for herdr, as doctor does, and what it starts then gets that
// shell's PATH after its own: the daemon looks for herdr, git and the
// harnesses on it.
func TestUpFindsHerdrWithTheLoginShell(t *testing.T) {
	home, _, a, herdrStarts, daemonStarts := upYard(t, false)
	stubLookPath(t, "claude", "git")
	local := t.TempDir()
	herdr := filepath.Join(local, "herdr")
	// The test is about what up does with what the login shell answers,
	// not about asking it, so it stubs the answers instead of starting
	// one: no spawn to stretch under load. The shell itself is named so
	// that up asks at all.
	stub(t, &loginShell, func() string { return "/bin/sh" })
	stub(t, &loginShellFinds, func(context.Context, []string) map[string]string { return map[string]string{"herdr": herdr} })
	stub(t, &loginShellPATH, func(context.Context) string { return local + ":/opt/tools:/usr/bin:/bin" })
	t.Setenv("PATH", "/usr/bin:/bin")
	stub(t, &spawnHerdr, func(bin, _ string) (<-chan error, error) {
		if bin != herdr {
			t.Errorf("herdr server started as %s", bin)
		}
		*herdrStarts++
		return nil, nil
	})
	// The daemon is started from the run's environment, which takes PATH
	// from the process as it is when the daemon starts.
	var daemon []string
	started := spawn
	stub(t, &spawn, func(environ []string, cfg *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		daemon = daemonEnv(environ, cfg.Home)
		return started(environ, cfg, k, args)
	})
	// A run with an environment of its own, as Execute gives one, taken
	// before up merges PATH.
	x := a(false)
	x.env = environMap(os.Environ())
	// On the yard's home: the process's names TestMain's, with no yard.
	x.env["YARDR_HOME"] = home
	out, err := run(t, x.rootCmd(), "up")
	if err != nil || *herdrStarts != 1 || *daemonStarts != 1 {
		t.Fatalf("up: %v, herdr started %d times, the daemon %d\n%s", err, *herdrStarts, *daemonStarts, out)
	}
	wantText(t, out, "herdr's server  started: herdr is "+herdr+", which your login shell finds and this PATH does not")
	want := "/usr/bin:/bin:" + local + ":/opt/tools"
	if got := os.Getenv("PATH"); got != want {
		t.Fatalf("PATH for what up starts = %s, want %s", got, want)
	}
	if !slices.Contains(daemon, "PATH="+want) || !slices.Contains(daemon, "YARDR_HOME="+home) {
		t.Fatalf("the daemon started with\n%s\nwant PATH=%s and YARDR_HOME=%s", strings.Join(daemon, "\n"), want, home)
	}
}

// Without a login service of its own, what up starts as its child gets the
// login shell's PATH after its own: from a card or a run, up's own PATH is
// the server's short one, on which the daemon would find no harness
// (yardr-b2rb). The shell is stubbed, as in the herdr test above: the test
// is about what up does with its answer.
func TestUpWithoutAServiceUsesTheLoginShellsPATH(t *testing.T) {
	home, _, a, _, daemonStarts := upYard(t, true)
	dir := t.TempDir()
	stub(t, &serviceDir, func() (string, error) { return dir, nil })
	local := t.TempDir()
	stub(t, &loginShellPATH, func(context.Context) string { return local + ":/usr/bin:/bin" })
	t.Setenv("PATH", "/usr/bin:/bin")
	var daemon []string
	started := spawn
	stub(t, &spawn, func(environ []string, cfg *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		daemon = daemonEnv(environ, cfg.Home)
		return started(environ, cfg, k, args)
	})
	// A run with an environment of its own, as Execute gives one, taken
	// before up merges PATH.
	x := a(false)
	x.env = environMap(os.Environ())
	// On the yard's home: the process's names TestMain's, with no yard.
	x.env["YARDR_HOME"] = home
	out, err := run(t, x.rootCmd(), "up")
	if err != nil || *daemonStarts != 1 {
		t.Fatalf("up: %v, the daemon started %d times\n%s", err, *daemonStarts, out)
	}
	want := "/usr/bin:/bin:" + local
	if got := os.Getenv("PATH"); got != want {
		t.Fatalf("PATH for what up starts = %s, want %s", got, want)
	}
	if !slices.Contains(daemon, "PATH="+want) {
		t.Fatalf("the daemon started with\n%s\nwant PATH=%s", strings.Join(daemon, "\n"), want)
	}
}

// up starts the web view when the yard's web_addr setting names its
// address, as yardr web start does.
func TestUpStartsTheWebViewTheYardNames(t *testing.T) {
	home, st, a, _, _ := upYard(t, true)
	if err := st.SetSetting(context.Background(), store.WebAddrSetting, "127.0.0.1:1", "Jo"); err != nil {
		t.Fatal(err)
	}
	stubServices(t, "darwin")
	u, err := newUnit(os.Environ(), config.At(home), kindOf(svcWeb), []string{"--addr", "127.0.0.1:8791"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.Path, []byte(u.render()), 0o600); err != nil {
		t.Fatal(err)
	}
	var serviceCalls []string
	stub(t, &serviceCtl, func(_ context.Context, name string, args ...string) (string, error) {
		serviceCalls = append(serviceCalls, name+" "+strings.Join(args, " "))
		return "", errors.New("unexpected service start")
	})
	var started []string
	stub(t, &spawn, func(_ []string, cfg *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		started = append(started, k.Name+" "+strings.Join(args, " "))
		if k.Name == svcDaemon {
			unlock, err := lockDaemon(cfg.DaemonLock(), 0)
			if err != nil {
				return 0, nil, nil, err
			}
			t.Cleanup(unlock)
		}
		return os.Getpid(), nil, func() error { return nil }, nil
	})
	// The address answers once the view was started.
	stub(t, &answers, func(string) bool { return len(started) == 2 })
	out, err := run(t, a(false).rootCmd(), "up")
	if err != nil || !slices.Equal(started, []string{"daemon ", "web --addr 127.0.0.1:1"}) {
		t.Fatalf("up: %v, started %q\n%s", err, started, out)
	}
	if len(serviceCalls) != 0 {
		t.Errorf("up tried the installed web service at its stale address instead of starting the configured address: %q", serviceCalls)
	}
	wantText(t, out, "web view        started (pid ", "): http://127.0.0.1:1/\n")
}

// A web view its login service runs is the yard's web view even when no
// web_addr is set: the service's file names its address, as on a yard
// installed on the default. yardr down stops it, and yardr up starts it
// again through the service, on the file's address, instead of leaving it
// stopped without a line (yardr-6fh0, yardr-b2rb).
func TestUpDownStartsTheServiceWebView(t *testing.T) {
	const addr = "127.0.0.1:18791"
	for _, system := range []string{"darwin", "linux"} {
		home, st, a, _, _ := upYard(t, true)
		_, m := stubServices(t, system)
		cfg := config.At(home)
		if all, err := st.Settings(context.Background()); err != nil || all[store.WebAddrSetting] != "" {
			t.Fatalf("%s: web_addr = %q, %v, want unset", system, all[store.WebAddrSetting], err)
		}
		u, err := newUnit(os.Environ(), cfg, kindOf(svcWeb), []string{"--addr", addr})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(u.Path, []byte(u.render()), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := serviceAddr(cfg, svcWeb); got != addr {
			t.Fatalf("%s: the service's address = %q, want %s", system, got, addr)
		}
		// The manager starts the service it is told to, as the real one
		// does; the address answers once it has.
		webUp := false
		stub(t, &serviceCtl, func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := m.ctl(ctx, name, args...)
			line := name + " " + strings.Join(args, " ")
			if err == nil && strings.Contains(line, "web") &&
				(strings.Contains(line, "bootstrap") || strings.Contains(line, "restart") || strings.Contains(line, "kickstart")) {
				webUp = true
			}
			return out, err
		})
		var started []string
		stub(t, &spawn, func(_ []string, cfg *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
			started = append(started, k.Name+" "+strings.Join(args, " "))
			if k.Name == svcDaemon {
				unlock, err := lockDaemon(cfg.DaemonLock(), 0)
				if err != nil {
					return 0, nil, nil, err
				}
				t.Cleanup(unlock)
			}
			return os.Getpid(), nil, func() error { return nil }, nil
		})
		stub(t, &answers, func(a string) bool { return a == addr && (webUp || slices.Contains(started, "web --addr "+a)) })
		stub(t, &stopServer, func(*config.Config) error { return nil })
		stub(t, &yardRunners, func(string, store.API) map[string]dispatch.Runner { return map[string]dispatch.Runner{} })
		// The service's view runs, as it does until yardr down stops it.
		running(t, cfg, kindOf(svcWeb), map[string]any{"addr": addr})
		out, err := run(t, a(false).rootCmd(), "down", "--yes", "--handoff-wait", "0")
		if err != nil {
			t.Fatalf("%s: down: %v\n%s", system, err, out)
		}
		if len(kindOf(svcWeb).running(cfg)) != 0 {
			t.Fatalf("%s: the web view still runs after down", system)
		}
		wantText(t, out, "web view        stopped (pid ")
		if strings.Contains(out, "does not start it again") {
			t.Errorf("%s: down says up does not start it again:\n%s", system, out)
		}
		out, err = run(t, a(false).rootCmd(), "up")
		if err != nil {
			t.Fatalf("%s: up: %v\n%s", system, err, out)
		}
		if !slices.Equal(started, []string{"daemon "}) {
			t.Errorf("%s: up started %q as its children, want the daemon alone: the web view starts through its service", system, started)
		}
		want := "launchctl bootstrap " + domain() + " " + u.Path
		if system == "linux" {
			want = "systemctl --user restart " + u.Name
		}
		if calls := strings.Join(m.calls, "\n"); !strings.Contains(calls, want) {
			t.Errorf("%s: the manager was not told to start the web view:\n%s", system, calls)
		}
		wantText(t, out, "web view        started", "http://"+addr+"/")
		if strings.Contains(out, "BDP") {
			t.Errorf("%s: up speaks of the BDP server the yard has no address for:\n%s", system, out)
		}
	}
}

// up does not start the BDP server on an address yardr bdp serve would
// refuse: it says why and starts nothing, as it does for the web view,
// instead of waiting out a server that ends at once. The setting names
// the part's address, and YARDR_BDP_ADDR takes it off loopback, as a
// stored value is where the yard was set up before settings set held
// bdp_addr to loopback.
func TestUpRefusesTheBDPServerOffLoopback(t *testing.T) {
	home, st, a, _, _ := upYard(t, true)
	if err := st.SetSetting(context.Background(), store.BDPAddrSetting, "127.0.0.1:8765", "Jo"); err != nil {
		t.Fatal(err)
	}
	testYard(t, st, home, "YARDR_BDP_ADDR=0.0.0.0:8765")
	var started []string
	stub(t, &spawn, func(_ []string, cfg *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		started = append(started, k.Name+" "+strings.Join(args, " "))
		if k.Name == svcDaemon {
			unlock, err := lockDaemon(cfg.DaemonLock(), 0)
			if err != nil {
				return 0, nil, nil, err
			}
			t.Cleanup(unlock)
		}
		return os.Getpid(), nil, func() error { return nil }, nil
	})
	stub(t, &answers, func(string) bool { return true })
	out, err := run(t, a(true).rootCmd(), "up", "--json")
	var rep upReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("up --json: %v %v\n%s", err, jerr, out)
	}
	var part yardPart
	for _, p := range rep.Parts {
		if p.Part == svcBDP {
			part = p
		}
	}
	if part.Running || part.Started || !strings.Contains(part.Error, "--addr 0.0.0.0:8765 refused") || !strings.Contains(part.Error, "yardr bdp has no authentication") {
		t.Errorf("the BDP part: %+v", part)
	}
	if want := []string{"daemon "}; !slices.Equal(started, want) {
		t.Errorf("up started %q, want %q", started, want)
	}
}

// On a home with no yard up and down say so, start nothing and exit 2, as
// yardr status does: a window calls them, and gets no onboarding.
func TestUpAndDownOnNoYard(t *testing.T) {
	home := newHomeDir(t)
	stub(t, &connect, func(context.Context, *config.Config) (store.API, error) {
		t.Error("connected to the store, which starts a server")
		return nil, errNoServer
	})
	for _, cmd := range []string{"up", "down"} {
		out, err := run(t, testApp(t, nil, home).rootCmd(), cmd)
		if !errors.As(err, new(notInitialized)) || exitCode(err) != 2 || !strings.Contains(err.Error(), home) {
			t.Fatalf("yardr %s on no yard: %v\n%s", cmd, err, out)
		}
	}
}

// The login shell's PATH is read from the line it is asked to print it on,
// whatever its profile prints before, and is nothing without a shell.
func TestLoginShellPATH(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "sh")
	// The script runs the -c command itself instead of re-execing a
	// shell for it: one spawn, not two, so less to stretch under load.
	// A stand-in to the warmed launcher, not a new executable file: the
	// first exec of a new file alone can spend the timeout (scripttest).
	scripttest.Write(t, shell, "echo 'PATH is nice today'\nPATH=/a:/b\neval \"$3\"\n")
	stub(t, &loginShell, func() string { return shell })
	// A loaded machine can spend the login shell's five seconds starting
	// the script alone; the test is about the PATH it answers, not the
	// bound, so it gets thirty, as fake tools get for a version flag.
	stub(t, &loginShellTimeout, 30*time.Second)
	if got := loginShellPATH(context.Background()); got != "/a:/b" {
		t.Fatalf("PATH = %q", got)
	}
	stub(t, &loginShell, func() string { return "" })
	if got := loginShellPATH(context.Background()); got != "" {
		t.Fatalf("PATH with no shell = %q", got)
	}
}

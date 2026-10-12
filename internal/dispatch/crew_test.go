package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// crewRunner fakes herdr for crew members: sessions with a status, and the
// nudges they received.
type crewRunner struct {
	mu       sync.Mutex
	n        int
	status   map[string]string // agent -> idle|working|"" (gone)
	nudges   []string
	stops    int
	failNext error
	blocked  bool                // next start blocks before the agent is ready (a trust prompt)
	foreign  *dispatch.LiveAgent // an agent yardr did not start that already is the crew member
	findErr  error               // the agent list cannot be read
	sent     bool                // next start sends the first prompt and does not see it arrive
	shown    bool                // the pane shows the first prompt
	drop     int                 // deliveries of the first prompt the agent still drops
	blind    bool                // the pane cannot be read
	typed    bool                // the first prompt is in the agent's input, not submitted
	stick    int                 // deliveries of the first prompt the agent still leaves in its input
	deaf     bool                // Enter does not submit what is in the input
	submits  int                 // times Enter was sent
	input    string              // what is in the agent's input, not sent
	noInput  bool                // the harness's input cannot be told
	open     []string            // agents whose workspace is open, in the order they started
	busy     map[string]bool     // agents in whose workspace another agent runs now
	closeErr error               // a workspace cannot be closed
	tail     string              // what the pane ends in
	first    string              // the first prompt a start gives, kept on the handle when set
	atDialog bool                // the agent is at a dialog: Nudge types nothing and says so
	stalls   bool                // the agent starts on nothing it is typed: Nudge says so
	took     bool                // ... though it took the text all the same
	dialog   string              // the dialog its harness has drawn in place of its input, by its word
	line     string              // ... and the dialog's first line
	asks     string              // the harness answers the next text typed with this dialog, and keeps the text behind it
	login    string              // the line of the login screen it sits at, "" for none
	noScreen bool                // the harness has not drawn its screen (no input mark)
}

func (f *crewRunner) InputDialog(context.Context, map[string]any) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blind {
		return "", "", errors.New("pane cannot be read")
	}
	return f.dialog, f.line, nil
}

func (f *crewRunner) InputLogin(context.Context, map[string]any) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blind {
		return "", errors.New("pane cannot be read")
	}
	return f.login, nil
}

func (f *crewRunner) HarnessScreen(context.Context, map[string]any) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blind {
		return false, false
	}
	return !f.noScreen, true
}

func (f *crewRunner) FirstPrompt(h map[string]any) string {
	p, _ := h["first_prompt"].(string)
	return p
}

func (f *crewRunner) Tail(context.Context, map[string]any, int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tail, nil
}

func (f *crewRunner) show(tail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tail = tail
}

// CloseLeft closes the workspace of a session that is gone, as herdr does:
// not one with an agent in it.
func (f *crewRunner) CloseLeft(_ context.Context, _ store.CrewMember, h map[string]any) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	agent := h["agent"].(string)
	if f.closeErr != nil {
		return false, f.closeErr
	}
	if f.busy[agent] || f.status[agent] != "" {
		return true, nil
	}
	f.open = slices.DeleteFunc(f.open, func(a string) bool { return a == agent })
	return false, nil
}

func (f *crewRunner) workspaces() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.open, " ")
}

func (f *crewRunner) InputText(context.Context, map[string]any) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blind {
		return "", false, errors.New("pane cannot be read")
	}
	return f.input, !f.noInput, nil
}

func (f *crewRunner) PromptTyped(context.Context, map[string]any, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typed, nil
}

func (f *crewRunner) SubmitPrompt(context.Context, map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits++
	if !f.deaf {
		f.typed, f.shown, f.input = false, true, ""
	}
	return nil
}

func (f *crewRunner) PromptShown(context.Context, map[string]any, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blind {
		return false, errors.New("pane cannot be read")
	}
	return f.shown, nil
}

func (f *crewRunner) LiveCrew(context.Context, store.CrewMember) (*dispatch.LiveAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.foreign, f.findErr
}

func (f *crewRunner) StartCrew(_ context.Context, r store.CrewMember) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return nil, err
	}
	f.n++
	agent := fmt.Sprintf("%s-%d", r.Name, f.n)
	f.status[agent] = "idle"
	f.open = append(f.open, agent)
	if f.blocked {
		f.blocked = false
		f.status[agent] = "blocked"
		return map[string]any{"agent": agent, store.PendingPrompt: "prime yourself"}, nil
	}
	if f.sent {
		f.sent = false
		return map[string]any{"agent": agent, store.PendingPrompt: "prime yourself"}, nil
	}
	if f.first != "" {
		return map[string]any{"agent": agent, "pane": "p-1", "first_prompt": f.first}, nil
	}
	return map[string]any{"agent": agent}, nil
}
func (f *crewRunner) Start(context.Context, dispatch.Job) (map[string]any, error) {
	return nil, nil
}
func (f *crewRunner) Alive(context.Context, map[string]any) (bool, error) { return true, nil }
func (f *crewRunner) Stop(_ context.Context, h map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	delete(f.status, h["agent"].(string))
	f.open = slices.DeleteFunc(f.open, func(a string) bool { return a == h["agent"] })
	return nil
}
func (f *crewRunner) Status(_ context.Context, h map[string]any) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[h["agent"].(string)], nil
}
func (f *crewRunner) Nudge(_ context.Context, h map[string]any, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.atDialog {
		return dispatch.ErrAgentBlocked
	}
	f.nudges = append(f.nudges, h["agent"].(string)+": "+text)
	if f.asks != "" {
		// As Kimi after a long idle spell: the menu is where the input was,
		// its selected answer marked as an input is, and herdr sees the
		// agent start on nothing.
		f.dialog, f.input, f.asks = f.asks, "Compact and continue", ""
		return dispatch.ErrPromptStalled
	}
	if f.stalls {
		// As a herdr that waited for the agent to start on the text and saw
		// nothing: it sits in the input unless the agent took it unseen.
		switch {
		case f.took:
		case text == h[store.PendingPrompt]:
			f.typed = true
		default:
			f.input = text
		}
		return dispatch.ErrPromptStalled
	}
	if text == h[store.PendingPrompt] {
		// The first prompt: it shows in the pane unless the agent drops it.
		if f.drop > 0 {
			f.drop--
		} else if f.stick > 0 {
			f.stick--
			f.typed = true
		} else {
			f.shown = true
		}
	}
	return nil
}
func (f *crewRunner) set(agent, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[agent] = status
}

func crewSetup(t *testing.T, def store.CrewMember) (store.API, *crewRunner, *dispatch.Dispatcher) {
	s := storetest.New(t)
	f := &crewRunner{status: map[string]string{}}
	// No waits: a pending prompt is delivered on the first pass that finds
	// the agent idle and judged on the next, unless a test sets them.
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}, PromptSettle: -1, PromptWait: -1}
	def.Name, def.Runner = "yardmaster", "fake"
	if _, err := s.PutCrew(context.Background(), def, "me"); err != nil {
		t.Fatal(err)
	}
	return s, f, d
}

func agentOf(t *testing.T, s store.API) string {
	r, err := s.GetCrew(context.Background(), "yardmaster")
	if err != nil || !r.Live() {
		t.Fatalf("crew member not running: %+v %v", r, err)
	}
	return r.Handle["agent"].(string)
}

func TestCrewKeptAliveAndInboxNudgedWhenIdle(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{On: []string{"gave_up"}})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if f.n != 1 {
		t.Fatalf("started %d sessions, want 1", f.n)
	}
	agent := agentOf(t, s)

	// Busy: mail waits.
	f.set(agent, "working")
	must(t, s.Mail(ctx, "yardmaster", "deploy freeze", "chris"))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 0 {
		t.Fatalf("nudged a busy agent: %v", f.nudges)
	}
	// Idle: one nudge, not repeated for the same events.
	f.set(agent, "idle")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 {
		t.Fatalf("nudges = %v", f.nudges)
	}
	// Something new: nudged again.
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, s.LogDispatch(ctx, b.ID, "gave_up", "yard", nil))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 2 {
		t.Fatalf("nudges = %v", f.nudges)
	}

	// The session dies, long enough after its start to be no failed start:
	// yard starts another.
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(2 * store.MinSessionLife) })
	f.set(agent, "")
	must(t, d.Reconcile(ctx))
	if f.n != 2 || agentOf(t, s) == agent {
		t.Fatalf("not restarted after death: starts %d", f.n)
	}
}

func TestHandoffRestartsWhenIdle(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	agent := agentOf(t, s)
	f.set(agent, "working") // still writing its handoff turn
	must(t, s.Journal(ctx, "yardmaster", store.NoteHandoff, "Next: review webapp-3", agent))
	must(t, d.Reconcile(ctx))
	if f.n != 1 {
		t.Fatal("restarted mid-turn")
	}
	f.set(agent, "idle")
	must(t, d.Reconcile(ctx))
	if f.n != 2 || f.stops != 1 {
		t.Fatalf("starts %d stops %d", f.n, f.stops)
	}
	p, err := s.CrewPrime(ctx, "yardmaster")
	if err != nil || p.Handoff == nil || p.Handoff.Text != "Next: review webapp-3" || p.NoHandoff {
		t.Fatalf("prime after handoff: %+v %v", p, err)
	}
}

func TestRotationAsksThenForces(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{MaxAge: "1ms", HandoffGrace: "1ms"})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	time.Sleep(5 * time.Millisecond)
	must(t, d.Reconcile(ctx)) // due: asked to hand off
	if len(f.nudges) != 1 || f.n != 1 {
		t.Fatalf("nudges %v starts %d", f.nudges, f.n)
	}
	time.Sleep(5 * time.Millisecond)
	must(t, d.Reconcile(ctx)) // no handoff within grace: restart requested
	must(t, d.Reconcile(ctx)) // ...and carried out
	if f.n != 2 {
		t.Fatalf("starts %d, want a forced rotation", f.n)
	}
	p, _ := s.CrewPrime(ctx, "yardmaster")
	if !p.NoHandoff {
		t.Fatal("the forced rotation left no handoff; the prime must fall back")
	}
}

func TestCrewStartFailureBacksOffAndDisableStops(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.failNext = errors.New("herdr down")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx)) // within backoff: no retry
	r, _ := s.GetCrew(ctx, "yardmaster")
	if f.n != 0 || r.Failures != 1 || r.RetryAfter == nil {
		t.Fatalf("starts %d crew member %+v", f.n, r)
	}
	must(t, s.SetCrewEnabled(ctx, "yardmaster", true, "", "me")) // clears the backoff
	must(t, d.Reconcile(ctx))
	agentOf(t, s)
	must(t, s.SetCrewEnabled(ctx, "yardmaster", false, "", "me"))
	must(t, d.Reconcile(ctx))
	if r, _ = s.GetCrew(ctx, "yardmaster"); r.Live() || f.stops != 1 {
		t.Fatalf("disable did not stop it: %+v stops %d", r, f.stops)
	}
}

// Found live: a crew member whose agent stops at Claude's trust prompt was
// treated as a failed start, its tab closed and retried in a loop, so the
// human could never answer. A blocked start is kept and its first prompt
// delivered once the agent is idle.
func TestBlockedStartWaitsForHumanThenPrompts(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.blocked = true
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r, _ := s.GetCrew(ctx, "yardmaster")
	if !r.Live() || f.stops != 0 || f.n != 1 || r.Failures != 0 {
		t.Fatalf("blocked start was not kept: %+v stops %d starts %d", r, f.stops, f.n)
	}
	if len(f.nudges) != 0 {
		t.Fatalf("prompted a blocked agent: %v", f.nudges)
	}
	evs, _ := s.Events(ctx, r.Journal, 100)
	blocked := 0
	for _, e := range evs {
		if e.Kind == "crew_blocked" {
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatalf("crew_blocked logged %d times", blocked)
	}
	// The human answers in the pane; the agent becomes idle.
	f.set(agentOf(t, s), "idle")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 || !strings.HasSuffix(f.nudges[0], "prime yourself") {
		t.Fatalf("pending prompt delivered %v", f.nudges)
	}
	if r, _ = s.GetCrew(ctx, "yardmaster"); r.Handle[store.PendingPrompt] != nil {
		t.Fatal("pending prompt not cleared")
	}
}

// Found live (yardr-ykbt): a first prompt that was sent and did not show in
// the pane is left pending. An agent that took it after the runner stopped
// looking is working on it: it does not get it a second time, and it is not
// reported as waiting for a human.
func TestPendingPromptThatShowsIsNotSentAgain(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.sent = true
	must(t, d.Reconcile(ctx))
	f.set(agentOf(t, s), "working")
	f.shown = true
	must(t, d.Reconcile(ctx))
	f.set(agentOf(t, s), "idle")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r, _ := s.GetCrew(ctx, "yardmaster")
	if len(f.nudges) != 0 || r.Handle[store.PendingPrompt] != nil {
		t.Fatalf("nudges %v, handle %v", f.nudges, r.Handle)
	}
	evs, _ := s.Events(ctx, r.Journal, 100)
	for _, e := range evs {
		if e.Kind == "crew_blocked" {
			t.Fatalf("a crew member working on its first prompt was reported blocked: %+v", e)
		}
	}
}

// One that did not show is delivered once the agent is idle, and once when
// it arrives.
func TestPendingPromptThatDidNotShowIsSentOnce(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.sent = true
	for range 4 {
		must(t, d.Reconcile(ctx))
	}
	r, _ := s.GetCrew(ctx, "yardmaster")
	if len(f.nudges) != 1 || !strings.HasSuffix(f.nudges[0], "prime yourself") || r.Handle[store.PendingPrompt] != nil {
		t.Fatalf("nudges %v, handle %v", f.nudges, r.Handle)
	}
}

// nudgeRunner is a bead runner whose agents can be blocked and prompted.
type nudgeRunner struct {
	*fakeRunner
	status string
	nudges []string
	shown  bool // the pane shows the first prompt
	drop   int  // deliveries of the first prompt the agent still drops

	typed   bool // the first prompt is in the agent's input, not submitted
	stick   int  // deliveries of the first prompt the agent still leaves in its input
	deaf    bool // Enter does not submit what is in the input
	submits int  // times Enter was sent

	input  string // what is in the agent's input, not sent
	dialog string // the dialog its harness has drawn in place of its input, by its word

	unconfirmed bool // Start sent the prompt itself and took it as arrived without seeing it taken
	blind       bool // the pane cannot be read
}

func (n *nudgeRunner) InputText(context.Context, map[string]any) (string, bool, error) {
	return n.input, true, nil
}

func (n *nudgeRunner) InputDialog(context.Context, map[string]any) (string, string, error) {
	return n.dialog, cacheMenuLine, nil
}

func (n *nudgeRunner) PromptTyped(context.Context, map[string]any, string) (bool, error) {
	return n.typed, nil
}

func (n *nudgeRunner) SubmitPrompt(context.Context, map[string]any) error {
	n.submits++
	if !n.deaf {
		n.typed, n.shown = false, true
	}
	return nil
}

func (n *nudgeRunner) PromptShown(context.Context, map[string]any, string) (bool, error) {
	if n.blind {
		return false, errors.New("pane read failed")
	}
	return n.shown, nil
}

func (n *nudgeRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	h, err := n.fakeRunner.Start(ctx, j)
	if h != nil {
		h[store.PendingPrompt] = "read your brief"
		if n.unconfirmed {
			h[dispatch.PromptUnconfirmed] = true
		}
	}
	return h, err
}
func (n *nudgeRunner) Status(context.Context, map[string]any) (string, error) { return n.status, nil }
func (n *nudgeRunner) Nudge(_ context.Context, _ map[string]any, text string) error {
	n.nudges = append(n.nudges, text)
	if n.drop > 0 {
		n.drop--
	} else if n.stick > 0 {
		n.stick--
		n.typed = true
	} else {
		n.shown = true
	}
	return nil
}

func TestBlockedBeadSessionGetsItsPromptWhenIdle(t *testing.T) {
	s := storetest.New(t)
	r := &nudgeRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "blocked"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, PromptSettle: -1, PromptWait: -1}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 0 {
		t.Fatal("prompted while blocked")
	}
	r.status = "idle"
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 1 || r.nudges[0] != "read your brief" {
		t.Fatalf("nudges = %v", r.nudges)
	}
	got, _ := s.GetBead(ctx, b.ID)
	sess, _ := s.GetSession(ctx, *got.Session)
	if sess.Handle[store.PendingPrompt] != nil || sess.State != "running" {
		t.Fatalf("session after prompt: %+v", sess)
	}
}

// The same for a bead session: a pending first prompt that shows in the pane
// was taken, so it is cleared without a second delivery, working or idle.
func TestBeadSessionPendingPromptThatShowsIsNotSentAgain(t *testing.T) {
	s := storetest.New(t)
	r := &nudgeRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working", shown: true}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r.status = "idle"
	must(t, d.Reconcile(ctx))
	got, _ := s.GetBead(ctx, b.ID)
	sess, _ := s.GetSession(ctx, *got.Session)
	if len(r.nudges) != 0 || sess.Handle[store.PendingPrompt] != nil {
		t.Fatalf("nudges %v, handle %v", r.nudges, sess.Handle)
	}
	evs, _ := s.Events(ctx, b.ID, 100)
	prompted := 0
	for _, e := range evs {
		if e.Kind == "session_prompted" {
			prompted++
			if e.Data["shown"] != true {
				t.Errorf("session_prompted does not say the prompt was found in the pane: %+v", e)
			}
		}
	}
	if prompted != 1 {
		t.Fatalf("session_prompted logged %d times", prompted)
	}
}

// nudgeSetup is a bead whose session starts with its first prompt pending,
// its agent at a question, as a Codex at its folder-trust dialog is.
func nudgeSetup(t *testing.T) (store.API, *nudgeRunner, *dispatch.Dispatcher, *store.Bead) {
	s := storetest.New(t)
	r := &nudgeRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "blocked"}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, PromptSettle: -1, PromptWait: -1}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, d.Reconcile(ctx))
	return s, r, d, b
}

func handleOf(t *testing.T, s store.API, bead string) map[string]any {
	t.Helper()
	got, err := s.GetBead(context.Background(), bead)
	must(t, err)
	sess, err := s.GetSession(context.Background(), *got.Session)
	must(t, err)
	return sess.Handle
}

// Found live (yardr-85jy): a Codex was given its pending prompt in the
// second its trust dialog was answered and dropped the keys. The prompt was
// cleared as delivered, session_prompted said so, and the session sat at an
// empty input until the yardmaster typed the prompt by hand. A delivery that
// does not show is sent again, and session_prompted is the arrival.
func TestPendingPromptDroppedOnceIsDeliveredAgain(t *testing.T) {
	s, r, d, b := nudgeSetup(t)
	ctx := context.Background()
	r.status, r.drop = "idle", 1
	must(t, d.Reconcile(ctx)) // delivered, and dropped
	if len(r.nudges) != 1 || handleOf(t, s, b.ID)[store.PendingPrompt] != "read your brief" {
		t.Fatalf("a prompt that did not show is not pending: nudges %v, handle %v", r.nudges, handleOf(t, s, b.ID))
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompted")); n != 0 {
		t.Fatalf("session_prompted logged for a prompt that did not arrive: %d", n)
	}
	for range 4 {
		must(t, d.Reconcile(ctx))
	}
	h := handleOf(t, s, b.ID)
	if len(r.nudges) != 2 || len(h) != 1 {
		t.Fatalf("nudges %v, handle %v; want the prompt sent twice and nothing of it left", r.nudges, h)
	}
	lost, prompted := eventKinds(t, s, b.ID, "session_prompt_lost"), eventKinds(t, s, b.ID, "session_prompted")
	if len(lost) != 1 || lost[0].Data["delivery"] != float64(1) || lost[0].Data["session"] == nil {
		t.Fatalf("session_prompt_lost = %+v", lost)
	}
	if len(prompted) != 1 || prompted[0].Data["deliveries"] != float64(2) || prompted[0].Data["shown"] != true {
		t.Fatalf("session_prompted = %+v", prompted)
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompt_gave_up")); n != 0 {
		t.Fatalf("gave up on a prompt that arrived: %d", n)
	}
}

// One that is dropped every time is sent PromptTries times and no more:
// the session is reported, with the command that gives the prompt by hand,
// and left alone. The prompt a human then gives it ends the wait.
func TestPendingPromptDroppedEveryTimeIsGivenUp(t *testing.T) {
	s, r, d, b := nudgeSetup(t)
	ctx := context.Background()
	r.status, r.drop = "idle", 1000
	for range 10 {
		must(t, d.Reconcile(ctx))
	}
	h := handleOf(t, s, b.ID)
	if len(r.nudges) != dispatch.PromptTries || h[store.PendingPrompt] != "read your brief" {
		t.Fatalf("nudges %v, handle %v", r.nudges, h)
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompt_lost")); n != dispatch.PromptTries {
		t.Fatalf("session_prompt_lost logged %d times, want one per delivery", n)
	}
	gaveUp := eventKinds(t, s, b.ID, "session_prompt_gave_up")
	if len(gaveUp) != 1 || gaveUp[0].Data["deliveries"] != float64(dispatch.PromptTries) ||
		!strings.HasPrefix(fmt.Sprint(gaveUp[0].Data["hint"]), "herdr agent prompt ") ||
		!strings.HasSuffix(fmt.Sprint(gaveUp[0].Data["hint"]), " 'read your brief'") {
		t.Fatalf("session_prompt_gave_up = %+v", gaveUp)
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompted")); n != 0 {
		t.Fatalf("session_prompted logged for a prompt that never arrived: %d", n)
	}
	// By hand: it shows, and the session is an ordinary one again.
	r.shown = true
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if h := handleOf(t, s, b.ID); len(h) != 1 || len(r.nudges) != dispatch.PromptTries {
		t.Fatalf("after the prompt was given by hand: nudges %v, handle %v", r.nudges, h)
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompted")); n != 1 {
		t.Fatalf("session_prompted logged %d times", n)
	}
}

// An agent that is working after a delivery took the prompt, whatever its
// pane shows by the time the dispatcher looks.
func TestPendingPromptOfAWorkingAgentArrived(t *testing.T) {
	s, r, d, b := nudgeSetup(t)
	ctx := context.Background()
	r.status, r.drop = "idle", 1000
	must(t, d.Reconcile(ctx))
	r.status = "working"
	must(t, d.Reconcile(ctx))
	r.status = "idle"
	must(t, d.Reconcile(ctx))
	if h := handleOf(t, s, b.ID); len(r.nudges) != 1 || len(h) != 1 {
		t.Fatalf("nudges %v, handle %v", r.nudges, h)
	}
	prompted := eventKinds(t, s, b.ID, "session_prompted")
	if len(prompted) != 1 || prompted[0].Data["shown"] != nil || len(eventKinds(t, s, b.ID, "session_prompt_lost")) != 0 {
		t.Fatalf("session_prompted = %+v", prompted)
	}
}

// The first delivery waits until the agent has been idle for PromptSettle
// (it has just left a dialog), and a delivery is not judged lost before
// PromptWait: an agent that is slow to echo is not sent its prompt twice.
func TestPendingPromptWaitsForTheAgentToSettleAndToEcho(t *testing.T) {
	s, r, d, b := nudgeSetup(t)
	ctx := context.Background()
	d.PromptSettle, d.PromptWait = 3*time.Second, 10*time.Second
	var skew time.Duration
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(skew) })
	r.status, r.drop = "idle", 1
	must(t, d.Reconcile(ctx))
	skew += 2 * time.Second
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 0 {
		t.Fatalf("prompted an agent that had just become idle: %v", r.nudges)
	}
	// At a next question in between: the wait starts again.
	r.status = "blocked"
	must(t, d.Reconcile(ctx))
	r.status = "idle"
	skew += 2 * time.Second
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 0 {
		t.Fatalf("prompted an agent that had just left its second question: %v", r.nudges)
	}
	skew += 4 * time.Second
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 1 {
		t.Fatalf("nudges after the agent settled = %v", r.nudges)
	}
	skew += 9 * time.Second
	must(t, d.Reconcile(ctx))
	if len(r.nudges) != 1 || len(eventKinds(t, s, b.ID, "session_prompt_lost")) != 0 {
		t.Fatalf("judged lost before PromptWait: nudges %v", r.nudges)
	}
	skew += 2 * time.Second
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if h := handleOf(t, s, b.ID); len(r.nudges) != 2 || len(h) != 1 {
		t.Fatalf("nudges %v, handle %v", r.nudges, h)
	}
}

// Found live (yardr-cxsb): a crew member's prime was typed into Claude Code's
// input and its Enter not taken; the text being in the pane, the prompt
// counted as arrived and the crew member sat idle until a person pressed
// Return. A prompt that is only in the input is submitted, once, and not
// typed a second time; session_prompted is still the arrival.
func TestPendingPromptTypedAndNotSubmittedIsSubmitted(t *testing.T) {
	for name, stick := range map[string]int{"the runner's own send": 0, "the dispatcher's delivery": 1} {
		s, r, d, b := nudgeSetup(t)
		ctx := context.Background()
		r.status, r.stick, r.typed = "idle", stick, stick == 0
		for range 5 {
			must(t, d.Reconcile(ctx))
		}
		if h := handleOf(t, s, b.ID); len(r.nudges) != stick || r.submits != 1 || len(h) != 1 {
			t.Fatalf("%s: typed %d times, submitted %d times, handle %v; want the text typed once in all and one Enter",
				name, len(r.nudges), r.submits, h)
		}
		typed, prompted := eventKinds(t, s, b.ID, "session_prompt_typed"), eventKinds(t, s, b.ID, "session_prompted")
		if len(typed) != 1 || typed[0].Data["submit"] != float64(1) || typed[0].Data["session"] == nil {
			t.Fatalf("%s: session_prompt_typed = %+v", name, typed)
		}
		if len(prompted) != 1 || prompted[0].Data["submits"] != float64(1) || prompted[0].Data["shown"] != true {
			t.Fatalf("%s: session_prompted = %+v", name, prompted)
		}
		for _, kind := range []string{"session_prompt_lost", "session_prompt_gave_up"} {
			if n := len(eventKinds(t, s, b.ID, kind)); n != 0 {
				t.Fatalf("%s: %s logged %d times for a prompt that only needed Enter", name, kind, n)
			}
		}
	}
}

// One whose Enter never takes is submitted PromptTries times, PromptWait
// apart, and no more: the session is reported, Enter in its pane being all
// it needs, and left alone. The Enter a human then gives it ends the wait.
func TestPendingPromptWhoseSubmitNeverTakesIsGivenUp(t *testing.T) {
	s, r, d, b := nudgeSetup(t)
	ctx := context.Background()
	d.PromptSettle, d.PromptWait = 3*time.Second, 10*time.Second
	var skew time.Duration
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(skew) })
	r.status, r.typed, r.deaf = "idle", true, true
	must(t, d.Reconcile(ctx))
	skew += 2 * time.Second
	must(t, d.Reconcile(ctx))
	if r.submits != 0 {
		t.Fatalf("submitted text whose own Enter may still be on its way: %d", r.submits)
	}
	skew += 2 * time.Second
	must(t, d.Reconcile(ctx))
	skew += 9 * time.Second
	must(t, d.Reconcile(ctx))
	if r.submits != 1 {
		t.Fatalf("submits = %d, want one once the agent settled and none before PromptWait", r.submits)
	}
	for range 10 {
		skew += 11 * time.Second
		must(t, d.Reconcile(ctx))
	}
	h := handleOf(t, s, b.ID)
	if r.submits != dispatch.PromptTries || len(r.nudges) != 0 || h[store.PendingPrompt] != "read your brief" {
		t.Fatalf("submits %d, nudges %v, handle %v", r.submits, r.nudges, h)
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompt_typed")); n != dispatch.PromptTries {
		t.Fatalf("session_prompt_typed logged %d times, want one per submit", n)
	}
	gaveUp := eventKinds(t, s, b.ID, "session_prompt_gave_up")
	if len(gaveUp) != 1 || gaveUp[0].Data["typed"] != true || gaveUp[0].Data["submits"] != float64(dispatch.PromptTries) ||
		!strings.HasPrefix(fmt.Sprint(gaveUp[0].Data["hint"]), "herdr pane send-keys ") ||
		!strings.HasSuffix(fmt.Sprint(gaveUp[0].Data["hint"]), " enter") ||
		!strings.Contains(fmt.Sprint(gaveUp[0].Data["reason"]), "Enter in its pane is all it needs") {
		t.Fatalf("session_prompt_gave_up = %+v", gaveUp)
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompted")); n != 0 {
		t.Fatalf("session_prompted logged for a prompt that was never taken: %d", n)
	}
	// By hand: the agent works on it, whatever its pane shows.
	r.status = "working"
	must(t, d.Reconcile(ctx))
	if h := handleOf(t, s, b.ID); len(h) != 1 || r.submits != dispatch.PromptTries {
		t.Fatalf("after Enter by hand: submits %d, handle %v", r.submits, h)
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompted")); n != 1 {
		t.Fatalf("session_prompted logged %d times", n)
	}
}

// Text in the input of an agent that is working was submitted: nothing more
// is sent to it.
func TestPendingPromptTypedByAWorkingAgentArrived(t *testing.T) {
	s, r, d, b := nudgeSetup(t)
	ctx := context.Background()
	r.status, r.typed = "working", true
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if h := handleOf(t, s, b.ID); r.submits != 0 || len(r.nudges) != 0 || len(h) != 1 {
		t.Fatalf("submits %d, nudges %v, handle %v", r.submits, r.nudges, h)
	}
	if n := len(eventKinds(t, s, b.ID, "session_prompted")); n != 1 {
		t.Fatalf("session_prompted logged %d times", n)
	}
}

// Found live (yardr-myyv): two builders sat idle with their first prompt
// in their input, and nothing was pending on their handles: the runner had
// taken the prompt as arrived at one look, on a sign other than its echo,
// and with the prompt off the handle nobody looked again. Such a prompt is
// left pending as unconfirmed, and the look is taken again on a pass, by
// whichever daemon makes it: here one made anew after the start. Text in
// the input of an idle agent gets its Enter; an agent that is still
// working, or a pane that still cannot be read, is what the runner went
// by, and the prompt is then not sent a second time.
func TestUnconfirmedPromptIsLookedAtAgain(t *testing.T) {
	for _, c := range []struct {
		name, status             string
		typed, blind             bool
		submits, typedEv, nudges int
	}{
		{"in the input of an idle agent", "idle", true, false, 1, 1, 0},
		{"the agent is working", "working", false, false, 0, 0, 0},
		{"the pane cannot be read", "idle", false, true, 0, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := storetest.New(t)
			r := &nudgeRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: c.status,
				typed: c.typed, blind: c.blind, unconfirmed: true}
			ctx := context.Background()
			must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
			_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
			b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
			starter := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, PromptSettle: -1, PromptWait: -1}
			must(t, starter.Reconcile(ctx))
			if h := handleOf(t, s, b.ID); h[store.PendingPrompt] == nil || h[dispatch.PromptUnconfirmed] != true {
				t.Fatalf("handle after the start: %v", h)
			}
			// The daemon replaced itself: what it knows is on the handle.
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}, PromptSettle: -1, PromptWait: -1}
			for range 4 {
				must(t, d.Reconcile(ctx))
			}
			if h := handleOf(t, s, b.ID); r.submits != c.submits || len(r.nudges) != c.nudges || len(h) != 1 {
				t.Fatalf("Enter sent %d times, prompt typed again %d times, handle %v; want %d, %d and nothing pending",
					r.submits, len(r.nudges), h, c.submits, c.nudges)
			}
			if n := len(eventKinds(t, s, b.ID, "session_prompt_typed")); n != c.typedEv {
				t.Fatalf("session_prompt_typed logged %d times, want %d", n, c.typedEv)
			}
			if n := len(eventKinds(t, s, b.ID, "session_prompted")); n != 1 {
				t.Fatalf("session_prompted logged %d times", n)
			}
		})
	}
}

// Crew members go the same way, with the events on their journal.
func TestCrewPromptTypedAndNotSubmittedIsSubmitted(t *testing.T) {
	for _, c := range []struct {
		deaf                             bool
		submits, typed, gaveUp, prompted int
	}{
		{false, 1, 1, 0, 1},
		{true, dispatch.PromptTries, dispatch.PromptTries, 1, 0},
	} {
		s, f, d := crewSetup(t, store.CrewMember{})
		ctx := context.Background()
		f.sent, f.typed, f.deaf = true, true, c.deaf
		for range 10 {
			must(t, d.Reconcile(ctx))
		}
		r, _ := s.GetCrew(ctx, "yardmaster")
		if f.submits != c.submits || len(f.nudges) != 0 || (r.Handle[store.PendingPrompt] == nil) != (c.prompted == 1) {
			t.Fatalf("deaf %v: submits %d, nudges %v, handle %v", c.deaf, f.submits, f.nudges, r.Handle)
		}
		for kind, want := range map[string]int{"session_prompt_typed": c.typed, "session_prompt_gave_up": c.gaveUp, "session_prompted": c.prompted} {
			evs := eventKinds(t, s, r.Journal, kind)
			if len(evs) != want {
				t.Fatalf("deaf %v: %s logged %d times, want %d", c.deaf, kind, len(evs), want)
			}
			if want > 0 && evs[0].Data["crew"] != "yardmaster" {
				t.Fatalf("deaf %v: %s does not name the crew member: %+v", c.deaf, kind, evs[0])
			}
		}
	}
}

// Crew members go the same way: a prime that is dropped is sent again, and one
// dropped every time is reported on the crew member's journal.
func TestCrewPendingPromptIsCheckedForArrival(t *testing.T) {
	for _, c := range []struct{ drop, nudges, lost, gaveUp, prompted int }{
		{0, 1, 0, 0, 1},
		{1, 2, 1, 0, 1},
		{1000, dispatch.PromptTries, dispatch.PromptTries, 1, 0},
	} {
		s, f, d := crewSetup(t, store.CrewMember{})
		ctx := context.Background()
		f.blocked, f.drop = true, c.drop
		must(t, d.Reconcile(ctx))
		f.set(agentOf(t, s), "idle")
		for range 10 {
			must(t, d.Reconcile(ctx))
		}
		r, _ := s.GetCrew(ctx, "yardmaster")
		if len(f.nudges) != c.nudges || (r.Handle[store.PendingPrompt] == nil) != (c.prompted == 1) {
			t.Fatalf("drop %d: nudges %v, handle %v", c.drop, f.nudges, r.Handle)
		}
		for kind, want := range map[string]int{"session_prompt_lost": c.lost, "session_prompt_gave_up": c.gaveUp, "session_prompted": c.prompted} {
			evs := eventKinds(t, s, r.Journal, kind)
			if len(evs) != want {
				t.Fatalf("drop %d: %s logged %d times, want %d", c.drop, kind, len(evs), want)
			}
			if want > 0 && evs[0].Data["crew"] != "yardmaster" {
				t.Fatalf("drop %d: %s does not name the crew member: %+v", c.drop, kind, evs[0])
			}
		}
	}
}

// A pane that cannot be read tells nothing about the prompt: what was sent
// is taken as arrived, as before yardr looked, and not sent a second time.
func TestPendingPromptWithAnUnreadablePaneIsSentOnce(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.sent, f.blind, f.drop = true, true, 1000
	for range 4 {
		must(t, d.Reconcile(ctx))
	}
	r, _ := s.GetCrew(ctx, "yardmaster")
	if len(f.nudges) != 1 || r.Handle[store.PendingPrompt] != nil {
		t.Fatalf("nudges %v, handle %v", f.nudges, r.Handle)
	}
}

// A crew member whose env_file is refused (missing, open to others) does not
// start, and crew_blocked tells the yardmaster why, once while the reason
// stays the same.
func TestCrewBlockedByItsConfig(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	refuse := dispatch.Blocked(errors.New("crew member yardmaster: env_file /x/yardmaster.env is open to others (mode 0644); chmod 600 it"))
	for range 2 {
		f.failNext = refuse
		must(t, s.SetCrewEnabled(ctx, "yardmaster", true, "", "me")) // clears the backoff
		must(t, d.Reconcile(ctx))
	}
	r, _ := s.GetCrew(ctx, "yardmaster")
	if r.Live() || f.n != 0 {
		t.Fatalf("blocked crew member started: %+v", r)
	}
	evs, _ := s.Events(ctx, r.Journal, 100)
	var reasons []string
	for _, e := range evs {
		if e.Kind == "crew_blocked" {
			reasons = append(reasons, fmt.Sprint(e.Data["reason"]))
		}
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "env_file") {
		t.Fatalf("crew_blocked = %q, want one naming the env_file", reasons)
	}
	// An ordinary failure is not a block.
	f.failNext = errors.New("herdr down")
	must(t, s.SetCrewEnabled(ctx, "yardmaster", true, "", "me"))
	must(t, d.Reconcile(ctx))
	if evs, _ = s.Events(ctx, r.Journal, 100); slices.ContainsFunc(evs[len(evs)-1:], func(e store.Event) bool {
		return e.Kind == "crew_blocked"
	}) {
		t.Fatal("herdr down reported as crew_blocked")
	}
}

// Found live (2026-10-03): a pane started by hand was the yardmaster when
// the crew member was defined, and the daemon started a second one beside it.
// A crew member whose runner finds a live agent that already is it is not
// started: crew_blocked names the agent and its pane, once while it is
// the same agent, nothing counts as a failed start, and the crew member starts
// on the pass after the agent is gone.
func TestCrewNotStartedBesideALiveAgent(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	blocked := func() (reasons []string) {
		evs, _ := s.Events(ctx, "yardmaster-journal", 100)
		for _, e := range evs {
			if e.Kind == "crew_blocked" {
				reasons = append(reasons, fmt.Sprint(e.Data["reason"]))
			}
		}
		return reasons
	}
	f.foreign = &dispatch.LiveAgent{Name: "yardmaster", Pane: "w1:p1"}
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	r, _ := s.GetCrew(ctx, "yardmaster")
	if r.Live() || f.n != 0 || r.Failures != 0 || r.RetryAfter != nil {
		t.Fatalf("started, or counted as a failure: %+v starts %d", r, f.n)
	}
	if got := blocked(); len(got) != 1 || !strings.Contains(got[0], `"yardmaster"`) || !strings.Contains(got[0], "w1:p1") {
		t.Fatalf("crew_blocked = %q, want one naming agent and pane", got)
	}
	// Another agent in its place is news.
	f.foreign = &dispatch.LiveAgent{Name: "yardr-yardmaster", Pane: "w2:p1"}
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if got := blocked(); len(got) != 2 || !strings.Contains(got[1], "yardr-yardmaster") || !strings.Contains(got[1], "w2:p1") {
		t.Fatalf("crew_blocked = %q, want a second for the other agent", got)
	}
	// The human ends it: the next pass starts the crew member.
	f.foreign = nil
	must(t, d.Reconcile(ctx))
	if r, _ = s.GetCrew(ctx, "yardmaster"); !r.Live() || f.n != 1 {
		t.Fatalf("not started once the agent was gone: %+v starts %d", r, f.n)
	}
	// It dies, long enough after its start to be no failed start, while
	// another agent is it again: said again, not restarted.
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(2 * store.MinSessionLife) })
	f.set(agentOf(t, s), "")
	f.foreign = &dispatch.LiveAgent{Name: "yardmaster", Pane: "w1:p1"}
	must(t, d.Reconcile(ctx))
	if r, _ = s.GetCrew(ctx, "yardmaster"); r.Live() || f.n != 1 || len(blocked()) != 3 {
		t.Fatalf("restarted beside a live agent: %+v starts %d blocked %q", r, f.n, blocked())
	}
}

// An agent list that cannot be read is a failed start like any other (herdr
// down): counted and backed off, not reported as blocked, and nothing is
// started blind.
func TestCrewAgentListUnreadableIsAFailedStart(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	f.findErr = errors.New("herdr: connection refused")
	must(t, d.Reconcile(ctx))
	r, _ := s.GetCrew(ctx, "yardmaster")
	if r.Live() || f.n != 0 || r.Failures != 1 || r.RetryAfter == nil {
		t.Fatalf("crew member after an unreadable agent list: %+v starts %d", r, f.n)
	}
	evs, _ := s.Events(ctx, r.Journal, 100)
	if slices.ContainsFunc(evs, func(e store.Event) bool { return e.Kind == "crew_blocked" }) {
		t.Fatal("herdr down reported as crew_blocked")
	}
}

// shortLived is crewSetup with a clock the test moves: at(by) puts the
// dispatcher by after the real time, which is what the store stamps a start
// with, so at(time.Second) right after a start makes that session a second
// old.
func shortLived(t *testing.T) (s store.API, f *crewRunner, d *dispatch.Dispatcher, at func(time.Duration)) {
	s, f, d = crewSetup(t, store.CrewMember{On: []string{"crew_gave_up"}})
	var skew time.Duration
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(skew) })
	return s, f, d, func(by time.Duration) { skew = by }
}

// Found live (yardr-05dd): a harness that exits right after its start was
// started again on every pass, a new workspace every 5 seconds, with nothing
// counted and nobody told. A session gone within store.MinSessionLife is a
// failed start: the waits grow and after store.MaxStartFails the crew member is
// given up.
func TestCrewSessionGoneAtOnceIsAFailedStart(t *testing.T) {
	s, f, d, at := shortLived(t)
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	var waits []time.Duration
	for n := 1; n <= store.MaxStartFails; n++ {
		if f.n != n {
			t.Fatalf("round %d: %d sessions started", n, f.n)
		}
		f.set(agentOf(t, s), "") // the harness exits
		at(time.Second)
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx)) // within the wait: no start
		r, _ := s.GetCrew(ctx, "yardmaster")
		if r.Live() || f.n != n || r.Failures != n || r.RetryAfter == nil || r.Handle != nil {
			t.Fatalf("round %d: starts %d crew member %+v", n, f.n, r)
		}
		wait := time.Until(*r.RetryAfter)
		waits = append(waits, wait)
		if n > 1 && wait < waits[n-2]+waits[n-2]/2 {
			t.Fatalf("waits do not grow: %v", waits)
		}
		if n == 3 && (r.State != "stopped" || wait < 100*time.Second) {
			t.Fatalf("after three sessions: state %s, waits %v, want stopped and about 30s, 1m, 2m", r.State, waits)
		}
		if n == store.MaxStartFails {
			if r.State != "failed" {
				t.Fatalf("state %s after %d sessions gone at once, want failed", r.State, n)
			}
			break
		}
		at(wait + time.Second)
		must(t, d.Reconcile(ctx))
	}
	// Given up: never started again, however long ago.
	at(24 * time.Hour)
	must(t, d.Reconcile(ctx))
	if f.n != store.MaxStartFails {
		t.Fatalf("started again after giving up: %d sessions", f.n)
	}
	evs, _ := s.Events(ctx, "yardmaster-journal", 100)
	kinds := map[string]int{}
	reason := ""
	for _, e := range evs {
		kinds[e.Kind]++
		if e.Kind == "crew_gave_up" {
			reason = fmt.Sprint(e.Data["reason"])
		}
	}
	if kinds["crew_start_failed"] != store.MaxStartFails-1 || kinds["crew_gave_up"] != 1 ||
		kinds["crew_stopped"] != store.MaxStartFails {
		t.Fatalf("events %v", kinds)
	}
	if !strings.Contains(reason, "the session ended 1s after its start") {
		t.Fatalf("crew_gave_up reason %q does not say how long the session lived", reason)
	}
}

// Found live (yardr-ybxk): a session that was gone left its workspace open,
// one for each, five by the time the daemon gave the crew member up. The
// daemon keeps the handle of every such session and, when the next one has
// started, closes them all but the latest, whose pane is the only place
// that shows why the harness exited.
func TestCrewSessionsGoneLeaveOneWorkspace(t *testing.T) {
	s, f, d, at := shortLived(t)
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	for n := 1; n <= store.MaxStartFails; n++ {
		f.set(agentOf(t, s), "") // the harness exits; the pane's shell stays
		at(time.Second)
		must(t, d.Reconcile(ctx))
		r, _ := s.GetCrew(ctx, "yardmaster")
		// Gone, and nothing started since: the one before it is still open
		// too, and no more than that, however many went before.
		want := fmt.Sprintf("yardmaster-%d", n)
		if n > 1 {
			want = fmt.Sprintf("yardmaster-%d yardmaster-%d", n-1, n)
		}
		if got := f.workspaces(); got != want || len(r.Left) != len(strings.Fields(want)) {
			t.Fatalf("after %d sessions gone: workspaces %q, left %v; want %q", n, got, r.Left, want)
		}
		if n < store.MaxStartFails {
			at(time.Until(*r.RetryAfter) + time.Second)
			must(t, d.Reconcile(ctx))
		}
	}
	if r, _ := s.GetCrew(ctx, "yardmaster"); r.State != "failed" || f.n != store.MaxStartFails {
		t.Fatalf("state %s after %d sessions", r.State, f.n)
	}
	// Five sessions gone; the next start leaves the last one's workspace
	// beside its own.
	must(t, s.SetCrewEnabled(ctx, "yardmaster", true, "", "me"))
	must(t, d.Reconcile(ctx))
	r, _ := s.GetCrew(ctx, "yardmaster")
	if got := f.workspaces(); got != "yardmaster-5 yardmaster-6" || !r.Live() || len(r.Left) != 1 || r.Left[0]["agent"] != "yardmaster-5" {
		t.Fatalf("after the next start: workspaces %q, crew member %+v", got, r)
	}
	// A session ended by yardr (here a restart) is stopped, which closes
	// what it ran in: nothing of it is kept, and the latest that was gone
	// stays.
	must(t, s.RequestRestart(ctx, "yardmaster", "test", "", "me"))
	must(t, d.Reconcile(ctx))
	r, _ = s.GetCrew(ctx, "yardmaster")
	if got := f.workspaces(); got != "yardmaster-5 yardmaster-7" || len(r.Left) != 1 {
		t.Fatalf("after a restart: workspaces %q, left %v", got, r.Left)
	}
}

// A workspace left by a gone session is not closed while an agent runs in
// it (someone started one there by hand), nor forgotten when herdr could
// not close it: its handle is kept and it is tried at the next start.
func TestCrewWorkspaceLeftWithAnAgentIsKept(t *testing.T) {
	s, f, d, at := shortLived(t)
	ctx := context.Background()
	gone := func() {
		t.Helper()
		f.set(agentOf(t, s), "")
		must(t, d.Reconcile(ctx)) // lived long enough: started again on this pass
	}
	must(t, d.Reconcile(ctx))
	at(2 * store.MinSessionLife)
	f.busy = map[string]bool{"yardmaster-1": true}
	gone()
	gone()
	gone()
	r, _ := s.GetCrew(ctx, "yardmaster")
	if got := f.workspaces(); got != "yardmaster-1 yardmaster-3 yardmaster-4" || len(r.Left) != 2 || r.Left[0]["agent"] != "yardmaster-1" {
		t.Fatalf("with an agent in the first: workspaces %q, left %v", got, r.Left)
	}
	f.busy, f.closeErr = nil, errors.New("herdr is down")
	gone()
	r, _ = s.GetCrew(ctx, "yardmaster")
	if got := f.workspaces(); got != "yardmaster-1 yardmaster-3 yardmaster-4 yardmaster-5" || len(r.Left) != 3 {
		t.Fatalf("herdr could not close: workspaces %q, left %v", got, r.Left)
	}
	f.closeErr = nil
	gone()
	r, _ = s.GetCrew(ctx, "yardmaster")
	if got := f.workspaces(); got != "yardmaster-5 yardmaster-6" || len(r.Left) != 1 || r.Left[0]["agent"] != "yardmaster-5" {
		t.Fatalf("once it can be closed: workspaces %q, left %v", got, r.Left)
	}
}

// A session that lives past the limit ends the streak, still running or gone
// since, so an early exit long ago does not add up with one today; the start
// alone does not, or two early exits would never be two in a row.
func TestCrewSessionThatLivesResetsFailedStarts(t *testing.T) {
	s, f, d, at := shortLived(t)
	ctx := context.Background()
	failures := func() int {
		r, _ := s.GetCrew(ctx, "yardmaster")
		return r.Failures
	}
	early := func() { // the running session exits a second after its start, and the next is started
		t.Helper()
		f.set(agentOf(t, s), "")
		at(time.Second)
		must(t, d.Reconcile(ctx))
		at(time.Hour)
		must(t, d.Reconcile(ctx))
		agentOf(t, s)
	}
	must(t, d.Reconcile(ctx))
	early()
	if failures() != 1 {
		t.Fatalf("failures %d right after the next start, want 1: a start alone does not reset", failures())
	}
	at(store.MinSessionLife - time.Second)
	must(t, d.Reconcile(ctx))
	if failures() != 1 {
		t.Fatalf("failures %d before the limit, want 1", failures())
	}
	// Still running past the limit.
	at(store.MinSessionLife + time.Second)
	must(t, d.Reconcile(ctx))
	if failures() != 0 {
		t.Fatalf("failures %d with a session past the limit, want 0", failures())
	}
	// Gone past the limit, between two passes: reset, and started at once.
	early()
	n := f.n
	f.set(agentOf(t, s), "")
	at(store.MinSessionLife + time.Second)
	must(t, d.Reconcile(ctx))
	if r, _ := s.GetCrew(ctx, "yardmaster"); !r.Live() || r.Failures != 0 || r.RetryAfter != nil || f.n != n+1 {
		t.Fatalf("a session gone after the limit: starts %d (was %d) crew member %+v", f.n, n, r)
	}
}

// Endings that were asked for are no failed starts, however soon after the
// start: a handoff, a requested restart (a rotation is one), a stop; nor is
// a session that ends itself once its restart was asked for.
func TestCrewWantedEndingsAreNoFailedStarts(t *testing.T) {
	s, f, d, at := shortLived(t)
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	at(time.Second)
	restarted := func(what string, n int) {
		t.Helper()
		must(t, d.Reconcile(ctx))
		r, _ := s.GetCrew(ctx, "yardmaster")
		if !r.Live() || r.Failures != 0 || r.RetryAfter != nil || f.n != n {
			t.Fatalf("%s: starts %d, want %d; crew member %+v", what, f.n, n, r)
		}
	}
	must(t, s.Journal(ctx, "yardmaster", store.NoteHandoff, "all quiet", "yardmaster"))
	restarted("handoff", 2)
	must(t, s.RequestRestart(ctx, "yardmaster", "requested by me", "", "me"))
	restarted("restart", 3)
	must(t, s.RequestRestart(ctx, "yardmaster", "requested by me", "", "me"))
	f.set(agentOf(t, s), "") // it exits by itself before the daemon stops it
	restarted("restart of a session that left", 4)
	must(t, s.SetCrewEnabled(ctx, "yardmaster", false, "", "me"))
	must(t, d.Reconcile(ctx))
	if r, _ := s.GetCrew(ctx, "yardmaster"); r.Live() || r.Failures != 0 || r.RetryAfter != nil {
		t.Fatalf("stop counted as a failed start: %+v", r)
	}
	evs, _ := s.Events(ctx, "yardmaster-journal", 100)
	if slices.ContainsFunc(evs, func(e store.Event) bool { return e.Kind == "crew_start_failed" }) {
		t.Fatal("a wanted ending logged crew_start_failed")
	}
}

// What the runtime says a crew member's session is doing is recorded on each
// pass, as the store's words for it, so a view reads it from the store and
// its events and asks no runtime.
func TestCrewStatusIsRecorded(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	agent := agentOf(t, s)
	for _, c := range []struct{ runtime, want string }{
		{"idle", "idle"}, {"working", "working"}, {"blocked", "blocked"}, {"done", "idle"}, {"starting", "unknown"},
	} {
		f.set(agent, c.runtime)
		must(t, d.Reconcile(ctx))
		r, err := s.GetCrew(ctx, "yardmaster")
		if err != nil || r.Status != c.want {
			t.Fatalf("runtime says %s: status %q, want %q (%v)", c.runtime, r.Status, c.want, err)
		}
	}
	// A pass that sees the same writes nothing.
	head, _ := s.Head(ctx)
	must(t, d.Reconcile(ctx))
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("an unchanged status was logged: head %d, was %d", now, head)
	}
}

// The nudge for mail that carries images ends with where they are, so the
// crew member reads them as it reads any file; one for mail without any
// says nothing of images.
func TestNudgeNamesTheImagesOfMail(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	d.Home = "/y"
	ctx := context.Background()
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	f.set(agentOf(t, s), "idle")
	must(t, s.Mail(ctx, "yardmaster", "deploy freeze", "chris"))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 || strings.Contains(f.nudges[0], "image") ||
		!strings.HasSuffix(f.nudges[0], " Answer mail with yardr reply <seq>, the number its inbox line starts with.") {
		t.Fatalf("nudges for mail without an image = %q", f.nudges)
	}
	shed, err := s.PutBlob(ctx, []byte("\x89PNG\r\n\x1a\n the shed"))
	must(t, err)
	fence, err := s.PutBlob(ctx, []byte("\xff\xd8\xff\xe0 the fence"))
	must(t, err)
	must(t, s.Mail(ctx, "yardmaster", "the shed", "chris", shed.Hash))
	must(t, d.Reconcile(ctx))
	if want := " 1 image: /y/blobs/" + shed.Hash; len(f.nudges) != 2 || !strings.Contains(f.nudges[1], want) ||
		!strings.HasSuffix(f.nudges[1], " Answer mail with yardr reply <seq>, the number its inbox line starts with.") {
		t.Fatalf("nudges = %q, want the last to name %q and end with how to answer it", f.nudges, want)
	}
	must(t, s.Mail(ctx, "yardmaster", "", "chris", fence.Hash))
	must(t, d.Reconcile(ctx))
	if want := " 2 images: /y/blobs/" + shed.Hash + " /y/blobs/" + fence.Hash; len(f.nudges) != 3 || !strings.Contains(f.nudges[2], want) ||
		!strings.HasSuffix(f.nudges[2], " Answer mail with yardr reply <seq>, the number its inbox line starts with.") {
		t.Fatalf("nudges = %q, want the last to name %q and end with how to answer it", f.nudges, want)
	}
}

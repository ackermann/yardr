package dispatch

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"yardr.dev/yardr/internal/store"
)

// CrewRunner starts always-on sessions that are not tied to a bead.
type CrewRunner interface {
	// StartCrew starts the line session ("") or the conversation session
	// for user of r, on that session's env.
	StartCrew(ctx context.Context, r store.CrewMember, user string) (handle map[string]any, err error)
}

// LiveAgent is an agent a runtime lists: its name there and the pane it
// runs in.
type LiveAgent struct{ Name, Pane string }

// CrewFinder is a CrewRunner whose runtime lists its agents, so it
// can tell when a session yardr did not start (a pane opened by hand under
// the crew member's name) already is a crew member. One without an agent list
// does not implement it, which reads as "none".
type CrewFinder interface {
	// LiveCrew is a live agent that already is the line session ("") or
	// the conversation session for user of r without being the session
	// its handle names, nil when there is none.
	LiveCrew(ctx context.Context, r store.CrewMember, user string) (*LiveAgent, error)
}

// CrewCloser is a CrewRunner in whose runtime a session that ends
// by itself leaves something open (herdr: the workspace, whose pane's shell
// outlives the harness). The dispatcher keeps the handle of every such
// session (store.CrewMember.Left) and has them closed when the crew
// member's next session starts: every session that never ran, and all but
// the latest of the rest. One that leaves nothing does not
// implement it, and no handle is kept.
type CrewCloser interface {
	// CloseLeft closes what the gone session of handle left open. kept is
	// true when it was left alone because an agent runs in it now: it is
	// asked again at a later start. What is closed already, or no longer
	// what yardr opened for r, is neither kept nor an error.
	CloseLeft(ctx context.Context, r store.CrewMember, handle map[string]any) (kept bool, err error)
}

// Nudger delivers a short message to a running session (an idle agent
// reads it as its next prompt). One that waits for the agent to start on
// it says with ErrAgentBlocked or ErrPromptStalled when it did not (see
// typeText, by which the dispatcher asks).
type Nudger interface {
	Nudge(ctx context.Context, handle map[string]any, text string) error
}

// DirectNudger submits a short message to a running session without
// waiting for the agent to start on it (herdr submits it itself). It is
// the delivery for a pane that shows no prompt of its harness, where
// nothing can be typed. Like a nudge it says ErrAgentBlocked when the
// agent is at a dialog and nothing was submitted.
type DirectNudger interface {
	NudgeDirect(ctx context.Context, handle map[string]any, text string) error
}

// Delivery ways a note's telling went, on its note_told event.
const (
	DeliveryTyped       = "typed"        // into the pane's prompt, as ever
	DeliveryAgentPrompt = "agent prompt" // through herdr, the pane showing none
)

const defaultHandoffGrace = 15 * time.Minute

// crewAskBound is how many member-sessions crew keeps at once: enough
// that one slow runner call no longer stacks behind every session before
// it, few enough that a sick runtime is not asked by the whole yard at
// once. It is fixed, not a setting, as reapAskBound is for reap.
const crewAskBound = 8

// crew keeps every enabled crew member running, delivers its inbox when
// it is idle, and rotates it through handoffs. One pass, idempotent. While
// paused by the upgrade gate it keeps them and tells them, but starts none.
//
// The sessions are kept side by side (keepCrewSession, at most
// crewAskBound at once), as reap judges its sessions: one slow runner
// call then costs the pass about that call, not itself stacked behind
// every member-session before it. A session's whole keep runs in its one
// worker, in the order it always ran: nothing inside a session is
// parallel, since its nudge and stop order is the semantics. No order
// is promised across sessions: each has its own map key and crew-session
// row, and runner asks carry that session's handle. Events may share a
// member's journal, where the session's user identifies them, and their
// append order is unspecified. What the pass kept of each is merged back
// in member order after the join (applyCrewMemory), timed asks are folded
// into the pass's slowest (passTiming.ask), and starts a keep asked for
// are handed there too, first per member as the serial pass handed them.
// crewUsage and its serial order stay before the workers, and
// harnessLogins after them.
func (d *Dispatcher) crew(ctx context.Context, paused bool) error {
	if _, err := d.Store.ExpireMemories(ctx); err != nil {
		d.Log.Warn("expire memories", "err", err)
	}
	rs, err := d.Store.Crew(ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		d.noteHomeCrew(r)
	}
	// Before the sessions are kept: one that ends in this pass is first read in
	// the next, once its agent has had the time between to write its last lines
	// (as usage runs before teardown).
	d.crewUsage(ctx, rs)
	jobs := d.crewJobs(ctx, rs, paused)
	outcomes := make([]*crewOutcome, len(jobs))
	sem := make(chan struct{}, crewAskBound)
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out := d.keepCrewSession(ctx, jobs[i])
			outcomes[i] = &out
		}()
	}
	wg.Wait()
	for i, job := range jobs {
		out := outcomes[i]
		if out == nil {
			continue
		}
		key := job.session.key()
		for _, c := range out.calls {
			d.timing.ask(key, c.name, c.took)
		}
		d.applyCrewMemory(key, out.mem)
		for _, start := range out.starts {
			// One start in flight per crew member: the first session
			// in member order that asked for one is handed it, as the
			// serial pass handed it and left the rest to a later pass.
			if _, in := d.crewInFlight[start.session.member.Name]; in {
				continue
			}
			d.handCrewStart(ctx, start)
		}
		if out.err != nil {
			d.Log.Error("crew", "crew", job.session.member.Name, "session", job.session.label(), "err", out.err)
		}
	}
	// From the login marks as the pass read them: one a pass sets is
	// read on the next, which its write wakes.
	d.harnessLogins(ctx, rs)
	return nil
}

// memberSession is one session of a crew member on a daemon pass: the
// member's definition with the user of the session ("": the line session)
// and a pass-local mirror of that session's runtime state.
type memberSession struct {
	member store.CrewMember
	user   string
	st     store.CrewSession
}

func newMemberSession(member store.CrewMember, user string) memberSession {
	return memberSession{member: member, user: user, st: member.Session(user)}
}

// key is the session in the daemon's per-session maps (d.status and the
// rest): "crew:<name>" for the line session, "crew:<name>:<user>" for a
// conversation.
func (s memberSession) key() string {
	if s.user == "" {
		return "crew:" + s.member.Name
	}
	return "crew:" + s.member.Name + ":" + s.user
}

// label names the session in logs: the crew member, and the user of a
// conversation.
func (s memberSession) label() string {
	if s.user == "" {
		return s.member.Name
	}
	return s.member.Name + " for " + s.user
}

// what says which session this is in a message: the line session, or the
// conversation for its user.
func (s memberSession) what() string {
	if s.user == "" {
		return "line session"
	}
	return "conversation session for " + s.user
}

// who names the session in event data: the crew member, and the user of a
// conversation session.
func (s memberSession) who(data map[string]any) map[string]any {
	data["crew"] = s.member.Name
	if s.user != "" {
		data["user"] = s.user
	}
	return data
}

// restartCmd is the command that restarts this session: crew restart,
// naming the user of a conversation.
func (s memberSession) restartCmd() string {
	if s.user == "" {
		return "yardr crew restart " + s.member.Name
	}
	return "yardr crew restart " + s.member.Name + " --for " + s.user
}

// operatorName is the yard's operator setting, "" where unset: the line
// session runs where the crew member serves them.
func (d *Dispatcher) operatorName(ctx context.Context) string {
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		return ""
	}
	return settings[store.OperatorSetting]
}

func (d *Dispatcher) keepSession(ctx context.Context, runner Runner, starter CrewRunner, s *memberSession, paused bool) error {
	def := s.member
	// A start in flight for this member, on its worker or finished and not
	// yet drained (crewstarts.go).
	startKey, starting := d.crewInFlight[def.Name]
	if starting && startKey == s.key() {
		// This session is the one being started. The store may already say
		// it runs (the worker wrote CrewStartedFor while this pass was under
		// way), but what the session before it left in the pass's maps is
		// cleared only by the drain, and a look before that would read it
		// (a stale crewWorkHistory refuses the new session's trust dialog):
		// nothing of it is kept until the drain.
		return nil
	}
	if s.st.Live() {
		status, err := d.crewStatus(ctx, runner, s)
		if err != nil {
			return nil // runtime unreachable: unknown, leave it be
		}
		idle := status == "idle" || status == "done"
		d.crewSeen(ctx, s, status)
		grace := durationOr(def.HandoffGrace, defaultHandoffGrace)
		// A session that has lived MinSessionLife started for good, running
		// still or gone since: the starts that failed before it are over.
		lived, early := time.Duration(0), false
		if s.st.StartedAt != nil {
			lived = d.now().Sub(*s.st.StartedAt)
			early = lived < store.MinSessionLife
		}
		if s.st.Failures > 0 && !early {
			if err := d.Store.CrewSettledFor(ctx, def.Name, s.user); err != nil {
				return err
			}
			s.st.Failures = 0
		}
		switch {
		case !def.Enabled:
			if paused {
				// The upgrade gate pauses dispatch without stopping an
				// already-running crew member. Apply the disable after it lifts.
				return nil
			}
			return d.stopCrew(ctx, runner, s, "disabled")
		case status == "":
			// Nothing is closed here: the pane it leaves shows what the
			// harness printed before it exited, the only place the reason
			// can be read. The handle is kept instead, where the runtime
			// has something to close later (see closeLeft).
			tail := ""
			if early && s.st.RestartRequested == "" {
				// A start that failed: its pane's last lines are read now,
				// while the shell still holds them, and go on the journal
				// with the failure. The handle is marked, so the next
				// start closes what it left even as the latest: nothing of
				// the diagnosis is lost with it.
				if t, ok := runner.(Tailer); ok && len(s.st.Handle) > 0 {
					done := askTimer(ctx, askPane)
					tail, _ = t.Tail(ctx, s.st.Handle, stallTail)
					done()
				}
				if _, ok := runner.(CrewCloser); ok && len(s.st.Handle) > 0 {
					d.setCrewHandle(ctx, s, withHandle(s.st.Handle, earlyExit, true))
				}
			}
			gone := d.Store.CrewStoppedFor
			if _, ok := runner.(CrewCloser); ok {
				gone = d.Store.CrewGoneFor
				if len(s.st.Handle) > 0 {
					s.st.Left = append(s.st.Left, s.st.Handle)
				}
			}
			if err := gone(ctx, def.Name, s.user, "the session is gone"); err != nil {
				return err
			}
			// Its handle is kept where the runtime closes leftovers, not
			// where the home-gone close looks.
			d.dropHome(crewHomeKey(def.Name, s.user))
			s.st.State = "stopped"
			if early && s.st.RestartRequested == "" {
				// Its harness exits at once (a broken install, a bad flag, an
				// expired login): started again on this pass it would do the
				// same every few seconds, forever. An ending that was asked
				// for (a handoff, a rotation, a restart) is not one of these.
				return d.crewStartFailed(ctx, runner, s, nil, tail, fmt.Errorf(
					"the session ended %s after its start (gone within %s counts as a failed start)",
					lived.Round(time.Second), store.MinSessionLife))
			}
		case s.st.RestartRequested != "" &&
			(idle || (s.st.RestartRequestedAt != nil && time.Since(*s.st.RestartRequestedAt) > grace)):
			if paused {
				// Keep the running agent through the upgrade; perform its
				// requested restart after the gate lifts.
				return nil
			}
			if err := d.stopCrew(ctx, runner, s, "restart: "+s.st.RestartRequested); err != nil {
				return err
			}
			s.st.State = "stopped"
		case s.st.RestartRequested != "":
			return nil // let it finish its handoff turn
		default:
			return d.crewRunning(ctx, runner, s, status, grace)
		}
	}
	if !def.Enabled || s.st.State == "failed" {
		return nil
	}
	if s.st.RetryAfter != nil && d.now().Before(*s.st.RetryAfter) {
		return nil
	}
	if starting {
		// One start in flight per crew member, and no queue: while its
		// worker starts one session (the line's or a conversation's) the
		// member starts no other, and a later pass asks again once the drain
		// has applied that start. Before the check below, which would find
		// the agent being started and call it a second session.
		return nil
	}
	if finder, ok := runner.(CrewFinder); ok {
		other, err := finder.LiveCrew(ctx, def, s.user)
		if err != nil {
			return d.crewStartFailed(ctx, runner, s, nil, "", err)
		}
		if other != nil {
			// A second session under one name: both would act as the
			// crew member and neither would know of the other. Nothing is
			// started and nothing counted as a failure, so the next pass
			// starts it once a human has ended the other.
			reason := fmt.Sprintf("agent %q in pane %s already is this crew member's %s; end it and the session starts",
				other.Name, other.Pane, s.what())
			key := s.key()
			if d.status[key] != "blocked: "+reason {
				d.status[key] = "blocked: " + reason
				d.Log.Warn("crew session not started", "crew", def.Name, "session", s.label(), "reason", reason)
				_ = d.Store.LogDispatch(ctx, def.Journal, "crew_blocked", actor, s.who(map[string]any{
					"reason": reason, "agent": other.Name, "pane": other.Pane}))
			}
			return nil
		}
	}
	if paused {
		// The upgrade gate: start nothing. Not a failed start:
		// nothing is counted and no wait is set.
		d.Log.Info("would start, paused by the upgrade gate", "crew", def.Name, "session", s.label())
		return nil
	}
	// Hand the session to its member's worker and go on: the worker runs
	// StartCrew beside the pass (crewstarts.go) and writes the start to the
	// store, and the next pass drains its completion, where a failed start
	// is counted (crewStartFailed) and a started one forgets what the session
	// before it left and has what gone sessions left open closed (closeLeft).
	// On a crew worker's clone the hand is recorded instead, and the pass
	// hands it in member order after the join (queueCrewStart).
	d.queueCrewStart(ctx, crewJob{session: *s, runner: runner, starter: starter})
	return nil
}

// earlyExit marks the handle of a crew session that never ran (an early
// exit, gone within store.MinSessionLife): its pane's last lines are on the
// journal beside its failure, so the next start closes what it left even as
// the latest (see closeLeft).
const earlyExit = "early_exit"

// closeLeft closes what r's sessions that were gone left open in the
// runtime: every session that never ran, latest or not, and all but the
// latest of the rest — that one still shows why the last session that died
// did. It runs when a session has started, so a crew member that is given
// up keeps at most two, and one that is started again goes back to one, or
// to none when every leftover never ran. Only handles yardr recorded are
// closed, never what merely carries the crew member's label; one the runner
// keeps or cannot close stays listed for the next start.
func (d *Dispatcher) closeLeft(ctx context.Context, runner Runner, s *memberSession) error {
	closer, ok := runner.(CrewCloser)
	if !ok || len(s.st.Left) == 0 {
		return nil
	}
	spared := -1
	for i := len(s.st.Left) - 1; i >= 0; i-- {
		if s.st.Left[i][earlyExit] != true {
			spared = i
			break
		}
	}
	left := make([]map[string]any, 0, len(s.st.Left))
	for i, h := range s.st.Left {
		if i == spared {
			left = append(left, h)
			continue
		}
		kept, err := closer.CloseLeft(ctx, s.member, h)
		if err != nil {
			d.Log.Warn("crew: closing what a gone session left", "crew", s.member.Name, "session", s.label(), "err", err)
			kept = true
		}
		if kept {
			left = append(left, h)
		}
	}
	if len(left) == len(s.st.Left) {
		return nil
	}
	return d.Store.CrewLeftFor(ctx, s.member.Name, s.user, left)
}

// crewAgentSession records the id the runner names for the agent of the live
// crew session s on its handle, as agentSession does for a bead's session:
// within ModelWindow of the start, so a kind the runner names no id for is not
// asked on every pass for ever. With last it asks past the window too, as
// the session is stopped and while its agent's tab is open (lastAgentSession):
// a crew session lives for a day, and an agent that was slow to start its own
// session would be missed by the first look alone. The ending of the session
// copies the id off the handle, which does not outlive it (store.CrewUncounted).
// s.st.Handle is updated with what it stores.
func (d *Dispatcher) crewAgentSession(ctx context.Context, r Runner, s *memberSession, last bool) {
	if !s.st.Live() || !last && (s.st.StartedAt == nil || d.now().Sub(*s.st.StartedAt) > ModelWindow) {
		return
	}
	// A crew handle carries no worktree, so the match the start is for has
	// nothing to match; the zero start of a session that never started one
	// is never read.
	var started time.Time
	if s.st.StartedAt != nil {
		started = *s.st.StartedAt
	}
	id := askAgentSession(ctx, r, s.st.Handle, started)
	if id == "" {
		return // none yet, or unreachable: asked again next pass
	}
	h := withHandle(s.st.Handle, AgentSession, id)
	if err := d.Store.SetCrewHandleFor(ctx, s.member.Name, s.user, h); err != nil {
		d.Log.Warn("record agent session", "crew", s.member.Name, "session", s.label(), "err", err)
		return
	}
	s.st.Handle = h
}

// crewUsage ends the count of every crew session that ended and is due one with
// one result, recorded by the store in the write that finishes the count
// (store.RecordCrewUsage), by the rules of a bead's session (usage): what the
// agent used, or why there is no count. The entries to read are the ones that
// wait on each session of members (store.CrewSession.Uncounted), the line
// session's and every conversation's, whether the config still declares the
// conversation or not.
func (d *Dispatcher) crewUsage(ctx context.Context, members []store.CrewMember) {
	for _, m := range members {
		r, ok := d.Runners[m.Runner].(UsageReader)
		if !ok {
			continue // a runner that is gone leaves the entries as they are
		}
		d.crewSessionUsage(ctx, r, m.Name, "", m.CrewSession)
		for _, user := range slices.Sorted(maps.Keys(m.Conversations)) {
			d.crewSessionUsage(ctx, r, m.Name, user, m.Conversations[user])
		}
	}
}

// crewSessionUsage reads and records the count of each entry of the crew
// session of name's user ("": the line session), oldest first.
func (d *Dispatcher) crewSessionUsage(ctx context.Context, r UsageReader, name, user string, st store.CrewSession) {
	for _, due := range st.Uncounted {
		// The agent's file is found by its kind and its id alone. The handle
		// is gone, and with it what a match by worktree would go by (a crew
		// member runs in a directory of its own, not a bead's worktree), so a
		// kind that herdr names no id for has none to count.
		handle := map[string]any{store.HandleKind: due.Kind, AgentSession: due.AgentSession}
		said := "usage:crew:" + name + ":" + user + ":" + due.AgentSession
		// The handle is gone, and with it what a match by worktree would go
		// by, so the start is never read; a session that never started one
		// passes the zero time.
		var started time.Time
		if due.StartedAt != nil {
			started = *due.StartedAt
		}
		rec := d.countRecord(ctx, r, handle, started, due.StoppedAt, said, "crew", name, "user", user)
		if rec == nil {
			continue
		}
		if err := d.Store.RecordCrewUsage(ctx, name, user, due.AgentSession, rec); err != nil {
			d.Log.Warn("record crew usage", "crew", name, "user", user, "err", err)
		}
	}
}

// crewSeen records what the runtime says the session is doing, so that
// who looks at the crew (yardr chat) reads it from the store and its
// event stream and asks no runtime. The store writes a change only. A
// session that is gone is recorded by CrewStopped, not here.
func (d *Dispatcher) crewSeen(ctx context.Context, s *memberSession, status string) {
	switch status {
	case "":
		return
	case "done":
		status = "idle"
	case "working", "idle", "blocked":
	default:
		status = "unknown"
	}
	if status == "working" {
		d.crewWorkHistory[s.key()] = true
	}
	if status == s.st.Status {
		return
	}
	if err := d.Store.CrewStatusFor(ctx, s.member.Name, s.user, status); err != nil {
		d.Log.Warn("crew member status", "crew", s.member.Name, "session", s.label(), "err", err)
	}
}

// crewWorked reports whether the current crew session has ever been seen
// working. crew_status events are global, so the latest matching
// crew_started event in the member's journal bounds the session's history.
// The result is cached for this dispatcher; a new dispatcher reconstructs it
// from the same store before answering a startup trust dialog.
func (d *Dispatcher) crewWorked(ctx context.Context, s memberSession) (bool, error) {
	key := s.key()
	if worked, known := d.crewWorkHistory[key]; known {
		return worked, nil
	}
	const allEvents = int(^uint(0) >> 1)
	starts, err := d.Store.Events(ctx, s.member.Journal, allEvents)
	if err != nil {
		return false, err
	}
	var start uint64
	for _, event := range starts {
		if event.Kind == "crew_started" && crewSessionEvent(event, s.member.Name, s.user) {
			start = event.Seq
		}
	}
	if start == 0 {
		// Without the current start event there is no proof that the session
		// has never worked. Keep the trust guard closed.
		d.crewWorkHistory[key] = true
		return true, nil
	}
	events, err := d.Store.EventsSince(ctx, start, 0)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.Kind == "crew_status" && event.Data["status"] == "working" && crewSessionEvent(event, s.member.Name, s.user) {
			d.crewWorkHistory[key] = true
			return true, nil
		}
	}
	d.crewWorkHistory[key] = false
	return false, nil
}

func crewSessionEvent(event store.Event, name, user string) bool {
	if event.Data["crew"] != name {
		return false
	}
	eventUser, _ := event.Data["user"].(string)
	return eventUser == user
}

// crewStartFailed counts a start that did not happen, or whose session
// was gone at once, against the session and backs it off; handle is what
// the runner left behind. tail is the pane's last lines of a session that
// was gone at once, "" where there are none: they go on the journal beside
// the failure, since the next start closes what the session left.
func (d *Dispatcher) crewStartFailed(ctx context.Context, runner Runner, s *memberSession, handle map[string]any, tail string, err error) error {
	backoff := min(30*time.Second<<min(s.st.Failures, 5), 10*time.Minute)
	if s.st.Failures+1 >= store.MaxStartFails {
		d.Log.Error("crew session given up", "crew", s.member.Name, "session", s.label(), "err", err, "failures", s.st.Failures+1)
	} else {
		d.Log.Warn("crew start failed", "crew", s.member.Name, "session", s.label(), "err", err, "failures", s.st.Failures+1, "retry_in", backoff)
	}
	key := s.key()
	if IsBlocked(err) && d.status[key] != "blocked: "+err.Error() {
		// Its configuration refuses it (an env file missing or open to
		// others): a human has to act, so say so, once per reason.
		d.status[key] = "blocked: " + err.Error()
		_ = d.Store.LogDispatch(ctx, s.member.Journal, "crew_blocked", actor, s.who(map[string]any{
			"reason": err.Error()}))
	}
	if len(handle) > 0 {
		done := askTimer(ctx, askNudge)
		stopErr := runner.Stop(ctx, handle)
		done()
		if stopErr == nil && s.member.Runner == "herdr" && hasHomeTab(handle) {
			d.dropHome(crewHomeKey(s.member.Name, s.user))
		}
	}
	if err := d.Store.CrewStartFailedFor(ctx, s.member.Name, s.user, err.Error(), backoff); err != nil {
		return err
	}
	if tail != "" {
		if err := d.Store.LogDispatch(ctx, s.member.Journal, "crew_start_tail", actor, s.who(map[string]any{"tail": tail})); err != nil {
			d.Log.Warn("crew session's last lines", "crew", s.member.Name, "session", s.label(), "err", err)
		}
	}
	return nil
}

// inlineMailBound is how long one mail's nudge line may be, in characters:
// a longer line falls back to counting the inbox.
const inlineMailBound = 600

// inlineNudgeMail is the nudge's first line where the slice holds exactly
// one unread entry and it is a short mail: its seq, sender and text on one
// line, so answering it costs the member no inbox run. Anything else (more
// entries, a longer line, a non-mail kind) keeps the count line, and reads
// as not ok.
func inlineNudgeMail(page *store.InboxPage) (string, bool) {
	if page.Total != 1 || len(page.Events) != 1 || page.Events[0].Kind != "mail" {
		return "", false
	}
	e := page.Events[0]
	text, _ := e.Data["text"].(string)
	line := fmt.Sprintf("yardr: %d mail from %s: %s", e.Seq, flatMailWords(store.MailFrom(e)), flatMailWords(text))
	if utf8.RuneCountInString(line) > inlineMailBound {
		return "", false
	}
	return line, true
}

// flatMailWords are a mail's words where the nudge types them: letters, on
// one line, so the text never opens an unmarked line of its own in the pane.
func flatMailWords(s string) string {
	return strings.ReplaceAll(store.OneLine(s), "\t", " ")
}

// crewRunning handles a healthy session: a first prompt still to
// deliver, text of yardr's own left in its input, then rotation, then the
// inbox.
func (d *Dispatcher) crewRunning(ctx context.Context, runner Runner, s *memberSession, status string, grace time.Duration) error {
	def := s.member
	key := s.key()
	idle := status == "idle" || status == "done"
	// First, so that what the passes below write of the handle holds the id.
	d.crewAgentSession(ctx, runner, s, false)
	if _, pending := s.st.Handle[store.PendingPrompt]; !pending {
		// A login screen is a crew state even when there is no inbox nudge
		// due. In particular, Claude's selected login option can make the
		// runtime report idle, so do not wait for a nudge to read the pane.
		if at, why := d.loginAt(ctx, runner, key, s.st.Handle); at {
			if line := d.logins[key]; line != "" {
				d.markCrewLogin(ctx, s, line)
			}
			if why.tells() {
				d.nudgeHeld(ctx, def.Journal, s.who(map[string]any{}), s.st.Handle, "a nudge", why)
			}
			return nil
		}
	}
	if p, ok := s.st.Handle[store.PendingPrompt].(string); ok {
		trustAllowed := true
		trustBypass := false
		// A working agent is past its trust dialog; every other status,
		// idle included, may still sit at one (see pendingPrompt).
		if _, canAnswer := runner.(TrustAcceptor); canAnswer && status != "working" {
			worked, err := d.crewWorked(ctx, *s)
			if err != nil {
				trustAllowed = false
				d.Log.Error("crew status history", "crew", def.Name, "session", s.label(), "err", err)
			} else {
				trustAllowed = !worked
				// A counted send may still be untaken: a delivery the
				// agent dropped, or a start that could not look. With
				// history saying the session never worked, the trust
				// answer stays open for one the pane demonstrably has
				// no echo of (see pendingPrompt).
				trustBypass = !worked
			}
		}
		pass := d.pendingPrompt(ctx, runner, key, s.st.Handle, p, status, def.Config, trustAllowed, trustBypass)
		if pass.handle != nil {
			if err := d.Store.SetCrewHandleFor(ctx, def.Name, s.user, pass.handle); err != nil {
				return err
			}
			// Kept as the session's, as a bead's is: what is marked below
			// builds on it.
			s.st.Handle = pass.handle
		}
		d.promptEvents(ctx, def.Journal, s.who(map[string]any{}), s.st.Handle, p, pass)
		if pass.arrived {
			if !idle {
				// Arrival can end the first-prompt branch before the usual
				// non-idle path below. Do not keep an unknown-dialog mark
				// from an idle spell once the agent is seen working.
				d.crewHarnessDialogOver(ctx, s)
			}
			// An agent working on its first prompt does not wait for a human.
			delete(d.status, key)
			return nil
		}
		if pass.trusted {
			// Its trust dialog was answered on this pass: no human waits,
			// and the prompt goes on the usual path once the agent is idle.
			if pass.trustedBypass {
				// Answered although the prompt was counted sent: say what
				// the bypass rested on, so the next sit is diagnosable
				// from the log as q7rd's was.
				d.Log.Warn("trust dialog answered for a counted send the session never took: history says it never worked and its pane has no echo of the prompt",
					"crew", def.Name, "session", s.label(), "sends", pass.sends, "unconfirmed", s.st.Handle[PromptUnconfirmed] == true)
			}
			return nil
		}
		if idle {
			// Idle at its start at a dialog yardr does not know is told
			// of, the way a bead's session is: a crew session has no idle
			// limit, so without this nothing is ever told (yardr-5v7m).
			d.crewHarnessDialog(ctx, runner, s)
			return nil
		}
		d.crewHarnessDialogOver(ctx, s)
		// At its login screen it waits on no human answer but a login:
		// that is said instead, from the login's own memory.
		if at, why := d.loginAt(ctx, runner, key, s.st.Handle); at {
			if line := d.logins[key]; line != "" {
				d.markCrewLogin(ctx, s, line)
			}
			if why.tells() {
				d.nudgeHeld(ctx, def.Journal, s.who(map[string]any{}), s.st.Handle, "the first prompt", why)
			}
			return nil
		}
		// Waiting on a human (e.g. a trust prompt): say so once.
		if d.status[key] != "waiting" {
			d.status[key] = "waiting"
			d.Log.Warn("crew session waiting for a human before it can start", "crew", def.Name, "session", s.label())
			_ = d.Store.LogDispatch(ctx, def.Journal, "crew_blocked", actor, s.who(map[string]any{
				"reason": "not ready at startup; answer it in its pane"}))
		}
		return nil
	}
	d.crewPromptTaken(ctx, runner, s, status)
	if d.ownInput(ctx, runner, key, def.Journal, s.who(map[string]any{}), s.st.Handle, status) {
		return nil // Enter went into its pane: nothing else does on this pass
	}
	if maxAge := durationOr(def.MaxAge, 0); maxAge > 0 && s.st.StartedAt != nil && time.Since(*s.st.StartedAt) > maxAge {
		switch {
		case s.st.RotationAskedAt == nil && idle:
			typed, err := d.nudge(ctx, runner, s, fmt.Sprintf(
				"Your session is due for rotation. Write your handoff now: yardr crew handoff %s - with the words on stdin: what you are doing, what is next, what to watch. "+
					"yardr then starts your next session from it.", def.Name))
			if err != nil || !typed {
				return err // not asked yet: the grace runs from the asking
			}
			return d.Store.MarkRotationAskedFor(ctx, def.Name, s.user)
		case s.st.RotationAskedAt != nil && time.Since(*s.st.RotationAskedAt) > grace:
			return d.Store.RequestRestartFor(ctx, def.Name, s.user, "rotation (no handoff within grace)", "", "yard")
		}
	}
	if idle {
		if asked, err := d.crewContext(ctx, runner, s); asked || err != nil {
			return err
		}
	}
	if !idle {
		delete(d.drafts, key) // what was in its input was sent
		delete(d.dialogs, key)
		if status == "working" {
			// Working: past any login screen seen before. A login
			// screen may read as blocked, so that status keeps the mark.
			d.markCrewLogin(ctx, s, "")
		}
		d.crewHarnessDialogOver(ctx, s)
		d.crewHarnessOver(ctx, s, false)
		return nil
	}
	page, err := d.Store.InboxFor(ctx, def.Name, s.user, 5)
	// Idle with something unread is where a harness stopped on an error
	// sits (harness.go), nudged already or not.
	if err == nil && page.Total > 0 {
		if prompted, err := d.crewReset(ctx, runner, s, page.Total, page.Last); prompted || err != nil {
			return err
		}
		d.crewHarnessError(ctx, runner, s)
	} else if err == nil {
		d.crewHarnessOver(ctx, s, true)
	}
	if err != nil || page.Total == 0 || page.Last <= s.st.Notified || d.crewNudgeHeld(s) {
		// Nothing to type, or held for an error of its harness: text in the
		// input holds nothing back, and a wait on it is over.
		delete(d.drafts, key)
		return err
	}
	// Mail from another yard is named with its sender, <name>@<peer>, and
	// what it is: the nudge is the first a crew member reads of it. A name
	// the yard trusts as a user (store.Peer.Trust) says whom it speaks as,
	// and is told apart from a stranger's.
	var kinds, outside, trusted []string
	var files []store.BlobRef
	for _, e := range page.Events {
		kind := e.Kind
		files = append(files, store.EventFiles(e)...)
		if store.FromOutside(e) {
			from := store.MailFrom(e)
			kind = "mail from " + from
			to := &outside
			if store.SpeaksAs(e) != "" {
				to = &trusted
			}
			if !slices.Contains(*to, from) {
				*to = append(*to, from)
			}
		}
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	more := ""
	if page.Total > len(page.Events) {
		more = ", …"
	}
	msg := fmt.Sprintf("yardr: %d new in your inbox (%s%s). Run yardr inbox to read them, and yardr inbox --ack <seq> through the last you handled.",
		page.Total, strings.Join(kinds, ", "), more)
	if line, ok := inlineNudgeMail(page); ok {
		msg = line
	}
	if len(outside) > 0 {
		msg += fmt.Sprintf(" Mail from %s is %s.", strings.Join(outside, ", "), store.OutsideMail)
	}
	if len(trusted) > 0 {
		msg += fmt.Sprintf(" Mail from %s is %s: the operator trusts the name as them.", strings.Join(trusted, ", "), store.TrustedMail)
	}
	if slices.Contains(kinds, "mail") || slices.ContainsFunc(kinds, func(k string) bool {
		return strings.HasPrefix(k, "mail from ")
	}) {
		msg += " Answer mail with yardr reply <seq>, the number its inbox line starts with."
	}
	// The files the inbox carries, each by its path: what a crew member's
	// harness reads a file by, on a marked line of their own as where each
	// event itself is read.
	if line := store.NoteFilesLine(d.Home, files); line != "" {
		msg += "\n" + line
	}
	// The message is made on the pass that types it: one held back by text
	// in the input (see inputHeld) counts the inbox anew when its turn comes.
	typed, err := d.nudge(ctx, runner, s, msg)
	if err != nil || !typed {
		return err
	}
	d.crewHarnessLook(s)
	return d.Store.MarkNotifiedFor(ctx, def.Name, s.user, page.Last)
}

func (d *Dispatcher) crewStatus(ctx context.Context, runner Runner, s *memberSession) (string, error) {
	if st, ok := runner.(Statuser); ok {
		done := askTimer(ctx, askStatus)
		status, err := st.Status(ctx, s.st.Handle)
		done()
		return status, err
	}
	done := askTimer(ctx, askStatus)
	alive, err := runner.Alive(ctx, s.st.Handle)
	done()
	if err != nil || !alive {
		return "", err
	}
	return "idle", nil
}

// nudge types text into the session as its next prompt. typed is false
// when it did not, because the input holds text (see inputHeld): the nudge
// is then still due, and the caller records nothing as said. So it is while
// a prompt given at a usage limit's reset has not shown. A runner that
// cannot nudge has nothing to wait for.
func (d *Dispatcher) nudge(ctx context.Context, runner Runner, s *memberSession, text string) (typed bool, err error) {
	n, ok := runner.(Nudger)
	if !ok {
		return true, nil
	}
	if s.st.Handle[harnessPrompted] != nil {
		// The prompt yardr gave at a usage limit's reset is not known to be
		// taken: the harness may be asking something first, and a nudge's
		// Enter would answer it (see crewPromptTaken).
		return false, nil
	}
	key := s.key()
	if held, why := d.inputHeld(ctx, runner, key, s.st.Handle); held {
		// At the login screen, told or not: inputHeld looks for it
		// first, so the memory is this pass's.
		if line := d.logins[key]; line != "" {
			d.markCrewLogin(ctx, s, line)
		}
		if why.tells() {
			d.nudgeHeld(ctx, s.member.Journal, s.who(map[string]any{}), s.st.Handle, "a nudge", why)
		}
		return false, nil
	}
	stalled, err := typeText(ctx, n, s.st.Handle, text)
	if errors.Is(err, ErrAgentBlocked) {
		// At a dialog a nudge's Enter would answer: held like one behind
		// someone's text, and said once for as long as nothing is typed.
		if d.status[key] != nudgeAtDialog {
			d.status[key] = nudgeAtDialog
			data := s.who(map[string]any{"reason": "the agent is at a dialog, so a nudge is not typed there: " +
				"its Enter would answer the dialog. It is typed once the dialog is gone. Answer it in its pane"})
			if pane, _ := s.st.Handle["pane"].(string); pane != "" {
				data["pane"] = pane
			}
			d.Log.Warn("a dialog in the agent's pane holds back a nudge", "crew", s.member.Name, "session", s.label())
			_ = d.Store.LogDispatch(ctx, s.member.Journal, "nudge_held", actor, data)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Typed: past any login screen seen before. A look that failed says
	// nothing either way; the next look that finds it marks it again.
	d.markCrewLogin(ctx, s, "")
	if d.status[key] == nudgeAtDialog {
		delete(d.status, key)
	}
	now := d.now()
	d.nudged[key] = ownNudge{text: text, at: now}
	if stalled {
		// The runner waited for the agent to start on the nudge and saw
		// nothing. If it is in the input, that is the text ownInput sends
		// Enter for once it has been there for PromptSettle, on a later
		// pass. It has been there for longer: the settle is taken as over,
		// and ownInput looks now. An agent the runner saw neither working
		// nor at a dialog is idle to it.
		d.unsent[key] = &unsent{text: squash(text), since: now.Add(-d.PromptSettle)}
		d.ownInput(ctx, runner, key, s.member.Journal, s.who(map[string]any{}), s.st.Handle, "idle")
		// Or the harness answered the nudge with a question of its own and
		// keeps it until that is answered (a dialog of its file, harnesses/).
		// The nudge is typed and counts as said, so no later pass has
		// anything to type that would find the dialog: it is told of here.
		if _, why := d.dialogAt(ctx, runner, key, s.st.Handle); why.tells() {
			d.nudgeHeld(ctx, s.member.Journal, s.who(map[string]any{}), s.st.Handle, "the nudge yardr typed", why)
		}
	}
	return true, nil
}

// nudgeAtDialog is a crew member's d.status while a nudge for it is held
// back by a dialog its agent is at (see nudge).
const nudgeAtDialog = "nudge held: at a dialog"

func (d *Dispatcher) stopCrew(ctx context.Context, runner Runner, s *memberSession, reason string) error {
	if len(s.st.Handle) > 0 {
		// Before the tab closes, which makes herdr forget the agent.
		d.crewAgentSession(ctx, runner, s, true)
		done := askTimer(ctx, askNudge)
		err := runner.Stop(ctx, s.st.Handle)
		done()
		if err != nil {
			d.Log.Warn("crew stop", "crew", s.member.Name, "session", s.label(), "err", err)
		} else {
			// Keep the handle in the home-loss snapshot if the runner could
			// not close it; a home watch can cancel this pass's stop.
			d.dropHome(crewHomeKey(s.member.Name, s.user))
		}
	}
	d.Log.Info("crew session stopped", "crew", s.member.Name, "session", s.label(), "reason", reason)
	return d.Store.CrewStoppedFor(ctx, s.member.Name, s.user, reason)
}

func (d *Dispatcher) reportCrew(name, msg string) {
	if d.noted("crew member", name, msg) {
		return
	}
	d.Log.Warn("crew", "crew", name, "problem", msg)
}

func durationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

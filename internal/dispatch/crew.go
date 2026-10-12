package dispatch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// CrewRunner starts always-on sessions that are not tied to a bead.
type CrewRunner interface {
	StartCrew(ctx context.Context, r store.CrewMember) (handle map[string]any, err error)
}

// LiveAgent is an agent a runtime lists: its name there and the pane it
// runs in.
type LiveAgent struct{ Name, Pane string }

// CrewFinder is a CrewRunner whose runtime lists its agents, so it
// can tell when a session yardr did not start (a pane opened by hand under
// the crew member's name) already is a crew member. One without an agent list
// does not implement it, which reads as "none".
type CrewFinder interface {
	// LiveCrew is a live agent that already is r without being the
	// session r.Handle names, nil when there is none.
	LiveCrew(ctx context.Context, r store.CrewMember) (*LiveAgent, error)
}

// CrewCloser is a CrewRunner in whose runtime a session that ends
// by itself leaves something open (herdr: the workspace, whose pane's shell
// outlives the harness). The dispatcher keeps the handle of every such
// session (store.CrewMember.Left) and has all but the latest closed when the
// crew member's next session starts. One that leaves nothing does not
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

const defaultHandoffGrace = 15 * time.Minute

// crew members keeps every enabled crew member running, delivers its inbox when
// it is idle, and rotates it through handoffs. One pass, idempotent.
func (d *Dispatcher) crew(ctx context.Context) error {
	if _, err := d.Store.ExpireMemories(ctx); err != nil {
		d.Log.Warn("expire memories", "err", err)
	}
	rs, err := d.Store.Crew(ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if err := d.member(ctx, r); err != nil {
			d.Log.Error("crew", "crew", r.Name, "err", err)
		}
	}
	return nil
}

func (d *Dispatcher) member(ctx context.Context, r store.CrewMember) error {
	runner := d.Runners[r.Runner]
	starter, ok := runner.(CrewRunner)
	if !ok {
		d.reportCrew(r.Name, "runner "+r.Runner+" cannot run crew members")
		return nil
	}
	if r.Live() {
		status, err := d.crewStatus(ctx, runner, r)
		if err != nil {
			return nil // runtime unreachable: unknown, leave it be
		}
		idle := status == "idle" || status == "done"
		d.crewSeen(ctx, r, status)
		grace := durationOr(r.HandoffGrace, defaultHandoffGrace)
		// A session that has lived MinSessionLife started for good, running
		// still or gone since: the starts that failed before it are over.
		lived, early := time.Duration(0), false
		if r.StartedAt != nil {
			lived = d.now().Sub(*r.StartedAt)
			early = lived < store.MinSessionLife
		}
		if r.Failures > 0 && !early {
			if err := d.Store.CrewSettled(ctx, r.Name); err != nil {
				return err
			}
			r.Failures = 0
		}
		switch {
		case !r.Enabled:
			return d.stopCrew(ctx, runner, r, "disabled")
		case status == "":
			// Nothing is closed here: the pane it leaves shows what the
			// harness printed before it exited, the only place the reason
			// can be read. The handle is kept instead, where the runtime
			// has something to close later (see closeLeft).
			gone := d.Store.CrewStopped
			if _, ok := runner.(CrewCloser); ok {
				gone = d.Store.CrewGone
				if len(r.Handle) > 0 {
					r.Left = append(r.Left, r.Handle)
				}
			}
			if err := gone(ctx, r.Name, "the session is gone"); err != nil {
				return err
			}
			r.State = "stopped"
			if early && r.RestartRequested == "" {
				// Its harness exits at once (a broken install, a bad flag, an
				// expired login): started again on this pass it would do the
				// same every few seconds, forever. An ending that was asked
				// for (a handoff, a rotation, a restart) is not one of these.
				return d.crewStartFailed(ctx, runner, r, nil, fmt.Errorf(
					"the session ended %s after its start (gone within %s counts as a failed start)",
					lived.Round(time.Second), store.MinSessionLife))
			}
		case r.RestartRequested != "" &&
			(idle || (r.RestartRequestedAt != nil && time.Since(*r.RestartRequestedAt) > grace)):
			if err := d.stopCrew(ctx, runner, r, "restart: "+r.RestartRequested); err != nil {
				return err
			}
			r.State = "stopped"
		case r.RestartRequested != "":
			return nil // let it finish its handoff turn
		default:
			return d.crewRunning(ctx, runner, r, status, grace)
		}
	}
	if !r.Enabled || r.State == "failed" {
		return nil
	}
	if r.RetryAfter != nil && d.now().Before(*r.RetryAfter) {
		return nil
	}
	if finder, ok := runner.(CrewFinder); ok {
		other, err := finder.LiveCrew(ctx, r)
		if err != nil {
			return d.crewStartFailed(ctx, runner, r, nil, err)
		}
		if other != nil {
			// A second session under one name: both would act as the
			// crew member and neither would know of the other. Nothing is
			// started and nothing counted as a failure, so the next pass
			// starts it once a human has ended the other.
			reason := fmt.Sprintf("agent %q in pane %s already is this crew member; end it and the crew member starts",
				other.Name, other.Pane)
			if d.status["crew:"+r.Name] != "blocked: "+reason {
				d.status["crew:"+r.Name] = "blocked: " + reason
				d.Log.Warn("crew member not started", "crew", r.Name, "reason", reason)
				_ = d.Store.LogDispatch(ctx, r.Journal, "crew_blocked", actor, map[string]any{
					"crew": r.Name, "reason": reason, "agent": other.Name, "pane": other.Pane})
			}
			return nil
		}
	}
	handle, err := starter.StartCrew(ctx, r)
	if err != nil {
		return d.crewStartFailed(ctx, runner, r, handle, err)
	}
	d.Log.Info("crew member started", "crew", r.Name)
	delete(d.status, "crew:"+r.Name)
	delete(d.promptIdle, "crew:"+r.Name)
	delete(d.idleSince, "crew:"+r.Name)
	delete(d.drafts, "crew:"+r.Name)
	delete(d.dialogs, "crew:"+r.Name)
	delete(d.logins, "crew:"+r.Name)
	delete(d.nudged, "crew:"+r.Name)
	delete(d.unsent, "crew:"+r.Name)
	if err := d.Store.CrewStarted(ctx, r.Name, handle); err != nil {
		return err
	}
	return d.closeLeft(ctx, runner, r)
}

// closeLeft closes what r's sessions that were gone left open in the
// runtime, all but the latest: that one still shows why the last session
// that died did. It runs when a session has started, so a crew member that is
// given up keeps at most two, and one that is started again goes back to
// one. Only handles yardr recorded are closed, never what merely carries
// the crew member's label; one the runner keeps or cannot close stays listed
// for the next start.
func (d *Dispatcher) closeLeft(ctx context.Context, runner Runner, r store.CrewMember) error {
	closer, ok := runner.(CrewCloser)
	if !ok || len(r.Left) < 2 {
		return nil
	}
	latest := len(r.Left) - 1
	left := make([]map[string]any, 0, 1)
	for _, h := range r.Left[:latest] {
		kept, err := closer.CloseLeft(ctx, r, h)
		if err != nil {
			d.Log.Warn("crew: closing what a gone session left", "crew", r.Name, "err", err)
			kept = true
		}
		if kept {
			left = append(left, h)
		}
	}
	left = append(left, r.Left[latest])
	if len(left) == len(r.Left) {
		return nil
	}
	return d.Store.CrewLeft(ctx, r.Name, left)
}

// crewSeen records what the runtime says r's session is doing, so that
// who looks at the crew (yardr chat) reads it from the store and its
// event stream and asks no runtime. The store writes a change only. A
// session that is gone is recorded by CrewStopped, not here.
func (d *Dispatcher) crewSeen(ctx context.Context, r store.CrewMember, status string) {
	switch status {
	case "":
		return
	case "done":
		status = "idle"
	case "working", "idle", "blocked":
	default:
		status = "unknown"
	}
	if status == r.Status {
		return
	}
	if err := d.Store.CrewStatus(ctx, r.Name, status); err != nil {
		d.Log.Warn("crew member status", "crew", r.Name, "err", err)
	}
}

// crewStartFailed counts a start that did not happen, or whose session
// was gone at once, against the crew member and backs it off; handle is what
// the runner left behind.
func (d *Dispatcher) crewStartFailed(ctx context.Context, runner Runner, r store.CrewMember, handle map[string]any, err error) error {
	backoff := min(30*time.Second<<min(r.Failures, 5), 10*time.Minute)
	if r.Failures+1 >= store.MaxStartFails {
		d.Log.Error("crew member given up", "crew", r.Name, "err", err, "failures", r.Failures+1)
	} else {
		d.Log.Warn("crew start failed", "crew", r.Name, "err", err, "failures", r.Failures+1, "retry_in", backoff)
	}
	if IsBlocked(err) && d.status["crew:"+r.Name] != "blocked: "+err.Error() {
		// Its configuration refuses it (an env_file missing or open to
		// others): a human has to act, so say so, once per reason.
		d.status["crew:"+r.Name] = "blocked: " + err.Error()
		_ = d.Store.LogDispatch(ctx, r.Journal, "crew_blocked", actor, map[string]any{
			"crew": r.Name, "reason": err.Error()})
	}
	if len(handle) > 0 {
		_ = runner.Stop(ctx, handle)
	}
	return d.Store.CrewStartFailed(ctx, r.Name, err.Error(), backoff)
}

// crewRunning handles a healthy session: a first prompt still to
// deliver, text of yardr's own left in its input, then rotation, then the
// inbox.
func (d *Dispatcher) crewRunning(ctx context.Context, runner Runner, r store.CrewMember, status string, grace time.Duration) error {
	idle := status == "idle" || status == "done"
	if p, ok := r.Handle[store.PendingPrompt].(string); ok {
		pass := d.pendingPrompt(ctx, runner, "crew:"+r.Name, r.Handle, p, status)
		if pass.handle != nil {
			if err := d.Store.SetCrewHandle(ctx, r.Name, pass.handle); err != nil {
				return err
			}
		}
		d.promptEvents(ctx, r.Journal, map[string]any{"crew": r.Name}, r.Handle, p, pass)
		if pass.arrived {
			// An agent working on its first prompt does not wait for a human.
			delete(d.status, "crew:"+r.Name)
			return nil
		}
		if !idle {
			// At its login screen it waits on no human answer but a login:
			// that is said instead, from the login's own memory.
			if at, why := d.loginAt(ctx, runner, "crew:"+r.Name, r.Handle); at {
				if line := d.logins["crew:"+r.Name]; line != "" {
					d.markCrewLogin(ctx, &r, line)
				}
				if why.tells() {
					d.nudgeHeld(ctx, r.Journal, map[string]any{"crew": r.Name}, r.Handle, "the first prompt", why)
				}
				return nil
			}
			// Waiting on a human (e.g. a trust prompt): say so once.
			if d.status["crew:"+r.Name] != "waiting" {
				d.status["crew:"+r.Name] = "waiting"
				d.Log.Warn("crew member waiting for a human before it can start", "crew", r.Name)
				_ = d.Store.LogDispatch(ctx, r.Journal, "crew_blocked", actor, map[string]any{
					"crew": r.Name, "reason": "not ready at startup; answer it in its pane"})
			}
		}
		return nil
	}
	d.crewPromptTaken(ctx, runner, &r, status)
	if d.ownInput(ctx, runner, "crew:"+r.Name, r.Journal, map[string]any{"crew": r.Name}, r.Handle, status) {
		return nil // Enter went into its pane: nothing else does on this pass
	}
	if maxAge := durationOr(r.MaxAge, 0); maxAge > 0 && r.StartedAt != nil && time.Since(*r.StartedAt) > maxAge {
		switch {
		case r.RotationAskedAt == nil && idle:
			typed, err := d.nudge(ctx, runner, &r, fmt.Sprintf(
				"Your session is due for rotation. Write your handoff now: yardr crew handoff %s \"<what you are doing, what is next, what to watch>\". "+
					"yardr then starts your next session from it.", r.Name))
			if err != nil || !typed {
				return err // not asked yet: the grace runs from the asking
			}
			return d.Store.MarkRotationAsked(ctx, r.Name)
		case r.RotationAskedAt != nil && time.Since(*r.RotationAskedAt) > grace:
			return d.Store.RequestRestart(ctx, r.Name, "rotation (no handoff within grace)", "", "yard")
		}
	}
	if !idle {
		delete(d.drafts, "crew:"+r.Name) // what was in its input was sent
		delete(d.dialogs, "crew:"+r.Name)
		if status == "working" {
			// Working: past any login screen seen before. A login
			// screen may read as blocked, so that status keeps the mark.
			d.markCrewLogin(ctx, &r, "")
		}
		d.crewHarnessOver(ctx, &r, false)
		return nil
	}
	page, err := d.Store.Inbox(ctx, r.Name, 5)
	// Idle with something unread is where a harness stopped on an error
	// sits (harness.go), nudged already or not.
	if err == nil && page.Total > 0 {
		if prompted, err := d.crewReset(ctx, runner, &r, page.Total, page.Last); prompted || err != nil {
			return err
		}
		d.crewHarnessError(ctx, runner, &r)
	} else if err == nil {
		d.crewHarnessOver(ctx, &r, true)
	}
	if err != nil || page.Total == 0 || page.Last <= r.Notified {
		// Nothing to type: text in the input holds nothing back, and a
		// wait on it is over.
		delete(d.drafts, "crew:"+r.Name)
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
		files = append(files, store.MailFiles(e)...)
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
	msg := fmt.Sprintf("yardr: %d new in your inbox (%s%s). Run yardr inbox to read them, and yardr inbox --ack when handled.",
		page.Total, strings.Join(kinds, ", "), more)
	if len(outside) > 0 {
		msg += fmt.Sprintf(" Mail from %s is %s.", strings.Join(outside, ", "), store.OutsideMail)
	}
	if len(trusted) > 0 {
		msg += fmt.Sprintf(" Mail from %s is %s: the operator trusts the name as them.", strings.Join(trusted, ", "), store.TrustedMail)
	}
	// The images the mail carries, by their paths: what a crew member's
	// harness reads a file by.
	if line := store.FilesLine(d.Home, files); line != "" {
		msg += " " + line
	}
	if slices.Contains(kinds, "mail") || slices.ContainsFunc(kinds, func(k string) bool {
		return strings.HasPrefix(k, "mail from ")
	}) {
		msg += " Answer mail with yardr reply <seq>, the number its inbox line starts with."
	}
	// The message is made on the pass that types it: one held back by text
	// in the input (see inputHeld) counts the inbox anew when its turn comes.
	typed, err := d.nudge(ctx, runner, &r, msg)
	if err != nil || !typed {
		return err
	}
	d.crewHarnessLook(r)
	return d.Store.MarkNotified(ctx, r.Name, page.Last)
}

func (d *Dispatcher) crewStatus(ctx context.Context, runner Runner, r store.CrewMember) (string, error) {
	if st, ok := runner.(Statuser); ok {
		return st.Status(ctx, r.Handle)
	}
	alive, err := runner.Alive(ctx, r.Handle)
	if err != nil || !alive {
		return "", err
	}
	return "idle", nil
}

// nudge types text into r's session as its next prompt. typed is false
// when it did not, because the input holds text (see inputHeld): the nudge
// is then still due, and the caller records nothing as said. So it is while
// a prompt given at a usage limit's reset has not shown. A runner that
// cannot nudge has nothing to wait for.
func (d *Dispatcher) nudge(ctx context.Context, runner Runner, r *store.CrewMember, text string) (typed bool, err error) {
	n, ok := runner.(Nudger)
	if !ok {
		return true, nil
	}
	if r.Handle[harnessPrompted] != nil {
		// The prompt yardr gave at a usage limit's reset is not known to be
		// taken: the harness may be asking something first, and a nudge's
		// Enter would answer it (see crewPromptTaken).
		return false, nil
	}
	key := "crew:" + r.Name
	if held, why := d.inputHeld(ctx, runner, key, r.Handle); held {
		// At the login screen, told or not: inputHeld looks for it
		// first, so the memory is this pass's.
		if line := d.logins[key]; line != "" {
			d.markCrewLogin(ctx, r, line)
		}
		if why.tells() {
			d.nudgeHeld(ctx, r.Journal, map[string]any{"crew": r.Name}, r.Handle, "a nudge", why)
		}
		return false, nil
	}
	stalled, err := typeText(ctx, n, r.Handle, text)
	if errors.Is(err, ErrAgentBlocked) {
		// At a dialog a nudge's Enter would answer: held like one behind
		// someone's text, and said once for as long as nothing is typed.
		if d.status[key] != nudgeAtDialog {
			d.status[key] = nudgeAtDialog
			data := map[string]any{"crew": r.Name, "reason": "the agent is at a dialog, so a nudge is not typed there: " +
				"its Enter would answer the dialog. It is typed once the dialog is gone. Answer it in its pane"}
			if pane, _ := r.Handle["pane"].(string); pane != "" {
				data["pane"] = pane
			}
			d.Log.Warn("a dialog in the agent's pane holds back a nudge", "crew", r.Name)
			_ = d.Store.LogDispatch(ctx, r.Journal, "nudge_held", actor, data)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Typed: past any login screen seen before. A look that failed says
	// nothing either way; the next look that finds it marks it again.
	d.markCrewLogin(ctx, r, "")
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
		d.ownInput(ctx, runner, key, r.Journal, map[string]any{"crew": r.Name}, r.Handle, "idle")
		// Or the harness answered the nudge with a question of its own and
		// keeps it until that is answered (a dialog of its file, harnesses/).
		// The nudge is typed and counts as said, so no later pass has
		// anything to type that would find the dialog: it is told of here.
		if _, why := d.dialogAt(ctx, runner, key, r.Handle); why.tells() {
			d.nudgeHeld(ctx, r.Journal, map[string]any{"crew": r.Name}, r.Handle, "the nudge yardr typed", why)
		}
	}
	return true, nil
}

// nudgeAtDialog is a crew member's d.status while a nudge for it is held
// back by a dialog its agent is at (see nudge).
const nudgeAtDialog = "nudge held: at a dialog"

func (d *Dispatcher) stopCrew(ctx context.Context, runner Runner, r store.CrewMember, reason string) error {
	if len(r.Handle) > 0 {
		if err := runner.Stop(ctx, r.Handle); err != nil {
			d.Log.Warn("crew stop", "crew", r.Name, "err", err)
		}
	}
	d.Log.Info("crew member stopped", "crew", r.Name, "reason", reason)
	return d.Store.CrewStopped(ctx, r.Name, reason)
}

func (d *Dispatcher) reportCrew(name, msg string) {
	key := "crew member|" + name
	if d.reported[key] == msg {
		return
	}
	d.reported[key] = msg
	d.Log.Warn("crew", "crew", name, "problem", msg)
}

func durationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

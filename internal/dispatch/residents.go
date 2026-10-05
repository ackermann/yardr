package dispatch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// ResidentRunner starts always-on sessions that are not tied to a bead.
type ResidentRunner interface {
	StartResident(ctx context.Context, r store.Resident) (handle map[string]any, err error)
}

// LiveAgent is an agent a runtime lists: its name there and the pane it
// runs in.
type LiveAgent struct{ Name, Pane string }

// ResidentFinder is a ResidentRunner whose runtime lists its agents, so it
// can tell when a session yardr did not start (a pane opened by hand under
// the resident's name) already is a resident. One without an agent list
// does not implement it, which reads as "none".
type ResidentFinder interface {
	// LiveResident is a live agent that already is r without being the
	// session r.Handle names, nil when there is none.
	LiveResident(ctx context.Context, r store.Resident) (*LiveAgent, error)
}

// Nudger delivers a short message to a running session (an idle agent
// reads it as its next prompt).
type Nudger interface {
	Nudge(ctx context.Context, handle map[string]any, text string) error
}

const defaultHandoffGrace = 15 * time.Minute

// residents keeps every enabled resident running, delivers its inbox when
// it is idle, and rotates it through handoffs. One pass, idempotent.
func (d *Dispatcher) residents(ctx context.Context) error {
	if _, err := d.Store.ExpireMemories(ctx); err != nil {
		d.Log.Warn("expire memories", "err", err)
	}
	rs, err := d.Store.Residents(ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if err := d.resident(ctx, r); err != nil {
			d.Log.Error("resident", "resident", r.Name, "err", err)
		}
	}
	return nil
}

func (d *Dispatcher) resident(ctx context.Context, r store.Resident) error {
	runner := d.Runners[r.Runner]
	starter, ok := runner.(ResidentRunner)
	if !ok {
		d.reportResident(r.Name, "runner "+r.Runner+" cannot run residents")
		return nil
	}
	if r.Live() {
		status, err := d.residentStatus(ctx, runner, r)
		if err != nil {
			return nil // runtime unreachable: unknown, leave it be
		}
		idle := status == "idle" || status == "done"
		d.residentSeen(ctx, r, status)
		grace := durationOr(r.HandoffGrace, defaultHandoffGrace)
		// A session that has lived MinSessionLife started for good, running
		// still or gone since: the starts that failed before it are over.
		lived, early := time.Duration(0), false
		if r.StartedAt != nil {
			lived = d.now().Sub(*r.StartedAt)
			early = lived < store.MinSessionLife
		}
		if r.Failures > 0 && !early {
			if err := d.Store.ResidentSettled(ctx, r.Name); err != nil {
				return err
			}
			r.Failures = 0
		}
		switch {
		case !r.Enabled:
			return d.stopResident(ctx, runner, r, "disabled")
		case status == "":
			if err := d.Store.ResidentStopped(ctx, r.Name, "the session is gone"); err != nil {
				return err
			}
			r.State = "stopped"
			if early && r.RestartRequested == "" {
				// Its harness exits at once (a broken install, a bad flag, an
				// expired login): started again on this pass it would do the
				// same every few seconds, forever. An ending that was asked
				// for (a handoff, a rotation, a restart) is not one of these.
				return d.residentStartFailed(ctx, runner, r, nil, fmt.Errorf(
					"the session ended %s after its start (gone within %s counts as a failed start)",
					lived.Round(time.Second), store.MinSessionLife))
			}
		case r.RestartRequested != "" &&
			(idle || (r.RestartRequestedAt != nil && time.Since(*r.RestartRequestedAt) > grace)):
			if err := d.stopResident(ctx, runner, r, "restart: "+r.RestartRequested); err != nil {
				return err
			}
			r.State = "stopped"
		case r.RestartRequested != "":
			return nil // let it finish its handoff turn
		default:
			return d.residentRunning(ctx, runner, r, idle, grace)
		}
	}
	if !r.Enabled || r.State == "failed" {
		return nil
	}
	if r.RetryAfter != nil && d.now().Before(*r.RetryAfter) {
		return nil
	}
	if finder, ok := runner.(ResidentFinder); ok {
		other, err := finder.LiveResident(ctx, r)
		if err != nil {
			return d.residentStartFailed(ctx, runner, r, nil, err)
		}
		if other != nil {
			// A second session under one name: both would act as the
			// resident and neither would know of the other. Nothing is
			// started and nothing counted as a failure, so the next pass
			// starts it once a human has ended the other.
			reason := fmt.Sprintf("agent %q in pane %s already is this resident; end it and the resident starts",
				other.Name, other.Pane)
			if d.status["resident:"+r.Name] != "blocked: "+reason {
				d.status["resident:"+r.Name] = "blocked: " + reason
				d.Log.Warn("resident not started", "resident", r.Name, "reason", reason)
				_ = d.Store.LogDispatch(ctx, r.Journal, "resident_blocked", actor, map[string]any{
					"resident": r.Name, "reason": reason, "agent": other.Name, "pane": other.Pane})
			}
			return nil
		}
	}
	handle, err := starter.StartResident(ctx, r)
	if err != nil {
		return d.residentStartFailed(ctx, runner, r, handle, err)
	}
	d.Log.Info("resident started", "resident", r.Name)
	delete(d.status, "resident:"+r.Name)
	return d.Store.ResidentStarted(ctx, r.Name, handle)
}

// residentSeen records what the runtime says r's session is doing, so that
// who looks at the residents (yardr chat) reads it from the store and its
// event stream and asks no runtime. The store writes a change only. A
// session that is gone is recorded by ResidentStopped, not here.
func (d *Dispatcher) residentSeen(ctx context.Context, r store.Resident, status string) {
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
	if err := d.Store.ResidentStatus(ctx, r.Name, status); err != nil {
		d.Log.Warn("resident status", "resident", r.Name, "err", err)
	}
}

// residentStartFailed counts a start that did not happen, or whose session
// was gone at once, against the resident and backs it off; handle is what
// the runner left behind.
func (d *Dispatcher) residentStartFailed(ctx context.Context, runner Runner, r store.Resident, handle map[string]any, err error) error {
	backoff := min(30*time.Second<<min(r.Failures, 5), 10*time.Minute)
	if r.Failures+1 >= store.MaxStartFails {
		d.Log.Error("resident given up", "resident", r.Name, "err", err, "failures", r.Failures+1)
	} else {
		d.Log.Warn("resident start failed", "resident", r.Name, "err", err, "failures", r.Failures+1, "retry_in", backoff)
	}
	if IsBlocked(err) && d.status["resident:"+r.Name] != "blocked: "+err.Error() {
		// Its configuration refuses it (an env_file missing or open to
		// others): a human has to act, so say so, once per reason.
		d.status["resident:"+r.Name] = "blocked: " + err.Error()
		_ = d.Store.LogDispatch(ctx, r.Journal, "resident_blocked", actor, map[string]any{
			"resident": r.Name, "reason": err.Error()})
	}
	if len(handle) > 0 {
		_ = runner.Stop(ctx, handle)
	}
	return d.Store.ResidentStartFailed(ctx, r.Name, err.Error(), backoff)
}

// residentRunning handles a healthy session: a first prompt still to
// deliver, then rotation, then the inbox.
func (d *Dispatcher) residentRunning(ctx context.Context, runner Runner, r store.Resident, idle bool, grace time.Duration) error {
	if p, ok := r.Handle[PendingPrompt].(string); ok {
		if promptShown(ctx, runner, r.Handle, p) {
			// Sent at the start and taken since: not a second time, and
			// an agent working on it does not wait for a human.
			delete(d.status, "resident:"+r.Name)
			return d.Store.SetResidentHandle(ctx, r.Name, withoutPending(r.Handle))
		}
		if !idle {
			// Waiting on a human (e.g. a trust prompt): say so once.
			if d.status["resident:"+r.Name] != "waiting" {
				d.status["resident:"+r.Name] = "waiting"
				d.Log.Warn("resident waiting for a human before it can start", "resident", r.Name)
				_ = d.Store.LogDispatch(ctx, r.Journal, "resident_blocked", actor, map[string]any{
					"resident": r.Name, "reason": "not ready at startup; answer it in its pane"})
			}
			return nil
		}
		if err := d.nudge(ctx, runner, r, p); err != nil {
			return nil // try again next pass
		}
		delete(d.status, "resident:"+r.Name)
		return d.Store.SetResidentHandle(ctx, r.Name, withoutPending(r.Handle))
	}
	if maxAge := durationOr(r.MaxAge, 0); maxAge > 0 && r.StartedAt != nil && time.Since(*r.StartedAt) > maxAge {
		switch {
		case r.RotationAskedAt == nil && idle:
			if err := d.nudge(ctx, runner, r, fmt.Sprintf(
				"Your session is due for rotation. Write your handoff now: yardr resident handoff %s \"<what you are doing, what is next, what to watch>\". "+
					"yardr then starts your next session from it.", r.Name)); err != nil {
				return err
			}
			return d.Store.MarkRotationAsked(ctx, r.Name)
		case r.RotationAskedAt != nil && time.Since(*r.RotationAskedAt) > grace:
			return d.Store.RequestRestart(ctx, r.Name, "rotation (no handoff within grace)", "", "yard")
		}
	}
	if !idle {
		return nil
	}
	page, err := d.Store.Inbox(ctx, r.Name, 5)
	if err != nil || page.Total == 0 || page.Last <= r.Notified {
		return err
	}
	// Mail from another yard is named with its sender, <name>@<peer>, and
	// what it is: the nudge is the first a resident reads of it.
	var kinds, outside []string
	for _, e := range page.Events {
		kind := e.Kind
		if peer, _ := e.Data["from_peer"].(string); e.Kind == "mail" && peer != "" {
			kind = "mail from " + e.Actor
			if !slices.Contains(outside, e.Actor) {
				outside = append(outside, e.Actor)
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
	if err := d.nudge(ctx, runner, r, msg); err != nil {
		return err
	}
	return d.Store.MarkNotified(ctx, r.Name, page.Last)
}

func (d *Dispatcher) residentStatus(ctx context.Context, runner Runner, r store.Resident) (string, error) {
	if st, ok := runner.(Statuser); ok {
		return st.Status(ctx, r.Handle)
	}
	alive, err := runner.Alive(ctx, r.Handle)
	if err != nil || !alive {
		return "", err
	}
	return "idle", nil
}

func (d *Dispatcher) nudge(ctx context.Context, runner Runner, r store.Resident, text string) error {
	n, ok := runner.(Nudger)
	if !ok {
		return nil
	}
	return n.Nudge(ctx, r.Handle, text)
}

func (d *Dispatcher) stopResident(ctx context.Context, runner Runner, r store.Resident, reason string) error {
	if len(r.Handle) > 0 {
		if err := runner.Stop(ctx, r.Handle); err != nil {
			d.Log.Warn("resident stop", "resident", r.Name, "err", err)
		}
	}
	d.Log.Info("resident stopped", "resident", r.Name, "reason", reason)
	return d.Store.ResidentStopped(ctx, r.Name, reason)
}

func (d *Dispatcher) reportResident(name, msg string) {
	key := "resident|" + name
	if d.reported[key] == msg {
		return
	}
	d.reported[key] = msg
	d.Log.Warn("resident", "resident", name, "problem", msg)
}

func durationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

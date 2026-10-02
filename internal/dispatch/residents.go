package dispatch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mckean/yard/internal/store"
)

// ResidentRunner starts always-on sessions that are not tied to a bead.
type ResidentRunner interface {
	StartResident(ctx context.Context, r store.Resident) (handle map[string]any, err error)
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
		grace := durationOr(r.HandoffGrace, defaultHandoffGrace)
		switch {
		case !r.Enabled:
			return d.stopResident(ctx, runner, r, "disabled")
		case status == "":
			if err := d.Store.ResidentStopped(ctx, r.Name, "the session is gone"); err != nil {
				return err
			}
			r.State = "stopped"
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
	if r.RetryAfter != nil && time.Now().Before(*r.RetryAfter) {
		return nil
	}
	handle, err := starter.StartResident(ctx, r)
	if err != nil {
		backoff := min(30*time.Second<<min(r.Failures, 5), 10*time.Minute)
		d.Log.Warn("resident start failed", "resident", r.Name, "err", err, "retry_in", backoff)
		if len(handle) > 0 {
			_ = runner.Stop(ctx, handle)
		}
		return d.Store.ResidentStartFailed(ctx, r.Name, err.Error(), backoff)
	}
	d.Log.Info("resident started", "resident", r.Name)
	return d.Store.ResidentStarted(ctx, r.Name, handle)
}

// residentRunning handles a healthy session: a first prompt still to
// deliver, then rotation, then the inbox.
func (d *Dispatcher) residentRunning(ctx context.Context, runner Runner, r store.Resident, idle bool, grace time.Duration) error {
	if p, ok := r.Handle[PendingPrompt].(string); ok {
		if !idle {
			// Waiting on a human (e.g. a trust prompt): say so once.
			if d.status["resident:"+r.Name] != "waiting" {
				d.status["resident:"+r.Name] = "waiting"
				d.Log.Warn("resident waiting for a human before it can start", "resident", r.Name)
				_ = d.Store.LogEvent(ctx, r.Journal, "resident_blocked", actor, map[string]any{
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
				"Your session is due for rotation. Write your handoff now: yard resident handoff %s \"<what you are doing, what is next, what to watch>\". "+
					"yard then starts your next session from it.", r.Name)); err != nil {
				return err
			}
			return d.Store.MarkRotationAsked(ctx, r.Name)
		case r.RotationAskedAt != nil && time.Since(*r.RotationAskedAt) > grace:
			return d.Store.RequestRestart(ctx, r.Name, "rotation (no handoff within grace)", "yard")
		}
	}
	if !idle {
		return nil
	}
	page, err := d.Store.Inbox(ctx, r.Name, 5)
	if err != nil || page.Total == 0 || page.Last <= r.Notified {
		return err
	}
	var kinds []string
	for _, e := range page.Events {
		if !slices.Contains(kinds, e.Kind) {
			kinds = append(kinds, e.Kind)
		}
	}
	more := ""
	if page.Total > len(page.Events) {
		more = ", …"
	}
	msg := fmt.Sprintf("yard: %d new in your inbox (%s%s). Run yard inbox to read them, and yard inbox --ack when handled.",
		page.Total, strings.Join(kinds, ", "), more)
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

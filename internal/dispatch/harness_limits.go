package dispatch

import (
	"context"
	"fmt"
	"time"

	"yardr.dev/yardr/internal/limits"
)

// A harness that stopped on a usage limit says so, and seldom says which
// window or until when in a line the yard can read: on 6 Oct 2026 the
// yardmaster guessed 02:13 for a crew member whose window opened at 00:02
// (yardr-m71s). A source of the yard that says limits knows (see
// internal/limits). So a session_harness_error of reason rate_limited, a
// session's or a crew member's, carries what that source says of the
// harness's kind: the report, and the window the agent waits for with when
// it resets.
//
// The source's command is slow, may be bounded in how often it answers and
// may renew a stored login as it reads one, so it is not leaned on. The
// dispatcher runs it in one place, harnessLimits, which is reached only
// while such an event is being raised: once per idle spell of a session
// and once per standing error of a crew member, never by a pass that finds
// nothing stopped on a usage limit, by a start or by a timer. And what it
// said is remembered for limitsMemory, so agents that stop together are
// one run.
//
// It is run inside the pass, as a crew member's env_command is: a command
// that hangs holds one pass for limits.Timeout, once in limitsMemory.

// limitsMemory is how long what a provider said is kept, in the memory of
// this daemon only: five sessions of one kind that stop in the same minute
// are one ask. An answer is the provider's, not the kind's, so the memory
// is by the name the source's file gives the provider.
const limitsMemory = 60 * time.Second

// limitsAnswer is what a provider said when it was last asked, or why it
// gave no answer: a provider that fails is not asked five times either.
type limitsAnswer struct {
	at     time.Time
	report *limits.Report
	err    error
}

// askLimits is what the yard's source says of kind's subscription: from
// memory when the provider was asked within limitsMemory, else by one run
// of its command. The error is a *limits.NoSource when no source lists the
// kind, and nothing was run.
func (d *Dispatcher) askLimits(ctx context.Context, kind string) (*limits.Report, error) {
	as, err := limits.Answerers(ctx, d.Store, d.Home)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		name, ok := a.Limits.Kinds[kind]
		if !ok {
			continue
		}
		key, now := a.Source+"\x00"+name, d.now()
		if m, ok := d.limitsAsked[key]; ok && now.Sub(m.at) < limitsMemory {
			return m.report, m.err
		}
		r, err := a.Ask(ctx, kind)
		if d.limitsAsked == nil {
			d.limitsAsked = map[string]limitsAnswer{}
		}
		d.limitsAsked[key] = limitsAnswer{at: now, report: r, err: err}
		if err != nil {
			d.Log.Warn("limits of a harness kind not known", "kind", kind, "source", a.Source, "err", err)
		}
		return r, err
	}
	return nil, &limits.NoSource{Kind: kind}
}

// harnessLimits adds to data, a session_harness_error of reason
// rate_limited for an agent of kind, what the yard's source says of the
// kind's limits: the report (`limits`) and, when a window is used up, the
// one the agent waits for (`window`) with when it resets (`resets_at`, when
// the provider said). It returns that in words, to begin the event's hint.
//
// With no source for the kind, or a provider that gave no answer, the event
// is as it was, with why nothing is known in `limits_unknown`, and "" is
// returned.
func (d *Dispatcher) harnessLimits(ctx context.Context, kind string, data map[string]any) string {
	r, err := d.askLimits(ctx, kind)
	if err != nil {
		if ctx.Err() != nil {
			return "" // the daemon is stopping: the provider was not heard out
		}
		data["limits_unknown"] = err.Error()
		return ""
	}
	data["limits"] = r
	w := r.UsedUp()
	if w == nil {
		// The provider reads the operator's login and knows no model: the
		// limit the harness hit is one it does not report.
		return fmt.Sprintf("no window of %s's subscription is used up, says %s: the limit is another account's, "+
			"one model's or a short rate limit; ", kind, r.Source)
	}
	data["window"] = w.Label
	if w.ResetsAt == nil {
		return fmt.Sprintf("%s's %s window is used up, and %s does not say when it resets; ", kind, w.Label, r.Source)
	}
	data["resets_at"] = w.ResetsAt.UTC().Format(time.RFC3339)
	now := d.now()
	if left := w.ResetsAt.Sub(now); left >= 0 {
		return fmt.Sprintf("%s's %s window is used up and resets at %s (in %s); ", kind, w.Label, limits.Clock(*w.ResetsAt, now), limits.Span(left))
	}
	// The provider's word, kept: it has not seen the window open yet.
	return fmt.Sprintf("%s's %s window is used up and was to reset at %s (%s ago); ", kind, w.Label,
		limits.Clock(*w.ResetsAt, now), limits.Span(now.Sub(*w.ResetsAt)))
}

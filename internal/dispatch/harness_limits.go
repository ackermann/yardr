package dispatch

import (
	"context"
	"fmt"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
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
// dispatcher runs it in three places. harnessLimits is reached only while
// such an event is being raised: once per idle spell of a session and once
// per standing error of a crew member. windowOpen (harness_reset.go) is
// reached only when the reset such an event told of has come and the agent
// is about to be prompted. Neither is reached by a pass that finds nothing
// stopped on a usage limit, or by a timer. And what it said is remembered
// for limitsMemory, so agents that stop or wake together are one run.
// routeQuota (quota.go) is reached only for a ready bead whose route puts a
// quota condition on a group, and reads the same memory with the longer
// age routeLimitsMemory: a start is no emergency, and beads become ready
// far more often than a harness stops.
//
// It is run inside the pass, as a crew member's env_command is: a command
// that hangs holds one pass for limits.Timeout, once in limitsMemory.

// limitsMemory is how long what a provider said is kept, in the memory of
// this daemon only: five sessions of one kind that stop in the same minute
// are one ask. An answer is the provider's, not the kind's, read under one
// login, so the memory is by the name the source's file gives the provider
// and whose login was read.
const limitsMemory = 60 * time.Second

// limitsAnswer is what a provider said when it was last asked, or why it
// gave no answer: a provider that fails is not asked five times either.
type limitsAnswer struct {
	at     time.Time
	report *limits.Report
	err    error
}

// groupEnv is the env_file entries of g, the environment its panes burn
// under, for a limits read of its login: nil where it names none, the
// machine's login being its login too. An error names why the file cannot
// be read; the read then knows nothing, as for a provider that fails.
func groupEnv(g store.Group) ([]string, error) {
	file, _ := g.Config["env_file"].(string)
	if file == "" {
		return nil, nil
	}
	f, err := config.ReadEnvFile(file)
	if err != nil {
		return nil, fmt.Errorf("group %s: %w", g.Name, err)
	}
	return f.Env, nil
}

// askLimits is what the yard's source says of the subscription an agent of
// kind with the provider setting burns, read under env: from memory when
// the provider was asked for scope within age (limitsMemory, or
// routeLimitsMemory for a route's condition), else by one run of its
// command. scope names whose login is read ("group builders", "" for the
// machine's), so two logins never share an answer. With the
// setting, the first source by name whose kinds table names it is asked,
// even where no table lists the kind; without it, the kind resolves
// through the kinds table as before. The error is a *limits.NoSource
// naming the provider where no source names it, and nothing was run.
func (d *Dispatcher) askLimits(ctx context.Context, kind, provider string, age time.Duration, env []string, scope string) (*limits.Report, error) {
	as, err := limits.Answerers(ctx, d.Store, d.Home)
	if err != nil {
		return nil, err
	}
	a, name, ok := limits.Resolve(as, kind, provider)
	if !ok {
		if provider != "" {
			return nil, &limits.NoSource{Provider: provider}
		}
		return nil, &limits.NoSource{Kind: kind}
	}
	key, now := a.Source+"\x00"+name+"\x00"+scope, d.now()
	if m, ok := d.limitsAsked[key]; ok && now.Sub(m.at) < age {
		return m.report, m.err
	}
	a.Env = env
	var r *limits.Report
	if provider != "" {
		r, err = a.AskName(ctx, name)
	} else {
		r, err = a.Ask(ctx, kind)
	}
	if d.limitsAsked == nil {
		d.limitsAsked = map[string]limitsAnswer{}
	}
	d.limitsAsked[key] = limitsAnswer{at: now, report: r, err: err}
	if err != nil {
		d.Log.Warn("limits of a harness kind not known", "kind", kind, "source", a.Source, "err", err)
	}
	return r, err
}

// askSource is what the source named source says of itself: from memory
// when the source was asked within age, else by one run of its command.
// Only a source whose limits key names no kinds answers; the error is a
// *limits.NoSource naming the source where none does, and nothing was run.
// The memory is the providers' own: a source's key has no provider in it,
// and no provider's name is empty.
func (d *Dispatcher) askSource(ctx context.Context, source string, age time.Duration) (*limits.Report, error) {
	as, err := limits.Answerers(ctx, d.Store, d.Home)
	if err != nil {
		return nil, err
	}
	a, ok := limits.SelfAnswerer(as, source)
	if !ok {
		return nil, &limits.NoSource{Source: source}
	}
	key, now := a.Source+"\x00", d.now()
	if m, ok := d.limitsAsked[key]; ok && now.Sub(m.at) < age {
		return m.report, m.err
	}
	r, err := a.AskSelf(ctx)
	if d.limitsAsked == nil {
		d.limitsAsked = map[string]limitsAnswer{}
	}
	d.limitsAsked[key] = limitsAnswer{at: now, report: r, err: err}
	if err != nil {
		d.Log.Warn("limits of a source not known", "source", a.Source, "err", err)
	}
	return r, err
}

// harnessLimits adds to data, a session_harness_error of reason
// rate_limited for an agent of kind with the provider setting, what the
// yard's source says of the subscription's limits, read under env: the
// report (`limits`) and, when a window is used up, the one the agent waits
// for (`window`) with when it resets (`resets_at`, when the provider
// said). scope names whose login is read, as askLimits asks. It returns
// that in words, to begin the event's hint, and the window, nil for none.
//
// With no source for the subscription, or a provider that gave no answer,
// the event is as it was, with why nothing is known in `limits_unknown`,
// and "" is returned.
func (d *Dispatcher) harnessLimits(ctx context.Context, kind, provider string, env []string, scope string, data map[string]any) (said string, w *limits.Window) {
	r, err := d.askLimits(ctx, kind, provider, limitsMemory, env, scope)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil // the daemon is stopping: the provider was not heard out
		}
		data["limits_unknown"] = err.Error()
		return "", nil
	}
	data["limits"] = r
	w = r.UsedUp()
	if w == nil {
		// The provider reads the login it was asked under and knows no
		// model: the limit the harness hit is one it does not report.
		return fmt.Sprintf("no window of %s's subscription is used up, says %s: the limit is another account's, "+
			"one model's or a short rate limit; ", kind, r.Source), nil
	}
	data["window"] = w.Label
	if w.ResetsAt == nil {
		return fmt.Sprintf("%s's %s window is used up, and %s does not say when it resets; ", kind, w.Label, r.Source), w
	}
	data["resets_at"] = w.ResetsAt.UTC().Format(time.RFC3339)
	now := d.now()
	if left := w.ResetsAt.Sub(now); left >= 0 {
		return fmt.Sprintf("%s's %s window is used up and resets at %s (in %s); ", kind, w.Label, limits.Clock(*w.ResetsAt, now), limits.Span(left)), w
	}
	// The provider's word, kept: it has not seen the window open yet.
	return fmt.Sprintf("%s's %s window is used up and was to reset at %s (%s ago); ", kind, w.Label,
		limits.Clock(*w.ResetsAt, now), limits.Span(now.Sub(*w.ResetsAt))), w
}

// sessionLimits is harnessLimits for a session of g: the subscription read
// under the group's env, so the event says what that login carries. A
// group whose env_file cannot be read knows nothing, as for a provider
// that gave no answer.
func (d *Dispatcher) sessionLimits(ctx context.Context, g store.Group, kind, provider string, data map[string]any) (string, *limits.Window) {
	env, err := groupEnv(g)
	if err != nil {
		data["limits_unknown"] = err.Error()
		return "", nil
	}
	return d.harnessLimits(ctx, kind, provider, env, "group "+g.Name, data)
}

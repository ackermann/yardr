package dispatch

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
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
// dispatcher reads it in three places, and runs it in none of them:
// harnessLimits is reached only while such an event is being raised, once
// per idle spell of a session and once per standing error of a crew
// member; windowOpen (harness_reset.go) only when the reset such an event
// told of has come and the agent is about to be prompted; routeQuota
// (quota.go) only for a ready bead whose route is gated (store.Route.Gated:
// it puts a quota condition on a group, or names a group that starts
// agents), with the longer age routeLimitsMemory, since a start is no
// emergency and beads become ready far more often than a harness stops.
// Every read is of the memory below, kept fresh by the background
// refresher (limits_refresh.go): a command that hangs holds no pass at
// all, and agents that stop or wake together are still one run. A route
// reads one thing of it past the age: the report the provider last gave
// (limitRead.last), whole while it is young and its used-up windows
// after that (unknownQuota), however many runs have failed since.

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
	// kept is the report the provider gave before the run that failed, so
	// that a window it said was used up is not forgotten with the failure
	// (limitRead.last). It is set only where report is not.
	kept *limits.Report
}

// known is the latest report the provider gave: this answer's, or for a
// failed run the one before it.
func (m limitsAnswer) known() *limits.Report {
	if m.report != nil {
		return m.report
	}
	return m.kept
}

// fresh is whether what was asked at m.at still holds now: for the caller's
// age, or sooner where the answer says how long it holds (Report.HoldsFor),
// whichever runs out first. An answer that says nothing, and a failure,
// hold for the whole age.
func fresh(m limitsAnswer, now time.Time, age time.Duration) bool {
	if m.report != nil && m.report.HoldsFor > 0 && m.report.HoldsFor < age {
		age = m.report.HoldsFor
	}
	return now.Sub(m.at) < age
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

// limitsEnvScope identifies the environment overlay whose login is read.
// Keep only its digest in the cache key: env_file entries can be secrets.
// Groups with the same overlay share one answer; changing a file's entries
// gives a new scope even while the old answer is still fresh.
func limitsEnvScope(env []string) string {
	if len(env) == 0 {
		return "ambient"
	}
	sum := sha256.Sum256([]byte(strings.Join(env, "\x00")))
	return fmt.Sprintf("env:%x", sum)
}

// limitState is which of the cache's answers a read found, which its
// readers must not confuse: a route fails open on the last two alike
// (but for what was last known, limitRead.last), a reset prompts
// on them and defers only on the middle one, and a stop writes
// limits_unknown for each.
type limitState int

const (
	// limitFresh is a fresh report (report set), or a fresh remembered
	// failure (err set): the provider was asked within the reader's age
	// and gave that answer.
	limitFresh limitState = iota
	// limitNone is no fresh answer: the key was never asked, or its
	// answer has gone stale and its refresh is due or already running.
	// The read has noted the key as wanted, so the refresher runs it.
	limitNone
	// limitUnresolvable is no answer and nothing to refresh: no source
	// names what was asked for, the store's sources could not be read,
	// or (at the caller) an env_file could not be read. There is no
	// key and no command, so no want is registered and no run starts.
	limitUnresolvable
)

// limitRead is one read of the cache: what it found, and in which state.
type limitRead struct {
	report *limits.Report
	err    error
	state  limitState
	// last is the latest report the provider gave when the read has none of
	// its own to go by: the answer that has gone stale, or the one before a
	// failed run. A route goes by it whole while it is young, and after
	// that by only a window it says was used up until a reset still ahead
	// (unknownQuota): that stays so however old the report is.
	last *limits.Report
}

// usedUpAhead is the windows of r that are used up and reset after now. A
// used-up window stays used up until it resets, whatever the provider says
// later, which no percentage below 100 does, and a window whose reset the
// provider did not say is not known to end at all. So these are the part of
// an answer that is still true when it is no longer fresh.
func usedUpAhead(r *limits.Report, now time.Time) []limits.Window {
	var out []limits.Window
	for _, w := range r.Windows {
		if w.UsedUp() && w.ResetsAt != nil && w.ResetsAt.After(now) {
			out = append(out, w)
		}
	}
	return out
}

// unknown is why nothing is known, for a limits_unknown or a route's
// Unknown: the remembered or resolving error, or, for no fresh answer,
// that there is none yet.
func (r limitRead) unknown() string {
	if r.err != nil {
		return r.err.Error()
	}
	return "no fresh answer yet: the limits command is being asked in the background"
}

// cachedLimits is what the yard's source says of the subscription an
// agent of kind with the provider setting burns, read under env, from
// the memory the background refresher keeps: never a run of the command
// and never a wait for one. Its whole cost is resolving the answerer
// (limits.Answerers over the store's sources, then Resolve or, with a
// provider setting, the first source by name whose kinds table names
// it, even where no table lists the kind) and the cache lookup under
// limitsMu. A key whose answer is missing or stale is noted as wanted
// at the caller's age before limitNone is returned.
func (d *Dispatcher) cachedLimits(ctx context.Context, kind, provider string, age time.Duration, env []string) limitRead {
	as, err := limits.Answerers(ctx, d.Store, d.Home)
	if err != nil {
		return limitRead{err: err, state: limitUnresolvable}
	}
	a, name, ok := limits.Resolve(as, kind, provider)
	if !ok {
		if provider != "" {
			return limitRead{err: &limits.NoSource{Provider: provider}, state: limitUnresolvable}
		}
		return limitRead{err: &limits.NoSource{Kind: kind}, state: limitUnresolvable}
	}
	key, now := a.Source+"\x00"+name+"\x00"+limitsEnvScope(env), d.now()
	a.Env = slices.Clone(env)
	want := limitWant{key: key, age: age, answerer: a, kind: kind, provider: provider, name: name}
	d.limitsMu.Lock()
	d.initLimitsLocked()
	m, asked := d.limitsAsked[key]
	d.limitsMu.Unlock()
	if asked && fresh(m, now, age) {
		d.renewLimitWant(want)
		return limitRead{report: m.report, err: m.err, state: limitFresh, last: m.kept}
	}
	d.registerLimitWant(want, false)
	return limitRead{state: limitNone, last: m.known()}
}

// cachedSource is what the source named source says of itself, read as
// cachedLimits reads a provider: from the refresher's memory, never a
// run. Only a source whose limits key names no kinds answers; the
// memory is the providers' own, whose keys name a provider beside the
// source, where a source's names none.
func (d *Dispatcher) cachedSource(ctx context.Context, source string, age time.Duration) limitRead {
	as, err := limits.Answerers(ctx, d.Store, d.Home)
	if err != nil {
		return limitRead{err: err, state: limitUnresolvable}
	}
	a, ok := limits.SelfAnswerer(as, source)
	if !ok {
		return limitRead{err: &limits.NoSource{Source: source}, state: limitUnresolvable}
	}
	key, now := a.Source+"\x00", d.now()
	want := limitWant{key: key, age: age, answerer: a, isSource: true}
	d.limitsMu.Lock()
	d.initLimitsLocked()
	m, asked := d.limitsAsked[key]
	d.limitsMu.Unlock()
	if asked && fresh(m, now, age) {
		d.renewLimitWant(want)
		return limitRead{report: m.report, err: m.err, state: limitFresh, last: m.kept}
	}
	d.registerLimitWant(want, false)
	return limitRead{state: limitNone, last: m.known()}
}

// harnessLimits adds to data, a session_harness_error of reason
// rate_limited for an agent of kind with the provider setting, what the
// yard's source says of the subscription's limits, read under env: the
// report (`limits`) and, when a window is used up, the one the agent waits
// for (`window`) with when it resets (`resets_at`, when the provider
// said). The agent's model is not known, so the window is UsedUpForStop's:
// one that counts for a single model only when no other is used up. scope
// names whose login is read, as cachedLimits reads. It returns that in words, to
// begin the event's hint, and the window, nil for none.
//
// With no source for the subscription, a provider that gave no answer,
// or no fresh answer yet (the daemon's first stop on a key, whose refresh
// is already running), the event is as it was, with why nothing is known
// in `limits_unknown`, and "" is returned: no window, no reset and no
// wake, and the answer that lands after amends nothing. The read still
// wants the key, so a later stop on the warmed key is told with its
// window, as the fresh case below is.
func (d *Dispatcher) harnessLimits(ctx context.Context, kind, provider string, env []string, data map[string]any) (said string, w *limits.Window) {
	rd := d.cachedLimits(ctx, kind, provider, limitsMemory, env)
	r := rd.report
	if r == nil {
		if ctx.Err() != nil {
			return "", nil // the daemon is stopping
		}
		data["limits_unknown"] = rd.unknown()
		return "", nil
	}
	data["limits"] = r
	w = r.UsedUpForStop()
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
	return d.harnessLimits(ctx, kind, provider, env, data)
}

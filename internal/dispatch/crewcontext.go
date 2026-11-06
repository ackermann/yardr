package dispatch

import (
	"context"
	"fmt"
	"strconv"
)

// ContextReader is implemented by runners that read the context a
// session's agent holds, in tokens (herdr reads the agent's own session
// file). ok is false when the session's kind names no source, or its
// file says no use yet.
type ContextReader interface {
	Context(ctx context.Context, handle map[string]any) (used int, ok bool, err error)
}

// defaultContextHandoff is the percent of the context window at which an
// idle crew session is asked for a handoff when its member sets none.
const defaultContextHandoff = 60

// contextThreshold is the percent of the context window at which an idle
// crew session is asked for a handoff: the member's setting, the default
// when unset, 0 when the member never asks. What the store refused to
// save reads as the default.
func contextThreshold(s string) int {
	if s == "" {
		return defaultContextHandoff
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 0 && n <= 100 {
		return n
	}
	return defaultContextHandoff
}

// crewContext reads the session's context use on an idle pass and, past
// its threshold, asks once for a handoff. asked is true when the nudge
// went out on this pass. The ask is never forced: no grace, no restart,
// and it is asked again only in a new session. A session already asked
// for its age is not asked for its use too.
func (d *Dispatcher) crewContext(ctx context.Context, runner Runner, s *memberSession) (asked bool, err error) {
	def := s.member
	r, ok := runner.(ContextReader)
	if !ok {
		return false, nil
	}
	used, ok, err := r.Context(ctx, s.st.Handle)
	if err != nil || !ok {
		return false, err
	}
	model, _ := def.Config["model"].(string)
	window := ContextWindow(model)
	pct := used * 100 / window
	if err := d.Store.SetContextFor(ctx, def.Name, s.user, pct, window); err != nil {
		return false, err
	}
	threshold := contextThreshold(def.ContextHandoff)
	if threshold <= 0 || pct < threshold || s.st.ContextAskedAt != nil || s.st.RotationAskedAt != nil {
		return false, nil
	}
	typed, err := d.nudge(ctx, runner, s, fmt.Sprintf(
		"Your context is %d%% of the window. Write your handoff when this conversation allows: yardr crew handoff %s - with the words on stdin: what you are doing, what is next, what to watch.",
		pct, def.Name))
	if err != nil || !typed {
		return false, err // not asked yet: the reading above is still recorded
	}
	return true, d.Store.MarkContextAskedFor(ctx, def.Name, s.user, pct, window)
}

package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"yardr.dev/yardr/internal/agents"
)

// Handle keys the dispatcher writes and Usage reads, as harnessKind is one the
// runner writes: the session id of the agent, kept on the handle because herdr
// no longer knows the agent once its tab is closed; the entry that marks a
// stopped session as due for its count; and in that entry the time up to which
// the count reads, for a session whose agent was left running.
const (
	agentSession = "agent_session"
	countDue     = "count_due"
	countUntil   = "until"
)

// The reasons a session's count is missing, as the record keeps them and the
// route report prints them.
const (
	missingNoID     = "no session id"
	missingNoFile   = "no file"
	missingNoLine   = "no usage line"
	missingNoSource = "kind declares no usage"
)

// ErrUsageNotYet is what Usage answers while the first entry of a kind's usage
// finds no line: the agent may not have written its total yet (Claude Code
// writes its cost-state as the process exits, a moment after the tab closes).
// The caller asks again.
var ErrUsageNotYet = errors.New("the session file holds no usage line yet")

// Usage is what the agent of handle's session used, read from its own session
// file by the entries of its kind's usage (agents.UsageSource), once the
// session has ended. The kind is the handle's own (harnessKind) and the file is
// named by the session id the dispatcher kept on it (agent_session): herdr is
// not asked, since it no longer knows an agent whose tab is closed.
//
// The first entry is the harness's own total, and the ones after it are
// fallbacks that fall short of it. Without fallback only the first is tried,
// and a file with no line for it yet is ErrUsageNotYet, so the caller can
// wait: the total may be written a moment after the session ended. With
// fallback the first entry that finds a line is the reading, and a file that
// has none for any entry is missing "no usage line". partial is that entry's
// own: what it read falls short of what the session used.
//
// handle.count_due.until is set for a session whose agent was left running for
// inspection. Usage then counts only the lines written at or before it, so what
// a person does in the pane afterwards is not the bead's, however late the read
// is. An entry that names no time for its lines is left out, since it could
// hold work after the cutoff (Claude's cost-state is written when the process
// exits, however long after), and so is a line whose time does not read. A
// caller reads such a session once until has passed: lines at or before it may
// still be coming until then.
//
// err is for what the caller tries again on a later pass: the file could not
// be read, or ErrUsageNotYet. missing is for what it records: why no count is
// there and none will come. Otherwise u is the count.
func (h *Herdr) Usage(ctx context.Context, handle map[string]any, fallback bool) (u agents.Usage, partial bool, missing string, err error) {
	if err := ctx.Err(); err != nil {
		return agents.Usage{}, false, "", err
	}
	name, _ := handle[harnessKind].(string)
	kind, _ := agents.Lookup(name)
	if kind.Model == nil || len(kind.Usage) == 0 {
		return agents.Usage{}, false, missingNoSource, nil
	}
	id, _ := handle[agentSession].(string)
	if !plainSessionID(id) {
		return agents.Usage{}, false, missingNoID, nil
	}
	until, kept, err := countUntilIn(handle)
	if err != nil {
		return agents.Usage{}, false, "", err
	}
	entries := usageEntries(kind.Usage, kept, fallback)
	if len(entries) == 0 {
		return agents.Usage{}, false, missingNoLine, nil
	}
	path, err := sessionFile(kind.Model.Pattern(h.agentHome(kind), id))
	switch {
	case err != nil:
		return agents.Usage{}, false, "", err
	case path == "":
		return agents.Usage{}, false, missingNoFile, nil
	}
	// One pass over the file feeds every entry tried: a file is a few MB, and
	// a fallback needs no second read.
	tallies := make([]*agents.UsageTally, len(entries))
	for i, e := range entries {
		tallies[i] = e.Tally()
	}
	err = eachLine(path, func(line []byte) bool {
		for i, e := range entries {
			l, ok := e.UsageIn(line)
			if !ok || kept && (l.At.IsZero() || l.At.After(until)) {
				continue
			}
			tallies[i].Add(l)
		}
		return false
	})
	if err != nil {
		return agents.Usage{}, false, "", err
	}
	for i, t := range tallies {
		if got, ok := t.Usage(); ok {
			return got, entries[i].Partial, "", nil
		}
	}
	if !fallback {
		return agents.Usage{}, false, "", ErrUsageNotYet
	}
	return agents.Usage{}, false, missingNoLine, nil
}

// usageEntries are the entries of a kind's usage that Usage tries, in the order
// of its file: all of them with fallback, else the first. A count that stops at
// a time (kept) leaves out the entries that name none for their lines, and the
// first of those left is then the first.
func usageEntries(entries []agents.UsageSource, kept, fallback bool) []agents.UsageSource {
	if kept {
		entries = slices.DeleteFunc(slices.Clone(entries), func(e agents.UsageSource) bool { return e.At == "" })
	}
	if !fallback && len(entries) > 1 {
		entries = entries[:1]
	}
	return entries
}

// countUntilIn is the time handle's count_due says to count up to, and whether
// it says one. A time that does not read is an error and never no time, which
// would count what a person did in a kept pane afterwards.
func countUntilIn(handle map[string]any) (until time.Time, kept bool, err error) {
	due, _ := handle[countDue].(map[string]any)
	v, kept := due[countUntil]
	if !kept {
		return time.Time{}, false, nil
	}
	text, _ := v.(string)
	until, err = time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("%s of %s %q: %w", countUntil, countDue, text, err)
	}
	return until, true, nil
}

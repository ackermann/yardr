package cli

import (
	"slices"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// beadSessionTokens is one agent session's count on the bead's tokens
// line: what it used, or why no count is there (missing) or none yet
// (pending). Tokens is the headline number, fresh input, cache writes
// and output; Usage carries all five counts.
type beadSessionTokens struct {
	Session string      `json:"session"`
	Stage   string      `json:"stage"`
	Kind    string      `json:"kind"`
	Tokens  int         `json:"tokens"`
	Usage   store.Usage `json:"usage"`
	Partial bool        `json:"partial,omitempty"`
	Missing string      `json:"missing,omitempty"`
	Pending bool        `json:"pending,omitempty"`
}

// beadTokens is what the bead's agent sessions used, as bead show
// prints it after labels and carries it in --json: the total over the
// sessions with a count and one entry per session. Uncounted counts
// the sessions whose result was missing, Pending those with none yet.
type beadTokens struct {
	Tokens    int                 `json:"tokens"`
	Usage     store.Usage         `json:"usage"`
	Partial   bool                `json:"partial"`
	Uncounted int                 `json:"uncounted"`
	Pending   int                 `json:"pending"`
	Sessions  []beadSessionTokens `json:"sessions"`
}

// summarizeBeadTokens sums sessions into the bead's tokens line, in
// the order they started. Only agent sessions take part: one with no
// harness kind (manual, exec, peer, or from before the kind was
// recorded) never has a count due, so it is neither counted nor
// pending, as in the route report. It is nil when no agent session
// ran, and then bead show prints no line.
func summarizeBeadTokens(sessions []store.Session) *beadTokens {
	ordered := slices.Clone(sessions)
	slices.SortStableFunc(ordered, func(a, b store.Session) int {
		return a.StartedAt.Compare(b.StartedAt)
	})
	toks := &beadTokens{}
	for _, s := range ordered {
		if s.Kind == "" {
			continue
		}
		e := beadSessionTokens{Session: s.ID, Stage: s.Stage, Kind: s.Kind}
		u, ok := usageOf(s.Handle)
		switch {
		case !ok:
			e.Pending = true
			toks.Pending++
		case u.missing != "":
			e.Missing = store.OneLine(u.missing)
			toks.Uncounted++
		default:
			e.Tokens = newTokens(u.usage)
			e.Usage = u.usage
			e.Partial = u.partial
			toks.Tokens += e.Tokens
			toks.Usage = toks.Usage.Add(u.usage)
			if u.partial {
				toks.Partial = true
			}
		}
		toks.Sessions = append(toks.Sessions, e)
	}
	if len(toks.Sessions) == 0 {
		return nil
	}
	return toks
}

// line is the tokens line's value: the new total and the cache reads
// over the counted sessions, then one entry per session. A total with
// a partial, missing or still unread session takes ≥, and its entry
// says which: the count with partial, the reason with missing, or
// PENDING.
func (t *beadTokens) line() string {
	total := lowerBound(shortTokens(t.Tokens), t.Partial, t.Uncounted, t.Pending) +
		" new, " + shortTokens(t.Usage.CacheRead) + " cache read"
	entries := make([]string, 0, len(t.Sessions))
	for _, e := range t.Sessions {
		entries = append(entries, e.entry())
	}
	return "tokens " + total + " · " + strings.Join(entries, " · ")
}

// entry is one session's entry on the tokens line.
func (e beadSessionTokens) entry() string {
	head := e.Stage + " " + e.Kind + " "
	switch {
	case e.Pending:
		return head + "PENDING"
	case e.Missing != "":
		return head + "missing (" + e.Missing + ")"
	case e.Partial:
		return head + shortTokens(e.Tokens) + " partial"
	default:
		return head + shortTokens(e.Tokens)
	}
}

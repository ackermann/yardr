package dispatch

import (
	"context"
	"maps"
	"regexp"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// A session whose agent's harness stopped on an error of its own: the model
// is at capacity, the login ran out, a usage limit was hit. The harness
// prints the error and waits for input, which herdr reports as idle, so to
// the yard it looks like an agent that is thinking about its answer or
// waiting on a human, and nobody hears of it before the idle limit (30m).
// The error is in the pane, though. So the end of an idle agent's pane is
// read at the start of each idle spell, and a line there that is a known
// error of its harness raises session_harness_error at once, with a reason
// and the pane's last lines.
//
// The session is left as it is: whether to wait, to get the agent going in
// its pane or to end the session is the yardmaster's to decide.

// Reasons a session_harness_error gives. ReasonOther is an error line that
// is known to stop the harness and is none of the others (a model it may
// not use, no credit, an API error).
const (
	ReasonAtCapacity  = "at_capacity"
	ReasonNotLoggedIn = "not_logged_in"
	ReasonRateLimited = "rate_limited"
	ReasonOther       = "other"
)

// HarnessErrorKey is a handle key: the reason of the session_harness_error
// raised in the session's current idle spell. It is kept on the handle, not
// in memory, so a daemon that replaces itself does not tell the yardmaster
// again, and it is taken off when the agent is seen doing something.
const HarnessErrorKey = "harness_error"

const (
	// harnessLook is how long into an idle spell the pane is read on each
	// pass. A harness prints its error before it goes idle, so the first
	// pass sees it; the passes after cover a screen drawn late. An agent
	// that idles for hours is not read for hours.
	harnessLook = 2 * time.Minute
	// harnessLines is how many of the pane's last lines are looked at. The
	// error that stopped a harness is the last thing it printed, above its
	// input box and status line; further up is what the agent wrote while
	// it still worked, which may well quote an error.
	harnessLines = 12
)

// harnessLine is one error line of a harness: what it means, and how the
// line starts.
type harnessLine struct {
	reason string
	re     *regexp.Regexp
}

// harnessErrors are the error lines each harness kind (a group's `kind`)
// is known to stop on, in the order they are tried: the first that matches
// a line gives the reason, so a line that says two things (an API error
// that ends in "Please run /login") is listed under the one that matters
// before the general one.
//
// To add one: take the line as the harness prints it (its source or docs,
// or the `tail` of a session_idle that should have been this event), and
// add a pattern to the kind's list. A pattern is matched against one line
// of the pane with the marks a TUI puts before a message taken off (see
// harnessMarks), so begin it with ^ unless the text comes later in the
// line: an agent that writes about an error mid-sentence is then not taken
// for a harness that stopped on it. A kind with no list is never reported.
// Add a case to TestHarnessErrorLines with the line.
var harnessErrors = map[string][]harnessLine{
	"claude": {
		{ReasonNotLoggedIn, regexp.MustCompile(`^(Not logged in|You are not logged in|Invalid API key|Please run /login)`)},
		{ReasonNotLoggedIn, regexp.MustCompile(`^(Your session has expired|Session expired)\. Please run /login`)},
		{ReasonNotLoggedIn, regexp.MustCompile(`^API Error.*(Please run /login|authentication_error)`)},
		{ReasonRateLimited, regexp.MustCompile(`^You.ve hit your `)},
		{ReasonRateLimited, regexp.MustCompile(`^(Claude usage|Usage|5-hour|Weekly|Session|Fable|Opus|Sonnet|Fast) limit reached`)},
		{ReasonRateLimited, regexp.MustCompile(`^API Error.*(\b429\b|rate_limit_error|Rate limit)`)},
		{ReasonAtCapacity, regexp.MustCompile(`^Repeated 529 Overloaded errors`)},
		{ReasonAtCapacity, regexp.MustCompile(`^API Error.*(\b529\b|overloaded_error|Overloaded)`)},
		{ReasonOther, regexp.MustCompile(`^There's an issue with the selected model`)},
		{ReasonOther, regexp.MustCompile(`^Credit balance (is )?too low`)},
		{ReasonOther, regexp.MustCompile(`^API Error`)},
	},
	"codex": {
		{ReasonAtCapacity, regexp.MustCompile(`^(Selected model is at capacity|Flex capacity unavailable)`)},
		{ReasonRateLimited, regexp.MustCompile(`^You.ve hit your usage limit`)},
		{ReasonRateLimited, regexp.MustCompile(`^exceeded retry limit, last status: 429`)},
		{ReasonNotLoggedIn, regexp.MustCompile(`^(Not logged in|Sign in with ChatGPT)`)},
		{ReasonNotLoggedIn, regexp.MustCompile(`^Your (access token|authentication session) could not be refreshed`)},
		{ReasonNotLoggedIn, regexp.MustCompile(`Please (log out and )?sign in again\.`)},
		{ReasonNotLoggedIn, regexp.MustCompile(`^(exceeded retry limit, last status: 401|unexpected status 401)`)},
		{ReasonOther, regexp.MustCompile(`^Quota exceeded`)},
		{ReasonOther, regexp.MustCompile(`^(stream error: )?stream disconnected before completion`)},
		{ReasonOther, regexp.MustCompile(`^(exceeded retry limit|unexpected status \d)`)},
	},
}

// harnessMarks are what a harness's TUI draws before a message: Claude
// Code's ⏺ and ⎿, Codex's ■ and •, a box's edge.
const harnessMarks = " \t⏺⎿●■•▌│┃✗✘⚠!"

// HarnessError finds, in the end of a pane, the error line a harness of
// kind stopped on: the reason, and the line as it stands in the pane. The
// last such line counts, since it is what the harness said last.
func HarnessError(kind, tail string) (reason, line string, ok bool) {
	known := harnessErrors[kind]
	if len(known) == 0 {
		return "", "", false
	}
	lines := strings.Split(tail, "\n")
	for i, seen := len(lines)-1, 0; i >= 0 && seen < harnessLines; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		seen++
		said := strings.TrimLeft(l, harnessMarks)
		for _, k := range known {
			if k.re.MatchString(said) {
				return k.reason, l, true
			}
		}
	}
	return "", "", false
}

// paneTail is the end of the pane of s's agent, as an event carries it: the
// lines a session_stalled has always had. A pane that cannot be read says
// so in their place. ok is false for a runner that has no pane to read.
func (d *Dispatcher) paneTail(ctx context.Context, r Runner, s store.Session) (tail string, ok bool) {
	t, ok := r.(Tailer)
	if !ok {
		return "", false
	}
	tail, err := t.Tail(ctx, s.Handle, stallTail)
	if err != nil {
		tail = "(pane not readable: " + err.Error() + ")"
	}
	return tail, true
}

// harnessError raises session_harness_error for the idle session s when
// tail, the end of its pane, shows a known error of its harness, once per
// idle spell. s.Handle is updated with the mark it stores.
func (d *Dispatcher) harnessError(ctx context.Context, s *store.Session, g store.Group, tail string) {
	if s.Handle[HarnessErrorKey] != nil {
		return
	}
	kind, _ := g.Config["kind"].(string)
	if kind == "" {
		kind = "claude" // as the herdr runner starts a group that names none
	}
	reason, line, ok := HarnessError(kind, tail)
	if !ok {
		return
	}
	d.Log.Warn("session's harness stopped on an error", "session", s.ID, "bead", s.Bead, "group", s.Group,
		"kind", kind, "reason", reason, "line", line)
	data := map[string]any{"session": s.ID, "group": s.Group, "stage": s.Stage, "kind": kind,
		"reason": reason, "line": line, "tail": tail,
		"hint": "the agent's harness stopped on an error and sits idle; the line is in line, its pane's last " +
			"lines in tail. The session is left as it is: get the agent going in its pane, or end the " +
			"session with `yardr bead release " + s.Bead + "`"}
	if err := d.Store.LogDispatch(ctx, s.Bead, "session_harness_error", actor, data); err != nil {
		d.Log.Warn("session_harness_error event", "session", s.ID, "err", err)
	}
	d.markHarnessError(ctx, s, reason)
}

// harnessOver ends the spell a session_harness_error was raised in: s's
// agent is doing something again, so its next error is told again.
func (d *Dispatcher) harnessOver(ctx context.Context, s *store.Session) {
	if s.Handle[HarnessErrorKey] != nil {
		d.markHarnessError(ctx, s, "")
	}
}

// markHarnessError keeps reason on s's handle, or takes the mark off for "".
func (d *Dispatcher) markHarnessError(ctx context.Context, s *store.Session, reason string) {
	h := maps.Clone(s.Handle)
	if h == nil {
		h = map[string]any{}
	}
	if reason == "" {
		delete(h, HarnessErrorKey)
	} else {
		h[HarnessErrorKey] = reason
	}
	if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
		d.Log.Warn("mark session's harness error", "session", s.ID, "err", err)
		return
	}
	s.Handle = h
}

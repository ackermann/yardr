package dispatch

import (
	"context"
	"fmt"
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
//
// But for one case, which needs no judgement (yardr-f7aw): on 6 Oct 2026 a
// reviewer's harness answered its first prompt with "Selected model is at
// capacity", and all the yardmaster did was give it the same prompt again
// by hand. So a session that stopped on at_capacity and has done nothing
// yet is given its first prompt again by yardr: harnessRetryWait after the
// error is seen, and again that long after each try while the error still
// ends its pane, HarnessRetries times in the session's life. Each try is a
// session_harness_retry event. Only when the tries are used up is
// session_harness_error raised, as it always was, with how often yardr
// tried (`tries`).
//
// Only the first prompt is sent again, since only that is known to be the
// right thing to say: a session that has left anything behind (worked, as
// for a stall) stopped mid-work, where "continue" may be right or its last
// message may, and that is the yardmaster's as before. So is one whose
// input holds text, whose first prompt is still being delivered, or whose
// runner cannot say what the first prompt was. The other reasons are never
// retried: a login or a usage limit does not come back by asking again.
//
// A usage limit's event says which window is used up and when it resets,
// where a source of the yard knows: see harness_limits.go.

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

// Handle keys of the tries yardr makes for a session stopped on
// at_capacity. They are on the handle, as a pending prompt's are (prompt.go),
// so a daemon that replaces itself neither starts the count again nor
// forgets the wait.
const (
	harnessRetries = "harness_retries"  // how often the first prompt was given again
	harnessRetryAt = "harness_retry_at" // when the pane is next looked at for a try (RFC 3339); only while one waits
)

// HarnessRetries is how often yardr gives a session stopped on at_capacity
// its first prompt again before the yardmaster hears of it.
const HarnessRetries = 3

// harnessRetryWait is how long the error stands before each try, and after
// the last one before it is raised: capacity that is gone is not back in
// seconds, and four waits still end before the idle limit (30m).
const harnessRetryWait = 5 * time.Minute

// FirstPrompter is implemented by runners that can say what a session's
// agent was first told (a herdr agent is pointed at its brief), so that it
// can be told again. "" is a session whose first prompt is not known.
type FirstPrompter interface {
	FirstPrompt(handle map[string]any) string
}

const (
	// harnessLook is how long into an idle spell the pane is read on each
	// pass. A harness prints its error before it goes idle, so the first
	// pass sees it; the passes after cover a screen drawn late. An agent
	// that idles for hours is not read for hours.
	harnessLook = 2 * time.Minute
	// harnessLines is how many of the pane's last lines are looked at. The
	// error that stopped a harness is the last thing it printed, above its
	// input box and status line; further up is what the agent wrote while
	// it still worked, which may well quote an error. It is all of the tail
	// an event carries (stallTail): Kimi breaks its error into lines of the
	// pane's width itself, and in a pane of 60 columns its usage limit
	// began 12 lines from the end, under the box and the status (yardr-r9sh).
	harnessLines = 20
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
//
// Kimi prints `Error: [<code>] <message>`, the code from its own list, and
// breaks the line at the pane's width itself, which no unwrapped read mends:
// only the line's first part is there to match, so its lines are told by
// the code, and a usage limit (a 403, so provider.auth_error to Kimi) by
// the words that still stand before the break in a pane of 60 columns.
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
	"kimi": {
		{ReasonRateLimited, regexp.MustCompile(`^Error: \[provider\.auth_error\] 403 You.ve reached`)},
		{ReasonRateLimited, regexp.MustCompile(`^Error: \[provider\.rate_limit\]`)},
		{ReasonAtCapacity, regexp.MustCompile(`^Error: \[provider\.overloaded\]`)},
		{ReasonNotLoggedIn, regexp.MustCompile(`^Error: \[(auth\.(login_required|token_missing|token_unauthorized)|provider\.auth_error)\]`)},
	},
}

// harnessMarks are what a harness's TUI draws before a message: Claude
// Code's ⏺ and ⎿, Codex's ■ and •, a box's edge. Kimi draws none before an
// error, only space.
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

// harnessWaits reports whether a try for s waits (see harnessRetry), and
// when it is due.
func harnessWaits(s *store.Session) (due time.Time, waits bool) {
	at, _ := s.Handle[harnessRetryAt].(string)
	due, err := time.Parse(time.RFC3339Nano, at)
	return due, err == nil
}

// harnessLooks reports whether the pane of s, idle for idleFor, is read on
// this pass for an error of its harness: while the spell is young, and when
// a try that waits is due. Until it is due, Run is told when to look.
func (d *Dispatcher) harnessLooks(s *store.Session, idleFor time.Duration) bool {
	if due, waits := harnessWaits(s); waits {
		if d.now().Before(due) {
			d.lookAt(due)
			return false
		}
		return true
	}
	return idleFor <= harnessLook && s.Handle[HarnessErrorKey] == nil
}

// harnessError raises session_harness_error for the idle session s when
// tail, the end of its pane, shows a known error of its harness, once per
// idle spell. One that is at_capacity is first left to harnessRetry.
// s.Handle is updated with the marks it stores.
func (d *Dispatcher) harnessError(ctx context.Context, r Runner, s *store.Session, g store.Group, tail string) {
	if s.Handle[HarnessErrorKey] != nil {
		return
	}
	kind, _ := g.Config["kind"].(string)
	if kind == "" {
		kind = "claude" // as the herdr runner starts a group that names none
	}
	reason, line, ok := HarnessError(kind, tail)
	if !ok {
		// The error a try waited on has left the pane.
		if _, waits := harnessWaits(s); waits {
			d.markHarnessError(ctx, s, "")
		}
		return
	}
	data := map[string]any{"session": s.ID, "group": s.Group, "stage": s.Stage, "kind": kind,
		"reason": reason, "line": line}
	if reason == ReasonAtCapacity && d.harnessRetry(ctx, r, s, data) {
		return
	}
	d.Log.Warn("session's harness stopped on an error", "session", s.ID, "bead", s.Bead, "group", s.Group,
		"kind", kind, "reason", reason, "line", line)
	data["tail"] = tail
	data["hint"] = "the agent's harness stopped on an error and sits idle; the line is in line, its pane's last " +
		"lines in tail. The session is left as it is: get the agent going in its pane, or end the " +
		"session with `yardr bead release " + s.Bead + "`"
	if reason == ReasonRateLimited {
		data["hint"] = d.harnessLimits(ctx, kind, data) + data["hint"].(string)
	}
	if tries := handleInt(s.Handle[harnessRetries]); reason == ReasonAtCapacity && tries > 0 {
		data["tries"] = tries
		data["hint"] = fmt.Sprintf("yardr gave the agent its first prompt again %d times, %s apart; ", tries, harnessRetryWait) +
			data["hint"].(string)
	}
	if err := d.Store.LogDispatch(ctx, s.Bead, "session_harness_error", actor, data); err != nil {
		d.Log.Warn("session_harness_error event", "session", s.ID, "err", err)
	}
	d.markHarnessError(ctx, s, reason)
}

// harnessRetry takes one step for s, idle with at_capacity at the end of
// its pane: it starts the wait for a try, leaves a wait that is not over
// alone, or, with the wait over, gives the agent its first prompt again and
// waits once more. data is what the try's event says of the error. It
// reports false when the error is to be raised instead: the tries are used
// up, or s is not a session yardr may prompt (see harnessPrompt).
//
// The wait after a try is on the handle with the try, not started by the
// next look at the pane: an agent that never left idle is past harnessLook
// by then and would not be looked at again. And the error is raised at the
// end of a wait, not at the try, so the last try has had as long as the
// others to show what it did; until the harness prints something, the old
// error still ends its pane.
func (d *Dispatcher) harnessRetry(ctx context.Context, r Runner, s *store.Session, data map[string]any) bool {
	now := d.now()
	due, waits := harnessWaits(s)
	if waits && now.Before(due) {
		return true
	}
	tries := handleInt(s.Handle[harnessRetries])
	if tries >= HarnessRetries {
		return false
	}
	prompt := d.harnessPrompt(ctx, r, s)
	if prompt == "" {
		return false
	}
	next := now.Add(harnessRetryWait)
	h := withHandle(s.Handle, harnessRetryAt, next.UTC().Format(time.RFC3339Nano))
	if waits {
		if err := r.(Nudger).Nudge(ctx, s.Handle, prompt); err != nil {
			d.Log.Warn("give a session at capacity its first prompt again", "session", s.ID, "err", err)
			return true // try again next pass
		}
		tries++
		h[harnessRetries] = tries
	}
	if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
		d.Log.Warn("mark session's harness retry", "session", s.ID, "err", err)
		return !waits // a try that is not counted is not made again: raised
	}
	s.Handle = h
	d.lookAt(next)
	if !waits {
		return true
	}
	d.Log.Warn("session's harness stopped at capacity: first prompt given again", "session", s.ID, "bead", s.Bead,
		"try", tries, "of", HarnessRetries)
	data = maps.Clone(data)
	data["try"], data["of"] = tries, HarnessRetries
	if err := d.Store.LogDispatch(ctx, s.Bead, "session_harness_retry", actor, data); err != nil {
		d.Log.Warn("session_harness_retry event", "session", s.ID, "err", err)
	}
	return true
}

// harnessPrompt is the prompt a try gives s's agent, "" when yardr may not
// prompt it: the first prompt, of a session that has done nothing with it
// yet (worked, as a stall asks) and whose input is empty. Whether it did
// anything is asked before each try, so a session a person got going and
// that stopped again mid-work is not sent back to its brief.
func (d *Dispatcher) harnessPrompt(ctx context.Context, r Runner, s *store.Session) string {
	fp, ok := r.(FirstPrompter)
	if _, nudges := r.(Nudger); !ok || !nudges || s.Handle[PendingPrompt] != nil {
		return ""
	}
	prompt := fp.FirstPrompt(s.Handle)
	if prompt == "" {
		return ""
	}
	if worked, err := d.worked(ctx, r, *s); err != nil || worked {
		return ""
	}
	// Text in the input is someone's (see inputHeld): nothing is typed
	// after it, and the session is theirs.
	if ir, ok := r.(InputReader); ok {
		if text, known, err := ir.InputText(ctx, s.Handle); err == nil && known && strings.TrimSpace(text) != "" {
			return ""
		}
	}
	return prompt
}

// harnessOver ends the spell a session_harness_error was raised in, or a
// try waited in: s's agent is doing something again, so its next error is
// told again. The count of tries stays: it is for the session's life, or an
// agent that works for a moment after each would be prompted for ever.
func (d *Dispatcher) harnessOver(ctx context.Context, s *store.Session) {
	if s.Handle[HarnessErrorKey] != nil || s.Handle[harnessRetryAt] != nil {
		d.markHarnessError(ctx, s, "")
	}
}

// markHarnessError keeps reason on s's handle, or takes the mark off for "".
// Either ends a wait for a try.
func (d *Dispatcher) markHarnessError(ctx context.Context, s *store.Session, reason string) {
	h := maps.Clone(s.Handle)
	if h == nil {
		h = map[string]any{}
	}
	delete(h, harnessRetryAt)
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

// A crew member's harness stops on the same errors, and it has no idle
// limit to be told at: on 6 Oct 2026 the crew member brakeman (kimi) sat at
// its usage limit for half an hour with a review waiting, and nobody heard
// (yardr-r9sh). So the end of its pane is read too, when it is idle with
// something unread in its inbox: one that is idle with nothing to do is
// what a crew member is most of the time, and is left alone.
//
// crewHarnessError raises session_harness_error on r's journal, with the
// crew member named (`crew`) where a session's names its session. The pane
// is read on each pass in the first harnessLook of a spell of being idle
// with an unread inbox, and again for as long after each nudge typed there
// (see crewHarnessLook): the nudge is what a stopped harness answers with
// its error again.
//
// The reason told is kept on r's handle (HarnessErrorKey), as a session's
// is, and the same reason is not told again while it stands. It stays
// through the agent working, unlike a session's: a nudge makes a stopped
// harness work for a moment and stop on the same error, so every new item
// in its inbox would raise the event again. The mark goes when a look finds
// the pane without an error, or the inbox empty (see crewHarnessOver).
// Nothing is tried again for a crew member.
func (d *Dispatcher) crewHarnessError(ctx context.Context, runner Runner, r *store.CrewMember) {
	t, ok := runner.(Tailer)
	if !ok {
		return
	}
	key := "crew:" + r.Name
	since, ok := d.idleSince[key]
	if !ok {
		since = d.now()
		d.idleSince[key] = since
	}
	if d.now().Sub(since) > harnessLook {
		return
	}
	tail, err := t.Tail(ctx, r.Handle, stallTail)
	if err != nil {
		return // a pane that cannot be read says nothing either way
	}
	kind, _ := r.Config["kind"].(string)
	if kind == "" {
		kind = "claude" // as the herdr runner starts one that names none
	}
	reason, line, _ := HarnessError(kind, tail)
	if told, _ := r.Handle[HarnessErrorKey].(string); reason == told {
		return
	}
	if reason != "" {
		d.Log.Warn("crew member's harness stopped on an error", "crew", r.Name, "kind", kind, "reason", reason, "line", line)
		data := map[string]any{"crew": r.Name, "kind": kind, "reason": reason, "line": line, "tail": tail,
			"hint": "the crew member's harness stopped on an error and sits idle with its inbox unread; the line is " +
				"in line, its pane's last lines in tail. It is left as it is: get the agent going in its pane, or " +
				"start a fresh session with `yardr crew restart " + r.Name + "`"}
		if reason == ReasonRateLimited {
			data["hint"] = d.harnessLimits(ctx, kind, data) + data["hint"].(string)
		}
		if err := d.Store.LogDispatch(ctx, r.Journal, "session_harness_error", actor, data); err != nil {
			d.Log.Warn("session_harness_error event", "crew", r.Name, "err", err)
		}
	}
	d.markCrewHarnessError(ctx, r, reason)
}

// crewHarnessLook starts the time r's pane is read in anew: something was
// typed there, and a harness that is stopped says so again under it.
func (d *Dispatcher) crewHarnessLook(r store.CrewMember) {
	d.idleSince["crew:"+r.Name] = d.now()
}

// crewHarnessOver ends the spell r's pane was read in: r's agent works, or
// is idle with nothing unread. With its inbox empty the error told of is
// over as well, since an agent that handled its inbox is not stopped.
func (d *Dispatcher) crewHarnessOver(ctx context.Context, r *store.CrewMember, handled bool) {
	delete(d.idleSince, "crew:"+r.Name)
	if handled && r.Handle[HarnessErrorKey] != nil {
		d.markCrewHarnessError(ctx, r, "")
	}
}

// markCrewHarnessError keeps reason on r's handle, or takes the mark off
// for "".
func (d *Dispatcher) markCrewHarnessError(ctx context.Context, r *store.CrewMember, reason string) {
	h := maps.Clone(r.Handle)
	if h == nil {
		h = map[string]any{}
	}
	if reason == "" {
		delete(h, HarnessErrorKey)
	} else {
		h[HarnessErrorKey] = reason
	}
	if err := d.Store.SetCrewHandle(ctx, r.Name, h); err != nil {
		d.Log.Warn("mark crew member's harness error", "crew", r.Name, "err", err)
		return
	}
	r.Handle = h
}

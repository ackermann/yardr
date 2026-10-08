package dispatch

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// An agent whose harness stopped on a usage limit can do nothing until the
// window opens, and then needs no more than to be told: on 6 Oct 2026 the
// crew member brakeman stopped at 23:20 with a review waiting, its window
// opened at 00:02, and it sat until a person prompted it (yardr-m71s). The
// session_harness_error raised at the stop says when the window resets,
// where a source of the yard knows (harness_limits.go). So for a reset no
// more than harnessResetMax away, yardr keeps the time on the agent's handle
// and comes back for it:
//
//   - While a session waits, its idle spell is not counted (limits):
//     session_idle would fire at 30 minutes and a group with grace would end the session before the window opens.
//     max_age still applies. A crew member has no idle limit.
//   - While a session waits, its group starts no further session with the
//     model it waits with (holdBack, stall.go): each would stop on the same
//     limit, and three would wait where one does. Nothing is asked for it.
//   - At the wake the source is asked once more, since the time was the
//     provider's word hours ago. A window still used up is told of again,
//     as at the stop, with the later time: what the first event said no
//     longer holds. A provider that gives no answer does not hold the
//     prompt back; the time it gave has come.
//   - Then the agent is prompted, when the usage limit still ends its pane,
//     its input is empty and no first prompt is on its way: a session that
//     has done nothing gets its first prompt again, as one at capacity
//     does, and one stopped mid-work or a crew member gets resetLine, which
//     states the fact and no more. Each prompt is a session_harness_retry
//     and one of the HarnessRetries of the session's life.
//
// A prompt typed is not a prompt taken. Seen live at that reset: a harness,
// idle for 44 minutes with 198k tokens of context, answered the prompt with
// a menu of its own ("Cache expired, the next message re-sends the entire
// history: Compact and continue / Start a new session / Continue as-is"),
// drawn in place of its input, and kept the prompt behind it (which harness,
// and the menu as it drew it: harnesses/kimi.yaml). herdr still
// says idle, and the error may still be on the screen above. So harnessLook
// after the prompt yardr looks once (harnessTaken, crewPromptTaken): an
// agent that is working took it; one that is not, in whose pane the prompt
// does not show, did not, and that is told as a session_harness_error with
// `prompt_shown: false` and the pane's end. Nothing is typed again and no
// key is pressed: the menu is the harness's question to whoever runs it,
// its default costs a compaction, and an Enter of yardr's would choose it.
// For the same reason a crew member's nudges wait while its prompt has not
// shown. Where the prompt shows, an error that ends the pane below it
// stands again and is raised as any error is; one above it is the old one.

const (
	// harnessResetMax is how far off a reset may be for yardr to wait for
	// it. A 5-hour window is within it wherever in the window the agent
	// stopped; a weekly one is not, and nobody wants a session held for
	// four days: that is the yardmaster's call.
	harnessResetMax = 6 * time.Hour
	// harnessResetLag is how long after the reset the provider is asked: its
	// clock and the subscription's need not agree to the second.
	harnessResetLag = time.Minute
)

// resetWake is the wait that begins for an agent of handle, stopped on the
// usage limit of w, when yardr will prompt it at the reset: the keys for
// its handle, and the words for the event's hint. data, the event, gets
// `wakes_at`. Nothing is planned, and nil returned, for a window with no
// reset said or one further off than harnessResetMax, and for an agent yardr
// could not prompt then anyway.
//
// A reset the provider still gives although it has passed is looked at in
// harnessResetLag: it has not seen the window open yet.
func (d *Dispatcher) resetWake(r Runner, handle map[string]any, w *limits.Window, data map[string]any) (wake map[string]any, said string) {
	if w == nil || w.ResetsAt == nil || !mayType(r, handle) || handleInt(handle[harnessRetries]) >= HarnessRetries {
		return nil, ""
	}
	now := d.now()
	if w.ResetsAt.Sub(now) > harnessResetMax {
		return nil, ""
	}
	at := *w.ResetsAt
	if at.Before(now) {
		at = now
	}
	at = at.Add(harnessResetLag)
	data["wakes_at"] = at.UTC().Format(time.RFC3339)
	d.lookAt(at)
	return map[string]any{harnessResetAt: at.UTC().Format(time.RFC3339Nano), harnessResetWindow: w.Label},
		fmt.Sprintf("yardr asks again at %s and prompts the agent when the window is open; ", limits.Clock(at, now))
}

// resetLine is what an agent that stopped mid-work on a usage limit is told
// when the window has reset. There is no knowing what its next step was, so
// the line says what happened and leaves the rest to the agent.
func resetLine(kind, window string) string {
	what := kind
	if window != "" {
		what += ", " + window + " window"
	}
	return fmt.Sprintf("yardr: the usage limit your harness stopped on (%s) has reset. Go on where you stopped.", what)
}

// windowOpen asks the yard's source once more whether an agent of kind can
// work: false while a window is used up that resets later, or at a time the
// provider does not say. It is the one place besides harnessLimits where the
// dispatcher runs the source's command, and it is reached only at the wake
// of an agent that is about to be prompted.
func (d *Dispatcher) windowOpen(ctx context.Context, kind string) bool {
	r, err := d.askLimits(ctx, kind)
	if err != nil {
		return ctx.Err() == nil
	}
	w := r.UsedUp()
	return w == nil || w.ResetsAt != nil && !w.ResetsAt.After(d.now())
}

// harnessReset is the wake of the idle session s, whose usage limit's window
// was to reset: see the comment at the head of this file. s.Handle is
// updated with what it stores.
func (d *Dispatcher) harnessReset(ctx context.Context, r Runner, s *store.Session, g store.Group) {
	t, ok := r.(Tailer)
	if !ok {
		d.markHarnessError(ctx, s, "", nil)
		return
	}
	tail, err := t.Tail(ctx, s.Handle, stallTail)
	if err != nil {
		return // a pane that cannot be read says nothing: looked at again next pass
	}
	kind := harnessKind(g.Config)
	reason, line, _ := HarnessError(kind, tail)
	if reason != ReasonRateLimited {
		// The limit has left the pane: what stands there now is judged as
		// in any idle spell, which begins with the next pass.
		d.markHarnessError(ctx, s, "", nil)
		return
	}
	window, _ := s.Handle[harnessResetWindow].(string)
	prompt := d.firstPromptAgain(ctx, r, s)
	if prompt == "" && mayType(r, s.Handle) {
		prompt = resetLine(kind, window)
	}
	tries := handleInt(s.Handle[harnessRetries])
	if prompt == "" || tries >= HarnessRetries || !inputEmpty(ctx, r, s.Handle) {
		// Not yardr's to prompt (text in its input is someone's): it is the
		// yardmaster's from here, and its idle spell counts again.
		d.Log.Warn("session's usage limit was to reset: not prompted", "session", s.ID, "bead", s.Bead)
		d.markHarnessError(ctx, s, reason, nil)
		return
	}
	if !d.windowOpen(ctx, kind) {
		if ctx.Err() != nil {
			return
		}
		// Told again as at the stop, with what the provider says now.
		h := maps.Clone(s.Handle)
		for _, key := range []string{HarnessErrorKey, harnessResetAt, harnessResetWindow, harnessResetModel} {
			delete(h, key)
		}
		s.Handle = h
		d.harnessError(ctx, r, s, g, tail)
		return
	}
	// One the agent did not start on was typed all the same: ownInput finds
	// it in the input on a later pass (harnessPrompted names it).
	if _, err := typeText(ctx, r.(Nudger), s.Handle, prompt); err != nil {
		d.Log.Warn("prompt a session whose usage limit has reset", "session", s.ID, "err", err)
		return // try again next pass
	}
	tries++
	next := d.now().Add(harnessLook)
	h := maps.Clone(s.Handle)
	for _, key := range []string{HarnessErrorKey, harnessResetAt, harnessResetWindow, harnessResetModel} {
		delete(h, key)
	}
	h[harnessRetries], h[harnessRetryAt], h[harnessPrompted] = tries, next.UTC().Format(time.RFC3339Nano), prompt
	if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
		d.Log.Warn("mark session prompted at its usage limit's reset", "session", s.ID, "err", err)
		return
	}
	s.Handle = h
	d.lookAt(next)
	d.Log.Warn("session's usage limit has reset: prompted", "session", s.ID, "bead", s.Bead, "try", tries, "of", HarnessRetries)
	data := map[string]any{"session": s.ID, "group": s.Group, "stage": s.Stage, "kind": kind, "reason": reason, "line": line,
		"try": tries, "of": HarnessRetries, "prompted": prompt}
	if window != "" {
		data["window"] = window
	}
	if err := d.Store.LogDispatch(ctx, s.Bead, "session_harness_retry", actor, data); err != nil {
		d.Log.Warn("session_harness_retry event", "session", s.ID, "err", err)
	}
}

// harnessTaken looks at what the prompt yardr gave the idle session s at a
// reset did, harnessLook after it; tail is the end of its pane. It reports
// true when a harness error ends the pane below the prompt: the caller then
// judges it as it does any error. Otherwise it has dealt with s: the look is
// not due yet, the prompt was taken, or it does not show in the pane, which
// is told. (A session seen working in between never comes here: harnessOver
// ended the watch.)
func (d *Dispatcher) harnessTaken(ctx context.Context, r Runner, s *store.Session, kind, tail string) bool {
	if due, waits := harnessWaits(s); waits && d.now().Before(due) {
		return false
	}
	prompt, _ := s.Handle[harnessPrompted].(string)
	if shown, known := promptShown(ctx, r, s.Handle, prompt); known && !shown {
		data := map[string]any{"session": s.ID, "group": s.Group, "stage": s.Stage,
			"hint": notTakenHint(kind) + "end the session with `yardr bead release " + s.Bead + "`"}
		d.Log.Warn("session prompted at its usage limit's reset has not taken the prompt", "session", s.ID, "bead", s.Bead)
		if err := d.Store.LogDispatch(ctx, s.Bead, "session_harness_error", actor, notTaken(data, kind, prompt, tail, s.Handle)); err != nil {
			d.Log.Warn("session_harness_error event", "session", s.ID, "err", err)
		}
		d.markHarnessError(ctx, s, ReasonRateLimited, nil)
		return false
	}
	reason, line, ok := HarnessError(kind, tail)
	if !ok {
		d.markHarnessError(ctx, s, "", nil)
		return false
	}
	if !standsAfter(tail, line, prompt) {
		// The error it was prompted for, still on its screen: told of
		// already, so the mark stays and this spell's pane is not read for
		// it again.
		d.markHarnessError(ctx, s, reason, nil)
		return false
	}
	h := maps.Clone(s.Handle)
	delete(h, harnessPrompted)
	delete(h, harnessRetryAt)
	s.Handle = h // stored with the mark of the error that is raised now
	return true
}

// notTakenHint begins the hint for a prompt given at a reset that does not
// show in the pane of an agent of kind; the caller ends it with what else
// can be done. What the kind's harness is known to ask first is its file's
// to say (hints.reset), and a kind whose file says nothing adds nothing.
func notTakenHint(kind string) string {
	asks := ""
	if h, _ := agents.Lookup(kind); h.Hints.Reset != "" {
		asks = " (" + h.Hints.Reset + ")"
	}
	return "yardr prompted the agent when the window of its usage limit had reset, and the prompt does not show " +
		"in its pane: its harness has not taken it, and may be asking something first" + asks + ". The pane's last " +
		"lines are in tail. yardr types nothing more there and answers no question of a harness: answer it in its pane, or "
}

// notTaken completes data, a session_harness_error that names whose it is
// and has its hint, for a prompt given at a reset that does not show in the
// agent's pane.
func notTaken(data map[string]any, kind, prompt, tail string, handle map[string]any) map[string]any {
	data["kind"], data["reason"], data["prompted"], data["prompt_shown"], data["tail"] = kind, ReasonRateLimited, prompt, false, tail
	data["tries"] = handleInt(handle[harnessRetries])
	return data
}

// promptLead is how much of a prompt, counted without its white space, is
// looked for in the end of a pane (as the herdr runner looks for one).
const promptLead = 24

// standsAfter reports whether line, the harness error that ends the pane in
// tail, is below prompt there, or prompt has left the tail: the harness
// answered the prompt with it. An error above the prompt is the one the
// prompt was given for.
func standsAfter(tail, line, prompt string) bool {
	lead := []rune(squash(prompt))
	if len(lead) > promptLead {
		lead = lead[:promptLead]
	}
	text := squash(tail)
	at := strings.LastIndex(text, string(lead))
	return at < 0 || strings.LastIndex(text, squash(line)) > at
}

// crewReset is the wake of the crew member r, idle with total unread in its
// inbox (the newest of them last), whose usage limit's window was to reset:
// see the comment at the head of this file. It reports whether r was
// prompted, with its inbox's count in the line as a nudge has it, so the
// pass types no nudge after it.
func (d *Dispatcher) crewReset(ctx context.Context, runner Runner, r *store.CrewMember, total int, last uint64) (prompted bool, err error) {
	due, waits := handleTime(r.Handle, harnessResetAt)
	if !waits {
		return false, nil
	}
	if d.now().Before(due) {
		d.lookAt(due)
		return false, nil
	}
	told, _ := r.Handle[HarnessErrorKey].(string)
	t, ok := runner.(Tailer)
	if !ok {
		d.markCrewHarnessError(ctx, r, told, nil)
		return false, nil
	}
	tail, err := t.Tail(ctx, r.Handle, stallTail)
	if err != nil {
		return false, nil // a pane that cannot be read says nothing: looked at again next pass
	}
	kind := harnessKind(r.Config)
	reason, line, _ := HarnessError(kind, tail)
	tries := handleInt(r.Handle[harnessRetries])
	if reason != ReasonRateLimited || !mayType(runner, r.Handle) || tries >= HarnessRetries {
		// The wait is over; what the pane says now is crewHarnessError's.
		d.markCrewHarnessError(ctx, r, told, nil)
		return false, nil
	}
	key := "crew:" + r.Name
	if held, why := d.inputHeld(ctx, runner, key, r.Handle); held {
		// Someone's text: the prompt stays due, and the provider is not
		// asked on every pass of the wait.
		if why.tells() {
			d.nudgeHeld(ctx, r.Journal, map[string]any{"crew": r.Name}, r.Handle, "the prompt at its usage limit's reset", why)
		}
		return false, nil
	}
	if !d.windowOpen(ctx, kind) {
		if ctx.Err() != nil {
			return false, nil
		}
		// Told again as at the stop, with what the provider says now: the
		// mark goes, and crewHarnessError reads the pane on this pass.
		h := maps.Clone(r.Handle)
		for _, key := range []string{HarnessErrorKey, harnessResetAt, harnessResetWindow} {
			delete(h, key)
		}
		r.Handle = h
		d.crewHarnessLook(*r)
		return false, nil
	}
	window, _ := r.Handle[harnessResetWindow].(string)
	prompt := resetLine(kind, window) + fmt.Sprintf(" %d new in your inbox: run yardr inbox to read them, and yardr inbox --ack when handled.", total)
	typed, err := d.nudge(ctx, runner, *r, prompt)
	if err != nil || !typed {
		return false, err
	}
	tries++
	next := d.now().Add(harnessLook)
	h := maps.Clone(r.Handle)
	delete(h, harnessResetAt)
	delete(h, harnessResetWindow)
	h[harnessRetries], h[harnessRetryAt], h[harnessPrompted] = tries, next.UTC().Format(time.RFC3339Nano), prompt
	d.setCrewHandle(ctx, r, h)
	d.lookAt(next)
	d.Log.Warn("crew member's usage limit has reset: prompted", "crew", r.Name, "try", tries, "of", HarnessRetries)
	data := map[string]any{"crew": r.Name, "kind": kind, "reason": reason, "line": line,
		"try": tries, "of": HarnessRetries, "prompted": prompt}
	if window != "" {
		data["window"] = window
	}
	if err := d.Store.LogDispatch(ctx, r.Journal, "session_harness_retry", actor, data); err != nil {
		d.Log.Warn("session_harness_retry event", "crew", r.Name, "err", err)
	}
	return true, d.Store.MarkNotified(ctx, r.Name, last)
}

// crewPromptTaken looks at what the prompt yardr gave the crew member r at
// a reset did, once harnessLook after it; status is what the runtime says
// of r now. Until it is known that r took the prompt, the prompt stays on
// r's handle and nothing else is typed into its pane (see nudge).
//
// An agent that is working took it. Otherwise the pane says: a prompt that
// does not show there was not taken, which is told once, and the watch goes
// on until it shows or the agent works. One that shows was taken; a harness
// error below it stands again, so the mark of the old one goes and
// crewHarnessError tells of it as of any.
func (d *Dispatcher) crewPromptTaken(ctx context.Context, runner Runner, r *store.CrewMember, status string) {
	prompt, ok := r.Handle[harnessPrompted].(string)
	if !ok {
		return
	}
	due, looks := handleTime(r.Handle, harnessRetryAt)
	if looks && d.now().Before(due) {
		d.lookAt(due)
		return
	}
	h := maps.Clone(r.Handle)
	delete(h, harnessPrompted)
	delete(h, harnessRetryAt)
	if status == "working" {
		if looks {
			delete(h, HarnessErrorKey) // what it stops on next is told
		}
		d.setCrewHandle(ctx, r, h)
		return
	}
	kind := harnessKind(r.Config)
	tail := ""
	if t, ok := runner.(Tailer); ok {
		tail, _ = t.Tail(ctx, r.Handle, stallTail)
	}
	if shown, known := promptShown(ctx, runner, r.Handle, prompt); known && !shown {
		if !looks {
			return // told already
		}
		data := map[string]any{"crew": r.Name,
			"hint": notTakenHint(kind) + "start a fresh session with `yardr crew restart " + r.Name + "`. Its nudges wait until the prompt shows or it works"}
		d.Log.Warn("crew member prompted at its usage limit's reset has not taken the prompt", "crew", r.Name)
		if err := d.Store.LogDispatch(ctx, r.Journal, "session_harness_error", actor, notTaken(data, kind, prompt, tail, r.Handle)); err != nil {
			d.Log.Warn("session_harness_error event", "crew", r.Name, "err", err)
		}
		h[harnessPrompted], h[HarnessErrorKey] = prompt, ReasonRateLimited
		d.setCrewHandle(ctx, r, h)
		return
	}
	if _, line, found := HarnessError(kind, tail); looks && found && standsAfter(tail, line, prompt) {
		delete(h, HarnessErrorKey)
		d.crewHarnessLook(*r)
	}
	d.setCrewHandle(ctx, r, h)
}

// ResetWait is what a session's handle says of the wait for the window of a
// usage limit to reset: until when yardr waits before it asks again and
// prompts the agent, the window as the provider labels it, and the model
// the session's group starts no further session with meanwhile ("" is the
// agent's own default). ok is false for a session that does not wait.
func ResetWait(handle map[string]any) (until time.Time, window, model string, ok bool) {
	until, ok = handleTime(handle, harnessResetAt)
	if !ok {
		return time.Time{}, "", "", false
	}
	window, _ = handle[harnessResetWindow].(string)
	model, _ = handle[harnessResetModel].(string)
	return until, window, model, true
}

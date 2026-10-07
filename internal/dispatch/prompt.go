package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Handle keys next to store.PendingPrompt, for a prompt the dispatcher delivers:
// they are on the handle and not in the dispatcher's memory, so a daemon
// that replaced itself neither sends a prompt the last one sent a moment
// ago nor starts the count of deliveries again.
const (
	promptSends  = "prompt_sends"   // how often the dispatcher sent it
	promptSentAt = "prompt_sent_at" // when it last did (RFC 3339)
	promptLost   = "prompt_lost"    // true once it is given up

	promptSubmits  = "prompt_submits"   // how often the dispatcher submitted it (see PromptSubmitter)
	promptSubmitAt = "prompt_submit_at" // when it last did (RFC 3339)
)

// PromptTries is how often the dispatcher delivers a pending prompt that
// does not arrive before it gives the session up to a human.
const PromptTries = 3

const (
	defaultPromptSettle = 3 * time.Second
	defaultPromptWait   = 10 * time.Second
	defaultDraftWait    = 10 * time.Minute
)

// PromptWatcher is implemented by runners that can see whether a prompt
// reached a session's agent (a herdr pane shows it). A pending prompt the
// runner had sent may have been taken after it stopped looking; one that
// shows is not delivered a second time, and one the dispatcher sent is
// pending until it shows. An error says the runner could not look (a pane
// that cannot be read), which tells nothing either way.
type PromptWatcher interface {
	PromptShown(ctx context.Context, handle map[string]any, prompt string) (bool, error)
}

// PromptSubmitter is implemented by runners that can tell a prompt the
// agent took from one that is typed in its input and was not submitted, and
// can submit the latter (a herdr pane takes Enter). PromptShown of such a
// runner is false for a prompt that is only in the input.
type PromptSubmitter interface {
	PromptTyped(ctx context.Context, handle map[string]any, prompt string) (bool, error)
	SubmitPrompt(ctx context.Context, handle map[string]any) error
}

// What a Nudger that waits for the agent to start on its text (herdr does,
// see the herdr runner's prompt) says with its error when the agent did not.
var (
	// ErrAgentBlocked: nothing was typed, because the agent is at a dialog
	// and the keys would answer it. The text is still due.
	ErrAgentBlocked = errors.New("the agent is at a dialog: nothing was typed")
	// ErrPromptStalled: the text was typed, and the agent was not seen to
	// start on it in the time the runner waits. It is no failure to type:
	// sent again, the text would be in the input twice. Whether it was taken
	// the pane tells (see typeText).
	ErrPromptStalled = errors.New("the text was typed and the agent was not seen to start on it")
)

// typeText has n type text into the agent of handle as its next prompt.
// stalled says the text was typed and the runner did not see the agent
// start on it (ErrPromptStalled): typed like any other, and where the
// runner can tell its own text in the input, due its Enter at once and not
// a settle later, the runner having waited already.
func typeText(ctx context.Context, n Nudger, handle map[string]any, text string) (stalled bool, err error) {
	if err = n.Nudge(ctx, handle, text); errors.Is(err, ErrPromptStalled) {
		return true, nil
	}
	return false, err
}

// InputReader is implemented by runners that can read what is typed in the
// input of a session's agent and not sent (a herdr pane shows it). known is
// false when the runner cannot tell the agent's input from the rest of its
// pane, and an error says it could not look; both tell nothing.
type InputReader interface {
	InputText(ctx context.Context, handle map[string]any) (text string, known bool, err error)
}

// draft is text seen in an agent's input on a pass that had something to
// type there.
type draft struct {
	since time.Time // when it was first seen
	said  bool      // the nudge_held event of this wait is logged
}

type ownNudge struct {
	text string
	at   time.Time
}

// inputHeld reports whether the input of the session of handle holds text,
// so that nothing may be typed there on this pass; key names the session
// in the dispatcher's memory. waited is not zero on the one pass of a wait
// that is to say so: the text has then been there for DraftWait.
//
// Found live (yardr-ps05): a person was typing in the yardmaster's pane when
// the yardmaster was nudged about its inbox. The nudge was typed after their
// words and its Enter sent both as one message; what they had still meant to
// write was lost. Text in an input is someone's, so yardr types nothing after
// it and looks again on the next pass: what it had to say stays due.
//
// Text that stays is a draft someone left. It is never cleared and never
// typed after, however long it sits: both would send or destroy words a
// person wrote, to deliver a line that is still true and still due once
// they are gone. What a long wait costs is that the agent is not told, so
// after DraftWait one nudge_held event says who is waiting on whose input,
// once per wait.
//
// The one text yardr sends is its own, alone in the input because its Enter
// was not taken: that is ownInput's, which looks before anything is typed.
//
// A runner that cannot look, or a harness whose input it cannot tell, says
// nothing: yardr then types as it did before it looked.
func (d *Dispatcher) inputHeld(ctx context.Context, r Runner, key string, handle map[string]any) (held bool, waited time.Duration) {
	ir, ok := r.(InputReader)
	if !ok {
		return false, 0
	}
	text, known, err := ir.InputText(ctx, handle)
	if err != nil || !known || strings.TrimSpace(text) == "" {
		delete(d.drafts, key)
		return false, 0
	}
	now := d.now()
	w := d.drafts[key]
	if w == nil {
		w = &draft{since: now}
		d.drafts[key] = w
	}
	if !w.said && now.Sub(w.since) >= d.DraftWait {
		w.said = true
		return true, now.Sub(w.since)
	}
	return true, 0
}

// InputMarker is implemented by runners that find an agent's input by the
// mark its harness begins it with and know that mark for some kinds of
// harness only. InputMarked reports whether the agent of handle is of such
// a kind. In the pane of any other, a line that begins like an input may be
// one the harness printed: enough to type nothing after it (inputHeld), not
// enough to send Enter for it (ownInput).
type InputMarker interface {
	InputMarked(handle map[string]any) bool
}

// typedText is a text yardr typed into an agent's pane, and what an event
// calls it.
type typedText struct{ what, text string }

// typedInto is what yardr typed into the pane of handle and may find in its
// input: the first prompt, the prompt it gave at a usage limit's reset
// while that is not known to be taken, and the last nudge. key names the
// session in the dispatcher's memory.
//
// A nudge counts for two Intervals: the same words in the input later are
// taken as a person's. The other two have no such end: they are read from
// the handle, so a daemon that replaced itself still knows them, and a
// first prompt left unsent is found however long it has sat.
func (d *Dispatcher) typedInto(r Runner, key string, handle map[string]any) []typedText {
	var own []typedText
	if fp, ok := r.(FirstPrompter); ok {
		if p := fp.FirstPrompt(handle); p != "" {
			own = append(own, typedText{"the first prompt", p})
		}
	}
	if p, _ := handle[harnessPrompted].(string); p != "" {
		own = append(own, typedText{"the prompt at its usage limit's reset", p})
	}
	if n, ok := d.nudged[key]; ok {
		if d.now().Sub(n.at) > 2*d.Interval {
			delete(d.nudged, key)
		} else {
			own = append(own, typedText{"a nudge", n.text})
		}
	}
	return own
}

// unsent is text of yardr's own seen in the input of an idle agent that has
// no prompt pending.
type unsent struct {
	text    string    // the input, without its white space
	since   time.Time // when it was first seen, or when Enter was last sent for it
	submits int       // how often Enter was sent for it
	gaveUp  bool      // session_prompt_gave_up is logged
}

// ownInput sends Enter for text of yardr's own that sits in the input of an
// idle agent with no prompt pending on its handle, and reports whether it
// did on this pass: nothing else is then typed there. It is asked on every
// pass that finds such an agent, a bead's session or a crew member as who
// names it, with its events on bead; key names it in the dispatcher's
// memory, and status is what the runtime says of it.
//
// Found live (yardr-7y16): the yardmaster's first prompt sat typed in its
// input, with "shown, look 1" on the handle and nothing pending, so
// pendingPrompt never looked. Whatever leaves a text of yardr's
// unsent with nothing on the handle (a look that was wrong, a nudge whose
// Enter was lost, a prompt typed a second time into a harness that had no
// input yet), the agent is idle until a person presses Enter. This is the
// net under all of them: after PromptSettle the text gets its Enter,
// PromptTries times in all and PromptSettle apart, then the session is
// reported as pendingPrompt reports one.
//
// The input has to hold yardr's text and nothing else, as often as it was
// typed: with a person's words before or after it, it is theirs (see
// inputHeld) and is never sent. An input the runner cannot tell, or one of
// a harness whose mark it does not know, says nothing, and no key is sent.
//
// The count is in memory, unlike a pending prompt's: there is nothing on
// the handle to keep it with, and a daemon that replaced itself counts from
// one again.
func (d *Dispatcher) ownInput(ctx context.Context, r Runner, key, bead string, who, handle map[string]any, status string) (pressed bool) {
	ir, reads := r.(InputReader)
	sub, submits := r.(PromptSubmitter)
	if m, ok := r.(InputMarker); ok && !m.InputMarked(handle) {
		reads = false
	}
	if !reads || !submits || (status != "idle" && status != "done") {
		delete(d.unsent, key)
		return false
	}
	own := d.typedInto(r, key, handle)
	if len(own) == 0 {
		delete(d.unsent, key)
		return false
	}
	text, known, err := ir.InputText(ctx, handle)
	if err != nil {
		delete(d.unsent, key) // a failed look breaks the observed settle period
		return false
	}
	in := squash(text)
	if known && in == "" {
		delete(d.nudged, key) // its input was empty: no old nudge can still be there
	}
	what, times := "", 0
	for _, t := range own {
		if n := repeats(in, squash(t.text)); known && n > 0 {
			what, times = t.what, n
			break
		}
	}
	if times == 0 {
		delete(d.unsent, key)
		return false
	}
	now := d.now()
	u := d.unsent[key]
	if u == nil || u.text != in {
		u = &unsent{text: in, since: now}
		d.unsent[key] = u
	}
	if u.gaveUp {
		return false
	}
	// Its own Enter may still be on its way, and so may the one sent last.
	if due := u.since.Add(d.PromptSettle); now.Before(due) {
		d.lookAt(due)
		return false
	}
	data := func(more map[string]any) map[string]any {
		for k, v := range who {
			more[k] = v
		}
		more["text"] = what
		if times > 1 {
			more["times"] = times // typed again into an input that still held it
		}
		return more
	}
	pane, _ := handle["pane"].(string)
	if u.submits >= PromptTries {
		u.gaveUp = true
		agent, _ := handle["agent"].(string)
		d.Log.Warn("text of yardr's own is in an idle agent's input and Enter does not send it", "bead", bead, "who", who, "text", what)
		_ = d.Store.LogDispatch(ctx, bead, "session_prompt_gave_up", actor, data(map[string]any{
			"typed": true, "submits": u.submits, "agent": agent,
			"reason": fmt.Sprintf("%s is typed in the agent's input, with no prompt pending on its handle, and Enter was sent %d times "+
				"without the agent taking it; the agent is idle and Enter in its pane is all it needs", what, u.submits),
			"hint": fmt.Sprintf("herdr pane send-keys %s enter", pane)}))
		return false
	}
	if err := sub.SubmitPrompt(ctx, handle); err != nil {
		return false // try again next pass
	}
	u.submits, u.since = u.submits+1, now
	d.lookAt(now.Add(d.PromptSettle))
	reason := fmt.Sprintf("%s was in the agent's input, not submitted, the agent idle and no prompt pending on its handle: "+
		"Enter was sent (the safety net for text of yardr's own left in an input)", what)
	if times > 1 {
		reason += fmt.Sprintf("; the text was there %d times over and is sent as one message", times)
	}
	d.Log.Warn("text of yardr's own was in an idle agent's input: Enter sent", "bead", bead, "who", who, "text", what,
		"submit", u.submits, "of", PromptTries)
	_ = d.Store.LogDispatch(ctx, bead, "session_prompt_typed", actor, data(map[string]any{
		"submit": u.submits, "of": PromptTries, "pane": pane, "reason": reason}))
	return true
}

// repeats is how often in is text over and over with nothing else, both
// without their white space; 0 when it is anything but that.
func repeats(in, text string) int {
	if text == "" || in == "" || len(in)%len(text) != 0 {
		return 0
	}
	n := len(in) / len(text)
	if strings.Repeat(text, n) != in {
		return 0
	}
	return n
}

// squash is s without its white space: a harness wraps what is in its
// input to the width of its pane.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// nudgeHeld logs the one event of a wait on text in an agent's input (see
// inputHeld): what, a nudge or the first prompt, is not typed, for whom
// (a crew member or a bead's session, as who names it), on bead.
func (d *Dispatcher) nudgeHeld(ctx context.Context, bead string, who, handle map[string]any, what string, waited time.Duration) {
	data := map[string]any{
		"waited": waited.Round(time.Second).String(),
		"reason": fmt.Sprintf("the agent's input has held text for %s, so %s is not typed there; "+
			"it is typed once the input is empty. Send or clear the text in its pane",
			waited.Round(time.Second), what)}
	if pane, _ := handle["pane"].(string); pane != "" {
		data["pane"] = pane
	}
	for k, v := range who {
		data[k] = v
	}
	d.Log.Warn("text in the agent's input holds back "+what, "bead", bead, "who", who, "waited", waited.Round(time.Second))
	_ = d.Store.LogDispatch(ctx, bead, "nudge_held", actor, data)
}

// promptShown reports whether r sees prompt in the session of handle, and
// whether it could look at all.
func promptShown(ctx context.Context, r Runner, handle map[string]any, prompt string) (shown, known bool) {
	w, ok := r.(PromptWatcher)
	if !ok {
		return false, false
	}
	shown, err := w.PromptShown(ctx, handle, prompt)
	return shown && err == nil, err == nil
}

// promptPass is what one pass did about a pending prompt.
type promptPass struct {
	handle  map[string]any // the handle to store, nil when it is unchanged
	arrived bool           // the agent has the prompt; it is off the handle
	shown   bool           // ... and it was found in the pane
	sends   int            // deliveries by the dispatcher so far
	lost    int            // the delivery this pass found not to have arrived, 0 for none
	gaveUp  bool           // ... and it was the last: the session is left to a human
	held    time.Duration  // how long other text in the input has held the prompt back, on the pass that says so
	submits int            // times the dispatcher submitted the prompt typed in the input
	typed   int            // the submit this pass made of it, 0 for none
	unsent  bool           // gaveUp, of a prompt that is in the input and was never taken
}

// pendingPrompt takes one step with the pending first prompt p of the
// session of handle, whose agent the runner reports as status; key names
// the session in the dispatcher's memory.
//
// Found live (yardr-85jy): a Codex whose folder-trust dialog a person had
// just answered was idle, got its prompt in the same second and dropped
// the keys; the prompt was cleared as delivered and the session sat at an
// empty input. `agent prompt` succeeding says the keys were sent, not that
// they were taken (see the herdr runner's arrived). So a prompt leaves the
// handle only once it arrived: it shows in the pane, or the agent is
// working after a delivery. One that did neither PromptWait after it was
// sent, with the agent idle, was lost and is sent again, PromptTries times
// in all; then the session is reported and left alone, still watched for
// the prompt a human gives it by hand.
//
// Found live (yardr-cxsb): a crew member's prime was in Claude Code's input,
// not sent, and the crew member idle for 95 seconds until a person pressed
// Return: the text had been typed and its Enter not taken as a submit. Text
// in the input is not an arrival. With the agent idle it is submitted
// (Enter alone: typing it again would put it in the input twice), as often
// as a lost prompt is delivered and as far apart; then the session is
// reported in the same way, Enter being all it needs.
//
// Found live (yardr-myyv): two builders sat idle on a prompt in their
// input that was on no handle, the runner having taken it as arrived at
// one look. A prompt the runner sent and did not see taken comes here as
// PromptUnconfirmed and counts as sent: an agent still working, or a pane
// that still cannot be read, ends the wait, and text in the input of an
// idle agent is submitted as above.
//
// A runner that cannot look (no PromptWatcher, a pane that cannot be read)
// tells nothing: what was sent is then taken as arrived, as it was before
// yardr looked.
func (d *Dispatcher) pendingPrompt(ctx context.Context, r Runner, key string, handle map[string]any, p, status string) promptPass {
	pass := promptPass{sends: handleInt(handle[promptSends]), submits: handleInt(handle[promptSubmits])}
	shown, known := promptShown(ctx, r, handle, p)
	sub, _ := r.(PromptSubmitter)
	typed := false
	if sub != nil && known && !shown {
		typed, _ = sub.PromptTyped(ctx, handle, p)
	}
	// Typed text was sent by someone, as a delivery of the dispatcher's was,
	// and as the runner's own was that it could not confirm.
	sent := pass.sends > 0 || handle[PromptUnconfirmed] == true
	if shown || ((sent || typed) && status == "working") || (sent && !known) {
		delete(d.promptIdle, key)
		delete(d.drafts, key)
		pass.arrived, pass.shown, pass.handle = true, shown, withoutPending(handle)
		return pass
	}
	if handle[promptLost] == true {
		return pass
	}
	if status != "idle" && status != "done" {
		// At a question, or not telling: nothing is sent, and nothing is
		// judged lost while the agent may yet turn to it.
		delete(d.promptIdle, key)
		delete(d.drafts, key)
		return pass
	}
	now := d.now()
	if typed {
		if pass.submits > 0 {
			at, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(handle[promptSubmitAt]))
			if due := at.Add(d.PromptWait); now.Before(due) {
				d.lookAt(due)
				return pass
			}
			if pass.submits >= PromptTries {
				pass.gaveUp, pass.unsent, pass.handle = true, true, withHandle(handle, promptLost, true)
				return pass
			}
		} else if !d.settled(key, now) {
			// Its own Enter may still be on its way.
			return pass
		}
		if err := sub.SubmitPrompt(ctx, handle); err != nil {
			return pass // try again next pass
		}
		delete(d.promptIdle, key)
		pass.submits++
		pass.typed = pass.submits
		pass.handle = withHandle(withHandle(handle, promptSubmits, pass.submits), promptSubmitAt, now.UTC().Format(time.RFC3339Nano))
		d.lookAt(now.Add(d.PromptWait))
		return pass
	}
	if pass.sends > 0 {
		sent, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(handle[promptSentAt]))
		if due := sent.Add(d.PromptWait); now.Before(due) {
			d.lookAt(due)
			return pass
		}
		pass.lost = pass.sends
		if pass.sends >= PromptTries {
			pass.gaveUp, pass.handle = true, withHandle(handle, promptLost, true)
			return pass
		}
	} else if !d.settled(key, now) {
		// The first delivery waits until the agent has been idle for
		// PromptSettle: one that has just left a dialog is reported idle
		// while it still draws its screen, and that is when keys are lost.
		return pass
	}
	n, ok := r.(Nudger)
	if !ok {
		// Nothing can deliver it: not kept for ever.
		delete(d.promptIdle, key)
		pass.arrived, pass.handle = true, withoutPending(handle)
		return pass
	}
	// Text in the input that is not the prompt (that one was submitted
	// above) is someone's: the prompt waits for it to go (see inputHeld).
	if held, waited := d.inputHeld(ctx, r, key, handle); held {
		pass.lost = 0 // said once it is sent again
		pass.held = waited
		return pass
	}
	stalled, err := typeText(ctx, n, handle, p)
	if err != nil {
		pass.lost = 0 // said once it is sent again
		return pass   // try again next pass
	}
	delete(d.promptIdle, key)
	pass.sends++
	if !known {
		pass.arrived, pass.handle = true, withoutPending(handle)
		return pass
	}
	pass.handle = withHandle(withHandle(handle, promptSends, pass.sends), promptSentAt, now.UTC().Format(time.RFC3339Nano))
	d.lookAt(now.Add(d.PromptWait))
	if !stalled || sub == nil {
		return pass
	}
	// The runner waited for the agent to start on it and saw nothing: text
	// that is in the input now has had the time its Enter needs, and gets a
	// second one on this pass, not on one PromptSettle later.
	if typed, _ := sub.PromptTyped(ctx, handle, p); !typed || sub.SubmitPrompt(ctx, handle) != nil {
		return pass
	}
	pass.submits++
	pass.typed = pass.submits
	pass.handle = withHandle(withHandle(pass.handle, promptSubmits, pass.submits), promptSubmitAt, now.UTC().Format(time.RFC3339Nano))
	return pass
}

// settled reports whether the agent of key has been idle for PromptSettle,
// counted from the first pass that asks; until then Run is told when to
// look again.
func (d *Dispatcher) settled(key string, now time.Time) bool {
	since, ok := d.promptIdle[key]
	if !ok {
		since = now
		d.promptIdle[key] = now
	}
	if due := since.Add(d.PromptSettle); now.Before(due) {
		d.lookAt(due)
		return false
	}
	return true
}

// promptEvents logs what pass found out about the first prompt p of who (a
// bead's session or a crew member, as the event's data names it) on bead, so
// `yardr events` tells a prompt that arrived from one that was sent and
// lost, and from one that was typed in the agent's input and had to be
// submitted. session_prompted is the arrival, not the sending.
func (d *Dispatcher) promptEvents(ctx context.Context, bead string, who, handle map[string]any, p string, pass promptPass) {
	data := func(more map[string]any) map[string]any {
		for k, v := range who {
			more[k] = v
		}
		return more
	}
	if pass.lost > 0 {
		d.Log.Warn("first prompt did not arrive", "bead", bead, "who", who, "delivery", pass.lost, "of", PromptTries)
		_ = d.Store.LogDispatch(ctx, bead, "session_prompt_lost", actor, data(map[string]any{
			"delivery": pass.lost, "of": PromptTries}))
	}
	if pass.held > 0 {
		d.nudgeHeld(ctx, bead, who, handle, "the first prompt", pass.held)
	}
	if pass.typed > 0 {
		d.Log.Warn("first prompt was typed and not submitted", "bead", bead, "who", who, "submit", pass.typed, "of", PromptTries)
		_ = d.Store.LogDispatch(ctx, bead, "session_prompt_typed", actor, data(map[string]any{
			"submit": pass.typed, "of": PromptTries,
			"reason": "the first prompt was in the agent's input, not submitted, and the agent idle: Enter was sent"}))
	}
	if pass.gaveUp && pass.unsent {
		agent, _ := handle["agent"].(string)
		pane, _ := handle["pane"].(string)
		_ = d.Store.LogDispatch(ctx, bead, "session_prompt_gave_up", actor, data(map[string]any{
			"typed": true, "submits": pass.submits, "agent": agent,
			"reason": fmt.Sprintf("the first prompt is typed in the agent's input and Enter was sent %d times without the agent "+
				"taking it; the agent is idle and Enter in its pane is all it needs", pass.submits),
			"hint": fmt.Sprintf("herdr pane send-keys %s enter", pane)}))
	} else if pass.gaveUp {
		agent, _ := handle["agent"].(string)
		_ = d.Store.LogDispatch(ctx, bead, "session_prompt_gave_up", actor, data(map[string]any{
			"deliveries": pass.sends, "agent": agent,
			"reason": fmt.Sprintf("the first prompt was sent %d times and never showed in the agent's pane; "+
				"the agent is idle without it and is not prompted again", pass.sends),
			"hint": fmt.Sprintf("herdr agent prompt %s %s", agent, shellQuote(p))}))
	}
	if pass.arrived {
		more := map[string]any{}
		if pass.shown {
			more["shown"] = true // found in the pane
		}
		if pass.sends > 0 {
			more["deliveries"] = pass.sends
		}
		if pass.submits > 0 {
			more["submits"] = pass.submits
		}
		_ = d.Store.LogDispatch(ctx, bead, "session_prompted", actor, data(more))
	}
}

// lookAt has Run make its next pass no later than at: a pending prompt is
// then delivered, or looked for, when it is due and not an Interval later.
func (d *Dispatcher) lookAt(at time.Time) {
	if d.promptDue == nil || at.Before(*d.promptDue) {
		d.promptDue = &at
	}
}

// handleInt reads a count from a handle, which is a float64 once it has
// been through the store.
func handleInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func withHandle(h map[string]any, key string, v any) map[string]any {
	out := make(map[string]any, len(h)+1)
	for k, old := range h {
		out[k] = old
	}
	out[key] = v
	return out
}

// withoutPending is h without its pending prompt and what was kept about
// delivering it.
func withoutPending(h map[string]any) map[string]any {
	out := make(map[string]any, len(h))
	for k, v := range h {
		switch k {
		case store.PendingPrompt, PromptUnconfirmed, promptSends, promptSentAt, promptLost, promptSubmits, promptSubmitAt:
		default:
			out[k] = v
		}
	}
	return out
}

// shellQuote quotes s as one word of a shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

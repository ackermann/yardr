package dispatch

import (
	"context"
	"fmt"
	"strings"
	"time"
)

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

// ownInputEffect is what applying one ownInput pass tells the store and
// the log after its runner asks have finished.
type ownInputEffect struct {
	logMessage string
	logAttrs   []any
	event      string
	data       map[string]any
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
	pressed, effect := d.ownInputWork(ctx, r, key, bead, who, handle, status)
	d.applyOwnInputEffect(ctx, bead, effect)
	return pressed
}

// ownInputWork asks the runner whether text yardr left in its input needs
// Enter. Its caller owns d's memory: reap runs it on a per-session clone
// and applies the returned state and effect in session order, while the
// serial crew and note paths apply them here through ownInput.
func (d *Dispatcher) ownInputWork(ctx context.Context, r Runner, key, bead string, who, handle map[string]any, status string) (pressed bool, effect *ownInputEffect) {
	ir, reads := r.(InputReader)
	sub, submits := r.(PromptSubmitter)
	if m, ok := r.(InputMarker); ok && !m.InputMarked(handle) {
		reads = false
	}
	if !reads || !submits || (status != "idle" && status != "done") {
		delete(d.unsent, key)
		return false, nil
	}
	own := d.typedInto(r, key, handle)
	if len(own) == 0 {
		delete(d.unsent, key)
		return false, nil
	}
	done := askTimer(ctx, askPane)
	text, known, err := ir.InputText(ctx, handle)
	done()
	if err != nil {
		delete(d.unsent, key) // a failed look breaks the observed settle period
		return false, nil
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
		return false, nil
	}
	now := d.now()
	u := d.unsent[key]
	if u == nil || u.text != in {
		u = &unsent{text: in, since: now}
		d.unsent[key] = u
	}
	if u.gaveUp {
		return false, nil
	}
	// Its own Enter may still be on its way, and so may the one sent last.
	if due := u.since.Add(d.PromptSettle); now.Before(due) {
		d.lookAt(due)
		return false, nil
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
		return false, &ownInputEffect{
			logMessage: "text of yardr's own is in an idle agent's input and Enter does not send it",
			logAttrs:   []any{"bead", bead, "who", who, "text", what},
			event:      "session_prompt_gave_up",
			data: data(map[string]any{
				"typed": true, "submits": u.submits, "agent": agent,
				"reason": fmt.Sprintf("%s is typed in the agent's input, with no prompt pending on its handle, and Enter was sent %d times "+
					"without the agent taking it; the agent is idle and Enter in its pane is all it needs", what, u.submits),
				"hint": fmt.Sprintf("herdr pane send-keys %s enter", pane)}),
		}
	}
	done = askTimer(ctx, askNudge)
	err = sub.SubmitPrompt(ctx, handle)
	done()
	if err != nil {
		return false, nil // try again next pass
	}
	u.submits, u.since = u.submits+1, now
	d.lookAt(now.Add(d.PromptSettle))
	reason := fmt.Sprintf("%s was in the agent's input, not submitted, the agent idle and no prompt pending on its handle: "+
		"Enter was sent (the safety net for text of yardr's own left in an input)", what)
	if times > 1 {
		reason += fmt.Sprintf("; the text was there %d times over and is sent as one message", times)
	}
	return true, &ownInputEffect{
		logMessage: "text of yardr's own was in an idle agent's input: Enter sent",
		logAttrs:   []any{"bead", bead, "who", who, "text", what, "submit", u.submits, "of", PromptTries},
		event:      "session_prompt_typed",
		data:       data(map[string]any{"submit": u.submits, "of": PromptTries, "pane": pane, "reason": reason}),
	}
}

// applyOwnInputEffect reports what ownInputWork found after its runner asks;
// a reap worker returns this effect so only its serial apply writes the store.
func (d *Dispatcher) applyOwnInputEffect(ctx context.Context, bead string, effect *ownInputEffect) {
	if effect == nil {
		return
	}
	d.Log.Warn(effect.logMessage, effect.logAttrs...)
	_ = d.Store.LogDispatch(ctx, bead, effect.event, actor, effect.data)
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

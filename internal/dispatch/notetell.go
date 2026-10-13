package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"yardr.dev/yardr/internal/store"
)

// tellNotes tells the live session s about a note written on its bead by
// anyone but it: the oldest one it has not been told of and not read. The
// telling is a crew member's nudge (see nudge), typed into the agent's pane
// as its next prompt: who wrote the note and how to read it, never the
// note's text, which the agent reads from the yard. Text typed into a pane
// is taken as instructions, so a note's own words never go there.
//
// It is told when the agent is idle, and never interrupts a turn: a busy
// agent's notes wait, and one per pass is told, so each is read before the
// next is typed. A session whose agent has not started (its first prompt
// is still pending), or that waits for the window of a usage limit to
// reset, is told nothing yet: the first prompt goes first, and a
// rate-limited agent cannot read the note. A runner with no agent to type
// to (exec, manual, peer) has nothing to wait for, as a crew member's
// nudge has: the note is marked told at once. If the session ends first,
// nothing is sent, and the note shows as untold.
func (d *Dispatcher) tellNotes(ctx context.Context, r Runner, s *store.Session) error {
	notes, err := d.Store.Notes(ctx, s.Bead)
	if err != nil {
		return err
	}
	var untold *store.Note
	for i := range notes {
		if n := &notes[i]; n.Notify == s.ID && !n.Told && !n.Acked {
			untold = n
			break
		}
	}
	if untold == nil {
		return nil
	}
	if _, ok := r.(Nudger); !ok {
		return d.Store.MarkNoteTold(ctx, s.Bead, untold.Seq, s.ID)
	}
	if st := d.status[s.ID]; st != "idle" && st != "done" {
		return nil
	}
	if s.Handle[store.PendingPrompt] != nil {
		return nil
	}
	if s.Handle[harnessPrompted] != nil {
		// The prompt yardr gave at a usage limit's reset is not known to
		// be taken: the harness may be asking something first, and a
		// nudge's Enter would answer it (see nudge).
		return nil
	}
	if _, waits := handleTime(s.Handle, harnessResetAt); waits {
		// It waits for the window of its usage limit to reset, which
		// prompts it then (harness_reset.go): two prompts would go as one
		// message, to an agent that can read neither yet.
		return nil
	}
	author := noteAuthor(untold.Author)
	text := fmt.Sprintf("yardr: a note (note %d) was added to your bead by %s: read it with `yardr bead show %s`, and ack it with `yardr bead note %s --ack %d`.",
		untold.Seq, author, s.Bead, s.Bead, untold.Seq)
	typed, err := d.nudgeSession(ctx, r, s, text)
	if err != nil || !typed {
		return err
	}
	return d.Store.MarkNoteTold(ctx, s.Bead, untold.Seq, s.ID)
}

// noteAuthor is a note's author as the nudge names it. The author is the
// writer's own word (--actor, $YARDR_ACTOR) and the store holds it as
// written, so in a pane it could say anything, at any length, as a note's
// text could: only what reads as a name is typed, one word of letters,
// digits and "-", "_", ".", "@" (a crew member, a session's actor, a
// name@peer). Anyone else's is on the bead, where the agent reads it.
func noteAuthor(author string) string {
	const unnamed = "someone (the bead says who)"
	if author == "" || len(author) > 64 {
		return unnamed
	}
	for _, r := range author {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.@", r) {
			return unnamed
		}
	}
	return author
}

// nudgeSession types text into the agent of the bead session s as its next
// prompt, as nudge does for a crew member's: nothing is typed after text
// in the input, at a dialog, or at the harness's login screen, and what
// was typed but not seen to start is Enter'd by ownInput. typed is false
// when it did not type: the note stays untold, due on a later pass. What
// holds the input back is told of on the bead (nudge_held), as a first
// prompt held back is. A runner that cannot nudge has nothing to wait for.
//
// d.status carries the session's runner status, so unlike a crew member's
// it cannot also mark a held nudge: that mark is under "noteheld:"+id,
// dropped with the session (see forget).
func (d *Dispatcher) nudgeSession(ctx context.Context, r Runner, s *store.Session, text string) (typed bool, err error) {
	n, ok := r.(Nudger)
	if !ok {
		return true, nil
	}
	key := s.ID
	who := map[string]any{"session": s.ID}
	if held, why := d.inputHeld(ctx, r, key, s.Handle); held {
		if why.tells() {
			d.nudgeHeld(ctx, s.Bead, who, s.Handle, "a nudge", why)
		}
		return false, nil
	}
	stalled, err := typeText(ctx, n, s.Handle, text)
	if errors.Is(err, ErrAgentBlocked) {
		// At a dialog a nudge's Enter would answer: held like one behind
		// someone's text, and said once for as long as nothing is typed.
		if held := "noteheld:" + key; d.status[held] != nudgeAtDialog {
			d.status[held] = nudgeAtDialog
			data := map[string]any{"session": s.ID, "reason": "the agent is at a dialog, so a nudge is not typed there: " +
				"its Enter would answer the dialog. It is typed once the dialog is gone. Answer it in its pane"}
			if pane, _ := s.Handle["pane"].(string); pane != "" {
				data["pane"] = pane
			}
			d.Log.Warn("a dialog in the agent's pane holds back a nudge", "session", s.ID)
			_ = d.Store.LogDispatch(ctx, s.Bead, "nudge_held", actor, data)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	delete(d.status, "noteheld:"+key)
	now := d.now()
	d.nudged[key] = ownNudge{text: text, at: now}
	if stalled {
		// As a crew member's (see nudge): the settle is taken as over,
		// and ownInput looks now.
		d.unsent[key] = &unsent{text: squash(text), since: now.Add(-d.PromptSettle)}
		d.ownInput(ctx, r, key, s.Bead, who, s.Handle, "idle")
		if _, why := d.dialogAt(ctx, r, key, s.Handle); why.tells() {
			d.nudgeHeld(ctx, s.Bead, who, s.Handle, "the nudge yardr typed", why)
		}
	}
	return true, nil
}

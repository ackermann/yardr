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
// telling is a crew member's nudge (see nudge), as its next prompt: who
// wrote the note and how to read it, never the note's text, which the
// agent reads from the yard. Text in a pane is taken as instructions, so a
// note's own words never go there.
//
// It is told when the agent is idle, and never interrupts a turn: a busy
// agent's notes wait, and one per pass is told, so each is read before the
// next is typed. A session that waits for the window of a usage limit to
// reset is told nothing yet: a rate-limited agent cannot read the note. A
// runner with no agent to tell (exec, manual, peer) has nothing to wait
// for, as a crew member's nudge has: the note is marked told at once. If
// the session ends first, nothing is sent, and the note shows as untold.
//
// How the telling goes depends on the pane. Where it shows the prompt of
// its harness it is typed, as ever; where it shows none, or none yardr
// can read (the harness idle at a prompt herdr cannot see, a shell of its
// own, a kind whose marks are unknown), it goes through herdr, which
// submits it itself. The note_told event says which way with delivery. A
// telling neither way takes is marked untold, with why, and the yardmaster
// hears it (note_untold is of the class attention): the note then says
// "could not be told", and "will be told" is said no more. One behind a
// first prompt that was given up is marked untold too: the first prompt
// goes first, and it never arrives.
func (d *Dispatcher) tellNotes(ctx context.Context, r Runner, s *store.Session) error {
	notes, err := d.Store.Notes(ctx, s.Bead)
	if err != nil {
		return err
	}
	var untold *store.Note
	for i := range notes {
		if n := &notes[i]; n.Notify == s.ID && !n.Told && !n.Acked && n.Untold == "" {
			untold = n
			break
		}
	}
	if untold == nil {
		return nil
	}
	if _, ok := r.(Nudger); !ok {
		return d.Store.MarkNoteTold(ctx, s.Bead, untold.Seq, s.ID, "")
	}
	if st := d.status[s.ID]; st != "idle" && st != "done" {
		return nil
	}
	direct := !d.paneHasPrompt(ctx, r, s.Handle)
	if s.Handle[store.PendingPrompt] != nil {
		if s.Handle[promptLost] == true {
			// Given up: the first prompt never arrives, so a note
			// behind it never goes either. Marked untold with why,
			// not left for a pass that never comes.
			return d.Store.MarkNoteUntold(ctx, s.Bead, untold.Seq, s.ID,
				"the session's first prompt never arrived and was given up, and a note is not told before it")
		}
		if !direct {
			// The first prompt goes first while its harness screen is up.
			// With no readable screen, the note goes through herdr instead
			// of waiting for the harness to draw one.
			return nil
		}
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
	told, delivery, err := d.nudgeSession(ctx, r, s, text)
	switch {
	case err != nil:
		// Neither way took it, for good: herdr is not down and the
		// agent is not at a dialog, so waiting changes nothing. One
		// bad telling must not fail the pass for every other bead.
		d.Log.Warn("note could not be told", "bead", s.Bead, "note", untold.Seq, "session", s.ID, "err", err)
		return d.Store.MarkNoteUntold(ctx, s.Bead, untold.Seq, s.ID, err.Error())
	case !told:
		return nil
	default:
		return d.Store.MarkNoteTold(ctx, s.Bead, untold.Seq, s.ID, delivery)
	}
}

// paneHasPrompt reports whether the pane of handle shows the prompt of its
// harness: its screen is up, with an input to type into. A screen yardr
// cannot read (an unknown kind, no pane, one that cannot be read) says it
// does not: the telling goes through herdr, which submits it itself.
//
// Found live (yardr-1h6r): Muse draws its own hint where its input is,
// which reads as text someone left, so a telling typed into the pane of a
// kind whose marks are unknown (no muse.yaml) waits behind it for as long
// as it sits there, and no note_untold says why. A runner with no
// ScreenWatcher still says it does: a telling then goes as it did before
// yardr looked, typed with herdr's wait.
func (d *Dispatcher) paneHasPrompt(ctx context.Context, r Runner, handle map[string]any) bool {
	sw, ok := r.(ScreenWatcher)
	if !ok {
		return true
	}
	up, known := sw.HarnessScreen(ctx, handle)
	return known && up
}

// noteAuthor is a note's author as the nudge names it. Outside a pane the
// author is the writer's own word (--actor, $YARDR_ACTOR) and the store
// holds it as written, so it could say anything, at any length, as a
// note's text could: only what reads as a name is typed, one word of
// letters, digits and "-", "_", ".", "@" (a crew member, a session's
// actor, a name@peer). Anyone else's is on the bead, where the agent
// reads it.
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

// nudgeSession sends text to the agent of the bead session s as its next
// prompt, as nudge does for a crew member's. Where the pane shows the
// prompt of its harness it is typed: nothing is typed after text in the
// input, at a dialog, or at the harness's login screen, and what was typed
// but not seen to start is Enter'd by ownInput. Where it shows none it is
// submitted through herdr, which submits it itself, with no input to look
// at first. delivery says which way it went. told is false when neither
// did: the note stays untold, due on a later pass, and what holds the
// input back is told of on the bead (nudge_held), as a first prompt held
// back is. An error says neither way will take it: herdr is not down and
// the agent is not at a dialog. A runner that cannot nudge has nothing to
// wait for.
//
// d.status carries the session's runner status, so unlike a crew member's
// it cannot also mark a held nudge: that mark is under "noteheld:"+id,
// dropped with the session (see forget).
func (d *Dispatcher) nudgeSession(ctx context.Context, r Runner, s *store.Session, text string) (told bool, delivery string, err error) {
	n, ok := r.(Nudger)
	if !ok {
		return true, "", nil
	}
	key := s.ID
	who := map[string]any{"session": s.ID}
	if !d.paneHasPrompt(ctx, r, s.Handle) {
		return d.nudgeDirect(ctx, r, s, text)
	}
	if held, why := d.inputHeld(ctx, r, key, s.Handle); held {
		if why.tells() {
			d.nudgeHeld(ctx, s.Bead, who, s.Handle, "a nudge", why)
		}
		return false, "", nil
	}
	stalled, err := typeText(ctx, n, s.Handle, text)
	if errors.Is(err, ErrAgentBlocked) {
		d.holdSessionNudge(ctx, s, key)
		return false, "", nil
	}
	if err != nil {
		if IsRuntime(err) {
			// herdr itself is down: the telling waits, like every
			// other delivery, and is not marked untold for it.
			return false, "", nil
		}
		return false, "", err
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
	return true, DeliveryTyped, nil
}

// nudgeDirect submits text to the agent of the bead session s through
// herdr, for a pane that shows no prompt of its harness. A runner with no
// direct way is nudged as ever: herdr's wait then says what the agent did
// with it, as for a pane that shows one.
func (d *Dispatcher) nudgeDirect(ctx context.Context, r Runner, s *store.Session, text string) (told bool, delivery string, err error) {
	dn, ok := r.(DirectNudger)
	if !ok {
		n, _ := r.(Nudger)
		stalled, err := typeText(ctx, n, s.Handle, text)
		if errors.Is(err, ErrAgentBlocked) {
			d.holdSessionNudge(ctx, s, s.ID)
			return false, "", nil
		}
		if err != nil {
			if IsRuntime(err) {
				return false, "", nil
			}
			return false, "", err
		}
		d.nudged[s.ID] = ownNudge{text: text, at: d.now()}
		_ = stalled
		return true, DeliveryAgentPrompt, nil
	}
	if err := dn.NudgeDirect(ctx, s.Handle, text); errors.Is(err, ErrAgentBlocked) {
		d.holdSessionNudge(ctx, s, s.ID)
		return false, "", nil
	} else if err != nil {
		if IsRuntime(err) {
			return false, "", nil
		}
		return false, "", err
	}
	delete(d.status, "noteheld:"+s.ID)
	d.nudged[s.ID] = ownNudge{text: text, at: d.now()}
	return true, DeliveryAgentPrompt, nil
}

// holdSessionNudge marks the session's nudge held at a dialog, once for as
// long as nothing is typed: at a dialog a nudge's Enter would answer, so
// it is typed once the dialog is gone.
func (d *Dispatcher) holdSessionNudge(ctx context.Context, s *store.Session, key string) {
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
}

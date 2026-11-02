// The last write of a phased run approval (runask.go): the command ran
// between writes, and this records its answer as an approval does.
package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// finishPhasedRun is the last write of a phased approval
// (approveHookRegister, approveSourceCheck): it records the approval's
// answer, as approveAsk does, holding that the ask is still open. The
// command ran between the writes, so a taken-back or answered ask here
// refuses the recording, not the run.
func (w *wtx) finishPhasedRun(ctx context.Context, id string, seq uint64, actor, asker string, argv, does []string, result string) (*AskApproved, error) {
	who, err := paneAuthor(w.Tx, callerOf(ctx), actor)
	if err != nil {
		return nil, err
	}
	actor = who
	row, err := w.loadBead(id)
	if err != nil {
		return nil, err
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return nil, err
	}
	var ask *ResolvedAsk
	resolved := ResolveAsks(notes, now())
	for i := range resolved {
		if resolved[i].Note.Seq == seq {
			ask = &resolved[i]
			break
		}
	}
	switch {
	case ask == nil:
		return nil, fmt.Errorf("bead %s has no ask %d", row.ID, seq)
	case ask.AskErr != nil:
		return nil, ask.AskErr
	case ask.State == AskDeclined:
		return nil, fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*ask.Current))
	case ask.State == AskTakenBack:
		return nil, fmt.Errorf("ask %d is asked no more: %s", seq, takenBackLine(*ask.Current))
	case ask.State == AskAnswered:
		return nil, fmt.Errorf("ask %d was approved already, by %s: a command runs once for one ask", seq, ask.Current.Note.Author)
	}
	text, err := json.Marshal(Answer{Ask: seq, Ran: &AskRan{Argv: argv, Result: result}})
	if err != nil {
		return nil, err
	}
	if _, err := w.addWitnessed(row, string(text), actor, ask.Ask.Weight); err != nil {
		return nil, err
	}
	return &AskApproved{Bead: row.ID, Ask: seq, Asker: asker, Argv: argv, Does: does, Result: result}, nil
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

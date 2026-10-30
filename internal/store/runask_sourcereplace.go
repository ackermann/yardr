// The replacement half of the source add row of the run-ask list: the card
// of a declaration whose name the yard has already. A replacement keeps
// the source's settings (its env_file, mint_command, call limits, base and
// ignored senders) and every grant of it, so a declaration that changes
// little can still move where the standing credential is sent and which
// programs the yard runs for it. The card shows the declaration that
// stands against the one the ask carries, and weighs as code in every form
// (sourceAddWeight). It tells what a replacement changes as a pack apply
// card does for a source it replaces (packReplacement, packOneSourceDoes),
// and the approval is held to the source whole, its command copies
// included (Seen).
package store

import (
	"fmt"

	"yardr.dev/yardr/internal/source"
)

// cardTooLong refuses a source add card past MaxRunSourceCardLines: a
// card is read whole, so a source it cannot hold is typed in a terminal.
func cardTooLong(name string, lines int, file string) error {
	return fmt.Errorf("source %s: its card would run to %d lines: a card is read whole, so this is the operator's to type in a terminal (yardr source add %s)", name, lines, file)
}

// sourceReplaceCard is the card of source add <file> for the declaration
// d, whose name the yard has: what changes between the declaration that
// stands and the one the ask carries, each changed key whole old and new
// (calls and addresses among them), each command file's change with its
// diff, and who a standing grant of read now reaches. The command that
// takes it back is the same command with the declaration that stands now,
// asked again as a card. The approval is held to the source as it stands
// and to its copies (Seen), so a change to either makes it stale.
func (w *wtx) sourceReplaceCard(run AskRun, d *source.Declaration, carried AskSource) (AskRun, error) {
	file := run.Argv[2]
	src, _, err := sourceIn(w.Tx, d.Name)
	if err != nil {
		return AskRun{}, fmt.Errorf("source %s: the declaration that stands is not one this yardr reads, so a card cannot show what it replaces: it is the operator's to type in a terminal (yardr source add %s): %s", d.Name, file, OneLine(err.Error()))
	}
	pay, err := w.packReplacement(&carried, d, fmt.Sprintf("this is the operator's to type in a terminal (yardr source add %s)", file))
	if err != nil {
		return AskRun{}, err
	}
	for _, f := range carried.Files {
		pay += len(f.Text)
	}
	if pay > MaxRunPackFiles {
		return AskRun{}, fmt.Errorf("source %s: its carried file texts and diffs run to %d bytes: a card holds at most %d together, so this is the operator's to type in a terminal (yardr source add %s)", d.Name, pay, MaxRunPackFiles, file)
	}
	// A source with its credential's file keeps it, so none is owed after.
	lines, err := w.packOneSourceDoes(carried, d, src.EnvFile != "", src.Base)
	if err != nil {
		return AskRun{}, err
	}
	copies := "copies no command files"
	switch {
	case len(carried.Files) > 0:
		copies = fmt.Sprintf("copies %s (%s)", countNoun(len(carried.Files), "command file"), countNoun(len(d.Commands()), "command"))
	case len(carried.Inventory) > 0:
		copies = "copies no command files, and removes the command copies that stand"
	}
	does := append([]string{safeSourceCardLine(fmt.Sprintf("it replaces one source and %s, and changes nothing else. It grants no call, makes no hook, and sets no credential's file: the source keeps its env_file, mint_command, region base, call limits and ignored senders, and every grant of it stands, now for this declaration.", copies))}, lines...)
	if len(does) > MaxRunSourceCardLines {
		return AskRun{}, cardTooLong(d.Name, len(does), file)
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	return AskRun{
		Argv:    run.Argv,
		Does:    does,
		Back:    `yardr bead ask <bead> "<question>" --run "source add <a file holding the declaration that stands now>"`,
		BackBy:  "only the operator",
		Seen:    map[string]string{"source " + d.Name: fingerprint},
		Carried: []AskSource{carried},
	}, nil
}

// sourceReplaceStale says why the ask seq, written as a replacement of the
// source name, is approved no more when the source is not the one its card
// was written about: it is gone, its command copies moved, or its
// declaration or a setting its card was held to was changed.
func (w *wtx) sourceReplaceStale(seq uint64, name string, was, now AskRun) error {
	// Removing a source leaves its copies standing, which a free name's
	// card inventories: gone is the record's absence, not an empty hold.
	if w.Bucket(bSources).Get([]byte(name)) == nil {
		return fmt.Errorf("ask %d was written when the source %s was in the yard: it was removed since. %s", seq, name, nothingRan)
	}
	var wasInv, nowInv []AskInventory
	if len(was.Carried) > 0 {
		wasInv = was.Carried[0].Inventory
	}
	if len(now.Carried) > 0 {
		nowInv = now.Carried[0].Inventory
	}
	if moved := inventoryMoved(wasInv, nowInv); moved != "" {
		return fmt.Errorf("ask %d was written when the source %s's copies stood as carded: %s since. %s", seq, name, moved, nothingRan)
	}
	return w.staleSource(seq, name, was, now)
}

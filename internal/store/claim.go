// A claim is the yard asking the operator "is this you?" of an address
// a delivery arrived from and no user has: the hook receiver drops
// such a delivery (hooks.Receiver.hears), and, for a source whose
// declaration names the address (source.Declaration.ClaimAddress),
// offers this claim instead. It is a run ask on its own question bead:
// yardr user key <operator> <source> with the address carried and
// shown, which the operator approves to write it into their env file
// exactly where yardr user key writes it, or declines to drop it.
// Nothing of the message is kept beyond the first 80 characters the
// card shows.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// claimSnippet is a message's first words for a claim card: its white
// space folded and at most the first 80 characters kept. The rest of
// the message is kept nowhere.
func claimSnippet(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(s) <= 80 {
		return s
	}
	return string([]rune(s)[:80])
}

// ClaimAddress offers a claim for the address a dropped delivery of
// the source sourceName arrived from, with text its first words. It
// reports whether a claim stands open from this call: false, with no
// error, where the source names no claim address, the yard has no
// operator, the operator has an address in the source already, a
// claim for this source and the operator already waits, or the
// delivery carried no address. One claim per source and operator
// waits at a time, whatever address later deliveries bring.
//
// Asked of the operator only, for the operator's own address: another
// user's address is a later bead. The ask is the yard's (author
// "yard"), on a question bead titled with the question, as a stage
// ask is the yard's on its bead.
func (s *Store) ClaimAddress(ctx context.Context, sourceName, address, text string) (bool, error) {
	address = strings.TrimSpace(address)
	if address == "" || strings.ContainsAny(address, " \t\r\n") {
		return false, nil
	}
	src, err := s.GetSource(ctx, sourceName)
	if err != nil {
		return false, nil // no source, no claim: the drop stands alone
	}
	d, err := src.Declaration()
	if err != nil {
		return false, err
	}
	field, env, ok := d.ClaimAddress()
	if !ok {
		return false, nil
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return false, err
	}
	operator := settings[OperatorSetting]
	if operator == "" {
		return false, nil
	}
	if _, err := s.GetUser(ctx, operator); err != nil {
		return false, nil
	}
	snippet := claimSnippet(text)
	question := fmt.Sprintf("A message reached %s from chat %s. Is this you?", sourceName, address)
	if snippet != "" {
		question = fmt.Sprintf("A message reached %s from chat %s (first words: '%s'). Is this you?", sourceName, address, snippet)
	}
	run := AskRun{Argv: []string{"user", "key", operator, sourceName}, Value: address}
	c, _, err := parseRow(run.Argv)
	if err != nil {
		return false, err
	}
	create, err := s.newBead(ctx, NewBead{Title: question, Type: TypeQuestion}, stageAskAuthor)
	if err != nil {
		return false, err
	}
	contextLine := fmt.Sprintf("Its %s is %s, which no user's env file sets as %s; the delivery was dropped and is recorded nowhere.", field, address, env)
	created := false
	err = s.write(func(w *wtx) error {
		// One pending claim per source and operator: scan the asks
		// whole, as UserAsks does, for an open or put-aside user key
		// ask for this pair. A batch may run this twice: the second
		// run sees the first run's ask rolled back with it, so it
		// writes the same one again.
		pending := false
		if err := each(w.Tx, bBeads, "", func(_ []byte, b *beadRow) error {
			if pending {
				return nil
			}
			notes, err := beadNotes(w.Tx, b.UUID)
			if err != nil {
				return err
			}
			for _, r := range ResolveAsks(notes, now()) {
				if r.AskErr != nil || r.Ask.Run == nil || r.Ask.To != operator {
					continue
				}
				if r.State != AskOpen && r.State != AskAside {
					continue
				}
				argv := r.Ask.Run.Argv
				if len(argv) == 4 && argv[0] == "user" && argv[1] == "key" && argv[2] == operator && argv[3] == sourceName {
					pending = true
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if pending {
			return nil // one already waits; created stays false
		}
		// Read the file inside this write too: a giving since the
		// delivery makes it ineligible for a claim. Errors are returned
		// so an unreadable file is never treated as empty.
		var userIn User
		if ok, err := get(w.Tx, bUsers, operator, &userIn); err != nil {
			return err
		} else if !ok {
			return nil
		}
		current, err := w.userKeyAddress(&userIn, env)
		if err != nil {
			return err
		}
		if current != "" {
			return nil
		}
		card, err := w.userKeyCard(run, true)
		if err != nil {
			return err
		}
		card.Description = c.description
		row, err := create(w)
		if err != nil {
			return err
		}
		ask := Ask{Question: question, To: operator, Context: []string{contextLine}, Run: &card}
		text, err := json.Marshal(ask)
		if err != nil {
			return err
		}
		n, err := w.addNoted(row, NoteAsk, string(text), stageAskAuthor)
		if err != nil {
			return err
		}
		if err := w.queueAskPush(operator, row.UUID, n.Seq, ""); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

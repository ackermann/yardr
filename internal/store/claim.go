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
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// claimPlain drops what a terminal would act on from text that came
// from outside the yard: control characters, bidi controls and the
// line and paragraph separators. The rest is data, shown as it is.
func claimPlain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
}

// claimSnippet is a message's first words for a claim card: its white
// space folded and at most the first 80 characters kept. The rest of
// the message is kept nowhere.
func claimSnippet(text string) string {
	s := strings.Join(strings.Fields(claimPlain(text)), " ")
	if utf8.RuneCountInString(s) <= 80 {
		return s
	}
	return string([]rune(s)[:80])
}

// claimPending reports whether a user key claim for this source and
// operator already waits, open or put aside.
func claimPending(tx *bolt.Tx, operator, sourceName string) (bool, error) {
	pending := false
	err := each(tx, bBeads, "", func(_ []byte, b *beadRow) error {
		if pending {
			return nil
		}
		notes, err := beadNotes(tx, b.UUID)
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
	})
	return pending, err
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
	// Keep the normalized event's address byte-for-byte: trimming here
	// could approve a different value than the one the next delivery
	// carries. Values with surrounding whitespace cannot be written as-is,
	// so do not offer a claim for them.
	if address == "" || strings.TrimSpace(address) != address || strings.ContainsAny(address, " \t\r\n") || claimPlain(address) != address {
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
	// A stranger can write again and again: where a claim already
	// waits or an address is set, say so from a read, and keep the
	// write for a claim that may stand. The write checks both again,
	// and a read that fails (an env file that cannot be read) falls
	// through to it, which refuses in its own words.
	blocked := false
	_ = s.read(func(tx *bolt.Tx) error {
		pending, err := claimPending(tx, operator, sourceName)
		if err != nil || pending {
			blocked = pending
			return err
		}
		var userIn User
		if ok, err := get(tx, bUsers, operator, &userIn); err != nil || !ok {
			return err
		}
		current, err := (&wtx{Tx: tx, home: s.home}).userKeyAddress(&userIn, env)
		if err == nil && current != "" {
			blocked = true
		}
		return nil
	})
	if blocked {
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
		pending, err := claimPending(w.Tx, operator, sourceName)
		if err != nil {
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

// mappingPending reports whether a mapping ask for this source and
// sender already waits for the operator, open or put aside.
func mappingPending(tx *bolt.Tx, operator, sourceName, sender string) (bool, error) {
	pending := false
	err := each(tx, bBeads, "", func(_ []byte, b *beadRow) error {
		if pending {
			return nil
		}
		notes, err := beadNotes(tx, b.UUID)
		if err != nil {
			return err
		}
		for _, r := range ResolveAsks(notes, now()) {
			if r.AskErr != nil || r.Ask.Identity == nil || r.Ask.To != operator {
				continue
			}
			if r.State != AskOpen && r.State != AskAside {
				continue
			}
			if r.Ask.Identity.Source == sourceName && r.Ask.Identity.Sender == sender {
				pending = true
			}
		}
		return nil
	})
	return pending, err
}

// identityHolders are the users whose identity in source the sender
// id names (User.IsIn over every user), in name order.
func identityHolders(tx *bolt.Tx, source, sender string) ([]string, error) {
	var holders []string
	err := each(tx, bUsers, "", func(_ []byte, u *User) error {
		if u.IsIn(source, sender) {
			holders = append(holders, u.Name)
		}
		return nil
	})
	slices.Sort(holders)
	return holders, err
}

// ClaimIdentity offers a mapping card for the sender a dropped
// delivery of the source sourceName arrived from, heard by identity
// and unmapped under the hook's unknown word, with channel where it
// arrived and text its first words. It reports whether a card stands
// from this call: false, with no error, where the source hears by
// no identity, the yard has no operator, the sender is mapped to
// exactly one user already, a card for this source and sender
// already waits, the yard has no user to offer, or the delivery
// carried no sender. One card per source and sender waits at a
// time. An id held twice names nobody, so it still offers a card.
//
// Asked of the operator only, for them to name who the sender is:
// picking a user writes that user's identity in this source in the
// answering write (answerMapping). The ask is the yard's (author
// "yard"), on a question bead titled with the question, as a claim
// is. Nothing of the message is kept beyond the first 80 characters
// the card shows.
func (s *Store) ClaimIdentity(ctx context.Context, sourceName, sender, channel, text string) (bool, error) {
	if sender == "" || strings.TrimSpace(sender) != sender || strings.ContainsAny(sender, " \t\r\n") || claimPlain(sender) != sender {
		return false, nil
	}
	if channel != "" && claimPlain(channel) != channel {
		return false, nil
	}
	src, err := s.GetSource(ctx, sourceName)
	if err != nil {
		return false, nil // no source, no card: the drop stands alone
	}
	d, err := src.Declaration()
	if err != nil {
		return false, err
	}
	if !d.HearsByIdentity() {
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
	blocked := false
	_ = s.read(func(tx *bolt.Tx) error {
		pending, err := mappingPending(tx, operator, sourceName, sender)
		if err != nil || pending {
			blocked = pending
			return err
		}
		holders, err := identityHolders(tx, sourceName, sender)
		if err != nil {
			return err
		}
		if len(holders) == 1 {
			blocked = true
		}
		return nil
	})
	if blocked {
		return false, nil
	}
	snippet := claimSnippet(text)
	question := fmt.Sprintf("A message reached %s from %s. Who is this?", sourceName, sender)
	if channel != "" && snippet != "" {
		question = fmt.Sprintf("A message reached %s from %s in channel %s (first words: '%s'). Who is this?", sourceName, sender, channel, snippet)
	} else if channel != "" {
		question = fmt.Sprintf("A message reached %s from %s in channel %s. Who is this?", sourceName, sender, channel)
	} else if snippet != "" {
		question = fmt.Sprintf("A message reached %s from %s (first words: '%s'). Who is this?", sourceName, sender, snippet)
	}
	create, err := s.newBead(ctx, NewBead{Title: question, Type: TypeQuestion}, stageAskAuthor)
	if err != nil {
		return false, err
	}
	created := false
	err = s.write(func(w *wtx) error {
		pending, err := mappingPending(w.Tx, operator, sourceName, sender)
		if err != nil {
			return err
		}
		if pending {
			return nil
		}
		holders, err := identityHolders(w.Tx, sourceName, sender)
		if err != nil {
			return err
		}
		if len(holders) == 1 {
			return nil
		}
		var users []string
		if err := each(w.Tx, bUsers, "", func(_ []byte, u *User) error {
			users = append(users, u.Name)
			return nil
		}); err != nil {
			return err
		}
		slices.Sort(users)
		if len(users) == 0 {
			return nil
		}
		options := make([]AskOption, 0, len(users)+1)
		for _, name := range users {
			options = append(options, AskOption{
				Label:       name,
				Consequence: fmt.Sprintf("yardr user identity %s --source %s --id %s", name, sourceName, sender),
			})
		}
		options = append(options, AskOption{Label: YouDecide, Consequence: "leave it to " + stageAskAuthor})
		contextLine := fmt.Sprintf("Its sender is %s, which no user maps in %s; the delivery was dropped and is recorded nowhere.", sender, sourceName)
		if channel != "" {
			contextLine = fmt.Sprintf("Its sender is %s in channel %s, which no user maps in %s; the delivery was dropped and is recorded nowhere.", sender, channel, sourceName)
		}
		row, err := create(w)
		if err != nil {
			return err
		}
		ask := Ask{
			Question: question, To: operator, Context: []string{contextLine},
			Options:  options,
			Identity: &AskIdentity{Source: sourceName, Sender: sender, Channel: channel, Users: users},
		}
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

// answerMapping writes the identity a mapping ask's pick names, in
// the write that records the answer: option n names Users[n-1],
// whose identity in the ask's source becomes the ask's sender.
// Later, Decline and a YouDecide pick write nothing. It refuses a
// pick by anyone but the operator the card asks, one from an agent
// pane (mayIdentify, as a terminal mapping is), and a stale pick:
// a sender mapped to anyone since the card was written, or a user
// gone since, writes nothing and leaves the card open.
func (w *wtx) answerMapping(ctx context.Context, ask Ask, ans Answer, actor string) error {
	id := ask.Identity
	if id == nil || ans.Option == 0 || ans.Later || ans.Decline {
		return nil
	}
	if actor != ask.To {
		return fmt.Errorf("ask %d asks who %s in %s is of %s, the operator: this is %s", ans.Ask, id.Sender, id.Source, ask.To, or(actor, "nobody"))
	}
	if ans.Option > len(id.Users) {
		return nil // YouDecide: leave it to the yard.
	}
	user := id.Users[ans.Option-1]
	holders, err := identityHolders(w.Tx, id.Source, id.Sender)
	if err != nil {
		return err
	}
	if len(holders) > 0 {
		return fmt.Errorf("ask %d was written when %s in %s was unmapped, and it names %s already: the card is from before that changed. %s", ans.Ask, id.Sender, id.Source, holders[0], nothingRan)
	}
	if _, err := getUser(w.Tx, user); err != nil {
		return fmt.Errorf("ask %d names %s, who is no user of the yard now: the card is from before that changed. %s", ans.Ask, user, nothingRan)
	}
	if err := w.mayIdentify(ctx, user, actor); err != nil {
		return err
	}
	ident := Identity{ID: id.Sender}
	ident.Key = foldIdentityKey(w.Tx, id.Source, ident.Key)
	if err := checkUserIdentityOneToOne(w.Tx, user, id.Source, ident); err != nil {
		return err
	}
	u, err := getUser(w.Tx, user)
	if err != nil {
		return err
	}
	kind, data, err := setIdentityIn(&u.Identities, user, id.Source, ident, "user", "user")
	if err != nil {
		return err
	}
	if err := put(w.Tx, bUsers, user, u); err != nil {
		return err
	}
	return w.event("", kind, actor, data)
}

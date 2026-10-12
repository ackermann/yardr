// A secret ask is an ask whose answer is no words: it asks the operator for
// the credential of a declared source, and what the person types goes to
// the file that credential is read from (GiveSecret), never into a note.
// The ask says where its answer goes, by source and by name; the answer
// says which names were given. Neither holds a value, and nothing else an
// ask has can take one: a pick, words and a decline's words are refused on
// a secret ask (answerable).
//
// A secret ask may ask for a person's own address in a source that tells
// people instead (AskSecret.User): the variable the source's notify key
// names, kept in that user's env file. It is the user's to give, or the
// operator's for them, as the settings that say where a user is told are
// (mayTell).
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/source"
)

// AskSecret is where the answer of a secret ask goes: the variables Env of
// the credential of the declared source Source. All of it is the yard's,
// from the source's declaration as it was when the ask was written: the
// asker names the source and may narrow the names, and names no path and no
// name the declaration does not have.
type AskSecret struct {
	Source string   `json:"source"`
	Env    []string `json:"env"`
	// Host is where the source's calls go, the scheme and host of its base:
	// what the card says beside the source's name. It is kept on the ask
	// because the views render an ask from its note alone; GiveSecret
	// refuses when the source's calls go elsewhere by then.
	Host string `json:"host,omitempty"`
	// User makes it an ask for that user's own address in the source, where
	// the source reaches a person: Env is then the one variable the source's
	// notify key names, and the answer goes to the user's env file, not the
	// source's. "" asks for the source's credential.
	User string `json:"user,omitempty"`
}

// Names are the variables asked for, in words.
func (s AskSecret) Names() string { return strings.Join(s.Env, " and ") }

// Line is what a card says a secret ask asks for, in the yard's words and
// never the asker's: whatever the question and its context say, the value
// becomes these variables of this source, or of this user's file. Giving a
// user's address does not say the user is told through the source, so the
// line names the command that does.
func (s AskSecret) Line() string {
	if s.User != "" {
		return fmt.Sprintf("a secret: %s, %s's own address in the source %s (%s); %s is told through %s once it is given and yardr user set %s %s=%s is run",
			s.Names(), s.User, s.Source, s.Host, s.User, s.Source, s.User, UserNotify, s.Source)
	}
	return fmt.Sprintf("a secret: %s, for the source %s (%s)", s.Names(), s.Source, s.Host)
}

// SecretAsked is the secret an ask asks for, as a client shows it: the
// source, the names to ask the person for, the user whose own address it
// is (absent for a source's credential), and the card's line whole.
type SecretAsked struct {
	Source string   `json:"source"`
	Env    []string `json:"env"`
	User   string   `json:"user,omitempty"`
	Line   string   `json:"line"`
}

// asked is the secret of the ask a as a client shows it, nil for an ask
// that asks for none.
func (a Ask) asked() *SecretAsked {
	if a.Secret == nil {
		return nil
	}
	return &SecretAsked{Source: a.Secret.Source, Env: a.Secret.Env, User: a.Secret.User, Line: a.Secret.Line()}
}

// sourceHost is where the calls of the source d declares go: the scheme and
// host of its base.
func sourceHost(d *source.Declaration) string {
	if u, err := url.Parse(d.Base); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return d.Base
}

// askedSecret checks the secret an ask for the user to asks for, and
// answers it as the ask stores it: the source by its own name, the names in
// the declaration's order (every one of its credential when the asker
// narrowed to none), and the host. It refuses a source the yard does not
// have, one whose declaration names no credential, a name the declaration
// does not have, and anyone but the operator as the one asked: giving is
// setting a source's credential, which is the operator's alone (maySource).
//
// With a User it is that user's own address in the source that is asked
// for (askedAddress).
func (w *wtx) askedSecret(sec AskSecret, to string) (*AskSecret, error) {
	name := strings.TrimSpace(sec.Source)
	if name == "" {
		return nil, fmt.Errorf("a secret ask names the source the secret is for: --secret <source>")
	}
	var src Source
	if ok, err := get(w.Tx, bSources, name, &src); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("a secret is asked for a source the yard has (yardr source list), which the operator adds: source %s: %w", name, ErrNotFound)
	}
	d, err := src.Declaration()
	if err != nil {
		return nil, err
	}
	if user := strings.TrimSpace(sec.User); user != "" {
		return w.askedAddress(d, user, sec.Env, to)
	}
	if d.Credential == nil {
		return nil, fmt.Errorf("source %s: its declaration names no credential, so there is no secret to ask for", name)
	}
	has := d.Credential.Variables()
	for _, n := range sec.Env {
		if !slices.Contains(has, n) {
			return nil, fmt.Errorf("source %s: %s is no variable of its credential (its declaration names %s)", name, n, d.Credential.Names())
		}
	}
	env := has
	if len(sec.Env) > 0 {
		env = slices.DeleteFunc(slices.Clone(has), func(n string) bool { return !slices.Contains(sec.Env, n) })
	}
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return nil, err
	}
	switch {
	case operator == "":
		return nil, fmt.Errorf("a secret is the operator's to give, and this yard has none: yardr settings set operator <name>")
	case to != operator:
		return nil, fmt.Errorf("a secret is the operator's to give (%s): setting a source's credential is theirs alone, so %s cannot be asked for one", operator, to)
	}
	return &AskSecret{Source: d.Name, Env: env, Host: sourceHost(d)}, nil
}

// askedAddress checks an ask for the user's own address in the source d
// declares, asked of to, and answers it as the ask stores it: the one
// variable the source's notify key names. It refuses a source that reaches
// no person, a user the yard does not have, a name the notify key does not
// name, and anyone but that user and the operator as the one asked: where a
// user is told is theirs to say, or the operator's for them (mayTell).
func (w *wtx) askedAddress(d *source.Declaration, user string, env []string, to string) (*AskSecret, error) {
	if d.Notify == nil {
		return nil, fmt.Errorf("source %s: its declaration has no notify key, so it reaches no person and there is no address of %s's to ask for", d.Name, user)
	}
	if _, err := getUser(w.Tx, user); err != nil {
		return nil, fmt.Errorf("an address is asked for a user of the yard (yardr user list): %w", err)
	}
	for _, n := range env {
		if n != d.Notify.AddressEnv {
			return nil, fmt.Errorf("source %s: %s is not what it reads of a user's file (its notify key names %s)", d.Name, n, d.Notify.AddressEnv)
		}
	}
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return nil, err
	}
	if to != user && (operator == "" || to != operator) {
		whose := fmt.Sprintf("%s's own address in the source %s is theirs to give", user, d.Name)
		if operator != "" && operator != user {
			whose += ", or the operator's (" + operator + ") for them"
		}
		return nil, fmt.Errorf("%s: what lands in their inbox goes there, so %s cannot be asked for it", whose, to)
	}
	return &AskSecret{Source: d.Name, Env: []string{d.Notify.AddressEnv}, Host: sourceHost(d), User: user}, nil
}

// GiveSecret answers the secret ask ask on the bead id with values, the
// names it asked for to the values they take: the values are written into
// the file the source's credential is read from (KeySource), and the answer
// is a note of kind answer that holds the names and never a value. It
// answers where the credential is set now, as KeySource does.
//
// It refuses an ask that is not on the bead, asks for no secret or was
// declined; names that are not exactly the ones asked for; whoever may not
// set that source's credential (maySource: the operator, and never the pane
// of an agent); and a source whose calls go elsewhere than the ask's card
// says. No refusal repeats a name or a value that was sent.
//
// An ask for a user's own address in the source (AskSecret.User) is given
// the same way into that user's env file (keyUser), by whoever may say
// where the user is told (mayTell: the user and the operator, and never the
// pane of an agent). It sets no notify: that the user is told through the
// source is a second thing, and theirs to say.
//
// Giving again is allowed while the ask is not declined, as a newer pick
// replaces an older one: each giving is a note with the names.
//
// The answer note is put in the transaction that the file's rename ends
// (keySource), so the store never says "gave" of a value the file does not
// hold. The other way round is not held: when that transaction fails after
// the rename the value is in the file, the ask is open and has no answer
// (*KeyNotRecorded), and giving again answers it.
//
// It is the one call of the operator's that a device may make, and only a
// device of the operator, or of the user whose own address is asked for
// (rpc.OperatorCalls): the note and the events of such a giving say how it
// came, as a device's other writes do (writeVia).
func (s *Store) GiveSecret(ctx context.Context, id string, ask uint64, values map[string]string, actor string) (*SourceKeyed, error) {
	var k sourceKey
	err := s.writeVia(ctx, func(w *wtx) error { return w.giveSecret(ctx, s.home, id, ask, values, actor, &k) })
	if err != nil {
		return nil, k.failed(err)
	}
	return k.keyed(), nil
}

// giveSecret is GiveSecret inside the transaction: the checks, the answer
// note, and last the file (keySource), after whose rename nothing may
// follow but the commit. A batch may run it twice for one call (write):
// the note of the run that is rolled back goes with it, and the second run
// finds the file's lines and writes the same content.
func (w *wtx) giveSecret(ctx context.Context, home, id string, ask uint64, values map[string]string, actor string, k *sourceKey) error {
	row, err := w.loadBead(id)
	if err != nil {
		return err
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return err
	}
	sec, err := givable(row.ID, notes, ask, values, now())
	if err != nil {
		return err
	}
	// Before the note is put: an agent's giving writes nothing, not even
	// what the transaction would take back.
	if sec.User != "" {
		err = w.mayTell(ctx, sec.User, actor)
	} else {
		err = w.maySource(ctx, actor, "setting a source's credential")
	}
	if err != nil {
		return err
	}
	var src Source
	if ok, err := get(w.Tx, bSources, sec.Source, &src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("ask %d: source %s: %w", ask, sec.Source, ErrNotFound)
	}
	d, err := src.Declaration()
	if err != nil {
		return err
	}
	if host := sourceHost(d); host != sec.Host {
		return fmt.Errorf("ask %d says the secret is for the source %s at %s, and the source's calls go to %s now: the ask is from before the source was replaced, and is to be asked again",
			ask, sec.Source, sec.Host, host)
	}
	text, err := json.Marshal(Answer{Ask: ask, Given: sec.Env})
	if err != nil {
		return err
	}
	if _, err := w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
		return err
	}
	if sec.User != "" {
		return w.keyUser(ctx, home, sec.User, sec.Source, values, actor, k)
	}
	return w.keySource(ctx, home, sec.Source, values, actor, k)
}

// givable decides whether values may be given to the ask seq among notes,
// the notes of the bead id, at the time at, and answers the secret it asks
// for. The names of values are held to the ask's here and to the source's
// declaration by keySource or keyUser; the values are the writer's to check
// (config.WriteEnvFile).
func givable(id string, notes []Note, seq uint64, values map[string]string, at time.Time) (*AskSecret, error) {
	var r *ResolvedAsk
	resolved := ResolveAsks(notes, at)
	for i := range resolved {
		if resolved[i].Note.Seq == seq {
			r = &resolved[i]
		}
	}
	switch {
	case r == nil && slices.ContainsFunc(notes, func(n Note) bool { return n.Seq == seq }):
		return nil, fmt.Errorf("note %d on bead %s is not an ask", seq, id)
	case r == nil:
		return nil, fmt.Errorf("bead %s has no ask %d", id, seq)
	case r.AskErr != nil:
		return nil, r.AskErr
	case r.Ask.Secret == nil:
		return nil, fmt.Errorf("ask %d asks for no secret: it is answered with yardr bead answer %s %d <n> [words], or --words", seq, id, seq)
	case r.State == AskDeclined:
		return nil, fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*r.Current))
	case r.State == AskTakenBack:
		return nil, fmt.Errorf("ask %d is asked no more: %s", seq, takenBackLine(*r.Current))
	}
	sec := r.Ask.Secret
	// What was sent is not said back: a client that put a value where a
	// name goes would read it in the refusal.
	for n := range values {
		if !slices.Contains(sec.Env, n) {
			return nil, fmt.Errorf("ask %d asks for %s, and was given a name it did not ask for", seq, sec.Names())
		}
	}
	for _, n := range sec.Env {
		if _, ok := values[n]; !ok {
			return nil, fmt.Errorf("ask %d asks for %s: %s was not given", seq, sec.Names(), n)
		}
	}
	return sec, nil
}

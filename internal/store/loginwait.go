// A login-input secret ask is answered unseen, as a source's is, but its
// value goes to no file: it goes to the harness login that waits for it,
// the `yardr harness login <kind> --json` leader the yardmaster relays
// (design yardr-bwm2, 3.5). The leader registers its wait here, in the
// server's memory, prints the wait's reference in its `input` need, and the
// yardmaster asks one secret ask naming it per need. GiveSecret hands the
// value to the wait and records only that it was given; the value is in no
// note, file, event or mail, only "given". A wait is the login of one user,
// recorded when it is registered, and an ask of any other user that names
// its reference is refused at the giving. A server that stops forgets its
// waits with its memory, as it forgets its pairings: a login led across one
// ends, and is led again.
package store

import (
	"context"
	"fmt"
	"strings"
	"sync"

	bolt "go.etcd.io/bbolt"
)

// loginInputName is the one name a login-input secret ask asks for: the
// value the waiting login takes as its answer. The fields that answer a
// secret ask show it as they show a source's variables, unchanged.
const loginInputName = "INPUT"

// loginIDMax is the longest reference a wait is registered under: a leader's
// is far shorter.
const loginIDMax = 128

// loginWaits are the harness logins that wait for an input, by the
// reference the leader printed: in memory only, so a value is never in the
// file. Its lock is taken before any of the store's, never after: GiveSecret
// hands a value over while it holds the write.
type loginWaits struct {
	mu      sync.Mutex
	waits   map[string]*loginWait
	changed chan struct{}
}

// loginWait is one login's wait for one input: the channel carries the
// value GiveSecret handed it, buffered so the handing never blocks the
// write that records it. owner is the user whose login it is, as it was
// when the wait was registered (loginOwner): the reference alone is
// ephemeral and ties a wait to nobody, so a value is handed only to an ask
// of that user (handLogin), and an ask of another user that names the
// reference finds the wait still waiting.
type loginWait struct {
	ch    chan string
	owner string
}

// ownedBy reports whether the wait is for user's login. A wait with no
// owner is nobody's: no ask is of nobody, so none is handed it.
func (w *loginWait) ownedBy(user string) bool {
	return w.owner != "" && w.owner == user
}

// loginSent is what a giving to a waiting login did, as GiveSecret returns
// it: the out-param a batch rerun finds set, as a file's rename is (keySource).
type loginSent struct {
	sent  bool
	id    string
	given []string
}

// LoginWait registers the login id as waiting for one input and waits for
// it: it answers the value GiveSecret hands to the secret ask that names
// id. The login is the one led for group ("" for the machine's own), and the
// wait is its user's: the one whose login a session on the group burns
// (loginOwner), recorded here so that GiveSecret hands the value only to an
// ask of that user. The leader names the group; the yard says whose it is.
// It refuses an id that already waits. When ctx ends first the wait is
// forgotten, and a giving for it is refused as one for no login waiting: an
// unanswered wait ends the leader, which times ctx out. Nothing of the value
// is kept after it is answered.
func (s *Store) LoginWait(ctx context.Context, id, group string) (string, error) {
	id = strings.TrimSpace(id)
	switch {
	case id == "":
		return "", fmt.Errorf("a login waits under the reference its leader printed, which is never empty")
	case len(id) > loginIDMax:
		return "", fmt.Errorf("a login waits under at most %d characters", loginIDMax)
	}
	var owner string
	err := s.read(func(tx *bolt.Tx) error {
		if err := s.may(ctx, tx, "", ActWaitLogin, Object{Kind: KindYard}); err != nil {
			return err
		}
		var err error
		owner, err = loginOwner(tx, strings.TrimSpace(group))
		return err
	})
	if err != nil {
		return "", err
	}
	s.logins.mu.Lock()
	if s.logins.waits == nil {
		s.logins.waits = map[string]*loginWait{}
	}
	if _, ok := s.logins.waits[id]; ok {
		s.logins.mu.Unlock()
		return "", fmt.Errorf("login %s already waits for an input", id)
	}
	w := &loginWait{ch: make(chan string, 1), owner: owner}
	s.logins.waits[id] = w
	s.logins.signalLocked()
	s.logins.mu.Unlock()
	select {
	case value := <-w.ch:
		s.forgetLoginWait(id, w)
		return value, nil
	case <-ctx.Done():
		s.forgetLoginWait(id, w)
		// A giving that claimed the wait holds the lock across the claim
		// and the handing (handLogin), so what it handed is in the
		// channel once the claim is seen here: a value that arrived with
		// the timeout is an answer, not a timeout.
		select {
		case value := <-w.ch:
			return value, nil
		default:
		}
		return "", ctx.Err()
	}
}

// loginOwner is the user whose login a login led for group is: whose login
// a session on the group burns (loginOf), so a wait and a session cannot
// disagree about it, and the operator's for a login led for no group. A
// group the yard does not have is refused: loginOf would read it as the
// operator's, and a mistyped name would bind a wait to the operator. It is
// "" where the yard names nobody (no operator, and a group that declares no
// login_for): the wait is still registered, so that a login led on stdin
// alone in a yard with no operator yet is not stopped at its first prompt,
// and nothing is handed to it (ownedBy).
func loginOwner(tx *bolt.Tx, group string) (string, error) {
	if group != "" {
		if ok, err := get(tx, bGroups, group, new(Group)); err != nil {
			return "", err
		} else if !ok {
			return "", fmt.Errorf("a login is led for a group of the yard (yardr group list): group %s: %w", group, ErrNotFound)
		}
	}
	return loginOf(tx, group), nil
}

// LoginWaitReady waits until LoginWait has registered id. A leader starts
// LoginWait in one call and waits here before it prints the reference for an
// operator to answer, so a quick giving cannot arrive before the wait.
func (s *Store) LoginWaitReady(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	switch {
	case id == "":
		return fmt.Errorf("a login waits under the reference its leader printed, which is never empty")
	case len(id) > loginIDMax:
		return fmt.Errorf("a login waits under at most %d characters", loginIDMax)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.logins.mu.Lock()
		if _, ok := s.logins.waits[id]; ok {
			s.logins.mu.Unlock()
			return nil
		}
		if s.logins.changed == nil {
			s.logins.changed = make(chan struct{})
		}
		changed := s.logins.changed
		s.logins.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *loginWaits) signalLocked() {
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
}

// forgetLoginWait removes only the wait that is finishing. A reference may
// be registered again after it was handed or its earlier wait ended; the
// earlier call must not remove that newer wait.
func (s *Store) forgetLoginWait(id string, w *loginWait) {
	s.logins.mu.Lock()
	defer s.logins.mu.Unlock()
	if s.logins.waits[id] == w {
		delete(s.logins.waits, id)
		s.logins.signalLocked()
	}
}

// handLogin hands value to the login that waits under id, when that login is
// the one of the user asked, and forgets the wait, so a second giving finds
// no login waiting. It holds the lock across the claim and the handing: the
// channel is empty and buffered, so the send never blocks. The owner is
// compared before the wait is claimed, and a wait that is another user's is
// left waiting, so that the giving that is its own finds it and the value
// never crosses users. It answers whose login waits under id, whether one
// does, and whether the value was handed.
func (s *Store) handLogin(id, asked, value string) (owner string, waits, handed bool) {
	s.logins.mu.Lock()
	defer s.logins.mu.Unlock()
	w, ok := s.logins.waits[id]
	if !ok {
		return "", false, false
	}
	if !w.ownedBy(asked) {
		return w.owner, true, false
	}
	delete(s.logins.waits, id)
	s.logins.signalLocked()
	w.ch <- value
	return w.owner, true, true
}

// loginValueError refuses the value for name on the ask seq as a value for
// a file is refused (WriteEnvFile): empty, with a line break, or with white
// space at either end. No refusal repeats the value.
func loginValueError(seq uint64, name, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("ask %d: the value for %s is empty", seq, name)
	case strings.ContainsAny(value, "\r\n"):
		return fmt.Errorf("ask %d: the value for %s has a line break in it", seq, name)
	case strings.TrimSpace(value) != value:
		return fmt.Errorf("ask %d: the value for %s begins or ends with white space", seq, name)
	}
	return nil
}

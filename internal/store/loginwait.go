// A login-input secret ask is answered unseen, as a source's is, but its
// value goes to no file: it goes to the harness login that waits for it,
// the `yardr harness login <kind> --json` leader the yardmaster relays
// (design yardr-bwm2, 3.5). The leader registers its wait here, in the
// server's memory, prints the wait's reference in its `input` need, and the
// yardmaster asks one secret ask naming it per need. GiveSecret hands the
// value to the wait and records only that it was given; the value is in no
// note, file, event or mail, only "given". A server that stops forgets its
// waits with its memory, as it forgets its pairings: a login led across one
// ends, and is led again.
package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	mu    sync.Mutex
	waits map[string]*loginWait
}

// loginWait is one login's wait for one input: the channel carries the
// value GiveSecret handed it, buffered so the handing never blocks the
// write that records it.
type loginWait struct {
	ch chan string
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
// id. It refuses an id that already waits. When ctx ends first the wait is
// forgotten, and a giving for it is refused as one for no login waiting: an
// unanswered wait ends the leader, which times ctx out. Nothing of the value
// is kept after it is answered.
func (s *Store) LoginWait(ctx context.Context, id string) (string, error) {
	id = strings.TrimSpace(id)
	switch {
	case id == "":
		return "", fmt.Errorf("a login waits under the reference its leader printed, which is never empty")
	case len(id) > loginIDMax:
		return "", fmt.Errorf("a login waits under at most %d characters", loginIDMax)
	}
	s.logins.mu.Lock()
	if s.logins.waits == nil {
		s.logins.waits = map[string]*loginWait{}
	}
	if _, ok := s.logins.waits[id]; ok {
		s.logins.mu.Unlock()
		return "", fmt.Errorf("login %s already waits for an input", id)
	}
	w := &loginWait{ch: make(chan string, 1)}
	s.logins.waits[id] = w
	s.logins.mu.Unlock()
	select {
	case value := <-w.ch:
		s.logins.mu.Lock()
		delete(s.logins.waits, id)
		s.logins.mu.Unlock()
		return value, nil
	case <-ctx.Done():
		s.logins.mu.Lock()
		delete(s.logins.waits, id)
		s.logins.mu.Unlock()
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

// handLogin hands value to the login that waits under id and forgets the
// wait, so a second giving finds no login waiting. It holds the lock across
// the claim and the handing: the channel is empty and buffered, so the send
// never blocks. False when no login waits under id.
func (s *Store) handLogin(id, value string) bool {
	s.logins.mu.Lock()
	defer s.logins.mu.Unlock()
	w, ok := s.logins.waits[id]
	if !ok {
		return false
	}
	delete(s.logins.waits, id)
	w.ch <- value
	return true
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

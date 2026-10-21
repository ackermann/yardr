package hooks

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/source"
)

// Moving an issue forward is what a hook with a map does when a bead enters
// a stage (see store.IssueMove): the same change as SetState, made only when
// it takes the issue further than it is. SetState puts an issue in exactly
// the status asked, so started on an issue in review moves it back; a
// stage's map must never do that, to a bead sent back or to an issue a
// person moved ahead by hand.

// Where an issue stands among the states, in the order work passes through
// them. StoodOther is a status yardr cannot place (cancelled, a duplicate, a
// type it does not know): it counts as past every state, so the issue is
// left alone.
const (
	StoodBefore = "before"
	StoodOther  = "other"
)

// Forwarded is what Forward found and did.
type Forwarded struct {
	Stated
	// Stood is where the issue stood before: before, started, review, done
	// or other. When Changed is false it was at or past the state asked, or
	// had the label asked.
	Stood string `json:"stood"`
}

// Forwarder is what a Stater whose issues a bead's stage can move adds.
type Forwarder interface {
	Stater
	// IssueThread is what Forward takes for the issue at fixed, its URL in
	// the source's fixed form (Linker.Issue). A link made from a URL has no
	// thread id, so the issue is named from the URL alone.
	IssueThread(fixed string) (string, error)
	// CredentialFrom is the source's credential as a crew member's env_file
	// gives it, get being what the file sets a name to: the one Credential
	// reads from the environment in the crew member's pane. When the file sets
	// none of the names it fails with a *MissingCredential.
	CredentialFrom(get func(name string) string) (string, error)
	// Places reports whether the source has a status for state at all, so a
	// map that names one it has not is refused when it is written.
	Places(state string) error
	// Forward moves the thread to the status state means in this source,
	// unless it stands at or past it already, or adds the label a
	// label:<name> names, unless the thread has it. It reads once and
	// writes at most once.
	Forward(ctx context.Context, thread, state, cred string) (Forwarded, error)
}

// ForwarderFor is the source's Forwarder, if a bead's stage can move
// its issues: the one built from d, the yard's declared source of that
// name, when it has the keys moving takes, else the builtin one of that
// name. No name is both, so at most one applies. d is nil when the yard
// declares no such source. home is the yard's home, which a relative
// command program runs from; mint is the source's mint_command
// setting, "" when the yard set none.
func ForwarderFor(source string, d *source.Declaration, home, mint string) (Forwarder, bool) {
	if dec, ok := declared(source, d, home, mint); ok {
		if dec.forwardable() {
			return dec, true
		}
		return nil, false
	}
	if f, ok := Adapters[source].(Forwarder); ok {
		return f, true
	}
	return nil, false
}

// Forwardable lists the sources a bead's stage can move issues in, sorted:
// the builtin ones, and the yard's declared sources among decls with the
// keys moving takes.
func Forwardable(decls ...*source.Declaration) []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := ForwarderFor(s, nil, "", ""); ok {
			out = append(out, s)
		}
	}
	for _, d := range decls {
		if d == nil {
			continue
		}
		if _, ok := ForwarderFor(d.Name, d, "", ""); ok {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

type finalError struct{ err error }

func (e *finalError) Error() string { return e.err.Error() }
func (e *finalError) Unwrap() error { return e.err }

func final(err error) error { return &finalError{err} }

// Final reports whether err is one a retry cannot change.
func Final(err error) bool {
	var fe *finalError
	return errors.As(err, &fe)
}

// Declared

// CredentialFrom is the source's credential as a crew member's env_file
// gives it: the mint's inputs where it sets every name, else the variable
// its credential.env names. The core hands the value to every thread
// command; the commands look nothing up themselves.
func (d Declared) CredentialFrom(get func(string) string) (string, error) {
	return declaredCredential(d.D, get)
}

// Places reports whether the source has a status for state at all: one of
// its thread.states, or a label when the source takes them
// (thread.labels). Without them a label is refused: the yard sets no
// label in a declared source.
func (d Declared) Places(state string) error {
	if _, ok := Label(state); ok {
		if d.D.Thread.Labels {
			return nil
		}
		return noLabel(d.D.Name, state)
	}
	if !slices.Contains(d.D.Thread.States, state) {
		return final(fmt.Errorf("%q is no state of %s: want %s", state, d.D.Name, strings.Join(d.D.Thread.States, ", ")))
	}
	return nil
}

// IssueThread is the id a thread command takes for the issue at fixed, its
// URL in the source's fixed form: link.thread over the groups link.issue
// reads from the URL's path, or the pull shape's, for a source with one.
// It runs nothing.
func (d Declared) IssueThread(fixed string) (string, error) {
	u, err := url.Parse(fixed)
	if err != nil {
		return "", final(fmt.Errorf("%s: %s names no issue", d.D.Name, fixed))
	}
	if thread, ok := d.D.Link.ThreadOf(u.Path); ok {
		return thread, nil
	}
	if d.D.Link.Pull != nil {
		if thread, ok := d.D.Link.PullThreadOf(u.Path); ok {
			return thread, nil
		}
	}
	return "", final(fmt.Errorf("%s: %s names no issue", d.D.Name, fixed))
}

// Forward moves the thread to the status state means in this source, by
// thread.state with forward, which never moves a thread backwards itself:
// one at or past the status is reported as not moved, with no second call.
// Stood is empty: the command says only whether it moved.
func (d Declared) Forward(ctx context.Context, thread, state, cred string) (Forwarded, error) {
	st, err := d.setState(ctx, thread, state, true, cred)
	if err != nil {
		return Forwarded{}, err
	}
	return Forwarded{Stated: st}, nil
}

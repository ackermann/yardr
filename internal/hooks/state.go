package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// Setting a thread's status is the third outbound call, after a reply and
// an opened thread: an issue moves to started, review or done. The call
// takes a thread's id and a credential and nothing else, so whoever holds
// both can make it: the CLI in a crew member's pane, with the crew member's own
// credential, and the daemon for a hook that acts as a crew member (forward.go).

// The states a thread can be asked into. The words are yardr's; each source
// has its own statuses, and its Stater says which one a word means there.
const (
	StateStarted = "started"
	StateReview  = "review"
	StateDone    = "done"
)

// States lists the words, in the order work passes through them.
func States() []string { return []string{StateStarted, StateReview, StateDone} }

// Label is the label a word asks for in place of a state, and whether it is
// such a word: label:<name>. A source whose threads take labels adds it and
// never removes one; any other refuses the word.
func Label(word string) (string, bool) { return store.ThreadLabel(word) }

// Word reports whether word is one a thread can be asked into in some
// source: a state, or a label.
func Word(word string) bool {
	_, label := Label(word)
	return label || slices.Contains(States(), word)
}

// Stated is what SetState found and did.
type Stated struct {
	Thread Thread `json:"thread"`
	// Status is the source's name of the status the thread is in now, and
	// Was the one it was in before. For a label Status is "labeled <name>",
	// as the source spells the name, and Was is empty.
	Status string `json:"status"`
	Was    string `json:"was,omitempty"`
	// Changed is false when the thread was in the status already: no call
	// changed anything.
	Changed bool `json:"changed"`
	// Instead is the word acted on when it is not the one asked: started,
	// for a review asked of a Linear team that has no review status.
	Instead string `json:"instead,omitempty"`
}

// Stater is what a Replier whose source lets a thread's status be set adds.
type Stater interface {
	Replier
	// SetState moves the thread (its id as deliveries name it) to the
	// status state means in this source, or adds the label a label:<name>
	// names. A thread already there is left as it is, and a word the source
	// has no status for is refused.
	SetState(ctx context.Context, thread, state, cred string) (Stated, error)
}

// StaterFor is the source's Stater, if a thread's status can be set in
// it: the one built from d, the yard's declared source of that name,
// when it has the keys setting takes, else the builtin one of that
// name. No name is both, so at most one applies. d is nil when the yard
// declares no such source. home is the yard's home, which a relative
// command program runs from; mint is the source's mint_command
// setting, "" when the yard set none.
func StaterFor(source string, d *source.Declaration, home, mint string) (Stater, bool) {
	if dec, ok := declared(source, d, home, mint); ok {
		if dec.stateable() {
			return dec, true
		}
		return nil, false
	}
	if s, ok := Adapters[source].(Stater); ok {
		return s, true
	}
	return nil, false
}

// Stateable lists the sources a thread's status can be set in, sorted: the
// builtin ones, and the yard's declared sources among decls with the keys
// setting takes.
func Stateable(decls ...*source.Declaration) []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := StaterFor(s, nil, "", ""); ok {
			out = append(out, s)
		}
	}
	for _, d := range decls {
		if d == nil {
			continue
		}
		if _, ok := StaterFor(d.Name, d, "", ""); ok {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// noLabel refuses a label in a source whose threads yardr sets none on.
func noLabel(source, word string) error {
	return final(fmt.Errorf("%s: yardr sets no label there, so %q is no state: want %s", source, word, strings.Join(States(), ", ")))
}

func (d Declared) SetState(ctx context.Context, thread, state, cred string) (Stated, error) {
	return d.setState(ctx, thread, state, false, cred)
}

// setState runs thread.state, with forward for a stage map's move, which
// the command never moves backwards: in {thread, state, forward?}.
func (d Declared) setState(ctx context.Context, thread, state string, forward bool, cred string) (Stated, error) {
	if err := d.Places(state); err != nil {
		return Stated{}, err
	}
	if thread == "" {
		return Stated{}, final(fmt.Errorf("%s: no thread to set the status of", d.D.Name))
	}
	in := map[string]any{"thread": thread, "state": state}
	if forward {
		in["forward"] = true
	}
	var out Stated
	err := d.runThread(ctx, "thread.state", d.D.Thread.State, in, cred, func(raw []byte) error {
		var o struct {
			Status string `json:"status"`
			Moved  bool   `json:"moved"`
			Was    string `json:"was"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return fmt.Errorf("its answer is no {status, moved}: %v", err)
		}
		if o.Status == "" {
			return fmt.Errorf("its answer names no status")
		}
		out = Stated{Thread: Thread{ID: thread}, Status: o.Status, Was: o.Was, Changed: o.Moved}
		if !o.Moved {
			out.Was = o.Status
		}
		return nil
	})
	if err != nil {
		return Stated{}, err
	}
	return out, nil
}

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"yardr.dev/yardr/internal/store"
)

// The gate in front of a weighty approval (design yardr-ekmx 3.3). The yard
// refuses a weighty card approved by a call that does not say its person saw
// the weight (weighed), and checks nothing more: the word is the client's.
// A terminal keeps it by asking: it prints the card's weight and wants the
// ask's number typed back, and sends weighed on a match and stops on
// anything else. It is friction against approving on reflex and no
// authentication, since the terminal is the yard's own machine and its shell
// is the operator's.
//
// Without a terminal the card is declined, not approved on a flag: a gate
// that a script walks through is not a gate. The person approves it in
// footplate or in a terminal.

// askedCard reads the ask seq of the bead id as the yard stored it. It says
// no when the ask cannot be read here (a bead@peer, which no command of this
// yard approves, an ask that is not there): the call that follows refuses
// that in its own words, and the gate stays out of its way.
func askedCard(ctx context.Context, st store.API, id string, seq uint64) (store.Ask, bool) {
	if strings.Contains(id, "@") {
		return store.Ask{}, false
	}
	notes, err := st.Notes(ctx, id)
	if err != nil {
		return store.Ask{}, false
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq && r.AskErr == nil {
			return r.Ask, true
		}
	}
	return store.Ask{}, false
}

// gate is weighed for the ask seq of the bead id, whose weight is the one
// the yard stored on it. Before it asks anything it asks the store's policy
// whether this run's actor may approve the card at all, by the gate the
// approval itself asks (store.ApprovalGate): one who may not is refused at
// once, in the store's words, and not after typing the number back.
func (a *app) gate(c *cobra.Command, st store.API, id string, seq uint64, act, again string) (bool, error) {
	card, ok := askedCard(c.Context(), st, id, seq)
	if !ok {
		return false, nil
	}
	if gate, obj, ok := store.ApprovalGate(id, seq, card); ok {
		if err := st.May(c.Context(), a.who(), gate, obj); err != nil {
			return false, a.loginActor(c.Context(), st, err)
		}
	}
	return a.weighed(c, id, seq, card.Weight, act, again)
}

// loginActor is the refusal err of what this run did as its actor, with how
// to name another where the actor is the login's ($USER) and a crew
// member's name: a person whose account shares a name with a crew member
// acts as that crew member unless they say who they are, and the store's
// refusal ("boo is a crew member") does not tell them how to.
func (a *app) loginActor(ctx context.Context, st store.API, err error) error {
	if a.actor != "" || a.identity().actor != "" {
		return err
	}
	if _, crewErr := st.GetCrew(ctx, a.who()); crewErr != nil {
		return err
	}
	return fmt.Errorf("%w.\nyardr took you for %s because that is the name you are logged in as ($USER), and %s is also the name of a crew member, so you acted as that crew member. To act as yourself, add --actor <you> or set YARDR_ACTOR to your name", err, a.who(), a.who())
}

// weighed says whether to send weighed with the approval of the ask seq of
// the bead id, whose card the yard weighed as weight: false and no error
// for a routine card, which asks for no word; true when this command is at
// a terminal and its person typed the ask's number back; and an error,
// before anything is approved or typed, when there is no terminal or the
// number was not typed back. act is the verb of what is asked (approve,
// give, pick), and again what to put after "yardr bead answer <id> <ask>"
// to do it where the gate can be walked through.
func (a *app) weighed(c *cobra.Command, id string, seq uint64, weight []store.AskWeight, act, again string) (bool, error) {
	if len(weight) == 0 {
		return false, nil
	}
	in := c.InOrStdin()
	if !a.atTerminal(in) {
		return false, fmt.Errorf("ask %d is %s, and a command with no terminal to type its number at cannot %s it. Approve it in footplate, or in a terminal on the yard's machine: yardr bead answer %s %d %s",
			seq, store.WeightWords(weight), act, id, seq, again)
	}
	// The prompt goes to the command's error stream, so what the command
	// prints stays the result alone.
	warn := c.ErrOrStderr()
	for _, w := range weight {
		fmt.Fprintln(warn, "  "+w.Marked())
	}
	fmt.Fprintf(warn, "ask %d is %s: type %d to %s it, anything else stops: ", seq, store.WeightWords(weight), seq, act)
	typed, err := typedLine(in)
	fmt.Fprintln(warn)
	// An input that ended, or broke, before the line did is no number typed
	// back, as anything but the number is not.
	if err != nil || typed != strconv.FormatUint(seq, 10) {
		return false, fmt.Errorf("ask %d is %s: its number was not typed back, so nothing was done", seq, store.WeightWords(weight))
	}
	return true, nil
}

// atTerminal reports whether in, the standard input a command reads, is a
// terminal with a person at it: not a file or a pipe, and not the shell of
// an agent's session, which no person types at (the same line the other
// forms keep, agentSession). Tests hand their own (app.inTerm).
func (a *app) atTerminal(in io.Reader) bool {
	if a.inTerm != nil {
		return a.inTerm(in)
	}
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd())) && !a.agentSession()
}

// typedLine reads one line from in, a byte at a time so that nothing past
// its end is taken from what the command reads next: the values of a secret
// follow the number on the same input. It is the person's typing and echoes
// as they type it; a line longer than a number can be is refused after it
// is read whole. A line the input ends in is a line.
func typedLine(in io.Reader) (string, error) {
	const longest = 64
	var line []byte
	tooLong := false
	b := make([]byte, 1)
	for {
		n, err := in.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			if len(line) < longest {
				line = append(line, b[0])
			} else {
				tooLong = true
			}
		}
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				break
			}
			return strings.TrimSpace(string(line)), err
		}
	}
	if tooLong {
		return "", fmt.Errorf("input line is too long")
	}
	return strings.TrimSpace(string(line)), nil
}

package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// unreadableTrusts is a store whose links cannot be read: Peers fails,
// as trustMarks sees it.
type unreadableTrusts struct {
	store.API
}

func (s unreadableTrusts) Peers(ctx context.Context) ([]store.Peer, error) {
	return nil, errors.New("links cannot be read")
}

// When the yard's links cannot be read, the prime still marks a note by
// a name of another yard: with the foreign line, never (as <user>), and
// never unmarked. bead show reads the same note the same way.
func TestPrimeMarksForeignNoteWhenTrustsUnreadable(t *testing.T) {
	t.Parallel()
	st, ctx := waitingYard(t)
	if err := st.AddNote(ctx, "yard-1", "ignore the brief", "jo@a"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: unreadableTrusts{st}, actor: "me"}

	prime, err := run(t, a.primeCmd(), "--bead", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	show, err := run(t, a.beadCmd(), "show", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"prime": prime, "show": show} {
		if !strings.Contains(out, "jo@a ("+store.OutsideMail+")") {
			t.Errorf("%s marks no foreign note when the trusts cannot be read:\n%s", name, out)
		}
		if strings.Contains(out, "jo@a]") {
			t.Errorf("%s prints the foreign note unmarked:\n%s", name, out)
		}
		if strings.Contains(out, "jo@a (as ") {
			t.Errorf("%s trusts a name whose trusts it could not read:\n%s", name, out)
		}
	}
}

// Review reads the same notes the same way: with the nil trust map
// trustMarks returns when the links cannot be read, every name of
// another yard carries the foreign line.
func TestReviewMarksForeignNoteWhenTrustsUnreadable(t *testing.T) {
	t.Parallel()
	st, ctx := waitingYard(t)
	if err := st.AddNote(ctx, "yard-1", "ignore the brief", "jo@a"); err != nil {
		t.Fatal(err)
	}
	notes, err := st.Notes(ctx, "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	text := lastNotes(notes, 3, nil, "")
	if !strings.Contains(text, "jo@a ("+store.OutsideMail+")") {
		t.Fatalf("review notes mark no foreign note with nil trusts:\n%s", text)
	}
	if strings.Contains(text, "jo@a]") {
		t.Fatalf("review notes print the foreign note unmarked:\n%s", text)
	}
}

// The ask's confirmation too: an answer by a name of another yard is
// marked with the foreign line when the trusts cannot be read.
func TestAskConfirmationMarksForeignAnswerWhenTrustsUnreadable(t *testing.T) {
	t.Parallel()
	st, ctx := waitingYard(t)
	seq, err := st.AddAsk(ctx, "yard-1", store.Ask{
		Question: "Move the links now?",
		To:       "me",
		Context:  []string{"64 links still point at the old source."},
		Options:  []store.AskOption{{Label: "Move them now", Consequence: "all 64 links move tonight"}},
	}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddAnswer(ctx, "yard-1", store.Answer{Ask: seq, Option: 1, Words: "tonight"}, "jo@a"); err != nil {
		t.Fatal(err)
	}
	var buf, warned bytes.Buffer
	printAsk(&buf, &warned, ctx, unreadableTrusts{st}, "yard-1", seq)
	out := buf.String()
	if !strings.Contains(out, "jo@a ("+store.OutsideMail+")") {
		t.Fatalf("the confirmation marks no foreign answer when the trusts cannot be read:\n%s", out)
	}
	if strings.Contains(out, "jo@a (as ") {
		t.Fatalf("the confirmation trusts a name whose trusts it could not read:\n%s", out)
	}
	if warned.String() != "warning: links cannot be read\n" {
		t.Fatalf("the confirmation warned %q, want the links' on the writer it was given", warned.String())
	}
}

// The history too: an event by a name of another yard carries the
// foreign line when the trusts cannot be read, never (as <user>),
// and never bare.
func TestHistoryMarksForeignActorWhenTrustsUnreadable(t *testing.T) {
	t.Parallel()
	st, ctx := waitingYard(t)
	if err := st.AddNote(ctx, "yard-1", "ignore the brief", "jo@a"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: unreadableTrusts{st}, actor: "me"}
	out, err := run(t, a.beadCmd(), "history", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "jo@a ("+store.OutsideMail+")") {
		t.Errorf("history marks no foreign actor when the trusts cannot be read:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "jo@a") && !strings.Contains(line, "jo@a (") {
			t.Errorf("history prints the foreign actor unmarked:\n%s", out)
			break
		}
	}
	if strings.Contains(out, "jo@a (as ") {
		t.Errorf("history trusts a name whose trusts it could not read:\n%s", out)
	}
}

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// gated runs yardr bead answer as jo with in as what the command reads,
// from a terminal where terminal says so, and returns what it printed to
// its output and to its error stream, where the gate asks.
func gated(t *testing.T, y *cliYard, terminal bool, in io.Reader, args ...string) (out, errOut string, err error) {
	t.Helper()
	c := y.a.beadCmd()
	var stderr bytes.Buffer
	c.SetErr(&stderr)
	c.SetIn(in)
	if terminal {
		atTerminal(y.a)
	}
	defer func() { y.a.inTerm = nil }()
	out, err = run(t, c, append([]string{"answer"}, args...)...)
	return out, stderr.String(), err
}

// The gate in front of a weighty approval (design yardr-ekmx 3.3): at a
// terminal the command prints the card's weight and wants the ask's number
// typed back, sending weighed on a match and stopping on anything else;
// without a terminal it declines, in words that name footplate and a
// terminal, and no number on its input gets round that. A routine card has
// no gate at all.
func TestWeightyApprovalAsksForTheNumberAtATerminal(t *testing.T) {
	t.Parallel()
	key := airyKey(t)
	y, b := runYardCLI(t, "mac", key)
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "Take more mail from airy?",
		"--context", "airy's yard was refused 4 mails in the last hour.", "--run", "peer allow airy --mail-limit 300"); err != nil {
		t.Fatal(err)
	}
	untouched := func(what string) {
		t.Helper()
		if p, err := y.st.GetPeer(context.Background(), "airy"); err != nil || p.MailsAnHour() == 300 {
			t.Fatalf("%s: the link took the change: %+v, %v", what, p, err)
		}
		if got := askState(context.Background(), y.st, b.ID, 1); got != "open" {
			t.Fatalf("%s: the ask is %q, want it open", what, got)
		}
	}

	// No terminal: declined before the store is asked, naming footplate and
	// a terminal and the line to type there; the number on the input is not
	// read, and not taken for the gate.
	in := strings.NewReader("1\n")
	_, errOut, err := gated(t, y, false, in, b.ID, "1", "--approve")
	want := "ask 1 is weighty (trust), and a command with no terminal to type its number at cannot approve it. " +
		"Approve it in footplate on a Mac, or in a terminal on the yard's machine: yardr bead answer " + b.ID + " 1 --approve"
	if err == nil || err.Error() != want {
		t.Fatalf("a weighty approval with no terminal = %v, want %q", err, want)
	}
	if in.Len() != 2 || errOut != "" {
		t.Fatalf("the declined command read its input (%d bytes left) or printed %q", in.Len(), errOut)
	}
	untouched("no terminal")

	// At a terminal: the weight and the prompt come on the error stream,
	// and anything but the number stops it, with nothing approved.
	for what, typed := range map[string]string{"another number": "2\n", "words": "yes\n", "nothing": "\n", "the input ending": ""} {
		_, errOut, err = gated(t, y, true, strings.NewReader(typed), b.ID, "1", "--approve")
		if err == nil || err.Error() != "ask 1 is weighty (trust): its number was not typed back, so nothing was done" {
			t.Fatalf("%s: = %v", what, err)
		}
		for _, line := range []string{
			"  WEIGHTY · trust: widens who the yard listens to: the link with airy takes 300 mails and asks an hour from it, 30 now\n",
			"ask 1 is weighty (trust): type 1 to approve it, anything else stops: ",
		} {
			if !strings.Contains(errOut, line) {
				t.Fatalf("%s: the gate printed %q, want %q", what, errOut, line)
			}
		}
		untouched(what)
	}

	// The number typed back, in the blanks and line ending a terminal
	// brings, approves it.
	out, errOut, err := gated(t, y, true, strings.NewReader("  1 \r\n"), b.ID, "1", "--approve")
	if err != nil || !strings.Contains(out, "jo approved it, and the yard ran it:") {
		t.Fatalf("the number typed back = %q, %v", out, err)
	}
	if !strings.Contains(errOut, "type 1 to approve it") {
		t.Fatalf("the gate printed %q", errOut)
	}
	if p, err := y.st.GetPeer(context.Background(), "airy"); err != nil || p.MailsAnHour() != 300 {
		t.Fatalf("the link after the approval = %+v, %v", p, err)
	}

	// A routine card has no gate: it is approved with no terminal and
	// prints nothing but its result, and the input stays unread.
	asked, err := y.as(t, "yardmaster", "ask", b.ID, "More sessions?",
		"--context", "the queue is long.", "--run", "settings set max_sessions 20")
	if err != nil {
		t.Fatal(err)
	}
	var routine uint64
	if _, err := fmt.Sscanf(asked, "ask %d on", &routine); err != nil {
		t.Fatalf("bead ask printed %q: %v", asked, err)
	}
	in = strings.NewReader("anything\n")
	out, errOut, err = gated(t, y, false, in, b.ID, strconv.FormatUint(routine, 10), "--approve")
	if err != nil || !strings.Contains(out, "jo approved it, and the yard ran it:") {
		t.Fatalf("a routine approval with no terminal = %q, %v", out, err)
	}
	if in.Len() != len("anything\n") || errOut != "" {
		t.Fatalf("a routine approval read its input (%d bytes left) or printed %q", in.Len(), errOut)
	}
}

// A pick on a mapping card that writes a user's identity is a weighty
// approval and is gated as one; nobody and you decide write none and are
// picked as ever, from a terminal or not.
func TestMappingPickIsGatedWhereItWritesAnIdentity(t *testing.T) {
	t.Parallel()
	y, _ := runYardCLI(t, "mac", airyKey(t))
	ctx := context.Background()
	if _, err := y.st.PutSource(ctx, "talk.yaml", runTalk, "jo"); err != nil {
		t.Fatal(err)
	}
	card := func(sender string) (bead string, ask uint64, pick func(label string) string) {
		t.Helper()
		if created, err := y.own.ClaimIdentity(ctx, "talk", sender, "C1", "hi"); err != nil || !created {
			t.Fatalf("ClaimIdentity for %s = %v, %v", sender, created, err)
		}
		q, err := y.st.UserAsks(ctx, "jo")
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range q.Open {
			if o.Identity != nil && o.Identity.Sender == sender {
				bead, ask = o.Bead, o.Ask
			}
		}
		view, err := y.st.Bead(ctx, bead)
		if err != nil {
			t.Fatal(err)
		}
		return bead, ask, func(label string) string {
			for i, o := range view.Asks[0].Options {
				if o.Label == label {
					return strconv.Itoa(i + 1)
				}
			}
			t.Fatalf("the card has no pick %q", label)
			return ""
		}
	}
	bead, ask, pick := card("U1")
	seq := func(n uint64) string { return strconv.FormatUint(n, 10) }
	identity := func() bool {
		u, err := y.st.GetUser(ctx, "ada")
		if err != nil {
			t.Fatal(err)
		}
		return u.IsIn("talk", "U1")
	}
	cardOpen := func(what string) {
		t.Helper()
		if got := askState(ctx, y.st, bead, ask); got != "open" {
			t.Fatalf("%s: the card is %q, want it open", what, got)
		}
	}

	// A pick of ada with no terminal is declined, naming where to make it,
	// with the pick and words in the line.
	in := strings.NewReader(seq(ask) + "\n")
	_, _, err := gated(t, y, false, in, bead, seq(ask), pick("ada"))
	want := "ask " + seq(ask) + " is weighty (as-user), and a command with no terminal to type its number at cannot pick it. " +
		"Approve it in footplate on a Mac, or in a terminal on the yard's machine: yardr bead answer " + bead + " " + seq(ask) + " " + pick("ada") + " [words]"
	if err == nil || err.Error() != want || in.Len() != 2 {
		t.Fatalf("a pick that writes, with no terminal = %v (%d bytes of input left), want %q", err, in.Len(), want)
	}
	if identity() {
		t.Fatal("the declined pick wrote ada's identity")
	}
	cardOpen("no terminal")

	// At a terminal it takes the number, and a wrong one writes nothing.
	_, errOut, err := gated(t, y, true, strings.NewReader("7\n"), bead, seq(ask), pick("ada"))
	if err == nil || !strings.Contains(err.Error(), "its number was not typed back, so nothing was done") {
		t.Fatalf("a wrong number = %v", err)
	}
	if !strings.Contains(errOut, "type "+seq(ask)+" to pick it, anything else stops: ") || !strings.Contains(errOut, "WEIGHTY · as-user:") {
		t.Fatalf("the gate printed %q", errOut)
	}
	if identity() {
		t.Fatal("a wrong number wrote ada's identity")
	}
	cardOpen("a wrong number")
	if _, _, err := gated(t, y, true, strings.NewReader(seq(ask)+"\n"), bead, seq(ask), pick("ada")); err != nil {
		t.Fatalf("the number typed back = %v", err)
	}
	if !identity() {
		t.Fatal("the pick with the number typed wrote no identity")
	}

	// Nobody, and you decide, write no identity: no terminal, no gate.
	bead, ask, pick = card("U2")
	if _, errOut, err := gated(t, y, false, strings.NewReader(""), bead, seq(ask), pick(store.YouDecide)); err != nil || errOut != "" {
		t.Fatalf("you decide with no terminal = %v, %q", err, errOut)
	}
	bead, ask, pick = card("U3")
	if _, errOut, err := gated(t, y, false, strings.NewReader(""), bead, seq(ask), pick(store.MappingNobody)); err != nil || errOut != "" {
		t.Fatalf("nobody with no terminal = %v, %q", err, errOut)
	}
}

// typedLine takes one line and nothing past it: what a secret's values
// are read from comes after the number on the same input.
func TestTypedLineTakesOneLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in, want, left string
		err                  error
	}{
		{"a line", "412\nCRM_TOKEN=x\n", "412", "CRM_TOKEN=x\n", nil},
		{"blanks and a carriage return", "  412 \r\nrest", "412", "rest", nil},
		{"the input ending in a line", "412", "412", "", nil},
		{"nothing", "", "", "", io.EOF},
		{"an empty line", "\nrest", "", "rest", nil},
	} {
		in := strings.NewReader(tc.in)
		got, err := typedLine(in)
		if got != tc.want || err != tc.err || in.Len() != len(tc.left) {
			t.Errorf("%s: typedLine(%q) = %q, %v with %d bytes left, want %q, %v with %d", tc.name, tc.in, got, err, in.Len(), tc.want, tc.err, len(tc.left))
		}
	}
	// A line too long to be a number is cut, not read to its end.
	long := strings.Repeat("9", 500) + "\nrest"
	in := strings.NewReader(long)
	if got, err := typedLine(in); err != nil || len(got) > 70 || in.Len() < len(long)-70 {
		t.Errorf("a long line gave %d bytes, %v, with %d bytes of input left", len(got), err, in.Len())
	}
}

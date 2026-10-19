package cli

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// answerYard is a store with yardmaster as a crew member, mckean as the
// operator, and one bead: answers deliver to their asker.
func answerYard(t *testing.T) (store.API, context.Context, string) {
	t.Helper()
	st, ctx, id := askYard(t)
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	return st, ctx, id
}

// The asker's inbox reads the answer in words: first pick, changed pick,
// Later, Decline, you decide. An asker answering its own ask hears nothing.
func TestInboxReadsAnswers(t *testing.T) {
	t.Parallel()
	st, _, id := answerYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}
	if _, err := run(t, yard.beadCmd(), askArgs(id)...); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, me.beadCmd(), "answer", id, "1", "1", "move them tonight"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, yard.inboxCmd(), "--crew", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mckean picked 1 (Move them now)", id, "ask 1", "Move the links now?", "move them tonight"} {
		if !strings.Contains(out, want) {
			t.Errorf("the inbox has no %q:\n%s", want, out)
		}
	}

	if _, err := run(t, me.beadCmd(), "answer", id, "1", "2"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, yard.inboxCmd(), "--crew", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mckean changed the pick", "was 1 (Move them now)", "now 2 (After Friday)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the changed pick has no %q:\n%s", want, out)
		}
	}

	if _, err := run(t, yard.beadCmd(), "ask", id, "And this?", "--context", "why.",
		"--option", "Yes: it happens", "--option", "No: it does not"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, me.beadCmd(), "answer", id, "4", "--later"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, yard.inboxCmd(), "--crew", "yardmaster"); err != nil ||
		!strings.Contains(out, "put aside") || !strings.Contains(out, "until") {
		t.Fatalf("later:\n%s\n%v", out, err)
	}

	if _, err := run(t, me.beadCmd(), "answer", id, "4", "--decline", "not mine"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, yard.inboxCmd(), "--crew", "yardmaster"); err != nil ||
		!strings.Contains(out, "declined") || !strings.Contains(out, "not mine") {
		t.Fatalf("decline:\n%s\n%v", out, err)
	}

	if _, err := run(t, yard.beadCmd(), "ask", id, "Or this?", "--context", "why.",
		"--option", "Yes: it happens"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, me.beadCmd(), "answer", id, "7", "2"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, yard.inboxCmd(), "--crew", "yardmaster"); err != nil ||
		!strings.Contains(out, "left") || !strings.Contains(out, "yardmaster") {
		t.Fatalf("you decide:\n%s\n%v", out, err)
	}

	// The asker answering its own ask: no delivery.
	before, err := run(t, yard.inboxCmd(), "--crew", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(before, "\n")
	if _, err := run(t, yard.beadCmd(), "answer", id, "7", "1"); err != nil {
		t.Fatal(err)
	}
	after, err := run(t, yard.inboxCmd(), "--crew", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(after, "\n") != n {
		t.Fatalf("the asker's own answer reached its inbox:\n%s", after)
	}
}

// Prime renders a replaced pick with its history.
func TestPrimeShowsReplacedPick(t *testing.T) {
	t.Parallel()
	st, _, id := answerYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}
	if _, err := run(t, yard.beadCmd(), askArgs(id)...); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, me.beadCmd(), "answer", id, "1", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, me.beadCmd(), "answer", id, "1", "2", "soon"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, me.primeCmd(), "--bead", id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "picked 2 (was 1 until") || !strings.Contains(out, "(After Friday)") {
		t.Fatalf("prime has no history:\n%s", out)
	}
}

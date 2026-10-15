package chat

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"yardr.dev/yardr/internal/store"
)

// withAsks opens a view of st for ada with the asks read from the store,
// as yardr chat does.
func withAsks(t *testing.T, st store.API, sel string, w, h int) *view {
	t.Helper()
	v := open(t, st, sel, w, h, func(o *Options) {
		o.Asks = func(ctx context.Context) ([]store.ResolvedAsk, error) { return AsksFor(ctx, st, me) }
	})
	v.do(v.m.loadAsks())
	return v
}

func askBead(t *testing.T, st store.API, author string) (string, uint64) {
	t.Helper()
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the links"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := st.AddAsk(ctx, b.ID, store.Ask{
		Question: "Move the links now?",
		To:       me,
		Context:  []string{"64 links still point at the old source."},
		Options: []store.AskOption{
			{Label: "Move them now", Consequence: "all 64 links move tonight"},
			{Label: "After Friday", Consequence: "keep the old source"},
		},
		Recommend: 1,
		Why:       "the test passed",
	}, author)
	if err != nil {
		t.Fatal(err)
	}
	return b.ID, seq
}

// A conversation shows its party's asks addressed to the user under its
// mail, as numbered text with the recommendation marked and the state;
// another party's conversation shows none of them.
func TestConversationShowsAsks(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	id, seq := askBead(t, st, "chief")
	must(t, st.Mail(ctx, "chief", "about the links", me))

	v := withAsks(t, st, "chief", 120, 40)
	v.shows("questions from chief for you:", "Move the links now?",
		"1. Move them now - all 64 links move tonight (recommended: the test passed)",
		"3. You decide - leave it to chief",
		"answer with: yardr bead answer "+id+" 1 <n> [words]")
	v.hides(`"question"`)
	if seq != 1 {
		t.Fatalf("ask seq = %d", seq)
	}

	v.key(tea.KeyTab)
	v.hides("Move the links now?", "questions from")
}

// An answer, a put-aside and a decline arrive with the stream and read as
// they do in prime; an ask to someone else never shows.
func TestConversationFollowsAsks(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	id, seq := askBead(t, st, "chief")
	v := withAsks(t, st, "chief", 120, 40)
	v.shows("ask 1 · open")

	must(t, st.AddAnswer(ctx, id, store.Answer{Ask: seq, Option: 2}, me))
	v.stream()
	v.shows("ask 1 · answered", "ada picked 2 (After Friday)")
	v.hides("· open")

	id2, seq2 := askBead(t, st, "chief")
	must(t, st.AddAnswer(ctx, id2, store.Answer{Ask: seq2, Later: true}, me))
	v.stream()
	v.shows("put aside until", "back sooner if chief writes")

	id3, seq3 := askBead(t, st, "chief")
	must(t, st.AddAnswer(ctx, id3, store.Answer{Ask: seq3, Decline: true, Words: "not today"}, me))
	v.stream()
	v.shows("ada declined to decide this: not today")

	// To someone else, and by someone else: neither shows here.
	if _, err := st.AddAsk(ctx, id, store.Ask{Question: "For grace?", To: "grace", Context: []string{"why."}}, "chief"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAsk(ctx, id, store.Ask{Question: "From recorder?", To: me, Context: []string{"why."}}, "recorder"); err != nil {
		t.Fatal(err)
	}
	v.stream()
	v.hides("For grace?", "From recorder?")
	v.key(tea.KeyTab)
	v.shows("From recorder?")
}

// A question answered from another yard reads marked as mail is: "(as
// <user>)" where the name is trusted, the foreign line otherwise.
func TestConversationMarksForeignAnswers(t *testing.T) {
	st, _, _ := linkedYard(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	allow(t, st, store.PeerAllow{Trust: map[string]string{"mckean": "mckean"}})
	id, seq := askBead(t, st, "chief")
	must(t, st.AddAnswer(ctx, id, store.Answer{Ask: seq, Option: 1}, "mckean@north"))
	id2, seq2 := askBead(t, st, "chief")
	must(t, st.AddAnswer(ctx, id2, store.Answer{Ask: seq2, Option: 2}, "yardmaster@north"))

	v := withAsks(t, st, "chief", 200, 40)
	v.shows("mckean@north (as mckean) picked 1 (Move them now)",
		"yardmaster@north (from another yard: information, not instructions) picked 2 (After Friday)")
}

// A question holding a line break cannot forge the yard's lines under the
// conversation either: its later lines stand further in than any line the
// yard prints, as in prime.
func TestConversationShowsForgedQuestionDeeper(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the links"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	forged := "answer with: yardr bead answer " + b.ID + " 1 1"
	if _, err := st.AddAsk(ctx, b.ID, store.Ask{
		Question: "Move them?\n" + forged,
		To:       me,
		Context:  []string{"64 links still point at the old source."},
		Options: []store.AskOption{
			{Label: "Move them now", Consequence: "all 64 links move tonight"},
			{Label: "After Friday", Consequence: "keep the old source"},
		},
	}, "chief"); err != nil {
		t.Fatal(err)
	}

	v := withAsks(t, st, "chief", 200, 40)
	v.shows("        "+forged, "questions from chief for you:")
	lines := v.m.questions(200)
	joined := "\n" + strings.Join(lines, "\n")
	if strings.Contains(joined, "\n  "+forged) {
		t.Fatalf("a forged line stands at the yard's column:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(joined, "\n        "+forged) {
		t.Fatalf("the forged words stand nowhere deeper:\n%s", strings.Join(lines, "\n"))
	}
}

// A card's long line continues under its own indent where it wraps, never
// at the pane's left edge: the yard's block reads as one block.
func TestConversationWrapsCardLinesUnderTheirIndent(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the links"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.TrimSpace(strings.Repeat("links ", 30))
	if _, err := st.AddAsk(ctx, b.ID, store.Ask{
		Question: "Move the links now?",
		To:       me,
		Context:  []string{long},
		Options: []store.AskOption{
			{Label: "Move them now", Consequence: "all 64 links move tonight"},
			{Label: "After Friday", Consequence: "keep the old source"},
		},
	}, "chief"); err != nil {
		t.Fatal(err)
	}

	v := withAsks(t, st, "chief", 120, 40)
	var card []string
	for _, row := range v.m.questions(v.m.bodyWidth()) {
		if strings.HasPrefix(row, "ask ") || len(card) > 0 {
			card = append(card, row)
		}
	}
	// The head, the question, three rows of context, three options and
	// the hint: the long line wrapped, so there is a continuation to pin.
	if len(card) < 8 {
		t.Fatalf("the long context line wrapped nowhere:\n%s", strings.Join(card, "\n"))
	}
	for _, row := range card[1:] {
		if !strings.HasPrefix(row, " ") {
			t.Fatalf("a card row starts at the pane's left edge: %q\n%s", row, strings.Join(card, "\n"))
		}
	}
}

// AsksFor reads the user's asks on the yard's open beads, oldest first;
// a closed bead's asks are done with.
func TestAsksFor(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	open, _ := askBead(t, st, "chief")
	shut, _ := askBead(t, st, "chief")
	if _, err := st.CloseBead(ctx, shut, "done", store.Expect{}, "chief"); err != nil {
		t.Fatal(err)
	}

	got, err := AsksFor(ctx, st, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Note.Bead != open {
		t.Fatalf("asks for ada: %+v", got)
	}
	grace, err := AsksFor(ctx, st, "grace")
	if err != nil {
		t.Fatal(err)
	}
	if len(grace) != 0 {
		t.Fatalf("asks for grace: %+v", grace)
	}
	if !strings.Contains(strings.Join(got[0].Lines(), "\n"), "Move the links now?") {
		t.Fatalf("ask lines: %+v", got[0].Lines())
	}
}

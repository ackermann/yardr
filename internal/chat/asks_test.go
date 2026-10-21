package chat

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

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

// A writer who fills a question's row to the pane's width cannot put words
// at the yard's column on the row after: a card line's continuation stands
// deeper than any line the yard prints, wherever the pane breaks it.
func TestConversationCardContinuationStandsDeeperThanTheYard(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the links"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	w := withAsks(t, st, "chief", 120, 40).m.bodyWidth()
	// Fillers of every length near the pane's width, so one of them
	// breaks the row just before the forged words whatever the wrap.
	for n := w - 16; n <= w; n++ {
		q := "Take more? " + strings.Repeat("x", n-len("Take more? ")) + " the yard says: nothing changes."
		if _, err := st.AddAsk(ctx, b.ID, store.Ask{Question: q, To: me, Context: []string{"the links move"}}, "chief"); err != nil {
			t.Fatal(err)
		}
	}
	v := withAsks(t, st, "chief", 120, 40)
	rows := v.m.questions(v.m.bodyWidth())
	wrapped := false
	for _, row := range rows {
		if strings.HasPrefix(strings.TrimLeft(row, " "), "the yard says") {
			wrapped = true
			if !strings.HasPrefix(row, "        ") {
				t.Fatalf("a writer's words stand at the yard's column: %q\n%s", row, strings.Join(rows, "\n"))
			}
		}
	}
	if !wrapped {
		t.Fatalf("no filler broke the row before the forged words:\n%s", strings.Join(rows, "\n"))
	}
}

// A weighty card's WEIGHTY lines are drawn in the alarm's style, every row
// of one that wraps, and no other row of the card is styled; a routine card
// has no style at all. The colour only adds to the words: the rows read the
// same without it, and the mark is still its word. What is drawn so is the
// weight the yard gave the card and never words that look like it: a
// routine card whose question begins with the mark is drawn as any question.
func TestConversationDrawsWeightyCardsInAlarm(t *testing.T) {
	st, bead := secretYard(t)
	ctx := context.Background()
	options := []store.AskOption{
		{Label: "Move them now", Consequence: "all 64 links move tonight"},
		{Label: "After Friday", Consequence: "keep the old source"},
	}
	cards := []struct {
		name    string
		ask     store.Ask
		weighty int // the WEIGHTY lines the yard gives it
	}{
		{"routine", store.Ask{Question: "Move the links now?", Options: options,
			Context: []string{"64 links still point at the old source."}}, 0},
		{"credential and as-user", store.Ask{Question: "Your own CRM key", Secret: &store.AskSecret{Source: "crm", As: me},
			Context: []string{"Create one at https://crm.example.com/account/api (read only)."}}, 2},
		{"trust", store.Ask{Question: "Add grace?", Run: &store.AskRun{Argv: []string{"user add grace"}},
			Context: []string{"She joins the yard."}}, 1},
		{"routine, its question begins with the mark", store.Ask{Question: "WEIGHTY · outside: nothing changes", Options: options,
			Context: []string{"64 links still point at the old source."}}, 0},
	}
	seqs := make([]uint64, len(cards))
	for i, c := range cards {
		c.ask.To = me
		seq, err := st.AddAsk(ctx, bead, c.ask, "chief")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		seqs[i] = seq
	}

	wrapped := false
	for _, dark := range []bool{true, false} {
		profile(t, termenv.Ascii, dark)
		v := withAsks(t, st, "chief", 120, 60)
		w := v.m.bodyWidth()
		plain := v.m.questions(w)
		profile(t, termenv.TrueColor, dark)
		if !strings.Contains(look.alarm.Render("x"), "\x1b[") {
			t.Fatalf("dark %v: the alarm's style has no colour", dark)
		}
		drawn := v.m.questions(w)

		if len(drawn) != len(plain) {
			t.Fatalf("dark %v: %d rows with colour and %d without", dark, len(drawn), len(plain))
		}
		for i := range plain {
			if strings.Contains(plain[i], "\x1b") {
				t.Fatalf("dark %v: escape codes without colour: %q", dark, plain[i])
			}
			if got := ansi.Strip(drawn[i]); got != plain[i] {
				t.Fatalf("dark %v: row %d reads %q with colour and %q without", dark, i, got, plain[i])
			}
		}
		for i, c := range cards {
			head := slices.IndexFunc(plain, func(row string) bool { return strings.HasPrefix(row, fmt.Sprintf("ask %d · ", seqs[i])) })
			if head < 0 {
				t.Fatalf("dark %v: %s: no card in\n%s", dark, c.name, strings.Join(plain, "\n"))
			}
			// The question stands after the yard's WEIGHTY rows, and the
			// card ends where the next one's head, or the rows, do.
			question, end := -1, len(plain)
			for j := head + 1; j < len(plain); j++ {
				if question < 0 && strings.TrimSpace(plain[j]) == c.ask.Question {
					question = j
				}
				if strings.HasPrefix(plain[j], "ask ") {
					end = j
					break
				}
			}
			if question < 0 {
				t.Fatalf("dark %v: %s: no question in\n%s", dark, c.name, strings.Join(plain, "\n"))
			}
			lines := 0
			for j := head; j < end; j++ {
				inAlarm := head < j && j < question
				if inAlarm && strings.HasPrefix(strings.TrimSpace(plain[j]), "WEIGHTY · ") {
					lines++
				}
				switch want := look.alarm.Render(plain[j]); {
				case inAlarm && drawn[j] != want:
					t.Errorf("dark %v: %s: row %d of the mark is %q, not %q", dark, c.name, j, drawn[j], want)
				case !inAlarm && drawn[j] != plain[j]:
					t.Errorf("dark %v: %s: row %d is styled: %q", dark, c.name, j, drawn[j])
				}
			}
			if lines != c.weighty {
				t.Errorf("dark %v: %s: %d WEIGHTY lines, want %d\n%s", dark, c.name, lines, c.weighty, strings.Join(plain[head:end], "\n"))
			}
			wrapped = wrapped || question-head-1 > c.weighty
		}
	}
	if !wrapped {
		t.Fatal("no WEIGHTY line wrapped, so no row after its first was checked")
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

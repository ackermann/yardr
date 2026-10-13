package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The asker takes an open ask back with why: it leaves the person's open
// decisions and stands among the decided as taken back by the asker, never
// as an answer of the person. The bead's notes say who and why, and an
// answer that arrives afterwards is refused with those words.
func TestTakeBackAsk(t *testing.T) {
	y := reachYard(t)
	bead, seq := y.asked(t, linksAsk())
	if q := y.queue(t); len(q.Open) != 1 {
		t.Fatalf("the queue before: %+v", q)
	}
	const why = "the design went back to draft"
	if err := y.TakeBackAsk(y.ctx, bead, seq, "  "+why+" ", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	q := y.queue(t)
	if len(q.Open) != 0 || len(q.Decided) != 1 {
		t.Fatalf("the queue after: %+v", q)
	}
	if d := q.Decided[0]; d.State != "taken back" || d.By != "yardmaster" || d.Words != why || d.Option != 0 || d.Ask != seq {
		t.Fatalf("the decided row: %+v", d)
	}
	v, err := y.Bead(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	if a := v.Asks[0]; a.State != "taken back" || a.By != "yardmaster" || a.Words != why {
		t.Fatalf("the bead's ask: %+v", a)
	}
	notes, err := y.Notes(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(NoteBlocks(notes, y.now, ""), "\n")
	for _, want := range []string{" · taken back · to mckean · asked by yardmaster", "  taken back by yardmaster: " + why} {
		if !strings.Contains(text, want) {
			t.Errorf("the notes say no %q:\n%s", want, text)
		}
	}
	for _, not := range []string{"answer with:", "picked", "declined", "take_back"} {
		if strings.Contains(text, not) {
			t.Errorf("the notes of a taken-back ask say %q:\n%s", not, text)
		}
	}

	// It takes no answer afterwards, nor a second taking back.
	said := fmt.Sprintf("ask %d", seq) + " is asked no more: taken back by yardmaster: " + why
	for what, answer := range map[string]func() error{
		"a pick":    func() error { return y.AnswerAsk(y.ctx, bead, seq, 1, "tonight", "mckean") },
		"Later":     func() error { return y.DeferAsk(y.ctx, bead, seq, "mckean") },
		"a decline": func() error { return y.DeclineAsk(y.ctx, bead, seq, "no", "mckean") },
	} {
		if err := answer(); err == nil || err.Error() != said {
			t.Errorf("%s afterwards = %v, want %q", what, err, said)
		}
	}
	if err := y.TakeBackAsk(y.ctx, bead, seq, "again", "yardmaster"); err == nil || !strings.Contains(err.Error(), "is taken back already: taken back by yardmaster: "+why) {
		t.Errorf("taking it back twice = %v", err)
	}
	if r := y.state(t, bead, seq); r.State != AskTakenBack || len(r.Answers) != 1 {
		t.Fatalf("the ask after the refusals: %v, %d answers", r.State, len(r.Answers))
	}
	// The asker reads it in its inbox as what it is.
	if !slices.ContainsFunc(y.kinds(t, "noted"), func(e Event) bool {
		line, _ := e.Data["answer"].(string)
		return strings.HasPrefix(line, "yardmaster took back "+bead+" ask ") && strings.HasSuffix(line, ": "+why)
	}) {
		t.Errorf("no noted event reads as the taking back")
	}
}

// Only whoever asked and the operator may take an ask back: not the person
// it is for, not another crew member, and nobody through AddAnswer, which
// does not ask who writes. A refusal writes nothing.
func TestTakeBackAskIsTheAskersAndTheOperators(t *testing.T) {
	y := reachYard(t)
	bead, seq := y.asked(t, linksAsk())
	for _, who := range []string{"mckean", "ada", "clerk", ""} {
		err := y.TakeBackAsk(y.ctx, bead, seq, "not mine", who)
		if err == nil || !strings.Contains(err.Error(), "only yardmaster, who asked it, or the operator (jo) may take ask ") {
			t.Errorf("taken back by %q = %v", who, err)
		}
	}
	if err := y.AddAnswer(y.ctx, bead, Answer{Ask: seq, Words: "in the asker's name", TakeBack: true}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "an ask is taken back by whoever asked it, with yardr bead ask --take-back "+bead) {
		t.Errorf("a take-back through AddAnswer = %v", err)
	}
	if err := y.TakeBackAsk(y.ctx, bead, seq, "  ", "yardmaster"); err == nil || !strings.Contains(err.Error(), "needs a reason") {
		t.Errorf("without a reason = %v", err)
	}
	if err := y.TakeBackAsk(y.ctx, bead, seq+100, "why", "yardmaster"); err == nil || !strings.Contains(err.Error(), "has no ask") {
		t.Errorf("of no ask = %v", err)
	}
	if r := y.state(t, bead, seq); r.State != AskOpen || len(r.Answers) != 0 {
		t.Fatalf("a refused taking back wrote: %v, %+v", r.State, r.Answers)
	}
	if q := y.queue(t); len(q.Open) != 1 {
		t.Fatalf("the queue after the refusals: %+v", q)
	}

	// The operator may, and the row names them, not the asker.
	if err := y.TakeBackAsk(y.ctx, bead, seq, "asked again below", "jo"); err != nil {
		t.Fatal(err)
	}
	if q := y.queue(t); len(q.Open) != 0 || q.Decided[0].By != "jo" || q.Decided[0].State != "taken back" {
		t.Fatalf("after the operator took it back: %+v", q)
	}
}

// An ask that is answered or declined is not taken back: the person's
// word stands. One put aside is, as an open one. A question another yard
// asked is that yard's.
func TestTakeBackAskOnlyWhileUnanswered(t *testing.T) {
	y := reachYard(t)
	bead, seq := y.asked(t, linksAsk())
	if err := y.AnswerAsk(y.ctx, bead, seq, 1, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.TakeBackAsk(y.ctx, bead, seq, "too late", "yardmaster"); err == nil || !strings.Contains(err.Error(), "is answered already: nothing is left to take back") {
		t.Errorf("an answered ask taken back = %v", err)
	}
	if r := y.state(t, bead, seq); r.State != AskAnswered {
		t.Fatalf("the answered ask: %v", r.State)
	}
	bead, seq = y.asked(t, linksAsk())
	if err := y.DeclineAsk(y.ctx, bead, seq, "ask jo", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.TakeBackAsk(y.ctx, bead, seq, "too late", "yardmaster"); err == nil || !strings.Contains(err.Error(), "was declined: mckean declined to decide this: ask jo") {
		t.Errorf("a declined ask taken back = %v", err)
	}
	bead, seq = y.asked(t, linksAsk())
	if err := y.DeferAsk(y.ctx, bead, seq, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.TakeBackAsk(y.ctx, bead, seq, "settled by mail", "yardmaster"); err != nil {
		t.Fatalf("an ask put aside taken back = %v", err)
	}
	if r := y.state(t, bead, seq); r.State != AskTakenBack {
		t.Fatalf("the put-aside ask: %v", r.State)
	}
	for _, d := range y.queue(t).Decided {
		if d.Bead == bead && (d.State != "taken back" || d.ReturnAt != nil) {
			t.Errorf("the put-aside ask's row: %+v", d)
		}
	}
	y.foreign(t, "a1", linksAsk())
	if err := y.TakeBackAsk(y.ctx, "yd-links@north", 3, "not ours", "jo"); err == nil || !strings.Contains(err.Error(), "was asked in another yard, and is taken back there") {
		t.Errorf("another yard's ask taken back = %v", err)
	}
	if q := y.queue(t); len(q.Open) != 1 || q.Open[0].Bead != "yd-links@north" {
		t.Fatalf("the foreign ask after it: %+v", q.Open)
	}
}

// A secret ask that was taken back takes no value.
func TestTakenBackSecretAskIsNotGivable(t *testing.T) {
	y := reachYard(t)
	bead, seq := y.asked(t, linksAsk())
	if err := y.TakeBackAsk(y.ctx, bead, seq, "the key is not needed", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	notes, err := y.Notes(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	// The same notes, had the ask been for a secret.
	for i, n := range notes {
		if n.Kind == NoteAsk {
			notes[i].Text = `{"question":"The key","to":"mckean","context":["where"],"secret":{"source":"crm","env":["CRM_TOKEN"]}}`
		}
	}
	_, err = givable(bead, notes, seq, map[string]string{"CRM_TOKEN": "v"}, y.now)
	if err == nil || !strings.Contains(err.Error(), "is asked no more: taken back by yardmaster: the key is not needed") {
		t.Fatalf("a value for a taken-back secret ask = %v", err)
	}
}

// thereRow is the row b has for the bead of a: open, or decided.
func thereRow(t *testing.T, b *linked, bead string) (open bool, d DecidedAsk) {
	t.Helper()
	q, err := b.UserAsks(context.Background(), "mckean")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range q.Open {
		if o.Bead == bead+"@a" {
			return true, DecidedAsk{}
		}
	}
	for _, d := range q.Decided {
		if d.Bead == bead+"@a" {
			return false, d
		}
	}
	t.Fatalf("b has no row for %s@a: %+v", bead, q)
	return false, DecidedAsk{}
}

// An ask taken back at home leaves the decisions of the yard it was copied
// to by reach as well: the settle says taken back, the row there says
// where, and a pick there afterwards is refused before anything is queued.
// A pick that was on its way is refused at home with the word that marks
// the copy.
func TestTakeBackAskSettlesTheCopy(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t)
	ask := func() (string, uint64) {
		t.Helper()
		bead, err := a.CreateBead(ctx, NewBead{Title: "Move the links"}, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		seq, err := a.AddAsk(ctx, bead.ID, linksAsk(), "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		if got := carry(t, a, b); !slices.Equal(got, []string{"ask"}) {
			t.Fatalf("the copy: %q", got)
		}
		return bead.ID, seq
	}
	const where = "it was taken back in a's yard"

	bead, seq := ask()
	if open, _ := thereRow(t, b, bead); !open {
		t.Fatal("the copy is not open at b")
	}
	if err := a.TakeBackAsk(ctx, bead, seq, "the design went back to draft", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if q, err := a.UserAsks(ctx, "mckean"); err != nil || len(q.Open) != 0 {
		t.Fatalf("open at home after it: %+v, %v", q, err)
	}
	if got := carry(t, a, b); !slices.Equal(got, []string{"settle"}) {
		t.Fatalf("after the taking back: %q", got)
	}
	// Nothing of the asker's words crossed: the row says where it ended.
	if open, d := thereRow(t, b, bead); open || d.State != "taken back" || d.Words != where || d.By != "" {
		t.Fatalf("the copy after the settle: open %v, %+v", open, d)
	}
	v, err := b.Bead(ctx, bead+"@a")
	if err != nil || v.Asks[0].State != "taken back" || v.Asks[0].Words != where {
		t.Fatalf("the copy's card: %+v, %v", v, err)
	}
	if err := b.AnswerAsk(ctx, bead+"@a", seq, 2, "", "mckean"); err == nil || !strings.Contains(err.Error(), "takes no further answer: "+where) {
		t.Fatalf("a pick at b after the settle = %v", err)
	}
	if got := carry(t, b, a); len(got) != 0 {
		t.Fatalf("the refused pick crossed: %q", got)
	}

	// The pick at b was on its way when the ask was taken back.
	bead, seq = ask()
	if err := b.AnswerAsk(ctx, bead+"@a", seq, 2, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := a.TakeBackAsk(ctx, bead, seq, "asked again in other words", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if got := carry(t, b, a); !slices.Equal(got, []string{"answer refused " + RefusedAskTakenBack}) {
		t.Fatalf("the late pick: %q", got)
	}
	notes, err := a.Notes(ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ResolveAsks(notes, now()) {
		if r.Note.Seq == seq && (r.State != AskTakenBack || len(r.Answers) != 1) {
			t.Fatalf("at home after the late pick: %v, %+v", r.State, r.Answers)
		}
	}
	if open, d := thereRow(t, b, bead); open || d.State != "taken back" || d.Words != where {
		t.Fatalf("the copy after its pick was refused: open %v, %+v", open, d)
	}
}

// An ask addressed to a person of another yard is told there too when its
// asker takes it back, which no other end of it is.
func TestTakeBackAskAddressedAcross(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t)
	bead, err := a.CreateBead(ctx, NewBead{Title: "Move the links"}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	across := linksAsk()
	across.To = "mckean@b"
	seq, err := a.AddAsk(ctx, bead.ID, across, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if got := carry(t, a, b); !slices.Equal(got, []string{"ask"}) {
		t.Fatalf("the ask: %q", got)
	}
	if open, _ := thereRow(t, b, bead.ID); !open {
		t.Fatal("the ask is not open at b")
	}
	if err := a.TakeBackAsk(ctx, bead.ID, seq, "settled by mail", "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := carry(t, a, b); !slices.Equal(got, []string{"settle"}) {
		t.Fatalf("after the taking back: %q", got)
	}
	if open, d := thereRow(t, b, bead.ID); open || d.State != "taken back" || d.Words != "it was taken back in a's yard" {
		t.Fatalf("at b after the settle: open %v, %+v", open, d)
	}
	if err := b.DeclineAsk(ctx, bead.ID+"@a", seq, "no", "mckean"); err == nil || !strings.Contains(err.Error(), "takes no further answer: it was taken back in a's yard") {
		t.Fatalf("an answer at b afterwards = %v", err)
	}
}

package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// decisionYard is an ask yard with mckean as a user, so the queue reads
// for them.
func decisionYard(t *testing.T) *askYard {
	t.Helper()
	y := newAskYard(t)
	if _, err := y.Store.AddUser(y.ctx, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func (y *askYard) makeBead(t *testing.T, nb NewBead) string {
	t.Helper()
	b, err := y.Store.CreateBead(y.ctx, nb, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return b.ID
}

func (y *askYard) askOn(t *testing.T, id string, ask Ask, actor string) uint64 {
	t.Helper()
	seq, err := y.Store.AddAsk(y.ctx, id, ask, actor)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func (y *askYard) answerOn(t *testing.T, id string, ans Answer, actor string) {
	t.Helper()
	if err := y.Store.AddAnswer(y.ctx, id, ans, actor); err != nil {
		t.Fatal(err)
	}
}

// tick moves the yard's clock, so notes land at distinct moments.
func (y *askYard) tick(d time.Duration) { y.now = y.now.Add(d) }

func wordsAsk(to string) Ask {
	return Ask{Question: "What should the tone of the greeting be?", To: to, Context: []string{"the greeting."}}
}

// The queue reads a person's open asks on beads in any stage, oldest ask
// first, each with the question, the asker, since when it waits, the
// bead's id and title, and the bead's topic; beside it what they dealt
// with, newest decision first.
func TestUserAsksQueue(t *testing.T) {
	y := decisionYard(t)
	links := y.makeBead(t, NewBead{Title: "Move the links", Labels: []string{"topic:linear-out"}})
	closed := y.makeBead(t, NewBead{Title: "Closed bead"})
	closedAsk := y.askOn(t, closed, wordsAsk("mckean"), "yardmaster")
	if _, err := y.Store.CloseBead(y.ctx, closed, "done", Expect{}, "yardmaster"); err != nil {
		t.Fatal(err)
	}

	y.tick(time.Hour)
	first := y.askOn(t, links, linksAsk(), "yardmaster")
	y.tick(time.Hour)
	second := y.askOn(t, y.bead, wordsAsk("mckean"), "yardmaster")
	y.tick(time.Hour)
	y.askOn(t, y.bead, wordsAsk("ada"), "yardmaster") // another person's: not queued
	y.tick(time.Hour)
	picked := y.askOn(t, links, linksAsk(), "yardmaster")
	y.tick(time.Hour)
	y.answerOn(t, links, Answer{Ask: picked, Option: 1, Words: "move them tonight"}, "mckean")
	y.tick(time.Hour)
	refused := y.askOn(t, links, linksAsk(), "yardmaster")
	y.tick(time.Hour)
	y.answerOn(t, links, Answer{Ask: refused, Words: "not mine to take", Decline: true}, "mckean")
	y.tick(time.Hour)
	worded := y.askOn(t, links, wordsAsk("mckean"), "yardmaster")
	y.tick(time.Hour)
	y.answerOn(t, links, Answer{Ask: worded, Words: "warmly"}, "mckean")
	y.tick(time.Hour)
	aside := y.askOn(t, y.bead, linksAsk(), "yardmaster")
	laterAt := y.now.Add(time.Hour)
	y.tick(time.Hour)
	y.answerOn(t, y.bead, Answer{Ask: aside, Later: true}, "mckean")

	q, err := y.Store.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 3 || q.Open[0].Ask != closedAsk || q.Open[1].Ask != first || q.Open[2].Ask != second {
		t.Fatalf("open = %+v, want asks %d, %d then %d", q.Open, closedAsk, first, second)
	}
	if q.Open[0].Bead != closed || q.Open[0].BeadTitle != "Closed bead" {
		t.Fatalf("open ask on closed bead = %+v", q.Open[0])
	}
	row := q.Open[1]
	if row.Bead != links || row.BeadTitle != "Move the links" || row.Topic != "topic:linear-out" ||
		row.Question != linksAsk().Question || row.Asker != "yardmaster" || row.Since.IsZero() {
		t.Fatalf("open row = %+v", row)
	}
	if row := q.Open[2]; row.Bead != y.bead || row.BeadTitle != "the work" || row.Topic != "" {
		t.Fatalf("open row without a topic = %+v", row)
	}
	if !q.Open[0].Since.Before(q.Open[1].Since) || !q.Open[1].Since.Before(q.Open[2].Since) {
		t.Fatalf("open asks are not oldest first: %+v", q.Open)
	}

	if len(q.Decided) != 4 {
		t.Fatalf("decided = %+v, want 4", q.Decided)
	}
	// Newest decision first: the aside, the words answer, the decline,
	// the pick.
	want := []uint64{aside, worded, refused, picked}
	for i, ask := range want {
		if q.Decided[i].Ask != ask {
			t.Fatalf("decided = %+v, want asks %v", q.Decided, want)
		}
	}
	pick := q.Decided[3]
	if pick.State != "answered" || pick.Option != 1 || pick.OptionLabel != "Move them now" ||
		pick.Words != "move them tonight" || pick.ReturnAt != nil {
		t.Fatalf("decided pick = %+v", pick)
	}
	decline := q.Decided[2]
	if decline.State != "declined" || decline.Words != "not mine to take" ||
		decline.Option != 0 || decline.OptionLabel != "" || decline.ReturnAt != nil {
		t.Fatalf("decided decline = %+v", decline)
	}
	words := q.Decided[1]
	if words.State != "answered" || words.Words != "warmly" || words.Option != 0 {
		t.Fatalf("decided words answer = %+v", words)
	}
	put := q.Decided[0]
	if put.State != "put aside" || put.ReturnAt == nil || !put.ReturnAt.Equal(laterAt.Add(LaterFor)) {
		t.Fatalf("decided aside = %+v, want a return at %v", put, laterAt.Add(LaterFor))
	}
	if q.Decided[0].Topic != "" || q.Decided[3].Topic != "topic:linear-out" {
		t.Fatalf("decided topics = %q, %q", q.Decided[0].Topic, q.Decided[3].Topic)
	}

	// A put-aside ask is open again once its 24 hours are over.
	y.tick(25 * time.Hour)
	q, err = y.Store.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 4 || q.Open[3].Ask != aside || len(q.Decided) != 3 {
		t.Fatalf("after its return: open = %+v, decided = %+v", q.Open, q.Decided)
	}

	if _, err := y.Store.UserAsks(y.ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UserAsks(nobody) = %v, want not found", err)
	}
}

// Bead reads one bead whole: body, stage, labels, notes, its asks with
// their options, recommendation and state, and for each about-bead its
// id, title and stage. Answers fold into their asks; nobody reads the raw
// form.
func TestBeadWhole(t *testing.T) {
	y := decisionYard(t)
	train := y.makeBead(t, NewBead{Title: "Release train"})
	links := y.makeBead(t, NewBead{Title: "Move the links", Body: "The links file declares the new source.",
		Labels: []string{"topic:linear-out"}})
	if err := y.Store.AddNote(y.ctx, links, "the test passed on a copy", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	withAbout := linksAsk()
	withAbout.About = []string{train}
	open := y.askOn(t, links, withAbout, "yardmaster")
	picked := y.askOn(t, links, linksAsk(), "yardmaster")
	y.answerOn(t, links, Answer{Ask: picked, Option: 2, Words: "after friday"}, "mckean")
	_ = open

	v, err := y.Store.Bead(y.ctx, links)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != links || v.Title != "Move the links" || v.Body != "The links file declares the new source." ||
		v.Stage == "" || len(v.Labels) != 1 || v.Labels[0] != "topic:linear-out" {
		t.Fatalf("bead = %+v", v)
	}
	if len(v.Notes) != 1 || v.Notes[0].Kind != NoteNote || v.Notes[0].Author != "yardmaster" ||
		v.Notes[0].Text != "the test passed on a copy" || v.Notes[0].Seq == 0 || v.Notes[0].At.IsZero() {
		t.Fatalf("notes = %+v", v.Notes)
	}
	if len(v.Asks) != 2 {
		t.Fatalf("asks = %+v, want 2", v.Asks)
	}
	first := v.Asks[0]
	if first.Ask != open || first.To != "mckean" || first.Asker != "yardmaster" || first.Since.IsZero() ||
		first.Question != linksAsk().Question || len(first.Context) != 2 || len(first.Options) != 3 ||
		first.Options[2].Label != YouDecide || first.Recommend != 1 || first.Why == "" ||
		first.State != "open" || first.Option != 0 || first.Words != "" || first.ReturnAt != nil {
		t.Fatalf("ask = %+v", first)
	}
	if len(first.About) != 1 || first.About[0].ID == "" || first.About[0].Title != "Release train" ||
		first.About[0].Stage == "" {
		t.Fatalf("about = %+v", first.About)
	}
	second := v.Asks[1]
	if second.Ask != picked || second.State != "answered" || second.Option != 2 || second.Words != "after friday" {
		t.Fatalf("answered ask = %+v", second)
	}

	// The raw form stands nowhere in the view.
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := y.Store.Notes(y.ctx, links)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if n.Kind == NoteAsk || n.Kind == NoteAnswer {
			if strings.Contains(string(raw), n.Text) {
				t.Fatalf("the view holds the raw %s %d", n.Kind, n.Seq)
			}
		}
	}

	// An ask that does not parse is left out, never raw.
	err = y.Store.write(func(w *wtx) error {
		row, err := w.loadBead(links)
		if err != nil {
			return err
		}
		_, err = w.addNoted(row, NoteAsk, "{oops", "yardmaster")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err = y.Store.Bead(y.ctx, links)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Asks) != 2 || len(v.Notes) != 1 || strings.Contains(string(mustJSON(t, v)), "{oops") {
		t.Fatalf("a corrupt ask stands in the view: %+v %+v", v.Asks, v.Notes)
	}
	q, err := y.Store.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range q.Open {
		if o.Question == "{oops" {
			t.Fatalf("a corrupt ask stands in the queue: %+v", q.Open)
		}
	}

	if _, err := y.Store.Bead(y.ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bead(nope) = %v, want not found", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The socket writes do exactly what yardr bead answer does: a pick with
// words, words on an ask with none, Later, Decline. Every refusal below
// is AddAnswer's, word for word.
func TestDecisionsWrites(t *testing.T) {
	y := decisionYard(t)
	opt := y.ask(t, linksAsk(), "yardmaster")
	w := linksAsk()
	w.Options, w.Recommend, w.Why = nil, 0, ""
	worded := y.ask(t, w, "yardmaster")

	if err := y.Store.AnswerAsk(y.ctx, y.bead, opt, 2, "after friday", "mckean"); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t)[0]; r.State != AskAnswered || r.Current.Answer.Option != 2 ||
		r.Current.Answer.Words != "after friday" || r.Current.Note.Author != "mckean" {
		t.Fatalf("pick = %+v", r)
	}
	// A newer pick supersedes; every pick stays a note.
	if err := y.Store.AnswerAsk(y.ctx, y.bead, opt, 1, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t)[0]; r.Current.Answer.Option != 1 || len(r.Answers) != 2 {
		t.Fatalf("repicked = %+v", r)
	}

	if err := y.Store.AnswerAsk(y.ctx, y.bead, worded, 0, "warmly", "mckean"); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t)[1]; r.State != AskAnswered || r.Current.Answer.Words != "warmly" {
		t.Fatalf("words answer = %+v", r)
	}

	fresh := y.ask(t, w, "yardmaster")
	freshOpt := y.ask(t, linksAsk(), "yardmaster")
	bad := []struct {
		name string
		call func() error
		want string
	}{
		{"words alone on an ask with options", func() error {
			return y.Store.AnswerAsk(y.ctx, y.bead, freshOpt, 0, "a thought", "mckean")
		}, "leave the ask open"},
		{"a pick on an ask with no options", func() error {
			return y.Store.AnswerAsk(y.ctx, y.bead, fresh, 1, "", "mckean")
		}, "wants words"},
		{"words again on an answered ask", func() error {
			return y.Store.AnswerAsk(y.ctx, y.bead, worded, 0, "twice", "mckean")
		}, "is answered already"},
		{"declining without words", func() error {
			return y.Store.DeclineAsk(y.ctx, y.bead, opt, "", "mckean")
		}, "needs words"},
	}
	for _, c := range bad {
		if err := c.call(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s = %v, want %q", c.name, err, c.want)
		}
	}

	later := y.ask(t, linksAsk(), "yardmaster")
	if err := y.Store.DeferAsk(y.ctx, y.bead, later, "mckean"); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t)[4]; r.State != AskAside || r.ReturnAt.IsZero() {
		t.Fatalf("aside = %+v", r)
	}

	refused := y.ask(t, linksAsk(), "yardmaster")
	if err := y.Store.DeclineAsk(y.ctx, y.bead, refused, "not mine to take", "mckean"); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t)[5]; r.State != AskDeclined {
		t.Fatalf("declined = %+v", r)
	}
	if err := y.Store.AnswerAsk(y.ctx, y.bead, refused, 1, "", "mckean"); err == nil ||
		!strings.Contains(err.Error(), "was declined") {
		t.Fatalf("answering a declined ask = %v", err)
	}
}

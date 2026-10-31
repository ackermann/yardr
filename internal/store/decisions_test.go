package store

import (
	"encoding/json"
	"errors"
	"slices"
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
	// Sequential: the package's fake clock.
	pinClockFixed(t, askNow)
	y := decisionYard(t)
	pinClock(t, &y.now)
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

	q, err := y.Store.UserAsks(y.ctx, "mckean", "mckean")
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
	q, err = y.Store.UserAsks(y.ctx, "mckean", "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 4 || q.Open[3].Ask != aside || len(q.Decided) != 3 {
		t.Fatalf("after its return: open = %+v, decided = %+v", q.Open, q.Decided)
	}

	// A crew journal is a bead like any other here, though a bead listing
	// leaves it out: the yard asks the operator how to log a harness in on
	// the journal of the crew member sitting at its login screen, and the
	// person answers there or nowhere. The yard itself and a device read it
	// alike, and a row reads on as its card, options and all.
	crew, err := y.Store.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"kind": "claude"}}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	y.tick(time.Hour)
	login := y.askOn(t, crew.Journal, linksAsk(), "yard")
	y.tick(time.Hour)
	ended := y.askOn(t, crew.Journal, linksAsk(), "yard")
	y.tick(time.Hour)
	if err := y.Store.TakeBackAsk(y.ctx, crew.Journal, ended, "the login ended", "yard"); err != nil {
		t.Fatal(err)
	}
	y.tick(time.Hour)
	// Another writer's card answers whatever the screens read: only the
	// yard's own login cards wait on a seat.
	chosen := y.askOn(t, crew.Journal, linksAsk(), "yardmaster")
	y.tick(time.Hour)
	y.answerOn(t, crew.Journal, Answer{Ask: chosen, Option: 2}, "mckean")
	for _, viewer := range [][]string{nil, {"mckean"}} {
		q, err := y.Store.UserAsks(y.ctx, "mckean", "mckean", viewer...)
		if err != nil {
			t.Fatal(err)
		}
		if len(q.Open) != 5 || q.Open[4].Ask != login {
			t.Fatalf("viewer %q: open = %+v, want the journal's ask %d after the four", viewer, q.Open, login)
		}
		row := q.Open[4]
		if row.Bead != crew.Journal || row.BeadTitle != "Journal of yardmaster" || row.Topic != "" ||
			row.Question != linksAsk().Question || row.Asker != "yard" || row.Since.IsZero() {
			t.Fatalf("viewer %q: open journal row = %+v", viewer, row)
		}
		if len(q.Decided) != 5 {
			t.Fatalf("viewer %q: decided = %+v, want the journal's two before the three", viewer, q.Decided)
		}
		if d := q.Decided[0]; d.Bead != crew.Journal || d.Ask != chosen || d.State != "answered" ||
			d.Option != 2 || d.OptionLabel != "After Friday" {
			t.Fatalf("viewer %q: decided journal answer = %+v", viewer, d)
		}
		if d := q.Decided[1]; d.Bead != crew.Journal || d.Ask != ended || d.State != "taken back" ||
			d.By != "yard" || d.Words != "the login ended" {
			t.Fatalf("viewer %q: decided journal take-back = %+v", viewer, d)
		}
		v, err := y.Store.Bead(y.ctx, row.Bead, viewer...)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(v.Asks, func(a BeadAsk) bool { return a.Ask == row.Ask })
		if i < 0 || v.Asks[i].State != "open" || len(v.Asks[i].Options) != 3 || v.Asks[i].Options[0].Label != "Move them now" {
			t.Fatalf("viewer %q: the journal's card = %+v, want ask %d open with its options", viewer, v.Asks, row.Ask)
		}
	}

	if _, err := y.Store.UserAsks(y.ctx, "nobody", "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UserAsks(nobody) = %v, want not found", err)
	}

	// Another user's queue is theirs and the operator's alone to read.
	// Left out, the reader is the user.
	if _, err := y.Store.AddUser(y.ctx, "ada", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.Store.AddUser(y.ctx, "jo", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.Store.SetUser(y.ctx, "jo", map[string]string{UserRole: RoleOperator}, "mckean"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, actor string
		ok          bool
	}{
		{"mckean", "mckean", true},
		{"ada", "ada", true},
		{"jo", "jo", true},
		{"mckean", "", true},
		{"mckean", "jo", true},
		{"mckean", "ada", false},
		{"jo", "ada", false},
	} {
		if _, err := y.Store.UserAsks(y.ctx, c.name, c.actor); (err == nil) != c.ok {
			t.Errorf("UserAsks(%s) by %q = %v", c.name, c.actor, err)
		}
	}
	if _, err := y.Store.UserAsks(y.ctx, "mckean", "ada"); err == nil ||
		err.Error() != "only mckean or the operator (jo) may read mckean's inbox; this is ada" {
		t.Errorf("another user's queue by a member = %v", err)
	}
}

// A stage card as a person reads it in their queue and on the bead: the
// question names the bead, the context opens with the bead and its branch,
// and the options are the template's alone, so a pick past them is refused
// and records nothing. A card put before the yard dropped You decide still
// answers it: the pick records, and the bead stays where it is.
func TestStageCardInTheQueue(t *testing.T) {
	t.Parallel()
	y := decisionYard(t)
	tmpl := &AskTemplate{
		Kind: AskChoice, Question: "Should this bead land?",
		Context: []string{"Landing it is your call."},
		Options: []AskTemplateOption{
			{Outcome: "done", Consequence: "The bead lands."},
			{Outcome: "changes", Consequence: "The bead goes back."},
		},
	}
	var current, older uint64
	var stage string
	if err := y.Store.write(func(w *wtx) error {
		row, err := w.loadBead(y.bead)
		if err != nil {
			return err
		}
		notes, err := beadNotes(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		entry, err := latestStayEntry(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		stage = row.Stage
		card := tmpl.framed(row, notes).stageAsk("mckean", row.Stage, entry)
		n, err := w.storeStageAsk(row, card)
		if err != nil {
			return err
		}
		current = n.Seq
		// The same card as the yard stored it before: You decide last, its
		// move empty.
		card.Options = append(card.Options, AskOption{Label: YouDecide, Consequence: "leave it to yard"})
		card.Move = append(card.Move, "")
		n, err = w.storeStageAsk(row, card)
		older = n.Seq
		return err
	}); err != nil {
		t.Fatal(err)
	}

	q, err := y.Store.UserAsks(y.ctx, "mckean", "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 2 || q.Open[0].Ask != current || q.Open[1].Ask != older {
		t.Fatalf("open = %+v, want the two cards", q.Open)
	}
	for _, row := range q.Open {
		if want := "Should this bead land? — " + y.bead + ": the work"; row.Question != want {
			t.Errorf("queue row %d reads %q, want %q", row.Ask, row.Question, want)
		}
	}
	v, err := y.Store.Bead(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Asks) != 2 {
		t.Fatalf("asks = %+v, want the two cards", v.Asks)
	}
	labels := func(a BeadAsk) (out []string) {
		for _, o := range a.Options {
			out = append(out, o.Label)
		}
		return out
	}
	for i, want := range [][]string{{"done", "changes"}, {"done", "changes", YouDecide}} {
		card := v.Asks[i]
		header := []string{"Bead: " + y.bead + " — the work (branch yard/" + y.bead + ")", "Landing it is your call."}
		if !slices.Equal(card.Context, header) {
			t.Errorf("card %d context = %q, want the header and the template's line %q", card.Ask, card.Context, header)
		}
		if got := labels(card); !slices.Equal(got, want) {
			t.Errorf("card %d offers %q, want %q", card.Ask, got, want)
		}
	}

	// The card put now has no third pick; the older one answers it, and
	// moves nothing.
	err = y.Store.AddAnswer(y.ctx, y.bead, Answer{Ask: current, Option: 3}, "mckean")
	if err == nil || !strings.Contains(err.Error(), "has options 1..2") {
		t.Fatalf("a pick past a card's options: %v", err)
	}
	y.answerOn(t, y.bead, Answer{Ask: older, Option: 3, Words: "later, then"}, "mckean")
	if b, err := y.Store.GetBead(y.ctx, y.bead); err != nil || b.Stage != stage {
		t.Fatalf("bead after You decide = %+v, %v, want it still in %s", b, err, stage)
	}
	q, err = y.Store.UserAsks(y.ctx, "mckean", "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 1 || q.Open[0].Ask != current || len(q.Decided) != 1 || q.Decided[0].Ask != older ||
		q.Decided[0].OptionLabel != YouDecide {
		t.Fatalf("queue after You decide: open %+v, decided %+v", q.Open, q.Decided)
	}
}

// Bead reads one bead whole: body, stage, labels, notes, its asks with
// their options, recommendation and state, and for each about-bead its
// id, title and stage. Answers fold into their asks; nobody reads the raw
// form.
func TestBeadWhole(t *testing.T) {
	// Sequential: the package's fake clock.
	pinClockFixed(t, askNow)
	y := decisionYard(t)
	pinClock(t, &y.now)
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
	if len(v.Moves) != 0 || v.Now == nil || v.Now.Session != nil || v.Now.Activity == nil || v.Now.Activity.Kind != "noted" {
		t.Fatalf("moves and now = %+v %+v, want activity only", v.Moves, v.Now)
	}
	q, err := y.Store.UserAsks(y.ctx, "mckean", "mckean")
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

	// A manual claim can outlive the event log: unlike a running agent, a
	// person does not hold the log back to the claim. The live session still
	// answers who works the bead when its claimed event has been pruned.
	if err := y.Store.PutGroup(y.ctx, Group{Name: "people", Runner: "manual", Limit: 5}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	manual := y.makeBead(t, NewBead{Title: "manual work"})
	sess, err := y.Store.Claim(y.ctx, manual, "people", "new", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	y.tick(91 * 24 * time.Hour)
	y.makeBead(t, NewBead{Title: "newer work"}) // keeps a newer event as the head
	if _, err := y.Store.Prune(y.ctx, Retention{Events: 90 * 24 * time.Hour}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	v, err = y.Store.Bead(y.ctx, manual)
	if err != nil {
		t.Fatal(err)
	}
	if v.Now == nil || v.Now.Session == nil || v.Now.Activity != nil ||
		v.Now.Session.Group != "people" || v.Now.Session.State != "starting" ||
		!v.Now.Session.StartedAt.Equal(sess.StartedAt) {
		t.Fatalf("moves and now after the claim was pruned = %+v %+v, want the live session without activity", v.Moves, v.Now)
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
	t.Parallel()
	y := decisionYard(t)
	opt := y.ask(t, linksAsk(), "yardmaster")
	w := linksAsk()
	w.Options, w.Recommend, w.Why = nil, 0, ""
	worded := y.ask(t, w, "yardmaster")

	if err := y.Store.AnswerAsk(y.ctx, y.bead, opt, 2, "after friday", "mckean", false); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t)[0]; r.State != AskAnswered || r.Current.Answer.Option != 2 ||
		r.Current.Answer.Words != "after friday" || r.Current.Note.Author != "mckean" {
		t.Fatalf("pick = %+v", r)
	}
	// A newer pick supersedes; every pick stays a note.
	if err := y.Store.AnswerAsk(y.ctx, y.bead, opt, 1, "", "mckean", false); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t)[0]; r.Current.Answer.Option != 1 || len(r.Answers) != 2 {
		t.Fatalf("repicked = %+v", r)
	}

	if err := y.Store.AnswerAsk(y.ctx, y.bead, worded, 0, "warmly", "mckean", false); err != nil {
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
			return y.Store.AnswerAsk(y.ctx, y.bead, freshOpt, 0, "a thought", "mckean", false)
		}, "leave the ask open"},
		{"a pick on an ask with no options", func() error {
			return y.Store.AnswerAsk(y.ctx, y.bead, fresh, 1, "", "mckean", false)
		}, "wants words"},
		{"words again on an answered ask", func() error {
			return y.Store.AnswerAsk(y.ctx, y.bead, worded, 0, "twice", "mckean", false)
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
	if err := y.Store.AnswerAsk(y.ctx, y.bead, refused, 1, "", "mckean", false); err == nil ||
		!strings.Contains(err.Error(), "was declined") {
		t.Fatalf("answering a declined ask = %v", err)
	}
}

// An answer to the yard's login card on a crew journal is refused once no
// seat of its crew member sits at the login screen: the answer made after
// the seat left, before the next pass takes the card back, would lead a
// login nothing waits on. While a seat sits — the line session or a
// conversation — the answer is taken. Declining the dead card ends it,
// putting it aside leaves it to the sweep, and a card no login waits on
// answers whatever the screens read.
func TestJournalAnswerRefusedAfterItsSeatLeft(t *testing.T) {
	t.Parallel()
	y := decisionYard(t)
	if err := y.Store.SetSetting(y.ctx, OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.Store.AddUser(y.ctx, "hal", "mckean"); err != nil {
		t.Fatal(err)
	}
	r, err := y.Store.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "env_for_hal": "/tmp/hal.env"}}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.Store.CrewStarted(y.ctx, "yardmaster", map[string]any{"agent": "ym"}); err != nil {
		t.Fatal(err)
	}
	card := y.askOn(t, r.Journal, linksAsk(), "yard")
	if err := y.Store.AnswerAsk(y.ctx, r.Journal, card, 1, "", "mckean", false); err == nil ||
		!strings.Contains(err.Error(), "left its login screen") {
		t.Fatalf("answering the yard's card with no seat at the screen = %v, want the seat-left refusal", err)
	}
	// A decline ends the dead card, and a deferral puts it aside for the
	// sweep: neither leads a login.
	declined := y.askOn(t, r.Journal, linksAsk(), "yard")
	if err := y.Store.DeclineAsk(y.ctx, r.Journal, declined, "not now", "mckean"); err != nil {
		t.Fatalf("declining the dead card = %v, want it taken", err)
	}
	aside := y.askOn(t, r.Journal, linksAsk(), "yard")
	if err := y.Store.DeferAsk(y.ctx, r.Journal, aside, "mckean"); err != nil {
		t.Fatalf("putting the dead card aside = %v, want it taken", err)
	}
	// Another writer's card on the journal answers whatever the screens
	// read: only the yard's login cards wait on a seat.
	other := y.askOn(t, r.Journal, linksAsk(), "yardmaster")
	if err := y.Store.AnswerAsk(y.ctx, r.Journal, other, 1, "", "mckean", false); err != nil {
		t.Fatalf("answering another writer's card with no seat = %v, want it taken", err)
	}
	// The refused card stands open: seating the line session takes the
	// same answer.
	if err := y.Store.SetCrewHandle(y.ctx, "yardmaster", map[string]any{"agent": "ym", HarnessLoginKey: "Select login method:"}); err != nil {
		t.Fatal(err)
	}
	if err := y.Store.AnswerAsk(y.ctx, r.Journal, card, 1, "", "mckean", false); err != nil {
		t.Fatalf("answering the yard's card with the line seated = %v, want it taken", err)
	}
	// A conversation seated while the line stands takes it too: the check
	// is over the member's sessions.
	if err := y.Store.SetCrewHandle(y.ctx, "yardmaster", map[string]any{"agent": "ym"}); err != nil {
		t.Fatal(err)
	}
	if err := y.Store.CrewStartedFor(y.ctx, "yardmaster", "hal", map[string]any{"agent": "ym-hal", HarnessLoginKey: "Select login method:"}); err != nil {
		t.Fatal(err)
	}
	seated := y.askOn(t, r.Journal, linksAsk(), "yard")
	if err := y.Store.AnswerAsk(y.ctx, r.Journal, seated, 1, "", "mckean", false); err != nil {
		t.Fatalf("answering the yard's card with a conversation seated = %v, want it taken", err)
	}
	// Both gone again: refused once more.
	if err := y.Store.SetCrewHandleFor(y.ctx, "yardmaster", "hal", map[string]any{"agent": "ym-hal"}); err != nil {
		t.Fatal(err)
	}
	gone := y.askOn(t, r.Journal, linksAsk(), "yard")
	if err := y.Store.AnswerAsk(y.ctx, r.Journal, gone, 1, "", "mckean", false); err == nil ||
		!strings.Contains(err.Error(), "takes the card back") {
		t.Fatalf("answering the yard's card after both seats left = %v, want the seat-left refusal", err)
	}
	orphaned := y.askOn(t, r.Journal, linksAsk(), "yard")
	if err := y.Store.DeleteCrew(y.ctx, "yardmaster", "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.Store.AnswerAsk(y.ctx, r.Journal, orphaned, 1, "", "mckean", false); err == nil ||
		!strings.Contains(err.Error(), "takes the card back") {
		t.Fatalf("answering the yard's card after its crew member was deleted = %v, want the seat-left refusal", err)
	}
}

package store

import (
	"strings"
	"testing"
	"time"
)

// answerYard is an ask yard with yardmaster as a crew member and mckean and
// ada as users, so answers deliver to their asker.
func answerYard(t *testing.T) *askYard {
	t.Helper()
	y := newAskYard(t)
	if _, err := y.Store.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"mckean", "ada"} {
		if _, err := y.Store.AddUser(y.ctx, u, u); err != nil {
			t.Fatal(err)
		}
	}
	return y
}

// lastAnswer is the newest noted event on the bead with an answer sentence.
func lastAnswer(t *testing.T, y *askYard) Event {
	t.Helper()
	evs, err := y.Store.Events(y.ctx, y.bead, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == "noted" {
			if line, _ := evs[i].Data["answer"].(string); line != "" {
				return evs[i]
			}
		}
	}
	t.Fatal("no answer event on the bead")
	return Event{}
}

// Every answer is delivered to the ask's asker, when the asker is a crew
// member or a user of this yard and is not the one answering: picked,
// answered with words, put aside, declined, left to it. The line says in
// words what happened.
func TestAnswerDeliversToAsker(t *testing.T) {
	y := answerYard(t)

	// First pick: the crew asker hears it, with who, which bead and ask,
	// the question, and the pick with its label and words.
	seq := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: seq, Option: 1, Words: "move them tonight"}, "mckean")
	e := lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != ReasonAnswered {
		t.Fatalf("the asker heard %q, want %q", got, ReasonAnswered)
	}
	line, _ := e.Data["answer"].(string)
	for _, want := range []string{"mckean picked 1 (Move them now)", y.bead, "ask", "Move this yard's links", "move them tonight"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line has no %q:\n%s", want, line)
		}
	}
	if page, err := y.Store.Inbox(y.ctx, "yardmaster", 0); err != nil || page.Total != 1 {
		t.Fatalf("the yardmaster's inbox = %+v, %v", page, err)
	}

	// A pick that replaces an earlier one reads as a change and names the
	// earlier one.
	y.tick(time.Hour)
	y.answer(t, Answer{Ask: seq, Option: 2}, "mckean")
	e = lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != ReasonAnswered {
		t.Fatalf("the changed pick heard %q, want %q", got, ReasonAnswered)
	}
	line, _ = e.Data["answer"].(string)
	for _, want := range []string{"mckean changed the pick", "was 1 (Move them now)", "now 2 (After Friday)"} {
		if !strings.Contains(line, want) {
			t.Errorf("the changed line has no %q:\n%s", want, line)
		}
	}

	// Later: put aside until when.
	laterAsk := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: laterAsk, Later: true}, "mckean")
	e = lastAnswer(t, y)
	line, _ = e.Data["answer"].(string)
	if !strings.Contains(line, "put aside") || !strings.Contains(line, "until") {
		t.Errorf("later reads:\n%s", line)
	}
	if got := Reason(e, "yardmaster"); got != ReasonAnswered {
		t.Errorf("later heard %q, want %q", got, ReasonAnswered)
	}

	// Decline: with the words.
	refused := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: refused, Words: "not mine to take", Decline: true}, "mckean")
	e = lastAnswer(t, y)
	line, _ = e.Data["answer"].(string)
	if !strings.Contains(line, "declined") || !strings.Contains(line, "not mine to take") {
		t.Errorf("decline reads:\n%s", line)
	}

	// You decide: left to the asker.
	decide := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: decide, Option: 3}, "mckean")
	e = lastAnswer(t, y)
	line, _ = e.Data["answer"].(string)
	if !strings.Contains(line, "left") || !strings.Contains(line, "yardmaster") {
		t.Errorf("you decide reads:\n%s", line)
	}

	// Words on an ask with none: answered with words.
	w := linksAsk()
	w.Options, w.Recommend, w.Why = nil, 0, ""
	worded := y.ask(t, w, "yardmaster")
	y.answer(t, Answer{Ask: worded, Words: "warmly"}, "mckean")
	e = lastAnswer(t, y)
	line, _ = e.Data["answer"].(string)
	if !strings.Contains(line, "answered") || !strings.Contains(line, "warmly") {
		t.Errorf("words read:\n%s", line)
	}
}

// An asker answering its own ask is told nothing: no delivery.
func TestAnswerOwnAskDeliversNothing(t *testing.T) {
	y := answerYard(t)
	seq := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: seq, Option: 1}, "yardmaster")
	e := lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != "" {
		t.Fatalf("the asker answering its own ask heard %q", got)
	}
	if page, err := y.Store.Inbox(y.ctx, "yardmaster", 0); err != nil || page.Total != 0 {
		t.Fatalf("the yardmaster's inbox = %+v, %v", page, err)
	}
}

// A user who asked is told in its notifications, with the same line.
func TestAnswerDeliversToUserAsker(t *testing.T) {
	y := answerYard(t)
	seq := y.askOn(t, y.bead, linksAsk(), "ada")
	y.answerOn(t, y.bead, Answer{Ask: seq, Option: 2, Words: "after friday"}, "mckean")
	evs, err := y.Store.Events(y.ctx, y.bead, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if Notified(e, "ada") == ReasonAnswered {
			found = true
			line, _ := e.Data["answer"].(string)
			if !strings.Contains(line, "mckean picked 2 (After Friday)") {
				t.Fatalf("the line reads:\n%s", line)
			}
		}
	}
	if !found {
		t.Fatal("ada was not notified")
	}
	page, err := y.Store.UserNotifications(y.ctx, "ada", 0)
	if err != nil || page.Unread != 1 {
		t.Fatalf("ada's notifications = %+v, %v", page, err)
	}
	text := page.Notifications[0].Text
	for _, want := range []string{"mckean picked 2 (After Friday)", "after friday"} {
		if !strings.Contains(text, want) {
			t.Errorf("the notification has no %q:\n%s", want, text)
		}
	}
}

// Where a pick was replaced the ask reads its history: "picked 2 (was 1
// until 11:07)".
func TestAskLinesShowReplacedPick(t *testing.T) {
	y := newAskYard(t)
	seq := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: seq, Option: 1}, "mckean")
	y.now = y.now.Add(time.Hour)
	y.answer(t, Answer{Ask: seq, Option: 2, Words: "after friday"}, "mckean")
	text := strings.Join(y.resolved(t)[0].Lines(), "\n")
	want := "picked 2 (was 1 until " + y.now.Local().Format("15:04") + ")"
	if !strings.Contains(text, want) {
		t.Fatalf("the ask has no %q:\n%s", want, text)
	}
	if !strings.Contains(text, "(After Friday)") || !strings.Contains(text, "after friday") {
		t.Fatalf("the pick lost its label or words:\n%s", text)
	}
	// A first pick reads as it did, with no history.
	y2 := newAskYard(t)
	only := y2.ask(t, linksAsk(), "yardmaster")
	y2.answer(t, Answer{Ask: only, Option: 1}, "mckean")
	if text := strings.Join(y2.resolved(t)[0].Lines(), "\n"); strings.Contains(text, "was 1 until") {
		t.Fatalf("a first pick reads a history:\n%s", text)
	}
}

// The bead call gives, for an ask, how many picks there were before the
// current one, so a client can say "changed".
func TestBeadPicksBefore(t *testing.T) {
	y := decisionYard(t)
	picked := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: picked, Option: 1}, "mckean")
	v, err := y.Store.Bead(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Asks) != 1 || v.Asks[0].PicksBefore != 0 {
		t.Fatalf("a first pick counts %+v", v.Asks)
	}
	y.answer(t, Answer{Ask: picked, Option: 2}, "mckean")
	v, err = y.Store.Bead(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Asks) != 1 || v.Asks[0].PicksBefore != 1 || v.Asks[0].Option != 2 {
		t.Fatalf("a changed pick counts %+v", v.Asks)
	}
	y.answer(t, Answer{Ask: picked, Option: 1}, "mckean")
	v, err = y.Store.Bead(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	if v.Asks[0].PicksBefore != 2 {
		t.Fatalf("a twice-changed pick counts %+v", v.Asks)
	}
}

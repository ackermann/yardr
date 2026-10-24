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
	// Sequential: the package's fake clock (via newAskYard).
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
	y.answer(t, Answer{Ask: laterAsk, Option: 2}, "mckean")
	e = lastAnswer(t, y)
	line, _ = e.Data["answer"].(string)
	if !strings.Contains(line, "picked 2 (After Friday)") || strings.Contains(line, "changed the pick") {
		t.Errorf("a pick after Later should not be a changed pick:\n%s", line)
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

// An answer to an ask whose asker is nobody with an inbox — a bead
// session's actor after its session ended — reaches the crew members
// whose yardr subscriptions cover the bead's current stage, under the
// subscription's own reason: a noted event otherwise carries no stage,
// so with the asker gone the answer would reach nobody. A crew asker's
// answer still reaches only its asker, as answered, and a stage ask's
// answer, which moves the bead itself, tells no stage subscriber.
func TestAnswerOrphanAskerDeliversToStageSubscribers(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := answerYard(t)
	if err := y.Store.AddSubscription(y.ctx, "yardmaster",
		Subscription{Source: SourceYardr, Scope: ScopeAll, Where: map[string]string{"stage": "decide"}},
		"yardmaster"); err != nil {
		t.Fatal(err)
	}
	decided, err := y.Store.CreateBead(y.ctx, NewBead{Title: "waiting on mckean", Stage: "decide"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	y.bead = decided.ID

	// The shape agent asked mckean and its session ended; mckean's
	// answer reaches the yardmaster through stage=decide, and the
	// yardmaster's inbox holds the answer line.
	seq := y.ask(t, linksAsk(), "yardr-vn86-shape")
	y.answer(t, Answer{Ask: seq, Option: 1, Words: "move them tonight"}, "mckean")
	e := lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != ReasonAll {
		t.Fatalf("the orphan answer reached the yardmaster as %q, want %q", got, ReasonAll)
	}
	line, _ := e.Data["answer"].(string)
	page, err := y.Store.Inbox(y.ctx, "yardmaster", 0)
	if err != nil || page.Total != 1 {
		t.Fatalf("the yardmaster's inbox = %+v, %v", page, err)
	}
	if got, _ := page.Events[0].Data["answer"].(string); got != line {
		t.Fatalf("the inbox holds %q, want the answer line %q", got, line)
	}

	// A crew asker's answer still reaches only its asker, as answered:
	// no stage fallback for an asker with an inbox.
	y.tick(time.Hour)
	crew := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: crew, Option: 2}, "mckean")
	e = lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != ReasonAnswered {
		t.Fatalf("the crew answer reached the yardmaster as %q, want %q", got, ReasonAnswered)
	}
	if d, _ := e.Data["deliveries"].(map[string]any); len(d) != 1 {
		t.Fatalf("the crew answer delivered to %v", d)
	}
	if n, _ := e.Data["notified"].(map[string]any); len(n) != 0 {
		t.Fatalf("the crew answer notified %v", n)
	}

	// A stage ask's answer moves the bead itself, so it tells no stage
	// subscriber: the answered event carries no new delivery. AddAsk
	// clears Stage — stage asks are the yard's own — so the ask is
	// stored the yard's way; a decline moves nothing.
	var stageSeq uint64
	if err := y.Store.write(func(w *wtx) error {
		row, err := w.loadBead(y.bead)
		if err != nil {
			return err
		}
		n, err := w.storeStageAsk(row, Ask{Question: "Move it on?", To: "mckean",
			Context: []string{"the bead waits in decide."}, Stage: "decide"})
		if err != nil {
			return err
		}
		stageSeq = n.Seq
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	y.tick(time.Hour)
	y.answer(t, Answer{Ask: stageSeq, Words: "not yet", Decline: true}, "mckean")
	e = lastAnswer(t, y)
	if got := Reason(e, "yardmaster"); got != "" {
		t.Fatalf("the stage answer reached the yardmaster as %q, want nothing", got)
	}
	if d, _ := e.Data["deliveries"].(map[string]any); len(d) != 0 {
		t.Fatalf("the stage answer delivered to %v", d)
	}
	if n, _ := e.Data["notified"].(map[string]any); len(n) != 0 {
		t.Fatalf("the stage answer notified %v", n)
	}
}

// An asker answering its own ask is told nothing: no delivery.
func TestAnswerOwnAskDeliversNothing(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
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
	// Sequential: the package's fake clock (via newAskYard).
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
	t.Run("run ask typed value and path", testRunApprovalOfTypedValueAndPathMarksItsAnswer)
}

// Where a pick was replaced the ask reads its history: "picked 2 (was 1
// until 11:07)".
func TestAskLinesShowReplacedPick(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
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
	// Sequential: the package's fake clock (via newAskYard).
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

// An approval of a run ask that sets a setting or adds a source marks its
// noted event, so the stream and the notifications hold its sentence from
// anyone who is no operator (WithoutHeld): the sentence names the command
// whole, value or path with it. The event keeps the sentence whole, as the
// asker's inbox reads it, and the operator's notification reads it too. An
// approval of any other row is marked nothing, and its sentence is told.
func testRunApprovalOfTypedValueAndPathMarksItsAnswer(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	// A second operator, who is told what the approval's actor is not told
	// of their own act.
	if _, err := y.AddUser(y.ctx, "op", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetUser(y.ctx, "op", map[string]string{UserRole: RoleOperator}, "mckean"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"op", "ada"} {
		if err := y.AddSubscription(y.ctx, user, Subscription{Source: SourceYardr, Scope: ScopeAll,
			Where: map[string]string{"kind": "noted"}}, user); err != nil {
			t.Fatal(err)
		}
	}
	const value = "early-shift"
	const file = "phone.yaml"
	setting := y.ask(t, Ask{Question: "May the yard's note change?", To: "mckean",
		Context: []string{"The night shift ends at six now."},
		Run:     &AskRun{Argv: []string{"settings", "set", "operator_note", value}}}, "yardmaster")
	source := y.ask(t, Ask{Question: "May the phone source be added?", To: "mckean",
		Context: []string{"The night shift wants its calls told by phone."},
		Run:     &AskRun{Argv: []string{"source", "add", file}, Carried: carriedOf(t, phoneDecl, nil)}}, "yardmaster")
	limit := y.ask(t, Ask{Question: "May airy send more mail?", To: "mckean",
		Context: []string{"The far yard's mail waits on its limit."},
		Run:     &AskRun{Argv: []string{"peer", "allow", "airy", "--mail-limit", "300"}}}, "yardmaster")
	for _, seq := range []uint64{setting, source, limit} {
		if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true); err != nil {
			t.Fatal(err)
		}
	}
	// answers are the noted events with an answer sentence naming want.
	answers := func(want string) []Event {
		t.Helper()
		evs, err := y.Store.Events(y.ctx, y.bead, 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []Event
		for _, e := range evs {
			if line, _ := e.Data["answer"].(string); line != "" && strings.Contains(line, want) {
				out = append(out, e)
			}
		}
		return out
	}
	for _, tc := range []struct {
		seq    uint64
		marked bool
		secret string
	}{
		{setting, true, value},
		{source, true, file},
		{limit, false, "--mail-limit 300"},
	} {
		found := answers(tc.secret)
		if len(found) != 1 {
			t.Fatalf("the answers naming %q = %d, want 1", tc.secret, len(found))
		}
		e := found[0]
		if got, _ := e.Data["answer_held"].(bool); got != tc.marked {
			t.Errorf("the answer of ask %d is marked %v, want %v", tc.seq, got, tc.marked)
		}
		told, held := e.WithoutHeld()
		if !held {
			t.Errorf("the answer of ask %d is told whole", tc.seq)
		}
		if _, ok := told.Data["answer"]; ok == tc.marked {
			t.Errorf("the answer of ask %d is told: %v, want %v", tc.seq, ok, !tc.marked)
		}
		if _, ok := told.Data["answer_held"]; ok {
			t.Errorf("the answer of ask %d tells its mark", tc.seq)
		}
	}
	// rows are the notifications of user by event seq.
	rows := func(user string) map[uint64]Notification {
		t.Helper()
		page, err := y.Store.UserNotifications(y.ctx, user, 100)
		if err != nil {
			t.Fatal(err)
		}
		out := map[uint64]Notification{}
		for _, n := range page.Notifications {
			out[n.Seq] = n
		}
		return out
	}
	ops, others := rows("op"), rows("ada")
	for _, tc := range []struct {
		secret string
		want   string
	}{
		{value, "the setting's value"},
		{file, "the source's path"},
	} {
		e := answers(tc.secret)[0]
		if n := ops[e.Seq]; !strings.Contains(n.Text, tc.secret) {
			t.Errorf("the operator's notification reads %q, want %s", n.Text, tc.want)
		}
		n := others[e.Seq]
		if strings.Contains(n.Text, tc.secret) {
			t.Errorf("the member's notification reads %q", n.Text)
		}
		if !strings.Contains(n.Text, "noted") || !strings.Contains(n.Text, y.bead) {
			t.Errorf("the member's notification reads %q, want the noted event without its sentence", n.Text)
		}
	}
	e := answers("--mail-limit 300")[0]
	if n := others[e.Seq]; !strings.Contains(n.Text, "--mail-limit 300") {
		t.Errorf("the member's notification of the told row reads %q", n.Text)
	}
}

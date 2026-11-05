package mcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/mcp"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The tests drive the tools through a real store (a throwaway one, as
// the depot's fixtures give), at the cheapest level that reaches the
// behaviour: a Service call, not a process. The wire shapes (both
// protocol eras, the typed-back number over the wire) are server_test.go's.

type yard struct {
	st  store.API
	ctx context.Context
}

type mailHookYard struct {
	mcp.Yard
	afterMail func(context.Context, string, string, string) error
}

func (y mailHookYard) Mail(ctx context.Context, to, text, actor string, files ...store.BlobFile) error {
	if err := y.Yard.Mail(ctx, to, text, actor, files...); err != nil {
		return err
	}
	if y.afterMail != nil {
		return y.afterMail(ctx, to, text, actor)
	}
	return nil
}

type readErrorYard struct {
	mcp.Yard
	settingsErr error
	crewErr     error
	blockersErr error
}

func (y readErrorYard) Settings(ctx context.Context) (map[string]string, error) {
	if y.settingsErr != nil {
		return nil, y.settingsErr
	}
	return y.Yard.Settings(ctx)
}

func (y readErrorYard) Crew(ctx context.Context) ([]store.CrewMember, error) {
	if y.crewErr != nil {
		return nil, y.crewErr
	}
	return y.Yard.Crew(ctx)
}

func (y readErrorYard) OpenBlockers(ctx context.Context, id string) ([]string, error) {
	if y.blockersErr != nil {
		return nil, y.blockersErr
	}
	return y.Yard.OpenBlockers(ctx, id)
}

func newYard(t *testing.T) *yard {
	t.Helper()
	y := &yard{st: storetest.New(t), ctx: context.Background()}
	if _, err := y.st.AddUser(y.ctx, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddUser(y.ctx, "bob", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := y.st.SetSetting(y.ctx, store.OperatorSetting, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.PutCrew(y.ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "ada"); err != nil {
		t.Fatal(err)
	}
	return y
}

func (y *yard) depot(t *testing.T, name string) {
	t.Helper()
	if err := y.st.PutDepot(y.ctx, store.Depot{Name: name, Kind: store.DepotDir, Path: t.TempDir()}, "ada"); err != nil {
		t.Fatal(err)
	}
}

func (y *yard) work(t *testing.T, title, depot, owner string) string {
	t.Helper()
	b, err := y.st.CreateBead(y.ctx, store.NewBead{Title: title, Type: "task", Depot: depot, For: owner}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return b.ID
}

func (y *yard) ask(t *testing.T, bead string, ask store.Ask) uint64 {
	t.Helper()
	seq, err := y.st.AddAsk(y.ctx, bead, ask, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func svc(y *yard, connection string) *mcp.Service {
	return &mcp.Service{Yard: y.st, User: "ada", Connection: connection, Now: time.Now}
}

// plain holds a result to the plain-words bar: no yard noun unexplained.
// The one allowed shape is the terminal command a refusal names, which
// is the person's own to type.
func plain(t *testing.T, text string) {
	t.Helper()
	lower := strings.ToLower(strings.ReplaceAll(text, "yardr bead answer", ""))
	for _, word := range []string{"bead", "depot", "flow", "stage", "seq"} {
		if strings.Contains(lower, word) {
			t.Errorf("the result uses the yard word %q:\n%s", word, text)
		}
	}
}

func TestNoUserRefusesEveryTool(t *testing.T) {
	y := newYard(t)
	s := &mcp.Service{Yard: y.st, User: ""}
	calls := []func() error{
		func() error { _, err := s.WhatIsHappening(y.ctx, "", ""); return err },
		func() error { _, err := s.AskForSomething(y.ctx, "a thing", ""); return err },
		func() error { _, err := s.WhatWaitsForMe(y.ctx); return err },
		func() error {
			_, err := s.Answer(y.ctx, mcp.AnswerInput{Work: "x", Question: 1, Pick: 1}, false)
			return err
		},
		func() error { _, err := s.AskTheYardmaster(y.ctx, "hi", nil); return err },
		func() error { _, err := s.ReadMyMail(y.ctx); return err },
	}
	for i, call := range calls {
		if err := call(); err == nil || err.Error() != mcp.NoUserMessage {
			t.Errorf("call %d = %v, want the one no-user message", i, err)
		}
	}
}

func TestAskForSomethingDepotResolution(t *testing.T) {
	y := newYard(t)
	s := svc(y, "")
	// No project at all: refuse, listing none, and file nothing.
	if _, err := s.AskForSomething(y.ctx, "Write the report", ""); err == nil ||
		!strings.Contains(err.Error(), "Your projects are: none yet") {
		t.Fatalf("no projects: %v", err)
	}
	y.depot(t, "yardr")
	// The one project needs no name, and takes the bead.
	res, err := s.AskForSomething(y.ctx, "Write the report\nWhat done looks like: a page the team reads.", "")
	if err != nil {
		t.Fatal(err)
	}
	plain(t, res.Text)
	if res.Structured["project"] != "yardr" || res.Structured["title"] != "Write the report" {
		t.Fatalf("filed as %+v", res.Structured)
	}
	// A name that is not the one project refuses the same way.
	if _, err := s.AskForSomething(y.ctx, "Other", "shop"); err == nil ||
		!strings.Contains(err.Error(), "Your projects are: yardr") {
		t.Fatalf("wrong project: %v", err)
	}
	y.depot(t, "shop")
	// Several projects: missing names none, a named one files there.
	// (The names are listed as the yard lists its projects.)
	if _, err := s.AskForSomething(y.ctx, "Other", ""); err == nil ||
		!strings.Contains(err.Error(), "Your projects are:") ||
		!strings.Contains(err.Error(), "yardr") || !strings.Contains(err.Error(), "shop") {
		t.Fatalf("several, unnamed: %v", err)
	}
	res, err = s.AskForSomething(y.ctx, "Stock the shelves", "Shop")
	if err != nil {
		t.Fatal(err)
	}
	if res.Structured["project"] != "shop" {
		t.Fatalf("filed as %+v, want the named project", res.Structured)
	}
	bead, err := y.st.GetBead(y.ctx, res.Structured["work"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if bead.Originator != "ada" || bead.Type != "task" {
		t.Fatalf("the filed work = %+v, want ada's task", bead)
	}
}

func TestWhatIsHappening(t *testing.T) {
	y := newYard(t)
	y.depot(t, "yardr")
	s := svc(y, "")
	mine := y.work(t, "Write the report", "yardr", "ada")
	y.work(t, "Bob's thing", "yardr", "bob")
	done := y.work(t, "Old report", "yardr", "ada")
	if _, err := y.st.CloseBead(y.ctx, done, "done", store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	held := y.work(t, "Stuck thing", "yardr", "ada")
	if _, err := y.st.SetHold(y.ctx, held, true, store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	res, err := s.WhatIsHappening(y.ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	plain(t, res.Text)
	for _, want := range []string{"Write the report", "Old report", "Stuck thing", "on hold"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the account lacks %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "Bob's thing") {
		t.Errorf("another person's work is in the account:\n%s", res.Text)
	}
	// One piece in full, with its notes.
	if err := y.st.AddNote(y.ctx, mine, "the figures arrived", "bob"); err != nil {
		t.Fatal(err)
	}
	res, err = s.WhatIsHappening(y.ctx, mine, "")
	if err != nil {
		t.Fatal(err)
	}
	plain(t, res.Text)
	if !strings.Contains(res.Text, "the figures arrived") || res.Structured["work"] != mine {
		t.Fatalf("the one piece:\n%s\n%+v", res.Text, res.Structured)
	}
	// A window that excludes the finished work drops it.
	res, err = s.WhatIsHappening(y.ctx, "", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "Old report") {
		t.Errorf("finished work outside the window is shown:\n%s", res.Text)
	}

	settingsErr := errors.New("settings unavailable")
	s.Yard = readErrorYard{Yard: y.st, settingsErr: settingsErr}
	if _, err := s.WhatIsHappening(y.ctx, "", ""); !errors.Is(err, settingsErr) {
		t.Fatalf("a failed owner lookup = %v, want its settings error", err)
	}
	blockersErr := errors.New("blockers unavailable")
	s.Yard = readErrorYard{Yard: y.st, blockersErr: blockersErr}
	if _, err := s.WhatIsHappening(y.ctx, "", ""); !errors.Is(err, blockersErr) {
		t.Fatalf("a failed blocker lookup = %v, want its error", err)
	}
	if _, err := s.WhatIsHappening(y.ctx, mine, ""); !errors.Is(err, blockersErr) {
		t.Fatalf("a failed one-piece blocker lookup = %v, want its error", err)
	}
}

func choicesAsk(to string) store.Ask {
	return store.Ask{
		Question: "Move the links now?",
		To:       to,
		Context:  []string{"The links still point at the old place."},
		Options: []store.AskOption{
			{Label: "Move them now", Consequence: "all links point at the new place tonight"},
			{Label: "After Friday", Consequence: "the old place stays until the release is out"},
		},
		Recommend: 1, Why: "the test passed",
	}
}

func TestAnswerForms(t *testing.T) {
	y := newYard(t)
	y.depot(t, "yardr")
	s := svc(y, "")

	// A pick, with words.
	bead := y.work(t, "Move the links", "yardr", "ada")
	seq := y.ask(t, bead, choicesAsk("ada"))
	res, err := s.Answer(y.ctx, mcp.AnswerInput{Work: bead, Question: seq, Pick: 1, Words: "tonight"}, false)
	if err != nil {
		t.Fatal(err)
	}
	plain(t, res.Text)
	if res.Structured["state"] != "answered" {
		t.Fatalf("the pick's structured state = %v, want answered", res.Structured["state"])
	}
	q, err := y.st.UserAsks(y.ctx, "ada", "ada")
	if err != nil || len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].Option != 1 {
		t.Fatalf("after the pick: %+v, %v", q, err)
	}

	// Words alone on a question with choices is refused, in plain words.
	bead2 := y.work(t, "Pick a colour", "yardr", "ada")
	seq2 := y.ask(t, bead2, choicesAsk("ada"))
	if _, err := s.Answer(y.ctx, mcp.AnswerInput{Work: bead2, Question: seq2, Words: "blue"}, false); err == nil {
		t.Fatal("words alone on a choices question was answered")
	}
	// Put aside, then decline after it comes back to the queue's words.
	aside, err := s.Answer(y.ctx, mcp.AnswerInput{Work: bead2, Question: seq2, PutAside: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if aside.Structured["state"] != "put aside" {
		t.Fatalf("the put-aside structured state = %v, want put aside", aside.Structured["state"])
	}
	declined, err := s.Answer(y.ctx, mcp.AnswerInput{Work: bead2, Question: seq2, Decline: true, Words: "not mine"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if declined.Structured["state"] != "declined" {
		t.Fatalf("the declined structured state = %v, want declined", declined.Structured["state"])
	}
	q, err = y.st.UserAsks(y.ctx, "ada", "ada")
	if err != nil || len(q.Open) != 0 {
		t.Fatalf("after aside and decline: %+v, %v", q, err)
	}

	// Words alone where the question wants words.
	bead3 := y.work(t, "Tone of the greeting", "yardr", "ada")
	seq3 := y.ask(t, bead3, store.Ask{Question: "What tone?", To: "ada", Context: []string{"the greeting."}})
	if _, err := s.Answer(y.ctx, mcp.AnswerInput{Work: bead3, Question: seq3, Words: "warm"}, false); err != nil {
		t.Fatal(err)
	}
	// Another person's question is not a member's to answer (an
	// operator may answer any, as in every window; that is the
	// store's rule, not this window's).
	bead4 := y.work(t, "Ada's question", "yardr", "ada")
	seq4 := y.ask(t, bead4, choicesAsk("ada"))
	bobSvc := &mcp.Service{Yard: y.st, User: "bob", Now: time.Now}
	if _, err := bobSvc.Answer(y.ctx, mcp.AnswerInput{Work: bead4, Question: seq4, Pick: 1}, false); err == nil {
		t.Fatal("another person's question was answered by a member")
	} else {
		plain(t, err.Error())
	}
}

const crmSource = `name: crm
base: https://crm.example.com/v1
credential: { env: CRM_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// fakeGiveSecret is unreachable through the tools by construction: the
// Yard interface has no secret call. This test holds the observable
// half: the refusal, in its plain words, with the question left open.
func TestSecretAnswerIsRefusedAndStaysOpen(t *testing.T) {
	y := newYard(t)
	y.depot(t, "yardr")
	if _, err := y.st.PutSource(y.ctx, "crm.yaml", crmSource, "ada"); err != nil {
		t.Fatal(err)
	}
	bead := y.work(t, "Wire the CRM", "yardr", "ada")
	seq := y.ask(t, bead, store.Ask{
		Question: "The API key for the CRM", To: "ada",
		Context: []string{"Create one at https://crm.example.com/account/api (read only)."},
		Secret:  &store.AskSecret{Source: "crm"},
	})
	s := svc(y, "")
	_, err := s.Answer(y.ctx, mcp.AnswerInput{Work: bead, Question: seq, Pick: 1}, false)
	if err == nil {
		t.Fatal("a secret answer was taken through MCP")
	}
	for _, want := range []string{"secrets are never given here", "still open", "yardr bead answer " + bead, "--secret"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	q, err := y.st.UserAsks(y.ctx, "ada", "ada")
	if err != nil || len(q.Open) != 1 || q.Open[0].Ask != seq {
		t.Fatalf("the secret question after the refusal: %+v, %v", q, err)
	}
	// Putting it aside or declining it stays the person's to do here.
	if _, err := s.Answer(y.ctx, mcp.AnswerInput{Work: bead, Question: seq, PutAside: true}, false); err != nil {
		t.Fatalf("putting a secret question aside: %v", err)
	}
}

func TestRunApprovalAsOperator(t *testing.T) {
	y := newYard(t)
	y.depot(t, "yardr")
	bead := y.work(t, "Add a person", "yardr", "ada")
	seq := y.ask(t, bead, store.Ask{
		Question: "Add grace as a person of the yard?", To: "ada",
		Context: []string{"Grace will read her own inbox."},
		Run:     &store.AskRun{Argv: []string{"user", "add", "grace"}},
	})
	s := svc(y, "")
	res, err := s.Answer(y.ctx, mcp.AnswerInput{Work: bead, Question: seq, Approve: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Weighed != nil {
		// Weighed, as a weighty card is: the typed-back number, then
		// the approval goes through with the word set.
		res, err = s.Answer(y.ctx, mcp.AnswerInput{Work: bead, Question: seq, Approve: true}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	plain(t, res.Text)
	if _, err := y.st.GetUser(y.ctx, "grace"); err != nil {
		t.Fatalf("the approved command did not run: %v", err)
	}
}

// An own-subscription run ask is its person's to approve on this
// path, as it is on the socket (design yardr-ts4d 3.3): a member
// approves the one that spends their own, and nobody else's.
func TestOwnSubscriptionApprovalAsItsPerson(t *testing.T) {
	y := newYard(t)
	y.depot(t, "yardr")
	bead := y.work(t, "How bob is told", "yardr", "bob")
	seq := y.ask(t, bead, store.Ask{
		Question: "Tell bob of mail only?", To: "bob",
		Context: []string{"Bob asked to hear of mail, and nothing else."},
		Run:     &store.AskRun{Argv: []string{"user", "set", "bob", "tell=mail"}},
	})
	bobSvc := &mcp.Service{Yard: y.st, User: "bob", Now: time.Now}
	res, err := bobSvc.Answer(y.ctx, mcp.AnswerInput{Work: bead, Question: seq, Approve: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Weighed != nil {
		res, err = bobSvc.Answer(y.ctx, mcp.AnswerInput{Work: bead, Question: seq, Approve: true}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	plain(t, res.Text)
	u, err := y.st.GetUser(y.ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Tell) != 1 || u.Tell[0] != "mail" {
		t.Fatalf("bob's tell after his own approval = %v", u.Tell)
	}
}

func TestWhatWaitsForMe(t *testing.T) {
	y := newYard(t)
	y.depot(t, "yardr")
	bead := y.work(t, "Move the links", "yardr", "ada")
	seq := y.ask(t, bead, choicesAsk("ada"))
	res, err := svc(y, "").WhatWaitsForMe(y.ctx)
	if err != nil {
		t.Fatal(err)
	}
	plain(t, res.Text)
	for _, want := range []string{"Move the links now?", "Move them now", "Recommended: 1"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the waiting list lacks %q:\n%s", want, res.Text)
		}
	}
	qs, _ := res.Structured["questions"].([]map[string]any)
	if len(qs) != 1 || qs[0]["work"] != bead || qs[0]["question"] != seq {
		t.Fatalf("the structured questions = %+v", res.Structured)
	}
	if _, ok := qs[0]["question_text"]; ok {
		t.Fatalf("the structured result duplicates question text without its outside mark: %+v", qs[0])
	}
}

func TestReadMyMailAcksThroughWhatItShowed(t *testing.T) {
	y := newYard(t)
	s := svc(y, "")
	if err := y.st.Mail(y.ctx, "ada", "first note", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := y.st.Mail(y.ctx, "ada", "second note", "bob"); err != nil {
		t.Fatal(err)
	}
	res, err := s.ReadMyMail(y.ctx)
	if err != nil {
		t.Fatal(err)
	}
	plain(t, res.Text)
	if !strings.Contains(res.Text, "first note") || !strings.Contains(res.Text, "second note") {
		t.Fatalf("the unread mail:\n%s", res.Text)
	}
	// Both senders read, and marked read: nothing waits now.
	if n, err := s.UnreadCount(y.ctx); err != nil || n != 0 {
		t.Fatalf("unread after reading = %d, %v", n, err)
	}
	res, err = s.ReadMyMail(y.ctx)
	if err != nil || !strings.Contains(res.Text, "no unread mail") {
		t.Fatalf("the second read: %v\n%s", err, res.Text)
	}
	// What arrives after is the next call's, unacknowledged until then.
	if err := y.st.Mail(y.ctx, "ada", "third note", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UnreadCount(y.ctx); n != 1 {
		t.Fatalf("unread = %d, want the new one", n)
	}
	res, err = s.ReadMyMail(y.ctx)
	if err != nil || !strings.Contains(res.Text, "third note") || strings.Contains(res.Text, "first note") {
		t.Fatalf("the third read: %v\n%s", err, res.Text)
	}
}

func TestAskTheYardmasterWait(t *testing.T) {
	y := newYard(t)
	// The reply inside the wait is the result.
	replyYard := mailHookYard{Yard: y.st, afterMail: func(ctx context.Context, to, _ string, actor string) error {
		if to != "yardmaster" {
			return nil
		}
		return y.st.Mail(ctx, actor, "the answer is 42", to)
	}}
	s := &mcp.Service{Yard: replyYard, User: "ada", Wait: 5 * time.Second,
		PollInterval: 10 * time.Millisecond, ProgressInterval: time.Hour}
	settingsErr := errors.New("settings unavailable")
	s.Yard = readErrorYard{Yard: y.st, settingsErr: settingsErr}
	if _, err := s.AskTheYardmaster(y.ctx, "what is the answer?", nil); !errors.Is(err, settingsErr) {
		t.Fatalf("a failed yardmaster setting lookup = %v, want its error", err)
	}
	crewErr := errors.New("crew unavailable")
	s.Yard = readErrorYard{Yard: y.st, crewErr: crewErr}
	if _, err := s.AskTheYardmaster(y.ctx, "what is the answer?", nil); !errors.Is(err, crewErr) {
		t.Fatalf("a failed yardmaster roster lookup = %v, want its error", err)
	}
	s.Yard = replyYard
	var progress []string
	res, err := s.AskTheYardmaster(y.ctx, "what is the answer?", func(msg string) { progress = append(progress, msg) })
	if err != nil {
		t.Fatal(err)
	}
	plain(t, res.Text)
	if !strings.Contains(res.Text, "the answer is 42") || len(progress) == 0 || progress[0] != "asked; waiting for the answer" {
		t.Fatalf("the waited reply:\n%s\nprogress %v", res.Text, progress)
	}
	// The wait ending says the answer will come, and keeps the message.
	s.Yard = y.st
	s.Wait = 150 * time.Millisecond
	res, err = s.AskTheYardmaster(y.ctx, "and this one?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "The answer will come") {
		t.Fatalf("the ended wait:\n%s", res.Text)
	}
	// The message itself stands in the conversation, read or not.
	letters, _, err := y.st.Conversation(y.ctx, "ada", "yardmaster", 0, 0, 0)
	if err != nil || len(letters) == 0 {
		t.Fatalf("the conversation after the wait: %v letters, %v", len(letters), err)
	}
	// Cancelling the wait keeps the message standing too.
	ctx, cancel := context.WithCancel(y.ctx)
	s.Wait = time.Minute
	sent := make(chan struct{}, 1)
	s.Yard = mailHookYard{Yard: y.st, afterMail: func(context.Context, string, string, string) error {
		sent <- struct{}{}
		return nil
	}}
	done := make(chan *mcp.Result, 1)
	go func() {
		res, _ := s.AskTheYardmaster(ctx, "a third question", nil)
		done <- res
	}()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the question was not sent before cancelling")
	}
	cancel()
	select {
	case res := <-done:
		if !strings.Contains(res.Text, "still with the yardmaster") {
			t.Fatalf("the cancelled wait:\n%s", res.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled wait did not end")
	}
}

func TestConnectionMarkOnWrites(t *testing.T) {
	y := newYard(t)
	y.depot(t, "yardr")

	// A write under a connection name reads "via" it; without a name,
	// as the user alone. Reads (what waits) leave no mark either way.
	for _, conn := range []string{"openclaw", ""} {
		s := svc(y, conn)
		res, err := s.AskForSomething(y.ctx, "File a thing "+conn, "")
		if err != nil {
			t.Fatal(err)
		}
		beadID := res.Structured["work"].(string)
		evs, err := y.st.Events(y.ctx, beadID, 10)
		if err != nil {
			t.Fatal(err)
		}
		var via string
		for _, e := range evs {
			if e.Kind == "created" {
				via = store.Via(e)
			}
		}
		if via != conn {
			t.Errorf("connection %q: the filed work's via = %q", conn, via)
		}
		// The person's own answer on a question is marked the same way.
		seq := y.ask(t, beadID, choicesAsk("ada"))
		if _, err := s.Answer(y.ctx, mcp.AnswerInput{Work: beadID, Question: seq, Pick: 2}, false); err != nil {
			t.Fatal(err)
		}
		notes, err := y.st.Notes(y.ctx, beadID)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range notes {
			if n.Kind == store.NoteAnswer && n.Via != conn {
				t.Errorf("connection %q: the answer note's via = %q", conn, n.Via)
			}
		}
		if _, err := s.WhatWaitsForMe(y.ctx); err != nil {
			t.Fatal(err)
		}
		head, err := y.st.Head(y.ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = head
	}

	// A read leaves no mark: what waits, read under a connection,
	// writes no event at all, and reading mail marks none of its
	// bookkeeping with the connection.
	s := svc(y, "openclaw")
	before, err := y.st.Head(y.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WhatWaitsForMe(y.ctx); err != nil {
		t.Fatal(err)
	}
	if err := y.st.Mail(y.ctx, "ada", "one letter", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadMyMail(y.ctx); err != nil {
		t.Fatal(err)
	}
	evs, err := y.st.EventsSince(y.ctx, before, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if store.Via(e) == "openclaw" {
			t.Errorf("a read left a marked event: %+v", e)
		}
	}
}

func TestFooterCounts(t *testing.T) {
	if got := mcp.Footer(0); got != "" {
		t.Errorf("Footer(0) = %q, want silence", got)
	}
	if got := mcp.Footer(1); !strings.Contains(got, "1 unread message waiting. Call read_my_mail to read it") {
		t.Errorf("Footer(1) = %q", got)
	}
	if got := mcp.Footer(3); !strings.Contains(got, "3 unread messages waiting. Call read_my_mail to read them") {
		t.Errorf("Footer(3) = %q", got)
	}
}

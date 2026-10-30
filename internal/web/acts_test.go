package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

type flowUnavailableStore struct{ store.API }

func (s flowUnavailableStore) NextStages(context.Context, string) (*store.NextStages, error) {
	return nil, errors.New("flow is unavailable")
}

// answerBarrier waits until both form submissions reach the store, so the
// test exercises two requests that passed the handler's read-side check
// before either writes.
type answerBarrier struct {
	store.API
	mu    sync.Mutex
	count int
	ready chan struct{}
}

func (s *answerBarrier) AddAnswerExpected(ctx context.Context, id string, answer store.Answer, actor, revision string) error {
	s.mu.Lock()
	s.count++
	if s.count == 2 {
		close(s.ready)
	}
	ready := s.ready
	s.mu.Unlock()
	<-ready
	return s.API.AddAnswerExpected(ctx, id, answer, actor, revision)
}

// actYard is a yard with the user jo, a depot, and a bead carrying an open
// ask to jo. It answers the bead and the ask's seq.
func actYard(t *testing.T, f *fixture, ask store.Ask) (*store.Bead, uint64) {
	t.Helper()
	ctx := context.Background()
	must(t, func() error { _, err := f.st.AddUser(ctx, "jo", "jo"); return err }())
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "jo"))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Depot: "home"}, "jo")
	must(t, err)
	ask.To = "jo"
	seq, err := f.st.AddAsk(ctx, b.ID, ask, "yardmaster")
	must(t, err)
	return b, seq
}

// actFlow gives the depot a flow with a decide stage a manual group works,
// so the bead waits for the view's user as `yardr review` walks it.
func actFlow(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"jo"}}, "jo"))
	fl, err := store.ParseFlow("new -> decide\ndecide -> merged on done | new on changes\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "jo")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "home", "", "code", "jo"))
	_, err = f.st.AddRoute(ctx, store.Route{Stage: "decide", Group: "decisions"}, "jo")
	must(t, err)
}

// revOf reads the bead's revision for a form.
func revOf(t *testing.T, f *fixture, id string) string {
	t.Helper()
	b, err := f.st.GetBead(context.Background(), id)
	must(t, err)
	return b.Revision
}

const secretActSource = "name: crm\nbase: https://crm.example.com/api\ncredential: { env: CRM_TOKEN }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"

// secretActYard is a yard with the depot home and the source crm, acting as
// its operator mckean, with a bead carrying an open secret ask to mckean.
// It answers the bead and the ask's seq.
func secretActYard(t *testing.T) (*fixture, *store.Bead, uint64) {
	t.Helper()
	f, _ := talkFixture(t, "mckean")
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "mckean"))
	must(t, f.st.SetSetting(ctx, "operator", "mckean", "mckean"))
	_, err := f.st.PutSource(ctx, "crm.yaml", secretActSource, "mckean")
	must(t, err)
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "the key", Stage: "new", Depot: "home"}, "mckean")
	must(t, err)
	seq, err := f.st.AddAsk(ctx, b.ID, store.Ask{Question: "The API key for the CRM", To: "mckean",
		Context: []string{"Create one in its settings."}, Secret: &store.AskSecret{Source: "crm"}}, "yardmaster")
	must(t, err)
	return f, b, seq
}

const mappingActSource = `name: talk
base: https://talk.example.com
credential: { env: TALK_TOKEN }
calls:
  message:
    kind: update
    params: { text: { type: string } }
    request: { method: POST, path: /send, body: { text: "{text}" } }
hook:
  verify: { scheme: secret, signature: { header: Talk-Secret } }
  bodies: unique
  normalize: [normalize]
  keys: [sender, channel]
hear:
  field: sender
  by: identity
`

// mappingActYard is a yard with the users mckean and ada, the depot home
// and the identity-hearing source talk, acting as its operator mckean,
// holding a mapping card for a sender no user maps. A mapping ask is the
// yard's own, so the card is written through the owning store, as
// ownedFixture's tests do. It answers the card bead and the ask's seq.
func mappingActYard(t *testing.T) (*fixture, *store.Bead, uint64) {
	t.Helper()
	api, own := storetest.NewOwned(t)
	home := filepath.Join(t.TempDir(), ".depot")
	srv, err := web.NewServer(api, home, nil)
	must(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: api, ts: ts, home: home}
	f.signIn(srv, srv.ActAs("mckean"))
	ctx := context.Background()
	must(t, func() error { _, err := f.st.AddUser(ctx, "mckean", "mckean"); return err }())
	must(t, func() error { _, err := f.st.AddUser(ctx, "ada", "ada"); return err }())
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "mckean"))
	must(t, f.st.SetSetting(ctx, "operator", "mckean", "mckean"))
	_, err = f.st.PutSource(ctx, "talk.yaml", mappingActSource, "mckean")
	must(t, err)
	created, err := own.ClaimIdentity(ctx, "talk", "U777", "C1", "hi")
	must(t, err)
	if !created {
		t.Fatal("ClaimIdentity offered no card")
	}
	if _, err := own.HoldIdentity(ctx, "talk", "talk", "U777", "C1", map[string]any{"text": "held-body-marker"}, []byte("held-body-marker"), nil); err != nil {
		t.Fatal(err)
	}
	q, err := f.st.UserAsks(ctx, "mckean", "mckean")
	must(t, err)
	for _, o := range q.Open {
		if o.Identity != nil && o.Identity.Sender == "U777" {
			b, err := f.st.GetBead(ctx, o.Bead)
			must(t, err)
			return f, b, o.Ask
		}
	}
	t.Fatal("mckean's queue holds no card for U777")
	return nil, nil, 0
}

// An asked bead's page, with --as, answers with buttons: the options, an
// Answer for words, Later and Decline. Without --as it shows no form.
func TestBeadAskButtons(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	b, seq := actYard(t, f, pickAsk())
	r := f.get("/beads/" + b.ID)
	for _, want := range []string{
		`<h3 class="label">asks for jo</h3>`,
		`<p class="ask-line">` + b.ID + ` · ask ` + strconv.FormatUint(seq, 10) + `</p>`,
		`<div class="ask-question"><p>Move the links now?</p><span class="asked">asked `,
		`<p class="ask-asker">asked by yardmaster</p>`,
		"The links file declares the new source.",
		`<li><span>1. Move them now</span><span class="faint"> — all links point at the new source tonight</span><span class="recommended">recommended: the test passed</span></li>`,
		`<li><span>2. After Friday</span>`,
		`<form class="need-answer" method="post" action="/beads/` + b.ID + `" hx-post="/beads/` + b.ID + `" hx-target="#bead"`,
		`<input type="hidden" name="rev" value="`,
		`<input type="hidden" name="ask" value="` + strconv.FormatUint(seq, 10) + `">`,
		`<button type="submit" name="option" value="1">Answer: 1 · Move them now</button>`,
		`<button type="submit" name="option" value="2">After Friday</button>`,
		`<button type="submit" name="action" value="send">Send</button>`,
		// The yard's own last pick, the shape a card stored before
		// yardr-17bj keeps: a plain button, since the recommendation is
		// another option's.
		`<button type="submit" name="option" value="3">You decide</button>`,
		`<button type="submit" name="action" value="later">Later</button>`,
		`<button type="submit" name="action" value="decline" title="Say no to all options. The asker finds another way.">Decline</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
		}
	}
	// A routine card carries no weight and no decline path.
	for _, foreign := range []string{`class="weighty"`, "WEIGHTY", "no gate", "Recommends", "Recommended:"} {
		if strings.Contains(r.body, foreign) {
			t.Errorf("the routine card shows %q:\n%s", foreign, r.body)
		}
	}
	// yardmaster is no crew member and no user of this yard, so the card
	// carries no Ask about this: there is no conversation to open.
	if strings.Contains(r.body, "Ask about this") {
		t.Errorf("the card links Ask about this at an asker nobody can write to:\n%s", r.body)
	}
	words := "Please leave the ask open while I check one more thing."
	if sent := f.postForm("/beads/"+b.ID, url.Values{
		"rev":    {revOf(t, f, b.ID)},
		"ask":    {strconv.FormatUint(seq, 10)},
		"words":  {words},
		"action": {"send"},
	}, "Origin", f.ts.URL); sent.status != http.StatusSeeOther {
		t.Fatalf("sending words as a note: status %d, want 303:\n%s", sent.status, sent.body)
	}
	notes, err := f.st.Notes(context.Background(), b.ID)
	must(t, err)
	if len(notes) == 0 || notes[len(notes)-1].Author != "jo" || notes[len(notes)-1].Text != words {
		t.Fatalf("sent note %+v, want jo's note %q", notes, words)
	}
	if page := f.get("/beads/" + b.ID).body; !strings.Contains(page, "Move the links now?") {
		t.Fatalf("sending words as a note closed the ask:\n%s", page)
	}
	if empty := f.postForm("/beads/"+b.ID, url.Values{
		"rev":    {revOf(t, f, b.ID)},
		"ask":    {strconv.FormatUint(seq, 10)},
		"words":  {"  \t"},
		"action": {"send"},
	}, "Origin", f.ts.URL); empty.status != http.StatusOK || !strings.Contains(empty.body, "a note needs words") {
		t.Fatalf("empty note response: status %d, want 200 and an explanation:\n%s", empty.status, empty.body)
	}
	// Where the asker is a crew member, it does.
	f4, _ := talkFixture(t, "jo")
	b4, _ := actYard(t, f4, pickAsk())
	must(t, func() error {
		_, err := f4.st.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo")
		return err
	}())
	if r := f4.get("/beads/" + b4.ID); !strings.Contains(r.body, `<a class="ask-about" href="/talk?with=yardmaster">Ask about this</a>`) {
		t.Errorf("the card to a crew member's asker lacks Ask about this:\n%s", r.body)
	}
	// And where the asker is another user, but not where the asker is the
	// viewing user themselves.
	f5, _ := talkFixture(t, "jo")
	ctx5 := context.Background()
	must(t, func() error { _, err := f5.st.AddUser(ctx5, "jo", "jo"); return err }())
	must(t, func() error { _, err := f5.st.AddUser(ctx5, "kay", "jo"); return err }())
	must(t, f5.st.PutDepot(ctx5, store.Depot{Name: "home", Base: "origin/main"}, "jo"))
	b5, err := f5.st.CreateBead(ctx5, store.NewBead{Title: "a door", Stage: "new", Depot: "home"}, "jo")
	must(t, err)
	ask := pickAsk()
	ask.To = "jo"
	if _, err := f5.st.AddAsk(ctx5, b5.ID, ask, "kay"); err != nil {
		t.Fatal(err)
	}
	if r := f5.get("/beads/" + b5.ID); !strings.Contains(r.body, `<a class="ask-about" href="/talk?with=kay">Ask about this</a>`) {
		t.Errorf("the card to another user's asker lacks Ask about this:\n%s", r.body)
	}
	b6, err := f5.st.CreateBead(ctx5, store.NewBead{Title: "a window", Stage: "new", Depot: "home"}, "jo")
	must(t, err)
	if _, err := f5.st.AddAsk(ctx5, b6.ID, ask, "jo"); err != nil {
		t.Fatal(err)
	}
	if r := f5.get("/beads/" + b6.ID); strings.Contains(r.body, "Ask about this") {
		t.Errorf("the card to the viewing user's own asker links Ask about this:\n%s", r.body)
	}
	// A words ask answers with words.
	f2, _ := talkFixture(t, "jo")
	b2, _ := actYard(t, f2, store.Ask{Question: "What should it say?", Context: []string{"the sign."}})
	r = f2.get("/beads/" + b2.ID)
	if !strings.Contains(r.body, `<button type="submit" name="action" value="answer">Answer</button>`) {
		t.Errorf("the words ask has no Answer:\n%s", r.body)
	}
	for _, foreign := range []string{`class="weighty"`, "WEIGHTY", "no gate"} {
		if strings.Contains(r.body, foreign) {
			t.Errorf("the routine card shows %q:\n%s", foreign, r.body)
		}
	}
	// Without --as the page shows the bead and no form.
	f3 := newFixture(t)
	ctx := context.Background()
	must(t, f3.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "me"))
	must(t, f3.st.SetSetting(ctx, "operator", "mckean", "me"))
	b3, err := f3.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Depot: "home"}, "me")
	must(t, err)
	if _, err := f3.st.AddAsk(ctx, b3.ID, pickAsk(), "yardmaster"); err != nil {
		t.Fatal(err)
	}
	// pickAsk asks mckean; the bead page without --as shows no asks either way.
	r = f3.get("/beads/" + b3.ID)
	if strings.Contains(r.body, "<form") || strings.Contains(r.body, "asks for") {
		t.Errorf("without --as the bead page answers:\n%s", r.body)
	}
}

// An ask remains available on the bead page even if the flow cannot be
// read: answering it does not depend on the flow.
func TestBeadAskVisibleWithBrokenFlow(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	b, seq := actYard(t, f, pickAsk())
	srv, err := web.NewServer(flowUnavailableStore{API: f.st}, f.home, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := srv.ActAs("jo")
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f.ts = ts
	f.signIn(srv, token)
	r := f.get("/beads/" + b.ID)
	for _, want := range []string{
		"flow is unavailable",
		"Move the links now?",
		`<input type="hidden" name="ask" value="` + strconv.FormatUint(seq, 10) + `">`,
		`<button type="submit" name="option" value="1">Answer: 1 · Move them now</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the bead page lacks %q:\n%s", want, r.body)
		}
	}
}

// A bead in a stage that waits for the view's user sends on as `yardr
// review` offers it, with a note. Otherwise the page names the outcomes
// as commands, as today.
func TestBeadMoveButtons(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	b, _ := actYard(t, f, pickAsk())
	actFlow(t, f)
	ctx := context.Background()
	moved, err := f.st.Advance(ctx, b.ID, "decide", store.Expect{}, "jo")
	must(t, err)
	r := f.get("/beads/" + b.ID)
	for _, want := range []string{
		`<h3 class="label">send on as jo</h3>`,
		`<form class="move-form" method="post" action="/beads/` + b.ID + `" hx-post="/beads/` + b.ID + `" hx-target="#bead"`,
		`<input type="hidden" name="rev" value="` + moved.Revision + `">`,
		`<button type="submit" name="outcome" value="done">done: finish as merged (closes the bead)</button>`,
		`<button type="submit" name="outcome" value="changes">changes: send back to new (note required)</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
		}
	}
	var got web.BeadPage
	if r := f.get("/beads/"+b.ID, "Accept", "application/json"); json.Unmarshal([]byte(r.body), &got) != nil {
		t.Fatal("the bead page is no JSON")
	} else if len(got.Moves) != 2 || got.Moves[0].Outcome != "done" || got.Moves[1].Outcome != "changes" || !got.Moves[1].NoteRequired {
		t.Errorf("the moves: %+v", got.Moves)
	}
	// An answered question offers its asker as a move on the bead page. The
	// answer is already in Notes, so returning needs no second note.
	f6, _ := talkFixture(t, "jo")
	ctx6 := context.Background()
	b6, _ := actYard(t, f6, pickAsk())
	must(t, f6.st.PutGroup(ctx6, store.Group{Name: "decisions", Runner: "manual", Members: []string{"jo"}}, "jo"))
	question, err := store.ParseFlow("new -> review\nreview -> decide on question\ndecide -> backlog | new\nbacklog -> new")
	must(t, err)
	question.Name = "question"
	_, err = f6.st.PutFlow(ctx6, *question, "jo")
	must(t, err)
	must(t, f6.st.SetFlowPointer(ctx6, "home", "", "question", "jo"))
	_, err = f6.st.AddRoute(ctx6, store.Route{Stage: "decide", Group: "decisions"}, "jo")
	must(t, err)
	if _, err := f6.st.Advance(ctx6, b6.ID, "review", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := f6.st.Done(ctx6, b6.ID, "question", "which route?", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	must(t, f6.st.AddNote(ctx6, b6.ID, "use the reviewed route", "jo"))
	r = f6.get("/beads/" + b6.ID)
	if want := `<button type="submit" name="to" value="review">Return to review</button>`; !strings.Contains(r.body, want) {
		t.Errorf("the page lacks the asker move %q:\n%s", want, r.body)
	}
	b6, err = f6.st.GetBead(ctx6, b6.ID)
	must(t, err)
	r = f6.postForm("/beads/"+b6.ID, url.Values{"rev": {b6.Revision}, "to": {"review"}}, "Origin", f6.ts.URL)
	if r.status != http.StatusSeeOther {
		t.Fatalf("return to asker without another note: status %d\n%s", r.status, r.body)
	}
	b6, err = f6.st.GetBead(ctx6, b6.ID)
	must(t, err)
	if b6.Stage != "review" {
		t.Fatalf("the asker move left the bead at %s", b6.Stage)
	}
	notes, err := f6.st.Notes(ctx6, b6.ID)
	must(t, err)
	answerFound := false
	for _, note := range notes {
		if note.Text == "use the reviewed route" {
			answerFound = true
		}
	}
	if !answerFound {
		t.Fatal("the answer disappeared on the asker move")
	}

	// At a round limit the send-on button names where the report really
	// goes: decide, with the rounds used.
	f4, _ := talkFixture(t, "jo")
	ctx4 := context.Background()
	must(t, func() error { _, err := f4.st.AddUser(ctx4, "jo", "jo"); return err }())
	must(t, f4.st.PutDepot(ctx4, store.Depot{Name: "home", Base: "origin/main"}, "jo"))
	must(t, f4.st.PutGroup(ctx4, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"jo"}}, "jo"))
	limited, err := store.ParseFlow("new -> review\nreview -> merged on done | new on changes max 1 then decide\ndecide -> new | review\nmerged terminal\n")
	must(t, err)
	limited.Name = "code"
	_, err = f4.st.PutFlow(ctx4, *limited, "jo")
	must(t, err)
	must(t, f4.st.SetFlowPointer(ctx4, "home", "", "code", "jo"))
	_, err = f4.st.AddRoute(ctx4, store.Route{Stage: "review", Group: "reviewers"}, "jo")
	must(t, err)
	b4, err := f4.st.CreateBead(ctx4, store.NewBead{Title: "a door", Stage: "new", Depot: "home"}, "jo")
	must(t, err)
	if _, err := f4.st.Advance(ctx4, b4.ID, "review", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := f4.st.Done(ctx4, b4.ID, "changes", "once more", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := f4.st.Advance(ctx4, b4.ID, "review", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	r = f4.get("/beads/" + b4.ID)
	if want := `<button type="submit" name="outcome" value="changes">changes: move to decide (the 1 round back to new is used)</button>`; !strings.Contains(r.body, want) {
		t.Errorf("the page lacks %q:\n%s", want, r.body)
	}
	terminal, err := store.ParseFlow("new -> review\nreview -> new on changes max 1 then merged\nmerged terminal\n")
	must(t, err)
	terminal.Name = "ending"
	_, err = f4.st.PutFlow(ctx4, *terminal, "jo")
	must(t, err)
	must(t, f4.st.SetFlowPointer(ctx4, "home", "", "ending", "jo"))
	b5, err := f4.st.CreateBead(ctx4, store.NewBead{Title: "a terminal return", Stage: "new", Depot: "home"}, "jo")
	must(t, err)
	if _, err := f4.st.Advance(ctx4, b5.ID, "review", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := f4.st.Done(ctx4, b5.ID, "changes", "once more", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := f4.st.Advance(ctx4, b5.ID, "review", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	r = f4.get("/beads/"+b5.ID, "Accept", "application/json")
	var terminalPage web.BeadPage
	if err := json.Unmarshal([]byte(r.body), &terminalPage); err != nil {
		t.Fatal("the terminal bead page is no JSON")
	}
	if len(terminalPage.Moves) != 1 || terminalPage.Moves[0].Outcome != "changes" || terminalPage.Moves[0].To != "merged" || !terminalPage.Moves[0].Terminal || terminalPage.Moves[0].NoteRequired || terminalPage.Moves[0].Label != "changes: finish as merged (closes the bead) (the 1 round back to new is used)" {
		t.Errorf("terminal round redirect move = %+v", terminalPage.Moves)
	}
	// A bead that waits for another group offers no move.
	f2, _ := talkFixture(t, "ada")
	ctx2 := context.Background()
	must(t, func() error { _, err := f2.st.AddUser(ctx2, "ada", "ada"); return err }())
	must(t, f2.st.PutDepot(ctx2, store.Depot{Name: "home", Base: "origin/main"}, "ada"))
	b2, err := f2.st.CreateBead(ctx2, store.NewBead{Title: "a door", Stage: "decide", Depot: "home"}, "ada")
	must(t, err)
	actFlow(t, f2)
	r = f2.get("/beads/" + b2.ID)
	if strings.Contains(r.body, "move-form") || strings.Contains(r.body, "send on as") {
		t.Errorf("the bead waits for decisions, and ada may send it:\n%s", r.body)
	}
	// Without --as the outcomes read as commands.
	f3 := newFixture(t)
	ctx3 := context.Background()
	must(t, f3.st.PutDepot(ctx3, store.Depot{Name: "home", Base: "origin/main"}, "me"))
	b3, err := f3.st.CreateBead(ctx3, store.NewBead{Title: "a door", Stage: "decide", Depot: "home"}, "me")
	must(t, err)
	fl, err := store.ParseFlow("new -> decide\ndecide -> merged on done | new on changes\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f3.st.PutFlow(ctx3, *fl, "me")
	must(t, err)
	must(t, f3.st.SetFlowPointer(ctx3, "home", "", "code", "me"))
	r = f3.get("/beads/" + b3.ID)
	if !strings.Contains(r.body, "<code>yardr bead done "+b3.ID+" --outcome changes</code>") || strings.Contains(r.body, "move-form") {
		t.Errorf("without --as the outcomes are not commands:\n%s", r.body)
	}
}

// A pick through the bead's form lands as the view's user; Later and
// Decline do too.
func TestBeadAnswerPost(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	b, seq := actYard(t, f, pickAsk())
	rev := revOf(t, f, b.ID)
	form := url.Values{"rev": {rev}, "ask": {strconv.FormatUint(seq, 10)}, "option": {"2"}}
	r := f.postForm("/beads/"+b.ID, form, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/beads/"+b.ID {
		t.Fatalf("status %d, location %q, want 303 to the bead", r.status, r.header.Get("Location"))
	}
	v, err := f.st.Bead(context.Background(), b.ID)
	must(t, err)
	if len(v.Asks) != 1 || v.Asks[0].State != "answered" || v.Asks[0].Option != 2 {
		t.Fatalf("the ask: %+v", v.Asks)
	}
	notes, err := f.st.Notes(context.Background(), b.ID)
	must(t, err)
	if last := notes[len(notes)-1]; last.Author != "jo" {
		t.Errorf("the answer is %s's, want jo's", last.Author)
	}
	// Later puts it aside; Decline ends it with the words.
	f2, _ := talkFixture(t, "jo")
	b2, seq2 := actYard(t, f2, pickAsk())
	rev2 := revOf(t, f2, b2.ID)
	r = f2.postForm("/beads/"+b2.ID, url.Values{"rev": {rev2}, "ask": {strconv.FormatUint(seq2, 10)}, "action": {"later"}}, "Origin", f2.ts.URL)
	if r.status != http.StatusSeeOther {
		t.Fatalf("later: status %d", r.status)
	}
	v, _ = f2.st.Bead(context.Background(), b2.ID)
	if v.Asks[0].State != "put aside" {
		t.Errorf("later: the ask is %s", v.Asks[0].State)
	}
	f3, _ := talkFixture(t, "jo")
	b3, seq3 := actYard(t, f3, pickAsk())
	rev3 := revOf(t, f3, b3.ID)
	r = f3.postForm("/beads/"+b3.ID, url.Values{"rev": {rev3}, "ask": {strconv.FormatUint(seq3, 10)}, "action": {"decline"}, "words": {"not mine"}}, "Origin", f3.ts.URL)
	if r.status != http.StatusSeeOther {
		t.Fatalf("decline: status %d", r.status)
	}
	v, _ = f3.st.Bead(context.Background(), b3.ID)
	if v.Asks[0].State != "declined" || v.Asks[0].Words != "not mine" {
		t.Errorf("decline: %+v", v.Asks[0])
	}
}

// A move through the bead's form sends the bead on with the note as the
// view's user.
func TestBeadMovePost(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	b, _ := actYard(t, f, pickAsk())
	actFlow(t, f)
	ctx := context.Background()
	_, err := f.st.Advance(ctx, b.ID, "decide", store.Expect{}, "jo")
	must(t, err)
	rev := revOf(t, f, b.ID)
	r := f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "outcome": {"done"}, "note": {"looks right"}}, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/beads/"+b.ID {
		t.Fatalf("status %d, location %q, want 303 to the bead", r.status, r.header.Get("Location"))
	}
	got, err := f.st.GetBead(ctx, b.ID)
	must(t, err)
	if got.Stage != "merged" || got.Status != "closed" {
		t.Errorf("the bead is in %s (%s), want merged (closed)", got.Stage, got.Status)
	}
	notes, err := f.st.Notes(ctx, b.ID)
	must(t, err)
	found := false
	for _, n := range notes {
		if n.Author == "jo" && strings.Contains(n.Text, "looks right") {
			found = true
		}
	}
	if !found {
		t.Error("jo's note is not on the bead")
	}
}

// Two tabs answering the same ask leave one answer and one refusal: the
// second tab's revision is stale, and the page comes back redrawn with
// why. A move from a stale page is refused the same way.
func TestBeadStaleRevision(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	b, seq := actYard(t, f, pickAsk())
	rev := revOf(t, f, b.ID)
	ask := strconv.FormatUint(seq, 10)
	if r := f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {ask}, "option": {"1"}}, "Origin", f.ts.URL); r.status != http.StatusSeeOther {
		t.Fatalf("first tab: status %d", r.status)
	}
	r := f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {ask}, "option": {"2"}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "is at revision") || !strings.Contains(r.body, "read it again") {
		t.Errorf("second tab: status %d, want the redrawn page with why:\n%s", r.status, r.body)
	}
	// The stale pick wrote nothing: the ask still carries the first.
	v, err := f.st.Bead(context.Background(), b.ID)
	must(t, err)
	if v.Asks[0].Option != 1 {
		t.Errorf("the ask carries option %d, want the first tab's 1", v.Asks[0].Option)
	}
	// A stale move is refused with the redrawn bead.
	f2, _ := talkFixture(t, "jo")
	b2, _ := actYard(t, f2, pickAsk())
	actFlow(t, f2)
	ctx := context.Background()
	_, err = f2.st.Advance(ctx, b2.ID, "decide", store.Expect{}, "jo")
	must(t, err)
	rev2 := revOf(t, f2, b2.ID)
	must(t, f2.st.AddNote(ctx, b2.ID, "a note from elsewhere", "yardmaster"))
	r = f2.postForm("/beads/"+b2.ID, url.Values{"rev": {rev2}, "outcome": {"done"}, "note": {"stale"}}, "Origin", f2.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "is at revision") {
		t.Errorf("stale move: status %d, want the redrawn page with why:\n%s", r.status, r.body)
	}
	if got, _ := f2.st.GetBead(ctx, b2.ID); got.Stage != "decide" {
		t.Errorf("the stale move sent the bead to %s", got.Stage)
	}
}

// Two tabs can submit the same revision at once. The store compares the
// revision in the write, so one answer lands and the other is redrawn as
// stale instead of overwriting the first pick.
func TestBeadConcurrentAnswers(t *testing.T) {
	t.Parallel()
	base, _ := talkFixture(t, "jo")
	b, seq := actYard(t, base, pickAsk())
	barrier := &answerBarrier{API: base.st, ready: make(chan struct{})}
	srv, err := web.NewServer(barrier, base.home, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := srv.ActAs("jo")
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: base.st, ts: ts, home: base.home}
	f.signIn(srv, token)
	rev := revOf(t, f, b.ID)
	var wg sync.WaitGroup
	responses := make(chan resp, 2)
	for _, option := range []string{"1", "2"} {
		wg.Add(1)
		go func(option string) {
			defer wg.Done()
			form := url.Values{"rev": {rev}, "ask": {strconv.FormatUint(seq, 10)}, "option": {option}}
			responses <- f.postForm("/beads/"+b.ID, form, "Origin", f.ts.URL)
		}(option)
	}
	wg.Wait()
	close(responses)
	succeeded, refused := 0, 0
	for r := range responses {
		switch {
		case r.status == http.StatusSeeOther:
			succeeded++
		case r.status == http.StatusOK && strings.Contains(r.body, "is at revision"):
			refused++
		default:
			t.Errorf("unexpected response: status %d\n%s", r.status, r.body)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("same-revision submissions: %d succeeded, %d refused; want one of each", succeeded, refused)
	}
	notes, err := f.st.Notes(context.Background(), b.ID)
	must(t, err)
	if len(notes) != 2 || notes[1].Author != "jo" {
		t.Errorf("notes after concurrent answers: %+v; want one answer by jo", notes)
	}
}

// Sending back without a note is refused, as `yardr review` refuses it;
// a forged move, one the page offers no button for, is refused too.
func TestBeadMoveRefused(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	b, _ := actYard(t, f, pickAsk())
	actFlow(t, f)
	ctx := context.Background()
	_, err := f.st.Advance(ctx, b.ID, "decide", store.Expect{}, "jo")
	must(t, err)
	rev := revOf(t, f, b.ID)
	r := f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "outcome": {"changes"}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "needs a note") {
		t.Errorf("send back without a note: status %d, want the reason:\n%s", r.status, r.body)
	}
	if got, _ := f.st.GetBead(ctx, b.ID); got.Stage != "decide" {
		t.Errorf("the bead moved to %s without its note", got.Stage)
	}
	r = f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "outcome": {"nosuch"}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "does not wait") {
		t.Errorf("forged move: status %d, want a refusal:\n%s", r.status, r.body)
	}
}

// A POST from another origin, and any POST without --as, writes nothing.
func TestBeadPostRefused(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	b, seq := actYard(t, f, pickAsk())
	rev := revOf(t, f, b.ID)
	form := url.Values{"rev": {rev}, "ask": {strconv.FormatUint(seq, 10)}, "option": {"1"}}
	for _, origin := range []string{"", "http://evil.example/"} {
		if r := f.postForm("/beads/"+b.ID, form, "Origin", origin); r.status != http.StatusForbidden {
			t.Errorf("origin %q: status %d, want 403", origin, r.status)
		}
	}
	v, _ := f.st.Bead(context.Background(), b.ID)
	if v.Asks[0].State != "open" {
		t.Error("another origin answered the ask")
	}
	f2 := newFixture(t)
	if r := f2.postForm("/beads/abc", form, "Origin", f2.ts.URL); r.status != http.StatusMethodNotAllowed {
		t.Errorf("without --as: status %d, want 405", r.status)
	}
}

// A plain form submission — the page's own form posted without script,
// as a browser sends it from a page served under Referrer-Policy
// same-origin, carrying the view's own Origin — is taken. Under
// no-referrer the browser would send Origin null for a navigate-mode
// POST (Fetch, "append a request Origin header") and ownOrigin would
// refuse it; the policy is why this POST carries an origin at all.
func TestBeadPostPlainFormCarriesOwnOrigin(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	b, seq := actYard(t, f, pickAsk())
	rev := revOf(t, f, b.ID)
	r := f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {strconv.FormatUint(seq, 10)}, "option": {"1"}}, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/beads/"+b.ID {
		t.Fatalf("status %d, location %q, want 303 to the bead", r.status, r.header.Get("Location"))
	}
	v, err := f.st.Bead(context.Background(), b.ID)
	must(t, err)
	if len(v.Asks) != 1 || v.Asks[0].State != "answered" || v.Asks[0].Option != 1 {
		t.Fatalf("the ask: %+v", v.Asks)
	}
}

// The needs rows answer where the view acts: home's oldest ask under its
// row, /line's every ask under its reason. Without --as neither answers.
func TestNeedsAnswerButtons(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	ctx := context.Background()
	must(t, func() error { _, err := f.st.AddUser(ctx, "jo", "jo"); return err }())
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "jo"))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Depot: "home"}, "jo")
	must(t, err)
	ask := pickAsk()
	ask.To = "jo"
	seq, err := f.st.AddAsk(ctx, b.ID, ask, "yardmaster")
	must(t, err)
	r := f.get("/")
	for _, want := range []string{
		`<form class="need-answer" method="post" action="/beads/` + b.ID + `">`,
		`<input type="hidden" name="ask" value="` + strconv.FormatUint(seq, 10) + `">`,
		`<button type="submit" name="option" value="1">Answer: 1 · Move them now</button>`,
		`<button type="submit" name="action" value="send">Send</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("home lacks %q:\n%s", want, r.body)
		}
	}
	line := f.get("/line")
	if !strings.Contains(line.body, `<form class="need-answer"`) {
		t.Errorf("/line answers nothing:\n%s", line.body)
	}
	if !strings.Contains(line.body, `<button type="submit" name="action" value="send">Send</button>`) {
		t.Errorf("/line has no Send button:\n%s", line.body)
	}
	// yardmaster is no crew member and no user here, so neither row links
	// Ask about this; where they are one, both rows do.
	for _, c := range []struct{ target, body string }{{"/", r.body}, {"/line", line.body}} {
		if strings.Contains(c.body, "Ask about this") {
			t.Errorf("%s links Ask about this at an asker nobody can write to:\n%s", c.target, c.body)
		}
	}
	must(t, func() error {
		_, err := f.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo")
		return err
	}())
	for _, target := range []string{"/", "/line"} {
		if r := f.get(target); !strings.Contains(r.body, `<a class="ask-about" href="/talk?with=yardmaster">Ask about this</a>`) {
			t.Errorf("%s lacks Ask about this to the crew asker's conversation:\n%s", target, r.body)
		}
	}
	// The needs answer lands as the view's user.
	rev := revOf(t, f, b.ID)
	r = f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {strconv.FormatUint(seq, 10)}, "option": {"1"}}, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther {
		t.Fatalf("needs answer: status %d", r.status)
	}
	v, _ := f.st.Bead(ctx, b.ID)
	if v.Asks[0].State != "answered" {
		t.Error("the needs answer did not land")
	}
	// Without --as the rows only link.
	f2 := newFixture(t)
	ctx2 := context.Background()
	must(t, f2.st.PutDepot(ctx2, store.Depot{Name: "home", Base: "origin/main"}, "me"))
	must(t, f2.st.SetSetting(ctx2, "operator", "mckean", "me"))
	b2, err := f2.st.CreateBead(ctx2, store.NewBead{Title: "a door", Stage: "new", Depot: "home"}, "me")
	must(t, err)
	if _, err := f2.st.AddAsk(ctx2, b2.ID, pickAsk(), "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if r := f2.get("/"); strings.Contains(r.body, "need-answer") {
		t.Error("without --as home answers")
	}
	if r := f2.get("/line"); strings.Contains(r.body, "need-answer") {
		t.Error("without --as /line answers")
	}
}

// A secret ask is weighty, and the page has no gate to put in front of a
// weighty card, so its card shows the weight first and the decline path
// instead of a giving: no field that does not echo, no Give button. Later
// and Decline stay, and the terminal command beside is where the card is
// given.
func TestBeadSecretWeightyCard(t *testing.T) {
	t.Parallel()
	f, b, seq := secretActYard(t)
	r := f.get("/beads/" + b.ID)
	for _, want := range []string{
		`<h3 class="label">asks for mckean</h3>`,
		`<p class="weighty">WEIGHTY · credential: gives a credential or an address to the yard: CRM_TOKEN, for the source crm (https://crm.example.com)</p>`,
		`a secret: CRM_TOKEN, for the source crm (https://crm.example.com)`,
		`This card is weighty and this window has no gate. Approve it in footplate, or in a terminal on the yard's machine:`,
		`Give it with <code>yardr bead answer ` + b.ID + ` ` + strconv.FormatUint(seq, 10) + ` --secret</code>`,
		`<button type="submit" name="action" value="later">Later</button>`,
		`<button type="submit" name="action" value="decline" title="Say no to all options. The asker finds another way.">Decline</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
		}
	}
	// No giving, and no words: a secret ask takes none, so the page
	// offers no field they could be typed into by mistake.
	for _, gone := range []string{`type="password"`, `value="give"`, `name="words"`} {
		if strings.Contains(r.body, gone) {
			t.Errorf("the weighty card offers %q:\n%s", gone, r.body)
		}
	}
	// Later still posts: putting the card aside needs no word.
	rev := revOf(t, f, b.ID)
	if r := f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {strconv.FormatUint(seq, 10)}, "action": {"later"}}, "Origin", f.ts.URL); r.status != http.StatusSeeOther {
		t.Fatalf("later: status %d", r.status)
	}
	v, err := f.st.Bead(context.Background(), b.ID)
	must(t, err)
	if v.Asks[0].State != "put aside" {
		t.Errorf("later: the ask is %s", v.Asks[0].State)
	}
}

// A giving posted straight at the handler is refused: a secret ask is
// weighty, and the page has no gate to put in front of it, so the post
// says nothing of its person's having seen the weight (weighed). The yard
// refuses in its own words, which the redrawn page shows with the card's
// decline path and no giving; nothing is written, the ask stays open, and
// the value is in nothing that comes back.
func TestBeadSecretGiveIsRefusedAsWeighty(t *testing.T) {
	t.Parallel()
	f, b, seq := secretActYard(t)
	ctx := context.Background()
	rev := revOf(t, f, b.ID)
	const value = "the-crm-key"
	r := f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {strconv.FormatUint(seq, 10)},
		"secret-CRM_TOKEN": {value}, "action": {"give"}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "is weighty (credential)") || !strings.Contains(r.body, "approve it where its weight is shown") {
		t.Fatalf("status %d, want the redrawn page with the yard's refusal:\n%s", r.status, r.body)
	}
	if strings.Contains(r.body, value) {
		t.Errorf("the redrawn page holds the value:\n%s", r.body)
	}
	if !strings.Contains(r.body, "This card is weighty and this window has no gate.") {
		t.Errorf("the redrawn page shows no decline path:\n%s", r.body)
	}
	for _, gone := range []string{`type="password"`, `value="give"`} {
		if strings.Contains(r.body, gone) {
			t.Errorf("the redrawn page offers %q:\n%s", gone, r.body)
		}
	}
	src, err := f.st.GetSource(ctx, "crm")
	must(t, err)
	if src.EnvFile != "" {
		t.Errorf("the source's env_file is %q after a refused giving", src.EnvFile)
	}
	notes, err := f.st.Notes(ctx, b.ID)
	must(t, err)
	for _, n := range notes {
		if strings.Contains(n.Text, value) {
			t.Errorf("note %d holds the value: %q", n.Seq, n.Text)
		}
	}
	v, err := f.st.Bead(ctx, b.ID)
	must(t, err)
	if len(v.Asks) != 1 || v.Asks[0].State != "open" || len(v.Asks[0].Given) != 0 {
		t.Fatalf("the ask: %+v, want it open and given nothing", v.Asks)
	}
}

// A login-input ask is a secret ask, so its card shows the weight first
// and the decline path instead of a giving, and a giving posted straight
// at the handler is refused as any secret ask's is: the value is handed
// to no waiting login, and is in no note.
func TestBeadSecretLoginGiveIsRefusedAsWeighty(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "mckean")
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "mckean"))
	must(t, f.st.SetSetting(ctx, "operator", "mckean", "mckean"))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "the login", Stage: "new", Depot: "home"}, "mckean")
	must(t, err)
	seq, err := f.st.AddAsk(ctx, b.ID, store.Ask{Question: "The code the sign-in page showed", To: "mckean",
		Context: []string{"The login waits for what the sign-in page showed."},
		Secret:  &store.AskSecret{Login: "claude-a1b2c3d4"}}, "yardmaster")
	must(t, err)
	r := f.get("/beads/" + b.ID)
	for _, want := range []string{
		`a secret: INPUT, for the waiting login claude-a1b2c3d4`,
		`<p class="weighty">WEIGHTY · credential:`,
		`This card is weighty and this window has no gate.`,
		`Give it with <code>yardr bead answer ` + b.ID + ` ` + strconv.FormatUint(seq, 10) + ` --secret</code>`,
		`<button type="submit" name="action" value="later">Later</button>`,
		`<button type="submit" name="action" value="decline" title="Say no to all options. The asker finds another way.">Decline</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
		}
	}
	for _, gone := range []string{`type="password"`, `value="give"`} {
		if strings.Contains(r.body, gone) {
			t.Errorf("the weighty card offers %q:\n%s", gone, r.body)
		}
	}
	rev := revOf(t, f, b.ID)
	const value = "the-login-code"
	r = f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {strconv.FormatUint(seq, 10)},
		"secret-INPUT": {value}, "action": {"give"}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "is weighty (credential)") || strings.Contains(r.body, value) {
		t.Fatalf("status %d, want the redrawn page with the yard's refusal and no value:\n%s", r.status, r.body)
	}
	if !strings.Contains(r.body, "This card is weighty and this window has no gate.") {
		t.Errorf("the redrawn page shows no decline path:\n%s", r.body)
	}
	notes, err := f.st.Notes(ctx, b.ID)
	must(t, err)
	for _, n := range notes {
		if strings.Contains(n.Text, value) {
			t.Errorf("note %d holds the value: %q", n.Seq, n.Text)
		}
	}
	if last := notes[len(notes)-1]; last.Kind == store.NoteAnswer {
		t.Errorf("the last note is %+v, want no answer", last)
	}
}

// A weighty mapping card's picks write a user's identity, which the yard
// refuses without its person's word that the weight was seen, and the
// page has no gate to ask for it: the card shows the weight first and
// the decline path with the terminal command instead of the pick buttons.
// The options still read as a list, and the words stay with Later and
// Decline, riding them as the reason.
func TestBeadMappingWeightyCard(t *testing.T) {
	t.Parallel()
	f, b, seq := mappingActYard(t)
	ctx := context.Background()
	ask := strconv.FormatUint(seq, 10)
	r := f.get("/beads/" + b.ID)
	if apiPage := f.get("/beads/"+b.ID, "Accept", "application/json"); strings.Contains(apiPage.body, "held-body-marker") {
		t.Fatalf("the page JSON carries the held body: %s", apiPage.body)
	}
	for _, want := range []string{
		`<h3 class="label">asks for mckean</h3>`,
		`<p class="weighty">WEIGHTY · as-user: lets calls or words count as a person&#39;s: a pick says which user U777 in talk is, and a delivery from them is then heard as that user&#39;s</p>`,
		`A message reached talk from U777 in channel C1. Who is this?`,
		`<h4 class="label">Held messages · 1</h4>`,
		`<pre class="text held-payload">held-body-marker</pre>`,
		`<li><span>1. ada</span>`,
		`<li><span>2. mckean</span>`,
		`This card is weighty and this window has no gate. Approve it in footplate, or in a terminal on the yard's machine:`,
		`Approve it with <code>yardr bead answer ` + b.ID + ` ` + ask + ` &lt;n&gt;</code>`,
		`<input type="text" name="words"`,
		`<button type="submit" name="action" value="later">Later</button>`,
		`<button type="submit" name="action" value="decline" title="Say no to all options. The asker finds another way.">Decline</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
		}
	}
	if strings.Contains(r.body, `name="option"`) {
		t.Errorf("the weighty card offers its picks:\n%s", r.body)
	}
	// A writing pick posted straight at the handler is still the yard's
	// to refuse: the redrawn page carries the refusal with the decline
	// path and no pick, and the ask stays open.
	rev := revOf(t, f, b.ID)
	r = f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {ask}, "option": {"1"}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "is weighty (as-user)") || !strings.Contains(r.body, "approve it where its weight is shown") {
		t.Fatalf("status %d, want the redrawn page with the yard's refusal:\n%s", r.status, r.body)
	}
	if !strings.Contains(r.body, "This card is weighty and this window has no gate.") || strings.Contains(r.body, `name="option"`) {
		t.Errorf("the redrawn page is no decline path:\n%s", r.body)
	}
	v, err := f.st.Bead(ctx, b.ID)
	must(t, err)
	if v.Asks[0].State != "open" {
		t.Fatalf("the ask is %s after a refused pick", v.Asks[0].State)
	}
	// Decline with words still posts: the words are why not.
	r = f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {ask}, "words": {"not now"}, "action": {"decline"}}, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther {
		t.Fatalf("decline: status %d", r.status)
	}
	v, err = f.st.Bead(ctx, b.ID)
	must(t, err)
	if v.Asks[0].State != "declined" || v.Asks[0].Words != "not now" {
		t.Errorf("the ask is %s with %q, want it declined with the words", v.Asks[0].State, v.Asks[0].Words)
	}
}

// A giving with no value, and one to an ask that asks for no secret, are
// refused with the page redrawn, and write nothing.
func TestBeadSecretGiveRefused(t *testing.T) {
	t.Parallel()
	f, b, seq := secretActYard(t)
	ctx := context.Background()
	rev := revOf(t, f, b.ID)
	ask := strconv.FormatUint(seq, 10)
	r := f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {ask}, "action": {"give"}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "CRM_TOKEN was not given") {
		t.Errorf("no value: status %d, want the redrawn page with why:\n%s", r.status, r.body)
	}
	v, err := f.st.Bead(ctx, b.ID)
	must(t, err)
	if v.Asks[0].State != "open" {
		t.Errorf("no value: the ask is %s", v.Asks[0].State)
	}
	f2, _ := talkFixture(t, "jo")
	b2, seq2 := actYard(t, f2, pickAsk())
	rev2 := revOf(t, f2, b2.ID)
	r = f2.postForm("/beads/"+b2.ID, url.Values{"rev": {rev2}, "ask": {strconv.FormatUint(seq2, 10)}, "action": {"give"}}, "Origin", f2.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "asks for no secret") {
		t.Errorf("no secret: status %d, want a refusal:\n%s", r.status, r.body)
	}
	v, err = f2.st.Bead(ctx, b2.ID)
	must(t, err)
	if v.Asks[0].State != "open" {
		t.Errorf("no secret: the ask is %s", v.Asks[0].State)
	}
}

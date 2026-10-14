package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

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

// An asked bead's page, with --as, answers with buttons: the options, an
// Answer for words, Later and Decline. Without --as it shows no form.
func TestBeadAskButtons(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	b, seq := actYard(t, f, pickAsk())
	r := f.get("/beads/" + b.ID)
	for _, want := range []string{
		`<h3 class="label">asks for jo</h3>`,
		"Move the links now?",
		"The links file declares the new source.",
		`<li><span>1. Move them now</span>`,
		`<li><span>2. After Friday</span>`,
		`Recommends 1: the test passed`,
		`<form class="need-answer" method="post" action="/beads/` + b.ID + `" hx-post="/beads/` + b.ID + `" hx-target="#bead"`,
		`<input type="hidden" name="rev" value="`,
		`<input type="hidden" name="ask" value="` + strconv.FormatUint(seq, 10) + `">`,
		`<button type="submit" name="option" value="1">Move them now</button>`,
		`<button type="submit" name="option" value="2">After Friday</button>`,
		`<button type="submit" name="action" value="later">Later</button>`,
		`<button type="submit" name="action" value="decline">Decline</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
		}
	}
	// A words ask answers with words.
	f2, _ := talkFixture(t, "jo")
	b2, _ := actYard(t, f2, store.Ask{Question: "What should it say?", Context: []string{"the sign."}})
	r = f2.get("/beads/" + b2.ID)
	if !strings.Contains(r.body, `<button type="submit" name="action" value="answer">Answer</button>`) {
		t.Errorf("the words ask has no Answer:\n%s", r.body)
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

// A bead in a stage that waits for the view's user sends on as `yardr
// review` offers it, with a note. Otherwise the page names the outcomes
// as commands, as today.
func TestBeadMoveButtons(t *testing.T) {
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

// Sending back without a note is refused, as `yardr review` refuses it;
// a forged move, one the page offers no button for, is refused too.
func TestBeadMoveRefused(t *testing.T) {
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

// The needs rows answer where the view acts: home's oldest ask under its
// row, /line's every ask under its reason. Without --as neither answers.
func TestNeedsAnswerButtons(t *testing.T) {
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
		`<button type="submit" name="option" value="1">Move them now</button>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("home lacks %q:\n%s", want, r.body)
		}
	}
	if r := f.get("/line"); !strings.Contains(r.body, `<form class="need-answer"`) {
		t.Errorf("/line answers nothing:\n%s", r.body)
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

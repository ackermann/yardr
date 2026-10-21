package web_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
	"yardr.dev/yardr/internal/yardkey"
)

// askFixture is a yard with an operator and a depot, ready for asks.
func askFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t)
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "me"))
	must(t, f.st.SetSetting(ctx, "operator", "mckean", "me"))
	return f
}

func askBead(t *testing.T, f *fixture, title, stage string, labels ...string) *store.Bead {
	t.Helper()
	b, err := f.st.CreateBead(context.Background(), store.NewBead{
		Title: title, Stage: stage, Depot: "home", Labels: labels,
	}, "me")
	must(t, err)
	return b
}

func askOn(t *testing.T, f *fixture, id string, ask store.Ask) uint64 {
	t.Helper()
	seq, err := f.st.AddAsk(context.Background(), id, ask, "yardmaster")
	must(t, err)
	return seq
}

func pickAsk() store.Ask {
	return store.Ask{
		Question: "Move the links now?",
		To:       "mckean",
		Context: []string{
			"The links file declares the new source.",
			"The old source goes away Friday.",
		},
		Options: []store.AskOption{
			{Label: "Move them now", Consequence: "all links point at the new source tonight"},
			{Label: "After Friday", Consequence: "keep the old source until the release is out"},
		},
		Recommend: 1,
		Why:       "the test passed",
	}
}

// A bead with an open ask and no other wait stands in the home list: the
// question first, the bead small, the recommendation and the age faint.
// It counts as a need, not a fault.
func TestHomeNeedsAsk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := askFixture(t)
	b := askBead(t, f, "a door", "new")
	askOn(t, f, b.ID, pickAsk())

	h := homeJSON(t, f, "/")
	if len(h.Attention) != 1 || h.Attention[0].Bead != b.ID {
		t.Fatalf("attention %+v, want the asked bead", h.Attention)
	}
	w := h.Attention[0]
	if len(w.Why) != 1 || w.Why[0].Kind != "ask" || w.Why[0].Text != "Move the links now?" {
		t.Fatalf("why %+v, want the ask's question", w.Why)
	}
	wantNote := "The links file declares the new source.\nThe old source goes away Friday."
	if w.Why[0].Note != wantNote {
		t.Errorf("note %q, want the ask's context", w.Why[0].Note)
	}
	// No new field: the reason's JSON is kind, text and note, as before,
	// and the waiting bead carries no topic.
	var l struct {
		Waits []struct {
			Why []map[string]any `json:"why"`
		} `json:"waits"`
		Attn []map[string]any `json:"-"`
	}
	r := f.get("/line", "Accept", "application/json")
	if err := json.Unmarshal([]byte(r.body), &l); err != nil {
		t.Fatal(err)
	}
	for k := range l.Waits[0].Why[0] {
		if k != "kind" && k != "text" && k != "note" {
			t.Errorf("the ask reason's JSON has %q", k)
		}
	}
	var home map[string]any
	r = f.get("/", "Accept", "application/json")
	if err := json.Unmarshal([]byte(r.body), &home); err != nil {
		t.Fatal(err)
	}
	attn, _ := home["attention"].([]any)
	if len(attn) != 1 {
		t.Fatalf("attention has %d rows, want one", len(attn))
	}
	for k := range attn[0].(map[string]any) {
		switch k {
		case "bead", "title", "depot", "stage", "since", "why":
		default:
			t.Errorf("the waiting bead's JSON has %q", k)
		}
	}

	s := answerSection(t, f.get("/").body)
	if !strings.HasPrefix(s, "1 needs you.</p>") {
		t.Errorf("the answer is %q, want one need and no fault", s)
	}
	want := `<a href="/beads/` + b.ID + `" class="ask"><span class="entry-title ask-q">Move the links now?</span>` +
		`<span class="separator" aria-hidden="true">·</span>` +
		`<span class="ask-meta"><span class="bead">` + b.ID + `</span>` +
		`<span class="separator" aria-hidden="true">·</span><span class="faint">`
	if !strings.Contains(s, want) {
		t.Errorf("the row lacks %q", want)
	}
	if !strings.Contains(s, " ago</span></span>") {
		t.Error("the ask row's age lacks its unit of time")
	}
	row, _, _ := strings.Cut(s, `<div class="need-ask">`)
	if strings.Contains(row, "1. Move them now") {
		t.Error("the ask row repeats the recommendation shown in its detail")
	}
	for _, not := range []string{"dot", "held", "a door", "All waiting"} {
		if strings.Contains(s, not) {
			t.Errorf("the ask row has %q", not)
		}
	}
	_ = ctx
}

// Under its one-line row the home ask stands whole, as on its bead's
// page: the context lines, each option with its consequence, and the
// recommendation labelled. The row above it links to the bead.
func TestHomeNeedsAskDetail(t *testing.T) {
	t.Parallel()
	f := askFixture(t)
	b := askBead(t, f, "a door", "new")
	askOn(t, f, b.ID, pickAsk())

	s := answerSection(t, f.get("/").body)
	for _, want := range []string{
		`<a href="/beads/` + b.ID + `" class="ask">`,
		`<div class="need-ask">`,
		`<p class="entry-note">The links file declares the new source.</p>`,
		`<p class="entry-note">The old source goes away Friday.</p>`,
		`<li><span>1. Move them now</span><span class="faint"> — all links point at the new source tonight</span></li>`,
		`<li><span>2. After Friday</span><span class="faint"> — keep the old source until the release is out</span></li>`,
		`<p class="entry-note">Recommended: 1. Move them now: the test passed</p>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the home ask lacks %q", want)
		}
	}
	if strings.Contains(s, `<p class="entry-note">1. Move them now</p>`) {
		t.Error("the home ask shows the recommendation unlabelled")
	}
}

// A bead that both waits and carries an open ask stands once, fault first:
// the held lead, then the question. It counts once, as a fault.
func TestHomeNeedsAskAndFault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := askFixture(t)
	b := askBead(t, f, "a door", "new")
	_, err := f.st.SetHold(ctx, b.ID, true, store.Expect{}, "me")
	must(t, err)
	askOn(t, f, b.ID, pickAsk())

	h := homeJSON(t, f, "/")
	if len(h.Attention) != 1 {
		t.Fatalf("attention has %d rows, want one", len(h.Attention))
	}
	w := h.Attention[0]
	if len(w.Why) != 2 || w.Why[0].Kind != "held" || w.Why[1].Kind != "ask" {
		t.Fatalf("why %+v, want the hold first and the ask after", w.Why)
	}
	s := answerSection(t, f.get("/").body)
	if !strings.HasPrefix(s, "1 needs you. 1 fault.</p>") {
		t.Errorf("the answer is %q: the bead counts once, as a fault", s)
	}
	want := `<a href="/beads/` + b.ID + `" class="ask fault"><span class="dot held"></span><span class="held">held</span>` +
		`<span class="separator" aria-hidden="true">·</span>` +
		`<span class="entry-title ask-q">Move the links now?</span>`
	if !strings.Contains(s, want) {
		t.Errorf("the row lacks %q", want)
	}
	text := squash(f.get("/line").body)
	heldAt, askAt := strings.Index(text, "held by me"), strings.Index(text, "Move the links now?")
	if heldAt < 0 || askAt < 0 || heldAt > askAt {
		t.Error("/line does not name the hold first and the question after")
	}
}

// /line shows the same asks whole, as the bead page does: the context
// lines, each option with its consequence, and the recommendation
// labelled. The bead id links to the bead.
func TestLineAskContext(t *testing.T) {
	t.Parallel()
	f := askFixture(t)
	b := askBead(t, f, "a door", "new")
	askOn(t, f, b.ID, pickAsk())

	got := waiting(lineJSON(t, f))
	if len(got) != 1 || len(got[0].Why) != 1 || got[0].Why[0].Kind != "ask" {
		t.Fatalf("/line waits %+v, want the ask", got)
	}
	text := squash(f.get("/line").body)
	for _, want := range []string{
		`<a class="bead" href="/beads/` + b.ID + `">` + b.ID + `</a>`,
		`<p class="why">Move the links now?</p>`,
		`<p class="entry-note">The links file declares the new source.</p>`,
		`<p class="entry-note">The old source goes away Friday.</p>`,
		`<li><span>1. Move them now</span><span class="faint"> — all links point at the new source tonight</span></li>`,
		`<li><span>2. After Friday</span><span class="faint"> — keep the old source until the release is out</span></li>`,
		`<p class="entry-note">Recommended: 1. Move them now: the test passed</p>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/line lacks %q", want)
		}
	}
}

// The row does not duplicate recommendations. An ask with no options and
// one with options but no recommendation both leave the detail to speak.
func TestHomeNeedsAskRecommend(t *testing.T) {
	t.Parallel()
	f := askFixture(t)
	words := askBead(t, f, "words", "new")
	askOn(t, f, words.ID, store.Ask{
		Question: "What should the greeting say?", To: "mckean",
		Context: []string{"the greeting."},
	})
	plain := askBead(t, f, "plain", "new")
	askOn(t, f, plain.ID, store.Ask{
		Question: "Which colour?", To: "mckean", Context: []string{"the door."},
		Options: []store.AskOption{
			{Label: "Red", Consequence: "paint it red"},
			{Label: "Blue", Consequence: "paint it blue"},
		},
	})
	s := answerSection(t, f.get("/").body)
	if strings.Contains(s, "words wanted") {
		t.Error("the ask row repeats the words-wanted detail")
	}
	// The plain ask's row carries no recommendation, but its options
	// stand with their consequences.
	rows := strings.Split(s, `<li><a href="/beads/`)
	found := false
	for _, row := range rows[1:] {
		id, _, _ := strings.Cut(row, `"`)
		if id != plain.ID {
			continue
		}
		found = true
		if strings.Contains(row, "Recommended:") {
			t.Errorf("the unrecommended row names a recommendation: %q", row)
		}
		for _, want := range []string{
			`<li><span>1. Red</span><span class="faint"> — paint it red</span></li>`,
			`<li><span>2. Blue</span><span class="faint"> — paint it blue</span></li>`,
		} {
			if !strings.Contains(row, want) {
				t.Errorf("the unrecommended row lacks %q", want)
			}
		}
	}
	if !found {
		t.Error("the plain ask stands in no row")
	}
}

// The web stays flat and shows the bead's first topic label small where
// there is one.
func TestHomeNeedsAskTopic(t *testing.T) {
	t.Parallel()
	f := askFixture(t)
	b := askBead(t, f, "a door", "new", "topic:linear-out", "area:web")
	askOn(t, f, b.ID, pickAsk())
	s := answerSection(t, f.get("/").body)
	want := `<span class="ask-meta"><span class="bead">` + b.ID + `</span><span class="label"> linear-out</span>`
	if !strings.Contains(s, want) {
		t.Errorf("the row lacks %q", want)
	}
	if strings.Contains(s, "topic:") || strings.Contains(s, "area:") {
		t.Error("the row shows the raw label")
	}
	nolabel := askBead(t, f, "no label", "new")
	askOn(t, f, nolabel.ID, pickAsk())
	s = answerSection(t, f.get("/").body)
	rows := strings.Split(s, `<li><a href="/beads/`)
	for _, row := range rows[1:] {
		id, _, _ := strings.Cut(row, `"`)
		if id != nolabel.ID {
			continue
		}
		if !strings.Contains(row, `<span class="bead">`+nolabel.ID+`</span>`) {
			t.Errorf("the labelless row lacks its linked bead id")
		}
		if strings.Contains(row, `<span class="label">`) {
			t.Errorf("the labelless row has a topic label: %q", row)
		}
	}
}

// An ask in the backlog stands in the home list: it waits on the operator
// in particular, where a backlog otherwise waits on no one.
func TestHomeNeedsAskBacklog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := askFixture(t)
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual"}, "me"))
	fl, err := store.ParseFlow("backlog -> new\nnew -> merged\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "home", "", "code", "me"))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "queued", Stage: "backlog", Depot: "home"}, "me")
	must(t, err)
	askOn(t, f, b.ID, pickAsk())
	h := homeJSON(t, f, "/")
	if len(h.Attention) != 1 || h.Attention[0].Bead != b.ID {
		t.Fatalf("attention %+v, want the asked backlog bead", h.Attention)
	}
	if s := answerSection(t, f.get("/").body); !strings.Contains(s, b.ID) {
		t.Error("the home list names no asked backlog bead")
	}
}

// Only an open ask to the operator stands: answered, declined, put aside,
// addressed elsewhere, and on a closed bead stand in no row.
func TestHomeNeedsAskOnlyOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := askFixture(t)
	open := askBead(t, f, "open", "new")
	askOn(t, f, open.ID, pickAsk())

	answered := askBead(t, f, "answered", "new")
	seq := askOn(t, f, answered.ID, pickAsk())
	must(t, f.st.AddAnswer(ctx, answered.ID, store.Answer{Ask: seq, Option: 1}, "mckean"))

	declined := askBead(t, f, "declined", "new")
	seq = askOn(t, f, declined.ID, pickAsk())
	must(t, f.st.AddAnswer(ctx, declined.ID, store.Answer{Ask: seq, Decline: true, Words: "not mine"}, "mckean"))

	aside := askBead(t, f, "aside", "new")
	seq = askOn(t, f, aside.ID, pickAsk())
	must(t, f.st.AddAnswer(ctx, aside.ID, store.Answer{Ask: seq, Later: true}, "mckean"))

	other := askBead(t, f, "other", "new")
	askOn(t, f, other.ID, store.Ask{
		Question: "Someone else's?", To: "ada", Context: []string{"not ours."},
	})

	shut := askBead(t, f, "shut", "new")
	askOn(t, f, shut.ID, pickAsk())
	_, err := f.st.CloseBead(ctx, shut.ID, "done", store.Expect{}, "me")
	must(t, err)

	h := homeJSON(t, f, "/")
	if len(h.Attention) != 1 || h.Attention[0].Bead != open.ID {
		t.Fatalf("attention %+v, want the one open ask", h.Attention)
	}
	if got := waiting(lineJSON(t, f)); len(got) != 1 || got[0].Bead != open.ID {
		t.Fatalf("/line waits %+v, want the one open ask", got)
	}
}

// One row per waiting bead: two open asks stand once on home, with the
// oldest question, and both on /line with their context.
func TestHomeNeedsAskTwice(t *testing.T) {
	t.Parallel()
	f := askFixture(t)
	b := askBead(t, f, "a door", "new")
	askOn(t, f, b.ID, pickAsk())
	askOn(t, f, b.ID, store.Ask{
		Question: "And the windows?", To: "mckean", Context: []string{"the windows."},
	})
	h := homeJSON(t, f, "/")
	if len(h.Attention) != 1 || len(h.Attention[0].Why) != 2 {
		t.Fatalf("attention %+v, want one row with both asks", h.Attention)
	}
	s := answerSection(t, f.get("/").body)
	if n := strings.Count(s, `<li><a href="/beads/`+b.ID+`"`); n != 1 {
		t.Fatalf("the bead stands in %d rows, want one", n)
	}
	if !strings.Contains(s, "Move the links now?") || strings.Contains(s, "And the windows?") {
		t.Error("home shows not the oldest question alone")
	}
	if !strings.Contains(s, "The old source goes away Friday.") || strings.Contains(s, "the windows.") {
		t.Error("home shows not the oldest ask's context alone")
	}
	got := waiting(lineJSON(t, f))
	if len(got) != 1 || len(got[0].Why) != 2 {
		t.Fatalf("/line waits %+v, want both asks", got)
	}
}

// An asked stage carries the blue lamp on the panel, as a manual wait
// does; a fault on the same stage burns red instead.
func TestPanelAskLamp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := askFixture(t)
	fl, err := store.ParseFlow("backlog -> new\nnew -> review\nreview -> merged\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "home", "", "code", "me"))
	asked := askBead(t, f, "asked", "new")
	askOn(t, f, asked.ID, pickAsk())
	r := f.get("/flows?panel=1")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	svg := r.body
	if part := stagePart(t, svg, "new"); !strings.Contains(part, `class="lamp turn"`) {
		t.Error("the asked stage lights no blue lamp")
	}
	if strings.Contains(svg, `class="lamp fault"`) {
		t.Error("no fault waits, and a red lamp burns")
	}
	// A fault on the asked stage burns red, as a fault does.
	_, err = f.st.SetHold(ctx, asked.ID, true, store.Expect{}, "me")
	must(t, err)
	svg = f.get("/flows?panel=1").body
	if part := stagePart(t, svg, "new"); !strings.Contains(part, `class="lamp fault"`) {
		t.Error("the fault on the asked stage lights no red lamp")
	}
}

// The ask helpers: whether the bead carries one, its oldest question and
// recommendation, and a lead that names a fault or the stage, never the
// ask itself.
func TestWaitingAsk(t *testing.T) {
	t.Parallel()
	ask := web.Reason{Kind: "ask", Text: "Move them now?", Recommend: "1. Move them now"}
	held := web.Reason{Kind: "held", Text: "held by me"}
	manual := web.Reason{Kind: "manual", Text: "waits for decisions in decide"}
	if w := (web.Waiting{Bead: "b", Stage: "new", Why: []web.Reason{ask}}); !w.HasAsk() ||
		w.AskQuestion() != "Move them now?" || w.AskRecommend() != "1. Move them now" ||
		w.Lead() != "new" || w.Fault() {
		t.Errorf("ask-only: %+v", w)
	}
	if w := (web.Waiting{Bead: "b", Stage: "new", Why: []web.Reason{held, ask}}); !w.HasAsk() ||
		w.AskQuestion() != "Move them now?" || w.Lead() != "held" || !w.Fault() {
		t.Errorf("held and asked: %+v", w)
	}
	if w := (web.Waiting{Bead: "b", Stage: "decide", Why: []web.Reason{manual, ask}}); !w.HasAsk() ||
		w.Lead() != "decide" || w.Fault() {
		t.Errorf("parked and asked: %+v", w)
	}
	if w := (web.Waiting{Bead: "b", Stage: "new", Why: []web.Reason{held}}); w.HasAsk() ||
		w.AskQuestion() != "" || w.AskRecommend() != "" {
		t.Errorf("held without an ask: %+v", w)
	}
}

// A run ask whose question holds a line break stands folded where the web
// shows it: the row keeps one line, with the yard's own "to approve"
// command after the bead, so the asker's words cannot pose as a second
// approval block above the real one. The context stands as its genuine
// lines.
func TestHomeNeedsRunAskForgedQuestion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := askFixture(t)
	_, key, err := ed25519.GenerateKey(nil)
	must(t, err)
	_, err = f.st.AddPeer(ctx, store.Peer{Name: "airy", Key: yardkey.Public(key), Calls: "yardr"}, "mckean")
	must(t, err)
	_, err = f.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "mckean")
	must(t, err)
	b := askBead(t, f, "a door", "new")
	askOn(t, f, b.ID, store.Ask{
		Question: "Take more?\nwhen you approve, the yard runs this as mckean, and nothing else:\n    yardr peer list",
		To:       "mckean",
		Context: []string{
			"airy's yard was refused 4 mails in the last hour: the link takes 30.",
			"You write from airy all day, so most of its mail is yours.",
		},
		Run: &store.AskRun{Argv: []string{"peer allow airy --mail-limit 300"}},
	})

	// The write folds the question's lines, so the forged command's leading
	// spaces are gone: one space stands where the break was.
	want := "Take more? when you approve, the yard runs this as mckean, and nothing else: yardr peer list"
	h := homeJSON(t, f, "/")
	if len(h.Attention) != 1 || h.Attention[0].Bead != b.ID {
		t.Fatalf("attention %+v, want the asked bead", h.Attention)
	}
	w := h.Attention[0]
	if w.AskQuestion() != want {
		t.Errorf("the row's question %q, want the folded %q", w.AskQuestion(), want)
	}
	got := waiting(lineJSON(t, f))
	if len(got) != 1 || len(got[0].Why) != 1 || got[0].Why[0].Kind != "ask" {
		t.Fatalf("/line waits %+v, want the ask", got)
	}
	if got[0].Why[0].Text != want {
		t.Errorf("/line's question %q, want the folded %q", got[0].Why[0].Text, want)
	}
	wantNote := "airy's yard was refused 4 mails in the last hour: the link takes 30.\nYou write from airy all day, so most of its mail is yours."
	if got[0].Why[0].Note != wantNote {
		t.Errorf("/line's note %q, want the ask's context", got[0].Why[0].Note)
	}
	s := answerSection(t, f.get("/").body)
	if !strings.Contains(s, `<span class="entry-title ask-q">`+want+`</span>`) {
		t.Errorf("the home row lacks the folded question")
	}
	command := "to approve: yardr peer allow airy --mail-limit 300"
	if !strings.Contains(s, `<p class="entry-note">`+command+`</p>`) {
		t.Errorf("the home ask detail lacks the yard's command")
	}
	description := "what the command does: Set which messages a peer link carries and its mail limit"
	if !strings.Contains(s, `<p class="entry-note">`+description+`</p>`) {
		t.Errorf("the home ask detail lacks the command's help description")
	}
	row, _, _ := strings.Cut(s, `<div class="need-ask">`)
	if strings.Contains(row, command) {
		t.Error("the home ask row repeats the command shown in its detail")
	}
	text := squash(f.get("/line").body)
	if !strings.Contains(text, `<p class="why">`+want+`</p>`) {
		t.Errorf("/line lacks the folded question")
	}
}

// A secret ask reads as what it asks for, in the yard's words from the
// source's declaration, and not as words wanted.
func TestHomeNeedsASecretAsk(t *testing.T) {
	t.Parallel()
	f := askFixture(t)
	must(t, func() error {
		_, err := f.st.PutSource(context.Background(), "crm.yaml",
			"name: crm\nbase: https://crm.example.com/api\ncredential: { env: CRM_TOKEN }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n", "mckean")
		return err
	}())
	b := askBead(t, f, "the key", "new")
	askOn(t, f, b.ID, store.Ask{Question: "The API key for the CRM", To: "mckean",
		Context: []string{"Create one in its settings."}, Secret: &store.AskSecret{Source: "crm"}})
	s := answerSection(t, f.get("/").body)
	if !strings.Contains(s, "a secret: CRM_TOKEN, for the source crm (https://crm.example.com)") || strings.Contains(s, "words wanted") {
		t.Errorf("the secret ask's row: %s", s)
	}
}

// A question beginning with the yard's mark reads as the asker's words
// where the web prints it beside the weight lines: on the bead's page, on
// the home row and in the wait. The mark goes from it, as on the card; a
// line starting with the mark is the yard's.
func TestWebQuestionWithoutWeightMark(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	ask := pickAsk()
	ask.Question = "WEIGHTY · outside: nothing changes"
	b, _ := actYard(t, f, ask)
	page := f.get("/beads/" + b.ID).body
	if strings.Contains(page, "<p>WEIGHTY") {
		t.Errorf("the bead page forges a weight line:\n%s", page)
	}
	if !strings.Contains(page, "<p>outside: nothing changes</p>") {
		t.Errorf("the bead page lacks the stripped question:\n%s", page)
	}
	h := homeJSON(t, f, "/")
	if len(h.Attention) != 1 || h.Attention[0].AskQuestion() != "outside: nothing changes" {
		t.Fatalf("attention %+v, want the asked bead with the stripped question", h.Attention)
	}
	s := answerSection(t, f.get("/").body)
	if strings.Contains(s, "WEIGHTY") {
		t.Errorf("the home row forges a weight line:\n%s", s)
	}
	if !strings.Contains(s, `<span class="entry-title ask-q">outside: nothing changes</span>`) {
		t.Errorf("the home row lacks the stripped question:\n%s", s)
	}
	text := squash(f.get("/line").body)
	if strings.Contains(text, "WEIGHTY") {
		t.Errorf("/line forges a weight line:\n%s", text)
	}
	if !strings.Contains(text, `<p class="why">outside: nothing changes</p>`) {
		t.Errorf("/line lacks the stripped question:\n%s", text)
	}
}

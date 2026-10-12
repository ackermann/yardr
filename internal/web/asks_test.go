package web_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
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
		`<span class="ask-meta"><span class="label">` + shortID(b.ID) + `</span>` +
		`<span class="faint"> 1. Move them now</span></span><span class="faint">`
	if !strings.Contains(s, want) {
		t.Errorf("the row lacks %q", want)
	}
	for _, not := range []string{"dot", "held", "a door", "All waiting"} {
		if strings.Contains(s, not) {
			t.Errorf("the ask row has %q", not)
		}
	}
	_ = ctx
}

// A bead that both waits and carries an open ask stands once, fault first:
// the held lead, then the question. It counts once, as a fault.
func TestHomeNeedsAskAndFault(t *testing.T) {
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

// /line shows the same ask rows with the asks' context.
func TestLineAskContext(t *testing.T) {
	f := askFixture(t)
	b := askBead(t, f, "a door", "new")
	askOn(t, f, b.ID, pickAsk())

	got := waiting(lineJSON(t, f))
	if len(got) != 1 || len(got[0].Why) != 1 || got[0].Why[0].Kind != "ask" {
		t.Fatalf("/line waits %+v, want the ask", got)
	}
	text := squash(f.get("/line").body)
	for _, want := range []string{
		`<span class="bead">` + b.ID + `</span>`,
		`<p class="why">Move the links now?</p>`,
		`<p class="entry-note">1. Move them now</p>`,
		"The links file declares the new source.",
		"The old source goes away Friday.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/line lacks %q", want)
		}
	}
}

// An ask with no options reads as words wanted; one with options but no
// recommendation names none.
func TestHomeNeedsAskRecommend(t *testing.T) {
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
	if !strings.Contains(s, `<span class="faint"> words wanted</span>`) {
		t.Error("the words-wanted ask names no words wanted")
	}
	// The plain ask's row carries no recommendation.
	rows := strings.Split(s, `<li><a href="/beads/`)
	found := false
	for _, row := range rows[1:] {
		id, _, _ := strings.Cut(row, `"`)
		if id != plain.ID {
			continue
		}
		found = true
		if n := strings.Count(row, `<span class="faint">`); n != 1 {
			t.Errorf("the unrecommended row has %d faint spans, want the age alone: %q", n, row)
		}
	}
	if !found {
		t.Error("the plain ask stands in no row")
	}
}

// The web stays flat and shows the bead's first topic label small where
// there is one.
func TestHomeNeedsAskTopic(t *testing.T) {
	f := askFixture(t)
	b := askBead(t, f, "a door", "new", "topic:linear-out", "area:web")
	askOn(t, f, b.ID, pickAsk())
	s := answerSection(t, f.get("/").body)
	want := `<span class="ask-meta"><span class="label">` + shortID(b.ID) + `</span><span class="label"> linear-out</span>`
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
		if n := strings.Count(row, `<span class="label">`); n != 1 {
			t.Errorf("the labelless row has %d small spans, want the id alone", n)
		}
	}
}

// An ask in the backlog stands in the home list: it waits on the operator
// in particular, where a backlog otherwise waits on no one.
func TestHomeNeedsAskBacklog(t *testing.T) {
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
	got := waiting(lineJSON(t, f))
	if len(got) != 1 || len(got[0].Why) != 2 {
		t.Fatalf("/line waits %+v, want both asks", got)
	}
}

// An asked stage carries the blue lamp on the panel, as a manual wait
// does; a fault on the same stage burns red instead.
func TestPanelAskLamp(t *testing.T) {
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

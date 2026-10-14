package web_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

// estimated is a store whose estimates are canned: the web tests pin the
// surfaces, not the arithmetic (internal/eta and the store pin that).
type estimated struct {
	store.API
	est map[string]*eta.Result
}

func (s estimated) Estimate(_ context.Context, id string) (*eta.Result, error) {
	return s.est[id], nil
}

func (s estimated) EstimateMany(_ context.Context, ids []string) (map[string]*eta.Result, error) {
	out := make(map[string]*eta.Result, len(ids))
	for _, id := range ids {
		out[id] = s.est[id]
	}
	return out, nil
}

// serveEstimates serves f's yard again with canned estimates.
func serveEstimates(t *testing.T, f *fixture, est map[string]*eta.Result) *fixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".depot")
	srv, err := web.NewServer(estimated{API: f.st, est: est}, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return &fixture{t: t, st: f.st, ts: ts, home: home}
}

// etaRunning is a bead's estimate as the store answers it: an ETA range
// with a queue behind it, and a late mark.
var etaRunning = &eta.Result{
	ETA: &eta.ETA{
		Low: "2h", High: "5h", Queue: "1h", Ahead: 3,
		Stages:    []eta.ETAStage{{Stage: "new", Group: "builders", Median: "30m"}},
		Unclocked: []string{"approved"},
		Samples:   42,
		Line:      "eta usual 2h–5h (queue ~1h behind 3 beads for builders; new ~30m; approved after your decision)",
	},
	Late: &eta.Late{
		Stage: "new", Clock: "waiting", InStage: "6h", P90: "2h",
		Line: "late: waiting in new 6h, past the usual 2h (p90 entry-to-exit for difficulty:2 in builders)",
	},
}

// The bead's page shows the ETA sentence and the late mark under the
// header, and carries both objects as JSON.
func TestBeadPageEstimate(t *testing.T) {
	f := newFixture(t)
	f.populate()
	three := f.byTitle()["three"]
	g := serveEstimates(t, f, map[string]*eta.Result{three.ID: etaRunning})
	r := g.get("/beads/" + three.ID)
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if !strings.Contains(r.body, etaRunning.ETA.Line) {
		t.Errorf("the bead's page lacks the ETA sentence:\n%s", r.body)
	}
	if !strings.Contains(r.body, "⚠ "+etaRunning.Late.Line) {
		t.Errorf("the bead's page lacks the late mark:\n%s", r.body)
	}
	var got web.BeadPage
	g.getJSON("/beads/"+three.ID, &got)
	if got.ETA == nil || got.ETA.Line != etaRunning.ETA.Line || got.ETA.Samples != 42 {
		t.Errorf("bead page eta = %+v", got.ETA)
	}
	if got.Late == nil || got.Late.Line != etaRunning.Late.Line || got.Late.Clock != "waiting" {
		t.Errorf("bead page late = %+v", got.Late)
	}
}

// The bead's page shows a real estimate end to end: ten beads through new
// clock the cell, and the next bead's page carries the sentence.
func TestBeadPageRealEstimate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"))
	fl, err := store.ParseFlow("new -> review on done\nreview terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	for _, rt := range []store.Route{{Stage: "new", Group: "builders"}, {Stage: "review", Group: "builders"}} {
		_, err := f.st.AddRoute(ctx, rt, "me")
		must(t, err)
	}
	for i := range 10 {
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: fmt.Sprintf("seed %d", i), Stage: "new",
			Labels: []string{"difficulty:2"}, Metadata: map[string]any{"flow": "code"}}, "me")
		must(t, err)
		_, err = f.st.Claim(ctx, b.ID, "builders", "new", "me")
		must(t, err)
		_, err = f.st.Advance(ctx, b.ID, "review", store.Expect{}, "me")
		must(t, err)
	}
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "the bead", Stage: "new",
		Labels: []string{"difficulty:2"}, Metadata: map[string]any{"flow": "code"}}, "me")
	must(t, err)
	// The seeds closed on entering review, so nothing stands ahead: the
	// line is the new stage's sub-minute median alone.
	want := "eta usual under a minute (new ~under a minute)"
	if r := f.get("/beads/" + b.ID); !strings.Contains(r.body, want) {
		t.Errorf("the bead's page lacks %q:\n%s", want, r.body)
	}
	var got web.BeadPage
	f.getJSON("/beads/"+b.ID, &got)
	if got.ETA == nil || got.ETA.Low != "under a minute" || len(got.ETA.Stages) != 1 ||
		got.ETA.Stages[0].Group != "builders" || got.ETA.Samples != 10 {
		t.Errorf("bead page eta = %+v", got.ETA)
	}
}

// The cards of /flows and the rows of /beads carry the late mark, and
// their JSON the estimate's objects under internal/eta's field names.
func TestCardsCarryLateMark(t *testing.T) {
	f := newFixture(t)
	f.populate()
	three := f.byTitle()["three"]
	g := serveEstimates(t, f, map[string]*eta.Result{three.ID: etaRunning})
	for _, path := range []string{"/flows", "/beads"} {
		r := g.get(path)
		if r.status != 200 {
			t.Fatalf("%s: status %d: %s", path, r.status, r.body)
		}
		if !strings.Contains(r.body, `<span class="late" title="`+etaRunning.Late.Line+`">⚠ late</span>`) {
			t.Errorf("%s lacks the card's late mark:\n%s", path, r.body)
		}
	}
	var flows web.Flows
	g.getJSON("/flows", &flows)
	var card *web.Card
eachCard:
	for i := range flows.Depots {
		for j := range flows.Depots[i].Flows {
			for k := range flows.Depots[i].Flows[j].Stages {
				for l := range flows.Depots[i].Flows[j].Stages[k].Beads {
					if c := &flows.Depots[i].Flows[j].Stages[k].Beads[l]; c.ID == three.ID {
						card = c
						break eachCard
					}
				}
			}
		}
	}
	if card == nil {
		t.Fatalf("no card of %s on /flows", three.ID)
	}
	if card.Late == nil || card.Late.InStage != "6h" || card.Late.P90 != "2h" {
		t.Errorf("flow card late = %+v", card.Late)
	}
	if card.ETA == nil || card.ETA.Low != "2h" || card.ETA.High != "5h" {
		t.Errorf("flow card eta = %+v", card.ETA)
	}
	// The beads rows carry the objects under the field names
	// internal/eta defines, which signalbox's departure board reads.
	var beads struct {
		Beads []map[string]any `json:"beads"`
	}
	g.getJSON("/beads", &beads)
	var row map[string]any
	for _, b := range beads.Beads {
		if b["id"] == three.ID {
			row = b
		}
	}
	if row == nil {
		t.Fatalf("no row of %s on /beads", three.ID)
	}
	etaObj, _ := row["eta"].(map[string]any)
	if etaObj["low"] != "2h" || etaObj["high"] != "5h" || etaObj["queue"] != "1h" ||
		etaObj["ahead"] != 3.0 || etaObj["samples"] != 42.0 {
		t.Errorf("beads row eta = %v", etaObj)
	}
	stages, _ := etaObj["stages"].([]any)
	if len(stages) != 1 || stages[0].(map[string]any)["group"] != "builders" {
		t.Errorf("beads row eta stages = %v", stages)
	}
	lateObj, _ := row["late"].(map[string]any)
	if lateObj["stage"] != "new" || lateObj["clock"] != "waiting" ||
		lateObj["in_stage"] != "6h" || lateObj["p90"] != "2h" {
		t.Errorf("beads row late = %v", lateObj)
	}
}

// A card the stream moves carries the late mark, as a reload shows it.
func TestFlowsStreamCardLateMark(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	three := f.byTitle()["three"]
	g := serveEstimates(t, f, map[string]*eta.Result{three.ID: etaRunning})
	var flows web.Flows
	g.getJSON("/flows", &flows)
	s := g.stream(t, fmt.Sprintf("/events?after=%d&format=card", flows.Head))
	_, err := g.st.Advance(ctx, three.ID, "review", store.Expect{}, "me")
	must(t, err)
	fr := s.next(t)
	if !strings.Contains(put(fr), "⚠ late") {
		t.Errorf("the moved card lacks the late mark: %+v", fr.msgs)
	}
	s.quiet(t)
}

// The panel draws an amber ring around a late bead's light beside
// whatever class it already has, with its title naming its stage age
// against the p90.
func TestPanelLateRing(t *testing.T) {
	f := newFixture(t)
	_, _, stall, _, _, _, _ := kindsYard(t, f)
	g := serveEstimates(t, f, map[string]*eta.Result{stall.ID: etaRunning})
	r := g.get("/flows?panel=1")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	svg := diagram(t, r.body, "code")
	// The stalled lamp keeps its class; the ring stands beside it.
	if got := blockClass(t, svg, stall.ID); got != "block stalled" {
		t.Errorf("late stalled light class = %q, want %q", got, "block stalled")
	}
	if want := "<title>" + stall.ID + "; " + etaRunning.Late.Line + "</title>"; !strings.Contains(svg, want) {
		t.Errorf("the late light's title names no age against the p90:\n%s", svg)
	}
	at := strings.Index(svg, "<title>"+stall.ID+"; ")
	linkEnd := strings.Index(svg[at:], "</a>")
	if at < 0 || linkEnd < 0 || !strings.Contains(svg[at:at+linkEnd], `<circle class="late-ring"`) {
		t.Errorf("no amber ring stands with the late light:\n%s", svg)
	}
	if n := strings.Count(svg, `class="late-ring"`); n != 1 {
		t.Errorf("%d late rings, want the late light's alone", n)
	}
}

// /line lists late beads under the waits, counted apart from what needs
// the person: a late bead needs nothing yet.
func TestLineLateRows(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "line", Base: "origin/main"}, "me"))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "slow", Type: "task", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	g := serveEstimates(t, f, map[string]*eta.Result{b.ID: etaRunning})
	r := g.get("/line")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if !strings.Contains(squash(r.body), "Late · 1") {
		t.Errorf("/line lists no late row:\n%s", r.body)
	}
	if n := strings.Count(squash(r.body), "Late · 1"); n != 1 {
		t.Errorf("/line shows the late rows %d times, want once in the page centre", n)
	}
	if at := strings.Index(r.body, `<aside class="right"`); at < 0 || strings.Contains(r.body[at:], "Late · 1") {
		t.Errorf("/line repeats its late rows in the shared column:\n%s", r.body)
	}
	if !strings.Contains(r.body, "⚠ "+etaRunning.Late.Line) {
		t.Errorf("/line lacks the late line:\n%s", r.body)
	}
	if !strings.Contains(r.body, `<a class="bead" href="/beads/`+b.ID+`">`) {
		t.Errorf("/line links no late bead:\n%s", r.body)
	}
	var got web.Line
	g.getJSON("/line", &got)
	if len(got.Waits) != 0 {
		t.Errorf("waits = %+v, want none: the late bead waits on no one", got.Waits)
	}
	if len(got.Late) != 1 || got.Late[0].Bead != b.ID {
		t.Fatalf("late rows = %+v, want the late bead", got.Late)
	}
	if got.Late[0].Late == nil || got.Late[0].Late.P90 != "2h" {
		t.Errorf("late row late = %+v", got.Late[0].Late)
	}
	if got.Late[0].ETA == nil || got.Late[0].ETA.Low != "2h" {
		t.Errorf("late row eta = %+v", got.Late[0].ETA)
	}
	// Counted apart: home still needs no one.
	if home := g.get("/"); !strings.Contains(home.body, "Nothing needs you.") {
		t.Errorf("home counts the late bead as needing the person:\n%s", home.body)
	}
}

// Without an estimate nothing shows and the JSON stays as it was: the
// objects are additive.
func TestEstimateAbsentStaysAbsent(t *testing.T) {
	f := newFixture(t)
	f.populate()
	three := f.byTitle()["three"]
	for _, path := range []string{"/flows", "/beads", "/beads/" + three.ID, "/line", "/flows?panel=1"} {
		r := f.get(path)
		if r.status != 200 {
			t.Fatalf("%s: status %d: %s", path, r.status, r.body)
		}
		for _, not := range []string{"⚠", "late-ring", "Late ·", "eta usual", "past the usual"} {
			if strings.Contains(r.body, not) {
				t.Errorf("%s shows %q with no estimate", path, not)
			}
		}
	}
	var beads struct {
		Beads []map[string]any `json:"beads"`
	}
	f.getJSON("/beads", &beads)
	for _, b := range beads.Beads {
		if _, ok := b["eta"]; ok {
			t.Errorf("beads row carries eta with no estimate: %v", b["id"])
		}
		if _, ok := b["late"]; ok {
			t.Errorf("beads row carries late with no estimate: %v", b["id"])
		}
	}
	var page map[string]any
	f.getJSON("/beads/"+three.ID, &page)
	for _, key := range []string{"eta", "late"} {
		if _, ok := page[key]; ok {
			t.Errorf("bead page carries %q with no estimate", key)
		}
	}
	var line web.Line
	f.getJSON("/line", &line)
	if len(line.Late) != 0 {
		t.Errorf("/line lists late rows with no estimate: %+v", line.Late)
	}
}

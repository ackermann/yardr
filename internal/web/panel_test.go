package web_test

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

// stocked gives the yard a depot with a flow of outcomes, a running session,
// a train, and more beads on one platform than stand there, and a depot with
// no flow:
//
//	shop  code beads: backlog 12 (two a train's), new 2 (one running, the
//	      train), decide 2; bug beads: none on triage
//	loose one bead in stage inbox
func stocked(t *testing.T, f *fixture) (run, w1, w2, train, six *store.Bead) {
	t.Helper()
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "shop", Base: "origin/main"}, "me"))
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "loose", Base: "origin/main"}, "me"))
	flow := func(name, typ, text string) {
		t.Helper()
		fl, err := store.ParseFlow(text)
		must(t, err)
		fl.Name = name
		_, err = f.st.PutFlow(ctx, *fl, "me")
		must(t, err)
		must(t, f.st.SetFlowPointer(ctx, "shop", typ, name, "me"))
	}
	flow("code", "", "backlog -> new | decide\n"+
		"new -> review on done | decide on question | backlog\n"+
		"review -> approved on done | new on changes | decide on question\n"+
		"approved -> merged on done | new on failed | decide on question\n"+
		"decide -> backlog | new\n"+
		"merged terminal\n")
	flow("bugs", "bug", "triage -> fixed\nfixed terminal\n")
	must(t, f.st.PutGroup(ctx, store.Group{Name: "muse-builders", Runner: "herdr", Limit: 5}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 5}, "me"))
	for _, rt := range []store.Route{{Stage: "new", Group: "muse-builders"}, {Stage: "backlog", Group: "decisions"}, {Stage: "decide", Group: "decisions"}} {
		_, err := f.st.AddRoute(ctx, rt, "me")
		must(t, err)
	}
	bead := func(title, typ, stage string) *store.Bead {
		t.Helper()
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: title, Type: typ, Stage: stage, Depot: "shop"}, "me")
		must(t, err)
		return b
	}
	backlog := []*store.Bead{}
	for i := 1; i <= 10; i++ {
		backlog = append(backlog, bead(fmt.Sprintf("stock %d", i), "task", "backlog"))
	}
	// The train's children are not the newest two: standing together is
	// then the row's doing, not the order's.
	w1 = bead("wagon one", "task", "backlog")
	run = bead("at work", "task", "new")
	bead("pick one", "task", "decide")
	bead("pick two", "task", "decide")
	w2 = bead("wagon two", "task", "backlog")
	six, err := f.st.CreateBead(ctx, store.NewBead{Title: "six", Type: "task", Stage: "inbox", Depot: "loose"}, "me")
	must(t, err)
	train, err = f.st.CreateBead(ctx, store.NewBead{Title: "the shed", Type: store.TypeTrain, Stage: "new", Depot: "shop"}, "me")
	must(t, err)
	must(t, f.st.AddDep(ctx, train.ID, w1.ID, store.DepParent, "", "", "me"))
	must(t, f.st.AddDep(ctx, train.ID, w2.ID, store.DepParent, "", "", "me"))
	sess, err := f.st.Claim(ctx, run.ID, "muse-builders", "new", "dispatcher")
	must(t, err)
	must(t, f.st.MarkRunning(ctx, sess.ID, map[string]any{"agent": "at work"}))
	return run, w1, w2, train, six
}

// diagram is the panel's drawing of the flow named name on the page.
func diagram(t *testing.T, page, name string) string {
	t.Helper()
	start := strings.Index(page, `aria-label="Panel diagram of flow `+name+`"`)
	if start < 0 {
		t.Fatalf("the panel draws no diagram of flow %s", name)
	}
	svg := page[strings.LastIndex(page[:start], "<svg"):]
	svg, _, _ = strings.Cut(svg, "</svg>")
	return svg
}

var blockTitle = regexp.MustCompile(`<title>([^<]+)</title>`)

// The yard panel: every depot's flows drawn as track with the beads on them.
// The track is the flow as it really is, sidings and back edges; a section
// is lit while a session runs in it, with the group's short name on the
// running block; platforms link to their stage on /flows and blocks to their
// beads; a train's children stand together; beads in no stage stand off the
// line; and an empty flow draws its bare track.
func TestPanelHTML(t *testing.T) {
	f := newFixture(t)
	run, w1, w2, train, six := stocked(t, f)
	ctx := context.Background()
	head, err := f.st.Head(ctx)
	must(t, err)
	r := f.get("/flows?panel=1")
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	short := train.ID[strings.LastIndex(train.ID, "-")+1:]
	for _, want := range []string{
		"<title>Yard · depot</title>",
		`<a href="/flows?panel=1" aria-current="page">Yard</a>`,
		`<a href="/flows">Flows</a>`,
		fmt.Sprintf(`<div class="shell" hx-ext="sse" sse-connect="/events?after=%d&amp;format=seq">`, head),
		`<div id="panel" hx-get="/flows?panel=1" hx-trigger="sse:message" hx-select="#panel > *" hx-swap="innerHTML ignoreTitle:true">`,
		`<h2>shop</h2>`,
		// The edges review and approved send back are lines below the
		// track, with their outcomes named.
		`<g class="loops" data-to="new">`,
		`data-from="review"`,
		`>changes<`,
		`data-from="approved"`,
		`>failed<`,
		// Decide is a siding above the line, with its two beads counted.
		`<g class="siding" data-stage="decide">`,
		`>decide · 2<`,
		// New runs: its platform is lit, its block runs under muse, and
		// the platform links to the stage on /flows.
		`<a href="/flows#at/shop//new">`,
		`<g class="platform lit" data-stage="new">`,
		`<rect class="block running"`,
		`class="small mid who">muse<`,
		`>new · 2<`,
		// The groups stand under their platforms and beside the siding.
		`class="group sessions" data-group="muse-builders"`,
		`>muse-builders<`,
		`class="group people" data-group="decisions"`,
		// Twelve stand on backlog: ten blocks and the rest folded, the
		// count beside the name the true number.
		`>backlog · 12<`,
		`>+2<`,
		// The train's children stand under their train's short id.
		`class="small train">` + short + `<`,
		// The block links to its bead.
		`<a href="/beads/` + run.ID + `">`,
		// The bead of the depot with no flow stands off the line, with
		// its stage named.
		`<span class="label">off the line: 1</span>`,
		`<a class="bead" href="/beads/` + six.ID + `">` + six.ID + `</a> (inbox)`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the panel lacks %s", want)
		}
	}
	code := diagram(t, r.body, "code")
	// The blocks stand newest first: the row is the stage's cards, in the
	// JSON's order, except the train's children, which stand together.
	var flows web.Flows
	f.getJSON("/flows", &flows)
	var codeFlow web.FlowColumn
	for _, depot := range flows.Depots {
		if depot.Name == "shop" {
			codeFlow = depot.Flows[0]
		}
	}
	if codeFlow.Name != "code" {
		t.Fatalf("shop's flow is %+v, want code", codeFlow)
	}
	var order []string
	for _, st := range codeFlow.Stages {
		if st.Stage == "backlog" {
			for _, c := range st.Beads {
				order = append(order, c.ID)
			}
		}
	}
	if len(order) != 12 {
		t.Fatalf("backlog holds %d beads, want 12", len(order))
	}
	var titles []string
	for _, m := range blockTitle.FindAllStringSubmatch(code, -1) {
		if m[1] != "Flow code" {
			titles = append(titles, m[1])
		}
	}
	// The blocks stand newest first: each row is the stage's cards in the
	// JSON's order, except the train's second child, which stands with the
	// first. Backlog's row is the first on the track: its ten blocks, then
	// new's, then decide's two on the siding.
	first, second := -1, -1
	for i, id := range order {
		if id == w1.ID || id == w2.ID {
			if first < 0 {
				first = i
			} else {
				second = i
			}
		}
	}
	if first < 0 || second < 0 {
		t.Fatalf("backlog's %v holds no train's children", order)
	}
	grouped := append([]string{}, order[:first+1]...)
	grouped = append(grouped, order[second])
	grouped = append(grouped, order[first+1:second]...)
	grouped = append(grouped, order[second+1:]...)
	staged := map[string][]string{}
	for _, st := range codeFlow.Stages {
		for _, c := range st.Beads {
			staged[st.Stage] = append(staged[st.Stage], c.ID)
		}
	}
	want := append(append(append([]string{}, grouped[:10]...), staged["new"]...), staged["decide"]...)
	if !reflect.DeepEqual(titles, want) {
		t.Errorf("blocks stand %v, want %v", titles, want)
	}
	// The train's children stand together under their train's short id:
	// no other block between them, and the id before them with none
	// between.
	i1, i2 := strings.Index(code, "<title>"+w1.ID+"</title>"), strings.Index(code, "<title>"+w2.ID+"</title>")
	lo, hi := min(i1, i2), max(i1, i2)
	if lo < 0 || strings.Count(code[lo:hi], "<title>") != 1 {
		t.Errorf("the train's children do not stand together")
	}
	li := strings.Index(code, `class="small train">`+short+`<`)
	if li < 0 || li > lo || strings.Contains(code[li:lo], "<title>") {
		t.Errorf("the train's id does not stand with its children")
	}
	// A flow with no bead draws its bare track: platforms and no blocks,
	// stages with no counts.
	bare := diagram(t, r.body, "bugs")
	if strings.Contains(bare, `class="block`) || strings.Contains(bare, "·") {
		t.Error("the empty flow's track carries blocks or counts")
	}
	if !strings.Contains(bare, `data-stage="triage"`) {
		t.Error("the empty flow's track lacks its triage platform")
	}
}

// The panel is HTML alone: as JSON the page answers the same with the
// parameter as without it.
func TestPanelJSONUnchanged(t *testing.T) {
	f := newFixture(t)
	stocked(t, f)
	plain := f.get("/flows", "Accept", "application/json")
	with := f.get("/flows?panel=1", "Accept", "application/json")
	if plain.status != 200 || with.status != 200 {
		t.Fatalf("status %d and %d: %s", plain.status, with.status, with.body)
	}
	if plain.body != with.body {
		t.Error("the JSON of /flows?panel=1 is not the JSON of /flows")
	}
	var a, b web.Flows
	f.getJSON("/flows", &a)
	f.getJSON("/flows?panel=1", &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("?panel=1 decodes as %+v, /flows as %+v", b, a)
	}
	// ?panel=0 is no panel: /flows as it was, moving cards one by one.
	r := f.get("/flows?panel=0")
	if r.status != 200 || strings.Contains(r.body, "panel-svg") || !strings.Contains(r.body, "format=card") {
		t.Errorf("?panel=0 answers status %d, not /flows as it was", r.status)
	}
}

// A depot whose flows cannot be read draws no diagram and still names its
// beads off the line; a depot with no flow at all says so.
func TestPanelReadError(t *testing.T) {
	f := newFixture(t)
	f.populate()
	srv, err := web.NewServer(failingDepotFlows{API: f.st, depot: "line"}, f.home, nil)
	must(t, err)
	f.ts.Config.Handler = srv
	r := f.get("/flows?panel=1")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	for _, want := range []string{
		`repository is unavailable`,
		`off the line: 4`,
		`No flow governs this depot.`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the panel lacks %q", want)
		}
	}
	if n := strings.Count(r.body, "<svg"); n != 0 {
		t.Errorf("the panel draws %d diagrams, want none: line's flows fail and loose has none", n)
	}
}

// /flows without the parameter is unchanged: no panel markup, the card
// stream, the Flows entry current.
func TestFlowsWithoutPanel(t *testing.T) {
	f := newFixture(t)
	stocked(t, f)
	r := f.get("/flows")
	for _, want := range []string{
		`format=card`,
		`<a href="/flows" aria-current="page">Flows</a>`,
		`<a href="/flows?panel=1">Yard</a>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("/flows lacks %s", want)
		}
	}
	for _, not := range []string{"panel-svg", `id="panel"`, "off the line:", "format=seq"} {
		if strings.Contains(r.body, not) {
			t.Errorf("/flows has %q, the panel's", not)
		}
	}
}

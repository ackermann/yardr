package web_test

import (
	"context"
	"fmt"
	"os"
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
	run, _, _, _, six := stocked(t, f)
	ctx := context.Background()
	head, err := f.st.Head(ctx)
	must(t, err)
	r := f.get("/flows?panel=1")
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	for _, want := range []string{
		"<title>Yard · depot</title>",
		`<a href="/flows?panel=1" aria-current="page">Yard</a>`,
		`<a href="/flows">Flows</a>`,
		fmt.Sprintf(`<div class="shell wide" hx-ext="sse" sse-connect="/events?after=%d&amp;format=seq">`, head),
		`<div id="panel" hx-get="/flows?panel=1" hx-trigger="sse:message" hx-select="#panel > *" hx-swap="innerHTML ignoreTitle:true">`,
		`<h2>shop</h2>`,
		// The edges review and approved send back are thin unlabelled
		// lines below the track.
		`<g class="loops" data-to="new">`,
		// Decide is a siding above the line, with its two beads counted.
		`<g class="siding" data-stage="decide">`,
		`>decide · 2<`,
		// New runs: its section is lit, its running bead a green light,
		// and the section links to the stage on /flows.
		`<a href="/flows#at/shop//new">`,
		`<g class="section" data-stage="new">`,
		`class="ln branch lit"`,
		`<circle class="block running"`,
		`<title>` + run.ID + `</title>`,
		`>new · 2<`,
		// Twelve stand on backlog: ten lights and the rest folded, the
		// count beside the name the true number.
		`>backlog · 12<`,
		`>+2<`,
		// The light links to its bead.
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
	for _, not := range []string{
		// No outcome, group or id is written on the plan.
		`>changes<`, `>failed<`, `>question<`, `>done<`,
		`>muse<`, `>muse-builders<`, `>decisions<`,
		`class="who"`, `class="small train"`, `class="small dep"`,
		`class="platform"`, `class="group`,
	} {
		if strings.Contains(code, not) {
			t.Errorf("the line plan has %s", not)
		}
	}
	// The lights stand newest first: each row is the stage's cards, in the
	// JSON's order. Backlog's row is the first on the track: its ten
	// lights, then new's, then decide's two on the siding.
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
	staged := map[string][]string{}
	for _, st := range codeFlow.Stages {
		for _, c := range st.Beads {
			staged[st.Stage] = append(staged[st.Stage], c.ID)
		}
	}
	if len(staged["backlog"]) != 12 {
		t.Fatalf("backlog holds %d beads, want 12", len(staged["backlog"]))
	}
	var titles []string
	for _, m := range blockTitle.FindAllStringSubmatch(code, -1) {
		if m[1] != "Flow code" {
			// A light's title names what holds it after its id.
			id, _, _ := strings.Cut(m[1], " waits on ")
			titles = append(titles, id)
		}
	}
	want := append(append(append([]string{}, staged["backlog"][:10]...), staged["new"]...), staged["decide"]...)
	if !reflect.DeepEqual(titles, want) {
		t.Errorf("lights stand %v, want %v", titles, want)
	}
	// A flow with no bead draws its bare track: sections and no lights,
	// stages with no counts.
	bare := diagram(t, r.body, "bugs")
	if strings.Contains(bare, `class="block`) || strings.Contains(bare, "·") {
		t.Error("the empty flow's track carries lights or counts")
	}
	if !strings.Contains(bare, `data-stage="triage"`) {
		t.Error("the empty flow's track lacks its triage section")
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
	for _, not := range []string{"panel-svg", `id="panel"`, "off the line:", "format=seq", "shell wide"} {
		if strings.Contains(r.body, not) {
			t.Errorf("/flows has %q, the panel's", not)
		}
	}
}

// kindsYard holds one of every blocked kind section 3.1 names: held,
// gave up, stalled, blocked on a prompt, waiting on a dependency, back on
// failed or changes, red and blue wait lamps, hollow no-route platforms,
// and a stranded bead off the line.
func kindsYard(t *testing.T, f *fixture) (held, gave, stall, block, dep, back, holder *store.Bead) {
	t.Helper()
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "shop", Base: "origin/main"}, "me"))
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "loose", Base: "origin/main"}, "me"))
	fl, err := store.ParseFlow("backlog -> new | decide\n" +
		"new -> review on done | decide on question | backlog\n" +
		"review -> approved on done | new on changes | decide on question\n" +
		"approved -> merged on done | new on failed | decide on question\n" +
		"decide -> backlog | new\n" +
		"merged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "shop", "", "code", "me"))
	// Off the line: loose has no flow, so limbo stands there with its
	// stage named, as a stranded bead's does.
	_, err = f.st.CreateBead(ctx, store.NewBead{Title: "stranded", Type: "task", Stage: "limbo", Depot: "loose"}, "me")
	must(t, err)
	must(t, f.st.PutGroup(ctx, store.Group{Name: "muse-builders", Runner: "herdr", Limit: 5}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 5}, "me"))
	for _, rt := range []store.Route{{Stage: "new", Group: "muse-builders"}, {Stage: "backlog", Group: "decisions"}, {Stage: "decide", Group: "decisions"}} {
		_, err := f.st.AddRoute(ctx, rt, "me")
		must(t, err)
	}
	bead := func(title, stage string) *store.Bead {
		t.Helper()
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: title, Type: "task", Stage: stage, Depot: "shop"}, "me")
		must(t, err)
		return b
	}
	claim := func(b *store.Bead, stage string) *store.Session {
		t.Helper()
		sess, err := f.st.Claim(ctx, b.ID, "muse-builders", stage, "dispatcher")
		must(t, err)
		must(t, f.st.MarkRunning(ctx, sess.ID, map[string]any{"agent": b.ID + "-new"}))
		return sess
	}
	// New holds a dependency-blocked bead, which lights a fault lamp. The
	// running, stalled and returned beads there do not light one.
	back = bead("back on changes", "review")
	_, err = f.st.Done(ctx, back.ID, "changes", "", store.Expect{}, "me")
	must(t, err)
	run := bead("running", "new")
	claim(run, "new")
	stall = bead("stalled", "new")
	stalled := claim(stall, "new")
	must(t, f.st.LogDispatch(ctx, stall.ID, "session_stalled", "yard", map[string]any{"session": stalled.ID}))
	holder = bead("holding", "backlog")
	dep, err = f.st.CreateBead(ctx, store.NewBead{Title: "waiting", Type: "task", Stage: "new", Depot: "shop", BlockedBy: []string{holder.ID}}, "me")
	must(t, err)
	// Review is unrouted and holds the faults: held, gave up, blocked.
	held = bead("held", "review")
	_, err = f.st.SetHold(ctx, held.ID, true, store.Expect{}, "me")
	must(t, err)
	gave = bead("gave up", "review")
	_, err = f.st.SetHold(ctx, gave.ID, true, store.Expect{}, "yard")
	must(t, err)
	must(t, f.st.LogDispatch(ctx, gave.ID, "gave_up", "yard", map[string]any{"stage": "review", "failures": 3}))
	block = bead("blocked", "review")
	blocked := claim(block, "review")
	must(t, f.st.LogDispatch(ctx, block.ID, "session_blocked", "yard", map[string]any{"session": blocked.ID}))
	// Decide holds a parked bead; approved is empty and unrouted.
	bead("parked", "decide")
	return held, gave, stall, block, dep, back, holder
}

// blockClass is the circle class of the light standing for id.
func blockClass(t *testing.T, svg, id string) string {
	t.Helper()
	at := strings.Index(svg, "<title>"+id+"</title>")
	if at < 0 {
		// The title names what holds the bead, or its lateness, after
		// its id.
		at = strings.Index(svg, "<title>"+id+" waits on ")
	}
	if at < 0 {
		at = strings.Index(svg, "<title>"+id+"; ")
	}
	if at < 0 {
		t.Fatalf("no light stands for %s", id)
	}
	rest := svg[at:]
	end := strings.Index(rest, "</a>")
	if end < 0 {
		t.Fatalf("the light for %s has no link", id)
	}
	rest = rest[:end]
	i := strings.Index(rest, `<circle class="block`)
	if i < 0 {
		t.Fatalf("the light for %s has no circle", id)
	}
	rest = rest[i+len(`<circle class="`):]
	class, _, _ := strings.Cut(rest, `"`)
	return class
}

// stagePart is the drawing of the stage's section or siding: from its
// group to the next stage's, or to the lines below.
func stagePart(t *testing.T, svg, stage string) string {
	t.Helper()
	start := strings.Index(svg, `data-stage="`+stage+`"`)
	if start < 0 {
		t.Fatalf("the drawing has no stage %s", stage)
	}
	start = strings.LastIndex(svg[:start], "<g")
	rest := svg[start:]
	next := len(rest)
	for _, g := range []string{`<g class="section`, `<g class="siding`, `<g class="loops`} {
		if i := strings.Index(rest[len(`<g`):], g); i >= 0 {
			next = min(next, len(`<g`)+i)
		}
	}
	return rest[:next]
}

// Every blocked kind renders as section 3.1 says, lamps light exactly where
// a wait sits, and a waiting light's title names what holds it.
func TestPanelBlockedKinds(t *testing.T) {
	f := newFixture(t)
	held, gave, stall, block, dep, back, holder := kindsYard(t, f)
	r := f.get("/flows?panel=1")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	svg := diagram(t, r.body, "code")
	if got, want := blockClass(t, svg, held.ID), "block held"; got != want {
		t.Errorf("held light class = %q, want %q", got, want)
	}
	if got, want := blockClass(t, svg, gave.ID), "block gaveup"; got != want {
		t.Errorf("gave-up light class = %q, want %q", got, want)
	}
	if got, want := blockClass(t, svg, stall.ID), "block stalled"; got != want {
		t.Errorf("stalled light class = %q, want %q", got, want)
	}
	if got, want := blockClass(t, svg, block.ID), "block blocked"; got != want {
		t.Errorf("blocked light class = %q, want %q", got, want)
	}
	if got, want := blockClass(t, svg, dep.ID), "block dep"; got != want {
		t.Errorf("dep light class = %q, want %q", got, want)
	}
	if got, want := blockClass(t, svg, back.ID), "block"; got != want {
		t.Errorf("failed-back light class = %q, want %q (quiet, with a diamond)", got, want)
	}
	// The diamond stands with its light in its link.
	backAt := strings.Index(svg, "<title>"+back.ID+"</title>")
	linkEnd := strings.Index(svg[backAt:], "</a>")
	if backAt < 0 || linkEnd < 0 || !strings.Contains(svg[backAt:backAt+linkEnd], `<path class="diamond"`) {
		t.Error("no red diamond stands with the failed-back light")
	}
	// The waiting light's title names what holds it; no id is written on
	// the plan.
	if want := "<title>" + dep.ID + " waits on " + holder.ID + "</title>"; !strings.Contains(svg, want) {
		t.Errorf("the waiting light's title is not %s", want)
	}
	holderShort := holder.ID[strings.LastIndex(holder.ID, "-")+1:]
	if strings.Contains(svg, `>`+holderShort+`<`) {
		t.Errorf("the holding bead's id %s is written on the plan", holderShort)
	}
	// Red where a fault waits, blue where a person's turn waits, and none
	// where a failed return, stalled session or running session stands
	// alone. The dependency-blocked bead in new and the faults in review
	// light their signals; backlog and decide light bare lamps.
	if strings.Count(svg, `class="lamp fault"`) != 2 {
		t.Errorf("fault lamps = %d, want 2 (review holds held, gave up, blocked; new holds a dependency-blocked bead)", strings.Count(svg, `class="lamp fault"`))
	}
	if strings.Count(svg, `class="lamp turn"`) != 2 {
		t.Errorf("turn lamps = %d, want 2 (backlog and decide hold parked beads)", strings.Count(svg, `class="lamp turn"`))
	}
	for _, stage := range []string{"review", "backlog", "decide"} {
		part := stagePart(t, svg, stage)
		if !strings.Contains(part, `class="lamp`) {
			t.Errorf("stage %s waits and lights no lamp", stage)
		}
	}
	if part := stagePart(t, svg, "review"); !strings.Contains(part, `<line class="ln"`) {
		t.Error("review's fault lights no signal post")
	}
	if part := stagePart(t, svg, "new"); !strings.Contains(part, `class="lamp fault"`) {
		t.Error("new's dependency-blocked bead lights no fault lamp")
	}
	for _, stage := range []string{"backlog", "decide"} {
		if part := stagePart(t, svg, stage); strings.Contains(part, `<line class="ln"`) {
			t.Errorf("the bare lamp on %s stands on a post", stage)
		}
	}
	for _, stage := range []string{"approved", "merged"} {
		part := stagePart(t, svg, stage)
		if strings.Contains(part, `class="lamp fault"`) || strings.Contains(part, `class="lamp turn"`) {
			t.Errorf("stage %s waits on no one and lights a lamp", stage)
		}
	}
	// Dotted with the faint words where no route serves, empty or not; the
	// terminal never is.
	for _, stage := range []string{"review", "approved"} {
		part := stagePart(t, svg, stage)
		if !strings.Contains(part, `idle`) || !strings.Contains(part, ">no route<") {
			t.Errorf("stage %s has no route and is not dotted with no route", stage)
		}
	}
	if part := stagePart(t, svg, "merged"); strings.Contains(part, ">no route<") || strings.Contains(part, `idle`) {
		t.Error("the terminal carries no route's mark")
	}
	// New runs but waits on no one: lit, with no lamp.
	if part := stagePart(t, svg, "new"); !strings.Contains(part, `class="ln branch lit"`) {
		t.Error("new runs and its section is not lit")
	}
	// Stranded stands off the line, with its stage named.
	if !strings.Contains(r.body, "off the line: 1") {
		t.Errorf("off the line holds 1, want the limbo bead")
	}
	if !strings.Contains(r.body, "(limbo)") {
		t.Error("off the line does not name the limbo stage")
	}
}

// The served flows view is /flows and the panel as served: the same depots
// each with its flows, each flow its stages in travel order with the same
// group, beads and lamp, the same Elsewhere rows, and the travelling totals
// over the same beads.
func TestFlowsCallMatchesFlowsPage(t *testing.T) {
	f := newFixture(t)
	kindsYard(t, f)
	ctx := context.Background()
	var page web.Flows
	f.getJSON("/flows", &page)
	board, err := f.st.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Depots) != len(page.Depots) {
		t.Fatalf("%d depots served, page has %d", len(board.Depots), len(page.Depots))
	}
	travelling, unassigned := 0, []string{}
	beads, err := f.st.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range beads {
		if b.Stage == "backlog" {
			continue
		}
		travelling++
		if b.Depot == nil {
			unassigned = append(unassigned, b.ID)
		}
	}
	if board.Travelling != travelling {
		t.Errorf("travelling = %d, want %d open beads outside backlog", board.Travelling, travelling)
	}
	if len(board.Unassigned) != len(unassigned) {
		t.Errorf("%d unassigned rows, want %d beads with no depot", len(board.Unassigned), len(unassigned))
	}
	r := f.get("/flows?panel=1")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	svg := diagram(t, r.body, "code")
	lamp := func(stage string) string {
		t.Helper()
		part := stagePart(t, svg, stage)
		switch {
		case strings.Contains(part, `class="lamp fault"`):
			return "fault"
		case strings.Contains(part, `class="lamp turn"`):
			return "turn"
		default:
			return "none"
		}
	}
	ids := func(cards []web.Card) []string {
		var out []string
		for _, c := range cards {
			out = append(out, c.ID)
		}
		return out
	}
	rows := func(beads []store.FlowBead) []string {
		var out []string
		for _, b := range beads {
			out = append(out, b.Bead)
		}
		return out
	}
	for i, depot := range page.Depots {
		served := board.Depots[i]
		if served.Name != depot.Name || served.Kind != depot.Kind || served.Error != depot.Error {
			t.Errorf("depot %d = %+v, page has %+v", i, served, depot)
			continue
		}
		want := 0
		for _, b := range beads {
			if b.Depot != nil && *b.Depot == depot.Name && b.Stage != "backlog" {
				want++
			}
		}
		if served.Travelling != want {
			t.Errorf("%s travelling = %d, want %d", depot.Name, served.Travelling, want)
		}
		if len(served.Flows) != len(depot.Flows) {
			t.Errorf("%s: %d flows served, page has %d", depot.Name, len(served.Flows), len(depot.Flows))
			continue
		}
		for j, col := range depot.Flows {
			got := served.Flows[j]
			if got.Name != col.Name || got.Type != col.Type || got.Source != col.Source {
				t.Errorf("%s flow %d = %+v, page has %+v", depot.Name, j, got, col)
				continue
			}
			if len(got.Stages) != len(col.Stages) {
				t.Errorf("%s %s: %d stages served, page has %d", depot.Name, col.Name, len(got.Stages), len(col.Stages))
				continue
			}
			for k, st := range col.Stages {
				stage := got.Stages[k]
				if stage.Stage != st.Stage || stage.Group != st.Group || stage.Human != st.Human {
					t.Errorf("%s %s %s served as %+v, page has %+v", depot.Name, col.Name, st.Stage, stage, st)
				}
				if !reflect.DeepEqual(rows(stage.Beads), ids(st.Beads)) {
					t.Errorf("%s %s %s beads = %v, page has %v", depot.Name, col.Name, st.Stage, rows(stage.Beads), ids(st.Beads))
				}
				for l, b := range stage.Beads {
					if b.Title != st.Beads[l].Title || b.Working != st.Beads[l].Working {
						t.Errorf("%s served as %+v, page card %+v", b.Bead, b, st.Beads[l])
					}
				}
				// The panel draws the depot's flows; loose has none.
				if depot.Name == "shop" {
					if stage.Lamp != lamp(st.Stage) {
						t.Errorf("%s %s lamp = %q, panel lights %q", depot.Name, st.Stage, stage.Lamp, lamp(st.Stage))
					}
				}
			}
		}
		if !reflect.DeepEqual(rows(served.Elsewhere), ids(depot.Elsewhere)) {
			t.Errorf("%s elsewhere = %v, page has %v", depot.Name, rows(served.Elsewhere), ids(depot.Elsewhere))
		}
	}
}

// quietYard holds no fault, no hold and no failed return: a running
// session, a quiet bead, and parked beads in manual stages (a person's
// turn, blue).
func quietYard(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "shop", Base: "origin/main"}, "me"))
	fl, err := store.ParseFlow("backlog -> new | decide\n" +
		"new -> review on done | decide on question | backlog\n" +
		"review -> approved on done | new on changes | decide on question\n" +
		"approved -> merged on done | new on failed | decide on question\n" +
		"decide -> backlog | new\n" +
		"merged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "shop", "", "code", "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "muse-builders", Runner: "herdr", Limit: 5}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 5}, "me"))
	for _, rt := range []store.Route{{Stage: "new", Group: "muse-builders"}, {Stage: "backlog", Group: "decisions"}, {Stage: "decide", Group: "decisions"}} {
		_, err := f.st.AddRoute(ctx, rt, "me")
		must(t, err)
	}
	run, err := f.st.CreateBead(ctx, store.NewBead{Title: "running", Type: "task", Stage: "new", Depot: "shop"}, "me")
	must(t, err)
	sess, err := f.st.Claim(ctx, run.ID, "muse-builders", "new", "dispatcher")
	must(t, err)
	must(t, f.st.MarkRunning(ctx, sess.ID, map[string]any{"agent": "run-new"}))
	for _, b := range []store.NewBead{
		{Title: "quiet", Type: "task", Stage: "new", Depot: "shop"},
		{Title: "parked", Type: "task", Stage: "backlog", Depot: "shop"},
		{Title: "picked", Type: "task", Stage: "decide", Depot: "shop"},
	} {
		_, err := f.st.CreateBead(ctx, b, "me")
		must(t, err)
	}
}

// A yard with no fault, no hold and no failed return shows no red at all
// on the panel: no held, gave-up or blocked light, no diamond, no fault
// lamp, and no bare signal lamp burning red.
func TestPanelQuietHasNoRed(t *testing.T) {
	f := newFixture(t)
	quietYard(t, f)
	r := f.get("/flows?panel=1")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	for _, not := range []string{
		`block held`, `block gaveup`, `block blocked`, `block stalled`,
		`diamond`, `lamp fault`, `class="lamp"`,
	} {
		if strings.Contains(r.body, not) {
			t.Errorf("a quiet yard shows %s", not)
		}
	}
	for _, want := range []string{
		`block running`, `lamp turn`, `lamp unlit`, `>no route<`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("a quiet yard lacks %s", want)
		}
	}
}

// The Yard page takes the whole window: a wide shell, no right column,
// each depot as wide as its widest plan needs, side by side where there
// is room.
func TestYardPageIsWide(t *testing.T) {
	f := newFixture(t)
	stocked(t, f)
	r := f.get("/flows?panel=1")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if !strings.Contains(r.body, `<div class="shell wide"`) {
		t.Error("the Yard page is not wide")
	}
	if strings.Contains(r.body, "<aside") {
		t.Error("the Yard page keeps its right column")
	}
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	for _, want := range []string{
		".shell.wide { grid-template-columns: var(--menu-width) minmax(0, 1fr); }",
		"#panel { display: flex; flex-wrap: wrap; column-gap: var(--gap); }",
		"#panel > .item { flex: 1 1 max-content; max-width: 100%; min-width: 0; }",
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("the stylesheet lacks %s", want)
		}
	}
}

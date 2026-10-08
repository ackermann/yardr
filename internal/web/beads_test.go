package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// getJSON reads target as JSON into v and returns the answer.
func (f *fixture) getJSON(target string, v any) resp {
	f.t.Helper()
	r := f.get(target, "Accept", "application/json")
	if r.status != 200 {
		f.t.Fatalf("%s: status %d: %s", target, r.status, r.body)
	}
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		f.t.Fatalf("%s: %v: %s", target, err, r.body)
	}
	return r
}

// byTitle maps the title of each open bead to the bead.
func (f *fixture) byTitle() map[string]store.Bead {
	f.t.Helper()
	bs, err := f.st.ListBeads(context.Background(), store.BeadFilter{})
	must(f.t, err)
	out := map[string]store.Bead{}
	for _, b := range bs {
		out[b.Title] = b
	}
	return out
}

// titles are the titles of cards, in their order.
func titles(cs []web.Card) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Title)
	}
	return out
}

// cards are the beads of /beads as the page shows them.
func cards(rs []web.BeadRow) []web.Card {
	out := []web.Card{}
	for _, r := range rs {
		out = append(out, r.Card())
	}
	return out
}

// /flows as JSON: per depot its flows, each stage with its group, its stage
// file and the open beads in it, the newest change first. A bead of a depot
// with no flow is in the depot's elsewhere; a closed one is nowhere.
func TestFlowsJSON(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 1}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Members: []string{"ada"}}, "me"))
	line := "line"
	_, err := f.st.AddRoute(ctx, store.Route{Stage: "new", Depot: &line, Group: "builders"}, "me")
	must(t, err)
	_, err = f.st.AddRoute(ctx, store.Route{Stage: "review", Group: "people"}, "me")
	must(t, err)
	// A store flow's stage file is in the yard's home.
	file := filepath.Join(store.HomeStageDir(f.home, "code"), "new.md")
	must(t, os.MkdirAll(filepath.Dir(file), 0o755))
	must(t, os.WriteFile(file, []byte("Build it.\n"), 0o644))
	head, err := f.st.Head(ctx)
	must(t, err)

	var got web.Flows
	f.getJSON("/flows", &got)
	if got.Head != head || len(got.Depots) != 2 {
		t.Fatalf("head %d, %d depots, want %d and 2: %+v", got.Head, len(got.Depots), head, got)
	}
	depot := got.Depots[0]
	if depot.Name != "line" || len(depot.Flows) != 2 || len(depot.Elsewhere) != 0 || depot.Error != "" {
		t.Fatalf("depot line: %+v", depot)
	}
	code := depot.Flows[0]
	if code.Name != "code" || code.Type != "" || code.Source != "store" || len(code.Stages) != 4 {
		t.Fatalf("flow code: %+v", code)
	}
	type stage struct {
		name, group, file string
		human, terminal   bool
		members, next     []string
		beads             []string
	}
	var have []stage
	for _, s := range code.Stages {
		have = append(have, stage{s.Stage, s.Group, s.StageFile, s.Human, s.Terminal, s.Members, s.Next, titles(s.Beads)})
	}
	want := []stage{
		// "two" was held after "one" was made: the newest change is first.
		{name: "backlog", next: []string{"new"}, beads: []string{"two", "one"}},
		{name: "new", group: "builders", file: file, next: []string{"review", "backlog"}, beads: []string{"three"}},
		{name: "review", group: "people", human: true, members: []string{"ada"}, next: []string{"merged", "new"}, beads: []string{}},
		{name: "merged", terminal: true, next: []string{}, beads: []string{}},
	}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("flow code's stages:\n got %+v\nwant %+v", have, want)
	}
	if two := code.Stages[0].Beads[0]; !two.Held || two.Working || two.Status != "open" || two.Depot != "line" || two.Stage != "backlog" {
		t.Errorf("the held bead's card: %+v", two)
	}
	bugs := depot.Flows[1]
	if bugs.Name != "bugs" || bugs.Type != "bug" || len(bugs.Stages) != 2 || !reflect.DeepEqual(titles(bugs.Stages[0].Beads), []string{"five"}) {
		t.Errorf("flow bugs: %+v", bugs)
	}
	loose := got.Depots[1]
	if loose.Name != "loose" || len(loose.Flows) != 0 || !reflect.DeepEqual(titles(loose.Elsewhere), []string{"six"}) {
		t.Errorf("depot loose: %+v", loose)
	}
}

// A bead that pins a flow of its own follows that one, so it stands off
// the depot's flow even in a stage the depot's flow has (as `yardr flow show`
// counts it).
func TestFlowsPinnedBead(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	fl, err := store.ParseFlow("new -> gone\ngone terminal\n")
	must(t, err)
	fl.Name = "short"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	_, err = f.st.CreateBead(ctx, store.NewBead{Title: "pinned", Stage: "new", Depot: "line",
		Metadata: map[string]any{"flow": "short"}}, "me")
	must(t, err)
	var got web.Flows
	f.getJSON("/flows", &got)
	if depot := got.Depots[0]; !reflect.DeepEqual(titles(depot.Elsewhere), []string{"pinned"}) ||
		!reflect.DeepEqual(titles(depot.Flows[0].Stages[1].Beads), []string{"three"}) {
		t.Errorf("elsewhere %v, new %v", titles(depot.Elsewhere), titles(depot.Flows[0].Stages[1].Beads))
	}
}

// A depot whose flows cannot be read still shows its beads, and the other
// depots their flows.
func TestFlowsReadError(t *testing.T) {
	f := newFixture(t)
	f.populate()
	srv, err := web.NewServer(failingDepotFlows{API: f.st, depot: "line"}, f.home, nil)
	must(t, err)
	f.ts.Config.Handler = srv
	var got web.Flows
	f.getJSON("/flows", &got)
	if depot := got.Depots[0]; depot.Error == "" || len(depot.Flows) != 0 || len(depot.Elsewhere) != 4 {
		t.Errorf("depot line: %+v", depot)
	}
	if r := f.get("/flows"); r.status != 200 || !strings.Contains(r.body, `id="off/loose"`) {
		t.Errorf("the page: status %d", r.status)
	}
}

// section is the id of the element that holds a card on /flows.
var (
	sectionRE = regexp.MustCompile(`<div class="cards" id="([^"]+)" sse-swap="[^"]+" hx-swap="afterbegin settle:0ms">(.*?)</div>`)
	cardRE    = regexp.MustCompile(`<article class="card( arrived)?" id="([^"]+)"`)
)

// sections reads /flows as a browser shows it: the id of each card's
// element, by the id of the section it is in.
func (f *fixture) sections() map[string]string {
	f.t.Helper()
	r := f.get("/flows")
	if r.status != 200 {
		f.t.Fatalf("/flows: status %d: %s", r.status, r.body)
	}
	out := map[string]string{}
	for _, sec := range sectionRE.FindAllStringSubmatch(squashLines(r.body), -1) {
		for _, c := range cardRE.FindAllStringSubmatch(sec[2], -1) {
			out[c[2]] = sec[1]
		}
	}
	return out
}

func squashLines(s string) string { return strings.ReplaceAll(s, "\n", " ") }

// The page: one section per stage, named so the stream can put a card in
// it, each bead a card that links to its page; and the stream it listens
// to, after the page's head.
func TestFlowsHTML(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	head, err := f.st.Head(ctx)
	must(t, err)
	r := f.get("/flows")
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	bs := f.byTitle()
	for _, want := range []string{
		fmt.Sprintf(`<div class="shell" hx-ext="sse" sse-connect="/events?after=%d&amp;format=card">`, head),
		// settle:0ms: a card the stream brought listens at once, so the
		// bead's next event, however soon, takes it out again.
		`<div class="cards" id="at/line//backlog" sse-swap="at/line//backlog" hx-swap="afterbegin settle:0ms">`,
		`<div class="cards" id="at/line//merged" sse-swap="at/line//merged" hx-swap="afterbegin settle:0ms">`,
		`<div class="cards" id="at/line/bug/triage" sse-swap="at/line/bug/triage" hx-swap="afterbegin settle:0ms">`,
		`<div class="cards" id="off/loose" sse-swap="off/loose" hx-swap="afterbegin settle:0ms">`,
		`<article class="card" id="bead/` + bs["three"].ID + `" sse-swap="bead/` + bs["three"].ID + `" hx-swap="delete">`,
		`<a class="bead" href="/beads/` + bs["three"].ID + `">` + bs["three"].ID + `</a>`,
		`<p class="card-title">three</p>`,
		`<a href="/flows" aria-current="page">`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	got := f.sections()
	want := map[string]string{
		"bead/" + bs["one"].ID: "at/line//backlog", "bead/" + bs["two"].ID: "at/line//backlog",
		"bead/" + bs["three"].ID: "at/line//new", "bead/" + bs["five"].ID: "at/line/bug/triage",
		"bead/" + bs["six"].ID: "off/loose",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cards by section:\n got %v\nwant %v", got, want)
	}
}

// apply does to cards (a card's element id to its section's) what the page
// does with one frame of the card stream: the card that hears its name
// takes itself out, the section that hears its name puts the card on top.
// The frame ends with the unnamed message the right column hears, the
// event's seq, whether a card moved or not.
func apply(t *testing.T, cards map[string]string, fr frame) {
	t.Helper()
	left := ""
	if n := len(fr.msgs); n == 0 || fr.msgs[n-1] != (msg{"", fmt.Sprint(fr.id)}) {
		t.Fatalf("frame %d does not end with its seq as an unnamed message: %+v", fr.id, fr.msgs)
	}
	for i, m := range fr.msgs[:len(fr.msgs)-1] {
		switch {
		case i == 0 && strings.HasPrefix(m.event, "bead/"):
			left = m.event
			delete(cards, left)
		case i == 1 && (strings.HasPrefix(m.event, "at/") || strings.HasPrefix(m.event, "off/")):
			c := cardRE.FindStringSubmatch(m.data)
			if c == nil || c[2] != left || !strings.Contains(m.data, ` sse-swap="`+left+`" hx-swap="delete"`) {
				t.Fatalf("frame %d puts in another card than it took out, or one that cannot leave: %s", fr.id, m.data)
			}
			cards[c[2]] = m.event
		default:
			t.Fatalf("frame %d: message %d is %q: %s", fr.id, i, m.event, m.data)
		}
	}
}

// put is the card a frame of the card stream puts into a section, "" when
// it puts none.
func put(fr frame) string {
	for _, m := range fr.msgs {
		if strings.HasPrefix(m.event, "at/") || strings.HasPrefix(m.event, "off/") {
			return m.data
		}
	}
	return ""
}

// The card stream moves a bead's card from one section of /flows to the
// next: taken through new, review and merged, the card leaves each section
// and arrives in the other, and leaves the page when the bead lands. What
// an open page then shows is what a reload shows.
func TestFlowsStream(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	bs := f.byTitle()
	three, six := bs["three"], bs["six"]
	page := f.sections()
	var flows web.Flows
	f.getJSON("/flows", &flows)
	s := f.stream(t, fmt.Sprintf("/events?after=%d&format=card", flows.Head))
	// step runs one change and applies the frames it sends to the page.
	step := func(change func(), frames int) frame {
		t.Helper()
		change()
		var fr frame
		for range frames {
			fr = s.next(t)
			apply(t, page, fr)
		}
		if got := f.sections(); !reflect.DeepEqual(page, got) {
			t.Fatalf("after frame %d the open page has\n%v\na reload shows\n%v", fr.id, page, got)
		}
		return fr
	}
	card := "bead/" + three.ID

	// new to review: out of new, into review, seen arriving.
	fr := step(func() {
		_, err := f.st.Advance(ctx, three.ID, "review", store.Expect{}, "me")
		must(t, err)
	}, 1)
	if page[card] != "at/line//review" || !strings.Contains(put(fr), `<article class="card arrived" id="`+card+`"`) {
		t.Errorf("the move to review: section %q, frame %s", page[card], put(fr))
	}
	// A hold changes the card where it is: nothing arrives.
	fr = step(func() {
		_, err := f.st.SetHold(ctx, three.ID, true, store.Expect{}, "me")
		must(t, err)
	}, 1)
	if page[card] != "at/line//review" || strings.Contains(put(fr), "arrived") || !strings.Contains(put(fr), `<span class="held">held</span>`) {
		t.Errorf("the hold: section %q, frame %s", page[card], put(fr))
	}
	// A note changes no card: its seq alone, for the right column.
	if fr = step(func() { must(t, f.st.AddNote(ctx, three.ID, "looks right", "me")) }, 1); len(fr.msgs) != 1 {
		t.Errorf("a note came with a card: %+v", fr.msgs)
	}
	step(func() {
		_, err := f.st.SetHold(ctx, three.ID, false, store.Expect{}, "me")
		must(t, err)
	}, 1)
	// A new bead arrives in the first stage of its flow.
	var made *store.Bead
	fr = step(func() {
		var err error
		made, err = f.st.CreateBead(ctx, store.NewBead{Title: "seven", Type: "bug", Depot: "line"}, "me")
		must(t, err)
	}, 1)
	if page["bead/"+made.ID] != "at/line/bug/triage" || !strings.Contains(put(fr), "card arrived") {
		t.Errorf("the new bead: section %q, frame %s", page["bead/"+made.ID], put(fr))
	}
	// A bead of a depot with no flow stays off the flow.
	step(func() {
		_, err := f.st.Advance(ctx, six.ID, "anywhere", store.Expect{}, "me")
		must(t, err)
	}, 1)
	if page["bead/"+six.ID] != "off/loose" {
		t.Errorf("the loose bead is in %q", page["bead/"+six.ID])
	}
	// review to merged lands the bead: an `advanced` and a `closed`, and
	// the card is gone from the page.
	fr = step(func() {
		_, err := f.st.Advance(ctx, three.ID, "merged", store.Expect{}, "me")
		must(t, err)
	}, 2)
	if _, there := page[card]; there || len(fr.msgs) != 2 || fr.msgs[0] != (msg{card, "left"}) {
		t.Errorf("the landed bead is still on the page: %+v", fr.msgs)
	}
	// Reopened, it stands in the stage it landed in.
	step(func() {
		_, err := f.st.Reopen(ctx, three.ID, store.Expect{}, "me")
		must(t, err)
	}, 1)
	if page[card] != "at/line//merged" {
		t.Errorf("the reopened bead is in %q", page[card])
	}
	s.quiet(t)
}

// A move is three messages: the card leaves, it enters its section, and
// the event's seq for whatever reads itself again. Only the last carries
// the seq as its id, so a browser cut off between them reconnects with the
// seq before and is sent the whole move again; with the id on the first,
// the card would be gone from the page for good.
func TestCardStreamCursor(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	three := f.byTitle()["three"]
	head, err := f.st.Head(ctx)
	must(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/events?after=%d&format=card", f.ts.URL, head), nil)
	must(t, err)
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	defer res.Body.Close()
	_, err = f.st.Advance(ctx, three.ID, "review", store.Expect{}, "me")
	must(t, err)
	var lines []string
	for sc := bufio.NewScanner(res.Body); sc.Scan(); {
		if line := sc.Text(); !strings.HasPrefix(line, "retry:") && !strings.HasPrefix(line, "data: ") && line != "" {
			lines = append(lines, line)
		}
		if len(lines) == 3 {
			break
		}
	}
	want := []string{"event: bead/" + three.ID, "event: at/line//review", fmt.Sprintf("id: %d", head+1)}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("the move came as %q, want %q", lines, want)
	}
}

// /beads: the open beads, the newest change first, with the query string's
// filters for depot and stage.
func TestBeadsJSON(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	head, err := f.st.Head(ctx)
	must(t, err)
	var got web.Beads
	f.getJSON("/beads", &got)
	// "two" changed last (its hold); the rest in the order they were made.
	if want := []string{"two", "six", "five", "three", "one"}; !reflect.DeepEqual(titles(cards(got.Beads)), want) {
		t.Errorf("beads %v, want %v", titles(cards(got.Beads)), want)
	}
	if got.Head != head || !reflect.DeepEqual(got.Depots, []string{"line", "loose"}) ||
		!reflect.DeepEqual(got.Stages, []string{"backlog", "inbox", "new", "triage"}) {
		t.Errorf("head %d (want %d), depots %v, stages %v", got.Head, head, got.Depots, got.Stages)
	}
	for target, want := range map[string][]string{
		"/beads?depot=line":               {"two", "five", "three", "one"},
		"/beads?stage=backlog":            {"two", "one"},
		"/beads?depot=line&stage=new":     {"three"},
		"/beads?depot=loose&stage=new":    {},
		"/beads?depot=nowhere":            {},
		"/beads?rig=line&stage=new":       {"three"}, // the filter's name until this release
		"/beads?depot=loose&stage=inbox&": {"six"},
	} {
		var got web.Beads
		f.getJSON(target, &got)
		if !reflect.DeepEqual(titles(cards(got.Beads)), want) {
			t.Errorf("%s: %v, want %v", target, titles(cards(got.Beads)), want)
		}
	}
	// The stages a depot's filter offers are the depot's.
	f.getJSON("/beads?depot=loose&stage=inbox", &got)
	if got.Depot != "loose" || got.Stage != "inbox" || !reflect.DeepEqual(got.Stages, []string{"inbox"}) {
		t.Errorf("filtered: %+v", got)
	}
}

// The page: the cards, the filters as links that keep each other, and the
// request it repeats when the stream says something happened.
func TestBeadsHTML(t *testing.T) {
	f := newFixture(t)
	f.populate()
	head, err := f.st.Head(context.Background())
	must(t, err)
	r := f.get("/beads?depot=line&stage=new")
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	three := f.byTitle()["three"]
	for _, want := range []string{
		fmt.Sprintf(`<div class="shell" hx-ext="sse" sse-connect="/events?after=%d&amp;format=seq">`, head),
		`<div id="beads" hx-get="/beads?depot=line&amp;stage=new" hx-trigger="sse:message" hx-select="#beads > *" hx-swap="innerHTML ignoreTitle:true">`,
		`<a href="/beads?stage=new">all</a>`,
		`<a href="/beads?depot=line&amp;stage=new" aria-current="true">line</a>`,
		`<a href="/beads?depot=loose&amp;stage=new">loose</a>`,
		`<a href="/beads?depot=line">all</a>`,
		`<a href="/beads?depot=line&amp;stage=backlog">backlog</a>`,
		`<a class="bead" href="/beads/` + three.ID + `">`,
		`line · new · task`,
		`<a href="/beads" aria-current="page">`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	// The right column may name it: "two" is held.
	list, _, _ := strings.Cut(r.body[strings.Index(r.body, `<div id="beads"`):], "</main>")
	if strings.Contains(list, ">two<") {
		t.Error("the filtered page shows a bead of another stage")
	}
	// Only /flows moves cards one by one: here they listen to nothing.
	if strings.Contains(r.body, `sse-swap="bead/`) {
		t.Error("a card of the list listens to the stream")
	}
}

// whole gives the yard a train with two children, one blocking the other,
// in a flow with outcomes and a stage file, with notes and a session, and
// returns the blocked child.
func whole(t *testing.T, f *fixture) (train, blocker, b *store.Bead) {
	t.Helper()
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "line", Base: "origin/main"}, "me"))
	fl, err := store.ParseFlow("new -> review on done\nnew -> decide on question\nreview -> merged\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "line", "", "code", "me"))
	file := filepath.Join(store.HomeStageDir(f.home, "code"), "new.md")
	must(t, os.MkdirAll(filepath.Dir(file), 0o755))
	// The summary line is for the map in yardr prime: the page leaves it out.
	must(t, os.WriteFile(file, []byte("summary: Built here.\n\nA builder implements the bead here.\n"), 0o644))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"))
	train, err = f.st.CreateBead(ctx, store.NewBead{Title: "the shed", Type: store.TypeTrain, Stage: "new", Depot: "line"}, "me")
	must(t, err)
	blocker, err = f.st.CreateBead(ctx, store.NewBead{Title: "the posts", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	b, err = f.st.CreateBead(ctx, store.NewBead{Title: "a door", Body: "Hang it.\n\n- on two hinges", Stage: "new", Depot: "line",
		Priority: 1, Labels: []string{"wood"}}, "me")
	must(t, err)
	// Claimed first: a blocked bead is not handed to anyone.
	_, err = f.st.Claim(ctx, b.ID, "builders", "new", "dispatcher")
	must(t, err)
	must(t, f.st.AddDep(ctx, train.ID, blocker.ID, store.DepParent, "me"))
	must(t, f.st.AddDep(ctx, train.ID, b.ID, store.DepParent, "me"))
	must(t, f.st.AddDep(ctx, blocker.ID, b.ID, store.DepBlocks, "me"))
	must(t, f.st.AddNote(ctx, b.ID, "the hinge is missing", "builder"))
	must(t, f.st.AddNote(ctx, b.ID, "found one", "passer"))
	b, err = f.st.GetBead(ctx, b.ID)
	must(t, err)
	return train, blocker, b
}

// A bead's page as JSON: what `yardr prime --bead` knows.
func TestBeadJSON(t *testing.T) {
	f := newFixture(t)
	train, blocker, b := whole(t, f)
	head, err := f.st.Head(context.Background())
	must(t, err)
	var got web.BeadPage
	f.getJSON("/beads/"+b.ID, &got)
	if got.Bead == nil || got.Bead.ID != b.ID || got.Bead.Title != "a door" || got.Bead.Body != "Hang it.\n\n- on two hinges" ||
		got.Bead.Stage != "new" || got.Bead.Type != "task" || got.Head != head {
		t.Fatalf("the bead: %+v, head %d (want %d)", got.Bead, got.Head, head)
	}
	if got.Flow == nil || *got.Flow != (web.BeadFlow{Name: "code", Source: "store"}) {
		t.Errorf("flow %+v", got.Flow)
	}
	if got.Base != store.Branch(train.ID) || got.Train == nil || got.Train.ID != train.ID || got.Train.Title != "the shed" {
		t.Errorf("base %q, train %+v", got.Base, got.Train)
	}
	if len(got.Blockers) != 1 || got.Blockers[0].ID != blocker.ID || got.Blockers[0].Title != "the posts" {
		t.Errorf("blockers %+v", got.Blockers)
	}
	if got.StageFile == nil || got.StageFile.Body() != "A builder implements the bead here.\n" ||
		got.StageFile.Path != filepath.Join(store.HomeStageDir(f.home, "code"), "new.md") {
		t.Errorf("stage file %+v", got.StageFile)
	}
	if want := []web.BeadOutcome{
		{Name: "done", To: "review", Command: "yardr bead done " + b.ID},
		{Name: "question", To: "decide", Command: "yardr bead done " + b.ID + " --outcome question"},
	}; !reflect.DeepEqual(got.Outcomes, want) {
		t.Errorf("outcomes %+v, want %+v", got.Outcomes, want)
	}
	if !reflect.DeepEqual(got.Next, []string{"review", "decide"}) {
		t.Errorf("next %v", got.Next)
	}
	// Oldest first.
	if len(got.Notes) != 2 || got.Notes[0].Text != "the hinge is missing" || got.Notes[0].Author != "builder" || got.Notes[1].Text != "found one" {
		t.Errorf("notes %+v", got.Notes)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Group != "builders" || got.Sessions[0].Stage != "new" || got.Sessions[0].Bead != b.ID {
		t.Errorf("sessions %+v", got.Sessions)
	}
	if len(got.Deps) < 2 {
		t.Errorf("deps %+v", got.Deps)
	}
	// The train's page has its children; a bead no flow governs says so by
	// having none.
	var ep web.BeadPage
	f.getJSON("/beads/"+train.ID, &ep)
	if ids := []string{ep.Children[0].ID, ep.Children[1].ID}; len(ep.Children) != 2 || !reflect.DeepEqual(ids, []string{blocker.ID, b.ID}) || ep.Train != nil {
		t.Errorf("the train's children %+v", ep.Children)
	}
}

// The page, for a person; an unknown bead is a 404 in both forms.
func TestBeadHTML(t *testing.T) {
	f := newFixture(t)
	train, blocker, b := whole(t, f)
	head, err := f.st.Head(context.Background())
	must(t, err)
	r := f.get("/beads/" + b.ID)
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	for _, want := range []string{
		fmt.Sprintf(`<div class="shell" hx-ext="sse" sse-connect="/events?after=%d&amp;format=seq">`, head),
		`<div id="bead" hx-get="/beads/` + b.ID + `" hx-trigger="sse:message" hx-select="#bead > *" hx-swap="innerHTML ignoreTitle:true">`,
		`<title>` + b.ID + ` · depot</title>`,
		`<h2>a door</h2>`,
		`task · stage new · open · P1`,
		`<dt>flow</dt><dd>code <span class="faint">(store)</span></dd>`,
		`<dt>base</dt><dd>yard/` + train.ID + `</dd>`,
		`<dt>train</dt><dd><a class="bead" href="/beads/` + train.ID + `">` + train.ID + `</a> the shed</dd>`,
		`<dt>labels</dt><dd>wood</dd>`,
		`<a class="bead" href="/beads/` + blocker.ID + `">`,
		"<pre class=\"text\">Hang it.\n\n- on two hinges</pre>",
		"<pre class=\"text file\">A builder implements the bead here.\n</pre>",
		`<code>yardr bead done ` + b.ID + ` --outcome question</code>`,
		`<pre class="text">the hinge is missing</pre>`,
		`builders · stage new`,
		`<a href="/beads" aria-current="page">`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if first, second := strings.Index(r.body, "the hinge is missing"), strings.Index(r.body, "found one"); first < 0 || second < first {
		t.Error("the notes are not oldest first")
	}
	if r := f.get("/beads/line-none"); r.status != 404 || !strings.Contains(r.body, `class="item held"`) {
		t.Errorf("an unknown bead: status %d", r.status)
	}
	if r := f.get("/beads/line-none", "Accept", "application/json"); r.status != 404 || !strings.Contains(r.body, `"error"`) {
		t.Errorf("an unknown bead as JSON: status %d: %s", r.status, r.body)
	}
}

// A bead found from another names it, and the origin's page lists what it
// led to, closed beads too.
func TestBeadDiscoveredFrom(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	origin, err := f.st.CreateBead(ctx, store.NewBead{Title: "the origin"}, "me")
	must(t, err)
	found, err := f.st.CreateBead(ctx, store.NewBead{Title: "a loose end", DiscoveredFrom: origin.ID}, "me")
	must(t, err)
	_, err = f.st.CloseBead(ctx, found.ID, "fixed", store.Expect{}, "me")
	must(t, err)

	var got web.BeadPage
	f.getJSON("/beads/"+found.ID, &got)
	if len(got.DiscoveredFrom) != 1 || got.DiscoveredFrom[0].ID != origin.ID || got.DiscoveredFrom[0].Title != "the origin" || len(got.LedTo) != 0 {
		t.Errorf("the found bead: discovered from %+v, led to %+v", got.DiscoveredFrom, got.LedTo)
	}
	got = web.BeadPage{}
	f.getJSON("/beads/"+origin.ID, &got)
	if len(got.LedTo) != 1 || got.LedTo[0].ID != found.ID || got.LedTo[0].Status != "closed" || len(got.DiscoveredFrom) != 0 {
		t.Errorf("the origin: led to %+v, discovered from %+v", got.LedTo, got.DiscoveredFrom)
	}
	if want, body := `<dt>discovered from</dt><dd><a class="bead" href="/beads/`+origin.ID+`">`+origin.ID+`</a> the origin</dd>`, f.get("/beads/"+found.ID).body; !strings.Contains(body, want) {
		t.Errorf("the found bead's page lacks %s", want)
	}
	body := f.get("/beads/" + origin.ID).body
	if at := strings.Index(body, `<h3 class="label">led to</h3>`); at < 0 || !strings.Contains(body[at:], `href="/beads/`+found.ID+`"`) {
		t.Errorf("the origin's page does not list %s under led to", found.ID)
	}
}

// A bead's title, body, notes and stage file are text someone wrote. Every
// page and the card stream write them as text, never as markup; JSON
// carries them as they are.
func TestBeadEscapes(t *testing.T) {
	f := newFixture(t)
	_, _, b := whole(t, f)
	ctx := context.Background()
	const evil = `<script>alert(1)</script><img src=x onerror=alert(2)>`
	file := filepath.Join(store.HomeStageDir(f.home, "code"), "new.md")
	must(t, os.WriteFile(file, []byte("stage "+evil), 0o644))
	head, err := f.st.Head(ctx)
	must(t, err)
	title, body := "title "+evil, "body "+evil
	_, err = f.st.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title, Body: &body}, store.Expect{}, "me")
	must(t, err)
	must(t, f.st.AddNote(ctx, b.ID, "note "+evil, "author "+evil))

	clean := func(what, html string) {
		t.Helper()
		if strings.Contains(html, "<script>alert") || strings.Contains(html, "<img") {
			t.Errorf("%s carries what someone wrote as markup", what)
		}
	}
	page := f.get("/beads/" + b.ID).body
	clean("the bead's page", page)
	const escaped = `&lt;script&gt;alert(1)&lt;/script&gt;&lt;img src=x onerror=alert(2)&gt;`
	for _, part := range []string{"title ", "body ", "note ", "stage ", "author "} {
		if !strings.Contains(page, part+escaped) {
			t.Errorf("the bead's page does not show the %sas text", part)
		}
	}
	for _, target := range []string{"/flows", "/beads", "/"} {
		p := f.get(target).body
		clean(target, p)
		if !strings.Contains(p, "title "+escaped) {
			t.Errorf("%s does not show the title as text", target)
		}
	}
	s := f.stream(t, fmt.Sprintf("/events?after=%d&format=card", head))
	if c := put(s.next(t)); !strings.Contains(c, "title "+escaped) {
		t.Errorf("the card stream does not show the title as text: %s", c)
	} else {
		clean("the card stream", c)
	}
	var got web.BeadPage
	f.getJSON("/beads/"+b.ID, &got)
	if got.Bead.Body != body || got.Notes[len(got.Notes)-1].Text != "note "+evil || got.StageFile.Text != "stage "+evil {
		t.Errorf("JSON changed what was written: %+v", got)
	}
}

// ?format=seq carries each event's seq and nothing else, whatever the
// event is of: what a page that reads itself again listens to.
func TestEventsSeq(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	head, err := f.st.Head(ctx)
	must(t, err)
	mine, other := bead(t, f.st, "mine"), bead(t, f.st, "other")
	must(t, f.st.AddNote(ctx, other.ID, "not for you", "me"))
	must(t, f.st.AddNote(ctx, mine.ID, "for you", "me"))

	s := f.stream(t, fmt.Sprintf("/events?after=%d&format=seq", head))
	for i := range 4 {
		if fr := s.next(t); fr.id != head+uint64(i)+1 || fr.data != fmt.Sprint(fr.id) {
			t.Errorf("frame %d: id %d, data %q", i, fr.id, fr.data)
		}
	}
	if r := f.get("/events?format=cards"); r.status != 400 {
		t.Errorf("format=cards: status %d, want 400", r.status)
	}
}

// A bead a manual group has claimed is a person's turn: its card has no
// working dot and says no "working" on /flows, on /beads, on its own page
// and as a child of its train, as JSON and as the page, and its session is
// listed as claimed, not running. A bead an agent's session has claimed is
// working everywhere, as before.
func TestManualClaimIsNotWorking(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	idea := backlogYard(t, f, 1)[0]
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	door, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	sess, err := f.st.Claim(ctx, door.ID, "builders", "new", "yard")
	must(t, err)
	must(t, f.st.MarkRunning(ctx, sess.ID, map[string]any{"agent": "door-new"}))
	train, err := f.st.CreateBead(ctx, store.NewBead{Title: "the shed", Type: store.TypeTrain, Stage: "new", Depot: "line"}, "me")
	must(t, err)
	must(t, f.st.AddDep(ctx, train.ID, idea.ID, store.DepParent, "me"))
	must(t, f.st.AddDep(ctx, train.ID, door.ID, store.DepParent, "me"))

	// working is what cards say of the two beads, by title.
	working := func(cs []web.Card) map[string]bool {
		out := map[string]bool{}
		for _, c := range cs {
			if c.ID == idea.ID || c.ID == door.ID {
				out[c.Title] = c.Working
			}
		}
		return out
	}
	want := map[string]bool{"idea a": false, "a door": true}
	var flows web.Flows
	flowsJSON := f.getJSON("/flows", &flows)
	if got := working(flows.Depots[0].Elsewhere); !reflect.DeepEqual(got, want) {
		t.Errorf("/flows: working %v, want %v", got, want)
	}
	var beads web.Beads
	beadsJSON := f.getJSON("/beads", &beads)
	if got := working(cards(beads.Beads)); !reflect.DeepEqual(got, want) {
		t.Errorf("/beads: working %v, want %v", got, want)
	}
	var ep, claimed, built web.BeadPage
	trainJSON := f.getJSON("/beads/"+train.ID, &ep)
	if got := working(ep.Children); !reflect.DeepEqual(got, want) {
		t.Errorf("the train's children: working %v, want %v", got, want)
	}
	f.getJSON("/beads/"+idea.ID, &claimed)
	f.getJSON("/beads/"+door.ID, &built)
	if claimed.Working || !built.Working {
		t.Errorf("the beads' pages: working %v for the manual claim, %v for the agent's, want false and true", claimed.Working, built.Working)
	}
	if r := f.get("/beads/"+idea.ID, "Accept", "application/json"); !strings.Contains(r.body, `"working": false`) {
		t.Errorf("the manual claim's page as JSON lacks \"working\":false: %s", r.body)
	}
	assertCardWorking := func(label string, body, id string, want bool) {
		t.Helper()
		var root any
		if err := json.Unmarshal([]byte(body), &root); err != nil {
			t.Fatalf("%s: decode JSON: %v", label, err)
		}
		var find func(any) (bool, bool)
		find = func(v any) (bool, bool) {
			switch v := v.(type) {
			case map[string]any:
				if v["id"] == id {
					got, ok := v["working"].(bool)
					return got, ok
				}
				for _, child := range v {
					if got, ok := find(child); ok {
						return got, true
					}
				}
			case []any:
				for _, child := range v {
					if got, ok := find(child); ok {
						return got, true
					}
				}
			}
			return false, false
		}
		if got, ok := find(root); !ok || got != want {
			t.Errorf("%s: card %s working = %v (present %v), want %v", label, id, got, ok, want)
		}
	}
	for _, check := range []struct {
		label, body string
	}{
		{"/flows JSON", flowsJSON.body}, {"/beads JSON", beadsJSON.body}, {"train page JSON", trainJSON.body},
	} {
		assertCardWorking(check.label, check.body, idea.ID, false)
		assertCardWorking(check.label, check.body, door.ID, true)
	}

	// cardOf is the bead's card on a page, "" when the page has none.
	cardOf := func(body, id string) string {
		at := strings.Index(body, `<article class="card" id="bead/`+id+`"`)
		if at < 0 {
			return ""
		}
		return body[at : at+strings.Index(body[at:], "</article>")]
	}
	for _, page := range []string{"/flows", "/beads", "/beads/" + train.ID} {
		body := f.get(page).body
		if c := cardOf(body, idea.ID); c == "" || strings.Contains(c, "working") || !strings.Contains(c, `<span class="dot"></span>`) {
			t.Errorf("%s: the manual claim's card: %q", page, c)
		}
		if c := cardOf(body, door.ID); !strings.Contains(c, `<span class="dot working"></span>`) || !strings.Contains(c, "· working") {
			t.Errorf("%s: the agent's bead's card: %q", page, c)
		}
	}
	body := squash(f.get("/beads/" + idea.ID).body)
	for _, want := range []string{
		`<span class="dot"></span>task · stage backlog · open`,
		`backlog · stage backlog · manual</span>`,
		`<p><span class="dot"></span>claimed <span class="faint">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the manual claim's page lacks %s", want)
		}
	}
	// The right column has a working dot of its own, for its count.
	if bead, _, _ := strings.Cut(body[strings.Index(body, `<div id="bead"`):], `<aside`); strings.Contains(bead, "dot working") {
		t.Errorf("the manual claim's page has a working dot: %s", bead)
	}
	body = squash(f.get("/beads/" + door.ID).body)
	for _, want := range []string{
		`<span class="dot working"></span>task · stage new · open`,
		`<p><span class="dot working"></span>running <span class="faint">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the agent's bead's page lacks %s", want)
		}
	}
}

// The card stream places a bead a manual group claims as not working, and
// one an agent's session claims as working: what a reload shows.
func TestFlowsStreamManualClaim(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "line", Base: "origin/main"}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "backlog", Runner: "manual"}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake"}, "me"))
	idea, err := f.st.CreateBead(ctx, store.NewBead{Title: "an idea", Stage: "backlog", Depot: "line"}, "me")
	must(t, err)
	door, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	head, err := f.st.Head(ctx)
	must(t, err)
	s := f.stream(t, fmt.Sprintf("/events?after=%d&format=card", head))

	_, err = f.st.Claim(ctx, idea.ID, "backlog", "backlog", "yard")
	must(t, err)
	if c := put(s.next(t)); !strings.Contains(c, `id="bead/`+idea.ID+`"`) || strings.Contains(c, "working") {
		t.Errorf("the manual claim's card: %s", c)
	}
	_, err = f.st.Claim(ctx, door.ID, "builders", "new", "yard")
	must(t, err)
	if c := put(s.next(t)); !strings.Contains(c, `id="bead/`+door.ID+`"`) || !strings.Contains(c, `<span class="dot working"></span>`) || !strings.Contains(c, "· working") {
		t.Errorf("the agent's bead's card: %s", c)
	}
	s.quiet(t)
}

// asked counts what the view asks the store for.
type asked struct {
	store.API
	mu sync.Mutex
	n  map[string]int
}

func (a *asked) count(what string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n[what]++
}

func (a *asked) reset() map[string]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	was := a.n
	a.n = map[string]int{}
	return was
}

func (a *asked) ListBeads(ctx context.Context, f store.BeadFilter) ([]store.Bead, error) {
	a.count("ListBeads")
	return a.API.ListBeads(ctx, f)
}

func (a *asked) Sessions(ctx context.Context, all bool) ([]store.Session, error) {
	if all {
		a.count("Sessions, ended ones too")
	} else {
		a.count("Sessions")
	}
	return a.API.Sessions(ctx, all)
}

func (a *asked) ListSessions(ctx context.Context, f store.SessionFilter) ([]store.Session, error) {
	a.count("ListSessions of " + f.Bead)
	return a.API.ListSessions(ctx, f)
}

// A bead's page reads that bead's sessions, ended ones too, and the live
// ones: never every session the yard keeps, which took a second at 20,000
// beads (yardr-wav4).
func TestBeadPageReadsItsOwnSessions(t *testing.T) {
	st := &asked{API: storetest.New(t), n: map[string]int{}}
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	must(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	f := &fixture{t: t, st: st, ts: ts}
	ctx := context.Background()
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	var beads []*store.Bead
	var ids []string
	for _, title := range []string{"a door", "a shed"} {
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: title, Stage: "new"}, "me")
		must(t, err)
		beads = append(beads, b)
	}
	// The door is worked twice, its first session dead; the shed once.
	for _, b := range []*store.Bead{beads[0], beads[1], beads[0]} {
		sess, err := f.st.Claim(ctx, b.ID, "builders", "new", "yard")
		must(t, err)
		ids = append(ids, sess.ID)
		if len(ids) == 1 {
			must(t, f.st.EndSession(ctx, sess.ID, "died", "gone"))
		}
	}
	st.reset()
	var page web.BeadPage
	f.getJSON("/beads/"+beads[0].ID, &page)
	got := st.reset()
	if got["ListSessions of "+beads[0].ID] != 1 || got["Sessions"] != 1 || got["Sessions, ended ones too"] != 0 {
		t.Errorf("the bead's page asked the store for %v, want its own sessions and the live ones, once each", got)
	}
	if len(page.Sessions) != 2 || page.Sessions[0].ID != ids[0] || page.Sessions[1].ID != ids[2] {
		t.Errorf("the bead's sessions = %+v, want %s then %s", page.Sessions, ids[0], ids[2])
	}
	if !page.Working {
		t.Error("the bead has a live session and the page does not say it is worked")
	}
}

func (a *asked) Events(ctx context.Context, bead string, limit int) ([]store.Event, error) {
	a.count("Events")
	return a.API.Events(ctx, bead, limit)
}

// /beads costs what its open beads cost, and each of them once: the list
// and the right column are made from one listing and the live sessions,
// and a bead's events are read only where the column says something from
// them: who held a bead, whether an agent's session waits on a prompt. A
// manual group's claim runs nothing that could, and a yard has one on
// every parked bead. The column is the one every other page shows.
func TestBeadsReadsTheYardOnce(t *testing.T) {
	st := &asked{API: storetest.New(t), n: map[string]int{}}
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	must(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	f := &fixture{t: t, st: st, ts: ts}
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "line", Base: "origin/main"}, "me"))
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "loose", Base: "origin/main"}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 5}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	_, err = f.st.AddRoute(ctx, store.Route{Stage: "decide", Group: "decisions"}, "me")
	must(t, err)
	bead := func(title, stage, depot string) *store.Bead {
		t.Helper()
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: title, Stage: stage, Depot: depot}, "me")
		must(t, err)
		return b
	}
	for _, title := range []string{"which colour", "which size", "which wood"} {
		sess, err := f.st.Claim(ctx, bead(title, "decide", "line").ID, "decisions", "decide", "yard")
		must(t, err)
		must(t, f.st.MarkRunning(ctx, sess.ID, map[string]any{"manual": true}))
	}
	door := bead("a door", "new", "line")
	sess, err := f.st.Claim(ctx, door.ID, "builders", "new", "yard")
	must(t, err)
	must(t, f.st.MarkRunning(ctx, sess.ID, map[string]any{"agent": "door-new"}))
	must(t, f.st.LogDispatch(ctx, door.ID, "session_blocked", "yard", map[string]any{"session": sess.ID}))
	_, err = f.st.SetHold(ctx, bead("a shed", "inbox", "loose").ID, true, store.Expect{}, "me")
	must(t, err)
	// A bead of no depot is in the list and in no depot's count.
	bead("a thought", "inbox", "")

	column := func(body string) string {
		t.Helper()
		_, aside, ok := strings.Cut(body, "<aside")
		if !ok {
			t.Fatalf("the page has no right column: %s", body)
		}
		return aside
	}
	home := column(f.get("/").body)
	st.reset()
	r := f.get("/beads")
	got := st.reset()
	if want := map[string]int{"ListBeads": 1, "Sessions": 1, "Events": 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("/beads asked the store for %v, want %v", got, want)
	}
	if col := column(r.body); col != home {
		t.Errorf("the column of /beads\n%s\nis not Home's\n%s", col, home)
	}
	text := squash(home)
	for _, want := range []string{
		`<h3 class="label">Parked · 3</h3>`,
		`<p class="why held">its session waits on a prompt: door-new</p>`,
		`<p class="why held">held by me</p>`,
		`<dt>open</dt><dd>5</dd>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the column lacks %s: %s", want, text)
		}
	}
	var list web.Beads
	f.getJSON("/beads", &list)
	if want := []string{"a thought", "a shed", "a door", "which wood", "which size", "which colour"}; !reflect.DeepEqual(titles(cards(list.Beads)), want) {
		t.Errorf("beads %v, want %v", titles(cards(list.Beads)), want)
	}
	if got := st.reset(); !reflect.DeepEqual(got, map[string]int{"ListBeads": 1, "Sessions": 1}) {
		t.Errorf("/beads as JSON asked the store for %v, want the beads and the live sessions once", got)
	}
	f.getJSON("/beads?depot=loose", &list)
	if want := []string{"a shed"}; !reflect.DeepEqual(titles(cards(list.Beads)), want) || !reflect.DeepEqual(list.Stages, []string{"inbox"}) {
		t.Errorf("loose: beads %v (want %v), stages %v", titles(cards(list.Beads)), want, list.Stages)
	}
}

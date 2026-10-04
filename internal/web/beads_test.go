package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
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

// /flows as JSON: per rig its flows, each stage with its group, its stage
// file and the open beads in it, the newest change first. A bead of a rig
// with no flow is in the rig's elsewhere; a closed one is nowhere.
func TestFlowsJSON(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 1}))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Members: []string{"ada"}}))
	line := "line"
	_, err := f.st.AddRoute(ctx, store.Route{Stage: "new", Rig: &line, Group: "builders"})
	must(t, err)
	_, err = f.st.AddRoute(ctx, store.Route{Stage: "review", Group: "people"})
	must(t, err)
	// A store flow's stage file is in the yard's home.
	file := filepath.Join(store.HomeStageDir(f.home, "code"), "new.md")
	must(t, os.MkdirAll(filepath.Dir(file), 0o755))
	must(t, os.WriteFile(file, []byte("Build it.\n"), 0o644))
	head, err := f.st.Head(ctx)
	must(t, err)

	var got web.Flows
	f.getJSON("/flows", &got)
	if got.Head != head || len(got.Rigs) != 2 {
		t.Fatalf("head %d, %d rigs, want %d and 2: %+v", got.Head, len(got.Rigs), head, got)
	}
	rig := got.Rigs[0]
	if rig.Name != "line" || len(rig.Flows) != 2 || len(rig.Elsewhere) != 0 || rig.Error != "" {
		t.Fatalf("rig line: %+v", rig)
	}
	code := rig.Flows[0]
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
	if two := code.Stages[0].Beads[0]; !two.Held || two.Working || two.Status != "open" || two.Rig != "line" || two.Stage != "backlog" {
		t.Errorf("the held bead's card: %+v", two)
	}
	bugs := rig.Flows[1]
	if bugs.Name != "bugs" || bugs.Type != "bug" || len(bugs.Stages) != 2 || !reflect.DeepEqual(titles(bugs.Stages[0].Beads), []string{"five"}) {
		t.Errorf("flow bugs: %+v", bugs)
	}
	loose := got.Rigs[1]
	if loose.Name != "loose" || len(loose.Flows) != 0 || !reflect.DeepEqual(titles(loose.Elsewhere), []string{"six"}) {
		t.Errorf("rig loose: %+v", loose)
	}
}

// A bead that pins a flow of its own follows that one, so it stands off
// the rig's flow even in a stage the rig's flow has (as `yardr flow show`
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
	_, err = f.st.CreateBead(ctx, store.NewBead{Title: "pinned", Stage: "new", Rig: "line",
		Metadata: map[string]any{"flow": "short"}}, "me")
	must(t, err)
	var got web.Flows
	f.getJSON("/flows", &got)
	if rig := got.Rigs[0]; !reflect.DeepEqual(titles(rig.Elsewhere), []string{"pinned"}) ||
		!reflect.DeepEqual(titles(rig.Flows[0].Stages[1].Beads), []string{"three"}) {
		t.Errorf("elsewhere %v, new %v", titles(rig.Elsewhere), titles(rig.Flows[0].Stages[1].Beads))
	}
}

// A rig whose flows cannot be read still shows its beads, and the other
// rigs their flows.
func TestFlowsReadError(t *testing.T) {
	f := newFixture(t)
	f.populate()
	srv, err := web.NewServer(failingRigFlows{API: f.st, rig: "line"}, f.home, nil)
	must(t, err)
	f.ts.Config.Handler = srv
	var got web.Flows
	f.getJSON("/flows", &got)
	if rig := got.Rigs[0]; rig.Error == "" || len(rig.Flows) != 0 || len(rig.Elsewhere) != 4 {
		t.Errorf("rig line: %+v", rig)
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
		made, err = f.st.CreateBead(ctx, store.NewBead{Title: "seven", Type: "bug", Rig: "line"}, "me")
		must(t, err)
	}, 1)
	if page["bead/"+made.ID] != "at/line/bug/triage" || !strings.Contains(put(fr), "card arrived") {
		t.Errorf("the new bead: section %q, frame %s", page["bead/"+made.ID], put(fr))
	}
	// A bead of a rig with no flow stays off the flow.
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
// filters for rig and stage.
func TestBeadsJSON(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	head, err := f.st.Head(ctx)
	must(t, err)
	var got web.Beads
	f.getJSON("/beads", &got)
	// "two" changed last (its hold); the rest in the order they were made.
	if want := []string{"two", "six", "five", "three", "one"}; !reflect.DeepEqual(titles(got.Beads), want) {
		t.Errorf("beads %v, want %v", titles(got.Beads), want)
	}
	if got.Head != head || !reflect.DeepEqual(got.Rigs, []string{"line", "loose"}) ||
		!reflect.DeepEqual(got.Stages, []string{"backlog", "inbox", "new", "triage"}) {
		t.Errorf("head %d (want %d), rigs %v, stages %v", got.Head, head, got.Rigs, got.Stages)
	}
	for target, want := range map[string][]string{
		"/beads?rig=line":               {"two", "five", "three", "one"},
		"/beads?stage=backlog":          {"two", "one"},
		"/beads?rig=line&stage=new":     {"three"},
		"/beads?rig=loose&stage=new":    {},
		"/beads?rig=nowhere":            {},
		"/beads?rig=loose&stage=inbox&": {"six"},
	} {
		var got web.Beads
		f.getJSON(target, &got)
		if !reflect.DeepEqual(titles(got.Beads), want) {
			t.Errorf("%s: %v, want %v", target, titles(got.Beads), want)
		}
	}
	// The stages a rig's filter offers are the rig's.
	f.getJSON("/beads?rig=loose&stage=inbox", &got)
	if got.Rig != "loose" || got.Stage != "inbox" || !reflect.DeepEqual(got.Stages, []string{"inbox"}) {
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
	r := f.get("/beads?rig=line&stage=new")
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	three := f.byTitle()["three"]
	for _, want := range []string{
		fmt.Sprintf(`<div class="shell" hx-ext="sse" sse-connect="/events?after=%d&amp;format=seq">`, head),
		`<div id="beads" hx-get="/beads?rig=line&amp;stage=new" hx-trigger="sse:message" hx-select="#beads > *" hx-swap="innerHTML ignoreTitle:true">`,
		`<a href="/beads?stage=new">all</a>`,
		`<a href="/beads?rig=line&amp;stage=new" aria-current="true">line</a>`,
		`<a href="/beads?rig=loose&amp;stage=new">loose</a>`,
		`<a href="/beads?rig=line">all</a>`,
		`<a href="/beads?rig=line&amp;stage=backlog">backlog</a>`,
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

// whole gives the yard an epic with two children, one blocking the other,
// in a flow with outcomes and a stage file, with notes and a session, and
// returns the blocked child.
func whole(t *testing.T, f *fixture) (epic, blocker, b *store.Bead) {
	t.Helper()
	ctx := context.Background()
	must(t, f.st.PutRig(ctx, store.Rig{Name: "line", Base: "origin/main"}, "me"))
	fl, err := store.ParseFlow("new -> review on done\nnew -> decide on question\nreview -> merged\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "line", "", "code", "me"))
	file := filepath.Join(store.HomeStageDir(f.home, "code"), "new.md")
	must(t, os.MkdirAll(filepath.Dir(file), 0o755))
	must(t, os.WriteFile(file, []byte("A builder implements the bead here.\n"), 0o644))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}))
	epic, err = f.st.CreateBead(ctx, store.NewBead{Title: "the gate", Type: store.TypeEpic, Stage: "new", Rig: "line"}, "me")
	must(t, err)
	blocker, err = f.st.CreateBead(ctx, store.NewBead{Title: "the posts", Stage: "new", Rig: "line"}, "me")
	must(t, err)
	b, err = f.st.CreateBead(ctx, store.NewBead{Title: "a door", Body: "Hang it.\n\n- on two hinges", Stage: "new", Rig: "line",
		Priority: 1, Labels: []string{"wood"}}, "me")
	must(t, err)
	// Claimed first: a blocked bead is not handed to anyone.
	_, err = f.st.Claim(ctx, b.ID, "builders", "new", "dispatcher")
	must(t, err)
	must(t, f.st.AddDep(ctx, epic.ID, blocker.ID, store.DepParent, "me"))
	must(t, f.st.AddDep(ctx, epic.ID, b.ID, store.DepParent, "me"))
	must(t, f.st.AddDep(ctx, blocker.ID, b.ID, store.DepBlocks, "me"))
	must(t, f.st.AddNote(ctx, b.ID, "the hinge is missing", "builder"))
	must(t, f.st.AddNote(ctx, b.ID, "found one", "passer"))
	b, err = f.st.GetBead(ctx, b.ID)
	must(t, err)
	return epic, blocker, b
}

// A bead's page as JSON: what `yardr prime --bead` knows.
func TestBeadJSON(t *testing.T) {
	f := newFixture(t)
	epic, blocker, b := whole(t, f)
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
	if got.Base != store.Branch(epic.ID) || got.Epic == nil || got.Epic.ID != epic.ID || got.Epic.Title != "the gate" {
		t.Errorf("base %q, epic %+v", got.Base, got.Epic)
	}
	if len(got.Blockers) != 1 || got.Blockers[0].ID != blocker.ID || got.Blockers[0].Title != "the posts" {
		t.Errorf("blockers %+v", got.Blockers)
	}
	if got.StageFile == nil || got.StageFile.Text != "A builder implements the bead here.\n" ||
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
	// The epic's page has its children; a bead no flow governs says so by
	// having none.
	var ep web.BeadPage
	f.getJSON("/beads/"+epic.ID, &ep)
	if ids := []string{ep.Children[0].ID, ep.Children[1].ID}; len(ep.Children) != 2 || !reflect.DeepEqual(ids, []string{blocker.ID, b.ID}) || ep.Epic != nil {
		t.Errorf("the epic's children %+v", ep.Children)
	}
}

// The page, for a person; an unknown bead is a 404 in both forms.
func TestBeadHTML(t *testing.T) {
	f := newFixture(t)
	epic, blocker, b := whole(t, f)
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
		`<dt>base</dt><dd>yard/` + epic.ID + `</dd>`,
		`<dt>epic</dt><dd><a class="bead" href="/beads/` + epic.ID + `">` + epic.ID + `</a> the gate</dd>`,
		`<dt>labels</dt><dd>wood</dd>`,
		`<a class="bead" href="/beads/` + blocker.ID + `">`,
		"<pre class=\"text\">Hang it.\n\n- on two hinges</pre>",
		"<pre class=\"text\">A builder implements the bead here.\n</pre>",
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

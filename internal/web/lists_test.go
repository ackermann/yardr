package web_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

// printed is v as a command prints it with --json, read back: what a client
// of either has.
func printed(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	must(t, err)
	var out any
	must(t, json.Unmarshal(raw, &out))
	return out
}

// listOf reads a route's answer: the list under key and the head.
func (f *fixture) listOf(target, key string, headers ...string) (list any, head uint64) {
	f.t.Helper()
	r := f.get(target, headers...)
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 200 {
		f.t.Fatalf("%s: status %d, %v: %s", target, r.status, err, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		f.t.Errorf("%s: Content-Type %q", target, ct)
	}
	if got[key] == nil || got["head"] == nil {
		f.t.Fatalf("%s: no %q or no head: %s", target, key, r.body)
	}
	must(f.t, json.Unmarshal(got[key], &list))
	must(f.t, json.Unmarshal(got["head"], &head))
	return list, head
}

// The lists answer what their list commands print with --json, under the
// route's name, with the yard's head, and an empty list for a yard that
// has none. Each has a page, so only a client that asks for JSON gets it.
func TestLists(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	routes := map[string]func() any{
		"depots": func() any { v, err := f.st.Depots(ctx); must(t, err); return v },
		"groups": func() any { v, err := f.st.Groups(ctx); must(t, err); return v },
		"routes": func() any { v, err := f.st.Routes(ctx); must(t, err); store.SortRoutes(v); return v },
		"peers":  func() any { v, err := f.st.Peers(ctx); must(t, err); return v },
		"deps": func() any {
			v, err := f.st.AllDeps(ctx)
			must(t, err)
			byID := map[string]store.Bead{}
			for _, b := range f.byTitle() {
				byID[b.ID] = b
			}
			rows := make([]web.DepRow, 0, len(v))
			for _, d := range v {
				end := func(id string) web.DepEnd {
					b := byID[id]
					return web.DepEnd{Title: b.Title, Stage: b.Stage, Status: b.Status}
				}
				rows = append(rows, web.DepRow{Dep: d, FromBead: end(d.From), ToBead: end(d.To)})
			}
			return rows
		},
	}
	paged := map[string]bool{"depots": true, "groups": true, "routes": true, "peers": true, "deps": true}
	for key := range routes {
		if list, _ := f.listOf("/"+key, key, "Accept", "application/json"); !reflect.DeepEqual(list, []any{}) {
			t.Errorf("/%s of an empty yard: %v, want an empty list", key, list)
		}
		if paged[key] {
			if r := f.get("/" + key); r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
				t.Errorf("/%s of an empty yard: status %d, type %q, want the page", key, r.status, r.header.Get("Content-Type"))
			}
		}
	}

	f.populate()
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 3, Config: map[string]any{"kind": "claude"}}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"jo"}}, "me"))
	line, bug := "line", "bug"
	for _, r := range []store.Route{
		{Stage: "new", Group: "builders"},
		{Stage: "new", Depot: &line, Type: &bug, Group: "decisions"},
	} {
		_, err := f.st.AddRoute(ctx, r, "me")
		must(t, err)
	}
	_, key, err := ed25519.GenerateKey(nil)
	must(t, err)
	_, err = f.st.AddPeer(ctx, store.Peer{Name: "north", Key: store.Public(key), URL: "http://127.0.0.1:1"}, "me")
	must(t, err)
	beads := f.byTitle()
	must(t, f.st.AddDep(ctx, beads["one"].ID, beads["three"].ID, store.DepBlocks, "", "", "me"))
	must(t, f.st.AddDep(ctx, beads["three"].ID, beads["five"].ID, store.DepDiscoveredFrom, "", "", "me"))

	head, err := f.st.Head(ctx)
	must(t, err)
	for key, read := range routes {
		// No Accept: a list with no page answers its JSON instead.
		jsonOnly := [][]string{nil, {"Accept", "application/json"}, {"Accept", "text/html"}}
		if paged[key] {
			jsonOnly = [][]string{{"Accept", "application/json"}}
		}
		for _, headers := range jsonOnly {
			list, got := f.listOf("/"+key, key, headers...)
			if want := printed(t, read()); !reflect.DeepEqual(list, want) || len(list.([]any)) == 0 {
				t.Errorf("/%s %v\n got %v\nwant %v", key, headers, list, want)
			}
			if got != head {
				t.Errorf("/%s: head %d, want %d", key, got, head)
			}
		}
		if paged[key] {
			// A browser, plain curl and */* get the page.
			for _, headers := range [][]string{nil, {"Accept", "text/html"}, {"Accept", "*/*"}} {
				r := f.get("/"+key, headers...)
				if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
					t.Errorf("/%s %v: status %d, type %q, want the page", key, headers, r.status, r.header.Get("Content-Type"))
				}
			}
		}
	}

	// What a second front end picks from them is there by name.
	var groups web.Groups
	f.getJSON("/groups", &groups)
	if g := groups.Groups[0]; g.Name != "builders" || g.Runner != "herdr" || g.Limit != 3 || g.Config["kind"] != "claude" ||
		!slices.Equal(groups.Groups[1].Members, []string{"jo"}) {
		t.Errorf("groups: %+v", groups.Groups)
	}
	// The routes in match order: the one that asks more first.
	var rs web.Routes
	f.getJSON("/routes", &rs)
	if len(rs.Routes) != 2 || rs.Routes[0].Group != "decisions" || *rs.Routes[0].Type != "bug" {
		t.Errorf("routes: %+v", rs.Routes)
	}
	var depots web.Depots
	f.getJSON("/depots", &depots)
	if len(depots.Depots) != 2 || depots.Depots[0].Name != "line" || depots.Depots[0].Base != "origin/main" {
		t.Errorf("depots: %+v", depots.Depots)
	}
	// Every edge once, though each is in the list of both its beads.
	var deps web.Deps
	f.getJSON("/deps", &deps)
	var edges []string
	for _, d := range deps.Deps {
		edges = append(edges, d.From+" "+d.Kind+" "+d.To)
	}
	want := []string{beads["one"].ID + " blocks " + beads["three"].ID, beads["three"].ID + " discovered-from " + beads["five"].ID}
	if !slices.Equal(edges, want) {
		t.Errorf("deps: %v, want %v", edges, want)
	}
	// A list is read only, as every page.
	if r := f.do("POST", "/groups"); r.status != 405 {
		t.Errorf("POST /groups: status %d", r.status)
	}
}

// The depot, group and route pages render the rows their list commands
// print: `yardr depot list`, `yardr group list` and `yardr route list`.
// The routes stand in match order, as the command prints them.
func TestConfigPages(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "line", Path: "/tmp/line", Base: "origin/main", Test: "make test"}, "me"))
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "docs", Path: "/tmp/docs", Kind: store.DepotDir}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 3,
		Config: map[string]any{"kind": "claude", "model": "opus"}}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"jo", "mo"}}, "me"))
	line, bug := "line", "bug"
	plain, err := f.st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	narrow, err := f.st.AddRoute(ctx, store.Route{Stage: "new", Depot: &line, Type: &bug, Label: &bug, Group: "decisions"}, "me")
	must(t, err)

	depots := squash(f.get("/depots").body)
	for _, want := range []string{
		`<h2>line</h2>`,
		`<div><dt>kind</dt><dd>git</dd></div>`,
		`<div><dt>path</dt><dd>/tmp/line</dd></div>`,
		`<div><dt>base</dt><dd>origin/main</dd></div>`,
		`<div><dt>test</dt><dd>make test</dd></div>`,
		`<h2>docs</h2>`,
		`<div><dt>kind</dt><dd>dir</dd></div>`,
		`<a href="/depots" aria-current="page">Depots</a>`,
	} {
		if !strings.Contains(depots, want) {
			t.Errorf("/depots lacks %s", want)
		}
	}
	// A directory depot has no base: no empty row for it.
	at := strings.Index(depots, "<h2>docs</h2>")
	article := depots[at : at+strings.Index(depots[at:], "</article>")]
	if strings.Contains(article, "<dt>base</dt>") {
		t.Error("/depots shows a base for the directory depot")
	}

	groups := squash(f.get("/groups").body)
	for _, want := range []string{
		`<h2>builders</h2>`,
		`<div><dt>runner</dt><dd>herdr</dd></div>`,
		`<div><dt>limit</dt><dd>3</dd></div>`,
		`<h3 class="label">config</h3>`,
		`<div><dt>kind</dt><dd>claude</dd></div>`,
		`<div><dt>model</dt><dd>opus</dd></div>`,
		`<h2>decisions</h2>`,
		`<div><dt>members</dt><dd>jo, mo</dd></div>`,
		`<a href="/groups" aria-current="page">Groups</a>`,
	} {
		if !strings.Contains(groups, want) {
			t.Errorf("/groups lacks %s", want)
		}
	}
	// The bare key beside its value: `config <key>` wrapped at 500 px.
	// The group without a config has no heading for one.
	for _, want := range []string{`<dt>config kind</dt>`, `<dt>config model</dt>`} {
		if strings.Contains(groups, want) {
			t.Errorf("/groups still labels %s", want)
		}
	}
	if n := strings.Count(groups, `<h3 class="label">config</h3>`); n != 1 {
		t.Errorf("/groups has %d config headings, want one (for builders)", n)
	}

	routes := squash(f.get("/routes").body)
	for _, want := range []string{
		// The wide table: one row per route, as the command prints them.
		`<th>stage</th><th>type</th><th>depot</th><th>label</th><th>beads</th><th>group</th><th>id</th>`,
		`<tr><td>new</td><td>bug</td><td>line</td><td>bug</td><td>-</td><td>decisions</td><td class="faint">` + narrow.ID + `</td></tr>`,
		`<tr><td>new</td><td>-</td><td>-</td><td>-</td><td>-</td><td>builders</td><td class="faint">` + plain.ID + `</td></tr>`,
		// The stacked cards below 700 px, the id quiet until opened.
		`<h2>new → decisions</h2>`,
		`<div><dt>type</dt><dd>bug</dd></div>`,
		`<div><dt>depot</dt><dd>line</dd></div>`,
		`<div><dt>label</dt><dd>bug</dd></div>`,
		`<details class="route-id"><summary>id</summary>` + narrow.ID + `</details>`,
		`<h2>new → builders</h2>`,
		`<details class="route-id"><summary>id</summary>` + plain.ID + `</details>`,
		`<a href="/routes" aria-current="page">Routes</a>`,
	} {
		if !strings.Contains(routes, want) {
			t.Errorf("/routes lacks %s", want)
		}
	}
	// Routes carry no priority, so neither view shows one.
	if strings.Contains(routes, "<dt>priority</dt>") {
		t.Errorf("/routes still names a priority")
	}
	// One table row per route, besides the header's.
	if n := strings.Count(routes, "<tr><td>"); n != 2 {
		t.Errorf("/routes has %d table rows, want one per route", n)
	}
	// The id takes no row of its own on a card.
	if strings.Contains(routes, "<div><dt>id</dt>") {
		t.Error("/routes still shows the id as a row of the stacked card")
	}
	// In match order: the higher priority first.
	if strings.Index(routes, narrow.ID) > strings.Index(routes, plain.ID) {
		t.Error("/routes does not stand in match order")
	}

	// An empty yard says so on each page.
	empty := newFixture(t)
	for path, want := range map[string]string{
		"/depots": "This yard has no depots.",
		"/groups": "This yard has no groups.",
		"/routes": "This yard has no routes.",
	} {
		if body := empty.get(path).body; !strings.Contains(body, want) {
			t.Errorf("%s of an empty yard lacks %q", path, want)
		}
	}
}

// The peer and edge pages render the rows their list commands print:
// `yardr peer list` and every bead's `yardr dep list`, each edge once, a
// staged edge with the stages it names.
func TestConfigPagesPeersDeps(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	_, key, err := ed25519.GenerateKey(nil)
	must(t, err)
	_, err = f.st.AddPeer(ctx, store.Peer{Name: "north", Key: store.Public(key), URL: "http://127.0.0.1:1"}, "me")
	must(t, err)
	_, key, err = ed25519.GenerateKey(nil)
	must(t, err)
	_, err = f.st.AddPeer(ctx, store.Peer{Name: "laptop", Key: store.Public(key)}, "me")
	must(t, err)
	_, err = f.st.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{"ping", "mail"}, Receive: []string{"ping"}}, "me")
	must(t, err)

	peers := squash(f.get("/peers").body)
	for _, want := range []string{
		`<h2>north</h2>`,
		`<div><dt>url</dt><dd>http://127.0.0.1:1</dd></div>`,
		`<div><dt>send</dt><dd>mail, ping</dd></div>`,
		`<div><dt>receive</dt><dd>ping</dd></div>`,
		`<div><dt>waiting</dt><dd>0</dd></div>`,
		`<h2>laptop</h2>`,
		`<div><dt>url</dt><dd>none — it calls this yard</dd></div>`,
		`<a href="/peers" aria-current="page">Peers</a>`,
	} {
		if !strings.Contains(peers, want) {
			t.Errorf("/peers lacks %s", want)
		}
	}

	beads := f.byTitle()
	one, three, five := beads["one"].ID, beads["three"].ID, beads["five"].ID
	must(t, f.st.AddDep(ctx, one, three, store.DepBlocks, "", "", "me"))
	must(t, f.st.AddDep(ctx, one, three, store.DepBlocks, "review", "", "me"))
	must(t, f.st.AddDep(ctx, three, five, store.DepDiscoveredFrom, "", "", "me"))
	deps := squash(f.get("/deps").body)
	for _, want := range []string{
		// The toggle: the open ends by default, every edge behind all.
		`<a href="/deps" aria-current="true">open</a>`,
		`<a href="/deps?all=1">all</a>`,
		// Each blocked bead's card, with the edges that hold it under it:
		// the bead, the kind, its title cut short, its stage. A staged
		// edge names the stages it names.
		`<a class="bead" href="/beads/` + three + `">` + three + `</a><span class="label">line · new · task</span>`,
		`<p class="card-title">three</p>`,
		`<a class="bead" href="/beads/` + one + `">` + one + `</a><span class="faint">blocks</span><span class="edge-title">one</span><span class="label">backlog</span></li>`,
		`<a class="bead" href="/beads/` + one + `">` + one + `</a><span class="faint">blocks</span><span class="faint">at review</span><span class="edge-title">one</span><span class="label">backlog</span></li>`,
		`<a class="bead" href="/beads/` + five + `">` + five + `</a><span class="label">line · triage · bug</span>`,
		`<a class="bead" href="/beads/` + three + `">` + three + `</a><span class="faint">discovered-from</span><span class="edge-title">three</span><span class="label">new</span></li>`,
		`<a href="/deps" aria-current="page">Deps</a>`,
	} {
		if !strings.Contains(deps, want) {
			t.Errorf("/deps lacks %s", want)
		}
	}
	// Grouped by the blocked bead, the longest waiting first: three's
	// oldest edge came before five's.
	if strings.Index(deps, `<p class="card-title">three</p>`) > strings.Index(deps, `<p class="card-title">five</p>`) {
		t.Error("/deps does not group by the blocked bead, longest waiting first")
	}

	// An empty yard says so on each page.
	empty := newFixture(t)
	for path, want := range map[string]string{
		"/peers": "This yard has no peers.",
		"/deps":  "This yard has no edges.",
	} {
		if body := empty.get(path).body; !strings.Contains(body, want) {
			t.Errorf("%s of an empty yard lacks %q", path, want)
		}
	}
	// With no edges at all, no toggle to all of them.
	if body := empty.get("/deps").body; strings.Contains(body, `aria-label="Edges"`) {
		t.Error("/deps of an empty yard shows the open/all toggle with nothing behind it")
	}
}

// By default /deps shows only the edges with an open end: an edge between
// two closed beads waits on nothing, and sits behind the link to all. An
// edge with one open end shows, whichever end is open, the closed end
// marked.
func TestDepsOpenEnds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	all, err := f.st.ListBeads(ctx, store.BeadFilter{All: true})
	must(t, err)
	ids := map[string]string{}
	for _, b := range all {
		ids[b.Title] = b.ID
	}
	one, three, four, five := ids["one"], ids["three"], ids["four"], ids["five"]
	must(t, f.st.AddDep(ctx, one, three, store.DepBlocks, "", "", "me"))
	must(t, f.st.AddDep(ctx, three, five, store.DepDiscoveredFrom, "", "", "me"))
	_, err = f.st.CloseBead(ctx, five, "dropped", store.Expect{}, "me")
	must(t, err)
	must(t, f.st.AddDep(ctx, four, five, store.DepBlocks, "", "", "me"))

	// The open-open and open-closed edges, grouped by the blocked bead;
	// the closed-closed edge is nowhere on the page.
	deps := squash(f.get("/deps").body)
	for _, want := range []string{
		`<p class="card-title">three</p>`,
		`<a class="bead" href="/beads/` + one + `">` + one + `</a><span class="faint">blocks</span><span class="edge-title">one</span><span class="label">backlog</span></li>`,
		`<p class="card-title">five</p>`,
		`<a class="bead" href="/beads/` + three + `">` + three + `</a><span class="faint">discovered-from</span><span class="edge-title">three</span><span class="label">new</span></li>`,
		`<div id="deps" hx-get="/deps" hx-trigger="sse:message" hx-select="#deps > *" hx-swap="innerHTML ignoreTitle:true">`,
		`<a href="/deps?all=1">all</a>`,
	} {
		if !strings.Contains(deps, want) {
			t.Errorf("/deps lacks %s", want)
		}
	}
	// Five is closed, so its card says so; four's edge is behind the link.
	if !strings.Contains(deps, `<span class="label">line · triage · bug · closed</span>`) {
		t.Error("/deps does not mark the closed blocked bead closed")
	}
	start := strings.Index(deps, `<div id="deps"`)
	if start < 0 {
		t.Fatal("/deps has no list to hide the closed edge from")
	}
	centre := deps[start:]
	if end := strings.Index(centre, "<aside"); end >= 0 {
		centre = centre[:end]
	}
	if strings.Contains(centre, four) {
		t.Error("/deps shows the edge between two closed beads")
	}

	var got web.Deps
	f.getJSON("/deps", &got)
	if got.All || got.Total != 3 || len(got.Deps) != 2 || len(got.Groups) != 2 {
		t.Fatalf("/deps: all %v, total %d, %d edges in %d groups, want false, 3, 2, 2",
			got.All, got.Total, len(got.Deps), len(got.Groups))
	}
	if got.Groups[0].Bead.ID != three || len(got.Groups[0].Deps) != 1 ||
		got.Groups[1].Bead.ID != five || len(got.Groups[1].Deps) != 1 {
		t.Errorf("/deps groups: %+v", got.Groups)
	}
	if d := got.Deps[1]; d.From != three || d.To != five ||
		d.FromBead.Title != "three" || d.FromBead.Stage != "new" ||
		d.ToBead.Title != "five" || d.ToBead.Stage != "triage" || d.ToBead.Status != "closed" {
		t.Errorf("/deps carries %+v", d)
	}

	// Behind the link, every edge, the closed ends marked, and the way back.
	every := squash(f.get("/deps?all=1").body)
	for _, want := range []string{
		`<a class="bead" href="/beads/` + four + `">` + four + `</a><span class="faint">blocks</span><span class="edge-title">four</span><span class="label">new · closed</span></li>`,
		`<div id="deps" hx-get="/deps?all=1" hx-trigger="sse:message" hx-select="#deps > *" hx-swap="innerHTML ignoreTitle:true">`,
		`<a href="/deps">open</a>`,
		`<a href="/deps?all=1" aria-current="true">all</a>`,
	} {
		if !strings.Contains(every, want) {
			t.Errorf("/deps?all=1 lacks %s", want)
		}
	}
	f.getJSON("/deps?all=1", &got)
	if !got.All || got.Total != 3 || len(got.Deps) != 3 || len(got.Groups) != 2 {
		t.Fatalf("/deps?all=1: all %v, total %d, %d edges in %d groups, want true, 3, 3, 2",
			got.All, got.Total, len(got.Deps), len(got.Groups))
	}
	if len(got.Groups[1].Deps) != 2 || got.Groups[1].Deps[1].From != four {
		t.Errorf("/deps?all=1 groups: %+v", got.Groups)
	}

	// A parent edge points from the train to its child, but the train is
	// what waits for the child's work. Group it under the train and show
	// the child as the other end.
	train, err := f.st.CreateBead(ctx, store.NewBead{Title: "train", Type: store.TypeTrain, Depot: "line"}, "me")
	must(t, err)
	must(t, f.st.AddDep(ctx, train.ID, one, store.DepParent, "", "", "me"))
	parent := squash(f.get("/deps").body)
	if !strings.Contains(parent, `<p class="card-title">train</p>`) ||
		!strings.Contains(parent, `<a class="bead" href="/beads/`+one+`">`+one+`</a><span class="faint">parent</span><span class="edge-title">one</span><span class="label">backlog</span></li>`) {
		t.Error("/deps does not group a parent edge under its waiting train with the child as its other end")
	}
	if strings.Contains(parent, `<p class="card-title">one</p>`) {
		t.Error("/deps groups the parent edge under its child instead of its waiting train")
	}

	// Only closed-only edges: the default says so, with the link to all.
	closed := newFixture(t)
	closed.populate()
	cbs, err := closed.st.ListBeads(ctx, store.BeadFilter{All: true})
	must(t, err)
	cids := map[string]string{}
	for _, b := range cbs {
		cids[b.Title] = b.ID
	}
	_, err = closed.st.CloseBead(ctx, cids["three"], "dropped", store.Expect{}, "me")
	must(t, err)
	must(t, closed.st.AddDep(ctx, cids["four"], cids["three"], store.DepBlocks, "", "", "me"))
	none := squash(closed.get("/deps").body)
	for _, want := range []string{
		`No edges with an open end.`,
		`<a href="/deps?all=1">all</a>`,
	} {
		if !strings.Contains(none, want) {
			t.Errorf("/deps without an open end lacks %s", want)
		}
	}
	if !strings.Contains(squash(closed.get("/deps?all=1").body), `<p class="card-title">three</p>`) {
		t.Error("/deps?all=1 without an open end shows no group")
	}
}

// What someone wrote for a group's config is shown as text, never as
// markup; JSON carries it as it is.
func TestConfigEscapes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	const evil = `<script>alert(1)</script>`
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "prompt": "read " + evil}}, "me"))
	page := f.get("/groups").body
	if strings.Contains(page, "<script>alert") {
		t.Error("/groups carries what someone wrote as markup")
	}
	if !strings.Contains(page, `read &lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Error("/groups does not show the config as text")
	}
	var got web.Groups
	f.getJSON("/groups", &got)
	if got.Groups[0].Config["prompt"] != "read "+evil {
		t.Errorf("JSON changed what was written: %+v", got.Groups[0].Config)
	}
}

// The hub links to the six configuration pages, and each marks its own
// Settings column row current and links back. The hub itself is static
// links with no live part; the column keeps the operator and JWT filters.
func TestConfigHub(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	hub := squash(f.get("/config").body)
	for _, want := range []string{
		`<a href="/depots">Depots</a>`,
		`<a href="/groups">Groups</a>`,
		`<a href="/routes">Routes</a>`,
		`<a href="/peers">Peers</a>`,
		`<a href="/deps">Deps</a>`,
		`<a href="/limits">Limits</a>`,
		`the repositories and directories beads are worked in`,
		`first match wins`,
		`the yards this one is linked with`,
		`what the subscriptions the yard burns still carry`,
		`<a href="/config" aria-current="page">Config</a>`,
	} {
		if !strings.Contains(hub, want) {
			t.Errorf("/config lacks %s", want)
		}
	}
	if strings.Contains(hub, `hx-get="/config"`) {
		t.Error("/config reads itself again: its links are static")
	}
	var got web.Config
	f.getJSON("/config", &got)
	if len(got.Links) != 6 || got.Links[0].Path != "/depots" || got.Links[2].Path != "/routes" ||
		got.Links[3].Path != "/peers" || got.Links[4].Path != "/deps" || got.Links[5].Path != "/limits" || got.Head == 0 {
		t.Errorf("the hub as JSON: %+v", got)
	}
	for path, label := range map[string]string{
		"/depots": "Depots", "/groups": "Groups", "/routes": "Routes",
		"/peers": "Peers", "/deps": "Deps", "/limits": "Limits",
	} {
		body := squash(f.get(path).body)
		for _, want := range []string{
			`<a href="` + path + `" aria-current="page">` + label + `</a>`,
			`<a href="/config">‹ Config</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %s", path, want)
			}
		}
	}
	t.Run("column visibility follows its filters", func(t *testing.T) {
		ctx := context.Background()
		member, _ := talkFixture(t, "sam")
		must(t, func() error { _, err := member.st.AddUser(ctx, "jo", "jo"); return err }())
		must(t, member.st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
		must(t, func() error { _, err := member.st.AddUser(ctx, "sam", "jo"); return err }())
		body := member.get("/config").body
		for _, want := range []string{
			`<li><a href="/config" aria-current="page">Config</a></li>`,
			`<li><a href="/depots">Depots</a></li>`,
			`<li><a href="/groups">Groups</a></li>`,
			`<li><a href="/routes">Routes</a></li>`,
			`<li><a href="/peers">Peers</a></li>`,
			`<li><a href="/deps">Deps</a></li>`,
			`<li><a href="/limits">Limits</a></li>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("a member's column lacks %s: %s", want, body)
			}
		}
		for _, foreign := range []string{`>Telemetry<`, `>Pair device<`} {
			if strings.Contains(body, foreign) {
				t.Errorf("a member's column holds %s: %s", foreign, body)
			}
		}
		// The operator sees Telemetry, and an unsigned view still does
		// not see Pair device.
		op, _ := talkFixture(t, "jo")
		must(t, func() error { _, err := op.st.AddUser(ctx, "jo", "jo"); return err }())
		must(t, op.st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
		body = op.get("/config").body
		if !strings.Contains(body, `<li><a href="/telemetry">Telemetry</a></li>`) {
			t.Errorf("the operator's column holds no Telemetry: %s", body)
		}
		if strings.Contains(body, `>Pair device<`) {
			t.Errorf("an unsigned view's column holds Pair device: %s", body)
		}
		// A JWT view sees Pair device.
		keys := mustJWTTestKeys(t)
		jwks := newJWKStub(t, jwksDoc(t, rsaJWK("a", &keys.a.PublicKey)))
		signed := jwtFixture(t, jwks, "", map[string]string{"jo@example.net": "jo"})
		must(t, func() error { _, err := signed.st.AddUser(ctx, "jo", "jo"); return err }())
		token := signRSA(t, keys.a, "a", jwtClaims("jo@example.net"))
		body = signed.get("/config", web.DefaultJWTHeader, token).body
		if !strings.Contains(body, `<li><a href="/pairing">Pair device</a></li>`) {
			t.Errorf("a JWT view's column holds no Pair device: %s", body)
		}
	})
}

// /beads has every field `yardr bead list --json` prints of a bead, and
// with ?all=1 the closed beads too, as bead list --all.
func TestBeadsAll(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	one := f.byTitle()["one"]
	_, err := f.st.UpdateBead(ctx, one.ID, store.BeadPatch{AddLabels: []string{"door"}, Priority: ptr(4)}, store.Expect{}, "me")
	must(t, err)

	for target, all := range map[string]bool{"/beads": false, "/beads?all=1": true, "/beads?all=0": false} {
		listed, err := f.st.ListBeads(ctx, store.BeadFilter{All: all})
		must(t, err)
		var got struct {
			All   bool             `json:"all"`
			Beads []map[string]any `json:"beads"`
		}
		f.getJSON(target, &got)
		if got.All != all || len(got.Beads) != len(listed) {
			t.Fatalf("%s: all %v, %d beads, want %v and %d", target, got.All, len(got.Beads), all, len(listed))
		}
		closed := 0
		for _, b := range listed {
			i := slices.IndexFunc(got.Beads, func(row map[string]any) bool { return row["id"] == b.ID })
			if i < 0 {
				t.Fatalf("%s lacks %s (%s)", target, b.ID, b.Title)
			}
			row := got.Beads[i]
			if b.Status == "closed" {
				closed++
			}
			// The card's own two fields beside the bead's.
			if row["working"] != false || (row["held"] == true) != b.Hold {
				t.Errorf("%s: %s working %v, held %v", target, b.Title, row["working"], row["held"])
			}
			delete(row, "working")
			delete(row, "held")
			if want := printed(t, b); !reflect.DeepEqual(any(row), want) {
				t.Errorf("%s: %s\n got %v\nwant %v", target, b.Title, row, want)
			}
		}
		if (closed == 1) != all {
			t.Errorf("%s: %d closed beads", target, closed)
		}
	}

	// What bead list prints and the cards did not have.
	var got web.Beads
	f.getJSON("/beads?all=1&stage=backlog", &got)
	i := slices.IndexFunc(got.Beads, func(r web.BeadRow) bool { return r.ID == one.ID })
	if i < 0 || !slices.Equal(got.Beads[i].Labels, []string{"door"}) || got.Beads[i].Priority != 4 || got.Beads[i].CreatedAt.IsZero() {
		t.Errorf("one: %+v", got.Beads)
	}
	if len(got.Beads) != 2 || got.Beads[1].Title != "two" || !got.Beads[1].Hold || !got.Beads[1].Held {
		t.Errorf("the held bead: %+v", got.Beads)
	}
	// The newest change first, the closed bead among them; a filter keeps all.
	f.getJSON("/beads?all=1&depot=line", &got)
	if want := []string{"one", "four", "two", "five", "three"}; !reflect.DeepEqual(titles(cards(got.Beads)), want) {
		t.Errorf("all of line: %v, want %v", titles(cards(got.Beads)), want)
	}
	text := squash(f.get("/beads?all=1&depot=line").body)
	for _, part := range []string{
		`hx-get="/beads?all=1&amp;depot=line"`,
		`<a href="/beads?all=1&amp;depot=line&amp;stage=new">new</a>`,
		` · closed</span></p><p class="card-title">four</p>`,
	} {
		if !strings.Contains(text, part) {
			t.Errorf("the page of all beads lacks %q", part)
		}
	}
	if text := f.get("/beads").body; strings.Contains(text, "four") || strings.Contains(text, "all=1") {
		t.Error("the page of the open beads shows the closed one, or links to all")
	}
}

func ptr[T any](v T) *T { return &v }

// /sessions has the sessions as `yardr session list --json` prints them
// under list, with the wait for a usage limit; with ?all=1 the
// ended ones too, with how they ended.
func TestSessionsList(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	b, sess := blockedYard(t, f)
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	must(t, f.st.SetHandle(ctx, sess.ID, map[string]any{"agent": "door-new",
		"harness_reset_at": until.Format(time.RFC3339Nano), "harness_reset_window": "5h", "harness_reset_model": "opus"}))
	gone, err := f.st.CreateBead(ctx, store.NewBead{Title: "ended", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	ended, err := f.st.Claim(ctx, gone.ID, "builders", "new", "yard")
	must(t, err)
	must(t, f.st.EndSession(ctx, ended.ID, "died", "the pane closed"))

	for target, all := range map[string]bool{"/sessions": false, "/sessions?all=1": true} {
		ss, err := f.st.ListSessions(ctx, store.SessionFilter{All: all})
		must(t, err)
		list, _ := f.listOf(target, "list", "Accept", "application/json")
		if want := printed(t, dispatch.Listed(ss)); !reflect.DeepEqual(list, want) {
			t.Errorf("%s\n got %v\nwant %v", target, list, want)
		}
		var got web.Sessions
		f.getJSON(target, &got)
		// The page's own rows are the live ones either way.
		if len(got.Sessions) != 1 || got.Sessions[0].ID != sess.ID || got.Sessions[0].Runner != "fake door-new" {
			t.Errorf("%s: sessions %+v", target, got.Sessions)
		}
		if want := map[bool]int{false: 1, true: 2}[all]; len(got.List) != want {
			t.Fatalf("%s: %d listed, want %d", target, len(got.List), want)
		}
		for _, s := range got.List {
			switch s.ID {
			case sess.ID:
				if s.Bead != b.ID || s.Group != "builders" || s.State != "running" || s.Runner != "fake" || s.EndedAt != nil ||
					s.ResetWait == nil || !s.ResetWait.Until.Equal(until) || s.ResetWait.Model != "opus" {
					t.Errorf("%s: the running session: %+v (wait %+v)", target, s, s.ResetWait)
				}
			case ended.ID:
				if s.Bead != gone.ID || s.State != "died" || s.EndedAt == nil || s.StartedAt.IsZero() {
					t.Errorf("%s: the ended session: %+v", target, s)
				}
			default:
				t.Errorf("%s lists %+v", target, s)
			}
		}
	}
}

// /crew says what a crew member's session is doing, as crew list --json.
func TestCrewStatus(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Enabled: true, Config: map[string]any{"kind": "codex", "model": "o3"}}, "me")
	must(t, err)
	must(t, f.st.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster"}))
	must(t, f.st.CrewStatus(ctx, "yardmaster", "idle"))
	var got web.Crew
	f.getJSON("/crew", &got)
	if len(got.Crew) != 1 || got.Crew[0].Status != "idle" || got.Crew[0].Kind != "codex" || got.Crew[0].Model != "o3" ||
		got.Crew[0].Agent != "herdr · codex · o3" {
		t.Errorf("crew: %+v", got.Crew)
	}
	rows, err := f.st.Crew(ctx)
	must(t, err)
	if len(got.Crew) == 1 {
		pageFields := map[string]bool{"running": true, "since": true, "up": true, "agent": true, "provider": true, "unread": true, "restart": true}
		var want, actual map[string]any
		want = printed(t, rows[0]).(map[string]any)
		actual = printed(t, got.Crew[0]).(map[string]any)
		for key := range pageFields {
			delete(actual, key)
		}
		if !reflect.DeepEqual(actual, want) {
			t.Errorf("/crew command fields\n got %v\nwant %v", actual, want)
		}
	}
}

// /groups and /crew carry each row's resolved provider: the provider
// setting where set, even where no source answers for it, else the kind's
// mapping through the source that lists it; none where neither answers, for
// a group or crew member with no kind, or for another runner. Resolving
// runs no command.
func TestRowsCarryProvider(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	asked := limitsSource(t, f, limitsAnswer())
	for _, g := range []store.Group{
		{Name: "mapped", Runner: "herdr", Config: map[string]any{"kind": "kimi"}},
		{Name: "override", Runner: "herdr", Config: map[string]any{"kind": "kimi", "provider": "zai"}},
		{Name: "override-answered", Runner: "herdr", Config: map[string]any{"kind": "kimi", "provider": "claude"}},
		{Name: "unmapped", Runner: "herdr", Config: map[string]any{"kind": "codex"}},
		{Name: "nokind", Runner: "herdr"},
		{Name: "decisions", Runner: "manual", Members: []string{"jo"}},
		{Name: "built", Runner: "exec", Config: map[string]any{"cmd": "make"}},
	} {
		must(t, f.st.PutGroup(ctx, g, "me"))
	}
	for _, m := range []store.CrewMember{
		{Name: "mapped", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "override", Runner: "herdr", Config: map[string]any{"kind": "kimi", "provider": "zai"}},
		{Name: "unmapped", Runner: "herdr", Config: map[string]any{"kind": "codex"}},
		{Name: "hand", Runner: "manual"},
	} {
		_, err := f.st.PutCrew(ctx, m, "me")
		must(t, err)
	}

	var groups web.Groups
	f.getJSON("/groups", &groups)
	got := map[string]string{}
	for _, g := range groups.Groups {
		got[g.Name] = g.Provider
	}
	want := map[string]string{
		"mapped": "moonshot", "override": "zai", "override-answered": "claude",
		"unmapped": "", "nokind": "", "decisions": "", "built": "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("/groups providers\n got %v\nwant %v", got, want)
	}
	// The setting stays inside config too, as the command prints it.
	i := slices.IndexFunc(groups.Groups, func(g web.GroupRow) bool { return g.Name == "override" })
	if i < 0 || groups.Groups[i].Config["provider"] != "zai" || groups.Groups[i].Config["kind"] != "kimi" {
		t.Errorf("the override row lost the stored config: %+v", groups.Groups)
	}
	// Absent means absent: no provider key on the row.
	var raw struct {
		Groups []map[string]any `json:"groups"`
	}
	f.getJSON("/groups", &raw)
	for _, g := range raw.Groups {
		_, has := g["provider"]
		if want[g["name"].(string)] == "" && has {
			t.Errorf("%s carries an empty provider: %v", g["name"], g)
		}
		if want[g["name"].(string)] != "" && g["provider"] != want[g["name"].(string)] {
			t.Errorf("%s carries %v, want %s", g["name"], g["provider"], want[g["name"].(string)])
		}
	}

	var crew web.Crew
	f.getJSON("/crew", &crew)
	got = map[string]string{}
	for _, m := range crew.Crew {
		got[m.Name] = m.Provider
	}
	want = map[string]string{"mapped": "claude", "override": "zai", "unmapped": "", "hand": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("/crew providers\n got %v\nwant %v", got, want)
	}
	var rawCrew struct {
		Crew []map[string]any `json:"crew"`
	}
	f.getJSON("/crew", &rawCrew)
	for _, m := range rawCrew.Crew {
		provider, has := m["provider"]
		if want[m["name"].(string)] == "" && has {
			t.Errorf("crew member %s carries an empty provider: %v", m["name"], m)
		}
		if want[m["name"].(string)] != "" && provider != want[m["name"].(string)] {
			t.Errorf("crew member %s carries %v, want %s", m["name"], provider, want[m["name"].(string)])
		}
	}

	// Neither route ran the source's command.
	if got := askedFor(t, asked); got != "" {
		t.Errorf("asked = %q, want nothing run", got)
	}
}

// answerStore is a yard's sources as Answerers reads them, without the
// store's one-source-per-kind refusal, so two sources may list one kind.
type answerStore []store.Source

func (s answerStore) Sources(context.Context) ([]store.Source, error) { return s, nil }

// The kind's mapping comes through the first source by name that lists it,
// as Ask picks. One source answers for a kind, so the store holds no such
// yard: this is the rows' resolution alone, against two sources that both
// list pi.
func TestProviderFirstByName(t *testing.T) {
	t.Parallel()
	st := answerStore{
		{Name: "zeta", Text: "name: zeta\nlimits:\n  command: [\"/bin/true\", \"{provider}\"]\n  kinds: { pi: openai }\n"},
		{Name: "alpha", Text: "name: alpha\nlimits:\n  command: [\"/bin/true\", \"{provider}\"]\n  kinds: { pi: zai }\n"},
	}
	as, err := limits.Answerers(context.Background(), st, "")
	must(t, err)
	if len(as) != 3 || as[0].Source != "alpha" || as[1].Source != "machine" || !as[1].Builtin || as[2].Source != "zeta" {
		t.Fatalf("Answerers = %+v, want alpha first by name, the built-in between", as)
	}
	if got := web.ProviderOf("herdr", map[string]any{"kind": "pi"}, as); got != "zai" {
		t.Errorf("pi resolves to %q, want alpha's zai", got)
	}
}

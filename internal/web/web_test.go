package web_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

func TestMain(m *testing.M) { storetest.Main(m) }

type fixture struct {
	t   *testing.T
	st  store.API
	own *store.Store
	ts  *httptest.Server
	// home is the yard's home, where a store flow's stage files are.
	home string
	// token, session and csrf are those of a view that acts, once signIn
	// has traded the token for the cookie as a browser does: do and
	// postForm then send the cookie, and the CSRF value with a write.
	token   string
	session *http.Cookie
	csrf    string
}

// newFixture serves a yard whose home is .../.depot and that has no `name`
// setting, so its name is depot.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, own := storetest.NewOwned(t)
	home := filepath.Join(t.TempDir(), ".depot")
	srv, err := web.NewServer(st, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return &fixture{t: t, st: st, own: own, ts: ts, home: home}
}

type resp struct {
	status int
	header http.Header
	body   string
}

func (f *fixture) do(method, target string, headers ...string) resp {
	f.t.Helper()
	req, err := http.NewRequest(method, f.ts.URL+target, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Add(headers[i], headers[i+1])
	}
	f.authorize(req)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

// signIn is a browser opening the URL the view printed: it trades the token
// ActAs gave for the cookie and keeps it, with the CSRF value the view's
// forms carry, for every request the fixture sends after.
func (f *fixture) signIn(srv *web.Server, token string) {
	f.t.Helper()
	f.token, f.csrf = token, srv.CSRF()
	f.session = f.cookieAt("")
}

// cookieAt trades the fixture's token for the cookie at the request host
// host ("" for the test server's own), as a browser that reached the view
// under that name does: the cookie is named for the port.
func (f *fixture) cookieAt(host string) *http.Cookie {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.ts.URL+"/?token="+f.token, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	cookies := r.Cookies()
	if r.StatusCode != http.StatusSeeOther || len(cookies) != 1 {
		f.t.Fatalf("the token URL at %q: status %d with %d cookies, want 303 and one", host, r.StatusCode, len(cookies))
	}
	return cookies[0]
}

// authorize gives req the cookie the fixture holds, and a write the CSRF
// value too, unless the caller sent one of its own.
func (f *fixture) authorize(req *http.Request) {
	if f.session == nil {
		return
	}
	req.AddCookie(f.session)
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		if req.Header.Get("X-CSRF-Token") == "" {
			req.Header.Set("X-CSRF-Token", f.csrf)
		}
	}
}

func (f *fixture) get(target string, headers ...string) resp {
	f.t.Helper()
	return f.do(http.MethodGet, target, headers...)
}

// populate gives the yard two depots: line, with a flow of its own and one
// for its bug beads, and loose, with no flow.
//
//	line   task beads: backlog 2 (one held), new 1; a closed one, not counted
//	       bug beads:  triage 1
//	loose  one bead in stage inbox
func (f *fixture) populate() {
	f.t.Helper()
	ctx := context.Background()
	must := func(err error) {
		f.t.Helper()
		if err != nil {
			f.t.Fatal(err)
		}
	}
	for _, depot := range []string{"line", "loose"} {
		must(f.st.PutDepot(ctx, store.Depot{Name: depot, Base: "origin/main"}, "me"))
	}
	flow := func(name, typ, text string) {
		f.t.Helper()
		fl, err := store.ParseFlow(text)
		must(err)
		fl.Name = name
		_, err = f.st.PutFlow(ctx, *fl, "me")
		must(err)
		must(f.st.SetFlowPointer(ctx, "line", typ, name, "me"))
	}
	flow("code", "", "backlog -> new\nnew -> review | backlog\nreview -> merged | new\nmerged terminal\n")
	flow("bugs", "bug", "triage -> fixed\nfixed terminal\n")
	bead := func(depot, typ, stage, title string) *store.Bead {
		f.t.Helper()
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: title, Type: typ, Stage: stage, Depot: depot}, "me")
		must(err)
		return b
	}
	bead("line", "task", "backlog", "one")
	held := bead("line", "task", "backlog", "two")
	bead("line", "task", "new", "three")
	closed := bead("line", "task", "new", "four")
	bead("line", "bug", "triage", "five")
	bead("loose", "task", "inbox", "six")
	_, err := f.st.SetHold(ctx, held.ID, true, store.Expect{}, "me")
	must(err)
	_, err = f.st.CloseBead(ctx, closed.ID, "dropped", store.Expect{}, "me")
	must(err)
}

var wantHome = web.Home{Yard: "depot", Depots: []web.HomeDepot{
	{Name: "line", Kind: store.DepotGit, Open: 4, Held: 1, Flows: []web.HomeFlow{
		{Name: "code", Stages: []web.StageCount{{Stage: "backlog", Open: 2}, {Stage: "new", Open: 1}, {Stage: "review"}}},
		{Name: "bugs", Type: "bug", Stages: []web.StageCount{{Stage: "triage", Open: 1}}},
	}},
	{Name: "loose", Kind: store.DepotGit, Open: 1, Flows: []web.HomeFlow{},
		Elsewhere: []web.StageCount{{Stage: "inbox", Open: 1}}},
}}

// The home page as JSON: the yard's name, its depots, and per depot the open
// beads per stage of its flows. A terminal stage holds no open bead and is
// left out; a closed bead is not counted.
func TestHomeJSON(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	r := f.get("/", "Accept", "application/json")
	if r.status != 200 || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, type %q: %s", r.status, r.header.Get("Content-Type"), r.body)
	}
	var got web.Home
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	// The feed has tests of its own (feed_test.go).
	if len(got.Events) == 0 || got.Head == 0 {
		t.Errorf("home has %d events and head %d, want the feed", len(got.Events), got.Head)
	}
	// The overview's other parts: the held bead waits, nothing runs, and
	// the bead closed today is named.
	bs := f.byTitle()
	if len(got.Attention) != 1 || got.Attention[0].Bead != bs["two"].ID {
		t.Errorf("attention %+v, want the held bead", got.Attention)
	}
	if len(got.Running) != 0 {
		t.Errorf("running %+v, want none: no session runs", got.Running)
	}
	if len(got.Closed) != 1 || got.Closed[0].Title != "four" || got.Closed[0].ClosedAt.IsZero() {
		t.Errorf("closed %+v, want the bead closed today", got.Closed)
	}
	got.Feed = web.Feed{}
	got.Attention, got.Running, got.Closed = nil, nil, nil
	if !reflect.DeepEqual(got, wantHome) {
		t.Errorf("home\n got %+v\nwant %+v", got, wantHome)
	}
	if v := r.header.Get("Vary"); v != "Accept" {
		t.Errorf("Vary = %q: one URL answers two ways, a cache must know", v)
	}
}

// The same request without the header is the page: the overview above the
// feed. The depots are one Yard row to the panel and what closed today one
// Closed row to the beads; each row hides at zero and the page prints no
// zero.
func TestHomeHTML(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	r := f.get("/")
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	bs := f.byTitle()
	text := squash(r.body)
	for _, want := range []string{
		"<title>Home · depot</title>",
		`<a class="chip chip-2" href="/line" title="depot" aria-label="depot">de</a>`,
		`<p class="answer">1 needs you. 1 fault.</p>`,
		`<a href="/beads/` + bs["two"].ID + `"><span class="dot held"></span><span class="held">held</span><span class="separator" aria-hidden="true">·</span><span class="entry-title">two</span><span class="separator" aria-hidden="true">·</span><span class="label">` + shortID(bs["two"].ID) + `</span><span class="faint">`,
		`<li><a href="/flows?panel=1">Yard 5 · held 1 ›</a></li>`,
		`<li><a href="/beads?all=1">Closed today 1 ›</a></li>`,
		`<a href="/" aria-current="page">Home</a>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// No session runs, so no Running row.
	if strings.Contains(text, `href="/sessions">Running`) {
		t.Error("the page has a Running row, and no session runs")
	}
	// The per-depot stage pairs and the closed titles left the page: no
	// depot line, no flow line, no old heading, and no zero. Working is
	// nothing here, so the Yard row names held alone. One wait fits, so
	// nothing overflows.
	for _, not := range []string{`<h2 class="label">Depots</h2>`, "flow-line", "4 open", "backlog 2",
		"off the flow", "nothing open", `<h2 class="label">Closed today</h2>`, "Running now",
		"working", "merged", ">review<", ">0<", "0 open", "git depot", "All waiting"} {
		if strings.Contains(text, not) {
			t.Errorf("the page has %q", not)
		}
	}
	// They stay in the JSON.
	h := homeJSON(t, f, "/")
	if len(h.Depots) != 2 || len(h.Depots[0].Flows) != 2 || len(h.Closed) != 1 || h.Closed[0].Title != "four" {
		t.Errorf("the JSON lost the depots or the closed title: %+v %+v", h.Depots, h.Closed)
	}
	must(t, f.st.SetSetting(context.Background(), "name", "plantime", "me"))
	if body := f.get("/").body; !strings.Contains(body, `href="/line" title="plantime" aria-label="plantime">pl</a>`) {
		t.Errorf("a named yard's chip carries no name: %s", body)
	}
}

// The home page for a yard with an empty depot and a busy one: the answer
// and the three rows in order above the feed, no zero printed, and what
// waits for a person first. A bead parked where its flow starts waits on no
// one in particular: it is on /line, not here.
func TestHomeOverview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "empty", Base: "origin/main"}, "me"))
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "busy", Base: "origin/main"}, "me"))
	fl, err := store.ParseFlow("backlog -> new\nnew -> decide\ndecide -> merged | new\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	for _, depot := range []string{"empty", "busy"} {
		must(t, f.st.SetFlowPointer(ctx, depot, "", "code", "me"))
	}
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 5}, "me"))
	for _, rt := range []store.Route{{Stage: "new", Group: "builders"}, {Stage: "decide", Group: "decisions"}, {Stage: "backlog", Group: "decisions"}} {
		_, err = f.st.AddRoute(ctx, rt, "me")
		must(t, err)
	}
	bead := func(title, stage string) *store.Bead {
		t.Helper()
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: title, Type: "task", Stage: stage, Depot: "busy"}, "me")
		must(t, err)
		return b
	}
	held := bead("held fast", "new")
	decide := bead("pick one", "decide")
	backlog := bead("backlog idea", "backlog")
	work := bead("at work", "new")
	blocked := bead("stuck prompt", "new")
	shut := bead("done deed", "new")
	_, err = f.st.SetHold(ctx, held.ID, true, store.Expect{}, "me")
	must(t, err)
	claim := func(b *store.Bead) *store.Session {
		t.Helper()
		sess, err := f.st.Claim(ctx, b.ID, "builders", "new", "yard")
		must(t, err)
		must(t, f.st.MarkRunning(ctx, sess.ID, map[string]any{"agent": b.Title}))
		return sess
	}
	claim(work)
	bsess := claim(blocked)
	must(t, f.st.LogDispatch(ctx, blocked.ID, "session_blocked", "yard", map[string]any{"session": bsess.ID}))
	_, err = f.st.CloseBead(ctx, shut.ID, "dropped", store.Expect{}, "me")
	must(t, err)

	h := homeJSON(t, f, "/")
	// Faults first, then the parked bead past its flow's start; the bead
	// parked in backlog is not among them.
	attn := []string{}
	for _, w := range h.Attention {
		attn = append(attn, w.Bead)
	}
	if len(attn) != 3 || attn[2] != decide.ID ||
		!slices.Contains(attn[:2], held.ID) || !slices.Contains(attn[:2], blocked.ID) {
		t.Errorf("attention %v, want the held and blocked beads, then the decide one", attn)
	}
	if l := lineJSON(t, f); len(l.Parked) != 2 {
		t.Errorf("/line parks %d beads, want backlog's and decide's", len(l.Parked))
	} else if l.Parked[0].Bead != backlog.ID && l.Parked[1].Bead != backlog.ID {
		t.Errorf("/line parks %v, without the backlog bead", l.Parked)
	}
	// The two agents' sessions, the oldest first.
	if len(h.Running) != 2 || h.Running[0].Bead != work.ID || h.Running[1].Bead != blocked.ID || !h.Running[1].Blocked {
		t.Errorf("running %+v, want work, then blocked and flagged", h.Running)
	}
	if len(h.Closed) != 1 || h.Closed[0].ID != shut.ID {
		t.Errorf("closed %+v, want the bead shut today", h.Closed)
	}

	text := squash(f.get("/").body)
	// The answer and the three rows in order, above the feed. The row
	// markers name their counts, so the rail's Lines tile does not match.
	heads := []string{`<p class="answer">`, `>Running 2 · oldest `, `>Yard 5 · working 2 · held 1 ›<`,
		`>Closed today 1 ›<`, "What happened"}
	for i := 1; i < len(heads); i++ {
		if !strings.Contains(text, heads[i-1]) || strings.Index(text, heads[i-1]) > strings.Index(text, heads[i]) {
			t.Errorf("the page does not run %q before %q", heads[i-1], heads[i])
		}
	}
	part := func(from, to string) string {
		_, s, _ := strings.Cut(text, from)
		s, _, _ = strings.Cut(s, to)
		return s
	}
	waits := part(`<p class="answer">`, `>Running 2 · oldest `)
	if !strings.HasPrefix(waits, "3 need you. 2 faults.</p>") {
		t.Errorf("the answer is %q, want three needs and two faults", waits)
	}
	for _, want := range []string{
		`<a href="/beads/` + held.ID + `"><span class="dot held"></span><span class="held">held</span><span class="separator" aria-hidden="true">·</span><span class="entry-title">held fast</span><span class="separator" aria-hidden="true">·</span><span class="label">` + shortID(held.ID) + `</span><span class="faint">`,
		`<a href="/beads/` + decide.ID + `"><span class="stage">decide</span><span class="separator" aria-hidden="true">·</span><span class="entry-title">pick one</span><span class="separator" aria-hidden="true">·</span><span class="label">` + shortID(decide.ID) + `</span><span class="faint">`,
		`<a href="/beads/` + blocked.ID + `"><span class="dot held"></span><span class="held">blocked</span><span class="separator" aria-hidden="true">·</span><span class="entry-title">stuck prompt</span><span class="separator" aria-hidden="true">·</span>`,
	} {
		if !strings.Contains(waits, want) {
			t.Errorf("what waits lacks %q", want)
		}
	}
	for _, id := range []string{held.ID, blocked.ID} {
		if !strings.Contains(waits, id) || strings.Index(waits, id) > strings.Index(waits, decide.ID) {
			t.Errorf("the fault %s does not stand before the parked bead", id)
		}
	}
	if strings.Contains(waits, backlog.ID) {
		t.Error("what waits names the backlog bead, which is parked where its flow starts")
	}
	// The rows: the Running row names the count and the oldest age, the
	// Yard row the open, working and held counts, the Closed row the count
	// alone, each linking out.
	// The page renders a fresh snapshot, so the age can round up since the
	// JSON read.
	runningRow := `<li><a href="/sessions">Running 2 · oldest `
	if _, tail, ok := strings.Cut(text, runningRow); !ok {
		t.Errorf("the rows lack %q", runningRow)
	} else if runningAge, _, ok := strings.Cut(tail, ` ›</a></li>`); !ok || runningAge == "" {
		t.Errorf("the Running row has no age: %q", tail)
	}
	for _, want := range []string{
		`<li><a href="/flows?panel=1">Yard 5 · working 2 · held 1 ›</a></li>`,
		`<li><a href="/beads?all=1">Closed today 1 ›</a></li>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the rows lack %q", want)
		}
	}
	rows := part(`</section>`, "What happened")
	// The sessions, the per-depot pairs and the closed titles left the
	// rows: no bead link, no flow line, no old heading, and no zero.
	for _, not := range []string{work.ID, blocked.ID, shut.ID, "flow-line", "5 open", "nothing open",
		`<h2 class="label">Depots</h2>`, `<h2 class="label">Closed today</h2>`, "Running now",
		">0<", "0 open", "merged", ">review<"} {
		if strings.Contains(rows, not) {
			t.Errorf("the rows have %q", not)
		}
	}
	// They stay in the JSON.
	busy := -1
	for _, d := range h.Depots {
		if d.Name == "busy" {
			busy = d.Open
		}
	}
	if len(h.Depots) != 2 || busy != 5 || len(h.Running) != 2 || h.Closed[0].Title != "done deed" {
		t.Errorf("the JSON lost the depots, the sessions or the closed title: %+v", h)
	}
}

// An empty row of the overview is absent: a yard with one plain bead and
// nothing else shows the answer and its Yard row alone.
func TestHomeOverviewEmpty(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "solo", Base: "origin/main"}, "me"))
	_, err := f.st.CreateBead(ctx, store.NewBead{Title: "only", Type: "task", Stage: "new", Depot: "solo"}, "me")
	must(t, err)
	text := squash(f.get("/").body)
	for _, want := range []string{
		`<p class="answer">Nothing needs you.</p>`,
		`<p class="faint">All clear.</p>`,
		`<li><a href="/flows?panel=1">Yard 1 ›</a></li>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, not := range []string{`href="/sessions">Running`, "Closed today", "All waiting",
		`<h2 class="label">Depots</h2>`, "flow-line", "1 open", "nothing open", ">0<", "0 open"} {
		if strings.Contains(text, not) {
			t.Errorf("the page has %q", not)
		}
	}
	h := homeJSON(t, f, "/")
	if len(h.Attention) != 0 || len(h.Running) != 0 || len(h.Closed) != 0 {
		t.Errorf("attention %+v, running %+v, closed %+v, want none", h.Attention, h.Running, h.Closed)
	}
	if len(h.Depots) != 1 || h.Depots[0].Open != 1 {
		t.Errorf("depots %+v, want solo's bead in the JSON", h.Depots)
	}
}

// The rows' counts: the Yard row sums the depots, and the Running row's age
// is the oldest session's, empty when nothing runs.
func TestHomeRows(t *testing.T) {
	t.Parallel()
	h := web.Home{Depots: []web.HomeDepot{{Open: 2, Working: 1}, {Open: 3, Held: 1}},
		Running: []web.SessionRow{{Age: "3h"}, {Age: "2m"}}}
	if h.YardOpen() != 5 || h.YardWorking() != 1 || h.YardHeld() != 1 {
		t.Errorf("yard %d/%d/%d, want 5/1/1", h.YardOpen(), h.YardWorking(), h.YardHeld())
	}
	if got := h.OldestAge(); got != "3h" {
		t.Errorf("oldest %q, want the first row's age", got)
	}
	if got := (web.Home{}).OldestAge(); got != "" {
		t.Errorf("oldest with nothing running is %q, want empty", got)
	}
}

// What closed today: the closed beads of this machine's date, the newest
// first, at most ten. The open, the dateless and the older ones are none
// of it.
func TestClosedToday(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 11, 15, 4, 5, 0, time.Local)
	at := func(day, h, m int) *time.Time {
		t := time.Date(2026, 10, day, h, m, 0, 0, time.Local)
		return &t
	}
	beads := []store.Bead{
		{ID: "open", Title: "open", Status: "open"},
		{ID: "nodate", Title: "nodate", Status: "closed"},
		{ID: "old", Title: "old", Status: "closed", ClosedAt: at(10, 23, 59)},
	}
	for i := range 12 {
		id := fmt.Sprintf("b%02d", i)
		beads = append(beads, store.Bead{ID: id, Title: id, Status: "closed", ClosedAt: at(11, 9, i)})
	}
	var want []string
	for i := 11; i >= 2; i-- {
		want = append(want, fmt.Sprintf("b%02d", i))
	}
	var got []string
	for _, c := range web.ClosedToday(beads, now) {
		got = append(got, c.ID)
		if c.Title != c.ID || c.ClosedAt.IsZero() {
			t.Errorf("closed bead %+v keeps no title or time", c)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("closed today %v, want %v", got, want)
	}
	if got := web.ClosedToday(nil, now); got == nil || len(got) != 0 {
		t.Errorf("no beads: %+v, want an empty list", got)
	}
}

// What closed today across the day boundary: the store keeps close times in
// UTC, and today is this machine's date. Just after midnight a bead closed a
// minute ago is today's, though its UTC date is yesterday's; just before
// midnight a bead closed at this morning's small hours is today's too,
// though its UTC date is yesterday's as well. Last night's beads are none
// of it in either case.
func TestClosedTodayDayBoundary(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("UTC+2", 2*60*60)
	utc := func(day, h, m int) *time.Time {
		t := time.Date(2026, 10, day, h, m, 0, 0, loc).UTC()
		return &t
	}
	ids := func(beads []store.Bead, now time.Time) []string {
		var got []string
		for _, c := range web.ClosedToday(beads, now) {
			got = append(got, c.ID)
		}
		return got
	}
	// Just after midnight: the bead closed a minute ago is named, last
	// night's is not.
	after := time.Date(2026, 10, 12, 0, 5, 0, 0, loc)
	minute := after.Add(-time.Minute).UTC()
	beads := []store.Bead{
		{ID: "now", Title: "now", Status: "closed", ClosedAt: &minute},
		{ID: "night", Title: "night", Status: "closed", ClosedAt: utc(11, 23, 54)},
	}
	if got, want := ids(beads, after), []string{"now"}; !reflect.DeepEqual(got, want) {
		t.Errorf("at 00:05 closed today %v, want %v", got, want)
	}
	// Just before midnight: the bead closed at this morning's small hours
	// is named, last night's is not.
	before := time.Date(2026, 10, 11, 23, 55, 0, 0, loc)
	beads = []store.Bead{
		{ID: "morning", Title: "morning", Status: "closed", ClosedAt: utc(11, 0, 4)},
		{ID: "night", Title: "night", Status: "closed", ClosedAt: utc(10, 23, 54)},
	}
	if got, want := ids(beads, before), []string{"morning"}; !reflect.DeepEqual(got, want) {
		t.Errorf("at 23:55 closed today %v, want %v", got, want)
	}
}

// squash drops the whitespace between tags, so a test reads the markup and
// not its indentation.
func squash(html string) string {
	return regexp.MustCompile(`>\s+<`).ReplaceAllString(html, "><")
}

// The yard's name is the `name` setting when there is one.
func TestYardNameSetting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.st.SetSetting(context.Background(), "name", "north", "me"); err != nil {
		t.Fatal(err)
	}
	var got web.Home
	if err := json.Unmarshal([]byte(f.get("/", "Accept", "application/json").body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Yard != "north" || len(got.Depots) != 0 || got.Depots == nil {
		t.Errorf("got %+v, want yard north and an empty list of depots", got)
	}
	r := f.get("/")
	if !strings.Contains(r.body, "This yard has no depot yet.") {
		t.Errorf("an empty yard's page does not say so: %s", r.body)
	}
	// Every row hides at zero: nothing runs, nothing is open, nothing
	// closed.
	for _, not := range []string{`href="/sessions">Running`, `href="/flows?panel=1">Yard `, "Closed today"} {
		if strings.Contains(squash(r.body), not) {
			t.Errorf("an empty yard's page has %q", not)
		}
	}
}

// A depot whose repository flows cannot be read is still shown, and must not
// hide the other depots. Its open beads are counted under "off the flow".
type failingDepotFlows struct {
	store.API
	depot string
}

func (s failingDepotFlows) DepotFlows(ctx context.Context, depot string) ([]store.DepotFlow, error) {
	if depot == s.depot {
		return nil, errors.New("repository is unavailable")
	}
	return s.API.DepotFlows(ctx, depot)
}

func TestFlowReadErrorKeepsOtherDepots(t *testing.T) {
	t.Parallel()
	base := storetest.New(t)
	ctx := context.Background()
	for _, name := range []string{"broken", "healthy"} {
		if err := base.PutDepot(ctx, store.Depot{Name: name, Base: "origin/main"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, depot := range []string{"broken", "healthy"} {
		if _, err := base.CreateBead(ctx, store.NewBead{Title: depot + " bead", Type: "task", Stage: "new", Depot: depot}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := web.NewServer(failingDepotFlows{API: base, depot: "broken"}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	req.Header.Set("Accept", "application/json")
	srv.ServeHTTP(r, req)
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body)
	}
	var got web.Home
	if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Depots) != 2 {
		t.Fatalf("got depots %+v, want broken and healthy", got.Depots)
	}
	broken := got.Depots[0]
	if broken.Name != "broken" || broken.Error != "repository is unavailable" || len(broken.Flows) != 0 ||
		!reflect.DeepEqual(broken.Elsewhere, []web.StageCount{{Stage: "new", Open: 1}}) {
		t.Errorf("broken depot = %+v", broken)
	}
	healthy := got.Depots[1]
	if healthy.Name != "healthy" || healthy.Error != "" || healthy.Open != 1 ||
		!reflect.DeepEqual(healthy.Elsewhere, []web.StageCount{{Stage: "new", Open: 1}}) {
		t.Errorf("healthy depot = %+v", healthy)
	}
}

// Which of the two a request gets: HTML unless it names JSON above HTML.
func TestNegotiation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for accept, wantJSON := range map[string]bool{
		"":    false,
		"*/*": false, // curl
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8": false, // a browser
		"application/json":                  true,
		"application/json, text/plain, */*": true,
		"application/*":                     true,
		"text/html;q=0.5, application/json": true,
		"application/json;q=0.5, text/html": false,
		"application/json;q=0":              false,
		"application/json;q=x":              false,
		"APPLICATION/JSON ; Q=1.0":          true,
	} {
		var headers []string
		if accept != "" {
			headers = []string{"Accept", accept}
		}
		r := f.get("/", headers...)
		if got := r.header.Get("Content-Type") == "application/json"; got != wantJSON || r.status != 200 {
			t.Errorf("Accept %q: status %d, JSON %v, want %v", accept, r.status, got, wantJSON)
		}
	}
}

// The line page is the right column's data, for a window without the column.
func TestLine(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	var got web.Line
	if err := json.Unmarshal([]byte(f.get("/line", "Accept", "application/json").body), &got); err != nil {
		t.Fatal(err)
	}
	want := web.Line{Open: 5, Held: 1, Parked: []web.Waiting{}, Late: []web.LateRow{}, Depots: []web.LineDepot{{Name: "line", Open: 4}, {Name: "loose", Open: 1}},
		Stages: []web.StageCount{{Stage: "backlog", Open: 2}, {Stage: "new", Open: 1}, {Stage: "triage", Open: 1}, {Stage: "inbox", Open: 1}}}
	// What waits has tests of its own (line_test.go).
	if len(got.Waits) != 1 {
		t.Errorf("%d beads wait, want the held one: %+v", len(got.Waits), got.Waits)
	}
	got.Waits = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("line\n got %+v\nwant %+v", got, want)
	}
	// Home and the line carry it in the aside; every other page counts
	// what waits in its header instead.
	for _, path := range []string{"/", "/line"} {
		if text := squash(f.get(path).body); !strings.Contains(text, `<dt>open</dt><dd>5</dd>`) {
			t.Errorf("%s does not show the line's 5 open beads", path)
		}
	}
	if text := squash(f.get("/beads").body); !strings.Contains(text, `<a class="needs-count" href="/">1 waits for you</a>`) {
		t.Error("/beads counts no waiting bead in its header")
	}
}

// Every page of the rail answers: the tiles in the rail's order, each
// opening its page, and each mode's column holding its rows. The pages
// later children add say so, in both forms, under a status no client
// takes for an empty answer.
func TestRailPages(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "line", Base: "origin/main"}, "me"))
	run, err := f.st.CreateBead(ctx, store.NewBead{Title: "at work", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	tiles := regexp.MustCompile(`<a class="tile[^"]*" href="([^"]+)"`)
	var hrefs []string
	for _, m := range tiles.FindAllStringSubmatch(f.get("/").body, -1) {
		hrefs = append(hrefs, m[1])
	}
	if want := []string{"/talk", "/line", "/flows?panel=1", "/config"}; !slices.Equal(hrefs, want) {
		t.Errorf("the rail's tiles open %v, want %v", hrefs, want)
	}
	for _, mode := range []struct{ path, tile, head string }{
		{"/talk", "/talk", "Crew"},
		{"/line", "/line", "Decide"},
		{"/flows?panel=1", "/flows?panel=1", "Lines"},
		{"/config", "/config", "Settings"},
	} {
		body := f.get(mode.path).body
		if want := `<a class="tile open" href="` + mode.tile + `"`; !strings.Contains(body, want) {
			t.Errorf("%s leaves no tile open for %s", mode.path, want)
		}
		if want := `<p class="section-head">` + mode.head + `</p>`; !strings.Contains(body, want) {
			t.Errorf("%s shows no %s column", mode.path, mode.head)
		}
	}
	// One page per mode exposes every column; the rail itself names
	// sixteen pages.
	column := regexp.MustCompile(`(?s)<nav class="rail-column".*?</nav>`)
	links := regexp.MustCompile(`<li><a href="([^"]+)"`)
	var paths []string
	seen := map[string]bool{}
	for _, page := range []string{"/talk", "/line", "/flows?panel=1", "/config"} {
		for _, l := range links.FindAllStringSubmatch(column.FindString(f.get(page).body), -1) {
			if !seen[l[1]] {
				seen[l[1]] = true
				paths = append(paths, l[1])
			}
		}
	}
	if len(paths) != 16 {
		t.Fatalf("the rail has %d pages, want 16: %v", len(paths), paths)
	}
	for _, path := range paths {
		r := f.get(path)
		switch path {
		case "/", "/talk", "/line", "/flows", "/flows?panel=1", "/beads", "/sessions", "/crew", "/previews", "/config",
			"/depots", "/groups", "/routes", "/peers", "/deps", "/limits":
			if r.status != 200 {
				t.Errorf("%s: status %d", path, r.status)
			}
		default:
			if r.status != http.StatusNotImplemented || !strings.Contains(r.body, "Not built yet.") {
				t.Errorf("%s: status %d, want 501 and the not-built page", path, r.status)
			}
			j := f.get(path, "Accept", "application/json")
			var got web.Unbuilt
			if err := json.Unmarshal([]byte(j.body), &got); err != nil || j.status != http.StatusNotImplemented ||
				got != (web.Unbuilt{Page: strings.TrimPrefix(path, "/")}) {
				t.Errorf("%s as JSON: status %d, %+v (%v)", path, j.status, got, err)
			}
		}
		if !strings.Contains(r.body, `<a href="`+path+`" aria-current="page">`) {
			t.Errorf("%s does not mark its own column row", path)
		}
	}
	bead := f.get("/beads/" + run.ID)
	if bead.status != http.StatusOK || !strings.Contains(bead.body, `<a class="tile open" href="/flows?panel=1"`) ||
		!strings.Contains(bead.body, `<a href="/beads" aria-current="page">Beads</a>`) {
		t.Errorf("a bead page does not stand under the Lines column: %s", bead.body)
	}
}

func TestNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if r := f.get("/nowhere"); r.status != 404 || !strings.Contains(r.body, "no page at /nowhere") || !strings.Contains(r.body, `class="rail"`) {
		t.Errorf("status %d, want 404 inside the shell: %s", r.status, r.body)
	} else if strings.Contains(r.body, `class="tile open"`) || strings.Contains(r.body, `<nav class="rail-column"`) ||
		strings.Contains(r.body, `class="pill"`) {
		t.Errorf("the error page opens a mode, column or pill: %s", r.body)
	}
	r := f.get("/nowhere", "Accept", "application/json")
	var got map[string]string
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 404 || got["error"] != "no page at /nowhere" {
		t.Errorf("JSON: status %d, %v (%v)", r.status, got, err)
	}
	if r := f.get("/favicon.ico"); r.status != http.StatusNoContent {
		t.Errorf("/favicon.ico: status %d, want 204: the view has no image and a browser asks anyway", r.status)
	}
	if r := f.get("/assets/README.md"); r.status != 404 {
		t.Errorf("the assets' README is served (status %d): it is not an asset", r.status)
	}
}

// Without --as the view reads the yard and changes nothing: no route takes
// a write, the talk and bead pages' included.
func TestReadOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/", "/talk", "/line", "/beads", "/beads/abc", "/assets/yard.css", "/nowhere"} {
			if r := f.do(method, path); r.status != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status %d, want 405", method, path, r.status)
			}
		}
	}
}

// An address off the loopback interface is refused, and the message says
// why: there is no authentication yet.
func TestRefusedAddress(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{":8791", "0.0.0.0:8791", "192.168.1.10:8791", "[::]:8791", "example.com:80", "[2001:db8::1]:80"} {
		err := web.CheckAddr("--addr", addr, false)
		if err == nil {
			t.Errorf("%s: accepted", addr)
			continue
		}
		for _, want := range []string{addr, "refused", "no authentication", "loopback", "--unsafe"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal lacks %q: %v", addr, want, err)
			}
		}
		if ln, err := web.Listen("--addr", addr, false); err == nil {
			ln.Close()
			t.Errorf("%s: Listen opened it", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8791", "127.0.0.1:0", "[::1]:8791", "localhost:8791", "127.8.8.8:1"} {
		if err := web.CheckAddr("--addr", addr, false); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if err := web.CheckAddr("--addr", "127.0.0.1", false); err == nil || strings.Contains(err.Error(), "refused") {
		t.Errorf("an address with no port: %v, want the parse error", err)
	}
	ln, err := web.Listen("--addr", "127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

// --unsafe lets every address through, the ones on every interface too,
// and Listen opens one; what is no address is still an error.
func TestUnsafeAddress(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{":9000", "0.0.0.0:9000", "[::]:9000", "192.168.178.90:8791", "127.0.0.1:8791", "example.com:80"} {
		if err := web.CheckAddr("--addr", addr, true); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if err := web.CheckAddr("--addr", "0.0.0.0", true); err == nil {
		t.Error("an address with no port: accepted")
	}
	ln, err := web.Listen("--addr", ":0", true)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if tcp := ln.Addr().(*net.TCPAddr); !tcp.IP.IsUnspecified() {
		t.Errorf(":0 listens on %s, want every interface", tcp)
	}
}

// Under --unsafe a request names the machine by any IP or by its host
// name; another site's name is still refused (DNS rebinding).
func TestUnsafeHost(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.AllowNetwork("AdventureTime.local")
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: st, ts: ts}
	for _, host := range []string{"192.168.178.90:8791", "192.168.178.90", "10.0.0.1:1", "[2001:db8::1]:8791", "[fe80::1]",
		"adventuretime", "adventuretime.local:8791", "ADVENTURETIME.LOCAL", "localhost:8791", "127.0.0.1:8791", "[::1]:8791"} {
		if r := f.get("/", "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d", host, r.status)
		}
	}
	for _, host := range []string{"evil.example", "evil.example:8791", "192.168.178.90.evil.example", "adventuretime.evil.example",
		"adventuretime.local.evil.example", "other.local", "local", ".local"} {
		r := f.get("/", "Host", host)
		if r.status != http.StatusMisdirectedRequest {
			t.Errorf("Host %s: status %d, want 421", host, r.status)
		}
		if !strings.Contains(r.body, "adventuretime.local") {
			t.Errorf("Host %s: the refusal does not say what to ask for: %q", host, r.body)
		}
	}

	// A machine with no host name is reached by IP and by no name.
	bare, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	if err != nil {
		t.Fatal(err)
	}
	bare.AllowNetwork("")
	bts := httptest.NewServer(bare)
	t.Cleanup(func() { shut(bts) })
	bf := &fixture{t: t, st: st, ts: bts}
	if r := bf.get("/", "Host", "10.0.0.1"); r.status != 200 {
		t.Errorf("no host name, Host 10.0.0.1: status %d", r.status)
	}
	for _, host := range []string{".local", "evil.example"} {
		if r := bf.get("/", "Host", host); r.status != http.StatusMisdirectedRequest {
			t.Errorf("no host name, Host %s: status %d, want 421", host, r.status)
		}
	}
}

// A name the operator gave (--host) is answered as the machine's own are,
// in any case and with any port; a name not given is still refused, and
// the refusal says how to give it.
func TestGivenHost(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.AllowNetwork("adventuretime", "adventuretime.tail5cf2e8.ts.net", "Yard.Example.net", "adventuretime")
	if got, want := srv.Names(), []string{"adventuretime", "adventuretime.local", "adventuretime.tail5cf2e8.ts.net", "yard.example.net"}; !slices.Equal(got, want) {
		t.Errorf("names %v, want %v", got, want)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: st, ts: ts}
	for _, host := range []string{"adventuretime.tail5cf2e8.ts.net", "adventuretime.tail5cf2e8.ts.net:8791", "ADVENTURETIME.Tail5CF2E8.ts.net:8791",
		"yard.example.net:443", "YARD.EXAMPLE.NET", "adventuretime.local:8791", "192.168.178.90:8791"} {
		if r := f.get("/", "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d", host, r.status)
		}
	}
	for host, name := range map[string]string{
		"other.tail5cf2e8.ts.net:8791":        "other.tail5cf2e8.ts.net",
		"Evil.Example":                        "evil.example",
		"tail5cf2e8.ts.net":                   "tail5cf2e8.ts.net",
		"x.adventuretime.tail5cf2e8.ts.net:1": "x.adventuretime.tail5cf2e8.ts.net",
		"yard.example.net.evil.example":       "yard.example.net.evil.example",
	} {
		r := f.get("/", "Host", host)
		if r.status != http.StatusMisdirectedRequest {
			t.Errorf("Host %s: status %d, want 421", host, r.status)
		}
		for _, want := range []string{"yard.example.net", ", not " + host + ". If that name is this machine's, start the view with --host " + name + "\n"} {
			if !strings.Contains(r.body, want) {
				t.Errorf("Host %s: the refusal lacks %q: %q", host, want, r.body)
			}
		}
	}
	// What is no host name is not offered back as one.
	if r := f.get("/", "Host", "evil*.example"); r.status != http.StatusMisdirectedRequest || strings.Contains(r.body, "--host") {
		t.Errorf("Host evil*.example: status %d, body %q", r.status, r.body)
	}

}

// On loopback (--host without --unsafe) the view answers to the loopback
// names and the names given, and to nothing it was not given: not the
// machine's own host name, and no IP address off loopback. The refusal
// names the flag for a name, on a view with no names too, and not for an
// IP address, which the flag does not take there.
func TestLoopbackHost(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.AllowHosts("adventuretime.tail5cf2e8.ts.net", "Yard.Example.net", "yard.example.net")
	if got, want := srv.Names(), []string{"adventuretime.tail5cf2e8.ts.net", "yard.example.net"}; !slices.Equal(got, want) {
		t.Errorf("names %v, want %v", got, want)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: st, ts: ts}
	for _, host := range []string{"adventuretime.tail5cf2e8.ts.net", "adventuretime.tail5cf2e8.ts.net:8791", "ADVENTURETIME.Tail5CF2E8.ts.net:443",
		"yard.example.net:443", "YARD.EXAMPLE.NET", "localhost:8791", "127.0.0.1:8791", "[::1]:8791"} {
		if r := f.get("/", "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d", host, r.status)
		}
	}
	// The machine's own host name is one of the names not given: only
	// --unsafe adds it by itself.
	hostname, _ := os.Hostname()
	own := strings.TrimSuffix(strings.ToLower(hostname), ".local")
	refused := map[string]string{
		"other.tail5cf2e8.ts.net:8791":      "other.tail5cf2e8.ts.net",
		"Evil.Example":                      "evil.example",
		"yard.example.net.evil.example:443": "yard.example.net.evil.example",
		"adventuretime":                     "adventuretime",
		"adventuretime.local:8791":          "adventuretime.local",
	}
	if _, err := web.CheckHost(own); err == nil {
		refused[own+":8791"] = own
		refused[own+".local"] = own + ".local"
	}
	for host, name := range refused {
		r := f.get("/", "Host", host)
		if r.status != http.StatusMisdirectedRequest {
			t.Errorf("Host %s: status %d, want 421", host, r.status)
		}
		for _, want := range []string{"ask for it as localhost or 127.0.0.1 or adventuretime.tail5cf2e8.ts.net or yard.example.net, not " + host +
			". If that name is this machine's, start the view with --host " + name + "\n"} {
			if !strings.Contains(r.body, want) {
				t.Errorf("Host %s: the refusal lacks %q: %q", host, want, r.body)
			}
		}
	}
	for _, host := range []string{"192.168.178.90:8791", "10.0.0.1", "[2001:db8::1]:8791", "evil*.example"} {
		if r := f.get("/", "Host", host); r.status != http.StatusMisdirectedRequest || strings.Contains(r.body, "--host") {
			t.Errorf("Host %s: status %d, body %q", host, r.status, r.body)
		}
	}

	// The machine's host name is answered once it is given.
	named, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	if err != nil {
		t.Fatal(err)
	}
	named.AllowHosts("adventuretime", "adventuretime.local")
	nts := httptest.NewServer(named)
	t.Cleanup(func() { shut(nts) })
	nf := &fixture{t: t, st: st, ts: nts}
	for _, host := range []string{"adventuretime:8791", "AdventureTime.local"} {
		if r := nf.get("/", "Host", host); r.status != 200 {
			t.Errorf("given, Host %s: status %d", host, r.status)
		}
	}

	// A view with no names says how to give one all the same.
	lf := newFixture(t)
	r := lf.get("/", "Host", "yard.example.net:8791")
	if want := "ask for it as localhost or 127.0.0.1, not yard.example.net:8791. If that name is this machine's, start the view with --host yard.example.net\n"; r.status != http.StatusMisdirectedRequest || !strings.Contains(r.body, want) {
		t.Errorf("no names, Host yard.example.net:8791: status %d, body %q, want %q", r.status, r.body, want)
	}
	if r := lf.get("/", "Host", "10.0.0.1:8791"); r.status != http.StatusMisdirectedRequest || strings.Contains(r.body, "--host") {
		t.Errorf("no names, Host 10.0.0.1:8791: status %d, body %q", r.status, r.body)
	}
}

// A name for --host is compared in lower case and without the dot that
// ends a full name; a wildcard, an empty name and what is no name at all
// are refused.
func TestCheckHost(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"adventuretime":                    "adventuretime",
		"AdventureTime.Tail5cf2e8.ts.net.": "adventuretime.tail5cf2e8.ts.net",
		"my_box.lan":                       "my_box.lan",
		"xn--bcher-kva.example":            "xn--bcher-kva.example",
	} {
		if got, err := web.CheckHost(name); err != nil || got != want {
			t.Errorf("CheckHost(%q) = %q, %v, want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"", ".", "*", "*.ts.net", "a*.example", "yard.example.net:8791", "http://yard.example.net", "[::1]",
		"a..b", ".example", "yard.example.net/", "a b", "bücher.example"} {
		if got, err := web.CheckHost(name); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("CheckHost(%q) = %q, %v, want a refusal", name, got, err)
		}
	}
}

// A request that reached the loopback listener under another host's name
// (DNS rebinding from a page elsewhere) is refused before any page is built.
func TestForeignHostRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	for _, host := range []string{"evil.example", "evil.example:8791", "127.0.0.1.evil.example", "10.0.0.1:8791"} {
		r := f.get("/", "Host", host, "Accept", "application/json")
		if r.status != http.StatusMisdirectedRequest || strings.Contains(r.body, "loose") {
			t.Errorf("Host %s: status %d, body %q", host, r.status, r.body)
		}
	}
	for _, host := range []string{"localhost:8791", "localhost", "LOCALHOST:1", "127.0.0.1:8791", "[::1]:8791"} {
		if r := f.get("/", "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d", host, r.status)
		}
	}
}

var otherHost = regexp.MustCompile(`(?i)\b(?:https?:)?//[^\s"'<>()\\]+`)

// The page loads with the network otherwise off: nothing the server sends,
// HTML, CSS or JS, names a URL on another host, and the policy it sends
// keeps the browser to this one.
func TestNoOtherHost(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.populate()
	// One bead carries a page, so the walk meets a run link: the one
	// foreign URL a page may name, to the yard's own sandbox origin.
	ctx := context.Background()
	door, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Depot: "loose"}, "me")
	must(t, err)
	page, err := f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, door.ID, "the door, hung", "me", "", store.BlobFile{Hash: page.Hash})
	must(t, err)
	self, err := url.Parse(f.ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	// isRunLink is a bead page's run link and nothing else: the sandbox
	// origin's address with a blob's path, never another host.
	isRunLink := func(link string) bool {
		hash, ok := strings.CutPrefix(link, "//"+store.DefaultWebPagesAddr+"/blobs/")
		return ok && len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
	}
	var runs int
	seen := map[string]bool{}
	// /telemetry stands alone: the hub links it for the operator alone,
	// and the walk reads as no one.
	queue := []string{"/", "/nowhere", "/telemetry"}
	ref := regexp.MustCompile(`(?:href|src)="([^"]+)"`)
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		seen[path] = true
		r := f.get(path)
		ctype := r.header.Get("Content-Type")
		for _, m := range otherHost.FindAllString(r.body, -1) {
			// "//" also opens a comment in the scripts; a URL has a host.
			u, err := url.Parse(m)
			if strings.HasPrefix(m, "//") && (err != nil || !strings.Contains(u.Host, ".")) {
				continue
			}
			if err != nil || u.Host != self.Host {
				t.Errorf("%s (%s) names another host: %s", path, ctype, m)
			}
		}
		if !strings.HasPrefix(ctype, "text/html") {
			continue
		}
		if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: Content-Security-Policy %q does not keep the browser to this host", path, csp)
		}
		if strings.Contains(r.body, "<style") || strings.Contains(r.body, " style=") || regexp.MustCompile(`<script(?:\s+defer)?>`).MatchString(r.body) {
			t.Errorf("%s has inline style or script, which the policy refuses", path)
		}
		for _, m := range ref.FindAllStringSubmatch(r.body, -1) {
			// A run link names the sandbox origin, the walk's one
			// link off this server; it is followed nowhere.
			if isRunLink(m[1]) {
				runs++
				continue
			}
			if !strings.HasPrefix(m[1], "/") || strings.HasPrefix(m[1], "//") {
				t.Errorf("%s links to %q, not a path on this server", path, m[1])
				continue
			}
			queue = append(queue, strings.ReplaceAll(m[1], "&amp;", "&"))
		}
	}
	if runs == 0 {
		t.Error("the walk met no run link")
	}
	var assets int
	var beadPage bool
	for path := range seen {
		if strings.HasPrefix(path, "/assets/") {
			assets++
		}
		beadPage = beadPage || strings.HasPrefix(path, "/beads/")
	}
	// The walk must have reached what it is there to check: every page of
	// the rail, a bead's own page, the error page.
	for _, path := range []string{"/", "/talk", "/line", "/flows", "/flows?panel=1", "/beads", "/sessions", "/crew",
		"/previews", "/config", "/depots", "/groups", "/routes", "/peers", "/deps", "/limits", "/telemetry", "/nowhere"} {
		if !seen[path] {
			t.Errorf("the walk did not reach %s: %v", path, seen)
		}
	}
	if assets != 4 || !beadPage {
		t.Errorf("walked %d assets, want 4, and a bead's page (%v): %v", assets, beadPage, seen)
	}
}

// Assets are served with their type and a checksum in the URL that lets a
// browser keep them for good.
func TestAssets(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	page := f.get("/").body
	for name, ctype := range map[string]string{
		"yard.css":            "text/css; charset=utf-8",
		"htmx.min.js":         "text/javascript; charset=utf-8",
		"htmx-ext-sse.min.js": "text/javascript; charset=utf-8",
		"yard.js":             "text/javascript; charset=utf-8",
	} {
		m := regexp.MustCompile(`"(/assets/` + regexp.QuoteMeta(name) + `\?v=[0-9a-f]{12})"`).FindStringSubmatch(page)
		if m == nil {
			t.Errorf("the page does not load %s by a versioned URL", name)
			continue
		}
		r := f.get(m[1])
		if r.status != 200 || r.header.Get("Content-Type") != ctype || !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: status %d, type %q, cache %q", m[1], r.status, r.header.Get("Content-Type"), r.header.Get("Cache-Control"))
		}
		disk, err := os.ReadFile(filepath.Join("assets", name))
		if err != nil || r.body != string(disk) {
			t.Errorf("%s is not the file in assets/ (%v)", name, err)
		}
		// Without the version it may be any binary's: the browser asks again.
		if r := f.get("/assets/" + name); r.status != 200 || r.header.Get("Cache-Control") != "no-cache" {
			t.Errorf("/assets/%s unversioned: status %d, cache %q", name, r.status, r.header.Get("Cache-Control"))
		}
		if r := f.get(m[1], "If-None-Match", r.header.Get("ETag")); r.status != http.StatusNotModified {
			t.Errorf("%s with its ETag: status %d, want 304", name, r.status)
		}
	}
}

// The vendored scripts are the files assets/README.md says they are.
func TestVendoredAssets(t *testing.T) {
	t.Parallel()
	readme, err := os.ReadFile("assets/README.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile("(?m)^\\| `([^`]+\\.js)` \\|.*\\| `([0-9a-f]{64})` \\|$").FindAllStringSubmatch(string(readme), -1)
	if len(rows) != 2 {
		t.Fatalf("the README pins %d scripts, want 2", len(rows))
	}
	for _, row := range rows {
		body, err := os.ReadFile(filepath.Join("assets", row[1]))
		if err != nil {
			t.Error(err)
			continue
		}
		if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != row[2] {
			t.Errorf("%s: sha256 %x, the README says %s", row[1], sum, row[2])
		}
	}
	scripts, _ := filepath.Glob("assets/*.js")
	// yard.js is ours, not a vendored script: the two the README pins
	// and it are all that may be here.
	if len(scripts) != 3 {
		t.Errorf("assets/ has %d scripts, want the two the README pins and yard.js: %v", len(scripts), scripts)
	}
}

// The stylesheet carries the yardr design tokens used by the web view.
func TestTokens(t *testing.T) {
	t.Parallel()
	css, err := os.ReadFile("assets/yard.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(css), "@media (prefers-color-scheme: light)") {
		t.Error("a light theme, which the view does not have")
	}
	if !strings.Contains(string(css), "color-scheme: dark;") {
		t.Error("the view's colour scheme is not dark")
	}
	tokens := map[string]string{
		"ground": "#161718", "panel": "#1c1d1f", "hairline": "#27282b", "hairline-strong": "#333538",
		"ink": "#e9e9ea", "dim": "#abadb0", "faint": "#808286",
		"accent": "#d3602f", "accent-hover": "#e2794a", "landed": "var(--faint)",
		"shade":   "rgba(16, 17, 18, 0.45)",
		"working": "var(--accent)", "running": "#4cc38a", "held": "#e5675a",
		"late": "#e8a33d", "done": "var(--faint)",
	}
	for name, value := range tokens {
		if !strings.Contains(string(css), "--"+name+": "+value+";") {
			t.Errorf("token --%s is not %s", name, value)
		}
	}
	for name, source := range map[string]string{
		"ground": "--surface-app", "panel": "--surface-raised", "hairline": "--border-subtle",
		"hairline-strong": "--border-strong", "ink": "--text-primary", "dim": "--text-secondary",
		"faint": "--text-disabled", "accent": "--yard-500", "accent-hover": "--yard-400", "pill": "--yard-600",
		"landed": "--text-disabled", "shade": "--dusk-950", "working": "--yard-500",
		"running": "--signal-green", "held": "--signal-red", "done": "--text-disabled",
	} {
		line := regexp.MustCompile(`(?m)^\s*--` + regexp.QuoteMeta(name) + `: [^;]+; /\* ` + regexp.QuoteMeta(source) + ` \*/$`)
		if !line.Match(css) {
			t.Errorf("token --%s does not name its design source %s", name, source)
		}
	}
	for name, stack := range map[string]string{
		"mono": `"IBM Plex Mono", ui-monospace, SFMono-Regular, Menlo, monospace`,
		"sans": `"IBM Plex Sans", system-ui, sans-serif`,
	} {
		if !strings.Contains(string(css), "--"+name+": "+stack+";") {
			t.Errorf("font token --%s is not %s", name, stack)
		}
	}
	for _, selector := range []string{".panel-svg .block.stalled", ".panel-svg .block.blocked"} {
		if strings.Contains(cssRule(t, selector), "var(--running)") {
			t.Errorf("%s uses the running green", selector)
		}
	}
	if !strings.Contains(cssRule(t, ".panel-svg .block.running"), "var(--running)") {
		t.Error("a running bead does not use the running green")
	}
	// Square corners everywhere; circles only for status dots and the
	// rail's chip, tiles and pills, which keep footplate's rounded
	// corners.
	radii := regexp.MustCompile(`(?m)^(.*)border-radius:\s*([^;]+);`).FindAllStringSubmatch(string(css), -1)
	for _, r := range radii {
		before := strings.TrimSpace(r[1])
		round := strings.HasPrefix(before, ".dot ") || strings.HasPrefix(before, ".chip ") ||
			strings.HasPrefix(before, ".tile ") || strings.HasPrefix(before, ".pill ")
		if r[2] != "0" && !round {
			t.Errorf("a corner that is not square: %s", r[0])
		}
	}
}

// The rule of one selector in the stylesheet, as its declarations.
func cssRule(t *testing.T, selector string) string {
	t.Helper()
	css, err := os.ReadFile("assets/yard.css")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(selector) + ` \{([^}]*)\}`).FindSubmatch(css)
	if m == nil {
		t.Fatalf("the stylesheet has no rule for %s", selector)
	}
	return string(m[1])
}

// A flow names its stage .stage: the flow's rule (a border on the left, a
// grid) is the flow's alone, under .column, and no bare .stage rule draws
// it beside anything else that takes the name.
func TestStageRuleIsTheFlows(t *testing.T) {
	t.Parallel()
	css, err := os.ReadFile("assets/yard.css")
	if err != nil {
		t.Fatal(err)
	}
	if bare := regexp.MustCompile(`(?m)(^|,\s*)\.stage\s*[{,]`).Find(css); bare != nil {
		t.Errorf("a rule for every .stage: %q", bare)
	}
	if rule := cssRule(t, ".column > .stage"); !strings.Contains(rule, "border-left:") {
		t.Errorf("a flow's stage has no rule on its left: %s", rule)
	}
}

// A stage file keeps the lines it was written in and scrolls inside its
// block: wrapped again at the column's width its lines end ragged and lose
// their indent. What a person wrote in a body or a note still wraps.
func TestStageFileKeepsItsLines(t *testing.T) {
	t.Parallel()
	file := cssRule(t, ".text.file")
	for _, want := range []string{"white-space: pre;", "overflow-x: auto;"} {
		if !strings.Contains(file, want) {
			t.Errorf(".text.file lacks %s: %s", want, file)
		}
	}
	if text := cssRule(t, ".text"); !strings.Contains(text, "white-space: pre-wrap;") {
		t.Errorf(".text does not wrap: %s", text)
	}
}

package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

func lineJSON(t *testing.T, f *fixture) web.Line {
	t.Helper()
	r := f.get("/line", "Accept", "application/json")
	var l web.Line
	if err := json.Unmarshal([]byte(r.body), &l); err != nil || r.status != 200 {
		t.Fatalf("/line: status %d, %v: %s", r.status, err, r.body)
	}
	return l
}

// waiting is what waits without its times, which no test can name.
func waiting(l web.Line) []web.Waiting {
	out := []web.Waiting{}
	for _, w := range l.Waits {
		if w.Since.IsZero() {
			w.Title += " (no since)"
		}
		w.Since = time.Time{}
		out = append(out, w)
	}
	return out
}

// A held bead waits for a person, with who held it and the reason they
// gave; unholding it takes it off again. The right column of every page
// shows it.
func TestWaitsHeld(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutRig(ctx, store.Rig{Name: "line", Base: "origin/main"}, "me"))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Rig: "line"}, "me")
	must(t, err)
	_, err = f.st.CreateBead(ctx, store.NewBead{Title: "a window", Stage: "new", Rig: "line"}, "me")
	must(t, err)
	if got := waiting(lineJSON(t, f)); len(got) != 0 {
		t.Fatalf("nothing is held, and these wait: %+v", got)
	}
	if text := squash(f.get("/").body); !strings.Contains(text, `<p class="faint">Nothing waits for you.</p>`) {
		t.Error("the column of a yard where nothing waits does not say so")
	}

	must(t, f.st.AddNote(ctx, b.ID, "the hinge is <missing>", "builder"))
	_, err = f.st.SetHold(ctx, b.ID, true, store.Expect{}, "builder")
	must(t, err)
	want := []web.Waiting{{Bead: b.ID, Title: "a door", Rig: "line", Stage: "new",
		Why: []web.Reason{{Kind: "held", Text: "held by builder", Note: "the hinge is <missing>"}}}}
	if got := waiting(lineJSON(t, f)); !reflect.DeepEqual(got, want) {
		t.Errorf("held\n got %+v\nwant %+v", got, want)
	}
	for _, path := range []string{"/", "/line", "/sessions", "/residents", "/beads", "/nowhere"} {
		text := squash(f.get(path).body)
		for _, part := range []string{
			`<article class="wait"><header><span class="bead">` + b.ID + `</span><span class="label">new</span></header>`,
			`<p class="wait-title">a door</p><p class="why held">held by builder</p><p class="entry-note">the hinge is &lt;missing&gt;</p>`,
			`<dt><span class="dot held"></span>held</dt><dd>1</dd>`,
		} {
			if !strings.Contains(text, part) {
				t.Errorf("%s lacks %q", path, part)
			}
		}
	}

	// A hold with no note of its holder right before it has no reason to
	// show: someone else's note is not one.
	_, err = f.st.SetHold(ctx, b.ID, false, store.Expect{}, "me")
	must(t, err)
	if got := waiting(lineJSON(t, f)); len(got) != 0 {
		t.Errorf("the bead is no longer held, and these wait: %+v", got)
	}
	_, err = f.st.SetHold(ctx, b.ID, true, store.Expect{}, "me")
	must(t, err)
	want[0].Why = []web.Reason{{Kind: "held", Text: "held by me"}}
	if got := waiting(lineJSON(t, f)); !reflect.DeepEqual(got, want) {
		t.Errorf("held again\n got %+v\nwant %+v", got, want)
	}
}

// A bead in a stage routed to a manual group waits for that group, claimed
// or not; one routed to an agent's group does not.
func TestWaitsManual(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutRig(ctx, store.Rig{Name: "line", Base: "origin/main"}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"anna", "ben"}}))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake"}))
	_, err := f.st.AddRoute(ctx, store.Route{Stage: "decide", Group: "decisions"})
	must(t, err)
	_, err = f.st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"})
	must(t, err)
	_, err = f.st.CreateBead(ctx, store.NewBead{Title: "built by an agent", Stage: "new", Rig: "line"}, "me")
	must(t, err)
	unclaimed, err := f.st.CreateBead(ctx, store.NewBead{Title: "which colour", Stage: "decide", Rig: "line"}, "me")
	must(t, err)
	claimed, err := f.st.CreateBead(ctx, store.NewBead{Title: "which size", Stage: "decide", Rig: "line"}, "me")
	must(t, err)
	sess, err := f.st.Claim(ctx, claimed.ID, "decisions", "decide", "yard")
	must(t, err)
	must(t, f.st.MarkRunning(ctx, sess.ID, map[string]any{"manual": true}))

	why := []web.Reason{{Kind: "manual", Text: "waits for decisions (anna, ben) in decide"}}
	// The one that began to wait last comes first.
	want := []web.Waiting{
		{Bead: claimed.ID, Title: "which size", Rig: "line", Stage: "decide", Why: why},
		{Bead: unclaimed.ID, Title: "which colour", Rig: "line", Stage: "decide", Why: why},
	}
	l := lineJSON(t, f)
	if got := waiting(l); !reflect.DeepEqual(got, want) {
		t.Errorf("manual\n got %+v\nwant %+v", got, want)
	}
	if l.Sessions != 1 || l.Working != 1 {
		t.Errorf("the line has %d sessions and %d working, want 1 and 1", l.Sessions, l.Working)
	}
	if text := squash(f.get("/").body); !strings.Contains(text, `<p class="why">waits for decisions (anna, ben) in decide</p>`) ||
		!strings.Contains(text, `<dt><span class="dot running"></span>sessions</dt><dd>1</dd>`) ||
		!strings.Contains(text, `<li><span>decide</span><span class="num">2</span></li>`) {
		t.Errorf("the column lacks the manual wait, the session count or the stage's count: %s", text)
	}
}

// blockedYard has one bead with a running session of an agent.
func blockedYard(t *testing.T, f *fixture) (*store.Bead, *store.Session) {
	t.Helper()
	ctx := context.Background()
	must(t, f.st.PutRig(ctx, store.Rig{Name: "line", Base: "origin/main"}, "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Rig: "line"}, "me")
	must(t, err)
	sess, err := f.st.Claim(ctx, b.ID, "builders", "new", "yard")
	must(t, err)
	must(t, f.st.MarkRunning(ctx, sess.ID, map[string]any{"agent": "door-new"}))
	return b, sess
}

// A bead whose session the dispatcher reported blocked on a prompt waits
// for a person until the dispatcher reports it unblocked. Another
// session's report does not count.
func TestWaitsBlocked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	b, sess := blockedYard(t, f)
	must(t, f.st.LogDispatch(ctx, b.ID, "session_blocked", "yard", map[string]any{"session": "another"}))
	if got := waiting(lineJSON(t, f)); len(got) != 0 {
		t.Fatalf("a running session waits for nobody, and these wait: %+v", got)
	}
	must(t, f.st.LogDispatch(ctx, b.ID, "session_blocked", "yard", map[string]any{"session": sess.ID}))
	want := []web.Waiting{{Bead: b.ID, Title: "a door", Rig: "line", Stage: "new",
		Why: []web.Reason{{Kind: "blocked", Text: "its session waits on a prompt: door-new"}}}}
	if got := waiting(lineJSON(t, f)); !reflect.DeepEqual(got, want) {
		t.Errorf("blocked\n got %+v\nwant %+v", got, want)
	}
	if text := squash(f.get("/").body); !strings.Contains(text, `<p class="why held">its session waits on a prompt: door-new</p>`) {
		t.Errorf("the column lacks the blocked session: %s", text)
	}
	must(t, f.st.LogDispatch(ctx, b.ID, "session_unblocked", "yard", map[string]any{"session": sess.ID}))
	if got := waiting(lineJSON(t, f)); len(got) != 0 {
		t.Errorf("the session is unblocked, and these wait: %+v", got)
	}
}

// strandedStore says one bead is stranded, or fails to say.
type strandedStore struct {
	store.API
	bead string
	err  error
}

func (s strandedStore) Stranded(context.Context, string) ([]store.StrandedBead, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []store.StrandedBead{{Bead: s.bead, Stage: "limbo", Flow: "code"}}, nil
}

// A bead stranded in a stage its flow lacks waits for a person. When the
// yard cannot say which are stranded, the column still shows the rest.
func TestWaitsStranded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutRig(ctx, store.Rig{Name: "line", Base: "origin/main"}, "me"))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "limbo", Rig: "line"}, "me")
	must(t, err)
	held, err := f.st.CreateBead(ctx, store.NewBead{Title: "a window", Stage: "new", Rig: "line"}, "me")
	must(t, err)
	_, err = f.st.SetHold(ctx, held.ID, true, store.Expect{}, "me")
	must(t, err)
	read := func(st store.API) web.Line {
		t.Helper()
		srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
		must(t, err)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "http://localhost/line", nil)
		req.Header.Set("Accept", "application/json")
		srv.ServeHTTP(rec, req)
		var l web.Line
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil || rec.Code != 200 {
			t.Fatalf("status %d, %v: %s", rec.Code, err, rec.Body)
		}
		return l
	}
	heldWait := web.Waiting{Bead: held.ID, Title: "a window", Rig: "line", Stage: "new",
		Why: []web.Reason{{Kind: "held", Text: "held by me"}}}
	want := []web.Waiting{heldWait, {Bead: b.ID, Title: "a door", Rig: "line", Stage: "limbo",
		Why: []web.Reason{{Kind: "stranded", Text: "stranded in limbo, a stage flow code lacks"}}}}
	if got := waiting(read(strandedStore{API: f.st, bead: b.ID})); !reflect.DeepEqual(got, want) {
		t.Errorf("stranded\n got %+v\nwant %+v", got, want)
	}
	if got := waiting(read(strandedStore{API: f.st, err: errors.New("repository is unavailable")})); !reflect.DeepEqual(got, []web.Waiting{heldWait}) {
		t.Errorf("with no word on the stranded\n got %+v\nwant the held bead alone", got)
	}
}

// Every page carries one stream, starting after the yard's head, and the
// right column reads itself again on each of its events: that is what
// makes the column live. The line page does the same for its own copy.
func TestColumnIsLive(t *testing.T) {
	f := newFixture(t)
	f.populate()
	head, err := f.st.Head(context.Background())
	must(t, err)
	// A page says what its stream carries besides the unnamed message per
	// event the column hears.
	formats := map[string]string{"/flows": "&amp;format=card", "/beads": "&amp;format=seq", "/beads/" + f.byTitle()["three"].ID: "&amp;format=seq"}
	column := `<aside class="right" aria-label="The line" hx-get="/line" hx-trigger="sse:message" hx-select="aside.right > *" hx-swap="innerHTML ignoreTitle:true">`
	for _, path := range []string{"/", "/line", "/sessions", "/residents", "/flows", "/beads", "/beads/" + f.byTitle()["three"].ID, "/nowhere"} {
		text := squash(f.get(path).body)
		shell := `<div class="shell" hx-ext="sse" sse-connect="/events?after=` + itoa(head) + formats[path] + `">`
		for _, part := range []string{shell, column} {
			if !strings.Contains(text, part) {
				t.Errorf("%s lacks %q", path, part)
			}
		}
		if n := strings.Count(text, "sse-connect"); n != 1 {
			t.Errorf("%s opens %d streams, want one: a browser allows a host few connections", path, n)
		}
	}
	for path, part := range map[string]string{
		"/line":      `<div class="item" id="line" hx-get="/line" hx-trigger="sse:message" hx-select="#line > *" hx-swap="innerHTML ignoreTitle:true">`,
		"/sessions":  `<div id="live" hx-get="/sessions" hx-trigger="sse:message" hx-select="#live > *" hx-swap="innerHTML ignoreTitle:true">`,
		"/residents": `<div id="live" hx-get="/residents" hx-trigger="sse:message" hx-select="#live > *" hx-swap="innerHTML ignoreTitle:true">`,
	} {
		if text := squash(f.get(path).body); !strings.Contains(text, part) {
			t.Errorf("%s lacks %q", path, part)
		}
	}
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// The sessions page lists the running sessions with their bead, stage,
// group, runner, state and age, as JSON and as the page; an ended session
// is not on it.
func TestSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var empty web.Sessions
	r := f.get("/sessions", "Accept", "application/json")
	if err := json.Unmarshal([]byte(r.body), &empty); err != nil || r.status != 200 || empty.Sessions == nil || len(empty.Sessions) != 0 {
		t.Fatalf("an empty yard: status %d, %v, %+v, want an empty list", r.status, err, empty)
	}
	if text := f.get("/sessions").body; !strings.Contains(text, "No session is running.") {
		t.Error("the page of a yard with no session does not say so")
	}

	b, sess := blockedYard(t, f)
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual"}))
	other, err := f.st.CreateBead(ctx, store.NewBead{Title: "which <colour>", Stage: "decide", Rig: "line"}, "me")
	must(t, err)
	manual, err := f.st.Claim(ctx, other.ID, "decisions", "decide", "yard")
	must(t, err)
	must(t, f.st.MarkRunning(ctx, manual.ID, map[string]any{"manual": true}))
	gone, err := f.st.CreateBead(ctx, store.NewBead{Title: "ended", Stage: "new", Rig: "line"}, "me")
	must(t, err)
	ended, err := f.st.Claim(ctx, gone.ID, "builders", "new", "yard")
	must(t, err)
	must(t, f.st.EndSession(ctx, ended.ID, "died", "the pane closed"))

	read := func() web.Sessions {
		t.Helper()
		var got web.Sessions
		r := f.get("/sessions", "Accept", "application/json")
		if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 200 {
			t.Fatalf("status %d, %v: %s", r.status, err, r.body)
		}
		for i := range got.Sessions {
			s := &got.Sessions[i]
			if s.StartedAt.IsZero() || s.Age == "" {
				t.Errorf("session %s has no start or age: %+v", s.ID, s)
			}
			s.StartedAt, s.Age = time.Time{}, ""
		}
		return got
	}
	want := web.Sessions{Sessions: []web.SessionRow{
		{ID: sess.ID, Bead: b.ID, Title: "a door", Stage: "new", Group: "builders", Runner: "fake door-new", State: "running"},
		{ID: manual.ID, Bead: other.ID, Title: "which <colour>", Stage: "decide", Group: "decisions", Runner: "manual", State: "running"},
	}}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Errorf("sessions\n got %+v\nwant %+v", got, want)
	}
	text := squash(f.get("/sessions").body)
	for _, part := range []string{
		`<span class="state"><span class="dot running"></span>running</span><span class="bead">` + b.ID + `</span>`,
		`<span class="state">running</span><span class="bead">` + other.ID + `</span>`,
		`<p class="entry-title">which &lt;colour&gt;</p>`,
		`<dt>stage</dt><dd>new</dd>`, `<dt>group</dt><dd>builders</dd>`, `<dt>runner</dt><dd>fake door-new</dd>`,
		`<dt>session</dt><dd>` + sess.ID + `</dd>`,
		`<a href="/sessions" aria-current="page">Sessions</a>`,
	} {
		if !strings.Contains(text, part) {
			t.Errorf("the page lacks %q", part)
		}
	}
	if strings.Contains(text, gone.ID) || strings.Contains(text, "No session is running.") {
		t.Error("the page shows the ended session, or says none runs")
	}

	// A session blocked on a prompt says so.
	must(t, f.st.LogDispatch(ctx, b.ID, "session_blocked", "yard", map[string]any{"session": sess.ID}))
	want.Sessions[0].Blocked = true
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Errorf("blocked\n got %+v\nwant %+v", got, want)
	}
	if text := squash(f.get("/sessions").body); !strings.Contains(text,
		`<span class="state"><span class="dot held"></span><span class="held">blocked</span></span>`) {
		t.Error("the page does not show the blocked session as blocked")
	}
}

// How long ago, in its two largest units.
func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second:                    "0s",
		42 * time.Second:                "42s",
		12*time.Minute + 3*time.Second:  "12m",
		3 * time.Hour:                   "3h",
		3*time.Hour + 4*time.Minute:     "3h 4m",
		48 * time.Hour:                  "2d",
		51*time.Hour + 59*time.Minute:   "2d 3h",
		59*time.Second + time.Second/2:  "1m",
		59*time.Minute + 59*time.Second: "59m",
	} {
		if got := web.Age(d); got != want {
			t.Errorf("age(%v) = %q, want %q", d, got, want)
		}
	}
}

// The residents page lists each resident, whether it runs and since when,
// and how many events of its inbox it has not read. What its journal says
// is not on it.
func TestResidents(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var empty web.Residents
	r := f.get("/residents", "Accept", "application/json")
	if err := json.Unmarshal([]byte(r.body), &empty); err != nil || r.status != 200 || empty.Residents == nil || len(empty.Residents) != 0 {
		t.Fatalf("an empty yard: status %d, %v, %+v, want an empty list", r.status, err, empty)
	}
	if text := f.get("/residents").body; !strings.Contains(text, "This yard has no resident.") {
		t.Error("the page of a yard with no resident does not say so")
	}

	for _, name := range []string{"mayor", "night"} {
		_, err := f.st.PutResident(ctx, store.Resident{Name: name, Runner: "herdr", Enabled: true,
			Config: map[string]any{"kind": "claude", "model": "opus"}}, "me")
		must(t, err)
	}
	must(t, f.st.ResidentStarted(ctx, "mayor", map[string]any{"agent": "mayor"}))
	must(t, f.st.Mail(ctx, "mayor", "the gate is red", "builder"))
	must(t, f.st.Mail(ctx, "mayor", "and still red", "builder"))
	must(t, f.st.Journal(ctx, "mayor", store.NoteJournal, "dear diary", "mayor"))
	must(t, f.st.SetResidentEnabled(ctx, "night", false, "", "me"))

	var got web.Residents
	r = f.get("/residents", "Accept", "application/json")
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 200 {
		t.Fatalf("status %d, %v: %s", r.status, err, r.body)
	}
	if len(got.Residents) != 2 || got.Residents[0].Since == nil || got.Residents[0].Up == "" {
		t.Fatalf("got %+v, want two residents, the first running since some time", got)
	}
	got.Residents[0].Since, got.Residents[0].Up = nil, ""
	want := web.Residents{Residents: []web.ResidentRow{
		{Name: "mayor", State: "running", Running: true, Enabled: true, Agent: "herdr · claude · opus", Unread: 2},
		{Name: "night", State: "stopped", Agent: "herdr · claude · opus"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("residents\n got %+v\nwant %+v", got, want)
	}
	text := squash(f.get("/residents").body)
	for _, part := range []string{
		`<h2>mayor</h2><span class="state"><span class="dot running"></span><span>running</span></span><span class="item-count">2 unread</span>`,
		`<h2>night</h2><span class="state"><span>stopped</span> · disabled</span><span class="item-count"><span class="zero">0 unread</span></span>`,
		`<dt>agent</dt><dd>herdr · claude · opus</dd>`,
		`<dt>running since</dt><dd><time datetime="`,
		`<a href="/residents" aria-current="page">Residents</a>`,
	} {
		if !strings.Contains(text, part) {
			t.Errorf("the page lacks %q", part)
		}
	}
	if strings.Count(text, "running since") != 1 {
		t.Error("the stopped resident is shown running since some time")
	}
	for _, not := range []string{"dear diary", "the gate is red"} {
		if strings.Contains(text, not) || strings.Contains(r.body, not) {
			t.Errorf("the page shows %q: a resident's journal and mail are its own", not)
		}
	}

	// Reading the inbox takes the count down.
	page, err := f.st.Inbox(ctx, "mayor", 0)
	must(t, err)
	must(t, f.st.AckInbox(ctx, "mayor", page.Last))
	if text := squash(f.get("/residents").body); !strings.Contains(text, `<h2>mayor</h2>`) || strings.Contains(text, "2 unread") {
		t.Error("the mayor read its inbox, and the page still counts 2 unread")
	}
}

// busyStore is a yard in which something happens while a page reads it:
// each time the rigs are listed, a bead gets a note.
type busyStore struct {
	store.API
	bead string
}

func (s busyStore) Rigs(ctx context.Context) ([]store.Rig, error) {
	if err := s.API.AddNote(ctx, s.bead, "meanwhile", "me"); err != nil {
		return nil, err
	}
	return s.API.Rigs(ctx)
}

// A page's stream starts after the head its handler read before it read
// the yard, not after one read once the page is built: an event that
// happens in between is in neither the page nor a stream that starts
// later, and a card that moved then would stay where it was.
func TestStreamStartsBeforeThePageWasRead(t *testing.T) {
	f := newFixture(t)
	f.populate()
	ctx := context.Background()
	srv, err := web.NewServer(busyStore{API: f.st, bead: f.byTitle()["three"].ID}, f.home, nil)
	must(t, err)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	for _, path := range []string{"/flows", "/beads", "/sessions", "/residents"} {
		head, err := f.st.Head(ctx)
		must(t, err)
		res, err := http.Get(ts.URL + path)
		must(t, err)
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		must(t, err)
		after, err := f.st.Head(ctx)
		must(t, err)
		if after == head {
			t.Fatalf("%s: nothing happened while the page was read", path)
		}
		if want := `sse-connect="/events?after=` + itoa(head); !strings.Contains(string(body), want+`"`) && !strings.Contains(string(body), want+`&amp;`) {
			t.Errorf("%s: the stream does not start after %d, the head before the page was read (now %d)", path, head, after)
		}
	}
}

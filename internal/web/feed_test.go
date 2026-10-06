package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// landing gives the yard a rig with a flow, a resident with a journal, and
// one bead that goes the whole way: created, held with a reason, unheld,
// moved with an outcome, landed with a commit. It returns the bead.
func landing(t *testing.T, st store.API) *store.Bead {
	t.Helper()
	ctx := context.Background()
	must(t, st.PutRig(ctx, store.Rig{Name: "line", Base: "origin/main"}, "me"))
	fl, err := store.ParseFlow("new -> review on done\nreview -> merged\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, st.SetFlowPointer(ctx, "line", "", "code", "me"))
	_, err = st.PutResident(ctx, store.Resident{Name: "yardmaster", Runner: "herdr"}, "me")
	must(t, err)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Rig: "line"}, "me")
	must(t, err)
	must(t, st.AddNote(ctx, b.ID, "someone else's remark", "passer"))
	must(t, st.Journal(ctx, "yardmaster", store.NoteJournal, "dear diary", "yardmaster"))
	must(t, st.AddNote(ctx, b.ID, "the hinge is missing", "builder"))
	_, err = st.SetHold(ctx, b.ID, true, store.Expect{}, "builder")
	must(t, err)
	_, err = st.SetHold(ctx, b.ID, false, store.Expect{}, "me")
	must(t, err)
	_, err = st.Done(ctx, b.ID, "done", "", store.Expect{}, "builder")
	must(t, err)
	must(t, st.Journal(ctx, "yardmaster", store.NoteHandoff, "over to you", "yardmaster"))
	_, err = st.AdvanceNote(ctx, b.ID, "merged", "refinery: merged 70dabcd", store.Expect{}, "refinery")
	must(t, err)
	return b
}

func homeJSON(t *testing.T, f *fixture, target string) web.Home {
	t.Helper()
	r := f.get(target, "Accept", "application/json")
	if r.status != 200 {
		t.Fatalf("%s: status %d: %s", target, r.status, r.body)
	}
	var h web.Home
	if err := json.Unmarshal([]byte(r.body), &h); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	return h
}

// The feed says each event in the yard's words, newest first: a move with
// its outcome, a hold with its reason, a landing with its commit. A
// resident's journal entries are left out.
func TestFeedWords(t *testing.T) {
	f := newFixture(t)
	b := landing(t, f.st)
	h := homeJSON(t, f, "/")
	type line struct{ actor, text, note, tone string }
	var got []line
	for i, e := range h.Events {
		if i > 0 && e.Seq >= h.Events[i-1].Seq {
			t.Errorf("entry %d has seq %d after %d: not newest first", i, e.Seq, h.Events[i-1].Seq)
		}
		if e.Kind == store.NoteJournal || e.Kind == store.NoteHandoff {
			t.Errorf("the feed holds the journal entry %d (%s)", e.Seq, e.Kind)
		}
		if e.Bead == b.ID {
			if e.Title != "a door" {
				t.Errorf("entry %d (%s) has title %q, want the bead's", e.Seq, e.Kind, e.Title)
			}
			got = append(got, line{e.Actor, e.Text, e.Note, e.Tone})
		}
	}
	want := []line{
		{"refinery", "landed in merged at 70dabcd", "refinery: merged 70dabcd", "landed"},
		{"refinery", "moved from review to merged", "refinery: merged 70dabcd", ""},
		{"refinery", "noted", "refinery: merged 70dabcd", ""},
		{"builder", "moved from new to review (done)", "", ""},
		{"me", "no longer held", "", ""},
		{"builder", "held", "the hinge is missing", "held"},
		{"builder", "noted", "the hinge is missing", ""},
		{"passer", "noted", "someone else's remark", ""},
		{"me", "created", "", ""},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the bead's entries\n got %q\nwant %q", got, want)
	}
	if h.Head != h.Events[0].Seq || h.Older != 0 || h.Before != 0 {
		t.Errorf("head %d older %d before %d, want the newest seq %d and no older page", h.Head, h.Older, h.Before, h.Events[0].Seq)
	}
	// The landing's commit is data of its own.
	if h.Events[0].Commit != "70dabcd" {
		t.Errorf("the landing's commit is %q, want 70dabcd", h.Events[0].Commit)
	}
}

// A close by hand is no landing, a hold with no note of the holder's has
// no reason, sessions start and end, and an event the feed has no words
// for is shown by its name with what it carries.
func TestFeedOtherWords(t *testing.T) {
	api, own := storetest.NewOwned(t)
	srv, err := web.NewServer(api, filepath.Join(t.TempDir(), ".depot"), nil)
	must(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	f := &fixture{t: t, st: api, ts: ts}
	ctx := context.Background()
	b, err := api.CreateBead(ctx, store.NewBead{Title: "a window", Stage: "new"}, "me")
	must(t, err)
	must(t, api.AddNote(ctx, b.ID, strings.Repeat("long ", 100), "passer"))
	_, err = api.SetHold(ctx, b.ID, true, store.Expect{}, "me")
	must(t, err)
	must(t, own.LogEvent(ctx, b.ID, "started", "yard", map[string]any{"session": "s1", "handle": map[string]any{"agent": "window-new"}}))
	must(t, own.LogEvent(ctx, b.ID, "session_failed", "yard", map[string]any{"session": "s1", "reason": "the pane died"}))
	must(t, api.LogDispatch(ctx, b.ID, "session_idle", "yard", map[string]any{"session": "s1"}))
	must(t, api.LogDispatch(ctx, b.ID, "session_harness_error", "yard", map[string]any{"session": "s1", "reason": "at_capacity",
		"line": "■ Selected model is at capacity.", "hint": "a long hint", "tail": "› \n■ Selected model is at capacity."}))
	must(t, api.LogDispatch(ctx, b.ID, "move_refused", "yard", map[string]any{"session": "s1", "reason": "not_allowed",
		"command": "yardr bead done " + b.ID + " --outcome shipped", "error": "flow has no outcome shipped", "hint": "a long hint"}))
	_, err = api.CloseBead(ctx, b.ID, "dropped", store.Expect{}, "me")
	must(t, err)
	must(t, api.SetSetting(ctx, "name", "north", "me"))

	h := homeJSON(t, f, "/")
	got := map[string]web.Entry{}
	for _, e := range h.Events {
		got[e.Kind] = e
	}
	for kind, want := range map[string]web.Entry{
		"held":           {Text: "held", Tone: "held"},
		"started":        {Text: "session started: window-new"},
		"session_failed": {Text: "session ended: failed", Note: "the pane died", Tone: "held"},
		"session_idle":   {Text: "session idle", Note: "session s1"},
		"session_harness_error": {Text: "harness stopped on an error: at capacity", Note: "■ Selected model is at capacity.",
			Tone: "held"},
		"move_refused": {Text: "move refused (not allowed): yardr bead done " + b.ID + " --outcome shipped",
			Note: "flow has no outcome shipped", Tone: "held"},
		"closed":          {Text: "closed: dropped"},
		"setting_changed": {Text: "setting changed", Note: "key name · value north"},
	} {
		e := got[kind]
		if e.Text != want.Text || e.Note != want.Note || e.Tone != want.Tone || e.Commit != "" {
			t.Errorf("%s: got %q, note %q, tone %q, commit %q; want %q, %q, %q and no commit",
				kind, e.Text, e.Note, e.Tone, e.Commit, want.Text, want.Note, want.Tone)
		}
	}
	if n := got["noted"].Note; len(n) > 300 || !strings.HasSuffix(n, "…") {
		t.Errorf("a 500-byte note is shown as %d bytes ending %q, want it cut short", len(n), n[max(0, len(n)-8):])
	}
}

// The first page holds the latest 50 entries and names the next; following
// the pages to the end shows every event once, newest first, the journal
// entries left out however many there are.
func TestFeedPages(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.st.PutResident(ctx, store.Resident{Name: "yardmaster", Runner: "herdr"}, "me")
	must(t, err)
	for i := range 130 {
		_, err := f.st.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("bead ", i), Stage: "new"}, "me")
		must(t, err)
		// A run of journal entries longer than one page of the feed.
		if i == 100 {
			for range 230 {
				must(t, f.st.Journal(ctx, "yardmaster", store.NoteJournal, "dear diary", "yardmaster"))
			}
		}
	}
	all, err := f.st.EventsSince(ctx, 0, 0)
	must(t, err)
	var want []uint64
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Kind != store.NoteJournal {
			want = append(want, all[i].Seq)
		}
	}
	var got []uint64
	target, pages := "/", 0
	for target != "" {
		h := homeJSON(t, f, target)
		pages++
		if len(h.Events) > 50 || (h.Older != 0 && len(h.Events) != 50) {
			t.Fatalf("%s holds %d entries and names %d as older, want 50 a page", target, len(h.Events), h.Older)
		}
		if h.Head != all[len(all)-1].Seq {
			t.Errorf("%s: head %d, want %d", target, h.Head, all[len(all)-1].Seq)
		}
		for _, e := range h.Events {
			got = append(got, e.Seq)
		}
		target = ""
		if h.Older != 0 {
			target = "/?before=" + fmt.Sprint(h.Older)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the pages hold\n%v\nwant\n%v", got, want)
	}
	if pages != 3 {
		t.Errorf("%d entries came in %d pages, want 3", len(got), pages)
	}
	if r := f.get("/?before=x"); r.status != 400 {
		t.Errorf("/?before=x: status %d, want 400", r.status)
	}
}

// The page is the same feed: each entry with its seq as id, the stream to
// continue it after the page's head, and the next page as a link that
// htmx follows when it scrolls into view. An older page's feed is not live.
func TestFeedHTML(t *testing.T) {
	f := newFixture(t)
	b := landing(t, f.st)
	ctx := context.Background()
	for i := range 60 {
		_, err := f.st.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("bead ", i), Stage: "new"}, "me")
		must(t, err)
	}
	h := homeJSON(t, f, "/")
	text := squash(f.get("/").body)
	for _, want := range []string{
		fmt.Sprintf(`<div class="shell" hx-ext="sse" sse-connect="/events?after=%d">`, h.Head),
		`<section class="feed" id="feed" aria-label="What happened" sse-swap="message" hx-swap="afterbegin">`,
		fmt.Sprintf(`<article class="item entry" id="e%d">`, h.Head),
		fmt.Sprintf(`<a class="item older label" href="/?before=%d" hx-get="/?before=%[1]d" hx-trigger="intersect once" hx-select="#feed > *" hx-target="this" hx-swap="outerHTML">Older</a>`, h.Older),
		"<h2>line</h2>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if n := strings.Count(text, `<article class="item entry"`); n != 50 {
		t.Errorf("the first page shows %d entries, want 50", n)
	}

	old := squash(f.get(fmt.Sprintf("/?before=%d", h.Older)).body)
	for _, want := range []string{
		`<section class="feed" id="feed" aria-label="What happened">`,
		`<a class="bead" href="/beads/` + b.ID + `">` + b.ID + `</a><span class="landed">landed in merged at 70dabcd</span>`,
		`<span class="held">held</span>`,
		`<p class="entry-title">a door</p>`,
		`<p class="entry-note">the hinge is missing</p>`,
		`<span class="label">refinery</span><time class="label" datetime="`,
	} {
		if !strings.Contains(old, want) {
			t.Errorf("the older page lacks %q", want)
		}
	}
	// The shell's stream is there for the right column; the feed takes
	// nothing from it.
	for _, not := range []string{"sse-swap", "<h2>line</h2>", "dear diary", `hx-get="/?before`} {
		if strings.Contains(old, not) {
			t.Errorf("the older page, the last, has %q", not)
		}
	}
}

// What an event says is text to the page, never markup.
func TestFeedEscapes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: `<script>alert(1)</script>`, Stage: "new"}, "me")
	must(t, err)
	must(t, f.st.AddNote(ctx, b.ID, "<img src=x>\n\ndata: second line", "me"))
	if body := f.get("/").body; strings.Contains(body, "<script>alert") || strings.Contains(body, "<img") {
		t.Error("the page carries an event's text as markup")
	}
	s := f.stream(t, "/events?after=0")
	for range 2 {
		fr := s.next(t)
		if strings.Contains(fr.data, "<script>alert") || strings.Contains(fr.data, "<img") {
			t.Errorf("the stream carries an event's text as markup: %s", fr.data)
		}
	}
}

// --- the stream ---------------------------------------------------------

// frame is what the stream sends for one event of the yard: the messages
// up to and including the one that carries the event's seq.
type frame struct {
	id uint64
	// data is the data of the message with the id.
	data string
	// msgs are the frame's messages that a browser would dispatch, in
	// order: all but the card stream send one at most.
	msgs []msg
}

type msg struct{ event, data string }

// stream is a client of /events.
type stream struct {
	frames chan frame
	cancel context.CancelFunc
	header http.Header
}

// stream opens target as an event stream; headers are name, value pairs.
func (f *fixture) stream(t *testing.T, target string, headers ...string) *stream {
	t.Helper()
	return openStream(t, f.ts.URL+target, headers...)
}

func openStream(t *testing.T, target string, headers ...string) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	must(t, err)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%s: status %d, type %q", target, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	s := &stream{frames: make(chan frame, 1024), cancel: cancel, header: resp.Header}
	go func() {
		defer resp.Body.Close()
		defer close(s.frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<24)
		var fr frame
		var data []string
		var event string
		var seen bool
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				fr.id, _ = strconv.ParseUint(line[4:], 10, 64)
				seen = true
			case strings.HasPrefix(line, "event: "):
				event = line[7:]
			case strings.HasPrefix(line, "data: "):
				data = append(data, line[6:])
			case line == "":
				// As a browser does: a message with no data is none, and
				// one with an id moves the cursor.
				if len(data) > 0 {
					fr.msgs = append(fr.msgs, msg{event, strings.Join(data, "\n")})
				}
				if seen {
					fr.data = strings.Join(data, "\n")
					s.frames <- fr
					fr, seen = frame{}, false
				}
				data, event = nil, ""
			}
		}
	}()
	return s
}

func (s *stream) next(t *testing.T) frame {
	t.Helper()
	select {
	case fr, ok := <-s.frames:
		if !ok {
			t.Fatal("the stream ended")
		}
		return fr
	case <-time.After(10 * time.Second):
		t.Fatal("no event in 10s")
	}
	return frame{}
}

// quiet fails if the stream carries anything more.
func (s *stream) quiet(t *testing.T) {
	t.Helper()
	select {
	case fr, ok := <-s.frames:
		if ok {
			t.Fatalf("the stream carried %d (%s), want nothing more", fr.id, fr.data)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func bead(t *testing.T, st store.API, title string) *store.Bead {
	t.Helper()
	b, err := st.CreateBead(context.Background(), store.NewBead{Title: title, Stage: "new"}, "me")
	must(t, err)
	return b
}

// /events?format=json carries every event as it is, its seq as the id:
// from ?after, from Last-Event-ID when both are given, and from now with
// neither.
func TestEventsJSON(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.st.PutResident(ctx, store.Resident{Name: "yardmaster", Runner: "herdr"}, "me")
	must(t, err)
	bead(t, f.st, "one")
	must(t, f.st.Journal(ctx, "yardmaster", store.NoteJournal, "dear diary", "yardmaster"))
	bead(t, f.st, "two")
	want, err := f.st.EventsSince(ctx, 0, 0)
	must(t, err)

	read := func(s *stream, from int) {
		t.Helper()
		for _, w := range want[from:] {
			fr := s.next(t)
			var e store.Event
			if err := json.Unmarshal([]byte(fr.data), &e); err != nil {
				t.Fatalf("%v: %q", err, fr.data)
			}
			if fr.id != w.Seq || e.Seq != w.Seq || e.Kind != w.Kind || e.Actor != w.Actor || !e.At.Equal(w.At) {
				t.Errorf("frame %d carries %+v, want %+v", fr.id, e, w)
			}
		}
	}
	all := f.stream(t, "/events?format=json&after=0")
	read(all, 0)
	if cc := all.header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", cc)
	}
	// An Accept that prefers JSON asks for the same.
	read(f.stream(t, "/events?after=2", "Accept", "application/json"), 2)
	// A reconnecting browser sends the URL it was given and where it got to.
	resumed := f.stream(t, "/events?format=json&after=0", "Last-Event-ID", fmt.Sprint(want[len(want)-2].Seq))
	read(resumed, len(want)-1)
	resumed.quiet(t)

	now := f.stream(t, "/events?format=json")
	now.quiet(t)
	b := bead(t, f.st, "three")
	if fr := now.next(t); !strings.Contains(fr.data, `"bead":"`+b.ID+`"`) || fr.id != want[len(want)-1].Seq+1 {
		t.Errorf("a stream from now carried %d (%s), want the bead created since", fr.id, fr.data)
	}

	for _, bad := range []string{"/events?after=x", "/events?format=xml"} {
		if r := f.get(bad); r.status != 400 {
			t.Errorf("%s: status %d, want 400", bad, r.status)
		}
	}
	if r := f.get("/events", "Last-Event-ID", "x"); r.status != 400 {
		t.Errorf("Last-Event-ID x: status %d, want 400", r.status)
	}
}

// By default the data is the feed's entry as an HTML fragment, the same
// markup the page has. An event the feed leaves out still comes, as an id
// with no data: the client's cursor passes it.
func TestEventsHTML(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.st.PutResident(ctx, store.Resident{Name: "yardmaster", Runner: "herdr"}, "me")
	must(t, err)
	head, err := f.st.Head(ctx)
	must(t, err)
	s := f.stream(t, fmt.Sprintf("/events?after=%d", head))
	b := bead(t, f.st, "a door")
	must(t, f.st.Journal(ctx, "yardmaster", store.NoteJournal, "dear diary", "yardmaster"))
	must(t, f.st.AddNote(ctx, b.ID, "two\nlines", "me"))

	fr := s.next(t)
	entry := regexp.MustCompile(`(?s)<article class="item entry" id="e\d+">.*?</article>`)
	if fr.id != head+1 || !strings.HasPrefix(fr.data, fmt.Sprintf(`<article class="item entry" id="e%d">`, fr.id)) ||
		!strings.Contains(fr.data, `<a class="bead" href="/beads/`+b.ID+`">`+b.ID+`</a> <span>created</span>`) ||
		!strings.Contains(fr.data, `<p class="entry-title">a door</p>`) {
		t.Errorf("frame %d: %s", fr.id, fr.data)
	}
	// The fragment is the page's own markup for the entry.
	if page := entry.FindString(f.get(fmt.Sprintf("/?before=%d", head+2)).body); page != fr.data {
		t.Errorf("the stream's entry\n%s\nis not the page's\n%s", fr.data, page)
	}
	if fr := s.next(t); fr.id != head+2 || fr.data != "" {
		t.Errorf("the journal entry came as %d with data %q, want its id alone", fr.id, fr.data)
	}
	if fr := s.next(t); fr.id != head+3 || !strings.Contains(fr.data, "<p class=\"entry-note\">two\nlines</p>") {
		t.Errorf("frame %d: %q, want the note with its two lines", fr.id, fr.data)
	}
}

// drain reads frames up to the one with id through and returns their ids.
func drain(t *testing.T, s *stream, through uint64) []uint64 {
	t.Helper()
	var ids []uint64
	for len(ids) == 0 || ids[len(ids)-1] < through {
		ids = append(ids, s.next(t).id)
	}
	return ids
}

// Stopping and starting `yardr web serve` while a page is open loses and
// repeats no event: the browser reconnects with the id of the last event
// it got, and the new server goes on after it, including what happened
// while none was up.
func TestStreamAcrossWebRestart(t *testing.T) {
	st := storetest.New(t)
	home := filepath.Join(t.TempDir(), ".depot")
	serve := func() *httptest.Server {
		srv, err := web.NewServer(st, home, nil)
		must(t, err)
		return httptest.NewServer(srv)
	}
	ctx := context.Background()
	bead(t, st, "before the page")
	first := serve()
	// The page's snapshot, and the stream it names.
	r, err := http.Get(first.URL + "/")
	must(t, err)
	r.Body.Close()
	head, err := st.Head(ctx)
	must(t, err)
	target := fmt.Sprintf("/events?after=%d", head)
	s := openStream(t, first.URL+target)
	for i := range 3 {
		bead(t, st, fmt.Sprint("while up ", i))
	}
	got := drain(t, s, head+3)

	// The server goes: the client loses its connection.
	first.CloseClientConnections()
	first.Close()
	if _, ok := <-s.frames; ok {
		t.Fatal("the stream outlived its server")
	}
	for i := range 4 {
		bead(t, st, fmt.Sprint("while down ", i))
	}
	second := serve()
	defer second.Close()
	// As the browser does: the same URL, with where it got to.
	s = openStream(t, second.URL+target, "Last-Event-ID", fmt.Sprint(got[len(got)-1]))
	for i := range 2 {
		bead(t, st, fmt.Sprint("up again ", i))
	}
	got = append(got, drain(t, s, head+9)...)
	s.quiet(t)
	s.cancel()

	var want []uint64
	for seq := head + 1; seq <= head+9; seq++ {
		want = append(want, seq)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("across the restart the page got events %v, want each of %v once", got, want)
	}
}

// flaky is a store whose subscriptions end early, as they do when the
// yardr server restarts: every one closes after two events, and every
// second attempt to subscribe fails.
type flaky struct {
	store.API
	calls atomic.Int64
}

func (f *flaky) Subscribe(ctx context.Context, after uint64) (<-chan store.Event, error) {
	if f.calls.Add(1)%2 == 0 {
		return nil, fmt.Errorf("yardr server: connection refused")
	}
	ctx, cancel := context.WithCancel(ctx)
	in, err := f.API.Subscribe(ctx, after)
	if err != nil {
		cancel()
		return nil, err
	}
	out := make(chan store.Event)
	go func() {
		defer close(out)
		defer cancel()
		for n := 0; n < 2; n++ {
			e, ok := <-in
			if !ok {
				return
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// A yardr server restart under the web view is ridden out inside the
// stream: the client's connection stays, and it gets every event once.
func TestStreamAcrossServerRestart(t *testing.T) {
	st := storetest.New(t)
	fl := &flaky{API: st}
	srv, err := web.NewServer(fl, filepath.Join(t.TempDir(), ".depot"), nil)
	must(t, err)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	for i := range 7 {
		bead(t, st, fmt.Sprint("bead ", i))
	}
	s := openStream(t, ts.URL+"/events?format=json&after=0")
	got := drain(t, s, 7)
	if fmt.Sprint(got) != "[1 2 3 4 5 6 7]" {
		t.Errorf("the stream carried %v, want 1 to 7 once each", got)
	}
	if fl.calls.Load() < 4 {
		t.Errorf("the store was subscribed to %d times: the stream was not interrupted", fl.calls.Load())
	}
	s.cancel()
}

// settle waits for cond, which a server's goroutines make true in their
// own time.
func settle(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A client that goes away takes its stream with it: no handler and no
// goroutine stays behind.
func TestGoneClients(t *testing.T) {
	st := storetest.New(t)
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	must(t, err)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	bead(t, st, "one")
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	// The goroutines of a yard at rest, to come back to.
	base := runtime.NumGoroutine()

	var streams []*stream
	for range 20 {
		s := openStream(t, ts.URL+"/events?after=0")
		s.next(t)
		streams = append(streams, s)
	}
	settle(t, "20 clients are not 20 streams", func() bool { return srv.Streams() == 20 })
	for _, s := range streams {
		s.cancel()
	}
	settle(t, fmt.Sprintf("%d streams outlived their clients", srv.Streams()), func() bool { return srv.Streams() == 0 })
	http.DefaultClient.CloseIdleConnections()
	settle(t, fmt.Sprintf("%d goroutines where there were %d before the clients came", runtime.NumGoroutine(), base),
		func() bool { return runtime.NumGoroutine() <= base })
}

// A client that takes nothing does not hold up the others, and is dropped
// when a write to it has waited long enough: its stream does not stay.
func TestSlowClient(t *testing.T) {
	st := storetest.New(t)
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	must(t, err)
	srv.SetStreamWrite(300 * time.Millisecond)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := context.Background()

	// The slow one: it asks, and never reads.
	slow, err := net.Dial("tcp", ts.Listener.Addr().String())
	must(t, err)
	defer slow.Close()
	_ = slow.(*net.TCPConn).SetReadBuffer(1024)
	_, err = fmt.Fprintf(slow, "GET /events?format=json&after=0 HTTP/1.1\r\nHost: %s\r\n\r\n", ts.Listener.Addr())
	must(t, err)
	quick := openStream(t, ts.URL+"/events?format=json&after=0")
	settle(t, "two clients are not two streams", func() bool { return srv.Streams() == 2 })

	// More than the connection's buffers hold: 48 MB.
	big := strings.Repeat("x", 1<<20)
	const n = 48
	start := time.Now()
	for range n {
		must(t, st.LogDispatch(ctx, "", "backoff", "me", map[string]any{"reason": big}))
	}
	for i := range n {
		if fr := quick.next(t); fr.id != uint64(i+1) || len(fr.data) < len(big) {
			t.Fatalf("the reading client got frame %d of %d bytes, want %d whole", fr.id, len(fr.data), i+1)
		}
	}
	t.Logf("the reading client had all %d events %s after the first was written", n, time.Since(start).Round(time.Millisecond))
	settle(t, "the client that reads nothing still has its stream", func() bool { return srv.Streams() == 1 })
	quick.cancel()
	settle(t, "a stream outlived its client", func() bool { return srv.Streams() == 0 })
}

// A timer is in the feed when it fires, once, in the yard's words: not
// when it is set, and not a second time as the mail a firing is to a user.
func TestFeedTimers(t *testing.T) {
	api, _ := storetest.NewOwned(t)
	srv, err := web.NewServer(api, filepath.Join(t.TempDir(), ".depot"), nil)
	must(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	f := &fixture{t: t, st: api, ts: ts}
	ctx := context.Background()
	_, err = api.AddUser(ctx, "jo", "me")
	must(t, err)
	_, err = api.PutResident(ctx, store.Resident{Name: "yardmaster", Runner: "herdr"}, "me")
	must(t, err)
	due := time.Now().Add(time.Hour)
	_, err = api.AddTimer(ctx, store.NewTimer{Reason: "look at the deploy", Due: due}, "yardmaster")
	must(t, err)
	_, err = api.AddTimer(ctx, store.NewTimer{To: "jo", Reason: "the refinery is red", Due: due}, "yardmaster")
	must(t, err)
	pass, err := api.FireTimers(ctx, due)
	must(t, err)
	if pass.Fired != 2 {
		t.Fatalf("pass = %+v", pass)
	}

	var texts []string
	for _, e := range homeJSON(t, f, "/").Events {
		switch e.Kind {
		case "timer":
			texts = append(texts, e.Text+": "+e.Note)
		case "timer_set", "mail":
			t.Errorf("the feed shows a %s: %+v", e.Kind, e)
		}
	}
	slices.Sort(texts)
	if len(texts) != 2 || !strings.HasPrefix(texts[0], "timer of yardmaster for jo: the refinery is red (set ") ||
		!strings.HasPrefix(texts[1], "timer of yardmaster: look at the deploy (set ") {
		t.Fatalf("timer entries = %q", texts)
	}
}

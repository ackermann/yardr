package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// talkFixture serves a yard as newFixture does, acting as the user as (""
// for a view that does not act), and hands back the server for AllowHosts
// and AllowNetwork.
func talkFixture(t *testing.T, as string) (*fixture, *web.Server) {
	t.Helper()
	st := storetest.New(t)
	home := filepath.Join(t.TempDir(), ".depot")
	srv, err := web.NewServer(st, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	if as != "" {
		token = actAs(t, srv, as)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: st, ts: ts, home: home}
	if as != "" {
		f.signIn(srv, token)
	}
	return f, srv
}

// postForm posts form to target, following no redirect, so a 303 is seen
// as one.
func (f *fixture) postForm(target string, form url.Values, headers ...string) resp {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+target, strings.NewReader(form.Encode()))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Add(headers[i], headers[i+1])
	}
	f.authorize(req)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

// talkYard is a yard with the user jo and the crew members yardmaster and
// scribe, and one mail each way between jo and the yardmaster.
func talkYard(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.st.AddUser(ctx, "jo", "jo")
	must(err)
	for _, name := range []string{"yardmaster", "scribe"} {
		_, err = f.st.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr"}, "jo")
		must(err)
	}
	must(f.st.Mail(ctx, "yardmaster", "paint the shed", "jo"))
	must(f.st.Mail(ctx, "jo", "it is painted", "yardmaster"))
}

// Without --as the talk page says the view does not act, and takes no
// write with the rest of the routes (TestReadOnly).
func TestTalkWithoutAs(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r := f.get("/talk")
	if r.status != 200 || !strings.Contains(r.body, "does not act") || strings.Contains(r.body, "<form") {
		t.Errorf("status %d, want the page that says the view does not act, with no form:\n%s", r.status, r.body)
	}
	if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'none'") {
		t.Errorf("Content-Security-Policy %q, want form-action 'none': no page has a form", csp)
	}
}

// With --as and no crew the page says whose command that is.
func TestTalkNoCrew(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	if _, err := f.st.AddUser(context.Background(), "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	r := f.get("/talk")
	if r.status != 200 || !strings.Contains(r.body, "has no crew") || !strings.Contains(r.body, "yardr crew add") {
		t.Errorf("status %d, want the page that says the yard has no crew:\n%s", r.status, r.body)
	}
}

// The conversation with the yardmaster, the form that writes to it, and
// the crew to talk to instead.
func TestTalk(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	r := f.get("/talk")
	for _, want := range []string{
		"paint the shed", "it is painted", // the letters, both directions
		"jo", "yardmaster", // who wrote each
		`<form id="send" method="post" action="/talk?with=yardmaster"`,
		`<textarea id="text" name="text"`,                 // yard.js sends on ctrl+Enter in this box
		`<span class="hint" id="send-key" hidden></span>`, // yard.js names the send key here
		`hx-post="/talk?with=yardmaster"`,
		`hx-get="/talk?with=yardmaster"`, // the newest page is live
		`maxlength="65536"`,
		`<a href="/talk?with=scribe">scribe</a>`,
		`<a href="/talk?with=yardmaster" aria-current="page">`, // the one in conversation
		`<a href="/talk" aria-current="page">`,                 // the Crew column's entry
		`class="dot unread"`,                                   // the yardmaster's mail to jo is unread
		`title="Crew — 1 unread letter" aria-label="Crew — 1 unread letter"`,
		`<span class="pill" aria-hidden="true">1</span>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
		}
	}
	// The conversation opens with its head, the party's name in the section
	// face, above the letters that scroll under it; each letter carries the
	// sender and the time yard.js reads again in the browser's zone.
	body := squash(r.body)
	head := strings.Index(body, `<header class="talk-head"><h2 class="section-head">yardmaster</h2>`)
	if letters := strings.Index(body, `<div id="letters">`); head < 0 || letters < head {
		t.Errorf("the head stands at %d and the letters at %d, want the head first:\n%s", head, letters, r.body)
	}
	for _, want := range []string{`data-from="jo"`, `data-from="yardmaster"`, `<time class="at" datetime="`} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the letters lack %q:\n%s", want, r.body)
		}
	}
	if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("Content-Security-Policy %q, want form-action 'self': the form posts to the view", csp)
	}
	// The other member's conversation, empty; one that is no member's, 404.
	r = f.get("/talk?with=scribe")
	if r.status != 200 || !strings.Contains(r.body, "No mail between jo and scribe yet.") {
		t.Errorf("/talk?with=scribe: status %d, want the empty conversation:\n%s", r.status, r.body)
	}
	if r := f.get("/talk?with=nosuch"); r.status != 404 {
		t.Errorf("/talk?with=nosuch: status %d, want 404", r.status)
	}
	if r := f.get("/talk?before=nope"); r.status != 400 {
		t.Errorf("/talk?before=nope: status %d, want 400", r.status)
	}
	// The aggregate counts mail from both senders. The scribe's unseen
	// letter is interleaved before the newest yardmaster letter; opening
	// that conversation must not acknowledge it.
	ctx := context.Background()
	must(t, f.st.Mail(ctx, "jo", "sweep the yard", "scribe"))
	must(t, f.st.Mail(ctx, "jo", "and oil the gate", "yardmaster"))
	r = f.get("/talk?with=yardmaster")
	if r.status != 200 || !strings.Contains(r.body, `title="Crew — 3 unread letters" aria-label="Crew — 3 unread letters"`) ||
		!strings.Contains(r.body, `<span class="pill" aria-hidden="true">3</span>`) {
		t.Errorf("opening the conversation changed the aggregate unread count: %s", r.body)
	}
	if !strings.Contains(r.body, `<a href="/talk?with=scribe"><span class="dot unread"`) {
		t.Errorf("the second sender's mark fell: %s", r.body)
	}
	box, err := f.st.UserInbox(ctx, "jo", 0, "jo")
	must(t, err)
	if box.Unread != 3 {
		t.Errorf("jo's unread is %d, want 3: opening the conversation acknowledged mail", box.Unread)
	}
	// Above nine the numeral caps, while the title and aria-label retain
	// the full count. An acknowledgement elsewhere changes the next render.
	for i := 0; i < 7; i++ {
		must(t, f.st.Mail(ctx, "jo", fmt.Sprintf("letter %d", i), "yardmaster"))
	}
	r = f.get("/talk?with=yardmaster")
	if !strings.Contains(r.body, `title="Crew — 10 unread letters" aria-label="Crew — 10 unread letters"`) ||
		!strings.Contains(r.body, `<span class="pill" aria-hidden="true">9&#43;</span>`) {
		t.Errorf("ten unread letters do not use the capped pill: %s", r.body)
	}
	box, err = f.st.UserInbox(ctx, "jo", 0, "jo")
	must(t, err)
	must(t, f.st.AckUserInbox(ctx, "jo", box.Mail[0].Seq, "jo"))
	r = f.get("/talk?with=yardmaster")
	if !strings.Contains(r.body, `title="Crew — 9 unread letters"`) ||
		!strings.Contains(r.body, `<span class="pill" aria-hidden="true">9</span>`) {
		t.Errorf("an acknowledgement elsewhere did not lower the next render: %s", r.body)
	}

	// The configured yardmaster wins even when another crew member comes
	// first; a name no crew member bears and the unset default keep the
	// first-crew fallback.
	defaults, _ := talkFixture(t, "jo")
	defaultCtx := context.Background()
	if _, err := defaults.st.AddUser(defaultCtx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"aaron", "mayor"} {
		if _, err := defaults.st.PutCrew(defaultCtx, store.CrewMember{Name: name, Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	defaultPartner := func() string {
		t.Helper()
		var got web.Talk
		defaults.getJSON("/talk", &got)
		return got.With
	}
	if err := defaults.st.SetSetting(defaultCtx, store.YardmasterSetting, "mayor", "jo"); err != nil {
		t.Fatal(err)
	}
	if got := defaultPartner(); got != "mayor" {
		t.Errorf("/talk with the yardmaster setting mayor opens on %q, want mayor", got)
	}
	if r := defaults.get("/talk"); r.status != http.StatusOK ||
		!strings.Contains(r.body, `<form id="send" method="post" action="/talk?with=mayor"`) {
		t.Errorf("the page's form is not to mayor (status %d):\n%s", r.status, r.body)
	}
	if err := defaults.st.SetSetting(defaultCtx, store.YardmasterSetting, "nosuch", "jo"); err != nil {
		t.Fatal(err)
	}
	if got := defaultPartner(); got != "aaron" {
		t.Errorf("/talk with the yardmaster setting naming no crew member opens on %q, want the first crew member", got)
	}
	if err := defaults.st.SetSetting(defaultCtx, store.YardmasterSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if got := defaultPartner(); got != "aaron" {
		t.Errorf("/talk with the yardmaster setting unset opens on %q, want the first crew member", got)
	}
	t.Run("grouped letters", func(t *testing.T) {
		f, _ := talkFixture(t, "jo")
		talkYard(t, f) // jo: paint the shed; the yardmaster: it is painted
		ctx := context.Background()
		must(t, f.st.Mail(ctx, "jo", "and the fence", "yardmaster"))
		must(t, f.st.Mail(ctx, "yardmaster", "thanks", "jo"))

		var got web.Talk
		f.getJSON("/talk?with=yardmaster", &got)
		if len(got.Letters) != 4 {
			t.Fatalf("%d letters, want 4: %+v", len(got.Letters), got.Letters)
		}
		for i, want := range []struct {
			grouped bool
			day     bool
		}{{false, true}, {false, false}, {true, false}, {false, false}} {
			if l := got.Letters[i]; l.Grouped != want.grouped || (l.Day != "") != want.day {
				t.Errorf("letter %d (%q): grouped %v, day %q, want grouped %v and a day %v", i+1, l.Text, l.Grouped, l.Day, want.grouped, want.day)
			}
		}

		var classes, from []string
		for _, m := range regexp.MustCompile(`<article class="([^"]*)" data-seq="\d+" data-from="([^"]*)"`).FindAllStringSubmatch(f.get("/talk?with=yardmaster").body, -1) {
			classes, from = append(classes, m[1]), append(from, m[2])
		}
		if want := []string{"letter head day own", "letter head", "letter", "letter head own"}; !reflect.DeepEqual(classes, want) {
			t.Errorf("the letters' classes %q, want %q", classes, want)
		}
		if want := []string{"jo", "yardmaster", "yardmaster", "jo"}; !reflect.DeepEqual(from, want) {
			t.Errorf("the letters' senders %q, want %q", from, want)
		}
	})
}

// The conversation as JSON: the user, the member, the letters oldest
// first, each saying whether its user wrote it.
func TestTalkJSON(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	r := f.get("/talk?with=yardmaster", "Accept", "application/json")
	var got web.Talk
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 200 {
		t.Fatalf("status %d (%v):\n%s", r.status, err, r.body)
	}
	if got.As != "jo" || got.With != "yardmaster" || len(got.Crew) != 2 || got.MaxMail != store.MaxMailParts*store.MaxMailBytes {
		t.Errorf("the page: %+v", got)
	}
	if len(got.Letters) != 2 || got.Letters[0].Text != "paint the shed" || !got.Letters[0].Own ||
		got.Letters[1].Text != "it is painted" || got.Letters[1].Own || got.More {
		t.Errorf("the letters: %+v", got.Letters)
	}
	if got.Crew[0].Name != "scribe" || got.Crew[0].Unread || got.Crew[1].Name != "yardmaster" || !got.Crew[1].Unread {
		t.Errorf("the crew: %+v", got.Crew)
	}
}

// Past the page's limit the oldest letters are an "Older mail" link away,
// on a page that is a snapshot: it does not read itself again.
func TestTalkOlder(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	ctx := context.Background()
	for i := 0; i < 52; i++ {
		if err := f.st.Mail(ctx, "yardmaster", "paint the shed", "jo"); err != nil {
			t.Fatal(err)
		}
	}
	r := f.get("/talk?with=yardmaster", "Accept", "application/json")
	var got web.Talk
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Letters) != 50 || !got.More {
		t.Fatalf("%d letters, more %v, want the newest 50 with more", len(got.Letters), got.More)
	}
	r = f.get("/talk?with=yardmaster")
	if !strings.Contains(r.body, "Older mail") || !strings.Contains(r.body, "before=") {
		t.Errorf("the newest page has no older-mail link:\n%s", r.body)
	}
	oldest := got.Letters[0].Seq
	r = f.get("/talk?with=yardmaster&before=" + strconv.FormatUint(oldest, 10))
	if r.status != 200 || strings.Contains(r.body, `hx-get="/talk`) || !strings.Contains(r.body, ">Newest<") {
		t.Errorf("the older page reads itself again or has no way back:\n%s", r.body)
	}
}

// Past the page's limit the talk page keeps the newest page live and reads
// the older ones upward: an "Older mail" link above the letters follows
// itself when it scrolls into view, putting each older page's letters in
// its place. The crew stands above the letters and the form below, the
// live letters below the earlier block in the pane, every block oldest
// first; an older page on its own is a snapshot with the way back.
func TestTalkPaging(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	ctx := context.Background()
	for i := 0; i < 102; i++ {
		if err := f.st.Mail(ctx, "yardmaster", "number "+strconv.Itoa(i), "jo"); err != nil {
			t.Fatal(err)
		}
	}
	// 104 letters: pages of 50, 50 and 4, newest last.
	page := func(before uint64) web.Talk {
		t.Helper()
		target := "/talk?with=yardmaster"
		if before != 0 {
			target += "&before=" + strconv.FormatUint(before, 10)
		}
		r := f.get(target, "Accept", "application/json")
		var got web.Talk
		if err := json.Unmarshal([]byte(r.body), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	newest := page(0)
	if len(newest.Letters) != 50 || !newest.More {
		t.Fatalf("newest page: %d letters, more %v", len(newest.Letters), newest.More)
	}
	middle := page(newest.Letters[0].Seq)
	if len(middle.Letters) != 50 || !middle.More {
		t.Fatalf("middle page: %d letters, more %v", len(middle.Letters), middle.More)
	}
	oldest := page(middle.Letters[0].Seq)
	if len(oldest.Letters) != 4 || oldest.More {
		t.Fatalf("oldest page: %d letters, more %v", len(oldest.Letters), oldest.More)
	}
	// The next page's link, which htmx follows on sight and puts that
	// page's earlier block in its place; without scripts it is a plain
	// link to the older page.
	link := func(before uint64) string {
		q := "/talk?with=yardmaster&amp;before=" + strconv.FormatUint(before, 10)
		return `<a class="item older label" href="` + q + `" hx-get="` + q +
			`" hx-trigger="intersect once" hx-select="#earlier > *" hx-target="this" hx-swap="outerHTML">Older mail</a>`
	}

	body := f.get("/talk?with=yardmaster").body
	peers := strings.Index(body, `<nav class="peers"`)
	letters := strings.Index(body, `<div id="letters">`)
	live := strings.Index(body, `<div id="live"`)
	earlier := strings.Index(body, `<div id="earlier">`)
	form := strings.Index(body, `<form id="send"`)
	if peers < 0 || letters < 0 || live < 0 || earlier < 0 || form < 0 ||
		!(peers < letters && letters < live && live < earlier && earlier < form) {
		t.Fatalf("peers %d, letters %d, live %d, earlier %d, form %d, want them in that order",
			peers, letters, live, earlier, form)
	}
	if !strings.Contains(body, link(newest.Letters[0].Seq)) {
		t.Error("the newest page has no link to the page above it")
	}
	first := strings.Index(body, newest.Letters[0].Text)
	last := strings.Index(body, newest.Letters[len(newest.Letters)-1].Text)
	if first < 0 || last < 0 || !(live < first && first < last && last < earlier) {
		t.Errorf("the newest letters stand at %d and %d, want them oldest first in the live block", first, last)
	}

	mid := f.get("/talk?with=yardmaster&before=" + strconv.FormatUint(newest.Letters[0].Seq, 10))
	if mid.status != 200 {
		t.Fatalf("the middle page: status %d", mid.status)
	}
	if !strings.Contains(mid.body, `<div id="live">`) {
		t.Error("the older page reads its letters again live")
	}
	if !strings.Contains(mid.body, link(middle.Letters[0].Seq)) {
		t.Error("the middle page has no link to the page above it")
	}
	liveAt := strings.Index(mid.body, `<div id="live">`)
	earlierAt := strings.Index(mid.body, `<div id="earlier">`)
	firstAt := strings.Index(mid.body, middle.Letters[0].Text)
	lastAt := strings.Index(mid.body, middle.Letters[len(middle.Letters)-1].Text)
	if liveAt < 0 || earlierAt < 0 || firstAt < 0 || lastAt < 0 ||
		!(liveAt < earlierAt && earlierAt < firstAt && firstAt < lastAt) {
		t.Errorf("the middle letters stand at %d and %d, want them oldest first in the earlier block", firstAt, lastAt)
	}
	if liveAt >= 0 && earlierAt > liveAt {
		if between := mid.body[liveAt:earlierAt]; strings.Contains(between, "<article") {
			t.Error("the older page has letters in the live block")
		}
	}
	peersAt := strings.Index(mid.body, `<nav class="peers"`)
	navEnd := -1
	if peersAt >= 0 {
		navEnd = strings.Index(mid.body[peersAt:], "</nav>")
	}
	if peersAt < 0 || navEnd < 0 || !strings.Contains(mid.body[peersAt:peersAt+navEnd], ">Newest<") {
		t.Error("the older page has no way back above the letters")
	}

	old := f.get("/talk?with=yardmaster&before=" + strconv.FormatUint(middle.Letters[0].Seq, 10)).body
	if strings.Contains(old, "Older mail") {
		t.Error("the oldest page links above itself")
	}
	if r := f.get("/talk?with=yardmaster&before=1"); !strings.Contains(r.body, "Nothing older.") {
		t.Errorf("below the oldest letter: want that nothing is older:\n%s", r.body)
	}
}

// talkBlobFixture serves a yard as talkFixture does, acting as the user
// as, whose web view reads the blobs where the yard keeps them, as
// production does: the store's home is the view's.
func talkBlobFixture(t *testing.T, as string) *fixture {
	t.Helper()
	dir := t.TempDir()
	st := storetest.Open(t, filepath.Join(dir, "yard.db"))
	srv, err := web.NewServer(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	if as != "" {
		token = actAs(t, srv, as)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: st, ts: ts, home: dir}
	if as != "" {
		f.signIn(srv, token)
	}
	return f
}

// The talk page draws each mail's images below its words, with the
// letter's words as their alt text. The JSON names the files.
func TestTalkShowsMailImages(t *testing.T) {
	t.Parallel()
	f := talkBlobFixture(t, "jo")
	talkYard(t, f)
	ctx := context.Background()
	ref, err := f.st.PutBlob(ctx, []byte("\x89PNG\r\n\x1a\n"+"a picture of the shed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Mail(ctx, "jo", `the "shed", painted <firmly>`, "yardmaster", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}

	var got web.Talk
	f.getJSON("/talk?with=yardmaster", &got)
	var files []store.BlobRef
	for _, l := range got.Letters {
		files = append(files, l.Files...)
	}
	if len(files) != 1 || files[0] != *ref {
		t.Fatalf("the JSON names %v, want %v", files, ref)
	}

	body := f.get("/talk?with=yardmaster").body
	if want := `<img src="/blobs/` + ref.Hash + `" alt="the &#34;shed&#34;, painted &lt;firmly&gt;">`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %s", want)
	}
	if n := strings.Count(body, "<img"); n != 1 {
		t.Errorf("the page draws %d images, want 1", n)
	}
}

// A mail's file the yard no longer holds is a line saying so, never a
// broken image: an image's line where the picture stood, a page's where
// its name stood.
func TestTalkMissingFile(t *testing.T) {
	t.Parallel()
	f := talkBlobFixture(t, "jo")
	talkYard(t, f)
	ctx := context.Background()
	shot, err := f.st.PutBlob(ctx, []byte("\x89PNG\r\n\x1a\n"+"a picture of the shed"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.st.PutBlob(ctx, []byte("<!DOCTYPE html><html><body>a door</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Mail(ctx, "jo", "the shed, painted", "yardmaster", store.BlobFile{Hash: shot.Hash}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Mail(ctx, "jo", "the door, hung", "yardmaster", store.BlobFile{Hash: page.Hash}); err != nil {
		t.Fatal(err)
	}
	must(t, os.Remove(store.BlobPath(f.home, shot.Hash)))
	must(t, os.Remove(store.BlobPath(f.home, page.Hash)))

	body := f.get("/talk?with=yardmaster").body
	if want := `image ` + shot.Hash[:8] + `: the yard no longer has it`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %q", want)
	}
	if want := `page ` + page.Hash[:8] + `: the yard no longer has it`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %q", want)
	}
	if strings.Contains(body, "<img") {
		t.Error("the page draws what the yard no longer holds")
	}
	if strings.Contains(body, "pages open from notes") {
		t.Error("the page names the missing page as one that opens from notes")
	}
}

// A mail's page is named, never drawn as an image: no link here opens
// one.
func TestTalkNamesPages(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	ctx := context.Background()
	ref, err := f.st.PutBlob(ctx, []byte("<!DOCTYPE html><html><body>a door</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Mail(ctx, "jo", "the door, hung", "yardmaster", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	body := f.get("/talk").body
	if strings.Contains(body, `<img src="/blobs/`+ref.Hash+`"`) {
		t.Error("the talk page draws the mail's page as an image")
	}
	if want := `page ` + ref.Hash[:8] + `: pages open from notes`; !strings.Contains(body, want) {
		t.Errorf("the talk page lacks %q", want)
	}
}

// Mail the crew member acknowledged says read under the user's own; mail
// it has not says nothing.
func TestTalkRead(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	ctx := context.Background()
	if err := f.st.Mail(ctx, "yardmaster", "and the fence", "jo"); err != nil {
		t.Fatal(err)
	}
	if r := f.get("/talk"); strings.Contains(r.body, ">read<") {
		t.Errorf("mail nobody acknowledged says read:\n%s", r.body)
	}
	letters, _, err := f.st.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(letters) != 3 || letters[0].From != "jo" || letters[2].From != "jo" {
		t.Fatalf("conversation = %+v, want two mails from jo and one reply", letters)
	}
	if err := f.st.AckInbox(ctx, "yardmaster", letters[0].Seq); err != nil {
		t.Fatal(err)
	}
	if r := f.get("/talk"); strings.Count(r.body, `<p class="label faint">read</p>`) != 1 {
		t.Errorf("partial acknowledgement should mark only the first own letter read:\n%s", r.body)
	}
}

// Beside the member in conversation is its state, in the words `yardr
// footplate lists it with: working and idle while it runs, not running
// while it is stopped, and not logged in at a login screen, where it reads
// no mail until the login the yard leads is made (yardr-302g).
func TestTalkStatus(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.st.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yard-yardmaster"}))
	must(f.st.CrewStatus(ctx, "yardmaster", "working"))
	must(f.st.CrewStarted(ctx, "scribe", map[string]any{"agent": "yard-scribe"}))
	must(f.st.CrewStatus(ctx, "scribe", "idle"))
	_, err := f.st.PutCrew(ctx, store.CrewMember{Name: "alder", Runner: "herdr", Enabled: true}, "jo")
	must(err)
	must(f.st.CrewStartFailed(ctx, "alder", "no workspace", time.Minute))
	_, err = f.st.PutCrew(ctx, store.CrewMember{Name: "login", Runner: "herdr"}, "jo")
	must(err)
	must(f.st.CrewStarted(ctx, "login", map[string]any{
		"agent": "yard-login", dispatch.HarnessLoginKey: "Sign in to continue",
	}))
	must(f.st.CrewStatus(ctx, "login", "idle"))
	for _, c := range []struct{ with, status string }{
		{"yardmaster", "working"},
		{"scribe", "idle"},
		{"alder", "not running"},
		{"login", "not logged in"},
	} {
		r := f.get("/talk?with=" + c.with)
		if r.status != 200 {
			t.Fatalf("/talk?with=%s: status %d", c.with, r.status)
		}
		// The state stands in the conversation's head, after the name, and
		// not again in the row of parties.
		if want := `<h2 class="section-head">` + c.with + `</h2><span class="state">· ` + c.status + `</span>`; !strings.Contains(squash(r.body), want) {
			t.Errorf("/talk?with=%s lacks %q", c.with, want)
		}
		if n := strings.Count(r.body, `<span class="state">`); n != 1 {
			t.Errorf("/talk?with=%s shows %d states, want the partner's alone", c.with, n)
		}
		r = f.get("/talk?with="+c.with, "Accept", "application/json")
		var got web.Talk
		if err := json.Unmarshal([]byte(r.body), &got); err != nil {
			t.Fatal(err)
		}
		if got.WithStatus != c.status {
			t.Errorf("/talk?with=%s: with_status %q, want %q", c.with, got.WithStatus, c.status)
		}
	}
}

// A conversation with a person renders without a status: people are no
// crew, so the page shows no state beside their name, while the picker's
// unread dot and the read marks under the user's own mail work as with
// crew. The picker lists the yard's other users after its crew, never the
// user the view acts as.
func TestTalkPerson(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	ctx := context.Background()
	if _, err := f.st.AddUser(ctx, "amy", "jo"); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.st.Mail(ctx, "amy", "lend me the ladder", "jo"))
	must(f.st.Mail(ctx, "jo", "it is by the shed", "amy"))
	must(f.st.Mail(ctx, "amy", "and the paint", "jo"))
	letters, _, err := f.st.Conversation(ctx, "jo", "amy", 0, 0, 0)
	must(err)
	if len(letters) != 3 {
		t.Fatalf("conversation = %+v, want three letters", letters)
	}
	must(f.st.AckUserInbox(ctx, "amy", letters[0].Seq, "amy"))

	r := f.get("/talk?with=amy")
	if r.status != 200 {
		t.Fatalf("/talk?with=amy: status %d, want the conversation", r.status)
	}
	for _, want := range []string{
		"lend me the ladder", "it is by the shed", "and the paint",
		`<a href="/talk?with=amy" aria-current="page">`,
		`<form id="send" method="post" action="/talk?with=amy"`,
		`<a href="/talk?with=yardmaster">`, // the crew stays listed
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
		}
	}
	if strings.Contains(r.body, `<span class="state">`) {
		t.Errorf("a person shows a state:\n%s", r.body)
	}
	if strings.Contains(r.body, "/talk?with=jo") {
		t.Errorf("the picker lists the view's own user:\n%s", r.body)
	}
	if n := strings.Count(r.body, `<p class="label faint">read</p>`); n != 1 {
		t.Errorf("%d read marks, want one: only the acknowledged own mail", n)
	}

	r = f.get("/talk?with=amy", "Accept", "application/json")
	var got web.Talk
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatal(err)
	}
	if got.With != "amy" || got.WithStatus != "" {
		t.Errorf("with %q status %q, want amy with none", got.With, got.WithStatus)
	}
	if len(got.People) != 1 || got.People[0].Name != "amy" || !got.People[0].Unread {
		t.Errorf("the people: %+v, want amy with unread mail", got.People)
	}
	if len(got.Crew) != 2 {
		t.Errorf("the crew: %+v, want both members still", got.Crew)
	}

	form := url.Values{"text": {"thanks"}}
	if r := f.postForm("/talk?with=amy", form, "Origin", f.ts.URL); r.status != http.StatusSeeOther {
		t.Errorf("POST to amy: status %d, want 303", r.status)
	}
	conv, _, err := f.st.Conversation(ctx, "jo", "amy", 0, 0, 0)
	must(err)
	if last := conv[len(conv)-1]; last.Text != "thanks" || last.From != "jo" {
		t.Errorf("the conversation ends with %+v", last)
	}
}

// With no crew, the talk page still opens on another user and shows the
// picker and form; the acting user is not a conversation partner.
func TestTalkPersonWithoutCrew(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	ctx := context.Background()
	if _, err := f.st.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddUser(ctx, "amy", "jo"); err != nil {
		t.Fatal(err)
	}

	r := f.get("/talk")
	if r.status != http.StatusOK {
		t.Fatalf("/talk: status %d, want the conversation with amy", r.status)
	}
	for _, want := range []string{
		`<a href="/talk?with=amy" aria-current="page">amy</a>`,
		`<form id="send" method="post" action="/talk?with=amy"`,
		"No mail between jo and amy yet.",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
		}
	}
	if strings.Contains(r.body, `<span class="state">`) || strings.Contains(r.body, "/talk?with=jo") {
		t.Errorf("the page shows a person status or lists the acting user:\n%s", r.body)
	}
	if r := f.get("/talk?with=jo"); r.status != http.StatusNotFound {
		t.Errorf("/talk?with=jo: status %d, want 404", r.status)
	}

	form := url.Values{"text": {"hello"}}
	if r := f.postForm("/talk", form, "Origin", f.ts.URL); r.status != http.StatusSeeOther || r.header.Get("Location") != "/talk?with=amy" {
		t.Fatalf("POST /talk: status %d, location %q, want 303 to amy", r.status, r.header.Get("Location"))
	}
	conv, _, err := f.st.Conversation(ctx, "jo", "amy", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(conv) != 1 || conv[0].Text != "hello" || conv[0].From != "jo" {
		t.Errorf("conversation with amy: %+v, want the one message from jo", conv)
	}
}

// A POST from the view's own origin sends mail as the view's user, and
// lands on the conversation; anything else is refused.
func TestTalkPost(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	form := url.Values{"text": {"and the fence"}}
	r := f.postForm("/talk?with=yardmaster", form, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/talk?with=yardmaster" {
		t.Fatalf("status %d, location %q, want 303 to the conversation", r.status, r.header.Get("Location"))
	}
	conv, _, err := f.st.Conversation(context.Background(), "jo", "yardmaster", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := conv[len(conv)-1]
	if last.Text != "and the fence" || last.From != "jo" {
		t.Fatalf("the conversation ends with %+v", last)
	}
	if r := f.get("/talk?with=yardmaster"); !strings.Contains(r.body, "and the fence") {
		t.Errorf("the sent mail is not in the conversation:\n%s", r.body)
	}
	// An empty origin, and another origin, are refused, and send
	// nothing (a missing one is refused in TestTalkPostOrigin).
	for _, origin := range []string{"", "http://evil.example/"} {
		before := len(conv)
		r := f.postForm("/talk?with=yardmaster", form, "Origin", origin)
		if r.status != http.StatusForbidden {
			t.Errorf("origin %q: status %d, want 403", origin, r.status)
		}
		conv, _, _ = f.st.Conversation(context.Background(), "jo", "yardmaster", 0, 0, 0)
		if len(conv) != before {
			t.Errorf("origin %q: sent mail all the same", origin)
		}
	}
	// To no crew member: 404.
	if r := f.postForm("/talk?with=nosuch", form, "Origin", f.ts.URL); r.status != 404 {
		t.Errorf("with=nosuch: status %d, want 404", r.status)
	}
	ctx := context.Background()
	if _, err := f.st.PutCrew(ctx, store.CrewMember{Name: "mayor", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetSetting(ctx, store.YardmasterSetting, "mayor", "jo"); err != nil {
		t.Fatal(err)
	}
	custom := url.Values{"text": {"the configured yardmaster"}}
	r = f.postForm("/talk", custom, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/talk?with=mayor" {
		t.Fatalf("POST /talk without a partner: status %d, location %q, want 303 to mayor", r.status, r.header.Get("Location"))
	}
	conv, _, err = f.st.Conversation(ctx, "jo", "mayor", 0, 0, 0)
	if err != nil || len(conv) != 1 || conv[0].Text != "the configured yardmaster" || conv[0].From != "jo" {
		t.Errorf("conversation with the configured yardmaster: %+v, %v", conv, err)
	}
}

// A text over one mail's size is sent as numbered mails, in order.
func TestTalkPostLong(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	text := strings.Repeat("x", 10<<10)
	r := f.postForm("/talk?with=yardmaster", url.Values{"text": {text}}, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther {
		t.Fatalf("status %d, want 303 to the conversation", r.status)
	}
	conv, _, err := f.st.Conversation(context.Background(), "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(conv) != 5 {
		t.Fatalf("%d letters, %v", len(conv), err)
	}
	for i, l := range conv[2:] {
		if head := strings.SplitN(l.Text, "\n", 2)[0]; head != fmt.Sprintf("(%d/3)", i+1) {
			t.Fatalf("mail %d begins %q", i+1, head)
		}
	}
}

// Mail refused stays in the form with the reason, as JSON too.
func TestTalkPostInvalid(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	r := f.postForm("/talk?with=yardmaster", url.Values{"text": {""}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "mail must be") {
		t.Errorf("empty mail: status %d, want the page with the reason:\n%s", r.status, r.body)
	}
	big := strings.Repeat("x", store.MaxMailParts*store.MaxMailBytes+1)
	r = f.postForm("/talk?with=yardmaster", url.Values{"text": {big}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "--file") || !strings.Contains(r.body, big[:100]) {
		t.Errorf("long mail: status %d, want the page with the reason and the words kept", r.status)
	}
	r = f.postForm("/talk?with=yardmaster", url.Values{"text": {""}}, "Origin", f.ts.URL, "Accept", "application/json")
	var got web.Talk
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 200 || got.Error == "" {
		t.Errorf("JSON: status %d, %+v (%v)", r.status, got, err)
	}
}

// The origin a POST is taken from: the request's host and explicit port,
// over http or https. Behind a proxy that is the name --host gave; an
// address off loopback is the request's host, including an IP under
// --unsafe. Other hosts and ports the view answers to are distinct origins.
func TestTalkPostOrigin(t *testing.T) {
	t.Parallel()
	form := url.Values{"text": {"and the fence"}}
	post := func(t *testing.T, f *fixture, requestHost, origin string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, f.ts.URL+"/talk?with=yardmaster", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		if requestHost != "" {
			req.Host = requestHost
		}
		// The cookie is the one a browser that reached the view under this
		// host has: it is named for the port, and the origin is what is
		// under test, not the token.
		req.AddCookie(f.cookieAt(requestHost))
		req.Header.Set("X-CSRF-Token", f.csrf)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "absent" {
			req.Header.Set("Origin", origin)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	self := func(f *fixture) string { return f.ts.URL }
	cases := []struct {
		name        string
		setup       func(srv *web.Server)
		requestHost string
		origin      func(f *fixture) string
		want        int
	}{
		{"loopback", nil, "", self, http.StatusSeeOther},
		{"loopback https", nil, "", func(f *fixture) string { return strings.Replace(self(f), "http:", "https:", 1) }, http.StatusSeeOther},
		{"localhost", nil, "localhost:8791", func(f *fixture) string { return "http://localhost:8791" }, http.StatusSeeOther},
		{"loopback v6", nil, "[::1]:8791", func(f *fixture) string { return "http://[::1]:8791" }, http.StatusSeeOther},
		{"another port", nil, "127.0.0.1:9000", func(f *fixture) string { return "http://127.0.0.1:9000" }, http.StatusSeeOther},
		{"different port", nil, "127.0.0.1:8791", func(f *fixture) string { return "http://127.0.0.1:9000" }, http.StatusForbidden},
		{"absent", nil, "", func(f *fixture) string { return "absent" }, http.StatusForbidden},
		{"empty", nil, "", func(f *fixture) string { return "" }, http.StatusForbidden},
		{"another site", nil, "", func(f *fixture) string { return "http://evil.example/" }, http.StatusForbidden},
		{"another site https", nil, "", func(f *fixture) string { return "https://evil.example/" }, http.StatusForbidden},
		{"an address", nil, "", func(f *fixture) string { return "http://93.184.216.34/" }, http.StatusForbidden},
		{"sandboxed", nil, "", func(f *fixture) string { return "null" }, http.StatusForbidden},
		{"no http", nil, "", func(f *fixture) string { return "ftp://127.0.0.1/" }, http.StatusForbidden},
		{"a suffix", nil, "", func(f *fixture) string { return "http://127.0.0.1.evil.example/" }, http.StatusForbidden},
		{"a host", func(srv *web.Server) { srv.AllowHosts("yard.example.net") }, "yard.example.net",
			func(f *fixture) string { return "https://yard.example.net" }, http.StatusSeeOther},
		{"a host with a port", func(srv *web.Server) { srv.AllowHosts("yard.example.net") }, "yard.example.net:8443",
			func(f *fixture) string { return "https://yard.example.net:8443" }, http.StatusSeeOther},
		{"a host in capitals", func(srv *web.Server) { srv.AllowHosts("yard.example.net") }, "yard.example.net",
			func(f *fixture) string { return "https://YARD.EXAMPLE.NET" }, http.StatusSeeOther},
		{"not that host", func(srv *web.Server) { srv.AllowHosts("yard.example.net") }, "yard.example.net",
			func(f *fixture) string { return "https://other.example.net" }, http.StatusForbidden},
		{"different host port", func(srv *web.Server) { srv.AllowHosts("yard.example.net") }, "yard.example.net:8443",
			func(f *fixture) string { return "https://yard.example.net:9443" }, http.StatusForbidden},
		{"another allowed host", func(srv *web.Server) { srv.AllowHosts("yard.example.net") }, "yard.example.net",
			func(f *fixture) string { return "http://localhost:8791" }, http.StatusForbidden},
		{"the machine", func(srv *web.Server) { srv.AllowNetwork("myhost") }, "myhost:8791",
			func(f *fixture) string { return "http://myhost:8791" }, http.StatusSeeOther},
		{"the machine local", func(srv *web.Server) { srv.AllowNetwork("myhost") }, "myhost.local",
			func(f *fixture) string { return "https://myhost.local" }, http.StatusSeeOther},
		{"an address off loopback", func(srv *web.Server) { srv.AllowNetwork("myhost") }, "93.184.216.34",
			func(f *fixture) string { return "http://93.184.216.34" }, http.StatusSeeOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, srv := talkFixture(t, "jo")
			talkYard(t, f)
			if c.setup != nil {
				c.setup(srv)
			}
			if got := post(t, f, c.requestHost, c.origin(f)); got != c.want {
				t.Errorf("origin %q: status %d, want %d", c.origin(f), got, c.want)
			}
		})
	}
}

// With --as the talk and bead pages take writes; no other route does.
func TestTalkOnlyWrite(t *testing.T) {
	t.Parallel()
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/", "/line", "/beads", "/crew", "/assets/yard.css", "/nowhere"} {
			if r := f.do(method, path); r.status != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status %d, want 405", method, path, r.status)
			}
		}
	}
}

package web_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
	if as != "" {
		srv.ActAs(as)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return &fixture{t: t, st: st, ts: ts, home: home}, srv
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
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	r := f.get("/talk")
	for _, want := range []string{
		"paint the shed", "it is painted", // the letters, both directions
		"jo", "yardmaster", // who wrote each
		`<form id="send" method="post" action="/talk?with=yardmaster"`,
		`hx-post="/talk?with=yardmaster"`,
		`hx-get="/talk?with=yardmaster"`, // the newest page is live
		`maxlength="4096"`,
		`<a href="/talk?with=scribe">scribe</a>`,
		`<a href="/talk?with=yardmaster" aria-current="page">`, // the one in conversation
		`<a href="/talk" aria-current="page">`,                 // the menu's entry
		`class="dot unread"`,                                   // the yardmaster's mail to jo is unread
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %q:\n%s", want, r.body)
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
}

// The conversation as JSON: the user, the member, the letters oldest
// first, each saying whether its user wrote it.
func TestTalkJSON(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	r := f.get("/talk?with=yardmaster", "Accept", "application/json")
	var got web.Talk
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 200 {
		t.Fatalf("status %d (%v):\n%s", r.status, err, r.body)
	}
	if got.As != "jo" || got.With != "yardmaster" || len(got.Crew) != 2 || got.MaxMail != 4096 {
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

// Mail the crew member acknowledged says read under the user's own.
func TestTalkRead(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	ctx := context.Background()
	head, err := f.st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.AckInbox(ctx, "yardmaster", head); err != nil {
		t.Fatal(err)
	}
	if r := f.get("/talk"); !strings.Contains(r.body, ">read<") {
		t.Errorf("no letter says read:\n%s", r.body)
	}
}

// A POST from the view's own origin sends mail as the view's user, and
// lands on the conversation; anything else is refused.
func TestTalkPost(t *testing.T) {
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
}

// Mail refused stays in the form with the reason, as JSON too.
func TestTalkPostInvalid(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	r := f.postForm("/talk?with=yardmaster", url.Values{"text": {""}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "mail must be") {
		t.Errorf("empty mail: status %d, want the page with the reason:\n%s", r.status, r.body)
	}
	big := strings.Repeat("x", store.MaxMailBytes+1)
	r = f.postForm("/talk?with=yardmaster", url.Values{"text": {big}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "mail must be") || !strings.Contains(r.body, big[:100]) {
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

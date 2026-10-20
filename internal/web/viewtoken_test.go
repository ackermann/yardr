package web_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

// bare is the fixture as a visitor that holds no cookie: what it sends is
// what a test puts on the request. The fixture it was made from keeps the
// cookie and the CSRF value a browser that opened the token URL has.
func (f *fixture) bare() *fixture {
	g := *f
	g.session = nil
	return &g
}

// csrfField is the hidden field every form of a view that acts carries.
var csrfField = regexp.MustCompile(`<input type="hidden" name="csrf" value="([0-9a-f]+)">`)

// noRedirect sends one request to the fixture's view and answers it as it
// is, so a 303 is seen as one.
func (f *fixture) noRedirect(method, target string, headers ...string) (*http.Response, string) {
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
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	return r, string(body)
}

// refusedWrites sends the writes that lack one of the proofs a view that
// acts asks for, or carry another view's, to target: each must be answered
// 403 and leave the yard as unchanged finds it. f holds the cookie and the
// CSRF value a browser that opened the token URL has.
func refusedWrites(t *testing.T, f *fixture, target string, form url.Values, unchanged func() bool) {
	t.Helper()
	visitor := f.bare()
	cookie := f.session.Name + "=" + f.session.Value
	another := f.session.Name + "=" + strings.Repeat("0", len(f.session.Value))
	with := func(csrf string) url.Values {
		v := url.Values{}
		for k, vs := range form {
			v[k] = vs
		}
		v.Set("csrf", csrf)
		return v
	}
	for _, c := range []struct {
		name    string
		form    url.Values
		headers []string
	}{
		// What the report did: the origin is the one header a client sets
		// for itself, and was the only guard the view had.
		{"a forged origin and nothing else", form, []string{"Origin", f.ts.URL}},
		{"the CSRF value without the cookie", with(f.csrf), []string{"Origin", f.ts.URL}},
		{"the cookie without a CSRF value", form, []string{"Origin", f.ts.URL, "Cookie", cookie}},
		{"the cookie with a CSRF value of no view's", with(strings.Repeat("0", len(f.csrf))), []string{"Origin", f.ts.URL, "Cookie", cookie}},
		{"the token in place of the CSRF value", with(f.token), []string{"Origin", f.ts.URL, "Cookie", cookie}},
		{"the CSRF value with a cookie of no view's", with(f.csrf), []string{"Origin", f.ts.URL, "Cookie", another}},
		{"the token in place of the cookie", with(f.csrf), []string{"Origin", f.ts.URL, "Cookie", f.session.Name + "=" + f.token}},
		{"both, from another site", with(f.csrf), []string{"Origin", "http://evil.example/", "Cookie", cookie}},
		{"both, with no origin", with(f.csrf), []string{"Cookie", cookie}},
	} {
		r := visitor.postForm(target, c.form, c.headers...)
		if r.status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403:\n%s", c.name, r.status, r.body)
		}
		if !unchanged() {
			t.Fatalf("%s: the write landed all the same", c.name)
		}
	}
}

// What the report did, and each proof a write needs besides the origin: a
// write to the talk page with a forged Origin and no cookie is refused, and
// so is one with the cookie and no CSRF value, and one with a CSRF value and
// no cookie. None sends the mail. A write with both lands, whether the CSRF
// value comes in the form or in a header, as a fetch would send it.
func TestTokenTalkWriteNeedsCookieAndCSRF(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	mails := func() int {
		conv, _, err := f.st.Conversation(context.Background(), "jo", "yardmaster", 0, 0, 0)
		must(t, err)
		return len(conv)
	}
	before := mails()
	form := url.Values{"text": {"and the fence"}}
	refusedWrites(t, f, "/talk?with=yardmaster", form, func() bool { return mails() == before })

	visitor := f.bare()
	cookie := f.session.Name + "=" + f.session.Value
	form.Set("csrf", f.csrf)
	if r := visitor.postForm("/talk?with=yardmaster", form, "Origin", f.ts.URL, "Cookie", cookie); r.status != http.StatusSeeOther || mails() != before+1 {
		t.Fatalf("the cookie and the CSRF value in the form: status %d, %d mails (was %d), want 303 and one more", r.status, mails(), before)
	}
	form.Del("csrf")
	if r := visitor.postForm("/talk?with=yardmaster", form, "Origin", f.ts.URL, "Cookie", cookie, "X-CSRF-Token", f.csrf); r.status != http.StatusSeeOther || mails() != before+2 {
		t.Fatalf("the cookie and the CSRF value in a header: status %d, %d mails (was %d), want 303 and one more", r.status, mails(), before)
	}
}

// The same on a bead's page, where the report answered the operator's ask
// with the curl: the ask stays open until a write holds both proofs.
func TestTokenBeadWriteNeedsCookieAndCSRF(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	b, seq := actYard(t, f, pickAsk())
	open := func() bool {
		v, err := f.st.Bead(context.Background(), b.ID)
		must(t, err)
		return v.Asks[0].State == "open"
	}
	form := url.Values{"rev": {revOf(t, f, b.ID)}, "ask": {strconv.FormatUint(seq, 10)}, "option": {"1"}}
	refusedWrites(t, f, "/beads/"+b.ID, form, open)

	form.Set("csrf", f.csrf)
	cookie := f.session.Name + "=" + f.session.Value
	if r := f.bare().postForm("/beads/"+b.ID, form, "Origin", f.ts.URL, "Cookie", cookie); r.status != http.StatusSeeOther || open() {
		t.Fatalf("the cookie and the CSRF value: status %d, ask still open: %v, want 303 and the answer", r.status, open())
	}
}

// Whatever the method, a write without the proofs is 403 before any route
// is looked for: a handler added later is guarded without being named.
func TestTokenGuardsEveryWrite(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	visitor := f.bare()
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/", "/talk", "/beads", "/beads/abc", "/assets/yard.css", "/nowhere"} {
			if r := visitor.do(method, path, "Origin", f.ts.URL); r.status != http.StatusForbidden {
				t.Errorf("%s %s without the proofs: status %d, want 403", method, path, r.status)
			}
		}
	}
}

// The token URL buys the cookie: HttpOnly, SameSite=Strict, for the whole
// view and for the browser's session, not the token itself, and a redirect
// to the same URL without the token. Only a GET with the right token does.
func TestTokenURLSetsTheCookie(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	visitor := f.bare()
	port := strings.TrimPrefix(f.ts.URL, "http://127.0.0.1:")

	r, _ := visitor.noRedirect(http.MethodGet, "/?token="+f.token)
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/" {
		t.Fatalf("status %d, location %q, want 303 to /", r.StatusCode, r.Header.Get("Location"))
	}
	cookies := r.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("%d cookies, want one", len(cookies))
	}
	c := cookies[0]
	if c.Name != "yardr_web_"+port || c.Value == "" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" ||
		c.MaxAge != 0 || !c.Expires.IsZero() || c.Domain != "" {
		t.Errorf("cookie %+v, want yardr_web_%s: HttpOnly, SameSite=Strict, Path=/, for the session and this host", c, port)
	}
	if strings.Contains(c.Value, f.token) || strings.Contains(r.Header.Get("Set-Cookie"), f.token) {
		t.Errorf("the cookie carries the token: %q", r.Header.Get("Set-Cookie"))
	}
	if cc := r.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q, want no-store: the answer has set a cookie", cc)
	}

	// The rest of the URL is kept, wherever the token stood in the query.
	for target, want := range map[string]string{
		"/talk?token=" + f.token + "&with=yardmaster": "/talk?with=yardmaster",
		"/beads?z=1&token=" + f.token + "&a=2":        "/beads?a=2&z=1",
		"/flows?panel=1&token=" + f.token:             "/flows?panel=1",
		"/beads/yard-1?token=" + f.token:              "/beads/yard-1",
		"/talk?with=a%20b&token=" + f.token:           "/talk?with=a+b",
		// A path with two slashes at the start is a path of this view, not
		// a host: the redirect must not leave it.
		"//evil.example/path?token=" + f.token: "/evil.example/path",
	} {
		if r, _ := visitor.noRedirect(http.MethodGet, target); r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != want || len(r.Cookies()) != 1 {
			t.Errorf("%s: status %d, location %q, %d cookies, want 303 to %q with the cookie", target, r.StatusCode, r.Header.Get("Location"), len(r.Cookies()), want)
		}
	}

	// A token that is not the view's buys nothing, and the page is the
	// read-only one.
	for _, given := range []string{"nope", "", f.token[:len(f.token)-1], f.token + "0", strings.ToUpper(f.token), f.csrf, c.Value} {
		r, body := visitor.noRedirect(http.MethodGet, "/talk?token="+given)
		if r.StatusCode != 200 || len(r.Cookies()) != 0 || !strings.Contains(body, "token URL") {
			t.Errorf("token %q: status %d, %d cookies, want the page that asks for the token and no cookie", given, r.StatusCode, len(r.Cookies()))
		}
	}
	// Only a GET trades it: a POST that carries it is a write without the
	// cookie, and a HEAD changes no state of the browser's.
	if r, _ := visitor.noRedirect(http.MethodPost, "/talk?with=yardmaster&token="+f.token, "Origin", f.ts.URL); r.StatusCode != http.StatusForbidden || len(r.Cookies()) != 0 {
		t.Errorf("POST with the token: status %d, %d cookies, want 403 and no cookie", r.StatusCode, len(r.Cookies()))
	}
	if r, _ := visitor.noRedirect(http.MethodHead, "/?token="+f.token); len(r.Cookies()) != 0 {
		t.Errorf("HEAD with the token set a cookie")
	}
}

// The token URL, then the write: a browser opens it, is sent on to the page
// without the token, takes the CSRF value from the form it finds there, and
// its write lands.
func TestTokenURLThenTheWriteLands(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	jar, err := cookiejar.New(nil)
	must(t, err)
	browser := &http.Client{Jar: jar}
	read := func(r *http.Response, err error) (string, *http.Response) {
		t.Helper()
		must(t, err)
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return string(b), r
	}

	// Without the cookie the page is the one a view with no --as shows.
	body, _ := read(browser.Get(f.ts.URL + "/talk?with=yardmaster"))
	if strings.Contains(body, "<form") || !strings.Contains(body, "token URL") {
		t.Fatalf("a browser with no cookie reads the page that asks for the token, with no form:\n%s", body)
	}
	// The URL the view printed: sent on to the page, the token gone.
	body, r := read(browser.Get(f.ts.URL + "/talk?with=yardmaster&token=" + f.token))
	if got := r.Request.URL.String(); got != f.ts.URL+"/talk?with=yardmaster" {
		t.Fatalf("the token URL ended at %s, want the page without the token", got)
	}
	m := csrfField.FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, "Mail to yardmaster as jo") {
		t.Fatalf("the page after the token URL has no form with a CSRF field:\n%s", body)
	}
	// The write, from the form's own values.
	form := url.Values{"text": {"and the fence"}, "csrf": {m[1]}}
	req, err := http.NewRequest(http.MethodPost, f.ts.URL+"/talk?with=yardmaster", strings.NewReader(form.Encode()))
	must(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", f.ts.URL)
	body, r = read(browser.Do(req))
	if r.StatusCode != 200 || !strings.Contains(body, "and the fence") {
		t.Fatalf("the write: status %d, want the conversation with the new mail:\n%s", r.StatusCode, body)
	}
	conv, _, err := f.st.Conversation(context.Background(), "jo", "yardmaster", 0, 0, 0)
	must(t, err)
	if last := conv[len(conv)-1]; last.From != "jo" || last.Text != "and the fence" {
		t.Fatalf("the conversation ends with %+v, want jo's mail", last)
	}
}

// A request without the cookie reads as on a view with no --as: nothing is
// drawn to write with, the page says why, and it acts as no one, so it is
// shown none of the user's conversation. The JSON says why too.
func TestVisitorWithoutCookieReads(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	talkYard(t, f)
	visitor := f.bare()
	r := visitor.get("/talk?with=yardmaster")
	if r.status != 200 || strings.Contains(r.body, "<form") || strings.Contains(r.body, "paint the shed") ||
		!strings.Contains(r.body, "token URL") || strings.Contains(r.body, "Start it with") {
		t.Errorf("the talk page for a visitor: status %d, want the page that asks for the token, with no form and no mail:\n%s", r.status, r.body)
	}
	if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'none'") {
		t.Errorf("Content-Security-Policy %q, want form-action 'none' for a request that cannot write", csp)
	}
	var talk web.Talk
	if r := visitor.get("/talk", "Accept", "application/json"); json.Unmarshal([]byte(r.body), &talk) != nil || talk.As != "" || !talk.NeedsToken {
		t.Errorf("the JSON for a visitor: %s, want no user and needs_token", r.body)
	}
	// With the cookie the same page is jo's conversation.
	if r := f.get("/talk?with=yardmaster"); !strings.Contains(r.body, "paint the shed") || strings.Contains(r.body, "token URL") {
		t.Errorf("the talk page with the cookie:\n%s", r.body)
	}
	// A view with no --as says what it did, not that a token is missing.
	if r := newFixture(t).get("/talk"); strings.Contains(r.body, "token URL") || !strings.Contains(r.body, "does not act") {
		t.Errorf("a view that does not act:\n%s", r.body)
	}
}

// Every form of the view carries the CSRF value for a request that holds
// the cookie, on every page that draws one, and none for a request that
// does not, which is drawn no form at all.
func TestEveryFormCarriesTheCSRFValue(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	b, _ := actYard(t, f, pickAsk())
	_, err := f.st.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo")
	must(t, err)
	actFlow(t, f)
	_, err = f.st.Advance(context.Background(), b.ID, "decide", store.Expect{}, "jo")
	must(t, err)
	visitor := f.bare()
	for _, c := range []struct {
		page  string
		forms int
	}{
		{"/talk?with=yardmaster", 1}, // the send form
		{"/beads/" + b.ID, 2},        // the ask's answer, and the move
		{"/", 1},                     // the needs row's answer
		{"/line", 2},                 // the ask under its reason, and in the aside
	} {
		r := f.get(c.page)
		fields := csrfField.FindAllStringSubmatch(r.body, -1)
		if forms := strings.Count(r.body, "<form"); forms != c.forms || len(fields) != forms {
			t.Errorf("%s: %d forms, %d CSRF fields, want %d of each:\n%s", c.page, forms, len(fields), c.forms, r.body)
		}
		for _, m := range fields {
			if m[1] != f.csrf {
				t.Errorf("%s: a CSRF field holds %q, want the view's value", c.page, m[1])
			}
		}
		if r := visitor.get(c.page); strings.Contains(r.body, "<form") || strings.Contains(r.body, "csrf") {
			t.Errorf("%s: a visitor with no cookie is drawn a form:\n%s", c.page, r.body)
		}
	}
}

// A view's secrets are its own: a token is 256 bits in hex, the cookie and
// the CSRF value are other than it and than each other, and none of them
// carries over to a second view or to the same one started again.
func TestTokenIsPerView(t *testing.T) {
	a, _ := talkFixture(t, "jo")
	b, _ := talkFixture(t, "jo")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a.token) {
		t.Errorf("token %q, want 256 bits in hex", a.token)
	}
	for name, pair := range map[string][2]string{
		"the token":                      {a.token, b.token},
		"the cookie":                     {a.session.Value, b.session.Value},
		"the CSRF value":                 {a.csrf, b.csrf},
		"a token and the cookie it buys": {a.token, a.session.Value},
		"a token and the CSRF value":     {a.token, a.csrf},
		"the cookie and the CSRF value":  {a.session.Value, a.csrf},
	} {
		if pair[0] == pair[1] {
			t.Errorf("%s is the same twice: %q", name, pair[0])
		}
	}
	// a's cookie, under the name b looks for, is no cookie of b's.
	replay := b.session.Name + "=" + a.session.Value
	if r := b.bare().get("/talk", "Cookie", replay); !strings.Contains(r.body, "token URL") {
		t.Errorf("another view's cookie acts here:\n%s", r.body)
	}
	if r, _ := b.bare().noRedirect(http.MethodGet, "/?token="+a.token); len(r.Cookies()) != 0 {
		t.Error("another view's token buys a cookie here")
	}
}

// The cookie is named for the port the view was reached on: a browser
// sends a host's cookies to every port of it, so two views on one machine
// would otherwise take one cookie from each other. A cookie of another
// port's name acts for no one here.
func TestTokenCookieIsNamedForThePort(t *testing.T) {
	f, _ := talkFixture(t, "jo")
	for host, want := range map[string]string{
		"127.0.0.1:8791": "yardr_web_8791",
		"localhost:9000": "yardr_web_9000",
		"[::1]:8791":     "yardr_web_8791",
		"localhost":      "yardr_web",
		"127.0.0.1":      "yardr_web",
	} {
		if got := f.cookieAt(host).Name; got != want {
			t.Errorf("Host %s: cookie %s, want %s", host, got, want)
		}
	}
	other := f.cookieAt("127.0.0.1:9000")
	r := f.bare().get("/talk", "Host", "127.0.0.1:8791", "Cookie", other.Name+"="+other.Value)
	if !strings.Contains(r.body, "token URL") {
		t.Errorf("the cookie of port 9000 acts on a request for port 8791:\n%s", r.body)
	}
	r = f.bare().get("/talk", "Host", "127.0.0.1:9000", "Cookie", other.Name+"="+other.Value)
	if strings.Contains(r.body, "token URL") {
		t.Errorf("the cookie of port 9000 does not act on a request for port 9000:\n%s", r.body)
	}
}

// A view with no --as takes no token, and the token URL is a page like any
// other there: a ?token= buys nothing and redirects nowhere.
func TestReadOnlyViewTakesNoToken(t *testing.T) {
	f := newFixture(t)
	r, body := f.noRedirect(http.MethodGet, "/?token=anything")
	if r.StatusCode != 200 || len(r.Cookies()) != 0 || strings.Contains(body, "csrf") {
		t.Errorf("status %d, %d cookies: a view that does not act took a token", r.StatusCode, len(r.Cookies()))
	}
}

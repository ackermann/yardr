package source

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// token is the credential of these tests. No error of a call may have it,
// nor secretText, a parameter's value, nor customer, the number in it. The
// number has seven digits because an error may name a stand-in's address,
// and a port has at most five: no address can be taken for it (yardr-wpk8).
const (
	token      = "tok-3f9a-not-to-be-seen"
	customer   = "4711001"
	secretText = "the invoice of customer " + customer
)

// standin is a service on the loopback that keeps what it was sent.
type standin struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []sent
}

type sent struct {
	Method, URI, Auth, ContentType string
	Body                           []byte
}

func newStandin(t *testing.T, answer http.HandlerFunc) *standin {
	t.Helper()
	return newStandinOn(t, nil, answer)
}

// newStandinOn is newStandin listening on l, or on a port of httptest's
// choosing when l is nil.
func newStandinOn(t *testing.T, l net.Listener, answer http.HandlerFunc) *standin {
	t.Helper()
	s := &standin{}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, sent{r.Method, r.RequestURI, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
		s.mu.Unlock()
		answer(w, r)
	}))
	if l != nil {
		s.Listener.Close()
		s.Listener = l
	}
	s.Start()
	t.Cleanup(s.Close)
	return s
}

func (s *standin) got() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sent(nil), s.reqs...)
}

// declared is the declaration text with its base at the stand-in.
func declared(t *testing.T, text, base, at string) *Declaration {
	t.Helper()
	if !strings.Contains(text, base) {
		t.Fatalf("the file has no base %s", base)
	}
	d, err := Parse("test.yaml", []byte(strings.Replace(text, base, at, 1)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func reason(err error) string {
	var ce *CallError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}

// clean fails the test when err says what no error of a call may.
func clean(t *testing.T, err error) {
	t.Helper()
	if s := leaked(err); s != "" {
		t.Fatalf("the error has %q in it: %v", s, err)
	}
}

// leaked is what err says that no error of a call may, or "".
func leaked(err error) string {
	if err == nil {
		return ""
	}
	for _, s := range []string{token, secretText, customer} {
		if strings.Contains(err.Error(), s) {
			return s
		}
	}
	return ""
}

func jsonAnswer(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

var user30 = strings.Repeat("u", 30)

// The Pushover call as its file declares it: the credential as a value of
// the body, each parameter under the service's name for it, and the
// optional one left out when it is not given.
func TestDoSendsTheCredentialInTheBody(t *testing.T) {
	s := newStandin(t, jsonAnswer(`{"status":1,"request":"r-1"}`))
	d := declared(t, pushover, "https://api.pushover.net/1", s.URL+"/1")
	res, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "yardr", "text": "a bead entered decide"}, token)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || !res.OK() || string(res.Answer) != `{"status":1,"request":"r-1"}` || res.Bytes == 0 {
		t.Fatalf("result = %+v", res)
	}
	got := s.got()
	if len(got) != 1 || got[0].Method != "POST" || got[0].URI != "/1/messages.json" || got[0].ContentType != "application/json" || got[0].Auth != "" {
		t.Fatalf("sent = %+v", got)
	}
	var body map[string]any
	if err := json.Unmarshal(got[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 4 || body["token"] != token || body["user"] != user30 || body["title"] != "yardr" || body["message"] != "a bead entered decide" {
		t.Fatalf("body = %s", got[0].Body)
	}
	if _, has := body["url"]; has {
		t.Fatalf("the optional link was not given and is in the body: %s", got[0].Body)
	}
	if _, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "t", "text": "x", "link": "https://yard.example/beads/1"}, token); err != nil {
		t.Fatal(err)
	}
	if got := s.got(); !strings.Contains(string(got[1].Body), `"url":"https://yard.example/beads/1"`) {
		t.Fatalf("body with a link = %s", got[1].Body)
	}
}

// A parameter is escaped by where it stands, a body's value has the
// parameter's type, the credential goes in its header, and select keeps
// the named keys of the answer.
func TestDoFillsPathQueryBodyAndHeader(t *testing.T) {
	const file = `name: crm
base: https://crm.example.com/api
credential: { env: CRM_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  find:
    kind: read
    params:
      id:    { type: string }
      limit: { type: int }
      open:  { type: bool, optional: true }
      plan:  { type: enum, values: [free, paid], optional: true }
    request:
      method: POST
      path: "/customers/{id}/notes"
      query: { "filter[plan]": "{plan}", per: "{limit}", fixed: v1 }
      body: { limit: "{limit}", open: "{open}", say: "for {id}", list: [ "{id}", 3 ] }
    answer: { select: [id, name] }
`
	s := newStandin(t, jsonAnswer(`[ {"id": 1, "name": "A", "secret": "s"}, {"id": 2, "name": "B", "plan": "paid"} ]`))
	d := declared(t, file, "https://crm.example.com/api", s.URL+"/api")
	res, err := Do(context.Background(), d, "find", map[string]string{"id": "a/b c?", "limit": "20", "plan": "paid"}, token)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Answer) != `[{"id":1,"name":"A"},{"id":2,"name":"B"}]` {
		t.Fatalf("answer = %s", res.Answer)
	}
	got := s.got()[0]
	if got.URI != "/api/customers/a%2Fb%20c%3F/notes?filter%5Bplan%5D=paid&fixed=v1&per=20" {
		t.Fatalf("address = %s", got.URI)
	}
	if got.Auth != "Bearer "+token {
		t.Fatalf("Authorization = %q", got.Auth)
	}
	if string(got.Body) != `{"limit":20,"list":["a/b c?",3],"say":"for a/b c?"}` {
		t.Fatalf("body = %s", got.Body)
	}
	if strings.Contains(string(got.Body), token) || strings.Contains(got.URI, token) {
		t.Fatal("the credential is outside its header")
	}
}

// No redirect is followed: the 3xx is the answer, the call fails with it,
// and where it points is never asked.
func TestDoFollowsNoRedirect(t *testing.T) {
	elsewhere := newStandin(t, jsonAnswer(`{"taken":true}`))
	for _, status := range []int{301, 302, 307, 308} {
		s := newStandin(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, elsewhere.URL+"/1/messages.json", status)
		})
		d := declared(t, pushover, "https://api.pushover.net/1", s.URL+"/1")
		res, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "t", "text": secretText}, token)
		var ce *CallError
		if !errors.As(err, &ce) || ce.Reason != Redirect || ce.Status != status || res != nil {
			t.Fatalf("%d: Do = %+v, %v; want it refused as a redirect", status, res, err)
		}
		if !strings.Contains(err.Error(), "a redirect to "+strings.TrimPrefix(elsewhere.URL, "http://")) || !strings.Contains(err.Error(), "yardr follows none") {
			t.Fatalf("%d: the error does not name the cause: %v", status, err)
		}
		clean(t, err)
	}
	if got := elsewhere.got(); len(got) != 0 {
		t.Fatalf("the redirect was followed: %+v", got)
	}
}

// An error may name a stand-in's address, and its port is any free one: one
// with the digits 4711 in it, which the customer's number once was, is no
// secret said (yardr-wpk8). The number itself still is.
func TestCleanTakesNoPortForTheSecret(t *testing.T) {
	var l net.Listener
	for _, port := range []string{"44711", "54711", "34711", "24711", "14711", "47110", "47111", "47112", "47113", "47114"} {
		var err error
		if l, err = net.Listen("tcp", "127.0.0.1:"+port); err == nil {
			break
		}
	}
	if l == nil {
		t.Skip("no port with 4711 in it is free on the loopback")
	}
	elsewhere := newStandin(t, jsonAnswer(`{"taken":true}`))
	s := newStandinOn(t, l, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/1/messages.json", http.StatusPermanentRedirect)
	})
	d := declared(t, pushover, "https://api.pushover.net/1", s.URL+"/1")
	_, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "t", "text": secretText}, token)
	if reason(err) != Redirect || !strings.Contains(err.Error(), strings.TrimPrefix(s.URL, "http://")) || !strings.Contains(err.Error(), "4711") {
		t.Fatalf("err = %v; want a refused redirect that names %s", err, s.URL)
	}
	if got := leaked(err); got != "" {
		t.Fatalf("the address is taken for %q: %v", got, err)
	}
	for _, said := range []string{token, secretText, "customer " + customer + " of 127.0.0.1:44711"} {
		if leaked(errors.New("the call sent "+said)) == "" {
			t.Errorf("an error with %q in it is clean", said)
		}
	}
}

// An answer over its bound is refused, not cut: over the call's own bound
// after select, and over what yardr reads of any answer before it.
func TestDoRefusesAnAnswerOverTheBound(t *testing.T) {
	const file = `name: big
base: https://big.example.com
calls:
  all:    { kind: read, request: { method: GET, path: /all } }
  raised: { kind: read, request: { method: GET, path: /all }, answer: { max: 200000 } }
  picked: { kind: read, request: { method: GET, path: /all }, answer: { select: [id] } }
`
	answer := func(n int) string { return `{"id":7,"pad":"` + strings.Repeat("x", n) + `"}` }
	size := AnswerMax + 1000
	s := newStandin(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, answer(size)) })
	d := declared(t, file, "https://big.example.com", s.URL)

	res, err := Do(context.Background(), d, "all", nil, "")
	if reason(err) != TooLarge || res != nil {
		t.Fatalf("Do = %+v, %v; want too_large", res, err)
	}
	if !strings.Contains(err.Error(), "over the call's bound of 65536") || !strings.Contains(err.Error(), "refused, not cut") {
		t.Fatalf("the error does not name the cause: %v", err)
	}
	if res, err := Do(context.Background(), d, "raised", nil, ""); err != nil || len(res.Answer) != len(answer(size)) {
		t.Fatalf("with answer.max raised: %v", err)
	}
	if res, err := Do(context.Background(), d, "picked", nil, ""); err != nil || string(res.Answer) != `{"id":7}` {
		t.Fatalf("with a select that brings it under the bound: %+v, %v", res, err)
	}

	size = AnswerLimit + 1
	for _, call := range []string{"all", "raised", "picked"} {
		_, err := Do(context.Background(), d, call, nil, "")
		if reason(err) != TooLarge || !strings.Contains(err.Error(), "the most yardr reads of one") {
			t.Fatalf("%s, over what is read at all: %v", call, err)
		}
	}
}

// An answer that is no JSON is refused; one that is no success is returned
// with its status and nothing of what the service said; a success that
// says nothing is one.
func TestDoAnswers(t *testing.T) {
	status, body := 200, "<html>hello</html>"
	s := newStandin(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
	d := declared(t, crm, "https://crm.example.com/api/v2", s.URL)
	if _, err := Do(context.Background(), d, "ping", nil, token); reason(err) != NotJSON || !strings.Contains(err.Error(), "answered 200 with what is no JSON") {
		t.Fatalf("not JSON: %v", err)
	}
	status, body = 401, `{"error":"bad token `+token+`"}`
	res, err := Do(context.Background(), d, "ping", nil, token)
	if err != nil || res.Status != 401 || res.OK() || res.Answer != nil {
		t.Fatalf("a 401 = %+v, %v", res, err)
	}
	status, body = 500, "<html>down</html>"
	if res, err := Do(context.Background(), d, "ping", nil, token); err != nil || res.Status != 500 || res.Answer != nil {
		t.Fatalf("a 500 = %+v, %v", res, err)
	}
	status, body = 204, ""
	if res, err := Do(context.Background(), d, "ping", nil, token); err != nil || !res.OK() || res.Answer != nil {
		t.Fatalf("a 204 = %+v, %v", res, err)
	}
}

func TestDoTimesOut(t *testing.T) {
	was := Timeout
	Timeout = 100 * time.Millisecond
	defer func() { Timeout = was }()
	release := make(chan struct{})
	s := newStandin(t, func(http.ResponseWriter, *http.Request) { <-release })
	defer close(release)
	d := declared(t, pushover, "https://api.pushover.net/1", s.URL)
	start := time.Now()
	_, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "t", "text": secretText}, token)
	if reason(err) != TimedOut || !strings.Contains(err.Error(), "nothing within 100ms") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the call took %s", took)
	}
	clean(t, err)
}

// A service that is not there: the error names the host and the cause, and
// not the address with the call's values in it.
func TestDoUnreachable(t *testing.T) {
	s := newStandin(t, jsonAnswer(`{}`))
	d := declared(t, crm, "https://crm.example.com/api/v2", s.URL)
	s.Close()
	_, err := Do(context.Background(), d, "customer", map[string]string{"id": customer}, token)
	if reason(err) != Unreachable || !strings.Contains(err.Error(), "no answer from "+strings.TrimPrefix(s.URL, "http://")) {
		t.Fatalf("err = %v", err)
	}
	clean(t, err)
}

// A call whose parameters are not what the declaration allows sends
// nothing, and says which parameter without saying its value.
func TestDoRefusesParameters(t *testing.T) {
	s := newStandin(t, jsonAnswer(`{}`))
	d := declared(t, pushover, "https://api.pushover.net/1", s.URL)
	ok := map[string]string{"user": user30, "title": "t", "text": secretText}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for k, v := range ok {
			m[k] = v
		}
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	for _, tc := range []struct {
		params map[string]string
		want   string
	}{
		{with("user", customer), "parameter user does not match its pattern"},
		{with("title", strings.Repeat("é", 251)), "parameter title is 251 characters, and at most 250"},
		{with("text", ""), "parameter text is missing"},
		{with("priority", "2"), `"priority" is no parameter of the call (it takes: user, title, text, link)`},
		{with("text", "\xff\xfe"), "parameter text is no text"},
	} {
		_, err := Do(context.Background(), d, "message", tc.params, token)
		if reason(err) != BadParam || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q: %v", tc.want, err)
		}
		clean(t, err)
	}
	if _, err := Do(context.Background(), d, "massage", ok, token); reason(err) != NoCall {
		t.Errorf("a call the file does not have: %v", err)
	}
	if _, err := Do(context.Background(), d, "message", ok, ""); reason(err) != NoCredential || !strings.Contains(err.Error(), "does not set PUSHOVER_TOKEN") {
		t.Errorf("no credential: %v", err)
	}
	if _, err := Do(context.Background(), d, "message", ok, token+"\nX-Injected: 1"); reason(err) != NoCredential {
		t.Errorf("a credential of two lines: %v", err)
	}

	// A value cannot leave the base through the path.
	c := declared(t, crm, "https://crm.example.com/api/v2", s.URL+"/api/v2")
	c.Calls[1].Params[0].Pattern, c.Calls[1].Params[0].re = "", nil
	for value, want := range map[string]string{"..": `makes a ".." segment`, ".": `makes a "." segment`, "": "parameter id is empty, and it is part of the path"} {
		if _, err := Do(context.Background(), c, "customer", map[string]string{"id": value}, token); reason(err) != BadParam || !strings.Contains(err.Error(), want) {
			t.Errorf("id %q: %v", value, err)
		}
	}
	if got := s.got(); len(got) != 0 {
		t.Fatalf("a refused call was sent: %+v", got)
	}
	if _, err := Do(context.Background(), c, "customer", map[string]string{"id": "../../admin"}, token); err != nil {
		t.Fatal(err)
	}
	if got := s.got(); len(got) != 1 || got[0].URI != "/api/v2/customers/..%2F..%2Fadmin" {
		t.Fatalf("a value with slashes is not one segment: %+v", got)
	}
}

// Plain http goes to this machine only, held where the call is made too: a
// declaration that did not come from Parse, and a name that resolves
// elsewhere, are refused before anything is sent.
func TestDoRefusesPlainHTTPToAnotherMachine(t *testing.T) {
	d := &Declaration{Name: "x", Base: "http://192.0.2.1:9", Calls: []Call{{Name: "ping", Kind: Read, Request: Request{Method: "GET", Path: "/"}, Answer: Answer{Max: AnswerMax}}}}
	if _, err := Do(context.Background(), d, "ping", nil, ""); reason(err) != Refused || !strings.Contains(err.Error(), "plain http to 192.0.2.1, which is not this machine") {
		t.Fatalf("err = %v", err)
	}
	d.Base = "ftp://127.0.0.1"
	if _, err := Do(context.Background(), d, "ping", nil, ""); reason(err) != Refused {
		t.Fatalf("ftp: %v", err)
	}
	if err := loopbackOnly("tcp4", "192.0.2.1:80", nil); err == nil {
		t.Fatal("a connection to another machine was allowed")
	}
	for _, a := range []string{"127.0.0.1:80", "127.9.9.9:80", "[::1]:80"} {
		if err := loopbackOnly("tcp", a, nil); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
}

// An update of a source whose file names the header is sent with the key it
// is given, and with the credential the caller read for it. A read call is
// sent no key, nor is an update of a file that names no header; a key that
// is no value of a header is refused before anything is sent.
func TestDoKeyedSendsTheKeyWithAnUpdate(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Method+" "+r.Header.Get("Authorization")+" "+r.Header.Get("Idempotency-Key"))
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	ctx := context.Background()
	d := declared(t, keyed, "https://crm.example.com/api/v2", srv.URL)
	note := map[string]string{"id": "7", "text": "called"}
	if _, err := DoKeyed(ctx, d, "note", note, "write-tok", "k-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := DoKeyed(ctx, d, "customer", map[string]string{"id": "7"}, token, "k-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := Do(ctx, d, "note", note, "write-tok"); err != nil {
		t.Fatal(err)
	}
	plain := declared(t, crm, "https://crm.example.com/api/v2", srv.URL)
	if _, err := DoKeyed(ctx, plain, "note", note, token, "k-3"); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST Bearer write-tok k-1", "GET Bearer " + token + " ", "POST Bearer write-tok ", "POST Bearer " + token + " "}
	if strings.Join(keys, "|") != strings.Join(want, "|") {
		t.Fatalf("sent = %q, want %q", keys, want)
	}
	if _, err := DoKeyed(ctx, d, "note", note, "write-tok", "k\r\nX-Other: 1"); reason(err) != BadParam || len(keys) != len(want) {
		t.Fatalf("a key with a line break = %v, sent %q", err, keys)
	}
	// Without the credential of an update, the error names its variable.
	if _, err := DoKeyed(ctx, d, "note", note, "", "k-4"); reason(err) != NoCredential || !strings.Contains(err.Error(), "CRM_WRITE_TOKEN") {
		t.Fatalf("no credential = %v", err)
	}
}

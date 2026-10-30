package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// noSecrets is the tests' stand-in for the store's secret reader, which
// this package's tests cannot import (the store imports source). No test
// declares a secret-file parameter, so it is never called; a call fails
// the test that made it.
func noSecrets(path string) ([]byte, string, error) {
	return nil, "", errors.New("no secret files in this test")
}

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
	res, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "yardr", "text": "a bead entered decide"}, token, noSecrets)
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
	if _, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "t", "text": "x", "link": "https://yard.example/beads/1"}, token, noSecrets); err != nil {
		t.Fatal(err)
	}
	if got := s.got(); !strings.Contains(string(got[1].Body), `"url":"https://yard.example/beads/1"`) {
		t.Fatalf("body with a link = %s", got[1].Body)
	}
}

// A file with in_path sends the credential in the path, as it is: a
// service that takes it there reads the URL as every client sends it. The
// stand-in sees the token with its ':' unescaped.
func TestDoSendsTheCredentialInThePath(t *testing.T) {
	const file = `name: pathcred
base: https://pathcred.example.com
credential: { env: PATHCRED_TOKEN, in_path: true }
calls:
  message:
    kind: update
    params: { chat: { type: string }, text: { type: string } }
    request:
      method: POST
      path: /bot{credential}/sendMessage
      body: { chat_id: "{chat}", text: "{text}" }
`
	s := newStandin(t, jsonAnswer(`{"ok":true}`))
	d := declared(t, file, "https://pathcred.example.com", s.URL)
	const cred = "123456:ABC-def_123"
	res, err := Do(context.Background(), d, "message", map[string]string{"chat": "987654321", "text": "hello"}, cred, noSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || !res.OK() {
		t.Fatalf("result = %+v", res)
	}
	got := s.got()
	if len(got) != 1 || got[0].Method != "POST" || got[0].URI != "/bot"+cred+"/sendMessage" {
		t.Fatalf("sent = %+v", got)
	}
	var body map[string]any
	if err := json.Unmarshal(got[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["chat_id"] != "987654321" || body["text"] != "hello" {
		t.Fatalf("body = %s", got[0].Body)
	}
	// Without in_path the same path is refused when the call is made, as
	// it is when the file is read, and the refusal names no credential.
	d.Credential.InPath = false
	_, err = Do(context.Background(), d, "message", map[string]string{"chat": "987654321", "text": "hello"}, cred, noSecrets)
	if err == nil || reason(err) != BadParam || !strings.Contains(err.Error(), "credential.in_path: true") || strings.Contains(err.Error(), cred) {
		t.Fatalf("without in_path: err = %v", err)
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
      enabled: { type: bool, default: false }
      plan:  { type: enum, values: [free, paid], optional: true }
    request:
      method: POST
      path: "/customers/{id}/notes"
      query: { "filter[plan]": "{plan}", per: "{limit}", fixed: v1 }
      body: { limit: "{limit}", open: "{open}", enabled: "{enabled}", say: "for {id}", list: [ "{id}", 3 ] }
    answer: { select: [id, name] }
`
	s := newStandin(t, jsonAnswer(`[ {"id": 1, "name": "A", "secret": "s"}, {"id": 2, "name": "B", "plan": "paid"} ]`))
	d := declared(t, file, "https://crm.example.com/api", s.URL+"/api")
	res, err := Do(context.Background(), d, "find", map[string]string{"id": "a/b c?", "limit": "20", "plan": "paid"}, token, noSecrets)
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
	if string(got.Body) != `{"enabled":false,"limit":20,"list":["a/b c?",3],"say":"for a/b c?"}` {
		t.Fatalf("body = %s", got.Body)
	}
	if strings.Contains(string(got.Body), token) || strings.Contains(got.URI, token) {
		t.Fatal("the credential is outside its header")
	}
	if _, err := Do(context.Background(), d, "find", map[string]string{"id": "a", "limit": "20", "enabled": "true"}, token, noSecrets); err != nil {
		t.Fatal(err)
	}
	if got := s.got()[1]; string(got.Body) != `{"enabled":true,"limit":20,"list":["a",3],"say":"for a"}` {
		t.Fatalf("explicit value overrides the default: body = %s", got.Body)
	}
}

// uploadPart is one part of a multipart body a test read back.
type uploadPart struct{ name, filename, ctype, body string }

// uploadParts reads the parts of a sent multipart body, in order.
func uploadParts(t *testing.T, sent sent) []uploadPart {
	t.Helper()
	mt, params, err := mime.ParseMediaType(sent.ContentType)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("Content-Type = %q, want a multipart upload", sent.ContentType)
	}
	mr := multipart.NewReader(bytes.NewReader(sent.Body), params["boundary"])
	var out []uploadPart
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("reading the upload: %v", err)
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Fatalf("reading a part: %v", err)
		}
		out = append(out, uploadPart{p.FormName(), p.FileName(), p.Header.Get("Content-Type"), string(data)})
	}
}

// A body that names a file parameter goes as a multipart upload: the file
// one file part under the body's key for it, every other value a text
// field. The part carries the file's own name and the type sniffed from
// what it holds, and an optional value left out stays out.
func TestDoSendsAFileAsMultipart(t *testing.T) {
	const file = `name: bot
base: https://bot.example.com
credential: { env: BOT_TOKEN, in_path: true }
calls:
  document:
    kind: update
    params:
      chat: { type: string, pattern: "^-?[0-9]+$" }
      path: { type: file, max: 52428800 }
      caption: { type: string, max: 1024, optional: true }
    request:
      method: POST
      path: /bot{credential}/sendDocument
      body: { chat_id: "{chat}", document: "{path}", caption: "{caption}" }
`
	s := newStandin(t, jsonAnswer(`{"ok":true,"result":{"message_id":7}}`))
	d := declared(t, file, "https://bot.example.com", s.URL)
	// Named .txt but holding a PDF: the part's type is sniffed from what
	// the file holds, not guessed from its name.
	content := "%PDF-1.4 fake plans\n%%EOF\n"
	upload := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(upload, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Do(context.Background(), d, "document", map[string]string{"chat": "987654321", "path": upload, "caption": "the shed from the north"}, token, noSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || !res.OK() || string(res.Answer) != `{"ok":true,"result":{"message_id":7}}` {
		t.Fatalf("result = %+v", res)
	}
	got := s.got()
	if len(got) != 1 || got[0].Method != "POST" || got[0].URI != "/bot"+token+"/sendDocument" {
		t.Fatalf("sent = %+v", got)
	}
	want := []uploadPart{
		{"caption", "", "", "the shed from the north"},
		{"chat_id", "", "", "987654321"},
		{"document", "notes.txt", "application/pdf", content},
	}
	if parts := uploadParts(t, got[0]); !slices.Equal(parts, want) {
		t.Fatalf("parts = %+v, want %+v", parts, want)
	}
	if _, err := Do(context.Background(), d, "document", map[string]string{"chat": "987654321", "path": upload}, token, noSecrets); err != nil {
		t.Fatal(err)
	}
	if parts := uploadParts(t, s.got()[1]); !slices.Equal(parts, want[1:]) {
		t.Fatalf("parts without a caption = %+v, want %+v", parts, want[1:])
	}

	// What is no string goes as its JSON: a whole number, a boolean and
	// a nested value each one text field, beside the file part.
	const typed = `name: typed
base: https://typed.example.com
calls:
  upload:
    kind: update
    params:
      path: { type: file }
      limit: { type: int }
      open: { type: bool }
      tag: { type: string }
    request:
      method: POST
      path: /upload
      body: { file: "{path}", limit: "{limit}", open: "{open}", meta: { tag: "{tag}" }, note: "for {tag}" }
`
	d = declared(t, typed, "https://typed.example.com", s.URL)
	if _, err := Do(context.Background(), d, "upload", map[string]string{"path": upload, "limit": "20", "open": "true", "tag": "x"}, "", noSecrets); err != nil {
		t.Fatal(err)
	}
	want = []uploadPart{
		{"file", "notes.txt", "application/pdf", content},
		{"limit", "", "", "20"},
		{"meta", "", "", `{"tag":"x"}`},
		{"note", "", "", "for x"},
		{"open", "", "", "true"},
	}
	if parts := uploadParts(t, s.got()[2]); !slices.Equal(parts, want) {
		t.Fatalf("parts = %+v, want %+v", parts, want)
	}
	// CR and LF in a path's base must stay inside the quoted filename, not
	// create another header in the multipart body.
	tricky := filepath.Join(t.TempDir(), "notes\r\nX-Injected: yes.txt")
	if err := os.WriteFile(tricky, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Do(context.Background(), d, "upload", map[string]string{"path": tricky, "limit": "20", "open": "true", "tag": "x"}, "", noSecrets); err != nil {
		t.Fatal(err)
	}
	trickyBody := string(s.got()[3].Body)
	if strings.Contains(trickyBody, "\r\nX-Injected:") || !strings.Contains(trickyBody, `filename="notes%0D%0AX-Injected: yes.txt"`) {
		t.Fatalf("filename was not safely escaped in multipart body: %q", trickyBody)
	}
	call, _ := d.Call("upload")
	params := map[string]string{"path": upload, "limit": "20", "open": "true", "tag": "x"}
	inputs, err := call.ReadInputs(params, noSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upload, []byte("changed after the card"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCallParamsWithInputs(d, "upload", params, inputs, noSecrets); err != nil {
		t.Fatalf("validation of the captured upload after its path changed: %v", err)
	}
	if _, err := DoKeyedWithInputs(context.Background(), d, "upload", params, "", "", inputs, noSecrets); err != nil {
		t.Fatal(err)
	}
	if parts := uploadParts(t, s.got()[4]); !slices.Equal(parts, want) {
		t.Fatalf("parts after the path changed = %+v, want the captured file bytes %+v", parts, want)
	}
}

// A file parameter is a path that can be read, within its bound: anything
// else refuses the call before anything is sent, naming the parameter and
// never the path.
func TestDoRefusesFileParameters(t *testing.T) {
	const file = `name: bot
base: https://bot.example.com
calls:
  document:
    kind: update
    params:
      chat: { type: string }
      path: { type: file, max: 8 }
    request:
      method: POST
      path: /sendDocument
      body: { chat_id: "{chat}", document: "{path}" }
`
	s := newStandin(t, jsonAnswer(`{}`))
	d := declared(t, file, "https://bot.example.com", s.URL)
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.txt")
	if err := os.WriteFile(ok, []byte("12345678"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, []byte("123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, want string }{
		{filepath.Join(dir, "missing.txt"), "parameter path is no file that can be read"},
		{dir, "parameter path is no file that can be read"},
		{big, "parameter path is 9 bytes, and at most 8"},
	} {
		_, err := Do(context.Background(), d, "document", map[string]string{"chat": "987654321", "path": tc.path}, "", noSecrets)
		if reason(err) != BadParam || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("path %q: err = %v, want %q", tc.path, err, tc.want)
		}
		if strings.Contains(err.Error(), tc.path) {
			t.Errorf("the refusal holds the path: %v", err)
		}
		clean(t, err)
	}
	if got := s.got(); len(got) != 0 {
		t.Fatalf("a refused call was sent: %+v", got)
	}
	// At the bound it goes.
	if _, err := Do(context.Background(), d, "document", map[string]string{"chat": "987654321", "path": ok}, "", noSecrets); err != nil {
		t.Fatal(err)
	}
	if got := s.got(); len(got) != 1 {
		t.Fatalf("sent = %+v", got)
	}
}

// The telegram pack's document call is an update sending a file to a chat,
// refused over the Bot API's 50 MB upload bound. It reads the pack's own
// file, so the wiring it pins is the pack's, not a copy's.
func TestTelegramDocumentSendsAFile(t *testing.T) {
	raw, err := os.ReadFile("../../packs/telegram/telegram.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse("telegram.yaml", raw)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := d.Call("document")
	if !ok || c.Kind != Update {
		t.Fatalf("document = %+v, %v: want an update call", c, ok)
	}
	if len(c.Params) != 3 || c.Params[0].Name != "chat" || c.Params[1].Name != "path" || c.Params[2].Name != "caption" {
		t.Fatalf("params = %+v", c.Params)
	}
	if p := c.Params[1]; p.Type != "file" || p.Max != 52428800 {
		t.Fatalf("path = %+v, want a file of at most 52428800 bytes", p)
	}
	if p := c.Params[2]; !p.Optional || p.Max != 1024 {
		t.Fatalf("caption = %+v, want an optional string of at most 1024", p)
	}
	s := newStandin(t, jsonAnswer(`{"ok":true,"result":{"message_id":8}}`))
	d.Base = s.URL
	dir := t.TempDir()
	big, err := os.Create(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(52428801); err != nil {
		t.Fatal(err)
	}
	big.Close()
	_, err = Do(context.Background(), d, "document", map[string]string{"chat": "987654321", "path": big.Name()}, "123:ABC", noSecrets)
	if reason(err) != BadParam || !strings.Contains(err.Error(), "parameter path is 52428801 bytes, and at most 52428800") {
		t.Fatalf("over 50 MB: err = %v", err)
	}
	if strings.Contains(err.Error(), big.Name()) {
		t.Fatalf("the refusal holds the path: %v", err)
	}
	// A small file goes to sendDocument as an upload.
	small := filepath.Join(dir, "shed.txt")
	if err := os.WriteFile(small, []byte("the shed, measured\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Do(context.Background(), d, "document", map[string]string{"chat": "987654321", "path": small, "caption": "tonight's build"}, "123:ABC", noSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("result = %+v", res)
	}
	got := s.got()
	if len(got) != 1 || got[0].URI != "/bot123:ABC/sendDocument" {
		t.Fatalf("sent = %+v", got)
	}
	want := []uploadPart{
		{"caption", "", "", "tonight's build"},
		{"chat_id", "", "", "987654321"},
		{"document", "shed.txt", "text/plain; charset=utf-8", "the shed, measured\n"},
	}
	if parts := uploadParts(t, got[0]); !slices.Equal(parts, want) {
		t.Fatalf("parts = %+v, want %+v", parts, want)
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
		res, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "t", "text": secretText}, token, noSecrets)
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
	_, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "t", "text": secretText}, token, noSecrets)
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

	res, err := Do(context.Background(), d, "all", nil, "", noSecrets)
	if reason(err) != TooLarge || res != nil {
		t.Fatalf("Do = %+v, %v; want too_large", res, err)
	}
	if !strings.Contains(err.Error(), "over the call's bound of 65536") || !strings.Contains(err.Error(), "refused, not cut") {
		t.Fatalf("the error does not name the cause: %v", err)
	}
	if res, err := Do(context.Background(), d, "raised", nil, "", noSecrets); err != nil || len(res.Answer) != len(answer(size)) {
		t.Fatalf("with answer.max raised: %v", err)
	}
	if res, err := Do(context.Background(), d, "picked", nil, "", noSecrets); err != nil || string(res.Answer) != `{"id":7}` {
		t.Fatalf("with a select that brings it under the bound: %+v, %v", res, err)
	}

	size = AnswerLimit + 1
	for _, call := range []string{"all", "raised", "picked"} {
		_, err := Do(context.Background(), d, call, nil, "", noSecrets)
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
	if _, err := Do(context.Background(), d, "ping", nil, token, noSecrets); reason(err) != NotJSON || !strings.Contains(err.Error(), "answered 200 with what is no JSON") {
		t.Fatalf("not JSON: %v", err)
	}
	status, body = 401, `{"error":"bad token `+token+`"}`
	res, err := Do(context.Background(), d, "ping", nil, token, noSecrets)
	if err != nil || res.Status != 401 || res.OK() || res.Answer != nil {
		t.Fatalf("a 401 = %+v, %v", res, err)
	}
	status, body = 500, "<html>down</html>"
	if res, err := Do(context.Background(), d, "ping", nil, token, noSecrets); err != nil || res.Status != 500 || res.Answer != nil {
		t.Fatalf("a 500 = %+v, %v", res, err)
	}
	status, body = 204, ""
	if res, err := Do(context.Background(), d, "ping", nil, token, noSecrets); err != nil || !res.OK() || res.Answer != nil {
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
	_, err := Do(context.Background(), d, "message", map[string]string{"user": user30, "title": "t", "text": secretText}, token, noSecrets)
	if reason(err) != TimedOut || !strings.Contains(err.Error(), "nothing within 100ms") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the call took %s", took)
	}
	clean(t, err)

	// A call cut by its caller's own deadline names the caller's bound,
	// not Timeout's.
	Timeout = time.Minute
	params := map[string]string{"user": user30, "title": "t", "text": secretText}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = Do(ctx, d, "message", params, token, noSecrets)
	if reason(err) != TimedOut || !strings.Contains(err.Error(), "nothing within the caller's 1s") {
		t.Fatalf("caller-cut err = %v", err)
	}
	clean(t, err)
}

func TestRoundBound(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{9999 * time.Millisecond, "10s"},    // whole seconds minus a breath
		{999 * time.Millisecond, "1s"},      // just under a second reads as one
		{1500 * time.Millisecond, "1.5s"},   // further off keeps its tenth
		{10400 * time.Millisecond, "10.4s"}, // past ten, tenths past whole
		{300 * time.Millisecond, "300ms"},
		{90 * time.Millisecond, "90ms"},
		{0, "0s"}, // a deadline already past leaves no wait
		{-time.Second, "0s"},
	} {
		if got := roundBound(tc.in).String(); got != tc.want {
			t.Errorf("roundBound(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// A service that is not there: the error names the host and the cause, and
// not the address with the call's values in it.
func TestDoUnreachable(t *testing.T) {
	s := newStandin(t, jsonAnswer(`{}`))
	d := declared(t, crm, "https://crm.example.com/api/v2", s.URL)
	s.Close()
	_, err := Do(context.Background(), d, "customer", map[string]string{"id": customer}, token, noSecrets)
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
		_, err := Do(context.Background(), d, "message", tc.params, token, noSecrets)
		if reason(err) != BadParam || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q: %v", tc.want, err)
		}
		clean(t, err)
	}
	if _, err := Do(context.Background(), d, "massage", ok, token, noSecrets); reason(err) != NoCall {
		t.Errorf("a call the file does not have: %v", err)
	}
	if _, err := Do(context.Background(), d, "message", ok, "", noSecrets); reason(err) != NoCredential || !strings.Contains(err.Error(), "does not set PUSHOVER_TOKEN") {
		t.Errorf("no credential: %v", err)
	}
	if _, err := Do(context.Background(), d, "message", ok, token+"\nX-Injected: 1", noSecrets); reason(err) != NoCredential {
		t.Errorf("a credential of two lines: %v", err)
	}

	// A value cannot leave the base through the path.
	c := declared(t, crm, "https://crm.example.com/api/v2", s.URL+"/api/v2")
	c.Calls[1].Params[0].Pattern, c.Calls[1].Params[0].re = "", nil
	for value, want := range map[string]string{"..": `makes a ".." segment`, ".": `makes a "." segment`, "": "parameter id is empty, and it is part of the path"} {
		if _, err := Do(context.Background(), c, "customer", map[string]string{"id": value}, token, noSecrets); reason(err) != BadParam || !strings.Contains(err.Error(), want) {
			t.Errorf("id %q: %v", value, err)
		}
	}
	if got := s.got(); len(got) != 0 {
		t.Fatalf("a refused call was sent: %+v", got)
	}
	if _, err := Do(context.Background(), c, "customer", map[string]string{"id": "../../admin"}, token, noSecrets); err != nil {
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
	if _, err := Do(context.Background(), d, "ping", nil, "", noSecrets); reason(err) != Refused || !strings.Contains(err.Error(), "plain http to 192.0.2.1, which is not this machine") {
		t.Fatalf("err = %v", err)
	}
	d.Base = "ftp://127.0.0.1"
	if _, err := Do(context.Background(), d, "ping", nil, "", noSecrets); reason(err) != Refused {
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
	if _, err := DoKeyed(ctx, d, "note", note, "write-tok", "k-1", noSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := DoKeyed(ctx, d, "customer", map[string]string{"id": "7"}, token, "k-2", noSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := Do(ctx, d, "note", note, "write-tok", noSecrets); err != nil {
		t.Fatal(err)
	}
	plain := declared(t, crm, "https://crm.example.com/api/v2", srv.URL)
	if _, err := DoKeyed(ctx, plain, "note", note, token, "k-3", noSecrets); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST Bearer write-tok k-1", "GET Bearer " + token + " ", "POST Bearer write-tok ", "POST Bearer " + token + " "}
	if strings.Join(keys, "|") != strings.Join(want, "|") {
		t.Fatalf("sent = %q, want %q", keys, want)
	}
	if _, err := DoKeyed(ctx, d, "note", note, "write-tok", "k\r\nX-Other: 1", noSecrets); reason(err) != BadParam || len(keys) != len(want) {
		t.Fatalf("a key with a line break = %v, sent %q", err, keys)
	}
	// Without the credential of an update, the error names its variable.
	if _, err := DoKeyed(ctx, d, "note", note, "", "k-4", noSecrets); reason(err) != NoCredential || !strings.Contains(err.Error(), "CRM_WRITE_TOKEN") {
		t.Fatalf("no credential = %v", err)
	}
}

// hookSecret is the path of a webhook's URL. Zapier, Make and n8n make that
// path the secret of theirs. No error of a call to it may have it, nor the
// host it is at.
const hookSecret = "s3cr3t-hook-path-7q2x"

// webhookDeclaration is the webhook file, read.
func webhookDeclaration(t *testing.T) *Declaration {
	t.Helper()
	d, err := Parse("webhook.yaml", []byte(webhook))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// addressLeak is what err says of a credential that is the address and no
// error of a call may: the address whole, its host with or without the
// port, or the secret path.
func addressLeak(err error, address string) string {
	if err == nil {
		return ""
	}
	u, perr := url.Parse(address)
	if perr != nil {
		return ""
	}
	said := strings.ToLower(err.Error())
	for _, s := range []string{address, u.Host, u.Hostname(), hookSecret} {
		if s != "" && strings.Contains(said, strings.ToLower(s)) {
			return s
		}
	}
	return ""
}

// A call to a credential that is the address goes to that address as it
// was given and to no other: with the trailing slash the service made it
// with, or none, an escape as written, the call's own query after it, and
// the body the call has. It sends no credential in a header.
func TestDoSendsToTheCredentialThatIsTheAddress(t *testing.T) {
	s := newStandin(t, jsonAnswer(`{"status":"success"}`))
	d := webhookDeclaration(t)
	ctx := context.Background()
	for _, path := range []string{
		"/hooks/catch/123456/" + hookSecret + "/",
		"/hooks/" + hookSecret,
		"/h%2Fx/" + hookSecret,
		"/",
	} {
		res, err := Do(ctx, d, "send", map[string]string{"text": "hello"}, s.URL+path, noSecrets)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !res.OK() || string(res.Answer) != `{"status":"success"}` {
			t.Fatalf("%s: result = %+v", path, res)
		}
		got := s.got()
		last := got[len(got)-1]
		if last.Method != "POST" || last.URI != path || last.Auth != "" || last.ContentType != "application/json" || string(last.Body) != `{"text":"hello"}` {
			t.Fatalf("%s: sent = %+v", path, last)
		}
	}
	// No path at all in the address is the path /, which a request has.
	if _, err := Do(ctx, d, "ping", nil, s.URL, noSecrets); err != nil {
		t.Fatal(err)
	}
	if got := s.got(); got[len(got)-1].URI != "/" {
		t.Fatalf("an address with no path = %+v", got[len(got)-1])
	}
	// The call's own query follows the address, which has none.
	if _, err := Do(ctx, d, "tagged", map[string]string{"tag": "a b&c"}, s.URL+"/hooks/"+hookSecret, noSecrets); err != nil {
		t.Fatal(err)
	}
	if got := s.got(); got[len(got)-1].URI != "/hooks/"+hookSecret+"?tag=a+b%26c" || string(got[len(got)-1].Body) != `{"a":1}` {
		t.Fatalf("a call with a query = %+v", got[len(got)-1])
	}
	// A body built by the call's command is sent unchanged.
	if _, err := DoKeyedBody(ctx, d, "send", map[string]string{"text": "x"}, s.URL+"/hooks/"+hookSecret, "", json.RawMessage(`{"built":[1,2]}`), noSecrets); err != nil {
		t.Fatal(err)
	}
	if got := s.got(); got[len(got)-1].URI != "/hooks/"+hookSecret || string(got[len(got)-1].Body) != `{"built":[1,2]}` {
		t.Fatalf("a built body = %+v", got[len(got)-1])
	}
}

// The answer of an endpoint the operator pointed the URL at is not the
// declaration's to describe. A success in JSON is returned. A success that
// says nothing, or says it in words (Make answers Accepted), is a success
// with no answer. Anything else is its status alone.
func TestDoReadsTheAnswerOfACredentialThatIsTheAddress(t *testing.T) {
	status, body := 200, `{"attempt":"a-1","status":"success"}`
	s := newStandin(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
	d := webhookDeclaration(t)
	address := s.URL + "/hooks/" + hookSecret
	for _, tc := range []struct {
		status          int
		body, want      string
		wantBytes       bool
		wantOK, wantErr bool
	}{
		{200, `{"attempt":"a-1","status":"success"}`, `{"attempt":"a-1","status":"success"}`, true, true, false},
		{200, "Accepted", "", true, true, false},
		{200, "<html>moved</html>", "", true, true, false},
		{204, "", "", false, true, false},
		{401, `{"error":"no"}`, "", false, false, false},
		{500, "<html>down</html>", "", false, false, false},
	} {
		status, body = tc.status, tc.body
		res, err := Do(context.Background(), d, "send", map[string]string{"text": "hello"}, address, noSecrets)
		if err != nil {
			t.Fatalf("%d %q: %v", tc.status, tc.body, err)
		}
		if res.Status != tc.status || res.OK() != tc.wantOK || string(res.Answer) != tc.want || (res.Bytes > 0) != tc.wantBytes {
			t.Errorf("%d %q: result = %+v", tc.status, tc.body, res)
		}
	}
}

// A credential that is not an address a call may go to is refused before
// anything is sent, in words that name what is wrong and none of the value,
// which is a secret; a call without one, or with a line break in it, is
// refused as any credential is.
func TestDoRefusesACredentialThatIsNotAnAddress(t *testing.T) {
	s := newStandin(t, jsonAnswer(`{}`))
	d := webhookDeclaration(t)
	hostile := "https://hooks.example.com/" + hookSecret
	for _, tc := range []struct{ value, want string }{
		{"http://hooks.example.com/" + hookSecret, "is plain http to a machine that is not this one"},
		{"ftp://hooks.example.com/" + hookSecret, "is not https://"},
		{"https://me:" + hookSecret + "@hooks.example.com/hooks", "has a user or password in it"},
		{hostile + "?key=" + hookSecret, "has a query or a fragment"},
		{hostile + "#" + hookSecret, "has a query or a fragment"},
		{"https://hooks.example.com/../" + hookSecret, "has a '..', '//' or '\\' in its path"},
		{"https://hooks.example.com//" + hookSecret, "has a '..', '//' or '\\' in its path"},
		{"hooks.example.com/" + hookSecret, "is no address"},
		{"https:///" + hookSecret, "is no address"},
		{"https://hooks.example.com:" + hookSecret + "/x", "is no address"},
	} {
		_, err := Do(context.Background(), d, "send", map[string]string{"text": "hello"}, tc.value, noSecrets)
		if reason(err) != Refused || !strings.Contains(err.Error(), "the value of WEBHOOK_URL "+tc.want) {
			t.Errorf("%q: err = %v, want %q", tc.value, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), hookSecret) || strings.Contains(err.Error(), "hooks.example.com") {
			t.Errorf("%q: the error repeats the value: %v", tc.value, err)
		}
	}
	if _, err := Do(context.Background(), d, "send", map[string]string{"text": "hello"}, "", noSecrets); reason(err) != NoCredential || !strings.Contains(err.Error(), "does not set WEBHOOK_URL") {
		t.Errorf("no credential: %v", err)
	}
	if _, err := Do(context.Background(), d, "send", map[string]string{"text": "hello"}, s.URL+"/"+hookSecret+"\nHost: elsewhere", noSecrets); reason(err) != NoCredential || strings.Contains(err.Error(), hookSecret) {
		t.Errorf("a credential of two lines: %v", err)
	}
	if got := s.got(); len(got) != 0 {
		t.Fatalf("a refused call was sent: %+v", got)
	}
}

// Nothing that fails a call to a credential that is the address says the
// address: not the host it is at and not its path, whether the service is
// not there, redirects, or answers too much.
func TestDoSaysNoAddressOfACredentialThatIsTheAddress(t *testing.T) {
	d := webhookDeclaration(t)
	params := map[string]string{"text": "hello"}

	gone := newStandin(t, jsonAnswer(`{}`))
	address := gone.URL + "/hooks/" + hookSecret
	gone.Close()
	_, err := Do(context.Background(), d, "send", params, address, noSecrets)
	if reason(err) != Unreachable || !strings.Contains(err.Error(), "no answer from the credential's URL: ") {
		t.Fatalf("unreachable: %v", err)
	}
	if got := addressLeak(err, address); got != "" {
		t.Fatalf("unreachable says %q: %v", got, err)
	}

	elsewhere := newStandin(t, jsonAnswer(`{}`))
	moved := newStandin(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/hooks/"+hookSecret, http.StatusFound)
	})
	address = moved.URL + "/hooks/" + hookSecret
	_, err = Do(context.Background(), d, "send", params, address, noSecrets)
	if reason(err) != Redirect || !strings.Contains(err.Error(), "the credential's URL answered 302, a redirect: yardr follows none") {
		t.Fatalf("redirect: %v", err)
	}
	if got := addressLeak(err, address); got != "" {
		t.Fatalf("redirect says %q: %v", got, err)
	}
	if got := addressLeak(err, elsewhere.URL+"/hooks/"+hookSecret); got != "" && got != hookSecret {
		t.Fatalf("redirect names where it points, %q: %v", got, err)
	}
	if got := elsewhere.got(); len(got) != 0 {
		t.Fatalf("the redirect was followed: %+v", got)
	}

	big := newStandin(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"pad":"`+strings.Repeat("x", AnswerLimit)+`"}`)
	})
	address = big.URL + "/hooks/" + hookSecret
	_, err = Do(context.Background(), d, "send", params, address, noSecrets)
	if reason(err) != TooLarge {
		t.Fatalf("too large: %v", err)
	}
	if got := addressLeak(err, address); got != "" {
		t.Fatalf("too large says %q: %v", got, err)
	}

	// The network's own words name the host, which a lookup that failed or
	// a certificate that does not fit says: they are taken out, the rest
	// of what they say is left.
	secretHost := "hooks-" + hookSecret + ".example.com"
	address = "https://" + secretHost + ":8443/hooks/" + hookSecret
	u, _ := url.Parse(address)
	for _, text := range []string{
		"dial tcp: lookup " + secretHost + " on 10.0.0.1:53: no such host",
		"dial tcp: lookup " + secretHost + ": no such host",
		"tls: failed to verify certificate: x509: certificate is valid for other.example.com, not " + secretHost,
		"dial tcp " + u.Host + ": i/o timeout",
		"dial tcp: lookup " + strings.ToUpper(secretHost) + ": no such host",
		"Post \"" + address + "\": EOF",
	} {
		got := hiding(address, u).ReplaceAllLiteralString(text, redacted)
		if addressLeak(errors.New(got), address) != "" {
			t.Errorf("%q is left as %q", text, got)
		}
		if !strings.Contains(got, "[address redacted]") {
			t.Errorf("%q is left as %q, which says nothing was taken out", text, got)
		}
	}
	if got := hiding(address, u).ReplaceAllLiteralString("dial tcp 10.1.2.3:8443: connect: connection refused", redacted); got != "dial tcp 10.1.2.3:8443: connect: connection refused" {
		t.Errorf("words that name no part of the address are changed: %q", got)
	}
}

// No command is handed a credential that is the address: a call goes
// there, and none needs where. One that is a token still is, under its name.
func TestCommandsAreHandedNoCredentialThatIsTheAddress(t *testing.T) {
	address := "https://hooks.example.com/hooks/" + hookSecret
	hook := webhookDeclaration(t)
	for _, kind := range []string{Read, Update} {
		for _, kv := range hook.CommandEnvOf(kind, address) {
			if strings.Contains(kv, hookSecret) || strings.HasPrefix(kv, "WEBHOOK_URL=") {
				t.Errorf("%s: the environment has %q", kind, kv)
			}
		}
	}
	d, err := Parse("crm.yaml", []byte(crm))
	if err != nil {
		t.Fatal(err)
	}
	if env := strings.Join(d.CommandEnv(token), "\n"); !strings.Contains(env, "CRM_TOKEN="+token) {
		t.Errorf("a token is not handed to a command: %s", env)
	}
}

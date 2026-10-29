package hooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// The webhook pack held to what Zapier, Make and n8n send and take: the
// envelope its normalize prints for the bodies they post (a flat object a
// Zap builds from its fields, a nested one an n8n workflow passes on, and
// words that are no JSON), the delivery that records a body with the right
// header and refuses one without it, and the call that posts a JSON object
// to a URL that is the credential. The shapes are the ones their own pages
// describe (packs/webhook/webhook.yaml names them); none is recorded, since
// what a sender posts is whatever its user built.

// webhookParse is the pack's declaration as the yard would read it.
func webhookParse(t *testing.T) (*source.Declaration, string, string) {
	t.Helper()
	dir := filepath.Join("..", "..", "packs", "webhook")
	text, err := os.ReadFile(filepath.Join(dir, "webhook.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "webhook.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	return d, dir, string(text)
}

// webhookDecl is the pack's declared source with its command files copied
// as yardr source add copies them. The commands are written in jq, so
// without it the tests that run them skip.
func webhookDecl(t *testing.T) hooks.Declared {
	t.Helper()
	needPackTools(t)
	d, dir, _ := webhookParse(t)
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return hooks.Declared{D: d, Home: home}
}

// The pack declares what the setup needs: a hook verified by the secret in
// its header, with a delivery id where the sender has one and unique bodies,
// no keys, and one update call to a credential that is the
// address. Nothing else: no base, no read call, no check, no thread, link
// or notify key.
func TestWebhookPackDeclaration(t *testing.T) {
	d, _, _ := webhookParse(t)
	if d.Name != "webhook" {
		t.Fatalf("name = %q", d.Name)
	}
	h := d.Hook
	if h == nil {
		t.Fatal("the pack declares no hook key")
	}
	if h.Verify.Scheme != source.HookSecret || h.Verify.Signature.Header != "X-Yardr-Secret" {
		t.Errorf("verify = %+v, want scheme secret in X-Yardr-Secret", h.Verify)
	}
	if h.Verify.Delivery.Header != "X-Yardr-Delivery" || h.Verify.Delivery.Body != "" {
		t.Errorf("delivery = %+v, want the header X-Yardr-Delivery", h.Verify.Delivery)
	}
	if h.Bodies != source.BodiesUnique {
		t.Errorf("bodies = %q, want unique", h.Bodies)
	}
	if strings.Join(h.Normalize, " ") != "webhook/normalize" {
		t.Errorf("normalize = %q", h.Normalize)
	}
	if len(h.Keys) != 0 || h.Register != nil {
		t.Errorf("keys = %q, register = %+v, want neither", h.Keys, h.Register)
	}
	if d.Base != "" || d.Credential == nil || d.Credential.Env != "WEBHOOK_URL" || !d.Credential.URL || d.Credential.Header != "" || d.Credential.InPath {
		t.Errorf("base %q, credential %+v: want WEBHOOK_URL as the address and nothing else", d.Base, d.Credential)
	}
	if d.Hear != nil || d.Thread != nil || d.Link != nil || d.Notify != nil || d.Check != "" || d.Idempotency != "" {
		t.Error("the pack declares a hear, thread, link, notify, check or idempotency key: it hears a POST and sends one, nothing else")
	}
	if len(d.Calls) != 1 {
		t.Fatalf("calls = %+v, want send alone", d.Calls)
	}
	c := d.Calls[0]
	if c.Name != "send" || c.Kind != source.Update || c.Request.Method != "POST" || c.Request.Line() != "POST /" || c.Request.Body != nil ||
		strings.Join(c.Build, " ") != "webhook/build-send" {
		t.Errorf("call = %+v, want an update that POSTs the body build-send prints", c)
	}
	if len(c.Params) != 1 || c.Params[0].Name != "data" || c.Params[0].Type != "string" || c.Params[0].Max != 65536 || c.Params[0].Optional || c.Params[0].Log {
		t.Errorf("params = %+v, want data, a required unlogged string of at most 64 KB", c.Params)
	}
	var commands []string
	for _, c := range d.Commands() {
		commands = append(commands, c.Key+" "+strings.Join(c.Argv, " "))
	}
	if want := []string{"hook.normalize webhook/normalize", "call.send.build webhook/build-send"}; !reflect.DeepEqual(commands, want) {
		t.Errorf("commands = %q, want %q", commands, want)
	}
}

// The envelopes for what the three post: a flat object with the fields a
// Zap maps (the title stands in for the text, the actor is a bare name), a
// nested one with an actor object and the workflow's own data beside the
// fields, and words that are no JSON at all, which are the delivery's text.
func TestWebhookPackNormalize(t *testing.T) {
	ad := webhookDecl(t)
	for _, tc := range []struct {
		name string
		body string
		want hooks.Envelope
	}{
		{"a flat object from a Zap",
			`{"type":"Lead","action":"created","title":"New lead from the pricing page","url":"https://crm.example.com/leads/7","actor":"ada@example.com","company":"Analytical Engines"}`,
			hooks.Envelope{Source: "webhook", Type: "lead", Action: "created", Actor: hooks.Party{ID: "ada@example.com", Name: "ada@example.com"},
				Mentions: []string{}, Text: "New lead from the pricing page", URL: "https://crm.example.com/leads/7"}},
		{"a nested object from an n8n workflow",
			`{"type":"order","action":"paid","text":"Order 1042 paid","url":"https://shop.example.com/orders/1042","actor":{"id":"u-9","name":"Ada Lovelace"},"order":{"id":1042,"lines":[{"sku":"A-1","qty":2}]}}`,
			hooks.Envelope{Source: "webhook", Type: "order", Action: "paid", Actor: hooks.Party{ID: "u-9", Name: "Ada Lovelace"},
				Mentions: []string{}, Text: "Order 1042 paid", URL: "https://shop.example.com/orders/1042"}},
		{"words that are no JSON",
			"backup ok\n\nnightly run finished   in 4 minutes\n",
			hooks.Envelope{Source: "webhook", Type: "event", Mentions: []string{}, Text: "backup ok nightly run finished in 4 minutes"}},
		// The command is handed the body decoded, so its text is the compact
		// JSON with the keys of every object in order.
		{"an object that names nothing the yard reads",
			`{"paid":true,"order":{"id":1042,"lines":[{"sku":"A-1","qty":2}]}}`,
			hooks.Envelope{Source: "webhook", Type: "event", Mentions: []string{}, Text: `{"order":{"id":1042,"lines":[{"qty":2,"sku":"A-1"}]},"paid":true}`}},
	} {
		got, err := ad.Normalize(nil, []byte(tc.body))
		if err != nil {
			t.Errorf("%s: normalize: %v", tc.name, err)
			continue
		}
		if !reflect.DeepEqual(got, &tc.want) {
			wantRaw, _ := json.MarshalIndent(tc.want, "", "  ")
			gotRaw, _ := json.MarshalIndent(got, "", "  ")
			t.Errorf("%s: got %s\nwant %s", tc.name, gotRaw, wantRaw)
		}
	}
}

// The shapes around them: every body is an envelope, whatever it is. A
// body that is JSON but no object is its own compact text; a title stands in
// for a missing or blank text and never beats one; a link that is no web
// address is left out; a type is lowercase and in one line, and `event`
// where the body names none or names a number; an actor is an object's id
// and name (a number is an id too) or a bare name, and nothing else; a long
// text is cut where hooks.truncate cuts it.
func TestWebhookPackNormalizeShapes(t *testing.T) {
	ad := webhookDecl(t)
	long := strings.Repeat("word ", 200) // 1000 characters, collapsed and cut
	wholeArray := `[` + strings.Repeat(`"word",`, 200) + `"end"]`
	for _, tc := range []struct {
		name       string
		body       string
		wantType   string
		wantAction string
		wantText   string
		wantURL    string
		wantActor  hooks.Party
	}{
		{"a JSON string", `"hello   world"`, "event", "", "hello world", "", hooks.Party{}},
		{"a JSON array", `[{"a":1},{"a":2}]`, "event", "", `[{"a":1},{"a":2}]`, "", hooks.Party{}},
		{"a JSON number", `42`, "event", "", "42", "", hooks.Party{}},
		{"a JSON null", `null`, "event", "", "null", "", hooks.Party{}},
		{"an empty object", `{}`, "event", "", "{}", "", hooks.Party{}},
		{"a title with no text", `{"title":"  Deploy finished  "}`, "event", "", "Deploy finished", "", hooks.Party{}},
		{"a text and a title", `{"text":"the text","title":"the title"}`, "event", "", "the text", "", hooks.Party{}},
		{"a blank text", `{"text":"   ","title":"the title"}`, "event", "", "the title", "", hooks.Party{}},
		{"a text that is no string", `{"text":{"a":1},"title":"the title"}`, "event", "", "the title", "", hooks.Party{}},
		{"a type and an action in the sender's case", `{"type":"  New  LEAD ","action":" Created ","text":"x"}`, "new lead", "Created", "x", "", hooks.Party{}},
		{"a type that is a number", `{"type":7,"action":false,"text":"x"}`, "event", "", "x", "", hooks.Party{}},
		{"a link that runs a script", `{"text":"x","url":"javascript:alert(1)"}`, "event", "", "x", "", hooks.Party{}},
		{"a link that is a file", `{"text":"x","url":"file:///etc/passwd"}`, "event", "", "x", "", hooks.Party{}},
		{"a link with a space", `{"text":"x","url":"https://example.com/a b"}`, "event", "", "x", "", hooks.Party{}},
		{"a plain http link", `{"text":"x","url":"http://intranet.example.com/a"}`, "event", "", "x", "http://intranet.example.com/a", hooks.Party{}},
		{"an actor with a number for an id", `{"text":"x","actor":{"id":7,"name":"Ada"}}`, "event", "", "x", "", hooks.Party{ID: "7", Name: "Ada"}},
		{"an actor of a name alone", `{"text":"x","actor":{"name":"Ada"}}`, "event", "", "x", "", hooks.Party{Name: "Ada"}},
		{"an actor whose name is no text", `{"text":"x","actor":{"id":"u-1","name":{"first":"Ada"}}}`, "event", "", "x", "", hooks.Party{ID: "u-1"}},
		{"an actor that is a number", `{"text":"x","actor":12}`, "event", "", "x", "", hooks.Party{}},
		{"an actor that is a list", `{"text":"x","actor":["ada"]}`, "event", "", "x", "", hooks.Party{}},
		{"a text in many lines", "{\"text\":\"line one\\n\\n  line two\\t\\ttabbed\"}", "event", "", "line one line two tabbed", "", hooks.Party{}},
		{"a long text is cut", `{"text":"` + long + `"}`, "event", "", strings.TrimRight(long, " ")[:499] + "…", "", hooks.Party{}},
		{"a long body is cut", wholeArray, "event", "", wholeArray[:499] + "…", "", hooks.Party{}},
	} {
		got, err := ad.Normalize(nil, []byte(tc.body))
		if err != nil {
			t.Errorf("%s: normalize: %v", tc.name, err)
			continue
		}
		if got.Source != "webhook" || got.Type != tc.wantType || got.Action != tc.wantAction || got.Text != tc.wantText ||
			got.URL != tc.wantURL || got.Actor != tc.wantActor {
			t.Errorf("%s: got %+v, want type %q action %q text %q url %q actor %+v", tc.name, got, tc.wantType, tc.wantAction, tc.wantText, tc.wantURL, tc.wantActor)
		}
		if got.Thread != (hooks.Thread{}) || len(got.Mentions) != 0 || got.Fields != nil || got.Entity != "" || got.Delivery != "" {
			t.Errorf("%s: got %+v, want no thread, mention, field, entity or delivery of the sender's making", tc.name, got)
		}
	}
}

// A normalize that exits non-zero is answered 400, which a sender retries.
// So the script answers an envelope for every body and never prints any of
// it: on success it prints nothing to stderr at all.
func TestWebhookPackNormalizeNeverPrintsBody(t *testing.T) {
	ad := webhookDecl(t)
	prog := filepath.Join(ad.Home, "sources", ad.D.Name, ad.D.Hook.Normalize[0])
	const marker = "MARKER-zz07-never-printed"
	for name, body := range map[string]any{
		"an object":          map[string]any{"text": marker, "actor": map[string]any{"id": marker}, "url": marker},
		"a nested object":    map[string]any{"text": map[string]any{"deep": []any{1, 2, map[string]any{"x": marker}}}},
		"a string":           marker,
		"an array":           []any{marker, nil, true},
		"a number":           1.5,
		"null":               nil,
		"a truth":            true,
		"a very long string": strings.Repeat(marker, 1000),
	} {
		in, err := json.Marshal(map[string]any{"headers": map[string]any{}, "body": body})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(prog)
		cmd.Stdin = bytes.NewReader(in)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Errorf("%s: %v, stderr %q", name, err, stderr.String())
			continue
		}
		if stderr.Len() != 0 {
			t.Errorf("%s: stderr = %q, want nothing", name, stderr.String())
		}
		var e hooks.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &e); err != nil || e.Type == "" {
			t.Errorf("%s: stdout is no envelope: %q, %v", name, stdout.String(), err)
		}
	}
}

// The hook is verified by the secret in its header, compared as it arrives,
// and nowhere else: a bearer token is none of its business. A sender's own
// id of a delivery is read from X-Yardr-Delivery where it sends one, and
// repeated bodies are duplicates.
func TestWebhookPackVerify(t *testing.T) {
	d, _, _ := webhookParse(t)
	ad := hooks.Declared{D: d}
	body := []byte(`{"text":"hello"}`)
	verify := func(headers map[string]string) (string, error) {
		req := httptest.NewRequest(http.MethodPost, "/hooks/zapier", bytes.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return ad.Verify(req, body, []byte("s3cret"), time.Now())
	}
	if id, err := verify(map[string]string{"X-Yardr-Secret": "s3cret"}); err != nil || id != "" {
		t.Fatalf("accept: id %q, err %v", id, err)
	}
	if id, err := verify(map[string]string{"X-Yardr-Secret": "s3cret", "X-Yardr-Delivery": "run-7"}); err != nil || id != "run-7" {
		t.Fatalf("accept with a delivery id: id %q, err %v", id, err)
	}
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"wrong secret", map[string]string{"X-Yardr-Secret": "nope"}},
		{"missing header", nil},
		{"the secret in a bearer token", map[string]string{"Authorization": "Bearer s3cret"}},
		{"the secret in another header", map[string]string{"X-Webhook-Secret": "s3cret"}},
		{"a delivery id and no secret", map[string]string{"X-Yardr-Delivery": "run-7"}},
	} {
		if _, err := verify(tc.headers); !errors.Is(err, hooks.ErrUnauthorized) {
			t.Errorf("%s: err = %v, want unauthorized", tc.name, err)
		}
	}
	if ad.Bodies() != hooks.BodiesUniquePerSource {
		t.Errorf("bodies = %v, want unique per source", ad.Bodies())
	}
}

// webhookDepot is a depot whose receiver serves the pack's declared source
// with a hook named zapier: the delivery path as the yard runs it.
func webhookDepot(t *testing.T) *depot {
	t.Helper()
	needPackTools(t)
	home := t.TempDir()
	r := newDepot(t)
	r.rc = hooks.NewReceiver(r.own, home, nil)
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = r.rc.Handler()
	ctx := context.Background()
	d, dir, text := webhookParse(t)
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "webhook.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(home, "webhook.secret")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "zapier", Source: "webhook", SecretFile: secretFile}, "me"); err != nil {
		t.Fatal(err)
	}
	return r
}

// webhookPost is one body delivered to the zapier hook, with the headers
// given: its status and its answer.
func webhookPost(t *testing.T, r *depot, body []byte, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.srv.URL+"/hooks/zapier", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// A POST with the secret in its header is recorded and routed to a
// subscription on its type; a repeated body or delivery id is dropped;
// one without the header, or with a wrong secret, is refused with 401 and
// recorded nowhere; words that are no JSON are heard as they are.
func TestWebhookPackEndToEnd(t *testing.T) {
	r := webhookDepot(t)
	ctx := context.Background()
	if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "webhook",
		Scope: store.ScopeAll, Where: map[string]string{"type": "lead"}}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	events := func() int { return len(r.hookEvents(t)) }
	inbox := func() int {
		t.Helper()
		page, err := r.st.Inbox(ctx, "yardmaster", 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Total
	}
	signed := map[string]string{"X-Yardr-Secret": secret, "Content-Type": "application/json"}
	with := func(k, v string) map[string]string {
		h := map[string]string{}
		for hk, hv := range signed {
			h[hk] = hv
		}
		h[k] = v
		return h
	}
	body := []byte(`{"type":"lead","action":"created","text":"New lead: Ada"}`)
	// The delivery is recorded and in the inbox.
	if code, ans := webhookPost(t, r, body, signed); code != http.StatusOK || !strings.Contains(ans, `"seq"`) {
		t.Fatalf("the delivery answered %d %s", code, ans)
	}
	if events() != 1 || inbox() != 1 {
		t.Fatalf("after the delivery: %d hook events, inbox %d", events(), inbox())
	}
	// Bodies are unique per source, even without a delivery id.
	if code, ans := webhookPost(t, r, body, signed); code != http.StatusOK || !strings.Contains(ans, `"duplicate"`) {
		t.Fatalf("the same body again answered %d %s", code, ans)
	}
	if events() != 1 || inbox() != 1 {
		t.Fatalf("after the same body again: %d hook events, inbox %d", events(), inbox())
	}
	// A new body with an id is recorded, then the same id marks its retry.
	newBody := []byte(`{"type":"lead","action":"created","text":"New lead: Grace"}`)
	if code, ans := webhookPost(t, r, newBody, with("X-Yardr-Delivery", "run-1")); code != http.StatusOK || !strings.Contains(ans, `"seq"`) {
		t.Fatalf("a delivery with an id answered %d %s", code, ans)
	}
	retried := []byte(`{"type":"lead","action":"created","text":"New lead: Grace","retry":1}`)
	if code, ans := webhookPost(t, r, retried, with("X-Yardr-Delivery", "run-1")); code != http.StatusOK || !strings.Contains(ans, `"duplicate"`) {
		t.Fatalf("the retry answered %d %s", code, ans)
	}
	if events() != 2 || inbox() != 2 {
		t.Fatalf("after the retry: %d hook events, inbox %d", events(), inbox())
	}
	// Without the header, with a wrong secret, and with the secret as a
	// bearer token, the same body is refused, and recorded nowhere.
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"unsigned", map[string]string{"Content-Type": "application/json"}},
		{"wrong secret", with("X-Yardr-Secret", "nope")},
		{"a bearer token", map[string]string{"Authorization": "Bearer " + secret}},
	} {
		if code, _ := webhookPost(t, r, body, tc.headers); code != http.StatusUnauthorized {
			t.Fatalf("%s: answered %d, want 401", tc.name, code)
		}
	}
	if events() != 2 || inbox() != 2 {
		t.Fatalf("after the refusals: %d hook events, inbox %d", events(), inbox())
	}
	if got := r.refused(t, "unauthorized"); got != 3 {
		t.Fatalf("unauthorized count %d, want 3", got)
	}
	// Words that are no JSON are recorded as an event of type event, and
	// reach nobody whose subscription asks for leads.
	if code, ans := webhookPost(t, r, []byte("backup ok"), map[string]string{"X-Yardr-Secret": secret, "Content-Type": "text/plain"}); code != http.StatusOK || !strings.Contains(ans, `"seq"`) {
		t.Fatalf("words answered %d %s", code, ans)
	}
	if events() != 3 || inbox() != 2 {
		t.Fatalf("after the words: %d hook events, inbox %d", events(), inbox())
	}
	last := r.hookEvents(t)[2]
	env, _ := json.Marshal(last.Data)
	if !strings.Contains(string(env), `"type":"event"`) || !strings.Contains(string(env), `"text":"backup ok"`) || !strings.Contains(string(env), `"source":"webhook"`) {
		t.Fatalf("the event of the words = %s", env)
	}
}

// webhookStandin is the service a webhook URL is the address of: it keeps
// what it is sent, and answers as its test has it answer.
type webhookStandin struct {
	*httptest.Server
	mu     sync.Mutex
	posts  []webhookPosted
	status int
	answer string
}

type webhookPosted struct{ Method, URI, ContentType, Accept, Body string }

func newWebhookStandin(t *testing.T) *webhookStandin {
	t.Helper()
	s := &webhookStandin{status: http.StatusOK, answer: `{"status":"success"}`}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.posts = append(s.posts, webhookPosted{r.Method, r.RequestURI, r.Header.Get("Content-Type"), r.Header.Get("Accept"), string(body)})
		status, answer := s.status, s.answer
		s.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *webhookStandin) got() []webhookPosted {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]webhookPosted(nil), s.posts...)
}

func (s *webhookStandin) says(status int, answer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.answer = status, answer
}

// yardr call webhook send posts the object it is given, as it was written,
// to the URL in WEBHOOK_URL as the service made it, and says the service's
// answer where that is JSON. What is no object is refused before anything
// is sent. The URL is in no error and no event: not its path, not its host.
func TestWebhookPackSend(t *testing.T) {
	needPackTools(t) // the build command is written in jq
	d, dir, text := webhookParse(t)
	home := t.TempDir()
	s, err := store.Open(filepath.Join(home, "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	ctx := context.Background()
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	standin := newWebhookStandin(t)
	const hookPath = "/hooks/catch/123456/s3cr3t-hook-path-7q2x/"
	address := standin.URL + hookPath
	if _, err := s.PutSource(ctx, "webhook.yaml", text, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	envFile := filepath.Join(t.TempDir(), "webhook.env")
	if err := os.WriteFile(envFile, []byte("WEBHOOK_URL="+address+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSource(ctx, "webhook", store.SourceEnvFile, envFile, "jo"); err != nil {
		t.Fatal(err)
	}
	// Every refusal below is counted as a call, and an update is at most 10
	// an hour unless the source says more.
	if _, err := s.SetSource(ctx, "webhook", store.SourceUpdatesPerHour, "100", "jo"); err != nil {
		t.Fatal(err)
	}
	call := func(data string) (*store.SourceAnswer, error) {
		return s.CallSource(ctx, store.SourceCall{Source: "webhook", Call: "send", Actor: "jo", Params: map[string]string{"data": data}})
	}

	// The object is posted as written, spaces and digits as they were.
	for _, data := range []string{`{"a":1}`, `{ "id" : 9007199254740993123, "n": [1, 2.50, true], "s": "é\n" }`} {
		res, err := call(data)
		if err != nil || res.Status != http.StatusOK || string(res.Answer) != `{"status":"success"}` {
			t.Fatalf("send %s = %+v, %v", data, res, err)
		}
		got := standin.got()
		last := got[len(got)-1]
		if last.Method != "POST" || last.URI != hookPath || last.ContentType != "application/json" || last.Body != data {
			t.Fatalf("the service was sent %+v, want a POST of %s to %s as application/json", last, data, hookPath)
		}
	}
	// A service that answers in words (Make's Accepted) took the call: the
	// words are not kept.
	standin.says(http.StatusOK, "Accepted")
	if res, err := call(`{"a":2}`); err != nil || res.Status != http.StatusOK || len(res.Answer) != 0 {
		t.Fatalf("send to a service that says Accepted = %+v, %v", res, err)
	}
	// A service that refuses is its status, and nothing it said.
	standin.says(http.StatusNotFound, `{"error":"no such hook"}`)
	if res, err := call(`{"a":3}`); err != nil || res.Status != http.StatusNotFound || len(res.Answer) != 0 {
		t.Fatalf("send to a service that refuses = %+v, %v", res, err)
	}
	standin.says(http.StatusOK, `{"status":"success"}`)
	posted := len(standin.got())

	// What is no JSON object is refused with the build command's own words
	// and sends nothing.
	for _, data := range []string{`[1]`, `"text"`, `42`, `not json`, `{"a":1}{"b":2}`, `{"a":`, ` `} {
		_, err := call(data)
		var failed *source.CallError
		if !errors.As(err, &failed) || failed.Reason != source.BuildRefused || !strings.Contains(err.Error(), "data is no JSON object") {
			t.Fatalf("send %q = %v, want build_refused", data, err)
		}
	}
	if got := standin.got(); len(got) != posted {
		t.Fatalf("a refused call was sent: %+v", got[posted:])
	}
	// Over 64 KB is refused by the parameter, before the command is run.
	big := `{"pad":"` + strings.Repeat("x", 65536) + `"}`
	if _, err := call(big); err == nil || !strings.Contains(err.Error(), "parameter data is ") || !strings.Contains(err.Error(), "and at most 65536") {
		t.Fatalf("send over 64 KB = %v", err)
	}
	if got := standin.got(); len(got) != posted {
		t.Fatalf("a call over the bound was sent: %+v", got[posted:])
	}

	// A service that is gone: the error says so, and says nothing of where.
	standin.Close()
	_, err = call(`{"a":4}`)
	var failed *source.CallError
	if !errors.As(err, &failed) || failed.Reason != source.Unreachable {
		t.Fatalf("send to a service that is gone = %v", err)
	}
	hidden := []string{address, hookPath, "s3cr3t-hook-path-7q2x", strings.TrimPrefix(standin.URL, "http://"), "127.0.0.1"}
	for _, h := range hidden {
		if strings.Contains(err.Error(), h) {
			t.Errorf("the error says %q: %v", h, err)
		}
	}

	// The record of each call has the names of its parameters and not the
	// data, the credential or any part of the address.
	evs, err := s.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind != "source_call" {
			continue
		}
		n++
		raw, _ := json.Marshal(e.Data)
		for _, h := range append(hidden, `"a":`, "pad") {
			if strings.Contains(string(raw), h) {
				t.Errorf("a source_call event says %q: %s", h, raw)
			}
		}
		if !strings.Contains(string(raw), `"params":["data"]`) {
			t.Errorf("a source_call event does not name its parameter: %s", raw)
		}
	}
	if n == 0 {
		t.Fatal("no source_call event")
	}
}

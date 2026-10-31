package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/packs"
)

type registerHit struct {
	method, path, auth string
	body               map[string]any
	raw                string
}

// registerYard is a yard with a bot source whose hook the yard
// registers itself at a stand-in, and hook_public_url set. fail is
// the status the stand-in answers until the test clears it.
func registerYard(t *testing.T, hits *[]registerHit, status *int) *askYard {
	t.Helper()
	return registerYardText(t, hits, status, "bot", `
name: bot
base: http://127.0.0.1:9
credential: { env: BOT_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  setwebhook: { kind: update, params: { url: {type: string, max: 2048}, secret_token: {type: string, max: 256} }, request: { method: POST, path: /setWebhook, body: { url: "{url}", secret_token: "{secret_token}", allowed_updates: ["message"] } } }
hook:
  verify: { scheme: secret, signature: { header: X-Test-Secret } }
  bodies: unique
  normalize: [bot/normalize]
  keys: [chat]
  register: { call: setwebhook, url: url, secret: secret_token }
`)
}

// registerYardText is registerYard with the source and the declaration
// of the test's own: its base stands where the stand-in's URL goes.
func registerYardText(t *testing.T, hits *[]registerHit, status *int, name, text string) *askYard {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hit := registerHit{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), raw: string(b)}
		_ = json.Unmarshal(b, &hit.body)
		*hits = append(*hits, hit)
		if *status != http.StatusOK {
			w.WriteHeader(*status)
			_, _ = w.Write([]byte(`{"ok":false,"description":"no"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(fake.Close)
	y := hookYard(t)
	text = strings.Replace(text, "http://127.0.0.1:9", fake.URL, 1)
	if !strings.Contains(text, fake.URL) {
		t.Fatal("the test's declaration names no base for the stand-in")
	}
	if _, err := y.PutSource(y.ctx, name+".yaml", text, "mckean"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), name+".env")
	if err := os.WriteFile(env, []byte("BOT_TOKEN=the-token\nPOSTHOG_API_KEY=the-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, name, SourceEnvFile, env, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetSetting(y.ctx, HookPublicURLSetting, "https://yard.example/hooks", "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func TestHookRegisterMakesSecretAndCalls(t *testing.T) {
	t.Parallel()
	var hits []registerHit
	status := http.StatusOK
	y := registerYard(t, &hits, &status)

	// No secret is asked for a source that registers itself.
	if _, err := y.AddAsk(y.ctx, y.bead, Ask{Question: "Secret?", To: "mckean", Context: []string{"a", "b"}, Secret: &AskSecret{Hook: "bot", Source: "bot"}}, "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "makes the signing secret") {
		t.Fatalf("secret ask for a registering source: err=%v", err)
	}

	ask := y.ask(t, Ask{Question: "Set the bot hook up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook", "add", "bot", "--source", "bot"}}}, "yardmaster")
	got := y.asked(t, ask)
	for _, want := range []string{
		"it will call POST /setWebhook at http://127.0.0.1:",
		"to register https://yard.example/hooks/bot",
		"No secret is typed",
	} {
		if !strings.Contains(strings.Join(got.Ask.Run.Does, "\n"), want) {
			t.Errorf("register card without %q:\n%s", want, strings.Join(got.Ask.Run.Does, "\n"))
		}
	}

	// The hook add is weighty (an endpoint that takes outside input), and
	// this approval writes in phases: a refusal for want of the client's
	// word that its person saw the weight (weighed) comes before the first,
	// so no hook is made, no secret is written and no register call goes
	// out; the ask stays open for the approval below.
	want := "ask " + strconv.FormatUint(ask, 10) + ` is weighty (trust): approve it where its weight is shown; a client sends "weighed": true once its person has passed its gate`
	if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", false, nil); err == nil || err.Error() != want {
		t.Fatalf("approving the hook without the word = %v, want %q", err, want)
	}
	if _, err := y.GetHook(y.ctx, "bot"); err == nil {
		t.Fatal("the refused approval made the hook")
	}
	if _, err := os.Stat(HookSecretFile(y.home, "bot")); !os.IsNotExist(err) {
		t.Fatalf("the refused approval wrote the hook's secret (%v)", err)
	}
	if len(hits) != 0 {
		t.Fatalf("the refused approval made the register call %d times", len(hits))
	}
	if got := y.asked(t, ask); got.State != AskOpen {
		t.Fatalf("the ask is %s after the refusal, want it open", got.State)
	}

	// The hook add is a trust card, and a paired device approves it as it
	// approves any weighty card. The approval writes in two phases with the
	// register call between them, and both carry the device: the answer's
	// event, written last, names it and claims no witness.
	done, err := y.ApproveAsk(WithVia(y.ctx, "mckean", "phone"), y.bead, ask, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev := lastAnswer(t, y); Via(ev) != "phone" || Witness(ev) != "" {
		t.Fatalf("the hook approval's event is via %q witnessed %q, want phone with no witness", Via(ev), Witness(ev))
	}
	if len(hits) != 1 {
		t.Fatalf("register call made %d times", len(hits))
	}
	hit := hits[0]
	if hit.method != "POST" || hit.path != "/setWebhook" || hit.auth != "Bearer the-token" {
		t.Errorf("register hit = %s %s auth %q", hit.method, hit.path, hit.auth)
	}
	if hit.body["url"] != "https://yard.example/hooks/bot" {
		t.Errorf("register url = %v", hit.body["url"])
	}
	file := HookSecretFile(y.home, "bot")
	secret, _, err := ReadSecretFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if hit.body["secret_token"] != string(secret) {
		t.Errorf("register secret does not equal the hook's file")
	}
	if len(secret) < 43 {
		t.Errorf("made secret is %d characters, want 32 bytes at least", len(secret))
	}
	for _, c := range string(secret) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			t.Fatalf("made secret holds %q, outside A-Z a-z 0-9 _ -", c)
		}
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("secret file mode = %o, want 600", info.Mode().Perm())
	}
	hook, err := y.GetHook(y.ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if hook.Source != "bot" || hook.SecretFile != file {
		t.Errorf("hook = %+v", hook)
	}
	if !strings.Contains(done.Result, "hook bot saved: POST /hooks/bot (bot); registered with POST /setWebhook") {
		t.Errorf("approval = %q", done.Result)
	}
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	events, err := y.EventsOfKind(y.ctx, "hook_added", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{done.Result, strings.Join(done.Does, "\n")} {
		if strings.Contains(text, string(secret)) {
			t.Fatal("the made secret stands in an answer or a card")
		}
	}
	for _, n := range notes {
		if strings.Contains(n.Text, string(secret)) || strings.Contains(n.Kind, string(secret)) {
			t.Fatal("the made secret stands in a note")
		}
	}
	for _, e := range events {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), string(secret)) {
			t.Fatal("the made secret stands in an event")
		}
		if Via(e) != "phone" {
			t.Errorf("the hook_added event is via %q, want phone", Via(e))
		}
	}
	db, err := os.ReadFile(filepath.Join(y.home, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(db), string(secret)) {
		t.Fatal("the made secret stands in the store file")
	}
	t.Run("chosen region base", func(t *testing.T) {
		var defaultHits []registerHit
		status := http.StatusOK
		y := registerYard(t, &defaultHits, &status)
		var regionHits []registerHit
		region := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			hit := registerHit{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), raw: string(b)}
			_ = json.Unmarshal(b, &hit.body)
			regionHits = append(regionHits, hit)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		t.Cleanup(region.Close)
		src, err := y.GetSource(y.ctx, "bot")
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Replace(src.Text, "base: "+mustBase(t, src)+"\n", "base: "+mustBase(t, src)+"\nbases:\n  eu: "+region.URL+"\n", 1)
		if text == src.Text {
			t.Fatal("the test adds no region base")
		}
		if _, err := y.PutSource(y.ctx, "bot.yaml", text, "mckean"); err != nil {
			t.Fatal(err)
		}
		if _, err := y.SetSource(y.ctx, "bot", SourceBase, "eu", "mckean"); err != nil {
			t.Fatal(err)
		}
		ask := y.ask(t, Ask{Question: "Set the bot hook up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook", "add", "bot", "--source", "bot"}}}, "yardmaster")
		card := strings.Join(y.asked(t, ask).Ask.Run.Does, "\n")
		if !strings.Contains(card, "at "+region.URL+" to register") || strings.Contains(card, mustBase(t, src)) {
			t.Fatalf("the hook card does not name only the selected base:\n%s", card)
		}
		if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil); err != nil {
			t.Fatal(err)
		}
		// On the socket the same two-phase approval is witnessed a terminal's.
		if ev := lastAnswer(t, y); Witness(ev) != WitnessTerminal || Via(ev) != "" {
			t.Fatalf("the hook approval's event is witnessed %q via %q, want terminal with no device", Witness(ev), Via(ev))
		}
		if len(regionHits) != 1 || regionHits[0].path != "/setWebhook" || len(defaultHits) != 0 {
			t.Fatalf("region requests = %+v, default requests = %+v", regionHits, defaultHits)
		}
	})
	t.Run("asked parameters", func(t *testing.T) {
		var hits []registerHit
		status := http.StatusOK
		y := registerYardText(t, &hits, &status, "bot", `
name: bot
base: http://127.0.0.1:9
credential: { env: BOT_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  setwebhook: { kind: update, params: { url: {type: string, max: 2048}, secret_token: {type: string, max: 256}, project: {type: string, pattern: "^[0-9]+$", max: 20} }, request: { method: POST, path: /setWebhook, body: { url: "{url}", secret_token: "{secret_token}", project: "{project}" } } }
hook:
  verify: { scheme: secret, signature: { header: X-Test-Secret } }
  bodies: unique
  normalize: [bot/normalize]
  keys: [chat]
  register: { call: setwebhook, url: url, secret: secret_token, ask: [project] }
`)
		ask := y.ask(t, Ask{Question: "Set the bot hook up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook", "add", "bot", "--source", "bot"}}}, "yardmaster")
		got := y.asked(t, ask)
		if len(got.Ask.Run.Ask) != 1 || got.Ask.Run.Ask[0] != "project" {
			t.Fatalf("the card asks for %q, want [project]", got.Ask.Run.Ask)
		}
		if joined := strings.Join(got.Ask.Run.Does, "\n"); !strings.Contains(joined, "the approval gives project in the clear") {
			t.Fatalf("the card without the asked sentence:\n%s", joined)
		}
		// No value: the approval is refused and stores nothing, as a
		// weight refusal above stores nothing.
		if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil); err == nil ||
			!strings.Contains(err.Error(), "asks for project in the clear") ||
			!strings.Contains(err.Error(), "add --value project=<value>") {
			t.Fatalf("an approval without the asked value = %v", err)
		}
		if _, err := y.GetHook(y.ctx, "bot"); err == nil {
			t.Fatal("the refused approval made the hook")
		}
		if _, err := os.Stat(HookSecretFile(y.home, "bot")); !os.IsNotExist(err) {
			t.Fatalf("the refused approval wrote the hook's secret (%v)", err)
		}
		if len(hits) != 0 {
			t.Fatalf("the refused approval made the register call %d times", len(hits))
		}
		if got := y.asked(t, ask); got.State != AskOpen {
			t.Fatalf("the ask is %s after the refusal, want it open", got.State)
		}
		// A mistyped name is refused as one the card asks for nothing of.
		if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, map[string]string{"porject": "48291"}); err == nil ||
			!strings.Contains(err.Error(), "asks for nothing of") {
			t.Fatalf("an approval with a mistyped name = %v", err)
		}
		// A value the pattern refuses is refused in the call's words.
		if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, map[string]string{"project": "oak"}); err == nil ||
			!strings.Contains(err.Error(), "does not match its pattern") {
			t.Fatalf("an approval with a bad value = %v", err)
		}
		if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, map[string]string{"project": "48291"}); err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 {
			t.Fatalf("register call made %d times", len(hits))
		}
		if hits[0].body["project"] != "48291" {
			t.Errorf("register project = %v, want the asked one", hits[0].body["project"])
		}
	})
}

// The embedded posthog pack through the hook card: the approval POSTs
// the destination to a fake PostHog with the public URL, the generated
// secret in the header and the asked project; without the project the
// approval is refused and stores nothing.
func TestHookRegisterPostHogDestination(t *testing.T) {
	t.Parallel()
	raw, err := packs.FS.ReadFile("posthog/posthog.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var hits []registerHit
	status := http.StatusOK
	y := registerYardText(t, &hits, &status, "posthog",
		strings.ReplaceAll(string(raw), "https://us.posthog.com", "http://127.0.0.1:9"))
	ask := y.ask(t, Ask{Question: "Hook PostHog's new error issues up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook", "add", "posthog", "--source", "posthog"}}}, "yardmaster")
	got := y.asked(t, ask)
	if len(got.Ask.Run.Ask) != 1 || got.Ask.Run.Ask[0] != "project" {
		t.Fatalf("the card asks for %q, want [project]", got.Ask.Run.Ask)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil); err == nil ||
		!strings.Contains(err.Error(), "asks for project in the clear") {
		t.Fatalf("an approval without the project = %v", err)
	}
	if _, err := y.GetHook(y.ctx, "posthog"); err == nil {
		t.Fatal("the refused approval made the hook")
	}
	if len(hits) != 0 {
		t.Fatalf("the refused approval made the register call %d times", len(hits))
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, map[string]string{"project": "48291"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(done.Result, "registered with POST /api/projects/{project}/hog_functions/") {
		t.Errorf("approval = %q", done.Result)
	}
	if len(hits) != 1 {
		t.Fatalf("register call made %d times", len(hits))
	}
	hit := hits[0]
	if hit.method != "POST" || hit.path != "/api/projects/48291/hog_functions/" || hit.auth != "Bearer the-key" {
		t.Fatalf("register hit = %s %s auth %q", hit.method, hit.path, hit.auth)
	}
	body := hit.body
	if body["name"] != "yardr" || body["type"] != "internal_destination" ||
		body["template_id"] != "template-webhook" || body["enabled"] != true {
		t.Errorf("destination = %v", body)
	}
	inputs, _ := body["inputs"].(map[string]any)
	value := func(input string) any {
		m, _ := inputs[input].(map[string]any)
		return m["value"]
	}
	if value("url") != "https://yard.example/hooks/posthog" {
		t.Errorf("destination url = %v", value("url"))
	}
	if value("method") != "POST" {
		t.Errorf("destination method = %v", value("method"))
	}
	wantBody := map[string]any{"event": "{event}", "person": "{person}", "project": "{project}"}
	if fmt.Sprint(value("body")) != fmt.Sprint(wantBody) {
		t.Errorf("destination body = %v, want %v", value("body"), wantBody)
	}
	secret, _, err := ReadSecretFile(HookSecretFile(y.home, "posthog"))
	if err != nil {
		t.Fatal(err)
	}
	headers, _ := value("secret_headers").(map[string]any)
	if headers["X-Yardr-Secret"] != string(secret) {
		t.Errorf("destination secret header is not the hook's made secret: %v", value("secret_headers"))
	}
	filters, _ := body["filters"].(map[string]any)
	events, _ := filters["events"].([]any)
	if filters["source"] != "internal-events" || len(events) != 1 {
		t.Fatalf("destination filters = %v", filters)
	}
	event, _ := events[0].(map[string]any)
	if event["id"] != "$error_tracking_issue_created" || event["type"] != "events" {
		t.Errorf("destination event = %v", event)
	}
}

// fakePostHog is the functions endpoint of a PostHog that commits what
// it is given: POSTs append to functions, GETs answer them back as a
// page, with secret inputs masked as PostHog answers them
// (mask_config_secrets). dropPost closes the first POST's connection
// without an answer, after committing it. postStatus and checkStatus
// answer those calls until the test changes them; a POST answered no
// success commits nothing, as a refused create does. ServeHTTP runs on
// the server's goroutine while the test reads what it saw, and the
// dropped POST answers nothing, so no round-trip orders the two: the
// lock guards every field, and the test reads through seen.
type fakePostHog struct {
	t           *testing.T
	mu          sync.Mutex
	functions   []map[string]any
	posts       int
	gets        int
	auths       []string
	queries     []url.Values
	getBodies   []string
	dropPost    bool
	postStatus  int
	checkStatus int
}

func newFakePostHog(t *testing.T) *fakePostHog {
	t.Helper()
	return &fakePostHog{t: t, postStatus: http.StatusCreated, checkStatus: http.StatusOK}
}

func (f *fakePostHog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	if r.Method == http.MethodPost {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		f.posts++
		if f.postStatus >= 200 && f.postStatus < 300 {
			f.functions = append(f.functions, body)
		}
		if f.dropPost && f.posts == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				f.t.Error("the fake cannot drop the POST's response: no hijacker")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				f.t.Errorf("the fake cannot drop the POST's response: %v", err)
				return
			}
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.postStatus)
		if f.postStatus >= 200 && f.postStatus < 300 {
			body["id"] = fmt.Sprintf("fn-%d", len(f.functions))
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		_, _ = w.Write([]byte(`{"type":"validation_error","detail":"no"}`))
		return
	}
	f.gets++
	f.queries = append(f.queries, r.URL.Query())
	if f.checkStatus < 200 || f.checkStatus >= 300 {
		w.WriteHeader(f.checkStatus)
		_, _ = w.Write([]byte(`{"type":"server_error","detail":"no"}`))
		return
	}
	// What the search keeps: a name or a description holding it. The
	// fake models the literal match alone, not the trigram around it.
	search := r.URL.Query().Get("search")
	var results []any
	for _, fn := range f.functions {
		name, _ := fn["name"].(string)
		description, _ := fn["description"].(string)
		if search != "" && !strings.Contains(name, search) && !strings.Contains(description, search) {
			continue
		}
		inputs, _ := fn["inputs"].(map[string]any)
		masked := map[string]any{}
		for k, v := range inputs {
			masked[k] = v
		}
		if _, ok := masked["secret_headers"]; ok {
			masked["secret_headers"] = map[string]any{"secret": true}
		}
		row := map[string]any{}
		for k, v := range fn {
			row[k] = v
		}
		row["inputs"] = masked
		results = append(results, row)
	}
	if results == nil {
		results = []any{}
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{"count": len(results), "next": nil, "previous": nil, "results": results})
	f.getBodies = append(f.getBodies, buf.String())
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buf.Bytes())
}

// postHogSeen is what the fake has seen and holds, copied out under
// its lock for the test's assertions.
type postHogSeen struct {
	posts, gets int
	functions   []map[string]any
	auths       []string
	queries     []url.Values
	getBodies   []string
}

func (f *fakePostHog) seen() postHogSeen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return postHogSeen{
		posts:     f.posts,
		gets:      f.gets,
		functions: copyFunctions(f.functions),
		auths:     append([]string(nil), f.auths...),
		queries:   append([]url.Values(nil), f.queries...),
		getBodies: append([]string(nil), f.getBodies...),
	}
}

// copyFunctions deep-copies stored destinations through JSON, so the
// test's assertions read their own maps: the fake writes an answered
// id into the body it stored.
func copyFunctions(fns []map[string]any) []map[string]any {
	out := make([]map[string]any, len(fns))
	for i, fn := range fns {
		b, _ := json.Marshal(fn)
		var c map[string]any
		_ = json.Unmarshal(b, &c)
		out[i] = c
	}
	return out
}

func (f *fakePostHog) setDropPost() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropPost = true
}

func (f *fakePostHog) setPostStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postStatus = status
}

func (f *fakePostHog) setCheckStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkStatus = status
}

// posthogCheckYard is a yard with the embedded posthog pack declared
// against the fake's URL, and hook_public_url set: the hook card's
// approval POSTs the destination to the fake and lists it back.
func posthogCheckYard(t *testing.T, fake *fakePostHog) *askYard {
	t.Helper()
	raw, err := packs.FS.ReadFile("posthog/posthog.yaml")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	y := hookYard(t)
	text := strings.ReplaceAll(string(raw), "https://us.posthog.com", server.URL)
	if !strings.Contains(text, server.URL) {
		t.Fatal("the test's declaration names no base for the stand-in")
	}
	if _, err := y.PutSource(y.ctx, "posthog.yaml", text, "mckean"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "posthog.env")
	if err := os.WriteFile(env, []byte("POSTHOG_API_KEY=the-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "posthog", SourceEnvFile, env, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetSetting(y.ctx, HookPublicURLSetting, "https://yard.example/hooks", "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

// A retry of the posthog hook card first lists what stands: where the
// first approval's create committed but its answer was lost, the retry
// adopts the standing destination instead of making another, and one
// function stands. The fake masks secret inputs as PostHog answers
// them, so the adoption proves the match is on the URL. A check that
// fails registers nothing blind.
func TestHookRegisterPostHogRetryChecksFirst(t *testing.T) {
	t.Parallel()
	const publicURL = "https://yard.example/hooks/posthog"
	t.Run("adopts the standing destination", func(t *testing.T) {
		fake := newFakePostHog(t)
		fake.setDropPost()
		y := posthogCheckYard(t, fake)
		ask := y.ask(t, Ask{Question: "Hook PostHog's new error issues up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook", "add", "posthog", "--source", "posthog"}}}, "yardmaster")
		if joined := strings.Join(y.asked(t, ask).Ask.Run.Does, "\n"); !strings.Contains(joined, "approving again first lists what stands") {
			t.Fatalf("the card without the retry sentence:\n%s", joined)
		}
		_, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, map[string]string{"project": "48291"})
		if err == nil || !strings.Contains(err.Error(), "registering it failed") || !strings.Contains(err.Error(), "Approve again to retry") {
			t.Fatalf("the approval with its answer lost = %v", err)
		}
		if got := y.asked(t, ask); got.State != AskOpen {
			t.Fatalf("the ask is %s after the lost answer, want it open", got.State)
		}
		if _, err := y.GetHook(y.ctx, "posthog"); err != nil {
			t.Fatalf("hook left by a lost answer: %v", err)
		}
		seen := fake.seen()
		if seen.posts != 1 || len(seen.functions) != 1 {
			t.Fatalf("the lost answer committed %d functions in %d posts", len(seen.functions), seen.posts)
		}
		secret, _, err := ReadSecretFile(HookSecretFile(y.home, "posthog"))
		if err != nil {
			t.Fatal(err)
		}
		inputs, _ := seen.functions[0]["inputs"].(map[string]any)
		urlInput, _ := inputs["url"].(map[string]any)
		headers, _ := inputs["secret_headers"].(map[string]any)
		sent, _ := headers["value"].(map[string]any)
		if seen.functions[0]["name"] != "yardr" || urlInput["value"] != publicURL || sent["X-Yardr-Secret"] != string(secret) {
			t.Fatalf("the committed destination is not the hook's: %v", seen.functions[0])
		}
		done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, map[string]string{"project": "48291"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(done.Result, "already lists a webhook for "+publicURL) || !strings.Contains(done.Result, "so destination was not called again") {
			t.Errorf("adoption = %q", done.Result)
		}
		seen = fake.seen()
		if seen.posts != 1 || len(seen.functions) != 1 {
			t.Fatalf("the retry leaves %d functions in %d posts, want one in one", len(seen.functions), seen.posts)
		}
		if seen.gets != 1 {
			t.Fatalf("the retry listed what stands %d times", seen.gets)
		}
		q := seen.queries[0]
		if q.Get("type") != "internal_destination" || q.Get("full") != "true" || q.Get("search") != "yardr" {
			t.Errorf("the check listed with %v", q)
		}
		for _, auth := range seen.auths {
			if auth != "Bearer the-key" {
				t.Errorf("a call without the credential: %q", auth)
			}
		}
		for _, body := range seen.getBodies {
			if strings.Contains(body, string(secret)) {
				t.Fatal("the listed destination carries the hook's secret in the clear")
			}
		}
	})
	t.Run("a failing check registers nothing", func(t *testing.T) {
		fake := newFakePostHog(t)
		fake.setPostStatus(http.StatusBadRequest)
		y := posthogCheckYard(t, fake)
		ask := y.ask(t, Ask{Question: "Hook PostHog's new error issues up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook", "add", "posthog", "--source", "posthog"}}}, "yardmaster")
		if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, map[string]string{"project": "48291"}); err == nil || !strings.Contains(err.Error(), "400") {
			t.Fatalf("the refused approval = %v", err)
		}
		if seen := fake.seen(); len(seen.functions) != 0 {
			t.Fatalf("the refused create committed %d functions", len(seen.functions))
		}
		fake.setCheckStatus(http.StatusInternalServerError)
		if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, map[string]string{"project": "48291"}); err == nil ||
			!strings.Contains(err.Error(), "the check whether its webhook stands failed") ||
			!strings.Contains(err.Error(), "Approve again to retry") {
			t.Fatalf("the approval with its check failed = %v", err)
		}
		if seen := fake.seen(); seen.posts != 1 {
			t.Fatalf("the failed check registered blind: %d posts", seen.posts)
		}
		if got := y.asked(t, ask); got.State != AskOpen {
			t.Fatalf("the ask is %s after the failed check, want it open", got.State)
		}
		fake.setCheckStatus(http.StatusOK)
		fake.setPostStatus(http.StatusCreated)
		done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, map[string]string{"project": "48291"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(done.Result, "registered with POST /api/projects/{project}/hog_functions/") {
			t.Errorf("recovery = %q", done.Result)
		}
		if seen := fake.seen(); seen.posts != 2 || len(seen.functions) != 1 {
			t.Fatalf("the recovery leaves %d functions in %d posts", len(seen.functions), seen.posts)
		}
	})
}

func mustBase(t *testing.T, src *Source) string {
	t.Helper()
	d, err := src.Declaration()
	if err != nil {
		t.Fatal(err)
	}
	return d.Base
}

func TestHookRegisterFailureLeavesHookAndRetries(t *testing.T) {
	t.Parallel()
	var hits []registerHit
	status := http.StatusBadRequest
	y := registerYard(t, &hits, &status)
	ask := y.ask(t, Ask{Question: "Set the bot hook up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook", "add", "bot", "--source", "bot"}}}, "yardmaster")
	_, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil)
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "hook bot is in place") || !strings.Contains(err.Error(), "Approve again to retry") {
		t.Fatalf("failed approval = %v", err)
	}
	if _, err := y.GetHook(y.ctx, "bot"); err != nil {
		t.Fatalf("hook left by a failed registration: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("register call made %d times", len(hits))
	}
	first, _, err := ReadSecretFile(HookSecretFile(y.home, "bot"))
	if err != nil {
		t.Fatal(err)
	}
	status = http.StatusOK
	done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(done.Result, "hook bot saved") {
		t.Errorf("retry approval = %q", done.Result)
	}
	if len(hits) != 2 {
		t.Fatalf("register call made %d times after the retry", len(hits))
	}
	if hits[1].body["secret_token"] != string(first) {
		t.Error("the retry registered a second secret")
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "approved already") {
		t.Fatalf("a third approval: err=%v", err)
	}
}

func TestHookRegisterMakesFreshSecretForReaddedHook(t *testing.T) {
	t.Parallel()
	var hits []registerHit
	status := http.StatusOK
	y := registerYard(t, &hits, &status)
	firstAsk := y.ask(t, Ask{Question: "Set the bot hook up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook add bot --source bot"}}}, "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, firstAsk, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	first, _, err := ReadSecretFile(HookSecretFile(y.home, "bot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := y.DeleteHook(y.ctx, "bot", "mckean"); err != nil {
		t.Fatal(err)
	}
	secondAsk := y.ask(t, Ask{Question: "Set the bot hook up again?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook add bot --source bot"}}}, "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, secondAsk, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	second, _, err := ReadSecretFile(HookSecretFile(y.home, "bot"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(second) {
		t.Fatal("a new hook registration reused the removed hook's secret")
	}
	if got := hits[1].body["secret_token"]; got != string(second) {
		t.Fatalf("re-registration sent %v, not the new hook secret", got)
	}
}

func TestHookRegisterRefusesBadParametersBeforeSavingHook(t *testing.T) {
	t.Parallel()
	var hits []registerHit
	status := http.StatusOK
	y := registerYard(t, &hits, &status)
	src, err := y.GetSource(y.ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(src.Text, "secret_token: {type: string, max: 256}", "secret_token: {type: string, max: 8}", 1)
	if text == src.Text {
		t.Fatal("test could not narrow the secret parameter")
	}
	if _, err := y.PutSource(y.ctx, "bot.yaml", text, "mckean"); err != nil {
		t.Fatal(err)
	}
	ask := y.ask(t, Ask{Question: "Set the bot hook up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook add bot --source bot"}}}, "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Fatalf("invalid secret parameter = %v", err)
	}
	if _, err := y.GetHook(y.ctx, "bot"); err == nil {
		t.Fatal("a hook was saved although the register call could not take its secret")
	}
	if _, err := os.Stat(HookSecretFile(y.home, "bot")); !os.IsNotExist(err) {
		t.Fatalf("secret file exists after a refused registration: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("invalid registration made %d service calls", len(hits))
	}
}

func TestHookRegisterNeedsPublicURL(t *testing.T) {
	t.Parallel()
	y := hookYard(t)
	text := `
name: bot
base: http://127.0.0.1:9
credential: { env: BOT_TOKEN }
calls:
  setwebhook: { kind: update, params: { url: {type: string, max: 2048}, secret_token: {type: string, max: 256} }, request: { method: POST, path: /setWebhook, body: { url: "{url}", secret_token: "{secret_token}" } } }
hook:
  verify: { scheme: secret, signature: { header: X-Test-Secret } }
  bodies: unique
  normalize: [bot/normalize]
  register: { call: setwebhook, url: url, secret: secret_token }
`
	if _, err := y.PutSource(y.ctx, "bot.yaml", text, "mckean"); err != nil {
		t.Fatal(err)
	}
	_, err := y.AddAsk(y.ctx, y.bead, Ask{Question: "Set the bot hook up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook", "add", "bot", "--source", "bot"}}}, "yardmaster")
	if err == nil || !strings.Contains(err.Error(), HookPublicURLSetting) {
		t.Fatalf("hook card with no public address: err=%v", err)
	}
	if err := y.SetSetting(y.ctx, HookPublicURLSetting, "http://yard.example/hooks", "mckean"); err == nil {
		t.Fatal("a public http address was taken")
	}
}

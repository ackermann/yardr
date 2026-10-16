package store

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
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
	text := "\nname: bot\nbase: " + fake.URL + `
credential: { env: BOT_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  setwebhook: { kind: update, params: { url: {type: string, max: 2048}, secret_token: {type: string, max: 256} }, request: { method: POST, path: /setWebhook, body: { url: "{url}", secret_token: "{secret_token}", allowed_updates: ["message"] } } }
hook:
  verify: { scheme: secret, signature: { header: X-Test-Secret } }
  bodies: unique
  normalize: [bot/normalize]
  keys: [chat]
  register: { call: setwebhook, url: url, secret: secret_token }
`
	if _, err := y.PutSource(y.ctx, "bot.yaml", text, "mckean"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "bot.env")
	if err := os.WriteFile(env, []byte("BOT_TOKEN=the-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "bot", SourceEnvFile, env, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetSetting(y.ctx, HookPublicURLSetting, "https://yard.example/hooks", "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func TestHookRegisterMakesSecretAndCalls(t *testing.T) {
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

	done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean")
	if err != nil {
		t.Fatal(err)
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
	secret, _, err := config.ReadSecretFile(file)
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
	}
	db, err := os.ReadFile(filepath.Join(y.home, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(db), string(secret)) {
		t.Fatal("the made secret stands in the store file")
	}
}

func TestHookRegisterFailureLeavesHookAndRetries(t *testing.T) {
	var hits []registerHit
	status := http.StatusBadRequest
	y := registerYard(t, &hits, &status)
	ask := y.ask(t, Ask{Question: "Set the bot hook up?", To: "mckean", Context: []string{"a", "b"}, Run: &AskRun{Argv: []string{"hook", "add", "bot", "--source", "bot"}}}, "yardmaster")
	_, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean")
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "hook bot is in place") || !strings.Contains(err.Error(), "Approve again to retry") {
		t.Fatalf("failed approval = %v", err)
	}
	if _, err := y.GetHook(y.ctx, "bot"); err != nil {
		t.Fatalf("hook left by a failed registration: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("register call made %d times", len(hits))
	}
	first, _, err := config.ReadSecretFile(HookSecretFile(y.home, "bot"))
	if err != nil {
		t.Fatal(err)
	}
	status = http.StatusOK
	done, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean")
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
	if _, err := y.ApproveAsk(y.ctx, y.bead, ask, "mckean"); err == nil || !strings.Contains(err.Error(), "approved already") {
		t.Fatalf("a third approval: err=%v", err)
	}
}

func TestHookRegisterNeedsPublicURL(t *testing.T) {
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

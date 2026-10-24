package hooks_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A hook the yard registers itself takes a delivery signed with the
// secret it made: approving its one card makes the secret and the
// hook and calls the stand-in, and POST /hooks/bot accepts what the
// file's secret signs.
func TestHookRegisterTakesDelivery(t *testing.T) {
	var gotURL, gotSecret string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		gotURL, _ = body["url"].(string)
		gotSecret, _ = body["secret_token"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer fake.Close()

	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	normalize := "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{\"type\":\"message\",\"action\":\"create\",\"thread\":{\"id\":\"t1\",\"title\":\"Hi\"},\"fields\":{\"chat\":[\"42\"]}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "normalize"), []byte(normalize), 0o755); err != nil {
		t.Fatal(err)
	}
	text := `name: bot
base: ` + fake.URL + `
credential: { env: BOT_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  setwebhook: { kind: update, params: { url: {type: string, max: 2048}, secret_token: {type: string, max: 256} }, request: { method: POST, path: /setWebhook, body: { url: "{url}", secret_token: "{secret_token}", allowed_updates: ["message"] } } }
hook:
  verify: { scheme: secret, signature: { header: X-Test-Secret } }
  bodies: unique
  normalize: [normalize]
  keys: [chat]
  register: { call: setwebhook, url: url, secret: secret_token }
`
	d, err := source.Parse("bot.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "bot.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "bot.env")
	if err := os.WriteFile(env, []byte("BOT_TOKEN=the-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "bot", store.SourceEnvFile, env, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.HookPublicURLSetting, "https://yard.example/hooks", "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "set up the bot"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	runSeq, err := st.AddAsk(ctx, b.ID, store.Ask{Question: "Set up the bot webhook?", To: "me",
		Context: []string{"the yard makes the secret and registers it"},
		Run:     &store.AskRun{Argv: []string{"hook add bot --source bot"}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveAsk(ctx, b.ID, runSeq, "me", true, nil); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://yard.example/hooks/bot" {
		t.Fatalf("registered url = %q", gotURL)
	}
	hook, err := st.GetHook(ctx, "bot")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := config.ReadSecretFile(hook.SecretFile)
	if err != nil {
		t.Fatal(err)
	}
	if gotSecret != string(secret) {
		t.Fatal("the registered secret is not the hook file's")
	}

	srv := httptest.NewServer(hooks.NewReceiver(own, home, nil).Handler())
	defer srv.Close()
	post := func(sec string) int {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/hooks/bot", strings.NewReader(`{"update_id":1,"message":{"text":"hi"}}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Test-Secret", sec)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if code := post(string(secret)); code != http.StatusOK {
		t.Fatalf("a delivery with the made secret = %d, want 200", code)
	}
	if code := post("wrong"); code != http.StatusUnauthorized {
		t.Fatalf("a delivery with a wrong secret = %d, want 401", code)
	}
	notes, err := st.Notes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if strings.Contains(n.Text, string(secret)) {
			t.Fatalf("note %d holds the made secret", n.Seq)
		}
	}
}

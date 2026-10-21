package hooks_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A hook set up by its two asks takes a signed Linear delivery: the
// yard has the linear source declared, the secret is given on its
// card and the hook approved, and POST /hooks/linear accepts what the
// secret signs and refuses what it does not.
func TestHookAskTakesLinearDelivery(t *testing.T) {
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	normalize := "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{\"type\":\"issue\",\"action\":\"create\",\"thread\":{\"id\":\"t1\",\"title\":\"Hi\"}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "normalize"), []byte(normalize), 0o755); err != nil {
		t.Fatal(err)
	}
	text := `name: linear
base: https://api.linear.app
credential: { env: LINEAR_API_KEY }
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Linear-Signature, encoding: hex }
    delivery: { header: Linear-Delivery }
    timestamp: { body: webhookTimestamp, unit: ms, window: 1m }
  bodies: unique
  normalize: [normalize]
  keys: [team, project, label]
calls:
  viewer: { kind: read, request: { method: GET, path: /viewer } }
`
	d, err := source.Parse("linear.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "linear.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "set up Linear"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "linear-ask-signing-secret"
	secretSeq, err := st.AddAsk(ctx, b.ID, store.Ask{Question: "The signing secret for Linear?", To: "me",
		Context: []string{"it is shown once where the webhook is made"},
		Secret:  &store.AskSecret{Source: "linear", Hook: "linear"}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	runSeq, err := st.AddAsk(ctx, b.ID, store.Ask{Question: "Set up the Linear webhook?", To: "me",
		Context: []string{"the signing secret is asked for above; give it first"},
		Run:     &store.AskRun{Argv: []string{"hook add linear --source linear"}}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	keyed, err := st.GiveSecret(ctx, b.ID, secretSeq, map[string]string{"LINEAR_SIGNING_SECRET": secret}, "me", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveAsk(ctx, b.ID, runSeq, "me", true); err != nil {
		t.Fatal(err)
	}
	hook, err := st.GetHook(ctx, "linear")
	if err != nil {
		t.Fatal(err)
	}
	if hook.SecretFile != keyed.File {
		t.Fatalf("the hook names %q, the giving wrote %q", hook.SecretFile, keyed.File)
	}

	srv := httptest.NewServer(hooks.NewReceiver(own, home, nil).Handler())
	t.Cleanup(srv.Close)
	post := func(id, sig string) int {
		body := fmt.Sprintf(`{"webhookTimestamp":%d,"type":"Issue","action":"create","data":{"id":"uuid-1"}}`, time.Now().UnixMilli())
		if sig == "" {
			sig = sign(secret, []byte(body))
		}
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/hooks/linear", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Linear-Signature", sig)
		req.Header.Set("Linear-Delivery", id)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("ask-1", ""); code != http.StatusOK {
		t.Fatalf("a signed delivery = %d, want 200", code)
	}
	if code := post("ask-2", strings.Repeat("0", 64)); code != http.StatusUnauthorized {
		t.Fatalf("a badly signed delivery = %d, want 401", code)
	}
	hook, err = st.GetHook(ctx, "linear")
	if err != nil {
		t.Fatal(err)
	}
	if hook.Accepted == nil || hook.Refused == nil {
		t.Fatalf("the hook counts %+v", hook)
	}
	notes, err := st.Notes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if strings.Contains(n.Text, secret) {
			t.Fatalf("note %d holds the secret", n.Seq)
		}
	}
}

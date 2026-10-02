package hooks_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mckean/yard/internal/hooks"
	"github.com/mckean/yard/internal/store"
	"github.com/mckean/yard/internal/storetest"
)

const secret = "lin_wh_test_secret"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sentAt is the fixture's webhookTimestamp.
func sentAt(t *testing.T, body []byte) time.Time {
	t.Helper()
	var p struct{ WebhookTimestamp int64 }
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return time.UnixMilli(p.WebhookTimestamp)
}

func sign(key string, body []byte) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

type rig struct {
	st  store.API
	rc  *hooks.Receiver
	srv *httptest.Server
	now time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st := storetest.New(t)
	dir := t.TempDir()
	for name, src := range map[string]string{"linear": "linear", "adhoc": "token"} {
		f := filepath.Join(dir, name+".secret")
		if err := os.WriteFile(f, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutHook(context.Background(), store.Hook{Name: name, Source: src, SecretFile: f}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	r := &rig{st: st, rc: hooks.NewReceiver(st, nil)}
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv = httptest.NewServer(r.rc.Handler())
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) post(t *testing.T, path string, body []byte, hdr map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+path, strings.NewReader(string(body)))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// linear posts body signed with key as Linear would.
func (r *rig) linear(t *testing.T, body []byte, key, delivery string) int {
	return r.post(t, "/hooks/linear", body, map[string]string{
		"Linear-Signature": sign(key, body), "Linear-Delivery": delivery, "Linear-Event": "Comment",
		"Content-Type": "application/json; charset=utf-8"})
}

func (r *rig) hookEvents(t *testing.T) []store.Event {
	t.Helper()
	evs, err := r.st.Events(context.Background(), "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if e.Kind == "hook" {
			out = append(out, e)
		}
	}
	return out
}

func TestLinearSignature(t *testing.T) {
	r := newRig(t)
	body := fixture(t, "linear_comment_create.json")
	r.now = sentAt(t, body).Add(2 * time.Second)

	if code := r.linear(t, body, "wrong-secret", "d-1"); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	if code := r.post(t, "/hooks/linear", body, nil); code != http.StatusUnauthorized {
		t.Fatalf("no signature: %d", code)
	}
	// A signature over different bytes (re-serialized JSON) must not pass.
	var v any
	_ = json.Unmarshal(body, &v)
	reencoded, _ := json.Marshal(v)
	if code := r.post(t, "/hooks/linear", body, map[string]string{"Linear-Signature": sign(secret, reencoded)}); code != http.StatusUnauthorized {
		t.Fatalf("signature of re-encoded body: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("rejected deliveries left %d events", n)
	}

	if code := r.linear(t, body, secret, "d-1"); code != http.StatusOK {
		t.Fatalf("good signature: %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 1 {
		t.Fatalf("want 1 hook event, got %d", len(evs))
	}
	e := evs[0]
	if e.Actor != "hook:linear" || e.Data["hook"] != "linear" || e.Data["source"] != "linear" ||
		e.Data["type"] != "comment" || e.Data["delivery"] != "d-1" {
		t.Fatalf("event: %+v", e)
	}
	raw, err := r.st.HookPayload(context.Background(), e.Seq)
	if err != nil || string(raw) != string(body) {
		t.Fatalf("stored payload differs from the body (err %v)", err)
	}
	if got := r.rc.Rejected()["unauthorized"]; got != 3 {
		t.Fatalf("unauthorized count %d, want 3", got)
	}
}

func TestLinearStaleTimestamp(t *testing.T) {
	r := newRig(t)
	body := fixture(t, "linear_issue_create.json")
	sent := sentAt(t, body)
	for _, at := range []time.Time{sent.Add(61 * time.Second), sent.Add(-61 * time.Second)} {
		r.now = at
		if code := r.linear(t, body, secret, ""); code != http.StatusUnauthorized {
			t.Fatalf("timestamp %s off: %d", at.Sub(sent), code)
		}
	}
	r.now = sent.Add(59 * time.Second)
	if code := r.linear(t, body, secret, ""); code != http.StatusOK {
		t.Fatalf("within window: %d", code)
	}
	// Signed but without webhookTimestamp: cannot be placed in time.
	noTS := []byte(`{"action":"create","type":"Issue","data":{"id":"x"}}`)
	if code := r.linear(t, noTS, secret, ""); code != http.StatusUnauthorized {
		t.Fatalf("no timestamp: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 hook event, got %d", n)
	}
}

func TestLinearDuplicateDelivery(t *testing.T) {
	r := newRig(t)
	body := fixture(t, "linear_comment_create.json")
	r.now = sentAt(t, body)
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusOK} {
		if code := r.linear(t, body, secret, "dup-1"); code != want {
			t.Fatalf("delivery %d: %d, want %d", i, code, want)
		}
	}
	// A different delivery id of the same body is a different delivery.
	if code := r.linear(t, body, secret, "dup-2"); code != http.StatusOK {
		t.Fatalf("second delivery: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 2 {
		t.Fatalf("want 2 hook events, got %d", n)
	}
	if got := r.rc.Rejected()["duplicate"]; got != 2 {
		t.Fatalf("duplicate count %d", got)
	}
	// Remembering is bounded in time: past the TTL the id is forgotten
	// (only reachable by a source whose window outlasts it).
	r.now = r.now.Add(hooks.DedupeTTL + time.Second)
	if code := r.post(t, "/hooks/adhoc", []byte("x"), map[string]string{"Authorization": "Bearer " + secret, "Yard-Delivery": "t"}); code != http.StatusOK {
		t.Fatalf("token delivery: %d", code)
	}
	if code := r.post(t, "/hooks/adhoc", []byte("x"), map[string]string{"Authorization": "Bearer " + secret, "Yard-Delivery": "t"}); code != http.StatusOK {
		t.Fatalf("token duplicate: %d", code)
	}
	r.now = r.now.Add(hooks.DedupeTTL + time.Second)
	if code := r.post(t, "/hooks/adhoc", []byte("x"), map[string]string{"Authorization": "Bearer " + secret, "Yard-Delivery": "t"}); code != http.StatusOK {
		t.Fatalf("token delivery after TTL: %d", code)
	}
}

// Regression: a delivery refused after its id was remembered must not make
// the corrected retry look like a duplicate.
func TestRefusedDeliveryIsNotRemembered(t *testing.T) {
	r := newRig(t)
	r.now = time.UnixMilli(1791018843600)
	noType := []byte(`{"action":"create","data":{},"webhookTimestamp":1791018843600}`)
	if code := r.linear(t, noType, secret, "retry-1"); code != http.StatusBadRequest {
		t.Fatalf("payload without type: %d", code)
	}
	fixed := []byte(`{"action":"create","type":"Issue","data":{"id":"i"},"webhookTimestamp":1791018843600}`)
	if code := r.linear(t, fixed, secret, "retry-1"); code != http.StatusOK {
		t.Fatalf("retry after refusal: %d", code)
	}
}

func TestOversizeBody(t *testing.T) {
	r := newRig(t)
	big := []byte(`{"type":"Comment","pad":"` + strings.Repeat("a", store.MaxHookPayload) + `"}`)
	r.now = time.Now()
	if code := r.linear(t, big, secret, "big"); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("oversize left %d events", n)
	}
	if got := r.rc.Rejected()["too_large"]; got != 1 {
		t.Fatalf("too_large count %d", got)
	}
}

func TestReceiverServesOnlyHooks(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/hooks/nope", http.StatusNotFound},
		{"GET", "/hooks/linear", http.StatusMethodNotAllowed},
		{"POST", "/call/Hooks", http.StatusNotFound}, // the store API
		{"GET", "/events", http.StatusNotFound},
		{"GET", "/ping", http.StatusNotFound},
		{"GET", "/", http.StatusNotFound},
	} {
		req, _ := http.NewRequest(c.method, r.srv.URL+c.path, strings.NewReader("[]"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}
}

func TestTokenSource(t *testing.T) {
	r := newRig(t)
	r.now = time.Now()
	body := []byte(`{"type":"Deploy","action":"finished","text":"prod is on v42","actor":{"id":"ci","name":"CI"}}`)
	for _, auth := range []string{"", "Bearer", "Bearer wrong", "Basic " + secret, "Bearer " + secret + "x"} {
		if code := r.post(t, "/hooks/adhoc", body, map[string]string{"Authorization": auth}); code != http.StatusUnauthorized {
			t.Fatalf("auth %q: %d", auth, code)
		}
	}
	if code := r.post(t, "/hooks/adhoc", body, map[string]string{"Authorization": "Bearer " + secret}); code != http.StatusOK {
		t.Fatalf("good token: %d", code)
	}
	if code := r.post(t, "/hooks/adhoc", []byte("plain words"), map[string]string{"Authorization": "Bearer " + secret}); code != http.StatusOK {
		t.Fatalf("plain body: %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	if d := evs[0].Data; d["type"] != "deploy" || d["action"] != "finished" || d["text"] != "prod is on v42" ||
		d["actor"].(map[string]any)["name"] != "CI" || d["source"] != "token" {
		t.Fatalf("json body: %+v", d)
	}
	if d := evs[1].Data; d["type"] != "message" || d["text"] != "plain words" {
		t.Fatalf("plain body: %+v", d)
	}
}

// An empty secret would let anyone through (an empty bearer token, or an
// HMAC with an empty key), so it fails closed.
func TestEmptySecretFailsClosed(t *testing.T) {
	r := newRig(t)
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(context.Background(), store.Hook{Name: "open", Source: "token", SecretFile: empty}, "me"); err != nil {
		t.Fatal(err)
	}
	if code := r.post(t, "/hooks/open", []byte("hi"), map[string]string{"Authorization": "Bearer "}); code != http.StatusInternalServerError {
		t.Fatalf("empty secret: %d", code)
	}
	// The secret is read at verify time: fixing the file fixes the hook.
	if err := os.WriteFile(empty, []byte("now-set"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := r.post(t, "/hooks/open", []byte("hi"), map[string]string{"Authorization": "Bearer now-set"}); code != http.StatusOK {
		t.Fatalf("after setting secret: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 event, got %d", n)
	}
}

func TestLinearNormalize(t *testing.T) {
	for _, c := range []struct {
		file string
		want hooks.Envelope
	}{
		{"linear_comment_create.json", hooks.Envelope{
			Source: "linear", Type: "comment", Action: "create",
			Actor: hooks.Party{ID: "b5ea5f1f-8adc-4f52-b4bd-ab4e84cf51ba", Name: "Linear Orbit"},
			Thread: hooks.Thread{ID: "539068e2-ae88-4d09-bd75-22eb4a59612f", Title: "Webhook receiver drops retries",
				URL: "https://linear.app/company/issue/ENG-42/webhook-receiver-drops-retries"},
			Mentions: []string{"mayor", "jane.doe"},
			Text: "https://linear.app/company/profiles/mayor can you take a look? Also cc https://linear.app/company/profiles/jane.doe, " +
				"and again https://linear.app/company/profiles/mayor.",
			URL: "https://linear.app/company/issue/ENG-42/webhook-receiver-drops-retries#comment-2174add1",
		}},
		{"linear_comment_bodydata.json", hooks.Envelope{
			Source: "linear", Type: "comment", Action: "update",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "539068e2-ae88-4d09-bd75-22eb4a59612f",
				URL: "https://linear.app/company/issue/ENG-42"},
			Mentions: []string{"4d3a2b1c-0000-4000-8000-00000000mayr"},
			Text:     "@mayor please check",
			URL:      "https://linear.app/company/issue/ENG-42#comment-8f1e0c55",
		}},
		{"linear_issue_create.json", hooks.Envelope{
			Source: "linear", Type: "issue", Action: "create",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "539068e2-ae88-4d09-bd75-22eb4a59612f", Title: "Webhook receiver drops retries",
				URL: "https://linear.app/company/issue/ENG-42/webhook-receiver-drops-retries"},
			Mentions: []string{"mayor"},
			Assignee: "4d3a2b1c-0000-4000-8000-00000000mayr",
			Text:     "Retries from the sender arrive after the window. https://linear.app/company/profiles/mayor owns triage.",
			URL:      "https://linear.app/company/issue/ENG-42/webhook-receiver-drops-retries",
		}},
		{"linear_issue_update.json", hooks.Envelope{
			Source: "linear", Type: "issue", Action: "update",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "539068e2-ae88-4d09-bd75-22eb4a59612f", Title: "Webhook receiver drops retries",
				URL: "https://linear.app/company/issue/ENG-42/webhook-receiver-drops-retries"},
			Mentions: []string{},
			Assignee: "aacdca22-6266-4c0a-ab3c-8fa70a26765c",
			Text:     "changed assigneeId, stateId",
			URL:      "https://linear.app/company/issue/ENG-42/webhook-receiver-drops-retries",
		}},
	} {
		got, err := hooks.Linear{}.Normalize(fixture(t, c.file))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if !reflect.DeepEqual(*got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.file, *got, c.want)
		}
	}
}

func TestTextIsTruncated(t *testing.T) {
	long := strings.Repeat("é", hooks.MaxText*3)
	body, _ := json.Marshal(map[string]any{"type": "Comment", "action": "create", "data": map[string]any{"body": long}})
	e, err := hooks.Linear{}.Normalize(body)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(e.Text)); n != hooks.MaxText || !strings.HasSuffix(e.Text, "…") {
		t.Fatalf("text of %d runes", n)
	}
}

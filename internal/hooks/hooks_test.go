package hooks_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
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
	own *store.Store // what the receiver records on: it runs in the owning process
	rc  *hooks.Receiver
	srv *httptest.Server
	now time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, own := storetest.NewOwned(t)
	dir := t.TempDir()
	for name, src := range map[string]string{"linear": "linear", "adhoc": "token", "gh": "github"} {
		f := filepath.Join(dir, name+".secret")
		if err := os.WriteFile(f, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutHook(context.Background(), store.Hook{Name: name, Source: src, SecretFile: f}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	r := &rig{st: st, own: own, rc: hooks.NewReceiver(own, nil)}
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
	for i := 0; i < 3; i++ {
		if code := r.linear(t, body, secret, "dup-1"); code != http.StatusOK {
			t.Fatalf("delivery %d: %d", i, code)
		}
	}
	// Linear signs a timestamp into every body, so the same body under
	// another delivery id is the same delivery sent again.
	if code := r.linear(t, body, secret, "dup-2"); code != http.StatusOK {
		t.Fatalf("redelivery: %d", code)
	}
	// So is the same delivery id with another body.
	other := fixture(t, "linear_comment_bodydata.json")
	r.now = sentAt(t, other)
	if code := r.linear(t, other, secret, "dup-1"); code != http.StatusOK {
		t.Fatalf("reused id: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 hook event, got %d", n)
	}
	if got := r.rc.Rejected()["duplicate"]; got != 4 {
		t.Fatalf("duplicate count %d", got)
	}
	// What was recorded is in the store, not the receiver: a receiver
	// started later (a server restart) still drops the redelivery.
	r.rc = hooks.NewReceiver(r.own, nil)
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = r.rc.Handler()
	r.now = sentAt(t, body)
	if code := r.linear(t, body, secret, "dup-3"); code != http.StatusOK {
		t.Fatalf("after restart: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 || r.rc.Rejected()["duplicate"] != 1 {
		t.Fatalf("after restart: %d events, %v", n, r.rc.Rejected())
	}
}

// Regression: two copies of one delivery arriving together must not both
// pass the duplicate check before either is recorded.
func TestConcurrentRedeliveryRecordsOnce(t *testing.T) {
	r := newRig(t)
	body := fixture(t, "linear_comment_create.json")
	r.now = sentAt(t, body)
	const copies = 8
	codes := make(chan int, copies)
	var wg sync.WaitGroup
	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Linear gives each retry its own delivery id; only the body ties them.
			req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/hooks/linear", bytes.NewReader(body))
			req.Header.Set("Linear-Signature", sign(secret, body))
			req.Header.Set("Linear-Delivery", fmt.Sprint("c-", i))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				codes <- 0
				return
			}
			resp.Body.Close()
			codes <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK {
			t.Fatalf("delivery answered %d", code)
		}
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 hook event, got %d", n)
	}
	if got := r.rc.Rejected()["duplicate"]; got != copies-1 {
		t.Fatalf("duplicate count %d, want %d", got, copies-1)
	}
}

// A token sender may repeat a body on purpose; only a repeated
// Yard-Delivery is a redelivery.
func TestTokenDedupesOnDeliveryIDOnly(t *testing.T) {
	r := newRig(t)
	r.now = time.Now()
	send := func(body, id string) {
		t.Helper()
		hdr := map[string]string{"Authorization": "Bearer " + secret}
		if id != "" {
			hdr["Yard-Delivery"] = id
		}
		if code := r.post(t, "/hooks/adhoc", []byte(body), hdr); code != http.StatusOK {
			t.Fatalf("%q %q: %d", body, id, code)
		}
	}
	send("backup ok", "")
	send("backup ok", "") // same body again: recorded
	send("deploy", "t-1")
	send("deploy", "t-1")   // same id: dropped
	send("rollback", "t-1") // same id, other body: dropped
	send("deploy", "t-2")   // same body, new id: recorded
	if n := len(r.hookEvents(t)); n != 4 {
		t.Fatalf("want 4 hook events, got %d", n)
	}
}

// An issue update re-sends the whole description; a resident it already
// mentioned is not told again, one newly mentioned is.
func TestIssueUpdateDeliversOnlyNewMentions(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for name, id := range map[string]store.Identity{
		"mayor": {ID: "4d3a2b1c-0000-4000-8000-00000000mayr", Key: "mayor"},
		"jane":  {ID: "jane-id", Key: "jane.doe"},
	} {
		if _, err := r.st.PutResident(ctx, store.Resident{Name: name}, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.SetIdentity(ctx, name, "linear", id, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.AddSubscription(ctx, name, store.Subscription{Source: "linear", Scope: store.ScopeMentions}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	create := fixture(t, "linear_issue_create.json")
	var p map[string]any
	if err := json.Unmarshal(create, &p); err != nil {
		t.Fatal(err)
	}
	// update is the issue changed at +n seconds, with desc as its description.
	update := func(n int64, desc string) []byte {
		p["action"] = "update"
		p["updatedFrom"] = map[string]any{"stateId": "s-" + fmt.Sprint(n)}
		p["webhookTimestamp"] = 1791014400100 + n*1000
		data := p["data"].(map[string]any)
		if desc != "" {
			data["description"] = desc
		}
		b, _ := json.Marshal(p)
		return b
	}
	desc := p["data"].(map[string]any)["description"].(string)
	inbox := func(name string) int {
		t.Helper()
		page, err := r.st.Inbox(ctx, name, 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Total
	}
	for i, c := range []struct {
		body        []byte
		mayor, jane int
	}{
		{create, 1, 0},        // mentions @mayor
		{update(1, ""), 1, 0}, // status change, still mentions @mayor
		{update(2, desc+" cc https://linear.app/company/profiles/jane.doe"), 1, 1}, // adds @jane
		{update(3, ""), 1, 1}, // both still mentioned
	} {
		r.now = sentAt(t, c.body)
		if code := r.linear(t, c.body, secret, fmt.Sprint("i-", i)); code != http.StatusOK {
			t.Fatalf("delivery %d: %d", i, code)
		}
		if m, j := inbox("mayor"), inbox("jane"); m != c.mayor || j != c.jane {
			t.Fatalf("after delivery %d: mayor %d, jane %d; want %d, %d", i, m, j, c.mayor, c.jane)
		}
	}
	// The event still carries everyone the description mentions.
	evs := r.hookEvents(t)
	last := evs[len(evs)-1].Data
	if len(evs) != 4 || fmt.Sprint(last["mentions"]) != "[mayor jane.doe]" || fmt.Sprint(last["new_mentions"]) != "[]" {
		t.Fatalf("last event: mentions %v, new %v (%d events)", last["mentions"], last["new_mentions"], len(evs))
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
			URL:    "https://linear.app/company/issue/ENG-42/webhook-receiver-drops-retries#comment-2174add1",
			Fields: map[string][]string{"team": {"ENG", "Engineering"}},
		}},
		{"linear_comment_bodydata.json", hooks.Envelope{
			Source: "linear", Type: "comment", Action: "update",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "539068e2-ae88-4d09-bd75-22eb4a59612f",
				URL: "https://linear.app/company/issue/ENG-42"},
			Mentions: []string{"4d3a2b1c-0000-4000-8000-00000000mayr", "mayor"},
			Text:     "@mayor please check",
			URL:      "https://linear.app/company/issue/ENG-42#comment-8f1e0c55",
		}},
		{"linear_comment_plain_mention.json", hooks.Envelope{
			Source: "linear", Type: "comment", Action: "create",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "b3f17ab3-91e6-4c9a-b083-358fd8c98b19", Title: "Testing local yardr setup",
				URL: "https://linear.app/company/issue/ENG-498/testing-local-yardr-setup"},
			Mentions: []string{"mayor"},
			Text:     "testing @mayor hello",
			URL:      "https://linear.app/company/issue/ENG-498/testing-local-yardr-setup#comment-3a486010",
			Fields:   map[string][]string{"team": {"ENG", "Engineering"}},
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
			Fields: map[string][]string{"team": {"ENG", "Engineering"}, "project": {"Webhooks"},
				"label": {"Bug"}},
			Entity: "539068e2-ae88-4d09-bd75-22eb4a59612f",
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
			Entity:   "539068e2-ae88-4d09-bd75-22eb4a59612f",
		}},
	} {
		got, err := hooks.Linear{}.Normalize(nil, fixture(t, c.file))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if !reflect.DeepEqual(*got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.file, *got, c.want)
		}
	}
}

// Linear's webhooks and API render a user mention in markdown as a plain
// @<profile key>; one that is part of an email address, a URL or code is
// not a mention.
func TestLinearPlainMentions(t *testing.T) {
	for _, c := range []struct {
		body string
		want []string
	}{
		{"@mayor can you look?", []string{"mayor"}},
		{"testing @mayor hello", []string{"mayor"}},
		{"over to @mayor", []string{"mayor"}},
		{"thanks @mayor.", []string{"mayor"}},
		{"@mayor, @jane.doe: and @mayor!", []string{"mayor", "jane.doe"}},
		{"(@mayor?)\n@ops_bot;", []string{"mayor", "ops_bot"}},
		{"ask @Mayor", []string{"mayor"}},
		{"mail x@mayor.com", []string{}},
		{"see https://example.com/@mayor/posts", []string{}},
		{"run `yardr ask @mayor` or\n```\n@mayor\n```", []string{}},
		{"a lone @ sign, @-dash and @.dot", []string{}},
		{"https://linear.app/company/profiles/mayor and @mayor", []string{"mayor"}},
	} {
		body, _ := json.Marshal(map[string]any{"type": "Comment", "action": "create", "data": map[string]any{"body": c.body}})
		e, err := hooks.Linear{}.Normalize(nil, body)
		if err != nil {
			t.Fatalf("%q: %v", c.body, err)
		}
		if !reflect.DeepEqual(e.Mentions, c.want) {
			t.Errorf("%q: mentions %q, want %q", c.body, e.Mentions, c.want)
		}
	}
}

func TestTextIsTruncated(t *testing.T) {
	long := strings.Repeat("é", hooks.MaxText*3)
	body, _ := json.Marshal(map[string]any{"type": "Comment", "action": "create", "data": map[string]any{"body": long}})
	e, err := hooks.Linear{}.Normalize(nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(e.Text)); n != hooks.MaxText || !strings.HasSuffix(e.Text, "…") {
		t.Fatalf("text of %d runes", n)
	}
}

// A Linear comment reaches a subscribed resident's inbox with its reason:
// by profile key from markdown (a profile URL or a plain @key), by user id
// from bodyData.
func TestLinearMentionReachesInbox(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.st.PutResident(ctx, store.Resident{Name: "mayor"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetIdentity(ctx, "mayor", "linear",
		store.Identity{ID: "4d3a2b1c-0000-4000-8000-00000000mayr", Key: "mayor"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.AddSubscription(ctx, "mayor", store.Subscription{Source: "linear", Scope: store.ScopeMentions,
		Where: map[string]string{"type": "comment"}}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"linear_comment_create.json", "linear_comment_bodydata.json", "linear_issue_create.json",
		"linear_comment_plain_mention.json"} {
		body := fixture(t, f)
		r.now = sentAt(t, body)
		if code := r.linear(t, body, secret, f); code != http.StatusOK {
			t.Fatalf("%s: %d", f, code)
		}
	}
	page, err := r.st.Inbox(ctx, "mayor", 0)
	if err != nil || page.Total != 3 {
		t.Fatalf("inbox: %+v %v", page, err)
	}
	for _, e := range page.Events {
		if why := store.Reason(e, "mayor"); why != store.ReasonMentioned || e.Data["type"] != "comment" {
			t.Errorf("event %d: %s, reason %q", e.Seq, e.Data["type"], why)
		}
	}
}

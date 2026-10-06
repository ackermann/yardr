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

type depot struct {
	st  store.API
	own *store.Store // what the receiver records on: it runs in the owning process
	rc  *hooks.Receiver
	srv *httptest.Server
	now time.Time
}

func newDepot(t *testing.T) *depot {
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
	r := &depot{st: st, own: own, rc: hooks.NewReceiver(own, nil)}
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv = httptest.NewServer(r.rc.Handler())
	t.Cleanup(r.srv.Close)
	return r
}

func (r *depot) post(t *testing.T, path string, body []byte, hdr map[string]string) int {
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
func (r *depot) linear(t *testing.T, body []byte, key, delivery string) int {
	return r.post(t, "/hooks/linear", body, map[string]string{
		"Linear-Signature": sign(key, body), "Linear-Delivery": delivery, "Linear-Event": "Comment",
		"Content-Type": "application/json; charset=utf-8"})
}

func (r *depot) hookEvents(t *testing.T) []store.Event {
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
	r := newDepot(t)
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
	r := newDepot(t)
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
	r := newDepot(t)
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
	r := newDepot(t)
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
	r := newDepot(t)
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

// An issue update re-sends the whole description; a crew member it already
// mentioned is not told again, one newly mentioned is.
func TestIssueUpdateDeliversOnlyNewMentions(t *testing.T) {
	r := newDepot(t)
	ctx := context.Background()
	for name, id := range map[string]store.Identity{
		"yardmaster": {ID: "4d3a2b1c-0000-4000-8000-00000000mayr", Key: "yardmaster"},
		"jane":       {ID: "jane-id", Key: "jane.doe"},
	} {
		if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: name}, "me"); err != nil {
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
		body             []byte
		yardmaster, jane int
	}{
		{create, 1, 0},        // mentions @yardmaster
		{update(1, ""), 1, 0}, // status change, still mentions @yardmaster
		{update(2, desc+" cc https://linear.app/company/profiles/jane.doe"), 1, 1}, // adds @jane
		{update(3, ""), 1, 1}, // both still mentioned
	} {
		r.now = sentAt(t, c.body)
		if code := r.linear(t, c.body, secret, fmt.Sprint("i-", i)); code != http.StatusOK {
			t.Fatalf("delivery %d: %d", i, code)
		}
		if m, j := inbox("yardmaster"), inbox("jane"); m != c.yardmaster || j != c.jane {
			t.Fatalf("after delivery %d: yardmaster %d, jane %d; want %d, %d", i, m, j, c.yardmaster, c.jane)
		}
	}
	// The event still carries everyone the description mentions.
	evs := r.hookEvents(t)
	last := evs[len(evs)-1].Data
	if len(evs) != 4 || fmt.Sprint(last["mentions"]) != "[yardmaster jane.doe]" || fmt.Sprint(last["new_mentions"]) != "[]" {
		t.Fatalf("last event: mentions %v, new %v (%d events)", last["mentions"], last["new_mentions"], len(evs))
	}
}

// A comment's edit re-sends its body whole, as an issue update does the
// description: a crew member the comment already mentioned is not told
// again, one newly mentioned is, and a removed comment is forgotten.
func TestCommentEditDeliversOnlyNewMentions(t *testing.T) {
	r := newDepot(t)
	ctx := context.Background()
	for name, id := range map[string]store.Identity{"yardmaster": {ID: "u-yardmaster", Key: "yardmaster"}, "jane": {ID: "u-jane", Key: "jane.doe"}} {
		if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: name}, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.SetIdentity(ctx, name, "linear", id, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.AddSubscription(ctx, name, store.Subscription{Source: "linear", Scope: store.ScopeMentions}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// comment is comment cm-1 on an issue after action, saying body, sent
	// n seconds after the first.
	comment := func(n int64, action, body string) []byte {
		b, _ := json.Marshal(map[string]any{"type": "Comment", "action": action,
			"url":              "https://linear.app/x/issue/ENG-1#comment-cm-1",
			"actor":            map[string]any{"id": "u-ann", "name": "Ann"},
			"data":             map[string]any{"id": "cm-1", "body": body, "issueId": "issue-1", "userId": "u-ann"},
			"webhookTimestamp": 1791014400100 + n*1000})
		return b
	}
	inbox := func(name string) int {
		t.Helper()
		page, err := r.st.Inbox(ctx, name, 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Total
	}
	for i, c := range []struct {
		body             []byte
		yardmaster, jane int
	}{
		{comment(0, "create", "@yardmaster look?"), 1, 0},
		{comment(1, "update", "@yardmaster look? (typo fixed)"), 1, 0}, // edited: still mentions yardmaster
		{comment(2, "update", "@yardmaster look? cc @jane.doe"), 1, 1}, // adds jane
		{comment(3, "remove", "@yardmaster look? cc @jane.doe"), 1, 1}, // removed: no one told
		{comment(4, "update", "@yardmaster look? cc @jane.doe"), 2, 2}, // forgotten: nothing held against the id
		{comment(5, "update", "@yardmaster look? cc @jane.doe"), 2, 2}, // and remembered again
	} {
		r.now = sentAt(t, c.body)
		if code := r.linear(t, c.body, secret, fmt.Sprint("c-", i)); code != http.StatusOK {
			t.Fatalf("delivery %d: %d", i, code)
		}
		if m, j := inbox("yardmaster"), inbox("jane"); m != c.yardmaster || j != c.jane {
			t.Fatalf("after delivery %d: yardmaster %d, jane %d; want %d, %d", i, m, j, c.yardmaster, c.jane)
		}
	}
	evs := r.hookEvents(t)
	last := evs[len(evs)-1].Data
	if len(evs) != 6 || last["entity"] != "cm-1" || fmt.Sprint(last["mentions"]) != "[yardmaster jane.doe]" || fmt.Sprint(last["new_mentions"]) != "[]" {
		t.Fatalf("last event: entity %v, mentions %v, new %v (%d events)", last["entity"], last["mentions"], last["new_mentions"], len(evs))
	}
}

// Regression: a delivery refused after its id was remembered must not make
// the corrected retry look like a duplicate.
func TestRefusedDeliveryIsNotRemembered(t *testing.T) {
	r := newDepot(t)
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
	r := newDepot(t)
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
	r := newDepot(t)
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
	r := newDepot(t)
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
	r := newDepot(t)
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
			Mentions: []string{"yardmaster", "jane.doe"},
			Text: "https://linear.app/company/profiles/yardmaster can you take a look? Also cc https://linear.app/company/profiles/jane.doe, " +
				"and again https://linear.app/company/profiles/yardmaster.",
			URL:     "https://linear.app/company/issue/ENG-42/webhook-receiver-drops-retries#comment-2174add1",
			Message: "2174add1-f7c8-44e3-bbf3-2d60b5ea8bc9",
			Entity:  "2174add1-f7c8-44e3-bbf3-2d60b5ea8bc9",
			Fields:  map[string][]string{"team": {"ENG", "Engineering"}},
		}},
		{"linear_comment_bodydata.json", hooks.Envelope{
			Source: "linear", Type: "comment", Action: "update",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "539068e2-ae88-4d09-bd75-22eb4a59612f",
				URL: "https://linear.app/company/issue/ENG-42"},
			Mentions: []string{"4d3a2b1c-0000-4000-8000-00000000mayr", "yardmaster"},
			Text:     "@yardmaster please check",
			URL:      "https://linear.app/company/issue/ENG-42#comment-8f1e0c55",
			Message:  "8f1e0c55-1b6f-4a1e-9e7a-2d4c1f0b9a10",
			Parent:   "2174add1-f7c8-44e3-bbf3-2d60b5ea8bc9",
			Entity:   "8f1e0c55-1b6f-4a1e-9e7a-2d4c1f0b9a10",
		}},
		{"linear_comment_plain_mention.json", hooks.Envelope{
			Source: "linear", Type: "comment", Action: "create",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "b3f17ab3-91e6-4c9a-b083-358fd8c98b19", Title: "Testing local yardr setup",
				URL: "https://linear.app/company/issue/ENG-498/testing-local-yardr-setup"},
			Mentions: []string{"yardmaster"},
			Text:     "testing @yardmaster hello",
			URL:      "https://linear.app/company/issue/ENG-498/testing-local-yardr-setup#comment-3a486010",
			Message:  "3a486010-d638-49dc-bcc0-800c16e08441",
			Entity:   "3a486010-d638-49dc-bcc0-800c16e08441",
			Fields:   map[string][]string{"team": {"ENG", "Engineering"}},
		}},
		{"linear_issue_create.json", hooks.Envelope{
			Source: "linear", Type: "issue", Action: "create",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "539068e2-ae88-4d09-bd75-22eb4a59612f", Title: "Webhook receiver drops retries",
				URL: "https://linear.app/company/issue/ENG-42/webhook-receiver-drops-retries"},
			Mentions: []string{"yardmaster"},
			Assignee: "4d3a2b1c-0000-4000-8000-00000000mayr",
			Text:     "Retries from the sender arrive after the window. https://linear.app/company/profiles/yardmaster owns triage.",
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
		// The shape of the delivery that reached nobody (yardr-rber): a
		// person is the assignee, the agent the delegate.
		{"linear_issue_delegated.json", hooks.Envelope{
			Source: "linear", Type: "issue", Action: "create",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "6f0d2c1b-8a3e-4b7c-9d15-3e2a1b0c9d8e", Title: "Notify the agent an issue is handed to",
				URL: "https://linear.app/company/issue/ENG-14/notify-the-agent-an-issue-is-handed-to"},
			Mentions: []string{},
			Assignee: "aacdca22-6266-4c0a-ab3c-8fa70a26765c",
			Delegate: "4d3a2b1c-0000-4000-8000-00000000mayr",
			Text:     "Created with a person as assignee and an agent as delegate.",
			URL:      "https://linear.app/company/issue/ENG-14/notify-the-agent-an-issue-is-handed-to",
			Fields:   map[string][]string{"team": {"ENG", "Engineering"}},
			Entity:   "6f0d2c1b-8a3e-4b7c-9d15-3e2a1b0c9d8e",
		}},
		// A delegate set later; this payload names it by the nested object
		// alone, which is read when delegateId is absent.
		{"linear_issue_delegate_set.json", hooks.Envelope{
			Source: "linear", Type: "issue", Action: "update",
			Actor: hooks.Party{ID: "aacdca22-6266-4c0a-ab3c-8fa70a26765c", Name: "Jane Doe"},
			Thread: hooks.Thread{ID: "6f0d2c1b-8a3e-4b7c-9d15-3e2a1b0c9d8e", Title: "Notify the agent an issue is handed to",
				URL: "https://linear.app/company/issue/ENG-14/notify-the-agent-an-issue-is-handed-to"},
			Mentions: []string{},
			Assignee: "aacdca22-6266-4c0a-ab3c-8fa70a26765c",
			Delegate: "4d3a2b1c-0000-4000-8000-00000000mayr",
			Text:     "changed delegateId",
			URL:      "https://linear.app/company/issue/ENG-14/notify-the-agent-an-issue-is-handed-to",
			Entity:   "6f0d2c1b-8a3e-4b7c-9d15-3e2a1b0c9d8e",
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
		{"@yardmaster can you look?", []string{"yardmaster"}},
		{"testing @yardmaster hello", []string{"yardmaster"}},
		{"over to @yardmaster", []string{"yardmaster"}},
		{"thanks @yardmaster.", []string{"yardmaster"}},
		{"@yardmaster, @jane.doe: and @yardmaster!", []string{"yardmaster", "jane.doe"}},
		{"(@yardmaster?)\n@ops_bot;", []string{"yardmaster", "ops_bot"}},
		{"ask @Yardmaster", []string{"yardmaster"}},
		{"mail x@yardmaster.com", []string{}},
		{"see https://example.com/@yardmaster/posts", []string{}},
		{"run `yardr ask @yardmaster` or\n```\n@yardmaster\n```", []string{}},
		{"a lone @ sign, @-dash and @.dot", []string{}},
		{"https://linear.app/company/profiles/yardmaster and @yardmaster", []string{"yardmaster"}},
		{"https://linear.app/company/profiles/Yardmaster and @yardmaster", []string{"yardmaster"}},
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

// A Linear comment reaches a subscribed crew member's inbox with its reason:
// by profile key from markdown (a profile URL or a plain @key), by user id
// from bodyData.
func TestLinearMentionReachesInbox(t *testing.T) {
	r := newDepot(t)
	ctx := context.Background()
	if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetIdentity(ctx, "yardmaster", "linear",
		store.Identity{ID: "4d3a2b1c-0000-4000-8000-00000000mayr", Key: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "linear", Scope: store.ScopeMentions,
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
	page, err := r.st.Inbox(ctx, "yardmaster", 0)
	if err != nil || page.Total != 3 {
		t.Fatalf("inbox: %+v %v", page, err)
	}
	for _, e := range page.Events {
		if why := store.Reason(e, "yardmaster"); why != store.ReasonMentioned || e.Data["type"] != "comment" {
			t.Errorf("event %d: %s, reason %q", e.Seq, e.Data["type"], why)
		}
	}
}

// An issue handed to a crew member as its delegate (Linear's field for the
// agent; the assignee stays the person) reaches it under involved as one
// assigned to it does: when it is created that way, and when the delegate
// is set later, each once, a redelivery included. One delegated to someone
// else does not.
func TestLinearDelegateReachesInbox(t *testing.T) {
	const yardmaster, other = "4d3a2b1c-0000-4000-8000-00000000mayr", "9e8d7c6b-0000-4000-8000-0000000other"
	inbox := func(t *testing.T, delegate string, files ...string) []store.Event {
		t.Helper()
		r := newDepot(t)
		ctx := context.Background()
		if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.SetIdentity(ctx, "yardmaster", "linear", store.Identity{ID: yardmaster, Key: "yardmaster"}, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "linear", Scope: store.ScopeInvolved}, "me"); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			body := []byte(strings.ReplaceAll(string(fixture(t, f)), yardmaster, delegate))
			r.now = sentAt(t, body)
			// Linear retries a delivery it got no answer to: the second
			// post is that, and must not be a second inbox entry.
			for range 2 {
				if code := r.linear(t, body, secret, f); code != http.StatusOK {
					t.Fatalf("%s: %d", f, code)
				}
			}
		}
		page, err := r.st.Inbox(ctx, "yardmaster", 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Events
	}
	for _, c := range []struct {
		name, delegate string
		files          []string
		want           []string // action of each inbox entry
	}{
		{"created delegated to it", yardmaster, []string{"linear_issue_delegated.json"}, []string{"create"}},
		{"created delegated to another", other, []string{"linear_issue_delegated.json"}, nil},
		{"delegate set by an update", yardmaster, []string{"linear_issue_delegate_set.json"}, []string{"update"}},
		{"update sets another delegate", other, []string{"linear_issue_delegate_set.json"}, nil},
	} {
		got := inbox(t, c.delegate, c.files...)
		var actions []string
		for _, e := range got {
			a, _ := e.Data["action"].(string)
			actions = append(actions, a)
			if why := store.Reason(e, "yardmaster"); why != store.ReasonAssigned {
				t.Errorf("%s: event %d: reason %q, want assigned", c.name, e.Seq, why)
			}
			if d, _ := e.Data["delegate"].(string); d != c.delegate {
				t.Errorf("%s: event %d: delegate %q", c.name, e.Seq, d)
			}
		}
		if !reflect.DeepEqual(actions, c.want) {
			t.Errorf("%s: inbox %v, want %v", c.name, actions, c.want)
		}
	}
}

func (r *depot) eventsOfKind(t *testing.T, kind string) []store.Event {
	t.Helper()
	evs, err := r.st.Events(context.Background(), "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// The secret file is held to the env_file rule on every delivery, not only
// at `hook add`: one loosened afterwards blocks the hook (503, even for a
// delivery the secret in it would verify), with one hook_blocked event for
// the whole stretch, and fixing the file unblocks it. (yardr-7730)
func TestSecretFileLoosenedAfterAddBlocksHook(t *testing.T) {
	r := newDepot(t)
	ctx := context.Background()
	h, err := r.st.GetHook(ctx, "adhoc")
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]string{"Authorization": "Bearer " + secret}
	if code := r.post(t, "/hooks/adhoc", []byte("one"), good); code != http.StatusOK {
		t.Fatalf("before loosening: %d", code)
	}
	if err := os.Chmod(h.SecretFile, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if code := r.post(t, "/hooks/adhoc", []byte("two"), good); code != http.StatusServiceUnavailable {
			t.Fatalf("delivery %d against a 0644 secret file: %d", i, code)
		}
	}
	if n := r.rc.Rejected()["blocked"]; n != 3 {
		t.Fatalf("blocked count = %d", n)
	}
	evs := r.eventsOfKind(t, "hook_blocked")
	if len(evs) != 1 {
		t.Fatalf("want one hook_blocked event over three deliveries, got %d", len(evs))
	}
	d := evs[0].Data
	reason, _ := d["reason"].(string)
	if d["hook"] != "adhoc" || d["path"] != h.SecretFile || !strings.Contains(reason, "mode 0644") {
		t.Fatalf("hook_blocked data: %+v", d)
	}
	if raw, _ := json.Marshal(evs[0]); strings.Contains(string(raw), secret) {
		t.Fatalf("hook_blocked shows the secret: %s", raw)
	}
	if got, err := r.st.GetHook(ctx, "adhoc"); err != nil || got.Blocked != reason {
		t.Fatalf("blocked hook reads %+v %v", got, err)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("a blocked delivery was recorded: %d hook events", n)
	}

	if err := os.Chmod(h.SecretFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := r.post(t, "/hooks/adhoc", []byte("three"), good); code != http.StatusOK {
		t.Fatalf("after chmod 600: %d", code)
	}
	if got, err := r.st.GetHook(ctx, "adhoc"); err != nil || got.Blocked != "" {
		t.Fatalf("fixed hook reads %+v %v", got, err)
	}
	// Blocked again is a new stretch, and a new event.
	if err := os.Chmod(h.SecretFile, 0o640); err != nil {
		t.Fatal(err)
	}
	if code := r.post(t, "/hooks/adhoc", []byte("four"), good); code != http.StatusServiceUnavailable {
		t.Fatalf("0640: %d", code)
	}
	if n := len(r.eventsOfKind(t, "hook_blocked")); n != 2 {
		t.Fatalf("want a second hook_blocked event, got %d", n)
	}
}

// A hook whose secret file is gone is blocked the same way, and a file put
// back takes deliveries again.
func TestMissingSecretFileBlocksHook(t *testing.T) {
	r := newDepot(t)
	ctx := context.Background()
	h, err := r.st.GetHook(ctx, "adhoc")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.SecretFile); err != nil {
		t.Fatal(err)
	}
	good := map[string]string{"Authorization": "Bearer " + secret}
	for i := 0; i < 2; i++ {
		if code := r.post(t, "/hooks/adhoc", []byte("x"), good); code != http.StatusServiceUnavailable {
			t.Fatalf("delivery %d with no secret file: %d", i, code)
		}
	}
	evs := r.eventsOfKind(t, "hook_blocked")
	if len(evs) != 1 || evs[0].Data["path"] != h.SecretFile {
		t.Fatalf("hook_blocked events: %+v", evs)
	}
	if got, err := r.st.GetHook(ctx, "adhoc"); err != nil || !strings.Contains(got.Blocked, "no such file") {
		t.Fatalf("blocked hook reads %+v %v", got, err)
	}
	if err := os.WriteFile(h.SecretFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := r.post(t, "/hooks/adhoc", []byte("x"), good); code != http.StatusOK {
		t.Fatalf("after restoring the file: %d", code)
	}
	if got, err := r.st.GetHook(ctx, "adhoc"); err != nil || got.Blocked != "" {
		t.Fatalf("restored hook reads %+v %v", got, err)
	}
}

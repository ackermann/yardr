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
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
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
	for name, src := range map[string]string{"adhoc": "token", "gh": "github"} {
		f := filepath.Join(dir, name+".secret")
		if err := os.WriteFile(f, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutHook(context.Background(), store.Hook{Name: name, Source: src, SecretFile: f}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	r := &depot{st: st, own: own, rc: hooks.NewReceiver(own, "", nil)}
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

// Regression: two copies of one delivery arriving together must not both
// pass the duplicate check before either is recorded.
func TestConcurrentRedeliveryRecordsOnce(t *testing.T) {
	r := newDepot(t)
	body := ghFixture(t, "issues_opened")
	const copies = 8
	codes := make(chan int, copies)
	var wg sync.WaitGroup
	for i := 0; i < copies; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// GitHub gives each retry its own delivery id; only the body ties them.
			req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/hooks/gh", bytes.NewReader(body))
			req.Header.Set("X-Hub-Signature-256", "sha256="+sign(secret, body))
			req.Header.Set("X-GitHub-Delivery", fmt.Sprint("c-", i))
			req.Header.Set("X-GitHub-Event", "issues")
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

// An issue's edits re-send its body whole; a crew member it already
// mentioned is not told again, one newly mentioned is.
func TestIssueBodyEditDeliversOnlyNewMentions(t *testing.T) {
	r := newDepot(t)
	ctx := context.Background()
	for _, name := range []string{"yardmaster", "ops"} {
		if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: name}, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.SetIdentity(ctx, name, "github", store.Identity{Key: name}, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.AddSubscription(ctx, name, store.Subscription{Source: "github", Scope: store.ScopeMentions}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	var p map[string]any
	if err := json.Unmarshal(ghFixture(t, "issues_opened"), &p); err != nil {
		t.Fatal(err)
	}
	// edited is the fixture's issue after action, saying body.
	edited := func(action, body string) []byte {
		p["action"] = action
		p["issue"].(map[string]any)["body"] = body
		b, _ := json.Marshal(p)
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
		body            []byte
		yardmaster, ops int
	}{
		{edited("opened", "Seen on startup. @yardmaster can you look?"), 1, 0},
		{edited("edited", "Seen on startup. @yardmaster can you look? (typo fixed)"), 1, 0}, // edited: still mentions yardmaster
		{edited("edited", "Seen on startup. @yardmaster can you look? cc @ops"), 1, 1},      // adds ops
		{edited("edited", "Seen on startup. @yardmaster can you look? cc @ops."), 1, 1},     // both still mentioned
	} {
		if code := r.github(t, "issues", c.body, secret, fmt.Sprint("is-", i)); code != http.StatusOK {
			t.Fatalf("delivery %d: %d", i, code)
		}
		if m, o := inbox("yardmaster"), inbox("ops"); m != c.yardmaster || o != c.ops {
			t.Fatalf("after delivery %d: yardmaster %d, ops %d; want %d, %d", i, m, o, c.yardmaster, c.ops)
		}
	}
	// The event still carries everyone the body mentions.
	evs := r.hookEvents(t)
	last := evs[len(evs)-1].Data
	if len(evs) != 4 || last["entity"] != "acme/yardr#23" ||
		fmt.Sprint(last["mentions"]) != "[yardmaster ops]" || fmt.Sprint(last["new_mentions"]) != "[]" {
		t.Fatalf("last event: entity %v, mentions %v, new %v (%d events)",
			last["entity"], last["mentions"], last["new_mentions"], len(evs))
	}
}

// Regression: a delivery refused after its id was remembered must not make
// the corrected retry look like a duplicate.
func TestRefusedDeliveryIsNotRemembered(t *testing.T) {
	r := newDepot(t)
	if code := r.github(t, "issues", []byte(`{"action":`), secret, "retry-1"); code != http.StatusBadRequest {
		t.Fatalf("payload that is no JSON: %d", code)
	}
	if code := r.github(t, "issues", ghFixture(t, "issues_opened"), secret, "retry-1"); code != http.StatusOK {
		t.Fatalf("retry after refusal: %d", code)
	}
}

func TestOversizeBody(t *testing.T) {
	r := newDepot(t)
	big := []byte(`{"type":"Deploy","pad":"` + strings.Repeat("a", store.MaxHookPayload) + `"}`)
	if code := r.post(t, "/hooks/adhoc", big, map[string]string{"Authorization": "Bearer " + secret}); code != http.StatusRequestEntityTooLarge {
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
		{"GET", "/hooks/gh", http.StatusMethodNotAllowed},
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

func TestTextIsTruncated(t *testing.T) {
	long := strings.Repeat("é", hooks.MaxText*3)
	body, _ := json.Marshal(map[string]any{"action": "opened", "issue": map[string]any{"number": 7, "body": long}})
	e, err := hooks.GitHub{}.Normalize(http.Header{"X-Github-Event": {"issues"}}, body)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(e.Text)); n != hooks.MaxText || !strings.HasSuffix(e.Text, "…") {
		t.Fatalf("text of %d runes", n)
	}
}

// delegateDepot is a depot whose receiver serves the declared source
// acme, with a normalize command that prints one issue envelope: the
// action and the delegate given.
func delegateDepot(t *testing.T, action, delegate string) *depot {
	t.Helper()
	home := t.TempDir()
	r := newDepot(t)
	r.rc = hooks.NewReceiver(r.own, home, nil)
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = r.rc.Handler()
	ctx := context.Background()
	stdin := filepath.Join(home, "stdin.json")
	d, err := source.Parse("acme.yaml", []byte(acmeText(stdin)))
	if err != nil {
		t.Fatal(err)
	}
	env := fmt.Sprintf(`{"type":"issue","action":%q,`+
		`"actor":{"id":"u-ann","name":"Ann"},`+
		`"thread":{"id":"T-1","title":"T","url":"https://acme.test/issue/ACME-1"},`+
		`"delegate":%q,"entity":"T-1","mentions":[]}`, action, delegate)
	declDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(declDir, "normalize"), []byte("#!/bin/sh\nprintf '%s' '"+env+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "acme.yaml", acmeText(stdin), "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(home, "acme.secret")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "acme", Source: "acme", SecretFile: secretFile}, "me"); err != nil {
		t.Fatal(err)
	}
	return r
}

// An issue handed to a crew member as its delegate reaches it under
// involved as one assigned to it does: when it is created that way, and
// when the delegate is set later, each once, a redelivery included. One
// delegated to someone else does not.
func TestDelegateReachesInbox(t *testing.T) {
	const yardmaster, other = "u-yardmaster", "u-other"
	inbox := func(t *testing.T, action, delegate string) []store.Event {
		t.Helper()
		r := delegateDepot(t, action, delegate)
		ctx := context.Background()
		if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.SetIdentity(ctx, "yardmaster", "acme", store.Identity{ID: yardmaster}, "me"); err != nil {
			t.Fatal(err)
		}
		if err := r.st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "acme", Scope: store.ScopeInvolved}, "me"); err != nil {
			t.Fatal(err)
		}
		r.now = time.Now()
		body := acmeBody(r.now)
		// The second post is the sender's retry, and must not be a
		// second inbox entry.
		for range 2 {
			if code := r.acme(t, body, secret, "m-1"); code != http.StatusOK {
				t.Fatalf("%s: %d", action, code)
			}
		}
		page, err := r.st.Inbox(ctx, "yardmaster", 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Events
	}
	for _, c := range []struct {
		name, action, delegate string
		want                   int
	}{
		{"created delegated to it", "create", yardmaster, 1},
		{"created delegated to another", "create", other, 0},
		{"delegate set by an update", "update", yardmaster, 1},
		{"update sets another delegate", "update", other, 0},
	} {
		got := inbox(t, c.action, c.delegate)
		if len(got) != c.want {
			t.Errorf("%s: inbox holds %d, want %d", c.name, len(got), c.want)
		}
		for _, e := range got {
			if why := store.Reason(e, "yardmaster"); why != store.ReasonAssigned {
				t.Errorf("%s: event %d: reason %q, want assigned", c.name, e.Seq, why)
			}
			if d, _ := e.Data["delegate"].(string); d != c.delegate {
				t.Errorf("%s: event %d: delegate %q", c.name, e.Seq, d)
			}
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

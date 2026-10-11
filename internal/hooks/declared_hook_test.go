package hooks_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// acmeDecl is a declared source that receives: a Linear-like signature in
// Acme-Signature past sha256=, a delivery id in Acme-Delivery, a body
// timestamp in ms, bodies unique, and the command printing a fixed
// envelope. stdin is the file argv[1] names, so the test reads what the
// command was handed.
const acmeNormalize = `#!/bin/sh
cat > "$1"
printf '%s' '{"type":"ping","action":"create","text":"hi","actor":{"id":"u1","name":"Ann"},"fields":{"team":["ENG"],"queue":["triage"]}}'
`

func acmeText(stdin string) string {
	return fmt.Sprintf(`name: acme
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Acme-Signature, encoding: hex, prefix: "sha256=" }
    delivery: { header: Acme-Delivery }
    timestamp: { body: sent, unit: ms, window: 1m }
  bodies: unique
  normalize: [normalize, %s]
  keys: [team, queue]
`, stdin)
}

// acmeDepot is a depot whose receiver serves the yard with home: the
// declared source acme, its hook, and a crew member subscribed to it.
func acmeDepot(t *testing.T) (*depot, string) {
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
	declDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(declDir, "normalize"), []byte(acmeNormalize), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
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
	return r, stdin
}

func acmeBody(sent time.Time) []byte {
	b, _ := json.Marshal(map[string]any{"sent": sent.UnixMilli(), "note": "hello"})
	return b
}

func (r *depot) acme(t *testing.T, body []byte, key, delivery string) int {
	t.Helper()
	return r.post(t, "/hooks/acme", body, map[string]string{
		"Acme-Signature": "sha256=" + sign(key, body), "Acme-Delivery": delivery,
		"Content-Type": "application/json"})
}

// A delivery to a hook of a declared source with a good signature is
// recorded with the envelope the command printed: the source the core's,
// the delivery the header's.
func TestDeclaredDeliveryRecorded(t *testing.T) {
	r, stdin := acmeDepot(t)
	ctx := context.Background()
	if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "ann"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.AddSubscription(ctx, "ann", store.Subscription{Source: "acme", Scope: store.ScopeAll,
		Where: map[string]string{"queue": "triage"}}, "me"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	body := acmeBody(r.now)
	if code := r.acme(t, body, secret, "a-1"); code != http.StatusOK {
		t.Fatalf("good signature: %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 1 {
		t.Fatalf("want 1 hook event, got %d", len(evs))
	}
	e := evs[0]
	if e.Actor != "hook:acme" || e.Data["hook"] != "acme" || e.Data["source"] != "acme" ||
		e.Data["type"] != "ping" || e.Data["action"] != "create" || e.Data["text"] != "hi" ||
		e.Data["delivery"] != "a-1" {
		t.Fatalf("event: %+v", e.Data)
	}
	if fmt.Sprint(e.Data["fields"]) != "map[queue:[triage] team:[ENG]]" {
		t.Fatalf("fields: %v", e.Data["fields"])
	}
	// The same body again is the same delivery sent again: bodies unique.
	r.now = r.now.Add(time.Second)
	if code := r.acme(t, body, secret, "a-2"); code != http.StatusOK {
		t.Fatalf("redelivery: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 hook event, got %d", n)
	}
	// The command was handed the headers and the body, and the
	// subscription on the hook's key matched.
	raw, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Headers map[string][]string `json:"headers"`
		Body    map[string]any      `json:"body"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("stdin is no {headers, body}: %v", err)
	}
	if len(in.Headers["Acme-Signature"]) != 1 || in.Body["note"] != "hello" {
		t.Fatalf("stdin: %s", raw)
	}
	page, err := r.st.Inbox(ctx, "ann", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("ann's inbox holds %d, want the delivery", page.Total)
	}
}

// A bad signature, a timestamp outside the window and a failing command
// are each refused and reach no subscription.
func TestDeclaredDeliveryRefused(t *testing.T) {
	r, stdin := acmeDepot(t)
	home := filepath.Dir(stdin)
	ctx := context.Background()
	if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "ann"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.AddSubscription(ctx, "ann", store.Subscription{Source: "acme", Scope: store.ScopeAll}, "me"); err != nil {
		t.Fatal(err)
	}
	inbox := func() int {
		t.Helper()
		page, err := r.st.Inbox(ctx, "ann", 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Total
	}
	r.now = time.Now()
	if code := r.acme(t, acmeBody(r.now), "wrong-secret", "b-1"); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	stale := acmeBody(r.now.Add(-61 * time.Second))
	if code := r.acme(t, stale, secret, "b-2"); code != http.StatusUnauthorized {
		t.Fatalf("timestamp outside the window: %d", code)
	}
	fail := []byte("#!/bin/sh\necho boom >&2\nexit 3\n")
	if err := os.WriteFile(filepath.Join(home, "sources", "acme", "normalize"), fail, 0o700); err != nil {
		t.Fatal(err)
	}
	if code := r.acme(t, acmeBody(r.now), secret, "b-3"); code != http.StatusBadRequest {
		t.Fatalf("failing command: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("refused deliveries left %d events", n)
	}
	if n := inbox(); n != 0 {
		t.Fatalf("refused deliveries reached ann's inbox %d times", n)
	}
	if got := r.rc.Rejected(); got["unauthorized"] != 2 || got["bad_request"] != 1 {
		t.Fatalf("rejected = %v", got)
	}
}

func TestAdapterFor(t *testing.T) {
	builtin, ok := hooks.AdapterFor("linear", nil, "")
	if !ok || builtin == nil {
		t.Fatal("linear has no adapter")
	}
	d, err := source.Parse("acme.yaml", []byte(acmeText("/tmp/stdin.json")))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hooks.AdapterFor("acme", d, "/tmp/home"); !ok {
		t.Fatal("a declared source with a hook key has no adapter")
	}
	hookless, err := source.Parse("q.yaml", []byte("name: q\nbase: https://q.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	for name, decl := range map[string]*source.Declaration{"unknown": nil, "acme": nil, "q": hookless, "other": d} {
		if _, ok := hooks.AdapterFor(name, decl, ""); ok {
			t.Errorf("%s with %+v has an adapter", name, decl)
		}
	}
}

// A subscription --where on a key in hook.keys is taken, and one on a key
// not there is refused; a hook's says and makes hold their filters to the
// same keys.
func TestDeclaredWhereKeys(t *testing.T) {
	r, _ := acmeDepot(t)
	ctx := context.Background()
	if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "ann"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, where := range []map[string]string{{"queue": "triage"}, {"team": "ENG"}, {"type": "ping", "action": "create"}} {
		if err := r.st.AddSubscription(ctx, "ann", store.Subscription{Source: "acme", Scope: store.ScopeAll, Where: where}, "me"); err != nil {
			t.Fatalf("where %v: %v", where, err)
		}
	}
	if err := r.st.AddSubscription(ctx, "ann", store.Subscription{Source: "acme", Scope: store.ScopeAll,
		Where: map[string]string{"train": "yard-x"}}, "me"); err == nil ||
		!strings.Contains(err.Error(), `cannot filter acme on "train": use type, action, team, project, repo, label, queue`) {
		t.Fatalf("where train: err = %v", err)
	}
	say, err := store.ParseSay("team=ENG -> done")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "says", Source: "acme", SecretFile: hookSecret(t), Says: []store.Say{say}}, "me"); err != nil {
		t.Fatalf("says on team: %v", err)
	}
	bad, _ := store.ParseSay("train=yard-x -> done")
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "bad", Source: "acme", SecretFile: hookSecret(t), Says: []store.Say{bad}}, "me"); err == nil ||
		!strings.Contains(err.Error(), `cannot filter acme on "train"`) {
		t.Fatalf("says on train: err = %v", err)
	}
	make, err := store.ParseMake("team=ENG -> task:new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "makes", Source: "acme", SecretFile: hookSecret(t), Makes: []store.Make{make}}, "me"); err != nil {
		t.Fatalf("makes on team: %v", err)
	}
}

func hookSecret(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "hook.secret")
	if err := os.WriteFile(f, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// Verify from hook.verify: a header timestamp in seconds, a nested body
// path, a body that is no JSON, and none at all.
func TestDeclaredVerify(t *testing.T) {
	decl := func(verify string) hooks.Declared {
		t.Helper()
		d, err := source.Parse("q.yaml", []byte("name: q\nhook:\n  verify:\n"+verify+
			"  bodies: unique\n  normalize: [normalize]\n"))
		if err != nil {
			t.Fatal(err)
		}
		return hooks.Declared{D: d}
	}
	sig := func(prefix string, body []byte) string { return prefix + sign(secret, body) }
	now := time.Now()
	for _, tc := range []struct {
		name   string
		verify string
		header map[string]string
		body   string
		want   string // "": verified; else the error it wraps
	}{
		{"no timestamp", "    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`))}, `{}`, ""},
		{"a timestamp in seconds in a header",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`)), "Q-Sent": fmt.Sprint(now.Unix())}, `{}`, ""},
		{"a stale header timestamp",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`)), "Q-Sent": fmt.Sprint(now.Add(-6 * time.Minute).Unix())}, `{}`, "unauthorized"},
		{"a timestamp beyond the largest window",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { header: Q-Sent, unit: s, window: 2562047h47m16.854775807s }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`)), "Q-Sent": "9223372036854775807"}, `{}`, "unauthorized"},
		{"a missing header timestamp",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`))}, `{}`, "unauthorized"},
		{"a nested body path",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { body: meta.sent, unit: ms, window: 1m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{"meta":{"sent":"`+fmt.Sprint(now.UnixMilli())+`"}}`))},
			`{"meta":{"sent":"` + fmt.Sprint(now.UnixMilli()) + `"}}`, ""},
		{"a body that is no JSON",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { body: sent, unit: ms, window: 1m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`plain`))}, `plain`, "bad request"},
		{"a missing prefix",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex, prefix: \"sha256=\" }\n    delivery: { header: Q-Delivery }\n",
			map[string]string{"Q-Sig": sign(secret, []byte(`{}`))}, `{}`, "unauthorized"},
	} {
		ad := decl(tc.verify)
		req, _ := http.NewRequest(http.MethodPost, "/hooks/q", strings.NewReader(tc.body))
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		_, err := ad.Verify(req, []byte(tc.body), []byte(secret), now)
		switch tc.want {
		case "":
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
		case "unauthorized":
			if !errors.Is(err, hooks.ErrUnauthorized) {
				t.Errorf("%s: err = %v, want unauthorized", tc.name, err)
			}
		case "bad request":
			if !errors.Is(err, hooks.ErrBadRequest) {
				t.Errorf("%s: err = %v, want bad request", tc.name, err)
			}
		}
	}
}

// Normalize by the command: the source is the core's, whatever the command
// printed; an envelope without a type is refused, as is one the command
// fails to print.
func TestDeclaredNormalize(t *testing.T) {
	run := func(t *testing.T, script, body string) (*hooks.Envelope, error) {
		t.Helper()
		home, declDir := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(declDir, "normalize"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		d, err := source.Parse("q.yaml", []byte("name: q\nhook:\n  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n  bodies: repeat\n  normalize: [normalize]\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := d.CopyCommandFiles(declDir, home); err != nil {
			t.Fatal(err)
		}
		return hooks.Declared{D: d, Home: home}.Normalize(http.Header{"Q-Delivery": {"d-1"}}, []byte(body))
	}
	env, err := run(t, "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{\"source\":\"other\",\"type\":\"Ping\",\"text\":\"hi\"}'\n", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if env.Source != "q" || env.Type != "Ping" || env.Mentions == nil {
		t.Fatalf("envelope = %+v", env)
	}
	long := strings.Repeat("w", 600)
	env, err = run(t, "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{\"type\":\"ping\",\"text\":\""+long+"\"}'\n", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(env.Text)) != 500 {
		t.Fatalf("text is %d runes", len([]rune(env.Text)))
	}
	if _, err := run(t, "#!/bin/sh\ncat >/dev/null\nprintf '%s' '{\"text\":\"hi\"}'\n", `{}`); err == nil ||
		!strings.Contains(err.Error(), "its envelope has no type") {
		t.Fatalf("typeless envelope: err = %v", err)
	}
	if _, err := run(t, "#!/bin/sh\nexit 3\n", `{}`); err == nil {
		t.Fatal("a failing command normalized")
	}
	// Not JSON: the command is handed the body as text.
	seen := filepath.Join(t.TempDir(), "stdin.json")
	env, err = run(t, "#!/bin/sh\ncat > "+seen+"\nprintf '%s' '{\"type\":\"ping\"}'\n", `plain words`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(seen)
	var in struct {
		Body any `json:"body"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if in.Body != "plain words" || env.Type != "ping" {
		t.Fatalf("stdin %s, envelope %+v", raw, env)
	}
}

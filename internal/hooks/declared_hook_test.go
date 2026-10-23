package hooks_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// acmeDecl is a declared source that receives: a Linear-like signature in
// Acme-Signature past sha256=, a delivery id in Acme-Delivery, a body
// timestamp in ms, bodies unique, and the command printing a fixed
// envelope. stdin is the file argv[1] names, so the test reads what the
// command was handed.
const acmeNormalize = `#!/bin/sh
printf '%s' "$YARDR_SOURCE_BASE" > "$1.base"
cat > "$1"
printf '%s' '{"type":"ping","action":"create","text":"hi","actor":{"id":"u1","name":"Ann"},"fields":{"team":["ENG"],"queue":["triage"]}}'
`

func acmeText(stdin string) string {
	return fmt.Sprintf(`name: acme
base: https://api.acme.test
bases:
  us: https://api.acme.test
  eu: https://eu.api.acme.test
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
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
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
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "acme.yaml", acmeText(stdin), "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.SetSource(ctx, "acme", store.SourceBase, "eu", "me"); err != nil {
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
		Where: map[string]string{"queue": "triage"}}, "ann"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	body := acmeBody(r.now)
	if code := r.acme(t, body, secret, "a-1"); code != http.StatusOK {
		t.Fatalf("good signature: %d", code)
	}
	if base, err := os.ReadFile(stdin + ".base"); err != nil || string(base) != "https://eu.api.acme.test" {
		t.Fatalf("normalize command received base %q, %v", base, err)
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
	if err := r.st.AddSubscription(ctx, "ann", store.Subscription{Source: "acme", Scope: store.ScopeAll}, "ann"); err != nil {
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
	scripttest.Write(t, filepath.Join(home, "sources", "acme", "normalize"), string(fail))
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

// Bodies scoped per hook: one event's body goes to every hook on the
// source, and each hook drops only its own repeats.
func TestDeclaredBodiesPerHook(t *testing.T) {
	r, stdin := acmeDepot(t)
	ctx := context.Background()
	text := strings.Replace(acmeText(stdin), "bodies: unique", "bodies: unique-per-hook", 1)
	if _, err := r.st.PutSource(ctx, "acme.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "acme2", Source: "acme", SecretFile: hookSecret(t)}, "me"); err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse("acme.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if ad, _ := hooks.AdapterFor("acme", d, ""); ad.Bodies() != hooks.BodiesUniquePerHook {
		t.Fatalf("bodies = %v, want per hook", ad.Bodies())
	}
	r.now = time.Now()
	body := acmeBody(r.now)
	if code := r.acme(t, body, secret, "a-1"); code != http.StatusOK {
		t.Fatalf("first hook: %d", code)
	}
	// The same body to the second hook is a new delivery, not a redelivery.
	if code := r.post(t, "/hooks/acme2", body, map[string]string{
		"Acme-Signature": "sha256=" + sign(secret, body), "Acme-Delivery": "a-1",
		"Content-Type": "application/json"}); code != http.StatusOK {
		t.Fatalf("second hook: %d", code)
	}
	// The same body again to the first hook is its own repeat.
	r.now = r.now.Add(time.Second)
	if code := r.acme(t, body, secret, "a-2"); code != http.StatusOK {
		t.Fatalf("redelivery: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 2 {
		t.Fatalf("want 2 hook events, got %d", n)
	}
}

// secretDepot is a depot whose receiver serves the yard with the declared
// source sentry, verified by verify (scheme secret in Sentry-Secret),
// bodies unique, and the command printing a fixed envelope.
const secretNormalize = `#!/bin/sh
printf '%s' '{"type":"ping","action":"create","text":"hi"}'
`

func secretDepot(t *testing.T, verify string) *depot {
	t.Helper()
	home := t.TempDir()
	r := newDepot(t)
	r.rc = hooks.NewReceiver(r.own, home, nil)
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = r.rc.Handler()
	ctx := context.Background()
	text := "name: sentry\nhook:\n  verify:\n" + verify + "  bodies: unique\n  normalize: [normalize]\n"
	d, err := source.Parse("sentry.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	declDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(declDir, "normalize"), []byte(secretNormalize), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "sentry.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(home, "sentry.secret")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "sentry", Source: "sentry", SecretFile: secretFile}, "me"); err != nil {
		t.Fatal(err)
	}
	return r
}

// A delivery carrying the secret is recorded with no delivery id; the same
// body again is dropped, and a wrong or missing secret is refused. An
// empty secret file fails closed, as with every scheme.
func TestDeclaredSecretDelivery(t *testing.T) {
	r := secretDepot(t, "    scheme: secret\n    signature: { header: Sentry-Secret }\n")
	body := []byte(`{"update_id":7,"note":"hello"}`)
	if code := r.post(t, "/hooks/sentry", body, map[string]string{"Sentry-Secret": secret}); code != http.StatusOK {
		t.Fatalf("good secret: %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 1 || evs[0].Data["type"] != "ping" {
		t.Fatalf("events: %+v", evs)
	}
	if _, ok := evs[0].Data["delivery"]; ok {
		t.Fatalf("delivery = %v, want none", evs[0].Data["delivery"])
	}
	// The same body again is the same delivery sent again: bodies unique.
	if code := r.post(t, "/hooks/sentry", body, map[string]string{"Sentry-Secret": secret}); code != http.StatusOK {
		t.Fatalf("redelivery: %d", code)
	}
	if code := r.post(t, "/hooks/sentry", body, map[string]string{"Sentry-Secret": "nope"}); code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d", code)
	}
	if code := r.post(t, "/hooks/sentry", body, nil); code != http.StatusUnauthorized {
		t.Fatalf("missing secret: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 hook event, got %d", n)
	}
	// An empty secret file fails closed: a secret compared against
	// nothing would let anyone through.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(context.Background(), store.Hook{Name: "sentry-open", Source: "sentry", SecretFile: empty}, "me"); err != nil {
		t.Fatal(err)
	}
	if code := r.post(t, "/hooks/sentry-open", body, map[string]string{"Sentry-Secret": secret}); code != http.StatusInternalServerError {
		t.Fatalf("empty secret: %d", code)
	}
}

// A retry the service re-serialized is still the same delivery when the
// declaration names the id in the body: only the id, not the bytes, tells
// them apart.
func TestDeclaredDeliveryBodyDedupesReserializedRetry(t *testing.T) {
	r := secretDepot(t, "    scheme: secret\n    signature: { header: Sentry-Secret }\n    delivery: { body: update_id }\n")
	first := []byte(`{"update_id":7,"note":"hello"}`)
	retry := []byte("{\n  \"note\": \"hello\",\n  \"update_id\": 7\n}\n")
	next := []byte(`{"update_id":8,"note":"hello"}`)
	large := []byte(`{"update_id":9007199254740992,"note":"hello"}`)
	largeNext := []byte(`{"update_id":9007199254740993,"note":"hello"}`)
	hdr := map[string]string{"Sentry-Secret": secret}
	if code := r.post(t, "/hooks/sentry", first, hdr); code != http.StatusOK {
		t.Fatalf("first: %d", code)
	}
	if code := r.post(t, "/hooks/sentry", retry, hdr); code != http.StatusOK {
		t.Fatalf("retry: %d", code)
	}
	if code := r.post(t, "/hooks/sentry", next, hdr); code != http.StatusOK {
		t.Fatalf("next: %d", code)
	}
	if code := r.post(t, "/hooks/sentry", large, hdr); code != http.StatusOK {
		t.Fatalf("large: %d", code)
	}
	if code := r.post(t, "/hooks/sentry", largeNext, hdr); code != http.StatusOK {
		t.Fatalf("large next: %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 4 {
		t.Fatalf("want 4 hook events, got %d", len(evs))
	}
	want := []string{"7", "8", "9007199254740992", "9007199254740993"}
	for i, e := range evs {
		if e.Data["delivery"] != want[i] {
			t.Errorf("delivery %d = %v, want %q", i, e.Data["delivery"], want[i])
		}
	}
}

// The train a head branch carries is the yard's convention, not the
// service's: the receiver derives it when the envelope left it empty, and
// keeps what the script said.
func TestReceiverDerivesTrainFromHeadBranch(t *testing.T) {
	r, stdin := acmeDepot(t)
	normalize := []byte(`#!/bin/sh
in=$(cat)
case "$in" in
*keep*) printf '%s' '{"type":"run","fields":{"head_branch":["yard/yard-x1"],"train":["yard-kept"]}}';;
*main*) printf '%s' '{"type":"run","fields":{"head_branch":["main"]}}';;
*) printf '%s' '{"type":"run","fields":{"head_branch":["yard/yard-x1"],"head_sha":["abc"]}}';;
esac
`)
	scripttest.Write(t, filepath.Join(filepath.Dir(stdin), "sources", "acme", "normalize"), string(normalize))
	r.now = time.Now()
	body := func(note string) []byte {
		b, _ := json.Marshal(map[string]any{"sent": r.now.UnixMilli(), "note": note})
		return b
	}
	for delivery, note := range map[string]string{"t-1": "run", "t-2": "keep this", "t-3": "main"} {
		if code := r.acme(t, body(note), secret, delivery); code != http.StatusOK {
			t.Fatalf("%s: %d", delivery, code)
		}
	}
	fields := map[string]string{}
	for _, e := range r.hookEvents(t) {
		fields[e.Data["delivery"].(string)] = fmt.Sprint(e.Data["fields"])
	}
	for delivery, want := range map[string]string{
		// Derived from the head branch.
		"t-1": "map[head_branch:[yard/yard-x1] head_sha:[abc] train:[yard-x1]]",
		// The script said the train: kept.
		"t-2": "map[head_branch:[yard/yard-x1] train:[yard-kept]]",
		// No yardr branch: no train.
		"t-3": "map[head_branch:[main]]",
	} {
		if fields[delivery] != want {
			t.Errorf("%s fields = %s, want %s", delivery, fields[delivery], want)
		}
	}
}

func TestAdapterFor(t *testing.T) {
	if _, ok := hooks.AdapterFor("github", nil, ""); ok {
		t.Fatal("github without a declaration has an adapter")
	}
	if _, ok := hooks.AdapterFor("linear", nil, ""); ok {
		t.Fatal("linear without a declaration has an adapter")
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

// The pack's github source answers every lookup when the yard has it:
// the adapter, replying, opening, setting state, forwarding and linking.
// Without it, github answers none.
func TestGitHubPackLookups(t *testing.T) {
	dir := filepath.Join("..", "..", "packs", "github")
	text, err := os.ReadFile(filepath.Join(dir, "github.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "github.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "github" {
		t.Fatalf("declaration = %+v", d)
	}
	if ad, ok := hooks.AdapterFor("github", d, ""); !ok {
		t.Error("declared github has no adapter")
	} else if _, ok := ad.(hooks.Declared); !ok {
		t.Errorf("AdapterFor is %T, want Declared", ad)
	}
	if r, ok := hooks.ReplierFor("github", d, "", ""); !ok {
		t.Error("declared github has no replier")
	} else if _, ok := r.(hooks.Declared); !ok {
		t.Errorf("ReplierFor is %T, want Declared", r)
	}
	if o, ok := hooks.OpenerFor("github", d, "", ""); !ok {
		t.Error("declared github has no opener")
	} else if _, ok := o.(hooks.Declared); !ok {
		t.Errorf("OpenerFor is %T, want Declared", o)
	}
	if s, ok := hooks.StaterFor("github", d, "", ""); !ok {
		t.Error("declared github has no stater")
	} else if _, ok := s.(hooks.Declared); !ok {
		t.Errorf("StaterFor is %T, want Declared", s)
	}
	if f, ok := hooks.ForwarderFor("github", d, "", ""); !ok {
		t.Error("declared github has no forwarder")
	} else if _, ok := f.(hooks.Declared); !ok {
		t.Errorf("ForwarderFor is %T, want Declared", f)
	}
	if got, err := hooks.IssueIn("github", "https://github.com/acme/yardr/issues/23", d); err != nil || got != "https://github.com/acme/yardr/issues/23" {
		t.Errorf("IssueIn = %q, %v", got, err)
	}
	if name, _, err := hooks.IssueWithHooks("https://github.com/acme/yardr/issues/23", []*source.Declaration{d}, map[string]bool{"github": true}); err != nil || name != "github" {
		t.Errorf("IssueWithHooks = %q, %v", name, err)
	}
	if _, ok := hooks.ReplierFor("github", nil, "", ""); ok {
		t.Error("github without a declaration has a replier")
	}
	if _, ok := hooks.OpenerFor("github", nil, "", ""); ok {
		t.Error("github without a declaration has an opener")
	}
	if _, ok := hooks.StaterFor("github", nil, "", ""); ok {
		t.Error("github without a declaration has a stater")
	}
	if _, ok := hooks.ForwarderFor("github", nil, "", ""); ok {
		t.Error("github without a declaration has a forwarder")
	}
	links := hooks.LinkSources(d)
	githubLinks := 0
	for _, name := range links {
		if name == "github" {
			githubLinks++
		}
	}
	if githubLinks != 1 {
		t.Errorf("LinkSources = %v, want github exactly once", links)
	}
}

func TestDeclaredGithubDoesNotFallBackForMissingCapabilities(t *testing.T) {
	d := &source.Declaration{Name: "github"}
	if _, ok := hooks.AdapterFor("github", d, ""); ok {
		t.Error("AdapterFor answered without a hook declaration")
	}
	if _, ok := hooks.ReplierFor("github", d, "", ""); ok {
		t.Error("ReplierFor answered without thread commands")
	}
	if _, ok := hooks.PullerFor("github", d); ok {
		t.Error("PullerFor answered without a pull link shape")
	}
	if _, ok := hooks.RepoerFor("github", d); ok {
		t.Error("RepoerFor answered without a repository pattern")
	}
}

// The pack's linear source answers every lookup when the yard has it:
// the adapter, replying, opening, setting state, forwarding and linking.
// Without it, linear answers none.
func TestLinearPackLookups(t *testing.T) {
	dir := filepath.Join("..", "..", "packs", "linear")
	text, err := os.ReadFile(filepath.Join(dir, "linear.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "linear.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "linear" {
		t.Fatalf("declaration = %+v", d)
	}
	if ad, ok := hooks.AdapterFor("linear", d, ""); !ok {
		t.Error("declared linear has no adapter")
	} else if _, ok := ad.(hooks.Declared); !ok {
		t.Errorf("AdapterFor is %T, want Declared", ad)
	}
	if r, ok := hooks.ReplierFor("linear", d, "", ""); !ok {
		t.Error("declared linear has no replier")
	} else if _, ok := r.(hooks.Declared); !ok {
		t.Errorf("ReplierFor is %T, want Declared", r)
	}
	if o, ok := hooks.OpenerFor("linear", d, "", ""); !ok {
		t.Error("declared linear has no opener")
	} else if _, ok := o.(hooks.Declared); !ok {
		t.Errorf("OpenerFor is %T, want Declared", o)
	}
	if s, ok := hooks.StaterFor("linear", d, "", ""); !ok {
		t.Error("declared linear has no stater")
	} else if _, ok := s.(hooks.Declared); !ok {
		t.Errorf("StaterFor is %T, want Declared", s)
	}
	if f, ok := hooks.ForwarderFor("linear", d, "", ""); !ok {
		t.Error("declared linear has no forwarder")
	} else if _, ok := f.(hooks.Declared); !ok {
		t.Errorf("ForwarderFor is %T, want Declared", f)
	}
	if got, err := hooks.IssueIn("linear", "https://linear.app/acme/issue/YRD-1", d); err != nil || got != "https://linear.app/acme/issue/YRD-1" {
		t.Errorf("IssueIn = %q, %v", got, err)
	}
	if name, _, err := hooks.IssueWithHooks("https://linear.app/acme/issue/YRD-1", []*source.Declaration{d}, map[string]bool{"linear": true}); err != nil || name != "linear" {
		t.Errorf("IssueWithHooks = %q, %v", name, err)
	}
	if _, ok := hooks.ReplierFor("linear", nil, "", ""); ok {
		t.Error("linear without a declaration has a replier")
	}
	if _, ok := hooks.OpenerFor("linear", nil, "", ""); ok {
		t.Error("linear without a declaration has an opener")
	}
	if _, ok := hooks.StaterFor("linear", nil, "", ""); ok {
		t.Error("linear without a declaration has a stater")
	}
	if _, ok := hooks.ForwarderFor("linear", nil, "", ""); ok {
		t.Error("linear without a declaration has a forwarder")
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
		if err := r.st.AddSubscription(ctx, "ann", store.Subscription{Source: "acme", Scope: store.ScopeAll, Where: where}, "ann"); err != nil {
			t.Fatalf("where %v: %v", where, err)
		}
	}
	if err := r.st.AddSubscription(ctx, "ann", store.Subscription{Source: "acme", Scope: store.ScopeAll,
		Where: map[string]string{"train": "yard-x"}}, "ann"); err == nil ||
		!strings.Contains(err.Error(), `cannot filter acme on "train": use type, action, team, project, repo, label, event, queue`) {
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

// signBase64 is the HMAC-SHA256 of data under key, written in base64 as a
// service that signs so writes it.
func signBase64(key []byte, data string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// Verify from hook.verify: a header timestamp in seconds, a nested body
// path, a body that is no JSON, and none at all; a signature in base64, or
// one of several; a delivery id that the signature covers.
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
	// A timestamp in a header is signed with the body, as a reader that
	// takes one requires: the signature covers v0:{timestamp}:{body}, the
	// header's raw string and the raw body.
	const headerTimed = "    scheme: hmac-sha256\n    signed: \"v0:{timestamp}:{body}\"\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n"
	sigAt := func(ts string, body []byte) string { return sign(secret, []byte("v0:"+ts+":"+string(body))) }
	now := time.Now()
	sent, stale, beyond := fmt.Sprint(now.Unix()), fmt.Sprint(now.Add(-6*time.Minute).Unix()), "9223372036854775807"
	// A delivery id is signed with the timestamp and the body, as it arrived.
	const idTimed = "    scheme: hmac-sha256\n    signed: \"{delivery}.{timestamp}.{body}\"\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { header: Q-Sent, unit: s, window: 5m }\n"
	sigID := func(id string) string { return sign(secret, []byte(id+"."+sent+"."+`{}`)) }
	// A signature in base64, and the same one spelled another way: the last
	// character holds two bits that belong to no byte, and flipping one
	// leaves the same bytes for a decoder that lets them be.
	const base64Sig = "    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: base64 }\n    delivery: { header: Q-Delivery }\n"
	good := signBase64([]byte(secret), `{}`)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	spare := good[:len(good)-2] + string(alphabet[strings.IndexByte(alphabet, good[len(good)-2])^1]) + "="
	if same, err := base64.StdEncoding.DecodeString(spare); err != nil || spare == good || base64.StdEncoding.EncodeToString(same) != good {
		t.Fatalf("the signature %q is not spelled another way by %q: %v", good, spare, err)
	}
	// Several signatures side by side, the ones this scheme checks behind
	// sha256=.
	const listed = "    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex, prefix: \"sha256=\", list: \",\" }\n    delivery: { header: Q-Delivery }\n"
	old, older := "sha256="+sign("old-secret", []byte(`{}`)), "sha256="+sign("older-secret", []byte(`{}`))
	for _, tc := range []struct {
		name     string
		verify   string
		header   map[string]string
		body     string
		want     string // "": verified; else the error it wraps
		delivery string // the id Verify returns when verified
	}{
		{"no timestamp", "    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`))}, `{}`, "", ""},
		{"a timestamp in seconds in a header",
			headerTimed + "    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Sig": sigAt(sent, []byte(`{}`)), "Q-Sent": sent}, `{}`, "", ""},
		{"a stale header timestamp",
			headerTimed + "    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Sig": sigAt(stale, []byte(`{}`)), "Q-Sent": stale}, `{}`, "unauthorized", ""},
		{"a timestamp beyond the largest window",
			headerTimed + "    timestamp: { header: Q-Sent, unit: s, window: 2562047h47m16.854775807s }\n",
			map[string]string{"Q-Sig": sigAt(beyond, []byte(`{}`)), "Q-Sent": beyond}, `{}`, "unauthorized", ""},
		{"a missing header timestamp",
			headerTimed + "    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Sig": sigAt("", []byte(`{}`))}, `{}`, "unauthorized", ""},
		{"a nested body path",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { body: meta.sent, unit: ms, window: 1m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{"meta":{"sent":"`+fmt.Sprint(now.UnixMilli())+`"}}`))},
			`{"meta":{"sent":"` + fmt.Sprint(now.UnixMilli()) + `"}}`, "", ""},
		{"a body that is no JSON",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { body: sent, unit: ms, window: 1m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`plain`))}, `plain`, "bad request", ""},
		{"a missing prefix",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex, prefix: \"sha256=\" }\n    delivery: { header: Q-Delivery }\n",
			map[string]string{"Q-Sig": sign(secret, []byte(`{}`))}, `{}`, "unauthorized", ""},
		{"a secret compared as it arrives",
			"    scheme: secret\n    signature: { header: Q-Secret }\n",
			map[string]string{"Q-Secret": secret}, `{}`, "", ""},
		{"a wrong secret",
			"    scheme: secret\n    signature: { header: Q-Secret }\n",
			map[string]string{"Q-Secret": "nope"}, `{}`, "unauthorized", ""},
		{"a missing secret",
			"    scheme: secret\n    signature: { header: Q-Secret }\n",
			nil, `{}`, "unauthorized", ""},
		{"a secret with a delivery header",
			"    scheme: secret\n    signature: { header: Q-Secret }\n    delivery: { header: Q-Delivery }\n",
			map[string]string{"Q-Secret": secret, "Q-Delivery": "d-9"}, `{}`, "", "d-9"},
		{"a secret with a stale timestamp",
			"    scheme: secret\n    signature: { header: Q-Secret }\n    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Secret": secret, "Q-Sent": fmt.Sprint(now.Add(-6 * time.Minute).Unix())}, `{}`, "unauthorized", ""},
		{"a delivery id in the body",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { body: update_id }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{"update_id":42}`))}, `{"update_id":42}`, "", "42"},
		{"a nested delivery path",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { body: meta.id }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{"meta":{"id":"x-1"}}`))}, `{"meta":{"id":"x-1"}}`, "", "x-1"},
		{"a delivery id missing from the body",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { body: update_id }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`))}, `{}`, "", ""},
		{"a delivery body that is no JSON",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { body: update_id }\n",
			map[string]string{"Q-Sig": sig("", []byte(`plain`))}, `plain`, "", ""},
		{"a delivery id that is no scalar",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { body: update_id }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{"update_id":{"n":1}}`))}, `{"update_id":{"n":1}}`, "", ""},
		{"a signed delivery id",
			idTimed, map[string]string{"Q-Sig": sigID("d-1"), "Q-Sent": sent, "Q-Delivery": "d-1"}, `{}`, "", "d-1"},
		{"a delivery id signed as it arrived",
			idTimed, map[string]string{"Q-Sig": sigID("0042"), "Q-Sent": sent, "Q-Delivery": "0042"}, `{}`, "", "0042"},
		{"a changed delivery id",
			idTimed, map[string]string{"Q-Sig": sigID("d-1"), "Q-Sent": sent, "Q-Delivery": "d-2"}, `{}`, "unauthorized", ""},
		{"a missing delivery id",
			idTimed, map[string]string{"Q-Sig": sigID("d-1"), "Q-Sent": sent}, `{}`, "unauthorized", ""},
		{"a delivery id that spells a placeholder",
			idTimed, map[string]string{"Q-Sig": sigID("{body}"), "Q-Sent": sent, "Q-Delivery": "{body}"}, `{}`, "", "{body}"},
		{"a body that spells the placeholders",
			idTimed, map[string]string{"Q-Sig": sign(secret, []byte("d-1."+sent+"."+`{"n":"{timestamp}{delivery}{body}"}`)), "Q-Sent": sent, "Q-Delivery": "d-1"},
			`{"n":"{timestamp}{delivery}{body}"}`, "", "d-1"},
		{"a base64 signature",
			base64Sig, map[string]string{"Q-Sig": good}, `{}`, "", ""},
		{"a hex signature where base64 is declared",
			base64Sig, map[string]string{"Q-Sig": sign(secret, []byte(`{}`))}, `{}`, "unauthorized", ""},
		{"a base64 signature spelled another way",
			base64Sig, map[string]string{"Q-Sig": spare}, `{}`, "unauthorized", ""},
		{"a list that holds one signature",
			listed, map[string]string{"Q-Sig": "sha256=" + sig("", []byte(`{}`))}, `{}`, "", ""},
		{"a list with the signature last",
			listed, map[string]string{"Q-Sig": old + ", " + older + ", sha256=" + sig("", []byte(`{}`))}, `{}`, "", ""},
		{"a list with the signature first",
			listed, map[string]string{"Q-Sig": "sha256=" + sig("", []byte(`{}`)) + "," + old}, `{}`, "", ""},
		{"a list with no signature that matches",
			listed, map[string]string{"Q-Sig": old + "," + older}, `{}`, "unauthorized", ""},
		{"a list of another kind's signatures only",
			listed, map[string]string{"Q-Sig": "v2=" + sig("", []byte(`{}`)) + ",v1a=" + sig("", []byte(`{}`))}, `{}`, "unauthorized", ""},
		{"a list with nothing in it",
			listed, map[string]string{"Q-Sig": " , ,"}, `{}`, "unauthorized", ""},
		{"a list declared, a header with none",
			listed, nil, `{}`, "unauthorized", ""},
	} {
		ad := decl(tc.verify)
		req, _ := http.NewRequest(http.MethodPost, "/hooks/q", strings.NewReader(tc.body))
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		delivery, err := ad.Verify(req, []byte(tc.body), []byte(secret), now)
		switch tc.want {
		case "":
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			} else if delivery != tc.delivery {
				t.Errorf("%s: delivery = %q, want %q", tc.name, delivery, tc.delivery)
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

// A delivery the edge kept while the yard was away is checked against
// when the edge received it, which the link marked on the request, and
// not against the yard's clock: a timestamp ten minutes old verifies
// against a received of ten minutes ago, and the same delivery with no
// mark is refused. A refusal against the edge's time says whose clock
// the timestamp missed.
func TestDeclaredWindowAgainstTheEdgesTime(t *testing.T) {
	d, err := source.Parse("q.yaml", []byte("name: q\nhook:\n  verify:\n"+
		"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n"+
		"    delivery: { header: Q-Delivery }\n    timestamp: { body: sent, unit: ms, window: 1m }\n"+
		"  bodies: unique\n  normalize: [normalize]\n"))
	if err != nil {
		t.Fatal(err)
	}
	ad := hooks.Declared{D: d}
	now := time.Now()
	sent := now.Add(-10 * time.Minute)
	body := []byte(`{"sent":` + fmt.Sprint(sent.UnixMilli()) + `}`)
	signature := sign(secret, body)
	verify := func(t *testing.T, received time.Time, marked bool) (string, error) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "/hooks/q", strings.NewReader(string(body)))
		req.Header.Set("Q-Sig", signature)
		req.Header.Set("Q-Delivery", "q-1")
		if marked {
			req = req.WithContext(hooks.WithEdgeReceived(req.Context(), received))
		}
		return ad.Verify(req, body, []byte(secret), now)
	}
	if delivery, err := verify(t, sent, true); err != nil || delivery != "q-1" {
		t.Errorf("marked with the edge's received: delivery %q, err %v", delivery, err)
	}
	if _, err := verify(t, time.Time{}, false); !errors.Is(err, hooks.ErrUnauthorized) {
		t.Errorf("unmarked: err = %v, want unauthorized", err)
	}
	_, err = verify(t, sent.Add(-10*time.Minute), true)
	if !errors.Is(err, hooks.ErrUnauthorized) || !strings.Contains(err.Error(), "when the edge received it") {
		t.Errorf("stale against the edge's time: err = %v", err)
	}
}

// A signature over timestamp and body together: the basestring is the
// template with the header's raw timestamp string and the raw body
// filled in, so a signature over the body alone, a bad signature and a
// correctly signed stale timestamp are each refused, and the timestamp
// is read as it arrived, never re-rendered.
func TestDeclaredVerifySignedTemplate(t *testing.T) {
	d, err := source.Parse("q.yaml", []byte("name: q\nhook:\n  verify:\n"+
		"    scheme: hmac-sha256\n    signed: \"v0:{timestamp}:{body}\"\n"+
		"    signature: { header: Q-Sig, encoding: hex, prefix: \"v0=\" }\n"+
		"    delivery: { header: Q-Delivery }\n"+
		"    timestamp: { header: Q-Sent, unit: s, window: 5m }\n"+
		"  bodies: repeat\n  normalize: [normalize]\n"))
	if err != nil {
		t.Fatal(err)
	}
	ad := hooks.Declared{D: d}
	now := time.Now()
	body := []byte(`{"type":"event_callback","event_id":"Ev1"}`)
	basestring := func(ts string, b []byte) []byte {
		return []byte("v0:" + ts + ":" + string(b))
	}
	verify := func(ts, sig string) error {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "/hooks/q", strings.NewReader(string(body)))
		req.Header.Set("Q-Sig", "v0="+sig)
		req.Header.Set("Q-Sent", ts)
		req.Header.Set("Q-Delivery", "d-1")
		delivery, err := ad.Verify(req, body, []byte(secret), now)
		if err == nil && delivery != "d-1" {
			t.Fatalf("delivery = %q, want d-1", delivery)
		}
		return err
	}
	ts := fmt.Sprint(now.Unix())
	if err := verify(ts, sign(secret, basestring(ts, body))); err != nil {
		t.Fatalf("signature over the basestring: %v", err)
	}
	// The header's raw string, with its leading zero kept: signing over
	// it verifies, which a re-rendered integer would not.
	padded := "0" + ts
	if err := verify(padded, sign(secret, basestring(padded, body))); err != nil {
		t.Fatalf("signature over the raw timestamp string: %v", err)
	}
	if err := verify(ts, sign(secret, body)); !errors.Is(err, hooks.ErrUnauthorized) {
		t.Fatalf("signature over the body alone: err = %v, want unauthorized", err)
	}
	if err := verify(ts, sign("wrong-secret", basestring(ts, body))); !errors.Is(err, hooks.ErrUnauthorized) {
		t.Fatalf("bad signature: err = %v, want unauthorized", err)
	}
	stale := fmt.Sprint(now.Add(-6 * time.Minute).Unix())
	if err := verify(stale, sign(secret, basestring(stale, body))); !errors.Is(err, hooks.ErrUnauthorized) {
		t.Fatalf("stale timestamp: err = %v, want unauthorized", err)
	}
}

// A hook whose secret is not the key's own bytes: the service hands the
// secret out behind a prefix and writes the key in base64 or hex, and the
// key is what the secret decodes to. A secret not so written verifies
// nothing, and neither does one that decodes to nothing: a signature made
// with an empty key is one anyone can make, and must not pass.
func TestDeclaredVerifyKey(t *testing.T) {
	raw := []byte("0123456789abcdef01234567")
	b64 := base64.StdEncoding.EncodeToString(raw)
	// One byte more than a multiple of three leaves four bits in base64's
	// last character that belong to no byte. A key is read as the sender's
	// libraries read it, which let them be.
	raw25 := append(append([]byte{}, raw...), 'x')
	b64of25 := base64.StdEncoding.EncodeToString(raw25)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	last := len(b64of25) - 3 // before the two "=" of padding
	respelled := b64of25[:last] + string(alphabet[strings.IndexByte(alphabet, b64of25[last])^1]) + "=="
	if same, err := base64.StdEncoding.DecodeString(respelled); err != nil || respelled == b64of25 || string(same) != string(raw25) {
		t.Fatalf("%q is not %q spelled another way: %v", respelled, b64of25, err)
	}
	if _, err := base64.StdEncoding.Strict().DecodeString(respelled); err == nil {
		t.Fatalf("%q is read strictly", respelled)
	}
	const whsec, sk = `{ prefix: "whsec_", encoding: base64 }`, `{ prefix: "sk_", encoding: hex }`
	const notKey, mismatch = "the hook's secret is not a key", "does not match"
	body := []byte(`{}`)
	for _, tc := range []struct {
		name   string
		key    string // verify.key
		secret string // what the hook's secret file holds
		signer []byte // the key the sender signs with
		fails  string // "": verified; else what the refusal says
	}{
		{"base64 behind a prefix", whsec, "whsec_" + b64, raw, ""},
		{"base64 behind no prefix", `{ encoding: base64 }`, b64, raw, ""},
		{"hex behind a prefix", sk, "sk_" + hex.EncodeToString(raw), raw, ""},
		{"base64 key without padding", whsec, "whsec_" + strings.TrimRight(b64of25, "="), raw25, ""},
		{"base64 spelled with spare bits", whsec, "whsec_" + respelled, raw25, ""},
		{"a secret without the prefix", whsec, b64, raw, notKey},
		{"a secret that is no base64", whsec, "whsec_not base64!", raw, notKey},
		{"a secret that is no hex", sk, "sk_xyz", raw, notKey},
		{"a secret that decodes to nothing, signed with an empty key", whsec, "whsec_", []byte{}, notKey},
		{"a secret that decodes to nothing, signed with its own bytes", whsec, "whsec_", []byte("whsec_"), notKey},
		{"the secret's own bytes as the key, though it writes one", whsec, "whsec_" + b64, []byte("whsec_" + b64), mismatch},
	} {
		d, err := source.Parse("q.yaml", []byte("name: q\nhook:\n  verify:\n"+
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n"+
			"    key: "+tc.key+"\n    delivery: { header: Q-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		req, _ := http.NewRequest(http.MethodPost, "/hooks/q", strings.NewReader(string(body)))
		req.Header.Set("Q-Sig", sign(string(tc.signer), body))
		_, err = hooks.Declared{D: d}.Verify(req, body, []byte(tc.secret), time.Now())
		switch {
		case tc.fails == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.fails != "" && (!errors.Is(err, hooks.ErrUnauthorized) || !strings.Contains(err.Error(), tc.fails)):
			t.Errorf("%s: err = %v, want unauthorized, %q", tc.name, err, tc.fails)
		case err != nil && strings.Contains(err.Error(), tc.secret):
			t.Errorf("%s: the refusal names the secret: %v", tc.name, err)
		}
	}
}

// The hash of the HMAC is the declaration's: algorithm names sha256, sha1 or
// sha512, a declaration that says nothing is sha256, and a delivery verifies
// under the hash its declaration names and under no other, whatever prefix
// the sender puts before the signature. The vectors are published ones, so
// the hashes are checked against something other than the code that makes
// them: RFC 2202 test case 2 (HMAC-SHA1) and RFC 4231 test case 2
// (HMAC-SHA-256 and HMAC-SHA-512) sign "what do ya want for nothing?" with
// the key "Jefe", and each digest below was checked here with
//
//	printf '%s' 'what do ya want for nothing?' | openssl dgst -sha1 -hmac Jefe
//
// (and -sha256, -sha512).
func TestDeclaredVerifyAlgorithm(t *testing.T) {
	const (
		jefe, data = "Jefe", "what do ya want for nothing?"
		sha1Sum    = "effcdf6ae5eb2fa2d27416d5f184df9c259a7c79"
		sha256Sum  = "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"
		sha512Sum  = "164b7a7bfcf819e2e395fbe73b56e0a387bd64222e831fd610270cd7ea2505549758bf75c05a994a6d034f65f8f0e6fdcaeab1a34d4a6b4b636e070a38bce737"
	)
	const mismatch, malformed = "does not match", "missing or malformed"
	decl := func(verify string) hooks.Declared {
		t.Helper()
		d, err := source.Parse("q.yaml", []byte("name: q\nhook:\n  verify:\n"+verify+"  bodies: unique\n  normalize: [normalize]\n"))
		if err != nil {
			t.Fatal(err)
		}
		return hooks.Declared{D: d}
	}
	check := func(ad hooks.Declared, header, sig, key, body string) (string, error) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "/hooks/q", strings.NewReader(body))
		req.Header.Set(header, sig)
		return ad.Verify(req, []byte(body), []byte(key), time.Now())
	}
	for _, tc := range []struct {
		name      string
		algorithm string // verify.algorithm, "" to leave it out
		prefix    string // verify.signature.prefix, "" for none
		sig       string // the header, as the sender sends it
		key       string // "": the vector's
		body      string // "": the vector's
		fails     string // "": verified; else what the refusal says
	}{
		{name: "sha256 left out", sig: sha256Sum},
		{name: "sha256", algorithm: "sha256", sig: sha256Sum},
		{name: "sha1", algorithm: "sha1", sig: sha1Sum},
		{name: "sha512", algorithm: "sha512", sig: sha512Sum},
		{name: "sha1 behind its prefix", algorithm: "sha1", prefix: "sha1=", sig: "sha1=" + sha1Sum},
		{name: "sha256, a wrong key", sig: sha256Sum, key: "Jeff", fails: mismatch},
		{name: "sha1, a wrong key", algorithm: "sha1", sig: sha1Sum, key: "Jeff", fails: mismatch},
		{name: "sha512, a wrong key", algorithm: "sha512", sig: sha512Sum, key: "Jeff", fails: mismatch},
		{name: "sha1, a changed body", algorithm: "sha1", sig: sha1Sum, body: data + "?", fails: mismatch},
		{name: "sha512, a changed body", algorithm: "sha512", sig: sha512Sum, body: data + "?", fails: mismatch},
		// Each hash is its own: the right key and body, signed with another.
		{name: "sha256 left out, a sha1 signature", sig: sha1Sum, fails: mismatch},
		{name: "sha256, a sha512 signature", algorithm: "sha256", sig: sha512Sum, fails: mismatch},
		{name: "sha1, a sha256 signature", algorithm: "sha1", sig: sha256Sum, fails: mismatch},
		{name: "sha512, a sha1 signature", algorithm: "sha512", sig: sha1Sum, fails: mismatch},
		// A prefix is text the signature is cut past and picks nothing: a
		// sender who names a weaker hash there is checked with the declared one.
		{name: "sha256 behind sha256=, a sha1 signature behind sha1=", prefix: "sha256=", sig: "sha1=" + sha1Sum, fails: malformed},
		{name: "sha256 behind sha256=, a sha1 signature behind sha256=", prefix: "sha256=", sig: "sha256=" + sha1Sum, fails: mismatch},
		{name: "sha1 behind sha1=, a sha256 signature behind sha1=", algorithm: "sha1", prefix: "sha1=", sig: "sha1=" + sha256Sum, fails: mismatch},
		{name: "sha1 behind sha1=, a sha256 signature behind sha256=", algorithm: "sha1", prefix: "sha1=", sig: "sha256=" + sha256Sum, fails: malformed},
		{name: "sha256 behind sha1=, a sha1 signature behind sha1=", algorithm: "sha256", prefix: "sha1=", sig: "sha1=" + sha1Sum, fails: mismatch},
	} {
		signature := "{ header: Q-Sig, encoding: hex }"
		if tc.prefix != "" {
			signature = "{ header: Q-Sig, encoding: hex, prefix: \"" + tc.prefix + "\" }"
		}
		verify := "    scheme: hmac-sha256\n    signed: body\n    signature: " + signature + "\n    delivery: { header: Q-Delivery }\n"
		if tc.algorithm != "" {
			verify += "    algorithm: " + tc.algorithm + "\n"
		}
		key, body := tc.key, tc.body
		if key == "" {
			key = jefe
		}
		if body == "" {
			body = data
		}
		_, err := check(decl(verify), "Q-Sig", tc.sig, key, body)
		switch {
		case tc.fails == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.fails != "" && (!errors.Is(err, hooks.ErrUnauthorized) || !strings.Contains(err.Error(), tc.fails)):
			t.Errorf("%s: err = %v, want unauthorized, %q", tc.name, err, tc.fails)
		}
	}

	// The reader cannot make a declaration whose hash this yard has no
	// constructor for, so one built by hand verifies nothing: not even a
	// signature that would verify with the default hash.
	ad := decl("    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n")
	ad.D.Hook.Verify.Algorithm = "md5"
	if _, err := check(ad, "Q-Sig", sha256Sum, jefe, data); !errors.Is(err, hooks.ErrUnauthorized) || !strings.Contains(err.Error(), `"md5" is not a hash this yard checks`) {
		t.Errorf("a hash with no constructor: err = %v, want unauthorized, a name the yard does not check", err)
	}

	// Intercom signs its notifications X-Hub-Signature: sha1=<hex>, the
	// HMAC-SHA1 of "the body of the JSON request" under the app's
	// client_secret, and the notification's id is the body's id
	// (developers.intercom.com/docs/references/webhooks/webhook-models,
	// "Signed notifications"). The page publishes no key, so the signature
	// in its sample cannot be checked: this body is a compacted copy of the
	// sample, and the digests are made with
	//
	//	printf '%s' "$body" | openssl dgst -sha1 -hmac ic_client_secret_example
	//
	// (and -sha256, for the signature of another hash).
	const (
		clientSecret = "ic_client_secret_example"
		notification = `{"type":"notification_event","id":"notif_78c122d0-23ba-11e4-9464-79b01267cc2e","topic":"contact.user.created","app_id":"a86dr8yl","data":{"type":"notification_event_data","item":{"type":"user","id":"530370b477ad7120001d","user_id":"25","name":"Joe Example","unsubscribed_from_emails":false,"custom_attributes":{}}}}`
		notifSHA1    = "bfa75cf091345ee52ee9d2076d870a518e0923a1"
		notifSHA256  = "8f35b2689c43add6d39c309a237a90da771aadf388055067d3e1807fb48d35ab"
	)
	bell := decl("    scheme: hmac-sha256\n    algorithm: sha1\n    signed: body\n    signature: { header: X-Hub-Signature, encoding: hex, prefix: \"sha1=\" }\n    delivery: { body: id }\n")
	if id, err := check(bell, "X-Hub-Signature", "sha1="+notifSHA1, clientSecret, notification); err != nil || id != "notif_78c122d0-23ba-11e4-9464-79b01267cc2e" {
		t.Errorf("a signed notification: delivery %q, err %v", id, err)
	}
	for _, tc := range []struct{ name, sig, key, fails string }{
		{"a wrong client secret", "sha1=" + notifSHA1, "another-secret", mismatch},
		{"the HMAC-SHA256 of the body behind sha1=", "sha1=" + notifSHA256, clientSecret, mismatch},
		{"no prefix", notifSHA1, clientSecret, malformed},
	} {
		if _, err := check(bell, "X-Hub-Signature", tc.sig, tc.key, notification); !errors.Is(err, hooks.ErrUnauthorized) || !strings.Contains(err.Error(), tc.fails) {
			t.Errorf("%s: err = %v, want unauthorized, %q", tc.name, err, tc.fails)
		}
	}
}

// Standard Webhooks (standardwebhooks.com) is data under hmac-sha256: the
// delivery id, the timestamp and the body signed together, the signatures
// in base64 behind v1, side by side, and a secret that is whsec_ and a key
// in base64. The vector is the one the specification's reference libraries
// publish in their tests, at the commit
// 7537d2a2d3d52d8f2e0ecd12527af4a9307fd81b of standard-webhooks/
// standard-webhooks: libraries/go/webhook_test.go (TestWebhookSign) and
// libraries/csharp/StandardWebhooks.Tests/WebhookTest.cs give this key,
// id, timestamp and payload that signature, and the Go test's "valid
// multi sig is valid" the other entries. The specification's own example
// delivery shows a signature but no secret, so it is no vector.
func TestDeclaredVerifyStandardWebhooks(t *testing.T) {
	const (
		whsec     = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
		id        = "msg_p5jXN8AQM9LWM0D4loKWxJek"
		stamp     = "1614265330"
		payload   = `{"test": 2432232314}`
		signature = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
		another   = "v1,Ceo5qEr07ixe2NLpvHk3FH9bwy/WavXrAFQ/9tdO6mc="
	)
	d, err := source.Parse("sw.yaml", []byte(`name: sw
hook:
  verify:
    scheme: hmac-sha256
    signed: "{delivery}.{timestamp}.{body}"
    signature: { header: webhook-signature, encoding: base64, list: " ", prefix: "v1," }
    key: { prefix: "whsec_", encoding: base64 }
    delivery: { header: webhook-id }
    timestamp: { header: webhook-timestamp, unit: s, window: 5m }
  bodies: repeat
  normalize: [normalize]
`))
	if err != nil {
		t.Fatal(err)
	}
	ad := hooks.Declared{D: d}
	sent := time.Unix(1614265330, 0)
	// A key of the right form that is not the hook's, and the vector's
	// signature under the version label of a kind this scheme does not check.
	otherKey := "whsec_" + base64.StdEncoding.EncodeToString([]byte("another-key-of-24-bytes"))
	rightButV1a := "v1a," + strings.TrimPrefix(signature, "v1,")
	const mismatch, malformed, late, notKey = "does not match", "missing or malformed", "its timestamp is", "the hook's secret is not a key"
	for _, tc := range []struct {
		name   string
		secret string            // "": the vector's
		now    time.Time         // zero: when it was sent
		set    map[string]string // headers, over the vector's; "-" drops one
		body   string            // "": the vector's
		fails  string            // "": verified; else what the refusal says
	}{
		{name: "the published vector"},
		{name: "a signature among others", set: map[string]string{"webhook-signature": another + " v2," + strings.TrimPrefix(another, "v1,") + " " + signature + " " + another}},
		{name: "the last moment of the window", now: sent.Add(5 * time.Minute)},
		{name: "a wrong key", secret: otherKey, fails: mismatch},
		{name: "a stale timestamp, rightly signed", now: sent.Add(5*time.Minute + time.Second), fails: late},
		{name: "a timestamp from the future, rightly signed", now: sent.Add(-6 * time.Minute), fails: late},
		{name: "a changed id", set: map[string]string{"webhook-id": id + "x"}, fails: mismatch},
		{name: "no id", set: map[string]string{"webhook-id": "-"}, fails: mismatch},
		{name: "a changed timestamp", now: sent.Add(time.Second), set: map[string]string{"webhook-timestamp": "1614265331"}, fails: mismatch},
		{name: "a changed body", body: `{"test": 2432232315}`, fails: mismatch},
		{name: "a list with no v1 match", set: map[string]string{"webhook-signature": another + " " + rightButV1a + " v2," + strings.TrimPrefix(signature, "v1,")}, fails: mismatch},
		{name: "a list of other versions only", set: map[string]string{"webhook-signature": rightButV1a}, fails: malformed},
		{name: "a partial signature", set: map[string]string{"webhook-signature": "v1,"}, fails: malformed},
		{name: "no signature", set: map[string]string{"webhook-signature": "-"}, fails: malformed},
		{name: "a secret without whsec_", secret: strings.TrimPrefix(whsec, "whsec_"), fails: notKey},
		{name: "a secret that is no base64", secret: "whsec_not base64!", fails: notKey},
		{name: "a secret that decodes to nothing", secret: "whsec_", set: map[string]string{"webhook-signature": "v1," + signBase64(nil, id+"."+stamp+"."+payload)}, fails: notKey},
	} {
		headers := map[string]string{"webhook-id": id, "webhook-timestamp": stamp, "webhook-signature": signature}
		for k, v := range tc.set {
			if v == "-" {
				delete(headers, k)
			} else {
				headers[k] = v
			}
		}
		body, secret, now := tc.body, tc.secret, tc.now
		if body == "" {
			body = payload
		}
		if secret == "" {
			secret = whsec
		}
		if now.IsZero() {
			now = sent
		}
		req, _ := http.NewRequest(http.MethodPost, "/hooks/sw", strings.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		delivery, err := ad.Verify(req, []byte(body), []byte(secret), now)
		switch {
		case tc.fails == "" && (err != nil || delivery != id):
			t.Errorf("%s: delivery %q, err %v", tc.name, delivery, err)
		case tc.fails != "" && (!errors.Is(err, hooks.ErrUnauthorized) || !strings.Contains(err.Error(), tc.fails)):
			t.Errorf("%s: err = %v, want unauthorized, %q", tc.name, err, tc.fails)
		}
	}
}

// Standard Webhooks through the receiver, as a service sends it: the
// published vector is recorded with the delivery id its signature covers,
// from a secret file that holds the sender's whsec_ secret as it was
// pasted, newline and all. The same delivery again is dropped; so is
// another body under the same id, validly signed, since the id is what
// tells a retry; a copy refused for another id, because the id is signed;
// and a copy sent after the window is refused though its signature still
// holds. That last is the replay a header timestamp that nothing signs
// would let through.
func TestDeclaredStandardWebhooksDelivery(t *testing.T) {
	r := secretDepot(t, "    scheme: hmac-sha256\n"+
		"    signed: \"{delivery}.{timestamp}.{body}\"\n"+
		"    signature: { header: webhook-signature, encoding: base64, list: \" \", prefix: \"v1,\" }\n"+
		"    key: { prefix: \"whsec_\", encoding: base64 }\n"+
		"    delivery: { header: webhook-id }\n"+
		"    timestamp: { header: webhook-timestamp, unit: s, window: 5m }\n")
	hook, err := r.st.GetHook(context.Background(), "sentry")
	if err != nil {
		t.Fatal(err)
	}
	const whsec = "MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	if err := os.WriteFile(hook.SecretFile, []byte("whsec_"+whsec+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(whsec)
	if err != nil {
		t.Fatal(err)
	}
	const id, stamp = "msg_p5jXN8AQM9LWM0D4loKWxJek", "1614265330"
	sent := time.Unix(1614265330, 0)
	r.now = sent
	headers := func(id, body string) map[string]string {
		return map[string]string{"webhook-id": id, "webhook-timestamp": stamp,
			"webhook-signature": "v1," + signBase64(key, id+"."+stamp+"."+body)}
	}
	vector := `{"test": 2432232314}`
	// The vector's own signature, as the reference libraries publish it: the
	// headers helper makes the same one.
	if got := headers(id, vector)["webhook-signature"]; got != "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=" {
		t.Fatalf("the vector is signed %q", got)
	}
	if code := r.post(t, "/hooks/sentry", []byte(vector), headers(id, vector)); code != http.StatusOK {
		t.Fatalf("the vector: %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 1 || evs[0].Data["delivery"] != id {
		t.Fatalf("events after the vector: %+v", evs)
	}
	other := `{"test": 1}`
	for _, tc := range []struct {
		name string
		body string
		hdr  map[string]string
		want int
	}{
		{"the same delivery again", vector, headers(id, vector), http.StatusOK},
		{"another body under the same id", other, headers(id, other), http.StatusOK},
		{"the vector's signature under another id", vector, map[string]string{"webhook-id": id + "x", "webhook-timestamp": stamp,
			"webhook-signature": headers(id, vector)["webhook-signature"]}, http.StatusUnauthorized},
	} {
		if code := r.post(t, "/hooks/sentry", []byte(tc.body), tc.hdr); code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, code, tc.want)
		}
		if n := len(r.hookEvents(t)); n != 1 {
			t.Errorf("%s: %d hook events, want 1", tc.name, n)
		}
	}
	r.now = sent.Add(5*time.Minute + time.Second)
	if code := r.post(t, "/hooks/sentry", []byte(vector), headers(id, vector)); code != http.StatusUnauthorized {
		t.Errorf("a copy after the window: %d, want %d", code, http.StatusUnauthorized)
	}
}

// challengeDepot is a depot whose receiver serves the declared source
// chal, verified by a signature over v0:{timestamp}:{body} and naming a
// url_verification handshake, with the command printing a fixed envelope.
func challengeDepot(t *testing.T) *depot {
	t.Helper()
	home := t.TempDir()
	r := newDepot(t)
	r.rc = hooks.NewReceiver(r.own, home, nil)
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = r.rc.Handler()
	ctx := context.Background()
	text := "name: chal\nhook:\n  verify:\n" +
		"    scheme: hmac-sha256\n    signed: \"v0:{timestamp}:{body}\"\n" +
		"    signature: { header: Chal-Signature, encoding: hex, prefix: \"v0=\" }\n" +
		"    delivery: { body: event_id }\n" +
		"    timestamp: { header: Chal-Timestamp, unit: s, window: 5m }\n" +
		"  bodies: repeat\n" +
		"  challenge: { body: type, value: url_verification, answer: challenge }\n" +
		"  normalize: [normalize]\n"
	d, err := source.Parse("chal.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	declDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(declDir, "normalize"), []byte(secretNormalize), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "chal.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(home, "chal.secret")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "chal", Source: "chal", SecretFile: secretFile}, "me"); err != nil {
		t.Fatal(err)
	}
	return r
}

// A verified handshake is answered with its challenge value and records
// nothing; an unverified one is refused as any bad signature is, a stale
// one likewise, and a verified body that is no handshake is recorded as
// any other delivery. A source with no challenge key answers the same
// body by recording it.
func TestDeclaredChallengeAnsweredAfterVerify(t *testing.T) {
	r := challengeDepot(t)
	r.now = time.Now()
	ts := fmt.Sprint(r.now.Unix())
	// A handshake names no delivery, as Slack's does not.
	challengeBody := []byte(`{"type":"url_verification","challenge":"tok-123"}`)
	signFor := func(key, stamp string, b []byte) string {
		return "v0=" + sign(key, []byte("v0:"+stamp+":"+string(b)))
	}
	post := func(b []byte, hdr map[string]string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/hooks/chal", strings.NewReader(string(b)))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, strings.TrimSpace(string(out))
	}
	good := map[string]string{"Chal-Signature": signFor(secret, ts, challengeBody), "Chal-Timestamp": ts}
	if code, body := post(challengeBody, good); code != http.StatusOK || body != `{"challenge":"tok-123"}` {
		t.Fatalf("verified challenge: %d %s", code, body)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("a challenge recorded %d hook events", n)
	}
	bodyOnly := map[string]string{"Chal-Signature": "v0=" + sign(secret, challengeBody), "Chal-Timestamp": ts}
	if code, _ := post(challengeBody, bodyOnly); code != http.StatusUnauthorized {
		t.Fatalf("challenge signed over the body alone: %d", code)
	}
	bad := map[string]string{"Chal-Signature": signFor("wrong-secret", ts, challengeBody), "Chal-Timestamp": ts}
	if code, _ := post(challengeBody, bad); code != http.StatusUnauthorized {
		t.Fatalf("challenge with a bad signature: %d", code)
	}
	stale := fmt.Sprint(r.now.Add(-6 * time.Minute).Unix())
	staleHdr := map[string]string{"Chal-Signature": signFor(secret, stale, challengeBody), "Chal-Timestamp": stale}
	if code, _ := post(challengeBody, staleHdr); code != http.StatusUnauthorized {
		t.Fatalf("challenge with a stale timestamp: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("refused challenges recorded %d hook events", n)
	}
	eventBody := []byte(`{"type":"event_callback","event_id":"Ev2"}`)
	eventHdr := map[string]string{"Chal-Signature": signFor(secret, ts, eventBody), "Chal-Timestamp": ts}
	if code, _ := post(eventBody, eventHdr); code != http.StatusOK {
		t.Fatalf("verified non-challenge: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("a verified non-challenge left %d hook events, want 1", n)
	}
	// The source repeats bodies, so a verified delivery that names no
	// event_id has no key to be told from its resend by: it is refused,
	// however often it is sent. The handshake above names none either, and
	// is not refused: it is answered before anything is kept.
	noID := []byte(`{"type":"app_rate_limited"}`)
	noIDHdr := map[string]string{"Chal-Signature": signFor(secret, ts, noID), "Chal-Timestamp": ts}
	for i := 1; i <= 2; i++ {
		if code, _ := post(noID, noIDHdr); code != http.StatusBadRequest {
			t.Fatalf("verified delivery with no id, sent %d times: %d", i, code)
		}
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("a delivery with no id left %d hook events, want the 1 before it", n)
	}
	// No challenge key: the same handshake-shaped body is a delivery.
	plain := secretDepot(t, "    scheme: secret\n    signature: { header: Sentry-Secret }\n")
	if code := plain.post(t, "/hooks/sentry", challengeBody, map[string]string{"Sentry-Secret": secret}); code != http.StatusOK {
		t.Fatalf("challenge-shaped body to a source with no challenge: %d", code)
	}
	if n := len(plain.hookEvents(t)); n != 1 {
		t.Fatalf("a source with no challenge left %d hook events, want 1", n)
	}
}

// With scheme secret the header holds the hook's secret itself, so the
// command is not handed it: the signing secret reaches no command. The
// other headers arrive as they do with a signature.
func TestDeclaredNormalizeNotHandedSecret(t *testing.T) {
	home, declDir := t.TempDir(), t.TempDir()
	seen := filepath.Join(t.TempDir(), "stdin.json")
	script := "#!/bin/sh\ncat > " + seen + "\nprintf '%s' '{\"type\":\"ping\"}'\n"
	if err := os.WriteFile(filepath.Join(declDir, "normalize"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse("q.yaml", []byte("name: q\nhook:\n  verify:\n    scheme: secret\n    signature: { header: q-secret }\n  bodies: unique\n  normalize: [normalize]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	h := http.Header{}
	h.Set("Q-Secret", secret)
	h.Set("Q-Event", "ping")
	if _, err := (hooks.Declared{D: d, Home: home}).Normalize(h, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("the command was handed the secret: %s", raw)
	}
	var in struct {
		Headers map[string][]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Headers["Q-Event"]) != 1 {
		t.Fatalf("the other headers are gone: %s", raw)
	}
	// The request's own headers are left as they came.
	if h.Get("Q-Secret") != secret {
		t.Fatal("Normalize changed the request's headers")
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
		scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
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

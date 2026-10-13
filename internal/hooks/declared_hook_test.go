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
	builtin, ok := hooks.AdapterFor("github", nil, "")
	if !ok || builtin == nil {
		t.Fatal("github has no adapter")
	}
	if _, ok := builtin.(hooks.GitHub); !ok {
		t.Fatalf("github without a declaration is %T, want GitHub", builtin)
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

// The pack's github source wins every lookup when the yard has it:
// the adapter, replying, opening, setting state, forwarding and
// linking. Without it, the Go adapter answers.
func TestDeclaredGithubWins(t *testing.T) {
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
		t.Error("AdapterFor fell back to the GitHub adapter without a hook declaration")
	}
	if _, ok := hooks.ReplierFor("github", d, "", ""); ok {
		t.Error("ReplierFor fell back to the GitHub adapter without thread commands")
	}
	if _, ok := hooks.PullerFor("github", d); ok {
		t.Error("PullerFor fell back to the GitHub adapter without a pull link shape")
	}
	if _, ok := hooks.RepoerFor("github", d); ok {
		t.Error("RepoerFor fell back to the GitHub adapter without a repository pattern")
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
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`)), "Q-Sent": fmt.Sprint(now.Unix())}, `{}`, "", ""},
		{"a stale header timestamp",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`)), "Q-Sent": fmt.Sprint(now.Add(-6 * time.Minute).Unix())}, `{}`, "unauthorized", ""},
		{"a timestamp beyond the largest window",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { header: Q-Sent, unit: s, window: 2562047h47m16.854775807s }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`)), "Q-Sent": "9223372036854775807"}, `{}`, "unauthorized", ""},
		{"a missing header timestamp",
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n    timestamp: { header: Q-Sent, unit: s, window: 5m }\n",
			map[string]string{"Q-Sig": sig("", []byte(`{}`))}, `{}`, "unauthorized", ""},
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

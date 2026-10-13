package hooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

// The pack held to what Linear{} proved: the envelopes its normalize
// prints for the bodies the adapter's tests already have, and what the
// stand-in receives as its thread commands reply, read, open and set
// state, with what each left. Each test below runs the pack's declared
// source (packs/linear) against the stand-in and compares with the golden
// file, which was written from a run the comparison tests (since removed)
// proved equal to Linear{}.
//
// The goldens live under testdata/golden and are compared as JSON, so a
// pack change that alters an envelope or a request fails here with both.

type goldenStep struct {
	Name   string   `json:"name"`
	Calls  []string `json:"calls"`
	Result any      `json:"result"`
}

// linearGolden is one stand-in with the pack's declared source against
// it: the pack's side of the old comparison tests.
type linearGolden struct {
	s   *standin.Server
	dec hooks.Declared
	n   int // calls emitted so far
}

func newLinearGolden(t *testing.T) *linearGolden {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's commands are written in it")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the pack's thread commands call Linear with it")
	}
	fixed := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	s := &standin.Server{LinearKey: "lin_api_test", Now: func() time.Time { return fixed }}
	s.Add(standin.Issue{Source: "linear", ID: "uuid-1", Key: "YRD-1", Title: "Web view", Status: "Todo",
		LinearComments: []standin.LinearComment{
			{ID: "c1", Body: "look?", At: time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC), By: "Ann", ByID: "u-ann"},
			{ID: "c2", Body: "on it", Parent: "c1", At: time.Date(2026, 10, 3, 10, 5, 0, 0, time.UTC), By: "Jane", ByID: "u-jane"},
		}})
	s.Add(standin.Issue{Source: "linear", ID: "uuid-2", Key: "YRD-2", Title: "Links", Status: "Backlog"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	dir := filepath.Join("..", "..", "packs", "linear")
	text, err := os.ReadFile(filepath.Join(dir, "linear.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "linear.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "linear" || d.Thread == nil || d.Link == nil {
		t.Fatalf("declaration = %+v", d)
	}
	d.Base = srv.URL
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return &linearGolden{s: s, dec: hooks.Declared{D: d, Home: home}}
}

// calls says what the stand-in received since the last calls.
func (g *linearGolden) calls() []string {
	out := g.s.Calls()[g.n:]
	g.n = len(g.s.Calls())
	if out == nil {
		out = []string{}
	}
	return out
}

func goldenIssue() hooks.Envelope {
	return hooks.Envelope{Source: "linear", Type: "issue", Action: "create",
		Thread: hooks.Thread{ID: "uuid-1", Title: "Web view", URL: "https://linear.app/acme/issue/YRD-1/web-view"}}
}

// runThreadStory is the pack speaking in a thread: a reply, one under
// --to's conversation, posted and read, an open and its lookup, a state
// set and a read with an OAuth token. The steps share the one stand-in,
// as the comparison test's did.
func runThreadStory(t *testing.T) []goldenStep {
	t.Helper()
	g := newLinearGolden(t)
	ctx := context.Background()
	const cred = "lin_api_test"
	issue := goldenIssue()
	var steps []goldenStep
	emit := func(name string, result any) {
		steps = append(steps, goldenStep{Name: name, Calls: g.calls(), Result: result})
	}

	const replyKey = "9d4c3b2a-1f0e-4d5c-8b7a-6f5e4d3c2b1a"
	m, err := g.dec.Reply(ctx, issue, "", "answer", replyKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("reply", map[string]any{"message": m, "comments": g.s.LinearComments("YRD-1")})

	const toKey = "7b2a9d4c-3f1e-4a5c-9d8b-6f5e4d3c2b1a"
	if _, err := g.dec.Reply(ctx, issue, "c2", "answer", toKey, cred); err != nil {
		t.Fatal(err)
	}
	cs := g.s.LinearComments("YRD-1")
	emit("reply-to", map[string]any{"parent": cs[len(cs)-1].Parent})

	found, err := g.dec.Posted(ctx, issue, replyKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("posted", found)
	if f, err := g.dec.Posted(ctx, issue, "no-such-key", cred); err != nil || f != nil {
		t.Fatalf("posted none: %+v, %v", f, err)
	}
	emit("posted-none", "none")

	ms, err := g.dec.Thread(ctx, issue, cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("read", ms)

	const openKey = "3c2b1a9d-4c3b-4d5c-8b7a-6f5e4d3c2b99"
	th, err := g.dec.Open(ctx, "YRD", "Made", "words", openKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("open", map[string]any{"thread": th, "issue": g.s.Issue("linear", "YRD-3")})
	if _, err := g.dec.Open(ctx, "Nope", "Made", "", "k-nope", cred); err == nil {
		t.Fatal("opened in no team")
	}
	emit("open-no-team", "refused")

	od, err := g.dec.Opened(ctx, "YRD", openKey, cred, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	emit("opened", od)
	if f, err := g.dec.Opened(ctx, "YRD", "no-such-key", cred, time.Now()); err != nil || f != nil {
		t.Fatalf("opened none: %+v, %v", f, err)
	}
	emit("opened-none", "none")

	st, err := g.dec.SetState(ctx, "uuid-1", "started", cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("state", map[string]any{"stated": st, "status": g.s.Status("linear", "YRD-1")})

	g.s.LinearKey = "oauth-token"
	oms, err := g.dec.Thread(ctx, issue, "oauth-token")
	if err != nil {
		t.Fatal(err)
	}
	emit("oauth-read", oms)
	return steps
}

// runReopenStory is SetState moving exactly: a Done issue asked to
// started is reopened, and asked again it is there, moved by no call.
func runReopenStory(t *testing.T) []goldenStep {
	t.Helper()
	g := newLinearGolden(t)
	ctx := context.Background()
	const cred = "lin_api_test"
	g.s.Add(standin.Issue{Source: "linear", ID: "uuid-9", Key: "YRD-9", Title: "Shipped", Status: "Done"})
	g.calls()
	var steps []goldenStep
	st, err := g.dec.SetState(ctx, "uuid-9", "started", cred)
	if err != nil {
		t.Fatal(err)
	}
	steps = append(steps, goldenStep{Name: "reopen", Calls: g.calls(),
		Result: map[string]any{"stated": st, "status": g.s.Status("linear", "YRD-9")}})
	again, err := g.dec.SetState(ctx, "uuid-9", "started", cred)
	if err != nil {
		t.Fatal(err)
	}
	steps = append(steps, goldenStep{Name: "reopen-again", Calls: g.calls(), Result: again})
	return steps
}

// runForwardStory is a stage map's move: Done asked to started stays,
// reported as not moved with no second call, and Todo asked to done
// moves.
func runForwardStory(t *testing.T) []goldenStep {
	t.Helper()
	g := newLinearGolden(t)
	ctx := context.Background()
	const cred = "lin_api_test"
	g.s.Add(standin.Issue{Source: "linear", ID: "uuid-9", Key: "YRD-9", Title: "Shipped", Status: "Done"})
	g.calls()
	var steps []goldenStep
	fw, err := g.dec.Forward(ctx, "uuid-9", "started", cred)
	if err != nil {
		t.Fatal(err)
	}
	steps = append(steps, goldenStep{Name: "forward-stays", Calls: g.calls(),
		Result: map[string]any{"forwarded": fw, "status": g.s.Status("linear", "YRD-9")}})
	fw, err = g.dec.Forward(ctx, "uuid-1", "done", cred)
	if err != nil {
		t.Fatal(err)
	}
	steps = append(steps, goldenStep{Name: "forward-moves", Calls: g.calls(),
		Result: map[string]any{"forwarded": fw, "status": g.s.Status("linear", "YRD-1")}})
	return steps
}

// runReviewStory is a team without a review status using its first
// started status for review.
func runReviewStory(t *testing.T) []goldenStep {
	t.Helper()
	g := newLinearGolden(t)
	old := standin.LinearStates
	standin.LinearStates = []standin.LinearState{
		{ID: "s-todo", Name: "Todo", Type: "unstarted", Position: 0},
		{ID: "s-doing", Name: "In Progress", Type: "started", Position: 1},
		{ID: "s-done", Name: "Done", Type: "completed", Position: 2},
	}
	t.Cleanup(func() { standin.LinearStates = old })
	ctx := context.Background()
	const cred = "lin_api_test"
	g.calls()
	st, err := g.dec.SetState(ctx, "uuid-1", "review", cred)
	if err != nil {
		t.Fatal(err)
	}
	return []goldenStep{{Name: "review-fallback", Calls: g.calls(),
		Result: map[string]any{"stated": st, "status": g.s.Status("linear", "YRD-1")}}}
}

// runAuthStory is the credential on the wire: a personal API key bare,
// anything else as a Bearer token. The core hands the command the caller's own
// credential, and the command sends it as Linear takes it.
func runAuthStory(t *testing.T) []goldenStep {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's thread commands are written in it")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the pack's thread commands call Linear with it")
	}
	s := &standin.Server{LinearKey: "lin_api_test"}
	s.Add(standin.Issue{Source: "linear", ID: "uuid-1", Key: "YRD-1", Title: "Web view", Status: "Todo"})
	var auth []string
	inner := s.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/graphql") {
			auth = append(auth, r.Header.Get("Authorization"))
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := filepath.Join("..", "..", "packs", "linear")
	text, err := os.ReadFile(filepath.Join(dir, "linear.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "linear.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	d.Base = srv.URL
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	dec := hooks.Declared{D: d, Home: home}
	ctx := context.Background()
	issue := hooks.Envelope{Source: "linear", Type: "issue",
		Thread: hooks.Thread{ID: "uuid-1", Title: "Web view", URL: "https://linear.app/acme/issue/YRD-1"}}
	var steps []goldenStep
	n := 0
	for _, c := range []string{"lin_api_test", "oauth-token", "Bearer oauth-token"} {
		s.LinearKey = strings.TrimPrefix(c, "Bearer ")
		if c == "Bearer oauth-token" {
			s.LinearKey = "oauth-token"
		}
		auth = nil
		if _, err := dec.Thread(ctx, issue, c); err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		steps = append(steps, goldenStep{Name: "auth " + c, Calls: s.Calls()[n:], Result: auth})
		n = len(s.Calls())
	}
	return steps
}

func checkGolden(t *testing.T, name string, got any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("golden %s is no JSON: %v", name, err)
	}
	enc, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var have any
	if err := json.Unmarshal(enc, &have); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(have, want) {
		wantRaw, _ := json.MarshalIndent(want, "", "  ")
		haveRaw, _ := json.MarshalIndent(have, "", "  ")
		t.Fatalf("golden %s differs:\n got %s\nwant %s", name, haveRaw, wantRaw)
	}
}

func TestLinearPackThread(t *testing.T) {
	checkGolden(t, "thread.json", runThreadStory(t))
}

func TestLinearPackReopen(t *testing.T) {
	checkGolden(t, "reopen.json", runReopenStory(t))
}

func TestLinearPackForward(t *testing.T) {
	checkGolden(t, "forward.json", runForwardStory(t))
}

func TestLinearPackReviewFallback(t *testing.T) {
	checkGolden(t, "review.json", runReviewStory(t))
}

func TestLinearPackAuthHeader(t *testing.T) {
	checkGolden(t, "auth.json", runAuthStory(t))
}

// linearGoldenBodies are the Linear webhook bodies the adapter's tests
// have: the envelopes the pack's normalize prints for them are the
// goldens.
var linearGoldenBodies = []string{
	"linear_comment_bodydata.json",
	"linear_comment_create.json",
	"linear_comment_plain_mention.json",
	"linear_issue_create.json",
	"linear_issue_delegate_set.json",
	"linear_issue_delegated.json",
	"linear_issue_update.json",
}

func linearGoldenNormalize(t *testing.T) hooks.Declared {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's normalize is written in it")
	}
	dir := filepath.Join("..", "..", "packs", "linear")
	text, err := os.ReadFile(filepath.Join(dir, "linear.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "linear.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "linear" || d.Hook == nil {
		t.Fatalf("declaration = %+v", d)
	}
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return hooks.Declared{D: d, Home: home}
}

func TestLinearPackNormalize(t *testing.T) {
	ad := linearGoldenNormalize(t)
	may := append(append([]string{}, store.WhereKeys...), ad.D.Hook.Keys...)
	for _, name := range linearGoldenBodies {
		body := fixture(t, name)
		got, err := ad.Normalize(http.Header{"Linear-Delivery": {"d-1"}}, body)
		if err != nil {
			t.Fatalf("%s: normalize: %v", name, err)
		}
		checkGolden(t, "normalize-"+name, got)
		for k := range got.Fields {
			found := false
			for _, k2 := range may {
				found = found || k == k2
			}
			if !found {
				t.Errorf("%s: field %q is in neither hook.keys nor store.WhereKeys", name, k)
			}
		}
	}
}

// linearGoldenMentions are bodies that mention in every markdown shape:
// the envelopes pin who the pack's normalize hears.
var linearGoldenMentions = []string{
	"@yardmaster can you look?",
	"testing @yardmaster hello",
	"over to @yardmaster",
	"thanks @yardmaster.",
	"@yardmaster, @jane.doe: and @yardmaster!",
	"(@yardmaster?)\n@ops_bot;",
	"ask @Yardmaster",
	"mail x@yardmaster.com",
	"see https://example.com/@yardmaster/posts",
	"run `yardr ask @yardmaster` or\n```\n@yardmaster\n```",
	"a lone @ sign, @-dash and @.dot",
	"https://linear.app/company/profiles/yardmaster and @yardmaster",
	"https://linear.app/company/profiles/Yardmaster and @yardmaster",
}

func runMentionEnvelopes(t *testing.T) []goldenStep {
	t.Helper()
	ad := linearGoldenNormalize(t)
	var steps []goldenStep
	for _, md := range linearGoldenMentions {
		body, _ := json.Marshal(map[string]any{"type": "Comment", "action": "create", "data": map[string]any{"body": md}})
		e, err := ad.Normalize(nil, body)
		if err != nil {
			t.Fatalf("%q: %v", md, err)
		}
		steps = append(steps, goldenStep{Name: md, Calls: []string{}, Result: e})
	}
	return steps
}

func TestLinearPackMentions(t *testing.T) {
	checkGolden(t, "mentions.json", runMentionEnvelopes(t))
}

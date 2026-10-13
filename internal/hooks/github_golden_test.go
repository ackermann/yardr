package hooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

// The pack held to what GitHub{} proved: the envelopes its normalize
// prints for the bodies the adapter's tests already have, and what the
// stand-in receives as its thread commands reply, read, open and set
// state and as its pr and ci commands read a pull request and its CI
// back, with what each left. Each test below runs the pack's declared
// source (packs/github) against the stand-in and compares with the golden
// file, which was written from a run a comparison test (since removed)
// proved equal to GitHub{}.
//
// The goldens live under testdata/golden and are compared as JSON, so a
// pack change that alters an envelope or a request fails here with both.

// githubGolden is one stand-in with the pack's declared source against
// it: the pack's side of the old comparison tests.
type githubGolden struct {
	s    *standin.Server
	dec  hooks.Declared
	home string
	base string // the stand-in's address, sanitized out of failures
	n    int    // calls emitted so far
}

func newGitHubGolden(t *testing.T) *githubGolden {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's commands are written in it")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the pack's thread commands call GitHub with it")
	}
	fixed := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	s := &standin.Server{GitHubToken: "gh-test", GitHubLogin: "standin"}
	app, err := standin.NewApp("12345", "yardr", filepath.Join(t.TempDir(), "app.pem"), "acme/yardr")
	if err != nil {
		t.Fatal(err)
	}
	app.Now = func() time.Time { return fixed }
	s.App = app
	s.Add(standin.Issue{Source: "github", Key: "acme/yardr#23", Title: "Hook listener logs the secret path", Status: "open",
		Comments: []standin.Comment{
			{ID: 10, By: "yardmaster", Body: "Reproduced.", At: time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)},
		}})
	s.Add(standin.Issue{Source: "github", Key: "acme/yardr#9", Title: "Shipped", Status: "closed"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	dir := filepath.Join("..", "..", "packs", "github")
	text, err := os.ReadFile(filepath.Join(dir, "github.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "github.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "github" || d.Thread == nil || d.Link == nil || d.Events == nil ||
		len(d.Events.PR) == 0 || len(d.Events.CI) == 0 {
		t.Fatalf("declaration = %+v", d)
	}
	d.Base = srv.URL
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return &githubGolden{s: s, dec: hooks.Declared{D: d, Home: home}, home: home, base: srv.URL}
}

// calls says what the stand-in received since the last calls.
func (g *githubGolden) calls() []string {
	out := g.s.Calls()[g.n:]
	g.n = len(g.s.Calls())
	if out == nil {
		out = []string{}
	}
	return out
}

// lookback runs the pack's events command argv (key, e.g. "events.ci") as
// the dispatcher does: in on its stdin, the credential under the variable
// credential.env names, YARDR_SOURCE_BASE from the declaration. A failure
// is its words, with the stand-in's address sanitized out.
func (g *githubGolden) lookback(t *testing.T, key string, argv []string, in map[string]any) any {
	t.Helper()
	full := slices.Clone(argv)
	full[0] = source.Command{Key: key, Argv: argv}.Program(g.home, g.dec.D.Name)
	var out any
	err := source.Run(context.Background(), key+" of source "+g.dec.D.Name, full, in,
		g.dec.D.CommandEnv("gh-test"), g.home, 0, func(b []byte) error {
			return json.Unmarshal(b, &out)
		})
	if err != nil {
		return strings.ReplaceAll(err.Error(), g.base, "http://standin.test")
	}
	return out
}

func goldenGitHubIssue() hooks.Envelope {
	return hooks.Envelope{Source: "github", Type: "issues", Action: "opened",
		Thread: hooks.Thread{ID: "acme/yardr#23", Title: "Hook listener logs the secret path",
			URL: "https://github.com/acme/yardr/issues/23"}}
}

// runGitHubThreadStory is the pack speaking in a thread: a reply, posted
// and read, an open and its lookup, a state set, set again and reopened,
// and a label added twice. The steps share the one stand-in, as the
// comparison test's did.
func runGitHubThreadStory(t *testing.T) []goldenStep {
	t.Helper()
	g := newGitHubGolden(t)
	ctx := context.Background()
	const cred = "gh-test"
	issue := goldenGitHubIssue()
	var steps []goldenStep
	emit := func(name string, result any) {
		steps = append(steps, goldenStep{Name: name, Calls: g.calls(), Result: result})
	}

	const replyKey = "9d4c3b2a-1f0e-4d5c-8b7a-6f5e4d3c2b1a"
	m, err := g.dec.Reply(ctx, issue, "", "answer", replyKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("reply", map[string]any{"message": m, "comments": g.s.Issue("github", "acme/yardr#23").Comments})

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
	th, err := g.dec.Open(ctx, "acme/yardr", "Made", "words", openKey, cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("open", map[string]any{"thread": th, "issue": g.s.Issue("github", th.ID)})
	if _, err := g.dec.Open(ctx, "Nope", "Made", "", "k-nope", cred); err == nil {
		t.Fatal("opened outside owner/repo")
	} else {
		emit("open-refused", err.Error())
	}

	od, err := g.dec.Opened(ctx, "acme/yardr", openKey, cred, time.Date(2026, 10, 11, 11, 55, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	emit("opened", od)
	if f, err := g.dec.Opened(ctx, "acme/yardr", "no-such-key", cred, time.Now()); err != nil || f != nil {
		t.Fatalf("opened none: %+v, %v", f, err)
	}
	emit("opened-none", "none")

	st, err := g.dec.SetState(ctx, "acme/yardr#23", "done", cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("state", map[string]any{"stated": st, "status": g.s.Status("github", "acme/yardr#23")})

	again, err := g.dec.SetState(ctx, "acme/yardr#23", "done", cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("state-again", again)

	re, err := g.dec.SetState(ctx, "acme/yardr#23", "started", cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("reopen", map[string]any{"stated": re, "status": g.s.Status("github", "acme/yardr#23")})

	lb, err := g.dec.SetState(ctx, "acme/yardr#23", "label:in-review", cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("label", map[string]any{"stated": lb, "labels": g.s.Labels("github", "acme/yardr#23")})

	lbAgain, err := g.dec.SetState(ctx, "acme/yardr#23", "label:in-review", cred)
	if err != nil {
		t.Fatal(err)
	}
	emit("label-again", lbAgain)
	return steps
}

// runGitHubForwardStory is a stage map's move: a closed issue asked to
// started stays, reported as not moved with no second call, and an open
// one asked to done moves.
func runGitHubForwardStory(t *testing.T) []goldenStep {
	t.Helper()
	g := newGitHubGolden(t)
	ctx := context.Background()
	const cred = "gh-test"
	var steps []goldenStep
	fw, err := g.dec.Forward(ctx, "acme/yardr#9", "started", cred)
	if err != nil {
		t.Fatal(err)
	}
	steps = append(steps, goldenStep{Name: "forward-stays", Calls: g.calls(),
		Result: map[string]any{"forwarded": fw, "status": g.s.Status("github", "acme/yardr#9")}})
	fw, err = g.dec.Forward(ctx, "acme/yardr#23", "done", cred)
	if err != nil {
		t.Fatal(err)
	}
	steps = append(steps, goldenStep{Name: "forward-moves", Calls: g.calls(),
		Result: map[string]any{"forwarded": fw, "status": g.s.Status("github", "acme/yardr#23")}})
	return steps
}

// The pull requests the pr and ci stories read: one green, one red, one
// bare of required checks and runs alike.
const (
	githubGreenSHA = "4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"
	githubRedSHA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	githubBareSHA  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func seedGitHubPulls(g *githubGolden) {
	g.s.Add(standin.Issue{Source: "github", Key: "acme/yardr#17", Title: "PR flow", Status: "open", Pull: true,
		Head: githubGreenSHA, Base: "main", Required: []string{"build"}, Rules: []string{"lint"},
		Runs: []standin.CheckRun{
			{Name: "build", Status: "completed", Conclusion: "success", URL: "https://ci.test/runs/5"},
			{Name: "lint", Status: "completed", Conclusion: "success", URL: "https://ci.test/runs/6"},
		},
		Statuses: []standin.CommitStatus{{Context: "deploy", State: "pending"}}})
	g.s.Add(standin.Issue{Source: "github", Key: "acme/yardr#18", Title: "Red", Status: "open", Pull: true,
		Head: githubRedSHA, Base: "side", Required: []string{"build"},
		Runs: []standin.CheckRun{
			{Name: "build", Status: "completed", Conclusion: "failure", URL: "https://ci.test/runs/7"},
		}})
	g.s.Add(standin.Issue{Source: "github", Key: "acme/yardr#19", Title: "Bare", Status: "open", Pull: true,
		Head: githubBareSHA, Base: "next"})
	g.calls()
}

// runGitHubPRStory is the pack reading a pull request back: by repository
// and number, by URL, and one there is none of.
func runGitHubPRStory(t *testing.T) []goldenStep {
	t.Helper()
	g := newGitHubGolden(t)
	seedGitHubPulls(g)
	ev := g.dec.D.Events
	var steps []goldenStep
	read := func(name string, in map[string]any) {
		out := g.lookback(t, "events.pr", ev.PR, in)
		steps = append(steps, goldenStep{Name: name, Calls: g.calls(), Result: out})
	}
	read("pr", map[string]any{"repo": "acme/yardr", "number": "17", "url": ""})
	read("pr-url", map[string]any{"repo": "", "number": "", "url": "https://github.com/acme/yardr/pull/17"})
	read("pr-none", map[string]any{"repo": "acme/yardr", "number": "21", "url": ""})
	return steps
}

// runGitHubCIStory is the pack reading CI state: green, red, and bare.
func runGitHubCIStory(t *testing.T) []goldenStep {
	t.Helper()
	g := newGitHubGolden(t)
	seedGitHubPulls(g)
	ev := g.dec.D.Events
	var steps []goldenStep
	read := func(name, number, sha string) {
		out := g.lookback(t, "events.ci", ev.CI,
			map[string]any{"repo": "acme/yardr", "number": number, "sha": sha})
		steps = append(steps, goldenStep{Name: name, Calls: g.calls(), Result: out})
	}
	read("ci-green", "17", githubGreenSHA)
	read("ci-red", "18", githubRedSHA)
	read("ci-bare", "19", githubBareSHA)
	return steps
}

func TestGitHubPackThread(t *testing.T) {
	checkGolden(t, "github-thread.json", runGitHubThreadStory(t))
}

func TestGitHubPackForward(t *testing.T) {
	checkGolden(t, "github-forward.json", runGitHubForwardStory(t))
}

func TestGitHubPackPR(t *testing.T) {
	checkGolden(t, "github-pr.json", runGitHubPRStory(t))
}

func TestGitHubPackCI(t *testing.T) {
	checkGolden(t, "github-ci.json", runGitHubCIStory(t))
}

// githubGoldenBodies are the GitHub webhook bodies the adapter's tests
// have: the envelopes the pack's normalize prints for them are the
// goldens.
var githubGoldenBodies = []string{
	"check_suite_cancelled.json",
	"issue_comment_issue.json",
	"issue_comment_pr.json",
	"issues_closed.json",
	"issues_edited.json",
	"issues_labeled.json",
	"issues_opened.json",
	"ping.json",
	"pull_request_closed_merged.json",
	"pull_request_closed_unmerged.json",
	"pull_request_opened.json",
	"pull_request_ready_for_review.json",
	"pull_request_reopened.json",
	"pull_request_review_approved.json",
	"pull_request_review_changes_requested.json",
	"pull_request_review_commented.json",
	"pull_request_synchronize.json",
	"push.json",
	"workflow_run_failure.json",
	"workflow_run_success.json",
}

// githubGoldenEvent is the X-GitHub-Event a recorded body was delivered
// with, from its file's name.
func githubGoldenEvent(name string) string {
	for _, e := range []string{"pull_request_review", "pull_request", "issue_comment", "issues", "workflow_run", "check_suite", "push", "ping"} {
		if name == e || len(name) > len(e) && name[len(e)] == '_' && name[:len(e)] == e {
			return e
		}
	}
	return ""
}

func githubGoldenNormalize(t *testing.T) hooks.Declared {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's normalize is written in it")
	}
	dir := filepath.Join("..", "..", "packs", "github")
	text, err := os.ReadFile(filepath.Join(dir, "github.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "github.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "github" || d.Hook == nil {
		t.Fatalf("declaration = %+v", d)
	}
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return hooks.Declared{D: d, Home: home}
}

func TestGitHubPackNormalize(t *testing.T) {
	ad := githubGoldenNormalize(t)
	may := append(append([]string{}, store.WhereKeys...), ad.D.Hook.Keys...)
	for _, name := range githubGoldenBodies {
		event := githubGoldenEvent(strings.TrimSuffix(name, ".json"))
		if event == "" {
			t.Fatalf("%s: no event in its name", name)
		}
		body := fixture(t, "github/"+name)
		got, err := ad.Normalize(http.Header{"X-Github-Event": {event}}, body)
		if err != nil {
			t.Fatalf("%s: normalize: %v", name, err)
		}
		checkGolden(t, "github-normalize-"+name, got)
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

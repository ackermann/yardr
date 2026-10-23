package hooks

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
)

// The pack's link fixes a GitHub URL as the Go adapter did: an issue has
// one fixed form however its URL was copied, owner and repo folded, and
// whatever follows the number left out; a pull request is a thread a bead
// can track too, under its own URL. What is no issue of the source is
// refused, and a host no source has issues on names where yardr knows
// them. Without the pack's declaration, github.com is such a host.
func TestGitHubPackLinkFixedForm(t *testing.T) {
	gh := githubPackDecl(t)
	const fixed = "https://github.com/acme/webapp/issues/7"
	for _, c := range []struct{ raw, want string }{
		{"https://github.com/acme/webapp/issues/7", fixed},
		{"https://github.com/acme/webapp/issues/7/", fixed},
		{"https://github.com/acme/webapp/issues/7/events?page=2", fixed},
		{"https://github.com/acme/webapp/issues/7?notification_referrer_id=x", fixed},
		{"https://github.com/acme/webapp/issues/7#issuecomment-99", fixed},
		{"https://GitHub.com/Acme/WebApp/issues/7", fixed},
		{"https://github.com/Acme/webapp/pull/7", "https://github.com/acme/webapp/pull/7"},
		{"https://github.com/acme/webapp/pull/7/files", "https://github.com/acme/webapp/pull/7"},
	} {
		source, got, err := Issue(c.raw, gh)
		if err != nil || source != "github" || got != c.want {
			t.Errorf("Issue(%q) = %q, %q, %v; want github, %q", c.raw, source, got, err, c.want)
		}
		if in, err := IssueIn("github", c.raw, gh); err != nil || in != c.want {
			t.Errorf("IssueIn(%q) = %q, %v; want %q", c.raw, in, err, c.want)
		}
	}
	for _, c := range []struct{ raw, why string }{
		{"https://github.com/acme/webapp/pull/0", "is no github issue or pull request"},
		{"https://github.com/acme/webapp/pulls", "is no github issue or pull request"},
		{"https://github.com/acme/webapp", "is no github issue or pull request"},
		{"https://github.com/acme/webapp/issues", "is no github issue or pull request"},
		{"https://github.com/acme/webapp/issues/0", "is no github issue or pull request"},
		{"https://github.com/acme/webapp/issues/new", "is no github issue or pull request"},
		{"https://github.com/acme/webapp/discussions/7", "is no github issue or pull request"},
		{"https://gitlab.com/acme/webapp/-/issues/7", "is on gitlab.com, where yardr knows no issues (it does on github.com)"},
		{"https://jira.example/browse/YRD-12", "where yardr knows no issues"},
		{"YRD-12", "is not the URL of an issue"},
		{"github.com/acme/webapp/issues/7", "is not the URL of an issue"},
		{"ftp://github.com/acme/webapp/issues/7", "is not the URL of an issue"},
		{"", "is not the URL of an issue"},
	} {
		if source, got, err := Issue(c.raw, gh); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("Issue(%q) = %q, %q, %v; want an error with %q", c.raw, source, got, err, c.why)
		}
	}
	if _, _, err := Issue("https://github.com/acme/webapp/issues/7"); err == nil ||
		!strings.Contains(err.Error(), "where yardr knows no issues (it does on none of the yard's sources)") {
		t.Errorf("no declaration: %v", err)
	}
	// The shapes name each other, and the number reads back out.
	if got := ThreadAlias("github", gh, fixed); got != "https://github.com/acme/webapp/pull/7" {
		t.Errorf("ThreadAlias(issue) = %q", got)
	}
	if got := ThreadAlias("github", gh, "https://github.com/acme/webapp/pull/7"); got != fixed {
		t.Errorf("ThreadAlias(pull) = %q", got)
	}
	if repo, number := PullParts("github", gh, "https://github.com/acme/webapp/pull/7"); repo != "acme/webapp" || number != "7" {
		t.Errorf("PullParts(pull) = %q, %q", repo, number)
	}
	if repo, number := PullParts("github", gh, fixed); repo != "" || number != "" {
		t.Errorf("PullParts(issue) = %q, %q, want none", repo, number)
	}
	if thread, err := (Declared{D: gh}).IssueThread(fixed); err != nil || thread != "acme/webapp#7" {
		t.Errorf("IssueThread(%q) = %q, %v; want acme/webapp#7", fixed, thread, err)
	}
	// A thread is an issue or pull request when the link shape reads its
	// URL. One with no URL, and a source with no link shape to read it by,
	// are taken to be: nothing says they are not.
	noShape := *gh
	noShape.Link = nil
	for _, c := range []struct {
		d    *source.Declaration
		raw  string
		want bool
	}{
		{gh, fixed, true},
		{gh, "https://github.com/acme/webapp/pull/7", true},
		{gh, "https://github.com/acme/webapp/pull/7/files", true},
		{gh, "https://github.com/acme/webapp/tree/main", false},
		{gh, "https://github.com/acme/webapp/tree/yard/yard-sv6b", false},
		{gh, "https://github.com/acme/webapp", false},
		{gh, "", true},
		{&noShape, "https://github.com/acme/webapp/tree/main", true},
		{nil, "https://github.com/acme/webapp/tree/main", true},
	} {
		if got := NamesIssue("github", c.d, c.raw); got != c.want {
			t.Errorf("NamesIssue(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// githubPackDecl is the GitHub source as packs/github declares it.
func githubPackDecl(t *testing.T) *source.Declaration {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "packs", "github", "github.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join("..", "..", "packs", "github", "github.yaml"), raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "github" || d.Link == nil || d.Link.Pull == nil {
		t.Fatalf("declaration = %+v", d)
	}
	return d
}

// A declared link with fold lowercases the groups before the fixed and
// thread templates expand: one spelling names the issue however it was
// typed.
func TestDeclaredLinkFold(t *testing.T) {
	text := "name: acme\n" +
		"link:\n  host: acme.test\n  issue: '^/([^/]+)/([^/]+)/issues/([0-9]+)'\n" +
		"  fixed: \"https://acme.test/{1}/{2}/issues/{3}\"\n  thread: \"{1}/{2}#{3}\"\n  fold: lower\n" +
		"hook:\n  verify:\n    scheme: hmac-sha256\n    signed: body\n" +
		"    signature: { header: Acme-Signature, encoding: hex }\n    delivery: { header: Acme-Delivery }\n" +
		"  bodies: repeat\n  normalize: [normalize]\n"
	d, err := source.Parse("acme.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	const raw = "https://acme.test/Acme/WebApp/issues/7"
	const want = "https://acme.test/acme/webapp/issues/7"
	if name, fixed, err := Issue(raw, d); err != nil || name != "acme" || fixed != want {
		t.Errorf("Issue(%q) = %q, %q, %v; want acme, %q", raw, name, fixed, err, want)
	}
	if fixed, err := IssueIn("acme", raw, d); err != nil || fixed != want {
		t.Errorf("IssueIn(%q) = %q, %v; want %q", raw, fixed, err, want)
	}
	if thread, err := (Declared{D: d}).IssueThread(raw); err != nil || thread != "acme/webapp#7" {
		t.Errorf("IssueThread(%q) = %q, %v; want acme/webapp#7", raw, thread, err)
	}
}

// A declared source with a pull shape has pull requests: PullerFor finds
// the declaration, PullOf reads a pull request's thread, PullURL builds
// its URL from the parts, ThreadAlias names the other shape's form, and
// PullParts reads the repository and number back out. A link is made
// from either shape, and a thread named from either fixed form. Without
// a pull shape the source has none of them.
func TestDeclaredPullShapes(t *testing.T) {
	text := "name: acme\nbase: https://acme.test\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" +
		"link:\n  host: acme.test\n  issue: '^/([^/]+)/([^/]+)/issues/([0-9]+)'\n" +
		"  fixed: \"https://acme.test/{1}/{2}/issues/{3}\"\n  thread: \"{1}/{2}#{3}\"\n  fold: lower\n" +
		"  pull:\n    issue: '^/([^/]+)/([^/]+)/pull/([0-9]+)'\n" +
		"    fixed: \"https://acme.test/{1}/{2}/pull/{3}\"\n    thread: \"{1}/{2}#{3}\"\n"
	d, err := source.Parse("acme.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	one, err := source.Parse("one.yaml", []byte(
		"name: one\nbase: https://one.test\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"+
			"link:\n  host: one.test\n  issue: '^/issue/([A-Z]+-[0-9]+)'\n  fixed: \"https://one.test/issue/{1}\"\n  thread: \"{1}\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := PullerFor("acme", d)
	if !ok {
		t.Fatal("declared acme has no Puller")
	}
	if _, is := p.(Declared); !is {
		t.Fatalf("PullerFor(acme) is %T, want Declared", p)
	}
	if _, ok := PullerFor("acme", one); ok {
		t.Fatal("acme with another declaration has a Puller")
	}
	if _, ok := PullerFor("one", one); ok {
		t.Fatal("a source with one shape has a Puller")
	}
	if _, ok := PullerFor("acme", nil); ok {
		t.Fatal("acme without a declaration has a Puller")
	}
	if _, ok := PullerFor("github", nil); ok {
		t.Fatal("github without a declaration has a Puller")
	}
	const (
		issue = "https://acme.test/acme/webapp/issues/7"
		pull  = "https://acme.test/acme/webapp/pull/7"
	)
	if got := p.PullOf("https://acme.test/Acme/WebApp/pull/7"); got != "acme/webapp#7" {
		t.Errorf("PullOf(pull) = %q, want acme/webapp#7", got)
	}
	for _, raw := range []string{issue, "https://acme.test/acme/webapp/discussions/7", "https://one.test/acme/webapp/pull/7", "no url"} {
		if got := p.PullOf(raw); got != "" {
			t.Errorf("PullOf(%q) = %q, want none", raw, got)
		}
	}
	if got := p.PullURL("acme/webapp", "7"); got != pull {
		t.Errorf("PullURL = %q, want %q", got, pull)
	}
	if got := p.PullURL("Acme/WebApp", "7"); got != pull {
		t.Errorf("PullURL folds = %q, want %q", got, pull)
	}
	if got := p.PullURL("acme/webapp/extra", "7"); got != "" {
		t.Errorf("PullURL of parts the pattern has no groups for = %q", got)
	}
	if got := ThreadAlias("acme", d, issue); got != pull {
		t.Errorf("ThreadAlias(issue) = %q, want %q", got, pull)
	}
	if got := ThreadAlias("acme", d, "https://acme.test/Acme/WebApp/pull/7"); got != issue {
		t.Errorf("ThreadAlias(pull) = %q, want %q", got, issue)
	}
	for _, raw := range []string{"https://acme.test/acme/webapp/discussions/7", "https://github.com/acme/webapp/pull/7", ""} {
		if got := ThreadAlias("acme", d, raw); got != "" {
			t.Errorf("ThreadAlias(%q) = %q, want none", raw, got)
		}
	}
	if got := ThreadAlias("one", one, "https://one.test/issue/X-1"); got != "" {
		t.Errorf("one shape aliases %q", got)
	}
	if repo, number := PullParts("acme", d, pull); repo != "acme/webapp" || number != "7" {
		t.Errorf("PullParts(pull) = %q, %q", repo, number)
	}
	if repo, number := PullParts("acme", d, issue); repo != "" || number != "" {
		t.Errorf("PullParts(issue) = %q, %q, want none", repo, number)
	}
	for _, raw := range []string{pull, pull + "/files", "https://acme.test/Acme/WebApp/pull/7/"} {
		name, fixed, err := Issue(raw, d)
		if err != nil || name != "acme" || fixed != pull {
			t.Errorf("Issue(%q) = %q, %q, %v; want acme, %q", raw, name, fixed, err, pull)
		}
		thread, err := (Declared{D: d}).IssueThread(fixed)
		if err != nil || thread != "acme/webapp#7" {
			t.Errorf("IssueThread(%q) = %q, %v; want acme/webapp#7", fixed, thread, err)
		}
	}
	if _, _, err := Issue("https://acme.test/acme/webapp/discussions/7", d); err == nil ||
		!strings.Contains(err.Error(), "is no acme issue or pull request") {
		t.Errorf("a discussion: %v", err)
	}
	if got := ThreadAlias("token", nil, "https://github.com/acme/webapp/pull/7"); got != "" {
		t.Errorf("a source with no pull requests aliases %q", got)
	}
}

// linearPackDecl is the Linear source as packs/linear declares it.
func linearPackDecl(t *testing.T) *source.Declaration {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "packs", "linear", "linear.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join("..", "..", "packs", "linear", "linear.yaml"), raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "linear" || d.Link == nil {
		t.Fatalf("declaration = %+v", d)
	}
	return d
}

// The pack's link fixes a Linear URL as the Go adapter did: the title
// slug Linear appends is left out, with whatever follows the issue. One
// deliberate difference: the adapter read the workspace and the key in
// any case, and a link cannot (its template fills in groups, it does not
// fold their case), so a URL not in Linear's own case is refused.
func TestLinearPackLinkFixedForm(t *testing.T) {
	lin := linearPackDecl(t)
	const fixed = "https://linear.app/acme/issue/YRD-12"
	for _, raw := range []string{
		"https://linear.app/acme/issue/YRD-12",
		"https://linear.app/acme/issue/YRD-12/web-view-shows-links",
		"https://linear.app/acme/issue/YRD-12/a-new-title",
		"https://linear.app/acme/issue/YRD-12/web-view/extra?x=1",
		"https://linear.app/acme/issue/YRD-12?focus=1",
		"https://linear.app/acme/issue/YRD-12/web-view#comment-4f1c",
		" https://linear.app/acme/issue/YRD-12\n",
	} {
		name, got, err := Issue(raw, lin)
		if err != nil || name != "linear" || got != fixed {
			t.Errorf("Issue(%q) = %q, %q, %v; want linear, %q", raw, name, got, err, fixed)
		}
		if in, err := IssueIn("linear", raw, lin); err != nil || in != fixed {
			t.Errorf("IssueIn(%q) = %q, %v; want %q", raw, in, err, fixed)
		}
	}
	for _, c := range []struct{ raw, why string }{
		{"https://linear.app/acme/project/web-view-1a2b", "is no linear issue"},
		{"https://linear.app/acme/issue/", "is no linear issue"},
		{"https://linear.app/acme/issue/12", "is no linear issue"},
		{"https://linear.app/acme/profiles/jane", "is no linear issue"},
		{"http://LINEAR.app/Acme/issue/yrd-12/", "is no linear issue"},
		{"https://github.com/acme/webapp/issues/7", "is on github.com, not on linear.app: no linear issue"},
	} {
		if got, err := IssueIn("linear", c.raw, lin); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("IssueIn(%q) = %q, %v; want an error with %q", c.raw, got, err, c.why)
		}
	}
	if _, _, err := Issue("https://linear.app.evil.example/acme/issue/YRD-12", lin); err == nil ||
		!strings.Contains(err.Error(), "where yardr knows no issues") {
		t.Errorf("evil host: %v", err)
	}
	// Without the pack's declaration, linear.app is a host yardr knows no
	// issues on.
	if _, _, err := Issue("https://linear.app/acme/issue/YRD-12"); err == nil ||
		!strings.Contains(err.Error(), "where yardr knows no issues (it does on none of the yard's sources)") {
		t.Errorf("no declaration: %v", err)
	}
}

// When two declared sources share a host, the yard's hooks decide: a
// declared source with a hook wins over one with none, else the first as
// given. A URL one takes and the other refuses is still that one's.
func TestIssueWithHooks(t *testing.T) {
	decl := &source.Declaration{Name: "gh-declared", Link: &source.Link{
		Host: "github.com", Issue: `^/([^/]+)/([^/]+)/issues/([0-9]+)`,
		Fixed: "https://github.com/{1}/{2}/issues/{3}", Thread: "{1}/{2}#{3}"}}
	decls := []*source.Declaration{decl, githubPackDecl(t)}
	const url = "https://github.com/acme/webapp/issues/7"
	for _, c := range []struct {
		name   string
		hooked map[string]bool
		want   string
	}{
		{"no hooks", nil, "gh-declared"},
		{"declared hooked", map[string]bool{"gh-declared": true}, "gh-declared"},
		{"both hooked", map[string]bool{"github": true, "gh-declared": true}, "gh-declared"},
		{"github hooked", map[string]bool{"github": true}, "github"},
		{"another hooked", map[string]bool{"token": true}, "gh-declared"},
	} {
		name, fixed, err := IssueWithHooks(url, decls, c.hooked)
		if err != nil || name != c.want || fixed != url {
			t.Errorf("%s: = %q, %q, %v; want %q, %q", c.name, name, fixed, err, c.want, url)
		}
	}
	// A pull request: github takes it, the declared one refuses it;
	// the link is still made, as github's.
	if name, fixed, err := IssueWithHooks("https://github.com/acme/webapp/pull/7", decls, map[string]bool{"gh-declared": true}); err != nil || name != "github" || fixed != "https://github.com/acme/webapp/pull/7" {
		t.Errorf("pull request: %q, %q, %v", name, fixed, err)
	}
	// No issue of either: the error names the preferred source.
	if _, _, err := IssueWithHooks("https://github.com/acme/webapp/discussions/7", decls, map[string]bool{"gh-declared": true}); err == nil || !strings.Contains(err.Error(), "is no gh-declared issue") {
		t.Errorf("discussions with declared hooked: %v", err)
	}
	if _, _, err := IssueWithHooks("https://github.com/acme/webapp/discussions/7", decls, nil); err == nil || !strings.Contains(err.Error(), "is no gh-declared issue") {
		t.Errorf("discussions without hooks: %v", err)
	}
}

// A URL whose source is known (a delivery's thread) is an issue of that
// source or nothing: a source with no issues (token) links none, and one
// the yard does not declare is unknown, with what the yard can link
// named, if anything.
func TestIssueInItsSource(t *testing.T) {
	for _, c := range []struct{ source, raw, why string }{
		{"token", "https://github.com/acme/webapp/issues/7", "a bead cannot link to an issue in token (it can in none of the yard's sources)"},
		{"github", "https://github.com/acme/webapp/issues/7", "a bead cannot link to an issue in github (it can in none of the yard's sources)"},
		{"", "https://github.com/acme/webapp/issues/7", "a bead cannot link to an issue in an unknown source"},
	} {
		if got, err := IssueIn(c.source, c.raw); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("IssueIn(%q, %q) = %q, %v; want an error with %q", c.source, c.raw, got, err, c.why)
		}
	}
	if got := LinkSources(); len(got) != 0 {
		t.Errorf("LinkSources() = %v, want none: no builtin links", got)
	}
	if got := LinkSources(githubPackDecl(t)); len(got) != 1 || got[0] != "github" {
		t.Errorf("LinkSources(github) = %v", got)
	}
}

// The thread URL a delivery carries is that of its issue in the fixed form
// too, so a link made from the URL and one made from a delivery are equal.
func TestDeliveryThreadIsItsIssue(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's normalize is written in it")
	}
	raw, err := os.ReadFile("testdata/github/issues_opened.json")
	if err != nil {
		t.Fatal(err)
	}
	gh := githubPackDecl(t)
	home := t.TempDir()
	dir := filepath.Join("..", "..", "packs", "github")
	if err := gh.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", gh.Name))
	env, err := (Declared{D: gh, Home: home}).Normalize(http.Header{"X-Github-Event": {"issues"}}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := IssueIn(env.Source, env.Thread.URL+"#issuecomment-99", gh); err != nil || got != env.Thread.URL {
		t.Fatalf("github thread %q: %q, %v", env.Thread.URL, got, err)
	}
	if !NamesIssue(env.Source, gh, env.Thread.URL) {
		t.Errorf("the thread of an issue, %q, is no issue", env.Thread.URL)
	}

	// The thread of a run for no pull request is its branch's, at a URL the
	// link shape does not read: no issue, so nothing is said or set in it.
	// A run for a pull request has its number alone, and is one.
	for _, c := range []struct {
		body       string
		thread     string
		namesIssue bool
	}{
		{"workflow_run_failure", "acme/yardr@yard/yard-sv6b", false},
		{"check_suite_main", "acme/yardr@main", false},
		{"workflow_run_success", "acme/yardr#17", true},
	} {
		raw, err := os.ReadFile("testdata/github/" + c.body + ".json")
		if err != nil {
			t.Fatal(err)
		}
		event := strings.Join(strings.Split(c.body, "_")[:2], "_")
		run, err := (Declared{D: gh, Home: home}).Normalize(http.Header{"X-Github-Event": {event}}, raw)
		if err != nil {
			t.Fatal(err)
		}
		if run.Thread.ID != c.thread || NamesIssue(run.Source, gh, run.Thread.URL) != c.namesIssue {
			t.Errorf("%s: thread %+v, NamesIssue %v; want %q, %v", c.body, run.Thread, !c.namesIssue, c.thread, c.namesIssue)
		}
	}
}

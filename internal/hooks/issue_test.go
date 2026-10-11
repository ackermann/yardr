package hooks

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/source"
)

// An issue has one fixed form however its URL was copied: both lose
// whatever follows the issue. What is no issue of the source is refused.
func TestIssueFixedForm(t *testing.T) {
	const gh = "https://github.com/acme/webapp/issues/7"
	for _, c := range []struct{ raw, source, want string }{
		{"https://github.com/acme/webapp/issues/7", "github", gh},
		{"https://github.com/acme/webapp/issues/7/", "github", gh},
		{"https://github.com/acme/webapp/issues/7/events?page=2", "github", gh},
		{"https://github.com/acme/webapp/issues/7?notification_referrer_id=x", "github", gh},
		{"https://github.com/acme/webapp/issues/7#issuecomment-99", "github", gh},
		{"https://GitHub.com/Acme/WebApp/issues/7", "github", gh},
		// A pull request is a thread a bead can track too, under its own URL.
		{"https://github.com/Acme/webapp/pull/7", "github", "https://github.com/acme/webapp/pull/7"},
		{"https://github.com/acme/webapp/pull/7/files", "github", "https://github.com/acme/webapp/pull/7"},
	} {
		source, got, err := Issue(c.raw)
		if err != nil || source != c.source || got != c.want {
			t.Errorf("Issue(%q) = %q, %q, %v; want %q, %q", c.raw, source, got, err, c.source, c.want)
		}
		if in, err := IssueIn(c.source, c.raw); err != nil || in != c.want {
			t.Errorf("IssueIn(%q, %q) = %q, %v; want %q", c.source, c.raw, in, err, c.want)
		}
	}
	for _, c := range []struct{ raw, why string }{
		{"https://github.com/acme/webapp/pull/0", "is no GitHub issue"},
		{"https://github.com/acme/webapp/pulls", "is no GitHub issue"},
		{"https://github.com/acme/webapp", "is no GitHub issue"},
		{"https://github.com/acme/webapp/issues", "is no GitHub issue"},
		{"https://github.com/acme/webapp/issues/0", "is no GitHub issue"},
		{"https://github.com/acme/webapp/issues/new", "is no GitHub issue"},
		{"https://github.com/acme/webapp/discussions/7", "is no GitHub issue"},
		{"https://gitlab.com/acme/webapp/-/issues/7", "is on gitlab.com, where yardr knows no issues (it does on github.com)"},
		{"https://jira.example/browse/YRD-12", "where yardr knows no issues"},
		{"YRD-12", "is not the URL of an issue"},
		{"github.com/acme/webapp/issues/7", "is not the URL of an issue"},
		{"ftp://github.com/acme/webapp/issues/7", "is not the URL of an issue"},
		{"", "is not the URL of an issue"},
	} {
		if source, got, err := Issue(c.raw); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("Issue(%q) = %q, %q, %v; want an error with %q", c.raw, source, got, err, c.why)
		}
	}
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
		!strings.Contains(err.Error(), "where yardr knows no issues (it does on github.com)") {
		t.Errorf("no declaration: %v", err)
	}
}

// When a builtin and a declared source share a host, the yard's hooks
// decide: a declared source with a hook wins over a builtin with none,
// else the builtin wins. A URL one takes and the other refuses is still
// that one's.
func TestIssueWithHooks(t *testing.T) {
	decl := &source.Declaration{Name: "gh-declared", Link: &source.Link{
		Host: "github.com", Issue: `^/([^/]+)/([^/]+)/issues/([0-9]+)`,
		Fixed: "https://github.com/{1}/{2}/issues/{3}", Thread: "{1}/{2}#{3}"}}
	decls := []*source.Declaration{decl}
	const url = "https://github.com/acme/webapp/issues/7"
	for _, c := range []struct {
		name   string
		hooked map[string]bool
		want   string
	}{
		{"no hooks", nil, "github"},
		{"declared hooked", map[string]bool{"gh-declared": true}, "gh-declared"},
		{"both hooked", map[string]bool{"github": true, "gh-declared": true}, "github"},
		{"builtin hooked", map[string]bool{"github": true}, "github"},
		{"another hooked", map[string]bool{"token": true}, "github"},
	} {
		name, fixed, err := IssueWithHooks(url, decls, c.hooked)
		if err != nil || name != c.want || fixed != url {
			t.Errorf("%s: = %q, %q, %v; want %q, %q", c.name, name, fixed, err, c.want, url)
		}
	}
	// A pull request: the builtin takes it, the declared one refuses it;
	// the link is still made, as the builtin's.
	if name, fixed, err := IssueWithHooks("https://github.com/acme/webapp/pull/7", decls, map[string]bool{"gh-declared": true}); err != nil || name != "github" || fixed != "https://github.com/acme/webapp/pull/7" {
		t.Errorf("pull request: %q, %q, %v", name, fixed, err)
	}
	// No issue of either: the error names the preferred source.
	if _, _, err := IssueWithHooks("https://github.com/acme/webapp/discussions/7", decls, map[string]bool{"gh-declared": true}); err == nil || !strings.Contains(err.Error(), "is no gh-declared issue") {
		t.Errorf("discussions with declared hooked: %v", err)
	}
	if _, _, err := IssueWithHooks("https://github.com/acme/webapp/discussions/7", decls, nil); err == nil || !strings.Contains(err.Error(), "is no GitHub issue") {
		t.Errorf("discussions without hooks: %v", err)
	}
}

// A URL whose source is known (a delivery's thread) is an issue of that
// source or nothing: a GitHub delivery cannot link a token, and a
// source with no issues (token) links none.
func TestIssueInItsSource(t *testing.T) {
	for _, c := range []struct{ source, raw, why string }{
		{"github", "https://github.com/acme/webapp/discussions/7", "is no GitHub issue"},
		{"token", "https://github.com/acme/webapp/issues/7", "a bead cannot link to an issue in token (it can in github)"},
		{"", "https://github.com/acme/webapp/issues/7", "a bead cannot link to an issue in an unknown source"},
	} {
		if got, err := IssueIn(c.source, c.raw); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("IssueIn(%q, %q) = %q, %v; want an error with %q", c.source, c.raw, got, err, c.why)
		}
	}
	if got := LinkSources(); len(got) != 1 || got[0] != "github" {
		t.Errorf("LinkSources() = %v", got)
	}
}

// The thread URL a delivery carries is that of its issue in the fixed form
// too, so a link made from the URL and one made from a delivery are equal.
func TestDeliveryThreadIsItsIssue(t *testing.T) {
	raw, err := os.ReadFile("testdata/github/issues_opened.json")
	if err != nil {
		t.Fatal(err)
	}
	env, err := GitHub{}.Normalize(http.Header{"X-Github-Event": {"issues"}}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := IssueIn(env.Source, env.Thread.URL+"#issuecomment-99"); err != nil || got != env.Thread.URL {
		t.Fatalf("github thread %q: %q, %v", env.Thread.URL, got, err)
	}
}

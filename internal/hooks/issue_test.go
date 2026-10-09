package hooks

import (
	"strings"
	"testing"
)

// An issue has one fixed form however its URL was copied: a Linear URL
// loses the title slug, which changes with the title, and both lose
// whatever follows the issue. What is no issue of the source is refused.
func TestIssueFixedForm(t *testing.T) {
	const lin, gh = "https://linear.app/acme/issue/YRD-12", "https://github.com/acme/webapp/issues/7"
	for _, c := range []struct{ raw, source, want string }{
		{"https://linear.app/acme/issue/YRD-12", "linear", lin},
		{"https://linear.app/acme/issue/YRD-12/web-view-shows-links", "linear", lin},
		{"https://linear.app/acme/issue/YRD-12/a-new-title", "linear", lin},
		{"https://linear.app/acme/issue/YRD-12/web-view/extra?x=1", "linear", lin},
		{"https://linear.app/acme/issue/YRD-12?focus=1", "linear", lin},
		{"https://linear.app/acme/issue/YRD-12/web-view#comment-4f1c", "linear", lin},
		{"http://LINEAR.app/Acme/issue/yrd-12/", "linear", lin},
		{" https://linear.app/acme/issue/YRD-12\n", "linear", lin},
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
		{"https://linear.app/acme/project/web-view-1a2b", "is no Linear issue"},
		{"https://linear.app/acme/issue/", "is no Linear issue"},
		{"https://linear.app/acme/issue/12", "is no Linear issue"},
		{"https://linear.app/acme/profiles/jane", "is no Linear issue"},
		{"https://gitlab.com/acme/webapp/-/issues/7", "is on gitlab.com, where yardr knows no issues (it does on github.com and linear.app)"},
		{"https://linear.app.evil.example/acme/issue/YRD-12", "where yardr knows no issues"},
		{"https://jira.example/browse/YRD-12", "where yardr knows no issues"},
		{"YRD-12", "is not the URL of an issue"},
		{"linear.app/acme/issue/YRD-12", "is not the URL of an issue"},
		{"ftp://linear.app/acme/issue/YRD-12", "is not the URL of an issue"},
		{"", "is not the URL of an issue"},
	} {
		if source, got, err := Issue(c.raw); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("Issue(%q) = %q, %q, %v; want an error with %q", c.raw, source, got, err, c.why)
		}
	}
}

// A URL whose source is known (a delivery's thread) is an issue of that
// source or nothing: a Linear delivery cannot link a GitHub issue, and a
// source with no issues (token) links none.
func TestIssueInItsSource(t *testing.T) {
	for _, c := range []struct{ source, raw, why string }{
		{"linear", "https://github.com/acme/webapp/issues/7", "is on github.com, not on linear.app: no linear issue"},
		{"github", "https://github.com/acme/webapp/discussions/7", "is no GitHub issue"},
		{"token", "https://linear.app/acme/issue/YRD-12", "a bead cannot link to an issue in token (it can in github, linear)"},
		{"", "https://linear.app/acme/issue/YRD-12", "a bead cannot link to an issue in an unknown source"},
	} {
		if got, err := IssueIn(c.source, c.raw); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("IssueIn(%q, %q) = %q, %v; want an error with %q", c.source, c.raw, got, err, c.why)
		}
	}
	if got := LinkSources(); len(got) != 2 || got[0] != "github" || got[1] != "linear" {
		t.Errorf("LinkSources() = %v", got)
	}
}

// The thread URL a delivery carries is that of its issue in the fixed form
// too, so a link made from the URL and one made from a delivery are equal.
func TestDeliveryThreadIsItsIssue(t *testing.T) {
	body := []byte(`{"action":"create","type":"Comment","url":"https://linear.app/acme/issue/YRD-12/web-view#comment-1",
		"data":{"id":"c1","body":"hi","issueId":"i1","issue":{"id":"i1","title":"Web view","url":"https://linear.app/acme/issue/YRD-12/web-view"}}}`)
	env, err := Linear{}.Normalize(nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := IssueIn(env.Source, env.Thread.URL); err != nil || got != "https://linear.app/acme/issue/YRD-12" {
		t.Fatalf("linear thread %q: %q, %v", env.Thread.URL, got, err)
	}
}

package hooks

import (
	"testing"

	"yardr.dev/yardr/internal/source"
)

// A declared source with a repos pattern reads a depot's repository from
// its origin remote: the remote's path past the source's host, however
// the URL is written, when the pattern matches it whole. A pattern that
// lets repositories nest reads those too.
func TestRemoteRepo(t *testing.T) {
	d, err := source.Parse("acme.yaml", []byte(
		"name: acme\nbase: https://acme.test\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"+
			"link:\n  host: acme.test\n  issue: '^/([^/]+)/([^/]+)/issues/([0-9]+)'\n"+
			"  fixed: \"https://acme.test/{1}/{2}/issues/{3}\"\n  thread: \"{1}/{2}#{3}\"\n  fold: lower\n"+
			"  repos: '[^/]+/[^/]+'\n"))
	if err != nil {
		t.Fatal(err)
	}
	nest, err := source.Parse("nest.yaml", []byte(
		"name: nest\nbase: https://nest.test\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"+
			"link:\n  host: nest.test\n  issue: '^/([^/]+)/([^/]+)/([^/]+)/issues/([0-9]+)'\n"+
			"  fixed: \"https://nest.test/{1}/{2}/{3}/issues/{4}\"\n  thread: \"{1}/{2}/{3}#{4}\"\n"+
			"  repos: '[^/]+/[^/]+/[^/]+'\n"))
	if err != nil {
		t.Fatal(err)
	}
	one, err := source.Parse("one.yaml", []byte(
		"name: one\nbase: https://one.test\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"+
			"link:\n  host: one.test\n  issue: '^/issue/([A-Z]+-[0-9]+)'\n  fixed: \"https://one.test/issue/{1}\"\n  thread: \"{1}\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := RepoerFor("acme", d)
	if !ok {
		t.Fatal("declared acme has no Repoer")
	}
	if _, is := r.(Declared); !is {
		t.Fatalf("RepoerFor(acme) is %T, want Declared", r)
	}
	if _, ok := RepoerFor("one", one); ok {
		t.Fatal("a source with no repos pattern has a Repoer")
	}
	if _, ok := RepoerFor("acme", nil); ok {
		t.Fatal("acme without a declaration has a Repoer")
	}
	n, ok := RepoerFor("nest", nest)
	if !ok {
		t.Fatal("declared nest has no Repoer")
	}
	for _, c := range []struct {
		repoer interface{ RemoteRepo(string) string }
		raw    string
		want   string
	}{
		{r, "git@acme.test:acme/webapp.git", "acme/webapp"},
		{r, "https://acme.test/Acme/WebApp", "acme/webapp"},
		{r, "ssh://git@acme.test/acme/webapp.git", "acme/webapp"},
		{r, "https://acme.test/acme/webapp/extra", ""},
		{r, "https://acme.test/acme", ""},
		{r, "git@github.com:acme/webapp.git", ""},
		{r, "not a remote", ""},
		{n, "https://nest.test/group/sub/name", "group/sub/name"},
		{n, "git@nest.test:group/sub/name.git", "group/sub/name"},
		{n, "https://nest.test/group/name", ""},
	} {
		if got := c.repoer.RemoteRepo(c.raw); got != c.want {
			t.Errorf("RemoteRepo(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
	if _, ok := RepoerFor("github", nil); ok {
		t.Fatal("github without a declaration has a Repoer")
	}
	declared, ok := RepoerFor("github", githubPackDecl(t))
	if !ok {
		t.Fatal("declared github has no Repoer")
	}
	for _, c := range []struct{ raw, want string }{
		{"git@github.com:acme/webapp.git", "acme/webapp"},
		{"https://github.com/acme/webapp", "acme/webapp"},
		{"https://github.com/acme/webapp/extra", ""},
		{"git@acme.test:acme/webapp.git", ""},
	} {
		if got := declared.RemoteRepo(c.raw); got != c.want {
			t.Errorf("github RemoteRepo(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

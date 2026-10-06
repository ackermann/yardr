package hooks

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// Linker is a source whose issues a bead can link to (yardr link). Nothing
// here calls the source: a link is made from the URL alone.
type Linker interface {
	// IssueHost is the host the source's issue URLs are on.
	IssueHost() string
	// Issue turns a URL on that host into the issue's fixed form, the one
	// spelling a link stores and compares, and refuses what is no issue of
	// the source. u has an http(s) scheme and the source's host.
	Issue(u *url.URL) (string, error)
}

// LinkSources lists the sources a bead can link to, sorted.
func LinkSources() []string {
	var out []string
	for name, ad := range Adapters {
		if _, ok := ad.(Linker); ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Issue is the source and the fixed form of the issue raw names: the
// source is told by the URL's host. Two URLs are of the same issue exactly
// when their fixed forms are equal.
func Issue(raw string) (source, fixed string, err error) {
	u, err := issueURL(raw)
	if err != nil {
		return "", "", err
	}
	var hosts []string
	for _, name := range LinkSources() {
		l := Adapters[name].(Linker)
		if strings.EqualFold(u.Hostname(), l.IssueHost()) {
			fixed, err = l.Issue(u)
			return name, fixed, err
		}
		hosts = append(hosts, l.IssueHost())
	}
	return "", "", fmt.Errorf("%s is on %s, where yardr knows no issues (it does on %s)", raw, u.Hostname(), strings.Join(hosts, " and "))
}

// IssueIn is the fixed form of raw as an issue of source, for a URL whose
// source is already known (a delivery's thread).
func IssueIn(source, raw string) (string, error) {
	l, ok := Adapters[source].(Linker)
	if !ok {
		return "", fmt.Errorf("a bead cannot link to an issue in %s (it can in %s)", or(source, "an unknown source"), strings.Join(LinkSources(), ", "))
	}
	u, err := issueURL(raw)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(u.Hostname(), l.IssueHost()) {
		return "", fmt.Errorf("%s is on %s, not on %s: no %s issue", raw, u.Hostname(), l.IssueHost(), source)
	}
	return l.Issue(u)
}

// The store brings every link's URL to its fixed form through this, so one
// issue is one link whoever names it; it cannot call Issue itself, since
// this package imports it.
func init() { store.IssueForm(storedIssue) }

// storedIssue is the fixed form of raw for the store (store.IssueForm):
// as an issue of source, or with no source of the one whose host it is on.
// A source that links no issues, or a host no source has issues on, has no
// fixed form, and raw is kept as it is.
func storedIssue(source, raw string) (string, error) {
	if source != "" {
		if _, ok := Adapters[source].(Linker); !ok {
			return raw, nil
		}
		return IssueIn(source, raw)
	}
	u, err := issueURL(raw)
	if err != nil {
		return "", err
	}
	for _, name := range LinkSources() {
		if l := Adapters[name].(Linker); strings.EqualFold(u.Hostname(), l.IssueHost()) {
			return l.Issue(u)
		}
	}
	return raw, nil
}

func issueURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("%q is not the URL of an issue (want https://...)", raw)
	}
	return u, nil
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// linearIssuePath matches /<workspace>/issue/<KEY-n>, then the title slug
// and whatever else follows.
var linearIssuePath = regexp.MustCompile(`^/([A-Za-z0-9][A-Za-z0-9_-]*)/issue/([A-Za-z][A-Za-z0-9]*-[0-9]+)(/.*)?$`)

func (Linear) IssueHost() string { return "linear.app" }

// Issue is https://linear.app/<workspace>/issue/<KEY-n>. The title slug
// Linear appends is left out: it changes when the issue is renamed, and
// Linear resolves the URL without it. Linear matches the workspace and the
// key in any case, so the workspace is lowercased and the key uppercased,
// as Linear writes them.
func (Linear) Issue(u *url.URL) (string, error) {
	m := linearIssuePath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", fmt.Errorf("%s is no Linear issue (want https://linear.app/<workspace>/issue/<KEY-n>)", u)
	}
	return "https://linear.app/" + strings.ToLower(m[1]) + "/issue/" + strings.ToUpper(m[2]), nil
}

// githubIssuePath matches /<owner>/<repo>/<kind>/<n> and whatever follows.
var githubIssuePath = regexp.MustCompile(`^/([A-Za-z0-9](?:-?[A-Za-z0-9])*)/([A-Za-z0-9._-]+)/([a-z]+)/([1-9][0-9]*)(/.*)?$`)

func (GitHub) IssueHost() string { return "github.com" }

// Issue is https://github.com/<owner>/<repo>/issues/<n>, owner and repo
// lowercased: GitHub matches both in any case. A pull request is refused,
// though GitHub numbers it with the issues: it is the work, not the issue
// the work is for, and its life is not a bead's stages.
func (GitHub) Issue(u *url.URL) (string, error) {
	m := githubIssuePath.FindStringSubmatch(u.Path)
	switch {
	case m == nil || (m[3] != "issues" && m[3] != "pull"):
		return "", fmt.Errorf("%s is no GitHub issue (want https://github.com/<owner>/<repo>/issues/<n>)", u)
	case m[3] == "pull":
		return "", fmt.Errorf("%s is a pull request, not an issue: a bead links to an issue (https://github.com/<owner>/<repo>/issues/<n>)", u)
	}
	return "https://github.com/" + strings.ToLower(m[1]) + "/" + strings.ToLower(m[2]) + "/issues/" + m[4], nil
}

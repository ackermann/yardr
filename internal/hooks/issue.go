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

// Puller is what an Adapter whose source has pull requests adds: a bead can
// track one (dispatch.Job.Pull), and a delivery of a CI run names its pull
// request by repository and number alone.
type Puller interface {
	// PullOf is the thread of the pull request raw names, in the form
	// IssueThread returns it; "" when raw is no pull request of the
	// source's.
	PullOf(raw string) string
	// PullURL is the URL of the pull request number of repo, as a link
	// stores it.
	PullURL(repo, number string) string
}

// PullerFor is the source's Puller, if it has pull requests.
func PullerFor(source string) (Puller, bool) {
	p, ok := Adapters[source].(Puller)
	return p, ok
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
// lowercased: GitHub matches both in any case. A pull request is a thread
// a bead can track too, as https://github.com/<owner>/<repo>/pull/<n>: one
// that came from outside is what its bead is about, and its events move
// that bead. What follows the number (/files, /commits) is left out.
func (GitHub) Issue(u *url.URL) (string, error) {
	m := githubIssuePath.FindStringSubmatch(u.Path)
	if m == nil || (m[3] != "issues" && m[3] != "pull") {
		return "", fmt.Errorf("%s is no GitHub issue or pull request (want https://github.com/<owner>/<repo>/issues/<n> or .../pull/<n>)", u)
	}
	return "https://github.com/" + strings.ToLower(m[1]) + "/" + strings.ToLower(m[2]) + "/" + m[3] + "/" + m[4], nil
}

// PullOf is the thread of the pull request raw names, "" when it names
// none: the URL's /pull/ says which it is before IssueThread reads it.
func (GitHub) PullOf(raw string) string {
	if !strings.Contains(raw, "/pull/") {
		return ""
	}
	thread, err := (GitHub{}).IssueThread(raw)
	if err != nil {
		return ""
	}
	return thread
}

// PullURL is where the pull request number of repo is read.
func (GitHub) PullURL(repo, number string) string {
	return "https://" + GitHub{}.IssueHost() + "/" + repo + "/pull/" + number
}

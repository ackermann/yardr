package hooks

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"yardr.dev/yardr/internal/source"
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

// LinkSources lists the sources a bead can link to, sorted: the builtin
// ones, and the yard's declared sources among decls with a link key.
func LinkSources(decls ...*source.Declaration) []string {
	var out []string
	for name, ad := range Adapters {
		if _, ok := ad.(Linker); ok {
			out = append(out, name)
		}
	}
	for _, d := range decls {
		if d == nil {
			continue
		}
		if d.Link != nil {
			out = append(out, d.Name)
		}
	}
	sort.Strings(out)
	return out
}

// declaredLink is the yard's declared source of that name among decls, when
// it has a link key.
func declaredLink(name string, decls []*source.Declaration) *source.Declaration {
	for _, d := range decls {
		if d != nil && d.Name == name && d.Link != nil {
			return d
		}
	}
	return nil
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
// source is told by the URL's host, a builtin one before a declared one
// with the same host. Two URLs are of the same issue exactly when their
// fixed forms are equal. decls are the yard's declared sources.
func Issue(raw string, decls ...*source.Declaration) (source, fixed string, err error) {
	return IssueWithHooks(raw, decls, nil)
}

// IssueWithHooks is Issue with the yard's hooks taken into account: when a
// builtin source and a declared source share the URL's host, a declared
// source that has a hook in the yard is the link's source before a builtin
// one that has none. hooked reports whether the yard has a hook of the
// source, by name. Without hooks (nil), as Issue: a builtin before a
// declared one. No name is both: a declaration may not take a built-in
// name, so a builtin and a declared source meet only on a host.
//
// The order tried is: builtins with a hook, declared sources with a hook,
// builtins without one, declared sources without one; within each, builtins
// by name and declared sources as given. The first whose host matches and
// whose Issue takes the URL wins; a URL no Issue takes gets the error of
// the first whose host matches, and one on no known host the list of hosts.
func IssueWithHooks(raw string, decls []*source.Declaration, hooked map[string]bool) (source, fixed string, err error) {
	u, err := issueURL(raw)
	if err != nil {
		return "", "", err
	}
	type candidate struct {
		name string
		link Linker
	}
	var builtins, declared []candidate
	for _, d := range decls {
		if d == nil || d.Link == nil {
			continue
		}
		declared = append(declared, candidate{d.Name, Declared{D: d}})
	}
	for _, name := range LinkSources() {
		builtins = append(builtins, candidate{name, Adapters[name].(Linker)})
	}
	ordered := []candidate{}
	for _, c := range builtins {
		if hooked[c.name] {
			ordered = append(ordered, c)
		}
	}
	for _, c := range declared {
		if hooked[c.name] {
			ordered = append(ordered, c)
		}
	}
	for _, c := range builtins {
		if !hooked[c.name] {
			ordered = append(ordered, c)
		}
	}
	for _, c := range declared {
		if !hooked[c.name] {
			ordered = append(ordered, c)
		}
	}
	var firstErr error
	var firstName string
	matched := false
	for _, c := range ordered {
		if !strings.EqualFold(u.Hostname(), c.link.IssueHost()) {
			continue
		}
		matched = true
		fixed, err = c.link.Issue(u)
		if err == nil {
			return c.name, fixed, nil
		}
		if firstErr == nil {
			firstErr, firstName = err, c.name
		}
	}
	if matched {
		return firstName, "", firstErr
	}
	var hosts []string
	for _, c := range append(builtins, declared...) {
		if !slices.Contains(hosts, c.link.IssueHost()) {
			hosts = append(hosts, c.link.IssueHost())
		}
	}
	return "", "", fmt.Errorf("%s is on %s, where yardr knows no issues (it does on %s)", raw, u.Hostname(), strings.Join(hosts, " and "))
}

// IssueIn is the fixed form of raw as an issue of source, for a URL whose
// source is already known (a delivery's thread). decls are the yard's
// declared sources.
func IssueIn(source, raw string, decls ...*source.Declaration) (string, error) {
	var l Linker
	if d := declaredLink(source, decls); d != nil {
		l = Declared{D: d}
	} else if b, ok := Adapters[source].(Linker); ok {
		l = b
	} else {
		return "", fmt.Errorf("a bead cannot link to an issue in %s (it can in %s)", or(source, "an unknown source"), strings.Join(LinkSources(decls...), ", "))
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

// Declared

func (d Declared) IssueHost() string { return d.D.Link.Host }

// Issue is the issue's one fixed form: link.fixed over the groups
// link.issue reads from the URL's path. It runs nothing: a link is made
// from the URL alone.
func (d Declared) Issue(u *url.URL) (string, error) {
	if fixed, ok := d.D.Link.FixedForm(u.Path); ok {
		return fixed, nil
	}
	return "", fmt.Errorf("%s is no %s issue", u, d.D.Name)
}

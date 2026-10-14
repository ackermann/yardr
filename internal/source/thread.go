package source

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Thread is how the yard speaks in the source's threads: a command per
// thing it does there. Each is the program and its arguments, a list run
// without a shell, as limits.command is: one JSON object on stdin, one on
// stdout, exit 0.
type Thread struct {
	// Reply answers in a thread: in {thread, parent, body, key}, out
	// {id, url}. Posted finds what a reply whose outcome was lost left:
	// in {thread, key}, out {id, url} or {}.
	Reply  []string `json:"reply,omitempty"`
	Posted []string `json:"posted,omitempty"`
	// Read lists a thread's messages: in {thread}, out {messages: [...]}.
	Read []string `json:"read,omitempty"`
	// Open starts a thread: in {in, title, body, key, assignee?}, out
	// {id, title, url}.
	Open []string `json:"open,omitempty"`
	// Opened finds what an open whose outcome was lost left: in {in, key},
	// out {id, title, url} or {}. Posted's sibling for opened threads.
	Opened []string `json:"opened,omitempty"`
	// State sets a thread's status: in {thread, state, forward?}, out
	// {status, moved, was?}. forward is a stage map's move, which never
	// moves backwards; setting moves exactly.
	State []string `json:"state,omitempty"`
	// Place is what a place to open a thread in is called in this source
	// (team): the word --in takes. Open needs it.
	Place string `json:"place,omitempty"`
	// States are the words State takes, of started, review and done.
	States []string `json:"states,omitempty"`
	// Labels is whether the source takes free-form tags: State then takes
	// label:<name> beside the states, adding the label and never removing
	// one, to any thread.
	Labels bool `json:"labels,omitempty"`
	// Assignee is whether Open takes whom the thread is assigned to, and
	// as what: the person's id in the source (AssigneeID) or their key
	// (AssigneeKey), the two parts of an identity. "" is a source whose
	// open assigns nobody.
	Assignee string `json:"assignee,omitempty"`
}

// The parts of an identity a thread.assignee names.
const (
	AssigneeID  = "id"
	AssigneeKey = "key"
)

// FoldLower folds to lowercase: how a source carries what it matches in
// any case.
const FoldLower = "lower"

// Identity is who a crew member is in the source, as the declaration says
// it: how its keys fold. A key is kept folded, as the source's deliveries
// carry it, so one spelling is stored however it was typed.
type Identity struct {
	// Fold is how identity keys fold: lower.
	Fold string `json:"fold"`
}

// Link is the source's issues as a bead's link: the host its issue URLs are
// on, a regular expression that reads an issue from a URL's path, and two
// templates over the expression's groups ({1}, {2}): Fixed, the issue's one
// fixed form, and Thread, the id a thread command takes for it.
type Link struct {
	Host   string `json:"host"`
	Issue  string `json:"issue"`
	Fixed  string `json:"fixed"`
	Thread string `json:"thread"`
	// Fold is how the expression's groups fold before the templates
	// expand: lower, or "" for a source that matches them exactly.
	Fold string `json:"fold,omitempty"`
	// Pull is the source's second thread shape, its pull requests, nil
	// for a source whose threads are issues alone.
	Pull *PullShape `json:"pull,omitempty"`
	// Repos is the regular expression a repository's path matches, as
	// store.RemoteForm carries it past "host/": "" for a source that
	// keeps no repositories.
	Repos string `json:"repos,omitempty"`

	re      *regexp.Regexp
	pullRe  *regexp.Regexp
	reposRe *regexp.Regexp
}

// PullShape is the source's second thread shape, its pull requests: a
// regular expression that reads one from a URL's path, with its own
// fixed form and thread id, over the same groups in the same order.
// The groups are the repository's path segments and the pull request's
// number, in that order, so a pull request's repository and number read
// back out of them.
type PullShape struct {
	Issue  string `json:"issue"`
	Fixed  string `json:"fixed"`
	Thread string `json:"thread"`
}

// Groups are what link.issue reads from path, the groups of the expression
// without the whole match, or nil when the path is no issue's. The
// expression is compiled anew: a link read back from the store has no
// compiled form. With fold, the groups fold before the fixed and thread
// templates expand, so one spelling names the issue however it was typed.
func (l *Link) Groups(path string) []string {
	re := l.re
	if re == nil {
		var err error
		if re, err = regexp.Compile(l.Issue); err != nil {
			return nil // the reader refused anything else
		}
	}
	m := re.FindStringSubmatch(path)
	if m == nil {
		return nil
	}
	return l.foldGroups(m[1:])
}

// foldGroups folds groups as the link says, before the fixed and thread
// templates expand over them.
func (l *Link) foldGroups(g []string) []string {
	if l.Fold == FoldLower {
		for i := range g {
			g[i] = strings.ToLower(g[i])
		}
	}
	return g
}

// pullGroups are what the pull shape reads from path, the groups of its
// expression without the whole match, or nil when the path is no pull
// request's or the source has one shape alone. As Groups, the compiled
// form is compiled anew, and the groups fold with it.
func (l *Link) pullGroups(path string) []string {
	if l.Pull == nil {
		return nil
	}
	re := l.pullRe
	if re == nil {
		var err error
		if re, err = regexp.Compile(l.Pull.Issue); err != nil {
			return nil // the reader refused anything else
		}
	}
	m := re.FindStringSubmatch(path)
	if m == nil {
		return nil
	}
	return l.foldGroups(m[1:])
}

// FixedForm is the issue whose URL has path in its one fixed form:
// link.fixed with the groups link.issue reads from the path. ok is false
// when the path is no issue's.
func (l *Link) FixedForm(path string) (fixed string, ok bool) {
	g := l.Groups(path)
	if g == nil {
		return "", false
	}
	return expandLink(l.Fixed, g), true
}

// ThreadOf is the id a thread command takes for the issue whose fixed URL
// has path: link.thread over the same groups. ok is false when the path
// is no issue's.
func (l *Link) ThreadOf(path string) (thread string, ok bool) {
	g := l.Groups(path)
	if g == nil {
		return "", false
	}
	return expandLink(l.Thread, g), true
}

// PullFixedForm is the pull request whose URL has path in its one fixed
// form: the pull shape's fixed template over the groups its pattern
// reads from the path. ok is false when the path is no pull request's,
// or the source has one shape alone.
func (l *Link) PullFixedForm(path string) (fixed string, ok bool) {
	if l.Pull == nil {
		return "", false
	}
	g := l.pullGroups(path)
	if g == nil {
		return "", false
	}
	return expandLink(l.Pull.Fixed, g), true
}

// PullThreadOf is the id a thread command takes for the pull request
// whose fixed URL has path: the pull shape's thread template over the
// same groups. ok is false when the path is no pull request's, or the
// source has one shape alone.
func (l *Link) PullThreadOf(path string) (thread string, ok bool) {
	if l.Pull == nil {
		return "", false
	}
	g := l.pullGroups(path)
	if g == nil {
		return "", false
	}
	return expandLink(l.Pull.Thread, g), true
}

// AliasOf is the other fixed form of the thread whose URL has path: the
// pull shape's over the groups the issue shape reads, or the issue
// shape's over the pull shape's. An issue and its pull request share
// their groups, so one names the other. ok is false when the path is no
// thread of either shape, or the source has one shape alone.
func (l *Link) AliasOf(path string) (string, bool) {
	if l.Pull == nil {
		return "", false
	}
	if g := l.Groups(path); g != nil {
		return expandLink(l.Pull.Fixed, g), true
	}
	if g := l.pullGroups(path); g != nil {
		return expandLink(l.Fixed, g), true
	}
	return "", false
}

// PullParts reads the repository and the number from the pull request
// whose URL has path: the pull shape's groups are the repository's path
// segments and the number, in that order. ok is false when the path is
// no pull request's.
func (l *Link) PullParts(path string) (repo, number string, ok bool) {
	g := l.pullGroups(path)
	if len(g) == 0 {
		return "", "", false
	}
	return strings.Join(g[:len(g)-1], "/"), g[len(g)-1], true
}

// PullURL is the URL of the pull request number of repo, as a link
// stores it: the pull shape's fixed template over the repository's path
// and the number. ok is false when they are no pull request of the
// source's: no pull shape, no repository or number, or parts the
// pattern has no groups for.
func (l *Link) PullURL(repo, number string) (string, bool) {
	if l.Pull == nil || repo == "" || number == "" {
		return "", false
	}
	g := append(strings.Split(repo, "/"), number)
	for _, p := range g {
		if p == "" {
			return "", false
		}
	}
	re := l.pullRe
	if re == nil {
		var err error
		if re, err = regexp.Compile(l.Pull.Issue); err != nil {
			return "", false // the reader refused anything else
		}
	}
	if len(g) != re.NumSubexp() {
		return "", false
	}
	return expandLink(l.Pull.Fixed, l.foldGroups(g)), true
}

// RepoOf is the repository the remote names: path, the remote in
// RemoteForm past "host/", when the repos pattern matches it whole.
// ok is false when the source keeps no repositories, or the path
// matches none.
func (l *Link) RepoOf(path string) (string, bool) {
	if l.Repos == "" {
		return "", false
	}
	re := l.reposRe
	if re == nil {
		var err error
		if re, err = regexp.Compile("^(?:" + l.Repos + ")$"); err != nil {
			return "", false // the reader refused anything else
		}
	}
	if !re.MatchString(path) {
		return "", false
	}
	if l.Fold == FoldLower {
		path = strings.ToLower(path)
	}
	return path, true
}

// expandLink fills the template with groups: {1} is the first. A group the
// expression has not is left as it is; the reader refuses such a template,
// so it is never from a file that was added.
func expandLink(tmpl string, groups []string) string {
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '{' {
			b.WriteByte(tmpl[i])
			continue
		}
		m := groupRefAt.FindStringSubmatch(tmpl[i:])
		if m == nil {
			b.WriteByte(tmpl[i])
			continue
		}
		if g, _ := strconv.Atoi(m[1]); g >= 1 && g <= len(groups) {
			b.WriteString(groups[g-1])
		} else {
			b.WriteString(m[0])
		}
		i += len(m[0]) - 1
	}
	return b.String()
}

// thread reads the keys that name the commands by which the yard speaks in
// the source's threads. Refused is what would fail at every run: a command
// that is no list of strings or has no program, an open without the place a
// thread is opened in, a state without the words it takes, an assignee
// that is no part of an identity or has no open to take it.
func (r *reader) thread(e entry) *Thread {
	t := &Thread{}
	var place, states, assignee *entry
	for _, f := range r.entries(e.v, e.path, "reply", "posted", "read", "open", "opened", "state", "place", "states", "labels", "assignee") {
		switch f.key {
		case "reply":
			t.Reply = r.command(f, "linear/thread, reply")
		case "posted":
			t.Posted = r.command(f, "linear/thread, posted")
		case "read":
			t.Read = r.command(f, "linear/thread, read")
		case "open":
			t.Open = r.command(f, "linear/thread, open")
		case "opened":
			t.Opened = r.command(f, "linear/thread, opened")
		case "state":
			t.State = r.command(f, "linear/thread, state")
		case "place":
			place = &f
			if s, ok := r.str(f.v, f.path); ok && !validName.MatchString(s) {
				r.errf(f.v, f.path, "%q: one word: what a place to open a thread in is called in this source (team)", s)
			} else if ok {
				t.Place = s
			}
		case "states":
			states = &f
			for _, s := range r.strs(f.v, f.path) {
				if !slices.Contains(threadStates, s) {
					r.errf(f.v, f.path, "%s is no state of a thread (%s)", s, strings.Join(threadStates, ", "))
					continue
				}
				t.States = append(t.States, s)
			}
		case "labels":
			t.Labels, _ = r.boolean(f.v, f.path)
		case "assignee":
			assignee = &f
			if s, ok := r.str(f.v, f.path); ok && s != AssigneeID && s != AssigneeKey {
				r.errf(f.v, f.path, "%q: %s or %s: which part of a person's identity in the source thread.open takes as assignee", s, AssigneeID, AssigneeKey)
			} else if ok {
				t.Assignee = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return t
	}
	if t.Open != nil && place == nil {
		r.errf(e.v, join(e.path, "place"), "missing: what a place to open a thread in is called in this source (team); thread.open needs it")
	}
	if assignee != nil && t.Open == nil {
		r.errf(assignee.v, assignee.path, "thread.open is missing: the assignee is whom the thread it opens is assigned to")
	}
	// A states given but with no word the yard knows is refused above, word
	// by word; an empty one is as good as none.
	statesGiven := states != nil && (states.v.Kind != yaml.SequenceNode || len(states.v.Content) > 0)
	if t.State != nil && !statesGiven {
		r.errf(e.v, join(e.path, "states"), "missing: the words thread.state takes, of %s", strings.Join(threadStates, ", "))
	}
	return t
}

// threadStates are the words a thread.state takes.
var threadStates = []string{"started", "review", "done"}

// hostName is the host of the source's issue URLs (linear.app).
var hostName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

// groupRefAt is a group of a link template at where it stands: {1}, {2}.
var groupRefAt = regexp.MustCompile(`^\{([0-9]+)\}`)

// link reads the keys that make the source's issues a bead's link: data,
// not a command. Refused is what would fail at every link made: a host that
// is none, an expression that does not compile, a template naming a group
// the expression has not, a fold that is none, a pull shape over other
// groups than the issue's, a repos pattern that does not compile.
func (r *reader) link(e entry) *Link {
	l := &Link{}
	var host, issue, fixed, thread, pull, pullIssue *entry
	for _, f := range r.entries(e.v, e.path, "host", "issue", "fixed", "thread", "fold", "pull", "repos") {
		switch f.key {
		case "host":
			host = &f
			if s, ok := r.str(f.v, f.path); ok && !hostName.MatchString(s) {
				r.errf(f.v, f.path, "%q is no host of the source's issue URLs (linear.app)", s)
			} else if ok {
				l.Host = s
			}
		case "issue":
			issue = &f
			s, ok := r.str(f.v, f.path)
			if !ok {
				continue
			}
			re, err := regexp.Compile(s)
			if err != nil {
				r.errf(f.v, f.path, "%v", err)
			} else {
				l.Issue, l.re = s, re
			}
		case "fixed":
			fixed = &f
			l.Fixed, _ = r.str(f.v, f.path)
		case "thread":
			thread = &f
			l.Thread, _ = r.str(f.v, f.path)
		case "fold":
			if s, ok := r.str(f.v, f.path); ok && s != FoldLower {
				r.errf(f.v, f.path, "%q: want %s", s, FoldLower)
			} else if ok {
				l.Fold = s
			}
		case "pull":
			pull = &f
			l.Pull, pullIssue = r.linkPull(f, l)
		case "repos":
			s, ok := r.str(f.v, f.path)
			if !ok {
				continue
			}
			if _, err := regexp.Compile(s); err != nil {
				r.errf(f.v, f.path, "%v", err)
			} else {
				l.Repos = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return l
	}
	if host == nil {
		r.errf(e.v, join(e.path, "host"), "missing: the host the source's issue URLs are on (linear.app)")
	}
	if issue == nil {
		r.errf(e.v, join(e.path, "issue"), "missing: the regular expression that reads an issue from a URL's path")
	}
	if fixed == nil {
		r.errf(e.v, join(e.path, "fixed"), "missing: the issue's one fixed form, a template over the expression's groups (https://linear.app/{1}/issue/{2})")
	}
	if thread == nil {
		r.errf(e.v, join(e.path, "thread"), "missing: the id a thread command takes for the issue, a template over the expression's groups ({2})")
	}
	if l.re != nil {
		if fixed != nil && l.Fixed != "" {
			r.linkTemplate(fixed.v, fixed.path, l.Fixed, l.re, "link.issue")
		}
		if thread != nil && l.Thread != "" {
			r.linkTemplate(thread.v, thread.path, l.Thread, l.re, "link.issue")
		}
	}
	// The pull shape's groups are the issue's groups: one names the
	// other, so their counts are one. The file may name pull first, so
	// the count is checked here, with both expressions read.
	if pull != nil && pullIssue != nil && l.re != nil && l.pullRe != nil &&
		l.pullRe.NumSubexp() != l.re.NumSubexp() {
		r.errf(pullIssue.v, pullIssue.path, "has %d groups, want the %d of link.issue, in the same order",
			l.pullRe.NumSubexp(), l.re.NumSubexp())
	}
	if pull != nil && pullIssue != nil && l.pullRe != nil && l.pullRe.NumSubexp() < 2 {
		r.errf(pullIssue.v, pullIssue.path, "has %d groups, want at least 2: one or more repository path segments, then the pull request number",
			l.pullRe.NumSubexp())
	}
	return l
}

// linkPull reads the source's second thread shape, its pull requests: a
// pattern with its own fixed and thread templates, over the same groups
// in the same order. It returns the shape and its issue entry: the
// group count is checked by link, with both expressions read. l carries
// the compiled form, as it carries the issue's.
func (r *reader) linkPull(e entry, l *Link) (*PullShape, *entry) {
	p := &PullShape{}
	var issue, fixed, thread *entry
	for _, f := range r.entries(e.v, e.path, "issue", "fixed", "thread") {
		switch f.key {
		case "issue":
			issue = &f
			s, ok := r.str(f.v, f.path)
			if !ok {
				continue
			}
			re, err := regexp.Compile(s)
			if err != nil {
				r.errf(f.v, f.path, "%v", err)
			} else {
				p.Issue, l.pullRe = s, re
			}
		case "fixed":
			fixed = &f
			p.Fixed, _ = r.str(f.v, f.path)
		case "thread":
			thread = &f
			p.Thread, _ = r.str(f.v, f.path)
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return p, issue
	}
	if issue == nil {
		r.errf(e.v, join(e.path, "issue"), "missing: the regular expression that reads a pull request from a URL's path")
	}
	if fixed == nil {
		r.errf(e.v, join(e.path, "fixed"), "missing: the pull request's one fixed form, a template over the expression's groups (https://forge.example/{1}/{2}/pull/{3})")
	}
	if thread == nil {
		r.errf(e.v, join(e.path, "thread"), "missing: the id a thread command takes for the pull request, a template over the expression's groups ({1}/{2}#{3})")
	}
	if l.pullRe != nil {
		if fixed != nil && p.Fixed != "" {
			r.linkTemplate(fixed.v, fixed.path, p.Fixed, l.pullRe, "link.pull.issue")
		}
		if thread != nil && p.Thread != "" {
			r.linkTemplate(thread.v, thread.path, p.Thread, l.pullRe, "link.pull.issue")
		}
	}
	return p, issue
}

// linkTemplate refuses a {n} of s that is no group of re: n counts from 1,
// as the groups of the expression expr do.
func (r *reader) linkTemplate(n *yaml.Node, key, s string, re *regexp.Regexp, expr string) {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		m := groupRefAt.FindStringSubmatch(s[i:])
		if m == nil {
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				end = len(s) - i
			}
			r.errf(n, key, "%s is no group of a template: a group is {1}, {2}, of the expression %s", s[i:i+end+1], expr)
			continue
		}
		g, _ := strconv.Atoi(m[1])
		if g < 1 || g > re.NumSubexp() {
			r.errf(n, key, "%s names a group %s has not (it has %d)", m[0], expr, re.NumSubexp())
		}
		i += len(m[0]) - 1
	}
}

// identity reads how a crew member's keys in the source fold: data, not a
// command. An identity key that says nothing is refused: fold is what it
// is for.
func (r *reader) identity(e entry) *Identity {
	id := &Identity{}
	var fold *entry
	for _, f := range r.entries(e.v, e.path, "fold") {
		fold = &f
		if s, ok := r.str(f.v, f.path); ok && s != FoldLower {
			r.errf(f.v, f.path, "%q: want %s", s, FoldLower)
		} else if ok {
			id.Fold = s
		}
	}
	if e.v.Kind == yaml.MappingNode && fold == nil {
		r.errf(e.v, join(e.path, "fold"), "missing: %s", FoldLower)
	}
	return id
}

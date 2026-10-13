package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/source"
)

// Opening a thread is the other outbound call: a crew member starts one in a
// source instead of answering in one. The rules are those of a reply: the
// CLI in the crew member's pane makes the call with the crew member's own
// credential, and the server only keeps the record.

// Opener is what a Replier whose source lets a thread be opened adds.
type Opener interface {
	Replier
	// Place says what Open's `in` is in this source, for messages: "team"
	// or "repository".
	Place() string
	// Assignee says which part of a person's identity in the source Open
	// takes as assignee, source.AssigneeID or source.AssigneeKey; "" for
	// a source whose Open assigns nobody.
	Assignee() string
	// Open starts a thread titled title, with body, in the place in (a
	// Linear team's key or name, a GitHub owner/repo), marked with key as
	// Reply marks a comment, and assigned to assignee, that part of the
	// person's identity ("" for nobody). It returns the thread as
	// deliveries from it will name it, so the opener participates in it
	// by that id.
	Open(ctx context.Context, in, title, body, key, assignee, cred string) (Thread, error)
	// Opened finds the thread an earlier Open with key left in the place
	// in, or nil when there is none. since is when the attempt was put on
	// record: nothing older can be it.
	Opened(ctx context.Context, in, key, cred string, since time.Time) (*Thread, error)
}

// OpenerFor is the source's Opener, if threads can be opened in it: the
// one built from d, the yard's declared source of that name, when it
// has the keys opening takes, else the builtin one of that name. No name
// is both, so at most one applies. d is nil when the yard declares no
// such source. home is the yard's home, which a relative command program
// runs from; mint is the source's mint_command setting, "" when the yard
// set none.
func OpenerFor(source string, d *source.Declaration, home, mint string) (Opener, bool) {
	if dec, ok := declared(source, d, home, mint); ok {
		if dec.openable() {
			return dec, true
		}
		return nil, false
	}
	if o, ok := Adapters[source].(Opener); ok {
		return o, true
	}
	return nil, false
}

// Openable lists the sources a thread can be opened in, sorted: the builtin
// ones, and the yard's declared sources among decls with the keys opening
// takes.
func Openable(decls ...*source.Declaration) []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := OpenerFor(s, nil, "", ""); ok {
			out = append(out, s)
		}
	}
	for _, d := range decls {
		if d == nil {
			continue
		}
		if _, ok := OpenerFor(d.Name, d, "", ""); ok {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Assignable lists the sources whose Open takes an assignee, of Openable's.
func Assignable(decls ...*source.Declaration) []string {
	byName := map[string]*source.Declaration{}
	for _, d := range decls {
		if d != nil {
			byName[d.Name] = d
		}
	}
	return slices.DeleteFunc(Openable(decls...), func(s string) bool {
		o, ok := OpenerFor(s, byName[s], "", "")
		return !ok || o.Assignee() == ""
	})
}

// openMarker is replyMarker for an opened thread: how a GitHub issue
// carries its key.
func openMarker(key string) string { return "<!-- yardr-thread " + key + " -->" }

// GitHub

func (GitHub) Place() string { return "repository" }

// Assignee is the login: what GitHub's issues name an assignee by.
func (GitHub) Assignee() string { return source.AssigneeKey }

// githubRepoName matches owner/repo as --in gives it: two names, nothing that
// would reach another endpoint.
var githubRepoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

type githubOpened struct {
	Number        int          `json:"number"`
	Title         string       `json:"title"`
	Body          string       `json:"body"`
	HTMLURL       string       `json:"html_url"`
	RepositoryURL string       `json:"repository_url"`
	Assignees     []githubUser `json:"assignees"`
}

// thread names the issue as its deliveries do: by the repository's full
// name as GitHub spells it (the tail of repository_url), which may differ
// in case from what was typed.
func (i githubOpened) thread(in string) Thread {
	repo := in
	if _, full, ok := strings.Cut(i.RepositoryURL, "/repos/"); ok && strings.Count(full, "/") == 1 {
		repo = full
	}
	return Thread{ID: thread(repo, i.Number), Title: i.Title, URL: i.HTMLURL}
}

func githubIssues(in string) (string, error) {
	if !githubRepoName.MatchString(in) {
		return "", fmt.Errorf("github: --in %q: want owner/repo", in)
	}
	return GitHubAPI + "/repos/" + in + "/issues", nil
}

// Open creates an issue in the repository. GitHub takes no client-chosen
// id, so the key goes into the body as a marker (openMarker).
func (GitHub) Open(ctx context.Context, in, title, body, key, assignee, cred string) (Thread, error) {
	u, err := githubIssues(in)
	if err != nil {
		return Thread{}, err
	}
	if key != "" {
		body = strings.TrimSpace(body + "\n\n" + openMarker(key))
	}
	issue := map[string]any{"title": title, "body": body}
	if assignee != "" {
		issue["assignees"] = []string{assignee}
	}
	var i githubOpened
	if err := githubCall(ctx, http.MethodPost, u, cred, issue, &i); err != nil {
		return Thread{}, fmt.Errorf("github: %w", err)
	}
	if i.Number <= 0 {
		return Thread{}, fmt.Errorf("github: the answer names no issue")
	}
	if assignee != "" && !slices.ContainsFunc(i.Assignees, func(user githubUser) bool {
		return strings.EqualFold(user.Login, assignee)
	}) {
		issue := i.HTMLURL
		if issue == "" {
			issue = fmt.Sprintf("issue #%d", i.Number)
		}
		return Thread{}, fmt.Errorf("github: created %s but it was not assigned to %s; GitHub needs push access to set assignees", issue, assignee)
	}
	return i.thread(in), nil
}

// Opened finds the issue carrying the key's marker among those of the
// repository touched since the attempt was recorded (a minute earlier, for
// clocks that disagree), newest first.
func (GitHub) Opened(ctx context.Context, in, key, cred string, since time.Time) (*Thread, error) {
	u, err := githubIssues(in)
	if err != nil {
		return nil, err
	}
	q := url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"desc"}, "per_page": {"100"}}
	if !since.IsZero() {
		q.Set("since", since.Add(-time.Minute).UTC().Format(time.RFC3339))
	}
	for page := 1; ; page++ {
		q.Set("page", fmt.Sprint(page))
		var is []githubOpened
		if err := githubCall(ctx, http.MethodGet, u+"?"+q.Encode(), cred, nil, &is); err != nil {
			return nil, fmt.Errorf("github: %w", err)
		}
		for _, i := range is {
			if strings.Contains(i.Body, openMarker(key)) {
				t := i.thread(in)
				return &t, nil
			}
		}
		if len(is) < 100 {
			return nil, nil
		}
	}
}

// Declared

func (d Declared) Place() string { return d.D.Thread.Place }

func (d Declared) Assignee() string { return d.D.Thread.Assignee }

// Open starts a thread titled title, with body, in the place in, by
// thread.open: in {in, title, body, key, assignee?}, out {id, title,
// url}. The key marks the thread as Reply marks a comment, so a retry
// finds it with Opened instead of opening it twice. assignee is there
// only when the thread is opened assigned, which the caller asks of a
// source with thread.assignee alone.
func (d Declared) Open(ctx context.Context, in, title, body, key, assignee, cred string) (Thread, error) {
	input := map[string]any{"in": in, "title": title, "body": body, "key": key}
	if assignee != "" {
		input["assignee"] = assignee
	}
	var t Thread
	err := d.runThread(ctx, "thread.open", d.D.Thread.Open, input, cred, func(out []byte) error {
		var o struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			URL   string `json:"url"`
		}
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {id, title, url}: %v", err)
		}
		t = Thread{ID: firstOf(o.ID, key), Title: firstOf(o.Title, title), URL: o.URL}
		return nil
	})
	if err != nil {
		return Thread{}, err
	}
	return t, nil
}

// Opened finds the thread an earlier Open with key left in the place in,
// by thread.opened: in {in, key, since?}, out {id, title, url} or {} for
// none. A retry asks before it opens, so an attempt whose answer was lost
// is not opened twice. since is when the attempt was put on record, in
// RFC 3339, so the command can bound its search; a command that needs no
// bound ignores the field.
func (d Declared) Opened(ctx context.Context, in, key, cred string, since time.Time) (*Thread, error) {
	input := map[string]any{"in": in, "key": key}
	if !since.IsZero() {
		input["since"] = since.UTC().Format(time.RFC3339)
	}
	var found *Thread
	err := d.runThread(ctx, "thread.opened", d.D.Thread.Opened, input, cred, func(out []byte) error {
		var o struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			URL   string `json:"url"`
		}
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {id, title, url}: %v", err)
		}
		if o.ID != "" {
			found = &Thread{ID: o.ID, Title: o.Title, URL: o.URL}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

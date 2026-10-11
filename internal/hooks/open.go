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
	// Open starts a thread titled title, with body, in the place in (a
	// Linear team's key or name, a GitHub owner/repo), marked with key as
	// Reply marks a comment. It returns the thread as deliveries from it
	// will name it, so the opener participates in it by that id.
	Open(ctx context.Context, in, title, body, key, cred string) (Thread, error)
	// Opened finds the thread an earlier Open with key left in the place
	// in, or nil when there is none. since is when the attempt was put on
	// record: nothing older can be it.
	Opened(ctx context.Context, in, key, cred string, since time.Time) (*Thread, error)
}

// OpenerFor is the source's Opener, if threads can be opened in it: the
// one built from d, the yard's declared source of that name, when it
// has the keys opening takes, else the builtin one of that name. The
// declared one wins when the yard has it. d is nil when the yard
// declares no such source. home is the yard's home, which a relative
// command program runs from.
func OpenerFor(source string, d *source.Declaration, home string) (Opener, bool) {
	if dec, ok := declared(source, d, home); ok && dec.openable() {
		return dec, true
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
		if _, ok := OpenerFor(s, nil, ""); ok {
			out = append(out, s)
		}
	}
	for _, d := range decls {
		if d == nil {
			continue
		}
		if _, ok := OpenerFor(d.Name, d, ""); ok {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// openMarker is replyMarker for an opened thread: how a GitHub issue
// carries its key.
func openMarker(key string) string { return "<!-- yardr-thread " + key + " -->" }

// Linear

func (Linear) Place() string { return "team" }

type linearIssueNode struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

func (n linearIssueNode) thread() Thread { return Thread{ID: n.ID, Title: n.Title, URL: n.URL} }

// linearTeam is the id of the team whose key or name is in, compared
// without case. The teams are listed and matched here: the credential sees
// only the teams it may write to, and a key wins over another team's name.
func linearTeam(ctx context.Context, in, cred string) (string, error) {
	type team struct {
		ID   string `json:"id"`
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	var teams []team
	var after any
	for {
		var q struct {
			Teams struct {
				Nodes    []team `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"teams"`
		}
		err := linearGraphQL(ctx, cred, `query($after: String) {
  teams(first: 100, after: $after) {
    nodes { id key name }
    pageInfo { hasNextPage endCursor }
  }
}`, map[string]any{"after": after}, &q)
		if err != nil {
			return "", err
		}
		teams = append(teams, q.Teams.Nodes...)
		if !q.Teams.PageInfo.HasNextPage || q.Teams.PageInfo.EndCursor == "" {
			break
		}
		after = q.Teams.PageInfo.EndCursor
	}
	var named []team
	keys := make([]string, 0, len(teams))
	for _, t := range teams {
		if strings.EqualFold(t.Key, in) {
			return t.ID, nil
		}
		if strings.EqualFold(t.Name, in) {
			named = append(named, t)
		}
		keys = append(keys, t.Key)
	}
	switch len(named) {
	case 1:
		return named[0].ID, nil
	case 0:
		return "", fmt.Errorf("linear: no team with key or name %q among those this credential sees (%s)", in, strings.Join(keys, ", "))
	}
	return "", fmt.Errorf("linear: %d teams are named %q: give the team's key", len(named), in)
}

// Open creates an issue in the team. The key becomes the issue's id, so
// Linear itself refuses a second issue with it, and the id is the thread's:
// what deliveries about the issue carry.
func (Linear) Open(ctx context.Context, in, title, body, key, cred string) (Thread, error) {
	team, err := linearTeam(ctx, in, cred)
	if err != nil {
		return Thread{}, err
	}
	input := map[string]any{"teamId": team, "title": title}
	if body != "" {
		input["description"] = body
	}
	if key != "" {
		input["id"] = key
	}
	var m struct {
		IssueCreate struct {
			Success bool            `json:"success"`
			Issue   linearIssueNode `json:"issue"`
		} `json:"issueCreate"`
	}
	err = linearGraphQL(ctx, cred, `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id title url } } }`,
		map[string]any{"input": input}, &m)
	if err != nil {
		return Thread{}, err
	}
	if !m.IssueCreate.Success {
		return Thread{}, fmt.Errorf("linear: issueCreate did not succeed")
	}
	t := m.IssueCreate.Issue.thread()
	t.ID, t.Title = firstOf(t.ID, key), firstOf(t.Title, title)
	return t, nil
}

// Opened looks the issue up by the key Open made its id; as in Posted, an
// error naming the entity is the "none" case. The team is not asked for
// again: the id is the key's alone.
func (Linear) Opened(ctx context.Context, _, key, cred string, _ time.Time) (*Thread, error) {
	var q struct {
		Issue linearIssueNode `json:"issue"`
	}
	err := linearGraphQL(ctx, cred, `query($id: String!) { issue(id: $id) { id title url } }`, map[string]any{"id": key}, &q)
	if err != nil {
		if strings.Contains(err.Error(), "Entity not found") {
			return nil, nil
		}
		return nil, err
	}
	if q.Issue.ID == "" {
		return nil, nil
	}
	t := q.Issue.thread()
	return &t, nil
}

// GitHub

func (GitHub) Place() string { return "repository" }

// githubRepoName matches owner/repo as --in gives it: two names, nothing that
// would reach another endpoint.
var githubRepoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

type githubOpened struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	HTMLURL       string `json:"html_url"`
	RepositoryURL string `json:"repository_url"`
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
func (GitHub) Open(ctx context.Context, in, title, body, key, cred string) (Thread, error) {
	u, err := githubIssues(in)
	if err != nil {
		return Thread{}, err
	}
	if key != "" {
		body = strings.TrimSpace(body + "\n\n" + openMarker(key))
	}
	var i githubOpened
	if err := githubCall(ctx, http.MethodPost, u, cred, map[string]string{"title": title, "body": body}, &i); err != nil {
		return Thread{}, fmt.Errorf("github: %w", err)
	}
	if i.Number <= 0 {
		return Thread{}, fmt.Errorf("github: the answer names no issue")
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

// Open starts a thread titled title, with body, in the place in, by
// thread.open: in {in, title, body, key}, out {id, title, url}. The key
// marks the thread as Reply marks a comment, so a retry finds it with
// Opened instead of opening it twice.
func (d Declared) Open(ctx context.Context, in, title, body, key, cred string) (Thread, error) {
	input := map[string]any{"in": in, "title": title, "body": body, "key": key}
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
// by thread.opened: in {in, key}, out {id, title, url} or {} for none. A
// retry asks before it opens, so an attempt whose answer was lost is not
// opened twice. since is not handed to the command: the thread is found
// by the key alone.
func (d Declared) Opened(ctx context.Context, in, key, cred string, _ time.Time) (*Thread, error) {
	input := map[string]any{"in": in, "key": key}
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

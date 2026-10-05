package hooks

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Opening a thread is the other outbound call: a resident starts one in a
// source instead of answering in one. The rules are those of a reply: the
// CLI in the resident's pane makes the call with the resident's own
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

// OpenerFor is the source's Opener, if threads can be opened in it.
func OpenerFor(source string) (Opener, bool) {
	o, ok := Adapters[source].(Opener)
	return o, ok
}

// Openable lists the sources a thread can be opened in, sorted.
func Openable() []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := OpenerFor(s); ok {
			out = append(out, s)
		}
	}
	return out
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

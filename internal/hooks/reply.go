package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// The outbound side. yard never speaks for anyone: a resident answers a
// delivery in its source with its own credential, and the CLI in its pane
// makes the call. The server is never asked to speak and never holds the
// credential; it only hands out the hook event that is being answered.

// Message is one message of a source's thread: a comment.
type Message struct {
	ID     string    `json:"id"`
	Parent string    `json:"parent,omitempty"` // the message it answers, in a threaded source
	Author Party     `json:"author"`
	At     time.Time `json:"at"`
	Body   string    `json:"body"`
	URL    string    `json:"url,omitempty"`
}

// Replier is what an Adapter whose source can be answered in adds.
type Replier interface {
	// Credential is the caller's credential for the source, read from the
	// calling process's environment (a resident's env_file sets it).
	Credential(ctx context.Context) (string, error)
	// Reply posts body in the thread of the delivery env, under the
	// conversation of message to ("" for the delivery's own), and returns
	// the URL of what it posted.
	Reply(ctx context.Context, env Envelope, to, body, cred string) (url string, err error)
	// Thread lists the messages of the delivery's thread, oldest first.
	Thread(ctx context.Context, env Envelope, cred string) ([]Message, error)
}

// Where the outbound calls go; tests point them at fake servers.
var (
	LinearAPI  = "https://api.linear.app/graphql"
	GitHubAPI  = "https://api.github.com"
	HTTPClient = &http.Client{Timeout: 30 * time.Second}
)

// ReplierFor is the source's Replier, if it can be answered in.
func ReplierFor(source string) (Replier, bool) {
	r, ok := Adapters[source].(Replier)
	return r, ok
}

// Repliable lists the sources that can be answered in, sorted.
func Repliable() []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := ReplierFor(s); ok {
			out = append(out, s)
		}
	}
	return out
}

func envCredential(name string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s is not set in this environment: answering in a source takes your own credential (a resident's env_file sets it)", name)
}

// call sends a JSON request and decodes a JSON answer into out, turning a
// status other than 2xx into an error that shows what the source said.
func call(ctx context.Context, method, url string, hdr http.Header, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header = hdr
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(raw[:min(len(raw), 500)])))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: answer is not JSON: %v", method, url, err)
	}
	return nil
}

// Linear

func (Linear) Credential(context.Context) (string, error) { return envCredential("LINEAR_API_KEY") }

// linearGraphQL runs one GraphQL operation as the holder of cred.
func linearGraphQL(ctx context.Context, cred, query string, vars map[string]any, out any) error {
	hdr := http.Header{}
	// A personal API key is sent as it is; an OAuth token (an app's) as a
	// bearer token.
	if strings.HasPrefix(cred, "lin_api_") || strings.HasPrefix(cred, "Bearer ") {
		hdr.Set("Authorization", cred)
	} else {
		hdr.Set("Authorization", "Bearer "+cred)
	}
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := call(ctx, http.MethodPost, LinearAPI, hdr, map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, len(resp.Errors))
		for i, e := range resp.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("linear: %s", strings.Join(msgs, "; "))
	}
	return json.Unmarshal(resp.Data, out)
}

// linearIssue is the issue a delivery belongs to: replies and threads are
// the comments of an issue.
func linearIssue(env Envelope) (string, error) {
	if env.Thread.ID == "" || (env.Type == "comment" && env.Thread.ID == env.Message) {
		return "", fmt.Errorf("this linear %s is not on an issue, so it has no comments to answer in", env.Type)
	}
	return env.Thread.ID, nil
}

// Reply comments on the delivery's issue. Linear threads comments one
// level deep: a reply's parent is always the top-level comment that opened
// the conversation. So a reply goes under the root of the message it
// answers: the message's parent when it has one, else the message itself.
// A delivery that is not a comment (the issue itself) gets a new
// top-level comment.
func (Linear) Reply(ctx context.Context, env Envelope, to, body, cred string) (string, error) {
	issue, err := linearIssue(env)
	if err != nil {
		return "", err
	}
	root := ""
	switch {
	case to != "":
		var q struct {
			Comment struct {
				ID     string `json:"id"`
				Parent *struct {
					ID string `json:"id"`
				} `json:"parent"`
				Issue *struct {
					ID string `json:"id"`
				} `json:"issue"`
			} `json:"comment"`
		}
		err := linearGraphQL(ctx, cred, `query($id: String!) { comment(id: $id) { id parent { id } issue { id } } }`,
			map[string]any{"id": to}, &q)
		if err != nil {
			return "", err
		}
		if q.Comment.Issue == nil || q.Comment.Issue.ID != issue {
			return "", fmt.Errorf("linear comment %s is not on the delivery's issue %s", to, issue)
		}
		root = q.Comment.ID
		if q.Comment.Parent != nil && q.Comment.Parent.ID != "" {
			root = q.Comment.Parent.ID
		}
	case env.Type == "comment":
		root = firstOf(env.Parent, env.Message)
	}
	input := map[string]any{"issueId": issue, "body": body}
	if root != "" {
		input["parentId"] = root
	}
	var m struct {
		CommentCreate struct {
			Success bool `json:"success"`
			Comment struct {
				URL string `json:"url"`
			} `json:"comment"`
		} `json:"commentCreate"`
	}
	err = linearGraphQL(ctx, cred, `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success comment { id url } } }`,
		map[string]any{"input": input}, &m)
	if err != nil {
		return "", err
	}
	if !m.CommentCreate.Success {
		return "", fmt.Errorf("linear: commentCreate did not succeed")
	}
	return m.CommentCreate.Comment.URL, nil
}

// Thread lists the comments of the delivery's issue, every conversation,
// oldest first; a reply names its root as parent.
func (Linear) Thread(ctx context.Context, env Envelope, cred string) ([]Message, error) {
	issue, err := linearIssue(env)
	if err != nil {
		return nil, err
	}
	type actor struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var out []Message
	var after any
	for {
		var q struct {
			Issue struct {
				Comments struct {
					Nodes []struct {
						ID        string    `json:"id"`
						Body      string    `json:"body"`
						CreatedAt time.Time `json:"createdAt"`
						URL       string    `json:"url"`
						Parent    *actor    `json:"parent"`
						User      *actor    `json:"user"`
						BotActor  *actor    `json:"botActor"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"comments"`
			} `json:"issue"`
		}
		err := linearGraphQL(ctx, cred, `query($id: String!, $after: String) {
  issue(id: $id) {
    comments(first: 100, after: $after) {
      nodes { id body createdAt url parent { id } user { id name } botActor { id name } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`, map[string]any{"id": issue, "after": after}, &q)
		if err != nil {
			return nil, err
		}
		for _, n := range q.Issue.Comments.Nodes {
			m := Message{ID: n.ID, At: n.CreatedAt, Body: n.Body, URL: n.URL}
			if n.Parent != nil {
				m.Parent = n.Parent.ID
			}
			// A comment an app posted as itself has a bot actor, no user.
			if a := n.User; a != nil {
				m.Author = Party{ID: a.ID, Name: a.Name}
			} else if a := n.BotActor; a != nil {
				m.Author = Party{ID: a.ID, Name: a.Name}
			}
			out = append(out, m)
		}
		if !q.Issue.Comments.PageInfo.HasNextPage || q.Issue.Comments.PageInfo.EndCursor == "" {
			break
		}
		after = q.Issue.Comments.PageInfo.EndCursor
	}
	sortMessages(out)
	return out, nil
}

// GitHub

// Credential is GITHUB_TOKEN, else what the gh CLI is logged in with.
func (GitHub) Credential(ctx context.Context) (string, error) {
	if v := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); v != "" {
		return v, nil
	}
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if tok := strings.TrimSpace(string(out)); err == nil && tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("GITHUB_TOKEN is not set in this environment and `gh auth token` gave none: answering in a source takes your own credential (a resident's env_file sets it)")
}

func githubHeader(cred string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+cred)
	h.Set("Accept", "application/vnd.github+json")
	h.Set("X-GitHub-Api-Version", "2022-11-28")
	return h
}

// githubComments is the comments endpoint of the delivery's issue or pull
// request (a pull request's conversation is its issue's).
func githubComments(env Envelope) (string, error) {
	i := strings.LastIndexByte(env.Thread.ID, '#')
	if i <= 0 || i == len(env.Thread.ID)-1 {
		return "", fmt.Errorf("this github %s is not about an issue or pull request, so it has no comments to answer in", env.Type)
	}
	return GitHubAPI + "/repos/" + env.Thread.ID[:i] + "/issues/" + env.Thread.ID[i+1:] + "/comments", nil
}

type githubComment struct {
	ID        int64      `json:"id"`
	Body      string     `json:"body"`
	HTMLURL   string     `json:"html_url"`
	CreatedAt time.Time  `json:"created_at"`
	User      githubUser `json:"user"`
}

// Reply comments on the delivery's issue or pull request. GitHub's issue
// comments are not threaded, so there is no conversation to pick.
func (GitHub) Reply(ctx context.Context, env Envelope, to, body, cred string) (string, error) {
	if to != "" {
		return "", fmt.Errorf("github comments are not threaded: reply without --to")
	}
	url, err := githubComments(env)
	if err != nil {
		return "", err
	}
	var c githubComment
	if err := call(ctx, http.MethodPost, url, githubHeader(cred), map[string]string{"body": body}, &c); err != nil {
		return "", fmt.Errorf("github: %w", err)
	}
	return c.HTMLURL, nil
}

// Thread lists the comments of the delivery's issue or pull request,
// oldest first.
func (GitHub) Thread(ctx context.Context, env Envelope, cred string) ([]Message, error) {
	url, err := githubComments(env)
	if err != nil {
		return nil, err
	}
	var out []Message
	for page := 1; ; page++ {
		var cs []githubComment
		if err := call(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", url, page), githubHeader(cred), nil, &cs); err != nil {
			return nil, fmt.Errorf("github: %w", err)
		}
		for _, c := range cs {
			out = append(out, Message{ID: fmt.Sprint(c.ID), At: c.CreatedAt, Body: c.Body, URL: c.HTMLURL,
				Author: Party{ID: strings.ToLower(c.User.Login), Name: c.User.Login}})
		}
		if len(cs) < 100 {
			break
		}
	}
	sortMessages(out)
	return out, nil
}

func sortMessages(ms []Message) {
	slices.SortStableFunc(ms, func(a, b Message) int { return a.At.Compare(b.At) })
}

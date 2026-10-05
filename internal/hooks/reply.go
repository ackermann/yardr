package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
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
	// conversation of message to ("" for the delivery's own), marked with
	// key (a UUID v4 the store chose for this reply: the comment's own id
	// where the source takes one, a marker in the body elsewhere), and
	// returns what it posted: at least the source's id of it and its URL.
	Reply(ctx context.Context, env Envelope, to, body, key, cred string) (Message, error)
	// Posted finds the comment an earlier Reply with key left in the
	// thread of env, or nil when there is none: a retry asks before it
	// posts, so an attempt whose answer was lost is not posted twice.
	Posted(ctx context.Context, env Envelope, key, cred string) (*Message, error)
	// Thread lists the messages of the delivery's thread, oldest first.
	Thread(ctx context.Context, env Envelope, cred string) ([]Message, error)
}

// ReplyTimeout bounds one call to a source. It is well past what a source
// takes on a slow day: at the 30 s it was, a post Linear completed slowly
// was reported as failed, and the resident posted it again. A call the
// source itself gives up on fails there, within this.
const ReplyTimeout = 2 * time.Minute

// Where the outbound calls go. YARDR_LINEAR_API and YARDR_GITHUB_API, in
// the environment of the process that makes the call, name another
// endpoint: a stand-in when yardr is tried out, a GitHub Enterprise host's
// API. Tests point the variables at fake servers.
var (
	LinearAPI  = endpoint("YARDR_LINEAR_API", "https://api.linear.app/graphql")
	GitHubAPI  = endpoint("YARDR_GITHUB_API", "https://api.github.com")
	HTTPClient = &http.Client{Timeout: ReplyTimeout}
)

func endpoint(name, def string) string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv(name)), "/"); v != "" {
		return v
	}
	return def
}

// replyMarker is how a reply carries its key in a source that takes no
// client-chosen id (GitHub): an HTML comment at the end of the body,
// which renders as nothing and is found again by Posted.
func replyMarker(key string) string { return "<!-- yardr-reply " + key + " -->" }

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

// MissingCredential is what CredentialFrom fails with when nothing it was
// given sets the source's credential. Names says what would, for the caller
// to say where.
type MissingCredential struct{ Names string }

func (e *MissingCredential) Error() string { return e.Names + " is not set" }

// CredentialWay is one way a source's credential is given: the names that
// together set it in a resident's env_file, and what kind of credential
// they make.
type CredentialWay struct {
	Kind  string   `json:"kind"`
	Names []string `json:"names"`
}

// credentialWays are the ways of each source, the one preferred first.
var credentialWays = map[string][]CredentialWay{
	"linear": {{"API key", []string{"LINEAR_API_KEY"}}},
	"github": {{"GitHub App", []string{githubAppIDName, githubAppKeyName}}, {"token", []string{githubTokenName}}},
}

// CredentialWays are the ways source's credential is given, the one used
// when several are set first. They are names only: what `yardr resident
// show` and a pack's plan read of an env_file.
func CredentialWays(source string) []CredentialWay { return credentialWays[source] }

// CredentialSet is the way of source that keys, the names an env_file
// sets, give in full: the one a call would use.
func CredentialSet(source string, keys []string) (CredentialWay, bool) {
	for _, w := range credentialWays[source] {
		if !slices.ContainsFunc(w.Names, func(n string) bool { return !slices.Contains(keys, n) }) {
			return w, true
		}
	}
	return CredentialWay{}, false
}

// CredentialNames says in words what an env_file sets for source's
// credential: "LINEAR_API_KEY", or for GitHub the App's two names or
// GITHUB_TOKEN.
func CredentialNames(source string) string {
	var ways []string
	for _, w := range credentialWays[source] {
		ways = append(ways, strings.Join(w.Names, " and "))
	}
	return strings.Join(ways, ", or ")
}

func envCredential(name string) (string, error) {
	cred, err := namedCredential(name, os.Getenv)
	if err != nil {
		return "", fmt.Errorf("%s is not set in this environment: answering in a source takes your own credential (a resident's env_file sets it)", name)
	}
	return cred, nil
}

func namedCredential(name string, get func(string) string) (string, error) {
	if v := strings.TrimSpace(get(name)); v != "" {
		return v, nil
	}
	return "", &MissingCredential{Names: name}
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
		return &statusError{code: resp.StatusCode,
			msg: fmt.Sprintf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(raw[:min(len(raw), 500)])))}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: answer is not JSON: %v", method, url, err)
	}
	return nil
}

// statusError is an answer with a status other than 2xx: what call fails
// with, kept apart so a caller can tell a 404 or a 401 from the rest.
type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

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
// top-level comment. The key becomes the comment's id, so Linear itself
// refuses a second post with it.
func (Linear) Reply(ctx context.Context, env Envelope, to, body, key, cred string) (Message, error) {
	issue, err := linearIssue(env)
	if err != nil {
		return Message{}, err
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
			return Message{}, err
		}
		if q.Comment.Issue == nil || q.Comment.Issue.ID != issue {
			return Message{}, fmt.Errorf("linear comment %s is not on the delivery's issue %s", to, issue)
		}
		root = q.Comment.ID
		if q.Comment.Parent != nil && q.Comment.Parent.ID != "" {
			root = q.Comment.Parent.ID
		}
	case env.Type == "comment" && env.Message != "":
		root = firstOf(env.Parent, env.Message)
	case env.Type == "comment":
		// A delivery recorded before envelopes carried the comment's id
		// still names it in its URL; without the id the reply would land
		// at the top level, outside the conversation it answers.
		m, err := linearDelivered(ctx, env, cred)
		if err != nil {
			return Message{}, err
		}
		root = firstOf(m.Parent, m.ID)
	}
	input := map[string]any{"issueId": issue, "body": body}
	if root != "" {
		input["parentId"] = root
	}
	if key != "" {
		input["id"] = key
	}
	var m struct {
		CommentCreate struct {
			Success bool `json:"success"`
			Comment struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"comment"`
		} `json:"commentCreate"`
	}
	err = linearGraphQL(ctx, cred, `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success comment { id url } } }`,
		map[string]any{"input": input}, &m)
	if err != nil {
		return Message{}, err
	}
	if !m.CommentCreate.Success {
		return Message{}, fmt.Errorf("linear: commentCreate did not succeed")
	}
	return Message{ID: firstOf(m.CommentCreate.Comment.ID, key), Body: body, URL: m.CommentCreate.Comment.URL}, nil
}

// Posted looks the comment up by the key Reply made its id. Linear
// answers a lookup of an id it has no comment for with an error naming
// the entity, which is the "none" case; any other error is one.
func (Linear) Posted(ctx context.Context, env Envelope, key, cred string) (*Message, error) {
	issue, err := linearIssue(env)
	if err != nil {
		return nil, err
	}
	var q struct {
		Comment struct {
			ID        string    `json:"id"`
			Body      string    `json:"body"`
			URL       string    `json:"url"`
			CreatedAt time.Time `json:"createdAt"`
			Issue     *struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"comment"`
	}
	err = linearGraphQL(ctx, cred, `query($id: String!) { comment(id: $id) { id body url createdAt issue { id } } }`,
		map[string]any{"id": key}, &q)
	if err != nil {
		if strings.Contains(err.Error(), "Entity not found") {
			return nil, nil
		}
		return nil, err
	}
	if q.Comment.ID == "" || q.Comment.Issue == nil || q.Comment.Issue.ID != issue {
		return nil, nil
	}
	return &Message{ID: q.Comment.ID, Body: q.Comment.Body, URL: q.Comment.URL, At: q.Comment.CreatedAt}, nil
}

// linearDelivered finds the delivered comment of an envelope that does not
// carry its id among its issue's comments, by the URL's #comment-<id
// prefix> fragment (the slug before it changes with the issue's title).
func linearDelivered(ctx context.Context, env Envelope, cred string) (Message, error) {
	_, frag, _ := strings.Cut(env.URL, "#comment-")
	if frag == "" {
		return Message{}, fmt.Errorf("this linear comment delivery names no comment (url %q): answer with --to <message-id> (ids from yardr thread show)", env.URL)
	}
	ms, err := Linear{}.Thread(ctx, env, cred)
	if err != nil {
		return Message{}, err
	}
	var found []Message
	for _, m := range ms {
		if strings.HasPrefix(m.ID, frag) {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		return Message{}, fmt.Errorf("linear comment %s: %d comments of the issue match, not one: answer with --to <message-id> (ids from yardr thread show)", frag, len(found))
	}
	return found[0], nil
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

// Credential is the GitHub App the environment names (GITHUB_APP_ID and
// GITHUB_APP_PRIVATE_KEY_FILE), else GITHUB_TOKEN, else, for a person at
// their own shell, what the gh CLI is logged in with. A resident's session
// (its pane, which names it in YARDR_RESIDENT) never asks gh: that login is
// whoever runs the yard, and the resident would answer in their name.
func (GitHub) Credential(ctx context.Context) (string, error) {
	cred, err := githubCredential(os.Getenv)
	if missing := (*MissingCredential)(nil); !errors.As(err, &missing) {
		return cred, err
	}
	if r := config.Getenv("RESIDENT"); r != "" {
		return "", fmt.Errorf("resident %s has no GitHub credential in this environment, and a resident does not speak with the gh login of whoever runs the yard: put %s and %s (a GitHub App) or %s (a personal token) in its env_file and restart it",
			r, githubAppIDName, githubAppKeyName, githubTokenName)
	}
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if tok := strings.TrimSpace(string(out)); err == nil && tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("neither a GitHub App (%s and %s) nor %s is set in this environment and `gh auth token` gave none: answering in a source takes your own credential (a resident's env_file sets it)",
		githubAppIDName, githubAppKeyName, githubTokenName)
}

// githubHeader is the header of a call made with tok: a personal token, an
// installation token, or the App's JWT. Calls for a resident are made by
// githubCall, which works the token out of its credential.
func githubHeader(tok string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
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
// comments are not threaded, so there is no conversation to pick, and it
// takes no client-chosen id, so the key goes into the body as a marker
// (replyMarker) that renders as nothing.
func (GitHub) Reply(ctx context.Context, env Envelope, to, body, key, cred string) (Message, error) {
	if to != "" {
		return Message{}, fmt.Errorf("github comments are not threaded: reply without --to")
	}
	url, err := githubComments(env)
	if err != nil {
		return Message{}, err
	}
	if key != "" {
		body += "\n\n" + replyMarker(key)
	}
	var c githubComment
	if err := githubCall(ctx, http.MethodPost, url, cred, map[string]string{"body": body}, &c); err != nil {
		return Message{}, fmt.Errorf("github: %w", err)
	}
	return Message{ID: fmt.Sprint(c.ID), Body: c.Body, URL: c.HTMLURL, At: c.CreatedAt,
		Author: Party{ID: strings.ToLower(c.User.Login), Name: c.User.Login}}, nil
}

// Posted finds the comment carrying the key's marker among the thread's.
func (GitHub) Posted(ctx context.Context, env Envelope, key, cred string) (*Message, error) {
	ms, err := GitHub{}.Thread(ctx, env, cred)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if strings.Contains(m.Body, replyMarker(key)) {
			return &m, nil
		}
	}
	return nil, nil
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
		if err := githubCall(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", url, page), cred, nil, &cs); err != nil {
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

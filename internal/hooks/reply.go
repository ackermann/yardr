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
	"yardr.dev/yardr/internal/source"
)

// The outbound side. yard never speaks for anyone: a crew member answers a
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
	// calling process's environment (a crew member's env_file or env_command sets it).
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
// was reported as failed, and the crew member posted it again. A call the
// source itself gives up on fails there, within this.
const ReplyTimeout = 2 * time.Minute

// Where the outbound calls go. YARDR_GITHUB_API, in the environment of
// the process that makes the call, names another endpoint: a stand-in
// when yardr is tried out, a GitHub Enterprise host's API. Tests point
// the variable at a fake server.
var (
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

// ReplierFor is the source's Replier, if it can be answered in: the one
// built from d, the yard's declared source of that name, when it has
// the keys a reply takes, else the builtin one of that name. No name is
// both, so at most one applies. d is nil when the yard declares no such
// source. home is the yard's home, which a relative command program runs
// from; mint is the source's mint_command setting, "" when the yard set
// none.
func ReplierFor(source string, d *source.Declaration, home, mint string) (Replier, bool) {
	if dec, ok := declared(source, d, home, mint); ok && dec.repliable() {
		return dec, true
	}
	if r, ok := Adapters[source].(Replier); ok {
		return r, true
	}
	return nil, false
}

// Repliable lists the sources that can be answered in, sorted: the builtin
// ones, and the yard's declared sources among decls with the keys a reply
// takes.
func Repliable(decls ...*source.Declaration) []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := ReplierFor(s, nil, "", ""); ok {
			out = append(out, s)
		}
	}
	for _, d := range decls {
		if d == nil {
			continue
		}
		if _, ok := ReplierFor(d.Name, d, "", ""); ok {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// MissingCredential is what CredentialFrom fails with when nothing it was
// given sets the source's credential. Names says what would, for the caller
// to say where.
type MissingCredential struct{ Names string }

func (e *MissingCredential) Error() string { return e.Names + " is not set" }

// CredentialWay is one way a source's credential is given: the names that
// together set it in a crew member's env_file, and what kind of credential
// they make.
type CredentialWay struct {
	Kind  string   `json:"kind"`
	Names []string `json:"names"`
}

// credentialWays are the ways of each source, the one preferred first.
var credentialWays = map[string][]CredentialWay{
	"github": {{"GitHub App", []string{githubAppIDName, githubAppKeyName}}, {"token", []string{githubTokenName}}},
}

// CredentialWays are the ways source's credential is given, the one used
// when several are set first. They are names only: what `yardr crew
// show` and a pack's plan read of an env_file. d is the yard's declared
// source of that name, used when the yard has it; a declared source has
// the one way, the variable its credential.env names, and where it names
// a mint, the minted way first.
func CredentialWays(source string, d *source.Declaration) []CredentialWay {
	if d != nil && d.Name == source && d.Credential != nil {
		if d.Credential.Minted() {
			return []CredentialWay{{Kind: "minted", Names: d.Credential.Mint}, {Kind: "API key", Names: []string{d.Credential.Env}}}
		}
		return []CredentialWay{{Kind: "API key", Names: []string{d.Credential.Env}}}
	}
	if w, ok := credentialWays[source]; ok {
		return w
	}
	return nil
}

// CredentialSet is the way of source that keys, the names an env_file
// sets, give in full: the one a call would use.
func CredentialSet(source string, keys []string, d *source.Declaration) (CredentialWay, bool) {
	for _, w := range CredentialWays(source, d) {
		if !slices.ContainsFunc(w.Names, func(n string) bool { return !slices.Contains(keys, n) }) {
			return w, true
		}
	}
	return CredentialWay{}, false
}

// CredentialNames says in words what an env_file sets for source's
// credential: "LINEAR_API_KEY", or for GitHub the App's two names or
// GITHUB_TOKEN.
func CredentialNames(source string, d *source.Declaration) string {
	var ways []string
	for _, w := range CredentialWays(source, d) {
		ways = append(ways, strings.Join(w.Names, " and "))
	}
	return strings.Join(ways, ", or ")
}

// declaredCredential is the credential get sets for the declared source d:
// the static value, or the mint's inputs as a reference for the call to
// resolve with the caller's scope, by the precedence Select keeps. A name
// nothing sets fails with a *MissingCredential, for the caller to say
// where.
func declaredCredential(d *source.Declaration, get func(string) string) (string, error) {
	value, inputs, err := d.Credential.Select(d.Credential.Env, get)
	if err != nil {
		var missing *source.Missing
		if errors.As(err, &missing) {
			return "", &MissingCredential{Names: missing.Names}
		}
		return "", err
	}
	if inputs != nil {
		return source.MintRef(inputs), nil
	}
	return value, nil
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

// GitHub

// Credential is the GitHub App the environment names (GITHUB_APP_ID and
// GITHUB_APP_PRIVATE_KEY_FILE), else GITHUB_TOKEN, else, for a person at
// their own shell, what the gh CLI is logged in with. A crew member's session
// (its pane, which names it in YARDR_CREW) never asks gh: that login is
// whoever runs the yard, and the crew member would answer in their name.
func (GitHub) Credential(ctx context.Context) (string, error) {
	cred, err := githubCredential(os.Getenv)
	if missing := (*MissingCredential)(nil); !errors.As(err, &missing) {
		return cred, err
	}
	if r := config.Getenv("CREW"); r != "" {
		return "", fmt.Errorf("crew member %s has no GitHub credential in this environment, and a crew member does not speak with the gh login of whoever runs the yard: put %s and %s (a GitHub App) or %s (a personal token) in its env_file, or have its env_command print them, and restart it",
			r, githubAppIDName, githubAppKeyName, githubTokenName)
	}
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if tok := strings.TrimSpace(string(out)); err == nil && tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("neither a GitHub App (%s and %s) nor %s is set in this environment and `gh auth token` gave none: answering in a source takes your own credential (a crew member's env_file or env_command sets it)",
		githubAppIDName, githubAppKeyName, githubTokenName)
}

// githubHeader is the header of a call made with tok: a personal token, an
// installation token, or the App's JWT. Calls for a crew member are made by
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

// Declared

// Credential is the caller's credential for the source: the mint's inputs
// where the calling process's environment sets every name, else the
// variable its credential.env names. The core hands the value to every
// thread command; the commands look nothing up themselves.
func (d Declared) Credential(context.Context) (string, error) {
	cred, err := declaredCredential(d.D, os.Getenv)
	if err != nil {
		var missing *MissingCredential
		if errors.As(err, &missing) {
			return "", fmt.Errorf("%s is not set in this environment: answering in a source takes your own credential (a crew member's env_file or env_command sets it)", missing.Names)
		}
	}
	return cred, err
}

// declaredThread is the thread of the delivery env: its thread's id, or why
// a reply has no comments to answer in.
func declaredThread(source string, env Envelope) (string, error) {
	if env.Thread.ID == "" || (env.Type == "comment" && env.Thread.ID == env.Message) {
		return "", fmt.Errorf("this %s %s is not on a thread, so it has no comments to answer in", source, env.Type)
	}
	return env.Thread.ID, nil
}

// Reply posts body in the thread of the delivery env, under the
// conversation of message to ("" for the delivery's own), by
// thread.reply: in {thread, parent, body, key}, out {id, url}. The parent
// is the root of the conversation, listed to find it where the envelope
// does not carry it; the key is the comment's own id where the source
// takes one, so a retry finds it with Posted.
func (d Declared) Reply(ctx context.Context, env Envelope, to, body, key, cred string) (Message, error) {
	thread, err := declaredThread(d.D.Name, env)
	if err != nil {
		return Message{}, err
	}
	parent, err := d.replyParent(ctx, env, to, cred)
	if err != nil {
		return Message{}, err
	}
	in := map[string]any{"thread": thread, "parent": parent, "body": body, "key": key}
	var m Message
	err = d.runThread(ctx, "thread.reply", d.D.Thread.Reply, in, cred, func(out []byte) error {
		var o struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		}
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {id, url}: %v", err)
		}
		m = Message{ID: firstOf(o.ID, key), Body: body, URL: o.URL}
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	return m, nil
}

// replyParent is the parent thread.reply takes for a reply to env, under
// the conversation of to ("" for the delivery's own): the root of the
// thread's conversation. A delivery that is not a comment gets a new
// top-level comment.
func (d Declared) replyParent(ctx context.Context, env Envelope, to, cred string) (string, error) {
	switch {
	case to != "":
		ms, err := d.Thread(ctx, env, cred)
		if err != nil {
			return "", err
		}
		for _, m := range ms {
			if m.ID == to {
				return firstOf(m.Parent, m.ID), nil
			}
		}
		return "", fmt.Errorf("%s message %s is not on the delivery's thread %s", d.D.Name, to, env.Thread.ID)
	case env.Type == "comment" && env.Message != "":
		return firstOf(env.Parent, env.Message), nil
	case env.Type == "comment":
		// A delivery recorded before envelopes carried the comment's id
		// still names it in its URL; without the id the reply would land
		// at the top level, outside the conversation it answers.
		ms, err := d.Thread(ctx, env, cred)
		if err != nil {
			return "", err
		}
		_, frag, _ := strings.Cut(env.URL, "#comment-")
		if frag == "" {
			return "", fmt.Errorf("this %s comment delivery names no comment (url %q): answer with --to <message-id> (ids from yardr thread show)", d.D.Name, env.URL)
		}
		var found []Message
		for _, m := range ms {
			if strings.HasPrefix(m.ID, frag) {
				found = append(found, m)
			}
		}
		if len(found) != 1 {
			return "", fmt.Errorf("%s comment %s: %d comments of the thread match, not one: answer with --to <message-id> (ids from yardr thread show)", d.D.Name, frag, len(found))
		}
		return firstOf(found[0].Parent, found[0].ID), nil
	default:
		return "", nil
	}
}

// Posted finds the comment an earlier Reply with key left in the thread of
// env, by thread.posted: in {thread, key}, out {id, url} or {} for none.
// A retry asks before it posts, so an attempt whose answer was lost is not
// posted twice.
func (d Declared) Posted(ctx context.Context, env Envelope, key, cred string) (*Message, error) {
	thread, err := declaredThread(d.D.Name, env)
	if err != nil {
		return nil, err
	}
	in := map[string]any{"thread": thread, "key": key}
	var found *Message
	err = d.runThread(ctx, "thread.posted", d.D.Thread.Posted, in, cred, func(out []byte) error {
		var o struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		}
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {id, url}: %v", err)
		}
		if o.ID != "" {
			found = &Message{ID: o.ID, URL: o.URL}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// Thread lists the messages of the delivery's thread, oldest first, by
// thread.read: in {thread}, out {messages: [...]}, each a Message as it
// marshals.
func (d Declared) Thread(ctx context.Context, env Envelope, cred string) ([]Message, error) {
	thread, err := declaredThread(d.D.Name, env)
	if err != nil {
		return nil, err
	}
	var out []Message
	err = d.runThread(ctx, "thread.read", d.D.Thread.Read, map[string]any{"thread": thread}, cred, func(raw []byte) error {
		var o struct {
			Messages []Message `json:"messages"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return fmt.Errorf("its answer is no {messages: [...]}: %v", err)
		}
		out = o.Messages
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortMessages(out)
	return out, nil
}

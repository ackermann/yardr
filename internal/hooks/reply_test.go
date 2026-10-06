package hooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
)

// fakeLinear answers the GraphQL operations the Linear Replier sends, from
// a fixed issue with a conversation (c1, answered by c2) and a lone c3.
// Comments it creates are looked up by id afterwards, like Linear's.
type fakeLinear struct {
	mu      sync.Mutex
	auth    []string
	created []map[string]any          // commentCreate inputs
	posted  map[string]map[string]any // comments created, by id
}

const fakeIssue = "issue-1"

func (f *fakeLinear) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		var data any
		switch {
		case strings.Contains(req.Query, "commentCreate"):
			in, _ := req.Variables["input"].(map[string]any)
			f.mu.Lock()
			f.created = append(f.created, in)
			n := len(f.created)
			// The client's id when it chose one, as Linear takes it.
			id, _ := in["id"].(string)
			if id == "" {
				id = fmt.Sprint("new-", n)
			}
			c := map[string]any{"id": id, "body": in["body"], "url": fmt.Sprint("https://linear.app/x/issue/ENG-1#comment-new", n),
				"createdAt": "2026-10-03T12:00:00Z", "parent": nil, "issue": map[string]any{"id": in["issueId"]}}
			if f.posted == nil {
				f.posted = map[string]map[string]any{}
			}
			f.posted[id] = c
			f.mu.Unlock()
			data = map[string]any{"commentCreate": map[string]any{"success": true,
				"comment": map[string]any{"id": id, "url": c["url"]}}}
		case strings.Contains(req.Query, "comment(id"):
			parents := map[string]any{"c1": nil, "c2": map[string]any{"id": "c1"}, "c3": nil}
			id, _ := req.Variables["id"].(string)
			p, ok := parents[id]
			f.mu.Lock()
			c, posted := f.posted[id]
			f.mu.Unlock()
			switch {
			case ok:
				data = map[string]any{"comment": map[string]any{"id": id, "parent": p, "issue": map[string]any{"id": fakeIssue}}}
			case posted:
				data = map[string]any{"comment": c}
			default:
				json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "Entity not found: Comment - Could not find referenced Comment."}}})
				return
			}
		case strings.Contains(req.Query, "comments("):
			if req.Variables["id"] != fakeIssue {
				t.Errorf("thread asked for issue %v", req.Variables["id"])
			}
			// Two pages, the second holding the oldest comment, so the
			// result must be sorted rather than taken in page order.
			if req.Variables["after"] == nil {
				data = issueComments([]any{
					comment("c2", "c1", "Jane", "2026-10-03T10:05:00Z", "on it"),
					comment("c3", "", "", "2026-10-03T11:00:00Z", "separately"),
				}, true)
			} else {
				data = issueComments([]any{comment("c1", "", "Ann", "2026-10-03T10:00:00Z", "@yardmaster look?")}, false)
			}
		default:
			t.Errorf("unexpected query %s", req.Query)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	old := hooks.LinearAPI
	hooks.LinearAPI = srv.URL
	t.Cleanup(func() { hooks.LinearAPI = old })
}

func comment(id, parent, user, at, body string) map[string]any {
	c := map[string]any{"id": id, "body": body, "createdAt": at, "url": "https://linear.app/x/issue/ENG-1#comment-" + id,
		"parent": nil, "user": nil, "botActor": nil}
	if parent != "" {
		c["parent"] = map[string]any{"id": parent}
	}
	if user != "" {
		c["user"] = map[string]any{"id": "u-" + strings.ToLower(user), "name": user}
	} else {
		c["botActor"] = map[string]any{"id": "bot", "name": "Yardmaster (app)"}
	}
	return c
}

func issueComments(nodes []any, more bool) map[string]any {
	return map[string]any{"issue": map[string]any{"comments": map[string]any{"nodes": nodes,
		"pageInfo": map[string]any{"hasNextPage": more, "endCursor": "cur-1"}}}}
}

func linearComment(id, parent string) hooks.Envelope {
	return hooks.Envelope{Source: "linear", Type: "comment", Thread: hooks.Thread{ID: fakeIssue}, Message: id, Parent: parent}
}

// oldLinearComment is a comment delivery as envelopes were before they
// carried the comment's id and parent: only its URL names it.
func oldLinearComment(url string) hooks.Envelope {
	return hooks.Envelope{Source: "linear", Type: "comment", Thread: hooks.Thread{ID: fakeIssue}, URL: url}
}

// A reply goes under the root of the conversation it answers: a top-level
// comment is its own root, a reply's root is its parent, and --to resolves
// the root of the message it names through the API.
func TestLinearReplyUnderRoot(t *testing.T) {
	f := &fakeLinear{}
	f.serve(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		env  hooks.Envelope
		to   string
		want any // parentId, nil for a top-level comment
	}{
		{"top-level comment", linearComment("c1", ""), "", "c1"},
		{"reply", linearComment("c2", "c1"), "", "c1"},
		{"to a reply", linearComment("c3", ""), "c2", "c1"},
		{"to a top-level comment", linearComment("c2", "c1"), "c3", "c3"},
		{"issue", hooks.Envelope{Source: "linear", Type: "issue", Thread: hooks.Thread{ID: fakeIssue}}, "", nil},
		// Recorded before envelopes carried the comment's id: the comment
		// is found by its URL's fragment, even after the issue's slug
		// changed (yard-npq6).
		{"old reply", oldLinearComment("https://linear.app/x/issue/ENG-1-renamed#comment-c2"), "", "c1"},
		{"old top-level comment", oldLinearComment("https://linear.app/x/issue/ENG-1#comment-c3"), "", "c3"},
	} {
		n := len(f.created)
		m, err := hooks.Linear{}.Reply(ctx, c.env, c.to, "answer: "+c.name, "", "lin_api_k")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		url := m.URL
		if len(f.created) != n+1 {
			t.Fatalf("%s: %d comments created", c.name, len(f.created)-n)
		}
		in := f.created[n]
		if in["parentId"] != c.want || in["issueId"] != fakeIssue || in["body"] != "answer: "+c.name {
			t.Errorf("%s: commentCreate input %v, want parentId %v", c.name, in, c.want)
		}
		if !strings.HasPrefix(url, "https://linear.app/x/issue/ENG-1#comment-new") || m.ID != fmt.Sprint("new-", n+1) {
			t.Errorf("%s: url %q, id %q", c.name, url, m.ID)
		}
	}
	// A personal key goes as it is, anything else as a bearer token.
	if _, err := (hooks.Linear{}).Reply(ctx, linearComment("c1", ""), "", "x", "", "oauth-tok"); err != nil {
		t.Fatal(err)
	}
	if got := f.auth[len(f.auth)-1]; got != "Bearer oauth-tok" {
		t.Errorf("oauth auth header %q", got)
	}
	if got := f.auth[0]; got != "lin_api_k" {
		t.Errorf("api key auth header %q", got)
	}
}

// What Linear refuses comes back as the error, and nothing is posted.
func TestLinearReplyErrors(t *testing.T) {
	f := &fakeLinear{}
	f.serve(t)
	ctx := context.Background()
	if _, err := (hooks.Linear{}).Reply(ctx, linearComment("c1", ""), "nope", "x", "", "k"); err == nil || !strings.Contains(err.Error(), "Entity not found") {
		t.Fatalf("unknown --to: %v", err)
	}
	// A comment on something other than an issue is its own thread.
	odd := hooks.Envelope{Source: "linear", Type: "comment", Thread: hooks.Thread{ID: "c9"}, Message: "c9"}
	if _, err := (hooks.Linear{}).Reply(ctx, odd, "", "x", "", "k"); err == nil || !strings.Contains(err.Error(), "not on an issue") {
		t.Fatalf("comment off an issue: %v", err)
	}
	// An old comment delivery whose comment cannot be told is refused, not
	// answered at the top level.
	for url, want := range map[string]string{
		"https://linear.app/x/issue/ENG-1":            "names no comment",
		"https://linear.app/x/issue/ENG-1#comment-c9": "0 comments of the issue match",
		"https://linear.app/x/issue/ENG-1#comment-c":  "3 comments of the issue match",
	} {
		if _, err := (hooks.Linear{}).Reply(ctx, oldLinearComment(url), "", "x", "", "k"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", url, err, want)
		}
	}
	if len(f.created) != 0 {
		t.Fatalf("posted %v", f.created)
	}
}

// The key a reply is made with becomes the comment's id in Linear, and
// Posted finds the comment by it afterwards: on the delivery's issue, or
// not at all.
func TestLinearReplyKey(t *testing.T) {
	f := &fakeLinear{}
	f.serve(t)
	ctx := context.Background()
	env := linearComment("c2", "c1")
	const key = "9d4c3b2a-1f0e-4d5c-8b7a-6f5e4d3c2b1a"
	if found, err := (hooks.Linear{}).Posted(ctx, env, key, "k"); err != nil || found != nil {
		t.Fatalf("before posting: %+v, %v", found, err)
	}
	m, err := hooks.Linear{}.Reply(ctx, env, "", "answer", key, "k")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != key || f.created[0]["id"] != key || f.created[0]["parentId"] != "c1" {
		t.Fatalf("reply %+v, created %v", m, f.created)
	}
	found, err := hooks.Linear{}.Posted(ctx, env, key, "k")
	if err != nil || found == nil || found.ID != key || found.URL != m.URL || found.Body != "answer" {
		t.Fatalf("after posting: %+v, %v", found, err)
	}
	// On another issue the comment is not this delivery's answer.
	other := hooks.Envelope{Source: "linear", Type: "issue", Thread: hooks.Thread{ID: "issue-2"}}
	if found, err := (hooks.Linear{}).Posted(ctx, other, key, "k"); err != nil || found != nil {
		t.Fatalf("other issue: %+v, %v", found, err)
	}
	if _, err := (hooks.Linear{}).Posted(ctx, env, key, "lin_api_k"); err != nil {
		t.Fatal(err)
	}
	if got := f.auth[len(f.auth)-1]; got != "lin_api_k" {
		t.Errorf("lookup auth header %q", got)
	}
}

func TestLinearThread(t *testing.T) {
	(&fakeLinear{}).serve(t)
	got, err := hooks.Linear{}.Thread(context.Background(), linearComment("c2", "c1"), "k")
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	want := []hooks.Message{
		{ID: "c1", Author: hooks.Party{ID: "u-ann", Name: "Ann"}, At: at("2026-10-03T10:00:00Z"), Body: "@yardmaster look?",
			URL: "https://linear.app/x/issue/ENG-1#comment-c1"},
		{ID: "c2", Parent: "c1", Author: hooks.Party{ID: "u-jane", Name: "Jane"}, At: at("2026-10-03T10:05:00Z"), Body: "on it",
			URL: "https://linear.app/x/issue/ENG-1#comment-c2"},
		{ID: "c3", Author: hooks.Party{ID: "bot", Name: "Yardmaster (app)"}, At: at("2026-10-03T11:00:00Z"), Body: "separately",
			URL: "https://linear.app/x/issue/ENG-1#comment-c3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

// fakeGitHub keeps the comments of acme/yardr#23.
func fakeGitHub(t *testing.T, comments *[]map[string]any) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-tok" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/repos/acme/yardr/issues/23/comments" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var in struct {
				Body string `json:"body"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			id := 100 + len(*comments)
			c := map[string]any{"id": id, "body": in.Body, "created_at": "2026-10-03T12:00:00Z",
				"html_url": fmt.Sprint("https://github.com/acme/yardr/issues/23#issuecomment-", id), "user": map[string]any{"login": "Yardmaster"}}
			*comments = append(*comments, c)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(c)
		case http.MethodGet:
			if r.URL.Query().Get("page") != "1" {
				w.Write([]byte("[]"))
				return
			}
			json.NewEncoder(w).Encode(*comments)
		}
	}))
	t.Cleanup(srv.Close)
	old := hooks.GitHubAPI
	hooks.GitHubAPI = srv.URL
	t.Cleanup(func() { hooks.GitHubAPI = old })
}

func TestGitHubReplyAndThread(t *testing.T) {
	comments := []map[string]any{{"id": 10, "body": "Reproduced.", "created_at": "2026-10-03T09:00:00Z",
		"html_url": "https://github.com/acme/yardr/issues/23#issuecomment-10", "user": map[string]any{"login": "Jane-Doe"}}}
	fakeGitHub(t, &comments)
	ctx := context.Background()
	env := hooks.Envelope{Source: "github", Type: "issue_comment", Thread: hooks.Thread{ID: "acme/yardr#23"}, Message: "10"}

	m, err := hooks.GitHub{}.Reply(ctx, env, "", "On it.", "", "gh-tok")
	if err != nil {
		t.Fatal(err)
	}
	if m.URL != "https://github.com/acme/yardr/issues/23#issuecomment-101" || m.ID != "101" || len(comments) != 2 || comments[1]["body"] != "On it." {
		t.Fatalf("reply %+v, comments %v", m, comments)
	}
	got, err := hooks.GitHub{}.Thread(ctx, env, "gh-tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "10" || got[0].Author != (hooks.Party{ID: "jane-doe", Name: "Jane-Doe"}) ||
		got[1].ID != "101" || got[1].Body != "On it." || got[1].At.IsZero() {
		t.Errorf("thread %+v", got)
	}

	// Comments are not threaded, a delivery without an issue or pull
	// request has none, and what GitHub refuses is the error.
	if _, err := (hooks.GitHub{}).Reply(ctx, env, "10", "x", "", "gh-tok"); err == nil || !strings.Contains(err.Error(), "not threaded") {
		t.Errorf("--to: %v", err)
	}
	run := hooks.Envelope{Source: "github", Type: "workflow_run"}
	if _, err := (hooks.GitHub{}).Reply(ctx, run, "", "x", "", "gh-tok"); err == nil || !strings.Contains(err.Error(), "not about an issue") {
		t.Errorf("no thread: %v", err)
	}
	if _, err := (hooks.GitHub{}).Reply(ctx, env, "", "x", "", "wrong"); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("bad token: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("refused replies posted: %v", comments)
	}
}

// GitHub takes no client-chosen id, so the key rides in the body as an
// HTML comment, which renders as nothing, and Posted finds the comment by
// it among the thread's.
func TestGitHubReplyKey(t *testing.T) {
	comments := []map[string]any{}
	fakeGitHub(t, &comments)
	ctx := context.Background()
	env := hooks.Envelope{Source: "github", Type: "issue_comment", Thread: hooks.Thread{ID: "acme/yardr#23"}, Message: "10"}
	const key = "9d4c3b2a-1f0e-4d5c-8b7a-6f5e4d3c2b1a"
	if found, err := (hooks.GitHub{}).Posted(ctx, env, key, "gh-tok"); err != nil || found != nil {
		t.Fatalf("before posting: %+v, %v", found, err)
	}
	m, err := hooks.GitHub{}.Reply(ctx, env, "", "On it.", key, "gh-tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0]["body"] != "On it.\n\n<!-- yardr-reply "+key+" -->" || m.ID != "100" {
		t.Fatalf("reply %+v, comments %v", m, comments)
	}
	found, err := hooks.GitHub{}.Posted(ctx, env, key, "gh-tok")
	if err != nil || found == nil || found.ID != "100" || found.URL != m.URL {
		t.Fatalf("after posting: %+v, %v", found, err)
	}
	if found, err := (hooks.GitHub{}).Posted(ctx, env, "another-key", "gh-tok"); err != nil || found != nil {
		t.Fatalf("another key: %+v, %v", found, err)
	}
	if _, err := (hooks.GitHub{}).Posted(ctx, env, key, "wrong"); err == nil {
		t.Error("lookup with a bad token: no error")
	}
}

// token is not a source anyone can be answered in.
func TestRepliable(t *testing.T) {
	if got := hooks.Repliable(); !reflect.DeepEqual(got, []string{"github", "linear"}) {
		t.Errorf("repliable %v", got)
	}
	if _, ok := hooks.ReplierFor("token"); ok {
		t.Error("token has a replier")
	}
}

func TestCredentials(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LINEAR_API_KEY", "")
	if _, err := (hooks.Linear{}).Credential(ctx); err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY is not set") {
		t.Errorf("unset: %v", err)
	}
	t.Setenv("LINEAR_API_KEY", " lin_api_x\n")
	if got, err := (hooks.Linear{}).Credential(ctx); err != nil || got != "lin_api_x" {
		t.Errorf("set: %q %v", got, err)
	}
	// A person's own shell, with no GitHub App in it: an App goes before
	// the token.
	asCrew(t, "")
	noGitHubCredential(t)
	t.Setenv("GITHUB_TOKEN", "ghp_x")
	if got, err := (hooks.GitHub{}).Credential(ctx); err != nil || got != "ghp_x" {
		t.Errorf("github: %q %v", got, err)
	}
	// Without GITHUB_TOKEN a person's shell asks gh; with no gh on PATH
	// there is none.
	t.Setenv("GITHUB_TOKEN", "")
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if _, err := (hooks.GitHub{}).Credential(ctx); err == nil || !strings.Contains(err.Error(), "gh auth token") {
		t.Errorf("no token, no gh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\n[ \"$*\" = \"auth token\" ] && echo gho_from_gh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := (hooks.GitHub{}).Credential(ctx); err != nil || got != "gho_from_gh" {
		t.Errorf("from gh: %q %v", got, err)
	}
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	if _, err := (hooks.GitHub{}).Credential(ctx); err == nil || !strings.Contains(err.Error(), "gh auth token") {
		t.Errorf("no token, no gh: %v", err)
	}
}

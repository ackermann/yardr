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
	"testing"

	"yardr.dev/yardr/internal/hooks"
)

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
	if got := hooks.Repliable(); !reflect.DeepEqual(got, []string{"github"}) {
		t.Errorf("repliable %v", got)
	}
	if _, ok := hooks.ReplierFor("token", nil, "", ""); ok {
		t.Error("token has a replier")
	}
}

func TestCredentials(t *testing.T) {
	ctx := context.Background()
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

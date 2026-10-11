package hooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
)

const openKey = "2174add1-f7c8-44e3-bbf3-2d60b5ea8bc9"

// GitHub opens an issue in the repository, the key a marker in its body,
// and names the thread as deliveries do: by the repository's own spelling.
// An earlier attempt is found by the marker among the issues touched since.
func TestGitHubOpen(t *testing.T) {
	var issues []map[string]any
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-tok" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if !strings.EqualFold(r.URL.Path, "/repos/acme/yardr/issues") {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var in struct{ Title, Body string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			n := 40 + len(issues)
			i := map[string]any{"number": n, "title": in.Title, "body": in.Body,
				"html_url":       fmt.Sprint("https://github.com/Acme/yardr/issues/", n),
				"repository_url": "https://api.github.com/repos/Acme/yardr"}
			issues = append(issues, i)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(i)
		case http.MethodGet:
			queries = append(queries, r.URL.RawQuery)
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode(issues)
		}
	}))
	t.Cleanup(srv.Close)
	old := hooks.GitHubAPI
	hooks.GitHubAPI = srv.URL
	t.Cleanup(func() { hooks.GitHubAPI = old })
	ctx := context.Background()
	o, ok := hooks.OpenerFor("github", nil, "", "")
	if !ok || o.Place() != "repository" {
		t.Fatal("github opens no threads")
	}
	since := time.Date(2026, 10, 4, 12, 0, 30, 0, time.UTC)

	if got, err := o.Opened(ctx, "acme/yardr", openKey, "gh-tok", since); err != nil || got != nil {
		t.Fatalf("before it is opened: %+v, %v", got, err)
	}
	if q := queries[0]; !strings.Contains(q, "since=2026-10-04T11%3A59%3A30Z") || !strings.Contains(q, "state=all") {
		t.Fatalf("query: %s", q)
	}
	if _, err := o.Open(ctx, "acme/yardr", "Another", "x", "", "gh-tok"); err != nil {
		t.Fatal(err)
	}
	th, err := o.Open(ctx, "acme/yardr", "Retries", "They drop.", openKey, "gh-tok")
	if err != nil {
		t.Fatal(err)
	}
	if th.ID != "Acme/yardr#41" || th.Title != "Retries" || th.URL != "https://github.com/Acme/yardr/issues/41" {
		t.Fatalf("thread: %+v", th)
	}
	if issues[0]["body"] != "x" || issues[1]["body"] != "They drop.\n\n<!-- yardr-thread "+openKey+" -->" {
		t.Fatalf("bodies: %q, %q", issues[0]["body"], issues[1]["body"])
	}
	got, err := o.Opened(ctx, "acme/yardr", openKey, "gh-tok", since)
	if err != nil || got == nil || *got != th {
		t.Fatalf("found again: %+v, %v", got, err)
	}

	n := len(issues)
	for _, in := range []string{"yardr", "acme/yardr/issues/1/comments", "../user", "acme/yardr?x=1", ""} {
		if _, err := o.Open(ctx, in, "t", "", openKey, "gh-tok"); err == nil || !strings.Contains(err.Error(), "want owner/repo") {
			t.Fatalf("--in %q: %v", in, err)
		}
		if _, err := o.Opened(ctx, in, openKey, "gh-tok", since); err == nil {
			t.Fatalf("Opened --in %q: accepted", in)
		}
	}
	if _, err := o.Open(ctx, "acme/yardr", "t", "", openKey, "wrong"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong credential: %v", err)
	}
	if len(issues) != n {
		t.Fatalf("a refused open created an issue")
	}
}

func TestOpenable(t *testing.T) {
	if got := strings.Join(hooks.Openable(), ","); got != "github" {
		t.Fatalf("openable: %s", got)
	}
	if _, ok := hooks.OpenerFor("token", nil, "", ""); ok {
		t.Fatal("a token source opens no threads")
	}
}

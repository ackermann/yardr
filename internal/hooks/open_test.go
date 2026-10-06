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

// fakeLinearTeams answers what Linear's Opener sends: the teams, two to a
// page, issueCreate, and an issue looked up by id.
type fakeLinearTeams struct {
	created []map[string]any
	issues  map[string]map[string]any
}

func (f *fakeLinearTeams) serve(t *testing.T) {
	t.Helper()
	f.issues = map[string]map[string]any{}
	teams := []any{
		map[string]any{"id": "team-eng", "key": "ENG", "name": "Engineering"},
		map[string]any{"id": "team-ops", "key": "OPS", "name": "Yard"},
		map[string]any{"id": "team-yrd", "key": "YRD", "name": "Yard"},
		map[string]any{"id": "team-eng2", "key": "Engineering", "name": "Platform"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Header.Get("Authorization") != "lin_api_yardmaster" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "Authentication required"}}})
			return
		}
		var data any
		switch {
		case strings.Contains(req.Query, "teams("):
			from, more := 0, true
			if req.Variables["after"] == "p2" {
				from, more = 2, false
			}
			data = map[string]any{"teams": map[string]any{"nodes": teams[from : from+2],
				"pageInfo": map[string]any{"hasNextPage": more, "endCursor": "p2"}}}
		case strings.Contains(req.Query, "issueCreate"):
			in, _ := req.Variables["input"].(map[string]any)
			f.created = append(f.created, in)
			id, _ := in["id"].(string)
			if f.issues[id] != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "Issue id is not unique"}}})
				return
			}
			i := map[string]any{"id": id, "title": in["title"], "url": "https://linear.app/acme/issue/YRD-" + fmt.Sprint(len(f.created))}
			f.issues[id] = i
			data = map[string]any{"issueCreate": map[string]any{"success": true, "issue": i}}
		case strings.Contains(req.Query, "issue(id"):
			id, _ := req.Variables["id"].(string)
			i, ok := f.issues[id]
			if !ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "Entity not found: Issue"}}})
				return
			}
			data = map[string]any{"issue": i}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	old := hooks.LinearAPI
	hooks.LinearAPI = srv.URL
	t.Cleanup(func() { hooks.LinearAPI = old })
}

const openKey = "2174add1-f7c8-44e3-bbf3-2d60b5ea8bc9"

// Linear opens an issue in the team --in names, by key or by name on any
// page of the teams, with the key as the issue's id: the thread's id, and
// what an earlier attempt is found by.
func TestLinearOpen(t *testing.T) {
	f := &fakeLinearTeams{}
	f.serve(t)
	ctx := context.Background()
	o, ok := hooks.OpenerFor("linear")
	if !ok || o.Place() != "team" {
		t.Fatal("linear opens no threads")
	}

	if got, err := o.Opened(ctx, "YRD", openKey, "lin_api_yardmaster", time.Now()); err != nil || got != nil {
		t.Fatalf("before it is opened: %+v, %v", got, err)
	}
	th, err := o.Open(ctx, "yrd", "Web view", "The train.", openKey, "lin_api_yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if th.ID != openKey || th.Title != "Web view" || th.URL != "https://linear.app/acme/issue/YRD-1" {
		t.Fatalf("thread: %+v", th)
	}
	in := f.created[0]
	if in["teamId"] != "team-yrd" || in["id"] != openKey || in["title"] != "Web view" || in["description"] != "The train." {
		t.Fatalf("issueCreate input: %v", in)
	}
	got, err := o.Opened(ctx, "YRD", openKey, "lin_api_yardmaster", time.Now())
	if err != nil || got == nil || *got != th {
		t.Fatalf("found again: %+v, %v", got, err)
	}
	// Linear refuses the id a second time: nothing is opened twice even
	// by a caller that does not look first.
	if _, err := o.Open(ctx, "YRD", "Web view", "The train.", openKey, "lin_api_yardmaster"); err == nil {
		t.Fatal("the same id twice")
	}

	// A key wins over another team's name; a name works when one team has
	// it; an empty body is no description.
	for in, team := range map[string]string{"engineering": "team-eng2", "Platform": "team-eng2", "ENG": "team-eng"} {
		if _, err := o.Open(ctx, in, "t", "", "", "lin_api_yardmaster"); err != nil {
			t.Fatalf("--in %s: %v", in, err)
		}
		last := f.created[len(f.created)-1]
		if _, has := last["description"]; last["teamId"] != team || has {
			t.Fatalf("--in %s: %v", in, last)
		}
		delete(f.issues, "")
	}
	n := len(f.created)
	for in, want := range map[string]string{"Yard": "2 teams are named", "nope": "no team with key or name \"nope\""} {
		if _, err := o.Open(ctx, in, "t", "", "", "lin_api_yardmaster"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("--in %s: %v", in, err)
		}
	}
	if _, err := o.Open(ctx, "YRD", "t", "", "", "wrong"); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("wrong credential: %v", err)
	}
	if len(f.created) != n {
		t.Fatalf("a refused open created an issue: %v", f.created[n:])
	}
}

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
	o, ok := hooks.OpenerFor("github")
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
	if got := strings.Join(hooks.Openable(), ","); got != "github,linear" {
		t.Fatalf("openable: %s", got)
	}
	if _, ok := hooks.OpenerFor("token"); ok {
		t.Fatal("a token source opens no threads")
	}
}

package standin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The stand-in takes a message with the application's token, refuses one
// with another as Pushover does, and answers what it is told to while it
// is to fail. It keeps the messages and never the token.
func TestMessages(t *testing.T) {
	s := &Server{PushToken: "tok"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	post := func(body string) int {
		t.Helper()
		resp, err := http.Post(srv.URL+"/1/messages.json", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(`{"token":"tok","user":"u1","title":"mail from jo","message":"hello"}`); got != 200 {
		t.Fatalf("a message: %d", got)
	}
	if got := post(`{"token":"other","user":"u1","message":"hello"}`); got != 400 {
		t.Fatalf("another token: %d", got)
	}
	if got := post(`{"token":"tok","user":"u1"}`); got != 400 {
		t.Fatalf("no text: %d", got)
	}
	if _, err := http.PostForm(srv.URL+"/messages/answer", url.Values{"status": {"500"}}); err != nil {
		t.Fatal(err)
	}
	if got := post(`{"token":"tok","user":"u1","message":"lost"}`); got != 500 {
		t.Fatalf("while it fails: %d", got)
	}
	s.Answer(200)
	if got := post(`{"token":"tok","user":"u1","message":"again"}`); got != 200 {
		t.Fatalf("after it was told to take them again: %d", got)
	}
	ms := s.Messages()
	if len(ms) != 2 || ms[0] != (Message{User: "u1", Title: "mail from jo", Text: "hello"}) || ms[1].Text != "again" || s.Asked() != 5 {
		t.Fatalf("messages = %+v, asked %d", ms, s.Asked())
	}
}

// The customer system takes a note on a customer under the token for
// updates, once for each key: a request with the key of a note it has is
// answered with that note. A held note is written and never answered.
func TestCustomerNotes(t *testing.T) {
	s := &Server{CRMToken: "read", CRMUpdateToken: "write"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	post := func(client *http.Client, token, key, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	c := http.DefaultClient
	const notes = "/crm/customers/7/notes"
	if got, _ := post(c, "read", "", notes, `{"text":"a"}`); got != 403 {
		t.Fatalf("a note with the token of the reads: %d", got)
	}
	if got, _ := post(c, "write", "", notes, `{}`); got != 400 {
		t.Fatalf("a note without a text: %d", got)
	}
	if got, _ := post(c, "write", "", "/crm/customers/x/notes", `{"text":"a"}`); got != 404 {
		t.Fatalf("a note on no customer: %d", got)
	}
	status, first := post(c, "write", "k1", notes, `{"text":"called"}`)
	if status != 201 || !strings.Contains(first, `"id":"n1"`) {
		t.Fatalf("a note: %d %s", status, first)
	}
	if status, again := post(c, "write", "k1", notes, `{"text":"called"}`); status != 201 || again != first {
		t.Fatalf("the key again: %d %s, first %s", status, again, first)
	}
	// Without a key, and with another, each is a note of its own.
	post(c, "write", "", notes, `{"text":"called"}`)
	post(c, "write", "k2", notes, `{"text":"called"}`)

	if _, err := http.Post(srv.URL+"/notes/hold", "", nil); err != nil {
		t.Fatal(err)
	}
	if status, why := post(&http.Client{Timeout: 200 * time.Millisecond}, "write", "k3", notes, `{"text":"held"}`); status != 0 {
		t.Fatalf("a held note was answered: %d %s", status, why)
	}
	if status, body := post(c, "write", "k3", notes, `{"text":"held"}`); status != 201 || !strings.Contains(body, `"id":"n4"`) {
		t.Fatalf("the held note's key again: %d %s", status, body)
	}
	got := s.CustomerNotes()
	if len(got) != 4 || got[0] != (CustomerNote{ID: "n1", Customer: "7", Text: "called", Key: "k1"}) || got[3].Text != "held" || s.NotesAsked() != 9 {
		t.Fatalf("notes = %+v, asked %d", got, s.NotesAsked())
	}
	resp, err := http.Get(srv.URL + "/notes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if raw, _ := io.ReadAll(resp.Body); !strings.Contains(string(raw), `"asked":9`) || !strings.Contains(string(raw), `"key":"k3"`) {
		t.Fatalf("GET /notes = %s", raw)
	}
}

// A pull request's head and base, what its base branch requires from its
// protection and its rulesets, and the check runs and commit statuses on
// a commit: what the pack's pr and ci commands read. An issue is no pull
// request, and a branch or commit the stand-in never heard of is 404, as
// GitHub answers them.
func TestGitHubPullAndCI(t *testing.T) {
	s := &Server{GitHubToken: "gh-test"}
	s.Add(Issue{Source: "github", Key: "acme/webapp#7", Title: "Flow", Status: "open", Pull: true,
		Head: "4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b", Base: "main",
		Required: []string{"build"}, Rules: []string{"lint"},
		Runs:     []CheckRun{{Name: "build", Status: "completed", Conclusion: "success", URL: "https://ci.test/runs/5"}},
		Statuses: []CommitStatus{{Context: "deploy", State: "pending"}}})
	s.Add(Issue{Source: "github", Key: "acme/webapp#8", Title: "Bug", Status: "open"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	get := func(path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer gh-test")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, body := get("/repos/acme/webapp/pulls/7"); code != 200 ||
		!strings.Contains(body, `"sha":"4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"`) ||
		!strings.Contains(body, `"ref":"main"`) || !strings.Contains(body, `"state":"open"`) {
		t.Fatalf("pulls/7 = %d %s", code, body)
	}
	if code, _ := get("/repos/acme/webapp/pulls/8"); code != 404 {
		t.Fatalf("an issue as a pull request: %d", code)
	}
	if code, _ := get("/repos/acme/webapp/pulls/9"); code != 404 {
		t.Fatalf("no such pull request: %d", code)
	}
	if code, body := get("/repos/acme/webapp/branches/main"); code != 200 ||
		!strings.Contains(body, `"contexts":["build"]`) {
		t.Fatalf("branches/main = %d %s", code, body)
	}
	if code, _ := get("/repos/acme/webapp/branches/other"); code != 404 {
		t.Fatalf("no such branch: %d", code)
	}
	if code, body := get("/repos/acme/webapp/rules/branches/main"); code != 200 ||
		!strings.Contains(body, `"type":"required_status_checks"`) || !strings.Contains(body, `"context":"lint"`) {
		t.Fatalf("rules/branches/main = %d %s", code, body)
	}
	if code, _ := get("/repos/acme/webapp/rules/branches/other"); code != 404 {
		t.Fatalf("no such branch's rules: %d", code)
	}
	const sha = "4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"
	if code, body := get("/repos/acme/webapp/commits/" + sha + "/check-runs"); code != 200 ||
		!strings.Contains(body, `"name":"build"`) || !strings.Contains(body, `"conclusion":"success"`) {
		t.Fatalf("check-runs = %d %s", code, body)
	}
	if code, body := get("/repos/acme/webapp/commits/" + sha + "/status"); code != 200 ||
		!strings.Contains(body, `"state":"pending"`) || !strings.Contains(body, `"context":"deploy"`) {
		t.Fatalf("status = %d %s", code, body)
	}
	if code, _ := get("/repos/acme/webapp/commits/0000000000000000000000000000000000000000/check-runs"); code != 404 {
		t.Fatalf("no such commit's runs: %d", code)
	}
	want := []string{"github pull acme/webapp#7", "github branch acme/webapp/main",
		"github branch acme/webapp/other", "github rules acme/webapp/main", "github rules acme/webapp/other",
		"github check-runs acme/webapp#7", "github status acme/webapp#7"}
	if got := s.Calls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

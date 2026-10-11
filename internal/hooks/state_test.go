package hooks_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
)

// fakeGitHubIssue is one issue of Acme/webapp, #7, as the REST API serves
// and patches it.
type fakeGitHubIssue struct {
	state, reason string
	pull          bool
	patches       []string // bodies, as sent
	gets          int
}

func (f *fakeGitHubIssue) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-yardmaster" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if strings.ToLower(r.URL.Path) != "/repos/acme/webapp/issues/7" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPatch:
			raw, _ := io.ReadAll(r.Body)
			f.patches = append(f.patches, string(raw))
			var in map[string]string
			_ = json.Unmarshal(raw, &in)
			f.state, f.reason = in["state"], in["state_reason"]
			if f.state == "open" {
				f.reason = "reopened"
			}
		case http.MethodGet:
			f.gets++
		default:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		i := map[string]any{"number": 7, "title": "Retries", "state": f.state, "state_reason": nil,
			"html_url": "https://github.com/Acme/webapp/issues/7", "repository_url": "https://api.github.com/repos/Acme/webapp"}
		if f.reason != "" {
			i["state_reason"] = f.reason
		}
		if f.pull {
			i["pull_request"] = map[string]any{"url": "https://api.github.com/repos/Acme/webapp/pulls/7"}
		}
		_ = json.NewEncoder(w).Encode(i)
	}))
	t.Cleanup(srv.Close)
	old := hooks.GitHubAPI
	hooks.GitHubAPI = srv.URL
	t.Cleanup(func() { hooks.GitHubAPI = old })
}

// GitHub closes an issue as completed for done and reopens a closed one for
// started; an issue already there gets no request that changes it.
func TestGitHubSetState(t *testing.T) {
	ctx := context.Background()
	thread := hooks.Thread{ID: "Acme/webapp#7", Title: "Retries", URL: "https://github.com/Acme/webapp/issues/7"}
	for _, c := range []struct {
		name                 string
		from, reason, state  string
		patch                string // "" for none
		status, was, reasons string
	}{
		{"done", "open", "", "done", `{"state":"closed","state_reason":"completed"}`, "closed", "open", "completed"},
		{"started reopens", "closed", "completed", "started", `{"state":"open"}`, "open", "closed", "reopened"},
		{"started reopens what was not planned", "closed", "not_planned", "started", `{"state":"open"}`, "open", "closed (not planned)", "reopened"},
		{"started on an open issue", "open", "", "started", "", "open", "open", ""},
		{"started on a reopened issue", "open", "reopened", "started", "", "open", "open", "reopened"},
		{"done on a closed issue", "closed", "completed", "done", "", "closed", "closed", "completed"},
		// Closed is done whatever it was closed as: the reason stays.
		{"done on one closed as not planned", "closed", "not_planned", "done", "", "closed (not planned)", "closed (not planned)", "not_planned"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeGitHubIssue{state: c.from, reason: c.reason}
			f.serve(t)
			// The thread as typed, the answer as GitHub spells it.
			got, err := hooks.GitHub{}.SetState(ctx, "acme/webapp#7", c.state, "gh-yardmaster")
			want := hooks.Stated{Thread: thread, Status: c.status, Was: c.was, Changed: c.patch != ""}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, %v\nwant %+v", got, err, want)
			}
			if c.patch == "" && len(f.patches) != 0 || c.patch != "" && (len(f.patches) != 1 || f.patches[0] != c.patch) {
				t.Fatalf("patches %q, want %q", f.patches, c.patch)
			}
			if f.reason != c.reasons {
				t.Fatalf("state_reason %q, want %q", f.reason, c.reasons)
			}
			if _, err := (hooks.GitHub{}).SetState(ctx, "Acme/webapp#7", c.state, "gh-yardmaster"); err != nil || len(f.patches) > 1 ||
				c.patch == "" && len(f.patches) != 0 {
				t.Fatalf("again: %v, patches %q", err, f.patches)
			}
		})
	}
}

func TestGitHubSetStateRefusals(t *testing.T) {
	ctx := context.Background()
	f := &fakeGitHubIssue{state: "open"}
	f.serve(t)
	for _, c := range []struct{ thread, state, cred, want string }{
		// The words GitHub has are named, and nothing is asked of it.
		{"Acme/webapp#7", "review", "gh-yardmaster", "an issue has no review status: the states it has are started and done"},
		{"Acme/webapp#7", "closed", "gh-yardmaster", `"closed" is no state`},
		{"Acme/webapp", "done", "gh-yardmaster", "want owner/repo#number"},
		{"Acme/webapp#", "done", "gh-yardmaster", "want owner/repo#number"},
		{"Acme/webapp#7/comments", "done", "gh-yardmaster", "want owner/repo#number"},
		{"../orgs/acme#7", "done", "gh-yardmaster", "want owner/repo#number"},
	} {
		if _, err := (hooks.GitHub{}).SetState(ctx, c.thread, c.state, c.cred); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	if f.gets != 0 || len(f.patches) != 0 {
		t.Fatalf("a refusal reached GitHub: %d gets, patches %q", f.gets, f.patches)
	}
	for _, c := range []struct{ thread, cred, want string }{
		{"Acme/webapp#8", "gh-yardmaster", "404"},
		{"Acme/webapp#7", "gh-other", "401"},
	} {
		if _, err := (hooks.GitHub{}).SetState(ctx, c.thread, "done", c.cred); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	// The issues endpoint would close a pull request unmerged.
	f.pull = true
	if _, err := (hooks.GitHub{}).SetState(ctx, "Acme/webapp#7", "done", "gh-yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "is a pull request") || len(f.patches) != 0 {
		t.Fatalf("a pull request: %v, patches %q", err, f.patches)
	}
}

func TestStateable(t *testing.T) {
	if got := hooks.Stateable(); !reflect.DeepEqual(got, []string{"github"}) {
		t.Fatalf("Stateable() = %v", got)
	}
	if _, ok := hooks.StaterFor("token", nil, "", ""); ok {
		t.Fatal("a token hook has no threads to set the status of")
	}
	if got := hooks.States(); !reflect.DeepEqual(got, []string{"started", "review", "done"}) {
		t.Fatalf("States() = %v", got)
	}
}

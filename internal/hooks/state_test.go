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

// fakeLinearStates answers what Linear's Stater sends: an issue with its
// status and its team's statuses, and issueUpdate. The statuses come in no
// order, as a connection's nodes may.
type fakeLinearStates struct {
	states  []map[string]any
	state   string           // the issue's status, by id
	updates []map[string]any // issueUpdate's variables, as sent
	queries int
}

func status(id, name, typ string, position float64) map[string]any {
	return map[string]any{"id": id, "name": name, "type": typ, "position": position}
}

// A team as Linear makes one, with a review status added, and a second
// status of each type further down.
func linearTeamStates(review bool) []map[string]any {
	out := []map[string]any{
		status("s-done2", "Released", "completed", 6),
		status("s-qa", "QA", "started", 4),
		status("s-done", "Done", "completed", 5),
		status("s-todo", "Todo", "unstarted", 1),
		status("s-prog", "In Progress", "started", 2),
		status("s-back", "Needs review of scope", "backlog", 0),
		status("s-cancel", "Canceled", "canceled", 7),
	}
	if review {
		out = append(out, status("s-rev2", "Review (design)", "started", 3.5), status("s-rev", "In REVIEW", "started", 3))
	}
	return out
}

func (f *fakeLinearStates) serve(t *testing.T) {
	t.Helper()
	gqlError := func(w http.ResponseWriter, msg string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": msg}}})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Header.Get("Authorization") != "lin_api_yardmaster" {
			gqlError(w, "Authentication required")
			return
		}
		if req.Variables["id"] != "issue-1" {
			gqlError(w, "Entity not found: Issue")
			return
		}
		var data any
		switch {
		case strings.Contains(req.Query, "issueUpdate"):
			f.updates = append(f.updates, req.Variables)
			in, _ := req.Variables["input"].(map[string]any)
			f.state, _ = in["stateId"].(string)
			data = map[string]any{"issueUpdate": map[string]any{"success": true}}
		case strings.Contains(req.Query, "issue(id"):
			f.queries++
			var cur any
			for _, s := range f.states {
				if s["id"] == f.state {
					cur = s
				}
			}
			data = map[string]any{"issue": map[string]any{"id": "issue-1", "title": "Web view", "url": "https://linear.app/acme/issue/YRD-1",
				"state": cur, "team": map[string]any{"states": map[string]any{"nodes": f.states}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	old := hooks.LinearAPI
	hooks.LinearAPI = srv.URL
	t.Cleanup(func() { hooks.LinearAPI = old })
}

// Linear maps each word to a status of the issue's team: the first of its
// type by position, and for review the first started one named for it. A
// team without a review status gets started, and the answer says so.
func TestLinearSetState(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name            string
		review          bool
		from, state     string
		to, status, was string
		instead         string
	}{
		{"started", true, "s-todo", "started", "s-prog", "In Progress", "Todo", ""},
		{"review", true, "s-prog", "review", "s-rev", "In REVIEW", "In Progress", ""},
		{"done", true, "s-rev", "done", "s-done", "Done", "In REVIEW", ""},
		{"started without a review status", false, "s-todo", "started", "s-prog", "In Progress", "Todo", ""},
		{"review without a review status", false, "s-todo", "review", "s-prog", "In Progress", "Todo", "started"},
		{"done without a review status", false, "s-prog", "done", "s-done", "Done", "In Progress", ""},
		// Back from review: started is the first started status, not any.
		{"started from review", true, "s-rev", "started", "s-prog", "In Progress", "In REVIEW", ""},
		{"done from another completed status", true, "s-done2", "done", "s-done", "Done", "Released", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeLinearStates{states: linearTeamStates(c.review), state: c.from}
			f.serve(t)
			got, err := hooks.Linear{}.SetState(ctx, "issue-1", c.state, "lin_api_yardmaster")
			want := hooks.Stated{Thread: hooks.Thread{ID: "issue-1", Title: "Web view", URL: "https://linear.app/acme/issue/YRD-1"},
				Status: c.status, Was: c.was, Changed: true, Instead: c.instead}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, %v\nwant %+v", got, err, want)
			}
			wantUpdate := map[string]any{"id": "issue-1", "input": map[string]any{"stateId": c.to}}
			if len(f.updates) != 1 || !reflect.DeepEqual(f.updates[0], wantUpdate) {
				t.Fatalf("updates %v, want %v", f.updates, wantUpdate)
			}

			// Asked again, the issue is there: nothing is sent to change it.
			again, err := hooks.Linear{}.SetState(ctx, "issue-1", c.state, "lin_api_yardmaster")
			want.Changed, want.Was = false, c.status
			if err != nil || !reflect.DeepEqual(again, want) {
				t.Fatalf("again %+v, %v\nwant %+v", again, err, want)
			}
			if len(f.updates) != 1 {
				t.Fatalf("an issue already there was updated: %v", f.updates)
			}
		})
	}
}

func TestLinearSetStateRefusals(t *testing.T) {
	ctx := context.Background()
	f := &fakeLinearStates{states: linearTeamStates(true), state: "s-todo"}
	f.serve(t)
	for _, c := range []struct{ thread, state, cred, want string }{
		{"issue-1", "closed", "lin_api_yardmaster", `"closed" is no state: want started, review, done`},
		{"issue-1", "", "lin_api_yardmaster", "is no state"},
		{"", "done", "lin_api_yardmaster", "no issue to set the status of"},
		{"issue-9", "done", "lin_api_yardmaster", "Entity not found"},
		{"issue-1", "done", "lin_api_other", "Authentication required"},
	} {
		if _, err := (hooks.Linear{}).SetState(ctx, c.thread, c.state, c.cred); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	// A word that is none is refused before Linear is asked anything.
	if f.queries != 0 || len(f.updates) != 0 {
		t.Fatalf("%d queries, updates %v", f.queries, f.updates)
	}
	// A team with no status of the type has nowhere to put the issue.
	f.states = []map[string]any{status("s-todo", "Todo", "unstarted", 1), status("s-prog", "In Progress", "started", 2)}
	if _, err := (hooks.Linear{}).SetState(ctx, "issue-1", "done", "lin_api_yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "no status of type completed") || len(f.updates) != 0 {
		t.Fatalf("no completed status: %v, %v", err, f.updates)
	}
}

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
	if got := hooks.Stateable(); !reflect.DeepEqual(got, []string{"github", "linear"}) {
		t.Fatalf("Stateable() = %v", got)
	}
	if _, ok := hooks.StaterFor("token"); ok {
		t.Fatal("a token hook has no threads to set the status of")
	}
	if got := hooks.States(); !reflect.DeepEqual(got, []string{"started", "review", "done"}) {
		t.Fatalf("States() = %v", got)
	}
}

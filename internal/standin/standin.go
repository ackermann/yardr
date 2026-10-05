// Package standin serves as much of Linear's and GitHub's APIs as yardr
// calls to read and set an issue's status, for tests and for trying yardr
// out with no call to the real services (YARDR_LINEAR_API and
// YARDR_GITHUB_API name it; see contrib/isolated-yard/sources).
//
// It is written from the two services' documentation, not recorded from
// them: Linear's GraphQL issue(id:), which takes an issue's id or its
// identifier (YRD-12), a team's workflow states with their types, and
// issueUpdate(input: {stateId}); GitHub's GET and PATCH
// /repos/{owner}/{repo}/issues/{number} with state and state_reason.
package standin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

// LinearState is a workflow state of the stand-in's one Linear team.
type LinearState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Position float64 `json:"position"`
}

// LinearStates are the statuses a new Linear team has, with In Review added
// as most teams do.
var LinearStates = []LinearState{
	{"s-backlog", "Backlog", "backlog", 0},
	{"s-todo", "Todo", "unstarted", 1},
	{"s-progress", "In Progress", "started", 2},
	{"s-review", "In Review", "started", 3},
	{"s-done", "Done", "completed", 4},
	{"s-canceled", "Canceled", "canceled", 5},
	{"s-duplicate", "Duplicate", "canceled", 6},
}

// Issue is an issue of either service. A Linear issue is named by ID or by
// Key (YRD-12) and is in the status Status names; a GitHub issue is named
// by Key (owner/repo#number, lowercase) and its Status is open, closed, or
// closed with another reason as "closed (not planned)".
type Issue struct {
	Source string `json:"source"` // linear, github
	ID     string `json:"id,omitempty"`
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Pull   bool   `json:"pull,omitempty"` // GitHub: a pull request
}

// Server is the stand-in. LinearKey and GitHubToken are the credentials it
// takes; any other is refused as the services refuse one.
type Server struct {
	LinearKey, GitHubToken string
	// File, when set, keeps the issues across a restart.
	File string

	mu     sync.Mutex
	issues []*Issue
	calls  []string
}

// Add adds an issue, or replaces the one with its source and key.
func (s *Server) Add(i Issue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.find(i.Source, i.Key); old != nil {
		*old = i
	} else {
		s.issues = append(s.issues, &i)
	}
	s.save()
}

// Status is the status of the issue with key ("" when there is none).
func (s *Server) Status(source, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.find(source, key); i != nil {
		return i.Status
	}
	return ""
}

// Calls lists what was asked of the stand-in, in order: "linear read
// YRD-1", "linear set YRD-1 In Progress", "github read acme/webapp#7",
// "github set acme/webapp#7 closed", and "<source> refused" for a call
// with another credential.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.calls...)
}

// Sets are the calls that changed an issue.
func (s *Server) Sets() []string {
	var out []string
	for _, c := range s.Calls() {
		if strings.Contains(c, " set ") {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) find(source, key string) *Issue {
	for _, i := range s.issues {
		if i.Source == source && (strings.EqualFold(i.Key, key) || (i.ID != "" && i.ID == key)) {
			return i
		}
	}
	return nil
}

// Load reads the issues File holds, if it exists.
func (s *Server) Load() error {
	raw, err := os.ReadFile(s.File)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(raw, &s.issues)
}

func (s *Server) save() {
	if s.File == "" {
		return
	}
	raw, _ := json.MarshalIndent(s.issues, "", "  ")
	_ = os.WriteFile(s.File, raw, 0o600)
}

// Handler serves Linear's GraphQL endpoint at /graphql, GitHub's REST API
// at /repos/, and, for whoever runs a trial, GET /issues (the issues and
// the calls made), POST /issues (add or replace one) and POST /status
// (source, key, status: a person moving an issue by hand).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", s.linear)
	mux.HandleFunc("/repos/", s.github)
	mux.HandleFunc("/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var i Issue
			if err := json.NewDecoder(r.Body).Decode(&i); err != nil || i.Source == "" || i.Key == "" {
				http.Error(w, "want an issue with source and key", http.StatusBadRequest)
				return
			}
			s.Add(i)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": s.issues, "calls": s.calls})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		i := s.find(r.FormValue("source"), r.FormValue("key"))
		if i == nil || r.FormValue("status") == "" {
			http.Error(w, "no such issue, or no status", http.StatusNotFound)
			return
		}
		i.Status = r.FormValue("status")
		s.save()
		fmt.Fprintf(w, "%s %s is %s\n", i.Source, i.Key, i.Status)
	})
	return mux
}

func (s *Server) linear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	defer s.mu.Unlock()
	gqlError := func(msg string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": msg}}})
	}
	// A personal key is sent bare, an OAuth token as a bearer token.
	if got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); got != s.LinearKey {
		s.calls = append(s.calls, "linear refused")
		gqlError("Authentication required, not authenticated")
		return
	}
	id, _ := req.Variables["id"].(string)
	i := s.find("linear", id)
	if i == nil {
		gqlError("Entity not found: Issue")
		return
	}
	state := func(by func(LinearState) bool) *LinearState {
		for _, st := range LinearStates {
			if by(st) {
				return &st
			}
		}
		return nil
	}
	var data any
	switch {
	case strings.Contains(req.Query, "issueUpdate"):
		in, _ := req.Variables["input"].(map[string]any)
		to := state(func(st LinearState) bool { return st.ID == in["stateId"] })
		if to == nil {
			gqlError("stateId: no such workflow state")
			return
		}
		i.Status = to.Name
		s.calls = append(s.calls, "linear set "+i.Key+" "+to.Name)
		s.save()
		data = map[string]any{"issueUpdate": map[string]any{"success": true}}
	case strings.Contains(req.Query, "issue(id"):
		s.calls = append(s.calls, "linear read "+i.Key)
		// A status the team does not have is one of a type of its own.
		cur := state(func(st LinearState) bool { return st.Name == i.Status })
		if cur == nil {
			cur = &LinearState{ID: "s-other", Name: i.Status, Type: "custom", Position: 9}
		}
		workspace := "acme"
		data = map[string]any{"issue": map[string]any{"id": i.ID, "title": i.Title,
			"url":   "https://linear.app/" + workspace + "/issue/" + i.Key + "/" + strings.ReplaceAll(strings.ToLower(i.Title), " ", "-"),
			"state": cur,
			"team":  map[string]any{"states": map[string]any{"nodes": LinearStates}}}}
	default:
		gqlError("the stand-in knows issue(id:) and issueUpdate only")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (s *Server) github(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
	}
	if r.Header.Get("Authorization") != "Bearer "+s.GitHubToken {
		s.calls = append(s.calls, "github refused")
		fail(http.StatusUnauthorized, "Bad credentials")
		return
	}
	// /repos/<owner>/<repo>/issues/<n>
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var i *Issue
	if len(parts) == 5 && parts[3] == "issues" {
		i = s.find("github", parts[1]+"/"+parts[2]+"#"+parts[4])
	}
	if i == nil {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.calls = append(s.calls, "github read "+i.Key)
	case http.MethodPatch:
		raw, _ := io.ReadAll(r.Body)
		var in map[string]string
		_ = json.Unmarshal(raw, &in)
		switch {
		case in["state"] == "open":
			i.Status = "open"
		case in["state"] == "closed" && (in["state_reason"] == "" || in["state_reason"] == "completed"):
			i.Status = "closed"
		case in["state"] == "closed":
			i.Status = "closed (" + strings.ReplaceAll(in["state_reason"], "_", " ") + ")"
		default:
			fail(http.StatusUnprocessableEntity, "Validation Failed")
			return
		}
		s.calls = append(s.calls, "github set "+i.Key+" "+i.Status)
		s.save()
	default:
		fail(http.StatusNotFound, "Not Found")
		return
	}
	repo, n, _ := strings.Cut(i.Key, "#")
	number, _ := strconv.Atoi(n)
	out := map[string]any{"number": number, "title": i.Title, "state": "open", "state_reason": nil,
		"html_url": "https://github.com/" + repo + "/issues/" + n, "repository_url": "https://api.github.com/repos/" + repo}
	if reason, closed := strings.CutPrefix(i.Status, "closed"); closed {
		out["state"], out["state_reason"] = "closed", "completed"
		if reason != "" {
			out["state_reason"] = strings.ReplaceAll(strings.Trim(reason, " ()"), " ", "_")
		}
	}
	if i.Pull {
		out["pull_request"] = map[string]any{"url": "https://api.github.com/repos/" + repo + "/pulls/" + n}
	}
	_ = json.NewEncoder(w).Encode(out)
}

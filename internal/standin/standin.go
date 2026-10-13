// Package standin serves as much of Linear's and GitHub's APIs as yardr
// calls to read and set an issue's status, for tests and for trying yardr
// out with no call to the real services (YARDR_GITHUB_API names it for
// GitHub; a declared source's base names it for Linear: see
// contrib/isolated-yard/sources).
//
// It is written from the two services' documentation, not recorded from
// them: Linear's GraphQL issue(id:), which takes an issue's id or its
// identifier (YRD-12), a team's workflow states with their types,
// issueUpdate(input: {stateId}), issueCreate and commentCreate with the
// id the caller chose, comment(id:), an issue's comments, and the teams
// listed or one looked up by key or name; GitHub's GET and PATCH
// /repos/{owner}/{repo}/issues/{number} with state and state_reason, an
// issue's comments (GET and POST .../comments), its labels (in the issue
// as GET gives it, and POST .../labels to add some), a pull request's head
// and base (GET .../pulls/{number}), what its base branch requires (GET
// .../branches/{branch} and GET .../rules/branches/{branch}), and the
// check runs and commit statuses on a commit (GET
// .../commits/{sha}/check-runs and .../status), and what a GitHub App
// authenticates with: GET /repos/{owner}/{repo}/installation and POST
// /app/installations/{id}/access_tokens, both as the App (a JWT signed
// with its key), and an installation token on every other call.
//
// It is also a service that takes a message for a person at POST
// /1/messages.json, for a declared source with a notify key: see Message.
//
// And it is a customer system under /crm/, for a declared source whose
// calls a session makes with yardr call: see Server.crm.
package standin

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
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

// LinearTeam is the stand-in's one Linear team: what teams lists, and what
// an issue is opened in.
var LinearTeam = struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}{ID: "t-yrd", Key: "YRD", Name: "Yardr"}

// LinearComment is a comment on a Linear issue. Parent is the id of the
// comment it answers, "" for a top-level one. By names who made it, or,
// for Bot, the app it was made as.
type LinearComment struct {
	ID     string    `json:"id"`
	Body   string    `json:"body"`
	Parent string    `json:"parent,omitempty"`
	At     time.Time `json:"at"`
	By     string    `json:"by,omitempty"`
	ByID   string    `json:"byID,omitempty"`
	Bot    bool      `json:"bot,omitempty"`
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
	Body   string `json:"body,omitempty"`
	// Assignee is whom the issue was opened assigned to: a Linear user's
	// id, a GitHub login.
	Assignee string `json:"assignee,omitempty"`
	Pull     bool   `json:"pull,omitempty"` // GitHub: a pull request
	// Labels are a GitHub issue's or pull request's, in the order added.
	Labels []string `json:"labels,omitempty"`
	// Comments are a GitHub issue's, oldest first.
	Comments []Comment `json:"comments,omitempty"`
	// LinearComments are a Linear issue's, oldest first.
	LinearComments []LinearComment `json:"linear_comments,omitempty"`
	// Head is a pull request's head commit, Base its base branch.
	Head string `json:"head,omitempty"`
	Base string `json:"base,omitempty"`
	// Required are the checks the base branch's protection requires,
	// Rules those its rulesets require.
	Required []string `json:"required,omitempty"`
	Rules    []string `json:"rules,omitempty"`
	// Runs are the check runs on Head, Statuses its commit statuses.
	Runs     []CheckRun     `json:"runs,omitempty"`
	Statuses []CommitStatus `json:"statuses,omitempty"`
}

// CheckRun is a check run on a pull request's head: its name, status
// (queued, in_progress, completed) and, when completed, its conclusion
// (success, neutral, skipped, failure, timed_out, startup_failure,
// cancelled) with where it ran.
type CheckRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

// CommitStatus is a commit status on a pull request's head: its context,
// state (pending, success, failure, error) and target.
type CommitStatus struct {
	Context string `json:"context"`
	State   string `json:"state"`
	URL     string `json:"url,omitempty"`
}

// Comment is a comment on a GitHub issue. By is the login it was made
// under: GitHubLogin for the personal token, <slug>[bot] for an App.
type Comment struct {
	ID   int64     `json:"id"`
	By   string    `json:"by"`
	Body string    `json:"body"`
	At   time.Time `json:"at"`
}

// App is a GitHub App the stand-in knows: who it is, the public half of
// its key, and where it is installed.
type App struct {
	// ID is what a JWT of the App names as its issuer: GitHub takes the
	// App's id or its client id.
	ID string
	// Slug names the App: what it does is done as <slug>[bot].
	Slug string
	// Key verifies the App's JWTs.
	Key *rsa.PublicKey
	// Repos are the repositories (owner/repo) the App is installed on; the
	// installation is the owner's, one per owner, as on GitHub.
	Repos []string
	// TokenLife is how long an installation token lives (default: an
	// hour, as GitHub's), and Now the clock it is dated by.
	TokenLife time.Duration
	Now       func() time.Time
}

// NewApp is an App with a key made for it, whose private half it writes to
// keyFile as GitHub hands an App's key out (PEM, PKCS #1), for its owner
// alone to read. The key is a throwaway: it signs for this stand-in only.
func NewApp(id, slug, keyFile string, repos ...string) (*App, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyFile, raw, 0o600); err != nil {
		return nil, err
	}
	return &App{ID: id, Slug: slug, Key: &key.PublicKey, Repos: repos}, nil
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// installation is the id of the App's installation on the owner of repo, 0
// when it is not installed on the repository.
func (a *App) installation(repo string) int64 {
	owners := []string{}
	found := int64(0)
	for _, r := range a.Repos {
		owner, _, _ := strings.Cut(strings.ToLower(r), "/")
		i := slices.Index(owners, owner)
		if i < 0 {
			owners = append(owners, owner)
			i = len(owners) - 1
		}
		if strings.EqualFold(r, repo) {
			found = int64(1001 + i)
		}
	}
	return found
}

// verify checks a JWT as GitHub does an App's: RS256, signed with the
// App's key, issued by the App, not expired, and good for no more than ten
// minutes. It answers why not, "" when it is the App's.
func (a *App) verify(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return "not a JWT"
	}
	dec := func(s string, v any) bool {
		raw, err := base64.RawURLEncoding.DecodeString(s)
		return err == nil && json.Unmarshal(raw, v) == nil
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if !dec(parts[0], &hdr) || !dec(parts[1], &claims) {
		return "not a JWT"
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || hdr.Alg != "RS256" {
		return "not signed with RS256"
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(a.Key, crypto.SHA256, sum[:], sig) != nil {
		return "the signature is not the App's key's"
	}
	now := a.now().Unix()
	switch {
	case claims.Iss != a.ID:
		return "issued by another App"
	case claims.Iat > now:
		return "issued in the future"
	case claims.Exp <= now:
		return "expired"
	case claims.Exp-claims.Iat > 11*60, claims.Exp-now > 10*60:
		return "good for more than ten minutes"
	}
	return ""
}

// installationToken is a token the stand-in gave an installation.
type installationToken struct {
	repos   []string // lowercase
	expires time.Time
}

// Server is the stand-in. LinearKey and GitHubToken are the credentials it
// takes, with the installation tokens it gave App; any other is refused as
// the services refuse one.
type Server struct {
	LinearKey, GitHubToken string
	// GitHubLogin is whose GitHubToken is (default "standin").
	GitHubLogin string
	// DropGitHubAssignees simulates GitHub accepting issue creation while
	// silently dropping assignees when the token lacks push access.
	DropGitHubAssignees bool
	// App, when set, is a GitHub App that may ask for installation tokens.
	App *App
	// File, when set, keeps the issues across a restart.
	File string
	// PushToken is the application token a message is taken with; "" takes
	// any.
	PushToken string
	// CRMToken is the bearer token the customer system under /crm/ takes;
	// "" takes any.
	CRMToken string
	// CRMUpdateToken is the bearer token a note on a customer is taken
	// with, for a source whose file names a second credential for its
	// updates (credential.update_env); "" for CRMToken.
	CRMUpdateToken string
	// Now is the clock a comment or issue it takes is dated by; time.Now
	// when nil.
	Now func() time.Time

	mu       sync.Mutex
	issues   []*Issue
	calls    []string
	tokens   map[string]installationToken
	minted   int
	messages []Message
	asked    int // requests to /1/messages.json, taken or not
	answer   int // what one is answered with, 0 for 200
	notes    []CustomerNote
	notesFor int  // requests for a note, written or not
	hold     bool // the next note is written and its answer held
}

// CustomerNote is a note the customer system took at POST
// /crm/customers/<id>/notes. Key is what the request's Idempotency-Key
// header held: a second request with the key of a note is answered with
// that note and writes nothing.
type CustomerNote struct {
	ID       string `json:"id"`
	Customer string `json:"customer"`
	Text     string `json:"text"`
	Key      string `json:"key,omitempty"`
}

// CustomerNotes are the notes written, in order, and NotesAsked how many
// requests there were for one, written or not.
func (s *Server) CustomerNotes() []CustomerNote {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CustomerNote{}, s.notes...)
}

func (s *Server) NotesAsked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notesFor
}

// HoldNote has the next note written and its answer held until the caller
// gives up: a service that took an update and whose answer was lost on the
// way, which is what a key is sent for.
func (s *Server) HoldNote() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hold = true
}

// Message is a message the stand-in took at POST /1/messages.json, whose
// JSON body has token, user, title, message, url. The token is checked
// and not kept.
type Message struct {
	User  string `json:"user"`
	Title string `json:"title,omitempty"`
	Text  string `json:"message"`
	URL   string `json:"url,omitempty"`
}

// Messages are the messages taken, in order, and Asked how many requests
// there were for one, taken or refused.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message{}, s.messages...)
}

func (s *Server) Asked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked
}

// Answer has every message from now on answered with status and not taken,
// as a service that is failing (500) or refuses (400) does; 200 takes them
// again.
func (s *Server) Answer(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = status
}

// message takes one message. It answers 400 for a token that is not the
// application's and for a message without a user or a text.
func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Message
	}
	err := json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked++
	answer := func(status int, v map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method != http.MethodPost:
		answer(http.StatusMethodNotAllowed, map[string]any{"status": 0, "errors": []string{"POST a message"}})
	case s.answer != 0 && s.answer != http.StatusOK:
		answer(s.answer, map[string]any{"status": 0, "errors": []string{"the stand-in was told to answer this"}})
	case err != nil:
		answer(http.StatusBadRequest, map[string]any{"status": 0, "errors": []string{"the body is no JSON"}})
	case s.PushToken != "" && body.Token != s.PushToken:
		answer(http.StatusBadRequest, map[string]any{"status": 0, "token": "invalid", "errors": []string{"application token is invalid"}})
	case body.User == "" || body.Text == "":
		answer(http.StatusBadRequest, map[string]any{"status": 0, "errors": []string{"user and message are required"}})
	default:
		s.messages = append(s.messages, body.Message)
		answer(http.StatusOK, map[string]any{"status": 1, "request": fmt.Sprintf("standin-%d", len(s.messages))})
	}
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

// Labels are the labels of the issue with key, in the order they were
// added.
func (s *Server) Labels(source, key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.find(source, key); i != nil {
		return slices.Clone(i.Labels)
	}
	return nil
}

// Issue is a copy of the issue with key, nil when there is none.
func (s *Server) Issue(source, key string) *Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.find(source, key); i != nil {
		out := *i
		out.Labels = slices.Clone(i.Labels)
		out.Comments = slices.Clone(i.Comments)
		out.LinearComments = slices.Clone(i.LinearComments)
		out.Required = slices.Clone(i.Required)
		out.Rules = slices.Clone(i.Rules)
		out.Runs = slices.Clone(i.Runs)
		out.Statuses = slices.Clone(i.Statuses)
		return &out
	}
	return nil
}

// LinearComments are the comments of the Linear issue with key, oldest
// first.
func (s *Server) LinearComments(key string) []LinearComment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.find("linear", key); i != nil {
		return slices.Clone(i.LinearComments)
	}
	return nil
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Calls lists what was asked of the stand-in, in order: "linear read
// YRD-1", "linear set YRD-1 In Progress", "linear comments YRD-1" for the
// issue's comments, "linear comment YRD-1" for one taken on it, "linear
// comment YRD-1 c9" for one looked up by id, "linear open YRD-3" for an
// issue taken, "linear teams" for the teams listed and "linear teams ENG"
// for one looked up, "github read acme/webapp#7",
// "github set acme/webapp#7 closed", "github label acme/webapp#7
// in-review", "github comment acme/webapp#7 by
// yardr[bot]", "github comments acme/webapp#7", "github pull acme/webapp#7"
// for a pull request read, "github branch acme/webapp/main" and "github
// rules acme/webapp/main" for what its base requires, "github check-runs
// acme/webapp#7" and "github status acme/webapp#7" for the checks on its
// head, for an App "github app installation acme/webapp", "github app token
// acme/webapp" and "github app refused: <why>", and "<source> refused" for
// a call with another credential. No call is listed with a credential in
// it.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.calls...)
}

// Sets are the calls that changed an issue.
func (s *Server) Sets() []string {
	var out []string
	for _, c := range s.Calls() {
		if strings.Contains(c, " set ") || strings.Contains(c, " label ") {
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

// crm is a customer system, for a declared source with `base: http://A/crm`
// and the header "Authorization: Bearer {credential}". It answers GET
// /crm/me (who the token is), GET /crm/customers/<id> (a customer of any id
// of digits, with a field a call's select leaves out), GET /crm/moved (a
// redirect, which yardr does not follow) and GET /crm/big (an answer of 200
// KB, over the bound of a call that raises none). And it takes a note on a
// customer: see note.
func (s *Server) crm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/crm")
	id, isCustomer := strings.CutPrefix(path, "/customers/")
	if of, ok := strings.CutSuffix(id, "/notes"); isCustomer && ok && r.Method == http.MethodPost {
		s.note(w, r, of)
		return
	}
	switch {
	case r.Method != http.MethodGet:
		w.WriteHeader(http.StatusMethodNotAllowed)
	case s.CRMToken != "" && r.Header.Get("Authorization") != "Bearer "+s.CRMToken:
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "the token is not taken"})
	case path == "/me":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "yard", "name": "The yard's service account"})
	case path == "/moved":
		w.Header().Set("Location", "/crm/me")
		w.WriteHeader(http.StatusFound)
	case path == "/big":
		_ = json.NewEncoder(w).Encode(map[string]any{"text": strings.Repeat("x", 200<<10)})
	case isCustomer && id != "" && strings.Trim(id, "0123456789") == "":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "Customer " + id, "plan": "pro", "owner": "ada", "open_tickets": 2,
			"internal_note": "a field the call's select leaves out"})
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
	}
}

// note takes POST /crm/customers/<id>/notes with a JSON body {"text": …},
// under CRMUpdateToken where one is set, and answers 201 with the note. It
// honours the Idempotency-Key header as a service that has one does: a
// request with the key of a note it took is answered with that note, and
// nothing is written. After HoldNote the note is written and the answer
// held until the caller gives up.
func (s *Server) note(w http.ResponseWriter, r *http.Request, customer string) {
	var in struct {
		Text string `json:"text"`
	}
	token := s.CRMUpdateToken
	if token == "" {
		token = s.CRMToken
	}
	s.mu.Lock()
	s.notesFor++
	s.mu.Unlock()
	switch {
	case token != "" && r.Header.Get("Authorization") != "Bearer "+token:
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "the token may not write"})
		return
	case customer == "" || strings.Trim(customer, "0123456789") != "":
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
		return
	case json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in) != nil || in.Text == "":
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "want a JSON body with a text"})
		return
	}
	key := r.Header.Get("Idempotency-Key")
	s.mu.Lock()
	var n CustomerNote
	for _, had := range s.notes {
		if key != "" && had.Key == key {
			n = had
		}
	}
	if n.ID == "" {
		n = CustomerNote{ID: fmt.Sprintf("n%d", len(s.notes)+1), Customer: customer, Text: in.Text, Key: key}
		s.notes = append(s.notes, n)
	}
	hold := s.hold
	s.hold = false
	s.mu.Unlock()
	if hold {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Minute):
		}
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": n.ID, "customer": n.Customer, "text": n.Text, "internal_note": "a field the call's select leaves out"})
}

// Handler serves Linear's GraphQL endpoint at /graphql, GitHub's REST API
// at /repos/, and, for whoever runs a trial, GET /issues (the issues and
// the calls made), POST /issues (add or replace one) and POST /status
// (source, key, status: a person moving an issue by hand). It takes a
// message for a person at POST /1/messages.json, and for a trial GET
// /messages (the messages taken, and how many were asked for) and POST
// /messages/answer (status: what the next ones are answered with). Under
// /crm/ it is a customer system (see crm), and for a trial GET /notes (the
// notes written on customers, and how many were asked for) and POST
// /notes/hold (the next note is written and its answer held).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/1/messages.json", s.message)
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": s.Messages(), "asked": s.Asked()})
	})
	mux.HandleFunc("/messages/answer", func(w http.ResponseWriter, r *http.Request) {
		status, err := strconv.Atoi(r.FormValue("status"))
		if err != nil || status < 200 || status > 599 {
			http.Error(w, "want status=<an HTTP status>", http.StatusBadRequest)
			return
		}
		s.Answer(status)
		fmt.Fprintf(w, "messages are answered %d\n", status)
	})
	mux.HandleFunc("/crm/", s.crm)
	mux.HandleFunc("/notes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"notes": s.CustomerNotes(), "asked": s.NotesAsked()})
	})
	mux.HandleFunc("/notes/hold", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST holds the answer of the next note", http.StatusMethodNotAllowed)
			return
		}
		s.HoldNote()
		fmt.Fprintln(w, "the next note is written, and its answer held until the caller gives up")
	})
	mux.HandleFunc("/graphql", s.linear)
	mux.HandleFunc("/repos/", s.github)
	mux.HandleFunc("/app/installations/", s.appToken)
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
	state := func(by func(LinearState) bool) *LinearState {
		for _, st := range LinearStates {
			if by(st) {
				return &st
			}
		}
		return nil
	}
	issueURL := func(i *Issue) string {
		return "https://linear.app/acme/issue/" + i.Key + "/" + strings.ReplaceAll(strings.ToLower(i.Title), " ", "-")
	}
	commentNode := func(i *Issue, c LinearComment) map[string]any {
		node := map[string]any{"id": c.ID, "body": c.Body, "createdAt": c.At.UTC().Format(time.RFC3339),
			"url": issueURL(i) + "#comment-" + c.ID}
		if c.Parent != "" {
			node["parent"] = map[string]any{"id": c.Parent}
		}
		switch who := map[string]any{"id": c.ByID, "name": c.By}; {
		case c.Bot:
			node["botActor"] = who
		case c.ByID != "" || c.By != "":
			node["user"] = who
		}
		return node
	}
	issueNode := func(i *Issue) map[string]any {
		// A status the team does not have is one of a type of its own.
		cur := state(func(st LinearState) bool { return st.Name == i.Status })
		if cur == nil {
			cur = &LinearState{ID: "s-other", Name: i.Status, Type: "custom", Position: 9}
		}
		nodes := []any{}
		for _, c := range i.LinearComments {
			nodes = append(nodes, commentNode(i, c))
		}
		return map[string]any{"id": i.ID, "title": i.Title, "url": issueURL(i),
			"state": cur,
			"team":  map[string]any{"states": map[string]any{"nodes": LinearStates}},
			"comments": map[string]any{"nodes": nodes,
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""}}}
	}
	findIssue := func() *Issue {
		id, _ := req.Variables["id"].(string)
		return s.find("linear", id)
	}
	var data any
	switch {
	case strings.Contains(req.Query, "issueUpdate"):
		i := findIssue()
		if i == nil {
			gqlError("Entity not found: Issue")
			return
		}
		in, _ := req.Variables["input"].(map[string]any)
		to := state(func(st LinearState) bool { return st.ID == in["stateId"] })
		if to == nil {
			gqlError("stateId: no such workflow state")
			return
		}
		i.Status = to.Name
		s.calls = append(s.calls, "linear set "+i.Key+" "+to.Name)
		s.save()
		data = map[string]any{"issueUpdate": map[string]any{"success": true,
			"issue": map[string]any{"state": map[string]any{"name": to.Name}}}}
	case strings.Contains(req.Query, "commentCreate"):
		in, _ := req.Variables["input"].(map[string]any)
		issueID, _ := in["issueId"].(string)
		i := s.find("linear", issueID)
		if i == nil {
			gqlError("Entity not found: Issue")
			return
		}
		parent, _ := in["parentId"].(string)
		if parent != "" {
			on := false
			for _, c := range i.LinearComments {
				on = on || c.ID == parent
			}
			if !on {
				gqlError("Entity not found: Comment")
				return
			}
		}
		id, _ := in["id"].(string)
		if id == "" {
			id = fmt.Sprintf("c-%d", len(i.LinearComments)+1)
		}
		body, _ := in["body"].(string)
		c := LinearComment{ID: id, Body: body, Parent: parent, At: s.now(), By: "Standin", ByID: "u-standin"}
		i.LinearComments = append(i.LinearComments, c)
		s.calls = append(s.calls, "linear comment "+i.Key)
		s.save()
		data = map[string]any{"commentCreate": map[string]any{"success": true,
			"comment": map[string]any{"id": id, "url": issueURL(i) + "#comment-" + id}}}
	case strings.Contains(req.Query, "issueCreate"):
		in, _ := req.Variables["input"].(map[string]any)
		teamID, _ := in["teamId"].(string)
		if teamID != LinearTeam.ID {
			gqlError("Entity not found: Team")
			return
		}
		id, _ := in["id"].(string)
		if id == "" {
			id = fmt.Sprintf("issue-%d", len(s.issues)+1)
		}
		n := 0
		for _, x := range s.issues {
			if x.Source == "linear" {
				n++
			}
		}
		title, _ := in["title"].(string)
		desc, _ := in["description"].(string)
		assignee, _ := in["assigneeId"].(string)
		i := &Issue{Source: "linear", ID: id, Key: LinearTeam.Key + "-" + strconv.Itoa(n+1),
			Title: title, Status: "Backlog", Body: desc, Assignee: assignee}
		s.issues = append(s.issues, i)
		s.calls = append(s.calls, "linear open "+i.Key)
		s.save()
		data = map[string]any{"issueCreate": map[string]any{"success": true,
			"issue": map[string]any{"id": i.ID, "title": i.Title, "url": issueURL(i)}}}
	case strings.Contains(req.Query, "comment(id"):
		id, _ := req.Variables["id"].(string)
		i, c := s.findComment(id)
		if c == nil {
			gqlError("Entity not found: Comment")
			return
		}
		s.calls = append(s.calls, "linear comment "+i.Key+" "+c.ID)
		node := commentNode(i, *c)
		node["issue"] = map[string]any{"id": i.ID}
		data = map[string]any{"comment": node}
	case strings.Contains(req.Query, "teams("):
		// The teams listed, or the one a filter by key or name names.
		t, _ := req.Variables["t"].(string)
		nodes := []any{}
		if t == "" {
			s.calls = append(s.calls, "linear teams")
			nodes = append(nodes, LinearTeam)
		} else {
			s.calls = append(s.calls, "linear teams "+t)
			if strings.EqualFold(LinearTeam.Key, t) || strings.EqualFold(LinearTeam.Name, t) {
				nodes = append(nodes, LinearTeam)
			}
		}
		data = map[string]any{"teams": map[string]any{"nodes": nodes,
			"pageInfo": map[string]any{"hasNextPage": false}}}
	case strings.Contains(req.Query, "comments("):
		i := findIssue()
		if i == nil {
			gqlError("Entity not found: Issue")
			return
		}
		s.calls = append(s.calls, "linear comments "+i.Key)
		data = map[string]any{"issue": issueNode(i)}
	case strings.Contains(req.Query, "issue(id"):
		i := findIssue()
		if i == nil {
			gqlError("Entity not found: Issue")
			return
		}
		s.calls = append(s.calls, "linear read "+i.Key)
		data = map[string]any{"issue": issueNode(i)}
	default:
		gqlError("the stand-in knows issue(id:), issueUpdate, issueCreate, comment(id:), commentCreate, comments and teams only")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// findComment is the Linear issue and comment with id, or nils when there
// is none.
func (s *Server) findComment(id string) (*Issue, *LinearComment) {
	for _, i := range s.issues {
		if i.Source != "linear" {
			continue
		}
		for j := range i.LinearComments {
			if i.LinearComments[j].ID == id {
				return i, &i.LinearComments[j]
			}
		}
	}
	return nil, nil
}

// asApp checks that a call is made as the App (its JWT as the bearer
// token), answering 401 when it is not.
func (s *Server) asApp(w http.ResponseWriter, r *http.Request, fail func(int, string)) bool {
	why := "this stand-in has no App"
	if s.App != nil {
		jwt, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		why = s.App.verify(jwt)
	}
	if why != "" {
		s.calls = append(s.calls, "github app refused: "+why)
		fail(http.StatusUnauthorized, "A JSON web token could not be decoded")
		return false
	}
	return true
}

func githubFail(w http.ResponseWriter) func(int, string) {
	return func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
	}
}

// appToken is POST /app/installations/<id>/access_tokens: a token for the
// installation's repositories, or for those of them the body names.
func (s *Server) appToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := githubFail(w)
	if !s.asApp(w, r, fail) {
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method != http.MethodPost || len(parts) != 4 || parts[3] != "access_tokens" {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	id, _ := strconv.ParseInt(parts[2], 10, 64)
	var in struct {
		Repositories []string `json:"repositories"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &in)
	var repos []string
	for _, repo := range s.App.Repos {
		_, name, _ := strings.Cut(repo, "/")
		named := len(in.Repositories) == 0
		for _, n := range in.Repositories {
			named = named || strings.EqualFold(n, name)
		}
		if s.App.installation(repo) == id && named {
			repos = append(repos, strings.ToLower(repo))
		}
	}
	if len(repos) == 0 {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	life := s.App.TokenLife
	if life == 0 {
		life = time.Hour
	}
	s.minted++
	tok := fmt.Sprintf("ghs_standin%d", s.minted)
	if s.tokens == nil {
		s.tokens = map[string]installationToken{}
	}
	expires := s.App.now().Add(life).UTC().Truncate(time.Second)
	s.tokens[tok] = installationToken{repos: repos, expires: expires}
	s.calls = append(s.calls, "github app token "+strings.Join(repos, ","))
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "expires_at": expires.Format(time.RFC3339)})
}

// Minted is how many installation tokens the stand-in has given.
func (s *Server) Minted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.minted
}

// Tokens are the installation tokens given, for a test that checks none of
// them shows where it must not.
func (s *Server) Tokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for t := range s.tokens {
		out = append(out, t)
	}
	return out
}

// githubCaller is the login a call to repo is made as: the personal
// token's, or the App's bot for an installation token that covers the
// repository and has not expired. "" for any other credential.
func (s *Server) githubCaller(r *http.Request, repo string) string {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return ""
	}
	if s.GitHubToken != "" && tok == s.GitHubToken {
		if s.GitHubLogin != "" {
			return s.GitHubLogin
		}
		return "standin"
	}
	if t, ok := s.tokens[tok]; ok && s.App != nil && s.App.now().Before(t.expires) && slices.Index(t.repos, strings.ToLower(repo)) >= 0 {
		return s.App.Slug + "[bot]"
	}
	return ""
}

func (s *Server) github(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := githubFail(w)
	// /repos/<owner>/<repo>/installation, /repos/<owner>/<repo>/issues,
	// /repos/<owner>/<repo>/issues/<n>, .../issues/<n>/comments and
	// .../issues/<n>/labels, /repos/<owner>/<repo>/pulls/<n>,
	// /repos/<owner>/<repo>/branches/<branch>,
	// /repos/<owner>/<repo>/rules/branches/<branch> and
	// /repos/<owner>/<repo>/commits/<sha>/check-runs and .../status
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	repo := ""
	if len(parts) >= 3 {
		repo = parts[1] + "/" + parts[2]
	}
	if len(parts) == 4 && parts[3] == "installation" && r.Method == http.MethodGet {
		if !s.asApp(w, r, fail) {
			return
		}
		s.calls = append(s.calls, "github app installation "+repo)
		id := s.App.installation(repo)
		if id == 0 {
			fail(http.StatusNotFound, "Not Found")
			return
		}
		owner, _, _ := strings.Cut(repo, "/")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "app_slug": s.App.Slug, "account": map[string]string{"login": owner}})
		return
	}
	who := s.githubCaller(r, repo)
	if who == "" {
		s.calls = append(s.calls, "github refused")
		fail(http.StatusUnauthorized, "Bad credentials")
		return
	}
	if len(parts) == 4 && parts[3] == "issues" {
		s.repoIssues(w, r, repo, fail)
		return
	}
	if len(parts) == 5 && parts[3] == "pulls" {
		s.pull(w, r, repo, parts[4], fail)
		return
	}
	if len(parts) >= 5 && parts[3] == "branches" {
		// A branch may itself hold slashes, which arrive decoded.
		s.branch(w, r, repo, strings.Join(parts[4:], "/"), fail)
		return
	}
	if len(parts) >= 6 && parts[3] == "rules" && parts[4] == "branches" {
		s.branchRules(w, r, repo, strings.Join(parts[5:], "/"), fail)
		return
	}
	if len(parts) == 6 && parts[3] == "commits" {
		var h *Issue
		for _, i := range s.issues {
			if in, _, _ := strings.Cut(i.Key, "#"); i.Source == "github" && strings.EqualFold(in, repo) &&
				i.Head != "" && strings.EqualFold(i.Head, parts[4]) {
				h = i
				break
			}
		}
		if h == nil {
			fail(http.StatusNotFound, "Not Found")
			return
		}
		switch parts[5] {
		case "check-runs":
			s.checkRuns(w, r, h, fail)
			return
		case "status":
			s.commitStatus(w, r, h, fail)
			return
		}
		fail(http.StatusNotFound, "Not Found")
		return
	}
	var i *Issue
	if len(parts) >= 5 && parts[3] == "issues" {
		i = s.find("github", repo+"#"+parts[4])
	}
	if i == nil || len(parts) > 6 || (len(parts) == 6 && parts[5] != "comments" && parts[5] != "labels") {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	if len(parts) == 6 && parts[5] == "labels" {
		s.labels(w, r, i, fail)
		return
	}
	if len(parts) == 6 {
		s.comments(w, r, i, who, fail)
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
	out["labels"] = labelsOut(i)
	_ = json.NewEncoder(w).Encode(out)
}

// pull serves GET .../pulls/<n>: the pull request's head commit, base
// branch and state.
func (s *Server) pull(w http.ResponseWriter, r *http.Request, repo, n string, fail func(int, string)) {
	if r.Method != http.MethodGet {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	i := s.find("github", repo+"#"+n)
	if i == nil || !i.Pull {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	s.calls = append(s.calls, "github pull "+i.Key)
	number, _ := strconv.Atoi(n)
	state := "open"
	if strings.HasPrefix(i.Status, "closed") {
		state = "closed"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"number": number, "state": state,
		"head": map[string]string{"sha": i.Head}, "base": map[string]string{"ref": i.Base}})
}

// basedOn is the pull request of repo based on branch, or nil: the
// stand-in's branches are what its pull requests are based on.
func (s *Server) basedOn(repo, branch string) *Issue {
	for _, i := range s.issues {
		if in, _, _ := strings.Cut(i.Key, "#"); i.Source == "github" && i.Pull &&
			strings.EqualFold(in, repo) && i.Base == branch {
			return i
		}
	}
	return nil
}

// branch serves GET .../branches/<branch>: the branch's protection, with
// the checks it requires; 404 when the stand-in has no branch by that
// name, as GitHub answers a branch with no protection.
func (s *Server) branch(w http.ResponseWriter, r *http.Request, repo, branch string, fail func(int, string)) {
	if r.Method != http.MethodGet {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	s.calls = append(s.calls, "github branch "+repo+"/"+branch)
	i := s.basedOn(repo, branch)
	if i == nil {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	contexts := []string{}
	for _, c := range i.Required {
		contexts = append(contexts, c)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"name": branch,
		"protection": map[string]any{"required_status_checks": map[string]any{"contexts": contexts}}})
}

// branchRules serves GET .../rules/branches/<branch>: the required status
// checks of the rulesets that apply to the branch; 404 when the stand-in
// has no branch by that name, as GitHub answers when no ruleset applies.
func (s *Server) branchRules(w http.ResponseWriter, r *http.Request, repo, branch string, fail func(int, string)) {
	if r.Method != http.MethodGet {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	s.calls = append(s.calls, "github rules "+repo+"/"+branch)
	i := s.basedOn(repo, branch)
	if i == nil {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	out := []map[string]any{}
	if len(i.Rules) > 0 {
		checks := []map[string]string{}
		for _, c := range i.Rules {
			checks = append(checks, map[string]string{"context": c})
		}
		out = append(out, map[string]any{"type": "required_status_checks",
			"parameters": map[string]any{"required_status_checks": checks}})
	}
	_ = json.NewEncoder(w).Encode(out)
}

// checkRuns serves GET .../commits/<sha>/check-runs: the latest check run
// of each name on the commit, as the pull request headed by it carries
// them. A run that has not completed carries its conclusion as null, as
// GitHub's does.
func (s *Server) checkRuns(w http.ResponseWriter, r *http.Request, h *Issue, fail func(int, string)) {
	if r.Method != http.MethodGet {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	s.calls = append(s.calls, "github check-runs "+h.Key)
	runs := []map[string]any{}
	for _, rn := range h.Runs {
		var conclusion any
		if rn.Conclusion != "" {
			conclusion = rn.Conclusion
		}
		runs = append(runs, map[string]any{"name": rn.Name, "status": rn.Status,
			"conclusion": conclusion, "html_url": rn.URL})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(runs), "check_runs": runs})
}

// commitStatus serves GET .../commits/<sha>/status: the combined status,
// with the latest status of each context on the commit.
func (s *Server) commitStatus(w http.ResponseWriter, r *http.Request, h *Issue, fail func(int, string)) {
	if r.Method != http.MethodGet {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	s.calls = append(s.calls, "github status "+h.Key)
	// The combined state, as GitHub computes it: failure wins over
	// pending, pending over success.
	state := "pending"
	if len(h.Statuses) > 0 {
		pending, bad := false, false
		for _, st := range h.Statuses {
			switch st.State {
			case "pending":
				pending = true
			case "success":
			default:
				bad = true
			}
		}
		switch {
		case bad:
			state = "failure"
		case pending:
			state = "pending"
		default:
			state = "success"
		}
	}
	statuses := []map[string]any{}
	for _, st := range h.Statuses {
		statuses = append(statuses, map[string]any{"context": st.Context,
			"state": st.State, "target_url": st.URL})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"state": state, "statuses": statuses})
}

func labelsOut(i *Issue) []map[string]string {
	out := []map[string]string{}
	for _, l := range i.Labels {
		out = append(out, map[string]string{"name": l})
	}
	return out
}

// labels adds labels to an issue or a pull request, as POST .../labels
// does: one the issue has, in any case, is not added again, and the answer
// is every label it has now.
func (s *Server) labels(w http.ResponseWriter, r *http.Request, i *Issue, fail func(int, string)) {
	if r.Method != http.MethodPost {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	var in struct {
		Labels []string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Labels) == 0 || slices.Contains(in.Labels, "") {
		fail(http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	for _, name := range in.Labels {
		s.calls = append(s.calls, "github label "+i.Key+" "+name)
		if !slices.ContainsFunc(i.Labels, func(has string) bool { return strings.EqualFold(has, name) }) {
			i.Labels = append(i.Labels, name)
		}
	}
	s.save()
	_ = json.NewEncoder(w).Encode(labelsOut(i))
}

// comments lists an issue's comments, all on the first page, or adds one
// as who.
func (s *Server) comments(w http.ResponseWriter, r *http.Request, i *Issue, who string, fail func(int, string)) {
	repo, n, _ := strings.Cut(i.Key, "#")
	out := func(c Comment) map[string]any {
		return map[string]any{"id": c.ID, "body": c.Body, "created_at": c.At.UTC().Format(time.RFC3339),
			"html_url": fmt.Sprintf("https://github.com/%s/issues/%s#issuecomment-%d", repo, n, c.ID),
			"user":     map[string]string{"login": c.By}}
	}
	switch r.Method {
	case http.MethodGet:
		s.calls = append(s.calls, "github comments "+i.Key)
		all := []map[string]any{}
		if p := r.URL.Query().Get("page"); p == "" || p == "1" {
			for _, c := range i.Comments {
				all = append(all, out(c))
			}
		}
		_ = json.NewEncoder(w).Encode(all)
	case http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		var in struct {
			Body string `json:"body"`
		}
		if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Body) == "" {
			fail(http.StatusUnprocessableEntity, "Validation Failed")
			return
		}
		at := time.Now()
		if s.App != nil {
			at = s.App.now()
		}
		// Numbered across the issues, and on from those a restart loaded.
		id := int64(1001)
		for _, other := range s.issues {
			id += int64(len(other.Comments))
		}
		c := Comment{ID: id, By: who, Body: in.Body, At: at.UTC().Truncate(time.Second)}
		i.Comments = append(i.Comments, c)
		s.calls = append(s.calls, "github comment "+i.Key+" by "+who)
		s.save()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(out(c))
	default:
		fail(http.StatusNotFound, "Not Found")
	}
}

// repoIssues lists a repository's issues, newest first and all on the
// first page, or opens one.
func (s *Server) repoIssues(w http.ResponseWriter, r *http.Request, repo string, fail func(int, string)) {
	out := func(i *Issue) map[string]any {
		_, n, _ := strings.Cut(i.Key, "#")
		number, _ := strconv.Atoi(n)
		assignees := []map[string]string{}
		for _, login := range strings.Split(i.Assignee, ",") {
			if login != "" {
				assignees = append(assignees, map[string]string{"login": login})
			}
		}
		return map[string]any{"number": number, "title": i.Title, "body": i.Body, "assignees": assignees,
			"html_url": "https://github.com/" + repo + "/issues/" + n, "repository_url": "https://api.github.com/repos/" + repo}
	}
	var of []*Issue
	for _, i := range s.issues {
		if in, _, _ := strings.Cut(i.Key, "#"); i.Source == "github" && strings.EqualFold(in, repo) {
			of = append(of, i)
		}
	}
	switch r.Method {
	case http.MethodGet:
		s.calls = append(s.calls, "github issues "+repo)
		all := []map[string]any{}
		if p := r.URL.Query().Get("page"); p == "" || p == "1" {
			for n := len(of) - 1; n >= 0; n-- {
				all = append(all, out(of[n]))
			}
		}
		_ = json.NewEncoder(w).Encode(all)
	case http.MethodPost:
		raw, _ := io.ReadAll(r.Body)
		var in struct {
			Title     string   `json:"title"`
			Body      string   `json:"body"`
			Assignees []string `json:"assignees"`
		}
		if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Title) == "" {
			fail(http.StatusUnprocessableEntity, "Validation Failed")
			return
		}
		number := 0
		for _, i := range of {
			_, n, _ := strings.Cut(i.Key, "#")
			if v, _ := strconv.Atoi(n); v > number {
				number = v
			}
		}
		assignee := strings.Join(in.Assignees, ",")
		if s.DropGitHubAssignees {
			assignee = ""
		}
		i := &Issue{Source: "github", Key: repo + "#" + strconv.Itoa(number+1), Title: in.Title, Body: in.Body, Status: "open",
			Assignee: assignee}
		s.issues = append(s.issues, i)
		s.calls = append(s.calls, "github open "+i.Key)
		s.save()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(out(i))
	default:
		fail(http.StatusNotFound, "Not Found")
	}
}

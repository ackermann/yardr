package hooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// GitHub verifies and normalizes GitHub repository webhooks
// (https://docs.github.com/webhooks), sent as application/json:
//
//	X-Hub-Signature-256  "sha256=" + hex HMAC-SHA256 of the raw body with the secret
//	X-GitHub-Delivery    GUID of the delivery, kept by a redelivery; not signed
//	X-GitHub-Event       the event (pull_request, issues, ...); the body has the action
type GitHub struct{}

func (GitHub) Verify(r *http.Request, body, secret []byte, _ time.Time) (string, error) {
	hexSig, ok := strings.CutPrefix(strings.TrimSpace(r.Header.Get("X-Hub-Signature-256")), "sha256=")
	sig, err := hex.DecodeString(hexSig)
	if !ok || err != nil || len(sig) == 0 {
		return "", unauthorized("missing or malformed X-Hub-Signature-256")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", unauthorized("X-Hub-Signature-256 does not match")
	}
	// GitHub signs no timestamp, so a replay cannot be told by age, and the
	// delivery id is a header the signature does not cover: whoever holds a
	// captured delivery can send it again under any id. The signed body is
	// what drops a replay (Bodies).
	return r.Header.Get("X-GitHub-Delivery"), nil
}

// Bodies are unique per hook. The signature covers the body alone, so the
// body is the only thing a replay cannot change, and a body seen before is
// dropped whatever X-GitHub-Delivery it comes under. Two events do not share
// one in practice: a body carries what the event changed and the updated_at
// of what it is about, to the second. Should two do (a label added, removed
// and added again within a second), the second says nothing the first did
// not. Per hook, not per source: GitHub sends an event's body to every
// webhook on the repository, and each hook records its own.
func (GitHub) Bodies() BodyScope { return BodiesUniquePerHook }

// githubPayload is the part of the events we normalize that we read.
type githubPayload struct {
	Action      string       `json:"action"`
	Sender      *githubUser  `json:"sender"`
	Repository  *githubRepo  `json:"repository"`
	PullRequest *githubIssue `json:"pull_request"`
	Issue       *githubIssue `json:"issue"`
	Comment     *githubText  `json:"comment"`
	Review      *githubText  `json:"review"`
	WorkflowRun *githubSuite `json:"workflow_run"`
	CheckSuite  *githubSuite `json:"check_suite"`
}

type githubUser struct {
	Login string `json:"login"`
}

type githubRepo struct {
	FullName string `json:"full_name"`
	HTMLURL  string `json:"html_url"`
}

// githubIssue is an issue or a pull request: GitHub sends both alike, a pull
// request with head, base and merged besides.
type githubIssue struct {
	Number   int           `json:"number"`
	Title    string        `json:"title"`
	HTMLURL  string        `json:"html_url"`
	Body     string        `json:"body"`
	State    string        `json:"state"`
	Merged   *bool         `json:"merged"`
	Assignee *githubUser   `json:"assignee"`
	Labels   []githubLabel `json:"labels"`
	Head     *githubRef    `json:"head"`
	Base     *githubRef    `json:"base"`
}

type githubLabel struct {
	Name string `json:"name"`
}

// githubRef is a pull request's head or base. Repo is the repository the
// branch is in: for a head, the fork when the pull request comes from one,
// and null once that fork is deleted.
type githubRef struct {
	Ref  string      `json:"ref"`
	SHA  string      `json:"sha"`
	Repo *githubRepo `json:"repo"`
}

// githubText is a comment or a review.
type githubText struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
}

// githubSuite is a workflow run or a check suite.
type githubSuite struct {
	Name         string `json:"name"`
	DisplayTitle string `json:"display_title"`
	HTMLURL      string `json:"html_url"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HeadBranch   string `json:"head_branch"`
	HeadSHA      string `json:"head_sha"`
	// HeadRepository is the repository the commit was pushed to, a fork for
	// a run on a fork's branch. Only a workflow run names it.
	HeadRepository *githubRepo `json:"head_repository"`
	App            *struct {
		Name string `json:"name"`
	} `json:"app"`
	PullRequests []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
}

// Logins are lowercased in actor, assignee and mentions: GitHub logins are
// case-insensitive and a mention is typed by hand, so a crew member's github
// identity is matched by its lowercase login.
func (GitHub) Normalize(h http.Header, body []byte) (*Envelope, error) {
	event := h.Get("X-GitHub-Event")
	if event == "" {
		return nil, bad("github delivery has no X-GitHub-Event")
	}
	var p githubPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, bad("github payload: %v", err)
	}
	e := &Envelope{Source: "github", Type: event, Action: p.Action, Mentions: []string{}}
	if p.Sender != nil {
		e.Actor = Party{ID: strings.ToLower(p.Sender.Login), Name: p.Sender.Login}
	}
	repo := ""
	if p.Repository != nil {
		repo = p.Repository.FullName
	}
	f := fields{}
	f.set("event", event)
	f.set("action", p.Action)
	f.set("repo", repo)

	// item is the issue or pull request the delivery is about, when it is
	// about one; text is the delivery's own words, whose mentions count.
	var item *githubIssue
	var text string
	switch event {
	case "pull_request":
		item = p.PullRequest
		if item != nil {
			text = item.Body
			e.URL = item.HTMLURL
			// The description is re-sent whole with every action, so its
			// mentions are counted against the last one seen.
			e.Entity = thread(repo, item.Number)
		}
	case "issues":
		item = p.Issue
		if item != nil {
			text = item.Body
			e.URL = item.HTMLURL
			e.Entity = thread(repo, item.Number)
		}
	case "issue_comment":
		item = p.Issue
		if p.Comment != nil {
			text, e.URL = p.Comment.Body, p.Comment.HTMLURL
			if p.Comment.ID != 0 {
				e.Message = strconv.FormatInt(p.Comment.ID, 10)
				// An edit re-sends the comment whole, so its mentions are
				// counted against the last one seen, and a deletion
				// forgets it. Named as GitHub's own URL fragment does, apart
				// from a review's id.
				e.Entity = "issuecomment-" + e.Message
			}
		}
	case "pull_request_review":
		item = p.PullRequest
		if p.Review != nil {
			text, e.URL = p.Review.Body, p.Review.HTMLURL
			f.set("state", strings.ToLower(p.Review.State))
			if p.Review.ID != 0 {
				e.Entity = "pullrequestreview-" + strconv.FormatInt(p.Review.ID, 10)
			}
		}
	case "workflow_run", "check_suite":
		s := p.WorkflowRun
		if event == "check_suite" {
			s = p.CheckSuite
		}
		if s == nil {
			break
		}
		f.set("conclusion", s.Conclusion)
		f.set("head_branch", s.HeadBranch)
		f.set("head_sha", s.HeadSHA)
		if s.HeadRepository != nil {
			f.set("head_repo", s.HeadRepository.FullName)
		}
		f.set("train", trainOf(s.HeadBranch))
		name := firstOf(s.Name, s.DisplayTitle)
		if name == "" && s.App != nil {
			name = s.App.Name
		}
		f.set("workflow", name)
		words := nonEmpty(name, firstOf(s.Conclusion, s.Status))
		if s.HeadBranch != "" {
			words = append(words, "on", s.HeadBranch)
		}
		e.Text = truncate(strings.Join(words, " "))
		e.URL = s.HTMLURL
		if e.URL == "" && p.Repository != nil && s.HeadSHA != "" {
			e.URL = p.Repository.HTMLURL + "/commit/" + s.HeadSHA
		}
		// A run for a pull request's branch belongs to its thread, so
		// whoever takes part in the pull request hears how CI went.
		// GitHub lists the pull requests only for branches in the repo.
		if len(s.PullRequests) > 0 && s.PullRequests[0].Number > 0 {
			n := s.PullRequests[0].Number
			f.set("number", strconv.Itoa(n))
			e.Thread = Thread{ID: thread(repo, n)}
		}
	}
	if item != nil && item.Number > 0 {
		e.Thread = Thread{ID: thread(repo, item.Number), Title: item.Title, URL: item.HTMLURL}
		f.set("number", strconv.Itoa(item.Number))
		if f["state"] == nil {
			f.set("state", item.State)
		}
		if item.Merged != nil {
			f.set("merged", strconv.FormatBool(*item.Merged))
		}
		if item.Head != nil {
			f.set("head_branch", item.Head.Ref)
			f.set("head_sha", item.Head.SHA)
			// The branch name alone does not tell whose branch it is: a
			// fork's pull request has the same head_branch, and so the same
			// train, as the yard's own. head_repo is what tells them apart.
			if item.Head.Repo != nil {
				f.set("head_repo", item.Head.Repo.FullName)
			}
			f.set("train", trainOf(item.Head.Ref))
		}
		if item.Base != nil {
			f.set("base", item.Base.Ref)
		}
		for _, l := range item.Labels {
			if l.Name != "" {
				f["label"] = append(f["label"], l.Name)
			}
		}
		if item.Assignee != nil {
			e.Assignee = strings.ToLower(item.Assignee.Login)
		}
		e.Text = truncate(text)
		e.Mentions = githubMentions(text)
	}
	e.Fields = map[string][]string(f)
	return e, nil
}

type fields map[string][]string

func (f fields) set(k, v string) {
	if v != "" {
		f[k] = []string{v}
	}
}

// thread names an issue or pull request across repos: owner/name#number,
// as GitHub writes a cross-repo reference.
func thread(repo string, n int) string { return repo + "#" + strconv.Itoa(n) }

// trainOf is the train a branch carries, when it is a yardr train branch
// (store.Branch): yard/yard-sv6b -> yard-sv6b.
func trainOf(branch string) string {
	id, ok := strings.CutPrefix(branch, store.Branch(""))
	if !ok {
		return ""
	}
	return id
}

// githubMention matches @login: alphanumerics and single inner hyphens,
// not preceded by what would make it an email address or part of a path,
// and with the character after it captured to tell an @org/team apart.
var githubMention = regexp.MustCompile(`(?:^|[^A-Za-z0-9_@./-])@([A-Za-z0-9](?:-?[A-Za-z0-9])*)(/?)`)

// githubCode matches fenced code blocks and inline code spans, where GitHub
// renders an @login as text and notifies no one.
var githubCode = regexp.MustCompile("(?s)```.*?(```|$)|`[^`\n]*`")

// githubMentions lists the lowercase logins a body mentions, once each, in
// order. Team mentions (@org/team) are left out: a team is not a crew member.
func githubMentions(md string) []string {
	out := []string{}
	seen := map[string]bool{}
	md = githubCode.ReplaceAllString(md, " ")
	for _, m := range githubMention.FindAllStringSubmatch(md, -1) {
		login := strings.ToLower(m[1])
		if m[2] == "/" || len(login) > 39 || seen[login] {
			continue
		}
		seen[login] = true
		out = append(out, login)
	}
	return out
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

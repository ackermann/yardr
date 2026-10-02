package hooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

func ghFixture(t *testing.T, name string) []byte {
	t.Helper()
	return fixture(t, "github/"+name+".json")
}

// github posts body for event signed with key as GitHub would.
func (r *rig) github(t *testing.T, event string, body []byte, key, delivery string) int {
	return r.post(t, "/hooks/gh", body, map[string]string{
		"X-Hub-Signature-256": "sha256=" + sign(key, body), "X-GitHub-Delivery": delivery,
		"X-GitHub-Event": event, "Content-Type": "application/json", "User-Agent": "GitHub-Hookshot/0"})
}

func TestGitHubSignature(t *testing.T) {
	r := newRig(t)
	r.now = time.Now()
	body := ghFixture(t, "pull_request_opened")

	if code := r.github(t, "pull_request", body, "wrong-secret", "g-1"); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	if code := r.post(t, "/hooks/gh", body, map[string]string{"X-GitHub-Event": "pull_request"}); code != http.StatusUnauthorized {
		t.Fatalf("no signature: %d", code)
	}
	// The SHA-1 header GitHub also sends, or a bare hex digest, is not
	// the SHA-256 one.
	if code := r.post(t, "/hooks/gh", body, map[string]string{"X-Hub-Signature-256": sign(secret, body),
		"X-GitHub-Event": "pull_request"}); code != http.StatusUnauthorized {
		t.Fatalf("signature without sha256= prefix: %d", code)
	}
	var v any
	_ = json.Unmarshal(body, &v)
	reencoded, _ := json.Marshal(v)
	if code := r.post(t, "/hooks/gh", body, map[string]string{"X-Hub-Signature-256": "sha256=" + sign(secret, reencoded),
		"X-GitHub-Event": "pull_request"}); code != http.StatusUnauthorized {
		t.Fatalf("signature of re-encoded body: %d", code)
	}
	// Signed, but which event it is cannot be told.
	if code := r.github(t, "", body, secret, "g-0"); code != http.StatusBadRequest {
		t.Fatalf("no X-GitHub-Event: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 0 {
		t.Fatalf("refused deliveries left %d events", n)
	}

	if code := r.github(t, "pull_request", body, secret, "g-1"); code != http.StatusOK {
		t.Fatalf("good signature: %d", code)
	}
	evs := r.hookEvents(t)
	if len(evs) != 1 {
		t.Fatalf("want 1 hook event, got %d", len(evs))
	}
	e := evs[0]
	if e.Actor != "hook:gh" || e.Data["source"] != "github" || e.Data["type"] != "pull_request" ||
		e.Data["delivery"] != "g-1" {
		t.Fatalf("event: %+v", e)
	}
	if got := r.rc.Rejected()["unauthorized"]; got != 4 {
		t.Fatalf("unauthorized count %d, want 4", got)
	}
}

// GitHub keeps the delivery id when a delivery is redelivered; bodies are
// not unique, so only the id drops a repeat.
func TestGitHubDuplicateDelivery(t *testing.T) {
	r := newRig(t)
	r.now = time.Now()
	body := ghFixture(t, "issue_comment_pr")
	for i := 0; i < 3; i++ {
		if code := r.github(t, "issue_comment", body, secret, "g-dup"); code != http.StatusOK {
			t.Fatalf("delivery %d: %d", i, code)
		}
	}
	if n := len(r.hookEvents(t)); n != 1 || r.rc.Rejected()["duplicate"] != 2 {
		t.Fatalf("%d events, %v", n, r.rc.Rejected())
	}
	// The same body under a new id is another delivery.
	if code := r.github(t, "issue_comment", body, secret, "g-other"); code != http.StatusOK {
		t.Fatalf("new id: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 2 {
		t.Fatalf("same body, new id: %d events", n)
	}
	// The record is the store's: a receiver started later still drops it.
	r.rc = hooks.NewReceiver(r.own, nil)
	r.srv.Config.Handler = r.rc.Handler()
	if code := r.github(t, "issue_comment", body, secret, "g-dup"); code != http.StatusOK {
		t.Fatalf("after restart: %d", code)
	}
	if n := len(r.hookEvents(t)); n != 2 || r.rc.Rejected()["duplicate"] != 1 {
		t.Fatalf("after restart: %d events, %v", n, r.rc.Rejected())
	}
}

func TestGitHubNormalize(t *testing.T) {
	const (
		repo   = "acme/yardr"
		pr     = "acme/yardr#17"
		prURL  = "https://github.com/acme/yardr/pull/17"
		prBody = "Lands the epic. @Mayor please review; cc @jane-doe and @acme/core. Mail ops@example.com."
		sha    = "4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"
		iss    = "acme/yardr#23"
		issURL = "https://github.com/acme/yardr/issues/23"
	)
	jane := hooks.Party{ID: "jane-doe", Name: "jane-doe"}
	mayor := hooks.Party{ID: "mayor", Name: "mayor"}
	prThread := hooks.Thread{ID: pr, Title: "PR flow: open, review and land epics", URL: prURL}
	issThread := hooks.Thread{ID: iss, Title: "Hook listener logs the secret path", URL: issURL}
	prFields := func(action, state, merged string, extra ...string) map[string][]string {
		f := map[string][]string{"event": {"pull_request"}, "action": {action}, "repo": {repo}, "number": {"17"},
			"state": {state}, "merged": {merged}, "head_branch": {"yard/yard-sv6b"}, "head_sha": {sha},
			"epic": {"yard-sv6b"}, "base": {"main"}, "label": {"pr-flow"}}
		for i := 0; i+1 < len(extra); i += 2 {
			f[extra[i]] = []string{extra[i+1]}
		}
		return f
	}
	prEnv := func(action string, actor hooks.Party, state, merged string) hooks.Envelope {
		return hooks.Envelope{Source: "github", Type: "pull_request", Action: action,
			Actor: actor, Thread: prThread, Mentions: []string{"mayor", "jane-doe"}, Assignee: "mayor",
			Text: prBody, URL: prURL, Entity: pr, Fields: prFields(action, state, merged)}
	}
	review := func(state, text string, mentions ...string) hooks.Envelope {
		if mentions == nil {
			mentions = []string{}
		}
		f := prFields("submitted", state, "false")
		f["event"] = []string{"pull_request_review"}
		return hooks.Envelope{Source: "github", Type: "pull_request_review", Action: "submitted",
			Actor: mayor, Thread: prThread, Mentions: mentions, Assignee: "mayor", Text: text,
			URL: prURL + "#pullrequestreview-1", Fields: f}
	}
	issue := func(action, state string) hooks.Envelope {
		return hooks.Envelope{Source: "github", Type: "issues", Action: action, Actor: jane, Thread: issThread,
			Mentions: []string{"mayor"}, Text: "Seen on startup. @mayor can you look?", URL: issURL, Entity: iss,
			Fields: map[string][]string{"event": {"issues"}, "action": {action}, "repo": {repo}, "number": {"23"},
				"state": {state}, "label": {"bug"}}}
	}
	suite := func(event, workflow, conclusion, text, url string, number ...string) hooks.Envelope {
		f := map[string][]string{"event": {event}, "action": {"completed"}, "repo": {repo}, "conclusion": {conclusion},
			"workflow": {workflow}, "head_branch": {"yard/yard-sv6b"}, "head_sha": {sha}, "epic": {"yard-sv6b"}}
		e := hooks.Envelope{Source: "github", Type: event, Action: "completed", Actor: jane, Mentions: []string{},
			Text: text, URL: url, Fields: f}
		if len(number) > 0 {
			f["number"] = number
			e.Thread = hooks.Thread{ID: pr}
		}
		return e
	}
	janeCased := hooks.Party{ID: "jane-doe", Name: "Jane-Doe"}

	for _, c := range []struct {
		file, event string
		want        hooks.Envelope
	}{
		{"pull_request_opened", "pull_request", prEnv("opened", janeCased, "open", "false")},
		{"pull_request_synchronize", "pull_request", prEnv("synchronize", janeCased, "open", "false")},
		{"pull_request_reopened", "pull_request", prEnv("reopened", janeCased, "open", "false")},
		{"pull_request_ready_for_review", "pull_request", prEnv("ready_for_review", janeCased, "open", "false")},
		{"pull_request_closed_merged", "pull_request", prEnv("closed", mayor, "closed", "true")},
		{"pull_request_closed_unmerged", "pull_request", prEnv("closed", janeCased, "closed", "false")},
		{"pull_request_review_approved", "pull_request_review", review("approved", "Looks good.")},
		{"pull_request_review_changes_requested", "pull_request_review",
			review("changes_requested", "@jane-doe the gate is red, see the race.", "jane-doe")},
		{"pull_request_review_commented", "pull_request_review", review("commented", "")},
		{"issue_comment_pr", "issue_comment", hooks.Envelope{Source: "github", Type: "issue_comment", Action: "created",
			Actor: jane, Thread: prThread, Mentions: []string{"yard-11-new", "mayor"},
			Text: "@yard-11-new rebase please, then @mayor merges.", URL: prURL + "#issuecomment-9", Message: "9",
			Fields: map[string][]string{"event": {"issue_comment"}, "action": {"created"}, "repo": {repo},
				"number": {"17"}, "state": {"open"}}}},
		{"issue_comment_issue", "issue_comment", hooks.Envelope{Source: "github", Type: "issue_comment", Action: "created",
			Actor: hooks.Party{ID: "mayor", Name: "Mayor"}, Thread: issThread, Mentions: []string{},
			Text: "Reproduced. `@mayor` is on it.", URL: issURL + "#issuecomment-10", Message: "10",
			Fields: map[string][]string{"event": {"issue_comment"}, "action": {"created"}, "repo": {repo},
				"number": {"23"}, "state": {"open"}, "label": {"bug"}}}},
		{"issues_opened", "issues", issue("opened", "open")},
		{"issues_closed", "issues", issue("closed", "closed")},
		{"issues_edited", "issues", issue("edited", "open")},
		{"workflow_run_success", "workflow_run", suite("workflow_run", "ci", "success", "ci success on yard/yard-sv6b",
			"https://github.com/acme/yardr/actions/runs/5", "17")},
		{"workflow_run_failure", "workflow_run", suite("workflow_run", "ci", "failure", "ci failure on yard/yard-sv6b",
			"https://github.com/acme/yardr/actions/runs/5")},
		{"check_suite_cancelled", "check_suite", suite("check_suite", "GitHub Actions", "cancelled",
			"GitHub Actions cancelled on yard/yard-sv6b", "https://github.com/acme/yardr/commit/"+sha, "17")},
		// Other events carry what every delivery has, and no thread.
		{"push", "push", hooks.Envelope{Source: "github", Type: "push", Actor: mayor, Mentions: []string{},
			Fields: map[string][]string{"event": {"push"}, "repo": {repo}}}},
		{"ping", "ping", hooks.Envelope{Source: "github", Type: "ping", Actor: jane, Mentions: []string{},
			Fields: map[string][]string{"event": {"ping"}, "repo": {repo}}}},
	} {
		got, err := hooks.GitHub{}.Normalize(http.Header{"X-Github-Event": {c.event}}, ghFixture(t, c.file))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if !reflect.DeepEqual(*got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.file, *got, c.want)
		}
	}
}

func TestGitHubMentions(t *testing.T) {
	for body, want := range map[string][]string{
		"@Mayor, @mayor and (@jane-doe).":                 {"mayor", "jane-doe"},
		"mail ops@example.com or see a/@b":                {},
		"@acme/core and @-x and @x- end":                  {"x"},
		"```\n@mayor in a block\n``` but `@jane` in code": {},
		"@" + strings.Repeat("a", 40):                     {},
	} {
		body, _ := json.Marshal(map[string]any{"issue": map[string]any{"number": 1, "body": body}})
		got, err := hooks.GitHub{}.Normalize(http.Header{"X-Github-Event": {"issues"}}, body)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Mentions, want) {
			t.Errorf("%s: mentions %q, want %q", body, got.Mentions, want)
		}
	}
}

// A github delivery reaches a resident by its lowercase login: a comment
// mentioning it, a pull request whose description newly mentions it, and,
// through a where filter on the epic, CI on the epic's branch. Its own
// actions and a description that mentioned it before do not.
func TestGitHubMentionReachesInbox(t *testing.T) {
	r := newRig(t)
	r.now = time.Now()
	ctx := context.Background()
	if _, err := r.st.PutResident(ctx, store.Resident{Name: "mayor"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetIdentity(ctx, "mayor", "github", store.Identity{Key: "mayor"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []store.Subscription{
		{Source: "github", Scope: store.ScopeMentions},
		{Source: "github", Scope: store.ScopeAll, Where: map[string]string{"epic": "yard-sv6b", "conclusion": "failure"}},
	} {
		if err := r.st.AddSubscription(ctx, "mayor", sub, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for i, d := range []struct{ event, file string }{
		{"pull_request", "pull_request_opened"},      // mentions @Mayor: mentioned
		{"pull_request", "pull_request_synchronize"}, // same description: not again
		{"issue_comment", "issue_comment_pr"},        // mentions @mayor: mentioned
		{"issue_comment", "issue_comment_issue"},     // by Mayor itself, in code anyway
		{"workflow_run", "workflow_run_failure"},     // the epic's CI failed: all
		{"workflow_run", "workflow_run_success"},     // not a failure
	} {
		if code := r.github(t, d.event, ghFixture(t, d.file), secret, d.file); code != http.StatusOK {
			t.Fatalf("%d %s: %d", i, d.file, code)
		}
	}
	page, err := r.st.Inbox(ctx, "mayor", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range page.Events {
		got = append(got, e.Data["type"].(string)+" "+store.Reason(e, "mayor"))
	}
	want := []string{"pull_request mentioned", "issue_comment mentioned", "workflow_run all"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inbox %q, want %q", got, want)
	}
}

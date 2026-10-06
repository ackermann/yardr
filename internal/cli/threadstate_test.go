package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// fakeStates stands in for Linear's GraphQL API and GitHub's REST API as
// yardr thread state uses them: one Linear issue, L1, in a team with or
// without a review status, and one GitHub issue, Acme/webapp#7. Each takes
// only its crew member's credential.
type fakeStates struct {
	review  bool   // the Linear team has a review status
	linear  string // L1's status, by id
	github  string // #7's state
	calls   int    // every request, to either
	changes []string
}

const linearIssue1 = "539068e2-ae88-4d09-bd75-22eb4a59612f"

func fakeStateAPIs(t *testing.T) *fakeStates {
	t.Helper()
	f := &fakeStates{review: true, linear: "s-todo", github: "open"}
	states := func() []any {
		out := []any{
			map[string]any{"id": "s-done", "name": "Done", "type": "completed", "position": 4},
			map[string]any{"id": "s-prog", "name": "In Progress", "type": "started", "position": 2},
			map[string]any{"id": "s-todo", "name": "Todo", "type": "unstarted", "position": 1},
		}
		if f.review {
			out = append(out, map[string]any{"id": "s-rev", "name": "In Review", "type": "started", "position": 3})
		}
		return out
	}
	gqlError := func(w http.ResponseWriter, msg string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": msg}}})
	}
	lin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Header.Get("Authorization") != "lin_api_yardmaster" {
			gqlError(w, "Authentication required")
			return
		}
		if req.Variables["id"] != linearIssue1 {
			gqlError(w, "Entity not found: Issue")
			return
		}
		var data any
		switch {
		case strings.Contains(req.Query, "issueUpdate"):
			in, _ := req.Variables["input"].(map[string]any)
			f.linear, _ = in["stateId"].(string)
			f.changes = append(f.changes, "linear "+f.linear)
			data = map[string]any{"issueUpdate": map[string]any{"success": true}}
		case strings.Contains(req.Query, "issue(id"):
			var cur any
			for _, s := range states() {
				if s.(map[string]any)["id"] == f.linear {
					cur = s
				}
			}
			data = map[string]any{"issue": map[string]any{"id": linearIssue1, "title": "Web view", "url": "https://linear.app/acme/issue/YRD-1",
				"state": cur, "team": map[string]any{"states": map[string]any{"nodes": states()}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		if r.Header.Get("Authorization") != "Bearer gh-yardmaster" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if strings.ToLower(r.URL.Path) != "/repos/acme/webapp/issues/7" || r.Method != http.MethodGet && r.Method != http.MethodPatch {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPatch {
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.github = in["state"]
			f.changes = append(f.changes, strings.TrimSpace("github "+in["state"]+" "+in["state_reason"]))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "title": "Retries", "state": f.github,
			"html_url": "https://github.com/Acme/webapp/issues/7", "repository_url": "https://api.github.com/repos/Acme/webapp"})
	}))
	t.Cleanup(lin.Close)
	t.Cleanup(gh.Close)
	oldLinear, oldGitHub := hooks.LinearAPI, hooks.GitHubAPI
	hooks.LinearAPI, hooks.GitHubAPI = lin.URL, gh.URL
	t.Cleanup(func() { hooks.LinearAPI, hooks.GitHubAPI = oldLinear, oldGitHub })
	return f
}

// stateYard is openYard for yardr thread state: allowed is the yardmaster's
// set_state setting, it is someone in both sources, and it takes part in
// the Linear issue and the GitHub issue the fake holds.
func stateYard(t *testing.T, allowed string) (*app, store.API, *store.Store) {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	def := store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{}}
	if allowed != "" {
		def.Config[store.SetState] = allowed
	}
	if _, err := st.PutCrew(ctx, def, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "operator", "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for src, id := range map[string]store.Identity{"linear": {ID: "u-yardmaster", Key: "yardmaster"}, "github": {ID: "yardmaster-bot"}} {
		if err := st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: src, Scope: store.ScopeInvolved}, "jo"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetIdentity(ctx, "yardmaster", src, id, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	for src, thread := range map[string]string{"linear": linearIssue1, "github": "Acme/webapp#7"} {
		if err := st.FollowThread(ctx, "yardmaster", src, thread, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster")
	// The token alone: an App in the environment goes before it.
	noGitHubCredential(t)
	t.Setenv("GITHUB_TOKEN", "gh-yardmaster")
	t.Setenv("YARDR_CREW", "yardmaster")
	return &app{st: st, actor: "yardmaster"}, st, own
}

// state runs yardr thread state and returns what it printed and what it
// warned of.
func state(t *testing.T, a *app, args ...string) (string, string, error) {
	t.Helper()
	c := a.threadStateCmd()
	var warned bytes.Buffer
	c.SetErr(&warned)
	out, err := run(t, c, args...)
	return out, warned.String(), err
}

// lastEvent is the newest event, which must be of the kind.
func lastEvent(t *testing.T, st store.API, kind string) store.Event {
	t.Helper()
	ctx := context.Background()
	head, _ := st.Head(ctx)
	evs, err := st.EventsSince(ctx, head-1, 1)
	if err != nil || len(evs) != 1 || evs[0].Kind != kind {
		t.Fatalf("last event %+v, %v; want a %s", evs, err, kind)
	}
	return evs[0]
}

// A crew member moves a Linear issue it takes part in through started, review
// and done, naming it by a delivery's seq, by its id, and by source and id.
// Each change is one request and one thread_state event; an issue already
// there gets neither. The delivery of its own change stays out of its inbox.
func TestThreadStateLinear(t *testing.T) {
	f := fakeStateAPIs(t)
	a, st, own := stateYard(t, "linear")
	ctx := context.Background()

	// Jane comments on the issue: the delivery whose seq names the thread.
	comment := hooks.Envelope{Source: "linear", Type: "comment", Action: "create", Actor: hooks.Party{ID: "u-jane", Name: "Jane"},
		Thread: hooks.Thread{ID: linearIssue1, Title: "Web view"}, Message: "c1", Text: "Is anyone on this?"}
	seq, _, err := own.RecordHook(ctx, "linear", comment.Map(), []byte("{}"), nil)
	if err != nil {
		t.Fatal(err)
	}
	inbox := func() int {
		t.Helper()
		page, err := st.Inbox(ctx, "yardmaster", 0)
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Events)
	}
	if inbox() != 1 {
		t.Fatalf("the comment did not reach the inbox")
	}

	for i, c := range []struct {
		args            []string
		to, status, was string
	}{
		{[]string{strconv.FormatUint(seq, 10), "started"}, "s-prog", "In Progress", "Todo"},
		{[]string{linearIssue1, "Review"}, "s-rev", "In Review", "In Progress"},
		{[]string{"--source", "linear", linearIssue1, "done"}, "s-done", "Done", "In Review"},
	} {
		word := strings.ToLower(c.args[len(c.args)-1])
		out, warned, err := state(t, a, c.args...)
		if want := "linear thread " + linearIssue1 + " is now " + c.status + " (was " + c.was + ")\n"; err != nil || out != want || warned != "" {
			t.Fatalf("%v: %q, %q, %v\nwant %q", c.args, out, warned, err, want)
		}
		if f.linear != c.to || len(f.changes) != i+1 {
			t.Fatalf("%v: linear is %s after %v", c.args, f.linear, f.changes)
		}
		e := lastEvent(t, st, "thread_state")
		want := map[string]any{"crew": "yardmaster", "source": "linear", "thread": linearIssue1, "state": word, "status": c.status,
			"was": c.was, "title": "Web view", "url": "https://linear.app/acme/issue/YRD-1"}
		if e.Actor != "yardmaster" || !reflect.DeepEqual(e.Data, want) {
			t.Fatalf("%v: event by %s: %v\nwant %v", c.args, e.Actor, e.Data, want)
		}

		// The same again: the issue is there, so nothing is sent to
		// change it and nothing is logged.
		head, _ := st.Head(ctx)
		calls := f.calls
		out, _, err = state(t, a, c.args...)
		if want := "linear thread " + linearIssue1 + " was already " + c.status + "; nothing set\n"; err != nil || out != want {
			t.Fatalf("%v again: %q, %v", c.args, out, err)
		}
		if now, _ := st.Head(ctx); now != head || len(f.changes) != i+1 || f.calls != calls+1 {
			t.Fatalf("%v again: %d events, changes %v, %d requests", c.args, now-head, f.changes, f.calls-calls)
		}

		// Linear tells of the change, made by the crew member's own user: it
		// does not reach the crew member, as a change by someone else does.
		change := hooks.Envelope{Source: "linear", Type: "issue", Action: "update", Actor: hooks.Party{ID: "u-yardmaster", Name: "Yardmaster"},
			Thread: hooks.Thread{ID: linearIssue1, Title: "Web view"}, Text: "changed stateId", Entity: linearIssue1}
		if _, _, err := own.RecordHook(ctx, "linear", change.Map(), []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
		if inbox() != 1 {
			t.Fatalf("%v: the delivery of its own change reached its inbox", c.args)
		}
	}
	change := hooks.Envelope{Source: "linear", Type: "issue", Action: "update", Actor: hooks.Party{ID: "u-jane", Name: "Jane"},
		Thread: hooks.Thread{ID: linearIssue1}, Text: "changed stateId", Entity: linearIssue1}
	if _, _, err := own.RecordHook(ctx, "linear", change.Map(), []byte("{}"), nil); err != nil || inbox() != 2 {
		t.Fatalf("a change by someone else: inbox %d, %v", inbox(), err)
	}

	// A team without a review status: review is started, and says so.
	f.review, f.linear = false, "s-todo"
	out, _, err := state(t, a, linearIssue1, "review")
	want := "linear has no review status here: started instead\nlinear thread " + linearIssue1 + " is now In Progress (was Todo)\n"
	if err != nil || out != want || f.linear != "s-prog" {
		t.Fatalf("no review status: %q, %v, linear %s", out, err, f.linear)
	}
	if e := lastEvent(t, st, "thread_state"); e.Data["state"] != "review" || e.Data["instead"] != "started" || e.Data["status"] != "In Progress" {
		t.Fatalf("no review status: event %v", e.Data)
	}
	// Asked again it was there, and that it is not a review status is said
	// again.
	n := len(f.changes)
	out, _, err = state(t, a, linearIssue1, "review")
	if err != nil || !strings.HasPrefix(out, "linear has no review status here: started instead\n") ||
		!strings.Contains(out, "was already In Progress; nothing set") || len(f.changes) != n {
		t.Fatalf("no review status, again: %q, %v", out, err)
	}

	// --json prints the event's fields, and whether anything changed. The
	// operator speaks as the crew member: the event names who did.
	a.json, a.actor = true, "jo"
	t.Setenv("YARDR_CREW", "")
	for _, changed := range []bool{true, false} {
		out, _, err = state(t, a, linearIssue1, "done", "--crew", "yardmaster")
		var res map[string]any
		want := map[string]any{"crew": "yardmaster", "source": "linear", "thread": linearIssue1, "state": "done", "status": "Done",
			"actor": "jo", "changed": changed, "title": "Web view", "url": "https://linear.app/acme/issue/YRD-1"}
		if changed {
			want["was"] = "In Progress"
		}
		if err != nil || json.Unmarshal([]byte(out), &res) != nil || !reflect.DeepEqual(res, want) {
			t.Fatalf("json: %q, %v\nwant %v", out, err, want)
		}
	}
	if e := lastEvent(t, st, "thread_state"); e.Actor != "jo" || e.Data["crew"] != "yardmaster" {
		t.Fatalf("the operator's event: %+v", e)
	}
	a.json = false

	// Without an identity in the source yardr cannot tell the delivery of
	// the change from someone else's, and says so when it changes something.
	if err := st.SetIdentity(ctx, "yardmaster", "linear", store.Identity{}, "jo"); err != nil {
		t.Fatal(err)
	}
	_, warned, err := state(t, a, linearIssue1, "started", "--crew", "yardmaster")
	if err != nil || !strings.Contains(warned, "yardmaster has no linear identity, so the delivery of this change may reach its own inbox: yardr crew identity yardmaster --source linear") {
		t.Fatalf("no identity: %q, %v", warned, err)
	}
	if _, warned, err = state(t, a, linearIssue1, "started", "--crew", "yardmaster"); err != nil || warned != "" {
		t.Fatalf("no identity, nothing changed: %q, %v", warned, err)
	}
}

// On GitHub done closes the issue as completed and started reopens it;
// either asked twice is one request that changes something. review is
// refused with the words GitHub has, before GitHub is asked.
func TestThreadStateGitHub(t *testing.T) {
	f := fakeStateAPIs(t)
	a, st, own := stateYard(t, "github")
	ctx := context.Background()
	comment := hooks.Envelope{Source: "github", Type: "issue_comment", Action: "created", Actor: hooks.Party{ID: "jane", Name: "jane"},
		Thread: hooks.Thread{ID: "Acme/webapp#7", Title: "Retries"}, Message: "c1", Text: "Still happening?"}
	seq, _, err := own.RecordHook(ctx, "github", comment.Map(), []byte("{}"), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Open already: started has nothing to do.
	out, _, err := state(t, a, "Acme/webapp#7", "started")
	if err != nil || out != "github thread Acme/webapp#7 was already open; nothing set\n" || len(f.changes) != 0 || kinds(t, st, "thread_state") != 0 {
		t.Fatalf("started on an open issue: %q, %v, %v", out, err, f.changes)
	}
	for i, c := range []struct {
		args                []string
		change, status, was string
	}{
		{[]string{strconv.FormatUint(seq, 10), "done"}, "github closed completed", "closed", "open"},
		{[]string{"--source", "github", "Acme/webapp#7", "started"}, "github open", "open", "closed"},
	} {
		out, warned, err := state(t, a, c.args...)
		if want := "github thread Acme/webapp#7 is now " + c.status + " (was " + c.was + ")\n"; err != nil || out != want || warned != "" {
			t.Fatalf("%v: %q, %q, %v", c.args, out, warned, err)
		}
		if len(f.changes) != i+1 || f.changes[i] != c.change {
			t.Fatalf("%v: changes %q", c.args, f.changes)
		}
		e := lastEvent(t, st, "thread_state")
		want := map[string]any{"crew": "yardmaster", "source": "github", "thread": "Acme/webapp#7", "state": c.args[len(c.args)-1],
			"status": c.status, "was": c.was, "title": "Retries", "url": "https://github.com/Acme/webapp/issues/7"}
		if e.Actor != "yardmaster" || !reflect.DeepEqual(e.Data, want) {
			t.Fatalf("%v: event %v\nwant %v", c.args, e.Data, want)
		}
		head, _ := st.Head(ctx)
		if out, _, err := state(t, a, c.args...); err != nil || !strings.Contains(out, "was already "+c.status+"; nothing set") {
			t.Fatalf("%v again: %q, %v", c.args, out, err)
		}
		if now, _ := st.Head(ctx); now != head || len(f.changes) != i+1 {
			t.Fatalf("%v again: %d events, changes %q", c.args, now-head, f.changes)
		}
		// GitHub tells of it; the crew member's own login is the sender.
		change := hooks.Envelope{Source: "github", Type: "issues", Action: map[string]string{"closed": "closed", "open": "reopened"}[c.status],
			Actor: hooks.Party{ID: "yardmaster-bot", Name: "Yardmaster-Bot"}, Thread: hooks.Thread{ID: "Acme/webapp#7", Title: "Retries"}}
		if _, _, err := own.RecordHook(ctx, "github", change.Map(), []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
		if page, err := st.Inbox(ctx, "yardmaster", 0); err != nil || len(page.Events) != 1 {
			t.Fatalf("%v: the delivery of its own change reached its inbox: %+v, %v", c.args, page, err)
		}
	}

	head, _ := st.Head(ctx)
	calls := f.calls
	_, _, err = state(t, a, "Acme/webapp#7", "review")
	if err == nil || !strings.Contains(err.Error(), "an issue has no review status: the states it has are started and done") {
		t.Fatalf("review: %v", err)
	}
	if now, _ := st.Head(ctx); now != head || f.calls != calls {
		t.Fatalf("review: %d events, %d requests", now-head, f.calls-calls)
	}
}

// Off unless the crew member's definition allows the source, only in a thread
// it takes part in, and only from its own session or the operator's. No
// refusal reaches a source or leaves an event.
func TestThreadStateRefusals(t *testing.T) {
	f := fakeStateAPIs(t)
	a, st, own := stateYard(t, "github")
	ctx := context.Background()
	record := func(e hooks.Envelope) string {
		t.Helper()
		seq, _, err := own.RecordHook(ctx, e.Source, e.Map(), []byte("{}"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatUint(seq, 10)
	}
	elsewhere := record(hooks.Envelope{Source: "github", Type: "issues", Action: "opened", Actor: hooks.Party{ID: "jane"},
		Thread: hooks.Thread{ID: "Acme/webapp#8", Title: "Other"}})
	linear := record(hooks.Envelope{Source: "linear", Type: "comment", Action: "create", Actor: hooks.Party{ID: "u-jane"},
		Thread: hooks.Thread{ID: linearIssue1}, Message: "c1"})
	token := record(hooks.Envelope{Source: "token", Type: "backup", Actor: hooks.Party{ID: "cron"}, Thread: hooks.Thread{ID: "nightly"}})
	if err := st.FollowThread(ctx, "yardmaster", "linear", "Acme/webapp#7", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.FollowThread(ctx, "yardmaster", "token", "nightly", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.Mail(ctx, "yardmaster", "hello", "jo"); err != nil {
		t.Fatal(err)
	}
	mail, _ := st.Head(ctx)

	head, _ := st.Head(ctx)
	refused := func(want string, args ...string) {
		t.Helper()
		_, _, err := state(t, a, args...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: got %v, want %q", args, err, want)
		}
		if f.calls != 0 {
			t.Fatalf("%v: reached a source", args)
		}
		if now, _ := st.Head(ctx); now != head {
			t.Fatalf("%v: the refusal logged %d events", args, now-head)
		}
	}
	// Without the setting for the source, however the thread is named.
	for _, args := range [][]string{{linear, "started"}, {linearIssue1, "started"}, {"--source", "linear", linearIssue1, "started"}} {
		refused("crew member yardmaster may not set the status of threads in linear", args...)
		refused("yardr crew set yardmaster set_state=github,linear", args...)
	}
	// Allowed in the source, but no part in the thread.
	refused("crew member yardmaster takes no part in github thread Acme/webapp#8", elsewhere, "done")
	refused("yardr thread follow yardmaster --source github Acme/webapp#8", elsewhere, "done")
	refused("yardr thread follow yardmaster --source github Acme/webapp#9", "--source", "github", "Acme/webapp#9", "done")
	refused("crew member yardmaster takes no part in a thread Acme/webapp#9", "Acme/webapp#9", "done")
	refused("yardr thread follow yardmaster --source <source> Acme/webapp#9", "Acme/webapp#9", "done")
	// An id it follows in two sources needs its source.
	refused("takes part in a thread Acme/webapp#7 in github and linear: say which with --source", "Acme/webapp#7", "done")
	// What is no state, no thread, no source with statuses.
	refused(`"closed" is no state: want started, review, done`, "Acme/webapp#7", "closed")
	refused("yardr cannot set the status of a thread in token (it can in github, linear)", "--source", "token", "nightly", "done")
	refused("which yardr cannot answer in", token, "done")
	refused(`--source "slack": one of`, "--source", "slack", "C1", "done")
	refused("is a mail event, not a hook delivery", strconv.FormatUint(mail, 10), "done")
	refused("event 99999", "99999", "done")
	refused("crew member ghost", "--source", "github", "Acme/webapp#7", "done", "--crew", "ghost")

	// A bead's agent, or another crew member, naming it with --crew.
	t.Setenv("YARDR_CREW", "")
	a.actor = "yard-10-new"
	refused("only yardmaster's own session or the operator (jo) may speak as yardmaster; this is yard-10-new",
		"--source", "github", "Acme/webapp#7", "done", "--crew", "yardmaster")
	refused("--crew is required", "--source", "github", "Acme/webapp#7", "done")

	// Without its credential nothing changes.
	a.actor = "yardmaster"
	t.Setenv("GITHUB_TOKEN", "wrong")
	if _, _, err := state(t, a, "--source", "github", "Acme/webapp#7", "done", "--crew", "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "401") || len(f.changes) != 0 || kinds(t, st, "thread_state") != 0 {
		t.Fatalf("wrong credential: %v, %v", err, f.changes)
	}
	// The operator may speak as it.
	t.Setenv("GITHUB_TOKEN", "gh-yardmaster")
	a.actor = "jo"
	if _, _, err := state(t, a, "--source", "github", "Acme/webapp#7", "done", "--crew", "yardmaster"); err != nil || f.github != "closed" {
		t.Fatalf("the operator: %v, github %s", err, f.github)
	}
}

// Only the operator lets a crew member set the status of threads, by the rules
// of open_threads: a crew member's session cannot add a source to its own
// definition or to another's, and may keep or narrow what it has.
func TestSetStateIsTheOperatorsToSet(t *testing.T) {
	a, st, _ := stateYard(t, "")
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scout", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	add := func(name string, sets ...string) error {
		t.Helper()
		args := []string{"add", name, "--replace", "--dir", t.TempDir()}
		for _, s := range sets {
			args = append(args, "--set", s)
		}
		_, err := run(t, a.crewCmd(), args...)
		return err
	}
	may := func(name string) string {
		t.Helper()
		r, err := st.GetCrew(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(r.MaySetState(), ",")
	}

	// yardmaster's own session, for itself, for another crew member and for one
	// that does not exist yet.
	for _, name := range []string{"yardmaster", "scout", "fresh"} {
		err := add(name, "kind=claude", "set_state=linear")
		if err == nil || !strings.Contains(err.Error(), "only the operator (jo) may let "+name+" set the status of threads in linear; this is yardmaster") {
			t.Fatalf("yardmaster's session allowing %s: %v", name, err)
		}
	}
	if _, err := st.GetCrew(ctx, "fresh"); err == nil || may("yardmaster") != "" || may("scout") != "" {
		t.Fatalf("a refused definition was saved: yardmaster %q, scout %q", may("yardmaster"), may("scout"))
	}
	// A bead's agent is no operator either.
	a.actor = "yard-10-new"
	if err := add("yardmaster", "kind=claude", `set_state=["github"]`); err == nil {
		t.Fatal("a bead's agent allowed it")
	}

	a.actor = "jo"
	if err := add("yardmaster", "kind=claude", "set_state=linear"); err != nil || may("yardmaster") != "linear" {
		t.Fatalf("the operator: %v, %q", err, may("yardmaster"))
	}
	for _, bad := range []string{"set_state=token", "set_state=linear,slack", "set_state=", "set_state=true"} {
		if err := add("yardmaster", "kind=claude", bad); err == nil || !strings.Contains(err.Error(), "--set set_state") {
			t.Fatalf("%s: %v", bad, err)
		}
	}

	// The crew member saves its definition with what it already has, but not
	// with more; without the setting, it has given it up. Being allowed to
	// open threads is no leave to set their status, nor the other way.
	a.actor = "yardmaster"
	if err := add("yardmaster", "kind=claude", "model=opus", "set_state=linear"); err != nil || may("yardmaster") != "linear" {
		t.Fatalf("keeping it: %v, %q", err, may("yardmaster"))
	}
	if err := add("yardmaster", "kind=claude", "set_state=linear,github"); err == nil || !strings.Contains(err.Error(), "set the status of threads in github;") {
		t.Fatalf("widening it: %v", err)
	}
	if err := add("yardmaster", "kind=claude", "set_state=linear", "open_threads=linear"); err == nil || !strings.Contains(err.Error(), "open threads in linear;") {
		t.Fatalf("taking the other setting: %v", err)
	}
	if err := add("yardmaster", "kind=claude"); err != nil || may("yardmaster") != "" {
		t.Fatalf("dropping it: %v, %q", err, may("yardmaster"))
	}
	if err := add("yardmaster", "kind=claude", "set_state=linear"); err == nil {
		t.Fatal("taking it back after dropping it")
	}

	// crew show says what is allowed.
	a.actor = "jo"
	if err := add("yardmaster", "kind=claude", "set_state=github,linear"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.crewCmd(), "show", "yardmaster")
	if err != nil || !strings.Contains(out, "may set the status of threads in github, linear\n") || strings.Contains(out, "may open threads") {
		t.Fatalf("show: %q, %v", out, err)
	}
}

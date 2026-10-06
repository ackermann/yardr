package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// fakeSources stands in for Linear's GraphQL API and GitHub's REST API as
// yardr thread open and yardr reply use them: issues are created, found
// again and commented on. Each takes only its crew member's credential. With
// lose set, a create makes its issue and then answers with a 504, as a
// post whose answer never arrived looks to the caller.
type fakeSources struct {
	linear   map[string]map[string]any // issues by id
	github   []map[string]any          // issues of Acme/webapp, #1 first
	comments []string                  // "<thread>: <body>", as posted
	lose     bool
}

func fakeSourceAPIs(t *testing.T) *fakeSources {
	t.Helper()
	f := &fakeSources{linear: map[string]map[string]any{}}
	gqlError := func(w http.ResponseWriter, msg string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": msg}}})
	}
	lin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Header.Get("Authorization") != "lin_api_yardmaster" {
			gqlError(w, "Authentication required")
			return
		}
		in, _ := req.Variables["input"].(map[string]any)
		id, _ := req.Variables["id"].(string)
		var data any
		switch {
		case strings.Contains(req.Query, "teams("):
			data = map[string]any{"teams": map[string]any{"nodes": []any{map[string]any{"id": "team-yrd", "key": "YRD", "name": "Yard"}},
				"pageInfo": map[string]any{"hasNextPage": false}}}
		case strings.Contains(req.Query, "issueCreate"):
			id, _ := in["id"].(string)
			if in["teamId"] != "team-yrd" || f.linear[id] != nil {
				gqlError(w, "issueCreate refused")
				return
			}
			i := map[string]any{"id": id, "title": in["title"], "description": in["description"],
				"url": fmt.Sprint("https://linear.app/acme/issue/YRD-", len(f.linear)+1)}
			f.linear[id] = i
			if f.lose {
				http.Error(w, "upstream timed out", http.StatusGatewayTimeout)
				return
			}
			data = map[string]any{"issueCreate": map[string]any{"success": true, "issue": i}}
		case strings.Contains(req.Query, "issue(id"):
			if f.linear[id] == nil {
				gqlError(w, "Entity not found: Issue")
				return
			}
			data = map[string]any{"issue": f.linear[id]}
		case strings.Contains(req.Query, "commentCreate"):
			issue, _ := in["issueId"].(string)
			if f.linear[issue] == nil {
				gqlError(w, "Entity not found: Issue")
				return
			}
			f.comments = append(f.comments, fmt.Sprintf("%s: %s", issue, in["body"]))
			data = map[string]any{"commentCreate": map[string]any{"success": true,
				"comment": map[string]any{"id": in["id"], "url": fmt.Sprint(f.linear[issue]["url"], "#comment-1")}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-yardmaster" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		var in struct{ Title, Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		path := strings.ToLower(r.URL.Path)
		switch {
		case path == "/repos/acme/webapp/issues" && r.Method == http.MethodPost:
			n := len(f.github) + 1
			i := map[string]any{"number": n, "title": in.Title, "body": in.Body,
				"html_url":       fmt.Sprint("https://github.com/Acme/webapp/issues/", n),
				"repository_url": "https://api.github.com/repos/Acme/webapp"}
			f.github = append(f.github, i)
			if f.lose {
				http.Error(w, "upstream timed out", http.StatusGatewayTimeout)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(i)
		case path == "/repos/acme/webapp/issues" && r.Method == http.MethodGet:
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode(f.github)
		case strings.HasSuffix(path, "/comments") && r.Method == http.MethodPost:
			n := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/Acme/webapp/issues/"), "/comments")
			if i, err := strconv.Atoi(n); err != nil || i < 1 || i > len(f.github) {
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			f.comments = append(f.comments, fmt.Sprintf("Acme/webapp#%s: %s", n, in.Body))
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "body": in.Body,
				"html_url": "https://github.com/Acme/webapp/issues/" + n + "#issuecomment-7", "user": map[string]any{"login": "yardmaster-bot"}})
		default:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(lin.Close)
	t.Cleanup(gh.Close)
	oldLinear, oldGitHub := hooks.LinearAPI, hooks.GitHubAPI
	hooks.LinearAPI, hooks.GitHubAPI = lin.URL, gh.URL
	t.Cleanup(func() { hooks.LinearAPI, hooks.GitHubAPI = oldLinear, oldGitHub })
	return f
}

// openYard is a store with the operator jo, a crew member yardmaster that hears of
// what it is involved in in both sources, and its session: the pane names
// it, and holds its credentials. allowed is its open_threads setting.
func openYard(t *testing.T, allowed string) (*app, store.API, *store.Store) {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	def := store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{}}
	if allowed != "" {
		def.Config[store.OpenThreads] = allowed
	}
	if _, err := st.PutCrew(ctx, def, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "operator", "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"linear", "github"} {
		if err := st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: src, Scope: store.ScopeInvolved}, "jo"); err != nil {
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

// kinds counts the events of a kind.
func kinds(t *testing.T, st store.API, kind string) int {
	t.Helper()
	evs, err := st.EventsSince(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// A crew member opens a thread as itself in Linear and in GitHub. The thread
// is its own at once: what someone else says there, with no mention,
// reaches its inbox, and yardr reply answers it in the thread.
func TestThreadOpen(t *testing.T) {
	f := fakeSourceAPIs(t)
	a, st, own := openYard(t, "linear,github")
	ctx := context.Background()

	cases := []struct {
		source, in, title, url string
		thread                 func() string // the id deliveries name it by
	}{
		{"linear", "yrd", "Web view: train", "https://linear.app/acme/issue/YRD-1", func() string {
			for id := range f.linear {
				return id
			}
			return ""
		}},
		// The repository as typed, the thread as GitHub spells it.
		{"github", "acme/webapp", "Retries drop the last attempt", "https://github.com/Acme/webapp/issues/1",
			func() string { return "Acme/webapp#1" }},
	}
	for i, c := range cases {
		out, err := run(t, a.threadOpenCmd(), c.source, "--in", c.in, c.title, "-b", "What it is for.\n")
		if err != nil {
			t.Fatalf("%s: %v", c.source, err)
		}
		if out != c.url+"\n" {
			t.Fatalf("%s: out %q", c.source, out)
		}
		thread := c.thread()
		ps, err := st.Threads(ctx, "yardmaster")
		if err != nil || len(ps) != i+1 {
			t.Fatalf("%s: threads %+v, %v", c.source, ps, err)
		}
		var p store.Participation
		for _, q := range ps {
			if q.Source == c.source {
				p = q
			}
		}
		if p.Thread != thread || p.How != store.ParticipantOpened || p.Title != c.title || p.URL != c.url {
			t.Fatalf("%s: participation %+v, want thread %q", c.source, p, thread)
		}
		if n := kinds(t, st, "thread_opened"); n != i+1 {
			t.Fatalf("%s: %d thread_opened events", c.source, n)
		}

		// Someone answers in the thread and mentions no one.
		env := hooks.Envelope{Source: c.source, Type: "comment", Action: "create", Actor: hooks.Party{ID: "u-jane", Name: "Jane"},
			Thread: hooks.Thread{ID: thread, Title: c.title, URL: c.url}, Message: "c1", Text: "Which release?"}
		if c.source == "github" {
			env.Type, env.Action = "issue_comment", "created"
		}
		seq, _, err := own.RecordHook(ctx, c.source, env.Map(), []byte("{}"), nil)
		if err != nil {
			t.Fatal(err)
		}
		page, err := st.Inbox(ctx, "yardmaster", 0)
		if err != nil || len(page.Events) != i+1 || page.Events[i].Seq != seq || store.Reason(page.Events[i], "yardmaster") != store.ReasonParticipant {
			t.Fatalf("%s: inbox %+v, %v", c.source, page, err)
		}
		if _, err := run(t, a.replyCmd(), strconv.FormatUint(seq, 10), "The next one."); err != nil {
			t.Fatalf("%s: reply: %v", c.source, err)
		}
		if len(f.comments) != i+1 || !strings.HasPrefix(f.comments[i], thread+": The next one.") {
			t.Fatalf("%s: comments %q", c.source, f.comments)
		}
	}
	var linear map[string]any
	for _, linear = range f.linear {
	}
	if len(f.linear) != 1 || linear["title"] != "Web view: train" || linear["description"] != "What it is for." || !uuid4.MatchString(linear["id"].(string)) {
		t.Fatalf("linear issues: %v", f.linear)
	}
	if len(f.github) != 1 || f.github[0]["title"] != "Retries drop the last attempt" ||
		!strings.HasPrefix(f.github[0]["body"].(string), "What it is for.\n\n<!-- yardr-thread ") {
		t.Fatalf("github issues: %v", f.github)
	}

	// The body from stdin, and the result as JSON.
	a.json = true
	c := a.threadOpenCmd()
	c.SetIn(strings.NewReader("From a file.\n"))
	out, err := run(t, c, "linear", "--in", "Yard", "Second", "-b", "-")
	a.json = false
	var res map[string]any
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res["repeated"] != false || res["source"] != "linear" ||
		res["title"] != "Second" || res["url"] != "https://linear.app/acme/issue/YRD-2" || f.linear[fmt.Sprint(res["thread"])]["description"] != "From a file." {
		t.Fatalf("json: %q, %v", out, err)
	}
}

// Run again after a lost answer, yardr thread open finds the thread the
// first run made and opens no second one.
func TestThreadOpenRetryDoesNotOpenTwice(t *testing.T) {
	for _, c := range []struct{ source, in, url string }{
		{"linear", "YRD", "https://linear.app/acme/issue/YRD-1"},
		{"github", "Acme/webapp", "https://github.com/Acme/webapp/issues/1"},
	} {
		t.Run(c.source, func(t *testing.T) {
			f := fakeSourceAPIs(t)
			a, st, _ := openYard(t, c.source)
			issues := func() int { return len(f.linear) + len(f.github) }
			open := func(title string) (string, error) {
				return run(t, a.threadOpenCmd(), c.source, "--in", c.in, title, "-b", "Body.")
			}

			// The answer is lost: the source made the issue, the call failed.
			f.lose = true
			if _, err := open("Train"); err == nil || !strings.Contains(err.Error(), "504") {
				t.Fatalf("lost answer: %v", err)
			}
			f.lose = false
			ps, _ := st.Threads(context.Background(), "yardmaster")
			if issues() != 1 || kinds(t, st, "thread_opened") != 0 || len(ps) != 0 {
				t.Fatalf("after the lost answer: %d issues, %d events, threads %v", issues(), kinds(t, st, "thread_opened"), ps)
			}
			out, err := open("Train")
			if err != nil || out != c.url+"\n" {
				t.Fatalf("retry: %q, %v", out, err)
			}
			ps, _ = st.Threads(context.Background(), "yardmaster")
			if issues() != 1 || kinds(t, st, "thread_opened") != 1 || len(ps) != 1 || ps[0].How != store.ParticipantOpened || ps[0].Title != "Train" {
				t.Fatalf("after the retry: %d issues, %d events, threads %+v", issues(), kinds(t, st, "thread_opened"), ps)
			}
			// Once recorded, the record is the whole answer.
			a.json = true
			out, err = open("Train")
			a.json = false
			var res map[string]any
			if err != nil || json.Unmarshal([]byte(out), &res) != nil || res["repeated"] != true || res["url"] != c.url || res["thread"] != ps[0].Thread {
				t.Fatalf("a recorded thread run again: %q, %v", out, err)
			}
			if issues() != 1 || kinds(t, st, "thread_opened") != 1 {
				t.Fatalf("run again: %d issues, %d events", issues(), kinds(t, st, "thread_opened"))
			}
			// Another title is another thread.
			if _, err := open("Other"); err != nil || issues() != 2 {
				t.Fatalf("another title: %d issues, %v", issues(), err)
			}
		})
	}
}

// Off unless the crew member's definition allows the source, and then only
// from the crew member's own session or the operator's.
func TestThreadOpenRefusals(t *testing.T) {
	f := fakeSourceAPIs(t)
	a, st, _ := openYard(t, "github")
	ctx := context.Background()
	head, _ := st.Head(ctx)
	refused := func(want string, args ...string) {
		t.Helper()
		_, err := run(t, a.threadOpenCmd(), args...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: got %v, want %q", args, err, want)
		}
		if len(f.linear)+len(f.github) != 0 {
			t.Fatalf("%v: opened %v %v", args, f.linear, f.github)
		}
		if now, _ := st.Head(ctx); now != head {
			t.Fatalf("%v: the refusal logged %d events", args, now-head)
		}
	}
	refused("crew member yardmaster may not open threads in linear", "linear", "--in", "YRD", "Train")
	refused("yardr crew set yardmaster open_threads=github,linear", "linear", "--in", "YRD", "Train")
	refused("yardr cannot open threads in token (it can in github, linear)", "token", "--in", "x", "Train")
	refused("--in is required: the repository to open the thread in", "github", "Train")
	refused("the title is empty", "github", "--in", "Acme/webapp", "  ")
	refused("want owner/repo", "github", "--in", "webapp", "Train")
	head, _ = st.Head(ctx) // the last one was recorded before the source refused it
	refused("crew member ghost", "github", "--in", "Acme/webapp", "Train", "--crew", "ghost")

	// A bead's agent, or another crew member, naming it with --crew.
	t.Setenv("YARDR_CREW", "")
	a.actor = "yard-10-new"
	refused("only yardmaster's own session or the operator (jo) may speak as yardmaster; this is yard-10-new",
		"github", "--in", "Acme/webapp", "Train", "--crew", "yardmaster")
	refused("--crew is required", "github", "--in", "Acme/webapp", "Train")
	// Without its credential nothing is posted.
	a.actor = "yardmaster"
	t.Setenv("GITHUB_TOKEN", "wrong")
	if _, err := run(t, a.threadOpenCmd(), "github", "--in", "Acme/webapp", "Train", "--crew", "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "401") || len(f.github) != 0 {
		t.Fatalf("wrong credential: %v, %v", err, f.github)
	}
	// The operator may speak as it.
	t.Setenv("GITHUB_TOKEN", "gh-yardmaster")
	a.actor = "jo"
	if _, err := run(t, a.threadOpenCmd(), "github", "--in", "Acme/webapp", "Train", "--crew", "yardmaster"); err != nil || len(f.github) != 1 {
		t.Fatalf("the operator: %v, %v", err, f.github)
	}
}

// Only the operator lets a crew member open threads: a crew member's session
// cannot add a source to its own definition or to another's. Keeping or
// narrowing what is allowed is anyone's, as the rest of the definition is.
func TestOpenThreadsIsTheOperatorsToSet(t *testing.T) {
	a, st, _ := openYard(t, "")
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
		return strings.Join(r.MayOpen(), ",")
	}

	// yardmaster's own session, for itself, for another crew member and for one
	// that does not exist yet.
	for _, name := range []string{"yardmaster", "scout", "fresh"} {
		err := add(name, "kind=claude", "open_threads=linear")
		if err == nil || !strings.Contains(err.Error(), "only the operator (jo) may let "+name+" open threads in linear; this is yardmaster") {
			t.Fatalf("yardmaster's session allowing %s: %v", name, err)
		}
	}
	if _, err := st.GetCrew(ctx, "fresh"); err == nil || may("yardmaster") != "" || may("scout") != "" {
		t.Fatalf("a refused definition was saved: yardmaster %q, scout %q", may("yardmaster"), may("scout"))
	}
	// A bead's agent is no operator either.
	a.actor = "yard-10-new"
	if err := add("yardmaster", "kind=claude", `open_threads=["github"]`); err == nil {
		t.Fatal("a bead's agent allowed it")
	}

	a.actor = "jo"
	if err := add("yardmaster", "kind=claude", "open_threads=linear"); err != nil || may("yardmaster") != "linear" {
		t.Fatalf("the operator: %v, %q", err, may("yardmaster"))
	}
	for _, bad := range []string{"open_threads=token", "open_threads=linear,slack", "open_threads=", "open_threads=true"} {
		if err := add("yardmaster", "kind=claude", bad); err == nil || !strings.Contains(err.Error(), "--set open_threads") {
			t.Fatalf("%s: %v", bad, err)
		}
	}

	// The crew member saves its definition with what it already has, but not
	// with more; without the setting, it has given it up.
	a.actor = "yardmaster"
	if err := add("yardmaster", "kind=claude", "model=opus", "open_threads=linear"); err != nil || may("yardmaster") != "linear" {
		t.Fatalf("keeping it: %v, %q", err, may("yardmaster"))
	}
	if err := add("yardmaster", "kind=claude", "open_threads=linear,github"); err == nil || !strings.Contains(err.Error(), "open threads in github;") {
		t.Fatalf("widening it: %v", err)
	}
	if err := add("yardmaster", "kind=claude"); err != nil || may("yardmaster") != "" {
		t.Fatalf("dropping it: %v, %q", err, may("yardmaster"))
	}
	if err := add("yardmaster", "kind=claude", "open_threads=linear"); err == nil {
		t.Fatal("taking it back after dropping it")
	}

	// crew show says what is allowed.
	a.actor = "jo"
	if err := add("yardmaster", "kind=claude", "open_threads=github,linear"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.crewCmd(), "show", "yardmaster")
	if err != nil || !strings.Contains(out, "may open threads in github, linear\n") {
		t.Fatalf("show: %q, %v", out, err)
	}
}

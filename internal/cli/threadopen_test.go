package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// fakeSources stands in for GitHub's REST API as yardr thread open uses
// it: issues are created, found again and commented on. It takes only
// its crew member's credential. With lose set, a create makes its issue
// and then answers with a 504, as a post whose answer never arrived
// looks to the caller.
type fakeSources struct {
	github   []map[string]any // issues of Acme/webapp, #1 first
	comments []string         // "<thread>: <body>", as posted
	lose     bool
}

func fakeSourceAPIs(t *testing.T) (*fakeSources, string) {
	t.Helper()
	f := &fakeSources{}
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
	t.Cleanup(gh.Close)
	return f, gh.URL
}

// openScripts answers an acme thread's commands from dir: open leaves
// one issue per key, opened finds it, reply answers c-9. With dir/lose
// present, open records the issue and then fails, as a post whose answer
// never arrived looks to the caller.
func openScripts(dir string) map[string]string {
	scripts := map[string]string{
		"normalize": "#!/bin/sh\nprintf '%s' '{\"type\":\"ping\"}'\n",
		"open": "#!/bin/sh\nin=$(cat)\n" +
			"printf '%s\\n' \"$in\" >> " + dir + "/open.stdin\n" +
			"key=$(printf '%s' \"$in\" | grep -o '\"key\":\"[^\"]*\"' | head -1 | cut -d'\"' -f4)\n" +
			"title=$(printf '%s' \"$in\" | grep -o '\"title\":\"[^\"]*\"' | head -1 | cut -d'\"' -f4)\n" +
			"if [ ! -f " + dir + "/id.\"$key\" ]; then\n" +
			"  n=$(ls " + dir + "/id.* 2>/dev/null | wc -l)\n" +
			"  printf 'issue-%s' \"$((n+1))\" > " + dir + "/id.\"$key\"\n" +
			"  printf '%s' \"$title\" > " + dir + "/title.\"$key\"\n" +
			"fi\n" +
			"touch " + dir + "/done.open.\"$key\"\n" +
			"if [ -f " + dir + "/lose ]; then echo 'upstream timed out' >&2; exit 1; fi\n" +
			"[ \"$ACME_TOKEN\" = tok-yardmaster ] || { echo 'Authentication required' >&2; exit 1; }\n" +
			"id=$(cat " + dir + "/id.\"$key\"); num=${id#issue-}; t=$(cat " + dir + "/title.\"$key\")\n" +
			"printf '{\"id\":\"%s\",\"title\":\"%s\",\"url\":\"https://acme.test/issue/ACME-%s\"}' \"$id\" \"$t\" \"$num\"\n",
		"opened": "#!/bin/sh\nin=$(cat)\n" +
			"printf '%s\\n' \"$in\" >> " + dir + "/opened.stdin\n" +
			"key=$(printf '%s' \"$in\" | grep -o '\"key\":\"[^\"]*\"' | head -1 | cut -d'\"' -f4)\n" +
			"if [ -f " + dir + "/id.\"$key\" ]; then\n" +
			"  id=$(cat " + dir + "/id.\"$key\"); num=${id#issue-}; t=$(cat " + dir + "/title.\"$key\")\n" +
			"  printf '{\"id\":\"%s\",\"title\":\"%s\",\"url\":\"https://acme.test/issue/ACME-%s\"}' \"$id\" \"$t\" \"$num\"\n" +
			"else\n" +
			"  printf '{}'\n" +
			"fi\n",
		"reply": "#!/bin/sh\nin=$(cat)\n" +
			"printf '%s\\n' \"$in\" >> " + dir + "/reply.stdin\n" +
			"printf '%s' '{\"id\":\"c-9\",\"url\":\"https://acme.test/issue/ACME-1#c-9\"}'\n",
		"posted": "#!/bin/sh\nprintf '%s' '{}'\n",
	}
	for _, name := range []string{"normalize", "reply", "posted", "read", "open", "opened", "state"} {
		if _, ok := scripts[name]; !ok {
			scripts[name] = "#!/bin/sh\necho 'no " + name + " in this test' >&2; exit 1\n"
		}
	}
	return scripts
}

// openYard is a store with the operator jo, a crew member yardmaster that hears of
// what it is involved in in both sources, and its session: the pane names
// it, and holds its credentials. allowed is its open_threads setting.
// ghBase is the fake's address, which the declared github source is pointed
// at ("" keeps the pack's own base, for a test that asks nothing of it).
// It returns the app, the store, the owned store, and the directory acme's
// commands record in.
func openYard(t *testing.T, allowed, ghBase string) (*app, store.API, *store.Store, string) {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	home, declDir, dir := t.TempDir(), t.TempDir(), t.TempDir()
	scripts := openScripts(dir)
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(declDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d, err := source.Parse("acme.yaml", []byte(acmeCLI))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
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
	if _, err := st.PutSource(ctx, "acme.yaml", acmeCLI, "jo"); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(ghBase, "http") {
		needPackTools(t)
	}
	declareGitHubPack(t, st, home, ghBase, "jo")
	for _, src := range []string{"acme", "github"} {
		if err := st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: src, Scope: store.ScopeInvolved}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	// The token alone: an App in the environment goes before it, and
	// TestMain leaves none.
	t.Setenv("GITHUB_TOKEN", "gh-yardmaster")
	t.Setenv("YARDR_CREW", "yardmaster")
	return &app{st: st, actor: "yardmaster", cfg: &config.Config{Home: home}}, st, own, dir
}

// acmeIssues is how many issues acme's open left in dir, one per key.
func acmeIssues(t *testing.T, dir string) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "id.*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
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

// A crew member opens a thread as itself in a declared source and in
// GitHub. The thread is its own at once: what someone else says there,
// with no mention, reaches its inbox, and yardr reply answers it in the
// thread.
func TestThreadOpen(t *testing.T) {
	f, ghBase := fakeSourceAPIs(t)
	a, st, own, dir := openYard(t, "acme,github", ghBase)
	ctx := context.Background()

	cases := []struct {
		source, in, title, url, thread string
	}{
		{"acme", "ENG", "Web view: train", "https://acme.test/issue/ACME-1", "issue-1"},
		// The repository as typed, the thread as GitHub spells it.
		{"github", "acme/webapp", "Retries drop the last attempt", "https://github.com/Acme/webapp/issues/1", "Acme/webapp#1"},
	}
	for i, c := range cases {
		out, err := run(t, a.threadOpenCmd(), c.source, "--in", c.in, c.title, "-b", "What it is for.\n")
		if err != nil {
			t.Fatalf("%s: %v", c.source, err)
		}
		if out != c.url+"\n" {
			t.Fatalf("%s: out %q", c.source, out)
		}
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
		if p.Thread != c.thread || p.How != store.ParticipantOpened || p.Title != c.title || p.URL != c.url {
			t.Fatalf("%s: participation %+v, want thread %q", c.source, p, c.thread)
		}
		if n := kinds(t, st, "thread_opened"); n != i+1 {
			t.Fatalf("%s: %d thread_opened events", c.source, n)
		}

		// Someone answers in the thread and mentions no one.
		env := hooks.Envelope{Source: c.source, Type: "comment", Action: "create", Actor: hooks.Party{ID: "u-jane", Name: "Jane"},
			Thread: hooks.Thread{ID: c.thread, Title: c.title, URL: c.url}, Message: "c1", Text: "Which release?"}
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
		if c.source == "github" {
			if len(f.comments) != 1 || !strings.HasPrefix(f.comments[0], c.thread+": The next one.") {
				t.Fatalf("%s: comments %q", c.source, f.comments)
			}
		} else if in := lastStdin(t, dir, "reply"); in["thread"] != c.thread || in["body"] != "The next one." {
			t.Fatalf("%s: reply stdin %v", c.source, in)
		}
	}
	var first map[string]any
	{
		raw, err := os.ReadFile(filepath.Join(dir, "open.stdin"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		if len(lines) != 1 {
			t.Fatalf("open ran %d times", len(lines))
		}
		if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
			t.Fatal(err)
		}
	}
	if first["in"] != "ENG" || first["title"] != "Web view: train" || first["body"] != "What it is for." || !uuid4.MatchString(fmt.Sprint(first["key"])) {
		t.Fatalf("open stdin %v", first)
	}
	if len(f.github) != 1 || f.github[0]["title"] != "Retries drop the last attempt" ||
		!strings.HasPrefix(f.github[0]["body"].(string), "What it is for.\n\n<!-- yardr-thread ") {
		t.Fatalf("github issues: %v", f.github)
	}

	// The body from stdin, and the result as JSON.
	a.json = true
	c := a.threadOpenCmd()
	c.SetIn(strings.NewReader("From a file.\n"))
	out, err := run(t, c, "acme", "--in", "ENG", "Second", "-b", "-")
	a.json = false
	var res map[string]any
	in := lastStdin(t, dir, "open")
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res["repeated"] != false || res["source"] != "acme" ||
		res["title"] != "Second" || res["url"] != "https://acme.test/issue/ACME-2" || res["thread"] != "issue-2" || in["body"] != "From a file." {
		t.Fatalf("json: %q, %v, stdin %v", out, err, in)
	}
}

// Run again after a lost answer, yardr thread open finds the thread the
// first run made and opens no second one.
func TestThreadOpenRetryDoesNotOpenTwice(t *testing.T) {
	for _, c := range []struct{ source, in, url string }{
		{"acme", "ENG", "https://acme.test/issue/ACME-1"},
		{"github", "Acme/webapp", "https://github.com/Acme/webapp/issues/1"},
	} {
		t.Run(c.source, func(t *testing.T) {
			f, ghBase := fakeSourceAPIs(t)
			a, st, _, dir := openYard(t, c.source, ghBase)
			issues := func() int { return acmeIssues(t, dir) + len(f.github) }
			open := func(title string) (string, error) {
				return run(t, a.threadOpenCmd(), c.source, "--in", c.in, title, "-b", "Body.")
			}
			wantErr := "504"
			if c.source == "acme" {
				wantErr = "upstream timed out"
			}
			lose := func(on bool) {
				t.Helper()
				if c.source == "acme" {
					if on {
						if err := os.WriteFile(filepath.Join(dir, "lose"), nil, 0o644); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Remove(filepath.Join(dir, "lose")); err != nil {
						t.Fatal(err)
					}
				} else {
					f.lose = on
				}
			}

			// The answer is lost: the source made the issue, the call failed.
			lose(true)
			if _, err := open("Train"); err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("lost answer: %v", err)
			}
			lose(false)
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
	f, ghBase := fakeSourceAPIs(t)
	a, st, _, dir := openYard(t, "github", ghBase)
	ctx := context.Background()
	head, _ := st.Head(ctx)
	refused := func(want string, args ...string) {
		t.Helper()
		_, err := run(t, a.threadOpenCmd(), args...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: got %v, want %q", args, err, want)
		}
		if len(f.github) != 0 || acmeIssues(t, dir) != 0 {
			t.Fatalf("%v: opened %v, acme %d", args, f.github, acmeIssues(t, dir))
		}
		if now, _ := st.Head(ctx); now != head {
			t.Fatalf("%v: the refusal logged %d events", args, now-head)
		}
	}
	refused("crew member yardmaster may not open threads in acme", "acme", "--in", "ENG", "Train")
	refused("yardr crew set yardmaster open_threads=github,acme", "acme", "--in", "ENG", "Train")
	refused("yardr cannot open threads in token (it can in acme, github)", "token", "--in", "x", "Train")
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
	a, st, _, _ := openYard(t, "", "")
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
		err := add(name, "kind=claude", "open_threads=acme")
		if err == nil || !strings.Contains(err.Error(), "only the operator (jo) may let "+name+" open threads in acme; this is yardmaster") {
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
	if err := add("yardmaster", "kind=claude", "open_threads=acme"); err != nil || may("yardmaster") != "acme" {
		t.Fatalf("the operator: %v, %q", err, may("yardmaster"))
	}
	for _, bad := range []string{"open_threads=token", "open_threads=acme,slack", "open_threads=", "open_threads=true"} {
		if err := add("yardmaster", "kind=claude", bad); err == nil || !strings.Contains(err.Error(), "--set open_threads") {
			t.Fatalf("%s: %v", bad, err)
		}
	}

	// The crew member saves its definition with what it already has, but not
	// with more; without the setting, it has given it up.
	a.actor = "yardmaster"
	if err := add("yardmaster", "kind=claude", "model=opus", "open_threads=acme"); err != nil || may("yardmaster") != "acme" {
		t.Fatalf("keeping it: %v, %q", err, may("yardmaster"))
	}
	if err := add("yardmaster", "kind=claude", "open_threads=acme,github"); err == nil || !strings.Contains(err.Error(), "open threads in github;") {
		t.Fatalf("widening it: %v", err)
	}
	if err := add("yardmaster", "kind=claude"); err != nil || may("yardmaster") != "" {
		t.Fatalf("dropping it: %v, %q", err, may("yardmaster"))
	}
	if err := add("yardmaster", "kind=claude", "open_threads=acme"); err == nil {
		t.Fatal("taking it back after dropping it")
	}

	// crew show says what is allowed.
	a.actor = "jo"
	if err := add("yardmaster", "kind=claude", "open_threads=acme,github"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.crewCmd(), "show", "yardmaster")
	if err != nil || !strings.Contains(out, "may open threads in acme, github\n") {
		t.Fatalf("show: %q, %v", out, err)
	}
}

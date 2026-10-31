package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

// fakeStates stands in for GitHub's REST API as yardr thread state uses
// it: one issue, Acme/webapp#7. It takes only its crew member's
// credential.
type fakeStates struct {
	github  string   // #7's state
	labels  []string // #7's labels
	calls   int      // every request
	changes []string
}

func fakeStateAPIs(t *testing.T) (*fakeStates, string) {
	t.Helper()
	f := &fakeStates{github: "open"}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls++
		if r.Header.Get("Authorization") != "Bearer gh-yardmaster" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if strings.ToLower(r.URL.Path) == "/repos/acme/webapp/issues/7/labels" && r.Method == http.MethodPost {
			var in struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.labels = append(f.labels, in.Labels...)
			f.changes = append(f.changes, "github label "+strings.Join(in.Labels, ","))
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		if strings.ToLower(r.URL.Path) != "/repos/acme/webapp/issues/7" || r.Method != http.MethodGet && r.Method != http.MethodPatch {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		labels := []map[string]string{}
		for _, l := range f.labels {
			labels = append(labels, map[string]string{"name": l})
		}
		if r.Method == http.MethodPatch {
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.github = in["state"]
			f.changes = append(f.changes, strings.TrimSpace("github "+in["state"]+" "+in["state_reason"]))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "title": "Retries", "state": f.github, "labels": labels,
			"html_url": "https://github.com/Acme/webapp/issues/7", "repository_url": "https://api.github.com/repos/Acme/webapp"})
	}))
	t.Cleanup(gh.Close)
	return f, gh.URL
}

// stateScripts answers an acme thread's state command from dir: the one
// issue's status, kept in dir/acme.status from Todo, moved exactly.
func stateScripts(dir string) map[string]string {
	scripts := map[string]string{
		"normalize": "#!/bin/sh\nprintf '%s' '{\"type\":\"ping\"}'\n",
		"state": "#!/bin/sh\nin=$(cat)\n" +
			"printf '%s\\n' \"$in\" >> " + dir + "/state.stdin\n" +
			"[ \"$ACME_TOKEN\" = tok-yardmaster ] || { echo 'Authentication required' >&2; exit 1; }\n" +
			"word=$(printf '%s' \"$in\" | grep -o '\"state\":\"[^\"]*\"' | head -1 | cut -d'\"' -f4)\n" +
			"cur=$(cat " + dir + "/acme.status 2>/dev/null || printf Todo)\n" +
			"case $word in\n" +
			"  started) want='In Progress';;\n" +
			"  review) want='In Review';;\n" +
			"  done) want='Done';;\n" +
			"esac\n" +
			"if [ \"$cur\" = \"$want\" ]; then\n" +
			"  printf '{\"status\":\"%s\",\"moved\":false,\"was\":\"%s\"}' \"$cur\" \"$cur\"\n" +
			"else\n" +
			"  printf '{\"status\":\"%s\",\"moved\":true,\"was\":\"%s\"}' \"$want\" \"$cur\"\n" +
			"  printf '%s' \"$want\" > " + dir + "/acme.status\n" +
			"fi\n",
	}
	for _, name := range []string{"normalize", "reply", "posted", "read", "open", "opened", "state"} {
		if _, ok := scripts[name]; !ok {
			scripts[name] = "#!/bin/sh\necho 'no " + name + " in this test' >&2; exit 1\n"
		}
	}
	return scripts
}

// stateYard is openYard for yardr thread state: allowed is the yardmaster's
// set_state setting, it is someone in both sources, and it takes part in
// the acme issue and the GitHub issue the fake holds. ghBase is the fake's
// address, which the declared github source is pointed at ("" keeps the
// pack's own base, for a test that asks nothing of it). It returns the app,
// the store, the owned store, and the directory acme's commands record in.
func stateYard(t *testing.T, allowed, ghBase string) (*app, store.API, *store.Store, string) {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	home, declDir, dir := t.TempDir(), t.TempDir(), t.TempDir()
	scripts := stateScripts(dir)
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
		def.Config[store.SetState] = allowed
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
	for src, id := range map[string]store.Identity{"acme": {ID: "u-yardmaster", Key: "yardmaster"}, "github": {ID: "yardmaster-bot"}} {
		if err := st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: src, Scope: store.ScopeInvolved}, "jo"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetIdentity(ctx, "yardmaster", src, id, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	for src, thread := range map[string]string{"acme": "ACME-1", "github": "Acme/webapp#7"} {
		if err := st.FollowThread(ctx, "yardmaster", src, thread, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	// The token alone: an App in the environment goes before it, and
	// TestMain leaves none.
	t.Setenv("GITHUB_TOKEN", "gh-yardmaster")
	return &app{st: st, actor: "yardmaster", cfg: &config.Config{Home: home}, env: testEnv(t, "", "YARDR_CREW=yardmaster")}, st, own, dir
}

// acmeStatus is the status acme's state command last left.
func acmeStatus(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "acme.status"))
	if os.IsNotExist(err) {
		return "Todo"
	} else if err != nil {
		t.Fatal(err)
	}
	return string(raw)
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

// A crew member moves a declared source's issue it takes part in through
// started, review and done, naming it by a delivery's seq, by its id, and
// by source and id. Each change is one request and one thread_state
// event; an issue already there gets neither. The delivery of its own
// change stays out of its inbox.
func TestThreadStateAcme(t *testing.T) {
	a, st, own, dir := stateYard(t, "acme", "")
	ctx := context.Background()

	// Jane comments on the issue: the delivery whose seq names the thread.
	comment := hooks.Envelope{Source: "acme", Type: "comment", Action: "create", Actor: hooks.Party{ID: "u-jane", Name: "Jane"},
		Thread: hooks.Thread{ID: "ACME-1", Title: "Web view"}, Message: "c1", Text: "Is anyone on this?"}
	seq, _, err := own.RecordHook(ctx, "acme", comment.Map(), []byte("{}"), nil)
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
		args        []string
		status, was string
	}{
		{[]string{strconv.FormatUint(seq, 10), "started"}, "In Progress", "Todo"},
		{[]string{"ACME-1", "Review"}, "In Review", "In Progress"},
		{[]string{"--source", "acme", "ACME-1", "done"}, "Done", "In Review"},
	} {
		word := strings.ToLower(c.args[len(c.args)-1])
		out, warned, err := state(t, a, c.args...)
		if want := "acme thread ACME-1 is now " + c.status + " (was " + c.was + ")\n"; err != nil || out != want || warned != "" {
			t.Fatalf("%v: %q, %q, %v\nwant %q", c.args, out, warned, err, want)
		}
		if got := acmeStatus(t, dir); got != c.status || kinds(t, st, "thread_state") != i+1 {
			t.Fatalf("%v: acme is %s with %d events", c.args, got, kinds(t, st, "thread_state"))
		}
		e := lastEvent(t, st, "thread_state")
		want := map[string]any{"crew": "yardmaster", "source": "acme", "thread": "ACME-1", "state": word, "status": c.status, "was": c.was}
		if e.Actor != "yardmaster" || !reflect.DeepEqual(e.Data, want) {
			t.Fatalf("%v: event by %s: %v\nwant %v", c.args, e.Actor, e.Data, want)
		}

		// The same again: the issue is there, so nothing is sent to
		// change it and nothing is logged.
		head, _ := st.Head(ctx)
		runs := stdinRuns(t, dir, "state")
		out, _, err = state(t, a, c.args...)
		if want := "acme thread ACME-1 was already " + c.status + "; nothing set\n"; err != nil || out != want {
			t.Fatalf("%v again: %q, %v", c.args, out, err)
		}
		if now, _ := st.Head(ctx); now != head || kinds(t, st, "thread_state") != i+1 || stdinRuns(t, dir, "state") != runs+1 {
			t.Fatalf("%v again: %d events, %d requests", c.args, now-head, stdinRuns(t, dir, "state")-runs)
		}

		// The source tells of the change, made by the crew member's own user: it
		// does not reach the crew member, as a change by someone else does.
		change := hooks.Envelope{Source: "acme", Type: "issue", Action: "update", Actor: hooks.Party{ID: "u-yardmaster", Name: "Yardmaster"},
			Thread: hooks.Thread{ID: "ACME-1", Title: "Web view"}, Text: "changed status", Entity: "ACME-1"}
		if _, _, err := own.RecordHook(ctx, "acme", change.Map(), []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
		if inbox() != 1 {
			t.Fatalf("%v: the delivery of its own change reached its inbox", c.args)
		}
	}
	change := hooks.Envelope{Source: "acme", Type: "issue", Action: "update", Actor: hooks.Party{ID: "u-jane", Name: "Jane"},
		Thread: hooks.Thread{ID: "ACME-1"}, Text: "changed status", Entity: "ACME-1"}
	if _, _, err := own.RecordHook(ctx, "acme", change.Map(), []byte("{}"), nil); err != nil || inbox() != 2 {
		t.Fatalf("a change by someone else: inbox %d, %v", inbox(), err)
	}

	// --json prints the event's fields, and whether anything changed. The
	// operator speaks as the crew member: the event names who did.
	a.json, a.actor = true, "jo"
	a.env["YARDR_CREW"] = ""
	for _, c := range []struct {
		state, status string
		was           string
		changed       bool
	}{
		{"started", "In Progress", "Done", true},
		{"done", "Done", "In Progress", true},
		{"done", "Done", "", false},
	} {
		out, _, err := state(t, a, "ACME-1", c.state, "--crew", "yardmaster")
		var res map[string]any
		want := map[string]any{"crew": "yardmaster", "source": "acme", "thread": "ACME-1", "state": c.state, "status": c.status,
			"actor": "jo", "changed": c.changed}
		if c.changed {
			want["was"] = c.was
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
	if err := st.SetIdentity(ctx, "yardmaster", "acme", store.Identity{}, "jo"); err != nil {
		t.Fatal(err)
	}
	_, warned, err := state(t, a, "ACME-1", "started", "--crew", "yardmaster")
	if err != nil || !strings.Contains(warned, "yardmaster has no acme identity, so the delivery of this change may reach its own inbox: yardr crew identity yardmaster --source acme") {
		t.Fatalf("no identity: %q, %v", warned, err)
	}
	if _, warned, err = state(t, a, "ACME-1", "started", "--crew", "yardmaster"); err != nil || warned != "" {
		t.Fatalf("no identity, nothing changed: %q, %v", warned, err)
	}
}

// On GitHub done closes the issue as completed and started reopens it;
// either asked twice is one request that changes something. review is
// refused with the words GitHub has, before GitHub is asked.
func TestThreadStateGitHub(t *testing.T) {
	f, ghBase := fakeStateAPIs(t)
	a, st, own, _ := stateYard(t, "github", ghBase)
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
			"status": c.status, "was": c.was}
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
	if err == nil || !strings.Contains(err.Error(), `"review" is no state of github: want started, done`) {
		t.Fatalf("review: %v", err)
	}
	if now, _ := st.Head(ctx); now != head || f.calls != calls {
		t.Fatalf("review: %d events, %d requests", now-head, f.calls-calls)
	}
}

// Off unless the crew member's definition allows the source, only in a thread
// it takes part in, and only from its own session or the operator's. No
// refusal reaches a source or leaves an event.
// A label is a word of thread state on GitHub: it is added with its name as
// typed, logged as a thread_state, and left alone when the thread has it.
func TestThreadStateGitHubLabel(t *testing.T) {
	f, ghBase := fakeStateAPIs(t)
	a, st, _, _ := stateYard(t, "github", ghBase)
	out, _, err := state(t, a, "--source", "github", "Acme/webapp#7", "label:In Review")
	if want := "github thread Acme/webapp#7 is now labeled In Review\n"; err != nil || out != want {
		t.Fatalf("label: %q, %v", out, err)
	}
	if !reflect.DeepEqual(f.changes, []string{"github label In Review"}) || f.github != "open" {
		t.Fatalf("changes %q, the issue %s", f.changes, f.github)
	}
	if e := lastEvent(t, st, "thread_state"); e.Data["state"] != "label:In Review" || e.Data["status"] != "labeled In Review" || e.Data["was"] != nil {
		t.Fatalf("event %v", e.Data)
	}
	out, _, err = state(t, a, "--source", "github", "Acme/webapp#7", "label:in review")
	if want := "github thread Acme/webapp#7 was already labeled In Review; nothing set\n"; err != nil || out != want ||
		len(f.changes) != 1 || kinds(t, st, "thread_state") != 1 {
		t.Fatalf("again: %q, %v, changes %q", out, err, f.changes)
	}
	// A declared source takes none, and nothing is asked of it.
	a2, _, _, dir := stateYard(t, "acme", "")
	if _, _, err := state(t, a2, "--source", "acme", "ACME-1", "label:in-review"); err == nil ||
		!strings.Contains(err.Error(), "acme: yardr sets no label there") || stdinRuns(t, dir, "state") != 0 {
		t.Fatalf("acme: %v, %d requests", err, stdinRuns(t, dir, "state"))
	}
}

func TestThreadStateRefusals(t *testing.T) {
	f, ghBase := fakeStateAPIs(t)
	a, st, own, dir := stateYard(t, "github", ghBase)
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
	acme := record(hooks.Envelope{Source: "acme", Type: "comment", Action: "create", Actor: hooks.Party{ID: "u-jane"},
		Thread: hooks.Thread{ID: "ACME-1"}, Message: "c1"})
	token := record(hooks.Envelope{Source: "token", Type: "backup", Actor: hooks.Party{ID: "cron"}, Thread: hooks.Thread{ID: "nightly"}})
	// A CI run on no pull request is on its branch's thread, which the
	// pack's link shape does not read as an issue or pull request.
	branch := record(hooks.Envelope{Source: "github", Type: "workflow_run", Action: "completed", Actor: hooks.Party{ID: "yardmaster-bot"},
		Thread: hooks.Thread{ID: "Acme/webapp@main", Title: "CI on Acme/webapp@main", URL: "https://github.com/Acme/webapp/tree/main"}})
	if err := st.FollowThread(ctx, "yardmaster", "acme", "Acme/webapp#7", "jo"); err != nil {
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
		if f.calls != 0 || stdinRuns(t, dir, "state") != 0 {
			t.Fatalf("%v: reached a source", args)
		}
		if now, _ := st.Head(ctx); now != head {
			t.Fatalf("%v: the refusal logged %d events", args, now-head)
		}
	}
	// Without the setting for the source, however the thread is named.
	for _, args := range [][]string{{acme, "started"}, {"ACME-1", "started"}, {"--source", "acme", "ACME-1", "started"}} {
		refused("crew member yardmaster may not set the status of threads in acme", args...)
		refused("yardr crew set yardmaster set_state=github,acme", args...)
	}
	// Allowed in the source, but no part in the thread.
	refused("crew member yardmaster takes no part in github thread Acme/webapp#8", elsewhere, "done")
	refused("yardr thread follow yardmaster --source github Acme/webapp#8", elsewhere, "done")
	refused("yardr thread follow yardmaster --source github Acme/webapp#9", "--source", "github", "Acme/webapp#9", "done")
	refused("crew member yardmaster takes no part in a thread Acme/webapp#9", "Acme/webapp#9", "done")
	refused("yardr thread follow yardmaster --source <source> Acme/webapp#9", "Acme/webapp#9", "done")
	// An id it follows in two sources needs its source.
	refused("takes part in a thread Acme/webapp#7 in acme and github: say which with --source", "Acme/webapp#7", "done")
	// What is no state, no thread, no source with statuses.
	refused(`"closed" is no state: want started, review, done`, "Acme/webapp#7", "closed")
	refused("yardr cannot set the status of a thread in token (it can in acme, github)", "--source", "token", "nightly", "done")
	refused("which yardr cannot answer in", token, "done")
	refused("event "+branch+" is on a thread that is no issue or pull request, so it has no status to set", branch, "done")
	refused("thread Acme/webapp@main is on a thread that is no issue or pull request, so it has no status to set", "Acme/webapp@main", "done")
	refused("thread Acme/webapp@main is on a thread that is no issue or pull request, so it has no status to set", "--source", "github", "Acme/webapp@main", "done")
	refused(`--source "slack": one of`, "--source", "slack", "C1", "done")
	refused("is a mail event, not a hook delivery", strconv.FormatUint(mail, 10), "done")
	refused("event 99999", "99999", "done")
	refused("crew member ghost", "--source", "github", "Acme/webapp#7", "done", "--crew", "ghost")

	// A bead's agent, or another crew member, naming it with --crew.
	a.env["YARDR_CREW"] = ""
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
	a, st, _, _ := stateYard(t, "", "")
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scout", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	crewDir := t.TempDir()
	add := func(name string, sets ...string) error {
		t.Helper()
		args := []string{"add", name, "--replace", "--dir", crewDir}
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
		err := add(name, "kind=claude", "set_state=acme")
		if err == nil || !strings.Contains(err.Error(), "only the operator (jo) may let "+name+" set the status of threads in acme; this is yardmaster") {
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
	if err := add("yardmaster", "kind=claude", "set_state=acme"); err != nil || may("yardmaster") != "acme" {
		t.Fatalf("the operator: %v, %q", err, may("yardmaster"))
	}
	for _, bad := range []string{"set_state=token", "set_state=acme,slack", "set_state=", "set_state=true"} {
		if err := add("yardmaster", "kind=claude", bad); err == nil || !strings.Contains(err.Error(), "--set set_state") {
			t.Fatalf("%s: %v", bad, err)
		}
	}

	// The crew member saves its definition with what it already has, but not
	// with more; without the setting, it has given it up. Being allowed to
	// open threads is no leave to set their status, nor the other way.
	a.actor = "yardmaster"
	if err := add("yardmaster", "kind=claude", "model=opus", "set_state=acme"); err != nil || may("yardmaster") != "acme" {
		t.Fatalf("keeping it: %v, %q", err, may("yardmaster"))
	}
	if err := add("yardmaster", "kind=claude", "set_state=acme,github"); err == nil || !strings.Contains(err.Error(), "set the status of threads in github;") {
		t.Fatalf("widening it: %v", err)
	}
	if err := add("yardmaster", "kind=claude", "set_state=acme", "open_threads=acme"); err == nil || !strings.Contains(err.Error(), "open threads in acme;") {
		t.Fatalf("taking the other setting: %v", err)
	}
	if err := add("yardmaster", "kind=claude"); err != nil || may("yardmaster") != "" {
		t.Fatalf("dropping it: %v, %q", err, may("yardmaster"))
	}
	if err := add("yardmaster", "kind=claude", "set_state=acme"); err == nil {
		t.Fatal("taking it back after dropping it")
	}

	// crew show says what is allowed.
	a.actor = "jo"
	if err := add("yardmaster", "kind=claude", "set_state=acme,github"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.crewCmd(), "show", "yardmaster")
	if err != nil || !strings.Contains(out, "may set the status of threads in acme, github\n") || strings.Contains(out, "may open threads") {
		t.Fatalf("show: %q, %v", out, err)
	}
}

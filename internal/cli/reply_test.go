package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// fakeLinear stands in for Linear's GraphQL API: an issue whose comment
// c1 is answered by c2. It records the commentCreate inputs it gets, and
// the comments they made are looked up by id afterwards. With lose set,
// a commentCreate makes its comment and then answers with a 504, as a
// post whose answer never arrived looks to the caller.
type fakeLinear struct {
	created []map[string]any
	posted  map[string]map[string]any
	lose    bool
}

func fakeLinearAPI(t *testing.T) *fakeLinear {
	t.Helper()
	f := &fakeLinear{posted: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Header.Get("Authorization") != "lin_api_yardmaster" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "Authentication required"}}})
			return
		}
		var data any
		switch {
		case strings.Contains(req.Query, "commentCreate"):
			in, _ := req.Variables["input"].(map[string]any)
			f.created = append(f.created, in)
			id, _ := in["id"].(string)
			if id == "" {
				id = "c9"
			}
			c := map[string]any{"id": id, "body": in["body"], "url": fmt.Sprint("https://linear.app/acme/issue/ENG-1#comment-", id),
				"createdAt": "2026-10-03T12:00:00Z", "issue": map[string]any{"id": in["issueId"]}}
			f.posted[id] = c
			if f.lose {
				http.Error(w, "upstream timed out", http.StatusGatewayTimeout)
				return
			}
			data = map[string]any{"commentCreate": map[string]any{"success": true,
				"comment": map[string]any{"id": id, "url": c["url"]}}}
		case strings.Contains(req.Query, "comment(id"):
			id, _ := req.Variables["id"].(string)
			if c, ok := f.posted[id]; ok {
				data = map[string]any{"comment": c}
				break
			}
			if id != "c1" && id != "c2" {
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "Entity not found: Comment"}}})
				return
			}
			data = map[string]any{"comment": map[string]any{"id": id, "parent": nil,
				"issue": map[string]any{"id": "issue-1"}}}
		case strings.Contains(req.Query, "comments("):
			data = map[string]any{"issue": map[string]any{"comments": map[string]any{
				"nodes": []any{
					map[string]any{"id": "c1", "body": "@yardmaster can you look?\nit drops retries", "createdAt": "2026-10-03T10:00:00Z",
						"url": "https://linear.app/acme/issue/ENG-1#comment-c1", "user": map[string]any{"id": "u-jane", "name": "Jane"}},
					map[string]any{"id": "c2", "body": "ping", "createdAt": "2026-10-03T10:05:00Z",
						"url":    "https://linear.app/acme/issue/ENG-1#comment-c2",
						"parent": map[string]any{"id": "c1"}, "user": map[string]any{"id": "u-ann", "name": "Ann"}},
				},
				"pageInfo": map[string]any{"hasNextPage": false}}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	old := hooks.LinearAPI
	hooks.LinearAPI = srv.URL
	t.Cleanup(func() { hooks.LinearAPI = old })
	return f
}

// replyYard is a store with crew member yardmaster and three events to answer: a
// Linear reply c2 (under c1), a token message, and yardmaster's own creation.
func replyYard(t *testing.T) (*app, store.API, map[string]string) {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	record := func(name string, env hooks.Envelope) string {
		seq, _, err := own.RecordHook(ctx, name, env.Map(), []byte("{}"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatUint(seq, 10)
	}
	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seqs := map[string]string{
		"bead": strconv.FormatUint(head, 10),
		"linear": record("linear", hooks.Envelope{Source: "linear", Type: "comment", Action: "create",
			Actor:   hooks.Party{ID: "u-ann", Name: "Ann"},
			Thread:  hooks.Thread{ID: "issue-1", Title: "Retries", URL: "https://linear.app/acme/issue/ENG-1"},
			Message: "c2", Parent: "c1"}),
		"token": record("cron", hooks.Envelope{Source: "token", Type: "message", Text: "backup ok"}),
		"top": record("linear", hooks.Envelope{Source: "linear", Type: "comment", Action: "create",
			Thread: hooks.Thread{ID: "issue-1", Title: "Retries"}, Message: "c1"}),
		"issue": record("linear", hooks.Envelope{Source: "linear", Type: "issue", Action: "create",
			Thread: hooks.Thread{ID: "issue-1", Title: "Retries"}}),
		// c2 as delivered before envelopes carried the comment's id.
		"old": record("linear", hooks.Envelope{Source: "linear", Type: "comment", Action: "create",
			Thread: hooks.Thread{ID: "issue-1", Title: "Retries"}, URL: "https://linear.app/acme/issue/ENG-1-retries#comment-c2"}),
	}
	return &app{st: st, actor: "yard-10-new"}, st, seqs
}

// uuid4 is the form of a reply's key: Linear takes it as the comment's id.
var uuid4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestReply(t *testing.T) {
	f := fakeLinearAPI(t)
	a, st, seqs := replyYard(t)
	ctx := context.Background()
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster")
	t.Setenv("YARDR_CREW", "yardmaster")

	out, err := run(t, a.replyCmd(), seqs["linear"], "Filed as yard-k3f9.")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 {
		t.Fatalf("created %v", f.created)
	}
	// The delivered comment is a reply to c1, so the answer goes under c1,
	// with the key the store chose as its id.
	in := f.created[0]
	key, _ := in["id"].(string)
	if in["parentId"] != "c1" || in["issueId"] != "issue-1" || in["body"] != "Filed as yard-k3f9." || !uuid4.MatchString(key) {
		t.Fatalf("commentCreate input %v", in)
	}
	url := "https://linear.app/acme/issue/ENG-1#comment-" + key
	if out != url+"\n" {
		t.Fatalf("output %q", out)
	}
	ps, err := st.Threads(ctx, "yardmaster")
	if err != nil || len(ps) != 1 || ps[0].Thread != "issue-1" || ps[0].How != store.ParticipantReplied || ps[0].Title != "Retries" {
		t.Fatalf("threads %+v %v", ps, err)
	}
	evs, _ := st.EventsSince(ctx, 0, 0)
	last := evs[len(evs)-1]
	if last.Kind != "replied" || last.Actor != "yardmaster" || last.Data["source"] != "linear" || last.Data["thread"] != "issue-1" ||
		last.Data["url"] != url || last.Data["message"] != key || fmt.Sprint(last.Data["seq"]) != seqs["linear"] {
		t.Fatalf("last event %+v", last)
	}

	// From stdin, under the root of --to's conversation, as JSON.
	a.json = true
	c := a.replyCmd()
	c.SetIn(strings.NewReader("  second answer\n"))
	out, err = run(t, c, seqs["linear"], "-", "--to", "c1")
	a.json = false
	if err != nil {
		t.Fatal(err)
	}
	// c1 is a top-level comment, so it is its own root.
	if len(f.created) != 2 || f.created[1]["body"] != "second answer" || f.created[1]["parentId"] != "c1" {
		t.Fatalf("created %v", f.created)
	}
	var res map[string]any
	if json.Unmarshal([]byte(out), &res) != nil || res["url"] != "https://linear.app/acme/issue/ENG-1#comment-"+fmt.Sprint(f.created[1]["id"]) ||
		res["crew"] != "yardmaster" || res["message"] != f.created[1]["id"] || res["repeated"] != false {
		t.Fatalf("json %q", out)
	}
}

// A reply run again is not posted again: one the store knows was posted
// is answered from the record; one whose answer was lost (the source took
// the post, the call failed) is found in the source by its key, recorded
// then, and not posted a second time. Another text, or another
// conversation, is another reply.
func TestReplyRetryDoesNotPostTwice(t *testing.T) {
	f := fakeLinearAPI(t)
	a, st, seqs := replyYard(t)
	ctx := context.Background()
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster")
	t.Setenv("YARDR_CREW", "yardmaster")
	replied := func() int {
		t.Helper()
		evs, err := st.EventsSince(ctx, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range evs {
			if e.Kind == "replied" {
				n++
			}
		}
		return n
	}

	first, err := run(t, a.replyCmd(), seqs["linear"], "Filed.")
	if err != nil {
		t.Fatal(err)
	}
	again, err := run(t, a.replyCmd(), seqs["linear"], "Filed.")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || again != first || replied() != 1 {
		t.Fatalf("a recorded reply run again: created %d, out %q vs %q, %d replied events", len(f.created), again, first, replied())
	}

	// The answer is lost: Linear made the comment, the call failed.
	f.lose = true
	if _, err := run(t, a.replyCmd(), seqs["linear"], "Second."); err == nil || !strings.Contains(err.Error(), "504") {
		t.Fatalf("lost answer: %v", err)
	}
	f.lose = false
	if len(f.created) != 2 || replied() != 1 {
		t.Fatalf("after the lost answer: created %d, %d replied events", len(f.created), replied())
	}
	out, err := run(t, a.replyCmd(), seqs["linear"], "Second.")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := f.created[1]["id"].(string)
	if len(f.created) != 2 || out != "https://linear.app/acme/issue/ENG-1#comment-"+key+"\n" || replied() != 2 {
		t.Fatalf("retry after the lost answer: created %d, out %q, %d replied events", len(f.created), out, replied())
	}
	a.json = true
	out, err = run(t, a.replyCmd(), seqs["linear"], "Second.")
	a.json = false
	var res map[string]any
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res["repeated"] != true || res["message"] != key {
		t.Fatalf("json of a repeat: %q, %v", out, err)
	}

	// Another text, and the same text under another conversation, are new
	// replies with keys of their own.
	if _, err := run(t, a.replyCmd(), seqs["linear"], "Third."); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["linear"], "Third.", "--to", "c1"); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 4 || f.created[2]["id"] == f.created[3]["id"] || f.created[3]["id"] == key {
		t.Fatalf("new replies: %v", f.created)
	}
}

// Speaking as a crew member takes its own session or the operator: a bead's
// agent naming it with --crew is refused before anything is posted.
func TestReplyOnlyByTheCrewOrOperator(t *testing.T) {
	f := fakeLinearAPI(t)
	a, st, seqs := replyYard(t) // acting as yard-10-new, a bead's agent
	ctx := context.Background()
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster")
	t.Setenv("YARDR_CREW", "")
	if err := st.SetSetting(ctx, "operator", "jo", "me"); err != nil {
		t.Fatal(err)
	}
	head, _ := st.Head(ctx)

	_, err := run(t, a.replyCmd(), seqs["linear"], "x", "--crew", "yardmaster")
	if err == nil || !strings.Contains(err.Error(), "only yardmaster's own session or the operator (jo) may speak as yardmaster") || !strings.Contains(err.Error(), "this is yard-10-new") {
		t.Fatalf("a bead's agent: %v", err)
	}
	if len(f.created) != 0 {
		t.Fatalf("posted %v", f.created)
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("refusal logged %d events", now-head)
	}
	// The operator, by the yard's setting.
	if err := st.SetSetting(ctx, "operator", "yard-10-new", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["linear"], "x", "--crew", "yardmaster"); err != nil {
		t.Fatalf("the operator: %v", err)
	}
	if err := st.SetSetting(ctx, "operator", "jo", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["linear"], "y", "--crew", "yardmaster"); err == nil {
		t.Fatal("no longer the operator, still allowed")
	}
	// The crew member's own session: the pane names it in YARDR_CREW and
	// YARDR_ACTOR, and acts as it. That its pane is its own session
	// whatever actor a command there names is the store's to know
	// (TestSpeakingAsACrewMember).
	t.Setenv("YARDR_CREW", "yardmaster")
	a.actor = "yardmaster"
	if _, err := run(t, a.replyCmd(), seqs["linear"], "y"); err != nil {
		t.Fatalf("its own session: %v", err)
	}
	t.Setenv("YARDR_CREW", "")
	if _, err := run(t, a.replyCmd(), seqs["linear"], "z", "--crew", "yardmaster"); err != nil {
		t.Fatalf("acting as the crew member: %v", err)
	}
	if len(f.created) != 3 {
		t.Fatalf("created %v", f.created)
	}
}

// Without --to a reply lands under the conversation of the delivered
// comment: a reply's parent, a top-level comment itself, nothing for an
// issue. An event recorded before envelopes carried the comment's id
// placed its reply at the top level (yard-npq6).
func TestReplyPlacement(t *testing.T) {
	f := fakeLinearAPI(t)
	a, _, seqs := replyYard(t)
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster")
	t.Setenv("YARDR_CREW", "yardmaster")
	for _, c := range []struct {
		event string
		want  any
	}{
		{"linear", "c1"},
		{"top", "c1"},
		{"old", "c1"},
		{"issue", nil},
	} {
		n := len(f.created)
		if _, err := run(t, a.replyCmd(), seqs[c.event], "answer"); err != nil {
			t.Fatalf("%s: %v", c.event, err)
		}
		if len(f.created) != n+1 {
			t.Fatalf("%s: created %v", c.event, f.created)
		}
		if in := f.created[n]; in["parentId"] != c.want || in["issueId"] != "issue-1" {
			t.Errorf("%s: commentCreate input %v, want parentId %v", c.event, in, c.want)
		}
	}
}

// What cannot be answered is refused before anything is posted or
// recorded.
func TestReplyRefusals(t *testing.T) {
	f := fakeLinearAPI(t)
	a, st, seqs := replyYard(t)
	ctx := context.Background()
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster")
	t.Setenv("YARDR_CREW", "yardmaster")
	head, _ := st.Head(ctx)

	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"bead event", []string{seqs["bead"], "x"}, "is a crew_defined event, not a hook delivery"},
		{"token source", []string{seqs["token"], "x"}, "came from token, which yardr cannot answer in (it can in github, linear)"},
		{"no such event", []string{"9999", "x"}, "event 9999: not found"},
		{"not a seq", []string{"abc", "x"}, "want the number an inbox line starts with"},
		{"empty text", []string{seqs["linear"], "  "}, "the reply is empty"},
		{"unknown crew member", []string{seqs["linear"], "x", "--crew", "ghost"}, "crew member ghost: not found"},
	} {
		_, err := run(t, a.replyCmd(), c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// thread show refuses the same seqs.
	for _, s := range []string{seqs["bead"], seqs["token"]} {
		if _, err := run(t, a.threadCmd(), "show", s); err == nil {
			t.Errorf("thread show %s: no error", s)
		}
	}
	// Without the credential in the environment nothing is sent.
	t.Setenv("LINEAR_API_KEY", "")
	if _, err := run(t, a.replyCmd(), seqs["linear"], "x"); err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY is not set") {
		t.Errorf("no key: %v", err)
	}
	// Nor without a crew member to reply as.
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster")
	t.Setenv("YARDR_CREW", "")
	if _, err := run(t, a.replyCmd(), seqs["linear"], "x"); err == nil || !strings.Contains(err.Error(), "--crew is required") {
		t.Errorf("no crew member: %v", err)
	}
	// What the source refuses is the error, and nothing is recorded.
	t.Setenv("YARDR_CREW", "yardmaster")
	t.Setenv("LINEAR_API_KEY", "lin_api_other")
	if _, err := run(t, a.replyCmd(), seqs["linear"], "x"); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Errorf("refused by linear: %v", err)
	}

	if len(f.created) != 0 {
		t.Fatalf("posted %v", f.created)
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("refusals logged %d events", now-head)
	}
	if ps, _ := st.Threads(ctx, "yardmaster"); len(ps) != 0 {
		t.Fatalf("threads %v", ps)
	}
}

func TestThreadShow(t *testing.T) {
	fakeLinearAPI(t)
	a, _, seqs := replyYard(t)
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster")

	out, err := run(t, a.threadCmd(), "show", seqs["linear"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`linear thread issue-1 "Retries"  https://linear.app/acme/issue/ENG-1`,
		"2 message(s)",
		"  c1  ", "Jane (u-jane)", "    @yardmaster can you look?\n    it drops retries\n",
		"* c2  ", "Ann (u-ann)  reply to c1\n    ping\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Index(out, " c1 ") > strings.Index(out, " c2 ") {
		t.Errorf("out of order:\n%s", out)
	}

	a.json = true
	out, err = run(t, a.threadCmd(), "show", seqs["linear"])
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Message  string          `json:"message"`
		Messages []hooks.Message `json:"messages"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Message != "c2" || len(res.Messages) != 2 || res.Messages[1].Parent != "c1" {
		t.Fatalf("json %q: %v", out, err)
	}

	t.Setenv("LINEAR_API_KEY", "")
	if _, err := run(t, a.threadCmd(), "show", seqs["linear"]); err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY") {
		t.Errorf("no key: %v", err)
	}
	// An event recorded before envelopes carried the comment's id marks
	// the comment its URL names.
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster")
	a.json = false
	out, err = run(t, a.threadCmd(), "show", seqs["old"])
	if err != nil || !strings.Contains(out, "* c2  ") || strings.Contains(out, "* c1") {
		t.Errorf("old event: %v\n%s", err, out)
	}
	if _, err := run(t, a.threadCmd(), "show", "9999"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown seq: %v", err)
	}
}

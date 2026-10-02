package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// fakeLinearAPI stands in for Linear's GraphQL API: an issue whose comment
// c1 is answered by c2. It records the commentCreate inputs it gets.
func fakeLinearAPI(t *testing.T) *[]map[string]any {
	t.Helper()
	var created []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.Header.Get("Authorization") != "lin_api_mayor" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "Authentication required"}}})
			return
		}
		var data any
		switch {
		case strings.Contains(req.Query, "commentCreate"):
			in, _ := req.Variables["input"].(map[string]any)
			created = append(created, in)
			data = map[string]any{"commentCreate": map[string]any{"success": true,
				"comment": map[string]any{"id": "c9", "url": "https://linear.app/acme/issue/ENG-1#comment-c9"}}}
		case strings.Contains(req.Query, "comment(id"):
			data = map[string]any{"comment": map[string]any{"id": req.Variables["id"], "parent": nil,
				"issue": map[string]any{"id": "issue-1"}}}
		case strings.Contains(req.Query, "comments("):
			data = map[string]any{"issue": map[string]any{"comments": map[string]any{
				"nodes": []any{
					map[string]any{"id": "c1", "body": "@mayor can you look?\nit drops retries", "createdAt": "2026-10-03T10:00:00Z",
						"user": map[string]any{"id": "u-jane", "name": "Jane"}},
					map[string]any{"id": "c2", "body": "ping", "createdAt": "2026-10-03T10:05:00Z",
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
	return &created
}

// replyYard is a store with resident mayor and three events to answer: a
// Linear reply c2 (under c1), a token message, and mayor's own creation.
func replyYard(t *testing.T) (*app, store.API, map[string]string) {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := st.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr"}, "me"); err != nil {
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
	}
	return &app{st: st, actor: "yard-10-new"}, st, seqs
}

func TestReply(t *testing.T) {
	created := fakeLinearAPI(t)
	a, st, seqs := replyYard(t)
	ctx := context.Background()
	t.Setenv("LINEAR_API_KEY", "lin_api_mayor")
	t.Setenv("YARDR_RESIDENT", "mayor")

	out, err := run(t, a.replyCmd(), seqs["linear"], "Filed as yard-k3f9.")
	if err != nil {
		t.Fatal(err)
	}
	if out != "https://linear.app/acme/issue/ENG-1#comment-c9\n" {
		t.Fatalf("output %q", out)
	}
	if len(*created) != 1 {
		t.Fatalf("created %v", *created)
	}
	// The delivered comment is a reply to c1, so the answer goes under c1.
	if in := (*created)[0]; in["parentId"] != "c1" || in["issueId"] != "issue-1" || in["body"] != "Filed as yard-k3f9." {
		t.Fatalf("commentCreate input %v", in)
	}
	ps, err := st.Threads(ctx, "mayor")
	if err != nil || len(ps) != 1 || ps[0].Thread != "issue-1" || ps[0].How != store.ParticipantReplied || ps[0].Title != "Retries" {
		t.Fatalf("threads %+v %v", ps, err)
	}
	evs, _ := st.EventsSince(ctx, 0, 0)
	last := evs[len(evs)-1]
	if last.Kind != "replied" || last.Actor != "mayor" || last.Data["source"] != "linear" || last.Data["thread"] != "issue-1" ||
		last.Data["url"] != "https://linear.app/acme/issue/ENG-1#comment-c9" {
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
	if len(*created) != 2 || (*created)[1]["body"] != "second answer" || (*created)[1]["parentId"] != "c1" {
		t.Fatalf("created %v", *created)
	}
	var res map[string]any
	if json.Unmarshal([]byte(out), &res) != nil || res["url"] != "https://linear.app/acme/issue/ENG-1#comment-c9" || res["resident"] != "mayor" {
		t.Fatalf("json %q", out)
	}
}

// What cannot be answered is refused before anything is posted or
// recorded.
func TestReplyRefusals(t *testing.T) {
	created := fakeLinearAPI(t)
	a, st, seqs := replyYard(t)
	ctx := context.Background()
	t.Setenv("LINEAR_API_KEY", "lin_api_mayor")
	t.Setenv("YARDR_RESIDENT", "mayor")
	head, _ := st.Head(ctx)

	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"bead event", []string{seqs["bead"], "x"}, "is a resident_defined event, not a hook delivery"},
		{"token source", []string{seqs["token"], "x"}, "came from token, which yardr cannot answer in (it can in github, linear)"},
		{"no such event", []string{"9999", "x"}, "event 9999: not found"},
		{"not a seq", []string{"abc", "x"}, "want the number an inbox line starts with"},
		{"empty text", []string{seqs["linear"], "  "}, "the reply is empty"},
		{"unknown resident", []string{seqs["linear"], "x", "--resident", "ghost"}, "resident ghost: not found"},
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
	// Nor without a resident to reply as.
	t.Setenv("LINEAR_API_KEY", "lin_api_mayor")
	t.Setenv("YARDR_RESIDENT", "")
	if _, err := run(t, a.replyCmd(), seqs["linear"], "x"); err == nil || !strings.Contains(err.Error(), "--resident is required") {
		t.Errorf("no resident: %v", err)
	}
	// What the source refuses is the error, and nothing is recorded.
	t.Setenv("YARDR_RESIDENT", "mayor")
	t.Setenv("LINEAR_API_KEY", "lin_api_other")
	if _, err := run(t, a.replyCmd(), seqs["linear"], "x"); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Errorf("refused by linear: %v", err)
	}

	if len(*created) != 0 {
		t.Fatalf("posted %v", *created)
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("refusals logged %d events", now-head)
	}
	if ps, _ := st.Threads(ctx, "mayor"); len(ps) != 0 {
		t.Fatalf("threads %v", ps)
	}
}

func TestThreadShow(t *testing.T) {
	fakeLinearAPI(t)
	a, _, seqs := replyYard(t)
	t.Setenv("LINEAR_API_KEY", "lin_api_mayor")

	out, err := run(t, a.threadCmd(), "show", seqs["linear"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`linear thread issue-1 "Retries"  https://linear.app/acme/issue/ENG-1`,
		"2 message(s)",
		"  c1  ", "Jane (u-jane)", "    @mayor can you look?\n    it drops retries\n",
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
	if _, err := run(t, a.threadCmd(), "show", "9999"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown seq: %v", err)
	}
}

package hooks_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
)

// threadDecl is the adapter for the declared source text says: scripts,
// name to body, are the files beside the declaration's file, copied as
// yardr source add copies them. The commands are a few lines of sh and
// need no jq.
func threadDecl(t *testing.T, text string, scripts map[string]string) hooks.Declared {
	t.Helper()
	home, declDir := t.TempDir(), t.TempDir()
	for name, body := range scripts {
		p := filepath.Join(declDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d, err := source.Parse("q.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFiles(declDir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return hooks.Declared{D: d, Home: home}
}

const acmeThread = `name: acme
base: https://acme.test
credential:
  env: ACME_TOKEN
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Acme-Signature, encoding: hex }
    delivery: { header: Acme-Delivery }
  bodies: repeat
  normalize: [normalize]
thread:
  reply: [reply]
  posted: [posted]
  read: [read]
  place: team
  open: [open]
  opened: [opened]
  state: [state]
  states: [started, review, done]
link:
  host: acme.test
  issue: '^/issue/([A-Z]+-[0-9]+)'
  fixed: "https://acme.test/issue/{1}"
  thread: "{1}"
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// acmeScripts are thread commands that record what they were handed and
// answer from it, without jq. dir holds the records: <cmd>.stdin counts
// the runs, <cmd>.token the credential, and done.<cmd> marks that the
// command ran, so posted and opened answer {} until the write they check
// for happened.
func acmeScripts(dir string) map[string]string {
	run := func(cmd, answer string) string {
		return "#!/bin/sh\n{ cat; echo; } >> " + dir + "/" + cmd + ".stdin\n" +
			"[ -n \"$ACME_TOKEN\" ] && printf '%s' \"$ACME_TOKEN\" > " + dir + "/" + cmd + ".token\n" +
			"printf '%s' \"$YARDR_SOURCE_BASE\" > " + dir + "/" + cmd + ".base\n" +
			"touch " + dir + "/done." + cmd + "\nprintf '%s' '" + answer + "'\n"
	}
	seen := func(self, check, answer string) string {
		return "#!/bin/sh\n{ cat; echo; } >> " + dir + "/" + self + ".stdin\n" +
			"if [ -f " + dir + "/done." + check + " ]; then printf '%s' '" + answer + "'; else printf '%s' '{}'; fi\n"
	}
	return map[string]string{
		"normalize": "#!/bin/sh\nprintf '%s' '{\"type\":\"ping\"}'\n",
		"reply":     run("reply", `{"id":"c-9","url":"https://acme.test/issue/ACME-1#c-9"}`),
		"posted":    seen("posted", "reply", `{"id":"c-9","url":"https://acme.test/issue/ACME-1#c-9"}`),
		"read": "#!/bin/sh\n{ cat; echo; } >> " + dir + "/read.stdin\n" +
			`printf '%s' '{"messages":[]}'` + "\n",
		"open":   run("open", `{"id":"issue-2","title":"Made","url":"https://acme.test/issue/ACME-2"}`),
		"opened": seen("opened", "open", `{"id":"issue-2","title":"Made","url":"https://acme.test/issue/ACME-2"}`),
		"state":  run("state", `{"status":"In Progress","moved":true}`),
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s is no JSON object: %v", path, err)
	}
	return v
}

// lastJSON is the last object of path, one per line, as a command that ran
// more than once recorded them.
func lastJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var v map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &v); err != nil {
		t.Fatalf("%s is no JSON objects: %v", path, err)
	}
	return v
}

func readText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A declared source without a key lacks that interface: each lookup finds
// only what the keys say it can do, and the lists name it only there.
func TestDeclaredCapabilities(t *testing.T) {
	full, err := source.Parse("q.yaml", []byte(acmeThread))
	if err != nil {
		t.Fatal(err)
	}
	without := func(change func(*source.Declaration)) *source.Declaration {
		raw, _ := json.Marshal(full)
		var d source.Declaration
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		change(&d)
		return &d
	}
	reply, open, state, forward := "reply", "open", "state", "forward"
	for _, tc := range []struct {
		name string
		d    *source.Declaration
		// has says which of reply, open, state and forward the source has.
		has  []string
		link bool
	}{
		{"all keys", full, []string{reply, open, state, forward}, true},
		{"no reply", without(func(d *source.Declaration) { d.Thread.Reply = nil }), nil, true},
		{"no posted", without(func(d *source.Declaration) { d.Thread.Posted = nil }), nil, true},
		{"no read", without(func(d *source.Declaration) { d.Thread.Read = nil }), nil, true},
		{"no credential", without(func(d *source.Declaration) { d.Credential = nil }), nil, true},
		{"no thread", without(func(d *source.Declaration) { d.Thread = nil }), nil, true},
		{"no open", without(func(d *source.Declaration) { d.Thread.Open = nil }), []string{reply, state, forward}, true},
		{"no opened", without(func(d *source.Declaration) { d.Thread.Opened = nil }), []string{reply, state, forward}, true},
		{"no place", without(func(d *source.Declaration) { d.Thread.Place = "" }), []string{reply, state, forward}, true},
		{"no state", without(func(d *source.Declaration) { d.Thread.State = nil }), []string{reply, open}, true},
		{"no states", without(func(d *source.Declaration) { d.Thread.States = nil }), []string{reply, open}, true},
		{"no link", without(func(d *source.Declaration) { d.Link = nil }), []string{reply, open, state}, false},
	} {
		r, _ := hooks.ReplierFor("acme", tc.d, "", "")
		o, _ := hooks.OpenerFor("acme", tc.d, "", "")
		s, _ := hooks.StaterFor("acme", tc.d, "", "")
		f, _ := hooks.ForwarderFor("acme", tc.d, "", "")
		var has []string
		if r != nil {
			has = append(has, reply)
		}
		if o != nil {
			has = append(has, open)
		}
		if s != nil {
			has = append(has, state)
		}
		if f != nil {
			has = append(has, forward)
		}
		if strings.Join(has, ",") != strings.Join(tc.has, ",") {
			t.Errorf("%s: has %v, want %v", tc.name, has, tc.has)
		}
		if _, err := hooks.IssueIn("acme", "https://acme.test/issue/ACME-1", tc.d); (err == nil) != tc.link {
			t.Errorf("%s: link err = %v, want link %v", tc.name, err, tc.link)
		}
	}
	// The lists name a declared source only where its keys put it.
	if got := hooks.Repliable(full); len(got) != 2 || got[0] != "acme" {
		t.Errorf("repliable = %v", got)
	}
	if got := hooks.Openable(full); len(got) != 2 || got[0] != "acme" {
		t.Errorf("openable = %v", got)
	}
	if got := hooks.Stateable(full); len(got) != 2 || got[0] != "acme" {
		t.Errorf("stateable = %v", got)
	}
	if got := hooks.Forwardable(full); len(got) != 2 || got[0] != "acme" {
		t.Errorf("forwardable = %v", got)
	}
	if got := hooks.LinkSources(full); len(got) != 2 || got[0] != "acme" {
		t.Errorf("link sources = %v", got)
	}
	stateless := without(func(d *source.Declaration) { d.Thread.State, d.Thread.States, d.Link = nil, nil, nil })
	if got := hooks.Stateable(stateless); len(got) != 1 {
		t.Errorf("stateable without state keys = %v", got)
	}
	if got := hooks.LinkSources(stateless); len(got) != 1 {
		t.Errorf("link sources without link = %v", got)
	}
	// Another name, and no declaration, find nothing declared.
	if _, ok := hooks.ReplierFor("other", full, "", ""); ok {
		t.Error("another name has a replier")
	}
	if _, ok := hooks.ReplierFor("acme", nil, "", ""); ok {
		t.Error("no declaration has a replier")
	}
	if _, ok := hooks.ReplierFor("linear", nil, "", ""); ok {
		t.Error("linear without a declaration has a replier")
	}
}

// The credential of a declared source is the one variable its
// credential.env names, read from the caller's own environment.
func TestDeclaredCredential(t *testing.T) {
	d, err := source.Parse("q.yaml", []byte(acmeThread))
	if err != nil {
		t.Fatal(err)
	}
	ad := hooks.Declared{D: d}
	ctx := context.Background()
	t.Setenv("ACME_TOKEN", "")
	if _, err := ad.Credential(ctx); err == nil || !strings.Contains(err.Error(), "ACME_TOKEN is not set") {
		t.Errorf("unset: %v", err)
	}
	t.Setenv("ACME_TOKEN", "tok-1\n")
	if got, err := ad.Credential(ctx); err != nil || got != "tok-1" {
		t.Errorf("set: %q %v", got, err)
	}
	if got, err := ad.CredentialFrom(func(name string) string { return "tok-2" }); err != nil || got != "tok-2" {
		t.Errorf("from: %q %v", got, err)
	}
	missing := (*hooks.MissingCredential)(nil)
	if _, err := ad.CredentialFrom(func(string) string { return "" }); !errors.As(err, &missing) || missing.Names != "ACME_TOKEN" {
		t.Errorf("missing: %v", err)
	}
	ways := hooks.CredentialWays("acme", d)
	if len(ways) != 1 || ways[0].Kind != "API key" || strings.Join(ways[0].Names, ",") != "ACME_TOKEN" {
		t.Fatalf("ways = %+v", ways)
	}
	if w, ok := hooks.CredentialSet("acme", []string{"ACME_TOKEN"}, d); !ok || w.Kind != "API key" {
		t.Errorf("set = %+v, %v", w, ok)
	}
	if _, ok := hooks.CredentialSet("acme", []string{"OTHER"}, d); ok {
		t.Error("another variable sets the credential")
	}
	if got := hooks.CredentialNames("acme", d); got != "ACME_TOKEN" {
		t.Errorf("names = %q", got)
	}
	if hooks.CredentialWays("acme", nil) != nil {
		t.Error("no declaration has a way")
	}
}

// A reply is posted once when the first attempt's answer is lost: Posted
// finds what the key left, so no second attempt posts. The command is
// handed the method's arguments, the credential under the variable the
// file names, and YARDR_SOURCE_BASE from the file.
func TestDeclaredReplyPostedOnce(t *testing.T) {
	dir := t.TempDir()
	ad := threadDecl(t, acmeThread, acmeScripts(dir))
	ctx := context.Background()
	env := hooks.Envelope{Source: "acme", Type: "issue", Thread: hooks.Thread{ID: "issue-1"}}
	const key = "9d4c3b2a-1f0e-4d5c-8b7a-6f5e4d3c2b1a"
	if found, err := ad.Posted(ctx, env, key, "tok-1"); err != nil || found != nil {
		t.Fatalf("before posting: %+v, %v", found, err)
	}
	m, err := ad.Reply(ctx, env, "", "answer", key, "tok-1")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "c-9" || m.URL != "https://acme.test/issue/ACME-1#c-9" || m.Body != "answer" {
		t.Fatalf("reply = %+v", m)
	}
	// The answer is lost; the retry asks first and posts nothing more.
	found, err := ad.Posted(ctx, env, key, "tok-1")
	if err != nil || found == nil || found.ID != "c-9" {
		t.Fatalf("after posting: %+v, %v", found, err)
	}
	runs := strings.Count(readText(t, filepath.Join(dir, "reply.stdin")), "\n")
	if runs != 1 {
		t.Fatalf("reply ran %d times", runs)
	}
	in := readJSON(t, filepath.Join(dir, "reply.stdin"))
	if in["thread"] != "issue-1" || in["parent"] != "" || in["body"] != "answer" || in["key"] != key {
		t.Fatalf("reply stdin = %v", in)
	}
	if got := readText(t, filepath.Join(dir, "reply.token")); got != "tok-1" {
		t.Fatalf("credential handed in = %q", got)
	}
	if got := readText(t, filepath.Join(dir, "reply.base")); got != "https://acme.test" {
		t.Fatalf("base handed in = %q", got)
	}
	in = lastJSON(t, filepath.Join(dir, "posted.stdin"))
	if in["thread"] != "issue-1" || in["key"] != key {
		t.Fatalf("posted stdin = %v", in)
	}
}

// A reply goes under the root of the conversation it answers, listed to
// find it where the envelope does not carry it.
func TestDeclaredReplyParent(t *testing.T) {
	dir := t.TempDir()
	scripts := acmeScripts(dir)
	scripts["read"] = "#!/bin/sh\n{ cat; echo; } >> " + dir + "/read.stdin\n" + `printf '%s' '{"messages":[` +
		`{"id":"c1","author":{"id":"u-ann","name":"Ann"},"at":"2026-10-03T10:00:00Z","body":"look?"},` +
		`{"id":"c2","parent":"c1","author":{"id":"u-jane","name":"Jane"},"at":"2026-10-03T10:05:00Z","body":"on it"}` +
		`]}'` + "\n"
	ad := threadDecl(t, acmeThread, scripts)
	ctx := context.Background()
	parentOf := func() string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, "reply.stdin"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		var in map[string]any
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &in); err != nil {
			t.Fatal(err)
		}
		p, _ := in["parent"].(string)
		return p
	}
	reads := func() int {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, "read.stdin"))
		if os.IsNotExist(err) {
			return 0
		} else if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "\n")
	}
	issue := hooks.Envelope{Source: "acme", Type: "issue", Thread: hooks.Thread{ID: "issue-1"}}
	if _, err := ad.Reply(ctx, issue, "", "new", "k1", "tok"); err != nil {
		t.Fatal(err)
	}
	if got := parentOf(); got != "" {
		t.Errorf("issue delivery: parent %q", got)
	}
	comment := hooks.Envelope{Source: "acme", Type: "comment", Thread: hooks.Thread{ID: "issue-1"}, Message: "c2", Parent: "c1"}
	n := reads()
	if _, err := ad.Reply(ctx, comment, "", "answer", "k2", "tok"); err != nil {
		t.Fatal(err)
	}
	if got := parentOf(); got != "c1" {
		t.Errorf("reply: parent %q", got)
	}
	if reads() != n {
		t.Error("the envelope carried the parent, yet the thread was listed")
	}
	if _, err := ad.Reply(ctx, issue, "c2", "answer", "k3", "tok"); err != nil {
		t.Fatal(err)
	}
	if got := parentOf(); got != "c1" {
		t.Errorf("--to a reply: parent %q", got)
	}
	if _, err := ad.Reply(ctx, issue, "c1", "answer", "k4", "tok"); err != nil {
		t.Fatal(err)
	}
	if got := parentOf(); got != "c1" {
		t.Errorf("--to a top-level comment: parent %q", got)
	}
	if _, err := ad.Reply(ctx, issue, "c9", "x", "k5", "tok"); err == nil ||
		!strings.Contains(err.Error(), "acme message c9 is not on the delivery's thread issue-1") {
		t.Errorf("unknown --to: %v", err)
	}
	old := hooks.Envelope{Source: "acme", Type: "comment", Thread: hooks.Thread{ID: "issue-1"},
		URL: "https://acme.test/issue/ACME-1#comment-c2"}
	if _, err := ad.Reply(ctx, old, "", "answer", "k6", "tok"); err != nil {
		t.Fatal(err)
	}
	if got := parentOf(); got != "c1" {
		t.Errorf("old reply: parent %q", got)
	}
	nourl := hooks.Envelope{Source: "acme", Type: "comment", Thread: hooks.Thread{ID: "issue-1"},
		URL: "https://acme.test/issue/ACME-1"}
	if _, err := ad.Reply(ctx, nourl, "", "x", "k7", "tok"); err == nil || !strings.Contains(err.Error(), "names no comment") {
		t.Errorf("old delivery without a fragment: %v", err)
	}
	odd := hooks.Envelope{Source: "acme", Type: "comment", Thread: hooks.Thread{ID: "c9"}, Message: "c9"}
	if _, err := ad.Reply(ctx, odd, "", "x", "k8", "tok"); err == nil || !strings.Contains(err.Error(), "not on a thread") {
		t.Errorf("comment off a thread: %v", err)
	}
	if _, err := ad.Posted(ctx, odd, "k8", "tok"); err == nil {
		t.Error("posted off a thread: no error")
	}
	if _, err := ad.Thread(ctx, odd, "tok"); err == nil {
		t.Error("thread off a thread: no error")
	}
}

// Thread lists the thread's messages oldest first, whatever order the
// command printed them in.
func TestDeclaredThread(t *testing.T) {
	dir := t.TempDir()
	scripts := acmeScripts(dir)
	scripts["read"] = "#!/bin/sh\n{ cat; echo; } >> " + dir + "/read.stdin\n" + `printf '%s' '{"messages":[` +
		`{"id":"c2","author":{"id":"u-jane","name":"Jane"},"at":"2026-10-03T10:05:00Z","body":"later"},` +
		`{"id":"c1","author":{"id":"u-ann","name":"Ann"},"at":"2026-10-03T10:00:00Z","body":"first"}` +
		`]}'` + "\n"
	ad := threadDecl(t, acmeThread, scripts)
	ms, err := ad.Thread(context.Background(),
		hooks.Envelope{Source: "acme", Type: "issue", Thread: hooks.Thread{ID: "issue-1"}}, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "c1" || ms[1].ID != "c2" || ms[0].Author.Name != "Ann" {
		t.Fatalf("thread = %+v", ms)
	}
	in := readJSON(t, filepath.Join(dir, "read.stdin"))
	if in["thread"] != "issue-1" || len(in) != 1 {
		t.Fatalf("read stdin = %v", in)
	}
}

// A thread is opened once when the first attempt's answer is lost: Opened
// finds what the key left, so no second attempt opens.
func TestDeclaredOpenOpenedOnce(t *testing.T) {
	dir := t.TempDir()
	ad := threadDecl(t, acmeThread, acmeScripts(dir))
	ctx := context.Background()
	if ad.Place() != "team" {
		t.Fatalf("place = %q", ad.Place())
	}
	const key = "9d4c3b2a-1f0e-4d5c-8b7a-6f5e4d3c2b1a"
	since := time.Date(2026, 10, 11, 14, 2, 3, 0, time.UTC)
	if found, err := ad.Opened(ctx, "ENG", key, "tok", since); err != nil || found != nil {
		t.Fatalf("before opening: %+v, %v", found, err)
	}
	th, err := ad.Open(ctx, "ENG", "Made", "words", key, "", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if th.ID != "issue-2" || th.Title != "Made" || th.URL != "https://acme.test/issue/ACME-2" {
		t.Fatalf("open = %+v", th)
	}
	found, err := ad.Opened(ctx, "ENG", key, "tok", since)
	if err != nil || found == nil || found.ID != "issue-2" {
		t.Fatalf("after opening: %+v, %v", found, err)
	}
	runs := strings.Count(readText(t, filepath.Join(dir, "open.stdin")), "\n")
	if runs != 1 {
		t.Fatalf("open ran %d times", runs)
	}
	in := readJSON(t, filepath.Join(dir, "open.stdin"))
	if in["in"] != "ENG" || in["title"] != "Made" || in["body"] != "words" || in["key"] != key {
		t.Fatalf("open stdin = %v", in)
	}
	in = lastJSON(t, filepath.Join(dir, "opened.stdin"))
	if in["in"] != "ENG" || in["key"] != key || in["since"] != "2026-10-11T14:02:03Z" || len(in) != 3 {
		t.Fatalf("opened stdin = %v", in)
	}
	// Without an attempt on record there is no bound to hand over.
	if _, err := ad.Opened(ctx, "ENG", key, "tok", time.Time{}); err != nil {
		t.Fatal(err)
	}
	in = lastJSON(t, filepath.Join(dir, "opened.stdin"))
	if in["in"] != "ENG" || in["key"] != key || len(in) != 2 {
		t.Fatalf("opened stdin without since = %v", in)
	}
}

// SetState moves the thread to what the word means, as the command says:
// its status, whether it moved, and what it was in when the command says.
// A word the source has no status for is refused before anything runs.
func TestDeclaredSetState(t *testing.T) {
	dir := t.TempDir()
	ad := threadDecl(t, acmeThread, acmeScripts(dir))
	ctx := context.Background()
	st, err := ad.SetState(ctx, "issue-1", "started", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "In Progress" || !st.Changed || st.Was != "" || st.Thread.ID != "issue-1" {
		t.Fatalf("set = %+v", st)
	}
	in := readJSON(t, filepath.Join(dir, "state.stdin"))
	if in["thread"] != "issue-1" || in["state"] != "started" || len(in) != 2 {
		t.Fatalf("state stdin = %v", in)
	}
	// Was is what a moved thread was in, when the command says.
	scripts := acmeScripts(dir)
	scripts["state"] = "#!/bin/sh\nprintf '%s' '{\"status\":\"In Progress\",\"moved\":true,\"was\":\"Todo\"}'\n"
	ad = threadDecl(t, acmeThread, scripts)
	st, err = ad.SetState(ctx, "issue-1", "started", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "In Progress" || st.Was != "Todo" || !st.Changed {
		t.Fatalf("set with was = %+v", st)
	}
	if _, err := ad.SetState(ctx, "issue-1", "blocked", "tok"); err == nil ||
		!strings.Contains(err.Error(), `"blocked" is no state of acme: want started, review, done`) || !hooks.Final(err) {
		t.Fatalf("unknown state: %v", err)
	}
	if _, err := ad.SetState(ctx, "issue-1", "label:ui", "tok"); err == nil ||
		!strings.Contains(err.Error(), "yardr sets no label there") {
		t.Fatalf("label: %v", err)
	}
	if _, err := ad.SetState(ctx, "", "started", "tok"); err == nil || !hooks.Final(err) {
		t.Fatalf("no thread: %v", err)
	}
	if runs := strings.Count(readText(t, filepath.Join(dir, "state.stdin")), "\n"); runs != 1 {
		t.Fatalf("refused states ran the command %d times", runs)
	}
	// An answer that names no status is none.
	scripts = acmeScripts(dir)
	scripts["state"] = "#!/bin/sh\nprintf '%s' '{\"moved\":true}'\n"
	ad = threadDecl(t, acmeThread, scripts)
	if _, err := ad.SetState(ctx, "issue-1", "started", "tok"); err == nil ||
		!strings.Contains(err.Error(), "its answer names no status") {
		t.Fatalf("statusless answer: %v", err)
	}
}

// A label:<name> sets no status but adds the label, when the thread takes
// labels: the word reaches thread.state whole, and the command says
// whether it moved.
func TestDeclaredSetStateLabel(t *testing.T) {
	dir := t.TempDir()
	text := strings.Replace(acmeThread,
		"  states: [started, review, done]\n", "  states: [started, review, done]\n  labels: true\n", 1)
	scripts := acmeScripts(dir)
	scripts["state"] = "#!/bin/sh\n{ cat; echo; } >> " + dir + "/state.stdin\nprintf '%s' '{\"status\":\"labeled ui\",\"moved\":true}'\n"
	ad := threadDecl(t, text, scripts)
	st, err := ad.SetState(context.Background(), "issue-1", "label:ui", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "labeled ui" || !st.Changed {
		t.Fatalf("set = %+v", st)
	}
	in := readJSON(t, filepath.Join(dir, "state.stdin"))
	if in["thread"] != "issue-1" || in["state"] != "label:ui" || len(in) != 2 {
		t.Fatalf("state stdin = %v", in)
	}
}

// Forward moves a thread further than it stands, and a status not moved
// backwards is reported as not moved: the command says whether it moved,
// and the adapter makes no second call.
func TestDeclaredForward(t *testing.T) {
	dir := t.TempDir()
	scripts := acmeScripts(dir)
	scripts["state"] = "#!/bin/sh\n{ cat; echo; } >> " + dir + "/state.stdin\nprintf '%s' '{\"status\":\"Done\",\"moved\":false}'\n"
	ad := threadDecl(t, acmeThread, scripts)
	ctx := context.Background()
	fw, err := ad.Forward(ctx, "issue-1", "started", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if fw.Status != "Done" || fw.Changed || fw.Was != "Done" {
		t.Fatalf("forward = %+v", fw)
	}
	in := readJSON(t, filepath.Join(dir, "state.stdin"))
	if in["thread"] != "issue-1" || in["state"] != "started" || in["forward"] != true || len(in) != 3 {
		t.Fatalf("forward stdin = %v", in)
	}
	if runs := strings.Count(readText(t, filepath.Join(dir, "state.stdin")), "\n"); runs != 1 {
		t.Fatalf("forward ran the command %d times", runs)
	}
	// A move forward says what the thread was in, when the command says.
	scripts["state"] = "#!/bin/sh\nprintf '%s' '{\"status\":\"Done\",\"moved\":true,\"was\":\"In Review\"}'\n"
	ad = threadDecl(t, acmeThread, scripts)
	fw, err = ad.Forward(ctx, "issue-1", "done", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if fw.Status != "Done" || fw.Was != "In Review" || !fw.Changed {
		t.Fatalf("forward moved = %+v", fw)
	}
	if err := ad.Places("review"); err != nil {
		t.Fatalf("places review: %v", err)
	}
	if err := ad.Places("blocked"); err == nil || !hooks.Final(err) {
		t.Fatalf("places blocked: %v", err)
	}
	thread, err := ad.IssueThread("https://acme.test/issue/ACME-1")
	if err != nil || thread != "ACME-1" {
		t.Fatalf("issue thread = %q, %v", thread, err)
	}
	if _, err := ad.IssueThread("https://acme.test/project/1"); err == nil || !hooks.Final(err) {
		t.Fatalf("issue thread of a project: %v", err)
	}
	missing := (*hooks.MissingCredential)(nil)
	if _, err := ad.CredentialFrom(func(string) string { return "" }); !errors.As(err, &missing) {
		t.Fatalf("credential from nothing: %v", err)
	}
}

// A link is made from the URL alone: the fixed form, or why it is no
// issue. A URL on a host a builtin has issues on is the builtin's.
func TestDeclaredLink(t *testing.T) {
	d, err := source.Parse("q.yaml", []byte(acmeThread))
	if err != nil {
		t.Fatal(err)
	}
	ad := hooks.Declared{D: d}
	if ad.IssueHost() != "acme.test" {
		t.Fatalf("host = %q", ad.IssueHost())
	}
	got, err := hooks.IssueIn("acme", "https://acme.test/issue/ACME-1/slug", d)
	if err != nil || got != "https://acme.test/issue/ACME-1" {
		t.Fatalf("in its source = %q, %v", got, err)
	}
	if _, err := hooks.IssueIn("acme", "https://acme.test/project/1", d); err == nil ||
		!strings.Contains(err.Error(), "is no acme issue") {
		t.Fatalf("a project: %v", err)
	}
	if _, err := hooks.IssueIn("acme", "https://else.test/issue/ACME-1", d); err == nil ||
		!strings.Contains(err.Error(), "not on acme.test") {
		t.Fatalf("another host: %v", err)
	}
	if _, err := hooks.IssueIn("quiet", "https://acme.test/issue/ACME-1", d); err == nil ||
		!strings.Contains(err.Error(), "a bead cannot link to an issue in quiet (it can in acme, github)") {
		t.Fatalf("another source: %v", err)
	}
	name, fixed, err := hooks.Issue("https://acme.test/issue/ACME-9/renamed", d)
	if err != nil || name != "acme" || fixed != "https://acme.test/issue/ACME-9" {
		t.Fatalf("by host = %q, %q, %v", name, fixed, err)
	}
	lin, err := source.Parse("lin.yaml", []byte(linearDeclared(t)))
	if err != nil {
		t.Fatal(err)
	}
	// linear.app is the pack's host: a bare URL is the declared linear's.
	if name, _, err := hooks.Issue("https://linear.app/acme/issue/YRD-1", lin); err != nil || name != "linear" {
		t.Fatalf("shared host = %q, %v", name, err)
	}
	if _, _, err := hooks.Issue("https://else.test/issue/1", d); err == nil ||
		!strings.Contains(err.Error(), "where yardr knows no issues (it does on github.com and acme.test)") {
		t.Fatalf("unknown host: %v", err)
	}
}

// linearDeclared is the Linear source as packs/linear declares it, for the
// shared-host case above.
func linearDeclared(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../packs/linear/linear.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A failing command's stderr reaches the user, and its stdout does not.
func TestDeclaredCommandFailure(t *testing.T) {
	dir := t.TempDir()
	scripts := acmeScripts(dir)
	scripts["reply"] = "#!/bin/sh\necho 'secret-stdout'\necho 'boom-stderr' >&2\nexit 3\n"
	ad := threadDecl(t, acmeThread, scripts)
	_, err := ad.Reply(context.Background(),
		hooks.Envelope{Source: "acme", Type: "issue", Thread: hooks.Thread{ID: "issue-1"}},
		"", "answer", "k", "tok")
	if err == nil || !strings.Contains(err.Error(), "boom-stderr") {
		t.Fatalf("reply: %v", err)
	}
	if strings.Contains(err.Error(), "secret-stdout") {
		t.Fatalf("reply stdout reached the user: %v", err)
	}
	scripts["state"] = "#!/bin/sh\necho 'not an object'\n"
	ad = threadDecl(t, acmeThread, scripts)
	_, err = ad.SetState(context.Background(), "issue-1", "started", "tok")
	if err == nil || !strings.Contains(err.Error(), "its answer is no {status, moved}") {
		t.Fatalf("state: %v", err)
	}
	if strings.Contains(err.Error(), "not an object") {
		t.Fatalf("state stdout reached the user: %v", err)
	}
}

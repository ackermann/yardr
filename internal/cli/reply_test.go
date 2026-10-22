package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// replyScripts answers an acme thread's commands from dir. Reply records
// its stdin, refuses a wrong token, and answers c-9; posted answers c-9
// once reply ran; read lists the issue's two comments. With dir/lose
// present, reply records the post and then fails, as a post whose answer
// never arrived looks to the caller.
func replyScripts(dir string) map[string]string {
	scripts := map[string]string{
		"normalize": "#!/bin/sh\nprintf '%s' '{\"type\":\"ping\"}'\n",
		"reply": "#!/bin/sh\nin=$(cat)\nprintf '%s\\n' \"$in\" >> " + dir + "/reply.stdin\n" +
			// What each file of the reply holds while it is being posted: its name, then its bytes.
			"for p in $(printf '%s' \"$in\" | sed -n 's/.*\"files\":\\[\\([^]]*\\)\\].*/\\1/p' | tr -d '\"' | tr ',' ' '); do " +
			"{ echo \"${p##*/}\"; cat \"$p\"; echo; } >> " + dir + "/files.seen; done\n" +
			"touch " + dir + "/done.reply\n" +
			"if [ -f " + dir + "/lose ]; then echo 'upstream timed out' >&2; exit 1; fi\n" +
			"[ \"$ACME_TOKEN\" = tok-yardmaster ] || { echo 'Authentication required' >&2; exit 1; }\n" +
			"printf '%s' '{\"id\":\"c-9\",\"url\":\"https://acme.test/issue/ACME-1#c-9\"}'\n",
		"posted": "#!/bin/sh\n{ cat; echo; } >> " + dir + "/posted.stdin\n" +
			"if [ -f " + dir + "/done.reply ]; then printf '%s' '{\"id\":\"c-9\",\"url\":\"https://acme.test/issue/ACME-1#c-9\"}'; else printf '%s' '{}'; fi\n",
		"read": "#!/bin/sh\n{ cat; echo; } >> " + dir + "/read.stdin\n" +
			`printf '%s' '{"messages":[` +
			`{"id":"c1","body":"@yardmaster can you look?\nit drops retries","at":"2026-10-03T10:00:00Z",` +
			`"url":"https://acme.test/issue/ACME-1#c1","author":{"id":"u-jane","name":"Jane"}},` +
			`{"id":"c2","body":"ping","parent":"c1","at":"2026-10-03T10:05:00Z",` +
			`"url":"https://acme.test/issue/ACME-1#c2","author":{"id":"u-ann","name":"Ann"}}]}'` + "\n",
	}
	for _, name := range []string{"normalize", "reply", "posted", "read", "open", "opened", "state"} {
		if _, ok := scripts[name]; !ok {
			scripts[name] = "#!/bin/sh\necho 'no " + name + " in this test' >&2; exit 1\n"
		}
	}
	return scripts
}

// replyYard is a yard with crew member yardmaster, the declared source
// acme, and four events to answer: an acme reply c2 (under c1), a token
// message, a top-level comment, an issue, and c2 as delivered before
// envelopes carried the comment's id. It returns the app, the store, the
// seqs, and the directory the commands record in.
func replyYard(t *testing.T) (*app, store.API, map[string]string, string) {
	t.Helper()
	home, declDir, dir := t.TempDir(), t.TempDir(), t.TempDir()
	st, own := storetest.NewOwnedIn(t, home)
	ctx := context.Background()
	scripts := replyScripts(dir)
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
	if _, err := st.PutSource(ctx, "acme.yaml", acmeCLI, "me"); err != nil {
		t.Fatal(err)
	}
	declareGitHubPack(t, st, "", "", "me")
	seqs := map[string]string{
		"bead": strconv.FormatUint(head, 10),
		"acme": record("acme", hooks.Envelope{Source: "acme", Type: "comment", Action: "create",
			Actor:   hooks.Party{ID: "u-ann", Name: "Ann"},
			Thread:  hooks.Thread{ID: "issue-1", Title: "Retries", URL: "https://acme.test/issue/ACME-1"},
			Message: "c2", Parent: "c1"}),
		"token": record("cron", hooks.Envelope{Source: "token", Type: "message", Text: "backup ok"}),
		"top": record("acme", hooks.Envelope{Source: "acme", Type: "comment", Action: "create",
			Thread: hooks.Thread{ID: "issue-1", Title: "Retries"}, Message: "c1"}),
		"issue": record("acme", hooks.Envelope{Source: "acme", Type: "issue", Action: "create",
			Thread: hooks.Thread{ID: "issue-1", Title: "Retries"}}),
		// c2 as delivered before envelopes carried the comment's id.
		"old": record("acme", hooks.Envelope{Source: "acme", Type: "comment", Action: "create",
			Thread: hooks.Thread{ID: "issue-1", Title: "Retries"}, URL: "https://acme.test/issue/ACME-1-retries#comment-c2"}),
		// A delivery of a source whose replies take no files.
		"gp": record("github", hooks.Envelope{Source: "github", Type: "comment", Action: "create",
			Thread: hooks.Thread{ID: "octo/repo#9", Title: "Nine"}, Message: "c1"}),
	}
	return &app{st: st, actor: "yard-10-new", cfg: &config.Config{Home: home}, env: testEnv(t, "")}, st, seqs, dir
}

func replyKeys(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "reply.stdin"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		key, _ := v["key"].(string)
		keys = append(keys, key)
	}
	return keys
}

// uuid4 is the form of a reply's key: the source takes it as the comment's id.
var uuid4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestReply(t *testing.T) {
	a, st, seqs, dir := replyYard(t)
	ctx := context.Background()
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	a.env["YARDR_CREW"] = "yardmaster"

	out, err := run(t, a.replyCmd(), seqs["acme"], "Filed as yard-k3f9.")
	if err != nil {
		t.Fatal(err)
	}
	if n := stdinRuns(t, dir, "reply"); n != 1 {
		t.Fatalf("reply ran %d times", n)
	}
	// The delivered comment is a reply to c1, so the answer goes under c1,
	// with the key the store chose.
	in := lastStdin(t, dir, "reply")
	key, _ := in["key"].(string)
	if in["parent"] != "c1" || in["thread"] != "issue-1" || in["body"] != "Filed as yard-k3f9." || !uuid4.MatchString(key) {
		t.Fatalf("reply stdin %v", in)
	}
	const url = "https://acme.test/issue/ACME-1#c-9"
	if out != url+"\n" {
		t.Fatalf("output %q", out)
	}
	ps, err := st.Threads(ctx, "yardmaster")
	if err != nil || len(ps) != 1 || ps[0].Thread != "issue-1" || ps[0].How != store.ParticipantReplied || ps[0].Title != "Retries" {
		t.Fatalf("threads %+v %v", ps, err)
	}
	evs, _ := st.EventsSince(ctx, 0, 0)
	last := evs[len(evs)-1]
	if last.Kind != "replied" || last.Actor != "yardmaster" || last.Data["source"] != "acme" || last.Data["thread"] != "issue-1" ||
		last.Data["url"] != url || last.Data["message"] != "c-9" || fmt.Sprint(last.Data["seq"]) != seqs["acme"] {
		t.Fatalf("last event %+v", last)
	}

	// From stdin, under the root of --to's conversation, as JSON.
	a.json = true
	c := a.replyCmd()
	c.SetIn(strings.NewReader("  second answer\n"))
	out, err = run(t, c, seqs["acme"], "-", "--to", "c1")
	a.json = false
	if err != nil {
		t.Fatal(err)
	}
	// c1 is a top-level comment, so it is its own root.
	if n := stdinRuns(t, dir, "reply"); n != 2 {
		t.Fatalf("reply ran %d times", n)
	}
	in = lastStdin(t, dir, "reply")
	if in["body"] != "second answer" || in["parent"] != "c1" {
		t.Fatalf("reply stdin %v", in)
	}
	var res map[string]any
	if json.Unmarshal([]byte(out), &res) != nil || res["url"] != url ||
		res["crew"] != "yardmaster" || res["message"] != "c-9" || res["repeated"] != false {
		t.Fatalf("json %q", out)
	}
	t.Run("files", testReplyWithFiles)
}

// With --file, given more than once for several, the reply carries files
// into the thread: each is put as a blob and thread.reply's in gains its
// path. The same words with the same files run again are already posted;
// with other files, or none, they post anew. A source whose file does not
// say its replies take files is refused before anything is put.
func testReplyWithFiles(t *testing.T) {
	a, _, seqs, dir := replyYard(t)
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	a.env["YARDR_CREW"] = "yardmaster"
	home := a.cfg.Home
	shotBytes := []byte("\x89PNG\r\n\x1a\nthe shed")
	pageBytes := []byte("<html>the shed</html>")
	shot := filepath.Join(t.TempDir(), "shot.png")
	page := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(shot, shotBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, pageBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.replyCmd(), seqs["acme"], "See the shed.", "--file", shot, "--file", page)
	if err != nil {
		t.Fatal(err)
	}
	if out != "https://acme.test/issue/ACME-1#c-9\n" {
		t.Fatalf("output %q", out)
	}
	in := lastStdin(t, dir, "reply")
	files, _ := in["files"].([]any)
	if len(files) != 2 || in["body"] != "See the shed." {
		t.Fatalf("reply stdin %v", in)
	}
	// Each file is read at a path called what the file was called, in a
	// directory of its own under the yard's tmp, and the directories are
	// gone once the post is over.
	seen, err := os.ReadFile(filepath.Join(dir, "files.seen"))
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range []struct {
		name  string
		bytes []byte
	}{{"shot.png", shotBytes}, {"page.html", pageBytes}} {
		p, _ := files[i].(string)
		if want := filepath.Join(home, "tmp") + string(filepath.Separator) + "reply-"; !strings.HasPrefix(p, want) || filepath.Base(p) != c.name {
			t.Fatalf("file %d path %q is no link called %s under %s", i, p, c.name, want)
		}
		if !strings.Contains(string(seen), c.name+"\n"+string(c.bytes)+"\n") {
			t.Fatalf("file %d (%s) was posted holding %q", i, c.name, seen)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(home, "tmp", "*")); len(left) != 0 {
		t.Fatalf("a post left %v", left)
	}
	blobs := func() []string {
		t.Helper()
		names, _ := filepath.Glob(filepath.Join(home, "blobs", "*"))
		return names
	}
	if got := blobs(); len(got) != 2 {
		t.Fatalf("the files are held as %d blobs: %v", len(got), got)
	}

	again, err := run(t, a.replyCmd(), seqs["acme"], "See the shed.", "--file", shot, "--file", page)
	if err != nil {
		t.Fatal(err)
	}
	if again != out || stdinRuns(t, dir, "reply") != 1 {
		t.Fatalf("run again: out %q, reply ran %d times", again, stdinRuns(t, dir, "reply"))
	}
	other := filepath.Join(t.TempDir(), "other.png")
	if err := os.WriteFile(other, []byte("\x89PNG\r\n\x1a\nother"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["acme"], "See the shed.", "--file", other); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["acme"], "See the shed."); err != nil {
		t.Fatal(err)
	}
	if n := stdinRuns(t, dir, "reply"); n != 3 {
		t.Fatalf("other files, then none: reply ran %d times, want 3", n)
	}
	if keys := replyKeys(t, dir); len(keys) != 3 || keys[0] == keys[1] || keys[0] == keys[2] || keys[1] == keys[2] {
		t.Fatalf("keys: %v", keys)
	}

	names, runs := blobs(), stdinRuns(t, dir, "reply")
	badName := filepath.Join(t.TempDir(), "bad\nname.png")
	if err := os.WriteFile(badName, shotBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["acme"], "See the shed.", "--file", badName); err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("a filename with a control character: %v", err)
	}
	if got := blobs(); len(got) != len(names) {
		t.Fatalf("a refused filename put %d blobs", len(got)-len(names))
	}
	if n := stdinRuns(t, dir, "reply"); n != runs {
		t.Fatalf("a refused filename posted %d times", n-runs)
	}

	if _, err := run(t, a.replyCmd(), seqs["gp"], "x", "--file", shot); err == nil ||
		!strings.Contains(err.Error(), "which takes no files") {
		t.Fatalf("a source without files: %v", err)
	}
	if got := blobs(); len(got) != len(names) {
		t.Fatalf("a refused reply put %d blobs", len(got)-len(names))
	}
	if n := stdinRuns(t, dir, "reply"); n != runs {
		t.Fatalf("a refused reply posted %d times", n-runs)
	}

	var many []string
	for i := 0; i < store.MaxMailFiles+1; i++ {
		many = append(many, "--file", shot)
	}
	if _, err := run(t, a.replyCmd(), append([]string{seqs["acme"], "x"}, many...)...); err == nil ||
		!strings.Contains(err.Error(), "a reply carries at most") {
		t.Fatalf("over the bound: %v", err)
	}
}

// A reply run again is not posted again: one the store knows was posted
// is answered from the record; one whose answer was lost (the source took
// the post, the call failed) is found in the source by its key, recorded
// then, and not posted a second time. Another text, or another
// conversation, is another reply.
func TestReplyRetryDoesNotPostTwice(t *testing.T) {
	a, st, seqs, dir := replyYard(t)
	ctx := context.Background()
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	a.env["YARDR_CREW"] = "yardmaster"
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

	first, err := run(t, a.replyCmd(), seqs["acme"], "Filed.")
	if err != nil {
		t.Fatal(err)
	}
	again, err := run(t, a.replyCmd(), seqs["acme"], "Filed.")
	if err != nil {
		t.Fatal(err)
	}
	if n := stdinRuns(t, dir, "reply"); n != 1 || again != first || replied() != 1 {
		t.Fatalf("a recorded reply run again: reply ran %d times, out %q vs %q, %d replied events", n, again, first, replied())
	}

	// The answer is lost: the source made the comment, the call failed.
	if err := os.WriteFile(filepath.Join(dir, "lose"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["acme"], "Second."); err == nil || !strings.Contains(err.Error(), "upstream timed out") {
		t.Fatalf("lost answer: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "lose")); err != nil {
		t.Fatal(err)
	}
	if n := stdinRuns(t, dir, "reply"); n != 2 || replied() != 1 {
		t.Fatalf("after the lost answer: reply ran %d times, %d replied events", n, replied())
	}
	out, err := run(t, a.replyCmd(), seqs["acme"], "Second.")
	if err != nil {
		t.Fatal(err)
	}
	if n := stdinRuns(t, dir, "reply"); n != 2 || out != "https://acme.test/issue/ACME-1#c-9\n" || replied() != 2 {
		t.Fatalf("retry after the lost answer: reply ran %d times, out %q, %d replied events", n, out, replied())
	}
	a.json = true
	out, err = run(t, a.replyCmd(), seqs["acme"], "Second.")
	a.json = false
	var res map[string]any
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res["repeated"] != true || res["message"] != "c-9" {
		t.Fatalf("json of a repeat: %q, %v", out, err)
	}

	// Another text, and the same text under another conversation, are new
	// replies with keys of their own.
	if _, err := run(t, a.replyCmd(), seqs["acme"], "Third."); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["acme"], "Third.", "--to", "c1"); err != nil {
		t.Fatal(err)
	}
	keys := replyKeys(t, dir)
	if len(keys) != 4 || keys[2] == keys[3] || keys[2] == keys[1] || keys[3] == keys[1] {
		t.Fatalf("new replies: %v", keys)
	}
}

// Speaking as a crew member takes its own session or the operator: a bead's
// agent naming it with --crew is refused before anything is posted.
func TestReplyOnlyByTheCrewOrOperator(t *testing.T) {
	a, st, seqs, dir := replyYard(t) // acting as yard-10-new, a bead's agent
	ctx := context.Background()
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	a.env["YARDR_CREW"] = ""
	if err := st.SetSetting(ctx, "operator", "jo", "me"); err != nil {
		t.Fatal(err)
	}
	head, _ := st.Head(ctx)

	_, err := run(t, a.replyCmd(), seqs["acme"], "x", "--crew", "yardmaster")
	if err == nil || !strings.Contains(err.Error(), "only yardmaster's own session or the operator (jo) may speak as yardmaster") || !strings.Contains(err.Error(), "this is yard-10-new") {
		t.Fatalf("a bead's agent: %v", err)
	}
	if n := stdinRuns(t, dir, "reply"); n != 0 {
		t.Fatalf("posted %d times", n)
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("refusal logged %d events", now-head)
	}
	// The operator, by the yard's setting.
	if err := st.SetSetting(ctx, "operator", "yard-10-new", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["acme"], "x", "--crew", "yardmaster"); err != nil {
		t.Fatalf("the operator: %v", err)
	}
	if err := st.SetSetting(ctx, "operator", "jo", "yard-10-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seqs["acme"], "y", "--crew", "yardmaster"); err == nil {
		t.Fatal("no longer the operator, still allowed")
	}
	// The crew member's own session: the pane names it in YARDR_CREW and
	// YARDR_ACTOR, and acts as it. That its pane is its own session
	// whatever actor a command there names is the store's to know
	// (TestSpeakingAsACrewMember).
	a.env["YARDR_CREW"] = "yardmaster"
	a.actor = "yardmaster"
	if _, err := run(t, a.replyCmd(), seqs["acme"], "y"); err != nil {
		t.Fatalf("its own session: %v", err)
	}
	a.env["YARDR_CREW"] = ""
	if _, err := run(t, a.replyCmd(), seqs["acme"], "z", "--crew", "yardmaster"); err != nil {
		t.Fatalf("acting as the crew member: %v", err)
	}
	if n := stdinRuns(t, dir, "reply"); n != 3 {
		t.Fatalf("reply ran %d times", n)
	}
}

// Without --to a reply lands under the conversation of the delivered
// comment: a reply's parent, a top-level comment itself, the one an old
// delivery's URL names, nothing for an issue. An event recorded before
// envelopes carried the comment's id placed its reply at the top level
// (yard-npq6).
func TestReplyPlacement(t *testing.T) {
	a, _, seqs, dir := replyYard(t)
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	a.env["YARDR_CREW"] = "yardmaster"
	for _, c := range []struct {
		event string
		want  any
	}{
		{"acme", "c1"},
		{"top", "c1"},
		{"old", "c1"},
		{"issue", ""},
	} {
		n := stdinRuns(t, dir, "reply")
		if _, err := run(t, a.replyCmd(), seqs[c.event], "answer"); err != nil {
			t.Fatalf("%s: %v", c.event, err)
		}
		if m := stdinRuns(t, dir, "reply"); m != n+1 {
			t.Fatalf("%s: reply ran %d times, want %d", c.event, m, n+1)
		}
		if in := lastStdin(t, dir, "reply"); in["parent"] != c.want || in["thread"] != "issue-1" {
			t.Errorf("%s: reply stdin %v, want parent %v", c.event, in, c.want)
		}
	}
}

// What cannot be answered is refused before anything is posted or
// recorded.
func TestReplyRefusals(t *testing.T) {
	a, st, seqs, dir := replyYard(t)
	ctx := context.Background()
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	a.env["YARDR_CREW"] = "yardmaster"
	head, _ := st.Head(ctx)

	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"bead event", []string{seqs["bead"], "x"}, "is a crew_defined event, not a hook delivery"},
		{"token source", []string{seqs["token"], "x"}, "came from token, which yardr cannot answer in (it can in acme, github)"},
		{"no such event", []string{"9999", "x"}, "event 9999: not found"},
		{"not a seq", []string{"abc", "x"}, "want the number an inbox line starts with"},
		{"empty text", []string{seqs["acme"], "  "}, "the reply is empty"},
		{"unknown crew member", []string{seqs["acme"], "x", "--crew", "ghost"}, "crew member ghost: not found"},
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
	t.Setenv("ACME_TOKEN", "")
	if _, err := run(t, a.replyCmd(), seqs["acme"], "x"); err == nil || !strings.Contains(err.Error(), "ACME_TOKEN is not set") {
		t.Errorf("no key: %v", err)
	}
	// Nor without a crew member to reply as.
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	a.env["YARDR_CREW"] = ""
	if _, err := run(t, a.replyCmd(), seqs["acme"], "x"); err == nil || !strings.Contains(err.Error(), "--crew is required") {
		t.Errorf("no crew member: %v", err)
	}
	// What the source refuses is the error, and nothing is recorded.
	a.env["YARDR_CREW"] = "yardmaster"
	t.Setenv("ACME_TOKEN", "tok-other")
	if _, err := run(t, a.replyCmd(), seqs["acme"], "x"); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Errorf("refused by the source: %v", err)
	}

	if n := stdinRuns(t, dir, "reply"); n != 1 {
		t.Fatalf("posted %d times", n)
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("refusals logged %d events", now-head)
	}
	if ps, _ := st.Threads(ctx, "yardmaster"); len(ps) != 0 {
		t.Fatalf("threads %v", ps)
	}
}

func TestThreadShow(t *testing.T) {
	a, _, seqs, _ := replyYard(t)
	t.Setenv("ACME_TOKEN", "tok-yardmaster")

	out, err := run(t, a.threadCmd(), "show", seqs["acme"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`acme thread issue-1 "Retries"  https://acme.test/issue/ACME-1`,
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
	out, err = run(t, a.threadCmd(), "show", seqs["acme"])
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

	t.Setenv("ACME_TOKEN", "")
	if _, err := run(t, a.threadCmd(), "show", seqs["acme"]); err == nil || !strings.Contains(err.Error(), "ACME_TOKEN") {
		t.Errorf("no key: %v", err)
	}
	// An event recorded before envelopes carried the comment's id marks
	// the comment its URL names.
	t.Setenv("ACME_TOKEN", "tok-yardmaster")
	a.json = false
	out, err = run(t, a.threadCmd(), "show", seqs["old"])
	if err != nil || !strings.Contains(out, "* c2  ") || strings.Contains(out, "* c1") {
		t.Errorf("old event: %v\n%s", err, out)
	}
	if _, err := run(t, a.threadCmd(), "show", "9999"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown seq: %v", err)
	}
}

package cli

import (
	"context"
	"encoding/json"
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

// acmeCLI declares acme: a source the yard speaks in through one command
// per thing it does in a thread, and whose issues are a bead's link.
const acmeCLI = `name: acme
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

// acmeCLIScripts are acme's thread commands: a few lines of sh that record
// what they were handed in dir and answer from it.
func acmeCLIScripts(dir string) map[string]string {
	run := func(cmd, answer string) string {
		return "#!/bin/sh\n{ cat; echo; } >> " + dir + "/" + cmd + ".stdin\n" +
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
			`printf '%s' '{"messages":[{"id":"c-9","author":{"id":"u-me","name":"Me"},"at":"2026-10-03T10:00:00Z","body":"answer"}]}'` + "\n",
		"open":   run("open", `{"id":"issue-2","title":"Made","url":"https://acme.test/issue/ACME-2"}`),
		"opened": seen("opened", "open", `{"id":"issue-2","title":"Made","url":"https://acme.test/issue/ACME-2"}`),
		"state":  run("state", `{"status":"In Progress","moved":true}`),
	}
}

// declaredThreadYard is a yard with crew member yardmaster, the declared
// source acme whose commands are beside its file, a hook on acme, an acme
// delivery, and depot yard with bead yard-1. It returns the app, the
// store, the delivery's seq, and the directory the commands record in.
func declaredThreadYard(t *testing.T) (*app, store.API, string, string) {
	t.Helper()
	ctx := context.Background()
	st, own := storetest.NewOwned(t)
	home, declDir, dir := t.TempDir(), t.TempDir(), t.TempDir()
	scripts := acmeCLIScripts(dir)
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
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-1", Title: "one", Depot: "yard"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "acme.yaml", acmeCLI, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetIdentity(ctx, "yardmaster", "acme", store.Identity{ID: "u-me"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutHook(ctx, store.Hook{Name: "acme", Source: "acme", SecretFile: "/s", As: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	env := hooks.Envelope{Source: "acme", Type: "issue", Action: "create",
		Thread: hooks.Thread{ID: "issue-1", Title: "Retries", URL: "https://acme.test/issue/ACME-1"}}
	seq, _, err := own.RecordHook(ctx, "acme", env.Map(), []byte("{}"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "me", cfg: &config.Config{Home: home}}, st, strconv.FormatUint(seq, 10), dir
}

func lastStdin(t *testing.T, dir, cmd string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, cmd+".stdin"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var v map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func stdinRuns(t *testing.T, dir, cmd string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, cmd+".stdin"))
	if os.IsNotExist(err) {
		return 0
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// yardr reply answers a declared source's delivery by running its thread
// commands: thread.reply to post, thread.posted before a second attempt,
// so an attempt whose answer was lost is posted once.
func TestReplyDeclaredSource(t *testing.T) {
	a, _, seq, dir := declaredThreadYard(t)
	t.Setenv("ACME_TOKEN", "tok-1")
	t.Setenv("YARDR_CREW", "yardmaster")

	out, err := run(t, a.replyCmd(), seq, "answer")
	if err != nil {
		t.Fatal(err)
	}
	if out != "https://acme.test/issue/ACME-1#c-9\n" {
		t.Fatalf("output %q", out)
	}
	in := lastStdin(t, dir, "reply")
	if in["thread"] != "issue-1" || in["parent"] != "" || in["body"] != "answer" || in["key"] == "" {
		t.Fatalf("reply stdin = %v", in)
	}
	// The same text again is the same reply: Posted finds it, and no
	// second attempt posts.
	out, err = run(t, a.replyCmd(), seq, "answer")
	if err != nil {
		t.Fatal(err)
	}
	if out != "https://acme.test/issue/ACME-1#c-9\n" {
		t.Fatalf("second run: %q", out)
	}
	if runs := stdinRuns(t, dir, "reply"); runs != 1 {
		t.Fatalf("reply ran %d times", runs)
	}
	if _, err := run(t, a.replyCmd(), "nope", "answer"); err == nil {
		t.Fatal("an unknown event is answered")
	}
}

// acmeMintedCLI is acmeCLI with the two names that together select a
// minted credential.
var acmeMintedCLI = strings.Replace(acmeCLI, "  env: ACME_TOKEN\n", "  env: ACME_TOKEN\n  mint: [ACME_APP_ID, ACME_APP_KEY]\n", 1)

// yardr reply with a mint: the pane holds only the mint's names, the
// operator's mint_command mints the token with the thread as scope, and the
// token is in nothing printed, no event and no record.
func TestReplyDeclaredSourceMinted(t *testing.T) {
	const token = "tok-reply-mint-never-stored"
	a, st, seq, dir := declaredThreadYard(t)
	ctx := context.Background()
	if _, err := st.PutSource(ctx, "acme.yaml", acmeMintedCLI, "me"); err != nil {
		t.Fatal(err)
	}
	mint := filepath.Join(dir, "mint")
	body := "#!/bin/sh\nd=" + dir + "\n{ cat; echo; } >>$d/mint.stdin\n" +
		"printf '{\"token\":\"" + token + "\",\"expires_at\":\"2030-01-01T00:00:00Z\"}'\n"
	scripttest.Write(t, mint, body)
	if _, err := st.SetSource(ctx, "acme", store.SourceMintCommand, mint, "me"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACME_APP_ID", "app-1")
	t.Setenv("ACME_APP_KEY", "keys/app.pem")
	t.Setenv("YARDR_CREW", "yardmaster")

	out, err := run(t, a.replyCmd(), seq, "answer")
	if err != nil {
		t.Fatal(err)
	}
	if out != "https://acme.test/issue/ACME-1#c-9\n" {
		t.Fatalf("output %q", out)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "mint.stdin")); string(raw) != "{\"inputs\":{\"ACME_APP_ID\":\"app-1\",\"ACME_APP_KEY\":\"keys/app.pem\"},\"scope\":\"issue-1\"}\n" {
		t.Fatalf("the mint's stdin = %q", raw)
	}
	evs, err := st.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(evs)
	src, _ := st.GetSource(ctx, "acme")
	rec, _ := json.Marshal(src)
	for what, text := range map[string]string{"what the command printed": out, "the events": string(raw), "the source's record": string(rec)} {
		for _, secret := range []string{token, "mint:v1:", "keys/app.pem"} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s has %q in it: %s", what, secret, text)
			}
		}
	}
}

// yardr crew show names the minted way where the declaration names a mint.
func TestCrewShowDeclaredSourceMinted(t *testing.T) {
	a, st, _, _ := declaredThreadYard(t)
	ctx := context.Background()
	if _, err := st.PutSource(ctx, "acme.yaml", acmeMintedCLI, "me"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "yardmaster.env")
	if err := os.WriteFile(env, []byte("ACME_APP_ID=app-1\nACME_APP_KEY=keys/app.pem\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"env_file": env}}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.crewCmd(), "show", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "minted (ACME_APP_ID, ACME_APP_KEY)") {
		t.Fatalf("credential:\n%s", out)
	}
}

// yardr thread show lists a declared source's thread by its thread.read.
func TestThreadShowDeclaredSource(t *testing.T) {
	a, _, seq, dir := declaredThreadYard(t)
	t.Setenv("ACME_TOKEN", "tok-1")

	out, err := run(t, a.threadShowCmd(), seq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "answer\n") || !strings.Contains(out, "c-9") {
		t.Fatalf("show:\n%s", out)
	}
	in := lastStdin(t, dir, "read")
	if in["thread"] != "issue-1" || len(in) != 1 {
		t.Fatalf("read stdin = %v", in)
	}
}

// yardr thread open opens a declared source's thread by its thread.open,
// once: Opened finds what the key left before a second attempt. The grant
// names the declared source, as crew show says.
func TestThreadOpenDeclaredSource(t *testing.T) {
	a, st, _, dir := declaredThreadYard(t)
	ctx := context.Background()
	t.Setenv("ACME_TOKEN", "tok-1")
	t.Setenv("YARDR_CREW", "yardmaster")

	if _, err := run(t, a.crewCmd(), "add", "yardmaster", "--replace", "--dir", t.TempDir(), "--set", "open_threads=acme"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.crewCmd(), "show", "yardmaster")
	if err != nil || !strings.Contains(out, "may open threads in acme\n") {
		t.Fatalf("show: %q, %v", out, err)
	}
	out, err = run(t, a.threadOpenCmd(), "acme", "Made", "--in", "ENG", "--body", "words")
	if err != nil {
		t.Fatal(err)
	}
	if out != "https://acme.test/issue/ACME-2\n" {
		t.Fatalf("output %q", out)
	}
	in := lastStdin(t, dir, "open")
	if in["in"] != "ENG" || in["title"] != "Made" || in["body"] != "words" || in["key"] == "" {
		t.Fatalf("open stdin = %v", in)
	}
	out, err = run(t, a.threadOpenCmd(), "acme", "Made", "--in", "ENG", "--body", "words")
	if err != nil {
		t.Fatal(err)
	}
	if out != "https://acme.test/issue/ACME-2\n" {
		t.Fatalf("second run: %q", out)
	}
	if runs := stdinRuns(t, dir, "open"); runs != 1 {
		t.Fatalf("open ran %d times", runs)
	}
	// A source the thread's keys do not open in is refused, naming what can.
	if _, err := run(t, a.threadOpenCmd(), "token", "Made", "--in", "x"); err == nil ||
		!strings.Contains(err.Error(), "yardr cannot open threads in token (it can in acme, github)") {
		t.Fatalf("token: %v", err)
	}
	// So is one whose grant the crew member lacks.
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scout", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.threadOpenCmd(), "acme", "Made", "--in", "ENG", "--crew", "scout"); err == nil ||
		!strings.Contains(err.Error(), "scout may not open threads in acme") {
		t.Fatalf("scout: %v", err)
	}
}

// yardr thread state sets a declared source's thread's status by its
// thread.state, with the grant naming the declared source.
func TestThreadStateDeclaredSource(t *testing.T) {
	a, _, seq, dir := declaredThreadYard(t)
	t.Setenv("ACME_TOKEN", "tok-1")
	t.Setenv("YARDR_CREW", "yardmaster")

	if _, err := run(t, a.crewCmd(), "add", "yardmaster", "--replace", "--dir", t.TempDir(), "--set", "set_state=acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.replyCmd(), seq, "answer"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.threadStateCmd(), seq, "started")
	if err != nil {
		t.Fatal(err)
	}
	// The command does not say what the thread was in, so the line has no
	// "(was …)" rather than "(was -)".
	if out != "acme thread issue-1 is now In Progress\n" {
		t.Fatalf("output %q", out)
	}
	in := lastStdin(t, dir, "state")
	if in["thread"] != "issue-1" || in["state"] != "started" || len(in) != 2 {
		t.Fatalf("state stdin = %v", in)
	}
	// A word that is no state at all, and a label the source takes none
	// of, are refused before anything runs.
	if _, err := run(t, a.threadStateCmd(), seq, "blocked"); err == nil ||
		!strings.Contains(err.Error(), `"blocked" is no state: want started, review, done`) {
		t.Fatalf("blocked: %v", err)
	}
	if _, err := run(t, a.threadStateCmd(), seq, "label:ui"); err == nil ||
		!strings.Contains(err.Error(), "yardr sets no label there") {
		t.Fatalf("label: %v", err)
	}
	// A thread named with its source, without a delivery.
	out, err = run(t, a.threadStateCmd(), "--source", "acme", "issue-1", "done")
	if err != nil {
		t.Fatal(err)
	}
	if out != "acme thread issue-1 is now In Progress\n" {
		t.Fatalf("--source output %q", out)
	}
	// A command that says what the thread was in names it in the line, and
	// one that did not move says the thread was already there.
	restate := func(answer string) {
		t.Helper()
		body := "#!/bin/sh\n{ cat; echo; } >> " + dir + "/state.stdin\nprintf '%s' '" + answer + "'\n"
		scripttest.Write(t, filepath.Join(a.home(), "sources", "acme", "state"), body)
	}
	restate(`{"status":"Done","moved":true,"was":"In Progress"}`)
	out, err = run(t, a.threadStateCmd(), seq, "done")
	if err != nil {
		t.Fatal(err)
	}
	if out != "acme thread issue-1 is now Done (was In Progress)\n" {
		t.Fatalf("with was: %q", out)
	}
	restate(`{"status":"Done","moved":false}`)
	out, err = run(t, a.threadStateCmd(), seq, "done")
	if err != nil {
		t.Fatal(err)
	}
	if out != "acme thread issue-1 was already Done; nothing set\n" {
		t.Fatalf("not moved: %q", out)
	}
}

// yardr link links a bead to a declared source's issue: by its URL, or by
// the seq of a delivery from its thread.
func TestLinkDeclaredSource(t *testing.T) {
	a, st, seq, _ := declaredThreadYard(t)
	ctx := context.Background()

	out, err := run(t, a.linkCmd(), "add", "yard-1", "https://acme.test/issue/ACME-1/slug")
	if err != nil {
		t.Fatal(err)
	}
	if out != "yard-1 -tracks-> https://acme.test/issue/ACME-1 (acme)\n" {
		t.Fatalf("add: %q", out)
	}
	if _, err := run(t, a.linkCmd(), "add", "yard-1", "https://acme.test/project/1"); err == nil ||
		!strings.Contains(err.Error(), "is no acme issue") {
		t.Fatalf("a project: %v", err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-2", Title: "two", Depot: "yard"}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.linkCmd(), "add", "yard-2", seq, "--relates")
	if err != nil {
		t.Fatal(err)
	}
	// The delivery is from the same issue yard-1 tracks: a relates link.
	if out != "yard-2 -relates-> https://acme.test/issue/ACME-1 (acme)\n" {
		t.Fatalf("add by seq: %q", out)
	}
	out, err = run(t, a.linkCmd(), "list", "--issue", "https://acme.test/issue/ACME-1")
	if err != nil {
		t.Fatal(err)
	}
	if out != "yard-1  tracks  https://acme.test/issue/ACME-1 (acme)\nyard-2  relates https://acme.test/issue/ACME-1 (acme)\n" {
		t.Fatalf("list:\n%s", out)
	}
}

// A hook on a declared source maps bead stages to its states, refused
// where the source has no status for the word.
func TestHookMapDeclaredSource(t *testing.T) {
	a, st, _, _ := declaredThreadYard(t)
	ctx := context.Background()
	f := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(f, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.hookCmd(), "add", "m", "--source", "acme", "--secret-file", f, "--as", "yardmaster",
		"--map", "task:new=started", "--map", "task:review=done"); err != nil {
		t.Fatal(err)
	}
	h, err := st.GetHook(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if h.Map["task:new"] != "started" || h.Map["task:review"] != "done" {
		t.Fatalf("map = %v", h.Map)
	}
	if _, err := run(t, a.hookCmd(), "add", "n", "--source", "acme", "--secret-file", f, "--as", "yardmaster",
		"--map", "task:new=blocked"); err == nil ||
		!strings.Contains(err.Error(), `"blocked" is no state: want started, review, done`) {
		t.Fatalf("blocked: %v", err)
	}
	if _, err := run(t, a.hookCmd(), "add", "n", "--source", "acme", "--secret-file", f, "--as", "yardmaster",
		"--map", "task:new=label:ui"); err == nil ||
		!strings.Contains(err.Error(), "yardr sets no label there") {
		t.Fatalf("label: %v", err)
	}
	if _, err := run(t, a.hookCmd(), "add", "o", "--source", "acme", "--secret-file", f, "--as", "yardmaster",
		"--map", "task:new"); err == nil ||
		!strings.Contains(err.Error(), `"task:new": want <bead type>:<stage>=<state>`) {
		t.Fatalf("bad line: %v", err)
	}
}

// crew show says what the crew member may do in a declared source, and its
// credential there: the one variable the file names.
func TestCrewShowDeclaredSource(t *testing.T) {
	a, st, _, _ := declaredThreadYard(t)
	ctx := context.Background()
	env := filepath.Join(t.TempDir(), "yardmaster.env")
	if err := os.WriteFile(env, []byte("ACME_TOKEN=tok-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"env_file": env, store.SetState: "acme"}}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.crewCmd(), "show", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "may set the status of threads in acme\n") {
		t.Fatalf("grants:\n%s", out)
	}
	if !strings.Contains(out, "API key (ACME_TOKEN)") {
		t.Fatalf("credential:\n%s", out)
	}
}

// yardr thread open --assign hands a declared source's thread.open the
// user's identity there, the part its thread.assignee names. Refused
// before anything is on record: a source whose declaration takes no
// assignee, a name that is no user, and a user with no identity there,
// with the command that sets one.
func TestThreadOpenAssign(t *testing.T) {
	a, st, _, dir := declaredThreadYard(t)
	ctx := context.Background()
	t.Setenv("ACME_TOKEN", "tok-1")
	t.Setenv("YARDR_CREW", "yardmaster")
	if _, err := run(t, a.crewCmd(), "add", "yardmaster", "--replace", "--dir", t.TempDir(), "--set", "open_threads=acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	// Who jo is in a source is jo's to say.
	a.actor = "jo"
	open := func(args ...string) (string, error) {
		return run(t, a.threadOpenCmd(), append([]string{"acme", "Made", "--in", "ENG"}, args...)...)
	}

	// acme's declaration says nothing of an assignee: refused, naming the
	// sources that take one.
	if _, err := open("--assign", "jo"); err == nil ||
		!strings.Contains(err.Error(), "yardr cannot assign a thread it opens in acme (it does in github): open it without --assign") {
		t.Fatalf("a source that takes no assignee: %v", err)
	}

	with := strings.Replace(acmeCLI, "  open: [open]\n", "  open: [open]\n  assignee: id\n", 1)
	if _, err := st.PutSource(ctx, "acme.yaml", with, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := open("--assign", "nobody"); err == nil ||
		!strings.Contains(err.Error(), "--assign nobody: no user of the yard (yardr user list)") {
		t.Fatalf("no user: %v", err)
	}
	// A crew member is no user, whatever it is in the source.
	if _, err := open("--assign", "yardmaster"); err == nil || !strings.Contains(err.Error(), "no user of the yard") {
		t.Fatalf("a crew member: %v", err)
	}
	if _, err := open("--assign", "jo"); err == nil ||
		!strings.Contains(err.Error(), "user jo has no id in acme, which is what its threads are assigned by: yardr user identity jo --source acme --id <their id there>") {
		t.Fatalf("no identity: %v", err)
	}
	// The key alone is not what acme assigns by.
	if _, err := run(t, a.userCmd(), "identity", "jo", "--source", "acme", "--key", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := open("--assign", "jo"); err == nil || !strings.Contains(err.Error(), "user jo has no id in acme") {
		t.Fatalf("a key where an id is taken: %v", err)
	}
	if runs := stdinRuns(t, dir, "open"); runs != 0 {
		t.Fatalf("open ran %d times for refusals", runs)
	}
	if ts, err := st.Threads(ctx, "yardmaster"); err != nil || len(ts) != 0 {
		t.Fatalf("a refusal left a thread on record: %+v, %v", ts, err)
	}

	out, err := run(t, a.userCmd(), "identity", "jo", "--source", "acme", "--id", "u-jo", "--key", "jo")
	if err != nil || !strings.Contains(out, "acme") || !strings.Contains(out, "u-jo") {
		t.Fatalf("identity: %q, %v", out, err)
	}
	if out, err = run(t, a.userCmd(), "show", "jo"); err != nil || !strings.Contains(out, "acme identity: u-jo, jo\n") {
		t.Fatalf("show: %q, %v", out, err)
	}
	if out, err = open("--assign", "jo"); err != nil || out != "https://acme.test/issue/ACME-2\n" {
		t.Fatalf("assigned open: %q, %v", out, err)
	}
	if in := lastStdin(t, dir, "open"); in["assignee"] != "u-jo" || in["title"] != "Made" {
		t.Fatalf("open stdin = %v", in)
	}

	// Without --assign the object has no assignee.
	if _, err := run(t, a.threadOpenCmd(), "acme", "Another", "--in", "ENG"); err != nil {
		t.Fatal(err)
	}
	if in := lastStdin(t, dir, "open"); in["title"] != "Another" {
		t.Fatalf("open stdin = %v", in)
	} else if _, ok := in["assignee"]; ok {
		t.Fatalf("unassigned open carries an assignee: %v", in)
	}

	// The identity command of one kind of party does not set the other's.
	if _, err := run(t, a.userCmd(), "identity", "yardmaster", "--source", "acme", "--id", "x"); err == nil {
		t.Fatal("yardr user identity set a crew member's")
	}
	if _, err := run(t, a.crewCmd(), "identity", "jo", "--source", "acme", "--id", "x"); err == nil {
		t.Fatal("yardr crew identity set a user's")
	}
}

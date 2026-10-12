package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// testdata/pushover.yaml is the file of the design (yardr-1481), whole. It
// is added, shown, listed and removed, and nothing is sent: it names no
// check call.
func TestSourceAddShowRemove(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	out, err := run(t, a.sourceCmd(), "add", "testdata/pushover.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"source pushover saved: 1 call under https://api.pushover.net/1",
		"notify: the call message tells a user, at the PUSHOVER_USER of their env_file (yardr user set <name> notify=pushover)",
		"next: yardr source set pushover env_file=<a file of mode 0600 that sets PUSHOVER_TOKEN>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("add printed no %q:\n%s", want, out)
		}
	}
	out, err = run(t, a.sourceCmd(), "show", "pushover")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pushover  https://api.pushover.net/1",
		"A push to a person's phone through Pushover.",
		"credential: PUSHOVER_TOKEN is not set up: yardr source set pushover env_file=",
		"check: the file names no call for it",
		"call message  update  POST /messages.json  One push to one person",
		"user  string  matching ^[A-Za-z0-9]{30}$",
		"link  string  at most 512 characters  optional",
		"notify: the call message tells a user, at the PUSHOVER_USER of their env_file",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show printed no %q:\n%s", want, out)
		}
	}
	if out, err = run(t, a.sourceCmd(), "list"); err != nil || !strings.Contains(out, "message (update)") || !strings.Contains(out, "PUSHOVER_TOKEN in no env_file yet") {
		t.Fatalf("list: %q %v", out, err)
	}
	if out, err = run(t, a.sourceCmd(), "check", "pushover"); err == nil || !strings.Contains(err.Error(), "PUSHOVER_TOKEN is not set up") {
		t.Fatalf("check without an env_file: %q %v", out, err)
	}
	env := envFile(t, "PUSHOVER_TOKEN=tok-of-the-yard\n", 0o600)
	if out, err = run(t, a.sourceCmd(), "set", "pushover", "env_file="+env); err != nil || !strings.Contains(out, "source pushover: PUSHOVER_TOKEN is set in "+env) {
		t.Fatalf("set: %q %v", out, err)
	}
	out, err = run(t, a.sourceCmd(), "check", "pushover")
	if err != nil || !strings.Contains(out, "names no check call") || !strings.Contains(out, "nothing was sent") || strings.Contains(out, "tok-of-the-yard") {
		t.Fatalf("check of a source without a check call: %q %v", out, err)
	}
	// The file again, changed: the source keeps its env_file, and what
	// changed of its calls is said.
	text, _ := os.ReadFile("testdata/pushover.yaml")
	changed := filepath.Join(t.TempDir(), "pushover.yaml")
	if err := os.WriteFile(changed, []byte(strings.Replace(string(text), "path: /messages.json", "path: /messages2.json", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.sourceCmd(), "add", changed); err != nil || !strings.Contains(out, "changed call message (update): POST /messages2.json, was (update) POST /messages.json") || strings.Contains(out, "next:") {
		t.Fatalf("add again: %q %v", out, err)
	}
	if out, err = run(t, a.sourceCmd(), "set", "pushover", "env_file="); err != nil || !strings.Contains(out, "no env_file") {
		t.Fatalf("set to nothing: %q %v", out, err)
	}
	if out, err = run(t, a.sourceCmd(), "rm", "pushover"); err != nil || !strings.Contains(out, "pushover: removed") {
		t.Fatalf("rm: %q %v", out, err)
	}
	if _, err = run(t, a.sourceCmd(), "show", "pushover"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("show after rm: %v", err)
	}
}

// source show of a source no call of which is granted, whose key a crew
// member holds, says the file is not set and nothing needs it — not the
// command that sets it. Only its check call wants the source's own file.
func TestSourceShowEnvFileACrewMemberHolds(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	if _, err := run(t, a.sourceCmd(), "add", "testdata/pushover.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(context.Background(), store.CrewMember{Name: "boss", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "env_file": envFile(t, "PUSHOVER_TOKEN=tok-of-boss\n", 0o600)}}, "jo"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "show", "pushover")
	if err != nil {
		t.Fatal(err)
	}
	if want := "  credential: none set: nobody is granted a call of pushover, and crew member boss holds PUSHOVER_TOKEN\n"; !strings.Contains(out, want) {
		t.Fatalf("show lacks %q:\n%s", want, out)
	}
	if strings.Contains(out, "tok-of-boss") {
		t.Fatalf("show prints a secret:\n%s", out)
	}
}

func TestSourceShowKeepsEnvFileNeedForNotifiedUser(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	if _, err := run(t, a.sourceCmd(), "add", "testdata/pushover.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(context.Background(), store.CrewMember{Name: "boss", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "env_file": envFile(t, "PUSHOVER_TOKEN=tok-of-boss\n", 0o600)}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(context.Background(), "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUser(context.Background(), "ada", map[string]string{store.UserNotify: "pushover", store.UserEnvFile: "/home/ada/ada.env"}, "ada"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "show", "pushover")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  credential: PUSHOVER_TOKEN is not set up: yardr source set pushover env_file=") {
		t.Fatalf("show hid the source credential needed for user notifications:\n%s", out)
	}
}

func envFile(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "source.env")
	if err := os.WriteFile(f, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, mode); err != nil { // past the umask
		t.Fatal(err)
	}
	return f
}

// A file that is refused is refused with its line and key, and nothing is
// kept: here the credential in a path.
func TestSourceAddRefuses(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	text, _ := os.ReadFile("testdata/pushover.yaml")
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte(strings.Replace(string(text), "path: /messages.json", `path: "/messages/{credential}.json"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, a.sourceCmd(), "add", bad)
	if err == nil || !strings.Contains(err.Error(), bad+":17: calls.message.request.path: {credential} in a path") {
		t.Fatalf("err = %v", err)
	}
	if all, _ := st.Sources(context.Background()); len(all) != 0 {
		t.Fatalf("a refused file was kept: %+v", all)
	}
}

const checkedSource = `name: crm
base: BASE/api
credential: { env: CRM_TOKEN, header: "Authorization: Bearer {credential}" }
check: ping
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// source check makes the one call the file names against a stand-in on the
// loopback and reports its status. The credential is read from the env_file
// for it and is in nothing the command prints and in no event.
func TestSourceCheck(t *testing.T) {
	const token = "tok-9c41-not-to-be-seen"
	status, location, hits, auth := 200, "", 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		auth = r.Header.Get("Authorization")
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
		if r.URL.Path == "/api/me" && status == 200 {
			io.WriteString(w, `{"id":"app-1"}`)
		}
	}))
	defer srv.Close()
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	file := filepath.Join(t.TempDir(), "crm.yaml")
	if err := os.WriteFile(file, []byte(strings.Replace(checkedSource, "BASE", srv.URL, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}

	// An env_file open to others, and one that does not set the name, are
	// refused when they are set.
	if _, err := run(t, a.sourceCmd(), "set", "crm", "env_file="+envFile(t, "CRM_TOKEN="+token+"\n", 0o644)); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("an env_file of mode 0644: %v", err)
	}
	other := envFile(t, "OTHER="+token+"\n", 0o600)
	if _, err := run(t, a.sourceCmd(), "set", "crm", "env_file="+other); err == nil || !strings.Contains(err.Error(), "env_file "+other+" does not set CRM_TOKEN") {
		t.Fatalf("an env_file without the name: %v", err)
	}
	if _, err := run(t, a.sourceCmd(), "set", "crm", "token="+token); err == nil || !strings.Contains(err.Error(), "want env_file=<file>") {
		t.Fatalf("a setting a source does not have: %v", err)
	}
	env := envFile(t, "# the yard's\nCRM_TOKEN="+token+"\n", 0o600)
	if _, err := run(t, a.sourceCmd(), "set", "crm", "env_file="+env); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.sourceCmd(), "check", "crm")
	if err != nil || !strings.Contains(out, "crm: check call ping (GET /me): 200 in ") || !strings.Contains(out, "14 bytes") {
		t.Fatalf("check: %q %v", out, err)
	}
	if hits != 1 || auth != "Bearer "+token {
		t.Fatalf("the stand-in was called %d times, with Authorization %q", hits, auth)
	}
	said := out

	status = 401
	_, err = run(t, a.sourceCmd(), "check", "crm")
	if err == nil || !strings.Contains(err.Error(), "the check call ping (GET /me) was answered 401: the service does not take what CRM_TOKEN is set to") {
		t.Fatalf("a 401: %v", err)
	}
	said += err.Error()

	status, location = 302, "http://127.0.0.1:1/elsewhere"
	_, err = run(t, a.sourceCmd(), "check", "crm")
	if err == nil || !strings.Contains(err.Error(), "answered 302, a redirect to 127.0.0.1:1: yardr follows none") {
		t.Fatalf("a redirect: %v", err)
	}
	said += err.Error()
	if hits != 3 {
		t.Fatalf("the stand-in was called %d times, want 3: one call per check", hits)
	}

	// The file is read at each call: gone, the check says so and sends
	// nothing.
	if err := os.Remove(env); err != nil {
		t.Fatal(err)
	}
	if _, err = run(t, a.sourceCmd(), "check", "crm"); err == nil || !strings.Contains(err.Error(), "source crm: env_file "+env) || hits != 3 {
		t.Fatalf("with the env_file gone: %v (%d calls)", err, hits)
	}

	evs, err := st.EventsSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(evs)
	src, _ := st.GetSource(context.Background(), "crm")
	rec, _ := json.Marshal(src)
	for what, text := range map[string]string{"what the command printed": said, "the events": string(raw), "the source's record": string(rec)} {
		if strings.Contains(text, token) {
			t.Fatalf("%s has the credential in it: %s", what, text)
		}
	}
	if !strings.Contains(string(raw), `"env_file":"`+env+`"`) {
		t.Fatalf("source_set does not say which file: %s", raw)
	}
}

const checkedMintedSource = `name: crm
base: BASE/api
credential: { env: CRM_TOKEN, mint: [CRM_APP_ID, CRM_APP_KEY], header: "Authorization: Bearer {credential}" }
check: ping
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// source check with a mint mints the token with the source's mint_command
// and makes the check call with it. Without the command it fails naming
// yardr source set. The token is in nothing the command prints and in no
// event.
func TestSourceCheckMinted(t *testing.T) {
	const token = "tok-mint-9c41-not-to-be-seen"
	status, hits, auth := 200, 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		auth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		if r.URL.Path == "/api/me" && status == 200 {
			io.WriteString(w, `{"id":"app-1"}`)
		}
	}))
	defer srv.Close()
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	file := filepath.Join(t.TempDir(), "crm.yaml")
	if err := os.WriteFile(file, []byte(strings.Replace(checkedMintedSource, "BASE", srv.URL, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "add", file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "next: yardr source set crm env_file=<a file of mode 0600 that sets CRM_APP_ID and CRM_APP_KEY, or CRM_TOKEN>") {
		t.Fatalf("add: %q", out)
	}
	if out, err = run(t, a.sourceCmd(), "list"); err != nil || !strings.Contains(out, "CRM_APP_ID, CRM_APP_KEY (minted), CRM_TOKEN in no env_file yet") {
		t.Fatalf("list: %q %v", out, err)
	}

	// The file is set before the command: the names are validated, and
	// the command is the next step.
	dir := t.TempDir()
	mint := filepath.Join(dir, "mint")
	body := "#!/bin/sh\nd=" + dir + "\n" +
		"n=$(($(cat $d/runs 2>/dev/null || echo 0)+1)); echo $n >$d/runs\n" +
		"{ cat; echo; } >>$d/stdin\n" +
		"printf '{\"token\":\"" + token + "\",\"expires_at\":\"2030-01-01T00:00:00Z\"}'\n"
	if err := os.WriteFile(mint, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	env := envFile(t, "CRM_APP_ID=app-1\nCRM_APP_KEY=keys/app.pem\n", 0o600)
	out, err = run(t, a.sourceCmd(), "set", "crm", "env_file="+env)
	if err != nil || !strings.Contains(out, "source crm: CRM_APP_ID and CRM_APP_KEY are set in "+env+"; no mint_command is set: yardr source set crm mint_command=<program> [args...]") {
		t.Fatalf("set env_file: %q %v", out, err)
	}
	said := out
	if out, err = run(t, a.sourceCmd(), "show", "crm"); err != nil || !strings.Contains(out, "credential: no mint_command is set: yardr source set crm mint_command=") {
		t.Fatalf("show without the command: %q %v", out, err)
	}

	// Without the command the check fails naming the setting.
	if _, err = run(t, a.sourceCmd(), "check", "crm"); err == nil || !strings.Contains(err.Error(), "source crm: no mint_command is set: yardr source set crm mint_command=<program> [args...]") {
		t.Fatalf("check without the command: %v", err)
	} else {
		said += err.Error()
	}

	// A command that is not there, one that is no absolute path, and one
	// for a declaration that names no mint are refused when they are set.
	if _, err := run(t, a.sourceCmd(), "set", "crm", "mint_command="+filepath.Join(dir, "nope")); err == nil || !strings.Contains(err.Error(), "mint_command "+filepath.Join(dir, "nope")) {
		t.Fatalf("a command that is not there: %v", err)
	}
	if _, err := run(t, a.sourceCmd(), "set", "crm", "mint_command=./mint"); err == nil || !strings.Contains(err.Error(), "want an absolute path, or a bare name on PATH") {
		t.Fatalf("a relative command: %v", err)
	}
	plain := filepath.Join(t.TempDir(), "plain.yaml")
	if err := os.WriteFile(plain, []byte(strings.Replace(checkedSource, "BASE", srv.URL, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.sourceCmd(), "add", plain); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.sourceCmd(), "set", "crm", "mint_command="+mint); err == nil || !strings.Contains(err.Error(), "names no mint (credential.mint)") {
		t.Fatalf("a command for a declaration with no mint: %v", err)
	}
	// The plain declaration replaced the minted one; add it back.
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}

	if out, err = run(t, a.sourceCmd(), "set", "crm", "mint_command="+mint); err != nil || !strings.Contains(out, "source crm: mint command "+mint) {
		t.Fatalf("set mint_command: %q %v", out, err)
	}
	if out, err = run(t, a.sourceCmd(), "show", "crm"); err != nil || !strings.Contains(out, "credential: CRM_APP_ID and CRM_APP_KEY are set in "+env+", minted with "+mint) {
		t.Fatalf("show: %q %v", out, err)
	}

	out, err = run(t, a.sourceCmd(), "check", "crm")
	if err != nil || !strings.Contains(out, "crm: credential: CRM_APP_ID and CRM_APP_KEY are set in "+env+", minted, good until ") ||
		!strings.Contains(out, "crm: check call ping (GET /me): 200 in ") {
		t.Fatalf("check: %q %v", out, err)
	}
	said += out
	if hits != 1 || auth != "Bearer "+token {
		t.Fatalf("the stand-in was called %d times, with Authorization %q", hits, auth)
	}
	// The mint ran once, with the inputs and no scope, and the second
	// check uses the token in hand.
	if raw, _ := os.ReadFile(filepath.Join(dir, "stdin")); string(raw) != "{\"inputs\":{\"CRM_APP_ID\":\"app-1\",\"CRM_APP_KEY\":\"keys/app.pem\"},\"scope\":\"\"}\n" {
		t.Fatalf("the mint's stdin = %q", raw)
	}
	if out, err = run(t, a.sourceCmd(), "check", "crm"); err != nil {
		t.Fatal(err)
	} else {
		said += out
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "runs")); string(raw) != "1\n" {
		t.Fatalf("the mint ran %q times, want once", raw)
	}

	status = 401
	if _, err = run(t, a.sourceCmd(), "check", "crm"); err == nil || !strings.Contains(err.Error(), "was answered 401: the service does not take its credential (minted from CRM_APP_ID and CRM_APP_KEY, or CRM_TOKEN)") {
		t.Fatalf("a 401: %v", err)
	} else {
		said += err.Error()
	}
	status = 200

	if out, err = run(t, a.sourceCmd(), "set", "crm", "mint_command="); err != nil || !strings.Contains(out, "no mint_command") {
		t.Fatalf("set to nothing: %q %v", out, err)
	}
	if _, err = run(t, a.sourceCmd(), "check", "crm"); err == nil || !strings.Contains(err.Error(), "no mint_command is set") {
		t.Fatalf("check after unsetting: %v", err)
	}

	evs, err := st.EventsSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(evs)
	src, _ := st.GetSource(context.Background(), "crm")
	rec, _ := json.Marshal(src)
	for what, text := range map[string]string{"what the commands printed": said, "the events": string(raw), "the source's record": string(rec)} {
		if strings.Contains(text, token) {
			t.Fatalf("%s has the token in it: %s", what, text)
		}
	}
	if !strings.Contains(string(raw), `"mint_command":"`+mint+`"`) {
		t.Fatalf("source_set does not say which command: %s", raw)
	}
}

// Show prints a link's pull shape and repos pattern, when the source
// declares them.
func TestSourceShowPullAndRepos(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	_, err := st.PutSource(context.Background(), "forge.yaml", "name: forge\nbase: https://forge.test\n"+
		"calls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"+
		"link:\n  host: forge.test\n  issue: '^/([^/]+)/([^/]+)/issues/([0-9]+)'\n"+
		"  fixed: \"https://forge.test/{1}/{2}/issues/{3}\"\n  thread: \"{1}/{2}#{3}\"\n"+
		"  pull:\n    issue: '^/([^/]+)/([^/]+)/pull/([0-9]+)'\n"+
		"    fixed: \"https://forge.test/{1}/{2}/pull/{3}\"\n    thread: \"{1}/{2}#{3}\"\n"+
		"  repos: '[^/]+/[^/]+'\n", "jo")
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "show", "forge")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"link: issues of forge.test",
		"pull: https://forge.test/{1}/{2}/pull/{3} (^/([^/]+)/([^/]+)/pull/([0-9]+))",
		"pull thread: {1}/{2}#{3}",
		"repos: [^/]+/[^/]+",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show printed no %q:\n%s", want, out)
		}
	}
}

// Show prints the events key's run and head types and its look-back
// commands, when the source declares them.
func TestSourceShowEvents(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	_, err := st.PutSource(context.Background(), "forge.yaml", "name: forge\nbase: https://forge.test\n"+
		"credential: { env: FORGE_TOKEN }\n"+
		"calls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"+
		"events:\n  runs: [workflow_run, check_suite]\n  head_from: [pull_request]\n"+
		"  pr: [forge/pr]\n  ci: [forge/ci]\n", "jo")
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "show", "forge")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"command events.pr: forge/pr",
		"command events.ci: forge/ci",
		"events.runs: workflow_run, check_suite",
		"events.head_from: pull_request",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show printed no %q:\n%s", want, out)
		}
	}
}

// A command of a declaration that is a file beside the declaration's file
// is copied into the yard at add, listed by show with its sha256, said
// runnable by check without being run, and removed with the source; an
// absolute program is the operator's own.
func TestSourceCommandFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YARDR_HOME", home)
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	dir := t.TempDir()
	// own is the operator's own program: there, and not copied.
	own := filepath.Join(dir, "own")
	if err := os.WriteFile(own, []byte("#!/bin/sh\necho '{}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "lin.yaml")
	decl := "name: lin\nbase: https://api.linear.app\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" +
		"thread:\n  reply: [scripts/thread, reply]\n  open: [" + own + ", open]\n  place: team\n  labels: true\n" +
		"link:\n  host: linear.app\n  issue: '^/([^/]+)/issue/([A-Z][A-Z0-9]*-[0-9]+)'\n  fixed: \"https://linear.app/{1}/issue/{2}\"\n  thread: \"{2}\"\n  fold: lower\n" +
		"identity:\n  fold: lower\n" +
		"hook:\n  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: Lin-Sig, encoding: hex }\n    delivery: { header: Lin-Delivery }\n  bodies: unique\n  normalize: [scripts/normalize]\n"
	if err := os.WriteFile(file, []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}

	// A program that is no file beside the declaration keeps it from being
	// added, and nothing is kept.
	if _, err := run(t, a.sourceCmd(), "add", file); err == nil ||
		!strings.Contains(err.Error(), "a command's program scripts/thread is a file beside the declaration's file") {
		t.Fatalf("add without the file: %v", err)
	}
	if all, _ := st.Sources(context.Background()); len(all) != 0 {
		t.Fatalf("a source without its file was kept: %+v", all)
	}
	if err := os.Mkdir(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "thread"), []byte("#!/bin/sh\necho '{}'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "normalize"), []byte("#!/bin/sh\necho '{}'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "add", file)
	if err != nil || !strings.Contains(out, "the commands' files are under "+filepath.Join(home, "sources", "lin")+", each mode 0700: scripts/thread, scripts/normalize") {
		t.Fatalf("add: %q %v", out, err)
	}
	copied := filepath.Join(home, "sources", "lin", "scripts", "thread")
	if fi, err := os.Stat(copied); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the copy = %v, %v", fi, err)
	}

	out, err = run(t, a.sourceCmd(), "show", "lin")
	sum := sha256.Sum256([]byte("#!/bin/sh\necho '{}'\n"))
	normalized := filepath.Join(home, "sources", "lin", "scripts", "normalize")
	for _, want := range []string{
		"command thread.reply: scripts/thread reply",
		"file " + copied + " sha256 " + hex.EncodeToString(sum[:]),
		"command thread.open: " + own + " open",
		"the program is the operator's own, not copied",
		"thread.place: team",
		"thread.labels: label:<name> adds one, never removes",
		"link: issues of linear.app",
		"fold: lower",
		"identity: keys fold lower",
		"command hook.normalize: scripts/normalize",
		"file " + normalized + " sha256 " + hex.EncodeToString(sum[:]),
		"hook.verify: hmac-sha256 of the body in Lin-Sig",
		"hook.bodies: unique",
	} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("show printed no %q: %q %v", want, out, err)
		}
	}

	// check runs none of them: it says of each whether its program can be
	// run, and fails when one cannot.
	out, err = run(t, a.sourceCmd(), "check", "lin")
	for _, want := range []string{
		"lin: command thread.reply (scripts/thread reply): can be run",
		"lin: command thread.open (" + own + " open): can be run",
		"lin: command hook.normalize (scripts/normalize): can be run",
	} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("check printed no %q: %q %v", want, out, err)
		}
	}
	if err := os.Remove(copied); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.sourceCmd(), "check", "lin")
	if err == nil || !strings.Contains(out, "lin: command thread.reply (scripts/thread reply): ") || !strings.Contains(out, "no such file") {
		t.Fatalf("check with a program gone: %q %v", out, err)
	}

	out, err = run(t, a.sourceCmd(), "rm", "lin")
	if err != nil || !strings.Contains(out, "lin: removed") {
		t.Fatalf("rm: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(home, "sources", "lin")); !os.IsNotExist(err) {
		t.Fatalf("the commands' directory stays: %v", err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Fatalf("the operator's own program was removed: %v", err)
	}
}

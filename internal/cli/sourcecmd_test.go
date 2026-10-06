package cli

import (
	"context"
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
		"note: the file's notify is kept with it and not read by this yardr",
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
		"kept with the file and not read by this yardr: notify",
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

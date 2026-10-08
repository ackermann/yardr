package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A user is given a source as their notify, and source check --user sends
// one test message through it: the stand-in gets the user's address from
// the user's env file and the yard's token from the source's. Neither is
// in anything a command prints, in an event or in a record.
func TestUserSetAndSourceCheckUser(t *testing.T) {
	const token, address = "tok-7d10-not-to-be-seen", "u9f2k4xq71ab03cd55ef8gh2ij6kl9"
	status := 200
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		got = append(got, body)
		w.WriteHeader(status)
		io.WriteString(w, `{"status":1}`)
	}))
	defer srv.Close()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	jo, ada, bob := &app{st: st, actor: "jo"}, &app{st: st, actor: "ada"}, &app{st: st, actor: "bob"}
	for _, name := range []string{"ada", "bob"} {
		if _, err := run(t, jo.userCmd(), "add", name); err != nil {
			t.Fatal(err)
		}
	}
	text, _ := os.ReadFile("testdata/pushover.yaml")
	file := filepath.Join(t.TempDir(), "pushover.yaml")
	if err := os.WriteFile(file, []byte(strings.Replace(string(text), "https://api.pushover.net/1", srv.URL, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	crm := filepath.Join(t.TempDir(), "crm.yaml")
	if err := os.WriteFile(crm, []byte(strings.Replace(checkedSource, "BASE", srv.URL, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{file, crm} {
		if _, err := run(t, jo.sourceCmd(), "add", f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run(t, jo.sourceCmd(), "set", "pushover", "env_file="+envFile(t, "PUSHOVER_TOKEN="+token+"\n", 0o600)); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, jo.sourceCmd(), "list"); err != nil || !strings.Contains(out, "NOTIFY") {
		t.Fatalf("list: %q %v", out, err)
	}
	if out, err := run(t, jo.sourceCmd(), "show", "crm"); err != nil || !strings.Contains(out, "notify: the file has no notify key, so no user is told through it") {
		t.Fatalf("show of a source without the key: %q %v", out, err)
	}
	var said string

	// A source without a notify key is refused, with the ones that have one.
	_, err := run(t, ada.userCmd(), "set", "ada", "notify=crm")
	if err == nil || !strings.Contains(err.Error(), "the source crm has no notify key") || !strings.Contains(err.Error(), "A user can be told through: pushover") {
		t.Fatalf("notify=crm: %v", err)
	}
	// An env file open to others, and one that does not set the source's
	// name, are refused when they are set.
	if _, err = run(t, ada.userCmd(), "set", "ada", "notify=pushover", "env_file="+envFile(t, "PUSHOVER_USER="+address+"\n", 0o644)); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("an env_file of mode 0644: %v", err)
	}
	other := envFile(t, "OTHER="+address+"\n", 0o600)
	if _, err = run(t, ada.userCmd(), "set", "ada", "notify=pushover", "env_file="+other); err == nil || !strings.Contains(err.Error(), "env_file "+other+" does not set PUSHOVER_USER") {
		t.Fatalf("an env_file without the name: %v", err)
	}
	said += err.Error()
	if _, err = run(t, ada.userCmd(), "set", "ada", "push=mail"); err == nil || !strings.Contains(err.Error(), "want notify=<source> or env_file=<file>") {
		t.Fatalf("a setting a user does not have: %v", err)
	}
	if u, _ := st.GetUser(ctx, "ada"); u.Notify != "" || u.EnvFile != "" {
		t.Fatalf("a refused setting was kept: %+v", u)
	}
	// Someone else's is not theirs to set.
	env := envFile(t, "# ada's own\nPUSHOVER_USER="+address+"\n", 0o600)
	if _, err = run(t, bob.userCmd(), "set", "ada", "notify=pushover", "env_file="+env); err == nil || !strings.Contains(err.Error(), "this is bob") {
		t.Fatalf("bob setting ada's: %v", err)
	}

	// The file first, to try the source before it is named.
	out, err := run(t, ada.userCmd(), "set", "ada", "env_file="+env)
	if err != nil || !strings.Contains(out, "told outside the yard: nowhere (env_file "+env+" is set, and no notify)") {
		t.Fatalf("set env_file: %q %v", out, err)
	}
	said += out
	out, err = run(t, ada.sourceCmd(), "check", "pushover", "--user", "ada")
	if err != nil || !strings.Contains(out, "pushover: told ada: 200 in ") || !strings.Contains(out, "next: yardr user set ada notify=pushover") {
		t.Fatalf("check --user: %q %v", out, err)
	}
	said += out
	if len(got) != 1 || got[0]["user"] != address || got[0]["token"] != token || got[0]["title"] != "yardr: a test" ||
		!strings.Contains(got[0]["message"].(string), "ada is told here by the yard, through the source pushover") {
		t.Fatalf("the stand-in got %v", got)
	}
	if _, has := got[0]["url"]; has {
		t.Fatalf("a link was sent where there is none: %v", got[0])
	}

	out, err = run(t, jo.userCmd(), "set", "ada", "notify=pushover")
	if err != nil || !strings.Contains(out, "user ada: told outside the yard: through pushover (PUSHOVER_USER is set in "+env+")") ||
		!strings.Contains(out, "next: yardr source check pushover --user ada") {
		t.Fatalf("the operator sets notify: %q %v", out, err)
	}
	said += out
	if out, err = run(t, ada.userCmd(), "show", "ada"); err != nil || !strings.Contains(out, "told outside the yard: through pushover (PUSHOVER_USER is set in "+env+")") {
		t.Fatalf("show: %q %v", out, err)
	}
	said += out
	if out, err = run(t, ada.userCmd(), "show", "bob"); err != nil || strings.Contains(out, "told outside") {
		t.Fatalf("show of a user who is told nowhere: %q %v", out, err)
	}
	// The operator may ask for the test, another user may not.
	if out, err = run(t, jo.sourceCmd(), "check", "pushover", "--user", "ada"); err != nil || strings.Contains(out, "next:") || len(got) != 2 {
		t.Fatalf("the operator's check --user: %q %v (%d calls)", out, err, len(got))
	}
	if _, err = run(t, bob.sourceCmd(), "check", "pushover", "--user", "ada"); err == nil || !strings.Contains(err.Error(), "a test message to ada is theirs to ask for, or the operator's (jo); this is bob") || len(got) != 2 {
		t.Fatalf("bob's check --user ada: %v (%d calls)", err, len(got))
	}
	// A source that tells nobody, and an answer that is no success.
	if _, err = run(t, ada.sourceCmd(), "check", "crm", "--user", "ada"); err == nil || !strings.Contains(err.Error(), "the source crm has no notify key") {
		t.Fatalf("check --user of a source without the key: %v", err)
	}
	status = 400
	if _, err = run(t, ada.sourceCmd(), "check", "pushover", "--user", "ada"); err == nil || !strings.Contains(err.Error(), "telling ada was answered 400: nothing reached them") {
		t.Fatalf("a 400: %v", err)
	}
	said += err.Error()

	// The file is read at each send: gone, show says so and nothing is sent.
	if err := os.Remove(env); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, ada.userCmd(), "show", "ada"); err != nil || !strings.Contains(out, "through pushover, which is not set up: user ada: env_file "+env) {
		t.Fatalf("show with the env_file gone: %q %v", out, err)
	}
	said += out
	if out, err = run(t, ada.userCmd(), "set", "ada", "notify=", "env_file="); err != nil || !strings.Contains(out, "told outside the yard: nowhere") {
		t.Fatalf("set to nothing: %q %v", out, err)
	}

	evs, err := st.EventsSince(ctx, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(evs)
	users, _ := st.Users(ctx)
	rec, _ := json.Marshal(users)
	for what, text := range map[string]string{"what the commands printed": said, "the events": string(raw), "the users' records": string(rec)} {
		if strings.Contains(text, address) || strings.Contains(text, token) {
			t.Fatalf("%s has the user's address or the credential in it: %s", what, text)
		}
	}
	if !strings.Contains(string(raw), `"user_set"`) || !strings.Contains(string(raw), `"env_file":"`+env+`"`) {
		t.Fatalf("user_set does not say which file: %s", raw)
	}
}

// yardr user show says how the last message to a user outside the yard
// ended, and yardr doctor notes a user whose last one was not taken, until
// a later one is.
func TestUserShowAndDoctorSayTheLastPush(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	jo := &app{st: st, actor: "jo"}
	if _, err := run(t, jo.userCmd(), "add", "ada"); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile("testdata/pushover.yaml")
	if _, err := st.PutSource(ctx, "pushover.yaml", string(text), "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "pushover"}, "jo"); err != nil {
		t.Fatal(err)
	}
	notes := func() string {
		t.Helper()
		out, err := partyNotes(ctx, st, nil, nil, "yardr")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(out, "\n")
	}
	if out, err := run(t, jo.userCmd(), "show", "ada"); err != nil || strings.Contains(out, "last told") || notes() != "" {
		t.Fatalf("before any message: %q %v, doctor: %q", out, err, notes())
	}
	seq := func() []uint64 {
		t.Helper()
		if err := st.Mail(ctx, "ada", "the tests are green", "jo"); err != nil {
			t.Fatal(err)
		}
		ps, err := st.Pushes(ctx)
		if err != nil || len(ps) != 1 {
			t.Fatalf("pushes = %+v, %v", ps, err)
		}
		return []uint64{ps[0].Seq}
	}
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)
	when := at.Format("2006-01-02 15:04")
	if err := st.PushTried(ctx, "ada", seq(), store.PushResult{At: at, Source: "pushover", Reason: store.PushRefused, Status: 400, Error: "pushover answered 400", Tries: 1}); err != nil {
		t.Fatal(err)
	}
	line := when + " through pushover, not sent: refused (pushover answered 400, 1 of the inbox in one message)"
	if out, err := run(t, jo.userCmd(), "show", "ada"); err != nil || !strings.Contains(out, "  last told: "+line+"\n") {
		t.Fatalf("show after a refusal: %q %v", out, err)
	}
	if got, want := notes(), "user ada was not told outside the yard: "+line+". Their inbox has it all the same; yardr source check pushover --user ada sends one test message"; got != want {
		t.Fatalf("doctor:\n%s\nwant\n%s", got, want)
	}
	// One that is still tried says when, and is noted too.
	retry := at.Add(30 * time.Second)
	again := seq()
	if err := st.PushTried(ctx, "ada", again, store.PushResult{At: at, Source: "pushover", Reason: store.PushRetry, Status: 500, Error: "pushover answered 500", Tries: 1, RetryAt: &retry}); err != nil {
		t.Fatal(err)
	}
	line = when + " through pushover, failed (pushover answered 500, 1 of the inbox in one message); next try 12:00:30"
	if out, err := run(t, jo.userCmd(), "show", "ada"); err != nil || !strings.Contains(out, "  last told: "+line+"\n") || !strings.Contains(notes(), line) {
		t.Fatalf("show while it is tried again: %q %v, doctor: %q", out, err, notes())
	}
	if err := st.PushTried(ctx, "ada", again, store.PushResult{At: at, Source: "pushover", Status: 200, Tries: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.PushTried(ctx, "ada", seq(), store.PushResult{At: at, Source: "pushover", Status: 200, Tries: 1}); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, jo.userCmd(), "show", "ada"); err != nil || !strings.Contains(out, "  last told: "+when+" through pushover, taken (status 200, 1 of the inbox in one message)\n") || notes() != "" {
		t.Fatalf("show after a message was taken: %q %v, doctor: %q", out, err, notes())
	}
}

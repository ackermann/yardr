package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// push is a source that tells a person as Pushover does: the yard's token
// and the person's key in the body, with short bounds so a test can pass
// them.
const push = `name: push
base: BASE
credential: { env: PUSH_TOKEN }
calls:
  message:
    kind: update
    params:
      user:  { type: string, pattern: "^u-[a-z0-9]{6}$" }
      title: { type: string, max: 20 }
      text:  { type: string, max: 40 }
      link:  { type: string, max: 30, optional: true }
    request:
      method: POST
      path: /messages.json
      body: { token: "{credential}", user: "{user}", title: "{title}", message: "{text}", url: "{link}" }
notify:
  call: message
  address: { param: user, env: PUSH_USER }
  title: title
  text: text
  link: link
`

const (
	token   = "tok-41c9-of-the-yard"
	address = "u-9f2k4x"
)

func envFile(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "x.env")
	if err := os.WriteFile(f, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, mode); err != nil { // past the umask
		t.Fatal(err)
	}
	return f
}

// standIn is a service that takes a message and keeps the bodies it got.
type standIn struct {
	*httptest.Server
	status int
	got    []map[string]any
}

func newStandIn(t *testing.T) *standIn {
	s := &standIn{status: 200}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("the body is no JSON: %s", raw)
		}
		s.got = append(s.got, body)
		w.WriteHeader(s.status)
		io.WriteString(w, `{"status":1}`)
	}))
	t.Cleanup(s.Close)
	return s
}

// yard is a store with the source push at the stand-in and the user ada,
// who names it.
func yard(t *testing.T, srv *standIn) store.API {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "push.yaml", strings.Replace(push, "BASE", srv.URL, 1), "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "push", store.SourceEnvFile, envFile(t, "PUSH_TOKEN="+token+"\n", 0o600), "ada"); err != nil {
		t.Fatal(err)
	}
	set := map[string]string{store.UserNotify: "push", store.UserEnvFile: envFile(t, "PUSH_USER="+address+"\n", 0o600)}
	if _, err := st.SetUser(ctx, "ada", set, "ada"); err != nil {
		t.Fatal(err)
	}
	return st
}

// Send makes the call the source's notify key names, with the user's
// address from their env file and the source's credential from its own. A
// title or a text over its parameter's bound is cut to it; a link that is
// too long, or that there is none of, is left out.
func TestSendThroughADeclaredSource(t *testing.T) {
	srv := newStandIn(t)
	st := yard(t, srv)
	ctx := context.Background()
	sent, err := Send(ctx, st, "ada", Message{Title: "mail from yardmaster", Text: "the gate is green", Link: "https://x.example.com/b/1"})
	if err != nil || !sent.OK() || sent.Status != 200 || sent.User != "ada" || sent.Source != "push" {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	want := map[string]any{"token": token, "user": address, "title": "mail from yardmaster", "message": "the gate is green", "url": "https://x.example.com/b/1"}
	if got := fmt.Sprint(srv.got[0]); got != fmt.Sprint(want) {
		t.Fatalf("the stand-in got %v, want %v", got, want)
	}

	long := strings.Repeat("ä", 60)
	if _, err = Send(ctx, st, "ada", Message{Title: long, Text: long, Link: "https://x.example.com/" + long}); err != nil {
		t.Fatal(err)
	}
	got := srv.got[1]
	if got["title"] != strings.Repeat("ä", 19)+"…" || got["message"] != strings.Repeat("ä", 39)+"…" {
		t.Fatalf("a title and a text over their bounds: %v", got)
	}
	if _, has := got["url"]; has {
		t.Fatalf("a link over its bound was sent, cut or whole: %v", got)
	}
	if _, err = Send(ctx, st, "ada", Message{Text: "no title, no link"}); err != nil {
		t.Fatal(err)
	}
	if got := srv.got[2]; got["title"] != "" || got["message"] != "no title, no link" || len(got) != 4 {
		t.Fatalf("a message with a text alone: %v", got)
	}

	// An answer that is no success is the caller's to weigh.
	srv.status = 500
	if sent, err = Send(ctx, st, "ada", Message{Text: "x"}); err != nil || sent.OK() || sent.Status != 500 {
		t.Fatalf("a 500: %+v, %v", sent, err)
	}
}

// What stands in the way of a send before anything is sent is a NotSetUp,
// which says what to do and has neither the address nor the credential in
// it; so does an address the call refuses.
func TestSendNotSetUp(t *testing.T) {
	srv := newStandIn(t)
	st := yard(t, srv)
	ctx := context.Background()
	u, _ := st.GetUser(ctx, "ada")
	src, _ := st.GetSource(ctx, "push")
	userEnv, sourceEnv := u.EnvFile, src.EnvFile
	set := func(k, v string) {
		t.Helper()
		if _, err := st.SetUser(ctx, "ada", map[string]string{k: v}, "ada"); err != nil {
			t.Fatal(err)
		}
	}
	var said []string
	refused := func(what, want string) {
		t.Helper()
		_, err := Send(ctx, st, "ada", Message{Text: "x"})
		var not *NotSetUp
		if !errors.As(err, &not) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want a NotSetUp with %q", what, err, want)
		}
		said = append(said, err.Error())
	}

	set(store.UserNotify, "")
	refused("no source named", "user ada names no source that tells them: yardr user set ada notify=<source>")
	set(store.UserNotify, "push")

	set(store.UserEnvFile, "")
	refused("no env file", "user ada has no env_file: yardr user set ada env_file=<a file of mode 0600 that sets PUSH_USER>")
	set(store.UserEnvFile, envFile(t, "PUSH_USER="+address+"\n", 0o644))
	refused("an env file open to others", "chmod 600")
	other := envFile(t, "OTHER="+address+"\n", 0o600)
	set(store.UserEnvFile, other)
	refused("an env file without the name", "user ada: env_file "+other+" does not set PUSH_USER")
	set(store.UserEnvFile, userEnv)

	if _, err := st.SetSource(ctx, "push", store.SourceEnvFile, "", "ada"); err != nil {
		t.Fatal(err)
	}
	refused("a source without its credential", "source push: PUSH_TOKEN is not set up")
	if _, err := st.SetSource(ctx, "push", store.SourceEnvFile, sourceEnv, "ada"); err != nil {
		t.Fatal(err)
	}

	// The source is replaced by a file without the key, then removed: the
	// user's record still names it.
	bare := strings.Replace(push, "BASE", srv.URL, 1)
	bare = bare[:strings.Index(bare, "notify:")]
	if _, err := st.PutSource(ctx, "push.yaml", bare, "ada"); err != nil {
		t.Fatal(err)
	}
	refused("a source that lost its notify key", "the source push has no notify key")
	if err := st.DeleteSource(ctx, "push", "ada"); err != nil {
		t.Fatal(err)
	}
	refused("a source that is gone", "the source push is not there to tell anyone")
	if _, err := Send(ctx, st, "bob", Message{Text: "x"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a user that is not there: %v", err)
	}

	// An address the call's pattern refuses: nothing is sent, and the
	// error does not repeat it.
	if _, err := st.PutSource(ctx, "push.yaml", strings.Replace(push, "BASE", srv.URL, 1), "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "push", store.SourceEnvFile, sourceEnv, "ada"); err != nil {
		t.Fatal(err)
	}
	set(store.UserEnvFile, envFile(t, "PUSH_USER=someone-else-entirely\n", 0o600))
	_, err := Send(ctx, st, "ada", Message{Text: "x"})
	var call *source.CallError
	if !errors.As(err, &call) || call.Reason != source.BadParam {
		t.Fatalf("an address the call refuses: %v", err)
	}
	said = append(said, err.Error())

	if len(srv.got) != 0 {
		t.Fatalf("something was sent: %v", srv.got)
	}
	for _, s := range said {
		if strings.Contains(s, token) || strings.Contains(s, address) || strings.Contains(s, "someone-else-entirely") {
			t.Fatalf("an error has the credential or the address in it: %s", s)
		}
	}
}

// Ready says whether a user could be told, sends nothing, and names the
// variable and the file, never the value.
func TestReady(t *testing.T) {
	srv := newStandIn(t)
	st := yard(t, srv)
	ctx := context.Background()
	u, _ := st.GetUser(ctx, "ada")
	state, err := Ready(ctx, st, u)
	if err != nil || state != "PUSH_USER is set in "+u.EnvFile || len(srv.got) != 0 {
		t.Fatalf("Ready = %q, %v (%d calls)", state, err, len(srv.got))
	}
	if names, err := Names(ctx, st, "push"); err != nil || len(names) != 1 || names[0] != "PUSH_USER" {
		t.Fatalf("Names = %v, %v", names, err)
	}
}

// teller is an adapter that can tell a person, as none yardr has can yet.
type teller struct {
	hooks.Adapter
	env                         map[string]string
	title, text, link, notified string
}

func (*teller) NotifyEnv() []string { return []string{"TELL_ID", "TELL_KEY"} }
func (a *teller) Notify(_ context.Context, env map[string]string, title, text, link string) (int, error) {
	a.env, a.title, a.text, a.link = env, title, text, link
	return 202, nil
}

// A source yardr has in Go tells a person through the same Send: who calls
// it cannot tell which kind it was.
func TestSendThroughAnAdapter(t *testing.T) {
	a := &teller{}
	hooks.Adapters["tell"] = a
	t.Cleanup(func() { delete(hooks.Adapters, "tell") })
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, ok := hooks.NotifierFor("linear"); ok || len(hooks.Notifying()) != 1 || hooks.Notifying()[0] != "tell" {
		t.Fatalf("Notifying = %v: no adapter of yardr's own tells a person yet", hooks.Notifying())
	}
	env := envFile(t, "TELL_ID=ada-there\nTELL_KEY=k-1\nOTHER=x\n", 0o600)
	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "tell", store.UserEnvFile: env}, "ada"); err != nil {
		t.Fatal(err)
	}
	sent, err := Send(ctx, st, "ada", Message{Title: "t", Text: "x", Link: "l"})
	if err != nil || sent.Status != 202 || sent.Source != "tell" {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	if fmt.Sprint(a.env) != fmt.Sprint(map[string]string{"TELL_ID": "ada-there", "TELL_KEY": "k-1"}) || a.title != "t" || a.text != "x" || a.link != "l" {
		t.Fatalf("the adapter got %+v", a)
	}
	u, _ := st.GetUser(ctx, "ada")
	if state, err := Ready(ctx, st, u); err != nil || state != "TELL_ID and TELL_KEY are set in "+env {
		t.Fatalf("Ready = %q, %v", state, err)
	}
	if err := os.WriteFile(env, []byte("TELL_ID=ada-there\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var not *NotSetUp
	if _, err := Send(ctx, st, "ada", Message{Text: "x"}); !errors.As(err, &not) || !strings.Contains(err.Error(), "does not set TELL_KEY") {
		t.Fatalf("an env file without one of the names: %v", err)
	}
}

// Telling a person is an update call: a source whose file names a second
// credential for its updates tells with that one.
func TestSendWithTheCredentialOfAnUpdate(t *testing.T) {
	srv := newStandIn(t)
	st := yard(t, srv)
	ctx := context.Background()
	two := strings.Replace(strings.Replace(push, "BASE", srv.URL, 1), "{ env: PUSH_TOKEN }", "{ env: PUSH_TOKEN, update_env: PUSH_SEND_TOKEN }", 1)
	if _, err := st.PutSource(ctx, "push.yaml", two, "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(ctx, st, "ada", Message{Title: "t", Text: "x"}); err == nil || !strings.Contains(err.Error(), "does not set PUSH_SEND_TOKEN") || len(srv.got) != 0 {
		t.Fatalf("Send without the update's credential = %v, sent %v", err, srv.got)
	}
	env := envFile(t, "PUSH_TOKEN="+token+"\nPUSH_SEND_TOKEN=tok-send-77\n", 0o600)
	if _, err := st.SetSource(ctx, "push", store.SourceEnvFile, env, "ada"); err != nil {
		t.Fatal(err)
	}
	if sent, err := Send(ctx, st, "ada", Message{Title: "t", Text: "x"}); err != nil || !sent.OK() || srv.got[0]["token"] != "tok-send-77" {
		t.Fatalf("Send = %+v, %v; the stand-in got %v", sent, err, srv.got)
	}
}

package notify

import (
	"bytes"
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
	"time"

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
	sent, err := Send(ctx, st, "", "ada", Message{Title: "mail from yardmaster", Text: "the tests are green", Link: "https://x.example.com/b/1"})
	if err != nil || !sent.OK() || sent.Status != 200 || sent.User != "ada" || sent.Source != "push" {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	want := map[string]any{"token": token, "user": address, "title": "mail from yardmaster", "message": "the tests are green", "url": "https://x.example.com/b/1"}
	if got := fmt.Sprint(srv.got[0]); got != fmt.Sprint(want) {
		t.Fatalf("the stand-in got %v, want %v", got, want)
	}

	long := strings.Repeat("ä", 60)
	if _, err = Send(ctx, st, "", "ada", Message{Title: long, Text: long, Link: "https://x.example.com/" + long}); err != nil {
		t.Fatal(err)
	}
	got := srv.got[1]
	if got["title"] != strings.Repeat("ä", 19)+"…" || got["message"] != strings.Repeat("ä", 39)+"…" {
		t.Fatalf("a title and a text over their bounds: %v", got)
	}
	if _, has := got["url"]; has {
		t.Fatalf("a link over its bound was sent, cut or whole: %v", got)
	}
	if _, err = Send(ctx, st, "", "ada", Message{Text: "no title, no link"}); err != nil {
		t.Fatal(err)
	}
	if got := srv.got[2]; got["title"] != "" || got["message"] != "no title, no link" || len(got) != 4 {
		t.Fatalf("a message with a text alone: %v", got)
	}

	// A message with a picture goes without it where the file names no
	// image command: today's unadorned tell.
	if _, err = Send(ctx, st, "", "ada", Message{Title: "t", Text: "x", Image: "/y/blobs/h"}); err != nil {
		t.Fatal(err)
	}
	if got := srv.got[3]; got["title"] != "t" || got["message"] != "x" || len(got) != 4 {
		t.Fatalf("a message with a picture and no image command: %v", got)
	}

	// An answer that is no success is the caller's to weigh.
	srv.status = 500
	if sent, err = Send(ctx, st, "", "ada", Message{Text: "x"}); err != nil || sent.OK() || sent.Status != 500 {
		t.Fatalf("a 500: %+v, %v", sent, err)
	}

	// A source declared at another host than its credential was given for
	// tells nobody: the credential goes to neither host, and the refusal is a
	// NotSetUp that names both and the way out. With the declaration
	// restored it tells again, the credential not given again.
	srv.status = 200
	told := len(srv.got)
	elsewhere := newStandIn(t)
	if _, err = st.PutSource(ctx, "push.yaml", strings.Replace(push, "BASE", elsewhere.URL, 1), "ada"); err != nil {
		t.Fatal(err)
	}
	_, err = Send(ctx, st, "", "ada", Message{Text: "x"})
	var not *NotSetUp
	for _, want := range []string{"its credential was given for " + srv.URL, "the source's calls go to " + elsewhere.URL + " now", "yardr source key push"} {
		if !errors.As(err, &not) || !strings.Contains(err.Error(), want) {
			t.Fatalf("a source declared at another host: err = %v, want a NotSetUp with %q", err, want)
		}
	}
	if len(srv.got) != told || len(elsewhere.got) != 0 || strings.Contains(err.Error(), token) {
		t.Fatalf("a source declared at another host: %d more requests to the host it was given for, %d to the other (%v)", len(srv.got)-told, len(elsewhere.got), err)
	}
	if _, err = st.PutSource(ctx, "push.yaml", strings.Replace(push, "BASE", srv.URL, 1), "ada"); err != nil {
		t.Fatal(err)
	}
	if sent, err = Send(ctx, st, "", "ada", Message{Text: "x"}); err != nil || !sent.OK() || len(srv.got) != told+1 || len(elsewhere.got) != 0 {
		t.Fatalf("the declaration restored: %+v, %v (%d requests to the host it was given for since, %d to the other)", sent, err, len(srv.got)-told, len(elsewhere.got))
	}
	t.Run("image command", testSendImage)
}

// A message with a picture goes through the file's image command, handed
// the address, the words and the picture's path with the source's
// credential, and the command's status is the send's. One without goes
// through the call, and so does one whose picture the command did not
// send: the words are not lost with it.
func testSendImage(t *testing.T) {
	srv := newStandIn(t)
	st := storetest.New(t)
	ctx := context.Background()
	dir, home := t.TempDir(), t.TempDir()
	script := filepath.Join(dir, "show")
	show := "#!/bin/sh\n{ cat; echo; } >> " + dir + "/image.stdin\n" +
		"printf '%s' \"$PUSH_TOKEN\" > " + dir + "/image.token\n" +
		"printf '%s' '{\"status\":215}'\n"
	if err := os.WriteFile(script, []byte(show), 0o755); err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(push, "BASE", srv.URL, 1)
	text = strings.Replace(text, "  link: link\n", "  link: link\n  image: ["+script+"]\n", 1)
	if _, err := st.AddUser(ctx, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "push.yaml", text, "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "push", store.SourceEnvFile, envFile(t, "PUSH_TOKEN="+token+"\n", 0o600), "ada"); err != nil {
		t.Fatal(err)
	}
	set := map[string]string{store.UserNotify: "push", store.UserEnvFile: envFile(t, "PUSH_USER="+address+"\n", 0o600)}
	if _, err := st.SetUser(ctx, "ada", set, "ada"); err != nil {
		t.Fatal(err)
	}

	sent, err := Send(ctx, st, home, "ada", Message{Title: "t", Text: "x", Link: "l", Image: "/y/blobs/h"})
	if err != nil || !sent.OK() || sent.Status != 215 {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "image.stdin"))
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	if err := json.Unmarshal(bytes.TrimRight(raw, "\n"), &in); err != nil {
		t.Fatalf("the command was handed %q: %v", raw, err)
	}
	if fmt.Sprint(in) != fmt.Sprint(map[string]any{"address": address, "title": "t", "text": "x", "image": "/y/blobs/h"}) {
		t.Fatalf("the command was handed %v", in)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "image.token")); string(got) != token {
		t.Fatalf("the command's credential = %q", got)
	}
	if len(srv.got) != 0 {
		t.Fatalf("the call was made too: %v", srv.got)
	}

	// Without a picture the call tells, and the command runs no more.
	if _, err := Send(ctx, st, home, "ada", Message{Title: "t", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(srv.got) != 1 || srv.got[0]["message"] != "x" {
		t.Fatalf("the call got %v", srv.got)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "image.stdin"))
	if n := strings.Count(string(raw), "\n"); n != 1 {
		t.Fatalf("the command ran %d times", n)
	}

	// A command that fails costs the words nothing: the call tells without
	// the picture, and the Sent says why. So does one that answers no
	// status, and one that answers a failure.
	for _, c := range []struct{ name, script, why string }{
		{"a failing command", "#!/bin/sh\necho 'the service is down' >&2\nexit 1\n", "the service is down"},
		{"an answer with no status", "#!/bin/sh\nprintf '%s' '{}'\n", "no {status}"},
		{"a refusal", "#!/bin/sh\nprintf '%s' '{\"status\":400}'\n", "answered 400"},
	} {
		if err := os.WriteFile(script, []byte(c.script), 0o755); err != nil {
			t.Fatal(err)
		}
		told := len(srv.got)
		sent, err := Send(ctx, st, home, "ada", Message{Title: "t", Text: "x", Image: "/y/blobs/h"})
		if err != nil || !sent.OK() || sent.Source != "push" || !strings.Contains(sent.Unpictured, c.why) {
			t.Fatalf("%s: %+v, %v", c.name, sent, err)
		}
		if len(srv.got) != told+1 || srv.got[told]["title"] != "t" || srv.got[told]["message"] != "x" {
			t.Fatalf("%s: the call got %v", c.name, srv.got[told:])
		}
	}
	// A picture that is slow rather than refused costs the words nothing
	// either. The command is still running when the send's own time is
	// over, and the call tells them under a time of their own, once, with
	// the Sent saying why the picture did not go: the send's time ended it,
	// not the command's own.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	told := len(srv.got)
	slow, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	sent, err = Send(slow, st, home, "ada", Message{Title: "t", Text: "x", Image: "/y/blobs/h"})
	cancel()
	if err != nil || !sent.OK() || !strings.Contains(sent.Unpictured, "no answer before the caller's deadline") {
		t.Fatalf("a picture that outlives the send: %+v, %v", sent, err)
	}
	if len(srv.got) != told+1 || srv.got[told]["title"] != "t" || srv.got[told]["message"] != "x" {
		t.Fatalf("a picture that outlives the send: the call got %v", srv.got[told:])
	}
	// A picture that went says nothing of the kind, and a send that fails
	// without it fails with the call's words.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '{\"status\":200}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if sent, err := Send(ctx, st, home, "ada", Message{Text: "x", Image: "/y/blobs/h"}); err != nil || sent.Unpictured != "" {
		t.Fatalf("a picture that went: %+v, %v", sent, err)
	}
	srv.status = 500
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if sent, err := Send(ctx, st, home, "ada", Message{Text: "x", Image: "/y/blobs/h"}); err != nil || sent.OK() || sent.Status != 500 {
		t.Fatalf("a picture refused and a call that fails: %+v, %v", sent, err)
	}

	// A source declared at another host than its credential was given for
	// runs no image command with it, and sends no words by the call either:
	// the picture's command is handed the credential, and it would send it on.
	// The declaration restored, the command runs again.
	srv.status = 200
	if err := os.WriteFile(script, []byte(show), 0o755); err != nil {
		t.Fatal(err)
	}
	ran := func() int {
		t.Helper()
		raw, _ := os.ReadFile(filepath.Join(dir, "image.stdin"))
		return strings.Count(string(raw), "\n")
	}
	before, told := ran(), len(srv.got)
	elsewhere := newStandIn(t)
	if _, err := st.PutSource(ctx, "push.yaml", strings.Replace(text, srv.URL, elsewhere.URL, 1), "ada"); err != nil {
		t.Fatal(err)
	}
	_, err = Send(ctx, st, home, "ada", Message{Title: "t", Text: "x", Image: "/y/blobs/h"})
	var not *NotSetUp
	if !errors.As(err, &not) || !strings.Contains(err.Error(), "its credential was given for "+srv.URL) {
		t.Fatalf("a picture through a source declared at another host: err = %v, want a NotSetUp", err)
	}
	if ran() != before || len(srv.got) != told || len(elsewhere.got) != 0 {
		t.Fatalf("a picture through a source declared at another host: the command ran %d more times, %d requests to the host it was given for, %d to the other", ran()-before, len(srv.got)-told, len(elsewhere.got))
	}
	if _, err := st.PutSource(ctx, "push.yaml", text, "ada"); err != nil {
		t.Fatal(err)
	}
	if sent, err := Send(ctx, st, home, "ada", Message{Title: "t", Text: "x", Image: "/y/blobs/h"}); err != nil || sent.Status != 215 || ran() != before+1 {
		t.Fatalf("the declaration restored: %+v, %v (the command ran %d times since)", sent, err, ran()-before)
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
		_, err := Send(ctx, st, "", "ada", Message{Text: "x"})
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
	refused("no env file", "user ada has no env_file: yardr user key ada push gives PUSH_USER")
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
	if _, err := Send(ctx, st, "", "bob", Message{Text: "x"}); !errors.Is(err, store.ErrNotFound) {
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
	_, err := Send(ctx, st, "", "ada", Message{Text: "x"})
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
	env               map[string]string
	title, text, link string
}

func (*teller) NotifyEnv() []string { return []string{"TELL_ID", "TELL_KEY"} }
func (a *teller) Notify(_ context.Context, env map[string]string, title, text, link string) (int, error) {
	a.env, a.title, a.text, a.link = env, title, text, link
	return 202, nil
}

// imager is a teller whose source takes a picture with the message, or
// fails to, as fail says. A picture it is asked to take runs onImage first.
type imager struct {
	teller
	image   string
	fail    error
	onImage func()
}

func (a *imager) NotifyImage(ctx context.Context, env map[string]string, title, text, link, image string) (int, error) {
	a.image = image
	if a.onImage != nil {
		a.onImage()
	}
	if a.fail != nil {
		return 0, a.fail
	}
	return a.teller.Notify(ctx, env, title, text, link)
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
	sent, err := Send(ctx, st, "", "ada", Message{Title: "t", Text: "x", Link: "l"})
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
	if _, err := Send(ctx, st, "", "ada", Message{Text: "x"}); !errors.As(err, &not) || !strings.Contains(err.Error(), "does not set TELL_KEY") {
		t.Fatalf("an env file without one of the names: %v", err)
	}
	t.Run("image interface", testSendImageThroughAnAdapter)
}

// A source in Go that takes a picture with the message gets it; one that
// takes none is told without, as before.
func testSendImageThroughAnAdapter(t *testing.T) {
	a := &teller{}
	hooks.Adapters["tell"] = a
	t.Cleanup(func() { delete(hooks.Adapters, "tell") })
	b := &imager{}
	hooks.Adapters["show"] = b
	t.Cleanup(func() { delete(hooks.Adapters, "show") })
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	env := envFile(t, "TELL_ID=ada-there\nTELL_KEY=k-1\n", 0o600)
	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "tell", store.UserEnvFile: env}, "ada"); err != nil {
		t.Fatal(err)
	}
	if sent, err := Send(ctx, st, "", "ada", Message{Title: "t", Text: "x", Image: "/y/blobs/h"}); err != nil || sent.Status != 202 {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	if a.title != "t" || a.text != "x" {
		t.Fatalf("the adapter without pictures got %+v", a)
	}
	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "show"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if sent, err := Send(ctx, st, "", "ada", Message{Title: "t", Text: "x", Link: "l", Image: "/y/blobs/h"}); err != nil || sent.Status != 202 {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	if b.title != "t" || b.text != "x" || b.link != "l" || b.image != "/y/blobs/h" {
		t.Fatalf("the adapter with pictures got %+v, image %q", b.teller, b.image)
	}
	b.image = ""
	if _, err := Send(ctx, st, "", "ada", Message{Title: "t", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if b.image != "" || b.title != "t" {
		t.Fatalf("a message without a picture: %+v, image %q", b.teller, b.image)
	}

	// A picture the source fails to take costs the words nothing: the
	// plain call tells, and the Sent says why.
	b.teller, b.fail = teller{}, errors.New("the photo is too tall")
	sent, err := Send(ctx, st, "", "ada", Message{Title: "t", Text: "x", Link: "l", Image: "/y/blobs/h"})
	if err != nil || sent.Status != 202 || !strings.Contains(sent.Unpictured, "the photo is too tall") {
		t.Fatalf("Send = %+v, %v", sent, err)
	}
	if b.image != "/y/blobs/h" || b.title != "t" || b.text != "x" || b.link != "l" {
		t.Fatalf("the adapter whose picture failed got %+v, image %q", b.teller, b.image)
	}

	// A caller that cancels has given up on the send: the picture's failure
	// ends it, and the words are not told on the way out. A daemon that is
	// stopping could not record them, and would tell them again after the
	// restart.
	cctx, cancel := context.WithCancel(ctx)
	b.teller, b.onImage = teller{}, cancel
	if _, err := Send(cctx, st, "", "ada", Message{Title: "t", Text: "x", Image: "/y/blobs/h"}); !errors.Is(err, context.Canceled) || b.text != "" {
		t.Fatalf("a send cancelled at its picture: %v; the adapter got %+v", err, b.teller)
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
	if _, err := Send(ctx, st, "", "ada", Message{Title: "t", Text: "x"}); err == nil || !strings.Contains(err.Error(), "does not set PUSH_SEND_TOKEN") || len(srv.got) != 0 {
		t.Fatalf("Send without the update's credential = %v, sent %v", err, srv.got)
	}
	env := envFile(t, "PUSH_TOKEN="+token+"\nPUSH_SEND_TOKEN=tok-send-77\n", 0o600)
	if _, err := st.SetSource(ctx, "push", store.SourceEnvFile, env, "ada"); err != nil {
		t.Fatal(err)
	}
	if sent, err := Send(ctx, st, "", "ada", Message{Title: "t", Text: "x"}); err != nil || !sent.OK() || srv.got[0]["token"] != "tok-send-77" {
		t.Fatalf("Send = %+v, %v; the stand-in got %v", sent, err, srv.got)
	}
}

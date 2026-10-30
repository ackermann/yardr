package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// webYard is a yard with the user Jo and a home of its own, which is all
// the tests of serve need of one: a store, and where its views record
// themselves. It stubs nothing and sets no variable (YARDR_WEB_AS is blank
// in the process already, TestMain), so the tests that use it run beside
// the rest.
func webYard(t *testing.T) (store.API, *config.Config) {
	t.Helper()
	st := storetest.New(t)
	if _, err := st.AddUser(context.Background(), "Jo", "Jo"); err != nil {
		t.Fatal(err)
	}
	return st, &config.Config{Home: newHomeDir(t)}
}

// serveWeb runs yardr web serve on loopback with args, in the background,
// until stop is called, with its stderr a terminal where terminal says so.
// It returns once the view serves: it has recorded itself and answers, so
// everything it prints at start is printed, and the view that its record
// names and what it printed to its stderr come with it.
func serveWeb(t *testing.T, st store.API, cfg *config.Config, terminal bool, args ...string) (v webView, stderr *lockedBuffer, stop func()) {
	t.Helper()
	a := appOn(st)
	a.cfg, a.actor = cfg, "me"
	a.isTerm = func(io.Writer) bool { return terminal }
	cmd := a.webCmd()
	stderr = &lockedBuffer{}
	cmd.SetErr(stderr)
	cmd.SetArgs(append([]string{"serve", "--addr", "127.0.0.1:0", "--pages-addr", "127.0.0.1:0"}, args...))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	stop = func() {
		t.Helper()
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("web serve did not stop")
		}
	}
	t.Cleanup(cancel)
	var views []webView
	for deadline := time.Now().Add(10 * time.Second); len(views) == 0; time.Sleep(10 * time.Millisecond) {
		select {
		case err := <-done:
			t.Fatalf("web serve %v ended: %v", args, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("web serve %v recorded nothing", args)
		}
		views = webViews(cfg)
	}
	v = views[0]
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if r, err := http.Get("http://" + v.Addr + "/health"); err == nil {
			r.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("web serve %v does not answer", args)
		}
	}
	return v, stderr, stop
}

// The refusal of --unsafe for a view that acts names the user and where the
// name came from, whichever of the flag, YARDR_WEB_AS and the web_as
// setting it was, says why, and says what to do instead (each of them its
// own way to stop acting).
func TestWebUnsafeAsRefusedNamesItsSource(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		from, whose, unset string
	}{
		{"", "--as Jo", "start without --as"},
		{store.FromEnvironment, "YARDR_WEB_AS Jo", "unset YARDR_WEB_AS"},
		{store.FromSet, "web_as Jo", "unset web_as"},
	} {
		err := webUnsafeAsRefused("Jo", c.from)
		for _, want := range []string{"--unsafe refused", "acts as Jo", "(" + c.whose + ")", "token and a cookie", "plain HTTP", "Drop --unsafe", c.unset, "JWT mode"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("from %q: the refusal lacks %q: %v", c.from, want, err)
			}
		}
	}
}

// --unsafe with a view that acts is refused outside JWT mode, whether --as
// or the web_as setting names the user, before anything listens. In JWT
// mode the acting user is refused first, for its own reason.
func TestWebServeUnsafeAsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, cfg := webYard(t)
	serve := func(st store.API, cfg *config.Config, args ...string) error {
		t.Helper()
		a := appOn(st)
		a.cfg = cfg
		_, err := run(t, a.webCmd(), append([]string{"serve", "--unsafe", "--addr", "127.0.0.1:0"}, args...)...)
		return err
	}
	refused := func(err error, whose string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s with --unsafe: served", whose)
		}
		for _, want := range []string{"--unsafe refused", "acts as Jo", "(" + whose + ")", "plain HTTP"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s with --unsafe: the refusal lacks %q: %v", whose, want, err)
			}
		}
		if views := webViews(cfg); len(views) != 0 {
			t.Errorf("%s with --unsafe: a view runs: %+v", whose, views)
		}
	}
	refused(serve(st, cfg, "--as", "Jo"), "--as Jo")
	if err := st.SetSetting(ctx, store.WebAsSetting, "Jo", "Jo"); err != nil {
		t.Fatal(err)
	}
	refused(serve(st, cfg), "web_as Jo")

	// In JWT mode --as is refused for its own reason, before --unsafe is
	// looked at.
	jst, jcfg := webYard(t)
	for k, v := range map[string]string{
		store.WebJWTIssuerSetting:   "https://team.example.net",
		store.WebJWTAudienceSetting: "aud-tag",
		store.WebJWTJWKSSetting:     "https://team.example.net/certs",
		store.WebJWTUsersSetting:    "jo@example.net=Jo",
	} {
		if err := jst.SetSetting(ctx, k, v, "Jo"); err != nil {
			t.Fatal(err)
		}
	}
	err := serve(jst, jcfg, "--as", "Jo")
	if err == nil || !strings.Contains(err.Error(), "JWT mode") || !strings.Contains(err.Error(), "takes no acting user") || strings.Contains(err.Error(), "--unsafe refused") {
		t.Errorf("--as and --unsafe in JWT mode: %v, want the refusal of the acting user", err)
	}
}

// A view that acts has a token of its own: a random 256 bits that its
// record, a file of mode 0600, holds and that its stderr prints once in a
// URL where a terminal reads it, and nothing else shows (status prints the
// views, and none of them has a field for it). The token is the one the view
// takes: its URL buys the cookie, and a write without the cookie is refused.
// A view that does not act has none and prints none. The record goes with
// the view.
func TestWebServeActsBehindItsToken(t *testing.T) {
	t.Parallel()
	st, cfg := webYard(t)

	v, stderr, stop := serveWeb(t, st, cfg, true, "--as", "Jo")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(v.token) {
		t.Fatalf("token %q, want 256 bits in hex", v.token)
	}
	records, _ := os.ReadDir(cfg.WebViews())
	if len(records) != 1 {
		t.Fatalf("%d records, want the view's", len(records))
	}
	if info, err := records[0].Info(); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the record %s has mode %v (%v), want 0600: it holds the token", records[0].Name(), info.Mode().Perm(), err)
	}
	if info, err := os.Stat(cfg.WebViews()); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the records' directory has mode %v (%v), want 0700", info.Mode().Perm(), err)
	}
	wantURL := "http://" + v.Addr + "/?token=" + v.token
	if out := stderr.String(); strings.Count(out, wantURL) != 1 || !strings.Contains(out, "To act as Jo") {
		t.Errorf("stderr:\n%s\nwant the URL %s once, for Jo", out, wantURL)
	}
	// What status and doctor print of a view is its struct as JSON, and
	// that holds no token.
	shown, err := json.Marshal(webViews(cfg))
	if err != nil || strings.Contains(string(shown), v.token) || !strings.Contains(string(shown), `"as":"Jo"`) {
		t.Errorf("the views as status prints them (%v): %s, want who each acts as and no token", err, shown)
	}

	// The view takes this token, not another: a write with no cookie is
	// refused, and the URL buys the cookie.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post, err := http.NewRequest(http.MethodPost, "http://"+v.Addr+"/talk", strings.NewReader(url.Values{"text": {"hello"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", "http://"+v.Addr)
	if r, err := client.Do(post); err != nil || r.StatusCode != http.StatusForbidden {
		t.Errorf("a write with a forged origin and no cookie: %v, %v, want 403", r, err)
	}
	r, err := client.Get(wantURL)
	if err != nil || r.StatusCode != http.StatusSeeOther || len(r.Cookies()) != 1 || !r.Cookies()[0].HttpOnly {
		t.Errorf("the printed URL: %v, %v, want 303 with a cookie", r, err)
	}
	stop()
	if views := webViews(cfg); len(views) != 0 {
		t.Errorf("the view ended and its record stands: %+v", views)
	}
	if left, _ := os.ReadDir(cfg.WebViews()); len(left) != 0 {
		t.Errorf("the view ended and left %d files with its token", len(left))
	}

	// Another serve is another view with a token of its own.
	again, _, stopAgain := serveWeb(t, st, cfg, true, "--as", "Jo")
	stopAgain()
	if again.token == "" || again.token == v.token {
		t.Errorf("a second serve has token %q after the first's %q, want one of its own", again.token, v.token)
	}

	// A view that does not act has none, and prints none.
	plain, stderr, stopPlain := serveWeb(t, st, cfg, true, "--as=")
	defer stopPlain()
	if plain.token != "" || strings.Contains(stderr.String(), "token") {
		t.Errorf("a view that does not act: token %q, stderr %q", plain.token, stderr.String())
	}
}

// Where no terminal reads what the view prints (a log, a pipe: what yardr
// web start and the login service leave it), the token is not printed: a log
// is read by more than the operator, and the record has the token for yardr
// web open.
func TestWebServeKeepsTheTokenOutOfALog(t *testing.T) {
	t.Parallel()
	st, cfg := webYard(t)
	v, stderr, stop := serveWeb(t, st, cfg, false, "--as", "Jo")
	defer stop()
	if v.token == "" || strings.Contains(stderr.String(), v.token) || strings.Contains(stderr.String(), "?token=") {
		t.Errorf("token %q, stderr %q: want a token in the record and none printed", v.token, stderr.String())
	}
}

// An acting view must have its private record: without it, a detached view
// has no way to open the token URL, and the token has nowhere mode 0600 can
// protect it. Read-only views may still serve without a record.
func TestWebServeActsRequiresItsTokenRecord(t *testing.T) {
	t.Parallel()
	st, cfg := webYard(t)
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.WebViews(), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := appOn(st)
	a.cfg = cfg
	_, err := run(t, a.webCmd(), "serve", "--addr", "127.0.0.1:0", "--pages-addr", "127.0.0.1:0", "--as", "Jo")
	if err == nil || !strings.Contains(err.Error(), "could not record the acting web view's token") {
		t.Fatalf("serve with no place for the token: %v, want it refused", err)
	}
	if views := webViews(cfg); len(views) != 0 {
		t.Errorf("serve with no token record left a view listed: %+v", views)
	}
}

// yardr web open opens the URL of the view that runs: with its token where
// the view acts, plain where it does not. It does not print the token where
// a browser took it, prints it where none did, and names the views when it
// cannot tell which to open.
func TestWebOpen(t *testing.T) {
	t.Parallel()
	home := newHomeDir(t)
	cfg := &config.Config{Home: home}
	var opened []string
	var openErr error
	command := func(json bool) *app {
		return &app{cfg: cfg, json: json, browser: func(url string) error {
			opened = append(opened, url)
			return openErr
		}}
	}
	open := func(args ...string) (string, error) {
		t.Helper()
		opened = nil
		return run(t, command(false).webCmd(), append([]string{"open"}, args...)...)
	}
	// view starts a view that runs, in the sense of its record, for the test.
	view := func(addr, as, token string) {
		t.Helper()
		clear, err := announceWeb(cfg, addr, "", false, as, "", token)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(clear)
	}

	if _, err := open(); err == nil || !strings.Contains(err.Error(), "no web view runs") || !strings.Contains(err.Error(), "web start") {
		t.Errorf("with no view: %v, want to be told none runs and how to start one", err)
	}
	if len(opened) != 0 {
		t.Errorf("a browser was opened for no view: %v", opened)
	}

	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	view("127.0.0.1:9000", "Jo", token)
	out, err := open()
	if err != nil || len(opened) != 1 || opened[0] != "http://127.0.0.1:9000/?token="+token {
		t.Fatalf("opened %v (%v), want the view's URL with its token", opened, err)
	}
	if strings.Contains(out, token) || !strings.Contains(out, "Opened http://127.0.0.1:9000/") || !strings.Contains(out, "acts as Jo") {
		t.Errorf("output %q, want the plain address and who the browser acts as, never the token", out)
	}

	// No browser: the URL is what there is to open by hand.
	openErr = errors.New("no display")
	out, err = open()
	if err != nil || !strings.Contains(out, "No browser opened (no display)") || !strings.Contains(out, "http://127.0.0.1:9000/?token="+token) {
		t.Errorf("without a browser: %q (%v), want the reason and the URL with the token", out, err)
	}
	openErr = nil

	// The JSON says what was opened.
	opened = nil
	jsonOut, err := run(t, command(true).webCmd(), "open")
	var got struct {
		Address string `json:"address"`
		URL     string `json:"url"`
		ActsAs  string `json:"acts_as"`
		Opened  bool   `json:"opened"`
	}
	if err != nil || json.Unmarshal([]byte(jsonOut), &got) != nil || got.URL != "http://127.0.0.1:9000/?token="+token || !got.Opened ||
		got.Address != "127.0.0.1:9000" || got.ActsAs != "Jo" {
		t.Errorf("--json: %q (%v), want the address, the URL with the token, who it acts as and opened", jsonOut, err)
	}

	// Two views: it asks which, and --addr says. One that does not act is
	// opened at its plain address.
	view("127.0.0.1:9001", "", "")
	if _, err := open(); err == nil || !strings.Contains(err.Error(), "--addr") || !strings.Contains(err.Error(), "127.0.0.1:9000") || !strings.Contains(err.Error(), "127.0.0.1:9001") || len(opened) != 0 {
		t.Errorf("with two views: %v, opened %v, want to be asked for --addr and told the addresses", err, opened)
	}
	if out, err := open("--addr", "127.0.0.1:9001"); err != nil || len(opened) != 1 || opened[0] != "http://127.0.0.1:9001/" || strings.Contains(out, "acts as") {
		t.Errorf("--addr 127.0.0.1:9001: opened %v, %q (%v), want the plain address", opened, out, err)
	}
	if _, err := open("--addr", "127.0.0.1:9999"); err == nil || !strings.Contains(err.Error(), "no web view runs at 127.0.0.1:9999") || len(opened) != 0 {
		t.Errorf("--addr 127.0.0.1:9999: %v, opened %v, want to be told no view runs there", err, opened)
	}
}

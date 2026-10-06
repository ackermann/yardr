package hooks_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/standin"
)

// appYard is a stand-in for GitHub that knows the App yardr (id 4242),
// installed on acme/webapp and acme/site, with the App's private key in a
// file as a resident's env_file would name it, and a clock the test moves
// for both sides.
type appYard struct {
	src     *standin.Server
	keyFile string
	now     time.Time
	mu      sync.Mutex
	bearers []string // every bearer token sent: JWTs and installation tokens
}

func newAppYard(t *testing.T) *appYard {
	t.Helper()
	y := &appYard{keyFile: filepath.Join(t.TempDir(), "yardr.pem"), now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	app, err := standin.NewApp("4242", "yardr", y.keyFile, "acme/webapp", "acme/site")
	if err != nil {
		t.Fatal(err)
	}
	app.Now = func() time.Time { return y.now }
	y.src = &standin.Server{GitHubToken: "gh-yardmaster", GitHubLogin: "Yardmaster", App: app}
	y.src.Add(standin.Issue{Source: "github", Key: "acme/webapp#7", Title: "Retries", Status: "open"})
	y.src.Add(standin.Issue{Source: "github", Key: "acme/site#2", Title: "Docs", Status: "open"})
	y.src.Add(standin.Issue{Source: "github", Key: "other/repo#1", Title: "Elsewhere", Status: "open"})
	inner := y.src.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		y.mu.Lock()
		y.bearers = append(y.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		y.mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	old := hooks.GitHubAPI
	hooks.GitHubAPI = srv.URL
	t.Cleanup(func() { hooks.GitHubAPI = old })
	t.Cleanup(hooks.SetGitHubClock(func() time.Time { return y.now }))
	return y
}

// cred is the credential an env_file with the App's two lines gives.
func (y *appYard) cred(t *testing.T) string {
	t.Helper()
	return appCred(t, "4242", y.keyFile)
}

func appCred(t *testing.T, id, keyFile string) string {
	t.Helper()
	file := map[string]string{"GITHUB_APP_ID": id, "GITHUB_APP_PRIVATE_KEY_FILE": keyFile}
	c, err := hooks.GitHub{}.CredentialFrom(func(name string) string { return file[name] })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// secrets are what must never be shown: every token and JWT that was sent,
// and the body of the private key.
func (y *appYard) secrets(t *testing.T) []string {
	t.Helper()
	y.mu.Lock()
	out := append(y.src.Tokens(), y.bearers...)
	y.mu.Unlock()
	pem, err := os.ReadFile(y.keyFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(pem), "\n") {
		if len(line) > 20 && !strings.HasPrefix(line, "-----") {
			out = append(out, line)
		}
	}
	return out
}

// shows fails when text holds one of the yard's secrets.
func (y *appYard) shows(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range y.secrets(t) {
		if s != "" && strings.Contains(text, s) {
			t.Errorf("%s shows a secret: %s", what, text)
			return
		}
	}
}

func githubEnv(thread string) hooks.Envelope {
	return hooks.Envelope{Source: "github", Type: "issue_comment", Thread: hooks.Thread{ID: thread}, Message: "10"}
}

// A reply with the App's pair is made in three calls: as the App, signed
// with its key (the stand-in verifies the JWT with the public half), it
// asks for the installation of the thread's repository and for a token of
// it, and posts with that token, so the comment is the bot's. The token is
// kept for the next call and asked for again shortly before it expires.
func TestGitHubAppReply(t *testing.T) {
	y := newAppYard(t)
	ctx := context.Background()
	cred := y.cred(t)
	m, err := hooks.GitHub{}.Reply(ctx, githubEnv("acme/webapp#7"), "", "On it.", "", cred)
	if err != nil {
		t.Fatal(err)
	}
	if m.Author != (hooks.Party{ID: "yardr[bot]", Name: "yardr[bot]"}) || m.Body != "On it." {
		t.Fatalf("reply %+v", m)
	}
	wantCalls(t, y.src.Calls(), "github app installation acme/webapp", "github app token acme/webapp",
		"github comment acme/webapp#7 by yardr[bot]")

	// Within the token's life nothing is asked of the App again: a thread
	// read, a second reply, and one 54 minutes on all use the token in hand.
	if ms, err := (hooks.GitHub{}).Thread(ctx, githubEnv("acme/webapp#7"), cred); err != nil || len(ms) != 1 {
		t.Fatalf("thread %+v, %v", ms, err)
	}
	y.now = y.now.Add(54 * time.Minute)
	if _, err := (hooks.GitHub{}).Reply(ctx, githubEnv("acme/webapp#7"), "", "Still on it.", "", cred); err != nil {
		t.Fatal(err)
	}
	if got := y.src.Minted(); got != 1 {
		t.Fatalf("tokens asked for within one's life: %d, want 1", got)
	}
	// Five minutes before it expires it is no longer used.
	y.now = y.now.Add(2 * time.Minute)
	if _, err := (hooks.GitHub{}).Reply(ctx, githubEnv("acme/webapp#7"), "", "Done.", "", cred); err != nil {
		t.Fatal(err)
	}
	if got := y.src.Minted(); got != 2 {
		t.Fatalf("tokens asked for after the first ran out: %d, want 2", got)
	}
	calls := y.src.Calls()
	wantCalls(t, calls[len(calls)-3:], "github app installation acme/webapp", "github app token acme/webapp",
		"github comment acme/webapp#7 by yardr[bot]")

	// Another repository of the installation has a token of its own, for
	// that repository alone.
	if _, err := (hooks.GitHub{}).Reply(ctx, githubEnv("acme/site#2"), "", "Here too.", "", cred); err != nil {
		t.Fatal(err)
	}
	calls = y.src.Calls()
	wantCalls(t, calls[len(calls)-3:], "github app installation acme/site", "github app token acme/site",
		"github comment acme/site#2 by yardr[bot]")
	for _, c := range calls {
		y.shows(t, "the stand-in's record", c)
		if strings.Contains(c, "refused") {
			t.Errorf("a call was refused: %v", calls)
		}
	}
}

// A token GitHub stops taking before its time (here: the stand-in's clock
// runs past it) is dropped, so the call after the refused one asks anew.
func TestGitHubAppTokenRefusedIsForgotten(t *testing.T) {
	y := newAppYard(t)
	ctx := context.Background()
	cred := y.cred(t)
	if _, err := (hooks.GitHub{}).Thread(ctx, githubEnv("acme/webapp#7"), cred); err != nil {
		t.Fatal(err)
	}
	theirs := y.now.Add(2 * time.Hour)
	y.src.App.Now = func() time.Time { return theirs }
	_, err := hooks.GitHub{}.Thread(ctx, githubEnv("acme/webapp#7"), cred)
	if err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("an expired token: %v", err)
	}
	y.shows(t, "the error", err.Error())
	y.src.App.Now = func() time.Time { return y.now }
	if _, err := (hooks.GitHub{}).Thread(ctx, githubEnv("acme/webapp#7"), cred); err != nil {
		t.Fatal(err)
	}
	if got := y.src.Minted(); got != 2 {
		t.Fatalf("tokens asked for: %d, want 2", got)
	}
}

// An App that is not installed on the thread's repository is refused in
// words that say where to install it, and nothing is posted.
func TestGitHubAppNotInstalled(t *testing.T) {
	y := newAppYard(t)
	_, err := hooks.GitHub{}.Reply(context.Background(), githubEnv("other/repo#1"), "", "Hello.", "", y.cred(t))
	if err == nil || !strings.Contains(err.Error(), "not installed on other/repo") || !strings.Contains(err.Error(), "install the App there") {
		t.Fatalf("not installed: %v", err)
	}
	y.shows(t, "the error", err.Error())
	wantCalls(t, y.src.Calls(), "github app installation other/repo")
}

// What goes wrong with the key or the App is said by name and path, never
// with what the key file holds or what was sent.
func TestGitHubAppRefusals(t *testing.T) {
	y := newAppYard(t)
	ctx := context.Background()
	env := githubEnv("acme/webapp#7")
	dir := t.TempDir()
	reply := func(cred string) string {
		t.Helper()
		_, err := hooks.GitHub{}.Reply(ctx, env, "", "Hello.", "", cred)
		if err == nil {
			t.Fatal("posted")
		}
		y.shows(t, "the error", err.Error())
		return err.Error()
	}

	// Another App's key: GitHub refuses the JWT.
	otherKey := filepath.Join(dir, "other.pem")
	if _, err := standin.NewApp("4242", "other", otherKey); err != nil {
		t.Fatal(err)
	}
	if got := reply(appCred(t, "4242", otherKey)); !strings.Contains(got, "401") || !strings.Contains(got, "as GitHub App 4242") {
		t.Errorf("another key: %s", got)
	}
	other, _ := os.ReadFile(otherKey)
	// Another App's id with this key.
	if got := reply(appCred(t, "Iv23liClientID", y.keyFile)); !strings.Contains(got, "401") {
		t.Errorf("another id: %s", got)
	}
	wantCalls(t, y.src.Calls(), "github app refused: the signature is not the App's key's", "github app refused: issued by another App")

	// A key file open to others, as an env_file or a hook's secret file is
	// refused; one that is no key; one that is not there.
	open := filepath.Join(dir, "open.pem")
	if err := os.WriteFile(open, other, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := reply(appCred(t, "4242", open)); !strings.Contains(got, "GITHUB_APP_PRIVATE_KEY_FILE") || !strings.Contains(got, "open to others") {
		t.Errorf("open key file: %s", got)
	}
	garbage := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not-a-key-but-a-secret-all-the-same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := reply(appCred(t, "4242", garbage)); !strings.Contains(got, "holds no RSA private key") || strings.Contains(got, "not-a-key") {
		t.Errorf("garbage key file: %s", got)
	}
	if got := reply(appCred(t, "4242", filepath.Join(dir, "none.pem"))); !strings.Contains(got, "no such file") {
		t.Errorf("no key file: %s", got)
	}
	wantCalls(t, y.src.Calls()[2:])

	// The pair is taken whole, with a path that means the same to the
	// daemon and to a pane.
	for _, c := range []struct {
		file map[string]string
		want string
	}{
		{map[string]string{"GITHUB_APP_ID": "4242", "GITHUB_TOKEN": "gh-yardmaster"}, "GITHUB_APP_ID is set without GITHUB_APP_PRIVATE_KEY_FILE"},
		{map[string]string{"GITHUB_APP_PRIVATE_KEY_FILE": y.keyFile}, "GITHUB_APP_PRIVATE_KEY_FILE is set without GITHUB_APP_ID"},
		{map[string]string{"GITHUB_APP_ID": "4242", "GITHUB_APP_PRIVATE_KEY_FILE": "keys/yardr.pem"}, "want an absolute path"},
		{map[string]string{"GITHUB_APP_ID": "42:42", "GITHUB_APP_PRIVATE_KEY_FILE": y.keyFile}, "is not an App's id"},
	} {
		if _, err := (hooks.GitHub{}).CredentialFrom(func(name string) string { return c.file[name] }); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.file, err, c.want)
		}
	}
}

// Opening a thread and setting a status go the same way as a reply: one
// token asked for, and what is done is the bot's.
func TestGitHubAppOpenAndState(t *testing.T) {
	y := newAppYard(t)
	ctx := context.Background()
	cred := y.cred(t)
	const key = "9d4c3b2a-1f0e-4d5c-8b7a-6f5e4d3c2b1a"
	th, err := hooks.GitHub{}.Open(ctx, "acme/webapp", "Retries drop the last attempt", "Seen today.", key, cred)
	if err != nil || th.ID != "acme/webapp#8" {
		t.Fatalf("open %+v, %v", th, err)
	}
	if found, err := (hooks.GitHub{}).Opened(ctx, "acme/webapp", key, cred, time.Time{}); err != nil || found == nil || found.ID != th.ID {
		t.Fatalf("opened %+v, %v", found, err)
	}
	if res, err := (hooks.GitHub{}).SetState(ctx, "acme/webapp#7", hooks.StateDone, cred); err != nil || !res.Changed {
		t.Fatalf("set state %+v, %v", res, err)
	}
	if res, err := (hooks.GitHub{}).Forward(ctx, "acme/webapp#8", hooks.StateDone, cred); err != nil || !res.Changed {
		t.Fatalf("forward %+v, %v", res, err)
	}
	wantCalls(t, y.src.Calls(), "github app installation acme/webapp", "github app token acme/webapp",
		"github open acme/webapp#8", "github issues acme/webapp",
		"github read acme/webapp#7", "github set acme/webapp#7 closed",
		"github read acme/webapp#8", "github set acme/webapp#8 closed")
	if _, err := (hooks.GitHub{}).SetState(ctx, "other/repo#1", hooks.StateDone, cred); err == nil || !strings.Contains(err.Error(), "install the App there") {
		t.Fatalf("set state where it is not installed: %v", err)
	}
}

// GITHUB_TOKEN alone is used as it was: sent as it is, with nothing asked
// of an App, and the comment is the token's owner's.
func TestGitHubTokenAlone(t *testing.T) {
	y := newAppYard(t)
	file := map[string]string{"GITHUB_TOKEN": "gh-yardmaster"}
	cred, err := hooks.GitHub{}.CredentialFrom(func(name string) string { return file[name] })
	if err != nil || cred != "gh-yardmaster" {
		t.Fatalf("credential %q, %v", cred, err)
	}
	m, err := hooks.GitHub{}.Reply(context.Background(), githubEnv("acme/webapp#7"), "", "On it.", "", cred)
	if err != nil || m.Author.ID != "yardmaster" {
		t.Fatalf("reply %+v, %v", m, err)
	}
	wantCalls(t, y.src.Calls(), "github comment acme/webapp#7 by Yardmaster")
	// With both in the file the App is who answers.
	file["GITHUB_APP_ID"], file["GITHUB_APP_PRIVATE_KEY_FILE"] = "4242", y.keyFile
	cred, err = hooks.GitHub{}.CredentialFrom(func(name string) string { return file[name] })
	if err != nil || cred == "gh-yardmaster" {
		t.Fatalf("credential with both: %q, %v", cred, err)
	}
	if m, err = (hooks.GitHub{}).Reply(context.Background(), githubEnv("acme/webapp#7"), "", "As the App.", "", cred); err != nil || m.Author.ID != "yardr[bot]" {
		t.Fatalf("reply with both %+v, %v", m, err)
	}
}

// In a resident's pane the credential is the App's pair or GITHUB_TOKEN
// from its env_file, and nothing else: with neither it is refused, in words
// that name both, and gh is not asked. Before, it answered with the gh
// login of whoever runs the yard.
func TestGitHubCredentialOfAResident(t *testing.T) {
	ctx := context.Background()
	bin := t.TempDir()
	asked := filepath.Join(bin, "asked")
	gh := "#!/bin/sh\necho \"$*\" >> " + asked + "\necho gho_the_operators\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"GITHUB_TOKEN", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_FILE", "YARD_RESIDENT"} {
		t.Setenv(name, "")
	}
	t.Setenv("YARDR_RESIDENT", "yardmaster")
	_, err := hooks.GitHub{}.Credential(ctx)
	if err == nil {
		t.Fatal("a resident with no GitHub credential got one")
	}
	for _, want := range []string{"resident yardmaster", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_FILE", "GITHUB_TOKEN", "env_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "gho_the_operators") {
		t.Errorf("refusal shows gh's token: %v", err)
	}
	if _, statErr := os.Stat(asked); statErr == nil {
		t.Error("gh was asked for a resident")
	}
	// Its own credential is used, and gh still is not asked.
	t.Setenv("GITHUB_TOKEN", "github_pat_yardmaster")
	if c, err := (hooks.GitHub{}).Credential(ctx); err != nil || c != "github_pat_yardmaster" {
		t.Errorf("token: %q, %v", c, err)
	}
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_APP_ID", "4242")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", "/keys/yardr.pem")
	if c, err := (hooks.GitHub{}).Credential(ctx); err != nil || c != appCred(t, "4242", "/keys/yardr.pem") {
		t.Errorf("app: %q, %v", c, err)
	}
	if _, statErr := os.Stat(asked); statErr == nil {
		t.Error("gh was asked for a resident")
	}
	// A person at their own shell, with nothing set, still speaks as gh
	// has them logged in.
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", "")
	t.Setenv("YARDR_RESIDENT", "")
	if c, err := (hooks.GitHub{}).Credential(ctx); err != nil || c != "gho_the_operators" {
		t.Errorf("a person: %q, %v", c, err)
	}
}

// What an env_file gives is told from its names alone.
func TestCredentialSet(t *testing.T) {
	for _, c := range []struct {
		source string
		keys   []string
		kind   string
	}{
		{"github", []string{"GITHUB_TOKEN"}, "token"},
		{"github", []string{"GITHUB_TOKEN", "GITHUB_APP_PRIVATE_KEY_FILE", "GITHUB_APP_ID"}, "GitHub App"},
		{"github", []string{"GITHUB_APP_ID"}, ""},
		{"github", []string{"LINEAR_API_KEY"}, ""},
		{"linear", []string{"LINEAR_API_KEY"}, "API key"},
		{"token", []string{"GITHUB_TOKEN"}, ""},
	} {
		if w, ok := hooks.CredentialSet(c.source, c.keys); w.Kind != c.kind || ok != (c.kind != "") {
			t.Errorf("%s %v: %+v, %v; want %q", c.source, c.keys, w, ok, c.kind)
		}
	}
	if got := hooks.CredentialNames("github"); got != "GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE, or GITHUB_TOKEN" {
		t.Errorf("names: %s", got)
	}
}

func wantCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

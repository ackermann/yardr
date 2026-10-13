package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// appYard is a yard whose yardmaster speaks on GitHub as the App yardr: a
// stand-in for GitHub that knows the App (installed on acme/webapp), the
// yardmaster's env_file with the App's two lines, and deliveries from a thread
// where the App is installed and from one where it is not. The environment
// is the yardmaster's pane's: YARDR_CREW and what the env_file sets.
type appYard struct {
	a       *app
	st      store.API
	src     *standin.Server
	keyFile string
	seqs    map[string]string
	mu      sync.Mutex
	sent    []string // every bearer token sent
}

func newAppYard(t *testing.T) *appYard {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	dir := t.TempDir()
	y := &appYard{st: st, keyFile: filepath.Join(dir, "yardr.pem"), seqs: map[string]string{}}
	theApp, err := standin.NewApp("4242", "yardr", y.keyFile, "acme/webapp")
	if err != nil {
		t.Fatal(err)
	}
	y.src = &standin.Server{GitHubToken: "gh-operator", GitHubLogin: "jo", App: theApp}
	y.src.Add(standin.Issue{Source: "github", Key: "acme/webapp#7", Title: "Retries", Status: "open"})
	y.src.Add(standin.Issue{Source: "github", Key: "other/repo#1", Title: "Elsewhere", Status: "open"})
	inner := y.src.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		y.mu.Lock()
		y.sent = append(y.sent, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		y.mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	old := hooks.GitHubAPI
	hooks.GitHubAPI = srv.URL
	t.Cleanup(func() { hooks.GitHubAPI = old })

	envFile := filepath.Join(dir, "yardmaster.env")
	lines := "GITHUB_APP_ID=4242\nGITHUB_APP_PRIVATE_KEY_FILE=" + y.keyFile + "\n"
	if err := os.WriteFile(envFile, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	def := store.CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"env_file": envFile, store.SetState: "github", store.OpenThreads: "github"}}
	if _, err := st.PutCrew(ctx, def, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "operator", "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for name, thread := range map[string]string{"installed": "acme/webapp#7", "elsewhere": "other/repo#1"} {
		env := hooks.Envelope{Source: "github", Type: "issue_comment", Action: "created",
			Actor: hooks.Party{ID: "jane", Name: "Jane"}, Thread: hooks.Thread{ID: thread, Title: "Retries"}, Message: "10", Text: thread}
		seq, _, err := own.RecordHook(ctx, "gh", env.Map(), []byte("{}"), nil)
		if err != nil {
			t.Fatal(err)
		}
		y.seqs[name] = strconv.FormatUint(seq, 10)
	}
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_APP_ID", "4242")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", y.keyFile)
	t.Setenv("YARDR_CREW", "yardmaster")
	y.a = &app{st: st, actor: "yardmaster"}
	return y
}

// clean fails when text, or any event of the yard, holds a token or JWT
// that was sent, or a line of the App's private key.
func (y *appYard) clean(t *testing.T, texts ...string) {
	t.Helper()
	y.mu.Lock()
	secrets := append(y.src.Tokens(), y.sent...)
	y.mu.Unlock()
	pem, err := os.ReadFile(y.keyFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(pem), "\n") {
		if len(line) > 20 && !strings.HasPrefix(line, "-----") {
			secrets = append(secrets, line)
		}
	}
	evs, err := y.st.EventsSince(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(evs)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range append(texts, string(raw)) {
		for _, s := range secrets {
			if s != "" && strings.Contains(text, s) {
				t.Fatalf("a secret shows in: %.300s", text)
			}
		}
	}
}

// yardr reply, thread open and thread state, run in the pane of a crew member
// whose env_file names a GitHub App, speak as the App's bot, with one
// installation token asked for; crew show says which kind of
// credential it has, by name. Neither the token nor the key is in an
// event or in what is printed.
func TestSpeakingAsAGitHubApp(t *testing.T) {
	y := newAppYard(t)
	out, err := run(t, y.a.replyCmd(), y.seqs["installed"], "On it.")
	if err != nil || !strings.HasPrefix(out, "https://github.com/acme/webapp/issues/7#issuecomment-") {
		t.Fatalf("reply: %q, %v", out, err)
	}
	opened, err := run(t, y.a.threadCmd(), "open", "github", "--in", "acme/webapp", "Retries drop the last attempt", "-b", "Seen today.")
	if err != nil || opened != "https://github.com/acme/webapp/issues/8\n" {
		t.Fatalf("thread open: %q, %v", opened, err)
	}
	stated, _, err := state(t, y.a, "--source", "github", "acme/webapp#7", "done")
	if err != nil || y.src.Status("github", "acme/webapp#7") != "closed" {
		t.Fatalf("thread state: %q, %v", stated, err)
	}
	want := []string{"github app installation acme/webapp", "github app token acme/webapp",
		"github comment acme/webapp#7 by yardr[bot]", "github open acme/webapp#8",
		"github read acme/webapp#7", "github set acme/webapp#7 closed"}
	if got := y.src.Calls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n  %s", strings.Join(got, "\n  "))
	}
	show, err := run(t, y.a.crewCmd(), "show", "yardmaster")
	if err != nil || !strings.Contains(show, "  github credential: GitHub App (GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY_FILE)\n") {
		t.Fatalf("crew show: %v\n%s", err, show)
	}
	y.clean(t, out, opened, stated, show)

	// Where the App is not installed the reply is refused, and says what
	// to do about it.
	_, err = run(t, y.a.replyCmd(), y.seqs["elsewhere"], "Hello.")
	if err == nil || !strings.Contains(err.Error(), "not installed on other/repo") || !strings.Contains(err.Error(), "install the App there") {
		t.Fatalf("not installed: %v", err)
	}
	y.clean(t, err.Error())
}

// A crew member whose environment has no GitHub credential is refused, with
// both ways to give it one named, and gh is not asked: before, the reply
// went out under the gh login of whoever runs the yard. The operator, at
// their own shell, still speaks as gh has them logged in.
func TestCrewWithoutGitHubCredentialIsRefused(t *testing.T) {
	y := newAppYard(t)
	bin := t.TempDir()
	asked := filepath.Join(bin, "asked")
	gh := "#!/bin/sh\necho \"$*\" >> " + asked + "\necho gh-operator\n"
	scripttest.Write(t, filepath.Join(bin, "gh"), gh)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", "")

	_, err := run(t, y.a.replyCmd(), y.seqs["installed"], "On it.")
	if err == nil {
		t.Fatal("posted")
	}
	for _, want := range []string{"crew member yardmaster has no GitHub credential", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_FILE", "GITHUB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
	if _, statErr := os.Stat(asked); statErr == nil {
		t.Error("gh was asked on a crew member's behalf")
	}
	if got := y.src.Calls(); len(got) != 0 {
		t.Errorf("calls %v", got)
	}

	// crew show says a crew member that is someone on GitHub has nothing
	// to answer there with. The bot's login is kept in lowercase, as
	// deliveries carry it.
	y.a.actor = "jo"
	if out, err := run(t, y.a.crewCmd(), "identity", "yardmaster", "--source", "github", "--key", "Yardr[bot]"); err != nil || !strings.Contains(out, "yardr[bot]") {
		t.Fatalf("identity: %q, %v", out, err)
	}
	y.a.actor = "yardmaster"
	r, err := y.st.GetCrew(context.Background(), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.Config["env_file"].(string), []byte("GITHUB_APP_ID=4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	show, err := run(t, y.a.crewCmd(), "show", "yardmaster")
	if err != nil || !strings.Contains(show, "  github credential: none (GITHUB_APP_ID): it takes GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE, or GITHUB_TOKEN\n") ||
		!strings.Contains(show, "github identity: yardr[bot]") {
		t.Fatalf("crew show: %v\n%s", err, show)
	}
	if err := os.WriteFile(r.Config["env_file"].(string), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if show, err = run(t, y.a.crewCmd(), "show", "yardmaster"); err != nil || !strings.Contains(show, "  github credential: none: it cannot answer in github until its env_file or env_command sets ") {
		t.Fatalf("crew show: %v\n%s", err, show)
	}

	// The operator's own shell: no YARDR_CREW, acting as themselves.
	t.Setenv("YARDR_CREW", "")
	y.a.actor = "jo"
	out, err := run(t, y.a.replyCmd(), y.seqs["installed"], "On it.", "--crew", "yardmaster")
	if err != nil || !strings.Contains(out, "#issuecomment-") {
		t.Fatalf("the operator's reply: %q, %v", out, err)
	}
	// The refused attempt is on record, so this one first looks for a
	// comment it might have left.
	if got := y.src.Calls(); strings.Join(got, "; ") != "github comments acme/webapp#7; github comment acme/webapp#7 by jo" {
		t.Errorf("calls %v", got)
	}
}

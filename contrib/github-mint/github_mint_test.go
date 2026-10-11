// Package githubmint tests contrib/github-mint/github-mint, the
// mint_command of a source whose credential is a GitHub App's.
package githubmint

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/standin"
)

// mintApp is a stand-in GitHub with the App 123456 installed on repos: the
// server, the stand-in, and the App's key file.
func mintApp(t *testing.T, repos ...string) (*httptest.Server, *standin.Server, string) {
	t.Helper()
	key := filepath.Join(t.TempDir(), "app.pem")
	app, err := standin.NewApp("123456", "yardr-test", key, repos...)
	if err != nil {
		t.Fatal(err)
	}
	src := &standin.Server{App: app}
	srv := httptest.NewServer(src.Handler())
	t.Cleanup(srv.Close)
	return srv, src, key
}

// mint runs the script with stdin and YARDR_SOURCE_BASE, in the environment
// the core runs a mint_command with: PATH, HOME and the base, and nothing
// else of the yard's.
func mint(t *testing.T, stdin, base string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("./github-mint")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "YARDR_SOURCE_BASE=" + base}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func stdinFor(id, key, scope string) string {
	return `{"inputs":{"GITHUB_APP_ID":"` + id + `","GITHUB_APP_PRIVATE_KEY_FILE":"` + key + `"},"scope":"` + scope + `"}`
}

// With a scope naming a repository the answer is an installation token
// narrowed to it: it reads there, and nowhere else.
func TestGitHubMintNarrowsToTheScope(t *testing.T) {
	srv, src, key := mintApp(t, "acme/webapp")
	before := time.Now()
	stdout, stderr, err := mint(t, stdinFor("123456", key, "acme/webapp#7"), srv.URL)
	if err != nil {
		t.Fatalf("mint: %v\n%s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	var o struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(stdout), &o); err != nil || !strings.HasPrefix(o.Token, "ghs_") {
		t.Fatalf("answer = %q: %v", stdout, err)
	}
	expires, err := time.Parse(time.RFC3339, o.ExpiresAt)
	if err != nil || expires.Before(before.Add(50*time.Minute)) || expires.After(before.Add(70*time.Minute)) {
		t.Fatalf("expires_at = %q", o.ExpiresAt)
	}
	if got := src.Minted(); got != 1 {
		t.Fatalf("tokens asked for: %d, want 1", got)
	}
	if calls := src.Calls(); len(calls) != 2 || calls[0] != "github app installation acme/webapp" || calls[1] != "github app token acme/webapp" {
		t.Fatalf("calls = %v", calls)
	}
	// The token reads the repository it was minted for, and no other.
	get := func(repo string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/repos/"+repo+"/issues", nil)
		req.Header.Set("Authorization", "Bearer "+o.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	if code := get("acme/webapp"); code != 200 {
		t.Fatalf("reading acme/webapp: %d", code)
	}
	if code := get("acme/other"); code != 401 {
		t.Fatalf("reading acme/other: %d, want refused", code)
	}
	// A bare repository mints the same way a thread there does.
	if _, _, err := mint(t, stdinFor("123456", key, "acme/webapp"), srv.URL); err != nil {
		t.Fatalf("a bare repository: %v", err)
	}
	if got := src.Minted(); got != 2 {
		t.Fatalf("tokens asked for: %d, want 2", got)
	}
}

// With no scope the answer is the App's JWT itself, good for the /app
// endpoints a check call reads.
func TestGitHubMintWithoutScopeAnswersTheJWT(t *testing.T) {
	srv, src, key := mintApp(t, "acme/webapp")
	stdout, stderr, err := mint(t, stdinFor("123456", key, ""), srv.URL)
	if err != nil {
		t.Fatalf("mint: %v\n%s", err, stderr)
	}
	var o struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(stdout), &o); err != nil {
		t.Fatalf("answer = %q: %v", stdout, err)
	}
	parts := strings.Split(o.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("the token is no JWT: %q", o.Token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("the token is no JWT: %v", err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Iss != "123456" || claims.Exp-claims.Iat != 600 {
		t.Fatalf("claims = %s", raw)
	}
	if expires, err := time.Parse(time.RFC3339, o.ExpiresAt); err != nil || expires.Unix() != claims.Exp {
		t.Fatalf("expires_at = %q, want the JWT's", o.ExpiresAt)
	}
	// The JWT authenticates as the App: the installation lookup takes it.
	req, _ := http.NewRequest("GET", srv.URL+"/repos/acme/webapp/installation", nil)
	req.Header.Set("Authorization", "Bearer "+o.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("the installation lookup: %d", res.StatusCode)
	}
	if got := src.Minted(); got != 0 {
		t.Fatalf("tokens asked for: %d, want none", got)
	}
}

// What goes wrong is said on stderr, with nothing of the answer on stdout:
// no token, and of the inputs only the key's path where the failure is the
// file.
func TestGitHubMintRefusals(t *testing.T) {
	srv, _, key := mintApp(t, "acme/webapp")
	badMode := filepath.Join(t.TempDir(), "open.pem")
	if err := os.WriteFile(badMode, []byte("not a key"), 0o644); err != nil {
		t.Fatal(err)
	}
	badKey := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(badKey, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		stdin string
		base  string
		want  string
	}{
		"not installed":     {stdinFor("123456", key, "other/repo#1"), srv.URL, "GitHub has no installation of the App on other/repo: install the App there"},
		"no repository":     {stdinFor("123456", key, "noslash"), srv.URL, "the scope names no repository (noslash)"},
		"no id":             {`{"inputs":{"GITHUB_APP_PRIVATE_KEY_FILE":"` + key + `"},"scope":""}`, srv.URL, "its inputs name no GITHUB_APP_ID"},
		"no key file":       {`{"inputs":{"GITHUB_APP_ID":"123456"},"scope":""}`, srv.URL, "its inputs name no GITHUB_APP_PRIVATE_KEY_FILE"},
		"no scope":          {`{"inputs":{"GITHUB_APP_ID":"123456","GITHUB_APP_PRIVATE_KEY_FILE":"` + key + `"}}`, srv.URL, "got no scope"},
		"no JSON":           {`{`, srv.URL, "its inputs name no GITHUB_APP_ID"},
		"nothing":           {``, srv.URL, "got nothing"},
		"a key open":        {stdinFor("123456", badMode, ""), srv.URL, "is open to others (mode 644)"},
		"a key missing":     {stdinFor("123456", filepath.Join(t.TempDir(), "nope.pem"), ""), srv.URL, "is not there"},
		"a key no good":     {stdinFor("123456", badKey, ""), srv.URL, "signs nothing"},
		"no service":        {stdinFor("123456", key, "acme/webapp"), "http://127.0.0.1:1", "could not reach http://127.0.0.1:1 (curl exit 7)"},
		"no service scoped": {stdinFor("123456", key, ""), "http://127.0.0.1:1", ""}, // scope-free mints nothing over the network
	} {
		stdout, stderr, err := mint(t, tc.stdin, tc.base)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: %v\n%s", name, err, stderr)
			}
			continue
		}
		if err == nil || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: %v\n%s", name, err, stderr)
		}
		if !strings.HasPrefix(stderr, "github-mint: ") {
			t.Errorf("%s names no command: %q", name, stderr)
		}
		if stdout != "" {
			t.Errorf("%s prints %q", name, stdout)
		}
		for _, secret := range []string{"123456", "eyJ", "ghs_"} {
			if strings.Contains(stderr, secret) {
				t.Errorf("%s shows %q: %q", name, secret, stderr)
			}
		}
	}
}

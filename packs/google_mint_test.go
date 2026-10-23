package packs_test

// These test google-mint, the mint command each google pack's
// declarations bring (credential.mint_command), against a fake gws on
// PATH and a fake token endpoint. No test reaches Google. The script is
// one copy per pack, since a pack is applied alone and a command's
// program is a file inside its own pack; TestGoogleMintCopiesAgree holds
// the copies to one text, and the tests run the google-mail one.

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/packs"
)

// The fake refresh credentials the fake gws exports, and the fake access
// token the fake endpoint answers: what the script must never print but
// as the answer's token.
const (
	fakeClientID     = "123456789012-abcdefghijklmnopqrstuvwxyz123456.apps.googleusercontent.com"
	fakeClientSecret = "GOCSPX-fake-client-secret-value"
	fakeRefresh      = "1//fake-refresh-token-value-0123456789abcdef"
	fakeAccess       = "ya29.fake-access-token-value"
)

// fakeGWS is a gws shaped like the real one's for the two commands the
// script runs, steered by GWS_* variables: GWS_USER (the default login's
// account, op@example.com), GWS_STATUS_EXIT and GWS_EXPORT_EXIT (a
// failing command), GWS_STATUS_BODY (no-user, bad-json) and GWS_EXPORT
// (no-refresh, bad-json, weird-chars). Like the real one it notes the
// keyring backend on stderr, which the script passes over. Like the real
// one it reads one login from GOOGLE_WORKSPACE_CLI_CONFIG_DIR where that
// variable is set: the login is the fake-login and fake-refresh files in
// that directory, and a directory without them answers no login. Where
// GWS_OBSERVE names a file, each command appends the config directory it
// was started with, so a test sees which login each gws run read.
const fakeGWS = `#!/bin/sh
echo "Using keyring backend: keyring" >&2
if [ -n "${GWS_OBSERVE:-}" ]; then echo "$1 $2 config_dir=${GOOGLE_WORKSPACE_CLI_CONFIG_DIR:-<unset>}" >> "$GWS_OBSERVE"; fi
dir=${GOOGLE_WORKSPACE_CLI_CONFIG_DIR:-}
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  if [ "${GWS_STATUS_EXIT:-0}" != "0" ]; then echo "Error: fake status failed" >&2; exit 1; fi
  if [ -n "$dir" ]; then
    if [ -f "$dir/fake-login" ]; then echo "{\"user\":\"$(cat "$dir/fake-login")\"}"; exit 0; fi
    echo "Error: fake has no login in $dir" >&2; exit 1
  fi
  case "${GWS_STATUS_BODY:-good}" in
    no-user) echo '{"nobody":true}' ;;
    bad-json) echo '{' ;;
    *) echo "{\"user\":\"${GWS_USER:-op@example.com}\"}" ;;
  esac
  exit 0
fi
if [ "$1" = "auth" ] && [ "$2" = "export" ]; then
  if [ "$3" != "--unmasked" ]; then echo "Error: fake export wants --unmasked" >&2; exit 2; fi
  if [ "${GWS_EXPORT_EXIT:-0}" != "0" ]; then echo "Error: fake not logged in" >&2; exit 1; fi
  if [ -n "$dir" ]; then
    if [ -f "$dir/fake-refresh" ]; then echo "{\"client_id\":\"` + fakeClientID + `\",\"client_secret\":\"` + fakeClientSecret + `\",\"refresh_token\":\"$(cat "$dir/fake-refresh")\",\"type\":\"authorized_user\"}"; exit 0; fi
    echo "Error: fake has no login in $dir" >&2; exit 1
  fi
  case "${GWS_EXPORT:-good}" in
    no-refresh) echo '{"client_id":"fake-client-id","client_secret":"fake-client-secret","type":"authorized_user"}' ;;
    bad-json) echo '{' ;;
    weird-chars) echo '{"client_id":"fake-client-id","client_secret":"has space","refresh_token":"1//fake","type":"authorized_user"}' ;;
    *) echo '{"client_id":"` + fakeClientID + `","client_secret":"` + fakeClientSecret + `","refresh_token":"` + fakeRefresh + `","type":"authorized_user"}' ;;
  esac
  exit 0
fi
echo "Error: fake gws takes auth status|export, got $*" >&2
exit 2
`

// fakeToken is a fake https://oauth2.googleapis.com/token: it answers
// mode (good, bad-request, error, bad-json, no-token, weird-token) and
// keeps the forms it was sent.
type fakeToken struct {
	t     *testing.T
	mu    sync.Mutex
	mode  string
	forms []url.Values
	types []string
}

func (f *fakeToken) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, err := url.ParseQuery(string(body))
	if err != nil {
		f.t.Errorf("the endpoint was sent %q: %v", body, err)
	}
	f.mu.Lock()
	f.forms = append(f.forms, form)
	f.types = append(f.types, r.Header.Get("Content-Type"))
	mode := f.mode
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch mode {
	case "bad-request":
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"invalid_grant","error_description":"Bad Request"}`)
	case "error":
		w.WriteHeader(500)
		io.WriteString(w, `{"error":"internal"}`)
	case "echo-request":
		w.WriteHeader(400)
		_, _ = w.Write(body)
	case "bad-json":
		io.WriteString(w, `{`)
	case "no-token":
		io.WriteString(w, `{"token_type":"Bearer"}`)
	case "weird-token":
		io.WriteString(w, `{"access_token":"has space","expires_in":3599}`)
	default:
		io.WriteString(w, `{"access_token":"`+tokenFor(form.Get("refresh_token"))+`","expires_in":3599,"scope":"openid","token_type":"Bearer"}`)
	}
}

// tokenFor is the access token the fake endpoint answers for a refresh
// token: the fixed fake token for the default fake login, and a token
// derived from any other refresh token, so two logins mint two tokens.
func tokenFor(refresh string) string {
	if refresh == fakeRefresh {
		return fakeAccess
	}
	return "ya29." + strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.' || r == '_' || r == '-':
			return r
		}
		return -1
	}, refresh)
}

func (f *fakeToken) sent() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.forms...)
}

func (f *fakeToken) setMode(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mode = mode
}

func (f *fakeToken) firstType() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.types) == 0 {
		return ""
	}
	return f.types[0]
}

// mint runs the script with stdin in an isolated pack-test environment,
// plus the endpoint and the fake's steering. The core also passes through
// the proxy and CA variables when it runs a mint_command. Extra steering
// goes in env.
func mint(t *testing.T, stdin, endpoint string, env ...string) (string, string, error) {
	t.Helper()
	return mintHome(t, t.TempDir(), stdin, endpoint, env...)
}

// mintHome runs the script as mint does, with home as its HOME, so a
// test plants a per-account login where the script derives it.
func mintHome(t *testing.T, home, stdin, endpoint string, env ...string) (string, string, error) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gws"), []byte(fakeGWS), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("./google-mail/google-mint")
	cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + home, "GOOGLE_TOKEN_ENDPOINT=" + endpoint}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// A second account with its own login and refresh token, beside the
// default fake login's op@example.com.
const (
	adaAccount = "ada@example.com"
	adaRefresh = "1//fake-ada-refresh-token-value-aaaa"
)

// writeLogin plants a fake gws login in account's own directory under
// home, as gws auth login with that config directory would: the login
// answers user, with refresh as its refresh token, or no export where
// refresh is empty.
func writeLogin(t *testing.T, home, account, user, refresh string) string {
	t.Helper()
	dir := filepath.Join(home, ".config", "gws-accounts", account)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake-login"), []byte(user+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if refresh != "" {
		if err := os.WriteFile(filepath.Join(dir, "fake-refresh"), []byte(refresh), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// filesUnder lists every file under dir, relative, so a test holds that
// the script wrote none beside what the test planted: no refresh
// credential reaches a file.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			out = append(out, rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func stdinFor(account, scope string) string {
	return `{"inputs":{"GOOGLE_ACCOUNT":"` + account + `"},"scope":"` + scope + `"}`
}

// A good exchange: the token of the endpoint's answer, expiring when it
// says, asked for with the grant and the three refresh credentials, and
// nothing on stderr. The fake gws is on PATH ahead of any real one, so
// where this machine holds the operator's gws the answer still comes
// from the fake: the endpoint got the fake secrets.
func TestGoogleMintGoodExchange(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	before := time.Now()
	stdout, stderr, err := mint(t, stdinFor("op@example.com", ""), srv.URL)
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
	if err := json.Unmarshal([]byte(stdout), &o); err != nil || o.Token != fakeAccess {
		t.Fatalf("answer = %q: %v", stdout, err)
	}
	expires, err := time.Parse(time.RFC3339, o.ExpiresAt)
	if err != nil || expires.Before(before.Add(3599*time.Second).Add(-2*time.Minute)) || expires.After(before.Add(3599*time.Second).Add(2*time.Minute)) {
		t.Fatalf("expires_at = %q", o.ExpiresAt)
	}
	forms := endpoint.sent()
	if len(forms) != 1 {
		t.Fatalf("exchanges: %d, want 1", len(forms))
	}
	if got := forms[0]; got.Get("grant_type") != "refresh_token" || got.Get("client_id") != fakeClientID || got.Get("client_secret") != fakeClientSecret || got.Get("refresh_token") != fakeRefresh {
		t.Fatalf("form = %v", got)
	}
	if got := endpoint.firstType(); got != "application/x-www-form-urlencoded" {
		t.Fatalf("content type = %q", got)
	}
	for _, secret := range []string{fakeClientID, fakeClientSecret, fakeRefresh} {
		if strings.Contains(stdout, secret) {
			t.Errorf("the answer holds %q", secret)
		}
	}
}

// Two accounts with two logins mint two tokens: the default login's and
// a per-account one's, each exchange carrying its own refresh token and
// each answer its own access token. The observe file holds which login
// each gws run read: the default account's runs set no config directory,
// exactly as before, and the other's set that account's directory for
// both status and export. Afterwards the home holds nothing the script
// wrote: no refresh credential reaches a file.
func TestGoogleMintTwoAccounts(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	home := t.TempDir()
	adaDir := writeLogin(t, home, adaAccount, adaAccount, adaRefresh)
	observe := filepath.Join(t.TempDir(), "observe")
	for _, tc := range []struct{ account, refresh string }{
		{"op@example.com", fakeRefresh},
		{adaAccount, adaRefresh},
	} {
		stdout, stderr, err := mintHome(t, home, stdinFor(tc.account, ""), srv.URL, "GWS_OBSERVE="+observe)
		if err != nil {
			t.Fatalf("mint %s: %v\n%s", tc.account, err, stderr)
		}
		if stderr != "" {
			t.Fatalf("mint %s: stderr = %q", tc.account, stderr)
		}
		var o struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(stdout), &o); err != nil {
			t.Fatalf("mint %s: answer = %q: %v", tc.account, stdout, err)
		}
		if want := tokenFor(tc.refresh); o.Token != want {
			t.Fatalf("mint %s: token = %q, want %q", tc.account, o.Token, want)
		}
		for _, secret := range []string{fakeClientID, fakeClientSecret, fakeRefresh, adaRefresh} {
			if strings.Contains(stdout, secret) {
				t.Errorf("mint %s: the answer holds %q", tc.account, secret)
			}
		}
	}
	if tokenFor(fakeRefresh) == tokenFor(adaRefresh) {
		t.Fatalf("the two accounts minted one token: %q", tokenFor(fakeRefresh))
	}
	forms := endpoint.sent()
	if len(forms) != 2 {
		t.Fatalf("exchanges: %d, want 2", len(forms))
	}
	for i, want := range []string{fakeRefresh, adaRefresh} {
		if got := forms[i].Get("refresh_token"); got != want {
			t.Fatalf("exchange %d refresh_token = %q, want %q", i, got, want)
		}
	}
	seen, err := os.ReadFile(observe)
	if err != nil {
		t.Fatal(err)
	}
	wantObserve := "auth status config_dir=<unset>\n" +
		"auth export config_dir=<unset>\n" +
		"auth status config_dir=<unset>\n" +
		"auth status config_dir=" + adaDir + "\n" +
		"auth export config_dir=" + adaDir + "\n"
	if string(seen) != wantObserve {
		t.Fatalf("gws runs:\n%s\nwant:\n%s", seen, wantObserve)
	}
	wantFiles := []string{
		filepath.Join(".config", "gws-accounts", adaAccount, "fake-login"),
		filepath.Join(".config", "gws-accounts", adaAccount, "fake-refresh"),
	}
	if got := filesUnder(t, home); strings.Join(got, "\n") != strings.Join(wantFiles, "\n") {
		t.Fatalf("files under home = %q, want %q", got, wantFiles)
	}
}

// An account with no login of its own is refused with the login that
// makes it: the endpoint hears nothing, and stdout stays empty.
func TestGoogleMintNoLogin(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	stdout, stderr, err := mint(t, stdinFor(adaAccount, ""), srv.URL)
	if err == nil {
		t.Fatalf("mint minted with no login:\n%s", stdout)
	}
	for _, want := range []string{
		"no gws login for GOOGLE_ACCOUNT=" + adaAccount,
		filepath.Join(".config", "gws-accounts", adaAccount),
		"GOOGLE_WORKSPACE_CLI_CONFIG_DIR=",
		"gws auth login -s gmail,drive,docs,slides,calendar",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if !strings.HasPrefix(stderr, "google-mint: ") {
		t.Errorf("names no command: %q", stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(endpoint.sent()) != 0 {
		t.Fatal("the endpoint was asked for a token it must not mint")
	}
}

// A per-account login for another account than the inputs name is
// refused, not minted: the endpoint hears nothing, and stdout stays
// empty. The refusal names the login it found and the one that makes
// the account's own.
func TestGoogleMintWrongAccount(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	home := t.TempDir()
	adaDir := writeLogin(t, home, adaAccount, "mallory@example.com", adaRefresh)
	stdout, stderr, err := mintHome(t, home, stdinFor(adaAccount, ""), srv.URL)
	if err == nil {
		t.Fatalf("mint minted for a login of another account:\n%s", stdout)
	}
	for _, want := range []string{
		"the gws login in " + adaDir + " is for mallory@example.com, not GOOGLE_ACCOUNT=" + adaAccount,
		"gws auth login -s gmail,drive,docs,slides,calendar",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(endpoint.sent()) != 0 {
		t.Fatal("the endpoint was asked for a token it must not mint")
	}
	for _, secret := range []string{fakeClientID, fakeClientSecret, fakeRefresh, adaRefresh} {
		if strings.Contains(stderr, secret) {
			t.Errorf("stderr holds %q", secret)
		}
	}
}

// A per-account login whose export fails is refused without repeating
// what gws said: on that line the refresh credentials themselves would
// be the leak.
func TestGoogleMintPerAccountExportFails(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	home := t.TempDir()
	writeLogin(t, home, adaAccount, adaAccount, "")
	stdout, stderr, err := mintHome(t, home, stdinFor(adaAccount, ""), srv.URL)
	if err == nil || !strings.Contains(stderr, "gws auth export for GOOGLE_ACCOUNT="+adaAccount) {
		t.Fatalf("err = %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "fake has no login") {
		t.Errorf("a failed export repeats gws: %q", stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(endpoint.sent()) != 0 {
		t.Fatal("the endpoint was asked for a token it must not mint")
	}
}

// An account that is no single directory name is refused before it
// names a login directory: gws runs once, for the default login, and
// never with a crafted config directory.
func TestGoogleMintHostileAccount(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	for _, account := range []string{"../elsewhere@example.com", adaAccount + "/evil", ".."} {
		home := t.TempDir()
		observe := filepath.Join(t.TempDir(), "observe")
		stdout, stderr, err := mintHome(t, home, stdinFor(account, ""), srv.URL, "GWS_OBSERVE="+observe)
		if err == nil || !strings.Contains(stderr, "names no per-account login") {
			t.Errorf("mint %s: err = %v\n%s", account, err, stderr)
		}
		if stdout != "" {
			t.Errorf("mint %s: stdout = %q", account, stdout)
		}
		seen, rerr := os.ReadFile(observe)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if want := "auth status config_dir=<unset>\n"; string(seen) != want {
			t.Errorf("mint %s: gws runs:\n%s\nwant:\n%s", account, seen, want)
		}
		if len(endpoint.sent()) != 0 {
			t.Errorf("mint %s: the endpoint was asked", account)
		}
	}
}

// The login command a refusal names is one shell word for its
// directory: an account with a ';', a '$', a space or a quote in it is
// data naming a directory, and must not become a command when the
// person the refusal is shown to copies that command into a shell.
// Without the quoting the directory after ';' would be run as a
// second command.
func TestGoogleMintLoginCommandQuoted(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	for _, account := range []string{"a;id@example.com", "$(id)@example.com", "a b@example.com", "a'b@example.com"} {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "gws-accounts", account)
		quoted := "'" + strings.ReplaceAll(dir, "'", "'\\''") + "'"
		want := "GOOGLE_WORKSPACE_CLI_CONFIG_DIR=" + quoted + " gws auth login -s gmail,drive,docs,slides,calendar"
		stdout, stderr, err := mintHome(t, home, stdinFor(account, ""), srv.URL)
		if err == nil {
			t.Errorf("mint %s: minted with no login:\n%s", account, stdout)
		}
		if !strings.Contains(stderr, want) {
			t.Errorf("mint %s: stderr lacks quoted login command %q:\n%s", account, want, stderr)
		}
		if stdout != "" {
			t.Errorf("mint %s: stdout = %q", account, stdout)
		}
		if len(endpoint.sent()) != 0 {
			t.Errorf("mint %s: the endpoint was asked", account)
		}
	}
}

// GWS_BIN names the gws, ahead of whatever is on PATH: the mint goes
// through it, with ~/ expanded.
func TestGoogleMintGWSBin(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gws"), []byte(fakeGWS), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("./google-mail/google-mint")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GOOGLE_TOKEN_ENDPOINT=" + srv.URL, "GWS_BIN=~/bin/gws"}
	cmd.Stdin = strings.NewReader(stdinFor("op@example.com", ""))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("mint: %v\n%s", err, stderr.String())
	}
	var o struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &o); err != nil || o.Token != fakeAccess {
		t.Fatalf("answer = %q: %v", stdout.String(), err)
	}
}

// What goes wrong is said on stderr, with nothing of the answer on
// stdout and no refresh credential in either: neither the client
// secret nor the refresh token, on a failure or anywhere else. The
// account is no secret and may be named.
func TestGoogleMintRefusals(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	for name, tc := range map[string]struct {
		stdin string
		env   []string
		mode  string
		want  string
	}{
		"no account":           {`{"inputs":{},"scope":""}`, nil, "good", "its inputs name no GOOGLE_ACCOUNT"},
		"no scope":             {`{"inputs":{"GOOGLE_ACCOUNT":"op@example.com"}}`, nil, "good", "got no scope"},
		"no JSON":              {`{`, nil, "good", "its inputs name no GOOGLE_ACCOUNT"},
		"nothing":              {``, nil, "good", "got nothing"},
		"no gws":               {stdinFor("op@example.com", ""), []string{"GWS_BIN=/nonexistent/gws"}, "good", "GWS_BIN=/nonexistent/gws is no program to run"},
		"status fails":         {stdinFor("op@example.com", ""), []string{"GWS_STATUS_EXIT=1"}, "good", "gws auth status failed (exit 1)"},
		"status no user":       {stdinFor("op@example.com", ""), []string{"GWS_STATUS_BODY=no-user"}, "good", "gws auth status answered with no user"},
		"status bad JSON":      {stdinFor("op@example.com", ""), []string{"GWS_STATUS_BODY=bad-json"}, "good", "gws auth status answered with no user"},
		"export fails":         {stdinFor("op@example.com", ""), []string{"GWS_EXPORT_EXIT=1"}, "good", "gws auth export failed (exit 1)"},
		"export no refresh":    {stdinFor("op@example.com", ""), []string{"GWS_EXPORT=no-refresh"}, "good", "gws auth export answered with no refresh credentials"},
		"export bad JSON":      {stdinFor("op@example.com", ""), []string{"GWS_EXPORT=bad-json"}, "good", "gws auth export answered with no refresh credentials"},
		"export weird":         {stdinFor("op@example.com", ""), []string{"GWS_EXPORT=weird-chars"}, "good", "what gws answered is no refresh credentials"},
		"token refused":        {stdinFor("op@example.com", ""), nil, "bad-request", "minting a token was answered 400"},
		"token echoes request": {stdinFor("op@example.com", ""), nil, "echo-request", "minting a token was answered 400"},
		"token failing":        {stdinFor("op@example.com", ""), nil, "error", "minting a token was answered 500"},
		"token bad JSON":       {stdinFor("op@example.com", ""), nil, "bad-json", "minting a token came back with no token"},
		"token no token":       {stdinFor("op@example.com", ""), nil, "no-token", "minting a token came back with no token"},
		"token weird":          {stdinFor("op@example.com", ""), nil, "weird-token", "the token Google answered is no token"},
	} {
		endpoint.setMode(tc.mode)
		stdout, stderr, err := mint(t, tc.stdin, srv.URL, tc.env...)
		if err == nil || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: %v\n%s", name, err, stderr)
		}
		if !strings.HasPrefix(stderr, "google-mint: ") {
			t.Errorf("%s names no command: %q", name, stderr)
		}
		if stdout != "" {
			t.Errorf("%s prints %q", name, stdout)
		}
		for _, secret := range []string{fakeClientID, fakeClientSecret, fakeRefresh, fakeAccess} {
			if strings.Contains(stderr, secret) || strings.Contains(stdout, secret) {
				t.Errorf("%s shows %q", name, secret)
			}
		}
		// What gws said on a failed export is not repeated: on that
		// line the credentials themselves would be the leak.
		if name == "export fails" && strings.Contains(stderr, "not logged in") {
			t.Errorf("a failed export repeats gws: %q", stderr)
		}
		// A response body is untrusted and could echo the form's secrets;
		// only the status code is reported.
		if (name == "token refused" || name == "token echoes request") && strings.Contains(stderr, "invalid_grant") {
			t.Errorf("a refused exchange repeated the response body: %q", stderr)
		}
	}
}

func TestGoogleMintRejectsRemoteTokenEndpoint(t *testing.T) {
	stdout, stderr, err := mint(t, stdinFor("op@example.com", ""), "https://attacker.example/token")
	if err == nil || !strings.Contains(stderr, "GOOGLE_TOKEN_ENDPOINT is only allowed for a loopback test endpoint") {
		t.Fatalf("err = %v\n%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, secret := range []string{fakeClientID, fakeClientSecret, fakeRefresh, fakeAccess} {
		if strings.Contains(stderr, secret) {
			t.Fatalf("stderr shows %q", secret)
		}
	}
}

// With no endpoint the exchange fails as unreachable, naming curl's
// exit, and still prints no secret.
func TestGoogleMintUnreachable(t *testing.T) {
	stdout, stderr, err := mint(t, stdinFor("op@example.com", ""), "http://127.0.0.1:1")
	if err == nil || !strings.Contains(stderr, "could not reach http://127.0.0.1:1 (curl exit 7)") {
		t.Fatalf("err = %v\n%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, secret := range []string{fakeClientID, fakeClientSecret, fakeRefresh, fakeAccess} {
		if strings.Contains(stderr, secret) {
			t.Fatalf("stderr shows %q", secret)
		}
	}
}

// Each google pack carries its own google-mint, since a pack is applied
// alone and a command's program is a file inside its pack; the four are
// one script, so a fix to one is a fix to all. Every declaration of the
// four packs mints with it (yardr-66qd).
func TestGoogleMintCopiesAgree(t *testing.T) {
	want, err := fs.ReadFile(packs.FS, "google-mail/google-mint")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"google-drive", "google-docs", "google-calendar"} {
		got, err := fs.ReadFile(packs.FS, p+"/google-mint")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s/google-mint differs from google-mail's: the copies are one script", p)
		}
	}
	for _, file := range []string{"google-mail/google-mail.yaml", "google-drive/google-drive.yaml", "google-docs/google-docs.yaml", "google-docs/google-slides.yaml", "google-calendar/google-calendar.yaml"} {
		raw, err := fs.ReadFile(packs.FS, file)
		if err != nil {
			t.Fatal(err)
		}
		d, err := source.Parse(file, raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Credential.MintCommand; len(got) != 1 || got[0] != "google-mint" {
			t.Errorf("%s: credential.mint_command = %q, want [google-mint]", file, got)
		}
	}
}

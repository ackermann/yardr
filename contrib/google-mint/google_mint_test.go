// Package googlemint tests contrib/google-mint/google-mint, the
// mint_command of the google pack's sources, against a fake gws on PATH
// and a fake token endpoint. No test reaches Google.
package googlemint

import (
	"bytes"
	"encoding/json"
	"io"
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
// script runs, steered by GWS_* variables: GWS_USER (the login's
// account, op@example.com), GWS_STATUS_EXIT and GWS_EXPORT_EXIT (a
// failing command), GWS_STATUS_BODY (no-user, bad-json) and GWS_EXPORT
// (no-refresh, bad-json, weird-chars). Like the real one it notes the
// keyring backend on stderr, which the script passes over.
const fakeGWS = `#!/bin/sh
echo "Using keyring backend: keyring" >&2
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  if [ "${GWS_STATUS_EXIT:-0}" != "0" ]; then echo "Error: fake status failed" >&2; exit 1; fi
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
	case "bad-json":
		io.WriteString(w, `{`)
	case "no-token":
		io.WriteString(w, `{"token_type":"Bearer"}`)
	case "weird-token":
		io.WriteString(w, `{"access_token":"has space","expires_in":3599}`)
	default:
		io.WriteString(w, `{"access_token":"`+fakeAccess+`","expires_in":3599,"scope":"openid","token_type":"Bearer"}`)
	}
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

// mint runs the script with stdin, in the environment the core runs a
// mint_command with (PATH, HOME and nothing else of the yard's), plus
// the endpoint and the fake's steering. Extra steering goes in env.
func mint(t *testing.T, stdin, endpoint string, env ...string) (string, string, error) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gws"), []byte(fakeGWS), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("./google-mint")
	cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GOOGLE_TOKEN_ENDPOINT=" + endpoint}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
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

// A gws login for another account than the inputs name is refused, not
// minted: the endpoint hears nothing, and stdout stays empty.
func TestGoogleMintWrongAccount(t *testing.T) {
	endpoint := &fakeToken{t: t}
	srv := httptest.NewServer(http.HandlerFunc(endpoint.handler))
	defer srv.Close()
	stdout, stderr, err := mint(t, stdinFor("someone@example.com", ""), srv.URL)
	if err == nil || !strings.Contains(stderr, "the gws login is for op@example.com, not GOOGLE_ACCOUNT=someone@example.com") {
		t.Fatalf("err = %v\n%s", err, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(endpoint.sent()) != 0 {
		t.Fatal("the endpoint was asked for a token it must not mint")
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
	cmd := exec.Command("./google-mint")
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
		"no account":        {`{"inputs":{},"scope":""}`, nil, "good", "its inputs name no GOOGLE_ACCOUNT"},
		"no scope":          {`{"inputs":{"GOOGLE_ACCOUNT":"op@example.com"}}`, nil, "good", "got no scope"},
		"no JSON":           {`{`, nil, "good", "its inputs name no GOOGLE_ACCOUNT"},
		"nothing":           {``, nil, "good", "got nothing"},
		"no gws":            {stdinFor("op@example.com", ""), []string{"GWS_BIN=/nonexistent/gws"}, "good", "GWS_BIN=/nonexistent/gws is no program to run"},
		"status fails":      {stdinFor("op@example.com", ""), []string{"GWS_STATUS_EXIT=1"}, "good", "gws auth status failed (exit 1)"},
		"status no user":    {stdinFor("op@example.com", ""), []string{"GWS_STATUS_BODY=no-user"}, "good", "gws auth status answered with no user"},
		"status bad JSON":   {stdinFor("op@example.com", ""), []string{"GWS_STATUS_BODY=bad-json"}, "good", "gws auth status answered with no user"},
		"export fails":      {stdinFor("op@example.com", ""), []string{"GWS_EXPORT_EXIT=1"}, "good", "gws auth export failed (exit 1)"},
		"export no refresh": {stdinFor("op@example.com", ""), []string{"GWS_EXPORT=no-refresh"}, "good", "gws auth export answered with no refresh credentials"},
		"export bad JSON":   {stdinFor("op@example.com", ""), []string{"GWS_EXPORT=bad-json"}, "good", "gws auth export answered with no refresh credentials"},
		"export weird":      {stdinFor("op@example.com", ""), []string{"GWS_EXPORT=weird-chars"}, "good", "what gws answered is no refresh credentials"},
		"token refused":     {stdinFor("op@example.com", ""), nil, "bad-request", "minting a token was answered 400"},
		"token failing":     {stdinFor("op@example.com", ""), nil, "error", "minting a token was answered 500"},
		"token bad JSON":    {stdinFor("op@example.com", ""), nil, "bad-json", "minting a token came back with no token"},
		"token no token":    {stdinFor("op@example.com", ""), nil, "no-token", "minting a token came back with no token"},
		"token weird":       {stdinFor("op@example.com", ""), nil, "weird-token", "the token Google answered is no token"},
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
		// The refusal of the token endpoint carries Google's error,
		// which holds no secret.
		if name == "token refused" && !strings.Contains(stderr, "invalid_grant") {
			t.Errorf("a refused exchange names no error: %q", stderr)
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

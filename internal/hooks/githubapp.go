package hooks

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"yardr.dev/yardr/internal/config"
)

// A crew member speaks on GitHub with a personal token (GITHUB_TOKEN) or as a
// GitHub App, whose comments appear under the App's bot name (<slug>[bot])
// and not under a person's account. An App has no token of its own: it
// signs a short-lived JWT with its private key, asks which installation
// covers the repository, and is given an installation access token that
// lives an hour. So the credential of an App is not a secret but where one
// is: the App's id and the path of its key. That is what Credential and
// CredentialFrom return for it, and every GitHub call (githubCall) turns
// it into a token for the repository it is about to ask, here and nowhere
// else.

// The names a GitHub credential goes by in a crew member's env_file.
const (
	githubTokenName  = "GITHUB_TOKEN"
	githubAppIDName  = "GITHUB_APP_ID"
	githubAppKeyName = "GITHUB_APP_PRIVATE_KEY_FILE"
)

// githubAppScheme starts the credential of an App: github-app:<id>:<key
// file>. No token GitHub issues starts so.
const githubAppScheme = "github-app:"

// githubAppID is what GITHUB_APP_ID may hold: the App's numeric id or its
// client id (Iv23li...). GitHub takes either as the JWT's issuer.
var githubAppID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// githubCredential is the GitHub credential the names get gives stand for:
// the App pair when it is set, else GITHUB_TOKEN. One of the pair without
// the other is refused, and not passed over for the token: a crew member meant
// to answer as the App would answer as a person.
func githubCredential(get func(string) string) (string, error) {
	id, key := strings.TrimSpace(get(githubAppIDName)), strings.TrimSpace(get(githubAppKeyName))
	switch {
	case id != "" && key != "":
		if !githubAppID.MatchString(id) {
			return "", fmt.Errorf("%s is not an App's id or client id", githubAppIDName)
		}
		// The daemon and a pane read the same line from different
		// directories, so the path may not depend on one.
		if !strings.HasPrefix(key, "/") && key != "~" && !strings.HasPrefix(key, "~/") {
			return "", fmt.Errorf("%s is %s: want an absolute path, or one from ~/", githubAppKeyName, key)
		}
		return githubAppScheme + id + ":" + key, nil
	case id != "":
		return "", fmt.Errorf("%s is set without %s: a GitHub App takes both", githubAppIDName, githubAppKeyName)
	case key != "":
		return "", fmt.Errorf("%s is set without %s: a GitHub App takes both", githubAppKeyName, githubAppIDName)
	}
	if tok := strings.TrimSpace(get(githubTokenName)); tok != "" {
		return tok, nil
	}
	return "", &MissingCredential{Names: githubAppIDName + " with " + githubAppKeyName + " (a GitHub App), or " + githubTokenName + " (a personal token)"}
}

// githubTokenLeft is how long before an installation token expires it is
// no longer used: a call made with it must still be answered within its
// life (ReplyTimeout), with room for a clock that disagrees.
const githubTokenLeft = 5 * time.Minute

// githubNow is the clock tokens are dated and aged by; tests move it.
var githubNow = time.Now

type githubInstallationToken struct {
	token   string
	expires time.Time
}

// githubTokens are the installation tokens in hand, by App, key file and
// repository. They are kept in this process's memory only: never in the
// store, an event or a log, and a new process asks for its own.
var githubTokens = struct {
	sync.Mutex
	m map[string]githubInstallationToken
}{m: map[string]githubInstallationToken{}}

// githubCall is call for GitHub's API as the holder of cred: a token as it
// is, an App as an installation token for the repository the URL is in.
// Every GitHub call yardr makes for a crew member goes through it.
func githubCall(ctx context.Context, method, url, cred string, in, out any) error {
	app, isApp := strings.CutPrefix(cred, githubAppScheme)
	if !isApp {
		return call(ctx, method, url, githubHeader(cred), in, out)
	}
	id, keyFile, _ := strings.Cut(app, ":")
	repo := githubRepoOf(url)
	if repo == "" {
		return fmt.Errorf("%s names no repository, and a GitHub App speaks through its installation on one", url)
	}
	cacheKey := id + "\x00" + keyFile + "\x00" + strings.ToLower(repo)
	// Held while the token is asked for, so two calls at once ask once.
	githubTokens.Lock()
	t, ok := githubTokens.m[cacheKey]
	if !ok || !githubNow().Before(t.expires.Add(-githubTokenLeft)) {
		var err error
		if t, err = githubInstall(ctx, id, keyFile, repo); err != nil {
			delete(githubTokens.m, cacheKey)
			githubTokens.Unlock()
			return err
		}
		githubTokens.m[cacheKey] = t
	}
	githubTokens.Unlock()
	err := call(ctx, method, url, githubHeader(t.token), in, out)
	// A token GitHub no longer takes (the App was suspended or removed, the
	// key revoked) is not offered again: the next call asks for a new one.
	if se := (*statusError)(nil); errors.As(err, &se) && se.code == http.StatusUnauthorized {
		githubTokens.Lock()
		if githubTokens.m[cacheKey] == t {
			delete(githubTokens.m, cacheKey)
		}
		githubTokens.Unlock()
	}
	return err
}

// githubRepoOf is the owner/repo a URL of GitHub's API is about: what
// follows /repos/.
func githubRepoOf(url string) string {
	rest, ok := strings.CutPrefix(url, GitHubAPI+"/repos/")
	if !ok {
		return ""
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	repo, _, _ := strings.Cut(parts[0]+"/"+parts[1], "?")
	if !githubRepoName.MatchString(repo) {
		return ""
	}
	return repo
}

// githubInstall asks GitHub, as the App, for an installation token for
// repo: which installation covers the repository, then a token of it,
// narrowed to that repository.
func githubInstall(ctx context.Context, id, keyFile, repo string) (githubInstallationToken, error) {
	var none githubInstallationToken
	jwt, err := githubAppJWT(id, keyFile)
	if err != nil {
		return none, err
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	err = call(ctx, http.MethodGet, GitHubAPI+"/repos/"+repo+"/installation", githubHeader(jwt), nil, &inst)
	if se := (*statusError)(nil); errors.As(err, &se) && se.code == http.StatusNotFound {
		return none, fmt.Errorf("the GitHub App %s is not installed on %s: install the App there (the App's page on GitHub, Install App, and select the repository)", id, repo)
	}
	if err != nil {
		return none, fmt.Errorf("as GitHub App %s, finding its installation on %s: %w", id, repo, err)
	}
	if inst.ID <= 0 {
		return none, fmt.Errorf("as GitHub App %s, finding its installation on %s: the answer names none", id, repo)
	}
	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	_, name, _ := strings.Cut(repo, "/")
	err = call(ctx, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", GitHubAPI, inst.ID), githubHeader(jwt),
		map[string]any{"repositories": []string{name}}, &tok)
	if err != nil {
		return none, fmt.Errorf("as GitHub App %s, asking for an installation token for %s: %w", id, repo, err)
	}
	if tok.Token == "" || tok.ExpiresAt.IsZero() {
		return none, fmt.Errorf("as GitHub App %s, asking for an installation token for %s: the answer holds no token with an expiry", id, repo)
	}
	return githubInstallationToken{token: tok.Token, expires: tok.ExpiresAt}, nil
}

// githubJWTLife is how long the App's JWT is good for. GitHub takes at most
// ten minutes; it is used for the two calls that follow and thrown away.
const githubJWTLife = 9 * time.Minute

// githubAppJWT is the JWT the App authenticates as itself with: RS256,
// issued by the App's id, dated a minute back for a clock ahead of
// GitHub's.
func githubAppJWT(id, keyFile string) (string, error) {
	key, err := githubAppKey(keyFile)
	if err != nil {
		return "", err
	}
	now := githubNow()
	enc := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signed := enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." +
		enc(map[string]any{"iss": id, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(githubJWTLife).Unix()})
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("%s: signing as the App failed", githubAppKeyName)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// githubAppKey reads the App's private key, under the rule every file yardr
// reads a secret from is held to (config.ReadSecretFile). GitHub hands the
// key out as PEM, PKCS #1; PKCS #8 is taken too. Errors name the file,
// never what is in it.
func githubAppKey(keyFile string) (*rsa.PrivateKey, error) {
	raw, path, err := config.ReadSecretFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", githubAppKeyName, err)
	}
	notKey := fmt.Errorf("%s: %s holds no RSA private key in PEM, as GitHub generates an App's", githubAppKeyName, path)
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, notKey
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	any8, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, notKey
	}
	key, ok := any8.(*rsa.PrivateKey)
	if !ok {
		return nil, notKey
	}
	return key, nil
}

// CheckGitHubAppKey reports why the App key at keyFile could not be signed
// with, for a check that reads what a crew member has without calling GitHub.
func CheckGitHubAppKey(keyFile string) error {
	_, err := githubAppKey(keyFile)
	return err
}

// CheckKey is why the App's key file cannot be signed with, when the
// credential get reads is the App's; nil for a token, which has none.
func (GitHub) CheckKey(get func(name string) string) error {
	key := strings.TrimSpace(get(githubAppKeyName))
	if key == "" || strings.TrimSpace(get(githubAppIDName)) == "" {
		return nil
	}
	return CheckGitHubAppKey(key)
}

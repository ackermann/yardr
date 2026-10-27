package packs_test

// These test git-credential, the git credential helper packs/github brings
// (git.credential in its declaration): the copy an import makes under
// <home>/sources/github/, driven as git drives it. The helper is copied
// into a fake yard home, with a fake source env file, and run directly
// with git's request on stdin; one test runs it through git credential
// fill in a test depot instead. No test reaches the network, and the
// tokens are fake.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// githubHelper is the helper as the pack ships it, run from this directory.
const githubHelper = "./github/github/git-credential"

// Fake tokens, in no provider's format: what the helper must print only to
// git on stdout, and never in an error, a file or a config.
const (
	helperToken  = "test-token-aaa-1"
	helperToken2 = "test-token-bbb-2"
	decoyToken   = "test-token-decoy-0"
)

// helperHome plants a fake yard home: the source's env file with content
// ("" for none), and the helper copied to
// sources/github/github/git-credential as an import copies it, mode 0700.
// It returns the home and the copy's path.
func helperHome(t *testing.T, content string) (home, helper string) {
	t.Helper()
	home = t.TempDir()
	raw, err := os.ReadFile(githubHelper)
	if err != nil {
		t.Fatal(err)
	}
	helper = filepath.Join(home, "sources", "github", "github", "git-credential")
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(home, "sources", "github.env"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, helper
}

// runHelper runs the helper with op and git's request on stdin, under env
// with PATH and a bare HOME. It returns what the helper printed and the
// error of the run.
func runHelper(t *testing.T, helper, op, stdin string, env ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(helper, op)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// The helper answers a get for github.com with x-access-token and the
// token, and the token is in nothing but that answer.
func TestGitHubCredentialAnswersGitHub(t *testing.T) {
	_, helper := helperHome(t, "GITHUB_TOKEN="+helperToken+"\n")
	stdout, stderr, err := runHelper(t, helper, "get", "protocol=https\nhost=github.com\n\n")
	if err != nil {
		t.Fatalf("get: %v, stderr %q", err, stderr)
	}
	if stdout != "username=x-access-token\npassword="+helperToken+"\n" {
		t.Fatalf("get stdout = %q", stdout)
	}
	if strings.Contains(stderr, helperToken) {
		t.Fatalf("get stderr holds the token: %q", stderr)
	}
}

// For any other host or protocol the helper answers nothing: no output,
// exit 0, so git asks the next helper.
func TestGitHubCredentialIgnoresOtherHosts(t *testing.T) {
	_, helper := helperHome(t, "GITHUB_TOKEN="+helperToken+"\n")
	for _, c := range []struct {
		name  string
		stdin string
	}{
		{"another host", "protocol=https\nhost=example.com\n\n"},
		{"the api host", "protocol=https\nhost=api.github.com\n\n"},
		{"plain http", "protocol=http\nhost=github.com\n\n"},
		{"another protocol", "protocol=ssh\nhost=github.com\n\n"},
		{"no host", "protocol=https\n\n"},
		{"no protocol", "host=github.com\n\n"},
		{"nothing", "\n"},
	} {
		stdout, stderr, err := runHelper(t, helper, "get", c.stdin)
		if err != nil || stdout != "" {
			t.Errorf("%s: stdout = %q, err = %v, stderr %q", c.name, stdout, err, stderr)
		}
		if strings.Contains(stdout+stderr, helperToken) {
			t.Errorf("%s: output holds the token", c.name)
		}
	}
}

// store and erase change nothing: the env file's bytes are the same
// afterwards, and no file is added anywhere under the home.
func TestGitHubCredentialStoreEraseChangeNothing(t *testing.T) {
	home, helper := helperHome(t, "# the source's token\nGITHUB_TOKEN="+helperToken+"\n")
	envFile := filepath.Join(home, "sources", "github.env")
	before, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(home, path)
			listed = append(listed, rel)
		}
		return nil
	})
	for _, op := range []string{"store", "erase"} {
		stdout, stderr, err := runHelper(t, helper, op, "protocol=https\nhost=github.com\nusername=x-access-token\npassword="+helperToken+"\n\n")
		if err != nil || stdout != "" {
			t.Errorf("%s: stdout = %q, err = %v, stderr %q", op, stdout, err, stderr)
		}
		if after, _ := os.ReadFile(envFile); !bytes.Equal(after, before) {
			t.Errorf("%s: env file changed to %q", op, after)
		}
		var now []string
		filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				rel, _ := filepath.Rel(home, path)
				now = append(now, rel)
			}
			return nil
		})
		if strings.Join(now, "\n") != strings.Join(listed, "\n") {
			t.Errorf("%s: files under the home changed: %q", op, now)
		}
	}
}

// Without a token to give the helper answers nothing, with the reason on
// stderr: an App-only env file, a missing file, a malformed line. The
// reason never holds the token.
func TestGitHubCredentialWithoutToken(t *testing.T) {
	_, helper := helperHome(t, "GITHUB_APP_ID=123\nGITHUB_APP_PRIVATE_KEY_FILE=/keys/app.pem\n")
	stdout, stderr, err := runHelper(t, helper, "get", "protocol=https\nhost=github.com\n\n")
	if err != nil || stdout != "" {
		t.Fatalf("App-only: stdout = %q, err = %v, stderr %q", stdout, err, stderr)
	}
	if !strings.Contains(stderr, "GITHUB_TOKEN is not set") {
		t.Fatalf("App-only stderr = %q", stderr)
	}
	_, missing := helperHome(t, "")
	stdout, stderr, err = runHelper(t, missing, "get", "protocol=https\nhost=github.com\n\n")
	if err != nil || stdout != "" {
		t.Fatalf("missing file: stdout = %q, err = %v, stderr %q", stdout, err, stderr)
	}
	if !strings.Contains(stderr, "GITHUB_TOKEN is not set") {
		t.Fatalf("missing file stderr = %q", stderr)
	}
	_, broken := helperHome(t, "GITHUB_TOKEN="+helperToken+"\nnot a line\n")
	stdout, stderr, err = runHelper(t, broken, "get", "protocol=https\nhost=github.com\n\n")
	if err != nil || stdout != "" {
		t.Fatalf("malformed file: stdout = %q, err = %v, stderr %q", stdout, err, stderr)
	}
	if !strings.Contains(stderr, "line 2") || strings.Contains(stderr, helperToken) {
		t.Fatalf("malformed file stderr = %q", stderr)
	}
}

// The env file is read as the core reads it: the last GITHUB_TOKEN line
// wins, surrounding whitespace is off, and an empty value is as good as
// unset.
func TestGitHubCredentialReadsEnvLikeTheCore(t *testing.T) {
	_, helper := helperHome(t, "  # indented, and spaces around the =\n"+
		"GITHUB_TOKEN = "+helperToken+"  \n"+
		"GITHUB_TOKEN="+helperToken2+"\n")
	stdout, _, err := runHelper(t, helper, "get", "protocol=https\nhost=github.com\n\n")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stdout != "username=x-access-token\npassword="+helperToken2+"\n" {
		t.Fatalf("get stdout = %q", stdout)
	}
	_, empty := helperHome(t, "GITHUB_TOKEN="+helperToken+"\nGITHUB_TOKEN=   \n")
	if stdout, _, err := runHelper(t, empty, "get", "protocol=https\nhost=github.com\n\n"); err != nil || stdout != "" {
		t.Fatalf("emptied token: stdout = %q, err = %v", stdout, err)
	}
}

// The helper finds the yard's home from its own copied path, whatever the
// environment says; run from anywhere else it takes YARDR_HOME, else the
// default home.
func TestGitHubCredentialFindsItsHome(t *testing.T) {
	_, helper := helperHome(t, "GITHUB_TOKEN="+helperToken+"\n")
	decoy, _ := helperHome(t, "GITHUB_TOKEN="+decoyToken+"\n")
	stdout, _, err := runHelper(t, helper, "get", "protocol=https\nhost=github.com\n\n", "YARDR_HOME="+decoy)
	if err != nil {
		t.Fatalf("copied helper with YARDR_HOME set: %v", err)
	}
	if !strings.Contains(stdout, helperToken) || strings.Contains(stdout, decoyToken) {
		t.Fatalf("copied helper answered from the environment: %q", stdout)
	}
	// Run in place, as no import copied it: YARDR_HOME wins over HOME.
	home, _ := helperHome(t, "GITHUB_TOKEN="+helperToken+"\n")
	user := t.TempDir()
	if err := os.MkdirAll(filepath.Join(user, ".yardr", "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(user, ".yardr", "sources", "github.env"), []byte("GITHUB_TOKEN="+decoyToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(githubHelper, "get")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + user, "YARDR_HOME=" + home}
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	var stdoutBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("in place with YARDR_HOME: %v", err)
	}
	if !strings.Contains(stdoutBuf.String(), helperToken) {
		t.Fatalf("in place answered %q, want the YARDR_HOME token", stdoutBuf.String())
	}
	cmd = exec.Command(githubHelper, "get")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + user}
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	var stderrBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdoutBuf, &stderrBuf
	stdoutBuf.Reset()
	if err := cmd.Run(); err != nil {
		t.Fatalf("in place with HOME alone: %v, stderr %q", err, stderrBuf.String())
	}
	if !strings.Contains(stdoutBuf.String(), decoyToken) {
		t.Fatalf("in place answered %q, want the default home's token", stdoutBuf.String())
	}
}

// Through git itself: credential fill in a depot whose origin is on
// github.com and whose config names the helper answers the token; the
// token stands in nothing the depot keeps, and a token changed in the
// source env file is the one the next fill answers.
func TestGitHubCredentialFill(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed, and the fill runs through it")
	}
	home, helper := helperHome(t, "GITHUB_TOKEN="+helperToken+"\n")
	depot := t.TempDir()
	unset := t.TempDir()
	env := append(os.Environ(),
		"HOME="+unset,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	git := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = depot
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err != nil {
			t.Fatalf("git %s: %v, stderr %q", strings.Join(args, " "), err, stderr.String())
		}
		return stdout.String(), nil
	}
	git("init", "-q")
	git("remote", "add", "origin", "https://github.com/acme/webapp.git")
	git("config", `credential.https://github.com.helper`, helper)
	fill := func(stdin string) (string, string, error) {
		t.Helper()
		cmd := exec.Command("git", "credential", "fill")
		cmd.Dir = depot
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	out, fillErr, err := fill("protocol=https\nhost=github.com\n\n")
	if err != nil {
		t.Fatalf("fill: %v, stderr %q", err, fillErr)
	}
	if !strings.Contains(out, "username=x-access-token\n") || !strings.Contains(out, "password="+helperToken+"\n") {
		config, _ := os.ReadFile(filepath.Join(depot, ".git", "config"))
		t.Fatalf("fill answered %q, stderr %q, config %q", out, fillErr, config)
	}
	config, err := os.ReadFile(filepath.Join(depot, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), helper) {
		t.Fatalf(".git/config names no helper: %q", config)
	}
	if strings.Contains(string(config), helperToken) {
		t.Fatalf(".git/config holds the token: %q", config)
	}
	url, _ := git("remote", "get-url", "origin")
	if strings.TrimSpace(url) != "https://github.com/acme/webapp.git" {
		t.Fatalf("origin is %q", url)
	}
	// A token changed in the source env file is the one the next fill
	// answers: nothing is re-turned-on.
	if err := os.WriteFile(filepath.Join(home, "sources", "github.env"), []byte("GITHUB_TOKEN="+helperToken2+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, fillErr, err = fill("protocol=https\nhost=github.com\n\n")
	if err != nil {
		t.Fatalf("fill after rotation: %v, stderr %q", err, fillErr)
	}
	if !strings.Contains(out, "password="+helperToken2+"\n") || strings.Contains(out, helperToken+"\n") {
		t.Fatalf("fill after rotation answered %q", out)
	}
	// Another host is answered with nothing: git finds no credential.
	out, _, err = fill("protocol=https\nhost=example.com\n\n")
	if err == nil {
		t.Fatalf("fill for another host passed: %q", out)
	}
	if strings.Contains(out, "username=") || strings.Contains(out, "password=") {
		t.Fatalf("fill for another host answered %q", out)
	}
}

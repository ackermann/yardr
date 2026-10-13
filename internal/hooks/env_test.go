package hooks_test

import (
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/scripttest"
)

// The tests run in a pane of the yard: a builder's, or a crew member's,
// which names it (YARDR_CREW, and YARDR_RESIDENT beside it for one release)
// and carries the credentials of its env_file. A test that read them passed
// in one pane and failed in another: TestCredentials with a GitHub App in
// the environment, TestGitHubCredentialOfACrew with YARDR_RESIDENT, which
// neither blanked (yardr-n1v5). So every run of this package is in a pane
// that says the wrong thing under each name the package reads, and a test
// sets what it needs (asCrew, noGitHubCredential, t.Setenv).
func TestMain(m *testing.M) {
	for _, name := range []string{"CREW", "ACTOR", "SESSION", "BEAD"} {
		for _, kv := range config.Env(name, "not-this-test") {
			k, v, _ := strings.Cut(kv, "=")
			os.Setenv(k, v)
		}
	}
	// No yard is here, and none can be made here.
	for _, kv := range config.Env("HOME", "/nonexistent/not-this-tests-yard") {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
	}
	// An App id that is none and a key file that is not there: a call made
	// with what the environment happens to hold fails instead of passing.
	os.Setenv("GITHUB_TOKEN", "ghp_not_this_tests")
	os.Setenv("GITHUB_APP_ID", "not this test's")
	os.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", "/nonexistent/not-this-tests.pem")
	os.Setenv("LINEAR_API_KEY", "lin_api_not_this_tests")
	code := m.Run()
	scripttest.Remove()
	os.Exit(code)
}

// asCrew makes the test's environment the pane of crew member name, or with
// "" a person's own shell, under every name the crew member is read by: one
// left out is one the pane the tests run in answers for.
func asCrew(t *testing.T, name string) {
	t.Helper()
	for _, kv := range config.Env("CREW", name) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

// noGitHubCredential leaves the test's environment with neither a GitHub
// App nor a token; the test sets the one it is about.
func noGitHubCredential(t *testing.T) {
	t.Helper()
	for _, name := range []string{"GITHUB_TOKEN", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_FILE"} {
		t.Setenv(name, "")
	}
}

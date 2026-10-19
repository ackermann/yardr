package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/version"
)

// yardr --version is what a person or an agent tries first on a new
// install: it prints what yardr version prints, as text and as JSON, on a
// home with no yard and no terminal, and creates nothing there (yardr-arys).
func TestVersionFlagPrintsWhatVersionDoes(t *testing.T) {
	user := noHome(t)
	requireNonInteractive(t)
	ask := func(args ...string) string {
		t.Helper()
		out, code := executeOut(&app{}, args...)
		if code != 0 {
			t.Fatalf("yardr %s: exit %d\n%s", strings.Join(args, " "), code, out)
		}
		return out
	}
	want := ask("version")
	if want != version.Describe()+"\n" {
		t.Fatalf("yardr version = %q, want %q", want, version.Describe()+"\n")
	}
	for _, flag := range []string{"--version", "-v"} {
		if got := ask(flag); got != want {
			t.Errorf("yardr %s = %q, yardr version = %q", flag, got, want)
		}
	}
	wantJSON := ask("version", "--json")
	if !strings.Contains(wantJSON, `"revision"`) {
		t.Fatalf("yardr version --json:\n%s", wantJSON)
	}
	if got := ask("--version", "--json"); got != wantJSON {
		t.Errorf("yardr --version --json = %q, yardr version --json = %q", got, wantJSON)
	}
	if _, err := os.Stat(filepath.Join(user, ".yardr")); !os.IsNotExist(err) {
		t.Errorf("asking for the version created the home: %v", err)
	}

	// The flag is bare yardr's alone: under a command it stays unknown,
	// free for what that command may mean by it.
	var errs strings.Builder
	if code := execute(&app{stderr: &errs}, []string{"doctor", "--version"}); code == 0 || !strings.Contains(errs.String(), "unknown flag: --version") {
		t.Errorf("yardr doctor --version: exit %d, %q", code, errs.String())
	}
}

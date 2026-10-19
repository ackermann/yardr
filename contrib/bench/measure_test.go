package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bench's depots are on main whatever branch git starts a repository
// on. On a machine whose git starts on master, depot add --base main
// refused the checkout and the bench never began (yardr-h8eb); that
// machine is played here by a git config of its own.
func TestInitDepotRepoIsOnMain(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[init]\n\tdefaultBranch = master\n[user]\n\tname = bench\n\temail = bench@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := filepath.Join(t.TempDir(), "depot")
	if err := initDepotRepo(dir); err != nil {
		t.Fatal(err)
	}
	out, err := run("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	if got := strings.TrimSpace(out); err != nil || got != "main" {
		t.Fatalf("the depot's repository is on %q (%v), want main", got, err)
	}
}

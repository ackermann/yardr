package depotfile

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A worktree's git path is absolute and inside its git directory, and the
// call is bounded: a wedged git fails it instead of hanging it.
func TestGitPath(t *testing.T) {
	repo, _ := baseRepo(t)
	got, err := GitPath(context.Background(), repo, "yardr")
	if want := filepath.Join(repo, ".git", "yardr"); err != nil || got != want {
		t.Fatalf("GitPath = %q, %v; want %q", got, err, want)
	}
	old := GitTimeout
	GitTimeout = time.Nanosecond
	defer func() { GitTimeout = old }()
	if _, err := GitPath(context.Background(), repo, "yardr"); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("GitPath with git timing out = %v", err)
	}
}

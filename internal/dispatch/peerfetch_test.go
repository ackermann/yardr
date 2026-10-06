package dispatch

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

type basesTestStore struct {
	store.API
	rig   store.Rig
	bases []store.PeerBase
}

func (s basesTestStore) PeerBases(context.Context) ([]store.PeerBase, error) {
	return s.bases, nil
}

func (s basesTestStore) Rigs(context.Context) ([]store.Rig, error) {
	return []store.Rig{s.rig}, nil
}

func TestBasesRetriesAfterRigBecomesAvailable(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "repo")
	rig := store.Rig{Name: "web", Path: dir, Kind: store.RigGit, Base: "main"}
	commit := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	api := basesTestStore{rig: rig, bases: []store.PeerBase{{Bead: "web-1", Rig: rig, Commit: ""}}}
	d := &Dispatcher{Store: api, Log: slog.Default()}
	d.bases(ctx) // Git cannot update the refs while the rig path is absent.
	if len(d.basesKept["web"].is) != 0 {
		t.Fatal("failed ref reconciliation was cached")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "init", "-q", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "base"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit("add", "base")
	commit("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "base")
	base := commit("rev-parse", "HEAD")
	api.bases[0].Commit = strings.TrimSpace(base)
	d.bases(ctx)
	refs := commit("for-each-ref", "--format=%(refname)", store.PeerBaseRef("web-1"))
	if strings.TrimSpace(refs) != store.PeerBaseRef("web-1") {
		t.Fatalf("refs after retry = %q", refs)
	}
}

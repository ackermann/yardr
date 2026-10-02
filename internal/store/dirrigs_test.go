package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A rig is a git rig unless it says otherwise; a dir rig has no base, and
// a kind is one of the two.
func TestRigKinds(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "web", Path: "/x", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetRig(ctx, "web"); err != nil || r.Kind != store.RigGit || r.IsDir() {
		t.Fatalf("rig without a kind = %+v, %v; want git", r, err)
	}
	if err := s.PutRig(ctx, store.Rig{Name: "docs", Path: "/d", Kind: store.RigDir}, "me"); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetRig(ctx, "docs"); err != nil || r.Kind != store.RigDir || !r.IsDir() || r.Base != "" {
		t.Fatalf("dir rig = %+v, %v", r, err)
	}
	err := s.PutRig(ctx, store.Rig{Name: "docs", Path: "/d", Kind: store.RigDir, Base: "main"}, "me")
	if err == nil || !strings.Contains(err.Error(), "has no base") {
		t.Fatalf("dir rig with a base: %v", err)
	}
	if err := s.PutRig(ctx, store.Rig{Name: "x", Path: "/d", Kind: "svn"}, "me"); err == nil || !strings.Contains(err.Error(), `kind "svn"`) {
		t.Fatalf("unknown kind: %v", err)
	}
	evs, _ := s.EventsSince(ctx, 0, 0)
	if last := evs[len(evs)-1]; last.Kind != "rig_updated" || last.Data["kind"] != store.RigDir {
		t.Fatalf("last event = %+v; want rig_updated with the kind", last)
	}
}

// A store written before rig kinds holds rigs without one: they open as git
// rigs, and stay so.
func TestMigrateRigKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		rigs, err := tx.CreateBucket([]byte("rigs"))
		if err != nil {
			return err
		}
		return rigs.Put([]byte("yard"), []byte(`{"name":"yard","path":"/tmp/yard","base":"origin/main"}`))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 2 {
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.GetRig(ctx, "yard")
		s.Close(ctx)
		if err != nil || r.Kind != store.RigGit || r.Base != "origin/main" {
			t.Fatalf("migrated rig = %+v, %v", r, err)
		}
	}
	// The migration wrote the kind to the file, not just a default on read.
	db, err = bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.View(func(tx *bolt.Tx) error {
		if raw := string(tx.Bucket([]byte("rigs")).Get([]byte("yard"))); !strings.Contains(raw, `"kind":"git"`) {
			t.Fatalf("stored rig = %s", raw)
		}
		return nil
	})
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A dir rig's flows are read from its directory as it is (there is no base
// to read them from), and reread when a file changes, even to the same size.
func TestDirRigFlows(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".yardr/flows/default.flow":       repoDefault,
		".yardr/flows/type/decision.flow": repoDecision,
		".yardr/flows/nested/x/y.flow":    repoAlt, // not a place flows are read from
	})
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "docs", Path: dir, Kind: store.RigDir}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Rig: "docs"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CreateBead(ctx, store.NewBead{Title: "q", Rig: "docs", Type: "decision", Stage: "ask"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, b.ID); name != "default" || src != "repo (directory)" {
		t.Fatalf("task: %s (%s)", name, src)
	}
	if name, src := governing(t, s, q.ID); name != "type/decision" || src != "repo (directory), type decision" {
		t.Fatalf("decision: %s (%s)", name, src)
	}
	repo, err := s.RepoFlows(ctx, "docs")
	if err != nil || repo.Dir != dir || repo.Base != "" || repo.Commit != "" || len(repo.Flows) != 2 {
		t.Fatalf("repo flows = %+v, %v", repo, err)
	}
	if _, err := s.Advance(ctx, b.ID, "done", "me"); err == nil {
		t.Fatal("dir flow not enforced")
	}

	// Same size, new content and mtime: the cache must not keep the old.
	flow := filepath.Join(dir, ".yardr/flows/default.flow")
	same := "new -> second\nsecond -> done\ndone terminal\n"
	if len(same) != len(repoDefault) {
		t.Fatalf("test flows differ in size: %d, %d", len(same), len(repoDefault))
	}
	writeFiles(t, dir, map[string]string{".yardr/flows/default.flow": same})
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(flow, later, later); err != nil {
		t.Fatal(err)
	}
	if next, err := s.NextStages(ctx, b.ID); err != nil || len(next.Stages) != 1 || next.Stages[0] != "second" {
		t.Fatalf("after an edit: next = %+v, %v", next, err)
	}

	// A broken flow refuses moves rather than leave beads ungoverned.
	writeFiles(t, dir, map[string]string{".yardr/flows/default.flow": "new => review\n"})
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "t2", Rig: "docs"}, "me"); err == nil || !strings.Contains(err.Error(), ".yardr/flows/default.flow") {
		t.Fatalf("create under a broken dir flow: %v", err)
	}

	// Removing the file leaves the rig's beads ungoverned, as a git rig
	// without flows.
	if err := os.Remove(flow); err != nil {
		t.Fatal(err)
	}
	if name, _ := governing(t, s, b.ID); name != "" {
		t.Fatalf("after removing default.flow: governed by %s", name)
	}
	// A pointer may name a flow only the directory has; a missing one says
	// where it was looked for.
	if err := s.SetFlowPointer(ctx, "docs", "", "ghost", "me"); err == nil || !strings.Contains(err.Error(), "in "+dir) {
		t.Fatalf("pointer to a missing dir flow: %v", err)
	}
	writeFiles(t, dir, map[string]string{".yardr/flows/alt.flow": repoAlt})
	if err := s.SetFlowPointer(ctx, "docs", "", "alt", "me"); err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, b.ID); name != "alt" || src != "store" {
		t.Fatalf("pointer to a dir flow: %s (%s)", name, src)
	}

	// A directory with no .yardr at all has no flows; that is not an error.
	if err := s.PutRig(ctx, store.Rig{Name: "bare", Path: t.TempDir(), Kind: store.RigDir}, "me"); err != nil {
		t.Fatal(err)
	}
	if rf, err := s.RepoFlows(ctx, "bare"); err != nil || len(rf.Flows) != 0 {
		t.Fatalf("bare dir rig: %+v, %v", rf, err)
	}
}

// Epics in a dir rig only group their children: nobody has a base, as
// there are no branches.
func TestDirRigEpics(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "docs", Path: t.TempDir(), Kind: store.RigDir}, "me"); err != nil {
		t.Fatal(err)
	}
	epic, err := s.CreateBead(ctx, store.NewBead{Title: "e", Rig: "docs", Type: store.TypeEpic}, "me")
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateBead(ctx, store.NewBead{Title: "c", Rig: "docs"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, epic.ID, child.ID, store.DepParent, "me"); err != nil {
		t.Fatalf("parent edge in a dir rig: %v", err)
	}
	f, err := s.Family(ctx, child.ID)
	if err != nil || f.Epic != epic.ID || f.Base != "" {
		t.Fatalf("child's family = %+v, %v; want the epic, no base", f, err)
	}
	f, err = s.Family(ctx, epic.ID)
	if err != nil || f.Base != "" || len(f.Children) != 1 || f.Children[0] != child.ID {
		t.Fatalf("epic's family = %+v, %v", f, err)
	}
}

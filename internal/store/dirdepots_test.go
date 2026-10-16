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

// A depot is a git depot unless it says otherwise; a dir depot has no base, and
// a kind is one of the two.
func TestDepotKinds(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: "/x", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetDepot(ctx, "web"); err != nil || r.Kind != store.DepotGit || r.IsDir() {
		t.Fatalf("depot without a kind = %+v, %v; want git", r, err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "docs", Path: "/d", Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetDepot(ctx, "docs"); err != nil || r.Kind != store.DepotDir || !r.IsDir() || r.Base != "" {
		t.Fatalf("dir depot = %+v, %v", r, err)
	}
	err := s.PutDepot(ctx, store.Depot{Name: "docs", Path: "/d", Kind: store.DepotDir, Base: "main"}, "me")
	if err == nil || !strings.Contains(err.Error(), "has no base") {
		t.Fatalf("dir depot with a base: %v", err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "x", Path: "/d", Kind: "svn"}, "me"); err == nil || !strings.Contains(err.Error(), `kind "svn"`) {
		t.Fatalf("unknown kind: %v", err)
	}
	evs, _ := s.EventsSince(ctx, 0, 0)
	if last := evs[len(evs)-1]; last.Kind != "depot_updated" || last.Data["kind"] != store.DepotDir {
		t.Fatalf("last event = %+v; want depot_updated with the kind", last)
	}
}

// A store written before depot kinds holds depots without one: they open as git
// depots, and stay so.
func TestMigrateDepotKinds(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "yard.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		depots, err := tx.CreateBucket([]byte("depots"))
		if err != nil {
			return err
		}
		return depots.Put([]byte("yard"), []byte(`{"name":"yard","path":"/tmp/yard","base":"origin/main"}`))
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
		r, err := s.GetDepot(ctx, "yard")
		s.Close(ctx)
		if err != nil || r.Kind != store.DepotGit || r.Base != "origin/main" {
			t.Fatalf("migrated depot = %+v, %v", r, err)
		}
	}
	// The migration wrote the kind to the file, not just a default on read.
	db, err = bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.View(func(tx *bolt.Tx) error {
		if raw := string(tx.Bucket([]byte("depots")).Get([]byte("yard"))); !strings.Contains(raw, `"kind":"git"`) {
			t.Fatalf("stored depot = %s", raw)
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

// A dir depot's flows are read from its directory as it is (there is no base
// to read them from), and reread when a file changes, even to the same size.
func TestDirDepotFlows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".yardr/flows/default.flow":       repoDefault,
		".yardr/flows/type/decision.flow": repoDecision,
		".yardr/flows/nested/x/y.flow":    repoAlt, // not a place flows are read from
	})
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "docs", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Depot: "docs"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CreateBead(ctx, store.NewBead{Title: "q", Depot: "docs", Type: "decision", Stage: "ask"}, "me")
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
	if _, err := s.Advance(ctx, b.ID, "done", store.Expect{}, "me"); err == nil {
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
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "t2", Depot: "docs"}, "me"); err == nil || !strings.Contains(err.Error(), ".yardr/flows/default.flow") {
		t.Fatalf("create under a broken dir flow: %v", err)
	}

	// Removing the file leaves the depot's beads ungoverned, as a git depot
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
	// The depot pointer would govern the decision too, whose stage ask the
	// flow lacks: it is refused until the decision is out of the way.
	writeFiles(t, dir, map[string]string{".yardr/flows/alt.flow": repoAlt})
	if err := s.SetFlowPointer(ctx, "docs", "", "alt", "me"); err == nil || !strings.Contains(err.Error(), q.ID+" (ask)") {
		t.Fatalf("depot pointer to a dir flow over a decision in ask: %v", err)
	}
	if q, err = s.Advance(ctx, q.ID, "answered", store.Expect{}, "me"); err != nil || q.Status != "closed" {
		t.Fatalf("decision: %v %+v", err, q)
	}
	if err := s.SetFlowPointer(ctx, "docs", "", "alt", "me"); err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, b.ID); name != "alt" || src != "store" {
		t.Fatalf("pointer to a dir flow: %s (%s)", name, src)
	}

	// A directory with no .yardr at all has no flows; that is not an error.
	if err := s.PutDepot(ctx, store.Depot{Name: "bare", Path: t.TempDir(), Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	if rf, err := s.RepoFlows(ctx, "bare"); err != nil || len(rf.Flows) != 0 {
		t.Fatalf("bare dir depot: %+v, %v", rf, err)
	}
}

// Trains in a dir depot only group their children: nobody has a base, as
// there are no branches.
func TestDirDepotTrains(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "docs", Path: t.TempDir(), Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	train, err := s.CreateBead(ctx, store.NewBead{Title: "e", Depot: "docs", Type: store.TypeTrain}, "me")
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateBead(ctx, store.NewBead{Title: "c", Depot: "docs"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, train.ID, child.ID, store.DepParent, "", "", "me"); err != nil {
		t.Fatalf("parent edge in a dir depot: %v", err)
	}
	f, err := s.Family(ctx, child.ID)
	if err != nil || f.Train != train.ID || f.Base != "" {
		t.Fatalf("child's family = %+v, %v; want the train, no base", f, err)
	}
	f, err = s.Family(ctx, train.ID)
	if err != nil || f.Base != "" || len(f.Children) != 1 || f.Children[0] != child.ID {
		t.Fatalf("train's family = %+v, %v", f, err)
	}
}

// A session of a dir depot's bead leaves a brief in the depot's directory, not
// a worktree: once the bead closes it is still found for cleanup.
func TestClosedWithWorkspaceDirDepot(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "docs", Path: "/d", Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for name, handle := range map[string]map[string]any{
		"dir":  {"agent": "a", "dir": "/d", "brief": "/d/.yardr/briefs/brief-s.md"},
		"none": {"agent": "b"},
	} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: name, Depot: "docs", Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRunning(ctx, sess.ID, handle); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkStopped(ctx, sess.ID); err != nil {
			t.Fatal(err)
		}
		ids[name] = sess.ID
	}
	ws, err := s.ClosedWithWorkspace(ctx)
	if err != nil || len(ws) != 1 || ws[0].ID != ids["dir"] {
		t.Fatalf("closed with workspace = %+v, %v; want only %s", ws, err, ids["dir"])
	}
}

// depot rm works on a dir depot as on a git one: the depot, the routes that name
// it and its flow pointers leave the store, and the directory, with its
// .yardr/, stays as it is (there are no worktrees to speak of).
func TestDeleteDirDepot(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	flow := filepath.Join(dir, ".yardr", "flows", "default.flow")
	if err := os.MkdirAll(filepath.Dir(flow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flow, []byte("new -> done\ndone terminal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "docs", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	docs := "docs"
	route, err := s.AddRoute(ctx, store.Route{Stage: "new", Depot: &docs, Group: "g"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("new -> done\ndone terminal")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "named"
	if _, err := s.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "docs", "", "named", "me"); err != nil {
		t.Fatal(err)
	}
	got, err := s.DeleteDepot(ctx, "docs", "me")
	if err != nil || got.Depot != "docs" || len(got.Routes) != 1 || got.Routes[0] != route.ID ||
		len(got.FlowPointers) != 1 || got.FlowPointers[0].String() != "depot docs" {
		t.Fatalf("DeleteDepot = %+v, %v", got, err)
	}
	if _, err := s.GetDepot(ctx, "docs"); err == nil {
		t.Fatal("dir depot still in the store")
	}
	if rs, _ := s.Routes(ctx); len(rs) != 0 {
		t.Fatalf("routes left: %+v", rs)
	}
	if ps, _ := s.FlowPointers(ctx); len(ps) != 0 {
		t.Fatalf("flow pointers left: %+v", ps)
	}
	if b, err := os.ReadFile(flow); err != nil || string(b) != "new -> done\ndone terminal\n" {
		t.Fatalf("the directory's .yardr changed: %q, %v", b, err)
	}
}

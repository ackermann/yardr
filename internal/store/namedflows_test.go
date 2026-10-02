package store_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// gitRepo makes a repository on branch main with files committed, for a
// rig whose base is main.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	commitFiles(t, dir, files)
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFiles(t *testing.T, dir string, files map[string]string) string {
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
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "files")
	return runGit(t, dir, "rev-parse", "HEAD")
}

const (
	repoDefault  = "new -> review\nreview -> done\ndone terminal\n"
	repoDecision = "ask -> answered\nanswered terminal\n"
	repoAlt      = "new -> alt\nalt terminal\n"
)

func repoRig(t *testing.T) (store.API, context.Context, string) {
	t.Helper()
	dir := gitRepo(t, map[string]string{
		".yardr/flows/default.flow":       repoDefault,
		".yardr/flows/type/decision.flow": repoDecision,
		".yardr/flows/alt.flow":           repoAlt,
	})
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "web", Path: dir, Base: "main"}); err != nil {
		t.Fatal(err)
	}
	return s, ctx, dir
}

func governing(t *testing.T, s store.API, id string) (string, string) {
	t.Helper()
	next, err := s.NextStages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if next.Flow == nil {
		return "", ""
	}
	return next.Flow.Name, next.Source.String()
}

// pin > store pointer > repo .yardr/flows > none, and within the store and
// within the repo, the type's flow over the rig's.
func TestFlowResolutionOrder(t *testing.T) {
	s, ctx, dir := repoRig(t)
	sha := runGit(t, dir, "rev-parse", "main")[:7]
	task, err := s.CreateBead(ctx, store.NewBead{Title: "t", Rig: "web"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CreateBead(ctx, store.NewBead{Title: "q", Rig: "web", Type: "decision", Stage: "ask"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// Repo: default.flow for the rig, type/decision.flow for decisions.
	if name, src := governing(t, s, task.ID); name != "default" || src != "repo@"+sha {
		t.Fatalf("task: %s (%s)", name, src)
	}
	if name, src := governing(t, s, q.ID); name != "type/decision" || src != "repo@"+sha+", type decision" {
		t.Fatalf("decision: %s (%s)", name, src)
	}
	if _, err := s.Advance(ctx, task.ID, "done", "me"); err == nil || !strings.Contains(err.Error(), "allowed next: review") {
		t.Fatalf("repo flow not enforced: %v", err)
	}

	// Store beats repo: a rig pointer overrides both repo files.
	putNamed(t, s, "code", yardFlow)
	if err := s.SetFlowPointer(ctx, "web", "", "code", "me"); err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, task.ID); name != "code" || src != "store" {
		t.Fatalf("task with pointer: %s (%s)", name, src)
	}
	if name, src := governing(t, s, q.ID); name != "code" || src != "store" {
		t.Fatalf("decision with rig pointer: %s (%s)", name, src)
	}
	// A type pointer may name a flow only the repo has.
	if err := s.SetFlowPointer(ctx, "web", "decision", "alt", "me"); err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, q.ID); name != "alt" || src != "store, type decision, from repo@"+sha {
		t.Fatalf("decision with type pointer: %s (%s)", name, src)
	}

	// Pin beats store.
	if _, err := s.UpdateBead(ctx, task.ID, store.BeadPatch{Metadata: map[string]any{"flow": "alt"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, task.ID); name != "alt" || src != "pin, from repo@"+sha {
		t.Fatalf("pinned task: %s (%s)", name, src)
	}
	if b, err := s.Advance(ctx, task.ID, "alt", "me"); err != nil || b.Status != "closed" {
		t.Fatalf("pinned flow not enforced: %v %+v", err, b)
	}
	// A pin on a bead without a rig governs it too, from the store.
	free, err := s.CreateBead(ctx, store.NewBead{Title: "f", Metadata: map[string]any{"flow": "code"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, free.ID); name != "code" || src != "pin" {
		t.Fatalf("pinned rigless bead: %s (%s)", name, src)
	}
	// Pinning a flow that exists nowhere refuses moves instead of
	// silently dropping the flow.
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "web", Metadata: map[string]any{"flow": "ghost"}}, "me"); err == nil || !strings.Contains(err.Error(), "pins flow ghost") {
		t.Fatalf("pin of unknown flow: %v", err)
	}

	// None: a rig whose repository has no flows, without pointers.
	empty := gitRepo(t, map[string]string{"README": "x"})
	if err := s.PutRig(ctx, store.Rig{Name: "bare", Path: empty, Base: "main"}); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Rig: "bare", Stage: "anything"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := governing(t, s, b.ID); name != "" {
		t.Fatalf("bare rig governed by %s", name)
	}
	// So is a rig whose path is no checkout.
	if err := s.PutRig(ctx, store.Rig{Name: "gone", Path: filepath.Join(empty, "missing"), Base: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "g", Rig: "gone", Stage: "anything"}, "me"); err != nil {
		t.Fatal(err)
	}

	rfs, err := s.RigFlows(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, rf := range rfs {
		got = append(got, rf.Type+"="+rf.Flow.Name+" ("+rf.Source.String()+")")
	}
	want := []string{"=code (store)", "decision=alt (store, type decision, from repo@" + sha + ")"}
	if !slices.Equal(got, want) {
		t.Fatalf("RigFlows = %v, want %v", got, want)
	}
}

// Repo flows come from the tip of the base branch: not the checkout's
// branch, not its working tree, and they follow the base when it moves.
func TestRepoFlowsReadFromBase(t *testing.T) {
	s, ctx, dir := repoRig(t)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Rig: "web"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// A bead branch that lowers the bar, checked out in the rig, plus an
	// uncommitted edit on top.
	runGit(t, dir, "checkout", "-q", "-b", "web/"+b.ID)
	commitFiles(t, dir, map[string]string{".yardr/flows/default.flow": "new -> done\ndone terminal\n"})
	if err := os.WriteFile(filepath.Join(dir, ".yardr/flows/default.flow"), []byte("new -> shipped\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, b.ID, "done", "me"); err == nil {
		t.Fatal("the bead branch's flow let the bead skip review")
	}
	next, err := s.NextStages(ctx, b.ID)
	if err != nil || !slices.Equal(next.Stages, []string{"review"}) {
		t.Fatalf("next = %+v, %v", next, err)
	}

	// Landing the change on main moves the base; the new flow applies.
	runGit(t, dir, "checkout", "-q", "-f", "main")
	sha := commitFiles(t, dir, map[string]string{".yardr/flows/default.flow": "new -> review | done\nreview -> done\ndone terminal\n"})
	next, err = s.NextStages(ctx, b.ID)
	if err != nil || !slices.Equal(next.Stages, []string{"review", "done"}) || next.Source.Commit != sha {
		t.Fatalf("after base moved: next = %+v, %v", next, err)
	}
	if b, err = s.Advance(ctx, b.ID, "done", "me"); err != nil || b.Status != "closed" {
		t.Fatalf("advance under the landed flow: %v %+v", err, b)
	}

	// A broken flow on the base refuses moves rather than leave beads
	// ungoverned.
	commitFiles(t, dir, map[string]string{".yardr/flows/default.flow": "new => review\n"})
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "t2", Rig: "web"}, "me")
	if err == nil || !strings.Contains(err.Error(), ".yardr/flows/default.flow") {
		t.Fatalf("create under a broken repo flow: %v %+v", err, b2)
	}
	repo, err := s.RepoFlows(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(repo.Flows, func(f store.RepoFlow) bool { return f.Name == "default" })
	if i < 0 || repo.Flows[i].Error == "" || repo.Flows[i].Flow != nil {
		t.Fatalf("repo flows = %+v", repo.Flows)
	}
}

func TestDeleteFlowRefusedWhileUsed(t *testing.T) {
	s, ctx := flowRig(t)
	putNamed(t, s, "code", yardFlow)
	if err := s.SetFlowPointer(ctx, "yard", "", "code", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "yard", "bug", "code", "me"); err != nil {
		t.Fatal(err)
	}
	pinned, err := s.CreateBead(ctx, store.NewBead{Title: "p", Metadata: map[string]any{"flow": "code"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	err = s.DeleteFlow(ctx, "code", "me")
	if err == nil {
		t.Fatal("removed a flow in use")
	}
	for _, want := range []string{"rig yard,", "rig yard type bug", "bead " + pinned.ID} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
	if _, err := s.GetFlow(ctx, "code"); err != nil {
		t.Fatalf("refused rm removed the flow: %v", err)
	}
	// A closed bead's pin and cleared pointers no longer hold it.
	if _, err := s.CloseBead(ctx, pinned.ID, "done", "me"); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"", "bug"} {
		if err := s.SetFlowPointer(ctx, "yard", typ, "", "me"); err != nil {
			t.Fatal(err)
		}
	}
	if ptrs, _ := s.FlowPointers(ctx); len(ptrs) != 0 {
		t.Fatalf("pointers left: %+v", ptrs)
	}
	if err := s.DeleteFlow(ctx, "code", "me"); err != nil {
		t.Fatal(err)
	}
}

// One named flow shared by two rigs: an edit applies to both.
func TestNamedFlowShared(t *testing.T) {
	s, ctx := flowRig(t)
	if err := s.PutRig(ctx, store.Rig{Name: "webapp", Path: "/tmp/webapp", Base: "origin/main"}); err != nil {
		t.Fatal(err)
	}
	putNamed(t, s, "code", "new -> review\nreview -> new\n")
	var beads []*store.Bead
	for _, rig := range []string{"yard", "webapp"} {
		if err := s.SetFlowPointer(ctx, rig, "", "code", "me"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: rig}, "me")
		if err != nil {
			t.Fatal(err)
		}
		beads = append(beads, b)
	}
	putNamed(t, s, "code", "new -> review | backlog\nreview -> new\nbacklog -> new\n")
	for _, b := range beads {
		if _, err := s.Advance(ctx, b.ID, "backlog", "me"); err != nil {
			t.Fatalf("%s: %v", *b.Rig, err)
		}
	}
}

// Stores from before named flows keep their lifecycles: each rig flow
// becomes the named flow <rig> and each type flow <rig>.<type>, with
// pointers to them.
func TestMigrateFlows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string]map[string]any{
		"yard\x00": {"rig": "yard", "stages": []string{"new", "review"},
			"transitions": map[string][]string{"new": {"review"}, "review": {"new"}}},
		"yard\x00decision": {"rig": "yard", "type": "decision", "stages": []string{"decide", "decided"},
			"transitions": map[string][]string{"decide": {"decided"}}, "terminal": []string{"decided"}},
	}
	err = db.Update(func(tx *bolt.Tx) error {
		rigs, err := tx.CreateBucket([]byte("rigs"))
		if err != nil {
			return err
		}
		if err := rigs.Put([]byte("yard"), []byte(`{"name":"yard","path":"/tmp/yard","base":"origin/main"}`)); err != nil {
			return err
		}
		flows, err := tx.CreateBucket([]byte("flows"))
		if err != nil {
			return err
		}
		for k, v := range legacy {
			raw, _ := json.Marshal(v)
			if err := flows.Put([]byte(k), raw); err != nil {
				return err
			}
		}
		return nil
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ptrs, err := s.FlowPointers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.FlowPointer{{Rig: "yard", Flow: "yard"}, {Rig: "yard", Type: "decision", Flow: "yard.decision"}}
	if !slices.Equal(ptrs, want) {
		t.Fatalf("pointers = %+v, want %+v", ptrs, want)
	}
	d, err := s.GetFlow(ctx, "yard.decision")
	if err != nil || !d.IsTerminal("decided") {
		t.Fatalf("yard.decision = %+v, %v", d, err)
	}
	// Behaviour is unchanged.
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard"}, "me")
	if _, err := s.Advance(ctx, b.ID, "decided", "me"); err == nil {
		t.Fatal("task used the decision flow")
	}
	q, err := s.CreateBead(ctx, store.NewBead{Title: "q", Rig: "yard", Type: "decision", Stage: "decide"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if q, err = s.Advance(ctx, q.ID, "decided", "me"); err != nil || q.Status != "closed" {
		t.Fatalf("decision: %v %+v", err, q)
	}
	evs, _ := s.EventsSince(ctx, 0, 0)
	if len(evs) == 0 || evs[0].Kind != "flows_migrated" {
		t.Fatalf("first event = %+v", evs)
	}
	s.Close(ctx)

	// Once: reopening leaves the migrated state alone.
	s2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close(ctx)
	if ptrs, _ := s2.FlowPointers(ctx); len(ptrs) != 2 {
		t.Fatalf("pointers after reopen = %+v", ptrs)
	}
	if err := s2.DeleteFlow(ctx, "yard", "me"); err == nil || !strings.Contains(err.Error(), "rig yard") {
		t.Fatalf("rm of a migrated rig flow: %v", err)
	}
}

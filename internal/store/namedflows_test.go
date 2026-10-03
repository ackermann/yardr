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
	if err := s.PutRig(ctx, store.Rig{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
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
	if _, err := s.Advance(ctx, task.ID, "done", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "allowed next: review") {
		t.Fatalf("repo flow not enforced: %v", err)
	}

	// Store beats repo: a rig pointer overrides both repo files. It would
	// govern the decision too, whose stage ask the flow lacks, so the
	// pointer is refused until the decision is out of the way.
	putNamed(t, s, "code", yardFlow)
	if err := s.SetFlowPointer(ctx, "web", "", "code", "me"); err == nil || !strings.Contains(err.Error(), q.ID+" (ask)") {
		t.Fatalf("rig pointer over a decision in ask: %v", err)
	}
	if q, err = s.Advance(ctx, q.ID, "answered", "me"); err != nil || q.Status != "closed" {
		t.Fatalf("decision: %v %+v", err, q)
	}
	if err := s.SetFlowPointer(ctx, "web", "", "code", "me"); err != nil {
		t.Fatal(err)
	}
	// In new, a stage flow alt has too, so the type pointer below is not
	// refused over it.
	if q, err = s.CreateBead(ctx, store.NewBead{Title: "q", Rig: "web", Type: "decision", Stage: "new"}, "me"); err != nil {
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
	if _, err := s.UpdateBead(ctx, task.ID, store.BeadPatch{Metadata: map[string]any{"flow": "alt"}}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, task.ID); name != "alt" || src != "pin, from repo@"+sha {
		t.Fatalf("pinned task: %s (%s)", name, src)
	}
	if b, err := s.Advance(ctx, task.ID, "alt", store.Expect{}, "me"); err != nil || b.Status != "closed" {
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
	if err := s.PutRig(ctx, store.Rig{Name: "bare", Path: empty, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Rig: "bare", Stage: "anything"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := governing(t, s, b.ID); name != "" {
		t.Fatalf("bare rig governed by %s", name)
	}
	// A rig whose path is no checkout is not "no flows": git failing there
	// says nothing about the base, so the bead is refused, not ungoverned.
	if err := s.PutRig(ctx, store.Rig{Name: "gone", Path: filepath.Join(empty, "missing"), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "g", Rig: "gone", Stage: "anything"}, "me"); err == nil || !strings.Contains(err.Error(), "resolving base main") {
		t.Fatalf("create in a rig without a checkout: %v", err)
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
	if _, err := s.Advance(ctx, b.ID, "done", store.Expect{}, "me"); err == nil {
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
	if b, err = s.Advance(ctx, b.ID, "done", store.Expect{}, "me"); err != nil || b.Status != "closed" {
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
	// A JSON flow file whose stage order is ambiguous is broken the same
	// way, never sorted into an order.
	commitFiles(t, dir, map[string]string{".yardr/flows/default.flow": `{"transitions": {"new": ["review"], "review": ["done"]}, "terminal": ["done"]}`})
	if repo, err = s.RepoFlows(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	i = slices.IndexFunc(repo.Flows, func(f store.RepoFlow) bool { return f.Name == "default" })
	if i < 0 || !strings.Contains(repo.Flows[i].Error, "ambiguous") || repo.Flows[i].Flow != nil {
		t.Fatalf("repo flows with an ambiguous JSON flow = %+v", repo.Flows)
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
	if _, err := s.CloseBead(ctx, pinned.ID, "done", store.Expect{}, "me"); err != nil {
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
	if err := s.PutRig(ctx, store.Rig{Name: "webapp", Base: "origin/main"}, "me"); err != nil {
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
		if _, err := s.Advance(ctx, b.ID, "backlog", store.Expect{}, "me"); err != nil {
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
	if _, err := s.Advance(ctx, b.ID, "decided", store.Expect{}, "me"); err == nil {
		t.Fatal("task used the decision flow")
	}
	q, err := s.CreateBead(ctx, store.NewBead{Title: "q", Rig: "yard", Type: "decision", Stage: "decide"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if q, err = s.Advance(ctx, q.ID, "decided", store.Expect{}, "me"); err != nil || q.Status != "closed" {
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

// Reading repo flows fails closed: only a base that does not exist in the
// checkout means "no repo flows"; a path git cannot read is an error.
func TestRepoFlowsGitErrors(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	repo := gitRepo(t, map[string]string{".yardr/flows/default.flow": "new -> done\n"})
	rigs := []store.Rig{
		{Name: "nobase", Path: repo, Base: "origin/main"}, // never fetched
		{Name: "plain", Path: t.TempDir(), Base: "main"},  // not a checkout
		{Name: "nopath"},
	}
	for _, r := range rigs {
		if err := s.PutRig(ctx, r, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, rig := range []string{"nobase", "nopath"} {
		rf, err := s.RepoFlows(ctx, rig)
		if err != nil || rf.Commit != "" || len(rf.Flows) != 0 {
			t.Fatalf("%s: repo flows = %+v, %v; want none", rig, rf, err)
		}
	}
	if rf, err := s.RepoFlows(ctx, "plain"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("plain: repo flows = %+v, %v; want the git error", rf, err)
	}
	// The error reaches the bead: it cannot be created ungoverned, and an
	// existing one cannot be moved.
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "p", Rig: "plain"}, "me"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("create in plain: %v", err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "p", Rig: "nopath"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	plain := "plain"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Rig: &plain}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextStages(ctx, b.ID); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("next stages in plain: %v", err)
	}
	if _, err := s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("advance in plain: %v", err)
	}
}

// Pointing a rig or type at a flow, or clearing the pointer, is refused
// while an open bead it would hand to a new flow sits in a stage that flow
// lacks; the beads are named, the pointer is not changed. Pinned beads,
// closed beads and beads of a type with its own pointer are not affected,
// so they do not stand in the way.
func TestSetFlowPointerRefusesStrays(t *testing.T) {
	s, ctx := flowRig(t)
	putNamed(t, s, "code", yardFlow)
	putNamed(t, s, "tri", "triage -> new\nnew -> done\ndone terminal\n")
	// Without a flow any stage is allowed: beads stray before the rules.
	stray, err := s.CreateBead(ctx, store.NewBead{Title: "stray", Rig: "yard", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	fine, err := s.CreateBead(ctx, store.NewBead{Title: "fine", Rig: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.CreateBead(ctx, store.NewBead{Title: "pinned", Rig: "yard", Stage: "triage", Metadata: map[string]any{"flow": "tri"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.CreateBead(ctx, store.NewBead{Title: "closed", Rig: "yard", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, closed.ID, "gone", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	bug, err := s.CreateBead(ctx, store.NewBead{Title: "bug", Rig: "yard", Type: "bug", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}

	err = s.SetFlowPointer(ctx, "yard", "", "code", "me")
	if err == nil {
		t.Fatal("rig pointer set over beads in triage")
	}
	for _, want := range []string{"flow code lacks the stage of open beads in rig yard", stray.ID + " (triage)", bug.ID + " (triage)", "--meta flow="} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	for _, id := range []string{fine.ID, pinned.ID, closed.ID} {
		if strings.Contains(err.Error(), id) {
			t.Errorf("error names %s, which the change does not strand: %v", id, err)
		}
	}
	if ptrs, _ := s.FlowPointers(ctx); len(ptrs) != 0 {
		t.Fatalf("refused pointer was kept: %+v", ptrs)
	}
	// The type's own pointer takes its beads out of the rig's hands.
	if err := s.SetFlowPointer(ctx, "yard", "bug", "tri", "me"); err != nil {
		t.Fatal(err)
	}
	err = s.SetFlowPointer(ctx, "yard", "", "code", "me")
	if err == nil || strings.Contains(err.Error(), bug.ID) || !strings.Contains(err.Error(), stray.ID) {
		t.Fatalf("rig pointer with the bug under its type's flow: %v", err)
	}
	// Moved to a stage the flow has (no flow governs it yet, so any move
	// goes), the pointer is accepted.
	if _, err := s.Advance(ctx, stray.ID, "new", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "yard", "", "code", "me"); err != nil {
		t.Fatal(err)
	}

	// Clearing the type pointer would hand the bug to code, which lacks
	// triage; the error names the flow it would fall under.
	err = s.SetFlowPointer(ctx, "yard", "bug", "", "me")
	if err == nil || !strings.Contains(err.Error(), "clearing the flow of rig yard type bug") || !strings.Contains(err.Error(), bug.ID+" (triage, not in flow code)") {
		t.Fatalf("clearing the type pointer: %v", err)
	}
	if name, _ := governing(t, s, bug.ID); name != "tri" {
		t.Fatalf("bug follows %s after a refused clear, want tri", name)
	}
	// Pinned to the flow it follows now, the bug is out of the way.
	if _, err := s.UpdateBead(ctx, bug.ID, store.BeadPatch{Metadata: map[string]any{"flow": "tri"}}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "yard", "bug", "", "me"); err != nil {
		t.Fatal(err)
	}
	// Clearing the rig's pointer leaves the rig without a flow (no repo):
	// any stage goes, so nothing strays.
	if err := s.SetFlowPointer(ctx, "yard", "", "", "me"); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.EventsSince(ctx, 0, 0)
	var kinds []string
	for _, e := range evs {
		if strings.HasPrefix(e.Kind, "flow_pointer") {
			kinds = append(kinds, e.Kind)
		}
	}
	want := []string{"flow_pointer_set", "flow_pointer_set", "flow_pointer_cleared", "flow_pointer_cleared"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("pointer events = %v, want %v (refused changes record none)", kinds, want)
	}
}

// Clearing a pointer hands beads to the repository's flows, which are
// checked the same way; a repository git cannot read is an error only for
// a bead whose flow would come from it.
func TestSetFlowPointerStraysAgainstRepo(t *testing.T) {
	s, ctx, _ := repoRig(t)
	putNamed(t, s, "tri", "triage -> new\nnew -> done\ndone terminal\n")
	if err := s.SetFlowPointer(ctx, "web", "", "tri", "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Rig: "web", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	err = s.SetFlowPointer(ctx, "web", "", "", "me")
	if err == nil || !strings.Contains(err.Error(), b.ID+" (triage, not in flow default)") {
		t.Fatalf("clear over the repo's default.flow: %v", err)
	}
	if name, src := governing(t, s, b.ID); name != "tri" || src != "store" {
		t.Fatalf("after a refused clear: %s (%s)", name, src)
	}
	// A type pointer naming a repo flow is checked against that flow.
	if err := s.SetFlowPointer(ctx, "web", "task", "alt", "me"); err == nil || !strings.Contains(err.Error(), b.ID+" (triage)") {
		t.Fatalf("type pointer at repo flow alt: %v", err)
	}
	if _, err := s.Advance(ctx, b.ID, "new", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "web", "", "", "me"); err != nil {
		t.Fatal(err)
	}

	// A rig whose checkout git cannot read: pointing it at a store flow
	// still works for beads that never need the repository; clearing the
	// pointer would, and fails with git's error rather than silently.
	s2 := storetest.New(t)
	if err := s2.PutRig(ctx, store.Rig{Name: "gone", Path: filepath.Join(t.TempDir(), "missing"), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, s2, "tri", "triage -> new\nnew -> done\ndone terminal\n")
	if err := s2.SetFlowPointer(ctx, "gone", "", "tri", "me"); err != nil {
		t.Fatalf("store pointer on a rig without a readable checkout: %v", err)
	}
	g, err := s2.CreateBead(ctx, store.NewBead{Title: "g", Rig: "gone", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.SetFlowPointer(ctx, "gone", "", "", "me"); err == nil || !strings.Contains(err.Error(), "resolving base main") || !strings.Contains(err.Error(), g.ID) {
		t.Fatalf("clear on a rig without a readable checkout: %v", err)
	}
}

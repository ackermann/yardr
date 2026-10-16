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
// depot whose base is main.
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

func repoDepot(t *testing.T) (store.API, context.Context, string) {
	t.Helper()
	dir := gitRepo(t, map[string]string{
		".yardr/flows/default.flow":       repoDefault,
		".yardr/flows/type/decision.flow": repoDecision,
		".yardr/flows/alt.flow":           repoAlt,
	})
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
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
// within the repo, the type's flow over the depot's.
func TestFlowResolutionOrder(t *testing.T) {
	t.Parallel()
	s, ctx, dir := repoDepot(t)
	sha := runGit(t, dir, "rev-parse", "main")[:7]
	task, err := s.CreateBead(ctx, store.NewBead{Title: "t", Depot: "web"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CreateBead(ctx, store.NewBead{Title: "q", Depot: "web", Type: "decision", Stage: "ask"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// Repo: default.flow for the depot, type/decision.flow for decisions.
	if name, src := governing(t, s, task.ID); name != "default" || src != "repo@"+sha {
		t.Fatalf("task: %s (%s)", name, src)
	}
	if name, src := governing(t, s, q.ID); name != "type/decision" || src != "repo@"+sha+", type decision" {
		t.Fatalf("decision: %s (%s)", name, src)
	}
	if _, err := s.Advance(ctx, task.ID, "done", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "allowed next: review") {
		t.Fatalf("repo flow not enforced: %v", err)
	}

	// Store beats repo: a depot pointer overrides both repo files. It would
	// govern the decision too, whose stage ask the flow lacks, so the
	// pointer is refused until the decision is out of the way.
	putNamed(t, s, "code", yardFlow)
	if err := s.SetFlowPointer(ctx, "web", "", "code", "me"); err == nil || !strings.Contains(err.Error(), q.ID+" (ask)") {
		t.Fatalf("depot pointer over a decision in ask: %v", err)
	}
	if q, err = s.Advance(ctx, q.ID, "answered", store.Expect{}, "me"); err != nil || q.Status != "closed" {
		t.Fatalf("decision: %v %+v", err, q)
	}
	if err := s.SetFlowPointer(ctx, "web", "", "code", "me"); err != nil {
		t.Fatal(err)
	}
	// In new, a stage flow alt has too, so the type pointer below is not
	// refused over it.
	if q, err = s.CreateBead(ctx, store.NewBead{Title: "q", Depot: "web", Type: "decision", Stage: "new"}, "me"); err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, task.ID); name != "code" || src != "store" {
		t.Fatalf("task with pointer: %s (%s)", name, src)
	}
	if name, src := governing(t, s, q.ID); name != "code" || src != "store" {
		t.Fatalf("decision with depot pointer: %s (%s)", name, src)
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
	// A pin on a bead without a depot governs it too, from the store.
	free, err := s.CreateBead(ctx, store.NewBead{Title: "f", Metadata: map[string]any{"flow": "code"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if name, src := governing(t, s, free.ID); name != "code" || src != "pin" {
		t.Fatalf("pinned depotless bead: %s (%s)", name, src)
	}
	// Pinning a flow that exists nowhere refuses moves instead of
	// silently dropping the flow.
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Metadata: map[string]any{"flow": "ghost"}}, "me"); err == nil || !strings.Contains(err.Error(), "pins flow ghost") {
		t.Fatalf("pin of unknown flow: %v", err)
	}

	// None: a depot whose repository has no flows, without pointers.
	empty := gitRepo(t, map[string]string{"README": "x"})
	if err := s.PutDepot(ctx, store.Depot{Name: "bare", Path: empty, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Depot: "bare", Stage: "anything"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := governing(t, s, b.ID); name != "" {
		t.Fatalf("bare depot governed by %s", name)
	}
	// A depot whose path is no checkout is not "no flows": git failing there
	// says nothing about the base, so the bead is refused, not ungoverned.
	if err := s.PutDepot(ctx, store.Depot{Name: "gone", Path: filepath.Join(empty, "missing"), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "g", Depot: "gone", Stage: "anything"}, "me"); err == nil || !strings.Contains(err.Error(), "resolving base main") {
		t.Fatalf("create in a depot without a checkout: %v", err)
	}

	rfs, err := s.DepotFlows(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, rf := range rfs {
		got = append(got, rf.Type+"="+rf.Flow.Name+" ("+rf.Source.String()+")")
	}
	want := []string{"=code (store)", "decision=alt (store, type decision, from repo@" + sha + ")"}
	if !slices.Equal(got, want) {
		t.Fatalf("DepotFlows = %v, want %v", got, want)
	}
}

// Repo flows come from the tip of the base branch: not the checkout's
// branch, not its working tree, and they follow the base when it moves.
func TestRepoFlowsReadFromBase(t *testing.T) {
	t.Parallel()
	s, ctx, dir := repoDepot(t)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Depot: "web"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// A bead branch that lowers the bar, checked out in the depot, plus an
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
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "t2", Depot: "web"}, "me")
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
	t.Parallel()
	s, ctx := flowDepot(t)
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
	for _, want := range []string{"depot yard,", "depot yard type bug", "bead " + pinned.ID} {
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

// One named flow shared by two depots: an edit applies to both.
func TestNamedFlowShared(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	if err := s.PutDepot(ctx, store.Depot{Name: "webapp", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, s, "code", "new -> review\nreview -> new\n")
	var beads []*store.Bead
	for _, depot := range []string{"yard", "webapp"} {
		if err := s.SetFlowPointer(ctx, depot, "", "code", "me"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: depot}, "me")
		if err != nil {
			t.Fatal(err)
		}
		beads = append(beads, b)
	}
	putNamed(t, s, "code", "new -> review | backlog\nreview -> new\nbacklog -> new\n")
	for _, b := range beads {
		if _, err := s.Advance(ctx, b.ID, "backlog", store.Expect{}, "me"); err != nil {
			t.Fatalf("%s: %v", *b.Depot, err)
		}
	}
}

// Stores from before named flows keep their lifecycles: each depot flow
// becomes the named flow <depot> and each type flow <depot>.<type>, with
// pointers to them.
func TestMigrateFlows(t *testing.T) {
	t.Parallel()
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
		// As the build that had this bucket stored them: a depot was a rig.
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
	want := []store.FlowPointer{{Depot: "yard", Flow: "yard"}, {Depot: "yard", Type: "decision", Flow: "yard.decision"}}
	if !slices.Equal(ptrs, want) {
		t.Fatalf("pointers = %+v, want %+v", ptrs, want)
	}
	d, err := s.GetFlow(ctx, "yard.decision")
	if err != nil || !d.IsTerminal("decided") {
		t.Fatalf("yard.decision = %+v, %v", d, err)
	}
	// Behaviour is unchanged.
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard"}, "me")
	if _, err := s.Advance(ctx, b.ID, "decided", store.Expect{}, "me"); err == nil {
		t.Fatal("task used the decision flow")
	}
	q, err := s.CreateBead(ctx, store.NewBead{Title: "q", Depot: "yard", Type: "decision", Stage: "decide"}, "me")
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
	if err := s2.DeleteFlow(ctx, "yard", "me"); err == nil || !strings.Contains(err.Error(), "depot yard") {
		t.Fatalf("rm of a migrated depot flow: %v", err)
	}
}

// Reading repo flows fails closed: only a base that does not exist in the
// checkout means "no repo flows"; a path git cannot read is an error.
func TestRepoFlowsGitErrors(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	repo := gitRepo(t, map[string]string{".yardr/flows/default.flow": "new -> done\n"})
	depots := []store.Depot{
		{Name: "nobase", Path: repo, Base: "origin/main"}, // never fetched
		{Name: "plain", Path: t.TempDir(), Base: "main"},  // not a checkout
		{Name: "nopath"},
	}
	for _, r := range depots {
		if err := s.PutDepot(ctx, r, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, depot := range []string{"nobase", "nopath"} {
		rf, err := s.RepoFlows(ctx, depot)
		if err != nil || rf.Commit != "" || len(rf.Flows) != 0 {
			t.Fatalf("%s: repo flows = %+v, %v; want none", depot, rf, err)
		}
	}
	if rf, err := s.RepoFlows(ctx, "plain"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("plain: repo flows = %+v, %v; want the git error", rf, err)
	}
	// The error reaches the bead: it cannot be created ungoverned, and an
	// existing one cannot be moved.
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "p", Depot: "plain"}, "me"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("create in plain: %v", err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "p", Depot: "nopath"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	plain := "plain"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Depot: &plain}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextStages(ctx, b.ID); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("next stages in plain: %v", err)
	}
	if _, err := s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("advance in plain: %v", err)
	}
}

// Replacing a named flow is refused while an open bead that follows it, by
// a depot's or type's pointer or by its own pin, sits in a stage the new
// version lacks; the beads are named with what leads them to the flow and
// the stored flow is kept. Closed beads, beads pinned elsewhere and beads
// of a type with its own pointer do not stand in the way (yardr-wna2).
func TestPutFlowRefusesStrays(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	const old, dropped = "triage -> new\nnew -> done\ndone terminal\n", "new -> done\ndone terminal\n"
	putNamed(t, s, "t", old)
	putNamed(t, s, "other", old)
	for _, p := range [][3]string{{"yard", "", "t"}, {"web", "bug", "t"}, {"yard", "chore", "other"}} {
		if err := s.SetFlowPointer(ctx, p[0], p[1], p[2], "me"); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(nb store.NewBead) *store.Bead {
		t.Helper()
		nb.Stage = "triage"
		b, err := s.CreateBead(ctx, nb, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	stray := mk(store.NewBead{Title: "stray", Depot: "yard"})
	bug := mk(store.NewBead{Title: "bug", Depot: "web", Type: "bug"})
	pin := mk(store.NewBead{Title: "pin", Depot: "web", Metadata: map[string]any{"flow": "t"}})
	// Not in the way: web's tasks follow no flow, yard's chores and the
	// bead pinned to other follow other, and the closed bead is done.
	task := mk(store.NewBead{Title: "task", Depot: "web"})
	chore := mk(store.NewBead{Title: "chore", Depot: "yard", Type: "chore"})
	elsewhere := mk(store.NewBead{Title: "elsewhere", Depot: "yard", Metadata: map[string]any{"flow": "other"}})
	closed := mk(store.NewBead{Title: "closed", Depot: "yard"})
	if _, err := s.CloseBead(ctx, closed.ID, "gone", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}

	replace := func() error {
		f, err := store.ParseFlow(dropped)
		if err != nil {
			t.Fatal(err)
		}
		f.Name = "t"
		_, err = s.PutFlow(ctx, *f, "me")
		return err
	}
	err := replace()
	if err == nil {
		t.Fatal("flow t replaced over beads in triage")
	}
	for _, want := range []string{
		"the new flow t lacks the stage of open beads",
		"in depot yard: " + stray.ID + " (triage)",
		"in depot web type bug: " + bug.ID + " (triage)",
		"pinning it: " + pin.ID + " (triage)",
		"--meta flow=",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	for _, b := range []*store.Bead{task, chore, elsewhere, closed} {
		if strings.Contains(err.Error(), b.ID) {
			t.Errorf("error names %s (%s), which the new version does not strand: %v", b.ID, b.Title, err)
		}
	}
	if f, err := s.GetFlow(ctx, "t"); err != nil || !slices.Contains(f.Stages, "triage") {
		t.Fatalf("refused version was kept: %+v, %v", f, err)
	}
	evs, _ := s.EventsSince(ctx, 0, 0)
	sets := 0
	for _, e := range evs {
		if e.Kind == "flow_set" {
			sets++
		}
	}
	if sets != 2 {
		t.Errorf("flow_set events = %d, want 2: the refused save left one", sets)
	}

	// Each bead out of the dropped stage, or pinned to a flow that has
	// it, stops standing in the way; the last one still does.
	if _, err := s.Advance(ctx, stray.ID, "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateBead(ctx, bug.ID, store.BeadPatch{Metadata: map[string]any{"flow": "other"}}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	err = replace()
	if err == nil || !strings.Contains(err.Error(), "open beads pinning it: "+pin.ID+" (triage)") || strings.Contains(err.Error(), stray.ID) || strings.Contains(err.Error(), bug.ID) {
		t.Fatalf("replace with only the pinned bead in triage: %v", err)
	}
	if _, err := s.Advance(ctx, pin.ID, "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := replace(); err != nil {
		t.Fatalf("replace with no open bead in a dropped stage: %v", err)
	}
	if f, err := s.GetFlow(ctx, "t"); err != nil || slices.Contains(f.Stages, "triage") {
		t.Fatalf("flow t after the replace: %+v, %v", f, err)
	}
}

// Pointing a depot or type at a flow, or clearing the pointer, is refused
// while an open bead it would hand to a new flow sits in a stage that flow
// lacks; the beads are named, the pointer is not changed. Pinned beads,
// closed beads and beads of a type with its own pointer are not affected,
// so they do not stand in the way.
func TestSetFlowPointerRefusesStrays(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putNamed(t, s, "code", yardFlow)
	putNamed(t, s, "tri", "triage -> new\nnew -> done\ndone terminal\n")
	// Without a flow any stage is allowed: beads stray before the rules.
	stray, err := s.CreateBead(ctx, store.NewBead{Title: "stray", Depot: "yard", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	fine, err := s.CreateBead(ctx, store.NewBead{Title: "fine", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.CreateBead(ctx, store.NewBead{Title: "pinned", Depot: "yard", Stage: "triage", Metadata: map[string]any{"flow": "tri"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.CreateBead(ctx, store.NewBead{Title: "closed", Depot: "yard", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, closed.ID, "gone", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	bug, err := s.CreateBead(ctx, store.NewBead{Title: "bug", Depot: "yard", Type: "bug", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}

	err = s.SetFlowPointer(ctx, "yard", "", "code", "me")
	if err == nil {
		t.Fatal("depot pointer set over beads in triage")
	}
	for _, want := range []string{"flow code lacks the stage of open beads in depot yard", stray.ID + " (triage)", bug.ID + " (triage)", "--meta flow="} {
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
	// The type's own pointer takes its beads out of the depot's hands.
	if err := s.SetFlowPointer(ctx, "yard", "bug", "tri", "me"); err != nil {
		t.Fatal(err)
	}
	err = s.SetFlowPointer(ctx, "yard", "", "code", "me")
	if err == nil || strings.Contains(err.Error(), bug.ID) || !strings.Contains(err.Error(), stray.ID) {
		t.Fatalf("depot pointer with the bug under its type's flow: %v", err)
	}
	// Moved to a stage the flow has (no flow governs it yet, so any move
	// goes), the pointer is accepted.
	if _, err := s.Advance(ctx, stray.ID, "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "yard", "", "code", "me"); err != nil {
		t.Fatal(err)
	}

	// Clearing the type pointer would hand the bug to code, which lacks
	// triage; the error names the flow it would fall under.
	err = s.SetFlowPointer(ctx, "yard", "bug", "", "me")
	if err == nil || !strings.Contains(err.Error(), "clearing the flow of depot yard type bug") || !strings.Contains(err.Error(), bug.ID+" (triage, not in flow code)") {
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
	// Clearing the depot's pointer leaves the depot without a flow (no repo):
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
	t.Parallel()
	s, ctx, _ := repoDepot(t)
	putNamed(t, s, "tri", "triage -> new\nnew -> done\ndone terminal\n")
	if err := s.SetFlowPointer(ctx, "web", "", "tri", "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Depot: "web", Stage: "triage"}, "me")
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
	if _, err := s.Advance(ctx, b.ID, "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "web", "", "", "me"); err != nil {
		t.Fatal(err)
	}

	// A depot whose checkout git cannot read: pointing it at a store flow
	// still works for beads that never need the repository; clearing the
	// pointer would, and fails with git's error rather than silently.
	s2 := storetest.New(t)
	if err := s2.PutDepot(ctx, store.Depot{Name: "gone", Path: filepath.Join(t.TempDir(), "missing"), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, s2, "tri", "triage -> new\nnew -> done\ndone terminal\n")
	if err := s2.SetFlowPointer(ctx, "gone", "", "tri", "me"); err != nil {
		t.Fatalf("store pointer on a depot without a readable checkout: %v", err)
	}
	g, err := s2.CreateBead(ctx, store.NewBead{Title: "g", Depot: "gone", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.SetFlowPointer(ctx, "gone", "", "", "me"); err == nil || !strings.Contains(err.Error(), "resolving base main") || !strings.Contains(err.Error(), g.ID) {
		t.Fatalf("clear on a depot without a readable checkout: %v", err)
	}
}

// A repository flow that loses a stage on the base leaves the open beads in
// it stranded: no command could refuse the commit, so Stranded names them,
// with the flow and the commit that lacks their stage. Such a bead leaves
// only by an explicit advance to the flow's first stage.
func TestStrandedByRepoFlowChange(t *testing.T) {
	t.Parallel()
	s, ctx, dir := repoDepot(t)
	create := func(nb store.NewBead) *store.Bead {
		t.Helper()
		b, err := s.CreateBead(ctx, nb, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	inReview := create(store.NewBead{Title: "in review", Depot: "web", Stage: "review"})
	create(store.NewBead{Title: "in new", Depot: "web"})
	pinned := create(store.NewBead{Title: "pins alt", Depot: "web", Metadata: map[string]any{"flow": "alt"}})
	closed := create(store.NewBead{Title: "closed in review", Depot: "web", Stage: "review"})
	if _, err := s.CloseBead(ctx, closed.ID, "dropped", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Stranded(ctx, "web"); err != nil || len(got) != 0 {
		t.Fatalf("stranded before the flow changed = %+v, %v", got, err)
	}

	// The base drops review from the depot's flow, and new from the pinned one.
	sha := commitFiles(t, dir, map[string]string{
		".yardr/flows/default.flow": "new -> done\ndone terminal\n",
		".yardr/flows/alt.flow":     "start -> alt\nalt terminal\n",
	})
	want := []store.StrandedBead{
		{Bead: inReview.ID, Stage: "review", Flow: "default", Source: store.FlowSource{Kind: "repo", Commit: sha, Path: ".yardr/flows/default.flow"}},
		{Bead: pinned.ID, Stage: "new", Flow: "alt", Source: store.FlowSource{Kind: "pin", Commit: sha, Path: ".yardr/flows/alt.flow"}},
	}
	slices.SortFunc(want, func(a, b store.StrandedBead) int { return strings.Compare(a.Bead, b.Bead) })
	for _, depot := range []string{"web", ""} {
		if got, err := s.Stranded(ctx, depot); err != nil || !slices.Equal(got, want) {
			t.Fatalf("Stranded(%q) = %+v, %v; want %+v", depot, got, err, want)
		}
	}
	if got, err := s.Stranded(ctx, "elsewhere"); err != nil || len(got) != 0 {
		t.Fatalf("stranded in another depot = %+v, %v", got, err)
	}
	byRepo := want[slices.IndexFunc(want, func(sb store.StrandedBead) bool { return sb.Bead == inReview.ID })]
	if text := byRepo.String(); text != inReview.ID+" is in stage review, which flow default (repo@"+sha[:7]+") lacks" {
		t.Fatalf("StrandedBead text = %q", text)
	}

	// From a stage the flow does not know, the first stage is the way back;
	// skipping ahead to a terminal stage is not.
	if _, err := s.Advance(ctx, inReview.ID, "done", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "allowed next: new") {
		t.Fatalf("advance from an unknown stage to done: %v", err)
	}
	if b, err := s.Advance(ctx, inReview.ID, "new", store.Expect{}, "me"); err != nil || b.Stage != "new" || b.Status != "open" {
		t.Fatalf("advance from an unknown stage to the first: %+v, %v", b, err)
	}
	if got, err := s.Stranded(ctx, "web"); err != nil || len(got) != 1 || got[0].Bead != pinned.ID {
		t.Fatalf("stranded after advancing = %+v, %v", got, err)
	}

	// A flow that does not resolve is bad_flow's to report, not stranded.
	commitFiles(t, dir, map[string]string{".yardr/flows/default.flow": "new => done\n", ".yardr/flows/alt.flow": repoAlt})
	if got, err := s.Stranded(ctx, "web"); err != nil || len(got) != 0 {
		t.Fatalf("stranded under a flow that does not parse = %+v, %v", got, err)
	}
}

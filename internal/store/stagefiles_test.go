package store_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// stageFileOf is the stage file NextStages reports for bead id: its path
// and text, or "" when the stage has none.
func stageFileOf(t *testing.T, s store.API, id string) (string, string) {
	t.Helper()
	next, err := s.NextStages(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if next.StageFile == nil {
		return "", ""
	}
	return next.StageFile.Path, next.StageFile.Text
}

// A stage file is read with the flow it belongs to, from the tip of the
// base: the default flow's, a type flow's, a pinned flow's and a store
// flow's each from the directory of that flow's name. A file on a bead's
// branch or in the working tree is not read until it lands.
func TestStageFilesReadFromBase(t *testing.T) {
	s, ctx, dir := repoRig(t)
	commitFiles(t, dir, map[string]string{
		".yardr/flows/default/new.md":       "Build it.\n",
		".yardr/flows/default/done.md":      " \n", // nothing in it: no stage file
		".yardr/flows/type/decision/ask.md": "Answer it.\n",
		".yardr/flows/alt/new.md":           "The alt way.\n",
		".yardr/flows/README.md":            "not a stage file\n",
	})
	task, err := s.CreateBead(ctx, store.NewBead{Title: "t", Rig: "web"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CreateBead(ctx, store.NewBead{Title: "q", Rig: "web", Type: "decision", Stage: "ask"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.CreateBead(ctx, store.NewBead{Title: "p", Rig: "web", Metadata: map[string]any{"flow": "alt"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, path, text string }{
		{task.ID, ".yardr/flows/default/new.md", "Build it.\n"},
		{q.ID, ".yardr/flows/type/decision/ask.md", "Answer it.\n"},
		{pinned.ID, ".yardr/flows/alt/new.md", "The alt way.\n"},
	} {
		if path, text := stageFileOf(t, s, c.id); path != c.path || text != c.text {
			t.Errorf("stage file of %s = %q %q, want %q %q", c.id, path, text, c.path, c.text)
		}
	}

	// The same stage name in another flow is another file: review of
	// default has none, though decision's ask and alt's new do.
	if _, err := s.Advance(ctx, task.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if path, _ := stageFileOf(t, s, task.ID); path != "" {
		t.Errorf("review has no stage file, got %s", path)
	}

	// A bead's branch that writes review's stage file, checked out in the
	// rig with an uncommitted edit to another on top: neither is read.
	runGit(t, dir, "checkout", "-q", "-b", "web/"+task.ID)
	commitFiles(t, dir, map[string]string{".yardr/flows/default/review.md": "Wave it through.\n"})
	if err := os.WriteFile(filepath.Join(dir, ".yardr/flows/alt/new.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if path, _ := stageFileOf(t, s, task.ID); path != "" {
		t.Errorf("a stage file on the bead's branch was read: %s", path)
	}
	if _, text := stageFileOf(t, s, pinned.ID); text != "The alt way.\n" {
		t.Errorf("the working tree's stage file was read: %q", text)
	}

	// Landed on the base, it is the stage's file.
	runGit(t, dir, "checkout", "-q", "-f", "main")
	commitFiles(t, dir, map[string]string{".yardr/flows/default/review.md": "Read the diff.\n"})
	if path, text := stageFileOf(t, s, task.ID); path != ".yardr/flows/default/review.md" || text != "Read the diff.\n" {
		t.Errorf("after landing: %q %q", path, text)
	}

	// A flow in the store has no stage files in any repository: a rig that
	// follows it by pointer is not asked for them, whatever it holds under
	// the flow's name. They are in the yard's home (TestHomeStageFiles).
	f, err := store.ParseFlow("new -> review\nreview -> shipped\nshipped terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "code"
	if _, err := s.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "web", "task", "code", "me"); err != nil {
		t.Fatal(err)
	}
	commitFiles(t, dir, map[string]string{".yardr/flows/code/review.md": "The rig's own.\n"})
	next, err := s.NextStages(ctx, task.ID)
	if err != nil || next.StageFile != nil || next.Flow.Name != "code" || !next.Source.InStore() {
		t.Fatalf("a store flow read a stage file from the rig: %+v, %v", next, err)
	}
	if next, err = s.NextStages(ctx, q.ID); err != nil || next.Source.InStore() {
		t.Fatalf("a repository flow counted as the store's: %+v, %v", next, err)
	}

	// RigFlows says which stages have one; an empty file is none.
	flows, err := s.RigFlows(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, rf := range flows {
		want := map[string]string{"new": ".yardr/flows/default/new.md", "review": ".yardr/flows/default/review.md"}
		switch rf.Type {
		case "decision":
			want = map[string]string{"ask": ".yardr/flows/type/decision/ask.md"}
		case "task":
			want = nil
		}
		if !maps.Equal(rf.StageFiles, want) {
			t.Errorf("flow %s stage files = %v, want %v", rf.Flow.Name, rf.StageFiles, want)
		}
	}
	if len(flows) != 3 {
		t.Errorf("rig flows = %+v", flows)
	}

	// A rig with no stage files, and a bead with no rig, have none.
	if err := s.PutRig(ctx, store.Rig{Name: "bare", Path: gitRepo(t, map[string]string{".yardr/flows/default.flow": repoDefault}), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	bare, err := s.CreateBead(ctx, store.NewBead{Title: "b", Rig: "bare"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if next, err = s.NextStages(ctx, bare.ID); err != nil || next.StageFile != nil || next.Flow == nil {
		t.Fatalf("rig without stage files: %+v, %v", next, err)
	}
}

// A dir rig's stage files are read from its directory as they are, and
// reread when one changes or appears. A stage whose name holds a slash has
// its file a directory deeper.
func TestDirRigStageFiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".yardr/flows/default.flow":   "new -> qa/smoke\nqa/smoke -> done\ndone terminal\n",
		".yardr/flows/default/new.md": "Write it.\n",
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
	if path, text := stageFileOf(t, s, b.ID); path != ".yardr/flows/default/new.md" || text != "Write it.\n" {
		t.Fatalf("dir rig stage file = %q %q", path, text)
	}
	// Same size, so only the mtime tells.
	writeFiles(t, dir, map[string]string{".yardr/flows/default/new.md": "Draw it.\n"})
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(dir, ".yardr/flows/default/new.md"), later, later); err != nil {
		t.Fatal(err)
	}
	if _, text := stageFileOf(t, s, b.ID); text != "Draw it.\n" {
		t.Fatalf("after an edit: %q", text)
	}
	if _, err := s.Advance(ctx, b.ID, "qa/smoke", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if path, _ := stageFileOf(t, s, b.ID); path != "" {
		t.Fatalf("qa/smoke has no file yet: %s", path)
	}
	writeFiles(t, dir, map[string]string{".yardr/flows/default/qa/smoke.md": "Smoke it.\n"})
	if path, text := stageFileOf(t, s, b.ID); path != ".yardr/flows/default/qa/smoke.md" || text != "Smoke it.\n" {
		t.Fatalf("a new stage file: %q %q", path, text)
	}
}

// A stage file named after no stage of its flow, or in a directory with no
// flow file beside it, is stray; the files of a type flow and of a flow
// that does not parse are not.
func TestStrayStageFiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		".yardr/flows/default.flow":         repoDefault,
		".yardr/flows/type/decision.flow":   repoDecision,
		".yardr/flows/broken.flow":          "new => review\n",
		".yardr/flows/README.md":            "about the flows",
		".yardr/flows/type/README.md":       "about the type flows",
		".yardr/flows/default/new.md":       "ok",
		".yardr/flows/default/done.md":      "ok: a terminal stage is a stage",
		".yardr/flows/default/reveiw.md":    "typo",
		".yardr/flows/type/decision/ask.md": "ok",
		".yardr/flows/type/decision/new.md": "default's stage, not decision's",
		".yardr/flows/type/nope/ask.md":     "no such type flow",
		".yardr/flows/defualt/new.md":       "typo in the flow",
		".yardr/flows/broken/anything.md":   "its flow cannot say",
	})
	tree, err := store.ReadFlowDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := tree.StrayStageFiles()
	slices.Sort(got)
	const hint = " for it to belong to (a flow in the store has its stage files in the yard's home, <home>/flows/<name>/<stage>.md)"
	want := []string{
		".yardr/flows/default/reveiw.md: flow default has no stage reveiw; stages: new, review, done",
		".yardr/flows/defualt/new.md: there is no .yardr/flows/defualt.flow" + hint,
		".yardr/flows/type/decision/new.md: flow type/decision has no stage new; stages: ask, answered",
		".yardr/flows/type/nope/ask.md: there is no .yardr/flows/type/nope.flow" + hint,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("stray stage files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A store flow's stage files are plain files in the yard's home,
// <home>/flows/<name>/<stage>.md, read as they are on disk. A file named
// after no stage of the flow is stray, and a stage name cannot lead out of
// the flow's directory.
func TestHomeStageFiles(t *testing.T) {
	home := t.TempDir()
	writeFiles(t, home, map[string]string{
		"flows/code/new.md":      "Build it.\n",
		"flows/code/qa/smoke.md": "Smoke it.\n",
		"flows/code/shipped.md":  "\n", // nothing in it: no stage file
		"flows/code/reveiw.md":   "typo",
		"flows/code/notes.txt":   "not markdown",
		"flows/other/new.md":     "another flow's",
		"secret.md":              "outside the flows",
	})
	code := &store.Flow{Name: "code", Stages: []string{"new", "review", "qa/smoke", "shipped", "../../secret"}}
	sf, err := store.HomeStageFile(home, "code", "new")
	if err != nil || sf == nil || sf.Text != "Build it.\n" || sf.Path != filepath.Join(home, "flows/code/new.md") {
		t.Fatalf("new = %+v, %v", sf, err)
	}
	for _, c := range []struct{ home, flow, stage string }{
		{home, "code", "review"},       // no file
		{home, "code", "shipped"},      // an empty one
		{home, "code", "../../secret"}, // would leave the flow's directory
		{home, "../flows/code", "new"}, // no flow name
		{home, "nope", "new"},
		{"", "code", "new"},
	} {
		if sf, err := store.HomeStageFile(c.home, c.flow, c.stage); err != nil || sf != nil {
			t.Errorf("HomeStageFile(%q, %q, %q) = %+v, %v; want none", c.home, c.flow, c.stage, sf, err)
		}
	}
	have, stray, err := store.HomeStageFiles(home, code)
	want := map[string]string{"new": filepath.Join(home, "flows/code/new.md"), "qa/smoke": filepath.Join(home, "flows/code/qa/smoke.md")}
	if err != nil || !maps.Equal(have, want) {
		t.Fatalf("have = %v, %v; want %v", have, err, want)
	}
	if len(stray) != 1 || stray[0] != filepath.Join(home, "flows/code/reveiw.md")+": flow code has no stage reveiw; stages: new, review, qa/smoke, shipped, ../../secret" {
		t.Fatalf("stray = %q", stray)
	}
	// A flow with no directory in the home has none, and nothing stray.
	if have, stray, err = store.HomeStageFiles(home, &store.Flow{Name: "bare", Stages: []string{"new"}}); err != nil || len(have)+len(stray) != 0 {
		t.Fatalf("no directory: %v %v %v", have, stray, err)
	}
}

package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/roles"
)

// depot init seeds a Go repo with the roles naming the yard's operator, a gate
// guessed from go.mod and the starter flow; a second run changes nothing.
func TestDepotInit(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"operator": "Jo", "yardmaster": "boss"} {
		if err := st.SetSetting(ctx, k, v, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.depotCmd(), "init", "web")
	if err != nil {
		t.Fatal(err)
	}

	files := []string{".yardr/roles/builder.md", ".yardr/roles/yardmaster.md", ".yardr/roles/merger.md",
		".yardr/roles/reviewer.md", ".yardr/check", ".yardr/flows/default.flow",
		".yardr/flows/default/backlog.md", ".yardr/flows/default/new.md", ".yardr/flows/default/review.md",
		".yardr/flows/default/approved.md", ".yardr/flows/default/decide.md"}
	for _, f := range files {
		if !strings.Contains(out, f) {
			t.Errorf("output does not name %s:\n%s", f, out)
		}
	}
	for _, s := range []string{"Nothing is committed", "origin/main", "go vet ./... && go test ./..."} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	for _, name := range []string{"yardmaster.md", "builder.md", "merger.md", "reviewer.md"} {
		tmpl, _ := roles.FS.ReadFile(name)
		got, err := os.ReadFile(filepath.Join(dir, ".yardr", "roles", name))
		if err != nil || !bytes.Equal(got, fillRole(tmpl, "Jo", "boss")) {
			t.Fatalf("%s: %v\n%s", name, err, got)
		}
		if bytes.Contains(got, []byte("{{")) {
			t.Errorf("%s keeps a placeholder:\n%s", name, got)
		}
	}
	check := filepath.Join(dir, ".yardr", "check")
	gate, _ := os.ReadFile(check)
	if !strings.HasPrefix(string(gate), "#!/bin/sh\n# The merge gate of depot web") ||
		!strings.Contains(string(gate), "\ngo vet ./... && go test ./...\n") {
		t.Fatalf("check:\n%s", gate)
	}
	if fi, _ := os.Stat(check); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("check is not executable: %v", fi.Mode())
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".yardr", "flows", "default.flow"))
	f, err := store.ParseFlow(string(body))
	if err != nil {
		t.Fatalf("starter flow: %v\n%s", err, body)
	}
	for _, s := range []string{"backlog", "new", "review", "approved", "merged", "decide"} {
		if !slices.Contains(f.Stages, s) {
			t.Errorf("flow has no stage %s: %v", s, f.Stages)
		}
	}

	// Again, with one file edited and --operator: nothing changes.
	if err := os.WriteFile(check, []byte("make test\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, dir)
	out, err = run(t, a.depotCmd(), "init", "web", "--operator", "Kim")
	if err != nil || !strings.Contains(out, "nothing written") {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if again := snapshot(t, dir); !maps.Equal(snap, again) {
		t.Fatal("second run changed files")
	}

	if _, err := run(t, a.depotCmd(), "init", "nope"); exitCode(err) != 3 {
		t.Fatalf("unknown depot: %v", err)
	}
}

// --operator names who the roles bring decisions to; without it or the
// setting, the login user.
func TestDepotInitOperator(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	t.Setenv("USER", "kim")
	for name, args := range map[string][]string{"kim": nil, "Ana": {"--operator", "Ana"}} {
		dir := t.TempDir()
		if err := st.PutDepot(ctx, store.Depot{Name: "r" + strings.ToLower(name), Path: dir, Base: "main"}, "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := run(t, (&app{st: st}).depotCmd(), append([]string{"init", "r" + strings.ToLower(name)}, args...)...); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, ".yardr", "roles", "yardmaster.md")); !bytes.Contains(b, []byte("bring to "+name)) {
			t.Errorf("yardmaster.md does not name %s", name)
		}
	}
}

// The yardmaster the roles tell agents to mail is the yard's: --yardmaster,
// else the yardmaster setting yardr init stores, else the one crew member of a
// yard from before the setting, else init's default name. A yard with several
// crew members and no setting does not guess among them.
func TestDepotInitYardmaster(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	mailTo := func(t *testing.T, dir string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, ".yardr", "roles", "builder.md"))
		if err != nil {
			t.Fatal(err)
		}
		_, after, ok := strings.Cut(string(b), "yardr mail ")
		if !ok {
			t.Fatalf("builder.md does not mail the yardmaster:\n%s", b)
		}
		return strings.Fields(after)[0]
	}
	initDepot := func(t *testing.T, name string, args ...string) string {
		t.Helper()
		dir := t.TempDir()
		if err := st.PutDepot(ctx, store.Depot{Name: name, Path: dir, Base: "main"}, "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := run(t, (&app{st: st}).depotCmd(), append([]string{"init", name}, args...)...); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if got := mailTo(t, initDepot(t, "empty")); got != "yardmaster" {
		t.Errorf("empty yard: mail %s, want yardmaster", got)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := mailTo(t, initDepot(t, "one")); got != "boss" {
		t.Errorf("one crew member: mail %s, want boss", got)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := mailTo(t, initDepot(t, "two")); got != "yardmaster" {
		t.Errorf("two crew members, no setting: mail %s, want yardmaster", got)
	}
	if _, err := run(t, (&app{st: st}).settingsCmd(), "set", "yardmaster", "boss"); err != nil {
		t.Fatal(err)
	}
	if got := mailTo(t, initDepot(t, "set")); got != "boss" {
		t.Errorf("yardmaster setting: mail %s, want boss", got)
	}
	if got := mailTo(t, initDepot(t, "flag", "--yardmaster", "boss")); got != "boss" {
		t.Errorf("--yardmaster: mail %s, want boss", got)
	}
	if _, err := run(t, (&app{st: st}).settingsCmd(), "set", "yardmaster", "no spaces"); err == nil {
		t.Error("settings set yardmaster accepted a name a crew member cannot have")
	}
}

// The gate is guessed from the repo: npm's placeholder test script is none.
func TestGuessGate(t *testing.T) {
	for _, tc := range []struct {
		files map[string]string
		gate  string
	}{
		{map[string]string{"go.mod": "module x\n"}, "go vet ./... && go test ./..."},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest run"}}`}, "npm test"},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest"}}`, "pnpm-lock.yaml": ""}, "pnpm test"},
		{map[string]string{"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`}, ""},
		{map[string]string{"package.json": `{}`}, ""},
		{nil, ""},
	} {
		dir := t.TempDir()
		for n, b := range tc.files {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(b), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gate, script := guessGate(store.Depot{Name: "r", Path: dir, Base: "main"})
		if gate != tc.gate {
			t.Errorf("%v: gate %q, want %q", tc.files, gate, tc.gate)
		}
		if gate == "" && (!strings.Contains(script, "TODO") || !strings.HasSuffix(script, "exit 0\n")) {
			t.Errorf("%v: no-guess script:\n%s", tc.files, script)
		}
	}
}

// snapshot reads every file under dir outside .git, with its mode.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() && fi.Name() == ".git" {
			return filepath.SkipDir
		}
		if !fi.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			m[p] = fi.Mode().String() + "\n" + string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// depot init on a dir depot writes the builder and reviewer roles, no merger, no
// check, and the dir flows, which govern the depot's tasks and trains from the
// directory without a pointer; the yardmaster's role only with
// --with-yardmaster.
func TestDepotInitDir(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := st.PutDepot(ctx, store.Depot{Name: "invoices", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.depotCmd(), "init", "invoices", "--operator", "Jo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".yardr/flows/default.flow",
		".yardr/flows/default/backlog.md", ".yardr/flows/default/decide.md", ".yardr/flows/default/new.md", ".yardr/flows/default/review.md",
		".yardr/flows/type/train.flow",
		".yardr/flows/type/train/backlog.md", ".yardr/flows/type/train/decide.md", ".yardr/flows/type/train/open.md", ".yardr/flows/type/train/review.md",
		".yardr/roles/builder.md", ".yardr/roles/reviewer.md"}
	var got []string
	for p := range snapshot(t, dir) {
		got = append(got, strings.TrimPrefix(p, dir+"/"))
	}
	if slices.Sort(got); !slices.Equal(got, want) {
		t.Fatalf("files = %v; want %v", got, want)
	}
	for _, s := range []string{"no gate", ".yardr/check", "docs/dir-depots.md", "open --type train"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "git -C") || strings.Contains(out, "Nothing is committed") {
		t.Errorf("a dir depot has nothing to commit:\n%s", out)
	}
	tmpl, _ := roles.FS.ReadFile("builder.md")
	if got, _ := os.ReadFile(filepath.Join(dir, ".yardr", "roles", "builder.md")); !bytes.Equal(got, fillRole(tmpl, "Jo", "yardmaster")) {
		t.Fatalf("builder.md:\n%s", got)
	}
	for path, body := range map[string]string{"default.flow": dirFlow, "type/train.flow": dirTrainFlow} {
		if got, _ := os.ReadFile(filepath.Join(dir, ".yardr", "flows", path)); string(got) != body {
			t.Errorf("%s:\n%s", path, got)
		}
	}

	// The flows govern as they are: no pointer in the store, the train's from
	// its type file.
	if ptrs, err := st.FlowPointers(ctx); err != nil || len(ptrs) != 0 {
		t.Fatalf("pointers = %v, %v", ptrs, err)
	}
	for typ, want := range map[string]string{"task": "default (repo (directory))", store.TypeTrain: "type/train (repo (directory), type train)"} {
		b, err := st.CreateBead(ctx, store.NewBead{Title: typ, Depot: "invoices", Type: typ, Stage: "backlog"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		next, err := st.NextStages(ctx, b.ID)
		if err != nil || next.Flow == nil {
			t.Fatalf("%s: %+v, %v", typ, next, err)
		}
		if got := next.Flow.Name + " (" + next.Source.String() + ")"; got != want {
			t.Errorf("%s is governed by %s; want %s", typ, got, want)
		}
	}

	// --with-yardmaster adds the yardmaster's role and keeps the rest.
	out, err = run(t, a.depotCmd(), "init", "invoices", "--with-yardmaster")
	if err != nil || !strings.Contains(out, ".yardr/roles/yardmaster.md\n") || !strings.Contains(out, "builder.md (already there, kept)") {
		t.Fatalf("--with-yardmaster: %v\n%s", err, out)
	}
	snap := snapshot(t, dir)
	if _, ok := snap[filepath.Join(dir, ".yardr/roles/merger.md")]; ok {
		t.Fatal("a dir depot got the merger's role")
	}
	out, err = run(t, a.depotCmd(), "init", "invoices", "--with-yardmaster")
	if err != nil || !strings.Contains(out, "nothing written") || !maps.Equal(snap, snapshot(t, dir)) {
		t.Fatalf("third run: %v\n%s", err, out)
	}
}

// The dir flows, walked: a task is built, reviewed (sent back once, asked
// about once) and done, which closes it; a train waits in open, is reviewed
// as a whole and done.
func TestDirDepotFlowsWalk(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	depot := store.Depot{Name: "invoices", Path: t.TempDir(), Kind: store.DepotDir}
	if err := st.PutDepot(ctx, depot, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := depotInit(depot, "Jo", "boss", false); err != nil {
		t.Fatal(err)
	}
	walk := func(typ string, steps [][2]string) *store.Bead {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: typ, Depot: "invoices", Type: typ, Stage: "backlog"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range steps {
			move, want := s[0], s[1]
			if to, ok := strings.CutPrefix(move, "->"); ok {
				b, err = st.Advance(ctx, b.ID, to, store.Expect{}, "me")
			} else {
				b, err = st.Done(ctx, b.ID, move, "", store.Expect{}, "me")
			}
			if err != nil || b.Stage != want {
				t.Fatalf("%s: %s from %v: %v; want %s", typ, move, b, err, want)
			}
		}
		return b
	}
	task := walk("task", [][2]string{
		{"->new", "new"}, {"done", "review"}, {"changes", "new"}, {"done", "review"},
		{"question", "decide"}, {"->new", "new"}, {"done", "review"}, {"done", "done"},
	})
	if task.Status != "closed" {
		t.Fatalf("a done task is %s", task.Status)
	}
	train := walk(store.TypeTrain, [][2]string{
		{"->open", "open"}, {"done", "review"}, {"changes", "open"}, {"done", "review"},
		{"question", "decide"}, {"->open", "open"}, {"done", "review"}, {"done", "done"},
	})
	if train.Status != "closed" {
		t.Fatalf("a done train is %s", train.Status)
	}

	// Neither flow has the git stages: nothing merges in a dir depot.
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "invoices"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Advance(ctx, b.ID, "approved", store.Expect{}, "me"); err == nil {
		t.Fatal("a dir depot's task reached approved")
	}
}

// depot init writes a stage file for every stage of the flows it writes that
// is not terminal, names each in its listing, and fills in the names; the
// depot's check then has no stage to name as lacking one and no stray file,
// for the git starter (once committed on the base) and the dir starter.
func TestDepotInitStageFiles(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	repo, folder := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	depots := map[string]store.Depot{
		"git": {Name: "web", Path: repo, Base: "main"},
		"dir": {Name: "invoices", Path: folder, Kind: store.DepotDir},
	}
	want := map[string][]string{
		"git": {"default/approved.md", "default/backlog.md", "default/decide.md", "default/new.md", "default/review.md"},
		"dir": {"default/backlog.md", "default/decide.md", "default/new.md", "default/review.md",
			"type/train/backlog.md", "type/train/decide.md", "type/train/open.md", "type/train/review.md"},
	}
	a := &app{st: st, actor: "me"}
	for kind, depot := range depots {
		if err := st.PutDepot(ctx, depot, "me"); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, a.depotCmd(), "init", depot.Name, "--operator", "Jo")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for p, body := range snapshot(t, depot.Path) {
			rel, ok := strings.CutPrefix(p, filepath.Join(depot.Path, ".yardr", "flows")+"/")
			if !ok || !strings.HasSuffix(rel, ".md") {
				continue
			}
			got = append(got, rel)
			// Once, as written: a stage several rules name is one file.
			if n := strings.Count(out, ".yardr/flows/"+rel); n != 1 || !strings.Contains(out, "  .yardr/flows/"+rel+"\n") {
				t.Errorf("%s: the listing names %s %d times, or not as written:\n%s", kind, rel, n, out)
			}
			if strings.Contains(body, "{{") || strings.TrimSpace(body) == "" {
				t.Errorf("%s: %s is empty or keeps a placeholder:\n%s", kind, rel, body)
			}
			if strings.HasSuffix(rel, "decide.md") && !strings.Contains(body, "Jo decides") {
				t.Errorf("%s: %s does not name the operator:\n%s", kind, rel, body)
			}
		}
		if slices.Sort(got); !slices.Equal(got, want[kind]) {
			t.Errorf("%s: stage files = %v; want %v", kind, got, want[kind])
		}
		// Every embedded starter was written: none is for a stage no starter
		// flow has.
		var embedded int
		if err := fs.WalkDir(roles.Stages, "stages/"+kind, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				embedded++
			}
			return err
		}); err != nil || embedded != len(want[kind]) {
			t.Errorf("%s: %d embedded stage files, %v; want %d", kind, embedded, err, len(want[kind]))
		}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "starter")

	for kind, depot := range depots {
		governing, err := st.DepotFlows(ctx, depot.Name)
		if wantFlows := map[string]int{"git": 1, "dir": 2}[kind]; err != nil || len(governing) != wantFlows {
			t.Fatalf("%s: %d governing flows, %v; want %d", kind, len(governing), err, wantFlows)
		}
		notes, stray, strayHome, err := stageFileFindings(ctx, st, depot, depot.Path, t.TempDir())
		if err != nil || len(notes)+len(stray)+len(strayHome) != 0 {
			t.Errorf("%s: notes %v, stray %v, stray in home %v, %v; want none", kind, notes, stray, strayHome, err)
		}
		// Not for want of looking: a stage file removed is a stage named.
		if err := os.Remove(filepath.Join(depot.Path, ".yardr", "flows", "default", "review.md")); err != nil {
			t.Fatal(err)
		}
		notes, _, _, err = stageFileFindings(ctx, st, depot, depot.Path, t.TempDir())
		if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "flow default has no stage file for review ") {
			t.Errorf("%s: without review.md: notes %v, %v", kind, notes, err)
		}
	}
}

// A flow already in the depot is the depot's own: depot init keeps it and writes
// no starter stage files beside it, which could name stages it lacks and
// fail the depot's check.
func TestDepotInitKeepsAFlowWithoutStageFiles(t *testing.T) {
	st := storetest.New(t)
	dir := t.TempDir()
	depot := store.Depot{Name: "web", Path: dir, Base: "main"}
	if err := st.PutDepot(context.Background(), depot, "me"); err != nil {
		t.Fatal(err)
	}
	flow := filepath.Join(dir, ".yardr", "flows", "default.flow")
	if err := os.MkdirAll(filepath.Dir(flow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flow, []byte("todo -> shipped\nshipped terminal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := depotInit(depot, "Jo", "boss", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Kept, ".yardr/flows/default.flow") {
		t.Fatalf("kept = %v", res.Kept)
	}
	for _, p := range slices.Concat(res.Written, res.Kept) {
		if strings.HasPrefix(p, ".yardr/flows/default/") {
			t.Errorf("stage file %s beside a flow that was kept", p)
		}
	}
	tree, err := store.ReadFlowDir(dir)
	if err != nil || len(tree.StageFiles) != 0 || len(tree.StrayStageFiles()) != 0 {
		t.Fatalf("stage files = %v, stray %v, %v", tree.StageFiles, tree.StrayStageFiles(), err)
	}
}

// This repository's own flows: every stage that is not terminal has a stage
// file, and no stage file is an orphan, which is what yardr depot check yardr
// reports of the tree.
func TestThisRepoHasItsStageFiles(t *testing.T) {
	tree, err := store.ReadFlowDir(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if stray := tree.StrayStageFiles(); len(stray) > 0 {
		t.Errorf("stage files that belong to no stage: %v", stray)
	}
	names := map[string]bool{}
	for _, rf := range tree.Flows {
		if rf.Flow == nil {
			t.Fatalf("%s: %s", rf.Path, rf.Error)
		}
		names[rf.Flow.Name] = true
		for _, s := range rf.Flow.Stages {
			if !rf.Flow.IsTerminal(s) && tree.StageFile(rf.Flow.Name, s) == nil {
				t.Errorf("flow %s has no stage file for %s", rf.Flow.Name, s)
			}
		}
	}
	if !names["default"] || !names["yardr.train"] {
		t.Fatalf("flows = %v", names)
	}
}

// A dir depot set up before a train was called that has its trains' flow in
// type/epic.flow, which still governs them: depot init keeps it and writes no
// type/train.flow beside it.
func TestDepotInitDirKeepsTheOldTrainFlow(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	old := filepath.Join(dir, ".yardr", "flows", "type", "epic.flow")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	const own = "backlog -> open\nopen -> done\ndone terminal\n"
	if err := os.WriteFile(old, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "invoices", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.depotCmd(), "init", "invoices", "--operator", "Jo"); err != nil {
		t.Fatal(err)
	}
	if text, err := os.ReadFile(old); err != nil || string(text) != own {
		t.Fatalf("type/epic.flow = %q, %v; want it as it was", text, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".yardr", "flows", "type", "train.flow")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("type/train.flow was written beside type/epic.flow: %v", err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "e", Depot: "invoices", Type: store.TypeTrain}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if m, err := st.FlowMap(ctx, b.ID); err != nil || m == nil || m.Name != "type/epic" {
		t.Fatalf("the train follows %+v, %v; want type/epic", m, err)
	}
}

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// stageYard is a depot "web" on a repository with a default and a type flow,
// a dir depot "api" following the store flow "code", and a yard home.
// stageTemplateDir is the repository stageYard gives every test, made once
// by TestMain: one commit with the flows on main. Making it per test spent
// three git runs each; a test gets a copy, and commits its own files on
// top. Made before the tests run, read only after, so no test assigns it.
var stageTemplateDir string

// buildStageTemplate makes the repository stageYard copies per test.
func buildStageTemplate() (string, error) {
	var err error
	dir, merr := os.MkdirTemp("", "ystage")
	if merr != nil {
		return "", merr
	}
	run := func(args ...string) {
		if err != nil {
			return
		}
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			err = fmt.Errorf("git %v: %v %s", args, cerr, out)
		}
	}
	write := func(files map[string]string) {
		if err != nil {
			return
		}
		for name, text := range files {
			p := filepath.Join(dir, name)
			if merr := os.MkdirAll(filepath.Dir(p), 0o755); merr != nil {
				err = merr
				return
			}
			if werr := os.WriteFile(p, []byte(text), 0o644); werr != nil {
				err = werr
				return
			}
		}
	}
	run("init", "-q", "-b", "main")
	write(map[string]string{
		".yardr/flows/default.flow":       "new -> review\nreview -> merged on done | new on changes\nmerged terminal\n",
		".yardr/flows/type/decision.flow": "ask -> answered\nanswered terminal\n",
	})
	run("add", "-A")
	run("commit", "-q", "-m", "flows")
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// stageRepo is a fresh copy of the stage repository: one commit with the
// flows on main.
func stageRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.CopyFS(repo, os.DirFS(stageTemplateDir)); err != nil {
		t.Fatal(err)
	}
	return repo
}

func stageYard(t *testing.T) (a *app, repo, home string, git func(args ...string) string, write func(root string, files map[string]string)) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	repo, home = stageRepo(t), t.TempDir()
	git = func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write = func(root string, files map[string]string) {
		t.Helper()
		for name, text := range files {
			p := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, r := range []store.Depot{{Name: "web", Path: repo, Base: "main"}, {Name: "api", Path: t.TempDir(), Kind: store.DepotDir}} {
		if err := st.PutDepot(ctx, r, "me"); err != nil {
			t.Fatal(err)
		}
	}
	code, err := store.ParseFlow("new -> review\nreview -> shipped\nshipped terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	code.Name = "code"
	if _, err := st.PutFlow(ctx, *code, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "api", "", "code", "me"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "me", cfg: &config.Config{Home: home}}, repo, home, git, write
}

// prime shows the stage file of the stage a bead is in, as text and in
// --json: a repository flow's from the base (the default flow's and a type
// flow's), a store flow's from the yard's home for every depot following it.
// A file on a branch is not shown, and without stage files prime prints
// what it did before.
func TestPrimeShowsTheStageFile(t *testing.T) {
	t.Parallel()
	a, repo, home, git, write := stageYard(t)
	ctx := context.Background()
	if err := a.st.PutDepot(ctx, store.Depot{Name: "api2"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetFlowPointer(ctx, "api2", "", "code", "me"); err != nil {
		t.Fatal(err)
	}
	bead := func(nb store.NewBead) string {
		t.Helper()
		b, err := a.st.CreateBead(ctx, nb, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	task := bead(store.NewBead{Title: "task", Depot: "web", Stage: "review"})
	ask := bead(store.NewBead{Title: "ask", Depot: "web", Type: "decision", Stage: "ask"})
	api := bead(store.NewBead{Title: "api", Depot: "api", Stage: "review"})
	api2 := bead(store.NewBead{Title: "api2", Depot: "api2", Stage: "review"})
	ids := []string{task, ask, api, api2}
	prime := func(id string, asJSON bool) string {
		t.Helper()
		a.json = asJSON
		defer func() { a.json = false }()
		out, err := run(t, a.primeCmd(), "--bead", id)
		if err != nil {
			t.Fatalf("prime %s: %v", id, err)
		}
		return out
	}

	before := map[string]string{}
	for _, id := range ids {
		before[id], before[id+"json"] = prime(id, false), prime(id, true)
		if strings.Contains(before[id], "STAGE ") || strings.Contains(before[id+"json"], "stage_file") {
			t.Fatalf("prime %s without stage files:\n%s\n%s", id, before[id], before[id+"json"])
		}
	}

	// On a branch only: not shown. Nor is a depot's file for a store flow.
	git("checkout", "-q", "-b", "web/"+task)
	write(repo, map[string]string{
		".yardr/flows/default/review.md": "Wave it through.\n",
		".yardr/flows/code/review.md":    "The depot's own.\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "branch stage file")
	for _, id := range ids {
		if got := prime(id, false); got != before[id] {
			t.Errorf("prime %s changed by a stage file on a branch:\n%s\nwas:\n%s", id, got, before[id])
		}
		if got := prime(id, true); got != before[id+"json"] {
			t.Errorf("prime --json %s changed by a stage file on a branch:\n%s", id, got)
		}
	}

	git("checkout", "-q", "-f", "main")
	write(repo, map[string]string{
		".yardr/flows/default/review.md":    "Read the whole diff.\n\n`changes` sends it back.\n",
		".yardr/flows/type/decision/ask.md": "Answer it.\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "stage files")
	write(home, map[string]string{"flows/code/review.md": "Review for every depot.\n"})
	codeFile := filepath.Join(home, "flows/code/review.md")
	for _, c := range []struct{ id, stage, path, text, shown string }{
		{task, "review", ".yardr/flows/default/review.md", "Read the whole diff.\n\n`changes` sends it back.\n", "  Read the whole diff.\n  \n  `changes` sends it back.\n\nOUTCOMES:"},
		{ask, "ask", ".yardr/flows/type/decision/ask.md", "Answer it.\n", "  Answer it.\n\nOUTCOMES:"},
		{api, "review", codeFile, "Review for every depot.\n", "  Review for every depot.\n\nOUTCOMES:"},
		{api2, "review", codeFile, "Review for every depot.\n", "  Review for every depot.\n\nOUTCOMES:"},
	} {
		if out, want := prime(c.id, false), "\nSTAGE "+c.stage+" ("+c.path+"):\n"+c.shown; !strings.Contains(out, want) {
			t.Errorf("prime %s:\n%s\nwant it to hold:\n%s", c.id, out, want)
		}
		var v struct {
			Next struct {
				StageFile store.StageFile `json:"stage_file"`
			} `json:"next"`
		}
		if err := json.Unmarshal([]byte(prime(c.id, true)), &v); err != nil || v.Next.StageFile != (store.StageFile{Path: c.path, Text: c.text}) {
			t.Errorf("prime --json %s: next.stage_file = %+v, %v", c.id, v.Next.StageFile, err)
		}
	}
}

// prime shows the stage file without its ## Ask section; --json keeps the
// whole file.
func TestPrimeLeavesOutTheAskSection(t *testing.T) {
	t.Parallel()
	a, repo, _, git, write := stageYard(t)
	ctx := context.Background()
	text := "Read the whole diff.\n\n## Ask\nkind: choice\nquestion: Where next?\ncontext:\n- The diff is green.\ndone: Merge it.\nchanges: Send it back.\n"
	write(repo, map[string]string{".yardr/flows/default/review.md": text})
	git("add", "-A")
	git("commit", "-q", "-m", "ask template")
	b, err := a.st.CreateBead(ctx, store.NewBead{Title: "task", Depot: "web", Stage: "review"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.primeCmd(), "--bead", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\nSTAGE review (.yardr/flows/default/review.md):\n  Read the whole diff.\n") {
		t.Errorf("prime lacks the stage's prose:\n%s", out)
	}
	for _, want := range []string{"## Ask", "kind: choice", "Where next?", "Send it back."} {
		if strings.Contains(out, want) {
			t.Errorf("prime shows the ask template %q:\n%s", want, out)
		}
	}
	a.json = true
	defer func() { a.json = false }()
	out, err = run(t, a.primeCmd(), "--bead", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Next struct {
			StageFile store.StageFile `json:"stage_file"`
		} `json:"next"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Next.StageFile.Text != text {
		t.Errorf("prime --json: next.stage_file.text = %q, %v", v.Next.StageFile.Text, err)
	}
}

// flow show says, per stage, whether it has a stage file: for a depot's
// flows in the FILE column, for a named flow with where its files live.
func TestFlowShowNamesStageFiles(t *testing.T) {
	t.Parallel()
	a, repo, home, git, write := stageYard(t)
	write(repo, map[string]string{".yardr/flows/default/review.md": "Read the diff.\n"})
	git("add", "-A")
	git("commit", "-q", "-m", "stage file")
	write(home, map[string]string{"flows/code/new.md": "Build.\n", "flows/code/reveiw.md": "typo\n"})

	views, err := flowViews(context.Background(), a.st, "web", home)
	if err != nil || len(views) != 2 {
		t.Fatalf("views = %+v, %v", views, err)
	}
	for _, s := range views[0].Stages {
		if want := map[string]string{"review": ".yardr/flows/default/review.md"}[s.Stage]; s.StageFile != want {
			t.Errorf("web %s: stage file %q, want %q", s.Stage, s.StageFile, want)
		}
	}
	out, err := run(t, a.flowCmd(), "show", "web")
	if err != nil || !strings.Contains(out, "FILE") {
		t.Fatalf("flow show web: %q, %v", out, err)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || (f[0] != "new" && f[0] != "review" && f[0] != "merged") {
			continue
		}
		if want := map[string]string{"new": "-", "review": "yes", "merged": "-"}[f[0]]; f[4] != want {
			t.Errorf("flow show web, stage %s: FILE %q, want %q:\n%s", f[0], f[4], want, out)
		}
	}

	// Nothing on the base is stray yet, and the home's stray file is another
	// flow's: web is warned of no stage file.
	if strings.Contains(out, "warning: stage file") {
		t.Errorf("flow show web warns of a stage file with none stray:\n%s", out)
	}

	// A store flow's are in the home; one named after no stage is warned of.
	stray := filepath.Join(home, "flows/code/reveiw.md") + ": flow code has no stage reveiw; stages: new, review, shipped"
	views, err = flowViews(context.Background(), a.st, "api", home)
	if err != nil || len(views) != 1 || views[0].Stages[0].StageFile != filepath.Join(home, "flows/code/new.md") || views[0].Stages[1].StageFile != "" {
		t.Fatalf("api views = %+v, %v", views, err)
	}
	if out, err = run(t, a.flowCmd(), "show", "api"); err != nil || !strings.Contains(out, "warning: stage file "+stray) {
		t.Fatalf("flow show api: %q, %v", out, err)
	}
	out, err = run(t, a.flowCmd(), "show", "code")
	if err != nil || !strings.Contains(out, "stage files: new ("+filepath.Join(home, "flows/code/<stage>.md")+")") || !strings.Contains(out, "warning: stage file "+stray) {
		t.Fatalf("flow show code: %q, %v", out, err)
	}
}

// flow show warns of a stage file whose ## Ask section is broken beside the
// file: one that does not parse, or whose outcomes do not resolve under the
// flow. A valid template warns of nothing.
func TestFlowShowWarnsOfBrokenAskTemplate(t *testing.T) {
	t.Parallel()
	a, repo, home, git, write := stageYard(t)
	write(repo, map[string]string{
		".yardr/flows/default/review.md":    "Read the diff.\n\n## Ask\nkind: choce\nquestion: Q?\ncontext:\n- Why.\n",
		".yardr/flows/type/decision/ask.md": "Answer it.\n\n## Ask\nkind: input\nquestion: What was decided?\ncontext:\n- The bead waits.\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "ask templates")
	write(home, map[string]string{
		"flows/code/review.md": "Review for every depot.\n\n## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\nship: Away.\n",
		"flows/code/new.md":    "Build.\n\n## Ask\nkind: input\nquestion: What was decided?\ncontext:\n- The bead waits.\n",
	})

	// The yard has no routes, so every stage warns of that too; only the
	// stage file warnings are this test's.
	fileWarns := func(v flowView) []string {
		var out []string
		for _, w := range v.Warnings {
			if strings.HasPrefix(w, "stage file ") {
				out = append(out, w)
			}
		}
		return out
	}
	views, err := flowViews(context.Background(), a.st, "web", home)
	if err != nil || len(views) != 2 {
		t.Fatalf("views = %+v, %v", views, err)
	}
	broken := "stage file .yardr/flows/default/review.md: ask line 4: kind \"choce\": want choice, input or check"
	if got := fileWarns(views[0]); !slices.Equal(got, []string{broken}) {
		t.Errorf("default stage file warnings = %q, want %q", got, broken)
	}
	// The type flow's valid template warns of nothing.
	if got := fileWarns(views[1]); len(got) != 0 {
		t.Errorf("decision stage file warnings = %q, want none", got)
	}
	if out, err := run(t, a.flowCmd(), "show", "web"); err != nil || !strings.Contains(out, "warning: "+broken) {
		t.Fatalf("flow show web: %q, %v", out, err)
	}

	// A store flow's broken template warns under the depot and the flow:
	// review's option names no outcome of the flow.
	want := "flow code has no outcome ship for stage review"
	views, err = flowViews(context.Background(), a.st, "api", home)
	if err != nil || len(views) != 1 {
		t.Fatalf("api views = %+v, %v", views, err)
	}
	if got := fileWarns(views[0]); len(got) != 1 || !strings.Contains(got[0], filepath.Join(home, "flows/code/review.md")+": ask line 8: "+want) {
		t.Errorf("api stage file warnings = %q", got)
	}
	if out, err := run(t, a.flowCmd(), "show", "api"); err != nil || !strings.Contains(out, "warning: stage file ") || !strings.Contains(out, want) {
		t.Fatalf("flow show api: %q, %v", out, err)
	}
	out, err := run(t, a.flowCmd(), "show", "code")
	if err != nil || !strings.Contains(out, "warning: stage file ") || !strings.Contains(out, want) {
		t.Fatalf("flow show code: %q, %v", out, err)
	}
	a.json = true
	defer func() { a.json = false }()
	out, err = run(t, a.flowCmd(), "show", "code")
	var v struct {
		Broken []string `json:"broken_stage_files"`
	}
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || len(v.Broken) != 1 || !strings.Contains(v.Broken[0], want) {
		t.Fatalf("flow show code --json: %q, %+v, %v", out, v, err)
	}
}

// flow show <depot> warns of a stage file on the depot's base that belongs
// to no stage, under the flow whose directory it is in: a typo in the name,
// or a directory with no flow file beside it. One on a branch is not read.
func TestFlowShowWarnsOfStrayStageFilesOnTheBase(t *testing.T) {
	t.Parallel()
	a, repo, _, git, write := stageYard(t)
	git("checkout", "-q", "-b", "yard/x")
	write(repo, map[string]string{".yardr/flows/default/reveiw.md": "typo\n"})
	git("add", "-A")
	git("commit", "-q", "-m", "a typo on a branch")
	out, err := run(t, a.flowCmd(), "show", "web")
	if err != nil || strings.Contains(out, "warning: stage file") {
		t.Fatalf("flow show web, the typo on a branch only: %q, %v", out, err)
	}

	git("checkout", "-q", "main")
	write(repo, map[string]string{
		".yardr/flows/default/review.md":    "Read the diff.\n",
		".yardr/flows/default/reveiw.md":    "typo\n",
		".yardr/flows/type/decision/new.md": "another flow's stage\n",
		".yardr/flows/code/review.md":       "no code.flow here\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "stray stage files")
	typo := "stage file .yardr/flows/default/reveiw.md: flow default has no stage reveiw; stages: new, review, merged"
	other := "stage file .yardr/flows/type/decision/new.md: flow type/decision has no stage new; stages: ask, answered"
	orphan := "stage file .yardr/flows/code/review.md: there is no .yardr/flows/code.flow for it to belong to"
	views, err := flowViews(context.Background(), a.st, "web", a.home())
	if err != nil || len(views) != 2 {
		t.Fatalf("views = %+v, %v", views, err)
	}
	// The depot's own flow has its typo and the file of no flow; the type's
	// flow has its own.
	has := func(v flowView, want string) bool {
		return slices.ContainsFunc(v.Warnings, func(w string) bool { return strings.HasPrefix(w, want) })
	}
	if !has(views[0], typo) || !has(views[0], orphan) || has(views[0], other) || !has(views[1], other) || has(views[1], typo) {
		t.Errorf("warnings: default %q, decision %q", views[0].Warnings, views[1].Warnings)
	}
	if out, err = run(t, a.flowCmd(), "show", "web"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{typo, other, orphan} {
		if !strings.Contains(out, "warning: "+want) {
			t.Errorf("flow show web lacks %q:\n%s", "warning: "+want, out)
		}
	}
	if n := strings.Count(out, "warning: stage file"); n != 3 {
		t.Errorf("flow show web: %d stage file warnings, want 3:\n%s", n, out)
	}
}

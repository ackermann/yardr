package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// stageYard is a depot "web" on a repository with a default and a type flow,
// a dir depot "api" following the store flow "code", and a yard home.
func stageYard(t *testing.T) (a *app, repo, home string, git func(args ...string) string, write func(root string, files map[string]string)) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	repo, home = t.TempDir(), t.TempDir()
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
	git("init", "-q", "-b", "main")
	write(repo, map[string]string{
		".yardr/flows/default.flow":       "new -> review\nreview -> merged on done | new on changes\nmerged terminal\n",
		".yardr/flows/type/decision.flow": "ask -> answered\nanswered terminal\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "flows")
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

// flow show says, per stage, whether it has a stage file: for a depot's
// flows in the FILE column, for a named flow with where its files live.
func TestFlowShowNamesStageFiles(t *testing.T) {
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

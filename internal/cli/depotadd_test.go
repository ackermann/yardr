package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// depot add takes a checkout for a git depot and any other directory for a
// dir depot, unless --kind says which; show and list name the kind.
func TestDepotAddDetectsKind(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}

	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := run(t, a.depotCmd(), "add", "web", repo, "--base", "main")
	if err != nil || !strings.Contains(out, "(git, base main)") {
		t.Fatalf("add a checkout: %q, %v", out, err)
	}
	docs := t.TempDir()
	if out, err = run(t, a.depotCmd(), "add", "docs", docs); err != nil || !strings.Contains(out, "(dir: no git, no base)") {
		t.Fatalf("add a plain directory: %q, %v", out, err)
	}
	for name, want := range map[string]string{"web": store.DepotGit, "docs": store.DepotDir} {
		if r, err := st.GetDepot(ctx, name); err != nil || r.Kind != want {
			t.Fatalf("%s = %+v, %v; want kind %s", name, r, err, want)
		}
	}
	if r, _ := st.GetDepot(ctx, "docs"); r.Base != "" {
		t.Fatalf("dir depot has base %q", r.Base)
	}
	// --kind dir makes a checkout a dir depot.
	if out, err = run(t, a.depotCmd(), "add", "flat", repo, "--kind", "dir"); err != nil || !strings.Contains(out, "(dir") {
		t.Fatalf("--kind dir on a checkout: %q, %v", out, err)
	}
	if _, err = run(t, a.depotCmd(), "add", "docs", docs, "--base", "main"); err == nil || !strings.Contains(err.Error(), "no base") {
		t.Fatalf("--base on a dir depot: %v", err)
	}
	if _, err = run(t, a.depotCmd(), "add", "docs", docs, "--kind", "git"); err == nil || !strings.Contains(err.Error(), "not a git checkout") {
		t.Fatalf("--kind git on a plain directory: %v", err)
	}
	if _, err = run(t, a.depotCmd(), "add", "docs", docs, "--kind", "svn"); err == nil {
		t.Fatal("--kind svn accepted")
	}
	if _, err = run(t, a.depotCmd(), "add", "x", filepath.Join(docs, "missing")); err == nil {
		t.Fatal("a path that does not exist registered")
	}

	out, err = run(t, a.depotCmd(), "show", "docs")
	if err != nil || !strings.Contains(out, "kind:  dir") || !strings.Contains(out, "base:  none") {
		t.Fatalf("show dir depot: %q, %v", out, err)
	}
	out, err = run(t, a.depotCmd(), "list")
	if err != nil || !strings.Contains(out, "KIND") || !regexp.MustCompile(`docs\s+dir\s`).MatchString(out) || !regexp.MustCompile(`web\s+git\s`).MatchString(out) {
		t.Fatalf("list: %q, %v", out, err)
	}
}

// An open bead in a stage its flow lost on the base is named by flow show,
// under the flow that lacks the stage, with the command that moves it, and
// no longer once it is advanced to a stage the flow has. So is one that
// pins a flow the depot does not otherwise follow.
func TestFlowShowReportsStrandedBeads(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	flow := func(text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(repo, ".yardr/flows"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".yardr/flows/default.flow"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		git("commit", "-q", "-m", "flow")
	}
	git("init", "-q", "-b", "main")
	flow("new -> triage | review\ntriage -> review\nreview -> merged\nmerged terminal\n")
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "in triage", Depot: "web", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if out, err := run(t, a.flowCmd(), "show", "web"); err != nil || strings.Contains(out, b.ID) {
		t.Fatalf("before the flow changed: %q, %v", out, err)
	}

	flow("new -> review\nreview -> merged\nmerged terminal\n")
	sha := git("rev-parse", "--short=7", "HEAD")
	named := "bead " + b.ID + " is in stage triage, which flow default (repo@" + sha + ") lacks"
	views, err := flowViews(ctx, st, "web", "")
	if err != nil || len(views) != 1 {
		t.Fatalf("views = %+v, %v", views, err)
	}
	if !slices.ContainsFunc(views[0].Warnings, func(w string) bool { return strings.HasPrefix(w, named+": it is not routed") }) {
		t.Fatalf("flow show warnings = %q, want one naming %s", views[0].Warnings, b.ID)
	}
	if out, err := run(t, a.flowCmd(), "show", "web"); err != nil || !strings.Contains(out, "warning: "+named) {
		t.Fatalf("flow show: %q, %v", out, err)
	}

	// Advanced to a stage the flow has, nothing is left to report.
	if _, err := st.Advance(ctx, b.ID, "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if views, err = flowViews(ctx, st, "web", ""); err != nil || slices.ContainsFunc(views[0].Warnings, func(w string) bool { return strings.Contains(w, b.ID) }) {
		t.Fatalf("flow show after advancing: %+v, %v", views, err)
	}

	// A bead may pin a flow that is not one of the depot's normal views. Its
	// warning still needs a visible heading under that flow.
	altPath := filepath.Join(repo, ".yardr/flows/alt.flow")
	if err := os.WriteFile(altPath, []byte("lost -> start\nstart -> finish\nfinish terminal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".yardr/flows/alt.flow")
	git("commit", "-q", "-m", "add pinned flow")
	if err := st.PutDepot(ctx, store.Depot{Name: "pin-only", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	pinned, err := st.CreateBead(ctx, store.NewBead{Title: "pinned stranded", Depot: "pin-only", Stage: "lost", Metadata: map[string]any{"flow": "alt"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(altPath, []byte("start -> finish\nfinish terminal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".yardr/flows/alt.flow")
	git("commit", "-q", "-m", "drop pinned stage")
	views, err = flowViews(ctx, st, "pin-only", "")
	if err != nil || !slices.ContainsFunc(views, func(v flowView) bool {
		return v.Flow.Name == "alt" && slices.ContainsFunc(v.Warnings, func(w string) bool { return strings.Contains(w, pinned.ID) })
	}) {
		t.Fatalf("flow show omitted pinned stranded bead: %+v, %v", views, err)
	}
}

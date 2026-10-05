package cli

import (
	"context"
	"errors"
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

// rig check exits 1 for a red gate and 2 when it could not run, and a red
// gate always prints something, whatever --tail says.
func TestRigCheckExitsAndTail(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	put := func(check string) {
		t.Helper()
		if err := st.PutRig(ctx, store.Rig{Name: "r", Path: t.TempDir(), Base: "main", Check: check}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	put("printf 'one\\ntwo\\nthree\\n'; exit 4")
	out, err := run(t, a.rigCheckCmd(), "r", "--tail", "0")
	if exitCode(err) != 1 || !strings.Contains(out, "3 lines of output not shown") || !strings.Contains(out, "FAIL (exit 4)") {
		t.Fatalf("red gate, --tail 0: %q, %v (exit %d)", out, err, exitCode(err))
	}
	if out, _ = run(t, a.rigCheckCmd(), "r", "--tail", "1"); !strings.Contains(out, "three") || strings.Contains(out, "two") {
		t.Fatalf("red gate, --tail 1: %q", out)
	}

	put("exit 1")
	if out, err = run(t, a.rigCheckCmd(), "r"); exitCode(err) != 1 || !strings.Contains(out, "(the check printed nothing)") {
		t.Fatalf("silent red gate: %q, %v", out, err)
	}

	put("sleep 30")
	if out, err = run(t, a.rigCheckCmd(), "r", "--timeout", "200ms"); exitCode(err) != 1 || !strings.Contains(out, "timed out after 200ms") {
		t.Fatalf("timed out gate: %q, %v", out, err)
	}

	// Ended by a signal from outside, either way a shell shows it: 128+N,
	// which the kill at the deadline above is not.
	put("echo ok; kill -TERM $$")
	if out, err = run(t, a.rigCheckCmd(), "r"); exitCode(err) != 143 || !errors.Is(err, errCheckFailed) ||
		!strings.Contains(out, "ok\n") || !strings.Contains(out, "FAIL (exit -1, ended by signal 15)") {
		t.Fatalf("gate killed by SIGTERM: %q, %v (exit %d), want 143", out, err, exitCode(err))
	}
	put("exit 143")
	if out, err = run(t, a.rigCheckCmd(), "r"); exitCode(err) != 143 || !strings.Contains(out, "FAIL (exit 143, ended by signal 15)") {
		t.Fatalf("gate exiting 143: %q, %v (exit %d), want 143", out, err, exitCode(err))
	}

	put("true")
	if out, err = run(t, a.rigCheckCmd(), "r"); exitCode(err) != 0 || !strings.Contains(out, ": pass") {
		t.Fatalf("green gate: %q, %v", out, err)
	}

	// Could not run: an unknown rig, a base that does not resolve.
	if _, err = run(t, a.rigCheckCmd(), "nope"); exitCode(err) != 2 {
		t.Fatalf("unknown rig: %v (exit %d), want 2", err, exitCode(err))
	}
	if err := st.PutRig(ctx, store.Rig{Name: "broken", Path: t.TempDir(), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err = run(t, a.rigCheckCmd(), "broken"); exitCode(err) != 2 {
		t.Fatalf("rig that is not a repo: %v (exit %d), want 2", err, exitCode(err))
	}
}

// rig add without --kind takes a git checkout for a git rig and any other
// directory for a dir rig, and says which; a dir rig refuses --base.
func TestRigAddDetectsKind(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}

	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := run(t, a.rigCmd(), "add", "web", repo, "--base", "main")
	if err != nil || !strings.Contains(out, "(git, base main)") {
		t.Fatalf("add a checkout: %q, %v", out, err)
	}
	docs := t.TempDir()
	if out, err = run(t, a.rigCmd(), "add", "docs", docs); err != nil || !strings.Contains(out, "(dir: no git, no base)") {
		t.Fatalf("add a plain directory: %q, %v", out, err)
	}
	for name, want := range map[string]string{"web": store.RigGit, "docs": store.RigDir} {
		if r, err := st.GetRig(ctx, name); err != nil || r.Kind != want {
			t.Fatalf("%s = %+v, %v; want kind %s", name, r, err, want)
		}
	}
	if r, _ := st.GetRig(ctx, "docs"); r.Base != "" {
		t.Fatalf("dir rig has base %q", r.Base)
	}
	// --kind dir makes a checkout a dir rig.
	if out, err = run(t, a.rigCmd(), "add", "flat", repo, "--kind", "dir"); err != nil || !strings.Contains(out, "(dir") {
		t.Fatalf("--kind dir on a checkout: %q, %v", out, err)
	}
	if _, err = run(t, a.rigCmd(), "add", "docs", docs, "--base", "main"); err == nil || !strings.Contains(err.Error(), "no base") {
		t.Fatalf("--base on a dir rig: %v", err)
	}
	if _, err = run(t, a.rigCmd(), "add", "docs", docs, "--kind", "git"); err == nil || !strings.Contains(err.Error(), "not a git checkout") {
		t.Fatalf("--kind git on a plain directory: %v", err)
	}
	if _, err = run(t, a.rigCmd(), "add", "docs", docs, "--kind", "svn"); err == nil {
		t.Fatal("--kind svn accepted")
	}
	if _, err = run(t, a.rigCmd(), "add", "x", filepath.Join(docs, "missing")); err == nil {
		t.Fatal("a path that does not exist registered")
	}

	out, err = run(t, a.rigCmd(), "show", "docs")
	if err != nil || !strings.Contains(out, "kind:  dir") || !strings.Contains(out, "base:  none") {
		t.Fatalf("show dir rig: %q, %v", out, err)
	}
	out, err = run(t, a.rigCmd(), "list")
	if err != nil || !strings.Contains(out, "KIND") || !regexp.MustCompile(`docs\s+dir\s`).MatchString(out) || !regexp.MustCompile(`web\s+git\s`).MatchString(out) {
		t.Fatalf("list: %q, %v", out, err)
	}
}

// rig check on a dir rig with no check passes ("no gate"); with one it runs
// it in --dir, by default the rig's directory.
func TestRigCheckDirRig(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := st.PutRig(ctx, store.Rig{Name: "docs", Path: dir, Kind: store.RigDir}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.rigCheckCmd(), "docs")
	if exitCode(err) != 0 || !strings.Contains(out, "dir rig with no check") || !strings.Contains(out, "pass (no gate)") {
		t.Fatalf("dir rig without a check: %q, %v", out, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".yardr", "check"), []byte("test -f summary.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.rigCheckCmd(), "docs"); exitCode(err) != 1 || !strings.Contains(out, "FAIL (exit 1)") {
		t.Fatalf("red dir check: %q, %v", out, err)
	}
	sub := filepath.Join(dir, "2025-09")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "summary.md"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.rigCheckCmd(), "docs", "--dir", sub); exitCode(err) != 0 || !strings.Contains(out, "check docs (dir) in "+sub+": pass") {
		t.Fatalf("green dir check in --dir: %q, %v", out, err)
	}
}

// A repository flow that drops a stage on the base strands the open beads
// in it: rig check fails naming each, with exit 2 when the gate itself is
// green (no branch can fix it) and 1 when the gate is red as well, and flow
// show warns under the flow that lacks the stage.
func TestRigCheckAndFlowShowReportStrandedBeads(t *testing.T) {
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
	put := func(check string) {
		t.Helper()
		if err := st.PutRig(ctx, store.Rig{Name: "web", Path: repo, Base: "main", Check: check}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	flow("new -> triage | review\ntriage -> review\nreview -> merged\nmerged terminal\n")
	put("echo gate ran")
	b, err := st.CreateBead(ctx, store.NewBead{Title: "in triage", Rig: "web", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if out, err := run(t, a.rigCheckCmd(), "web"); exitCode(err) != 0 || strings.Contains(out, "stranded") {
		t.Fatalf("before the flow changed: %q, %v", out, err)
	}

	flow("new -> review\nreview -> merged\nmerged terminal\n")
	sha := git("rev-parse", "--short=7", "HEAD")
	named := "bead " + b.ID + " is in stage triage, which flow default (repo@" + sha + ") lacks"
	out, err := run(t, a.rigCheckCmd(), "web")
	if exitCode(err) != 2 || !strings.Contains(out, "gate ran") || !strings.Contains(out, "stranded: "+named) ||
		!strings.Contains(out, "yardr bead advance "+b.ID+" --to <stage>") || !strings.Contains(out, "rig web: FAIL") || strings.Contains(out, ": pass") {
		t.Fatalf("green gate, stranded bead: %q, %v (exit %d), want exit 2 naming %s", out, err, exitCode(err), b.ID)
	}
	put("exit 3")
	if out, err = run(t, a.rigCheckCmd(), "web"); exitCode(err) != 1 || !strings.Contains(out, "FAIL (exit 3)") || !strings.Contains(out, "stranded: "+named) {
		t.Fatalf("red gate, stranded bead: %q, %v (exit %d), want exit 1", out, err, exitCode(err))
	}
	put("echo gate ran")
	a.json = true
	if out, err = run(t, a.rigCheckCmd(), "web"); exitCode(err) != 2 || !strings.Contains(out, `"pass": false`) || !strings.Contains(out, `"bead": "`+b.ID+`"`) {
		t.Fatalf("--json: %q, %v", out, err)
	}
	a.json = false

	views, err := flowViews(ctx, st, "web", "")
	if err != nil || len(views) != 1 {
		t.Fatalf("views = %+v, %v", views, err)
	}
	if !slices.ContainsFunc(views[0].Warnings, func(w string) bool { return strings.HasPrefix(w, named+": it is not routed") }) {
		t.Fatalf("flow show warnings = %q, want one naming %s", views[0].Warnings, b.ID)
	}
	if out, err = run(t, a.flowCmd(), "show", "web"); err != nil || !strings.Contains(out, "warning: "+named) {
		t.Fatalf("flow show: %q, %v", out, err)
	}

	// Advanced to a stage the flow has, nothing is left to report.
	if _, err := st.Advance(ctx, b.ID, "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.rigCheckCmd(), "web"); exitCode(err) != 0 || strings.Contains(out, "stranded") {
		t.Fatalf("after advancing: %q, %v", out, err)
	}
	if views, err = flowViews(ctx, st, "web", ""); err != nil || slices.ContainsFunc(views[0].Warnings, func(w string) bool { return strings.Contains(w, b.ID) }) {
		t.Fatalf("flow show after advancing: %+v, %v", views, err)
	}

	// A bead may pin a flow that is not one of the rig's normal views. Its
	// warning still needs a visible heading under that flow.
	altPath := filepath.Join(repo, ".yardr/flows/alt.flow")
	if err := os.WriteFile(altPath, []byte("lost -> start\nstart -> finish\nfinish terminal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".yardr/flows/alt.flow")
	git("commit", "-q", "-m", "add pinned flow")
	if err := st.PutRig(ctx, store.Rig{Name: "pin-only", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	pinned, err := st.CreateBead(ctx, store.NewBead{Title: "pinned stranded", Rig: "pin-only", Stage: "lost", Metadata: map[string]any{"flow": "alt"}}, "me")
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

// rig check --light runs the rig's light gate under the full one's exit
// statuses, and names it; a rig without one runs the full gate and says so
// in one line, first.
func TestRigCheckLight(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	put := func(check, light string) {
		t.Helper()
		if err := st.PutRig(ctx, store.Rig{Name: "r", Path: t.TempDir(), Base: "main", Check: check, CheckLight: light}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	put("echo full; exit 4", "echo light")
	out, err := run(t, a.rigCheckCmd(), "r", "--light")
	if exitCode(err) != 0 || !strings.Contains(out, "light\n") || strings.Contains(out, "full\n") ||
		!strings.Contains(out, "check r (light gate, store) in ") || !strings.Contains(out, ": pass") {
		t.Fatalf("green light gate: %q, %v", out, err)
	}
	if out, err = run(t, a.rigCheckCmd(), "r"); exitCode(err) != 1 || strings.Contains(out, "light") {
		t.Fatalf("the full gate beside a light one: %q, %v", out, err)
	}

	put("true", "echo no; exit 3")
	if out, err = run(t, a.rigCheckCmd(), "r", "--light"); exitCode(err) != 1 || !strings.Contains(out, "FAIL (exit 3)") {
		t.Fatalf("red light gate: %q, %v (exit %d)", out, err, exitCode(err))
	}
	put("true", "exit 143")
	if _, err = run(t, a.rigCheckCmd(), "r", "--light"); exitCode(err) != 143 {
		t.Fatalf("light gate ended by a signal: %v (exit %d), want 143", err, exitCode(err))
	}
	put("true", "sleep 30")
	if out, err = run(t, a.rigCheckCmd(), "r", "--light", "--timeout", "200ms"); exitCode(err) != 1 || !strings.Contains(out, "timed out after 200ms") {
		t.Fatalf("timed out light gate: %q, %v", out, err)
	}

	// No light gate: the full one, said first, and its verdict.
	put("echo full; exit 4", "")
	out, err = run(t, a.rigCheckCmd(), "r", "--light")
	if exitCode(err) != 1 || !strings.HasPrefix(out, "rig r has no light gate (") || !strings.Contains(out, "running the full gate\nfull\n") ||
		strings.Contains(out, "light gate, ") {
		t.Fatalf("--light without a light gate: %q, %v (exit %d)", out, err, exitCode(err))
	}
	// Could not run is still exit 2.
	if err := st.PutRig(ctx, store.Rig{Name: "broken", Path: t.TempDir(), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err = run(t, a.rigCheckCmd(), "broken", "--light"); exitCode(err) != 2 {
		t.Fatalf("rig that is not a repo, --light: %v (exit %d), want 2", err, exitCode(err))
	}
}

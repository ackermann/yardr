package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

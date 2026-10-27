package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestPreviewProposeListStopForget(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "ada", "me"); err != nil {
		t.Fatal(err)
	}
	// A depot with one commit, so propose reads its HEAD itself.
	depot := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "a@b.c"}, {"config", "user.name", "a"}} {
		if out, err := exec.Command("git", append([]string{"-C", depot}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(depot, "app.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "one"}} {
		if out, err := exec.Command("git", append([]string{"-C", depot}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head, _ := exec.Command("git", "-C", depot, "rev-parse", "HEAD").Output()
	if err := st.PutDepot(ctx, store.Depot{Name: "demo", Path: depot, Kind: store.DepotGit, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.previewCmd(), "propose", "demo", "--do", "python3 -m http.server 18099", "--in", depot, "--port", "18099")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pending") {
		t.Fatalf("propose printed %q", out)
	}
	p, err := st.GetPreview(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if p.Commit != strings.TrimSpace(string(head)) || p.Depot != "demo" || p.State != store.PreviewPending {
		t.Fatalf("recorded proposal = %+v, want commit %s", p, strings.TrimSpace(string(head)))
	}
	out, err = run(t, a.previewCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"demo", "pending", p.Commit[:12]} {
		if !strings.Contains(out, want) {
			t.Fatalf("list %q lacks %q", out, want)
		}
	}
	if _, err := st.ApprovePreview(ctx, "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.previewCmd(), "stop", "demo"); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetPreview(ctx, "demo"); p.State != store.PreviewStopped {
		t.Fatalf("after stop = %+v", p)
	}
	// logs reads the preview's file under the yard's home.
	home := t.TempDir()
	a.cfg = config.At(home)
	if err := os.MkdirAll(filepath.Join(home, "previews"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "previews", "demo.log"), []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.previewCmd(), "logs", "demo", "--tail", "1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "second" {
		t.Fatalf("logs printed %q", out)
	}
	if _, err := run(t, a.previewCmd(), "logs", "demo", "--tail", "-1"); err == nil || !strings.Contains(err.Error(), "tail must not be negative") {
		t.Fatalf("negative log tail err = %v", err)
	}
	a.actor = "ada"
	if _, err := run(t, a.previewCmd(), "forget", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPreview(ctx, "demo"); err == nil {
		t.Fatal("preview still there after forget")
	}
}

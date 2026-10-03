package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// hook add takes every source with an adapter, github among them, and its
// help lists them.
func TestHookAddGitHub(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "me"}
	f := filepath.Join(t.TempDir(), "github.secret")
	if err := os.WriteFile(f, []byte("gh_test_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.hookCmd(), "add", "gh", "--source", "github", "--secret-file", f)
	if err != nil || !strings.Contains(out, "hook gh saved: POST /hooks/gh (github)") {
		t.Fatalf("hook add: %q %v", out, err)
	}
	h, err := st.GetHook(context.Background(), "gh")
	if err != nil || h.Source != "github" || h.SecretFile != f {
		t.Fatalf("stored hook %+v %v", h, err)
	}
	_, err = run(t, a.hookCmd(), "add", "x", "--source", "gitlab", "--secret-file", f)
	if err == nil || !strings.Contains(err.Error(), "github, linear, token") {
		t.Fatalf("unknown source err = %v", err)
	}
	for _, c := range a.hookCmd().Commands() {
		if c.Name() == "add" && !strings.Contains(c.Long, "\n  github  ") {
			t.Fatalf("hook add help does not list github:\n%s", c.Long)
		}
	}
}

// A secret file anyone else can read is refused and nothing is saved, as an
// env_file of that mode is; 0600 is saved. (yardr-7730: it used to be saved
// with a warning.)
func TestHookAddRefusesSecretFileOpenToOthers(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "me"}
	f := filepath.Join(t.TempDir(), "t.secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, 0o644); err != nil { // past the umask
		t.Fatal(err)
	}
	_, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", f)
	if err == nil || !strings.Contains(err.Error(), "mode 0644") || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("0644 secret file: err = %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error shows the secret: %v", err)
	}
	if hs, err := st.Hooks(context.Background()); err != nil || len(hs) != 0 {
		t.Fatalf("a refused hook was saved: %+v %v", hs, err)
	}
	if _, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", t.TempDir()); err == nil {
		t.Fatal("a directory was accepted as secret file")
	}

	if err := os.Chmod(f, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", f); err != nil {
		t.Fatalf("0600 secret file: %q %v", out, err)
	}
	h, err := st.GetHook(context.Background(), "t")
	if err != nil || h.SecretFile != f || h.Blocked != "" {
		t.Fatalf("stored hook %+v %v", h, err)
	}
	out, err := run(t, a.hookCmd(), "list")
	if err != nil || !strings.Contains(out, "STATE") || !strings.Contains(out, "ok") || strings.Contains(out, "blocked") {
		t.Fatalf("hook list: %q %v", out, err)
	}
}

// hook list shows a hook the receiver has blocked, and why; adding the hook
// again starts it unblocked, and no client can save one as blocked.
func TestHookListShowsBlocked(t *testing.T) {
	st, own := storetest.NewOwned(t)
	a := &app{st: st, actor: "me"}
	ctx := context.Background()
	f := filepath.Join(t.TempDir(), "t.secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", f); err != nil {
		t.Fatal(err)
	}
	if err := own.BlockHook(ctx, "t", "secret file "+f+" is open to others (mode 0644); chmod 600 it"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.hookCmd(), "list")
	if err != nil || !strings.Contains(out, "blocked: secret file "+f+" is open to others (mode 0644); chmod 600 it") {
		t.Fatalf("hook list: %q %v", out, err)
	}
	if _, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", f); err != nil {
		t.Fatal(err)
	}
	if h, err := st.GetHook(ctx, "t"); err != nil || h.Blocked != "" {
		t.Fatalf("re-added hook %+v %v", h, err)
	}
	h, err := st.PutHook(ctx, store.Hook{Name: "u", Source: "token", SecretFile: f, Blocked: "forged"}, "me")
	if err != nil || h.Blocked != "" {
		t.Fatalf("PutHook kept a client's blocked state: %+v %v", h, err)
	}
}

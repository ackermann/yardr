package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

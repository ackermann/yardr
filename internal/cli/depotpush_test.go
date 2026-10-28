package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

const depotPushCLIDecl = `name: github
base: https://api.github.com
credential: { env: GITHUB_TOKEN }
git: { credential: [github/git-credential] }
link:
  host: github.com
  issue: '^/([A-Za-z0-9](?:-?[A-Za-z0-9])*)/([A-Za-z0-9._-]+)/issues/([1-9][0-9]*)(?:/.*)?$'
  fixed: "https://github.com/{1}/{2}/issues/{3}"
  thread: "{1}/{2}#{3}"
calls:
  viewer:
    kind: read
    request: { method: GET, path: /user }
`

func depotPushCLIYard(t *testing.T) (store.API, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "none"))
	// Nor one git builds in beside the system file (Apple's git brings
	// a credential.helper of its own).
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	home := t.TempDir()
	st := storetest.NewIn(t, home)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: home}, "mckean"); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edge := store.Edge{URL: "https://my.yardr.dev", Key: yardkey.Public(key), Handle: "mckean", Name: "edgeyard"}
	if err := st.SetSetting(ctx, store.EdgeSetting, edge.String(), "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "github.yaml", depotPushCLIDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sources", "github.env"), []byte("GITHUB_TOKEN=stand-in\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/ada/web.git"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "web-assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "/srv/merge-local"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return st, b.ID
}

// yardr bead ask --depot-push files the card and prints how to
// approve it; the two flags refuse each other.
func TestBeadAskDepotPush(t *testing.T) {
	st, id := depotPushCLIYard(t)
	a := &app{st: st, actor: "yardmaster"}
	out, err := run(t, a.beadCmd(), "ask", id, "Turn push on?", "--context", "the depot is linked", "--depot-push", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "to approve, yardr bead answer "+id+" 1 --approve (turns push on for depot web)") {
		t.Errorf("ask printed:\n%s", out)
	}
	notes, err := st.Notes(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range store.ResolveAsks(notes, notes[0].At) {
		if r.Ask.DepotPush != nil && r.Ask.DepotPush.Depot == "web" && !r.Ask.DepotPush.Off {
			found = true
		}
	}
	if !found {
		t.Fatal("no depot-push card was stored")
	}
	if _, err := run(t, a.beadCmd(), "ask", id, "Both?", "--context", "x", "--depot-push", "web", "--depot-push-off", "web"); err == nil ||
		!strings.Contains(err.Error(), "one of the two") {
		t.Errorf("both flags = %v, want one-of-the-two", err)
	}
}

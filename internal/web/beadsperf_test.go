package web_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// perfRepo makes a repository on branch main with files committed, for a
// depot whose base is main.
func perfRepo(t testing.TB, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "--allow-empty", "-m", "files")
	return dir
}

// seedPerfBeads gives the yard one git depot with a repo flow and n open
// beads across its stages, routed to a machine group: the production shape,
// where every bead's estimate resolves its flow from the checkout.
func seedPerfBeads(t testing.TB, st store.API, n int) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := perfRepo(t, map[string]string{
		".yardr/flows/default.flow": "backlog -> new\nnew -> review\nreview -> merged\nmerged terminal\n",
	})
	must(st.PutDepot(ctx, store.Depot{Name: "perf", Path: repo, Base: "main"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"))
	for _, stage := range []string{"backlog", "new", "review"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Group: "builders"}, "me")
		must(err)
	}
	stages := []string{"backlog", "new", "review"}
	for i := range n {
		_, err := st.CreateBead(ctx, store.NewBead{
			Title: fmt.Sprintf("bead %d", i), Type: "task",
			Stage: stages[i%len(stages)], Depot: "perf",
			Labels: []string{"difficulty:2"},
		}, "me")
		must(err)
	}
}

// serveBeads serves st's yard for a benchmark.
func serveBeads(t testing.TB, st store.API) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".depot")
	srv, err := web.NewServer(st, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return ts.URL
}

// /beads over a few hundred beads answers in bounded time: every row's
// estimate resolves its flow, and that must not cost a git invocation per
// row. Run with -benchmem and -cpuprofile to see where the render goes.
func BenchmarkBeadsPageManyBeads(b *testing.B) {
	st := storetest.New(b)
	seedPerfBeads(b, st, 300)
	url := serveBeads(b, st)
	b.ResetTimer()
	for range b.N {
		r, err := http.Get(url + "/beads")
		if err != nil {
			b.Fatal(err)
		}
		bd, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			b.Fatalf("status %d: %s", r.StatusCode, bd)
		}
	}
}

// /beads over a few hundred beads lists every one: the perf fixture is a
// page like any other, newest change first.
func TestBeadsPageManyBeads(t *testing.T) {
	f := newFixture(t)
	seedPerfBeads(t, f.st, 300)
	var got web.Beads
	r := f.getJSON("/beads", &got)
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if len(got.Beads) != 300 {
		t.Fatalf("/beads lists %d beads, want 300", len(got.Beads))
	}
	if !strings.Contains(f.get("/beads").body, "bead 299") {
		t.Errorf("the /beads page lacks the newest bead")
	}
}

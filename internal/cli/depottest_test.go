package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A depot's test command is what depot add --test gave it: depot show says
// it, in text and in JSON, the depot_updated event carries it, and depot add
// without the flag drops it, as it drops --check.
func TestDepotAddTest(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	if out := mustAs(t, st, "jo", "depot", "add", "web", repo, "--base", "main", "--test", "make test"); !strings.Contains(out, "\ntest: make test\n") {
		t.Fatalf("git depot add --test = %q", out)
	}
	if r, err := st.GetDepot(ctx, "web"); err != nil || r.Test != "make test" {
		t.Fatalf("git depot = %+v, %v", r, err)
	}
	if out := mustAs(t, st, "jo", "depot", "show", "web"); !strings.Contains(out, "\n  test:  make test\n") {
		t.Fatalf("git depot show = %q", out)
	}

	dir := t.TempDir()
	if out := mustAs(t, st, "jo", "depot", "add", "docs", dir, "--kind", "dir", "--test", "sh .yardr/test"); !strings.Contains(out, "\ntest: sh .yardr/test\n") {
		t.Fatalf("depot add --test = %q", out)
	}
	if r, err := st.GetDepot(ctx, "docs"); err != nil || r.Test != "sh .yardr/test" {
		t.Fatalf("depot = %+v, %v", r, err)
	}
	if out := mustAs(t, st, "jo", "depot", "show", "docs"); !strings.Contains(out, "\n  test:  sh .yardr/test\n") {
		t.Fatalf("depot show = %q", out)
	}
	var shown store.Depot
	if out := mustAs(t, st, "jo", "depot", "show", "docs", "--json"); json.Unmarshal([]byte(out), &shown) != nil || shown.Test != "sh .yardr/test" {
		t.Fatalf("depot show --json = %q", out)
	}

	if out := mustAs(t, st, "jo", "depot", "add", "docs", dir, "--kind", "dir"); strings.Contains(out, "test:") {
		t.Fatalf("depot add without --test = %q", out)
	}
	if r, err := st.GetDepot(ctx, "docs"); err != nil || r.Test != "" {
		t.Fatalf("depot = %+v, %v; want no test command", r, err)
	}
	for _, args := range [][]string{{"depot", "show", "docs"}, {"depot", "show", "docs", "--json"}} {
		if out := mustAs(t, st, "jo", args...); strings.Contains(out, "test") {
			t.Fatalf("yardr %s = %q; want no test line", strings.Join(args, " "), out)
		}
	}

	evs, err := st.Events(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var got []any
	for _, e := range evs {
		if e.Kind == "depot_updated" && e.Data["depot"] == "docs" {
			got = append(got, e.Data["test"])
		}
	}
	if len(got) != 2 || !(got[0] == "sh .yardr/test" && got[1] == "" || got[0] == "" && got[1] == "sh .yardr/test") {
		t.Fatalf("depot_updated events carry test = %q; want the command once and its clearing once", got)
	}
}

// prime prints the depot's test command under the bead's depot, and nothing
// for a depot with none. bead show, which is not about to work the bead,
// does not.
func TestPrimeSaysTheDepotsTest(t *testing.T) {
	a, repo, _, _, _ := stageYard(t)
	ctx := context.Background()
	b, err := a.st.CreateBead(ctx, store.NewBead{Title: "task", Depot: "web"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	prime := func(asJSON bool) string {
		t.Helper()
		a.json = asJSON
		defer func() { a.json = false }()
		out, err := run(t, a.primeCmd(), "--bead", b.ID)
		if err != nil {
			t.Fatalf("prime: %v", err)
		}
		return out
	}
	if out := prime(false); strings.Contains(out, "test:") {
		t.Fatalf("prime of a depot with no test command:\n%s", out)
	}
	if out := prime(true); strings.Contains(out, `"test"`) {
		t.Fatalf("prime --json of a depot with no test command:\n%s", out)
	}

	if err := a.st.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main", Test: "make test"}, "me"); err != nil {
		t.Fatal(err)
	}
	if out, want := prime(false), "  depot web\n  test: make test   (run it in your worktree before you hand on)\n"; !strings.Contains(out, want) {
		t.Fatalf("prime:\n%s\nwant it to hold:\n%s", out, want)
	}
	var v struct{ Test string }
	if out := prime(true); json.Unmarshal([]byte(out), &v) != nil || v.Test != "make test" {
		t.Fatalf("prime --json:\n%s", out)
	}
	if out, err := run(t, a.rootCmd(), "bead", "show", b.ID); err != nil || strings.Contains(out, "test:") {
		t.Fatalf("bead show = %v:\n%s", err, out)
	}
}

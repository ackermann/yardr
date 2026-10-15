package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// brokenAskYard is a depot "web" on a repository flow with a templated
// manual stage review, worked by the manual group reviewers (ada, mckean).
// It returns the app, the repo dir, and a commit function for breaking the
// template on main.
func brokenAskYard(t *testing.T, reviewFile string) (*app, string, func(files map[string]string)) {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(files map[string]string) {
		t.Helper()
		for name, text := range files {
			p := filepath.Join(repo, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	commit := func(files map[string]string) {
		t.Helper()
		write(files)
		git("add", "-A")
		git("commit", "-q", "--allow-empty", "-m", "files")
	}
	git("init", "-q", "-b", "main")
	commit(map[string]string{
		".yardr/flows/default.flow":      "new -> review\nreview -> approved on done | new on changes\napproved -> merged\nmerged terminal\n",
		".yardr/flows/default/review.md": reviewFile,
	})
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ada", "mckean"} {
		if _, err := st.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada", "mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "mckean"}, repo, commit
}

const brokenAskGood = `Review the diff.

## Ask
kind: choice
question: Where should this bead go next?
context:
- The builder asked for a decision.
done: The bead moves to approved.
changes: The bead goes back for changes.
`

// A bead that entered a manual stage with a broken template shows the
// template's error where the ask would stand: prime --bead prints the
// bad_template reason, and review list marks the bead with it instead of
// its move actions.
func TestBrokenTemplateShowsWhereAskWouldStand(t *testing.T) {
	a, _, commit := brokenAskYard(t, brokenAskGood)
	ctx := context.Background()
	b, err := a.st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = a.st.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// Break the template, move out and back in: the re-entry puts no
	// fresh ask and logs one bad_template event.
	commit(map[string]string{".yardr/flows/default/review.md": "Review.\n\n## Ask\nkind: chek\n"})
	if b, err = a.st.Advance(ctx, b.ID, "new", store.Expect{}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if b, err = a.st.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	evs, err := a.st.Events(ctx, b.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var reason string
	for _, e := range evs {
		if e.Kind == "bad_template" {
			reason, _ = e.Data["reason"].(string)
		}
	}
	if reason == "" {
		t.Fatalf("no bad_template event, events %+v", evs)
	}
	if out, err := run(t, a.primeCmd(), "--bead", b.ID); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out, reason) {
		t.Errorf("prime --bead shows no broken-template error:\n%s\nwant it to hold %q", out, reason)
	}
	if out, err := run(t, a.reviewCmd(), "list"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out, b.ID) || !strings.Contains(out, reason) {
		t.Errorf("review list marks no broken-template error:\n%s\nwant %s with %q", out, b.ID, reason)
	} else if strings.Contains(out, "done:approved") {
		t.Errorf("review list shows the usual moves beside the error:\n%s", out)
	}
	a.json = true
	defer func() { a.json = false }()
	if out, err := run(t, a.primeCmd(), "--bead", b.ID); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out, `"template_error"`) || !strings.Contains(out, "chek") {
		t.Errorf("prime --json carries no template error:\n%s", out)
	}
	if out, err := run(t, a.reviewCmd(), "list"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out, `"template_error"`) || !strings.Contains(out, "chek") {
		t.Errorf("review list --json carries no template error:\n%s", out)
	}
}

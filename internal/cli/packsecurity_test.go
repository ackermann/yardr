package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// The starter's second review is worked by a group of its own, and the
// import asks nothing for it: pack check lists the group as bound as the
// reviewers, and an import would ask for the builders and the reviewers as
// it did before the group was there.
func TestPackCheckAsksNothingForTheSecurityReviewers(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web", "--crew", "yardmaster=boss")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  create           web-security-reviewers\n                     agent, model and arguments as group web-reviewers\n",
		"  create           depot web stage review-security -> web-security-reviewers\n",
		"  create           depot web stage review-security type train -> web-security-reviewers\n",
		"  create           flows/default/review-security.md -> ",
		"  create           roles/review-security.md -> ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q", want)
		}
	}
	ask := out[strings.Index(out, "an import would ask"):]
	if strings.Contains(ask, "security-reviewers") {
		t.Errorf("an import would ask for the security reviewers:\n%s", ask)
	}
	if t.Failed() {
		t.Log(out)
	}
}

// Without a flag for it the second review's group gets the reviewers'
// agent, model and arguments; --bind security-reviewers=<kind>[:<model>]
// gives it its own, and then the reviewers' stay theirs.
func TestPackApplyBindsTheSecurityReviewersLikeTheReviewers(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	requireNonInteractive(t)
	ctx := context.Background()
	args := append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes"}, starterFlags...)
	if out, err := run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	agent := func(name string) string {
		t.Helper()
		g, err := a.st.GetGroup(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(g.Config["kind"], " ", g.Config["model"], " ", g.Config["args"])
	}
	// starterFlags binds reviewers=codex, the arguments those of a new codex
	// agent without a terminal.
	want := "codex <nil> [--dangerously-bypass-approvals-and-sandbox]"
	if got := agent("web-reviewers"); got != want {
		t.Fatalf("web-reviewers = %s, want %s", got, want)
	}
	if got := agent("web-security-reviewers"); got != want {
		t.Fatalf("web-security-reviewers = %s, want the reviewers' %s", got, want)
	}
	if g, _ := a.st.GetGroup(ctx, "web-security-reviewers"); g == nil || g.Limit != 2 || !strings.HasSuffix(fmt.Sprint(g.Config["prompt"]), "roles/review-security.md") {
		t.Fatalf("web-security-reviewers = %+v, want its own limit and role", g)
	}

	// Another depot names the second review's agent: it asks for it by the
	// pack's name for the group, and the reviewers keep theirs.
	if err := a.st.PutDepot(ctx, store.Depot{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), "apply", "starter", "--depot", "api", "--yes", "--crew", "yardmaster=boss", "--members", "backlog=",
		"--bind", "builders=claude", "--bind", "reviewers=codex", "--bind", "security-reviewers=claude:opus")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := agent("api-security-reviewers"), "claude opus [--permission-mode auto]"; got != want {
		t.Fatalf("api-security-reviewers = %s, want %s", got, want)
	}
	if got := agent("api-reviewers"); got != "codex <nil> [--dangerously-bypass-approvals-and-sandbox]" {
		t.Fatalf("api-reviewers = %s", got)
	}
	if strings.Contains(out, "agent, model and arguments as group api-reviewers") {
		t.Errorf("the plan says the security reviewers bind as the reviewers though --bind named them:\n%s", out)
	}
}

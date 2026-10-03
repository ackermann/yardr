package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// sourcesYard is stageYard's rig "web" with a role and a stage file of its
// own on the base, and an agent group its review stage is routed to.
func sourcesYard(t *testing.T) (a *app, home, sha string) {
	t.Helper()
	a, repo, home, git, write := stageYard(t)
	ctx := context.Background()
	write(repo, map[string]string{
		".yardr/flows/default/review.md":    "the rig's review\n",
		".yardr/flows/default/new.md":       "the rig's new\n",
		".yardr/flows/type/decision/ask.md": "the rig's ask\n",
		".yardr/roles/builder.md":           "the rig's builder\n",
		".yardr/roles/reviewer.md":          "the rig's reviewer\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "roles and stage files")
	for _, g := range []store.Group{
		{Name: "web-builders", Runner: "herdr", Config: map[string]any{"kind": "claude", "prompt": "roles/builder.md"}, Limit: 1},
		{Name: "web-reviewers", Runner: "herdr", Config: map[string]any{"kind": "claude", "prompt": "roles/reviewer.md"}, Limit: 1},
	} {
		if err := a.st.PutGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	rig := "web"
	for stage, group := range map[string]string{"new": "web-builders", "review": "web-reviewers"} {
		if _, err := a.st.AddRoute(ctx, store.Route{Stage: stage, Rig: &rig, Group: group}); err != nil {
			t.Fatal(err)
		}
	}
	return a, home, git("rev-parse", "HEAD")[:7]
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sourceLines are the listing's lines with their columns joined by one space, so
// a test does not depend on the widths.
func sourceLines(t *testing.T, out string) []string {
	t.Helper()
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		got = append(got, strings.Join(strings.Fields(l), " "))
	}
	return got
}

func wantLines(t *testing.T, out string, want ...string) {
	t.Helper()
	got := sourceLines(t, out)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rig sources:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A rig governed by its repository alone: every flow, stage file and role
// is the rig's, at the base commit, and nothing is shadowed.
func TestRigSourcesRepositoryAlone(t *testing.T) {
	a, _, sha := sourcesYard(t)
	out, err := run(t, a.rigSourcesCmd(), "web")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out,
		"flow default rig (.yardr/flows/default.flow@"+sha+")",
		"stage default:new rig (.yardr/flows/default/new.md@"+sha+")",
		"stage default:review rig (.yardr/flows/default/review.md@"+sha+")",
		"flow type/decision rig (.yardr/flows/type/decision.flow@"+sha+")",
		"stage decision:ask rig (.yardr/flows/type/decision/ask.md@"+sha+")",
		"role web-builders rig (.yardr/roles/builder.md@"+sha+")",
		"role web-reviewers rig (.yardr/roles/reviewer.md@"+sha+")",
	)
	if strings.Contains(out, "shadows") {
		t.Errorf("a rig governed by its repository shadows nothing:\n%s", out)
	}
	if out, err = run(t, a.rigCheckCmd(), "web"); err != nil || strings.Contains(out, "shadowed") {
		t.Fatalf("rig check notes a shadowed file where there is none: %v\n%s", err, out)
	}
}

// A rig with a pack applied: the pack's flow, and its roles, stand in front
// of the rig's files, and each such file is named where it is shadowed: the
// flow file and the type's, the stage files, the roles. rig check names the
// same files in notes and still passes.
func TestRigSourcesAppliedPack(t *testing.T) {
	a, home, _ := sourcesYard(t)
	setInteractive(t, false)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--rig", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// A stage file of the pack's flow, in the yard's home.
	stageFile := filepath.Join(home, "flows", "starter.default", "review.md")
	writeFile(t, stageFile, "the yard's review\n")
	packs := filepath.Join(home, "packs", "starter", "roles")

	out, err := run(t, a.rigSourcesCmd(), "web")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out,
		"flow default pack starter (store starter.default) shadows .yardr/flows/default.flow, .yardr/flows/type/decision.flow, .yardr/flows/type/decision/ask.md",
		"stage backlog none",
		"stage new none shadows .yardr/flows/default/new.md",
		"stage review pack starter ("+stageFile+") shadows .yardr/flows/default/review.md",
		"stage approved none",
		"stage decide none",
		"role web-builders pack starter ("+filepath.Join(packs, "builder.md")+") shadows .yardr/roles/builder.md",
		"role web-reviewers pack starter ("+filepath.Join(packs, "reviewer.md")+") shadows .yardr/roles/reviewer.md",
		"role boss pack starter ("+filepath.Join(packs, "mayor.md")+")",
	)

	a.json = true
	out, err = run(t, a.rigSourcesCmd(), "web")
	a.json = false
	var lines []rigSource
	if err := json.Unmarshal([]byte(out), &lines); err != nil || len(lines) != 9 {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	if l := lines[3]; l.Kind != "stage" || l.Name != "review" || l.Flow != "starter.default" || l.Source != sourcePack || l.Pack != "starter" ||
		l.Path != stageFile || len(l.Shadows) != 1 || l.Shadows[0] != ".yardr/flows/default/review.md" {
		t.Errorf("--json review = %+v", l)
	}

	out, err = run(t, a.rigCheckCmd(), "web")
	if err != nil {
		t.Fatalf("shadowed files fail the check: %v\n%s", err, out)
	}
	for _, want := range []string{
		"note: .yardr/flows/default.flow is shadowed by flow starter.default (pack starter) (yardr rig sources web)\n",
		"note: .yardr/flows/type/decision.flow is shadowed by flow starter.default (pack starter) (yardr rig sources web)\n",
		"note: .yardr/flows/type/decision/ask.md is shadowed by flow starter.default (pack starter) (yardr rig sources web)\n",
		"note: .yardr/flows/default/new.md is shadowed by flow starter.default, which has no stage file for new (yardr rig sources web)\n",
		"note: .yardr/flows/default/review.md is shadowed by the stage file of review in flow starter.default, pack starter (" + stageFile + ") (yardr rig sources web)\n",
		"note: .yardr/roles/builder.md is shadowed by the prompt of web-builders, pack starter (" + filepath.Join(packs, "builder.md") + ") (yardr rig sources web)\n",
		": pass\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rig check lacks %q:\n%s", want, out)
		}
	}
}

// A mix: the pack's flow governs, and the rig keeps its own roles
// (--rig-roles). The role's line says the pack has a copy; a resident, the
// yard's, is the pack's either way. Clearing the pointer gives the flow
// back to the repository, and the line says the pack's is still there.
func TestRigSourcesPackFlowRigRoles(t *testing.T) {
	a, home, sha := sourcesYard(t)
	setInteractive(t, false)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--rig", "web", "--yes", "--rig-roles"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, a.rigSourcesCmd(), "web")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out,
		"flow default pack starter (store starter.default) shadows .yardr/flows/default.flow, .yardr/flows/type/decision.flow, .yardr/flows/type/decision/ask.md",
		"stage backlog none",
		"stage new none shadows .yardr/flows/default/new.md",
		"stage review none shadows .yardr/flows/default/review.md",
		"stage approved none",
		"stage decide none",
		"role web-builders rig (.yardr/roles/builder.md@"+sha+") pack starter has a copy",
		"role web-reviewers rig (.yardr/roles/reviewer.md@"+sha+") pack starter has a copy",
		"role boss pack starter ("+filepath.Join(home, "packs", "starter", "roles", "mayor.md")+")",
	)

	if err := a.st.SetFlowPointer(context.Background(), "web", "", "", "me"); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.rigSourcesCmd(), "web"); err != nil {
		t.Fatal(err)
	}
	if got := sourceLines(t, out)[0]; got != "flow default rig (.yardr/flows/default.flow@"+sha+") pack starter has flow starter.default" {
		t.Errorf("after clearing the pointer: %s", got)
	}
}

// A pointer at a store flow no pack brought is the store's, its stage files
// the home's; a prompt under the home's roles is the home's, and shadows the
// rig's role of the name when it is named by its path.
func TestRigSourcesStoreFlowAndHomeRole(t *testing.T) {
	a, home, _ := sourcesYard(t)
	ctx := context.Background()
	if err := a.st.SetFlowPointer(ctx, "web", "", "code", "me"); err != nil {
		t.Fatal(err)
	}
	stageFile := filepath.Join(home, "flows", "code", "new.md")
	role := filepath.Join(home, "roles", "builder.md")
	writeFile(t, stageFile, "the yard's new\n")
	writeFile(t, role, "the yard's builder\n")
	g, err := a.st.GetGroup(ctx, "web-builders")
	if err != nil {
		t.Fatal(err)
	}
	g.Config["prompt"] = role
	if err := a.st.PutGroup(ctx, *g); err != nil {
		t.Fatal(err)
	}
	g.Name, g.Config = "web-lost", map[string]any{"kind": "claude", "prompt": "roles/nobody.md"}
	if err := a.st.PutGroup(ctx, *g); err != nil {
		t.Fatal(err)
	}
	rig := "web"
	if _, err := a.st.AddRoute(ctx, store.Route{Stage: "elsewhere", Rig: &rig, Group: "web-lost"}); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.rigSourcesCmd(), "web")
	if err != nil {
		t.Fatal(err)
	}
	got := sourceLines(t, out)
	for i, want := range []string{
		"flow default store (store code) shadows .yardr/flows/default.flow, .yardr/flows/type/decision.flow, .yardr/flows/type/decision/ask.md",
		"stage new home (" + stageFile + ") shadows .yardr/flows/default/new.md",
		"stage review none shadows .yardr/flows/default/review.md",
		"role web-builders home (" + role + ") shadows .yardr/roles/builder.md",
		"role web-reviewers rig (.yardr/roles/reviewer.md@",
	} {
		if i >= len(got) || !strings.HasPrefix(got[i], want) {
			t.Fatalf("line %d: want %q in:\n%s", i, want, strings.Join(got, "\n"))
		}
	}
	// A prompt nobody has is said, not an error of the whole listing.
	if last := got[len(got)-1]; !strings.HasPrefix(last, "role web-lost none: roles/nobody.md not found") {
		t.Errorf("a missing prompt: %s", last)
	}
}

func TestRigSourcesIncludesAllPotentialRouteGroups(t *testing.T) {
	a, _, _ := sourcesYard(t)
	ctx := context.Background()
	for _, name := range []string{"web-specialists", "yard-general", "yard-unlisted"} {
		if err := a.st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": "claude", "prompt": "roles/reviewer.md"}}); err != nil {
			t.Fatal(err)
		}
	}
	rig := "web"
	label := "special"
	for _, r := range []store.Route{
		// A label-specific route may win for beads that carry this label,
		// even though the ordinary route also matches the review stage.
		{Stage: "review", Rig: &rig, Label: &label, Group: "web-specialists", Priority: 10},
		// Unscoped routes apply to beads in this rig too, including a stage
		// outside the current flow.
		{Stage: "review", Group: "yard-general"},
		{Stage: "unlisted", Group: "yard-unlisted"},
	} {
		if _, err := a.st.AddRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, a.rigSourcesCmd(), "web")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sourceLines(t, out), "\n")
	for _, name := range []string{"web-specialists", "yard-general", "yard-unlisted"} {
		if !strings.Contains(got, "role "+name+" rig (.yardr/roles/reviewer.md@") {
			t.Errorf("potential route group %s is missing:\n%s", name, got)
		}
	}
}

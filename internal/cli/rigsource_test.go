package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// packedYard is sourcesYard with the starter pack imported into web, the
// pack's roles set.
func packedYard(t *testing.T) (a *app, home, sha string) {
	t.Helper()
	a, home, sha = sourcesYard(t)
	setInteractive(t, false)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--rig", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return a, home, sha
}

// sourcesLine is the line of yardr rig sources web that starts with prefix.
func sourcesLine(t *testing.T, a *app, prefix string) string {
	t.Helper()
	out, err := run(t, a.rigSourcesCmd(), "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range sourceLines(t, out) {
		if strings.HasPrefix(l, prefix+" ") {
			return l
		}
	}
	t.Fatalf("rig sources has no line %q:\n%s", prefix, out)
	return ""
}

// refused runs rig source and wants an error holding each of want, with
// rig sources as it was.
func refused(t *testing.T, a *app, args []string, want ...string) {
	t.Helper()
	before, _ := run(t, a.rigSourcesCmd(), "web")
	out, err := run(t, a.rigSourceCmd(), args...)
	if err == nil {
		t.Fatalf("rig source %v is not refused:\n%s", args, out)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("rig source %v: %v\nlacks %q", args, err, w)
		}
	}
	if after, _ := run(t, a.rigSourcesCmd(), "web"); after != before {
		t.Errorf("the refused rig source %v changed the rig:\n%s\nwas:\n%s", args, after, before)
	}
}

// A group's role switches to the rig's own and back to the pack's: the
// command prints the line rig sources then shows, says when the pack's
// record would undo it, and a second time changes nothing.
func TestRigSourceRoleOfAGroup(t *testing.T) {
	a, home, sha := packedYard(t)
	copied := filepath.Join(home, "packs", "starter", "roles", "builder.md")
	rigs := "role web-builders rig (.yardr/roles/builder.md@" + sha + ") pack starter has a copy"
	packs := "role web-builders pack starter (" + copied + ") shadows .yardr/roles/builder.md"

	out, err := run(t, a.rigSourceCmd(), "web", "role", "web-builders", "rig")
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceLines(t, out); got[0] != rigs || len(got) != 2 ||
		got[1] != "note: the import of pack starter chose the pack's roles: the next yardr pack apply sets its role on web-builders again unless given --rig-roles" {
		t.Errorf("to the rig's:\n%s", out)
	}
	if got := sourcesLine(t, a, "role web-builders"); got != rigs {
		t.Errorf("rig sources after: %s", got)
	}
	if g, _ := a.st.GetGroup(context.Background(), "web-builders"); g.Config["prompt"] != "roles/builder.md" || g.Config["kind"] != "claude" {
		t.Errorf("the group = %+v", g.Config)
	}
	if out, err = run(t, a.rigSourceCmd(), "web", "role", "web-builders", "rig"); err != nil || !strings.Contains(out, "unchanged") {
		t.Errorf("a second time: %v\n%s", err, out)
	}

	a.json = true
	out, err = run(t, a.rigSourceCmd(), "web", "role", "web-builders", "pack")
	a.json = false
	var res sourceSwitch
	if err := json.Unmarshal([]byte(out), &res); err != nil || !res.Changed || len(res.Lines) != 1 ||
		res.Lines[0].Source != sourcePack || res.Lines[0].Path != copied || len(res.Notes) != 0 {
		t.Errorf("to the pack's, --json: %v\n%s", err, out)
	}
	if got := sourcesLine(t, a, "role web-builders"); got != packs {
		t.Errorf("rig sources after: %s", got)
	}
}

// A resident reads a path as it is, so its rig's role is the file in the
// rig's checkout: refused while there is none, then listed as the rig's.
func TestRigSourceRoleOfAResident(t *testing.T) {
	a, home, _ := packedYard(t)
	rig, err := a.st.GetRig(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	refused(t, a, []string{"web", "role", "boss", "rig"}, "rig web has no .yardr/roles/yardmaster.md in its checkout")

	writeFile(t, filepath.Join(rig.Path, ".yardr", "roles", "yardmaster.md"), "the rig's yardmaster\n")
	out, err := run(t, a.rigSourceCmd(), "web", "role", "boss", "rig")
	if err != nil {
		t.Fatal(err)
	}
	want := "role boss rig (.yardr/roles/yardmaster.md) pack starter has a copy"
	if got := sourceLines(t, out); got[0] != want || !strings.Contains(out, "unless given --rig-roles") {
		t.Errorf("to the rig's:\n%s", out)
	}
	if got := sourcesLine(t, a, "role boss"); got != want {
		t.Errorf("rig sources after: %s", got)
	}
	if out, err = run(t, a.rigSourceCmd(), "web", "role", "boss", "pack"); err != nil {
		t.Fatal(err)
	}
	want = "role boss pack starter (" + filepath.Join(home, "packs", "starter", "roles", "yardmaster.md") + ")"
	if got := sourcesLine(t, a, "role boss"); !strings.HasPrefix(got, want) || !strings.HasPrefix(sourceLines(t, out)[0], want) {
		t.Errorf("back to the pack's: %s\n%s", got, out)
	}
}

// Under an import given --rig-roles, a group switched to the pack's role is
// told that the next import switches it back.
func TestRigSourceRoleAgainstTheRecord(t *testing.T) {
	a, _, _ := sourcesYard(t)
	setInteractive(t, false)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--rig", "web", "--yes", "--rig-roles"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, a.rigSourceCmd(), "web", "role", "web-reviewers", "pack")
	if err != nil || !strings.HasPrefix(sourceLines(t, out)[0], "role web-reviewers pack starter ") || !strings.Contains(out, "unless given --pack-roles") {
		t.Errorf("%v\n%s", err, out)
	}
	if out, err = run(t, a.rigSourceCmd(), "web", "role", "web-reviewers", "rig"); err != nil || strings.Contains(out, "note:") {
		t.Errorf("back to what the record chose: %v\n%s", err, out)
	}
}

// A role is refused, changing nothing, when the chosen source does not
// exist: no pack imported, a pack without the role, a rig without the file,
// a name that is no role of the rig, a prompt that names no role.
func TestRigSourceRoleRefusals(t *testing.T) {
	a, home, _ := sourcesYard(t)
	ctx := context.Background()
	refused(t, a, []string{"web", "role", "web-builders", "pack"}, "no pack is imported into rig web")
	refused(t, a, []string{"web", "role", "nobody", "rig"}, "rig web has no role nobody")
	refused(t, a, []string{"web", "role", "web-builders", "home"}, "rig or pack")
	refused(t, a, []string{"web", "role", "web-builders"}, "give role")

	// A group prompted from the home, with a role neither the rig nor the
	// pack has, and one whose prompt is no role at all.
	planner, plain := filepath.Join(home, "roles", "planner.md"), filepath.Join(home, "notes", "brief.txt")
	writeFile(t, planner, "the yard's planner\n")
	writeFile(t, plain, "a brief\n")
	rig := "web"
	for name, prompt := range map[string]string{"web-planners": planner, "web-plain": plain} {
		if err := a.st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": "claude", "prompt": prompt}}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.st.AddRoute(ctx, store.Route{Stage: name, Rig: &rig, Group: name}); err != nil {
			t.Fatal(err)
		}
	}
	refused(t, a, []string{"web", "role", "web-planners", "rig"}, "rig web has no .yardr/roles/planner.md on its base main")
	refused(t, a, []string{"web", "role", "web-plain", "rig"}, "names no role")

	setInteractive(t, false)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--rig", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	refused(t, a, []string{"web", "role", "web-planners", "pack"}, "no pack imported into rig web (starter) installed a role planner.md")
}

// The rig's flow switches to the repository's and back to the pack's, whole:
// the stage files come with it, and the output says so.
func TestRigSourceFlow(t *testing.T) {
	a, home, sha := packedYard(t)
	out, err := run(t, a.rigSourceCmd(), "web", "flow", "rig")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out,
		"flow default rig (.yardr/flows/default.flow@"+sha+") pack starter has flow starter.default",
		"stage new rig (.yardr/flows/default/new.md@"+sha+")",
		"stage review rig (.yardr/flows/default/review.md@"+sha+")",
		"the flow switched whole: its stage files are the ones above now",
	)
	if got := sourcesLine(t, a, "flow default"); !strings.HasPrefix(got, "flow default rig ") {
		t.Errorf("rig sources after: %s", got)
	}
	if out, err = run(t, a.rigSourceCmd(), "web", "flow", "rig"); err != nil || !strings.Contains(out, "unchanged") {
		t.Errorf("a second time: %v\n%s", err, out)
	}

	if out, err = run(t, a.rigSourceCmd(), "web", "flow", "pack"); err != nil {
		t.Fatal(err)
	}
	got := sourceLines(t, out)
	if len(got) != 7 || !strings.HasPrefix(got[0], "flow default pack starter (store starter.default) shadows .yardr/flows/default.flow") ||
		got[2] != "stage new pack starter ("+filepath.Join(home, "flows", "starter.default", "new.md")+") shadows .yardr/flows/default/new.md" || !strings.HasPrefix(got[6], "the flow switched whole") {
		t.Errorf("to the pack's:\n%s", out)
	}
	if got := sourcesLine(t, a, "flow default"); !strings.HasPrefix(got, "flow default pack starter ") {
		t.Errorf("rig sources after: %s", got)
	}
	if out, err = run(t, a.rigSourceCmd(), "web", "flow", "pack"); err != nil || !strings.Contains(out, "unchanged") {
		t.Errorf("a second time: %v\n%s", err, out)
	}
}

// A type's flow switches by its own pointer: to a pack's flow for the type,
// and back to the rig's type file. Switching the rig's flow names the type
// that stays where it is.
func TestRigSourceFlowOfAType(t *testing.T) {
	a, _, sha := sourcesYard(t)
	ctx := context.Background()
	// An import that pointed decisions at a flow of its own.
	if _, err := a.st.RecordPack(ctx, store.PackImport{Pack: "ask", Rig: "web", Objects: []store.PackObject{
		{Kind: "flow", Name: "code"}, {Kind: "pointer", Type: "decision", Flow: "code"}, {Kind: "pointer", Flow: "code"},
	}}, true, "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.rigSourceCmd(), "web", "flow", "--type", "decision", "pack")
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceLines(t, out); got[0] != "flow type/decision pack ask (store code) shadows .yardr/flows/type/decision.flow, .yardr/flows/type/decision/ask.md" {
		t.Errorf("to the pack's:\n%s", out)
	}
	if out, err = run(t, a.rigSourceCmd(), "web", "flow", "pack"); err != nil {
		t.Fatal(err)
	}
	// Under the rig's pointer no type file governs.
	refused(t, a, []string{"web", "flow", "--type", "decision", "rig"}, "switch the rig's flow first")
	if out, err = run(t, a.rigSourceCmd(), "web", "flow", "rig"); err != nil ||
		!strings.Contains(out, "note: type/decision still comes from pack ask: yardr rig source web flow --type decision rig") {
		t.Errorf("the rig's flow, a type left behind: %v\n%s", err, out)
	}
	if out, err = run(t, a.rigSourceCmd(), "web", "flow", "--type", "decision", "rig"); err != nil {
		t.Fatal(err)
	}
	wantLines(t, out,
		"flow type/decision rig (.yardr/flows/type/decision.flow@"+sha+")",
		"stage decision:ask rig (.yardr/flows/type/decision/ask.md@"+sha+")",
		"the flow switched whole: its stage files are the ones above now",
	)
}

// A flow is refused, changing nothing, when the chosen source does not
// exist (no pack imported, a pack without a flow for the type, a rig
// without the flow file) and when the store refuses the pointer for an open
// bead in a stage the new flow lacks.
func TestRigSourceFlowRefusals(t *testing.T) {
	a, _, _ := sourcesYard(t)
	ctx := context.Background()
	refused(t, a, []string{"web", "flow", "pack"}, "no pack is imported into rig web")
	refused(t, a, []string{"web", "flow", "--type", "bug", "rig"}, "rig web has no .yardr/flows/type/bug.flow on its base main")
	refused(t, a, []string{"web", "flow", "builders", "rig"}, "give role")
	// api is a dir rig pointed at a store flow, with no flow file of its own.
	if _, err := run(t, a.rigSourceCmd(), "api", "flow", "rig"); err == nil || !strings.Contains(err.Error(), "rig api has no .yardr/flows/default.flow in its directory") {
		t.Errorf("a rig without a flow file: %v", err)
	}

	setInteractive(t, false)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--rig", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	refused(t, a, []string{"web", "flow", "--type", "decision", "pack"}, "no pack imported into rig web (starter) has a flow for beads of type decision")

	// approved is a stage of the pack's flow the repository's lacks.
	b, err := a.st.CreateBead(ctx, store.NewBead{Title: "x", Rig: "web", Stage: "approved"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	refused(t, a, []string{"web", "flow", "rig"}, "would leave open beads in stages their flow lacks", b.ID+" (approved")
}

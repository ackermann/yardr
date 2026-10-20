package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// packedYard is sourcesYard with the starter pack imported into web, the
// pack's roles set.
func packedYard(t *testing.T) (a *app, home, sha string) {
	t.Helper()
	a, home, sha = sourcesYard(t)
	requireNonInteractive(t)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return a, home, sha
}

// sourcesLine is the line of yardr depot sources web that starts with prefix.
func sourcesLine(t *testing.T, a *app, prefix string) string {
	t.Helper()
	out, err := run(t, a.depotSourcesCmd(), "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range sourceLines(t, out) {
		if strings.HasPrefix(l, prefix+" ") {
			return l
		}
	}
	t.Fatalf("depot sources has no line %q:\n%s", prefix, out)
	return ""
}

// refused runs depot source and wants an error holding each of want, with
// depot sources as it was. It returns the listing after, for a test with
// one refusal after another to hand to refusedSince.
func refused(t *testing.T, a *app, args []string, want ...string) string {
	t.Helper()
	before, _ := run(t, a.depotSourcesCmd(), "web")
	return refusedSince(t, a, before, args, want...)
}

// refusedSince is refused against a listing taken before: a test with one
// refusal after another hands each the listing the last left, and lists
// once per refusal instead of twice. It returns the listing after. The
// listing is taken anew after anything that changes the depot.
func refusedSince(t *testing.T, a *app, before string, args []string, want ...string) string {
	t.Helper()
	out, err := run(t, a.depotSourceCmd(), args...)
	if err == nil {
		t.Fatalf("depot source %v is not refused:\n%s", args, out)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("depot source %v: %v\nlacks %q", args, err, w)
		}
	}
	after, _ := run(t, a.depotSourcesCmd(), "web")
	if after != before {
		t.Errorf("the refused depot source %v changed the depot:\n%s\nwas:\n%s", args, after, before)
	}
	return after
}

// A group's role switches to the depot's own and back to the pack's: the
// command prints the line depot sources then shows, says when the pack's
// record would undo it, and a second time changes nothing.
func TestDepotSourceRoleOfAGroup(t *testing.T) {
	t.Parallel()
	a, home, sha := packedYard(t)
	copied := filepath.Join(config.At(home).PackRoles("starter"), "builder.md")
	depots := "role web-builders depot (.yardr/roles/builder.md@" + sha + ") pack starter has a copy"
	packs := "role web-builders pack starter (" + copied + ") shadows .yardr/roles/builder.md"

	out, err := run(t, a.depotSourceCmd(), "web", "role", "web-builders", "depot")
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceLines(t, out); got[0] != depots || len(got) != 2 ||
		got[1] != "note: the import of pack starter chose the pack's roles: the next yardr pack apply sets its role on web-builders again unless given --depot-roles" {
		t.Errorf("to the depot's:\n%s", out)
	}
	if got := sourcesLine(t, a, "role web-builders"); got != depots {
		t.Errorf("depot sources after: %s", got)
	}
	if g, _ := a.st.GetGroup(context.Background(), "web-builders"); g.Config["prompt"] != "roles/builder.md" || g.Config["kind"] != "claude" {
		t.Errorf("the group = %+v", g.Config)
	}
	if out, err = run(t, a.depotSourceCmd(), "web", "role", "web-builders", "depot"); err != nil || !strings.Contains(out, "unchanged") {
		t.Errorf("a second time: %v\n%s", err, out)
	}

	a.json = true
	out, err = run(t, a.depotSourceCmd(), "web", "role", "web-builders", "pack")
	a.json = false
	var res sourceSwitch
	if err := json.Unmarshal([]byte(out), &res); err != nil || !res.Changed || len(res.Lines) != 1 ||
		res.Lines[0].Source != sourcePack || res.Lines[0].Path != copied || len(res.Notes) != 0 {
		t.Errorf("to the pack's, --json: %v\n%s", err, out)
	}
	if got := sourcesLine(t, a, "role web-builders"); got != packs {
		t.Errorf("depot sources after: %s", got)
	}
}

// A crew member reads a path as it is, so its depot's role is the file in the
// depot's checkout: refused while there is none, then listed as the depot's.
func TestDepotSourceRoleOfACrew(t *testing.T) {
	t.Parallel()
	a, home, _ := packedYard(t)
	depot, err := a.st.GetDepot(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	refused(t, a, []string{"web", "role", "boss", "depot"}, "depot web has no .yardr/roles/yardmaster.md in its checkout")

	writeFile(t, filepath.Join(depot.Path, ".yardr", "roles", "yardmaster.md"), "the depot's yardmaster\n")
	out, err := run(t, a.depotSourceCmd(), "web", "role", "boss", "depot")
	if err != nil {
		t.Fatal(err)
	}
	want := "role boss depot (.yardr/roles/yardmaster.md) pack starter has a copy"
	if got := sourceLines(t, out); got[0] != want || !strings.Contains(out, "unless given --depot-roles") {
		t.Errorf("to the depot's:\n%s", out)
	}
	if got := sourcesLine(t, a, "role boss"); got != want {
		t.Errorf("depot sources after: %s", got)
	}
	if out, err = run(t, a.depotSourceCmd(), "web", "role", "boss", "pack"); err != nil {
		t.Fatal(err)
	}
	want = "role boss pack starter (" + filepath.Join(config.At(home).PackRoles("starter"), "yardmaster.md") + ")"
	if got := sourcesLine(t, a, "role boss"); !strings.HasPrefix(got, want) || !strings.HasPrefix(sourceLines(t, out)[0], want) {
		t.Errorf("back to the pack's: %s\n%s", got, out)
	}
}

// Under an import given --depot-roles, a group switched to the pack's role is
// told that the next import switches it back.
func TestDepotSourceRoleAgainstTheRecord(t *testing.T) {
	t.Parallel()
	a, _, _ := sourcesYard(t)
	requireNonInteractive(t)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes", "--depot-roles"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, a.depotSourceCmd(), "web", "role", "web-reviewers", "pack")
	if err != nil || !strings.HasPrefix(sourceLines(t, out)[0], "role web-reviewers pack starter ") || !strings.Contains(out, "unless given --pack-roles") {
		t.Errorf("%v\n%s", err, out)
	}
	if out, err = run(t, a.depotSourceCmd(), "web", "role", "web-reviewers", "depot"); err != nil || strings.Contains(out, "note:") {
		t.Errorf("back to what the record chose: %v\n%s", err, out)
	}
}

// A role is refused, changing nothing, when the chosen source does not
// exist: no pack imported, a pack without the role, a depot without the file,
// a name that is no role of the depot, a prompt that names no role.
func TestDepotSourceRoleRefusals(t *testing.T) {
	t.Parallel()
	a, home, _ := sourcesYard(t)
	ctx := context.Background()
	before := refused(t, a, []string{"web", "role", "web-builders", "pack"}, "no pack is imported into depot web")
	before = refusedSince(t, a, before, []string{"web", "role", "nobody", "depot"}, "depot web has no role nobody")
	before = refusedSince(t, a, before, []string{"web", "role", "web-builders", "home"}, "depot or pack")
	refusedSince(t, a, before, []string{"web", "role", "web-builders"}, "give role")

	// A group prompted from the home, with a role neither the depot nor the
	// pack has, and one whose prompt is no role at all.
	planner, plain := filepath.Join(home, "roles", "planner.md"), filepath.Join(home, "notes", "brief.txt")
	writeFile(t, planner, "the yard's planner\n")
	writeFile(t, plain, "a brief\n")
	depot := "web"
	for name, prompt := range map[string]string{"web-planners": planner, "web-plain": plain} {
		if err := a.st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": "claude", "prompt": prompt}}, "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.st.AddRoute(ctx, store.Route{Stage: name, Depot: &depot, Group: name}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	before = refused(t, a, []string{"web", "role", "web-planners", "depot"}, "depot web has no .yardr/roles/planner.md on its base main")
	refusedSince(t, a, before, []string{"web", "role", "web-plain", "depot"}, "names no role")

	requireNonInteractive(t)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	refused(t, a, []string{"web", "role", "web-planners", "pack"}, "no pack imported into depot web (starter) installed a role planner.md")
}

// The depot's flow switches to the repository's and back to the pack's, whole:
// the stage files come with it, and the output says so.
func TestDepotSourceFlow(t *testing.T) {
	t.Parallel()
	a, home, sha := packedYard(t)
	out, err := run(t, a.depotSourceCmd(), "web", "flow", "depot")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out,
		"flow default depot (.yardr/flows/default.flow@"+sha+") pack starter has flow starter.default",
		"stage new depot (.yardr/flows/default/new.md@"+sha+")",
		"stage review depot (.yardr/flows/default/review.md@"+sha+")",
		"the flow switched whole: its stage files are the ones above now",
		"note: type/train still comes from pack starter: yardr depot source web flow --type train depot",
	)
	if got := sourcesLine(t, a, "flow default"); !strings.HasPrefix(got, "flow default depot ") {
		t.Errorf("depot sources after: %s", got)
	}
	if out, err = run(t, a.depotSourceCmd(), "web", "flow", "depot"); err != nil || !strings.Contains(out, "unchanged") {
		t.Errorf("a second time: %v\n%s", err, out)
	}

	if out, err = run(t, a.depotSourceCmd(), "web", "flow", "pack"); err != nil {
		t.Fatal(err)
	}
	got := sourceLines(t, out)
	if len(got) != 8 || !strings.HasPrefix(got[0], "flow default pack starter (store starter.default) shadows .yardr/flows/default.flow") ||
		got[2] != "stage new pack starter ("+filepath.Join(home, "flows", "starter.default", "new.md")+") shadows .yardr/flows/default/new.md" || !strings.HasPrefix(got[7], "the flow switched whole") {
		t.Errorf("to the pack's:\n%s", out)
	}
	if got := sourcesLine(t, a, "flow default"); !strings.HasPrefix(got, "flow default pack starter ") {
		t.Errorf("depot sources after: %s", got)
	}
	if out, err = run(t, a.depotSourceCmd(), "web", "flow", "pack"); err != nil || !strings.Contains(out, "unchanged") {
		t.Errorf("a second time: %v\n%s", err, out)
	}
}

// A type's flow switches by its own pointer: to a pack's flow for the type,
// and back to the depot's type file. Switching the depot's flow names the type
// that stays where it is.
func TestDepotSourceFlowOfAType(t *testing.T) {
	t.Parallel()
	a, _, sha := sourcesYard(t)
	ctx := context.Background()
	// An import that pointed decisions at a flow of its own.
	if _, err := a.st.RecordPack(ctx, store.PackImport{Pack: "ask", Depot: "web", Objects: []store.PackObject{
		{Kind: "flow", Name: "code"}, {Kind: "pointer", Type: "decision", Flow: "code"}, {Kind: "pointer", Flow: "code"},
	}}, true, "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.depotSourceCmd(), "web", "flow", "--type", "decision", "pack")
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceLines(t, out); got[0] != "flow type/decision pack ask (store code) shadows .yardr/flows/type/decision.flow, .yardr/flows/type/decision/ask.md" {
		t.Errorf("to the pack's:\n%s", out)
	}
	if out, err = run(t, a.depotSourceCmd(), "web", "flow", "pack"); err != nil {
		t.Fatal(err)
	}
	// Under the depot's pointer no type file governs.
	refused(t, a, []string{"web", "flow", "--type", "decision", "depot"}, "switch the depot's flow first")
	if out, err = run(t, a.depotSourceCmd(), "web", "flow", "depot"); err != nil ||
		!strings.Contains(out, "note: type/decision still comes from pack ask: yardr depot source web flow --type decision depot") {
		t.Errorf("the depot's flow, a type left behind: %v\n%s", err, out)
	}
	if out, err = run(t, a.depotSourceCmd(), "web", "flow", "--type", "decision", "depot"); err != nil {
		t.Fatal(err)
	}
	wantLines(t, out,
		"flow type/decision depot (.yardr/flows/type/decision.flow@"+sha+")",
		"stage decision:ask depot (.yardr/flows/type/decision/ask.md@"+sha+")",
		"the flow switched whole: its stage files are the ones above now",
	)
}

// A flow is refused, changing nothing, when the chosen source does not
// exist (no pack imported, a pack without a flow for the type, a depot
// without the flow file) and when the store refuses the pointer for an open
// bead in a stage the new flow lacks.
func TestDepotSourceFlowRefusals(t *testing.T) {
	t.Parallel()
	a, _, _ := sourcesYard(t)
	ctx := context.Background()
	before := refused(t, a, []string{"web", "flow", "pack"}, "no pack is imported into depot web")
	before = refusedSince(t, a, before, []string{"web", "flow", "--type", "bug", "depot"}, "depot web has no .yardr/flows/type/bug.flow on its base main")
	refusedSince(t, a, before, []string{"web", "flow", "builders", "depot"}, "give role")
	// api is a dir depot pointed at a store flow, with no flow file of its own.
	if _, err := run(t, a.depotSourceCmd(), "api", "flow", "depot"); err == nil || !strings.Contains(err.Error(), "depot api has no .yardr/flows/default.flow in its directory") {
		t.Errorf("a depot without a flow file: %v", err)
	}

	requireNonInteractive(t)
	if out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	refused(t, a, []string{"web", "flow", "--type", "decision", "pack"}, "no pack imported into depot web (starter) has a flow for beads of type decision")

	// approved is a stage of the pack's flow the repository's lacks.
	b, err := a.st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "approved"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	refused(t, a, []string{"web", "flow", "depot"}, "would leave open beads in stages their flow lacks", b.ID+" (approved")
}

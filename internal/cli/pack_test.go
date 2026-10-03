package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func packApp(t *testing.T) *app {
	t.Helper()
	st := storetest.New(t)
	if err := st.PutRig(context.Background(), store.Rig{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "me", cfg: &config.Config{Home: t.TempDir()}}
}

func TestPackCheckPrintsThePlan(t *testing.T) {
	a := packApp(t)
	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--rig", "web", "--resident", "mayor=boss")
	if err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(a.cfg.Home, "packs", "starter")
	for _, want := range []string{
		"into rig web\n",
		"its roles and scripts would be copied to " + install + "\n",
		"  create           roles/builder.md -> " + filepath.Join(install, "roles", "builder.md") + "\n",
		"  create           web-builders\n                     asks: group.web-builders.kind\n",
		"  create           rig web stage approved -> web-refinery\n",
		"  create           boss linear involved\n",
		"an import would ask\n  group.web-builders.kind: which agent runs group web-builders",
		"flows governing rig web afterwards\n  the rig's beads: starter.default (store); now: none\n",
		"source linear, for resident boss\n  missing  hook ",
		"20 to create, 0 to change, 0 unchanged, 0 not in the pack\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q", want)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
	// --resident named it, so the import need not ask who takes mayor.
	if strings.Contains(out, "resident.boss.resident") {
		t.Errorf("asks who takes mayor though --resident said:\n%s", out)
	}

	a.json = true
	out, err = run(t, a.packCmd(), "check", "../../packs/starter", "--rig", "web")
	if err != nil {
		t.Fatal(err)
	}
	var plan pack.Plan
	if err := json.Unmarshal([]byte(out), &plan); err != nil || plan.Pack != "starter" || len(plan.Objects) != 20 || len(plan.Questions) != 10 {
		t.Fatalf("json plan: %v\n%s", err, out)
	}
}

// An invalid pack exits non-zero, each mistake with file, line and key,
// and the store is not even asked.
func TestPackCheckInvalidPackFails(t *testing.T) {
	dir := t.TempDir()
	text := "name: bad\nflows:\n  default:\n    stages:\n      new: { group: nobody, on: { done: new } }\n"
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{actor: "me"} // no store: reaching for one would fail the test's home
	_, err := run(t, a.packCmd(), "check", dir, "--rig", "web")
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d), want exit 1", err, exitCode(err))
	}
	file := filepath.Join(dir, "pack.yaml")
	for _, want := range []string{
		file + ":4: flows.default.stages: flow default has no end",
		file + ":5: flows.default.stages.new.group: no group nobody is declared",
		file + ":5: flows.default.stages.new.on: YAML reads the bare key on as a boolean",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}

	a.json = true
	out, err := run(t, a.packCmd(), "check", dir, "--rig", "web")
	var got struct {
		Errors []pack.Error `json:"errors"`
	}
	if !errors.Is(err, errCheckFailed) || json.Unmarshal([]byte(out), &got) != nil || len(got.Errors) != 3 || got.Errors[0].Line != 4 {
		t.Fatalf("json: %v\n%s", err, out)
	}

	if _, err := run(t, packApp(t).packCmd(), "check", "../../packs/starter"); err == nil || !strings.Contains(err.Error(), "rig") {
		t.Fatalf("without --rig: %v", err)
	}
}

// A plan the import would refuse is printed whole, then fails the check.
func TestPackCheckRefusedPlanFails(t *testing.T) {
	a := packApp(t)
	b, err := a.st.CreateBead(context.Background(), store.NewBead{Title: "stray", Rig: "web", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--rig", "web")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want the check failed", err)
	}
	if !strings.Contains(out, "an import would be refused\n  flow starter.default lacks the stage of open beads that would follow it: "+b.ID+" (triage)") ||
		!strings.Contains(out, "20 to create") {
		t.Fatalf("out:\n%s", out)
	}
}

// starterFlags are the flags that answer everything the starter pack asks
// of an empty yard.
var starterFlags = []string{"--resident", "mayor=boss", "--bind", "builders=claude:opus", "--bind", "reviewers=codex",
	"--bind", "resident:mayor=claude", "--members", "backlog=", "--members", "decisions=ann,boss"}

// Without a terminal pack apply asks nothing: a missing answer is an error
// naming the flag that gives it, never a default, and nothing is changed.
// With the answers but no --yes it still does not apply.
func TestPackApplyWithoutATerminalNeedsEveryAnswer(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	ctx := context.Background()
	unchanged := func() {
		t.Helper()
		groups, _ := a.st.Groups(ctx)
		recs, _ := a.st.Packs(ctx)
		if _, err := os.Stat(filepath.Join(a.cfg.Home, "packs")); len(groups) != 0 || len(recs) != 0 || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("something was changed: groups %v, records %v, files %v", groups, recs, err)
		}
	}

	_, err := run(t, a.packCmd(), "apply", "../../packs/starter", "--rig", "web", "--yes")
	if err == nil || !strings.Contains(err.Error(), "cannot ask without a terminal") || !strings.Contains(err.Error(), "--resident mayor=<resident>") {
		t.Fatalf("err = %v, want the --resident flag named", err)
	}
	unchanged()

	_, err = run(t, a.packCmd(), "apply", "../../packs/starter", "--rig", "web", "--yes", "--resident", "mayor=boss", "--bind", "builders=claude")
	for _, want := range []string{"cannot ask without a terminal", "--bind reviewers=<kind>[:<model>] for the new group web-reviewers",
		"--members backlog=<a,b> for the new group backlog", "--bind resident:mayor=<kind>[:<model>] for the new resident boss"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q:\n%v", want, err)
		}
	}
	if err != nil && strings.Contains(err.Error(), "--bind builders=") {
		t.Errorf("asks for builders, which --bind answered:\n%v", err)
	}
	unchanged()

	_, err = run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--rig", "web"}, starterFlags...)...)
	if err == nil || !strings.Contains(err.Error(), "give --yes") {
		t.Fatalf("without --yes: %v", err)
	}
	unchanged()

	_, err = run(t, a.packCmd(), "apply", "../../packs/starter", "--rig", "web", "--yes", "--resident", "mayor=boss", "--bind", "coders=claude")
	if err == nil || !strings.Contains(err.Error(), "the pack has no group coders") {
		t.Fatalf("--bind for a group the pack lacks: %v", err)
	}
	unchanged()
}

// With every answer in flags and --yes, pack apply imports without a
// terminal; the same command again changes nothing; pack list shows the
// record.
func TestPackApplyWithFlags(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	ctx := context.Background()
	args := append([]string{"apply", "../../packs/starter", "--rig", "web", "--yes"}, starterFlags...)
	out, err := run(t, a.packCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"pack starter (", "20 to create", "\napplied pack starter to rig web: 20 created, 0 changed, 0 unchanged\n",
		"group web-builders is new and has no agent arguments"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	g, err := a.st.GetGroup(ctx, "web-builders")
	if err != nil || g.Config["kind"] != "claude" || g.Config["model"] != "opus" {
		t.Fatalf("web-builders = %+v, %v", g, err)
	}
	if g, _ := a.st.GetGroup(ctx, "web-reviewers"); g == nil || g.Config["kind"] != "codex" || g.Config["model"] != nil {
		t.Fatalf("web-reviewers = %+v", g)
	}
	if g, _ := a.st.GetGroup(ctx, "backlog"); g == nil || len(g.Members) != 0 {
		t.Fatalf("backlog = %+v", g)
	}
	if g, _ := a.st.GetGroup(ctx, "decisions"); g == nil || strings.Join(g.Members, ",") != "ann,boss" {
		t.Fatalf("decisions = %+v", g)
	}
	boss, err := a.st.GetResident(ctx, "boss")
	if err != nil || boss.Config["kind"] != "claude" || boss.Dir != a.cfg.Home {
		t.Fatalf("boss = %+v, %v", boss, err)
	}

	head, _ := a.st.Head(ctx)
	out, err = run(t, a.packCmd(), args...)
	if err != nil || !strings.Contains(out, "pack starter is already applied to rig web: nothing changed\n") {
		t.Fatalf("second apply: %v\n%s", err, out)
	}
	if evs, _ := a.st.EventsSince(ctx, head, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" || evs[0].Actor != "me" {
		t.Fatalf("the second apply logged %+v", evs)
	}
	// And with no flags at all: the record names the resident, and nothing
	// is left to ask or to confirm.
	if out, err = run(t, a.packCmd(), "apply", "../../packs/starter", "--rig", "web"); err != nil || !strings.Contains(out, "nothing changed") {
		t.Fatalf("apply without flags over an applied pack: %v\n%s", err, out)
	}

	out, err = run(t, a.packCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"starter in rig web  ", " by me\n", "  source     ", "  flow       default (stored as starter.default)\n",
		"builders -> web-builders", "  residents  mayor -> boss\n", "  objects    4 files, 5 groups, 1 flows, 1 flow pointers, 5 routes, 3 subscriptions\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("pack list lacks %q:\n%s", want, out)
		}
	}

	a.json = true
	out, err = run(t, a.packCmd(), args...)
	var got struct {
		Plan   pack.Plan   `json:"plan"`
		Result pack.Result `json:"result"`
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Result.Unchanged != 20 || got.Result.Recorded || got.Plan.Rig != "web" {
		t.Fatalf("json: %v\n%s", err, out)
	}
	out, err = run(t, a.packCmd(), "list")
	var recs []store.PackImport
	if err != nil || json.Unmarshal([]byte(out), &recs) != nil || len(recs) != 1 || recs[0].Residents["mayor"] != "boss" {
		t.Fatalf("json list: %v\n%s", err, out)
	}
}

// In a terminal pack apply asks: who takes the pack's resident, the members
// of a new people group, and whether to apply. A form that is left at the
// confirmation has changed nothing.
func TestPackApplyAsksInATerminal(t *testing.T) {
	a := packApp(t)
	setInteractive(t, true)
	ctx := context.Background()
	var asked []string
	decline := true
	stubAsk(t, func(fields ...huh.Field) error {
		for _, f := range fields {
			asked = append(asked, f.GetKey()+fmt.Sprintf("%T", f))
			if _, ok := f.(*huh.Confirm); ok && decline {
				return errDeclined
			}
		}
		return nil
	})
	args := []string{"apply", "../../packs/starter", "--rig", "web", "--bind", "builders=claude", "--bind", "reviewers=claude", "--bind", "resident:mayor=claude"}
	out, err := run(t, a.packCmd(), args...)
	if !errors.Is(err, errDeclined) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if groups, _ := a.st.Groups(ctx); len(groups) != 0 {
		t.Fatalf("groups after leaving the form: %v", groups)
	}
	// A select for the resident, an input per people group, the confirm.
	if len(asked) != 4 {
		t.Fatalf("asked %v", asked)
	}

	decline = false
	if out, err = run(t, a.packCmd(), args...); err != nil || !strings.Contains(out, "applied pack starter to rig web: 20 created") {
		t.Fatalf("%v\n%s", err, out)
	}
	// The select's default is a new resident under the pack's name.
	if _, err := a.st.GetResident(ctx, "mayor"); err != nil {
		t.Fatal(err)
	}
}

var errDeclined = errors.New("declined")

// A refused import prints the plan and why, exits 1, and changes nothing.
func TestPackApplyRefused(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	b, err := a.st.CreateBead(context.Background(), store.NewBead{Title: "stray", Rig: "web", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--rig", "web", "--yes"}, starterFlags...)...)
	if !errors.Is(err, errCheckFailed) || !strings.Contains(out, "an import would be refused\n  flow starter.default lacks the stage of open beads that would follow it: "+b.ID+" (triage)") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if groups, _ := a.st.Groups(context.Background()); len(groups) != 0 {
		t.Fatalf("a refused import made groups: %v", groups)
	}
}

// What the pack has dropped is listed by check and by apply, and removed
// by apply --prune.
func TestPackApplyPrune(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../packs/starter")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "scripts", "merge-local"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"apply", dir, "--rig", "web", "--yes"}, starterFlags...)
	if out, err := run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	path := filepath.Join(dir, "pack.yaml")
	text, _ := os.ReadFile(path)
	s := strings.Replace(string(text), "      stages: [decide]\n", "", 1)
	if s == string(text) {
		t.Fatal("pack.yaml has no hears.stages line")
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	const sub = "boss yardr all rig=web stage=decide"
	out, err := run(t, a.packCmd(), "check", dir, "--rig", "web")
	if err != nil || !strings.Contains(out, "  no longer in it  "+sub+" (an earlier import brought it; left alone unless --prune)\n") ||
		!strings.Contains(out, ", 1 no longer in it\n") {
		t.Fatalf("check: %v\n%s", err, out)
	}
	out, err = run(t, a.packCmd(), "apply", dir, "--rig", "web")
	if err != nil || !strings.Contains(out, "no longer in the pack and left in the yard (yardr pack apply … --prune removes them):\n  subscription "+sub+"\n") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// A prune writes, so it needs --yes like any other change.
	if _, err = run(t, a.packCmd(), "apply", dir, "--rig", "web", "--prune"); err == nil || !strings.Contains(err.Error(), "give --yes") {
		t.Fatalf("prune without --yes: %v", err)
	}
	out, err = run(t, a.packCmd(), "apply", dir, "--rig", "web", "--prune", "--yes")
	if err != nil || !strings.Contains(out, "  pruned  subscription "+sub+"\n") {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	boss, _ := a.st.GetResident(context.Background(), "boss")
	if len(boss.Subscriptions) != 2 {
		t.Fatalf("boss subscriptions = %v", boss.Subscriptions)
	}
}

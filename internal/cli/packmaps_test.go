package cli

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

const starterMapLines = "task:merged=done task:new=started task:review-security=review task:review=review"

// mapApp is openApp with boss known in linear, which a hook that acts as
// it needs.
func mapApp(t *testing.T, sources ...string) *app {
	t.Helper()
	a := openApp(t, sources...)
	if err := a.st.SetIdentity(context.Background(), "boss", "linear", store.Identity{ID: "user-boss"}, "me"); err != nil {
		t.Fatal(err)
	}
	return a
}

func applyStarter(t *testing.T, a *app, more ...string) string {
	t.Helper()
	args := append(append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes"}, starterFlags...), more...)
	out, err := run(t, a.packCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out
}

func linearHook(t *testing.T, a *app) store.Hook {
	t.Helper()
	h, err := a.st.GetHook(context.Background(), "linear")
	if err != nil {
		t.Fatal(err)
	}
	return *h
}

// Importing the starter pack into a yard with a linear hook sets the map
// and the crew member on that hook, which yardr hook show prints. The plan and
// the output name the set_state the crew member lacks and the command that
// grants it, and the import does not grant it. A second apply changes
// nothing and logs pack_unchanged.
func TestPackApplySetsTheStageMapOnTheYardsHook(t *testing.T) {
	t.Parallel()
	a := mapApp(t, "linear")
	ctx := context.Background()
	const grant = "crew member boss may not set a status in linear, so no issue is moved. The import does not grant it: the operator allows it with yardr crew set boss set_state=linear\n"

	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web", "--crew", "yardmaster=boss")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\nstage maps\n  create           hook linear as boss\n",
		"\nstage map of source linear, as crew member boss\n  the pack says " + starterMapLines + "\n  would be set on hook linear, which has none\n",
		"  ok       hook         hook linear\n",
		"  ok       identity     crew member boss has an identity in linear\n",
		"  missing  set state    " + grant,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	if h := linearHook(t, a); h.As != "" || len(h.Map) != 0 {
		t.Fatalf("pack check set the map: %+v", h)
	}

	out = applyStarter(t, a)
	for _, want := range []string{
		"\nhook linear now moves tracked linear issues as boss: " + starterMapLines + "\n",
		"\nnote: " + grant,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if h := linearHook(t, a); h.As != "boss" || !maps.Equal(h.Map, map[string]string{"task:new": "started", "task:review": "review", "task:review-security": "review", "task:merged": "done"}) {
		t.Fatalf("hook = %+v", h)
	}
	show, err := run(t, a.hookCmd(), "show", "linear")
	if err != nil || !strings.Contains(show, "  acts as boss\n  map task:merged=done\n  map task:new=started\n  map task:review-security=review\n  map task:review=review\n") {
		t.Fatalf("hook show: %v\n%s", err, show)
	}
	if r, _ := a.st.GetCrew(ctx, "boss"); len(r.MaySetState()) != 0 {
		t.Fatalf("the import let boss set a status in %v", r.MaySetState())
	}

	head, _ := a.st.Head(ctx)
	out = applyStarter(t, a)
	if !strings.Contains(out, "nothing changed") || !strings.Contains(out, "  hook linear has it\n") || !strings.Contains(out, "\nnote: "+grant) {
		t.Fatalf("second apply:\n%s", out)
	}
	evs, _ := a.st.EventsSince(ctx, head, 0)
	if len(evs) != 1 || evs[0].Kind != "pack_unchanged" {
		t.Fatalf("second apply logged %+v", evs)
	}

	// Once the operator has allowed it, nothing is left to say.
	boss, _ := a.st.GetCrew(ctx, "boss")
	boss.Config[store.SetState] = "linear"
	if _, err := a.st.PutCrew(ctx, *boss, "me"); err != nil {
		t.Fatal(err)
	}
	if out = applyStarter(t, a); strings.Contains(out, "may not set a status") || !strings.Contains(out, "  ok       set state    crew member boss may set a status in linear\n") {
		t.Fatalf("with set_state:\n%s", out)
	}
}

// Importing ci-red into a depot on the starter flow sets its makes line on
// the yard's github hook, beside the lines there: a failed run on main then
// makes one task bead in new. check plans the one line and refuses nothing.
func TestPackApplySetsTheMakesLineOnTheYardsHook(t *testing.T) {
	t.Parallel()
	a := openApp(t, "github")
	ctx := context.Background()
	applyStarter(t, a)

	out, err := run(t, a.packCmd(), "check", "ci-red", "--depot", "web")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	const line = "conclusion=failure event=workflow_run head_branch=main -> task:new"
	for _, want := range []string{
		"\nwhat the events of source github say of a bead, and make\n  the pack makes " + line + "\n",
		"  would be set on hook github, which has none\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "an import would be refused") {
		t.Errorf("pack check refuses the line:\n%s", out)
	}

	out, err = run(t, a.packCmd(), "apply", "ci-red", "--depot", "web", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\napplied pack ci-red to depot web: ",
		"\nhook github's events now make a bead for a thread none tracks:\n  " + line + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	h, err := a.st.GetHook(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	if got := store.MakeLines(h.Makes); !slices.Equal(got, []string{line}) {
		t.Fatalf("the hook makes %v, want the pack's line", got)
	}
	if len(h.Says) == 0 {
		t.Fatalf("the import took the starter's says lines off the hook: %+v", h)
	}
}

// The same pack checked against a depot whose flow lacks new is refused at
// plan time, in the same words the reader refuses a pack's own line.
func TestPackCheckRefusesAMakesLineTheDepotsFlowLacks(t *testing.T) {
	t.Parallel()
	a := openApp(t, "github")
	ctx := context.Background()
	f, err := store.ParseFlow("open -> shut on done\nshut terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "line"
	if _, err := a.st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetFlowPointer(ctx, "web", "", "line", "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), "check", "ci-red", "--depot", "web")
	if err == nil {
		t.Fatalf("pack check passed:\n%s", out)
	}
	const want = "sources.github.makes[0]: no bead of type task could be made there: flow line: no stage new a bead can be made in (stages: open)"
	if !strings.Contains(out, want) {
		t.Errorf("pack check lacks %q:\n%s", want, out)
	}
}

// A yard without a hook of the source: the import goes through, makes no
// hook and says which to add. After yardr hook add, the next apply sets the
// map on it.
func TestPackApplyWithoutTheHookSaysWhatToAdd(t *testing.T) {
	t.Parallel()
	a := mapApp(t)
	ctx := context.Background()
	out := applyStarter(t, a)
	for _, want := range []string{
		"\nstage map of source linear, as crew member boss\n  the pack says " + starterMapLines + "\n  nothing would be set: the import goes through without it",
		"  missing  hook         no hook receives linear, and the import makes none: yardr hook add linear --source linear --secret-file <file>, then apply the pack again\n",
		"\napplied pack starter to depot web: ",
		"\nthe stage map of linear was not set: no hook receives linear, and the import makes none: yardr hook add linear --source linear --secret-file <file>, then apply the pack again\n",
		"\nnote: crew member boss may not set a status in linear",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if hs, _ := a.st.Hooks(ctx); len(hs) != 0 {
		t.Fatalf("the import made a hook: %+v", hs)
	}

	secret := filepath.Join(t.TempDir(), "linear.secret")
	if err := os.WriteFile(secret, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.hookCmd(), "add", "linear", "--source", "linear", "--secret-file", secret); err != nil {
		t.Fatalf("hook add: %v\n%s", err, out)
	}
	out = applyStarter(t, a)
	if !strings.Contains(out, "\nhook linear now moves tracked linear issues as boss: "+starterMapLines+"\n") {
		t.Fatalf("the apply after hook add:\n%s", out)
	}
	if h := linearHook(t, a); h.As != "boss" || len(h.Map) != 4 || h.SecretFile != secret {
		t.Fatalf("hook = %+v", h)
	}
}

// A hook that acts as a crew member speaks as it, so only the operator or that
// crew member's own session has the import set the map: anyone else gets the
// refusal yardr hook add --as gives and the rest of the pack.
func TestPackApplyByAnotherSessionSetsNoStageMap(t *testing.T) {
	t.Parallel()
	a := mapApp(t, "linear")
	ctx := context.Background()
	// Boss already hears what the pack says: its subscriptions would fail
	// another session's import loud, and the test is the stage map.
	for _, sub := range []store.Subscription{
		{Source: "linear", Scope: store.ScopeInvolved},
		{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"depot": "web", "stage": "decide"}},
		{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"depot": "web", "stage": "clear"}},
		{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"depot": "web", "state": store.StateDone}},
	} {
		if err := a.st.AddSubscription(ctx, "boss", sub, "boss"); err != nil {
			t.Fatal(err)
		}
	}
	seedPackHeld(t, a, "../../packs/starter")
	a.actor = "web-builder-1"
	const refusal = "only boss's own session or the operator (me) may speak as boss; this is web-builder-1"
	out := applyStarter(t, a)
	for _, want := range []string{
		"\nstage map of source linear, as crew member boss\n  the pack says " + starterMapLines + "\n  not set on hook linear: ",
		"\napplied pack starter to depot web: ",
		"\nthe stage map of linear was not set on hook linear: pack apply: the stage map of linear --crew boss: " + refusal + ". The rest of the import does not depend on it\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if h := linearHook(t, a); h.As != "" || len(h.Map) != 0 {
		t.Fatalf("hook = %+v", h)
	}
	if _, err := a.st.GetGroup(ctx, "web-builders"); err != nil {
		t.Fatalf("the rest of the pack was not imported: %v", err)
	}
	recs, _ := a.st.Packs(ctx)
	if len(recs) != 1 || slices.ContainsFunc(recs[0].Objects, func(po store.PackObject) bool { return po.Kind == "map" }) {
		t.Fatalf("records = %+v", recs)
	}

	// As data too, and still not set on a second run by the same session.
	a.json = true
	var got struct {
		Refused map[string]string `json:"maps_refused"`
		Result  struct {
			Mapped []any `json:"mapped"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(applyStarter(t, a)), &got); err != nil || !strings.Contains(got.Refused["linear"], refusal) || len(got.Result.Mapped) != 0 {
		t.Fatalf("--json: %v, %+v", err, got)
	}
	a.json = false

	// The crew member's own session may, as it may run hook add --as itself.
	a.actor = "boss"
	if out = applyStarter(t, a); !strings.Contains(out, "\nhook linear now moves tracked linear issues as boss: ") {
		t.Fatalf("as boss:\n%s", out)
	}
	if h := linearHook(t, a); h.As != "boss" {
		t.Fatalf("hook = %+v", h)
	}
}

// A hook with another map gets the pack's, and the plan shows both.
func TestPackApplyShowsTheMapItReplaces(t *testing.T) {
	t.Parallel()
	a := mapApp(t, "linear")
	ctx := context.Background()
	if _, err := a.st.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: "/yard/secret", As: "boss", Map: map[string]string{"task:review": "done"}}, "me"); err != nil {
		t.Fatal(err)
	}
	out := applyStarter(t, a)
	for _, want := range []string{
		"\nstage maps\n  change           hook linear as boss\n                     map: task:review=done -> " + starterMapLines + "\n",
		"  would replace what hook linear has: as boss, task:review=done\n",
		"\nhook linear now moves tracked linear issues as boss: " + starterMapLines + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if h := linearHook(t, a); len(h.Map) != 4 || h.SecretFile != "/yard/secret" {
		t.Fatalf("hook = %+v", h)
	}
}

// A hook's tables are shared by the packs that carry the source: pack check
// shows the lines the pack would add and the ones it would drop, which are
// its own from its last import and no others, so the lines of another pack
// on the hook are never offered as drops and nothing needs --yes.
func TestPackCheckShowsTheLinesItAddsAndDrops(t *testing.T) {
	t.Parallel()
	a := openApp(t, "github")
	requireNonInteractive(t)
	ctx := context.Background()
	if out := applyStarter(t, a); !strings.Contains(out, "hook github's events now say of a bead:") {
		t.Fatalf("the starter's apply:\n%s", out)
	}
	hook := func() store.Hook {
		h, err := a.st.GetHook(ctx, "github")
		if err != nil {
			t.Fatal(err)
		}
		return *h
	}
	trains := hook().Says
	if len(trains) != 6 {
		t.Fatalf("the starter's lines on the hook: %v", store.SayLines(trains))
	}

	// The contributors pack onto the starter's hook: lines to add, none to drop.
	check := []string{"--depot", "web", "--crew", "yardmaster=boss"}
	out, err := run(t, a.packCmd(), append([]string{"check", "../../packs/contributors"}, check...)...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"  would add the says line \"action=closed event=pull_request merged=true type=pr -> merged\" to hook github\n",
		"  would add the says line \"action=closed event=issues stage=approved stage=backlog stage=decide stage=new stage=review stage=triage type=issue -> dropped\" to hook github\n",
		"  would add the makes line \"action=opened action=reopened event=issues -> issue:triage\" to hook github\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	// The hook's six lines of the starter's are none of its to drop.
	for _, not := range []string{"would drop", "--yes drops"} {
		if strings.Contains(out, not) {
			t.Errorf("pack check of the contributors pack says %q:\n%s", not, out)
		}
	}
	// Applied, the hook has the lines of both, the starter's in their places
	// and the pack's after them as it gives them. They are read from the pack,
	// not counted, so that a line the pack gains does not break this.
	applied := append([]string{"apply", "../../packs/contributors", "--yes", "--members", "maintainers=jo", "--members", "outside=", "--bind", "pr-reviewers=claude,read-only"}, check...)
	if out, err := run(t, a.packCmd(), applied...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	contributors, err := pack.LoadDir("../../packs/contributors")
	if err != nil {
		t.Fatal(err)
	}
	gh := contributors.Sources[0]
	if h := hook(); !store.SameSays(h.Says, append(slices.Clone(trains), gh.Says...)) || !store.SameMakes(h.Makes, gh.Makes) {
		t.Fatalf("hook after the contributors pack: %v, makes %v", store.SayLines(h.Says), store.MakeLines(h.Makes))
	}

	// A pack that lets go of a line would drop that one alone.
	dir := filepath.Join(t.TempDir(), "contributors")
	if err := os.CopyFS(dir, os.DirFS("../../packs/contributors")); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(dir, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const gone = `      - { where: "event=issues action=closed stage=triage stage=backlog stage=new stage=review stage=approved stage=decide type=issue", outcome: dropped }` + "\n"
	if !strings.Contains(string(text), gone) {
		t.Fatalf("pack.yaml lacks %q", gone)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(strings.Replace(string(text), gone, "", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.packCmd(), append([]string{"check", dir}, check...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "  would drop the says line \"action=closed event=issues stage=approved stage=backlog stage=decide stage=new stage=review stage=triage type=issue -> dropped\" from hook github: the pack set it at its last import and no longer gives it\n"; !strings.Contains(out, want) ||
		strings.Count(out, "would drop") != 1 || strings.Contains(out, "would add") {
		t.Fatalf("pack check of the pack without the line:\n%s", out)
	}
}

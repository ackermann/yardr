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

	"yardr.dev/yardr/internal/store"
)

const starterMapLines = "task:merged=done task:new=started task:review=review"

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
	if h := linearHook(t, a); h.As != "boss" || !maps.Equal(h.Map, map[string]string{"task:new": "started", "task:review": "review", "task:merged": "done"}) {
		t.Fatalf("hook = %+v", h)
	}
	show, err := run(t, a.hookCmd(), "show", "linear")
	if err != nil || !strings.Contains(show, "  acts as boss\n  map task:merged=done\n  map task:new=started\n  map task:review=review\n") {
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
	if h := linearHook(t, a); h.As != "boss" || len(h.Map) != 3 || h.SecretFile != secret {
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
		{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"depot": "web", "state": store.StateDone}},
	} {
		if err := a.st.AddSubscription(ctx, "boss", sub, "boss"); err != nil {
			t.Fatal(err)
		}
	}
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
	if h := linearHook(t, a); len(h.Map) != 3 || h.SecretFile != "/yard/secret" {
		t.Fatalf("hook = %+v", h)
	}
}

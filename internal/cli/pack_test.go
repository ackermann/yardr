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

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func packApp(t *testing.T) *app {
	t.Helper()
	st := storetest.New(t)
	if err := st.PutDepot(context.Background(), store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "me", cfg: &config.Config{Home: t.TempDir()}}
}

func TestPackCheckPrintsThePlan(t *testing.T) {
	a := packApp(t)
	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web", "--crew", "yardmaster=boss")
	if err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(a.cfg.Home, "packs", "starter")
	for _, want := range []string{
		"), kind git, into depot web\n",
		"its roles and scripts would be copied to " + install + "\n",
		"in its roles and stage files {{operator}} would read " + defaultOperator() + ", {{yardmaster}} would read boss\n",
		"  create           roles/builder.md -> " + filepath.Join(install, "roles", "builder.md") + "\n",
		"  create           web-builders\n                     asks: group.web-builders.kind\n",
		"  create           depot web stage approved -> web-assembly\n",
		"  create           boss linear involved\n",
		"an import would ask\n  group.web-builders.kind: which agent runs group web-builders",
		"flows governing depot web afterwards\n  the depot's beads: starter.default (store); now: none\n",
		"source linear, for crew member boss\n  missing  hook ",
		"its stage files would be copied to " + filepath.Join(a.cfg.Home, "flows", "starter.<flow>") + "\n",
		"  create           flows/default/new.md -> " + filepath.Join(a.cfg.Home, "flows", "starter.default", "new.md") + "\n",
		"25 to create, 0 to change, 0 unchanged, 0 not in the pack\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q", want)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
	// --crew named it, so the import need not ask who takes yardmaster.
	if strings.Contains(out, "crew.boss.crew") {
		t.Errorf("asks who takes yardmaster though --crew said:\n%s", out)
	}

	a.json = true
	out, err = run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web")
	if err != nil {
		t.Fatal(err)
	}
	var plan pack.Plan
	if err := json.Unmarshal([]byte(out), &plan); err != nil || plan.Pack != "starter" || len(plan.Objects) != 25 || len(plan.Questions) != 13 {
		t.Fatalf("json plan: %v\n%s", err, out)
	}
}

// An invalid pack exits non-zero, each mistake with file, line and key,
// and the store is not even asked.
func TestPackCheckInvalidPackFails(t *testing.T) {
	dir := t.TempDir()
	text := "name: bad\nflows:\n  default:\n    stages:\n      new: { group: nobody, on: { done: new } }\nkind: svn\n"
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	// No store, and a home with no yard in it: reaching for one fails, where
	// the home of whoever runs the tests may have a server to answer.
	t.Setenv("YARDR_HOME", t.TempDir())
	a := &app{actor: "me"}
	_, err := run(t, a.packCmd(), "check", dir, "--depot", "web")
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d), want exit 1", err, exitCode(err))
	}
	file := filepath.Join(dir, "pack.yaml")
	for _, want := range []string{
		file + ":4: flows.default.stages: flow default has no end",
		file + ":5: flows.default.stages.new.group: no group nobody is declared",
		file + ":5: flows.default.stages.new.on: YAML reads the bare key on as a boolean",
		file + `:6: kind: kind "svn": a pack is for a depot of kind git or dir`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}

	a.json = true
	out, err := run(t, a.packCmd(), "check", dir, "--depot", "web")
	var got struct {
		Errors []pack.Error `json:"errors"`
	}
	if !errors.Is(err, errCheckFailed) || json.Unmarshal([]byte(out), &got) != nil || len(got.Errors) != 4 || got.Errors[0].Line != 4 {
		t.Fatalf("json: %v\n%s", err, out)
	}

	// pack apply reads a directory first too.
	a.json = false
	if _, err := run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes"); err == nil || !strings.Contains(err.Error(), file+":4: flows.default.stages: flow default has no end") {
		t.Fatalf("apply: %v", err)
	}

	if _, err := run(t, packApp(t).packCmd(), "check", "../../packs/starter"); err == nil || !strings.Contains(err.Error(), "depot") {
		t.Fatalf("without --depot: %v", err)
	}
}

// pack check on a directory passes the first-run check (yardr-qmy2): on a
// home with no yard an invalid pack is told its mistakes, and a valid one
// stops with the 'no yard' line where its plan needs the yard. A pack given
// by name needs the yard from the start. No store is made, no server started.
func TestPackCheckDirWithoutAYard(t *testing.T) {
	home := newHome(t)
	setInteractive(t, false)
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "pack.yaml"), []byte("name: bad\nkind: svn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check := func(pack string) (int, string) {
		t.Helper()
		errs := captureStderr(t)
		var code int
		capture(t, func() { code = execute(&app{}, []string{"pack", "check", pack, "--depot", "web"}) })
		return code, errs.String()
	}
	noYard := "no yard in " + home + " yet"

	code, said := check(bad)
	if code != 1 || !strings.Contains(said, filepath.Join(bad, "pack.yaml")+`:2: kind: kind "svn"`) || strings.Contains(said, noYard) {
		t.Errorf("an invalid directory pack: exit %d, want 1 and its mistakes:\n%s", code, said)
	}
	if code, said = check("../../packs/starter"); code != 2 || !strings.Contains(said, noYard) {
		t.Errorf("a valid directory pack: exit %d, want 2 and the 'no yard' line:\n%s", code, said)
	}
	if code, said = check("starter"); code != 2 || !strings.Contains(said, noYard) {
		t.Errorf("a pack by name: exit %d, want 2 and the 'no yard' line:\n%s", code, said)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, made := range []string{cfg.DBPath(), cfg.PIDFile(), cfg.Socket()} {
		if _, err := os.Stat(made); err == nil {
			t.Errorf("%s exists: pack check reached for a store", made)
		}
	}
}

// A plan the import would refuse is printed whole, then fails the check.
func TestPackCheckRefusedPlanFails(t *testing.T) {
	a := packApp(t)
	b, err := a.st.CreateBead(context.Background(), store.NewBead{Title: "stray", Depot: "web", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want the check failed", err)
	}
	if !strings.Contains(out, "an import would be refused\n  flow starter.default lacks the stage of open beads that would follow it: "+b.ID+" (triage)") ||
		!strings.Contains(out, "25 to create") {
		t.Fatalf("out:\n%s", out)
	}
}

// starterFlags are the flags that answer everything the starter pack asks
// of an empty yard.
var starterFlags = []string{"--crew", "yardmaster=boss", "--bind", "builders=claude:opus", "--bind", "reviewers=codex",
	"--bind", "crew:yardmaster=claude", "--members", "backlog=", "--members", "decisions=ann,boss"}

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

	_, err := run(t, a.packCmd(), "apply", "../../packs/starter", "--depot", "web", "--yes")
	if err == nil || !strings.Contains(err.Error(), "cannot ask without a terminal") || !strings.Contains(err.Error(), "--crew yardmaster=<crew member>") {
		t.Fatalf("err = %v, want the --crew flag named", err)
	}
	unchanged()

	_, err = run(t, a.packCmd(), "apply", "../../packs/starter", "--depot", "web", "--yes", "--crew", "yardmaster=boss", "--bind", "builders=claude")
	for _, want := range []string{"cannot ask without a terminal", "--bind reviewers=<kind>[:<model>] for the new group web-reviewers",
		"--members backlog=<a,b> for the new group backlog", "--bind crew:yardmaster=<kind>[:<model>] for the new crew member boss"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q:\n%v", want, err)
		}
	}
	if err != nil && strings.Contains(err.Error(), "--bind builders=") {
		t.Errorf("asks for builders, which --bind answered:\n%v", err)
	}
	unchanged()

	_, err = run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--depot", "web"}, starterFlags...)...)
	if err == nil || !strings.Contains(err.Error(), "give --yes") {
		t.Fatalf("without --yes: %v", err)
	}
	unchanged()

	_, err = run(t, a.packCmd(), "apply", "../../packs/starter", "--depot", "web", "--yes", "--crew", "yardmaster=boss", "--bind", "coders=claude")
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
	args := append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes"}, starterFlags...)
	out, err := run(t, a.packCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// The plan says what each new agent gets, arguments included: nobody
	// is given them silently.
	for _, want := range []string{"pack starter (", "25 to create", "\napplied pack starter to depot web: 25 created, 0 changed, 0 unchanged\n",
		"  create           web-builders\n                     agent: claude\n                     model: opus\n                     arguments: --permission-mode auto\n",
		"  create           web-reviewers\n                     agent: codex\n                     model: the agent's own default\n                     arguments: --dangerously-bypass-approvals-and-sandbox\n",
		"  create           backlog\n                     members: anyone\n",
		"  create           boss\n                     agent: claude\n                     model: the agent's own default\n                     arguments: --permission-mode auto\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// Every flag is given: nothing is open, so nothing is listed as asked,
	// and no agent is left without arguments.
	for _, not := range []string{"an import would ask", "asks: ", "has no agent arguments"} {
		if strings.Contains(out, not) {
			t.Errorf("output has %q:\n%s", not, out)
		}
	}
	g, err := a.st.GetGroup(ctx, "web-builders")
	if err != nil || g.Config["kind"] != "claude" || g.Config["model"] != "opus" || fmt.Sprint(g.Config["args"]) != "[--permission-mode auto]" {
		t.Fatalf("web-builders = %+v, %v", g, err)
	}
	if g, _ := a.st.GetGroup(ctx, "web-reviewers"); g == nil || g.Config["kind"] != "codex" || g.Config["model"] != nil ||
		fmt.Sprint(g.Config["args"]) != "[--dangerously-bypass-approvals-and-sandbox]" {
		t.Fatalf("web-reviewers = %+v", g)
	}
	if g, _ := a.st.GetGroup(ctx, "web-assembly"); g == nil || g.Config["args"] != nil {
		t.Fatalf("web-assembly, a script group, = %+v", g)
	}
	if g, _ := a.st.GetGroup(ctx, "backlog"); g == nil || len(g.Members) != 0 {
		t.Fatalf("backlog = %+v", g)
	}
	if g, _ := a.st.GetGroup(ctx, "decisions"); g == nil || strings.Join(g.Members, ",") != "ann,boss" {
		t.Fatalf("decisions = %+v", g)
	}
	boss, err := a.st.GetCrew(ctx, "boss")
	if err != nil || boss.Config["kind"] != "claude" || boss.Dir != a.cfg.Home || fmt.Sprint(boss.Config["args"]) != "[--permission-mode auto]" {
		t.Fatalf("boss = %+v, %v", boss, err)
	}

	head, _ := a.st.Head(ctx)
	out, err = run(t, a.packCmd(), args...)
	if err != nil || !strings.Contains(out, "pack starter is already applied to depot web: nothing changed\n") {
		t.Fatalf("second apply: %v\n%s", err, out)
	}
	if evs, _ := a.st.EventsSince(ctx, head, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" || evs[0].Actor != "me" {
		t.Fatalf("the second apply logged %+v", evs)
	}
	// And with no flags at all: the record names the crew member, and nothing
	// is left to ask or to confirm.
	if out, err = run(t, a.packCmd(), "apply", "../../packs/starter", "--depot", "web"); err != nil || !strings.Contains(out, "nothing changed") {
		t.Fatalf("apply without flags over an applied pack: %v\n%s", err, out)
	}

	out, err = run(t, a.packCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"starter in depot web  ", " by me\n", "  source     ", "  flow       default (stored as starter.default)\n",
		"builders -> web-builders", "  crew       yardmaster -> boss\n", "  objects    9 files, 5 groups, 1 flows, 1 flow pointers, 5 routes, 3 subscriptions\n"} {
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
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Result.Unchanged != 25 || got.Result.Recorded || got.Plan.Depot != "web" {
		t.Fatalf("json: %v\n%s", err, out)
	}
	out, err = run(t, a.packCmd(), "list")
	var recs []store.PackImport
	if err != nil || json.Unmarshal([]byte(out), &recs) != nil || len(recs) != 1 || recs[0].Crew["yardmaster"] != "boss" {
		t.Fatalf("json list: %v\n%s", err, out)
	}
}

// In a terminal pack apply asks: who takes the pack's crew member, the members
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
	args := []string{"apply", "../../packs/starter", "--depot", "web", "--bind", "builders=claude", "--bind", "reviewers=claude", "--bind", "crew:yardmaster=claude"}
	out, err := run(t, a.packCmd(), args...)
	if !errors.Is(err, errDeclined) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if groups, _ := a.st.Groups(ctx); len(groups) != 0 {
		t.Fatalf("groups after leaving the form: %v", groups)
	}
	// A select for the crew member, one for the permissions of each new agent
	// --bind named without a preset, an input per people group, the confirm.
	if len(asked) != 7 {
		t.Fatalf("asked %v", asked)
	}

	decline = false
	if out, err = run(t, a.packCmd(), args...); err != nil || !strings.Contains(out, "applied pack starter to depot web: 25 created") {
		t.Fatalf("%v\n%s", err, out)
	}
	// The select's default is a new crew member under the pack's name, and
	// the permissions' is the kind's unattended arguments.
	yardmaster, err := a.st.GetCrew(ctx, "yardmaster")
	if err != nil || fmt.Sprint(yardmaster.Config["args"]) != "[--permission-mode auto]" {
		t.Fatalf("yardmaster = %+v, %v", yardmaster, err)
	}
	if g, _ := a.st.GetGroup(ctx, "web-builders"); g == nil || fmt.Sprint(g.Config["args"]) != "[--permission-mode auto]" {
		t.Fatalf("web-builders = %+v", g)
	}
}

// A terminal offers the permissions the group form does, the kind's
// unattended arguments preselected, and what is picked is what the new
// group gets. A --bind with a preset is not asked about.
func TestPackApplyAsksPermissionsInATerminal(t *testing.T) {
	a := packApp(t)
	setInteractive(t, true)
	var titles []string
	stubAsk(t, func(fields ...huh.Field) error {
		for _, f := range fields {
			sel, ok := f.(*huh.Select[string])
			if !ok {
				continue
			}
			title := strings.SplitN(sel.View(), "\n", 2)[0]
			titles = append(titles, title)
			if strings.Contains(title, "Permissions for group web-builders") {
				if got := sel.GetValue(); got != "--permission-mode auto" {
					t.Errorf("preselected %q, want claude's unattended arguments", got)
				}
				// Down twice, past accept edits, to bypass.
				sel.WithKeyMap(huh.NewDefaultKeyMap())
				sel.Focus()
				sel.Update(tea.KeyMsg{Type: tea.KeyDown})
				sel.Update(tea.KeyMsg{Type: tea.KeyDown})
			}
		}
		return nil
	})
	args := []string{"apply", "../../packs/starter", "--depot", "web", "--crew", "yardmaster=boss", "--bind", "builders=claude",
		"--bind", "reviewers=codex,approve-for-me", "--bind", "crew:yardmaster=claude,none", "--members", "backlog=", "--members", "decisions="}
	out, err := run(t, a.packCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := strings.Count(strings.Join(titles, "\n"), "Permissions"); n != 1 {
		t.Fatalf("asked for permissions %d times, want once (web-builders): %q", n, titles)
	}
	ctx := context.Background()
	if g, _ := a.st.GetGroup(ctx, "web-builders"); g == nil || fmt.Sprint(g.Config["args"]) != "[--dangerously-skip-permissions]" {
		t.Fatalf("web-builders = %+v", g)
	}
	if g, _ := a.st.GetGroup(ctx, "web-reviewers"); g == nil || fmt.Sprint(g.Config["args"]) != "[--approve-for-me]" {
		t.Fatalf("web-reviewers = %+v", g)
	}
	// The open question is listed, the answered ones are not.
	if !strings.Contains(out, "an import would ask\n  group.web-builders.args: ") || strings.Contains(out, "\n  group.web-reviewers.") {
		t.Errorf("the plan should list web-builders' arguments alone as open:\n%s", out)
	}
}

// --bind may name a permission preset by its label; none is no arguments,
// and the import then says the agent stops at prompts. A kind yardr knows
// no unattended arguments for gets none and the same warning. An existing
// group keeps its arguments whatever the flags say.
func TestPackApplyPresets(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	ctx := context.Background()
	if err := a.st.PutGroup(ctx, store.Group{Name: "web-reviewers", Runner: "herdr", Limit: 1,
		Config: map[string]any{"kind": "claude", "args": []any{"--mine"}}}, "me"); err != nil {
		t.Fatal(err)
	}
	apply := func(binds ...string) (string, error) {
		args := []string{"apply", "../../packs/starter", "--depot", "web", "--yes", "--crew", "yardmaster=boss", "--members", "backlog=", "--members", "decisions="}
		for _, b := range binds {
			args = append(args, "--bind", b)
		}
		return run(t, a.packCmd(), args...)
	}

	_, err := apply("builders=claude:opus,yolo", "crew:yardmaster=pi")
	if err == nil || !strings.Contains(err.Error(), `claude has no permission preset "yolo" (it has: auto, accept-edits, bypass, none)`) {
		t.Fatalf("an unknown preset: %v", err)
	}
	if _, err := a.st.GetGroup(ctx, "web-builders"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unknown preset still made the group: %v", err)
	}

	out, err := apply("builders=claude:opus,Accept Edits", "reviewers=codex,bypass", "crew:yardmaster=pi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"  create           web-builders\n                     agent: claude\n                     model: opus\n                     arguments: --permission-mode acceptEdits\n",
		"                     arguments: none; its agent stops at every permission prompt\n",
		"crew member boss is new and has no agent arguments: an unattended agent stops at every permission prompt",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "group web-builders is new and has no agent arguments") || strings.Contains(out, "an import would ask") {
		t.Errorf("warns about a group that has arguments, or lists answered questions:\n%s", out)
	}
	if g, _ := a.st.GetGroup(ctx, "web-builders"); g == nil || fmt.Sprint(g.Config["args"]) != "[--permission-mode acceptEdits]" {
		t.Fatalf("web-builders = %+v", g)
	}
	if g, _ := a.st.GetGroup(ctx, "web-reviewers"); g == nil || g.Config["kind"] != "claude" || fmt.Sprint(g.Config["args"]) != "[--mine]" {
		t.Fatalf("web-reviewers, which the yard had, = %+v", g)
	}
	if r, _ := a.st.GetCrew(ctx, "boss"); r == nil || r.Config["kind"] != "pi" || r.Config["args"] != nil {
		t.Fatalf("boss = %+v", r)
	}

	// Again, with other presets: the groups are the yard's now and stay.
	head, _ := a.st.Head(ctx)
	out, err = apply("builders=claude,bypass", "reviewers=codex,bypass", "crew:yardmaster=claude")
	if err != nil || !strings.Contains(out, "nothing changed") {
		t.Fatalf("second apply: %v\n%s", err, out)
	}
	if evs, _ := a.st.EventsSince(ctx, head, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" {
		t.Fatalf("the second apply logged %+v", evs)
	}
	if g, _ := a.st.GetGroup(ctx, "web-builders"); g == nil || fmt.Sprint(g.Config["args"]) != "[--permission-mode acceptEdits]" {
		t.Fatalf("web-builders after a second apply = %+v", g)
	}

	// none is honoured too, and warned about.
	b := packApp(t)
	out, err = run(t, b.packCmd(), "apply", "../../packs/starter", "--depot", "web", "--yes", "--crew", "yardmaster=boss", "--members", "backlog=", "--members", "decisions=",
		"--bind", "builders=claude,none", "--bind", "reviewers=codex", "--bind", "crew:yardmaster=claude")
	if err != nil || !strings.Contains(out, "group web-builders is new and has no agent arguments") || strings.Contains(out, "group web-reviewers is new and has no") {
		t.Fatalf("none: %v\n%s", err, out)
	}
	if g, _ := b.st.GetGroup(ctx, "web-builders"); g == nil || g.Config["args"] != nil {
		t.Fatalf("web-builders with none = %+v", g)
	}
}

var errDeclined = errors.New("declined")

// A refused import prints the plan and why, exits 1, and changes nothing.
func TestPackApplyRefused(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	b, err := a.st.CreateBead(context.Background(), store.NewBead{Title: "stray", Depot: "web", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes"}, starterFlags...)...)
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
	args := append([]string{"apply", dir, "--depot", "web", "--yes"}, starterFlags...)
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
	const sub = "boss yardr all depot=web stage=decide"
	out, err := run(t, a.packCmd(), "check", dir, "--depot", "web")
	if err != nil || !strings.Contains(out, "  no longer in it  "+sub+" (an earlier import brought it; left alone unless --prune)\n") ||
		!strings.Contains(out, ", 1 no longer in it\n") {
		t.Fatalf("check: %v\n%s", err, out)
	}
	out, err = run(t, a.packCmd(), "apply", dir, "--depot", "web")
	if err != nil || !strings.Contains(out, "no longer in the pack and left in the yard (yardr pack apply … --prune removes them):\n  subscription "+sub+"\n") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	// A prune writes, so it needs --yes like any other change.
	if _, err = run(t, a.packCmd(), "apply", dir, "--depot", "web", "--prune"); err == nil || !strings.Contains(err.Error(), "give --yes") {
		t.Fatalf("prune without --yes: %v", err)
	}
	out, err = run(t, a.packCmd(), "apply", dir, "--depot", "web", "--prune", "--yes")
	if err != nil || !strings.Contains(out, "  pruned  subscription "+sub+"\n") {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	boss, _ := a.st.GetCrew(context.Background(), "boss")
	if len(boss.Subscriptions) != 2 {
		t.Fatalf("boss subscriptions = %v", boss.Subscriptions)
	}
}

// The roles choice at the command: the plan names the depot's file a pack's
// role shadows, --depot-roles keeps the depot's own and pack list says which was
// chosen, a later apply without the flag repeats it, and --pack-roles, the
// default now, says so and undoes it.
func TestPackApplyRolesChoice(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	ctx := context.Background()
	depotDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(depotDir, ".yardr", "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depotDir, ".yardr", "roles", "builder.md"), []byte("the depot's own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.st.PutDepot(ctx, store.Depot{Name: "web", Path: depotDir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(a.cfg.Home, "packs", "starter", "roles", "builder.md")
	prompt := func() any {
		g, err := a.st.GetGroup(ctx, "web-builders")
		if err != nil {
			t.Fatal(err)
		}
		return g.Config["prompt"]
	}
	args := append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes"}, starterFlags...)

	out, err := run(t, a.packCmd(), append(args, "--depot-roles")...)
	if err != nil || !strings.Contains(out, "kept: prompt roles/builder.md: the depot has its own .yardr/roles/builder.md\n") || prompt() != "roles/builder.md" {
		t.Fatalf("--depot-roles: %v, prompt %v\n%s", err, prompt(), out)
	}
	if out, err = run(t, a.packCmd(), "list"); err != nil || !strings.Contains(out, "  roles      the depot's own where it has them (--depot-roles)\n") {
		t.Fatalf("pack list after --depot-roles: %v\n%s", err, out)
	}
	// No flag: the recorded choice, so nothing changes.
	if out, err = run(t, a.packCmd(), "apply", "../../packs/starter", "--depot", "web"); err != nil || !strings.Contains(out, "nothing changed") || prompt() != "roles/builder.md" {
		t.Fatalf("apply without flags after --depot-roles: %v, prompt %v\n%s", err, prompt(), out)
	}

	out, err = run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web", "--pack-roles")
	if err != nil || !strings.Contains(out, "  change           web-builders\n                     shadows .yardr/roles/builder.md\n                     prompt: roles/builder.md -> "+copied+"\n") {
		t.Fatalf("check --pack-roles: %v\n%s", err, out)
	}
	if out, err = run(t, a.packCmd(), append(args, "--pack-roles")...); err != nil || prompt() != copied {
		t.Fatalf("--pack-roles: %v, prompt %v\n%s", err, prompt(), out)
	}
	if out, err = run(t, a.packCmd(), "list"); err != nil || !strings.Contains(out, "  roles      the pack's\n") {
		t.Fatalf("pack list after --pack-roles: %v\n%s", err, out)
	}
	if out, err = run(t, a.packCmd(), "apply", "../../packs/starter", "--depot", "web"); err != nil || !strings.Contains(out, "nothing changed") || prompt() != copied {
		t.Fatalf("apply without flags after --pack-roles: %v, prompt %v\n%s", err, prompt(), out)
	}
	if _, err = run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web", "--pack-roles", "--depot-roles"); err == nil {
		t.Fatal("--pack-roles with --depot-roles was accepted")
	}
}

// A copy under the home that was edited by hand is marked in the plan and
// not overwritten on a plain apply: the error names the file and says where
// the edit belongs. --yes overwrites it.
func TestPackApplyRefusesAHandEditedCopy(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	args := append([]string{"apply", "../../packs/starter", "--depot", "web"}, starterFlags...)
	if out, err := run(t, a.packCmd(), append(args, "--yes")...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	copied := filepath.Join(a.cfg.Home, "packs", "starter", "roles", "builder.md")
	if err := os.WriteFile(copied, []byte("edited in the yard\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web")
	if err != nil || !strings.Contains(out, "edited by hand since the last import: overwritten or removed only with --yes; the edit belongs in the pack's source\n") {
		t.Fatalf("check over a hand edit: %v\n%s", err, out)
	}
	// Refused in a terminal too: a confirmation is not enough to lose an edit.
	for _, tty := range []bool{false, true} {
		setInteractive(t, tty)
		out, err = run(t, a.packCmd(), args...)
		if err == nil || !strings.Contains(err.Error(), copied+" was edited by hand") || !strings.Contains(err.Error(), "the edit belongs in the pack's source (") ||
			!strings.Contains(err.Error(), "--yes proceeds") {
			t.Fatalf("apply over a hand edit (terminal %v): %v\n%s", tty, err, out)
		}
		if text, _ := os.ReadFile(copied); string(text) != "edited in the yard\n" {
			t.Fatalf("the refused apply wrote %q", text)
		}
	}
	setInteractive(t, false)
	if out, err = run(t, a.packCmd(), append(args, "--yes")...); err != nil || !strings.Contains(out, "1 changed") {
		t.Fatalf("apply --yes over a hand edit: %v\n%s", err, out)
	}
	// The pack's text, with the yard's names where it has placeholders.
	want, _ := os.ReadFile("../../packs/starter/roles/builder.md")
	want = pack.Fill(want, map[string]string{"operator": defaultOperator(), "yardmaster": "boss"})
	if text, _ := os.ReadFile(copied); string(text) != string(want) || strings.Contains(string(text), "{{") {
		t.Fatalf("after --yes the copy is %q", text)
	}
}

// pack check refuses a pack whose role has a placeholder no import fills,
// naming the file and the line.
func TestPackCheckRefusesAnUnknownPlaceholder(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../packs/starter")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "scripts", "merge-local"), 0o755); err != nil {
		t.Fatal(err)
	}
	role := filepath.Join(dir, "roles", "reviewer.md")
	if err := os.WriteFile(role, []byte("# Reviewer\n\nAsk {{operator}}, or {{lead}}.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := packApp(t)
	_, err := run(t, a.packCmd(), "check", dir, "--depot", "web")
	if err == nil || exitCode(err) != 1 || !strings.Contains(err.Error(), role+":3: placeholder {{lead}}: an import fills {{operator}}, {{yardmaster}}") {
		t.Fatalf("check of a pack with an unknown placeholder: %v (exit %d)", err, exitCode(err))
	}
	if _, err := os.Stat(filepath.Join(a.cfg.Home, "packs")); err == nil {
		t.Fatal("the refused pack was installed")
	}
}

// The starter pack's group assembly was refinery until this release: a flag
// that names it so, as a script written then does, is still about that
// group, and a name the pack never had is refused as before.
func TestPackApplyReadsTheGroupsOldName(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	args := append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes", "--members", "refinery=ann", "--bind", "refinery=claude"}, starterFlags...)
	if out, err := run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if g, _ := a.st.GetGroup(context.Background(), "web-assembly"); g == nil || g.Runner != "exec" || len(g.Members) != 0 {
		t.Fatalf("web-assembly = %+v", g)
	}
	args = append([]string{"apply", "../../packs/starter", "--depot", "web", "--yes", "--members", "smelter=ann"}, starterFlags...)
	if _, err := run(t, a.packCmd(), args...); err == nil || !strings.Contains(err.Error(), "the pack has no group smelter (it has: assembly, ") {
		t.Fatalf("--members smelter=ann: %v", err)
	}
}

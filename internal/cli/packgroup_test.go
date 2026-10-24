package cli

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/store"
)

// sharedGroups gives the yard the groups every depot's routes use, as the
// yard that builds yardr has them.
func sharedGroups(t *testing.T, a *app) []store.Group {
	t.Helper()
	ctx := context.Background()
	for _, g := range []store.Group{
		{Name: "yardr-builders", Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "claude", "model": "opus", "prompt": "/roles/builder.md"}},
		{Name: "yardr-reviewers", Runner: "herdr", Limit: 3, Config: map[string]any{"kind": "codex", "prompt": "/roles/reviewer.md"}},
		{Name: "triage", Runner: "manual", Limit: 50, Members: []string{"ann"}},
	} {
		if err := a.st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	have, err := a.st.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return have
}

// pack check lists, for a group it would make, the question of which group
// of the yard takes it, with the flag; --group answers it, and the plan
// then takes the yard's group instead of creating the depot's.
func TestPackCheckGroupFlag(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	sharedGroups(t, a)
	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web", "--crew", "yardmaster=boss")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  create           web-builders\n                     asks: group.web-builders.group\n                     asks: group.web-builders.kind\n",
		"an import would ask\n  group.web-builders.group: which group of the yard takes the pack's builders (--group builders=<name>)? The yard has: yardr-builders, yardr-reviewers. [a new group web-builders]\n",
		"  group.backlog.group: which group of the yard takes the pack's backlog (--group backlog=<name>)? The yard has: triage. [a new group backlog]\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q:\n%s", want, out)
		}
	}

	out, err = run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web", "--crew", "yardmaster=boss",
		"--group", "builders=yardr-builders", "--group", "backlog=triage")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  takes            yardr-builders (the yard's, for the pack's builders: nothing of it would be changed)\n" +
			"                     kept: prompt /roles/builder.md: the group is the yard's, and the pack's role roles/builder.md is not set on it\n" +
			"                     kept: limit 4: the group is the yard's, and the pack's limit 5 is not set on it\n",
		"  takes            triage (the yard's, for the pack's backlog: nothing of it would be changed)\n",
		"  create           depot web stage new -> yardr-builders\n",
		"  create           depot web stage backlog -> triage\n",
		"0 not in the pack, 2 of the yard's taken\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"create           web-builders", "group.web-builders", "group.backlog", "group.yardr-builders"} {
		if strings.Contains(out, not) {
			t.Errorf("the plan has %q though a group of the yard takes it:\n%s", not, out)
		}
	}

	for flag, want := range map[string]string{
		"builders":        `--group "builders": want <pack's group>=<yard's group>`,
		"builders=":       `--group "builders=": want <pack's group>=<yard's group>`,
		"builders=triage": "--group builders=triage: the pack's builders is an agent group, and triage is a people group",
		"builders=nope":   "--group builders=nope: the yard has no group nope",
	} {
		if _, err := run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web", "--group", flag); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("--group %s: err = %v, want %q", flag, err, want)
		}
	}
}

// depot init --group sets a depot up on groups the yard has: no copy of
// them is made, nothing of them is changed, the depot's routes lead to
// them, and a second run with no flag at all takes them again. Without a
// terminal and without the flag a new group is the depot's own, as it was
// before an import could ask: the question is no missing answer.
func TestDepotInitTakesGroupsOfTheYard(t *testing.T) {
	t.Parallel()
	a, _ := packYard(t, nil)
	requireNonInteractive(t)
	ctx := context.Background()
	before := sharedGroups(t, a)
	args := []string{"init", "web", "--pack", "starter", "--yes", "--crew", "yardmaster=boss", "--bind", "crew:yardmaster=claude",
		"--group", "builders=yardr-builders", "--group", "reviewers=yardr-reviewers", "--group", "backlog=triage", "--members", "decisions="}
	out, err := run(t, a.depotCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"  takes            yardr-builders (the yard's, for the pack's builders", "\napplied pack starter to depot web: 35 created, 0 changed, 0 unchanged\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	after, _ := a.st.Groups(ctx)
	var names []string
	for _, g := range after {
		names = append(names, g.Name)
		if i := slices.IndexFunc(before, func(h store.Group) bool { return h.Name == g.Name }); i >= 0 && !reflect.DeepEqual(before[i], g) {
			t.Errorf("group %s changed: %+v, was %+v", g.Name, g, before[i])
		}
	}
	slices.Sort(names)
	// The second review's group is the depot's own, and runs the agent of
	// the yard's group that takes the pack's reviewers.
	if want := []string{"decisions", "triage", "web-assembly", "web-security-reviewers", "yardr-builders", "yardr-reviewers"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("groups = %v, want %v", names, want)
	}
	if g, err := a.st.GetGroup(ctx, "web-security-reviewers"); err != nil || g.Config["kind"] != "codex" {
		t.Fatalf("web-security-reviewers = %+v, %v, want the agent of yardr-reviewers", g, err)
	}
	routes, _ := a.st.Routes(ctx)
	to := map[string]string{}
	for _, r := range routes {
		if r.Type == nil {
			to[r.Stage] = r.Group
		}
	}
	if to["new"] != "yardr-builders" || to["review"] != "yardr-reviewers" || to["backlog"] != "triage" || to["approved"] != "web-assembly" {
		t.Fatalf("routes lead to %v", to)
	}

	// As at the last import.
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter", "--yes")
	if err != nil || !strings.Contains(out, "pack starter is already applied to depot web: nothing changed") || !strings.Contains(out, "  takes            yardr-reviewers") {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = run(t, a.packCmd(), "list")
	if err != nil || !strings.Contains(out, "builders -> yardr-builders") || !strings.Contains(out, "3 groups,") {
		t.Fatalf("%v\n%s", err, out)
	}

	// Another depot, with the flags an import took before --group was: the
	// yard's agent groups could take its builders, and nothing asks.
	if err := a.st.PutDepot(ctx, store.Depot{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.packCmd(), "apply", "starter", "--depot", "api", "--yes", "--crew", "yardmaster=boss", "--bind", "builders=claude", "--bind", "reviewers=codex", "--members", "backlog=")
	if err != nil || !strings.Contains(out, "  create           api-builders\n                     agent: claude\n") {
		t.Fatalf("%v\n%s", err, out)
	}
	if g, err := a.st.GetGroup(ctx, "api-builders"); err != nil || g.Config["kind"] != "claude" {
		t.Fatalf("api-builders = %+v, %v", g, err)
	}
	if g, err := a.st.GetGroup(ctx, "api-security-reviewers"); err != nil || g.Config["kind"] != "codex" {
		t.Fatalf("api-security-reviewers = %+v, %v, want the agent of api-reviewers, asked nothing", g, err)
	}
}

// In a terminal the import asks which group takes each new group the yard
// has one of the kind for, beside the bind: a group of the yard that is
// picked takes it and is asked nothing more, and a group a --bind or
// --members names is new without the question.
func TestPackApplyAsksWhichGroupTakes(t *testing.T) {
	a := packApp(t)
	setInteractive(t, true)
	ctx := context.Background()
	before := sharedGroups(t, a)
	var titles []string
	stubAsk(t, func(fields ...huh.Field) error {
		for _, f := range fields {
			title := strings.SplitN(f.View(), "\n", 2)[0]
			titles = append(titles, title)
			sel, ok := f.(*huh.Select[string])
			if !ok {
				continue
			}
			switch {
			case strings.Contains(title, "Which group takes the pack's builders?"):
				if got := sel.GetValue(); got != "web-builders" {
					t.Errorf("preselected %q, want the new group", got)
				}
				if view := sel.View(); !strings.Contains(view, "yardr-builders  (claude · opus · limit 4)") || !strings.Contains(view, "yardr-reviewers") || strings.Contains(view, "triage") {
					t.Errorf("the builders are offered:\n%s", view)
				}
				// Down once, from the new group to the first of the yard's.
				sel.WithKeyMap(huh.NewDefaultKeyMap())
				sel.Focus()
				sel.Update(tea.KeyMsg{Type: tea.KeyDown})
			case strings.Contains(title, "Which group takes the pack's backlog?"):
				if view := sel.View(); !strings.Contains(view, "triage  (ann · limit 50)") || strings.Contains(view, "yardr-builders") {
					t.Errorf("the backlog is offered:\n%s", view)
				}
			}
		}
		return nil
	})
	out, err := run(t, a.packCmd(), "apply", "../../packs/starter", "--depot", "web", "--crew", "yardmaster=boss", "--bind", "crew:yardmaster=claude,none",
		"--bind", "reviewers=codex,none", "--members", "decisions=")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	asked := func(what string) bool {
		return slices.ContainsFunc(titles, func(s string) bool { return strings.Contains(s, what) })
	}
	for _, want := range []string{"Which group takes the pack's builders?", "Which group takes the pack's backlog?", "Members of group backlog"} {
		if !asked(want) {
			t.Errorf("not asked %q: %v", want, titles)
		}
	}
	for _, not := range []string{"the pack's reviewers", "the pack's decisions", "the pack's assembly", "web-builders", "yardr-builders"} {
		if asked(not) {
			t.Errorf("asked about %q: %v", not, titles)
		}
	}
	if !strings.Contains(out, "  takes            yardr-builders (") {
		t.Errorf("the plan does not take yardr-builders:\n%s", out)
	}
	after, _ := a.st.Groups(ctx)
	if i := slices.IndexFunc(after, func(g store.Group) bool { return g.Name == "yardr-builders" }); i < 0 || !reflect.DeepEqual(after[i], before[slices.IndexFunc(before, func(g store.Group) bool { return g.Name == "yardr-builders" })]) {
		t.Errorf("yardr-builders changed: %+v", after)
	}
	for _, name := range []string{"web-reviewers", "backlog"} {
		if _, err := a.st.GetGroup(ctx, name); err != nil {
			t.Errorf("group %s: %v", name, err)
		}
	}
	if _, err := a.st.GetGroup(ctx, "web-builders"); err == nil {
		t.Error("web-builders was made though yardr-builders took the pack's builders")
	}
}

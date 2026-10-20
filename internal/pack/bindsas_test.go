package pack_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// bindsPack is a pack whose second review is run by the agent the first is,
// unless the import is told otherwise.
const bindsPack = `name: bind
flows:
  default:
    stages:
      new:    { group: builders, outcomes: { done: review } }
      review: { group: reviewers, outcomes: { done: second, changes: new } }
      second: { group: security, outcomes: { done: merged, changes: new } }
      merged: { end: true }
groups:
  builders:  { agent: roles/builder.md, limit: 2 }
  reviewers: { agent: roles/reviewer.md, limit: 3 }
  security:  { agent: roles/security.md, limit: 1, binds_as: reviewers }
`

func bindsFiles(yaml string) map[string]string {
	return map[string]string{"pack.yaml": yaml, "roles/builder.md": "build\n", "roles/reviewer.md": "review\n", "roles/security.md": "secure\n"}
}

func questionIDs(p *pack.Plan) []string {
	var ids []string
	for _, q := range p.Questions {
		ids = append(ids, q.ID)
	}
	return ids
}

func groupOf(t *testing.T, st store.API, name string) store.Group {
	t.Helper()
	groups, err := st.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == name })
	if i < 0 {
		t.Fatalf("the yard has no group %s", name)
	}
	return groups[i]
}

// agentOf is what a group runs its agent with.
func agentOf(g store.Group) []any {
	return []any{g.Config["kind"], g.Config["model"], g.Config["args"]}
}

// A group that binds as another is asked nothing: the import gives it the
// agent, model and arguments it binds the other with.
func TestGroupBindsAsAnother(t *testing.T) {
	st, o := yard(t)
	plan := build(t, st, load(t, writePack(t, bindsFiles(bindsPack))), o)
	wantQ := []string{
		"group.web-builders.kind", "group.web-builders.model", "group.web-builders.args",
		"group.web-reviewers.kind", "group.web-reviewers.model", "group.web-reviewers.args",
	}
	if got := questionIDs(plan); !reflect.DeepEqual(got, wantQ) {
		t.Fatalf("questions = %v, want %v: nothing is asked of web-security", got, wantQ)
	}
	sec := find(t, plan, "group", "web-security")
	if sec.Binds != "web-reviewers" || len(sec.Asks) != 0 || sec.Action != pack.Create {
		t.Fatalf("web-security = %+v, want a create that binds as web-reviewers and asks nothing", sec)
	}
	if got := find(t, plan, "group", "web-reviewers"); got.Binds != "" {
		t.Fatalf("web-reviewers binds as %s", got.Binds)
	}

	apply(t, st, plan, map[string]string{
		"group.web-reviewers.kind": "codex", "group.web-reviewers.model": "luna", "group.web-reviewers.args": "--auto --fast",
	})
	want := []any{"codex", "luna", []any{"--auto", "--fast"}}
	for _, name := range []string{"web-reviewers", "web-security"} {
		if got := agentOf(groupOf(t, st, name)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s runs %v, want %v", name, got, want)
		}
	}
	// What is the group's own stays: its role and its limit.
	if g := groupOf(t, st, "web-security"); g.Limit != 1 || !strings.HasSuffix(g.Config["prompt"].(string), "roles/security.md") {
		t.Fatalf("web-security = %+v", g)
	}

	// Applied again, nothing changes, and nothing is asked.
	again := build(t, st, load(t, writePack(t, bindsFiles(bindsPack))), o)
	if len(again.Questions) != 0 || len(actions(again, pack.Create)) != 0 || len(actions(again, pack.Change)) != 0 {
		t.Fatalf("a second import: questions %v, create %v, change %v", questionIDs(again), actions(again, pack.Create), actions(again, pack.Change))
	}
}

// A flag that binds the group itself is answered as for any new group: the
// group does not take the other's agent.
func TestGroupBindsAsIsAskedWhereBound(t *testing.T) {
	st, o := yard(t)
	o.Bound = []string{"security"}
	plan := build(t, st, load(t, writePack(t, bindsFiles(bindsPack))), o)
	if got := find(t, plan, "group", "web-security"); got.Binds != "" || !reflect.DeepEqual(got.Asks, []string{"group.web-security.kind", "group.web-security.model", "group.web-security.args"}) {
		t.Fatalf("web-security = %+v, want its own three questions", got)
	}
	apply(t, st, plan, map[string]string{
		"group.web-reviewers.kind": "codex", "group.web-reviewers.model": "luna",
		"group.web-security.kind": "claude", "group.web-security.model": "opus",
	})
	if got, want := agentOf(groupOf(t, st, "web-security")), []any{"claude", "opus", nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("web-security runs %v, want %v", got, want)
	}
	if got, want := agentOf(groupOf(t, st, "web-reviewers")), []any{"codex", "luna", nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("web-reviewers runs %v, want %v", got, want)
	}
}

// Where the yard has the group already, a new group that binds as it gets
// its agent, model and arguments as they stand, which is how a yard set up
// before the pack had the second group is brought to it, asked nothing.
func TestGroupBindsAsAGroupTheYardHas(t *testing.T) {
	st, o := yard(t)
	if err := st.PutGroup(ctx, store.Group{Name: "web-reviewers", Runner: "herdr", Limit: 3,
		Config: map[string]any{"kind": "codex", "model": "luna", "args": []any{"--auto"}, "prompt": "/somewhere/reviewer.md", "env_file": "/keep/me"}}, "me"); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, writePack(t, bindsFiles(bindsPack))), o)
	for _, id := range questionIDs(plan) {
		if strings.Contains(id, "web-security") || strings.Contains(id, "web-reviewers") {
			t.Fatalf("the import asks %s", id)
		}
	}
	sec := find(t, plan, "group", "web-security")
	if sec.Binds != "web-reviewers" || len(sec.Asks) != 0 {
		t.Fatalf("web-security = %+v", sec)
	}
	apply(t, st, plan, nil)
	g := groupOf(t, st, "web-security")
	if got, want := agentOf(g), []any{"codex", "luna", []any{"--auto"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("web-security runs %v, want %v", got, want)
	}
	// Only the agent is taken: not the other group's credentials or role.
	if _, ok := g.Config["env_file"]; ok || g.Config["prompt"] == "/somewhere/reviewer.md" {
		t.Fatalf("web-security took more than the agent: %+v", g.Config)
	}

	// A group of the yard that takes the pack's reviewers is the one it
	// binds as.
	if err := st.PutGroup(ctx, store.Group{Name: "mine", Runner: "herdr", Limit: 1, Config: map[string]any{"kind": "claude", "model": "sonnet"}}, "me"); err != nil {
		t.Fatal(err)
	}
	o.Groups = map[string]string{"reviewers": "mine"}
	o.Depot = "web"
	if err := st.PutDepot(ctx, store.Depot{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	o.Depot = "api"
	plan = build(t, st, load(t, writePack(t, bindsFiles(bindsPack))), o)
	apply(t, st, plan, map[string]string{"group.api-builders.kind": "claude"})
	if got, want := agentOf(groupOf(t, st, "api-security")), []any{"claude", "sonnet", nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("api-security runs %v, want %v, those of mine, which takes the pack's reviewers", got, want)
	}
}

// A group the yard already has under the name is the yard's: nothing is
// taken for it, and nothing asked.
func TestGroupBindsAsLeavesAnExistingGroup(t *testing.T) {
	st, o := yard(t)
	if err := st.PutGroup(ctx, store.Group{Name: "web-security", Runner: "herdr", Limit: 1, Config: map[string]any{"kind": "gemini"}}, "me"); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, writePack(t, bindsFiles(bindsPack))), o)
	if sec := find(t, plan, "group", "web-security"); sec.Binds != "" || len(sec.Asks) != 0 {
		t.Fatalf("web-security = %+v, want the yard's own, asked nothing", sec)
	}
	apply(t, st, plan, nil)
	if got := groupOf(t, st, "web-security").Config["kind"]; got != "gemini" {
		t.Fatalf("web-security runs %v, want gemini", got)
	}
}

// A group it cannot take an agent from is asked about as any new group is:
// the yard's group of that name is no agent.
func TestGroupBindsAsAGroupThatRunsNoAgent(t *testing.T) {
	st, o := yard(t)
	if err := st.PutGroup(ctx, store.Group{Name: "web-reviewers", Runner: "manual", Limit: 1}, "me"); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, writePack(t, bindsFiles(bindsPack))), o)
	if sec := find(t, plan, "group", "web-security"); sec.Binds != "" || len(sec.Asks) != 3 {
		t.Fatalf("web-security = %+v, want its own questions", sec)
	}
}

// A group binds as one before it, in the pack's order, that runs an agent.
func TestBindsAsIsHeldToTheGroupsOfThePack(t *testing.T) {
	for name, c := range map[string]struct{ groups, want string }{
		"no such group": {`
  security:  { agent: roles/security.md, binds_as: nobody }
`, "the pack has no group nobody (it has: security)"},
		"itself": {`
  security:  { agent: roles/security.md, binds_as: security }
`, "group security is not declared before security"},
		"a later group": {`
  security:  { agent: roles/security.md, binds_as: reviewers }
  reviewers: { agent: roles/reviewer.md }
`, "group reviewers is not declared before security"},
		"a script group": {`
  reviewers: { run: scripts/land }
  security:  { agent: roles/security.md, binds_as: reviewers }
`, "group reviewers is not an agent group"},
		"from a script group": {`
  reviewers: { agent: roles/reviewer.md }
  security:  { run: scripts/land, binds_as: reviewers }
`, "a group binds as another only with agent:"},
		"a people group": {`
  reviewers: { agent: roles/reviewer.md }
  security:  { people: true, binds_as: reviewers }
`, "a group binds as another only with agent:"},
		"not a name": {`
  reviewers: { agent: roles/reviewer.md }
  security:  { agent: roles/security.md, binds_as: [reviewers] }
`, "want a string"},
	} {
		t.Run(name, func(t *testing.T) {
			yaml := `name: bind
flows:
  default:
    stages:
      new:    { group: security, outcomes: { done: merged } }
      merged: { end: true }
groups:` + c.groups
			files := bindsFiles(yaml)
			files["scripts/land"] = "#!/bin/sh\n"
			_, err := pack.LoadDir(writePack(t, files))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error with %q", err, c.want)
			}
		})
	}
}

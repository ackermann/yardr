package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func group(t *testing.T, st store.API, name string) *store.Group {
	t.Helper()
	g, err := st.GetGroup(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// group set changes the settings named and keeps the rest (yardr-9zv7):
// group add writes the whole group, so one key changed with it cost the
// group every --set that was not retyped.
func TestGroupSetKeepsTheRest(t *testing.T) {
	st := userYard(t)
	mustAs(t, st, "jo", "group", "add", "agents", "--runner", "herdr", "--limit", "2", "--set", "kind=claude",
		"--set", "model=opus", "--set", `args=["--permission-mode","auto"]`, "--set", "idle=10m")

	out := mustAs(t, st, "jo", "group", "set", "agents", "model=sonnet")
	if out != "group agents saved\n  model: opus -> sonnet\n" {
		t.Fatalf("set of one key printed %q", out)
	}
	g := group(t, st, "agents")
	want := map[string]any{"kind": "claude", "model": "sonnet", "args": []any{"--permission-mode", "auto"}, "idle": "10m"}
	if !reflect.DeepEqual(g.Config, want) || g.Limit != 2 || g.Runner != "herdr" {
		t.Fatalf("after set model: %+v", g)
	}

	out = mustAs(t, st, "jo", "group", "set", "agents", "--limit", "3", "--unset", "idle", "env_file=/srv/agents.env", "stall=1h")
	if out != "group agents saved\n  --limit: 2 -> 3\n  env_file: (not set) -> /srv/agents.env\n  idle: 10m -> (dropped)\n  stall: (not set) -> 1h\n" {
		t.Fatalf("set of several printed %q", out)
	}
	g = group(t, st, "agents")
	delete(want, "idle")
	want["env_file"], want["stall"] = "/srv/agents.env", "1h"
	if !reflect.DeepEqual(g.Config, want) || g.Limit != 3 {
		t.Fatalf("after the second set: %+v", g)
	}

	// Set so already: said, and neither written nor logged.
	ctx := context.Background()
	before, err := st.EventsOfKind(ctx, "group_set", 50)
	if err != nil {
		t.Fatal(err)
	}
	if out := mustAs(t, st, "jo", "group", "set", "agents", "model=sonnet", "--limit", "3", `args=["--permission-mode","auto"]`); out != "group agents unchanged: it is set so already\n" {
		t.Fatalf("a set that changes nothing printed %q", out)
	}
	if after, err := st.EventsOfKind(ctx, "group_set", 50); err != nil || len(after) != len(before) {
		t.Fatalf("a set that changes nothing logged group_set: %d, then %d (%v)", len(before), len(after), err)
	}

	// --json is the group as saved.
	var saved store.Group
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "group", "set", "agents", "--limit", "1", "--json")), &saved); err != nil || saved.Limit != 1 || saved.Config["model"] != "sonnet" {
		t.Fatalf("set --json = %+v, %v", saved, err)
	}

	// group add on the same name still replaces the group.
	mustAs(t, st, "jo", "group", "add", "agents", "--runner", "herdr", "--set", "kind=claude")
	if g := group(t, st, "agents"); !reflect.DeepEqual(g.Config, map[string]any{"kind": "claude"}) || g.Limit != 1 {
		t.Fatalf("group add kept what it was not given: %+v", g)
	}
}

// --members replaces the list of a manual group and is refused on another,
// as group add refuses it; a group's subscriptions are kept.
func TestGroupSetMembers(t *testing.T) {
	st := userYard(t)
	mustAs(t, st, "jo", "group", "add", "approvers", "--runner", "manual", "--members", "jo", "--set", "notify=true")
	if out := mustAs(t, st, "jo", "group", "set", "approvers", "--members", "jo,ada"); out != "group approvers saved\n  --members: jo -> jo,ada\n" {
		t.Fatalf("set --members printed %q", out)
	}
	if out := mustAs(t, st, "jo", "group", "set", "approvers", "--members", ""); out != "group approvers saved\n  --members: jo,ada -> (dropped)\n" {
		t.Fatalf("set --members '' printed %q", out)
	}
	if g := group(t, st, "approvers"); len(g.Members) != 0 || g.Config["notify"] != true {
		t.Fatalf("after --members '': %+v", g)
	}
	mustAs(t, st, "jo", "group", "add", "tick", "--runner", "exec", "--set", `cmd="true"`)
	refusedAs(t, st, "jo", "members are only for manual groups", "group", "set", "tick", "--members", "jo")
}

func TestGroupSetRefuses(t *testing.T) {
	st := userYard(t)
	mustAs(t, st, "jo", "group", "add", "tick", "--runner", "exec", "--limit", "2", "--set", `cmd="true"`, "--set", "dir=/srv")
	was := group(t, st, "tick")
	for want, args := range map[string][]string{
		"nothing to set: give key=value, --unset <key>, --limit or --members": {"tick"},
		`"dir": want key=value (--unset <key> removes a setting)`:             {"tick", "dir"},
		"dir is both set and unset":                                           {"tick", "dir=/x", "--unset", "dir"},
		"--unset log: tick has no such setting (it has: cmd, dir)":            {"tick", "--unset", "log"},
		"group tick: a limit is at least 1":                                   {"tick", "--limit", "0"},
		"group nobody: not found":                                             {"nobody", "dir=/x"},
		// The runner is asked of the group as it would be, and the hint is
		// this command's: the key bare.
		`group tick: exec runner needs config.cmd as a string, got the JSON boolean true; write 'cmd="true"'`: {"tick", "cmd=true"},
		"group tick: exec runner needs config.cmd":                                                            {"tick", "--unset", "cmd"},
	} {
		refusedAs(t, st, "jo", want, append([]string{"group", "set"}, args...)...)
	}
	if g := group(t, st, "tick"); !reflect.DeepEqual(g, was) {
		t.Fatalf("a refused group set changed the group:\n got %+v\nwant %+v", g, was)
	}
	// A key the exec group keeps is not asked for again.
	mustAs(t, st, "jo", "group", "set", "tick", "dir=/srv/b")
}

// group show prints one group: what group list has of it, a config key to a
// line, and the routes that send it work.
func TestGroupShow(t *testing.T) {
	// The key named like a secret is one no code reads, so it is from a
	// yard set up before the store refused such a key.
	path := filepath.Join(t.TempDir(), "yard.db")
	old, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	userYardOn(t, old)
	mustAs(t, old, "jo", "group", "add", "agents", "--runner", "herdr", "--limit", "2", "--set", "kind=claude",
		"--set", "model=opus", "--set", `args=["--permission-mode","auto"]`)
	if err := old.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	storetest.OldConfig(t, path, "groups", "agents", "api_token", "s3cret")
	st := storetest.Open(t, path)
	mustAs(t, st, "jo", "route", "add", "--stage", "new", "--group", "agents", "--type", "bug")
	out := mustAs(t, st, "jo", "group", "show", "agents")
	for _, want := range []string{"agents  herdr · claude · opus · limit 2\n", "\n  api_token=(a value, not shown)\n  args=[\"--permission-mode\",\"auto\"]\n  kind=claude\n  model=opus\n",
		": stage new · type bug · priority "} {
		if !strings.Contains(out, want) {
			t.Fatalf("group show has no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cret") {
		t.Fatalf("group show printed a secret config value:\n%s", out)
	}
	var shown struct {
		Group   store.Group
		Routes  []store.Route
		Harness struct {
			Kind   string
			Always []string
		}
	}
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "group", "show", "agents", "--json")), &shown); err != nil ||
		shown.Group.Config["model"] != "opus" || len(shown.Routes) != 1 || shown.Routes[0].Stage != "new" {
		t.Fatalf("group show --json = %+v, %v", shown, err)
	}
	mustAs(t, st, "jo", "group", "add", "codex", "--runner", "herdr", "--set", "kind=codex")
	codex := mustAs(t, st, "jo", "group", "show", "codex")
	if !strings.Contains(codex, "harness codex always: --no-daemon --config=check_for_update_on_startup=false") {
		t.Fatalf("group show omitted Codex's always flags:\n%s", codex)
	}
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "group", "show", "codex", "--json")), &shown); err != nil ||
		shown.Harness.Kind != "codex" || !reflect.DeepEqual(shown.Harness.Always, []string{"--no-daemon", "--config=check_for_update_on_startup=false"}) {
		t.Fatalf("group show --json harness = %+v, %v", shown.Harness, err)
	}
	refusedAs(t, st, "jo", "group nobody: not found", "group", "show", "nobody")
}

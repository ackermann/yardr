package cli

import (
	"context"
	"strings"
	"testing"
)

// A --set value is read as JSON first, so cmd=true is the boolean, which the
// exec runner takes as no command: the group was stored and every session
// of it failed. group add asks the runner before it stores anything, and
// says how the string is written.
func TestGroupAddRefusesASettingTheRunnerCannotRead(t *testing.T) {
	st := userYard(t)
	for _, c := range []struct {
		want string
		args []string
	}{
		{`group tick: exec runner needs config.cmd as a string, got the JSON boolean true; write --set 'cmd="true"'`,
			[]string{"--runner", "exec", "--set", "cmd=true"}},
		{`needs config.cmd as a string, got the JSON null; write --set 'cmd="null"'`,
			[]string{"--runner", "exec", "--set", "cmd=null"}},
		{`needs config.cmd as a string, got the JSON number 3; write --set 'cmd="3"'`,
			[]string{"--runner", "exec", "--set", "cmd=3"}},
		{`needs config.cmd as a string, got the JSON list ["make","merge"]; write --set 'cmd="..."'`,
			[]string{"--runner", "exec", "--set", `cmd=["make","merge"]`}},
		{"group tick: exec runner needs config.cmd", []string{"--runner", "exec"}},
		{"group tick: exec runner needs config.cmd", []string{"--runner", "exec", "--set", "cmd="}},
		{`group tick: herdr runner needs config.prompt as a string, got the JSON boolean false; write --set 'prompt="false"'`,
			[]string{"--runner", "herdr", "--set", "prompt=false"}},
		{`group tick: herdr runner needs config.env_command as a string or a list, got the JSON boolean true; write --set 'env_command="true"'`,
			[]string{"--runner", "herdr", "--set", "env_command=true"}},
		{`herdr runner needs config.kind as a string, got the JSON null`,
			[]string{"--runner", "herdr", "--set", "kind=null"}},
	} {
		refusedAs(t, st, "jo", c.want, append([]string{"group", "add", "tick"}, c.args...)...)
	}
	if gs, err := st.Groups(context.Background()); err != nil {
		t.Fatal(err)
	} else {
		for _, g := range gs {
			if g.Name == "tick" {
				t.Fatalf("a refused group was stored: %+v", g)
			}
		}
	}
	if out := mustAs(t, st, "jo", "group", "list"); strings.Contains(out, "tick") {
		t.Fatalf("group list shows a refused group:\n%s", out)
	}

	// The JSON string is the command, and a list of words is still one.
	mustAs(t, st, "jo", "group", "add", "tick", "--runner", "exec", "--set", `cmd="true"`)
	mustAs(t, st, "jo", "group", "add", "agents", "--runner", "herdr", "--set", "prompt=roles/builder.md",
		"--set", `args=["--model","opus"]`, "--set", "env_command=op inject -i env.tpl")
	g, err := st.GetGroup(context.Background(), "tick")
	if err != nil || g.Config["cmd"] != "true" {
		t.Fatalf("tick = %+v, %v; want the string true as its cmd", g, err)
	}
}

// group list prints a config as --set takes it back, so the command "true"
// and the boolean true, which both printed as cmd=true, can be told apart.
func TestGroupListPrintsConfigAsSetTakesIt(t *testing.T) {
	st := userYard(t)
	mustAs(t, st, "jo", "group", "add", "tick", "--runner", "exec", "--set", `cmd="true"`, "--set", "dir=/srv/a b")
	mustAs(t, st, "jo", "group", "add", "humans", "--runner", "manual", "--set", "notify=true", "--set", "n=3",
		"--set", `words=["a & b","c"]`)
	out := strings.Join(strings.Fields(mustAs(t, st, "jo", "group", "list")), " ")
	for _, want := range []string{`cmd="true" dir=/srv/a b`, `n=3 notify=true words=["a & b","c"]`} {
		if !strings.Contains(out, want) {
			t.Fatalf("group list has no %q:\n%s", want, out)
		}
	}
	// What it printed is what --set reads to the same config.
	if got := fmtSets(map[string]any{"cmd": "true", "kind": "claude", "model": "5", "keep": true}); got != `cmd="true" keep=true kind=claude model="5"` {
		t.Fatalf("fmtSets = %s", got)
	}
}

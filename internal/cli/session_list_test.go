package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// session list --running leaves out a manual group's claims, which are
// beads waiting for a person; without the flag they are listed as before
// (yardr-jcw1: 35 of 38 rows were backlog beads).
func TestSessionListRunning(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	rig := gitRig(t, st)
	commitRig(t, rig)
	beads := workYard(t, st, rig.Name, []store.Group{
		{Name: "builders", Runner: "herdr", Limit: 1},
		{Name: "refinery", Runner: "exec", Limit: 1},
		{Name: "backlog", Runner: "manual", Limit: 1},
	}, "builders", "refinery", "backlog")

	// The beads listed, in text and in JSON.
	list := func(args ...string) (text, js []string) {
		t.Helper()
		out, err := run(t, (&app{st: st}).rootCmd(), append([]string{"session", "list"}, args...)...)
		if err != nil {
			t.Fatalf("session list %v: %v\n%s", args, err, out)
		}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
			text = append(text, strings.Fields(l)[1])
		}
		out, err = run(t, (&app{st: st}).rootCmd(), append([]string{"session", "list", "--json"}, args...)...)
		var ss []store.Session
		if jerr := json.Unmarshal([]byte(out), &ss); err != nil || jerr != nil {
			t.Fatalf("session list --json %v: %v %v\n%s", args, err, jerr, out)
		}
		for _, s := range ss {
			js = append(js, s.Bead)
		}
		slices.Sort(text)
		slices.Sort(js)
		return text, js
	}
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }

	all := sorted(beads...)
	if text, js := list(); !slices.Equal(text, all) || !slices.Equal(js, all) {
		t.Errorf("session list = %v, --json %v, want %v", text, js, all)
	}
	running := sorted(beads[0], beads[1])
	if text, js := list("--running"); !slices.Equal(text, running) || !slices.Equal(js, running) {
		t.Errorf("session list --running = %v, --json %v, want %v", text, js, running)
	}
}

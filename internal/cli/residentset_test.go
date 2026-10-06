package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/store"
)

// clerkYard is userYard with a resident clerk whose definition has what the
// yardmaster's had when a bare resident add wiped it (yardr-q5hf): a model,
// args, an env_file, a dir and a max-age.
func clerkYard(t *testing.T) (store.API, *store.Resident) {
	t.Helper()
	st := userYard(t)
	env := filepath.Join(t.TempDir(), "clerk.env")
	if err := os.WriteFile(env, []byte("LINEAR_API_KEY=lin_api_clerk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "resident", "add", "clerk", "--set", "kind=claude", "--set", "model=opus",
		"--set", `args=["--permission-mode","auto"]`, "--set", "env_file="+env, "--dir", t.TempDir(), "--max-age", "6h")
	return st, resident(t, st, "clerk")
}

func resident(t *testing.T, st store.API, name string) *store.Resident {
	t.Helper()
	r, err := st.GetResident(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// sameDefinition fails unless got is defined as want is, but for the config
// keys named.
func sameDefinition(t *testing.T, got, want *store.Resident, but ...string) {
	t.Helper()
	g, w := map[string]any{}, map[string]any{}
	for k, v := range got.Config {
		g[k] = v
	}
	for k, v := range want.Config {
		w[k] = v
	}
	for _, k := range but {
		delete(g, k)
		delete(w, k)
	}
	if !reflect.DeepEqual(g, w) || got.Runner != want.Runner || got.Dir != want.Dir || !reflect.DeepEqual(got.On, want.On) ||
		got.MaxAge != want.MaxAge || got.HandoffGrace != want.HandoffGrace {
		t.Fatalf("the definition is not as it was:\n got %+v\nwant %+v", got, want)
	}
}

// resident set changes the settings named and keeps the rest (yardr-q5hf:
// granting one permission took resident add with the whole definition
// retyped), and says what it changed.
func TestResidentSetKeepsTheRest(t *testing.T) {
	st, was := clerkYard(t)

	out := mustAs(t, st, "jo", "resident", "set", "clerk", "set_state=linear")
	if out != "resident clerk saved\n  set_state: (not set) -> linear\n" {
		t.Fatalf("set set_state: %q", out)
	}
	r := resident(t, st, "clerk")
	sameDefinition(t, r, was, "set_state")
	if !reflect.DeepEqual(r.MaySetState(), []string{"linear"}) || r.Journal != was.Journal {
		t.Fatalf("after the grant: %+v", r)
	}

	// Several at once, a flag among them; a list is a list in the store.
	out = mustAs(t, st, "jo", "resident", "set", "clerk", "model=sonnet", `args=["--verbose"]`, "--max-age", "2h")
	for _, want := range []string{"  --max-age: 6h -> 2h\n", "  model: opus -> sonnet\n", `  args: ["--permission-mode","auto"] -> ["--verbose"]` + "\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("set model, args, --max-age: no %q in\n%s", want, out)
		}
	}
	r = resident(t, st, "clerk")
	if r.Config["model"] != "sonnet" || !reflect.DeepEqual(r.Config["args"], []any{"--verbose"}) || r.MaxAge != "2h" ||
		r.Config["env_file"] != was.Config["env_file"] || r.Dir != was.Dir || !reflect.DeepEqual(r.MaySetState(), []string{"linear"}) {
		t.Fatalf("after model, args and --max-age: %+v", r)
	}

	// Set as it is: nothing is written.
	if out := mustAs(t, st, "jo", "resident", "set", "clerk", "model=sonnet"); out != "resident clerk unchanged: it is set so already\n" ||
		resident(t, st, "clerk").Revision != r.Revision {
		t.Fatalf("set to what it is: %q", out)
	}

	// --unset removes a key, and --on and --dir replace theirs.
	dir := t.TempDir()
	out = mustAs(t, st, "jo", "resident", "set", "clerk", "--unset", "set_state", "--unset", "model", "--on", "held,gave_up", "--dir", dir)
	for _, want := range []string{"  set_state: linear -> (dropped)\n", "  model: sonnet -> (dropped)\n", "  --dir: " + was.Dir + " -> " + dir + "\n", " -> gave_up,held\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("--unset: no %q in\n%s", want, out)
		}
	}
	r = resident(t, st, "clerk")
	if _, has := r.Config["set_state"]; has || r.Config["model"] != nil || r.Dir != dir || !reflect.DeepEqual(r.On, []string{"held", "gave_up"}) ||
		r.Config["env_file"] != was.Config["env_file"] || r.Config["kind"] != "claude" {
		t.Fatalf("after --unset: %+v", r)
	}

	// The value of a key named like a secret is never printed.
	out = mustAs(t, st, "jo", "resident", "set", "clerk", "api_token=hunter2")
	out += mustAs(t, st, "jo", "resident", "set", "clerk", "api_token=hunter3")
	out += mustAs(t, st, "jo", "resident", "set", "clerk", "--unset", "api_token")
	if strings.Contains(out, "hunter") || !strings.Contains(out, "  api_token: (a value, not shown) -> (a value, not shown)\n") ||
		!strings.Contains(out, "  api_token: (a value, not shown) -> (dropped)\n") {
		t.Fatalf("a secret's change:\n%s", out)
	}

	r = resident(t, st, "clerk")
	for want, args := range map[string][]string{
		"nothing to set":                           {"clerk"},
		`"model": want key=value`:                  {"clerk", "model"},
		"--unset model: clerk has no such setting": {"clerk", "--unset", "model"},
		"kind is both set and unset":               {"clerk", "kind=pi", "--unset", "kind"},
		"resident nobody":                          {"nobody", "model=opus"},
		"is not a directory":                       {"clerk", "--dir", filepath.Join(dir, "none")},
		"not " + was.Revision:                      {"clerk", "model=opus", "--revision", was.Revision},
	} {
		refusedAs(t, st, "jo", want, append([]string{"resident", "set"}, args...)...)
	}
	if got := resident(t, st, "clerk"); got.Revision != r.Revision {
		t.Fatalf("a refused set wrote: %+v", got)
	}
	mustAs(t, st, "jo", "resident", "set", "clerk", "model=opus", "--revision", r.Revision)
}

// The operator rule of resident add holds on resident set: allowing a
// resident a source or a peer it was not allowed is the operator's, and
// everything else is anyone's.
func TestResidentSetIsTheOperatorsForPermissions(t *testing.T) {
	st, was := clerkYard(t)
	for _, actor := range []string{"yardmaster", "clerk", "yard-10-new"} {
		refusedAs(t, st, actor, "set_state: only the operator (jo) may let clerk set the status of threads in linear; this is "+actor,
			"resident", "set", "clerk", "set_state=linear")
		refusedAs(t, st, actor, "open_threads: only the operator (jo) may let clerk open threads in github; this is "+actor,
			"resident", "set", "clerk", "model=sonnet", "open_threads=github")
	}
	if r := resident(t, st, "clerk"); r.Revision != was.Revision {
		t.Fatalf("a refused grant wrote: %+v", r)
	}
	for want, kv := range map[string]string{
		"set_state: want sources, comma-separated (":           "set_state=",
		"--unset set_state to allow none":                      "set_state=true",
		`open_threads: yardr cannot open threads in "slack"`:   "open_threads=linear,slack",
		`peers: this yard has no peer "south" (it has: none)`:  "peers=south",
		"peers: want peers, comma-separated (); --unset peers": "peers=",
	} {
		refusedAs(t, st, "jo", want, "resident", "set", "clerk", kv)
	}

	mustAs(t, st, "jo", "resident", "set", "clerk", "set_state=linear,github")
	// Keeping a grant while changing something else, narrowing it and
	// removing it are anyone's: the rule is about what is added.
	mustAs(t, st, "yardmaster", "resident", "set", "clerk", "model=sonnet")
	mustAs(t, st, "yardmaster", "resident", "set", "clerk", "set_state=github,linear")
	mustAs(t, st, "yard-10-new", "resident", "set", "clerk", "set_state=linear")
	if r := resident(t, st, "clerk"); !reflect.DeepEqual(r.MaySetState(), []string{"linear"}) || r.Config["model"] != "sonnet" {
		t.Fatalf("after narrowing: %+v", r)
	}
	refusedAs(t, st, "yardmaster", "may let clerk set the status of threads in github; this is yardmaster", "resident", "set", "clerk", "set_state=linear,github")
	mustAs(t, st, "yardmaster", "resident", "set", "clerk", "--unset", "set_state")
	if r := resident(t, st, "clerk"); len(r.MaySetState()) != 0 {
		t.Fatalf("after --unset: %+v", r)
	}
}

// resident add on a resident that exists replaces its whole definition, so
// outside a terminal it is refused unless --replace says that is meant, and
// the refusal lists what would go (yardr-q5hf: "yardr resident add
// yardmaster" alone answered "saved" and had dropped a running resident's
// model, args, prompt, env_file, dir and max-age).
func TestResidentAddOnAnExistingResident(t *testing.T) {
	st, was := clerkYard(t)
	setInteractive(t, false)

	_, err := as(t, st, "jo", "resident", "add", "clerk")
	if err == nil {
		t.Fatal("resident add clerk alone replaced the definition")
	}
	for _, want := range []string{"resident clerk exists, and add replaces its whole definition",
		"  model: opus -> (dropped)\n", "  env_file: " + was.Config["env_file"].(string) + " -> (dropped)\n",
		`  args: ["--permission-mode","auto"] -> (dropped)` + "\n", "  --dir: " + was.Dir + " -> (dropped)\n", "  --max-age: 6h -> (dropped)\n",
		"yardr resident set clerk key=value", "--replace"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal has no %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "kind:") {
		t.Fatalf("the refusal lists a setting that stays:\n%v", err)
	}
	r := resident(t, st, "clerk")
	sameDefinition(t, r, was)
	if r.Revision != was.Revision {
		t.Fatalf("the refused add wrote: %+v", r)
	}

	// An agent's pane is a terminal, and nobody answers there: refused too.
	setInteractive(t, true)
	stubAsk(t, func(...huh.Field) error { t.Error("asked in an agent's session"); return nil })
	t.Setenv("YARDR_RESIDENT", "clerk")
	if _, err := as(t, st, "jo", "resident", "add", "clerk"); err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("in an agent's session: %v", err)
	}
	t.Setenv("YARDR_RESIDENT", "")

	// In a terminal it shows the same list and asks; no is the default.
	var shown string
	answer := ""
	stubAsk(t, func(fields ...huh.Field) error {
		c := fields[0].(*huh.Confirm)
		shown = c.View()
		if c.GetValue() != false {
			t.Error("replacing defaults to yes")
		}
		if answer != "" {
			c.WithKeyMap(huh.NewDefaultKeyMap())
			c.Focus()
			c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(answer)})
		}
		return nil
	})
	if out := mustAs(t, st, "jo", "resident", "add", "clerk"); out != "resident clerk not changed\n" ||
		!strings.Contains(shown, "Replace the definition of clerk?") || !strings.Contains(shown, "model: opus -> (dropped)") {
		t.Fatalf("asked and answered no: %q\n%s", out, shown)
	}
	sameDefinition(t, resident(t, st, "clerk"), was)

	// The same definition again changes nothing, so there is nothing to
	// ask or refuse: a script that adds its residents runs twice.
	setInteractive(t, false)
	again := []string{"resident", "add", "clerk", "--set", "kind=claude", "--set", "model=opus",
		"--set", `args=["--permission-mode","auto"]`, "--set", "env_file=" + was.Config["env_file"].(string), "--dir", was.Dir, "--max-age", "6h"}
	if out := mustAs(t, st, "jo", again...); strings.Contains(out, "->") {
		t.Fatalf("the same definition again: %q", out)
	}
	sameDefinition(t, resident(t, st, "clerk"), was)

	// --replace saves what was given and says what that changed; so does a
	// yes in a terminal.
	out := mustAs(t, st, "jo", "resident", "add", "clerk", "--replace", "--set", "model=sonnet", "--dir", was.Dir)
	if !strings.HasPrefix(out, "resident clerk saved; journal clerk-journal\n") || !strings.Contains(out, "  model: opus -> sonnet\n") ||
		!strings.Contains(out, "  env_file: "+was.Config["env_file"].(string)+" -> (dropped)\n") {
		t.Fatalf("--replace: %q", out)
	}
	if r := resident(t, st, "clerk"); r.Config["model"] != "sonnet" || r.Config["env_file"] != nil || r.MaxAge != "" || r.Dir != was.Dir {
		t.Fatalf("after --replace: %+v", r)
	}
	setInteractive(t, true)
	answer = "y"
	if out := mustAs(t, st, "jo", "resident", "add", "clerk", "--set", "model=opus"); !strings.Contains(out, "resident clerk saved") ||
		!strings.Contains(out, "  model: sonnet -> opus\n") || resident(t, st, "clerk").Config["model"] != "opus" {
		t.Fatalf("asked and answered yes: %q", out)
	}
	// A new resident is asked nothing.
	stubAsk(t, func(...huh.Field) error { t.Error("asked about a new resident"); return nil })
	if out := mustAs(t, st, "jo", "resident", "add", "scout", "--set", "kind=claude"); out != "resident scout saved; journal scout-journal\n" {
		t.Fatalf("a new resident: %q", out)
	}
}

// movingStore refuses the first PutResident that names a revision, as the
// store does when the daemon's bookkeeping wrote the resident in between.
type movingStore struct {
	store.API
	moved int
}

func (m *movingStore) PutResident(ctx context.Context, def store.Resident, actor string) (*store.Resident, error) {
	if def.Revision != "" && m.moved == 0 {
		m.moved++
		// What the daemon does to a running resident: a write of its own.
		if err := m.API.SetResidentEnabled(ctx, def.Name, true, "", "yardr"); err != nil {
			return nil, err
		}
	}
	return m.API.PutResident(ctx, def, actor)
}

// A resident written between resident set's read and its write (a running
// one's revision moves with the daemon's bookkeeping) is read again and
// set, not refused and not overwritten blind; with --revision the caller
// asked for the refusal.
func TestResidentSetReadsAgainWhenTheResidentMoved(t *testing.T) {
	st, was := clerkYard(t)
	m := &movingStore{API: st}
	if out := mustAs(t, m, "jo", "resident", "set", "clerk", "model=sonnet"); m.moved != 1 || !strings.Contains(out, "  model: opus -> sonnet\n") {
		t.Fatalf("set over a moved resident (%d): %q", m.moved, out)
	}
	r := resident(t, st, "clerk")
	sameDefinition(t, r, was, "model")
	if r.Config["model"] != "sonnet" {
		t.Fatalf("after the second try: %+v", r)
	}
	m.moved = 0
	refusedAs(t, m, "jo", "read it again", "resident", "set", "clerk", "model=opus", "--revision", r.Revision)
	if got := resident(t, st, "clerk"); got.Config["model"] != "sonnet" {
		t.Fatalf("a set at a stale revision wrote: %+v", got)
	}
}

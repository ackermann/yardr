package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// clerkYard is userYard with a crew member clerk whose definition has what the
// yardmaster's had when a bare crew add wiped it (yardr-q5hf): a model,
// args, an env_file, a dir and a max-age.
func clerkYard(t *testing.T) (store.API, *store.CrewMember) {
	t.Helper()
	return clerkYardOn(t, storetest.New(t))
}

// clerkYardOn is clerkYard on the store st.
func clerkYardOn(t *testing.T, st store.API) (store.API, *store.CrewMember) {
	t.Helper()
	userYardOn(t, st)
	env := filepath.Join(t.TempDir(), "clerk.env")
	if err := os.WriteFile(env, []byte("LINEAR_API_KEY=lin_api_clerk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "crew", "add", "clerk", "--set", "kind=claude", "--set", "model=opus",
		"--set", `args=["--permission-mode","auto"]`, "--set", "env_file="+env, "--dir", t.TempDir(), "--max-age", "6h")
	return st, member(t, st, "clerk")
}

func member(t *testing.T, st store.API, name string) *store.CrewMember {
	t.Helper()
	r, err := st.GetCrew(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// sameDefinition fails unless got is defined as want is, but for the config
// keys named.
func sameDefinition(t *testing.T, got, want *store.CrewMember, but ...string) {
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

// crew set changes the settings named and keeps the rest (yardr-q5hf:
// granting one permission took crew add with the whole definition
// retyped), and says what it changed.
func TestCrewSetKeepsTheRest(t *testing.T) {
	st, was := clerkYard(t)
	if _, err := st.PutSource(context.Background(), "acme.yaml", acmeCLI, "jo"); err != nil {
		t.Fatal(err)
	}
	declareGitHubPack(t, st, "", "", "jo")

	out := mustAs(t, st, "jo", "crew", "set", "clerk", "set_state=acme")
	if out != "crew member clerk saved\n  set_state: (not set) -> acme\n" {
		t.Fatalf("set set_state: %q", out)
	}
	r := member(t, st, "clerk")
	sameDefinition(t, r, was, "set_state")
	if !reflect.DeepEqual(r.MaySetState(), []string{"acme"}) || r.Journal != was.Journal {
		t.Fatalf("after the grant: %+v", r)
	}

	// Several at once, a flag among them; a list is a list in the store.
	out = mustAs(t, st, "jo", "crew", "set", "clerk", "model=sonnet", `args=["--verbose"]`, "--max-age", "2h")
	for _, want := range []string{"  --max-age: 6h -> 2h\n", "  model: opus -> sonnet\n", `  args: ["--permission-mode","auto"] -> ["--verbose"]` + "\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("set model, args, --max-age: no %q in\n%s", want, out)
		}
	}
	r = member(t, st, "clerk")
	if r.Config["model"] != "sonnet" || !reflect.DeepEqual(r.Config["args"], []any{"--verbose"}) || r.MaxAge != "2h" ||
		r.Config["env_file"] != was.Config["env_file"] || r.Dir != was.Dir || !reflect.DeepEqual(r.MaySetState(), []string{"acme"}) {
		t.Fatalf("after model, args and --max-age: %+v", r)
	}

	// Set as it is: nothing is written.
	if out := mustAs(t, st, "jo", "crew", "set", "clerk", "model=sonnet"); out != "crew member clerk unchanged: it is set so already\n" ||
		member(t, st, "clerk").Revision != r.Revision {
		t.Fatalf("set to what it is: %q", out)
	}

	// --unset removes a key, and --on and --dir replace theirs.
	dir := t.TempDir()
	out = mustAs(t, st, "jo", "crew", "set", "clerk", "--unset", "set_state", "--unset", "model", "--on", "held,gave_up", "--dir", dir)
	for _, want := range []string{"  set_state: acme -> (dropped)\n", "  model: sonnet -> (dropped)\n", "  --dir: " + was.Dir + " -> " + dir + "\n", " -> gave_up,held\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("--unset: no %q in\n%s", want, out)
		}
	}
	r = member(t, st, "clerk")
	if _, has := r.Config["set_state"]; has || r.Config["model"] != nil || r.Dir != dir || !reflect.DeepEqual(r.On, []string{"held", "gave_up"}) ||
		r.Config["env_file"] != was.Config["env_file"] || r.Config["kind"] != "claude" {
		t.Fatalf("after --unset: %+v", r)
	}

	r = member(t, st, "clerk")
	for want, args := range map[string][]string{
		"nothing to set":                           {"clerk"},
		`"model": want key=value`:                  {"clerk", "model"},
		"--unset model: clerk has no such setting": {"clerk", "--unset", "model"},
		"kind is both set and unset":               {"clerk", "kind=pi", "--unset", "kind"},
		"crew member nobody":                       {"nobody", "model=opus"},
		"is not a directory":                       {"clerk", "--dir", filepath.Join(dir, "none")},
		"not " + was.Revision:                      {"clerk", "model=opus", "--revision", was.Revision},
	} {
		refusedAs(t, st, "jo", want, append([]string{"crew", "set"}, args...)...)
	}
	if got := member(t, st, "clerk"); got.Revision != r.Revision {
		t.Fatalf("a refused set wrote: %+v", got)
	}
	mustAs(t, st, "jo", "crew", "set", "clerk", "model=opus", "--revision", r.Revision)
}

// The operator rule of crew add holds on crew set: allowing a
// crew member a source or a peer it was not allowed is the operator's, and
// everything else is anyone's.
func TestCrewSetIsTheOperatorsForPermissions(t *testing.T) {
	st, was := clerkYard(t)
	if _, err := st.PutSource(context.Background(), "acme.yaml", acmeCLI, "jo"); err != nil {
		t.Fatal(err)
	}
	declareGitHubPack(t, st, "", "", "jo")
	for _, actor := range []string{"yardmaster", "clerk", "yard-10-new"} {
		refusedAs(t, st, actor, "only the operator (jo) may let clerk set the status of threads in acme; this is "+actor,
			"crew", "set", "clerk", "set_state=acme")
		refusedAs(t, st, actor, "only the operator (jo) may let clerk open threads in github; this is "+actor,
			"crew", "set", "clerk", "model=sonnet", "open_threads=github")
	}
	if r := member(t, st, "clerk"); r.Revision != was.Revision {
		t.Fatalf("a refused grant wrote: %+v", r)
	}
	for want, kv := range map[string]string{
		"set_state: want sources, comma-separated (":           "set_state=",
		"--unset set_state to allow none":                      "set_state=true",
		`open_threads: yardr cannot open threads in "slack"`:   "open_threads=acme,slack",
		`peers: this yard has no peer "south" (it has: none)`:  "peers=south",
		"peers: want peers, comma-separated (); --unset peers": "peers=",
	} {
		refusedAs(t, st, "jo", want, "crew", "set", "clerk", kv)
	}

	mustAs(t, st, "jo", "crew", "set", "clerk", "set_state=acme,github")
	// Keeping a grant while changing something else, narrowing it and
	// removing it are anyone's: the rule is about what is added.
	mustAs(t, st, "yardmaster", "crew", "set", "clerk", "model=sonnet")
	mustAs(t, st, "yardmaster", "crew", "set", "clerk", "set_state=github,acme")
	mustAs(t, st, "yard-10-new", "crew", "set", "clerk", "set_state=acme")
	if r := member(t, st, "clerk"); !reflect.DeepEqual(r.MaySetState(), []string{"acme"}) || r.Config["model"] != "sonnet" {
		t.Fatalf("after narrowing: %+v", r)
	}
	refusedAs(t, st, "yardmaster", "may let clerk set the status of threads in github; this is yardmaster", "crew", "set", "clerk", "set_state=acme,github")
	mustAs(t, st, "yardmaster", "crew", "set", "clerk", "--unset", "set_state")
	if r := member(t, st, "clerk"); len(r.MaySetState()) != 0 {
		t.Fatalf("after --unset: %+v", r)
	}
}

// crew add on a crew member that exists replaces its whole definition, so
// outside a terminal it is refused unless --replace says that is meant, and
// the refusal lists what would go (yardr-q5hf: "yardr crew add
// yardmaster" alone answered "saved" and had dropped a running crew member's
// model, args, prompt, env_file, dir and max-age).
func TestCrewAddOnAnExistingCrew(t *testing.T) {
	st, was := clerkYard(t)
	setInteractive(t, false)

	_, err := as(t, st, "jo", "crew", "add", "clerk")
	if err == nil {
		t.Fatal("crew add clerk alone replaced the definition")
	}
	for _, want := range []string{"crew member clerk exists, and add replaces its whole definition",
		"  model: opus -> (dropped)\n", "  env_file: " + was.Config["env_file"].(string) + " -> (dropped)\n",
		`  args: ["--permission-mode","auto"] -> (dropped)` + "\n", "  --dir: " + was.Dir + " -> (dropped)\n", "  --max-age: 6h -> (dropped)\n",
		"yardr crew set clerk key=value", "--replace"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal has no %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "kind:") {
		t.Fatalf("the refusal lists a setting that stays:\n%v", err)
	}
	r := member(t, st, "clerk")
	sameDefinition(t, r, was)
	if r.Revision != was.Revision {
		t.Fatalf("the refused add wrote: %+v", r)
	}

	// An agent's pane is a terminal, and nobody answers there: refused too.
	setInteractive(t, true)
	stubAsk(t, func(...huh.Field) error { t.Error("asked in an agent's session"); return nil })
	t.Setenv("YARDR_CREW", "clerk")
	if _, err := as(t, st, "jo", "crew", "add", "clerk"); err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("in an agent's session: %v", err)
	}
	t.Setenv("YARDR_CREW", "")

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
	if out := mustAs(t, st, "jo", "crew", "add", "clerk"); out != "crew member clerk not changed\n" ||
		!strings.Contains(shown, "Replace the definition of clerk?") || !strings.Contains(shown, "model: opus -> (dropped)") {
		t.Fatalf("asked and answered no: %q\n%s", out, shown)
	}
	sameDefinition(t, member(t, st, "clerk"), was)

	// The same definition again changes nothing, so there is nothing to
	// ask or refuse: a script that adds its crew runs twice.
	setInteractive(t, false)
	again := []string{"crew", "add", "clerk", "--set", "kind=claude", "--set", "model=opus",
		"--set", `args=["--permission-mode","auto"]`, "--set", "env_file=" + was.Config["env_file"].(string), "--dir", was.Dir, "--max-age", "6h"}
	if out := mustAs(t, st, "jo", again...); strings.Contains(out, "->") {
		t.Fatalf("the same definition again: %q", out)
	}
	sameDefinition(t, member(t, st, "clerk"), was)

	// --replace saves what was given and says what that changed; so does a
	// yes in a terminal.
	out := mustAs(t, st, "jo", "crew", "add", "clerk", "--replace", "--set", "model=sonnet", "--dir", was.Dir)
	if !strings.HasPrefix(out, "crew member clerk saved; journal clerk-journal\n") || !strings.Contains(out, "  model: opus -> sonnet\n") ||
		!strings.Contains(out, "  env_file: "+was.Config["env_file"].(string)+" -> (dropped)\n") {
		t.Fatalf("--replace: %q", out)
	}
	if r := member(t, st, "clerk"); r.Config["model"] != "sonnet" || r.Config["env_file"] != nil || r.MaxAge != "" || r.Dir != was.Dir {
		t.Fatalf("after --replace: %+v", r)
	}
	setInteractive(t, true)
	answer = "y"
	if out := mustAs(t, st, "jo", "crew", "add", "clerk", "--set", "model=opus"); !strings.Contains(out, "crew member clerk saved") ||
		!strings.Contains(out, "  model: sonnet -> opus\n") || member(t, st, "clerk").Config["model"] != "opus" {
		t.Fatalf("asked and answered yes: %q", out)
	}
	// A new crew member is asked nothing.
	stubAsk(t, func(...huh.Field) error { t.Error("asked about a new crew member"); return nil })
	if out := mustAs(t, st, "jo", "crew", "add", "scout", "--set", "kind=claude"); out != "crew member scout saved; journal scout-journal\n" {
		t.Fatalf("a new crew member: %q", out)
	}
}

// movingStore refuses the first PutCrew that names a revision, as the
// store does when the daemon's bookkeeping wrote the crew member in between.
type movingStore struct {
	store.API
	moved int
}

func (m *movingStore) PutCrew(ctx context.Context, def store.CrewMember, actor string) (*store.CrewMember, error) {
	if def.Revision != "" && m.moved == 0 {
		m.moved++
		// What the daemon does to a running crew member: a write of its own.
		if err := m.API.SetCrewEnabled(ctx, def.Name, true, "", "yardr"); err != nil {
			return nil, err
		}
	}
	return m.API.PutCrew(ctx, def, actor)
}

// A crew member written between crew set's read and its write (a running
// one's revision moves with the daemon's bookkeeping) is read again and
// set, not refused and not overwritten blind; with --revision the caller
// asked for the refusal.
func TestCrewSetReadsAgainWhenTheCrewMoved(t *testing.T) {
	st, was := clerkYard(t)
	m := &movingStore{API: st}
	if out := mustAs(t, m, "jo", "crew", "set", "clerk", "model=sonnet"); m.moved != 1 || !strings.Contains(out, "  model: opus -> sonnet\n") {
		t.Fatalf("set over a moved crew member (%d): %q", m.moved, out)
	}
	r := member(t, st, "clerk")
	sameDefinition(t, r, was, "model")
	if r.Config["model"] != "sonnet" {
		t.Fatalf("after the second try: %+v", r)
	}
	m.moved = 0
	refusedAs(t, m, "jo", "read it again", "crew", "set", "clerk", "model=opus", "--revision", r.Revision)
	if got := member(t, st, "clerk"); got.Config["model"] != "sonnet" {
		t.Fatalf("a set at a stale revision wrote: %+v", got)
	}
}

// A --set value is read as JSON first, so env_command=true is the boolean,
// which the herdr runner takes as not set: the crew member was stored and
// started without the credentials the command was to print, with no error
// (yardr-d0ps). crew add and crew set ask the runner before they store
// anything, as group add does, and say how the string is written.
func TestCrewRefusesASettingTheRunnerCannotRead(t *testing.T) {
	st, was := clerkYard(t)
	if _, err := st.PutSource(context.Background(), "acme.yaml", acmeCLI, "jo"); err != nil {
		t.Fatal(err)
	}
	declareGitHubPack(t, st, "", "", "jo")
	ctx := context.Background()

	for want, set := range map[string]string{
		`crew member x: herdr runner needs config.env_command as a string or a list, got the JSON boolean true; write --set 'env_command="true"'`: "env_command=true",
		`crew member x: herdr runner needs config.prompt as a string, got the JSON boolean false; write --set 'prompt="false"'`:                   "prompt=false",
		`crew member x: herdr runner needs config.model as a string, got the JSON number 5; write --set 'model="5"'`:                              "model=5",
		`crew member x: herdr runner needs config.env_file as a string, got the JSON null; write --set 'env_file="null"'`:                         "env_file=null",
		`crew member x: herdr runner needs config.kind as a string, got the JSON list ["claude"]; write --set 'kind="..."'`:                       `kind=["claude"]`,
	} {
		refusedAs(t, st, "jo", want, "crew", "add", "x", "--set", set)
	}
	if _, err := st.GetCrew(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused crew member was stored: %v", err)
	}
	if out, err := as(t, st, "jo", "crew", "show", "x"); err == nil {
		t.Fatalf("crew show lists a refused crew member:\n%s", out)
	}
	// Replacing one that exists is refused the same, before it is asked about.
	refusedAs(t, st, "jo", `crew member clerk: herdr runner needs config.env_command as a string or a list`,
		"crew", "add", "clerk", "--replace", "--set", "env_command=true")

	// crew set takes the key bare, so its hint has no --set.
	mustAs(t, st, "jo", "crew", "set", "clerk", "env_command=op inject -i env.tpl")
	for want, set := range map[string]string{
		`crew member clerk: herdr runner needs config.env_command as a string or a list, got the JSON boolean true; write 'env_command="true"'`: "env_command=true",
		`crew member clerk: herdr runner needs config.model as a string, got the JSON number 5; write 'model="5"'`:                              "model=5",
	} {
		refusedAs(t, st, "jo", want, "crew", "set", "clerk", set, "set_state=acme")
	}
	r := member(t, st, "clerk")
	sameDefinition(t, r, was, "env_command")
	if r.Config["env_command"] != "op inject -i env.tpl" || len(r.MaySetState()) != 0 {
		t.Fatalf("a refused crew set changed the definition: %+v", r.Config)
	}

	// The JSON string is the string, and a list of words is still one.
	mustAs(t, st, "jo", "crew", "set", "clerk", `model="5"`, `env_command=["op","inject"]`)
	mustAs(t, st, "jo", "crew", "add", "x", "--set", `env_command="true"`)
	if x := member(t, st, "x"); x.Config["env_command"] != "true" {
		t.Fatalf("x = %+v; want the string true as its env_command", x.Config)
	}

	// crew show prints the config as --set takes it back.
	out := mustAs(t, st, "jo", "crew", "show", "x")
	if want := `  config env_command="true" kind=claude` + "\n"; !strings.Contains(out, want) {
		t.Fatalf("crew show x has no %q:\n%s", want, out)
	}
	if out := mustAs(t, st, "jo", "crew", "show", "clerk"); !strings.Contains(out, `env_command=["op","inject"]`) || !strings.Contains(out, ` model="5"`) {
		t.Fatalf("crew show clerk:\n%s", out)
	}
}

// A config key nothing reads is refused by the store now, but a yard set up
// before it was may hold one, named like a secret: the crew member can
// still be changed and shown, the key removed, and its value is never
// printed. What the store refuses is a write that adds or changes such a
// key (yardr-3wt9).
func TestCrewKeepsAKeyNothingReadsAndNeverPrintsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	old, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	clerkYardOn(t, old)
	if err := old.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	storetest.OldConfig(t, path, "crew", "clerk", "api_token", "hunter2")
	st := storetest.Open(t, path)

	out := mustAs(t, st, "jo", "crew", "set", "clerk", "model=sonnet")
	out += mustAs(t, st, "jo", "crew", "show", "clerk")
	if !strings.Contains(out, "api_token=(a value, not shown)") || strings.Contains(out, "hunter") {
		t.Fatalf("a crew member with a key nothing reads, set and shown:\n%s", out)
	}
	if r := member(t, st, "clerk"); r.Config["api_token"] != "hunter2" || r.Config["model"] != "sonnet" {
		t.Fatalf("after a set of another key: %+v", r.Config)
	}
	for _, set := range []string{"api_token=hunter3", "modle=opus"} {
		key, _, _ := strings.Cut(set, "=")
		refusedAs(t, st, "jo", `crew member clerk: unknown config key "`+key+`": nothing reads it. A crew member of the herdr runner takes: args, env_command, env_file, for, greet, kind, model, open_threads, peers, prompt, provider, set_state, trust`,
			"crew", "set", "clerk", set)
	}
	refusedAs(t, st, "jo", `crew member y: unknown config key "max_age"`, "crew", "add", "y", "--set", "max_age=6h")
	if out := mustAs(t, st, "jo", "crew", "set", "clerk", "--unset", "api_token"); strings.Contains(out, "hunter") ||
		!strings.Contains(out, "  api_token: (a value, not shown) -> (dropped)\n") {
		t.Fatalf("a secret's removal:\n%s", out)
	}
}

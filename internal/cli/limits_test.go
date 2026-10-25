package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// limitsStandIn is a script in place of a provider's program, and the file
// of a source, quokka, that runs it for kimi and claude. The script writes
// a line to asked each time it is run, and then does body.
func limitsStandIn(t *testing.T, body string) (file, asked string) {
	t.Helper()
	return limitsStandInAs(t, "quokka", "{ kimi: moonshot, claude: claude }", body)
}

// limitsStandInAs is limitsStandIn for a source named name with the kinds
// table given, for a yard with more than one source.
func limitsStandInAs(t *testing.T, name, kinds, body string) (file, asked string) {
	t.Helper()
	dir := t.TempDir()
	asked = filepath.Join(dir, "asked")
	script := filepath.Join(dir, "provider")
	scripttest.Write(t, script, "#!/bin/sh\necho \"$1\" >>"+asked+"\n"+body+"\n")
	file = filepath.Join(dir, name+".yaml")
	text := "name: " + name + "\nabout: A stand-in for a provider of limits.\nlimits:\n  command: [" + script + ", \"{provider}\", --json]\n  kinds: " + kinds + "\n"
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return file, asked
}

// asks are the provider names the stand-in was run for, in order.
func asks(t *testing.T, asked string) string {
	t.Helper()
	b, err := os.ReadFile(asked)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// kimiAnswer is a report with a 5h window used up that resets in 42
// minutes and some seconds, and a weekly one that is not.
func kimiAnswer() string {
	in := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }
	return fmt.Sprintf(`echo '{"provider":"Kimi","windows":[{"label":"5h","used_percent":100,"resets_at":%q,"used":100,"limit":100},{"label":"Weekly","used_percent":38,"resets_at":%q}]}'`,
		in(42*time.Minute+20*time.Second), in(81*time.Hour+20*time.Second))
}

// yardr limits <kind> asks the source that lists the kind, once, and prints
// its windows; --json has the report. A kind nothing lists, and a command
// that fails, are "not known" with why, and exit 1.
func TestLimitsOfAKind(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	file, asked := limitsStandIn(t, kimiAnswer())
	out, err := run(t, a.sourceCmd(), "add", file)
	if err != nil || !strings.Contains(out, "source quokka saved: no call, its limits command alone\n") ||
		!strings.Contains(out, "limits: its command says what the subscription of claude, kimi still carries (yardr limits <kind>)") ||
		!strings.Contains(out, "next: yardr source check quokka") {
		t.Fatalf("source add: %q %v", out, err)
	}
	if got := asks(t, asked); got != "" {
		t.Fatalf("adding the source asked it: %q", got)
	}
	out, _ = writerRun(t, a.limitsCmd(), "kimi")
	for _, want := range []string{
		"kimi  quokka says (provider Kimi):\n",
		"  5h      100% used (100 of 100)  resets ",
		" (in 42m)  used up\n",
		"  Weekly  38% used                resets ",
		" (in 3d 9h)\n",
		"  an agent of kind kimi waits for the 5h window, which resets ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("limits kimi printed no %q:\n%s", want, out)
		}
	}
	if got := asks(t, asked); got != "moonshot" {
		t.Fatalf("asked = %q, want the provider's name for kimi, once", got)
	}

	a.json = true
	out, _ = writerRun(t, a.limitsCmd(), "kimi")
	a.json = false
	var res struct {
		Limits []struct {
			Kind   string `json:"kind"`
			Limits struct {
				Source, Provider string
				Windows          []struct{ Label string }
			} `json:"limits"`
			Window   string    `json:"window"`
			ResetsAt time.Time `json:"resets_at"`
		} `json:"limits"`
	}
	if json.Unmarshal([]byte(out), &res) != nil || len(res.Limits) != 1 {
		t.Fatalf("limits kimi --json: %q", out)
	}
	if k := res.Limits[0]; k.Kind != "kimi" || k.Limits.Source != "quokka" || k.Limits.Provider != "Kimi" || len(k.Limits.Windows) != 2 ||
		k.Window != "5h" || time.Until(k.ResetsAt) < 40*time.Minute || time.Until(k.ResetsAt) > 43*time.Minute {
		t.Fatalf("limits kimi --json = %+v", k)
	}

	// No source lists codex: nothing is asked.
	out, err = run(t, a.limitsCmd(), "codex")
	if !errors.Is(err, errCheckFailed) || !strings.Contains(out, "codex  not known: no source of this yard says the limits of codex") {
		t.Fatalf("limits codex: %q %v", out, err)
	}
	if got := asks(t, asked); got != "moonshot moonshot" {
		t.Fatalf("asked = %q", got)
	}

	// The provider fails: not known, with what it said.
	file, _ = limitsStandIn(t, `echo "aiquokka: kimi: not logged in" >&2; exit 1`)
	if _, err = run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.limitsCmd(), "kimi")
	if !errors.Is(err, errCheckFailed) || !strings.Contains(out, "kimi  not known: source quokka: limits command ") ||
		!strings.Contains(out, "exit status 1; its stderr ended: aiquokka: kimi: not logged in") {
		t.Fatalf("limits kimi, the provider failing: %q %v", out, err)
	}
	a.json = true
	out, err = run(t, a.limitsCmd(), "kimi")
	a.json = false
	if !errors.Is(err, errCheckFailed) || !strings.Contains(out, `"limits_unknown": "source quokka: limits command `) || strings.Contains(out, `"window"`) {
		t.Fatalf("limits kimi --json, the provider failing: %q %v", out, err)
	}
}

// With --group the command runs with the group's env and the answer is for
// the login the group burns: two groups on different logins report
// different windows, and neither is the ambient answer. Without the flag
// the machine's login is read as before.
func TestLimitsGroupReadsTheGroupsLogin(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "jo"}
	// The ambient login the limits command hands its provider's process.
	t.Setenv("QUOKKA_LOGIN", "operator")
	file, _ := limitsStandIn(t, `case "$QUOKKA_LOGIN" in
first) echo '{"provider":"Kimi","plan":"First","windows":[{"label":"Weekly","used_percent":10}]}';;
second) echo '{"provider":"Kimi","plan":"Second","windows":[{"label":"Weekly","used_percent":90}]}';;
*) echo '{"provider":"Kimi","plan":"Ambient","windows":[{"label":"Weekly","used_percent":50}]}';;
esac`)
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	env := func(login string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "group.env")
		if err := os.WriteFile(p, []byte("QUOKKA_LOGIN="+login+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	putGroup := func(name, kind, provider, login string) {
		t.Helper()
		cfg := map[string]any{"kind": kind}
		if provider != "" {
			cfg["provider"] = provider
		}
		if login != "" {
			cfg["env_file"] = env(login)
		}
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: cfg}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	putGroup("builders", "kimi", "", "first")
	putGroup("mckean2-builders", "kimi", "", "second")

	out, err := run(t, a.limitsCmd(), "--group", "mckean2-builders")
	if err != nil {
		t.Fatalf("limits --group mckean2-builders: %q %v", out, err)
	}
	for _, want := range []string{
		"kimi  quokka says (provider Kimi, plan Second), for group mckean2-builders:\n",
		"Weekly  90% used",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("limits --group mckean2-builders printed no %q:\n%s", want, out)
		}
	}
	out, err = run(t, a.limitsCmd(), "--group", "builders")
	if err != nil || !strings.Contains(out, "kimi  quokka says (provider Kimi, plan First), for group builders:\n") ||
		!strings.Contains(out, "Weekly  10% used") {
		t.Fatalf("limits --group builders: %q %v", out, err)
	}
	// The flag's read is the group's, not the ambient answer: the operator
	// reads Ambient.
	out, err = run(t, a.limitsCmd(), "kimi")
	if err != nil || !strings.Contains(out, "kimi  quokka says (provider Kimi, plan Ambient):\n") {
		t.Fatalf("limits kimi: %q %v", out, err)
	}
	// --json carries whose login was read.
	a.json = true
	out, err = run(t, a.limitsCmd(), "--group", "mckean2-builders")
	a.json = false
	var res struct {
		Limits []struct {
			Kind   string `json:"kind"`
			Group  string `json:"group"`
			Limits struct {
				Plan string `json:"plan"`
			} `json:"limits"`
		} `json:"limits"`
	}
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || len(res.Limits) != 1 {
		t.Fatalf("limits --group mckean2-builders --json: %q %v", out, err)
	}
	if k := res.Limits[0]; k.Kind != "kimi" || k.Group != "mckean2-builders" || k.Limits.Plan != "Second" {
		t.Fatalf("limits --group mckean2-builders --json = %+v", k)
	}
	// A named kind is asked under the group's env the same way.
	out, err = run(t, a.limitsCmd(), "kimi", "--group", "mckean2-builders")
	if err != nil || !strings.Contains(out, "plan Second), for group mckean2-builders:\n") {
		t.Fatalf("limits kimi --group mckean2-builders: %q %v", out, err)
	}

	// A group with the provider setting is asked as the provider, though
	// no table lists its kind.
	putGroup("zap", "codex", "moonshot", "second")
	out, err = run(t, a.limitsCmd(), "--group", "zap")
	if err != nil || !strings.Contains(out, "codex  quokka says (provider Kimi, plan Second), for group zap:\n") {
		t.Fatalf("limits --group zap: %q %v", out, err)
	}
	// A group with no env_file reads the machine's login, as its panes do.
	putGroup("bare", "kimi", "", "")
	out, err = run(t, a.limitsCmd(), "--group", "bare")
	if err != nil || !strings.Contains(out, "plan Ambient), for group bare:\n") {
		t.Fatalf("limits --group bare: %q %v", out, err)
	}
	// A group there is none of, one whose file cannot be read, and one
	// that starts no agents are refused naming why, and ask nothing.
	if _, err := run(t, a.limitsCmd(), "--group", "ghost"); err == nil || !strings.Contains(err.Error(), "group ghost") {
		t.Fatalf("limits --group ghost: %v", err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "broken", Runner: "herdr",
		Config: map[string]any{"kind": "kimi", "env_file": filepath.Join(t.TempDir(), "gone.env")}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.limitsCmd(), "--group", "broken"); err == nil || !strings.Contains(err.Error(), "group broken") {
		t.Fatalf("limits --group broken: %v", err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "bench", Runner: "manual", Config: map[string]any{}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.limitsCmd(), "--group", "bench"); err == nil ||
		!strings.Contains(err.Error(), "group bench starts no agents") {
		t.Fatalf("limits --group bench: %v", err)
	}
}

// A window whose provider says what share of it has passed shows the
// target beside the used figure, and one burned past it is marked over
// target; --json carries the share. A window it says counts for one model
// alone is marked with the model, and --json carries it for that window
// alone.
func TestLimitsShowsTheTarget(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	in := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }
	file, _ := limitsStandIn(t, fmt.Sprintf(`echo '{"provider":"Kimi","windows":[`+
		`{"label":"5h","used_percent":100,"target_percent":60,"resets_at":%q,"used":100,"limit":100},`+
		`{"label":"Weekly","used_percent":38,"target_percent":17,"resets_at":%q},`+
		`{"label":"Session","used_percent":10,"target_percent":17},`+
		`{"label":"Weekly k3","model":"k3","used_percent":100,"resets_at":%q}]}'`,
		in(42*time.Minute), in(81*time.Hour), in(81*time.Hour)))
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.limitsCmd(), "kimi")
	if err != nil {
		t.Fatalf("limits kimi: %q %v", out, err)
	}
	line := func(label string) string {
		t.Helper()
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, label) && strings.Contains(l, "used") {
				return l
			}
		}
		t.Fatalf("no line for the %s window:\n%s", label, out)
		return ""
	}
	if five := line("5h"); !strings.Contains(five, "100% used (100 of 100, target 60%)") ||
		!strings.Contains(five, "used up") || !strings.Contains(five, "over target") {
		t.Errorf("the 5h window shows %q, want the target and both marks", five)
	}
	if weekly := line("Weekly"); !strings.Contains(weekly, "38% used (target 17%)") ||
		!strings.Contains(weekly, "over target") || strings.Contains(weekly, "used up") {
		t.Errorf("the Weekly window shows %q, want the target and over target alone", weekly)
	}
	if session := line("Session"); !strings.Contains(session, "10% used (target 17%)") ||
		strings.Contains(session, "over target") {
		t.Errorf("the Session window shows %q, want the target and no mark", session)
	}
	if k3 := line("Weekly k3"); !strings.HasSuffix(k3, "  used up  model k3") {
		t.Errorf("the Weekly k3 window shows %q, want used up and its model", k3)
	}
	for _, label := range []string{"5h", "Weekly", "Session"} {
		if l := line(label); strings.Contains(l, "model") {
			t.Errorf("the %s window shows %q, want no model: it counts for every model", label, l)
		}
	}

	a.json = true
	out, err = run(t, a.limitsCmd(), "kimi")
	a.json = false
	if err != nil || !strings.Contains(out, `"target_percent": 60`) || !strings.Contains(out, `"target_percent": 17`) {
		t.Fatalf("limits kimi --json carries no target: %q %v", out, err)
	}
	if n := strings.Count(out, `"model"`); n != 1 || !strings.Contains(out, `"model": "k3"`) {
		t.Fatalf("limits kimi --json carries the model %d times, want once, k3's: %q", n, out)
	}
}

// Without a kind, yardr limits resolves what the yard runs on to providers
// and asks once per provider, naming who burns it: two groups on one
// provider are one ask. A provider setting is asked as named even where no
// table lists the kind; a provider no source names is not known, and a kind
// resolving to nothing stays unmapped.
func TestLimitsOfTheProvidersInUse(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "jo"}
	out, err := run(t, a.limitsCmd())
	if err != nil {
		t.Fatalf("limits in a yard with no source: %q %v", out, err)
	}
	for _, want := range []string{
		"machine says (provider machine):\n",
		"  load  ", "  swap  ", "  disk  ", "  mem   ",
		"% used  reset not known",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("limits in a yard with no source printed no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "target") {
		t.Errorf("the built-in carries no target:\n%s", out)
	}
	file, asked := limitsStandIn(t, kimiAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.limitsCmd())
	if err != nil || !strings.Contains(out, "machine says (provider machine):\n") || strings.Contains(out, "Nothing was asked") {
		t.Fatalf("limits with no kind in use: %q %v", out, err)
	}
	if got := asks(t, asked); got != "" {
		t.Fatalf("with no kind in use the provider was asked: %q", got)
	}
	putGroup := func(name, kind, provider string) {
		t.Helper()
		cfg := map[string]any{"kind": kind}
		if provider != "" {
			cfg["provider"] = provider
		}
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: cfg}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	putGroup("builders", "kimi", "")
	putGroup("more", "kimi", "")
	putGroup("reviewers", "codex", "")
	out, err = run(t, a.limitsCmd())
	if err != nil || !strings.Contains(out, "moonshot  quokka says (provider Kimi): burned by groups builders, more (kind kimi)") ||
		!strings.Contains(out, "  an agent burning moonshot waits for the 5h window, which resets ") ||
		!strings.Contains(out, "not asked: codex, in use here and listed by no source (limits.kinds)") {
		t.Fatalf("limits: %q %v", out, err)
	}
	if got := asks(t, asked); got != "moonshot" {
		t.Fatalf("asked = %q, want kimi's provider once: claude is not in use, codex is not listed", got)
	}

	// A group with provider=zai whose kind no table lists is asked as zai
	// through the source naming it, beside the rest: one ask per provider.
	wombat, askedWombat := limitsStandInAs(t, "wombat", "{ grok: zai }", `echo '{"provider":"Z.ai","windows":[{"label":"5h","used_percent":12,"used":12,"limit":100}]}'`)
	if _, err := run(t, a.sourceCmd(), "add", wombat); err != nil {
		t.Fatal(err)
	}
	putGroup("zap", "pi", "zai")
	out, err = run(t, a.limitsCmd())
	if err != nil || !strings.Contains(out, "moonshot  quokka says (provider Kimi): burned by groups builders, more (kind kimi)") ||
		!strings.Contains(out, "zai  wombat says (provider Z.ai): burned by group zap (kind pi)") ||
		!strings.Contains(out, "  no window is used up") {
		t.Fatalf("limits with a provider setting: %q %v", out, err)
	}
	if got := asks(t, asked); got != "moonshot moonshot" {
		t.Fatalf("asked quokka = %q, want one ask per run", got)
	}
	if got := asks(t, askedWombat); got != "zai" {
		t.Fatalf("asked wombat = %q, want the setting's provider, once", got)
	}

	// A provider no source names is not known, with who burns it, and the
	// run fails; nothing further is asked.
	putGroup("zap", "pi", "openai")
	out, err = run(t, a.limitsCmd())
	if !errors.Is(err, errCheckFailed) ||
		!strings.Contains(out, "openai  not known: no source of this yard answers for provider openai (a source with a limits key whose kinds name it); burned by group zap (kind pi)") {
		t.Fatalf("limits with an unanswered provider: %q %v", out, err)
	}
	if got := asks(t, asked); got != "moonshot moonshot moonshot" {
		t.Fatalf("asked quokka = %q, want the answered provider alone, once", got)
	}
	if got := asks(t, askedWombat); got != "zai" {
		t.Fatalf("asked wombat = %q, want nothing further asked", got)
	}

	// --json has one row per provider, with the burning kinds and users.
	a.json = true
	out, err = run(t, a.limitsCmd())
	a.json = false
	var res struct {
		Limits []struct {
			Provider string   `json:"provider"`
			Kinds    []string `json:"kinds"`
			BurnedBy []string `json:"burned_by"`
			Limits   struct {
				Source, Provider string
			} `json:"limits"`
			Window  string `json:"window"`
			Unknown string `json:"limits_unknown"`
		} `json:"limits"`
		Unmapped []string `json:"unmapped"`
	}
	if !errors.Is(err, errCheckFailed) || json.Unmarshal([]byte(out), &res) != nil || len(res.Limits) != 2 {
		t.Fatalf("limits --json: %q %v", out, err)
	}
	if first := res.Limits[0]; first.Provider != "moonshot" || !reflect.DeepEqual(first.Kinds, []string{"kimi"}) ||
		!reflect.DeepEqual(first.BurnedBy, []string{"group builders", "group more"}) ||
		first.Limits.Source != "quokka" || first.Window != "5h" || first.Unknown != "" {
		t.Fatalf("limits --json first row = %+v", first)
	}
	if second := res.Limits[1]; second.Provider != "openai" || !reflect.DeepEqual(second.Kinds, []string{"pi"}) ||
		!reflect.DeepEqual(second.BurnedBy, []string{"group zap"}) || second.Unknown == "" {
		t.Fatalf("limits --json second row = %+v", second)
	}
	if !reflect.DeepEqual(res.Unmapped, []string{"codex"}) {
		t.Fatalf("limits --json unmapped = %q", res.Unmapped)
	}

	// A crew member and the yard's default burn their providers too, and
	// appear on the row: none burns without its user.
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.DefaultKindSetting, "kimi", "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.limitsCmd())
	if !errors.Is(err, errCheckFailed) ||
		!strings.Contains(out, "moonshot  quokka says (provider Kimi): burned by the yard's default, group builders, group more (kind kimi)") ||
		!strings.Contains(out, "claude  quokka says (provider Kimi): burned by crew member scribe (kind claude)") {
		t.Fatalf("limits with a crew member and a default: %q %v", out, err)
	}
}

// selfStandIn is a file of a source, machine, that answers for itself: its
// command takes no provider, writes a line to asked each time it is run,
// and then does body.
func selfStandIn(t *testing.T, body string) (file, asked string) {
	t.Helper()
	dir := t.TempDir()
	asked = filepath.Join(dir, "asked")
	script := filepath.Join(dir, "machine")
	scripttest.Write(t, script, "#!/bin/sh\necho ran >>"+asked+"\n"+body+"\n")
	file = filepath.Join(dir, "machine.yaml")
	if err := os.WriteFile(file, []byte("name: machine\nlimits:\n  command: ["+script+"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return file, asked
}

// machineAnswer is a report with the load, swap and disk windows, each with
// its target.
func machineAnswer() string {
	return `echo '{"provider":"machine","windows":[` +
		`{"label":"load","used_percent":12.5,"target_percent":80},` +
		`{"label":"swap","used_percent":10,"target_percent":85},` +
		`{"label":"disk","used_percent":93,"target_percent":90}]}'`
}

// yardr limits lists a source that answers for itself beside the providers,
// each with its windows and their targets; with its name it asks for it
// alone. --json carries the sources under sources.
func TestLimitsListsASourceThatAnswersForItself(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "jo"}
	file, _ := limitsStandIn(t, kimiAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	machine, askedMachine := selfStandIn(t, machineAnswer())
	out, err := run(t, a.sourceCmd(), "add", machine)
	if err != nil || !strings.Contains(out, "limits: its command says what the source itself carries (yardr limits)") ||
		!strings.Contains(out, "next: yardr source check machine (it asks the source for itself)") {
		t.Fatalf("source add machine: %q %v", out, err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Config: map[string]any{"kind": "kimi"}}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.limitsCmd())
	if err != nil {
		t.Fatalf("limits: %q %v", out, err)
	}
	for _, want := range []string{
		"moonshot  quokka says (provider Kimi): burned by group builders (kind kimi)",
		"machine says (provider machine):\n",
		"  load  12.5% used (target 80%)  reset not known",
		"  swap  10% used (target 85%)",
		"  disk  93% used (target 90%)",
		"over target",
		"  no window is used up",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("limits printed no %q:\n%s", want, out)
		}
	}
	if got := asks(t, askedMachine); got != "ran" {
		t.Fatalf("asked machine = %q, want once", got)
	}

	a.json = true
	out, err = run(t, a.limitsCmd())
	a.json = false
	var res struct {
		Limits []struct {
			Provider string `json:"provider"`
		} `json:"limits"`
		Sources []struct {
			Source string `json:"source"`
			Limits struct {
				Provider string `json:"provider"`
				Windows  []struct {
					Label         string   `json:"label"`
					TargetPercent *float64 `json:"target_percent"`
				} `json:"windows"`
			} `json:"limits"`
			Unknown string `json:"limits_unknown"`
		} `json:"sources"`
	}
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || len(res.Limits) != 1 || len(res.Sources) != 1 {
		t.Fatalf("limits --json: %q %v", out, err)
	}
	if s := res.Sources[0]; s.Source != "machine" || s.Limits.Provider != "machine" || len(s.Limits.Windows) != 3 ||
		s.Limits.Windows[0].Label != "load" || *s.Limits.Windows[0].TargetPercent != 80 || s.Unknown != "" {
		t.Fatalf("limits --json sources = %+v", res.Sources)
	}

	// With its name the source is asked for alone, and a kind it does not
	// list stays a kind: the kind meaning wins.
	out, err = run(t, a.limitsCmd(), "machine")
	if err != nil || !strings.Contains(out, "machine says (provider machine):\n") || strings.Contains(out, "moonshot") {
		t.Fatalf("limits machine: %q %v", out, err)
	}
	if out, err = run(t, a.limitsCmd(), "kimi"); err != nil || !strings.Contains(out, "kimi  quokka says") {
		t.Fatalf("limits kimi: %q %v", out, err)
	}
	if got := asks(t, askedMachine); got != "ran ran ran" {
		t.Fatalf("asked machine = %q, want once per run that lists it, and none for the kind", got)
	}
}

// An explicit provider is still a known use when no source has a limits key:
// list it as unknown, while kinds with no provider remain unmapped.
func TestLimitsProviderWithoutAnySource(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "jo", json: true}
	for _, g := range []store.Group{
		{Name: "zap", Runner: "herdr", Config: map[string]any{"kind": "pi", "provider": "zai"}},
		{Name: "reviewers", Runner: "herdr", Config: map[string]any{"kind": "codex"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, a.limitsCmd())
	var res struct {
		Limits []struct {
			Provider string   `json:"provider"`
			Kinds    []string `json:"kinds"`
			BurnedBy []string `json:"burned_by"`
			Unknown  string   `json:"limits_unknown"`
		} `json:"limits"`
		Unmapped []string `json:"unmapped"`
	}
	if !errors.Is(err, errCheckFailed) || json.Unmarshal([]byte(out), &res) != nil || len(res.Limits) != 1 {
		t.Fatalf("limits --json without a source: %q %v", out, err)
	}
	row := res.Limits[0]
	if row.Provider != "zai" || row.Unknown == "" || !reflect.DeepEqual(row.Kinds, []string{"pi"}) ||
		!reflect.DeepEqual(row.BurnedBy, []string{"group zap"}) || !reflect.DeepEqual(res.Unmapped, []string{"codex"}) {
		t.Fatalf("limits --json without a source = %+v unmapped %q", row, res.Unmapped)
	}
}

// /limits answers what `yardr limits` answers with no kind named, beside
// the yard's head.
func TestLimitsRouteAnswersTheCommand(t *testing.T) {
	t.Parallel()
	routeOf := func(t *testing.T, st store.API) map[string]any {
		t.Helper()
		srv, err := web.NewServer(st, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv)
		t.Cleanup(ts.Close)
		req, err := http.NewRequest("GET", ts.URL+"/limits", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("/limits: status %d: %s", res.StatusCode, body)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("/limits: %v: %s", err, body)
		}
		return out
	}
	commandOf := func(t *testing.T, a *app) map[string]any {
		t.Helper()
		a.json = true
		out, err := run(t, a.limitsCmd())
		a.json = false
		if err != nil {
			t.Fatalf("limits: %q %v", out, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("limits --json: %v: %s", err, out)
		}
		return decoded
	}

	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "jo"}
	file, _ := limitsStandIn(t, kimiAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"builders": "kimi", "reviewers": "codex"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	command, route := commandOf(t, a), routeOf(t, st)
	timeless(command)
	timeless(route)
	if !reflect.DeepEqual(route["limits"], command["limits"]) {
		t.Errorf("/limits %q\n got %v\nwant %v (the command's)", "limits", route["limits"], command["limits"])
	}
	if !reflect.DeepEqual(route["unmapped"], command["unmapped"]) {
		t.Errorf("/limits %q\n got %v\nwant %v (the command's)", "unmapped", route["unmapped"], command["unmapped"])
	}
	if head, err := st.Head(ctx); err != nil || route["head"] != float64(head) {
		t.Errorf("/limits head = %v, want %d", route["head"], head)
	}

	// No source of the yard says limits: nothing is known either way.
	bare := storetest.New(t)
	b := &app{st: bare, actor: "jo"}
	command, route = commandOf(t, b), routeOf(t, bare)
	if !reflect.DeepEqual(route["limits"], command["limits"]) || !reflect.DeepEqual(route["unmapped"], command["unmapped"]) {
		t.Errorf("with no source, /limits = %v, the command = %v", route, command)
	}
}

// timeless drops the times two asks never share: when each was asked, and
// the resets the stand-in counts from now.
func timeless(v any) {
	switch v := v.(type) {
	case map[string]any:
		delete(v, "asked_at")
		delete(v, "resets_at")
		for _, child := range v {
			timeless(child)
		}
	case []any:
		for _, child := range v {
			timeless(child)
		}
	}
}

// A source that says limits is shown and listed with its command and
// kinds, and yardr source check asks for each kind the file lists.
func TestSourceWithLimitsShownAndChecked(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	file, asked := limitsStandIn(t, `[ "$1" = moonshot ] || { echo "aiquokka: $1: not logged in" >&2; exit 1; }; `+kimiAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "show", "quokka")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"quokka  no calls\n",
		"  credential: none: the file names no credential\n",
		"  limits: its command says what the subscription of claude, kimi still carries",
		" moonshot --json\n", "    kind kimi is asked as moonshot\n",
	} {
		// The command is shown as the file has it, the mark in place.
		want = strings.Replace(want, " moonshot --json", " {provider} --json", 1)
		if !strings.Contains(out, want) {
			t.Errorf("show printed no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "check:") {
		t.Errorf("show speaks of a check call for a source without calls:\n%s", out)
	}
	if out, err = run(t, a.sourceCmd(), "list"); err != nil || !strings.Contains(out, "LIMITS") || !strings.Contains(out, "claude, kimi") {
		t.Fatalf("list: %q %v", out, err)
	}
	if got := asks(t, asked); got != "" {
		t.Fatalf("show and list asked: %q", got)
	}
	out, err = run(t, a.sourceCmd(), "check", "quokka")
	if !errors.Is(err, errCheckFailed) || !strings.Contains(out, "quokka: limits: claude  not known: source quokka: limits command ") ||
		!strings.Contains(out, "its stderr ended: aiquokka: claude: not logged in") ||
		!strings.Contains(out, "quokka: limits: kimi  quokka says (provider Kimi):") || !strings.Contains(out, "quokka: limits:   5h ") {
		t.Fatalf("check: %q %v", out, err)
	}
	if got := asks(t, asked); got != "claude moonshot" {
		t.Fatalf("check asked %q, want each kind of the file once", got)
	}
	// With every kind answered the check passes.
	file, _ = limitsStandIn(t, kimiAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.sourceCmd(), "check", "quokka"); err != nil {
		t.Fatalf("check, every kind answered: %q %v", out, err)
	}
}

// A source that answers for itself is shown and listed with its command,
// and yardr source check asks it once, for the source.
func TestSourceThatAnswersForItselfShownAndChecked(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	file, asked := selfStandIn(t, machineAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "show", "machine")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"machine  no calls\n",
		"  limits: its command says what the source itself carries (yardr limits)\n",
		"    command: ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show printed no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "kind ") {
		t.Errorf("show speaks of kinds for a source that answers for itself:\n%s", out)
	}
	if out, err = run(t, a.sourceCmd(), "list"); err != nil || !strings.Contains(out, "itself") {
		t.Fatalf("list: %q %v", out, err)
	}
	if got := asks(t, asked); got != "" {
		t.Fatalf("show and list asked: %q", got)
	}
	out, err = run(t, a.sourceCmd(), "check", "machine")
	if err != nil || !strings.Contains(out, "machine: limits: machine says (provider machine):") ||
		!strings.Contains(out, "machine: limits:   load  12.5% used (target 80%)") {
		t.Fatalf("check: %q %v", out, err)
	}
	if got := asks(t, asked); got != "ran" {
		t.Fatalf("check asked %q, want the source once", got)
	}
}

// The built-in machine is shown, listed and checked where the yard
// declares none of its name, and removing it is refused: each says it is
// built in. A declared source named machine wins over it while it stands,
// and removing that one brings the built-in back.
func TestBuiltinMachineSourceShownCheckedListedAndRemoved(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	out, err := run(t, a.sourceCmd(), "show", "machine")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"machine  no calls, built in\n",
		"  Load, swap, disk and memory of this machine, as limits windows.\n",
		"  credential: none: the yard answers it itself\n",
		"  limits: the yard answers what the source itself carries, in process (yardr limits)\n",
		"  built in: no file declares it; a declared source named machine wins over it (yardr source add)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show printed no %q:\n%s", want, out)
		}
	}
	a.json = true
	out, err = run(t, a.sourceCmd(), "show", "machine")
	a.json = false
	var shown struct {
		Builtin bool `json:"builtin"`
		Source  struct {
			Name    string `json:"name"`
			Builtin bool   `json:"builtin"`
		} `json:"source"`
		Declaration struct {
			Name   string `json:"name"`
			Limits struct {
				Kinds map[string]string `json:"kinds"`
			} `json:"limits"`
		} `json:"declaration"`
	}
	if err != nil || json.Unmarshal([]byte(out), &shown) != nil || !shown.Builtin ||
		shown.Source.Name != "machine" || !shown.Source.Builtin || shown.Declaration.Name != "machine" {
		t.Fatalf("show --json: %q %v", out, err)
	}
	if out, err = run(t, a.sourceCmd(), "list"); err != nil || !strings.Contains(out, "machine (built in)") || !strings.Contains(out, "itself") {
		t.Fatalf("list: %q %v", out, err)
	}
	a.json = true
	out, err = run(t, a.sourceCmd(), "list")
	a.json = false
	var listed []struct {
		Name    string `json:"name"`
		Builtin bool   `json:"builtin"`
	}
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || len(listed) != 1 ||
		listed[0].Name != "machine" || !listed[0].Builtin {
		t.Fatalf("list --json: %q %v", out, err)
	}
	out, err = run(t, a.sourceCmd(), "check", "machine")
	if err != nil || !strings.Contains(out, "machine: built in: no credential, no check call\n") ||
		!strings.Contains(out, "machine: limits: machine says (provider machine):\n") ||
		!strings.Contains(out, "machine: limits:   load  ") {
		t.Fatalf("check: %q %v", out, err)
	}
	if _, err = run(t, a.sourceCmd(), "check", "machine", "--as", "ada"); err == nil ||
		!strings.Contains(err.Error(), "source machine is built in: it takes no credential, so --as ada has nothing to read") {
		t.Fatalf("check --as: %v", err)
	}
	if _, err = run(t, a.sourceCmd(), "rm", "machine"); err == nil || err.Error() != "source machine is built in: nothing to remove" {
		t.Fatalf("rm: %v", err)
	}

	// Declared, machine is the declaration's: shown and asked as any
	// source that answers for itself, with no word of built in.
	file, _ := selfStandIn(t, machineAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.sourceCmd(), "show", "machine"); err != nil || strings.Contains(out, "built in") {
		t.Fatalf("show of the declared source: %q %v", out, err)
	}
	if out, err = run(t, a.sourceCmd(), "list"); err != nil || strings.Contains(out, "(built in)") {
		t.Fatalf("list with the declared source: %q %v", out, err)
	}
	out, err = run(t, a.limitsCmd(), "machine")
	if err != nil || !strings.Contains(out, "12.5% used (target 80%)") {
		t.Fatalf("limits machine with the declared source: %q %v", out, err)
	}
	// Removed, the built-in answers again.
	out, err = run(t, a.sourceCmd(), "rm", "machine")
	if err != nil || !strings.Contains(out, "machine: removed;") ||
		!strings.Contains(out, "  machine is built in, and answers for itself again.\n") {
		t.Fatalf("rm of the declared source: %q %v", out, err)
	}
	out, err = run(t, a.limitsCmd(), "machine")
	if err != nil || !strings.Contains(out, "machine says (provider machine):\n") || strings.Contains(out, "target") {
		t.Fatalf("limits machine with the built-in back: %q %v", out, err)
	}
}

// doctor says of a source that answers for itself, and --try asks it.
func TestDoctorLimitsPartAsksASourceThatAnswersForItself(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	a := &app{st: st, actor: "jo"}
	cfg := &config.Config{Home: t.TempDir()}
	file, asked := selfStandIn(t, machineAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	part, err := limitsPart(context.Background(), cfg, st, nil, false)
	if err != nil || !part.Configured || !strings.HasPrefix(part.Detail, "machine answers for itself; not asked (") || len(part.Notes) != 0 {
		t.Fatalf("part = %+v %v", part, err)
	}
	if got := asks(t, asked); got != "" {
		t.Fatalf("doctor without --try asked the source: %q", got)
	}
	part, err = limitsPart(context.Background(), cfg, st, nil, true)
	if err != nil || part.Detail != "machine answers for itself" || len(part.Notes) != 1 ||
		part.Notes[0] != "tried: machine answered for itself: 3 windows; no window is used up" {
		t.Fatalf("part under --try = %+v %v", part, err)
	}
	if got := asks(t, asked); got != "ran" {
		t.Fatalf("doctor --try asked %q, want the source once", got)
	}
}

// doctor says which source answers for which kinds, a program that is not
// there, a kind in use that nothing lists and a provider setting no source
// answers for, and asks no provider: that is --try's, once per provider.
func TestDoctorLimitsPart(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "jo"}
	cfg := &config.Config{Home: t.TempDir()}
	uses := []limitUse{
		{kind: "kimi", by: "group builders"},
		{kind: "pi", provider: "moonshot", by: "group zap"},
		{kind: "codex", by: "the yard's default"},
		{kind: "codex", by: "crew member scribe"},
		{kind: "pi", provider: "zai", by: "group nightshift"},
		{kind: "grok", provider: "zai", by: "crew member watcher"},
	}
	part, err := limitsPart(ctx, cfg, st, uses, true)
	if err != nil || !part.Configured || part.Detail != "machine (built in) answers for itself" || len(part.Notes) != 6 ||
		part.Notes[0] != "group zap names provider moonshot and no source answers for it (limits.kinds)" ||
		part.Notes[1] != "group nightshift names provider zai and no source answers for it (limits.kinds)" ||
		part.Notes[2] != "crew member watcher names provider zai and no source answers for it (limits.kinds)" ||
		part.Notes[3] != "kimi is in use (group builders) and no source says its limits (limits.kinds)" ||
		part.Notes[4] != "codex is in use (the yard's default, crew member scribe) and no source says its limits (limits.kinds)" ||
		!strings.HasPrefix(part.Notes[5], "tried: machine (built in) answered for itself: 4 windows; ") {
		t.Fatalf("no source: %+v %v", part, err)
	}
	file, asked := limitsStandIn(t, kimiAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	part, err = limitsPart(ctx, cfg, st, uses, false)
	if err != nil || !part.Configured || !strings.HasPrefix(part.Detail, "machine (built in) answers for itself; quokka for claude, kimi; not asked (") || len(part.Notes) != 3 ||
		part.Notes[0] != "group nightshift names provider zai and no source answers for it (limits.kinds)" ||
		part.Notes[1] != "crew member watcher names provider zai and no source answers for it (limits.kinds)" ||
		part.Notes[2] != "codex is in use (the yard's default, crew member scribe) and no source says its limits (limits.kinds)" {
		t.Fatalf("part = %+v %v", part, err)
	}
	if got := asks(t, asked); got != "" {
		t.Fatalf("doctor without --try asked the provider: %q", got)
	}
	part, err = limitsPart(ctx, cfg, st, uses, true)
	if err != nil || part.Detail != "machine (built in) answers for itself; quokka for claude, kimi" || len(part.Notes) != 5 ||
		!strings.HasPrefix(part.Notes[0], "tried: quokka answered for provider moonshot (group builders, group zap): 2 windows; an agent burning moonshot waits for the 5h window, which resets ") ||
		part.Notes[1] != "group nightshift names provider zai and no source answers for it (limits.kinds)" ||
		part.Notes[3] != "codex is in use (the yard's default, crew member scribe) and no source says its limits (limits.kinds)" ||
		!strings.HasPrefix(part.Notes[4], "tried: machine (built in) answered for itself: 4 windows; ") {
		t.Fatalf("part under --try = %+v %v", part, err)
	}
	if got := asks(t, asked); got != "moonshot" {
		t.Fatalf("doctor --try asked %q, want the one resolved provider, once", got)
	}
	// The program is gone: said without running anything.
	os.Remove(filepath.Join(filepath.Dir(file), "provider"))
	part, err = limitsPart(ctx, cfg, st, uses[:1], false)
	if err != nil || len(part.Notes) != 1 || !strings.Contains(part.Notes[0], "source quokka gives no answer as this command finds it: limits command ") ||
		!strings.Contains(part.Notes[0], "no such file or directory") {
		t.Fatalf("part with the program gone = %+v %v", part, err)
	}
}

// testdata/aiquokka.yaml is the file of the design (yardr-m71s), whole, and
// docs/integrations.md gives it as the example: what a reader copies from
// the page is what is added here.
func TestAiquokkaFileOfTheDocs(t *testing.T) {
	t.Parallel()
	text, err := os.ReadFile("testdata/aiquokka.yaml")
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile("../../docs/integrations.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "```yaml\n"+string(text)+"```\n") {
		t.Fatalf("docs/integrations.md has no yaml block that is testdata/aiquokka.yaml:\n%s", text)
	}
	d, err := source.Parse("testdata/aiquokka.yaml", text)
	if err != nil || d.Limits == nil {
		t.Fatalf("declaration = %+v, %v", d, err)
	}
	if argv, _ := d.Limits.Argv("kimi"); strings.Join(argv, " ") != "aiquokka kimi --json" {
		t.Fatalf("the command for kimi = %q", argv)
	}
	a := &app{st: storetest.New(t), actor: "jo"}
	out, err := run(t, a.sourceCmd(), "add", "testdata/aiquokka.yaml")
	if err != nil || !strings.Contains(out, "the subscription of claude, codex, copilot, grok, kimi, kiro still carries") {
		t.Fatalf("source add: %q %v", out, err)
	}
}

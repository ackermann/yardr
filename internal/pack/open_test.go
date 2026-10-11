package pack_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

// has gives the yard what it takes to be asked about a source: a hook that
// receives it, and its credential's name in the crew member's env_file. The
// crew member is made when the yard lacks it, and keeps what it may open.
func has(t *testing.T, st store.API, member string, sources ...string) {
	t.Helper()
	keys := map[string]string{"linear": "LINEAR_API_KEY", "github": "GITHUB_TOKEN", "acme": "ACME_TOKEN", "pushover": "PUSHOVER_TOKEN"}
	r, err := st.GetCrew(ctx, member)
	if err != nil {
		r = &store.CrewMember{Name: member, Runner: "herdr", Config: map[string]any{"kind": "claude"}}
	}
	env, _ := r.Config["env_file"].(string)
	if env == "" {
		env = filepath.Join(t.TempDir(), "env")
		r.Config["env_file"] = env
		if _, err := st.PutCrew(ctx, *r, "me"); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(env, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, src := range sources {
		if _, err := f.WriteString(keys[src] + "=secret-" + src + "\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutHook(ctx, store.Hook{Name: src, Source: src, SecretFile: filepath.Join(t.TempDir(), "secret")}, "me"); err != nil {
			t.Fatal(err)
		}
		if src == "linear" {
			withLinear(t, st)
		}
	}
}

// open is the plan's entry for what the pack asks crew member may open.
func open(t *testing.T, p *pack.Plan, member string) pack.OpenThreads {
	t.Helper()
	i := slices.IndexFunc(p.OpenThreads, func(o pack.OpenThreads) bool { return o.CrewMember == member })
	if i < 0 {
		t.Fatalf("the plan asks nothing for %s: %+v", member, p.OpenThreads)
	}
	return p.OpenThreads[i]
}

// applyOpen applies the plan with the operator's answers to what it asks
// about opening threads.
func applyOpen(t *testing.T, st store.API, p *pack.Plan, answers map[string]map[string]bool) *pack.Result {
	t.Helper()
	a := pack.Answers{}
	for _, q := range p.Questions {
		a[q.ID] = map[string]string{"kind": "claude", "dir": t.TempDir()}[q.Field]
	}
	res, err := pack.Apply(ctx, st, p, pack.ApplyOptions{Answers: a, OpenThreads: answers, Actor: "jo"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func mayOpen(t *testing.T, st store.API, member string) string {
	t.Helper()
	r, err := st.GetCrew(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(r.MayOpen(), ",")
}

// A plan sorts the sources a pack asks for: what the crew member may already,
// what the yard has no hook or credential for, and what is left to ask the
// operator. Building it grants nothing.
func TestPlanSortsWhatThePackAsksToOpen(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	dir := copyStarter(t)
	edit(t, dir, "open_threads: [linear]", "open_threads: [linear, github]")

	// A crew member the import would create has no credential yet, and the
	// yard no hook: nothing can be asked.
	ot := open(t, build(t, st, load(t, dir), o), "boss")
	if ot.Pack != "yardmaster" || !reflect.DeepEqual(ot.Wants, []string{"linear", "github"}) || len(ot.Has)+len(ot.Asks)+len(ot.Declined) != 0 || len(ot.Lacks) != 2 {
		t.Fatalf("into an empty yard: %+v", ot)
	}
	what := func(l pack.Lack) string {
		var out []string
		for _, n := range l.Missing {
			out = append(out, n.What)
		}
		return l.Source + ": " + strings.Join(out, ",")
	}
	if got := what(ot.Lacks[0]); got != "linear: source" {
		t.Fatalf("lacks %s", got)
	}
	if d := ot.Lacks[0].Missing[0].Detail; !strings.Contains(d, "the yard has no source linear (yardr pack apply linear brings it); the next apply asks") {
		t.Fatalf("lack detail = %q", d)
	}

	// Set up for linear alone: linear is a question, github still is not,
	// and each of hook and credential is named when it alone is missing.
	has(t, st, "boss", "linear")
	ot = open(t, build(t, st, load(t, dir), o), "boss")
	if !reflect.DeepEqual(ot.Asks, []string{"linear"}) || len(ot.Lacks) != 1 || what(ot.Lacks[0]) != "github: hook,credential" {
		t.Fatalf("set up for linear: %+v", ot)
	}
	if _, err := st.PutHook(ctx, store.Hook{Name: "gh", Source: "github", SecretFile: "/nowhere"}, "me"); err != nil {
		t.Fatal(err)
	}
	if ot = open(t, build(t, st, load(t, dir), o), "boss"); len(ot.Lacks) != 1 || what(ot.Lacks[0]) != "github: credential" {
		t.Fatalf("with a hook for github: %+v", ot)
	}

	// A GitHub App's two names stand in for GITHUB_TOKEN: one of them alone
	// is no credential, nor is the pair with a key file open to others, and
	// with a key only its owner reads github is a question like linear.
	boss, err := st.GetCrew(ctx, "boss")
	if err != nil {
		t.Fatal(err)
	}
	env, key := boss.Config["env_file"].(string), filepath.Join(t.TempDir(), "yardr.pem")
	credential := func(lines string) pack.OpenThreads {
		t.Helper()
		if err := os.WriteFile(env, []byte("LINEAR_API_KEY=secret-linear\n"+lines), 0o600); err != nil {
			t.Fatal(err)
		}
		return open(t, build(t, st, load(t, dir), o), "boss")
	}
	lack := func(ot pack.OpenThreads, detail string) {
		t.Helper()
		if len(ot.Lacks) != 1 || what(ot.Lacks[0]) != "github: credential" || !strings.Contains(ot.Lacks[0].Missing[0].Detail, detail) {
			t.Fatalf("want github's credential missing with %q: %+v", detail, ot)
		}
	}
	lack(credential("GITHUB_APP_ID=4242\n"), "GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE, or GITHUB_TOKEN, in crew member boss's env_file")
	if _, err := standin.NewApp("4242", "yardr", key); err != nil {
		t.Fatal(err)
	}
	pair := "GITHUB_APP_ID=4242\nGITHUB_APP_PRIVATE_KEY_FILE=" + key + "\n"
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	lack(credential(pair), "open to others")
	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatal(err)
	}
	if ot = credential(pair); !reflect.DeepEqual(ot.Asks, []string{"linear", "github"}) || len(ot.Lacks) != 0 {
		t.Fatalf("with a GitHub App's pair: %+v", ot)
	}
	if ot = credential("GITHUB_TOKEN=secret-github\n"); !reflect.DeepEqual(ot.Asks, []string{"linear", "github"}) || len(ot.Lacks) != 0 {
		t.Fatalf("with GITHUB_TOKEN: %+v", ot)
	}
	credential("")

	// The credential from a provider, the crew member's env_command, counts as
	// the env_file's does: the pair it prints, a provider that fails, and
	// both with the name in the file alone.
	provider := filepath.Join(t.TempDir(), "provider")
	provided := func(body string, cfg map[string]any) pack.OpenThreads {
		t.Helper()
		if err := os.WriteFile(provider, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		def := *boss
		def.Revision = ""
		def.Config = map[string]any{"kind": "claude", "env_command": provider}
		for k, v := range cfg {
			def.Config[k] = v
		}
		if _, err := st.PutCrew(ctx, def, "me"); err != nil {
			t.Fatal(err)
		}
		return open(t, build(t, st, load(t, dir), o), "boss")
	}
	both := `echo "LINEAR_API_KEY=secret-linear"; echo "GITHUB_APP_ID=4242"; echo "GITHUB_APP_PRIVATE_KEY_FILE=` + key + `"`
	if ot = provided(both, nil); !reflect.DeepEqual(ot.Asks, []string{"linear", "github"}) || len(ot.Lacks) != 0 {
		t.Fatalf("with a provider's pair: %+v", ot)
	}
	if ot = provided(both+`; echo "vault is locked" >&2; exit 3`, nil); len(ot.Lacks) != 2 ||
		!strings.Contains(ot.Lacks[0].Missing[0].Detail, "env_command "+provider+": exit status 3; its stderr ended: vault is locked") ||
		strings.Contains(fmt.Sprint(ot), "secret-linear") {
		t.Fatalf("with a provider that fails: %+v", ot)
	}
	lack(provided(`echo "GITHUB_APP_ID=4242"`, map[string]any{"env_file": env}), "env_file "+env+" with env_command "+provider+" does not set it")
	if ot = provided(`echo "GITHUB_TOKEN=secret-github"`, map[string]any{"env_file": env}); !reflect.DeepEqual(ot.Asks, []string{"linear", "github"}) || len(ot.Lacks) != 0 {
		t.Fatalf("with linear's in the file and github's from the provider: %+v", ot)
	}
	boss.Revision = ""
	if _, err := st.PutCrew(ctx, *boss, "me"); err != nil {
		t.Fatal(err)
	}

	// What the crew member may already is no question.
	if _, err := st.AllowOpening(ctx, "boss", []string{"linear"}, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if ot = open(t, build(t, st, load(t, dir), o), "boss"); !reflect.DeepEqual(ot.Has, []string{"linear"}) || len(ot.Asks) != 0 {
		t.Fatalf("with linear allowed by hand: %+v", ot)
	}

	// A pack without the key has no entry at all.
	edit(t, dir, "    open_threads: [linear, github]\n", "")
	if p := build(t, st, load(t, dir), o); len(p.OpenThreads) != 0 {
		t.Fatalf("a pack that asks for nothing: %+v", p.OpenThreads)
	}
}

// An import grants on a yes and only then, adds to what the crew member may
// and takes nothing away, logs the grant, and records each answer: the
// second import asks nothing and changes nothing, a no is not asked again,
// and a source the pack asks for later is the only new question.
func TestApplyGrantsWhatTheOperatorAnswered(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	dir := copyStarter(t)
	has(t, st, "boss", "linear", "github")
	if _, err := st.AllowOpening(ctx, "boss", []string{"github"}, "", "jo"); err != nil {
		t.Fatal(err)
	}

	// Without an answer nothing is granted, and nothing is recorded as
	// answered: the next import still asks.
	res := applyOpen(t, st, build(t, st, load(t, dir), o), nil)
	if len(res.Granted) != 0 || mayOpen(t, st, "boss") != "github" || record(t, st).OpenThreads != nil {
		t.Fatalf("unanswered: granted %v, may open %s, record %v", res.Granted, mayOpen(t, st, "boss"), record(t, st).OpenThreads)
	}
	plan := build(t, st, load(t, dir), o)
	if ot := open(t, plan, "boss"); !reflect.DeepEqual(ot.Asks, []string{"linear"}) {
		t.Fatalf("after an import without an answer: %+v", ot)
	}

	// Yes: linear is added, github stays, and the log says who granted
	// what to whom for which pack.
	h := head(t, st)
	res = applyOpen(t, st, plan, map[string]map[string]bool{"boss": {"linear": true}})
	if want := []pack.Grant{{CrewMember: "boss", Sources: []string{"linear"}}}; !reflect.DeepEqual(res.Granted, want) || !res.Wrote() || !res.Recorded {
		t.Fatalf("granted %+v, wrote %v, recorded %v", res.Granted, res.Wrote(), res.Recorded)
	}
	if got := mayOpen(t, st, "boss"); got != "github,linear" {
		t.Fatalf("boss may open %s", got)
	}
	evs, _ := st.EventsSince(ctx, h, 0)
	if len(evs) != 2 || evs[0].Kind != "open_threads_granted" || evs[0].Actor != "jo" || evs[0].Data["pack"] != "starter" ||
		evs[0].Data["crew"] != "boss" || !reflect.DeepEqual(evs[0].Data["sources"], []any{"linear"}) || evs[1].Kind != "pack_applied" {
		t.Fatalf("events = %+v", evs)
	}
	if got, want := record(t, st).OpenThreads, map[string]map[string]bool{"boss": {"linear": true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %v", got)
	}

	// Twice is once.
	plan = build(t, st, load(t, dir), o)
	if ot := open(t, plan, "boss"); len(ot.Asks) != 0 || !reflect.DeepEqual(ot.Has, []string{"linear"}) {
		t.Fatalf("second import: %+v", ot)
	}
	h = head(t, st)
	if res = applyOpen(t, st, plan, nil); res.Wrote() || res.Recorded || !reflect.DeepEqual(kinds(t, st, h), []string{"pack_unchanged"}) {
		t.Fatalf("second import: wrote %v, recorded %v, events %v", res.Wrote(), res.Recorded, kinds(t, st, h))
	}

	// The pack asks for a further source: that one alone is asked. No is
	// recorded, grants nothing, and is not asked again.
	if _, err := st.PutCrew(ctx, func() store.CrewMember {
		r, _ := st.GetCrew(ctx, "boss")
		r.Config[store.OpenThreads] = "linear"
		return *r
	}(), "jo"); err != nil {
		t.Fatal(err)
	}
	edit(t, dir, "open_threads: [linear]", "open_threads: [linear, github]")
	plan = build(t, st, load(t, dir), o)
	if ot := open(t, plan, "boss"); !reflect.DeepEqual(ot.Asks, []string{"github"}) {
		t.Fatalf("a further source: %+v", ot)
	}
	res = applyOpen(t, st, plan, map[string]map[string]bool{"boss": {"github": false}})
	if len(res.Granted) != 0 || mayOpen(t, st, "boss") != "linear" || !res.Recorded {
		t.Fatalf("no: granted %v, may open %s, recorded %v", res.Granted, mayOpen(t, st, "boss"), res.Recorded)
	}
	plan = build(t, st, load(t, dir), o)
	if ot := open(t, plan, "boss"); len(ot.Asks) != 0 || !reflect.DeepEqual(ot.Declined, []string{"github"}) {
		t.Fatalf("after a no: %+v", ot)
	}
	h = head(t, st)
	if res = applyOpen(t, st, plan, nil); res.Wrote() || !reflect.DeepEqual(kinds(t, st, h), []string{"pack_unchanged"}) {
		t.Fatalf("after a no, again: wrote %v, events %v", res.Wrote(), kinds(t, st, h))
	}
	// The operator may still say yes to what was declined.
	res = applyOpen(t, st, plan, map[string]map[string]bool{"boss": {"github": true}})
	if len(res.Granted) != 1 || mayOpen(t, st, "boss") != "github,linear" || !record(t, st).OpenThreads["boss"]["github"] {
		t.Fatalf("yes after a no: %+v, may open %s", res.Granted, mayOpen(t, st, "boss"))
	}

	// A yes the operator took back by hand is not given again unasked: the
	// record forgets it and the import asks.
	r, _ := st.GetCrew(ctx, "boss")
	delete(r.Config, store.OpenThreads)
	if _, err := st.PutCrew(ctx, *r, "jo"); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	if ot := open(t, plan, "boss"); !reflect.DeepEqual(ot.Asks, []string{"linear", "github"}) {
		t.Fatalf("after the setting was removed: %+v", ot)
	}
	if res = applyOpen(t, st, plan, nil); len(res.Granted) != 0 || mayOpen(t, st, "boss") != "" || record(t, st).OpenThreads != nil {
		t.Fatalf("after the setting was removed: granted %v, may open %q, record %v", res.Granted, mayOpen(t, st, "boss"), record(t, st).OpenThreads)
	}
}

// An answer is to a question the plan asks. One for a source the yard
// lacks, one the pack does not name, and one for another crew member are
// refused before anything is written.
func TestApplyRefusesAnAnswerThePlanDidNotAsk(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	dir := copyStarter(t)
	edit(t, dir, "open_threads: [linear]", "open_threads: [linear, github]")
	has(t, st, "boss", "linear")
	plan := build(t, st, load(t, dir), o)
	a := pack.Answers{}
	for _, q := range plan.Questions {
		a[q.ID] = map[string]string{"kind": "claude", "dir": t.TempDir()}[q.Field]
	}
	for _, c := range []struct {
		answers map[string]map[string]bool
		want    string
	}{
		{map[string]map[string]bool{"boss": {"github": true}}, "does not ask whether crew member boss may open threads in github"},
		{map[string]map[string]bool{"boss": {"github": false}}, "does not ask whether crew member boss may open threads in github"},
		{map[string]map[string]bool{"boss": {"token": true}}, "may open threads in token"},
		{map[string]map[string]bool{"scout": {"linear": true}}, "does not ask whether crew member scout may open threads in linear"},
	} {
		h := head(t, st)
		_, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, OpenThreads: c.answers, Actor: "jo"})
		if err == nil || !strings.Contains(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v: %v", c.answers, err)
		}
		if got := kinds(t, st, h); len(got) != 0 || mayOpen(t, st, "boss") != "" {
			t.Fatalf("%v: a refused import logged %v, and boss may open %q", c.answers, got, mayOpen(t, st, "boss"))
		}
	}
}

// Opening threads in a source the yard has nothing to open with is asked
// about nowhere: the plan lacks the source, and the next apply asks once
// the yard has it.
func TestOpenThreadsWaitsForTheYardsSource(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	yaml := strings.Replace(small, "    subscribe:\n      linear: involved\n", "    subscribe:\n      linear: involved\n    open_threads: [acme]\n", 1)
	p := load(t, writePack(t, smallFiles(yaml)))

	ot := open(t, build(t, st, p, o), "boss")
	if !reflect.DeepEqual(ot.Wants, []string{"acme"}) || len(ot.Asks) != 0 || len(ot.Lacks) != 1 {
		t.Fatalf("into a yard without the source: %+v", ot)
	}
	if l := ot.Lacks[0]; l.Source != "acme" || len(l.Missing) != 1 || l.Missing[0].What != "source" ||
		!strings.Contains(l.Missing[0].Detail, "the yard has no source acme") {
		t.Fatalf("lacks = %+v", ot.Lacks)
	}

	if _, err := st.PutSource(ctx, "acme.yaml", cmdDecl, "me"); err != nil {
		t.Fatal(err)
	}
	ot = open(t, build(t, st, p, o), "boss")
	if len(ot.Lacks) != 1 || len(ot.Lacks[0].Missing) != 2 {
		t.Fatalf("with the source but no hook: %+v", ot)
	}
	has(t, st, "boss", "acme")
	ot = open(t, build(t, st, p, o), "boss")
	if !reflect.DeepEqual(ot.Asks, []string{"acme"}) || len(ot.Lacks) != 0 {
		t.Fatalf("set up for acme: %+v", ot)
	}
}

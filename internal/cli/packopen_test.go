package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// openPack is the starter pack in a directory of the test's, asking that
// its mayor may open threads in sources.
func openPack(t *testing.T, sources string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../packs/starter")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "scripts", "merge-local"), 0o755); err != nil {
		t.Fatal(err)
	}
	setOpenThreads(t, dir, "    open_threads: [linear]\n", sources)
	return dir
}

// setOpenThreads replaces the pack's open_threads line, old, by one asking
// for sources; "" drops the key.
func setOpenThreads(t *testing.T, dir, old, sources string) string {
	t.Helper()
	path := filepath.Join(dir, "pack.yaml")
	text, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(text), old) {
		t.Fatalf("pack.yaml lacks %q: %v", old, err)
	}
	line := ""
	if sources != "" {
		line = "    open_threads: [" + sources + "]\n"
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(text), old, line, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return line
}

// openApp is packApp with the operator me, who runs the command, and the
// resident boss; yardHas sets the yard up for sources.
func openApp(t *testing.T, sources ...string) *app {
	t.Helper()
	a := packApp(t)
	ctx := context.Background()
	if err := a.st.SetSetting(ctx, "operator", "me", "me"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(env, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	def := store.Resident{Name: "boss", Runner: "herdr", Dir: t.TempDir(), Config: map[string]any{"kind": "claude", "env_file": env}}
	if _, err := a.st.PutResident(ctx, def, "me"); err != nil {
		t.Fatal(err)
	}
	yardHas(t, a, sources...)
	return a
}

// yardHas gives the yard a hook for each source and boss its credential.
func yardHas(t *testing.T, a *app, sources ...string) {
	t.Helper()
	ctx := context.Background()
	boss, err := a.st.GetResident(ctx, "boss")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(boss.Config["env_file"].(string), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, src := range sources {
		key := map[string]string{"linear": "LINEAR_API_KEY", "github": "GITHUB_TOKEN"}[src]
		if _, err := f.WriteString(key + "=not-a-real-one\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.st.PutHook(ctx, store.Hook{Name: src, Source: src, SecretFile: filepath.Join(t.TempDir(), "secret")}, "me"); err != nil {
			t.Fatal(err)
		}
	}
}

func bossMayOpen(t *testing.T, a *app) string {
	t.Helper()
	r, err := a.st.GetResident(context.Background(), "boss")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(r.MayOpen(), ",")
}

func openRecord(t *testing.T, a *app) map[string]map[string]bool {
	t.Helper()
	recs, err := a.st.Packs(context.Background())
	if err != nil || len(recs) != 1 {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	return recs[0].OpenThreads
}

// answerForms answers every form: the open-threads question with yes when
// say is true, everything else with its default. It returns the titles of
// the confirmations asked, in order, and fails on one whose default is yes
// for opening threads.
func answerForms(t *testing.T, say *bool) *[]string {
	t.Helper()
	var asked []string
	stubAsk(t, func(fields ...huh.Field) error {
		for _, f := range fields {
			c, ok := f.(*huh.Confirm)
			if !ok {
				continue
			}
			title := strings.TrimSpace(strings.SplitN(c.View(), "\n", 2)[0])
			asked = append(asked, title)
			if !strings.Contains(title, "open threads") {
				continue
			}
			if c.GetValue() != false {
				t.Errorf("%q defaults to yes", title)
			}
			if *say {
				c.WithKeyMap(huh.NewDefaultKeyMap())
				c.Focus()
				c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
			}
		}
		return nil
	})
	return &asked
}

// In a terminal the operator is asked once per resident, after the
// import's other questions and with no as the default, and only about a
// source the yard has a hook and a credential for: the plan names what the
// other lacks. Yes sets it on the resident and is logged; the second apply
// asks nothing and changes nothing; once the yard has the other source the
// next apply asks about that one alone, and a no is not asked again.
func TestPackApplyAsksTheOperatorAboutOpeningThreads(t *testing.T) {
	a := openApp(t, "linear")
	setInteractive(t, true)
	ctx := context.Background()
	dir := openPack(t, "linear, github")
	say := true
	asked := answerForms(t, &say)
	args := append([]string{"apply", dir, "--rig", "web"}, starterFlags...)

	out, err := run(t, a.packCmd(), "check", dir, "--rig", "web", "--resident", "mayor=boss")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\nopen threads, for resident boss\n  the pack asks for linear, github\n  asks      linear: the operator is asked, and the answer without one is no\n",
		"  the pack wants github, this yard lacks hook and credential: nothing is asked or granted\n    missing  hook         no hook receives github",
		"    missing  credential   GITHUB_TOKEN in resident boss's env_file",
		"\nan import would ask\n",
		"  the operator: let boss open threads in linear (--open-threads mayor=linear, or =none)? [no]\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	if bossMayOpen(t, a) != "" {
		t.Fatal("pack check granted something")
	}

	head, _ := a.st.Head(ctx)
	if out, err = run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := len(*asked); n < 2 || (*asked)[n-1] != "Let boss open threads in linear?" || !strings.HasPrefix((*asked)[n-2], "Apply pack starter") {
		t.Fatalf("asked %q, want the question about linear alone, once, after the confirmation", *asked)
	}
	if !strings.Contains(out, "\nresident boss may now open threads in linear\n") || bossMayOpen(t, a) != "linear" {
		t.Fatalf("boss may open %q:\n%s", bossMayOpen(t, a), out)
	}
	if show, err := run(t, a.residentCmd(), "show", "boss"); err != nil || !strings.Contains(show, "  may open threads in linear\n") {
		t.Fatalf("resident show: %v\n%s", err, show)
	}
	var grant *store.Event
	evs, _ := a.st.EventsSince(ctx, head, 0)
	for i, e := range evs {
		if e.Kind == "open_threads_granted" {
			grant = &evs[i]
		}
	}
	if grant == nil || grant.Actor != "me" || grant.Data["pack"] != "starter" || grant.Data["resident"] != "boss" || len(grant.Data["sources"].([]any)) != 1 {
		t.Fatalf("the grant in the log: %+v", grant)
	}
	if got := openRecord(t, a); len(got) != 1 || !got["boss"]["linear"] || len(got["boss"]) != 1 {
		t.Fatalf("record = %v: github, which the yard lacks, must not be answered", got)
	}

	// Twice is once.
	*asked = nil
	head, _ = a.st.Head(ctx)
	if out, err = run(t, a.packCmd(), args...); err != nil || !strings.Contains(out, "nothing changed") || len(*asked) != 0 {
		t.Fatalf("second apply: %v, asked %q\n%s", err, *asked, out)
	}
	if !strings.Contains(out, "  has       linear: it may already, and nothing is asked\n") {
		t.Errorf("the plan does not say boss has linear:\n%s", out)
	}
	if evs, _ := a.st.EventsSince(ctx, head, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" {
		t.Fatalf("the second apply logged %+v", evs)
	}

	// The yard has github now: that one is asked. No grants nothing and is
	// recorded, so it is not asked a second time.
	yardHas(t, a, "github")
	say = false
	if out, err = run(t, a.packCmd(), args...); err != nil || len(*asked) != 1 || (*asked)[0] != "Let boss open threads in github?" {
		t.Fatalf("with github set up: %v, asked %q\n%s", err, *asked, out)
	}
	if got := openRecord(t, a); bossMayOpen(t, a) != "linear" || got["boss"]["github"] || len(got["boss"]) != 2 {
		t.Fatalf("after a no: boss may open %q, record %v", bossMayOpen(t, a), got)
	}
	if !strings.Contains(out, "resident boss was not let open threads in github: the no is recorded and not asked again (--open-threads mayor=github grants it after all)\n") {
		t.Errorf("a no is not said to be recorded:\n%s", out)
	}
	*asked = nil
	head, _ = a.st.Head(ctx)
	if out, err = run(t, a.packCmd(), args...); err != nil || len(*asked) != 0 ||
		!strings.Contains(out, "  declined  github: an earlier import was told no (--open-threads mayor=<source> grants it)\n") {
		t.Fatalf("after a no, again: %v, asked %q\n%s", err, *asked, out)
	}
	if evs, _ := a.st.EventsSince(ctx, head, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" {
		t.Fatalf("after a no, the next apply logged %+v", evs)
	}
	if list, _ := run(t, a.packCmd(), "list"); !strings.Contains(list, "  may open   boss: github no, linear yes\n") {
		t.Errorf("pack list:\n%s", list)
	}
}

// Without a terminal, with --json and with --yes nothing is asked, and
// without --open-threads nothing is granted: the import goes through and
// names the flag. The flag grants what it names of what the yard has, says
// what a source it names lacks, and says no to what it leaves out.
func TestPackApplyOpenThreadsFlag(t *testing.T) {
	ctx := context.Background()
	dir := openPack(t, "linear, github")
	args := append([]string{"apply", dir, "--rig", "web", "--yes"}, starterFlags...)
	const names = "resident boss was not let open threads in linear: nothing is asked here, and the answer without a flag is no. --open-threads mayor=linear grants it, --open-threads mayor=none says no for good"

	t.Run("no flag", func(t *testing.T) {
		a := openApp(t, "linear")
		setInteractive(t, false)
		out, err := run(t, a.packCmd(), args...)
		if err != nil || !strings.Contains(out, "applied pack starter to rig web") || !strings.Contains(out, names) {
			t.Fatalf("%v\n%s", err, out)
		}
		if bossMayOpen(t, a) != "" || openRecord(t, a) != nil {
			t.Fatalf("boss may open %q, record %v", bossMayOpen(t, a), openRecord(t, a))
		}
		// --yes in a terminal asks nothing either.
		setInteractive(t, true)
		say := true
		asked := answerForms(t, &say)
		if out, err = run(t, a.packCmd(), args...); err != nil || len(*asked) != 0 || !strings.Contains(out, names) || bossMayOpen(t, a) != "" {
			t.Fatalf("--yes in a terminal: %v, asked %q, may open %q\n%s", err, *asked, bossMayOpen(t, a), out)
		}
		// And --json, in a terminal too, names the flag as data.
		a.json = true
		out, err = run(t, a.packCmd(), args...)
		var got struct {
			Open struct {
				Answers map[string]map[string]bool `json:"answers"`
				Notes   []string                   `json:"notes"`
			} `json:"open_threads"`
			Plan   pack.Plan   `json:"plan"`
			Result pack.Result `json:"result"`
		}
		if err != nil || json.Unmarshal([]byte(out), &got) != nil || len(got.Open.Notes) != 1 || got.Open.Notes[0] != names ||
			len(got.Open.Answers) != 0 || len(got.Result.Granted) != 0 || len(got.Plan.OpenThreads) != 1 || got.Plan.OpenThreads[0].Asks[0] != "linear" {
			t.Fatalf("json: %v\n%s", err, out)
		}
	})

	t.Run("the flag", func(t *testing.T) {
		a := openApp(t, "linear")
		setInteractive(t, false)
		// Without --yes a grant is a change like any other.
		noYes := append([]string{"apply", dir, "--rig", "web", "--open-threads", "mayor=linear"}, starterFlags...)
		if _, err := run(t, a.packCmd(), noYes...); err == nil || !strings.Contains(err.Error(), "give --yes") {
			t.Fatalf("without --yes: %v", err)
		}
		out, err := run(t, a.packCmd(), append(args, "--open-threads", "mayor=linear,github")...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for _, want := range []string{
			"  grants    linear: --open-threads says so\n",
			"\nresident boss may now open threads in linear\n",
			"--open-threads mayor=github grants nothing: the pack wants github, this yard lacks hook and credential",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "an import would ask") {
			t.Errorf("the flag answered, and the plan still lists a question:\n%s", out)
		}
		if got := openRecord(t, a); bossMayOpen(t, a) != "linear" || len(got["boss"]) != 1 || !got["boss"]["linear"] {
			t.Fatalf("boss may open %q, record %v", bossMayOpen(t, a), got)
		}
		// The same command again is no error and changes nothing.
		head, _ := a.st.Head(ctx)
		if out, err = run(t, a.packCmd(), append(args, "--open-threads", "mayor=linear,github")...); err != nil || !strings.Contains(out, "nothing changed") {
			t.Fatalf("again: %v\n%s", err, out)
		}
		if evs, _ := a.st.EventsSince(ctx, head, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" {
			t.Fatalf("again logged %+v", evs)
		}
		// github was not recorded as answered: with the yard set up for it,
		// an import names the flag for it.
		yardHas(t, a, "github")
		if out, err = run(t, a.packCmd(), args...); err != nil || !strings.Contains(out, "--open-threads mayor=github grants it") {
			t.Fatalf("with github set up: %v\n%s", err, out)
		}
		// A flag that leaves a source out says no to it.
		if out, err = run(t, a.packCmd(), append(args, "--open-threads", "mayor=linear")...); err != nil || !strings.Contains(out, "  declines  github: --open-threads leaves it out\n") {
			t.Fatalf("leaving github out: %v\n%s", err, out)
		}
		if got := openRecord(t, a); bossMayOpen(t, a) != "linear" || got["boss"]["github"] || len(got["boss"]) != 2 {
			t.Fatalf("boss may open %q, record %v", bossMayOpen(t, a), got)
		}
		// And the flag grants what an earlier import was told no for.
		if out, err = run(t, a.packCmd(), append(args, "--open-threads", "mayor=github")...); err != nil || bossMayOpen(t, a) != "github,linear" {
			t.Fatalf("yes after a no: %v, may open %q\n%s", err, bossMayOpen(t, a), out)
		}
	})

	t.Run("none", func(t *testing.T) {
		a := openApp(t, "linear", "github")
		setInteractive(t, false)
		out, err := run(t, a.packCmd(), append(args, "--open-threads", "mayor=none")...)
		if err != nil || strings.Contains(out, "nothing is asked here") || !strings.Contains(out, "was not let open threads in github, linear: the no is recorded") {
			t.Fatalf("%v\n%s", err, out)
		}
		got := openRecord(t, a)
		if bossMayOpen(t, a) != "" || len(got["boss"]) != 2 || got["boss"]["linear"] || got["boss"]["github"] {
			t.Fatalf("boss may open %q, record %v", bossMayOpen(t, a), got)
		}
		// Answered: the next import, flag or none, has nothing to say.
		if out, err = run(t, a.packCmd(), args...); err != nil || !strings.Contains(out, "nothing changed") || strings.Contains(out, "--open-threads mayor=linear") {
			t.Fatalf("after none: %v\n%s", err, out)
		}
	})

	t.Run("refused flags", func(t *testing.T) {
		a := openApp(t, "linear")
		setInteractive(t, false)
		for flag, want := range map[string]string{
			"scout=linear":  "the pack has no resident scout (it has: mayor)",
			"mayor=token":   `the pack does not ask that mayor may open threads in "token" (it asks for: linear, github)`,
			"mayor":         "want <pack's resident>=<source>[,<source>], or =none",
			"mayor=":        "want <pack's resident>=<source>[,<source>], or =none",
			"mayor=linear,": `may open threads in ""`,
		} {
			_, err := run(t, a.packCmd(), append(args, "--open-threads", flag)...)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("--open-threads %s: %v, want %q", flag, err, want)
			}
		}
		if _, err := run(t, a.packCmd(), append(args, "--open-threads", "mayor=linear", "--open-threads", "mayor=none")...); err == nil || !strings.Contains(err.Error(), "answered twice") {
			t.Errorf("the flag twice for one resident: %v", err)
		}
		// A pack that asks for nothing takes no answer.
		plain := openPack(t, "")
		_, err := run(t, a.packCmd(), append([]string{"apply", plain, "--rig", "web", "--yes", "--open-threads", "mayor=linear"}, starterFlags...)...)
		if err == nil || !strings.Contains(err.Error(), "the pack asks for no source its resident mayor may open threads in") {
			t.Errorf("a pack without the key: %v", err)
		}
		if groups, _ := a.st.Groups(ctx); len(groups) != 0 || bossMayOpen(t, a) != "" {
			t.Fatalf("a refused flag changed the yard: %v, may open %q", groups, bossMayOpen(t, a))
		}
	})
}

// Letting a resident open threads is the operator's, by the check yardr
// resident add makes. Run by anyone else, a resident's own session say, the
// import does everything else, grants nothing whatever the flag says,
// records no answer, and names who may grant it. The operator's next apply
// is then asked.
func TestPackApplyOnlyTheOperatorGrants(t *testing.T) {
	a := openApp(t, "linear")
	ctx := context.Background()
	dir := openPack(t, "linear")
	a.actor = "boss"
	args := append([]string{"apply", dir, "--rig", "web", "--yes"}, starterFlags...)
	const says = "resident boss was not let open threads in linear: only the operator (me) may grant that, and this is boss."

	setInteractive(t, false)
	out, err := run(t, a.packCmd(), append(args, "--open-threads", "mayor=linear")...)
	if err != nil || !strings.Contains(out, "applied pack starter to rig web: ") || !strings.Contains(out, says) || strings.Contains(out, "may now open") {
		t.Fatalf("%v\n%s", err, out)
	}
	if g, err := a.st.GetGroup(ctx, "web-builders"); err != nil || g.Config["kind"] != "claude" {
		t.Fatalf("the rest of the import: %+v, %v", g, err)
	}
	if bossMayOpen(t, a) != "" || openRecord(t, a) != nil {
		t.Fatalf("boss may open %q, record %v", bossMayOpen(t, a), openRecord(t, a))
	}
	// Nor may it say no for the operator.
	if out, err = run(t, a.packCmd(), append(args, "--open-threads", "mayor=none")...); err != nil || !strings.Contains(out, says) || openRecord(t, a) != nil {
		t.Fatalf("none from a resident: %v, record %v\n%s", err, openRecord(t, a), out)
	}
	// In a terminal it is not asked, and is told the same.
	setInteractive(t, true)
	say := true
	asked := answerForms(t, &say)
	plain := append([]string{"apply", dir, "--rig", "web"}, starterFlags...)
	head, _ := a.st.Head(ctx)
	if out, err = run(t, a.packCmd(), plain...); err != nil || len(*asked) != 0 || !strings.Contains(out, says) || bossMayOpen(t, a) != "" {
		t.Fatalf("in a terminal: %v, asked %q\n%s", err, *asked, out)
	}
	if evs, _ := a.st.EventsSince(ctx, head, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" {
		t.Fatalf("an apply that granted nothing logged %+v", evs)
	}

	// The operator: asked, and granted.
	a.actor = "me"
	if out, err = run(t, a.packCmd(), plain...); err != nil || len(*asked) != 1 || bossMayOpen(t, a) != "linear" {
		t.Fatalf("the operator: %v, asked %q, may open %q\n%s", err, *asked, bossMayOpen(t, a), out)
	}
}

// The import only adds: a source the resident may open threads in by hand
// stays through a yes, a no, and a pack that does not ask at all.
func TestPackApplyOnlyAddsToOpenThreads(t *testing.T) {
	a := openApp(t, "linear")
	setInteractive(t, false)
	ctx := context.Background()
	if _, err := a.st.AllowOpening(ctx, "boss", []string{"github"}, "", "me"); err != nil {
		t.Fatal(err)
	}
	flags := append([]string{"--rig", "web", "--yes"}, starterFlags...)
	plain := openPack(t, "")
	if out, err := run(t, a.packCmd(), append([]string{"apply", plain}, flags...)...); err != nil || strings.Contains(out, "open threads") || bossMayOpen(t, a) != "github" {
		t.Fatalf("a pack without the key: %v, may open %q\n%s", err, bossMayOpen(t, a), out)
	}
	dir := openPack(t, "linear")
	if out, err := run(t, a.packCmd(), append([]string{"apply", dir, "--open-threads", "mayor=none"}, flags...)...); err != nil || bossMayOpen(t, a) != "github" {
		t.Fatalf("no: %v, may open %q\n%s", err, bossMayOpen(t, a), out)
	}
	if out, err := run(t, a.packCmd(), append([]string{"apply", dir, "--open-threads", "mayor=linear"}, flags...)...); err != nil || bossMayOpen(t, a) != "github,linear" {
		t.Fatalf("yes: %v, may open %q\n%s", err, bossMayOpen(t, a), out)
	}
}

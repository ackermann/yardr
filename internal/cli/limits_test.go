package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// limitsStandIn is a script in place of a provider's program, and the file
// of a source, quokka, that runs it for kimi and claude. The script writes
// a line to asked each time it is run, and then does body.
func limitsStandIn(t *testing.T, body string) (file, asked string) {
	t.Helper()
	dir := t.TempDir()
	asked = filepath.Join(dir, "asked")
	script := filepath.Join(dir, "provider")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$1\" >>"+asked+"\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(dir, "quokka.yaml")
	text := "name: quokka\nabout: A stand-in for a provider of limits.\nlimits:\n  command: [" + script + ", \"{provider}\", --json]\n  kinds: { kimi: moonshot, claude: claude }\n"
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
	out, err = run(t, a.limitsCmd(), "kimi")
	if err != nil {
		t.Fatalf("limits kimi: %q %v", out, err)
	}
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
	out, err = run(t, a.limitsCmd(), "kimi")
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
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || len(res.Limits) != 1 {
		t.Fatalf("limits kimi --json: %q %v", out, err)
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

// Without a kind, yardr limits asks for the kinds the yard runs on that a
// source lists, each once, and names those in use that none lists.
func TestLimitsOfTheKindsInUse(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "jo"}
	out, err := run(t, a.limitsCmd())
	if err != nil || !strings.Contains(out, "No source of this yard says limits: nothing is known, and nothing was asked.") {
		t.Fatalf("limits in a yard with no source: %q %v", out, err)
	}
	file, asked := limitsStandIn(t, kimiAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.limitsCmd())
	if err != nil || !strings.Contains(out, "Nothing was asked: no group, crew member or default_kind of this yard names a kind a source says the limits of (quokka for claude, kimi).") {
		t.Fatalf("limits with no kind in use: %q %v", out, err)
	}
	for name, kind := range map[string]string{"builders": "kimi", "reviewers": "codex", "more": "kimi"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	out, err = run(t, a.limitsCmd())
	if err != nil || !strings.Contains(out, "kimi  quokka says (provider Kimi):") || strings.Contains(out, "claude  ") ||
		!strings.Contains(out, "not asked: codex, in use here and listed by no source (limits.kinds)") {
		t.Fatalf("limits: %q %v", out, err)
	}
	if got := asks(t, asked); got != "moonshot" {
		t.Fatalf("asked = %q, want kimi's provider once: claude is not in use, codex is not listed", got)
	}
}

// A source that says limits is shown and listed with its command and
// kinds, and yardr source check asks for each kind the file lists.
func TestSourceWithLimitsShownAndChecked(t *testing.T) {
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

// doctor says which source answers for which kinds, a program that is not
// there and a kind in use that nothing lists, and asks no provider: that
// is --try's.
func TestDoctorLimitsPart(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "jo"}
	cfg := &config.Config{Home: t.TempDir()}
	uses := []harnessUse{{"kimi", []string{"group builders"}}, {"codex", []string{"the yard's default", "crew member scribe"}}}
	part, err := limitsPart(ctx, cfg, st, uses, true)
	if err != nil || part.Configured || len(part.Notes) != 0 || !strings.Contains(part.Detail, "no source says what a harness kind's subscription still carries") {
		t.Fatalf("no source: %+v %v", part, err)
	}
	file, asked := limitsStandIn(t, kimiAnswer())
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	part, err = limitsPart(ctx, cfg, st, uses, false)
	if err != nil || !part.Configured || !strings.HasPrefix(part.Detail, "quokka for claude, kimi; not asked (") || len(part.Notes) != 1 ||
		part.Notes[0] != "codex is in use (the yard's default, crew member scribe) and no source says its limits (limits.kinds)" {
		t.Fatalf("part = %+v %v", part, err)
	}
	if got := asks(t, asked); got != "" {
		t.Fatalf("doctor without --try asked the provider: %q", got)
	}
	part, err = limitsPart(ctx, cfg, st, uses, true)
	if err != nil || part.Detail != "quokka for claude, kimi" || len(part.Notes) != 2 ||
		!strings.HasPrefix(part.Notes[0], "tried: quokka answered for kimi: 2 windows; an agent of kind kimi waits for the 5h window, which resets ") {
		t.Fatalf("part under --try = %+v %v", part, err)
	}
	if got := asks(t, asked); got != "moonshot" {
		t.Fatalf("doctor --try asked %q, want the kind in use that is listed, once", got)
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
	if argv, _ := d.Limits.Argv("kimi"); strings.Join(argv, " ") != "/Users/mckean/go/bin/aiquokka kimi --json" {
		t.Fatalf("the command for kimi = %q", argv)
	}
	a := &app{st: storetest.New(t), actor: "jo"}
	out, err := run(t, a.sourceCmd(), "add", "testdata/aiquokka.yaml")
	if err != nil || !strings.Contains(out, "the subscription of claude, codex, copilot, grok, kimi, kiro still carries") {
		t.Fatalf("source add: %q %v", out, err)
	}
}

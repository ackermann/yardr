package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// styledYard is a yard made by yardr init with args after --harness claude
// --operator Jo, and a bead in it. It returns the app, the store and the
// bead's id.
func styledYard(t *testing.T, args ...string) (*app, store.API, string) {
	t.Helper()
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	if out, err := run(t, a.rootCmd(), append([]string{"init", "--harness", "claude", "--operator", "Jo"}, args...)...); err != nil {
		t.Fatalf("init %v: %v\n%s", args, err, out)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return a, st, b.ID
}

// primes are what yardr prime prints for the bead and for the yardmaster.
func primes(t *testing.T, st store.API, id string, asJSON bool) (bead, member string) {
	t.Helper()
	for i, args := range [][]string{{"--bead", id}, {"--crew", "yardmaster"}} {
		out, err := run(t, (&app{st: st, actor: "me", json: asJSON}).primeCmd(), args...)
		if err != nil {
			t.Fatalf("prime %v: %v\n%s", args, err, out)
		}
		if i == 0 {
			bead = out
		} else {
			member = out
		}
	}
	return bead, member
}

// The onboarding asks, after "Your name", how agents should work with the
// operator, each choice with its meaning on the page, and for a line about
// them. Accepted as it stands the page stores `show` and no note; answered,
// it stores the answers.
func TestOnboardingAsksTheStyle(t *testing.T) {
	var style, about string
	fields := styleFields(&style, &about)
	if len(fields) != 2 || fields[0].GetKey() != "style" || fields[1].GetKey() != "about" {
		t.Fatalf("the page's fields: %d", len(fields))
	}
	fields[0].Focus()
	page := fields[0].View()
	for _, s := range workStyles {
		if !strings.Contains(page, s.label) || !strings.Contains(page, s.means) {
			t.Errorf("the page lacks %q with its meaning %q:\n%s", s.label, s.means, page)
		}
	}
	if !strings.Contains(page, "How should agents work with you?") ||
		!strings.Contains(fields[1].View(), "Anything agents should know about you?") {
		t.Errorf("the page's questions:\n%s\n%s", page, fields[1].View())
	}

	for _, tc := range []struct {
		name    string
		answers map[string]any
		want    map[string]string
	}{
		{"defaults", nil, map[string]string{styleSetting: "show", noteSetting: ""}},
		{"answered", map[string]any{"style": "report", "about": "I review every merge"},
			map[string]string{styleSetting: "report", noteSetting: "I review every merge"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newHome(t)
			stubServices(t, "darwin")
			stubWeb(t)
			st := storetest.New(t)
			out, keys := onboardTo(t, st, tc.answers)
			if !slices.Equal(keys[:min(2, len(keys))], []string{"style", "about"}) {
				t.Fatalf("asked %q\n%s", keys, out)
			}
			wantText(t, out, "with whom agents work in the style "+tc.want[styleSetting])
			got, _ := st.Settings(context.Background())
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// yardr init stores --style and --about, and without them stores neither:
// only the form has a default.
func TestInitStyleFlags(t *testing.T) {
	_, st, _ := styledYard(t)
	if got, _ := st.Settings(context.Background()); got[styleSetting] != "" || got[noteSetting] != "" {
		t.Fatalf("init without --style: %v", got)
	}
	_, st, _ = styledYard(t, "--style", "work", "--about", " I read diffs, not prose ")
	if got, _ := st.Settings(context.Background()); got[styleSetting] != "work" || got[noteSetting] != "I read diffs, not prose" {
		t.Fatalf("init --style work --about: %v", got)
	}
}

// What is no style is refused with the four that are, by init before it
// creates anything and by settings set.
func TestStyleRefused(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), `operator_style "loud": there is guide, show, work, report`) {
			t.Errorf("%s: %v", what, err)
		}
	}
	_, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--style", "loud")
	refused("init --style loud", err)
	if rs, _ := st.Crew(ctx); len(rs) != 0 {
		t.Errorf("the refused init added %v", rs)
	}
	if got, _ := st.Settings(ctx); len(got) != 0 {
		t.Errorf("the refused init stored %v", got)
	}
	if _, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--about", "two\nlines"); err == nil {
		t.Error("a note of two lines accepted")
	}
	_, err = run(t, a.rootCmd(), "settings", "set", styleSetting, "loud")
	refused("settings set", err)
	if got, _ := st.Settings(ctx); got[styleSetting] != "" {
		t.Errorf("settings set stored %q", got[styleSetting])
	}
}

// Init again keeps the style and the note as it keeps operator, says so, and
// changes each only by its flag.
func TestInitAgainKeepsStyle(t *testing.T) {
	a, st, _ := styledYard(t, "--style", "guide", "--about", "new to git")
	ctx := context.Background()
	out, err := run(t, a.rootCmd(), "init")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.Settings(ctx)
	if got[styleSetting] != "guide" || got[noteSetting] != "new to git" {
		t.Fatalf("init again: %v", got)
	}
	if !strings.Contains(out, "operator_style guide, operator_note new to git, as set before") {
		t.Errorf("init again does not say what it kept:\n%s", out)
	}
	if _, err := run(t, a.rootCmd(), "init", "--style", "report", "--actor", "Jo"); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.Settings(ctx); got[styleSetting] != "report" || got[noteSetting] != "new to git" || got["operator"] != "Jo" {
		t.Fatalf("init --style report: %v", got)
	}
}

// Both primes name the operator and say the style as instructions, with the
// note when there is one, for every style; as JSON too.
func TestPrimesSayTheStyle(t *testing.T) {
	for _, s := range workStyles {
		for _, note := range []string{"", "I review every merge"} {
			args := []string{"--style", s.key}
			if note != "" {
				args = append(args, "--about", note)
			}
			_, st, id := styledYard(t, args...)
			bead, member := primes(t, st, id, false)
			for name, out := range map[string]string{"bead": bead, "crew": member} {
				wantText(t, out, "Jo", "`"+s.key+"`", `"`+s.label+`"`, s.tells)
				if has := strings.Contains(out, "What Jo wants agents to know: "+note); has != (note != "") {
					t.Errorf("%s prime, style %s, note %q:\n%s", name, s.key, note, out)
				}
			}
			wantText(t, bead, "\nOPERATOR Jo:\n  Jo asked agents")
			wantText(t, member, "\n## The operator: Jo\n\nJo asked agents")
			jbead, jcrew := primes(t, st, id, true)
			for _, out := range []string{jbead, jcrew} {
				var v struct {
					Operator operatorPrime `json:"operator"`
				}
				if err := json.Unmarshal([]byte(out), &v); err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				if want := (operatorPrime{Name: "Jo", Style: s.key, Label: s.label, Tells: s.tells, Note: note}); v.Operator != want {
					t.Errorf("operator = %+v, want %+v", v.Operator, want)
				}
			}
		}
	}
}

// A yard without the setting primes as it did before: no section, no
// operator in the JSON, no style assumed. A note alone is said without one.
func TestPrimesWithoutStyle(t *testing.T) {
	a, st, id := styledYard(t)
	bead, member := primes(t, st, id, false)
	jbead, jcrew := primes(t, st, id, true)
	for _, out := range []string{bead, member, jbead, jcrew} {
		if strings.Contains(out, "OPERATOR") || strings.Contains(out, "The operator") ||
			strings.Contains(out, `"operator"`) || strings.Contains(out, "asked agents") {
			t.Errorf("a yard without the setting says something about the operator:\n%s", out)
		}
	}
	if _, err := run(t, a.rootCmd(), "settings", "set", noteSetting, "I am on call this week", "--actor", "Jo"); err != nil {
		t.Fatal(err)
	}
	bead, member = primes(t, st, id, false)
	for _, out := range []string{bead, member} {
		wantText(t, out, "What Jo wants agents to know: I am on call this week")
		if strings.Contains(out, "asked agents") {
			t.Errorf("a note alone gave a style:\n%s", out)
		}
	}
}

// yardr settings set changes what the next prime prints, with no init: the
// primes read the setting, and no role file holds it.
func TestSettingsSetChangesThePrimes(t *testing.T) {
	a, st, id := styledYard(t, "--style", "guide")
	guide, _ := styleOf("guide")
	report, _ := styleOf("report")
	bead, member := primes(t, st, id, false)
	wantText(t, bead, guide.tells)
	wantText(t, member, guide.tells)
	if _, err := run(t, a.rootCmd(), "settings", "set", styleSetting, "report", "--actor", "Jo"); err != nil {
		t.Fatal(err)
	}
	bead, member = primes(t, st, id, false)
	for _, out := range []string{bead, member} {
		wantText(t, out, report.tells)
		if strings.Contains(out, guide.tells) {
			t.Errorf("the prime still says guide:\n%s", out)
		}
	}
	// Unset, the section is gone.
	if _, err := run(t, a.rootCmd(), "settings", "set", styleSetting, "", "--actor", "Jo"); err != nil {
		t.Fatal(err)
	}
	if bead, member = primes(t, st, id, false); strings.Contains(bead, "OPERATOR") || strings.Contains(member, "The operator") {
		t.Errorf("unset, the primes still have the section:\n%s\n%s", bead, member)
	}
}

// With operator_style guide the setup's pages run their commands and show no
// command line: not yardr's, not git's. With any other style they show them
// (TestSetupForms has no style at all).
func TestSetupGuideShowsNoCommands(t *testing.T) {
	for _, style := range []string{"guide", "work"} {
		t.Run(style, func(t *testing.T) {
			f := &former{answers: map[string]any{"title": "Say hello", "goal": "HELLO.md says hello."},
				goes: []string{goHere, goHere, goHere}}
			_, st, _ := setupYard(t, f)
			if _, err := run(t, (&app{st: st}).rootCmd(), "settings", "set", styleSetting, style, "--actor", yardOperator(context.Background(), st)); err != nil {
				t.Fatal(err)
			}
			out := setup(t, st)
			if next := todo(t, st); next != stepMerged {
				t.Fatalf("next step = %q, want the merged bead:\n%s", next, out)
			}
			if shown := strings.Contains(out, "$ "); shown != (style != "guide") {
				t.Errorf("style %s: command lines shown = %v\n%s", style, shown, out)
			}
		})
	}
}

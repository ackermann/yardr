package cli

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A fresh yard's yardmaster still primes with its First run section: the
// setup's steps are its job until every step is done.
func TestPrimeCrewKeepsFirstRunWhileSetupTodo(t *testing.T) {
	st := userYard(t)
	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	for _, want := range []string{"## First run", "Ask for the repository's path", "yardr depot init"} {
		if !strings.Contains(out, want) {
			t.Errorf("a fresh yard's prime lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, setupDoneLine) {
		t.Errorf("a fresh yard's prime says the setup is done:\n%s", out)
	}
}

// Once the setup is complete, the yardmaster's prime prints one line
// where its First run section was: the section is read at every start
// of every session, and a set-up yard has no use for it.
func TestPrimeCrewReplacesFirstRunWhenSetupComplete(t *testing.T) {
	// As userYard but without its chdir: gitDepot reads testdata by
	// path, which a chdir away from the package leaves behind.
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	for _, k := range []string{"ACTOR", "BEAD", "SESSION", "CREW"} {
		t.Setenv("YARDR_"+k, "")
	}
	st := storetest.New(t)
	if out, err := run(t, (&app{st: st, actor: "jo"}).rootCmd(), "init", "--harness", "claude", "--operator", "jo"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	ctx := context.Background()
	fresh := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")

	depot := gitDepot(t, st)
	commitDepot(t, depot)
	for _, g := range []store.Group{
		{Name: "web/builders", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "web/reviewers", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "web/mergers", Runner: "manual"},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for stage, group := range map[string]string{"new": "web/builders", "review": "web/reviewers", "approved": "web/mergers"} {
		if _, err := st.AddRoute(ctx, store.Route{Stage: stage, Depot: &depot.Name, Group: group}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "first", Depot: depot.Name}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"new", "review", "approved", "merged"} {
		if _, err := st.Advance(ctx, b.ID, stage, store.Expect{}, "me"); err != nil {
			t.Fatalf("to %s: %v", stage, err)
		}
	}
	if steps, err := setupSteps(ctx, st); err != nil || nextStep(steps) != nil {
		t.Fatalf("the yard is not set up: %+v, %v", steps, err)
	}

	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	if !strings.Contains(out, setupDoneLine) {
		t.Errorf("a set-up yard's prime lacks the setup-done line:\n%s", out)
	}
	for _, not := range []string{"## First run", "Ask for the repository's path", "yardr depot init"} {
		if strings.Contains(out, not) {
			t.Errorf("a set-up yard's prime still holds %q:\n%s", not, out)
		}
	}
	// Shorter by about the section (some 4 KB), not by a line.
	if d := len(fresh) - len(out); d < 3000 {
		t.Errorf("a set-up yard's prime is shorter by %d bytes, want over 3000", d)
	}
	// The rest of the role is still there, around the line.
	if i, j, k := strings.Index(out, "You coordinate the work"), strings.Index(out, setupDoneLine), strings.Index(out, "## The work"); i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Errorf("the line is not where the section was:\n%s", out)
	}
	// JSON carries the same role text.
	js := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster", "--json")
	if !strings.Contains(js, setupDoneLine) || strings.Contains(js, "## First run") {
		t.Errorf("prime --json keeps the section:\n%s", js)
	}
}

// withoutFirstRun works on a role file a yard has edited: without the
// heading it changes nothing, and whatever the section holds is replaced
// by the one line.
func TestWithoutFirstRun(t *testing.T) {
	const edited = "# Yardmaster\n\nYou coordinate the work.\n\n## The work\n\nTriage.\n"
	if got := withoutFirstRun(edited); got != edited {
		t.Errorf("a role without the section changed:\n%s", got)
	}
	const own = "# Yardmaster\n\nYou coordinate.\n\n## First run\n\nAsk them nicely.\n\n## The work\n\nTriage.\n"
	want := "# Yardmaster\n\nYou coordinate.\n\n" + setupDoneLine + "\n\n## The work\n\nTriage."
	if got := withoutFirstRun(own); got != want {
		t.Errorf("an edited section:\n%s\nwant:\n%s", got, want)
	}
	// Last in the file, and with no blank line around the heading.
	const last = "# Yardmaster\n\n## First run\n\nRead yardr doctor.\n"
	if got, want := withoutFirstRun(last), "# Yardmaster\n\n"+setupDoneLine; got != want {
		t.Errorf("the section last:\n%s\nwant:\n%s", got, want)
	}
}
